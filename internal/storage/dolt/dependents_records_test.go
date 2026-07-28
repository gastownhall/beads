package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/types"
)

// TestGetDependentRecords verifies the target-keyed raw dependents read:
// direction correctness (rows whose target is X, not whose source is X), the
// dep-type filter, that rows span BOTH the durable and wisp dependency tables,
// row-id keyset paging with no drop/dup across the two-table boundary, and that
// it does not drop rows on source status (raw, no source hydration). Ordering is
// by the dependency row's primary id (a UUIDv5), so assertions are set-based.
func TestGetDependentRecords(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	mk := func(id string, ephemeral bool) *types.Issue {
		iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: ephemeral}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		return iss
	}
	target := mk("dr-target", false)
	block := mk("dr-s1-block", false)   // blocks -> X (durable, `dependencies`)
	childA := mk("dr-s2-childa", false) // parent-child -> X (durable)
	closed := mk("dr-s3-closed", false) // parent-child -> X, then closed (durable)
	wispDep := mk("dr-s4-wisp", true)   // blocks -> X from a WISP source (`wisp_dependencies`)
	other := mk("dr-other", false)      // X -> other (decoy: source is X)

	addDep := func(src, tgt string, typ types.DependencyType) {
		if err := store.AddDependency(ctx, &types.Dependency{IssueID: src, DependsOnID: tgt, Type: typ}, "tester"); err != nil {
			t.Fatalf("add dep %s -> %s (%s): %v", src, tgt, typ, err)
		}
	}
	addDep(block.ID, target.ID, types.DepBlocks)
	addDep(childA.ID, target.ID, types.DepParentChild)
	addDep(closed.ID, target.ID, types.DepParentChild)
	addDep(wispDep.ID, target.ID, types.DepBlocks) // wisp source -> durable target (wisp_dependencies)
	addDep(target.ID, other.ID, types.DepBlocks)   // decoy: target is the SOURCE here
	if err := store.CloseIssue(ctx, closed.ID, "done", "tester", ""); err != nil {
		t.Fatalf("close %s: %v", closed.ID, err)
	}

	srcSet := func(deps []*types.Dependency) map[string]bool {
		out := map[string]bool{}
		for _, d := range deps {
			out[d.IssueID] = true
		}
		return out
	}
	eqSet := func(t *testing.T, got map[string]bool, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("dependent sources = %v, want %v", got, want)
		}
		for _, w := range want {
			if !got[w] {
				t.Fatalf("dependent sources = %v, missing %q", got, w)
			}
		}
	}

	// Direction + raw + two-table span: all inbound edges of X regardless of
	// source status, INCLUDING the wisp source; the decoy must not appear.
	all, err := store.GetDependentRecords(ctx, target.ID, "", 100, "")
	if err != nil {
		t.Fatalf("GetDependentRecords(all): %v", err)
	}
	eqSet(t, srcSet(all), block.ID, childA.ID, closed.ID, wispDep.ID)
	for _, d := range all {
		if d.ID == "" {
			t.Fatalf("dependent row has empty ID (the keyset cursor): %+v", d)
		}
		if d.DependsOnID != target.ID {
			t.Fatalf("row %s has target %s, want %s", d.IssueID, d.DependsOnID, target.ID)
		}
		if d.IssueID == target.ID {
			t.Fatalf("direction violation: returned a row whose SOURCE is the target")
		}
	}

	// The decoy edge is discoverable from the OTHER direction (target=other).
	fromOther, err := store.GetDependentRecords(ctx, other.ID, "", 100, "")
	if err != nil {
		t.Fatalf("GetDependentRecords(other): %v", err)
	}
	eqSet(t, srcSet(fromOther), target.ID)

	// Type filter: only parent-child edges (the wisp edge is 'blocks', excluded).
	pc, err := store.GetDependentRecords(ctx, target.ID, string(types.DepParentChild), 100, "")
	if err != nil {
		t.Fatalf("GetDependentRecords(parent-child): %v", err)
	}
	eqSet(t, srcSet(pc), childA.ID, closed.ID)

	// Row-id keyset paging across the two-table boundary: page size 1 walks all
	// four inbound sources (three durable + one wisp) with no gaps or duplicates.
	seen := map[string]bool{}
	var pages int
	after := ""
	for {
		page, err := store.GetDependentRecords(ctx, target.ID, "", 1, after)
		if err != nil {
			t.Fatalf("GetDependentRecords(page after %q): %v", after, err)
		}
		if len(page) == 0 {
			break
		}
		if len(page) != 1 {
			t.Fatalf("page size = %d, want 1", len(page))
		}
		if seen[page[0].IssueID] {
			t.Fatalf("duplicate source %q across pages (keyset drop/dup)", page[0].IssueID)
		}
		seen[page[0].IssueID] = true
		after = page[0].ID
		if pages++; pages > 10 {
			t.Fatalf("paging did not terminate")
		}
	}
	eqSet(t, seen, block.ID, childA.ID, closed.ID, wispDep.ID)
}

// TestCountDependentRecords verifies the un-paged total matches the paged read's
// membership, spans both tables, and honors the type filter.
func TestCountDependentRecords(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	mk := func(id string, ephemeral bool) {
		iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: ephemeral}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	mk("cd-target", false)
	mk("cd-s1", false)
	mk("cd-s2", false)
	mk("cd-w", true)
	add := func(src string, typ types.DependencyType) {
		if err := store.AddDependency(ctx, &types.Dependency{IssueID: src, DependsOnID: "cd-target", Type: typ}, "tester"); err != nil {
			t.Fatalf("add dep %s: %v", src, err)
		}
	}
	add("cd-s1", types.DepBlocks)
	add("cd-s2", types.DepParentChild)
	add("cd-w", types.DepBlocks) // wisp source (wisp_dependencies)

	if n, err := store.CountDependentRecords(ctx, "cd-target", ""); err != nil {
		t.Fatalf("CountDependentRecords: %v", err)
	} else if n != 3 {
		t.Fatalf("CountDependentRecords(all) = %d, want 3 (2 durable + 1 wisp)", n)
	}
	if n, err := store.CountDependentRecords(ctx, "cd-target", string(types.DepBlocks)); err != nil {
		t.Fatalf("CountDependentRecords(blocks): %v", err)
	} else if n != 2 {
		t.Fatalf("CountDependentRecords(blocks) = %d, want 2 (cd-s1 durable + cd-w wisp)", n)
	}
	if n, err := store.CountDependentRecords(ctx, "cd-target", string(types.DepParentChild)); err != nil {
		t.Fatalf("CountDependentRecords(parent-child): %v", err)
	} else if n != 1 {
		t.Fatalf("CountDependentRecords(parent-child) = %d, want 1", n)
	}
	// A target with no dependents counts zero, not an error.
	if n, err := store.CountDependentRecords(ctx, "cd-s1", ""); err != nil {
		t.Fatalf("CountDependentRecords(leaf): %v", err)
	} else if n != 0 {
		t.Fatalf("CountDependentRecords(leaf) = %d, want 0", n)
	}
}

// TestDependentRecordsCrossTableCollision seeds the SAME logical edge in BOTH
// the durable and wisp dependency tables — they share one depid-derived id
// because depid.New keys on (issue_id, target) and omits the table — and proves
// the target-keyed reads treat it as a single inbound edge: GetDependentRecords
// returns one row per id (the durable copy), CountDependentRecords equals the
// distinct total (not the sum of two per-table COUNTs), and keyset paging is
// stable across the collision (no dup, no drop). A collision like this arises
// from a wisp promoted to durable or two Dolt clones merged.
func TestDependentRecordsCrossTableCollision(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	mk := func(id string, ephemeral bool) {
		iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: ephemeral}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	mk("cc-target", false)
	mk("cc-a", false) // durable source of the collision edge
	mk("cc-c", false) // durable-only dependent
	mk("cc-w", true)  // genuine wisp-only dependent

	add := func(src string) {
		if err := store.AddDependency(ctx, &types.Dependency{IssueID: src, DependsOnID: "cc-target", Type: types.DepBlocks}, "tester"); err != nil {
			t.Fatalf("add dep %s: %v", src, err)
		}
	}
	add("cc-a") // -> dependencies, id = depid.New("cc-a", "cc-target")
	add("cc-c") // -> dependencies
	add("cc-w") // wisp source -> wisp_dependencies

	// Force the collision: the SAME (cc-a, cc-target) edge into wisp_dependencies
	// with the same depid. FK checks are relaxed because cc-a is not a wisp — the
	// point is to reproduce the post-merge/promotion state where one edge exists
	// in both tables under one id.
	collisionID := depid.New("cc-a", "cc-target")
	if _, err := store.db.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0"); err != nil {
		t.Fatalf("disable FK checks: %v", err)
	}
	if _, err := store.db.ExecContext(ctx,
		"INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, type, created_by) VALUES (?, ?, ?, 'blocks', 'tester')",
		collisionID, "cc-a", "cc-target"); err != nil {
		t.Fatalf("seed collision row: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 1"); err != nil {
		t.Fatalf("re-enable FK checks: %v", err)
	}

	// One row per id: cc-a appears once (its durable copy), never twice.
	all, err := store.GetDependentRecords(ctx, "cc-target", "", 100, "")
	if err != nil {
		t.Fatalf("GetDependentRecords: %v", err)
	}
	ids := map[string]int{}
	srcs := map[string]bool{}
	for _, d := range all {
		ids[d.ID]++
		srcs[d.IssueID] = true
	}
	for id, n := range ids {
		if n != 1 {
			t.Fatalf("id %s appears %d times in one page; want exactly 1 (collision not de-duped)", id, n)
		}
	}
	if len(srcs) != 3 || !srcs["cc-a"] || !srcs["cc-c"] || !srcs["cc-w"] {
		t.Fatalf("dependent sources = %v, want {cc-a, cc-c, cc-w}", srcs)
	}

	// Count equals the distinct total (3), not the sum of two per-table COUNTs (4).
	if n, err := store.CountDependentRecords(ctx, "cc-target", ""); err != nil {
		t.Fatalf("CountDependentRecords: %v", err)
	} else if n != 3 {
		t.Fatalf("CountDependentRecords = %d, want 3 (distinct); a sum-of-counts would report 4", n)
	}

	// Keyset paging (size 1) is stable across the collision: 3 distinct sources,
	// each once, no drop.
	seen := map[string]bool{}
	after := ""
	for i := 0; i < 10; i++ {
		page, err := store.GetDependentRecords(ctx, "cc-target", "", 1, after)
		if err != nil {
			t.Fatalf("page after %q: %v", after, err)
		}
		if len(page) == 0 {
			break
		}
		if len(page) != 1 {
			t.Fatalf("page size = %d, want 1", len(page))
		}
		if seen[page[0].IssueID] {
			t.Fatalf("duplicate source %q across pages (collision drop/dup)", page[0].IssueID)
		}
		seen[page[0].IssueID] = true
		after = page[0].ID
	}
	if len(seen) != 3 || !seen["cc-a"] || !seen["cc-c"] || !seen["cc-w"] {
		t.Fatalf("paged sources = %v, want 3 distinct {cc-a, cc-c, cc-w}", seen)
	}
}

// TestGetDependentRecordsLimitClamp verifies the self-clamp (default 100, cap 500).
func TestGetDependentRecordsLimitClamp(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	if err := store.CreateIssue(ctx, &types.Issue{ID: "lc-target", Title: "t", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "tester"); err != nil {
		t.Fatalf("create target: %v", err)
	}
	// 120 durable dependents -> more than the default clamp (100), fewer than
	// the cap (500), so we can observe the default kick in without seeding 500.
	const n = 120
	for i := 0; i < n; i++ {
		id := "lc-s" + pad(i)
		if err := store.CreateIssue(ctx, &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		if err := store.AddDependency(ctx, &types.Dependency{IssueID: id, DependsOnID: "lc-target", Type: types.DepBlocks}, "tester"); err != nil {
			t.Fatalf("add dep %s: %v", id, err)
		}
	}

	// limit <= 0 => issueops.defaultDependentRecordsLimit (100).
	def, err := store.GetDependentRecords(ctx, "lc-target", "", 0, "")
	if err != nil {
		t.Fatalf("GetDependentRecords(limit=0): %v", err)
	}
	if len(def) != 100 {
		t.Fatalf("default clamp: got %d rows, want 100", len(def))
	}
	// A limit above the total returns the total (and is under the 500 cap).
	full, err := store.GetDependentRecords(ctx, "lc-target", "", 100000, "")
	if err != nil {
		t.Fatalf("GetDependentRecords(limit=100000): %v", err)
	}
	if len(full) != n {
		t.Fatalf("clamp with limit>total: got %d rows, want %d", len(full), n)
	}
}

// TestGetDependentRecordsForIssues verifies the BATCHED target-keyed dependents
// read: it keys inbound edges by target across a SET of targets in ONE call,
// spans both dependency tables (durable + wisp sources), returns the FULL dep-type
// set (blocks, waits-for, conditional-blocks, parent-child — no type is dropped,
// unlike GetBlockingInfoForIssues which restricts to blocks/parent-child), keeps
// each row's real dep_type, and never surfaces an edge whose target is not the
// key (a decoy where the id is the SOURCE stays out).
func TestGetDependentRecordsForIssues(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	mk := func(id string, ephemeral bool) {
		iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Ephemeral: ephemeral}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	mk("bt-x", false)     // target X
	mk("bt-y", false)     // target Y
	mk("bt-blk", false)   // blocks -> X (durable)
	mk("bt-wait", false)  // waits-for -> X (durable)
	mk("bt-cond", true)   // conditional-blocks -> X from a WISP source (wisp_dependencies)
	mk("bt-child", false) // parent-child -> X (durable)
	mk("bt-yblk", false)  // blocks -> Y (durable)
	mk("bt-z", false)     // decoy target: X -> Z (X is the SOURCE)

	addDep := func(src, tgt string, typ types.DependencyType) {
		if err := store.AddDependency(ctx, &types.Dependency{IssueID: src, DependsOnID: tgt, Type: typ}, "tester"); err != nil {
			t.Fatalf("add dep %s -> %s (%s): %v", src, tgt, typ, err)
		}
	}
	addDep("bt-blk", "bt-x", types.DepBlocks)
	addDep("bt-wait", "bt-x", types.DepWaitsFor)
	addDep("bt-cond", "bt-x", types.DepConditionalBlocks) // wisp source -> wisp_dependencies
	addDep("bt-child", "bt-x", types.DepParentChild)
	addDep("bt-yblk", "bt-y", types.DepBlocks)
	addDep("bt-x", "bt-z", types.DepBlocks) // decoy: X is the SOURCE here

	typesBySrc := func(deps []*types.Dependency, target string) map[string]types.DependencyType {
		out := map[string]types.DependencyType{}
		for _, d := range deps {
			if d.DependsOnID != target {
				t.Fatalf("row keyed under %s has target %s (must equal the key)", target, d.DependsOnID)
			}
			if d.ID == "" {
				t.Fatalf("dependent row has empty ID (the row primary key): %+v", d)
			}
			out[d.IssueID] = d.Type
		}
		return out
	}

	byTarget, err := store.GetDependentRecordsForIssues(ctx, []string{"bt-x", "bt-y"})
	if err != nil {
		t.Fatalf("GetDependentRecordsForIssues: %v", err)
	}

	// X: all four inbound edges, keyed by target, spanning both tables, with the
	// FULL type set and real dep_type preserved (no drop of waits-for/conditional-
	// blocks, no drop of the wisp-source or the parent-child edge).
	x := typesBySrc(byTarget["bt-x"], "bt-x")
	if len(x) != 4 {
		t.Fatalf("X dependents = %v, want 4 (blocks, waits-for, conditional-blocks[wisp], parent-child)", x)
	}
	if x["bt-blk"] != types.DepBlocks || x["bt-wait"] != types.DepWaitsFor ||
		x["bt-cond"] != types.DepConditionalBlocks || x["bt-child"] != types.DepParentChild {
		t.Fatalf("X dependents lost a real dep_type or dropped a wisp/blocking edge: %v", x)
	}
	if _, bad := x["bt-z"]; bad {
		t.Fatalf("decoy edge X->Z surfaced as an inbound edge of X: %v", x)
	}

	// Y: its single inbound blocks edge, keyed separately in the same batch.
	y := typesBySrc(byTarget["bt-y"], "bt-y")
	if len(y) != 1 || y["bt-yblk"] != types.DepBlocks {
		t.Fatalf("Y dependents = %v, want {bt-yblk: blocks}", y)
	}

	// A batch element with no inbound edges is simply absent from the map (bt-blk
	// only has an OUTGOING edge), never an error or a phantom key.
	leaf, err := store.GetDependentRecordsForIssues(ctx, []string{"bt-blk"})
	if err != nil {
		t.Fatalf("GetDependentRecordsForIssues(leaf): %v", err)
	}
	if got, present := leaf["bt-blk"]; present {
		t.Fatalf("source-only node bt-blk has inbound edges %v, want absent", got)
	}

	// Empty input is a valid empty map, not an error.
	if got, err := store.GetDependentRecordsForIssues(ctx, nil); err != nil {
		t.Fatalf("GetDependentRecordsForIssues(nil): %v", err)
	} else if len(got) != 0 {
		t.Fatalf("GetDependentRecordsForIssues(nil) = %v, want empty", got)
	}
}

// pad renders i as a zero-padded 4-digit string for stable id construction.
func pad(i int) string {
	s := ""
	for _, d := range []int{1000, 100, 10, 1} {
		s += string(rune('0' + (i/d)%10))
	}
	return s
}
