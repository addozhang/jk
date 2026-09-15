package cli

// auth.go wires `jk auth add|list|remove`. The auth commands are the
// only ones that read interactively from the terminal: `add` prompts
// for a username (plain echo) and an API token (no echo on TTY).
//
// Design notes:
//
//   - Token entry is funneled through the package-level `readSecret`
//     hook so tests can substitute a non-TTY reader. Production calls
//     golang.org/x/term.ReadPassword when stdin is a terminal, else
//     falls back to a newline-delimited read so piped input (`echo tok
//     | jk auth add ...`) still works in CI.
//
//   - Host normalization: we accept any URL the user might paste —
//     including ones with a path like `/job/foo/` — and store the
//     scheme + host + optional non-default port, plus an optional
//     context-path prefix (the path before the first `/job/`, or the
//     whole path when there is no `/job/`). This matches the
//     specs/auth normalization scenarios and the credential-lookup key
//     used by the transport (auth.Store.Resolve).
//
//   - Overwrite protection: a second `add` for an existing host is
//     refused unless `--force` is set. We do NOT prompt for y/n
//     because stdin is already consumed by the username + token reads
//     and re-prompting on the same stream confuses pipelines. The
//     spec's "prompts for confirmation" phrasing is satisfied by the
//     explicit `--force` flag, which is louder than a typeable "y".
//
//   - Secure storage (opt-in): `--secure-storage` (or JK_SECURE_STORAGE=1)
//     routes the token to the OS keyring via the auth package; the
//     credentials file then records only the username for the host. All
//     keyring interaction lives in internal/auth so the injectable
//     keyring hooks stay testable; the CLI only passes the Secure flag
//     through and mirrors the choice in its confirmation message.
//
//   - `add` and `remove` print human-readable confirmations to stderr
//     and do NOT emit structured output (per docs/schema.md §3.2);
//     `list` renders schema.AuthList to stdout via the normal output
//     layer so `-o json` and schemaVersion injection both work.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/addozhang/jk/internal/auth"
	jkerrors "github.com/addozhang/jk/internal/errors"
	"github.com/addozhang/jk/internal/schema"
)

// readSecret is the package-level hook used by `auth add` to read an
// API token without echoing it. Production wires it to ReadPassword
// when in is a terminal; tests override it with a buffered-line read.
// The prompt is written to out so the user sees what is being asked.
//
// The `in` parameter is a *bufio.Reader (not a raw io.Reader) so a
// single buffer is shared across the username and token prompts. If
// each prompt allocated its own bufio.Reader, the first one would
// gobble the token bytes into a private 4 KiB buffer and the second
// would see EOF.
var readSecret = func(prompt string, in *bufio.Reader, out io.Writer) (string, error) {
	if _, err := fmt.Fprint(out, prompt); err != nil {
		return "", err
	}
	// Terminal detection: only attempt ReadPassword when stdin is a
	// real *os.File attached to a TTY. The bufio.Reader wrapping
	// hides the underlying type, so we re-peek os.Stdin directly —
	// terminal interaction only happens against the process's real
	// stdin anyway. Tests substitute this whole function so they
	// never hit this branch.
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(out) //nolint:errcheck // best-effort cosmetic newline
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	line, err := in.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// secureHostLister is the optional capability a Store may implement to
// report which hosts keep their token in the OS keyring. It is an
// interface upgrade rather than a Store method so alternate Store
// implementations (test fakes, future backends) stay valid without
// growing keyring-aware code they do not need.
type secureHostLister interface {
	SecureHosts() ([]string, error)
}

// newAuthCommand returns the `jk auth` parent + its three subcommands.
func newAuthCommand(flags *GlobalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage Jenkins credentials",
		Long:  "Store and list per-host Jenkins API tokens at ~/.config/jk/credentials.",
	}
	cmd.AddCommand(
		newAuthAddCommand(flags),
		newAuthListCommand(flags),
		newAuthWhoAmICommand(flags),
		newAuthRemoveCommand(flags),
	)
	return cmd
}

// ---------------------------------------------------------------------------
// jk auth whoami <url>
// ---------------------------------------------------------------------------

func newAuthWhoAmICommand(flags *GlobalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "whoami <url>",
		Short: "Show the authenticated Jenkins user",
		Long: `Verify the credentials selected for <url> by querying Jenkins's
whoAmI endpoint. The URL may be a Jenkins root URL or a job URL; context paths
are preserved. Tokens are never printed.

See docs/schema.md for the response shape.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAuthWhoAmI(cmd, flags, args[0])
		},
	}
}

func runAuthWhoAmI(cmd *cobra.Command, flags *GlobalFlags, rawURL string) error {
	baseURL, err := normalizeWhoAmIURL(rawURL)
	if err != nil {
		return err
	}
	cc, err := newCommandContext(cmd, flags)
	if err != nil {
		return err
	}
	ctx, cancel := cc.withTimeout(cmd.Context())
	defer cancel()
	body, err := cc.client.GetWhoAmI(ctx, baseURL)
	if err != nil {
		return translateWhoAmIError(baseURL, flags.Timeout, err)
	}
	identity, err := schema.MapAuthWhoAmI(body)
	if err != nil {
		return jkerrors.NewMalformedResponse(baseURL, err)
	}
	return cc.render(identity)
}

func translateWhoAmIError(host string, timeout time.Duration, err error) error {
	return translateError(host, timeout, err, func() error {
		return jkerrors.NewIdentityEndpointNotFound(host)
	})
}

func normalizeWhoAmIURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", &jkerrors.JKError{
			Code:       "invalid_url",
			Message:    fmt.Sprintf("Host %q is missing a scheme or hostname.", raw),
			Suggestion: "Use a URL like https://jenkins.example.com",
		}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", &jkerrors.JKError{
			Code:    "invalid_url",
			Message: fmt.Sprintf("Unsupported scheme %q (expected http or https).", u.Scheme),
		}
	}
	return u.Scheme + "://" + u.Host + extractBasePath(u.Path), nil
}

// ---------------------------------------------------------------------------
// jk auth add <host>
// ---------------------------------------------------------------------------

func newAuthAddCommand(flags *GlobalFlags) *cobra.Command {
	var force bool
	var secureStorage bool
	cmd := &cobra.Command{
		Use:   "add <host>",
		Short: "Store API token for a Jenkins host",
		Long: `Prompts for a username and API token, then writes them to
~/.config/jk/credentials (mode 0600). The <host> argument is normalized
to scheme://host[:port] plus an optional context-path prefix (the part of
the path before the first /job/ segment, or the whole path when there is
no /job/). Job hierarchy and trailing slashes are discarded. The
confirmation message names the exact key that was stored.

With --secure-storage (or JK_SECURE_STORAGE=1) the API token is stored
in the OS keyring instead of the credentials file; the file records only
the username for the host. Re-adding the host without --secure-storage
migrates it back to file storage and deletes the keyring entry.

If an entry already exists for the key, the command refuses to
overwrite without --force.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAuthAdd(cmd, flags, args[0], force, secureStorage)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing credential without confirmation")
	cmd.Flags().BoolVar(&secureStorage, "secure-storage", false, "store the API token in the OS keyring instead of the credentials file")
	return cmd
}

func runAuthAdd(cmd *cobra.Command, _ *GlobalFlags, rawHost string, force, secureStorage bool) error {
	host, err := normalizeAuthHost(rawHost)
	if err != nil {
		return err
	}

	store, err := openCredentialStore()
	if err != nil {
		return err
	}

	_, exists, err := store.Get(host)
	if err != nil {
		return err
	}
	if exists && !force {
		return &jkerrors.JKError{
			Code:       "credential_exists",
			Message:    fmt.Sprintf("Credentials already configured for %s.", host),
			Suggestion: "Re-run with --force to overwrite.",
		}
	}

	in := bufio.NewReader(cmd.InOrStdin())
	stderr := cmd.ErrOrStderr()

	username, err := promptLine(in, stderr, fmt.Sprintf("Username for %s: ", host))
	if err != nil {
		return fmt.Errorf("read username: %w", err)
	}
	if username == "" {
		return &jkerrors.JKError{
			Code:    "credential_invalid",
			Message: "Username must not be empty.",
		}
	}
	token, err := readSecret(fmt.Sprintf("API token for %s: ", host), in, stderr)
	if err != nil {
		return fmt.Errorf("read token: %w", err)
	}
	if token == "" {
		return &jkerrors.JKError{
			Code:    "credential_invalid",
			Message: "API token must not be empty.",
		}
	}

	// The env var makes secure storage scriptable (CI, agent setups)
	// without rewriting the command line; the flag always wins. The
	// keyring round-trip itself happens inside auth store.Add.
	secure := secureStorage || os.Getenv("JK_SECURE_STORAGE") == "1"
	if err := store.Add(host, auth.Credential{Username: username, Token: token, Secure: secure}); err != nil {
		return err
	}
	storage := "the credentials file"
	if secure {
		storage = "the OS keyring"
	}
	if _, err := fmt.Fprintf(stderr, "Stored credentials for %s (token in %s).\n", host, storage); err != nil {
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// jk auth list
// ---------------------------------------------------------------------------

func newAuthListCommand(flags *GlobalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured Jenkins hosts",
		Long:  "Prints the configured hosts to stdout in insertion order, plus which hosts keep their token in the OS keyring. Never prints tokens. See docs/schema.md §3.1 for the response shape.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAuthList(cmd, flags)
		},
	}
}

func runAuthList(cmd *cobra.Command, flags *GlobalFlags) error {
	store, err := openCredentialStore()
	if err != nil {
		return err
	}
	hosts, err := store.List()
	if err != nil {
		return err
	}
	// schema.AuthList.Hosts is documented as always non-nil; List()
	// already guarantees a non-nil slice but we defend here too so a
	// future refactor of Store can't silently break the contract.
	if hosts == nil {
		hosts = []string{}
	}
	// Additive schema field (docs/schema.md §3.1): which hosts keep
	// their token in the OS keyring. Optional-interface upgrade so the
	// core Store interface stays keyring-agnostic; a Store without the
	// capability simply reports an empty list.
	secure := []string{}
	if sl, ok := store.(secureHostLister); ok {
		secure, err = sl.SecureHosts()
		if err != nil {
			return err
		}
		if secure == nil {
			secure = []string{}
		}
	}
	cc := &commandContext{cmd: cmd, flags: flags, stderr: cmd.ErrOrStderr()}
	return cc.render(schema.AuthList{Hosts: hosts, Secure: secure})
}

// ---------------------------------------------------------------------------
// jk auth remove <host>
// ---------------------------------------------------------------------------

func newAuthRemoveCommand(flags *GlobalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <host>",
		Short: "Remove stored credentials for a host",
		Long:  "Idempotent: removing a host that is not configured is not an error.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAuthRemove(cmd, flags, args[0])
		},
	}
}

func runAuthRemove(cmd *cobra.Command, _ *GlobalFlags, rawHost string) error {
	host, err := normalizeAuthHost(rawHost)
	if err != nil {
		return err
	}
	store, err := openCredentialStore()
	if err != nil {
		return err
	}
	if err := store.Remove(host); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Removed credentials for %s (if any).\n", host); err != nil {
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// openCredentialStore wires the on-disk credential store. Extracted
// because all three auth subcommands need the same construction.
func openCredentialStore() (auth.Store, error) {
	path, err := defaultCredentialsPath()
	if err != nil {
		return nil, fmt.Errorf("locate credentials file: %w", err)
	}
	store, err := auth.NewFileStore(path)
	if err != nil {
		return nil, fmt.Errorf("open credentials store: %w", err)
	}
	return store, nil
}

// normalizeAuthHost reduces a user-supplied URL to its scheme + host
// + optional non-default port, plus an optional context-path prefix.
// The prefix is the portion of the path preceding the first `/job/`
// segment, or the entire path when the URL contains no `/job/` segment
// (see extractBasePath); it is normalized to a leading `/` with no
// trailing slash, and an empty result yields a host-only key identical
// to prior behavior. Query and fragment are discarded. Returns a JKError
// on parse failure so the user sees a friendly message instead of
// url.Parse's terse output.
//
// Anchoring the stored key on the same base-path rule that request
// resolution uses (auth.Store.Resolve) guarantees a context-path entry
// lands on the segment boundary the resolver later computes.
func normalizeAuthHost(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", &jkerrors.JKError{
			Code:    "invalid_url",
			Message: "Host argument must not be empty.",
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", &jkerrors.JKError{
			Code:       "invalid_url",
			Message:    fmt.Sprintf("Could not parse host %q.", raw),
			Suggestion: "Use a URL like https://jenkins.example.com",
		}
	}
	if u.Scheme == "" || u.Host == "" {
		return "", &jkerrors.JKError{
			Code:       "invalid_url",
			Message:    fmt.Sprintf("Host %q is missing a scheme or hostname.", raw),
			Suggestion: "Use a URL like https://jenkins.example.com",
		}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", &jkerrors.JKError{
			Code:    "invalid_url",
			Message: fmt.Sprintf("Unsupported scheme %q (expected http or https).", u.Scheme),
		}
	}
	return u.Scheme + "://" + u.Host + extractBasePath(u.Path), nil
}

// extractBasePath reduces a URL path to the Jenkins context-path prefix that
// becomes part of the credential key. The prefix is the portion preceding the
// first `/job/` segment; when the path contains no `/job/` segment the entire
// path is treated as the prefix (so `auth add https://h/team-a` keys on
// `/team-a`). The result is normalized to a leading `/` with no trailing
// slash; an empty or pure-job path yields "" (a host-only key). The `/job/`
// boundary handling mirrors jenkinsurl's base-path extraction so a stored key
// lands on the same segment boundary that request resolution computes.
func extractBasePath(rawPath string) string {
	trimmed := strings.Trim(rawPath, "/")
	if trimmed == "" {
		return ""
	}
	parts := strings.Split(trimmed, "/")
	jobIdx := -1
	for i, p := range parts {
		if p == "job" {
			jobIdx = i
			break
		}
	}
	switch {
	case jobIdx == 0:
		// Path is job hierarchy from the start: no context path.
		return ""
	case jobIdx > 0:
		parts = parts[:jobIdx]
	}
	// jobIdx == -1: no `job` token anywhere; keep the whole path as prefix.
	return "/" + strings.Join(parts, "/")
}

// promptLine writes prompt to out and reads a single newline-delimited
// line from in (trimmed of \r\n). Used for the username prompt where
// echo is fine; the no-echo token prompt uses readSecret instead.
// The shared *bufio.Reader matters: see readSecret comment.
func promptLine(in *bufio.Reader, out io.Writer, prompt string) (string, error) {
	if _, err := fmt.Fprint(out, prompt); err != nil {
		return "", err
	}
	line, err := in.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
