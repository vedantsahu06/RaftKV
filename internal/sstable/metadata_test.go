package sstable

import (
	"errors"
	"testing"

	"github.com/vedantsahu06/raftkv/internal/types"
)

func validMetadata(t *testing.T) Metadata {
	t.Helper()

	filename, err := FileName(17)
	if err != nil {
		t.Fatalf("FileName(17): %v", err)
	}
	return Metadata{
		ID:                 17,
		Filename:           filename,
		Level:              LevelZero,
		FileSize:           12_345,
		SmallestKey:        []byte("a"),
		LargestKey:         []byte("z"),
		MinSequence:        1,
		MaxSequence:        200,
		Checksum:           0xdeadbeef,
		SourceWALSegmentID: 3,
	}
}

func TestMetadataValidate(t *testing.T) {
	tooLargeKey := make([]byte, types.MaxKeySize+1)

	tests := []struct {
		name    string
		mutate  func(*Metadata)
		wantErr bool
	}{
		{name: "valid"},
		{name: "equal key bounds", mutate: func(m *Metadata) { m.LargestKey = []byte("a") }},
		{name: "maximum key size", mutate: func(m *Metadata) {
			m.SmallestKey = make([]byte, types.MaxKeySize)
			m.LargestKey = make([]byte, types.MaxKeySize)
		}},
		{name: "minimum ID", mutate: func(m *Metadata) { setID(t, m, MinID) }},
		{name: "maximum ID", mutate: func(m *Metadata) { setID(t, m, MaxID) }},
		{name: "ID zero", mutate: func(m *Metadata) {
			m.ID = 0
			m.Filename = "sstable-000000.sst"
		}, wantErr: true},
		{name: "ID above maximum", mutate: func(m *Metadata) {
			m.ID = MaxID + 1
			m.Filename = "sstable-1000000.sst"
		}, wantErr: true},
		{name: "empty filename", mutate: func(m *Metadata) { m.Filename = "" }, wantErr: true},
		{name: "filename for another ID", mutate: func(m *Metadata) { m.Filename = "sstable-000018.sst" }, wantErr: true},
		{name: "temporary filename", mutate: func(m *Metadata) { m.Filename = "sstable-000017.sst.tmp" }, wantErr: true},
		{name: "unsupported level", mutate: func(m *Metadata) { m.Level = 1 }, wantErr: true},
		{name: "zero file size", mutate: func(m *Metadata) { m.FileSize = 0 }, wantErr: true},
		{name: "empty smallest key", mutate: func(m *Metadata) { m.SmallestKey = nil }, wantErr: true},
		{name: "empty largest key", mutate: func(m *Metadata) { m.LargestKey = []byte{} }, wantErr: true},
		{name: "smallest key greater than largest key", mutate: func(m *Metadata) {
			m.SmallestKey = []byte("z")
			m.LargestKey = []byte("a")
		}, wantErr: true},
		{name: "oversized smallest key", mutate: func(m *Metadata) {
			m.SmallestKey = tooLargeKey
			m.LargestKey = tooLargeKey
		}, wantErr: true},
		{name: "zero minimum sequence", mutate: func(m *Metadata) { m.MinSequence = 0 }, wantErr: true},
		{name: "maximum sequence below minimum", mutate: func(m *Metadata) { m.MaxSequence = 0 }, wantErr: true},
		{name: "zero source WAL segment", mutate: func(m *Metadata) { m.SourceWALSegmentID = 0 }, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metadata := validMetadata(t)
			if tt.mutate != nil {
				tt.mutate(&metadata)
			}

			err := metadata.Validate()
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidMetadata) {
					t.Fatalf("Validate() = %v, want ErrInvalidMetadata", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}

func setID(t *testing.T, metadata *Metadata, id types.SSTableID) {
	t.Helper()

	filename, err := FileName(id)
	if err != nil {
		t.Fatalf("FileName(%d): %v", id, err)
	}
	metadata.ID = id
	metadata.Filename = filename
}

func TestMetadataEqual(t *testing.T) {
	base := validMetadata(t)
	if !base.Equal(base.Clone()) {
		t.Fatal("a metadata copy is not Equal to the original")
	}

	tests := []struct {
		name   string
		mutate func(*Metadata)
	}{
		{name: "ID", mutate: func(m *Metadata) { setID(t, m, 18) }},
		{name: "filename", mutate: func(m *Metadata) { m.Filename = "sstable-000018.sst" }},
		{name: "level", mutate: func(m *Metadata) { m.Level = 1 }},
		{name: "file size", mutate: func(m *Metadata) { m.FileSize++ }},
		{name: "smallest key", mutate: func(m *Metadata) { m.SmallestKey = []byte("b") }},
		{name: "smallest key nil versus empty", mutate: func(m *Metadata) { m.SmallestKey = []byte{} }},
		{name: "largest key", mutate: func(m *Metadata) { m.LargestKey = []byte("y") }},
		{name: "minimum sequence", mutate: func(m *Metadata) { m.MinSequence++ }},
		{name: "maximum sequence", mutate: func(m *Metadata) { m.MaxSequence++ }},
		{name: "checksum", mutate: func(m *Metadata) { m.Checksum++ }},
		{name: "source WAL segment", mutate: func(m *Metadata) { m.SourceWALSegmentID++ }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			other := base.Clone()
			tt.mutate(&other)
			if base.Equal(other) {
				t.Fatalf("Equal() = true after changing %s", tt.name)
			}
		})
	}
}

func TestMetadataCloneDoesNotAliasKeys(t *testing.T) {
	original := validMetadata(t)
	clone := original.Clone()

	clone.SmallestKey[0] = 'x'
	clone.LargestKey[0] = 'y'

	if string(original.SmallestKey) != "a" {
		t.Fatalf("original smallest key = %q after clone mutation, want %q", original.SmallestKey, "a")
	}
	if string(original.LargestKey) != "z" {
		t.Fatalf("original largest key = %q after clone mutation, want %q", original.LargestKey, "z")
	}
}
