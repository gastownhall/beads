package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/git"
)

func TestResetRepoCachesForTestScopesDiscovery(t *testing.T) {
	t.Setenv("BEADS_DIR", "")
	repo := t.TempDir()
	initGitRepoAt(t, repo)
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.Mkdir(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Chdir(t.TempDir())
	resetRepoCachesForTest(t)
	assertNoWorkspace := func() {
		t.Helper()
		if got := git.GetRepoRoot(); got != "" {
			t.Fatalf("non-repository directory resolved Git root %q", got)
		}
		if ctx, err := beads.GetRepoContext(); err == nil || ctx != nil {
			t.Fatalf("non-workspace directory resolved context %+v, err=%v", ctx, err)
		}
	}
	// Prime both cached failures before entering a different fixture.
	assertNoWorkspace()
	t.Run("repository", func(t *testing.T) {
		t.Chdir(repo)
		resetRepoCachesForTest(t)
		ctx, err := beads.GetRepoContext()
		if err != nil {
			t.Fatal(err)
		}
		for _, paths := range []struct{ got, want string }{
			{git.GetRepoRoot(), repo},
			{ctx.RepoRoot, repo},
			{ctx.BeadsDir, beadsDir},
		} {
			gotInfo, err := os.Stat(paths.got)
			if err != nil {
				t.Fatal(err)
			}
			wantInfo, err := os.Stat(paths.want)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(gotInfo, wantInfo) {
				t.Errorf("discovered %q, want filesystem identity of %q", paths.got, paths.want)
			}
		}
	})
	// The child primed successful answers; its cleanup must discard both.
	assertNoWorkspace()
}

// runInDir shares the invariant above, so it owes the same guarantee: workspace
// discovery must follow it into dir and must not survive the restore.
func TestRunInDirScopesWorkspaceDiscovery(t *testing.T) {
	t.Setenv("BEADS_DIR", "")
	repo := t.TempDir()
	initGitRepoAt(t, repo)
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.Mkdir(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Chdir(t.TempDir())
	resetRepoCachesForTest(t)
	assertNoWorkspaceContext := func() {
		t.Helper()
		if ctx, err := beads.GetRepoContext(); err == nil || ctx != nil {
			t.Fatalf("non-workspace directory resolved context %+v, err=%v", ctx, err)
		}
	}
	// Prime the cached failure that a Git-only reset would leave in place.
	assertNoWorkspaceContext()

	runInDir(t, repo, func() {
		ctx, err := beads.GetRepoContext()
		if err != nil {
			t.Fatalf("runInDir left workspace discovery scoped to the previous directory: %v", err)
		}
		gotInfo, err := os.Stat(ctx.BeadsDir)
		if err != nil {
			t.Fatal(err)
		}
		wantInfo, err := os.Stat(beadsDir)
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(gotInfo, wantInfo) {
			t.Errorf("discovered %q, want filesystem identity of %q", ctx.BeadsDir, beadsDir)
		}
	})

	// runInDir primed a successful answer; its restore must discard it too.
	assertNoWorkspaceContext()
}
