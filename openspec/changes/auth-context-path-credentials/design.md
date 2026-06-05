## Context

`jk` resolves credentials host-only. The auth injector computes its lookup key from the request URL as scheme + lowercase host + non-default port, discarding the path (`transport.go:253` → `hostKeyFromURL`, `transport.go:269-282`), then does an exact map lookup against the store. The CSRF crumb subsystem keys its cache the same host-only way (`crumb.go:210`) and, worse, hard-codes the crumb endpoint to the host root — `u.Path = "/crumbIssuer/api/json"` (`crumb.go:128`) — dropping any context path, so writes (POST/PUT/DELETE) against a context-path instance fetch the crumb from the wrong place. On the write side, `normalizeAuthHost` strips path/query/fragment and persists only scheme + host (`auth.go:300`). The store itself is a plain `Hosts map[string]Credential` with an insertion `Order` slice (`store.go:231-243`). The transport stack is `base → authInjector{creds} → [crumb] → [debug]` (`transport.go:153-160`); `authInjector` holds the store, and the crumb layer fetches through an auth-wrapped client.

The just-shipped `parse-context-path-prefix` change added `Ref.BasePath` (the prefix before the first `/job/`) but deliberately left the credential model host-only (its D4 / Non-Goals). This change addresses the exact scenario that left open: multiple independent Jenkins instances behind one host, separated by a context path (`https://ci.example.com/team-a/`, `https://ci.example.com/team-b/`), each with distinct tokens. Today they collide on a single host key and the second `auth add` silently overwrites the first.

## Goals / Non-Goals

**Goals:**
- Let `jk auth add <url>` retain a context-path prefix so two instances on one host can hold distinct credentials.
- Resolve a request URL to the most specific stored credential, falling back to a host-only entry when no context-path entry matches.
- Keep every existing host-only credential file working unchanged — no migration, no re-add.
- Route CSRF crumb fetches to the instance's actual context path and cache them per resolved key, so writes succeed against a context-path instance and per-instance crumbs do not collide.

**Non-Goals:**
- Changing the on-disk storage format or introducing named profiles / contexts (kubectl-style). The store stays a host→credential map.
- Fuzzy, regex, or case-insensitive path matching. Path matching is exact and segment-bounded.
- Interpreting the context path beyond what `jenkinsurl` already extracts; it is opaque pass-through, reusing existing base-path semantics.

## Decisions

### D1: Resolve by matching stored keys against the request URL (not by deriving a context path from the request)

Credential resolution iterates the stored keys and selects the longest one that is a segment-boundary prefix of the request URL, rather than parsing a context path out of the incoming request and looking it up exactly.

- *Why:* At the RoundTripper and crumb layers the request URL is an arbitrary API sub-path — `…/team-a/job/x/api/json`, but also `…/team-a/crumbIssuer/api/json` or `…/team-a/api/json`, none of which contain a reliable `/job/` delimiter. You cannot robustly recover where the context path ends from such a URL. The stored keys, by contrast, are exact: each one either is or is not a prefix of the request, so matching is deterministic and needs no guessing.
- *Bonus:* A host-only key (empty path) is automatically the shortest valid prefix of any same-host URL, so legacy entries keep matching with zero special-casing — backward compatibility falls out of the same rule.
- *Alternatives considered:* (a) Thread the already-parsed `Ref.HostKey + BasePath` from the command layer down into the transport as an injected key — workable but invasive: it routes auth state through every request-construction path and the crumb RoundTripper. (b) Re-extract the base path from the request URL via the first `/job/` — fails for crumb and bare `api/json` requests that have no `/job/`.

### D2: Segment-boundary longest-prefix match

A stored key `K` matches request URL `U` iff `K.scheme` and `K.host` equal `U`'s, and `U.path` either equals `K.path` or begins with `K.path + "/"`. Among all matching keys, pick the one with the longest `K.path`. A host-only key has empty path and matches at the `/` boundary.

- *Why the boundary check:* Plain string-prefix would let `https://h/jenkins` wrongly match `https://h/jenkins-other/...`. Requiring the next character to be `/` (or exact equality) confines matches to whole path segments.
- *Determinism:* Longest-path wins, so `https://h/team-a` beats `https://h` for a `/team-a/...` request; ties are impossible because two equal paths are the same key.

### D3: `auth add` extracts and normalizes the base path with the same rule as request resolution

`normalizeAuthHost` keeps scheme + host + non-default port and now also captures a base path, reusing the `jenkinsurl` base-path semantics: the prefix before the first `/job/` segment, or the entire path when there is no `/job/`. It is normalized to a leading `/` with no trailing `/`; an empty result yields a pure host key as before. The confirmation message echoes the full key so the user sees exactly what was stored.

- *Why:* The key written by `auth add` must land on the same segment boundary that resolution computes, or a stored context-path entry would never match. Anchoring both sides on the identical base-path rule guarantees agreement.
- *Consequence (intentional):* `auth add https://h/job/x` still stores `https://h` (empty base path — `/job/x` is job hierarchy, not context), identical to today. But `auth add https://h/ci` now stores `https://h/ci` rather than collapsing to `https://h`. This is the point of the feature, and the echoed key makes it visible.

### D4: Storage format unchanged; resolution is additive

The store stays `Hosts map[string]Credential` + `Order`. Keys may now contain a path, but that is just a longer string. `Add`/`Get`/`List`/`Remove` keep exact-key semantics (used by `auth add`'s overwrite check and `auth remove`); only a new `Resolve(requestURL)` method performs longest-prefix matching, and only the transport and crumb layers call it. Old files are entirely host-only keys and resolve exactly as before.

### D5: Crumb fetches and caches by the resolved credential key

The crumb layer resolves the request URL against the store (D6) and uses the matched key's base path for both the endpoint and the cache key: the crumb is fetched from `scheme://host + basePath + /crumbIssuer/api/json` instead of the hard-coded root path, and cached under the full resolved key instead of `hostKeyFromURL(req.URL)`. Two instances on one host therefore fetch from their own `/crumbIssuer` and keep independent crumb entries.

- *Why fetch-path too, not just the cache key:* with the endpoint pinned to root, a write against `https://h/team-a/...` fetches the crumb from `https://h/crumbIssuer/...` — a 404 (CSRF treated as disabled) or another instance's crumb (403). Caching alone would not make writes work; the endpoint must follow the context path.
- *Anonymous fallback:* when no credential matches, the resolver returns the host-only key with an empty base path, so the endpoint and cache key collapse to today's behavior. A context-path instance that has *no* configured credential and *does* enforce CSRF is therefore not corrected — see Risks. This is acceptable because the feature is about instances you have authenticated to: a matched credential carries the base path the crumb needs.

### D6: A single `Resolve` entry point shared by auth and crumb

Add `auth.Store.Resolve(reqURL *url.URL) (key string, c Credential, ok bool, err error)` implementing the D2 segment-boundary longest-prefix match. The `err` return mirrors the rest of the `Store` interface (every method surfaces load/parse failures rather than masking a malformed file as "no credential"). Both `authInjector` (for the `Authorization` header) and the crumb layer call it, guaranteeing the token injected and the crumb endpoint agree on the same instance. The existing exact-key `Get` is retained for `auth add`'s overwrite check and `auth remove`; only `Resolve` does prefix matching, and only the transport and crumb layers call it. The crumb layer gains a reference to `opts.Credentials` at construction so it can resolve independently of the auth injector.

- *Why a store method, not a free function:* the match must iterate the store's keys; co-locating it with the data keeps the `Order`/`Hosts` representation private and lets `Get`/`List`/`Remove` keep their exact-key contract untouched.

## Risks / Trade-offs

- **Behavior change for `auth add <url-with-non-job-path>`**: a stray path that is not job hierarchy (e.g. `https://h/dashboard`) now becomes part of the key instead of being discarded → Mitigation: the confirmation message prints the stored key verbatim; documented in the `auth add` spec scenario and README so the result is never silent.
- **Two entries on one host (host-only + context-path) coexisting**: requests must route to the longest match and fall back correctly → Mitigation: covered by explicit longest-prefix and fallback scenarios/tests; the boundary rule (D2) prevents cross-segment leakage.
- **Resolution cost grows with entry count** (linear scan vs. map lookup) → Mitigation: credential stores hold a handful of hosts; a linear scan with longest-match tracking is negligible and avoids any index.
- **Anonymous context-path instance with CSRF enabled**: with no matching credential the crumb endpoint falls back to root and writes may still fail → Mitigation: documented Non-Goal-adjacent limitation; the realistic remedy (configure a credential for that instance) is exactly this feature's flow, and read-only commands are unaffected.
- **`auth remove` / overwrite must use the same normalized key** as `auth add` → Mitigation: both go through the shared `normalizeAuthHost`, so exact-key operations stay consistent.

## Migration Plan

No migration. The on-disk format is unchanged and existing host-only keys are valid longest-prefix matches under the new resolver. Rollback is safe: an older binary ignores any context-path keys (they simply never match its host-only lookup and sit inert in the file), and the format it writes is still readable by the new binary.

## Open Questions

None blocking. A future `jk auth list` could group entries by host for readability, but the flat verbatim listing is sufficient for this change.
