package schema_test

// Mapper tests verify the JSON-shape -> schema.* transformation. Each
// fixture is a minimal but realistic Jenkins response captured from
// documented sources (Jenkins core + pipeline-stage-view-plugin
// README + spike notes). When spike 1.x produces real fixtures, those
// will replace or augment the inline fixtures below; the tests will
// then either still pass (mapper is correct) or fail with a clear
// signal that the mapper needs updating.
//
// OpenSpec mapping:
//   - tasks 12.1 -> Test_MapPipelineInfo_*
//   - tasks 12.2 -> Test_MapPipelineParams_*
//   - tasks 12.3 -> Test_MapPipelineList_*
//   - tasks 12.7 -> covered transitively (enum normalization is
//                    asserted within each higher-level test)

import (
	"testing"

	"github.com/addozhang/jk/internal/schema"
)

// stringPtr is a brevity helper used heavily in the fixtures.
func stringPtr(s string) *string { return &s }

// ---------------------------------------------------------------------------
// 12.1 MapPipelineInfo
// ---------------------------------------------------------------------------

// A single-branch WorkflowJob with a last build that's still running
// (result == null in the JSON). Asserts:
//   - Name/FullName/URL pass through;
//   - description null is preserved as nil *string;
//   - LastBuild.Result is *nil* while running;
//   - Branches is nil for non-multibranch pipelines (so it serializes
//     as null per the schema).
func Test_MapPipelineInfo_SingleBranchRunningBuild(t *testing.T) {
	raw := []byte(`{
		"_class":"org.jenkinsci.plugins.workflow.job.WorkflowJob",
		"name":"svc",
		"fullName":"team/svc",
		"url":"http://jenkins.example/job/team/job/svc/",
		"description":null,
		"buildable":true,
		"lastBuild":{
			"number":42,
			"url":"http://jenkins.example/job/team/job/svc/42/",
			"result":null
		}
	}`)

	got, err := schema.MapPipelineInfo(raw)
	if err != nil {
		t.Fatalf("MapPipelineInfo: %v", err)
	}

	if got.Name != "svc" {
		t.Errorf("Name=%q, want %q", got.Name, "svc")
	}
	if got.FullName != "team/svc" {
		t.Errorf("FullName=%q, want %q", got.FullName, "team/svc")
	}
	if got.Description != nil {
		t.Errorf("Description=%v, want nil", got.Description)
	}
	if !got.Buildable {
		t.Errorf("Buildable=false, want true")
	}
	if got.LastBuild == nil {
		t.Fatalf("LastBuild is nil; want non-nil")
	}
	if got.LastBuild.Number != 42 {
		t.Errorf("LastBuild.Number=%d, want 42", got.LastBuild.Number)
	}
	if got.LastBuild.Result != nil {
		t.Errorf("LastBuild.Result=%v, want nil (still running)", got.LastBuild.Result)
	}
	if got.Branches != nil {
		t.Errorf("Branches=%v, want nil for single-branch pipeline", got.Branches)
	}
}

// A WorkflowJob with a finished build: result is "SUCCESS" and must
// be normalized to schema.BuildResultSuccess.
func Test_MapPipelineInfo_FinishedBuildResultEnum(t *testing.T) {
	raw := []byte(`{
		"name":"svc","fullName":"svc","url":"http://x/job/svc/",
		"description":"deploys the svc","buildable":true,
		"lastBuild":{"number":7,"url":"http://x/job/svc/7/","result":"SUCCESS"}
	}`)
	got, err := schema.MapPipelineInfo(raw)
	if err != nil {
		t.Fatalf("MapPipelineInfo: %v", err)
	}
	if got.Description == nil || *got.Description != "deploys the svc" {
		t.Errorf("Description=%v, want %q", got.Description, "deploys the svc")
	}
	if got.LastBuild.Result == nil {
		t.Fatal("LastBuild.Result is nil; want SUCCESS")
	}
	if *got.LastBuild.Result != schema.BuildResultSuccess {
		t.Errorf("LastBuild.Result=%q, want %q", *got.LastBuild.Result, schema.BuildResultSuccess)
	}
}

// A pipeline that has never been built: lastBuild is null. LastBuild
// must serialize as nil (i.e. JSON null), not as a zero-value struct.
func Test_MapPipelineInfo_NeverBuilt(t *testing.T) {
	raw := []byte(`{"name":"svc","fullName":"svc","url":"http://x/job/svc/","buildable":true,"lastBuild":null}`)
	got, err := schema.MapPipelineInfo(raw)
	if err != nil {
		t.Fatalf("MapPipelineInfo: %v", err)
	}
	if got.LastBuild != nil {
		t.Errorf("LastBuild=%+v, want nil for never-built pipeline", got.LastBuild)
	}
}

// A multibranch WorkflowMultiBranchProject exposes branches via the
// `jobs` field. The mapper surfaces those as Branches and leaves
// LastBuild nil (multibranch parents themselves do not have builds).
func Test_MapPipelineInfo_MultibranchExposesBranches(t *testing.T) {
	raw := []byte(`{
		"_class":"org.jenkinsci.plugins.workflow.multibranch.WorkflowMultiBranchProject",
		"name":"svc","fullName":"team/svc",
		"url":"http://x/job/team/job/svc/","buildable":false,
		"jobs":[
			{"_class":"org.jenkinsci.plugins.workflow.job.WorkflowJob","name":"main","url":"http://x/job/team/job/svc/job/main/"},
			{"_class":"org.jenkinsci.plugins.workflow.job.WorkflowJob","name":"PR-12","url":"http://x/job/team/job/svc/job/PR-12/"}
		]
	}`)
	got, err := schema.MapPipelineInfo(raw)
	if err != nil {
		t.Fatalf("MapPipelineInfo: %v", err)
	}
	if got.LastBuild != nil {
		t.Errorf("LastBuild=%+v, want nil for multibranch parent", got.LastBuild)
	}
	if len(got.Branches) != 2 {
		t.Fatalf("len(Branches)=%d, want 2", len(got.Branches))
	}
	if got.Branches[0].Name != "main" || got.Branches[1].Name != "PR-12" {
		t.Errorf("Branches=%+v, want [main, PR-12]", got.Branches)
	}
}

// ---------------------------------------------------------------------------
// 12.2 MapPipelineParams
// ---------------------------------------------------------------------------

func Test_MapPipelineParams_AllTypes(t *testing.T) {
	raw := []byte(`{
		"property":[
			{"_class":"hudson.model.ParametersDefinitionProperty","parameterDefinitions":[
				{"_class":"hudson.model.StringParameterDefinition","name":"BRANCH","description":"git ref","type":"StringParameterDefinition","defaultParameterValue":{"value":"main"}},
				{"_class":"hudson.model.BooleanParameterDefinition","name":"DRY_RUN","description":null,"type":"BooleanParameterDefinition","defaultParameterValue":{"value":false}},
				{"_class":"hudson.model.ChoiceParameterDefinition","name":"ENV","description":null,"type":"ChoiceParameterDefinition","choices":["dev","staging","prod"]},
				{"_class":"hudson.model.TextParameterDefinition","name":"NOTES","type":"TextParameterDefinition","defaultParameterValue":{"value":"hello\nworld"}},
				{"_class":"hudson.model.PasswordParameterDefinition","name":"TOKEN","type":"PasswordParameterDefinition"},
				{"_class":"com.example.SuperCustomParameter","name":"WEIRD","type":"WeirdParameterDefinition"}
			]}
		]
	}`)
	got, err := schema.MapPipelineParams(raw)
	if err != nil {
		t.Fatalf("MapPipelineParams: %v", err)
	}
	if len(got.Parameters) != 6 {
		t.Fatalf("Parameters len=%d, want 6", len(got.Parameters))
	}
	check := func(idx int, name string, typ schema.ParameterType) {
		t.Helper()
		p := got.Parameters[idx]
		if p.Name != name {
			t.Errorf("[%d] Name=%q, want %q", idx, p.Name, name)
		}
		if p.Type != typ {
			t.Errorf("[%d] Type=%q, want %q", idx, p.Type, typ)
		}
	}
	check(0, "BRANCH", schema.ParameterTypeString)
	check(1, "DRY_RUN", schema.ParameterTypeBoolean)
	check(2, "ENV", schema.ParameterTypeChoice)
	check(3, "NOTES", schema.ParameterTypeText)
	check(4, "TOKEN", schema.ParameterTypePassword)
	check(5, "WEIRD", schema.ParameterTypeUnknown)

	// Choices should only be populated for CHOICE.
	if got.Parameters[2].Choices == nil || len(got.Parameters[2].Choices) != 3 {
		t.Errorf("Choices=%v, want [dev staging prod]", got.Parameters[2].Choices)
	}
	if got.Parameters[0].Choices != nil {
		t.Errorf("non-choice param has Choices=%v, want nil", got.Parameters[0].Choices)
	}

	// Defaults: string preserved, bool preserved, missing default -> nil.
	if got.Parameters[0].Default != "main" {
		t.Errorf("default[0]=%v, want \"main\"", got.Parameters[0].Default)
	}
	if got.Parameters[1].Default != false {
		t.Errorf("default[1]=%v, want false", got.Parameters[1].Default)
	}
	if got.Parameters[4].Default != nil {
		t.Errorf("default[4]=%v, want nil (no default declared)", got.Parameters[4].Default)
	}
}

// A pipeline with no ParametersDefinitionProperty yields an empty
// (non-nil) Parameters slice so the JSON output is `[]`, not `null`.
func Test_MapPipelineParams_NoParameters(t *testing.T) {
	raw := []byte(`{"property":[]}`)
	got, err := schema.MapPipelineParams(raw)
	if err != nil {
		t.Fatalf("MapPipelineParams: %v", err)
	}
	if got.Parameters == nil {
		t.Errorf("Parameters is nil; want empty slice")
	}
	if len(got.Parameters) != 0 {
		t.Errorf("len=%d, want 0", len(got.Parameters))
	}
}

// ---------------------------------------------------------------------------
// 12.3 MapPipelineList
// ---------------------------------------------------------------------------

// A folder containing one regular pipeline, one multibranch (reported
// as FOLDER per docs/schema.md §3.5), and one sub-folder.
func Test_MapPipelineList_MixedChildren(t *testing.T) {
	raw := []byte(`{"jobs":[
		{"_class":"com.cloudbees.hudson.plugins.folder.Folder","name":"sub","url":"http://x/job/team/job/sub/"},
		{"_class":"org.jenkinsci.plugins.workflow.job.WorkflowJob","name":"svc","url":"http://x/job/team/job/svc/"},
		{"_class":"org.jenkinsci.plugins.workflow.multibranch.WorkflowMultiBranchProject","name":"app","url":"http://x/job/team/job/app/"}
	]}`)
	got, err := schema.MapPipelineList(raw)
	if err != nil {
		t.Fatalf("MapPipelineList: %v", err)
	}
	if len(got.Items) != 3 {
		t.Fatalf("len=%d, want 3", len(got.Items))
	}
	want := []schema.ItemType{
		schema.ItemTypeFolder,   // sub
		schema.ItemTypePipeline, // svc
		schema.ItemTypeFolder,   // multibranch -> FOLDER per spec
	}
	for i, w := range want {
		if got.Items[i].Type != w {
			t.Errorf("[%d] %q Type=%q, want %q", i, got.Items[i].Name, got.Items[i].Type, w)
		}
	}
}

// Empty folder produces an empty (non-nil) Items slice.
func Test_MapPipelineList_EmptyFolder(t *testing.T) {
	raw := []byte(`{"jobs":[]}`)
	got, err := schema.MapPipelineList(raw)
	if err != nil {
		t.Fatalf("MapPipelineList: %v", err)
	}
	if got.Items == nil {
		t.Errorf("Items is nil; want empty slice")
	}
}

// A bogus _class falls back to PIPELINE (the only safe assumption for
// "looks like a runnable thing") with a follow-up TODO recorded in the
// mapper for future hardening.
func Test_MapPipelineList_UnknownClass(t *testing.T) {
	raw := []byte(`{"jobs":[
		{"_class":"plugin.from.the.future.MagicJob","name":"x","url":"http://x/job/team/job/x/"}
	]}`)
	got, err := schema.MapPipelineList(raw)
	if err != nil {
		t.Fatalf("MapPipelineList: %v", err)
	}
	if got.Items[0].Type != schema.ItemTypePipeline {
		t.Errorf("unknown class -> Type=%q, want PIPELINE (safe default)", got.Items[0].Type)
	}
}

// ---------------------------------------------------------------------------
// Sanity: malformed JSON surfaces a clear error rather than panicking.
// ---------------------------------------------------------------------------

func Test_Mappers_RejectMalformedJSON(t *testing.T) {
	cases := map[string]func([]byte) error{
		"PipelineInfo":   func(b []byte) error { _, err := schema.MapPipelineInfo(b); return err },
		"PipelineParams": func(b []byte) error { _, err := schema.MapPipelineParams(b); return err },
		"PipelineList":   func(b []byte) error { _, err := schema.MapPipelineList(b); return err },
	}
	for name, fn := range cases {
		if err := fn([]byte(`{not json`)); err == nil {
			t.Errorf("%s: expected error on malformed JSON", name)
		}
	}
}

// Defensive use of stringPtr to keep the import meaningful even when
// the only consumer of stringPtr is removed in a future refactor; this
// reference is removed by the compiler if unused elsewhere.
var _ = stringPtr
