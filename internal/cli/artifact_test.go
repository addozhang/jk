package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/addozhang/jk/internal/jenkinsurl"
)

type artifactStreamerFunc func(context.Context, *jenkinsurl.Ref, string, io.Writer) error

func (f artifactStreamerFunc) StreamArtifact(ctx context.Context, ref *jenkinsurl.Ref, path string, w io.Writer) error {
	return f(ctx, ref, path, w)
}

func TestArtifactDestination(t *testing.T) {
	root := t.TempDir()
	for _, relativePath := range []string{"", "/absolute.txt", "../secret.txt", "reports/../secret.txt", "reports/./index.html", "reports/\x00index.html", `reports\index.html`} {
		t.Run(relativePath, func(t *testing.T) {
			if _, err := artifactDestination(root, relativePath); err == nil {
				t.Fatalf("artifactDestination(%q) succeeded", relativePath)
			}
		})
	}
	got, err := artifactDestination(root, "reports/unit/index.html")
	if err != nil {
		t.Fatalf("artifactDestination: %v", err)
	}
	if want := filepath.Join(root, "reports", "unit", "index.html"); got != want {
		t.Fatalf("destination = %q, want %q", got, want)
	}
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "reports"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "reports", "linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := artifactDestination(root, "reports/linked/nested/index.html"); err == nil {
		t.Fatal("expected intermediate symlink rejection")
	}
	rootLink := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(root, rootLink); err != nil {
		t.Fatal(err)
	}
	if _, err := artifactDestination(rootLink, "nested/index.html"); err == nil {
		t.Fatal("expected destination root symlink rejection")
	}
}

func TestDownloadArtifactFile(t *testing.T) {
	ref := &jenkinsurl.Ref{Host: "https://jenkins.example", JobSegments: []string{"svc"}, BuildNumber: 42}
	t.Run("success creates nested destination", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "nested", "app.bin")
		streamer := artifactStreamerFunc(func(_ context.Context, _ *jenkinsurl.Ref, _ string, w io.Writer) error {
			_, err := w.Write([]byte("artifact"))
			return err
		})
		if err := downloadArtifactFile(context.Background(), streamer, ref, "dist/app.bin", destination, false); err != nil {
			t.Fatalf("downloadArtifactFile: %v", err)
		}
		got, _ := os.ReadFile(destination)
		if string(got) != "artifact" {
			t.Fatalf("content = %q", got)
		}
	})

	t.Run("failure removes temporary file", func(t *testing.T) {
		dir := t.TempDir()
		destination := filepath.Join(dir, "app.bin")
		streamer := artifactStreamerFunc(func(_ context.Context, _ *jenkinsurl.Ref, _ string, w io.Writer) error {
			_, _ = w.Write([]byte("partial"))
			return errors.New("interrupted")
		})
		if err := downloadArtifactFile(context.Background(), streamer, ref, "app.bin", destination, false); err == nil {
			t.Fatal("expected error")
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Fatalf("left files: %v", entries)
		}
	})

	t.Run("existing file requires force", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "app.bin")
		if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		called := false
		streamer := artifactStreamerFunc(func(_ context.Context, _ *jenkinsurl.Ref, _ string, w io.Writer) error {
			called = true
			_, err := w.Write([]byte("new"))
			return err
		})
		if err := downloadArtifactFile(context.Background(), streamer, ref, "app.bin", destination, false); err == nil {
			t.Fatal("expected collision error")
		}
		if called {
			t.Fatal("stream called before collision rejection")
		}
		if err := downloadArtifactFile(context.Background(), streamer, ref, "app.bin", destination, true); err != nil {
			t.Fatalf("force: %v", err)
		}
		got, _ := os.ReadFile(destination)
		if string(got) != "new" {
			t.Fatalf("content = %q", got)
		}
	})

	t.Run("rejects destination changed to another regular file during download", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "app.bin")
		if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		streamer := artifactStreamerFunc(func(_ context.Context, _ *jenkinsurl.Ref, _ string, w io.Writer) error {
			if err := os.Remove(destination); err != nil {
				return err
			}
			if err := os.WriteFile(destination, []byte("replacement"), 0o600); err != nil {
				return err
			}
			_, err := w.Write([]byte("new"))
			return err
		})
		if err := downloadArtifactFile(context.Background(), streamer, ref, "app.bin", destination, true); err == nil {
			t.Fatal("expected changed destination rejection")
		}
		got, err := os.ReadFile(destination)
		if err != nil || string(got) != "replacement" {
			t.Fatalf("replacement content=%q err=%v", got, err)
		}
	})

	t.Run("rejects destination changed to symlink during download", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation requires privileges on Windows")
		}
		root := t.TempDir()
		outside := filepath.Join(t.TempDir(), "outside.bin")
		if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		destination := filepath.Join(root, "app.bin")
		if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		streamer := artifactStreamerFunc(func(_ context.Context, _ *jenkinsurl.Ref, _ string, w io.Writer) error {
			if err := os.Remove(destination); err != nil {
				return err
			}
			if err := os.Symlink(outside, destination); err != nil {
				return err
			}
			_, err := w.Write([]byte("new"))
			return err
		})
		if err := downloadArtifactFile(context.Background(), streamer, ref, "app.bin", destination, true); err == nil {
			t.Fatal("expected changed destination rejection")
		}
		info, err := os.Lstat(destination)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("destination no longer symlink: info=%v err=%v", info, err)
		}
		got, err := os.ReadFile(outside)
		if err != nil || string(got) != "outside" {
			t.Fatalf("outside content=%q err=%v", got, err)
		}
	})

	t.Run("rejects parent changed to symlink during download", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation requires privileges on Windows")
		}
		root := t.TempDir()
		parent := filepath.Join(root, "reports")
		if err := os.Mkdir(parent, 0o755); err != nil {
			t.Fatal(err)
		}
		outside := t.TempDir()
		destination := filepath.Join(parent, "index.html")
		streamer := artifactStreamerFunc(func(_ context.Context, _ *jenkinsurl.Ref, _ string, w io.Writer) error {
			if err := os.Remove(parent); err != nil {
				return err
			}
			if err := os.Symlink(outside, parent); err != nil {
				return err
			}
			_, err := w.Write([]byte("new"))
			return err
		})
		if err := downloadArtifactFile(context.Background(), streamer, ref, "reports/index.html", destination, false); err == nil {
			t.Fatal("expected changed parent rejection")
		}
		if _, err := os.Stat(filepath.Join(outside, "index.html")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("outside file was created: %v", err)
		}
	})

	t.Run("rejects symlink parent and destination", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(root, "reports")); err != nil {
			t.Fatal(err)
		}
		streamer := artifactStreamerFunc(func(_ context.Context, _ *jenkinsurl.Ref, _ string, w io.Writer) error { return nil })
		if err := downloadArtifactFile(context.Background(), streamer, ref, "reports/index.html", filepath.Join(root, "reports", "index.html"), false); err == nil {
			t.Fatal("expected symlink parent error")
		}
		link := filepath.Join(root, "artifact-link")
		if err := os.Symlink(filepath.Join(outside, "target"), link); err != nil {
			t.Fatal(err)
		}
		if err := downloadArtifactFile(context.Background(), streamer, ref, "app.bin", link, true); err == nil {
			t.Fatal("expected symlink destination error")
		}
	})
}
