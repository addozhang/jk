//go:build e2e

package e2e

// Build-input e2e: drive `jk build input <url> proceed -p …` against
// the seeded `deploy-input` pipeline. Unlike the other e2e tests, this
// suite cannot rely on setupHarness's pre-warmed builds — deploy-input
// pauses indefinitely on a pending input step, so it is excluded from
// jobsToTrigger and each test here triggers + drains its own build.
//
// Each test owns one build of `deploy-input`. To avoid build-number
// races between tests, we serialize via a package-level mutex and use
// the discovered nextBuildNumber as the URL the test operates on.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"testing"
	"time"
)

// deployInputMu serializes the trigger → submit → wait lifecycle so
// concurrent tests do not race on lastBuild discovery against the same
// job. The e2e suite runs single-threaded by default, but `go test
// -parallel` would otherwise break the polling logic.
var deployInputMu sync.Mutex

// triggerDeployInput POSTs to /job/deploy-input/build and returns the
// build number Jenkins assigned. Uses a fresh cookie jar + crumb each
// call to stay independent of jk's transport.
func triggerDeployInput(t *testing.T) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	client := &http.Client{Timeout: 15 * time.Second, Jar: jar}

	// Capture nextBuildNumber BEFORE triggering so we know which build
	// number this test owns. Jenkins increments this atomically when
	// the POST is accepted.
	want, err := nextBuildNumber(ctx, client, h.jenkinsURL, "deploy-input", h.jenkinsUser, h.secret)
	if err != nil {
		t.Fatalf("nextBuildNumber: %v", err)
	}

	if err = triggerOne(ctx, client, h.jenkinsURL, "deploy-input", "/build", h.jenkinsUser, h.secret); err != nil {
		t.Fatalf("trigger deploy-input: %v", err)
	}
	return want
}

// nextBuildNumber returns the build number Jenkins will assign to the
// next /build POST against jobPath.
func nextBuildNumber(ctx context.Context, client *http.Client, base, jobPath, user, token string) (int, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+jenkinsJobPath(jobPath)+"/api/json?tree=nextBuildNumber", nil)
	req.SetBasicAuth(user, token)
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("nextBuildNumber status %d", resp.StatusCode)
	}
	var body struct {
		NextBuildNumber int `json:"nextBuildNumber"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, fmt.Errorf("decode nextBuildNumber: %w", err)
	}
	return body.NextBuildNumber, nil
}

// waitForPendingInput polls `jk build status` against the given build
// URL until the rendered YAML contains `state: PENDING_INPUT`, or the
// deadline expires. Using jk itself rather than raw Jenkins API keeps
// the test honest about the read path the user actually sees.
func waitForPendingInput(t *testing.T, buildURL string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		stdout, _, _ := h.run(t, "build", "status", buildURL)
		if strings.Contains(stdout, "state: PENDING_INPUT") {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("build %s never reached PENDING_INPUT within 60s", buildURL)
}

// waitForBuildResult polls `jk build status` until result is non-null,
// returning the result value (SUCCESS, FAILURE, ABORTED, …).
func waitForBuildResult(t *testing.T, buildURL string) string {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		stdout, _, _ := h.run(t, "build", "status", buildURL)
		// "result: null" means still running. Anything else is final.
		for _, line := range strings.Split(stdout, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "result: ") {
				val := strings.TrimSpace(strings.TrimPrefix(line, "result:"))
				if val != "null" && val != "" {
					return val
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("build %s did not finish within 60s", buildURL)
	return ""
}

// abortBuild best-effort aborts a build via jk; used in test cleanup
// to avoid leaking paused builds across runs.
func abortBuild(t *testing.T, buildURL string) {
	t.Helper()
	_, _, _ = h.run(t, "build", "input", buildURL, "abort")
}

// Test_E2E_BuildInput_SubmitParameterizedInput drives the happy path:
// trigger deploy-input, wait for the pending input, submit -p flags,
// wait for SUCCESS, and assert the After-stage echo line proves the
// submitted values landed in the build's environment.
func Test_E2E_BuildInput_SubmitParameterizedInput(t *testing.T) {
	deployInputMu.Lock()
	defer deployInputMu.Unlock()

	num := triggerDeployInput(t)
	buildURL := fmt.Sprintf("%s%s/%d/", h.jenkinsURL, jenkinsJobPath("deploy-input"), num)

	waitForPendingInput(t, buildURL)

	stdout, stderr := h.mustRun(t, "build", "input", buildURL, "proceed",
		"-p", "ENV=prod", "-p", "DRY_RUN=false")
	for _, want := range []string{
		"action: PROCEED",
		"inputId: Deploy",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q in stdout:\n%s\nstderr:\n%s", want, stdout, stderr)
		}
	}

	if got := waitForBuildResult(t, buildURL); got != "SUCCESS" {
		t.Fatalf("build result=%s, want SUCCESS", got)
	}

	// Pull the build log and confirm the submitted values rendered in
	// the After-stage echo line: `decision: ENV=prod DRY_RUN=false`.
	logs, _ := h.mustRun(t, "build", "logs", buildURL)
	if !strings.Contains(logs, "decision: ENV=prod DRY_RUN=false") {
		t.Errorf("expected submitted param values in build log, got:\n%s", logs)
	}
}

// Test_E2E_BuildInput_InvalidChoice_ExitsLocally verifies client-side
// validation: -p ENV=devvv (not in the declared choices) MUST exit 10
// without contacting Jenkins. The build is left paused and must be
// aborted by the test for hygiene.
func Test_E2E_BuildInput_InvalidChoice_ExitsLocally(t *testing.T) {
	deployInputMu.Lock()
	defer deployInputMu.Unlock()

	num := triggerDeployInput(t)
	buildURL := fmt.Sprintf("%s%s/%d/", h.jenkinsURL, jenkinsJobPath("deploy-input"), num)

	waitForPendingInput(t, buildURL)
	t.Cleanup(func() { abortBuild(t, buildURL) })

	stdout, stderr, err := h.run(t, "build", "input", buildURL, "proceed",
		"-p", "ENV=devvv")
	if err == nil {
		t.Fatalf("expected non-zero exit for invalid choice; stdout=%s stderr=%s", stdout, stderr)
	}
	// jk emits the validation error on stderr; the message MUST name
	// the offending parameter and list the valid choices so the user
	// can fix the invocation.
	if !strings.Contains(stderr, "ENV") {
		t.Errorf("expected parameter name in error, got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "staging") || !strings.Contains(stderr, "prod") {
		t.Errorf("expected valid choices in error, got:\n%s", stderr)
	}

	// Build MUST still be paused — invalid validation must NOT have
	// progressed it past the Approval stage.
	statusOut, _ := h.mustRun(t, "build", "status", buildURL)
	if !strings.Contains(statusOut, "state: PENDING_INPUT") {
		t.Errorf("build advanced past PENDING_INPUT despite local validation failure:\n%s", statusOut)
	}
}
