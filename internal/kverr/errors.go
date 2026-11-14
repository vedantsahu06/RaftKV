// Package kverr defines sentinel errors shared by the RaftKV storage engine.
//
// Public sentinel errors in package raftkv alias these values. Internal
// packages wrap them with context, so callers should use errors.Is.
package kverr

import "errors"

var (
	// ErrNotFound means that a key has no visible value at the read sequence.
	ErrNotFound = errors.New("raftkv: key not found")

	// ErrInvalidArgument means that a caller supplied an invalid value.
	ErrInvalidArgument = errors.New("raftkv: invalid argument")

	// ErrClosed means that the Engine is closing or closed.
	ErrClosed = errors.New("raftkv: engine is closed")

	// ErrFailed means that the Engine has entered fail-stop.
	ErrFailed = errors.New("raftkv: engine has failed")

	// ErrNotImplemented means that the requested operation is not implemented
	// in this build.
	ErrNotImplemented = errors.New("raftkv: operation not implemented")

	// ErrUnsupported means that an operation or platform is not supported.
	ErrUnsupported = errors.New("raftkv: operation is not supported")

	// ErrUnsupportedVolume means that a volume does not provide the Phase 1
	// local fixed NTFS platform contract.
	ErrUnsupportedVolume = errors.New("raftkv: volume is not supported")

	// ErrRecoveryRequired means that existing database state cannot be opened
	// by this build because recovery is not implemented.
	ErrRecoveryRequired = errors.New("raftkv: existing database state requires recovery")
)
