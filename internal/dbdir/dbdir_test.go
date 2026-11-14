package dbdir

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/vedantsahu06/raftkv/internal/filesystem"
	"github.com/vedantsahu06/raftkv/internal/kverr"
	"github.com/vedantsahu06/raftkv/internal/platform"
)

func newTestFS(t *testing.T, root string) filesystem.FS {
	t.Helper()
	fsys, err := platform.New(root)
	if err != nil {
		if errors.Is(err, kverr.ErrUnsupported) || errors.Is(err, kverr.ErrUnsupportedVolume) {
			t.Skipf("test environment is not supported: %v", err)
		}
		t.Fatalf("platform.New(%q): %v", root, err)
	}
	return fsys
}

func TestInitializeFreshLayout(t *testing.T) {
	root := filepath.Join(t.TempDir(), "db")
	fsys := newTestFS(t, root)

	layout, err := Initialize(fsys, root)
	if err != nil {
		t.Fatalf("Initialize() = %v, want nil", err)
	}
	if want := LayoutFor(root); layout != want {
		t.Fatalf("Initialize() layout = %+v, want %+v", layout, want)
	}

	for _, dir := range append([]string{layout.Root}, layout.Directories()...) {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %q: %v", dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("%q is not a directory", dir)
		}
	}

	inventory, err := Inspect(fsys, root)
	if err != nil {
		t.Fatalf("Inspect() = %v, want nil", err)
	}
	if !inventory.Empty() {
		t.Fatalf("Inspect() artifacts = %+v, want empty", inventory.Artifacts)
	}
}

func TestInitializeIsIdempotentOnEmptyLayout(t *testing.T) {
	root := filepath.Join(t.TempDir(), "db")
	fsys := newTestFS(t, root)

	if _, err := Initialize(fsys, root); err != nil {
		t.Fatalf("first Initialize() = %v", err)
	}
	if _, err := Initialize(fsys, root); err != nil {
		t.Fatalf("second Initialize() = %v", err)
	}
}

func TestInitializeCreatesNestedRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "a", "b", "db")
	fsys := newTestFS(t, root)

	layout, err := Initialize(fsys, root)
	if err != nil {
		t.Fatalf("Initialize() = %v, want nil", err)
	}
	for _, dir := range append([]string{layout.Root}, layout.Directories()...) {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %q: %v", dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("%q is not a directory", dir)
		}
	}
}

func TestInspectReportsArtifacts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "db")
	fsys := newTestFS(t, root)
	if _, err := Initialize(fsys, root); err != nil {
		t.Fatalf("Initialize() = %v", err)
	}

	files := map[string]string{
		"wal/wal-00000000000000000001.wal.tmp":       "wal",
		"sst/sstable-000001.sst":                     "sst",
		"manifest/MANIFEST-00000000000000000001.tmp": "manifest",
		"unexpected.txt":                             "root",
	}
	for rel, contents := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatalf("write artifact %q: %v", rel, err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "extra"), 0o700); err != nil {
		t.Fatalf("mkdir extra: %v", err)
	}

	inventory, err := Inspect(fsys, root)
	if err != nil {
		t.Fatalf("Inspect() = %v, want nil", err)
	}
	var got []string
	for _, artifact := range inventory.Artifacts {
		got = append(got, artifact.Path)
	}
	slices.Sort(got)

	want := []string{
		"extra",
		"manifest/MANIFEST-00000000000000000001.tmp",
		"sst/sstable-000001.sst",
		"unexpected.txt",
		"wal/wal-00000000000000000001.wal.tmp",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("artifact paths = %v, want %v", got, want)
	}
	for _, artifact := range inventory.Artifacts {
		if artifact.Path == "extra" && !artifact.IsDir {
			t.Errorf("artifact extra IsDir = false, want true")
		}
		if artifact.Path != "extra" && artifact.IsDir {
			t.Errorf("artifact %q IsDir = true, want false", artifact.Path)
		}
	}
}

func TestInitializeRefusesExistingArtifacts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "db")
	walDir := filepath.Join(root, WALDirName)
	if err := os.MkdirAll(walDir, 0o700); err != nil {
		t.Fatalf("mkdir wal: %v", err)
	}
	artifact := filepath.Join(walDir, "wal-00000000000000000001.wal")
	if err := os.WriteFile(artifact, []byte("existing"), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	fsys := newTestFS(t, root)

	_, err := Initialize(fsys, root)
	if !errors.Is(err, kverr.ErrRecoveryRequired) {
		t.Fatalf("Initialize() = %v, want ErrRecoveryRequired", err)
	}
	var existingErr *ExistingArtifactsError
	if !errors.As(err, &existingErr) {
		t.Fatalf("Initialize() error = %T, want *ExistingArtifactsError", err)
	}
	if len(existingErr.Artifacts) != 1 || existingErr.Artifacts[0].Path != "wal/wal-00000000000000000001.wal" {
		t.Errorf("artifacts = %+v, want wal/wal-00000000000000000001.wal", existingErr.Artifacts)
	}
	if contents, readErr := os.ReadFile(artifact); readErr != nil || string(contents) != "existing" {
		t.Errorf("artifact changed: contents=%q err=%v", contents, readErr)
	}
	for _, name := range []string{SSTablesDirName, ManifestDirName} {
		if _, statErr := os.Stat(filepath.Join(root, name)); !errors.Is(statErr, fs.ErrNotExist) {
			t.Errorf("%s directory was created despite existing artifacts: %v", name, statErr)
		}
	}
}

func TestInitializeRefusesNonDirectoryRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "db")
	if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write root file: %v", err)
	}
	fsys := newTestFS(t, root)

	_, err := Initialize(fsys, root)
	if !errors.Is(err, kverr.ErrInvalidArgument) {
		t.Fatalf("Initialize() = %v, want ErrInvalidArgument", err)
	}
	if contents, readErr := os.ReadFile(root); readErr != nil || string(contents) != "not a directory" {
		t.Errorf("root file changed: contents=%q err=%v", contents, readErr)
	}
}

func TestInitializeRequiresAbsolutePath(t *testing.T) {
	fsys := newTestFS(t, t.TempDir())
	_, err := Initialize(fsys, "relative/db")
	if !errors.Is(err, kverr.ErrInvalidArgument) {
		t.Fatalf("Initialize() = %v, want ErrInvalidArgument", err)
	}
}

func TestInspectMissingRootIsEmpty(t *testing.T) {
	parent := t.TempDir()
	fsys := newTestFS(t, parent)
	inventory, err := Inspect(fsys, filepath.Join(parent, "missing"))
	if err != nil {
		t.Fatalf("Inspect() = %v, want nil", err)
	}
	if !inventory.Empty() {
		t.Fatalf("Inspect() artifacts = %+v, want empty", inventory.Artifacts)
	}
}
