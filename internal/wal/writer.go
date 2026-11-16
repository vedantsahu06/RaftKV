package wal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/vedantsahu06/raftkv/internal/kverr"
	"github.com/vedantsahu06/raftkv/internal/types"
)

// ErrSequenceExhausted reports that no larger WAL sequence number can be
// assigned. Sequence zero is reserved, so the maximum sequence is the largest
// representable types.Sequence value.
var ErrSequenceExhausted = errors.New("raftkv: WAL sequence space exhausted")

// errQueueReservationLost is an internal invariant violation. A caller that
// owns a queue slot must always be able to place its request in the queue.
var errQueueReservationLost = errors.New("raftkv: WAL queue reservation lost")

// WriterState is the observable lifecycle state of a WAL Writer.
type WriterState uint8

const (
	// WriterOpen accepts appends and writes group-commit batches.
	WriterOpen WriterState = iota + 1

	// WriterClosing rejects new appends while admitted requests finish.
	WriterClosing

	// WriterFailed is terminal for this Writer. Recovery and reopen are
	// required before WAL writes can resume.
	WriterFailed

	// WriterClosed has completed its shutdown and owns no running resources.
	WriterClosed
)

// String returns the writer state name.
func (s WriterState) String() string {
	switch s {
	case WriterOpen:
		return "open"
	case WriterClosing:
		return "closing"
	case WriterFailed:
		return "failed"
	case WriterClosed:
		return "closed"
	default:
		return fmt.Sprintf("WriterState(%d)", uint8(s))
	}
}

// WriterConfig controls WAL group commit.
//
// Every field must be positive. The architecture defaults from D-066 are
// supplied by the public raftkv configuration; this package does not invent a
// second set of defaults.
type WriterConfig struct {
	// QueueCapacity bounds requests waiting for the writer goroutine.
	QueueCapacity int

	// MaxBatchRecords limits the number of records in one batch.
	MaxBatchRecords int

	// BatchByteTarget is the collection target for one batch. It is not a
	// per-record limit; one larger record forms its own batch.
	BatchByteTarget int64

	// GroupCommitInterval is how long the writer waits for more requests
	// before writing the current batch.
	GroupCommitInterval time.Duration
}

// Writer is the single owner of WAL appends and Sync calls.
//
// Append is synchronous: it returns only after the record's batch has completed
// one successful Sync. A dedicated goroutine performs every file write and
// Sync, so no other goroutine can reorder records or interleave durability
// boundaries.
//
// The bounded channel is the request queue. The slots channel holds one token
// for every admitted request that the writer has not dequeued yet. A sender
// acquires a token before taking the admission mutex. This guarantees that the
// channel send inside the critical section cannot block, which in turn lets
// Close and failure handling change state without racing a blocked sender.
type Writer struct {
	segment *Segment
	config  WriterConfig

	requests   chan *appendRequest
	slots      chan struct{}
	failureCh  chan struct{}
	shutdownCh chan struct{}
	done       chan struct{}

	failureOnce  sync.Once
	shutdownOnce sync.Once

	// admitMu serializes sequence assignment and enqueue order. It also
	// protects the writer state and the rejection error fields.
	admitMu      sync.Mutex
	state        WriterState
	nextSequence types.Sequence
	failureErr   error
	closeErr     error
}

type appendRequest struct {
	frame  []byte
	result chan appendResult
}

type appendResult struct {
	err error
}

// NewWriter starts the dedicated WAL writer goroutine.
//
// The caller transfers exclusive ownership of segment to the Writer. The
// segment must be established and active. A successful Writer.Close seals it.
func NewWriter(segment *Segment, config WriterConfig) (*Writer, error) {
	if segment == nil {
		return nil, fmt.Errorf("%w: WAL segment must not be nil", kverr.ErrInvalidArgument)
	}
	if segment.State() != SegmentActive {
		return nil, fmt.Errorf("%w: cannot start WAL writer on a %s segment", kverr.ErrInvalidArgument, segment.State())
	}
	if config.QueueCapacity <= 0 {
		return nil, fmt.Errorf("%w: WAL queue capacity must be positive", kverr.ErrInvalidArgument)
	}
	if config.MaxBatchRecords <= 0 {
		return nil, fmt.Errorf("%w: WAL maximum batch records must be positive", kverr.ErrInvalidArgument)
	}
	if config.BatchByteTarget <= 0 {
		return nil, fmt.Errorf("%w: WAL batch byte target must be positive", kverr.ErrInvalidArgument)
	}
	if config.GroupCommitInterval <= 0 {
		return nil, fmt.Errorf("%w: WAL group-commit interval must be positive", kverr.ErrInvalidArgument)
	}

	writer := &Writer{
		segment:    segment,
		config:     config,
		requests:   make(chan *appendRequest, config.QueueCapacity),
		slots:      make(chan struct{}, config.QueueCapacity),
		failureCh:  make(chan struct{}),
		shutdownCh: make(chan struct{}),
		done:       make(chan struct{}),
		state:      WriterOpen,
		// New segments are empty. Recovery will supply a recovered next
		// sequence when it is integrated in a later milestone.
		nextSequence: 1,
	}

	go writer.run()
	return writer, nil
}

// State returns the current writer state.
func (w *Writer) State() WriterState {
	w.admitMu.Lock()
	defer w.admitMu.Unlock()
	return w.state
}

// Append validates a mutation, assigns its sequence and waits for a batch Sync.
//
// The returned sequence is valid only when the error is nil. Context
// cancellation may prevent admission. Once the request is enqueued, it cannot
// be cancelled and Append waits for the writer's result.
func (w *Writer) Append(ctx context.Context, entryType types.EntryType, key, value []byte) (types.Sequence, error) {
	if ctx == nil {
		return 0, fmt.Errorf("%w: context must not be nil", kverr.ErrInvalidArgument)
	}

	// Validate before admission so an invalid record never consumes a queue
	// slot or a sequence number. The placeholder sequence satisfies the
	// validator; the real sequence is assigned under admitMu below.
	validationRecord := Record{
		Type:     entryType,
		Sequence: 1,
		Key:      key,
		Value:    value,
	}
	if err := validateRecord(validationRecord); err != nil {
		return 0, err
	}

	select {
	case w.slots <- struct{}{}:
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-w.failureCh:
		return 0, w.failureError()
	case <-w.shutdownCh:
		return 0, closedWriterError()
	}

	w.admitMu.Lock()
	if err := ctx.Err(); err != nil {
		w.admitMu.Unlock()
		w.releaseSlot()
		return 0, err
	}
	if err := w.rejectionErrorLocked(); err != nil {
		w.admitMu.Unlock()
		w.releaseSlot()
		return 0, err
	}
	if w.nextSequence == 0 {
		w.admitMu.Unlock()
		w.releaseSlot()
		return 0, ErrSequenceExhausted
	}

	sequence := w.nextSequence
	frame, err := EncodeRecord(Record{
		Type:     entryType,
		Sequence: sequence,
		Key:      key,
		Value:    value,
	})
	if err != nil {
		w.admitMu.Unlock()
		w.releaseSlot()
		return 0, err
	}

	request := &appendRequest{
		frame:  frame,
		result: make(chan appendResult, 1),
	}

	// A held slot guarantees this send cannot block. The writer releases the
	// slot when it dequeues the request.
	select {
	case w.requests <- request:
		w.nextSequence++
		w.admitMu.Unlock()
	default:
		w.admitMu.Unlock()
		w.releaseSlot()
		return 0, errQueueReservationLost
	}

	// The request is admitted. Cancellation no longer has any effect: a
	// durable record must not be left without a caller receiving its result.
	result := <-request.result
	if result.err != nil {
		return 0, result.err
	}
	return sequence, nil
}

// Close stops admitting new requests and waits for all admitted requests to
// finish. Context expiry stops only this caller's wait; the writer continues
// draining in the background and a later Close may wait for the same shutdown.
func (w *Writer) Close(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: context must not be nil", kverr.ErrInvalidArgument)
	}

	w.beginClose()

	select {
	case <-w.done:
		w.admitMu.Lock()
		defer w.admitMu.Unlock()
		if w.state == WriterFailed {
			return w.failureErr
		}
		return w.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *Writer) beginClose() {
	w.admitMu.Lock()
	if w.state == WriterOpen {
		w.state = WriterClosing
		w.shutdownOnce.Do(func() {
			close(w.shutdownCh)
		})
	}
	w.admitMu.Unlock()
}

func (w *Writer) rejectionErrorLocked() error {
	switch w.state {
	case WriterOpen:
		return nil
	case WriterFailed:
		return w.failureErrorLocked()
	case WriterClosing, WriterClosed:
		return closedWriterError()
	default:
		return fmt.Errorf("%w: WAL writer is in unknown state %s", kverr.ErrFailed, w.state)
	}
}

func (w *Writer) failureError() error {
	w.admitMu.Lock()
	defer w.admitMu.Unlock()
	return w.failureErrorLocked()
}

func (w *Writer) failureErrorLocked() error {
	if w.failureErr != nil {
		return w.failureErr
	}
	return fmt.Errorf("%w: WAL writer failed", kverr.ErrFailed)
}

func closedWriterError() error {
	return fmt.Errorf("%w: WAL writer is closing or closed", kverr.ErrClosed)
}

func (w *Writer) releaseSlot() {
	<-w.slots
}

// run is the only goroutine that writes or Syncs the WAL file.
//
// A write or Sync failure is terminal: fail records the error, wakes every
// admitted request and stops the loop. There is deliberately no retry, seek or
// truncation after a failed durability operation.
func (w *Writer) run() {
	for {
		request, ok := w.nextRequest()
		if !ok {
			w.finishClosed()
			return
		}

		batch := w.collectBatch(request)
		if err := w.writeAndSync(batch); err != nil {
			w.fail(err, batch)
			w.sealSegment()
			close(w.done)
			return
		}

		for _, admitted := range batch {
			admitted.result <- appendResult{}
		}
	}
}

func (w *Writer) nextRequest() (*appendRequest, bool) {
	select {
	case request := <-w.requests:
		w.releaseSlot()
		return request, true
	case <-w.shutdownCh:
		// beginClose set WriterClosing while holding admitMu, so no sender
		// can add another request after this point. Drain what is queued.
		select {
		case request := <-w.requests:
			w.releaseSlot()
			return request, true
		default:
			return nil, false
		}
	}
}

func (w *Writer) collectBatch(first *appendRequest) []*appendRequest {
	batch := []*appendRequest{first}
	batchBytes := int64(len(first.frame))

	if w.batchLimitReached(len(batch), batchBytes) {
		return batch
	}

	timer := time.NewTimer(w.config.GroupCommitInterval)
	defer timer.Stop()

	for {
		select {
		case request := <-w.requests:
			w.releaseSlot()
			batch = append(batch, request)
			batchBytes += int64(len(request.frame))
			if w.batchLimitReached(len(batch), batchBytes) {
				return batch
			}
		case <-timer.C:
			return batch
		case <-w.shutdownCh:
			// Admission is closed. Stop waiting for the collection timer and
			// take whatever is already queued, up to the same batch limits.
			return w.drainQueuedBatch(batch, batchBytes)
		}
	}
}

func (w *Writer) batchLimitReached(records int, bytes int64) bool {
	return records >= w.config.MaxBatchRecords || bytes >= w.config.BatchByteTarget
}

func (w *Writer) drainQueuedBatch(batch []*appendRequest, batchBytes int64) []*appendRequest {
	for !w.batchLimitReached(len(batch), batchBytes) {
		select {
		case request := <-w.requests:
			w.releaseSlot()
			batch = append(batch, request)
			batchBytes += int64(len(request.frame))
		default:
			return batch
		}
	}
	return batch
}

func (w *Writer) writeAndSync(batch []*appendRequest) error {
	totalBytes := 0
	for _, request := range batch {
		totalBytes += len(request.frame)
	}

	frames := make([]byte, 0, totalBytes)
	for _, request := range batch {
		frames = append(frames, request.frame...)
	}

	// Exactly one write is attempted for the batch. A partial write is a
	// fail-stop condition because the writer cannot know which records reached
	// the file.
	written, err := w.segment.file.Write(frames)
	if err != nil {
		return fmt.Errorf("%w: write WAL batch: %w", kverr.ErrFailed, err)
	}
	if written != len(frames) {
		return fmt.Errorf("%w: write WAL batch: %w", kverr.ErrFailed, io.ErrShortWrite)
	}

	// Exactly one Sync follows the write. A successful Sync is the batch's
	// durability boundary.
	if err := w.segment.file.Sync(); err != nil {
		return fmt.Errorf("%w: sync WAL batch: %w", kverr.ErrFailed, err)
	}
	return nil
}

func (w *Writer) fail(cause error, currentBatch []*appendRequest) {
	// Wake callers waiting for a queue slot first. They must not need admitMu
	// to learn that the writer has failed.
	w.failureOnce.Do(func() {
		close(w.failureCh)
	})

	w.admitMu.Lock()
	if w.state == WriterOpen || w.state == WriterClosing {
		w.state = WriterFailed
	}
	if w.failureErr == nil {
		w.failureErr = cause
	}
	failureErr := w.failureErrorLocked()
	w.admitMu.Unlock()

	for _, request := range currentBatch {
		request.result <- appendResult{err: failureErr}
	}
	w.failQueuedRequests(failureErr)
}

func (w *Writer) failQueuedRequests(failureErr error) {
	for {
		select {
		case request := <-w.requests:
			w.releaseSlot()
			request.result <- appendResult{err: failureErr}
		default:
			return
		}
	}
}

func (w *Writer) finishClosed() {
	w.sealSegment()

	w.admitMu.Lock()
	if w.state == WriterClosing {
		w.state = WriterClosed
	}
	w.admitMu.Unlock()
	close(w.done)
}

func (w *Writer) sealSegment() {
	err := w.segment.Seal()

	w.admitMu.Lock()
	w.closeErr = err
	w.admitMu.Unlock()
}
