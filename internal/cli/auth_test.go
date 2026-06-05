package cli

// E2e tests for `jk auth {add|list|remove}`. Mirror pipeline_test.go:
// every test redirects HOME to t.TempDir() and drives the root command.
//
// `add` prompts for both username (plain) and token (no-echo on TTY).
// Production uses golang.org/x/term against os.Stdin; tests override
// the package-level `readSecret` hook to consume from a bytes.Buffer
// so we never need a real PTY.

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/addozhang/jk/internal/auth"
)

// withStubReadSecret installs a test-only readSecret that reads a single
// newline-delimited line from the shared bufio.Reader. It is restored
// via t.Cleanup.
func withStubReadSecret(t *testing.T) {
	t.Helper()
	prev := readSecret
	readSecret = func(_ string, in *bufio.Reader, _ io.Writer) (string, error) {
		line, err := in.ReadString('\n')
		return strings.TrimRight(line, "\r\n"), err
	}
	t.Cleanup(func() { readSecret = prev })
}

// runJKWithStdin is runJK with an attached stdin reader.
func runJKWithStdin(t *testing.T, args []string, stdin string) (stdout, stderr string, err error) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	withStubReadSecret(t)

	root := NewRootCommand()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = root.ExecuteContext(ctx)
	return out.String(), errBuf.String(), err
}

// credentialsPathForTest derives the same path defaultCredentialsPath()
// would produce given $HOME.
func credentialsPathForTest(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	return filepath.Join(home, ".config", "jk", "credentials")
}

// Spec: "First-time credential addition". File is created chmod 0600
// with the host entry stored.
func Test_AuthAdd_FirstTimeWritesCredential(t *testing.T) {
	// Stdin shape: username line + token line. Production prompts
	// username via plain stdin read, token via readSecret; the test
	// stub consumes both lines from the same Reader.
	stdin := "alice\ns3cret-token\n"
	_, stderr, err := runJKWithStdin(t, []string{"auth", "add", "https://jenkins.example.com"}, stdin)
	if err != nil {
		t.Fatalf("auth add: %v\nstderr: %s", err, stderr)
	}
	// Confirmation goes to stderr (docs/schema.md §3.2: no structured
	// output for add/remove).
	if !strings.Contains(stderr, "https://jenkins.example.com") {
		t.Errorf("expected confirmation mentioning host, got: %q", stderr)
	}

	path := credentialsPathForTest(t)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("credentials file not created: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("credentials file mode = %o, want 0600", info.Mode().Perm())
	}

	store, err := auth.NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	c, ok, err := store.Get("https://jenkins.example.com")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if c.Username != "alice" || c.Token != "s3cret-token" {
		t.Errorf("stored credential = %+v, want {alice s3cret-token}", c)
	}
}

// Spec: "Host-only normalization (no context path)". Trailing slash and
// any /job/ hierarchy collapse to the bare host; a non-default port is
// preserved. Context-path retention is covered by Test_normalizeAuthHost
// and Test_AuthAdd_ContextPath_RetainsKeyAndEchoes.
func Test_AuthAdd_HostNormalization(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
	}{
		{"https://jenkins.example.com/", "https://jenkins.example.com"},
		{"https://jenkins.example.com/job/foo/", "https://jenkins.example.com"},
		{"https://jenkins.example.com:8443/job/x", "https://jenkins.example.com:8443"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			_, _, err := runJKWithStdin(t, []string{"auth", "add", tc.input}, "u\nt\n")
			if err != nil {
				t.Fatalf("auth add %s: %v", tc.input, err)
			}
			store, _ := auth.NewFileStore(credentialsPathForTest(t))
			if _, ok, _ := store.Get(tc.want); !ok {
				hosts, _ := store.List()
				t.Errorf("expected host %q in store, got %v", tc.want, hosts)
			}
		})
	}
}

// Test_normalizeAuthHost exercises the base-path capture rule directly
// (design.md D3, auth/spec.md "Add credentials for a Jenkins host"): the
// stored key is scheme + host + optional non-default port, plus the path
// prefix before the first `/job/` segment (or the whole path when there is
// no `/job/`), normalized to a leading `/` with no trailing slash.
func Test_normalizeAuthHost(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"bare host", "https://jenkins.example.com", "https://jenkins.example.com"},
		{"trailing slash stays host-only", "https://jenkins.example.com/", "https://jenkins.example.com"},
		{"job hierarchy stays host-only", "https://jenkins.example.com/job/foo/", "https://jenkins.example.com"},
		{"non-default port preserved", "https://jenkins.example.com:8443/job/x", "https://jenkins.example.com:8443"},
		{"single context segment retained", "https://ci.example.com/team-a", "https://ci.example.com/team-a"},
		{"context segment trailing slash normalized", "https://ci.example.com/team-a/", "https://ci.example.com/team-a"},
		{"context path before job hierarchy retained", "https://ci.example.com/team-a/job/svc/", "https://ci.example.com/team-a"},
		{"multi-segment context path retained", "https://ci.example.com/team/ci", "https://ci.example.com/team/ci"},
		{"non-job path retained verbatim", "https://jenkins.example.com:8443/anything", "https://jenkins.example.com:8443/anything"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeAuthHost(tc.in)
			if err != nil {
				t.Fatalf("normalizeAuthHost(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("normalizeAuthHost(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Spec: "Context-path instance credential addition". The stored key retains
// the context path, the confirmation names that full key, and a sibling
// instance on the same host is left untouched.
func Test_AuthAdd_ContextPath_RetainsKeyAndEchoes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	withStubReadSecret(t)

	path := credentialsPathForTest(t)
	store, err := auth.NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	// Pre-seed a sibling instance on the same host; it must survive.
	if err := store.Add("https://ci.example.com/team-b", auth.Credential{Username: "bob", Token: "tok-b"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	root := NewRootCommand()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetIn(strings.NewReader("alice\ntok-a\n"))
	// Job hierarchy after the context path must be stripped, leaving /team-a.
	root.SetArgs([]string{"auth", "add", "https://ci.example.com/team-a/job/svc/"})
	if err := root.Execute(); err != nil {
		t.Fatalf("auth add: %v\nstderr: %s", err, errBuf.String())
	}

	if !strings.Contains(errBuf.String(), "https://ci.example.com/team-a") {
		t.Errorf("confirmation does not name the context-path key: %q", errBuf.String())
	}
	if c, ok, _ := store.Get("https://ci.example.com/team-a"); !ok || c.Token != "tok-a" {
		t.Errorf("team-a not stored under context-path key: ok=%v c=%+v", ok, c)
	}
	if c, ok, _ := store.Get("https://ci.example.com/team-b"); !ok || c.Token != "tok-b" {
		t.Errorf("sibling team-b entry was disturbed: ok=%v c=%+v", ok, c)
	}
}

// `auth remove` must normalize its argument the same way `auth add` does, so
// removing via a job URL still targets the stored context-path key.
func Test_AuthRemove_ContextPathKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, _ := auth.NewFileStore(credentialsPathForTest(t))
	if err := store.Add("https://ci.example.com/team-a", auth.Credential{Username: "u", Token: "t"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	root := NewRootCommand()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"auth", "remove", "https://ci.example.com/team-a/job/svc/"})
	if err := root.Execute(); err != nil {
		t.Fatalf("auth remove: %v\nstderr: %s", err, errBuf.String())
	}
	if _, ok, _ := store.Get("https://ci.example.com/team-a"); ok {
		t.Errorf("context-path entry still present after remove")
	}
}

// Spec: "Overwriting an existing credential". Without --force the
// command MUST error; with --force the overwrite proceeds.
func Test_AuthAdd_OverwriteRequiresForce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	withStubReadSecret(t)

	path := credentialsPathForTest(t)
	store, err := auth.NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if err := store.Add("https://jenkins.example.com", auth.Credential{Username: "old", Token: "old-tok"}); err != nil {
		t.Fatalf("seed Add: %v", err)
	}

	root := NewRootCommand()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetIn(strings.NewReader("alice\nnew-tok\n"))
	root.SetArgs([]string{"auth", "add", "https://jenkins.example.com"})
	if err := root.Execute(); err == nil {
		t.Fatalf("expected error without --force, got nil; stderr=%s", errBuf.String())
	}
	c, _, _ := store.Get("https://jenkins.example.com")
	if c.Token != "old-tok" {
		t.Errorf("credential overwritten without --force: %+v", c)
	}

	root2 := NewRootCommand()
	out.Reset()
	errBuf.Reset()
	root2.SetOut(&out)
	root2.SetErr(&errBuf)
	root2.SetIn(strings.NewReader("alice\nnew-tok\n"))
	root2.SetArgs([]string{"auth", "add", "--force", "https://jenkins.example.com"})
	if err := root2.Execute(); err != nil {
		t.Fatalf("auth add --force: %v\nstderr: %s", err, errBuf.String())
	}
	c2, _, _ := store.Get("https://jenkins.example.com")
	if c2.Token != "new-tok" {
		t.Errorf("credential not overwritten with --force: %+v", c2)
	}
}

// Spec: "Listing configured hosts". stdout in YAML w/ schemaVersion +
// hosts in insertion order. Tokens MUST NOT appear.
func Test_AuthList_RendersHostsInOrder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, err := auth.NewFileStore(credentialsPathForTest(t))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	for _, h := range []string{"https://a.example.com", "https://b.example.com"} {
		if err := store.Add(h, auth.Credential{Username: "u", Token: "t-XXXXX"}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// Do not use runJK here: runJK rewrites HOME, which would point
	// the auth list at an unseeded temp dir.
	root := NewRootCommand()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"auth", "list"})
	if err := root.Execute(); err != nil {
		t.Fatalf("auth list: %v\nstderr: %s", err, errBuf.String())
	}
	stdout := out.String()
	if !strings.HasPrefix(stdout, `schemaVersion: "1"`) {
		t.Errorf("missing schemaVersion: %q", stdout)
	}
	idxA := strings.Index(stdout, "https://a.example.com")
	idxB := strings.Index(stdout, "https://b.example.com")
	if idxA < 0 || idxB < 0 || idxA >= idxB {
		t.Errorf("hosts not in insertion order:\n%s", stdout)
	}
	if strings.Contains(stdout, "t-XXXXX") {
		t.Errorf("token leaked to stdout:\n%s", stdout)
	}
}

// Spec: "No hosts configured". Missing file -> empty array, exit 0.
func Test_AuthList_EmptyStoreEmitsEmptyArray(t *testing.T) {
	stdout, _, err := runJK(t, []string{"auth", "list"})
	if err != nil {
		t.Fatalf("auth list: %v", err)
	}
	if !strings.Contains(stdout, "hosts: []") {
		t.Errorf("expected hosts: [], got:\n%s", stdout)
	}
}

// `auth remove` is task 5.6 (not in spec). Contract: removes existing
// entries and is idempotent on missing ones.
func Test_AuthRemove_RemovesEntryAndIsIdempotent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store, _ := auth.NewFileStore(credentialsPathForTest(t))
	if err := store.Add("https://x.example.com", auth.Credential{Username: "u", Token: "t"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	for _, args := range [][]string{
		{"auth", "remove", "https://x.example.com"}, // present
		{"auth", "remove", "https://x.example.com"}, // already gone
	} {
		root := NewRootCommand()
		var out, errBuf bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&errBuf)
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Fatalf("%v: %v\nstderr: %s", args, err, errBuf.String())
		}
	}

	if _, ok, _ := store.Get("https://x.example.com"); ok {
		t.Errorf("entry still present after remove")
	}
}
