// Package auth manages persistent Jenkins credentials.
//
// jk addresses Jenkins instances by URL; the URL's host (scheme + host +
// optional non-default port) is the lookup key into a credentials file at
// `~/.config/jk/credentials`. This package owns the read/write/lookup
// primitives for that file. CLI prompting and the `jk auth *` commands
// live in internal/cli and use this package as the storage backend.
//
// File format: TOML, one table per host:
//
//	order = ["https://a.example.com", "https://b.example.com"]
//
//	[hosts."https://a.example.com"]
//	username = "alice"
//	token = "<api-token>"
//
//	[hosts."https://b.example.com"]
//	username = "bob"
//	token = "<api-token>"
//
// The explicit `order` array preserves insertion order across reads
// (TOML's map-typed `hosts` table is unordered on its own; see
// docs/schema.md §3.1 which requires insertion-order output for
// `jk auth list`).
//
// Permissions: on POSIX systems the credentials file is created with mode
// 0600 and its parent directory with 0700. On Windows, chmod is a no-op;
// see design.md §"Cross-platform credential file permissions".
//
// The store reads the file on every operation and rewrites it atomically
// (tempfile + rename) on every mutation. This is safe and simple for the
// expected scale (handfuls of hosts); we will revisit if file size becomes
// a bottleneck.
package auth

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Credential is the per-host secret pair used to authenticate with Jenkins.
// The Jenkins HTTP basic auth header carries Username:Token; both fields
// are required for a usable credential.
type Credential struct {
	Username string
	Token    string
}

// Store is the persistence interface for credentials. Implementations MUST
// be safe to call from a single process; cross-process concurrent writes
// are out of scope (the credentials file is normally edited by a single
// user, single shell).
type Store interface {
	// Add inserts or overwrites the credential for host. Insertion order
	// is preserved across Add calls; overwriting an existing host does
	// NOT change its position in the order.
	Add(host string, c Credential) error

	// Get returns the credential for host. The boolean is false if no
	// entry exists for host; in that case the returned Credential is
	// zero-valued and the error is nil.
	Get(host string) (Credential, bool, error)

	// Resolve selects the most specific stored credential for a request
	// URL using segment-boundary longest-prefix matching: a stored key
	// matches when its scheme and host equal the request's and the
	// request path equals the key's path or continues past it at a `/`
	// boundary; among all matches the one with the longest path wins. A
	// host-only key (no path) is the shortest valid prefix and therefore
	// matches any same-host request, preserving single-instance behavior.
	// ok is false when no key matches. The returned key is the exact
	// stored key, suitable as a crumb cache key and for deriving the
	// instance's context path. Unlike Get, Resolve is the lookup used by
	// the transport layer, where the request URL carries an arbitrary API
	// sub-path rather than a bare host.
	Resolve(reqURL *url.URL) (key string, c Credential, ok bool, err error)

	// List returns the configured hosts in insertion order. The result
	// is always a non-nil slice (possibly empty) so callers can pass it
	// directly to the output renderer (which emits [] for empty slices).
	List() ([]string, error)

	// Remove deletes the entry for host. Removing a missing host is a
	// no-op (idempotent), not an error, to make scripted cleanup safe.
	Remove(host string) error
}

// fileStore is the on-disk implementation of [Store]. It owns the path and
// re-reads the file on every operation so that two Store instances on the
// same path stay coherent.
type fileStore struct {
	path string
}

// NewFileStore returns a Store backed by the file at path. The file does
// not need to exist yet; it will be created on the first Add. Parent
// directories are created on demand with mode 0700 (POSIX).
//
// NewFileStore does NOT read or validate the file at construction time so
// that an unconfigured user can still run `jk auth add` without first
// having a credentials file.
func NewFileStore(path string) (Store, error) {
	if path == "" {
		return nil, errors.New("auth: credentials path must not be empty")
	}
	return &fileStore{path: path}, nil
}

// PathOf returns the on-disk path of a fileStore. Test-only helper —
// production code SHOULD NOT rely on knowing the path of a Store.
func PathOf(s Store) string {
	if fs, ok := s.(*fileStore); ok {
		return fs.path
	}
	return ""
}

// fileShape is the on-disk TOML schema. Kept private; callers see only
// the Store interface.
type fileShape struct {
	// Order preserves the user-visible insertion order of hosts. New
	// hosts are appended; overwrites do not reorder.
	Order []string `toml:"order"`
	// Hosts maps a host key to its credential. The order of this map is
	// NOT meaningful; rely on Order instead.
	Hosts map[string]Credential `toml:"hosts"`
}

func (fs *fileShape) ensureInit() {
	if fs.Hosts == nil {
		fs.Hosts = map[string]Credential{}
	}
}

// load reads and parses the credentials file. A non-existent file is
// treated as an empty store; any other I/O or parse error is surfaced.
func (s *fileStore) load() (*fileShape, error) {
	shape := &fileShape{}
	shape.ensureInit()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return shape, nil
		}
		return nil, fmt.Errorf("auth: read credentials: %w", err)
	}
	if len(data) == 0 {
		return shape, nil
	}
	if err := toml.Unmarshal(data, shape); err != nil {
		return nil, fmt.Errorf("auth: parse credentials %s: %w", s.path, err)
	}
	shape.ensureInit()
	return shape, nil
}

// save serializes shape and atomically replaces the credentials file.
// Atomicity: we write to a sibling tempfile, fsync, then os.Rename. On
// POSIX, rename within the same directory is atomic; on Windows it is not
// strictly atomic but is the best portable approximation.
func (s *fileStore) save(shape *fileShape) error {
	if err := s.ensureParentDir(); err != nil {
		return err
	}
	buf, err := tomlMarshal(shape)
	if err != nil {
		return fmt.Errorf("auth: encode credentials: %w", err)
	}
	return writeAtomic(s.path, buf)
}

// writeAtomic writes data to path via a sibling tempfile + rename so a
// crash mid-write never leaves a half-written credentials file. The file
// is created with mode 0600 (POSIX); the parent directory is assumed to
// already exist (the caller does that work).
func writeAtomic(path string, data []byte) (retErr error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".credentials.*.tmp")
	if err != nil {
		return fmt.Errorf("auth: create tempfile: %w", err)
	}
	tmpPath := tmp.Name()

	// Track closed/renamed state so the deferred cleanup is correct in
	// every exit path. closed prevents a double Close; renamed prevents
	// os.Remove from deleting the freshly-installed file.
	var closed, renamed bool
	defer func() {
		if !closed {
			if err := tmp.Close(); err != nil && retErr == nil {
				retErr = fmt.Errorf("auth: close tempfile: %w", err)
			}
		}
		if !renamed {
			if err := os.Remove(tmpPath); err != nil && !errors.Is(err, fs.ErrNotExist) && retErr == nil {
				retErr = fmt.Errorf("auth: cleanup tempfile: %w", err)
			}
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("auth: write tempfile: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("auth: chmod tempfile: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("auth: sync tempfile: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("auth: close tempfile: %w", err)
	}
	closed = true
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("auth: rename tempfile: %w", err)
	}
	renamed = true
	return nil
}

// ensureParentDir creates the parent directory chain for the credentials
// file with mode 0700 (POSIX). os.MkdirAll is a no-op when the directory
// already exists; we additionally chmod the immediate parent so an
// existing-but-too-permissive directory is tightened. We do NOT recurse
// up to chmod $HOME — that would be surprising.
func (s *fileStore) ensureParentDir() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("auth: create parent dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		// Non-fatal on Windows where chmod has no real effect.
		return fmt.Errorf("auth: chmod parent dir: %w", err)
	}
	return nil
}

// Add implements [Store.Add].
func (s *fileStore) Add(host string, c Credential) error {
	if host == "" {
		return errors.New("auth: host key must not be empty")
	}
	shape, err := s.load()
	if err != nil {
		return err
	}
	if _, exists := shape.Hosts[host]; !exists {
		shape.Order = append(shape.Order, host)
	}
	shape.Hosts[host] = c
	return s.save(shape)
}

// Get implements [Store.Get].
func (s *fileStore) Get(host string) (Credential, bool, error) {
	shape, err := s.load()
	if err != nil {
		return Credential{}, false, err
	}
	c, ok := shape.Hosts[host]
	return c, ok, nil
}

// Resolve implements [Store.Resolve]. It performs a single linear scan of
// the stored keys, tracking the longest key path that is a segment-boundary
// prefix of the request URL. Credential stores hold a handful of hosts, so a
// linear scan is negligible and avoids any secondary index. Both the request
// host and each stored key's host are normalized identically (lowercased,
// default port stripped) so a request carrying an explicit `:443`/`:80`
// still matches a key stored without it, matching the transport's host
// normalization.
func (s *fileStore) Resolve(reqURL *url.URL) (string, Credential, bool, error) {
	if reqURL == nil {
		return "", Credential{}, false, nil
	}
	shape, err := s.load()
	if err != nil {
		return "", Credential{}, false, err
	}

	reqScheme := strings.ToLower(reqURL.Scheme)
	reqOrigin := reqScheme + "://" + normalizeHostPort(reqScheme, reqURL.Host)
	reqPath := reqURL.Path

	bestKey := ""
	bestPath := ""
	found := false
	for key := range shape.Hosts {
		origin, keyPath, ok := splitOriginPath(key)
		if !ok || origin != reqOrigin {
			continue
		}
		if !pathMatches(reqPath, keyPath) {
			continue
		}
		// Longest key path wins. Ties are impossible: two equal paths on
		// the same origin would be the same key. The found flag handles
		// the empty-path host-only key, whose length never beats a
		// non-empty path but must still win when it is the only match.
		if !found || len(keyPath) > len(bestPath) {
			bestKey, bestPath, found = key, keyPath, true
		}
	}
	if !found {
		return "", Credential{}, false, nil
	}
	return bestKey, shape.Hosts[bestKey], true, nil
}

// splitOriginPath splits a stored credential key into its normalized origin
// (`scheme://host`, default port stripped) and its context-path remainder.
// A host-only key yields an empty path. Keys that do not parse as an
// absolute http(s) URL are reported with ok=false so Resolve skips them.
func splitOriginPath(key string) (origin, path string, ok bool) {
	u, err := url.Parse(key)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", "", false
	}
	scheme := strings.ToLower(u.Scheme)
	return scheme + "://" + normalizeHostPort(scheme, u.Host), u.Path, true
}

// pathMatches reports whether keyPath is a segment-boundary prefix of
// reqPath. An empty keyPath (host-only key) matches any path as the shortest
// valid prefix. A non-empty keyPath matches only when reqPath equals it or
// continues past it at a `/` boundary, so `/team-a` matches `/team-a/job/x`
// but never `/team-amber`.
func pathMatches(reqPath, keyPath string) bool {
	if keyPath == "" {
		return true
	}
	return reqPath == keyPath || strings.HasPrefix(reqPath, keyPath+"/")
}

// normalizeHostPort lowercases host and strips the port when it is the
// scheme's default (80 for http, 443 for https). It mirrors the
// normalization in jenkinsurl.normalizeHost and transport.hostKeyFromURL so
// credential keys resolve consistently across the write and request paths.
func normalizeHostPort(scheme, host string) string {
	host = strings.ToLower(host)
	switch {
	case scheme == "http" && strings.HasSuffix(host, ":80"):
		host = strings.TrimSuffix(host, ":80")
	case scheme == "https" && strings.HasSuffix(host, ":443"):
		host = strings.TrimSuffix(host, ":443")
	}
	return host
}

// List implements [Store.List]. The returned slice is filtered against
// shape.Hosts so that stale entries in shape.Order (e.g. from a hand-edited
// file where someone removed a host table but forgot to update Order) are
// silently dropped rather than reported as configured hosts.
func (s *fileStore) List() ([]string, error) {
	shape, err := s.load()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(shape.Order))
	seen := make(map[string]struct{}, len(shape.Order))
	for _, h := range shape.Order {
		if _, ok := shape.Hosts[h]; !ok {
			continue
		}
		if _, dup := seen[h]; dup {
			continue
		}
		seen[h] = struct{}{}
		out = append(out, h)
	}
	// Hand-edited files may have host tables not listed in Order; append
	// them after the ordered set so they remain visible. They appear in
	// TOML iteration order, which is unspecified but stable within a
	// single decode.
	for h := range shape.Hosts {
		if _, ok := seen[h]; ok {
			continue
		}
		seen[h] = struct{}{}
		out = append(out, h)
	}
	return out, nil
}

// Remove implements [Store.Remove]. Idempotent: removing a missing host is
// not an error so that scripted cleanup is safe to re-run.
func (s *fileStore) Remove(host string) error {
	shape, err := s.load()
	if err != nil {
		return err
	}
	if _, exists := shape.Hosts[host]; !exists {
		return nil
	}
	delete(shape.Hosts, host)
	// Drop host from Order while preserving the position of remaining
	// entries.
	pruned := shape.Order[:0]
	for _, h := range shape.Order {
		if h != host {
			pruned = append(pruned, h)
		}
	}
	shape.Order = pruned
	return s.save(shape)
}
