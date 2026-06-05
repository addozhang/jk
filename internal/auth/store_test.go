package auth_test

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/addozhang/jk/internal/auth"
)

// newStore constructs a Store backed by a fresh temp file. The returned
// path is the credentials file path; the directory wraps it so the store
// can also enforce directory perms (0700 on POSIX).
func newStore(t *testing.T) (auth.Store, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials")
	s, err := auth.NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	return s, path
}

func Test_Store_AddThenGet_RoundTrips(t *testing.T) {
	s, _ := newStore(t)
	want := auth.Credential{Username: "alice", Token: "tok-123"}
	if err := s.Add("https://jenkins.example.com", want); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, ok, err := s.Get("https://jenkins.example.com")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("Get: expected ok=true")
	}
	if got != want {
		t.Errorf("Get = %+v, want %+v", got, want)
	}
}

func Test_Store_Get_MissingHost_ReturnsFalse(t *testing.T) {
	s, _ := newStore(t)
	_, ok, err := s.Get("https://nope.example.com")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Error("expected ok=false for missing host")
	}
}

func Test_Store_List_PreservesInsertionOrder(t *testing.T) {
	// docs/schema.md §3.1: hosts are returned "in insertion order".
	s, _ := newStore(t)
	hosts := []string{
		"https://a.example.com",
		"https://b.example.com",
		"https://c.example.com",
	}
	for _, h := range hosts {
		if err := s.Add(h, auth.Credential{Username: "u", Token: "t"}); err != nil {
			t.Fatalf("Add %s: %v", h, err)
		}
	}
	got, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !equal(got, hosts) {
		t.Errorf("List = %v, want %v (insertion order)", got, hosts)
	}
}

func Test_Store_List_EmptyStoreReturnsEmptySlice(t *testing.T) {
	s, _ := newStore(t)
	got, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got == nil {
		t.Error("List() returned nil; spec requires empty slice (so it marshals to [])")
	}
	if len(got) != 0 {
		t.Errorf("List = %v, want empty", got)
	}
}

func Test_Store_Add_OverwriteReplacesEntry(t *testing.T) {
	s, _ := newStore(t)
	host := "https://jenkins.example.com"
	if err := s.Add(host, auth.Credential{Username: "old", Token: "old-tok"}); err != nil {
		t.Fatalf("Add#1: %v", err)
	}
	if err := s.Add(host, auth.Credential{Username: "new", Token: "new-tok"}); err != nil {
		t.Fatalf("Add#2: %v", err)
	}
	got, _, err := s.Get(host)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Token != "new-tok" || got.Username != "new" {
		t.Errorf("got %+v, want overwritten credential", got)
	}
	// Overwrite must not duplicate the entry in List().
	list, _ := s.List()
	if len(list) != 1 {
		t.Errorf("List length = %d, want 1 after overwrite", len(list))
	}
}

func Test_Store_Remove_DeletesEntry(t *testing.T) {
	s, _ := newStore(t)
	host := "https://jenkins.example.com"
	if err := s.Add(host, auth.Credential{Username: "u", Token: "t"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.Remove(host); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	_, ok, err := s.Get(host)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ok {
		t.Error("Get returned ok=true after Remove")
	}
}

func Test_Store_Remove_MissingHost_IsNotAnError(t *testing.T) {
	s, _ := newStore(t)
	if err := s.Remove("https://does-not-exist.example.com"); err != nil {
		t.Errorf("Remove on missing host should be idempotent, got: %v", err)
	}
}

func Test_Store_File_Mode0600_OnPOSIX(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only: chmod is a no-op on Windows; see design.md")
	}
	s, path := newStore(t)
	if err := s.Add("https://jenkins.example.com", auth.Credential{Username: "u", Token: "t"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("file mode = %o, want 0600", mode)
	}
}

func Test_Store_ParentDir_Mode0700_OnPOSIX(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only")
	}
	// Build a store rooted in a fresh, non-existent nested path so the
	// store has to create the parent directory itself.
	tmp := t.TempDir()
	path := filepath.Join(tmp, "newdir", "credentials")
	s, err := auth.NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if err = s.Add("https://h", auth.Credential{Username: "u", Token: "t"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("Stat parent: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Errorf("dir mode = %o, want 0700", mode)
	}
}

func Test_Store_Persists_AcrossInstances(t *testing.T) {
	// Two distinct Store instances backed by the same file must observe
	// each other's writes — confirming we always read fresh state.
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials")
	s1, err := auth.NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s1.Add("https://h", auth.Credential{Username: "u", Token: "t"}); err != nil {
		t.Fatal(err)
	}
	s2, err := auth.NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := s2.Get("https://h")
	if err != nil || !ok {
		t.Fatalf("Get from second store: ok=%v err=%v", ok, err)
	}
	if got.Token != "t" {
		t.Errorf("got token %q, want \"t\"", got.Token)
	}
}

func Test_Store_AtomicWrite_NoPartialFileOnCrash(t *testing.T) {
	// We can't simulate a crash, but we can assert that the implementation
	// writes via tempfile + rename: after Add, there must be no stray
	// `*.tmp` siblings in the credentials directory.
	s, path := newStore(t)
	if err := s.Add("https://h", auth.Credential{Username: "u", Token: "t"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != filepath.Base(path) {
			t.Errorf("stray file left behind: %s", e.Name())
		}
	}
}
func Test_Store_InvalidHostKey_IsRejected(t *testing.T) {
	// The store accepts a pre-normalized HostKey from jenkinsurl.Ref.
	// Bare hostnames without scheme should not silently pass: the lookup
	// is keyed by scheme+host+port. Empty key MUST be rejected so we don't
	// produce an unparseable TOML entry.
	s, _ := newStore(t)
	err := s.Add("", auth.Credential{Username: "u", Token: "t"})
	if err == nil {
		t.Error("expected error for empty host key")
	}
}

func Test_Store_RoundTrip_PreservesSpecialCharactersInToken(t *testing.T) {
	// Tokens can contain symbols; ensure the TOML encoder/decoder doesn't
	// mangle them (especially backslash, double-quote, control chars).
	s, _ := newStore(t)
	want := auth.Credential{
		Username: "alice@corp",
		Token:    `pa$$"w\\rd` + "\t",
	}
	if err := s.Add("https://h", want); err != nil {
		t.Fatal(err)
	}
	// Force a fresh read.
	s2, err := auth.NewFileStore(getPath(s))
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := s2.Get("https://h")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got.Token, want.Token)
	}
}

// getPath is a hook for tests that need to construct a second Store on the
// same file as an existing one. We avoid leaking this on the public API by
// asserting against the unexported method via the package's test-only
// PathOf helper.
func getPath(s auth.Store) string { return auth.PathOf(s) }

func Test_NewFileStore_AcceptsExistingMalformedFile_WithClearError(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "credentials")
	if err := os.WriteFile(path, []byte("not valid toml = = ="), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := auth.NewFileStore(path)
	if err != nil {
		// Acceptable: surface at construction time.
		return
	}
	// Or: surface at first read.
	_, _, err = s.Get("https://anywhere")
	if err == nil {
		t.Error("expected error reading malformed credentials file")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Errorf("error should be a parse error, not NotExist: %v", err)
	}
}

// mustParseURL parses raw or fails the test; Resolve takes a *url.URL.
func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", raw, err)
	}
	return u
}

// Test_Store_Resolve exercises the segment-boundary longest-prefix match
// that backs credential resolution (design.md D2/D6, auth/spec.md "Look up
// credentials by hostname"). A host-only key (empty path) is the shortest
// valid prefix of any same-host URL, so legacy single-instance files keep
// resolving exactly as before.
func Test_Store_Resolve(t *testing.T) {
	credRoot := auth.Credential{Username: "root", Token: "tok-root"}
	credA := auth.Credential{Username: "alice", Token: "tok-a"}
	credB := auth.Credential{Username: "bob", Token: "tok-b"}
	credSeg := auth.Credential{Username: "carol", Token: "tok-seg"}

	seeds := []struct {
		key  string
		cred auth.Credential
	}{
		{"https://ci.example.com", credRoot},
		{"https://ci.example.com/team-a", credA},
		{"https://ci.example.com/team-b", credB},
		// seg.example.com deliberately has NO host-only entry so the
		// segment-boundary cases can assert a clean no-match.
		{"https://seg.example.com/team-a", credSeg},
	}

	cases := []struct {
		name    string
		reqURL  string
		wantKey string
		wantTok string
		wantOK  bool
	}{
		{
			name:    "host-only match when no context entry applies",
			reqURL:  "https://ci.example.com/job/x/api/json",
			wantKey: "https://ci.example.com",
			wantTok: "tok-root",
			wantOK:  true,
		},
		{
			name:    "most-specific context-path entry wins over host-only",
			reqURL:  "https://ci.example.com/team-a/job/svc/2/api/json",
			wantKey: "https://ci.example.com/team-a",
			wantTok: "tok-a",
			wantOK:  true,
		},
		{
			name:    "sibling context path selects its own entry",
			reqURL:  "https://ci.example.com/team-b/job/svc/build",
			wantKey: "https://ci.example.com/team-b",
			wantTok: "tok-b",
			wantOK:  true,
		},
		{
			name:    "fallback to host-only for an unknown context path",
			reqURL:  "https://ci.example.com/team-z/job/x/",
			wantKey: "https://ci.example.com",
			wantTok: "tok-root",
			wantOK:  true,
		},
		{
			name:    "exact-path match for a crumb endpoint (no /job/ delimiter)",
			reqURL:  "https://ci.example.com/team-a/crumbIssuer/api/json",
			wantKey: "https://ci.example.com/team-a",
			wantTok: "tok-a",
			wantOK:  true,
		},
		{
			name:    "exact path equality matches",
			reqURL:  "https://ci.example.com/team-a",
			wantKey: "https://ci.example.com/team-a",
			wantTok: "tok-a",
			wantOK:  true,
		},
		{
			name:    "segment-boundary prefix still matches at the boundary",
			reqURL:  "https://seg.example.com/team-a/job/x/",
			wantKey: "https://seg.example.com/team-a",
			wantTok: "tok-seg",
			wantOK:  true,
		},
		{
			name:   "segment boundary: /team-a must not match /team-amber",
			reqURL: "https://seg.example.com/team-amber/job/x/",
			wantOK: false,
		},
		{
			name:   "no host match returns ok=false",
			reqURL: "https://nope.example.com/job/x/",
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newStore(t)
			for _, sd := range seeds {
				if err := s.Add(sd.key, sd.cred); err != nil {
					t.Fatalf("seed Add %q: %v", sd.key, err)
				}
			}
			key, got, ok, err := s.Resolve(mustParseURL(t, tc.reqURL))
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if ok != tc.wantOK {
				t.Fatalf("Resolve ok = %v, want %v (key=%q)", ok, tc.wantOK, key)
			}
			if !tc.wantOK {
				return
			}
			if key != tc.wantKey {
				t.Errorf("Resolve key = %q, want %q", key, tc.wantKey)
			}
			if got.Token != tc.wantTok {
				t.Errorf("Resolve token = %q, want %q", got.Token, tc.wantTok)
			}
		})
	}
}

// Test_Store_Resolve_EmptyStore confirms a request against a store with no
// entries reports no match rather than erroring.
func Test_Store_Resolve_EmptyStore(t *testing.T) {
	s, _ := newStore(t)
	_, _, ok, err := s.Resolve(mustParseURL(t, "https://ci.example.com/job/x/"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ok {
		t.Error("expected ok=false for empty store")
	}
}

// Test_Store_Resolve_StripsDefaultPort verifies a request carrying the
// scheme's default port resolves a credential stored without the port, so
// the transport's host normalization is honored inside the store.
func Test_Store_Resolve_StripsDefaultPort(t *testing.T) {
	s, _ := newStore(t)
	if err := s.Add("https://ci.example.com/team-a", auth.Credential{Username: "u", Token: "t"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	key, _, ok, err := s.Resolve(mustParseURL(t, "https://ci.example.com:443/team-a/job/x/"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !ok || key != "https://ci.example.com/team-a" {
		t.Errorf("Resolve = (%q, ok=%v), want (https://ci.example.com/team-a, true)", key, ok)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
