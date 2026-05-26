package jenkins

// This file owns the CSRF crumb subsystem. It is logically separate from
// transport.go because the crumb logic owns its own lifecycle (cache,
// invalidation, single-retry), but it is wired in via [New] so the
// resulting *http.Client transparently handles crumbs for every caller.
//
// Behavioral spec (from openspec/.../auth/spec.md "Acquire and cache
// CSRF crumb" + design.md D4):
//
//   1. Crumbs are only fetched for state-changing methods
//      (POST/PUT/DELETE/PATCH). GETs/HEADs bypass the crumb path
//      entirely so plain reads stay fast.
//
//   2. The crumb is fetched on demand from `/crumbIssuer/api/json` on
//      the same scheme+host as the outbound request, parsed into
//      header-name + header-value, and attached to the original request.
//
//   3. Result is cached per host for the process lifetime. A 404 from
//      the crumb endpoint is cached as "this host has CSRF disabled" so
//      we neither send crumb headers nor re-probe.
//
//   4. On a 403 with a CSRF-indicative body, the cache for that host is
//      invalidated, a fresh crumb is fetched, and the original request
//      is replayed EXACTLY ONCE. If the retry still fails, the original
//      403 response is returned to the caller without further attempts.
//
//   5. Replay requires `req.GetBody` to be non-nil. http.NewRequest
//      synthesizes GetBody for strings.Reader/bytes.Reader/bytes.Buffer
//      bodies, which covers everything jk's API client sends. Custom
//      readers without GetBody bypass the retry path.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// closeBody discards the error from Body.Close. Used in places where
// the body has already been drained (or the close happens during error
// handling) and a close failure cannot meaningfully be surfaced.
func closeBody(b io.Closer) {
	_ = b.Close() //nolint:errcheck // body close errors are not actionable here
}

// crumbCacheEntry is a single host's CSRF state. exactly one of
// (field/value) or `disabled=true` is meaningful.
type crumbCacheEntry struct {
	field    string // header name, e.g. "Jenkins-Crumb"
	value    string // header value
	disabled bool   // true if the host returned 404 on /crumbIssuer/api/json
}

// crumbManager fetches, caches and invalidates crumbs. It is safe for
// concurrent use by multiple goroutines sharing one *http.Client.
type crumbManager struct {
	// fetcher is an *http.Client used to issue the
	// `/crumbIssuer/api/json` GET. Using http.Client.Do (rather than
	// RoundTripper.RoundTrip directly) ensures the cookie jar on the
	// client is consulted: Jenkins binds the crumb to the JSESSIONID
	// cookie returned by the crumb GET, and that same cookie must be
	// present on the subsequent POST or Jenkins rejects the crumb with
	// 403. The transport inside this client must NOT be the outer
	// crumb-wrapped transport or we would recurse forever.
	fetcher *http.Client

	mu    sync.Mutex
	cache map[string]crumbCacheEntry // keyed by hostKeyFromURL
}

func newCrumbManager(fetcher *http.Client) *crumbManager {
	return &crumbManager{
		fetcher: fetcher,
		cache:   make(map[string]crumbCacheEntry),
	}
}

// cookies returns the cookies the fetcher client's jar has for the given URL.
// This is used to copy the JSESSIONID (set during the crumb GET) onto the
// outbound POST request, since the POST goes through RoundTrip (not
// client.Do) and the jar is only consulted by client.Do — so we must
// manually bridge the session cookie from the crumb fetch into the request.
func (m *crumbManager) cookies(u *url.URL) []*http.Cookie {
	if m.fetcher.Jar == nil {
		return nil
	}
	return m.fetcher.Jar.Cookies(u)
}

// get returns the cached entry for hostKey, or ok==false if none exists.
func (m *crumbManager) get(hostKey string) (crumbCacheEntry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.cache[hostKey]
	return e, ok
}

// set replaces the cached entry for hostKey.
func (m *crumbManager) set(hostKey string, e crumbCacheEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cache[hostKey] = e
}

// invalidate removes any cached crumb for hostKey. Used after a 403/CSRF
// response so the next attempt will re-fetch.
func (m *crumbManager) invalidate(hostKey string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cache, hostKey)
}

// fetch issues a GET to /crumbIssuer/api/json on the same host as
// reqURL, using a request derived from src so we inherit context,
// scheme, and (via the auth injector inside m.fetcher) credentials.
//
// Returns:
//   - entry with field+value populated on success;
//   - entry with disabled=true if the endpoint returned 404 (CSRF off);
//   - error if the network call failed or the response was unparseable.
func (m *crumbManager) fetch(src *http.Request) (crumbCacheEntry, error) {
	u := *src.URL
	u.Path = "/crumbIssuer/api/json"
	u.RawQuery = ""
	u.Fragment = ""

	req, err := http.NewRequestWithContext(src.Context(), http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return crumbCacheEntry{}, fmt.Errorf("build crumb request: %w", err)
	}
	// Use client.Do (not RoundTrip) so the cookie jar on the client is
	// engaged: Jenkins binds the crumb to the JSESSIONID cookie set
	// during this GET; the jar stores it and sends it on the subsequent
	// POST, keeping both requests in the same session.
	resp, err := m.fetcher.Do(req)
	if err != nil {
		return crumbCacheEntry{}, fmt.Errorf("crumb fetch: %w", err)
	}
	defer closeBody(resp.Body)

	if resp.StatusCode == http.StatusNotFound {
		return crumbCacheEntry{disabled: true}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return crumbCacheEntry{}, fmt.Errorf("crumb fetch: unexpected status %d", resp.StatusCode)
	}

	// Bound the read to avoid OOM on a hostile server.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return crumbCacheEntry{}, fmt.Errorf("crumb fetch: read body: %w", err)
	}
	var payload struct {
		Crumb             string `json:"crumb"`
		CrumbRequestField string `json:"crumbRequestField"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return crumbCacheEntry{}, fmt.Errorf("crumb fetch: parse json: %w", err)
	}
	if payload.Crumb == "" || payload.CrumbRequestField == "" {
		return crumbCacheEntry{}, errors.New("crumb fetch: response missing crumb or crumbRequestField")
	}
	return crumbCacheEntry{field: payload.CrumbRequestField, value: payload.Crumb}, nil
}

// ---------------------------------------------------------------------------
// RoundTripper
// ---------------------------------------------------------------------------

// crumbRoundTripper is the http.RoundTripper layer that integrates the
// crumb manager into the transport stack. It MUST sit above the auth
// injector so the crumb fetch itself is authenticated, and the manager
// holds a separate reference to the auth-wrapped base so its fetches
// bypass `crumbRoundTripper` (and the recursion that would imply).
type crumbRoundTripper struct {
	next http.RoundTripper
	mgr  *crumbManager
}

// isStateChanging reports whether method requires CSRF protection per
// Jenkins' default policy. We treat the same set as Jenkins: anything
// other than GET/HEAD/OPTIONS/TRACE.
func isStateChanging(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace, "":
		return false
	default:
		return true
	}
}

// looksLikeCSRFRejection returns true when a 403 response body suggests
// the rejection was due to a missing/stale crumb (as opposed to plain
// authorization failure). Jenkins emits messages containing "crumb"
// in this case; we match case-insensitively.
func looksLikeCSRFRejection(body []byte) bool {
	return strings.Contains(strings.ToLower(string(body)), "crumb")
}

func (c *crumbRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if !isStateChanging(req.Method) {
		return c.next.RoundTrip(req)
	}

	hostKey := hostKeyFromURL(req.URL)

	// Attempt 1: use cached entry if any, otherwise fetch.
	entry, ok := c.mgr.get(hostKey)
	if !ok {
		fetched, err := c.mgr.fetch(req)
		if err != nil {
			// Surface fetch errors directly; the API client (group 11)
			// will translate them into crumb_failed if/when the user
			// ultimately cannot make the call.
			return nil, err
		}
		entry = fetched
		c.mgr.set(hostKey, entry)
	}

	first, err := c.attempt(req, entry)
	if err != nil {
		return nil, err
	}

	// Retry exactly once when (a) the server actually applied a crumb
	// expectation, (b) it returned 403, and (c) the body indicates CSRF.
	// Skip retry if the host has CSRF disabled (entry.disabled), if the
	// body cannot be replayed, or if the response is anything other than
	// 403/CSRF.
	if entry.disabled || first.StatusCode != http.StatusForbidden {
		return first, nil
	}
	if req.GetBody == nil && req.Body != nil && req.Body != http.NoBody {
		return first, nil
	}

	// Peek a small slice of the body to confirm CSRF involvement, then
	// restore an equivalent body for the caller in case we decide not to
	// retry after all.
	peek, err := io.ReadAll(io.LimitReader(first.Body, 4*1024))
	closeBody(first.Body)
	if err != nil {
		return nil, fmt.Errorf("read 403 body: %w", err)
	}
	if !looksLikeCSRFRejection(peek) {
		// Not a CSRF-style rejection; rewrap the body so callers can read it.
		first.Body = io.NopCloser(strings.NewReader(string(peek)))
		return first, nil
	}

	// CSRF retry path: invalidate, refetch, replay body, attempt again.
	c.mgr.invalidate(hostKey)
	refreshed, err := c.mgr.fetch(req)
	if err != nil {
		// Could not refresh; return a synthetic 403 with the original body
		// so the caller still sees the server's message. Callers in
		// group 11 will translate to crumb_failed when appropriate.
		// nilerr: returning the underlying err would mask the HTTP-level
		// 403 the server actually sent; the caller treats response and
		// error as alternatives, not both.
		return &http.Response{ //nolint:nilerr // intentional: surface 403 to caller
			Status:     first.Status,
			StatusCode: first.StatusCode,
			Proto:      first.Proto,
			ProtoMajor: first.ProtoMajor,
			ProtoMinor: first.ProtoMinor,
			Header:     first.Header,
			Body:       io.NopCloser(strings.NewReader(string(peek))),
			Request:    first.Request,
		}, nil
	}
	c.mgr.set(hostKey, refreshed)

	retryReq, err := cloneWithFreshBody(req)
	if err != nil {
		return nil, err
	}
	return c.attempt(retryReq, refreshed)
}

// attempt issues a single request with the supplied crumb entry merged
// in. It clones the request to avoid mutating the caller's headers.
// It also injects any cookies from the fetcher's jar so the POST is in
// the same Jenkins session as the crumb GET (Jenkins ties crumb validity
// to the session established during /crumbIssuer/api/json).
func (c *crumbRoundTripper) attempt(req *http.Request, entry crumbCacheEntry) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	if !entry.disabled && entry.field != "" {
		cloned.Header.Set(entry.field, entry.value)
	}
	// Inject session cookies from the crumb fetcher's jar so the
	// outgoing request shares the JSESSIONID from the crumb GET.
	// We do this here (inside the RoundTripper chain) because
	// http.Client.Do already ran cookie injection before entering the
	// transport stack, so the jar was empty at that point and the POST
	// has no session cookie yet.
	for _, ck := range c.mgr.cookies(req.URL) {
		cloned.AddCookie(ck)
	}
	return c.next.RoundTrip(cloned)
}

// cloneWithFreshBody returns a copy of req with its body re-initialized
// via GetBody, so the retry sends the same payload the original did.
// The caller is responsible for ensuring GetBody is non-nil (we check
// before invoking).
func cloneWithFreshBody(req *http.Request) (*http.Request, error) {
	cloned := req.Clone(req.Context())
	if req.GetBody == nil {
		// No body to replay; safe to retry as-is.
		return cloned, nil
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, fmt.Errorf("replay request body: %w", err)
	}
	cloned.Body = body
	return cloned, nil
}
