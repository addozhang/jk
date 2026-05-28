## Why

Two related gaps in how `jk` addresses builds and their parameters:

**1. Jenkins permalink URLs are rejected.** Jenkins exposes every job under symbolic permalink paths — `lastBuild`, `lastSuccessfulBuild`, `lastFailedBuild`, `lastStableBuild`, `lastUnstableBuild`, `lastUnsuccessfulBuild`, `lastCompletedBuild` — and users routinely copy these URLs out of the Jenkins UI ("Permalinks" sidebar) and dashboards. Today `jk` rejects every such URL with "not a Jenkins job URL (no /job/ segments found)" because the parser only recognizes numeric trailing segments as build references. Users have to manually rewrite the URL to a numeric build number or trim the permalink off, which contradicts the project's URL-as-identity contract: any URL Jenkins itself emits should be accepted.

**2. No command surfaces the parameters used to trigger a specific build.** `jk pipeline params <url>` returns parameter *definitions* declared on a pipeline (name, type, default, choices), but nothing returns the *values* that were actually passed when build #42 was triggered. Answering "what did this build run with?" today requires the Jenkins UI. The gap pairs naturally with permalinks: `jk build params <url>/lastSuccessfulBuild/` is a one-liner that becomes possible only when both land together.

## What Changes

**Permalinks**
- Parser recognizes the seven Jenkins permalinks as valid trailing build references on any job URL.
- `Ref` gains a `BuildPermalink string` field. It is mutually exclusive with `BuildNumber > 0`; when a permalink is present the parser sets `BuildPermalink` and leaves `BuildNumber == 0`.
- `Ref.APIPath` emits the permalink as a literal path segment (Jenkins resolves it server-side), so existing callers that build `<host>/<segs>/<bn>/<suffix>` get `<host>/<segs>/lastSuccessfulBuild/<suffix>` automatically.
- Build-scoped commands (`jk build status`, `jk build stages`, `jk build logs`, `jk build input`, and the new `jk build params`) accept permalink URLs; output records the resolved numeric build number returned by Jenkins so downstream consumers (e.g. `-o json`) get a stable identity.
- Unknown trailing non-numeric segments continue to fail with the existing "not a Jenkins job URL" error (no silent acceptance of typos like `latestBuild`).

**Build parameters view**
- New `jk build params <build-url>` command returning the trigger-time parameter values for a specific build.
- New `BuildParams` schema type, `stable` from day one: `{schemaVersion, buildUrl, buildNumber, parameters[]}`.
- The `parameters` field reuses the existing `Parameter` struct (same shape as `jk pipeline params` returns), so users see one consistent parameter model across both commands.
- Reads `<build>/api/json?tree=number,url,actions[parameters[name,value]]` and filters `actions[]` by `_class == "hudson.model.ParametersAction"`.
- Builds with no parameters return an empty `parameters` array, not an error.
- No `--show-secrets` flag in v1 — Jenkins redacts password/credentials parameters server-side and returns `null` for their value regardless of caller permissions; we surface `null` faithfully and document the behavior.

## Capabilities

### New Capabilities
<!-- none -->

### Modified Capabilities
- `url-resolution`: permalink trailing segments are recognized as build references and surface a new `BuildPermalink` field on the parsed reference.
- `build`: build-scoped commands accept permalink URLs and resolve them via the Jenkins API the same way they resolve `BuildNumber == 0` today; a new `jk build params` subcommand returns trigger-time parameter values for a specific build.

## Impact

- `internal/jenkinsurl/parse.go` — recognize the 7 permalinks during trailing-segment extraction; add `Ref.BuildPermalink`; teach `APIPath` to emit it.
- `internal/jenkinsurl/parse_test.go` and `docs/spikes/urls.txt` — extend the conformance corpus.
- `internal/jenkins/client.go` — gain a `GetBuildParams` method; existing `BuildNumber == 0` branches also check `BuildPermalink != ""` (treat as "Jenkins resolves it" rather than "fetch lastBuild via tree query").
- `internal/cli/build.go` — new `newBuildParamsCommand`; permalink-aware code paths.
- `internal/schema/types.go` — new `BuildParams` type.
- `internal/schema/mapper_build.go` — new `MapBuildParams` that filters `actions[]` by `_class`.
- `docs/schema.md` and `README.md` — document `BuildPermalink` carrying through URL formatting; document `BuildParams` type and the new command with example.
- No new dependencies. No breaking changes — existing numeric-build URLs, bare-job URLs, and all current command behaviors remain bit-identical.
