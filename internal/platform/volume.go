// Package platform selects and qualifies the filesystem backend for the host
// platform.
package platform

import (
	"fmt"
	"strings"

	"github.com/vedantsahu06/raftkv/internal/filesystem"
	"github.com/vedantsahu06/raftkv/internal/kverr"
)

// validateVolume enforces the Phase 1 platform contract: a fixed local NTFS
// volume. Network, removable, optical and other volumes are rejected until
// their durability behavior is validated.
func validateVolume(info filesystem.VolumeInfo) error {
	if !strings.EqualFold(info.FileSystem, "NTFS") {
		return fmt.Errorf(
			"%w: volume %q uses filesystem %q; Phase 1 requires fixed local NTFS",
			kverr.ErrUnsupportedVolume,
			info.VolumePath,
			info.FileSystem,
		)
	}
	if info.DriveType != filesystem.DriveFixed {
		return fmt.Errorf(
			"%w: volume %q has drive type %s; Phase 1 requires a fixed local volume",
			kverr.ErrUnsupportedVolume,
			info.VolumePath,
			info.DriveType,
		)
	}
	return nil
}
