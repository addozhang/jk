# jk output schema (v1)

This document is the **public contract** for every YAML and JSON response produced by `jk`. Scripts depending on `jk -o json | jq` or `jk -o yaml | yq` rely on the field names, types, and stability tiers described here.

The contract is governed by:

- **SPEC.md §Schema Review Workflow** — process for changing this document.
- **`openspec/changes/init-jk-jenkins-cli/specs/output/`** — behavioral requirements (default format, schemaVersion injection, null handling).

If you find a real-world Jenkins field that `jk` does not expose, use `-o raw` as the escape hatch and file an issue requesting the field be added to this schema.

---

## 1. Versioning policy

Every response (in `yaml` or `json` format) carries a top-level `schemaVersion` string field as its first key. The current value is `"1"`.

### Bump rules

The `schemaVersion` value is bumped (to `"2"`, `"3"`, …) when any of the following happens to a field tagged `stable`:

- **Removal** — the field is no longer emitted.
- **Rename** — the field's key changes.
- **Type change** — e.g. `string` → `integer`, scalar → object, optional → required.
- **Semantic change** — the field still has the same name and type, but its meaning differs (e.g. `durationMs` switches from "build time" to "queue + build time").
- **Enum narrowing** — an enum value is removed or its meaning changes.
- **Demotion** — promoting `stable` → `experimental` is breaking; consumers must opt in to the new version.

The following changes do **not** bump `schemaVersion`:

- **Adding** a new optional field at any level.
- **Adding** a new enum value (additive; consumers must already handle unknown values gracefully).
- **Promotion** of an `experimental` field to `stable` (additive: the field becomes more durable, not less).
- Anything touching only `experimental` fields.

### Process

A breaking change requires an OpenSpec change whose `design.md` includes a "Stable schema impact" section listing every affected field as `old.path: oldType → new.path: newType` plus a migration story for existing consumers. See SPEC.md §Schema Review Workflow.

---

## 2. Field conventions

| Convention | Rule | Example |
|---|---|---|
| Casing | camelCase for every field name | `buildNumber`, not `build_number` or `BuildNumber` |
| Timestamps | ISO 8601 / RFC 3339 in UTC with `Z` suffix; field name ends with `Utc` | `timestampUtc: "2025-05-26T16:00:00Z"` |
| Durations | Integer milliseconds; field name ends with `Ms` | `durationMs: 12500` |
| Enums | UPPERCASE string constants | `result: SUCCESS`, `state: PENDING_INPUT` |
| Missing values | Explicit `null` (never omit, never empty string) | `result: null` while a build is running |
| Optionals | Same — defined fields are always present; absence is signalled by `null` | `branches: null` for a single-branch pipeline |
| Arrays | Empty array `[]` when present-but-empty; `null` when not applicable | `parameters: []` vs `parallel: null` |
| Booleans | `true` / `false` literals only; never `"true"` strings | `buildable: true` |

### Stability tiers

Every field below is tagged:

- **`stable`** — covered by §1 bump rules. Scripts can rely on it across minor jk releases.
- **`experimental`** — may change without notice within a `schemaVersion`. Use at your own risk; pin to `schemaVersion` only protects `stable` fields.

The default posture for new fields is `experimental`. Promotion to `stable` happens after at least one minor release of real-world use.

---

## 3. Per-command schemas

Every response starts with `schemaVersion: "1"` (omitted from each table for brevity). All field paths are JSONPath-style relative to the response root.

### 3.1 `jk auth list`

Returns the configured Jenkins hosts. Never includes API tokens.

| Field | Type | Tier | Description |
|---|---|---|---|
| `hosts` | `string[]` | `stable` | Array of credential keys (`scheme://host[:port]` plus an optional context path, e.g. `https://ci.example.com/team-a`), in insertion order. Empty array when no credentials are configured. |

### 3.2 `jk auth add <host>` / `jk auth remove <host>`

These commands print a human-readable confirmation to stderr and do not emit structured output. No schema.

The credential key is `scheme://host[:non-default-port]` plus an optional **context path** — the URL segments before the first `/job/` (a bare host or a pure `/job/...` URL yields no path; default ports `:80`/`:443` are dropped). Commands resolve a request URL to the most specific stored key that is a segment-boundary path-prefix of the URL, falling back to a host-only key when no context path matches. This lets several Jenkins instances on one host each carry a distinct credential while a plain host key still covers every path beneath it.

### 3.3 `jk pipeline info <url>`

Returns metadata about a single pipeline.

| Field | Type | Tier | Description |
|---|---|---|---|
| `name` | `string` | `stable` | Pipeline's short name (the last `/job/<name>` segment). |
| `fullName` | `string` | `stable` | Slash-joined folder path plus name, e.g. `team/platform/svc`. |
| `url` | `string` | `stable` | Canonical Jenkins URL of the pipeline. |
| `description` | `string \| null` | `stable` | User-provided description. `null` if unset. |
| `buildable` | `boolean` | `stable` | Whether new builds can be triggered. |
| `lastBuild` | `BuildRef \| null` | `stable` | Most recent build, or `null` if the pipeline has never run. |
| `branches` | `BranchRef[] \| null` | `experimental` | Branch list for multibranch pipelines; `null` for single-branch pipelines. Shape pending spike against a real multibranch job. |

**Nested types:**

```yaml
BuildRef:
  number: integer       # stable
  url: string           # stable
  result: BuildResult | null  # stable (null while running)

BranchRef:
  name: string          # experimental
  url: string           # experimental
```

### 3.4 `jk pipeline params <url>`

Returns the parameter definitions of a pipeline.

| Field | Type | Tier | Description |
|---|---|---|---|
| `parameters` | `Parameter[]` | `stable` | Parameter definitions in Jenkins-declared order. Empty array if none. |

```yaml
Parameter:
  name: string                       # stable
  type: ParameterType                # stable
  default: string | boolean | null   # stable (null when no default)
  description: string | null         # stable
  choices: string[] | null           # stable (non-null only when type == CHOICE)
```

`ParameterType` enum (`stable`): `STRING`, `BOOLEAN`, `CHOICE`, `TEXT`, `PASSWORD`, `UNKNOWN`.

`UNKNOWN` is emitted for Jenkins parameter classes jk does not yet recognize (e.g. plugin-specific types). The raw class name is available via `-o raw`.

### 3.5 `jk pipeline list <folder-url>`

Returns the immediate children of a Jenkins folder.

| Field | Type | Tier | Description |
|---|---|---|---|
| `items` | `Item[]` | `stable` | Child pipelines and sub-folders, in Jenkins's natural order. |

```yaml
Item:
  name: string              # stable
  type: ItemType            # stable
  url: string               # stable
  lastBuild: BuildRef | null  # stable (null for folders and never-built pipelines)
```

`ItemType` enum (`stable`): `PIPELINE`, `FOLDER`.

Multibranch pipelines are reported as `FOLDER` (because their children are branch jobs); to fetch a specific branch, use that branch's URL with `jk pipeline info`.

### 3.6 `jk build trigger <url>`

Returns the queue + resolved build identifiers after a successful trigger.

| Field | Type | Tier | Description |
|---|---|---|---|
| `queueId` | `integer` | `stable` | Jenkins queue item ID. |
| `buildUrl` | `string \| null` | `stable` | URL of the created build. `null` if the queue item has not yet been assigned a build (e.g. with `--watch=false` and an instantly-returning call). |
| `buildNumber` | `integer \| null` | `stable` | Build number; `null` until the queue item is assigned. |

With `--watch`, the command does not emit a final structured response — exit code conveys the build result per `specs/build`.

### 3.7 `jk build status <url>`

Returns the current state of a build.

The `<url>` may address the build by numeric build number (`/job/svc/42/`) or by any of the seven Jenkins permalinks (`lastBuild`, `lastCompletedBuild`, `lastSuccessfulBuild`, `lastUnsuccessfulBuild`, `lastFailedBuild`, `lastStableBuild`, `lastUnstableBuild`). Permalinks are resolved server-side by Jenkins; the `buildNumber` field in the output always reflects the resolved numeric build.

All `<url>` arguments (here and across every command) accept Jenkins instances mounted under a URL context path — any segments before the first `/job/` (e.g. `https://host/jenkins/job/svc/42/`) are preserved and replayed against the server. The context path may also form part of the credential key (`scheme://host[:port]` plus optional base path); see §3.2 for how a request resolves to the most specific matching credential.

| Field | Type | Tier | Description |
|---|---|---|---|
| `buildUrl` | `string` | `stable` | Canonical Jenkins URL of the build. |
| `buildNumber` | `integer` | `stable` | Build number. |
| `queueId` | `integer \| null` | `stable` | Originating queue item, when known. |
| `result` | `BuildResult \| null` | `stable` | Final result; `null` while building. |
| `state` | `BuildState` | `stable` | Lifecycle state (see §4). |
| `building` | `boolean` | `stable` | `true` iff Jenkins reports the build as in-progress. |
| `timestampUtc` | `string` | `stable` | Build start time (RFC 3339 UTC). |
| `durationMs` | `integer` | `stable` | Elapsed time so far for running builds; final duration for completed builds. |
| `estimatedDurationMs` | `integer \| null` | `stable` | Jenkins's estimate from historical runs; `null` if unavailable. |
| `progressPercent` | `integer` | `stable` | `0`–`100`. Computed as `min(100, 100 * durationMs / estimatedDurationMs)`; equals `100` once `state == DONE`. |
| `pendingInput` | `PendingInput \| null` | `experimental` | Non-null iff `state == PENDING_INPUT`. Populated from `/wfapi/pendingInputActions` (not from core `actions[]`, which only carries an `_class` marker). |

```yaml
PendingInput:
  id: string                       # experimental
  message: string                  # experimental
  ok: string                       # experimental — label of the "proceed" button; also the value of the `proceed` form field that `jk build input` sends to the submit endpoint (§3.10)
  parameters: Parameter[]          # stable — input-step parameters (reuses §3.4 shape); consumed by `jk build input -p` validation
```

`state` is derived in the following order — the first matching rule wins:

1. `building == false` → `DONE` (a finished build is never `PENDING_INPUT`, even if a stale input-action marker is still attached to `actions[]`).
2. The core `actions[]` array carries a `pendingInput` marker AND `/wfapi/pendingInputActions` returns at least one entry → `PENDING_INPUT`, and `pendingInput` is populated from the wfapi response.
3. The core marker is present but wfapi returns an empty list (input was submitted between the two HTTP calls, or enrichment failed) → `RUNNING`, `pendingInput` omitted.
4. Otherwise → `RUNNING`.

### 3.8 `jk build params <url>`

Returns the parameter **values** that were recorded on a specific build, in contrast to `jk pipeline params` which returns the parameter **definitions** of the pipeline.

The `<url>` may address the build by numeric build number or by any of the seven Jenkins permalinks (see §3.7). The `buildNumber` and `buildUrl` fields always reflect the resolved numeric build.

| Field | Type | Tier | Description |
|---|---|---|---|
| `buildUrl` | `string` | `stable` | Canonical Jenkins URL of the build (numeric form, even when the request used a permalink). |
| `buildNumber` | `integer` | `stable` | Build number. |
| `parameters` | `BuildParameter[]` | `stable` | Parameters recorded on this build, in Jenkins-declared order. Empty array if the build was unparameterized or carried no `ParametersAction`. |

```yaml
BuildParameter:
  name: string                       # stable
  value: string | boolean | null     # stable — natural JSON type as recorded by Jenkins; null for password/credentials parameters whose values Jenkins redacts
```

Notes:

- `BuildParameter` is intentionally distinct from the `Parameter` shape in §3.4: definitions describe what a pipeline accepts (type, default, choices, description), while build parameters record what a specific run was given (name + recorded value only).
- When a build's `actions[]` contains multiple `ParametersAction` entries (rare but possible with plugins that inject parameters mid-run), entries are merged **last-write-wins** by `name`; the first occurrence's position in the output array is preserved.
- `value: null` is preserved verbatim for `PasswordParameterValue` and credentials parameters, where Jenkins refuses to disclose the recorded secret. Consumers can distinguish "no value" from "value present but redacted" only by consulting the pipeline's parameter definitions via `jk pipeline params`.

### 3.9 `jk build stages <url>`

Returns the pipeline run's stage tree.

| Field | Type | Tier | Description |
|---|---|---|---|
| `stages` | `Stage[]` | `experimental` | Top-level stages in execution order. Shape pending spike 1.1; both the nested `parallel` and the duplicate-name `displayName` suffix are tentative. |

```yaml
Stage:
  name: string                # experimental
  displayName: string         # experimental — equals name unless duplicated; then suffix "#1", "#2", …
  status: StageStatus         # experimental
  startTimeUtc: string | null # experimental — null if the stage has not started
  durationMs: integer | null  # experimental — null if the stage has not finished
  parallel: Stage[] | null    # experimental — child stages running in parallel under this stage; null for sequential stages
```

`StageStatus` enum (`experimental`): `SUCCESS`, `FAILURE`, `ABORTED`, `UNSTABLE`, `RUNNING`, `NOT_EXECUTED`, `PAUSED_PENDING_INPUT`, `QUEUED`.

This entire section is `experimental` until the wfapi spike (tasks 1.1, 1.3) confirms the real shape against parallel pipelines.

### 3.10 `jk build input <url> proceed|abort`

Submits a `proceed` or `abort` response to a paused Pipeline input step. When the build has exactly one pending input, the command operates on that input by default; when two or more are pending, `--input-id <id>` is required.

Pass input-step parameters with repeatable `-p KEY=VALUE` flags. Use `@PATH` to load a value from a file (the file is read verbatim, including trailing newlines). Values are validated client-side against the pending input's declared parameter shape (see §3.7 `PendingInput.parameters`) **before** any HTTP request reaches Jenkins:

- `CHOICE`: value must appear in the declared `choices` list.
- `BOOLEAN`: accepted as case-insensitive `true` / `false` / `1` / `0`; normalized to lowercase `true` / `false` on the wire.
- `STRING` / `TEXT` / `PASSWORD` / `UNKNOWN`: any string.

Unknown keys, invalid choices, or required parameters with no value (and no `defaultValue` in the declaration) exit with code `10` and the message names the offending parameter. `-p` is ignored — with a single stderr warning — when the action is `abort`.

The endpoint and wire format are chosen by the action and parameter set:

| Action  | Parameters supplied         | Endpoint                                                        | Body                                                                          |
|---------|------------------------------|------------------------------------------------------------------|--------------------------------------------------------------------------------|
| `proceed` | none, none declared        | `POST /input/<id>/proceedEmpty`                                  | empty                                                                          |
| `proceed` | declared, all defaulted    | `POST <proceedUrl>` (wfapi-advertised; legacy fallback `POST /input/<id>/submit`) | `json=<URL-encoded JSON>&proceed=<ok-label>`                                  |
| `proceed` | any supplied               | `POST <proceedUrl>` (wfapi-advertised; legacy fallback `POST /input/<id>/submit`) | `json=<URL-encoded JSON of {"parameter":[{"name":..,"value":..}]}>&proceed=<ok-label>` |
| `abort`   | (ignored)                  | `POST /input/<id>/abort`                                         | empty                                                                          |

For parameterized submissions, jk uses the `proceedUrl` path that Jenkins itself advertises in the `/wfapi/pendingInputActions` response (typically `/job/<...>/wfapi/inputSubmit?inputId=<id>`). The legacy `/input/<id>/submit` path is kept as a fallback for older Jenkins servers whose wfapi response omits `proceedUrl`. The wfapi path is the only one modern Jenkins accepts cleanly: posting parameterized input to `/input/<id>/submit` returns `HTTP 200` but silently records `Rejected by <user>` and aborts the build (confirmed v0.2.0 dogfood against the deploy-input harness pipeline).

The `proceed=<ok-label>` form field is **required** on both endpoints — without it Jenkins returns `HTTP 200` but records `Rejected by <user>` in the build log and aborts the run. The label is sourced from the pending input's `ok` field (§3.7 `PendingInput.ok`) and threaded through automatically; users do not need to supply it.

Returns confirmation that the input was submitted:

| Field | Type | Tier | Description |
|---|---|---|---|
| `inputId` | `string` | `experimental` | The ID of the input that was responded to. |
| `action` | `InputAction` | `experimental` | `PROCEED` or `ABORT`. |
| `buildUrl` | `string` | `stable` | URL of the build that received the input. |
| `state` | `BuildState` | `stable` | Build state immediately after submission (typically `RUNNING` or `DONE`). |

`InputAction` enum (`experimental`): `PROCEED`, `ABORT`.

### 3.11 `jk build logs <url>`

This command streams plain text to stdout. It is **not** wrapped in the jk schema; consumers should treat the output as opaque log bytes. `schemaVersion` is **not** injected. `-o json` and `-o yaml` behave identically to `-o raw` for this command (all three stream raw log text).

This is the single intentional deviation from the §output spec; it is documented here so consumers do not script around a missing `schemaVersion`.

---

## 4. Enum catalog

All enum values are uppercase ASCII strings. Unknown values from a future Jenkins MAY appear in `experimental` fields; consumers MUST handle unknown enum values gracefully (e.g. fall through to `UNKNOWN` rendering).

### `BuildResult` (`stable`)

The terminal outcome of a build. `null` while the build is running.

| Value | Meaning |
|---|---|
| `SUCCESS` | Build finished with no failures. |
| `FAILURE` | Build failed (script error, test failure, etc.). |
| `UNSTABLE` | Build finished but reported instability (e.g. failing tests but no script error). |
| `ABORTED` | Build was cancelled before reaching a terminal state. |
| `NOT_BUILT` | Stage or build was skipped. Rare at the build level; common at the stage level. |

### `BuildState` (`stable`)

The lifecycle state of a build. Unlike `BuildResult`, this is always non-null.

| Value | Meaning |
|---|---|
| `QUEUED` | In the Jenkins queue; no build number yet assigned. |
| `RUNNING` | Executing. |
| `PENDING_INPUT` | Paused at a Pipeline `input` step awaiting `jk build input`. |
| `DONE` | Terminal — `result` is non-null. |

### `ParameterType` (`stable`)

See §3.4.

### `ItemType` (`stable`)

See §3.5.

### `StageStatus` (`experimental`)

See §3.9.

### `InputAction` (`experimental`)

See §3.10.

---

## 5. Cross-reference

Every field referenced by any `openspec/changes/init-jk-jenkins-cli/specs/**/*.md` scenario appears in §3 above. If you add a scenario that mentions a new field, update this document **in the same change** per SPEC.md §Always-do.

| Spec file | Sections satisfied |
|---|---|
| `specs/auth/spec.md` | §3.1, §3.2 |
| `specs/pipeline/spec.md` | §3.3, §3.4, §3.5 |
| `specs/build/spec.md` | §3.6, §3.7, §3.8, §3.9, §3.10, §3.11 |
| `specs/output/spec.md` | §1, §2 |
| `specs/url-resolution/spec.md` | (no response shape; URL handling is internal) |
| `specs/tls-and-transport/spec.md` | (no response shape; transport-level behavior) |
| `specs/errors/spec.md` | (no schema; errors print human-readable text to stderr) |
