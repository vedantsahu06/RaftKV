package wal

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/vedantsahu06/raftkv/internal/filesystem"
	"github.com/vedantsahu06/raftkv/internal/kverr"
	"github.com/vedantsahu06/raftkv/internal/types"
)

// SegmentState is the lifecycle state of an established WAL segment.
//
// An unestablished segment is represented by not returning a Segment at all.
// CreateSegment returns a Segment only after the complete publication protocol
// succeeds, so there is no object through which a caller could write to a
// temporary or not-yet-durable file.
type SegmentState uint8

const (
	// SegmentActive is the one segment allowed to accept new records.
	SegmentActive SegmentState = iota + 1

	// SegmentSealed accepts no further records. Sealing does not authorize
	// deletion.
	SegmentSealed
)

// String returns the segment state name.
func (s SegmentState) String() string {
	switch s {
	case SegmentActive:
		return "active"
	case SegmentSealed:
		return "sealed"
	default:
		return fmt.Sprintf("SegmentState(%d)", uint8(s))
	}
}

// Segment is an established WAL file owned by the dedicated WAL writer.
//
// The returned file handle is positioned immediately after the segment header.
// This milestone does not expose an append operation; group commit and record
// appends are implemented by the next milestone. Only the writer goroutine may
// use the segment, while the mutex makes state observation and sealing safe.
type Segment struct {
	id   types.SegmentID
	path string
	file filesystem.File

	mu    sync.Mutex
	state SegmentState
}

// CreateSegment creates and durably publishes a new empty WAL segment.
//
// The protocol is strict:
//
//  1. create an exclusively named temporary file;
//  2. write the complete segment header;
//  3. Sync the temporary file;
//  4. rename it without replacing an existing final segment;
//  5. make the WAL directory entry durable;
//  6. open the published file for the writer and return an active Segment.
//
// If any step before the rename fails, the temporary file is removed on a best-
// effort basis. If the rename succeeds but directory durability fails, the final
// file is left in place but no Segment is returned and no write is accepted.
// The final file is never removed by this function: a later attempt must use a
// new segment ID rather than overwriting a possibly published name.
func CreateSegment(fsys filesystem.FS, dir string, id types.SegmentID) (*Segment, error) {
	if dir == "" {
		return nil, fmt.Errorf("%w: WAL directory must not be empty", kverr.ErrInvalidArgument)
	}
	dir = filepath.Clean(dir)

	finalName, err := SegmentFileName(id)
	if err != nil {
		return nil, err
	}
	tempName, err := SegmentTempFileName(id)
	if err != nil {
		return nil, err
	}
	finalPath := filepath.Join(dir, finalName)
	tempPath := filepath.Join(dir, tempName)

	info, err := fsys.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("stat WAL directory %q: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: WAL path %q is not a directory", kverr.ErrInvalidArgument, dir)
	}

	header, err := EncodeSegmentHeader(id)
	if err != nil {
		return nil, err
	}

	tempFile, err := fsys.CreateExclusive(tempPath)
	if err != nil {
		return nil, fmt.Errorf("create temporary WAL segment %q: %w", tempPath, err)
	}

	closeAttempted := false
	closeTemp := func() error {
		if closeAttempted {
			return nil
		}
		closeAttempted = true
		return tempFile.Close()
	}
	cleanupTemp := func(cause error) error {
		problems := []error{cause}
		if closeErr := closeTemp(); closeErr != nil {
			problems = append(problems, fmt.Errorf("close temporary WAL segment %q: %w", tempPath, closeErr))
		}
		if removeErr := fsys.Remove(tempPath); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
			problems = append(problems, fmt.Errorf("remove temporary WAL segment %q: %w", tempPath, removeErr))
		}
		return errors.Join(problems...)
	}

	written, writeErr := tempFile.Write(header)
	if writeErr != nil {
		return nil, cleanupTemp(fmt.Errorf("write segment header to %q: %w", tempPath, writeErr))
	}
	if written != len(header) {
		return nil, cleanupTemp(fmt.Errorf("write segment header to %q: %w", tempPath, io.ErrShortWrite))
	}
	if err := tempFile.Sync(); err != nil {
		return nil, cleanupTemp(fmt.Errorf("sync temporary WAL segment %q: %w", tempPath, err))
	}
	// The Windows backend opens files without delete sharing. Close the handle
	// before the rename so the namespace operation cannot fail because of it.
	if err := closeTemp(); err != nil {
		return nil, cleanupTemp(fmt.Errorf("close temporary WAL segment %q before rename: %w", tempPath, err))
	}

	if err := fsys.RenameNoReplace(tempPath, finalPath); err != nil {
		return nil, cleanupTemp(fmt.Errorf("publish WAL segment %q as %q: %w", tempPath, finalPath, err))
	}

	// A successful file Sync does not make a new directory entry durable. The
	// rename is not established until this platform operation succeeds, so no
	// Segment may be returned before it.
	//
	// From this point onward the final filename exists. Do not delete it on a
	// later failure: recovery can observe a valid empty segment, and removing it
	// would create another uncertain directory-entry window.
	if err := fsys.SyncDir(dir); err != nil {
		return nil, fmt.Errorf("make WAL directory entry for %q durable: %w", finalPath, err)
	}

	activeFile, err := fsys.OpenFile(finalPath, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open published WAL segment %q: %w", finalPath, err)
	}
	offset, err := activeFile.Seek(0, io.SeekEnd)
	if err != nil {
		closeErr := activeFile.Close()
		seekErr := fmt.Errorf("seek to end of published WAL segment %q: %w", finalPath, err)
		if closeErr != nil {
			return nil, errors.Join(seekErr, fmt.Errorf("close published WAL segment %q after seek failure: %w", finalPath, closeErr))
		}
		return nil, seekErr
	}
	if offset != int64(types.WALSegmentHeaderSize) {
		closeErr := activeFile.Close()
		sizeErr := fmt.Errorf("%w: published WAL segment %q has size %d after header creation, want %d", ErrCorrupt, finalPath, offset, types.WALSegmentHeaderSize)
		if closeErr != nil {
			return nil, errors.Join(sizeErr, fmt.Errorf("close published WAL segment %q after size check: %w", finalPath, closeErr))
		}
		return nil, sizeErr
	}

	return &Segment{
		id:    id,
		path:  finalPath,
		file:  activeFile,
		state: SegmentActive,
	}, nil
}

// ID returns the segment ID.
func (s *Segment) ID() types.SegmentID {
	return s.id
}

// Path returns the final filesystem path of the segment.
func (s *Segment) Path() string {
	return s.path
}

// State returns the current segment state.
func (s *Segment) State() SegmentState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Seal closes the segment to further appends.
//
// Sealing does not provide a durability boundary; the writer must have completed
// its required Sync before sealing. A second Seal is a harmless no-op. The state
// changes to sealed before the file is closed so no future operation can treat
// the segment as active while its handle is being closed.
func (s *Segment) Seal() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state == SegmentSealed {
		return nil
	}
	if s.state != SegmentActive {
		return fmt.Errorf("%w: cannot seal WAL segment in state %s", kverr.ErrInvalidArgument, s.state)
	}

	s.state = SegmentSealed
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	if err != nil {
		return fmt.Errorf("close WAL segment %q while sealing: %w", s.path, err)
	}
	return nil
}
