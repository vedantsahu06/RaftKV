package sstable

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/vedantsahu06/raftkv/internal/types"
)

const (
	// MinID is the smallest valid Phase 1 SSTable ID.
	MinID types.SSTableID = 1

	// MaxID is the largest valid Phase 1 SSTable ID. Allocation must never wrap
	// or reuse an ID after this value.
	MaxID types.SSTableID = 999_999

	// FilenameDigits is the exact number of zero-padded decimal digits in an
	// SSTable filename.
	FilenameDigits = 6

	// LevelZero is the only SSTable level used in Phase 1.
	LevelZero uint32 = 0
)

// ErrInvalidMetadata reports SSTable metadata that violates the Phase 1 schema.
var ErrInvalidMetadata = errors.New("raftkv: invalid SSTable metadata")

// Metadata is the Manifest record for one published SSTable.
//
// The Manifest stores these fields as JSON. Field order is defined in
// docs/architecture/phase-1/MANIFEST_DESIGN.md and is enforced by the Manifest
// encoder, not by this struct.
type Metadata struct {
	ID                 types.SSTableID
	Filename           string
	Level              uint32
	FileSize           uint64
	SmallestKey        []byte
	LargestKey         []byte
	MinSequence        types.Sequence
	MaxSequence        types.Sequence
	Checksum           uint32
	SourceWALSegmentID types.SegmentID
}

// Validate checks the metadata rules that can be established without opening the
// SSTable file. Header, block, footer and whole-file checksum agreement with the
// file are checked by the SSTable validation milestone.
func (m Metadata) Validate() error {
	if m.ID < MinID || m.ID > MaxID {
		return invalidMetadata("sstable_id %d is outside %d..%d", m.ID, MinID, MaxID)
	}

	expectedFilename, err := FileName(m.ID)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidMetadata, err)
	}
	if m.Filename != expectedFilename {
		return invalidMetadata("filename %q does not match sstable_id %d (want %q)", m.Filename, m.ID, expectedFilename)
	}

	if m.Level != LevelZero {
		return invalidMetadata("level %d is not supported in Phase 1 (want %d)", m.Level, LevelZero)
	}
	if m.FileSize == 0 {
		return invalidMetadata("file_size must be greater than zero")
	}
	if err := validateKey("smallest_key", m.SmallestKey); err != nil {
		return err
	}
	if err := validateKey("largest_key", m.LargestKey); err != nil {
		return err
	}
	if bytes.Compare(m.SmallestKey, m.LargestKey) > 0 {
		return invalidMetadata("smallest_key is greater than largest_key")
	}
	if m.MinSequence == 0 {
		return invalidMetadata("min_sequence must be greater than zero")
	}
	if m.MaxSequence < m.MinSequence {
		return invalidMetadata("max_sequence %d is less than min_sequence %d", m.MaxSequence, m.MinSequence)
	}
	if m.SourceWALSegmentID == 0 {
		return invalidMetadata("source_wal_segment_id must be greater than zero")
	}
	return nil
}

// Equal reports whether two metadata records describe the same published
// SSTable. Publication uses this to accept an identical re-publication and to
// reject conflicting metadata for an already-known SSTable ID.
func (m Metadata) Equal(other Metadata) bool {
	return m.ID == other.ID &&
		m.Filename == other.Filename &&
		m.Level == other.Level &&
		m.FileSize == other.FileSize &&
		bytes.Equal(m.SmallestKey, other.SmallestKey) &&
		bytes.Equal(m.LargestKey, other.LargestKey) &&
		m.MinSequence == other.MinSequence &&
		m.MaxSequence == other.MaxSequence &&
		m.Checksum == other.Checksum &&
		m.SourceWALSegmentID == other.SourceWALSegmentID
}

// Clone returns a copy whose key byte slices do not alias the receiver's slices.
func (m Metadata) Clone() Metadata {
	m.SmallestKey = bytes.Clone(m.SmallestKey)
	m.LargestKey = bytes.Clone(m.LargestKey)
	return m
}

func validateKey(name string, key []byte) error {
	if len(key) == 0 {
		return invalidMetadata("%s must not be empty", name)
	}
	if len(key) > types.MaxKeySize {
		return invalidMetadata("%s length %d exceeds maximum %d", name, len(key), types.MaxKeySize)
	}
	return nil
}

func invalidMetadata(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidMetadata, fmt.Sprintf(format, args...))
}
