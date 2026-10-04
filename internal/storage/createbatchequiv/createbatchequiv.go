// Package createbatchequiv checks that the batch-create fast paths in
// internal/storage/issueops (the createBatchCache, the dependency pass's batch
// lookups and in-memory graph, and the blocked-state no-edge shortcut) store
// exactly what the per-row bodies store. Each backend's test package runs Run
// against its own engine; the scenario and the comparison live here once.
package createbatchequiv

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// Prefix is the issue prefix every scenario id carries.
const Prefix = "eq"

// Open returns a *sql.DB on a fresh, migrated database whose issue_prefix is
// Prefix. Run calls it twice, once per body.
type Open func(t *testing.T) *sql.DB

// Outcome is everything Run compares between the two bodies.
type Outcome struct {
	Tables  map[string][]string
	Skipped []string
	Changed []string
	Counter []string
}

// Run seeds two fresh databases identically, applies the same import-shaped
// batch to each — one through the fast paths, one with them disabled — and
// fails if the stored outcome differs in any compared table.
func Run(t *testing.T, open Open) {
	t.Helper()
	fast := apply(t, open, false)
	perRow := apply(t, open, true)
	for _, table := range sortedKeys(perRow.Tables) {
		if !reflect.DeepEqual(fast.Tables[table], perRow.Tables[table]) {
			t.Errorf("%s differs between the fast and per-row bodies:\nfast:    %s\nper-row: %s",
				table, strings.Join(fast.Tables[table], "\n         "), strings.Join(perRow.Tables[table], "\n         "))
		}
	}
	if !reflect.DeepEqual(fast.Skipped, perRow.Skipped) {
		t.Errorf("skipped dependencies differ:\nfast:    %v\nper-row: %v", fast.Skipped, perRow.Skipped)
	}
	if !reflect.DeepEqual(fast.Changed, perRow.Changed) {
		t.Errorf("changed tables differ: fast %v, per-row %v", fast.Changed, perRow.Changed)
	}
	if !reflect.DeepEqual(fast.Counter, perRow.Counter) {
		t.Errorf("changed child-counter tables differ: fast %v, per-row %v", fast.Counter, perRow.Counter)
	}
	if len(perRow.Skipped) == 0 || len(perRow.Tables["events"]) == 0 ||
		len(perRow.Tables["bd_events_journal"]) == 0 || len(perRow.Tables["issue_versions"]) == 0 {
		t.Fatalf("scenario exercised nothing: skipped=%d events=%d journal=%d versions=%d", len(perRow.Skipped),
			len(perRow.Tables["events"]), len(perRow.Tables["bd_events_journal"]), len(perRow.Tables["issue_versions"]))
	}
}

func id(s string) string { return Prefix + "-" + s }

var fixedAt = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func issue(suffix, title string, labels ...string) *types.Issue {
	return &types.Issue{
		ID: id(suffix), Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		CreatedAt: fixedAt, UpdatedAt: fixedAt, Labels: labels,
	}
}

func dep(source, target string, t types.DependencyType) *types.Dependency {
	return &types.Dependency{IssueID: source, DependsOnID: target, Type: t, CreatedAt: fixedAt}
}

func seed() []*types.Issue {
	s1 := issue("s1", "seed one", "x")
	s2 := issue("s2", "seed two")
	s2.Dependencies = []*types.Dependency{dep(id("s2"), id("s1"), types.DepBlocks)}
	s3 := issue("s3", "seed closed")
	s3.Status = types.StatusClosed
	parent := issue("p", "parent")
	child := issue("p.1", "child")
	child.Dependencies = []*types.Dependency{dep(id("p.1"), id("p"), types.DepParentChild)}
	w1 := issue("w1", "seed wisp")
	w1.Ephemeral = true
	s4 := issue("s4", "seed with a stale blocked flag")
	return []*types.Issue{s1, s2, s3, parent, child, w1, s4}
}

func batch() []*types.Issue {
	var out []*types.Issue
	const chain = 30
	for i := 1; i <= chain; i++ {
		n := issue(fmt.Sprintf("n%d", i), fmt.Sprintf("new %d", i), "a", "b", "a")
		if i > 1 {
			n.Dependencies = append(n.Dependencies, dep(n.ID, id(fmt.Sprintf("n%d", i-1)), types.DepBlocks))
		}
		out = append(out, n)
	}
	// A closed blocker does not block.
	out[4].Dependencies = append(out[4].Dependencies, dep(out[4].ID, id("s3"), types.DepBlocks))
	// Closing the chain into a cycle: skipped.
	out[0].Dependencies = append(out[0].Dependencies, dep(out[0].ID, id(fmt.Sprintf("n%d", chain)), types.DepBlocks))
	// Imported comment on a new issue.
	out[0].Comments = []*types.Comment{{ID: "eq-comment-1", Author: "alice", Text: "hello", CreatedAt: fixedAt}}

	// Upsert of a seeded issue, adding one label.
	s1 := issue("s1", "seed one again", "x", "y")
	// Re-import of a seeded edge with a different type: the stored row stays.
	s2 := issue("s2", "seed two again")
	s2.Dependencies = []*types.Dependency{dep(id("s2"), id("s1"), types.DepRelated)}
	// A child blocked by its own parent: hierarchy conflict, skipped.
	c := issue("n32", "child blocked by parent")
	c.Dependencies = []*types.Dependency{
		dep(id("n32"), id("p"), types.DepBlocks),
		dep(id("n32"), id("p"), types.DepParentChild),
	}
	c2 := issue("n33", "second child")
	c2.Dependencies = []*types.Dependency{dep(id("n33"), id("p"), types.DepParentChild), dep(id("n33"), id("n32"), types.DepBlocks)}
	missing := issue("n34", "dangling", "z")
	missing.Dependencies = []*types.Dependency{dep(id("n34"), id("missing"), types.DepBlocks)}
	external := issue("n35", "external")
	external.Dependencies = []*types.Dependency{
		dep(id("n35"), "external:other:thing", types.DepBlocks),
		dep(id("n35"), "zz-1", types.DepBlocks),
		dep(id("n35"), id("n1"), types.DepRelated),
	}
	// The same id twice in one batch: the second is an upsert of the first.
	dup := issue("n2", "new 2 again", "c", "a")
	// Wisps, with a wisp->wisp edge and a wisp->issue edge.
	w2 := issue("w2", "batch wisp", "wl")
	w2.Ephemeral = true
	w2.Dependencies = []*types.Dependency{dep(id("w2"), id("w1"), types.DepBlocks), dep(id("w2"), id("n3"), types.DepBlocks)}
	// A generated id.
	gen := &types.Issue{Title: "generated", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeBug,
		CreatedAt: fixedAt, UpdatedAt: fixedAt, Labels: []string{"g"}}
	// A hierarchical child id advances the parent's counter.
	h := issue("p.7", "seventh child")
	h.Dependencies = []*types.Dependency{dep(id("p.7"), id("p"), types.DepParentChild)}
	// An edgeless row whose stored is_blocked is stale: recompute clears it.
	s4 := issue("s4", "seed with a stale blocked flag, again")
	out = append(out, s1, s2, c, c2, missing, external, dup, w2, gen, h, s4)
	return out
}

func apply(t *testing.T, open Open, perRow bool) Outcome {
	t.Helper()
	ctx := context.Background()
	db := open(t)
	opts := storage.BatchCreateOptions{SkipPrefixValidation: true}
	run := func(issues []*types.Issue, opts storage.BatchCreateOptions, journaled bool) issueops.CreateIssuesResult {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		// The batch runs with the events journal and versioned history on,
		// so the comparison covers the order and content of what they record.
		defer issueops.ScopeEventsJournalTransaction(tx, journaled)()
		defer issueops.ScopeVersionedHistoryTransaction(tx, journaled)()
		result, err := issueops.CreateIssuesInTxWithResult(ctx, tx, issues, "importer", opts)
		if err != nil {
			_ = tx.Rollback()
			t.Fatalf("CreateIssuesInTxWithResult: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
		return result
	}
	// Seed through the per-row bodies on both sides, so only the batch below
	// differs.
	restore := issueops.DisableCreateFastPathsForTest()
	run(seed(), opts, false)
	if _, err := db.ExecContext(ctx, "UPDATE issues SET is_blocked = 1 WHERE id = ?", id("s4")); err != nil {
		t.Fatalf("plant stale is_blocked: %v", err)
	}
	if !perRow {
		restore()
	}
	var out Outcome
	opts.SkipDependencyValidationErrors = true
	opts.OnSkippedDependency = func(issueID, dependsOnID, reason string) {
		out.Skipped = append(out.Skipped, issueID+" -> "+dependsOnID+": "+reason)
	}
	result := run(batch(), opts, true)
	if perRow {
		restore()
	}
	out.Changed = sortedKeys(result.ChangedTables)
	out.Counter = sortedKeys(result.ChangedChildCounterTables)
	out.Tables = map[string][]string{}
	for table, query := range map[string]string{
		"issues":            "SELECT id, title, status, priority, is_blocked, content_hash FROM issues",
		"wisps":             "SELECT id, title, status, priority, is_blocked, content_hash FROM wisps",
		"labels":            "SELECT issue_id, label FROM labels",
		"wisp_labels":       "SELECT issue_id, label FROM wisp_labels",
		"dependencies":      "SELECT issue_id, " + issueops.DepTargetExpr + ", type, created_by, metadata FROM dependencies",
		"wisp_dependencies": "SELECT issue_id, " + issueops.DepTargetExpr + ", type, created_by, metadata FROM wisp_dependencies",
		// created_at is wall clock (and so is every id derived from it); the
		// rest of the row, with multiplicity, is the comparable content.
		"events":         "SELECT issue_id, event_type, actor, old_value, new_value, comment FROM events",
		"wisp_events":    "SELECT issue_id, event_type, actor, old_value, new_value, comment FROM wisp_events",
		"comments":       "SELECT id, issue_id, author, text FROM comments",
		"child_counters": "SELECT parent_id, last_child FROM child_counters",
		"issue_versions": "SELECT issue_id, revision, change_actor FROM issue_versions",
	} {
		out.Tables[table] = rowsOf(t, db, query)
	}
	// The journal is an ordered log: compare it in seq order, with each
	// snapshot reduced to the fields a create decides (row_lock and other
	// minted tokens differ run to run by design).
	out.Tables["bd_events_journal"] = journalRows(t, db)
	return out
}

func journalRows(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query("SELECT op, issue_id, actor, issue_json, dep_json, comment_json FROM bd_events_journal ORDER BY seq")
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var op, issueID, actor string
		var issueJSON, depJSON, commentJSON sql.NullString
		if err := rows.Scan(&op, &issueID, &actor, &issueJSON, &depJSON, &commentJSON); err != nil {
			t.Fatalf("read journal: %v", err)
		}
		snapshot := ""
		if issueJSON.Valid {
			var issue types.Issue
			if err := json.Unmarshal([]byte(issueJSON.String), &issue); err != nil {
				t.Fatalf("decode journal snapshot: %v", err)
			}
			snapshot = fmt.Sprintf("%s|%s|%s|blocked=%v|labels=%v", issue.ID, issue.Title, issue.Status, issue.IsBlocked, issue.Labels)
		}
		out = append(out, fmt.Sprintf("%s %s %s %s dep=%s comment=%v", op, issueID, actor, snapshot, depJSON.String, commentJSON.Valid))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read journal: %v", err)
	}
	return out
}

func rowsOf(t *testing.T, db *sql.DB, query string) []string {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	var out []string
	for rows.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		parts := make([]string, len(vals))
		for i, v := range vals {
			if v.Valid {
				parts[i] = fmt.Sprintf("%q", v.String)
			} else {
				parts[i] = "NULL"
			}
		}
		out = append(out, strings.Join(parts, " "))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	sort.Strings(out)
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
