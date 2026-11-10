//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// These tests pin GH#7757 at the settlement layer: two clones comment on the
// same issue, one also edits a field, and the issues row conflicts. The merge
// commit the settle pass writes (what `bd dolt push` sends) must carry the
// peer's cleanly merged rows in other tables (comments, labels, user kv.*
// config) next to the settled issue. They read AS OF 'HEAD' immediately after
// settlement, before any push or is_blocked recompute could commit the
// working set and mask a merge commit that left the rows behind.

// gh7757Divergence describes the peer rows seeded by seedGH7757Divergence.
type gh7757Divergence struct {
	issueID   string
	peer      string
	peerLabel bool // the peer also adds a label and a kv.peer config row
	// declined makes the issues conflict one the LWW resolver declines (both
	// sides retitle with the same updated_at), so only an explicit strategy
	// settles it. Otherwise main changes status and the peer the title, with
	// distinct updated_at values, which the field-level merge settles.
	declined bool
}

// seedGH7757Divergence seeds the issue, commits, branches the peer, and
// diverges: main comments ("comment from A") and edits the issue; the peer
// comments ("comment from B"), edits the issue and, with peerLabel, adds the
// label "from-b" and config kv.peer = "fromB". Leaves main checked out.
func seedGH7757Divergence(t *testing.T, ctx context.Context, conn *sql.Conn, d gh7757Divergence) {
	t.Helper()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec("INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, updated_at) "+
		"VALUES (?, 'shared issue', '', '', '', '', 'open', 2, 'task', '2026-01-01 09:00:00')", d.issueID)
	exec("CALL DOLT_COMMIT('-Am', 'seed shared issue')")
	exec("CALL DOLT_BRANCH(?, 'HEAD')", d.peer)

	exec("INSERT INTO comments (id, issue_id, author, text, created_at) VALUES (UUID(), ?, 'a', 'comment from A', NOW())", d.issueID)
	if d.declined {
		exec("UPDATE issues SET title = 'ours', updated_at = '2026-01-01 10:00:00' WHERE id = ?", d.issueID)
	} else {
		exec("UPDATE issues SET status = 'in_progress', updated_at = '2026-01-01 10:00:00' WHERE id = ?", d.issueID)
	}
	exec("CALL DOLT_COMMIT('-Am', 'clone A comments and edits')")

	exec("CALL DOLT_CHECKOUT(?)", d.peer)
	exec("INSERT INTO comments (id, issue_id, author, text, created_at) VALUES (UUID(), ?, 'b', 'comment from B', NOW())", d.issueID)
	if d.declined {
		exec("UPDATE issues SET title = 'theirs', updated_at = '2026-01-01 10:00:00' WHERE id = ?", d.issueID)
	} else {
		exec("UPDATE issues SET title = 'renamed on B', updated_at = '2026-01-01 11:00:00' WHERE id = ?", d.issueID)
	}
	if d.peerLabel {
		exec("INSERT INTO labels (issue_id, label) VALUES (?, 'from-b')", d.issueID)
		exec("INSERT INTO config (`key`, value) VALUES ('kv.peer', 'fromB')")
	}
	exec("CALL DOLT_COMMIT('-Am', 'clone B comments and edits')")
	exec("CALL DOLT_CHECKOUT('main')")
}

// headComments returns the issue's comment texts in the committed HEAD root.
func headComments(t *testing.T, ctx context.Context, conn *sql.Conn, issueID string) []string {
	t.Helper()
	rows, err := conn.QueryContext(ctx,
		"SELECT text FROM comments AS OF 'HEAD' WHERE issue_id = ? ORDER BY text", issueID)
	if err != nil {
		t.Fatalf("query HEAD comments: %v", err)
	}
	defer rows.Close()
	var texts []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan HEAD comment: %v", err)
		}
		texts = append(texts, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate HEAD comments: %v", err)
	}
	return texts
}

// configValue reads a config row from the given root ("HEAD" or "WORKING");
// ok is false when the row is absent.
func configValue(t *testing.T, ctx context.Context, conn *sql.Conn, root, key string) (value string, ok bool) {
	t.Helper()
	q := "SELECT value FROM config AS OF 'HEAD' WHERE `key` = ?"
	if root == "WORKING" {
		q = "SELECT value FROM config WHERE `key` = ?"
	}
	err := conn.QueryRowContext(ctx, q, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false
	}
	if err != nil {
		t.Fatalf("read %s config %s: %v", root, key, err)
	}
	return value, true
}

// headIssue reads the issue's title and status from HEAD.
func headIssue(t *testing.T, ctx context.Context, conn *sql.Conn, issueID string) (title, status string) {
	t.Helper()
	if err := conn.QueryRowContext(ctx,
		"SELECT title, status FROM issues AS OF 'HEAD' WHERE id = ?", issueID).Scan(&title, &status); err != nil {
		t.Fatalf("read HEAD issue %s: %v", issueID, err)
	}
	return title, status
}

// assertHeadCarriesPeerRows asserts the merge commit holds both clones'
// comments and, when peerLabel, the peer's label and kv.peer row.
func assertHeadCarriesPeerRows(t *testing.T, ctx context.Context, conn *sql.Conn, d gh7757Divergence) {
	t.Helper()
	if got, want := headComments(t, ctx, conn, d.issueID), []string{"comment from A", "comment from B"}; !reflect.DeepEqual(got, want) {
		t.Errorf("HEAD comments = %v, want %v: the merge commit dropped a clone's comment", got, want)
	}
	if !d.peerLabel {
		return
	}
	if got, want := headLabels(t, ctx, conn, d.issueID), []string{"from-b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("HEAD labels = %v, want %v: the merge commit dropped the peer's label", got, want)
	}
	if v, ok := configValue(t, ctx, conn, "HEAD", "kv.peer"); !ok || v != "fromB" {
		t.Errorf("HEAD config kv.peer = %q (present %v), want %q: the merge commit dropped the peer's kv row", v, ok, "fromB")
	}
}

// TestEmbeddedMergeAndSettleCommitsPeerCleanTables (GH#7757, AC-1): an
// LWW-settled issues conflict riding with the peer's comment, label and kv
// insert concludes as a two-parent merge commit holding all of them and the
// field-merged issue, with nothing left unstaged.
func TestEmbeddedMergeAndSettleCommitsPeerCleanTables(t *testing.T) {
	te := newTestEnv(t, "gh7757lww")
	ctx := t.Context()
	conn := openSettleConn(t, ctx, te)

	d := gh7757Divergence{issueID: "pr-1", peer: "prpeer", peerLabel: true}
	seedGH7757Divergence(t, ctx, conn, d)

	if err := versioncontrolops.MergeAndSettle(ctx, conn, d.peer); err != nil {
		t.Fatalf("MergeAndSettle: %v", err)
	}

	assertHeadCarriesPeerRows(t, ctx, conn, d)
	if title, status := headIssue(t, ctx, conn, d.issueID); title != "renamed on B" || status != "in_progress" {
		t.Errorf("HEAD issue = (%q, %q), want (%q, %q): the field-level merge did not settle both edits",
			title, status, "renamed on B", "in_progress")
	}
	assertMergeConcluded(t, ctx, conn)
}
