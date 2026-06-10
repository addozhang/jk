//go:build e2e

package e2e

// Build-cancel e2e: drive `jk build cancel <url> [--wait]` against the
// seeded `deploy-input` pipeline, which pauses indefinitely on a
// pending input step and therefore gives us a reliably-running build to
// stop. Reuses the trigger/wait helpers and serialization mutex from
// build_input_e2e_test.go.

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// Test_E2E_BuildCancel_StopsRunningBuild triggers deploy-input, waits
// for it to pause on input, cancels it (without --wait), and asserts
// the build ends up ABORTED.
func Test_E2E_BuildCancel_StopsRunningBuild(t *testing.T) {
	deployInputMu.Lock()
	defer deployInputMu.Unlock()

	num := triggerDeployInput(t)
	buildURL := fmt.Sprintf("%s%s/%d/", h.jenkinsURL, jenkinsJobPath("deploy-input"), num)

	waitForPendingInput(t, buildURL)

	stdout, stderr := h.mustRun(t, "build", "cancel", buildURL)
	for _, want := range []string{
		`schemaVersion: "1"`,
		fmt.Sprintf("buildNumber: %d", num),
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q in stdout:\n%s\nstderr:\n%s", want, stdout, stderr)
		}
	}

	if got := waitForBuildResult(t, buildURL); got != "ABORTED" {
		t.Fatalf("build result=%s, want ABORTED", got)
	}
}

// Test_E2E_BuildCancel_WaitExitsAborted cancels with --wait and asserts
// the process exits with code 3 (ABORTED). The poll must pass through
// the PENDING_INPUT window without treating it as terminal.
func Test_E2E_BuildCancel_WaitExitsAborted(t *testing.T) {
	deployInputMu.Lock()
	defer deployInputMu.Unlock()

	num := triggerDeployInput(t)
	buildURL := fmt.Sprintf("%s%s/%d/", h.jenkinsURL, jenkinsJobPath("deploy-input"), num)

	waitForPendingInput(t, buildURL)

	_, _, err := h.run(t, "build", "cancel", buildURL, "--wait")
	if err == nil {
		t.Fatal("expected non-zero exit (ABORTED=3), got nil")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("error type=%T, want *exec.ExitError: %v", err, err)
	}
	if code := exitErr.ExitCode(); code != 3 {
		t.Errorf("exit code=%d, want 3 (ABORTED)", code)
	}
}
