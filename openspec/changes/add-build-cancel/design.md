## Context

`jk` currently supports triggering builds, watching status, and responding to input steps, but provides no way to stop a running build. Users must fall back to the Jenkins UI for cancellation. The Jenkins REST API exposes `POST /job/<name>/<n>/stop` which requests a graceful termination; Jenkins marks the build `ABORTED` asynchronously.

## Goals / Non-Goals

**Goals:**
- Add `jk build cancel <build-url>` that POSTs to the Jenkins stop endpoint
- Add `--wait` flag that polls `jk build status` until the build reaches a terminal state (reusing the existing `watchBuild` helper)
- Emit a YAML confirmation document on success

**Non-Goals:**
- Force-kill (`/kill` endpoint) — too destructive and rarely needed
- Cancelling queued (not yet started) builds — separate endpoint, separate UX; deferred
- `--watch` timeout configuration — inherits global `--timeout`

## Decisions

### D1: Use `POST /stop`, not `/kill`

Jenkins exposes two termination endpoints: `/stop` (graceful, sets result to `ABORTED`) and `/kill` (forceful, for stuck builds). `/stop` is the right default — it mirrors what the UI "Stop" button does and gives the build a chance to run `post { always {} }` cleanup blocks. `/kill` is not exposed.

### D2: `--wait` uses a dedicated poll loop, not `watchBuild`

`watchBuild` (used by `build trigger --watch`) treats `PENDING_INPUT` as a terminal state and exits with code 4 — correct for trigger, where a build paused on `input` is genuinely waiting for the user. But for `cancel --wait` this is wrong: after the stop request, the build is being aborted, and in the brief window before Jenkins records `ABORTED` it may still report `PENDING_INPUT`. Reusing `watchBuild` would make `cancel --wait` exit 4 instead of 3 (confirmed in e2e against the `deploy-input` harness).

So `cancel --wait` uses its own poll loop (`waitForBuildDone`) that treats only `DONE` as terminal — `PENDING_INPUT` keeps polling until the abort completes. The loop reuses `watchPollIntervalFor` (same 2s→10s cadence) and `buildResultToExit` (same exit-code mapping) to stay consistent with `watchBuild` without inheriting its input-pending semantics. It is simpler than `watchBuild`: no wfapi enrichment, no pending-input notice.

**Alternative considered**: add a `treatPendingInputAsTerminal bool` parameter to `watchBuild`. Rejected — it muddies an already-busy function signature for a single caller, and the cancel loop is small enough that duplication is cheaper than the coupling.


### D3: Output shape

On success (without `--wait`) emit:

```yaml
schemaVersion: "1"
buildUrl: <url>
buildNumber: <n>
state: RUNNING   # state at the moment cancel was requested
```

With `--wait`, the dedicated poll loop emits progress to stderr and the command exits with the build-result exit code (3 for ABORTED) — no YAML document is rendered, mirroring `build trigger --watch`.

### D4: HTTP 200 vs other status codes

Jenkins `/stop` returns HTTP 200 even when the build has already finished. A 404 means the build URL is wrong (handled by `translateBuildClientError`). No special-casing needed for "build already done" — the user gets a 200 and the YAML shows the current state.

### D5: Accept permalinks

`cancel` accepts a build permalink (`.../lastBuild/`, `.../lastSuccessfulBuild/`, …) in the build slot, consistent with `build status` and `build params`. Jenkins serves `/lastBuild/stop` correctly, so the client guard rejects only a Ref that addresses neither a numeric build nor a permalink. `--wait` reads the resolved numeric build number from the status response, so exit-code mapping is unaffected by permalink addressing.

## Risks / Trade-offs

- **Asynchronous termination**: `/stop` is a request, not a guarantee. The build may take seconds to actually stop. Without `--wait`, the emitted `state` may still be `RUNNING`. The YAML output intentionally reflects the state *at the time of the request* to be honest about this.
- **No idempotency concern**: POSTing `/stop` on an already-finished build returns 200 without error. This is acceptable behaviour.
- **`buildNumber: 0` in the degraded path**: if the stop POST succeeds but the follow-up status fetch fails *and* the user addressed the build by permalink, the fallback document reports `buildNumber: 0` (the permalink was never resolved to a number). This is a rare triple-condition path on an experimental field; acceptable.
