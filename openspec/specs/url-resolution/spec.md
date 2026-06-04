# url-resolution Specification

## Purpose

Parsing Jenkins URLs into a structured reference (host, ordered `/job/` segments, optional build number) covering folder, multibranch, and build-specific shapes, and mapping host to stored credentials. The parser is intentionally context-free: distinguishing folder vs. pipeline vs. branch requires a Jenkins API call and is not the URL parser's job.
## Requirements
### Requirement: Parse Jenkins URLs into a structured reference

The system SHALL provide a URL parser that converts any supported Jenkins URL into a structured `Ref` value containing the hostname, an ordered list of `/job/<name>` segments, and an optional build number. The parser MUST NOT attempt to classify any segment as folder, pipeline, or branch — those distinctions require Jenkins API context that the URL alone does not provide.

The parser MUST accept URLs with or without trailing slashes, with `http://` or `https://` schemes, with or without explicit ports, and with arbitrarily nested `/job/<name>` segments. The parser MUST URL-decode each segment (e.g. `%20` → space). The parser MUST strip any query string or fragment before extraction.

#### Scenario: Top-level pipeline URL
- **WHEN** the parser receives `https://jenkins.foo.com/job/svc/`
- **THEN** the parser returns a Ref with host `https://jenkins.foo.com`, job segments `["svc"]`, and build number `0` (meaning unspecified)

#### Scenario: Multi-segment pipeline URL
- **WHEN** the parser receives `https://jenkins.foo.com/job/team/job/platform/job/svc/`
- **THEN** the parser returns a Ref with host `https://jenkins.foo.com`, job segments `["team", "platform", "svc"]`, and build number `0`

#### Scenario: Multi-segment URL with explicit build number
- **WHEN** the parser receives `https://jenkins.foo.com/job/team/job/svc/job/main/42/`
- **THEN** the parser returns a Ref with host `https://jenkins.foo.com`, job segments `["team", "svc", "main"]`, and build number `42`

#### Scenario: URL without trailing slash
- **WHEN** the parser receives `https://jenkins.foo.com/job/svc` (no trailing slash)
- **THEN** the parser returns the same Ref as the equivalent URL with a trailing slash

#### Scenario: URL with explicit port
- **WHEN** the parser receives `http://jenkins.local:8080/job/svc/`
- **THEN** the parser returns a Ref whose host preserves the scheme, hostname, and port: `http://jenkins.local:8080`

#### Scenario: URL with default port
- **WHEN** the parser receives `https://jenkins.foo.com:443/job/svc/`
- **THEN** the parser returns a Ref whose host is `https://jenkins.foo.com` (the default `:443` is stripped); likewise `http://...:80/...` strips `:80`

#### Scenario: URL with encoded segment
- **WHEN** the parser receives `https://jenkins.foo.com/job/my%20pipeline/`
- **THEN** the parser returns a Ref with job segments `["my pipeline"]`

#### Scenario: URL with query string and fragment
- **WHEN** the parser receives `https://jenkins.foo.com/job/svc/?foo=bar#frag`
- **THEN** the parser returns a Ref with job segments `["svc"]`, build number `0`, and ignores the query and fragment

#### Scenario: Invalid URL rejected — not a job URL
- **WHEN** the parser receives a URL that does not contain `/job/` segments (e.g. `https://jenkins.foo.com/view/All/`)
- **THEN** the parser returns an error indicating that the URL does not point at a Jenkins job

#### Scenario: Invalid URL rejected — malformed
- **WHEN** the parser receives a string that is not a valid URL (e.g. `not a url`, empty string, scheme-less `jenkins.foo.com/job/svc`)
- **THEN** the parser returns an error describing the parse failure

#### Scenario: Invalid URL rejected — empty job segment
- **WHEN** the parser receives `https://jenkins.foo.com/job//job/svc/` (empty job name)
- **THEN** the parser returns an error indicating an empty job segment

### Requirement: Default build number to lastBuild when unspecified

The system SHALL, for build-scoped commands (`build status`, `build stages`, `build logs`, `build input`), treat a Ref with build number `0` as a request to operate on the pipeline's `lastBuild`. An explicitly specified build number MUST be used as given.

#### Scenario: Status without build number
- **WHEN** the user runs `jk build status https://jenkins.foo.com/job/svc/` (no build number in URL)
- **THEN** the command resolves the target build by calling Jenkins's `lastBuild` endpoint and returns the status of that build

#### Scenario: Status with explicit build number
- **WHEN** the user runs `jk build status https://jenkins.foo.com/job/svc/42/`
- **THEN** the command operates on build `42` and does not fetch `lastBuild`

### Requirement: Map URL host to stored credentials

The system SHALL extract the scheme, hostname, and port (if non-default) from a parsed URL and use that string as the lookup key into the credentials store described in the `auth` capability.

#### Scenario: Host extraction for credential lookup
- **WHEN** a command is invoked with `https://jenkins.foo.com/job/svc/`
- **THEN** the system looks up credentials using the key `https://jenkins.foo.com`

#### Scenario: Host extraction preserves non-default port
- **WHEN** a command is invoked with `http://jenkins.local:8080/job/svc/`
- **THEN** the system looks up credentials using the key `http://jenkins.local:8080`

#### Scenario: Host extraction strips default port
- **WHEN** a command is invoked with `https://jenkins.foo.com:443/job/svc/`
- **THEN** the system looks up credentials using the key `https://jenkins.foo.com` (the default port is stripped to normalize the lookup key)

### Requirement: Recognize Jenkins permalink build references

The system SHALL accept any of the seven Jenkins permalink names — `lastBuild`, `lastSuccessfulBuild`, `lastFailedBuild`, `lastStableBuild`, `lastUnstableBuild`, `lastUnsuccessfulBuild`, `lastCompletedBuild` — as a trailing build-reference segment on a parsed job URL, populating a new `BuildPermalink` field on the parsed reference. Recognition MUST be case-sensitive. Any other non-numeric trailing segment MUST continue to be rejected with the existing "not a Jenkins job URL" error.

#### Scenario: Permalink lastBuild
- **WHEN** the user parses `http://jenkins.foo.com/job/svc/lastBuild/`
- **THEN** the parser returns a Ref with `JobSegments == ["svc"]`, `BuildNumber == 0`, and `BuildPermalink == "lastBuild"`

#### Scenario: Permalink lastSuccessfulBuild on a nested path
- **WHEN** the user parses `http://jenkins.foo.com/job/team/job/svc/lastSuccessfulBuild`
- **THEN** the parser returns a Ref with `JobSegments == ["team", "svc"]`, `BuildNumber == 0`, and `BuildPermalink == "lastSuccessfulBuild"`

#### Scenario: All seven permalinks accepted
- **WHEN** the user parses URLs ending in each of `lastBuild`, `lastSuccessfulBuild`, `lastFailedBuild`, `lastStableBuild`, `lastUnstableBuild`, `lastUnsuccessfulBuild`, `lastCompletedBuild`
- **THEN** each parse succeeds and `BuildPermalink` is set to the exact name from the URL

#### Scenario: Permalink and numeric build number are mutually exclusive
- **WHEN** the parser sets `BuildPermalink` to a non-empty value
- **THEN** `BuildNumber` MUST be `0`; conversely a non-zero `BuildNumber` MUST leave `BuildPermalink` empty

#### Scenario: Unknown trailing segment still rejected
- **WHEN** the user parses `http://jenkins.foo.com/job/svc/latestBuild/` (typo) or `.../job/svc/config.xml`
- **THEN** the parser returns the existing "not a Jenkins job URL" error

#### Scenario: Case mismatch rejected
- **WHEN** the user parses `http://jenkins.foo.com/job/svc/LASTBUILD/`
- **THEN** the parser returns the existing "not a Jenkins job URL" error (Jenkins permalinks are case-sensitive)

### Requirement: Format API URLs from permalink references

The system SHALL emit the `BuildPermalink` value as a literal path segment when formatting Jenkins API URLs from a Ref, in the same position a numeric build number would occupy. The Jenkins server resolves the permalink to a concrete build at request time.

#### Scenario: APIPath for a permalink reference
- **WHEN** a Ref has `JobSegments == ["svc"]`, `BuildPermalink == "lastSuccessfulBuild"`, and `BuildNumber == 0`
- **THEN** `APIPath("api/json")` returns `<host>/job/svc/lastSuccessfulBuild/api/json`

#### Scenario: APIPath for a bare job (no build, no permalink) unchanged
- **WHEN** a Ref has `JobSegments == ["svc"]`, `BuildPermalink == ""`, and `BuildNumber == 0`
- **THEN** `APIPath("api/json")` returns `<host>/job/svc/api/json`

### Requirement: Parse Jenkins URLs deployed under a context path

The system SHALL accept Jenkins job and build URLs whose `/job/` hierarchy is preceded by an arbitrary context-path prefix (one or more leading path segments), capturing that prefix on a new `BasePath` field of the parsed reference. The prefix is everything between the host and the first `/job/` segment. `BasePath` MUST be stored verbatim (not URL-decoded) normalized to a leading `/` with no trailing `/`, and MUST be the empty string for URLs mounted at the host root. The presence of a context path MUST NOT change the host-to-credential lookup key, which continues to be scheme + host only. A URL that contains no `/job/` segment MUST continue to be rejected with the existing "not a Jenkins job URL" error.

#### Scenario: Single-segment context path with build number
- **WHEN** the parser receives `https://example.com/domain/job/abc/job/svc/2/`
- **THEN** the parser returns a Ref with host `https://example.com`, `BasePath == "/domain"`, job segments `["abc", "svc"]`, and build number `2`

#### Scenario: Common /jenkins context path on a bare job
- **WHEN** the parser receives `https://ci.example.com/jenkins/job/svc/`
- **THEN** the parser returns a Ref with host `https://ci.example.com`, `BasePath == "/jenkins"`, job segments `["svc"]`, and build number `0`

#### Scenario: Multi-segment context path
- **WHEN** the parser receives `https://example.com/team/ci/job/svc/job/main/42/`
- **THEN** the parser returns a Ref with host `https://example.com`, `BasePath == "/team/ci"`, job segments `["svc", "main"]`, and build number `42`

#### Scenario: Context path with a permalink trailing segment
- **WHEN** the parser receives `https://example.com/jenkins/job/svc/lastSuccessfulBuild/`
- **THEN** the parser returns a Ref with `BasePath == "/jenkins"`, job segments `["svc"]`, `BuildNumber == 0`, and `BuildPermalink == "lastSuccessfulBuild"`

#### Scenario: Root-mounted URL yields an empty BasePath
- **WHEN** the parser receives `https://jenkins.foo.com/job/svc/42/`
- **THEN** the parser returns a Ref with `BasePath == ""`, job segments `["svc"]`, and build number `42`, identical to the behavior before context-path support

#### Scenario: Credential key excludes the context path
- **WHEN** a command is invoked with `https://example.com/jenkins/job/svc/`
- **THEN** the system looks up credentials using the key `https://example.com` (the `/jenkins` context path is not part of the credential key)

#### Scenario: Context-path URL with no /job/ segment is rejected
- **WHEN** the parser receives `https://example.com/jenkins/view/All/` (a prefix but no `/job/` token)
- **THEN** the parser returns the existing "not a Jenkins job URL" error

#### Scenario: Empty job segment after a context path is rejected
- **WHEN** the parser receives `https://example.com/jenkins/job//job/svc/` (empty job name after the prefix)
- **THEN** the parser returns an error indicating an empty job segment

### Requirement: Format API URLs including the context path prefix

The system SHALL emit the `BasePath` value immediately after the host and before the first `/job/` segment when formatting Jenkins API URLs from a Ref, so that requests address the Jenkins instance at its actual context path. When `BasePath` is empty the formatted URL MUST be identical to the root-mounted form.

#### Scenario: APIPath includes the context path and build number
- **WHEN** a Ref has host `https://example.com`, `BasePath == "/domain"`, `JobSegments == ["abc", "svc"]`, and `BuildNumber == 2`
- **THEN** `APIPath("api/json")` returns `https://example.com/domain/job/abc/job/svc/2/api/json`

#### Scenario: APIPath with empty BasePath is unchanged
- **WHEN** a Ref has host `https://jenkins.foo.com`, `BasePath == ""`, and `JobSegments == ["svc"]`
- **THEN** `APIPath("api/json")` returns `https://jenkins.foo.com/job/svc/api/json`

#### Scenario: Context-path URL round-trips through APIPath
- **WHEN** the parser parses `https://example.com/jenkins/job/svc/job/main/42` and the result is re-rendered via `APIPath("")`
- **THEN** the output equals the input URL `https://example.com/jenkins/job/svc/job/main/42`

