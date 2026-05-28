## Context

This change bundles two related additions to how `jk` addresses builds.

**Permalinks.** `internal/jenkinsurl.Parse` is the single entry point that turns a Jenkins URL into a `Ref{Host, JobSegments, BuildNumber}`. The parser is intentionally pure (no I/O) so it can run offline and so credential lookup, debug-redaction, and `APIPath` formatting all share one identity. Today the trailing-segment detector at `internal/jenkinsurl/parse.go:142` only accepts a positive integer as a build reference; every Jenkins symbolic permalink (`lastBuild`, `lastSuccessfulBuild`, `lastFailedBuild`, `lastStableBuild`, `lastUnstableBuild`, `lastUnsuccessfulBuild`, `lastCompletedBuild`) falls through to the `parts[i] != "job"` guard and returns the "not a Jenkins job URL" error. Build-scoped commands already have a "no explicit build number" code path: when `BuildNumber == 0`, `internal/jenkins/client.go:126` queries `<job>/api/json?tree=lastBuild[number]` to discover the latest build. That path works only for `lastBuild`; the other six permalinks have distinct semantics ("last successful", "last failed", etc.) that the Jenkins server resolves natively at `<job>/<permalink>/api/json`.

**Build parameters view.** `jk pipeline params <url>` returns parameter *definitions* (name/type/default/choices) from `<job>/api/json?tree=property[parameterDefinitions[...]]`. No command returns the *values* a specific build was triggered with. Jenkins records those values in the per-build `actions[]` array as a `hudson.model.ParametersAction` entry; the data is one tree query away (`<build>/api/json?tree=number,url,actions[parameters[name,value]]`) but the mapper currently ignores `actions[]` entirely. The two additions ship together because they hit overlapping files (`jenkinsurl`, `client.go`, `build.go`, `mapper_build.go`) and because the natural usage pattern is `jk build params <url>/lastSuccessfulBuild/`, which requires both to land at once.

## Goals / Non-Goals

**Goals:**
- Accept all seven Jenkins permalinks as trailing segments in URL parsing.
- Preserve URL-as-identity: a permalink URL stays a permalink until an HTTP round-trip resolves it; the parsed `Ref` is enough to format every downstream API URL.
- Keep the parser pure and offline.
- Add a `jk build params <build-url>` command that returns trigger-time parameter values for a specific build, with permalink URLs accepted automatically.
- Introduce a dedicated `BuildParameter {name, value}` schema struct for trigger-time submitted values, distinct from the existing `Parameter` struct (which describes parameter *definitions*: type/default/choices/description). Reusing `Parameter` would force its `Default` field to carry submitted values, producing the misleading wire shape `default: prod` for a value the user actually passed at trigger time. Symmetry between `pipeline params` (definitions) and `build params` (values) lives at the command level, not in a shared element struct.
- Existing numeric-build and bare-job URLs continue to behave bit-identically.

**Non-Goals:**
- Resolving permalinks at parse time (would require I/O).
- Supporting non-Jenkins symbolic names (e.g. `latest`, `HEAD`) or typos like `latestBuild`.
- Changing how `BuildNumber == 0` is handled today (commands still fall back to `lastBuild` via the existing tree query when no permalink is present).
- Unredacting password/credentials parameter values — Jenkins masks these server-side and we surface `null` faithfully.
- Embedding trigger-time parameters inside the `BuildStatus` payload (kept as a separate command and type; see D7).
- IPv6 host support (still out of scope per current parser).

## Decisions

**D1. Add `BuildPermalink string` to `Ref`; do not overload `BuildNumber`.**
Rejected alternative: alias every permalink to `BuildNumber == 0` (= `lastBuild`). That silently changes semantics for the six non-`lastBuild` permalinks. A separate field keeps the URL's stated intent explicit and lets `APIPath` round-trip the permalink without ambiguity.

**D2. Mutually exclusive with `BuildNumber`.**
The parser populates exactly one of `{BuildNumber > 0, BuildPermalink != "", neither}`. A URL like `/job/x/42/` sets `BuildNumber=42`; `/job/x/lastBuild/` sets `BuildPermalink="lastBuild"`; `/job/x/` sets neither. Detection order: try numeric first (existing logic), then check the permalink set, otherwise leave both zero.

**D3. Permalink set is a closed allowlist.**
The seven Jenkins-documented names, case-sensitive. Unknown trailing non-numeric segments continue to fail with the existing "not a Jenkins job URL" error rather than being silently accepted. This avoids accidentally treating a typo (`latestBuild`) or a different sub-resource (`config.xml`, `consoleText`) as a build reference.

**D4. `APIPath` emits the permalink literally.**
`<host>/<segs>/lastSuccessfulBuild/api/json` is a valid Jenkins API path — the server resolves the permalink before serving the response. So `APIPath("api/json")` simply substitutes `BuildPermalink` where it would have substituted `strconv.Itoa(BuildNumber)`. Nothing else in the URL formatter needs to change.

**D5. Client code path: `BuildPermalink != ""` skips the `lastBuild[number]` tree query.**
The pre-flight existence check at `client.go:126` exists because `BuildNumber == 0` means "user didn't say which build, find the latest"; it must fail loudly when the pipeline has never been built. When a permalink is present the user *did* specify which build — Jenkins itself will return 404 if e.g. there has never been a successful build, which the existing HTTP error path surfaces cleanly. So the new code path is: if `BuildPermalink != ""`, skip the tree-query pre-flight and let the main API call handle missing-permalink as a normal HTTP error.

**D6. Status output records the resolved numeric build.**
When Jenkins returns a build object for a permalink request, it includes the actual `number`. The mapper already serializes this into `Build.Number`. Downstream consumers (`-o json`, scripts) see the stable numeric identity even though the input was symbolic. The original permalink is not preserved in output — it was an input convenience.

**D7. New `jk build params` command — not a field on `BuildStatus`.**
Rejected alternative: add `Parameters []Parameter` to `BuildStatus`. Reasons for rejection:
- `BuildStatus` answers "what is this build doing right now?" (lifecycle, progress, pause state); trigger-time parameters are immutable historical metadata that nobody polls. Bundling them either forces every `jk build status` caller to pay for an extra `actions[parameters[...]]` tree query, or makes the response inconsistent depending on whether enrichment succeeded — the same trap that drove the v0.2 wfapi gating work.
- A dedicated `BuildParams` type can ship `stable` from day one (closed, well-understood shape); adding `Parameters` to `BuildStatus` would require an `experimental` tag on what is fundamentally stable data.
- `jk build params <url>` mirrors the existing `jk pipeline params <url>`, giving the CLI a discoverable symmetric pair (definitions vs. trigger-time values).
- Failure modes stay isolated: if Jenkins truncates or omits the `ParametersAction` (it happens — plugin variations), only `build params` is affected, not `build status`.

**D8. Mapping `actions[]`.**
Jenkins returns `actions` as a heterogeneous array of `_class`-tagged objects. The mapper filters for entries whose `_class == "hudson.model.ParametersAction"` and reads their `parameters[]` array. If no such action is present (the common case for unparameterized builds), the result is an empty `parameters` slice — never an error. Multiple ParametersAction entries (rare; possible under certain plugin chains) are merged in encounter order with last-write-wins on duplicate names, matching Jenkins' own behavior. The `_class` filter is the only piece of Jenkins-internal type-naming we depend on; documented inline in the mapper.

**D9. Permalink synergy is automatic.**
Because `APIPath` already does the right thing when `BuildPermalink != ""`, the new `GetBuildParams` client method needs zero special-casing for permalink Refs — it composes `APIPath("api/json?tree=...")` and goes. The e2e suite covers this explicitly: one test passes a numeric build URL, a second passes `<job>/lastSuccessfulBuild/`, and both must succeed.

## Risks / Trade-offs

- **[Risk]** A future Jenkins release adds a new permalink (e.g. `lastKeepForeverBuild`). → Mitigation: the allowlist lives in one place (`internal/jenkinsurl/parse.go`); adding a name is a one-line change covered by the conformance corpus.
- **[Risk]** Users paste `lastBuild` URLs expecting `jk` to keep them symbolic across multiple commands (e.g. "tail logs of whatever is latest"). → Trade-off accepted: each invocation re-resolves the permalink, which matches Jenkins' own UI behavior. Documented in the spec's scenarios.
- **[Risk]** Case mismatches (`LASTBUILD`, `lastbuild`). → Decision: reject. Jenkins itself is case-sensitive on these paths; accepting other casings would mis-mirror server behavior.
- **[Risk]** The `BuildPermalink != ""` skip of the existence pre-flight means a 404 on `/job/x/lastSuccessfulBuild/api/json` surfaces as a generic Jenkins HTTP error rather than the friendly "pipeline has never been built" message. → Trade-off accepted; "no successful build yet" is a distinct condition that deserves its own (future) friendly message rather than reusing the never-built copy.
- **[Risk]** Password / credentials parameters return `null` value from Jenkins regardless of caller permissions (Jenkins redacts server-side). → Surface the `null` faithfully in `BuildParams.parameters[].value`; document the behavior in `--help` and README. Do not introduce a `--show-secrets` flag in v1 — there is nothing to show; Jenkins doesn't send the values down the wire to anyone.
- **[Risk]** A pipeline using an exotic parameters plugin may report parameter types we don't yet model in the `Parameter` struct. → The mapper preserves `name` and `value` always; unknown `type` values surface as the raw Jenkins string so users still see *something* useful even if the type isn't in our canonical enum.

## Migration Plan

No migration. Pure additive change. No config, no on-disk format, no wire-format changes. Existing parsed URLs and stored credentials are unaffected. Rollback = revert.

## Open Questions

None.
