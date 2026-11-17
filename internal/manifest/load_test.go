package manifest

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vedantsahu06/raftkv/internal/filesystem"
	"github.com/vedantsahu06/raftkv/internal/kverr"
	"github.com/vedantsahu06/raftkv/internal/types"
)

func TestReadVersion(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		fsys, dir := newTestManifestDir(t)
		want := stateWithTables(t, 2, 2, testMetadata(t, 1, 1, 10, 1))
		writeVersionFile(t, fsys, dir, want)

		got, err := ReadVersion(fsys, dir, 2)
		if err != nil {
			t.Fatalf("ReadVersion(2): %v", err)
		}
		if got.Version != want.Version || got.FlushedThroughSeq != want.FlushedThroughSeq {
			t.Fatalf("ReadVersion(2) = %+v, want %+v", got, want)
		}
	})

	t.Run("missing", func(t *testing.T) {
		fsys, dir := newTestManifestDir(t)
		_, err := ReadVersion(fsys, dir, 5)
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("ReadVersion(5) = %v, want fs.ErrNotExist", err)
		}
	})

	t.Run("version zero", func(t *testing.T) {
		fsys, dir := newTestManifestDir(t)
		_, err := ReadVersion(fsys, dir, 0)
		if !errors.Is(err, kverr.ErrInvalidArgument) {
			t.Fatalf("ReadVersion(0) = %v, want ErrInvalidArgument", err)
		}
	})

	t.Run("filename and content version disagree", func(t *testing.T) {
		fsys, dir := newTestManifestDir(t)
		data, err := Encode(emptyManifest(3))
		if err != nil {
			t.Fatalf("Encode(): %v", err)
		}
		writeRawVersionFile(t, fsys, dir, 2, data)

		_, err = ReadVersion(fsys, dir, 2)
		if !errors.Is(err, ErrCorrupt) {
			t.Fatalf("ReadVersion(2) = %v, want ErrCorrupt", err)
		}
	})
}

func TestLoadLatestSelectsHighest(t *testing.T) {
	fsys, dir := newTestManifestDir(t)
	writeVersionFile(t, fsys, dir, emptyManifest(1))
	writeVersionFile(t, fsys, dir, emptyManifest(2))
	want := emptyManifest(3)
	want.NextSSTableID = 9
	writeVersionFile(t, fsys, dir, want)

	var validated []types.ManifestVersion
	got, err := LoadLatest(fsys, dir, func(m Manifest) error {
		validated = append(validated, m.Version)
		return nil
	})
	if err != nil {
		t.Fatalf("LoadLatest(): %v", err)
	}
	if got.Version != 3 || got.NextSSTableID != 9 {
		t.Fatalf("LoadLatest() = %+v, want version 3 with NextSSTableID 9", got)
	}
	if len(validated) != 1 || validated[0] != 3 {
		t.Fatalf("validator calls = %v, want [3]", validated)
	}
}

func TestLoadLatestFourVersionRecovery(t *testing.T) {
	// Four final versions may exist transiently between publication and
	// cleanup. Recovery must consider all of them, highest first.
	t.Run("selects transient fourth", func(t *testing.T) {
		fsys, dir := newTestManifestDir(t)
		for version := types.ManifestVersion(1); version <= 4; version++ {
			writeVersionFile(t, fsys, dir, emptyManifest(version))
		}

		got, err := LoadLatest(fsys, dir, acceptAllManifests)
		if err != nil {
			t.Fatalf("LoadLatest(): %v", err)
		}
		if got.Version != 4 {
			t.Fatalf("LoadLatest() version = %d, want 4", got.Version)
		}
	})

	t.Run("falls back from corrupt fourth", func(t *testing.T) {
		fsys, dir := newTestManifestDir(t)
		for version := types.ManifestVersion(1); version <= 3; version++ {
			writeVersionFile(t, fsys, dir, emptyManifest(version))
		}
		data, err := Encode(emptyManifest(4))
		if err != nil {
			t.Fatalf("Encode(): %v", err)
		}
		data[len(data)-1] ^= 0xff
		writeRawVersionFile(t, fsys, dir, 4, data)

		requireLoadVersion(t, fsys, dir, 3)
	})
}

func TestLoadLatestFallsBackFromInvalidNewest(t *testing.T) {
	t.Run("bad checksum", func(t *testing.T) {
		fsys, dir := newTestManifestDir(t)
		writeVersionFile(t, fsys, dir, emptyManifest(1))
		data, err := Encode(emptyManifest(2))
		if err != nil {
			t.Fatalf("Encode(): %v", err)
		}
		data[len(data)-1] ^= 0xff
		writeRawVersionFile(t, fsys, dir, 2, data)
		requireLoadVersion(t, fsys, dir, 1)
	})

	t.Run("malformed JSON", func(t *testing.T) {
		fsys, dir := newTestManifestDir(t)
		writeVersionFile(t, fsys, dir, emptyManifest(1))
		writeRawVersionFile(t, fsys, dir, 2, withTrailer([]byte("not json")))
		requireLoadVersion(t, fsys, dir, 1)
	})

	t.Run("schema violation", func(t *testing.T) {
		fsys, dir := newTestManifestDir(t)
		writeVersionFile(t, fsys, dir, emptyManifest(1))
		payload := replaceOnce(t, emptyManifestPayload, `"next_sstable_id":1`, `"next_sstable_id":0`)
		writeRawVersionFile(t, fsys, dir, 2, withTrailer([]byte(payload)))
		requireLoadVersion(t, fsys, dir, 1)
	})

	t.Run("filename and content version disagree", func(t *testing.T) {
		fsys, dir := newTestManifestDir(t)
		writeVersionFile(t, fsys, dir, emptyManifest(1))
		data, err := Encode(emptyManifest(7))
		if err != nil {
			t.Fatalf("Encode(): %v", err)
		}
		writeRawVersionFile(t, fsys, dir, 2, data)
		requireLoadVersion(t, fsys, dir, 1)
	})

	t.Run("state validator rejects newest", func(t *testing.T) {
		fsys, dir := newTestManifestDir(t)
		writeVersionFile(t, fsys, dir, emptyManifest(1))
		writeVersionFile(t, fsys, dir, emptyManifest(2))

		got, err := LoadLatest(fsys, dir, rejectManifestVersion(2))
		if err != nil {
			t.Fatalf("LoadLatest(): %v", err)
		}
		if got.Version != 1 {
			t.Fatalf("LoadLatest() version = %d, want 1", got.Version)
		}
	})
}

func TestLoadLatestIgnoresTempAndMalformedNames(t *testing.T) {
	fsys, dir := newTestManifestDir(t)
	writeVersionFile(t, fsys, dir, emptyManifest(1))

	tempData, err := Encode(emptyManifest(2))
	if err != nil {
		t.Fatalf("Encode(): %v", err)
	}
	writeTempVersionFile(t, fsys, dir, 2, tempData)
	writeTestFile(t, fsys, filepath.Join(dir, "MANIFEST-2"), tempData)
	writeTestFile(t, fsys, filepath.Join(dir, "CURRENT"), tempData)

	got, err := LoadLatest(fsys, dir, acceptAllManifests)
	if err != nil {
		t.Fatalf("LoadLatest(): %v", err)
	}
	if got.Version != 1 {
		t.Fatalf("LoadLatest() version = %d, want 1 (temp and malformed names are not authoritative)", got.Version)
	}
}

func TestLoadLatestAllInvalid(t *testing.T) {
	fsys, dir := newTestManifestDir(t)
	writeRawVersionFile(t, fsys, dir, 1, withTrailer([]byte("bad one")))
	writeRawVersionFile(t, fsys, dir, 2, withTrailer([]byte("bad two")))

	_, err := LoadLatest(fsys, dir, acceptAllManifests)
	if !errors.Is(err, ErrNoValidManifest) {
		t.Fatalf("LoadLatest() = %v, want ErrNoValidManifest", err)
	}
	if !strings.Contains(err.Error(), "Manifest version 2") || !strings.Contains(err.Error(), "Manifest version 1") {
		t.Fatalf("LoadLatest() error %q does not describe both rejected versions", err)
	}
}

func TestLoadLatestNoFinalVersions(t *testing.T) {
	fsys, dir := newTestManifestDir(t)
	_, err := LoadLatest(fsys, dir, acceptAllManifests)
	if !errors.Is(err, ErrNoValidManifest) {
		t.Fatalf("LoadLatest() = %v, want ErrNoValidManifest", err)
	}
}

func TestLoadLatestRequiresValidator(t *testing.T) {
	fsys, dir := newTestManifestDir(t)
	_, err := LoadLatest(fsys, dir, nil)
	if !errors.Is(err, kverr.ErrInvalidArgument) {
		t.Fatalf("LoadLatest(nil validator) = %v, want ErrInvalidArgument", err)
	}
}

func TestLoadLatestPropagatesIOErrors(t *testing.T) {
	injected := errors.New("injected I/O failure")

	t.Run("directory read", func(t *testing.T) {
		fsys, dir := newTestManifestDir(t)
		faults := &faultFS{FS: fsys, readDirErr: injected}
		_, err := LoadLatest(faults, dir, acceptAllManifests)
		if !errors.Is(err, injected) {
			t.Fatalf("LoadLatest() = %v, want injected error", err)
		}
		if errors.Is(err, ErrNoValidManifest) {
			t.Fatalf("LoadLatest() = %v, must not report I/O failure as no valid Manifest", err)
		}
	})

	t.Run("Manifest file open", func(t *testing.T) {
		fsys, dir := newTestManifestDir(t)
		writeVersionFile(t, fsys, dir, emptyManifest(1))
		faults := &faultFS{FS: fsys, openErr: injected}
		_, err := LoadLatest(faults, dir, acceptAllManifests)
		if !errors.Is(err, injected) {
			t.Fatalf("LoadLatest() = %v, want injected error", err)
		}
		if errors.Is(err, ErrNoValidManifest) {
			t.Fatalf("LoadLatest() = %v, must not report I/O failure as no valid Manifest", err)
		}
	})

	t.Run("Manifest file read", func(t *testing.T) {
		fsys, dir := newTestManifestDir(t)
		writeVersionFile(t, fsys, dir, emptyManifest(1))
		faults := &faultFS{FS: fsys, readErr: injected}
		_, err := LoadLatest(faults, dir, acceptAllManifests)
		if !errors.Is(err, injected) {
			t.Fatalf("LoadLatest() = %v, want injected error", err)
		}
		if errors.Is(err, ErrNoValidManifest) {
			t.Fatalf("LoadLatest() = %v, must not report I/O failure as no valid Manifest", err)
		}
	})

	t.Run("Manifest file close", func(t *testing.T) {
		fsys, dir := newTestManifestDir(t)
		writeVersionFile(t, fsys, dir, emptyManifest(1))
		faults := &faultFS{FS: fsys, closeErr: injected}
		_, err := LoadLatest(faults, dir, acceptAllManifests)
		if !errors.Is(err, injected) {
			t.Fatalf("LoadLatest() = %v, want injected error", err)
		}
		if errors.Is(err, ErrNoValidManifest) {
			t.Fatalf("LoadLatest() = %v, must not report I/O failure as no valid Manifest", err)
		}
	})
}

func requireLoadVersion(t *testing.T, fsys filesystem.FS, dir string, want types.ManifestVersion) {
	t.Helper()

	got, err := LoadLatest(fsys, dir, acceptAllManifests)
	if err != nil {
		t.Fatalf("LoadLatest(): %v", err)
	}
	if got.Version != want {
		t.Fatalf("LoadLatest() version = %d, want %d", got.Version, want)
	}
}
