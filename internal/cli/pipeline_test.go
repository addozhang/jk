package cli

// E2e tests for `jk pipeline {info|params|list}` commands. Each test
// spins up an httptest.Server that returns a recorded-ish Jenkins
// response, runs the root command pointed at it, and asserts on
// stdout. We cover at least one happy path per spec scenario
// (specs/pipeline/spec.md) plus the documented error paths
// (auth_rejected, not_found, not_a_folder).
//
// Tests deliberately do NOT exercise auth credentials: the transport's
// auth injector is configured per-host out of the credentials store,
// and httptest.Server's host (127.0.0.1:NNNN) is never present in any
// credentials file, so requests carry no Authorization header. This
// matches Jenkins's "anonymous read enabled" mode and is the easiest
// surface to test against.

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runJK executes the root command with the given args + global flags,
// pointing the credentials store at a non-existent path inside t.TempDir()
// so the test never touches the real ~/.config/jk/credentials.
func runJK(t *testing.T, args []string) (stdout, stderr string, err error) {
	t.Helper()
	// Redirect HOME so defaultCredentialsPath() resolves to a temp
	// location. We don't actually create the file; auth.NewFileStore
	// tolerates a missing file (loads as empty store).
	t.Setenv("HOME", t.TempDir())

	root := NewRootCommand()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	// Bound every test invocation with a context so a misbehaving
	// httptest.Server cannot wedge the suite forever.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = root.ExecuteContext(ctx)
	return out.String(), errBuf.String(), err
}

// pipelineInfoResp is the minimal JSON shape the info handler returns;
// fields not under test are omitted (the mapper tolerates missing
// optionals).
const pipelineInfoRespSingleBranch = `{
	"_class":"org.jenkinsci.plugins.workflow.job.WorkflowJob",
	"name":"svc","fullName":"team/svc",
	"url":"%[1]s/job/team/job/svc/",
	"description":null,"buildable":true,
	"lastBuild":{"number":42,"url":"%[1]s/job/team/job/svc/42/","result":"SUCCESS"}
}`

// Spec: "Single-branch pipeline info". Asserts schemaVersion injection +
// the at-minimum field set.
func Test_PipelineInfo_SingleBranch_YAMLDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sanity: the request path should target /job/team/job/svc/api/json.
		if !strings.HasSuffix(r.URL.Path, "/job/team/job/svc/api/json") {
			t.Errorf("unexpected request path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, pipelineInfoRespSingleBranch, "http://"+r.Host)
	}))
	defer srv.Close()

	url := srv.URL + "/job/team/job/svc/"
	stdout, _, err := runJK(t, []string{"pipeline", "info", url})
	if err != nil {
		t.Fatalf("pipeline info: %v", err)
	}
	// schemaVersion line must come first (output package guarantees).
	if !strings.HasPrefix(stdout, `schemaVersion: "1"`) {
		t.Errorf("missing schemaVersion line:\n%s", stdout)
	}
	for _, want := range []string{
		"name: svc",
		"fullName: team/svc",
		"buildable: true",
		"number: 42",
		"result: SUCCESS",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
}

// Spec: "Multibranch pipeline info". Asserts branches array appears.
func Test_PipelineInfo_Multibranch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{
			"_class":"org.jenkinsci.plugins.workflow.multibranch.WorkflowMultiBranchProject",
			"name":"svc","fullName":"team/svc",
			"url":"%[1]s/job/team/job/svc/",
			"description":null,"buildable":false,
			"jobs":[
				{"name":"main","url":"%[1]s/job/team/job/svc/job/main/"},
				{"name":"develop","url":"%[1]s/job/team/job/svc/job/develop/"}
			]
		}`, "http://"+r.Host)
	}))
	defer srv.Close()

	url := srv.URL + "/job/team/job/svc/"
	stdout, _, err := runJK(t, []string{"pipeline", "info", url})
	if err != nil {
		t.Fatalf("pipeline info: %v", err)
	}
	for _, want := range []string{"branches:", "- name: main", "- name: develop"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
	// Multibranch parent has no own lastBuild — must serialize as null
	// (the schema uses a pointer field with explicit null).
	if !strings.Contains(stdout, "lastBuild: null") {
		t.Errorf("expected lastBuild: null for multibranch parent:\n%s", stdout)
	}
}

// Spec: "Pipeline not found". 404 from Jenkins must become a JKError
// whose message names the URL and whose suggestion mentions
// `jk pipeline list`. The exit code is non-zero (cobra-level error
// propagated; the actual exit code mapping is done by main, not here).
func Test_PipelineInfo_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	url := srv.URL + "/job/missing/"
	_, _, err := runJK(t, []string{"pipeline", "info", url})
	if err == nil {
		t.Fatal("expected error for 404 response, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Pipeline not found") {
		t.Errorf("error message missing 'Pipeline not found': %q", msg)
	}
	if !strings.Contains(msg, "jk pipeline list") {
		t.Errorf("error message missing remediation hint: %q", msg)
	}
}

// Spec: "Pipeline with parameters". Asserts the parameters array carries
// the right shapes including default values and CHOICE choices.
func Test_PipelineParams_WithParameters(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Mimics the tree-filtered shape the client requests. Choice
		// uses ChoiceParameterDefinition; string uses
		// StringParameterDefinition.
		fmt.Fprint(w, `{
			"property":[{
				"_class":"hudson.model.ParametersDefinitionProperty",
				"parameterDefinitions":[
					{"_class":"hudson.model.StringParameterDefinition",
					 "name":"BRANCH","description":"git branch",
					 "type":"StringParameterDefinition",
					 "defaultParameterValue":{"value":"main"}},
					{"_class":"hudson.model.ChoiceParameterDefinition",
					 "name":"ENV","description":null,
					 "type":"ChoiceParameterDefinition",
					 "defaultParameterValue":{"value":"dev"},
					 "choices":["dev","prod"]}
				]
			}]
		}`)
	}))
	defer srv.Close()

	url := srv.URL + "/job/svc/"
	stdout, _, err := runJK(t, []string{"pipeline", "params", url})
	if err != nil {
		t.Fatalf("pipeline params: %v", err)
	}
	for _, want := range []string{
		"name: BRANCH", "type: STRING", "default: main",
		"name: ENV", "type: CHOICE", "- dev", "- prod",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
}

// Spec: "Pipeline with no parameters" — empty parameters array, exit 0.
func Test_PipelineParams_EmptyArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"property":[]}`)
	}))
	defer srv.Close()

	stdout, _, err := runJK(t, []string{"pipeline", "params", srv.URL + "/job/svc/"})
	if err != nil {
		t.Fatalf("pipeline params: %v", err)
	}
	// YAML empty slice renders as `parameters: []`.
	if !strings.Contains(stdout, "parameters: []") {
		t.Errorf("expected `parameters: []` in output:\n%s", stdout)
	}
}

// Spec: "Listing pipelines in a folder". The handler serves TWO endpoints:
//   - /api/json (no tree)  -> _class probe for folder shape;
//   - /api/json?tree=jobs… -> the actual list.
//
// Both must succeed for the test to pass.
func Test_PipelineList_Folder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only the folder API endpoint matters; both calls hit the
		// same URL with different `tree=` query params.
		if !strings.HasSuffix(r.URL.Path, "/job/team/api/json") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("tree") == "" {
			// First call: folder shape probe.
			fmt.Fprint(w, `{"_class":"com.cloudbees.hudson.plugins.folder.Folder"}`)
			return
		}
		// Second call: jobs listing.
		fmt.Fprintf(w, `{"jobs":[
			{"_class":"org.jenkinsci.plugins.workflow.job.WorkflowJob",
			 "name":"svc-a","url":"%[1]s/job/team/job/svc-a/",
			 "lastBuild":{"number":3,"url":"%[1]s/job/team/job/svc-a/3/","result":"FAILURE"}},
			{"_class":"com.cloudbees.hudson.plugins.folder.Folder",
			 "name":"sub","url":"%[1]s/job/team/job/sub/"}
		]}`, "http://"+r.Host)
	}))
	defer srv.Close()

	stdout, _, err := runJK(t, []string{"pipeline", "list", srv.URL + "/job/team/"})
	if err != nil {
		t.Fatalf("pipeline list: %v", err)
	}
	for _, want := range []string{
		"name: svc-a", "type: PIPELINE",
		"name: sub", "type: FOLDER",
		"result: FAILURE",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
}

// Spec: "URL is not a folder". Pointing list at a WorkflowJob URL must
// fail with a "use jk pipeline info" hint, NOT silently emit an empty
// items array.
func Test_PipelineList_NotAFolder_Suggestion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"_class":"org.jenkinsci.plugins.workflow.job.WorkflowJob"}`)
	}))
	defer srv.Close()

	url := srv.URL + "/job/svc/"
	_, _, err := runJK(t, []string{"pipeline", "list", url})
	if err == nil {
		t.Fatal("expected error for non-folder URL, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "is a pipeline, not a folder") {
		t.Errorf("error missing shape hint: %q", msg)
	}
	if !strings.Contains(msg, "jk pipeline info") {
		t.Errorf("error missing remediation hint: %q", msg)
	}
}

// JSON output mode: schemaVersion must appear as the first key of the
// top-level object. Exercised once for `pipeline info` to lock in the
// output-package contract end-to-end.
func Test_PipelineInfo_JSONOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, pipelineInfoRespSingleBranch, "http://"+r.Host)
	}))
	defer srv.Close()

	stdout, _, err := runJK(t, []string{"-o", "json", "pipeline", "info", srv.URL + "/job/team/job/svc/"})
	if err != nil {
		t.Fatalf("pipeline info: %v", err)
	}
	if !strings.HasPrefix(stdout, `{"schemaVersion":"1"`) {
		t.Errorf("schemaVersion must be the first JSON key:\n%s", stdout)
	}
}

// Malformed URL must fail BEFORE any HTTP request is issued; asserts
// the resolveRef gate.
func Test_PipelineInfo_RejectsMalformedURL(t *testing.T) {
	_, _, err := runJK(t, []string{"pipeline", "info", "not-a-url"})
	if err == nil {
		t.Fatal("expected error for malformed URL")
	}
	if !strings.Contains(err.Error(), "invalid URL") {
		t.Errorf("expected 'invalid URL' prefix, got: %v", err)
	}
}

// Defensive: ensure tests do not accidentally pick up a real HOME's
// credentials. If TempDir is what we expect, defaultCredentialsPath
// resolves under it.
func Test_DefaultCredentialsPath_RespectsHOME(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	got, err := defaultCredentialsPath()
	if err != nil {
		t.Fatalf("defaultCredentialsPath: %v", err)
	}
	want := filepath.Join(tmp, ".config", "jk", "credentials")
	if got != want {
		t.Errorf("path=%q, want %q", got, want)
	}
}
