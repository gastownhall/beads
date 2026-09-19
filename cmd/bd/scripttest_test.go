//go:build scripttests
// +build scripttests

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"rsc.io/script"
	"rsc.io/script/scripttest"
)

func TestScripts(t *testing.T) {
	// Skip on Windows - test scripts use sh -c which requires Unix shell
	if runtime.GOOS == "windows" {
		t.Skip("scripttest uses Unix shell commands (sh -c), skipping on Windows")
	}

	// Locate or build the bd binary. Prebuilt fast path (scripts/test.sh and
	// CI export BEADS_TEST_BD_BINARY, wy-4mtr0); the scripts invoke plain
	// `bd` via `sh -c`, so the prebuilt is only usable when its basename is
	// exactly the expected executable name.
	exeName := "bd"
	binDir := t.TempDir()
	exe := filepath.Join(binDir, exeName)
	if prebuilt, err := findPrebuiltBDBinary(); err == nil && prebuilt != "" && filepath.Base(prebuilt) == exeName {
		exe = prebuilt
		binDir = filepath.Dir(prebuilt)
	} else if err := exec.Command("go", "build", "-tags", "gms_pure_go", "-o", exe, ".").Run(); err != nil {
		t.Fatal(err)
	}

	// Create minimal engine with default commands plus bd
	timeout := 2 * time.Second
	engine := script.NewEngine()
	engine.Cmds["bd"] = script.Program(exe, nil, timeout)

	// Add binDir to PATH so 'sh -c bd ...' works in test scripts
	currentPath := os.Getenv("PATH")
	env := []string{"PATH=" + binDir + ":" + currentPath}

	// Run all tests
	scripttest.Test(t, context.Background(), engine, env, "testdata/*.txt")
}
