package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/issueops"
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
		cmd.Flags().String("if-threads", "", "")
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

	t.Run("accepts cascade and projects it (embedded parity, bd-paurh)", func(t *testing.T) {
		cmd := newCommand(t)
		if err := cmd.Flags().Set("cascade", "true"); err != nil {
			t.Fatalf("set cascade: %v", err)
		}
		jsonOutput, quietFlag = false, false

		got, err := gatherDeleteInput(cmd, []string{"bd-target"})
		if err != nil {
			t.Fatalf("gatherDeleteInput() with --cascade: %v", err)
		}
		want := &deleteInput{ids: []string{"bd-target"}, cascade: true}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("gatherDeleteInput(): got %#v, want %#v", got, want)
		}
	})

	t.Run("loads the --if-threads guard file", func(t *testing.T) {
		guardPath := filepath.Join(t.TempDir(), "guard.json")
		if err := os.WriteFile(guardPath, []byte(`{"cutoff": "2026-10-24T12:00:00Z", "member_label": "mail",
			"exclude_label": "keep", "edge": "replies-to",
			"threads": [{"root": "bd-r", "members": [{"id": "bd-r", "revision": "abc"}]}]}`), 0o600); err != nil {
			t.Fatalf("write guard file: %v", err)
		}
		cmd := newCommand(t)
		for name, value := range map[string]string{"if-threads": guardPath, "force": "true"} {
			if err := cmd.Flags().Set(name, value); err != nil {
				t.Fatalf("set %s: %v", name, err)
			}
		}
		jsonOutput, quietFlag = false, false

		got, err := gatherDeleteInput(cmd, []string{"bd-r"})
		if err != nil {
			t.Fatalf("gatherDeleteInput() with --if-threads: %v", err)
		}
		want := &deleteInput{ids: []string{"bd-r"}, force: true, threadGuard: &issueops.ThreadGuard{
			Cutoff:       time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC),
			MemberLabel:  "mail",
			ExcludeLabel: "keep",
			Edge:         "replies-to",
			Threads: []issueops.GuardedThread{{Root: "bd-r", Members: []issueops.GuardedMember{
				{ID: "bd-r", Revision: "abc"},
			}}},
		}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("gatherDeleteInput(): got %#v, want %#v", got, want)
		}
	})
}

// TestLoadDeleteThreadGuardRules pins the --if-threads flag rules every route
// shares: no guard without the flag, --force required, --cascade refused, and
// a guard file that is not exactly the JSON form of issueops.ThreadGuard
// refused rather than read with a check missing.
func TestLoadDeleteThreadGuardRules(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}
	good := write("good.json", `{"cutoff": "2026-10-24T12:00:00Z", "member_label": "mail", "edge": "replies-to", "threads": []}`)

	if guard, err := loadDeleteThreadGuard("", false, false, true); guard != nil || err != nil {
		t.Errorf("no --if-threads: got guard=%v err=%v, want no guard and no error", guard, err)
	}
	for _, tc := range []struct {
		name           string
		path           string
		force, cascade bool
		want           string
	}{
		{"empty path", "", true, false, "--if-threads needs a guard file path"},
		{"blank path", "  ", true, false, "--if-threads needs a guard file path"},
		{"empty path without --force", "", false, false, "--if-threads needs a guard file path"},
		{"empty path with --cascade", "", true, true, "--if-threads needs a guard file path"},
		{"without --force", good, false, false, "--if-threads requires --force"},
		{"with --cascade", good, true, true, "cannot be combined with --cascade"},
		{"missing file", filepath.Join(dir, "absent.json"), true, false, "reading --if-threads file"},
		{"not JSON", write("bad.json", "cutoff: soon"), true, false, "parsing --if-threads file"},
		{"unknown key", write("typo.json", `{"member_lable": "mail"}`), true, false, "unknown field"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guard, err := loadDeleteThreadGuard(tc.path, true, tc.force, tc.cascade)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("loadDeleteThreadGuard() = %v, %v; want an error containing %q", guard, err, tc.want)
			}
		})
	}
	if guard, err := loadDeleteThreadGuard(good, true, true, false); err != nil || guard == nil || guard.MemberLabel != "mail" {
		t.Errorf("good guard file: got guard=%+v err=%v", guard, err)
	}
}
