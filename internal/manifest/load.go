package manifest

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/vedantsahu06/raftkv/internal/filesystem"
	"github.com/vedantsahu06/raftkv/internal/kverr"
	"github.com/vedantsahu06/raftkv/internal/types"
)

// ErrNoValidManifest reports that no available final Manifest passed validation.
var ErrNoValidManifest = errors.New("raftkv: no valid Manifest version")

// StateValidator applies the validation that cannot be performed from Manifest
// bytes alone. A recovery caller must check that every referenced SSTable exists
// and passes full-file validation, including Manifest metadata agreement.
//
// The validator must not modify the Manifest it receives.
type StateValidator func(Manifest) error

// ReadVersion reads one final Manifest version and validates its filename
// agreement, CRC32C trailer, canonical JSON and Manifest schema.
//
// It does not validate referenced SSTable files. Callers that need recovery
// validity must apply a StateValidator as well.
func ReadVersion(fsys filesystem.FS, dir string, version types.ManifestVersion) (Manifest, error) {
	if dir == "" {
		return Manifest{}, fmt.Errorf("%w: Manifest directory must not be empty", kverr.ErrInvalidArgument)
	}
	name, err := FileName(version)
	if err != nil {
		return Manifest{}, err
	}
	path := filepath.Join(filepath.Clean(dir), name)

	data, err := readManifestFile(fsys, path)
	if err != nil {
		return Manifest{}, err
	}

	m, err := Decode(data)
	if err != nil {
		return Manifest{}, fmt.Errorf("read Manifest version %d from %q: %w", version, path, err)
	}
	if m.Version != version {
		return Manifest{}, fmt.Errorf(
			"%w: %q contains manifest_version %d, but the filename requires %d",
			ErrCorrupt,
			path,
			m.Version,
			version,
		)
	}
	return m, nil
}

// LoadLatest tries every final Manifest version from highest to lowest and
// returns the first one that passes both Manifest decoding and validate.
//
// Corruption is a candidate failure and triggers fallback. File I/O failures are
// returned immediately because the state of the unreadable candidate is unknown;
// silently skipping it could hide a configuration or platform problem.
//
// A surviving higher version may be selected even if its directory-entry
// durability was never confirmed before a crash: being valid now proves a
// consistent surviving state, not that publication was acknowledged.
func LoadLatest(fsys filesystem.FS, dir string, validate StateValidator) (Manifest, error) {
	if validate == nil {
		return Manifest{}, fmt.Errorf("%w: Manifest StateValidator must not be nil", kverr.ErrInvalidArgument)
	}

	scan, err := ScanVersions(fsys, dir)
	if err != nil {
		return Manifest{}, err
	}
	if len(scan.Final) == 0 {
		return Manifest{}, fmt.Errorf("%w in %q: no final Manifest versions", ErrNoValidManifest, dir)
	}

	var rejected []error
	for i := len(scan.Final) - 1; i >= 0; i-- {
		version := scan.Final[i]

		m, err := ReadVersion(fsys, dir, version)
		if err != nil {
			if errors.Is(err, ErrCorrupt) {
				rejected = append(rejected, fmt.Errorf("Manifest version %d: %w", version, err))
				continue
			}
			return Manifest{}, err
		}
		if err := validate(m); err != nil {
			rejected = append(rejected, fmt.Errorf("Manifest version %d: %w", version, err))
			continue
		}
		return m, nil
	}

	return Manifest{}, fmt.Errorf("%w in %q: %w", ErrNoValidManifest, dir, errors.Join(rejected...))
}

func readManifestFile(fsys filesystem.FS, path string) ([]byte, error) {
	file, err := fsys.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open Manifest file %q: %w", path, err)
	}

	data, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil {
		readErr = fmt.Errorf("read Manifest file %q: %w", path, readErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("close Manifest file %q: %w", path, closeErr)
	}
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	return data, nil
}
