package jenkins_test

// Client tests exercise the Jenkins API methods against httptest.Server
// fakes that mimic the response shapes documented at
// https://www.jenkins.io/doc/book/using/remote-access-api/. Each test
// asserts both the request the client emits (path, method, headers)
// and the bytes it returns to the caller.
//
// OpenSpec mapping:
//   - tasks 11.1 -> Test_Client_GetPipelineInfo
//   - tasks 11.2 -> Test_Client_GetPipelineParams
//   - tasks 11.3 -> Test_Client_ListPipelinesInFolder
//   - tasks 11.6 -> Test_Client_GetBuildStatus
//   - tasks 11.12 -> Test_Client_ResolveLastBuild
//
// The fixtures here are intentionally minimal: enough fields to prove
// the URL composition and pass-through behavior; mapping richness is
// the job of group 12.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/addozhang/jk/internal/auth"
	"github.com/addozhang/jk/internal/jenkins"
	"github.com/addozhang/jk/internal/jenkinsurl"
)

func Test_Client_GetBuildArtifacts(t *testing.T) {
	client, rec, srv := newClientAgainst(t)
	rec.handle("/job/svc/lastSuccessfulBuild/api/json", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("tree"); got != "number,url,artifacts[fileName,relativePath]" {
			t.Errorf("tree = %q", got)
		}
		_, _ = io.WriteString(w, `{"number":42,"url":"https://jenkins.example/job/svc/42/","artifacts":[]}`)
	})
	ref := mustParseRef(t, srv.URL+"/job/svc/lastSuccessfulBuild/")
	if _, err := client.GetBuildArtifacts(context.Background(), ref); err != nil {
		t.Fatalf("GetBuildArtifacts: %v", err)
	}
}

func Test_Client_StreamArtifact(t *testing.T) {
	client, rec, srv := newClientAgainst(t)
	want := bytes.Repeat([]byte{0, 1, 2, 3}, (16<<20)/4+1)
	rec.handle("/job/svc/42/artifact/dist/app.bin", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(want)
	})
	ref := mustParseRef(t, srv.URL+"/job/svc/42/")
	var got bytes.Buffer
	if err := client.StreamArtifact(context.Background(), ref, "dist/app.bin", &got); err != nil {
		t.Fatalf("StreamArtifact: %v", err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("downloaded %d bytes, want %d", got.Len(), len(want))
	}
}

func Test_Client_StreamArtifact_HTTPError(t *testing.T) {
	client, rec, srv := newClientAgainst(t)
	rec.handle("/job/svc/42/artifact/missing.txt", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	})
	ref := mustParseRef(t, srv.URL+"/job/svc/42/")
	err := client.StreamArtifact(context.Background(), ref, "missing.txt", io.Discard)
	var statusErr *jenkins.HTTPStatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusNotFound {
		t.Fatalf("error = %v, want HTTPStatusError 404", err)
	}
}

func Test_Client_StreamArtifact_FollowsRedirectWithoutForwardingAuth(t *testing.T) {
	var redirectedAuth string
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, "stored")
	}))
	defer storage.Close()
	var jenkinsAuth string
	jenkinsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jenkinsAuth = r.Header.Get("Authorization")
		http.Redirect(w, r, storage.URL+"/object", http.StatusFound)
	}))
	defer jenkinsServer.Close()

	store, err := auth.NewFileStore(filepath.Join(t.TempDir(), "credentials"))
	if err != nil {
		t.Fatal(err)
	}
	err = store.Add(jenkinsServer.URL, auth.Credential{Username: "alice", Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	httpClient, err := jenkins.New(jenkins.Options{Credentials: store, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	client := jenkins.NewClient(httpClient)
	ref := mustParseRef(t, jenkinsServer.URL+"/job/svc/42/")
	var got strings.Builder
	if err := client.StreamArtifact(context.Background(), ref, "app.txt", &got); err != nil {
		t.Fatal(err)
	}
	if jenkinsAuth == "" {
		t.Fatal("production auth injector did not authenticate the Jenkins request")
	}
	if got.String() != "stored" || redirectedAuth != "" {
		t.Fatalf("content=%q redirected auth=%q", got.String(), redirectedAuth)
	}
}

func Test_Client_StreamArtifact_Cancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	client := jenkins.NewClient(srv.Client())
	ref := mustParseRef(t, srv.URL+"/job/svc/42/")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.StreamArtifact(ctx, ref, "app.txt", io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
}

// recordedRequest captures everything we want to assert about a single
// request the client made to the fake server.
type recordedRequest struct {
	Method string
	Path   string
	Query  string
	Header http.Header
}

// requestRecorder is a fake Jenkins backend keyed by exact request
// path (without query). Each entry returns a canned body + status; the
// recorder also stores every observed request for later inspection.
type requestRecorder struct {
	mu       sync.Mutex
	requests []recordedRequest
	routes   map[string]routeHandler
}

type routeHandler func(w http.ResponseWriter, r *http.Request)

func newRecorder() *requestRecorder {
	return &requestRecorder{routes: make(map[string]routeHandler)}
}

func (rr *requestRecorder) handle(path string, h routeHandler) {
	rr.routes[path] = h
}

func (rr *requestRecorder) serve(w http.ResponseWriter, r *http.Request) {
	rr.mu.Lock()
	rr.requests = append(rr.requests, recordedRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.RawQuery,
		Header: r.Header.Clone(),
	})
	h, ok := rr.routes[r.URL.Path]
	rr.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	h(w, r)
}

func (rr *requestRecorder) lastRequest(t *testing.T) recordedRequest {
	t.Helper()
	rr.mu.Lock()
	defer rr.mu.Unlock()
	if len(rr.requests) == 0 {
		t.Fatal("recorder has no requests")
	}
	return rr.requests[len(rr.requests)-1]
}

// newClientAgainst builds a jenkins.Client wired to srv with default
// options (no CSRF, no auth, no debug). Tests that need those flip them
// on directly.
func newClientAgainst(t *testing.T) (*jenkins.Client, *requestRecorder, *httptest.Server) {
	t.Helper()
	rec := newRecorder()
	srv := httptest.NewServer(http.HandlerFunc(rec.serve))
	t.Cleanup(srv.Close)

	client, err := jenkins.New(jenkins.Options{Stderr: io.Discard})
	if err != nil {
		t.Fatalf("jenkins.New: %v", err)
	}
	c := jenkins.NewClient(client)
	return c, rec, srv
}

func mustParseRef(t *testing.T, raw string) *jenkinsurl.Ref {
	t.Helper()
	ref, err := jenkinsurl.Parse(raw)
	if err != nil {
		t.Fatalf("Parse(%q): %v", raw, err)
	}
	return ref
}

// newRecorderServer is the lower-level helper used by tests that want
// to wire their own jenkins.Client (e.g. with EnableCSRF=true). The
// returned server is registered for cleanup automatically.
func newRecorderServer(t *testing.T, rec *requestRecorder) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(rec.serve))
	t.Cleanup(srv.Close)
	return srv
}

// ---------------------------------------------------------------------------
// 11.1 GetPipelineInfo
// ---------------------------------------------------------------------------

func Test_Client_GetPipelineInfo(t *testing.T) {
	client, rec, srv := newClientAgainst(t)

	const body = `{"_class":"org.jenkinsci.plugins.workflow.job.WorkflowJob","name":"svc","url":"http://example/job/svc/"}`
	rec.handle("/job/svc/api/json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})

	ref := mustParseRef(t, srv.URL+"/job/svc")
	raw, err := client.GetPipelineInfo(context.Background(), ref)
	if err != nil {
		t.Fatalf("GetPipelineInfo: %v", err)
	}
	if string(raw) != body {
		t.Errorf("body mismatch:\n got %s\nwant %s", raw, body)
	}
	req := rec.lastRequest(t)
	if req.Method != http.MethodGet {
		t.Errorf("method=%s, want GET", req.Method)
	}
}

// ---------------------------------------------------------------------------
// 11.2 GetPipelineParams (a refined tree query on /api/json)
// ---------------------------------------------------------------------------

func Test_Client_GetPipelineParams(t *testing.T) {
	client, rec, srv := newClientAgainst(t)

	rec.handle("/job/svc/api/json", func(w http.ResponseWriter, r *http.Request) {
		// Params endpoint reuses /api/json but with a tree filter for property
		// definitions; assert the tree= parameter is present so the response
		// is reduced server-side.
		if !strings.Contains(r.URL.RawQuery, "tree=") {
			t.Errorf("expected tree= query, got %q", r.URL.RawQuery)
		}
		if !strings.Contains(r.URL.RawQuery, "property") {
			t.Errorf("tree filter must request 'property': %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"property":[]}`))
	})

	ref := mustParseRef(t, srv.URL+"/job/svc")
	if _, err := client.GetPipelineParams(context.Background(), ref); err != nil {
		t.Fatalf("GetPipelineParams: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 11.3 ListPipelinesInFolder
// ---------------------------------------------------------------------------

func Test_Client_ListPipelinesInFolder(t *testing.T) {
	client, rec, srv := newClientAgainst(t)

	rec.handle("/job/team/api/json", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "tree=jobs") {
			t.Errorf("expected tree=jobs query, got %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jobs":[{"name":"svc","_class":"x"}]}`))
	})

	ref := mustParseRef(t, srv.URL+"/job/team")
	raw, err := client.ListPipelinesInFolder(context.Background(), ref)
	if err != nil {
		t.Fatalf("ListPipelinesInFolder: %v", err)
	}
	if !strings.Contains(string(raw), `"jobs"`) {
		t.Errorf("body missing jobs key: %s", raw)
	}
}

// ---------------------------------------------------------------------------
// 11.6 GetBuildStatus
// ---------------------------------------------------------------------------

func Test_Client_GetBuildStatus_ExplicitBuild(t *testing.T) {
	client, rec, srv := newClientAgainst(t)

	rec.handle("/job/svc/42/api/json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"number":42,"building":false,"result":"SUCCESS"}`))
	})

	ref := mustParseRef(t, srv.URL+"/job/svc/42")
	raw, err := client.GetBuildStatus(context.Background(), ref)
	if err != nil {
		t.Fatalf("GetBuildStatus: %v", err)
	}
	if !strings.Contains(string(raw), `"result":"SUCCESS"`) {
		t.Errorf("body missing result: %s", raw)
	}
}

// GetBuildStatus must NOT silently fall back to lastBuild when the Ref
// lacks a build number — that's ResolveLastBuild's job. The caller is
// expected to resolve first, then ask for status. We assert by passing
// a Ref without a build number and observing a clear error.
func Test_Client_GetBuildStatus_RequiresBuildNumber(t *testing.T) {
	client, _, srv := newClientAgainst(t)
	ref := mustParseRef(t, srv.URL+"/job/svc")
	_, err := client.GetBuildStatus(context.Background(), ref)
	if err == nil {
		t.Fatal("expected error when BuildNumber == 0")
	}
}

// When a Ref carries a BuildPermalink, GetBuildStatus MUST issue the
// status request against `<permalink>/api/json` directly (no tree-query
// pre-flight) and let Jenkins resolve the permalink server-side.
func Test_Client_GetBuildStatus_AcceptsPermalink(t *testing.T) {
	client, rec, srv := newClientAgainst(t)

	var hits []string
	rec.handle("/job/svc/lastSuccessfulBuild/api/json", func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"number":17,"building":false,"result":"SUCCESS","url":"` + srv.URL + `/job/svc/17/"}`))
	})
	// Detect any accidental pre-flight against /job/svc/api/json.
	rec.handle("/job/svc/api/json", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected pre-flight tree query: %s?%s", r.URL.Path, r.URL.RawQuery)
		http.Error(w, "no", http.StatusInternalServerError)
	})

	ref := mustParseRef(t, srv.URL+"/job/svc/lastSuccessfulBuild/")
	body, err := client.GetBuildStatus(context.Background(), ref)
	if err != nil {
		t.Fatalf("GetBuildStatus: %v", err)
	}
	if len(hits) != 1 {
		t.Errorf("expected exactly one permalink fetch, got %d (%v)", len(hits), hits)
	}
	if !strings.Contains(string(body), `"number":17`) {
		t.Errorf("body missing resolved build number: %s", body)
	}
}

// A 404 on a permalink path (e.g. no successful build yet) must surface
// as a normal HTTP error, not a synthetic "never built" message.
func Test_Client_GetBuildStatus_PermalinkNotFoundSurfacesHTTPError(t *testing.T) {
	client, rec, srv := newClientAgainst(t)
	rec.handle("/job/svc/lastSuccessfulBuild/api/json", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})

	ref := mustParseRef(t, srv.URL+"/job/svc/lastSuccessfulBuild/")
	_, err := client.GetBuildStatus(context.Background(), ref)
	if err == nil {
		t.Fatal("expected error on 404")
	}
	// We should NOT mention "lastBuild" or claim the pipeline has no builds —
	// that copy is reserved for ResolveLastBuild's never-built case.
	if strings.Contains(err.Error(), "has no builds yet") {
		t.Errorf("unexpected friendly never-built copy on permalink 404: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 9.x GetBuildParams (extend-build-addressing)
// ---------------------------------------------------------------------------

// GetBuildParams MUST issue a tree-filtered request for
// number,url,actions[parameters[name,value]] so the response is
// scoped to exactly the fields MapBuildParams reads.
func Test_Client_GetBuildParams_Numeric(t *testing.T) {
	client, rec, srv := newClientAgainst(t)
	rec.handle("/job/svc/42/api/json", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.RawQuery
		for _, want := range []string{"tree=", "number", "url", "actions", "parameters", "name", "value"} {
			if !strings.Contains(q, want) {
				t.Errorf("tree query missing %q: %s", want, q)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"number":42,"url":"x","actions":[]}`))
	})

	ref := mustParseRef(t, srv.URL+"/job/svc/42/")
	if _, err := client.GetBuildParams(context.Background(), ref); err != nil {
		t.Fatalf("GetBuildParams: %v", err)
	}
}

// Permalink synergy: GetBuildParams against a permalink Ref hits
// <permalink>/api/json directly (no pre-flight tree=lastBuild lookup),
// just like GetBuildStatus.
func Test_Client_GetBuildParams_Permalink(t *testing.T) {
	client, rec, srv := newClientAgainst(t)

	var hits []string
	rec.handle("/job/svc/lastSuccessfulBuild/api/json", func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"number":17,"url":"x","actions":[]}`))
	})
	rec.handle("/job/svc/api/json", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected pre-flight tree query: %s?%s", r.URL.Path, r.URL.RawQuery)
		http.Error(w, "no", http.StatusInternalServerError)
	})

	ref := mustParseRef(t, srv.URL+"/job/svc/lastSuccessfulBuild/")
	if _, err := client.GetBuildParams(context.Background(), ref); err != nil {
		t.Fatalf("GetBuildParams: %v", err)
	}
	if len(hits) != 1 {
		t.Errorf("expected exactly one permalink fetch, got %d (%v)", len(hits), hits)
	}
}

// GetBuildParams MUST refuse a Ref with neither BuildNumber nor
// BuildPermalink (same gating as GetBuildStatus). Falling back to
// lastBuild silently would surprise the caller in the same way.
func Test_Client_GetBuildParams_RequiresBuildAddress(t *testing.T) {
	client, _, srv := newClientAgainst(t)
	ref := mustParseRef(t, srv.URL+"/job/svc")
	if _, err := client.GetBuildParams(context.Background(), ref); err == nil {
		t.Fatal("expected error when Ref carries neither BuildNumber nor BuildPermalink")
	}
}

// ---------------------------------------------------------------------------
// 11.12 ResolveLastBuild
// ---------------------------------------------------------------------------

func Test_Client_ResolveLastBuild(t *testing.T) {
	client, rec, srv := newClientAgainst(t)

	rec.handle("/job/svc/api/json", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "lastBuild") {
			t.Errorf("expected lastBuild in tree query: %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"lastBuild":{"number":99}}`))
	})

	ref := mustParseRef(t, srv.URL+"/job/svc")
	got, err := client.ResolveLastBuild(context.Background(), ref)
	if err != nil {
		t.Fatalf("ResolveLastBuild: %v", err)
	}
	if got != 99 {
		t.Errorf("ResolveLastBuild=%d, want 99", got)
	}
}

func Test_Client_ResolveLastBuild_None(t *testing.T) {
	client, rec, srv := newClientAgainst(t)

	rec.handle("/job/svc/api/json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"lastBuild":null}`))
	})

	ref := mustParseRef(t, srv.URL+"/job/svc")
	_, err := client.ResolveLastBuild(context.Background(), ref)
	if err == nil {
		t.Fatal("expected error when pipeline has no lastBuild")
	}
}

// ---------------------------------------------------------------------------
// Error propagation: non-2xx responses become errors so callers can
// translate them via internal/errors.Classify.
// ---------------------------------------------------------------------------

func Test_Client_PropagatesHTTPErrors(t *testing.T) {
	client, rec, srv := newClientAgainst(t)
	rec.handle("/job/svc/api/json", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	})
	ref := mustParseRef(t, srv.URL+"/job/svc")
	_, err := client.GetPipelineInfo(context.Background(), ref)
	if err == nil {
		t.Fatal("expected error on 403")
	}
}
