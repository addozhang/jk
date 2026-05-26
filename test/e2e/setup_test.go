//go:build e2e

// Package e2e drives the compiled `jk` binary against a real Jenkins
// instance (the docker-compose harness under ./test/e2e). These tests
// are gated by the `e2e` build tag so `go test ./...` does not require
// docker to be running.
//
// Run:
//
//	make e2e-up       # build image, start Jenkins, wait for ready
//	make test-e2e     # go test -tags=e2e ./test/e2e/...
//	make e2e-down     # stop and remove
//
// The harness deliberately treats jk as a black-box CLI: each test
// shells out to the built binary, captures stdout/stderr, and asserts
// on the textual response. This mirrors what a user sees and exercises
// the full code path including flag parsing, output rendering, and
// process exit codes.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/addozhang/jk/internal/auth"
)

// harness bundles everything an e2e test needs: the absolute path to the
// freshly-built `jk` binary, the Jenkins base URL, and a temp $HOME with
// pre-seeded credentials so commands authenticate without prompting.
//
// One harness is built per `go test` invocation (in TestMain) and shared
// across all tests; nothing here is mutable per-test except the per-test
// stdout/stderr buffers returned from runJK.
type harness struct {
	binPath     string
	jenkinsURL  string
	jenkinsUser string
	// secret is what gets stored in the jk credentials file and sent
	// via HTTP Basic auth. For the harness this is the admin password
	// rather than an API token — Jenkins accepts both interchangeably
	// and the apitoken-property plugin has no public API to inject a
	// known-plaintext token. See test/e2e/jenkins/jcasc/jenkins.yaml.
	secret  string
	homeDir string
}

// h is the package-global harness initialized by TestMain. Using a
// package var (rather than constructing per-test) keeps the test files
// readable: each test starts with `out, err := h.run(t, "pipeline", "info", url)`.
var h *harness

// Defaults match test/e2e/.env so a developer can `make e2e-up &&
// make test-e2e` without exporting anything. CI may override via env.
const (
	defaultURL    = "http://localhost:18080"
	defaultUser   = "admin"
	defaultSecret = "admin-password"
)

// envOr returns the named env var or fallback when unset/empty.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func TestMain(m *testing.M) {
	// Run setup in its own scope so the context's cancel runs before
	// os.Exit (which would otherwise skip the deferred cleanup).
	code := func() int {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		hh, err := setupHarness(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "e2e setup failed: %v\n", err)
			return 2
		}
		h = hh
		return m.Run()
	}()
	os.Exit(code)
}

// setupHarness builds the jk binary, waits for Jenkins to answer, seeds
// the credentials file, and warms up the build history. It is the only
// place the e2e suite shells out to `go build`; per-test cost is just
// `exec.Command(h.binPath, ...)`.
func setupHarness(ctx context.Context) (*harness, error) {
	url := envOr("JK_E2E_URL", defaultURL)
	user := envOr("JK_E2E_USER", defaultUser)
	secret := envOr("JK_E2E_SECRET", defaultSecret)

	tmpHome, err := os.MkdirTemp("", "jk-e2e-home-*")
	if err != nil {
		return nil, fmt.Errorf("create temp home: %w", err)
	}

	// Build jk into the temp HOME's bin dir so the test cannot be
	// surprised by a stale binary on PATH. Output is silenced unless
	// build fails; failures dump the toolchain's stderr verbatim.
	binDir := filepath.Join(tmpHome, "bin")
	if err = os.MkdirAll(binDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir bin: %w", err)
	}
	binPath := filepath.Join(binDir, "jk")
	if err = buildBinary(ctx, binPath); err != nil {
		return nil, err
	}

	// Seed credentials at <tmpHome>/.config/jk/credentials so the
	// jk subprocess (which we launch with HOME=tmpHome) finds the
	// admin user/token without an interactive `auth add`. This is
	// the same path defaultCredentialsPath() resolves to.
	credPath := filepath.Join(tmpHome, ".config", "jk", "credentials")
	store, err := auth.NewFileStore(credPath)
	if err != nil {
		return nil, fmt.Errorf("open seed store: %w", err)
	}
	if err := store.Add(url, auth.Credential{Username: user, Token: secret}); err != nil {
		return nil, fmt.Errorf("seed credentials: %w", err)
	}

	if err := waitForJenkins(ctx, url, user, secret); err != nil {
		return nil, fmt.Errorf("wait for jenkins: %w", err)
	}
	if err := warmBuildHistory(ctx, url, user, secret); err != nil {
		return nil, fmt.Errorf("warm build history: %w", err)
	}

	return &harness{
		binPath:     binPath,
		jenkinsURL:  url,
		jenkinsUser: user,
		secret:      secret,
		homeDir:     tmpHome,
	}, nil
}

// buildBinary compiles `cmd/jk` into outPath. We invoke `go build`
// rather than reusing `go test -c` because the e2e suite drives the
// CLI as a separate process, not as in-process Go code.
func buildBinary(ctx context.Context, outPath string) error {
	repoRoot, err := findRepoRoot()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "go", "build", "-o", outPath, "./cmd/jk")
	cmd.Dir = repoRoot
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go build: %w\n%s", err, stderr.String())
	}
	return nil
}

// findRepoRoot walks upward from the test file's directory looking for
// go.mod. The e2e test binary's working directory is test/e2e/ when
// `go test` runs it; the repo root is two levels up but we walk to
// stay robust against future reorganizations.
func findRepoRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above %s", cwd)
		}
		dir = parent
	}
}

// waitForJenkins polls /api/json with basic auth until it returns 200
// or the context is cancelled. /api/json (not /login) is the
// authoritative readiness check because JCasC's user creation completes
// AFTER /login responds, and we need the seeded admin to be live.
func waitForJenkins(ctx context.Context, url, user, token string) error {
	client := &http.Client{Timeout: 5 * time.Second}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(2 * time.Minute)
	}
	var lastErr error
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api/json", nil)
		req.SetBasicAuth(user, token)
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("jenkins not ready: %w", lastErr)
}

// warmBuildHistory POSTs to /job/<name>/build for each seeded pipeline so
// the build-* e2e tests have at least one build to query. We then poll
// /lastBuild/api/json until the build leaves the "building" state.
//
// The wait is bounded by ctx; if a build hangs we let the test that
// depends on it surface the failure with a clearer error message rather
// than failing setup opaquely.
func warmBuildHistory(ctx context.Context, baseURL, user, token string) error {
	// Cookie jar is required: Jenkins' CSRF crumb is bound to the
	// HTTP session, so the crumb GET and the POST that uses it must
	// share JSESSIONID. Without this, the POST returns 403.
	jar, err := cookiejar.New(nil)
	if err != nil {
		return fmt.Errorf("cookiejar: %w", err)
	}
	client := &http.Client{Timeout: 30 * time.Second, Jar: jar}
	// jobsToTrigger maps job path → buildPath suffix. Parameterized
	// pipelines reject /build with 400 until their parameter set has
	// been registered (which happens on the first successful build);
	// we work around that by hitting /buildWithParameters from the
	// outset — Jenkins accepts the call even if no params are sent.
	jobsToTrigger := []struct {
		jobPath   string
		buildPath string
	}{
		{"hello", "/build"},
		{"params", "/buildWithParameters"},
		{"team/parallel", "/build"},
	}
	for _, j := range jobsToTrigger {
		if err := triggerOne(ctx, client, baseURL, j.jobPath, j.buildPath, user, token); err != nil {
			return fmt.Errorf("trigger %s: %w", j.jobPath, err)
		}
	}
	// Wait for builds individually so a slow one doesn't block the
	// others' kickoff.
	for _, j := range jobsToTrigger {
		if err := waitForBuild(ctx, client, baseURL, j.jobPath, user, token); err != nil {
			return fmt.Errorf("wait %s: %w", j.jobPath, err)
		}
	}
	return nil
}

// jenkinsJobPath converts a slash-separated job path like
// "team/parallel" into the Jenkins URL path "/job/team/job/parallel".
// Centralized so trigger/wait/test code all encode the same way.
func jenkinsJobPath(jobPath string) string {
	segs := strings.Split(jobPath, "/")
	out := make([]string, 0, len(segs)*2)
	for _, s := range segs {
		out = append(out, "job", s)
	}
	return "/" + strings.Join(out, "/")
}

func triggerOne(ctx context.Context, client *http.Client, base, jobPath, buildPath, user, token string) error {
	// Jenkins requires a CSRF crumb on POSTs when the standard crumb
	// issuer is enabled (which the harness JCasC turns on by default).
	// Fetching the crumb here keeps the harness independent of the
	// production transport's crumb cache; that path has its own unit
	// coverage in internal/jenkins/crumb_test.go.
	crumbField, crumbValue, err := fetchCrumb(ctx, client, base, user, token)
	if err != nil {
		return fmt.Errorf("fetch crumb: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+jenkinsJobPath(jobPath)+buildPath, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(user, token)
	if crumbField != "" {
		req.Header.Set(crumbField, crumbValue)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	// 201 Created (queued) is the documented success code; 200 is
	// accepted by some plugin versions. Anything else is fatal.
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}

// fetchCrumb hits /crumbIssuer/api/json to retrieve the current CSRF
// crumb. Returns ("", "", nil) when the crumb issuer is disabled (404)
// so callers can no-op cleanly.
func fetchCrumb(ctx context.Context, client *http.Client, base, user, token string) (field, value string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/crumbIssuer/api/json", nil)
	if err != nil {
		return "", "", err
	}
	req.SetBasicAuth(user, token)
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return "", "", nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("crumbIssuer status %d", resp.StatusCode)
	}
	// Tiny ad-hoc decoder to avoid pulling encoding/json types into
	// this file just for two fields.
	var body struct {
		Crumb             string `json:"crumb"`
		CrumbRequestField string `json:"crumbRequestField"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", "", fmt.Errorf("decode crumb: %w", err)
	}
	return body.CrumbRequestField, body.Crumb, nil
}

func waitForBuild(ctx context.Context, client *http.Client, base, jobPath, user, token string) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(2 * time.Minute)
	}
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+jenkinsJobPath(jobPath)+"/lastBuild/api/json?tree=building,result", nil)
		req.SetBasicAuth(user, token)
		resp, err := client.Do(req)
		if err == nil {
			// Trivial parse: look for `"building":false` in body.
			// Avoids a full json.Unmarshal here so the harness has
			// no extra type definitions.
			buf := new(bytes.Buffer)
			_, _ = io.Copy(buf, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK && strings.Contains(buf.String(), `"building":false`) {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("build of %s did not finish in time", jobPath)
}

// run executes the jk binary with HOME pointed at the seeded temp dir
// and a 30s deadline. Returns (stdout, stderr, error).
func (h *harness) run(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, h.binPath, args...)
	cmd.Env = append(os.Environ(), "HOME="+h.homeDir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// mustRun is run() but fails the test on non-zero exit. Most positive
// tests use this so the assertion section stays uncluttered.
func (h *harness) mustRun(t *testing.T, args ...string) (string, string) {
	t.Helper()
	stdout, stderr, err := h.run(t, args...)
	if err != nil {
		t.Fatalf("jk %s\nexit: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, stdout, stderr)
	}
	return stdout, stderr
}

// jobURL builds a Jenkins job URL from a slash-separated job path.
// e.g. ("team/parallel") -> "<base>/job/team/job/parallel/"
func (h *harness) jobURL(jobPath string) string {
	return h.jenkinsURL + jenkinsJobPath(jobPath) + "/"
}
