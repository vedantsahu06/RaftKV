//go:build windows

package winfs

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/vedantsahu06/raftkv/internal/kverr"
)

// SyncDir makes directory entries durable on local NTFS.
//
// The backend opens a directory handle with FILE_FLAG_BACKUP_SEMANTICS and both
// GENERIC_READ and GENERIC_WRITE, then calls FlushFileBuffers. FlushFileBuffers
// requires GENERIC_WRITE; a read-only directory handle fails with access
// denied. Go's os.File.Sync on a directory is not used because it uses a
// read-only handle and fails here.
//
// Microsoft documents FlushFileBuffers for a file and MS-FSA describes flushing
// a DirectoryFile. The public Win32 contract does not establish directory-entry
// persistence across abrupt power loss. This method therefore reports success
// only for the platform operation; it does not by itself prove crash
// durability. AQ-003 remains a validation requirement.
func (winFS) SyncDir(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve directory %q: %w", path, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("stat directory %q: %w", abs, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: cannot make directory entries durable for non-directory %q", kverr.ErrInvalidArgument, abs)
	}

	ptr, err := syscall.UTF16PtrFromString(abs)
	if err != nil {
		return fmt.Errorf("encode directory path %q: %w", abs, err)
	}
	h, err := syscall.CreateFile(
		ptr,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return fmt.Errorf("open directory %q for flushing: %w", abs, err)
	}

	flushErr := syscall.FlushFileBuffers(h)
	closeErr := syscall.CloseHandle(h)
	if flushErr != nil {
		return fmt.Errorf("flush directory %q: %w", abs, flushErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close directory handle for %q: %w", abs, closeErr)
	}
	return nil
}
