// Package manifest implements the Phase 1 authoritative metadata record.
//
// A Manifest is deterministically encoded compact JSON followed by a four-byte
// little-endian CRC32C trailer.  Publication uses an exclusive temporary file,
// file Sync, non-overwriting rename and directory-entry durability. There is no
// CURRENT pointer file.
//
// Milestone 4 validates the Manifest file itself. Full recovery validity also
// requires every referenced SSTable to exist and pass full-file validation;
// that belongs to the SSTable and recovery milestones. Functions that select or
// clean up versions therefore require a caller-supplied StateValidator. Passing a
// validator that checks only Manifest bytes is safe only for isolated Manifest
// tests, not as a claim of complete crash recovery.
//
// Directory-entry durability is delegated to the platform filesystem. The
// Windows mechanism and power-loss behavior remain AQ-003/AQ-006 validation
// requirements; this package does not claim that crash durability is proven.
package manifest
