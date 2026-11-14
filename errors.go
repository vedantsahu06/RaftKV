package raftkv

import "github.com/vedantsahu06/raftkv/internal/kverr"

// ErrNotFound is returned when a key has no visible value.
var ErrNotFound = kverr.ErrNotFound

// ErrInvalidArgument is returned when a caller supplies an invalid value.
var ErrInvalidArgument = kverr.ErrInvalidArgument

// ErrClosed is returned when an operation is attempted on a closing or closed
// Engine.
var ErrClosed = kverr.ErrClosed

// ErrFailed is returned after the Engine has entered fail-stop.
var ErrFailed = kverr.ErrFailed

// ErrNotImplemented is returned by operations that are part of the public API
// but are not implemented in this build.
var ErrNotImplemented = kverr.ErrNotImplemented

// ErrUnsupported is returned when an operation or platform is not supported.
var ErrUnsupported = kverr.ErrUnsupported

// ErrUnsupportedVolume is returned when the database directory is not on a
// fixed local NTFS volume.
var ErrUnsupportedVolume = kverr.ErrUnsupportedVolume

// ErrRecoveryRequired is returned when Open finds existing database artifacts.
// This build does not implement recovery and never initializes empty over them.
var ErrRecoveryRequired = kverr.ErrRecoveryRequired
