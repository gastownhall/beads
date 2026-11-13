package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// TestRepoAddRelativePathSurvivesCwdChange reproduces bd-59llyl: a relative
// `bd repo add` target must still name the same workspace when a later command
// resolves it from a different working directory.
func TestRepoAddRelativePathSurvivesCwdChange(t *testing.T) {
	root := t.TempDir()
	primary := filepath.Join(root, "primary")
	other := filepath.Join(root, "other")
	elsewhere := filepath.Join(root, "elsewhere", "deep")
	for _, dir := range []string{filepath.Join(primary, ".beads"), filepath.Join(other, ".beads"), elsewhere} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(primary, ".beads", "config.yaml")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", filepath.Join(primary, ".beads"))

	t.Chdir(primary)
	if err := repoAddCmd.RunE(repoAddCmd, []string{"../other"}); err != nil {
		t.Fatalf("repo add: %v", err)
	}

	repos, err := config.GetReposFromYAML(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos.Additional) != 1 {
		t.Fatalf("repos.additional = %v, want one entry", repos.Additional)
	}

	// A later `bd repo sync` resolves the stored entry with filepath.Abs from its own cwd.
	t.Chdir(elsewhere)
	resolved, err := filepath.Abs(repos.Additional[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(resolved, ".beads")); err != nil {
		t.Fatalf("stored path %q does not resolve to the added workspace from a different cwd: %v", repos.Additional[0], err)
	}
	// An unrelated .beads must not stand in for the added workspace.
	if !sameDir(t, resolved, other) {
		t.Fatalf("stored path %q resolves to %s from a different cwd, want %s", repos.Additional[0], resolved, other)
	}
}

// sameDir reports whether a and b name the same existing directory.
func sameDir(t *testing.T, a, b string) bool {
	t.Helper()
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	if err != nil {
		t.Fatal(err)
	}
	return os.SameFile(ai, bi)
}
