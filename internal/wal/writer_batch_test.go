package wal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vedantsahu06/raftkv/internal/kverr"
	"github.com/vedantsahu06/raftkv/internal/types"
)

func TestWriterBatchRecordLimitStopsCollection(t *testing.T) {
	config := WriterConfig{
		QueueCapacity:       8,
		MaxBatchRecords:     3,
		BatchByteTarget:     1 << 20,
		GroupCommitInterval: 10 * time.Second,
	}
	harness := newWriterHarness(t, config)
	harness.blockSync()

	results := make([]<-chan appendOutcome, 0, 9)
	for i := 0; i < 3; i++ {
		value := []byte{byte('0' + i)}
		results = append(results, appendAsync(context.Background(), harness.writer, types.EntryTypePut, []byte("k"), value))
	}
	harness.waitSyncStarted(t)

	for i := 3; i < 9; i++ {
		value := []byte{byte('0' + i)}
		results = append(results, appendAsync(context.Background(), harness.writer, types.EntryTypePut, []byte("k"), value))
	}
	harness.waitQueued(t, 6)
	harness.releaseSync()

	seen := make(map[types.Sequence]bool, len(results))
	for i, result := range results {
		outcome := waitOutcome(t, result)
		if outcome.err != nil {
			t.Fatalf("Append %d = %v", i, outcome.err)
		}
		if seen[outcome.sequence] {
			t.Fatalf("duplicate sequence %d", outcome.sequence)
		}
		seen[outcome.sequence] = true
	}

	snapshot := harness.control.snapshot()
	if snapshot.syncs != 3 {
		t.Fatalf("Sync calls = %d, want 3 (one for each batch)", snapshot.syncs)
	}
	wantBatchSizes := []int{3, 3, 3}
	for i, wantSize := range wantBatchSizes {
		records := decodeRecordFrames(t, snapshot.writes[i])
		if len(records) != wantSize {
			t.Fatalf("batch %d records = %d, want %d", i, len(records), wantSize)
		}
	}

	var sequences []types.Sequence
	for _, write := range snapshot.writes {
		for _, record := range decodeRecordFrames(t, write) {
			sequences = append(sequences, record.Sequence)
		}
	}
	if len(sequences) != 9 {
		t.Fatalf("records on disk = %d, want 9", len(sequences))
	}
	for i, sequence := range sequences {
		if sequence != types.Sequence(i+1) {
			t.Fatalf("record %d sequence = %d, want %d", i, sequence, i+1)
		}
	}
}

func TestWriterBatchByteTargetStopsCollection(t *testing.T) {
	config := WriterConfig{
		QueueCapacity:       4,
		MaxBatchRecords:     10,
		BatchByteTarget:     100,
		GroupCommitInterval: 10 * time.Second,
	}
	harness := newWriterHarness(t, config)

	value := make([]byte, 31) // frame size: 28 overhead + 1 key + 31 value = 60
	first := appendAsync(context.Background(), harness.writer, types.EntryTypePut, []byte("k"), value)
	second := appendAsync(context.Background(), harness.writer, types.EntryTypePut, []byte("k"), value)

	firstOutcome := waitOutcome(t, first)
	secondOutcome := waitOutcome(t, second)
	if firstOutcome.err != nil || secondOutcome.err != nil {
		t.Fatalf("Append() errors = %v, %v", firstOutcome.err, secondOutcome.err)
	}

	snapshot := harness.control.snapshot()
	if len(snapshot.writes) != 1 {
		t.Fatalf("Write calls = %d, want 1", len(snapshot.writes))
	}
	if snapshot.syncs != 1 {
		t.Fatalf("Sync calls = %d, want 1", snapshot.syncs)
	}
	records := decodeRecordFrames(t, snapshot.writes[0])
	if len(records) != 2 {
		t.Fatalf("records in batch = %d, want 2", len(records))
	}
}

func TestWriterSingleOversizedRecordFormsOwnBatch(t *testing.T) {
	config := WriterConfig{
		QueueCapacity:       4,
		MaxBatchRecords:     10,
		BatchByteTarget:     10,
		GroupCommitInterval: 10 * time.Second,
	}
	harness := newWriterHarness(t, config)

	value := make([]byte, 20)
	for i := 0; i < 2; i++ {
		sequence, err := harness.writer.Append(context.Background(), types.EntryTypePut, []byte("k"), value)
		if err != nil {
			t.Fatalf("Append %d = %v", i, err)
		}
		if sequence != types.Sequence(i+1) {
			t.Fatalf("Append %d sequence = %d, want %d", i, sequence, i+1)
		}
	}

	snapshot := harness.control.snapshot()
	if len(snapshot.writes) != 2 {
		t.Fatalf("Write calls = %d, want 2", len(snapshot.writes))
	}
	if snapshot.syncs != 2 {
		t.Fatalf("Sync calls = %d, want 2", snapshot.syncs)
	}
	for i, write := range snapshot.writes {
		if records := decodeRecordFrames(t, write); len(records) != 1 {
			t.Fatalf("batch %d records = %d, want 1", i, len(records))
		}
	}
}

func TestWriterCollectionTimerFlushesBatch(t *testing.T) {
	const interval = 40 * time.Millisecond
	config := WriterConfig{
		QueueCapacity:       4,
		MaxBatchRecords:     1024,
		BatchByteTarget:     1 << 20,
		GroupCommitInterval: interval,
	}
	harness := newWriterHarness(t, config)

	start := time.Now()
	sequence, err := harness.writer.Append(context.Background(), types.EntryTypePut, []byte("k"), []byte("v"))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Append() = %v", err)
	}
	if sequence != 1 {
		t.Fatalf("Append() sequence = %d, want 1", sequence)
	}
	if elapsed < interval/2 {
		t.Fatalf("Append() completed in %v, want the %v collection interval to apply", elapsed, interval)
	}

	snapshot := harness.control.snapshot()
	if len(snapshot.writes) != 1 || snapshot.syncs != 1 {
		t.Fatalf("I/O counts = writes %d, syncs %d, want 1 and 1", len(snapshot.writes), snapshot.syncs)
	}
}

func TestWriterQueueBackpressureAndCancellation(t *testing.T) {
	config := WriterConfig{
		QueueCapacity:       2,
		MaxBatchRecords:     1,
		BatchByteTarget:     1 << 20,
		GroupCommitInterval: 10 * time.Second,
	}
	harness := newWriterHarness(t, config)
	harness.blockSync()

	first := appendAsync(context.Background(), harness.writer, types.EntryTypePut, []byte("k"), []byte("first"))
	harness.waitSyncStarted(t)

	second := appendAsync(context.Background(), harness.writer, types.EntryTypePut, []byte("k"), []byte("second"))
	third := appendAsync(context.Background(), harness.writer, types.EntryTypePut, []byte("k"), []byte("third"))
	waitForCondition(t, func() bool {
		return len(harness.writer.requests) == 2 && len(harness.writer.slots) == 2
	}, func() string { return "WAL queue did not fill to its configured capacity" })

	blockedContext, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	blocked := appendAsync(blockedContext, harness.writer, types.EntryTypePut, []byte("k"), []byte("blocked"))
	blockedOutcome := waitOutcome(t, blocked)
	if !errors.Is(blockedOutcome.err, context.DeadlineExceeded) {
		t.Fatalf("blocked Append() = %v, want context.DeadlineExceeded", blockedOutcome.err)
	}
	if blockedOutcome.sequence != 0 {
		t.Fatalf("blocked Append() sequence = %d, want 0", blockedOutcome.sequence)
	}
	if len(harness.writer.requests) != 2 {
		t.Fatalf("queue length after cancellation = %d, want 2", len(harness.writer.requests))
	}

	harness.releaseSync()
	outcomes := []appendOutcome{
		waitOutcome(t, first),
		waitOutcome(t, second),
		waitOutcome(t, third),
	}
	for i, outcome := range outcomes {
		if outcome.err != nil {
			t.Fatalf("admitted Append %d = %v", i, outcome.err)
		}
	}
	if outcomes[0].sequence != 1 {
		t.Fatalf("first admitted sequence = %d, want 1", outcomes[0].sequence)
	}
	secondSequence := outcomes[1].sequence
	thirdSequence := outcomes[2].sequence
	if secondSequence > thirdSequence {
		secondSequence, thirdSequence = thirdSequence, secondSequence
	}
	if secondSequence != 2 || thirdSequence != 3 {
		t.Fatalf("admitted sequences after first = %d and %d, want 2 and 3", outcomes[1].sequence, outcomes[2].sequence)
	}

	next, err := harness.writer.Append(context.Background(), types.EntryTypePut, []byte("k"), []byte("next"))
	if err != nil {
		t.Fatalf("Append after cancellation = %v", err)
	}
	if next != 4 {
		t.Fatalf("Append after cancellation sequence = %d, want 4 (no sequence gap)", next)
	}

	snapshot := harness.control.snapshot()
	if len(snapshot.writes) != 4 || snapshot.syncs != 4 {
		t.Fatalf("I/O counts = writes %d, syncs %d, want 4 and 4", len(snapshot.writes), snapshot.syncs)
	}
}

func TestWriterCloseDrainsAdmittedRequests(t *testing.T) {
	config := WriterConfig{
		QueueCapacity:       2,
		MaxBatchRecords:     1,
		BatchByteTarget:     1 << 20,
		GroupCommitInterval: 10 * time.Second,
	}
	harness := newWriterHarness(t, config)
	harness.blockSync()

	first := appendAsync(context.Background(), harness.writer, types.EntryTypePut, []byte("k"), []byte("first"))
	harness.waitSyncStarted(t)
	second := appendAsync(context.Background(), harness.writer, types.EntryTypePut, []byte("k"), []byte("second"))
	third := appendAsync(context.Background(), harness.writer, types.EntryTypePut, []byte("k"), []byte("third"))
	harness.waitQueued(t, 2)

	closeResult := make(chan error, 1)
	go func() {
		closeResult <- harness.writer.Close(context.Background())
	}()
	waitForCondition(t, func() bool {
		return harness.writer.State() == WriterClosing
	}, func() string { return "writer did not enter closing state" })

	if _, err := harness.writer.Append(context.Background(), types.EntryTypePut, []byte("k"), []byte("late")); !errors.Is(err, kverr.ErrClosed) {
		t.Fatalf("Append during Close = %v, want ErrClosed", err)
	}

	harness.releaseSync()
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatalf("Close() = %v", err)
		}
	case <-time.After(testWaitTimeout):
		t.Fatal("timed out waiting for Close")
	}

	for i, result := range []<-chan appendOutcome{first, second, third} {
		outcome := waitOutcome(t, result)
		if outcome.err != nil {
			t.Fatalf("admitted Append %d = %v", i, outcome.err)
		}
	}
	if harness.writer.State() != WriterClosed {
		t.Fatalf("writer state = %s, want closed", harness.writer.State())
	}
}

func TestWriterCloseContextExpiryDoesNotStopShutdown(t *testing.T) {
	config := WriterConfig{
		QueueCapacity:       2,
		MaxBatchRecords:     1,
		BatchByteTarget:     1 << 20,
		GroupCommitInterval: 10 * time.Second,
	}
	harness := newWriterHarness(t, config)
	harness.blockSync()

	result := appendAsync(context.Background(), harness.writer, types.EntryTypePut, []byte("k"), []byte("v"))
	harness.waitSyncStarted(t)

	closeContext, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := harness.writer.Close(closeContext); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close(expired context) = %v, want context.DeadlineExceeded", err)
	}
	if harness.writer.State() != WriterClosing {
		t.Fatalf("writer state after expired Close = %s, want closing", harness.writer.State())
	}

	harness.releaseSync()
	if err := harness.writer.Close(context.Background()); err != nil {
		t.Fatalf("second Close() = %v", err)
	}
	outcome := waitOutcome(t, result)
	if outcome.err != nil {
		t.Fatalf("admitted Append() = %v", outcome.err)
	}
	if harness.writer.State() != WriterClosed {
		t.Fatalf("writer state = %s, want closed", harness.writer.State())
	}
}
