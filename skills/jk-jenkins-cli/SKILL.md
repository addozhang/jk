---
name: jk-jenkins-cli
description: Operate Jenkins from AI coding agents using the `jk` CLI. Use this skill whenever the user mentions Jenkins, Jenkins Pipeline, CI failures, build logs, failed deployments, pending Jenkins input steps, or asks an agent to inspect, trigger, watch, retry, or debug Jenkins builds from a terminal.
---

# jk Jenkins CLI

Use `jk` to inspect and operate Jenkins Pipelines from the terminal.

## Core Model

`jk` is Pipeline-native:

- Jenkins URLs are the primary identity. Use the exact URL the user would paste into a browser.
- Credentials are selected by normalized Jenkins host.
- Output is stable and self-owned. Prefer `-o json` for agent parsing, YAML for humans, and `-o raw` only when a command is explicitly raw/log-oriented.
- The tool is Pipeline-focused. Do not expect plugin, agent, credential-store, Freestyle, or Jenkinsfile-editing administration commands.
- It can inspect pipelines, trigger builds, watch builds, read logs, inspect stages, inspect submitted parameters, and respond to pending Pipeline `input` steps.

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
jk pipeline info <pipeline-url> -o json
jk pipeline params <pipeline-url> -o json
jk pipeline list <folder-url> -o json
jk build trigger <pipeline-url> [-p KEY=VALUE ...] [--watch]
jk build status <build-url> -o json
jk build params <build-url> -o json
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
jk build status ...
jk build params ...
jk build stages ...
jk build logs ...
```

State-changing commands need clear user intent:

```sh
jk build trigger ...
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
- Unknown parameter: run `jk pipeline params <pipeline-url> -o json` and retry with valid names.
- TLS failure: prefer `SSL_CERT_FILE`; avoid `--insecure` unless explicitly acceptable.
- `PENDING_INPUT`: inspect `pendingInput`; use `--input-id` when ambiguous; do not auto-proceed without user intent.

## Agent Response Pattern

When reporting Jenkins findings back to the user:

1. State the Jenkins object inspected: pipeline URL or build URL.
2. State the build state/result and failed stage if known.
3. Provide the first actionable error from logs.
4. Suggest the smallest next action: fix code/config, retry build, provide input, or update credentials.
5. Mention if inspection was limited by missing auth, missing URL, unavailable `jk`, or truncated logs.
