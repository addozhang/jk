package jenkins_test

// Tests for the write-side of the Jenkins API: triggering a build and
// resolving the resulting queue item to a concrete build number.
//
// OpenSpec mapping:
//   - tasks 11.4 -> Test_Client_TriggerBuild_*
//   - tasks 11.5 -> Test_Client_ResolveQueueItem_*
//
// Per design.md D4, state-changing calls (POST) flow through the
// crumb subsystem in production. These tests keep CSRF disabled
// (Options.EnableCSRF defaults to false) because the crumb logic is
// exhaustively covered in crumb_test.go and orthogonal to API-shape
// concerns we're proving here.

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/addozhang/jk/internal/jenkins"
)

// ---------------------------------------------------------------------------
// 11.4 TriggerBuild
// ---------------------------------------------------------------------------

// Parameterless trigger uses /build, returns the queue Location.
func Test_Client_TriggerBuild_NoParams_UsesBuild(t *testing.T) {
	client, rec, srv := newClientAgainst(t)

	rec.handle("/job/svc/build", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method=%s, want POST", r.Method)
		}
		w.Header().Set("Location", srv.URL+"/queue/item/77/")
		w.WriteHeader(http.StatusCreated)
	})

	ref := mustParseRef(t, srv.URL+"/job/svc")
	loc, err := client.TriggerBuild(context.Background(), ref, nil)
	if err != nil {
		t.Fatalf("TriggerBuild: %v", err)
	}
	if !strings.HasSuffix(loc, "/queue/item/77/") {
		t.Errorf("queue location=%q, want suffix /queue/item/77/", loc)
	}
}

// Parameterized trigger uses /buildWithParameters with form-encoded body.
func Test_Client_TriggerBuild_WithParams_UsesBuildWithParameters(t *testing.T) {
	client, rec, srv := newClientAgainst(t)

	rec.handle("/job/svc/buildWithParameters", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method=%s, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); !strings.Contains(ct, "application/x-www-form-urlencoded") {
			t.Errorf("Content-Type=%q, want form-urlencoded", ct)
		}
		_ = r.ParseForm()
		if r.Form.Get("BRANCH") != "main" {
			t.Errorf("form BRANCH=%q, want main", r.Form.Get("BRANCH"))
		}
		if r.Form.Get("ENV") != "prod" {
			t.Errorf("form ENV=%q, want prod", r.Form.Get("ENV"))
		}
		w.Header().Set("Location", srv.URL+"/queue/item/101/")
		w.WriteHeader(http.StatusCreated)
	})

	ref := mustParseRef(t, srv.URL+"/job/svc")
	loc, err := client.TriggerBuild(context.Background(), ref, map[string]string{
		"BRANCH": "main",
		"ENV":    "prod",
	})
	if err != nil {
		t.Fatalf("TriggerBuild: %v", err)
	}
	if !strings.HasSuffix(loc, "/queue/item/101/") {
		t.Errorf("queue location=%q", loc)
	}
}

// Jenkins occasionally responds 200 with a Location header instead of
// 201; both must be accepted.
func Test_Client_TriggerBuild_Accepts200And201(t *testing.T) {
	client, rec, srv := newClientAgainst(t)
	rec.handle("/job/svc/build", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", srv.URL+"/queue/item/3/")
		w.WriteHeader(http.StatusOK)
	})
	ref := mustParseRef(t, srv.URL+"/job/svc")
	if _, err := client.TriggerBuild(context.Background(), ref, nil); err != nil {
		t.Errorf("TriggerBuild: %v", err)
	}
}

// A 2xx without a Location header is a Jenkins-side anomaly; we surface
// it as a clear error rather than silently returning an empty string.
func Test_Client_TriggerBuild_MissingLocationIsError(t *testing.T) {
	client, rec, srv := newClientAgainst(t)
	rec.handle("/job/svc/build", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	ref := mustParseRef(t, srv.URL+"/job/svc")
	_, err := client.TriggerBuild(context.Background(), ref, nil)
	if err == nil {
		t.Fatal("expected error when Location header is missing")
	}
}

// ---------------------------------------------------------------------------
// 11.5 ResolveQueueItem
// ---------------------------------------------------------------------------

// The happy path: queue item already has an executable; return number.
func Test_Client_ResolveQueueItem_ExecutableReady(t *testing.T) {
	client, rec, srv := newClientAgainst(t)

	rec.handle("/queue/item/42/api/json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"executable":{"number":501,"url":"http://x/job/svc/501/"}}`))
	})

	url, build, err := client.ResolveQueueItem(
		context.Background(),
		srv.URL+"/queue/item/42/",
		2*time.Second,
	)
	if err != nil {
		t.Fatalf("ResolveQueueItem: %v", err)
	}
	if build != 501 {
		t.Errorf("build=%d, want 501", build)
	}
	if url == "" {
		t.Error("buildURL is empty")
	}
}

// The polling path: first call has no executable, second call does.
// We assert the client retries and eventually succeeds within the
// supplied timeout.
func Test_Client_ResolveQueueItem_PollsUntilExecutable(t *testing.T) {
	client, rec, srv := newClientAgainst(t)

	call := 0
	rec.handle("/queue/item/42/api/json", func(w http.ResponseWriter, r *http.Request) {
		call++
		if call < 2 {
			_, _ = w.Write([]byte(`{"executable":null,"why":"In the quiet period"}`))
			return
		}
		_, _ = w.Write([]byte(`{"executable":{"number":7,"url":"http://x/job/svc/7/"}}`))
	})

	_, build, err := client.ResolveQueueItem(
		context.Background(),
		srv.URL+"/queue/item/42/",
		2*time.Second,
	)
	if err != nil {
		t.Fatalf("ResolveQueueItem: %v", err)
	}
	if build != 7 {
		t.Errorf("build=%d, want 7", build)
	}
	if call < 2 {
		t.Errorf("expected at least 2 polls, got %d", call)
	}
}

// If a queue item is cancelled, Jenkins sets `cancelled:true` and never
// produces an executable. We surface a distinct error so the CLI can
// translate it.
func Test_Client_ResolveQueueItem_CancelledItem(t *testing.T) {
	client, rec, srv := newClientAgainst(t)

	rec.handle("/queue/item/42/api/json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"executable":null,"cancelled":true,"why":"cancelled"}`))
	})

	_, _, err := client.ResolveQueueItem(
		context.Background(),
		srv.URL+"/queue/item/42/",
		1*time.Second,
	)
	if err == nil {
		t.Fatal("expected error when queue item is cancelled")
	}
	if !strings.Contains(err.Error(), "cancel") {
		t.Errorf("error should mention cancellation: %v", err)
	}
}

// Timeout: no executable appears before the deadline. Caller gets an
// error that mentions the timeout for actionable feedback.
func Test_Client_ResolveQueueItem_Timeout(t *testing.T) {
	client, rec, srv := newClientAgainst(t)

	rec.handle("/queue/item/42/api/json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"executable":null,"why":"waiting"}`))
	})

	_, _, err := client.ResolveQueueItem(
		context.Background(),
		srv.URL+"/queue/item/42/",
		200*time.Millisecond,
	)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

// ---------------------------------------------------------------------------
// Helper to use jenkins.Options.EnableCSRF for the crumb interaction in
// production wiring tests (kept here to assert the client method does
// flow through a CSRF-enabled client without panicking).
// ---------------------------------------------------------------------------

func Test_Client_TriggerBuild_WorksWithCSRFEnabled(t *testing.T) {
	rec := newRecorder()
	srv := newRecorderServer(t, rec)

	// Crumb endpoint + build endpoint.
	rec.handle("/crumbIssuer/api/json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"crumb":"abc","crumbRequestField":"Jenkins-Crumb"}`))
	})
	rec.handle("/job/svc/build", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Jenkins-Crumb") != "abc" {
			t.Errorf("missing crumb header: %v", r.Header)
		}
		w.Header().Set("Location", srv.URL+"/queue/item/1/")
		w.WriteHeader(http.StatusCreated)
	})

	httpClient, err := jenkins.New(jenkins.Options{Stderr: io.Discard, EnableCSRF: true})
	if err != nil {
		t.Fatalf("jenkins.New: %v", err)
	}
	c := jenkins.NewClient(httpClient)
	ref := mustParseRef(t, srv.URL+"/job/svc")
	if _, err := c.TriggerBuild(context.Background(), ref, nil); err != nil {
		t.Errorf("TriggerBuild: %v", err)
	}
}
