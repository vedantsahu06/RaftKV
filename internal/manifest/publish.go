package manifest

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"slices"
	"sync"

	"github.com/vedantsahu06/raftkv/internal/filesystem"
	"github.com/vedantsahu06/raftkv/internal/kverr"
	"github.com/vedantsahu06/raftkv/internal/sstable"
	"github.com/vedantsahu06/raftkv/internal/types"
)

// ErrConflictingMetadata reports a publication attempt that gives new metadata
// to an SSTable ID already recorded by a valid Manifest version.
var ErrConflictingMetadata = errors.New("raftkv: conflicting SSTable metadata")

// Publisher serializes Manifest version allocation and publication within one
// process.
//
// The mutex prevents two goroutines from choosing the same next version or from
// publishing conflicting SSTable metadata concurrently. Cross-process exclusive
// database ownership is an Engine/platform responsibility and is not provided by
// this type.
type Publisher struct {
	mu   sync.Mutex
	fsys filesystem.FS
	dir  string
}

// NewPublisher validates the Manifest directory and returns a publisher rooted
// at it.
func NewPublisher(fsys filesystem.FS, dir string) (*Publisher, error) {
	if fsys == nil {
		return nil, fmt.Errorf("%w: filesystem must not be nil", kverr.ErrInvalidArgument)
	}
	if dir == "" {
		return nil, fmt.Errorf("%w: Manifest directory must not be empty", kverr.ErrInvalidArgument)
	}

	dir = filepath.Clean(dir)
	info, err := fsys.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("stat Manifest directory %q: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: Manifest path %q is not a directory", kverr.ErrInvalidArgument, dir)
	}

	return &Publisher{fsys: fsys, dir: dir}, nil
}

// Publish durably installs state as the next Manifest version and returns the
// versioned Manifest that was written.
//
// The caller supplies the complete logical state with Version zero. The
// publisher allocates max(observed final and temp versions) + 1, so a failed
// attempt is retried with a new version rather than by overwriting a file.
//
// Protocol:
//
//  1. create an exclusive temporary file;
//  2. write canonical payload plus CRC32C trailer;
//  3. Sync the temporary file;
//  4. close it and rename without replacing an existing final Manifest;
//  5. make the Manifest directory entry durable;
//  6. only then return success.
//
// If a step before the rename fails, the temporary file is removed on a best-
// effort basis. If the rename succeeds but directory durability fails, the final
// file is left in place but Publish reports failure. Recovery may later select
// that surviving valid version; a filename alone is not proof of acknowledged
// publication.
func (p *Publisher) Publish(state Manifest) (Manifest, error) {
	if p == nil {
		return Manifest{}, fmt.Errorf("%w: Manifest publisher is nil", kverr.ErrInvalidArgument)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if state.Version != 0 {
		return Manifest{}, fmt.Errorf("%w: Publish input must not choose a Manifest version", kverr.ErrInvalidArgument)
	}

	scan, err := ScanVersions(p.fsys, p.dir)
	if err != nil {
		return Manifest{}, err
	}
	nextVersion, err := NextVersion(scan)
	if err != nil {
		return Manifest{}, err
	}

	candidate := state.Clone()
	candidate.Version = nextVersion
	if err := candidate.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("%w: %w", kverr.ErrInvalidArgument, err)
	}
	if err := p.checkConflicts(candidate, scan.Final); err != nil {
		return Manifest{}, err
	}

	encoded, err := Encode(candidate)
	if err != nil {
		return Manifest{}, fmt.Errorf("encode Manifest version %d: %w", nextVersion, err)
	}

	finalName, err := FileName(nextVersion)
	if err != nil {
		return Manifest{}, err
	}
	tempName, err := TempFileName(nextVersion)
	if err != nil {
		return Manifest{}, err
	}
	finalPath := filepath.Join(p.dir, finalName)
	tempPath := filepath.Join(p.dir, tempName)

	tempFile, err := p.fsys.CreateExclusive(tempPath)
	if err != nil {
		return Manifest{}, fmt.Errorf("create temporary Manifest %q: %w", tempPath, err)
	}

	closeAttempted := false
	closeTemp := func() error {
		if closeAttempted {
			return nil
		}
		closeAttempted = true
		return tempFile.Close()
	}
	cleanupTemp := func(cause error) error {
		problems := []error{cause}
		if closeErr := closeTemp(); closeErr != nil {
			problems = append(problems, fmt.Errorf("close temporary Manifest %q: %w", tempPath, closeErr))
		}
		if removeErr := p.fsys.Remove(tempPath); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
			problems = append(problems, fmt.Errorf("remove temporary Manifest %q: %w", tempPath, removeErr))
		}
		return errors.Join(problems...)
	}

	written, writeErr := tempFile.Write(encoded)
	if writeErr != nil {
		return Manifest{}, cleanupTemp(fmt.Errorf("write temporary Manifest %q: %w", tempPath, writeErr))
	}
	if written != len(encoded) {
		return Manifest{}, cleanupTemp(fmt.Errorf("write temporary Manifest %q: %w", tempPath, io.ErrShortWrite))
	}
	if err := tempFile.Sync(); err != nil {
		return Manifest{}, cleanupTemp(fmt.Errorf("sync temporary Manifest %q: %w", tempPath, err))
	}
	// The Windows backend opens files without delete sharing, so close the
	// temporary handle before the namespace operation.
	if err := closeTemp(); err != nil {
		return Manifest{}, cleanupTemp(fmt.Errorf("close temporary Manifest %q before rename: %w", tempPath, err))
	}

	if err := p.fsys.RenameNoReplace(tempPath, finalPath); err != nil {
		return Manifest{}, cleanupTemp(fmt.Errorf("publish Manifest %q as %q: %w", tempPath, finalPath, err))
	}

	// File contents were synced, but the renamed directory entry is not durable
	// until this platform operation succeeds. No success may be reported before
	// it. From here on the final name exists, so it is never removed: that would
	// create another uncertain deletion window.
	if err := p.fsys.SyncDir(p.dir); err != nil {
		return Manifest{}, fmt.Errorf("make Manifest directory entry for %q durable: %w", finalPath, err)
	}

	return candidate, nil
}

// Cleanup removes retired final Manifest versions so that at most three valid
// versions remain. It must only be called after the newest publication returned
// success, or after recovery has established an authoritative state.
//
// validate must perform the same full state validation used by recovery,
// including every referenced SSTable. Cleanup deletes fallbacks, so a validator
// that checks Manifest bytes only is not sufficient for a real database.
//
// Only valid versions beyond the newest three are removed. Versions that fail
// validation are retained for diagnosis; removal of a known-invalid version is
// permitted by the design but is not required, and retaining it is safer when
// validity could not be established. Temporary files are not authoritative and
// are left untouched. This function never deletes WAL or SSTable files; those
// deletion classes require their own later coordination.
func (p *Publisher) Cleanup(validate StateValidator) error {
	if p == nil {
		return fmt.Errorf("%w: Manifest publisher is nil", kverr.ErrInvalidArgument)
	}
	if validate == nil {
		return fmt.Errorf("%w: Manifest StateValidator must not be nil", kverr.ErrInvalidArgument)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	scan, err := ScanVersions(p.fsys, p.dir)
	if err != nil {
		return err
	}
	if len(scan.Final) == 0 {
		return nil
	}

	valid := make(map[types.ManifestVersion]bool, len(scan.Final))
	for _, version := range scan.Final {
		m, err := ReadVersion(p.fsys, p.dir, version)
		if err != nil {
			if errors.Is(err, ErrCorrupt) {
				valid[version] = false
				continue
			}
			return err
		}
		if err := validate(m); err != nil {
			valid[version] = false
			continue
		}
		valid[version] = true
	}

	// Walk newest to oldest and keep the newest three valid versions. Invalid
	// versions never count toward the retained set and are never deleted here.
	validKept := 0
	var deleteVersions []types.ManifestVersion
	for i := len(scan.Final) - 1; i >= 0; i-- {
		version := scan.Final[i]
		if !valid[version] {
			continue
		}
		validKept++
		if validKept > RetainedVersions {
			deleteVersions = append(deleteVersions, version)
		}
	}

	if len(deleteVersions) == 0 {
		return nil
	}
	slices.Sort(deleteVersions)

	for _, version := range deleteVersions {
		name, err := FileName(version)
		if err != nil {
			return err
		}
		path := filepath.Join(p.dir, name)
		if err := p.fsys.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("delete retired Manifest version %d at %q: %w", version, path, err)
		}
	}

	// Until this succeeds, a deleted version may reappear after a crash. No data
	// protected only by these versions may be deleted before durable cleanup.
	if err := p.fsys.SyncDir(p.dir); err != nil {
		return fmt.Errorf("make Manifest cleanup in %q durable: %w", p.dir, err)
	}
	return nil
}

// checkConflicts enforces immutable SSTable metadata. A candidate may repeat an
// existing ID only with byte-for-byte equivalent metadata.
//
// Known-corrupt Manifest versions are not authoritative and are ignored here.
// Manifest-level validity is enough for this check: conflicting metadata is
// unsafe even if the old SSTable file is currently missing or damaged.
func (p *Publisher) checkConflicts(candidate Manifest, finalVersions []types.ManifestVersion) error {
	wanted := make(map[types.SSTableID]sstable.Metadata, len(candidate.SSTables))
	for _, table := range candidate.SSTables {
		wanted[table.ID] = table
	}

	for _, version := range finalVersions {
		existing, err := ReadVersion(p.fsys, p.dir, version)
		if err != nil {
			if errors.Is(err, ErrCorrupt) {
				continue
			}
			return err
		}
		for _, oldTable := range existing.SSTables {
			newTable, exists := wanted[oldTable.ID]
			if !exists {
				continue
			}
			if !newTable.Equal(oldTable) {
				return fmt.Errorf(
					"%w: sstable_id %d has different metadata in Manifest version %d",
					ErrConflictingMetadata,
					oldTable.ID,
					version,
				)
			}
		}
	}
	return nil
}
