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

// extend-build-addressing §6: `jk build status` MUST accept a
// Jenkins permalink in place of a numeric build number, and the
// output MUST report the resolved numeric build. Exercises the
// most common permalink shape (`lastBuild`).
func Test_E2E_BuildStatus_LastBuildPermalink(t *testing.T) {
	url := h.jobURL("hello") + "lastBuild/"
	stdout, _ := h.mustRun(t, "build", "status", url)
	for _, want := range []string{
		`schemaVersion: "1"`,
		"buildNumber: 1",
		"state: DONE",
		"result: SUCCESS",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q in stdout:\n%s", want, stdout)
		}
	}
}

// Same as above but with `lastSuccessfulBuild` to cover the
// non-`lastBuild` permalink resolution path (some Jenkins versions
// fast-path lastBuild specifically, so we want at least one
// non-aliased permalink in the e2e suite).
func Test_E2E_BuildStatus_LastSuccessfulBuildPermalink(t *testing.T) {
	url := h.jobURL("hello") + "lastSuccessfulBuild/"
	stdout, _ := h.mustRun(t, "build", "status", url)
	for _, want := range []string{
		"buildNumber: 1",
		"result: SUCCESS",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q in stdout:\n%s", want, stdout)
		}
	}
}

// extend-build-addressing §11.1: `jk build params <numeric-url>`
// against the `params` pipeline MUST return the parameter values
// the build was triggered with. We trigger a fresh build with
// known parameter values (rather than asserting on a harness-seeded
// build) because Jenkins does NOT record a ParametersAction on the
// very first build of a parameterized pipeline — the parameter
// definitions only become "known" after build #1 completes. By
// triggering inside the test we guarantee a build with a fully
// populated actions[] regardless of harness build-history state.
func Test_E2E_BuildParams_Numeric(t *testing.T) {
	// Trigger a build with non-default values so the assertions
	// cannot accidentally match a different build's defaults.
	pipelineURL := h.jobURL("params")
	triggerOut, _ := h.mustRun(t,
		"build", "trigger", pipelineURL,
		"-p", "GREETING=hola",
		"-p", "TARGET=mundo",
		"-p", "LOUD=true",
		"--watch", "-o", "json",
	)
	buildURL := extractField(t, triggerOut, "buildUrl")

	stdout, _ := h.mustRun(t, "build", "params", buildURL)
	for _, want := range []string{
		`schemaVersion: "1"`,
		"name: GREETING",
		"value: hola",
		"name: TARGET",
		"value: mundo",
		"name: LOUD",
		"value: true",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q in stdout:\n%s", want, stdout)
		}
	}
}

// extend-build-addressing §11.2: same `params` pipeline addressed
// by permalink — exercises the permalink synergy explicitly and
// proves the resolved numeric buildNumber surfaces in output even
// when the user supplied a symbolic URL. The values asserted are
// those of whichever build is currently lastSuccessfulBuild, so we
// trigger a build with known values first and then read it back via
// permalink.
func Test_E2E_BuildParams_Permalink(t *testing.T) {
	pipelineURL := h.jobURL("params")
	h.mustRun(t,
		"build", "trigger", pipelineURL,
		"-p", "GREETING=bonjour",
		"-p", "TARGET=monde",
		"-p", "LOUD=false",
		"--watch",
	)

	url := pipelineURL + "lastSuccessfulBuild/"
	stdout, _ := h.mustRun(t, "build", "params", url)
	for _, want := range []string{
		"name: GREETING",
		"value: bonjour",
		"name: TARGET",
		"value: monde",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q in stdout:\n%s", want, stdout)
		}
	}
}

// Unparameterized build: `params` array MUST be empty, not error,
// not null. Uses the `hello` pipeline which has no `parameters {}`
// block.
func Test_E2E_BuildParams_UnparameterizedBuildEmpty(t *testing.T) {
	url := lastBuildURL("hello")
	stdout, _ := h.mustRun(t, "build", "params", url, "-o", "json")
	if !strings.Contains(stdout, `"parameters":[]`) {
		t.Errorf("expected parameters:[] in json output:\n%s", stdout)
	}
}

// extractField is a tiny JSON field extractor for trigger-output
// parsing. We avoid importing encoding/json into the e2e tests so
// signature drift in jk's output surfaces as a test failure rather
// than a silent unmarshal-into-different-struct.
func extractField(t *testing.T, jsonOut, key string) string {
	t.Helper()
	needle := `"` + key + `":"`
	i := strings.Index(jsonOut, needle)
	if i < 0 {
		t.Fatalf("field %q not found in output:\n%s", key, jsonOut)
	}
	start := i + len(needle)
	end := strings.IndexByte(jsonOut[start:], '"')
	if end < 0 {
		t.Fatalf("unterminated %q value in:\n%s", key, jsonOut)
	}
	return jsonOut[start : start+end]
}
