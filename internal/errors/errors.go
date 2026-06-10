// Package errors provides translated, user-facing error types for jk.
//
// The CLI never prints raw Jenkins HTML, stack traces, or Go-internal error
// strings to the user (unless --debug is set). Instead, every layer that
// touches the network or filesystem returns a [*JKError] constructed from
// one of the translators in this package. The translators encode the exact
// phrasing required by openspec/changes/init-jk-jenkins-cli/specs/errors.
//
// Exit codes:
//
//   - 0..9 are reserved for command-result semantics (e.g. build-result
//     codes from `jk build trigger --watch`). Use the [ExitCoder] interface
//     to surface such codes.
//   - >= 10 indicates a jk-level failure (auth, network, parsing, config).
//     [JKError] satisfies this category. [ExitCode] returns 10 for any
//     non-nil error that does not implement [ExitCoder].
//
// The exact value within the >= 10 range MAY change across versions; tests
// MUST only assert `>= 10`, never a specific value.
package errors

import (
	"context"
	stderrors "errors"
	"fmt"
	"net"
	"time"
)

// jkLevelExitCode is the single value returned by [JKError.ExitCode]. It is
// fixed at 10 today but the spec allows it to vary, so callers MUST go
// through [ExitCode] instead of hardcoding 10.
const jkLevelExitCode = 10

// JKError is the canonical translated error returned by every jk layer that
// detects a user-actionable failure.
//
// Fields:
//
//   - Code: machine-readable identifier (e.g. "auth_rejected"). Used by
//     tests and reserved for a future --json-errors mode.
//   - Message: one-sentence human description of what went wrong.
//   - Suggestion: one-sentence next step the user can take (a jk command,
//     a flag, or a verification step). REQUIRED by the errors spec.
//   - Cause: optional underlying error for `errors.Is` / `errors.As`
//     unwrapping and for --debug logging.
type JKError struct {
	Code       string
	Message    string
	Suggestion string
	Cause      error
}

// Error returns "<Message> <Suggestion>" — both fragments are designed to
// end with a period, so the result reads as two consecutive sentences.
func (e *JKError) Error() string {
	if e.Suggestion == "" {
		return e.Message
	}
	return e.Message + " " + e.Suggestion
}

// Unwrap returns the underlying cause so errors.Is/As walk through it.
func (e *JKError) Unwrap() error { return e.Cause }

// ExitCode implements [ExitCoder]; always returns the jk-level value.
func (e *JKError) ExitCode() int { return jkLevelExitCode }

// ExitCoder is implemented by errors that carry their own exit code (most
// notably the build-result error from `jk build trigger --watch`, which
// maps SUCCESS/FAILURE/UNSTABLE/ABORTED/PENDING_INPUT to codes 0..4).
//
// Top-level main() walks the error chain with errors.As to find the
// outermost ExitCoder and uses its code; if nothing implements it, the
// default of 10 (jk-level failure) is used.
type ExitCoder interface {
	error
	ExitCode() int
}

// ExitCode maps an error to a process exit code:
//
//   - nil           -> 0
//   - ExitCoder     -> its ExitCode()
//   - anything else -> jkLevelExitCode (10)
//
// Callers SHOULD use this rather than type-switching on *JKError directly,
// because the build-result code path uses a different concrete type.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ec ExitCoder
	if stderrors.As(err, &ec) {
		return ec.ExitCode()
	}
	return jkLevelExitCode
}

// ---------------------------------------------------------------------------
// Translators
//
// One constructor per error-spec scenario. Phrasing is verbatim from
// specs/errors/spec.md so that the spec scenarios double as acceptance
// tests when wired into integration tests.
// ---------------------------------------------------------------------------

// NewAuthRejected is returned for HTTP 401/403 on authenticated requests.
func NewAuthRejected(host string) *JKError {
	return &JKError{
		Code:       "auth_rejected",
		Message:    fmt.Sprintf("API token rejected by %s.", host),
		Suggestion: fmt.Sprintf("Run: jk auth add %s to refresh.", host),
	}
}

// NewNotFound is returned for HTTP 404 against a pipeline URL.
func NewNotFound(pipelineURL string) *JKError {
	return &JKError{
		Code:       "not_found",
		Message:    fmt.Sprintf("Pipeline not found: %s.", pipelineURL),
		Suggestion: "Check the URL or list with: jk pipeline list <parent-folder-url>.",
	}
}

// NewBuildNotFound is returned for HTTP 404 against a build URL.
func NewBuildNotFound(buildURL string) *JKError {
	return &JKError{
		Code:       "not_found",
		Message:    fmt.Sprintf("Build not found: %s.", buildURL),
		Suggestion: "Check the build number or list recent builds with: jk build status <pipeline-url>.",
	}
}

// NewTimeout is returned when a request exceeds the configured timeout.
func NewTimeout(host string, timeout time.Duration) *JKError {
	return &JKError{
		Code:       "timeout",
		Message:    fmt.Sprintf("Timed out after %s contacting %s.", timeout, host),
		Suggestion: "Increase with --timeout <duration> or check VPN connectivity.",
	}
}

// NewCrumbFailed is returned when the CSRF crumb refresh-and-retry logic
// still cannot obtain a usable crumb.
func NewCrumbFailed(host string) *JKError {
	return &JKError{
		Code:       "crumb_failed",
		Message:    fmt.Sprintf("Unable to obtain a CSRF crumb that %s accepts.", host),
		Suggestion: "This Jenkins version may have incompatible CSRF behavior; please file an issue.",
	}
}

// NewTLSCertFile is returned when SSL_CERT_FILE is set but the path is
// missing or contains unparseable PEM data.
func NewTLSCertFile(path string, cause error) *JKError {
	return &JKError{
		Code:       "tls_cert_file",
		Message:    fmt.Sprintf("SSL_CERT_FILE points to %s which could not be loaded.", path),
		Suggestion: "Verify the path exists, is readable, and contains valid PEM-encoded certificates.",
		Cause:      cause,
	}
}

// NewMalformedResponse is returned when Jenkins returns a body that cannot
// be parsed against the expected shape (truncated JSON, unexpected HTML,
// missing required fields).
func NewMalformedResponse(host string, cause error) *JKError {
	return &JKError{
		Code:       "malformed_response",
		Message:    fmt.Sprintf("Received an unexpected response from %s.", host),
		Suggestion: "Re-run with --debug to inspect the raw exchange; if the response looks valid, please file an issue.",
		Cause:      cause,
	}
}

// NewNetwork is returned for connection-level failures (refused, DNS,
// unreachable) that are not timeouts. Timeouts go through [Classify].
func NewNetwork(host string, cause error) *JKError {
	return &JKError{
		Code:       "network",
		Message:    fmt.Sprintf("Network error contacting %s: %v.", host, cause),
		Suggestion: "Verify the host is reachable from this machine and that any required VPN is connected.",
		Cause:      cause,
	}
}

// Classify converts a low-level error returned by net/http or context into
// a translated [*JKError] when possible. Only errors whose shape we
// recognize are translated; everything else is returned unchanged so the
// caller can apply a context-aware translator instead.
//
// Recognized inputs:
//
//   - context.DeadlineExceeded -> NewTimeout
//   - net.Error with Timeout()==true (including wrapped) -> NewTimeout
//
// Callers SHOULD use this for the generic timeout case and fall back to
// NewNetwork / NewMalformedResponse for residual errors.
func Classify(host string, timeout time.Duration, err error) error {
	if err == nil {
		return nil
	}
	if stderrors.Is(err, context.DeadlineExceeded) {
		return NewTimeout(host, timeout)
	}
	var nerr net.Error
	if stderrors.As(err, &nerr) && nerr.Timeout() {
		return NewTimeout(host, timeout)
	}
	return err
}

// ---------------------------------------------------------------------------
// Build-result exit codes (jk build trigger --watch)
//
// Per specs/build §"Watch a triggered build until completion" the
// process exit code MUST encode the terminal build state:
//
//	0 SUCCESS, 1 FAILURE, 2 UNSTABLE, 3 ABORTED, 4 PENDING_INPUT.
//
// These overlap the "command-result" exit-code range (0..9) reserved
// in this package's doc comment. main() walks the error chain with
// errors.As to find an [ExitCoder]; BuildResultExitError satisfies it
// so a watch that ends UNSTABLE returns 2 rather than the generic
// jk-level 10.
// ---------------------------------------------------------------------------

// BuildResultExitError carries a terminal build result up to main() so
// the process exits with the appropriate watch-mode code. The wrapped
// message is intentionally minimal — `jk build trigger --watch` prints
// the human-facing progress lines as it polls, so by the time this
// error reaches stderr the user already knows what happened; this
// error string is for log files and `--debug` consumers.
type BuildResultExitError struct {
	// Code is the process exit code per the spec mapping above. MUST be
	// in 0..4.
	Code int
	// Reason is a short token describing why this code was chosen,
	// embedded into Error() for debug-log readability.
	Reason string
}

// Error returns a concise, machine-greppable message. Callers typically
// do NOT print this — main()'s error printer is suppressed for
// build-result exits since the watch loop already streamed status.
func (e *BuildResultExitError) Error() string {
	return fmt.Sprintf("build finished: %s (exit %d)", e.Reason, e.Code)
}

// ExitCode satisfies [ExitCoder].
func (e *BuildResultExitError) ExitCode() int { return e.Code }
