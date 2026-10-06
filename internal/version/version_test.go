package version

import "testing"

// TestBuildDefaultsToDev pins that Build stays "dev" without the -ldflags stamp.
func TestBuildDefaultsToDev(t *testing.T) {
	if Build != "dev" {
		t.Errorf("Build = %q, want %q (the default for non-release builds)", Build, "dev")
	}
}
