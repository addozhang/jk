## ADDED Requirements

### Requirement: Add credentials for a Jenkins host

The system SHALL provide a `jk auth add <host>` command that stores an API token for a Jenkins host. The host MUST be a URL scheme + hostname (with optional port), e.g. `https://jenkins.foo.com`. Credentials MUST be persisted to a file under the user's config directory (`~/.config/jk/credentials` on macOS/Linux; equivalent on Windows) with file mode `0600` on POSIX systems.

#### Scenario: First-time credential addition
- **WHEN** the user runs `jk auth add https://jenkins.foo.com` and enters a valid API token at the prompt
- **THEN** the system writes the host + token pair to the credentials file with mode `0600` and prints a confirmation message

#### Scenario: Overwriting an existing credential
- **WHEN** the user runs `jk auth add https://jenkins.foo.com` for a host that already has credentials
- **THEN** the system prompts for confirmation before overwriting and replaces the existing entry on confirmation

#### Scenario: Host normalization
- **WHEN** the user runs `jk auth add https://jenkins.foo.com/` (with trailing slash) or `jk auth add https://jenkins.foo.com/job/whatever` (with path)
- **THEN** the system stores only the scheme + host (and port, if present) as the lookup key, discarding any path or trailing slash

### Requirement: List configured hosts

The system SHALL provide a `jk auth list` command that prints each configured host, one per line. The command MUST NOT print API tokens.

#### Scenario: Listing configured hosts
- **WHEN** the user runs `jk auth list` after adding credentials for two hosts
- **THEN** the system prints both host URLs to stdout, one per line, in the configured output format (YAML by default), and exits with code `0`

#### Scenario: No hosts configured
- **WHEN** the user runs `jk auth list` with no credentials file or an empty credentials file
- **THEN** the system prints an empty list (or an empty YAML/JSON array) and exits with code `0`

### Requirement: Look up credentials by hostname

The system SHALL provide an internal credential lookup that resolves a Jenkins URL's hostname (scheme + host + port) to the stored API token. The lookup is used by all commands that contact a Jenkins instance.

#### Scenario: Successful lookup
- **WHEN** any command is invoked with a URL whose hostname matches a configured credential entry
- **THEN** the system retrieves the corresponding API token and uses it in the `Authorization` header for subsequent requests

#### Scenario: Missing credential for host
- **WHEN** any command is invoked with a URL whose hostname has no configured credential entry
- **THEN** the system exits with a non-zero code and prints an actionable error message instructing the user to run `jk auth add <host>`

### Requirement: Acquire and cache CSRF crumb

The system SHALL automatically acquire a CSRF crumb from `/crumbIssuer/api/json` before issuing any state-changing request (POST, PUT, DELETE) to a Jenkins host. The crumb MUST be cached in memory for the process lifetime. On a 403 response that indicates a crumb mismatch, the system MUST refresh the crumb once and retry the original request exactly one time.

#### Scenario: Initial crumb acquisition
- **WHEN** a state-changing request is made to a host for which no crumb is cached
- **THEN** the system first issues a `GET /crumbIssuer/api/json` request, parses the returned `crumb` and `crumbRequestField`, and includes them as a header on the original request

#### Scenario: Crumb expiry recovery
- **WHEN** a state-changing request returns 403 with a CSRF-related error body, and the cached crumb is currently being used
- **THEN** the system fetches a new crumb, retries the original request once, and propagates the result (success or failure) to the caller

#### Scenario: CSRF disabled on instance
- **WHEN** `GET /crumbIssuer/api/json` returns 404
- **THEN** the system treats the host as not requiring CSRF, omits crumb headers from subsequent requests, and caches that fact for the process lifetime
