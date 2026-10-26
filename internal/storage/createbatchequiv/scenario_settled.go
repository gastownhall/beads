package createbatchequiv

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// The settled scenario drives the status changes that hand the blocked-state
// recompute the row they just wrote as settled (a close, an update to closed
// or pinned) over rows whose recompute would otherwise reach that row through
// every leg of the should-be-blocked union: a blocked row closed with its own
// blockers, parent, children, dependents, waiters and a sibling dependent in
// the same recompute; a parent closed over more than one recompute chunk; a
// legacy close-gate epic whose children are explained by its subtree; wisps
// and issues on both ends of cross-plane edges; one id held by both planes;
// updates to pinned, back to open and to closed on both planes; edges added in
// the same transaction just before the close; and a stale parent-child cycle.
//
// Two closes pin when the shortcut clears the row, which it moves out of the
// chunk's unmark statement into a statement of its own. Each closes a blocked
// row above a child that also hangs under a wisp hanging under it, with the
// wisp's flag stale, so whether that cycle stays dark depends only on whether
// the child's unmark sees the closed row still blocked. In I the child shares
// the closed row's chunk, whose union statements saw the row's pre-pass flag,
// so the shortcut must clear the row after them; in J a chunk of dependers
// puts the child in the next chunk, which saw the row cleared, so the shortcut
// must clear it before that chunk.
//
// It records every row's status and is_blocked after each step, and Run
// compares the stored outcome with the shortcut on and off (compare).

// settledBulkChildren is how many children the multi-chunk parent carries:
// with the parent itself, one more than a recompute chunk (queryBatchSize,
// 200), so the parent's close recomputes its children over two chunks.
const settledBulkChildren = 205

// settledChunkDependers is how many rows block on J's closed row: with the
// row itself, exactly one recompute chunk (queryBatchSize, 200), so its child,
// which the recompute's descendant walk lists after them, opens the next one.
const settledChunkDependers = 199

// settledBulkPrefixes are the planted bulk rows the snapshot counts by state
// instead of listing.
var settledBulkPrefixes = []string{"zB-c", "zJ-d"}

func seedSettled() []*types.Issue {
	withDeps := func(is *types.Issue, deps ...*types.Dependency) *types.Issue {
		is.Dependencies = deps
		return is
	}
	wisp := func(is *types.Issue) *types.Issue {
		is.Ephemeral = true
		return is
	}
	waitsFor := func(source, spawner, metadata string) *types.Dependency {
		d := dep(id(source), id(spawner), types.DepWaitsFor)
		d.Metadata = metadata
		return d
	}
	return []*types.Issue{
		// A: a blocked row closed with everything around it.
		withDeps(issue("zA-root", "parent, blocked from outside"), dep(id("zA-root"), id("zA-rt"), types.DepBlocks)),
		issue("zA-rt", "blocker of the parent"),
		issue("zA-t", "blocker of the closed row"),
		withDeps(issue("zA-x", "blocked row that is closed"),
			dep(id("zA-x"), id("zA-t"), types.DepBlocks), dep(id("zA-x"), id("zA-root"), types.DepParentChild)),
		withDeps(issue("zA-sd", "sibling blocked by the closed row"),
			dep(id("zA-sd"), id("zA-root"), types.DepParentChild), dep(id("zA-sd"), id("zA-x"), types.DepBlocks)),
		withDeps(issue("zA-d1", "blocked by the closed row"), dep(id("zA-d1"), id("zA-x"), types.DepBlocks)),
		withDeps(issue("zA-d2", "conditionally blocked by the closed row"), dep(id("zA-d2"), id("zA-x"), types.DepConditionalBlocks)),
		withDeps(issue("zA-k", "child of the closed row"), dep(id("zA-k"), id("zA-x"), types.DepParentChild)),
		withDeps(issue("zA-kg", "grandchild of the closed row"), dep(id("zA-kg"), id("zA-k"), types.DepParentChild)),
		withDeps(issue("zA-w", "waits for the closed row, also blocks"), waitsFor("zA-w", "zA-x", `{"also_blocks":"true"}`)),
		withDeps(issue("zA-w2", "waits for any child of the closed row"), waitsFor("zA-w2", "zA-x", `{"gate":"any-children"}`)),
		// B: a parent closed over two recompute chunks (children planted).
		issue("zB-t", "blocker of the bulk parent"),
		withDeps(issue("zB-p", "bulk parent"), dep(id("zB-p"), id("zB-t"), types.DepBlocks)),
		// C: a legacy close-gate epic (its blocks edges onto its own children
		// are planted).
		issue("zC-e", "close-gate epic"),
		withDeps(issue("zC-k1", "gate child one"), dep(id("zC-k1"), id("zC-e"), types.DepParentChild)),
		withDeps(issue("zC-k2", "gate child two"), dep(id("zC-k2"), id("zC-e"), types.DepParentChild)),
		withDeps(issue("zC-k2a", "grandchild under the gate"), dep(id("zC-k2a"), id("zC-k2"), types.DepParentChild)),
		// D: a wisp closed with dependents in both planes (cross-plane edges
		// are added by the run).
		issue("zD-t", "issue blocker of the wisp"),
		wisp(issue("zD-wx", "wisp that is closed")),
		wisp(withDeps(issue("zD-wd", "wisp blocked by the wisp"), dep(id("zD-wd"), id("zD-wx"), types.DepBlocks))),
		wisp(withDeps(issue("zD-wk", "wisp child of the wisp"), dep(id("zD-wk"), id("zD-wx"), types.DepParentChild))),
		wisp(withDeps(issue("zD-ww", "wisp waiting on the wisp"), waitsFor("zD-ww", "zD-wx", "{}"))),
		issue("zD-id", "issue blocked by the wisp"),
		issue("zD-ik", "issue child of the wisp"),
		// E: one id in both planes (the issues row is planted).
		issue("zE-t", "blocker of the issues-plane twin"),
		wisp(issue("zE-dup", "wisp-plane twin")),
		// F: updates to pinned, open and closed on both planes.
		issue("zF-t", "blocker of the updated issue"),
		withDeps(issue("zF-x", "updated issue"), dep(id("zF-x"), id("zF-t"), types.DepBlocks)),
		withDeps(issue("zF-k", "child of the updated issue"), dep(id("zF-k"), id("zF-x"), types.DepParentChild)),
		withDeps(issue("zF-d", "blocked by the updated issue"), dep(id("zF-d"), id("zF-x"), types.DepBlocks)),
		wisp(issue("zF-wt", "wisp blocker of the updated wisp")),
		wisp(withDeps(issue("zF-wx", "updated wisp"), dep(id("zF-wx"), id("zF-wt"), types.DepBlocks))),
		wisp(withDeps(issue("zF-wd", "wisp blocked by the updated wisp"), dep(id("zF-wd"), id("zF-wx"), types.DepBlocks))),
		issue("zF-id", "issue blocked by the updated wisp"),
		// G: edges added in the run's own transaction, then the close.
		issue("zG-x", "row wired and closed in one transaction"),
		issue("zG-t", "blocker wired in"),
		issue("zG-d", "dependent wired in"),
		issue("zG-c", "child wired in"),
		// H: a stale parent-child cycle and a stale edgeless flag.
		issue("zH-q1", "cycle one"),
		issue("zH-q2", "cycle two"),
		issue("zH-x", "edgeless row with a stale flag"),
		// I: a blocked row closed above a stale cross-plane cycle (the
		// cycle's edges are planted): its child also hangs under a wisp that
		// hangs under the child, and the wisp's flag was never raised.
		withDeps(issue("zI-a", "blocked row closed above the cycle"), dep(id("zI-a"), id("zI-t"), types.DepBlocks)),
		issue("zI-t", "blocker of the row above the cycle"),
		withDeps(issue("zI-b", "child of the closed row, in the cycle"), dep(id("zI-b"), id("zI-a"), types.DepParentChild)),
		wisp(issue("zI-c", "wisp in the cycle")),
		// J: I's shape below a row with a chunk of dependers (planted).
		withDeps(issue("zJ-x", "blocked row with a chunk of dependers"), dep(id("zJ-x"), id("zJ-t"), types.DepBlocks)),
		issue("zJ-t", "blocker of the row with the dependers"),
		withDeps(issue("zJ-c", "child of the row, in the cycle"), dep(id("zJ-c"), id("zJ-x"), types.DepParentChild)),
		wisp(issue("zJ-w", "wisp in the cycle")),
	}
}

// afterSeedSettled plants what no write path creates: the bulk parent's
// children (darkened, as the cascade leaves them), the legacy close-gate
// edges, the issues-plane twin of a wisp id, stale parent-child cycles, a
// stale flag, and J's chunk of dependers. Every planted row carries the
// scenario's fixed timestamps. Then it declares the issues rows
// participating, as stampParticipation does.
func afterSeedSettled(t *testing.T, db *sql.DB) {
	t.Helper()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("plant settled scenario: %v\n%s", err, query)
		}
	}
	planted := func(rows []string) {
		t.Helper()
		values := make([]string, 0, len(rows))
		args := make([]any, 0, 5*len(rows))
		for i := 0; i < len(rows); i += 2 {
			values = append(values, "(?, ?, '', '', '', '', 'open', 2, 'task', ?, ?, ?)")
			blocked := 0
			if rows[i+1] == "blocked" {
				blocked = 1
			}
			args = append(args, rows[i], "planted "+rows[i], fixedAt, fixedAt, blocked)
		}
		exec(`INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, created_at, updated_at, is_blocked)
			VALUES `+strings.Join(values, ", "), args...)
	}
	// dependencies.id is CHAR(36): the planted rows are numbered.
	plantedEdges := 0
	edges := func(rows [][4]string) {
		t.Helper()
		values := make([]string, 0, len(rows))
		args := make([]any, 0, 6*len(rows))
		for _, r := range rows {
			plantedEdges++
			// r: issue_id, target column, target, type.
			issueTarget, wispTarget := any(nil), any(nil)
			if r[1] == "depends_on_wisp_id" {
				wispTarget = r[2]
			} else {
				issueTarget = r[2]
			}
			values = append(values, "(?, ?, ?, ?, ?, 'planted', ?, '{}')")
			args = append(args, fmt.Sprintf("settled-planted-%04d", plantedEdges), r[0], issueTarget, wispTarget, r[3], fixedAt)
		}
		exec(`INSERT INTO dependencies (id, issue_id, depends_on_issue_id, depends_on_wisp_id, type, created_by, created_at, metadata)
			VALUES `+strings.Join(values, ", "), args...)
	}

	var children []string
	var childEdges [][4]string
	for i := 1; i <= settledBulkChildren; i++ {
		child := id(fmt.Sprintf("zB-c%03d", i))
		children = append(children, child, "blocked")
		childEdges = append(childEdges, [4]string{child, "depends_on_issue_id", id("zB-p"), "parent-child"})
	}
	// The last child also has a blocker of its own, so it stays blocked
	// after its parent closes.
	childEdges = append(childEdges, [4]string{id(fmt.Sprintf("zB-c%03d", settledBulkChildren)), "depends_on_issue_id", id("zB-t"), "blocks"})
	planted(children)
	edges(childEdges)

	edges([][4]string{
		// C: the close gate, as legacy data holds it; the epic is blocked
		// only by its own subtree, so it does not darken its children.
		{id("zC-e"), "depends_on_issue_id", id("zC-k1"), "blocks"},
		{id("zC-e"), "depends_on_issue_id", id("zC-k2"), "blocks"},
		// H: a parent-child two-cycle.
		{id("zH-q1"), "depends_on_issue_id", id("zH-q2"), "parent-child"},
		{id("zH-q2"), "depends_on_issue_id", id("zH-q1"), "parent-child"},
	})
	exec("UPDATE issues SET is_blocked = 1, updated_at = updated_at WHERE id = ?", id("zC-e"))
	// H: the cycle holds itself dark, and an edgeless row carries a stale flag.
	exec("UPDATE issues SET is_blocked = 1, updated_at = updated_at WHERE id IN (?, ?, ?)", id("zH-q1"), id("zH-q2"), id("zH-x"))

	// E: the issues-plane twin of the wisp zE-dup, blocked by the wisp and
	// by an open issue of its own.
	planted([]string{id("zE-dup"), "blocked"})
	edges([][4]string{
		{id("zE-dup"), "depends_on_wisp_id", id("zE-dup"), "blocks"},
		{id("zE-dup"), "depends_on_issue_id", id("zE-t"), "blocks"},
	})

	// I and J: each child also hangs under a wisp that hangs under it. The
	// seed gave the wisps no edges, so their flags are still down: a stale
	// cycle that only the child's own flag can hold up.
	edges([][4]string{
		{id("zI-b"), "depends_on_wisp_id", id("zI-c"), "parent-child"},
		{id("zJ-c"), "depends_on_wisp_id", id("zJ-w"), "parent-child"},
	})
	for i, r := range [][2]string{{id("zI-c"), id("zI-b")}, {id("zJ-w"), id("zJ-c")}} {
		exec(`INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, type, created_by, created_at, metadata)
			VALUES (?, ?, ?, 'parent-child', 'planted', ?, '{}')`, fmt.Sprintf("settled-planted-w%03d", i+1), r[0], r[1], fixedAt)
	}
	// J: the dependers that fill the closed row's recompute chunk.
	var dependers []string
	var dependerEdges [][4]string
	for i := 1; i <= settledChunkDependers; i++ {
		depender := id(fmt.Sprintf("zJ-d%03d", i))
		dependers = append(dependers, depender, "blocked")
		dependerEdges = append(dependerEdges, [4]string{depender, "depends_on_issue_id", id("zJ-x"), "blocks"})
	}
	planted(dependers)
	edges(dependerEdges)

	stampParticipation(t, db)
}

func runSettled(ctx context.Context, tx *sql.Tx) ([]string, error) {
	var out []string
	snapshot := func(step string) error {
		var rows []string
		// bulk counts the planted bulk rows: prefix -> "status/is_blocked" -> n.
		bulk := map[string]map[string]int{}
		bulkPrefix := func(rid string) string {
			for _, p := range settledBulkPrefixes {
				if strings.HasPrefix(rid, id(p)) {
					return p
				}
			}
			return ""
		}
		for _, table := range []string{"issues", "wisps"} {
			//nolint:gosec // G201: table is one of two constants.
			r, err := tx.QueryContext(ctx, fmt.Sprintf("SELECT id, status, is_blocked FROM %s WHERE id LIKE 'eq-z%%'", table))
			if err != nil {
				return err
			}
			for r.Next() {
				var rid, status string
				var blocked int
				if err := r.Scan(&rid, &status, &blocked); err != nil {
					_ = r.Close()
					return err
				}
				if p := bulkPrefix(rid); p != "" {
					if bulk[p] == nil {
						bulk[p] = map[string]int{}
					}
					bulk[p][fmt.Sprintf("%s/%d", status, blocked)]++
					continue
				}
				plane := ""
				if table == "wisps" {
					plane = "@w"
				}
				rows = append(rows, fmt.Sprintf("%s%s=%s/%d", rid, plane, status, blocked))
			}
			_ = r.Close()
			if err := r.Err(); err != nil {
				return err
			}
		}
		for p, states := range bulk {
			for state, n := range states {
				rows = append(rows, fmt.Sprintf("%s*=%s x%d", id(p), state, n))
			}
		}
		sort.Strings(rows)
		out = append(out, step+": "+strings.Join(rows, " "))
		return nil
	}
	add := func(d *types.Dependency) error {
		_, err := issueops.AddDependencyInTx(ctx, tx, d, "settled-writer", issueops.AddDependencyOpts{})
		return err
	}
	closeIssue := func(suffix string) func() error {
		return func() error {
			_, err := issueops.CloseIssueInTx(ctx, tx, id(suffix), "done", "settled-writer", "")
			return err
		}
	}
	setStatus := func(suffix string, status types.Status) func() error {
		return func() error {
			_, err := issueops.UpdateIssueInTx(ctx, tx, id(suffix), map[string]interface{}{
				"status": status, issueops.OpForceClosePolicy: true,
			}, "settled-writer")
			return err
		}
	}
	steps := []struct {
		name string
		do   func() error
	}{
		{"setup: cross-plane edges", func() error {
			for _, d := range []*types.Dependency{
				dep(id("zD-wx"), id("zD-t"), types.DepBlocks),
				dep(id("zD-id"), id("zD-wx"), types.DepBlocks),
				dep(id("zD-ik"), id("zD-wx"), types.DepParentChild),
				dep(id("zF-id"), id("zF-wx"), types.DepBlocks),
			} {
				if err := add(d); err != nil {
					return fmt.Errorf("%s -> %s: %w", d.IssueID, d.DependsOnID, err)
				}
			}
			return nil
		}},
		{"A: close the blocked row", closeIssue("zA-x")},
		{"B: close the bulk parent", closeIssue("zB-p")},
		{"C: close gate child one", closeIssue("zC-k1")},
		{"C: update gate child two to closed", setStatus("zC-k2", types.StatusClosed)},
		{"D: close the wisp", closeIssue("zD-wx")},
		{"E: close the wisp twin", closeIssue("zE-dup")},
		{"F: update the issue to pinned", setStatus("zF-x", types.StatusPinned)},
		{"F: update the wisp to closed", setStatus("zF-wx", types.StatusClosed)},
		{"F: update the issue back to open", setStatus("zF-x", types.StatusOpen)},
		{"F: update the issue to closed", setStatus("zF-x", types.StatusClosed)},
		{"G: wire the row in this transaction", func() error {
			for _, d := range []*types.Dependency{
				dep(id("zG-x"), id("zG-t"), types.DepBlocks),
				dep(id("zG-d"), id("zG-x"), types.DepBlocks),
				dep(id("zG-c"), id("zG-x"), types.DepParentChild),
			} {
				if err := add(d); err != nil {
					return fmt.Errorf("%s -> %s: %w", d.IssueID, d.DependsOnID, err)
				}
			}
			return nil
		}},
		{"G: close the wired row", closeIssue("zG-x")},
		{"H: close one end of the stale cycle", closeIssue("zH-q1")},
		{"H: close the stale edgeless row", closeIssue("zH-x")},
		{"I: close the row above a stale cross-plane cycle", closeIssue("zI-a")},
		{"J: close the row whose dependers fill its recompute chunk", closeIssue("zJ-x")},
	}
	if err := snapshot("seeded"); err != nil {
		return nil, err
	}
	for _, st := range steps {
		if err := st.do(); err != nil {
			out = append(out, st.name+": "+err.Error())
			continue
		}
		if err := snapshot(st.name); err != nil {
			return nil, err
		}
	}
	return out, nil
}
