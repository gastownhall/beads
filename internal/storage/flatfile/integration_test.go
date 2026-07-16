package flatfile

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestFullWorkflow exercises the complete issue lifecycle:
// create epic → create children → add deps → add labels → add comments →
// close children → verify ready work → close epic.
func TestFullWorkflow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// 1. Create an epic.
	epic := &types.Issue{ID: "wf-epic", Title: "Q3 Roadmap", IssueType: "epic"}
	if err := s.CreateIssue(ctx, epic, "pm"); err != nil {
		t.Fatalf("create epic: %v", err)
	}

	// 2. Create child tasks.
	tasks := []*types.Issue{
		{ID: "wf-1", Title: "Design API", Priority: 1, IssueType: "task"},
		{ID: "wf-2", Title: "Implement API", Priority: 1, IssueType: "task"},
		{ID: "wf-3", Title: "Write tests", Priority: 2, IssueType: "task"},
	}
	for _, task := range tasks {
		if err := s.CreateIssue(ctx, task, "dev"); err != nil {
			t.Fatalf("create %s: %v", task.ID, err)
		}
		// Parent-child dep
		s.AddDependency(ctx, &types.Dependency{IssueID: task.ID, DependsOnID: "wf-epic", Type: "parent-child"}, "dev")
	}

	// 3. Add blocking deps: wf-2 blocked by wf-1, wf-3 blocked by wf-2.
	s.AddDependency(ctx, &types.Dependency{IssueID: "wf-2", DependsOnID: "wf-1", Type: "blocks"}, "dev")
	s.AddDependency(ctx, &types.Dependency{IssueID: "wf-3", DependsOnID: "wf-2", Type: "blocks"}, "dev")

	// 4. Add labels.
	s.AddLabel(ctx, "wf-1", "api", "dev")
	s.AddLabel(ctx, "wf-2", "api", "dev")
	s.AddLabel(ctx, "wf-3", "testing", "dev")

	// 5. Add comments.
	s.AddIssueComment(ctx, "wf-1", "pm", "This is the first priority")
	s.AddIssueComment(ctx, "wf-1", "dev", "On it")

	// 6. Verify ready work: only wf-1 and wf-epic should be ready (wf-2, wf-3 blocked).
	ready, err := s.GetReadyWork(ctx, types.WorkFilter{})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	readyIDs := make(map[string]bool)
	for _, r := range ready {
		readyIDs[r.ID] = true
	}
	if !readyIDs["wf-1"] {
		t.Error("wf-1 should be ready")
	}
	if readyIDs["wf-2"] {
		t.Error("wf-2 should be blocked")
	}
	if readyIDs["wf-3"] {
		t.Error("wf-3 should be blocked")
	}

	// 7. Close wf-1 → wf-2 should become ready.
	s.CloseIssue(ctx, "wf-1", "done", "dev", "sess-1")
	ready2, _ := s.GetReadyWork(ctx, types.WorkFilter{})
	readyIDs2 := make(map[string]bool)
	for _, r := range ready2 {
		readyIDs2[r.ID] = true
	}
	if !readyIDs2["wf-2"] {
		t.Error("wf-2 should be ready after wf-1 closed")
	}
	if readyIDs2["wf-3"] {
		t.Error("wf-3 should still be blocked")
	}

	// 8. Close wf-2 and wf-3.
	s.CloseIssue(ctx, "wf-2", "done", "dev", "sess-2")
	s.CloseIssue(ctx, "wf-3", "done", "dev", "sess-3")

	// 9. Epic should be eligible for closure.
	epics, err := s.GetEpicsEligibleForClosure(ctx)
	if err != nil {
		t.Fatalf("GetEpicsEligibleForClosure: %v", err)
	}
	found := false
	for _, e := range epics {
		if e.Epic.ID == "wf-epic" && e.EligibleForClose {
			found = true
		}
	}
	if !found {
		t.Error("wf-epic should be eligible for closure")
	}

	// 10. Verify search.
	apiIssues, _ := s.SearchIssues(ctx, "API", types.IssueFilter{})
	if len(apiIssues) != 2 {
		t.Errorf("search 'API': got %d, want 2", len(apiIssues))
	}

	// 11. Verify label search.
	apiLabeled, _ := s.GetIssuesByLabel(ctx, "api")
	if len(apiLabeled) != 2 {
		t.Errorf("GetIssuesByLabel 'api': got %d, want 2", len(apiLabeled))
	}

	// 12. Verify comments.
	comments, _ := s.GetIssueComments(ctx, "wf-1")
	if len(comments) != 2 {
		t.Errorf("comments on wf-1: got %d, want 2", len(comments))
	}

	// 13. Verify statistics.
	stats, err := s.GetStatistics(ctx)
	if err != nil {
		t.Fatalf("GetStatistics: %v", err)
	}
	if stats.TotalIssues != 4 {
		t.Errorf("total issues = %d, want 4", stats.TotalIssues)
	}
	if stats.ClosedIssues != 3 {
		t.Errorf("closed = %d, want 3", stats.ClosedIssues)
	}

	// 14. Verify config round-trip.
	s.SetConfig(ctx, "test.key", "test.value")
	val, _ := s.GetConfig(ctx, "test.key")
	if val != "test.value" {
		t.Errorf("config round-trip: got %q, want %q", val, "test.value")
	}

	// 15. Verify transaction.
	err = s.RunInTransaction(ctx, "test tx", func(tx storage.Transaction) error {
		return tx.CreateIssue(ctx, &types.Issue{ID: "wf-tx", Title: "From transaction"}, "txer")
	})
	if err != nil {
		t.Fatalf("RunInTransaction: %v", err)
	}
	txIssue, err := s.GetIssue(ctx, "wf-tx")
	if err != nil {
		t.Fatalf("GetIssue after tx: %v", err)
	}
	if txIssue.Title != "From transaction" {
		t.Errorf("tx issue title = %q", txIssue.Title)
	}
}

// TestMigrationRoundTrip simulates migrating data from one store to another.
func TestMigrationRoundTrip(t *testing.T) {
	// Source store with test data.
	src := newTestStore(t)
	ctx := context.Background()

	// Populate source.
	src.CreateIssue(ctx, &types.Issue{ID: "mig-1", Title: "First", Priority: 0, IssueType: "bug"}, "alice")
	src.CreateIssue(ctx, &types.Issue{ID: "mig-2", Title: "Second", Priority: 1, IssueType: "task"}, "bob")
	src.AddDependency(ctx, &types.Dependency{IssueID: "mig-2", DependsOnID: "mig-1", Type: "blocks"}, "bob")
	src.AddLabel(ctx, "mig-1", "urgent", "alice")
	src.AddLabel(ctx, "mig-1", "security", "alice")
	src.AddIssueComment(ctx, "mig-1", "alice", "Found this in prod")
	src.SetConfig(ctx, "issue_prefix", "mig")
	src.SetConfig(ctx, "custom_key", "custom_value")

	// Export all data from source.
	srcIssues, _ := src.SearchIssues(ctx, "", types.IssueFilter{})
	srcConfig, _ := src.GetAllConfig(ctx)

	// Create destination store.
	dst := newTestStore(t)

	// Migrate issues with their labels and deps.
	for _, issue := range srcIssues {
		labels, _ := src.GetLabels(ctx, issue.ID)
		issue.Labels = labels
		deps, _ := src.GetDependencyRecords(ctx, issue.ID)
		issue.Dependencies = deps

		if err := dst.CreateIssue(ctx, issue, "migrate"); err != nil {
			t.Fatalf("migrate create %s: %v", issue.ID, err)
		}
	}

	// Migrate comments.
	for _, issue := range srcIssues {
		comments, _ := src.GetIssueComments(ctx, issue.ID)
		for _, c := range comments {
			dst.ImportIssueComment(ctx, c.IssueID, c.Author, c.Text, c.CreatedAt)
		}
	}

	// Migrate config.
	for k, v := range srcConfig {
		dst.SetConfig(ctx, k, v)
	}

	// Verify destination matches source.
	dstIssues, _ := dst.SearchIssues(ctx, "", types.IssueFilter{})
	if len(dstIssues) != len(srcIssues) {
		t.Fatalf("issue count: src=%d dst=%d", len(srcIssues), len(dstIssues))
	}

	// Verify labels migrated.
	dstLabels, _ := dst.GetLabels(ctx, "mig-1")
	if len(dstLabels) != 2 {
		t.Errorf("mig-1 labels: got %d, want 2", len(dstLabels))
	}

	// Verify deps migrated.
	dstDeps, _ := dst.GetDependencies(ctx, "mig-2")
	if len(dstDeps) != 1 || dstDeps[0].ID != "mig-1" {
		t.Errorf("mig-2 deps: got %v, want [mig-1]", dstDeps)
	}

	// Verify comments migrated.
	dstComments, _ := dst.GetIssueComments(ctx, "mig-1")
	if len(dstComments) != 1 {
		t.Errorf("mig-1 comments: got %d, want 1", len(dstComments))
	}

	// Verify config migrated.
	dstPrefix, _ := dst.GetConfig(ctx, "issue_prefix")
	if dstPrefix != "mig" {
		t.Errorf("config issue_prefix: got %q, want %q", dstPrefix, "mig")
	}
	dstCustom, _ := dst.GetConfig(ctx, "custom_key")
	if dstCustom != "custom_value" {
		t.Errorf("config custom_key: got %q, want %q", dstCustom, "custom_value")
	}

	// Verify ready work is consistent.
	srcReady, _ := src.GetReadyWork(ctx, types.WorkFilter{})
	dstReady, _ := dst.GetReadyWork(ctx, types.WorkFilter{})
	if len(srcReady) != len(dstReady) {
		t.Errorf("ready work: src=%d dst=%d", len(srcReady), len(dstReady))
	}
}
