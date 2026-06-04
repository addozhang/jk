//go:build e2e

package e2e

// Context-path e2e: drive jk against a Jenkins instance exposed beneath a
// /jenkins context path (via the nginx mount defined in
// test/e2e/nginx/nginx.conf). This is the end-to-end proof for the
// parse-context-path-prefix change: a single `jk build status` run
// exercises the whole chain that the unit tests cover in isolation —
//
//   1. the URL parser accepts the context path instead of rejecting it
//      with "no /job/ segments found";
//   2. APIPath preserves the /jenkins prefix on the outgoing request, so
//      nginx routes it (a dropped prefix would 404);
//   3. the credential resolves by host only, so the /jenkins path does
//      not leak into the lookup key (a mis-key would 401).
//
// The hello build #1 is warmed by setupHarness against the root URL; the
// same build is what we read back through the context-path mount here.

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Test_E2E_ContextPath_BuildStatus reads hello build #1 through the
// /jenkins context-path mount and asserts the build is reported as a
// successful, completed build — which is only possible if jk parsed the
// context-path URL, kept the prefix on the wire, and authenticated.
func Test_E2E_ContextPath_BuildStatus(t *testing.T) {
	waitForContextPath(t)

	url := contextPathBaseURL + "/job/hello/1/"
	stdout, _ := h.mustRun(t, "build", "status", url)
	for _, want := range []string{
		`schemaVersion: "1"`,
		"buildNumber: 1",
		"state: DONE",
		"result: SUCCESS",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q in build status through context path:\n%s", want, stdout)
		}
	}
}

// Test_E2E_ContextPath_Permalink resolves a permalink through the
// context-path mount. Permalink resolution issues its own GET against
// <base>/<path>/lastBuild/api/json, so this confirms jk reconstructs the
// API path with the /jenkins prefix on the resolution request too, not
// just on the final read. The resolved build number is volume-dependent
// (lastBuild moves as builds accumulate), so we assert on the completed
// successful shape rather than a specific number — Test_E2E_ContextPath_
// BuildStatus already pins the deterministic numeric case.
func Test_E2E_ContextPath_Permalink(t *testing.T) {
	waitForContextPath(t)

	url := contextPathBaseURL + "/job/hello/lastBuild/"
	stdout, _ := h.mustRun(t, "build", "status", url)
	for _, want := range []string{
		`schemaVersion: "1"`,
		"state: DONE",
		"result: SUCCESS",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q in permalink status through context path:\n%s", want, stdout)
		}
	}
	// The resolved build number must surface as a positive integer
	// (proving the permalink resolved to a concrete build via the
	// context-path API request), even though its exact value varies.
	if !strings.Contains(stdout, "buildNumber: ") || strings.Contains(stdout, "buildNumber: 0\n") {
		t.Errorf("expected a resolved positive buildNumber in permalink status:\n%s", stdout)
	}
}

// waitForContextPath blocks until the nginx /jenkins mount answers so the
// tests do not race nginx container startup (nginx starts after Jenkins
// becomes healthy). /jenkins/login strips to /login on the root Jenkins
// and returns 200 once the proxy is live. Bounded; fails fast with the
// last observed error.
func waitForContextPath(t *testing.T) {
	t.Helper()
	client := &http.Client{Timeout: 3 * time.Second}
	deadline := time.Now().Add(30 * time.Second)
	last := "no attempt made"
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, contextPathBaseURL+"/login", nil)
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
			last = resp.Status
		} else {
			last = err.Error()
		}
		time.Sleep(1 * time.Second)
	}
	t.Fatalf("context-path mount %s not ready: %s", contextPathBaseURL, last)
}
