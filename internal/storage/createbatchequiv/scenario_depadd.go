package createbatchequiv

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// The depadd scenario drives the single-edge dependency paths — the
// reachability and hierarchy probes AddDependencyInTx, the domain stack's
// HasCycle / ValidateBlockingHierarchy and apply-batch's end gate share, and
// AddDependencyInTx itself — over a seeded graph that mixes both dependency
// tables, every target column, scheduling and non-scheduling edge types, a
// parent-child hierarchy, and closed rows. Probe answers and refusals are
// recorded verbatim; accepted edges land in the compared tables.

func seedDepAdd() []*types.Issue {
	withDeps := func(is *types.Issue, deps ...*types.Dependency) *types.Issue {
		is.Dependencies = deps
		return is
	}
	wisp := func(is *types.Issue) *types.Issue {
		is.Ephemeral = true
		return is
	}
	closed := issue("dc", "closed")
	closed.Status = types.StatusClosed
	return []*types.Issue{
		issue("de", "epic"),
		withDeps(issue("de.1", "child"), dep(id("de.1"), id("de"), types.DepParentChild)),
		withDeps(issue("de.1.1", "grandchild"), dep(id("de.1.1"), id("de.1"), types.DepParentChild)),
		withDeps(issue("de.2", "second child"), dep(id("de.2"), id("de"), types.DepParentChild), dep(id("de.2"), id("dc"), types.DepBlocks)),
		issue("db1", "blocker one"),
		withDeps(issue("db2", "blocker two"), dep(id("db2"), id("db1"), types.DepBlocks)),
		withDeps(issue("db3", "blocker three"), dep(id("db3"), id("db2"), types.DepBlocks)),
		withDeps(issue("db4", "conditional"), dep(id("db4"), id("db3"), types.DepConditionalBlocks)),
		withDeps(issue("dr", "related only"), dep(id("dr"), id("db4"), types.DepRelated)),
		withDeps(issue("dwf", "waits for"), dep(id("dwf"), id("de.1.1"), types.DepWaitsFor)),
		closed,
		wisp(issue("dw1", "wisp child of epic")),
		wisp(withDeps(issue("dw2", "wisp blocked by wisp"), dep(id("dw2"), id("dw1"), types.DepBlocks))),
		withDeps(issue("dx", "issue blocked by wisp"), dep(id("dx"), "external:proj:cap", types.DepBlocks)),
		issue("dl", "lone"),
		issue("dk1", "legacy cyclic parent one"),
		issue("dk2", "legacy cyclic parent two"),
	}
}

// plantParentCycle writes a parent-child 2-cycle (dk1 <-> dk2) straight into
// dependencies, as legacy or merged data can hold even though every write
// path refuses it, so the probes show the walks terminate on stored cycles
// (UNION distinct recursion over unique nodes) and answer across them.
func plantParentCycle(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, pair := range [][2]string{{id("dk1"), id("dk2")}, {id("dk2"), id("dk1")}} {
		if _, err := db.Exec(`INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_by, created_at, metadata)
			VALUES (?, ?, ?, 'parent-child', 'legacy', ?, '{}')`, "legacy-"+pair[0]+"-"+pair[1], pair[0], pair[1], fixedAt); err != nil {
			t.Fatalf("plant legacy parent cycle: %v", err)
		}
	}
}

func depAddNodes() []string {
	return []string{
		id("de"), id("de.1"), id("de.1.1"), id("de.2"), id("db1"), id("db2"), id("db3"), id("db4"),
		id("dr"), id("dwf"), id("dc"), id("dw1"), id("dw2"), id("dx"), id("dl"), id("dk1"), id("dk2"),
		"external:proj:cap", id("dmissing"),
	}
}

// runDepAdd records every probe over every ordered node pair, then adds a
// sequence of edges, recording each result or refusal.
func runDepAdd(ctx context.Context, tx *sql.Tx) ([]string, error) {
	var out []string
	nodes := depAddNodes()
	errText := func(err error) string {
		if err == nil {
			return "ok"
		}
		return err.Error()
	}
	// The cross-plane seed edges: a mixed create batch refuses them.
	for _, d := range []*types.Dependency{dep(id("dw1"), id("de"), types.DepParentChild), dep(id("dx"), id("dw2"), types.DepBlocks)} {
		if _, err := issueops.AddDependencyInTx(ctx, tx, d, "dep-writer", issueops.AddDependencyOpts{}); err != nil {
			return nil, fmt.Errorf("seed %s -> %s: %w", d.IssueID, d.DependsOnID, err)
		}
	}
	for _, u := range nodes {
		for _, v := range nodes {
			for _, tables := range [][]string{nil, {"dependencies"}, {"wisp_dependencies"}} {
				cycle, err := issueops.WouldCreateSchedulingCycleInTx(ctx, tx, u, v, tables)
				if err != nil {
					return nil, err
				}
				blocks := &types.Dependency{IssueID: u, DependsOnID: v, Type: types.DepBlocks}
				hier := issueops.CheckBlockingHierarchyInTx(ctx, tx, blocks, tables)
				cyc := issueops.CheckDependencyCycleInTx(ctx, tx, &types.Dependency{IssueID: u, DependsOnID: v, Type: types.DepParentChild}, tables)
				out = append(out, fmt.Sprintf("probe %s -> %s tables=%v: cycle=%v hierarchy=%s cycle-check=%s", u, v, tables, cycle, errText(hier), errText(cyc)))
			}
		}
	}
	adds := []*types.Dependency{
		dep(id("db1"), id("db4"), types.DepBlocks),            // closes a blocks/conditional cycle
		dep(id("db1"), id("dr"), types.DepBlocks),             // related is not scheduling: accepted
		dep(id("de"), id("de.1.1"), types.DepBlocks),          // blocker is a descendant
		dep(id("de.1.1"), id("de"), types.DepBlocks),          // blocker is an ancestor
		dep(id("de.1.1"), id("de.2"), types.DepBlocks),        // cousin: accepted
		dep(id("de"), id("dw2"), types.DepParentChild),        // cycle through the wisp table
		dep(id("dw1"), id("dx"), types.DepBlocks),             // wisp source: cycle via dx -> dw2 -> dw1
		dep(id("dw2"), id("dl"), types.DepBlocks),             // wisp source, accepted
		dep(id("dl"), id("dw1"), types.DepConditionalBlocks),  // issue -> wisp target: accepted
		dep(id("dl"), id("dc"), types.DepBlocks),              // closed blocker: accepted, not blocked by it
		dep(id("dl"), id("dl"), types.DepBlocks),              // self
		dep(id("dl"), id("db3"), types.DepWaitsFor),           // non-scheduling
		dep(id("db1"), id("dl"), types.DepBlocks),             // accepted
		dep(id("db4"), id("db1"), types.DepParentChild),       // shortcut of db4 -> db3 -> db2 -> db1: accepted
		dep(id("dmissing"), id("dl"), types.DepBlocks),        // missing source
		dep(id("dl"), id("dmissing"), types.DepBlocks),        // missing target
		dep(id("db2"), id("db1"), types.DepRelated),           // type conflict with the stored blocks row
		dep(id("dr"), "external:proj:other", types.DepBlocks), // external target
		dep(id("de.2"), id("de.1"), types.DepParentChild),     // re-parent sideways: accepted
		dep(id("de.1"), id("de.2"), types.DepBlocks),          // blocker is now a parent
		dep(id("dk1"), id("dl"), types.DepBlocks),             // source on a stored parent cycle: accepted
		dep(id("dl"), id("dk2"), types.DepBlocks),             // closes a cycle through dk2 -> dk1 -> dl
		dep(id("dk2"), id("dk1"), types.DepBlocks),            // blocker is (cyclically) an ancestor
	}
	for _, d := range adds {
		_, err := issueops.AddDependencyInTx(ctx, tx, d, "dep-writer", issueops.AddDependencyOpts{EmitEvent: true})
		out = append(out, fmt.Sprintf("add %s -%s-> %s: %s", d.IssueID, d.Type, d.DependsOnID, errText(err)))
	}
	return out, nil
}
