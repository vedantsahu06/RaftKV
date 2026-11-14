//go:build windows

package platform

import (
	"errors"
	"testing"

	"github.com/vedantsahu06/raftkv/internal/kverr"
)

func TestNewQualifiesLocalVolume(t *testing.T) {
	fsys, err := New(t.TempDir())
	if err != nil {
		if errors.Is(err, kverr.ErrUnsupported) || errors.Is(err, kverr.ErrUnsupportedVolume) {
			t.Skipf("test environment is not supported: %v", err)
		}
		t.Fatalf("New() = %v", err)
	}
	if fsys == nil {
		t.Fatal("New() returned a nil filesystem")
	}
}
