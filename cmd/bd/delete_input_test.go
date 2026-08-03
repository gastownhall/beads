package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestReadIssueIDsFromFile(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("read valid IDs from file", func(t *testing.T) {
		testFile := filepath.Join(tmpDir, "ids.txt")
		content := "bd-1\nbd-2\nbd-3\n"
		if err := os.WriteFile(testFile, []byte(content), 0o644); err != nil {
			t.Fatalf("Failed to write test file: %v", err)
		}

		ids, err := readIssueIDsFromFile(testFile)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if !reflect.DeepEqual(ids, []string{"bd-1", "bd-2", "bd-3"}) {
			t.Errorf("IDs: got %v, want [bd-1 bd-2 bd-3]", ids)
		}
	})

	t.Run("skip empty lines and comments", func(t *testing.T) {
		testFile := filepath.Join(tmpDir, "ids_with_comments.txt")
		content := "bd-1\n\n# This is a comment\nbd-2\n  \nbd-3\n"
		if err := os.WriteFile(testFile, []byte(content), 0o644); err != nil {
			t.Fatalf("Failed to write test file: %v", err)
		}

		ids, err := readIssueIDsFromFile(testFile)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if !reflect.DeepEqual(ids, []string{"bd-1", "bd-2", "bd-3"}) {
			t.Errorf("IDs: got %v, want [bd-1 bd-2 bd-3]", ids)
		}
	})

	t.Run("handle non-existent file", func(t *testing.T) {
		_, err := readIssueIDsFromFile(filepath.Join(tmpDir, "nonexistent.txt"))
		if err == nil {
			t.Error("Expected error for non-existent file")
		}
	})
}

func TestUniqueStrings(t *testing.T) {
	t.Run("remove duplicates while preserving the first occurrence order", func(t *testing.T) {
		got := uniqueStrings([]string{"a", "b", "a", "c", "b", "d"})
		want := []string{"a", "b", "c", "d"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("uniqueStrings(): got %v, want %v", got, want)
		}
	})

	t.Run("handle empty input", func(t *testing.T) {
		if got := uniqueStrings([]string{}); len(got) != 0 {
			t.Errorf("Expected empty result, got %d items", len(got))
		}
	})

	t.Run("handle all unique", func(t *testing.T) {
		got := uniqueStrings([]string{"a", "b", "c"})
		want := []string{"a", "b", "c"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("uniqueStrings(): got %v, want %v", got, want)
		}
	})
}

func TestGatherDeleteInput(t *testing.T) {
	oldJSONOutput, oldQuiet := jsonOutput, quietFlag
	t.Cleanup(func() { jsonOutput, quietFlag = oldJSONOutput, oldQuiet })

	newCommand := func(t *testing.T) *cobra.Command {
		t.Helper()
		cmd := &cobra.Command{}
		cmd.Flags().String("from-file", "", "")
		cmd.Flags().Bool("force", false, "")
		cmd.Flags().Bool("dry-run", false, "")
		cmd.Flags().Bool("cascade", false, "")
		return cmd
	}

	t.Run("merges positional and file IDs with stable deduplication and projects flags", func(t *testing.T) {
		idsPath := filepath.Join(t.TempDir(), "ids.txt")
		if err := os.WriteFile(idsPath, []byte(strings.Join([]string{"bd-file", "bd-shared", "bd-file"}, "\n")), 0o600); err != nil {
			t.Fatalf("write IDs file: %v", err)
		}
		cmd := newCommand(t)
		for name, value := range map[string]string{"from-file": idsPath, "force": "true", "dry-run": "true"} {
			if err := cmd.Flags().Set(name, value); err != nil {
				t.Fatalf("set %s: %v", name, err)
			}
		}
		jsonOutput, quietFlag = true, true

		got, err := gatherDeleteInput(cmd, []string{"bd-arg", "bd-shared", "bd-arg"})
		if err != nil {
			t.Fatalf("gatherDeleteInput: %v", err)
		}
		want := &deleteInput{
			ids:        []string{"bd-arg", "bd-shared", "bd-file"},
			force:      true,
			dryRun:     true,
			jsonOutput: true,
			quiet:      true,
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("gatherDeleteInput(): got %#v, want %#v", got, want)
		}
	})

	t.Run("rejects cascade without accessing a store", func(t *testing.T) {
		cmd := newCommand(t)
		if err := cmd.Flags().Set("cascade", "true"); err != nil {
			t.Fatalf("set cascade: %v", err)
		}

		_, err := gatherDeleteInput(cmd, []string{"bd-target"})
		if err == nil || !strings.Contains(err.Error(), "--cascade") {
			t.Fatalf("gatherDeleteInput() error = %v, want --cascade rejection", err)
		}
	})
}
