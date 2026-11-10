package issueops

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/types"
)

// The fingerprint's whole job is to be equal when the pending edits are equal
// and different when they are not, so these tests drive the two queries it makes
// — dolt_status, then dolt_diff per dirty table — rather than a live engine. The
// SQL itself runs against a real engine in the dolt/embedded recompute suites.
func newFingerprintMock(t *testing.T) (sqlmock.Sqlmock, DBTX) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet sqlmock expectations: %v", err)
		}
		_ = db.Close()
	})
	return mock, db
}

func expectDirtyStatus(mock sqlmock.Sqlmock, tables ...string) {
	rows := sqlmock.NewRows([]string{"table_name"})
	for _, table := range tables {
		rows.AddRow(table)
	}
	mock.ExpectQuery("dolt_status").WillReturnRows(rows)
}

// expectDiff scripts one dolt_diff result. The columns deliberately include the
// commit metadata the signature must ignore.
func expectDiff(mock sqlmock.Sqlmock, diffRows [][]driver.Value) {
	rows := sqlmock.NewRows([]string{"to_id", "to_title", "from_id", "diff_type", "to_commit", "from_commit"})
	for _, r := range diffRows {
		rows.AddRow(r...)
	}
	mock.ExpectQuery("dolt_diff").WillReturnRows(rows)
}

func TestDirtyGraphFingerprintCleanWorkingSet(t *testing.T) {
	mock, db := newFingerprintMock(t)
	expectDirtyStatus(mock)

	got, err := DirtyGraphFingerprint(context.Background(), db)
	if err != nil {
		t.Fatalf("DirtyGraphFingerprint: %v", err)
	}
	// "" is the contract's "clean", and it must cost no dolt_diff query at all
	// (an unexpected one would fail the mock).
	if got != "" {
		t.Errorf("fingerprint = %q, want empty on a clean working set", got)
	}
}

func TestDirtyGraphFingerprintIsStableAndSensitive(t *testing.T) {
	fingerprint := func(t *testing.T, diff [][]driver.Value) string {
		t.Helper()
		mock, db := newFingerprintMock(t)
		expectDirtyStatus(mock, "issues")
		expectDiff(mock, diff)
		got, err := DirtyGraphFingerprint(context.Background(), db)
		if err != nil {
			t.Fatalf("DirtyGraphFingerprint: %v", err)
		}
		if got == "" {
			t.Fatal("fingerprint is empty for a dirty working set")
		}
		return got
	}

	first := fingerprint(t, [][]driver.Value{
		{"bd-1", "one", nil, "added", nil, "abc"},
		{"bd-2", "two", "bd-2", "modified", nil, "abc"},
	})

	// Same pending edits, reported in a different order and against a different
	// HEAD commit: a neighbour committing elsewhere must not look like THIS
	// working set advancing, or a stuck table would never be detected on a busy
	// server.
	t.Run("stable across row order and HEAD movement", func(t *testing.T) {
		got := fingerprint(t, [][]driver.Value{
			{"bd-2", "two", "bd-2", "modified", nil, "zzz"},
			{"bd-1", "one", nil, "added", nil, "zzz"},
		})
		if got != first {
			t.Errorf("fingerprint changed for identical pending edits:\n %s\n %s", first, got)
		}
	})

	// A genuinely different edit must move it, or a stuck table and a moving one
	// are indistinguishable and a busy fleet gets escalated.
	t.Run("sensitive to a changed row", func(t *testing.T) {
		got := fingerprint(t, [][]driver.Value{
			{"bd-1", "one", nil, "added", nil, "abc"},
			{"bd-2", "two-EDITED", "bd-2", "modified", nil, "abc"},
		})
		if got == first {
			t.Error("fingerprint unchanged after a changed row")
		}
	})

	t.Run("sensitive to a row going away", func(t *testing.T) {
		got := fingerprint(t, [][]driver.Value{{"bd-1", "one", nil, "added", nil, "abc"}})
		if got == first {
			t.Error("fingerprint unchanged after a row went away")
		}
	})
}

// Both dirty tables must contribute, and the table names must be in the token: a
// fingerprint that ignored WHICH table was dirty would call "issues dirty" and
// "dependencies dirty" the same condition.
func TestDirtyGraphFingerprintCoversEveryDirtyTable(t *testing.T) {
	mock, db := newFingerprintMock(t)
	expectDirtyStatus(mock, "dependencies", "issues")
	expectDiff(mock, [][]driver.Value{{"bd-1", "one", nil, "added", nil, "abc"}})
	expectDiff(mock, [][]driver.Value{{"bd-2", "two", nil, "added", nil, "abc"}})

	got, err := DirtyGraphFingerprint(context.Background(), db)
	if err != nil {
		t.Fatalf("DirtyGraphFingerprint: %v", err)
	}
	for _, table := range []string{"issues:", "dependencies:"} {
		if !strings.Contains(got, table) {
			t.Errorf("fingerprint %q does not name %s", got, table)
		}
	}
}

// An engine that cannot answer (no dolt_diff table function, a revoked read)
// must produce an ERROR, never a fingerprint: the caller declines to escalate on
// an error, where a silent "" would be compared as evidence of a clean set.
func TestDirtyGraphFingerprintUnavailableEvidenceErrors(t *testing.T) {
	t.Run("status query fails", func(t *testing.T) {
		mock, db := newFingerprintMock(t)
		mock.ExpectQuery("dolt_status").WillReturnError(errors.New("access denied"))
		if _, err := DirtyGraphFingerprint(context.Background(), db); err == nil {
			t.Fatal("want an error when the dirty set cannot be read")
		}
	})
	t.Run("diff query fails", func(t *testing.T) {
		mock, db := newFingerprintMock(t)
		expectDirtyStatus(mock, "issues")
		mock.ExpectQuery("dolt_diff").WillReturnError(errors.New("function not found: dolt_diff"))
		_, err := DirtyGraphFingerprint(context.Background(), db)
		if err == nil {
			t.Fatal("want an error when the row identities cannot be read")
		}
		if !strings.Contains(err.Error(), "issues") {
			t.Errorf("error %v does not name the table it failed on", err)
		}
	})
}

// The guard and the fingerprint must agree on which tables matter, or the
// fingerprint is evidence about a different question than the one the retry loop
// is asking. The allowlist is also what makes the interpolated table literal in
// dirtyGraphTableSignature safe.
func TestDirtyGraphFingerprintOnlyAllowsGuardedTables(t *testing.T) {
	for _, table := range blockedRecomputeGraphTables {
		if !isBlockedRecomputeGraphTable(table) {
			t.Errorf("guarded table %q rejected by the fingerprint's allowlist", table)
		}
	}
	for _, table := range []string{"wisps", "issues; DROP TABLE issues", "ISSUES", ""} {
		if isBlockedRecomputeGraphTable(table) {
			t.Errorf("fingerprint allowlist accepted %q", table)
		}
	}
	mock, db := newFingerprintMock(t)
	expectDirtyStatus(mock, "wisps")
	if _, err := DirtyGraphFingerprint(context.Background(), db); err == nil {
		t.Fatal("want an error when dolt_status reports a table outside the allowlist")
	}
}

// The guard's own behavior must be unchanged by the extraction the fingerprint
// shares with it: a clean graph passes, a dirty one returns the typed sentinel
// naming every dirty table.
func TestGuardBlockedRecomputeWorkingSet(t *testing.T) {
	t.Run("clean", func(t *testing.T) {
		mock, db := newFingerprintMock(t)
		expectDirtyStatus(mock)
		if err := GuardBlockedRecomputeWorkingSet(context.Background(), db); err != nil {
			t.Fatalf("clean working set rejected: %v", err)
		}
	})
	t.Run("dirty", func(t *testing.T) {
		mock, db := newFingerprintMock(t)
		expectDirtyStatus(mock, "issues", "dependencies")
		err := GuardBlockedRecomputeWorkingSet(context.Background(), db)
		if !errors.Is(err, ErrBlockedRecomputeDirtyGraph) {
			t.Fatalf("err = %v, want the typed dirty-graph sentinel", err)
		}
		// Sorted, so the message is stable for an operator diffing two ticks.
		if !strings.Contains(err.Error(), "dependencies, issues") {
			t.Errorf("err = %v, want both dirty tables named in sorted order", err)
		}
	})
	t.Run("query failure is not a dirty graph", func(t *testing.T) {
		mock, db := newFingerprintMock(t)
		mock.ExpectQuery("dolt_status").WillReturnError(errors.New("access denied"))
		err := GuardBlockedRecomputeWorkingSet(context.Background(), db)
		if err == nil {
			t.Fatal("want an error")
		}
		if errors.Is(err, ErrBlockedRecomputeDirtyGraph) {
			t.Error("a failed status query must not be classified as a retryable dirty graph")
		}
	})
}

// The parent-child legs cascade on a parent's stored bit only while the parent
// is neither closed nor pinned: the merge clause of BlockedStateInvariant
// admits a stale 1 on such a row (gastownhall/beads#7626). The real-engine
// tests cover the behavior; this pins the text on the ci lane, where they skip.
func TestShouldBeBlockedUnionParentLegsGuardStatus(t *testing.T) {
	const guarded = "AND p.is_blocked = 1 AND p.status <> 'closed' AND p.status <> 'pinned'"
	for _, scope := range []string{"", "AND d.issue_id IN (?)"} {
		q := strings.Join(strings.Fields(shouldBeBlockedIDsUnionCoreSQL("dependencies", scope, "", "")), " ")
		if got := strings.Count(q, "p.is_blocked = 1"); got != 2 {
			t.Fatalf("scope %q: %d reads of p.is_blocked, want 2 (one per parent leg)", scope, got)
		}
		if got := strings.Count(q, guarded); got != 2 {
			t.Errorf("scope %q: %d parent legs guard p.status after p.is_blocked = 1, want 2", scope, got)
		}
	}
}

// The close-gate walk meets a parent as blocked only while it is neither
// closed nor pinned, as the parent-child legs do (gastownhall/beads#7626).
func TestBlockedParentEdgesGuardStatus(t *testing.T) {
	const guarded = "AND p.is_blocked = 1 AND p.status <> 'closed' AND p.status <> 'pinned'"
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(
		func(_, actual string) error {
			if !strings.Contains(strings.Join(strings.Fields(actual), " "), guarded) {
				return errors.New("walk does not guard p.status after p.is_blocked = 1")
			}
			return nil
		})))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()
	// One child issue: one lookup per parent kind.
	for range 2 {
		mock.ExpectQuery("").WillReturnRows(sqlmock.NewRows([]string{"issue_id", "id"}))
	}
	if _, err := blockedParentEdgesInTx(context.Background(), db, []parentRef{{id: "c"}}); err != nil {
		t.Fatalf("blockedParentEdgesInTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

// The failure-close predicate is spliced into the batched is_blocked
// templates, whose expander counts every percent sign and question mark
// textually (expandBatchTemplate). The rendered predicate must therefore carry
// none of its own — pinned here on the ci lane beside the budget tests it
// exists to keep — and must cover every types.FailureCloseKeywords entry with
// its single quotes doubled, so a keyword like "won't fix" survives the string
// literal.
func TestFailureClosePredicateSQLContract(t *testing.T) {
	pred := FailureClosePredicateSQL("t.close_reason")
	if strings.ContainsRune(pred, '%') {
		t.Errorf("predicate carries a percent sign of its own: %s", pred)
	}
	if strings.ContainsRune(pred, '?') {
		t.Errorf("predicate carries a question mark of its own: %s", pred)
	}
	for _, keyword := range types.FailureCloseKeywords {
		literal := "'" + strings.ReplaceAll(keyword, "'", "''") + "'"
		if !strings.Contains(pred, literal) {
			t.Errorf("predicate misses keyword %q (as %s): %s", keyword, literal, pred)
		}
	}
	if !strings.Contains(pred, "COALESCE(t.close_reason, '')") {
		t.Errorf("predicate must NULL-guard the reason expression: %s", pred)
	}

	guard := edgeTargetHoldsSQL("d", "t")
	for text, name := range map[string]string{pred: "predicate", guard: "target guard"} {
		if strings.ContainsRune(text, '%') || strings.ContainsRune(text, '?') {
			t.Errorf("union %s carries a percent sign or question mark of its own: %s", name, text)
		}
	}
	if !strings.Contains(guard, "t.status <> 'pinned'") {
		t.Errorf("target guard must release a pinned target: %s", guard)
	}
	if !strings.Contains(guard, "d.type = 'conditional-blocks'") {
		t.Errorf("target guard must split on the conditional-blocks type: %s", guard)
	}
}

// The union's status-tested legs apply the conditional-blocks close rule
// (GH#5577): the leg guard names the target's close_reason and the
// conditional-blocks type, on the ci lane where the dolt suites skip.
func TestShouldBeBlockedUnionAppliesConditionalCloseGuard(t *testing.T) {
	for _, depTable := range []string{"dependencies", "wisp_dependencies"} {
		q := strings.Join(strings.Fields(shouldBeBlockedIDsUnionCoreSQL(depTable, "", "", "")), " ")
		if got := strings.Count(q, "d.type = 'conditional-blocks' AND NOT (INSTR"); got != 2 {
			t.Errorf("%s: %d conditional close guards in the status-tested legs, want 2: %s", depTable, got, q)
		}
		if !strings.Contains(q, "t.close_reason") {
			t.Errorf("%s: status-tested legs must read the target's close_reason: %s", depTable, q)
		}
	}
}
