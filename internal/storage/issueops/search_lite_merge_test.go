package issueops

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/types"
)

// liteColumnNames mirrors ScanIssueLiteFrom's dests order exactly (scan.go).
// A mismatch here silently misaligns every column, not just the ones this
// test inspects, so keep it in lockstep with that function's dests slice.
func liteColumnNames() []string {
	return []string{
		"id", "content_hash", "title",
		"status",
		"priority", "issue_type", "assignee", "estimated_minutes",
		"created_at", "created_by", "owner", "updated_at", "started_at", "closed_at", "external_ref", "spec_id",
		"compaction_level", "compacted_at", "compacted_at_commit", "original_size", "source_repo", "close_reason",
		"sender", "ephemeral", "no_history", "wisp_type", "pinned", "is_template",
		"await_type", "await_id", "timeout_ns",
		"mol_type",
		"event_kind", "actor", "target",
		"due_at", "defer_until",
		"work_type", "source_system", "metadata", "row_lock", "storage_class",
		"lease_expires_at", "heartbeat_at", "granted_node",
	}
}

// liteMergeRow builds one full-width IssueSelectColumnsLite row with only
// identity/status/priority/created_at populated; every other column is
// NULL. Column count and order must track liteColumnNames/ScanIssueLiteFrom.
func liteMergeRow(id string, createdAt time.Time) []driver.Value {
	return []driver.Value{
		id, nil, id, // id, content_hash, title
		"open",              // status
		2, "task", nil, nil, // priority, issue_type, assignee, estimated_minutes
		createdAt.Format(time.RFC3339), nil, nil, nil, nil, nil, nil, nil, // created_at, created_by, owner, updated_at, started_at, closed_at, external_ref, spec_id
		0, nil, nil, nil, nil, nil, // compaction_level, compacted_at, compacted_at_commit, original_size, source_repo, close_reason
		nil, nil, nil, nil, nil, nil, // sender, ephemeral, no_history, wisp_type, pinned, is_template
		nil, nil, nil, // await_type, await_id, timeout_ns
		nil,           // mol_type
		nil, nil, nil, // event_kind, actor, target
		nil, nil, // due_at, defer_until
		nil, nil, nil, 0, nil, // work_type, source_system, metadata, row_lock, storage_class
		nil, nil, nil, // lease_expires_at, heartbeat_at, granted_node
	}
}

// TestSearchIssuesInTx_Lite_MergesAcrossIssuesAndWisps is the regression test
// for be-yrtwi: issueLiteProjection (search.go) was missing the
// less: sqlbuild.Less comparator that issueProjection carries, so
// sortMergedResults silently no-op'd on the issues+wisps merge branch and
// trimToSearchLimit kept whichever leg came first in concatenation order
// (always the issues leg, since results := append(filtered, wispResults...))
// instead of the true globally-sorted top-N.
//
// Fixture: the issues table's 2 rows are both older than either wisps-table
// row. With the bug, a Limit=2 search over IssueFilter{Lite: true,
// SortBy: "created"} returns the 2 (stale) issues rows every time, because
// they sort first in the concatenation and trim never looks past them. With
// less wired, the true top-2 by created_at DESC — both wisp rows — survive
// instead. NoIDShrink avoids Pattern B's id-then-fetch dance (orthogonal to
// this bug); SkipLabels avoids mocking the per-leg label hydration queries.
func TestSearchIssuesInTx_Lite_MergesAcrossIssuesAndWisps(t *testing.T) {
	t.Parallel()

	day := func(d int) time.Time { return time.Date(2026, 6, d, 0, 0, 0, 0, time.UTC) }

	_, mock, tx := beginMockTx(t)

	// Issues leg: SQL already orders these DESC by created_at (day5, day1) —
	// both older than anything in the wisps leg below.
	mock.ExpectQuery(`(?s)FROM issues.*LIMIT 2`).
		WillReturnRows(sqlmock.NewRows(liteColumnNames()).
			AddRow(liteMergeRow("bd-issue-mid", day(5))...).
			AddRow(liteMergeRow("bd-issue-old", day(1))...))

	mock.ExpectQuery(`SELECT 1 FROM wisps LIMIT 1`).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))

	// Wisps leg: SQL already orders these DESC by created_at (day12, day10) —
	// both newer than either issues-leg row. True global top-2 by created
	// DESC is entirely this leg.
	mock.ExpectQuery(`(?s)FROM wisps.*LIMIT 2`).
		WillReturnRows(sqlmock.NewRows(liteColumnNames()).
			AddRow(liteMergeRow("bd-wisp-newest", day(12))...).
			AddRow(liteMergeRow("bd-wisp-new", day(10))...))

	filter := types.IssueFilter{
		Lite:       true,
		SortBy:     "created",
		Limit:      2,
		NoIDShrink: true,
		SkipLabels: true,
	}

	got, err := SearchIssuesInTx(context.Background(), tx, "", filter)
	if err != nil {
		t.Fatalf("SearchIssuesInTx: %v", err)
	}

	if len(got) != 2 || got[0].ID != "bd-wisp-newest" || got[1].ID != "bd-wisp-new" {
		ids := make([]string, len(got))
		for i, g := range got {
			ids[i] = g.ID
		}
		t.Fatalf("SearchIssuesInTx Lite merge: got %v, want [bd-wisp-newest bd-wisp-new] "+
			"(true created-DESC top-2 across issues+wisps, not issues-leg concatenation order)", ids)
	}

	if !got[0].IsLitePartial {
		t.Error("SearchIssuesInTx with Lite:true: IsLitePartial = false, want true (confirms Lite scan path engaged)")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
