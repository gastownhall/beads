package dolt

import (
	"context"
	"database/sql"
	"strconv"
	"testing"
)

// TestProbeDoltDisjointCellMergeRevision locks the Dolt 2.1.8 merge behavior the
// revision-cell resolver (red-team B1.2 #8) is built on: when two branches edit
// DISJOINT columns of one issue row and each stamps its own fresh revision nonce,
// the merge does NOT silently cell-merge — it raises a whole-row conflict. But
// with @autocommit off the conflict persists in dolt_conflicts_issues with full
// per-column base/our/their state, and revision is the ONLY both-sides-diverged
// cell. Consequence: a bare --ours resolve would drop the peer's disjoint edit, so
// the resolver must rebuild the row cell-wise (take the single side that diverged
// per column; a fresh nonce for revision), not resolve --ours. If a future Dolt
// changes this (e.g. auto-cell-merges, or stops exposing the 3-way state), this
// test breaks and the resolver's design must be revisited.
func TestProbeDoltDisjointCellMergeRevision(t *testing.T) {
	skipIfNoDolt(t)
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx := context.Background()

	// Base row on main (via the pool; commits on main).
	makeCASIssue(t, ctx, store, "probe-1")
	base := revOf(t, ctx, store, "probe-1")
	t.Logf("base revision R0 = %d", base)

	// Everything below runs on ONE pinned connection: DOLT_CHECKOUT is
	// session-scoped, so the pool would scatter the branch dance.
	conn, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin conn: %v", err)
	}
	defer conn.Close()

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	scanScalar := func(q string) string {
		var v sql.NullString
		if err := conn.QueryRowContext(ctx, q).Scan(&v); err != nil {
			return "<err: " + err.Error() + ">"
		}
		if !v.Valid {
			return "<null>"
		}
		return v.String
	}

	const nonceA, nonceB = int64(111111111), int64(222222222)

	mustExec("CALL DOLT_BRANCH('bA')")
	mustExec("CALL DOLT_BRANCH('bB')")

	mustExec("CALL DOLT_CHECKOUT('bA')")
	mustExec("UPDATE issues SET title = 'titleA', revision = ? WHERE id = 'probe-1'", nonceA)
	mustExec("CALL DOLT_COMMIT('-Am', 'bA: edit title')")

	mustExec("CALL DOLT_CHECKOUT('bB')")
	mustExec("UPDATE issues SET status = 'in_progress', revision = ? WHERE id = 'probe-1'", nonceB)
	mustExec("CALL DOLT_COMMIT('-Am', 'bB: edit status')")

	mustExec("CALL DOLT_CHECKOUT('main')")
	if _, err := conn.ExecContext(ctx, "CALL DOLT_MERGE('bA')"); err != nil {
		t.Logf("merge bA -> main returned: %v", err)
	}
	t.Logf("after merge bA: title=%q status=%q revision=%s",
		scanScalar("SELECT title FROM issues WHERE id='probe-1'"),
		scanScalar("SELECT status FROM issues WHERE id='probe-1'"),
		scanScalar("SELECT revision FROM issues WHERE id='probe-1'"))

	// Disable autocommit + allow committing with conflicts so the conflict PERSISTS
	// in dolt_conflicts_issues for inspection instead of rolling back.
	mustExec("SET @@autocommit = 0")
	mustExec("SET @@dolt_allow_commit_conflicts = 1")
	if _, mergeErr := conn.ExecContext(ctx, "CALL DOLT_MERGE('bB')"); mergeErr != nil {
		t.Fatalf("with autocommit off + allow_commit_conflicts, the merge should record the conflict rather than error: %v", mergeErr)
	}

	// The disjoint edits produce exactly one issue-row conflict.
	if n := scanScalar("SELECT COUNT(*) FROM dolt_conflicts_issues"); n != "1" {
		t.Fatalf("dolt_conflicts_issues count = %s, want 1", n)
	}

	// The conflict table exposes the full 3-way state per column. This is the
	// mechanism the resolver depends on: title/status each diverged on ONE side
	// (auto-resolvable to that side), and revision is the only both-sides conflict.
	assertConflict := func(col, wantBase, wantOur, wantTheir string) {
		t.Helper()
		get := func(side string) string {
			return scanScalar("SELECT CAST(" + side + "_" + col + " AS CHAR) FROM dolt_conflicts_issues LIMIT 1")
		}
		if b, o, th := get("base"), get("our"), get("their"); b != wantBase || o != wantOur || th != wantTheir {
			t.Errorf("conflict %s: base=%q our=%q their=%q; want base=%q our=%q their=%q",
				col, b, o, th, wantBase, wantOur, wantTheir)
		}
	}
	assertConflict("title", "cas test probe-1", "titleA", "cas test probe-1") // only ours diverged
	assertConflict("status", "open", "open", "in_progress")                   // only theirs diverged
	assertConflict("revision", strconv.FormatInt(base, 10),                   // both diverged: the true conflict
		strconv.FormatInt(nonceA, 10), strconv.FormatInt(nonceB, 10))

	// Whether the nonce ADDS a wedge or is no-worse than status quo depends on
	// updated_at (ON UPDATE CURRENT_TIMESTAMP): if the two branch edits share a
	// second, updated_at ties and revision is the sole conflict; otherwise
	// updated_at already conflicts and the nonce changes nothing.
	uaBase := scanScalar("SELECT CAST(base_updated_at AS CHAR) FROM dolt_conflicts_issues LIMIT 1")
	uaOur := scanScalar("SELECT CAST(our_updated_at AS CHAR) FROM dolt_conflicts_issues LIMIT 1")
	uaTheir := scanScalar("SELECT CAST(their_updated_at AS CHAR) FROM dolt_conflicts_issues LIMIT 1")
	t.Logf("updated_at conflict state: base=%q our=%q their=%q (our==their? %v; if equal, revision is the SOLE conflict cell)",
		uaBase, uaOur, uaTheir, uaOur == uaTheir)

	t.Logf("CONFIRMED: Dolt raises a whole-row conflict but exposes per-column 3-way state; "+
		"revision is the sole both-sides conflict. Resolver must rebuild cell-wise (take the "+
		"diverged side per column, fresh nonce for revision), not bare --ours. "+
		"final ours-side working row: title=%q status=%q revision=%s",
		scanScalar("SELECT title FROM issues WHERE id='probe-1'"),
		scanScalar("SELECT status FROM issues WHERE id='probe-1'"),
		scanScalar("SELECT revision FROM issues WHERE id='probe-1'"))
}
