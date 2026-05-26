package errors_test

import (
	"context"
	stderrors "errors"
	"net"
	"testing"
	"time"

	jkerrors "github.com/addozhang/jk/internal/errors"
)

func Test_JKError_Error_IncludesMessageAndSuggestion(t *testing.T) {
	e := &jkerrors.JKError{
		Code:       "auth_rejected",
		Message:    "API token rejected by https://jenkins.example.com.",
		Suggestion: "Run: jk auth add https://jenkins.example.com to refresh.",
	}
	got := e.Error()
	for _, want := range []string{
		"API token rejected by https://jenkins.example.com.",
		"Run: jk auth add https://jenkins.example.com to refresh.",
	} {
		if !contains(got, want) {
			t.Errorf("Error() missing %q in %q", want, got)
		}
	}
}

func Test_JKError_Unwrap_ReturnsCause(t *testing.T) {
	root := stderrors.New("connection refused")
	e := &jkerrors.JKError{Code: "network", Message: "net", Cause: root}
	if !stderrors.Is(e, root) {
		t.Errorf("errors.Is should walk through Cause")
	}
}

func Test_JKError_ExitCode_IsJKLevel(t *testing.T) {
	// Per errors spec: jk-level failures exit with code >= 10.
	e := &jkerrors.JKError{Code: "any", Message: "x"}
	if e.ExitCode() < 10 {
		t.Errorf("JKError.ExitCode() = %d, want >= 10", e.ExitCode())
	}
}

// ---------------------------------------------------------------------------
// Translators
// ---------------------------------------------------------------------------

func Test_NewAuthRejected_MatchesSpecPhrasing(t *testing.T) {
	e := jkerrors.NewAuthRejected("https://jenkins.example.com")
	want := "API token rejected by https://jenkins.example.com"
	if !contains(e.Error(), want) {
		t.Errorf("missing %q in %q", want, e.Error())
	}
	if !contains(e.Error(), "jk auth add https://jenkins.example.com") {
		t.Errorf("missing remediation hint in %q", e.Error())
	}
	if e.Code != "auth_rejected" {
		t.Errorf("Code = %q, want auth_rejected", e.Code)
	}
}

func Test_NewNotFound_MatchesSpecPhrasing(t *testing.T) {
	u := "https://jenkins.example.com/job/team/job/svc/"
	e := jkerrors.NewNotFound(u)
	if !contains(e.Error(), "Pipeline not found: "+u) {
		t.Errorf("missing 'Pipeline not found' phrasing in %q", e.Error())
	}
	if !contains(e.Error(), "jk pipeline list") {
		t.Errorf("missing 'jk pipeline list' hint in %q", e.Error())
	}
	if e.Code != "not_found" {
		t.Errorf("Code = %q, want not_found", e.Code)
	}
}

func Test_NewTimeout_IncludesDurationAndHost(t *testing.T) {
	e := jkerrors.NewTimeout("https://jenkins.example.com", 30*time.Second)
	if !contains(e.Error(), "30s") {
		t.Errorf("missing duration '30s' in %q", e.Error())
	}
	if !contains(e.Error(), "https://jenkins.example.com") {
		t.Errorf("missing host in %q", e.Error())
	}
	if !contains(e.Error(), "--timeout") {
		t.Errorf("missing --timeout suggestion in %q", e.Error())
	}
	if e.Code != "timeout" {
		t.Errorf("Code = %q, want timeout", e.Code)
	}
}

func Test_NewCrumbFailed_NamesHostAndAsksToFileIssue(t *testing.T) {
	e := jkerrors.NewCrumbFailed("https://jenkins.example.com")
	if !contains(e.Error(), "https://jenkins.example.com") {
		t.Errorf("missing host in %q", e.Error())
	}
	if !contains(e.Error(), "CSRF") && !contains(e.Error(), "crumb") {
		t.Errorf("expected CSRF/crumb mention in %q", e.Error())
	}
	if !contains(e.Error(), "issue") {
		t.Errorf("expected file-an-issue suggestion in %q", e.Error())
	}
	if e.Code != "crumb_failed" {
		t.Errorf("Code = %q, want crumb_failed", e.Code)
	}
}

func Test_NewTLSCertFile_NamesEnvAndPath(t *testing.T) {
	e := jkerrors.NewTLSCertFile("/etc/ssl/missing.pem", stderrors.New("open: no such file"))
	if !contains(e.Error(), "SSL_CERT_FILE") {
		t.Errorf("missing SSL_CERT_FILE in %q", e.Error())
	}
	if !contains(e.Error(), "/etc/ssl/missing.pem") {
		t.Errorf("missing path in %q", e.Error())
	}
	if e.Code != "tls_cert_file" {
		t.Errorf("Code = %q, want tls_cert_file", e.Code)
	}
}

func Test_NewMalformedResponse_NamesHost(t *testing.T) {
	e := jkerrors.NewMalformedResponse("https://jenkins.example.com", stderrors.New("unexpected EOF"))
	if !contains(e.Error(), "https://jenkins.example.com") {
		t.Errorf("missing host in %q", e.Error())
	}
	if e.Code != "malformed_response" {
		t.Errorf("Code = %q, want malformed_response", e.Code)
	}
}

func Test_NewNetwork_NamesHostAndWrapsCause(t *testing.T) {
	cause := stderrors.New("dial tcp: connect: connection refused")
	e := jkerrors.NewNetwork("https://jenkins.example.com", cause)
	if !contains(e.Error(), "https://jenkins.example.com") {
		t.Errorf("missing host in %q", e.Error())
	}
	if !stderrors.Is(e, cause) {
		t.Errorf("network error should wrap cause")
	}
	if e.Code != "network" {
		t.Errorf("Code = %q, want network", e.Code)
	}
}

// ---------------------------------------------------------------------------
// Classify: maps stdlib net/TLS/context errors to JKErrors.
// ---------------------------------------------------------------------------

func Test_Classify_ContextDeadlineExceeded_BecomesTimeout(t *testing.T) {
	got := jkerrors.Classify("https://jenkins.example.com", 5*time.Second, context.DeadlineExceeded)
	var jke *jkerrors.JKError
	if !stderrors.As(got, &jke) {
		t.Fatalf("expected *JKError, got %T", got)
	}
	if jke.Code != "timeout" {
		t.Errorf("Code = %q, want timeout", jke.Code)
	}
}

type fakeTimeoutErr struct{}

func (fakeTimeoutErr) Error() string   { return "i/o timeout" }
func (fakeTimeoutErr) Timeout() bool   { return true }
func (fakeTimeoutErr) Temporary() bool { return true }

func Test_Classify_NetTimeoutInterface_BecomesTimeout(t *testing.T) {
	// Real net.Error implementations return Timeout()==true on i/o timeouts.
	got := jkerrors.Classify("https://h", 1*time.Second, fakeTimeoutErr{})
	var jke *jkerrors.JKError
	if !stderrors.As(got, &jke) {
		t.Fatalf("expected *JKError, got %T", got)
	}
	if jke.Code != "timeout" {
		t.Errorf("Code = %q, want timeout", jke.Code)
	}
}

func Test_Classify_NonNetworkError_PassesThrough(t *testing.T) {
	// Classify should not invent a JKError for errors it doesn't recognize.
	// (Caller will wrap with a context-specific translator instead.)
	plain := stderrors.New("something else")
	got := jkerrors.Classify("https://h", 1*time.Second, plain)
	if got != plain { //nolint:errorlint // we deliberately test identity
		t.Errorf("expected identity pass-through, got %v", got)
	}
}

// Sanity check that *net.OpError carrying a timeout still classifies as
// timeout via the Timeout()-method path.
func Test_Classify_NetOpErrorTimeout_BecomesTimeout(t *testing.T) {
	op := &net.OpError{Op: "dial", Net: "tcp", Err: fakeTimeoutErr{}}
	got := jkerrors.Classify("https://h", 1*time.Second, op)
	var jke *jkerrors.JKError
	if !stderrors.As(got, &jke) {
		t.Fatalf("expected *JKError, got %T", got)
	}
	if jke.Code != "timeout" {
		t.Errorf("Code = %q, want timeout", jke.Code)
	}
}

// ---------------------------------------------------------------------------
// Exit code helper.
// ---------------------------------------------------------------------------

func Test_ExitCode_NilError_IsZero(t *testing.T) {
	if got := jkerrors.ExitCode(nil); got != 0 {
		t.Errorf("ExitCode(nil) = %d, want 0", got)
	}
}

func Test_ExitCode_JKError_IsAtLeast10(t *testing.T) {
	e := &jkerrors.JKError{Code: "x", Message: "x"}
	if got := jkerrors.ExitCode(e); got < 10 {
		t.Errorf("ExitCode(*JKError) = %d, want >= 10", got)
	}
}

func Test_ExitCode_PlainError_IsAtLeast10(t *testing.T) {
	// Anything we don't recognize is still a jk-level failure.
	if got := jkerrors.ExitCode(stderrors.New("oops")); got < 10 {
		t.Errorf("ExitCode(plain) = %d, want >= 10", got)
	}
}

type fakeBuildResultErr struct{ code int }

func (fakeBuildResultErr) Error() string { return "build outcome" }
func (e fakeBuildResultErr) ExitCode() int {
	return e.code
}

func Test_ExitCode_RespectsExitCoderInterface(t *testing.T) {
	// build trigger --watch returns an error implementing ExitCoder so that
	// build-result codes (0..4) reach the process exit code.
	for _, c := range []int{0, 1, 2, 3, 4} {
		got := jkerrors.ExitCode(fakeBuildResultErr{code: c})
		if got != c {
			t.Errorf("ExitCode for code=%d returned %d", c, got)
		}
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func contains(haystack, needle string) bool {
	return len(needle) == 0 || indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
