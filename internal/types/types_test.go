package types

import "testing"

func TestEntryTypeValid(t *testing.T) {
	tests := []struct {
		entryType EntryType
		want      bool
	}{
		{0, false},
		{EntryTypePut, true},
		{EntryTypeDelete, true},
		{3, false},
	}
	for _, tt := range tests {
		if got := tt.entryType.Valid(); got != tt.want {
			t.Errorf("EntryType(%d).Valid() = %v, want %v", tt.entryType, got, tt.want)
		}
	}
}

func TestEntryTypeString(t *testing.T) {
	if got := EntryTypePut.String(); got != "put" {
		t.Errorf("EntryTypePut.String() = %q, want %q", got, "put")
	}
	if got := EntryTypeDelete.String(); got != "delete" {
		t.Errorf("EntryTypeDelete.String() = %q, want %q", got, "delete")
	}
	if got := EntryType(99).String(); got != "EntryType(99)" {
		t.Errorf("EntryType(99).String() = %q, want %q", got, "EntryType(99)")
	}
}

func TestFormatLimits(t *testing.T) {
	if WALSegmentHeaderSize != 20 {
		t.Errorf("WALSegmentHeaderSize = %d, want 20", WALSegmentHeaderSize)
	}
	if WALRecordOverhead != 28 {
		t.Errorf("WALRecordOverhead = %d, want 28", WALRecordOverhead)
	}
	if MaxRecordSize != 16<<20 {
		t.Errorf("MaxRecordSize = %d, want %d", MaxRecordSize, 16<<20)
	}
	if MaxKeySize != 1<<20 {
		t.Errorf("MaxKeySize = %d, want %d", MaxKeySize, 1<<20)
	}
}

func TestMaxValueSizeForKey(t *testing.T) {
	tests := []struct {
		keyLen int
		want   int
		wantOK bool
	}{
		{0, 0, false},
		{-1, 0, false},
		{1, MaxRecordSize - WALRecordOverhead - 1, true},
		{MaxKeySize, MaxRecordSize - WALRecordOverhead - MaxKeySize, true},
		{MaxKeySize + 1, 0, false},
	}
	for _, tt := range tests {
		got, ok := MaxValueSizeForKey(tt.keyLen)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("MaxValueSizeForKey(%d) = (%d, %v), want (%d, %v)", tt.keyLen, got, ok, tt.want, tt.wantOK)
		}
	}
}
