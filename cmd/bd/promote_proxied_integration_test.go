//go:build cgo

package main

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

// TestProxiedServerPromote is the promote-family journey on the shared
// proxied server. The downstream contract (wyvern wheelhouse portcullis
// salvage) is that promote REWRITES THE ROW IN PLACE: same id, wisp_type
// retained, labels/deps/events/comments preserved, still visible to
// `bd list --include-infra --wisp-type <t>`, no longer purge-reclaimable,
// same --json shape and error contract as classic.
func TestProxiedServerPromote(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("journey_rewrites_row_in_place", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pm")
		target := bdProxiedCreate(t, bd, p.dir, "Promote dep target", "--type", "task")
		inbound := bdProxiedCreate(t, bd, p.dir, "Inbound dependent", "--type", "task")
		wisp := bdProxiedCreate(t, bd, p.dir, "Promote journey wisp",
			"--ephemeral", "--wisp-type", "patrol", "--label", "keep-me")

		if out, err := bdProxiedRun(t, bd, p.dir, "comment", wisp.ID, "pre-promote note"); err != nil {
			t.Fatalf("bd comment: %v\n%s", err, out)
		}

		db := openProxiedDB(t, p)
		// Outbound edge lives in the wisp plane while the issue is a wisp.
		if _, err := db.Exec(
			"INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by) VALUES (UUID(), ?, ?, ?, NOW(), 'test')",
			wisp.ID, target.ID, "blocks"); err != nil {
			t.Fatalf("plant outbound wisp edge: %v", err)
		}
		// Inbound edge from a durable issue targets the wisp column.
		if _, err := db.Exec(
			"INSERT INTO dependencies (id, issue_id, depends_on_wisp_id, type, created_at, created_by) VALUES (UUID(), ?, ?, ?, NOW(), 'test')",
			inbound.ID, wisp.ID, "blocks"); err != nil {
			t.Fatalf("plant inbound edge: %v", err)
		}

		head := proxiedDoltHead(t, db)

		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "promote", wisp.ID, "--reason", "worth keeping")
		if err != nil {
			t.Fatalf("bd promote: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if !strings.Contains(stdout, "Promoted "+wisp.ID+" to permanent bead") {
			t.Errorf("expected promote success line, got: %s", stdout)
		}

		// Exactly one Dolt commit, carrying the classic commit message.
		if n := proxiedDoltCommitCountSince(t, db, head); n != 1 {
			t.Errorf("expected exactly 1 Dolt commit for promote, got %d", n)
		}
		var msg string
		if err := db.QueryRow("SELECT message FROM DOLT_LOG('HEAD', '--not', ?)", head).Scan(&msg); err != nil {
			t.Fatalf("read promote commit message: %v", err)
		}
		if msg != "bd: promote "+wisp.ID {
			t.Errorf("commit message = %q, want %q", msg, "bd: promote "+wisp.ID)
		}

		// The row moved planes in place: same id, flag cleared, type retained.
		if n := countRows(t, db, "SELECT COUNT(*) FROM wisps WHERE id = ?", wisp.ID); n != 0 {
			t.Errorf("expected wisps row gone after promote, found %d", n)
		}
		var ephemeral bool
		var wispType string
		if err := db.QueryRow("SELECT ephemeral, wisp_type FROM issues WHERE id = ?", wisp.ID).Scan(&ephemeral, &wispType); err != nil {
			t.Fatalf("read promoted issues row: %v", err)
		}
		if ephemeral {
			t.Error("expected promoted row non-ephemeral")
		}
		if wispType != "patrol" {
			t.Errorf("wisp_type = %q, want patrol (retained)", wispType)
		}

		// Aux rows moved to the permanent tables and the wisp tables emptied.
		if n := countRows(t, db, "SELECT COUNT(*) FROM labels WHERE issue_id = ? AND label = 'keep-me'", wisp.ID); n != 1 {
			t.Errorf("expected label keep-me in labels, got %d", n)
		}
		if n := countRows(t, db, "SELECT COUNT(*) FROM wisp_labels WHERE issue_id = ?", wisp.ID); n != 0 {
			t.Errorf("expected wisp_labels emptied, got %d", n)
		}
		if n := countRows(t, db, "SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_issue_id = ?", wisp.ID, target.ID); n != 1 {
			t.Errorf("expected outbound dep in dependencies, got %d", n)
		}
		if n := countRows(t, db, "SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = ?", wisp.ID); n != 0 {
			t.Errorf("expected wisp_dependencies emptied, got %d", n)
		}
		if n := countRows(t, db, "SELECT COUNT(*) FROM events WHERE issue_id = ?", wisp.ID); n == 0 {
			t.Error("expected events carried to events table")
		}
		if n := countRows(t, db, "SELECT COUNT(*) FROM wisp_events WHERE issue_id = ?", wisp.ID); n != 0 {
			t.Errorf("expected wisp_events emptied, got %d", n)
		}

		// Inbound edge retargeted from the wisp column to the issue column.
		if n := countRows(t, db, "SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_issue_id = ? AND depends_on_wisp_id IS NULL", inbound.ID, wisp.ID); n != 1 {
			t.Errorf("expected inbound edge retargeted to depends_on_issue_id, got %d", n)
		}

		// Comments preserved, promotion comment (with reason) appended, all
		// in the permanent table.
		if n := countRows(t, db, "SELECT COUNT(*) FROM wisp_comments WHERE issue_id = ?", wisp.ID); n != 0 {
			t.Errorf("expected wisp_comments emptied, got %d", n)
		}
		if n := countRows(t, db, "SELECT COUNT(*) FROM comments WHERE issue_id = ? AND text = 'pre-promote note'", wisp.ID); n != 1 {
			t.Errorf("expected pre-promote comment preserved, got %d", n)
		}
		if n := countRows(t, db, "SELECT COUNT(*) FROM comments WHERE issue_id = ? AND text = 'Promoted from Level 0: worth keeping'", wisp.ID); n != 1 {
			t.Errorf("expected promotion comment with reason, got %d", n)
		}

		// Downstream contract: the promoted row is still returned by
		// `bd list --include-infra --wisp-type patrol` (callers' -s open
		// filters keep working because the default status is unchanged).
		listed := bdProxiedListJSON(t, bd, p, "--include-infra", "--wisp-type", "patrol")
		if !containsID(listed, wisp.ID) {
			t.Errorf("expected promoted %s in list --include-infra --wisp-type patrol, got %d rows", wisp.ID, len(listed))
		}
	})

	t.Run("json_shape_matches_classic", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pmj")
		wisp := bdProxiedCreate(t, bd, p.dir, "Promote JSON wisp",
			"--ephemeral", "--wisp-type", "heartbeat", "--label", "shaped")

		out, err := bdProxiedRun(t, bd, p.dir, "promote", wisp.ID, "--json")
		if err != nil {
			t.Fatalf("bd promote --json: %v\n%s", err, out)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("parse promote JSON: %v\n%s", err, out)
		}
		// Classic prints the re-read issue object: id preserved, ephemeral
		// cleared (omitempty ⇒ absent), wisp_type retained, labels included.
		if m["id"] != wisp.ID {
			t.Errorf("expected id=%s, got %v", wisp.ID, m["id"])
		}
		if eph, ok := m["ephemeral"].(bool); ok && eph {
			t.Error("expected promoted JSON not ephemeral")
		}
		if m["wisp_type"] != "heartbeat" {
			t.Errorf("expected wisp_type=heartbeat in JSON, got %v", m["wisp_type"])
		}
		labels, _ := m["labels"].([]interface{})
		foundLabel := false
		for _, l := range labels {
			if l == "shaped" {
				foundLabel = true
			}
		}
		if !foundLabel {
			t.Errorf("expected label 'shaped' in promote --json output, got %v", m["labels"])
		}
	})

	t.Run("one_way_purge_no_longer_reclaims", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pmp")
		promoted := bdProxiedCreate(t, bd, p.dir, "Promoted survives purge", "--ephemeral")
		control := bdProxiedCreate(t, bd, p.dir, "Control wisp purged", "--ephemeral")

		if out, err := bdProxiedRun(t, bd, p.dir, "promote", promoted.ID); err != nil {
			t.Fatalf("bd promote: %v\n%s", err, out)
		}
		for _, id := range []string{promoted.ID, control.ID} {
			if out, err := bdProxiedRun(t, bd, p.dir, "close", id); err != nil {
				t.Fatalf("bd close %s: %v\n%s", id, err, out)
			}
		}

		if out, err := bdProxiedRun(t, bd, p.dir, "purge", "--force"); err != nil {
			t.Fatalf("bd purge --force: %v\n%s", err, out)
		}

		db := openProxiedDB(t, p)
		if n := countRows(t, db, "SELECT COUNT(*) FROM wisps WHERE id = ?", control.ID); n != 0 {
			t.Errorf("expected control wisp %s purged (proves purge ran), got %d", control.ID, n)
		}
		if n := countRows(t, db, "SELECT COUNT(*) FROM issues WHERE id = ?", promoted.ID); n != 1 {
			t.Errorf("expected promoted %s to survive purge, got %d rows", promoted.ID, n)
		}
	})

	t.Run("non_wisp_and_missing_id_fail", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pmf")
		durable := bdProxiedCreate(t, bd, p.dir, "Already permanent", "--type", "task")

		out, err := bdProxiedRun(t, bd, p.dir, "promote", durable.ID)
		if err == nil {
			t.Fatalf("expected promote of non-wisp to fail, got:\n%s", out)
		}
		if !strings.Contains(string(out), "is not a wisp (already persistent)") {
			t.Errorf("expected classic non-wisp error text, got: %s", out)
		}

		out, err = bdProxiedRun(t, bd, p.dir, "promote", "pmf-nope999")
		if err == nil {
			t.Fatalf("expected promote of missing id to fail, got:\n%s", out)
		}
		if !strings.Contains(string(out), "no issue found matching") {
			t.Errorf("expected classic resolve-failure error, got: %s", out)
		}

		// A promoted bead cannot be promoted twice (it is no longer a wisp).
		wisp := bdProxiedCreate(t, bd, p.dir, "Promote twice", "--ephemeral")
		if out, err := bdProxiedRun(t, bd, p.dir, "promote", wisp.ID); err != nil {
			t.Fatalf("bd promote: %v\n%s", err, out)
		}
		out, err = bdProxiedRun(t, bd, p.dir, "promote", wisp.ID)
		if err == nil {
			t.Fatalf("expected second promote to fail, got:\n%s", out)
		}
		if !strings.Contains(string(out), "is not a wisp (already persistent)") {
			t.Errorf("expected classic already-persistent error text, got: %s", out)
		}
	})
}

func countRows(t *testing.T, db *sql.DB, query string, args ...interface{}) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count query %q: %v", query, err)
	}
	return n
}
