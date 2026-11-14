package wal

import (
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/vedantsahu06/raftkv/internal/dbdir"
	"github.com/vedantsahu06/raftkv/internal/filesystem"
	"github.com/vedantsahu06/raftkv/internal/kverr"
	"github.com/vedantsahu06/raftkv/internal/platform"
)

// newTestWALDir creates a fresh database layout through the platform filesystem
// and returns the WAL directory path.
func newTestWALDir(t *testing.T) (filesystem.FS, string) {
	t.Helper()

	root := filepath.Join(t.TempDir(), "db")
	fsys, err := platform.New(root)
	if err != nil {
		if errors.Is(err, kverr.ErrUnsupported) || errors.Is(err, kverr.ErrUnsupportedVolume) {
			t.Skipf("test environment is not supported: %v", err)
		}
		t.Fatalf("platform.New(%q): %v", root, err)
	}
	layout, err := dbdir.Initialize(fsys, root)
	if err != nil {
		t.Fatalf("dbdir.Initialize(%q): %v", root, err)
	}
	return fsys, layout.WAL
}

func readTestFile(t *testing.T, fsys filesystem.FS, path string) []byte {
	t.Helper()

	f, err := fsys.Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	contents, readErr := io.ReadAll(f)
	closeErr := f.Close()
	if readErr != nil {
		t.Fatalf("ReadAll(%q): %v", path, readErr)
	}
	if closeErr != nil {
		t.Fatalf("Close(%q): %v", path, closeErr)
	}
	return contents
}

func writeTestFile(t *testing.T, fsys filesystem.FS, path string, contents []byte) {
	t.Helper()

	f, err := fsys.CreateExclusive(path)
	if err != nil {
		t.Fatalf("CreateExclusive(%q): %v", path, err)
	}
	written, writeErr := f.Write(contents)
	if writeErr != nil {
		f.Close()
		t.Fatalf("Write(%q): %v", path, writeErr)
	}
	if written != len(contents) {
		f.Close()
		t.Fatalf("Write(%q) wrote %d bytes, want %d", path, written, len(contents))
	}
	if err := f.Sync(); err != nil {
		f.Close()
		t.Fatalf("Sync(%q): %v", path, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close(%q): %v", path, err)
	}
}

func requireExists(t *testing.T, fsys filesystem.FS, path string) fs.FileInfo {
	t.Helper()

	info, err := fsys.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q) = %v, want an existing path", path, err)
	}
	return info
}

func requireNotExist(t *testing.T, fsys filesystem.FS, path string) {
	t.Helper()

	_, err := fsys.Stat(path)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Stat(%q) = %v, want fs.ErrNotExist", path, err)
	}
}
