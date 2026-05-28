## ADDED Requirements

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

The system SHALL support a `--watch` flag on `jk build trigger` that polls the build's status after triggering and exits when the build reaches a terminal state. The process exit code MUST encode the build result: `0` for `SUCCESS`, `1` for `FAILURE`, `2` for `UNSTABLE`, `3` for `ABORTED`, `4` for `PENDING_INPUT`, and a value `>= 10` for any internal error of `jk` itself. The polling interval MUST start at 2 seconds and back off to a maximum of 10 seconds after one minute of polling.

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
