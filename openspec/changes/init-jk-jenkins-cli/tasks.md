## 1. Spike & validation (do first — kill or scope down before broad work)

- [ ] 1.1 Spike: call `/wfapi/runs/{id}/describe` against a real running pipeline; confirm stage tree shape including parallel branches and capture two real samples in `docs/spikes/wfapi-stages/`
- [ ] 1.2 Spike: trigger a pipeline with a Pipeline `input` step; identify the exact submit endpoint, payload shape, and input-id semantics; capture in `docs/spikes/wfapi-input/`
- [ ] 1.3 Spike: fetch per-stage logs via `wfapi` against a multi-stage build; capture endpoint + truncation behavior in `docs/spikes/wfapi-logs/`
- [ ] 1.4 Collect 10 real Jenkins URLs (top-level, folders, multibranch, with/without build numbers, with/without trailing slashes) into `docs/spikes/urls.txt` for parser conformance testing
- [ ] 1.5 Decision review: if any spike reveals a blocker, update `design.md` and re-scope the affected requirements before continuing

## 2. Project scaffolding

- [ ] 2.1 Initialize Go module (`go mod init github.com/addozhang/jk`), set Go version, commit `.gitignore`
- [ ] 2.2 Create directory skeleton: `cmd/jk/`, `internal/cli/`, `internal/jenkins/`, `internal/jenkinsurl/`, `internal/auth/`, `internal/schema/`, `internal/output/`, `internal/errors/`, `docs/`
- [ ] 2.3 Add dependencies: `spf13/cobra`, `sigs.k8s.io/yaml`, `BurntSushi/toml`
- [ ] 2.4 Wire root `cmd/jk/main.go` with Cobra root command + global flags (`-o/--output`, `--insecure`, `--timeout`, `--debug`) and `jk version` subcommand
- [ ] 2.5 Add Makefile / `justfile` with `build`, `test`, `lint`, `release-snapshot` targets
- [ ] 2.6 Configure linter (`golangci-lint`) and pre-commit hooks

## 3. Schema documentation (the public contract — write before code)

- [ ] 3.1 Create `docs/schema.md` §1: versioning policy (semver-style rules for `schemaVersion` bumps; what counts as breaking)
- [ ] 3.2 `docs/schema.md` §2: field conventions (camelCase, `Ms` and `Utc` suffixes, uppercase enums, explicit `null`, stable/experimental tagging)
- [ ] 3.3 `docs/schema.md` §3: full schema for every MVP command response (every field, type, stability tier, one-line description)
- [ ] 3.4 `docs/schema.md` §4: enum value catalog (`SUCCESS`, `FAILURE`, `ABORTED`, `UNSTABLE`, `RUNNING`, `QUEUED`, `PENDING_INPUT`, `DONE`) with semantics
- [ ] 3.5 Cross-check: every field referenced in any spec scenario appears in `docs/schema.md`

## 4. URL resolution (`internal/jenkinsurl/`)

- [x] 4.1 Define `Ref` struct (`Host`, `JobSegments`, `BuildNumber`)
- [x] 4.2 Implement `Parse(rawURL string) (*Ref, error)` covering all shapes from `docs/spikes/urls.txt` and the `url-resolution` spec scenarios
- [x] 4.3 Implement `Ref.HostKey() string` for credential lookup (scheme + host + non-default port only; default ports `:80`/`:443` stripped)
- [x] 4.4 Implement `Ref.APIPath(suffix string) string` to build Jenkins API URLs from a `Ref` (joins all `JobSegments` with `/job/` and appends optional `/<BuildNumber>` when non-zero)
- [x] 4.5 Unit tests: every URL in `docs/spikes/urls.txt` parses correctly; rejected URLs produce clear errors

## 5. Auth and credentials (`internal/auth/`)

- [x] 5.1 Define credentials file format (TOML, `[hosts."<key>"] token = "..."` table) at `~/.config/jk/credentials`
- [x] 5.2 Implement `Store` interface: `Add(host, token)`, `Get(host) (token, error)`, `List() []string`, `Remove(host)`
- [x] 5.3 Enforce `0600` file mode on write (POSIX); document Windows ACL caveat
- [x] 5.4 Implement `jk auth add <host>` (prompt for token via stdin; suppress echo using `golang.org/x/term`)
- [x] 5.5 Implement `jk auth list` (renders via output layer; never prints tokens)
- [x] 5.6 Add `jk auth remove <host>` (convenience, not in spec but small and natural)
- [x] 5.7 Unit tests for `Store` (round-trip, missing host, overwrite confirmation logic)

## 6. HTTP transport (`internal/jenkins/transport.go`)

- [x] 6.1 Build `http.Client` factory that respects `--timeout`, `--insecure`, and `SSL_CERT_FILE`
- [x] 6.2 Implement `SSL_CERT_FILE` loader: read file, augment system CA pool with PEM certs; return explicit error on missing/unparseable file (mapping to the `tls-and-transport` scenarios)
- [x] 6.3 Implement debug `http.RoundTripper` wrapper that logs request/response to stderr, redacting `Authorization`
- [x] 6.4 Implement `Authorization` injection middleware sourced from `internal/auth.Store`
- [x] 6.5 Integration tests via `httptest.Server` covering: success, 401, 403, 404, timeout, TLS verify failure with and without `SSL_CERT_FILE`

## 7. CSRF crumb handling (`internal/jenkins/crumb.go`)

- [x] 7.1 Implement crumb fetcher: `GET /crumbIssuer/api/json`, parse `crumb` + `crumbRequestField`
- [x] 7.2 Implement in-memory per-host crumb cache (process lifetime)
- [x] 7.3 Cache "CSRF disabled" verdict when crumb endpoint returns 404; skip crumb headers thereafter
- [x] 7.4 Implement single-retry recovery on 403 with CSRF-indicative body
- [x] 7.5 Wire crumb logic into the HTTP client so all state-changing requests pass through it automatically
- [x] 7.6 Unit tests with `httptest.Server`: cold fetch, expired-then-refresh, CSRF disabled, repeated failure (no infinite loop)

## 8. Error translation (`internal/errors/`)

- [x] 8.1 Define `JKError` type (`Code`, `Message`, `Suggestion`, wrapped underlying error)
- [x] 8.2 Implement translators for: HTTP 401/403, 404, network timeout, TLS errors, `SSL_CERT_FILE` problems, CSRF unrecoverable, malformed Jenkins response
- [x] 8.3 Implement top-level error printer in `cmd/jk/main.go` that renders translated message to stderr and selects exit code (>=10 for jk-level failures)
- [x] 8.4 Wire `--debug` to additionally dump raw exchange before the translated message
- [x] 8.5 Unit tests asserting each translator produces the message text required by the `errors` spec scenarios

## 9. Output rendering (`internal/output/`)

- [x] 9.1 Implement `Render(v any, format string) ([]byte, error)` supporting `yaml` (default), `json`, and `raw` (passthrough `[]byte`)
- [x] 9.2 Inject `schemaVersion: "1"` as the first key for `yaml` and `json` formats; omit for `raw`
- [x] 9.3 Configure marshaling to emit `null` for `nil` values (no `omitempty`) on all schema types
- [ ] 9.4 Validate enum string values at marshal time (assert uppercase, no typos) via a small helper
- [x] 9.5 Unit tests covering all three formats, schemaVersion ordering, and null handling

## 10. Schema types (`internal/schema/`)

- [x] 10.1 Define types matching `docs/schema.md` §3: `PipelineInfo`, `PipelineParams`, `PipelineList`, `BuildTrigger`, `BuildStatus`, `BuildStages`, `BuildInputResult`, `PendingInput`, `Stage`
- [x] 10.2 Define enum constants for `BuildResult`, `BuildState` (uppercase strings)
- [x] 10.3 Add struct tags for both `yaml` and `json` ensuring identical output
- [x] 10.4 Add doc comments per field that mirror the `docs/schema.md` description (sets up future `jk explain` generation)
- [ ] 10.5 Compile-time check: every schema field is referenced by exactly one command

## 11. Jenkins API client (`internal/jenkins/client.go`)

- [x] 11.1 Implement `GetPipelineInfo(ctx, ref *Ref) (rawAPIResponse, error)` calling `<ref>/api/json` with relevant `tree=` parameter
- [x] 11.2 Implement `GetPipelineParams(ctx, ref) (raw, error)` via the property definitions on `api/json`
- [x] 11.3 Implement `ListPipelinesInFolder(ctx, ref) (raw, error)` via folder `api/json?tree=jobs[...]`
- [x] 11.4 Implement `TriggerBuild(ctx, ref, params) (queueLocation, error)` (chooses `build` vs `buildWithParameters` based on params presence)
- [x] 11.5 Implement `ResolveQueueItem(ctx, queueLocation) (buildURL, buildNumber, error)` polling the queue item
- [x] 11.6 Implement `GetBuildStatus(ctx, ref) (raw, error)` via build `api/json`
- [x] 11.7 Implement `GetBuildStages(ctx, ref) (raw, error)` via `wfapi/runs/{id}/describe`
- [x] 11.8 Implement `GetPendingInputs(ctx, ref) (raw, error)` via `wfapi` input endpoint identified in spike 1.2
- [x] 11.9 Implement `SubmitInput(ctx, ref, inputID, proceed bool) error`
- [x] 11.10 Implement `StreamConsoleLog(ctx, ref, w io.Writer, follow bool) error` using `logText/progressiveText`
- [x] 11.11 Implement `GetStageLog(ctx, ref, stageName string) ([]byte, error)` via `wfapi` stage log endpoint
- [x] 11.12 Implement `ResolveLastBuild(ctx, ref) (buildNumber int, error)` when `BuildNumber == 0`
- [x] 11.13 Integration tests against `httptest.Server` returning recorded responses from the spikes

## 12. Schema mappers (`internal/schema/mapper.go`)

- [x] 12.1 Map raw `api/json` pipeline response → `schema.PipelineInfo`
- [x] 12.2 Map raw `api/json` property definitions → `schema.PipelineParams`
- [x] 12.3 Map raw folder `jobs` → `schema.PipelineList`
- [x] 12.4 Map raw build `api/json` → `schema.BuildStatus`, computing `state`, `progressPercent`, `pendingInput`
- [x] 12.5 Map raw `wfapi` stage tree → `schema.BuildStages`, handling sequential, parallel, and duplicate-name cases (suffix `#1`, `#2`)
- [x] 12.6 Map raw `wfapi` input list → `schema.PendingInput`
- [x] 12.7 Map enum strings from Jenkins to `jk` enum constants (`null`/missing → `RUNNING` vs `DONE` distinction)
- [x] 12.8 Unit tests covering each mapper with fixtures captured during spikes (using documented Jenkins shapes as proxy fixtures; will be re-verified against real samples once spikes 1.1–1.3 land)

## 13. CLI: `pipeline` commands (`internal/cli/pipeline.go`)

- [x] 13.1 `jk pipeline info <url>`: parse URL → client call → mapper → render
- [x] 13.2 `jk pipeline params <url>`: same chain
- [x] 13.3 `jk pipeline list <folder-url>`: same chain; detect non-folder URL and emit suggestive error
- [x] 13.4 End-to-end tests with `httptest.Server` covering each command's spec scenarios

## 14. CLI: `build` commands (`internal/cli/build.go`)

- [x] 14.1 `jk build trigger <url> [-p K=V ...]`: parse params (including `@file.txt`), validate against `pipeline params`, trigger, resolve queue, render
- [x] 14.2 `jk build trigger ... --watch`: implement adaptive poller (2s start, back off to 10s after 60s), map terminal state → exit code per design D6
- [x] 14.3 `jk build status <url>`: client + mapper + render; include `pendingInput`
- [x] 14.4 `jk build stages <url>`: client + mapper + render; verify parallel rendering matches design output sample
- [x] 14.5 `jk build input <url> proceed|abort [--input-id]`: handle single-input default, multi-input requires `--input-id`, errors otherwise
- [x] 14.6 `jk build logs <url> [-f] [--stage NAME]`: stream console log; `--stage` path uses stage-log endpoint; unknown stage lists actual names
- [x] 14.7 End-to-end tests for each command + flag combo against `httptest.Server`

## 15. End-to-end test against a real Jenkins (dogfood)

- [x] 15.1 Set up a disposable Jenkins instance (Docker `jenkins/jenkins:lts`) with one folder, one multibranch pipeline containing an `input` step and a parameterized job
- [x] 15.2 Run every MVP command against it; capture failures
- [x] 15.3 Verify `SSL_CERT_FILE` path against a self-signed proxy (e.g. nginx with self-signed cert in front of the Jenkins container)
- [x] 15.4 Verify `--watch` exit codes for all five terminal states (SUCCESS, FAILURE, UNSTABLE, ABORTED, PENDING_INPUT)
- [x] 15.5 Document any deviations between the live Jenkins and the spike fixtures; update mappers as needed

## 16. Distribution

- [x] 16.1 Add GoReleaser config building macOS arm64/amd64 and Linux amd64/arm64 binaries
- [x] 16.2 Set up GitHub Actions release workflow (tag → goreleaser → GitHub release with binaries + checksums)
- [x] 16.3 Create Homebrew tap repo or formula directory; verify `brew install <tap>/jk` installs the released binary
- [x] 16.4 Verify `go install github.com/addozhang/jk/cmd/jk@latest` produces a working binary
- [x] 16.5 Update README with both install paths

## 17. Documentation & launch readiness

- [x] 17.1 README: 60-second overview (what jk is, what it isn't), install, three example commands
- [x] 17.2 README section "URL conventions": every supported URL shape with examples
- [x] 17.3 README section "First-time setup": `jk auth add` + `SSL_CERT_FILE` walkthrough
- [x] 17.4 README section "Scripting with jk": exit code table, JSON example, `schemaVersion` pinning advice
- [x] 17.5 Link `docs/schema.md` from README and from every command's `--help`
- [x] 17.6 Tag `v0.1.0`, publish release, dogfood for 2 weeks per validation assumptions in proposal/design
