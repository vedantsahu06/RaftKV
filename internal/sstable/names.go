package sstable

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/vedantsahu06/raftkv/internal/filesystem"
	"github.com/vedantsahu06/raftkv/internal/kverr"
	"github.com/vedantsahu06/raftkv/internal/types"
)

const (
	filenamePrefix = "sstable-"
	finalSuffix    = ".sst"
	tempSuffix     = ".sst.tmp"
)

// ErrInvalidName reports a filename that is not an exact SSTable v1 final or
// temporary filename.
var ErrInvalidName = errors.New("raftkv: invalid SSTable filename")

// ErrIDExhausted reports that no SSTable ID remains in the Phase 1 range.
var ErrIDExhausted = errors.New("raftkv: SSTable ID space exhausted")

// FileName returns the final filename for id, without a directory.
func FileName(id types.SSTableID) (string, error) {
	return formatName(id, finalSuffix)
}

// TempFileName returns the temporary creation filename for id, without a
// directory.
func TempFileName(id types.SSTableID) (string, error) {
	return formatName(id, tempSuffix)
}

// ParseFileName parses an exact final SSTable base filename.
func ParseFileName(name string) (types.SSTableID, error) {
	return parseName(name, finalSuffix)
}

// ParseTempFileName parses an exact temporary SSTable base filename.
func ParseTempFileName(name string) (types.SSTableID, error) {
	return parseName(name, tempSuffix)
}

// IDScan is the result of scanning one SSTable directory for allocation safety.
type IDScan struct {
	// IDs is the sorted, de-duplicated set of IDs established by valid final
	// and temporary filenames.
	IDs []types.SSTableID

	// Highest is the largest observed ID, or zero when no valid name exists.
	Highest types.SSTableID

	// Malformed contains sorted names that begin with "sstable-" but do not
	// match either exact filename form. They establish no ID and are never
	// treated as published.
	Malformed []string
}

// ScanIDs reads dir and reports every SSTable ID established by a final or
// temporary filename.
//
// A temporary file is not published, but its ID is consumed so a later flush
// cannot create a colliding name. Contents are deliberately not inspected here:
// even a corrupt artifact consumes its ID.
func ScanIDs(fsys filesystem.FS, dir string) (IDScan, error) {
	entries, err := fsys.ReadDir(dir)
	if err != nil {
		return IDScan{}, fmt.Errorf("read SSTable directory %q: %w", dir, err)
	}

	observed := make(map[types.SSTableID]struct{})
	var malformed []string
	for _, entry := range entries {
		name := entry.Name()

		id, parseErr := ParseFileName(name)
		if parseErr != nil {
			id, parseErr = ParseTempFileName(name)
		}
		if parseErr == nil {
			if entry.IsDir() {
				// A directory with an SSTable filename is not a file artifact.
				// It establishes no ID, but it is reported for diagnostics.
				malformed = append(malformed, name)
				continue
			}
			observed[id] = struct{}{}
			continue
		}

		if strings.HasPrefix(name, filenamePrefix) {
			malformed = append(malformed, name)
		}
	}

	scan := IDScan{
		IDs:       make([]types.SSTableID, 0, len(observed)),
		Malformed: malformed,
	}
	for id := range observed {
		scan.IDs = append(scan.IDs, id)
	}
	slices.Sort(scan.IDs)
	slices.Sort(scan.Malformed)
	if len(scan.IDs) > 0 {
		scan.Highest = scan.IDs[len(scan.IDs)-1]
	}
	return scan, nil
}

// NextID computes the next allocatable SSTable ID:
//
//	max(manifestNext, HighestObservedSSTableID + 1)
//
// Exhaustion is reported rather than wrapping to 1. An ID allocated in memory
// but never used may be reused after restart; an observed final or temporary
// filename may never be reused.
func NextID(manifestNext types.SSTableID, scan IDScan) (types.SSTableID, error) {
	if manifestNext < MinID {
		return 0, fmt.Errorf("%w: Manifest NextSSTableID must be at least %d", kverr.ErrInvalidArgument, MinID)
	}
	if scan.Highest > MaxID {
		return 0, fmt.Errorf("%w: observed SSTable ID %d exceeds %d", kverr.ErrInvalidArgument, scan.Highest, MaxID)
	}

	next := manifestNext
	if scan.Highest >= next {
		next = scan.Highest + 1
	}
	if next > MaxID {
		return 0, ErrIDExhausted
	}
	return next, nil
}

func formatName(id types.SSTableID, suffix string) (string, error) {
	if id < MinID || id > MaxID {
		return "", fmt.Errorf("%w: SSTable ID %d is outside %d..%d", kverr.ErrInvalidArgument, id, MinID, MaxID)
	}
	return fmt.Sprintf("%s%06d%s", filenamePrefix, id, suffix), nil
}

func parseName(name, suffix string) (types.SSTableID, error) {
	if !strings.HasPrefix(name, filenamePrefix) || !strings.HasSuffix(name, suffix) {
		return 0, fmt.Errorf("%w: %q", ErrInvalidName, name)
	}

	digitsStart := len(filenamePrefix)
	digitsEnd := len(name) - len(suffix)
	if digitsEnd-digitsStart != FilenameDigits {
		return 0, fmt.Errorf("%w: %q does not contain %d ID digits", ErrInvalidName, name, FilenameDigits)
	}

	digits := name[digitsStart:digitsEnd]
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return 0, fmt.Errorf("%w: %q contains a non-decimal ID", ErrInvalidName, name)
		}
	}

	id, err := strconv.ParseUint(digits, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q: %v", ErrInvalidName, name, err)
	}
	if id == 0 {
		return 0, fmt.Errorf("%w: %q has SSTable ID 0", ErrInvalidName, name)
	}
	return types.SSTableID(id), nil
}
