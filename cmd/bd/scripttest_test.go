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

	// Build the bd binary
	exeName := "bd"
	binDir := t.TempDir()
	exe := filepath.Join(binDir, exeName)
	if err := exec.Command("go", "build", "-o", exe, ".").Run(); err != nil {
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
