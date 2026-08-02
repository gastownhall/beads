package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dolthub/dolt/go/store/blobstore"
)

// TestResetDataRefNamesMatchDolt keeps the locally pinned data-plane ref
// names in sync with the constants dolt's git blobstore actually publishes
// (store/blobstore/git_refs.go). If a dolt bump renames either ref, this
// fails instead of reset-data silently deleting the wrong refs.
func TestResetDataRefNamesMatchDolt(t *testing.T) {
	if gitDoltDataRef != blobstore.DoltDataRef {
		t.Errorf("gitDoltDataRef = %q, dolt publishes %q", gitDoltDataRef, blobstore.DoltDataRef)
	}
	if want := "refs/heads/" + blobstore.DefaultInfoBranch; gitDoltInfoRef != want {
		t.Errorf("gitDoltInfoRef = %q, dolt publishes %q", gitDoltInfoRef, want)
	}
}

func TestClassifyResetDataRemote(t *testing.T) {
	makeBareGitRepo := func(t *testing.T) string {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "objects"), 0o755); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	makeDoltFileStore := func(t *testing.T) string {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "manifest"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	bareRepo := makeBareGitRepo(t)
	fileStore := makeDoltFileStore(t)
	emptyDir := t.TempDir()
	strangeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(strangeDir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		url     string
		want    resetDataKind
		wantErr bool
	}{
		{"git+https", "git+https://github.com/org/repo.git", resetDataGitBacked, false},
		{"git+ssh", "git+ssh://git@github.com/org/repo.git", resetDataGitBacked, false},
		{"scp-style", "git@github.com:org/repo.git", resetDataGitBacked, false},
		{"git+file", "git+file://" + bareRepo, resetDataGitBacked, false},
		{"file url to bare git repo", "file://" + bareRepo, resetDataGitBacked, false},
		{"bare path to bare git repo", bareRepo, resetDataGitBacked, false},
		{"file url to dolt store", "file://" + fileStore, resetDataFileStore, false},
		{"bare path to dolt store", fileStore, resetDataFileStore, false},
		{"file url to missing dir", "file://" + filepath.Join(emptyDir, "nope"), resetDataFileAbsent, false},
		{"file url to empty dir", "file://" + emptyDir, resetDataFileAbsent, false},
		{"file url to unrecognized dir", "file://" + strangeDir, 0, true},
		{"aws", "aws://[table:bucket]/db", resetDataUnsupported, false},
		{"gs", "gs://bucket/db", resetDataUnsupported, false},
		{"dolthub", "dolthub://org/db", resetDataUnsupported, false},
		{"https hosted", "https://doltremoteapi.dolthub.com/org/db", resetDataUnsupported, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := classifyResetDataRemote(tt.url)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("classifyResetDataRemote(%q) = %v, want error", tt.url, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("classifyResetDataRemote(%q): %v", tt.url, err)
			}
			if got != tt.want {
				t.Errorf("classifyResetDataRemote(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestResetDataGitURL(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"git+https://github.com/org/repo.git", "https://github.com/org/repo.git"},
		{"git+ssh://git@github.com/org/repo.git", "ssh://git@github.com/org/repo.git"},
		{"git+file:///tmp/remote.git", "file:///tmp/remote.git"},
		{"git@github.com:org/repo.git", "git@github.com:org/repo.git"},
		{"file:///tmp/remote.git", "file:///tmp/remote.git"},
		{"/tmp/remote.git", "file:///tmp/remote.git"},
	}
	for _, tt := range tests {
		if got := resetDataGitURL(tt.url); got != tt.want {
			t.Errorf("resetDataGitURL(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}
