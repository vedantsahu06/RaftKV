package manifest

import (
	"errors"
	"slices"
	"testing"

	"github.com/vedantsahu06/raftkv/internal/kverr"
	"github.com/vedantsahu06/raftkv/internal/types"
)

func TestCleanupRetainsNewestThree(t *testing.T) {
	fsys, dir := newTestManifestDir(t)
	for version := types.ManifestVersion(1); version <= 4; version++ {
		writeVersionFile(t, fsys, dir, emptyManifest(version))
	}

	faults := &faultFS{FS: fsys}
	publisher, err := NewPublisher(faults, dir)
	if err != nil {
		t.Fatalf("NewPublisher(): %v", err)
	}
	if err := publisher.Cleanup(acceptAllManifests); err != nil {
		t.Fatalf("Cleanup(): %v", err)
	}

	requireVersions(t, fsys, dir, 2, 3, 4)
	requireNoTemporaryVersions(t, fsys, dir)
	if !slices.Contains(faults.calls, "remove") {
		t.Fatalf("Cleanup() calls = %v, want a remove", faults.calls)
	}
	if !slices.Contains(faults.calls, "syncdir") {
		t.Fatalf("Cleanup() calls = %v, want directory durability after deletion", faults.calls)
	}
}

func TestCleanupFiveVersionsKeepsNewestThree(t *testing.T) {
	fsys, dir := newTestManifestDir(t)
	for version := types.ManifestVersion(1); version <= 5; version++ {
		writeVersionFile(t, fsys, dir, emptyManifest(version))
	}

	publisher, err := NewPublisher(fsys, dir)
	if err != nil {
		t.Fatalf("NewPublisher(): %v", err)
	}
	if err := publisher.Cleanup(acceptAllManifests); err != nil {
		t.Fatalf("Cleanup(): %v", err)
	}
	requireVersions(t, fsys, dir, 3, 4, 5)
}

func TestCleanupFewerThanThreeVersionsDoesNothing(t *testing.T) {
	fsys, dir := newTestManifestDir(t)
	writeVersionFile(t, fsys, dir, emptyManifest(1))
	writeVersionFile(t, fsys, dir, emptyManifest(2))

	faults := &faultFS{FS: fsys}
	publisher, err := NewPublisher(faults, dir)
	if err != nil {
		t.Fatalf("NewPublisher(): %v", err)
	}
	if err := publisher.Cleanup(acceptAllManifests); err != nil {
		t.Fatalf("Cleanup(): %v", err)
	}

	requireVersions(t, fsys, dir, 1, 2)
	if slices.Contains(faults.calls, "remove") {
		t.Fatalf("Cleanup() calls = %v, want no removal", faults.calls)
	}
	if slices.Contains(faults.calls, "syncdir") {
		t.Fatalf("Cleanup() calls = %v, want no directory sync when nothing was deleted", faults.calls)
	}
}

func TestCleanupRetainsInvalidNewestVersion(t *testing.T) {
	fsys, dir := newTestManifestDir(t)
	writeVersionFile(t, fsys, dir, emptyManifest(1))
	writeVersionFile(t, fsys, dir, emptyManifest(2))
	writeVersionFile(t, fsys, dir, emptyManifest(3))
	writeRawVersionFile(t, fsys, dir, 4, withTrailer([]byte("corrupt newest")))

	publisher, err := NewPublisher(fsys, dir)
	if err != nil {
		t.Fatalf("NewPublisher(): %v", err)
	}
	if err := publisher.Cleanup(acceptAllManifests); err != nil {
		t.Fatalf("Cleanup(): %v", err)
	}

	// Version 4 is invalid, but no valid higher version exists, so it is kept
	// for diagnosis. The three valid versions remain the fallback set.
	requireVersions(t, fsys, dir, 1, 2, 3, 4)
}

func TestCleanupRetainsInvalidVersionWithValidHigherVersion(t *testing.T) {
	fsys, dir := newTestManifestDir(t)
	writeVersionFile(t, fsys, dir, emptyManifest(1))
	writeVersionFile(t, fsys, dir, emptyManifest(2))
	writeRawVersionFile(t, fsys, dir, 3, withTrailer([]byte("corrupt middle")))
	writeVersionFile(t, fsys, dir, emptyManifest(4))

	publisher, err := NewPublisher(fsys, dir)
	if err != nil {
		t.Fatalf("NewPublisher(): %v", err)
	}
	if err := publisher.Cleanup(acceptAllManifests); err != nil {
		t.Fatalf("Cleanup(): %v", err)
	}

	// Invalid version 3 does not count toward the three valid versions, and it
	// is retained for diagnosis rather than being deleted automatically.
	requireVersions(t, fsys, dir, 1, 2, 3, 4)
}

func TestCleanupValidatorControlsValidity(t *testing.T) {
	fsys, dir := newTestManifestDir(t)
	for version := types.ManifestVersion(1); version <= 5; version++ {
		writeVersionFile(t, fsys, dir, emptyManifest(version))
	}

	publisher, err := NewPublisher(fsys, dir)
	if err != nil {
		t.Fatalf("NewPublisher(): %v", err)
	}
	if err := publisher.Cleanup(rejectManifestVersion(5)); err != nil {
		t.Fatalf("Cleanup(): %v", err)
	}

	// Version 5 is rejected by full-state validation and has no valid higher
	// version, so it is retained. Of the valid versions 1..4, the newest three
	// remain.
	requireVersions(t, fsys, dir, 2, 3, 4, 5)
}

func TestCleanupRequiresValidator(t *testing.T) {
	fsys, dir := newTestManifestDir(t)
	writeVersionFile(t, fsys, dir, emptyManifest(1))

	publisher, err := NewPublisher(fsys, dir)
	if err != nil {
		t.Fatalf("NewPublisher(): %v", err)
	}
	if err := publisher.Cleanup(nil); !errors.Is(err, kverr.ErrInvalidArgument) {
		t.Fatalf("Cleanup(nil) = %v, want ErrInvalidArgument", err)
	}
	requireVersions(t, fsys, dir, 1)
}

func TestCleanupRemoveFailurePreservesVersions(t *testing.T) {
	fsys, dir := newTestManifestDir(t)
	for version := types.ManifestVersion(1); version <= 4; version++ {
		writeVersionFile(t, fsys, dir, emptyManifest(version))
	}

	injected := errors.New("injected remove failure")
	faults := &faultFS{FS: fsys, removeErr: injected}
	publisher, err := NewPublisher(faults, dir)
	if err != nil {
		t.Fatalf("NewPublisher(): %v", err)
	}
	err = publisher.Cleanup(acceptAllManifests)
	if !errors.Is(err, injected) {
		t.Fatalf("Cleanup() = %v, want injected error", err)
	}

	requireVersions(t, fsys, dir, 1, 2, 3, 4)
	if slices.Contains(faults.calls, "syncdir") {
		t.Fatalf("Cleanup() calls = %v, want no directory sync after a failed removal", faults.calls)
	}
}

func TestCleanupDirectorySyncFailureReportsFailure(t *testing.T) {
	fsys, dir := newTestManifestDir(t)
	for version := types.ManifestVersion(1); version <= 4; version++ {
		writeVersionFile(t, fsys, dir, emptyManifest(version))
	}

	injected := errors.New("injected directory sync failure")
	faults := &faultFS{FS: fsys, syncDirErr: injected}
	publisher, err := NewPublisher(faults, dir)
	if err != nil {
		t.Fatalf("NewPublisher(): %v", err)
	}
	err = publisher.Cleanup(acceptAllManifests)
	if !errors.Is(err, injected) {
		t.Fatalf("Cleanup() = %v, want injected error", err)
	}

	// The unlink happened in the process, but cleanup is not durably
	// established, so it must report failure. A crash may make version 1
	// reappear.
	requireVersions(t, fsys, dir, 2, 3, 4)
	if !slices.Contains(faults.calls, "syncdir") {
		t.Fatalf("Cleanup() calls = %v, want directory durability attempt", faults.calls)
	}
}

func TestCleanupDoesNotDeleteTemporaryFiles(t *testing.T) {
	fsys, dir := newTestManifestDir(t)
	for version := types.ManifestVersion(1); version <= 4; version++ {
		writeVersionFile(t, fsys, dir, emptyManifest(version))
	}
	tempData, err := Encode(emptyManifest(9))
	if err != nil {
		t.Fatalf("Encode(): %v", err)
	}
	writeTempVersionFile(t, fsys, dir, 9, tempData)

	publisher, err := NewPublisher(fsys, dir)
	if err != nil {
		t.Fatalf("NewPublisher(): %v", err)
	}
	if err := publisher.Cleanup(acceptAllManifests); err != nil {
		t.Fatalf("Cleanup(): %v", err)
	}

	requireVersions(t, fsys, dir, 2, 3, 4)
	requireExists(t, fsys, tempManifestPath(t, dir, 9))
}

func TestCleanupNoVersions(t *testing.T) {
	fsys, dir := newTestManifestDir(t)
	publisher, err := NewPublisher(fsys, dir)
	if err != nil {
		t.Fatalf("NewPublisher(): %v", err)
	}
	if err := publisher.Cleanup(acceptAllManifests); err != nil {
		t.Fatalf("Cleanup(): %v", err)
	}
	requireNoFinalVersions(t, fsys, dir)
}

func TestCleanupPropagatesIOErrors(t *testing.T) {
	injected := errors.New("injected I/O failure")

	t.Run("directory read", func(t *testing.T) {
		fsys, dir := newTestManifestDir(t)
		faults := &faultFS{FS: fsys, readDirErr: injected}
		publisher, err := NewPublisher(faults, dir)
		if err != nil {
			t.Fatalf("NewPublisher(): %v", err)
		}
		if err := publisher.Cleanup(acceptAllManifests); !errors.Is(err, injected) {
			t.Fatalf("Cleanup() = %v, want injected error", err)
		}
	})

	t.Run("Manifest file open", func(t *testing.T) {
		fsys, dir := newTestManifestDir(t)
		writeVersionFile(t, fsys, dir, emptyManifest(1))
		faults := &faultFS{FS: fsys, openErr: injected}
		publisher, err := NewPublisher(faults, dir)
		if err != nil {
			t.Fatalf("NewPublisher(): %v", err)
		}
		if err := publisher.Cleanup(acceptAllManifests); !errors.Is(err, injected) {
			t.Fatalf("Cleanup() = %v, want injected error", err)
		}
		requireExists(t, fsys, manifestPath(t, dir, 1))
	})
}

func TestCleanupNilPublisher(t *testing.T) {
	var publisher *Publisher
	if err := publisher.Cleanup(acceptAllManifests); !errors.Is(err, kverr.ErrInvalidArgument) {
		t.Fatalf("Cleanup() = %v, want ErrInvalidArgument", err)
	}
}
