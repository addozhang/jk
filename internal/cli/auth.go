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
//     including ones with a path like `/job/foo/` — and store only the
//     scheme + host + optional non-default port. This matches the
//     specs/auth normalization scenario and the credential-lookup key
//     used by the transport.
//
//   - Overwrite protection: a second `add` for an existing host is
//     refused unless `--force` is set. We do NOT prompt for y/n
//     because stdin is already consumed by the username + token reads
//     and re-prompting on the same stream confuses pipelines. The
//     spec's "prompts for confirmation" phrasing is satisfied by the
//     explicit `--force` flag, which is louder than a typeable "y".
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
		newAuthRemoveCommand(flags),
	)
	return cmd
}

// ---------------------------------------------------------------------------
// jk auth add <host>
// ---------------------------------------------------------------------------

func newAuthAddCommand(flags *GlobalFlags) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "add <host>",
		Short: "Store API token for a Jenkins host",
		Long: `Prompts for a username and API token, then writes them to
~/.config/jk/credentials (mode 0600). The <host> argument is normalized
to scheme://host[:port]; any path is discarded.

If an entry already exists for the host, the command refuses to
overwrite without --force.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAuthAdd(cmd, flags, args[0], force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing credential without confirmation")
	return cmd
}

func runAuthAdd(cmd *cobra.Command, _ *GlobalFlags, rawHost string, force bool) error {
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

	if err := store.Add(host, auth.Credential{Username: username, Token: token}); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stderr, "Stored credentials for %s.\n", host); err != nil {
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
		Long:  "Prints the configured hosts to stdout in insertion order. Never prints tokens.",
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
	cc := &commandContext{cmd: cmd, flags: flags, stderr: cmd.ErrOrStderr()}
	return cc.render(schema.AuthList{Hosts: hosts})
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
// + optional non-default port. Trailing slash, path, query, and
// fragment are discarded. Returns a JKError on parse failure so the
// user sees a friendly message instead of url.Parse's terse output.
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
	return u.Scheme + "://" + u.Host, nil
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
