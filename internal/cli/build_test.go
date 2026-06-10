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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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
			// MapBuildStatus to mark State=PENDING_INPUT. Per the
			// realistic core shape (see openspec change
			// add-input-parameter-submission §6), core /api/json
			// only carries the _class marker — id/message/parameters
			// live on wfapi/pendingInputActions below.
			fmt.Fprintf(w, `{
				"number":2,"url":"http://%s/job/svc/2/",
				"building":true,"result":null,"duration":0,"estimatedDuration":-1,
				"actions":[{
					"_class":"org.jenkinsci.plugins.workflow.support.steps.input.InputAction"
				}]
			}`, r.Host)
		}).
		handle("/job/svc/2/wfapi/pendingInputActions", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `[{
				"id":"Deploy-Approval",
				"proceedText":"Proceed",
				"message":"Deploy to prod?",
				"inputs":[]
			}]`)
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

// extend-build-addressing §4: `jk build status` MUST accept a Jenkins
// permalink (e.g. lastSuccessfulBuild) in the build-position slot,
// pass it through to Jenkins without any pre-flight lastBuild lookup,
// and surface the resolved numeric build returned by the server in
// the output. This pins both the CLI gate (resolveBuildRef) and the
// client (GetBuildStatus) to the permalink contract.
func Test_BuildStatus_AcceptsPermalink(t *testing.T) {
	srv := newMux(t).
		handle("/job/svc/lastSuccessfulBuild/api/json", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{
				"number":7,"url":"http://example/job/svc/7/",
				"building":false,"result":"SUCCESS",
				"timestamp":1700000000000,
				"duration":45000,"estimatedDuration":60000,
				"actions":[]
			}`)
		}).
		server()
	defer srv.Close()

	stdout, _, err := runJK(t, []string{"build", "status", srv.URL + "/job/svc/lastSuccessfulBuild/"})
	if err != nil {
		t.Fatalf("build status (permalink): %v", err)
	}
	for _, want := range []string{
		"buildNumber: 7",
		"state: DONE",
		"result: SUCCESS",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
}

// Scenario from openspec change add-input-parameter-submission §6,
// scenario "Live paused build populates pendingInput from wfapi":
// when core /api/json reports building==true with an InputAction
// marker but no id/message/parameters fields (the realistic shape),
// the status command MUST additionally call
// /<n>/wfapi/pendingInputActions and populate the pendingInput block
// from that response.
func Test_BuildStatus_LivePausedEnrichesFromWfapi(t *testing.T) {
	srv := newMux(t).
		handle("/job/svc/5/api/json", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{
				"number":5,"url":"http://example/job/svc/5/",
				"building":true,"result":null,
				"timestamp":1700000000000,
				"duration":1000,"estimatedDuration":60000,
				"actions":[
					{"_class":"org.jenkinsci.plugins.workflow.support.steps.input.InputAction"}
				]
			}`)
		}).
		handle("/job/svc/5/wfapi/pendingInputActions", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `[{
				"id":"Deploy",
				"proceedText":"Deploy",
				"message":"Deploy to which environment?",
				"inputs":[
					{"_class":"hudson.model.ChoiceParameterDefinition",
					 "name":"ENV","type":"ChoiceParameterDefinition",
					 "choices":["staging","prod"],
					 "defaultParameterValue":{"value":"staging"}},
					{"_class":"hudson.model.BooleanParameterDefinition",
					 "name":"DRY_RUN","type":"BooleanParameterDefinition",
					 "defaultParameterValue":{"value":true}}
				]
			}]`)
		}).
		server()
	defer srv.Close()

	stdout, _, err := runJK(t, []string{"build", "status", srv.URL + "/job/svc/5/"})
	if err != nil {
		t.Fatalf("build status: %v", err)
	}
	for _, want := range []string{
		"state: PENDING_INPUT",
		"id: Deploy",
		"message: Deploy to which environment?",
		"name: ENV",
		"name: DRY_RUN",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
}

// Scenario "Finished build with historical InputAction reports DONE":
// a build that has already completed must report state==DONE even
// when actions[] still carries the historical InputAction marker, and
// MUST NOT make the secondary wfapi call. The newMux catch-all
// t.Errorf's on any unhandled path, so registering only the /api/json
// handler is sufficient to assert "wfapi was not called."
func Test_BuildStatus_FinishedBuildDoesNotEnrich(t *testing.T) {
	srv := newMux(t).
		handle("/job/svc/5/api/json", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{
				"number":5,"url":"http://example/job/svc/5/",
				"building":false,"result":"SUCCESS",
				"timestamp":1700000000000,
				"duration":65000,"estimatedDuration":60000,
				"actions":[
					{"_class":"org.jenkinsci.plugins.workflow.support.steps.input.InputAction"}
				]
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
		"result: SUCCESS",
		"progressPercent: 100",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
	// PENDING_INPUT must not leak in even though the historical
	// marker is in actions[].
	if strings.Contains(stdout, "PENDING_INPUT") {
		t.Errorf("finished build must not report PENDING_INPUT:\n%s", stdout)
	}
}

// Scenario "wfapi enrichment failure degrades gracefully": if the
// secondary /wfapi/pendingInputActions call returns HTTP 500, the
// status command MUST NOT fail. It logs to stderr under --debug,
// omits the pendingInput block, and falls back to state=RUNNING.
func Test_BuildStatus_WfapiEnrichmentFailureDegradesGracefully(t *testing.T) {
	srv := newMux(t).
		handle("/job/svc/5/api/json", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{
				"number":5,"url":"http://example/job/svc/5/",
				"building":true,"result":null,
				"timestamp":1700000000000,
				"duration":1000,"estimatedDuration":60000,
				"actions":[
					{"_class":"org.jenkinsci.plugins.workflow.support.steps.input.InputAction"}
				]
			}`)
		}).
		handle("/job/svc/5/wfapi/pendingInputActions", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}).
		server()
	defer srv.Close()

	stdout, stderr, err := runJK(t, []string{
		"--debug",
		"build", "status", srv.URL + "/job/svc/5/",
	})
	if err != nil {
		t.Fatalf("build status: %v (want exit 0 on enrichment failure)", err)
	}
	if !strings.Contains(stdout, "state: RUNNING") {
		t.Errorf("expected state: RUNNING fallback, got:\n%s", stdout)
	}
	// pendingInput.id should be empty / the block should serialize
	// to null. Easier to assert: PENDING_INPUT must NOT appear.
	if strings.Contains(stdout, "PENDING_INPUT") {
		t.Errorf("must not report PENDING_INPUT when enrichment failed:\n%s", stdout)
	}
	// Under --debug we surface a log line about the failure.
	if !strings.Contains(stderr, "pendingInputActions") {
		t.Errorf("--debug stderr must mention the wfapi enrichment failure:\n%s", stderr)
	}
}

// Scenario "Race — input submitted between the two HTTP calls": the
// marker is in actions[] but wfapi returns []. The command MUST fall
// back to state=RUNNING and omit pendingInput, exit 0.
func Test_BuildStatus_InputSubmittedRaceReturnsRunning(t *testing.T) {
	srv := newMux(t).
		handle("/job/svc/5/api/json", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{
				"number":5,"url":"http://example/job/svc/5/",
				"building":true,"result":null,
				"timestamp":1700000000000,
				"duration":1000,"estimatedDuration":60000,
				"actions":[
					{"_class":"org.jenkinsci.plugins.workflow.support.steps.input.InputAction"}
				]
			}`)
		}).
		handle("/job/svc/5/wfapi/pendingInputActions", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `[]`)
		}).
		server()
	defer srv.Close()

	stdout, _, err := runJK(t, []string{"build", "status", srv.URL + "/job/svc/5/"})
	if err != nil {
		t.Fatalf("build status: %v", err)
	}
	if !strings.Contains(stdout, "state: RUNNING") {
		t.Errorf("expected state: RUNNING on race condition, got:\n%s", stdout)
	}
	if strings.Contains(stdout, "PENDING_INPUT") {
		t.Errorf("must not report PENDING_INPUT when wfapi returns []:\n%s", stdout)
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
// 14.5b build input -p (v0.2 add-input-parameter-submission)
// ---------------------------------------------------------------------------

// pendingInputEnvOnly declares one CHOICE param with a default. Used
// for "all defaults" and "invalid choice" tests.
const pendingInputEnvOnly = `[{
	"id":"Deploy","proceedText":"Deploy","message":"Pick env",
	"inputs":[
		{"type":"ChoiceParameterDefinition","name":"ENV",
		 "definition":{"defaultVal":"staging","choices":["staging","prod"]}}
	]
}]`

// pendingInputEnvNoDefault declares ENV without a default — submission
// without -p must fail validation.
const pendingInputEnvNoDefault = `[{
	"id":"Deploy","proceedText":"Deploy","message":"Pick env",
	"inputs":[
		{"type":"ChoiceParameterDefinition","name":"ENV",
		 "definition":{"choices":["staging","prod"]}}
	]
}]`

// stubBuildStatus returns a minimal post-submit /api/json response.
func stubBuildStatus(w http.ResponseWriter, _ *http.Request) {
	fmt.Fprint(w, `{"number":5,"url":"http://example/job/svc/5/","building":true,"result":null,"timestamp":1700000000000,"duration":0,"estimatedDuration":-1,"actions":[]}`)
}

// decodeSubmitForm parses the form-encoded body of a /submit POST and
// returns the inner JSON's parameter list as a name->value map.
func decodeSubmitForm(t *testing.T, r *http.Request) map[string]string {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type=%q, want application/x-www-form-urlencoded", ct)
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		t.Fatalf("parse form: %v (body=%q)", err, body)
	}
	jsonField := form.Get("json")
	if jsonField == "" {
		t.Fatalf("body missing json field: %q", body)
	}
	var inner struct {
		Parameter []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"parameter"`
	}
	if err := json.Unmarshal([]byte(jsonField), &inner); err != nil {
		t.Fatalf("inner JSON: %v (raw=%q)", err, jsonField)
	}
	out := make(map[string]string, len(inner.Parameter))
	for _, p := range inner.Parameter {
		out[p.Name] = p.Value
	}
	return out
}

// 3.1 Submit a single CHOICE parameter via -p ENV=prod.
func Test_BuildInput_SubmitSingleChoice(t *testing.T) {
	var got map[string]string
	srv := newMux(t).
		handle("/job/svc/5/wfapi/pendingInputActions", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, pendingInputEnvOnly)
		}).
		handle("/job/svc/5/input/Deploy/submit", func(w http.ResponseWriter, r *http.Request) {
			got = decodeSubmitForm(t, r)
			w.WriteHeader(http.StatusOK)
		}).
		handle("/job/svc/5/api/json", stubBuildStatus).
		server()
	defer srv.Close()

	_, _, err := runJK(t, []string{"build", "input", srv.URL + "/job/svc/5/", "proceed", "-p", "ENV=prod"})
	if err != nil {
		t.Fatalf("build input: %v", err)
	}
	if got["ENV"] != "prod" {
		t.Errorf("submitted ENV=%q, want prod (full=%v)", got["ENV"], got)
	}
}

// 3.2 Mixed types incl. @file → value loaded from file.
func Test_BuildInput_SubmitMixedTypesIncludingAtFile(t *testing.T) {
	notesFile := t.TempDir() + "/notes.txt"
	if err := os.WriteFile(notesFile, []byte("multi\nline notes"), 0o600); err != nil {
		t.Fatalf("write tempfile: %v", err)
	}

	const pending = `[{
		"id":"Release","proceedText":"Release","message":"Release",
		"inputs":[
			{"type":"ChoiceParameterDefinition","name":"ENV",
			 "definition":{"defaultVal":"staging","choices":["staging","prod"]}},
			{"type":"BooleanParameterDefinition","name":"DRY_RUN",
			 "definition":{"defaultVal":true}},
			{"type":"TextParameterDefinition","name":"NOTES",
			 "definition":{"defaultVal":""}}
		]
	}]`

	var got map[string]string
	srv := newMux(t).
		handle("/job/svc/5/wfapi/pendingInputActions", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, pending)
		}).
		handle("/job/svc/5/input/Release/submit", func(w http.ResponseWriter, r *http.Request) {
			got = decodeSubmitForm(t, r)
			w.WriteHeader(http.StatusOK)
		}).
		handle("/job/svc/5/api/json", stubBuildStatus).
		server()
	defer srv.Close()

	_, _, err := runJK(t, []string{
		"build", "input", srv.URL + "/job/svc/5/", "proceed",
		"-p", "ENV=prod",
		"-p", "DRY_RUN=false",
		"-p", "NOTES=@" + notesFile,
	})
	if err != nil {
		t.Fatalf("build input: %v", err)
	}
	if got["ENV"] != "prod" || got["DRY_RUN"] != "false" {
		t.Errorf("got=%v", got)
	}
	if got["NOTES"] != "multi\nline notes" {
		t.Errorf("NOTES=%q, want multi\\nline notes", got["NOTES"])
	}
}

// 3.3 Unknown -p key → exit 10, no HTTP submit.
func Test_BuildInput_UnknownParamKey(t *testing.T) {
	submitHit := false
	srv := newMux(t).
		handle("/job/svc/5/wfapi/pendingInputActions", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, pendingInputEnvOnly)
		}).
		handle("/job/svc/5/input/Deploy/submit", func(w http.ResponseWriter, _ *http.Request) {
			submitHit = true
			w.WriteHeader(http.StatusOK)
		}).
		server()
	defer srv.Close()

	_, _, err := runJK(t, []string{"build", "input", srv.URL + "/job/svc/5/", "proceed", "-p", "REGION=eu"})
	if err == nil {
		t.Fatal("expected error for unknown -p key")
	}
	if submitHit {
		t.Error("submit endpoint must not be called when validation fails")
	}
	if got := jkerrors.ExitCode(err); got != 10 {
		t.Errorf("exit code = %d, want 10", got)
	}
	if !strings.Contains(err.Error(), "ENV") {
		t.Errorf("error must list valid parameter names: %q", err.Error())
	}
}

// 3.4 Invalid choice value → exit 10, error lists choices.
func Test_BuildInput_InvalidChoice(t *testing.T) {
	srv := newMux(t).
		handle("/job/svc/5/wfapi/pendingInputActions", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, pendingInputEnvOnly)
		}).
		server()
	defer srv.Close()

	_, _, err := runJK(t, []string{"build", "input", srv.URL + "/job/svc/5/", "proceed", "-p", "ENV=devvv"})
	if err == nil {
		t.Fatal("expected error for invalid choice")
	}
	if got := jkerrors.ExitCode(err); got != 10 {
		t.Errorf("exit code = %d, want 10", got)
	}
	msg := err.Error()
	for _, want := range []string{"staging", "prod"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error must list choice %q: %q", want, msg)
		}
	}
}

// 3.5 Required param missing (no default, no -p) → exit 10.
func Test_BuildInput_RequiredParamMissing(t *testing.T) {
	srv := newMux(t).
		handle("/job/svc/5/wfapi/pendingInputActions", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, pendingInputEnvNoDefault)
		}).
		server()
	defer srv.Close()

	_, _, err := runJK(t, []string{"build", "input", srv.URL + "/job/svc/5/", "proceed"})
	if err == nil {
		t.Fatal("expected error for missing required param")
	}
	if got := jkerrors.ExitCode(err); got != 10 {
		t.Errorf("exit code = %d, want 10", got)
	}
	if !strings.Contains(err.Error(), "ENV") {
		t.Errorf("error must name missing param ENV: %q", err.Error())
	}
}

// 3.6 All defaults available + no -p → posts /submit with the default
// value (not /proceedEmpty). A declared-but-defaulted input requires
// the parameterized endpoint because Jenkins doesn't infer defaults
// for `input` steps the way it does for parameterized builds.
func Test_BuildInput_AllDefaultsUsesSubmit(t *testing.T) {
	var got map[string]string
	proceedEmptyHit := false
	srv := newMux(t).
		handle("/job/svc/5/wfapi/pendingInputActions", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, pendingInputEnvOnly)
		}).
		handle("/job/svc/5/input/Deploy/submit", func(w http.ResponseWriter, r *http.Request) {
			got = decodeSubmitForm(t, r)
			w.WriteHeader(http.StatusOK)
		}).
		handle("/job/svc/5/input/Deploy/proceedEmpty", func(w http.ResponseWriter, _ *http.Request) {
			proceedEmptyHit = true
			w.WriteHeader(http.StatusOK)
		}).
		handle("/job/svc/5/api/json", stubBuildStatus).
		server()
	defer srv.Close()

	_, _, err := runJK(t, []string{"build", "input", srv.URL + "/job/svc/5/", "proceed"})
	if err != nil {
		t.Fatalf("build input: %v", err)
	}
	if proceedEmptyHit {
		t.Error("proceedEmpty must not be called when params are declared (defaults must be sent via /submit)")
	}
	if got["ENV"] != "staging" {
		t.Errorf("default ENV=%q, want staging", got["ENV"])
	}
}

// 3.7 Zero declared params + no -p → /proceedEmpty (v0.1 regression).
func Test_BuildInput_ZeroParamsUsesProceedEmpty(t *testing.T) {
	submitHit := false
	proceedEmptyHit := false
	srv := newMux(t).
		handle("/job/svc/5/wfapi/pendingInputActions", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `[{"id":"Approval","proceedText":"OK","message":"go?","inputs":[]}]`)
		}).
		handle("/job/svc/5/input/Approval/proceedEmpty", func(w http.ResponseWriter, _ *http.Request) {
			proceedEmptyHit = true
			w.WriteHeader(http.StatusOK)
		}).
		handle("/job/svc/5/input/Approval/submit", func(w http.ResponseWriter, _ *http.Request) {
			submitHit = true
			w.WriteHeader(http.StatusOK)
		}).
		handle("/job/svc/5/api/json", stubBuildStatus).
		server()
	defer srv.Close()

	_, _, err := runJK(t, []string{"build", "input", srv.URL + "/job/svc/5/", "proceed"})
	if err != nil {
		t.Fatalf("build input: %v", err)
	}
	if !proceedEmptyHit {
		t.Error("proceedEmpty must be hit when zero params declared")
	}
	if submitHit {
		t.Error("submit must NOT be hit when zero params declared")
	}
}

// 3.8 abort + -p → POST /abort, warning to stderr, exit 0.
func Test_BuildInput_AbortIgnoresParams(t *testing.T) {
	abortHit := false
	srv := newMux(t).
		handle("/job/svc/5/wfapi/pendingInputActions", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, pendingInputEnvOnly)
		}).
		handle("/job/svc/5/input/Deploy/abort", func(w http.ResponseWriter, _ *http.Request) {
			abortHit = true
			w.WriteHeader(http.StatusOK)
		}).
		handle("/job/svc/5/api/json", stubBuildStatus).
		server()
	defer srv.Close()

	_, stderr, err := runJK(t, []string{"build", "input", srv.URL + "/job/svc/5/", "abort", "-p", "ENV=prod"})
	if err != nil {
		t.Fatalf("build input abort: %v", err)
	}
	if !abortHit {
		t.Error("abort endpoint must be hit")
	}
	if !strings.Contains(stderr, "-p") || !strings.Contains(stderr, "ignored") {
		t.Errorf("stderr must warn about ignored -p: %q", stderr)
	}
}

// 3.9 BOOLEAN accepts true/True/TRUE/false/False/1/0; rejects others.
func Test_BuildInput_BooleanParsing(t *testing.T) {
	accept := []string{"true", "True", "TRUE", "false", "False", "1", "0"}
	reject := []string{"yes", "no", "maybe"}

	const pending = `[{
		"id":"Deploy","proceedText":"Deploy","message":"go?",
		"inputs":[
			{"type":"BooleanParameterDefinition","name":"DRY_RUN",
			 "definition":{"defaultVal":true}}
		]
	}]`

	for _, v := range accept {
		t.Run("accept_"+v, func(t *testing.T) {
			submitted := false
			srv := newMux(t).
				handle("/job/svc/5/wfapi/pendingInputActions", func(w http.ResponseWriter, _ *http.Request) {
					fmt.Fprint(w, pending)
				}).
				handle("/job/svc/5/input/Deploy/submit", func(w http.ResponseWriter, _ *http.Request) {
					submitted = true
					w.WriteHeader(http.StatusOK)
				}).
				handle("/job/svc/5/api/json", stubBuildStatus).
				server()
			defer srv.Close()

			_, _, err := runJK(t, []string{"build", "input", srv.URL + "/job/svc/5/", "proceed", "-p", "DRY_RUN=" + v})
			if err != nil {
				t.Fatalf("accept %q: %v", v, err)
			}
			if !submitted {
				t.Errorf("submit not hit for %q", v)
			}
		})
	}
	for _, v := range reject {
		t.Run("reject_"+v, func(t *testing.T) {
			srv := newMux(t).
				handle("/job/svc/5/wfapi/pendingInputActions", func(w http.ResponseWriter, _ *http.Request) {
					fmt.Fprint(w, pending)
				}).
				server()
			defer srv.Close()

			_, _, err := runJK(t, []string{"build", "input", srv.URL + "/job/svc/5/", "proceed", "-p", "DRY_RUN=" + v})
			if err == nil {
				t.Fatalf("expected error for %q", v)
			}
			if got := jkerrors.ExitCode(err); got != 10 {
				t.Errorf("exit code = %d, want 10 for %q", got, v)
			}
		})
	}
}

// 3.10 When wfapi advertises `proceedUrl` for the pending input,
// `jk build input proceed -p …` MUST POST to that path (typically
// /wfapi/inputSubmit?inputId=<id>) and NOT to the legacy
// /input/<id>/submit. Confirmed against deploy-input harness build
// #20: the legacy path silently records "Rejected by <user>" and
// aborts the build, while wfapi/inputSubmit succeeds.
func Test_BuildInput_UsesWfapiProceedURL(t *testing.T) {
	const pending = `[{
		"id":"Deploy","proceedText":"Deploy","message":"Pick env",
		"proceedUrl":"/job/svc/5/wfapi/inputSubmit?inputId=Deploy",
		"inputs":[
			{"type":"ChoiceParameterDefinition","name":"ENV",
			 "definition":{"defaultVal":"staging","choices":["staging","prod"]}}
		]
	}]`

	var (
		wfapiHit  bool
		legacyHit bool
		got       map[string]string
	)
	srv := newMux(t).
		handle("/job/svc/5/wfapi/pendingInputActions", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, pending)
		}).
		handle("/job/svc/5/wfapi/inputSubmit", func(w http.ResponseWriter, r *http.Request) {
			wfapiHit = true
			got = decodeSubmitForm(t, r)
			if q := r.URL.Query().Get("inputId"); q != "Deploy" {
				t.Errorf("inputId query=%q, want Deploy", q)
			}
			w.WriteHeader(http.StatusOK)
		}).
		handle("/job/svc/5/input/Deploy/submit", func(w http.ResponseWriter, _ *http.Request) {
			legacyHit = true
			w.WriteHeader(http.StatusOK)
		}).
		handle("/job/svc/5/api/json", stubBuildStatus).
		server()
	defer srv.Close()

	_, _, err := runJK(t, []string{"build", "input", srv.URL + "/job/svc/5/", "proceed", "-p", "ENV=prod"})
	if err != nil {
		t.Fatalf("build input: %v", err)
	}
	if !wfapiHit {
		t.Error("expected wfapi/inputSubmit to be hit")
	}
	if legacyHit {
		t.Error("legacy /input/<id>/submit MUST NOT be hit when wfapi proceedUrl is advertised")
	}
	if got["ENV"] != "prod" {
		t.Errorf("submitted ENV=%q, want prod (full=%v)", got["ENV"], got)
	}
}

// ---------------------------------------------------------------------------
// 14.5 build cancel
// ---------------------------------------------------------------------------

// Scenario: "Cancel a running build without --wait" — POST /stop,
// then re-fetch /api/json; output carries the state at request time.
func Test_BuildCancel_RunningNoWait(t *testing.T) {
	stopHit := false
	srv := newMux(t).
		handle("/job/svc/42/stop", func(w http.ResponseWriter, r *http.Request) {
			stopHit = true
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			w.WriteHeader(http.StatusOK)
		}).
		handle("/job/svc/42/api/json", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{
				"number":42,"url":"http://example/job/svc/42/",
				"building":true,"result":null,
				"timestamp":1700000000000,
				"duration":5000,"estimatedDuration":60000,
				"actions":[]
			}`)
		}).
		server()
	defer srv.Close()

	stdout, _, err := runJK(t, []string{"build", "cancel", srv.URL + "/job/svc/42/"})
	if err != nil {
		t.Fatalf("build cancel: %v", err)
	}
	if !stopHit {
		t.Error("expected /stop to be hit")
	}
	for _, want := range []string{
		`schemaVersion: "1"`,
		"buildNumber: 42",
		"state: RUNNING",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
}

// A 404 from /stop means the build URL is wrong; the command MUST exit
// non-zero with a "Build not found" error (not "Pipeline not found").
func Test_BuildCancel_NotFound(t *testing.T) {
	srv := newMux(t).
		handle("/job/svc/999/stop", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}).
		server()
	defer srv.Close()

	_, _, err := runJK(t, []string{"build", "cancel", srv.URL + "/job/svc/999/"})
	if err == nil {
		t.Fatal("expected not_found error, got nil")
	}
	if !strings.Contains(err.Error(), "Build not found") {
		t.Errorf("error must say 'Build not found': %v", err)
	}
}

// Missing build number must be rejected by the resolveBuildRef gate
// before any HTTP call.
func Test_BuildCancel_MissingBuildNumber(t *testing.T) {
	_, _, err := runJK(t, []string{"build", "cancel", "https://jenkins.example/job/svc/"})
	if err == nil {
		t.Fatal("expected missing-build-number error")
	}
	if !strings.Contains(err.Error(), "build number") {
		t.Errorf("error must mention build number: %v", err)
	}
}

// cancel MUST accept a permalink (e.g. lastBuild) in the build slot,
// mirroring build status/params. The /stop POST is addressed at the
// permalink and the re-fetched status reports the resolved number.
func Test_BuildCancel_AcceptsPermalink(t *testing.T) {
	stopHit := false
	srv := newMux(t).
		handle("/job/svc/lastBuild/stop", func(w http.ResponseWriter, r *http.Request) {
			stopHit = true
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			w.WriteHeader(http.StatusOK)
		}).
		handle("/job/svc/lastBuild/api/json", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{
				"number":42,"url":"http://example/job/svc/42/",
				"building":true,"result":null,
				"timestamp":1700000000000,
				"duration":5000,"estimatedDuration":60000,
				"actions":[]
			}`)
		}).
		server()
	defer srv.Close()

	stdout, _, err := runJK(t, []string{"build", "cancel", srv.URL + "/job/svc/lastBuild/"})
	if err != nil {
		t.Fatalf("build cancel (permalink): %v", err)
	}
	if !stopHit {
		t.Error("expected /lastBuild/stop to be hit")
	}
	if !strings.Contains(stdout, "buildNumber: 42") {
		t.Errorf("output must report the resolved build number:\n%s", stdout)
	}
}

// Scenario: "Cancel a running build with --wait" — after POST /stop,
// the command polls until DONE and exits with code 3 (ABORTED). The
// poll passes through PENDING_INPUT without treating it as terminal.
func Test_BuildCancel_WaitExitsAborted(t *testing.T) {
	withFastWatchPoll(t)
	var polls int32
	srv := newMux(t).
		handle("/job/svc/42/stop", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}).
		handle("/job/svc/42/api/json", func(w http.ResponseWriter, r *http.Request) {
			n := atomic.AddInt32(&polls, 1)
			// First poll: still paused on input (must NOT be treated
			// as terminal). Second poll: aborted.
			if n == 1 {
				fmt.Fprintf(w, `{
					"number":42,"url":"http://%s/job/svc/42/",
					"building":true,"result":null,
					"timestamp":1700000000000,
					"duration":5000,"estimatedDuration":-1,
					"actions":[{
						"_class":"org.jenkinsci.plugins.workflow.support.steps.input.InputAction"
					}]
				}`, r.Host)
				return
			}
			fmt.Fprintf(w, `{
				"number":42,"url":"http://%s/job/svc/42/",
				"building":false,"result":"ABORTED",
				"timestamp":1700000000000,
				"duration":8000,"estimatedDuration":-1,
				"actions":[]
			}`, r.Host)
		}).
		server()
	defer srv.Close()

	_, _, err := runJK(t, []string{"build", "cancel", srv.URL + "/job/svc/42/", "--wait"})
	if code := jkerrors.ExitCode(err); code != 3 {
		t.Errorf("exit code = %d, want 3 (ABORTED), err=%v", code, err)
	}
	if atomic.LoadInt32(&polls) < 2 {
		t.Errorf("expected at least 2 status polls (pass through PENDING_INPUT), got %d", polls)
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

// ---------------------------------------------------------------------------
// extend-build-addressing §10: `jk build params <url>`
// ---------------------------------------------------------------------------

// Happy path: a build triggered with two parameters renders both as
// {name,value} entries plus the resolved buildNumber/buildUrl.
func Test_BuildParams_Numeric(t *testing.T) {
	srv := newMux(t).
		handle("/job/svc/42/api/json", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{
				"number":42,"url":"http://example/job/svc/42/",
				"actions":[
					{"_class":"hudson.model.CauseAction"},
					{"_class":"hudson.model.ParametersAction","parameters":[
						{"name":"ENV","value":"prod"},
						{"name":"DRY_RUN","value":false}
					]}
				]
			}`)
		}).
		server()
	defer srv.Close()

	stdout, _, err := runJK(t, []string{"build", "params", srv.URL + "/job/svc/42/"})
	if err != nil {
		t.Fatalf("build params: %v", err)
	}
	for _, want := range []string{
		`schemaVersion: "1"`,
		"buildNumber: 42",
		"name: ENV",
		"value: prod",
		"name: DRY_RUN",
		"value: false",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
}

// Unparameterized build: parameters MUST render as empty array, never
// null, never an error.
func Test_BuildParams_EmptyArray(t *testing.T) {
	srv := newMux(t).
		handle("/job/hello/3/api/json", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{
				"number":3,"url":"http://example/job/hello/3/",
				"actions":[{"_class":"hudson.model.CauseAction"}]
			}`)
		}).
		server()
	defer srv.Close()

	stdout, _, err := runJK(t, []string{"build", "params", srv.URL + "/job/hello/3/", "-o", "json"})
	if err != nil {
		t.Fatalf("build params: %v", err)
	}
	if !strings.Contains(stdout, `"parameters":[]`) {
		t.Errorf("expected parameters:[] in json output:\n%s", stdout)
	}
}

// Permalink synergy: `jk build params <url>/lastSuccessfulBuild/`
// MUST succeed and report the resolved numeric build number.
func Test_BuildParams_Permalink(t *testing.T) {
	srv := newMux(t).
		handle("/job/svc/lastSuccessfulBuild/api/json", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, `{
				"number":17,"url":"http://example/job/svc/17/",
				"actions":[
					{"_class":"hudson.model.ParametersAction","parameters":[
						{"name":"ENV","value":"staging"}
					]}
				]
			}`)
		}).
		server()
	defer srv.Close()

	stdout, _, err := runJK(t, []string{"build", "params", srv.URL + "/job/svc/lastSuccessfulBuild/"})
	if err != nil {
		t.Fatalf("build params (permalink): %v", err)
	}
	for _, want := range []string{
		"buildNumber: 17",
		"name: ENV",
		"value: staging",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
}

// Missing build address must be rejected by the resolveBuildRef gate
// with a friendly suggestion (same gating story as `build status`).
func Test_BuildParams_MissingBuildNumber(t *testing.T) {
	_, _, err := runJK(t, []string{"build", "params", "https://jenkins.example/job/svc/"})
	if err == nil {
		t.Fatal("expected missing-build-number error")
	}
	if !strings.Contains(err.Error(), "build number") {
		t.Errorf("error must mention build number: %v", err)
	}
}
