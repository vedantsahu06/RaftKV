// Package filesystem defines the storage operations required by the Phase 1
// storage engine and the platform backends that provide them.
//
// WAL, Memtable, SSTable and Manifest code must use this abstraction. No such
// component may call platform file APIs directly.
package filesystem

import (
	"fmt"
	"io"
	"io/fs"
)

// File is an open file. The read, write, seek, truncate and Sync operations are
// the complete set required by WAL recovery, SSTable validation and Manifest
// publication.
type File interface {
	io.Reader
	io.ReaderAt
	io.Writer
	io.WriterAt
	io.Seeker
	io.Closer

	// Sync flushes the file contents to stable storage. A successful Sync is
	// the durability boundary for all preceding writes to this file.
	Sync() error

	// Truncate changes the file size.
	Truncate(size int64) error

	// Name returns the name the file was opened with.
	Name() string

	// Stat returns the current file information.
	Stat() (fs.FileInfo, error)
}

// FS is the platform filesystem contract used by the storage engine.
type FS interface {
	// Mkdir creates one directory. It must fail if the path already exists.
	Mkdir(path string, perm fs.FileMode) error

	// Open opens an existing file for reading.
	Open(path string) (File, error)

	// OpenFile opens a file with the supplied os.O_* flags.
	OpenFile(path string, flag int, perm fs.FileMode) (File, error)

	// CreateExclusive creates a new file for reading and writing and fails if
	// the path already exists. The caller owns the returned File.
	CreateExclusive(path string) (File, error)

	// RenameNoReplace atomically renames oldpath to newpath on one volume. It
	// must fail, without changing either path, when newpath already exists.
	RenameNoReplace(oldpath, newpath string) error

	// Remove deletes a file or empty directory.
	Remove(path string) error

	// Stat returns information about path.
	Stat(path string) (fs.FileInfo, error)

	// ReadDir lists a directory.
	ReadDir(path string) ([]fs.DirEntry, error)

	// SyncDir makes preceding directory-entry creation, rename or deletion
	// operations durable. It must return an error when the operation is not
	// supported; it must never report silent success.
	SyncDir(path string) error

	// Qualify reports the filesystem and volume type for path.
	Qualify(path string) (VolumeInfo, error)
}

// DriveType is the Win32 drive type of a volume.
type DriveType uint32

const (
	DriveUnknown   DriveType = 0
	DriveNoRootDir DriveType = 1
	DriveRemovable DriveType = 2
	DriveFixed     DriveType = 3
	DriveRemote    DriveType = 4
	DriveCDROM     DriveType = 5
	DriveRAMDisk   DriveType = 6
)

// String returns a human-readable drive type.
func (d DriveType) String() string {
	switch d {
	case DriveUnknown:
		return "unknown"
	case DriveNoRootDir:
		return "no root directory"
	case DriveRemovable:
		return "removable"
	case DriveFixed:
		return "fixed"
	case DriveRemote:
		return "remote"
	case DriveCDROM:
		return "cd-rom"
	case DriveRAMDisk:
		return "ramdisk"
	default:
		return fmt.Sprintf("DriveType(%d)", uint32(d))
	}
}

// VolumeInfo describes the volume containing a path.
type VolumeInfo struct {
	// VolumePath is the volume mount path, for example "C:\".
	VolumePath string

	// FileSystem is the filesystem name reported by the platform, for example
	// "NTFS".
	FileSystem string

	// DriveType is the platform volume type.
	DriveType DriveType
}
