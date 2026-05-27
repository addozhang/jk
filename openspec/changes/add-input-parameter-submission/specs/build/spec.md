## ADDED Requirements

### Requirement: Submit a pending input step with parameters

The system SHALL extend `jk build input <url> proceed` to accept repeatable `-p KEY=VALUE` flags whose values populate the parameters declared on the build's pending `input` step. The command MUST reuse the `-p KEY=VALUE` and `-p KEY=@file` semantics already established for `jk build trigger` (literal value, or contents of the named file when prefixed with `@`).

When the user supplies at least one `-p` flag, OR when the pending input declares any parameter without a `defaultValue`, the command MUST submit the response via `POST .../input/<id>/submit` carrying the parameter values as a JSON form payload in the shape Jenkins expects (`json={"parameter":[{"name":"X","value":"Y"}, ...]}`). When the pending input has zero declared parameters, the command MUST continue to use `POST .../input/<id>/proceedEmpty` (unchanged from v0.1).

Before contacting Jenkins, the command MUST validate every supplied `-p` against `pendingInput.parameters` and fail with exit code `10` and an actionable error when:
- a supplied key is not declared by the input step (error MUST list valid parameter names);
- a `CHOICE` parameter receives a value not present in its `choices` array (error MUST list the valid choices);
- a `BOOLEAN` parameter receives a value that is not `true`/`false` (case-insensitive);
- a required parameter (one without a `defaultValue`) has neither a `-p` value nor a `defaultValue` to fall back on.

The `abort` action is unchanged: it MUST ignore any `-p` flags (with a warning to stderr if any are supplied) and continue to call `POST .../input/<id>/abort`.

The `pendingInput.parameters` field in the `jk build status` output schema is promoted from stability tier `experimental` to `stable` by this change.

#### Scenario: Submit a single CHOICE parameter
- **WHEN** the user runs `jk build input <url> proceed -p ENV=prod` against a build whose pending input declares a `CHOICE` parameter named `ENV` with choices `["staging", "prod"]`
- **THEN** the command issues `POST .../input/<id>/submit` with payload `json={"parameter":[{"name":"ENV","value":"prod"}]}` and returns a YAML document confirming the build resumed

#### Scenario: Submit multiple mixed-type parameters
- **WHEN** the user runs `jk build input <url> proceed -p ENV=prod -p DRY_RUN=false -p NOTES=@./release.md` against a build whose pending input declares `ENV` (CHOICE), `DRY_RUN` (BOOLEAN), and `NOTES` (TEXT)
- **THEN** the command reads the contents of `./release.md` into the `NOTES` value, validates each parameter against its declaration, and submits all three in a single `submit` request

#### Scenario: Unknown parameter key
- **WHEN** the user runs `jk build input <url> proceed -p REGION=eu-west-1` against a build whose pending input declares only `ENV` and `DRY_RUN`
- **THEN** the command exits with code `10` without contacting Jenkins, prints a message naming the unknown key, and lists the valid parameter names (`ENV`, `DRY_RUN`)

#### Scenario: Invalid CHOICE value
- **WHEN** the user runs `jk build input <url> proceed -p ENV=devvv` against an input whose `ENV` parameter declares choices `["staging", "prod"]`
- **THEN** the command exits with code `10` without contacting Jenkins, prints a message naming the invalid value, and lists the valid choices

#### Scenario: Required parameter missing
- **WHEN** the user runs `jk build input <url> proceed` (no `-p` flags) against an input whose `ENV` parameter has no `defaultValue`
- **THEN** the command exits with code `10` without contacting Jenkins, prints a message naming `ENV` as required, and suggests re-running with `-p ENV=<value>`

#### Scenario: All parameters have defaults and no -p supplied
- **WHEN** the user runs `jk build input <url> proceed` against an input whose parameters all have `defaultValue` set
- **THEN** the command issues `POST .../input/<id>/submit` with the default values for every declared parameter, and the build resumes successfully

#### Scenario: Zero-parameter input still uses proceedEmpty
- **WHEN** the user runs `jk build input <url> proceed` against an input that declares no parameters
- **THEN** the command issues `POST .../input/<id>/proceedEmpty` (unchanged from v0.1) and the build resumes

#### Scenario: -p flags are ignored on abort
- **WHEN** the user runs `jk build input <url> abort -p ENV=prod`
- **THEN** the command prints a warning to stderr noting that `-p` is ignored for `abort`, issues `POST .../input/<id>/abort`, and exits with code `0`

#### Scenario: Disambiguation with --input-id still applies
- **WHEN** the user runs `jk build input <url> proceed --input-id Deploy -p ENV=prod` against a build with two pending inputs including one with id `Deploy`
- **THEN** the command validates `-p ENV=prod` against the `Deploy` input's parameter declarations (not the other input's), submits to the `Deploy` endpoint, and leaves the other input pending

### Requirement: Report accurate pending-input state in build status

The `jk build status <url>` command SHALL derive its `pendingInput` block and `state` field so that they accurately reflect what the target build is doing right now, not historical markers left in Jenkins's core `actions[]` array.

When the core `GET /<n>/api/json` response indicates `building: false`, the command MUST report `state: DONE` regardless of whether `actions[]` still carries an `InputAction` marker, and MUST NOT emit a `pendingInput` block.

When the core response indicates `building: true` AND `actions[]` carries an `InputAction` marker, the command MUST additionally fetch `GET /<n>/wfapi/pendingInputActions`. If the response is a non-empty array, the command MUST populate `pendingInput` from the first entry (id, message, ok, parameters with full type information) and report `state: PENDING_INPUT`. If the response is an empty array (the input was submitted between the two HTTP calls), the command MUST omit the `pendingInput` block and report `state: RUNNING`.

When the core response indicates `building: true` AND `actions[]` carries no `InputAction` marker, the command MUST report `state: RUNNING` and MUST NOT make the wfapi call.

If the `/wfapi/pendingInputActions` fetch fails (HTTP error, decode error), the command MUST NOT fail the whole `build status` call. It MUST log the failure under `--debug`, omit the `pendingInput` block, and derive `state` from `building` alone (`RUNNING` for a live build).

#### Scenario: Finished build with historical InputAction reports DONE
- **GIVEN** a build whose `/<n>/api/json` response has `building: false`, `result: "SUCCESS"`, and `actions[]` containing an `InputAction` entry left over from the input step that was submitted earlier in the run
- **WHEN** the user runs `jk build status <url>`
- **THEN** the output has `state: DONE`, `result: SUCCESS`, and no `pendingInput` block, and the command does NOT call `/wfapi/pendingInputActions`

#### Scenario: Live paused build populates pendingInput from wfapi
- **GIVEN** a build whose `/<n>/api/json` response has `building: true` and `actions[]` containing an `InputAction` marker (with only `_class` populated), and whose `/<n>/wfapi/pendingInputActions` response is `[{id: "Deploy", message: "Deploy to which environment?", proceedText: "Deploy", inputs: [ChoiceParameterDefinition ENV, BooleanParameterDefinition DRY_RUN]}]`
- **WHEN** the user runs `jk build status <url>`
- **THEN** the output has `state: PENDING_INPUT`, and `pendingInput.id == "Deploy"`, `pendingInput.message == "Deploy to which environment?"`, `pendingInput.parameters` contains both `ENV` (with its `CHOICE` type and `choices`) and `DRY_RUN` (with its `BOOLEAN` type and `defaultValue`)

#### Scenario: Live running build with no input marker skips wfapi
- **GIVEN** a build whose `/<n>/api/json` response has `building: true` and `actions[]` with no `InputAction` entry
- **WHEN** the user runs `jk build status <url>`
- **THEN** the output has `state: RUNNING`, no `pendingInput` block, and the command does NOT call `/wfapi/pendingInputActions`

#### Scenario: Race — input submitted between the two HTTP calls
- **GIVEN** a build whose `/<n>/api/json` response has `building: true` with an `InputAction` marker, but whose subsequent `/<n>/wfapi/pendingInputActions` response is `[]` (because the input was submitted in the interim)
- **WHEN** the user runs `jk build status <url>`
- **THEN** the output has `state: RUNNING`, no `pendingInput` block, and the command exits 0

#### Scenario: wfapi enrichment failure degrades gracefully
- **GIVEN** a build whose `/<n>/api/json` response has `building: true` with an `InputAction` marker, but whose `/<n>/wfapi/pendingInputActions` call returns HTTP 500
- **WHEN** the user runs `jk build status <url>`
- **THEN** the output has `state: RUNNING`, no `pendingInput` block, the command exits 0, and `--debug` output contains a line describing the wfapi enrichment failure
