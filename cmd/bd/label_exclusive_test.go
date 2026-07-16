package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/labelns"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// newExclusiveLabelStore returns a test store with tier: and review:
// configured as exclusive label namespaces (bd-7u5ki).
func newExclusiveLabelStore(t *testing.T) *dolt.DoltStore {
	t.Helper()
	st := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")
	if err := st.SetConfig(context.Background(), labelns.ConfigKey, "tier:,review"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	return st
}

func mustCreateLabeledIssue(t *testing.T, st *dolt.DoltStore, labels ...string) *types.Issue {
	t.Helper()
	issue := &types.Issue{Title: "x", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, Labels: labels}
	if err := st.CreateIssue(context.Background(), issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	return issue
}

func TestExclusiveLabels_AddLabelRejected(t *testing.T) {
	ctx := context.Background()
	st := newExclusiveLabelStore(t)
	issue := mustCreateLabeledIssue(t, st, "tier:fable")

	err := st.AddLabel(ctx, issue.ID, "tier:opus", "tester")
	if err == nil || !strings.Contains(err.Error(), "exclusive") {
		t.Fatalf("expected exclusive-namespace rejection, got %v", err)
	}
	if !strings.Contains(err.Error(), "tier:fable") {
		t.Fatalf("error should name the existing label, got %v", err)
	}

	// Re-adding the same label stays idempotent.
	if err := st.AddLabel(ctx, issue.ID, "tier:fable", "tester"); err != nil {
		t.Fatalf("re-adding existing label: %v", err)
	}
	// The colon-less config form ("review") normalizes to review: and enforces.
	if err := st.AddLabel(ctx, issue.ID, "review:fable", "tester"); err != nil {
		t.Fatalf("first review: label: %v", err)
	}
	if err := st.AddLabel(ctx, issue.ID, "review:opus", "tester"); err == nil {
		t.Fatal("expected second review: label to be rejected")
	}
	// Labels outside exclusive namespaces stay free-form.
	if err := st.AddLabel(ctx, issue.ID, "area:x", "tester"); err != nil {
		t.Fatalf("non-namespaced label: %v", err)
	}
	if err := st.AddLabel(ctx, issue.ID, "area:y", "tester"); err != nil {
		t.Fatalf("second non-namespaced label: %v", err)
	}
}

func TestExclusiveLabels_UnconfiguredKeepsFreeFormLabels(t *testing.T) {
	ctx := context.Background()
	st := newTestStoreWithPrefix(t, filepath.Join(t.TempDir(), "test.db"), "test")
	issue := mustCreateLabeledIssue(t, st, "tier:fable")

	if err := st.AddLabel(ctx, issue.ID, "tier:opus", "tester"); err != nil {
		t.Fatalf("unconfigured workspace must accept both labels: %v", err)
	}
	labels, _ := st.GetLabels(ctx, issue.ID)
	if len(labels) != 2 {
		t.Fatalf("expected 2 labels, got %v", labels)
	}
}

func TestExclusiveLabels_CreateWithConflictRejected(t *testing.T) {
	ctx := context.Background()
	st := newExclusiveLabelStore(t)

	issue := &types.Issue{Title: "x", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		Labels: []string{"tier:fable", "tier:opus"}}
	err := st.CreateIssue(ctx, issue, "tester")
	if err == nil || !strings.Contains(err.Error(), "exclusive") {
		t.Fatalf("expected create to reject conflicting labels, got %v", err)
	}
}

func TestExclusiveLabels_UpdateSwapsInOneCommand(t *testing.T) {
	ctx := context.Background()
	st := newExclusiveLabelStore(t)
	issue := mustCreateLabeledIssue(t, st, "tier:fable")

	// --remove-label tier:fable --add-label tier:opus in one update: removes
	// must run before adds or the add trips the exclusivity guard.
	if err := applyLabelUpdates(ctx, st, issue.ID, "tester", nil, []string{"tier:opus"}, []string{"tier:fable"}); err != nil {
		t.Fatalf("applyLabelUpdates swap: %v", err)
	}
	labels, _ := st.GetLabels(ctx, issue.ID)
	if len(labels) != 1 || labels[0] != "tier:opus" {
		t.Fatalf("expected [tier:opus], got %v", labels)
	}
}

func TestExclusiveLabels_ReplaceTxFuncSwaps(t *testing.T) {
	ctx := context.Background()
	st := newExclusiveLabelStore(t)
	issue := mustCreateLabeledIssue(t, st, "tier:fable")

	txFunc := exclusiveReplaceAddTxFunc([]string{"tier:", "review:"})
	err := st.RunInTransaction(ctx, "", func(tx storage.Transaction) error {
		return txFunc(ctx, tx, issue.ID, "tier:opus", "tester")
	})
	if err != nil {
		t.Fatalf("replace add: %v", err)
	}
	labels, _ := st.GetLabels(ctx, issue.ID)
	if len(labels) != 1 || labels[0] != "tier:opus" {
		t.Fatalf("expected [tier:opus], got %v", labels)
	}

	// Without prefixes (no --replace), the same helper is a plain add and the
	// storage guard rejects the conflict.
	plain := exclusiveReplaceAddTxFunc(nil)
	err = st.RunInTransaction(ctx, "", func(tx storage.Transaction) error {
		return plain(ctx, tx, issue.ID, "tier:fable", "tester")
	})
	if err == nil || !strings.Contains(err.Error(), "exclusive") {
		t.Fatalf("expected rejection without --replace, got %v", err)
	}
}

func TestExclusiveLabels_ImportWarnsAndKeepsLabels(t *testing.T) {
	ctx := context.Background()
	st := newExclusiveLabelStore(t)

	issue := &types.Issue{ID: "test-imp1", Title: "imported", Status: types.StatusOpen, Priority: 2,
		IssueType: types.TypeTask, Labels: []string{"tier:fable", "tier:opus"}}
	var reported [][3]string
	err := st.CreateIssuesWithFullOptions(ctx, []*types.Issue{issue}, "importer", storage.BatchCreateOptions{
		OrphanHandling:             storage.OrphanAllow,
		SkipPrefixValidation:       true,
		ExclusiveLabelConflictWarn: true,
		OnExclusiveLabelConflict: func(issueID, prefix string, labels []string) {
			reported = append(reported, [3]string{issueID, prefix, strings.Join(labels, ",")})
		},
	})
	if err != nil {
		t.Fatalf("import-style create must warn, not fail: %v", err)
	}
	if len(reported) == 0 {
		t.Fatal("expected the violation to be reported through the callback")
	}
	if reported[0][0] != "test-imp1" || reported[0][1] != "tier:" {
		t.Fatalf("unexpected report: %v", reported[0])
	}
	labels, _ := st.GetLabels(ctx, "test-imp1")
	if len(labels) != 2 {
		t.Fatalf("import must keep violating labels (no silent data loss), got %v", labels)
	}
}

func TestFindExclusiveLabelViolations(t *testing.T) {
	ctx := context.Background()
	st := newExclusiveLabelStore(t)
	prefixes := []string{"tier:", "review:"}

	// A violating issue (written import-style, bypassing the write guard),
	// a clean issue, and a closed violating issue that must be skipped.
	violating := &types.Issue{ID: "test-v1", Title: "v", Status: types.StatusOpen, Priority: 2,
		IssueType: types.TypeTask, Labels: []string{"tier:fable", "tier:opus"}}
	closed := &types.Issue{ID: "test-v2", Title: "c", Status: types.StatusClosed, Priority: 2,
		IssueType: types.TypeTask, Labels: []string{"tier:fable", "tier:opus"}}
	if err := st.CreateIssuesWithFullOptions(ctx, []*types.Issue{violating, closed}, "importer", storage.BatchCreateOptions{
		OrphanHandling:             storage.OrphanAllow,
		SkipPrefixValidation:       true,
		ExclusiveLabelConflictWarn: true,
	}); err != nil {
		t.Fatalf("seed violations: %v", err)
	}
	mustCreateLabeledIssue(t, st, "tier:fable", "area:x")

	violations, err := issueops.FindExclusiveLabelViolations(ctx, st.UnderlyingDB(), prefixes)
	if err != nil {
		t.Fatalf("FindExclusiveLabelViolations: %v", err)
	}
	if len(violations) != 1 {
		t.Fatalf("expected exactly 1 violation, got %v", violations)
	}
	v := violations[0]
	if v.IssueID != "test-v1" || v.Prefix != "tier:" || len(v.Labels) != 2 {
		t.Fatalf("unexpected violation: %+v", v)
	}

	// No prefixes configured: nothing to scan.
	none, err := issueops.FindExclusiveLabelViolations(ctx, st.UnderlyingDB(), nil)
	if err != nil || none != nil {
		t.Fatalf("expected nil, nil for empty prefixes, got %v, %v", none, err)
	}
}

func TestDropConflictingInheritedLabels(t *testing.T) {
	prefixes := []string{"tier:"}

	got := dropConflictingInheritedLabels([]string{"tier:opus"}, []string{"tier:fable", "area:x"}, prefixes)
	if len(got) != 1 || got[0] != "area:x" {
		t.Fatalf("explicit tier: label should evict inherited one, got %v", got)
	}
	got = dropConflictingInheritedLabels([]string{"area:y"}, []string{"tier:fable"}, prefixes)
	if len(got) != 1 || got[0] != "tier:fable" {
		t.Fatalf("inherited label without explicit conflict should survive, got %v", got)
	}
	got = dropConflictingInheritedLabels([]string{"tier:opus"}, []string{"tier:fable"}, nil)
	if len(got) != 1 || got[0] != "tier:fable" {
		t.Fatalf("no exclusive prefixes: inheritance unchanged, got %v", got)
	}
}
