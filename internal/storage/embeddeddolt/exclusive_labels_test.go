//go:build cgo

package embeddeddolt_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/labelns"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestImportJSONLData_KeepsExclusiveLabelViolations pins warn mode on the
// embedded auto-import fast path (bd-7u5ki): the exported
// labels.exclusive-prefixes config is replayed in the same transaction as the
// issues, so a historical violation must be kept for bd doctor to report
// rather than failing the whole hydration.
func TestImportJSONLData_KeepsExclusiveLabelViolations(t *testing.T) {
	te := newTestEnv(t, "ij")
	ctx := context.Background()

	issues := []*types.Issue{{
		ID:        "ij-1",
		Title:     "carries two tier labels",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Labels:    []string{"tier:fable", "tier:opus"},
	}}
	imported, err := te.store.ImportJSONLData(ctx, issues, map[string]string{labelns.ConfigKey: "tier:"}, "test")
	if err != nil {
		t.Fatalf("ImportJSONLData must not fail on an exclusive-label violation: %v", err)
	}
	if imported != 1 {
		t.Fatalf("imported = %d, want 1", imported)
	}

	labels, err := te.store.GetLabels(ctx, "ij-1")
	if err != nil {
		t.Fatalf("GetLabels: %v", err)
	}
	slices.Sort(labels)
	if !slices.Equal(labels, []string{"tier:fable", "tier:opus"}) {
		t.Fatalf("labels = %v, want both kept", labels)
	}
	raw, err := te.store.GetConfig(ctx, labelns.ConfigKey)
	if err != nil || raw != "tier:" {
		t.Fatalf("config %s = %q, %v; want the replayed value", labelns.ConfigKey, raw, err)
	}
}

// TestExclusiveLabels_CreateAndRename pins the guarded create and rename
// paths the direct and embedded routes share (bd-7u5ki): issueops.ExecuteCreate
// lets an explicit label win over an inherited one in its exclusive namespace
// and refuses a parent's inherited conflict by name, and RenameLabelInTx
// refuses a rename that would give a carrier a second label in one.
func TestExclusiveLabels_CreateAndRename(t *testing.T) {
	te := newTestEnv(t, "xl")
	ctx := context.Background()
	if err := te.store.SetConfig(ctx, labelns.ConfigKey, "tier:"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	operations, err := embeddeddolt.NewIssueOperations(te.store)
	if err != nil {
		t.Fatalf("NewIssueOperations: %v", err)
	}
	seed := func(id string, labels ...string) {
		t.Helper()
		issue := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Labels: labels}
		// Warn mode stages a violating parent the way an import can.
		if err := te.store.CreateIssuesWithFullOptions(ctx, []*types.Issue{issue}, "seed", storage.BatchCreateOptions{
			SkipPrefixValidation:       true,
			ExclusiveLabelConflictWarn: true,
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	createChild := func(id, parentID string, labels ...string) (publicops.CreateResult, error) {
		return operations.Create(ctx, publicops.CreateRequest{
			Actor:                   "writer",
			ForceIDPrefix:           true,
			Issue:                   &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Labels: labels},
			ParentID:                parentID,
			InheritLabelsFromParent: true,
		})
	}
	requireLabels := func(id string, want ...string) {
		t.Helper()
		got, err := te.store.GetLabels(ctx, id)
		if err != nil {
			t.Fatalf("GetLabels %s: %v", id, err)
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Fatalf("labels of %s = %v, want %v", id, got, want)
		}
	}

	t.Run("explicit label beats inherited", func(t *testing.T) {
		seed("xl-parent", "area:x", "tier:fable")
		result, err := createChild("xl-child", "xl-parent", "tier:opus")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		requireLabels(result.Issue.ID, "area:x", "tier:opus")
	})

	t.Run("inherited conflict names the parent", func(t *testing.T) {
		seed("xl-violator", "tier:fable", "tier:opus")
		_, err := createChild("xl-orphan", "xl-violator")
		if err == nil || !strings.Contains(err.Error(), "parent xl-violator carries tier:fable, tier:opus") ||
			!strings.Contains(err.Error(), "--no-inherit-labels") {
			t.Fatalf("expected the inherited conflict to name the parent, got %v", err)
		}
		if _, err := createChild("xl-settled", "xl-violator", "tier:opus"); err != nil {
			t.Fatalf("an explicit label settles the parent's namespace: %v", err)
		}
		requireLabels("xl-settled", "tier:opus")
	})

	t.Run("rename refuses a new violation", func(t *testing.T) {
		seed("xl-rn", "legacy", "tier:fable")
		_, _, _, err := te.store.RenameLabel(ctx, "legacy", "tier:opus", "tester")
		if err == nil || !strings.Contains(err.Error(), `xl-rn already has "tier:fable"`) {
			t.Fatalf("expected rename refusal naming xl-rn, got %v", err)
		}
		requireLabels("xl-rn", "legacy", "tier:fable")
	})
}
