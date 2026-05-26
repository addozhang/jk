package cli

import (
	"bytes"
	"strings"
	"testing"
)

// Test_NewRootCommand_RegistersGlobalFlags asserts that the root command
// exposes the four flags every jk command depends on. If any of these flags
// is renamed or removed, downstream commands silently lose configurability.
func Test_NewRootCommand_RegistersGlobalFlags(t *testing.T) {
	root := NewRootCommand()

	tests := []struct {
		name string
		flag string
	}{
		{name: "output", flag: "output"},
		{name: "insecure", flag: "insecure"},
		{name: "timeout", flag: "timeout"},
		{name: "debug", flag: "debug"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if root.PersistentFlags().Lookup(tt.flag) == nil {
				t.Fatalf("expected persistent flag --%s to be registered", tt.flag)
			}
		})
	}
}

// Test_VersionCommand_PrintsSchemaVersion guards the SPEC.md commitment that
// `jk version` exposes schemaVersion so users can pin scripts.
func Test_VersionCommand_PrintsSchemaVersion(t *testing.T) {
	root := NewRootCommand()
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stdout)
	root.SetArgs([]string{"version"})

	if err := root.Execute(); err != nil {
		t.Fatalf("version command failed: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "schemaVersion "+schemaVersion) {
		t.Fatalf("expected output to contain %q; got %q", "schemaVersion "+schemaVersion, out)
	}
	if !strings.Contains(out, "jk ") {
		t.Fatalf("expected output to start with binary version line; got %q", out)
	}
}
