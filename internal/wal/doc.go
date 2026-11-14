// Package wal implements WAL v1 framing and durable segment creation.
//
// This milestone establishes the on-disk format, segment filenames and the
// creation protocol from docs/architecture/phase-1/WAL_DESIGN.md. Group commit,
// sequence assignment, recovery scanning and record appends are intentionally
// not part of this package yet.
//
// Only the dedicated WAL writer goroutine may own a Segment. The Segment
// returned by CreateSegment is active only after the complete creation protocol
// has finished, including the platform directory-entry durability step.
//
// Segment creation calls the platform directory-entry durability operation. The
// Windows mechanism and power-loss behavior remain AQ-003/AQ-006 validation
// requirements; this package does not claim that crash durability has been
// proven.
package wal
