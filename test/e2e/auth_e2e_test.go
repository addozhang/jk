//go:build e2e

package e2e

// Auth e2e: drive jk auth add/list/remove against the seeded harness.
// These tests exercise the credentials store roundtrip end-to-end via
// the real `jk` binary; the harness's seeded HOME is mutated, so each
// test cleans up after itself to avoid leaking state.

import (
	"os/exec"
	"strings"
	"testing"
)

// Test_E2E_AuthList shows the seeded admin host (added by setupHarness).
func Test_E2E_AuthList(t *testing.T) {
	stdout, stderr := h.mustRun(t, "auth", "list")
	if !strings.HasPrefix(stdout, `schemaVersion: "1"`) {
		t.Errorf("missing schemaVersion in stdout: %q", stdout)
	}
	if !strings.Contains(stdout, h.jenkinsURL) {
		t.Errorf("seeded host %s not listed:\nstdout:\n%s\nstderr:\n%s",
			h.jenkinsURL, stdout, stderr)
	}
}

// Test_E2E_AuthAddRemove adds a throwaway host, confirms it appears in
// list, removes it, and confirms it is gone. Done in one test so the
// harness's seeded credentials are restored on test exit.
func Test_E2E_AuthAddRemove(t *testing.T) {
	throwaway := "https://throwaway.example.com"

	// `jk auth add` is interactive; we drive its stdin by invoking
	// the binary with a piped Stdin. The harness's run() helper does
	// not pipe stdin (most commands don't need it), so we do the
	// exec ourselves here.
	cmd := exec.Command(h.binPath, "auth", "add", "--force", throwaway)
	cmd.Env = append(cmd.Environ(), "HOME="+h.homeDir)
	cmd.Stdin = strings.NewReader("e2e-user\ne2e-token\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("auth add: %v\n%s", err, out)
	}

	stdout, _ := h.mustRun(t, "auth", "list")
	if !strings.Contains(stdout, throwaway) {
		t.Errorf("throwaway host not present after add:\n%s", stdout)
	}

	if _, _, err := h.run(t, "auth", "remove", throwaway); err != nil {
		t.Fatalf("auth remove: %v", err)
	}

	stdout2, _ := h.mustRun(t, "auth", "list")
	if strings.Contains(stdout2, throwaway) {
		t.Errorf("throwaway host still present after remove:\n%s", stdout2)
	}
}
