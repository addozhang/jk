# jk

A Pipeline-native CLI for Jenkins.

`jk` is for the day-to-day pipeline operator: inspect a job, trigger a build, watch it, tail its log, answer an `input` step — all from the terminal, all driven by the Jenkins URL you'd paste into a browser.

**What jk is**
- URL-as-identity: every command takes a Jenkins URL; the host (and optional context path) picks the stored credentials.
- Stable, self-owned schema: `yaml` (default), `json`, or `raw`. Output begins with `schemaVersion: "1"`; breaking changes bump the version.
- Pipeline-aware: understands wfapi (stage tree, parallel branches, pending input).
- Scripting-friendly: `--watch` returns the build result via exit code; `-o json` pipes cleanly into `jq`.
- Agent-ready: ships an optional skill that teaches AI coding agents how to use `jk` safely from the terminal.

**What jk is not**
- Not a general Jenkins administration tool — no plugin/agent/credentials-store management.
- Not a Freestyle CLI — Pipeline (declarative or scripted) is the supported job type.
- Not a Jenkinsfile linter or editor.

## Install

### Homebrew (macOS / Linux)

```sh
brew install addozhang/tap/jk
```

Requires the tap repo [`addozhang/homebrew-tap`](https://github.com/addozhang/homebrew-tap) to be accessible.

### go install

```sh
go install github.com/addozhang/jk/cmd/jk@latest
```

Requires Go 1.22+. The Go toolchain fetches the module directly from GitHub; no proxy is needed for a public repo. If you forked into a private repo, set `GOPRIVATE=github.com/addozhang/*` first.

### Download a pre-built binary

Download `jk_<version>_<os>_<arch>.tar.gz` from the [Releases page](https://github.com/addozhang/jk/releases), extract, and move `jk` onto your `PATH`.

## Quick start

```sh
# 1. Add credentials for your Jenkins host
jk auth add https://jenkins.example.com

# 2. Inspect a pipeline
jk pipeline info https://jenkins.example.com/job/my-folder/job/my-pipeline

# 3. Trigger a build and watch it finish
jk build trigger https://jenkins.example.com/job/my-folder/job/my-pipeline \
    -p ENV=staging --watch

# 4. Tail the console log of the latest build
jk build logs https://jenkins.example.com/job/my-folder/job/my-pipeline/lastBuild -f

# 5. Respond to a paused input step with parameter values
jk build input https://jenkins.example.com/job/deploy/42 proceed \
    -p ENV=prod -p DRY_RUN=false

# 6. Inspect the parameter values a build was triggered with
jk build params https://jenkins.example.com/job/my-pipeline/lastSuccessfulBuild
```

## AI agent skill

`jk` includes a companion skill for AI coding agents at [`skills/jk-jenkins-cli/`](./skills/jk-jenkins-cli/). The skill is a concise operating guide for agents that need to inspect Jenkins failures, trigger and watch builds, read stage-scoped logs, or handle pending Pipeline `input` steps with the right safety checks.

Use it when an agent has terminal access and needs to work with Jenkins through `jk`. The skill emphasizes:

- using Jenkins URLs as the unit of identity;
- preferring `-o json` for machine-readable inspection;
- inspecting status and stages before reading logs;
- using `--stage` for focused log reads;
- checking `pendingInput` and `--input-id` before acting on input steps;
- avoiding unsupported Jenkins administration tasks.

## First-time setup

### 1. Add a host credential

```sh
jk auth add https://jenkins.example.com
# Token: (paste your Jenkins API token — input is hidden)
```

Credentials are stored in `~/.config/jk/credentials` (mode `0600`, TOML format). Tokens are never printed by any `jk` command.

#### Context-path-scoped credentials

`jk auth add` accepts an optional **context path**, so several Jenkins instances reachable on the same host — distinguished only by a reverse-proxy prefix — can each hold their own credential:

```sh
jk auth add https://ci.example.com/team-a   # instance under /team-a
jk auth add https://ci.example.com/team-b   # instance under /team-b
jk auth add https://ci.example.com          # host-only fallback (optional)
```

The stored key is `scheme://host[:port]` plus the normalised context path — the segments before the first `/job/`; a bare host or a pure `/job/...` URL stores no path. When a command runs, `jk` selects the **most specific** entry whose key is a path-prefix of the request URL, falling back to the host-only entry when no context path matches. Matching honours segment boundaries, so a `/team-a` entry never captures a request to `/team-amber`.

### 2. Custom CA / self-signed certificates

If your Jenkins is behind a TLS proxy with a self-signed certificate, point `SSL_CERT_FILE` at the PEM bundle:

```sh
export SSL_CERT_FILE=/etc/ssl/my-ca-bundle.pem
jk pipeline info https://jenkins.example.com/job/...
```

Pass `--insecure` only as a last resort; it disables all certificate verification and prints a warning.

## URL conventions

`jk` treats the Jenkins URL as the primary identifier. Every URL shape that Jenkins produces is accepted:

| URL shape | Meaning |
|-----------|---------|
| `https://host/job/pipeline` | top-level pipeline |
| `https://host/job/folder/job/pipeline` | pipeline inside a folder |
| `https://host/job/folder/job/sub/job/pipeline` | arbitrarily nested folders |
| `https://host/job/mb/job/main` | multibranch pipeline → branch `main` |
| `https://host/job/pipeline/42` | specific build #42 |
| `https://host/job/pipeline/lastBuild` | symbolic last build |
| `https://host/job/folder/` | folder (for `pipeline list`) |
| `https://host/jenkins/job/pipeline` | instance mounted under a context path (`/jenkins`, `/domain`, …) |

Any of the seven Jenkins build permalinks may appear in the build-position slot in place of a numeric build number: `lastBuild`, `lastCompletedBuild`, `lastSuccessfulBuild`, `lastUnsuccessfulBuild`, `lastFailedBuild`, `lastStableBuild`, `lastUnstableBuild`. Jenkins resolves them server-side; `jk` output always records the resolved numeric `buildNumber`.

Jenkins instances deployed under a URL **context path** (a reverse-proxy mount such as `/jenkins`, or a multi-tenant prefix like `/domain`) are supported: any path segments before the first `/job/` are preserved and replayed against the server. A context path can also scope stored credentials, so multiple instances on one host can each hold their own token — see *Context-path-scoped credentials* under First-time setup.

Normalisation rules:
- Trailing slashes are stripped.
- Default ports (`:80` for `http`, `:443` for `https`) are dropped when looking up credentials, so `https://jenkins.example.com` and `https://jenkins.example.com:443` resolve to the same credential entry.
- The credential lookup key is `scheme://host[:non-default-port]` plus an optional context path. A request resolves to the most specific stored key that is a segment-boundary path-prefix of the URL, falling back to a host-only entry — so a single host can serve several credentialed instances while a plain host entry still covers every path beneath it.

## Scripting with jk

### JSON output

```sh
# Get just the build result
jk build status https://host/job/pipeline/42 -o json | jq -r '.result'

# Find any pending input
jk build status https://host/job/pipeline/lastBuild -o json | jq '.pendingInput'
```

### Exit codes

When `--watch` is **not** used, `jk` exits `0` on success or `10` on any jk-level error (bad URL, auth failure, network problem, TLS verification failure, CSRF crumb failure, malformed Jenkins response). The stderr message identifies which.

When `--watch` **is** used with `jk build trigger`, the exit code reflects the build's terminal state:

| Code | Meaning |
|------|---------|
| `0`  | Build SUCCESS |
| `1`  | Build FAILURE |
| `2`  | Build UNSTABLE |
| `3`  | Build ABORTED |
| `4`  | Build paused at PENDING_INPUT (poll deadline reached) |
| `10` | jk-level error (see stderr) |

```sh
jk build trigger https://host/job/pipeline --watch
echo $?   # 0..4 = build result; 10 = jk-level failure
```

### Schema pinning

All structured output begins with `schemaVersion: "1"`. Scripts should assert on this value:

```sh
schema=$(jk pipeline info https://host/job/foo -o json | jq -r '.schemaVersion')
[ "$schema" = "1" ] || { echo "unexpected schema $schema"; exit 1; }
```

Breaking changes will increment the version; additive changes (new fields, new enum values tagged `experimental`) will not. See [`docs/schema.md`](./docs/schema.md) for the full field reference and versioning policy.

## Release notes

### v0.2.0 — input-step parameters and status correctness

**New features**

- `jk build input <url> proceed` now accepts repeatable `-p KEY=VALUE` flags to submit parameter values to a paused input step. `@PATH` loads the value from a file. Values are validated client-side against the pending input's declared shape (CHOICE / BOOLEAN / STRING / TEXT / PASSWORD) before any HTTP call; invalid values exit `10` with a message that names the offending parameter and lists the valid choices.
- `pendingInput.parameters` in `jk build status` output is promoted from `experimental` to `stable`. Scripts can rely on the field name and `Parameter` shape.

**Behavior changes (bug fixes)**

- Finished builds no longer report `state: PENDING_INPUT`. A non-building build is always `DONE`, regardless of whether a stale `pendingInput` action marker is still attached to Jenkins's core `/api/json` response.
- Live paused builds now populate `pendingInput.id`, `pendingInput.message`, and `pendingInput.parameters` correctly. The data is sourced from `/wfapi/pendingInputActions` rather than the core `actions[]` array, which only carries an `_class` marker.

See [`docs/schema.md §3.7` and `§3.9`](./docs/schema.md) for the full field reference and endpoint wire-format details.

## Develop

```sh
make build             # build ./bin/jk
make test              # unit + integration with -race
make test-e2e          # end-to-end (requires `make e2e-up` first)
make lint              # golangci-lint v2, zero warnings required
make release-snapshot  # local cross-platform snapshot via GoReleaser
make help              # full target list
```

Requires Go 1.22+ and `golangci-lint` v2.

## Docs

- [`SPEC.md`](./SPEC.md) — engineering practice, tech stack, boundaries.
- [`openspec/`](./openspec/) — change management; behavior changes go through OpenSpec.
- [`docs/schema.md`](./docs/schema.md) — output schema contract (field reference, enum catalog, versioning policy).
