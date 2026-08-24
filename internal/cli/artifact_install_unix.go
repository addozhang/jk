//go:build !windows

package cli

import "os"

func replaceArtifactFile(source, destination string) error {
	return os.Rename(source, destination)
}
