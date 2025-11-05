package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestCreate_BasicIssue(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	issue := &types.Issue{
		Title:     "Test Issue",
		Priority:  1,
		IssueType: types.TypeBug,
		Status:    types.StatusOpen,
		CreatedAt: time.Now(),
	}

	if err := s.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	issues, err := s.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("failed to search issues: %v", err)
	}

	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}

	created := issues[0]
	if created.Title != "Test Issue" {
		t.Errorf("expected title 'Test Issue', got %q", created.Title)
	}
	if created.Priority != 1 {
		t.Errorf("expected priority 1, got %d", created.Priority)
	}
	if created.IssueType != types.TypeBug {
		t.Errorf("expected type bug, got %s", created.IssueType)
	}
}

func TestCreate_WithDescription(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	issue := &types.Issue{
		Title:       "Issue with desc",
		Description: "This is a description",
		Priority:    2,
		Status:      types.StatusOpen,
		IssueType:   types.TypeTask,
		CreatedAt:   time.Now(),
	}

	if err := s.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	issues, err := s.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("failed to search issues: %v", err)
	}

	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}

	if issues[0].Description != "This is a description" {
		t.Errorf("expected description, got %q", issues[0].Description)
	}
}

func TestCreate_WithDesignAndAcceptance(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	issue := &types.Issue{
		Title:              "Feature with design",
		Design:             "Use MVC pattern",
		AcceptanceCriteria: "All tests pass",
		IssueType:          types.TypeFeature,
		Priority:           2,
		Status:             types.StatusOpen,
		CreatedAt:          time.Now(),
	}

	if err := s.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	issues, err := s.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("failed to search issues: %v", err)
	}

	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}

	created := issues[0]
	if created.Design != "Use MVC pattern" {
		t.Errorf("expected design, got %q", created.Design)
	}
	if created.AcceptanceCriteria != "All tests pass" {
		t.Errorf("expected acceptance criteria, got %q", created.AcceptanceCriteria)
	}
}

func TestCreate_WithLabels(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	issue := &types.Issue{
		Title:     "Issue with labels",
		Priority:  0,
		Status:    types.StatusOpen,
		IssueType: types.TypeBug,
		CreatedAt: time.Now(),
	}

	if err := s.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	// Add labels
	if err := s.AddLabel(ctx, issue.ID, "bug", "test"); err != nil {
		t.Fatalf("failed to add bug label: %v", err)
	}
	if err := s.AddLabel(ctx, issue.ID, "critical", "test"); err != nil {
		t.Fatalf("failed to add critical label: %v", err)
	}

	labels, err := s.GetLabels(ctx, issue.ID)
	if err != nil {
		t.Fatalf("failed to get labels: %v", err)
	}

	if len(labels) != 2 {
		t.Errorf("expected 2 labels, got %d", len(labels))
	}

	labelMap := make(map[string]bool)
	for _, l := range labels {
		labelMap[l] = true
	}

	if !labelMap["bug"] || !labelMap["critical"] {
		t.Errorf("expected labels 'bug' and 'critical', got %v", labels)
	}
}

func TestCreate_WithDependencies(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	parent := &types.Issue{
		Title:     "Parent issue",
		Priority:  1,
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
	}

	if err := s.CreateIssue(ctx, parent, "test"); err != nil {
		t.Fatalf("failed to create parent: %v", err)
	}

	child := &types.Issue{
		Title:     "Child issue",
		Priority:  1,
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
	}

	if err := s.CreateIssue(ctx, child, "test"); err != nil {
		t.Fatalf("failed to create child: %v", err)
	}

	// Add dependency
	dep := &types.Dependency{
		IssueID:     child.ID,
		DependsOnID: parent.ID,
		Type:        types.DepBlocks,
		CreatedAt:   time.Now(),
	}

	if err := s.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("failed to add dependency: %v", err)
	}

	deps, err := s.GetDependencies(ctx, child.ID)
	if err != nil {
		t.Fatalf("failed to get dependencies: %v", err)
	}

	if len(deps) != 1 {
		t.Fatalf("expected 1 dependency, got %d", len(deps))
	}

	if deps[0].ID != parent.ID {
		t.Errorf("expected dependency on %s, got %s", parent.ID, deps[0].ID)
	}
}

func TestCreate_WithDiscoveredFromDependency(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	parent := &types.Issue{
		Title:     "Parent work",
		Priority:  1,
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
	}

	if err := s.CreateIssue(ctx, parent, "test"); err != nil {
		t.Fatalf("failed to create parent: %v", err)
	}

	discovered := &types.Issue{
		Title:     "Found bug",
		Priority:  1,
		Status:    types.StatusOpen,
		IssueType: types.TypeBug,
		CreatedAt: time.Now(),
	}

	if err := s.CreateIssue(ctx, discovered, "test"); err != nil {
		t.Fatalf("failed to create discovered issue: %v", err)
	}

	// Add discovered-from dependency
	dep := &types.Dependency{
		IssueID:     discovered.ID,
		DependsOnID: parent.ID,
		Type:        types.DepDiscoveredFrom,
		CreatedAt:   time.Now(),
	}

	if err := s.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("failed to add dependency: %v", err)
	}

	deps, err := s.GetDependencies(ctx, discovered.ID)
	if err != nil {
		t.Fatalf("failed to get dependencies: %v", err)
	}

	if len(deps) != 1 {
		t.Fatalf("expected 1 dependency, got %d", len(deps))
	}

	if deps[0].ID != parent.ID {
		t.Errorf("expected dependency on %s, got %s", parent.ID, deps[0].ID)
	}
}

func TestCreate_WithExplicitID(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	issue := &types.Issue{
		ID:        "test-abc123",
		Title:     "Custom ID issue",
		Priority:  1,
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
	}

	if err := s.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	issues, err := s.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("failed to search issues: %v", err)
	}

	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}

	if issues[0].ID != "test-abc123" {
		t.Errorf("expected ID 'test-abc123', got %q", issues[0].ID)
	}
}

func TestCreate_WithAssignee(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	issue := &types.Issue{
		Title:     "Assigned issue",
		Assignee:  "alice",
		Priority:  1,
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
	}

	if err := s.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	issues, err := s.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("failed to search issues: %v", err)
	}

	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}

	if issues[0].Assignee != "alice" {
		t.Errorf("expected assignee 'alice', got %q", issues[0].Assignee)
	}
}

func TestCreate_AllIssueTypes(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	issueTypes := []types.IssueType{
		types.TypeBug,
		types.TypeFeature,
		types.TypeTask,
		types.TypeEpic,
		types.TypeChore,
	}

	for _, issueType := range issueTypes {
		issue := &types.Issue{
			Title:     "Test " + string(issueType),
			IssueType: issueType,
			Priority:  2,
			Status:    types.StatusOpen,
			CreatedAt: time.Now(),
		}

		if err := s.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("failed to create issue type %s: %v", issueType, err)
		}
	}

	issues, err := s.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("failed to search issues: %v", err)
	}

	if len(issues) != 5 {
		t.Errorf("expected 5 issues, got %d", len(issues))
	}
}

func TestCreate_MultipleDependencies(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	parent1 := &types.Issue{
		Title:     "Parent 1",
		Priority:  1,
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
	}

	parent2 := &types.Issue{
		Title:     "Parent 2",
		Priority:  1,
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
	}

	child := &types.Issue{
		Title:     "Child",
		Priority:  1,
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		CreatedAt: time.Now(),
	}

	if err := s.CreateIssue(ctx, parent1, "test"); err != nil {
		t.Fatalf("failed to create parent1: %v", err)
	}
	if err := s.CreateIssue(ctx, parent2, "test"); err != nil {
		t.Fatalf("failed to create parent2: %v", err)
	}
	if err := s.CreateIssue(ctx, child, "test"); err != nil {
		t.Fatalf("failed to create child: %v", err)
	}

	// Add multiple dependencies
	dep1 := &types.Dependency{
		IssueID:     child.ID,
		DependsOnID: parent1.ID,
		Type:        types.DepBlocks,
		CreatedAt:   time.Now(),
	}
	dep2 := &types.Dependency{
		IssueID:     child.ID,
		DependsOnID: parent2.ID,
		Type:        types.DepRelated,
		CreatedAt:   time.Now(),
	}

	if err := s.AddDependency(ctx, dep1, "test"); err != nil {
		t.Fatalf("failed to add dep1: %v", err)
	}
	if err := s.AddDependency(ctx, dep2, "test"); err != nil {
		t.Fatalf("failed to add dep2: %v", err)
	}

	deps, err := s.GetDependencies(ctx, child.ID)
	if err != nil {
		t.Fatalf("failed to get dependencies: %v", err)
	}

	if len(deps) != 2 {
		t.Fatalf("expected 2 dependencies, got %d", len(deps))
	}
}

func TestCreate_DiscoveredFromInheritsSourceRepo(t *testing.T) {
	tmpDir := t.TempDir()
	testDB := filepath.Join(tmpDir, ".beads", "beads.db")
	s := newTestStore(t, testDB)
	ctx := context.Background()

	// Create a parent issue with a custom source_repo
	parent := &types.Issue{
		Title:      "Parent issue",
		Priority:   1,
		Status:     types.StatusOpen,
		IssueType:  types.TypeTask,
		SourceRepo: "/path/to/custom/repo",
		CreatedAt:  time.Now(),
	}

	if err := s.CreateIssue(ctx, parent, "test"); err != nil {
		t.Fatalf("failed to create parent: %v", err)
	}

	// Create a discovered issue with discovered-from dependency
	// This should inherit the parent's source_repo
	discovered := &types.Issue{
		Title:     "Discovered bug",
		Priority:  1,
		Status:    types.StatusOpen,
		IssueType: types.TypeBug,
		CreatedAt: time.Now(),
	}

	// Simulate what happens in create.go when --deps discovered-from:parent is used
	// The source_repo should be inherited from the parent
	parentIssue, err := s.GetIssue(ctx, parent.ID)
	if err != nil {
		t.Fatalf("failed to get parent issue: %v", err)
	}
	if parentIssue.SourceRepo != "" {
		discovered.SourceRepo = parentIssue.SourceRepo
	}

	if err := s.CreateIssue(ctx, discovered, "test"); err != nil {
		t.Fatalf("failed to create discovered issue: %v", err)
	}

	// Add discovered-from dependency
	dep := &types.Dependency{
		IssueID:     discovered.ID,
		DependsOnID: parent.ID,
		Type:        types.DepDiscoveredFrom,
		CreatedAt:   time.Now(),
	}

	if err := s.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatalf("failed to add dependency: %v", err)
	}

	// Verify the discovered issue inherited the source_repo
	retrievedIssue, err := s.GetIssue(ctx, discovered.ID)
	if err != nil {
		t.Fatalf("failed to get discovered issue: %v", err)
	}

	if retrievedIssue.SourceRepo != parent.SourceRepo {
		t.Errorf("expected source_repo %q, got %q", parent.SourceRepo, retrievedIssue.SourceRepo)
	}

	if retrievedIssue.SourceRepo != "/path/to/custom/repo" {
		t.Errorf("expected source_repo '/path/to/custom/repo', got %q", retrievedIssue.SourceRepo)
	}
}
