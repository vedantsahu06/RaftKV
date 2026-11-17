package manifest

import (
	"errors"
	"testing"

	"github.com/vedantsahu06/raftkv/internal/sstable"
	"github.com/vedantsahu06/raftkv/internal/types"
)

func stateWithTables(t *testing.T, version types.ManifestVersion, next types.SSTableID, tables ...sstable.Metadata) Manifest {
	t.Helper()

	state := Manifest{
		FormatVersion: FormatVersion,
		Version:       version,
		NextSSTableID: next,
		SSTables:      tables,
	}
	if len(tables) > 0 {
		state.FlushedThroughSeq = tables[len(tables)-1].MaxSequence
	}
	return state
}

func TestManifestValidate(t *testing.T) {
	maximumSequence := ^types.Sequence(0)

	tests := []struct {
		name    string
		build   func(t *testing.T) Manifest
		wantErr bool
	}{
		{
			name: "valid empty",
			build: func(t *testing.T) Manifest {
				return emptyManifest(1)
			},
		},
		{
			name: "valid one SSTable",
			build: func(t *testing.T) Manifest {
				return stateWithTables(t, 1, 2, testMetadata(t, 1, 1, 10, 1))
			},
		},
		{
			name: "valid two SSTables",
			build: func(t *testing.T) Manifest {
				return stateWithTables(t, 2, 3,
					testMetadata(t, 1, 1, 10, 1),
					testMetadata(t, 2, 11, 20, 2),
				)
			},
		},
		{
			name: "valid next ID one past maximum",
			build: func(t *testing.T) Manifest {
				return stateWithTables(t, 1, 1_000_000, testMetadata(t, 999_999, 1, 1, 1))
			},
		},
		{
			name: "valid maximum sequence",
			build: func(t *testing.T) Manifest {
				state := stateWithTables(t, 1, 2, testMetadata(t, 1, 1, maximumSequence, 1))
				state.FlushedThroughSeq = maximumSequence
				return state
			},
		},
		{
			name: "unsupported format version",
			build: func(t *testing.T) Manifest {
				state := emptyManifest(1)
				state.FormatVersion = 2
				return state
			},
			wantErr: true,
		},
		{
			name: "version zero",
			build: func(t *testing.T) Manifest {
				return emptyManifest(0)
			},
			wantErr: true,
		},
		{
			name: "next ID zero",
			build: func(t *testing.T) Manifest {
				state := emptyManifest(1)
				state.NextSSTableID = 0
				return state
			},
			wantErr: true,
		},
		{
			name: "empty with nonzero flushed sequence",
			build: func(t *testing.T) Manifest {
				state := emptyManifest(1)
				state.FlushedThroughSeq = 1
				return state
			},
			wantErr: true,
		},
		{
			name: "unsorted IDs",
			build: func(t *testing.T) Manifest {
				return stateWithTables(t, 1, 3,
					testMetadata(t, 2, 1, 10, 1),
					testMetadata(t, 1, 11, 20, 2),
				)
			},
			wantErr: true,
		},
		{
			name: "duplicate ID",
			build: func(t *testing.T) Manifest {
				return stateWithTables(t, 1, 2,
					testMetadata(t, 1, 1, 10, 1),
					testMetadata(t, 1, 11, 20, 2),
				)
			},
			wantErr: true,
		},
		{
			name: "next ID not greater than listed ID",
			build: func(t *testing.T) Manifest {
				return stateWithTables(t, 1, 1, testMetadata(t, 1, 1, 10, 1))
			},
			wantErr: true,
		},
		{
			name: "duplicate source generation",
			build: func(t *testing.T) Manifest {
				return stateWithTables(t, 1, 3,
					testMetadata(t, 1, 1, 10, 7),
					testMetadata(t, 2, 11, 20, 7),
				)
			},
			wantErr: true,
		},
		{
			name: "sequence starts above one",
			build: func(t *testing.T) Manifest {
				return stateWithTables(t, 1, 2, testMetadata(t, 1, 2, 10, 1))
			},
			wantErr: true,
		},
		{
			name: "sequence gap",
			build: func(t *testing.T) Manifest {
				return stateWithTables(t, 1, 3,
					testMetadata(t, 1, 1, 10, 1),
					testMetadata(t, 2, 12, 20, 2),
				)
			},
			wantErr: true,
		},
		{
			name: "sequence overlap",
			build: func(t *testing.T) Manifest {
				return stateWithTables(t, 1, 3,
					testMetadata(t, 1, 1, 10, 1),
					testMetadata(t, 2, 10, 20, 2),
				)
			},
			wantErr: true,
		},
		{
			name: "flushed sequence below highest",
			build: func(t *testing.T) Manifest {
				state := stateWithTables(t, 1, 3,
					testMetadata(t, 1, 1, 10, 1),
					testMetadata(t, 2, 11, 20, 2),
				)
				state.FlushedThroughSeq = 19
				return state
			},
			wantErr: true,
		},
		{
			name: "flushed sequence above highest",
			build: func(t *testing.T) Manifest {
				state := stateWithTables(t, 1, 3,
					testMetadata(t, 1, 1, 10, 1),
					testMetadata(t, 2, 11, 20, 2),
				)
				state.FlushedThroughSeq = 21
				return state
			},
			wantErr: true,
		},
		{
			name: "maximum sequence followed by another SSTable",
			build: func(t *testing.T) Manifest {
				state := stateWithTables(t, 1, 3,
					testMetadata(t, 1, 1, maximumSequence, 1),
					testMetadata(t, 2, 1, 1, 2),
				)
				state.FlushedThroughSeq = maximumSequence
				return state
			},
			wantErr: true,
		},
		{
			name: "invalid SSTable metadata",
			build: func(t *testing.T) Manifest {
				table := testMetadata(t, 1, 1, 10, 1)
				table.Level = 1
				return stateWithTables(t, 1, 2, table)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.build(t).Validate()
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidManifest) {
					t.Fatalf("Validate() = %v, want ErrInvalidManifest", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestManifestValidatePropagatesMetadataError(t *testing.T) {
	table := testMetadata(t, 1, 1, 10, 1)
	table.Level = 1
	err := stateWithTables(t, 1, 2, table).Validate()
	if !errors.Is(err, ErrInvalidManifest) || !errors.Is(err, sstable.ErrInvalidMetadata) {
		t.Fatalf("Validate() = %v, want both ErrInvalidManifest and ErrInvalidMetadata", err)
	}
}

func TestManifestCloneDeepCopies(t *testing.T) {
	original := stateWithTables(t, 1, 2, testMetadata(t, 1, 1, 10, 1))
	clone := original.Clone()

	clone.SSTables[0].SmallestKey[0] = 'x'
	clone.SSTables[0].LargestKey[0] = 'y'
	clone.SSTables = append(clone.SSTables, sstable.Metadata{})

	if string(original.SSTables[0].SmallestKey) != "a" {
		t.Fatalf("original smallest key = %q after clone mutation, want %q", original.SSTables[0].SmallestKey, "a")
	}
	if string(original.SSTables[0].LargestKey) != "z" {
		t.Fatalf("original largest key = %q after clone mutation, want %q", original.SSTables[0].LargestKey, "z")
	}
	if len(original.SSTables) != 1 {
		t.Fatalf("original SSTables length = %d after clone append, want 1", len(original.SSTables))
	}
}
