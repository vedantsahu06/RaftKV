package manifest

import (
	"errors"
	"fmt"
	"slices"

	"github.com/vedantsahu06/raftkv/internal/sstable"
	"github.com/vedantsahu06/raftkv/internal/types"
)

const (
	// FormatVersion is the only Manifest format version supported in Phase 1.
	FormatVersion uint32 = 1

	// RetainedVersions is the number of committed Manifest versions retained
	// after cleanup. A transient fourth version is allowed during publication.
	RetainedVersions = 3

	maxSequence = ^types.Sequence(0)
)

// ErrInvalidManifest reports an in-memory Manifest that violates the Phase 1
// schema or invariants.
var ErrInvalidManifest = errors.New("raftkv: invalid Manifest")

// Manifest is the complete authoritative metadata state.
type Manifest struct {
	FormatVersion     uint32
	Version           types.ManifestVersion
	FlushedThroughSeq types.Sequence
	NextSSTableID     types.SSTableID
	SSTables          []sstable.Metadata
}

// Validate checks the Manifest schema and metadata invariants that do not
// require opening SSTable files.
//
// The following recovery checks are intentionally outside this function:
// referenced SSTable existence and full-file validation, header/metadata
// agreement with the Manifest, and WAL evidence.
func (m Manifest) Validate() error {
	if m.FormatVersion != FormatVersion {
		return invalidManifest("format_version %d is not supported (want %d)", m.FormatVersion, FormatVersion)
	}
	if m.Version == 0 {
		return invalidManifest("manifest_version must be greater than zero")
	}
	if m.NextSSTableID == 0 {
		return invalidManifest("next_sstable_id must be greater than zero")
	}
	if len(m.SSTables) == 0 {
		if m.FlushedThroughSeq != 0 {
			return invalidManifest("flushed_through_seq %d must be 0 when sstables is empty", m.FlushedThroughSeq)
		}
		return nil
	}

	seenGenerations := make(map[types.SegmentID]types.SSTableID, len(m.SSTables))
	previousID := types.SSTableID(0)
	for i, table := range m.SSTables {
		if err := table.Validate(); err != nil {
			return fmt.Errorf("%w: sstables[%d]: %w", ErrInvalidManifest, i, err)
		}
		if table.ID <= previousID {
			return invalidManifest("sstables must be sorted by strictly increasing sstable_id: sstables[%d] has ID %d after %d", i, table.ID, previousID)
		}
		if table.ID >= m.NextSSTableID {
			return invalidManifest("next_sstable_id %d must be greater than listed sstable_id %d", m.NextSSTableID, table.ID)
		}
		if firstID, exists := seenGenerations[table.SourceWALSegmentID]; exists {
			return invalidManifest("SSTable IDs %d and %d claim the same source_wal_segment_id %d", firstID, table.ID, table.SourceWALSegmentID)
		}
		seenGenerations[table.SourceWALSegmentID] = table.ID
		previousID = table.ID
	}

	// Sequence ranges must form one contiguous published prefix starting at 1.
	// Sort by sequence rather than relying on SSTable ID order so this check
	// states the actual invariant.
	orderedBySequence := slices.Clone(m.SSTables)
	slices.SortFunc(orderedBySequence, func(a, b sstable.Metadata) int {
		switch {
		case a.MinSequence < b.MinSequence:
			return -1
		case a.MinSequence > b.MinSequence:
			return 1
		default:
			return 0
		}
	})

	expectedMin := types.Sequence(1)
	for i, table := range orderedBySequence {
		if table.MinSequence != expectedMin {
			return invalidManifest("sequence range starting at %d, want %d (ranges must be contiguous from 1)", table.MinSequence, expectedMin)
		}
		if i == len(orderedBySequence)-1 {
			continue
		}
		if table.MaxSequence == maxSequence {
			return invalidManifest("sequence range ending at the maximum sequence is followed by another SSTable")
		}
		expectedMin = table.MaxSequence + 1
	}

	highestSequence := orderedBySequence[len(orderedBySequence)-1].MaxSequence
	if m.FlushedThroughSeq != highestSequence {
		return invalidManifest("flushed_through_seq %d does not equal highest max_sequence %d", m.FlushedThroughSeq, highestSequence)
	}
	return nil
}

// Clone returns a deep copy. The copied SSTable key slices do not alias the
// receiver's slices.
func (m Manifest) Clone() Manifest {
	clone := m
	clone.SSTables = make([]sstable.Metadata, len(m.SSTables))
	for i, table := range m.SSTables {
		clone.SSTables[i] = table.Clone()
	}
	return clone
}

func invalidManifest(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidManifest, fmt.Sprintf(format, args...))
}
