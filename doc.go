// Package raftkv implements the Phase 1 single-node RaftKV storage engine.
//
// This build contains the Milestone 1 foundation: configuration, database
// directory initialization, the filesystem abstraction and the public API
// surface. Put, Get and Delete are not implemented yet and return
// ErrNotImplemented. No WAL, Memtable, SSTable or Manifest format is present in
// this milestone, and no durability guarantee is claimed.
package raftkv
