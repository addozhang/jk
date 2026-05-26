## Why

Developers who work across multiple Jenkins instances spend significant time clicking through a slow web UI to trigger pipelines, watch logs, and respond to manual approval gates. Existing tools (`jenkins-cli.jar`, `jcli`, `jenni`) treat Jenkins as a bag of generic jobs, fall apart on multibranch pipelines with parameters and input steps, and tie users to per-instance profile configuration. None offer a stable structured output contract that scripts can depend on across Jenkins versions.

We build `jk` — a Go CLI organized around Jenkins **Pipeline** concepts (folders, parameters, stages, inputs), where the unit of identity is the Jenkins URL itself, output defaults to YAML with a stable self-owned schema, and TLS/CSRF/auth concerns of real enterprise Jenkins are handled out of the box.

## What Changes

- Introduce the `jk` binary with 11 MVP commands grouped under `auth`, `pipeline`, and `build`.
- Adopt **URL-as-identity**: every command accepts a full Jenkins URL; hostname implicitly selects stored credentials. No `context`/`profile`/`use` ceremony.
- Adopt **kubectl-style output flags**: `-o yaml|json|raw`, default `yaml`. `--raw` returns the original Jenkins API response as an escape hatch.
- Maintain a **stable self-owned output schema** (`docs/schema.md`) versioned by a top-level `schemaVersion` field; isolate consumers from Jenkins API drift across versions/plugins.
- Schema conventions: camelCase field names, ISO 8601 UTC timestamps with `Utc` suffix, durations in milliseconds with `Ms` suffix, uppercase string enums, explicit `null` for missing values, per-field stability tier (`stable` / `experimental`).
- Support **API token auth per host** stored in `~/.config/jk/credentials` (chmod 0600); auto-fetch and cache CSRF crumbs.
- Support **enterprise TLS**: trust additional CAs via `SSL_CERT_FILE`; `--insecure` flag for development.
- Support **parameterized triggering** via `-p KEY=VALUE` (and `-p KEY=@file.txt` to read from file, avoiding shell escaping issues).
- Support **`--watch` on `build trigger`**: poll until terminal state, exit code reflects build result, enabling CI-of-CI scripting.
- Support **Pipeline input steps**: query pending input on `build status`, respond with `build input <url> proceed|abort` (with `--input-id` disambiguation when multiple).
- Support **stage-level introspection**: `build stages` returns the stage tree (including parallel branches); `build logs --stage NAME` fetches a single stage's log.
- Provide **human-readable errors** with actionable next steps (e.g. "API token invalid for host X. Run: jk auth add X") — explicit non-goal: do not mimic `jenkins-cli.jar` error style.
- Distribute via **Homebrew tap and `go install`**.

## Capabilities

### New Capabilities

- `auth`: per-host API token storage and lookup; credential file management; CSRF crumb acquisition and caching.
- `url-resolution`: parsing Jenkins URLs into structured references (host, folder path, pipeline, branch, build number) covering folder, multibranch, and build-specific shapes; mapping host to stored credentials.
- `pipeline`: querying pipeline metadata, parameter definitions, and listing pipelines under a folder.
- `build`: triggering builds with parameters, querying build status (including pending input), retrieving the stage tree, responding to input steps, fetching full or per-stage logs, and the `--watch` polling lifecycle with build-result-as-exit-code semantics.
- `output`: rendering `jk`'s self-owned schema as YAML or JSON, the `--raw` passthrough, top-level `schemaVersion` field, and the documented field conventions (camelCase, `Ms`/`Utc` suffixes, uppercase enums, explicit nulls).
- `tls-and-transport`: HTTP client with `SSL_CERT_FILE` support, `--insecure`, `--timeout`, and `--debug` request/response logging.
- `errors`: translation of Jenkins API errors into human-readable messages with suggested next actions.

### Modified Capabilities

None (greenfield project; no existing specs).

## Impact

- **New repository scaffolding**: Go module layout (`cmd/jk`, `internal/...`), build/release tooling, Homebrew formula scaffold.
- **New external contract**: `docs/schema.md` becomes a public-facing document that constrains future changes (breaking changes require bumping `schemaVersion`).
- **No existing code/users affected**: greenfield; no migration concerns.
- **Dependencies**: a Jenkins REST/`wfapi` client (build or vendor), YAML/JSON encoders, an HTTP client respecting `SSL_CERT_FILE`. No TUI, no AI, no notification daemon — explicitly excluded.
- **Spike risk**: the `wfapi` `inputSubmit` endpoint and stage tree shape are the highest-uncertainty integration points; design must validate them before broad task breakdown.
