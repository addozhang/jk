## Context

`jk` already resolves credentials by Jenkins URL, reads recorded build parameters, triggers pipelines, and resolves queue items. The new commands should compose those existing primitives rather than depend on Jenkins plugins or duplicate transport behavior.

## Goals / Non-Goals

**Goals:**

- Verify the identity selected by stored credentials for root-mounted and context-path Jenkins instances.
- Re-trigger a specified numeric or permalink build with its recorded parameters.
- Prevent a rebuild from silently changing behavior when Jenkins redacts values or the pipeline parameter definition has changed.

**Non-Goals:**

- Reproducing build environment variables, workspaces, SCM revisions, causes, or plugin-specific state.
- Recovering values Jenkins redacts.
- Depending on the Jenkins Rebuild plugin.

## Decisions

1. `auth whoami` calls the core `/whoAmI/api/json` endpoint through the existing authenticated transport. A root or job URL is reduced to its Jenkins origin plus context path so credential resolution remains consistent.
2. `build rebuild` fetches the specified build's `ParametersAction`, converts supported scalar values to form strings, validates every recorded name against the pipeline's current parameter definitions, and then uses the existing trigger and queue-resolution flow.
3. A recorded `null` or unsupported structured value is a hard error. Silent omission could trigger a deployment with defaults and is less safe than requiring an explicit `build trigger -p` invocation.
4. Rebuild output reuses the stable `BuildTrigger` schema. Whoami introduces an additive experimental `AuthWhoAmI` schema.
5. Identity endpoint errors use auth-specific translation; a missing endpoint must not be described as a missing pipeline.
6. The additive commands ship as `v0.7.0`, a minor release after `v0.6.0`. The output `schemaVersion` remains `"1"` because no stable field is removed, renamed, narrowed, or semantically changed.

## Risks / Trade-offs

- [Risk] Jenkins plugins can record non-scalar parameter values. → Reject unsupported values and direct the user to explicit `build trigger -p` values.
- [Risk] Pipeline parameter definitions can change after the original build. → Validate names immediately before triggering and abort on drift.
- [Risk] Anonymous Jenkins may return HTTP 200 with `authenticated: false`. → Render the server response faithfully; only transport/status failures become errors.
- [Risk] Queue resolution waits for an executor. → Reuse the bounded trigger timeout policy already used by `build trigger`.
