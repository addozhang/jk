package output_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/addozhang/jk/internal/output"
	"github.com/addozhang/jk/internal/schema"
)

func sampleStatus() schema.BuildStatus {
	qid := 17
	res := schema.BuildResultSuccess
	return schema.BuildStatus{
		BuildURL:            "https://jenkins.example.com/job/svc/42/",
		BuildNumber:         42,
		QueueID:             &qid,
		Result:              &res,
		State:               schema.BuildStateDone,
		Building:            false,
		TimestampUtc:        "2026-05-26T10:00:00Z",
		DurationMs:          12345,
		EstimatedDurationMs: nil,
		ProgressPercent:     100,
		PendingInput:        nil,
	}
}

func Test_Render_DefaultIsYAML(t *testing.T) {
	// The CLI passes "" (or "yaml") for default; both MUST yield YAML.
	for _, f := range []string{"", "yaml"} {
		b, err := output.Render(sampleStatus(), f)
		if err != nil {
			t.Fatalf("format=%q: %v", f, err)
		}
		s := string(b)
		// YAML uses `key: value` lines, JSON uses `{`.
		if strings.HasPrefix(s, "{") {
			t.Errorf("format=%q: got JSON-like output, want YAML: %s", f, s)
		}
		if !strings.HasPrefix(s, "schemaVersion:") {
			t.Errorf("format=%q: YAML must start with schemaVersion, got: %s", f, s)
		}
	}
}

func Test_Render_YAML_SchemaVersionFirstLine(t *testing.T) {
	b, err := output.Render(sampleStatus(), "yaml")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	first, _, _ := strings.Cut(string(b), "\n")
	if first != `schemaVersion: "1"` {
		t.Errorf("first line = %q, want `schemaVersion: \"1\"`", first)
	}
}

func Test_Render_JSON_SchemaVersionFirstKey(t *testing.T) {
	b, err := output.Render(sampleStatus(), "json")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(b)
	// Compact JSON should begin with the schemaVersion key right after `{`.
	if !strings.HasPrefix(s, `{"schemaVersion":"1",`) {
		t.Errorf("JSON must start with schemaVersion as first key, got: %s", s)
	}
}

func Test_Render_JSON_IsCompact(t *testing.T) {
	b, err := output.Render(sampleStatus(), "json")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	// Compact = no newlines and no two-space indents inside the document.
	if bytes.Contains(b, []byte("\n  ")) || bytes.Count(b, []byte{'\n'}) > 1 {
		t.Errorf("JSON output should be compact, got:\n%s", b)
	}
}

func Test_Render_JSON_ValidStructure(t *testing.T) {
	b, err := output.Render(sampleStatus(), "json")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, b)
	}
	if got["schemaVersion"] != "1" {
		t.Errorf("schemaVersion = %v, want \"1\"", got["schemaVersion"])
	}
	if got["buildNumber"].(float64) != 42 {
		t.Errorf("buildNumber = %v, want 42", got["buildNumber"])
	}
	// Nullable fields MUST be present (explicit null, not omitted).
	if _, ok := got["pendingInput"]; !ok {
		t.Errorf("pendingInput field missing (must be explicit null)")
	}
}

func Test_Render_YAML_ValidStructure(t *testing.T) {
	b, err := output.Render(sampleStatus(), "yaml")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	var got map[string]any
	if err := yaml.Unmarshal(b, &got); err != nil {
		t.Fatalf("output is not valid YAML: %v\n%s", err, b)
	}
	if got["schemaVersion"] != "1" {
		t.Errorf("schemaVersion = %v, want \"1\"", got["schemaVersion"])
	}
	if got["buildNumber"].(float64) != 42 {
		t.Errorf("buildNumber = %v, want 42", got["buildNumber"])
	}
}

func Test_Render_YAML_JSON_HaveSameSchema(t *testing.T) {
	// The "identical output" guarantee from tasks 9.x: yaml and json must
	// decode to equivalent maps.
	yb, err := output.Render(sampleStatus(), "yaml")
	if err != nil {
		t.Fatal(err)
	}
	jb, err := output.Render(sampleStatus(), "json")
	if err != nil {
		t.Fatal(err)
	}
	var yMap, jMap map[string]any
	if err := yaml.Unmarshal(yb, &yMap); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(jb, &jMap); err != nil {
		t.Fatal(err)
	}
	ybytes, _ := json.Marshal(yMap)
	jbytes, _ := json.Marshal(jMap)
	if string(ybytes) != string(jbytes) {
		t.Errorf("yaml vs json shape diverges:\n  yaml: %s\n  json: %s", ybytes, jbytes)
	}
}

func Test_Render_Raw_BytesPassThrough(t *testing.T) {
	raw := []byte(`{"_class":"hudson.model.FreeStyleProject","name":"svc"}`)
	b, err := output.Render(raw, "raw")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !bytes.Equal(b, raw) {
		t.Errorf("raw output altered:\n  got:  %s\n  want: %s", b, raw)
	}
}

func Test_Render_Raw_OmitsSchemaVersion(t *testing.T) {
	raw := []byte(`{"foo":"bar"}`)
	b, err := output.Render(raw, "raw")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("schemaVersion")) {
		t.Errorf("raw output must not inject schemaVersion: %s", b)
	}
}

func Test_Render_Raw_RejectsNonBytes(t *testing.T) {
	_, err := output.Render(sampleStatus(), "raw")
	if err == nil {
		t.Error("expected error when -o raw is given a struct value")
	}
}

func Test_Render_UnknownFormatErrors(t *testing.T) {
	_, err := output.Render(sampleStatus(), "xml")
	if err == nil {
		t.Error("expected error for unknown format")
	}
}

func Test_Render_NullableFieldsRenderAsNull(t *testing.T) {
	// Regression: explicit nulls survive the schemaVersion injection.
	pi := schema.PipelineInfo{
		Name:        "svc",
		FullName:    "team/svc",
		URL:         "https://jenkins.example.com/job/team/job/svc",
		Description: nil,
		Buildable:   true,
		LastBuild:   nil,
		Branches:    nil,
	}
	jb, err := output.Render(pi, "json")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"description":null`,
		`"lastBuild":null`,
		`"branches":null`,
	} {
		if !strings.Contains(string(jb), want) {
			t.Errorf("missing %q in JSON: %s", want, jb)
		}
	}

	yb, err := output.Render(pi, "yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"description: null",
		"lastBuild: null",
		"branches: null",
	} {
		if !strings.Contains(string(yb), want) {
			t.Errorf("missing %q in YAML:\n%s", want, yb)
		}
	}
}

func Test_Render_YAML_EndsWithNewline(t *testing.T) {
	b, err := output.Render(sampleStatus(), "yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 || b[len(b)-1] != '\n' {
		t.Errorf("YAML output should end with newline, got: %q", b)
	}
}
