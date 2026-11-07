//go:build cgo

package embeddeddolt_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// These tests are the RUNTIME twin of issueops' version_completeness_test.go,
// which pins by static analysis that every issue-plane mutation reaches
// RecordVersionInTx. That guard cannot see whether the mint actually lands —
// that INSERT IGNORE reports zero affected rows for a duplicate label on this
// engine, that a closed row can still be snapshotted, that a batch create's
// deferred mint runs after its edges — so the law is exercised here once,
// flag on, on the in-process leg: every accepted mutation mints exactly one
// version row, a no-op mints none, and current_revision always equals the
// row count (FR-1, FR-2, FR-5 of #6135). It is an ENGINE CHECK on the shared
// issueops bodies, like TestEmbeddedDependencyEditorVersionsOnlyARealAddOrRemove
// beside it: the dolt and uow legs run the same functions.
//
// Every assertion is a DELTA against the issue's own version-row count, so
// the cases stay correct regardless of how many rows their seeds minted.

type versionProbe struct {
	t   *testing.T
	kit roleFixtureKit
}

func newVersionProbe(t *testing.T, te *testEnv, prefix string) versionProbe {
	t.Helper()
	configurer, ok := any(te.store).(storage.VersionedHistoryConfigurer)
	if !ok {
		t.Fatalf("%T does not implement storage.VersionedHistoryConfigurer", te.store)
	}
	configurer.SetVersionedHistoryEnabled(true)
	return versionProbe{t: t, kit: newEmbeddedRoleFixtureKit(te, prefix)}
}

func (p versionProbe) rows(id string) int {
	p.t.Helper()
	var count int
	if err := p.kit.QueryScalar(p.t.Context(), "SELECT COUNT(*) FROM issue_versions WHERE issue_id = ?", []any{id}, &count); err != nil {
		p.t.Fatalf("count issue_versions for %s: %v", id, err)
	}
	return count
}

func (p versionProbe) currentRevision(id string) int {
	p.t.Helper()
	var revision int
	if err := p.kit.QueryScalar(p.t.Context(), "SELECT current_revision FROM issues WHERE id = ?", []any{id}, &revision); err != nil {
		p.t.Fatalf("read current_revision for %s: %v", id, err)
	}
	return revision
}

// latestState decodes the highest-revision durable_state for id.
func (p versionProbe) latestState(id string) map[string]any {
	p.t.Helper()
	var raw string
	if err := p.kit.QueryScalar(p.t.Context(),
		"SELECT durable_state FROM issue_versions WHERE issue_id = ? ORDER BY revision DESC LIMIT 1",
		[]any{id}, &raw); err != nil {
		p.t.Fatalf("read latest durable_state for %s: %v", id, err)
	}
	var state map[string]any
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		p.t.Fatalf("decode durable_state for %s: %v\n%s", id, err, raw)
	}
	return state
}

// expect asserts the version-row count of id moved from before by delta and
// returns the new count, so the steps of a scenario chain.
func (p versionProbe) expect(step, id string, before, delta int) int {
	p.t.Helper()
	got := p.rows(id)
	if got != before+delta {
		p.t.Fatalf("%s: version rows for %s = %d, want %d (before %d, delta %+d)", step, id, got, before+delta, before, delta)
	}
	return got
}

// revisionMatchesRows pins FR-5 for id: current_revision is nothing but the
// count of rows RecordVersionInTx has minted.
func (p versionProbe) revisionMatchesRows(id string) {
	p.t.Helper()
	if rev, n := p.currentRevision(id), p.rows(id); rev != n {
		p.t.Fatalf("current_revision for %s = %d but it has %d version row(s)", id, rev, n)
	}
}

func dependencyTargets(t *testing.T, state map[string]any) []string {
	t.Helper()
	raw, _ := state["dependencies"].([]any)
	targets := make([]string, 0, len(raw))
	for _, dep := range raw {
		m, _ := dep.(map[string]any)
		target, _ := m["depends_on_id"].(string)
		targets = append(targets, target)
	}
	return targets
}

func labelsOf(state map[string]any) []string {
	raw, _ := state["labels"].([]any)
	labels := make([]string, 0, len(raw))
	for _, l := range raw {
		s, _ := l.(string)
		labels = append(labels, s)
	}
	return labels
}

// TestEmbeddedEveryAcceptedLifecycleMutationMintsOneVersion walks the
// lifecycle verbs the write-path inventory found unversioned — claim, release,
// label add/remove, the legacy dependency verbs, close and reopen — and pins
// one row per accepted change and none for each verb's no-op shape.
func TestEmbeddedEveryAcceptedLifecycleMutationMintsOneVersion(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "vermint")
	ctx := t.Context()
	p := newVersionProbe(t, te, "vermint")
	store := te.store

	a, b := p.kit.IssuePrefix+"-a", p.kit.IssuePrefix+"-b"
	for _, id := range []string{a, b} {
		if err := p.kit.CreateIssue(ctx, &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}, "seed"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
	}
	na := p.expect("seed", a, 0, 1)
	nb := p.expect("seed", b, 0, 1)

	// claim: one row for the CAS that wrote assignee/status/started_at; the
	// idempotent re-claim by the same actor returns without writing.
	if err := store.ClaimIssue(ctx, a, "worker"); err != nil {
		t.Fatalf("ClaimIssue: %v", err)
	}
	na = p.expect("claim", a, na, 1)
	if err := store.ClaimIssue(ctx, a, "worker"); err != nil {
		t.Fatalf("ClaimIssue (idempotent re-claim): %v", err)
	}
	na = p.expect("idempotent re-claim", a, na, 0)

	// release: assignee and status revert.
	if err := store.UnclaimIssue(ctx, a, "worker", false); err != nil {
		t.Fatalf("UnclaimIssue: %v", err)
	}
	na = p.expect("unclaim", a, na, 1)

	// labels: INSERT IGNORE on an existing label and DELETE of an absent one
	// are the no-op shapes.
	if err := store.AddLabel(ctx, a, "l1", "actor"); err != nil {
		t.Fatalf("AddLabel: %v", err)
	}
	na = p.expect("add label", a, na, 1)
	if labels := labelsOf(p.latestState(a)); len(labels) != 1 || labels[0] != "l1" {
		t.Fatalf("add label: latest durable_state labels = %v, want [l1]", labels)
	}
	if err := store.AddLabel(ctx, a, "l1", "actor"); err != nil {
		t.Fatalf("AddLabel (duplicate): %v", err)
	}
	na = p.expect("duplicate add label", a, na, 0)
	if err := store.RemoveLabel(ctx, a, "l1", "actor"); err != nil {
		t.Fatalf("RemoveLabel: %v", err)
	}
	na = p.expect("remove label", a, na, 1)
	if err := store.RemoveLabel(ctx, a, "l1", "actor"); err != nil {
		t.Fatalf("RemoveLabel (absent): %v", err)
	}
	na = p.expect("absent remove label", a, na, 0)

	// the legacy store dependency verbs (no DependencyEditor role): the
	// referencing issue versions, the target does not.
	if err := store.AddDependency(ctx, &types.Dependency{IssueID: a, DependsOnID: b, Type: types.DepBlocks}, "actor"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	na = p.expect("legacy dep add", a, na, 1)
	nb = p.expect("legacy dep add (target)", b, nb, 0)
	if targets := dependencyTargets(t, p.latestState(a)); len(targets) != 1 || targets[0] != b {
		t.Fatalf("legacy dep add: latest durable_state dependencies = %v, want [%s]", targets, b)
	}
	if err := store.RemoveDependency(ctx, a, b, "actor"); err != nil {
		t.Fatalf("RemoveDependency: %v", err)
	}
	na = p.expect("legacy dep remove", a, na, 1)
	if targets := dependencyTargets(t, p.latestState(a)); len(targets) != 0 {
		t.Fatalf("legacy dep remove: latest durable_state dependencies = %v, want none", targets)
	}

	// close, including the WithoutEvent-style idempotent already-closed
	// return, then reopen.
	if err := store.CloseIssue(ctx, a, "done", "actor", ""); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}
	na = p.expect("close", a, na, 1)
	if status, _ := p.latestState(a)["status"].(string); status != string(types.StatusClosed) {
		t.Fatalf("close: latest durable_state status = %q, want closed", status)
	}
	if err := store.CloseIssue(ctx, a, "done", "actor", ""); err != nil {
		t.Fatalf("CloseIssue (already closed): %v", err)
	}
	na = p.expect("already-closed close", a, na, 0)
	if err := store.ReopenIssue(ctx, a, "", "actor"); err != nil {
		t.Fatalf("ReopenIssue: %v", err)
	}
	p.expect("reopen", a, na, 1)

	p.revisionMatchesRows(a)
	p.revisionMatchesRows(b)
}

// TestEmbeddedMintLeavesUpdatedAtWhereHistoryOffDoes pins that minting a
// version is not itself an edit. issues.updated_at carries ON UPDATE
// CURRENT_TIMESTAMP, so a mint whose own UPDATE left it unassigned would move
// it to the clock on every versioned write, including the label and
// dependency writes that never touch it with history off. bd import's
// stale-upsert check reads that column, so a moved updated_at would also turn
// the source's next real edit away as stale. Each scenario runs with history
// off first, as the control the history-on run must match.
func TestEmbeddedMintLeavesUpdatedAtWhereHistoryOffDoes(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	// Fixed instants well before any clock this runs under, so a mint that
	// wrote the clock cannot pass for any of them.
	aged := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	sourceEdit := time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)
	laterSourceEdit := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)

	for _, history := range []struct {
		name string
		on   bool
	}{{"HistoryOff", false}, {"HistoryOn", true}} {
		t.Run(history.name, func(t *testing.T) {
			te := newTestEnv(t, "verts")
			ctx := t.Context()
			te.store.SetVersionedHistoryEnabled(history.on)
			// With history on every step below must mint exactly one version,
			// so a mint that never ran cannot pass for one that kept updated_at.
			mints := 0
			if history.on {
				mints = 1
			}

			a, b := "verts-a", "verts-b"
			for _, id := range []string{a, b} {
				if err := te.store.CreateIssue(ctx, &types.Issue{
					ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
				}, "seed"); err != nil {
					t.Fatalf("CreateIssue(%s): %v", id, err)
				}
			}
			age := func() { te.exec(t, ctx, "UPDATE issues SET updated_at = ? WHERE id = ?", aged, a) }
			versions := func() int {
				t.Helper()
				var n int
				te.queryScalar(t, ctx, "SELECT COUNT(*) FROM issue_versions WHERE issue_id = ?", []any{a}, &n)
				return n
			}
			// check asserts a's updated_at and that the step minted as the
			// history setting says, and returns the new version count.
			check := func(step string, before int, want time.Time) int {
				t.Helper()
				var got time.Time
				te.queryScalar(t, ctx, "SELECT updated_at FROM issues WHERE id = ?", []any{a}, &got)
				if !got.UTC().Equal(want) {
					t.Errorf("%s: updated_at for %s is %v, want %v", step, a, got.UTC(), want)
				}
				n := versions()
				if n != before+mints {
					t.Fatalf("%s: version rows for %s = %d, want %d", step, a, n, before+mints)
				}
				return n
			}

			// Label and dependency writes never assign updated_at themselves.
			age()
			n := versions()
			if err := te.store.AddLabel(ctx, a, "l1", "actor"); err != nil {
				t.Fatalf("AddLabel: %v", err)
			}
			n = check("add label", n, aged)
			if err := te.store.RemoveLabel(ctx, a, "l1", "actor"); err != nil {
				t.Fatalf("RemoveLabel: %v", err)
			}
			n = check("remove label", n, aged)
			if err := te.store.AddDependency(ctx, &types.Dependency{IssueID: a, DependsOnID: b, Type: types.DepBlocks}, "actor"); err != nil {
				t.Fatalf("AddDependency: %v", err)
			}
			n = check("add dependency", n, aged)
			if err := te.store.RemoveDependency(ctx, a, b, "actor"); err != nil {
				t.Fatalf("RemoveDependency: %v", err)
			}
			n = check("remove dependency", n, aged)

			// bd import's options. The upsert stores the source's own
			// updated_at, and the source's next edit is compared against it.
			// Aged again so this half does not depend on the half above.
			age()
			var rejected []string
			opts := storage.BatchCreateOptions{
				RejectStaleUpserts:             true,
				SkipDependencyValidationErrors: true,
				OnStaleRejected:                func(id string) { rejected = append(rejected, id) },
			}
			importEdit := func(title string, at time.Time) {
				t.Helper()
				if err := te.store.CreateIssuesWithFullOptions(ctx, []*types.Issue{{
					ID: a, Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
					CreatedAt: aged, UpdatedAt: at,
				}}, "importer", opts); err != nil {
					t.Fatalf("import %q: %v", title, err)
				}
				if len(rejected) != 0 {
					t.Fatalf("import %q with source updated_at %v was rejected as stale (%v)", title, at, rejected)
				}
			}
			importEdit("source edit", sourceEdit)
			n = check("import", n, sourceEdit)
			importEdit("later source edit", laterSourceEdit)
			check("later import", n, laterSourceEdit)
		})
	}
}

// TestEmbeddedBatchCreateFirstVersionCarriesCreationTimeEdges pins the
// batch-create ordering fix: the one version minted at creation is minted
// AFTER PersistDependenciesWithOptionsResult, so it carries the outgoing edge
// set, and the dependency pass mints nothing of its own.
func TestEmbeddedBatchCreateFirstVersionCarriesCreationTimeEdges(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "vercreate")
	ctx := t.Context()
	p := newVersionProbe(t, te, "vercreate")

	src, tgt := p.kit.IssuePrefix+"-src", p.kit.IssuePrefix+"-tgt"
	issues := []*types.Issue{
		{ID: src, Title: src, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
			Dependencies: []*types.Dependency{{IssueID: src, DependsOnID: tgt, Type: types.DepBlocks}}},
		{ID: tgt, Title: tgt, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	}
	if err := te.store.CreateIssues(ctx, issues, "actor"); err != nil {
		t.Fatalf("CreateIssues: %v", err)
	}
	p.expect("batch create", src, 0, 1)
	p.expect("batch create", tgt, 0, 1)
	if targets := dependencyTargets(t, p.latestState(src)); len(targets) != 1 || targets[0] != tgt {
		t.Fatalf("batch create: the first version's durable_state dependencies = %v, want [%s]: "+
			"the mint must run after the creation-time edges are persisted", targets, tgt)
	}
	p.revisionMatchesRows(src)
	p.revisionMatchesRows(tgt)
}

// TestEmbeddedGuardedUpdateMintsOnceAfterItsPatches pins the ExecuteUpdate
// ordering fix: a guarded update carrying a field edit, a label patch and a
// parent patch mints ONE version, whose durable_state carries all three, and
// a guarded update that changes nothing mints none.
func TestEmbeddedGuardedUpdateMintsOnceAfterItsPatches(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "verupd")
	ctx := t.Context()
	p := newVersionProbe(t, te, "verupd")

	child, parent := p.kit.IssuePrefix+"-child", p.kit.IssuePrefix+"-parent"
	for _, id := range []string{child, parent} {
		if err := p.kit.CreateIssue(ctx, &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}, "seed"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
	}
	lifecycle, err := te.store.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}

	n := p.rows(child)
	if _, err := lifecycle.Update(ctx, issueops.UpdateRequest{
		Actor: "actor", IssueID: child,
		Patch: issueops.IssuePatch{
			Title:    issueops.Field[string]{Set: true, Value: "renamed"},
			Labels:   issueops.LabelPatch{Add: []string{"patched"}},
			ParentID: issueops.Field[string]{Set: true, Value: parent},
		},
	}); err != nil {
		t.Fatalf("Lifecycle.Update: %v", err)
	}
	n = p.expect("guarded update with field + label + parent patches", child, n, 1)
	state := p.latestState(child)
	if title, _ := state["title"].(string); title != "renamed" {
		t.Fatalf("guarded update: durable_state title = %q, want renamed", title)
	}
	if labels := labelsOf(state); len(labels) != 1 || labels[0] != "patched" {
		t.Fatalf("guarded update: durable_state labels = %v, want [patched]: the label patch must land before the mint", labels)
	}
	if targets := dependencyTargets(t, state); len(targets) != 1 || targets[0] != parent {
		t.Fatalf("guarded update: durable_state dependencies = %v, want [%s]: the parent patch must land before the mint", targets, parent)
	}

	// The same request again changes nothing: the field is a no-op, the label
	// set already matches and the parent is already set.
	if _, err := lifecycle.Update(ctx, issueops.UpdateRequest{
		Actor: "actor", IssueID: child,
		Patch: issueops.IssuePatch{
			Title:    issueops.Field[string]{Set: true, Value: "renamed"},
			Labels:   issueops.LabelPatch{Add: []string{"patched"}},
			ParentID: issueops.Field[string]{Set: true, Value: parent},
		},
	}); err != nil {
		t.Fatalf("Lifecycle.Update (no-op): %v", err)
	}
	p.expect("no-op guarded update", child, n, 0)
	p.revisionMatchesRows(child)
	p.revisionMatchesRows(parent)
}

// TestEmbeddedFirstMintOnEmptyHistoryIsRevisionOne pins the next-revision
// computation on an issue with no issue_versions rows yet. The ungrouped
// MAX(revision) over the empty primary-key prefix (issue_id = ?) must still
// yield one row so the first mint is revision 1. On the Dolt 2.4.0-2.4.2
// engines, go-mysql-server's aggregate-over-index rewrite
// (dolthub/go-mysql-server#3907) returned zero rows for that shape, which
// failed every first mint with "sql: no rows in result set". The 2.5.x engine
// embedded mode links answers the old flat query correctly too, so this test
// pins the result, not the query form; the sqlmock pins in issueops'
// version_history_preenable_scan_test.go hold the form. The second mint pins
// that the computation still advances on a non-empty prefix, and a second
// issue's first mint pins that it counts per issue: without the issue_id
// filter it would continue the first issue's sequence.
func TestEmbeddedFirstMintOnEmptyHistoryIsRevisionOne(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "verfirst")
	ctx := t.Context()
	p := newVersionProbe(t, te, "verfirst")

	create := func(id string) {
		t.Helper()
		if err := p.kit.CreateIssue(ctx, &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}, "seed"); err != nil {
			t.Fatalf("CreateIssue(%s) on empty history: %v", id, err)
		}
	}
	maxRevision := func(id string) int {
		t.Helper()
		var rev int
		if err := p.kit.QueryScalar(ctx,
			"SELECT revision FROM issue_versions WHERE issue_id = ? ORDER BY revision DESC LIMIT 1",
			[]any{id}, &rev); err != nil {
			t.Fatalf("read latest revision for %s: %v", id, err)
		}
		return rev
	}

	id := p.kit.IssuePrefix + "-a"
	create(id)
	if got := maxRevision(id); got != 1 {
		t.Fatalf("first mint revision = %d, want 1", got)
	}
	p.expect("first mint", id, 0, 1)
	p.revisionMatchesRows(id)

	if err := te.store.AddLabel(ctx, id, "l1", "actor"); err != nil {
		t.Fatalf("AddLabel: %v", err)
	}
	if got := maxRevision(id); got != 2 {
		t.Fatalf("second mint revision = %d, want 2", got)
	}
	p.revisionMatchesRows(id)

	other := p.kit.IssuePrefix + "-b"
	create(other)
	if got := maxRevision(other); got != 1 {
		t.Fatalf("first mint revision for a second issue = %d, want 1 (revisions count per issue)", got)
	}
	p.expect("second issue's first mint", other, 0, 1)
	p.revisionMatchesRows(other)
}
