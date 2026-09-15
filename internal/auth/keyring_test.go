package auth

// Keyring-backed secure storage tests. These live in the internal test
// package (not auth_test) because the keyring primitives are deliberately
// unexported package vars: tests swap them for an in-memory fake via
// useMockKeyring, mirroring the jira-cli reference implementation.
//
// The real OS keyring is NEVER touched: every test installs the fake and
// restores the production wiring via t.Cleanup.

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// useMockKeyring replaces the package-level keyring hooks with an
// in-memory fake keyed by username (the host key). With fail=true every
// primitive errors, simulating a locked/unavailable keyring. The returned
// func restores the originals; tests defer it or register it via
// t.Cleanup.
func useMockKeyring(store map[string]string, fail bool) func() {
	origGet, origSet, origDelete := keyringGet, keyringSet, keyringDelete
	keyringGet = func(service, user string) (string, error) {
		if fail {
			return "", errors.New("keyring unavailable")
		}
		value, ok := store[user]
		if !ok {
			return "", keyring.ErrNotFound
		}
		return value, nil
	}
	keyringSet = func(service, user, password string) error {
		if fail {
			return errors.New("keyring unavailable")
		}
		store[user] = password
		return nil
	}
	keyringDelete = func(service, user string) error {
		delete(store, user)
		return nil
	}
	return func() { keyringGet, keyringSet, keyringDelete = origGet, origSet, origDelete }
}

// newKeyringStore is the local counterpart of store_test.go's newStore;
// it cannot be reused because that helper lives in the external test
// package.
func newKeyringStore(t *testing.T) (Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials")
	s, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	return s, path
}

// mustParseURL parses raw or fails the test; Resolve takes a *url.URL.
// Local copy of the store_test.go helper (external test package).
func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", raw, err)
	}
	return u
}

// secureHostsOf calls the optional SecureHosts capability on a Store,
// failing the test if the implementation does not provide it.
func secureHostsOf(t *testing.T, s Store) []string {
	t.Helper()
	sl, ok := s.(interface{ SecureHosts() ([]string, error) })
	if !ok {
		t.Fatal("store does not implement SecureHosts")
	}
	hosts, err := sl.SecureHosts()
	if err != nil {
		t.Fatalf("SecureHosts: %v", err)
	}
	return hosts
}

// Test_Keyring_SecureRoundTrip covers the full secure lifecycle: add with
// Secure=true must keep the token out of the TOML file, Get/Resolve must
// transparently hydrate it from the keyring, SecureHosts must report the
// host, and Remove must clean up the keyring entry.
func Test_Keyring_SecureRoundTrip(t *testing.T) {
	mock := map[string]string{}
	t.Cleanup(useMockKeyring(mock, false))
	s, path := newKeyringStore(t)

	host := "https://jenkins.example.com"
	if err := s.Add(host, Credential{Username: "alice", Token: "sekret", Secure: true}); err != nil {
		t.Fatalf("Add secure: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(raw), "sekret") {
		t.Fatalf("token leaked to credentials file:\n%s", raw)
	}
	if !strings.Contains(string(raw), "secure = true") {
		t.Errorf("credentials file missing secure marker:\n%s", raw)
	}

	// Fresh store instance: state must come from the file + keyring.
	s2, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore #2: %v", err)
	}
	got, ok, err := s2.Get(host)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.Token != "sekret" || got.Username != "alice" || !got.Secure {
		t.Errorf("Get = %+v, want hydrated secure credential", got)
	}
	_, cred, ok, err := s2.Resolve(mustParseURL(t, host+"/job/svc/7/"))
	if err != nil || !ok {
		t.Fatalf("Resolve: ok=%v err=%v", ok, err)
	}
	if cred.Token != "sekret" {
		t.Errorf("Resolve token = %q, want %q", cred.Token, "sekret")
	}

	secureHosts := secureHostsOf(t, s2)
	if len(secureHosts) != 1 || secureHosts[0] != host {
		t.Errorf("SecureHosts = %v, want [%s]", secureHosts, host)
	}

	// Removing the credential must remove the keyring entry too.
	if err := s2.Remove(host); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, present := mock[host]; present {
		t.Error("keyring entry was not deleted by Remove")
	}
}

// Test_Keyring_SecureAddFailure_KeepsFileUntouched asserts the ordering
// guarantee: the token goes to the keyring BEFORE the file is written, so
// an unavailable keyring fails the Add without creating (or modifying)
// the credentials file, and the error suggests the file-storage fallback.
func Test_Keyring_SecureAddFailure_KeepsFileUntouched(t *testing.T) {
	t.Cleanup(useMockKeyring(map[string]string{}, true))
	s, path := newKeyringStore(t)

	host := "https://jenkins.example.com"
	err := s.Add(host, Credential{Username: "alice", Token: "sekret", Secure: true})
	if err == nil {
		t.Fatal("expected error when the keyring is unavailable")
	}
	if !strings.Contains(err.Error(), "without --secure-storage") || !strings.Contains(err.Error(), "credentials file") {
		t.Errorf("error lacks file-storage fallback hint: %v", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("credentials file should not exist, got %v", statErr)
	}
}

// Test_Keyring_MissingToken_IsActionable covers a file that says secure
// but has no keyring entry (out-of-band deletion, machine migration):
// both Get and Resolve must fail with a message telling the user how to
// re-store the token.
func Test_Keyring_MissingToken_IsActionable(t *testing.T) {
	mock := map[string]string{}
	t.Cleanup(useMockKeyring(mock, false))
	s, _ := newKeyringStore(t)

	host := "https://jenkins.example.com"
	// Seed the file directly via Add, then drop the keyring entry to
	// simulate out-of-band deletion.
	if err := s.Add(host, Credential{Username: "alice", Token: "sekret", Secure: true}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	delete(mock, host)

	_, ok, err := s.Get(host)
	if err == nil {
		t.Fatal("expected error for missing keyring token on Get")
	}
	if !ok {
		t.Error("Get should still report ok=true; the file entry exists")
	}
	if !strings.Contains(err.Error(), "no token for "+host+" in the OS keyring") || !strings.Contains(err.Error(), "--secure-storage") {
		t.Errorf("Get error lacks re-add hint: %v", err)
	}

	_, _, _, err = s.Resolve(mustParseURL(t, host+"/job/svc/"))
	if err == nil {
		t.Fatal("expected error for missing keyring token on Resolve")
	}
	if !strings.Contains(err.Error(), "--secure-storage") {
		t.Errorf("Resolve error lacks re-add hint: %v", err)
	}
}

// Test_Keyring_KeyringError_IsActionable covers non-NotFound keyring
// failures (locked keychain, headless Linux without a secret service):
// reads must suggest falling back to file storage.
func Test_Keyring_KeyringError_IsActionable(t *testing.T) {
	t.Cleanup(useMockKeyring(map[string]string{}, false))
	s, _ := newKeyringStore(t)

	host := "https://jenkins.example.com"
	if err := s.Add(host, Credential{Username: "alice", Token: "sekret", Secure: true}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	t.Cleanup(useMockKeyring(map[string]string{}, true)) // keyring now "broken" for reads

	_, _, err := s.Get(host)
	if err == nil {
		t.Fatal("expected keyring read error")
	}
	if !strings.Contains(err.Error(), "without --secure-storage") {
		t.Errorf("Get error lacks file-storage fallback hint: %v", err)
	}
}

// Test_Keyring_FileReAdd_OverSecure_CleansKeyring asserts that re-adding
// a host with file storage migrates it off the keyring: the stale keyring
// entry is deleted, the file carries the plaintext token again, and the
// host disappears from SecureHosts.
func Test_Keyring_FileReAdd_OverSecure_CleansKeyring(t *testing.T) {
	mock := map[string]string{}
	t.Cleanup(useMockKeyring(mock, false))
	s, path := newKeyringStore(t)

	host := "https://jenkins.example.com"
	if err := s.Add(host, Credential{Username: "alice", Token: "sekret", Secure: true}); err != nil {
		t.Fatalf("Add secure: %v", err)
	}
	if err := s.Add(host, Credential{Username: "alice", Token: "plain"}); err != nil {
		t.Fatalf("Add file: %v", err)
	}
	if len(mock) != 0 {
		t.Errorf("keyring entry was not cleaned up: %v", mock)
	}

	s2, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore #2: %v", err)
	}
	got, ok, err := s2.Get(host)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.Token != "plain" || got.Secure {
		t.Errorf("Get = %+v, want file-storage credential with token \"plain\"", got)
	}
	secureHosts := secureHostsOf(t, s2)
	if len(secureHosts) != 0 {
		t.Errorf("SecureHosts = %v, want empty", secureHosts)
	}
}
