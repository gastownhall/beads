package main

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"rsc.io/script"
	"rsc.io/script/scripttest"
)

func TestScripts(t *testing.T) {
	// Build the bd binary
	exe := t.TempDir() + "/bd"
	if err := exec.Command("go", "build", "-o", exe, ".").Run(); err != nil {
		t.Fatal(err)
	}

	// Create minimal engine with default commands plus bd
	engine := script.NewEngine()
	engine.Cmds["bd"] = script.Program(exe, nil, 100*time.Millisecond)

	// Run all tests
	scripttest.Test(t, context.Background(), engine, nil, "testdata/*.txt")
}