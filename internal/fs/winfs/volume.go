//go:build windows

package winfs

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"github.com/vedantsahu06/raftkv/internal/filesystem"
)

var (
	kernel32                  = syscall.NewLazyDLL("kernel32.dll")
	procGetVolumePathNameW    = kernel32.NewProc("GetVolumePathNameW")
	procGetDriveTypeW         = kernel32.NewProc("GetDriveTypeW")
	procGetVolumeInformationW = kernel32.NewProc("GetVolumeInformationW")
)

// Qualify reports the volume information for path. The path need not exist; the
// nearest existing ancestor is used.
func (winFS) Qualify(path string) (filesystem.VolumeInfo, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filesystem.VolumeInfo{}, fmt.Errorf("resolve path %q: %w", path, err)
	}
	existing, err := nearestExisting(abs)
	if err != nil {
		return filesystem.VolumeInfo{}, err
	}
	volumePath, err := getVolumePathName(existing)
	if err != nil {
		return filesystem.VolumeInfo{}, fmt.Errorf("find volume for %q: %w", abs, err)
	}
	driveType, err := getDriveType(volumePath)
	if err != nil {
		return filesystem.VolumeInfo{}, fmt.Errorf("read drive type for %q: %w", volumePath, err)
	}
	fileSystem, err := getVolumeFileSystem(volumePath)
	if err != nil {
		return filesystem.VolumeInfo{}, fmt.Errorf("read filesystem for %q: %w", volumePath, err)
	}
	return filesystem.VolumeInfo{
		VolumePath: volumePath,
		FileSystem: fileSystem,
		DriveType:  driveType,
	}, nil
}

func volumeRootForPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	existing, err := nearestExisting(abs)
	if err != nil {
		return "", err
	}
	return getVolumePathName(existing)
}

func nearestExisting(abs string) (string, error) {
	p := filepath.Clean(abs)
	for {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("stat %q while resolving volume: %w", p, err)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", fmt.Errorf("no existing ancestor for %q", abs)
		}
		p = parent
	}
}

func getVolumePathName(path string) (string, error) {
	ptr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return "", fmt.Errorf("encode path %q: %w", path, err)
	}
	buf := make([]uint16, 1024)
	r1, _, callErr := procGetVolumePathNameW.Call(
		uintptr(unsafe.Pointer(ptr)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	runtime.KeepAlive(ptr)
	runtime.KeepAlive(buf)
	if r1 == 0 {
		return "", windowsCallError("GetVolumePathNameW", callErr)
	}
	if buf[0] == 0 {
		return "", fmt.Errorf("GetVolumePathNameW returned an empty volume path for %q", path)
	}
	return syscall.UTF16ToString(buf), nil
}

func getDriveType(root string) (filesystem.DriveType, error) {
	ptr, err := syscall.UTF16PtrFromString(root)
	if err != nil {
		return filesystem.DriveUnknown, fmt.Errorf("encode volume path %q: %w", root, err)
	}
	r1, _, callErr := procGetDriveTypeW.Call(uintptr(unsafe.Pointer(ptr)))
	runtime.KeepAlive(ptr)
	driveType := filesystem.DriveType(r1)
	if driveType == filesystem.DriveUnknown || driveType == filesystem.DriveNoRootDir {
		return driveType, windowsCallError("GetDriveTypeW", callErr)
	}
	return driveType, nil
}

func getVolumeFileSystem(root string) (string, error) {
	ptr, err := syscall.UTF16PtrFromString(root)
	if err != nil {
		return "", fmt.Errorf("encode volume path %q: %w", root, err)
	}
	buf := make([]uint16, 64)
	r1, _, callErr := procGetVolumeInformationW.Call(
		uintptr(unsafe.Pointer(ptr)),
		0,
		0,
		0,
		0,
		0,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	runtime.KeepAlive(ptr)
	runtime.KeepAlive(buf)
	if r1 == 0 {
		return "", windowsCallError("GetVolumeInformationW", callErr)
	}
	return syscall.UTF16ToString(buf), nil
}

func windowsCallError(op string, callErr error) error {
	errno, ok := callErr.(syscall.Errno)
	if !ok || errno == 0 {
		errno = syscall.EINVAL
	}
	return fmt.Errorf("%s: %w", op, errno)
}
