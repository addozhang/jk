## 1. Store: segment-boundary longest-prefix `Resolve` (D2, D6)

- [x] 1.1 RED: add table-driven tests for `auth.Store.Resolve(*url.URL)` in `internal/auth/store_test.go` covering: host-only match, most-specific context-path entry wins, fallback to host-only when no context entry matches, segment-boundary rejection (`/team-a` must not match `/team-amber`), exact-path match, and no-match returns `ok=false`
- [x] 1.2 GREEN: implement `Resolve` on `fileStore` performing the segment-boundary longest-prefix match over stored keys; keep `Get`/`Add`/`List`/`Remove` exact-key and unchanged; add `Resolve` to the `Store` interface
- [x] 1.3 Verify `go test ./internal/auth/...` passes

## 2. `auth add`/`remove` base-path capture and echo (D3)

- [x] 2.1 RED: extend `internal/cli/auth_test.go` for `normalizeAuthHost` — host-only cases (trailing slash, `/job/whatever`) stay `scheme://host`; context-path cases (`/team-a`, `/team-a/job/svc/`, multi-segment `/team/ci`) retain the normalized base path; confirm `auth add` confirmation message names the full key and `auth remove` resolves the same normalized key
- [x] 2.2 GREEN: update `normalizeAuthHost` (`internal/cli/auth.go`) to retain a normalized base path, reusing the `jenkinsurl` base-path extraction (prefix before first `/job/`, else whole path; leading `/`, no trailing `/`, empty → host-only); update `runAuthAdd` confirmation to echo the stored key
- [x] 2.3 Verify `go test ./internal/cli/...` passes

## 3. Auth injector resolves via `Resolve` (D1, D6)

- [x] 3.1 RED: add tests in `internal/jenkins/transport_test.go` — a request to a context-path URL selects the most-specific credential; falls back to a host-only entry; a no-match request leaves `Authorization` unset; a pre-set `Authorization` is still not overridden
- [x] 3.2 GREEN: change `authInjector.RoundTrip` to call `a.creds.Resolve(req.URL)` instead of `Get(hostKeyFromURL(req.URL))`
- [x] 3.3 Verify `go test ./internal/jenkins/...` passes for the transport tests

## 4. Crumb endpoint and cache key follow the resolved credential (D5)

- [x] 4.1 RED: add tests in `internal/jenkins/crumb_test.go` — crumb is fetched from the context-path endpoint (`/team-a/crumbIssuer/api/json`) when the matched key carries `/team-a`; two instances on one host (`/team-a`, `/team-b`) keep independent cached crumbs; with no matching credential the endpoint and cache key fall back to the host root (existing behavior preserved)
- [x] 4.2 GREEN: give the crumb layer access to the credential store; resolve the request URL, derive the base path from the matched key, build the endpoint as `scheme://host + basePath + /crumbIssuer/api/json` (replacing the hard-coded `u.Path = "/crumbIssuer/api/json"`), and key the cache (`get`/`set`/`invalidate`) on the resolved credential key
- [x] 4.3 Verify `go test ./internal/jenkins/...` passes (full package, including existing crumb and transport suites)

## 5. Documentation

- [x] 5.1 Update the README auth section: `jk auth add` accepts an optional context path; explain most-specific longest-prefix resolution with host-only fallback; add a same-host multi-instance example
- [x] 5.2 Update `docs/schema.md` credential-key description to reflect scheme + host + optional base path and the resolution rule

## 6. End-to-end and validation

- [x] 6.1 Add an e2e (`test/e2e`, build tag `e2e`) that seeds a context-path-scoped credential keyed to the `/jenkins` mount and asserts an authorized build-status call resolves through it; assert a host-only credential still resolves for a root request
- [x] 6.2 Run `make test-unit` (race) — all internal packages green
- [x] 6.3 Run `make lint` — 0 issues
- [x] 6.4 Mark every task complete and run `openspec validate auth-context-path-credentials --strict`
