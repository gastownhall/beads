//go:build cgo

package main

import (
	"os"
	"os/exec"
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

	t.Run("gate_create_repo_flag_is_not_a_workspace", func(t *testing.T) {
		// The --repo bypass is for top-level create only. `bd gate create
		// --repo` names the GitHub repository a gh:run/gh:pr gate is checked
		// in, so outside a workspace it must fail like any other command.
		// While the bypass matched the leaf name "create", the slug form
		// opened a stray ./owner/repo/.beads workspace under the cwd and the
		// URL form panicked on the nil store.
		for _, tc := range []struct{ name, repo string }{
			{"slug", "owner/repo"},
			{"url", "https://github.com/owner/repo"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				noBeadsCwd := t.TempDir()
				cmd := exec.Command(bd, "gate", "create", "--type=gh:pr", "--blocks", "bd-abc", "--await-id=42", "--repo", tc.repo)
				cmd.Dir = noBeadsCwd
				cmd.Env = bdEnv(t.TempDir())
				out, err := cmd.CombinedOutput()
				if err == nil {
					t.Fatalf("bd gate create --repo %s succeeded outside a workspace:\n%s", tc.repo, out)
				}
				if !strings.Contains(string(out), "no beads database found") || strings.Contains(string(out), "panic") {
					t.Errorf("expected 'no beads database found' error, got:\n%s", out)
				}
				entries, readErr := os.ReadDir(noBeadsCwd)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if len(entries) != 0 {
					t.Errorf("bd gate create wrote into the cwd: %d entries, first %q", len(entries), entries[0].Name())
				}
			})
		}
	})
}
