## ADDED Requirements

### Requirement: Cancel a running build

The system SHALL provide a `jk build cancel <build-url>` command that requests Jenkins to stop the specified build. The command MUST POST to the Jenkins `/stop` endpoint and return a YAML document confirming the cancellation request was accepted. The command MUST accept a `--wait` flag that polls the build status until the build reaches a terminal state before exiting.

#### Scenario: Cancel a running build without --wait

- **WHEN** the user runs `jk build cancel http://jenkins/job/svc/42/`
- **THEN** the command POSTs to `http://jenkins/job/svc/42/stop`, receives HTTP 200, and returns a YAML document containing `schemaVersion`, `buildUrl`, `buildNumber`, and `state` reflecting the build state at the time of the request

#### Scenario: Cancel a running build with --wait

- **WHEN** the user runs `jk build cancel http://jenkins/job/svc/42/ --wait`
- **THEN** the command POSTs to the stop endpoint and then polls `build status` until the build reaches a terminal state, emitting the final build status YAML with `state: DONE` and `result: ABORTED`

#### Scenario: Cancel with a non-existent build URL

- **WHEN** the user runs `jk build cancel` with a URL whose build number does not exist
- **THEN** the command exits with a non-zero code and prints a `Build not found` error with a suggestion to check the build number

#### Scenario: Cancel a build that has already finished

- **WHEN** the user runs `jk build cancel` against a build that has already completed
- **THEN** the command succeeds (Jenkins returns HTTP 200) and the returned YAML reflects the terminal state of the build

#### Scenario: Cancel a build addressed by permalink

- **WHEN** the user runs `jk build cancel http://jenkins/job/svc/lastBuild/`
- **THEN** the command POSTs to `.../lastBuild/stop` and reports the resolved numeric build number in the output
