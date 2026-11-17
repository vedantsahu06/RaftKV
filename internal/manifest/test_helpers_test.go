package manifest

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
	"github.com/vedantsahu06/raftkv/internal/sstable"
	"github.com/vedantsahu06/raftkv/internal/types"
)

// newTestManifestDir creates a fresh database layout through the platform
// filesystem and returns the Manifest directory path.
func newTestManifestDir(t *testing.T) (filesystem.FS, string) {
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
	return fsys, layout.Manifest
}

func emptyManifest(version types.ManifestVersion) Manifest {
	return Manifest{
		FormatVersion: FormatVersion,
		Version:       version,
		NextSSTableID: 1,
	}
}

func emptyPublishState() Manifest {
	return Manifest{
		FormatVersion: FormatVersion,
		NextSSTableID: 1,
	}
}

func testMetadata(t *testing.T, id types.SSTableID, minSequence, maxSequence types.Sequence, source types.SegmentID) sstable.Metadata {
	t.Helper()

	filename, err := sstable.FileName(id)
	if err != nil {
		t.Fatalf("sstable.FileName(%d): %v", id, err)
	}
	return sstable.Metadata{
		ID:                 id,
		Filename:           filename,
		Level:              sstable.LevelZero,
		FileSize:           1_000 + uint64(id),
		SmallestKey:        []byte("a"),
		LargestKey:         []byte("z"),
		MinSequence:        minSequence,
		MaxSequence:        maxSequence,
		Checksum:           uint32(id) * 2_654_435_761,
		SourceWALSegmentID: source,
	}
}

func writeVersionFile(t *testing.T, fsys filesystem.FS, dir string, m Manifest) {
	t.Helper()

	data, err := Encode(m)
	if err != nil {
		t.Fatalf("Encode(version %d): %v", m.Version, err)
	}
	name, err := FileName(m.Version)
	if err != nil {
		t.Fatalf("FileName(%d): %v", m.Version, err)
	}
	writeTestFile(t, fsys, filepath.Join(dir, name), data)
}

func writeRawVersionFile(t *testing.T, fsys filesystem.FS, dir string, version types.ManifestVersion, data []byte) {
	t.Helper()

	name, err := FileName(version)
	if err != nil {
		t.Fatalf("FileName(%d): %v", version, err)
	}
	writeTestFile(t, fsys, filepath.Join(dir, name), data)
}

func writeTempVersionFile(t *testing.T, fsys filesystem.FS, dir string, version types.ManifestVersion, data []byte) {
	t.Helper()

	name, err := TempFileName(version)
	if err != nil {
		t.Fatalf("TempFileName(%d): %v", version, err)
	}
	writeTestFile(t, fsys, filepath.Join(dir, name), data)
}

func manifestPath(t *testing.T, dir string, version types.ManifestVersion) string {
	t.Helper()

	name, err := FileName(version)
	if err != nil {
		t.Fatalf("FileName(%d): %v", version, err)
	}
	return filepath.Join(dir, name)
}

func tempManifestPath(t *testing.T, dir string, version types.ManifestVersion) string {
	t.Helper()

	name, err := TempFileName(version)
	if err != nil {
		t.Fatalf("TempFileName(%d): %v", version, err)
	}
	return filepath.Join(dir, name)
}

func acceptAllManifests(Manifest) error {
	return nil
}

func rejectManifestVersion(rejected types.ManifestVersion) StateValidator {
	return func(m Manifest) error {
		if m.Version == rejected {
			return errors.New("injected state validation failure")
		}
		return nil
	}
}

func readTestFile(t *testing.T, fsys filesystem.FS, path string) []byte {
	t.Helper()

	file, err := fsys.Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	contents, readErr := io.ReadAll(file)
	closeErr := file.Close()
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

	file, err := fsys.CreateExclusive(path)
	if err != nil {
		t.Fatalf("CreateExclusive(%q): %v", path, err)
	}
	written, writeErr := file.Write(contents)
	if writeErr != nil {
		file.Close()
		t.Fatalf("Write(%q): %v", path, writeErr)
	}
	if written != len(contents) {
		file.Close()
		t.Fatalf("Write(%q) wrote %d bytes, want %d", path, written, len(contents))
	}
	if err := file.Sync(); err != nil {
		file.Close()
		t.Fatalf("Sync(%q): %v", path, err)
	}
	if err := file.Close(); err != nil {
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

func pathExists(t *testing.T, fsys filesystem.FS, path string) bool {
	t.Helper()

	_, err := fsys.Stat(path)
	if err == nil {
		return true
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	t.Fatalf("Stat(%q): %v", path, err)
	return false
}
