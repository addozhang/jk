//go:build e2e

package e2e

// Build e2e: drive jk build {status|stages|logs} against the pre-warmed
// build history seeded by setupHarness (which triggers one build of
// each seeded pipeline and waits for completion).
//
// `build input` is covered separately in build_input_e2e_test.go since
// it needs to drive a paused-then-resumed build lifecycle that does not
// fit setupHarness's "trigger and wait for completion" model. We still
// do NOT cover `build trigger` or `--watch` here: those depend on
// resolution shapes still being validated against a real Jenkins.

import (
	"strings"
	"testing"
)

// lastBuildURL returns the canonical URL for the most recent build of
// jobPath. setupHarness has already warmed at least one build per
// seeded job; build #1 always exists.
func lastBuildURL(jobPath string) string {
	return h.jobURL(jobPath) + "1/"
}

func Test_E2E_BuildStatus_HelloWorld(t *testing.T) {
	url := lastBuildURL("hello")
	stdout, _ := h.mustRun(t, "build", "status", url)
	for _, want := range []string{
		`schemaVersion: "1"`,
		"state: DONE",
		"result: SUCCESS",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q in stdout:\n%s", want, stdout)
		}
	}
}

func Test_E2E_BuildStages_ParallelLayout(t *testing.T) {
	url := lastBuildURL("team/parallel")
	stdout, _ := h.mustRun(t, "build", "stages", url)
	// The parallel job declares Build, Test (which fans out into
	// Unit/Integration/Lint), and Deploy. The stage names MUST all
	// appear in the rendered stage tree.
	for _, want := range []string{"Build", "Test", "Unit", "Integration", "Lint", "Deploy"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing stage %q in stages output:\n%s", want, stdout)
		}
	}
}

func Test_E2E_BuildLogs_HelloWorld(t *testing.T) {
	url := lastBuildURL("hello")
	stdout, _ := h.mustRun(t, "build", "logs", url)
	// build logs intentionally does NOT inject schemaVersion (raw
	// stream). Assertion focuses on the pipeline's own echo'd text.
	if !strings.Contains(stdout, "hello from jk e2e harness") {
		t.Errorf("expected pipeline echo in logs, got:\n%s", stdout)
	}
}

// build logs --stage filters to a single stage's log. The parallel job
// has a uniquely-named "Unit" stage we can target.
func Test_E2E_BuildLogs_StageFilter(t *testing.T) {
	url := lastBuildURL("team/parallel")
	stdout, _ := h.mustRun(t, "build", "logs", url, "--stage", "Unit")
	if !strings.Contains(stdout, "unit tests") {
		t.Errorf("expected 'unit tests' in stage log, got:\n%s", stdout)
	}
	// The "integration tests" echo MUST NOT appear when filtered to
	// the Unit stage.
	if strings.Contains(stdout, "integration tests") {
		t.Errorf("Unit-stage log leaked Integration output:\n%s", stdout)
	}
}

// Unknown stage name MUST produce a friendly error listing the actual
// stages (per design D7 + specs/build).
func Test_E2E_BuildLogs_UnknownStage(t *testing.T) {
	url := lastBuildURL("team/parallel")
	stdout, stderr, err := h.run(t, "build", "logs", url, "--stage", "NoSuchStage")
	if err == nil {
		t.Fatalf("expected non-zero exit for unknown stage; stdout=%s stderr=%s", stdout, stderr)
	}
	// The stage-not-found error message lists discovered stage names.
	if !strings.Contains(stderr, "Build") || !strings.Contains(stderr, "Deploy") {
		t.Errorf("expected stage list in error, got:\n%s", stderr)
	}
}
