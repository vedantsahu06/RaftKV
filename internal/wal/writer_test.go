package wal

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/vedantsahu06/raftkv/internal/filesystem"
	"github.com/vedantsahu06/raftkv/internal/kverr"
	"github.com/vedantsahu06/raftkv/internal/types"
)

const testWaitTimeout = 2 * time.Second

// controlledFile wraps the real segment file and lets tests observe and
// control batch writes, Sync calls and Close failures.
type controlledFile struct {
	filesystem.File

	mu sync.Mutex

	writeErr   error
	syncErr    error
	closeErr   error
	shortWrite bool

	syncRelease <-chan struct{}
	syncStarted chan struct{}

	writes    [][]byte
	syncs     int
	seeks     int
	truncates int
	closes    int
}

var _ filesystem.File = (*controlledFile)(nil)

func newControlledFile(file filesystem.File) *controlledFile {
	return &controlledFile{
		File:        file,
		syncStarted: make(chan struct{}, 1),
	}
}

func (f *controlledFile) Write(p []byte) (int, error) {
	f.mu.Lock()
	f.writes = append(f.writes, append([]byte(nil), p...))
	writeErr := f.writeErr
	shortWrite := f.shortWrite
	f.mu.Unlock()

	if writeErr != nil {
		return 0, writeErr
	}
	if shortWrite {
		if len(p) == 0 {
			return 0, nil
		}
		return len(p) - 1, nil
	}
	return f.File.Write(p)
}

func (f *controlledFile) Sync() error {
	f.mu.Lock()
	f.syncs++
	syncErr := f.syncErr
	release := f.syncRelease
	f.mu.Unlock()

	select {
	case f.syncStarted <- struct{}{}:
	default:
	}

	if release != nil {
		<-release
	}
	if syncErr != nil {
		return syncErr
	}
	return f.File.Sync()
}

func (f *controlledFile) Close() error {
	err := f.File.Close()

	f.mu.Lock()
	f.closes++
	closeErr := f.closeErr
	f.mu.Unlock()

	if closeErr != nil {
		return closeErr
	}
	return err
}

func (f *controlledFile) Seek(offset int64, whence int) (int64, error) {
	f.mu.Lock()
	f.seeks++
	f.mu.Unlock()
	return f.File.Seek(offset, whence)
}

func (f *controlledFile) Truncate(size int64) error {
	f.mu.Lock()
	f.truncates++
	f.mu.Unlock()
	return f.File.Truncate(size)
}

func (f *controlledFile) setWriteErr(err error) {
	f.mu.Lock()
	f.writeErr = err
	f.mu.Unlock()
}

func (f *controlledFile) setSyncErr(err error) {
	f.mu.Lock()
	f.syncErr = err
	f.mu.Unlock()
}

func (f *controlledFile) setCloseErr(err error) {
	f.mu.Lock()
	f.closeErr = err
	f.mu.Unlock()
}

func (f *controlledFile) setShortWrite(short bool) {
	f.mu.Lock()
	f.shortWrite = short
	f.mu.Unlock()
}

func (f *controlledFile) blockSync(release <-chan struct{}) {
	f.mu.Lock()
	f.syncRelease = release
	f.mu.Unlock()
}

type controlledFileSnapshot struct {
	writes    [][]byte
	syncs     int
	seeks     int
	truncates int
	closes    int
}

func (f *controlledFile) snapshot() controlledFileSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()

	writes := make([][]byte, len(f.writes))
	for i, write := range f.writes {
		writes[i] = append([]byte(nil), write...)
	}
	return controlledFileSnapshot{
		writes:    writes,
		syncs:     f.syncs,
		seeks:     f.seeks,
		truncates: f.truncates,
		closes:    f.closes,
	}
}

type writerHarness struct {
	fsys    filesystem.FS
	dir     string
	segment *Segment
	control *controlledFile
	writer  *Writer

	syncRelease chan struct{}
	releaseOnce sync.Once
}

func newWriterHarness(t *testing.T, config WriterConfig) *writerHarness {
	t.Helper()

	fsys, dir := newTestWALDir(t)
	segment, err := CreateSegment(fsys, dir, 1)
	if err != nil {
		t.Fatalf("CreateSegment() = %v", err)
	}

	control := newControlledFile(segment.file)
	segment.file = control

	writer, err := NewWriter(segment, config)
	if err != nil {
		control.Close()
		t.Fatalf("NewWriter() = %v", err)
	}

	harness := &writerHarness{
		fsys:        fsys,
		dir:         dir,
		segment:     segment,
		control:     control,
		writer:      writer,
		syncRelease: make(chan struct{}),
	}

	t.Cleanup(func() {
		harness.releaseSync()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Tests assert Close behavior explicitly. Cleanup only prevents a
		// leaked goroutine or file handle.
		_ = harness.writer.Close(ctx)
	})

	return harness
}

func (h *writerHarness) blockSync() {
	h.control.blockSync(h.syncRelease)
}

func (h *writerHarness) releaseSync() {
	h.releaseOnce.Do(func() {
		close(h.syncRelease)
	})
}

func (h *writerHarness) waitSyncStarted(t *testing.T) {
	t.Helper()

	select {
	case <-h.control.syncStarted:
	case <-time.After(testWaitTimeout):
		t.Fatal("timed out waiting for WAL Sync to start")
	}
}

func (h *writerHarness) waitQueued(t *testing.T, count int) {
	t.Helper()

	waitForCondition(t, func() bool {
		return len(h.writer.requests) == count
	}, func() string {
		return "WAL queue length did not reach the expected value"
	})
}

func (h *writerHarness) waitDone(t *testing.T) {
	t.Helper()

	select {
	case <-h.writer.done:
	case <-time.After(testWaitTimeout):
		t.Fatal("timed out waiting for WAL writer to stop")
	}
}

func waitForCondition(t *testing.T, condition func() bool, message func() string) {
	t.Helper()

	deadline := time.Now().Add(testWaitTimeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal(message())
}

func readTestRecords(t *testing.T, data []byte) []Record {
	t.Helper()

	if len(data) < types.WALSegmentHeaderSize {
		t.Fatalf("WAL file length %d is smaller than the segment header", len(data))
	}
	if _, err := DecodeSegmentHeader(data[:types.WALSegmentHeaderSize]); err != nil {
		t.Fatalf("DecodeSegmentHeader() = %v", err)
	}
	return decodeRecordFrames(t, data[types.WALSegmentHeaderSize:])
}

func decodeRecordFrames(t *testing.T, data []byte) []Record {
	t.Helper()

	var records []Record
	offset := 0
	for offset < len(data) {
		if len(data)-offset < 4 {
			t.Fatalf("WAL file has %d trailing bytes, want a record length", len(data)-offset)
		}
		recordLen := int(binary.LittleEndian.Uint32(data[offset : offset+4]))
		if recordLen < types.WALRecordOverhead || offset+recordLen > len(data) {
			t.Fatalf("invalid record length %d at offset %d of %d", recordLen, offset, len(data))
		}
		record, err := DecodeRecord(data[offset : offset+recordLen])
		if err != nil {
			t.Fatalf("DecodeRecord() at offset %d = %v", offset, err)
		}
		records = append(records, Record{
			Type:     record.Type,
			Sequence: record.Sequence,
			Key:      append([]byte(nil), record.Key...),
			Value:    append([]byte(nil), record.Value...),
		})
		offset += recordLen
	}
	return records
}

func readHarnessRecords(t *testing.T, harness *writerHarness) []Record {
	t.Helper()

	data := readTestFile(t, harness.fsys, harness.segment.Path())
	return readTestRecords(t, data)
}

func basicWriterConfig() WriterConfig {
	return WriterConfig{
		QueueCapacity:       16,
		MaxBatchRecords:     32,
		BatchByteTarget:     1 << 20,
		GroupCommitInterval: time.Millisecond,
	}
}

type appendOutcome struct {
	sequence types.Sequence
	err      error
}

func appendAsync(ctx context.Context, writer *Writer, entryType types.EntryType, key, value []byte) <-chan appendOutcome {
	result := make(chan appendOutcome, 1)
	go func() {
		sequence, err := writer.Append(ctx, entryType, key, value)
		result <- appendOutcome{sequence: sequence, err: err}
	}()
	return result
}

func waitOutcome(t *testing.T, result <-chan appendOutcome) appendOutcome {
	t.Helper()

	select {
	case outcome := <-result:
		return outcome
	case <-time.After(testWaitTimeout):
		t.Fatal("timed out waiting for Append")
		return appendOutcome{}
	}
}

func TestWriterStateString(t *testing.T) {
	tests := []struct {
		state WriterState
		want  string
	}{
		{WriterOpen, "open"},
		{WriterClosing, "closing"},
		{WriterFailed, "failed"},
		{WriterClosed, "closed"},
		{WriterState(99), "WriterState(99)"},
	}
	for _, test := range tests {
		if got := test.state.String(); got != test.want {
			t.Errorf("WriterState(%d).String() = %q, want %q", uint8(test.state), got, test.want)
		}
	}
}

func TestNewWriterRejectsInvalidInputs(t *testing.T) {
	if _, err := NewWriter(nil, basicWriterConfig()); !errors.Is(err, kverr.ErrInvalidArgument) {
		t.Fatalf("NewWriter(nil) = %v, want ErrInvalidArgument", err)
	}

	fsys, dir := newTestWALDir(t)
	segment, err := CreateSegment(fsys, dir, 1)
	if err != nil {
		t.Fatalf("CreateSegment() = %v", err)
	}
	t.Cleanup(func() {
		if err := segment.Seal(); err != nil {
			t.Errorf("Seal() = %v", err)
		}
	})

	tests := []struct {
		name   string
		change func(*WriterConfig)
	}{
		{"queue capacity", func(c *WriterConfig) { c.QueueCapacity = 0 }},
		{"batch records", func(c *WriterConfig) { c.MaxBatchRecords = 0 }},
		{"byte target", func(c *WriterConfig) { c.BatchByteTarget = 0 }},
		{"interval", func(c *WriterConfig) { c.GroupCommitInterval = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := basicWriterConfig()
			test.change(&config)
			if _, err := NewWriter(segment, config); !errors.Is(err, kverr.ErrInvalidArgument) {
				t.Fatalf("NewWriter() = %v, want ErrInvalidArgument", err)
			}
		})
	}

	sealed, err := CreateSegment(fsys, dir, 2)
	if err != nil {
		t.Fatalf("CreateSegment(2) = %v", err)
	}
	if err := sealed.Seal(); err != nil {
		t.Fatalf("Seal() = %v", err)
	}
	if _, err := NewWriter(sealed, basicWriterConfig()); !errors.Is(err, kverr.ErrInvalidArgument) {
		t.Fatalf("NewWriter(sealed) = %v, want ErrInvalidArgument", err)
	}
}

func TestWriterAppendUsesOneWriteAndOneSync(t *testing.T) {
	harness := newWriterHarness(t, basicWriterConfig())

	sequence, err := harness.writer.Append(context.Background(), types.EntryTypePut, []byte("key"), []byte("value"))
	if err != nil {
		t.Fatalf("Append() = %v", err)
	}
	if sequence != 1 {
		t.Fatalf("Append() sequence = %d, want 1", sequence)
	}
	if harness.writer.State() != WriterOpen {
		t.Fatalf("writer state = %s, want open", harness.writer.State())
	}

	snapshot := harness.control.snapshot()
	if len(snapshot.writes) != 1 {
		t.Fatalf("Write calls = %d, want 1", len(snapshot.writes))
	}
	if snapshot.syncs != 1 {
		t.Fatalf("Sync calls = %d, want 1", snapshot.syncs)
	}

	records := decodeRecordFrames(t, snapshot.writes[0])
	if len(records) != 1 {
		t.Fatalf("records in batch = %d, want 1", len(records))
	}
	record := records[0]
	if record.Sequence != 1 || record.Type != types.EntryTypePut ||
		!bytes.Equal(record.Key, []byte("key")) || !bytes.Equal(record.Value, []byte("value")) {
		t.Fatalf("record = %+v, want put sequence 1 key/value", record)
	}
}

func TestWriterAppendCompletesOnlyAfterSync(t *testing.T) {
	harness := newWriterHarness(t, basicWriterConfig())
	harness.blockSync()

	result := appendAsync(context.Background(), harness.writer, types.EntryTypePut, []byte("k"), []byte("v"))
	harness.waitSyncStarted(t)

	select {
	case outcome := <-result:
		t.Fatalf("Append completed before Sync: %+v", outcome)
	default:
	}

	harness.releaseSync()
	outcome := waitOutcome(t, result)
	if outcome.err != nil {
		t.Fatalf("Append() = %v", outcome.err)
	}
	if outcome.sequence != 1 {
		t.Fatalf("Append() sequence = %d, want 1", outcome.sequence)
	}
}

func TestWriterRejectsInvalidRecordBeforeAdmission(t *testing.T) {
	harness := newWriterHarness(t, basicWriterConfig())

	tests := []struct {
		name      string
		entryType types.EntryType
		key       []byte
		value     []byte
	}{
		{"unknown type", types.EntryType(9), []byte("k"), []byte("v")},
		{"empty key", types.EntryTypePut, nil, []byte("v")},
		{"delete value", types.EntryTypeDelete, []byte("k"), []byte("v")},
		{"oversized key", types.EntryTypePut, make([]byte, types.MaxKeySize+1), nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sequence, err := harness.writer.Append(context.Background(), test.entryType, test.key, test.value)
			if !errors.Is(err, kverr.ErrInvalidArgument) {
				t.Fatalf("Append() = %v, want ErrInvalidArgument", err)
			}
			if sequence != 0 {
				t.Fatalf("Append() sequence = %d, want 0", sequence)
			}
		})
	}

	if harness.writer.State() != WriterOpen {
		t.Fatalf("writer state = %s, want open", harness.writer.State())
	}
	if snapshot := harness.control.snapshot(); len(snapshot.writes) != 0 || snapshot.syncs != 0 {
		t.Fatalf("invalid records caused I/O: %+v", snapshot)
	}

	sequence, err := harness.writer.Append(context.Background(), types.EntryTypePut, []byte("valid"), nil)
	if err != nil {
		t.Fatalf("valid Append() = %v", err)
	}
	if sequence != 1 {
		t.Fatalf("valid Append() sequence = %d, want 1 (invalid records must not consume a sequence)", sequence)
	}
}

func TestWriterRejectsOversizedRecordBeforeAdmission(t *testing.T) {
	harness := newWriterHarness(t, basicWriterConfig())

	value := make([]byte, types.MaxRecordSize)
	sequence, err := harness.writer.Append(context.Background(), types.EntryTypePut, []byte("k"), value)
	if !errors.Is(err, kverr.ErrInvalidArgument) {
		t.Fatalf("Append(oversized) = %v, want ErrInvalidArgument", err)
	}
	if sequence != 0 {
		t.Fatalf("Append(oversized) sequence = %d, want 0", sequence)
	}
	if snapshot := harness.control.snapshot(); len(snapshot.writes) != 0 || snapshot.syncs != 0 {
		t.Fatalf("oversized record caused I/O: %+v", snapshot)
	}

	sequence, err = harness.writer.Append(context.Background(), types.EntryTypePut, []byte("k"), []byte("v"))
	if err != nil {
		t.Fatalf("valid Append() = %v", err)
	}
	if sequence != 1 {
		t.Fatalf("valid Append() sequence = %d, want 1", sequence)
	}
}

func TestWriterRejectsNilContext(t *testing.T) {
	harness := newWriterHarness(t, basicWriterConfig())

	sequence, err := harness.writer.Append(nil, types.EntryTypePut, []byte("k"), []byte("v"))
	if !errors.Is(err, kverr.ErrInvalidArgument) {
		t.Fatalf("Append(nil) = %v, want ErrInvalidArgument", err)
	}
	if sequence != 0 {
		t.Fatalf("Append(nil) sequence = %d, want 0", sequence)
	}

	if err := harness.writer.Close(nil); !errors.Is(err, kverr.ErrInvalidArgument) {
		t.Fatalf("Close(nil) = %v, want ErrInvalidArgument", err)
	}
}

func TestWriterCanceledContextBeforeAdmission(t *testing.T) {
	harness := newWriterHarness(t, basicWriterConfig())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	sequence, err := harness.writer.Append(ctx, types.EntryTypePut, []byte("k"), []byte("v"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Append(canceled) = %v, want context.Canceled", err)
	}
	if sequence != 0 {
		t.Fatalf("Append(canceled) sequence = %d, want 0", sequence)
	}

	sequence, err = harness.writer.Append(context.Background(), types.EntryTypePut, []byte("k"), []byte("v"))
	if err != nil {
		t.Fatalf("next Append() = %v", err)
	}
	if sequence != 1 {
		t.Fatalf("next Append() sequence = %d, want 1", sequence)
	}
}

func TestWriterIdleClose(t *testing.T) {
	harness := newWriterHarness(t, basicWriterConfig())

	if err := harness.writer.Close(context.Background()); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if harness.writer.State() != WriterClosed {
		t.Fatalf("writer state = %s, want closed", harness.writer.State())
	}
	if harness.segment.State() != SegmentSealed {
		t.Fatalf("segment state = %s, want sealed", harness.segment.State())
	}

	_, err := harness.writer.Append(context.Background(), types.EntryTypePut, []byte("k"), []byte("v"))
	if !errors.Is(err, kverr.ErrClosed) {
		t.Fatalf("Append(after Close) = %v, want ErrClosed", err)
	}

	snapshot := harness.control.snapshot()
	if len(snapshot.writes) != 0 || snapshot.syncs != 0 {
		t.Fatalf("idle close caused I/O: %+v", snapshot)
	}
	if snapshot.closes != 1 {
		t.Fatalf("Close calls on segment = %d, want 1", snapshot.closes)
	}
}

func TestWriterConcurrentSequenceOrder(t *testing.T) {
	config := basicWriterConfig()
	config.QueueCapacity = 8
	config.MaxBatchRecords = 7
	config.BatchByteTarget = 4 << 10
	config.GroupCommitInterval = 200 * time.Microsecond
	harness := newWriterHarness(t, config)

	const writers = 8
	const perWriter = 25

	results := make(chan appendOutcome, writers*perWriter)
	var group sync.WaitGroup
	for worker := 0; worker < writers; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for i := 0; i < perWriter; i++ {
				sequence, err := harness.writer.Append(
					context.Background(),
					types.EntryTypePut,
					[]byte("shared-key"),
					[]byte("value"),
				)
				results <- appendOutcome{sequence: sequence, err: err}
			}
		}()
	}
	group.Wait()
	close(results)

	seen := make(map[types.Sequence]bool, writers*perWriter)
	for outcome := range results {
		if outcome.err != nil {
			t.Fatalf("Append() = %v", outcome.err)
		}
		if outcome.sequence == 0 {
			t.Fatal("Append() returned sequence 0")
		}
		if seen[outcome.sequence] {
			t.Fatalf("duplicate sequence %d", outcome.sequence)
		}
		seen[outcome.sequence] = true
	}
	if len(seen) != writers*perWriter {
		t.Fatalf("unique sequences = %d, want %d", len(seen), writers*perWriter)
	}

	if err := harness.writer.Close(context.Background()); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	records := readHarnessRecords(t, harness)
	if len(records) != writers*perWriter {
		t.Fatalf("records on disk = %d, want %d", len(records), writers*perWriter)
	}
	for i, record := range records {
		want := types.Sequence(i + 1)
		if record.Sequence != want {
			t.Fatalf("record %d sequence = %d, want %d", i, record.Sequence, want)
		}
	}
}

func TestWriterCloseErrorIsReportedWithoutFailStop(t *testing.T) {
	harness := newWriterHarness(t, basicWriterConfig())
	closeErr := errors.New("close failed")
	harness.control.setCloseErr(closeErr)

	if err := harness.writer.Close(context.Background()); !errors.Is(err, closeErr) {
		t.Fatalf("Close() = %v, want close error", err)
	}
	if harness.writer.State() != WriterClosed {
		t.Fatalf("writer state = %s, want closed (close failure is cleanup, not write failure)", harness.writer.State())
	}
}
