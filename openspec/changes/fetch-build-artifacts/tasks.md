## 1. Artifact Metadata and URLs

- [x] 1.1 Add Jenkins response models and client logic for listing resolved build identity and artifact metadata with the filtered build API query.
- [x] 1.2 Add segment-safe artifact content URL construction and tests for nested paths, spaces, reserved characters, numeric builds, and permalinks.
- [x] 1.3 Add schema types and mapping for stable YAML/JSON artifact-list output, including an empty artifact array.

## 2. Streaming Downloads

- [x] 2.1 Add an authenticated, redirect-aware client method that streams one artifact response into an `io.Writer` without using the JSON response-size limit.
- [x] 2.2 Change HTTP debug handling so artifact response bodies are neither buffered nor printed while request and response metadata remain available.
- [x] 2.3 Add client tests for successful streaming, HTTP errors, redirects, cancellation, and large responses.

## 3. Safe Local Storage

- [x] 3.1 Implement artifact relative-path validation and destination confinement, including absolute paths, traversal components, NUL bytes, and symlinked parents.
- [x] 3.2 Implement same-directory temporary-file downloads, cleanup on every failure, close-before-rename, and atomic final installation.
- [x] 3.3 Implement default collision rejection and `--force` replacement for regular files while always rejecting directory and symlink destinations.
- [x] 3.4 Add filesystem tests for nested directory creation, traversal attempts, symlink escape, existing destinations, forced replacement, and interrupted downloads.

## 4. Build Commands

- [x] 4.1 Add `jk build artifacts <build-url>` with structured output and existing build-reference/error semantics.
- [x] 4.2 Add `jk build artifact <build-url> <relative-path> --destination <file> [--force]`, including exact metadata membership validation before content fetch.
- [x] 4.3 Add `jk build artifacts fetch <build-url> --directory <dir> [--force]` with sequential, fail-fast downloads preserving artifact relative paths.
- [x] 4.4 Add CLI tests for argument validation, empty builds, output formats, permalink resolution, missing artifacts, fail-fast behavior, and actionable errors.

## 5. End-to-End Verification

- [x] 5.1 Extend the Jenkins test fixture or Pipeline to archive nested binary and report artifacts.
- [x] 5.2 Add end-to-end coverage for listing, single download, bulk download, preserved paths, content integrity, authentication, and overwrite protection.
- [x] 5.3 Run formatting, unit tests, race-enabled tests if available, and the project validation suite; fix all regressions.
