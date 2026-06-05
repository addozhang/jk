## Why

Jenkins is sometimes deployed as multiple independent instances behind a single reverse-proxy host, distinguished only by a context-path prefix (e.g. `https://ci.example.com/team-a/` and `https://ci.example.com/team-b/`), each with its own accounts and API tokens. `jk` keys credentials by scheme + host only, so these instances collide on one credential entry: the second `jk auth add` silently overwrites the first, and there is no way to authenticate to both at once.

## What Changes

- `jk auth add <url>` accepts an optional context-path prefix and stores it as part of the credential key (scheme + host + normalized base path) instead of discarding the path. A bare host argument behaves exactly as today.
- Credential lookup resolves a request URL to the **most specific** stored key via segment-boundary longest-prefix matching, falling back to a host-only entry when no context-path entry matches.
- CSRF crumb acquisition uses the same resolved key so per-instance crumbs do not collide on a shared host.
- Existing host-only credential files keep working unchanged — a host-only key is the shortest valid prefix and still matches. **Non-breaking.**
- `jk auth list` displays context-path keys verbatim alongside host-only keys.

## Capabilities

### New Capabilities

(none)

### Modified Capabilities

- `auth`: the credential key extends from scheme + host to scheme + host + optional base path; lookup changes from exact-host match to segment-boundary longest-prefix match with host-only fallback; CSRF crumb host-keying reuses the resolved key.

## Impact

- Code: `internal/cli/auth.go` (`normalizeAuthHost` retains and normalizes the base path), `internal/auth/store.go` (new longest-prefix `Resolve`), `internal/jenkins/transport.go` (auth lookup at the RoundTripper), `internal/jenkins/crumb.go` (crumb host key).
- Reuses the `jenkinsurl` base-path extraction semantics (the prefix before the first `/job/` segment) so `auth add` and request-time resolution agree.
- Storage format is unchanged (still a host → credential map ordered by insertion); only the key contents gain an optional path. Backward compatible — no migration.
- Docs: README auth section and `docs/schema.md` credential-key description.
