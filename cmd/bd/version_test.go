package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	// Save original stdout
	oldStdout := os.Stdout
	defer func() { os.Stdout = oldStdout }()

	t.Run("plain text version output", func(t *testing.T) {
		// Create a pipe to capture output
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("Failed to create pipe: %v", err)
		}
		os.Stdout = w
		jsonOutput = false

		// Run version command
		versionCmd.Run(versionCmd, []string{})

		// Close writer and read output
		w.Close()
		var buf bytes.Buffer
		buf.ReadFrom(r)
		output := buf.String()

		// Verify output contains version info
		if !strings.Contains(output, "bd version") {
			t.Errorf("Expected output to contain 'bd version', got: %s", output)
		}
		if !strings.Contains(output, Version) {
			t.Errorf("Expected output to contain version %s, got: %s", Version, output)
		}
	})

	t.Run("json version output", func(t *testing.T) {
		// Create a pipe to capture output
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("Failed to create pipe: %v", err)
		}
		os.Stdout = w
		jsonOutput = true

		// Run version command
		versionCmd.Run(versionCmd, []string{})

		// Close writer and read output
		w.Close()
		var buf bytes.Buffer
		buf.ReadFrom(r)
		output := buf.String()

		// Parse JSON output
		var result map[string]string
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatalf("Failed to parse JSON output: %v", err)
		}

		// Verify JSON contains version and build
		if result["version"] != Version {
			t.Errorf("Expected version %s, got %s", Version, result["version"])
		}
		if result["build"] == "" {
			t.Error("Expected build field to be non-empty")
		}
	})

	// Restore default
	jsonOutput = false
}
