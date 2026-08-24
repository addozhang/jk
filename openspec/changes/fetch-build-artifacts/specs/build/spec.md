## ADDED Requirements

### Requirement: Accept supported build references for artifact commands
The system SHALL allow `build artifacts`, `build artifact`, and `build artifacts fetch` to operate on the same numeric and Jenkins permalink build URLs supported by other build-scoped commands. For permalink input, Jenkins SHALL resolve the build and structured output MUST report its concrete numeric build identity.

#### Scenario: List artifacts through a permalink
- **WHEN** the user runs `jk build artifacts http://jenkins/job/svc/lastSuccessfulBuild/`
- **THEN** the command queries that permalink's build API and reports the resolved numeric `buildNumber` and concrete `buildUrl`

#### Scenario: Download through a permalink
- **WHEN** the user runs an artifact download command with `http://jenkins/job/svc/lastBuild/`
- **THEN** the command lists and downloads artifacts from the build resolved by Jenkins without performing the pipeline `lastBuild` pre-flight check

#### Scenario: Permalink has no matching build
- **WHEN** an artifact command targets `lastSuccessfulBuild` for a pipeline with no successful build
- **THEN** the command surfaces Jenkins's HTTP 404 through the normal build-not-found error handling
