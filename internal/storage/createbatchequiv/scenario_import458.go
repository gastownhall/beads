package createbatchequiv

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// The 458-issue scenario (lifted from the batch-create review's differential
// against the pre-change code). One batch crosses every chunk boundary the
// fast paths have — the 100-row issue INSERTs, the 200-id IN-lists, label and
// event chunks — and mixes: case-variant and in-issue duplicate labels, wisps
// with wisp->wisp, wisp->issue and issue->wisp edges, chain blocks,
// parent-child under a blocked parent, closed blockers, two cycles, a
// hierarchy conflict, dangling and external targets, comments, duplicate ids
// inside and across deferred runs, generated ids, hierarchical child ids
// (one placed before its parent in the same run), upserts that RejectStale
// rejects or accepts, an edgeless row with a stale is_blocked, and new edges
// whose blocked-state fallout reaches stored rows outside the batch (a
// spawner's waiter, an upserted parent's existing child).

// seed458 is the stored state the 458-issue batch lands on.
func seed458() []*types.Issue {
	s1 := issue("s1", "seed one", "x")
	s2 := issue("s2", "seed two")
	s2.Dependencies = []*types.Dependency{dep(id("s2"), id("s1"), types.DepBlocks)}
	s3 := issue("s3", "seed closed")
	s3.Status = types.StatusClosed
	p := issue("p", "parent")
	c := issue("p.1", "child")
	c.Dependencies = []*types.Dependency{dep(id("p.1"), id("p"), types.DepParentChild)}
	w1 := issue("w1", "seed wisp")
	w1.Ephemeral = true
	s4 := issue("s4", "stale blocked")
	newer := issue("s5", "stored newer")
	newer.UpdatedAt = fixedAt.Add(time.Hour)
	// blocked parent with an open blocker, so children inherit
	bp := issue("bp", "blocked parent")
	bp.Dependencies = []*types.Dependency{dep(id("bp"), id("s1"), types.DepBlocks)}
	// A spawner with a waiter, and a parent with a child, for the recompute
	// seeding the batch's new edges reach beyond the batch's own rows.
	sp := issue("sp", "spawner")
	wt := issue("wt", "waiter")
	wt.Dependencies = []*types.Dependency{dep(id("wt"), id("sp"), types.DepWaitsFor)}
	ep := issue("ep", "existing parent")
	epc := issue("ep.1", "existing child")
	epc.Dependencies = []*types.Dependency{dep(id("ep.1"), id("ep"), types.DepParentChild)}
	return []*types.Issue{s1, s2, s3, p, c, w1, s4, newer, bp, sp, wt, ep, epc}
}

// batch458 is the 458-issue import batch.
func batch458() []*types.Issue {
	var out []*types.Issue
	const n = 450
	for i := 1; i <= n; i++ {
		is := issue(fmt.Sprintf("b%d", i), fmt.Sprintf("big %d", i), "L1", "l1", "L1", fmt.Sprintf("x%d", i%3))
		if i%7 == 0 {
			is.Ephemeral = true
			is.Labels = append(is.Labels, "wl")
		}
		if i > 1 && i%5 != 0 {
			is.Dependencies = append(is.Dependencies, dep(is.ID, id(fmt.Sprintf("b%d", i-1)), types.DepBlocks))
		}
		if i%10 == 0 {
			is.Dependencies = append(is.Dependencies, dep(is.ID, id("bp"), types.DepParentChild))
		}
		if i%11 == 0 {
			is.Dependencies = append(is.Dependencies, dep(is.ID, id("p"), types.DepParentChild))
		}
		if i%13 == 0 {
			is.Dependencies = append(is.Dependencies, dep(is.ID, id("s3"), types.DepBlocks))
		}
		if i%17 == 0 {
			is.Dependencies = append(is.Dependencies, dep(is.ID, id("w1"), types.DepBlocks))
		}
		if i%19 == 0 {
			is.Comments = []*types.Comment{{ID: fmt.Sprintf("eq-c-%d", i), Author: "a", Text: "hi", CreatedAt: fixedAt}}
		}
		out = append(out, is)
	}
	// cycles
	out[0].Dependencies = append(out[0].Dependencies, dep(out[0].ID, id("b4"), types.DepBlocks))
	out[300].Dependencies = append(out[300].Dependencies, dep(out[300].ID, id("b320"), types.DepBlocks))
	// hierarchy conflict: b20 child of bp, blocks on bp
	out[19].Dependencies = append(out[19].Dependencies, dep(out[19].ID, id("bp"), types.DepBlocks))
	// dangling + external
	out[50].Dependencies = append(out[50].Dependencies, dep(out[50].ID, id("nope"), types.DepBlocks), dep(out[50].ID, "external:a:b", types.DepBlocks))
	// upserts: s1 newer (accepted), s5 stale (rejected under RejectStaleUpserts), s4 edgeless stale flag
	s1 := issue("s1", "seed one again", "x", "y", "Y")
	s1.UpdatedAt = fixedAt.Add(time.Minute)
	s5 := issue("s5", "stale incoming", "stale-label")
	s5.Dependencies = []*types.Dependency{dep(id("s5"), id("b1"), types.DepBlocks)}
	s4 := issue("s4", "stale blocked again")
	s4.UpdatedAt = fixedAt.Add(time.Minute)
	// duplicates within batch (incl. across a deferred run)
	d1 := issue("b3", "big 3 again", "new3", "L1")
	d1.UpdatedAt = fixedAt.Add(time.Minute)
	d2 := issue("b250", "big 250 again", "new250")
	d2.UpdatedAt = fixedAt.Add(time.Minute)
	d2.Dependencies = []*types.Dependency{dep(id("b250"), id("b1"), types.DepBlocks)}
	gen := &types.Issue{Title: "generated", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeBug,
		CreatedAt: fixedAt, UpdatedAt: fixedAt, Labels: []string{"g", "L1"}}
	gen2 := &types.Issue{Title: "generated2", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeBug,
		CreatedAt: fixedAt, UpdatedAt: fixedAt}
	h := issue("p.9", "ninth child")
	h.Dependencies = []*types.Dependency{dep(id("p.9"), id("p"), types.DepParentChild)}
	// child before parent within one deferred run (hierarchical id)
	hc := issue("np.3", "child of later parent")
	hc.Dependencies = []*types.Dependency{dep(id("np.3"), id("np"), types.DepParentChild)}
	hp := issue("np", "later parent")
	mid := out[:200]
	rest := out[200:]
	var all []*types.Issue
	all = append(all, hc, hp)
	all = append(all, mid...)
	all = append(all, s1, d1, gen, s5)
	all = append(all, rest...)
	// A first child under the spawner (its waiter becomes blocked), and an
	// upsert that blocks the existing parent (its existing child follows).
	spc := issue("sp.1", "spawned child")
	spc.Dependencies = []*types.Dependency{dep(id("sp.1"), id("sp"), types.DepParentChild)}
	epUp := issue("ep", "existing parent, now blocked")
	epUp.UpdatedAt = fixedAt.Add(time.Minute)
	epUp.Dependencies = []*types.Dependency{dep(id("ep"), id("s1"), types.DepBlocks)}
	all = append(all, d2, gen2, h, s4, spc, epUp)
	return all
}

// afterSeed458 plants the stale is_blocked flags the batch must settle: an
// edgeless row marked blocked, and a blocked parent marked unblocked.
func afterSeed458(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, q := range []struct {
		flag   int
		suffix string
	}{{1, "s4"}, {0, "bp"}} {
		if _, err := db.Exec("UPDATE issues SET is_blocked = ? WHERE id = ?", q.flag, id(q.suffix)); err != nil {
			t.Fatalf("plant is_blocked: %v", err)
		}
	}
}
