package cli

// E2e tests for the `jk build {trigger,status,stages,input,logs}`
// commands. Each test boots an httptest.Server that mimics the Jenkins
// surface for one scenario from specs/build/spec.md, runs the root
// command with runJK (defined in pipeline_test.go), and asserts on
// stdout / stderr / the returned error.
//
// Common shortcuts:
//   - Build URLs are constructed as <server>/job/svc/<n>/ so that
//     jenkinsurl.Parse captures BuildNumber = n.
//   - The watch tests swap watchPollIntervalFor for a 5ms cadence so
//     the suite stays under one second per invocation.
//   - Tests asserting on exit codes use jkerrors.ExitCode rather than
//     the spec-internal value to keep them stable across future
//     adjustments to the error mapping.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	jkerrors "github.com/addozhang/jk/internal/errors"
)

// muxBuilder is a tiny helper that lets each test declaratively wire
// path → handler pairs without re-typing the http.ServeMux boilerplate.
// Unmatched paths produce a `t.Errorf` so missing routes don't
// silently 404 and confuse the failure mode.
type muxBuilder struct {
	t   *testing.T
	mux *http.ServeMux
}

func newMux(t *testing.T) *muxBuilder {
	t.Helper()
	m := &muxBuilder{t: t, mux: http.NewServeMux()}
	// The transport probes /crumbIssuer/api/json before any state-
	// changing POST. We answer 404 (= CSRF disabled on this server)
	// so the crumb cache records "no crumb" and POSTs proceed
	// unprotected. Without this, the mux fallback would t.Errorf.
	m.mux.HandleFunc("/crumbIssuer/api/json", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	m.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		http.NotFound(w, r)
	})
	return m
}

func (m *muxBuilder) handle(path string, h http.HandlerFunc) *muxBuilder {
	m.mux.HandleFunc(path, h)
	return m
}

func (m *muxBuilder) server() *httptest.Server {
	return httptest.NewServer(m.mux)
}

// ---------------------------------------------------------------------------
// 14.1 build trigger
// ---------------------------------------------------------------------------

// Scenario: "Trigger an unparameterized build" — POST /build, queue
// resolves, response carries queueId/buildUrl/buildNumber.
func Test_BuildTrigger_Unparameterized(t *testing.T) {
	srv := newMux(t).
		handle("/job/svc/build", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			// Location is what the client polls; the queue ID extractor
			// pulls the trailing number out of the URL path.
			w.Header().Set("Location", "http://"+r.Host+"/queue/item/77/")
			w.WriteHeader(http.StatusCreated)
		}).
		handle("/queue/item/77/api/json", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"executable":{"number":12,"url":"http://example/job/svc/12/"}}`)
		}).
		server()
	defer srv.Close()

	stdout, _, err := runJK(t, []string{"build", "trigger", srv.URL + "/job/svc/"})
	if err != nil {
		t.Fatalf("build trigger: %v", err)
	}
	for _, want := range []string{
		`schemaVersion: "1"`,
		"queueId: 77",
		"buildNumber: 12",
		"buildUrl: http://example/job/svc/12/",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
}

// Scenario: "Trigger a parameterized build with inline values" — POST
// goes to /buildWithParameters and carries the form body verbatim.
func Test_BuildTrigger_Parameterized_FormBody(t *testing.T) {
	var (
		gotPath string
		gotForm string
	)
	srv := newMux(t).
		handle("/job/svc/api/json", func(w http.ResponseWriter, _ *http.Request) {
			// param validation probe
			fmt.Fprint(w, `{"property":[{
				"_class":"hudson.model.ParametersDefinitionProperty",
				"parameterDefinitions":[
					{"_class":"hudson.model.StringParameterDefinition","name":"BRANCH","type":"StringParameterDefinition"},
					{"_class":"hudson.model.StringParameterDefinition","name":"ENV","type":"StringParameterDefinition"}
				]}]}`)
		}).
		handle("/job/svc/buildWithParameters", func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse form: %v", err)
			}
			gotForm = r.Form.Encode()
			w.Header().Set("Location", "http://"+r.Host+"/queue/item/1/")
			w.WriteHeader(http.StatusCreated)
		}).
		handle("/queue/item/1/api/json", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"executable":{"number":3,"url":"http://example/job/svc/3/"}}`)
		}).
		server()
	defer srv.Close()

	_, _, err := runJK(t, []string{
		"build", "trigger", srv.URL + "/job/svc/",
		"-p", "BRANCH=main", "-p", "ENV=prod",
	})
	if err != nil {
		t.Fatalf("build trigger: %v", err)
	}
	if !strings.HasSuffix(gotPath, "/job/svc/buildWithParameters") {
		t.Errorf("trigger path = %q, want suffix /job/svc/buildWithParameters", gotPath)
	}
	if !strings.Contains(gotForm, "BRANCH=main") || !strings.Contains(gotForm, "ENV=prod") {
		t.Errorf("form body missing params: %q", gotForm)
	}
}

// Scenario: "Trigger with a parameter value loaded from a file" — the
// `@file` shorthand reads the file's contents and submits them
// verbatim.
func Test_BuildTrigger_ParameterFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	contents := `{"k":"v"}`
	if err := writeFile(path, contents); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	var gotConfig string
	srv := newMux(t).
		handle("/job/svc/api/json", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"property":[{
				"_class":"hudson.model.ParametersDefinitionProperty",
				"parameterDefinitions":[
					{"_class":"hudson.model.StringParameterDefinition","name":"CONFIG","type":"StringParameterDefinition"}
				]}]}`)
		}).
		handle("/job/svc/buildWithParameters", func(w http.ResponseWriter, r *http.Request) {
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse form: %v", err)
			}
			gotConfig = r.Form.Get("CONFIG")
			w.Header().Set("Location", "http://"+r.Host+"/queue/item/1/")
			w.WriteHeader(http.StatusCreated)
		}).
		handle("/queue/item/1/api/json", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"executable":{"number":1,"url":"http://example/job/svc/1/"}}`)
		}).
		server()
	defer srv.Close()

	_, _, err := runJK(t, []string{
		"build", "trigger", srv.URL + "/job/svc/",
		"-p", "CONFIG=@" + path,
	})
	if err != nil {
		t.Fatalf("build trigger: %v", err)
	}
	if gotConfig != contents {
		t.Errorf("CONFIG body = %q, want %q", gotConfig, contents)
	}
}

// Scenario: "Triggering an unknown parameter" — validation occurs
// BEFORE the build is triggered; the error mentions
// `jk pipeline params`.
func Test_BuildTrigger_UnknownParameter_Rejected(t *testing.T) {
	srv := newMux(t).
		handle("/job/svc/api/json", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"property":[{
				"_class":"hudson.model.ParametersDefinitionProperty",
				"parameterDefinitions":[
					{"_class":"hudson.model.StringParameterDefinition","name":"BRANCH","type":"StringParameterDefinition"}
				]}]}`)
		}).
		// Intentionally NO /build or /buildWithParameters handler:
		// validation must short-circuit before the POST is issued, and
		// the muxBuilder default handler will t.Errorf if we ever do.
		server()
	defer srv.Close()

	_, _, err := runJK(t, []string{
		"build", "trigger", srv.URL + "/job/svc/",
		"-p", "UNKNOWN=value",
	})
	if err == nil {
		t.Fatal("expected error for unknown parameter")
	}
	msg := err.Error()
	if !strings.Contains(msg, "UNKNOWN") {
		t.Errorf("error must name the offending parameter: %q", msg)
	}
	if !strings.Contains(msg, "jk pipeline params") {
		t.Errorf("error must point at jk pipeline params: %q", msg)
	}
	if got := jkerrors.ExitCode(err); got < 10 {
		t.Errorf("exit code = %d, want >= 10 (jk-level)", got)
	}
}

// ---------------------------------------------------------------------------
// 14.2 build trigger --watch
// ---------------------------------------------------------------------------

// Scenario: "Watch a build that succeeds" — final state DONE +
// SUCCESS yields BuildResultExitError(Code=0).
func Test_BuildTrigger_Watch_Success_ExitCode0(t *testing.T) {
	withFastWatchPoll(t)
	srv := watchScenario(t, []watchPoll{
		{building: true, result: nil},
		{building: false, result: stringPtr("SUCCESS")},
	})
	defer srv.Close()

	_, _, err := runJK(t, []string{
		"build", "trigger", srv.URL + "/job/svc/", "--watch",
	})
	if err == nil {
		t.Fatal("expected BuildResultExitError, got nil")
	}
	if code := jkerrors.ExitCode(err); code != 0 {
		t.Errorf("exit code = %d, want 0 (SUCCESS)", code)
	}
}

// Scenario: "Watch a build that fails" — FAILURE → exit 1.
func Test_BuildTrigger_Watch_Failure_ExitCode1(t *testing.T) {
	withFastWatchPoll(t)
	srv := watchScenario(t, []watchPoll{
		{building: false, result: stringPtr("FAILURE")},
	})
	defer srv.Close()

	_, _, err := runJK(t, []string{
		"build", "trigger", srv.URL + "/job/svc/", "--watch",
	})
	if code := jkerrors.ExitCode(err); code != 1 {
		t.Errorf("exit code = %d, want 1 (FAILURE), err=%v", code, err)
	}
}

// Scenario: "Watch a build that pauses for input" — PENDING_INPUT →
// exit 4 + the pending input message is printed to stderr.
func Test_BuildTrigger_Watch_PendingInput_ExitCode4(t *testing.T) {
	withFastWatchPoll(t)
	srv := newMux(t).
		handle("/job/svc/build", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "http://"+r.Host+"/queue/item/9/")
			w.WriteHeader(http.StatusCreated)
		}).
		handle("/queue/item/9/api/json", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"executable":{"number":2,"url":"http://%s/job/svc/2/"}}`, r.Host)
		}).
		handle("/job/svc/2/api/json", func(w http.ResponseWriter, r *http.Request) {
			// Always paused on input. The InputAction class triggers
			// MapBuildStatus to mark State=PENDING_INPUT.
			fmt.Fprintf(w, `{
				"number":2,"url":"http://%s/job/svc/2/",
				"building":true,"result":null,"duration":0,"estimatedDuration":-1,
				"actions":[{
					"_class":"org.jenkinsci.plugins.workflow.support.steps.input.InputAction",
					"id":"Deploy-Approval","message":"Deploy to prod?","ok":"Proceed",
					"parameters":[]
				}]
			}`, r.Host)
		}).
		server()
	defer srv.Close()

	_, stderr, err := runJK(t, []string{
		"build", "trigger", srv.URL + "/job/svc/", "--watch",
	})
	if code := jkerrors.ExitCode(err); code != 4 {
		t.Errorf("exit code = %d, want 4 (PENDING_INPUT), err=%v", code, err)
	}
	if !strings.Contains(stderr, "Deploy-Approval") {
		t.Errorf("stderr must mention pending input id:\n%s", stderr)
	}
}

// ---------------------------------------------------------------------------
// 14.3 build status
// ---------------------------------------------------------------------------

// Scenario: "Running build status" — running build returns
// state=RUNNING, building=true, result=null.
func Test_BuildStatus_Running(t *testing.T) {
	srv := newMux(t).
		handle("/job/svc/5/api/json", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{
				"number":5,"url":"http://example/job/svc/5/",
				"building":true,"result":null,
				"timestamp":1700000000000,
				"duration":12000,"estimatedDuration":60000,
				"actions":[]
			}`)
		}).
		server()
	defer srv.Close()

	stdout, _, err := runJK(t, []string{"build", "status", srv.URL + "/job/svc/5/"})
	if err != nil {
		t.Fatalf("build status: %v", err)
	}
	for _, want := range []string{
		"state: RUNNING",
		"building: true",
		"result: null",
		"progressPercent: 20", // 12000 / 60000 = 20%
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
}

// Scenario: "Completed build status" — DONE/SUCCESS + progress 100.
func Test_BuildStatus_DoneSuccess(t *testing.T) {
	srv := newMux(t).
		handle("/job/svc/5/api/json", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{
				"number":5,"url":"http://example/job/svc/5/",
				"building":false,"result":"SUCCESS",
				"timestamp":1700000000000,
				"duration":45000,"estimatedDuration":60000,
				"actions":[]
			}`)
		}).
		server()
	defer srv.Close()

	stdout, _, err := runJK(t, []string{"build", "status", srv.URL + "/job/svc/5/"})
	if err != nil {
		t.Fatalf("build status: %v", err)
	}
	for _, want := range []string{
		"state: DONE",
		"building: false",
		"result: SUCCESS",
		"progressPercent: 100",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
}

// Defensive: build status URL without a build number must fail fast
// (resolveBuildRef gate) and propose appending the number.
func Test_BuildStatus_MissingBuildNumber(t *testing.T) {
	_, _, err := runJK(t, []string{"build", "status", "https://jenkins.example/job/svc/"})
	if err == nil {
		t.Fatal("expected missing-build-number error")
	}
	if !strings.Contains(err.Error(), "build number") {
		t.Errorf("error must mention build number: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 14.4 build stages
// ---------------------------------------------------------------------------

// Scenario: "Sequential stages" — three stages in order with status,
// startTimeUtc, durationMs.
func Test_BuildStages_Sequential(t *testing.T) {
	srv := newMux(t).
		handle("/job/svc/5/wfapi/describe", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"stages":[
				{"id":"3","name":"Build","status":"SUCCESS","startTimeMillis":1700000000000,"durationMillis":1000},
				{"id":"5","name":"Test","status":"SUCCESS","startTimeMillis":1700000001000,"durationMillis":2000},
				{"id":"7","name":"Deploy","status":"IN_PROGRESS","startTimeMillis":1700000003000,"durationMillis":500}
			]}`)
		}).
		server()
	defer srv.Close()

	stdout, _, err := runJK(t, []string{"build", "stages", srv.URL + "/job/svc/5/"})
	if err != nil {
		t.Fatalf("build stages: %v", err)
	}
	for _, want := range []string{
		"name: Build", "name: Test", "name: Deploy",
		"status: SUCCESS", "status: RUNNING",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
}

// Scenario: "Parallel stages" — parent has a parallel: child list with
// independent status/duration.
func Test_BuildStages_Parallel(t *testing.T) {
	srv := newMux(t).
		handle("/job/svc/5/wfapi/describe", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"stages":[
				{"id":"3","name":"Test","status":"SUCCESS","startTimeMillis":1700000000000,"durationMillis":5000,
				 "parallel":[
					{"id":"4","name":"Unit","status":"SUCCESS","startTimeMillis":1700000000000,"durationMillis":2000},
					{"id":"5","name":"Integration","status":"FAILED","startTimeMillis":1700000000000,"durationMillis":3000}
				 ]}
			]}`)
		}).
		server()
	defer srv.Close()

	stdout, _, err := runJK(t, []string{"build", "stages", srv.URL + "/job/svc/5/"})
	if err != nil {
		t.Fatalf("build stages: %v", err)
	}
	for _, want := range []string{
		"parallel:",
		"name: Unit", "name: Integration",
		"status: FAILURE",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
}

// ---------------------------------------------------------------------------
// 14.5 build input
// ---------------------------------------------------------------------------

// Scenario: "Proceed with a single pending input".
func Test_BuildInput_SingleProceed(t *testing.T) {
	var (
		gotProceedID string
	)
	srv := newMux(t).
		handle("/job/svc/5/wfapi/pendingInputActions", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `[{"id":"Approval","proceedText":"Proceed","message":"OK?","inputs":[]}]`)
		}).
		handle("/job/svc/5/input/Approval/proceedEmpty", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			gotProceedID = "Approval"
			w.WriteHeader(http.StatusOK)
		}).
		handle("/job/svc/5/api/json", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{
				"number":5,"url":"http://example/job/svc/5/",
				"building":true,"result":null,
				"timestamp":1700000000000,"duration":0,"estimatedDuration":-1,
				"actions":[]
			}`)
		}).
		server()
	defer srv.Close()

	stdout, _, err := runJK(t, []string{"build", "input", srv.URL + "/job/svc/5/", "proceed"})
	if err != nil {
		t.Fatalf("build input proceed: %v", err)
	}
	if gotProceedID != "Approval" {
		t.Errorf("server did not receive proceed for default input; gotProceedID=%q", gotProceedID)
	}
	for _, want := range []string{
		`inputId: Approval`,
		`action: PROCEED`,
		`state: RUNNING`,
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
}

// Scenario: "Abort a single pending input".
func Test_BuildInput_SingleAbort(t *testing.T) {
	var gotAborted bool
	srv := newMux(t).
		handle("/job/svc/5/wfapi/pendingInputActions", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `[{"id":"Approval","proceedText":"Proceed","message":"OK?","inputs":[]}]`)
		}).
		handle("/job/svc/5/input/Approval/abort", func(w http.ResponseWriter, _ *http.Request) {
			gotAborted = true
			w.WriteHeader(http.StatusOK)
		}).
		handle("/job/svc/5/api/json", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{
				"number":5,"url":"http://example/job/svc/5/",
				"building":false,"result":"ABORTED",
				"timestamp":1700000000000,"duration":1000,"estimatedDuration":-1,
				"actions":[]
			}`)
		}).
		server()
	defer srv.Close()

	stdout, _, err := runJK(t, []string{"build", "input", srv.URL + "/job/svc/5/", "abort"})
	if err != nil {
		t.Fatalf("build input abort: %v", err)
	}
	if !gotAborted {
		t.Error("server did not receive abort POST")
	}
	if !strings.Contains(stdout, "action: ABORT") {
		t.Errorf("output missing action: ABORT:\n%s", stdout)
	}
}

// Scenario: "Multiple pending inputs without disambiguation".
func Test_BuildInput_MultiplePending_Ambiguous(t *testing.T) {
	srv := newMux(t).
		handle("/job/svc/5/wfapi/pendingInputActions", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `[
				{"id":"Stage-Approval","proceedText":"Proceed","message":"Stage prod?","inputs":[]},
				{"id":"Deploy-Approval","proceedText":"Proceed","message":"Deploy?","inputs":[]}
			]`)
		}).
		server()
	defer srv.Close()

	_, _, err := runJK(t, []string{"build", "input", srv.URL + "/job/svc/5/", "proceed"})
	if err == nil {
		t.Fatal("expected ambiguity error")
	}
	msg := err.Error()
	for _, want := range []string{"Stage-Approval", "Deploy-Approval", "--input-id"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error must mention %q: %q", want, msg)
		}
	}
	if got := jkerrors.ExitCode(err); got < 10 {
		t.Errorf("exit code = %d, want >= 10", got)
	}
}

// Scenario: "Multiple pending inputs with disambiguation".
func Test_BuildInput_MultiplePending_WithID(t *testing.T) {
	var hitID string
	srv := newMux(t).
		handle("/job/svc/5/wfapi/pendingInputActions", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `[
				{"id":"Stage-Approval","proceedText":"Proceed","message":"Stage prod?","inputs":[]},
				{"id":"Deploy-Approval","proceedText":"Proceed","message":"Deploy?","inputs":[]}
			]`)
		}).
		handle("/job/svc/5/input/Deploy-Approval/proceedEmpty", func(w http.ResponseWriter, _ *http.Request) {
			hitID = "Deploy-Approval"
			w.WriteHeader(http.StatusOK)
		}).
		handle("/job/svc/5/api/json", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"number":5,"url":"http://x/","building":true,"result":null,"duration":0,"estimatedDuration":-1,"actions":[]}`)
		}).
		server()
	defer srv.Close()

	_, _, err := runJK(t, []string{
		"build", "input", srv.URL + "/job/svc/5/", "proceed", "--input-id", "Deploy-Approval",
	})
	if err != nil {
		t.Fatalf("build input: %v", err)
	}
	if hitID != "Deploy-Approval" {
		t.Errorf("expected POST to Deploy-Approval, got %q", hitID)
	}
}

// ---------------------------------------------------------------------------
// 14.6 build logs
// ---------------------------------------------------------------------------

// Scenario: "Print a finished build's log".
func Test_BuildLogs_NoFollow(t *testing.T) {
	srv := newMux(t).
		handle("/job/svc/5/logText/progressiveText", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Text-Size", "12")
			fmt.Fprint(w, "hello world\n")
		}).
		server()
	defer srv.Close()

	stdout, _, err := runJK(t, []string{"build", "logs", srv.URL + "/job/svc/5/"})
	if err != nil {
		t.Fatalf("build logs: %v", err)
	}
	if !strings.Contains(stdout, "hello world") {
		t.Errorf("expected log content in stdout:\n%s", stdout)
	}
}

// Scenario: "Single stage log" — fetches /wfapi/describe to find the
// flow node ID, then /execution/node/<id>/wfapi/describe to enumerate
// child step nodes (real Jenkins puts the log text on those, not on
// the stage node itself), then /execution/node/<childId>/wfapi/log.
//
// This test exercises the "stage has child step nodes" branch; the
// no-children fallback (use the stage node's own /wfapi/log) is
// covered by Test_BuildLogs_SingleStage_NoChildren below.
func Test_BuildLogs_SingleStage(t *testing.T) {
	srv := newMux(t).
		handle("/job/svc/5/wfapi/describe", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"stages":[
				{"id":"3","name":"Build","status":"SUCCESS"},
				{"id":"7","name":"Deploy","status":"SUCCESS"}
			]}`)
		}).
		handle("/job/svc/5/execution/node/7/wfapi/describe", func(w http.ResponseWriter, _ *http.Request) {
			// The Deploy stage has two child step nodes; both
			// contribute log text and the command MUST concatenate
			// them in document order.
			fmt.Fprint(w, `{"id":"7","name":"Deploy","stageFlowNodes":[
				{"id":"8","name":"Print Message","status":"SUCCESS"},
				{"id":"9","name":"Print Message","status":"SUCCESS"}
			]}`)
		}).
		handle("/job/svc/5/execution/node/8/wfapi/log", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"text":"deploy stage output\n"}`)
		}).
		handle("/job/svc/5/execution/node/9/wfapi/log", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"text":"deploy second line\n"}`)
		}).
		server()
	defer srv.Close()

	stdout, _, err := runJK(t, []string{"build", "logs", srv.URL + "/job/svc/5/", "--stage", "Deploy"})
	if err != nil {
		t.Fatalf("build logs --stage: %v", err)
	}
	if !strings.Contains(stdout, "deploy stage output") {
		t.Errorf("expected stage log content:\n%s", stdout)
	}
	if !strings.Contains(stdout, "deploy second line") {
		t.Errorf("expected second child log line:\n%s", stdout)
	}
}

// Scenario: stage has no child step nodes; fall back to the stage
// node's own /wfapi/log endpoint. This preserves compatibility with
// older Jenkins versions where the stage node itself carries the log.
func Test_BuildLogs_SingleStage_NoChildren(t *testing.T) {
	srv := newMux(t).
		handle("/job/svc/5/wfapi/describe", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"stages":[{"id":"7","name":"Deploy","status":"SUCCESS"}]}`)
		}).
		handle("/job/svc/5/execution/node/7/wfapi/describe", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"id":"7","name":"Deploy"}`)
		}).
		handle("/job/svc/5/execution/node/7/wfapi/log", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"text":"legacy stage log\n"}`)
		}).
		server()
	defer srv.Close()

	stdout, _, err := runJK(t, []string{"build", "logs", srv.URL + "/job/svc/5/", "--stage", "Deploy"})
	if err != nil {
		t.Fatalf("build logs --stage: %v", err)
	}
	if !strings.Contains(stdout, "legacy stage log") {
		t.Errorf("expected legacy stage log:\n%s", stdout)
	}
}

// Scenario: "Stage not found" — error lists actual stage names.
func Test_BuildLogs_StageNotFound(t *testing.T) {
	srv := newMux(t).
		handle("/job/svc/5/wfapi/describe", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{"stages":[
				{"id":"3","name":"Build","status":"SUCCESS"},
				{"id":"7","name":"Deploy","status":"SUCCESS"}
			]}`)
		}).
		server()
	defer srv.Close()

	_, _, err := runJK(t, []string{"build", "logs", srv.URL + "/job/svc/5/", "--stage", "Nonexistent"})
	if err == nil {
		t.Fatal("expected stage_not_found error")
	}
	msg := err.Error()
	for _, want := range []string{"Nonexistent", "Build", "Deploy"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error must mention %q: %q", want, msg)
		}
	}
}

// ---------------------------------------------------------------------------
// extractQueueID unit test (mostly belt-and-braces; the trigger test
// covers the happy path indirectly)
// ---------------------------------------------------------------------------

func Test_ExtractQueueID(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    int
		wantErr bool
	}{
		{"trailing slash", "http://j/queue/item/77/", 77, false},
		{"no trailing slash", "http://j/queue/item/9", 9, false},
		{"multi-digit", "http://j/queue/item/12345/", 12345, false},
		{"missing path", "http://j/somewhere/else/", 0, true},
		{"non-numeric", "http://j/queue/item/abc/", 0, true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractQueueID(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got id=%d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("id = %d, want %d", got, tt.want)
			}
		})
	}
}

// parseParamFlags unit test for the `@file` path + duplicate handling.
func Test_ParseParamFlags(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v.txt")
	if err := writeFile(path, "from file\n"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := parseParamFlags([]string{"A=1", "B=@" + path, "A=2"})
	if err != nil {
		t.Fatalf("parseParamFlags: %v", err)
	}
	if got["A"] != "2" {
		t.Errorf("A = %q, want last-wins value 2", got["A"])
	}
	if got["B"] != "from file\n" {
		t.Errorf("B = %q, want file contents incl. trailing newline", got["B"])
	}

	if _, err := parseParamFlags([]string{"no-equals"}); err == nil {
		t.Error("expected error for KEY without =")
	}
	if _, err := parseParamFlags([]string{"K=@/no/such/path/" + t.Name()}); err == nil {
		t.Error("expected error for missing @file")
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// withFastWatchPoll swaps watchPollIntervalFor for a 5ms cadence so
// --watch tests don't sleep their way through a 2s initial poll, and
// restores the original on test cleanup.
func withFastWatchPoll(t *testing.T) {
	t.Helper()
	orig := watchPollIntervalFor
	watchPollIntervalFor = func(time.Duration) time.Duration { return 5 * time.Millisecond }
	t.Cleanup(func() { watchPollIntervalFor = orig })
}

// watchPoll describes one canned /api/json response for the watch
// scenario builder. Polls drain in order; once exhausted, the last
// poll's response is repeated.
type watchPoll struct {
	building bool
	result   *string
}

// watchScenario builds an httptest.Server that:
//   - accepts POST /job/svc/build and returns a queue Location;
//   - resolves the queue to build #1 on the same server;
//   - replays the supplied watchPoll list on /job/svc/1/api/json.
//
// The resolved build URL points back at the same httptest.Server so
// the watch loop's GET <buildURL>/api/json actually reaches the
// /job/svc/1/api/json handler below.
func watchScenario(t *testing.T, polls []watchPoll) *httptest.Server {
	t.Helper()
	idx := int32(-1)
	return newMux(t).
		handle("/job/svc/build", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "http://"+r.Host+"/queue/item/1/")
			w.WriteHeader(http.StatusCreated)
		}).
		handle("/queue/item/1/api/json", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"executable":{"number":1,"url":"http://%s/job/svc/1/"}}`, r.Host)
		}).
		handle("/job/svc/1/api/json", func(w http.ResponseWriter, r *http.Request) {
			i := atomic.AddInt32(&idx, 1)
			n := int(i)
			if n >= len(polls) {
				n = len(polls) - 1
			}
			p := polls[n]
			resultLiteral := "null"
			if p.result != nil {
				resultLiteral = `"` + *p.result + `"`
			}
			fmt.Fprintf(w, `{
				"number":1,"url":"http://%s/job/svc/1/",
				"building":%t,"result":%s,
				"timestamp":1700000000000,
				"duration":0,"estimatedDuration":-1,
				"actions":[]
			}`, r.Host, p.building, resultLiteral)
		}).
		server()
}

func stringPtr(s string) *string { return &s }

// writeFile is a tiny os.WriteFile shim that the tests use to seed
// `@file` fixtures. Defined here (rather than calling os.WriteFile
// directly inline) so future changes to perms or cleanup land in one
// place.
func writeFile(path, contents string) error {
	return os.WriteFile(path, []byte(contents), 0o600)
}
