# jk

A Pipeline-native CLI for Jenkins.

`jk` is for the day-to-day pipeline operator: inspect a job, trigger a build, watch it, tail its log, answer an `input` step — all from the terminal, all driven by the Jenkins URL you'd paste into a browser.

**What jk is**
- URL-as-identity: every command takes a Jenkins URL; the hostname picks the stored credentials.
- Stable, self-owned schema: `yaml` (default), `json`, or `raw`. Output begins with `schemaVersion: "1"`; breaking changes bump the version.
- Pipeline-aware: understands wfapi (stage tree, parallel branches, pending input).
- Scripting-friendly: `--watch` returns the build result via exit code; `-o json` pipes cleanly into `jq`.

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
```

## First-time setup

### 1. Add a host credential

```sh
jk auth add https://jenkins.example.com
# Token: (paste your Jenkins API token — input is hidden)
```

Credentials are stored in `~/.config/jk/credentials` (mode `0600`, TOML format). Tokens are never printed by any `jk` command.

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

Normalisation rules:
- Trailing slashes are stripped.
- Default ports (`:80` for `http`, `:443` for `https`) are dropped when looking up credentials, so `https://jenkins.example.com` and `https://jenkins.example.com:443` resolve to the same credential entry.
- The hostname (scheme + host + non-default port) is the credential lookup key — one entry per Jenkins instance.

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
