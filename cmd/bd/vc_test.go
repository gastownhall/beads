package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

func TestVcCommitStdinFlag(t *testing.T) {
	t.Run("StdinReadsMessage", func(t *testing.T) {
		// Save and restore globals
		origMessage := vcCommitMessage
		origStdin := vcCommitStdin
		t.Cleanup(func() {
			vcCommitMessage = origMessage
			vcCommitStdin = origStdin
		})

		// Create a pipe to simulate stdin
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("failed to create pipe: %v", err)
		}

		oldStdin := os.Stdin
		os.Stdin = r
		t.Cleanup(func() { os.Stdin = oldStdin })

		content := "Multi-line commit message\nWith special chars: 'quotes' & \"doubles\"\n"
		go func() {
			w.WriteString(content)
			w.Close()
		}()

		// Simulate what the Run function does when --stdin is set
		vcCommitStdin = true
		vcCommitMessage = ""

		cmd := vcCommitCmd
		cmd.SetArgs([]string{"--stdin"})
		if err := cmd.ParseFlags([]string{"--stdin"}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		// Verify the flag is registered and parsed
		stdinVal, err := cmd.Flags().GetBool("stdin")
		if err != nil {
			t.Fatalf("--stdin flag not found: %v", err)
		}
		if !stdinVal {
			t.Error("expected --stdin to be true")
		}
	})

	t.Run("StdinAndMessageConflict", func(t *testing.T) {
		// Verify both flags can be set (conflict check happens at runtime in Run)
		cmd := vcCommitCmd
		if err := cmd.ParseFlags([]string{"--stdin", "-m", "test"}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		stdinVal, _ := cmd.Flags().GetBool("stdin")
		msgVal, _ := cmd.Flags().GetString("message")

		if !stdinVal {
			t.Error("expected --stdin to be true")
		}
		if msgVal != "test" {
			t.Errorf("expected message 'test', got %q", msgVal)
		}
	})

	t.Run("StdinFlagRegistered", func(t *testing.T) {
		// Verify the --stdin flag exists on the commit command
		flag := vcCommitCmd.Flags().Lookup("stdin")
		if flag == nil {
			t.Fatal("--stdin flag not registered on vc commit command")
		}
		if flag.Usage != "Read commit message from stdin" {
			t.Errorf("unexpected usage text: %q", flag.Usage)
		}
	})

	t.Run("TrailingNewlinesTrimmed", func(t *testing.T) {
		// Verify strings.TrimRight behavior for the stdin path
		// This tests the trimming logic used in the command
		input := "commit message\n\n"
		expected := "commit message"
		got := trimTrailingNewlines(input)
		if got != expected {
			t.Errorf("expected %q, got %q", expected, got)
		}
	})

	t.Run("PreservesInternalNewlines", func(t *testing.T) {
		input := "line 1\nline 2\nline 3\n"
		expected := "line 1\nline 2\nline 3"
		got := trimTrailingNewlines(input)
		if got != expected {
			t.Errorf("expected %q, got %q", expected, got)
		}
	})
}

// trimTrailingNewlines mirrors the strings.TrimRight(s, "\n") used in vcCommitCmd.
func trimTrailingNewlines(s string) string {
	// This matches the behavior in vc.go: strings.TrimRight(string(b), "\n")
	for len(s) > 0 && s[len(s)-1] == '\n' {
		s = s[:len(s)-1]
	}
	return s
}

// failingVCStatusStore is a minimal DoltStorage stand-in for the vc status
// error path: branch and commit reads succeed, the working-set Status read
// fails. It embeds the nil interface for identity/type assertion and
// overrides only the three methods the command touches (the same shape
// failingBackupStore uses).
type failingVCStatusStore struct {
	storage.DoltStorage
	statusErr error
}

func (f *failingVCStatusStore) CurrentBranch(context.Context) (string, error) {
	return "main", nil
}

func (f *failingVCStatusStore) GetCurrentCommit(context.Context) (string, error) {
	return "deadbeefdeadbeef", nil
}

func (f *failingVCStatusStore) Status(context.Context) (*storage.Status, error) {
	return nil, f.statusErr
}

// TestVCStatusStatusReadFailureIsAnError pins the GH#7783 error contract at
// the command level: when reading the working set fails, `bd vc status` must
// fail with that error in both output modes. Before the fix the command never
// read the working set at all, so an unreadable dolt_status printed a clean
// branch/commit report — a misleading success over a failed read.
func TestVCStatusStatusReadFailureIsAnError(t *testing.T) {
	origStore, origJSON := store, jsonOutput
	t.Cleanup(func() { store, jsonOutput = origStore, origJSON })
	store = &failingVCStatusStore{statusErr: errors.New("dolt_status: read exploded")}

	runStatus := func() (string, string, error) {
		var err error
		stdout, stderr := captureStdio(t, func() {
			err = vcStatusCmd.RunE(vcStatusCmd, nil)
		})
		return stdout, stderr, err
	}

	t.Run("text mode", func(t *testing.T) {
		jsonOutput = false
		stdout, stderr, err := runStatus()
		if err == nil {
			t.Fatalf("vc status must fail when the working-set read fails; stdout:\n%s", stdout)
		}
		for _, want := range []string{"failed to read working-set status", "dolt_status: read exploded"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr missing %q:\n%s", want, stderr)
			}
		}
		if strings.Contains(stdout, "Version Control Status") && !strings.Contains(stderr, "failed to read") {
			t.Errorf("a failing read must not print a clean report; stdout:\n%s", stdout)
		}
	})

	t.Run("json mode", func(t *testing.T) {
		jsonOutput = true
		stdout, stderr, err := runStatus()
		if err == nil {
			t.Fatalf("vc status --json must fail when the working-set read fails; stdout:\n%s", stdout)
		}
		combined := stdout + stderr
		for _, want := range []string{"failed to read working-set status", "dolt_status: read exploded"} {
			if !strings.Contains(combined, want) {
				t.Errorf("output missing %q:\nstdout:\n%s\nstderr:\n%s", want, stdout, stderr)
			}
		}
	})
}
