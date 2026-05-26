package schema_test

import (
	"encoding/json"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/addozhang/jk/internal/schema"
)

// These tests pin the public contract documented in docs/schema.md §3.
// They intentionally exercise marshaling (both json and yaml) so that the
// "identical output" guarantee from tasks.md 10.3 is enforced.

func Test_Enums_HaveCorrectStringValues(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"BuildResultSuccess", string(schema.BuildResultSuccess), "SUCCESS"},
		{"BuildResultFailure", string(schema.BuildResultFailure), "FAILURE"},
		{"BuildResultUnstable", string(schema.BuildResultUnstable), "UNSTABLE"},
		{"BuildResultAborted", string(schema.BuildResultAborted), "ABORTED"},
		{"BuildResultNotBuilt", string(schema.BuildResultNotBuilt), "NOT_BUILT"},

		{"BuildStateQueued", string(schema.BuildStateQueued), "QUEUED"},
		{"BuildStateRunning", string(schema.BuildStateRunning), "RUNNING"},
		{"BuildStatePendingInput", string(schema.BuildStatePendingInput), "PENDING_INPUT"},
		{"BuildStateDone", string(schema.BuildStateDone), "DONE"},

		{"ParameterTypeString", string(schema.ParameterTypeString), "STRING"},
		{"ParameterTypeBoolean", string(schema.ParameterTypeBoolean), "BOOLEAN"},
		{"ParameterTypeChoice", string(schema.ParameterTypeChoice), "CHOICE"},
		{"ParameterTypeText", string(schema.ParameterTypeText), "TEXT"},
		{"ParameterTypePassword", string(schema.ParameterTypePassword), "PASSWORD"},
		{"ParameterTypeUnknown", string(schema.ParameterTypeUnknown), "UNKNOWN"},

		{"ItemTypePipeline", string(schema.ItemTypePipeline), "PIPELINE"},
		{"ItemTypeFolder", string(schema.ItemTypeFolder), "FOLDER"},

		{"StageStatusSuccess", string(schema.StageStatusSuccess), "SUCCESS"},
		{"StageStatusFailure", string(schema.StageStatusFailure), "FAILURE"},
		{"StageStatusAborted", string(schema.StageStatusAborted), "ABORTED"},
		{"StageStatusUnstable", string(schema.StageStatusUnstable), "UNSTABLE"},
		{"StageStatusRunning", string(schema.StageStatusRunning), "RUNNING"},
		{"StageStatusNotExecuted", string(schema.StageStatusNotExecuted), "NOT_EXECUTED"},
		{"StageStatusPausedPendingInput", string(schema.StageStatusPausedPendingInput), "PAUSED_PENDING_INPUT"},
		{"StageStatusQueued", string(schema.StageStatusQueued), "QUEUED"},

		{"InputActionProceed", string(schema.InputActionProceed), "PROCEED"},
		{"InputActionAbort", string(schema.InputActionAbort), "ABORT"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}
}

func Test_PipelineInfo_JSON_NullForUnsetPointers(t *testing.T) {
	// docs/schema.md §3.3: description, lastBuild, branches are nullable.
	// Per output spec, nil MUST render as null (not omitted).
	pi := schema.PipelineInfo{
		Name:        "svc",
		FullName:    "team/platform/svc",
		URL:         "https://jenkins.example.com/job/team/job/platform/job/svc",
		Description: nil,
		Buildable:   true,
		LastBuild:   nil,
		Branches:    nil,
	}
	b, err := json.Marshal(pi)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	s := string(b)
	for _, want := range []string{
		`"description":null`,
		`"lastBuild":null`,
		`"branches":null`,
		`"buildable":true`,
		`"name":"svc"`,
		`"fullName":"team/platform/svc"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in JSON output: %s", want, s)
		}
	}
}

func Test_PipelineInfo_YAML_MatchesJSONShape(t *testing.T) {
	desc := "service pipeline"
	res := schema.BuildResultSuccess
	pi := schema.PipelineInfo{
		Name:        "svc",
		FullName:    "team/svc",
		URL:         "https://jenkins.example.com/job/team/job/svc",
		Description: &desc,
		Buildable:   true,
		LastBuild: &schema.BuildRef{
			Number: 42,
			URL:    "https://jenkins.example.com/job/team/job/svc/42/",
			Result: &res,
		},
		Branches: nil,
	}

	jsonBytes, err := json.Marshal(pi)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	yamlBytes, err := yaml.Marshal(pi)
	if err != nil {
		t.Fatalf("yaml.Marshal: %v", err)
	}

	// Round-trip YAML back to JSON; the two JSON encodings MUST be equal
	// (this is the "identical output" guarantee).
	roundTripped, err := yaml.YAMLToJSON(yamlBytes)
	if err != nil {
		t.Fatalf("yaml.YAMLToJSON: %v", err)
	}
	// Re-marshal both to canonical form via map round-trip.
	var a, b map[string]any
	if err := json.Unmarshal(jsonBytes, &a); err != nil {
		t.Fatalf("unmarshal direct json: %v", err)
	}
	if err := json.Unmarshal(roundTripped, &b); err != nil {
		t.Fatalf("unmarshal yaml->json: %v", err)
	}
	ajson, _ := json.Marshal(a)
	bjson, _ := json.Marshal(b)
	if string(ajson) != string(bjson) {
		t.Errorf("yaml output diverges from json output:\n  json: %s\n  yaml: %s", ajson, bjson)
	}
}

func Test_Parameter_DefaultAcceptsStringBoolNil(t *testing.T) {
	// docs/schema.md §3.4: default is string | boolean | null.
	choices := []string{"a", "b"}
	cases := []struct {
		name string
		def  any
		want string
	}{
		{"string default", "main", `"default":"main"`},
		{"bool default", true, `"default":true`},
		{"nil default", nil, `"default":null`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := schema.Parameter{
				Name:        "BRANCH",
				Type:        schema.ParameterTypeChoice,
				Default:     c.def,
				Description: nil,
				Choices:     choices,
			}
			b, err := json.Marshal(p)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			if !strings.Contains(string(b), c.want) {
				t.Errorf("missing %q in %s", c.want, b)
			}
			if !strings.Contains(string(b), `"description":null`) {
				t.Errorf("missing description:null in %s", b)
			}
		})
	}
}

func Test_BuildStatus_AllFieldsMarshal(t *testing.T) {
	// docs/schema.md §3.7: pin all field names.
	qid := 17
	res := schema.BuildResultSuccess
	est := 60000
	bs := schema.BuildStatus{
		BuildURL:            "https://jenkins.example.com/job/svc/42/",
		BuildNumber:         42,
		QueueID:             &qid,
		Result:              &res,
		State:               schema.BuildStateDone,
		Building:            false,
		TimestampUtc:        "2026-05-26T10:00:00Z",
		DurationMs:          12345,
		EstimatedDurationMs: &est,
		ProgressPercent:     100,
		PendingInput:        nil,
	}
	b, err := json.Marshal(bs)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	s := string(b)
	for _, want := range []string{
		`"buildUrl":"https://jenkins.example.com/job/svc/42/"`,
		`"buildNumber":42`,
		`"queueId":17`,
		`"result":"SUCCESS"`,
		`"state":"DONE"`,
		`"building":false`,
		`"timestampUtc":"2026-05-26T10:00:00Z"`,
		`"durationMs":12345`,
		`"estimatedDurationMs":60000`,
		`"progressPercent":100`,
		`"pendingInput":null`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in %s", want, s)
		}
	}
}

func Test_BuildStages_NullableTimingAndParallel(t *testing.T) {
	// docs/schema.md §3.8: startTimeUtc, durationMs, parallel are nullable.
	stages := schema.BuildStages{
		Stages: []schema.Stage{
			{
				Name:         "build",
				DisplayName:  "build",
				Status:       schema.StageStatusRunning,
				StartTimeUtc: nil,
				DurationMs:   nil,
				Parallel:     nil,
			},
		},
	}
	b, err := json.Marshal(stages)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	s := string(b)
	for _, want := range []string{
		`"name":"build"`,
		`"displayName":"build"`,
		`"status":"RUNNING"`,
		`"startTimeUtc":null`,
		`"durationMs":null`,
		`"parallel":null`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in %s", want, s)
		}
	}
}

func Test_PipelineList_ItemsAlwaysArray(t *testing.T) {
	// Empty items MUST marshal as [] not null per docs/schema.md §3.5
	// ("Empty array if none." applies to params; for list it's "child pipelines… in natural order").
	// We adopt the same convention: empty slice -> [].
	list := schema.PipelineList{Items: []schema.Item{}}
	b, err := json.Marshal(list)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !strings.Contains(string(b), `"items":[]`) {
		t.Errorf("expected items:[] in %s", b)
	}
}

func Test_PipelineParams_EmptyParametersIsArray(t *testing.T) {
	pp := schema.PipelineParams{Parameters: []schema.Parameter{}}
	b, err := json.Marshal(pp)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !strings.Contains(string(b), `"parameters":[]`) {
		t.Errorf("expected parameters:[] in %s", b)
	}
}

func Test_AuthList_HostsAlwaysArray(t *testing.T) {
	a := schema.AuthList{Hosts: []string{}}
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !strings.Contains(string(b), `"hosts":[]`) {
		t.Errorf("expected hosts:[] in %s", b)
	}
}

func Test_BuildInputResult_FieldsAndEnum(t *testing.T) {
	r := schema.BuildInputResult{
		InputID:  "Deploy-to-prod",
		Action:   schema.InputActionProceed,
		BuildURL: "https://jenkins.example.com/job/svc/42/",
		State:    schema.BuildStateRunning,
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	s := string(b)
	for _, want := range []string{
		`"inputId":"Deploy-to-prod"`,
		`"action":"PROCEED"`,
		`"buildUrl":"https://jenkins.example.com/job/svc/42/"`,
		`"state":"RUNNING"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in %s", want, s)
		}
	}
}

func Test_BuildTrigger_NullableBuildIdentifiers(t *testing.T) {
	bt := schema.BuildTrigger{
		QueueID:     7,
		BuildURL:    nil,
		BuildNumber: nil,
	}
	b, err := json.Marshal(bt)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	s := string(b)
	for _, want := range []string{
		`"queueId":7`,
		`"buildUrl":null`,
		`"buildNumber":null`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in %s", want, s)
		}
	}
}
