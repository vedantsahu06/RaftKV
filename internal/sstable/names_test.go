package sstable

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

type scanFileInfo struct {
	name string
	dir  bool
}

func (i scanFileInfo) Name() string { return i.name }
func (i scanFileInfo) Size() int64  { return 0 }
func (i scanFileInfo) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir
	}
	return 0
}
func (i scanFileInfo) ModTime() time.Time { return time.Time{} }
func (i scanFileInfo) IsDir() bool        { return i.dir }
func (i scanFileInfo) Sys() any           { return nil }

type readDirFS struct {
	filesystem.FS
	entries []fs.DirEntry
	err     error
}

func (f readDirFS) ReadDir(string) ([]fs.DirEntry, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.entries, nil
}

func scanEntries(specs []scanFileInfo) []fs.DirEntry {
	entries := make([]fs.DirEntry, len(specs))
	for i, spec := range specs {
		entries[i] = fs.FileInfoToDirEntry(spec)
	}
	return entries
}

func TestSSTableFileNames(t *testing.T) {
	tests := []struct {
		name     string
		id       uint64
		want     string
		tempWant string
		wantErr  bool
	}{
		{name: "minimum", id: 1, want: "sstable-000001.sst", tempWant: "sstable-000001.sst.tmp"},
		{name: "design example", id: 17, want: "sstable-000017.sst", tempWant: "sstable-000017.sst.tmp"},
		{name: "maximum", id: 999_999, want: "sstable-999999.sst", tempWant: "sstable-999999.sst.tmp"},
		{name: "zero", id: 0, wantErr: true},
		{name: "above maximum", id: 1_000_000, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FileName(types.SSTableID(tt.id))
			if tt.wantErr {
				if !errors.Is(err, kverr.ErrInvalidArgument) {
					t.Fatalf("FileName(%d) = %v, want ErrInvalidArgument", tt.id, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("FileName(%d): %v", tt.id, err)
			}
			if got != tt.want {
				t.Fatalf("FileName(%d) = %q, want %q", tt.id, got, tt.want)
			}

			temp, err := TempFileName(types.SSTableID(tt.id))
			if err != nil {
				t.Fatalf("TempFileName(%d): %v", tt.id, err)
			}
			if temp != tt.tempWant {
				t.Fatalf("TempFileName(%d) = %q, want %q", tt.id, temp, tt.tempWant)
			}
		})
	}
}

func TestParseSSTableFileNames(t *testing.T) {
	finalTests := []struct {
		name string
		want uint64
		ok   bool
	}{
		{name: "sstable-000001.sst", want: 1, ok: true},
		{name: "sstable-999999.sst", want: 999_999, ok: true},
		{name: "", ok: false},
		{name: "wal-000001.sst", ok: false},
		{name: "sstable-00001.sst", ok: false},
		{name: "sstable-0000001.sst", ok: false},
		{name: "sstable-00a001.sst", ok: false},
		{name: "sstable-000000.sst", ok: false},
		{name: "sstable-1000000.sst", ok: false},
		{name: "sstable-000001.sst.tmp", ok: false},
		{name: "SSTABLE-000001.sst", ok: false},
		{name: "sstable-000001.SST", ok: false},
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
		{name: "sstable-000001.sst.tmp", want: 1, ok: true},
		{name: "sstable-999999.sst.tmp", want: 999_999, ok: true},
		{name: "sstable-000001.sst", ok: false},
		{name: "sstable-000001.sst.tmp.tmp", ok: false},
		{name: "sstable-000000.sst.tmp", ok: false},
		{name: "sstable-00001.sst.tmp", ok: false},
		{name: "sstable-00a001.sst.tmp", ok: false},
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

func TestScanIDs(t *testing.T) {
	entries := scanEntries([]scanFileInfo{
		{name: "sstable-000003.sst"},
		{name: "sstable-000001.sst.tmp"},
		{name: "sstable-000003.sst.tmp"},
		{name: "sstable-999999.sst"},
		{name: "sstable-000002.sst.tmp.tmp"},
		{name: "sstable-000000.sst"},
		{name: "sstable-12345.sst"},
		{name: "sstable-000007.sst", dir: true},
		{name: "unrelated.txt"},
		{name: "unrelated-directory", dir: true},
		{name: "wal-000001.wal"},
	})
	fsys := readDirFS{entries: entries}

	scan, err := ScanIDs(fsys, "sst")
	if err != nil {
		t.Fatalf("ScanIDs(): %v", err)
	}

	wantIDs := []types.SSTableID{1, 3, 999_999}
	if !reflect.DeepEqual(scan.IDs, wantIDs) {
		t.Fatalf("IDs = %v, want %v", scan.IDs, wantIDs)
	}
	if scan.Highest != 999_999 {
		t.Fatalf("Highest = %d, want 999999", scan.Highest)
	}
	wantMalformed := []string{
		"sstable-000000.sst",
		"sstable-000002.sst.tmp.tmp",
		"sstable-000007.sst",
		"sstable-12345.sst",
	}
	if !reflect.DeepEqual(scan.Malformed, wantMalformed) {
		t.Fatalf("Malformed = %v, want %v", scan.Malformed, wantMalformed)
	}
}

func TestScanIDsEmptyDirectory(t *testing.T) {
	scan, err := ScanIDs(readDirFS{}, "sst")
	if err != nil {
		t.Fatalf("ScanIDs(): %v", err)
	}
	if len(scan.IDs) != 0 || scan.Highest != 0 || len(scan.Malformed) != 0 {
		t.Fatalf("ScanIDs() = %+v, want an empty scan", scan)
	}
}

func TestScanIDsReadDirError(t *testing.T) {
	injected := errors.New("injected read failure")
	_, err := ScanIDs(readDirFS{err: injected}, "sst")
	if !errors.Is(err, injected) {
		t.Fatalf("ScanIDs() = %v, want injected error", err)
	}
}

func TestNextID(t *testing.T) {
	tests := []struct {
		name         string
		manifestNext types.SSTableID
		highest      types.SSTableID
		want         types.SSTableID
	}{
		{name: "manifest ahead", manifestNext: 5, highest: 2, want: 5},
		{name: "observed ahead", manifestNext: 5, highest: 7, want: 8},
		{name: "fresh database", manifestNext: 1, highest: 0, want: 1},
		{name: "manifest is maximum", manifestNext: 999_999, highest: 999_998, want: 999_999},
		{name: "observed orphan reaches maximum", manifestNext: 1, highest: 999_999, want: 0},
		{name: "manifest already exhausted", manifestNext: 1_000_000, highest: 0, want: 0},
		{name: "manifest maximum uint64", manifestNext: ^types.SSTableID(0), highest: 0, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NextID(tt.manifestNext, IDScan{Highest: tt.highest})
			if tt.want == 0 {
				if !errors.Is(err, ErrIDExhausted) {
					t.Fatalf("NextID(%d, highest %d) = %v, want ErrIDExhausted", tt.manifestNext, tt.highest, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("NextID(%d, highest %d): %v", tt.manifestNext, tt.highest, err)
			}
			if got != tt.want {
				t.Fatalf("NextID(%d, highest %d) = %d, want %d", tt.manifestNext, tt.highest, got, tt.want)
			}
		})
	}
}

func TestNextIDRejectsInvalidInputs(t *testing.T) {
	if _, err := NextID(0, IDScan{}); !errors.Is(err, kverr.ErrInvalidArgument) {
		t.Fatalf("NextID(0) = %v, want ErrInvalidArgument", err)
	}
	if _, err := NextID(1, IDScan{Highest: MaxID + 1}); !errors.Is(err, kverr.ErrInvalidArgument) {
		t.Fatalf("NextID with invalid scan = %v, want ErrInvalidArgument", err)
	}
}
