package db

import (
	"database/sql/driver"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

// These tests pin the proxied counted search's single-plane routes (SkipWisps,
// Ephemeral-only, an empty wisps plane) to GH#4370's shape: select the plane's
// ordered ID window, then hydrate exactly those IDs through the by-IDs counts
// form. sqlmock runs in ordered mode and fails any statement it has no
// expectation for, so the predicate-form mega-query — whose aggregates read
// whole side tables — cannot run without failing the test. Every fixture uses
// Lite hydration so the hydrated rows stay small.

// countsLiteColumns is the lite counts-row shape issueops.ScanReadyWorkRowWithCounts
// reads: ScanIssueLiteFrom's columns (keep in lockstep with its dests slice)
// followed by the six aggregate columns.
func countsLiteColumns() []string {
	return []string{
		"id", "content_hash", "title",
		"status",
		"priority", "issue_type", "assignee", "estimated_minutes",
		"created_at", "created_by", "owner", "updated_at", "started_at", "closed_at", "external_ref", "spec_id",
		"compaction_level", "compacted_at", "compacted_at_commit", "original_size", "source_repo", "close_reason", "closed_by_session",
		"sender", "ephemeral", "no_history", "wisp_type", "pinned", "is_template",
		"await_type", "await_id", "timeout_ns",
		"mol_type",
		"event_kind", "actor", "target",
		"due_at", "defer_until",
		"work_type", "source_system", "metadata", "row_lock", "storage_class", "arrival_seq",
		"lease_expires_at", "heartbeat_at", "granted_node",
		"labels_json", "dep_count", "rdep_count", "comment_count", "parent_id", "deps_json",
	}
}

// countsLiteRowsReversed answers a hydration batch in REVERSE order, so a
// caller that does not restore the window's order is caught.
func countsLiteRowsReversed(ids []string) *sqlmock.Rows {
	rows := sqlmock.NewRows(countsLiteColumns())
	created := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	for i := len(ids) - 1; i >= 0; i-- {
		rows.AddRow(
			ids[i], nil, ids[i], // id, content_hash, title
			"open",              // status
			2, "task", nil, nil, // priority, issue_type, assignee, estimated_minutes
			created, nil, nil, nil, nil, nil, nil, nil, // created_at … spec_id
			0, nil, nil, nil, nil, nil, nil, // compaction_level … closed_by_session
			nil, nil, nil, nil, nil, nil, // sender … is_template
			nil, nil, nil, // await_type, await_id, timeout_ns
			nil,           // mol_type
			nil, nil, nil, // event_kind, actor, target
			nil, nil, // due_at, defer_until
			nil, nil, nil, 0, nil, nil, // work_type … arrival_seq
			nil, nil, nil, // lease_expires_at, heartbeat_at, granted_node
			nil, 0, 0, 0, nil, nil, // labels_json, dep/rdep/comment counts, parent_id, deps_json
		)
	}
	return rows
}

// expectCountsByIDs registers the exact by-IDs statement and arguments
// SearchCountsSQL renders for one batch.
func expectCountsByIDs(mock sqlmock.Sqlmock, tables filterTables, batch []string, includeWispReverseDeps bool) {
	query, args := sqlbuild.SearchCountsSQL(tables, batch, "", "", "", includeWispReverseDeps, sqlbuild.CountsHydration{Lite: true})
	want := make([]driver.Value, len(args))
	for i, a := range args {
		want[i] = a
	}
	mock.ExpectQuery("^" + regexp.QuoteMeta(strings.TrimSpace(query)) + "$").
		WithArgs(want...).
		WillReturnRows(countsLiteRowsReversed(batch))
}

func countsIDRows(ids ...string) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"id"})
	for _, id := range ids {
		rows.AddRow(id)
	}
	return rows
}

func countsSeqIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("bd-%04d", i)
	}
	return ids
}

func expectPresent(mock sqlmock.Sqlmock, table string) {
	mock.ExpectQuery(`SELECT 1 FROM ` + table + ` LIMIT 1`).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
}

// TestSearchCountsSinglePlaneSelectsTheWindowBeforeHydrating (AC-5): every
// single-plane route of the proxied counted search runs the plane's ordered,
// window-bound id scan first and hydrates exactly the IDs the window kept —
// in the window's order, with the cap, offset and has-more verdict applied
// once on the IDs — and never runs the predicate-form counts query.
func TestSearchCountsSinglePlaneSelectsTheWindowBeforeHydrating(t *testing.T) {
	const order = `ORDER BY %[1]s\.priority ASC, %[1]s\.created_at DESC, %[1]s\.id ASC`
	yes := true

	for _, tc := range []struct {
		name    string
		filter  types.IssueFilter
		probes  func(mock sqlmock.Sqlmock)
		tables  filterTables
		wispRev bool
		window  string   // the id scan's LIMIT/OFFSET tail
		scanned []string // what the id scan returns
		page    []string // what the window keeps, and the only IDs hydrated
		hasMore bool
	}{
		{
			name:   "SkipWisps page",
			filter: types.IssueFilter{SkipWisps: true, Lite: true, Limit: 3},
			probes: func(mock sqlmock.Sqlmock) { expectPresent(mock, "wisp_dependencies") },
			tables: issuesFilterTables, wispRev: true,
			window:  `LIMIT 4`,
			scanned: []string{"bd-a", "bd-b", "bd-c", "bd-d"},
			page:    []string{"bd-a", "bd-b", "bd-c"},
			hasMore: true,
		},
		{
			name:   "SkipWisps offset under a cap is skipped once",
			filter: types.IssueFilter{SkipWisps: true, Lite: true, Limit: 2, Offset: 2, MaxRows: 10},
			probes: func(mock sqlmock.Sqlmock) { expectPresent(mock, "wisp_dependencies") },
			tables: issuesFilterTables, wispRev: true,
			window:  `LIMIT 5`,
			scanned: []string{"bd-a", "bd-b", "bd-c", "bd-d", "bd-e"},
			page:    []string{"bd-c", "bd-d"},
			hasMore: true,
		},
		{
			name:   "empty wisps plane",
			filter: types.IssueFilter{Lite: true, Limit: 2},
			probes: func(mock sqlmock.Sqlmock) {
				expectPresent(mock, "wisp_dependencies")
				mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).WillReturnRows(sqlmock.NewRows([]string{"1"}))
			},
			tables: issuesFilterTables, wispRev: true,
			window:  `LIMIT 3`,
			scanned: []string{"bd-a", "bd-b"},
			page:    []string{"bd-a", "bd-b"},
		},
		{
			name:   "Ephemeral-only",
			filter: types.IssueFilter{Ephemeral: &yes, Lite: true, Limit: 2},
			probes: func(mock sqlmock.Sqlmock) {
				expectPresent(mock, "wisp_dependencies")
				expectPresent(mock, "wisps")
			},
			tables: wispsFilterTables, wispRev: true,
			window:  `LIMIT 3`,
			scanned: []string{"bd-w1", "bd-w2", "bd-w3"},
			page:    []string{"bd-w1", "bd-w2"},
			hasMore: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock, repo := newMockRepo(t)
			tc.probes(mock)
			idScan := `(?s)^SELECT ` + regexp.QuoteMeta(tc.tables.Main) + `\.id FROM ` + regexp.QuoteMeta(tc.tables.Main) +
				` .*` + fmt.Sprintf(order, tc.tables.Main) + `\s+` + tc.window + `\s*$`
			mock.ExpectQuery(idScan).WillReturnRows(countsIDRows(tc.scanned...))
			expectCountsByIDs(mock, tc.tables, tc.page, tc.wispRev)

			got, err := repo.searchAcrossIssuesAndWispsWithCounts(t.Context(), "", tc.filter)
			if err != nil {
				t.Fatalf("searchAcrossIssuesAndWispsWithCounts: %v", err)
			}
			assertCountsPage(t, got, tc.page, tc.hasMore)
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet SQL expectations: %v", err)
			}
		})
	}

	t.Run("empty window hydrates nothing", func(t *testing.T) {
		mock, repo := newMockRepo(t)
		expectPresent(mock, "wisp_dependencies")
		mock.ExpectQuery(`(?s)^SELECT issues\.id FROM issues .*LIMIT 4\s*$`).WillReturnRows(countsIDRows())

		got, err := repo.searchAcrossIssuesAndWispsWithCounts(t.Context(), "", types.IssueFilter{SkipWisps: true, Lite: true, Limit: 3})
		if err != nil {
			t.Fatalf("searchAcrossIssuesAndWispsWithCounts: %v", err)
		}
		assertCountsPage(t, got, nil, false)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})

	t.Run("unlimited window hydrates in batches", func(t *testing.T) {
		ids := countsSeqIDs(205)
		mock, repo := newMockRepo(t)
		expectPresent(mock, "wisp_dependencies")
		mock.ExpectQuery(`(?s)^SELECT issues\.id FROM issues .*` + fmt.Sprintf(order, "issues") + `\s*$`).
			WillReturnRows(countsIDRows(ids...))
		expectCountsByIDs(mock, issuesFilterTables, ids[:sqlbuild.QueryBatchSize], true)
		expectCountsByIDs(mock, issuesFilterTables, ids[sqlbuild.QueryBatchSize:], true)

		got, err := repo.searchAcrossIssuesAndWispsWithCounts(t.Context(), "", types.IssueFilter{SkipWisps: true, Lite: true})
		if err != nil {
			t.Fatalf("searchAcrossIssuesAndWispsWithCounts: %v", err)
		}
		assertCountsPage(t, got, ids, false)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
	})
}

func assertCountsPage(t *testing.T, got domain.SearchCountsPage, want []string, hasMore bool) {
	t.Helper()
	ids := iwcIDs(got)
	if len(ids) != len(want) {
		t.Fatalf("got %d rows %v, want %d", len(ids), ids, len(want))
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("row %d = %q, want %q (page order not restored from the ID window)", i, ids[i], want[i])
		}
	}
	if got.HasMore != hasMore {
		t.Fatalf("HasMore = %v, want %v", got.HasMore, hasMore)
	}
}
