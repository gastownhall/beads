package issueops

import (
	"context"
	"database/sql/driver"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// flagRow is one row a batched is_blocked read answers with: the id and its
// stored flag.
type flagRow struct {
	id   string
	flag int
}

func flaggedRows(ids []string, flag int) []flagRow {
	rows := make([]flagRow, len(ids))
	for i, id := range ids {
		rows[i] = flagRow{id: id, flag: flag}
	}
	return rows
}

// expectFlagRead pins one batched is_blocked read (readIsBlockedIntoFromTable)
// against table: the IN list over ids, answered by rows or a whole-batch
// error. No rows means the table simply does not hold those ids.
func expectFlagRead(mock sqlmock.Sqlmock, table string, err error, ids []string, rows ...flagRow) {
	placeholders := make([]string, len(ids))
	args := make([]driver.Value, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	expect := mock.ExpectQuery(regexp.QuoteMeta(fmt.Sprintf(
		"SELECT id, is_blocked FROM %s WHERE id IN (%s)", table, strings.Join(placeholders, ",")))).
		WithArgs(args...)
	if err != nil {
		expect.WillReturnError(err)
		return
	}
	result := sqlmock.NewRows([]string{"id", "is_blocked"})
	for _, row := range rows {
		result.AddRow(row.id, row.flag)
	}
	expect.WillReturnRows(result)
}

// expectOwnEdgeProbeError pins the qualification probe against depTable with a
// whole-batch error, the tolerated one being a missing wisp_dependencies.
func expectOwnEdgeProbeError(mock sqlmock.Sqlmock, depTable string, err error, ids []string) {
	placeholders := make([]string, len(ids))
	args := make([]driver.Value, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	mock.ExpectQuery(regexp.QuoteMeta(fmt.Sprintf(
		"SELECT DISTINCT issue_id FROM %s WHERE issue_id IN (%s) AND type IN (%s)",
		depTable, strings.Join(placeholders, ","), readyAffectingTypesSQLList))).
		WithArgs(args...).
		WillReturnError(err)
}

// runEffectiveBlockedRead runs effectiveBlockedBatchInTx against a fresh
// sqlmock database seeded by seed, in a transaction it rolls back at the end.
// The ordered mock fails the run on any statement seed did not name, so a
// case that pins "no probe runs" just seeds no probe.
func runEffectiveBlockedRead(t *testing.T, ids []string, seed func(mock sqlmock.Sqlmock)) (map[string]bool, error) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()
	mock.ExpectBegin()
	seed(mock)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	blocked, readErr := effectiveBlockedBatchInTx(context.Background(), tx, ids)
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if readErr == nil {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sql expectations: %v", err)
		}
	}
	return blocked, readErr
}

// TestEffectiveBlockedBatchInTx exercises the dependency tree's effective
// blocked read directly: the raw stored flags (IsBlockedBatchInTx) minus the
// flags that are stale, set on a row with no own ready-affecting edge
// (sqlbuild.OwnReadyAffectingEdgeIDBatchSQL; GH#3268). The probe's type list —
// which covers blocks, parent-child, conditional-blocks and waits-for, so an
// own parent-child edge qualifies a child exactly like a blocks edge — is
// pinned by the sqlbuild test; the mock sees only the ids it answers with.
func TestEffectiveBlockedBatchInTx(t *testing.T) {
	// A past-the-boundary request: 250 flagged issues, so the flag reads and
	// the probes each split at queryBatchSize (200) into two batches.
	var batchIDs []string
	batchWant := make(map[string]bool, 250)
	for i := 0; i < 250; i++ {
		id := fmt.Sprintf("flagged-%03d", i)
		batchIDs = append(batchIDs, id)
		batchWant[id] = true
	}

	tests := []struct {
		name    string
		ids     []string
		seed    func(mock sqlmock.Sqlmock)
		want    map[string]bool
		wantErr string
	}{
		{
			name: "a stale edgeless issues flag reads false",
			ids:  []string{"stale"},
			seed: func(mock sqlmock.Sqlmock) {
				expectFlagRead(mock, "issues", nil, []string{"stale"}, flagRow{id: "stale", flag: 1})
				expectFlagRead(mock, "wisps", missingTableError("wisps"), []string{"stale"})
				expectOwnEdgeProbe(mock, "dependencies", nil, []string{"stale"})
			},
			want: map[string]bool{"stale": false},
		},
		{
			name: "an own ready-affecting edge keeps the issues flag",
			ids:  []string{"blocked"},
			seed: func(mock sqlmock.Sqlmock) {
				expectFlagRead(mock, "issues", nil, []string{"blocked"}, flagRow{id: "blocked", flag: 1})
				expectFlagRead(mock, "wisps", missingTableError("wisps"), []string{"blocked"})
				expectOwnEdgeProbe(mock, "dependencies", []string{"blocked"}, []string{"blocked"})
			},
			want: map[string]bool{"blocked": true},
		},
		{
			name: "an issues row beats the wisps row and is probed once",
			ids:  []string{"dup"},
			seed: func(mock sqlmock.Sqlmock) {
				expectFlagRead(mock, "issues", nil, []string{"dup"}, flagRow{id: "dup", flag: 1})
				// The wisps row exists but loses the collision; no wisp probe runs.
				expectFlagRead(mock, "wisps", nil, []string{"dup"}, flagRow{id: "dup", flag: 0})
				expectOwnEdgeProbe(mock, "dependencies", []string{"dup"}, []string{"dup"})
			},
			want: map[string]bool{"dup": true},
		},
		{
			name: "an issues flag of false wins over a wisp flag and probes nothing",
			ids:  []string{"dup"},
			seed: func(mock sqlmock.Sqlmock) {
				expectFlagRead(mock, "issues", nil, []string{"dup"}, flagRow{id: "dup", flag: 0})
				expectFlagRead(mock, "wisps", nil, []string{"dup"}, flagRow{id: "dup", flag: 1})
			},
			want: map[string]bool{"dup": false},
		},
		{
			name: "a wisp flag is qualified by its own family's edges",
			ids:  []string{"wisp"},
			seed: func(mock sqlmock.Sqlmock) {
				expectFlagRead(mock, "issues", nil, []string{"wisp"})
				expectFlagRead(mock, "wisps", nil, []string{"wisp"}, flagRow{id: "wisp", flag: 1})
				expectOwnEdgeProbe(mock, "wisp_dependencies", []string{"wisp"}, []string{"wisp"})
			},
			want: map[string]bool{"wisp": true},
		},
		{
			name: "a stale edgeless wisp flag reads false",
			ids:  []string{"wisp"},
			seed: func(mock sqlmock.Sqlmock) {
				expectFlagRead(mock, "issues", nil, []string{"wisp"})
				expectFlagRead(mock, "wisps", nil, []string{"wisp"}, flagRow{id: "wisp", flag: 1})
				expectOwnEdgeProbe(mock, "wisp_dependencies", nil, []string{"wisp"})
			},
			want: map[string]bool{"wisp": false},
		},
		{
			name: "a database without wisp_dependencies keeps the raw wisp flags",
			ids:  []string{"wisp"},
			seed: func(mock sqlmock.Sqlmock) {
				expectFlagRead(mock, "issues", nil, []string{"wisp"})
				expectFlagRead(mock, "wisps", nil, []string{"wisp"}, flagRow{id: "wisp", flag: 1})
				expectOwnEdgeProbeError(mock, "wisp_dependencies", missingTableError("wisp_dependencies"), []string{"wisp"})
			},
			want: map[string]bool{"wisp": true},
		},
		{
			name: "an id absent from both families reads false with no probe",
			ids:  []string{"ghost"},
			seed: func(mock sqlmock.Sqlmock) {
				expectFlagRead(mock, "issues", nil, []string{"ghost"})
				expectFlagRead(mock, "wisps", nil, []string{"ghost"})
			},
			want: map[string]bool{"ghost": false},
		},
		{
			name: "an empty request reads nothing at all",
			ids:  nil,
			seed: func(mock sqlmock.Sqlmock) {},
			want: map[string]bool{},
		},
		{
			name: "an all-false batch probes nothing",
			ids:  []string{"a", "b"},
			seed: func(mock sqlmock.Sqlmock) {
				expectFlagRead(mock, "issues", nil, []string{"a", "b"}, flagRow{id: "a"}, flagRow{id: "b"})
				expectFlagRead(mock, "wisps", nil, []string{"a", "b"})
			},
			want: map[string]bool{"a": false, "b": false},
		},
		{
			name: "the flag reads and the probes batch at queryBatchSize",
			ids:  batchIDs,
			seed: func(mock sqlmock.Sqlmock) {
				expectFlagRead(mock, "issues", nil, batchIDs[:200], flaggedRows(batchIDs[:200], 1)...)
				expectFlagRead(mock, "issues", nil, batchIDs[200:], flaggedRows(batchIDs[200:], 1)...)
				// The wisps read aborts on the first batch's missing table.
				expectFlagRead(mock, "wisps", missingTableError("wisps"), batchIDs[:200])
				expectOwnEdgeProbe(mock, "dependencies", batchIDs[:200], batchIDs[:200])
				expectOwnEdgeProbe(mock, "dependencies", batchIDs[200:], batchIDs[200:])
			},
			want: batchWant,
		},
		{
			name: "an issues read failure propagates",
			ids:  []string{"stale"},
			seed: func(mock sqlmock.Sqlmock) {
				expectFlagRead(mock, "issues", fmt.Errorf("connection lost"), []string{"stale"})
			},
			wantErr: "read is_blocked from issues",
		},
		{
			name: "a wisps failure other than a missing table propagates",
			ids:  []string{"stale"},
			seed: func(mock sqlmock.Sqlmock) {
				expectFlagRead(mock, "issues", nil, []string{"stale"}, flagRow{id: "stale", flag: 1})
				expectFlagRead(mock, "wisps", fmt.Errorf("connection lost"), []string{"stale"})
			},
			wantErr: "read is_blocked from wisps",
		},
		{
			name: "an issues probe failure propagates",
			ids:  []string{"stale"},
			seed: func(mock sqlmock.Sqlmock) {
				expectFlagRead(mock, "issues", nil, []string{"stale"}, flagRow{id: "stale", flag: 1})
				expectFlagRead(mock, "wisps", missingTableError("wisps"), []string{"stale"})
				expectOwnEdgeProbeError(mock, "dependencies", fmt.Errorf("connection lost"), []string{"stale"})
			},
			wantErr: "check own ready-affecting edges in dependencies",
		},
		{
			name: "a wisp probe failure propagates",
			ids:  []string{"wisp"},
			seed: func(mock sqlmock.Sqlmock) {
				expectFlagRead(mock, "issues", nil, []string{"wisp"})
				expectFlagRead(mock, "wisps", nil, []string{"wisp"}, flagRow{id: "wisp", flag: 1})
				expectOwnEdgeProbeError(mock, "wisp_dependencies", fmt.Errorf("connection lost"), []string{"wisp"})
			},
			wantErr: "check own ready-affecting edges in wisp_dependencies",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := runEffectiveBlockedRead(t, tt.ids, tt.seed)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("effectiveBlockedBatchInTx: %v", err)
			}
			// Absent ids read no row, so got may hold fewer keys than want;
			// per-key lookups cover those through the map's false default.
			for id, wantFlag := range tt.want {
				if got[id] != wantFlag {
					t.Fatalf("blocked[%s] = %v, want %v (all: %v)", id, got[id], wantFlag, got)
				}
			}
		})
	}
}

// TestStampTreeBlockedInTxEmptyWalkReadsNothing pins that an empty walk (a
// depth bound of 0, a nonexistent root) reads no flags at all: the ordered
// mock names only the transaction markers, so any read would fail it.
func TestStampTreeBlockedInTxEmptyWalkReadsNothing(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := stampTreeBlockedInTx(context.Background(), tx, "root", nil); err != nil {
		_ = tx.Rollback()
		t.Fatalf("stampTreeBlockedInTx on an empty walk: %v", err)
	}
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}