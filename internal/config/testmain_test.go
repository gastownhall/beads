package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMain isolates tests from the repository's own `.beads/config.yaml`.
//
// Many tests expect config defaults (e.g. sync.mode=git-portable). If the test
// process runs from within this repo, Initialize() will walk up from CWD and
// load the repo's tracked `.beads/config.yaml`, which sets sync.mode=dolt-native.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "beads-config-tests-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp dir: %v\n", err)
		os.Exit(1)
	}

	oldWD, _ := os.Getwd()

	// Point config discovery away from the repo and user's machine.
	_ = os.Chdir(tmp)
	_ = os.Setenv("HOME", tmp)
	_ = os.Setenv("USERPROFILE", tmp) // Windows compatibility
	_ = os.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg-config"))

	code := m.Run()

	_ = os.Chdir(oldWD)
	_ = os.RemoveAll(tmp)
	os.Exit(code)
}
