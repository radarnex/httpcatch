package buildinfo_test

import (
	"testing"

	"github.com/radarnex/httpcatch/internal/buildinfo"
)

func TestDefaults(t *testing.T) {
	if buildinfo.Version != "dev" {
		t.Errorf("Version: got %q want %q", buildinfo.Version, "dev")
	}
	if buildinfo.BuildTime != "unknown" {
		t.Errorf("BuildTime: got %q want %q", buildinfo.BuildTime, "unknown")
	}
}
