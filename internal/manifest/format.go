package manifest

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"

	"github.com/vedantsahu06/raftkv/internal/sstable"
	"github.com/vedantsahu06/raftkv/internal/types"
)

// ErrCorrupt reports a Manifest file whose bytes cannot be accepted as the
// canonical Phase 1 representation of a valid Manifest.
var ErrCorrupt = errors.New("raftkv: corrupt Manifest data")

var castagnoliTable = crc32.MakeTable(crc32.Castagnoli)

// The JSON structs below are private on purpose. Field order in a Go struct is
// preserved by encoding/json, so these definitions are the single source of the
// canonical field order. They are never used as the public data model.
type encodedManifest struct {
	FormatVersion     uint32           `json:"format_version"`
	ManifestVersion   uint64           `json:"manifest_version"`
	FlushedThroughSeq uint64           `json:"flushed_through_seq"`
	NextSSTableID     uint64           `json:"next_sstable_id"`
	SSTables          []encodedSSTable `json:"sstables"`
}

type encodedSSTable struct {
	SSTableID          uint64 `json:"sstable_id"`
	Filename           string `json:"filename"`
	Level              uint32 `json:"level"`
	FileSize           uint64 `json:"file_size"`
	SmallestKey        []byte `json:"smallest_key_b64"`
	LargestKey         []byte `json:"largest_key_b64"`
	MinSequence        uint64 `json:"min_sequence"`
	MaxSequence        uint64 `json:"max_sequence"`
	Checksum           uint32 `json:"checksum"`
	SourceWALSegmentID uint64 `json:"source_wal_segment_id"`
}

// Encode validates m and returns the complete Manifest file bytes: canonical
// compact JSON payload followed by the four-byte little-endian CRC32C trailer.
func Encode(m Manifest) ([]byte, error) {
	payload, err := encodePayload(m)
	if err != nil {
		return nil, err
	}

	checksum := crc32.Checksum(payload, castagnoliTable)
	encoded := make([]byte, len(payload)+4)
	copy(encoded, payload)
	binary.LittleEndian.PutUint32(encoded[len(payload):], checksum)
	return encoded, nil
}

// Decode parses and fully validates the Manifest-level representation in data.
// Referenced SSTable files are not opened; recovery must apply that additional
// validation before treating the result as a usable database state.
func Decode(data []byte) (Manifest, error) {
	if len(data) < 4 {
		return Manifest{}, corruptf("file length %d does not contain a four-byte CRC trailer", len(data))
	}

	payload := data[:len(data)-4]
	wantChecksum := binary.LittleEndian.Uint32(data[len(data)-4:])
	gotChecksum := crc32.Checksum(payload, castagnoliTable)
	if gotChecksum != wantChecksum {
		return Manifest{}, corruptf("payload checksum %#08x does not match trailer %#08x", gotChecksum, wantChecksum)
	}

	return decodePayload(payload)
}

func encodePayload(m Manifest) ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}

	wire := encodedManifest{
		FormatVersion:     m.FormatVersion,
		ManifestVersion:   uint64(m.Version),
		FlushedThroughSeq: uint64(m.FlushedThroughSeq),
		NextSSTableID:     uint64(m.NextSSTableID),
		// A non-nil empty slice makes the canonical form [] rather than null.
		SSTables: make([]encodedSSTable, len(m.SSTables)),
	}
	for i, table := range m.SSTables {
		wire.SSTables[i] = encodedSSTable{
			SSTableID:          uint64(table.ID),
			Filename:           table.Filename,
			Level:              table.Level,
			FileSize:           table.FileSize,
			SmallestKey:        table.SmallestKey,
			LargestKey:         table.LargestKey,
			MinSequence:        uint64(table.MinSequence),
			MaxSequence:        uint64(table.MaxSequence),
			Checksum:           table.Checksum,
			SourceWALSegmentID: uint64(table.SourceWALSegmentID),
		}
	}

	payload, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("encode Manifest JSON: %w", err)
	}
	return payload, nil
}

func decodePayload(payload []byte) (Manifest, error) {
	if len(payload) == 0 {
		return Manifest{}, corruptf("payload is empty")
	}

	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()

	var wire encodedManifest
	if err := decoder.Decode(&wire); err != nil {
		return Manifest{}, fmt.Errorf("%w: decode JSON: %w", ErrCorrupt, err)
	}

	// Reject a second JSON value or malformed trailing bytes. Trailing
	// whitespace is syntactically accepted here but is caught by the canonical
	// byte comparison below.
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Manifest{}, corruptf("payload contains more than one JSON value")
		}
		return Manifest{}, fmt.Errorf("%w: decode trailing JSON: %w", ErrCorrupt, err)
	}

	m := Manifest{
		FormatVersion:     wire.FormatVersion,
		Version:           types.ManifestVersion(wire.ManifestVersion),
		FlushedThroughSeq: types.Sequence(wire.FlushedThroughSeq),
		NextSSTableID:     types.SSTableID(wire.NextSSTableID),
		SSTables:          make([]sstable.Metadata, len(wire.SSTables)),
	}
	for i, table := range wire.SSTables {
		m.SSTables[i] = sstable.Metadata{
			ID:                 types.SSTableID(table.SSTableID),
			Filename:           table.Filename,
			Level:              table.Level,
			FileSize:           table.FileSize,
			SmallestKey:        table.SmallestKey,
			LargestKey:         table.LargestKey,
			MinSequence:        types.Sequence(table.MinSequence),
			MaxSequence:        types.Sequence(table.MaxSequence),
			Checksum:           table.Checksum,
			SourceWALSegmentID: types.SegmentID(table.SourceWALSegmentID),
		}
	}

	if err := m.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("%w: schema: %w", ErrCorrupt, err)
	}

	// Re-encoding and comparing bytes rejects duplicate fields, unknown key
	// casing, alternative number spellings, non-minimal base64 and any other
	// representation that is not the one canonical encoder output.
	canonical, err := encodePayload(m)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: re-encode: %w", ErrCorrupt, err)
	}
	if !bytes.Equal(canonical, payload) {
		return Manifest{}, corruptf("payload is not canonical compact JSON with the required field order")
	}
	return m, nil
}

func corruptf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrCorrupt, fmt.Sprintf(format, args...))
}
