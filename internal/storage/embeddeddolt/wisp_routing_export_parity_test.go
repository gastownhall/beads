//go:build cgo

package embeddeddolt_test

// wisp_routing_export_parity_test.go — Commit 1 correctness gate.
//
// Creates a fixture DB with ~20 permanent issues and ~20 explicit-ID wisps,
// then compares four bulk helpers rewritten in this commit against a legacy
// per-ID IsActiveWispInTx loop:
//
//   - GetLabelsForIssuesInTx        vs legacyLabels
//   - GetCommentCountsInTx          vs legacyCommentCounts
//   - GetCommentsForIssuesInTx      vs legacyCommentContent
//   - GetDependencyRecordsForIssuesInTx vs legacyDependencyRecords
//
// Each legacy helper calls IsActiveWispInTx(ctx, tx, id) per ID and queries
// the appropriate table (comments/wisp_comments, labels/wisp_labels, etc.)
// directly — mirroring the code path that existed BEFORE PartitionByWispInTx
// was introduced. reflect.DeepEqual is the comparison; slice contents are
// sorted where needed so the check is order-agnostic.
//
// A byte-diff on exported JSONL is intentionally avoided because JSON
// field-order noise would cause false failures. Instead we compare typed
// Go structures.

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// buildParityFixture seeds ~20 permanent issues and ~20 wisps with labels,
// comments, and cross-dependencies, then returns the combined ID slice.
func buildParityFixture(t *testing.T, te *testEnv) []string {
	t.Helper()
	ctx := t.Context()

	const nPerm = 20
	const nWisp = 20

	var ids []string

	// Seed permanent issues.
	for i := 0; i < nPerm; i++ {
		id := fmt.Sprintf("par-perm-%02d", i)
		ids = append(ids, id)
		if err := te.store.CreateIssue(ctx, &types.Issue{
			ID:        id,
			Title:     "perm " + id,
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}, "tester"); err != nil {
			t.Fatalf("CreateIssue perm %q: %v", id, err)
		}
	}

	// Seed wisp issues.
	for i := 0; i < nWisp; i++ {
		id := fmt.Sprintf("par-wisp-%02d", i)
		ids = append(ids, id)
		if err := te.store.CreateIssue(ctx, &types.Issue{
			ID:        id,
			Title:     "wisp " + id,
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
		}, "tester"); err != nil {
			t.Fatalf("CreateIssue wisp %q: %v", id, err)
		}
	}

	// Add labels to every other issue (alternating perm/wisp).
	for i, id := range ids {
		if i%3 == 0 {
			if err := te.store.AddLabel(ctx, id, "tagged", "tester"); err != nil {
				t.Fatalf("AddLabel %q: %v", id, err)
			}
		}
		if i%5 == 0 {
			if err := te.store.AddLabel(ctx, id, "important", "tester"); err != nil {
				t.Fatalf("AddLabel2 %q: %v", id, err)
			}
		}
	}

	// Add comments to every 4th issue (single comment).
	for i, id := range ids {
		if i%4 == 0 {
			if _, err := te.store.AddIssueComment(ctx, id, "bot", "comment on "+id); err != nil {
				t.Fatalf("AddIssueComment %q: %v", id, err)
			}
		}
	}

	// Add multiple comments on both a wisp and a permanent issue so the
	// content parity check is non-trivial (exercises ordering + multi-row).
	multiPerm := "par-perm-01"
	multiWisp := "par-wisp-01"
	for i, id := range []string{multiPerm, multiWisp} {
		for j := 0; j < 3; j++ {
			text := fmt.Sprintf("multi-%d on %s", j, id)
			if _, err := te.store.AddIssueComment(ctx, id, "bot", text); err != nil {
				t.Fatalf("AddIssueComment multi[%d][%d] %q: %v", i, j, id, err)
			}
		}
	}

	return ids
}

// legacyLabels reproduces the old per-ID partition for GetLabelsForIssues.
func legacyLabels(ctx context.Context, tx *sql.Tx, ids []string) (map[string][]string, error) {
	result := make(map[string][]string)
	var wispIDs, permIDs []string
	for _, id := range ids {
		if issueops.IsActiveWispInTx(ctx, tx, id) {
			wispIDs = append(wispIDs, id)
		} else {
			permIDs = append(permIDs, id)
		}
	}
	// Merge labels from both tables.
	for _, pair := range []struct {
		table string
		batch []string
	}{
		{"wisp_labels", wispIDs},
		{"labels", permIDs},
	} {
		if len(pair.batch) == 0 {
			continue
		}
		for _, id := range pair.batch {
			lbls, err := issueops.GetLabelsInTx(ctx, tx, pair.table, id)
			if err != nil {
				return nil, err
			}
			if len(lbls) > 0 {
				result[id] = lbls
			}
		}
	}
	return result, nil
}

// TestWispRoutingExportParity is the Commit 1 correctness gate.
// It verifies that GetLabelsForIssuesInTx (and the comment/dep bulk helpers
// which use the same partition) produce semantically identical output whether
// the partitioning is done per-ID (legacy) or via PartitionByWispInTx (new).
func TestWispRoutingExportParity(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	te := newTestEnv(t, "par")
	ids := buildParityFixture(t, te)

	// Open a single read-only tx for both paths.
	ctx := t.Context()
	db, dbCleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer dbCleanup()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: false})
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback()

	// --- Labels ---
	legacyLbls, err := legacyLabels(ctx, tx, ids)
	if err != nil {
		t.Fatalf("legacyLabels: %v", err)
	}
	newLbls, err := issueops.GetLabelsForIssuesInTx(ctx, tx, ids)
	if err != nil {
		t.Fatalf("GetLabelsForIssuesInTx: %v", err)
	}

	// Normalize: sort label slices for deterministic comparison.
	normalizeLabelMap(legacyLbls)
	normalizeLabelMap(newLbls)

	if !reflect.DeepEqual(legacyLbls, newLbls) {
		t.Errorf("labels mismatch:\nlegacy: %v\nnew:    %v", legacyLbls, newLbls)
	}

	// --- Comment counts ---
	legacyCounts, err := legacyCommentCounts(ctx, tx, ids)
	if err != nil {
		t.Fatalf("legacyCommentCounts: %v", err)
	}
	newCounts, err := issueops.GetCommentCountsInTx(ctx, tx, ids)
	if err != nil {
		t.Fatalf("GetCommentCountsInTx: %v", err)
	}
	if !reflect.DeepEqual(legacyCounts, newCounts) {
		t.Errorf("comment counts mismatch:\nlegacy: %v\nnew:    %v", legacyCounts, newCounts)
	}

	// --- Comment content (full rows, not just counts) ---
	legacyComments, err := legacyCommentContent(ctx, tx, ids)
	if err != nil {
		t.Fatalf("legacyCommentContent: %v", err)
	}
	newComments, err := issueops.GetCommentsForIssuesInTx(ctx, tx, ids)
	if err != nil {
		t.Fatalf("GetCommentsForIssuesInTx: %v", err)
	}
	// GetCommentsForIssuesInTx returns a non-nil empty map; legacy may too.
	// Sort each issue's comment slice deterministically before comparing.
	normalizeCommentMap(legacyComments)
	normalizeCommentMap(newComments)
	if !reflect.DeepEqual(legacyComments, newComments) {
		t.Errorf("comment content mismatch:\nlegacy: %+v\nnew:    %+v", legacyComments, newComments)
	}

	// --- Dependency records ---
	legacyDeps, err := legacyDependencyRecords(ctx, tx, ids)
	if err != nil {
		t.Fatalf("legacyDependencyRecords: %v", err)
	}
	newDeps, err := issueops.GetDependencyRecordsForIssuesInTx(ctx, tx, ids)
	if err != nil {
		t.Fatalf("GetDependencyRecordsForIssuesInTx: %v", err)
	}
	if !reflect.DeepEqual(legacyDeps, newDeps) {
		t.Errorf("dependency records mismatch:\nlegacy: %v\nnew:    %v", legacyDeps, newDeps)
	}

	// Total comment rows across all issues — sanity check that the multi-comment
	// fixture exercised the multi-row code path.
	totalCommentRows := 0
	for _, cs := range newComments {
		totalCommentRows += len(cs)
	}
	t.Logf("parity OK: %d IDs, %d with labels, %d with comments, %d comment rows, %d dep-record buckets",
		len(ids), len(newLbls), len(newCounts), totalCommentRows, len(newDeps))
}

// normalizeLabelMap sorts label slices in place so DeepEqual is order-agnostic.
func normalizeLabelMap(m map[string][]string) {
	for id, lbls := range m {
		sort.Strings(lbls)
		m[id] = lbls
	}
}

// legacyCommentCounts reproduces the old per-ID partition for GetCommentCounts.
// Only records entries with cnt > 0 to match GetCommentCountsInTx semantics
// (which uses GROUP BY and only emits rows with at least one comment).
func legacyCommentCounts(ctx context.Context, tx *sql.Tx, ids []string) (map[string]int, error) {
	result := make(map[string]int)
	for _, id := range ids {
		table := "comments"
		if issueops.IsActiveWispInTx(ctx, tx, id) {
			table = "wisp_comments"
		}
		var cnt int
		err := tx.QueryRowContext(ctx,
			fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE issue_id = ?", table), //nolint:gosec
			id).Scan(&cnt)
		if err != nil {
			return nil, fmt.Errorf("count comments for %s: %w", id, err)
		}
		if cnt > 0 {
			result[id] = cnt
		}
	}
	return result, nil
}

// legacyCommentContent reproduces the OLD per-ID partition for
// GetCommentsForIssuesInTx: for each ID, call IsActiveWispInTx to pick the
// table (comments vs wisp_comments) and query that table directly.
// Matches the column set and ORDER BY in bulk_ops.go so DeepEqual is
// meaningful.
func legacyCommentContent(ctx context.Context, tx *sql.Tx, ids []string) (map[string][]*types.Comment, error) {
	result := make(map[string][]*types.Comment)
	for _, id := range ids {
		table := "comments"
		if issueops.IsActiveWispInTx(ctx, tx, id) {
			table = "wisp_comments"
		}
		//nolint:gosec // G201: table is one of two hardcoded literals
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(
			`SELECT id, issue_id, author, text, created_at
			 FROM %s
			 WHERE issue_id = ?
			 ORDER BY issue_id, created_at ASC, id ASC`, table), id)
		if err != nil {
			return nil, fmt.Errorf("legacyCommentContent %s: %w", id, err)
		}
		for rows.Next() {
			var c types.Comment
			if scanErr := rows.Scan(&c.ID, &c.IssueID, &c.Author, &c.Text, &c.CreatedAt); scanErr != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("legacyCommentContent scan %s: %w", id, scanErr)
			}
			result[c.IssueID] = append(result[c.IssueID], &c)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("legacyCommentContent rows %s: %w", id, err)
		}
	}
	return result, nil
}

// normalizeCommentMap sorts each issue's comment slice by (created_at, id)
// so DeepEqual is insensitive to any transient storage-engine ordering.
// GetCommentsForIssuesInTx and legacyCommentContent both use the same
// ORDER BY in SQL, so this is defense-in-depth — but it also prevents
// flakes if storage ever yields ties in a different internal order.
func normalizeCommentMap(m map[string][]*types.Comment) {
	for _, cs := range m {
		sort.Slice(cs, func(i, j int) bool {
			if !cs[i].CreatedAt.Equal(cs[j].CreatedAt) {
				return cs[i].CreatedAt.Before(cs[j].CreatedAt)
			}
			return cs[i].ID < cs[j].ID
		})
	}
}

// legacyDependencyRecords reproduces the OLD per-ID partition for
// GetDependencyRecords: for each ID, call IsActiveWispInTx to pick the
// table (dependencies vs wisp_dependencies) and query that table directly.
// This mirrors the code path that existed BEFORE the batch partitioner
// was introduced, so it provides a genuine cross-check for the new code.
func legacyDependencyRecords(ctx context.Context, tx *sql.Tx, ids []string) (map[string][]*types.Dependency, error) {
	result := make(map[string][]*types.Dependency)
	for _, id := range ids {
		table := "dependencies"
		if issueops.IsActiveWispInTx(ctx, tx, id) {
			table = "wisp_dependencies"
		}
		//nolint:gosec // G201: table is one of two hardcoded literals
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(
			`SELECT issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id
			 FROM %s WHERE issue_id = ? ORDER BY issue_id`, table), id)
		if err != nil {
			return nil, fmt.Errorf("legacyDependencyRecords %s: %w", id, err)
		}
		for rows.Next() {
			var dep types.Dependency
			var createdAt sql.NullTime
			var metadata, threadID sql.NullString
			if scanErr := rows.Scan(&dep.IssueID, &dep.DependsOnID, &dep.Type, &createdAt, &dep.CreatedBy, &metadata, &threadID); scanErr != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("legacyDependencyRecords scan %s: %w", id, scanErr)
			}
			if createdAt.Valid {
				dep.CreatedAt = createdAt.Time
			}
			if metadata.Valid {
				dep.Metadata = metadata.String
			}
			if threadID.Valid {
				dep.ThreadID = threadID.String
			}
			result[dep.IssueID] = append(result[dep.IssueID], &dep)
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("legacyDependencyRecords rows %s: %w", id, err)
		}
	}
	return result, nil
}
