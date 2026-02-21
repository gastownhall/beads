//go:build cgo

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
		var result map[string]interface{}
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
		// Verify cgo field is present and true (test file has //go:build cgo)
		if cgo, ok := result["cgo"]; !ok {
			t.Error("Expected cgo field to be present")
		} else if cgo != true {
			t.Errorf("Expected cgo=true in CGO build, got %v", cgo)
		}
	})

	// Restore default
	jsonOutput = false
}

func TestResolveCommitHash(t *testing.T) {
	// Save original Commit value
	origCommit := Commit
	defer func() { Commit = origCommit }()

	t.Run("returns ldflag value when set", func(t *testing.T) {
		testCommit := "abc123def456"
		Commit = testCommit
		result := resolveCommitHash()
		if result != testCommit {
			t.Errorf("Expected %q, got %q", testCommit, result)
		}
	})

	t.Run("returns empty string when not set", func(t *testing.T) {
		Commit = ""
		result := resolveCommitHash()
		// Result could be from git or empty - just verify it doesn't panic
		if result == "" || len(result) >= 7 {
			// Either empty or looks like a git hash
			return
		}
		t.Errorf("Unexpected result format: %q", result)
	})
}

func TestResolveBranch(t *testing.T) {
	// Save original Branch value
	origBranch := Branch
	defer func() { Branch = origBranch }()

	t.Run("returns ldflag value when set", func(t *testing.T) {
		testBranch := "main"
		Branch = testBranch
		result := resolveBranch()
		if result != testBranch {
			t.Errorf("Expected %q, got %q", testBranch, result)
		}
	})

	t.Run("returns empty string or git branch when not set", func(t *testing.T) {
		Branch = ""
		result := resolveBranch()
		// Result could be from git or empty - just verify it doesn't panic
		if result == "" || result == "main" || strings.Contains(result, "detached") {
			return
		}
		t.Logf("Got branch: %q", result)
	})
}

func TestVersionOutputWithCommitAndBranch(t *testing.T) {
	// Save original values
	oldStdout := os.Stdout
	origCommit := Commit
	origBranch := Branch
	defer func() {
		os.Stdout = oldStdout
		Commit = origCommit
		Branch = origBranch
	}()

	t.Run("text output includes commit and branch when available", func(t *testing.T) {
		Commit = "7e709405b38c472d8cbc996c7cd26df7e3b438d0"
		Branch = "main"

		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("Failed to create pipe: %v", err)
		}
		os.Stdout = w
		jsonOutput = false

		versionCmd.Run(versionCmd, []string{})

		w.Close()
		var buf bytes.Buffer
		buf.ReadFrom(r)
		output := buf.String()

		// Should contain both branch and commit
		if !strings.Contains(output, "main@") {
			t.Errorf("Expected output to contain 'main@', got: %s", output)
		}
		if !strings.Contains(output, "7e70940") { // first 7 chars of commit
			t.Errorf("Expected output to contain commit hash, got: %s", output)
		}
	})

	t.Run("json output includes commit and branch when available", func(t *testing.T) {
		Commit = "7e709405b38c472d8cbc996c7cd26df7e3b438d0"
		Branch = "main"

		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("Failed to create pipe: %v", err)
		}
		os.Stdout = w
		jsonOutput = true

		versionCmd.Run(versionCmd, []string{})

		w.Close()
		var buf bytes.Buffer
		buf.ReadFrom(r)
		output := buf.String()

		var result map[string]interface{}
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatalf("Failed to parse JSON output: %v", err)
		}

		if result["commit"] != Commit {
			t.Errorf("Expected commit %q, got %q", Commit, result["commit"])
		}
		if result["branch"] != Branch {
			t.Errorf("Expected branch %q, got %q", Branch, result["branch"])
		}
	})
}

func TestVersionFlag(t *testing.T) {
	// Reset global state for test isolation
	ensureCleanGlobalState(t)

	// Ensure cleanup after running cobra commands
	t.Cleanup(func() {
		resetCommandContext()
	})

	// Save original stdout
	oldStdout := os.Stdout
	defer func() { os.Stdout = oldStdout }()

	t.Run("--version flag", func(t *testing.T) {
		// Create a pipe to capture output
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("Failed to create pipe: %v", err)
		}
		os.Stdout = w

		// Set version flag and run root command
		rootCmd.SetArgs([]string{"--version"})
		rootCmd.Execute()

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

		// Reset args
		rootCmd.SetArgs(nil)
	})

	t.Run("-v shorthand", func(t *testing.T) {
		// Create a pipe to capture output
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("Failed to create pipe: %v", err)
		}
		os.Stdout = w

		// Set version flag and run root command
		rootCmd.SetArgs([]string{"-v"})
		rootCmd.Execute()

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

		// Reset args
		rootCmd.SetArgs(nil)
	})
}
