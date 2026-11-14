package raftkv

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/vedantsahu06/raftkv/internal/dbdir"
	"github.com/vedantsahu06/raftkv/internal/filesystem"
	"github.com/vedantsahu06/raftkv/internal/kverr"
	"github.com/vedantsahu06/raftkv/internal/platform"
	"github.com/vedantsahu06/raftkv/internal/types"
)

type engineState uint8

const (
	stateOpen engineState = iota + 1
	stateClosing
	stateClosed
)

// Engine is a single-node RaftKV storage engine. It is safe for concurrent use
// and must not be copied.
//
// This milestone implements configuration, directory initialization and
// lifecycle. Put, Get and Delete return ErrNotImplemented.
type Engine struct {
	cfg    Config
	fs     filesystem.FS
	layout dbdir.Layout

	mu    sync.Mutex
	state engineState

	shutdownOnce sync.Once
	shutdownDone chan struct{}
	shutdownErr  error
}

// Open validates cfg, qualifies the target volume as fixed local NTFS, and
// initializes the database directory layout.
//
// Open refuses to initialize over existing artifacts because recovery is not
// implemented in this milestone. It does not create a Manifest.
//
// Cross-process exclusive database ownership is not implemented. Callers must
// not open the same database directory from multiple processes.
func Open(cfg Config) (*Engine, error) {
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	abs, err := filepath.Abs(cfg.Dir)
	if err != nil {
		return nil, fmt.Errorf("raftkv: resolve database directory %q: %w", cfg.Dir, err)
	}
	cfg.Dir = abs

	fsys, err := platform.New(abs)
	if err != nil {
		return nil, fmt.Errorf("raftkv: open %q: %w", abs, err)
	}
	layout, err := dbdir.Initialize(fsys, abs)
	if err != nil {
		return nil, fmt.Errorf("raftkv: open %q: %w", abs, err)
	}

	return &Engine{
		cfg:          cfg,
		fs:           fsys,
		layout:       layout,
		state:        stateOpen,
		shutdownDone: make(chan struct{}),
	}, nil
}

// Put stores a value for key.
//
// Put is not implemented in this milestone. It validates the key and returns
// ErrNotImplemented for a valid call.
func (e *Engine) Put(ctx context.Context, key, value []byte) error {
	if err := e.checkOperation(ctx, key); err != nil {
		return err
	}
	return fmt.Errorf("raftkv: put: %w", kverr.ErrNotImplemented)
}

// Get returns the value visible for key.
//
// Get is not implemented in this milestone. It validates the key and returns
// ErrNotImplemented for a valid call.
func (e *Engine) Get(ctx context.Context, key []byte) ([]byte, error) {
	if err := e.checkOperation(ctx, key); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("raftkv: get: %w", kverr.ErrNotImplemented)
}

// Delete removes key.
//
// Delete is not implemented in this milestone. It validates the key and returns
// ErrNotImplemented for a valid call.
func (e *Engine) Delete(ctx context.Context, key []byte) error {
	if err := e.checkOperation(ctx, key); err != nil {
		return err
	}
	return fmt.Errorf("raftkv: delete: %w", kverr.ErrNotImplemented)
}

// Close begins graceful shutdown and waits for it to complete or for ctx to
// expire.
//
// Context expiry stops only this caller's wait. Shutdown continues in the
// background, never force-closes resources, and a later Close waits for the same
// shutdown. In this milestone the Engine owns no long-lived file handles, so
// shutdown normally completes immediately.
func (e *Engine) Close(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", kverr.ErrInvalidArgument)
	}
	e.shutdownOnce.Do(func() {
		e.mu.Lock()
		e.state = stateClosing
		e.mu.Unlock()
		go e.shutdown()
	})

	select {
	case <-e.shutdownDone:
		return e.shutdownErr
	case <-ctx.Done():
		select {
		case <-e.shutdownDone:
			return e.shutdownErr
		default:
			return ctx.Err()
		}
	}
}

func (e *Engine) shutdown() {
	// No long-lived resources exist in this milestone. Later milestones must
	// drain admitted writes, finish publication and wait for readers here before
	// closing the state.
	e.mu.Lock()
	e.state = stateClosed
	e.mu.Unlock()
	close(e.shutdownDone)
}

func (e *Engine) checkOperation(ctx context.Context, key []byte) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", kverr.ErrInvalidArgument)
	}
	e.mu.Lock()
	state := e.state
	e.mu.Unlock()
	if state != stateOpen {
		return kverr.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(key) == 0 {
		return fmt.Errorf("%w: key must not be empty", kverr.ErrInvalidArgument)
	}
	if len(key) > types.MaxKeySize {
		return fmt.Errorf("%w: key length %d exceeds maximum %d", kverr.ErrInvalidArgument, len(key), types.MaxKeySize)
	}
	return nil
}
