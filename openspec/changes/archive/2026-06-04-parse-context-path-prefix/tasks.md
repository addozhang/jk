## 1. Parser: capture context-path prefix (RED → GREEN)

- [x] 1.1 Add failing table-driven cases to `internal/jenkinsurl/parse_test.go` covering: single-segment prefix with build number (`/domain/job/abc/job/svc/2/`), `/jenkins` prefix on a bare job, multi-segment prefix (`/team/ci/...`), prefix + permalink trailing segment, and root-mounted URL asserting `BasePath == ""`. Confirm they fail against the current implementation.
- [x] 1.2 Add `BasePath string` field to `Ref` in `internal/jenkinsurl/parse.go` with a doc comment: verbatim (not decoded), leading `/`, no trailing `/`, empty when root-mounted; explicitly not part of `HostKey`.
- [x] 1.3 Rework `extractJobSegments` to locate the index of the first part exactly equal to `"job"`; treat the segments before it as the prefix; run the existing `job`/`<name>` alternation walk from that index. Return the prefix as an additional value.
- [x] 1.4 Preserve current rejections: no `"job"` token anywhere → return the "not a Jenkins job URL" sentinel (empty segments); empty/missing name slot after the prefix → keep the "empty job segment" error.
- [x] 1.5 Wire the prefix return value through `Parse` into `Ref.BasePath` (join the prefix parts with `/`, prepend `/`; empty string when there is no prefix).
- [x] 1.6 Run `go test ./internal/jenkinsurl/...` until the new and all existing parse tests pass.

## 2. Builder: emit BasePath in APIPath (RED → GREEN)

- [x] 2.1 Add failing cases to `Test_Ref_APIPath` (and a permalink case to `Test_Ref_APIPath_Permalink`) asserting `BasePath` is emitted after the host and before the first `/job/`, and that an empty `BasePath` renders identically to today.
- [x] 2.2 Update `Ref.APIPath` in `internal/jenkinsurl/parse.go` to write `BasePath` immediately after `Host`.
- [x] 2.3 Extend `Test_Parse_RoundTripsThroughAPIPath` with at least two context-path URLs (with and without a trailing build number) and confirm byte-for-byte round-trip.
- [x] 2.4 Run `go test ./internal/jenkinsurl/...`; confirm green.

## 3. Guard credential keying and existing callers

- [x] 3.1 Add a parse test asserting `HostKey()` for a context-path URL equals scheme+host only (e.g. `https://example.com/jenkins/job/svc/` → `https://example.com`).
- [x] 3.2 Verify no `internal/jenkins/client.go` caller needs changes: all networked requests go through `APIPath`; confirm the `ref.Host + proceedURL` input-submit path (around `client.go:445`) stays correct (server-supplied `proceedUrl` already includes the context path) — add a brief inline comment if the invariant is non-obvious.
- [x] 3.3 Run `go test ./internal/jenkins/...` to confirm no regression.

## 4. Conformance corpus & docs

- [x] 4.1 If `docs/spikes/urls.txt` exists, add the context-path shapes (single-segment, multi-segment, `/jenkins`, prefix+permalink, prefix+no-`/job/` rejection) with annotations matching the existing format.
- [x] 4.2 Add a short note to `README.md` (URL forms accepted) and `docs/schema.md` (URL-resolution section) that Jenkins instances mounted under a context path are supported and that the context path does not affect credential keying.

## 5. Release gate

- [x] 5.1 Run `make test-unit && make lint` (or `go test ./... && go vet ./...`) for every touched package; confirm green.
- [x] 5.2 Manual dogfood: run `jk build status` against a context-path build URL (e.g. `https://example.com/domain/job/abc/.../2/`) and confirm it parses and reaches the correct endpoint instead of the "no /job/ segments found" error.
- [x] 5.3 Update the change status and run `openspec validate parse-context-path-prefix` (or `openspec status`) before archiving.
