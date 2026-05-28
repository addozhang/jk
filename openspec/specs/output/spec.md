# output Specification

## Purpose

Rendering `jk`'s self-owned schema as YAML or JSON, the `-o raw` passthrough escape hatch, the top-level `schemaVersion` field, and the documented field conventions (camelCase, `Ms`/`Utc` suffixes, uppercase enums, explicit nulls).

## Requirements

### Requirement: Render output as YAML by default

The system SHALL render all command output as YAML by default. The YAML output MUST follow the `jk` self-owned schema documented in `docs/schema.md`, not the raw Jenkins API field names.

#### Scenario: Default output is YAML
- **WHEN** the user runs any read command without specifying `-o`
- **THEN** the response is printed to stdout as a YAML document

#### Scenario: Field names follow schema conventions
- **WHEN** any command renders YAML output
- **THEN** field names use camelCase, durations use the `Ms` suffix on integer milliseconds, timestamps use the `Utc` suffix on ISO 8601 UTC strings, and enums are uppercase string constants

### Requirement: Support JSON output via `-o json`

The system SHALL support a `-o json` / `--output json` flag that renders the same self-owned schema as compact JSON to stdout.

#### Scenario: JSON output for a status command
- **WHEN** the user runs `jk build status <url> -o json`
- **THEN** the response is printed as JSON whose structure matches the YAML output exactly, with the same field names, types, and `schemaVersion`

### Requirement: Support raw API passthrough via `-o raw`

The system SHALL support a `-o raw` / `--output raw` flag that bypasses the self-owned schema layer and prints the underlying Jenkins API response body verbatim. The `-o raw` flag is the escape hatch for fields that `jk`'s schema does not expose.

#### Scenario: Raw output for a status command
- **WHEN** the user runs `jk build status <url> -o raw`
- **THEN** the response printed to stdout is the original Jenkins API response body (typically JSON), without re-rendering and without `schemaVersion` injection

### Requirement: Include `schemaVersion` in every schema-rendered response

The system SHALL include a top-level `schemaVersion` field as the first key in every YAML or JSON response. The field value MUST be a string identifying the current schema major version (initial value: `"1"`). The field MUST NOT be present when `-o raw` is used.

#### Scenario: Schema version in YAML output
- **WHEN** the user runs any command with default YAML output
- **THEN** the response begins with `schemaVersion: "1"` on the first non-comment line

#### Scenario: Schema version omitted from raw output
- **WHEN** the user runs any command with `-o raw`
- **THEN** the response does not contain a `schemaVersion` field injected by `jk`

### Requirement: Use explicit `null` for missing values

The system SHALL emit `null` (not omit the field, not emit an empty string) for any schema field whose value is unavailable for the current response. Consumers MUST be able to assume that a field defined in `docs/schema.md` is always present on every response of the relevant command.

#### Scenario: Null result on a running build
- **WHEN** `jk build status <url>` returns the status of a still-running build whose `result` is not yet determined
- **THEN** the rendered output contains the literal field `result: null` rather than omitting `result` or using an empty string

### Requirement: Tag fields as `stable` or `experimental` in schema documentation

The system SHALL maintain `docs/schema.md` as the authoritative output contract. Every field defined by the schema MUST be tagged either `stable` or `experimental`. Only `stable` fields carry the version-compatibility promise: they will not be removed, renamed, or have their type changed without bumping `schemaVersion`. `experimental` fields MAY change without notice within a major schema version.

#### Scenario: Schema document exists and tags every field
- **WHEN** a maintainer inspects `docs/schema.md`
- **THEN** every field referenced by any command's output appears in `docs/schema.md` with an explicit `stable` or `experimental` tag and a one-line description
