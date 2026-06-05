## MODIFIED Requirements

### Requirement: Add credentials for a Jenkins host

The system SHALL provide a `jk auth add <url>` command that stores an API token for a Jenkins instance. The lookup key MUST be the URL scheme + hostname (with optional non-default port) plus an optional context-path prefix. The context-path prefix is the portion of the path preceding the first `/job/` segment, or the entire path when the URL contains no `/job/` segment; it MUST be normalized to a leading `/` with no trailing slash, and an empty result MUST yield a host-only key identical to prior behavior. Credentials MUST be persisted to a file under the user's config directory (`~/.config/jk/credentials` on macOS/Linux; equivalent on Windows) with file mode `0600` on POSIX systems. The confirmation message MUST name the stored key verbatim so the user can see whether a context path was retained.

#### Scenario: First-time credential addition
- **WHEN** the user runs `jk auth add https://jenkins.foo.com` and enters a valid API token at the prompt
- **THEN** the system writes the credential under the key `https://jenkins.foo.com` with mode `0600` and prints a confirmation message naming that key

#### Scenario: Overwriting an existing credential
- **WHEN** the user runs `jk auth add https://jenkins.foo.com` for a key that already has credentials
- **THEN** the system prompts for confirmation before overwriting and replaces the existing entry on confirmation

#### Scenario: Host-only normalization (no context path)
- **WHEN** the user runs `jk auth add https://jenkins.foo.com/` (trailing slash) or `jk auth add https://jenkins.foo.com/job/whatever` (job hierarchy, no context path)
- **THEN** the system stores the host-only key `https://jenkins.foo.com`, discarding the trailing slash and any `/job/...` hierarchy

#### Scenario: Context-path instance credential addition
- **WHEN** the user runs `jk auth add https://ci.example.com/team-a` (or `https://ci.example.com/team-a/job/svc/` with job hierarchy) for a host that already has a separate `https://ci.example.com/team-b` entry
- **THEN** the system stores the key `https://ci.example.com/team-a`, names that full key in the confirmation message, and leaves the `https://ci.example.com/team-b` entry untouched

### Requirement: Look up credentials by hostname

The system SHALL provide an internal credential lookup that resolves a Jenkins request URL to a stored API token by selecting the most specific configured key. A configured key matches a request when its scheme and host equal the request's and the request path either equals the key's context-path prefix or continues past it at a `/` segment boundary. Among all matching keys, the system SHALL select the one with the longest context-path prefix. A host-only key (no context path) matches any same-host request as the shortest prefix, preserving prior single-instance behavior. The lookup is used by all commands that contact a Jenkins instance.

#### Scenario: Host-only lookup unchanged
- **WHEN** a command is invoked with a URL whose host matches a host-only credential entry and no more specific entry exists
- **THEN** the system retrieves that entry's API token and uses it in the `Authorization` header

#### Scenario: Most-specific context-path entry wins
- **WHEN** the store holds both `https://ci.example.com` and `https://ci.example.com/team-a`, and a command runs against `https://ci.example.com/team-a/job/svc/2/`
- **THEN** the system selects the `https://ci.example.com/team-a` token, not the host-only one

#### Scenario: Fallback to a host-only entry
- **WHEN** the store holds `https://ci.example.com` (host-only) and a command runs against `https://ci.example.com/team-z/job/x/` for which no `/team-z` entry exists
- **THEN** the system uses the host-only `https://ci.example.com` token

#### Scenario: Segment boundary prevents partial matches
- **WHEN** the store holds `https://ci.example.com/team-a` (and no host-only entry) and a command runs against `https://ci.example.com/team-amber/job/x/`
- **THEN** the `https://ci.example.com/team-a` entry does NOT match, because the request path does not continue at a `/` boundary

#### Scenario: Missing credential for the request
- **WHEN** a command is invoked with a URL for which no configured key matches at any prefix
- **THEN** the system exits with a non-zero code and prints an actionable error message instructing the user to run `jk auth add <url>`

### Requirement: Acquire and cache CSRF crumb

The system SHALL automatically acquire a CSRF crumb before issuing any state-changing request (POST, PUT, DELETE) to a Jenkins instance. The crumb endpoint MUST be `crumbIssuer/api/json` resolved against the matched credential's context path (i.e. `scheme://host` + base path + `/crumbIssuer/api/json`), so instances mounted under a context path are addressed correctly. The crumb MUST be cached in memory for the process lifetime, keyed by the resolved credential key so that two instances sharing a host keep independent crumbs. On a 403 response that indicates a crumb mismatch, the system MUST refresh the crumb once and retry the original request exactly one time.

#### Scenario: Initial crumb acquisition
- **WHEN** a state-changing request is made to an instance for which no crumb is cached
- **THEN** the system first issues a `GET` to the instance's `crumbIssuer/api/json` (including its context path, if any), parses the returned `crumb` and `crumbRequestField`, and includes them as a header on the original request

#### Scenario: Crumb fetched from the context-path endpoint
- **WHEN** a state-changing request targets `https://ci.example.com/team-a/job/svc/build` and the matched credential key is `https://ci.example.com/team-a`
- **THEN** the system fetches the crumb from `https://ci.example.com/team-a/crumbIssuer/api/json`, not from the host root

#### Scenario: Per-instance crumb caching on a shared host
- **WHEN** state-changing requests are made to both `https://ci.example.com/team-a/...` and `https://ci.example.com/team-b/...`, each with its own credential
- **THEN** the system caches and presents a distinct crumb for each instance, keyed by its resolved credential key

#### Scenario: Crumb expiry recovery
- **WHEN** a state-changing request returns 403 with a CSRF-related error body, and the cached crumb is currently being used
- **THEN** the system fetches a new crumb, retries the original request once, and propagates the result (success or failure) to the caller

#### Scenario: CSRF disabled on instance
- **WHEN** the `GET` to the instance's `crumbIssuer/api/json` returns 404
- **THEN** the system treats the instance as not requiring CSRF, omits crumb headers from subsequent requests, and caches that fact for the process lifetime
