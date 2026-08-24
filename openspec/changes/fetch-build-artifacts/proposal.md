## Why

Users can inspect and operate Jenkins builds with `jk`, but cannot retrieve files archived by those builds. This forces them to leave the CLI or manually construct authenticated Jenkins artifact URLs, including for reports published as archived files.

## What Changes

- Add a build command that lists archived artifacts with their file names and relative paths.
- Add a build command that downloads one archived artifact by relative path.
- Add a build command that downloads all archived artifacts into a local directory while preserving their relative paths.
- Stream artifact content to disk instead of buffering complete files in memory.
- Protect local files with path validation, temporary-file writes, and explicit overwrite behavior.
- Treat reports as ordinary archived artifacts; no report-plugin-specific APIs or discovery are introduced.

## Capabilities

### New Capabilities
- `build-artifacts`: List and download files archived by a Jenkins build, including safe local destination handling.

### Modified Capabilities
- `build`: Expose artifact operations under the existing build command and accept the same supported build references.

## Impact

- Adds subcommands under `jk build` and corresponding structured artifact-list output.
- Extends the Jenkins client with artifact metadata and streaming download requests.
- Extends build reference URL construction for artifact API and content paths.
- Adds local filesystem writes and tests for traversal, collisions, partial downloads, authentication, and HTTP failures.
- Requires no new external dependency or Jenkins plugin; artifacts must already have been archived by Jenkins.
