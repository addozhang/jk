# build Specification

## Purpose

Triggering Jenkins Pipeline builds with parameters, querying build status (including pending input), retrieving the stage tree, responding to input steps, fetching full or per-stage logs, and the `--watch` polling lifecycle with build-result-as-exit-code semantics.

## Requirements

### Requirement: Trigger a build with parameters

The system SHALL provide a `jk build trigger <url> [-p KEY=VALUE ...]` command that triggers a new build of the specified pipeline. Parameters MUST be passed using repeatable `-p KEY=VALUE` flags. A value of the form `@path/to/file` MUST be treated as a request to read the parameter value from the named file. The command MUST return the queue ID, the eventual build URL (once known), and the build number.

#### Scenario: Trigger an unparameterized build
- **WHEN** the user runs `jk build trigger https://jenkins.foo.com/job/svc/`
- **THEN** the command issues a `POST .../build` request, polls the queue item until a build URL is assigned, and returns a YAML document containing `schemaVersion`, `queueId`, `buildUrl`, and `buildNumber`

#### Scenario: Trigger a parameterized build with inline values
- **WHEN** the user runs `jk build trigger https://jenkins.foo.com/job/svc/ -p BRANCH=main -p ENV=prod`
- **THEN** the command issues a `POST .../buildWithParameters` request including both parameters and returns the same response shape as the unparameterized case

#### Scenario: Trigger with a parameter value loaded from a file
- **WHEN** the user runs `jk build trigger <url> -p CONFIG=@./config.json`
- **THEN** the command reads the contents of `./config.json` and submits them as the value of the `CONFIG` parameter

#### Scenario: Triggering an unknown parameter
- **WHEN** the user runs `jk build trigger <url> -p UNKNOWN=value` against a pipeline that does not define a parameter named `UNKNOWN`
- **THEN** the command exits with a non-zero code and prints an actionable error suggesting `jk pipeline params <url>` to list valid parameter names

### Requirement: Watch a triggered build until completion

The system SHALL support a `--watch` flag on `jk build trigger` that polls the build's status after triggering and exits when the build reaches a terminal state. The process exit code MUST encode the build result: `0` for `SUCCESS`, `1` for `FAILURE`, `2` for `UNSTABLE`, `3` for `ABORTED`, `4` for `PENDING_INPUT`, and a value `>= 10` for any internal error of `jk` itself. The polling interval MUST start at 2 seconds and back off to a maximum of 10 seconds after one minute of polling. The same exit code semantics MUST apply to `jk build cancel --wait`.

#### Scenario: Watch a build that succeeds
- **WHEN** the user runs `jk build trigger <url> --watch` and the build eventually finishes with result `SUCCESS`
- **THEN** the command prints intermediate status updates and exits with code `0`

#### Scenario: Watch a build that fails
- **WHEN** the user runs `jk build trigger <url> --watch` and the build finishes with result `FAILURE`
- **THEN** the command exits with code `1`

#### Scenario: Watch a build that pauses for input
- **WHEN** the user runs `jk build trigger <url> --watch` and the build pauses on a Pipeline input step
- **THEN** the command prints the pending input details (id, message) and exits with code `4` without continuing to poll

### Requirement: Get build status

The system SHALL provide a `jk build status <url>` command that returns the current state of a build. The response MUST include `schemaVersion`, `buildUrl`, `buildNumber`, `queueId`, `result` (or `null` while running), `state` (one of `QUEUED`, `RUNNING`, `PENDING_INPUT`, `DONE`), `building`, `timestampUtc`, `durationMs`, `estimatedDurationMs`, `progressPercent`, and `pendingInput` (an object containing `id`, `message`, `ok`, and `parameters` when `state == PENDING_INPUT`; otherwise `null`).

#### Scenario: Running build status
- **WHEN** the user runs `jk build status <url>` against a currently running build
- **THEN** the response has `state: RUNNING`, `building: true`, `result: null`, a non-null `durationMs` of `0` or the running duration, `estimatedDurationMs` from Jenkins, and a numeric `progressPercent` between `0` and `100`

#### Scenario: Completed build status
- **WHEN** the user runs `jk build status <url>` against a finished build with result `SUCCESS`
- **THEN** the response has `state: DONE`, `building: false`, `result: SUCCESS`, `progressPercent: 100`, and a non-zero `durationMs`

#### Scenario: Build paused for input
- **WHEN** the user runs `jk build status <url>` against a build that is paused at a Pipeline `input` step
- **THEN** the response has `state: PENDING_INPUT` and `pendingInput` is a non-null object with `id`, `message`, `ok`, and `parameters`

### Requirement: Get build stage tree

The system SHALL provide a `jk build stages <url>` command that returns the pipeline run's stage tree, including stage name, status, start time, duration, and any nested parallel branches. The structure MUST preserve the parent-child relationship of sequential and parallel stages.

#### Scenario: Sequential stages
- **WHEN** the user runs `jk build stages <url>` against a build with sequential stages `Build`, `Test`, `Deploy`
- **THEN** the response contains a `stages` array with three entries in order, each with `name`, `status`, `startTimeUtc`, and `durationMs`

#### Scenario: Parallel stages
- **WHEN** the user runs `jk build stages <url>` against a build that contains a `Test` stage with two parallel children `Unit` and `Integration`
- **THEN** the `Test` entry has a `parallel` field listing both child stages with their own `status`, `startTimeUtc`, and `durationMs`

#### Scenario: Duplicate stage names
- **WHEN** a build contains the same stage name appearing more than once (e.g. inside a loop or retry)
- **THEN** the response includes each occurrence as a separate entry, distinguished by a positional suffix (`#1`, `#2`, ...) in a `displayName` field

### Requirement: Respond to a pending input step

The system SHALL provide a `jk build input <url> proceed|abort` command that responds to a Pipeline input step. When a build has exactly one pending input, the command MUST default to operating on that input. When multiple inputs are pending, the command MUST require a `--input-id <id>` flag and exit with a non-zero code if not provided.

#### Scenario: Proceed with a single pending input
- **WHEN** the user runs `jk build input <url> proceed` against a build with exactly one pending input
- **THEN** the command submits the input as approved and returns a YAML document confirming the new build state

#### Scenario: Abort a single pending input
- **WHEN** the user runs `jk build input <url> abort` against a build with exactly one pending input
- **THEN** the command submits the input as aborted and returns a YAML document confirming the new build state

#### Scenario: Multiple pending inputs without disambiguation
- **WHEN** the user runs `jk build input <url> proceed` against a build with two pending inputs and no `--input-id` flag
- **THEN** the command exits with a non-zero code, prints all pending input IDs and messages, and instructs the user to re-run with `--input-id <id>`

#### Scenario: Multiple pending inputs with disambiguation
- **WHEN** the user runs `jk build input <url> proceed --input-id Deploy-Approval` against a build with two pending inputs including one with id `Deploy-Approval`
- **THEN** the command submits proceed for that specific input and leaves the others pending

### Requirement: Get build logs

The system SHALL provide a `jk build logs <url>` command that prints a build's console log to stdout. The command MUST support a `-f` / `--follow` flag that streams new log output until the build reaches a terminal state. The command MUST support a `--stage NAME` flag that returns only the log of the named stage.

#### Scenario: Print a finished build's log
- **WHEN** the user runs `jk build logs <url>` against a completed build
- **THEN** the command prints the full console output to stdout and exits with code `0`

#### Scenario: Follow a running build's log
- **WHEN** the user runs `jk build logs <url> -f` against a running build
- **THEN** the command prints existing log content, continues streaming new content as it appears, and exits with code `0` when the build reaches a terminal state

#### Scenario: Single stage log
- **WHEN** the user runs `jk build logs <url> --stage Deploy` against a build whose `Deploy` stage has finished
- **THEN** the command prints only the log content produced during the `Deploy` stage's execution

#### Scenario: Stage not found
- **WHEN** the user runs `jk build logs <url> --stage Nonexistent` against a build that does not contain a stage named `Nonexistent`
- **THEN** the command exits with a non-zero code and lists the actual stage names from the build

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

### Requirement: Accept permalink URLs for build-scoped commands

The system SHALL allow build-scoped commands (`build status`, `build stages`, `build logs`, `build input`, `build params`) to operate on URLs whose trailing segment is a Jenkins permalink (`lastBuild`, `lastSuccessfulBuild`, `lastFailedBuild`, `lastStableBuild`, `lastUnstableBuild`, `lastUnsuccessfulBuild`, `lastCompletedBuild`). The Jenkins server SHALL resolve the permalink to a concrete build for each command invocation; the resolved build's numeric identifier MUST appear in any output that records the build number.

#### Scenario: Status for lastBuild URL
- **WHEN** the user runs `jk build status http://jenkins.foo.com/job/svc/lastBuild/`
- **THEN** the command fetches `<host>/job/svc/lastBuild/api/json`, the response carries the resolved numeric build number, and the output records that number as the build identity

#### Scenario: Status for lastSuccessfulBuild URL when one exists
- **WHEN** the user runs `jk build status http://jenkins.foo.com/job/svc/lastSuccessfulBuild/` and the pipeline has at least one successful build
- **THEN** the command returns the status of the most recent successful build with its actual numeric identifier

#### Scenario: Permalink URL skips the lastBuild existence pre-flight
- **WHEN** the user runs any build-scoped command against a permalink URL
- **THEN** the client MUST NOT issue the `<job>/api/json?tree=lastBuild[number]` pre-flight check (which is only used for "no build specified" defaulting); a 404 from Jenkins on the permalink path surfaces as the normal HTTP error

#### Scenario: Permalink URL with no matching build
- **WHEN** the user runs `jk build status http://jenkins.foo.com/job/svc/lastSuccessfulBuild/` and the pipeline has no successful builds yet
- **THEN** the command surfaces the Jenkins HTTP 404 as an error rather than reporting a synthetic "never built" message

### Requirement: Return parameter values used to trigger a specific build

The system SHALL provide a `jk build params <build-url>` command that returns the trigger-time parameter values of the specified build as a `BuildParams` payload (`{schemaVersion, buildUrl, buildNumber, parameters[]}`). Each `parameters[]` entry MUST be a `BuildParameter` object with the shape `{name: string, value: string|boolean|null}` — distinct from the `Parameter` shape returned by `jk pipeline params` (which describes parameter *definitions*: type, default, choices, description), because `build params` reports the *submitted values* of one specific build. Builds with no parameters MUST return an empty `parameters` array, not an error. Parameter values that Jenkins reports as null (e.g. redacted password / credentials parameters) MUST be preserved as null in the output.

#### Scenario: Build with parameters
- **WHEN** the user runs `jk build params http://jenkins.foo.com/job/svc/42/` against a build triggered with `ENV=prod` and `DRY_RUN=false`
- **THEN** the output's `parameters` array contains entries `{name: "ENV", value: "prod"}` and `{name: "DRY_RUN", value: "false"}` (order matching Jenkins' `actions[].parameters` order) and `buildNumber == 42`

#### Scenario: Build with no parameters returns empty array
- **WHEN** the user runs `jk build params http://jenkins.foo.com/job/hello/3/` against an unparameterized build
- **THEN** the command exits 0 and the output's `parameters` field is `[]` (not null, not an error)

#### Scenario: Permalink URL accepted
- **WHEN** the user runs `jk build params http://jenkins.foo.com/job/svc/lastSuccessfulBuild/`
- **THEN** the command resolves the permalink server-side, returns the parameter values for that build, and `buildNumber` records the resolved numeric build number

#### Scenario: Redacted parameter preserved as null
- **WHEN** a build was triggered with a `password`-type parameter named `API_TOKEN` and Jenkins returns `{name: "API_TOKEN", value: null}` in `actions[].parameters`
- **THEN** the output preserves `{name: "API_TOKEN", value: null}` without substituting an empty string or omitting the entry

#### Scenario: Pipeline that has never been built
- **WHEN** the user runs `jk build params http://jenkins.foo.com/job/new-svc/lastBuild/` against a pipeline that has never built
- **THEN** the command surfaces the Jenkins HTTP 404 as an error

### Requirement: Cancel a running build

The system SHALL provide a `jk build cancel <build-url>` command that requests Jenkins to stop the specified build. The command MUST POST to the Jenkins `/stop` endpoint and return a YAML document confirming the cancellation request was accepted. The command MUST accept a `--wait` flag that polls the build status until the build reaches a terminal state before exiting. The command MUST accept a build permalink (e.g. `lastBuild`) in the build-position slot.

#### Scenario: Cancel a running build without --wait
- **WHEN** the user runs `jk build cancel http://jenkins/job/svc/42/`
- **THEN** the command POSTs to `http://jenkins/job/svc/42/stop`, receives HTTP 200, and returns a YAML document containing `schemaVersion`, `buildUrl`, `buildNumber`, and `state` reflecting the build state at the time of the request

#### Scenario: Cancel a running build with --wait
- **WHEN** the user runs `jk build cancel http://jenkins/job/svc/42/ --wait`
- **THEN** the command POSTs to the stop endpoint and then polls the build status until it reaches a terminal state, exiting with code `3` once the build is `ABORTED`

#### Scenario: Cancel with a non-existent build URL
- **WHEN** the user runs `jk build cancel` with a URL whose build number does not exist
- **THEN** the command exits with a non-zero code and prints a `Build not found` error with a suggestion to check the build number

#### Scenario: Cancel a build that has already finished
- **WHEN** the user runs `jk build cancel` against a build that has already completed
- **THEN** the command succeeds (Jenkins returns HTTP 200) and the returned YAML reflects the terminal state of the build

#### Scenario: Cancel a build addressed by permalink
- **WHEN** the user runs `jk build cancel http://jenkins/job/svc/lastBuild/`
- **THEN** the command POSTs to `.../lastBuild/stop` and reports the resolved numeric build number in the output
