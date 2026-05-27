## Context

`jk` is a greenfield Go CLI for driving Jenkins Pipelines from the terminal. The target user is a developer working across multiple Jenkins instances who wants to trigger pipelines, watch logs, inspect stages, and respond to manual approval gates without leaving the terminal — and without ceremony.

Several non-trivial integration concerns shape the design:

- **Jenkins API surface is split** across `api/json`, `wfapi` (Pipeline Stage View), and `crumbIssuer` endpoints with non-uniform field naming and version-dependent shapes.
- **Self-signed certificates** are normal in enterprise Jenkins; the CLI must respect `SSL_CERT_FILE`.
- **CSRF crumbs** are required for state-changing requests on most instances and expire on Jenkins restart.
- **Pipeline URLs vary widely**: top-level jobs, nested folders, multibranch with branch segments, with/without trailing slash, with/without build number.
- **Pipeline input steps** are the highest-uncertainty area; the `wfapi` submit endpoint has limited documentation.
- **Schema stability** is a hard requirement: scripts must keep working across Jenkins upgrades, so `jk` owns its output contract.

There are no existing users, no legacy code, and no migration concerns. The constraint surface is technical, not organizational.

## Goals / Non-Goals

**Goals:**

- Single statically-linked Go binary, cross-platform (macOS arm64/amd64, Linux amd64/arm64).
- URL-as-identity for every command; hostname maps to credentials with zero context switching.
- Stable, versioned, self-owned output schema in YAML (default) and JSON, with `--raw` escape hatch to the underlying Jenkins API response.
- First-class support for the four hardest things in real Jenkins CLI use: parameterized triggering, stage introspection, input-step response, log streaming per stage.
- Out-of-the-box support for enterprise TLS (`SSL_CERT_FILE`) and CSRF.
- Human-readable errors with actionable next steps.
- `--watch` on `build trigger` propagates build result to process exit code, enabling CI-of-CI scripting.

**Non-Goals:**

- No TUI, no fuzzy picker, no interactive prompts, no notification daemon, no AI features.
- No profile/context/alias system. URLs are the only addressing mechanism.
- No git-branch inference. Users provide explicit URLs.
- No SSO/OAuth/OIDC login flows. API token only.
- No log intelligence (grep/since/last-failure/diff-vs-last-green). Plain `-f` streaming only.
- No Jenkins admin surface (plugins, nodes, credentials, queue, view config).
- No pipeline authoring/editing.
- No `jk explain` command in MVP. Schema is documented manually in `docs/schema.md`; `explain` is deferred until the schema stabilizes and can be generated from struct tags.
- No complex parameter types in MVP (file params, masked password params with safe entry) — deferred.
- No multi-user/team config sharing.

## Decisions

### D1. URL-as-identity, no profile system

Every command accepts a full Jenkins URL. The URL's hostname is the lookup key into `~/.config/jk/credentials`. There is no `jk config use-context prod` equivalent.

**Why:** Users already have URLs from Slack, PRs, browser bookmarks. A profile system is ceremony that re-creates the "which Jenkins am I on?" cognitive load the URL already eliminates. `kubectl`-style contexts are valuable when the same resource name exists in multiple clusters; Jenkins URLs are globally unique, so contexts add nothing.

**Alternatives considered:**

- *Named profiles + short job names* (jcli/jenkins-cli.jar pattern): rejected — re-introduces "which profile am I on?" and "what was the job called again?"
- *Git-branch inference*: rejected by user — jobs are not 1:1 with branches in their workflow.
- *Recent-jobs / fuzzy picker*: rejected by user — preference is explicit URLs, not implicit state.

### D2. Self-owned schema with `schemaVersion` and `--raw` escape hatch

`jk` defines its own output schema in `docs/schema.md`. Every response embeds a top-level `schemaVersion: "1"` field. `--raw` returns the original Jenkins API JSON, unprocessed.

**Why:** The user cannot control Jenkins versions across their work environments. "Pass through Jenkins fields directly" would mean `jk` output silently changes shape when ops upgrades Jenkins or installs a plugin, breaking every script that depends on `jk -o json | jq`. A self-owned schema isolates consumers from that drift. The `--raw` flag is the safety valve when the schema is missing something.

**Schema conventions:**

- camelCase field names
- Timestamps: ISO 8601 UTC strings, suffix `Utc` (e.g. `timestampUtc`)
- Durations: integer milliseconds, suffix `Ms` (e.g. `durationMs`)
- Enums: uppercase string constants (`SUCCESS`, `FAILURE`, `ABORTED`, `UNSTABLE`, `RUNNING`, `QUEUED`, `PENDING_INPUT`, `DONE`)
- Missing values: explicit `null`, never omitted (simplifies consumer logic)
- Each field tagged `stable` or `experimental` in `docs/schema.md`; only `stable` fields carry the version compatibility promise

**Versioning policy** (to be ratified in `docs/schema.md`):

- Adding new fields → no version bump
- Removing or renaming a `stable` field, changing its type, or changing the meaning of an enum value → bump `schemaVersion` to `"2"`
- Promoting `experimental` → `stable` → no version bump (additive)
- Demoting `stable` → `experimental` → bump version (consumers must opt in)

**Alternatives considered:**

- *Pass-through Jenkins API*: rejected — fails the "scripts must keep working" requirement given the user cannot control Jenkins versions.
- *Schema with no version field*: rejected — versioning is the entire point of owning the schema.
- *Auto-generate schema from a Go struct without manual docs*: rejected for MVP — premature. Writing `docs/schema.md` by hand for v1 forces clarity; auto-generation is the v0.2 path once the schema is stable.

### D3. Parameter passing via `-p KEY=VALUE`, not dynamic flags

Build parameters are passed with repeatable `-p` flags. `-p KEY=@path/to/file` reads the value from a file.

**Why:** Jenkins parameter names are user-defined at runtime and can collide with `jk`'s own flags (`--output`, `--debug`), contain non-ASCII characters, or require API lookup before validation. `-p` is a stable, safe, scriptable namespace that needs no pre-flight API call.

**Alternatives considered:**

- *Dynamic flags (`--BRANCH main`)*: rejected — flag conflicts, requires API call for `--help`, complex unknown-flag handling.
- *Namespaced flags (`--param.BRANCH main`)*: rejected — uglier than `-p` for marginal benefit.
- *Interactive prompt mode (`-i`)*: rejected for MVP — user wants commands, not prompts.

### D4. CSRF crumb auto-acquisition with cache + retry

Before any state-changing request, `jk` fetches `/crumbIssuer/api/json` and caches the crumb in memory for the process lifetime. On 403 with a CSRF-related body, `jk` refreshes the crumb once and retries the original request.

**Why:** Crumbs expire on Jenkins restart, and most enterprise instances enable CSRF. Making the user manage crumbs manually would be hostile. Single retry prevents infinite loops if the underlying issue is auth, not CSRF.

**Storage:** in-memory only. Persisting to disk adds invalidation complexity for negligible benefit (a single extra HTTP call per process).

### D5. TLS: respect `SSL_CERT_FILE`, plus `--insecure` for dev

The HTTP client's `tls.Config.RootCAs` is built from the system pool, augmented by certs loaded from `SSL_CERT_FILE` when set. `--insecure` sets `InsecureSkipVerify: true` and prints a warning to stderr.

**Why:** `SSL_CERT_FILE` is the OpenSSL/curl convention; users with self-signed Jenkins already have it set. Go's standard library does not read it automatically — we must do so explicitly.

**Alternatives considered:**

- *`--cacert` flag only*: rejected — users would have to remember to pass it on every invocation.
- *Read system keychain*: rejected — platform-specific, complex, and not actually how enterprise users distribute internal CAs (they ship a cert file).

### D6. `--watch` on `build trigger` polls, exits with build's result code

`jk build trigger <url> --watch` polls `build status` at an adaptive interval (start 2s, back off to 10s after 1 minute) until `state` reaches `DONE` or `PENDING_INPUT`. Process exit codes:

- `0` — build `SUCCESS`
- `1` — build `FAILURE`
- `2` — build `UNSTABLE`
- `3` — build `ABORTED`
- `4` — build paused on `PENDING_INPUT` (informational; user must intervene)
- `≥10` — `jk` itself failed (auth, network, parse, etc.)

**Why:** Pipelines can run for minutes to hours. Adaptive polling avoids hammering Jenkins early-out cases while keeping latency low for short builds. Exit-code-as-result enables `jk build trigger ... --watch && deploy.sh` patterns. Treating `PENDING_INPUT` as a distinct exit code prevents silently hanging in scripts.

**Alternatives considered:**

- *Constant 5s polling*: rejected — wastes Jenkins resources on long builds.
- *Server-Sent Events / WebSocket*: rejected — Jenkins's SSE story is plugin-dependent; polling is universal.
- *Block on `PENDING_INPUT` until resolved*: rejected — couples `jk` to a long-running session; script-friendly behavior is to exit and let the operator run `jk build input`.

### D7. URL parser produces a structured `Ref`; commands work against the `Ref`

A single `internal/jenkinsurl` package parses any supported URL shape into:

```
type Ref struct {
    Host        string   // "https://jenkins.foo.com" (scheme + host + port, default ports stripped)
    JobSegments []string // ["team", "svc", "main"] — every /job/<name> in order, decoded
    BuildNumber int      // 0 means "not specified" → use lastBuild
}
```

The parser accepts: trailing slash or not, `https://` or `http://`, default-port stripping, URL-encoded segments, optional explicit build number (trailing `/<n>/`), query strings, and fragments (the last two are stripped). It rejects URLs that do not match `/job/...` structure with a clear error, and URLs containing empty job segments.

**Why a flat `JobSegments` slice instead of `FolderPath` + `Pipeline` + `Branch`:** the URL `…/job/a/job/b/job/c/` is structurally ambiguous between "three-level folder containing pipeline `c`" and "two-level folder containing multibranch `b` with branch `c`". Distinguishing the two requires a Jenkins API call (the `_class` of each intermediate job). The parser has no such context, so it returns the raw segments and lets each command decide. For build-scoped commands the right-most segment is the leaf job regardless; for `pipeline list` every segment is treated as a folder path; for `build status` the second-to-last is interpreted as the pipeline only when the Jenkins response identifies it as a multibranch.

**Why a single parser:** eliminates a class of bugs (every command rewriting the same URL logic) and makes the URL contract explicit and testable. A `Ref` is easy to mock for tests.

**Build number defaulting**: when omitted, build-scoped commands (`status`, `stages`, `logs`, `input`) operate on `lastBuild`. This is the dominant use case ("what's happening on this pipeline right now?"). Explicit build number wins when present.

### D8. Error translation layer

A small `internal/errors` package wraps Jenkins API errors into a `JKError` with `Code`, `Message`, and `Suggestion` fields. Examples:

- HTTP 401/403 on a known host → "API token rejected by `<host>`. Run: `jk auth add <host>` to refresh."
- HTTP 404 on a pipeline URL → "Pipeline not found: `<url>`. Check the URL, or list with: `jk pipeline list <parent-folder-url>`."
- Network timeout → "Timed out after 30s contacting `<host>`. Increase with `--timeout 2m`, or check VPN connectivity."
- CSRF crumb refresh failed → "Could not obtain CSRF crumb from `<host>`. The host may have CSRF disabled — file an issue with the Jenkins version."

`--debug` bypasses translation and prints the raw HTTP exchange.

**Why:** The user explicitly rejected `jenkins-cli.jar`-style errors. Every error must point at a next action.

### D9. Layered package structure

```
cmd/jk/                       # main, flag wiring, subcommand registration
internal/cli/                 # Cobra commands (one file per subcommand group)
internal/jenkins/             # HTTP client, CSRF, TLS, low-level API calls
internal/jenkinsurl/          # URL parser, Ref type
internal/auth/                # credential file read/write, host lookup
internal/schema/              # output schema types + version constant
internal/output/              # YAML/JSON/raw renderers
internal/errors/              # JKError type + translation
docs/schema.md                # public schema contract
```

`internal/jenkins/` returns raw API responses; `internal/schema/` maps them to `jk`'s schema; `internal/cli/` orchestrates and renders. This separation is what makes the schema isolation real (not just aspirational).

### D10. Library choices

- **CLI framework**: `spf13/cobra` — de facto standard, supports nested subcommands cleanly.
- **YAML**: `sigs.k8s.io/yaml` (wraps `gopkg.in/yaml.v3`, identical semantics to kubectl, JSON-tag compatible).
- **HTTP**: standard library `net/http`. No third-party Jenkins client — they are all incomplete and would constrain the schema isolation.
- **Logging in `--debug`**: standard `log/slog` with a custom HTTP transport that logs requests/responses.
- **Testing**: standard `testing` + `httptest.Server` for the Jenkins client.

## Risks / Trade-offs

- **`wfapi` undocumented corners** → mitigate with a Day-1 spike (see tasks 1.x) that validates `inputSubmit`, stage tree shape including parallel branches, and log-per-stage retrieval against a real Jenkins. If `wfapi` is insufficient, the affected commands (`build input`, `build stages`, `build logs --stage`) get scoped down or moved to v0.2 before broad task breakdown.
- **Schema design freezes too early** → mitigate by tagging fields `experimental` liberally in v1; only fields the user actively scripts against become `stable`. The dogfood-for-2-weeks validation step measures `--raw` frequency to detect under-specified schema.
- **Adaptive polling drift on `--watch`** → if Jenkins is slow or paused, polling may overshoot `estimatedDurationMs` significantly. Mitigation: cap interval at 10s; document the behavior.
- **CSRF disabled instances** → the auto-retry logic treats CSRF errors as recoverable; on instances without CSRF, the initial crumb fetch returns 404, which we handle by skipping crumb headers (not erroring).
- **Self-signed cert UX** → if `SSL_CERT_FILE` is set but missing/invalid, Go's error is opaque. Wrap that in an `errors` translation so the user knows the env var is the problem.
- **Cross-platform credential file permissions** → on Windows, chmod is a no-op; document that the credentials file should be ACL-protected on Windows. MVP targets macOS/Linux primarily; Windows support is best-effort.
- **`-p KEY=@file.txt` shell quoting** → `@` is not special to common shells, but document that `-p KEY=@-` could be a future addition for stdin; do not implement in MVP.
- **Folder-pipeline ambiguity** → `jk pipeline list <url>` on a non-folder URL must return a clear error ("This URL points to a pipeline, not a folder. Did you mean `jk pipeline info`?"). Cover in tests.

## Migration Plan

Not applicable. Greenfield project, no existing users.

## Open Questions

- **Schema version bump policy for enum value changes vs. additions**: adding `PENDING_INPUT` later would be additive; renaming `RUNNING` to `IN_PROGRESS` would be breaking. To be ratified in `docs/schema.md` §1.
- **Multi-pending-input behavior**: when multiple inputs are pending simultaneously, default behavior is to error and list IDs requiring `--input-id`. Confirm this matches the user's mental model after first real encounter.
- **`build stages` YAML shape for parallel branches**: nested `parallel: [[stageA, stageB], [stageC]]` arrays vs. flat list with `parentId`. Decide after spike against two real-world parallel pipelines.
- **Duplicate stage names** (loops/retries): suffix with `#1`, `#2` in order of occurrence in `jk`'s output. Document in `docs/schema.md`.

Resolved upstream (see `/SPEC.md` §Tech Stack and §Open Questions): auth file format is TOML at `~/.config/jk/credentials` (0600); `jk version` output includes `schemaVersion`; module owner is `addozhang`; Homebrew tap is `addozhang/homebrew-jk`; repository is private (consumers must set `GOPRIVATE`).
