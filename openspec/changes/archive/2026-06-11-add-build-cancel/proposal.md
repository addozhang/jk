## Why

Users can trigger builds and respond to input steps, but have no way to stop a running build from the CLI. This forces them to switch to the Jenkins UI for a fundamental operational task.

## What Changes

- Add `jk build cancel <build-url>` command that stops a running build
- Add `--wait` flag to block until the build reaches a terminal state after cancellation

## Capabilities

### New Capabilities

- `build-cancel`: Cancel a running Jenkins build via `jk build cancel <build-url>`, with an optional `--wait` flag to poll until the build finishes

### Modified Capabilities

- `build`: Add the cancel command to the build command group and document exit-code behaviour for the `ABORTED` result

## Impact

- `internal/cli/build.go`: new `newBuildCancelCommand` / `runBuildCancel` functions
- `internal/jenkins/client.go`: new `CancelBuild` method (`POST /job/<name>/<n>/stop`)
- `openspec/specs/build/spec.md`: new requirement section for cancel
