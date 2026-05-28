## 1. Parser: recognize permalinks (RED → GREEN)

- [x] 1.1 Add failing table-driven test cases to `internal/jenkinsurl/parse_test.go` covering: each of the 7 permalinks on a top-level job, `lastSuccessfulBuild` on a folder-nested path, mutual exclusion with `BuildNumber`, case mismatch rejected, unknown trailing segment rejected, `APIPath` round-trip for a permalink Ref. Confirm tests fail with the current implementation.
- [x] 1.2 Add `BuildPermalink string` field to `Ref` in `internal/jenkinsurl/parse.go` with doc comment noting mutual exclusion with `BuildNumber`.
- [x] 1.3 Define a package-private allowlist (slice or set) of the 7 permalink names. Document the source (Jenkins core `PermalinkProjectAction`).
- [x] 1.4 Extend `extractJobSegments` so that, after the numeric build-number probe fails, it probes the trailing segment against the permalink allowlist; on match it strips the segment and returns it as a third return value.
- [x] 1.5 Wire the new return value through `Parse` into `Ref.BuildPermalink`.
- [x] 1.6 Update `APIPath` to emit `BuildPermalink` in the build-position slot when set (mirroring the existing `BuildNumber > 0` branch).
- [x] 1.7 Run `go test ./internal/jenkinsurl/...` until all new and existing tests pass.

## 2. Conformance corpus

- [x] 2.1 Add the 7 permalink shapes plus the 2 rejection cases (`latestBuild`, `LASTBUILD`) to `docs/spikes/urls.txt` with annotations matching the existing format.
- [x] 2.2 Confirm `internal/jenkinsurl/parse_test.go` still mirrors the corpus.

## 3. Client: skip the lastBuild pre-flight for permalink Refs

- [x] 3.1 Add a failing unit test in `internal/jenkins/client_test.go` asserting that when `Ref.BuildPermalink != ""`, `GetBuildStatus` (or the equivalent build-fetch entry point) does NOT issue the `tree=lastBuild[number]` pre-flight request and instead calls `<permalink>/api/json` directly.
- [x] 3.2 Branch the existence-check logic in `internal/jenkins/client.go` (around line 126) so it only runs when both `BuildNumber == 0` and `BuildPermalink == ""`.
- [x] 3.3 Add a failing unit test asserting that a permalink request returning 404 surfaces as a normal HTTP error (not the synthetic "never built" message).
- [x] 3.4 Confirm the existing "no lastBuild" friendly-error test still passes for the `BuildNumber == 0 && BuildPermalink == ""` case.

## 4. CLI: thread permalink through build-scoped commands

- [x] 4.1 Audit every `BuildNumber == 0` branch in `internal/cli/build.go` (and any other command file that reads `BuildNumber`). For each, decide whether the branch must also consider `BuildPermalink`. Document decisions inline as comments where non-obvious.
- [x] 4.2 Add a CLI-level test (e.g. `internal/cli/build_test.go`) running `jk build status` against a fake server keyed on a permalink URL, asserting the resolved numeric build number appears in YAML output.
- [x] 4.3 Implement the necessary plumbing (most callers should need zero changes because `APIPath` already does the right thing; the pre-flight branch is the only behavior change).

## 5. Documentation

- [x] 5.1 Update `docs/schema.md` if any schema-touching note is needed (likely just a sentence in the URL-resolution section noting that permalink-shaped URLs are accepted and emit the resolved numeric build in output).
- [x] 5.2 Add a short README note under the "URL forms accepted" section listing the seven permalinks.

## 6. E2E

- [x] 6.1 Add at least one e2e test in `test/e2e/build_e2e_test.go` that builds the `hello` pipeline, then runs `jk build status` against `<host>/job/hello/lastBuild/` and asserts the output's numeric build number matches the build just triggered.
- [x] 6.2 Add a second e2e test against `lastSuccessfulBuild` after a known-good build, exercising the non-`lastBuild` permalink resolution path.

## 7. Release

(Folded into §13.)

## 8. BuildParams schema + mapper (RED → GREEN)

- [x] 8.1 Add failing tests in `internal/schema/types_test.go` for `BuildParams` JSON marshaling: camelCase fields, empty `parameters` array serializes as `[]` not `null`, `BuildParameter` with `value: null` round-trips faithfully.
- [x] 8.2 Define `BuildParams` struct in `internal/schema/types.go` with fields `BuildURL string`, `BuildNumber int`, `Parameters []BuildParameter` — all `stable`. Define new `BuildParameter` struct `{Name string, Value any}` distinct from the existing `Parameter` (definitions); document the distinction inline.
- [x] 8.3 Add failing tests in `internal/schema/mapper_build_test.go` for `MapBuildParams` covering: a build with two parameters, a build with no `ParametersAction` entry in `actions[]` (returns empty slice), a build whose `actions[]` contains unrelated `_class` entries mixed with one ParametersAction, a parameter with `value: null` preserved as null.
- [x] 8.4 Implement `MapBuildParams(raw []byte) (*BuildParams, error)` in `internal/schema/mapper_build.go`: parse the build envelope, walk `actions[]`, filter by `_class == "hudson.model.ParametersAction"`, copy `name`/`value` into `[]BuildParameter`.
- [x] 8.5 Handle duplicate parameter names across multiple ParametersAction entries with last-write-wins semantics; document inline.

## 9. Client method

- [x] 9.1 Add failing tests in `internal/jenkins/client_test.go` for `GetBuildParams`: tree query shape `number,url,actions[parameters[name,value]]`, permalink Ref path uses `<permalink>/api/json` not numeric, numeric Ref path uses `<number>/api/json`.
- [x] 9.2 Implement `Client.GetBuildParams(ctx, ref) ([]byte, error)` in `internal/jenkins/client.go`; reuse `APIPath` so permalink + numeric paths share one code path; skip the `lastBuild[number]` pre-flight (gated on `BuildPermalink != "" || BuildNumber > 0`).

## 10. CLI subcommand

- [x] 10.1 Add failing CLI tests in `internal/cli/build_test.go` for `jk build params <url>` covering YAML and `-o json` output, asserting `buildNumber`, `buildUrl`, and `parameters[]` content.
- [x] 10.2 Add failing CLI test for `jk build params <url>/lastSuccessfulBuild/` asserting the resolved numeric `buildNumber` appears in output.
- [x] 10.3 Implement `newBuildParamsCommand` in `internal/cli/build.go`; wire it into the `build` subcommand tree alongside `status`/`stages`/`input`/`logs`.
- [x] 10.4 `--help` text includes one example invocation and notes that redacted (password / credentials) parameters surface as `value: null`.

## 11. E2E

- [x] 11.1 Add e2e test in `test/e2e/build_e2e_test.go` (or a new `build_params_e2e_test.go`) that triggers the `params` pipeline with known `-p ENV=staging -p DRY_RUN=true`, captures the build number, then runs `jk build params <build-url>` and asserts the returned parameter values match what was submitted.
- [x] 11.2 Add a second e2e test that runs `jk build params <host>/job/params/lastBuild/` against the same trigger and asserts the resolved numeric `buildNumber` equals the captured one (exercises the permalink synergy explicitly).

## 12. Docs

- [x] 12.1 README: add `jk build params` to the command list with one example invocation under the build-commands section; add a short paragraph under "URL forms accepted" enumerating the seven permalinks.
- [x] 12.2 `docs/schema.md`: add a `BuildParams` subsection under §3 documenting the type's fields, stability tier, and the null-value behavior for redacted parameters; add a one-liner under the URL-resolution discussion noting that permalink-shaped URLs are accepted and that output records the resolved numeric build.

## 13. Release gate (combined)

- [x] 13.1 Run `make test-unit && make lint` for every touched package; confirm green.
- [x] 13.2 Run the full e2e suite against the local harness (`make test-e2e` or equivalent); confirm green.
- [ ] 13.3 Manual dogfood: copy a `lastBuild` URL from a real Jenkins UI, run `jk build status` and `jk build params` against it; confirm both succeed and the resolved numeric build number is correct.
- [ ] 13.4 Decide v0.2.1 (patch — permalinks alone arguably fit) vs v0.3.0 (minor — new `build params` command is additive surface area). Recommendation: v0.3.0 because a new command warrants a minor bump under semver-flavored intent. Tag and release once 13.1–13.3 pass.
