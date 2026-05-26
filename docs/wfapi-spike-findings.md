# Spike findings: Jenkins wfapi deviations from initial assumptions

**Date**: 2026-05-26  
**Jenkins version**: `jenkins/jenkins:lts-jdk21` (used in e2e harness)  
**Plugins**: workflow-aggregator, pipeline-rest-api, pipeline-stage-view, configuration-as-code, job-dsl  
**Tasks covered**: 15.5, 1.1, 1.2, 1.3

---

## 1. wfapi/describe — stage hierarchy is flat, not nested

**Assumption** (pre-spike): parallel stages would appear as children of their
parent stage node under a `parallel` key, mirroring the `stage { parallel { ... } }`
Declarative syntax.

**Reality**: `wfapi/describe` returns all stages — including parallel branches —
as a **flat array** at `stages[]`. The parent stage (`Test`) and its parallel
children (`Unit`, `Integration`, `Lint`) are siblings in the same array. No
nesting exists in the response.

```json
// GET /job/team/job/parallel/1/wfapi/describe
{
  "stages": [
    { "id": "6",  "name": "Build"       },
    { "id": "11", "name": "Test"        },   // parent — appears BEFORE children
    { "id": "19", "name": "Unit"        },   // parallel branch
    { "id": "21", "name": "Integration" },   // parallel branch
    { "id": "23", "name": "Lint"        },   // parallel branch
    { "id": "40", "name": "Deploy"      }
  ]
}
```

**Impact**: `schema.MapBuildStages` already treats `stages[]` as a flat list and
assembles display order accordingly — the test `Test_E2E_BuildStages_ParallelLayout`
passes without changes.

**`stageNode.Parallel` field**: the original `stageNode` struct in `internal/cli/build.go`
had a `Parallel []stageNode` field anticipating nesting. Since the real API is flat,
this field is always empty. The `findStageID` depth-first walk is still correct
(it recurses into `Parallel`, which is empty, and falls through to siblings) but
the `Parallel` field is dead weight. It is kept for now as a forward-compat placeholder;
a future spike against Jenkins views (`/wfapi/describe?depth=…`) may revisit.

---

## 2. Stage log lives on child step nodes, not the stage node

**Assumption** (pre-spike): `GET .../execution/node/<stageID>/wfapi/log` returns
the stage's log text.

**Reality**: that endpoint returns `{"length":0, "hasMore":false}` for every
stage node. The actual log text lives on the **child step nodes** (`stageFlowNodes`)
returned by the stage node's own `wfapi/describe` response:

```json
// GET .../execution/node/19/wfapi/describe  (stage "Unit")
{
  "id": "19",
  "name": "Unit",
  "stageFlowNodes": [
    { "id": "24", "name": "Print Message", "status": "SUCCESS" }
  ]
}

// GET .../execution/node/24/wfapi/log  (child step node)
{
  "nodeId": "24",
  "nodeStatus": "SUCCESS",
  "length": 11,
  "text": "unit tests\n"
}
```

**Fix applied**: `runBuildStageLog` in `internal/cli/build.go` now:
1. Fetches the stage node's `wfapi/describe` via `Client.GetNodeDescribe` to enumerate `stageFlowNodes`.
2. Fetches and concatenates the log text from each child node.
3. Falls back to the stage node's own `wfapi/log` when `stageFlowNodes` is empty
   (preserves compatibility with potential older Jenkins where the stage node itself
   carries the text).

New client method: `Client.GetNodeDescribe(ctx, ref, flowNodeID)`.

---

## 3. pendingInputActions shape

```json
// GET .../wfapi/pendingInputActions  (while build is blocked at input step)
[
  {
    "id": "Spike-input",
    "proceedText": "Go",
    "message": "Proceed?",
    "inputs": [],
    "proceedUrl": "/job/input/lastBuild/wfapi/inputSubmit?inputId=Spike-input",
    "abortUrl": "/job/input/lastBuild/input/Spike-input/abort",
    "redirectApprovalUrl": "/job/input/lastBuild/input/"
  }
]
```

Key observations:
- `id` is the capitalised form of the Jenkinsfile `id:` parameter (`"Spike-input"` from `id: 'spike-input'`).
- `inputs: []` when the input step has no parameters.
- Returns `[]` (empty array, not 404) when the build is not waiting at an input step.

**proceed endpoint**: `POST .../input/<id>/proceedEmpty` → HTTP 200. This is the correct
path for proceeding with no parameters. `POST .../wfapi/inputSubmit?inputId=<id>` requires
a form body with `json=<params-json>` and returns 500 when the body is malformed.

**abort endpoint**: `POST .../input/<id>/abort` → HTTP 200, build result becomes `ABORTED`.

**Implementation status**: `Client.SubmitInput` already uses `proceedEmpty` / `abort` —
matches exactly. **No changes required.**

---

## 4. Queue item shape (spike 1.3)

```json
// GET /queue/item/<id>/api/json  (while still queued)
{
  "_class": "hudson.model.Queue$WaitingItem",
  "id": 23,
  "buildable": false,
  "why": "Finished waiting",
  "task": { "name": "hello", "url": "http://localhost:18080/job/hello/" }
}

// GET /queue/item/<id>/api/json  (after build has started)
{
  "_class": "hudson.model.Queue$LeftItem",
  "id": 23,
  "cancelled": false,
  "why": null,
  "executable": {
    "_class": "org.jenkinsci.plugins.workflow.job.WorkflowRun",
    "number": 6,
    "url": "http://localhost:18080/job/hello/6/"
  }
}
```

Key observations:
- `executable` only appears in `LeftItem` (after the build has started execution).
- `why: null` + `executable` present = build is running. `cancelled: true` + no `executable` = item was cancelled before starting.
- `_class` transitions: `WaitingItem` → `BlockedItem` → `BuildableItem` → `LeftItem`.

**Implementation status**: `Client.ResolveQueueItem` polls until `executable` is
present. **No changes required.**

---

## 5. Error printing was silent (bug, now fixed)

`cmd/jk/main.go` had `SilenceErrors: true` on the cobra root but never printed
errors itself. All JKError messages (including "stage not found" with its
Available-stages suggestion) were swallowed; exit code 10 was correct but
stderr was empty.

**Fix applied**: `main.go` now calls `printError(err)` before `os.Exit`, rendering
`JKError.Message` + `JKError.Suggestion` to stderr. `BuildResultExitError` is
deliberately not printed (the --watch output already reported the result).

---

## Summary table

| Finding | Assumed | Reality | Fix needed |
|---------|---------|---------|-----------|
| wfapi stage hierarchy | nested `parallel[]` | flat `stages[]` | none (schema already flat) |
| Stage log location | `node/<id>/wfapi/log` | child `stageFlowNodes[].id`'s log | **fixed** (`GetNodeDescribe` + child iteration) |
| pendingInputActions | unknown | flat array, `[]` when not waiting | none (matches existing impl) |
| proceed endpoint | `wfapi/inputSubmit` | `input/<id>/proceedEmpty` | none (existing impl correct) |
| abort endpoint | unknown | `input/<id>/abort` → 200 | none (existing impl correct) |
| Queue executable field | unknown | `LeftItem.executable.url` | none (existing impl correct) |
| Error printing | cobra handles it | silenced — main never printed | **fixed** (`printError` in main) |
