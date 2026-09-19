package issueops

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
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
