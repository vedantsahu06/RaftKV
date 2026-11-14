package wal

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/vedantsahu06/raftkv/internal/kverr"
	"github.com/vedantsahu06/raftkv/internal/types"
)

func TestSegmentFileNames(t *testing.T) {
	tests := []struct {
		id        types.SegmentID
		wantFinal string
		wantTemp  string
	}{
		{
			id:        1,
			wantFinal: "wal-00000000000000000001.wal",
			wantTemp:  "wal-00000000000000000001.wal.tmp",
		},
		{
			id:        42,
			wantFinal: "wal-00000000000000000042.wal",
			wantTemp:  "wal-00000000000000000042.wal.tmp",
		},
		{
			id:        maxSegmentID,
			wantFinal: "wal-18446744073709551615.wal",
			wantTemp:  "wal-18446744073709551615.wal.tmp",
		},
	}

	for _, tt := range tests {
		final, err := SegmentFileName(tt.id)
		if err != nil {
			t.Fatalf("SegmentFileName(%d) = %v", tt.id, err)
		}
		if final != tt.wantFinal {
			t.Errorf("SegmentFileName(%d) = %q, want %q", tt.id, final, tt.wantFinal)
		}

		temp, err := SegmentTempFileName(tt.id)
		if err != nil {
			t.Fatalf("SegmentTempFileName(%d) = %v", tt.id, err)
		}
		if temp != tt.wantTemp {
			t.Errorf("SegmentTempFileName(%d) = %q, want %q", tt.id, temp, tt.wantTemp)
		}
	}
}

func TestSegmentFileNameRejectsZeroID(t *testing.T) {
	if _, err := SegmentFileName(0); !errors.Is(err, kverr.ErrInvalidArgument) {
		t.Errorf("SegmentFileName(0) = %v, want ErrInvalidArgument", err)
	}
	if _, err := SegmentTempFileName(0); !errors.Is(err, kverr.ErrInvalidArgument) {
		t.Errorf("SegmentTempFileName(0) = %v, want ErrInvalidArgument", err)
	}
}

func TestParseSegmentFileNames(t *testing.T) {
	tests := []struct {
		name  string
		parse func(string) (types.SegmentID, error)
		input string
		want  types.SegmentID
	}{
		{
			name:  "final one",
			parse: ParseSegmentFileName,
			input: "wal-00000000000000000001.wal",
			want:  1,
		},
		{
			name:  "final maximum",
			parse: ParseSegmentFileName,
			input: "wal-18446744073709551615.wal",
			want:  maxSegmentID,
		},
		{
			name:  "temporary one",
			parse: ParseSegmentTempFileName,
			input: "wal-00000000000000000001.wal.tmp",
			want:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.parse(tt.input)
			if err != nil {
				t.Fatalf("parse(%q) = %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("parse(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseSegmentFileNamesRejectsMalformed(t *testing.T) {
	invalid := []string{
		"",
		"wal-.wal",
		"wal-1.wal",
		"wal-0000000000000000001.wal",
		"wal-000000000000000000001.wal",
		"wal-0000000000000000000a.wal",
		"wal-00000000000000000000.wal",
		"wal-99999999999999999999.wal",
		"wal-00000000000000000001.wal ",
		"wal-00000000000000000001.WAL",
		"xwal-00000000000000000001.wal",
		"wal-00000000000000000001",
		"wal-00000000000000000001.wal.tmp",
	}

	for _, name := range invalid {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseSegmentFileName(name); !errors.Is(err, ErrInvalidSegmentName) {
				t.Errorf("ParseSegmentFileName(%q) = %v, want ErrInvalidSegmentName", name, err)
			}
		})
	}

	if _, err := ParseSegmentTempFileName("wal-00000000000000000001.wal.tmp.bak"); !errors.Is(err, ErrInvalidSegmentName) {
		t.Errorf("ParseSegmentTempFileName(extra suffix) = %v, want ErrInvalidSegmentName", err)
	}
	if _, err := ParseSegmentTempFileName("wal-00000000000000000001.wal"); !errors.Is(err, ErrInvalidSegmentName) {
		t.Errorf("ParseSegmentTempFileName(final name) = %v, want ErrInvalidSegmentName", err)
	}
}

func TestNextSegmentID(t *testing.T) {
	t.Run("empty directory starts at one", func(t *testing.T) {
		fsys, dir := newTestWALDir(t)
		id, err := NextSegmentID(fsys, dir)
		if err != nil {
			t.Fatalf("NextSegmentID() = %v", err)
		}
		if id != 1 {
			t.Fatalf("NextSegmentID() = %d, want 1", id)
		}
	})

	t.Run("consumes final and temporary IDs", func(t *testing.T) {
		fsys, dir := newTestWALDir(t)
		writeTestFile(t, fsys, filepath.Join(dir, "wal-00000000000000000001.wal"), nil)
		writeTestFile(t, fsys, filepath.Join(dir, "wal-00000000000000000003.wal.tmp"), nil)
		writeTestFile(t, fsys, filepath.Join(dir, "wal-00000000000000000002.wal"), nil)

		id, err := NextSegmentID(fsys, dir)
		if err != nil {
			t.Fatalf("NextSegmentID() = %v", err)
		}
		if id != 4 {
			t.Fatalf("NextSegmentID() = %d, want 4", id)
		}
	})

	t.Run("ignores malformed and unrelated names", func(t *testing.T) {
		fsys, dir := newTestWALDir(t)
		writeTestFile(t, fsys, filepath.Join(dir, "wal-00000000000000000009.wal.bak"), nil)
		writeTestFile(t, fsys, filepath.Join(dir, "wal-9.wal"), nil)
		writeTestFile(t, fsys, filepath.Join(dir, "other.txt"), nil)

		id, err := NextSegmentID(fsys, dir)
		if err != nil {
			t.Fatalf("NextSegmentID() = %v", err)
		}
		if id != 1 {
			t.Fatalf("NextSegmentID() = %d, want 1", id)
		}
	})

	t.Run("exhausted ID space", func(t *testing.T) {
		fsys, dir := newTestWALDir(t)
		writeTestFile(t, fsys, filepath.Join(dir, "wal-18446744073709551615.wal"), nil)

		_, err := NextSegmentID(fsys, dir)
		if !errors.Is(err, ErrSegmentIDExhausted) {
			t.Fatalf("NextSegmentID() = %v, want ErrSegmentIDExhausted", err)
		}
	})

	t.Run("missing directory", func(t *testing.T) {
		fsys, dir := newTestWALDir(t)
		if _, err := NextSegmentID(fsys, filepath.Join(dir, "missing")); err == nil {
			t.Fatal("NextSegmentID(missing directory) = nil, want an error")
		}
	})
}
