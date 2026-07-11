//go:build cgo

package main

import (
	"os"
	"strings"
	"testing"
)

// TestEmbeddedCreateRepoFromNonBeadsCwd reproduces GH#3686: running
// `bd create --repo=<local path>` from a directory that has no .beads/
// workspace of its own must resolve the target repo's workspace instead of
// failing with "no beads database found".
//
// Before the fix, PersistentPreRun exited early with that error because the
// current directory had no discoverable database, so create.go's --repo
// handling never ran. The reproduction, contributor bug report, and expected
// behavior are due to kevglynn (GH#3774).
func TestEmbeddedCreateRepoFromNonBeadsCwd(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt create tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("repo_flag_resolves_target_workspace", func(t *testing.T) {
		// Target repo with a real .beads/ workspace.
		targetDir, targetBeadsDir, _ := bdInit(t, bd, "--prefix", "rp")

		// A separate directory with NO .beads/ workspace (and no .beads
		// ancestor, since it is an independent temp dir).
		noBeadsCwd := t.TempDir()

		// Sanity: the cwd genuinely has no .beads workspace.
		if _, err := os.Stat(noBeadsCwd + "/.beads"); err == nil {
			t.Fatalf("test setup: %s unexpectedly has a .beads dir", noBeadsCwd)
		}

		// Create from the non-beads cwd, targeting the other repo. Before the
		// fix this failed with "no beads database found".
		issue := bdCreate(t, bd, noBeadsCwd, "Routed from non-beads cwd", "--repo", targetDir)
		if issue.ID == "" {
			t.Fatal("expected issue ID")
		}
		if !strings.HasPrefix(issue.ID, "rp-") {
			t.Errorf("ID should have target prefix rp-, got %q", issue.ID)
		}
		if issue.Title != "Routed from non-beads cwd" {
			t.Errorf("title: got %q, want %q", issue.Title, "Routed from non-beads cwd")
		}

		// The issue must land in the target repo's store.
		assertIssueInStore(t, targetBeadsDir, "rp", issue.ID)
	})

	t.Run("no_repo_flag_still_errors_in_non_beads_cwd", func(t *testing.T) {
		// Regression guard: the no-database-found error must still fire for an
		// ordinary create with no --repo when the cwd has no workspace, so the
		// fix does not swallow the diagnostic for the common mistake.
		noBeadsCwd := t.TempDir()
		out := bdCreateFail(t, bd, noBeadsCwd, "should fail")
		if !strings.Contains(out, "no beads database found") {
			t.Errorf("expected 'no beads database found' error, got:\n%s", out)
		}
	})
}
