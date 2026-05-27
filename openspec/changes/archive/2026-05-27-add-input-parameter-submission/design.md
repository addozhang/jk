## Context

`jk` v0.1.0 shipped a `jk build input <url> proceed|abort` command that responds to a paused Pipeline `input` step. The `proceed` path hard-codes the `POST .../input/<id>/proceedEmpty` endpoint, which Jenkins only accepts when the input declares zero parameters (or every parameter has a `defaultValue`). The schema layer **already** exposes the input's parameter definitions via `pendingInput.parameters` in `jk build status` output, so users can see that Jenkins is asking for an `ENV` choice between `staging` and `prod` — but the CLI has no flag to express their choice. This proposal closes that gap.

Dogfooding v0.1.0 against the harness on 2026-05-27 surfaced two further bugs in the *read* side that this change must also resolve:

1. `jk build status` derives `pendingInput` from the core `GET /<n>/api/json` `actions[]` array, which only carries the `_class` tag for `InputAction` — never the `id`/`message`/`parameters` fields the schema documents. The populated payload only lives at `/wfapi/pendingInputActions` (which `jk build input` already calls correctly). So in practice every real `build status` against a paused build emits `pendingInput.id: ""` and `pendingInput.parameters: []`, making client-side validation impossible.
2. The `state` switch in `MapBuildStatus` checks `pending != nil` before checking `building`/`result`. After a build completes, the historical `InputAction` marker stays in `actions[]` forever; finished builds therefore report `state: PENDING_INPUT` while simultaneously reporting `building: false` and `result: SUCCESS`. This is a self-contradiction in our own output.

Both bugs sit upstream of the write-path `-p` work: without (1), local parameter validation has nothing to validate against; without (2), `--watch` loops and scripted state machines cannot trust `state` to terminate.

The shape `jk build trigger` uses for build parameters (`-p KEY=VALUE`, with `-p KEY=@file` for file-loaded values) is well-validated in v0.1 e2e tests. Reusing it for input submission is the lowest-surprise design.

Jenkins exposes a separate `POST .../input/<id>/submit` endpoint for parameterized submissions. The payload format is a URL-encoded form with a single `json` field whose value is a JSON document of shape `{"parameter":[{"name":"X","value":"Y"}, ...]}`. This was confirmed during the v0.1 wfapi spike (see `docs/wfapi-spike-findings.md`).

## Goals / Non-Goals

**Goals:**
- Let users submit choice/boolean/string/password values for paused input steps via the existing `-p KEY=VALUE` flag shape.
- Validate user-supplied parameters against `pendingInput.parameters` **before** any HTTP call, so the user gets an actionable local error instead of a generic Jenkins HTTP 400.
- Keep the existing v0.1 zero-parameter write path bit-for-bit unchanged (same `proceedEmpty` endpoint, same exit code, same output).
- Promote `pendingInput.parameters` from `experimental` to `stable` once tested against ≥2 parameter types.
- Make `jk build status`'s `pendingInput` block carry the real `id`/`message`/`parameters` for live paused builds, and stop reporting `state: PENDING_INPUT` for finished builds.

**Non-Goals:**
- Interactive TTY prompts ("which environment? [1] staging [2] prod"). `jk` is scriptable-first; interactive mode is a separate v0.3+ design discussion.
- Sourcing parameter values from environment variables (e.g. `-p ENV=$ENV_VAR`). Shells already do this expansion before `jk` sees the flag; no need to duplicate.
- Submitting `input` responses to triggered inputs **before** they appear (no polling-then-submit single command). Users compose `--watch` + `build input` themselves.
- Supporting the legacy `submit` endpoint shape from pre-Pipeline Jenkins (free-form jobs). Out of scope: this change targets Pipeline `input` step only.
- Changing the `abort` semantics. `-p` on `abort` warns and is ignored.

## Decisions

### Decision 1: Reuse `jk build trigger`'s `-p KEY=VALUE` flag, not a new flag name

**Choice:** Add `-p KEY=VALUE` (repeatable) to `jk build input proceed`. Share the parsing helper currently in `internal/cli/build.go` between `trigger` and `input`.

**Alternatives considered:**
- `--input-param KEY=VALUE` (more explicit, but verbose and inconsistent with `trigger`).
- `--param KEY=VALUE` (no shorthand; `jk build trigger` already uses `-p` so users would have to remember which command takes which spelling).

**Why:** Minimum cognitive load. The mental model "`-p` carries parameters into a Jenkins request, regardless of which request" is internally consistent and matches `kubectl run --env`, `git commit -m`, etc. The `@file` semantics carry over for free.

### Decision 2: Endpoint selection rule — `submit` when needed, `proceedEmpty` when sufficient

**Choice:**
- 0 `-p` flags AND 0 declared parameters → `proceedEmpty` (v0.1 path, unchanged)
- 0 `-p` flags AND ≥1 declared parameter with all defaults present → `submit` with all defaults
- 0 `-p` flags AND ≥1 declared parameter missing a default → **fail locally** with exit 10 before any HTTP call
- ≥1 `-p` flag → `submit` always (even if it could be expressed as `proceedEmpty`)

**Alternatives considered:**
- Always use `submit`. Cleaner code path, but changes the wire format for the most common v0.1 case (zero-parameter inputs), increasing the blast radius of this change unnecessarily.
- Always require `-p` when input has parameters (never fall back to defaults). Surface-area honest, but breaks the "all-defaults click-through" workflow that some teams script.

**Why:** Preserve v0.1 behavior for zero-parameter inputs verbatim; let "all defaults" Just Work; fail fast (no HTTP roundtrip) on the cases where the user's intent cannot be satisfied. The fail-fast rule is the most important: a generic Jenkins HTTP 400 ("Required parameter X missing") is much worse UX than `jk` saying `ENV has no default; rerun with -p ENV=<staging|prod>`.

### Decision 3: Validate `-p` values against `pendingInput.parameters` before submitting

**Choice:** Before calling `submit`, walk the user's `-p` map and the input's parameter declarations:
- Reject unknown keys (list the valid keys).
- For `CHOICE`, reject values not in `choices` (list the valid choices).
- For `BOOLEAN`, reject values that don't parse to `true`/`false` (case-insensitive).
- For `STRING`/`TEXT`/`PASSWORD`, accept any string.
- For `UNKNOWN` (plugin-specific types `jk` doesn't recognize), accept any string and let Jenkins validate.

**Alternatives considered:**
- Server-side validation only (just POST and surface Jenkins's error). Saves code, but Jenkins's error messages for input parameters are poor: HTTP 400 with HTML body, no structured indication of which parameter failed.
- Strict validation of all types including `UNKNOWN`. Brittle — plugins evolve, and `jk`'s job is to not break when they do.

**Why:** Local-first validation is faster (no HTTP), more actionable (`jk` knows the valid set), and aligns with the v0.1 `jk build trigger` philosophy where unknown parameter keys are caught client-side before submission.

### Decision 4: Encode the payload as `application/x-www-form-urlencoded` with a single `json=` field

**Choice:** The submitted body MUST be `Content-Type: application/x-www-form-urlencoded` and the body MUST contain exactly one field, `json`, whose URL-encoded value is the JSON string `{"parameter":[{"name":"X","value":"Y"}, ...]}`. This matches what the Jenkins web UI sends and what `workflow-input-step` plugin expects.

**Alternatives considered:**
- `Content-Type: application/json` with the JSON document as the body. Jenkins ignores it for the `submit` endpoint — confirmed during spike.
- Multipart form. Unnecessary; no file uploads.

**Why:** Wire-format compatibility with what Jenkins's `input` step handler actually parses. Spike-validated.

### Decision 5: `-p` on `abort` warns instead of erroring

**Choice:** If the user passes `-p` to `abort`, print a single line to stderr (`warning: -p flags are ignored for 'abort'`) and proceed with the abort. Exit code is `0` on success of the abort itself.

**Alternatives considered:**
- Hard error with exit 10. Defensible — flags shouldn't silently disappear. But scripts that do `jk build input $URL $ACTION -p ENV=prod` (parameterizing `$ACTION`) would break.
- Silent ignore. Worse — invisible bugs.

**Why:** Warn-don't-error matches Unix convention for "I did what you asked but FYI." Scripts can `2>/dev/null` if they don't want the noise.

### Decision 6: `build status` populates `pendingInput` from `/wfapi/pendingInputActions`, not from core `actions[]`

**Choice:** Reshape the read path in `runBuildStatus` so the pending-input data flows from the same source `build input` already uses:

1. Fetch `GET /<n>/api/json` (existing call). Decode `building`, `result`, `actions[]._class` only — drop the attempt to pull `id`/`message`/`ok`/`parameters` from `actions[]` entirely, since those fields are never populated there.
2. If `building == true` AND `actions[]` contains an `InputAction` marker, fetch `GET /<n>/wfapi/pendingInputActions`. If the response is a non-empty array, populate `pendingInput` from the first entry (mapped via the existing `MapPendingInput` helper). If the array is empty (race: the marker is there but the input has already been submitted), emit no `pendingInput` block.
3. If `building == false`, never fetch `/wfapi/pendingInputActions` and never emit `pendingInput`, even if `actions[]` carries a historical `InputAction` marker.

**Alternatives considered:**
- Always fetch `/wfapi/pendingInputActions` for every `build status` call. Doubles the request count for every status read, even for builds that have no input step at all.
- Stop calling `/api/json` and fetch only from wfapi. Loses `result`, `timestamp`, `duration`, `queueId` — those are only on core.
- Have `MapBuildStatus` itself make the HTTP call. Violates the layering: the mapper is currently a pure function over bytes; introducing an HTTP dependency there is a regression in testability.

**Why:** The condition `building == true && has InputAction marker` is the precise minimum signal that "there is something to fetch from wfapi." It avoids the extra request for the common case (finished builds, builds without input steps) and limits the change to the CLI orchestration layer, keeping `MapBuildStatus` pure.

### Decision 7: State derivation: `DONE` wins over any `InputAction` marker

**Choice:** In `MapBuildStatus`, derive `state` as:

```
if building == false      → DONE
else if pendingInput set  → PENDING_INPUT
else                      → RUNNING
```

That is, `building == false` is checked first and short-circuits. `PENDING_INPUT` requires `building == true` AND a non-nil `pendingInput` (which, given Decision 6, is only ever set when wfapi confirms a live pending input).

**Alternatives considered:**
- Keep the current order (pending wins over building). The current behavior — finished builds reporting `PENDING_INPUT` — is exactly the bug we are fixing.
- Add a fourth state `STALE_INPUT` for the historical case. Adds vocabulary for a condition users do not need to act on.

**Why:** A build that is not building is not pending anything. The `actions[]` marker is a historical artifact of the input ever having been raised; it is not a current condition. The schema's contract for `state` is "what is this build doing right now," and the right answer for a finished build is always `DONE`.

## Risks / Trade-offs

- **[Risk]** Jenkins changes the `submit` wire format in a future plugin version → **Mitigation:** the e2e test exercises the actual harness, which will catch wire-format drift on each CI run. We also document the exact payload shape in `docs/wfapi-spike-findings.md`.
- **[Risk]** Validation false-positives — a future Jenkins parameter type we mis-classify as `BOOLEAN` rejects a legitimate value → **Mitigation:** the `UNKNOWN` escape hatch already exists; if a real type starts hitting this, demote validation to `UNKNOWN` for that class.
- **[Risk]** Promoting `pendingInput.parameters` to `stable` locks the shape → **Mitigation:** the shape has been validated against the harness for v0.1; the fields (`name`, `type`, `choices`, `defaultValue`, `description`) are the obvious minimal set. Promoting is conservative.
- **[Risk]** The extra `/wfapi/pendingInputActions` fetch in `build status` (Decision 6) fails (network blip, plugin missing) → **Mitigation:** treat failures as "no pending input known" — log to `--debug` only, emit no `pendingInput` block, derive state from `building` alone. Never fail the whole `build status` call because of a wfapi enrichment failure.
- **[Risk]** Decision 7 changes the `state` value for finished builds that previously reported `PENDING_INPUT` → this is the bug fix; scripts relying on the buggy value will break. **Mitigation:** call this out in the v0.2 changelog under "Behavior changes," not "Breaking changes" (since the previous value was incorrect, not contractual).
- **[Trade-off]** Users with all-defaults inputs now send a `submit` request when they previously could have sent `proceedEmpty` — except we keep the `proceedEmpty` shortcut when there are zero declared parameters, which is the common v0.1 case. The new `submit` traffic is for inputs that actually have parameters, where v0.1 was already failing.
- **[Trade-off]** Larger surface area in `jk build input` — moves from "two literal verbs, one flag" to "two verbs, two flags, validated parameter map." The added complexity is bounded by Decision 1 (reuse `trigger`'s parsing) and Decision 3 (a single 30-line validator).
- **[Trade-off]** `build status` for paused builds now makes two HTTP requests instead of one (Decision 6). Acceptable: paused builds are rare relative to all status calls, the trigger condition is precise, and the alternative — incorrect output — is worse.

## Migration Plan

- No breaking changes on the write path. Existing zero-parameter `jk build input proceed` calls continue to hit `proceedEmpty` unchanged.
- Users with all-defaults inputs whose previous `jk build input proceed` calls **failed** (because `proceedEmpty` rejected the parameters) will start succeeding after this change ships — this is a bug fix, not a behavior change for any passing call.
- Read-path behavior changes (Decisions 6 and 7) are bug fixes; document under "Behavior changes" in the v0.2 release notes with before/after examples for finished builds and live paused builds.
- v0.2 release notes MUST document the new `-p` flag with an example.
- Roll-forward only — no schema migration needed.

## Open Questions

(none — all decisions resolved by spike findings or v0.1 precedent)
