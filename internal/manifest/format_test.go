package manifest

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"reflect"
	"strings"
	"testing"

	"github.com/vedantsahu06/raftkv/internal/sstable"
	"github.com/vedantsahu06/raftkv/internal/types"
)

const emptyManifestPayload = `{"format_version":1,"manifest_version":1,"flushed_through_seq":0,"next_sstable_id":1,"sstables":[]}`

const oneSSTableManifestPayload = `{"format_version":1,"manifest_version":7,"flushed_through_seq":200,"next_sstable_id":18,"sstables":[{"sstable_id":17,"filename":"sstable-000017.sst","level":0,"file_size":12345,"smallest_key_b64":"YQ==","largest_key_b64":"eg==","min_sequence":1,"max_sequence":200,"checksum":3735928559,"source_wal_segment_id":1}]}`

func TestEncodeEmptyManifestExactBytes(t *testing.T) {
	encoded, err := Encode(emptyManifest(1))
	if err != nil {
		t.Fatalf("Encode(): %v", err)
	}

	// The trailer is the independently computed little-endian CRC32C of the
	// exact payload above.
	want := append([]byte(emptyManifestPayload), 0x8e, 0x21, 0xe7, 0x34)
	if !bytes.Equal(encoded, want) {
		t.Fatalf("Encode() = %x, want %x", encoded, want)
	}
}

func TestEncodeSSTableManifestExactBytes(t *testing.T) {
	state := Manifest{
		FormatVersion:     FormatVersion,
		Version:           7,
		FlushedThroughSeq: 200,
		NextSSTableID:     18,
		SSTables: []sstable.Metadata{{
			ID:                 17,
			Filename:           "sstable-000017.sst",
			Level:              0,
			FileSize:           12_345,
			SmallestKey:        []byte("a"),
			LargestKey:         []byte("z"),
			MinSequence:        1,
			MaxSequence:        200,
			Checksum:           3_735_928_559,
			SourceWALSegmentID: 1,
		}},
	}

	encoded, err := Encode(state)
	if err != nil {
		t.Fatalf("Encode(): %v", err)
	}

	want := append([]byte(oneSSTableManifestPayload), 0x69, 0x73, 0xe4, 0x75)
	if !bytes.Equal(encoded, want) {
		t.Fatalf("Encode() = %x, want %x", encoded, want)
	}
}

func TestEncodeChecksumCoversPayloadOnly(t *testing.T) {
	encoded, err := Encode(emptyManifest(1))
	if err != nil {
		t.Fatalf("Encode(): %v", err)
	}
	if len(encoded) != len(emptyManifestPayload)+4 {
		t.Fatalf("encoded length = %d, want payload %d + 4", len(encoded), len(emptyManifestPayload))
	}

	payload := encoded[:len(encoded)-4]
	if !bytes.Equal(payload, []byte(emptyManifestPayload)) {
		t.Fatalf("payload = %q, want %q", payload, emptyManifestPayload)
	}
	wantChecksum := crc32.Checksum(payload, crc32.MakeTable(crc32.Castagnoli))
	gotChecksum := binary.LittleEndian.Uint32(encoded[len(encoded)-4:])
	if gotChecksum != wantChecksum {
		t.Fatalf("trailer checksum = %#08x, want %#08x", gotChecksum, wantChecksum)
	}
}

func TestEncodeDeterministic(t *testing.T) {
	state := Manifest{
		FormatVersion:     FormatVersion,
		Version:           3,
		FlushedThroughSeq: 20,
		NextSSTableID:     3,
		SSTables: []sstable.Metadata{
			testMetadata(t, 1, 1, 10, 1),
			testMetadata(t, 2, 11, 20, 2),
		},
	}

	first, err := Encode(state)
	if err != nil {
		t.Fatalf("Encode() first: %v", err)
	}
	second, err := Encode(state)
	if err != nil {
		t.Fatalf("Encode() second: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("two encodings differ:\nfirst  %x\nsecond %x", first, second)
	}
}

func TestDecodeRoundTrip(t *testing.T) {
	state := Manifest{
		FormatVersion:     FormatVersion,
		Version:           3,
		FlushedThroughSeq: 20,
		NextSSTableID:     4,
		SSTables: []sstable.Metadata{
			testMetadata(t, 1, 1, 10, 1),
			testMetadata(t, 2, 11, 20, 2),
		},
	}
	state.SSTables[0].SmallestKey = []byte{0x00, 0xff, 'a'}
	state.SSTables[0].LargestKey = []byte{0x7f, 'z'}
	state.SSTables[1].SmallestKey = []byte("binary\x00key")
	state.SSTables[1].LargestKey = []byte("binary\xffkey")

	encoded, err := Encode(state)
	if err != nil {
		t.Fatalf("Encode(): %v", err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatalf("Decode(): %v", err)
	}
	if !reflect.DeepEqual(decoded, state) {
		t.Fatalf("Decode() = %+v, want %+v", decoded, state)
	}
}

func TestDecodeMaxKeySizeRoundTrip(t *testing.T) {
	smallestKey := bytes.Repeat([]byte{0x00}, types.MaxKeySize)
	largestKey := bytes.Repeat([]byte{0xff}, types.MaxKeySize)

	state := Manifest{
		FormatVersion:     FormatVersion,
		Version:           1,
		FlushedThroughSeq: 1,
		NextSSTableID:     2,
		SSTables: []sstable.Metadata{{
			ID:                 1,
			Filename:           "sstable-000001.sst",
			Level:              0,
			FileSize:           1,
			SmallestKey:        smallestKey,
			LargestKey:         largestKey,
			MinSequence:        1,
			MaxSequence:        1,
			Checksum:           0,
			SourceWALSegmentID: 1,
		}},
	}

	encoded, err := Encode(state)
	if err != nil {
		t.Fatalf("Encode(): %v", err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatalf("Decode(): %v", err)
	}
	if len(decoded.SSTables) != 1 {
		t.Fatalf("decoded SSTable count = %d, want 1", len(decoded.SSTables))
	}
	if !bytes.Equal(decoded.SSTables[0].SmallestKey, smallestKey) {
		t.Fatalf("decoded smallest key length = %d, want exactly %d equal bytes", len(decoded.SSTables[0].SmallestKey), len(smallestKey))
	}
	if !bytes.Equal(decoded.SSTables[0].LargestKey, largestKey) {
		t.Fatalf("decoded largest key length = %d, want exactly %d equal bytes", len(decoded.SSTables[0].LargestKey), len(largestKey))
	}
}

func TestDecodeRejectsBadTrailerOrLength(t *testing.T) {
	valid, err := Encode(emptyManifest(1))
	if err != nil {
		t.Fatalf("Encode(): %v", err)
	}

	badTrailer := bytes.Clone(valid)
	badTrailer[len(badTrailer)-1] ^= 0xff
	badPayload := bytes.Clone(valid)
	badPayload[0] ^= 0xff

	tests := []struct {
		name string
		data []byte
	}{
		{name: "zero bytes", data: nil},
		{name: "one byte", data: []byte{0}},
		{name: "two bytes", data: []byte{0, 0}},
		{name: "three bytes", data: []byte{0, 0, 0}},
		{name: "four zero bytes", data: []byte{0, 0, 0, 0}},
		{name: "bad trailer", data: badTrailer},
		{name: "bad payload with valid-looking trailer", data: badPayload},
		{name: "extra byte after valid file", data: append(bytes.Clone(valid), 0)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Decode(tt.data)
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("Decode() = %v, want ErrCorrupt", err)
			}
		})
	}
}

func TestDecodeRejectsNonCanonicalOrMalformedPayload(t *testing.T) {
	payloads := []struct {
		name    string
		payload string
	}{
		{name: "empty payload", payload: ""},
		{name: "empty object", payload: "{}"},
		{name: "null", payload: "null"},
		{name: "array", payload: "[]"},
		{name: "not JSON", payload: "not json"},
		{name: "duplicate top-level field", payload: replaceOnce(t, emptyManifestPayload, `"format_version":1,`, `"format_version":1,"format_version":1,`)},
		{name: "unknown top-level field", payload: replaceOnce(t, emptyManifestPayload, "{", `{"extra":1,`)},
		{name: "missing field", payload: replaceOnce(t, emptyManifestPayload, `"next_sstable_id":1,`, "")},
		{name: "reordered fields", payload: replaceOnce(t, emptyManifestPayload, `"format_version":1,"manifest_version":1,`, `"manifest_version":1,"format_version":1,`)},
		{name: "space after brace", payload: replaceOnce(t, emptyManifestPayload, "{", "{ ")},
		{name: "trailing newline", payload: emptyManifestPayload + "\n"},
		{name: "trailing space", payload: emptyManifestPayload + " "},
		{name: "case-variant field name", payload: replaceOnce(t, emptyManifestPayload, `"format_version":1`, `"Format_Version":1`)},
		{name: "escaped field name", payload: replaceOnce(t, emptyManifestPayload, `"format_version":1`, `"\u0066ormat_version":1`)},
		{name: "fractional number", payload: replaceOnce(t, emptyManifestPayload, `"manifest_version":1,`, `"manifest_version":1.0,`)},
		{name: "exponent number", payload: replaceOnce(t, emptyManifestPayload, `"manifest_version":1,`, `"manifest_version":1e0,`)},
		{name: "plus number", payload: replaceOnce(t, emptyManifestPayload, `"manifest_version":1,`, `"manifest_version":+1,`)},
		{name: "leading zero", payload: replaceOnce(t, emptyManifestPayload, `"manifest_version":1,`, `"manifest_version":01,`)},
		{name: "negative number", payload: replaceOnce(t, emptyManifestPayload, `"manifest_version":1,`, `"manifest_version":-1,`)},
		{name: "number as string", payload: replaceOnce(t, emptyManifestPayload, `"manifest_version":1,`, `"manifest_version":"1",`)},
		{name: "null sstables", payload: replaceOnce(t, emptyManifestPayload, `"sstables":[]`, `"sstables":null`)},
		{name: "second JSON value", payload: emptyManifestPayload + `{}`},
		{name: "UTF-8 BOM", payload: "\xef\xbb\xbf" + emptyManifestPayload},
		{name: "duplicate nested field", payload: replaceOnce(t, oneSSTableManifestPayload, `"level":0,`, `"level":0,"level":0,`)},
		{name: "unknown nested field", payload: replaceOnce(t, oneSSTableManifestPayload, `"level":0,`, `"level":0,"extra":1,`)},
		{name: "nested field order", payload: replaceOnce(t, oneSSTableManifestPayload, `"level":0,"file_size":12345,`, `"file_size":12345,"level":0,`)},
		{name: "escaped filename dot", payload: replaceOnce(t, oneSSTableManifestPayload, `"sstable-000017.sst"`, `"sstable-000017\u002esst"`)},
		{name: "base64 without padding", payload: replaceOnce(t, oneSSTableManifestPayload, `"YQ=="`, `"YQ"`)},
		{name: "base64 with noncanonical trailing bits", payload: replaceOnce(t, oneSSTableManifestPayload, `"YQ=="`, `"YR=="`)},
		{name: "base64 with embedded newline escape", payload: replaceOnce(t, oneSSTableManifestPayload, `"YQ=="`, `"YQ==\n"`)},
		{name: "null key", payload: replaceOnce(t, oneSSTableManifestPayload, `"smallest_key_b64":"YQ=="`, `"smallest_key_b64":null`)},
		{name: "next ID not greater than table ID", payload: replaceOnce(t, oneSSTableManifestPayload, `"next_sstable_id":18`, `"next_sstable_id":17`)},
	}

	for _, tt := range payloads {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Decode(withTrailer([]byte(tt.payload)))
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("Decode() = %v, want ErrCorrupt", err)
			}
		})
	}
}

func TestEncodeRejectsInvalidManifest(t *testing.T) {
	tests := []struct {
		name string
		m    Manifest
	}{
		{name: "format version", m: Manifest{FormatVersion: 2, Version: 1, NextSSTableID: 1}},
		{name: "version zero", m: Manifest{FormatVersion: FormatVersion, Version: 0, NextSSTableID: 1}},
		{name: "next ID zero", m: Manifest{FormatVersion: FormatVersion, Version: 1, NextSSTableID: 0}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Encode(tt.m)
			if !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("Encode() = %v, want ErrInvalidManifest", err)
			}
		})
	}
}

func replaceOnce(t *testing.T, value, old, replacement string) string {
	t.Helper()

	if !strings.Contains(value, old) {
		t.Fatalf("test payload does not contain %q", old)
	}
	return strings.Replace(value, old, replacement, 1)
}

func withTrailer(payload []byte) []byte {
	result := make([]byte, len(payload)+4)
	copy(result, payload)
	crc := crc32.Checksum(payload, crc32.MakeTable(crc32.Castagnoli))
	binary.LittleEndian.PutUint32(result[len(payload):], crc)
	return result
}
