package db

import (
	"database/sql/driver"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scanIssue delegates to issueops.ScanIssueFrom, which scans positionally
// against issueops.IssueSelectColumns. If the domain column list ever drifts
// from the classic one, adjacent same-typed columns would swap silently, so
// the two constants must stay identical (modulo whitespace).
func TestIssueSelectColumns_MatchClassic(t *testing.T) {
	normalize := func(s string) string {
		fields := strings.Split(s, ",")
		for i, f := range fields {
			fields[i] = strings.TrimSpace(f)
		}
		return strings.Join(fields, ",")
	}
	if got, want := normalize(issueSelectColumns), normalize(issueops.IssueSelectColumns); got != want {
		t.Errorf("domain/db issueSelectColumns drifted from issueops.IssueSelectColumns:\n got: %s\nwant: %s", got, want)
	}
}

// The pre-fix hand-rolled scan read created_at/updated_at into sql.NullTime,
// which hard-fails whenever the driver hands timestamps back as text (e.g. a
// connection without parseTime, or a driver that returns DATETIME as string).
// Classic string-parses with format fallbacks; the shared scan must too.
func TestScanIssue_StringTimestamps(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	cols := strings.Split(issueSelectColumns, ",")
	for i := range cols {
		cols[i] = strings.TrimSpace(cols[i])
	}
	require.Len(t, cols, 46)

	row := []driver.Value{
		"bd-test.1", nil, "title", "desc", "", "", "", // id..notes
		"open", 1, "task", nil, nil, // status..estimated_minutes
		"2026-06-12 10:00:00", nil, nil, "2026-06-12T10:00:01Z", nil, nil, nil, nil, // created_at..spec_id
		0, nil, nil, nil, nil, nil, // compaction_level..close_reason
		nil, nil, nil, nil, nil, nil, // sender..is_template
		nil, nil, nil, nil, // await_type..waiters
		nil,                // mol_type
		nil, nil, nil, nil, // event_kind..payload
		nil, nil, // due_at, defer_until
		nil, nil, nil, // work_type, source_system, metadata
	}
	require.Len(t, row, 46)

	mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows(cols).AddRow(row...))

	rows, err := db.Query("SELECT 1")
	require.NoError(t, err)
	defer rows.Close()
	require.True(t, rows.Next())

	issue, err := scanIssue(rows)
	require.NoError(t, err, "string timestamps must scan via the shared classic parse")
	assert.Equal(t, time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC), issue.CreatedAt)
	assert.Equal(t, time.Date(2026, 6, 12, 10, 0, 1, 0, time.UTC), issue.UpdatedAt)
}
