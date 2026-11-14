package platform

import (
	"errors"
	"testing"

	"github.com/vedantsahu06/raftkv/internal/filesystem"
	"github.com/vedantsahu06/raftkv/internal/kverr"
)

func TestValidateVolume(t *testing.T) {
	tests := []struct {
		name    string
		info    filesystem.VolumeInfo
		wantErr bool
	}{
		{
			name: "fixed NTFS",
			info: filesystem.VolumeInfo{VolumePath: `C:\`, FileSystem: "NTFS", DriveType: filesystem.DriveFixed},
		},
		{
			name: "case-insensitive NTFS",
			info: filesystem.VolumeInfo{VolumePath: `C:\`, FileSystem: "ntfs", DriveType: filesystem.DriveFixed},
		},
		{
			name:    "remote NTFS",
			info:    filesystem.VolumeInfo{VolumePath: `\\server\share\`, FileSystem: "NTFS", DriveType: filesystem.DriveRemote},
			wantErr: true,
		},
		{
			name:    "removable NTFS",
			info:    filesystem.VolumeInfo{VolumePath: `E:\`, FileSystem: "NTFS", DriveType: filesystem.DriveRemovable},
			wantErr: true,
		},
		{
			name:    "fixed exFAT",
			info:    filesystem.VolumeInfo{VolumePath: `D:\`, FileSystem: "exFAT", DriveType: filesystem.DriveFixed},
			wantErr: true,
		},
		{
			name:    "unknown drive type",
			info:    filesystem.VolumeInfo{VolumePath: `C:\`, FileSystem: "NTFS", DriveType: filesystem.DriveUnknown},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateVolume(tt.info)
			if tt.wantErr {
				if !errors.Is(err, kverr.ErrUnsupportedVolume) {
					t.Fatalf("validateVolume() = %v, want ErrUnsupportedVolume", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateVolume() = %v, want nil", err)
			}
		})
	}
}

func TestDriveTypeString(t *testing.T) {
	tests := []struct {
		driveType filesystem.DriveType
		want      string
	}{
		{filesystem.DriveUnknown, "unknown"},
		{filesystem.DriveNoRootDir, "no root directory"},
		{filesystem.DriveRemovable, "removable"},
		{filesystem.DriveFixed, "fixed"},
		{filesystem.DriveRemote, "remote"},
		{filesystem.DriveCDROM, "cd-rom"},
		{filesystem.DriveRAMDisk, "ramdisk"},
		{filesystem.DriveType(99), "DriveType(99)"},
	}
	for _, tt := range tests {
		if got := tt.driveType.String(); got != tt.want {
			t.Errorf("DriveType(%d).String() = %q, want %q", tt.driveType, got, tt.want)
		}
	}
}
