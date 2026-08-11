## ADDED Requirements

### Requirement: Inspect authenticated Jenkins identity

The system SHALL provide `jk auth whoami <url>` that queries the Jenkins instance selected by the URL and emits `authenticated`, `name`, and `authorities` without exposing stored credentials. The URL MAY be a Jenkins root URL or job URL and MUST preserve any context path preceding `/job/`.

#### Scenario: Authenticated identity
- **WHEN** the selected credentials authenticate as `alice`
- **THEN** the command emits `authenticated: true`, `name: alice`, and Jenkins-reported authorities

#### Scenario: Anonymous identity
- **WHEN** Jenkins returns a successful whoAmI response with `authenticated: false`
- **THEN** the command emits that identity and exits successfully

#### Scenario: Missing identity endpoint
- **WHEN** Jenkins returns HTTP 404 for the whoAmI endpoint
- **THEN** the command returns an identity-specific error and MUST NOT describe a missing pipeline

#### Scenario: Context-path identity
- **WHEN** the command receives a job URL under a Jenkins context path
- **THEN** it queries `<origin><context-path>/whoAmI/api/json` using the matching context-path credential
