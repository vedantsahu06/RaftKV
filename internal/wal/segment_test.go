package wal

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/vedantsahu06/raftkv/internal/kverr"
	"github.com/vedantsahu06/raftkv/internal/types"
)

func TestCreateSegment(t *testing.T) {
	fsys, dir := newTestWALDir(t)

	segment, err := CreateSegment(fsys, dir, 42)
	if err != nil {
		t.Fatalf("CreateSegment() = %v", err)
	}
	t.Cleanup(func() {
		if err := segment.Seal(); err != nil {
			t.Errorf("Seal() during cleanup = %v", err)
		}
	})

	if segment.ID() != 42 {
		t.Errorf("segment ID = %d, want 42", segment.ID())
	}
	if segment.State() != SegmentActive {
		t.Errorf("segment state = %s, want active", segment.State())
	}

	finalName, err := SegmentFileName(42)
	if err != nil {
		t.Fatalf("SegmentFileName(42) = %v", err)
	}
	wantPath := filepath.Join(dir, finalName)
	if segment.Path() != wantPath {
		t.Errorf("segment path = %q, want %q", segment.Path(), wantPath)
	}

	info := requireExists(t, fsys, wantPath)
	if info.Size() != types.WALSegmentHeaderSize {
		t.Errorf("segment size = %d, want only the %d-byte header", info.Size(), types.WALSegmentHeaderSize)
	}

	finalContents := readTestFile(t, fsys, wantPath)
	if len(finalContents) != types.WALSegmentHeaderSize {
		t.Fatalf("published segment length = %d, want %d", len(finalContents), types.WALSegmentHeaderSize)
	}
	id, err := DecodeSegmentHeader(finalContents)
	if err != nil {
		t.Fatalf("DecodeSegmentHeader(published) = %v", err)
	}
	if id != 42 {
		t.Errorf("published header ID = %d, want 42", id)
	}
	nameID, err := ParseSegmentFileName(finalName)
	if err != nil {
		t.Fatalf("ParseSegmentFileName(%q) = %v", finalName, err)
	}
	if nameID != id {
		t.Errorf("filename ID = %d, header ID = %d; they must match", nameID, id)
	}

	tempName, err := SegmentTempFileName(42)
	if err != nil {
		t.Fatalf("SegmentTempFileName(42) = %v", err)
	}
	requireNotExist(t, fsys, filepath.Join(dir, tempName))

	if err := segment.Seal(); err != nil {
		t.Fatalf("Seal() = %v", err)
	}
	if segment.State() != SegmentSealed {
		t.Errorf("segment state after Seal() = %s, want sealed", segment.State())
	}
	if err := segment.Seal(); err != nil {
		t.Errorf("second Seal() = %v, want nil", err)
	}
	if got := readTestFile(t, fsys, wantPath); !bytes.Equal(got, finalContents) {
		t.Errorf("published segment changed after sealing")
	}
}

func TestCreateSegmentRejectsZeroIDBeforeIO(t *testing.T) {
	fsys, dir := newTestWALDir(t)
	faults := &faultFS{FS: fsys}

	segment, err := CreateSegment(faults, dir, 0)
	if !errors.Is(err, kverr.ErrInvalidArgument) {
		t.Fatalf("CreateSegment(0) = %v, want ErrInvalidArgument", err)
	}
	if segment != nil {
		t.Fatal("CreateSegment(0) returned a segment")
	}
	if len(faults.calls) != 0 {
		t.Errorf("CreateSegment(0) performed I/O calls: %v", faults.calls)
	}
}

func TestCreateSegmentDoesNotOverwriteExistingFinal(t *testing.T) {
	fsys, dir := newTestWALDir(t)
	finalName, err := SegmentFileName(1)
	if err != nil {
		t.Fatalf("SegmentFileName(1) = %v", err)
	}
	finalPath := filepath.Join(dir, finalName)
	original := []byte("existing final segment")
	writeTestFile(t, fsys, finalPath, original)

	segment, err := CreateSegment(fsys, dir, 1)
	if err == nil {
		segment.Seal()
		t.Fatal("CreateSegment() succeeded over an existing final segment")
	}
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("CreateSegment() = %v, want fs.ErrExist", err)
	}
	if segment != nil {
		t.Fatal("CreateSegment() returned a segment after a failed rename")
	}
	if got := readTestFile(t, fsys, finalPath); !bytes.Equal(got, original) {
		t.Errorf("existing final segment changed: %q", got)
	}

	tempName, err := SegmentTempFileName(1)
	if err != nil {
		t.Fatalf("SegmentTempFileName(1) = %v", err)
	}
	requireNotExist(t, fsys, filepath.Join(dir, tempName))
}

func TestCreateSegmentDoesNotOverwriteExistingTemporary(t *testing.T) {
	fsys, dir := newTestWALDir(t)
	tempName, err := SegmentTempFileName(1)
	if err != nil {
		t.Fatalf("SegmentTempFileName(1) = %v", err)
	}
	tempPath := filepath.Join(dir, tempName)
	original := []byte("existing temporary segment")
	writeTestFile(t, fsys, tempPath, original)

	segment, err := CreateSegment(fsys, dir, 1)
	if err == nil {
		segment.Seal()
		t.Fatal("CreateSegment() succeeded over an existing temporary segment")
	}
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("CreateSegment() = %v, want fs.ErrExist", err)
	}
	if got := readTestFile(t, fsys, tempPath); !bytes.Equal(got, original) {
		t.Errorf("existing temporary segment changed: %q", got)
	}

	finalName, err := SegmentFileName(1)
	if err != nil {
		t.Fatalf("SegmentFileName(1) = %v", err)
	}
	requireNotExist(t, fsys, filepath.Join(dir, finalName))
}

func TestCreateSegmentConcurrentSameID(t *testing.T) {
	const attempts = 16
	fsys, dir := newTestWALDir(t)

	type result struct {
		segment *Segment
		err     error
	}
	results := make(chan result, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			segment, err := CreateSegment(fsys, dir, 1)
			results <- result{segment: segment, err: err}
		}()
	}
	wg.Wait()
	close(results)

	var winners []*Segment
	failures := 0
	for result := range results {
		if result.err == nil {
			winners = append(winners, result.segment)
			continue
		}
		if result.segment != nil {
			t.Error("failed creation returned a non-nil segment")
		}
		failures++
	}
	if len(winners) != 1 {
		for _, segment := range winners {
			segment.Seal()
		}
		t.Fatalf("successful creations = %d, want 1", len(winners))
	}
	winner := winners[0]
	t.Cleanup(func() {
		if err := winner.Seal(); err != nil {
			t.Errorf("Seal() during cleanup = %v", err)
		}
	})
	if failures != attempts-1 {
		t.Errorf("failed creations = %d, want %d", failures, attempts-1)
	}
	if winner.State() != SegmentActive {
		t.Errorf("winner state = %s, want active", winner.State())
	}

	finalName, err := SegmentFileName(1)
	if err != nil {
		t.Fatalf("SegmentFileName(1) = %v", err)
	}
	finalContents := readTestFile(t, fsys, filepath.Join(dir, finalName))
	if _, err := DecodeSegmentHeader(finalContents); err != nil {
		t.Fatalf("DecodeSegmentHeader(winner) = %v", err)
	}

	tempName, err := SegmentTempFileName(1)
	if err != nil {
		t.Fatalf("SegmentTempFileName(1) = %v", err)
	}
	requireNotExist(t, fsys, filepath.Join(dir, tempName))
}

func TestCreateSegmentRequiresExistingDirectory(t *testing.T) {
	fsys, dir := newTestWALDir(t)

	missingDir := filepath.Join(dir, "missing")
	segment, err := CreateSegment(fsys, missingDir, 1)
	if err == nil {
		segment.Seal()
		t.Fatal("CreateSegment(missing directory) = nil error")
	}
	if segment != nil {
		segment.Seal()
		t.Fatal("CreateSegment(missing directory) returned a segment")
	}

	filePath := filepath.Join(dir, "not-a-directory")
	writeTestFile(t, fsys, filePath, []byte("file"))
	segment, err = CreateSegment(fsys, filePath, 1)
	if !errors.Is(err, kverr.ErrInvalidArgument) {
		t.Fatalf("CreateSegment(file path) = %v, want ErrInvalidArgument", err)
	}
	if segment != nil {
		t.Fatal("CreateSegment(file path) returned a segment")
	}
}

func TestCreateSegmentPublicationOrder(t *testing.T) {
	fsys, dir := newTestWALDir(t)
	faults := &faultFS{FS: fsys}

	segment, err := CreateSegment(faults, dir, 7)
	if err != nil {
		t.Fatalf("CreateSegment() = %v", err)
	}
	if err := segment.Seal(); err != nil {
		t.Fatalf("Seal() = %v", err)
	}

	want := []string{
		"stat",
		"create",
		"file.write",
		"file.sync",
		"file.close",
		"rename",
		"syncdir",
		"openfile",
		"file.seek",
		"file.close",
	}
	if !slices.Equal(faults.calls, want) {
		t.Fatalf("creation call order = %v, want %v", faults.calls, want)
	}
}

func TestCreateSegmentFailureStages(t *testing.T) {
	injected := errors.New("injected I/O failure")

	tests := []struct {
		name        string
		arrange     func(*faultFS)
		wantErr     error
		finalExists bool
	}{
		{
			name:    "create temporary file",
			arrange: func(f *faultFS) { f.createErr = injected },
			wantErr: injected,
		},
		{
			name:    "write segment header",
			arrange: func(f *faultFS) { f.writeErr = injected },
			wantErr: injected,
		},
		{
			name:    "short header write",
			arrange: func(f *faultFS) { f.shortWrite = true },
			wantErr: io.ErrShortWrite,
		},
		{
			name:    "sync segment header",
			arrange: func(f *faultFS) { f.syncErr = injected },
			wantErr: injected,
		},
		{
			name:    "close before rename",
			arrange: func(f *faultFS) { f.closeErr = injected },
			wantErr: injected,
		},
		{
			name:    "rename final segment",
			arrange: func(f *faultFS) { f.renameErr = injected },
			wantErr: injected,
		},
		{
			name:        "make directory entry durable",
			arrange:     func(f *faultFS) { f.syncDirErr = injected },
			wantErr:     injected,
			finalExists: true,
		},
		{
			name:        "open published segment",
			arrange:     func(f *faultFS) { f.openErr = injected },
			wantErr:     injected,
			finalExists: true,
		},
		{
			name:        "seek to append offset",
			arrange:     func(f *faultFS) { f.seekErr = injected },
			wantErr:     injected,
			finalExists: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys, dir := newTestWALDir(t)
			faults := &faultFS{FS: fsys}
			tt.arrange(faults)

			segment, err := CreateSegment(faults, dir, 7)
			if segment != nil {
				segment.Seal()
				t.Fatal("failed creation returned a segment")
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("CreateSegment() = %v, want error matching %v", err, tt.wantErr)
			}

			finalName, nameErr := SegmentFileName(7)
			if nameErr != nil {
				t.Fatalf("SegmentFileName(7) = %v", nameErr)
			}
			finalPath := filepath.Join(dir, finalName)
			if tt.finalExists {
				contents := readTestFile(t, fsys, finalPath)
				id, decodeErr := DecodeSegmentHeader(contents)
				if decodeErr != nil {
					t.Fatalf("DecodeSegmentHeader(surviving final) = %v", decodeErr)
				}
				if id != 7 {
					t.Errorf("surviving final header ID = %d, want 7", id)
				}
			} else {
				requireNotExist(t, fsys, finalPath)
			}

			tempName, nameErr := SegmentTempFileName(7)
			if nameErr != nil {
				t.Fatalf("SegmentTempFileName(7) = %v", nameErr)
			}
			requireNotExist(t, fsys, filepath.Join(dir, tempName))
		})
	}
}

func TestCreateSegmentReportsCleanupFailure(t *testing.T) {
	fsys, dir := newTestWALDir(t)
	writeErr := errors.New("injected header write failure")
	removeErr := errors.New("injected temporary cleanup failure")
	faults := &faultFS{
		FS:        fsys,
		writeErr:  writeErr,
		removeErr: removeErr,
	}

	segment, err := CreateSegment(faults, dir, 1)
	if segment != nil {
		segment.Seal()
		t.Fatal("failed creation returned a segment")
	}
	if !errors.Is(err, writeErr) {
		t.Errorf("CreateSegment() error = %v, want write failure", err)
	}
	if !errors.Is(err, removeErr) {
		t.Errorf("CreateSegment() error = %v, want cleanup failure", err)
	}

	finalName, nameErr := SegmentFileName(1)
	if nameErr != nil {
		t.Fatalf("SegmentFileName(1) = %v", nameErr)
	}
	requireNotExist(t, fsys, filepath.Join(dir, finalName))

	tempName, nameErr := SegmentTempFileName(1)
	if nameErr != nil {
		t.Fatalf("SegmentTempFileName(1) = %v", nameErr)
	}
	requireExists(t, fsys, filepath.Join(dir, tempName))
}
