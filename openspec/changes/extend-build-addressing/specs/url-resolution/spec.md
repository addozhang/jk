## ADDED Requirements

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
