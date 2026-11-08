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

// GH#5654: when the artifact paths match HEAD and only foreign work is
// staged, the bootstrap commit must not move HEAD and must report neither
// success nor failure: there is nothing for init to commit.
func TestCommitEmbeddedInitArtifactsNothingToCommitIsSilent(t *testing.T) {
	repo := t.TempDir()
	initRoleFixtureGit(t, repo, "init", "-q")
	initRoleFixtureGit(t, repo, "config", "user.email", "t@t.local")
	initRoleFixtureGit(t, repo, "config", "user.name", "Test")
	initRoleFixtureGit(t, repo, "config", "commit.gpgsign", "false")
	if err := os.MkdirAll(filepath.Join(repo, ".beads"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".beads", "config.yaml"), []byte("# beads\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	initRoleFixtureGit(t, repo, "add", ".beads")
	initRoleFixtureGit(t, repo, "commit", "-m", "seed")
	if err := os.WriteFile(filepath.Join(repo, "work.txt"), []byte("unrelated work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	initRoleFixtureGit(t, repo, "add", "work.txt")
	before := initRoleFixtureGit(t, repo, "rev-parse", "HEAD")

	// Two runs: the shared stdio mutex does not allow capturing both
	// streams at once, and neither run changes any state.
	stderr := captureStderr(t, func() { commitEmbeddedInitArtifacts(repo, false) })
	if stderr != "" {
		t.Errorf("nothing-to-commit bootstrap wrote stderr: %q", stderr)
	}
	stdout := captureStdout(t, func() error {
		commitEmbeddedInitArtifacts(repo, false)
		return nil
	})
	if strings.Contains(stdout, "Committed beads files") {
		t.Errorf("nothing-to-commit bootstrap reported success: %q", stdout)
	}
	if after := initRoleFixtureGit(t, repo, "rev-parse", "HEAD"); after != before {
		t.Errorf("HEAD moved: %s -> %s", before, after)
	}
	if staged := initRoleFixtureGit(t, repo, "diff", "--cached", "--name-only"); staged != "work.txt" {
		t.Errorf("staged after init = %q, want the user's work.txt still staged", staged)
	}
}

// GH#5654: a colocated Jujutsu workspace keeps its own commit graph, so the
// embedded bootstrap must not make a native Git commit in it: HEAD and the
// staged entries stay untouched and a note (only when not quiet) says the
// artifacts are left for jj to record.
func TestCommitEmbeddedInitArtifactsSkipsJujutsuWorkspace(t *testing.T) {
	for _, quiet := range []bool{false, true} {
		repo := t.TempDir()
		initRoleFixtureGit(t, repo, "init", "-q")
		initRoleFixtureGit(t, repo, "config", "user.email", "t@t.local")
		initRoleFixtureGit(t, repo, "config", "user.name", "Test")
		initRoleFixtureGit(t, repo, "config", "commit.gpgsign", "false")
		if err := os.MkdirAll(filepath.Join(repo, ".jj"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, "seed.txt"), []byte("seed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		initRoleFixtureGit(t, repo, "add", "seed.txt")
		initRoleFixtureGit(t, repo, "commit", "-m", "seed")
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
		before := initRoleFixtureGit(t, repo, "rev-parse", "HEAD")

		stderr := captureStderr(t, func() { commitEmbeddedInitArtifacts(repo, quiet) })

		if quiet {
			if stderr != "" {
				t.Errorf("quiet Jujutsu skip wrote stderr: %q", stderr)
			}
		} else {
			if !strings.Contains(stderr, "skipped bootstrap commit") || !strings.Contains(stderr, "Jujutsu") {
				t.Errorf("missing Jujutsu skip note: %q", stderr)
			}
		}
		if after := initRoleFixtureGit(t, repo, "rev-parse", "HEAD"); after != before {
			t.Errorf("Jujutsu skip still moved HEAD: %s -> %s", before, after)
		}
		if tracked := initRoleFixtureGit(t, repo, "ls-files", "--", ".beads"); tracked != "" {
			t.Errorf("Jujutsu skip staged beads artifacts: %s", tracked)
		}
		if staged := initRoleFixtureGit(t, repo, "diff", "--cached", "--name-only"); staged != "work.txt" {
			t.Errorf("staged after Jujutsu skip = %q, want the user's work.txt still staged", staged)
		}
	}
}
