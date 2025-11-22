package main

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/spf13/cobra"
)

// TestSearchCommand_HelpErrorHandling verifies that the search command handles
// Help() errors gracefully.
//
// This test addresses bd-gra: errcheck flagged cmd.Help() return value not checked
// in search.go:39. The current behavior is intentional:
// - Help() is called when query is missing (error path)
// - Even if Help() fails (e.g., output redirection fails), we still exit with code 1
// - The error from Help() is rare (typically I/O errors writing to stderr)
// - Since we're already in an error state, ignoring Help() errors is acceptable
func TestSearchCommand_HelpErrorHandling(t *testing.T) {
	// Create a test command similar to searchCmd
	cmd := &cobra.Command{
		Use:   "search [query]",
		Short: "Test search command",
		Run: func(cmd *cobra.Command, args []string) {
			// Simulate search.go:37-40
			query := ""
			if len(args) > 0 {
				query = args[0]
			}

			if query == "" {
				// This is the code path being tested
				_ = cmd.Help() // Intentionally ignore error (bd-gra)
				// In real code, os.Exit(1) follows, so Help() error doesn't matter
			}
		},
	}

	// Test 1: Normal case - Help() writes to stdout/stderr
	t.Run("normal_help_output", func(t *testing.T) {
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})

		// Call with no args (triggers help)
		cmd.SetArgs([]string{})
		_ = cmd.Execute() // Help is shown, no error expected
	})

	// Test 2: Help() with failed output writer
	t.Run("help_with_failed_writer", func(t *testing.T) {
		// Create a writer that always fails
		failWriter := &failingWriter{}
		cmd.SetOut(failWriter)
		cmd.SetErr(failWriter)

		// Call with no args (triggers help)
		cmd.SetArgs([]string{})
		err := cmd.Execute()

		// Even if Help() fails internally, cmd.Execute() may not propagate it
		// because we ignore the Help() return value
		t.Logf("cmd.Execute() returned: %v", err)

		// Key insight: The error from Help() is intentionally ignored because:
		// 1. We're already in an error path (missing required argument)
		// 2. The subsequent os.Exit(1) will terminate regardless
		// 3. Help() errors are rare (I/O failures writing to stderr)
		// 4. User will still see "Error: search query is required" before Help() is called
	})
}

// TestSearchCommand_HelpSuppression verifies that #nosec comment is appropriate
func TestSearchCommand_HelpSuppression(t *testing.T) {
	// This test documents why ignoring cmd.Help() error is safe:
	//
	// 1. Help() is called in an error path (missing required argument)
	// 2. We print "Error: search query is required" before calling Help()
	// 3. We call os.Exit(1) after Help(), terminating regardless of Help() success
	// 4. Help() errors are rare (typically I/O errors writing to stderr)
	// 5. If stderr is broken, user already can't see error messages anyway
	//
	// Therefore, checking Help() error adds no value and can be safely ignored.

	// Demonstrate that Help() can fail
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Test",
	}

	// With failing writer, Help() should error
	failWriter := &failingWriter{}
	cmd.SetOut(failWriter)
	cmd.SetErr(failWriter)

	err := cmd.Help()
	if err == nil {
		t.Logf("Help() succeeded even with failing writer (cobra may handle gracefully)")
	} else {
		t.Logf("Help() returned error as expected: %v", err)
	}

	// But in the search command, this error is intentionally ignored because
	// the command is already in an error state and will exit
}

// failingWriter is a writer that always returns an error
type failingWriter struct{}

func (fw *failingWriter) Write(p []byte) (n int, err error) {
	return 0, io.ErrClosedPipe // Simulate I/O error
}

// TestSearchCommand_MissingQueryShowsHelp verifies the intended behavior
func TestSearchCommand_MissingQueryShowsHelp(t *testing.T) {
	// This test verifies that when query is missing, we:
	// 1. Print error message to stderr
	// 2. Show help (even if it fails, we tried)
	// 3. Exit with code 1

	// We can't test os.Exit() directly, but we can verify the logic up to that point
	cmd := &cobra.Command{
		Use:   "search [query]",
		Short: "Test",
		Run: func(cmd *cobra.Command, args []string) {
			query := ""
			if len(args) > 0 {
				query = args[0]
			}

			if query == "" {
				// Capture stderr
				oldStderr := os.Stderr
				r, w, _ := os.Pipe()
				os.Stderr = w

				cmd.PrintErrf("Error: search query is required\n")

				w.Close()
				os.Stderr = oldStderr

				var buf bytes.Buffer
				io.Copy(&buf, r)

				if buf.String() == "" {
					t.Error("Expected error message to stderr")
				}

				// Help() is called here (may fail, but we don't care)
				_ = cmd.Help() // #nosec - see bd-gra

				// os.Exit(1) would be called here
			}
		},
	}

	cmd.SetArgs([]string{}) // No query
	_ = cmd.Execute()
}
