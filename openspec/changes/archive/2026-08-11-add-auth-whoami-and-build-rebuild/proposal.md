## Why

Users need a safe way to verify which Jenkins identity their stored credentials select and to re-run a specific historical build without manually reconstructing its parameters.

## What Changes

- Add `jk auth whoami <url>` to query Jenkins's authenticated identity without exposing credentials.
- Add `jk build rebuild <build-url>` to trigger the same pipeline with the specified build's recorded parameters.
- Reject rebuilds when recorded parameter values are unavailable or no longer defined by the pipeline.
- Preserve existing URL-as-identity, context-path, output, timeout, TLS, and error-handling behavior.

## Capabilities

### New Capabilities

- `build-rebuild`: Re-triggering a specified historical build with validated recorded parameters.

### Modified Capabilities

- `auth`: Add authenticated identity inspection for a Jenkins instance.

## Impact

- CLI command registration and orchestration under `internal/cli`.
- Jenkins API client identity and build-parameter calls under `internal/jenkins`.
- Additive experimental output schema for authenticated identity.
- User-facing command documentation and tests; no new dependencies or credential-file changes.
