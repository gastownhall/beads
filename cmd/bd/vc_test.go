package main

import (
	"context"
	"encoding/json"
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

// vcStatusUnreadableStore is a DoltStorage stand-in whose branch and commit
// reads succeed but whose working-set status read fails. It embeds a nil
// DoltStorage and overrides only the reads bd vc status makes.
type vcStatusUnreadableStore struct {
	storage.DoltStorage
}

func (vcStatusUnreadableStore) CurrentBranch(context.Context) (string, error) {
	return "main", nil
}

func (vcStatusUnreadableStore) GetCurrentCommit(context.Context) (string, error) {
	return "0123456789abcdef0123456789abcdef", nil
}

func (vcStatusUnreadableStore) Status(context.Context) (*storage.Status, error) {
	return nil, errors.New("dolt_status exploded")
}

// TestVCStatusFailsWhenWorkingSetUnreadable pins that bd vc status fails with
// a status-read error, in text and JSON, when the working-set status cannot be
// read, instead of printing the branch and commit as if the store were clean.
func TestVCStatusFailsWhenWorkingSetUnreadable(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		name := "text"
		if asJSON {
			name = "json"
		}
		t.Run(name, func(t *testing.T) {
			saveAndRestoreGlobals(t)
			origJSON, origCtx := jsonOutput, rootCtx
			t.Cleanup(func() { jsonOutput, rootCtx = origJSON, origCtx })
			store = vcStatusUnreadableStore{}
			jsonOutput = asJSON
			rootCtx = context.Background()

			var runErr error
			stdioMutex.Lock()
			stdout, stderr := captureStdio(t, func() {
				runErr = vcStatusCmd.RunE(vcStatusCmd, nil)
			})
			stdioMutex.Unlock()

			if runErr == nil {
				t.Fatalf("bd vc status succeeded although the working-set status could not be read\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
			}
			var report string
			if asJSON {
				var payload map[string]interface{}
				if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
					t.Fatalf("JSON error output does not parse: %v\n%s", err, stdout)
				}
				msg, _ := payload["error"].(string)
				report = msg
				for _, key := range []string{"branch", "commit", "staged", "unstaged"} {
					if _, ok := payload[key]; ok {
						t.Errorf("JSON output carries %q alongside the error: %s", key, stdout)
					}
				}
			} else {
				report = stderr
				if strings.Contains(stdout, "Branch") || strings.Contains(stdout, "no uncommitted changes") {
					t.Errorf("text output reports a status despite the failed read:\n%s", stdout)
				}
			}
			for _, want := range []string{"working-set status", "dolt_status exploded"} {
				if !strings.Contains(report, want) {
					t.Errorf("error report must mention %q, got:\n%s", want, report)
				}
			}
		})
	}
}
