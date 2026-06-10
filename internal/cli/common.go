package cli

// This file owns the shared plumbing every jk subcommand needs to talk to
// Jenkins: building the HTTP transport, the auth store, and the API
// client; classifying low-level errors into JKErrors; rendering schema
// values to the user's chosen output format.
//
// Each command function calls newCommandContext(flags) once at the top
// to obtain a ready-to-use *Client plus a render helper. Keeping the
// wiring here means each command file stays focused on its own
// URL parse -> client call -> mapper -> render pipeline (the four-step
// pattern documented in design.md §D7).

import (
	"context"
	stderrors "errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/addozhang/jk/internal/auth"
	jkerrors "github.com/addozhang/jk/internal/errors"
	"github.com/addozhang/jk/internal/jenkins"
	"github.com/addozhang/jk/internal/jenkinsurl"
	"github.com/addozhang/jk/internal/output"
)

// commandContext bundles the per-invocation runtime: the Cobra command (for
// its OutOrStdout/ErrOrStderr writers), the parsed global flags, and the
// Jenkins API client. Construction is centralized in [newCommandContext] so
// every subcommand obtains a transport configured with the same defaults
// (timeout, TLS, CSRF, auth) and writes to the same streams Cobra has wired
// in for tests.
type commandContext struct {
	cmd    *cobra.Command
	flags  *GlobalFlags
	client *jenkins.Client
	// stderr is the destination for warnings (e.g. --insecure) and
	// debug logs. Captured from cmd.ErrOrStderr() at construction so
	// tests can substitute a buffer.
	stderr io.Writer
}

// newCommandContext wires up auth + transport + API client for one
// invocation. The hostKey is the credentials-lookup key (i.e. Ref.Host);
// it is used only as a courtesy for transport's auth injector — the
// injector itself selects credentials per outbound URL, so passing the
// "wrong" hostKey here is harmless. Commands pass the parsed Ref.Host so
// the future per-host caching layer (deferred) has the right key.
func newCommandContext(cmd *cobra.Command, flags *GlobalFlags) (*commandContext, error) {
	stderr := cmd.ErrOrStderr()

	// Auth store: missing file is fine (NewFileStore is lazy); a missing
	// XDG path becomes a JKError so the user gets actionable guidance.
	credPath, err := defaultCredentialsPath()
	if err != nil {
		return nil, fmt.Errorf("locate credentials file: %w", err)
	}
	store, err := auth.NewFileStore(credPath)
	if err != nil {
		return nil, fmt.Errorf("open credentials store: %w", err)
	}

	httpClient, err := jenkins.New(jenkins.Options{
		Timeout:     flags.Timeout,
		Insecure:    flags.Insecure,
		SSLCertFile: os.Getenv("SSL_CERT_FILE"),
		Debug:       flags.Debug,
		EnableCSRF:  true,
		Credentials: store,
		Stderr:      stderr,
	})
	if err != nil {
		return nil, err
	}
	return &commandContext{
		cmd:    cmd,
		flags:  flags,
		client: jenkins.NewClient(httpClient),
		stderr: stderr,
	}, nil
}

// defaultCredentialsPath returns the on-disk path of the TOML credentials
// file. Per SPEC.md the path is `~/.config/jk/credentials` regardless of
// platform — we do NOT use os.UserConfigDir() because its macOS default
// (`~/Library/Application Support`) is surprising for a CLI tool and the
// XDG-style path is the documented contract.
func defaultCredentialsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(home, ".config", "jk", "credentials"), nil
}

// render writes v to the command's stdout using the configured output
// format. The trailing newline (already present in YAML output; appended
// for JSON) keeps shell pipelines well-formed.
func (cc *commandContext) render(v any) error {
	body, err := output.Render(v, cc.flags.Output)
	if err != nil {
		return err
	}
	// YAML output is newline-terminated by the renderer; JSON is not.
	// Always emit a trailing newline so the cursor lands on a fresh
	// line in interactive use.
	if len(body) == 0 || body[len(body)-1] != '\n' {
		body = append(body, '\n')
	}
	_, err = cc.cmd.OutOrStdout().Write(body)
	return err
}

// translateClientError converts a low-level error from internal/jenkins
// (HTTPStatusError or transport-level error) into the user-facing
// [*jkerrors.JKError] vocabulary. The mapping is:
//
//   - 401, 403 (non-CSRF)             -> auth_rejected
//   - 404                             -> not_found (uses pipelineURL for context)
//   - other non-2xx                   -> malformed_response (Jenkins returned an
//     unexpected status; caller can re-run with
//     --debug to inspect the body)
//   - timeout (context, net.Error)    -> timeout
//   - everything else                 -> network
//
// pipelineURL is the user-facing URL the command was operating on; it is
// embedded into the not_found message so the user can copy-paste it.
func translateClientError(host, pipelineURL string, timeout time.Duration, err error) error {
	return translateError(host, timeout, err, func() error {
		return jkerrors.NewNotFound(pipelineURL)
	})
}

// translateBuildClientError is like translateClientError but produces a
// build-specific not_found message when Jenkins returns 404. Use this
// in all build subcommands so that a missing build number yields
// "Build not found" rather than the pipeline-oriented "Pipeline not found".
func translateBuildClientError(host, buildURL string, timeout time.Duration, err error) error {
	return translateError(host, timeout, err, func() error {
		return jkerrors.NewBuildNotFound(buildURL)
	})
}

// translateError is the shared core of translateClientError and
// translateBuildClientError. notFoundFn is called only when the HTTP
// status is 404, letting each caller supply the right error message.
func translateError(host string, timeout time.Duration, err error, notFoundFn func() error) error {
	if err == nil {
		return nil
	}
	// Timeouts get classified first because they may arrive wrapped as
	// a url.Error containing a net.Error. We check the same conditions
	// Classify checks; if either matches, the error is a timeout.
	if stderrors.Is(err, context.DeadlineExceeded) {
		return jkerrors.NewTimeout(host, timeout)
	}
	var nerr interface{ Timeout() bool }
	if stderrors.As(err, &nerr) && nerr.Timeout() {
		return jkerrors.NewTimeout(host, timeout)
	}
	var hse *jenkins.HTTPStatusError
	if stderrors.As(err, &hse) {
		switch hse.StatusCode {
		case 401, 403:
			return jkerrors.NewAuthRejected(host)
		case 404:
			return notFoundFn()
		default:
			return jkerrors.NewMalformedResponse(host, err)
		}
	}
	return jkerrors.NewNetwork(host, err)
}

// resolveRef parses a user-supplied URL into a *jenkinsurl.Ref. It is a
// thin wrapper so every command has identical error wording when a URL
// is malformed.
func resolveRef(rawURL string) (*jenkinsurl.Ref, error) {
	ref, err := jenkinsurl.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	return ref, nil
}

// parseParamFlags converts repeated `-p KEY=VALUE` flag values into a
// map[string]string. A value of the form `@path` is interpreted as
// "read the value from the named file" (per specs/build §"Trigger with
// a parameter value loaded from a file"). The file is read with
// os.ReadFile and submitted verbatim — including trailing newlines —
// because Jenkins parameter values are taken literally and we MUST
// NOT silently strip whitespace that a user may have placed there.
//
// Duplicate KEY: the last occurrence wins. We chose "last wins" over
// "error on duplicate" because cobra/pflag accumulates -p in order,
// and users running `jk build trigger ... -p X=a -p X=b` plainly
// intend to override.
//
// Errors:
//   - malformed entry (no `=`): returned as a JKError-free fmt.Errorf
//     because the only sensible remediation is "fix your command line";
//     cobra prints the result before main() exits with the jk-level
//     code 10.
//   - file read failure: wrapped with the offending path so the user
//     sees which `@file` reference broke.
func parseParamFlags(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(values))
	for _, raw := range values {
		key, val, ok := strings.Cut(raw, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid -p value %q: expected KEY=VALUE", raw)
		}
		if strings.HasPrefix(val, "@") {
			path := strings.TrimPrefix(val, "@")
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("read parameter file %s for %s: %w", path, key, err)
			}
			val = string(data)
		}
		out[key] = val
	}
	return out, nil
}

// withTimeout returns a context bounded by the GlobalFlags.Timeout
// value, plus a cancel function the caller MUST defer. Commands use
// this rather than context.Background() so a hung Jenkins is bounded
// by the same deadline the HTTP transport enforces.
func (cc *commandContext) withTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, cc.flags.Timeout)
}
