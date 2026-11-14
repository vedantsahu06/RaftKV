// Package dbdir defines the Phase 1 database directory layout and initializes a
// genuinely empty database. Recovery of existing artifacts is intentionally not
// implemented in this milestone.
package dbdir

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/vedantsahu06/raftkv/internal/filesystem"
	"github.com/vedantsahu06/raftkv/internal/kverr"
)

const (
	// WALDirName is the WAL segment directory under the database root.
	WALDirName = "wal"

	// SSTablesDirName is the SSTable directory under the database root.
	SSTablesDirName = "sst"

	// ManifestDirName is the Manifest directory under the database root.
	ManifestDirName = "manifest"
)

// Layout is the fixed Phase 1 database directory layout.
type Layout struct {
	Root     string
	WAL      string
	SSTables string
	Manifest string
}

// LayoutFor returns the layout rooted at root.
func LayoutFor(root string) Layout {
	root = filepath.Clean(root)
	return Layout{
		Root:     root,
		WAL:      filepath.Join(root, WALDirName),
		SSTables: filepath.Join(root, SSTablesDirName),
		Manifest: filepath.Join(root, ManifestDirName),
	}
}

// Directories returns the three component directories.
func (l Layout) Directories() []string {
	return []string{l.WAL, l.SSTables, l.Manifest}
}

// Artifact is one existing directory entry that is not part of an empty
// database. Path is slash-separated and relative to the database root.
type Artifact struct {
	Path  string
	IsDir bool
}

// Inventory is the set of existing artifacts under a database root.
type Inventory struct {
	Artifacts []Artifact
}

// Empty reports whether no artifacts were observed.
func (i Inventory) Empty() bool {
	return len(i.Artifacts) == 0
}

// ExistingArtifactsError reports that a database directory contains files that
// would require recovery. It unwraps to kverr.ErrRecoveryRequired.
type ExistingArtifactsError struct {
	Root      string
	Artifacts []Artifact
}

func (e *ExistingArtifactsError) Error() string {
	const displayLimit = 5
	names := make([]string, 0, min(len(e.Artifacts), displayLimit))
	for _, artifact := range e.Artifacts {
		if len(names) == displayLimit {
			break
		}
		names = append(names, artifact.Path)
	}
	if len(e.Artifacts) > displayLimit {
		names = append(names, fmt.Sprintf("... and %d more", len(e.Artifacts)-displayLimit))
	}
	return fmt.Sprintf(
		"raftkv: database directory %q contains existing artifacts (%s); recovery is not implemented",
		e.Root,
		strings.Join(names, ", "),
	)
}

// Unwrap returns kverr.ErrRecoveryRequired.
func (e *ExistingArtifactsError) Unwrap() error {
	return kverr.ErrRecoveryRequired
}

// Inspect reports the artifacts under root. A missing root is empty. A root that
// is not a directory is an invalid-argument error.
func Inspect(fsys filesystem.FS, root string) (Inventory, error) {
	root = filepath.Clean(root)
	var inventory Inventory

	info, err := fsys.Stat(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return inventory, nil
		}
		return inventory, fmt.Errorf("inspect database directory %q: %w", root, err)
	}
	if !info.IsDir() {
		return inventory, fmt.Errorf("%w: database path %q is not a directory", kverr.ErrInvalidArgument, root)
	}

	entries, err := fsys.ReadDir(root)
	if err != nil {
		return inventory, fmt.Errorf("read database directory %q: %w", root, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if isComponentDir(name) && entry.IsDir() {
			childEntries, err := fsys.ReadDir(filepath.Join(root, name))
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				return inventory, fmt.Errorf("read %s directory: %w", name, err)
			}
			for _, child := range childEntries {
				inventory.Artifacts = append(inventory.Artifacts, Artifact{
					Path:  path.Join(name, child.Name()),
					IsDir: child.IsDir(),
				})
			}
			continue
		}
		inventory.Artifacts = append(inventory.Artifacts, Artifact{
			Path:  name,
			IsDir: entry.IsDir(),
		})
	}

	slices.SortFunc(inventory.Artifacts, func(a, b Artifact) int {
		return strings.Compare(a.Path, b.Path)
	})
	return inventory, nil
}

// Initialize creates the fixed database layout at root when root is genuinely
// empty. It refuses to initialize over existing artifacts and never deletes or
// renames anything.
func Initialize(fsys filesystem.FS, root string) (Layout, error) {
	if !filepath.IsAbs(root) {
		return Layout{}, fmt.Errorf("%w: database directory %q must be absolute", kverr.ErrInvalidArgument, root)
	}
	layout := LayoutFor(root)

	inventory, err := Inspect(fsys, layout.Root)
	if err != nil {
		return Layout{}, err
	}
	if !inventory.Empty() {
		return Layout{}, &ExistingArtifactsError{Root: layout.Root, Artifacts: inventory.Artifacts}
	}

	if err := durableMkdirAll(fsys, layout.Root); err != nil {
		return Layout{}, err
	}
	for _, dir := range layout.Directories() {
		if err := durableMkdirAll(fsys, dir); err != nil {
			return Layout{}, err
		}
	}

	// Re-inspect after creation so a concurrent creator cannot make Open
	// silently treat new artifacts as an initialized empty database.
	inventory, err = Inspect(fsys, layout.Root)
	if err != nil {
		return Layout{}, err
	}
	if !inventory.Empty() {
		return Layout{}, &ExistingArtifactsError{Root: layout.Root, Artifacts: inventory.Artifacts}
	}
	return layout, nil
}

func isComponentDir(name string) bool {
	switch name {
	case WALDirName, SSTablesDirName, ManifestDirName:
		return true
	default:
		return false
	}
}

// durableMkdirAll creates dir and any missing ancestors, making each created
// directory entry durable in its parent before continuing.
func durableMkdirAll(fsys filesystem.FS, dir string) error {
	info, err := fsys.Stat(dir)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("%w: %q is not a directory", kverr.ErrInvalidArgument, dir)
		}
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat %q: %w", dir, err)
	}

	var missing []string
	current := dir
	for {
		info, err := fsys.Stat(current)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("%w: %q is not a directory", kverr.ErrInvalidArgument, current)
			}
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("stat %q: %w", current, err)
		}
		missing = append(missing, current)
		parent := filepath.Dir(current)
		if parent == current {
			return fmt.Errorf("cannot create %q: no existing ancestor", dir)
		}
		current = parent
	}

	for i := len(missing) - 1; i >= 0; i-- {
		target := missing[i]
		if err := fsys.Mkdir(target, 0o700); err != nil {
			if errors.Is(err, fs.ErrExist) {
				info, statErr := fsys.Stat(target)
				if statErr != nil {
					return fmt.Errorf("stat %q after concurrent creation: %w", target, statErr)
				}
				if !info.IsDir() {
					return fmt.Errorf("%w: %q is not a directory", kverr.ErrInvalidArgument, target)
				}
				continue
			}
			return fmt.Errorf("create directory %q: %w", target, err)
		}
		if err := fsys.SyncDir(filepath.Dir(target)); err != nil {
			return fmt.Errorf("make directory entry for %q durable: %w", target, err)
		}
	}
	return nil
}
