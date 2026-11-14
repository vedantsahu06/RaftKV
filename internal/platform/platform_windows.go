//go:build windows

package platform

import (
	"fmt"
	"path/filepath"

	"github.com/vedantsahu06/raftkv/internal/filesystem"
	"github.com/vedantsahu06/raftkv/internal/fs/winfs"
)

// New returns the Windows filesystem backend after qualifying root's volume as
// fixed local NTFS. root need not exist.
func New(root string) (filesystem.FS, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve database path %q: %w", root, err)
	}
	fsys := winfs.New()
	info, err := fsys.Qualify(abs)
	if err != nil {
		return nil, fmt.Errorf("qualify volume for %q: %w", abs, err)
	}
	if err := validateVolume(info); err != nil {
		return nil, err
	}
	return fsys, nil
}
