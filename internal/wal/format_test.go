package wal

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"strings"
	"testing"

	"github.com/vedantsahu06/raftkv/internal/kverr"
	"github.com/vedantsahu06/raftkv/internal/types"
)

func TestCRC32CCastagnoliVector(t *testing.T) {
	// This is the standard CRC32C check value for "123456789".
	if got := crc32.Checksum([]byte("123456789"), castagnoliTable); got != 0xe3069283 {
		t.Fatalf("CRC32C check value = %#08x, want %#08x", got, uint32(0xe3069283))
	}
}

func TestEncodeSegmentHeaderExactBytes(t *testing.T) {
	tests := []struct {
		id      types.SegmentID
		wantHex string
	}{
		{
			id:      1,
			wantHex: "524b574c01000000010000000000000010d9a662",
		},
		{
			id:      42,
			wantHex: "524b574c010000002a000000000000003a3fd9f6",
		},
	}

	for _, tt := range tests {
		header, err := EncodeSegmentHeader(tt.id)
		if err != nil {
			t.Fatalf("EncodeSegmentHeader(%d) = %v", tt.id, err)
		}
		want, err := hex.DecodeString(tt.wantHex)
		if err != nil {
			t.Fatalf("decode expected header: %v", err)
		}
		if !bytes.Equal(header, want) {
			t.Errorf("EncodeSegmentHeader(%d) = %x, want %x", tt.id, header, want)
		}

		gotID, err := DecodeSegmentHeader(header)
		if err != nil {
			t.Fatalf("DecodeSegmentHeader(%d header) = %v", tt.id, err)
		}
		if gotID != tt.id {
			t.Errorf("DecodeSegmentHeader() ID = %d, want %d", gotID, tt.id)
		}
	}
}

func TestEncodeSegmentHeaderRejectsZeroID(t *testing.T) {
	_, err := EncodeSegmentHeader(0)
	if !errors.Is(err, kverr.ErrInvalidArgument) {
		t.Fatalf("EncodeSegmentHeader(0) = %v, want ErrInvalidArgument", err)
	}
}

func TestDecodeSegmentHeaderMalformed(t *testing.T) {
	valid := mustEncodeSegmentHeader(t, 9)

	tests := []struct {
		name       string
		build      func() []byte
		wantPhrase string
	}{
		{
			name:       "empty",
			build:      func() []byte { return nil },
			wantPhrase: "header length",
		},
		{
			name:       "short",
			build:      func() []byte { return append([]byte(nil), valid[:len(valid)-1]...) },
			wantPhrase: "header length",
		},
		{
			name:       "long",
			build:      func() []byte { return append(append([]byte(nil), valid...), 0) },
			wantPhrase: "header length",
		},
		{
			name: "bad magic",
			build: func() []byte {
				header := append([]byte(nil), valid...)
				header[0] = 'X'
				refreshHeaderCRC(header)
				return header
			},
			wantPhrase: "magic",
		},
		{
			name: "unknown version",
			build: func() []byte {
				header := append([]byte(nil), valid...)
				binary.LittleEndian.PutUint16(header[segmentVersionOffset:], 2)
				refreshHeaderCRC(header)
				return header
			},
			wantPhrase: "version",
		},
		{
			name: "nonzero flags",
			build: func() []byte {
				header := append([]byte(nil), valid...)
				binary.LittleEndian.PutUint16(header[segmentFlagsOffset:], 1)
				refreshHeaderCRC(header)
				return header
			},
			wantPhrase: "flags",
		},
		{
			name: "zero ID",
			build: func() []byte {
				header := append([]byte(nil), valid...)
				binary.LittleEndian.PutUint64(header[segmentIDOffset:segmentCRCOffset], 0)
				refreshHeaderCRC(header)
				return header
			},
			wantPhrase: "segment ID",
		},
		{
			name: "bad checksum in covered ID byte",
			build: func() []byte {
				header := append([]byte(nil), valid...)
				header[segmentCRCOffset-1] ^= 0x01
				return header
			},
			wantPhrase: "checksum",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := tt.build()
			_, err := DecodeSegmentHeader(header)
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("DecodeSegmentHeader() = %v, want ErrCorrupt", err)
			}
			if !strings.Contains(err.Error(), tt.wantPhrase) {
				t.Errorf("DecodeSegmentHeader() error = %q, want phrase %q", err, tt.wantPhrase)
			}
		})
	}
}

func TestEncodeRecordExactBytes(t *testing.T) {
	tests := []struct {
		name    string
		record  Record
		wantHex string
	}{
		{
			name: "put",
			record: Record{
				Type:     types.EntryTypePut,
				Sequence: 1,
				Key:      []byte("k"),
				Value:    []byte("v"),
			},
			wantHex: "1e00000001000000010000000000000001000000010000006b76ce3b958d",
		},
		{
			name: "delete",
			record: Record{
				Type:     types.EntryTypeDelete,
				Sequence: 7,
				Key:      []byte("abc"),
			},
			wantHex: "1f000000020000000700000000000000030000000000000061626398c50b52",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frame, err := EncodeRecord(tt.record)
			if err != nil {
				t.Fatalf("EncodeRecord() = %v", err)
			}
			want, err := hex.DecodeString(tt.wantHex)
			if err != nil {
				t.Fatalf("decode expected record: %v", err)
			}
			if !bytes.Equal(frame, want) {
				t.Fatalf("EncodeRecord() = %x, want %x", frame, want)
			}

			decoded, err := DecodeRecord(frame)
			if err != nil {
				t.Fatalf("DecodeRecord() = %v", err)
			}
			if decoded.Type != tt.record.Type || decoded.Sequence != tt.record.Sequence {
				t.Errorf("DecodeRecord() type/sequence = %s/%d, want %s/%d", decoded.Type, decoded.Sequence, tt.record.Type, tt.record.Sequence)
			}
			if !bytes.Equal(decoded.Key, tt.record.Key) || !bytes.Equal(decoded.Value, tt.record.Value) {
				t.Errorf("DecodeRecord() key/value = %q/%q, want %q/%q", decoded.Key, decoded.Value, tt.record.Key, tt.record.Value)
			}
		})
	}
}

func TestRecordRoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		record Record
	}{
		{
			name: "put with empty value",
			record: Record{
				Type:     types.EntryTypePut,
				Sequence: 1,
				Key:      []byte("key"),
			},
		},
		{
			name: "put with binary bytes",
			record: Record{
				Type:     types.EntryTypePut,
				Sequence: 99,
				Key:      []byte{0x00, 0xff, 'k'},
				Value:    []byte{0x00, 0x01, 0xfe, 0xff},
			},
		},
		{
			name: "delete",
			record: Record{
				Type:     types.EntryTypeDelete,
				Sequence: 100,
				Key:      []byte("gone"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frame, err := EncodeRecord(tt.record)
			if err != nil {
				t.Fatalf("EncodeRecord() = %v", err)
			}
			if got, want := len(frame), types.WALRecordOverhead+len(tt.record.Key)+len(tt.record.Value); got != want {
				t.Fatalf("encoded length = %d, want %d", got, want)
			}

			decoded, err := DecodeRecord(frame)
			if err != nil {
				t.Fatalf("DecodeRecord() = %v", err)
			}
			if decoded.Type != tt.record.Type || decoded.Sequence != tt.record.Sequence {
				t.Errorf("decoded type/sequence = %s/%d, want %s/%d", decoded.Type, decoded.Sequence, tt.record.Type, tt.record.Sequence)
			}
			if !bytes.Equal(decoded.Key, tt.record.Key) {
				t.Errorf("decoded key = %x, want %x", decoded.Key, tt.record.Key)
			}
			if !bytes.Equal(decoded.Value, tt.record.Value) {
				t.Errorf("decoded value = %x, want %x", decoded.Value, tt.record.Value)
			}
		})
	}
}

func TestEncodeRecordBoundaries(t *testing.T) {
	t.Run("record exactly 16 MiB", func(t *testing.T) {
		key := bytes.Repeat([]byte("k"), types.MaxKeySize)
		maxValueLen, ok := types.MaxValueSizeForKey(len(key))
		if !ok {
			t.Fatal("MaxValueSizeForKey(MaxKeySize) unexpectedly invalid")
		}
		value := bytes.Repeat([]byte("v"), maxValueLen)

		frame, err := EncodeRecord(Record{
			Type:     types.EntryTypePut,
			Sequence: 1,
			Key:      key,
			Value:    value,
		})
		if err != nil {
			t.Fatalf("EncodeRecord(maximum record) = %v", err)
		}
		if len(frame) != types.MaxRecordSize {
			t.Fatalf("maximum encoded record length = %d, want %d", len(frame), types.MaxRecordSize)
		}
		decoded, err := DecodeRecord(frame)
		if err != nil {
			t.Fatalf("DecodeRecord(maximum record) = %v", err)
		}
		if len(decoded.Key) != len(key) || len(decoded.Value) != len(value) {
			t.Fatalf("decoded key/value lengths = %d/%d, want %d/%d", len(decoded.Key), len(decoded.Value), len(key), len(value))
		}
	})

	t.Run("record one byte over 16 MiB", func(t *testing.T) {
		maxValueLen, _ := types.MaxValueSizeForKey(1)
		_, err := EncodeRecord(Record{
			Type:     types.EntryTypePut,
			Sequence: 1,
			Key:      []byte("k"),
			Value:    make([]byte, maxValueLen+1),
		})
		if !errors.Is(err, kverr.ErrInvalidArgument) {
			t.Fatalf("EncodeRecord(oversized value) = %v, want ErrInvalidArgument", err)
		}
	})

	t.Run("key one byte over 1 MiB", func(t *testing.T) {
		_, err := EncodeRecord(Record{
			Type:     types.EntryTypePut,
			Sequence: 1,
			Key:      make([]byte, types.MaxKeySize+1),
		})
		if !errors.Is(err, kverr.ErrInvalidArgument) {
			t.Fatalf("EncodeRecord(oversized key) = %v, want ErrInvalidArgument", err)
		}
	})

	t.Run("key exactly 1 MiB", func(t *testing.T) {
		frame, err := EncodeRecord(Record{
			Type:     types.EntryTypePut,
			Sequence: 1,
			Key:      make([]byte, types.MaxKeySize),
		})
		if err != nil {
			t.Fatalf("EncodeRecord(maximum key) = %v", err)
		}
		if got, want := len(frame), types.WALRecordOverhead+types.MaxKeySize; got != want {
			t.Fatalf("encoded length = %d, want %d", got, want)
		}
	})

	t.Run("put with empty value", func(t *testing.T) {
		frame, err := EncodeRecord(Record{
			Type:     types.EntryTypePut,
			Sequence: 1,
			Key:      []byte("k"),
		})
		if err != nil {
			t.Fatalf("EncodeRecord() = %v", err)
		}
		if len(frame) != types.WALRecordOverhead+1 {
			t.Fatalf("encoded length = %d, want %d", len(frame), types.WALRecordOverhead+1)
		}
	})
}

func TestEncodeRecordRejectsInvalidFields(t *testing.T) {
	tests := []struct {
		name   string
		record Record
	}{
		{
			name:   "empty key",
			record: Record{Type: types.EntryTypePut, Sequence: 1},
		},
		{
			name: "zero sequence",
			record: Record{
				Type:  types.EntryTypePut,
				Key:   []byte("k"),
				Value: []byte("v"),
			},
		},
		{
			name: "unknown zero type",
			record: Record{
				Type:     0,
				Sequence: 1,
				Key:      []byte("k"),
			},
		},
		{
			name: "unknown type 3",
			record: Record{
				Type:     types.EntryType(3),
				Sequence: 1,
				Key:      []byte("k"),
			},
		},
		{
			name: "delete with value",
			record: Record{
				Type:     types.EntryTypeDelete,
				Sequence: 1,
				Key:      []byte("k"),
				Value:    []byte("v"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := EncodeRecord(tt.record)
			if !errors.Is(err, kverr.ErrInvalidArgument) {
				t.Fatalf("EncodeRecord() = %v, want ErrInvalidArgument", err)
			}
		})
	}
}

func TestDecodeRecordMalformed(t *testing.T) {
	valid := mustEncodeRecord(t, Record{
		Type:     types.EntryTypePut,
		Sequence: 1,
		Key:      []byte("k"),
		Value:    []byte("v"),
	})

	tests := []struct {
		name  string
		build func() []byte
	}{
		{
			name:  "empty",
			build: func() []byte { return nil },
		},
		{
			name:  "shorter than overhead",
			build: func() []byte { return append([]byte(nil), valid[:types.WALRecordOverhead-1]...) },
		},
		{
			name: "length field mismatch",
			build: func() []byte {
				frame := append([]byte(nil), valid...)
				binary.LittleEndian.PutUint32(frame[recordLengthOffset:], uint32(len(frame)+1))
				return frame
			},
		},
		{
			name: "length field overflow value",
			build: func() []byte {
				frame := append([]byte(nil), valid...)
				binary.LittleEndian.PutUint32(frame[recordLengthOffset:], ^uint32(0))
				return frame
			},
		},
		{
			name: "record over maximum size",
			build: func() []byte {
				frame := make([]byte, types.MaxRecordSize+1)
				binary.LittleEndian.PutUint32(frame[recordLengthOffset:], uint32(len(frame)))
				return frame
			},
		},
		{
			name: "unknown zero type",
			build: func() []byte {
				frame := append([]byte(nil), valid...)
				frame[recordTypeOffset] = 0
				refreshRecordCRC(frame)
				return frame
			},
		},
		{
			name: "unknown type 3",
			build: func() []byte {
				frame := append([]byte(nil), valid...)
				frame[recordTypeOffset] = 3
				refreshRecordCRC(frame)
				return frame
			},
		},
		{
			name: "nonzero flags",
			build: func() []byte {
				frame := append([]byte(nil), valid...)
				frame[recordFlagsOffset] = 1
				refreshRecordCRC(frame)
				return frame
			},
		},
		{
			name: "nonzero reserved",
			build: func() []byte {
				frame := append([]byte(nil), valid...)
				binary.LittleEndian.PutUint16(frame[recordReservedOffset:], 1)
				refreshRecordCRC(frame)
				return frame
			},
		},
		{
			name: "zero sequence",
			build: func() []byte {
				frame := append([]byte(nil), valid...)
				binary.LittleEndian.PutUint64(frame[recordSequenceOffset:], 0)
				refreshRecordCRC(frame)
				return frame
			},
		},
		{
			name: "empty key",
			build: func() []byte {
				return buildRawRecord(types.EntryTypePut, 1, 0, 1, []byte("v"))
			},
		},
		{
			name: "key over maximum size",
			build: func() []byte {
				return buildRawRecord(types.EntryTypePut, 1, types.MaxKeySize+1, 0, make([]byte, types.MaxKeySize+1))
			},
		},
		{
			name: "key and value lengths inconsistent with record length",
			build: func() []byte {
				frame := append([]byte(nil), valid...)
				binary.LittleEndian.PutUint32(frame[recordKeyLenOffset:recordValueLenOffset], 2)
				binary.LittleEndian.PutUint32(frame[recordValueLenOffset:recordKeyOffset], 1)
				refreshRecordCRC(frame)
				return frame
			},
		},
		{
			name: "overflowing key and value lengths",
			build: func() []byte {
				return buildRawRecord(types.EntryTypePut, 1, ^uint32(0), ^uint32(0), nil)
			},
		},
		{
			name: "delete with value",
			build: func() []byte {
				return buildRawRecord(types.EntryTypeDelete, 1, 1, 1, []byte("kv"))
			},
		},
		{
			name: "bad checksum",
			build: func() []byte {
				frame := append([]byte(nil), valid...)
				frame[len(frame)-1] ^= 0x01
				return frame
			},
		},
		{
			name: "trailing byte without length update",
			build: func() []byte {
				return append(append([]byte(nil), valid...), 0)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeRecord(tt.build())
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("DecodeRecord() = %v, want ErrCorrupt", err)
			}
		})
	}
}

func mustEncodeSegmentHeader(t *testing.T, id types.SegmentID) []byte {
	t.Helper()
	header, err := EncodeSegmentHeader(id)
	if err != nil {
		t.Fatalf("EncodeSegmentHeader(%d): %v", id, err)
	}
	return header
}

func refreshHeaderCRC(header []byte) {
	checksum := crc32.Checksum(header[:segmentCRCOffset], castagnoliTable)
	binary.LittleEndian.PutUint32(header[segmentCRCOffset:], checksum)
}

func mustEncodeRecord(t *testing.T, record Record) []byte {
	t.Helper()
	frame, err := EncodeRecord(record)
	if err != nil {
		t.Fatalf("EncodeRecord(%+v): %v", record, err)
	}
	return frame
}

// buildRawRecord constructs a syntactically framed record without applying the
// semantic length/type checks. Tests use it to reach validation branches that
// EncodeRecord would otherwise prevent.
func buildRawRecord(entryType types.EntryType, sequence types.Sequence, keyLen, valueLen uint32, payload []byte) []byte {
	frame := make([]byte, recordKeyOffset+len(payload)+4)
	binary.LittleEndian.PutUint32(frame[recordLengthOffset:recordTypeOffset], uint32(len(frame)))
	frame[recordTypeOffset] = byte(entryType)
	binary.LittleEndian.PutUint64(frame[recordSequenceOffset:recordKeyLenOffset], uint64(sequence))
	binary.LittleEndian.PutUint32(frame[recordKeyLenOffset:recordValueLenOffset], keyLen)
	binary.LittleEndian.PutUint32(frame[recordValueLenOffset:recordKeyOffset], valueLen)
	copy(frame[recordKeyOffset:recordKeyOffset+len(payload)], payload)
	refreshRecordCRC(frame)
	return frame
}

func refreshRecordCRC(frame []byte) {
	checksum := crc32.Checksum(frame[:len(frame)-4], castagnoliTable)
	binary.LittleEndian.PutUint32(frame[len(frame)-4:], checksum)
}
