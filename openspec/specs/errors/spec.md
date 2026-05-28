# errors Specification

## Purpose

Translation of Jenkins API errors into human-readable messages with suggested next actions, and the exit-code discipline that separates `jk`-level failures from command-result semantics.

## Requirements

### Requirement: Translate API errors into human-readable messages with next-step suggestions

The system SHALL translate Jenkins API errors into human-readable error messages printed to stderr. Every translated error MUST include a description of what happened and a suggested next action (a specific `jk` command, a flag, or a verification step). The system MUST NOT print raw Jenkins HTML error pages or Java stack traces to the user when `--debug` is not in effect.

#### Scenario: Invalid API token
- **WHEN** any command receives HTTP 401 or 403 from Jenkins on a request that should be authenticated
- **THEN** the command exits with a non-zero code and prints an error of the form: "API token rejected by `<host>`. Run: `jk auth add <host>` to refresh."

#### Scenario: Pipeline not found
- **WHEN** any command receives HTTP 404 against a Jenkins pipeline URL
- **THEN** the command exits with a non-zero code and prints an error of the form: "Pipeline not found: `<url>`. Check the URL or list with: `jk pipeline list <parent-folder-url>`."

#### Scenario: Network timeout
- **WHEN** any command's request to Jenkins exceeds the configured timeout
- **THEN** the command exits with a non-zero code and prints an error of the form: "Timed out after `<duration>` contacting `<host>`. Increase with `--timeout <duration>` or check VPN connectivity."

#### Scenario: CSRF crumb acquisition failure
- **WHEN** the CSRF crumb refresh-and-retry recovery logic still fails to obtain a crumb that Jenkins accepts
- **THEN** the command exits with a non-zero code and prints an error noting the host and suggesting that the Jenkins version may have an incompatible CSRF behavior, with a request to file an issue

#### Scenario: `SSL_CERT_FILE` misconfiguration
- **WHEN** the configured `SSL_CERT_FILE` path is missing or unparseable
- **THEN** the command exits with a non-zero code and prints an error explicitly naming `SSL_CERT_FILE` and the resolved path

### Requirement: Bypass error translation with `--debug`

The system SHALL, when `--debug` is set, additionally print the raw HTTP exchange (request and response, including body) to stderr alongside the translated error. The translated message MUST still be printed for consistency.

#### Scenario: Debug shows raw response alongside translation
- **WHEN** the user runs any failing command with `--debug`
- **THEN** stderr contains both the raw HTTP request/response and the translated `jk` error message

### Requirement: Reserve exit codes for `jk`-level failures

The system SHALL reserve exit codes `0`–`9` for command-result semantics (e.g. build result mapping under `build trigger --watch`) and use exit codes `>= 10` for `jk`-level failures (authentication, network, parsing, configuration). The exact value within the `>= 10` range MAY be unstable across versions but MUST be non-zero.

#### Scenario: Authentication failure exit code
- **WHEN** a command fails because of an authentication error
- **THEN** the command exits with a code `>= 10`

#### Scenario: Successful command with a failed build under `--watch`
- **WHEN** the user runs `jk build trigger <url> --watch` and the build itself reports `FAILURE`
- **THEN** the command exits with code `1` (the build-result code), not a `>= 10` code, because `jk` itself succeeded
