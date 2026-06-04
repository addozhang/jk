## Context

`jenkinsurl.Parse` is the single canonical parser for every Jenkins URL `jk` touches. It produces a `Ref{Host, JobSegments, BuildNumber, BuildPermalink}` and a companion `APIPath(suffix)` that re-renders the URL for HTTP requests. The parser assumes Jenkins lives at the host root: `extractJobSegments` requires `parts[0] == "job"` and the walk loop requires the `job`/`<name>` alternation to begin at index 0 (`parse.go:190`, `parse.go:197-200`). Any path that starts with a prefix segment — i.e. Jenkins mounted under a context path such as `/jenkins` or `/domain` — returns zero segments and surfaces as `not a Jenkins job URL (no /job/ segments found)`.

Context-path deployments are mainstream (reverse-proxy `/jenkins`, multi-tenant `/domain`). Credentials are already resolved host-only: the auth injector computes its lookup key from `req.URL` scheme+host with default ports stripped (`transport.go:269`), never consulting the path. So the credential model needs no change; only URL parsing and re-rendering do.

## Goals / Non-Goals

**Goals:**
- Accept any Jenkins job/build URL whose `/job/` path is preceded by an arbitrary (possibly multi-segment) context path.
- Re-render the exact same context path in `APIPath` so requests hit the real endpoint.
- Preserve byte-for-byte round-tripping (`Parse(u).APIPath("") == u` up to existing host normalization).
- Keep all current behavior for root-mounted URLs identical, including every rejection case that has no `/job/` token.

**Non-Goals:**
- Changing the credential-lookup key to include the context path. Credentials stay keyed on host.
- Interpreting or validating the context path (no allowlist, no decoding semantics) — it is opaque pass-through.
- Supporting a context path whose final segment is literally `job` (pathological; not a real Jenkins layout).

## Decisions

### D1: Define the context path as "everything before the first `/job/` token"
`extractJobSegments` finds the index of the first part exactly equal to `"job"`. Everything before it is the prefix; the existing alternation walk runs from that index. If no `"job"` token exists, the path is not a job URL and the current `(nil, 0, "", nil)` → "not a Jenkins job URL" path is preserved unchanged.

- *Why:* Jenkins addressable job paths always begin at the first `/job/`. The first `job` token is therefore an unambiguous delimiter between the instance base path and the job hierarchy.
- *Alternatives considered:* (a) A known-prefix allowlist (`/jenkins`, `/ci`, …) — rejected; context paths are operator-chosen and unbounded. (b) Single-segment prefixes only — rejected; proxies nest (`/team/ci/jenkins/...`).

### D2: Store the prefix verbatim on a new `Ref.BasePath`, not decoded
`BasePath` holds the raw substring between the host and the first `/job/`, normalized to a leading `/` with no trailing `/` (empty string for root-mounted instances). It is **not** URL-decoded and re-encoded the way `JobSegments` are.

- *Why:* `JobSegments` are decoded because callers display them and the builder re-encodes each segment; the base path is never displayed or interpreted — it must be reproduced exactly to address the server. Verbatim pass-through guarantees a faithful round trip with zero encoding assumptions.
- *Alternative:* Model the prefix as a `[]string` of decoded segments — rejected; adds encode/decode surface for a value we only ever echo back.

### D3: `APIPath` emits `Host` + `BasePath` + `/job/…`
The builder inserts `BasePath` immediately after `Host`. Because every networked caller already routes through `APIPath` (`client.go` build/status/stages/logs/input/params/wfapi), they inherit context-path correctness with no per-call changes.

- *Note:* The one non-`APIPath` request — `endpoint = ref.Host + proceedURL` for input submit (`client.go:445`) — stays correct: Jenkins returns `proceedUrl` as an absolute path already including its own context path, so prepending pure `Host` (not `Host+BasePath`) is exactly right. This is a reason to keep `Host` context-path-free (reinforces D4).

### D4: Leave `Host` / `HostKey` and the credential model untouched
`Host` remains scheme + lowercase host + non-default port. `HostKey()` is unchanged, so stored credentials resolve identically.

- *Why:* The auth injector already keys on host only; folding `BasePath` into `Host` would break that match and force users to re-add credentials per context path, and would corrupt the `Host + proceedURL` join in D3.

### D5: Trailing build/permalink detection is unchanged and order-independent
The existing trailing-segment probe (numeric build number / permalink) operates on the tail of `parts` and is unaffected by a leading prefix, so it runs first exactly as today; prefix extraction happens afterward on the remaining head.

## Risks / Trade-offs

- **Looser acceptance of non-job URLs that happen to contain `/job/`** (e.g. `/view/All/job/svc` now parses with `BasePath=/view/All` where it was previously rejected) → Mitigation: a valid `/job/<name>` pair is still required, so the realistic failure mode is a request that the server rejects, not a silent wrong answer. URLs with no `/job/` token at all remain rejected as before. Net surface is strictly job-shaped paths.
- **Context path segment literally `job`** (`/job/job/svc`) is interpreted as a root-mounted job hierarchy, not a `/job` context path → Mitigation: documented Non-Goal; no real Jenkins uses `/job` as its context path.
- **Empty-job-segment diagnostics** must keep firing for malformed job pairs after a prefix (`/x/job//job/svc`) → covered by spec scenarios and tests; the walk still validates each pair from the first `job` index.
