//go:build e2e

package e2e

// Pipeline e2e: drive jk pipeline {info|params|list} against the seeded
// pipelines. The assertions match the field-level contract documented in
// docs/schema.md §3.3 / §3.4 / §3.5.

import (
	"strings"
	"testing"
)

func Test_E2E_PipelineInfo_HelloWorld(t *testing.T) {
	url := h.jobURL("hello")
	stdout, stderr := h.mustRun(t, "pipeline", "info", url)

	for _, want := range []string{
		`schemaVersion: "1"`,
		"name: hello",
		"buildable: true",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q in stdout:\n%s\nstderr:\n%s", want, stdout, stderr)
		}
	}
}

func Test_E2E_PipelineInfo_FolderJob(t *testing.T) {
	url := h.jobURL("team/parallel")
	stdout, _ := h.mustRun(t, "pipeline", "info", url)
	for _, want := range []string{
		"name: parallel",
		"fullName: team/parallel",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q in stdout:\n%s", want, stdout)
		}
	}
}

// Spec: pipeline params surfaces parameter definitions w/ types + defaults.
func Test_E2E_PipelineParams_RendersAllThree(t *testing.T) {
	url := h.jobURL("params")
	stdout, _ := h.mustRun(t, "pipeline", "params", url)

	// The seeded params job declares GREETING, TARGET, LOUD; each MUST
	// appear with its type. Order is Jenkins-determined; we only check
	// presence.
	for _, want := range []string{
		"GREETING", "TARGET", "LOUD",
		// String + boolean parameter types in the schema enum.
		"STRING", "BOOLEAN",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q in stdout:\n%s", want, stdout)
		}
	}
}

// Spec: pipeline list against a non-folder URL surfaces an actionable
// error telling the user to use `jk pipeline info` instead.
//
// We deliberately do NOT test "list at Jenkins root URL" because the
// specs in openspec/changes/init-jk-jenkins-cli/specs/pipeline/spec.md
// only define list against a <folder-url>; the Jenkins root is not a
// /job/.../ path and url-resolution explicitly rejects it as "not a
// Jenkins job URL". Listing the implicit top-level is tracked as a
// possible v0.2 enhancement.
func Test_E2E_PipelineList_RejectsPipelineURL(t *testing.T) {
	stdout, stderr, err := h.run(t, "pipeline", "list", h.jobURL("hello"))
	if err == nil {
		t.Fatalf("expected non-zero exit listing a pipeline URL; stdout:\n%s", stdout)
	}
	// Error wording is defined in specs/pipeline/spec.md scenario
	// "list against a pipeline URL"; assert on the actionable hint
	// rather than the exact phrasing.
	if !strings.Contains(stderr, "info") {
		t.Errorf("expected stderr to suggest `pipeline info`, got:\n%s", stderr)
	}
}

// pipeline list against a folder lists its children.
func Test_E2E_PipelineList_Folder(t *testing.T) {
	stdout, _ := h.mustRun(t, "pipeline", "list", h.jobURL("team"))
	if !strings.Contains(stdout, "parallel") {
		t.Errorf("missing 'parallel' in folder list:\n%s", stdout)
	}
}
