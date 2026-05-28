## ADDED Requirements

### Requirement: Accept permalink URLs for build-scoped commands

The system SHALL allow build-scoped commands (`build status`, `build stages`, `build logs`, `build input`, `build params`) to operate on URLs whose trailing segment is a Jenkins permalink (`lastBuild`, `lastSuccessfulBuild`, `lastFailedBuild`, `lastStableBuild`, `lastUnstableBuild`, `lastUnsuccessfulBuild`, `lastCompletedBuild`). The Jenkins server SHALL resolve the permalink to a concrete build for each command invocation; the resolved build's numeric identifier MUST appear in any output that records the build number.

#### Scenario: Status for lastBuild URL
- **WHEN** the user runs `jk build status http://jenkins.foo.com/job/svc/lastBuild/`
- **THEN** the command fetches `<host>/job/svc/lastBuild/api/json`, the response carries the resolved numeric build number, and the output records that number as the build identity

#### Scenario: Status for lastSuccessfulBuild URL when one exists
- **WHEN** the user runs `jk build status http://jenkins.foo.com/job/svc/lastSuccessfulBuild/` and the pipeline has at least one successful build
- **THEN** the command returns the status of the most recent successful build with its actual numeric identifier

#### Scenario: Permalink URL skips the lastBuild existence pre-flight
- **WHEN** the user runs any build-scoped command against a permalink URL
- **THEN** the client MUST NOT issue the `<job>/api/json?tree=lastBuild[number]` pre-flight check (which is only used for "no build specified" defaulting); a 404 from Jenkins on the permalink path surfaces as the normal HTTP error

#### Scenario: Permalink URL with no matching build
- **WHEN** the user runs `jk build status http://jenkins.foo.com/job/svc/lastSuccessfulBuild/` and the pipeline has no successful builds yet
- **THEN** the command surfaces the Jenkins HTTP 404 as an error rather than reporting a synthetic "never built" message

### Requirement: Return parameter values used to trigger a specific build

The system SHALL provide a `jk build params <build-url>` command that returns the trigger-time parameter values of the specified build as a `BuildParams` payload (`{schemaVersion, buildUrl, buildNumber, parameters[]}`). Each `parameters[]` entry MUST be a `BuildParameter` object with the shape `{name: string, value: string|boolean|null}` — distinct from the `Parameter` shape returned by `jk pipeline params` (which describes parameter *definitions*: type, default, choices, description), because `build params` reports the *submitted values* of one specific build. Builds with no parameters MUST return an empty `parameters` array, not an error. Parameter values that Jenkins reports as null (e.g. redacted password / credentials parameters) MUST be preserved as null in the output.

#### Scenario: Build with parameters
- **WHEN** the user runs `jk build params http://jenkins.foo.com/job/svc/42/` against a build triggered with `ENV=prod` and `DRY_RUN=false`
- **THEN** the output's `parameters` array contains entries `{name: "ENV", value: "prod"}` and `{name: "DRY_RUN", value: "false"}` (order matching Jenkins' `actions[].parameters` order) and `buildNumber == 42`

#### Scenario: Build with no parameters returns empty array
- **WHEN** the user runs `jk build params http://jenkins.foo.com/job/hello/3/` against an unparameterized build
- **THEN** the command exits 0 and the output's `parameters` field is `[]` (not null, not an error)

#### Scenario: Permalink URL accepted
- **WHEN** the user runs `jk build params http://jenkins.foo.com/job/svc/lastSuccessfulBuild/`
- **THEN** the command resolves the permalink server-side, returns the parameter values for that build, and `buildNumber` records the resolved numeric build number

#### Scenario: Redacted parameter preserved as null
- **WHEN** a build was triggered with a `password`-type parameter named `API_TOKEN` and Jenkins returns `{name: "API_TOKEN", value: null}` in `actions[].parameters`
- **THEN** the output preserves `{name: "API_TOKEN", value: null}` without substituting an empty string or omitting the entry

#### Scenario: Pipeline that has never been built
- **WHEN** the user runs `jk build params http://jenkins.foo.com/job/new-svc/lastBuild/` against a pipeline that has never built
- **THEN** the command surfaces the Jenkins HTTP 404 as an error
