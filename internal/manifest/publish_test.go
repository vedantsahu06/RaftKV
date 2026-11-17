package manifest

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/vedantsahu06/raftkv/internal/filesystem"
	"github.com/vedantsahu06/raftkv/internal/kverr"
	"github.com/vedantsahu06/raftkv/internal/types"
)

func TestPublishEmptyState(t *testing.T) {
	fsys, dir := newTestManifestDir(t)
	publisher, err := NewPublisher(fsys, dir)
	if err != nil {
		t.Fatalf("NewPublisher(): %v", err)
	}

	published, err := publisher.Publish(emptyPublishState())
	if err != nil {
		t.Fatalf("Publish(): %v", err)
	}
	if published.Version != 1 {
		t.Fatalf("published version = %d, want 1", published.Version)
	}
	if published.FormatVersion != FormatVersion || published.NextSSTableID != 1 || len(published.SSTables) != 0 {
		t.Fatalf("published state = %+v, want an empty Manifest", published)
	}

	path := manifestPath(t, dir, 1)
	requireExists(t, fsys, path)
	requireNotExist(t, fsys, tempManifestPath(t, dir, 1))
	requireNotExist(t, fsys, filepath.Join(dir, "CURRENT"))

	data := readTestFile(t, fsys, path)
	wantData, err := Encode(published)
	if err != nil {
		t.Fatalf("Encode(): %v", err)
	}
	if !bytes.Equal(data, wantData) {
		t.Fatalf("published bytes = %x, want %x", data, wantData)
	}

	decoded, err := Decode(data)
	if err != nil {
		t.Fatalf("Decode(): %v", err)
	}
	if !reflect.DeepEqual(decoded, published) {
		t.Fatalf("Decode() = %+v, want %+v", decoded, published)
	}
}

func TestPublishSequentialVersionsDoNotOverwrite(t *testing.T) {
	fsys, dir := newTestManifestDir(t)
	publisher, err := NewPublisher(fsys, dir)
	if err != nil {
		t.Fatalf("NewPublisher(): %v", err)
	}

	if _, err := publisher.Publish(emptyPublishState()); err != nil {
		t.Fatalf("Publish(empty): %v", err)
	}
	firstBytes := readTestFile(t, fsys, manifestPath(t, dir, 1))

	oneTable := stateWithTables(t, 0, 2, testMetadata(t, 1, 1, 10, 1))
	published2, err := publisher.Publish(oneTable)
	if err != nil {
		t.Fatalf("Publish(one table): %v", err)
	}
	if published2.Version != 2 {
		t.Fatalf("second published version = %d, want 2", published2.Version)
	}

	twoTables := stateWithTables(t, 0, 3,
		testMetadata(t, 1, 1, 10, 1),
		testMetadata(t, 2, 11, 20, 2),
	)
	published3, err := publisher.Publish(twoTables)
	if err != nil {
		t.Fatalf("Publish(two tables): %v", err)
	}
	if published3.Version != 3 {
		t.Fatalf("third published version = %d, want 3", published3.Version)
	}

	if got := readTestFile(t, fsys, manifestPath(t, dir, 1)); !bytes.Equal(got, firstBytes) {
		t.Fatal("publishing later versions modified Manifest version 1")
	}
	requireVersions(t, fsys, dir, 1, 2, 3)
	requireNoTemporaryVersions(t, fsys, dir)

	latest, err := LoadLatest(fsys, dir, acceptAllManifests)
	if err != nil {
		t.Fatalf("LoadLatest(): %v", err)
	}
	if latest.Version != 3 || len(latest.SSTables) != 2 {
		t.Fatalf("LoadLatest() = %+v, want version 3 with two SSTables", latest)
	}
}

func TestPublishAllocatesAfterTemporaryVersion(t *testing.T) {
	fsys, dir := newTestManifestDir(t)
	tempData, err := Encode(emptyManifest(5))
	if err != nil {
		t.Fatalf("Encode(): %v", err)
	}
	writeTempVersionFile(t, fsys, dir, 5, tempData)

	publisher, err := NewPublisher(fsys, dir)
	if err != nil {
		t.Fatalf("NewPublisher(): %v", err)
	}
	published, err := publisher.Publish(emptyPublishState())
	if err != nil {
		t.Fatalf("Publish(): %v", err)
	}
	if published.Version != 6 {
		t.Fatalf("published version = %d, want 6 (temporary version 5 must be consumed)", published.Version)
	}
	requireExists(t, fsys, tempManifestPath(t, dir, 5))
	requireExists(t, fsys, manifestPath(t, dir, 6))
	requireVersions(t, fsys, dir, 6)
}

func TestPublishRejectsInvalidState(t *testing.T) {
	tests := []struct {
		name  string
		state Manifest
	}{
		{
			name: "caller chooses version",
			state: func() Manifest {
				state := emptyPublishState()
				state.Version = 4
				return state
			}(),
		},
		{
			name: "unsupported format",
			state: func() Manifest {
				state := emptyPublishState()
				state.FormatVersion = 2
				return state
			}(),
		},
		{
			name: "next ID zero",
			state: func() Manifest {
				state := emptyPublishState()
				state.NextSSTableID = 0
				return state
			}(),
		},
		{
			name: "invalid SSTable metadata",
			state: func() Manifest {
				table := testMetadata(t, 1, 1, 10, 1)
				table.Level = 1
				return stateWithTables(t, 0, 2, table)
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys, dir := newTestManifestDir(t)
			publisher, err := NewPublisher(fsys, dir)
			if err != nil {
				t.Fatalf("NewPublisher(): %v", err)
			}

			_, err = publisher.Publish(tt.state)
			if !errors.Is(err, kverr.ErrInvalidArgument) {
				t.Fatalf("Publish() = %v, want ErrInvalidArgument", err)
			}
			requireNoFinalVersions(t, fsys, dir)
			requireNoTemporaryVersions(t, fsys, dir)
		})
	}
}

func TestNewPublisherValidatesDirectory(t *testing.T) {
	fsys, dir := newTestManifestDir(t)

	if _, err := NewPublisher(nil, dir); !errors.Is(err, kverr.ErrInvalidArgument) {
		t.Fatalf("NewPublisher(nil) = %v, want ErrInvalidArgument", err)
	}
	if _, err := NewPublisher(fsys, ""); !errors.Is(err, kverr.ErrInvalidArgument) {
		t.Fatalf("NewPublisher(empty dir) = %v, want ErrInvalidArgument", err)
	}

	notDirectory := filepath.Join(dir, "not-a-directory")
	writeTestFile(t, fsys, notDirectory, []byte("x"))
	if _, err := NewPublisher(fsys, notDirectory); !errors.Is(err, kverr.ErrInvalidArgument) {
		t.Fatalf("NewPublisher(file path) = %v, want ErrInvalidArgument", err)
	}

	if _, err := NewPublisher(fsys, filepath.Join(dir, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("NewPublisher(missing path) = %v, want fs.ErrNotExist", err)
	}
}

func TestPublishPublicationOrder(t *testing.T) {
	fsys, dir := newTestManifestDir(t)
	faults := &faultFS{FS: fsys}
	publisher, err := NewPublisher(faults, dir)
	if err != nil {
		t.Fatalf("NewPublisher(): %v", err)
	}

	if _, err := publisher.Publish(emptyPublishState()); err != nil {
		t.Fatalf("Publish(): %v", err)
	}

	want := []string{
		"readdir",
		"create",
		"file.write",
		"file.sync",
		"file.close",
		"rename",
		"syncdir",
	}
	if !slices.Equal(faults.calls, want) {
		t.Fatalf("publication call order = %v, want %v", faults.calls, want)
	}
}

func TestPublishFailureStages(t *testing.T) {
	injected := errors.New("injected I/O failure")

	tests := []struct {
		name        string
		arrange     func(*faultFS)
		wantErr     error
		finalExists bool
	}{
		{
			name:    "read Manifest directory",
			arrange: func(f *faultFS) { f.readDirErr = injected },
			wantErr: injected,
		},
		{
			name:    "create temporary file",
			arrange: func(f *faultFS) { f.createErr = injected },
			wantErr: injected,
		},
		{
			name:    "write temporary file",
			arrange: func(f *faultFS) { f.writeErr = injected },
			wantErr: injected,
		},
		{
			name:    "short write",
			arrange: func(f *faultFS) { f.shortWrite = true },
			wantErr: io.ErrShortWrite,
		},
		{
			name:    "sync temporary file",
			arrange: func(f *faultFS) { f.syncErr = injected },
			wantErr: injected,
		},
		{
			name:    "close temporary file",
			arrange: func(f *faultFS) { f.closeErr = injected },
			wantErr: injected,
		},
		{
			name:    "rename final Manifest",
			arrange: func(f *faultFS) { f.renameErr = injected },
			wantErr: injected,
		},
		{
			name:        "make directory entry durable",
			arrange:     func(f *faultFS) { f.syncDirErr = injected },
			wantErr:     injected,
			finalExists: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys, dir := newTestManifestDir(t)
			faults := &faultFS{FS: fsys}
			tt.arrange(faults)
			publisher, err := NewPublisher(faults, dir)
			if err != nil {
				t.Fatalf("NewPublisher(): %v", err)
			}

			_, err = publisher.Publish(emptyPublishState())
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Publish() = %v, want %v", err, tt.wantErr)
			}
			if got := pathExists(t, fsys, manifestPath(t, dir, 1)); got != tt.finalExists {
				t.Fatalf("final Manifest exists = %v, want %v", got, tt.finalExists)
			}
			requireNotExist(t, fsys, tempManifestPath(t, dir, 1))
		})
	}
}

func TestPublishDirectorySyncFailureLeavesSurvivingVersion(t *testing.T) {
	fsys, dir := newTestManifestDir(t)
	injected := errors.New("injected directory sync failure")
	faults := &faultFS{FS: fsys, syncDirErr: injected}
	publisher, err := NewPublisher(faults, dir)
	if err != nil {
		t.Fatalf("NewPublisher(): %v", err)
	}

	_, err = publisher.Publish(emptyPublishState())
	if !errors.Is(err, injected) {
		t.Fatalf("Publish() = %v, want injected error", err)
	}
	requireExists(t, fsys, manifestPath(t, dir, 1))

	// A crash after rename but before directory durability may leave a complete
	// valid higher version. Recovery may adopt it, but publication was not
	// acknowledged.
	latest, err := LoadLatest(fsys, dir, acceptAllManifests)
	if err != nil {
		t.Fatalf("LoadLatest(): %v", err)
	}
	if latest.Version != 1 {
		t.Fatalf("LoadLatest() version = %d, want 1", latest.Version)
	}

	// A retry must use a new version rather than overwrite the surviving name.
	realPublisher, err := NewPublisher(fsys, dir)
	if err != nil {
		t.Fatalf("NewPublisher(real): %v", err)
	}
	retried, err := realPublisher.Publish(emptyPublishState())
	if err != nil {
		t.Fatalf("Publish(retry): %v", err)
	}
	if retried.Version != 2 {
		t.Fatalf("retry version = %d, want 2", retried.Version)
	}
	requireVersions(t, fsys, dir, 1, 2)
}

func TestPublishRejectsConflictingMetadata(t *testing.T) {
	fsys, dir := newTestManifestDir(t)
	publisher, err := NewPublisher(fsys, dir)
	if err != nil {
		t.Fatalf("NewPublisher(): %v", err)
	}

	originalTable := testMetadata(t, 1, 1, 10, 1)
	original := stateWithTables(t, 0, 2, originalTable)
	if _, err := publisher.Publish(original); err != nil {
		t.Fatalf("Publish(original): %v", err)
	}
	if _, err := publisher.Publish(original); err != nil {
		t.Fatalf("Publish(identical re-publication): %v", err)
	}
	requireVersions(t, fsys, dir, 1, 2)

	conflictingTable := originalTable.Clone()
	conflictingTable.FileSize++
	conflicting := stateWithTables(t, 0, 2, conflictingTable)
	_, err = publisher.Publish(conflicting)
	if !errors.Is(err, ErrConflictingMetadata) {
		t.Fatalf("Publish(conflicting) = %v, want ErrConflictingMetadata", err)
	}
	requireVersions(t, fsys, dir, 1, 2)
	requireNoTemporaryVersions(t, fsys, dir)
}

func TestPublishChecksConflictsAgainstOlderManifestVersions(t *testing.T) {
	fsys, dir := newTestManifestDir(t)
	originalTable := testMetadata(t, 1, 1, 10, 1)
	writeVersionFile(t, fsys, dir, stateWithTables(t, 1, 2, originalTable))
	writeVersionFile(t, fsys, dir, emptyManifest(2))

	publisher, err := NewPublisher(fsys, dir)
	if err != nil {
		t.Fatalf("NewPublisher(): %v", err)
	}
	conflictingTable := originalTable.Clone()
	conflictingTable.Checksum++
	conflicting := stateWithTables(t, 0, 2, conflictingTable)

	_, err = publisher.Publish(conflicting)
	if !errors.Is(err, ErrConflictingMetadata) {
		t.Fatalf("Publish() = %v, want ErrConflictingMetadata from older version 1", err)
	}
	requireVersions(t, fsys, dir, 1, 2)
}

func TestPublishPropagatesHistoryReadError(t *testing.T) {
	fsys, dir := newTestManifestDir(t)
	writeVersionFile(t, fsys, dir, emptyManifest(1))

	injected := errors.New("injected history read failure")
	faults := &faultFS{FS: fsys, readErr: injected}
	publisher, err := NewPublisher(faults, dir)
	if err != nil {
		t.Fatalf("NewPublisher(): %v", err)
	}

	_, err = publisher.Publish(emptyPublishState())
	if !errors.Is(err, injected) {
		t.Fatalf("Publish() = %v, want injected error", err)
	}
	requireVersions(t, fsys, dir, 1)
	requireNoTemporaryVersions(t, fsys, dir)
}

func TestPublishIgnoresCorruptManifestForConflictCheck(t *testing.T) {
	fsys, dir := newTestManifestDir(t)
	writeRawVersionFile(t, fsys, dir, 1, withTrailer([]byte("corrupt Manifest")))

	publisher, err := NewPublisher(fsys, dir)
	if err != nil {
		t.Fatalf("NewPublisher(): %v", err)
	}
	table := testMetadata(t, 1, 1, 10, 1)
	published, err := publisher.Publish(stateWithTables(t, 0, 2, table))
	if err != nil {
		t.Fatalf("Publish(): %v", err)
	}
	if published.Version != 2 {
		t.Fatalf("published version = %d, want 2", published.Version)
	}
	requireVersions(t, fsys, dir, 1, 2)
}

func TestPublishConcurrentVersions(t *testing.T) {
	fsys, dir := newTestManifestDir(t)
	publisher, err := NewPublisher(fsys, dir)
	if err != nil {
		t.Fatalf("NewPublisher(): %v", err)
	}

	const goroutines = 8
	state := stateWithTables(t, 0, 2, testMetadata(t, 1, 1, 10, 1))
	start := make(chan struct{})
	results := make(chan struct {
		version types.ManifestVersion
		err     error
	}, goroutines)

	var waitGroup sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			published, err := publisher.Publish(state)
			results <- struct {
				version types.ManifestVersion
				err     error
			}{version: published.Version, err: err}
		}()
	}
	close(start)
	waitGroup.Wait()
	close(results)

	var versions []types.ManifestVersion
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent Publish(): %v", result.err)
		}
		versions = append(versions, result.version)
	}
	slices.Sort(versions)
	wantVersions := []types.ManifestVersion{1, 2, 3, 4, 5, 6, 7, 8}
	if !slices.Equal(versions, wantVersions) {
		t.Fatalf("concurrent published versions = %v, want %v", versions, wantVersions)
	}

	requireVersions(t, fsys, dir, wantVersions...)
	requireNoTemporaryVersions(t, fsys, dir)

	latest, err := LoadLatest(fsys, dir, acceptAllManifests)
	if err != nil {
		t.Fatalf("LoadLatest(): %v", err)
	}
	if latest.Version != goroutines || len(latest.SSTables) != 1 {
		t.Fatalf("LoadLatest() = %+v, want version %d with one SSTable", latest, goroutines)
	}
}

func requireVersions(t *testing.T, fsys filesystem.FS, dir string, want ...types.ManifestVersion) {
	t.Helper()

	scan, err := ScanVersions(fsys, dir)
	if err != nil {
		t.Fatalf("ScanVersions(): %v", err)
	}
	if !slices.Equal(scan.Final, want) {
		t.Fatalf("final versions = %v, want %v", scan.Final, want)
	}
}

func requireNoFinalVersions(t *testing.T, fsys filesystem.FS, dir string) {
	t.Helper()
	requireVersions(t, fsys, dir)
}

func requireNoTemporaryVersions(t *testing.T, fsys filesystem.FS, dir string) {
	t.Helper()

	scan, err := ScanVersions(fsys, dir)
	if err != nil {
		t.Fatalf("ScanVersions(): %v", err)
	}
	if len(scan.Temp) != 0 {
		t.Fatalf("temporary versions = %v, want none", scan.Temp)
	}
}
