//go:build windows

package winfs

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"github.com/vedantsahu06/raftkv/internal/kverr"
)

const moveFileWriteThrough = 0x00000008

var procMoveFileExW = kernel32.NewProc("MoveFileExW")

// RenameNoReplace atomically renames a file on one volume without replacing an
// existing target.
//
// MoveFileExW is called with MOVEFILE_WRITE_THROUGH and without
// MOVEFILE_REPLACE_EXISTING. Same-volume renames are atomic at the namespace
// level. MOVEFILE_WRITE_THROUGH requests write-through behavior, but callers
// must still call SyncDir because the Win32 documentation does not guarantee
// same-volume rename namespace durability. Cross-volume moves are rejected.
func (winFS) RenameNoReplace(oldpath, newpath string) error {
	oldAbs, err := filepath.Abs(oldpath)
	if err != nil {
		return fmt.Errorf("resolve rename source %q: %w", oldpath, err)
	}
	newAbs, err := filepath.Abs(newpath)
	if err != nil {
		return fmt.Errorf("resolve rename target %q: %w", newpath, err)
	}
	same, err := sameVolume(oldAbs, newAbs)
	if err != nil {
		return fmt.Errorf("check rename volumes for %q -> %q: %w", oldAbs, newAbs, err)
	}
	if !same {
		return fmt.Errorf("%w: cross-volume rename from %q to %q", kverr.ErrUnsupported, oldAbs, newAbs)
	}

	from, err := syscall.UTF16PtrFromString(oldAbs)
	if err != nil {
		return fmt.Errorf("encode rename source %q: %w", oldAbs, err)
	}
	to, err := syscall.UTF16PtrFromString(newAbs)
	if err != nil {
		return fmt.Errorf("encode rename target %q: %w", newAbs, err)
	}

	r1, _, callErr := procMoveFileExW.Call(
		uintptr(unsafe.Pointer(from)),
		uintptr(unsafe.Pointer(to)),
		uintptr(moveFileWriteThrough),
	)
	runtime.KeepAlive(from)
	runtime.KeepAlive(to)
	if r1 != 0 {
		return nil
	}

	errno, ok := callErr.(syscall.Errno)
	if !ok || errno == 0 {
		errno = syscall.EINVAL
	}
	if errno == syscall.ERROR_FILE_EXISTS || errno == syscall.ERROR_ALREADY_EXISTS {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: fs.ErrExist}
	}
	return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: errno}
}

func sameVolume(a, b string) (bool, error) {
	aRoot, err := volumeRootForPath(a)
	if err != nil {
		return false, err
	}
	bRoot, err := volumeRootForPath(b)
	if err != nil {
		return false, err
	}
	return strings.EqualFold(aRoot, bRoot), nil
}
