# jk

Pipeline-native Jenkins CLI. URLs are the unit of identity; output is YAML (or JSON) with a stable, self-owned schema.

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
| `https://host/job/folder/job/pipeline` | top-level pipeline in a folder |
| `https://host/job/pipeline` | top-level pipeline |
| `https://host/job/pipeline/42` | specific build #42 |
| `https://host/job/pipeline/lastBuild` | symbolic last build |
| `https://host/job/mb/job/main` | multibranch branch pipeline |
| `https://host/job/folder/` | folder (for `pipeline list`) |

Trailing slashes are stripped. Default ports (`:80`, `:443`) are normalised away in credential lookups.

## Scripting with jk

```sh
# JSON output for jq
jk build status https://host/job/pipeline/42 -o json | jq '.result'

# Exit code reflects build result when --watch is used
jk build trigger https://host/job/pipeline --watch
echo $?   # 0=SUCCESS 1=FAILURE 2=UNSTABLE 3=ABORTED 4=PENDING_INPUT

# jk-level errors exit ≥ 10 (authentication, network, bad URL, …)
```

All structured output includes `schemaVersion: "1"` as the first key. Scripts should pin on this value; future breaking changes will increment it.

See [`docs/schema.md`](./docs/schema.md) for the full field reference.

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
