package bench_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/sqlbuild"
)

// planeConfig bundles what fixture and test code need to drive one plane
// (issues or wisps) through the shared sqlbuild table-name parameterization,
// so seeding and assertions can iterate over both planes generically instead
// of duplicating logic per plane.
type planeConfig struct {
	name            string
	tables          sqlbuild.FilterTables
	depTargetColumn string // "depends_on_issue_id" or "depends_on_wisp_id"
}

var issuesPlane = planeConfig{
	name:            "issues",
	tables:          sqlbuild.IssuesFilterTables,
	depTargetColumn: "depends_on_issue_id",
}

var wispsPlane = planeConfig{
	name:            "wisps",
	tables:          sqlbuild.WispsFilterTables,
	depTargetColumn: "depends_on_wisp_id",
}

const (
	// seedBackgroundCount is the size of the large background corpus each
	// plane is seeded with — big enough that a narrow WHERE clause scanning
	// it is representative of the production hot path this harness targets
	// (see counts.go's driver-filter doc comment).
	seedBackgroundCount = 5000
	// seedLabelsPerRow gives seedBackgroundCount*seedLabelsPerRow = 20000
	// label rows across the corpus.
	seedLabelsPerRow = 4
	// seedNarrowCount is the size of the tagged subset a realistic
	// assignee-scoped WHERE clause isolates out of seedBackgroundCount rows —
	// the "narrow ephemeral-work search" shape SearchCountsSQL's predicate
	// form exists for.
	seedNarrowCount    = 50
	seedNarrowAssignee = "bench-narrow-target"

	seedInsertBatch = sqlbuild.QueryBatchSize
)

// seedBaseline is a fixed instant (not time.Now()) so every seeded row has a
// byte-identical timestamp across runs, keeping the corpus fully
// deterministic.
var seedBaseline = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// seedRowIDs returns the deterministic ID list for plane's background
// corpus. The last seedNarrowCount IDs are the ones seedPlaneCorpus tags
// with seedNarrowAssignee.
func seedRowIDs(plane planeConfig) []string {
	ids := make([]string, seedBackgroundCount)
	for i := range ids {
		ids[i] = fmt.Sprintf("bench-%s-%05d", plane.name, i)
	}
	return ids
}

// narrowIDs returns the subset of ids tagged with seedNarrowAssignee.
func narrowIDs(ids []string) []string {
	return ids[len(ids)-seedNarrowCount:]
}

// seedPlaneCorpus deterministically seeds plane's main/labels/dependencies
// tables: seedBackgroundCount rows with seedLabelsPerRow labels each, plus
// the trailing seedNarrowCount rows tagged with seedNarrowAssignee and given
// a dependency on an earlier background row. No randomness — every field is
// a function of the row index, so repeated runs produce an identical corpus.
// Comment counts are intentionally left at zero (comments are not seeded);
// the shapes under test do not depend on comment content, only its count
// column being present and comparable.
func seedPlaneCorpus(ctx context.Context, t *testing.T, db *sql.DB, plane planeConfig) []string {
	t.Helper()
	ids := seedRowIDs(plane)
	insertMainRows(ctx, t, db, plane, ids)
	insertLabelRows(ctx, t, db, plane, ids)
	insertDependencyRows(ctx, t, db, plane, ids)
	return ids
}

func insertMainRows(ctx context.Context, t *testing.T, db *sql.DB, plane planeConfig, ids []string) {
	t.Helper()
	narrowStart := len(ids) - seedNarrowCount

	for start := 0; start < len(ids); start += seedInsertBatch {
		end := start + seedInsertBatch
		if end > len(ids) {
			end = len(ids)
		}
		placeholders := make([]string, 0, end-start)
		args := make([]any, 0, (end-start)*8)
		for i := start; i < end; i++ {
			assignee := ""
			if i >= narrowStart {
				assignee = seedNarrowAssignee
			}
			placeholders = append(placeholders, "(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
			args = append(args,
				ids[i],          // id
				"bench "+ids[i], // title
				"",              // description
				"",              // design
				"",              // acceptance_criteria
				"",              // notes
				"open",          // status
				2,               // priority
				"task",          // issue_type
				assignee,        // assignee
				seedBaseline,    // created_at
				seedBaseline,    // updated_at
			)
		}
		query := fmt.Sprintf(
			"INSERT INTO %s (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, assignee, created_at, updated_at) VALUES %s "+
				"ON DUPLICATE KEY UPDATE title = VALUES(title)",
			plane.tables.Main, strings.Join(placeholders, ","),
		)
		_, err := db.ExecContext(ctx, query, args...)
		require.NoError(t, err, "seed %s rows [%d:%d)", plane.tables.Main, start, end)
	}
}

func insertLabelRows(ctx context.Context, t *testing.T, db *sql.DB, plane planeConfig, ids []string) {
	t.Helper()
	type labelRow struct {
		issueID, label string
	}
	var rows []labelRow
	for i, id := range ids {
		for k := 0; k < seedLabelsPerRow; k++ {
			rows = append(rows, labelRow{issueID: id, label: fmt.Sprintf("bench-label-%03d", (i*7+k)%50)})
		}
	}

	for start := 0; start < len(rows); start += seedInsertBatch {
		end := start + seedInsertBatch
		if end > len(rows) {
			end = len(rows)
		}
		placeholders := make([]string, 0, end-start)
		args := make([]any, 0, (end-start)*2)
		for _, r := range rows[start:end] {
			placeholders = append(placeholders, "(?, ?)")
			args = append(args, r.issueID, r.label)
		}
		query := fmt.Sprintf(
			"INSERT INTO %s (issue_id, label) VALUES %s ON DUPLICATE KEY UPDATE label = label",
			plane.tables.Labels, strings.Join(placeholders, ","),
		)
		_, err := db.ExecContext(ctx, query, args...)
		require.NoError(t, err, "seed %s rows [%d:%d)", plane.tables.Labels, start, end)
	}
}

// insertDependencyRows gives each narrow-tagged row a single dependency on
// an earlier background row, so dep_count/rdep_count/deps_json are
// meaningfully non-zero for the rows the narrow WHERE clause isolates
// (and rdep_count is non-zero for whichever background rows they land on).
func insertDependencyRows(ctx context.Context, t *testing.T, db *sql.DB, plane planeConfig, ids []string) {
	t.Helper()
	background := ids[:len(ids)-seedNarrowCount]
	narrow := narrowIDs(ids)

	placeholders := make([]string, 0, len(narrow))
	args := make([]any, 0, len(narrow)*7)
	for i, id := range narrow {
		target := background[i%len(background)]
		placeholders = append(placeholders, "(?, ?, ?, ?, ?, ?, ?)")
		args = append(args, depid.New(id, target), id, target, "blocks", "bench", seedBaseline, "{}")
	}
	query := fmt.Sprintf(
		"INSERT INTO %s (id, issue_id, %s, type, created_by, created_at, metadata) VALUES %s",
		plane.tables.Dependencies, plane.depTargetColumn, strings.Join(placeholders, ","),
	)
	_, err := db.ExecContext(ctx, query, args...)
	require.NoError(t, err, "seed %s rows", plane.tables.Dependencies)
}
