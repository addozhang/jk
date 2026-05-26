package jenkins_test

// Tests for the wfapi (workflow Pipeline Stage View) endpoints + log
// streaming. Per design.md tasks 11.7-11.11, these implementations are
// best-effort against documented shapes; precise field names will be
// confirmed during spike 1.2 against a real Jenkins. Tests here lock
// in the URL composition and pass-through behavior so any future shape
// changes are caught immediately.
//
// OpenSpec mapping:
//   - tasks 11.7  -> Test_Client_GetBuildStages_*
//   - tasks 11.8  -> Test_Client_GetPendingInputs_*
//   - tasks 11.9  -> Test_Client_SubmitInput_*
//   - tasks 11.10 -> Test_Client_StreamConsoleLog_*
//   - tasks 11.11 -> Test_Client_GetStageLog_*

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// 11.7 GetBuildStages
// ---------------------------------------------------------------------------

func Test_Client_GetBuildStages(t *testing.T) {
	client, rec, srv := newClientAgainst(t)

	rec.handle("/job/svc/42/wfapi/describe", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"stages":[{"name":"Build","status":"SUCCESS"}]}`))
	})

	ref := mustParseRef(t, srv.URL+"/job/svc/42")
	raw, err := client.GetBuildStages(context.Background(), ref)
	if err != nil {
		t.Fatalf("GetBuildStages: %v", err)
	}
	if !strings.Contains(string(raw), `"Build"`) {
		t.Errorf("body missing expected stage: %s", raw)
	}
}

func Test_Client_GetBuildStages_RequiresBuildNumber(t *testing.T) {
	client, _, srv := newClientAgainst(t)
	ref := mustParseRef(t, srv.URL+"/job/svc")
	if _, err := client.GetBuildStages(context.Background(), ref); err == nil {
		t.Fatal("expected error when BuildNumber == 0")
	}
}

// ---------------------------------------------------------------------------
// 11.8 GetPendingInputs
// ---------------------------------------------------------------------------

func Test_Client_GetPendingInputs(t *testing.T) {
	client, rec, srv := newClientAgainst(t)

	rec.handle("/job/svc/42/wfapi/pendingInputActions", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"approve-deploy","message":"Ship it?"}]`))
	})

	ref := mustParseRef(t, srv.URL+"/job/svc/42")
	raw, err := client.GetPendingInputs(context.Background(), ref)
	if err != nil {
		t.Fatalf("GetPendingInputs: %v", err)
	}
	if !strings.Contains(string(raw), "approve-deploy") {
		t.Errorf("body missing input: %s", raw)
	}
}

// ---------------------------------------------------------------------------
// 11.9 SubmitInput
// ---------------------------------------------------------------------------

func Test_Client_SubmitInput_Proceed(t *testing.T) {
	client, rec, srv := newClientAgainst(t)

	rec.handle("/job/svc/42/input/approve-deploy/proceedEmpty", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method=%s, want POST", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	})

	ref := mustParseRef(t, srv.URL+"/job/svc/42")
	if err := client.SubmitInput(context.Background(), ref, "approve-deploy", true); err != nil {
		t.Errorf("SubmitInput(proceed): %v", err)
	}
}

func Test_Client_SubmitInput_Abort(t *testing.T) {
	client, rec, srv := newClientAgainst(t)

	rec.handle("/job/svc/42/input/approve-deploy/abort", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method=%s, want POST", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	})

	ref := mustParseRef(t, srv.URL+"/job/svc/42")
	if err := client.SubmitInput(context.Background(), ref, "approve-deploy", false); err != nil {
		t.Errorf("SubmitInput(abort): %v", err)
	}
}

// ---------------------------------------------------------------------------
// 11.10 StreamConsoleLog
// ---------------------------------------------------------------------------

// Non-follow mode: one POST to logText/progressiveText, response body
// written through to the caller's writer.
func Test_Client_StreamConsoleLog_NoFollow(t *testing.T) {
	client, rec, srv := newClientAgainst(t)

	rec.handle("/job/svc/42/logText/progressiveText", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("line one\nline two\n"))
	})

	ref := mustParseRef(t, srv.URL+"/job/svc/42")
	var buf bytes.Buffer
	if err := client.StreamConsoleLog(context.Background(), ref, &buf, false); err != nil {
		t.Fatalf("StreamConsoleLog: %v", err)
	}
	if got := buf.String(); !strings.Contains(got, "line one") {
		t.Errorf("output missing expected line: %q", got)
	}
}

// Follow mode: repeated requests, using X-More-Data + X-Text-Size
// headers to drive incremental retrieval. We simulate two chunks then
// "done" by omitting X-More-Data.
func Test_Client_StreamConsoleLog_Follow(t *testing.T) {
	client, rec, srv := newClientAgainst(t)

	chunks := []string{"chunk1\n", "chunk2\n"}
	call := 0
	rec.handle("/job/svc/42/logText/progressiveText", func(w http.ResponseWriter, r *http.Request) {
		// Caller passes ?start=<offset>; we honor it loosely.
		body := chunks[call]
		call++
		w.Header().Set("X-Text-Size", "0") // value irrelevant for this fake
		if call < len(chunks) {
			w.Header().Set("X-More-Data", "true")
		}
		_, _ = w.Write([]byte(body))
	})

	ref := mustParseRef(t, srv.URL+"/job/svc/42")
	var buf bytes.Buffer
	if err := client.StreamConsoleLog(context.Background(), ref, &buf, true); err != nil {
		t.Fatalf("StreamConsoleLog: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "chunk1") || !strings.Contains(got, "chunk2") {
		t.Errorf("missing chunks: %q", got)
	}
	if call < 2 {
		t.Errorf("expected at least 2 polls in follow mode, got %d", call)
	}
}

// ---------------------------------------------------------------------------
// 11.11 GetStageLog
// ---------------------------------------------------------------------------

func Test_Client_GetStageLog(t *testing.T) {
	client, rec, srv := newClientAgainst(t)

	// Jenkins addresses stage logs via /execution/node/<flowNodeID>/wfapi/log.
	// Stage names map to flowNodeIDs via the wfapi/describe response;
	// to keep this layer name-agnostic we accept the resolved stage
	// identifier as a string argument and treat it as opaque path data.
	rec.handle("/job/svc/42/execution/node/13/wfapi/log", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"text":"stage output"}`))
	})

	ref := mustParseRef(t, srv.URL+"/job/svc/42")
	raw, err := client.GetStageLog(context.Background(), ref, "13")
	if err != nil {
		t.Fatalf("GetStageLog: %v", err)
	}
	if !strings.Contains(string(raw), "stage output") {
		t.Errorf("body missing expected text: %s", raw)
	}
}
