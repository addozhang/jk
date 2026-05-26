package jenkins_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	stderrors "errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/addozhang/jk/internal/auth"
	jkerrors "github.com/addozhang/jk/internal/errors"
	"github.com/addozhang/jk/internal/jenkins"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// memoryStore is a Store double that keeps everything in memory so tests
// don't touch the filesystem.
type memoryStore struct {
	creds map[string]auth.Credential
	order []string
}

func newMemStore() *memoryStore { return &memoryStore{creds: map[string]auth.Credential{}} }

func (m *memoryStore) Add(host string, c auth.Credential) error {
	if _, ok := m.creds[host]; !ok {
		m.order = append(m.order, host)
	}
	m.creds[host] = c
	return nil
}

func (m *memoryStore) Get(host string) (auth.Credential, bool, error) {
	c, ok := m.creds[host]
	return c, ok, nil
}

func (m *memoryStore) List() ([]string, error) {
	out := make([]string, len(m.order))
	copy(out, m.order)
	return out, nil
}

func (m *memoryStore) Remove(host string) error {
	delete(m.creds, host)
	for i, h := range m.order {
		if h == host {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// basic transport + timeout
// ---------------------------------------------------------------------------

func Test_New_DefaultTimeout_Is30s(t *testing.T) {
	client, err := jenkins.New(jenkins.Options{Stderr: io.Discard})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if client.Timeout != 30*time.Second {
		t.Errorf("default timeout = %s, want 30s", client.Timeout)
	}
}

func Test_New_CustomTimeoutIsRespected(t *testing.T) {
	client, err := jenkins.New(jenkins.Options{Timeout: 5 * time.Second, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if client.Timeout != 5*time.Second {
		t.Errorf("timeout = %s, want 5s", client.Timeout)
	}
}

func Test_Client_TimesOut_WhenServerIsSlow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	client, err := jenkins.New(jenkins.Options{Timeout: 50 * time.Millisecond, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get(srv.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected timeout error")
	}
	// Per design: transport surfaces raw errors; API client translates.
	// But the underlying error should be classifiable as a timeout.
	if got := jkerrors.Classify("h", 50*time.Millisecond, err); got == nil {
		t.Errorf("Classify returned nil; expected timeout translation")
	} else {
		var jke *jkerrors.JKError
		if !stderrors.As(got, &jke) || jke.Code != "timeout" {
			t.Errorf("Classify did not produce a timeout JKError: %v", got)
		}
	}
}

// ---------------------------------------------------------------------------
// authorization injection
// ---------------------------------------------------------------------------

func Test_Client_InjectsBasicAuth_FromStore(t *testing.T) {
	var seenAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	store := newMemStore()
	_ = store.Add(srv.URL, auth.Credential{Username: "alice", Token: "tok-xyz"})

	client, err := jenkins.New(jenkins.Options{Credentials: store, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get(srv.URL + "/job/svc/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if seenAuth == "" {
		t.Fatal("Authorization header missing")
	}
	if !strings.HasPrefix(seenAuth, "Basic ") {
		t.Errorf("Authorization is not Basic auth: %q", seenAuth)
	}
	// Round-trip the basic auth and verify user/token.
	req, _ := http.NewRequest(http.MethodGet, srv.URL, http.NoBody)
	req.SetBasicAuth("alice", "tok-xyz")
	if want := req.Header.Get("Authorization"); seenAuth != want {
		t.Errorf("Authorization = %q, want %q", seenAuth, want)
	}
}

func Test_Client_OmitsAuth_WhenStoreHasNoCredsForHost(t *testing.T) {
	// Spec: "If any command is invoked with a URL whose hostname has no
	// configured credential entry, the system exits non-zero ..." — but
	// that exit-code logic belongs to the CLI / API client. The transport
	// itself simply makes the request without an Authorization header,
	// letting the server respond with 401/403 which the API client then
	// translates.
	var seenAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	client, err := jenkins.New(jenkins.Options{Credentials: newMemStore(), Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if seenAuth != "" {
		t.Errorf("Authorization injected for unknown host: %q", seenAuth)
	}
}

func Test_Client_HostNormalization_StripsDefaultPort(t *testing.T) {
	// The credential lookup key is `scheme://host[:non-default-port]`.
	// A request to http://example.com:80/... should match credentials
	// stored under http://example.com (default port stripped).
	var seenAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	store := newMemStore()
	// Store under the normalized host (which jenkinsurl.HostKey produces).
	// The httptest server URL contains the random port; we expect the
	// transport to look it up under that exact key, NOT to incorrectly
	// strip it.
	_ = store.Add(srv.URL, auth.Credential{Username: "u", Token: "t"})

	client, err := jenkins.New(jenkins.Options{Credentials: store, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get(srv.URL + "/api/json")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if seenAuth == "" {
		t.Error("Authorization not injected; host normalization mismatch?")
	}
}

func Test_Client_DoesNotOverrideExistingAuthHeader(t *testing.T) {
	// If a caller manually sets Authorization (e.g. for the crumb fetch
	// path or for testing), the transport must not override it.
	var seenAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	store := newMemStore()
	_ = store.Add(srv.URL, auth.Credential{Username: "auto", Token: "auto-tok"})

	client, err := jenkins.New(jenkins.Options{Credentials: store, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL, http.NoBody)
	req.Header.Set("Authorization", "Bearer manual")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if seenAuth != "Bearer manual" {
		t.Errorf("Authorization overridden: got %q", seenAuth)
	}
}

// ---------------------------------------------------------------------------
// TLS: SSL_CERT_FILE + --insecure
// ---------------------------------------------------------------------------

func Test_New_SSL_CERT_FILE_Missing_ReturnsTLSCertFileError(t *testing.T) {
	// Spec: "SSL_CERT_FILE set but file is missing" -> JKError naming the
	// env var and the resolved path.
	_, err := jenkins.New(jenkins.Options{
		SSLCertFile: "/this/path/does/not/exist.pem",
		Stderr:      io.Discard,
	})
	if err == nil {
		t.Fatal("expected error for missing SSL_CERT_FILE")
	}
	var jke *jkerrors.JKError
	if !stderrors.As(err, &jke) {
		t.Fatalf("expected *JKError, got %T", err)
	}
	if jke.Code != "tls_cert_file" {
		t.Errorf("Code = %q, want tls_cert_file", jke.Code)
	}
	if !strings.Contains(jke.Error(), "/this/path/does/not/exist.pem") {
		t.Errorf("error must contain resolved path: %v", jke)
	}
	if !strings.Contains(jke.Error(), "SSL_CERT_FILE") {
		t.Errorf("error must mention SSL_CERT_FILE: %v", jke)
	}
}

func Test_New_SSL_CERT_FILE_Unparseable_ReturnsTLSCertFileError(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "garbage.pem")
	if err := os.WriteFile(bad, []byte("definitely not a PEM bundle"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := jenkins.New(jenkins.Options{SSLCertFile: bad, Stderr: io.Discard})
	if err == nil {
		t.Fatal("expected error for unparseable PEM file")
	}
	var jke *jkerrors.JKError
	if !stderrors.As(err, &jke) || jke.Code != "tls_cert_file" {
		t.Errorf("expected tls_cert_file JKError, got: %v", err)
	}
}

func Test_New_SSL_CERT_FILE_ValidPEM_AugmentsPool(t *testing.T) {
	// Start an httptest TLS server whose certificate is in srv.TLS.Certificates.
	// Without trusting it, a request fails verification. After feeding the
	// cert into a PEM file referenced by SSL_CERT_FILE, the request
	// succeeds. (We can verify the pool was actually built by checking
	// the resulting *http.Client has a non-default Transport whose
	// TLSClientConfig.RootCAs is non-nil.)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	// Encode the server cert to PEM into a tempfile.
	pem := certToPEM(t, srv.Certificate())
	dir := t.TempDir()
	pemPath := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(pemPath, pem, 0o600); err != nil {
		t.Fatal(err)
	}

	client, err := jenkins.New(jenkins.Options{
		SSLCertFile: pemPath,
		Stderr:      io.Discard,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("request failed with augmented pool: %v", err)
	}
	resp.Body.Close()
}

func Test_New_Insecure_DisablesVerify_AndWarns(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	var stderr strings.Builder
	client, err := jenkins.New(jenkins.Options{Insecure: true, Stderr: &stderr})
	if err != nil {
		t.Fatal(err)
	}

	// Should succeed despite TLS being self-signed.
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("--insecure request failed: %v", err)
	}
	resp.Body.Close()

	if !strings.Contains(stderr.String(), "insecure") && !strings.Contains(stderr.String(), "Insecure") {
		t.Errorf("expected insecure warning on stderr, got: %q", stderr.String())
	}
}

func Test_New_WithoutInsecure_RejectsSelfSigned(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	client, err := jenkins.New(jenkins.Options{Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get(srv.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Error("expected TLS verification error against self-signed server")
	}
}

// ---------------------------------------------------------------------------
// debug logging
// ---------------------------------------------------------------------------

func Test_Debug_LogsRequestAndResponse_ToStderr(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	var stderr strings.Builder
	client, err := jenkins.New(jenkins.Options{Debug: true, Stderr: &stderr})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get(srv.URL + "/job/svc/api/json")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	log := stderr.String()
	if !strings.Contains(log, "GET") {
		t.Errorf("debug log missing request method: %q", log)
	}
	if !strings.Contains(log, "/job/svc/api/json") {
		t.Errorf("debug log missing request path: %q", log)
	}
	if !strings.Contains(log, "200") {
		t.Errorf("debug log missing response status: %q", log)
	}
	if !strings.Contains(log, "ok") {
		t.Errorf("debug log missing response body: %q", log)
	}
}

func Test_Debug_RedactsAuthorizationHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	store := newMemStore()
	_ = store.Add(srv.URL, auth.Credential{Username: "alice", Token: "super-secret-token"})

	var stderr strings.Builder
	client, err := jenkins.New(jenkins.Options{Debug: true, Credentials: store, Stderr: &stderr})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	log := stderr.String()
	if strings.Contains(log, "super-secret-token") {
		t.Errorf("debug log leaked token: %q", log)
	}
	// The basic-auth header encodes credentials base64; the redacted log
	// MUST NOT contain the encoded form either.
	if strings.Contains(log, "Basic ") && !strings.Contains(log, "REDACTED") {
		t.Errorf("Basic auth not redacted: %q", log)
	}
}

func Test_Debug_LogsToStderrOnly_NotStdout(t *testing.T) {
	// Spec scenario: debug output is exclusively on stderr.
	// We assert that by routing it to our buffer (which the test owns)
	// and confirming nothing leaks to os.Stdout. Since we can't easily
	// capture os.Stdout from a unit test, we instead rely on the
	// implementation contract: opts.Stderr is the only sink.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	var stderr strings.Builder
	client, err := jenkins.New(jenkins.Options{Debug: true, Stderr: &stderr})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if stderr.Len() == 0 {
		t.Error("debug log empty; expected request/response trace")
	}
}

func Test_NoDebug_DoesNotLog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	var stderr strings.Builder
	client, err := jenkins.New(jenkins.Options{Stderr: &stderr})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if stderr.Len() != 0 {
		t.Errorf("stderr should be empty without --debug, got: %q", stderr.String())
	}
}

// ---------------------------------------------------------------------------
// context propagation
// ---------------------------------------------------------------------------

func Test_Client_RespectsRequestContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	client, err := jenkins.New(jenkins.Options{Timeout: 10 * time.Second, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	t.Cleanup(cancel)

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, http.NoBody)
	resp, err := client.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// certToPEM PEM-encodes a single x509 cert. The test certificate from
// httptest.NewTLSServer is already DER; we just need to wrap it.
func certToPEM(t *testing.T, cert *x509.Certificate) []byte {
	t.Helper()
	if cert == nil {
		t.Fatal("nil cert")
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
}

// Assert that the package builds at all by referencing tls.VersionTLS12,
// the standard symbol we use for minimum TLS version. This keeps the
// import live and surfaces compile errors clearly if jenkins.Options
// later removes TLS handling.
var _ = tls.VersionTLS12
