package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestAddToGitignoreCompletesTrailingCR(t *testing.T) {
	repoRoot := initGitRepoForGitignoreTest(t)
	path := filepath.Join(repoRoot, ".gitignore")
	initial := []byte("a/\r\nb/\r")
	if err := os.WriteFile(path, initial, 0644); err != nil {
		t.Fatal(err)
	}
	want := []byte("a/\r\nb/\r\n# bd worktree\r\nworktree-feature/\r\n")
	for i := 0; i < 2; i++ {
		if err := addToGitignore(context.Background(), repoRoot, "worktree-feature"); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("append %d bytes = %q, want %q", i+1, got, want)
		}
	}
}

// TestAddToGitignoreCompletesUnterminatedCRLFLine covers the one arm where
// addToGitignore's append completes the final line with the detected line ending.
//
// gitignore.AppendLines guards that completion twice over: it only completes a
// file that does not end in "\n", and it completes a trailing "\r" with a bare
// "\n" instead. So the detected "\r\n" is used only for a CRLF file whose
// final byte is neither — an unterminated final line like "build/". The sibling
// fixtures in this file cannot reach it: "a/\r\nb/\r" ends in CR and takes the
// bare "\n" completion, and "node_modules/\r\n" ends in LF and needs no
// completion at all. Without this case, completing the final line with a
// hardcoded "\n" still passes every test here while writing exactly the
// mixed-terminator .gitignore this change exists to prevent.
//
// internal/gitignore's own tables already cover this input (append_test.go,
// "CRLF with unterminated final line" for the detector and "CRLF unterminated"
// for AppendLines); this is the call site that consumes them.
func TestAddToGitignoreCompletesUnterminatedCRLFLine(t *testing.T) {
	repoRoot := initGitRepoForGitignoreTest(t)
	path := filepath.Join(repoRoot, ".gitignore")
	initial := []byte("node_modules/\r\nbuild/")
	if err := os.WriteFile(path, initial, 0644); err != nil {
		t.Fatal(err)
	}
	want := []byte("node_modules/\r\nbuild/\r\n# bd worktree\r\nworktree-feature/\r\n")
	// Twice: the append must be idempotent as well as CRLF-correct.
	for i := 0; i < 2; i++ {
		if err := addToGitignore(context.Background(), repoRoot, "worktree-feature"); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("append %d bytes = %q, want %q", i+1, got, want)
		}
	}
}

func TestAddToGitignorePreservesCRLFWhenAppending(t *testing.T) {
	repoRoot := initGitRepoForGitignoreTest(t)
	gitignorePath := filepath.Join(repoRoot, ".gitignore")
	initial := []byte("node_modules/\r\n")
	if err := os.WriteFile(gitignorePath, initial, 0644); err != nil {
		t.Fatalf("failed to write .gitignore: %v", err)
	}

	entry := "worktree-feature"
	if err := addToGitignore(context.Background(), repoRoot, entry); err != nil {
		t.Fatalf("first addToGitignore failed: %v", err)
	}

	want := []byte("node_modules/\r\n# bd worktree\r\nworktree-feature/\r\n")
	updated, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("failed to read .gitignore: %v", err)
	}
	if !bytes.Equal(updated, want) {
		t.Fatalf(".gitignore bytes after append:\nwant: %q\ngot:  %q", want, updated)
	}

	if err := addToGitignore(context.Background(), repoRoot, entry); err != nil {
		t.Fatalf("second addToGitignore failed: %v", err)
	}
	unchanged, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("failed to reread .gitignore: %v", err)
	}
	if !bytes.Equal(unchanged, want) {
		t.Fatalf("second append changed .gitignore:\nwant: %q\ngot:  %q", want, unchanged)
	}
}
