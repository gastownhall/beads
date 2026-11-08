package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recordingInitCommitter struct {
	message string
	err     error
}

func (s *recordingInitCommitter) CommitWithConfig(_ context.Context, message string) error {
	s.message = message
	return s.err
}

func TestCommitInitStateIncludesIntentionalConfig(t *testing.T) {
	store := &recordingInitCommitter{}
	if err := commitInitState(context.Background(), store); err != nil {
		t.Fatalf("commitInitState: %v", err)
	}
	if store.message != "bd init" {
		t.Fatalf("CommitWithConfig message = %q, want %q", store.message, "bd init")
	}
}

func TestCommitInitStatePreservesCommitError(t *testing.T) {
	want := errors.New("commit failed")
	store := &recordingInitCommitter{err: want}
	if got := commitInitState(context.Background(), store); !errors.Is(got, want) {
		t.Fatalf("commitInitState error = %v, want %v", got, want)
	}
}

// GH#5654: the bootstrap commit must be scoped to the init artifacts; a file
// the user staged before bd init must stay staged, not land in the init commit.
func TestCommitEmbeddedInitArtifactsLeavesPreStagedFilesOut(t *testing.T) {
	repo := t.TempDir()
	initRoleFixtureGit(t, repo, "init", "-q")
	initRoleFixtureGit(t, repo, "config", "user.email", "t@t.local")
	initRoleFixtureGit(t, repo, "config", "user.name", "Test")
	initRoleFixtureGit(t, repo, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(repo, "work.txt"), []byte("unrelated work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	initRoleFixtureGit(t, repo, "add", "work.txt")
	if err := os.MkdirAll(filepath.Join(repo, ".beads"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".beads", "config.yaml"), []byte("# beads\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	commitEmbeddedInitArtifacts(repo, true)

	committed := initRoleFixtureGit(t, repo, "show", "--name-only", "--format=", "HEAD")
	if !strings.Contains(committed, ".beads/config.yaml") {
		t.Fatalf("init commit is missing the beads scaffold; committed files:\n%s", committed)
	}
	if strings.Contains(committed, "work.txt") {
		t.Errorf("init commit swept the pre-staged work.txt; committed files:\n%s", committed)
	}
	if staged := initRoleFixtureGit(t, repo, "diff", "--cached", "--name-only"); staged != "work.txt" {
		t.Errorf("staged after init = %q, want the user's work.txt still staged", staged)
	}
}
