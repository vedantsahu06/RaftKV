package wal

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/vedantsahu06/raftkv/internal/types"
)

func TestWriterStressWithBackpressure(t *testing.T) {
	config := WriterConfig{
		QueueCapacity:       4,
		MaxBatchRecords:     9,
		BatchByteTarget:     2 << 10,
		GroupCommitInterval: 100 * time.Microsecond,
	}
	harness := newWriterHarness(t, config)

	const writers = 16
	const perWriter = 50
	const total = writers * perWriter

	results := make(chan appendOutcome, total)
	var group sync.WaitGroup
	for worker := 0; worker < writers; worker++ {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			for i := 0; i < perWriter; i++ {
				key := []byte{byte('a' + worker%26)}
				value := make([]byte, 40)
				sequence, err := harness.writer.Append(context.Background(), types.EntryTypePut, key, value)
				results <- appendOutcome{sequence: sequence, err: err}
			}
		}(worker)
	}
	group.Wait()
	close(results)

	seen := make(map[types.Sequence]bool, total)
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
	if len(seen) != total {
		t.Fatalf("unique sequences = %d, want %d", len(seen), total)
	}

	if err := harness.writer.Close(context.Background()); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	records := readHarnessRecords(t, harness)
	if len(records) != total {
		t.Fatalf("records on disk = %d, want %d", len(records), total)
	}
	for i, record := range records {
		want := types.Sequence(i + 1)
		if record.Sequence != want {
			t.Fatalf("record %d sequence = %d, want %d", i, record.Sequence, want)
		}
		if record.Type != types.EntryTypePut {
			t.Fatalf("record %d type = %s, want put", i, record.Type)
		}
	}
}
