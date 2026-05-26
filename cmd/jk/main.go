// Command jk is a Pipeline-native Jenkins CLI.
//
// jk addresses Jenkins pipelines by URL: every command accepts a full Jenkins
// URL and the hostname implicitly selects stored credentials. Output defaults
// to YAML with a stable, self-owned schema (see docs/schema.md); use
// -o json|raw to switch formats.
//
// This package contains only wiring; all behavior lives under internal/.
// Command jk is a Pipeline-native Jenkins CLI.
//
// jk addresses Jenkins pipelines by URL: every command accepts a full Jenkins
// URL and the hostname implicitly selects stored credentials. Output defaults
// to YAML with a stable, self-owned schema (see docs/schema.md); use
// -o json|raw to switch formats.
//
// This package contains only wiring; all behavior lives under internal/.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/addozhang/jk/internal/cli"
	jkerrors "github.com/addozhang/jk/internal/errors"
)

func main() {
	// Run the root command. The root has SilenceErrors=true (set in
	// internal/cli/root.go) so cobra does not print the error itself
	// — main owns error rendering so we can format JKError suggestions
	// alongside the message and pick the right exit code.
	//
	// Exit codes (per openspec/.../specs/errors/spec.md):
	//   0      success
	//   0..9   command-result codes (e.g. build trigger --watch result)
	//   >= 10  jk-level failures (auth, network, parsing, config)
	//
	// BuildResultExitError implements ExitCoder with a code in [0,9];
	// all other errors fall through to the jk-level default (10).
	err := cli.NewRootCommand().Execute()
	if err != nil {
		printError(err)
	}
	os.Exit(jkerrors.ExitCode(err))
}

// printError writes a human-friendly error message to stderr. JKError
// is rendered as "Error: <message>" followed by an indented Suggestion
// block when present; other errors fall through to err.Error().
//
// BuildResultExitError carries its own non-zero exit code but does not
// represent a jk-level failure (the build itself finished with a
// non-success result), so we deliberately do NOT print anything for
// it: the user already saw the live --watch output, and a trailing
// "Error:" line would be misleading.
func printError(err error) {
	var bre *jkerrors.BuildResultExitError
	if errors.As(err, &bre) {
		return
	}
	var jk *jkerrors.JKError
	if errors.As(err, &jk) {
		fmt.Fprintf(os.Stderr, "Error: %s\n", jk.Message)
		if jk.Suggestion != "" {
			fmt.Fprintf(os.Stderr, "%s\n", jk.Suggestion)
		}
		return
	}
	fmt.Fprintf(os.Stderr, "Error: %s\n", err.Error())
}
