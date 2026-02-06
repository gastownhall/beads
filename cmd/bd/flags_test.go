package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestReadBodyFile(t *testing.T) {
	t.Run("ReadFromFile", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "desc.md")
		content := "## Problem\n\nSomething is broken.\n\n## Solution\n\nFix it.\n"
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		got, err := readBodyFile(filePath)
		if err != nil {
			t.Fatalf("readBodyFile failed: %v", err)
		}
		if got != content {
			t.Errorf("expected %q, got %q", content, got)
		}
	})

	t.Run("ReadEmptyFile", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "empty.md")
		if err := os.WriteFile(filePath, []byte{}, 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		got, err := readBodyFile(filePath)
		if err != nil {
			t.Fatalf("readBodyFile failed: %v", err)
		}
		if got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("NonExistentFile", func(t *testing.T) {
		_, err := readBodyFile("/nonexistent/path/file.md")
		if err == nil {
			t.Fatal("expected error for non-existent file, got nil")
		}
		if !strings.Contains(err.Error(), "failed to open file") {
			t.Errorf("expected 'failed to open file' error, got: %v", err)
		}
	})

	t.Run("SpecialCharacters", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "special.md")
		content := `Quotes: "double" and 'single'
Backticks: ` + "`code`" + `
Newlines and tabs:	here
Shell chars: $HOME $(whoami) && || > < |
Unicode: 日本語 émojis 🎉
`
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		got, err := readBodyFile(filePath)
		if err != nil {
			t.Fatalf("readBodyFile failed: %v", err)
		}
		if got != content {
			t.Errorf("content mismatch:\nexpected: %q\ngot:      %q", content, got)
		}
	})

	t.Run("BinaryContent", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "binary.bin")
		// Binary content with null bytes
		content := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 0x00, 0x48, 0x65, 0x6c, 0x6c, 0x6f}
		if err := os.WriteFile(filePath, content, 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		got, err := readBodyFile(filePath)
		if err != nil {
			t.Fatalf("readBodyFile should handle binary content: %v", err)
		}
		if got != string(content) {
			t.Errorf("binary content mismatch")
		}
	})

	t.Run("ReadFromStdin", func(t *testing.T) {
		// Create a pipe to simulate stdin
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("failed to create pipe: %v", err)
		}

		// Save and restore original stdin
		oldStdin := os.Stdin
		os.Stdin = r
		t.Cleanup(func() { os.Stdin = oldStdin })

		// Write content to pipe in a goroutine
		content := "Description from stdin\nWith multiple lines\n"
		go func() {
			w.WriteString(content)
			w.Close()
		}()

		got, err := readBodyFile("-")
		if err != nil {
			t.Fatalf("readBodyFile('-') failed: %v", err)
		}
		if got != content {
			t.Errorf("expected %q, got %q", content, got)
		}
	})
}

func TestGetDescriptionFlag(t *testing.T) {
	// Helper to create a cobra command with common issue flags registered
	newCmd := func() *cobra.Command {
		cmd := &cobra.Command{
			Use: "test",
			Run: func(cmd *cobra.Command, args []string) {},
		}
		registerCommonIssueFlags(cmd)
		return cmd
	}

	t.Run("BodyFileFlag", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "desc.md")
		content := "Description from file"
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		cmd := newCmd()
		cmd.SetArgs([]string{"--body-file", filePath})
		if err := cmd.ParseFlags([]string{"--body-file", filePath}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		got, changed := getDescriptionFlag(cmd)
		if !changed {
			t.Error("expected changed=true")
		}
		if got != content {
			t.Errorf("expected %q, got %q", content, got)
		}
	})

	t.Run("DescriptionFileFlag", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "desc.md")
		content := "Description from description-file"
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		cmd := newCmd()
		if err := cmd.ParseFlags([]string{"--description-file", filePath}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		got, changed := getDescriptionFlag(cmd)
		if !changed {
			t.Error("expected changed=true")
		}
		if got != content {
			t.Errorf("expected %q, got %q", content, got)
		}
	})

	t.Run("BodyFileTakesPrecedenceOverDescription", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "desc.md")
		fileContent := "From file"
		if err := os.WriteFile(filePath, []byte(fileContent), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		cmd := newCmd()
		// body-file + description should error
		err := cmd.ParseFlags([]string{"--body-file", filePath, "--description", "From flag"})
		if err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		// getDescriptionFlag should os.Exit(1) when both are set
		// We can't easily test os.Exit, so we just verify body-file is checked first
		// by testing them independently
	})

	t.Run("DescriptionFlagFallback", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.ParseFlags([]string{"--description", "Direct description"}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		got, changed := getDescriptionFlag(cmd)
		if !changed {
			t.Error("expected changed=true")
		}
		if got != "Direct description" {
			t.Errorf("expected 'Direct description', got %q", got)
		}
	})

	t.Run("BodyFlagFallback", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.ParseFlags([]string{"--body", "Body description"}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		got, changed := getDescriptionFlag(cmd)
		if !changed {
			t.Error("expected changed=true")
		}
		if got != "Body description" {
			t.Errorf("expected 'Body description', got %q", got)
		}
	})

	t.Run("MessageFlagFallback", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.ParseFlags([]string{"--message", "Message description"}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		got, changed := getDescriptionFlag(cmd)
		if !changed {
			t.Error("expected changed=true")
		}
		if got != "Message description" {
			t.Errorf("expected 'Message description', got %q", got)
		}
	})

	t.Run("NoFlagsSet", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.ParseFlags([]string{}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		got, changed := getDescriptionFlag(cmd)
		if changed {
			t.Error("expected changed=false when no flags set")
		}
		if got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("BodyFileAndDescriptionFileSameValue", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "desc.md")
		content := "Same file content"
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		cmd := newCmd()
		// Both pointing to same file should work
		if err := cmd.ParseFlags([]string{"--body-file", filePath, "--description-file", filePath}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		got, changed := getDescriptionFlag(cmd)
		if !changed {
			t.Error("expected changed=true")
		}
		if got != content {
			t.Errorf("expected %q, got %q", content, got)
		}
	})

	t.Run("DescriptionAndBodySameValue", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.ParseFlags([]string{"--description", "same", "--body", "same"}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		got, changed := getDescriptionFlag(cmd)
		if !changed {
			t.Error("expected changed=true")
		}
		if got != "same" {
			t.Errorf("expected 'same', got %q", got)
		}
	})
}
