//go:build windows

package winfs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vedantsahu06/raftkv/internal/filesystem"
	"github.com/vedantsahu06/raftkv/internal/kverr"
)

func writeSyncedFile(t *testing.T, fsys filesystem.FS, path, contents string) {
	t.Helper()
	f, err := fsys.CreateExclusive(path)
	if err != nil {
		t.Fatalf("CreateExclusive(%q): %v", path, err)
	}
	if _, err := f.Write([]byte(contents)); err != nil {
		f.Close()
		t.Fatalf("write %q: %v", path, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		t.Fatalf("sync %q: %v", path, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close %q: %v", path, err)
	}
}

func readFile(t *testing.T, fsys filesystem.FS, path string) string {
	t.Helper()
	f, err := fsys.Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	contents, readErr := io.ReadAll(f)
	closeErr := f.Close()
	if readErr != nil {
		t.Fatalf("ReadAll(%q): %v", path, readErr)
	}
	if closeErr != nil {
		t.Fatalf("Close(%q): %v", path, closeErr)
	}
	return string(contents)
}

func TestCreateExclusiveAndSync(t *testing.T) {
	fsys := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.tmp")

	f, err := fsys.CreateExclusive(path)
	if err != nil {
		t.Fatalf("CreateExclusive() = %v", err)
	}
	if f.Name() != path {
		t.Errorf("Name() = %q, want %q", f.Name(), path)
	}
	if _, err := f.Write([]byte("hello")); err != nil {
		f.Close()
		t.Fatalf("Write() = %v", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		t.Fatalf("Stat() = %v", err)
	}
	if info.Size() != 5 {
		t.Errorf("Stat().Size() = %d, want 5", info.Size())
	}
	if err := f.Sync(); err != nil {
		f.Close()
		t.Fatalf("Sync() = %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	if got := readFile(t, fsys, path); got != "hello" {
		t.Errorf("file contents = %q, want %q", got, "hello")
	}
}

func TestCreateExclusiveDoesNotOverwrite(t *testing.T) {
	fsys := New()
	path := filepath.Join(t.TempDir(), "a.tmp")
	writeSyncedFile(t, fsys, path, "first")

	f, err := fsys.CreateExclusive(path)
	if err == nil {
		f.Close()
		t.Fatal("CreateExclusive() succeeded for an existing file")
	}
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("CreateExclusive() = %v, want fs.ErrExist", err)
	}
	if got := readFile(t, fsys, path); got != "first" {
		t.Errorf("existing file contents = %q, want %q", got, "first")
	}
}

func TestRenameNoReplace(t *testing.T) {
	fsys := New()
	dir := t.TempDir()
	src := filepath.Join(dir, "a.tmp")
	dst := filepath.Join(dir, "a.final")

	writeSyncedFile(t, fsys, src, "first")
	if err := fsys.RenameNoReplace(src, dst); err != nil {
		t.Fatalf("RenameNoReplace() = %v", err)
	}
	if err := fsys.SyncDir(dir); err != nil {
		t.Fatalf("SyncDir() = %v", err)
	}
	if _, err := fsys.Stat(src); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("source still exists after rename: %v", err)
	}
	if got := readFile(t, fsys, dst); got != "first" {
		t.Errorf("renamed contents = %q, want %q", got, "first")
	}

	writeSyncedFile(t, fsys, src, "second")
	err := fsys.RenameNoReplace(src, dst)
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("RenameNoReplace() over existing target = %v, want fs.ErrExist", err)
	}
	if got := readFile(t, fsys, dst); got != "first" {
		t.Errorf("target changed after failed rename: contents = %q, want %q", got, "first")
	}
	if got := readFile(t, fsys, src); got != "second" {
		t.Errorf("source changed after failed rename: contents = %q, want %q", got, "second")
	}
}

func TestRenameNoReplaceConcurrent(t *testing.T) {
	const attempts = 16
	fsys := New()
	dir := t.TempDir()
	dst := filepath.Join(dir, "target.final")

	type result struct {
		payload string
		err     error
	}
	results := make(chan result, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			payload := fmt.Sprintf("payload-%02d", i)
			src := filepath.Join(dir, fmt.Sprintf("src-%02d.tmp", i))
			f, err := fsys.CreateExclusive(src)
			if err != nil {
				results <- result{payload: payload, err: err}
				return
			}
			_, writeErr := f.Write([]byte(payload))
			syncErr := f.Sync()
			closeErr := f.Close()
			if writeErr != nil || syncErr != nil || closeErr != nil {
				results <- result{payload: payload, err: errors.Join(writeErr, syncErr, closeErr)}
				return
			}
			results <- result{payload: payload, err: fsys.RenameNoReplace(src, dst)}
		}(i)
	}
	wg.Wait()
	close(results)

	successes := 0
	winner := ""
	for result := range results {
		if result.err == nil {
			successes++
			winner = result.payload
			continue
		}
		if !errors.Is(result.err, fs.ErrExist) {
			t.Errorf("losing rename error = %v, want fs.ErrExist", result.err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful renames = %d, want 1", successes)
	}
	if got := readFile(t, fsys, dst); got != winner {
		t.Errorf("target contents = %q, want winner %q", got, winner)
	}
	for i := 0; i < attempts; i++ {
		payload := fmt.Sprintf("payload-%02d", i)
		src := filepath.Join(dir, fmt.Sprintf("src-%02d.tmp", i))
		_, err := fsys.Stat(src)
		if payload == winner {
			if !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("winner source still exists: %v", err)
			}
			continue
		}
		if err != nil {
			t.Errorf("losing source missing: %v", err)
			continue
		}
		if got := readFile(t, fsys, src); got != payload {
			t.Errorf("losing source contents = %q, want %q", got, payload)
		}
	}
	if err := fsys.SyncDir(dir); err != nil {
		t.Fatalf("SyncDir() = %v", err)
	}
}

func TestDurablePublicationSequence(t *testing.T) {
	fsys := New()
	dir := t.TempDir()
	temp := filepath.Join(dir, "MANIFEST-00000000000000000001.tmp")
	final := filepath.Join(dir, "MANIFEST-00000000000000000001")

	writeSyncedFile(t, fsys, temp, "first")
	if err := fsys.RenameNoReplace(temp, final); err != nil {
		t.Fatalf("RenameNoReplace() = %v", err)
	}
	if err := fsys.SyncDir(dir); err != nil {
		t.Fatalf("SyncDir() after rename = %v", err)
	}
	if _, err := fsys.Stat(temp); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("temporary file still exists after publication: %v", err)
	}
	if got := readFile(t, fsys, final); got != "first" {
		t.Errorf("published contents = %q, want %q", got, "first")
	}

	writeSyncedFile(t, fsys, temp, "second")
	if err := fsys.RenameNoReplace(temp, final); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("republish over existing final = %v, want fs.ErrExist", err)
	}
	if got := readFile(t, fsys, final); got != "first" {
		t.Errorf("published contents changed after failed republish = %q, want %q", got, "first")
	}

	if err := fsys.Remove(final); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	if err := fsys.SyncDir(dir); err != nil {
		t.Fatalf("SyncDir() after remove = %v", err)
	}
	if _, err := fsys.Stat(final); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("published file still exists after deletion: %v", err)
	}
}

func TestSyncDir(t *testing.T) {
	fsys := New()
	dir := t.TempDir()
	if err := fsys.SyncDir(dir); err != nil {
		t.Fatalf("SyncDir(directory) = %v, want nil", err)
	}

	file := filepath.Join(dir, "file")
	writeSyncedFile(t, fsys, file, "data")
	if err := fsys.SyncDir(file); !errors.Is(err, kverr.ErrInvalidArgument) {
		t.Fatalf("SyncDir(file) = %v, want ErrInvalidArgument", err)
	}
	if err := fsys.SyncDir(filepath.Join(dir, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("SyncDir(missing) = %v, want fs.ErrNotExist", err)
	}
}

func TestRemoveThenSyncDir(t *testing.T) {
	fsys := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.tmp")
	writeSyncedFile(t, fsys, path, "data")

	if err := fsys.Remove(path); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	if err := fsys.SyncDir(dir); err != nil {
		t.Fatalf("SyncDir() after remove = %v", err)
	}
	if _, err := fsys.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Stat() after remove = %v, want fs.ErrNotExist", err)
	}
	if err := fsys.Remove(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Remove(missing) = %v, want fs.ErrNotExist", err)
	}
}

func TestQualifyLocalVolume(t *testing.T) {
	fsys := New()
	info, err := fsys.Qualify(t.TempDir())
	if err != nil {
		t.Fatalf("Qualify() = %v", err)
	}
	if info.VolumePath == "" {
		t.Fatal("Qualify().VolumePath is empty")
	}
	if !strings.EqualFold(info.FileSystem, "NTFS") || info.DriveType != filesystem.DriveFixed {
		t.Skipf("test volume is not fixed local NTFS: volume=%q filesystem=%q driveType=%s", info.VolumePath, info.FileSystem, info.DriveType)
	}
}

func TestOpenFileAndTruncate(t *testing.T) {
	fsys := New()
	path := filepath.Join(t.TempDir(), "a.tmp")
	writeSyncedFile(t, fsys, path, "abcdefghij")

	f, err := fsys.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("OpenFile() = %v", err)
	}
	if err := f.Truncate(4); err != nil {
		f.Close()
		t.Fatalf("Truncate() = %v", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		t.Fatalf("Sync() = %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if got := readFile(t, fsys, path); got != "abcd" {
		t.Errorf("truncated contents = %q, want %q", got, "abcd")
	}
}
