package conformance

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// This file is the ONE assertion kit every is_blocked postcondition case in
// this package goes through, so five role contracts cannot drift into five
// slightly different versions of one promise. The promise itself is
// issueops.BlockedStateInvariant, cited BY SYMBOL from each case; nothing here
// asserts more than that anchor says.
//
// WHAT THE THREE LEGS ARE WORTH, per role, because it is not uniform and the
// mutation verdicts depend on it:
//
//   - DependencyEditor is TWO GENUINE VOTES plus an engine check. The add and
//     remove wirings are hand-mirrored between internal/storage/issueops
//     (dependencies.go addDependencyInTx / removeDependencyInTx, shared by the
//     two store backends) and internal/storage/domain/db (dependency.go Insert
//     / Delete, the unit-of-work body). The second file's comment cites the
//     first, which is exactly what a mirrored body looks like the day before it
//     stops being one: bd-6dnrw.44 item 3 was a uow copy that skipped the
//     descendant expansion.
//   - Lifecycle.Update's status crossing is TWO GENUINE VOTES: issueops
//     update.go and domain/db issue.go Update each decide independently when a
//     status move counts as a crossing.
//   - Deleter is TWO GENUINE VOTES: issueops delete.go against domain
//     issue_delete.go.
//   - Lifecycle.Close, Lifecycle.Reopen and BatchCloser are ONE BODY plus a
//     wrapper and engine check. All three legs reach issueops.closeIssueInTx
//     and ReopenIssueInTx, uow through the domain issue repository. Their cases
//     still run everywhere, for the reason the TreeWalker contract gives — every
//     measured drift in that family lived in a WRAPPER — but a mutation there
//     reddens all three legs at once and proves nothing about per-leg wiring.
//
// TWO STRUCTURAL ASYMMETRIES, RECORDED HERE AND DELIBERATELY NOT FIXED. Both
// are shape differences between the store-backed and unit-of-work wirings that
// are not semantic divergences today; the cases pin the behavior so that if
// either becomes one, it becomes one loudly.
//
//   - CREATE-WITH-EDGES runs ONE terminal recompute over the union of created
//     ids and per-edge affected sets on the store-backed side
//     (internal/storage/issueops/create.go), and per-edge maintenance through
//     the dependency repository's Insert on the unit-of-work side. Convergent
//     by ARGUMENT — adds are monotonic and the one non-monotonic add
//     (parent-child) recomputes on both — and an argument is not a pinned fact.
//     RunIssueOperationsCreateWithDependenciesSettlesInTheCreatingTransaction
//     is what pins it.
//   - THE WISP PLANE has store-only recompute call sites with no unit-of-work
//     twin: the persistence move (dolt's demoteToWispInTx) and the store's own
//     wisp delete and batch wisp delete. Neither is on a role path — the
//     Deleter role reaches wisps through the shared delete body, and a wisp
//     CLOSE routes inside closeIssueInTx on all three legs rather than through
//     a separate site — so no case here covers them, and that is a scope
//     statement rather than an omission.
//
// HOW THESE CASES CANNOT BE THE DEFECT THIS PROGRAM ALREADY SHIPPED. One
// retired case seeded is_blocked = 1 with no blocker edge, so the guard it was
// named for short-circuited and it could never fail
// (engdocs/ADDING_AN_ISSUEOPS_ROLE.md, "Ask what the fixture makes
// unobservable"). Four structural rules, enforced here rather than asked for:
//
//  1. THE FIXTURE NEVER WRITES THE FLAG. There is no hook in this file that
//     sets is_blocked, and none of the role fixtures has one. Every state is
//     built through role verbs and the column value is EARNED, then read raw.
//  2. EVERY CASE FLIPS. blockedStateFlip refuses at t.Fatal if the pre-verb raw
//     value already equals the value the case is about to assert, so a body
//     that recomputes nothing fails by construction instead of passing on a
//     value the seed happened to leave behind.
//  3. A BLOCKED PRECONDITION PINS THE REASON. requireBlockedByOpenBlocker
//     checks the flag AND the edge AND the blocker's open status;
//     requireBlockedWithNoDirectBlockerEdges checks the flag AND the absence of
//     any direct edge, which is the pair the retired case lacked.
//  4. EVERY CASE CARRIES A CONTROL that must NOT move, read through the same
//     raw reader, so a reader pointed at the wrong plane or a seed that never
//     landed shows up as a green control that should have been a red one.
//
// Reads go through the fixture's QueryScalar hook rather than a fixture value,
// because the five role fixtures are five types with one identically-shaped
// hook; taking the hook is what lets one kit serve all of them.

// blockedStatePlane names one of the two issue planes and the tables that hold
// its rows and its outgoing edges. Cross-plane cases assert residency rather
// than assuming it: cross-plane and cross-tier is where the earlier is_blocked
// defects lived (bd-6dnrw.44, and the reason dolt/crosstier_dep_test.go
// exists).
type blockedStatePlane struct {
	name  string
	rows  string
	edges string
}

var (
	blockedStateIssues = blockedStatePlane{name: "issue", rows: "issues", edges: "dependencies"}
	blockedStateWisps  = blockedStatePlane{name: "wisp", rows: "wisps", edges: "wisp_dependencies"}
)

// blockedStateRow names one row by id and plane. Everything in this file takes
// one of these rather than a bare id, so a case can never read the issues table
// for a row that lives in the wisps one and conclude it is unblocked.
type blockedStateRow struct {
	ID    string
	Plane blockedStatePlane
}

func blockedIssue(id string) blockedStateRow {
	return blockedStateRow{ID: id, Plane: blockedStateIssues}
}
func blockedWisp(id string) blockedStateRow { return blockedStateRow{ID: id, Plane: blockedStateWisps} }

func (r blockedStateRow) String() string { return r.Plane.name + " " + r.ID }

// blockedStateProbe is the raw reader. It reads COLUMNS, never role answers:
// is_blocked is derived AND persisted, so a case that asks a role whether an
// issue is blocked passes against a backend that never denormalizes, which is
// the whole failure mode these cases exist to catch.
type blockedStateProbe struct {
	ctx         context.Context
	queryScalar func(context.Context, string, []any, ...any) error
}

func newBlockedStateProbe(ctx context.Context, queryScalar func(context.Context, string, []any, ...any) error) *blockedStateProbe {
	return &blockedStateProbe{ctx: ctx, queryScalar: queryScalar}
}

// rawBlocked reads the stored flag. A missing row is a FAILURE, not a zero: a
// reader that decayed an absent row to "not blocked" would make every unmark
// assertion pass against a fixture whose seed never landed.
func (p *blockedStateProbe) rawBlocked(t *testing.T, row blockedStateRow) int {
	t.Helper()
	var blocked int
	//nolint:gosec // G201: the table name comes from the two plane constants above.
	query := fmt.Sprintf("SELECT CAST(COALESCE(is_blocked, 0) AS SIGNED) FROM %s WHERE id = ?", row.Plane.rows)
	if err := p.queryScalar(p.ctx, query, []any{row.ID}, &blocked); err != nil {
		t.Fatalf("read raw is_blocked for %s: %v", row, err)
	}
	return blocked
}

// rawUpdatedAt reads the timestamp the non-perturbation clause of
// issueops.BlockedStateInvariant protects. It is read as text at column
// precision, which is the granularity a merge between two clones compares.
func (p *blockedStateProbe) rawUpdatedAt(t *testing.T, row blockedStateRow) string {
	t.Helper()
	var stamp string
	//nolint:gosec // G201: the table name comes from the two plane constants above.
	query := fmt.Sprintf("SELECT COALESCE(CAST(updated_at AS CHAR), '') FROM %s WHERE id = ?", row.Plane.rows)
	if err := p.queryScalar(p.ctx, query, []any{row.ID}, &stamp); err != nil {
		t.Fatalf("read raw updated_at for %s: %v", row, err)
	}
	return stamp
}

func (p *blockedStateProbe) rawStatus(t *testing.T, row blockedStateRow) string {
	t.Helper()
	var status string
	//nolint:gosec // G201: the table name comes from the two plane constants above.
	query := fmt.Sprintf("SELECT status FROM %s WHERE id = ?", row.Plane.rows)
	if err := p.queryScalar(p.ctx, query, []any{row.ID}, &status); err != nil {
		t.Fatalf("read raw status for %s: %v", row, err)
	}
	return status
}

// directBlockerEdges counts the outgoing edges that would make a row's block
// DIRECT rather than inherited. Both blocking types count, because the
// predicate treats them alike.
func (p *blockedStateProbe) directBlockerEdges(t *testing.T, row blockedStateRow) int {
	t.Helper()
	var edges int
	//nolint:gosec // G201: the table name comes from the two plane constants above.
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE issue_id = ? AND (type = ? OR type = ?)", row.Plane.edges)
	if err := p.queryScalar(p.ctx, query, []any{row.ID, string(types.DepBlocks), string(types.DepConditionalBlocks)}, &edges); err != nil {
		t.Fatalf("count direct blocker edges for %s: %v", row, err)
	}
	return edges
}

// requirePlaneResidency proves the row is where the case thinks it is: present
// in its own plane's table and ABSENT from the other. The second half is the
// half that catches a cross-plane routing regression, because a wisp that
// leaked a durable row would still read is_blocked correctly from one of them.
func (p *blockedStateProbe) requirePlaneResidency(t *testing.T, row blockedStateRow) {
	t.Helper()
	other := blockedStateIssues
	if row.Plane.rows == blockedStateIssues.rows {
		other = blockedStateWisps
	}
	for _, probe := range []struct {
		table string
		want  int
	}{{row.Plane.rows, 1}, {other.rows, 0}} {
		var count int
		//nolint:gosec // G201: the table name comes from the two plane constants above.
		query := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE id = ?", probe.table)
		if err := p.queryScalar(p.ctx, query, []any{row.ID}, &count); err != nil {
			t.Fatalf("count %s rows named %s: %v", probe.table, row.ID, err)
		}
		if count != probe.want {
			t.Fatalf("%s has %d row(s) in %s, want %d: the case is about plane residency and the seed did not produce it",
				row, count, probe.table, probe.want)
		}
	}
}

// requireBlockedByOpenBlocker pins a DIRECT blocked precondition together with
// its REASON — the flag, the edge, and the blocker's own open status. Asserting
// the flag alone is the shape of the retired defect: a row carrying 1 with no
// live blocker behind it means the case is testing a value nothing produced.
func (p *blockedStateProbe) requireBlockedByOpenBlocker(t *testing.T, row, blocker blockedStateRow, why string) {
	t.Helper()
	if got := p.rawBlocked(t, row); got != 1 {
		t.Fatalf("%s raw is_blocked = %d, want 1 (%s)", row, got, why)
	}
	var edges int
	//nolint:gosec // G201: the table names come from the two plane constants above.
	query := fmt.Sprintf(
		"SELECT COUNT(*) FROM %s WHERE issue_id = ? AND COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) = ? AND (type = ? OR type = ?)",
		row.Plane.edges)
	if err := p.queryScalar(p.ctx, query,
		[]any{row.ID, blocker.ID, string(types.DepBlocks), string(types.DepConditionalBlocks)}, &edges); err != nil {
		t.Fatalf("count the blocking edge %s -> %s: %v", row, blocker, err)
	}
	if edges != 1 {
		t.Fatalf("%s carries %d blocking edges onto %s, want the 1 that makes its blocked state MEAN something (%s)",
			row, edges, blocker, why)
	}
	if status := p.rawStatus(t, blocker); status == string(types.StatusClosed) || status == string(types.StatusPinned) {
		t.Fatalf("blocker %s has status %q, which blocks nothing: the precondition is a LIVE blocker (%s)", blocker, status, why)
	}
}

// requireBlockedWithNoDirectBlockerEdges pins an INHERITED blocked
// precondition. The pair is the point: the flag says blocked, the edge count
// says the block cannot be its own, so what is under test is the transitive
// propagation and nothing else. This is the exact pair the retired
// fixture-defect case lacked.
func (p *blockedStateProbe) requireBlockedWithNoDirectBlockerEdges(t *testing.T, row blockedStateRow, why string) {
	t.Helper()
	if got := p.rawBlocked(t, row); got != 1 {
		t.Fatalf("%s raw is_blocked = %d, want 1 (%s)", row, got, why)
	}
	if got := p.directBlockerEdges(t, row); got != 0 {
		t.Fatalf("%s carries %d direct blocker edges, want 0: an inherited-block case that has its own blocker tests nothing (%s)",
			row, got, why)
	}
}

func (p *blockedStateProbe) requireUnblocked(t *testing.T, row blockedStateRow, why string) {
	t.Helper()
	if got := p.rawBlocked(t, row); got != 0 {
		t.Fatalf("%s raw is_blocked = %d, want 0 (%s)", row, got, why)
	}
}

// blockedStateFlip is the falsifiability harness. It snapshots the raw
// pre-verb value of every subject and every control, and requireFlippedTo then
// refuses to pass — or even to be a meaningful assertion — unless the subjects
// MOVED to the asserted value and the controls did not move at all.
type blockedStateFlip struct {
	probe     *blockedStateProbe
	subjects  []blockedStateRow
	controls  []blockedStateRow
	blocked   map[string]int
	updatedAt map[string]string
	written   map[string]bool
}

// watchFlip records the pre-verb state. Call it immediately before the verb
// under test; every row named here is read RAW.
//
// The controls are not decoration. A control is a row of the same shape whose
// cause the verb did not remove, so it proves the reader and the seed observe
// the database the subject assertion thinks they do — the pattern
// RunBatchCloserClaimNextSeesAnUnblockingFromItsOwnBatch already uses.
func (p *blockedStateProbe) watchFlip(t *testing.T, subjects, controls []blockedStateRow) *blockedStateFlip {
	t.Helper()
	if len(subjects) == 0 {
		t.Fatal("watchFlip needs at least one subject row")
	}
	if len(controls) == 0 {
		t.Fatal("watchFlip needs at least one control row: a case with nothing that must stay put cannot tell a correct recompute from a blanket one")
	}
	flip := &blockedStateFlip{
		probe:     p,
		subjects:  subjects,
		controls:  controls,
		blocked:   make(map[string]int, len(subjects)+len(controls)),
		updatedAt: make(map[string]string, len(subjects)+len(controls)),
		written:   make(map[string]bool, 1),
	}
	for _, row := range append(append([]blockedStateRow{}, subjects...), controls...) {
		flip.blocked[row.String()] = p.rawBlocked(t, row)
		flip.updatedAt[row.String()] = p.rawUpdatedAt(t, row)
	}
	return flip
}

// watchControls records only rows that must NOT move, for the one case shape
// that has no flag of its own to flip: a verb the invariant says must not reach
// blocked state at all. Such a case still has to be falsifiable, and it is —
// but on a different column, so the case itself asserts a flip the verb DID
// make (a claimed row's status) and names that as its must-flip term.
func (p *blockedStateProbe) watchControls(t *testing.T, controls ...blockedStateRow) *blockedStateFlip {
	t.Helper()
	if len(controls) == 0 {
		t.Fatal("watchControls needs at least one control row")
	}
	flip := &blockedStateFlip{
		probe:     p,
		controls:  controls,
		blocked:   make(map[string]int, len(controls)),
		updatedAt: make(map[string]string, len(controls)),
		written:   make(map[string]bool, 1),
	}
	for _, row := range controls {
		flip.blocked[row.String()] = p.rawBlocked(t, row)
		flip.updatedAt[row.String()] = p.rawUpdatedAt(t, row)
	}
	return flip
}

// requireFlippedTo asserts the local-write clause of
// issueops.BlockedStateInvariant on the rows watchFlip snapshotted:
//
//   - every subject NOW reads want, and DID NOT read want before. The second
//     half is checked first and is fatal, because a subject that already held
//     the asserted value makes the case unfalsifiable — it would pass against a
//     backend whose verb did nothing at all.
//   - every subject's updated_at is unchanged, which is the non-perturbation
//     clause. It is asserted HERE, on rows that actually flipped, because the
//     mark and unmark templates only touch a row whose value changes: a row
//     that did not flip could not have had its timestamp bumped by a recompute,
//     so asserting it there would be asserting nothing. A subject named to
//     alsoWrites is exempt from THIS half and no other, because the verb writes
//     that row for its own reasons.
//   - every control still reads what it read, with its timestamp intact.
func (f *blockedStateFlip) requireFlippedTo(t *testing.T, want int, why string) {
	t.Helper()
	for _, row := range f.subjects {
		key := row.String()
		if f.blocked[key] == want {
			t.Fatalf("%s already read is_blocked = %d before the verb ran, so this case cannot fail on the term it is named for (%s)",
				row, want, why)
		}
		if got := f.probe.rawBlocked(t, row); got != want {
			t.Errorf("%s raw is_blocked = %d after the verb, want %d — it read %d before (%s)",
				row, got, want, f.blocked[key], why)
		}
		if f.written[key] {
			continue
		}
		if got := f.probe.rawUpdatedAt(t, row); got != f.updatedAt[key] {
			t.Errorf("%s updated_at moved %q -> %q across a blocked-state flip, want it untouched: a recompute is derived state, not a user edit (%s)",
				row, f.updatedAt[key], got, why)
		}
	}
	f.requireControlsUnmoved(t, why)
}

// alsoWrites marks a row the verb under test LEGITIMATELY WRITES. Its blocked
// flag stays fully asserted either way — a control still must not move and a
// subject still must flip, because the invariant's non-perturbation clause is
// about the flag, and a claim must not change it. Only updated_at is waived,
// because the verb moves that row by design: a claim writes the row it claims,
// and a status crossing writes the row whose status crossed.
//
// Without this the case asserts the verb did not touch the row it exists to
// touch, and passes only when both writes land in the same second: updated_at
// is DATETIME with no fractional precision. That is the same second-precision
// property that made an updated_at comparison useless as a detector elsewhere
// in this suite, and here it produced a case that passed locally and failed in
// CI on a second boundary.
func (f *blockedStateFlip) alsoWrites(rows ...blockedStateRow) *blockedStateFlip {
	for _, row := range rows {
		f.written[row.String()] = true
	}
	return f
}

// requireControlsUnmoved asserts only the control half, for the no-change cases
// that have no flip of their own to make.
func (f *blockedStateFlip) requireControlsUnmoved(t *testing.T, why string) {
	t.Helper()
	for _, row := range f.controls {
		key := row.String()
		if got := f.probe.rawBlocked(t, row); got != f.blocked[key] {
			t.Errorf("control %s raw is_blocked moved %d -> %d, want it unchanged: the verb reached outside its affected set (%s)",
				row, f.blocked[key], got, why)
		}
		if f.written[key] {
			continue
		}
		if got := f.probe.rawUpdatedAt(t, row); got != f.updatedAt[key] {
			t.Errorf("control %s updated_at moved %q -> %q, want it untouched (%s)", row, f.updatedAt[key], got, why)
		}
	}
}
