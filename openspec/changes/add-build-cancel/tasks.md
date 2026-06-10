## 1. Jenkins Client

- [x] 1.1 Add `CancelBuild(ctx, ref)` method to `internal/jenkins/client.go` — `POST ref.APIPath("stop")`, drain body, return `HTTPStatusError` on non-2xx
- [x] 1.2 Add `CancelBuild` to the `buildClientSurface` interface in `build.go`
- [x] 1.3 Add unit tests for `CancelBuild` in `internal/jenkins/client_wfapi_test.go` (200 OK, 404 not found)

## 2. Schema

- [x] 2.1 Add `BuildCancel` output type to `internal/schema/types.go` with fields `SchemaVersion`, `BuildURL`, `BuildNumber`, `State`
- [x] 2.2 Add `MapBuildCancel(statusBody []byte) (*BuildCancel, error)` mapper in `internal/schema/mapper_build.go`
- [x] 2.3 Add unit tests for `MapBuildCancel`

## 3. CLI Command

- [x] 3.1 Add `newBuildCancelCommand` and `runBuildCancel` in `internal/cli/build.go`
- [x] 3.2 Register `newBuildCancelCommand` in `newBuildCommand`
- [x] 3.3 Implement `--wait` flag — call `watchBuild` after successful POST
- [x] 3.4 Add unit tests for `runBuildCancel` in `internal/cli/build_test.go` (happy path, 404, --wait)

## 4. Spec Delta

- [x] 4.1 Update `openspec/specs/build/spec.md` — merge the MODIFIED requirement for watch exit codes (applied at archive time)
- [x] 4.2 Create `openspec/specs/build-cancel/spec.md` from the change spec (applied at archive time)

## 5. E2E

- [x] 5.1 Add `Test_E2E_BuildCancel_*` cases to `test/e2e/build_e2e_test.go` — trigger a build, cancel it, assert `result: ABORTED`
- [x] 5.2 Add `--wait` e2e case — cancel with `--wait`, assert exit code 3
