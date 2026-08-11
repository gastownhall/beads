//go:build cgo

package embeddeddolt_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

func TestRunInTransactionPromotesWispAggregateAndStagesChildCounter(t *testing.T) {
	te := newTestEnv(t, "ewp")
	ctx := t.Context()
	issue := &types.Issue{
		ID: "ewp-parent", Title: "embedded runtime parent", Status: types.StatusOpen,
		Priority: 2, IssueType: types.TypeEpic, Ephemeral: true, ClosedBySession: "session-embedded",
	}
	if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	firstChild, err := te.store.GetNextChildID(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetNextChildID before promotion: %v", err)
	}
	if firstChild != issue.ID+".1" {
		t.Fatalf("first child ID = %q, want %q", firstChild, issue.ID+".1")
	}

	if err := te.store.RunInTransaction(ctx, "test: promote embedded runtime parent", func(tx storage.Transaction) error {
		return tx.UpdateIssue(ctx, issue.ID,
			map[string]interface{}{"wisp": false, "no_history": false}, "tester")
	}); err != nil {
		t.Fatalf("RunInTransaction promote: %v", err)
	}

	var issueRows, wispRows, lastChild, committedLastChild, wispCounterRows int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", []any{issue.ID}, &issueRows)
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM wisps WHERE id = ?", []any{issue.ID}, &wispRows)
	te.queryScalar(t, ctx, "SELECT last_child FROM child_counters WHERE parent_id = ?", []any{issue.ID}, &lastChild)
	te.queryScalar(t, ctx, "SELECT last_child FROM child_counters AS OF 'HEAD' WHERE parent_id = ?", []any{issue.ID}, &committedLastChild)
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM wisp_child_counters WHERE parent_id = ?", []any{issue.ID}, &wispCounterRows)
	if issueRows != 1 || wispRows != 0 || lastChild != 1 || committedLastChild != 1 || wispCounterRows != 0 {
		t.Fatalf("embedded promotion state: issues=%d wisps=%d counter=%d committed_counter=%d source_counter_rows=%d",
			issueRows, wispRows, lastChild, committedLastChild, wispCounterRows)
	}

	secondChild, err := te.store.GetNextChildID(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetNextChildID after promotion: %v", err)
	}
	if secondChild != issue.ID+".2" {
		t.Fatalf("next child ID = %q, want %q", secondChild, issue.ID+".2")
	}
	if err := te.store.UpdateIssue(ctx, issue.ID,
		map[string]interface{}{"metadata": `{"phase":"durable"}`}, "tester"); err != nil {
		t.Fatalf("later durable metadata update: %v", err)
	}
	got, err := te.store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssue after later update: %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(got.Metadata, &metadata); err != nil {
		t.Fatalf("decode later metadata: %v", err)
	}
	if metadata["phase"] != "durable" {
		t.Fatalf("later metadata = %s, want durable phase", got.Metadata)
	}
	if got.ClosedBySession != issue.ClosedBySession {
		t.Fatalf("closed_by_session = %q, want %q", got.ClosedBySession, issue.ClosedBySession)
	}
}

func TestEmbeddedCheckedUpdatePromotesPhysicalWisp(t *testing.T) {
	te := newTestEnv(t, "ewc")
	ctx := t.Context()
	issue := &types.Issue{
		ID: "ewc-checked", Title: "embedded checked runtime", Status: types.StatusOpen,
		Priority: 2, IssueType: types.TypeTask, NoHistory: true,
	}
	if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	current, err := te.store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	expectedStatus := string(types.StatusOpen)
	if err := te.store.UpdateIssueChecked(ctx, issue.ID,
		map[string]interface{}{"wisp": false, "no_history": false}, "tester",
		storage.UpdateIssueOptions{ExpectedVersion: &current.RowVersion, ExpectedStatus: &expectedStatus}); err != nil {
		t.Fatalf("UpdateIssueChecked: %v", err)
	}
	var issueRows, wispRows int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", []any{issue.ID}, &issueRows)
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM wisps WHERE id = ?", []any{issue.ID}, &wispRows)
	if issueRows != 1 || wispRows != 0 {
		t.Fatalf("checked promotion plane: issues=%d wisps=%d, want 1/0", issueRows, wispRows)
	}
}

func TestEmbeddedAutomaticPromotionCollisionRollsBackAggregate(t *testing.T) {
	te := newTestEnv(t, "ewr")
	ctx := t.Context()
	issue := &types.Issue{
		ID: "ewr-source", Title: "embedded rollback source", Status: types.StatusOpen,
		Priority: 2, IssueType: types.TypeTask, NoHistory: true,
	}
	if err := te.store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue source: %v", err)
	}
	if err := te.store.AddLabel(ctx, issue.ID, "must-remain", "tester"); err != nil {
		t.Fatalf("AddLabel source: %v", err)
	}
	if _, err := te.store.AddIssueComment(ctx, issue.ID, "tester", "source comment"); err != nil {
		t.Fatalf("AddIssueComment source: %v", err)
	}
	var collisionID string
	te.queryScalar(t, ctx, "SELECT id FROM wisp_comments WHERE issue_id = ? LIMIT 1", []any{issue.ID}, &collisionID)
	holder := &types.Issue{
		ID: "ewr-holder", Title: "collision holder", Status: types.StatusOpen,
		Priority: 2, IssueType: types.TypeTask,
	}
	if err := te.store.CreateIssue(ctx, holder, "tester"); err != nil {
		t.Fatalf("CreateIssue collision holder: %v", err)
	}
	te.exec(t, ctx, `
		INSERT INTO comments (id, issue_id, author, text, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, collisionID, holder.ID, "tester", "destination collision", time.Now().UTC())

	err := te.store.UpdateIssue(ctx, issue.ID,
		map[string]interface{}{"wisp": false, "no_history": false}, "tester")
	if err == nil || !strings.Contains(err.Error(), "copy comments for promoted wisp") {
		t.Fatalf("automatic promotion error = %v, want strict comment collision", err)
	}

	var issueRows, wispRows, sourceLabels, destinationLabels int
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", []any{issue.ID}, &issueRows)
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM wisps WHERE id = ?", []any{issue.ID}, &wispRows)
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM wisp_labels WHERE issue_id = ?", []any{issue.ID}, &sourceLabels)
	te.queryScalar(t, ctx, "SELECT COUNT(*) FROM labels WHERE issue_id = ?", []any{issue.ID}, &destinationLabels)
	if issueRows != 0 || wispRows != 1 || sourceLabels != 1 || destinationLabels != 0 {
		t.Fatalf("failed promotion changed aggregate: issues=%d wisps=%d source_labels=%d destination_labels=%d",
			issueRows, wispRows, sourceLabels, destinationLabels)
	}
	got, err := te.store.GetIssue(ctx, issue.ID)
	if err != nil {
		t.Fatalf("GetIssue source after rollback: %v", err)
	}
	if got.Ephemeral || !got.NoHistory {
		t.Fatalf("source markers after rollback = ephemeral:%t no_history:%t, want false/true",
			got.Ephemeral, got.NoHistory)
	}
}

func TestEmbeddedPromotionRefusesDualPlaneAndPreservesSource(t *testing.T) {
	te := newTestEnv(t, "ewd")
	ctx := t.Context()
	const id = "ewd-dual"
	destination := &types.Issue{
		ID: id, Title: "durable destination", Status: types.StatusOpen,
		Priority: 2, IssueType: types.TypeTask,
	}
	if err := te.store.CreateIssue(ctx, destination, "tester"); err != nil {
		t.Fatalf("CreateIssue destination: %v", err)
	}
	source := &types.Issue{
		ID: id, Title: "physical wisp source", Status: types.StatusOpen,
		Priority: 2, IssueType: types.TypeTask, Ephemeral: true,
	}
	if err := issueops.PrepareIssueForInsert(source, nil, nil); err != nil {
		t.Fatalf("prepare source wisp: %v", err)
	}
	rawDB, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL for dual-plane seed: %v", err)
	}
	tx, err := rawDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin dual-plane seed: %v", err)
	}
	if err := issueops.InsertIssueIntoTable(ctx, tx, "wisps", source); err != nil {
		_ = tx.Rollback()
		t.Fatalf("insert physical source wisp: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO wisp_labels (issue_id, label) VALUES (?, ?)", id, "source-only"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("insert source wisp label: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit dual-plane seed: %v", err)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("close dual-plane seed SQL: %v", err)
	}

	err = te.store.PromoteFromEphemeral(ctx, id, "tester")
	if err == nil || !strings.Contains(err.Error(), "destination already exists in issues") {
		t.Fatalf("dual-plane promotion error = %v, want destination collision", err)
	}
	var durableTitle, wispTitle string
	var sourceLabels int
	te.queryScalar(t, ctx, "SELECT title FROM issues WHERE id = ?", []any{id}, &durableTitle)
	te.queryScalar(t, ctx, "SELECT title FROM wisps WHERE id = ?", []any{id}, &wispTitle)
	te.queryScalar(t, ctx,
		"SELECT COUNT(*) FROM wisp_labels WHERE issue_id = ? AND label = ?", []any{id, "source-only"}, &sourceLabels)
	if durableTitle != destination.Title || wispTitle != "physical wisp source" || sourceLabels != 1 {
		t.Fatalf("dual-plane refusal changed state: issues=%q wisps=%q source_labels=%d",
			durableTitle, wispTitle, sourceLabels)
	}
}
