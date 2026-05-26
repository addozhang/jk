// Package cli wires Cobra commands together. It owns flag registration,
// subcommand grouping, and the global flag struct shared across commands.
// Business logic belongs in sibling internal packages (jenkins, schema,
// output, errors, auth, jenkinsurl); cli only orchestrates.
package cli

import (
	"time"

	"github.com/spf13/cobra"
)

// GlobalFlags collects flags that apply to every jk command. The pointer is
// shared by NewRootCommand into each subcommand so they can read the final
// values after Cobra has parsed the command line.
type GlobalFlags struct {
	// Output controls the response format: "yaml" (default), "json", or "raw".
	Output string
	// Insecure disables TLS certificate verification when true. The CLI must
	// print a stderr warning when this is enabled (see specs/tls-and-transport).
	Insecure bool
	// Timeout bounds every outbound HTTP request. Default 30s.
	Timeout time.Duration
	// Debug enables HTTP request/response logging to stderr with Authorization
	// redacted (see specs/tls-and-transport).
	Debug bool
}

// NewRootCommand returns the root `jk` command with global flags registered
// and all subcommands attached. It is the single entry point used by both
// cmd/jk/main.go and tests.
func NewRootCommand() *cobra.Command {
	flags := &GlobalFlags{}

	root := &cobra.Command{
		Use:           "jk",
		Short:         "Pipeline-native Jenkins CLI",
		Long:          "jk drives Jenkins pipelines from the terminal. URLs are the unit of identity; output defaults to YAML with a stable schema (see docs/schema.md).",
		SilenceUsage:  true, // Cobra's usage dump on every error is noise; we print our own actionable errors.
		SilenceErrors: true, // main owns error printing + exit-code mapping.
	}

	root.PersistentFlags().StringVarP(&flags.Output, "output", "o", "yaml", "output format: yaml, json, or raw")
	root.PersistentFlags().BoolVar(&flags.Insecure, "insecure", false, "disable TLS certificate verification (prints warning to stderr)")
	root.PersistentFlags().DurationVar(&flags.Timeout, "timeout", 30*time.Second, "per-request timeout (e.g. 30s, 2m)")
	root.PersistentFlags().BoolVar(&flags.Debug, "debug", false, "log HTTP requests/responses to stderr (Authorization redacted)")

	root.AddCommand(newVersionCommand())
	root.AddCommand(newPipelineCommand(flags))
	root.AddCommand(newBuildCommand(flags))
	root.AddCommand(newAuthCommand(flags))

	return root
}
