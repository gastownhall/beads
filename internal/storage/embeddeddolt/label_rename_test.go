//go:build cgo

package embeddeddolt_test

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// TestRenameLabel gives EmbeddedDoltStore.RenameLabel the same first-party
// coverage AddLabel/RemoveLabel already carry (bda-c77s sibling finding: the
// embedded impl had zero tests while DoltStore.RenameLabel has a dedicated
// suite). The deep merge/wisp/event semantics live in issueops and are pinned
// by internal/storage/dolt/label_rename_test.go against the same shared
// implementation; these cases pin the embedded WIRING - the store method
// reaches RenameLabelInTx and reports its counts through withConn.
func TestRenameLabel(t *testing.T) {
	skipUnlessEmbeddedDolt(t)

	t.Run("basic", func(t *testing.T) {
		te := newTestEnv(t, "rl")
		ctx := t.Context()

		for _, id := range []string{"rl-a", "rl-b"} {
			issue := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
			if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
				t.Fatalf("CreateIssue(%s): %v", id, err)
			}
			if err := te.store.AddLabel(ctx, id, "backend", "tester"); err != nil {
				t.Fatalf("AddLabel(%s): %v", id, err)
			}
		}

		renamed, merged, ids, err := te.store.RenameLabel(ctx, "backend", "server", "tester")
		if err != nil {
			t.Fatalf("RenameLabel: %v", err)
		}
		if renamed != 2 {
			t.Errorf("renamed = %d, want 2", renamed)
		}
		if merged != 0 {
			t.Errorf("merged = %d, want 0", merged)
		}
		if len(ids) != 2 {
			t.Errorf("ids = %v, want 2 entries", ids)
		}
		for _, id := range []string{"rl-a", "rl-b"} {
			labels, err := te.store.GetLabels(ctx, id)
			if err != nil {
				t.Fatalf("GetLabels(%s): %v", id, err)
			}
			if len(labels) != 1 || labels[0] != "server" {
				t.Errorf("GetLabels(%s) = %v, want [server]", id, labels)
			}
		}
	})

	t.Run("merge", func(t *testing.T) {
		te := newTestEnv(t, "rlm")
		ctx := t.Context()

		issue := &types.Issue{ID: "rlm-both", Title: "Both", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		for _, l := range []string{"wip", "in-progress"} {
			if err := te.store.AddLabel(ctx, "rlm-both", l, "tester"); err != nil {
				t.Fatalf("AddLabel(%s): %v", l, err)
			}
		}

		renamed, merged, _, err := te.store.RenameLabel(ctx, "wip", "in-progress", "tester")
		if err != nil {
			t.Fatalf("RenameLabel: %v", err)
		}
		if renamed != 1 {
			t.Errorf("renamed = %d, want 1", renamed)
		}
		if merged != 1 {
			t.Errorf("merged = %d, want 1", merged)
		}
		labels, err := te.store.GetLabels(ctx, "rlm-both")
		if err != nil {
			t.Fatalf("GetLabels: %v", err)
		}
		if len(labels) != 1 || labels[0] != "in-progress" {
			t.Errorf("GetLabels = %v, want exactly [in-progress]", labels)
		}
	})

	t.Run("zero-carrier-no-op", func(t *testing.T) {
		te := newTestEnv(t, "rlz")
		ctx := t.Context()

		renamed, merged, ids, err := te.store.RenameLabel(ctx, "nobody-has-this", "target", "tester")
		if err != nil {
			t.Fatalf("RenameLabel: %v", err)
		}
		if renamed != 0 || merged != 0 || len(ids) != 0 {
			t.Errorf("renamed=%d merged=%d ids=%v, want all zero/empty", renamed, merged, ids)
		}
	})

	t.Run("same-name-refused", func(t *testing.T) {
		te := newTestEnv(t, "rls")
		ctx := t.Context()

		_, _, _, err := te.store.RenameLabel(ctx, "x", "x", "tester")
		if !errors.Is(err, issueops.ErrRenameLabelSameName) {
			t.Errorf("err = %v, want ErrRenameLabelSameName", err)
		}
	})
}

// TestRenameLabelMintsOneVersionPerTouchedBead pins the versioned-history law
// (#6135 FR-1/FR-2/FR-5) for a rename. Every bead whose label set the rename
// changed mints exactly one version row - a plain rename and a merge alike,
// since both drop oldLabel. A bead that never carried oldLabel mints none, a
// rename nobody carries mints none, and current_revision stays equal to the
// row count. issueops' TestEveryBeadMutatorMintsOrIsExempt only proves
// statically that RenameLabelInTx reaches RecordVersionInTx; this is its
// runtime twin, on the same probe TestEmbeddedEveryAcceptedLifecycleMutationMintsOneVersion uses.
func TestRenameLabelMintsOneVersionPerTouchedBead(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "rlver")
	ctx := t.Context()
	p := newVersionProbe(t, te, "rlver")

	plain := p.kit.IssuePrefix + "-plain"
	merge := p.kit.IssuePrefix + "-merge"
	untouched := p.kit.IssuePrefix + "-untouched"
	seed := map[string][]string{
		plain:     {"old"},
		merge:     {"old", "new"},
		untouched: {"other"},
	}
	for _, id := range []string{plain, merge, untouched} {
		if err := p.kit.CreateIssue(ctx, &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}, "seed"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
		for _, label := range seed[id] {
			if err := te.store.AddLabel(ctx, id, label, "seed"); err != nil {
				t.Fatalf("AddLabel(%s, %s): %v", id, label, err)
			}
		}
	}
	np, nm, nu := p.rows(plain), p.rows(merge), p.rows(untouched)

	renamed, merged, _, err := te.store.RenameLabel(ctx, "old", "new", "actor")
	if err != nil {
		t.Fatalf("RenameLabel: %v", err)
	}
	if renamed != 2 || merged != 1 {
		t.Fatalf("RenameLabel counts = (renamed %d, merged %d), want (2, 1)", renamed, merged)
	}
	np = p.expect("rename (plain)", plain, np, 1)
	nm = p.expect("rename (merge)", merge, nm, 1)
	nu = p.expect("rename (untouched)", untouched, nu, 0)
	for _, id := range []string{plain, merge} {
		if labels := labelsOf(p.latestState(id)); len(labels) != 1 || labels[0] != "new" {
			t.Fatalf("rename: latest durable_state labels for %s = %v, want [new]", id, labels)
		}
	}

	// The no-op shape: nothing carries oldLabel any more.
	renamed, _, _, err = te.store.RenameLabel(ctx, "old", "new", "actor")
	if err != nil {
		t.Fatalf("RenameLabel (no carriers): %v", err)
	}
	if renamed != 0 {
		t.Fatalf("RenameLabel (no carriers): renamed = %d, want 0", renamed)
	}
	p.expect("no-op rename (plain)", plain, np, 0)
	p.expect("no-op rename (merge)", merge, nm, 0)
	p.expect("no-op rename (untouched)", untouched, nu, 0)
	for _, id := range []string{plain, merge, untouched} {
		p.revisionMatchesRows(id)
	}
}
