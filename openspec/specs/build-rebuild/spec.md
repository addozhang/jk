# build-rebuild Specification

## Purpose

Re-triggering a specified historical Jenkins build with its recorded parameters while preventing unsafe parameter drift or secret omission.

## Requirements
### Requirement: Re-trigger a specified build

The system SHALL provide `jk build rebuild <build-url>` that reads the specified build's recorded parameters, validates them against the pipeline's current parameter definitions, triggers the same pipeline, and emits the standard build-trigger response. Numeric and permalink build URLs and Jenkins context paths MUST be supported.

#### Scenario: Parameterized rebuild
- **WHEN** the specified build recorded supported scalar parameters that remain defined
- **THEN** the command triggers the same pipeline with those values and reports the new queue and build identifiers

#### Scenario: Unparameterized rebuild
- **WHEN** the specified build has no recorded parameters
- **THEN** the command triggers the pipeline's unparameterized build endpoint

#### Scenario: Redacted parameter
- **WHEN** a recorded parameter value is null because Jenkins redacted it
- **THEN** the command exits without triggering and directs the user to supply explicit values with `jk build trigger`

#### Scenario: Parameter definition drift
- **WHEN** a recorded parameter name is no longer defined by the pipeline
- **THEN** the command exits without triggering and identifies the obsolete parameter

#### Scenario: Permalink and context path
- **WHEN** the specified build uses a Jenkins permalink under a context-path-mounted instance
- **THEN** the command reads the permalink build and triggers the corresponding pipeline under the same context path
