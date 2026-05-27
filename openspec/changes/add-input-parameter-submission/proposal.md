## Why

When a Pipeline `input` step declares `parameters` (choice / boolean / string / password), the Jenkins web UI presents a form with dropdowns and checkboxes that the human reviewer fills in before clicking "Proceed". `jk build input proceed` in v0.1 hard-codes the `proceedEmpty` endpoint, which silently submits whatever `defaultValue` each parameter has — and outright fails when a parameter has no default. The schema *already exposes* the input's parameter definitions in `pendingInput.parameters`, so users can see the dropdown choices but have no CLI mechanism to pick one. This makes the entire class of "approver-must-choose" gated pipelines (deploy environment, release channel, region, dry-run toggle) unusable from `jk`.

Live dogfood against the harness on 2026-05-27 also uncovered two adjacent v0.1 bugs in the *read* path that this change MUST fix together with the *write* path, because the write path depends on the read path returning trustworthy data:

- **BUG: `build status` reads pending-input data from the wrong endpoint.** It currently calls only `GET /<n>/api/json`, where Jenkins core exposes `InputAction` as `{"_class": "...InputAction"}` with no `id`/`message`/`ok`/`parameters` fields. As a result, the `pendingInput` block in real-world `jk build status` output is always zero-valued — defeating the very feature this change relies on for client-side validation. The pending-input data the schema documents only exists on `/wfapi/pendingInputActions`, which `jk` already knows how to call (`build input` uses it) but `build status` does not.
- **BUG: `build status` reports `state: PENDING_INPUT` for builds that have already finished.** State derivation checks for an `InputAction` in `actions[]` before consulting `building`/`result`, so a long-finished build whose final `actions[]` still carries the historical `InputAction` reports as still pending — even when `building: false` and `result: SUCCESS`.

## What Changes

- Extend `jk build input <url> proceed` to accept repeatable `-p KEY=VALUE` flags carrying values for the input step's declared parameters.
- Reuse the same `-p KEY=VALUE` and `-p KEY=@file` semantics already established for `jk build trigger`, including `@file` file-loading for long/multiline values.
- Switch the request endpoint from `POST .../input/<id>/proceedEmpty` to `POST .../input/<id>/submit` when the user supplies any `-p` flag OR when the pending input has at least one parameter without a `defaultValue`. Continue using `proceedEmpty` when the input has no parameters at all.
- Validate user-supplied parameters against `pendingInput.parameters` *before* posting: unknown keys, missing required values (no `-p` and no `defaultValue`), and type mismatches (e.g. non-boolean value for a `BOOLEAN` parameter) MUST fail fast with an actionable error and exit code `10`, without contacting Jenkins.
- For `CHOICE` parameters, the supplied value MUST be one of the declared choices; the error MUST list the valid choices.
- `jk build input abort` is unchanged (Jenkins does not accept parameters on abort).
- The `pendingInput.parameters` schema field is promoted from `experimental` to `stable` (its shape has been validated against real Jenkins and is the contract users now script against).
- **Fix `build status` read path:** when the core build response carries an `InputAction` marker AND the build is still `building`, additionally fetch `GET /<n>/wfapi/pendingInputActions` and populate `pendingInput` from that response (the same source `build input` already uses). When the build is no longer `building`, do not emit `pendingInput` at all and do not derive `PENDING_INPUT` state from stale `actions[]` markers.
- **Fix `build status` state derivation:** `state` MUST be `DONE` whenever `building == false` (regardless of any `InputAction` marker present in `actions[]`), and `PENDING_INPUT` MUST require both `building == true` and a non-empty pending-input fetch from `/wfapi/pendingInputActions`.

## Capabilities

### New Capabilities

- `build`: adds a new requirement `Submit a pending input step with parameters` that layers parameter-submission behavior on top of the existing v0.1 `Respond to a pending input step` requirement (defined in the unarchived `init-jk-jenkins-cli` change). The v0.1 requirement is intentionally left untouched — it continues to describe the zero-parameter path; the new requirement describes the parameterized path. They compose: when the user supplies `-p`, the new requirement applies; otherwise the v0.1 requirement applies.

### Modified Capabilities

(none — the `init-jk-jenkins-cli` change has not been archived to `openspec/specs/` yet, so MODIFIED deltas are not available. When that change is archived in the future, this proposal's ADDED requirement and the v0.1 requirement will sit side-by-side in the `build` baseline; a follow-up refactor MAY merge them then.)

## Impact

- **Code**: `internal/jenkins/client.go` (`SubmitInput` signature + endpoint selection), `internal/cli/build.go` (`newBuildInputCommand` adds `-p` flag, shares parameter-parsing helper with `build trigger`; `runBuildStatus` additionally fetches `/wfapi/pendingInputActions` for live builds), `internal/schema/types.go` (stability tier comment on `PendingInput.Parameters`), `internal/schema/mapper_build.go` (state derivation: `DONE` wins over stale `InputAction` marker; `findPendingInputAction` no longer fabricates a `PendingInput` from core `actions[]` — it returns only the presence-bit).
- **Tests**: new unit tests in `internal/cli/build_test.go` (parameter validation, `submit` vs `proceedEmpty` routing) and `internal/schema/mapper_build_test.go` (state derivation: finished build with stale `InputAction` reports `DONE`, not `PENDING_INPUT`; replace the existing `Test_MapBuildStatus_PendingInputState` fixture with one that mirrors the real core `actions[]` shape so it stops false-positiving); new e2e test in `test/e2e/build_e2e_test.go` against the `deploy-input` pipeline added to the harness on 2026-05-26.
- **Docs**: `docs/schema.md §3.9` (input submission, and clarify which fields come from core vs wfapi), `README.md` example showing `jk build input ... proceed -p ENV=prod -p DRY_RUN=false`.
- **No breaking changes for callers on the write path**: existing `jk build input proceed` calls with zero `-p` flags continue to work identically against parameter-less inputs. Inputs that have parameters with full defaults also continue to work via `submit` (sending all defaults) — the *endpoint* changes but the user-observable behavior is identical to v0.1.
- **Behavior change on the read path** (intentional bug fix): `jk build status` for a finished build will stop emitting `state: PENDING_INPUT`. Scripts that branched on this incorrect signal will now see the correct `DONE` state. `jk build status` for a live paused build will start emitting a populated `pendingInput` block (previously zero-valued); scripts that did not check `pendingInput.id != ""` before using it will start seeing real data instead of empty strings.
- **Compatibility**: requires the standard Jenkins `input` step (built into the workflow-input-step plugin since 2.x); no new plugin dependencies.
- **Out of scope**: parameter values sourced from environment variables (deferred); interactive TTY prompts (an `--interactive` mode for "pick from choices" was explicitly rejected — `jk` stays scriptable-first).
