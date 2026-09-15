// keyring.go adds opt-in OS keyring storage to the file-backed credential
// store. The design mirrors the sibling jira-cli project: the token never
// touches the TOML file when a credential is marked Secure; only a marker
// (`secure = true`) is persisted, and every read resolves the token from
// the OS keyring on demand.
//
// Storage layout: the keyring entry is looked up under the service name
// `jk` and the username slot carries the exact stored credential key (the
// same `scheme://host[:port][/context-path]` string used as the TOML host
// table key), so a keyring entry maps 1:1 onto a credentials-file entry
// and Remove can clean it up without extra bookkeeping.
//
// The keyring primitives are package-level vars (keyringGet/keyringSet/
// keyringDelete) rather than direct calls: tests substitute them with an
// in-memory fake, and a future alternative backend only needs to rewire
// these three hooks. Production wiring is zalando/go-keyring.
package auth

import (
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

// keyringService is the service name under which jk stores tokens in the
// OS keyring. One service for all hosts; the host key disambiguates.
const keyringService = "jk"

// keyringGet/keyringSet/keyringDelete are the injectable keyring
// primitives. They exist so tests can replace the real OS keyring with an
// in-memory fake (see keyring_test.go); production code MUST NOT reassign
// them. Signatures match zalando/go-keyring's Get/Set/Delete.
var (
	keyringGet    = keyring.Get
	keyringSet    = keyring.Set
	keyringDelete = keyring.Delete
)

// tokenFor resolves the API token for a stored credential. Non-secure
// credentials carry the token inline; secure ones hold an empty Token and
// the real value is fetched from the OS keyring under the host key.
//
// Error policy (both branches are actionable, per design.md's "every
// error names the next step" rule):
//
//   - keyring.ErrNotFound: the file says secure storage, but the keyring
//     has no token for the host (e.g. the entry was deleted out-of-band,
//     or the file was copied to another machine). The user must re-add
//     with --secure-storage to repopulate the keyring.
//   - any other keyring error: the keyring itself is unavailable
//     (locked keychain, no D-Bus session on headless Linux). The user can
//     fall back to file storage by re-adding without --secure-storage.
func tokenFor(key string, c Credential) (string, error) {
	if !c.Secure {
		return c.Token, nil
	}
	token, err := keyringGet(keyringService, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", fmt.Errorf("auth: no token for %s in the OS keyring; re-run \"jk auth add %s --secure-storage\" to store it again", key, key)
	}
	if err != nil {
		return "", fmt.Errorf("auth: read the OS keyring for %s: %w; re-run \"jk auth add %s\" without --secure-storage to keep the token in the credentials file", key, err, key)
	}
	return token, nil
}

// storeTokenInKeyring writes the token for a host key into the OS keyring.
// Callers MUST invoke this before persisting a Secure credential to the
// file, so the file never advertises secure storage for a token that is
// not actually in the keyring yet.
func storeTokenInKeyring(key, token string) error {
	if err := keyringSet(keyringService, key, token); err != nil {
		return fmt.Errorf("auth: store the token for %s in the OS keyring: %w; re-run \"jk auth add %s\" without --secure-storage to keep the token in the credentials file", key, err, key)
	}
	return nil
}

// deleteTokenFromKeyring is best-effort keyring cleanup for Remove and
// overwrite paths. Failures (including a missing entry) are deliberately
// ignored: a stale keyring entry is a hygiene issue, not a reason to fail
// a Remove that already succeeded against the file.
func deleteTokenFromKeyring(key string) {
	_ = keyringDelete(keyringService, key)
}

// SecureHosts returns the hosts whose API tokens live in the OS keyring
// rather than the credentials file, in the same insertion order List()
// reports. It backs the additive `secure` field of `jk auth list`
// (docs/schema.md §3.1). The result is always non-nil.
func (s *fileStore) SecureHosts() ([]string, error) {
	shape, err := s.load()
	if err != nil {
		return nil, err
	}
	hosts := orderedHosts(shape)
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		if shape.Hosts[h].Secure {
			out = append(out, h)
		}
	}
	return out, nil
}
