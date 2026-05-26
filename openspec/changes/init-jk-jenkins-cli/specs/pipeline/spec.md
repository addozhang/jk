## ADDED Requirements

### Requirement: Get pipeline info

The system SHALL provide a `jk pipeline info <url>` command that returns metadata about a Jenkins pipeline, including its name, full display name, description, URL, whether it is buildable, the URL and number of its most recent build, and (for multibranch jobs) the list of available branches.

#### Scenario: Single-branch pipeline info
- **WHEN** the user runs `jk pipeline info https://jenkins.foo.com/job/svc/`
- **THEN** the command returns a YAML document containing at minimum: `schemaVersion`, `name`, `fullName`, `url`, `description`, `buildable`, `lastBuild` (with `number` and `url`, or `null` if none)

#### Scenario: Multibranch pipeline info
- **WHEN** the user runs `jk pipeline info` against a multibranch pipeline URL
- **THEN** the response additionally includes a `branches` array listing each branch's name and URL

#### Scenario: Pipeline not found
- **WHEN** the user runs `jk pipeline info` against a URL that does not resolve to an existing pipeline
- **THEN** the command exits with a non-zero code and prints a human-readable error suggesting to list pipelines with `jk pipeline list <parent-folder-url>`

### Requirement: Get pipeline parameter definitions

The system SHALL provide a `jk pipeline params <url>` command that returns the parameter definitions for a pipeline as a list. Each parameter entry MUST include the parameter name, type, default value (or `null` if none), description, and (for choice parameters) the list of allowed choices.

#### Scenario: Pipeline with parameters
- **WHEN** the user runs `jk pipeline params` against a pipeline that defines parameters `BRANCH` (string, default `main`) and `ENV` (choice, choices `dev|prod`, default `dev`)
- **THEN** the command returns a YAML document with a `parameters` array containing one entry per parameter, each with `name`, `type`, `default`, `description`, and (for `ENV`) a `choices` field

#### Scenario: Pipeline with no parameters
- **WHEN** the user runs `jk pipeline params` against a pipeline that has no parameter definitions
- **THEN** the command returns a YAML document with an empty `parameters` array and exits with code `0`

### Requirement: List pipelines under a folder

The system SHALL provide a `jk pipeline list <folder-url>` command that returns the list of pipelines (and sub-folders) directly contained in a Jenkins folder. Each entry MUST include the name, type (`pipeline` or `folder`), URL, and (for pipelines) the last build URL and result.

#### Scenario: Listing pipelines in a folder
- **WHEN** the user runs `jk pipeline list https://jenkins.foo.com/job/team/`
- **THEN** the command returns a YAML document with an `items` array containing one entry per child, each with `name`, `type`, `url`, and (for pipelines) `lastBuild`

#### Scenario: URL is not a folder
- **WHEN** the user runs `jk pipeline list` against a URL that points to a pipeline (not a folder)
- **THEN** the command exits with a non-zero code and prints an error message suggesting `jk pipeline info` instead
