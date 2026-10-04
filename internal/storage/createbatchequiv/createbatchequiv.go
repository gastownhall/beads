// Package createbatchequiv checks that the batch-create fast paths in
// internal/storage/issueops (the createBatchCache, the dependency pass's batch
// lookups and in-memory graph, and the blocked-state no-edge shortcut) store
// exactly what the per-row bodies store. Each backend's test package runs Run
// against its own engine; the scenario and the comparison live here once.
package createbatchequiv

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// Prefix is the issue prefix every scenario id carries.
const Prefix = "eq"

// Open returns a *sql.DB on a fresh, migrated database whose issue_prefix is
// Prefix. Run calls it twice per scenario, once per body.
type Open func(t *testing.T) *sql.DB

// Outcome is everything Run compares, between the two bodies and against the
// golden digests.
type Outcome struct {
	Tables  map[string][]string
	Skipped []string
	Stale   []string
	Changed []string
	Counter []string
}

// scenario is one seed plus one batch. A batch is either an import-shaped
// CreateIssuesInTxWithResult call (batch) or an apply-batch request (apply).
type scenario struct {
	name      string
	seed      func() []*types.Issue
	afterSeed func(t *testing.T, db *sql.DB)
	batch     func() []*types.Issue
	opts      storage.BatchCreateOptions
	apply     func() publicops.ApplyBatchRequest
}

// scenarios are the light ones Run drives on every lane.
func scenarios() []scenario {
	return []scenario{
		{name: "small", seed: seed, afterSeed: plantStaleBlocked("s4"), batch: batch},
		{name: "apply", seed: seedApply, apply: applyRequest},
	}
}

// largeScenarios are the 458-issue ones RunLarge drives: about 35 s each on
// the embedded engine without the race detector, many times that with it.
func largeScenarios() []scenario {
	return []scenario{
		{name: "import458-reject-stale", seed: seed458, afterSeed: afterSeed458, batch: batch458,
			opts: storage.BatchCreateOptions{RejectStaleUpserts: true}},
		{name: "import458", seed: seed458, afterSeed: afterSeed458, batch: batch458},
	}
}

// GoldenDirEnv names the environment variable that switches Run from
// checking to recording: set to a directory, each scenario's outcome digest
// is written there instead of compared. The committed digests (golden/) were
// recorded from the code BEFORE the batch-create fast paths existed
// (ebe3b6bcb), so the check holds the current code — fast and per-row
// bodies alike, including changes the fast-path switch does not gate — to
// what that code stored.
const GoldenDirEnv = "CREATEBATCHEQUIV_GOLDEN_DIR"

// Run seeds fresh databases, applies each scenario's batch through the fast
// paths and with them disabled, and fails if the two stored outcomes differ in
// any compared table, or if they differ from the scenario's golden digest.
func Run(t *testing.T, open Open) {
	t.Helper()
	runScenarios(t, open, scenarios())
}

// RunLarge is Run for the 458-issue scenarios. A backend runs it from its own
// top-level test so a race-instrumented lane can skip it (see the embedded
// backend's caller) without losing the light scenarios.
func RunLarge(t *testing.T, open Open) {
	t.Helper()
	runScenarios(t, open, largeScenarios())
}

func runScenarios(t *testing.T, open Open, list []scenario) {
	t.Helper()
	for _, sc := range list {
		t.Run(sc.name, func(t *testing.T) {
			if dir := os.Getenv(GoldenDirEnv); dir != "" {
				writeGolden(t, dir, sc.name, applyScenario(t, open, sc, false))
				return
			}
			fast := applyScenario(t, open, sc, false)
			perRow := applyScenario(t, open, sc, true)
			compareOutcomes(t, fast, perRow)
			checkGolden(t, sc.name, fast)
			if sc.apply == nil && (len(perRow.Skipped) == 0 || len(perRow.Tables["events"]) == 0 ||
				len(perRow.Tables["bd_events_journal"]) == 0 || len(perRow.Tables["issue_versions"]) == 0) {
				t.Fatalf("scenario exercised nothing: skipped=%d events=%d journal=%d versions=%d", len(perRow.Skipped),
					len(perRow.Tables["events"]), len(perRow.Tables["bd_events_journal"]), len(perRow.Tables["issue_versions"]))
			}
		})
	}
}

func compareOutcomes(t *testing.T, fast, perRow Outcome) {
	t.Helper()
	for _, table := range sortedKeys(perRow.Tables) {
		if !reflect.DeepEqual(fast.Tables[table], perRow.Tables[table]) {
			t.Errorf("%s differs between the fast and per-row bodies:\nfast:    %s\nper-row: %s",
				table, strings.Join(fast.Tables[table], "\n         "), strings.Join(perRow.Tables[table], "\n         "))
		}
	}
	for _, f := range []struct {
		what         string
		fast, perRow []string
	}{
		{"skipped dependencies", fast.Skipped, perRow.Skipped},
		{"stale rejections", fast.Stale, perRow.Stale},
		{"changed tables", fast.Changed, perRow.Changed},
		{"changed child-counter tables", fast.Counter, perRow.Counter},
	} {
		if !reflect.DeepEqual(f.fast, f.perRow) {
			t.Errorf("%s differ:\nfast:    %v\nper-row: %v", f.what, f.fast, f.perRow)
		}
	}
}

// golden is an Outcome reduced to a row count and a digest per table (the
// full row dumps of the 458-issue scenario run to hundreds of kilobytes); the
// short lists are kept verbatim.
type golden struct {
	Tables  map[string]goldenTable `json:"tables"`
	Skipped []string               `json:"skipped"`
	Stale   []string               `json:"stale"`
	Changed []string               `json:"changed"`
	Counter []string               `json:"counter"`
}

type goldenTable struct {
	Rows   int    `json:"rows"`
	SHA256 string `json:"sha256"`
}

var eventUpdatedAtRe = regexp.MustCompile(`\\"updated_at\\":\\"[^\\]*\\"`)

//go:embed golden/*.json
var goldenFS embed.FS

func digestOf(o Outcome) golden {
	g := golden{Tables: map[string]goldenTable{}, Skipped: o.Skipped, Stale: o.Stale, Changed: o.Changed, Counter: o.Counter}
	for table, rows := range o.Tables {
		sum := sha256.Sum256([]byte(strings.Join(rows, "\n")))
		g.Tables[table] = goldenTable{Rows: len(rows), SHA256: hex.EncodeToString(sum[:])}
	}
	return g
}

func writeGolden(t *testing.T, dir, name string, o Outcome) {
	t.Helper()
	b, err := json.MarshalIndent(digestOf(o), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	// The full dump beside it, for diffing a future mismatch by hand.
	full, _ := json.MarshalIndent(o, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, name+".full.json.txt"), full, 0o600)
}

func checkGolden(t *testing.T, name string, o Outcome) {
	t.Helper()
	b, err := goldenFS.ReadFile("golden/" + name + ".json")
	if err != nil {
		t.Fatalf("no golden digest for scenario %q: record one from the pre-fast-path code with %s (see its doc): %v", name, GoldenDirEnv, err)
	}
	var want golden
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatalf("golden/%s.json: %v", name, err)
	}
	got := digestOf(o)
	norm := func(s []string) []string {
		if len(s) == 0 {
			return nil
		}
		return s
	}
	for _, table := range sortedKeys(want.Tables) {
		if got.Tables[table] != want.Tables[table] {
			t.Errorf("%s: %s differs from the pre-change golden: got %d rows %s, want %d rows %s\n%s",
				name, table, got.Tables[table].Rows, got.Tables[table].SHA256, want.Tables[table].Rows, want.Tables[table].SHA256,
				strings.Join(o.Tables[table], "\n"))
		}
	}
	for _, f := range []struct {
		what      string
		got, want []string
	}{
		{"skipped dependencies", got.Skipped, want.Skipped},
		{"stale rejections", got.Stale, want.Stale},
		{"changed tables", got.Changed, want.Changed},
		{"changed child-counter tables", got.Counter, want.Counter},
	} {
		if !reflect.DeepEqual(norm(f.got), norm(f.want)) {
			t.Errorf("%s: %s differ from the pre-change golden:\ngot:  %v\nwant: %v", name, f.what, f.got, f.want)
		}
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

func plantStaleBlocked(suffix string) func(t *testing.T, db *sql.DB) {
	return func(t *testing.T, db *sql.DB) {
		t.Helper()
		if _, err := db.Exec("UPDATE issues SET is_blocked = 1 WHERE id = ?", id(suffix)); err != nil {
			t.Fatalf("plant stale is_blocked: %v", err)
		}
	}
}

func applyScenario(t *testing.T, open Open, sc scenario, perRow bool) Outcome {
	t.Helper()
	ctx := context.Background()
	db := open(t)
	inTx := func(journaled bool, body func(tx *sql.Tx)) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		// The batch runs with the events journal and versioned history on,
		// so the comparison covers the order and content of what they record.
		clearJournal := issueops.ScopeEventsJournalTransaction(tx, journaled)
		clearVersions := issueops.ScopeVersionedHistoryTransaction(tx, journaled)
		body(tx)
		clearVersions()
		clearJournal()
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}
	// Seed through the per-row bodies on both sides, so only the batch
	// differs.
	restore := issueops.DisableCreateFastPathsForTest()
	inTx(false, func(tx *sql.Tx) {
		if _, err := issueops.CreateIssuesInTxWithResult(ctx, tx, sc.seed(), "importer", storage.BatchCreateOptions{SkipPrefixValidation: true}); err != nil {
			_ = tx.Rollback()
			t.Fatalf("seed: %v", err)
		}
	})
	if sc.afterSeed != nil {
		sc.afterSeed(t, db)
	}
	if !perRow {
		restore()
	}
	var out Outcome
	inTx(true, func(tx *sql.Tx) {
		if sc.apply != nil {
			plan, err := storage.PlanApplyBatch(sc.apply())
			if err != nil {
				t.Fatalf("plan apply batch: %v", err)
			}
			result, write, err := issueops.ApplyBatchInTx(ctx, tx, plan)
			if err != nil {
				_ = tx.Rollback()
				t.Fatalf("ApplyBatchInTx: %v", err)
			}
			out.Changed = sortedKeys(write.Tables)
			for _, item := range result.Items {
				out.Skipped = append(out.Skipped, fmt.Sprintf("%s %s changed=%v", item.Kind, item.IssueID, item.Changed))
			}
			return
		}
		opts := sc.opts
		opts.SkipPrefixValidation = true
		opts.SkipDependencyValidationErrors = true
		opts.OnSkippedDependency = func(issueID, dependsOnID, reason string) {
			out.Skipped = append(out.Skipped, issueID+" -> "+dependsOnID+": "+reason)
		}
		opts.OnStaleRejected = func(issueID string) { out.Stale = append(out.Stale, issueID) }
		result, err := issueops.CreateIssuesInTxWithResult(ctx, tx, sc.batch(), "importer", opts)
		if err != nil {
			_ = tx.Rollback()
			t.Fatalf("CreateIssuesInTxWithResult: %v", err)
		}
		out.Changed = sortedKeys(result.ChangedTables)
		out.Counter = sortedKeys(result.ChangedChildCounterTables)
	})
	if perRow {
		restore()
	}
	out.Tables = map[string][]string{}
	for table, query := range map[string]string{
		"issues": "SELECT id, title, status, priority, is_blocked, content_hash, assignee FROM issues",
		"wisps":  "SELECT id, title, status, priority, is_blocked, content_hash FROM wisps",
		// updated_at is wall clock on rows the batch creates without a
		// timestamp (apply), so only rows the scenarios stamp are compared.
		"issues_updated_at": "SELECT id, DATE_FORMAT(updated_at, '%Y-%m-%d %H:%i:%s') FROM issues WHERE id LIKE 'eq-%' AND updated_at < '2026-06-01'",
		"labels":            "SELECT issue_id, label FROM labels",
		"wisp_labels":       "SELECT issue_id, label FROM wisp_labels",
		"dependencies":      "SELECT id, issue_id, " + issueops.DepTargetExpr + ", type, created_by, metadata FROM dependencies",
		"wisp_dependencies": "SELECT id, issue_id, " + issueops.DepTargetExpr + ", type, created_by, metadata FROM wisp_dependencies",
		// created_at is wall clock (and so is every id derived from it); the
		// rest of the row, with multiplicity, is the comparable content.
		"events":         "SELECT issue_id, event_type, actor, old_value, new_value, comment FROM events",
		"wisp_events":    "SELECT issue_id, event_type, actor, old_value, new_value, comment FROM wisp_events",
		"events_ids":     "SELECT COUNT(*), COUNT(DISTINCT id) FROM events",
		"comments":       "SELECT id, issue_id, author, text FROM comments",
		"child_counters": "SELECT parent_id, last_child FROM child_counters",
		"issue_versions": "SELECT issue_id, revision, change_actor FROM issue_versions",
	} {
		out.Tables[table] = rowsOf(t, db, query)
	}
	// An update event's old_value snapshots the row, wall-clock updated_at
	// included.
	for _, table := range []string{"events", "wisp_events"} {
		for i, row := range out.Tables[table] {
			out.Tables[table][i] = eventUpdatedAtRe.ReplaceAllString(row, `updated_at:*`)
		}
		sort.Strings(out.Tables[table])
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
			snapshot = fmt.Sprintf("%s|%s|%s|blocked=%v|labels=%v|deps=%d", issue.ID, issue.Title, issue.Status, issue.IsBlocked, issue.Labels, len(issue.Dependencies))
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
