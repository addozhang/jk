# Spec: jk — Pipeline-Native Jenkins CLI

> **This document is the project constitution.** It defines how we work in this repository: toolchain, layout, style, boundaries.
> **Product specifications live elsewhere**: `openspec/changes/` for in-flight changes, `openspec/specs/` for archived capability contracts, `docs/schema.md` for the external output contract.
> Changes to this file should be rare events; product behavior changes go through the OpenSpec change workflow.

## Objective

Build `jk`: a Go-written command-line tool organized around Jenkins Pipeline concepts.

- **User**: developers working across multiple Jenkins instances who want to handle trigger / status / stages / input / logs from the terminal without opening a browser.
- **Core bets**: URL as identity (no profile/context switching) + a stable self-owned schema (isolating users from Jenkins version drift) + kubectl-style output (`-o yaml|json|raw`, YAML by default).
- **MVP completion criteria**: see §Success Criteria. Full capability behavior lives in `openspec/changes/init-jk-jenkins-cli/specs/`.

## Language Policy

All project artifacts — source code, comments, commit messages, documentation, command help text, error messages, schema field names, this SPEC, OpenSpec artifacts (`proposal.md`, `design.md`, `specs/**/*.md`, `tasks.md`), and PR descriptions — **MUST be written in English**. No exceptions. This rule applies even when the contributor is more comfortable in another language; use a translator if needed.

Rationale: the project is open-source-by-default, the Go ecosystem is English-first, and a mixed-language repository becomes painful for tooling (grep, linters, search) and future contributors.

## Tech Stack

- **Language**: Go 1.22+
- **CLI framework**: `github.com/spf13/cobra`
- **YAML**: `sigs.k8s.io/yaml` (kubectl's choice; JSON tag compatible)
- **TOML (credentials file)**: `github.com/BurntSushi/toml`
- **HTTP**: standard library `net/http` (no third-party Jenkins client — they are all incomplete and would pollute the schema isolation)
- **Logging (debug)**: standard library `log/slog`
- **Testing**: standard library `testing` + `net/http/httptest` (no testify/ginkgo)
- **Lint**: `golangci-lint` (config in `.golangci.yml`, strict preset)
- **Release**: GoReleaser + GitHub Actions
- **Distribution channels**: GitHub Releases (binaries), `go install github.com/addozhang/jk/cmd/jk@latest`, Homebrew tap at `addozhang/homebrew-jk`.
- **Repository visibility**: **private** during MVP. Consumers of `go install` must set `GOPRIVATE=github.com/addozhang/*` and have repo read access; CI runners use a deploy key or fine-grained PAT.
- **Min platforms**: macOS arm64/amd64, Linux amd64/arm64. Windows best-effort.

## Commands

All commands run from the repository root.

```
# Daily development
make build              # go build -o bin/jk ./cmd/jk
make test               # go test ./... -race -cover
make test-unit          # go test ./internal/... -race -cover
make test-integration   # go test ./test/integration/... -race
make lint               # golangci-lint run ./...
make fmt                # gofmt -s -w . && goimports -w .
make tidy               # go mod tidy

# Dev loop
make watch              # watch files and re-run tests (optional, via air or reflex)

# Manual verification
./bin/jk version
./bin/jk auth add https://jenkins.example.com

# Release
make release-snapshot   # goreleaser release --snapshot --clean
make release            # runs in CI only; local release is forbidden
```

Any change that introduces a new top-level make target must justify itself in the PR description.

## Project Structure

```
/
├── SPEC.md                          ← this document (project constitution)
├── README.md                        ← user docs: what is jk, install, 3 examples
├── go.mod / go.sum
├── Makefile
├── .golangci.yml
├── .goreleaser.yml
│
├── cmd/
│   └── jk/                          ← main entry, flag registration, command wiring
│       └── main.go
│
├── internal/                        ← all business code; not importable outside the module
│   ├── cli/                         ← Cobra command implementations, one file per group
│   │   ├── auth.go
│   │   ├── pipeline.go
│   │   ├── build.go
│   │   └── version.go
│   ├── jenkins/                     ← HTTP client, CSRF, TLS, low-level API calls
│   ├── jenkinsurl/                  ← URL parsing → Ref struct
│   ├── auth/                        ← credentials file IO, host lookup
│   ├── schema/                      ← self-owned schema types + mappers
│   ├── output/                      ← YAML/JSON/raw renderers
│   └── errors/                      ← JKError type + translation layer
│
├── test/
│   └── integration/                 ← cross-package integration tests (real httptest.Server)
│
├── docs/
│   ├── schema.md                    ← external output contract (schemaVersion, stability tiers)
│   ├── spikes/                      ← early exploration notes (wfapi shape, URL samples, etc.)
│   └── adr/                         ← architecture decision records (when design.md is insufficient)
│
└── openspec/                        ← change management (maintained by the OpenSpec tool)
    ├── changes/                     ← in-flight changes
    └── specs/                       ← archived capability contracts (authoritative product behavior)
```

**Hard rules:**

- Unit tests live next to the code they test, file name `*_test.go`.
- Cross-package integration tests live under `test/integration/`.
- No business code outside `internal/` (prevents accidental API export).
- One file per command group (auth / pipeline / build); split when a file exceeds ~300 lines.

## Code Style

`gofmt -s` + `goimports` + `golangci-lint` decide most things. Project-specific conventions follow.

### Style example (one code block worth more than three paragraphs of prose)

```go
// Package cli implements the `jk` subcommands.
package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/addozhang/jk/internal/errors"
	"github.com/addozhang/jk/internal/jenkins"
	"github.com/addozhang/jk/internal/jenkinsurl"
	"github.com/addozhang/jk/internal/output"
	"github.com/addozhang/jk/internal/schema"
)

// newBuildStatusCmd builds the `jk build status <url>` subcommand.
//
// It resolves the URL into a Ref, fetches the build status from Jenkins,
// maps the raw response into schema.BuildStatus, and renders it via the
// global output format.
func newBuildStatusCmd(deps *Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "status <url>",
		Short: "Show the current status of a build",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := jenkinsurl.Parse(args[0])
			if err != nil {
				return errors.WrapURLParse(args[0], err)
			}

			raw, err := deps.Client.GetBuildStatus(cmd.Context(), ref)
			if err != nil {
				return errors.TranslateJenkins(ref.HostKey(), err)
			}

			status, err := schema.MapBuildStatus(raw)
			if err != nil {
				return fmt.Errorf("map build status: %w", err)
			}

			return output.Write(cmd.OutOrStdout(), status, deps.OutputFormat)
		},
	}
}
```

### Key conventions

1. **Language**: every identifier, comment, and string literal that may be displayed to users is written in English. See §Language Policy.

2. **Naming**
   - Package names: single word, lowercase, no underscores (`jenkinsurl`, not `jenkins_url`).
   - Exported types / functions: doc comments start with the identifier (Go standard).
   - Error variables: `ErrFoo`. Error types: `FooError`.
   - Schema fields: every `yaml` and `json` tag must match exactly (camelCase).

3. **Error handling**
   - No bare `return err`; always wrap with context: `return fmt.Errorf("doing X: %w", err)`.
   - User-facing errors go through `internal/errors` for translation; lower layers return wrapped errors and let the top layer translate.
   - `panic` is only for "this cannot happen" internal invariants. Anything from IO, parsing, or user input returns an error.

4. **Context**
   - Every function that may do IO takes `ctx context.Context` as its first parameter.
   - Cobra commands get the context via `cmd.Context()` (already bound to `--timeout`).

5. **Dependency injection**
   - Command constructors take a `*Deps` (HTTP client, auth store, output format, etc.) to keep them testable.
   - No global mutable state; the only allowed global is `slog.Default()` for `--debug` logging.

6. **Visibility**
   - Default to unexported; export only when another package needs it.
   - `internal/cli` exports nothing; outside code interacts only through CLI invocation.

7. **Tests**
   - Table-driven tests are the default pattern (`tests := []struct{...}{...}; for _, tt := range tests {...}`).
   - Test names: `Test_<func>_<scenario>` (underscores; readability wins over idiomatic).
   - Use `httptest.NewServer` for Jenkins API mocking; never mock standard library types directly.

8. **Comments**
   - Every exported symbol has a godoc comment.
   - Comments explain *why*, not *what* (the code already shows the what).
   - TODO must reference an issue or owner: `// TODO(addozhang): see openspec/changes/.../tasks.md §11.8`.

## Testing Strategy

| Layer | Location | How to run | Coverage target |
|---|---|---|---|
| **Unit** | `internal/<pkg>/*_test.go` | `make test-unit` | core packages ≥ 70% |
| **Integration** | `test/integration/*_test.go` | `make test-integration` | happy path + main sad paths |
| **End-to-end** | real Jenkins via Docker | manual + CI nightly | ≥ 1 real call per MVP command |

**Core packages must have unit tests** (70% coverage gate enforced in CI):

- `internal/jenkinsurl`
- `internal/schema` (especially the mapper code)
- `internal/auth`
- `internal/output`
- `internal/errors`

**Integration tests use `httptest.Server`**: record real Jenkins responses under `test/integration/testdata/`, and have the server return fixtures by path. **Never connect to a real Jenkins in tests** (CI cannot reach it; developer machines differ wildly).

**End-to-end tests** live under `test/e2e/` (separate directory), require a local `jenkins/jenkins:lts` Docker container, and are gated behind `-tags=e2e`. They are not part of `make test`.

**TDD bias**: when adding behavior, write the table-driven test first and the implementation second; when fixing a bug, write a failing test that reproduces it before changing any production code.

## Boundaries

### Always do

- Run `make test` + `make lint` before pushing; both must be green locally.
- Run `make fmt` and let gofmt/goimports decide formatting (no manual formatting).
- Document public output fields in `docs/schema.md` **before** implementing them.
- Confirm the corresponding OpenSpec change's task status before multi-file work.
- Update README or docs in the same change that alters user-visible behavior.
- Reference the corresponding OpenSpec change path in every PR description.
- Write all identifiers, comments, docs, and user-facing strings in English (§Language Policy).

### Ask first

- Add a new third-party dependency (even a dev-only one).
- Modify any field tagged `stable` in `docs/schema.md` (any rename, type change, or removal).
- Change the `schemaVersion` constant.
- Change `Makefile` / `.golangci.yml` / `.goreleaser.yml` / GitHub Actions workflows.
- Add a new top-level directory (anything outside `/cmd`, `/internal`, `/test`, `/docs`, `/openspec`).
- Bypass the OpenSpec workflow to edit anything under archived `openspec/specs/`.
- Raise the Go minimum version.
- Introduce `unsafe`, `cgo`, build constraints, or assembly.

### Never do

- Commit API tokens, credentials, `.env` files, or personal Jenkins URLs.
- Add a `vendor/` directory to the repo (use `go mod`).
- Delete or skip a failing test without fixing it (a `t.Skip` must reference an issue).
- Edit anything under archived `openspec/changes/<archived>/`.
- Put business logic in `cmd/jk/` (main does wiring only).
- Export `internal/` types outside the `internal/` tree.
- Use `panic` for expected runtime failures (auth, network, parse, etc.).
- Print to stdout/stderr bypassing `internal/output` and `internal/errors`.
- Silently swallow errors (`_ = err`, empty `if err != nil {}`).
- Write code, comments, docs, or commit messages in a language other than English.
- Add a TUI, AI features, notifications, or daemons — see the "Not Doing" list in the OpenSpec proposal.

## Schema Review Workflow

The output schema is the contract scripts depend on. Changes to anything tagged `stable` in `docs/schema.md` follow this workflow regardless of contributor count — it is dormant for the solo phase and activates automatically when a second contributor joins.

1. **OpenSpec change required.** Any rename, type change, removal, or semantic change to a `stable` field must ship inside an OpenSpec change whose `design.md` includes a "Stable schema impact" section listing every affected field as `old.path: oldType → new.path: newType` plus the migration story for existing consumers.
2. **Version bump rule.** Breaking changes (rename, removal, type narrowing, semantic change) bump `schemaVersion`. Additive-only changes (new optional field, new enum value gated by feature detection) do not. The bump decision is recorded in `design.md`.
3. **Review gate.** Once a second contributor exists, every PR that touches a `stable` field requires at least one approving review from a contributor other than the author. In the solo phase, the author MUST self-review by re-reading `docs/schema.md` end-to-end and confirming the change is intentional in the PR description.
4. **Docs-first.** `docs/schema.md` is updated in the same PR as the code change, not in a follow-up.
5. **Experimental escape hatch.** New fields land tagged `experimental` for at least one minor release before promotion to `stable`; promotion itself is a `stable` change and goes through this same workflow.

## Success Criteria

MVP (v0.1.0) "done" criteria — all must be true before tagging a release:

- [ ] `go install github.com/addozhang/jk/cmd/jk@latest` produces a working `jk` binary on macOS (arm64+amd64) and Linux (amd64+arm64).
- [ ] `brew install addozhang/jk/jk` (tap `addozhang/homebrew-jk`) produces a working `jk` binary on the same platforms.
- [ ] With the repo private, `GOPRIVATE=github.com/addozhang/*` + a valid token allows `go install` to succeed; without it, the failure mode is documented in README.
- [ ] Every MVP command in OpenSpec change `init-jk-jenkins-cli` runs its happy path successfully against a real Jenkins instance.
- [ ] `jk -o yaml` output matches `docs/schema.md` `stable` fields 100%; every response's first line is `schemaVersion: "1"`.
- [ ] With `SSL_CERT_FILE` pointing at a valid PEM, `jk` connects to a self-signed Jenkins without `--insecure`; with an invalid path, it emits a clear error.
- [ ] `jk build trigger <url> --watch` exits with the correct code for all five terminal states (0=SUCCESS, 1=FAILURE, 2=UNSTABLE, 3=ABORTED, 4=PENDING_INPUT); `jk`-level failures use ≥10.
- [ ] CI enforces ≥ 70% unit test coverage on `internal/jenkinsurl`, `internal/schema`, `internal/auth`, `internal/output`, `internal/errors`.
- [ ] `make lint` has zero warnings.
- [ ] `make test` is fully green with no race detections.
- [ ] README includes install instructions, three real examples, the exit-code table, and `schemaVersion` pinning guidance.
- [ ] `docs/schema.md` covers every field of every command; each field is tagged `stable` or `experimental`.
- [ ] After two weeks of real-world use, `--raw` invocations account for < 20% of total commands (schema coverage validation).

## Open Questions

1. **Credentials file encryption**: MVP uses 0600 plaintext at `~/.config/jk/credentials`. Deferred to v0.2 — revisit when introducing OS keychain integration (macOS Keychain / Linux Secret Service / Windows Credential Manager). No action required during MVP.

## Relationship to OpenSpec

This repository uses [OpenSpec](https://openspec.dev) for change management. **Any modification that would fall under §Boundaries → Ask first, or that introduces or alters a capability, MUST go through the OpenSpec workflow**:

1. `openspec new change <name>` creates the change directory.
2. Generate `proposal.md` / `design.md` / `specs/**/*.md` / `tasks.md`.
3. After implementation, archive the change so its specs land in `openspec/specs/`.

Division of labor between this document and OpenSpec:

- **This document** governs "how we work in the repo" (toolchain / layout / style / boundaries) — rarely changes.
- **OpenSpec specs** govern "what the product does" (capability behavior) — changes per change.
- **docs/schema.md** governs "the external output contract" (fields, stability tiers) — evolves with the schema.
- **README** governs "how users use it" — evolves with user-visible behavior.

If this document and OpenSpec disagree, OpenSpec wins on product behavior; this document wins on engineering practice.
