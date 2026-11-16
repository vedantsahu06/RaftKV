package wal

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/vedantsahu06/raftkv/internal/kverr"
	"github.com/vedantsahu06/raftkv/internal/types"
)

func TestWriterWriteFailureStopsWriter(t *testing.T) {
	config := WriterConfig{
		QueueCapacity:       4,
		MaxBatchRecords:     1,
		BatchByteTarget:     1 << 20,
		GroupCommitInterval: 10 * time.Second,
	}
	harness := newWriterHarness(t, config)
	writeErr := errors.New("injected write failure")
	harness.control.setWriteErr(writeErr)

	sequence, err := harness.writer.Append(context.Background(), types.EntryTypePut, []byte("k"), []byte("v"))
	if !errors.Is(err, kverr.ErrFailed) {
		t.Fatalf("Append() = %v, want ErrFailed", err)
	}
	if !errors.Is(err, writeErr) {
		t.Fatalf("Append() = %v, want injected write failure", err)
	}
	if sequence != 0 {
		t.Fatalf("Append() sequence = %d, want 0 after failure", sequence)
	}
	if harness.writer.State() != WriterFailed {
		t.Fatalf("writer state = %s, want failed", harness.writer.State())
	}

	snapshot := harness.control.snapshot()
	if len(snapshot.writes) != 1 {
		t.Fatalf("Write calls = %d, want 1", len(snapshot.writes))
	}
	if snapshot.syncs != 0 {
		t.Fatalf("Sync calls = %d, want 0 after write failure", snapshot.syncs)
	}
	if snapshot.seeks != 0 || snapshot.truncates != 0 {
		t.Fatalf("writer performed seek/truncate after failure: seeks=%d truncates=%d", snapshot.seeks, snapshot.truncates)
	}

	sequence, err = harness.writer.Append(context.Background(), types.EntryTypePut, []byte("k"), []byte("v"))
	if !errors.Is(err, kverr.ErrFailed) {
		t.Fatalf("Append(after failure) = %v, want ErrFailed", err)
	}
	if sequence != 0 {
		t.Fatalf("Append(after failure) sequence = %d, want 0", sequence)
	}
	after := harness.control.snapshot()
	if len(after.writes) != len(snapshot.writes) || after.syncs != snapshot.syncs {
		t.Fatalf("rejected append caused I/O: before=%+v after=%+v", snapshot, after)
	}

	if err := harness.writer.Close(context.Background()); !errors.Is(err, kverr.ErrFailed) {
		t.Fatalf("Close() = %v, want ErrFailed", err)
	}
}

func TestWriterShortWriteStopsWriter(t *testing.T) {
	config := WriterConfig{
		QueueCapacity:       4,
		MaxBatchRecords:     1,
		BatchByteTarget:     1 << 20,
		GroupCommitInterval: 10 * time.Second,
	}
	harness := newWriterHarness(t, config)
	harness.control.setShortWrite(true)

	sequence, err := harness.writer.Append(context.Background(), types.EntryTypePut, []byte("k"), []byte("v"))
	if !errors.Is(err, kverr.ErrFailed) {
		t.Fatalf("Append() = %v, want ErrFailed", err)
	}
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("Append() = %v, want io.ErrShortWrite", err)
	}
	if sequence != 0 {
		t.Fatalf("Append() sequence = %d, want 0", sequence)
	}
	if harness.writer.State() != WriterFailed {
		t.Fatalf("writer state = %s, want failed", harness.writer.State())
	}

	snapshot := harness.control.snapshot()
	if snapshot.syncs != 0 {
		t.Fatalf("Sync calls = %d, want 0 after short write", snapshot.syncs)
	}
	if snapshot.seeks != 0 || snapshot.truncates != 0 {
		t.Fatalf("writer performed seek/truncate after short write: seeks=%d truncates=%d", snapshot.seeks, snapshot.truncates)
	}
}

func TestWriterSyncFailureFailsInFlightAndQueuedRequests(t *testing.T) {
	config := WriterConfig{
		QueueCapacity:       2,
		MaxBatchRecords:     1,
		BatchByteTarget:     1 << 20,
		GroupCommitInterval: 10 * time.Second,
	}
	harness := newWriterHarness(t, config)
	harness.blockSync()
	syncErr := errors.New("injected sync failure")
	harness.control.setSyncErr(syncErr)

	first := appendAsync(context.Background(), harness.writer, types.EntryTypePut, []byte("k"), []byte("first"))
	harness.waitSyncStarted(t)

	second := appendAsync(context.Background(), harness.writer, types.EntryTypePut, []byte("k"), []byte("second"))
	third := appendAsync(context.Background(), harness.writer, types.EntryTypePut, []byte("k"), []byte("third"))
	harness.waitQueued(t, 2)

	// This request is waiting for a queue slot. Writer failure must wake it
	// without requiring the queue to drain first.
	waiting := appendAsync(context.Background(), harness.writer, types.EntryTypePut, []byte("k"), []byte("waiting"))

	harness.releaseSync()

	for i, result := range []<-chan appendOutcome{first, second, third, waiting} {
		outcome := waitOutcome(t, result)
		if !errors.Is(outcome.err, kverr.ErrFailed) {
			t.Fatalf("Append %d = %v, want ErrFailed", i, outcome.err)
		}
		if !errors.Is(outcome.err, syncErr) {
			t.Fatalf("Append %d = %v, want injected sync failure", i, outcome.err)
		}
		if outcome.sequence != 0 {
			t.Fatalf("Append %d sequence = %d, want 0 after Sync failure", i, outcome.sequence)
		}
	}

	if harness.writer.State() != WriterFailed {
		t.Fatalf("writer state = %s, want failed", harness.writer.State())
	}

	// The failed batch was written once and Sync was attempted once. Queued
	// requests must not be written after the failed Sync.
	snapshot := harness.control.snapshot()
	if len(snapshot.writes) != 1 {
		t.Fatalf("Write calls = %d, want 1", len(snapshot.writes))
	}
	if snapshot.syncs != 1 {
		t.Fatalf("Sync calls = %d, want 1 (no retry)", snapshot.syncs)
	}
	if snapshot.seeks != 0 || snapshot.truncates != 0 {
		t.Fatalf("writer performed seek/truncate after Sync failure: seeks=%d truncates=%d", snapshot.seeks, snapshot.truncates)
	}
	records := decodeRecordFrames(t, snapshot.writes[0])
	if len(records) != 1 || !equalsBytes(records[0].Value, []byte("first")) {
		t.Fatalf("written records after failed Sync = %+v, want only the in-flight request", records)
	}

	if len(harness.writer.requests) != 0 {
		t.Fatalf("queued requests after failure = %d, want 0", len(harness.writer.requests))
	}

	sequence, err := harness.writer.Append(context.Background(), types.EntryTypePut, []byte("k"), []byte("v"))
	if !errors.Is(err, kverr.ErrFailed) {
		t.Fatalf("Append(after failure) = %v, want ErrFailed", err)
	}
	if sequence != 0 {
		t.Fatalf("Append(after failure) sequence = %d, want 0", sequence)
	}

	if err := harness.writer.Close(context.Background()); !errors.Is(err, kverr.ErrFailed) {
		t.Fatalf("Close() = %v, want ErrFailed", err)
	}
}

func TestWriterSequenceExhaustionDoesNotWrap(t *testing.T) {
	config := WriterConfig{
		QueueCapacity:       4,
		MaxBatchRecords:     1,
		BatchByteTarget:     1 << 20,
		GroupCommitInterval: time.Millisecond,
	}
	harness := newWriterHarness(t, config)

	harness.writer.admitMu.Lock()
	harness.writer.nextSequence = ^types.Sequence(0)
	harness.writer.admitMu.Unlock()

	sequence, err := harness.writer.Append(context.Background(), types.EntryTypePut, []byte("k"), []byte("v"))
	if err != nil {
		t.Fatalf("Append(max sequence) = %v", err)
	}
	if sequence != ^types.Sequence(0) {
		t.Fatalf("Append(max sequence) sequence = %d, want maximum", sequence)
	}

	sequence, err = harness.writer.Append(context.Background(), types.EntryTypePut, []byte("k"), []byte("v"))
	if !errors.Is(err, ErrSequenceExhausted) {
		t.Fatalf("Append(after max) = %v, want ErrSequenceExhausted", err)
	}
	if sequence != 0 {
		t.Fatalf("Append(after max) sequence = %d, want 0", sequence)
	}
	if harness.writer.State() != WriterOpen {
		t.Fatalf("writer state = %s, want open", harness.writer.State())
	}
}

func equalsBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
