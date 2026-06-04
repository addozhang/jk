## ADDED Requirements

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
