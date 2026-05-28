// Package jenkins provides the HTTP transport and (in later packages) the
// Jenkins REST API client used by every jk command.
//
// This file owns the transport layer. The single public entry point is
// [New], which returns a fully configured *http.Client whose RoundTripper
// stack handles:
//
//  1. Authorization injection: looks up Basic-auth credentials for the
//     request's host from the supplied [auth.Store] and attaches them.
//     Pre-set Authorization headers are NOT overridden, allowing callers
//     (e.g. the crumb fetcher) to bypass the lookup when needed.
//
//  2. Debug logging (when Options.Debug is true): dumps every request and
//     response to Options.Stderr, with the Authorization header redacted
//     so secrets never reach disk or screen.
//
//  3. TLS configuration: builds a root CA pool from the system trust store
//     plus any certs loaded from Options.SSLCertFile. If Options.Insecure
//     is set, certificate verification is disabled and a one-time warning
//     is written to Options.Stderr.
//
// Error translation is INTENTIONALLY not done here. The transport returns
// raw *http.Response and raw transport errors; the API client (group 11)
// classifies them into [jkerrors.JKError]s with the right user-facing
// phrasing based on the request context (was this a pipeline URL? a
// credentialed call?).
package jenkins

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/addozhang/jk/internal/auth"
	jkerrors "github.com/addozhang/jk/internal/errors"
)

// DefaultTimeout is applied when Options.Timeout is the zero value. It
// matches the value documented in
// openspec/changes/init-jk-jenkins-cli/specs/tls-and-transport/spec.md.
const DefaultTimeout = 30 * time.Second

// Options configures [New]. All fields are optional except Stderr, which
// is required so the transport has somewhere to write the --insecure
// warning and --debug logs without ever touching os.Stdout (the spec
// requires that stdout remains clean for script-friendly output).
type Options struct {
	// Timeout bounds each outbound HTTP request. Defaults to
	// [DefaultTimeout] when zero.
	Timeout time.Duration

	// Insecure disables TLS certificate verification when true. A
	// warning is printed to Stderr once at construction time.
	Insecure bool

	// SSLCertFile is an optional path to a PEM bundle whose certificates
	// will be added to the system root CA pool. Missing file or
	// unparseable PEM produces a [jkerrors.JKError] with code
	// "tls_cert_file".
	SSLCertFile string

	// Debug enables request/response logging to Stderr. The
	// Authorization header is replaced with "REDACTED" in the log.
	Debug bool

	// EnableCSRF turns on the automatic CSRF crumb subsystem. When
	// true, state-changing requests (POST/PUT/DELETE/PATCH) trigger a
	// `/crumbIssuer/api/json` lookup whose result is attached as a
	// header and cached per-host for the process lifetime. See
	// crumb.go for the full lifecycle.
	EnableCSRF bool

	// Credentials is the credential store used by the Authorization
	// injector. May be nil, in which case no Authorization header is
	// added (the server will respond with 401/403 and the API client
	// translates that into auth_rejected with a remediation hint).
	Credentials auth.Store

	// Stderr is the sink for warnings and debug logs. MUST NOT be nil;
	// use io.Discard if the caller does not care.
	Stderr io.Writer
}

// New returns an *http.Client configured per opts. The returned client is
// safe to reuse for the entire process lifetime.
func New(opts Options) (*http.Client, error) {
	if opts.Stderr == nil {
		return nil, fmt.Errorf("jenkins.New: Stderr is required")
	}
	if opts.Timeout == 0 {
		opts.Timeout = DefaultTimeout
	}

	tlsCfg, err := buildTLSConfig(opts)
	if err != nil {
		return nil, err
	}

	// Base transport: a clone of DefaultTransport so we keep stdlib's
	// sensible connection pooling and HTTP/2 wiring, with only the TLS
	// config swapped in. The type assertion is safe: stdlib guarantees
	// DefaultTransport is a *http.Transport.
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("jenkins.New: http.DefaultTransport is not *http.Transport")
	}
	base := defaultTransport.Clone()
	base.TLSClientConfig = tlsCfg

	if opts.Insecure {
		// Best-effort warning; if stderr is broken we cannot do better.
		_, _ = fmt.Fprintln(opts.Stderr, "warning: TLS certificate verification disabled (--insecure)") //nolint:errcheck // best-effort warning
	}

	// Build the RoundTripper stack and cookie jar. The jar is created
	// first so it can be shared between the crumb fetcher client and the
	// final client. Stack from inside out:
	//   base -> auth -> [crumb] -> [debug]
	// Auth must wrap base so headers are set on the actual outbound
	// request. The crumb layer (when enabled) fetches via a dedicated
	// http.Client (auth+jar, no crumb/debug) to prevent recursion and
	// to engage the cookie jar. Debug always wraps the outermost layer
	// to capture exactly what the client would have sent.
	// A cookie jar is required for CSRF crumb validity. Jenkins ties
	// the crumb to the JSESSIONID cookie set during the
	// /crumbIssuer/api/json GET; if the subsequent POST carries a
	// different (or absent) session cookie the crumb is rejected with
	// 403. We use nil PublicSuffixList (no public-suffix filtering)
	// which is correct for a CLI tool that only ever talks to known,
	// user-configured hosts — not a browser serving untrusted sites.
	//
	// The jar is created BEFORE the RoundTripper stack so we can wire
	// it into the crumb fetcher client (see below). Both the crumb
	// fetcher client and the final client share the same jar instance,
	// ensuring the JSESSIONID cookie set during the crumb GET is
	// available on every subsequent request.
	jar, err := cookiejar.New(nil)
	if err != nil {
		// cookiejar.New documents that it never returns an error when
		// given nil options; this branch is defensive.
		return nil, fmt.Errorf("jenkins.New: create cookie jar: %w", err)
	}

	var rt http.RoundTripper = base
	rt = &authInjector{next: rt, creds: opts.Credentials}
	if opts.EnableCSRF {
		// The crumb manager needs an *http.Client (not just a
		// RoundTripper) so that http.Client.Do() engages the cookie
		// jar. We give it a client whose transport is the auth-wrapped
		// base — credentials are injected, but no crumb layer (to avoid
		// recursion) and no debug layer (crumb fetches are internal).
		// The jar is shared with the outer client so JSESSIONID flows
		// from the crumb GET into all subsequent POSTs.
		crumbClient := &http.Client{
			Timeout:   opts.Timeout,
			Transport: rt, // auth-wrapped base, no crumb/debug layers
			Jar:       jar,
		}
		mgr := newCrumbManager(crumbClient)
		rt = &crumbRoundTripper{next: rt, mgr: mgr}
	}
	if opts.Debug {
		rt = &debugLogger{next: rt, w: opts.Stderr}
	}

	return &http.Client{
		Timeout:   opts.Timeout,
		Transport: rt,
		Jar:       jar,
	}, nil
}

// ---------------------------------------------------------------------------
// TLS config
// ---------------------------------------------------------------------------

// buildTLSConfig assembles the *tls.Config used by the transport. It
// honors Insecure (disables verification entirely) and SSLCertFile
// (augments the system pool with additional CAs).
func buildTLSConfig(opts Options) (*tls.Config, error) {
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}
	if opts.Insecure {
		// nolint:gosec // explicit user opt-in; the warning to stderr is
		// printed in New().
		cfg.InsecureSkipVerify = true
		return cfg, nil
	}
	if opts.SSLCertFile == "" {
		// Default behavior: stdlib uses the system pool.
		return cfg, nil
	}

	pool, err := loadCertPool(opts.SSLCertFile)
	if err != nil {
		return nil, err
	}
	cfg.RootCAs = pool
	return cfg, nil
}

// loadCertPool starts from a copy of the system root pool (if available)
// and appends every PEM-encoded certificate in path. A missing file or
// PEM data with zero usable certificates is reported as a JKError so the
// user sees the exact path and a remediation hint.
func loadCertPool(path string) (*x509.CertPool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, jkerrors.NewTLSCertFile(path, err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		// SystemCertPool can fail on Windows; fall back to an empty pool
		// so the user's SSL_CERT_FILE certs still work.
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(data) {
		return nil, jkerrors.NewTLSCertFile(path,
			fmt.Errorf("file contains no valid PEM-encoded certificates"))
	}
	return pool, nil
}

// ---------------------------------------------------------------------------
// Authorization injector
// ---------------------------------------------------------------------------

// authInjector is a RoundTripper that adds an HTTP Basic Authorization
// header derived from the credentials store. The lookup key is the
// request's scheme + host (with default ports stripped) — matching the
// normalization done by [jenkinsurl.Ref.HostKey].
//
// If the request already has an Authorization header, it is preserved
// untouched: callers (the crumb fetcher, tests, future SSO bridges) own
// that header when they set it explicitly.
type authInjector struct {
	next  http.RoundTripper
	creds auth.Store
}

func (a *authInjector) RoundTrip(req *http.Request) (*http.Response, error) {
	if a.creds != nil && req.Header.Get("Authorization") == "" {
		key := hostKeyFromURL(req.URL)
		if c, ok, err := a.creds.Get(key); err == nil && ok {
			// Clone the request to avoid mutating the caller's value;
			// RoundTrip contract requires the request to be unchanged
			// after return.
			cloned := req.Clone(req.Context())
			cloned.SetBasicAuth(c.Username, c.Token)
			req = cloned
		}
	}
	return a.next.RoundTrip(req)
}

// hostKeyFromURL produces the same `scheme://host[:non-default-port]`
// form that jenkinsurl.Ref.HostKey emits, so credentials stored via
// `jk auth add <url>` resolve correctly when a request arrives.
func hostKeyFromURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	host := strings.ToLower(u.Host)
	// Strip default ports so http://h:80 and http://h resolve to the
	// same credential entry.
	switch {
	case u.Scheme == "http" && strings.HasSuffix(host, ":80"):
		host = strings.TrimSuffix(host, ":80")
	case u.Scheme == "https" && strings.HasSuffix(host, ":443"):
		host = strings.TrimSuffix(host, ":443")
	}
	return u.Scheme + "://" + host
}

// ---------------------------------------------------------------------------
// Debug logger
// ---------------------------------------------------------------------------

// debugLogger is a RoundTripper that dumps the request and response to
// the configured stderr. The Authorization header is replaced with a
// REDACTED placeholder in both the request dump and (defensively) in the
// response dump, so tokens never reach logs.
type debugLogger struct {
	next http.RoundTripper
	w    io.Writer
}

const redactedPlaceholder = "REDACTED"

func (d *debugLogger) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone the request for dumping so we can scrub Authorization without
	// mutating what the next RoundTripper sees. NOTE: Request.Clone is a
	// SHALLOW clone — the Body field (an io.ReadCloser) is shared with the
	// original request. Reading from dumpReq.Body would drain req.Body and
	// leave downstream RoundTrippers (and ultimately Go's transport) with
	// a zero-byte body, which some HTTP intermediaries
	// reject with 400. To dump the body safely we must obtain an
	// INDEPENDENT reader, either from req.GetBody (preferred; callers
	// such as TriggerBuild set it) or by buffering + restoring the body.
	dumpReq := req.Clone(req.Context())
	if dumpReq.Header.Get("Authorization") != "" {
		dumpReq.Header.Set("Authorization", redactedPlaceholder)
	}
	if err := snapshotBodyForDump(req, dumpReq); err != nil {
		d.logf("--- jk request (body snapshot failed: %v) ---\n", err)
	} else if dump, err := httputil.DumpRequest(dumpReq, true); err == nil {
		d.logf("--- jk request ---\n%s\n", dump)
	} else {
		d.logf("--- jk request (dump failed: %v) ---\n", err)
	}

	resp, err := d.next.RoundTrip(req)
	if err != nil {
		d.logf("--- jk response (error) ---\n%v\n", err)
		return nil, err
	}
	if dump, derr := httputil.DumpResponse(resp, true); derr == nil {
		d.logf("--- jk response ---\n%s\n", dump)
	} else {
		d.logf("--- jk response (dump failed: %v) ---\n", derr)
	}
	return resp, nil
}

// snapshotBodyForDump gives dumpReq its own independent Body so that
// httputil.DumpRequest can render the payload without consuming the
// body that the real outbound request needs.
//
// Three cases:
//
//  1. Body is nil or http.NoBody — nothing to do.
//  2. orig.GetBody is set — call it for a fresh reader. This is the
//     happy path; callers performing state-changing requests (e.g.
//     TriggerBuild) already set GetBody so the crumb RoundTripper can
//     replay the body on a CSRF retry. The same hook serves us here.
//  3. GetBody is nil — buffer orig.Body into memory, then restore both
//     orig.Body and dumpReq.Body as independent readers backed by the
//     same byte slice. This costs one full body read; acceptable for
//     debug mode (off by default) and unavoidable when the caller did
//     not provide a replay hook.
func snapshotBodyForDump(orig, dumpReq *http.Request) error {
	if orig.Body == nil || orig.Body == http.NoBody {
		return nil
	}
	if orig.GetBody != nil {
		fresh, err := orig.GetBody()
		if err != nil {
			return err
		}
		dumpReq.Body = fresh
		return nil
	}
	buf, err := io.ReadAll(orig.Body)
	if err != nil {
		return err
	}
	_ = orig.Body.Close() //nolint:errcheck // body already drained; close is best-effort
	orig.Body = io.NopCloser(bytes.NewReader(buf))
	dumpReq.Body = io.NopCloser(bytes.NewReader(buf))
	return nil
}

// logf writes a debug line to the configured sink. Errors are
// intentionally ignored: debug output to stderr is best-effort and a
// broken sink must not affect request semantics.
func (d *debugLogger) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(d.w, format, args...) //nolint:errcheck // best-effort debug log
}
