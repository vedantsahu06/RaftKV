package manifest

import (
	"errors"
	"io/fs"
	"reflect"
	"testing"
	"time"

	"github.com/vedantsahu06/raftkv/internal/filesystem"
	"github.com/vedantsahu06/raftkv/internal/kverr"
	"github.com/vedantsahu06/raftkv/internal/types"
)

func TestManifestFileNames(t *testing.T) {
	tests := []struct {
		name     string
		version  types.ManifestVersion
		want     string
		tempWant string
	}{
		{
			name:     "one",
			version:  1,
			want:     "MANIFEST-00000000000000000001",
			tempWant: "MANIFEST-00000000000000000001.tmp",
		},
		{
			name:     "large",
			version:  42,
			want:     "MANIFEST-00000000000000000042",
			tempWant: "MANIFEST-00000000000000000042.tmp",
		},
		{
			name:     "maximum uint64",
			version:  ^types.ManifestVersion(0),
			want:     "MANIFEST-18446744073709551615",
			tempWant: "MANIFEST-18446744073709551615.tmp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FileName(tt.version)
			if err != nil {
				t.Fatalf("FileName(%d): %v", tt.version, err)
			}
			if got != tt.want {
				t.Fatalf("FileName(%d) = %q, want %q", tt.version, got, tt.want)
			}

			temp, err := TempFileName(tt.version)
			if err != nil {
				t.Fatalf("TempFileName(%d): %v", tt.version, err)
			}
			if temp != tt.tempWant {
				t.Fatalf("TempFileName(%d) = %q, want %q", tt.version, temp, tt.tempWant)
			}
		})
	}

	if _, err := FileName(0); !errors.Is(err, kverr.ErrInvalidArgument) {
		t.Fatalf("FileName(0) = %v, want ErrInvalidArgument", err)
	}
	if _, err := TempFileName(0); !errors.Is(err, kverr.ErrInvalidArgument) {
		t.Fatalf("TempFileName(0) = %v, want ErrInvalidArgument", err)
	}
}

func TestParseManifestFileNames(t *testing.T) {
	finalTests := []struct {
		name string
		want uint64
		ok   bool
	}{
		{name: "MANIFEST-00000000000000000001", want: 1, ok: true},
		{name: "MANIFEST-00000000000000000042", want: 42, ok: true},
		{name: "MANIFEST-18446744073709551615", want: ^uint64(0), ok: true},
		{name: "", ok: false},
		{name: "CURRENT", ok: false},
		{name: "MANIFEST-1", ok: false},
		{name: "MANIFEST-0000000000000000001", ok: false},
		{name: "MANIFEST-000000000000000000001", ok: false},
		{name: "MANIFEST-0000000000000000000a", ok: false},
		{name: "MANIFEST-00000000000000000000", ok: false},
		{name: "manifest-00000000000000000001", ok: false},
		{name: "MANIFEST-00000000000000000001.tmp", ok: false},
	}

	for _, tt := range finalTests {
		t.Run("final/"+tt.name, func(t *testing.T) {
			got, err := ParseFileName(tt.name)
			if !tt.ok {
				if !errors.Is(err, ErrInvalidName) {
					t.Fatalf("ParseFileName(%q) = %v, want ErrInvalidName", tt.name, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseFileName(%q): %v", tt.name, err)
			}
			if uint64(got) != tt.want {
				t.Fatalf("ParseFileName(%q) = %d, want %d", tt.name, got, tt.want)
			}
		})
	}

	tempTests := []struct {
		name string
		want uint64
		ok   bool
	}{
		{name: "MANIFEST-00000000000000000001.tmp", want: 1, ok: true},
		{name: "MANIFEST-18446744073709551615.tmp", want: ^uint64(0), ok: true},
		{name: "MANIFEST-00000000000000000001", ok: false},
		{name: "MANIFEST-00000000000000000001.tmp.tmp", ok: false},
		{name: "MANIFEST-00000000000000000000.tmp", ok: false},
	}

	for _, tt := range tempTests {
		t.Run("temp/"+tt.name, func(t *testing.T) {
			got, err := ParseTempFileName(tt.name)
			if !tt.ok {
				if !errors.Is(err, ErrInvalidName) {
					t.Fatalf("ParseTempFileName(%q) = %v, want ErrInvalidName", tt.name, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseTempFileName(%q): %v", tt.name, err)
			}
			if uint64(got) != tt.want {
				t.Fatalf("ParseTempFileName(%q) = %d, want %d", tt.name, got, tt.want)
			}
		})
	}
}

type versionScanFileInfo struct {
	name string
	dir  bool
}

func (i versionScanFileInfo) Name() string { return i.name }
func (i versionScanFileInfo) Size() int64  { return 0 }
func (i versionScanFileInfo) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir
	}
	return 0
}
func (i versionScanFileInfo) ModTime() time.Time { return time.Time{} }
func (i versionScanFileInfo) IsDir() bool        { return i.dir }
func (i versionScanFileInfo) Sys() any           { return nil }

type versionScanFS struct {
	filesystem.FS
	entries []fs.DirEntry
	err     error
}

func (f versionScanFS) ReadDir(string) ([]fs.DirEntry, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.entries, nil
}

func versionScanEntries(specs []versionScanFileInfo) []fs.DirEntry {
	entries := make([]fs.DirEntry, len(specs))
	for i, spec := range specs {
		entries[i] = fs.FileInfoToDirEntry(spec)
	}
	return entries
}

func TestScanVersions(t *testing.T) {
	entries := versionScanEntries([]versionScanFileInfo{
		{name: "MANIFEST-00000000000000000003"},
		{name: "MANIFEST-00000000000000000001"},
		{name: "MANIFEST-00000000000000000005.tmp"},
		{name: "MANIFEST-00000000000000000002.tmp"},
		{name: "MANIFEST-00000000000000000000"},
		{name: "MANIFEST-1"},
		{name: "MANIFEST-00000000000000000007.tmp.tmp"},
		{name: "MANIFEST-00000000000000000009", dir: true},
		{name: "CURRENT"},
		{name: "unrelated.txt"},
	})
	fsys := versionScanFS{entries: entries}

	scan, err := ScanVersions(fsys, "manifest")
	if err != nil {
		t.Fatalf("ScanVersions(): %v", err)
	}

	wantFinal := []types.ManifestVersion{1, 3}
	if !reflect.DeepEqual(scan.Final, wantFinal) {
		t.Fatalf("Final = %v, want %v", scan.Final, wantFinal)
	}
	wantTemp := []types.ManifestVersion{2, 5}
	if !reflect.DeepEqual(scan.Temp, wantTemp) {
		t.Fatalf("Temp = %v, want %v", scan.Temp, wantTemp)
	}
	wantMalformed := []string{
		"MANIFEST-00000000000000000000",
		"MANIFEST-00000000000000000007.tmp.tmp",
		"MANIFEST-00000000000000000009",
		"MANIFEST-1",
	}
	if !reflect.DeepEqual(scan.Malformed, wantMalformed) {
		t.Fatalf("Malformed = %v, want %v", scan.Malformed, wantMalformed)
	}
}

func TestScanVersionsEmptyDirectory(t *testing.T) {
	scan, err := ScanVersions(versionScanFS{}, "manifest")
	if err != nil {
		t.Fatalf("ScanVersions(): %v", err)
	}
	if len(scan.Final) != 0 || len(scan.Temp) != 0 || len(scan.Malformed) != 0 {
		t.Fatalf("ScanVersions() = %+v, want an empty scan", scan)
	}
}

func TestScanVersionsReadDirError(t *testing.T) {
	injected := errors.New("injected read failure")
	_, err := ScanVersions(versionScanFS{err: injected}, "manifest")
	if !errors.Is(err, injected) {
		t.Fatalf("ScanVersions() = %v, want injected error", err)
	}
}

func TestNextVersion(t *testing.T) {
	tests := []struct {
		name string
		scan VersionScan
		want types.ManifestVersion
	}{
		{name: "empty", scan: VersionScan{}, want: 1},
		{name: "final only", scan: VersionScan{Final: []types.ManifestVersion{1, 3}}, want: 4},
		{name: "temp only", scan: VersionScan{Temp: []types.ManifestVersion{2, 5}}, want: 6},
		{name: "temp ahead of final", scan: VersionScan{Final: []types.ManifestVersion{1}, Temp: []types.ManifestVersion{9}}, want: 10},
		{
			name: "maximum minus one",
			scan: VersionScan{Final: []types.ManifestVersion{^types.ManifestVersion(0) - 1}},
			want: ^types.ManifestVersion(0),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NextVersion(tt.scan)
			if err != nil {
				t.Fatalf("NextVersion(): %v", err)
			}
			if got != tt.want {
				t.Fatalf("NextVersion() = %d, want %d", got, tt.want)
			}
		})
	}

	_, err := NextVersion(VersionScan{Temp: []types.ManifestVersion{^types.ManifestVersion(0)}})
	if !errors.Is(err, ErrVersionExhausted) {
		t.Fatalf("NextVersion(max) = %v, want ErrVersionExhausted", err)
	}
}
