package manifest

import (
	"io/fs"

	"github.com/vedantsahu06/raftkv/internal/filesystem"
)

// faultFS wraps a real filesystem and injects failures at named operations.
// Tests use it to model process crashes and I/O errors without changing the
// production filesystem abstraction.
type faultFS struct {
	filesystem.FS

	calls []string

	createErr  error
	renameErr  error
	syncDirErr error
	removeErr  error
	readDirErr error
	openErr    error
	readErr    error

	writeErr   error
	syncErr    error
	closeErr   error
	shortWrite bool
}

func (f *faultFS) log(call string) {
	f.calls = append(f.calls, call)
}

func (f *faultFS) ReadDir(path string) ([]fs.DirEntry, error) {
	f.log("readdir")
	if f.readDirErr != nil {
		return nil, f.readDirErr
	}
	return f.FS.ReadDir(path)
}

func (f *faultFS) Open(path string) (filesystem.File, error) {
	f.log("open")
	if f.openErr != nil {
		return nil, f.openErr
	}
	file, err := f.FS.Open(path)
	if err != nil {
		return nil, err
	}
	return &faultFile{File: file, fs: f}, nil
}

func (f *faultFS) CreateExclusive(path string) (filesystem.File, error) {
	f.log("create")
	if f.createErr != nil {
		return nil, f.createErr
	}
	file, err := f.FS.CreateExclusive(path)
	if err != nil {
		return nil, err
	}
	return &faultFile{File: file, fs: f}, nil
}

func (f *faultFS) RenameNoReplace(oldpath, newpath string) error {
	f.log("rename")
	if f.renameErr != nil {
		return f.renameErr
	}
	return f.FS.RenameNoReplace(oldpath, newpath)
}

func (f *faultFS) Remove(path string) error {
	f.log("remove")
	if f.removeErr != nil {
		return f.removeErr
	}
	return f.FS.Remove(path)
}

func (f *faultFS) SyncDir(path string) error {
	f.log("syncdir")
	if f.syncDirErr != nil {
		return f.syncDirErr
	}
	return f.FS.SyncDir(path)
}

type faultFile struct {
	filesystem.File
	fs *faultFS
}

func (f *faultFile) Read(p []byte) (int, error) {
	if f.fs.readErr != nil {
		return 0, f.fs.readErr
	}
	return f.File.Read(p)
}

func (f *faultFile) Write(p []byte) (int, error) {
	f.fs.log("file.write")
	if f.fs.writeErr != nil {
		return 0, f.fs.writeErr
	}
	if f.fs.shortWrite {
		if len(p) == 0 {
			return 0, nil
		}
		return len(p) - 1, nil
	}
	return f.File.Write(p)
}

func (f *faultFile) Sync() error {
	f.fs.log("file.sync")
	if f.fs.syncErr != nil {
		return f.fs.syncErr
	}
	return f.File.Sync()
}

func (f *faultFile) Close() error {
	f.fs.log("file.close")
	closeErr := f.File.Close()
	if f.fs.closeErr != nil {
		return f.fs.closeErr
	}
	return closeErr
}

var (
	_ filesystem.FS   = (*faultFS)(nil)
	_ filesystem.File = (*faultFile)(nil)
)
