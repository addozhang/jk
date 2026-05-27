## ADDED Requirements

### Requirement: Trust additional CAs via `SSL_CERT_FILE`

The system SHALL, on every HTTPS request to a Jenkins host, build the TLS root CA pool from the system trust store augmented by any certificates loaded from the file path stored in the `SSL_CERT_FILE` environment variable. The environment variable MUST be honored without any additional flag.

#### Scenario: Self-signed Jenkins via `SSL_CERT_FILE`
- **WHEN** the user runs any `jk` command against a Jenkins instance whose certificate is signed by a CA contained in the file referenced by `SSL_CERT_FILE`
- **THEN** the request succeeds without certificate verification errors

#### Scenario: `SSL_CERT_FILE` set but file is missing
- **WHEN** the user runs any `jk` command with `SSL_CERT_FILE` pointing at a path that does not exist
- **THEN** the command exits with a non-zero code and prints an error indicating that `SSL_CERT_FILE` references a missing file, with the resolved path included in the message

#### Scenario: `SSL_CERT_FILE` set but file is not a valid PEM bundle
- **WHEN** the user runs any `jk` command with `SSL_CERT_FILE` pointing at a file that is not a valid PEM-encoded certificate bundle
- **THEN** the command exits with a non-zero code and prints an error indicating that the file could not be parsed as a PEM certificate bundle

### Requirement: Allow insecure TLS via `--insecure` flag

The system SHALL provide a global `--insecure` flag that disables TLS certificate verification for the current invocation. When `--insecure` is in effect, the system MUST print a warning to stderr.

#### Scenario: Using `--insecure` against a self-signed instance
- **WHEN** the user runs `jk pipeline info https://jenkins.local/job/svc/ --insecure`
- **THEN** TLS verification is skipped, a warning is printed to stderr indicating that certificate verification was disabled, and the request proceeds

### Requirement: Apply a global request timeout

The system SHALL apply a per-request timeout configurable by a global `--timeout` flag accepting a Go-style duration string (e.g. `30s`, `2m`). The default value MUST be `30s`.

#### Scenario: Default timeout applied
- **WHEN** the user runs any `jk` command without `--timeout`
- **THEN** each outbound HTTP request to Jenkins is bounded by a 30-second timeout

#### Scenario: Custom timeout applied
- **WHEN** the user runs `jk build logs <url> -f --timeout 5m`
- **THEN** each outbound HTTP request is bounded by a 5-minute timeout

#### Scenario: Timeout exceeded
- **WHEN** a request to Jenkins does not return within the configured timeout
- **THEN** the command exits with a non-zero code and prints an error indicating the timeout duration that was exceeded and suggesting `--timeout <duration>` to increase it

### Requirement: Log HTTP exchanges with `--debug`

The system SHALL provide a global `--debug` flag that logs every outbound HTTP request (method, URL, headers excluding `Authorization`, body) and inbound HTTP response (status, headers, body) to stderr.

#### Scenario: Debug logging suppresses Authorization header
- **WHEN** the user runs any `jk` command with `--debug`
- **THEN** the debug log includes request method, URL, body, and all headers except `Authorization`, which is either omitted or rendered as a redacted placeholder

#### Scenario: Debug logging does not affect stdout
- **WHEN** the user runs `jk build status <url> --debug -o json`
- **THEN** debug output is written exclusively to stderr, and stdout contains only the normal JSON response, allowing the user to pipe stdout to other tools while watching the debug stream
