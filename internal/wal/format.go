package wal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"

	"github.com/vedantsahu06/raftkv/internal/kverr"
	"github.com/vedantsahu06/raftkv/internal/types"
)

// ErrCorrupt reports malformed WAL on-disk data. A short read during recovery is
// a separate incomplete-tail condition and must be classified by the caller
// before this error is treated as fatal corruption.
var ErrCorrupt = errors.New("raftkv: corrupt WAL data")

const (
	segmentMagic = "RKWL"

	segmentMagicOffset   = 0
	segmentVersionOffset = 4
	segmentFlagsOffset   = 6
	segmentIDOffset      = 8
	segmentCRCOffset     = 16

	segmentFormatVersion uint16 = 1
	segmentFlagsV1       uint16 = 0

	recordLengthOffset   = 0
	recordTypeOffset     = 4
	recordFlagsOffset    = 5
	recordReservedOffset = 6
	recordSequenceOffset = 8
	recordKeyLenOffset   = 16
	recordValueLenOffset = 20
	recordKeyOffset      = 24
)

// castagnoliTable is the CRC32C table required by the WAL format contract.
var castagnoliTable = crc32.MakeTable(crc32.Castagnoli)

// EncodeSegmentHeader returns the exact 20-byte v1 segment header for id.
//
// Segment IDs start at 1. The returned bytes include the CRC32C over bytes 0-15.
func EncodeSegmentHeader(id types.SegmentID) ([]byte, error) {
	if id == 0 {
		return nil, fmt.Errorf("%w: WAL segment ID must be at least 1", kverr.ErrInvalidArgument)
	}

	header := make([]byte, types.WALSegmentHeaderSize)
	copy(header[segmentMagicOffset:segmentVersionOffset], segmentMagic)
	binary.LittleEndian.PutUint16(header[segmentVersionOffset:segmentFlagsOffset], segmentFormatVersion)
	binary.LittleEndian.PutUint16(header[segmentFlagsOffset:segmentIDOffset], segmentFlagsV1)
	binary.LittleEndian.PutUint64(header[segmentIDOffset:segmentCRCOffset], uint64(id))

	checksum := crc32.Checksum(header[:segmentCRCOffset], castagnoliTable)
	binary.LittleEndian.PutUint32(header[segmentCRCOffset:], checksum)
	return header, nil
}

// DecodeSegmentHeader validates an exact 20-byte v1 segment header and returns
// its segment ID. Unknown versions, nonzero flags, an invalid ID and a checksum
// mismatch are all rejected. The header's ID must be compared with the filename
// by the caller.
func DecodeSegmentHeader(header []byte) (types.SegmentID, error) {
	if len(header) != types.WALSegmentHeaderSize {
		return 0, fmt.Errorf("%w: segment header length %d, want %d", ErrCorrupt, len(header), types.WALSegmentHeaderSize)
	}
	if string(header[segmentMagicOffset:segmentVersionOffset]) != segmentMagic {
		return 0, fmt.Errorf("%w: segment header magic %q, want %q", ErrCorrupt, header[segmentMagicOffset:segmentVersionOffset], segmentMagic)
	}

	version := binary.LittleEndian.Uint16(header[segmentVersionOffset:segmentFlagsOffset])
	if version != segmentFormatVersion {
		return 0, fmt.Errorf("%w: unsupported segment format version %d", ErrCorrupt, version)
	}
	flags := binary.LittleEndian.Uint16(header[segmentFlagsOffset:segmentIDOffset])
	if flags != segmentFlagsV1 {
		return 0, fmt.Errorf("%w: unsupported segment flags %#x", ErrCorrupt, flags)
	}

	id := types.SegmentID(binary.LittleEndian.Uint64(header[segmentIDOffset:segmentCRCOffset]))
	if id == 0 {
		return 0, fmt.Errorf("%w: segment ID must be at least 1", ErrCorrupt)
	}

	wantCRC := binary.LittleEndian.Uint32(header[segmentCRCOffset:])
	gotCRC := crc32.Checksum(header[:segmentCRCOffset], castagnoliTable)
	if gotCRC != wantCRC {
		return 0, fmt.Errorf("%w: segment header checksum %#08x, want %#08x", ErrCorrupt, gotCRC, wantCRC)
	}
	return id, nil
}

// Record is one decoded or to-be-encoded WAL record.
//
// Key and Value are byte slices. EncodeRecord does not retain them. DecodeRecord
// returns slices that alias its frame argument, so the caller must not mutate
// the frame while using the record.
type Record struct {
	Type     types.EntryType
	Sequence types.Sequence
	Key      []byte
	Value    []byte
}

// EncodeRecord returns one complete WAL record frame, including its trailing
// CRC32C. The frame is never split; its length is 28 + len(Key) + len(Value).
func EncodeRecord(record Record) ([]byte, error) {
	if err := validateRecord(record); err != nil {
		return nil, err
	}

	recordLen := types.WALRecordOverhead + len(record.Key) + len(record.Value)
	frame := make([]byte, recordLen)
	binary.LittleEndian.PutUint32(frame[recordLengthOffset:recordTypeOffset], uint32(recordLen))
	frame[recordTypeOffset] = byte(record.Type)
	// Flags and reserved bytes remain zero.
	binary.LittleEndian.PutUint64(frame[recordSequenceOffset:recordKeyLenOffset], uint64(record.Sequence))
	binary.LittleEndian.PutUint32(frame[recordKeyLenOffset:recordValueLenOffset], uint32(len(record.Key)))
	binary.LittleEndian.PutUint32(frame[recordValueLenOffset:recordKeyOffset], uint32(len(record.Value)))

	keyEnd := recordKeyOffset + len(record.Key)
	copy(frame[recordKeyOffset:keyEnd], record.Key)
	copy(frame[keyEnd:recordLen-4], record.Value)

	checksum := crc32.Checksum(frame[:recordLen-4], castagnoliTable)
	binary.LittleEndian.PutUint32(frame[recordLen-4:], checksum)
	return frame, nil
}

// DecodeRecord validates and decodes one complete WAL record frame.
//
// All length fields are validated with uint64 arithmetic before any slice is
// taken from the frame. The decoder never allocates storage based on a claimed
// record length; callers that read from disk must apply the same bound before
// allocating a frame.
func DecodeRecord(frame []byte) (Record, error) {
	if len(frame) < types.WALRecordOverhead {
		return Record{}, fmt.Errorf("%w: record frame length %d is smaller than the %d-byte overhead", ErrCorrupt, len(frame), types.WALRecordOverhead)
	}

	recordLen := uint64(binary.LittleEndian.Uint32(frame[recordLengthOffset:recordTypeOffset]))
	if recordLen != uint64(len(frame)) {
		return Record{}, fmt.Errorf("%w: record length field %d, actual frame length %d", ErrCorrupt, recordLen, len(frame))
	}
	if recordLen > uint64(types.MaxRecordSize) {
		return Record{}, fmt.Errorf("%w: record length %d exceeds maximum %d", ErrCorrupt, recordLen, types.MaxRecordSize)
	}

	entryType := types.EntryType(frame[recordTypeOffset])
	flags := frame[recordFlagsOffset]
	reserved := binary.LittleEndian.Uint16(frame[recordReservedOffset:recordSequenceOffset])
	sequence := types.Sequence(binary.LittleEndian.Uint64(frame[recordSequenceOffset:recordKeyLenOffset]))
	keyLen := uint64(binary.LittleEndian.Uint32(frame[recordKeyLenOffset:recordValueLenOffset]))
	valueLen := uint64(binary.LittleEndian.Uint32(frame[recordValueLenOffset:recordKeyOffset]))

	expectedLen := uint64(types.WALRecordOverhead) + keyLen + valueLen
	if expectedLen != recordLen {
		return Record{}, fmt.Errorf("%w: key length %d plus value length %d produce record length %d, field says %d", ErrCorrupt, keyLen, valueLen, expectedLen, recordLen)
	}

	if !entryType.Valid() {
		return Record{}, fmt.Errorf("%w: unknown record type %d", ErrCorrupt, uint8(entryType))
	}
	if flags != 0 {
		return Record{}, fmt.Errorf("%w: unsupported record flags %#x", ErrCorrupt, flags)
	}
	if reserved != 0 {
		return Record{}, fmt.Errorf("%w: reserved record field is nonzero (%#x)", ErrCorrupt, reserved)
	}
	if sequence == 0 {
		return Record{}, fmt.Errorf("%w: sequence number must be at least 1", ErrCorrupt)
	}
	if keyLen == 0 {
		return Record{}, fmt.Errorf("%w: key must not be empty", ErrCorrupt)
	}
	if keyLen > uint64(types.MaxKeySize) {
		return Record{}, fmt.Errorf("%w: key length %d exceeds maximum %d", ErrCorrupt, keyLen, types.MaxKeySize)
	}

	maxValueLen := uint64(types.MaxRecordSize - types.WALRecordOverhead - int(keyLen))
	if valueLen > maxValueLen {
		return Record{}, fmt.Errorf("%w: value length %d exceeds maximum %d for a %d-byte key", ErrCorrupt, valueLen, maxValueLen, keyLen)
	}
	if entryType == types.EntryTypeDelete && valueLen != 0 {
		return Record{}, fmt.Errorf("%w: delete record must have an empty value, got %d bytes", ErrCorrupt, valueLen)
	}

	wantCRC := binary.LittleEndian.Uint32(frame[len(frame)-4:])
	gotCRC := crc32.Checksum(frame[:len(frame)-4], castagnoliTable)
	if gotCRC != wantCRC {
		return Record{}, fmt.Errorf("%w: record checksum %#08x, want %#08x", ErrCorrupt, gotCRC, wantCRC)
	}

	keyEnd := recordKeyOffset + int(keyLen)
	valueEnd := keyEnd + int(valueLen)
	return Record{
		Type:     entryType,
		Sequence: sequence,
		Key:      frame[recordKeyOffset:keyEnd],
		Value:    frame[keyEnd:valueEnd],
	}, nil
}

func validateRecord(record Record) error {
	if !record.Type.Valid() {
		return fmt.Errorf("%w: unknown record type %d", kverr.ErrInvalidArgument, uint8(record.Type))
	}
	if record.Sequence == 0 {
		return fmt.Errorf("%w: sequence number must be at least 1", kverr.ErrInvalidArgument)
	}
	if len(record.Key) == 0 {
		return fmt.Errorf("%w: key must not be empty", kverr.ErrInvalidArgument)
	}
	if len(record.Key) > types.MaxKeySize {
		return fmt.Errorf("%w: key length %d exceeds maximum %d", kverr.ErrInvalidArgument, len(record.Key), types.MaxKeySize)
	}

	maxValueLen, ok := types.MaxValueSizeForKey(len(record.Key))
	if !ok {
		return fmt.Errorf("%w: key length %d is outside the valid range", kverr.ErrInvalidArgument, len(record.Key))
	}
	if len(record.Value) > maxValueLen {
		return fmt.Errorf("%w: value length %d exceeds maximum %d for a %d-byte key", kverr.ErrInvalidArgument, len(record.Value), maxValueLen, len(record.Key))
	}
	if record.Type == types.EntryTypeDelete && len(record.Value) != 0 {
		return fmt.Errorf("%w: delete record must have an empty value, got %d bytes", kverr.ErrInvalidArgument, len(record.Value))
	}
	return nil
}
