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
	// peerExec runs on the peer branch just before its commit.
	peerExec []string
}

// seedGH7757Divergence seeds the issue, commits, branches the peer, and
// diverges: main comments ("comment from A") and edits the issue; the peer
// comments ("comment from B"), edits the issue and, with peerLabel, adds the
// label "from-b" and config kv.peer = "fromB", then runs peerExec. Leaves
// main checked out.
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
	for _, q := range d.peerExec {
		exec(q)
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

// TestEmbeddedStrategyMergeCommitsPeerCleanTables (GH#7757, AC-2): an issues
// conflict only an explicit strategy settles, resolved through SettleMerge
// (pull --strategy) or MergeWithStrategy (bd vc merge --strategy), keeps the
// peer's cleanly merged rows in the merge commit and applies the strategy to
// the conflicted title.
func TestEmbeddedStrategyMergeCommitsPeerCleanTables(t *testing.T) {
	for _, route := range []string{"settle", "vcmerge"} {
		for _, strategy := range []string{"ours", "theirs"} {
			t.Run(route+"/"+strategy, func(t *testing.T) {
				te := newTestEnv(t, "gh7757"+route+strategy)
				ctx := t.Context()
				conn := openSettleConn(t, ctx, te)

				d := gh7757Divergence{issueID: "ps-1", peer: "pspeer", peerLabel: true, declined: true}
				seedGH7757Divergence(t, ctx, conn, d)

				switch route {
				case "settle":
					if err := versioncontrolops.MergeAndSettleWithStrategy(ctx, conn, d.peer, strategy); err != nil {
						t.Fatalf("MergeAndSettleWithStrategy(%s): %v", strategy, err)
					}
				case "vcmerge":
					conflicts, err := versioncontrolops.MergeWithStrategy(ctx, conn, d.peer, "bd-test <bd-test@example.com>", strategy)
					if err != nil {
						t.Fatalf("MergeWithStrategy(%s): %v", strategy, err)
					}
					if len(conflicts) == 0 {
						t.Fatal("MergeWithStrategy reported no conflicts; the seed must conflict on the issues row")
					}
				}

				assertHeadCarriesPeerRows(t, ctx, conn, d)
				if title, _ := headIssue(t, ctx, conn, d.issueID); title != strategy {
					t.Errorf("HEAD title = %q, want %q (the %s side)", title, strategy, strategy)
				}
				assertMergeConcluded(t, ctx, conn)
			})
		}
	}
}

// TestEmbeddedMergeAndSettleKeepsLocalPendingConfigAndLabel (GH#7757, AC-4):
// local uncommitted edits to an internal config key and to labels, tables
// both committed branches left identical, stay unstaged with their working
// values through an otherwise conflicted settlement and stay out of the merge
// commit, which still carries the peer's comment.
func TestEmbeddedMergeAndSettleKeepsLocalPendingConfigAndLabel(t *testing.T) {
	te := newTestEnv(t, "gh7757local")
	ctx := t.Context()
	conn := openSettleConn(t, ctx, te)

	d := gh7757Divergence{issueID: "pl-1", peer: "plpeer"}
	seedGH7757Divergence(t, ctx, conn, d)
	for _, q := range []string{
		"INSERT INTO config (`key`, value) VALUES ('local_pending_setting', 'local')",
		"INSERT INTO labels (issue_id, label) VALUES ('pl-1', 'local-pending')",
	} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	if err := versioncontrolops.MergeAndSettle(ctx, conn, d.peer); err != nil {
		t.Fatalf("MergeAndSettle: %v", err)
	}

	assertHeadCarriesPeerRows(t, ctx, conn, d)
	if v, ok := configValue(t, ctx, conn, "HEAD", "local_pending_setting"); ok {
		t.Errorf("merge commit swept in the local pending config row (value %q)", v)
	}
	if v, ok := configValue(t, ctx, conn, "WORKING", "local_pending_setting"); !ok || v != "local" {
		t.Errorf("working config local_pending_setting = %q (present %v), want %q", v, ok, "local")
	}
	if got := headLabels(t, ctx, conn, d.issueID); len(got) != 0 {
		t.Errorf("merge commit swept in the local pending label: HEAD labels = %v", got)
	}
	var workingLabels int
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM labels WHERE issue_id = 'pl-1' AND label = 'local-pending'").Scan(&workingLabels); err != nil {
		t.Fatalf("count working labels: %v", err)
	}
	if workingLabels != 1 {
		t.Errorf("working set lost the local pending label (%d rows)", workingLabels)
	}
	if got, want := doltStatus(t, ctx, conn), map[string]bool{"config": false, "labels": false}; !reflect.DeepEqual(got, want) {
		t.Errorf("dolt_status after settlement = %v, want %v (the local edits pending and unstaged, nothing else)", got, want)
	}
	if mergeOpen(t, ctx, conn) {
		t.Error("merge left open (dolt_merge_status.is_merging = 1) after settlement")
	}
}

// doltStatus maps each dolt_status table name to its staged flag.
func doltStatus(t *testing.T, ctx context.Context, conn *sql.Conn) map[string]bool {
	t.Helper()
	rows, err := conn.QueryContext(ctx, "SELECT table_name, staged FROM dolt_status ORDER BY table_name")
	if err != nil {
		t.Fatalf("read dolt_status: %v", err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var name string
		var staged bool
		if err := rows.Scan(&name, &staged); err != nil {
			t.Fatalf("scan dolt_status: %v", err)
		}
		got[name] = staged
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate dolt_status: %v", err)
	}
	return got
}

// TestEmbeddedMergeAndSettleCommitsTableAnIgnoreOverrideKeeps (GH#7757): a
// table an exact ignored = 0 dolt_ignore row keeps versioned under a broader
// ignored = 1 pattern is merge output like any other. Dolt resolves
// dolt_ignore most specific first, so DOLT_ADD stages comments here; a
// staging pass that skipped every table some ignored = 1 row matches left the
// peer's comment out of the merge commit, and a completeness re-read filtered
// the same way never noticed.
func TestEmbeddedMergeAndSettleCommitsTableAnIgnoreOverrideKeeps(t *testing.T) {
	te := newTestEnv(t, "gh7757override")
	ctx := t.Context()
	conn := openSettleConn(t, ctx, te)

	if _, err := conn.ExecContext(ctx,
		"INSERT INTO dolt_ignore VALUES ('comm%', true), ('comments', false)"); err != nil {
		t.Fatalf("seed dolt_ignore override: %v", err)
	}
	d := gh7757Divergence{issueID: "po-1", peer: "popeer"}
	seedGH7757Divergence(t, ctx, conn, d)

	if err := versioncontrolops.MergeAndSettle(ctx, conn, d.peer); err != nil {
		t.Fatalf("MergeAndSettle: %v", err)
	}

	assertHeadCarriesPeerRows(t, ctx, conn, d)
	assertMergeConcluded(t, ctx, conn)
}

// TestEmbeddedMergeAndSettleLeavesIgnoredTrackedTablesUnstaged (GH#7757): a
// tracked table dolt_ignore ignores, here committed __temp__ residue under
// the canonical __temp__% pattern and a table an operator's later 'scratch*'
// pattern covers, keeps the peer's change out of the merge commit (DOLT_ADD
// without --force skips it, as before GH#7757). The completeness re-read must
// not count it as an incomplete merge: the abort restores the same working
// set, so every retry would refuse the same way. Dolt matches '*' as a
// wildcard where LIKE takes it literally, so scratch_notes pins Dolt's
// pattern matching, not LIKE's.
func TestEmbeddedMergeAndSettleLeavesIgnoredTrackedTablesUnstaged(t *testing.T) {
	te := newTestEnv(t, "gh7757ignored")
	ctx := t.Context()
	conn := openSettleConn(t, ctx, te)

	for _, q := range []string{
		"CREATE TABLE `__temp__probe` (id INT PRIMARY KEY, v VARCHAR(16))",
		"INSERT INTO `__temp__probe` VALUES (1, 'base')",
		"CALL DOLT_ADD('-f', '__temp__probe')",
		"CREATE TABLE scratch_notes (id INT PRIMARY KEY, v VARCHAR(16))",
		"INSERT INTO scratch_notes VALUES (1, 'base')",
		"CALL DOLT_COMMIT('-Am', 'track the tables before an ignore pattern covers them')",
		"INSERT INTO dolt_ignore VALUES ('scratch*', true)",
	} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	d := gh7757Divergence{issueID: "pi-1", peer: "pipeer", peerExec: []string{
		"UPDATE `__temp__probe` SET v = 'peer' WHERE id = 1",
		"CALL DOLT_ADD('-f', '__temp__probe')",
		"UPDATE scratch_notes SET v = 'peer' WHERE id = 1",
		"CALL DOLT_ADD('-f', 'scratch_notes')",
	}}
	seedGH7757Divergence(t, ctx, conn, d)

	if err := versioncontrolops.MergeAndSettle(ctx, conn, d.peer); err != nil {
		t.Fatalf("MergeAndSettle: %v", err)
	}

	assertHeadCarriesPeerRows(t, ctx, conn, d)
	for _, table := range []string{"__temp__probe", "scratch_notes"} {
		var v string
		if err := conn.QueryRowContext(ctx,
			"SELECT v FROM `"+table+"` AS OF 'HEAD' WHERE id = 1").Scan(&v); err != nil {
			t.Fatalf("read HEAD %s: %v", table, err)
		}
		if v != "base" {
			t.Errorf("HEAD %s.v = %q, want %q: the merge commit staged an ignored table", table, v, "base")
		}
	}
	if got, want := doltStatus(t, ctx, conn), map[string]bool{"__temp__probe": false, "scratch_notes": false}; !reflect.DeepEqual(got, want) {
		t.Errorf("dolt_status after settlement = %v, want %v (only the ignored tables' peer changes unstaged)", got, want)
	}
	if mergeOpen(t, ctx, conn) {
		t.Error("merge left open (dolt_merge_status.is_merging = 1) after settlement")
	}
	var parents int
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_commit_ancestors WHERE commit_hash = HASHOF('HEAD')").Scan(&parents); err != nil {
		t.Fatalf("count HEAD parents: %v", err)
	}
	if parents != 2 {
		t.Errorf("HEAD (a concluded merge) has %d parent(s), want 2", parents)
	}
}
