package cli

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// schemaVersion is the current major version of jk's self-owned output schema.
// Bumping this value is governed by SPEC.md §Schema Review Workflow.
const schemaVersion = "1"

// version is overridden at build time by `-ldflags "-X ..."` (set by GoReleaser).
// At `go install` time it remains "dev" and we fall back to module info.
var version = "dev"

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print jk version and schema version",
		Long:  "Prints the jk binary version and the output schema version. Use schemaVersion to pin scripts; see docs/schema.md.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			v := resolveVersion()
			// stdout, not stderr: machine-readable consumption is allowed.
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "jk %s\nschemaVersion %s\n", v, schemaVersion)
			return err
		},
	}
}

func resolveVersion() string {
	if version != "dev" {
		return version
	}
	// When installed via `go install`, the ldflag isn't set; read the embedded
	// module version (e.g. v0.1.0) if available.
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
