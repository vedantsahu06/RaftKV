package wal

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/vedantsahu06/raftkv/internal/filesystem"
	"github.com/vedantsahu06/raftkv/internal/kverr"
	"github.com/vedantsahu06/raftkv/internal/types"
)

const (
	segmentNamePrefix  = "wal-"
	segmentNameDigits  = 20
	segmentFinalSuffix = ".wal"
	segmentTempSuffix  = ".wal.tmp"

	// maxSegmentID is the largest representable SegmentID. It is also exactly
	// 20 decimal digits, so every valid final name has a fixed encoded width.
	maxSegmentID = ^types.SegmentID(0)
)

// ErrInvalidSegmentName reports a filename that is not an exact WAL v1 segment
// or temporary segment name.
var ErrInvalidSegmentName = errors.New("raftkv: invalid WAL segment filename")

// ErrSegmentIDExhausted reports that no larger segment ID can be allocated.
var ErrSegmentIDExhausted = errors.New("raftkv: WAL segment ID space exhausted")

// SegmentFileName returns the final filename for id, without a directory.
func SegmentFileName(id types.SegmentID) (string, error) {
	return formatSegmentName(id, segmentFinalSuffix)
}

// SegmentTempFileName returns the temporary creation filename for id, without a
// directory.
func SegmentTempFileName(id types.SegmentID) (string, error) {
	return formatSegmentName(id, segmentTempSuffix)
}

// ParseSegmentFileName parses an exact final WAL segment base filename.
func ParseSegmentFileName(name string) (types.SegmentID, error) {
	return parseSegmentName(name, segmentFinalSuffix)
}

// ParseSegmentTempFileName parses an exact temporary WAL segment base filename.
func ParseSegmentTempFileName(name string) (types.SegmentID, error) {
	return parseSegmentName(name, segmentTempSuffix)
}

// NextSegmentID returns the next allocatable WAL segment ID after observing
// every valid final and temporary WAL filename in dir.
//
// Temporary files are never published segments, but their IDs are still
// consumed so a later creation cannot collide with an existing temporary name.
// Malformed names establish no ID and are ignored here; recovery is responsible
// for treating invalid published WAL artifacts as errors.
func NextSegmentID(fsys filesystem.FS, dir string) (types.SegmentID, error) {
	entries, err := fsys.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("read WAL directory %q: %w", dir, err)
	}

	var highest types.SegmentID
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()

		id, finalErr := ParseSegmentFileName(name)
		if finalErr != nil {
			id, finalErr = ParseSegmentTempFileName(name)
		}
		if finalErr != nil {
			continue
		}
		if id > highest {
			highest = id
		}
	}

	if highest == maxSegmentID {
		return 0, ErrSegmentIDExhausted
	}
	return highest + 1, nil
}

func formatSegmentName(id types.SegmentID, suffix string) (string, error) {
	if id == 0 {
		return "", fmt.Errorf("%w: WAL segment ID must be at least 1", kverr.ErrInvalidArgument)
	}
	// The width matches segmentNameDigits and the format in WAL_DESIGN.md.
	return fmt.Sprintf("%s%020d%s", segmentNamePrefix, uint64(id), suffix), nil
}

func parseSegmentName(name, suffix string) (types.SegmentID, error) {
	if !strings.HasPrefix(name, segmentNamePrefix) || !strings.HasSuffix(name, suffix) {
		return 0, fmt.Errorf("%w: %q", ErrInvalidSegmentName, name)
	}

	digitsStart := len(segmentNamePrefix)
	digitsEnd := len(name) - len(suffix)
	digits := name[digitsStart:digitsEnd]
	if len(digits) != segmentNameDigits {
		return 0, fmt.Errorf("%w: %q does not contain %d ID digits", ErrInvalidSegmentName, name, segmentNameDigits)
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return 0, fmt.Errorf("%w: %q contains a non-decimal ID", ErrInvalidSegmentName, name)
		}
	}

	id, err := strconv.ParseUint(digits, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q: %v", ErrInvalidSegmentName, name, err)
	}
	if id == 0 {
		return 0, fmt.Errorf("%w: %q has segment ID 0", ErrInvalidSegmentName, name)
	}
	return types.SegmentID(id), nil
}
