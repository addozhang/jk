package jenkins_test

// The crumb subsystem implements OpenSpec scenarios:
//
//   - auth/spec.md "Initial crumb acquisition"   -> Test_Crumb_FetchAndAttach_OnFirstStateChange
//   - auth/spec.md "Crumb expiry recovery"        -> Test_Crumb_RefreshAndRetry_On403
//   - auth/spec.md "CSRF disabled on instance"    -> Test_Crumb_404DisablesCrumbForHost
//   - design.md  D4 single-retry-no-loops        -> Test_Crumb_DoesNotRetryMoreThanOnce
//   - tasks 7.2  per-host in-memory cache         -> Test_Crumb_CacheIsPerHost
//   - tasks 7.5  only state-changing methods     -> Test_Crumb_GetRequestsBypassCrumb
//   - design.md  body replay constraint           -> Test_Crumb_RetryRequiresGetBody
//
// We exercise the public façade (jenkins.New + an option that installs the
// crumb RoundTripper above auth) using httptest.Server, mirroring the
// transport_test.go strategy. The crumb code path is integration-tested
// against the actual http.Client to catch wiring mistakes.

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/addozhang/jk/internal/jenkins"
)

// crumbHandlerState records what the fake Jenkins endpoint observed so
// tests can assert request counts and header values without re-parsing
// raw logs.
type crumbHandlerState struct {
	crumbFetches  atomic.Int32
	stateChanges  atomic.Int32
	lastCrumbHdr  atomic.Value // string
	crumbDisabled bool         // when true, /crumbIssuer/api/json returns 404
	rejectFirst   bool         // when true, first POST returns 403 with CSRF body
	rejectAll     bool         // when true, every POST returns 403 with CSRF body
}

func newCrumbFakeServer(state *crumbHandlerState) *httptest.Server {
	currentCrumb := "crumb-1"
	rejected := false
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/crumbIssuer/api/json":
			state.crumbFetches.Add(1)
			if state.crumbDisabled {
				http.NotFound(w, r)
				return
			}
			// Issue a new crumb every fetch so refresh is observable.
			if state.crumbFetches.Load() > 1 {
				currentCrumb = "crumb-2"
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"crumb":"` + currentCrumb + `","crumbRequestField":"Jenkins-Crumb"}`))
		default:
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				w.WriteHeader(http.StatusOK)
				return
			}
			state.stateChanges.Add(1)
			state.lastCrumbHdr.Store(r.Header.Get("Jenkins-Crumb"))
			if state.rejectAll || (state.rejectFirst && !rejected) {
				rejected = true
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte("No valid crumb was included in the request"))
				return
			}
			w.WriteHeader(http.StatusOK)
		}
	}))
}

func newCrumbClient(t *testing.T, srvURL string) *http.Client {
	t.Helper()
	client, err := jenkins.New(jenkins.Options{
		Stderr:     io.Discard,
		EnableCSRF: true,
	})
	if err != nil {
		t.Fatalf("jenkins.New: %v", err)
	}
	_ = srvURL // returned client is host-agnostic; tests pass srvURL per call
	return client
}

func postJSON(t *testing.T, client *http.Client, url, body string) (*http.Response, error) {
	t.Helper()
	// http.NewRequest with strings.NewReader sets GetBody automatically,
	// which is what the crumb retry logic needs.
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return client.Do(req)
}

// ---------------------------------------------------------------------------
// Scenario tests
// ---------------------------------------------------------------------------

// Test_Crumb_FetchAndAttach_OnFirstStateChange covers the "Initial crumb
// acquisition" scenario: a POST triggers a GET to /crumbIssuer/api/json
// and the returned crumb is attached as a header on the original POST.
func Test_Crumb_FetchAndAttach_OnFirstStateChange(t *testing.T) {
	state := &crumbHandlerState{}
	srv := newCrumbFakeServer(state)
	t.Cleanup(srv.Close)

	client := newCrumbClient(t, srv.URL)
	resp, err := postJSON(t, client, srv.URL+"/job/x/build", `{}`)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status=%d, want 200", resp.StatusCode)
	}
	if got := state.crumbFetches.Load(); got != 1 {
		t.Errorf("crumb fetches=%d, want 1", got)
	}
	if got := state.lastCrumbHdr.Load(); got != "crumb-1" {
		t.Errorf("Jenkins-Crumb header=%q, want %q", got, "crumb-1")
	}
}

// Test_Crumb_GetRequestsBypassCrumb verifies tasks 7.5: only state-
// changing methods (POST/PUT/DELETE) trigger crumb acquisition. A GET
// must not fetch a crumb.
func Test_Crumb_GetRequestsBypassCrumb(t *testing.T) {
	state := &crumbHandlerState{}
	srv := newCrumbFakeServer(state)
	t.Cleanup(srv.Close)

	client := newCrumbClient(t, srv.URL)
	resp, err := client.Get(srv.URL + "/api/json")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if got := state.crumbFetches.Load(); got != 0 {
		t.Errorf("crumb fetches=%d, want 0 (GET must not fetch crumb)", got)
	}
}

// Test_Crumb_RefreshAndRetry_On403 covers the "Crumb expiry recovery"
// scenario. The cached crumb becomes stale; the server returns 403 with
// a CSRF-related body; the client refetches and retries exactly once.
func Test_Crumb_RefreshAndRetry_On403(t *testing.T) {
	state := &crumbHandlerState{rejectFirst: true}
	srv := newCrumbFakeServer(state)
	t.Cleanup(srv.Close)

	client := newCrumbClient(t, srv.URL)
	resp, err := postJSON(t, client, srv.URL+"/job/x/build", `{}`)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status=%d, want 200 after retry", resp.StatusCode)
	}
	if got := state.crumbFetches.Load(); got != 2 {
		t.Errorf("crumb fetches=%d, want 2 (initial + refresh)", got)
	}
	if got := state.stateChanges.Load(); got != 2 {
		t.Errorf("POST attempts=%d, want 2 (original + retry)", got)
	}
	if got := state.lastCrumbHdr.Load(); got != "crumb-2" {
		t.Errorf("retry used crumb=%q, want %q (refreshed)", got, "crumb-2")
	}
}

// Test_Crumb_DoesNotRetryMoreThanOnce enforces the no-infinite-loop
// invariant from design.md D4: if the second attempt also fails, the
// 403 is propagated to the caller — we do NOT keep refreshing.
func Test_Crumb_DoesNotRetryMoreThanOnce(t *testing.T) {
	state := &crumbHandlerState{rejectAll: true}
	srv := newCrumbFakeServer(state)
	t.Cleanup(srv.Close)

	client := newCrumbClient(t, srv.URL)
	resp, err := postJSON(t, client, srv.URL+"/job/x/build", `{}`)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status=%d, want 403 after exhausted retry", resp.StatusCode)
	}
	if got := state.stateChanges.Load(); got != 2 {
		t.Errorf("POST attempts=%d, want exactly 2 (no further retries)", got)
	}
}

// Test_Crumb_404DisablesCrumbForHost covers "CSRF disabled on instance":
// a 404 on /crumbIssuer/api/json means CSRF is off; subsequent state-
// changing requests must not re-probe and must not send a crumb header.
func Test_Crumb_404DisablesCrumbForHost(t *testing.T) {
	state := &crumbHandlerState{crumbDisabled: true}
	srv := newCrumbFakeServer(state)
	t.Cleanup(srv.Close)

	client := newCrumbClient(t, srv.URL)
	for i := 0; i < 3; i++ {
		resp, err := postJSON(t, client, srv.URL+"/job/x/build", `{}`)
		if err != nil {
			t.Fatalf("POST %d: %v", i, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("POST %d status=%d, want 200", i, resp.StatusCode)
		}
	}
	if got := state.crumbFetches.Load(); got != 1 {
		t.Errorf("crumb fetches=%d, want 1 (cached 404 must not re-probe)", got)
	}
	if got := state.lastCrumbHdr.Load(); got != "" {
		t.Errorf("Jenkins-Crumb header=%q, want empty (CSRF disabled)", got)
	}
}

// Test_Crumb_CacheIsPerHost ensures that the cache is keyed per host so
// a 404 on one host does not silence crumb fetches on another.
func Test_Crumb_CacheIsPerHost(t *testing.T) {
	a := &crumbHandlerState{crumbDisabled: true}
	srvA := newCrumbFakeServer(a)
	t.Cleanup(srvA.Close)

	b := &crumbHandlerState{}
	srvB := newCrumbFakeServer(b)
	t.Cleanup(srvB.Close)

	client := newCrumbClient(t, srvA.URL)

	respA, err := postJSON(t, client, srvA.URL+"/job/x/build", `{}`)
	if err != nil {
		t.Fatalf("POST A: %v", err)
	}
	_ = respA.Body.Close()

	respB, err := postJSON(t, client, srvB.URL+"/job/y/build", `{}`)
	if err != nil {
		t.Fatalf("POST B: %v", err)
	}
	_ = respB.Body.Close()

	if got := a.crumbFetches.Load(); got != 1 {
		t.Errorf("host A crumb fetches=%d, want 1", got)
	}
	if got := b.crumbFetches.Load(); got != 1 {
		t.Errorf("host B crumb fetches=%d, want 1 (separate cache key)", got)
	}
	if got := b.lastCrumbHdr.Load(); got != "crumb-1" {
		t.Errorf("host B crumb header=%q, want %q", got, "crumb-1")
	}
}

// Test_Crumb_RetryRequiresGetBody documents the design decision that
// the retry path uses req.GetBody to replay the body. When a caller
// passes a one-shot io.Reader (no GetBody), retry is skipped and the
// original 403 propagates unchanged. We use a custom body type whose
// http.NewRequest does NOT autopopulate GetBody.
func Test_Crumb_RetryRequiresGetBody(t *testing.T) {
	state := &crumbHandlerState{rejectFirst: true}
	srv := newCrumbFakeServer(state)
	t.Cleanup(srv.Close)

	client := newCrumbClient(t, srv.URL)
	// Wrap in a custom Reader so http.NewRequest cannot detect a known
	// reader type and synthesize GetBody.
	body := io.NopCloser(bytes.NewReader([]byte(`{}`)))
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/job/x/build", body)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.GetBody = nil // explicit: no retry possible

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status=%d, want 403 (retry must be skipped without GetBody)", resp.StatusCode)
	}
	if got := state.stateChanges.Load(); got != 1 {
		t.Errorf("POST attempts=%d, want 1 (no retry without GetBody)", got)
	}
}
