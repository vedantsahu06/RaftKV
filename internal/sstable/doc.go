// Package sstable defines the Phase 1 SSTable metadata shared with the Manifest,
// the SSTable filename rules and the observed-ID scan used for allocation.
//
// The SSTable file builder, full-file validation and durable creation protocol
// belong to a later milestone. This package currently defines only the metadata
// and naming contract those later operations must follow.
package sstable
