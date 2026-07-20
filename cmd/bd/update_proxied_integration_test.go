//go:build cgo

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestProxiedServerUpdate(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("no_ids_errors", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "un1")
		out := bdProxiedUpdateFail(t, bd, p.dir)
		if !strings.Contains(out, "no issue ID provided") {
			t.Errorf("expected no-id error, got: %s", out)
		}
	})

	t.Run("no_flags_is_noop_message", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "un2")
		issue := bdProxiedCreate(t, bd, p.dir, "Seed")
		out, err := bdProxiedRun(t, bd, p.dir, "update", issue.ID)
		if err != nil {
			t.Fatalf("update with no flags should succeed, got: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "No updates specified") {
			t.Errorf("expected 'No updates specified', got: %s", out)
		}
	})

	t.Run("field_updates_round_trip", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "uf")
		issue := bdProxiedCreate(t, bd, p.dir, "Original")
		updated := bdProxiedUpdateOne(t, bd, p.dir, issue.ID,
			"--title", "Renamed",
			"-p", "0",
			"--assignee", "alice",
			"--description", "new body")
		if updated.Title != "Renamed" {
			t.Errorf("title: got %q, want %q", updated.Title, "Renamed")
		}
		if updated.Priority != 0 {
			t.Errorf("priority: got %d, want 0", updated.Priority)
		}
		if updated.Assignee != "alice" {
			t.Errorf("assignee: got %q, want %q", updated.Assignee, "alice")
		}
		if updated.Description != "new body" {
			t.Errorf("description: got %q, want %q", updated.Description, "new body")
		}
	})

	t.Run("claim_sets_assignee_and_in_progress", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "uc")
		issue := bdProxiedCreate(t, bd, p.dir, "To claim")
		updated := bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--claim")
		if updated.Status != types.StatusInProgress {
			t.Errorf("status: got %q, want in_progress", updated.Status)
		}
		if updated.Assignee == "" {
			t.Errorf("assignee should be set after --claim, got empty")
		}
	})

	t.Run("claim_then_other_user_conflicts", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ucc")
		issue := bdProxiedCreate(t, bd, p.dir, "Contested")
		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--claim", "--assignee", "alice")

		out := bdProxiedUpdateFail(t, bd, p.dir, issue.ID, "--claim", "--assignee", "bob")
		if !strings.Contains(out, "already claimed") {
			t.Errorf("expected 'already claimed' error, got: %s", out)
		}
	})

	// Parity with the non-proxied update_claim_batch_partial_loss_exits_nonzero:
	// a batch where one claim is lost and another is won must exit non-zero so
	// the lost claim is not hidden from exit-code automation (beads audit
	// finding #10). The winner is still committed.
	t.Run("claim_batch_partial_loss_exits_nonzero", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ucbp")
		lost := bdProxiedCreate(t, bd, p.dir, "Batch lost")
		won := bdProxiedCreate(t, bd, p.dir, "Batch won")
		// Pre-claim `lost` as alice so bob's claim in the batch loses.
		bdProxiedUpdateOne(t, bd, p.dir, lost.ID, "--claim", "--actor", "alice")

		out := bdProxiedUpdateFail(t, bd, p.dir, lost.ID, won.ID, "--claim", "--actor", "bob")
		if !strings.Contains(out, "already claimed") {
			t.Errorf("expected 'already claimed' error in batch output, got: %s", out)
		}
		// The winning claim still lands despite the batch exiting non-zero.
		gotWon := bdProxiedShow(t, bd, p.dir, won.ID)
		if gotWon.Status != types.StatusInProgress {
			t.Errorf("winning claim %s: status = %s, want in_progress", won.ID, gotWon.Status)
		}
		if gotWon.Assignee != "bob" {
			t.Errorf("winning claim %s: assignee = %q, want bob", won.ID, gotWon.Assignee)
		}
		// The lost issue stays claimed by alice.
		gotLost := bdProxiedShow(t, bd, p.dir, lost.ID)
		if gotLost.Assignee != "alice" {
			t.Errorf("lost issue %s assignee = %q, want alice (unchanged)", lost.ID, gotLost.Assignee)
		}
	})

	// Parity with the non-proxied TestMultiIDUpdatePartialFailureExitsNonzero:
	// a generic per-ID failure (a bogus ID) between two good IDs must exit
	// non-zero and name the failed ID, while the good IDs stay applied. Before
	// the proxied-parity fix the proxied path returned exit 0 whenever another
	// ID succeeded, hiding the failure from exit-code automation.
	t.Run("generic_batch_partial_failure_exits_nonzero", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ugbp")
		good1 := bdProxiedCreate(t, bd, p.dir, "Batch good 1")
		good2 := bdProxiedCreate(t, bd, p.dir, "Batch good 2")
		const bogus = "ugbp-zzzzzzzzzz" // cannot collide with generated IDs

		out := bdProxiedUpdateFail(t, bd, p.dir, good1.ID, bogus, good2.ID, "--priority", "0")
		if !strings.Contains(out, bogus) {
			t.Errorf("expected failed ID %s in output, got: %s", bogus, out)
		}
		// The command is per-ID, not atomic: both good IDs must still be applied.
		for _, id := range []string{good1.ID, good2.ID} {
			if got := bdProxiedShow(t, bd, p.dir, id); got.Priority != 0 {
				t.Errorf("issue %s priority = %d, want 0 (successful IDs must stay applied)", id, got.Priority)
			}
		}
	})

	// The --json partial-failure contract on the proxied path: stdout keeps the
	// array-of-updated-issues success shape and the last stderr line is the
	// machine-parseable failed-ID report, matching the non-proxied path.
	t.Run("generic_batch_partial_failure_json_reports_failed_ids", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ugbj")
		good1 := bdProxiedCreate(t, bd, p.dir, "JSON good 1")
		good2 := bdProxiedCreate(t, bd, p.dir, "JSON good 2")
		const bogus = "ugbj-zzzzzzzzzz"

		stdout, stderr, err := bdProxiedUpdateRaw(t, bd, p.dir, "--json", good1.ID, bogus, good2.ID, "--priority", "0")
		if err == nil {
			t.Fatalf("proxied --json batch with a bogus ID exited 0, want non-zero\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}

		// stdout keeps the success array of the two updated issues.
		start := strings.Index(stdout, "[")
		if start < 0 {
			t.Fatalf("no JSON success array on stdout:\n%s", stdout)
		}
		var updated []*types.Issue
		if uerr := json.Unmarshal([]byte(stdout[start:]), &updated); uerr != nil {
			t.Fatalf("stdout is not the array-of-issues success shape: %v\n%s", uerr, stdout)
		}
		gotIDs := map[string]bool{}
		for _, u := range updated {
			gotIDs[u.ID] = true
		}
		if len(updated) != 2 || !gotIDs[good1.ID] || !gotIDs[good2.ID] {
			t.Errorf("stdout success array = %v, want exactly the two good IDs %s %s", updated, good1.ID, good2.ID)
		}

		// The last stderr line is a JSON failure report naming the bogus ID.
		lines := strings.Split(strings.TrimSpace(stderr), "\n")
		last := lines[len(lines)-1]
		var report struct {
			Error  string `json:"error"`
			Failed []struct {
				ID    string `json:"id"`
				Error string `json:"error"`
			} `json:"failed"`
		}
		if uerr := json.Unmarshal([]byte(last), &report); uerr != nil {
			t.Fatalf("last stderr line is not a JSON failure report: %v\nstderr:\n%s", uerr, stderr)
		}
		if report.Error == "" {
			t.Errorf("JSON failure report has empty error message: %s", last)
		}
		if len(report.Failed) != 1 || report.Failed[0].ID != bogus {
			t.Errorf("JSON failure report failed list = %+v, want exactly one entry for %s", report.Failed, bogus)
		}
	})

	t.Run("add_remove_labels", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ul")
		issue := bdProxiedCreate(t, bd, p.dir, "Labeled")
		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--add-label", "perf,tech-debt")
		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--remove-label", "perf")

		db := openProxiedDB(t, p)
		labels := getProxiedLabels(t, db, issue.ID)
		if len(labels) != 1 || labels[0] != "tech-debt" {
			t.Errorf("labels after add+remove: got %v, want [tech-debt]", labels)
		}
	})

	t.Run("set_labels_diffs", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "usl")
		issue := bdProxiedCreate(t, bd, p.dir, "Set labels")
		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--add-label", "a,b,c")
		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--set-labels", "b,d")

		db := openProxiedDB(t, p)
		labels := getProxiedLabels(t, db, issue.ID)
		got := strings.Join(labels, ",")
		if got != "b,d" && got != "d,b" {
			t.Errorf("labels after --set-labels: got %v, want [b d] (any order)", labels)
		}
	})

	t.Run("reparent_replaces_existing_parent", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "urp")
		oldParent := bdProxiedCreate(t, bd, p.dir, "Old parent", "-t", "epic")
		newParent := bdProxiedCreate(t, bd, p.dir, "New parent", "-t", "epic")
		child := bdProxiedCreate(t, bd, p.dir, "Child", "--parent", oldParent.ID)

		bdProxiedUpdateOne(t, bd, p.dir, child.ID, "--parent", newParent.ID)

		db := openProxiedDB(t, p)
		assertProxiedDepExistsWithType(t, db, child.ID, newParent.ID, string(types.DepParentChild))
		var oldRowCount int
		if err := db.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_issue_id = ?",
			child.ID, oldParent.ID).Scan(&oldRowCount); err != nil {
			t.Fatalf("count old parent dep: %v", err)
		}
		if oldRowCount != 0 {
			t.Errorf("old parent dep should be gone, got %d rows", oldRowCount)
		}
	})

	t.Run("reparent_empty_unparents", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "uup")
		parent := bdProxiedCreate(t, bd, p.dir, "Parent", "-t", "epic")
		child := bdProxiedCreate(t, bd, p.dir, "Child", "--parent", parent.ID)

		bdProxiedUpdateOne(t, bd, p.dir, child.ID, "--parent", "")

		db := openProxiedDB(t, p)
		var count int
		if err := db.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND type = 'parent-child'",
			child.ID).Scan(&count); err != nil {
			t.Fatalf("count parent dep: %v", err)
		}
		if count != 0 {
			t.Errorf("expected no parent-child dep after unparent, got %d", count)
		}
	})

	t.Run("close_unblocks_dependents", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ucb")
		blocker := bdProxiedCreate(t, bd, p.dir, "Blocker")
		blocked := bdProxiedCreate(t, bd, p.dir, "Dependent", "--deps", "depends-on:"+blocker.ID)

		db := openProxiedDB(t, p)
		if !readIsBlocked(t, db, blocked.ID) {
			t.Fatalf("dependent should be blocked before blocker closes")
		}

		bdProxiedUpdateOne(t, bd, p.dir, blocker.ID, "-s", "closed")

		if readIsBlocked(t, db, blocked.ID) {
			t.Errorf("dependent should be unblocked after blocker closes")
		}
	})

	t.Run("invalid_status_rejected", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "uis")
		issue := bdProxiedCreate(t, bd, p.dir, "Status test")
		out := bdProxiedUpdateFail(t, bd, p.dir, issue.ID, "-s", "not-a-real-status")
		if !strings.Contains(out, "invalid status") {
			t.Errorf("expected 'invalid status' error, got: %s", out)
		}
	})

	t.Run("multiple_ids_update_all", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "umu")
		a := bdProxiedCreate(t, bd, p.dir, "A")
		b := bdProxiedCreate(t, bd, p.dir, "B")
		issues := bdProxiedUpdate(t, bd, p.dir, a.ID, b.ID, "--assignee", "team")
		if len(issues) != 2 {
			t.Fatalf("expected 2 updated issues, got %d", len(issues))
		}
		for _, iss := range issues {
			if iss.Assignee != "team" {
				t.Errorf("%s assignee: got %q, want team", iss.ID, iss.Assignee)
			}
		}
	})

	t.Run("defer_clear_restores_open", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "udf")
		issue := bdProxiedCreate(t, bd, p.dir, "Deferred")

		deferred := bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--defer", "+1d")
		if deferred.Status != types.StatusDeferred {
			t.Fatalf("expected deferred status, got %q", deferred.Status)
		}

		restored := bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--defer", "")
		if restored.Status != types.StatusOpen {
			t.Errorf("--defer='' should restore open, got %q", restored.Status)
		}
	})

	t.Run("update_type", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ut")
		issue := bdProxiedCreate(t, bd, p.dir, "Type test")
		updated := bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--type", "bug")
		if updated.IssueType != types.TypeBug {
			t.Errorf("type: got %q, want %q", updated.IssueType, types.TypeBug)
		}
	})

	t.Run("update_type_invalid_rejected", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "uti")
		issue := bdProxiedCreate(t, bd, p.dir, "Bad type test")
		out := bdProxiedUpdateFail(t, bd, p.dir, issue.ID, "--type", "not-a-real-type")
		if !strings.Contains(strings.ToLower(out), "invalid") &&
			!strings.Contains(strings.ToLower(out), "unknown") {
			t.Errorf("expected invalid/unknown type error, got: %s", out)
		}
	})

	t.Run("update_type_custom", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "utc")
		issue := bdProxiedCreate(t, bd, p.dir, "Custom type test")

		db := openProxiedDB(t, p)
		if _, err := db.ExecContext(context.Background(),
			"INSERT INTO config (`key`, value) VALUES ('types.custom', ?) "+
				"ON DUPLICATE KEY UPDATE value = VALUES(value)",
			"molecule"); err != nil {
			t.Fatalf("seed types.custom: %v", err)
		}

		updated := bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--type", "molecule")
		if string(updated.IssueType) != "molecule" {
			t.Errorf("type: got %q, want %q", updated.IssueType, "molecule")
		}
	})

	t.Run("description_from_file", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ubf")
		issue := bdProxiedCreate(t, bd, p.dir, "Body file test")

		bodyPath := filepath.Join(p.dir, "body.txt")
		if err := os.WriteFile(bodyPath, []byte("from file"), 0o600); err != nil {
			t.Fatalf("write body file: %v", err)
		}

		updated := bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--body-file", bodyPath)
		if updated.Description != "from file" {
			t.Errorf("description: got %q, want %q", updated.Description, "from file")
		}
	})

	t.Run("description_body_alias", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "uba")
		issue := bdProxiedCreate(t, bd, p.dir, "Body alias test")
		updated := bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--body", "via body flag")
		if updated.Description != "via body flag" {
			t.Errorf("description: got %q, want %q", updated.Description, "via body flag")
		}
	})

	t.Run("update_creates_dolt_commit", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "udc")
		issue := bdProxiedCreate(t, bd, p.dir, "Dolt commit test")

		db := openProxiedDB(t, p)
		var before string
		if err := db.QueryRowContext(context.Background(),
			"SELECT HASHOF('HEAD')").Scan(&before); err != nil {
			t.Fatalf("read HEAD before: %v", err)
		}

		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--title", "Renamed for commit")

		var after string
		if err := db.QueryRowContext(context.Background(),
			"SELECT HASHOF('HEAD')").Scan(&after); err != nil {
			t.Fatalf("read HEAD after: %v", err)
		}
		if after == before {
			t.Errorf("HEAD did not advance: before=%s after=%s (uw.Commit should produce a Dolt commit)",
				before, after)
		}
	})

	t.Run("reparent_from_orphan", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "urpo")
		parent := bdProxiedCreate(t, bd, p.dir, "New parent", "-t", "epic")
		orphan := bdProxiedCreate(t, bd, p.dir, "Orphan child")

		bdProxiedUpdateOne(t, bd, p.dir, orphan.ID, "--parent", parent.ID)

		db := openProxiedDB(t, p)
		assertProxiedDepExistsWithType(t, db, orphan.ID, parent.ID, string(types.DepParentChild))
	})

	t.Run("close_with_session_flag", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ucws")
		issue := bdProxiedCreate(t, bd, p.dir, "Close-with-session flag test")

		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "-s", "closed", "--session", "sess-flag")

		db := openProxiedDB(t, p)
		var got sql.NullString
		if err := db.QueryRowContext(context.Background(),
			"SELECT closed_by_session FROM issues WHERE id = ?", issue.ID).Scan(&got); err != nil {
			t.Fatalf("read closed_by_session: %v", err)
		}
		if !got.Valid || got.String != "sess-flag" {
			t.Errorf("closed_by_session: got %q (valid=%v), want %q", got.String, got.Valid, "sess-flag")
		}
	})

	t.Run("close_with_session_env", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ucwe")
		issue := bdProxiedCreate(t, bd, p.dir, "Close-with-session env test")

		if _, stderr, err := bdProxiedRunEnv(t, bd, p.dir, []string{"CLAUDE_SESSION_ID=sess-env"}, "update", issue.ID, "-s", "closed"); err != nil {
			t.Fatalf("update failed: %v\n%s", err, stderr)
		}

		db := openProxiedDB(t, p)
		var got sql.NullString
		if err := db.QueryRowContext(context.Background(),
			"SELECT closed_by_session FROM issues WHERE id = ?", issue.ID).Scan(&got); err != nil {
			t.Fatalf("read closed_by_session: %v", err)
		}
		if !got.Valid || got.String != "sess-env" {
			t.Errorf("closed_by_session: got %q (valid=%v), want %q (from CLAUDE_SESSION_ID)",
				got.String, got.Valid, "sess-env")
		}
	})

	t.Run("ephemeral_persistent_conflict", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "uepc")
		issue := bdProxiedCreate(t, bd, p.dir, "Ephemeral/persistent conflict test")
		out := bdProxiedUpdateFail(t, bd, p.dir, issue.ID, "--ephemeral", "--persistent")
		if !strings.Contains(out, "cannot specify both --ephemeral and --persistent") {
			t.Errorf("expected conflict error, got: %s", out)
		}
	})

	t.Run("update_history", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "uh")
		issue := bdProxiedCreate(t, bd, p.dir, "History test")

		db := openProxiedDB(t, p)
		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--no-history")
		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--history")

		var noHistory int
		if err := db.QueryRowContext(context.Background(),
			"SELECT no_history FROM issues WHERE id = ?", issue.ID).Scan(&noHistory); err != nil {
			t.Fatalf("read no_history: %v", err)
		}
		if noHistory != 0 {
			t.Errorf("no_history: got %d, want 0 after --history", noHistory)
		}
	})

	t.Run("update_no_history", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "unh")
		issue := bdProxiedCreate(t, bd, p.dir, "No history test")

		db := openProxiedDB(t, p)
		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--no-history")

		var noHistory int
		if err := db.QueryRowContext(context.Background(),
			"SELECT no_history FROM issues WHERE id = ?", issue.ID).Scan(&noHistory); err != nil {
			t.Fatalf("read no_history: %v", err)
		}
		if noHistory != 1 {
			t.Errorf("no_history: got %d, want 1 after --no-history", noHistory)
		}
	})

	t.Run("update_persistent", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ups")
		issue := bdProxiedCreate(t, bd, p.dir, "Persistent test")

		db := openProxiedDB(t, p)
		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--ephemeral")
		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--persistent")

		var ephemeral int
		if err := db.QueryRowContext(context.Background(),
			"SELECT ephemeral FROM issues WHERE id = ?", issue.ID).Scan(&ephemeral); err != nil {
			t.Fatalf("read ephemeral: %v", err)
		}
		if ephemeral != 0 {
			t.Errorf("ephemeral: got %d, want 0 after --persistent", ephemeral)
		}
	})

	t.Run("update_ephemeral", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "uep")
		issue := bdProxiedCreate(t, bd, p.dir, "Ephemeral test")

		db := openProxiedDB(t, p)
		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--ephemeral")

		var ephemeral int
		if err := db.QueryRowContext(context.Background(),
			"SELECT ephemeral FROM issues WHERE id = ?", issue.ID).Scan(&ephemeral); err != nil {
			t.Fatalf("read ephemeral: %v", err)
		}
		if ephemeral != 1 {
			t.Errorf("ephemeral: got %d, want 1 after --ephemeral", ephemeral)
		}
	})

	t.Run("reopen_reblocks_dependents", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "urb")
		blocker := bdProxiedCreate(t, bd, p.dir, "Reopen blocker")
		blocked := bdProxiedCreate(t, bd, p.dir, "Reopen dependent", "--deps", "depends-on:"+blocker.ID)

		db := openProxiedDB(t, p)
		if !readIsBlocked(t, db, blocked.ID) {
			t.Fatalf("dependent should be blocked before close")
		}

		bdProxiedUpdateOne(t, bd, p.dir, blocker.ID, "-s", "closed")
		if readIsBlocked(t, db, blocked.ID) {
			t.Fatalf("dependent should be unblocked after blocker closes")
		}

		bdProxiedUpdateOne(t, bd, p.dir, blocker.ID, "-s", "open")
		if !readIsBlocked(t, db, blocked.ID) {
			t.Errorf("dependent should be re-blocked after blocker reopens")
		}
	})

	t.Run("update_nonexistent_id", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "unx")
		out := bdProxiedUpdateFail(t, bd, p.dir, "unx-doesnotexist", "--title", "x")
		if !strings.Contains(strings.ToLower(out), "not found") &&
			!strings.Contains(strings.ToLower(out), "no rows") &&
			!strings.Contains(strings.ToLower(out), "error") {
			t.Errorf("expected a not-found / error message, got: %s", out)
		}
	})

	t.Run("update_invalid_priority", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "uip")
		issue := bdProxiedCreate(t, bd, p.dir, "Invalid priority test")
		out := bdProxiedUpdateFail(t, bd, p.dir, issue.ID, "-p", "99")
		if !strings.Contains(out, "invalid priority") {
			t.Errorf("expected 'invalid priority' error, got: %s", out)
		}
	})

	t.Run("update_metadata_invalid_json", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "umij")
		issue := bdProxiedCreate(t, bd, p.dir, "Metadata invalid JSON test")
		out := bdProxiedUpdateFail(t, bd, p.dir, issue.ID, "--metadata", "not json at all")
		if !strings.Contains(out, "invalid JSON") {
			t.Errorf("expected 'invalid JSON' error, got: %s", out)
		}
	})

	t.Run("update_metadata_at_file", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "umaf")
		issue := bdProxiedCreate(t, bd, p.dir, "Metadata @file test")

		metaPath := filepath.Join(p.dir, "meta.json")
		if err := os.WriteFile(metaPath, []byte(`{"src":"file","n":7}`), 0o600); err != nil {
			t.Fatalf("write metadata file: %v", err)
		}

		updated := bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--metadata", "@"+metaPath)
		var got map[string]any
		if err := json.Unmarshal(updated.Metadata, &got); err != nil {
			t.Fatalf("parse metadata %q: %v", updated.Metadata, err)
		}
		if got["src"] != "file" {
			t.Errorf("metadata[src]: got %v, want %q", got["src"], "file")
		}
		if got["n"] != float64(7) {
			t.Errorf("metadata[n]: got %v, want 7", got["n"])
		}
	})

	t.Run("metadata_and_set_conflict", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "umsc")
		issue := bdProxiedCreate(t, bd, p.dir, "Metadata conflict test")
		out := bdProxiedUpdateFail(t, bd, p.dir, issue.ID,
			"--metadata", `{"a":1}`,
			"--set-metadata", "x=y")
		if !strings.Contains(out, "cannot combine --metadata with --set-metadata") {
			t.Errorf("expected conflict error, got: %s", out)
		}
	})

	t.Run("update_unset_metadata", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "uum")
		issue := bdProxiedCreate(t, bd, p.dir, "Unset metadata test")
		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--metadata", `{"keep":"yes","drop":"yes"}`)

		updated := bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--unset-metadata", "drop")

		var got map[string]any
		if err := json.Unmarshal(updated.Metadata, &got); err != nil {
			t.Fatalf("parse metadata %q: %v", updated.Metadata, err)
		}
		if _, present := got["drop"]; present {
			t.Errorf("metadata[drop]: still present after --unset-metadata, got %v", got)
		}
		if got["keep"] != "yes" {
			t.Errorf("metadata[keep]: got %v, want %q", got["keep"], "yes")
		}
	})

	t.Run("update_set_metadata", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "usm")
		issue := bdProxiedCreate(t, bd, p.dir, "Set metadata test")
		updated := bdProxiedUpdateOne(t, bd, p.dir, issue.ID,
			"--set-metadata", "tier=gold",
			"--set-metadata", "score=99")

		var got map[string]any
		if err := json.Unmarshal(updated.Metadata, &got); err != nil {
			t.Fatalf("parse metadata %q: %v", updated.Metadata, err)
		}
		if got["tier"] != "gold" {
			t.Errorf("metadata[tier]: got %v, want %q", got["tier"], "gold")
		}
		if got["score"] != "99" {
			t.Errorf("metadata[score]: got %v, want %q (--set-metadata always stores string values)", got["score"], "99")
		}
	})

	t.Run("update_metadata_merge", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "umm")
		issue := bdProxiedCreate(t, bd, p.dir, "Metadata merge test")
		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--metadata", `{"a":1,"b":2}`)
		merged := bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--metadata", `{"b":3,"c":4}`)

		var got map[string]any
		if err := json.Unmarshal(merged.Metadata, &got); err != nil {
			t.Fatalf("parse metadata %q: %v", merged.Metadata, err)
		}
		want := map[string]float64{"a": 1, "b": 3, "c": 4}
		for k, v := range want {
			gv, ok := got[k].(float64)
			if !ok {
				t.Errorf("metadata[%s]: got %v (%T), want %v", k, got[k], got[k], v)
				continue
			}
			if gv != v {
				t.Errorf("metadata[%s]: got %v, want %v", k, gv, v)
			}
		}
		if len(got) != 3 {
			t.Errorf("metadata: got %d keys (%v), want 3 (a,b,c)", len(got), got)
		}
	})

	t.Run("update_metadata_json", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "umd")
		issue := bdProxiedCreate(t, bd, p.dir, "Metadata json test")
		updated := bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--metadata", `{"k":"v","n":42}`)
		var got map[string]any
		if err := json.Unmarshal(updated.Metadata, &got); err != nil {
			t.Fatalf("parse metadata %q: %v", updated.Metadata, err)
		}
		if got["k"] != "v" {
			t.Errorf("metadata[k]: got %v, want %q", got["k"], "v")
		}
		if got["n"] != float64(42) {
			t.Errorf("metadata[n]: got %v, want 42", got["n"])
		}
	})

	t.Run("update_await_id", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "uaw")
		issue := bdProxiedCreate(t, bd, p.dir, "Await id test")
		updated := bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--await-id", "gate-1")
		if updated.AwaitID != "gate-1" {
			t.Errorf("await_id: got %q, want %q", updated.AwaitID, "gate-1")
		}
	})

	t.Run("update_defer_clear_preserves_non_deferred_status", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "udfp2")
		issue := bdProxiedCreate(t, bd, p.dir, "Defer clear preserve test")

		seeded := bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--defer", "+1d", "-s", "blocked")
		if seeded.Status != types.StatusBlocked {
			t.Fatalf("seed: expected blocked, got %q", seeded.Status)
		}

		cleared := bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--defer", "")
		if cleared.Status != types.StatusBlocked {
			t.Errorf("status: got %q, want %q (defer='' should not flip non-deferred status to open)",
				cleared.Status, types.StatusBlocked)
		}
		if cleared.DeferUntil != nil {
			t.Errorf("defer_until: expected nil after clear, got %s", cleared.DeferUntil)
		}
	})

	t.Run("update_defer_past_date_keeps_status_open", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "udfp")
		issue := bdProxiedCreate(t, bd, p.dir, "Defer past test")

		updated := bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--defer", "2020-01-01")
		if updated.Status != types.StatusOpen {
			t.Errorf("status: got %q, want %q (past defer date must not flip to deferred)",
				updated.Status, types.StatusOpen)
		}
		if updated.DeferUntil == nil {
			t.Errorf("defer_until: got nil, want past timestamp set")
		}
	})

	t.Run("update_defer_respects_explicit_status", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "udfe")
		issue := bdProxiedCreate(t, bd, p.dir, "Defer explicit status test")

		updated := bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--defer", "+1d", "-s", "blocked")
		if updated.Status != types.StatusBlocked {
			t.Errorf("status: got %q, want %q (explicit --status must win over defer auto-set)",
				updated.Status, types.StatusBlocked)
		}
		if updated.DeferUntil == nil {
			t.Errorf("defer_until: got nil, want non-nil (defer still applied)")
		}
	})

	t.Run("update_defer_set", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "udfs")
		issue := bdProxiedCreate(t, bd, p.dir, "Defer set test")

		now := time.Now()
		set := bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--defer", "+1d")
		if set.Status != types.StatusDeferred {
			t.Errorf("status: got %q, want %q", set.Status, types.StatusDeferred)
		}
		if set.DeferUntil == nil {
			t.Fatalf("defer_until: got nil, want a future time")
		}
		if !set.DeferUntil.After(now) {
			t.Errorf("defer_until: got %s, expected after %s", set.DeferUntil, now)
		}
	})

	t.Run("update_due", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "udu")
		issue := bdProxiedCreate(t, bd, p.dir, "Due test")

		now := time.Now()
		set := bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--due", "+6h")
		if set.DueAt == nil {
			t.Fatalf("due_at: got nil, want a future time")
		}
		if !set.DueAt.After(now) {
			t.Errorf("due_at: got %s, expected after %s", set.DueAt, now)
		}

		cleared := bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--due", "")
		if cleared.DueAt != nil {
			t.Errorf("due_at: expected nil after clear, got %s", cleared.DueAt)
		}
	})

	t.Run("update_estimate", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ues")
		issue := bdProxiedCreate(t, bd, p.dir, "Estimate test")

		updated := bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--estimate", "60")
		if updated.EstimatedMinutes == nil {
			t.Fatalf("estimate: got nil, want 60")
		}
		if *updated.EstimatedMinutes != 60 {
			t.Errorf("estimate: got %d, want 60", *updated.EstimatedMinutes)
		}

		out := bdProxiedUpdateFail(t, bd, p.dir, issue.ID, "--estimate", "-1")
		if !strings.Contains(out, "non-negative") {
			t.Errorf("expected 'non-negative' error, got: %s", out)
		}
	})

	t.Run("update_spec_id", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "usp")
		issue := bdProxiedCreate(t, bd, p.dir, "Spec id test")
		updated := bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--spec-id", "spec-42")
		if updated.SpecID != "spec-42" {
			t.Errorf("spec_id: got %q, want %q", updated.SpecID, "spec-42")
		}
	})

	t.Run("update_external_ref_clear", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "uerc")
		issue := bdProxiedCreate(t, bd, p.dir, "External ref clear test", "--external-ref", "gh-9")
		if issue.ExternalRef == nil || *issue.ExternalRef != "gh-9" {
			t.Fatalf("seed: external_ref not set as expected, got %v", issue.ExternalRef)
		}

		cleared := bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--external-ref", "")
		if cleared.ExternalRef != nil && *cleared.ExternalRef != "" {
			t.Errorf("external_ref: expected nil or empty after clear, got %q", *cleared.ExternalRef)
		}
	})

	t.Run("update_external_ref", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "uer")
		issue := bdProxiedCreate(t, bd, p.dir, "External ref test")
		updated := bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--external-ref", "gh-9")
		if updated.ExternalRef == nil {
			t.Fatalf("external_ref: got nil, want pointer to %q", "gh-9")
		}
		if *updated.ExternalRef != "gh-9" {
			t.Errorf("external_ref: got %q, want %q", *updated.ExternalRef, "gh-9")
		}
	})

	t.Run("update_acceptance", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "uac")
		issue := bdProxiedCreate(t, bd, p.dir, "Acceptance test")

		shortFlag := bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--acceptance", "via short")
		if shortFlag.AcceptanceCriteria != "via short" {
			t.Errorf("--acceptance: got %q, want %q", shortFlag.AcceptanceCriteria, "via short")
		}

		longFlag := bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--acceptance-criteria", "via long")
		if longFlag.AcceptanceCriteria != "via long" {
			t.Errorf("--acceptance-criteria: got %q, want %q", longFlag.AcceptanceCriteria, "via long")
		}
	})

	t.Run("notes_and_append_conflict", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "una")
		issue := bdProxiedCreate(t, bd, p.dir, "Notes conflict test")
		out := bdProxiedUpdateFail(t, bd, p.dir, issue.ID, "--notes", "a", "--append-notes", "b")
		if !strings.Contains(out, "cannot specify both --notes and --append-notes") {
			t.Errorf("expected conflict error, got: %s", out)
		}
	})

	t.Run("update_notes", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "un")
		issue := bdProxiedCreate(t, bd, p.dir, "Notes test", "--notes", "first")
		updated := bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--notes", "replacement")
		if updated.Notes != "replacement" {
			t.Errorf("notes: got %q, want %q", updated.Notes, "replacement")
		}
	})

	t.Run("update_design", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "ud")
		issue := bdProxiedCreate(t, bd, p.dir, "Design test")

		flagUpdated := bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--design", "via flag")
		if flagUpdated.Design != "via flag" {
			t.Errorf("--design: got %q, want %q", flagUpdated.Design, "via flag")
		}

		designFile := filepath.Join(p.dir, "design.txt")
		if err := os.WriteFile(designFile, []byte("via file"), 0o600); err != nil {
			t.Fatalf("write design file: %v", err)
		}
		fileUpdated := bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--design-file", designFile)
		if fileUpdated.Design != "via file" {
			t.Errorf("--design-file: got %q, want %q", fileUpdated.Design, "via file")
		}
	})

	t.Run("update_type_custom_table", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "utt")
		issue := bdProxiedCreate(t, bd, p.dir, "Custom type table test")

		db := openProxiedDB(t, p)
		if _, err := db.ExecContext(context.Background(),
			"INSERT INTO custom_types (name) VALUES (?)", "swarm"); err != nil {
			t.Fatalf("seed custom_types row: %v", err)
		}

		updated := bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--type", "swarm")
		if string(updated.IssueType) != "swarm" {
			t.Errorf("type: got %q, want %q", updated.IssueType, "swarm")
		}
	})

	t.Run("append_notes_concatenates", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "uan")
		issue := bdProxiedCreate(t, bd, p.dir, "Notes", "--notes", "first")
		updated := bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--append-notes", "second")
		want := "first\nsecond"
		if updated.Notes != want {
			t.Errorf("notes: got %q, want %q", updated.Notes, want)
		}
	})
}

func TestProxiedServerUpdateHooks(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("on_update_fires_on_field_change", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		markerPath := filepath.Join(dir, "on_update_marker")
		hookBody := "#!/bin/sh\necho \"$1\" > " + markerPath + "\n"

		p := newSharedProxiedProjectWithHooks(t, bd, "uph", map[string]string{
			"on_update": hookBody,
		})
		issue := bdProxiedCreate(t, bd, p.dir, "Hook test")

		_ = os.Remove(markerPath)
		stdout, stderr, runErr := bdProxiedUpdateRaw(t, bd, p.dir, issue.ID, "--title", "After update")
		if runErr != nil {
			t.Fatalf("bd update failed: %v\nstdout:\n%s\nstderr:\n%s", runErr, stdout, stderr)
		}

		gotID, err := waitForMarker(markerPath, 5*time.Second)
		if err != nil {
			t.Fatalf("on_update hook did not fire within timeout: %v\nstdout:\n%s\nstderr:\n%s",
				err, stdout, stderr)
		}
		if strings.TrimSpace(gotID) != issue.ID {
			t.Errorf("on_update marker: got %q, want issue ID %q", strings.TrimSpace(gotID), issue.ID)
		}
	})

	t.Run("on_close_fires_when_status_transitions_to_closed", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		markerPath := filepath.Join(dir, "on_close_marker")
		hookBody := "#!/bin/sh\necho \"$1\" > " + markerPath + "\n"

		p := newSharedProxiedProjectWithHooks(t, bd, "uphc", map[string]string{
			"on_close": hookBody,
		})
		issue := bdProxiedCreate(t, bd, p.dir, "Hook close test")

		_ = os.Remove(markerPath)
		bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "-s", "closed")

		gotID, err := waitForMarker(markerPath, 5*time.Second)
		if err != nil {
			t.Fatalf("on_close hook did not fire within timeout: %v", err)
		}
		if strings.TrimSpace(gotID) != issue.ID {
			t.Errorf("on_close marker: got %q, want issue ID %q", strings.TrimSpace(gotID), issue.ID)
		}
	})
}

func waitForMarker(path string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			return string(data), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("marker %s not found after %s", path, timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestProxiedServerUpdateWisp(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("wisp_field_update_routes_to_wisps_table", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "uwf")
		wisp := bdProxiedCreate(t, bd, p.dir, "Wisp field test", "--ephemeral")
		db := openProxiedDB(t, p)
		assertRowExists(t, db, "wisps", wisp.ID)
		assertRowAbsent(t, db, "issues", wisp.ID)

		updated := bdProxiedUpdateOne(t, bd, p.dir, wisp.ID, "--title", "wisp renamed", "-p", "0")
		if updated.Title != "wisp renamed" {
			t.Errorf("title: got %q, want %q", updated.Title, "wisp renamed")
		}
		if updated.Priority != 0 {
			t.Errorf("priority: got %d, want 0", updated.Priority)
		}

		var wispTitle string
		var wispPriority int
		s := db.QueryRowContext(context.Background(),
			"SELECT title, priority FROM wisps WHERE id = ?", wisp.ID).Scan(&wispTitle, &wispPriority)
		if s != nil {
			t.Fatalf("read wisp row: %v", s)
		}
		if wispTitle != "wisp renamed" || wispPriority != 0 {
			t.Errorf("wisps row: title=%q priority=%d, want (wisp renamed, 0)", wispTitle, wispPriority)
		}
	})

	t.Run("wisp_status_close_routes_to_wisps_table", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "uws")
		wisp := bdProxiedCreate(t, bd, p.dir, "Wisp close test", "--ephemeral")

		bdProxiedUpdateOne(t, bd, p.dir, wisp.ID, "-s", "closed")

		db := openProxiedDB(t, p)
		var status string
		if err := db.QueryRowContext(context.Background(),
			"SELECT status FROM wisps WHERE id = ?", wisp.ID).Scan(&status); err != nil {
			t.Fatalf("read wisp status: %v", err)
		}
		if status != "closed" {
			t.Errorf("wisp status: got %q, want closed", status)
		}
	})

	t.Run("wisp_labels_route_to_wisp_labels", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "uwl")
		wisp := bdProxiedCreate(t, bd, p.dir, "Wisp labels test", "--ephemeral")

		bdProxiedUpdateOne(t, bd, p.dir, wisp.ID, "--add-label", "alpha,beta")

		db := openProxiedDB(t, p)
		wispLabels := readLabels(t, db, "wisp_labels", wisp.ID)
		if got := strings.Join(wispLabels, ","); got != "alpha,beta" && got != "beta,alpha" {
			t.Errorf("wisp_labels: got %v, want [alpha beta] (any order)", wispLabels)
		}
		issueLabels := readLabels(t, db, "labels", wisp.ID)
		if len(issueLabels) != 0 {
			t.Errorf("labels table must be empty for wisp ids, got %v", issueLabels)
		}
	})

	t.Run("wisp_set_labels_diffs_against_wisp_labels", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "uwsl")
		wisp := bdProxiedCreate(t, bd, p.dir, "Wisp set-labels test", "--ephemeral")

		bdProxiedUpdateOne(t, bd, p.dir, wisp.ID, "--add-label", "keep,drop")
		bdProxiedUpdateOne(t, bd, p.dir, wisp.ID, "--set-labels", "keep,add")

		db := openProxiedDB(t, p)
		got := strings.Join(readLabels(t, db, "wisp_labels", wisp.ID), ",")
		if got != "add,keep" && got != "keep,add" {
			t.Errorf("wisp_labels after set-labels: got %s, want [add keep] (any order)", got)
		}
	})

	t.Run("wisp_claim_routes_to_wisps_table", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "uwc")
		wisp := bdProxiedCreate(t, bd, p.dir, "Wisp claim test", "--ephemeral")

		updated := bdProxiedUpdateOne(t, bd, p.dir, wisp.ID, "--claim", "--actor", "alice")
		if updated.Assignee != "alice" {
			t.Errorf("assignee: got %q, want alice", updated.Assignee)
		}
		if updated.Status != types.StatusInProgress {
			t.Errorf("status: got %q, want in_progress", updated.Status)
		}

		db := openProxiedDB(t, p)
		var assignee, status string
		if err := db.QueryRowContext(context.Background(),
			"SELECT assignee, status FROM wisps WHERE id = ?", wisp.ID).Scan(&assignee, &status); err != nil {
			t.Fatalf("read wisps row: %v", err)
		}
		if assignee != "alice" || status != "in_progress" {
			t.Errorf("wisps row: assignee=%q status=%q, want (alice, in_progress)", assignee, status)
		}
	})

	t.Run("wisp_reparent_routes_to_wisp_dependencies", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "uwr")
		parent := bdProxiedCreate(t, bd, p.dir, "Wisp parent", "--ephemeral")
		child := bdProxiedCreate(t, bd, p.dir, "Wisp child", "--ephemeral")

		bdProxiedUpdateOne(t, bd, p.dir, child.ID, "--parent", parent.ID)

		db := openProxiedDB(t, p)
		var wispCount, permCount int
		if err := db.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = ? AND depends_on_wisp_id = ? AND type = 'parent-child'",
			child.ID, parent.ID).Scan(&wispCount); err != nil {
			t.Fatalf("read wisp_dependencies: %v", err)
		}
		if wispCount != 1 {
			t.Errorf("wisp_dependencies: got %d parent-child rows, want 1", wispCount)
		}
		if err := db.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM dependencies WHERE issue_id = ?", child.ID).Scan(&permCount); err != nil {
			t.Fatalf("read dependencies: %v", err)
		}
		if permCount != 0 {
			t.Errorf("dependencies (issues table) must be empty for wisp reparent, got %d", permCount)
		}
	})

	t.Run("wisp_metadata_routes_to_wisps_table", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "uwm")
		wisp := bdProxiedCreate(t, bd, p.dir, "Wisp metadata test", "--ephemeral")

		bdProxiedUpdateOne(t, bd, p.dir, wisp.ID, "--metadata", `{"src":"wisp"}`)

		db := openProxiedDB(t, p)
		var raw sql.NullString
		if err := db.QueryRowContext(context.Background(),
			"SELECT metadata FROM wisps WHERE id = ?", wisp.ID).Scan(&raw); err != nil {
			t.Fatalf("read wisp metadata: %v", err)
		}
		if !raw.Valid {
			t.Fatalf("wisp metadata: NULL, want JSON")
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(raw.String), &got); err != nil {
			t.Fatalf("parse wisp metadata %q: %v", raw.String, err)
		}
		if got["src"] != "wisp" {
			t.Errorf("metadata[src]: got %v, want %q", got["src"], "wisp")
		}
	})
}

func assertRowExists(t *testing.T, db *sql.DB, table, id string) {
	t.Helper()
	var count int
	q := "SELECT COUNT(*) FROM " + table + " WHERE id = ?"
	if err := db.QueryRowContext(context.Background(), q, id).Scan(&count); err != nil {
		t.Fatalf("read %s for %s: %v", table, id, err)
	}
	if count != 1 {
		t.Fatalf("%s row %s: count=%d, want 1", table, id, count)
	}
}

func assertRowAbsent(t *testing.T, db *sql.DB, table, id string) {
	t.Helper()
	var count int
	q := "SELECT COUNT(*) FROM " + table + " WHERE id = ?"
	if err := db.QueryRowContext(context.Background(), q, id).Scan(&count); err != nil {
		t.Fatalf("read %s for %s: %v", table, id, err)
	}
	if count != 0 {
		t.Fatalf("%s row %s: count=%d, want 0", table, id, count)
	}
}

func readLabels(t *testing.T, db *sql.DB, table, id string) []string {
	t.Helper()
	q := "SELECT label FROM " + table + " WHERE issue_id = ? ORDER BY label"
	rows, err := db.QueryContext(context.Background(), q, id)
	if err != nil {
		t.Fatalf("query %s for %s: %v", table, id, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			t.Fatalf("scan %s: %v", table, err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iter %s: %v", table, err)
	}
	return out
}

func TestProxiedServerUpdateConcurrentClaim(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	p := newSharedProxiedProject(t, bd, "ucc")
	issue := bdProxiedCreate(t, bd, p.dir, "Concurrent claim contest")

	const n = 5
	type result struct {
		actor    string
		exitErr  error
		stderr   string
		combined string
	}
	results := make([]result, n)

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			actorName := fmt.Sprintf("alice-%d", i)
			stdout, stderr, err := bdProxiedUpdateRaw(t, bd, p.dir,
				issue.ID, "--claim", "--actor", actorName)
			results[i] = result{
				actor:    actorName,
				exitErr:  err,
				stderr:   stderr,
				combined: stdout + stderr,
			}
		}()
	}
	wg.Wait()

	var winners []string
	var conflicts int
	for _, r := range results {
		if r.exitErr == nil {
			winners = append(winners, r.actor)
			continue
		}
		isClaimedConflict := strings.Contains(r.combined, "already claimed")
		isSerializationFailure := strings.Contains(r.combined, "serialization failure") ||
			strings.Contains(r.combined, "Error 1213")
		if isClaimedConflict || isSerializationFailure {
			conflicts++
			continue
		}
		t.Errorf("unexpected failure for %s: err=%v combined=%s", r.actor, r.exitErr, r.combined)
	}

	if len(winners) != 1 {
		t.Errorf("expected exactly one winner, got %d: %v", len(winners), winners)
	}
	if conflicts != n-1 {
		t.Errorf("expected %d conflicts (claim or serialization), got %d", n-1, conflicts)
	}

	db := openProxiedDB(t, p)
	var assignee string
	var status string
	if err := db.QueryRowContext(context.Background(),
		"SELECT assignee, status FROM issues WHERE id = ?", issue.ID).Scan(&assignee, &status); err != nil {
		t.Fatalf("read final issue state: %v", err)
	}
	if status != "in_progress" {
		t.Errorf("final status: got %q, want in_progress", status)
	}
	if len(winners) == 1 && assignee != winners[0] {
		t.Errorf("final assignee: got %q, want %q (the actor that won the CAS)", assignee, winners[0])
	}
}

// TestProxiedServerUpdateConcurrentMetadata is the proxied-server flavor of
// the concurrent-metadata lost-update regression test: real bd processes in
// proxied-server mode racing `bd update --set-metadata` with DISTINCT keys on
// the SAME issue. Every writer that exits 0 must have its key in the final
// metadata, and the seed key must survive.
//
// Before the fix this route lost writes two ways: buildUpdateSpecForIssue
// merged metadata from the unit of work's MVCC snapshot (erasing keys a
// concurrent writer committed after the snapshot), and when Dolt's commit-time
// merge rolled the loser back, uow.CommitWithRetries re-committed the
// now-empty session — Dolt said "nothing to commit", the handler swallowed it,
// printed "✓ Updated", and exited 0 with the write gone.
func TestProxiedServerUpdateConcurrentMetadata(t *testing.T) {
	requireProxiedServerEnv(t)
	bd := buildEmbeddedBD(t)

	p := bdProxiedInit(t, bd, "umr")
	issue := bdProxiedCreate(t, bd, p.dir, "Concurrent metadata target")
	bdProxiedUpdateOne(t, bd, p.dir, issue.ID, "--metadata", `{"seed":"yes"}`)

	const writers = 4
	const rounds = 3
	type result struct {
		key      string
		exitErr  error
		combined string
	}

	var results []result
	for round := 0; round < rounds; round++ {
		roundResults := make([]result, writers)
		var wg sync.WaitGroup
		for i := 0; i < writers; i++ {
			i := i
			key := fmt.Sprintf("w%dr%d", i, round)
			wg.Add(1)
			go func() {
				defer wg.Done()
				stdout, stderr, err := bdProxiedUpdateRaw(t, bd, p.dir,
					issue.ID, "--set-metadata", key+"=1")
				roundResults[i] = result{key: key, exitErr: err, combined: stdout + stderr}
			}()
		}
		wg.Wait()
		results = append(results, roundResults...)
	}

	final := bdProxiedShow(t, bd, p.dir, issue.ID)
	got := map[string]any{}
	if len(final.Metadata) > 0 {
		if err := json.Unmarshal(final.Metadata, &got); err != nil {
			t.Fatalf("parse final metadata %q: %v", final.Metadata, err)
		}
	}

	if got["seed"] != "yes" {
		t.Errorf("seed metadata key erased by concurrent --set-metadata writers: %v", got)
	}
	var lost []string
	for _, r := range results {
		if r.exitErr == nil {
			if _, ok := got[r.key]; !ok {
				lost = append(lost, r.key)
			}
			if strings.Contains(r.combined, "retries exhausted") {
				t.Errorf("writer %s exited 0 but reported exhausted retries:\n%s", r.key, r.combined)
			}
			continue
		}
		// A loud loss is acceptable; a silent one is the defect.
		isConflictFailure := strings.Contains(r.combined, "retries exhausted") ||
			strings.Contains(r.combined, "serialization failure") ||
			strings.Contains(r.combined, "Error 1213")
		if !isConflictFailure {
			t.Errorf("writer %s failed for an unexpected reason: err=%v\n%s", r.key, r.exitErr, r.combined)
		}
	}
	if len(lost) > 0 {
		t.Fatalf("silent lost update: %d exit-0 --set-metadata writes missing from final metadata: %v (got %v)",
			len(lost), lost, got)
	}
}

// TestProxiedServerUpdateConcurrentAppendNotes covers the notes flavor of the
// same defect: concurrent `bd update --append-notes` writers on one issue.
// Every line whose writer exited 0 must appear in the final notes.
func TestProxiedServerUpdateConcurrentAppendNotes(t *testing.T) {
	requireProxiedServerEnv(t)
	bd := buildEmbeddedBD(t)

	p := bdProxiedInit(t, bd, "unr")
	issue := bdProxiedCreate(t, bd, p.dir, "Concurrent notes target", "--notes", "seed line")

	const writers = 4
	type result struct {
		line     string
		exitErr  error
		combined string
	}
	results := make([]result, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		i := i
		line := fmt.Sprintf("appended by writer %d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			stdout, stderr, err := bdProxiedUpdateRaw(t, bd, p.dir,
				issue.ID, "--append-notes", line)
			results[i] = result{line: line, exitErr: err, combined: stdout + stderr}
		}()
	}
	wg.Wait()

	final := bdProxiedShow(t, bd, p.dir, issue.ID)
	if !strings.Contains(final.Notes, "seed line") {
		t.Errorf("seed notes erased by concurrent --append-notes writers: %q", final.Notes)
	}
	for _, r := range results {
		if r.exitErr != nil {
			isConflictFailure := strings.Contains(r.combined, "retries exhausted") ||
				strings.Contains(r.combined, "serialization failure") ||
				strings.Contains(r.combined, "Error 1213")
			if !isConflictFailure {
				t.Errorf("writer failed for an unexpected reason: err=%v\n%s", r.exitErr, r.combined)
			}
			continue
		}
		if !strings.Contains(final.Notes, r.line) {
			t.Errorf("silent lost update: exit-0 append %q missing from final notes %q", r.line, final.Notes)
		}
	}
}

func readIsBlocked(t *testing.T, db *sql.DB, id string) bool {
	t.Helper()
	var v int
	if err := db.QueryRowContext(context.Background(),
		"SELECT is_blocked FROM issues WHERE id = ?", id).Scan(&v); err != nil {
		t.Fatalf("read is_blocked for %s: %v", id, err)
	}
	return v != 0
}
