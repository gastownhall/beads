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
