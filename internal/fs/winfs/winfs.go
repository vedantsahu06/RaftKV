//go:build windows

// Package winfs implements the Phase 1 filesystem abstraction on Windows.
//
// The backend exposes exclusive creation, file Sync, non-overwriting write-
// through rename, directory-entry durability, deletion and local NTFS volume
// qualification. The directory-entry durability mechanism is documented in
// dirsync.go and is not a claim of verified power-loss durability.
package winfs

import (
	"fmt"
	"io/fs"
	"os"

	"github.com/vedantsahu06/raftkv/internal/filesystem"
)

type winFS struct{}

// New returns the Windows filesystem backend.
func New() filesystem.FS {
	return winFS{}
}

var _ filesystem.FS = winFS{}

type winFile struct {
	f *os.File
}

var _ filesystem.File = (*winFile)(nil)

func (f *winFile) Read(p []byte) (int, error) {
	return f.f.Read(p)
}

func (f *winFile) ReadAt(p []byte, off int64) (int, error) {
	return f.f.ReadAt(p, off)
}

func (f *winFile) Write(p []byte) (int, error) {
	return f.f.Write(p)
}

func (f *winFile) WriteAt(p []byte, off int64) (int, error) {
	return f.f.WriteAt(p, off)
}

func (f *winFile) Seek(offset int64, whence int) (int64, error) {
	return f.f.Seek(offset, whence)
}

func (f *winFile) Sync() error {
	if err := f.f.Sync(); err != nil {
		return fmt.Errorf("sync %q: %w", f.f.Name(), err)
	}
	return nil
}

func (f *winFile) Truncate(size int64) error {
	if err := f.f.Truncate(size); err != nil {
		return fmt.Errorf("truncate %q: %w", f.f.Name(), err)
	}
	return nil
}

func (f *winFile) Name() string {
	return f.f.Name()
}

func (f *winFile) Stat() (fs.FileInfo, error) {
	return f.f.Stat()
}

func (f *winFile) Close() error {
	if err := f.f.Close(); err != nil {
		return fmt.Errorf("close %q: %w", f.f.Name(), err)
	}
	return nil
}

func (winFS) Mkdir(path string, perm fs.FileMode) error {
	if err := os.Mkdir(path, perm); err != nil {
		return fmt.Errorf("mkdir %q: %w", path, err)
	}
	return nil
}

func (winFS) Open(path string) (filesystem.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return &winFile{f: f}, nil
}

func (winFS) OpenFile(path string, flag int, perm fs.FileMode) (filesystem.File, error) {
	f, err := os.OpenFile(path, flag, perm)
	if err != nil {
		return nil, err
	}
	return &winFile{f: f}, nil
}

func (winFS) CreateExclusive(path string) (filesystem.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create exclusive %q: %w", path, err)
	}
	return &winFile{f: f}, nil
}

func (winFS) Remove(path string) error {
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove %q: %w", path, err)
	}
	return nil
}

func (winFS) Stat(path string) (fs.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	return info, nil
}

func (winFS) ReadDir(path string) ([]fs.DirEntry, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	return entries, nil
}
