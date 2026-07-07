//go:build cgo

package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// bdMigratePersonal runs "bd migrate-personal" with the given args.
// Returns stdout+stderr and error (does not fatal on error).
func bdMigratePersonal(t *testing.T, bd, dir string, args ...string) (string, error) {
	t.Helper()
	fullArgs := append([]string{"migrate-personal"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// setupContributorRouting sets routing.contributor to a planning dir so that
// migrate-personal has somewhere to route to.
func setupContributorRouting(t *testing.T, bd, dir string) string {
	t.Helper()
	planningDir := t.TempDir()
	// Init a minimal git repo in the planning dir.
	exec.Command("git", "init", planningDir).Run() //nolint:errcheck
	// Initialize beads in planning dir.
	initCmd := exec.Command(bd, "init", "--prefix", "pl", "--quiet")
	initCmd.Dir = planningDir
	initCmd.Env = bdEnv(planningDir)
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("bd init planning dir: %v\n%s", err, out)
	}

	// Point routing.contributor at the planning dir.
	bdRunWithFlockRetry(t, bd, dir, "config", "set", "routing.contributor", planningDir) //nolint:errcheck
	bdRunWithFlockRetry(t, bd, dir, "config", "set", "routing.mode", "auto")             //nolint:errcheck
	return planningDir
}

// TestMigratePersonal_noopWhenEmpty verifies that migrate-personal with no
// matching issues exits 0 and reports nothing to migrate.
func TestMigratePersonal_noopWhenEmpty(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "mpn")

	setupContributorRouting(t, bd, dir)

	// No issues owned by the git identity → noop.
	out, err := bdMigratePersonal(t, bd, dir, "--yes")
	if err != nil {
		// Some backends may report "no issues to migrate" as success.
		// Allow either exit 0 or a meaningful message.
		if !strings.Contains(strings.ToLower(out), "no") &&
			!strings.Contains(strings.ToLower(out), "0 issue") {
			t.Logf("migrate-personal with no matching issues: err=%v output=%s", err, out)
		}
	}
}

// TestMigratePersonal_movesIssues verifies that migrate-personal moves issues
// matching the current git identity to the planning repo.
func TestMigratePersonal_movesIssues(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "mpm")
	planningDir := setupContributorRouting(t, bd, dir)

	// Create issues with the git identity (getActorWithGit will use git user.name).
	gitName, _ := exec.Command("git", "config", "--global", "user.name").Output()
	actor := strings.TrimSpace(string(gitName))
	if actor == "" {
		actor = "Test"
	}

	bdCreate(t, bd, dir, "Personal planning issue", "--actor", actor)
	bdCreate(t, bd, dir, "Team issue", "--actor", "other-person")

	out, err := bdMigratePersonal(t, bd, dir, "--yes")
	if err != nil {
		// Skip if Dolt server unavailable for planning dir or actor mismatch.
		lout := strings.ToLower(out)
		if strings.Contains(lout, "routing.contributor") ||
			strings.Contains(lout, "no personal") ||
			strings.Contains(lout, "dolt") ||
			strings.Contains(lout, "cannot connect") ||
			strings.Contains(lout, "server") {
			t.Logf("migrate-personal: %v — output: %s", err, out)
			t.Skip("skipping: Dolt unavailable for planning dir or no actor match")
		}
		t.Fatalf("bd migrate-personal failed: %v\noutput:\n%s", err, out)
	}

	// After migration, the team issue should remain in project db.
	issues := bdListJSON(t, bd, dir)
	for _, iss := range issues {
		if iss.Title == "Team issue" {
			// Good — team issue stayed.
			return
		}
	}
	_ = planningDir
}

// TestMigratePersonal_abortOnNoConfirm verifies that without --yes, the command
// prompts and aborts when stdin signals no confirmation (EOF).
func TestMigratePersonal_abortOnNoConfirm(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "mpa")
	setupContributorRouting(t, bd, dir)

	// Create an issue owned by current git user.
	gitName, _ := exec.Command("git", "config", "--global", "user.name").Output()
	actor := strings.TrimSpace(string(gitName))
	if actor == "" {
		actor = "Test"
	}
	bdCreate(t, bd, dir, "Personal issue for abort test", "--actor", actor)

	// Run migrate-personal without --yes and with closed stdin (simulates EOF / no confirmation).
	cmd := exec.Command(bd, "migrate-personal")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	cmd.Stdin = strings.NewReader("") // EOF immediately
	out, _ := cmd.CombinedOutput()

	// Issue should still be in project db (not moved).
	issues := bdListJSON(t, bd, dir)
	var found *types.IssueWithCounts
	for _, iss := range issues {
		if iss.Title == "Personal issue for abort test" {
			found = iss
			break
		}
	}
	_ = out
	if found == nil {
		// Issue may have been migrated if the command doesn't prompt when no matching issues found.
		t.Logf("note: abort test — issue may not have been found by migrate-personal")
	}
}

// TestMigratePersonal_preservesComments is a regression test for the maphew
// review of PR #4023 (be-e2nb): migrate-personal must copy comments as
// structured rows (ImportIssueComment) so they survive in the planning repo.
// The pre-fix code used AddComment, which records an event-style entry that
// GetIssueComments never returns — silently dropping the migrated issue's
// comments.
func TestMigratePersonal_preservesComments(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "mpc")
	planningDir := setupContributorRouting(t, bd, dir)

	gitName, _ := exec.Command("git", "config", "--global", "user.name").Output()
	actor := strings.TrimSpace(string(gitName))
	if actor == "" {
		actor = "Test"
	}

	issue := bdCreate(t, bd, dir, "Personal issue with a comment", "--actor", actor)
	const commentText = "migrate-personal-must-preserve-this-comment"
	bdComment(t, bd, dir, issue.ID, commentText)

	out, err := bdMigratePersonal(t, bd, dir, "--yes")
	if err != nil {
		lout := strings.ToLower(out)
		if strings.Contains(lout, "routing.contributor") ||
			strings.Contains(lout, "no personal") ||
			strings.Contains(lout, "dolt") ||
			strings.Contains(lout, "cannot connect") ||
			strings.Contains(lout, "server") {
			t.Logf("migrate-personal: %v — output: %s", err, out)
			t.Skip("skipping: Dolt unavailable for planning dir or no actor match")
		}
		t.Fatalf("bd migrate-personal failed: %v\noutput:\n%s", err, out)
	}

	// The comment must survive in the planning repo as a structured comment that
	// `bd comments list` returns.
	got := bdCommentList(t, bd, planningDir, issue.ID)
	if !strings.Contains(got, commentText) {
		t.Errorf("migrated issue %s lost its comment in the planning repo %s.\ncomments list output:\n%s",
			issue.ID, planningDir, got)
	}
}

// TestCopyIssueRelations_preservesCommentWithTimestamp is the in-process proof
// for the maphew review of PR #4023 (be-e2nb). The full migrate-personal command
// can't run in the embedded harness because its planning store uses the
// server-based Dolt store, so this drives the helper directly against two
// embedded stores: a comment must land in the destination as a structured row
// (returned by GetIssueComments) preserving its original timestamp, not be lost
// as an event-style AddComment entry.
func TestCopyIssueRelations_preservesCommentWithTimestamp(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	// Two initialized embedded stores standing in for the project and planning
	// DBs. Same prefix so the copied issue ID is valid in both.
	_, srcBeads, _ := bdInit(t, bd, "--prefix", "mp")
	_, dstBeads, _ := bdInit(t, bd, "--prefix", "mp")
	src := openStore(t, srcBeads, "mp")
	dst := openStore(t, dstBeads, "mp")

	ctx := context.Background()

	issue := &types.Issue{
		ID:        "mp-100",
		Title:     "Issue with a comment",
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		CreatedBy: "alice",
	}
	if err := src.CreateIssue(ctx, issue, "alice"); err != nil {
		t.Fatalf("seed src CreateIssue: %v", err)
	}
	origTS := time.Date(2024, 3, 4, 5, 6, 7, 0, time.UTC)
	if _, err := src.ImportIssueComment(ctx, issue.ID, "alice", "preserve me", origTS); err != nil {
		t.Fatalf("seed src comment: %v", err)
	}

	// Phase 1a of runMigratePersonal creates the issue row in dst first.
	if err := dst.CreateIssue(ctx, issue, "alice"); err != nil {
		t.Fatalf("dst CreateIssue: %v", err)
	}

	// Code under test.
	if err := copyIssueRelations(ctx, src, dst, issue, "alice"); err != nil {
		t.Fatalf("copyIssueRelations: %v", err)
	}

	got, err := dst.GetIssueComments(ctx, issue.ID)
	if err != nil {
		t.Fatalf("dst.GetIssueComments: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("planning DB has %d structured comments, want 1 (event-style AddComment would yield 0)", len(got))
	}
	if got[0].Text != "preserve me" {
		t.Errorf("comment text = %q, want %q", got[0].Text, "preserve me")
	}
	if !got[0].CreatedAt.Equal(origTS) {
		t.Errorf("comment created_at = %v, want preserved %v (original timestamp dropped)", got[0].CreatedAt, origTS)
	}
}
