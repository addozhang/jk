## ADDED Requirements

### Requirement: List archived build artifacts
The system SHALL provide `jk build artifacts <build-url>` and return a structured payload containing `schemaVersion`, the resolved `buildUrl`, the resolved `buildNumber`, and an `artifacts` array. Each artifact entry MUST contain `fileName` and `relativePath`, and its order MUST match Jenkins's response.

#### Scenario: Build contains archived artifacts
- **WHEN** the user runs `jk build artifacts http://jenkins/job/svc/42/` and Jenkins reports `dist/app.zip` and `reports/index.html`
- **THEN** the command exits with code `0` and returns both entries with their file names and relative paths

#### Scenario: Build contains no archived artifacts
- **WHEN** the user runs `jk build artifacts <build-url>` and Jenkins reports an empty artifact collection
- **THEN** the command exits with code `0` and returns `artifacts: []`

#### Scenario: Artifact listing uses configured output format
- **WHEN** the user runs `jk build artifacts <build-url> --output json`
- **THEN** the command emits the artifact payload as JSON using the same schema fields as the default YAML output

### Requirement: Download one archived artifact
The system SHALL provide `jk build artifact <build-url> <relative-path> --destination <file>`. The requested relative path MUST exactly match an artifact returned by Jenkins for that build before content is requested. The system MUST stream the artifact response to a temporary file in the destination directory and make the destination visible only after the complete download is closed successfully.

#### Scenario: Download one artifact
- **WHEN** the user runs `jk build artifact <build-url> dist/app.zip --destination ./app.zip` and that artifact exists
- **THEN** the command downloads `<build-url>/artifact/dist/app.zip`, atomically installs it as `./app.zip`, and exits with code `0`

#### Scenario: Requested artifact is not in the build metadata
- **WHEN** the user requests `dist/missing.zip` and Jenkins's artifact list does not contain that exact relative path
- **THEN** the command exits non-zero without issuing an artifact-content request and identifies the missing artifact

#### Scenario: Download follows an Artifact Manager redirect
- **WHEN** Jenkins redirects the artifact-content request to external artifact storage
- **THEN** the command follows the redirect according to the HTTP client's redirect policy and writes the returned content without forwarding Jenkins credentials to an unrelated host

#### Scenario: Interrupted single-file download
- **WHEN** the artifact response fails or the command is interrupted before copying and closing completes
- **THEN** the command exits non-zero, removes its temporary file, and does not create or replace the final destination

### Requirement: Download all archived artifacts
The system SHALL provide `jk build artifacts fetch <build-url> --directory <dir>`. It MUST list the build's artifacts and download each artifact sequentially beneath the destination directory while preserving `relativePath`. It MUST stop on the first failed artifact and identify that artifact in the error.

#### Scenario: Fetch all artifacts with directory structure
- **WHEN** Jenkins lists `dist/app.zip` and `reports/index.html` and the user fetches them into `./out`
- **THEN** the command creates `./out/dist/app.zip` and `./out/reports/index.html` with the corresponding content

#### Scenario: Fetch a build with no artifacts
- **WHEN** the user fetches all artifacts from a build whose artifact list is empty
- **THEN** the command exits with code `0` without creating artifact files

#### Scenario: Later artifact fails
- **WHEN** one artifact downloads successfully and the next artifact request fails
- **THEN** the command retains the completed first artifact, leaves no final or temporary file for the failed artifact, stops without requesting later artifacts, and exits non-zero

### Requirement: Protect existing local paths
Artifact download commands MUST reject an existing destination by default. They SHALL accept `--force` to replace an existing regular file only after the replacement has downloaded completely. They MUST reject a destination that is a directory or symbolic link even when `--force` is supplied.

#### Scenario: Destination already exists
- **WHEN** the destination is an existing regular file and the user does not supply `--force`
- **THEN** the command exits non-zero before downloading and leaves the existing file unchanged

#### Scenario: Force replaces a regular file
- **WHEN** the destination is an existing regular file and the user supplies `--force`
- **THEN** the existing file remains unchanged until the new content is complete and is then replaced by the completed artifact

#### Scenario: Destination is a symlink
- **WHEN** the destination is a symbolic link and the user supplies `--force`
- **THEN** the command exits non-zero and does not modify the link or its target

### Requirement: Confine bulk downloads to the destination directory
The system MUST treat Jenkins artifact relative paths as untrusted input. Before writing, it MUST reject empty paths, absolute paths, NUL bytes, `.` or `..` path components, normalized paths outside the requested destination, and paths traversing an existing symbolic-link parent.

#### Scenario: Artifact path contains parent traversal
- **WHEN** Jenkins reports an artifact path such as `reports/../../secret.txt`
- **THEN** bulk fetch exits non-zero without creating any file for that artifact outside or inside the destination

#### Scenario: Artifact path traverses a symlinked directory
- **WHEN** `out/reports` is a symbolic link and Jenkins reports `reports/index.html`
- **THEN** bulk fetch exits non-zero without writing through the symbolic link

#### Scenario: Artifact path uses normal nested components
- **WHEN** Jenkins reports `reports/unit/index.html` and all existing parent components are real directories
- **THEN** bulk fetch creates missing directories beneath the requested destination and safely writes the artifact

### Requirement: Keep artifact downloads memory bounded
The system MUST stream artifact response bodies rather than reading complete files into memory. Debug logging MUST NOT buffer or print artifact response bodies.

#### Scenario: Download a large artifact
- **WHEN** the user downloads an artifact larger than the JSON response-size limit
- **THEN** the command completes through streaming without rejecting it because of the JSON size limit

#### Scenario: Debug logging during artifact download
- **WHEN** the user downloads an artifact with `--debug`
- **THEN** debug output may include request and response metadata but does not contain or buffer the artifact body
