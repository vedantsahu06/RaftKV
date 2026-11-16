// Package wal implements WAL v1 framing, durable segment creation and group
// commit.
//
// The on-disk format, segment creation protocol, sequence assignment, batching,
// one-write/one-Sync group commit and fail-stop behavior follow
// docs/architecture/phase-1/WAL_DESIGN.md. Recovery scanning, rotation and
// physical deletion are intentionally not part of this package yet.
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
