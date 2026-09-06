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
