## MODIFIED Requirements

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

#### Scenario: Cancel with --wait exits with ABORTED code

- **WHEN** the user runs `jk build cancel <url> --wait` and the build is successfully stopped
- **THEN** the command exits with code `3`
