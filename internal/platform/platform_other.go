//go:build !windows

package platform

import (
	"fmt"

	"github.com/vedantsahu06/raftkv/internal/filesystem"
	"github.com/vedantsahu06/raftkv/internal/kverr"
)

// New returns an explicit unsupported-platform error. Phase 1 claims support
// only on Windows with fixed local NTFS.
func New(root string) (filesystem.FS, error) {
	return nil, fmt.Errorf("%w: Phase 1 requires Windows with fixed local NTFS (database path %q)", kverr.ErrUnsupported, root)
}
