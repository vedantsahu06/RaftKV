// Package types defines the identifier, entry and format types shared by the
// Phase 1 storage engine components.
package types

import "fmt"

// Sequence is the monotonically increasing WAL sequence number assigned to an
// admitted mutation. Sequence numbers start at 1.
type Sequence uint64

// SegmentID identifies one WAL segment. Segment IDs start at 1.
type SegmentID uint64

// GenerationID identifies one Memtable generation. The architecture defines a
// generation by its WAL segment, so the two identifiers are the same type.
type GenerationID = SegmentID

// SSTableID identifies one immutable SSTable. It is independent of WAL segment
// identifiers.
type SSTableID uint64

// ManifestVersion is the monotonically increasing version of a Manifest file.
// Manifest versions start at 1.
type ManifestVersion uint64

// EntryType identifies the kind of mutation stored in a WAL record or Memtable
// entry.
type EntryType uint8

const (
	// EntryTypePut is the only value-bearing entry type in Phase 1.
	EntryTypePut EntryType = 1

	// EntryTypeDelete is a tombstone.
	EntryTypeDelete EntryType = 2
)

// Valid reports whether t is an approved Phase 1 entry type.
func (t EntryType) Valid() bool {
	return t == EntryTypePut || t == EntryTypeDelete
}

// String returns the entry type name or a numeric representation for unknown
// values.
func (t EntryType) String() string {
	switch t {
	case EntryTypePut:
		return "put"
	case EntryTypeDelete:
		return "delete"
	default:
		return fmt.Sprintf("EntryType(%d)", uint8(t))
	}
}

// WAL v1 format limits. These are on-disk format contracts, not tuning
// parameters.
const (
	// WALSegmentHeaderSize is the exact v1 WAL segment header size in bytes.
	WALSegmentHeaderSize = 20

	// WALRecordOverhead is the fixed per-record framing overhead in bytes,
	// including the trailing CRC32C.
	WALRecordOverhead = 28

	// MaxRecordSize is the maximum complete encoded WAL record size in bytes.
	MaxRecordSize = 16 << 20

	// MaxKeySize is the maximum key size in bytes.
	MaxKeySize = 1 << 20
)

// MaxValueSizeForKey returns the largest value that can be stored in a WAL
// record with keyLen bytes while keeping the complete record within
// MaxRecordSize. It returns false when keyLen is outside the valid key range.
func MaxValueSizeForKey(keyLen int) (int, bool) {
	if keyLen < 1 || keyLen > MaxKeySize {
		return 0, false
	}
	return MaxRecordSize - WALRecordOverhead - keyLen, true
}
