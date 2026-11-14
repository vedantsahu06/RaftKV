package raftkv

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/vedantsahu06/raftkv/internal/dbdir"
	"github.com/vedantsahu06/raftkv/internal/types"
)

func openTestEngine(t *testing.T) *Engine {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "db")
	engine, err := Open(DefaultConfig(dir))
	if err != nil {
		if errors.Is(err, ErrUnsupported) || errors.Is(err, ErrUnsupportedVolume) {
			t.Skipf("test environment is not supported: %v", err)
		}
		t.Fatalf("Open() = %v", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(context.Background()); err != nil {
			t.Errorf("Close() = %v", err)
		}
	})
	return engine
}

func closeTestEngine(t *testing.T, engine *Engine) {
	t.Helper()
	if err := engine.Close(context.Background()); err != nil {
		t.Fatalf("Close() = %v", err)
	}
}

func TestOpenCreatesLayoutAndNormalizesConfig(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "db")
	engine, err := Open(Config{Dir: dir})
	if err != nil {
		if errors.Is(err, ErrUnsupported) || errors.Is(err, ErrUnsupportedVolume) {
			t.Skipf("test environment is not supported: %v", err)
		}
		t.Fatalf("Open() = %v", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(context.Background()); err != nil {
			t.Errorf("Close() = %v", err)
		}
	})

	if want := DefaultConfig(dir); engine.cfg != want {
		t.Errorf("engine config = %+v, want %+v", engine.cfg, want)
	}
	if want := dbdir.LayoutFor(dir); engine.layout != want {
		t.Errorf("engine layout = %+v, want %+v", engine.layout, want)
	}
	for _, path := range append([]string{dir}, engine.layout.Directories()...) {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %q: %v", path, err)
		}
		if !info.IsDir() {
			t.Fatalf("%q is not a directory", path)
		}
	}
}

func TestOpenRejectsExistingArtifacts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	engine, err := Open(DefaultConfig(dir))
	if err != nil {
		t.Fatalf("first Open() = %v", err)
	}
	closeTestEngine(t, engine)

	artifact := filepath.Join(dir, dbdir.WALDirName, "wal-00000000000000000001.wal")
	if err := os.WriteFile(artifact, []byte("existing"), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	_, err = Open(DefaultConfig(dir))
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("Open() = %v, want ErrRecoveryRequired", err)
	}
	var existingErr *dbdir.ExistingArtifactsError
	if !errors.As(err, &existingErr) {
		t.Fatalf("Open() error = %T, want *dbdir.ExistingArtifactsError", err)
	}
	if len(existingErr.Artifacts) != 1 || existingErr.Artifacts[0].Path != "wal/wal-00000000000000000001.wal" {
		t.Errorf("artifacts = %+v, want wal/wal-00000000000000000001.wal", existingErr.Artifacts)
	}
	if contents, readErr := os.ReadFile(artifact); readErr != nil || string(contents) != "existing" {
		t.Errorf("artifact changed: contents=%q err=%v", contents, readErr)
	}
}

func TestOpenRejectsUnknownRootArtifact(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("unrelated"), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	_, err := Open(DefaultConfig(dir))
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("Open() = %v, want ErrRecoveryRequired", err)
	}
	for _, name := range []string{dbdir.WALDirName, dbdir.SSTablesDirName, dbdir.ManifestDirName} {
		if _, statErr := os.Stat(filepath.Join(dir, name)); !errors.Is(statErr, fs.ErrNotExist) {
			t.Errorf("%s directory was created over existing artifacts: %v", name, statErr)
		}
	}
}

func TestOpenRejectsNonDirectoryPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	if err := os.WriteFile(path, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write path: %v", err)
	}

	_, err := Open(DefaultConfig(path))
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("Open() = %v, want ErrInvalidArgument", err)
	}
	if contents, readErr := os.ReadFile(path); readErr != nil || string(contents) != "not a directory" {
		t.Errorf("path changed: contents=%q err=%v", contents, readErr)
	}
}

func TestOpenRejectsEmptyDirectory(t *testing.T) {
	_, err := Open(Config{})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("Open() = %v, want ErrInvalidArgument", err)
	}
}

func TestDataOperationsNotImplemented(t *testing.T) {
	engine := openTestEngine(t)
	ctx := context.Background()

	if err := engine.Put(ctx, []byte("key"), []byte("value")); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("Put() = %v, want ErrNotImplemented", err)
	}
	value, err := engine.Get(ctx, []byte("key"))
	if !errors.Is(err, ErrNotImplemented) {
		t.Errorf("Get() = %v, want ErrNotImplemented", err)
	}
	if value != nil {
		t.Errorf("Get() value = %q, want nil", value)
	}
	if err := engine.Delete(ctx, []byte("key")); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("Delete() = %v, want ErrNotImplemented", err)
	}
}

func TestOperationValidation(t *testing.T) {
	engine := openTestEngine(t)
	ctx := context.Background()

	if _, err := engine.Get(ctx, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("Get(nil key) = %v, want ErrInvalidArgument", err)
	}
	if _, err := engine.Get(nil, []byte("key")); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("Get(nil context) = %v, want ErrInvalidArgument", err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := engine.Get(canceled, []byte("key")); !errors.Is(err, context.Canceled) {
		t.Errorf("Get(canceled context) = %v, want context.Canceled", err)
	}

	oversizedKey := make([]byte, types.MaxKeySize+1)
	if err := engine.Put(ctx, oversizedKey, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("Put(oversized key) = %v, want ErrInvalidArgument", err)
	}
}

func TestCloseIsIdempotentAndConcurrent(t *testing.T) {
	engine := openTestEngine(t)

	const callers = 16
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- engine.Close(context.Background())
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("Close() = %v, want nil", err)
		}
	}
	if err := engine.Close(context.Background()); err != nil {
		t.Errorf("Close() after completion = %v, want nil", err)
	}

	if err := engine.Put(context.Background(), []byte("key"), nil); !errors.Is(err, ErrClosed) {
		t.Errorf("Put() after Close = %v, want ErrClosed", err)
	}
	if _, err := engine.Get(context.Background(), []byte("key")); !errors.Is(err, ErrClosed) {
		t.Errorf("Get() after Close = %v, want ErrClosed", err)
	}
	if err := engine.Delete(context.Background(), []byte("key")); !errors.Is(err, ErrClosed) {
		t.Errorf("Delete() after Close = %v, want ErrClosed", err)
	}
}

func TestCloseNilContext(t *testing.T) {
	engine := openTestEngine(t)
	if err := engine.Close(nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("Close(nil) = %v, want ErrInvalidArgument", err)
	}
}
