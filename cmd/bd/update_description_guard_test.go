package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func newUpdateDescriptionGuardCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "test",
		Run: func(cmd *cobra.Command, args []string) {},
	}
	registerCommonIssueFlags(cmd)
	cmd.Flags().Bool("allow-empty-description", false, "Allow empty description replacement when reading from stdin or file")
	return cmd
}

func withTestStdin(t *testing.T, content string, fn func()) {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}

	oldStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = oldStdin })

	go func() {
		_, _ = w.WriteString(content)
		_ = w.Close()
	}()

	fn()
}

func TestValidateDescriptionUpdateRejectsEmptyStdinWithoutOptIn(t *testing.T) {
	withTestStdin(t, "", func() {
		cmd := newUpdateDescriptionGuardCmd()
		if err := cmd.ParseFlags([]string{"--stdin"}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		description, changed := getDescriptionFlag(cmd)
		err := validateDescriptionUpdate(cmd, description, changed)
		if err == nil {
			t.Fatal("expected empty stdin description to be rejected")
		}
		if !strings.Contains(err.Error(), "--allow-empty-description") {
			t.Fatalf("expected opt-in guidance in error, got: %v", err)
		}
	})
}

func TestValidateDescriptionUpdateRejectsEmptyDashShorthandWithoutOptIn(t *testing.T) {
	withTestStdin(t, "", func() {
		cmd := newUpdateDescriptionGuardCmd()
		if err := cmd.ParseFlags([]string{"--description", "-"}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		description, changed := getDescriptionFlag(cmd)
		err := validateDescriptionUpdate(cmd, description, changed)
		if err == nil {
			t.Fatal("expected empty dash shorthand description to be rejected")
		}
		if !strings.Contains(err.Error(), "--allow-empty-description") {
			t.Fatalf("expected opt-in guidance in error, got: %v", err)
		}
	})
}

func TestValidateDescriptionUpdateRejectsEmptyBodyFileWithoutOptIn(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "empty.md")
	if err := os.WriteFile(filePath, []byte{}, 0644); err != nil {
		t.Fatalf("failed to write empty file: %v", err)
	}

	cmd := newUpdateDescriptionGuardCmd()
	if err := cmd.ParseFlags([]string{"--body-file", filePath}); err != nil {
		t.Fatalf("failed to parse flags: %v", err)
	}

	description, changed := getDescriptionFlag(cmd)
	err := validateDescriptionUpdate(cmd, description, changed)
	if err == nil {
		t.Fatal("expected empty body file description to be rejected")
	}
	if !strings.Contains(err.Error(), "--allow-empty-description") {
		t.Fatalf("expected opt-in guidance in error, got: %v", err)
	}
}

func TestValidateDescriptionUpdateAllowsEmptyStdinWithOptIn(t *testing.T) {
	withTestStdin(t, "", func() {
		cmd := newUpdateDescriptionGuardCmd()
		if err := cmd.ParseFlags([]string{"--stdin", "--allow-empty-description"}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		description, changed := getDescriptionFlag(cmd)
		if err := validateDescriptionUpdate(cmd, description, changed); err != nil {
			t.Fatalf("expected opt-in empty stdin to succeed, got: %v", err)
		}
		if description != "" {
			t.Fatalf("expected empty description, got %q", description)
		}
	})
}

func TestValidateDescriptionUpdateAllowsNonEmptyStdinWithoutOptIn(t *testing.T) {
	withTestStdin(t, "Updated from stdin\n", func() {
		cmd := newUpdateDescriptionGuardCmd()
		if err := cmd.ParseFlags([]string{"--stdin"}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		description, changed := getDescriptionFlag(cmd)
		if err := validateDescriptionUpdate(cmd, description, changed); err != nil {
			t.Fatalf("expected non-empty stdin to succeed, got: %v", err)
		}
		if description != "Updated from stdin\n" {
			t.Fatalf("expected stdin content to round-trip, got %q", description)
		}
	})
}

func TestValidateDescriptionUpdateAllowsExplicitInlineEmpty(t *testing.T) {
	cmd := newUpdateDescriptionGuardCmd()
	if err := cmd.ParseFlags([]string{"--description", ""}); err != nil {
		t.Fatalf("failed to parse flags: %v", err)
	}

	description, changed := getDescriptionFlag(cmd)
	if err := validateDescriptionUpdate(cmd, description, changed); err != nil {
		t.Fatalf("expected explicit inline empty description to succeed, got: %v", err)
	}
	if description != "" {
		t.Fatalf("expected empty inline description, got %q", description)
	}
}
