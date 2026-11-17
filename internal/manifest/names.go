package manifest

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/vedantsahu06/raftkv/internal/filesystem"
	"github.com/vedantsahu06/raftkv/internal/kverr"
	"github.com/vedantsahu06/raftkv/internal/types"
)

const (
	filenamePrefix = "MANIFEST-"
	filenameDigits = 20
	tempSuffix     = ".tmp"

	maxManifestVersion = ^types.ManifestVersion(0)
)

// ErrInvalidName reports a filename that is not an exact Manifest v1 final or
// temporary filename.
var ErrInvalidName = errors.New("raftkv: invalid Manifest filename")

// ErrVersionExhausted reports that no larger Manifest version can be allocated.
var ErrVersionExhausted = errors.New("raftkv: Manifest version space exhausted")

// FileName returns the final filename for version, without a directory.
func FileName(version types.ManifestVersion) (string, error) {
	return formatVersionName(version, "")
}

// TempFileName returns the temporary publication filename for version, without
// a directory.
func TempFileName(version types.ManifestVersion) (string, error) {
	return formatVersionName(version, tempSuffix)
}

// ParseFileName parses an exact final Manifest base filename.
func ParseFileName(name string) (types.ManifestVersion, error) {
	return parseVersionName(name, "")
}

// ParseTempFileName parses an exact temporary Manifest base filename.
func ParseTempFileName(name string) (types.ManifestVersion, error) {
	return parseVersionName(name, tempSuffix)
}

// VersionScan reports the final and temporary Manifest names in one directory.
type VersionScan struct {
	// Final is sorted ascending. These are the only authoritative candidates.
	Final []types.ManifestVersion

	// Temp is sorted ascending. Temporary files never become authoritative,
	// but their version numbers are consumed for the next publication attempt.
	Temp []types.ManifestVersion

	// Malformed contains sorted "MANIFEST-" names that match neither exact
	// filename form.
	Malformed []string
}

// ScanVersions reads dir and reports every valid final and temporary Manifest
// version.
func ScanVersions(fsys filesystem.FS, dir string) (VersionScan, error) {
	entries, err := fsys.ReadDir(dir)
	if err != nil {
		return VersionScan{}, fmt.Errorf("read Manifest directory %q: %w", dir, err)
	}

	var scan VersionScan
	for _, entry := range entries {
		name := entry.Name()

		version, parseErr := ParseFileName(name)
		if parseErr == nil {
			if entry.IsDir() {
				scan.Malformed = append(scan.Malformed, name)
			} else {
				scan.Final = append(scan.Final, version)
			}
			continue
		}

		version, parseErr = ParseTempFileName(name)
		if parseErr == nil {
			if entry.IsDir() {
				scan.Malformed = append(scan.Malformed, name)
			} else {
				scan.Temp = append(scan.Temp, version)
			}
			continue
		}

		if strings.HasPrefix(name, filenamePrefix) {
			scan.Malformed = append(scan.Malformed, name)
		}
	}

	slices.Sort(scan.Final)
	slices.Sort(scan.Temp)
	slices.Sort(scan.Malformed)
	return scan, nil
}

// NextVersion returns max(observed final versions, observed temp versions) + 1.
// A stale temporary file therefore consumes its version even though its contents
// are never authoritative.
func NextVersion(scan VersionScan) (types.ManifestVersion, error) {
	var highest types.ManifestVersion
	for _, version := range scan.Final {
		if version > highest {
			highest = version
		}
	}
	for _, version := range scan.Temp {
		if version > highest {
			highest = version
		}
	}

	if highest == maxManifestVersion {
		return 0, ErrVersionExhausted
	}
	return highest + 1, nil
}

func formatVersionName(version types.ManifestVersion, suffix string) (string, error) {
	if version == 0 {
		return "", fmt.Errorf("%w: Manifest version must be at least 1", kverr.ErrInvalidArgument)
	}
	return fmt.Sprintf("%s%020d%s", filenamePrefix, version, suffix), nil
}

func parseVersionName(name, suffix string) (types.ManifestVersion, error) {
	if !strings.HasPrefix(name, filenamePrefix) || !strings.HasSuffix(name, suffix) {
		return 0, fmt.Errorf("%w: %q", ErrInvalidName, name)
	}

	digitsStart := len(filenamePrefix)
	digitsEnd := len(name) - len(suffix)
	if digitsEnd-digitsStart != filenameDigits {
		return 0, fmt.Errorf("%w: %q does not contain %d version digits", ErrInvalidName, name, filenameDigits)
	}

	digits := name[digitsStart:digitsEnd]
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return 0, fmt.Errorf("%w: %q contains a non-decimal version", ErrInvalidName, name)
		}
	}

	version, err := strconv.ParseUint(digits, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q: %v", ErrInvalidName, name, err)
	}
	if version == 0 {
		return 0, fmt.Errorf("%w: %q has Manifest version 0", ErrInvalidName, name)
	}
	return types.ManifestVersion(version), nil
}
