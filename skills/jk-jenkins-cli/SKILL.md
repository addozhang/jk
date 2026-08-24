---
name: jk-jenkins-cli
description: Operate Jenkins from AI coding agents using the `jk` CLI. Use this skill whenever the user mentions Jenkins, Jenkins Pipeline, CI failures, build logs, archived artifacts or reports, failed deployments, pending Jenkins input steps, or asks an agent to inspect, trigger, watch, retry, download, or debug Jenkins builds from a terminal.
---

# jk Jenkins CLI

Use `jk` to inspect and operate Jenkins Pipelines from the terminal.

## Core Model

`jk` is Pipeline-native:

- Jenkins URLs are the primary identity. Use the exact URL the user would paste into a browser.
- Credentials are selected by normalized Jenkins host.
- Output is stable and self-owned. Prefer `-o json` for agent parsing, YAML for humans, and `-o raw` only when a command is explicitly raw/log-oriented.
- The tool is Pipeline-focused. Do not expect plugin, agent, credential-store, Freestyle, or Jenkinsfile-editing administration commands.
- It can verify the authenticated Jenkins identity, inspect pipelines, trigger or rebuild builds, watch builds, read logs, inspect stages and submitted parameters, fetch archived artifacts, and respond to pending Pipeline `input` steps.

## First Move

When Jenkins work is requested:

1. Check whether `jk` is available with `jk version` or `jk --help`.
2. If the command shape is unclear, run `jk <group> --help` rather than guessing.
3. Ask for the Jenkins URL if the user has not provided enough information to identify the pipeline, folder, or build.
4. Prefer `-o json` for status, pipeline, parameter, and stage inspection so the result is machine-readable.
5. Prefer stage-scoped logs when stages identify the failing area. Inspect full logs only when the failed stage is unknown or the user asks for full logs.

## Command Reference

```sh
jk auth add <jenkins-url-or-host>
jk auth list -o json
jk auth whoami <jenkins-url-or-job-url> -o json
jk pipeline info <pipeline-url> -o json
jk pipeline params <pipeline-url> -o json
jk pipeline list <folder-url> -o json
jk build trigger <pipeline-url> [-p KEY=VALUE ...] [--watch]
jk build rebuild <build-url>
jk build status <build-url> -o json
jk build params <build-url> -o json
jk build artifacts <build-url> -o json
jk build artifact <build-url> <relative-path> --destination <file> [--force]
jk build artifacts fetch <build-url> --directory <dir> [--force]
jk build stages <build-url> -o json
jk build logs <build-url> [--stage NAME] [-f]
jk build input <build-url> proceed|abort [--input-id ID] [-p KEY=VALUE ...]
jk build cancel <build-url> [--wait]
```

## URL Handling

Use Jenkins URLs instead of inventing job names. Common shapes:

```text
https://host/job/pipeline
https://host/job/folder/job/pipeline
https://host/job/folder/job/sub/job/pipeline
https://host/job/multibranch/job/main
https://host/job/pipeline/42
https://host/job/pipeline/lastBuild
https://host/job/folder/
```

Build commands that inspect a build usually need a build URL, not just a pipeline URL. If the user only gives a pipeline URL and asks about the latest build, use a Jenkins permalink such as `<pipeline-url>/lastBuild` when appropriate.

Accepted build permalinks:

```text
lastBuild
lastCompletedBuild
lastSuccessfulBuild
lastUnsuccessfulBuild
lastFailedBuild
lastStableBuild
lastUnstableBuild
```

## Common Workflows

### Inspect a Failing Build

1. Resolve the target build URL. If the user gives a pipeline URL, start with `/lastBuild` or ask which build if ambiguity matters.
2. Run `jk build status <build-url> -o json`.
3. Run `jk build stages <build-url> -o json` to find the failed stage or branch.
4. Run `jk build logs <build-url> --stage <failed-stage>` when stages identify a failed stage; otherwise inspect the full build log.
5. Report the first actionable failure, not just the final failure summary.

Example:

```sh
jk build status https://jenkins.example.com/job/app/job/main/lastBuild -o json
jk build stages https://jenkins.example.com/job/app/job/main/lastBuild -o json
jk build logs https://jenkins.example.com/job/app/job/main/lastBuild --stage Test
```

### Trigger and Watch a Build

Inspect parameters before triggering when the pipeline may be parameterized:

```sh
jk pipeline params https://jenkins.example.com/job/app/job/main -o json
jk build trigger https://jenkins.example.com/job/app/job/main -p BRANCH=main --watch
```

When `--watch` is used, interpret exit codes as build results:

```text
0 SUCCESS
1 FAILURE
2 UNSTABLE
3 ABORTED
4 PENDING_INPUT
10 jk-level error such as URL, auth, network, TLS, CSRF, or malformed response
```

Do not treat a non-zero `--watch` exit as a generic CLI failure. Exit codes `1` through `4` are Jenkins build states and should guide follow-up inspection.

### Rebuild a Specific Build

Use `jk build rebuild` only when the user explicitly wants to re-trigger a specific historical build. It reads that build's recorded parameters, validates that they are still defined, and triggers the same pipeline without depending on the Jenkins Rebuild plugin.

```sh
jk build params https://jenkins.example.com/job/app/42 -o json
jk build rebuild https://jenkins.example.com/job/app/42
```

Safety rules:

- Treat rebuild as state-changing. Do not run it when the user only asks to inspect, diagnose, or explain a failure.
- Use the exact numeric or permalink build URL the user selected; do not silently substitute `lastBuild` when the requested source build is ambiguous.
- For production, release, deployment, or destructive pipelines, confirm before rebuilding unless the user explicitly requested that exact rebuild.
- If Jenkins redacted a password or credential parameter, or a recorded parameter no longer exists, do not guess or omit it. Explain the error and use `jk build trigger -p KEY=VALUE` only after the user supplies or authorizes explicit replacement values.
- Rebuild reproduces recorded parameters only. It does not reproduce workspaces, environment variables, SCM revisions, causes, or plugin-specific state.

### Fetch Archived Artifacts and Reports

Treat reports as ordinary artifacts when the Pipeline published them with `archiveArtifacts`. `jk` does not discover plugin-specific report APIs, Pipeline stashes, workspace files, or files that Jenkins did not archive.

List metadata before downloading when the exact relative path is unknown:

```sh
jk build artifacts https://jenkins.example.com/job/app/42 -o json
```

Download one known artifact to an explicit file:

```sh
jk build artifact https://jenkins.example.com/job/app/42 dist/app.zip \
  --destination ./app.zip
```

Download all artifacts while preserving their archived directory structure:

```sh
jk build artifacts fetch https://jenkins.example.com/job/app/42 \
  --directory ./artifacts
```

Artifact rules:

- Numeric build URLs and Jenkins build permalinks are accepted. Prefer a numeric build URL when reproducibility matters because a permalink can point to a newer build later.
- Single-file fetch requires an exact `relativePath` from `build artifacts`; do not guess from `fileName` when multiple directories may contain the same name.
- Downloads stream to temporary files and become visible only after completion. Existing files are rejected unless `--force` is supplied.
- `--force` only replaces regular files. The CLI rejects directory, symlink, traversal, absolute, and escaping artifact paths.
- Bulk fetch is sequential and fail-fast. Files completed before an error remain in place; report the failing `relativePath` and do not claim the directory is complete.
- Do not use `-o raw` to download artifact content. Use `--destination` or `--directory`.

### Handle a Pending Input Step

For any pending input request, inspect the gate before asking for approval or changing state. If the user asks to approve a pending input but has not explicitly authorized `proceed`, answer with the read-only inspection command first, then ask for confirmation.

1. Run `jk build status <build-url> -o json`.
2. Inspect `pendingInput`, including IDs, allowed parameter names, types, and choices.
3. If multiple inputs are pending, use `--input-id ID` instead of relying on a default.
4. If the user explicitly approves the action, submit `proceed`; if the user asks to stop the gate, submit `abort`.
5. Confirm before proceeding with production, release, destructive, or deployment input steps unless the user already authorized that exact action.

Example:

```sh
jk build status https://jenkins.example.com/job/deploy/42 -o json
jk build input https://jenkins.example.com/job/deploy/42 proceed --input-id deployGate -p ENV=prod -p DRY_RUN=false
jk build input https://jenkins.example.com/job/deploy/42 abort --input-id deployGate
```

For an unconfirmed production approval request, respond in this shape:

```text
I will not approve it yet. First inspect the pending gate:
jk build status <build-url> -o json
After confirming the pendingInput details, ask the user to explicitly confirm proceed before running jk build input ... proceed.
```

### Cancel a Running Build

Use `jk build cancel` to stop a running build — the equivalent of the Jenkins UI Stop button. The build is marked ABORTED after Jenkins completes any cleanup blocks.

```sh
# Stop immediately, see state at the moment of the request
jk build cancel https://jenkins.example.com/job/deploy/42

# Stop and wait until ABORTED; exits with code 3
jk build cancel https://jenkins.example.com/job/deploy/42 --wait
```

Confirm with the user before cancelling a production or long-running build unless they have explicitly requested it. After cancellation, verify with `jk build status` if needed.

### Discover Pipelines in a FolderUse folder URLs with `pipeline list`:

```sh
jk pipeline list https://jenkins.example.com/job/platform/ -o json
jk pipeline info https://jenkins.example.com/job/platform/job/api -o json
```

If `pipeline list` says the URL is a pipeline rather than a folder, switch to `pipeline info` for that URL.

## Credentials and TLS

Use `jk auth add <host-or-url>` for first-time setup. It stores credentials under `~/.config/jk/credentials` with file mode `0600` and never prints tokens.

Use `jk auth whoami <url> -o json` to verify which Jenkins identity the selected credentials authenticate as. The URL may be a Jenkins root URL or job URL; context paths are preserved. An `authenticated: false` response is a successful anonymous identity response, not a token leak or parser failure.

Security rules:

- Do not ask the user to paste Jenkins tokens into chat if an interactive terminal path is available.
- Do not print credential file contents.
- Do not include tokens in command lines, logs, issue comments, or summaries.
- Use `SSL_CERT_FILE=/path/to/ca.pem` for private CAs when possible.
- Use `--insecure` only as a last resort, and mention that it disables certificate verification.

## Output Handling

Prefer `-o json` for commands whose output will be parsed or summarized:

```sh
jk build status <build-url> -o json
jk build artifacts <build-url> -o json
jk build stages <build-url> -o json
jk pipeline params <pipeline-url> -o json
```

Structured output starts with `schemaVersion: "1"`. If writing automation around `jk`, check the schema version before relying on fields.

When reading logs, prefer `--stage NAME` after identifying the failed stage. Use `-f` only when the user asked to watch a running build. Focus on the earliest actionable error because Jenkins logs often contain repeated downstream failures after the root cause.

## Safety

Read-only inspection is safe by default:

```sh
jk pipeline info ...
jk pipeline params ...
jk pipeline list ...
jk auth list ...
jk auth whoami ...
jk build status ...
jk build params ...
jk build artifacts ...
jk build stages ...
jk build logs ...
```

Artifact downloads write local files but do not mutate Jenkins. Before using `--force`, confirm that replacing the local destination is intended.

State-changing commands need clear user intent:

```sh
jk build trigger ...
jk build rebuild ...
jk build input ... proceed|abort
jk build cancel ...
jk auth add ... --force
jk auth remove ...
```

For pending input requests, first run the read-only inspection command:

```sh
jk build status <build-url> -o json
```

Use the returned `pendingInput` details to identify the exact gate. Then ask one short confirmation before production deploys, release jobs, destructive jobs, or approving pending input steps unless the user explicitly requested that exact action.

## Error Triage

`jk` user-facing errors usually include a code, message, and suggestion. Follow the suggestion first.

Common cases:

- Bad URL or missing build number: use a full Jenkins URL and append a build number or permalink for build inspection.
- Auth failure: run `jk auth list -o json` to see configured hosts, then `jk auth add <host>` if needed.
- Unclear authenticated identity: run `jk auth whoami <url> -o json`; never inspect or print the credentials file.
- Unknown parameter: run `jk pipeline params <pipeline-url> -o json` and retry with valid names.
- Rebuild parameter unavailable or removed: do not retry by dropping it; inspect `jk build params <build-url> -o json` and require explicit replacement values through `jk build trigger`.
- Artifact not found: run `jk build artifacts <build-url> -o json` and use the exact `relativePath`; confirm the Pipeline archived the file and retention has not deleted it.
- Artifact destination exists: choose another path or use `--force` only when replacing that regular local file is intended.
- TLS failure: prefer `SSL_CERT_FILE`; avoid `--insecure` unless explicitly acceptable.
- `PENDING_INPUT`: inspect `pendingInput`; use `--input-id` when ambiguous; do not auto-proceed without user intent.

## Agent Response Pattern

When reporting Jenkins findings back to the user:

1. State the Jenkins object inspected: pipeline URL or build URL.
2. State the build state/result and failed stage if known.
3. Provide the first actionable error from logs.
4. Suggest the smallest next action: fix code/config, retry build, provide input, or update credentials.
5. Mention if inspection was limited by missing auth, missing URL, unavailable `jk`, or truncated logs.
