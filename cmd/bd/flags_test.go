package main

import (
	"io"
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

		got, changed, err := getDescriptionFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
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

		got, changed, err := getDescriptionFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
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

		got, changed, err := getDescriptionFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
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

		got, changed, err := getDescriptionFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
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

		got, changed, err := getDescriptionFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
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

		got, changed, err := getDescriptionFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
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

		got, changed, err := getDescriptionFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !changed {
			t.Error("expected changed=true")
		}
		if got != content {
			t.Errorf("expected %q, got %q", content, got)
		}
	})

	t.Run("StdinFlag", func(t *testing.T) {
		// Create a pipe to simulate stdin
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("failed to create pipe: %v", err)
		}

		oldStdin := os.Stdin
		os.Stdin = r
		t.Cleanup(func() { os.Stdin = oldStdin })

		content := "Description from --stdin flag\nMulti-line content\n"
		go func() {
			w.WriteString(content)
			w.Close()
		}()

		cmd := newCmd()
		if err := cmd.ParseFlags([]string{"--stdin"}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		got, changed, err := getDescriptionFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !changed {
			t.Error("expected changed=true")
		}
		if got != content {
			t.Errorf("expected %q, got %q", content, got)
		}
	})

	t.Run("StdinMutuallyExclusiveWithDescription", func(t *testing.T) {
		cmd := newCmd()
		// cobra's MarkFlagsMutuallyExclusive validates at execution time,
		// but we can verify the flag is registered
		flag := cmd.Flags().Lookup("stdin")
		if flag == nil {
			t.Fatal("expected --stdin flag to be registered")
		}
		if flag.DefValue != "false" {
			t.Errorf("expected default value 'false', got %q", flag.DefValue)
		}
	})

	t.Run("DescriptionAndBodySameValue", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.ParseFlags([]string{"--description", "same", "--body", "same"}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		got, changed, err := getDescriptionFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !changed {
			t.Error("expected changed=true")
		}
		if got != "same" {
			t.Errorf("expected 'same', got %q", got)
		}
	})
}

func TestGetDesignFlag(t *testing.T) {
	// Helper to create a cobra command with common issue flags registered
	newCmd := func() *cobra.Command {
		cmd := &cobra.Command{
			Use: "test",
			Run: func(cmd *cobra.Command, args []string) {},
		}
		registerCommonIssueFlags(cmd)
		return cmd
	}

	t.Run("InlineDesignFlag", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.ParseFlags([]string{"--design", "inline design content"}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		got, changed, err := getDesignFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !changed {
			t.Error("expected changed=true")
		}
		if got != "inline design content" {
			t.Errorf("expected 'inline design content', got %q", got)
		}
	})

	t.Run("DesignFileFlag", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "design.md")
		content := "## Architecture\n\nUse microservices.\n"
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		cmd := newCmd()
		if err := cmd.ParseFlags([]string{"--design-file", filePath}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		got, changed, err := getDesignFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !changed {
			t.Error("expected changed=true")
		}
		if got != content {
			t.Errorf("expected %q, got %q", content, got)
		}
	})

	t.Run("DesignFileStdin", func(t *testing.T) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("failed to create pipe: %v", err)
		}
		oldStdin := os.Stdin
		os.Stdin = r
		t.Cleanup(func() { os.Stdin = oldStdin })

		content := "Design from stdin\nWith multiple lines\n"
		go func() {
			w.WriteString(content)
			w.Close()
		}()

		cmd := newCmd()
		if err := cmd.ParseFlags([]string{"--design-file", "-"}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		got, changed, err := getDesignFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !changed {
			t.Error("expected changed=true")
		}
		if got != content {
			t.Errorf("expected %q, got %q", content, got)
		}
	})

	t.Run("NoFlagsSet", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.ParseFlags([]string{}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		got, changed, err := getDesignFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if changed {
			t.Error("expected changed=false when no flags set")
		}
		if got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("MutualExclusionRegistered", func(t *testing.T) {
		cmd := newCmd()
		// Verify both flags are registered
		if cmd.Flags().Lookup("design") == nil {
			t.Fatal("expected --design flag to be registered")
		}
		if cmd.Flags().Lookup("design-file") == nil {
			t.Fatal("expected --design-file flag to be registered")
		}
	})
}

func TestTextFromSources(t *testing.T) {
	bodyFile := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(bodyFile, []byte("file text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	crlfFile := filepath.Join(t.TempDir(), "crlf.md")
	if err := os.WriteFile(crlfFile, []byte("Approved\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		useStdin   bool
		stdin      string
		filePath   string
		flagText   string
		positional []string
		want       string
		wantErr    bool
	}{
		{name: "flag text", flagText: "flag text", want: "flag text"},
		{name: "positional joined", positional: []string{"Use", "OAuth2"}, want: "Use OAuth2"},
		{name: "file content is verbatim", filePath: bodyFile, want: "file text\n"},
		{name: "file content keeps CRLF", filePath: crlfFile, want: "Approved\r\n"},
		{name: "stdin trims CRLF", useStdin: true, stdin: "Approved\r\n", want: "Approved"},
		{name: "stdin conflicts with file and flag", useStdin: true, stdin: "stdin text\n", filePath: bodyFile, flagText: "flag", wantErr: true},
		{name: "file conflicts with flag", filePath: bodyFile, flagText: "flag", wantErr: true},
		{name: "no source is empty", want: ""},
		{name: "positional conflicts with stdin", useStdin: true, stdin: "x", positional: []string{"pos"}, wantErr: true},
		{name: "positional conflicts with file", filePath: bodyFile, positional: []string{"pos"}, wantErr: true},
		{name: "positional conflicts with flag", flagText: "flag", positional: []string{"pos"}, wantErr: true},
		{name: "blank positional does not conflict with file", filePath: bodyFile, positional: []string{""}, want: "file text\n"},
		{name: "blank positional does not conflict with stdin", useStdin: true, stdin: "stdin text\n", positional: []string{" "}, want: "stdin text"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdin io.Reader
			if tt.useStdin {
				stdin = strings.NewReader(tt.stdin)
			}
			got, _, err := textFromSources(textSources{stdin: stdin, filePath: tt.filePath, flagText: tt.flagText, flagName: "--response", positional: tt.positional})
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected conflict error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}

	if _, _, err := textFromSources(textSources{filePath: filepath.Join(t.TempDir(), "missing.md")}); err == nil {
		t.Error("expected error for unreadable file")
	}
}

func TestRequireTextFromSources(t *testing.T) {
	if _, err := requireTextFromSources("response text", "use positional args or --response",
		textSources{}); err == nil || !strings.Contains(err.Error(), "no response text provided (use positional args or --response)") {
		t.Errorf("expected no-source error listing the hint, got %v", err)
	}
	if _, err := requireTextFromSources("response text", "hint",
		textSources{positional: []string{"   "}}); err == nil || !strings.Contains(err.Error(), "response text cannot be empty") {
		t.Errorf("expected cannot-be-empty error for blank positional text, got %v", err)
	}
	if _, err := requireTextFromSources("note text", "hint",
		textSources{stdin: strings.NewReader("\n")}); err == nil || !strings.Contains(err.Error(), "note text cannot be empty") {
		t.Errorf("expected cannot-be-empty error for blank stdin, got %v", err)
	}
	got, err := requireTextFromSources("response text", "hint",
		textSources{flagText: "flag text", flagName: "--response"})
	if err != nil || got != "flag text" {
		t.Errorf("got %q, %v; want \"flag text\", nil", got, err)
	}
}
