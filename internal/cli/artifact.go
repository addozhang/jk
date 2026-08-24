package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	jkerrors "github.com/addozhang/jk/internal/errors"
	"github.com/addozhang/jk/internal/jenkins"
	"github.com/addozhang/jk/internal/jenkinsurl"
	"github.com/addozhang/jk/internal/schema"
)

type artifactStreamer interface {
	StreamArtifact(context.Context, *jenkinsurl.Ref, string, io.Writer) error
}

type artifactDestinationState struct {
	exists bool
	info   os.FileInfo
}

func newBuildArtifactsCommand(flags *GlobalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "artifacts <build-url>",
		Short: "List artifacts archived by a build",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBuildArtifacts(cmd, flags, args[0])
		},
	}
	cmd.AddCommand(newBuildArtifactsFetchCommand(flags))
	return cmd
}

func runBuildArtifacts(cmd *cobra.Command, flags *GlobalFlags, rawURL string) error {
	_, artifacts, cc, err := loadBuildArtifacts(cmd, flags, rawURL)
	if err != nil {
		return err
	}
	return cc.render(artifacts)
}

func newBuildArtifactCommand(flags *GlobalFlags) *cobra.Command {
	var destination string
	var force bool
	cmd := &cobra.Command{
		Use:   "artifact <build-url> <relative-path>",
		Short: "Download one artifact archived by a build",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBuildArtifact(cmd, flags, args[0], args[1], destination, force)
		},
	}
	cmd.Flags().StringVar(&destination, "destination", "", "destination file path")
	if err := cmd.MarkFlagRequired("destination"); err != nil {
		panic(err)
	}
	cmd.Flags().BoolVar(&force, "force", false, "replace an existing regular file")
	return cmd
}

func runBuildArtifact(cmd *cobra.Command, flags *GlobalFlags, rawURL, relativePath, destination string, force bool) error {
	ref, artifacts, cc, err := loadBuildArtifacts(cmd, flags, rawURL)
	if err != nil {
		return err
	}
	found := false
	for _, artifact := range artifacts.Artifacts {
		if artifact.RelativePath == relativePath {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("artifact %q was not archived by this build", relativePath)
	}
	if _, err := artifactDestination(".", relativePath); err != nil {
		return err
	}
	ctx, cancel := cc.withTimeout(cmd.Context())
	defer cancel()
	if err := downloadArtifactFile(ctx, cc.client, ref, relativePath, destination, force); err != nil {
		return artifactDownloadError(ref.Host, rawURL, flags.Timeout, relativePath, err)
	}
	return nil
}

func newBuildArtifactsFetchCommand(flags *GlobalFlags) *cobra.Command {
	var directory string
	var force bool
	cmd := &cobra.Command{
		Use:   "fetch <build-url>",
		Short: "Download all artifacts archived by a build",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBuildArtifactsFetch(cmd, flags, args[0], directory, force)
		},
	}
	cmd.Flags().StringVar(&directory, "directory", "", "destination directory")
	if err := cmd.MarkFlagRequired("directory"); err != nil {
		panic(err)
	}
	cmd.Flags().BoolVar(&force, "force", false, "replace existing regular files")
	return cmd
}

func runBuildArtifactsFetch(cmd *cobra.Command, flags *GlobalFlags, rawURL, directory string, force bool) error {
	ref, artifacts, cc, err := loadBuildArtifacts(cmd, flags, rawURL)
	if err != nil {
		return err
	}
	for _, artifact := range artifacts.Artifacts {
		destination, err := artifactDestination(directory, artifact.RelativePath)
		if err != nil {
			return fmt.Errorf("artifact %q: %w", artifact.RelativePath, err)
		}
		ctx, cancel := cc.withTimeout(cmd.Context())
		err = downloadArtifactFile(ctx, cc.client, ref, artifact.RelativePath, destination, force)
		cancel()
		if err != nil {
			return artifactDownloadError(ref.Host, rawURL, flags.Timeout, artifact.RelativePath, err)
		}
	}
	return nil
}

func loadBuildArtifacts(cmd *cobra.Command, flags *GlobalFlags, rawURL string) (*jenkinsurl.Ref, *schema.BuildArtifacts, *commandContext, error) {
	ref, err := resolveBuildRef(rawURL)
	if err != nil {
		return nil, nil, nil, err
	}
	cc, err := newCommandContext(cmd, flags)
	if err != nil {
		return nil, nil, nil, err
	}
	ctx, cancel := cc.withTimeout(cmd.Context())
	body, err := cc.client.GetBuildArtifacts(ctx, ref)
	cancel()
	if err != nil {
		return nil, nil, nil, translateBuildClientError(ref.Host, rawURL, flags.Timeout, err)
	}
	artifacts, err := schema.MapBuildArtifacts(body)
	if err != nil {
		return nil, nil, nil, jkerrors.NewMalformedResponse(ref.Host, err)
	}
	return ref, artifacts, cc, nil
}

func artifactDownloadError(host, buildURL string, timeout time.Duration, relativePath string, err error) error {
	var httpErr *jenkins.HTTPStatusError
	var networkErr *url.Error
	if errors.As(err, &httpErr) || errors.As(err, &networkErr) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return fmt.Errorf("download artifact %q: %w", relativePath, translateBuildClientError(host, buildURL, timeout, err))
	}
	return fmt.Errorf("download artifact %q: %w", relativePath, err)
}

func artifactDestination(root, relativePath string) (string, error) {
	if relativePath == "" || strings.ContainsRune(relativePath, 0) || strings.Contains(relativePath, `\`) {
		return "", fmt.Errorf("invalid artifact path %q", relativePath)
	}
	if path.IsAbs(relativePath) {
		return "", fmt.Errorf("artifact path %q must be relative", relativePath)
	}
	for _, component := range strings.Split(relativePath, "/") {
		if component == "" || component == "." || component == ".." {
			return "", fmt.Errorf("invalid artifact path component in %q", relativePath)
		}
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve artifact directory: %w", err)
	}
	destination := filepath.Join(rootAbs, filepath.FromSlash(relativePath))
	rel, err := filepath.Rel(rootAbs, destination)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("artifact path %q escapes destination directory", relativePath)
	}
	if err := rejectSymlinkParents(rootAbs, filepath.Dir(destination)); err != nil {
		return "", err
	}
	return destination, nil
}

func rejectSymlinkParents(root, parent string) error {
	rel, err := filepath.Rel(root, parent)
	if err != nil {
		return fmt.Errorf("resolve artifact parent: %w", err)
	}
	if err := validateArtifactDirectory(root); err != nil {
		return err
	}
	current := root
	if rel == "." {
		return nil
	}
	components := strings.Split(rel, string(filepath.Separator))
	for _, component := range components {
		current = filepath.Join(current, component)
		if err := validateArtifactDirectory(current); err != nil {
			return err
		}
	}
	return nil
}

func validateArtifactDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect artifact parent %q: %w", directory, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("artifact parent %q is a symbolic link", directory)
	}
	if !info.IsDir() {
		return fmt.Errorf("artifact parent %q is not a directory", directory)
	}
	return nil
}

func downloadArtifactFile(ctx context.Context, client artifactStreamer, ref *jenkinsurl.Ref, relativePath, destination string, force bool) (err error) {
	destination, err = filepath.Abs(destination)
	if err != nil {
		return fmt.Errorf("resolve destination path: %w", err)
	}
	parent := filepath.Dir(destination)
	if directoryErr := ensureRealDirectories(parent); directoryErr != nil {
		return directoryErr
	}
	destinationState, err := inspectArtifactDestination(destination, force)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(parent, ".jk-artifact-*")
	if err != nil {
		return fmt.Errorf("create temporary artifact file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close() //nolint:errcheck // best-effort cleanup; the successful close is checked below
		if err != nil {
			_ = os.Remove(tmpName) //nolint:errcheck // best-effort cleanup while preserving the primary error
		}
	}()

	if err = client.StreamArtifact(ctx, ref, relativePath, tmp); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close temporary artifact file: %w", err)
	}
	if err = ensureRealDirectories(parent); err != nil {
		return err
	}
	if err = verifyArtifactDestination(destination, destinationState); err != nil {
		return err
	}
	if err = replaceArtifactFile(tmpName, destination); err != nil {
		return fmt.Errorf("install artifact at %q: %w", destination, err)
	}
	return nil
}

func inspectArtifactDestination(destination string, force bool) (artifactDestinationState, error) {
	info, err := os.Lstat(destination)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return artifactDestinationState{}, fmt.Errorf("destination %q is not a regular file", destination)
		}
		if !force {
			return artifactDestinationState{}, fmt.Errorf("destination %q already exists (use --force to replace it)", destination)
		}
		return artifactDestinationState{exists: true, info: info}, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return artifactDestinationState{}, nil
	}
	return artifactDestinationState{}, fmt.Errorf("inspect destination %q: %w", destination, err)
}

func verifyArtifactDestination(destination string, expected artifactDestinationState) error {
	info, err := os.Lstat(destination)
	if errors.Is(err, os.ErrNotExist) {
		if expected.exists {
			return fmt.Errorf("destination %q changed during download", destination)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect destination %q: %w", destination, err)
	}
	if !expected.exists || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !os.SameFile(expected.info, info) {
		return fmt.Errorf("destination %q changed during download", destination)
	}
	return nil
}

func ensureRealDirectories(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve destination directory: %w", err)
	}
	current := abs
	missing := []string{}
	for {
		info, statErr := os.Lstat(current)
		if statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("destination parent %q is a symbolic link", current)
			}
			if !info.IsDir() {
				return fmt.Errorf("destination parent %q is not a directory", current)
			}
			break
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("inspect destination parent %q: %w", current, statErr)
		}
		parent := filepath.Dir(current)
		missing = append(missing, filepath.Base(current))
		if parent == current {
			return fmt.Errorf("cannot locate existing parent for %q", abs)
		}
		current = parent
	}
	for i := len(missing) - 1; i >= 0; i-- {
		current = filepath.Join(current, missing[i])
		if err := os.Mkdir(current, 0o755); err != nil {
			return fmt.Errorf("create destination directory %q: %w", current, err)
		}
	}
	return nil
}
