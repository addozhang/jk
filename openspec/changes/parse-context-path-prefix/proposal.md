## Why

Jenkins is frequently deployed under a URL **context path** rather than at the host root — `https://ci.example.com/jenkins/job/...` is the single most common reverse-proxy layout, and corporate installs nest deeper still (`https://example.com/domain/job/abc/job/.../5.3.39-atlassian-4/2/`). Today `jk` rejects every such URL with `not a Jenkins job URL (no /job/ segments found)` because the parser requires the path to begin with `job/`. The error is doubly confusing because the URL plainly contains several `/job/` segments — the parser bails the moment it sees the leading prefix segment and never inspects them. This makes `jk` unusable against any Jenkins that is not mounted at `/`, which contradicts the project's URL-as-identity contract: any URL Jenkins itself emits should be accepted.

## What Changes

- The parser recognizes an optional **context-path prefix**: any leading path segments before the first `/job/` are captured as the instance base path instead of triggering rejection. `https://host/jenkins/job/svc/42/` and `https://host/a/b/job/svc/42/` now parse successfully.
- `Ref` gains a `BasePath string` field holding the prefix exactly as it appeared in the URL (leading slash, no trailing slash; empty for root-mounted instances). It is preserved verbatim — not URL-decoded/re-encoded — so it round-trips byte-for-byte.
- `Ref.APIPath` emits `BasePath` immediately after `Host` and before the first `/job/`, so every existing caller that builds `<host>/job/<segs>/<suffix>` automatically produces `<host><basepath>/job/<segs>/<suffix>` and hits the correct endpoint.
- The credential-lookup key (`Ref.Host` / `HostKey`) is **unchanged** — it remains scheme + host with default ports stripped. Credentials are keyed per host, not per context path, matching how the auth injector already resolves them (`transport.go` keys on `req.URL` host only). No re-auth or config migration is required.
- URLs that contain no `/job/` segment at all (e.g. `/view/All/`, bare host) continue to be rejected with the existing "not a Jenkins job URL" error. Empty-job-segment and other malformed-shape errors are unchanged.
- No **BREAKING** changes: root-mounted URLs parse to `BasePath == ""` and render bit-identically to today.

## Capabilities

### New Capabilities
<!-- none -->

### Modified Capabilities
- `url-resolution`: the parser accepts a context-path prefix preceding the first `/job/` segment, captures it on a new `BasePath` field, and emits it when formatting API URLs. Host-to-credential mapping is explicitly unaffected.

## Impact

- `internal/jenkinsurl/parse.go` — capture leading prefix in `extractJobSegments`; add `Ref.BasePath`; teach `APIPath` to emit it; thread the prefix through `Parse`.
- `internal/jenkinsurl/parse_test.go` — add context-path cases (prefix + build number, prefix + permalink, multi-segment prefix, round-trip through `APIPath`, root-mounted still `BasePath == ""`).
- `docs/spikes/urls.txt` — extend the conformance corpus with context-path shapes if present.
- `docs/schema.md` / `README.md` — note that context-path-mounted Jenkins URLs are accepted.
- No new dependencies. No changes to `internal/jenkins/client.go` callers (they already go through `APIPath`); the one `Ref.Host + proceedURL` path in `client.go` stays correct because server-supplied input URLs are already absolute from the host root.
