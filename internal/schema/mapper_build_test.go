package schema_test

// Mapper tests for build-level responses. Each fixture is a minimal
// but realistic Jenkins response captured from documented sources
// (Jenkins core api/json + pipeline-stage-view-plugin wfapi README).
// When spike 1.1/1.2 produces real fixtures, those will replace or
// augment the inline fixtures below; the tests will then either still
// pass (mapper is correct) or fail with a clear signal that the
// mapper needs updating.
//
// OpenSpec mapping:
//   - tasks 12.4 -> Test_MapBuildStatus_*
//   - tasks 12.5 -> Test_MapBuildStages_*
//   - tasks 12.6 -> Test_MapPendingInput_*
//   - tasks 12.7 -> covered transitively (enum normalization is
//                    asserted within each higher-level test)
//
// Rationale notes inline with each test explain why the assertion
// matters per docs/schema.md §3.7-§3.9 so a reviewer can verify the
// mapping intent without re-reading the spec.

import (
	"testing"

	"github.com/addozhang/jk/internal/schema"
)

// ---------------------------------------------------------------------------
// 12.4 MapBuildStatus
// ---------------------------------------------------------------------------

// A build that's still running: building=true, result=null. Asserts:
//   - State=RUNNING (derived from building + null result);
//   - Result is *nil (preserved through *BuildResult);
//   - ProgressPercent computed from durationMs / estimatedDurationMs;
//   - PendingInput is nil (not paused at an input step);
//   - TimestampUtc converted from Jenkins millisecond epoch to RFC 3339 UTC.
func Test_MapBuildStatus_Running(t *testing.T) {
	// timestamp = 1700000000000 ms = 2023-11-14T22:13:20Z
	raw := []byte(`{
		"number":42,
		"url":"http://jenkins.example/job/svc/42/",
		"queueId":17,
		"result":null,
		"building":true,
		"timestamp":1700000000000,
		"duration":0,
		"estimatedDuration":60000,
		"actions":[
			{"_class":"hudson.model.CauseAction"},
			{}
		]
	}`)
	// duration=0 (Jenkins reports 0 for in-progress builds in the
	// api/json `duration` field; the real elapsed time only comes via
	// wfapi or by computing now-timestamp). For v0.1 we trust Jenkins's
	// number: 0 here means progressPercent=0.

	got, err := schema.MapBuildStatus(raw)
	if err != nil {
		t.Fatalf("MapBuildStatus: %v", err)
	}

	if got.BuildNumber != 42 {
		t.Errorf("BuildNumber=%d, want 42", got.BuildNumber)
	}
	if got.BuildURL != "http://jenkins.example/job/svc/42/" {
		t.Errorf("BuildURL=%q", got.BuildURL)
	}
	if got.QueueID == nil || *got.QueueID != 17 {
		t.Errorf("QueueID=%v, want *17", got.QueueID)
	}
	if got.Result != nil {
		t.Errorf("Result=%v, want nil while running", *got.Result)
	}
	if got.State != schema.BuildStateRunning {
		t.Errorf("State=%q, want RUNNING", got.State)
	}
	if !got.Building {
		t.Error("Building=false, want true")
	}
	if got.TimestampUtc != "2023-11-14T22:13:20Z" {
		t.Errorf("TimestampUtc=%q, want 2023-11-14T22:13:20Z", got.TimestampUtc)
	}
	if got.DurationMs != 0 {
		t.Errorf("DurationMs=%d, want 0", got.DurationMs)
	}
	if got.EstimatedDurationMs == nil || *got.EstimatedDurationMs != 60000 {
		t.Errorf("EstimatedDurationMs=%v, want *60000", got.EstimatedDurationMs)
	}
	if got.ProgressPercent != 0 {
		t.Errorf("ProgressPercent=%d, want 0", got.ProgressPercent)
	}
	if got.PendingInput != nil {
		t.Errorf("PendingInput=%+v, want nil for non-paused build", got.PendingInput)
	}
}

// A completed build: building=false, result=SUCCESS. Asserts:
//   - State=DONE (terminal);
//   - Result points at SUCCESS;
//   - ProgressPercent forced to 100 once DONE (per docs/schema.md §3.7);
//   - DurationMs is the final duration (Jenkins reports it once done).
func Test_MapBuildStatus_DoneSuccess(t *testing.T) {
	raw := []byte(`{
		"number":7,
		"url":"http://jenkins.example/job/svc/7/",
		"queueId":3,
		"result":"SUCCESS",
		"building":false,
		"timestamp":1700000000000,
		"duration":45000,
		"estimatedDuration":60000
	}`)

	got, err := schema.MapBuildStatus(raw)
	if err != nil {
		t.Fatalf("MapBuildStatus: %v", err)
	}

	if got.State != schema.BuildStateDone {
		t.Errorf("State=%q, want DONE", got.State)
	}
	if got.Result == nil || *got.Result != schema.BuildResultSuccess {
		t.Errorf("Result=%v, want *SUCCESS", got.Result)
	}
	if got.DurationMs != 45000 {
		t.Errorf("DurationMs=%d, want 45000", got.DurationMs)
	}
	if got.ProgressPercent != 100 {
		t.Errorf("ProgressPercent=%d, want 100 once DONE", got.ProgressPercent)
	}
	if got.Building {
		t.Error("Building=true, want false")
	}
}

// ProgressPercent capping: durationMs > estimatedDurationMs (Jenkins's
// estimate was too low). Per docs/schema.md §3.7 the value is clamped
// to 100 via min(100, …). Build is still running.
func Test_MapBuildStatus_ProgressClampedTo100(t *testing.T) {
	raw := []byte(`{
		"number":1,
		"url":"http://x/1/",
		"result":null,
		"building":true,
		"timestamp":1700000000000,
		"duration":90000,
		"estimatedDuration":60000
	}`)

	got, err := schema.MapBuildStatus(raw)
	if err != nil {
		t.Fatalf("MapBuildStatus: %v", err)
	}
	if got.ProgressPercent != 100 {
		t.Errorf("ProgressPercent=%d, want 100 (clamped)", got.ProgressPercent)
	}
	if got.State != schema.BuildStateRunning {
		t.Errorf("State=%q, want RUNNING", got.State)
	}
}

// estimatedDuration absent or non-positive: ProgressPercent stays 0
// for running builds (no division by zero). Per docs/schema.md §3.7:
// "estimatedDurationMs is null if unavailable".
func Test_MapBuildStatus_ProgressZeroWhenEstimateMissing(t *testing.T) {
	raw := []byte(`{
		"number":1,
		"url":"http://x/1/",
		"result":null,
		"building":true,
		"timestamp":1700000000000,
		"duration":30000,
		"estimatedDuration":-1
	}`)
	// Jenkins emits -1 when it has no historical baseline. Treat as
	// "unavailable" -> EstimatedDurationMs nil, ProgressPercent 0.

	got, err := schema.MapBuildStatus(raw)
	if err != nil {
		t.Fatalf("MapBuildStatus: %v", err)
	}
	if got.EstimatedDurationMs != nil {
		t.Errorf("EstimatedDurationMs=%v, want nil when Jenkins reports -1", got.EstimatedDurationMs)
	}
	if got.ProgressPercent != 0 {
		t.Errorf("ProgressPercent=%d, want 0 when estimate unavailable", got.ProgressPercent)
	}
}

// Pending-input detection: when Jenkins reports an in-progress build
// with an InputAction marker in `actions`, State must be PENDING_INPUT
// (NOT plain RUNNING). Per docs/schema.md §3.7 and §4: "Paused at a
// Pipeline `input` step awaiting `jk build input`".
//
// Realistic fixture: core /api/json exposes the InputAction with only
// the `_class` discriminator — the id/message/ok/parameters fields are
// not populated there. They live on /wfapi/pendingInputActions and are
// mapped separately by MapPendingInput. So this mapper only emits the
// state derivation and leaves PendingInput nil for the CLI layer to
// enrich. (See openspec change add-input-parameter-submission §6.)
func Test_MapBuildStatus_PendingInputMarkerOnly(t *testing.T) {
	raw := []byte(`{
		"number":9,
		"url":"http://x/9/",
		"result":null,
		"building":true,
		"timestamp":1700000000000,
		"duration":1000,
		"estimatedDuration":60000,
		"actions":[
			{"_class":"org.jenkinsci.plugins.workflow.support.steps.input.InputAction"}
		]
	}`)

	got, err := schema.MapBuildStatus(raw)
	if err != nil {
		t.Fatalf("MapBuildStatus: %v", err)
	}
	if got.State != schema.BuildStatePendingInput {
		t.Errorf("State=%q, want PENDING_INPUT", got.State)
	}
	// PendingInput is left nil here — populating it requires a
	// /wfapi/pendingInputActions fetch, which lives in the CLI layer.
	if got.PendingInput != nil {
		t.Errorf("PendingInput=%+v, want nil (mapper does not enrich from wfapi)", got.PendingInput)
	}
}

// Regression for the v0.1 state-derivation bug: a finished build whose
// historical InputAction marker is still in actions[] must report DONE,
// not PENDING_INPUT. Per openspec change add-input-parameter-submission
// Decision 7: building==false short-circuits state derivation.
func Test_MapBuildStatus_DoneWinsOverInputMarker(t *testing.T) {
	raw := []byte(`{
		"number":9,
		"url":"http://x/9/",
		"result":"SUCCESS",
		"building":false,
		"timestamp":1700000000000,
		"duration":65000,
		"estimatedDuration":60000,
		"actions":[
			{"_class":"org.jenkinsci.plugins.workflow.support.steps.input.InputAction"}
		]
	}`)

	got, err := schema.MapBuildStatus(raw)
	if err != nil {
		t.Fatalf("MapBuildStatus: %v", err)
	}
	if got.State != schema.BuildStateDone {
		t.Errorf("State=%q, want DONE (building==false wins over stale InputAction marker)", got.State)
	}
	if got.Result == nil || *got.Result != schema.BuildResultSuccess {
		t.Errorf("Result=%v, want *SUCCESS", got.Result)
	}
	if got.PendingInput != nil {
		t.Errorf("PendingInput=%+v, want nil for finished build", got.PendingInput)
	}
}

// HasPendingInputMarker exposes the actions[] presence-bit so the CLI
// layer can decide whether to make the wfapi enrichment call without
// re-parsing the core JSON.
func Test_HasPendingInputMarker(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{
			name: "marker present",
			raw:  `{"actions":[{"_class":"org.jenkinsci.plugins.workflow.support.steps.input.InputAction"}]}`,
			want: true,
		},
		{
			name: "no actions",
			raw:  `{"actions":[]}`,
			want: false,
		},
		{
			name: "other actions only",
			raw:  `{"actions":[{"_class":"hudson.model.CauseAction"},{}]}`,
			want: false,
		},
		{
			name: "actions field absent",
			raw:  `{"number":1}`,
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := schema.HasPendingInputMarker([]byte(c.raw))
			if err != nil {
				t.Fatalf("HasPendingInputMarker: %v", err)
			}
			if got != c.want {
				t.Errorf("got=%v, want=%v", got, c.want)
			}
		})
	}
}

// queueId may be absent (older Jenkins or builds without queue info);
// it must marshal as null, i.e. *int nil. Asserts the *int handling.
func Test_MapBuildStatus_NullQueueID(t *testing.T) {
	raw := []byte(`{
		"number":1,
		"url":"http://x/1/",
		"result":"SUCCESS",
		"building":false,
		"timestamp":1700000000000,
		"duration":10
	}`)
	got, err := schema.MapBuildStatus(raw)
	if err != nil {
		t.Fatalf("MapBuildStatus: %v", err)
	}
	if got.QueueID != nil {
		t.Errorf("QueueID=%v, want nil when absent in source", got.QueueID)
	}
}

// ---------------------------------------------------------------------------
// 12.5 MapBuildStages
// ---------------------------------------------------------------------------

// A simple sequential pipeline with three stages, all completed.
// Asserts:
//   - Stages preserved in declaration order;
//   - DisplayName == Name when no duplicates;
//   - Parallel is nil for sequential stages (serializes as JSON null);
//   - StartTimeUtc converted from ms epoch; DurationMs preserved.
//
// Fixture mirrors pipeline-stage-view-plugin's wfapi describe shape:
// each stage has id, name, status (Jenkins uses verbs like "SUCCESS"),
// startTimeMillis, durationMillis.
func Test_MapBuildStages_Sequential(t *testing.T) {
	raw := []byte(`{
		"stages":[
			{"id":"5","name":"Checkout","status":"SUCCESS",
			 "startTimeMillis":1700000000000,"durationMillis":1500},
			{"id":"10","name":"Build","status":"SUCCESS",
			 "startTimeMillis":1700000001500,"durationMillis":30000},
			{"id":"20","name":"Test","status":"FAILED",
			 "startTimeMillis":1700000031500,"durationMillis":12000}
		]
	}`)

	got, err := schema.MapBuildStages(raw)
	if err != nil {
		t.Fatalf("MapBuildStages: %v", err)
	}

	if len(got.Stages) != 3 {
		t.Fatalf("len(Stages)=%d, want 3", len(got.Stages))
	}
	names := []string{got.Stages[0].Name, got.Stages[1].Name, got.Stages[2].Name}
	want := []string{"Checkout", "Build", "Test"}
	for i := range names {
		if names[i] != want[i] {
			t.Errorf("Stages[%d].Name=%q, want %q", i, names[i], want[i])
		}
		if got.Stages[i].DisplayName != want[i] {
			t.Errorf("Stages[%d].DisplayName=%q, want %q (no duplicates)", i, got.Stages[i].DisplayName, want[i])
		}
		if got.Stages[i].Parallel != nil {
			t.Errorf("Stages[%d].Parallel=%v, want nil for sequential", i, got.Stages[i].Parallel)
		}
	}
	// "FAILED" in wfapi is the verb form of BuildResult FAILURE. Per
	// the StageStatus enum the canonical value is FAILURE.
	if got.Stages[2].Status != schema.StageStatusFailure {
		t.Errorf("Stages[2].Status=%q, want FAILURE (normalized from FAILED)", got.Stages[2].Status)
	}
	// First stage startTimeUtc: 1700000000000 ms == 2023-11-14T22:13:20Z.
	if got.Stages[0].StartTimeUtc == nil || *got.Stages[0].StartTimeUtc != "2023-11-14T22:13:20Z" {
		t.Errorf("Stages[0].StartTimeUtc=%v, want *2023-11-14T22:13:20Z", got.Stages[0].StartTimeUtc)
	}
	if got.Stages[1].DurationMs == nil || *got.Stages[1].DurationMs != 30000 {
		t.Errorf("Stages[1].DurationMs=%v, want *30000", got.Stages[1].DurationMs)
	}
}

// A running stage: durationMillis is 0 (or absent) and status RUNNING.
// Per docs/schema.md §3.8 DurationMs is null until the stage finishes.
// We treat status=="IN_PROGRESS" (wfapi verb) as RUNNING.
func Test_MapBuildStages_RunningStageNullDuration(t *testing.T) {
	raw := []byte(`{
		"stages":[
			{"id":"3","name":"Deploy","status":"IN_PROGRESS",
			 "startTimeMillis":1700000000000,"durationMillis":0}
		]
	}`)
	got, err := schema.MapBuildStages(raw)
	if err != nil {
		t.Fatalf("MapBuildStages: %v", err)
	}
	if got.Stages[0].Status != schema.StageStatusRunning {
		t.Errorf("Status=%q, want RUNNING (normalized from IN_PROGRESS)", got.Stages[0].Status)
	}
	if got.Stages[0].DurationMs != nil {
		t.Errorf("DurationMs=%v, want nil while running", got.Stages[0].DurationMs)
	}
}

// A not-yet-started stage: startTimeMillis missing/0. StartTimeUtc
// must be nil per docs/schema.md §3.8 ("null if the stage has not
// started").
func Test_MapBuildStages_NotStartedStageNullTime(t *testing.T) {
	raw := []byte(`{
		"stages":[
			{"id":"1","name":"Pending","status":"NOT_EXECUTED","startTimeMillis":0,"durationMillis":0}
		]
	}`)
	got, err := schema.MapBuildStages(raw)
	if err != nil {
		t.Fatalf("MapBuildStages: %v", err)
	}
	if got.Stages[0].StartTimeUtc != nil {
		t.Errorf("StartTimeUtc=%v, want nil for not-started stage", got.Stages[0].StartTimeUtc)
	}
	if got.Stages[0].Status != schema.StageStatusNotExecuted {
		t.Errorf("Status=%q, want NOT_EXECUTED", got.Stages[0].Status)
	}
}

// Parallel stages: a parent stage with an `execNode` or `stageFlowNodes`
// shape may not be enough; the canonical wfapi representation is a
// parent stage with a `parallel` array containing branch stages.
// Asserts:
//   - Parent stage carries a non-nil Parallel slice with branch stages;
//   - Each branch stage has its own Name/Status/DisplayName.
func Test_MapBuildStages_Parallel(t *testing.T) {
	raw := []byte(`{
		"stages":[
			{"id":"7","name":"Test","status":"SUCCESS",
			 "startTimeMillis":1700000000000,"durationMillis":5000,
			 "parallel":[
				{"id":"8","name":"unit","status":"SUCCESS",
				 "startTimeMillis":1700000000000,"durationMillis":3000},
				{"id":"9","name":"integration","status":"SUCCESS",
				 "startTimeMillis":1700000000000,"durationMillis":5000}
			 ]}
		]
	}`)

	got, err := schema.MapBuildStages(raw)
	if err != nil {
		t.Fatalf("MapBuildStages: %v", err)
	}
	if len(got.Stages) != 1 {
		t.Fatalf("len(Stages)=%d, want 1", len(got.Stages))
	}
	parent := got.Stages[0]
	if parent.Parallel == nil {
		t.Fatal("Parent.Parallel=nil, want populated for parallel stage")
	}
	if len(parent.Parallel) != 2 {
		t.Fatalf("len(Parallel)=%d, want 2", len(parent.Parallel))
	}
	if parent.Parallel[0].Name != "unit" || parent.Parallel[1].Name != "integration" {
		t.Errorf("Parallel names=%q,%q; want unit,integration",
			parent.Parallel[0].Name, parent.Parallel[1].Name)
	}
	// Branch stages with unique names: DisplayName == Name.
	if parent.Parallel[0].DisplayName != "unit" {
		t.Errorf("Parallel[0].DisplayName=%q, want unit", parent.Parallel[0].DisplayName)
	}
}

// Duplicate stage names within the same run must be disambiguated via
// DisplayName per docs/schema.md §3.8: "equals name unless duplicated;
// then suffix #1, #2, …". The Name field stays the original verbatim.
//
// Disambiguation scope is per-run (i.e. across the entire Stages tree,
// including parallel branches). For v0.1 we limit scope to the
// top-level Stages slice to avoid cross-branch suffix collisions; the
// nested-parallel duplicate case is exercised in a separate test.
func Test_MapBuildStages_DuplicateNamesAtTopLevel(t *testing.T) {
	raw := []byte(`{
		"stages":[
			{"id":"1","name":"Deploy","status":"SUCCESS",
			 "startTimeMillis":1700000000000,"durationMillis":1000},
			{"id":"2","name":"Deploy","status":"SUCCESS",
			 "startTimeMillis":1700000001000,"durationMillis":1000},
			{"id":"3","name":"Cleanup","status":"SUCCESS",
			 "startTimeMillis":1700000002000,"durationMillis":1000},
			{"id":"4","name":"Deploy","status":"SUCCESS",
			 "startTimeMillis":1700000003000,"durationMillis":1000}
		]
	}`)

	got, err := schema.MapBuildStages(raw)
	if err != nil {
		t.Fatalf("MapBuildStages: %v", err)
	}
	if len(got.Stages) != 4 {
		t.Fatalf("len(Stages)=%d, want 4", len(got.Stages))
	}
	// All three Deploy stages get suffixed #1, #2, #3 in
	// declaration order. If a name appears more than once, every
	// occurrence is suffixed (no ambiguity for the user). The
	// non-duplicate "Cleanup" stays unsuffixed. Name field preserved
	// verbatim throughout.
	wantDisplay := []string{"Deploy #1", "Deploy #2", "Cleanup", "Deploy #3"}
	for i, w := range wantDisplay {
		if got.Stages[i].DisplayName != w {
			t.Errorf("Stages[%d].DisplayName=%q, want %q", i, got.Stages[i].DisplayName, w)
		}
	}
	// Name field preserved verbatim.
	for i, n := range []string{"Deploy", "Deploy", "Cleanup", "Deploy"} {
		if got.Stages[i].Name != n {
			t.Errorf("Stages[%d].Name=%q, want %q", i, got.Stages[i].Name, n)
		}
	}
}

// Empty stages array still returns a non-nil slice (renders as `[]`
// per the schema contract).
func Test_MapBuildStages_EmptyIsNonNilSlice(t *testing.T) {
	raw := []byte(`{"stages":[]}`)
	got, err := schema.MapBuildStages(raw)
	if err != nil {
		t.Fatalf("MapBuildStages: %v", err)
	}
	if got.Stages == nil {
		t.Error("Stages=nil, want non-nil empty slice for [] rendering")
	}
	if len(got.Stages) != 0 {
		t.Errorf("len(Stages)=%d, want 0", len(got.Stages))
	}
}

// ---------------------------------------------------------------------------
// 12.6 MapPendingInput
// ---------------------------------------------------------------------------

// Standard wfapi pendingInputActions response: a single input step
// awaiting response. The response is an array (Jenkins may surface
// multiple paused inputs in unusual pipelines); for v0.1 we map the
// first entry, matching `jk build input`'s "one input at a time" UX.
func Test_MapPendingInput_Single(t *testing.T) {
	raw := []byte(`[
		{
			"id":"Proceed",
			"proceedText":"Deploy",
			"message":"Deploy to prod?",
			"inputs":[]
		}
	]`)

	got, err := schema.MapPendingInput(raw)
	if err != nil {
		t.Fatalf("MapPendingInput: %v", err)
	}
	if got == nil {
		t.Fatal("got=nil, want populated PendingInput")
	}
	if got.ID != "Proceed" {
		t.Errorf("ID=%q, want Proceed", got.ID)
	}
	if got.Message != "Deploy to prod?" {
		t.Errorf("Message=%q", got.Message)
	}
	// wfapi calls it `proceedText`; the schema field is `ok` per
	// docs/schema.md §3.7 PendingInput shape.
	if got.OK != "Deploy" {
		t.Errorf("OK=%q, want Deploy (from proceedText)", got.OK)
	}
	if got.Parameters == nil {
		t.Error("Parameters=nil, want [] (non-nil empty slice)")
	}
}

// STRING parameter via wfapi shape (defaultVal under definition,
// no description). Complements Test_MapPendingInput_RealWfapiShape
// which covers CHOICE + BOOLEAN.
func Test_MapPendingInput_WithParameters(t *testing.T) {
	raw := []byte(`[
		{
			"id":"Release",
			"proceedText":"Release",
			"message":"Pick version",
			"inputs":[
				{
					"type":"StringParameterDefinition",
					"name":"notes",
					"definition":{"defaultVal":"no notes"}
				}
			]
		}
	]`)

	got, err := schema.MapPendingInput(raw)
	if err != nil {
		t.Fatalf("MapPendingInput: %v", err)
	}
	if got == nil {
		t.Fatal("got=nil")
	}
	if len(got.Parameters) != 1 {
		t.Fatalf("len(Parameters)=%d, want 1", len(got.Parameters))
	}
	if got.Parameters[0].Type != schema.ParameterTypeString {
		t.Errorf("Parameters[0].Type=%q, want STRING", got.Parameters[0].Type)
	}
	if got.Parameters[0].Default != "no notes" {
		t.Errorf("Parameters[0].Default=%v, want \"no notes\"", got.Parameters[0].Default)
	}
	if got.Parameters[0].Description != nil {
		t.Errorf("Parameters[0].Description=%v, want nil", got.Parameters[0].Description)
	}
}

// Real wfapi pendingInputActions response shape (captured from a live
// Jenkins instance running the deploy-input harness pipeline). Unlike
// the /api/json `parameterDefinitions` shape, wfapi wraps choices and
// defaults inside a nested `definition` object and uses the field name
// `defaultVal` instead of `defaultParameterValue.value`. Also note
// there is no `_class` field — only `type` (simple class name).
//
// Asserts that:
//   - CHOICE params populate `Choices`,
//   - BOOLEAN params populate `Default` (true/false),
//   - `Description` flows from the top-level `description` field.
//
// This is the shape the client-side `-p` validator in
// `add-input-parameter-submission` will consume.
func Test_MapPendingInput_RealWfapiShape(t *testing.T) {
	raw := []byte(`[
		{
			"id":"Deploy",
			"proceedText":"Deploy",
			"message":"Deploy to which environment?",
			"inputs":[
				{
					"type":"ChoiceParameterDefinition",
					"name":"ENV",
					"description":"Target environment",
					"definition":{
						"defaultVal":"staging",
						"choices":["staging","prod"]
					}
				},
				{
					"type":"BooleanParameterDefinition",
					"name":"DRY_RUN",
					"description":"Skip side effects",
					"definition":{
						"defaultVal":true
					}
				}
			]
		}
	]`)

	got, err := schema.MapPendingInput(raw)
	if err != nil {
		t.Fatalf("MapPendingInput: %v", err)
	}
	if got == nil {
		t.Fatal("got=nil")
	}
	if len(got.Parameters) != 2 {
		t.Fatalf("len(Parameters)=%d, want 2", len(got.Parameters))
	}

	env := got.Parameters[0]
	if env.Name != "ENV" || env.Type != schema.ParameterTypeChoice {
		t.Errorf("ENV: name=%q type=%q, want ENV/CHOICE", env.Name, env.Type)
	}
	if env.Description == nil || *env.Description != "Target environment" {
		t.Errorf("ENV.Description=%v, want \"Target environment\"", env.Description)
	}
	if len(env.Choices) != 2 || env.Choices[0] != "staging" || env.Choices[1] != "prod" {
		t.Errorf("ENV.Choices=%v, want [staging prod]", env.Choices)
	}
	if env.Default != "staging" {
		t.Errorf("ENV.Default=%v, want \"staging\"", env.Default)
	}

	dry := got.Parameters[1]
	if dry.Name != "DRY_RUN" || dry.Type != schema.ParameterTypeBoolean {
		t.Errorf("DRY_RUN: name=%q type=%q, want DRY_RUN/BOOLEAN", dry.Name, dry.Type)
	}
	if dry.Default != true {
		t.Errorf("DRY_RUN.Default=%v (%T), want true (bool)", dry.Default, dry.Default)
	}
	if len(dry.Choices) != 0 {
		t.Errorf("DRY_RUN.Choices=%v, want nil/empty for BOOLEAN", dry.Choices)
	}
}

// Empty array (no paused inputs) returns nil + nil error: callers
// interpret nil as "no pending input" and emit `pendingInput: null`.
func Test_MapPendingInput_EmptyReturnsNil(t *testing.T) {
	got, err := schema.MapPendingInput([]byte(`[]`))
	if err != nil {
		t.Fatalf("MapPendingInput: %v", err)
	}
	if got != nil {
		t.Errorf("got=%+v, want nil for empty input list", got)
	}
}

// ---------------------------------------------------------------------------
// Sanity: malformed JSON surfaces a clear error rather than panicking.
// ---------------------------------------------------------------------------

func Test_BuildMappers_RejectMalformedJSON(t *testing.T) {
	cases := map[string]func([]byte) error{
		"BuildStatus":  func(b []byte) error { _, err := schema.MapBuildStatus(b); return err },
		"BuildStages":  func(b []byte) error { _, err := schema.MapBuildStages(b); return err },
		"PendingInput": func(b []byte) error { _, err := schema.MapPendingInput(b); return err },
		"BuildParams":  func(b []byte) error { _, err := schema.MapBuildParams(b); return err },
	}
	for name, fn := range cases {
		if err := fn([]byte(`{not json`)); err == nil {
			t.Errorf("%s: expected error on malformed JSON", name)
		}
	}
}

// extend-build-addressing §8.3: MapBuildParams walks actions[],
// filters on _class == hudson.model.ParametersAction, and copies
// name/value pairs into BuildParameter entries preserving Jenkins
// declared order.
func Test_MapBuildParams_TwoParameters(t *testing.T) {
	raw := []byte(`{
		"number":42,
		"url":"http://example/job/svc/42/",
		"actions":[
			{"_class":"hudson.model.CauseAction"},
			{"_class":"hudson.model.ParametersAction","parameters":[
				{"_class":"hudson.model.StringParameterValue","name":"ENV","value":"prod"},
				{"_class":"hudson.model.BooleanParameterValue","name":"DRY_RUN","value":false}
			]}
		]
	}`)
	got, err := schema.MapBuildParams(raw)
	if err != nil {
		t.Fatalf("MapBuildParams: %v", err)
	}
	if got.BuildNumber != 42 {
		t.Errorf("BuildNumber: got %d want 42", got.BuildNumber)
	}
	if got.BuildURL != "http://example/job/svc/42/" {
		t.Errorf("BuildURL: got %q", got.BuildURL)
	}
	if len(got.Parameters) != 2 {
		t.Fatalf("Parameters: got %d want 2", len(got.Parameters))
	}
	if got.Parameters[0].Name != "ENV" || got.Parameters[0].Value != "prod" {
		t.Errorf("Parameters[0]: %+v", got.Parameters[0])
	}
	if got.Parameters[1].Name != "DRY_RUN" || got.Parameters[1].Value != false {
		t.Errorf("Parameters[1]: %+v", got.Parameters[1])
	}
}

// No ParametersAction present -> empty slice, NOT error, NOT nil.
func Test_MapBuildParams_NoParametersAction(t *testing.T) {
	raw := []byte(`{
		"number":3,
		"url":"http://example/job/hello/3/",
		"actions":[
			{"_class":"hudson.model.CauseAction"},
			{"_class":"jenkins.metrics.impl.TimeInQueueAction"}
		]
	}`)
	got, err := schema.MapBuildParams(raw)
	if err != nil {
		t.Fatalf("MapBuildParams: %v", err)
	}
	if got.Parameters == nil {
		t.Fatal("Parameters MUST be non-nil empty slice, got nil")
	}
	if len(got.Parameters) != 0 {
		t.Errorf("Parameters: got %d want 0", len(got.Parameters))
	}
}

// Redacted password/credentials parameters surface as value:null from
// Jenkins; we MUST preserve that null faithfully (no empty-string
// coercion, no omit).
func Test_MapBuildParams_NullValuePreserved(t *testing.T) {
	raw := []byte(`{
		"number":7,
		"url":"http://example/job/svc/7/",
		"actions":[
			{"_class":"hudson.model.ParametersAction","parameters":[
				{"_class":"hudson.model.PasswordParameterValue","name":"API_TOKEN","value":null}
			]}
		]
	}`)
	got, err := schema.MapBuildParams(raw)
	if err != nil {
		t.Fatalf("MapBuildParams: %v", err)
	}
	if len(got.Parameters) != 1 || got.Parameters[0].Name != "API_TOKEN" {
		t.Fatalf("unexpected: %+v", got.Parameters)
	}
	if got.Parameters[0].Value != nil {
		t.Errorf("Value: want nil, got %#v", got.Parameters[0].Value)
	}
}

// Multiple ParametersAction entries: last-write-wins on duplicates,
// other names accumulate in encounter order.
func Test_MapBuildParams_MultipleActionsLastWriteWins(t *testing.T) {
	raw := []byte(`{
		"number":9,
		"url":"http://example/job/svc/9/",
		"actions":[
			{"_class":"hudson.model.ParametersAction","parameters":[
				{"name":"ENV","value":"staging"},
				{"name":"REGION","value":"us-east-1"}
			]},
			{"_class":"hudson.model.ParametersAction","parameters":[
				{"name":"ENV","value":"prod"}
			]}
		]
	}`)
	got, err := schema.MapBuildParams(raw)
	if err != nil {
		t.Fatalf("MapBuildParams: %v", err)
	}
	values := map[string]any{}
	for _, p := range got.Parameters {
		values[p.Name] = p.Value
	}
	if values["ENV"] != "prod" {
		t.Errorf("ENV: want prod (last-write-wins), got %v", values["ENV"])
	}
	if values["REGION"] != "us-east-1" {
		t.Errorf("REGION: want us-east-1, got %v", values["REGION"])
	}
}

// MapBuildCancel projects a running build's status onto BuildCancel.
// State reflects the build state at the moment cancel was requested,
// so a still-running build yields RUNNING.
func Test_MapBuildCancel_RunningBuild(t *testing.T) {
	raw := []byte(`{
		"number":42,
		"url":"http://jenkins.example/job/svc/42/",
		"result":null,
		"building":true,
		"timestamp":1700000000000,
		"duration":5000,
		"estimatedDuration":60000
	}`)

	got, err := schema.MapBuildCancel(raw)
	if err != nil {
		t.Fatalf("MapBuildCancel: %v", err)
	}
	if got.BuildNumber != 42 {
		t.Errorf("BuildNumber=%d, want 42", got.BuildNumber)
	}
	if got.BuildURL != "http://jenkins.example/job/svc/42/" {
		t.Errorf("BuildURL=%q, unexpected", got.BuildURL)
	}
	if got.State != schema.BuildStateRunning {
		t.Errorf("State=%q, want RUNNING", got.State)
	}
}

// When cancel is invoked against an already-finished build, the
// projected state is DONE (with whatever result Jenkins recorded).
func Test_MapBuildCancel_AlreadyDone(t *testing.T) {
	raw := []byte(`{
		"number":42,
		"url":"http://jenkins.example/job/svc/42/",
		"result":"ABORTED",
		"building":false,
		"timestamp":1700000000000,
		"duration":12000,
		"estimatedDuration":60000
	}`)

	got, err := schema.MapBuildCancel(raw)
	if err != nil {
		t.Fatalf("MapBuildCancel: %v", err)
	}
	if got.State != schema.BuildStateDone {
		t.Errorf("State=%q, want DONE", got.State)
	}
}

// Malformed JSON propagates the underlying MapBuildStatus error.
func Test_MapBuildCancel_MalformedJSON(t *testing.T) {
	_, err := schema.MapBuildCancel([]byte(`{not json`))
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}
