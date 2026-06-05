//go:build e2e

package e2e

// Context-path credential e2e: the end-to-end proof for the
// auth-context-path-credentials change. Unlike contextpath_e2e_test.go
// (which seeds a single host-only credential and proves URL/prefix
// handling), this test seeds THREE credentials in an isolated $HOME to
// prove the resolver picks the right one:
//
//   1. a VALID credential keyed to the /jenkins context path;
//   2. a DECOY host-only credential for the same host:port carrying a
//      deliberately wrong token;
//   3. a VALID host-only credential for the root Jenkins (different port).
//
// A `jk build status` through /jenkins must authenticate using (1): if the
// resolver wrongly fell back to the host-only decoy (2), Jenkins would
// answer 401 and the run would fail. A second `jk build status` against the
// root URL must still resolve the host-only credential (3), proving the
// host-only fallback survives.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/addozhang/jk/internal/auth"
)

func Test_E2E_ContextPath_ScopedCredentialResolves(t *testing.T) {
	waitForContextPath(t)

	home := t.TempDir()
	credPath := filepath.Join(home, ".config", "jk", "credentials")
	store, err := auth.NewFileStore(credPath)
	if err != nil {
		t.Fatalf("open isolated store: %v", err)
	}

	// (1) Context-path-scoped credential: the VALID admin secret, keyed
	// to the /jenkins mount. This is the entry resolution must select for
	// a /jenkins/... request.
	if err := store.Add(contextPathBaseURL, auth.Credential{Username: h.jenkinsUser, Token: h.secret}); err != nil {
		t.Fatalf("seed context-path credential: %v", err)
	}
	// (2) Host-only DECOY for the same host:port, with a wrong token. If
	// the resolver ignores the context path and falls back here, Jenkins
	// returns 401 and the build-status run fails — so a green result is
	// proof the /jenkins key was chosen over this less-specific entry.
	if err := store.Add(contextPathHost, auth.Credential{Username: h.jenkinsUser, Token: "wrong-token-must-not-be-used"}); err != nil {
		t.Fatalf("seed host-only decoy credential: %v", err)
	}
	// (3) Root host-only credential (different host:port) with the VALID
	// secret, to prove plain host-only resolution still works.
	if err := store.Add(h.jenkinsURL, auth.Credential{Username: h.jenkinsUser, Token: h.secret}); err != nil {
		t.Fatalf("seed root host-only credential: %v", err)
	}

	// 1. Context-path request must resolve the /jenkins-scoped credential.
	out := runWithHome(t, home, "build", "status", contextPathBaseURL+"/job/hello/1/")
	for _, want := range []string{"buildNumber: 1", "state: DONE", "result: SUCCESS"} {
		if !strings.Contains(out, want) {
			t.Errorf("context-path-scoped credential did not resolve; missing %q:\n%s", want, out)
		}
	}

	// 2. Root request must still resolve the host-only credential.
	rootOut := runWithHome(t, home, "build", "status", h.jenkinsURL+"/job/hello/1/")
	for _, want := range []string{"buildNumber: 1", "state: DONE", "result: SUCCESS"} {
		if !strings.Contains(rootOut, want) {
			t.Errorf("host-only credential did not resolve for root request; missing %q:\n%s", want, rootOut)
		}
	}
}

// runWithHome runs the jk binary with an isolated HOME (so each test can
// stage its own credential topology) and fails on non-zero exit, returning
// combined stdout+stderr.
func runWithHome(t *testing.T, home string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, h.binPath, args...)
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("jk %s\nexit: %v\noutput:\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
