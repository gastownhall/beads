package dolt

import (
	"context"
	"testing"

	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

func TestIssueOperationsUpdateDualResidentUsesOneCanonicalPlane(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const (
		id       = "role-dual-update"
		targetID = "role-dual-update-target"
	)
	seedRoleDualIssue(t, ctx, store, id, types.StatusOpen, types.StatusOpen)
	if err := store.CreateIssue(ctx, crossTierRegularIssue(targetID, "dependency target"), "tester"); err != nil {
		t.Fatalf("CreateIssue dependency target: %v", err)
	}
	if err := store.AddLabel(ctx, id, "canonical-wisp-label", "tester"); err != nil {
		t.Fatalf("AddLabel canonical wisp: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: id, DependsOnID: targetID, Type: types.DepRelated,
	}, "tester"); err != nil {
		t.Fatalf("AddDependency canonical wisp: %v", err)
	}
	var expected int64
	if err := store.db.QueryRowContext(ctx, "SELECT row_lock FROM wisps WHERE id = ?", id).Scan(&expected); err != nil {
		t.Fatalf("read canonical wisp version: %v", err)
	}
	enableJournalForTest(t, store)
	clearJournal(t, store)

	lifecycle, err := store.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle: %v", err)
	}
	result, err := lifecycle.Update(ctx, publicops.UpdateRequest{
		Actor: "tester", IssueID: id, ExpectedVersion: &expected,
		Patch: publicops.IssuePatch{
			Title: publicops.Field[string]{Set: true, Value: "canonical wisp updated"},
			Labels: publicops.LabelPatch{Replace: publicops.Field[[]string]{
				Set: true, Value: []string{"replacement-wisp-label"},
			}},
		},
	})
	if err != nil {
		t.Fatalf("Lifecycle.Update dual canonical wisp: %v", err)
	}
	if !result.Changed || result.Issue == nil || result.Issue.Title != "canonical wisp updated" || !result.Issue.Ephemeral {
		t.Fatalf("Update result = %+v, want changed canonical wisp", result)
	}
	assertStringSet(t, result.Issue.Labels, []string{"replacement-wisp-label"})
	if len(result.Issue.Dependencies) != 1 || result.Issue.Dependencies[0].DependsOnID != targetID {
		t.Fatalf("Update result dependencies = %+v, want canonical wisp edge to %s", result.Issue.Dependencies, targetID)
	}
	assertRoleDualRows(t, ctx, store, id, "durable twin", types.StatusOpen, "canonical wisp updated", types.StatusOpen)
	assertLabelsInTable(t, ctx, store, "labels", id, []string{"durable-label"})
	assertLabelsInTable(t, ctx, store, "wisp_labels", id, []string{"replacement-wisp-label"})
	assertLatestJournalIssueSnapshot(t, ctx, store, id, "canonical wisp updated", true)
}

func TestIssueOperationsCloseDualResidentUsesCanonicalPolicyAndSnapshot(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const (
		id      = "role-dual-close"
		childID = "role-dual-close-durable-child"
	)
	if err := store.CreateIssue(ctx, crossTierRegularIssue(id, "durable twin"), "tester"); err != nil {
		t.Fatalf("CreateIssue durable twin: %v", err)
	}
	if err := store.AddLabel(ctx, id, "durable-label", "tester"); err != nil {
		t.Fatalf("AddLabel durable twin: %v", err)
	}
	if err := store.CreateIssue(ctx, crossTierRegularIssue(childID, "durable child"), "tester"); err != nil {
		t.Fatalf("CreateIssue durable child: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: childID, DependsOnID: id, Type: types.DepParentChild,
	}, "tester"); err != nil {
		t.Fatalf("seed durable child edge: %v", err)
	}
	seedRoleWispTwin(t, ctx, store, id, types.StatusOpen)
	if err := store.AddLabel(ctx, id, "canonical-wisp-label", "tester"); err != nil {
		t.Fatalf("AddLabel canonical wisp: %v", err)
	}
	var expected int64
	if err := store.db.QueryRowContext(ctx, "SELECT row_lock FROM wisps WHERE id = ?", id).Scan(&expected); err != nil {
		t.Fatalf("read canonical wisp version: %v", err)
	}
	enableJournalForTest(t, store)
	clearJournal(t, store)

	lifecycle, err := store.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle: %v", err)
	}
	result, err := lifecycle.Close(ctx, publicops.CloseRequest{
		Actor: "tester", IssueID: id, Reason: "done", ExpectedVersion: &expected,
	})
	if err != nil {
		t.Fatalf("Lifecycle.Close canonical wisp with durable-twin child: %v", err)
	}
	if !result.Changed || result.OpenChildren != 0 || result.Issue == nil ||
		result.Issue.Title != "canonical wisp" || !result.Issue.Ephemeral || result.Issue.Status != types.StatusClosed {
		t.Fatalf("Close result = %+v, want closed canonical wisp with no open children", result)
	}
	assertStringSet(t, result.Issue.Labels, []string{"canonical-wisp-label"})
	assertRoleDualRows(t, ctx, store, id, "durable twin", types.StatusOpen, "canonical wisp", types.StatusClosed)
	assertLatestJournalIssueSnapshot(t, ctx, store, id, "canonical wisp", true)
}

func TestIssueOperationsReopenDualResidentReturnsCanonicalSnapshot(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const id = "role-dual-reopen"
	seedRoleDualIssue(t, ctx, store, id, types.StatusOpen, types.StatusClosed)
	if err := store.AddLabel(ctx, id, "canonical-wisp-label", "tester"); err != nil {
		t.Fatalf("AddLabel canonical wisp: %v", err)
	}
	var expected int64
	if err := store.db.QueryRowContext(ctx, "SELECT row_lock FROM wisps WHERE id = ?", id).Scan(&expected); err != nil {
		t.Fatalf("read canonical wisp version: %v", err)
	}
	enableJournalForTest(t, store)
	clearJournal(t, store)

	lifecycle, err := store.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle: %v", err)
	}
	result, err := lifecycle.Reopen(ctx, publicops.ReopenRequest{
		Actor: "tester", IssueID: id, Reason: "retry", ExpectedVersion: &expected,
	})
	if err != nil {
		t.Fatalf("Lifecycle.Reopen dual canonical wisp: %v", err)
	}
	if !result.Changed || result.Issue == nil || result.Issue.Title != "canonical wisp" ||
		!result.Issue.Ephemeral || result.Issue.Status != types.StatusOpen {
		t.Fatalf("Reopen result = %+v, want reopened canonical wisp", result)
	}
	assertStringSet(t, result.Issue.Labels, []string{"canonical-wisp-label"})
	assertRoleDualRows(t, ctx, store, id, "durable twin", types.StatusOpen, "canonical wisp", types.StatusOpen)
	assertLatestJournalIssueSnapshot(t, ctx, store, id, "canonical wisp", true)
}

func TestIssueOperationsDemotionPublishesProvenanceCascade(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const id = "role-demote-provenance-cascade"
	if err := store.CreateIssue(ctx, crossTierRegularIssue(id, "durable with provenance"), "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	ref, refKind := "work-123", "work-id"
	if _, inserted, err := store.RecordProvenanceEvent(ctx, types.ProvenanceEvent{
		IssueID: id, Kind: types.ProvUsed, Source: "test",
		Ref: &ref, RefKind: &refKind,
	}); err != nil || !inserted {
		t.Fatalf("RecordProvenanceEvent inserted=%t err=%v", inserted, err)
	}

	lifecycle, err := store.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle: %v", err)
	}
	result, err := lifecycle.Update(ctx, publicops.UpdateRequest{
		Actor: "tester", IssueID: id,
		Patch: publicops.IssuePatch{Persistence: publicops.Field[publicops.PersistenceMode]{
			Set: true, Value: publicops.PersistenceModeEphemeral,
		}},
	})
	if err != nil {
		t.Fatalf("Lifecycle.Update persistence demotion: %v", err)
	}
	if !result.Changed || result.Issue == nil || !result.Issue.Ephemeral {
		t.Fatalf("demotion result = %+v, want changed wisp", result)
	}
	var working, head int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM provenance_events WHERE issue_id = ?", id,
	).Scan(&working); err != nil {
		t.Fatalf("read working provenance: %v", err)
	}
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM provenance_events AS OF 'HEAD' WHERE issue_id = ?", id,
	).Scan(&head); err != nil {
		t.Fatalf("read HEAD provenance: %v", err)
	}
	if working != 0 || head != 0 {
		t.Fatalf("provenance after demotion = working:%d HEAD:%d, want 0/0", working, head)
	}
	requireCleanTables(ctx, t, store, "issues", "provenance_events")
}

func seedRoleDualIssue(t *testing.T, ctx context.Context, store *DoltStore, id string, durableStatus, wispStatus types.Status) {
	t.Helper()
	durable := crossTierRegularIssue(id, "durable twin")
	durable.Status = durableStatus
	if err := store.CreateIssue(ctx, durable, "tester"); err != nil {
		t.Fatalf("CreateIssue durable twin: %v", err)
	}
	if err := store.AddLabel(ctx, id, "durable-label", "tester"); err != nil {
		t.Fatalf("AddLabel durable twin: %v", err)
	}
	seedRoleWispTwin(t, ctx, store, id, wispStatus)
}

func seedRoleWispTwin(t *testing.T, ctx context.Context, store *DoltStore, id string, status types.Status) {
	t.Helper()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx for wisp twin: %v", err)
	}
	if err := storageissueops.InsertIssueStrictInTx(ctx, tx, "wisps", &types.Issue{
		ID: id, Title: "canonical wisp", Status: status,
		Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
	}); err != nil {
		_ = tx.Rollback()
		t.Fatalf("seed wisp twin: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit wisp twin: %v", err)
	}
}

func assertRoleDualRows(t *testing.T, ctx context.Context, store *DoltStore, id, durableTitle string, durableStatus types.Status, wispTitle string, wispStatus types.Status) {
	t.Helper()
	var gotDurableTitle, gotWispTitle string
	var gotDurableStatus, gotWispStatus types.Status
	if err := store.db.QueryRowContext(ctx, "SELECT title, status FROM issues WHERE id = ?", id).Scan(&gotDurableTitle, &gotDurableStatus); err != nil {
		t.Fatalf("read durable twin: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, "SELECT title, status FROM wisps WHERE id = ?", id).Scan(&gotWispTitle, &gotWispStatus); err != nil {
		t.Fatalf("read wisp twin: %v", err)
	}
	if gotDurableTitle != durableTitle || gotDurableStatus != durableStatus || gotWispTitle != wispTitle || gotWispStatus != wispStatus {
		t.Fatalf("dual rows durable=(%q,%s) wisp=(%q,%s), want (%q,%s) and (%q,%s)",
			gotDurableTitle, gotDurableStatus, gotWispTitle, gotWispStatus,
			durableTitle, durableStatus, wispTitle, wispStatus)
	}
}

func assertLabelsInTable(t *testing.T, ctx context.Context, store *DoltStore, table, id string, want []string) {
	t.Helper()
	rows, err := store.db.QueryContext(ctx, "SELECT label FROM "+table+" WHERE issue_id = ? ORDER BY label", id)
	if err != nil {
		t.Fatalf("read labels from %s: %v", table, err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			t.Fatalf("scan label from %s: %v", table, err)
		}
		got = append(got, label)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate labels from %s: %v", table, err)
	}
	assertStringSet(t, got, want)
}

func assertStringSet(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("strings = %v, want %v", got, want)
	}
	seen := make(map[string]int, len(got))
	for _, value := range got {
		seen[value]++
	}
	for _, value := range want {
		if seen[value] == 0 {
			t.Fatalf("strings = %v, want %v", got, want)
		}
		seen[value]--
	}
}
