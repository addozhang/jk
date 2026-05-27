## 1. Schema and Client Surface

- [x] 1.1 In `internal/schema/types.go`, update the doc comment on `PendingInput.Parameters` to promote stability from `experimental` to `stable`. Mirror the change in `docs/schema.md §3.9`.
- [x] 1.2 In `internal/jenkins/client.go`, change `SubmitInput`'s signature to `SubmitInput(ctx, ref, inputID string, proceed bool, proceedText string, parameters []InputParameterValue) error` where `InputParameterValue{Name, Value string}` is a new exported struct in the same file. _(proceedText added during integration — Jenkins's `/submit` endpoint requires the input step's `ok` label in a `proceed=<text>` form field; without it Jenkins records "Rejected by <user>" and aborts the build.)_
- [x] 1.3 Endpoint selection inside `SubmitInput`: if `proceed == false` → `abort` (ignore parameters). If `proceed == true` AND `len(parameters) == 0` → `proceedEmpty` (v0.1 path, untouched). If `proceed == true` AND `len(parameters) > 0` → build a `submit` POST with body `Content-Type: application/x-www-form-urlencoded`, body `json=<URL-encoded JSON of {"parameter":[...]}>&proceed=<URL-encoded proceedText>`. Use `encoding/json` for the inner JSON and `net/url` for the form encoding.
- [x] 1.4 Add unit tests in `internal/jenkins/client_wfapi_test.go`: one for the `proceedEmpty` path (unchanged, regression guard), one for the `submit` path asserting the exact wire-format body, and one for the `abort` path asserting parameters are ignored.

## 2. CLI Parameter Parsing and Validation

- [x] 2.1 In `internal/cli/build.go`, extract the existing `-p KEY=VALUE` parsing logic from `newBuildTriggerCommand` into a package-private helper `parseParamFlags(flags []string) (map[string]string, error)` so it can be shared. Preserve `@file` semantics (read file when value starts with `@`). _(Already done in v0.1 — helper lives in `common.go:193`.)_
- [x] 2.2 Add `-p` / `--param` repeatable string-slice flag to `newBuildInputCommand`. Wire it through `runBuildInput(cmd, flags, rawURL, action, inputID, paramFlags)`.
- [x] 2.3 After fetching pending inputs (existing code at `runBuildInput` line ~348) and picking the input ID, fetch the chosen input's `Parameters []Parameter` from the decoded `pendingInputItem` (extend `pendingInputItem` decoding if needed).
- [x] 2.4 Implement `validateInputParameters(declared []Parameter, supplied map[string]string) ([]jenkins.InputParameterValue, error)` in `internal/cli/build.go`. Walk every supplied key → assert it appears in `declared`. Walk every declared key → if absent from `supplied`, require a `defaultValue`. For `CHOICE`, assert the value is in `choices`. For `BOOLEAN`, accept case-insensitive `true`/`false`/`1`/`0`. For `STRING`/`TEXT`/`PASSWORD`/`UNKNOWN`, accept any string. Each error MUST be a `jkerrors.JKError` with code `invalid_input_parameter`, an actionable `Suggestion`, and return exit code `10`.
- [x] 2.5 Call `validateInputParameters` before `SubmitInput`. On validation error, return without contacting Jenkins.
- [x] 2.6 For `action == "abort"`: if `len(paramFlags) > 0`, write `warning: -p flags are ignored for 'abort'\n` to `cmd.ErrOrStderr()` once, then continue with the abort path.

## 3. CLI Unit Tests

- [x] 3.1 Add `Test_BuildInput_SubmitSingleChoice` to `internal/cli/build_test.go`: stub Jenkins responds with one pending input declaring `ENV` (CHOICE: staging/prod). Run with `-p ENV=prod`. Assert the recorded POST hits `/input/Deploy/submit`, Content-Type `application/x-www-form-urlencoded`, body decodes to the expected JSON.
- [x] 3.2 Add `Test_BuildInput_SubmitMixedTypesIncludingAtFile`: input declares CHOICE + BOOLEAN + TEXT. Use `-p NOTES=@<tempfile>`. Assert tempfile contents land in the `NOTES` value.
- [x] 3.3 Add `Test_BuildInput_UnknownParamKey`: stub input declares `ENV` only; user supplies `-p REGION=eu`. Assert no HTTP call made, exit code 10, stderr lists `ENV` as valid.
- [x] 3.4 Add `Test_BuildInput_InvalidChoice`: `-p ENV=devvv` against `["staging","prod"]`. Assert exit 10, stderr lists choices.
- [x] 3.5 Add `Test_BuildInput_RequiredParamMissing`: input declares `ENV` (no default), user passes no `-p`. Assert exit 10, stderr names `ENV`.
- [x] 3.6 Add `Test_BuildInput_AllDefaultsUsesSubmit`: input declares one CHOICE with a default, user passes no `-p`. Assert POST hits `/submit` with the default value (not `/proceedEmpty`).
- [x] 3.7 Add `Test_BuildInput_ZeroParamsUsesProceedEmpty`: input declares zero parameters. Assert POST hits `/proceedEmpty` (regression guard for v0.1 path).
- [x] 3.8 Add `Test_BuildInput_AbortIgnoresParams`: `abort -p X=Y` → assert POST hits `/abort`, stderr contains the warning line, exit 0.
- [x] 3.9 Add `Test_BuildInput_BooleanParsing`: parametric test over `true`/`True`/`TRUE`/`false`/`False`/`1`/`0` → accept; over `yes`/`no`/`maybe` → exit 10.

## 4. End-to-End Test

- [ ] 4.1 In `test/e2e/build_e2e_test.go`, remove the comment at lines 9–13 that excludes `input` from the e2e set (or scope it down to just `--watch`).
- [ ] 4.2 Add `Test_E2E_BuildInput_SubmitParameterizedInput` that uses the `deploy-input` pipeline already seeded in the harness (added 2026-05-26). Trigger it, poll until `pendingInput` appears, run `jk build input <url> proceed -p ENV=prod -p DRY_RUN=false`, then poll until `result == SUCCESS`. Assert the final build log contains evidence that `ENV=prod` was the choice that ran (e.g. echo line).
- [ ] 4.3 Add a small smoke test `Test_E2E_BuildInput_InvalidChoice_ExitsLocally` that runs `jk build input <url> proceed -p ENV=devvv` against the same pending input and asserts exit code 10 without the build progressing past `Approval`. Abort the lingering build at the end.

## 5. Status Read-Path Fixes (Decisions 6 and 7)

- [x] 5.1 In `internal/schema/mapper_build.go`, drop the `id`/`message`/`ok`/`parameters` fields from the inline `Actions[]` struct in `MapBuildStatus` — keep only `_class`. Update `findPendingInputAction` to return a `bool` presence flag (e.g. `hasPendingInputMarker`) instead of a fabricated `*PendingInput`. `MapBuildStatus` no longer populates `out.PendingInput` from core JSON; that responsibility moves entirely to the CLI layer.
- [x] 5.2 In `internal/schema/mapper_build.go`, reorder the `state` switch in `MapBuildStatus` to check `!src.Building` first → `BuildStateDone`; then `hasPendingInputMarker` → `BuildStatePendingInput` (provisional, may be downgraded by the CLI layer in 5.4); else → `BuildStateRunning`. Make `BuildStatus` expose enough hook for the CLI layer to mutate `state` and `pendingInput` after a wfapi enrichment call (e.g. an exported `WithPendingInput(pi *PendingInput)` method, or simply have the CLI layer build the final `BuildStatus` itself).
- [x] 5.3 Update `internal/schema/mapper_build_test.go`: replace `Test_MapBuildStatus_PendingInputState` (which uses an unrealistic core-JSON fixture with populated `id`/`parameters`) with `Test_MapBuildStatus_PendingInputMarkerOnly` that feeds the realistic shape (`actions[]` entry with only `_class`) and asserts `state == PENDING_INPUT` for a building build and `state == DONE` for a non-building build that still carries the marker. Add `Test_MapBuildStatus_DoneWinsOverInputMarker` as a dedicated regression for Decision 7.
- [x] 5.4 In `internal/cli/build.go` `runBuildStatus`, after decoding the core response: if `building == true` AND the core response had an `InputAction` marker, call `client.GetPendingInputs(ctx, ref)`. On non-empty result, set `status.PendingInput = first` and leave `state` as `PENDING_INPUT`. On empty result, clear `state` to `RUNNING`. On error, log under `--debug`, set `state` to `RUNNING`, omit `pendingInput`. Never propagate the enrichment error to the caller.
- [x] 5.5 Add `Test_RunBuildStatus_LivePausedEnrichesFromWfapi` to `internal/cli/build_test.go` using a stub server that serves the realistic core JSON + a non-empty wfapi response. Assert the rendered output has `state: PENDING_INPUT` and `pendingInput.id == "Deploy"`.
- [x] 5.6 Add `Test_RunBuildStatus_FinishedBuildDoesNotEnrich` to `internal/cli/build_test.go` using a stub that serves `building: false` core JSON with a stale `InputAction` marker, and FAILS the test if `/wfapi/pendingInputActions` is called. Assert output has `state: DONE` and no `pendingInput`.
- [x] 5.7 Add `Test_RunBuildStatus_WfapiEnrichmentFailureDegradesGracefully`: stub returns 500 on `/wfapi/pendingInputActions` for a live paused build; assert exit 0, `state: RUNNING`, no `pendingInput`, and a `--debug` log line.
- [x] 5.8 Add `Test_RunBuildStatus_InputSubmittedRaceReturnsRunning`: live core JSON with marker, but wfapi returns `[]`; assert exit 0, `state: RUNNING`, no `pendingInput`.

## 6. Docs

- [ ] 6.1 Update `internal/cli/build.go` `newBuildInputCommand` long help text: document `-p KEY=VALUE`, the validation rules, and link to `docs/schema.md §3.9`.
- [ ] 6.2 Update `docs/schema.md §3.9` to (a) mark `pendingInput.parameters` as `stable`, (b) describe the new `submit` vs `proceedEmpty` rule, (c) document the `-p` flag, (d) clarify that `pendingInput` is populated from `/wfapi/pendingInputActions` (not core `actions[]`), and (e) document the corrected state-derivation order.
- [ ] 6.3 Add a `README.md` example under the existing input section: `jk build input http://jenkins.example.com/job/deploy/42/ proceed -p ENV=prod -p DRY_RUN=false`.
- [ ] 6.4 Update `CHANGELOG.md` (or `README.md` "Release notes" section) for v0.2.0 with two sections: **New features** (parameterized input submission, `pendingInput.parameters` promoted to stable) and **Behavior changes (bug fixes)** (finished builds no longer report `state: PENDING_INPUT`; live paused builds now populate `pendingInput.id`/`message`/`parameters` correctly).

## 7. Validation and Release

- [ ] 7.1 Run `make test-unit && make lint` — both must be green.
- [ ] 7.2 Run `make test-e2e` against the local harness — all tests including the two new write-path tests and the read-path enrichment tests must pass.
- [ ] 7.3 Run `openspec validate add-input-parameter-submission --strict` — must pass.
- [x] 7.4 Manually verify against the harness: trigger `deploy-input`, run `jk build status <url>` while paused → confirm populated `pendingInput`; run `jk build input <url> proceed -p ENV=prod -p DRY_RUN=false` → confirm success; run `jk build status <url>` after completion → confirm `state: DONE`, no `pendingInput`.
- [ ] 7.5 Commit using conventional commit style: `feat(build): submit input step parameters via -p flag and fix status read path`.
- [ ] 7.6 Tag and release v0.2.0 once dogfood validates the new flag against ≥1 real-world parameterized input pipeline.
