package linear

import (
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestGenerateIssueIDs(t *testing.T) {
	// Create test issues without IDs
	issues := []*types.Issue{
		{
			Title:       "First issue",
			Description: "Description 1",
			CreatedAt:   time.Now(),
		},
		{
			Title:       "Second issue",
			Description: "Description 2",
			CreatedAt:   time.Now().Add(-time.Hour),
		},
		{
			Title:       "Third issue",
			Description: "Description 3",
			CreatedAt:   time.Now().Add(-2 * time.Hour),
		},
	}

	// Generate IDs
	err := GenerateIssueIDs(issues, "test", "linear-import", IDGenerationOptions{})
	if err != nil {
		t.Fatalf("GenerateIssueIDs failed: %v", err)
	}

	// Verify all issues have IDs
	for i, issue := range issues {
		if issue.ID == "" {
			t.Errorf("Issue %d has empty ID", i)
		}
		// Verify prefix
		if !hasPrefix(issue.ID, "test-") {
			t.Errorf("Issue %d ID '%s' doesn't have prefix 'test-'", i, issue.ID)
		}
	}

	// Verify all IDs are unique
	seen := make(map[string]bool)
	for i, issue := range issues {
		if seen[issue.ID] {
			t.Errorf("Duplicate ID found: %s (issue %d)", issue.ID, i)
		}
		seen[issue.ID] = true
	}
}

func TestGenerateIssueIDsPreservesExisting(t *testing.T) {
	existingID := "test-existing"
	issues := []*types.Issue{
		{
			ID:          existingID,
			Title:       "Existing issue",
			Description: "Has an ID already",
			CreatedAt:   time.Now(),
		},
		{
			Title:       "New issue",
			Description: "Needs an ID",
			CreatedAt:   time.Now(),
		},
	}

	err := GenerateIssueIDs(issues, "test", "linear-import", IDGenerationOptions{})
	if err != nil {
		t.Fatalf("GenerateIssueIDs failed: %v", err)
	}

	// First issue should keep its original ID
	if issues[0].ID != existingID {
		t.Errorf("Existing ID was changed: got %s, want %s", issues[0].ID, existingID)
	}

	// Second issue should have a new ID
	if issues[1].ID == "" {
		t.Error("Second issue has empty ID")
	}
	if issues[1].ID == existingID {
		t.Error("Second issue has same ID as first (collision)")
	}
}

func TestGenerateIssueIDsNoDuplicates(t *testing.T) {
	// Create issues with identical content - should still get unique IDs
	now := time.Now()
	issues := []*types.Issue{
		{
			Title:       "Same title",
			Description: "Same description",
			CreatedAt:   now,
		},
		{
			Title:       "Same title",
			Description: "Same description",
			CreatedAt:   now,
		},
	}

	err := GenerateIssueIDs(issues, "bd", "linear-import", IDGenerationOptions{})
	if err != nil {
		t.Fatalf("GenerateIssueIDs failed: %v", err)
	}

	// Both should have IDs
	if issues[0].ID == "" || issues[1].ID == "" {
		t.Error("One or both issues have empty IDs")
	}

	// IDs should be different (nonce handles collision)
	if issues[0].ID == issues[1].ID {
		t.Errorf("Both issues have same ID: %s", issues[0].ID)
	}
}

func TestNormalizeIssueForLinearHashCanonicalizesExternalRef(t *testing.T) {
	slugged := "https://linear.app/crown-dev/issue/BEA-93/updated-title-for-beads"
	canonical := "https://linear.app/crown-dev/issue/BEA-93"
	issue := &types.Issue{
		Title:       "Title",
		Description: "Description",
		ExternalRef: &slugged,
	}

	normalized := NormalizeIssueForLinearHash(issue)
	if normalized.ExternalRef == nil {
		t.Fatal("expected external_ref to be present")
	}
	if *normalized.ExternalRef != canonical {
		t.Fatalf("expected canonical external_ref %q, got %q", canonical, *normalized.ExternalRef)
	}
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func TestDefaultMappingConfig(t *testing.T) {
	config := DefaultMappingConfig()

	// Check priority mappings
	if config.PriorityMap["0"] != 4 {
		t.Errorf("PriorityMap[0] = %d, want 4", config.PriorityMap["0"])
	}
	if config.PriorityMap["1"] != 0 {
		t.Errorf("PriorityMap[1] = %d, want 0", config.PriorityMap["1"])
	}

	// Check state mappings
	if config.StateMap["backlog"] != "open" {
		t.Errorf("StateMap[backlog] = %s, want open", config.StateMap["backlog"])
	}
	if config.StateMap["started"] != "in_progress" {
		t.Errorf("StateMap[started] = %s, want in_progress", config.StateMap["started"])
	}
	if config.StateMap["completed"] != "closed" {
		t.Errorf("StateMap[completed] = %s, want closed", config.StateMap["completed"])
	}

	// Check label type mappings
	if config.LabelTypeMap["bug"] != "bug" {
		t.Errorf("LabelTypeMap[bug] = %s, want bug", config.LabelTypeMap["bug"])
	}
	if config.LabelTypeMap["feature"] != "feature" {
		t.Errorf("LabelTypeMap[feature] = %s, want feature", config.LabelTypeMap["feature"])
	}
	if config.LabelTypeMap["decision"] != "decision" {
		t.Errorf("LabelTypeMap[decision] = %s, want decision", config.LabelTypeMap["decision"])
	}
	if config.LabelTypeMap["spike"] != "spike" {
		t.Errorf("LabelTypeMap[spike] = %s, want spike", config.LabelTypeMap["spike"])
	}
	if config.LabelTypeMap["story"] != "story" {
		t.Errorf("LabelTypeMap[story] = %s, want story", config.LabelTypeMap["story"])
	}
	if config.LabelTypeMap["milestone"] != "milestone" {
		t.Errorf("LabelTypeMap[milestone] = %s, want milestone", config.LabelTypeMap["milestone"])
	}

	// Check relation mappings
	if config.RelationMap["blocks"] != "blocks" {
		t.Errorf("RelationMap[blocks] = %s, want blocks", config.RelationMap["blocks"])
	}
}

func TestPriorityToBeads(t *testing.T) {
	config := DefaultMappingConfig()

	tests := []struct {
		linearPriority int
		want           int
	}{
		{0, 4}, // No priority -> Backlog
		{1, 0}, // Urgent -> Critical
		{2, 1}, // High -> High
		{3, 2}, // Medium -> Medium
		{4, 3}, // Low -> Low
		{5, 2}, // Unknown -> Medium (default)
	}

	for _, tt := range tests {
		got := PriorityToBeads(tt.linearPriority, config)
		if got != tt.want {
			t.Errorf("PriorityToBeads(%d) = %d, want %d", tt.linearPriority, got, tt.want)
		}
	}
}

func TestPriorityToLinear(t *testing.T) {
	config := DefaultMappingConfig()

	tests := []struct {
		beadsPriority int
		want          int
	}{
		{0, 1}, // Critical -> Urgent
		{1, 2}, // High -> High
		{2, 3}, // Medium -> Medium
		{3, 4}, // Low -> Low
		{4, 0}, // Backlog -> No priority
		{5, 3}, // Unknown -> Medium (default)
	}

	for _, tt := range tests {
		got := PriorityToLinear(tt.beadsPriority, config)
		if got != tt.want {
			t.Errorf("PriorityToLinear(%d) = %d, want %d", tt.beadsPriority, got, tt.want)
		}
	}
}

func TestStateToBeadsStatus(t *testing.T) {
	config := DefaultMappingConfig()

	tests := []struct {
		state *State
		want  types.Status
	}{
		{nil, types.StatusOpen},
		{&State{Type: "backlog", Name: "Backlog"}, types.StatusOpen},
		{&State{Type: "unstarted", Name: "Todo"}, types.StatusOpen},
		{&State{Type: "started", Name: "In Progress"}, types.StatusInProgress},
		{&State{Type: "completed", Name: "Done"}, types.StatusClosed},
		{&State{Type: "canceled", Name: "Cancelled"}, types.StatusClosed},
		{&State{Type: "unknown", Name: "Unknown"}, types.StatusOpen}, // Default
	}

	for _, tt := range tests {
		got := StateToBeadsStatus(tt.state, config)
		if got != tt.want {
			stateName := "nil"
			if tt.state != nil {
				stateName = tt.state.Type
			}
			t.Errorf("StateToBeadsStatus(%s) = %v, want %v", stateName, got, tt.want)
		}
	}
}

func TestParseBeadsStatus(t *testing.T) {
	tests := []struct {
		input string
		want  types.Status
	}{
		{"open", types.StatusOpen},
		{"OPEN", types.StatusOpen},
		{"in_progress", types.StatusInProgress},
		{"in-progress", types.StatusInProgress},
		{"inprogress", types.StatusInProgress},
		{"blocked", types.StatusBlocked},
		{"closed", types.StatusClosed},
		{"CLOSED", types.StatusClosed},
		{"done", types.StatusClosed},
		{"Done", types.StatusClosed},
		{"deferred", types.StatusDeferred},
		{"pinned", types.StatusPinned},
		{"hooked", types.StatusHooked},
		{"unknown", types.StatusOpen}, // Default
	}

	for _, tt := range tests {
		got := ParseBeadsStatus(tt.input)
		if got != tt.want {
			t.Errorf("ParseBeadsStatus(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestStatusToLinearStateType(t *testing.T) {
	tests := []struct {
		status types.Status
		want   string
	}{
		{types.StatusOpen, "unstarted"},
		{types.StatusInProgress, "started"},
		{types.StatusBlocked, "started"},
		{types.StatusClosed, "completed"},
		{types.Status("unknown"), "unstarted"}, // Unknown -> default
	}

	for _, tt := range tests {
		got := StatusToLinearStateType(tt.status)
		if got != tt.want {
			t.Errorf("StatusToLinearStateType(%v) = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestLabelToIssueType(t *testing.T) {
	config := DefaultMappingConfig()

	tests := []struct {
		labels *Labels
		want   types.IssueType
	}{
		{nil, types.TypeTask},
		{&Labels{Nodes: []Label{}}, types.TypeTask},
		{&Labels{Nodes: []Label{{Name: "bug"}}}, types.TypeBug},
		{&Labels{Nodes: []Label{{Name: "Bug"}}}, types.TypeBug},
		{&Labels{Nodes: []Label{{Name: "feature"}}}, types.TypeFeature},
		{&Labels{Nodes: []Label{{Name: "epic"}}}, types.TypeEpic},
		{&Labels{Nodes: []Label{{Name: "chore"}}}, types.TypeChore},
		{&Labels{Nodes: []Label{{Name: "decision"}}}, types.TypeDecision},
		{&Labels{Nodes: []Label{{Name: "spike"}}}, types.TypeSpike},
		{&Labels{Nodes: []Label{{Name: "story"}}}, types.TypeStory},
		{&Labels{Nodes: []Label{{Name: "milestone"}}}, types.TypeMilestone},
		{&Labels{Nodes: []Label{{Name: "random"}, {Name: "bug"}}}, types.TypeBug},
		{&Labels{Nodes: []Label{{Name: "contains-bug-keyword"}}}, types.TypeBug},
		{&Labels{Nodes: []Label{{Name: "history"}}}, types.TypeTask},
		{&Labels{Nodes: []Label{{Name: "decision-log"}}}, types.TypeTask},
		{&Labels{Nodes: []Label{{Name: "spike-detector"}}}, types.TypeTask},
		{&Labels{Nodes: []Label{{Name: "storytime"}}}, types.TypeTask},
		{&Labels{Nodes: []Label{{Name: "pre-milestone"}}}, types.TypeTask},
	}

	for _, tt := range tests {
		got := LabelToIssueType(tt.labels, config)
		if got != tt.want {
			t.Errorf("LabelToIssueType(%v) = %v, want %v", tt.labels, got, tt.want)
		}
	}
}

func TestLabelToIssueTypeCustomStoryAliasIsExactOnly(t *testing.T) {
	config := DefaultMappingConfig()
	config.LabelTypeMap["user-story"] = "story"

	tests := []struct {
		label string
		want  types.IssueType
	}{
		{"user-story", types.TypeStory},
		{"backend-user-story", types.TypeTask},
	}

	for _, tt := range tests {
		labels := &Labels{Nodes: []Label{{Name: tt.label}}}
		got := LabelToIssueType(labels, config)
		if got != tt.want {
			t.Errorf("LabelToIssueType(%q) = %v, want %v", tt.label, got, tt.want)
		}
	}
}

func TestParseIssueType(t *testing.T) {
	tests := []struct {
		input string
		want  types.IssueType
	}{
		{"bug", types.TypeBug},
		{"BUG", types.TypeBug},
		{"feature", types.TypeFeature},
		{"task", types.TypeTask},
		{"epic", types.TypeEpic},
		{"chore", types.TypeChore},
		{"decision", types.TypeDecision},
		{"spike", types.TypeSpike},
		{"story", types.TypeStory},
		{"milestone", types.TypeMilestone},
		{"unknown", types.TypeTask}, // Default
	}

	for _, tt := range tests {
		got := ParseIssueType(tt.input)
		if got != tt.want {
			t.Errorf("ParseIssueType(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestRelationToBeadsDep(t *testing.T) {
	config := DefaultMappingConfig()

	tests := []struct {
		relationType string
		want         string
	}{
		{"blocks", "blocks"},
		{"blockedBy", "blocks"},
		{"duplicate", "duplicates"},
		{"related", "related"},
		{"unknown", "related"}, // Default
	}

	for _, tt := range tests {
		got := RelationToBeadsDep(tt.relationType, config)
		if got != tt.want {
			t.Errorf("RelationToBeadsDep(%q) = %q, want %q", tt.relationType, got, tt.want)
		}
	}
}

func TestIssueToBeads(t *testing.T) {
	config := DefaultMappingConfig()

	linearIssue := &Issue{
		ID:          "uuid-123",
		Identifier:  "PROJ-123",
		Title:       "Test Issue",
		Description: "Test description",
		URL:         "https://linear.app/team/issue/PROJ-123/test-issue",
		Priority:    2, // High
		State:       &State{Type: "started", Name: "In Progress"},
		Assignee:    &User{Name: "John Doe", Email: "john@example.com"},
		Labels:      &Labels{Nodes: []Label{{Name: "bug"}}},
		CreatedAt:   "2024-01-15T10:00:00Z",
		UpdatedAt:   "2024-01-16T12:00:00Z",
	}

	result := IssueToBeads(linearIssue, config)
	issue := result.Issue.(*types.Issue)

	if issue.Title != "Test Issue" {
		t.Errorf("Title = %q, want %q", issue.Title, "Test Issue")
	}
	if issue.Description != "Test description" {
		t.Errorf("Description = %q, want %q", issue.Description, "Test description")
	}
	if issue.Priority != 1 { // High in beads
		t.Errorf("Priority = %d, want 1", issue.Priority)
	}
	if issue.Status != types.StatusInProgress {
		t.Errorf("Status = %v, want %v", issue.Status, types.StatusInProgress)
	}
	if issue.Assignee != "john@example.com" {
		t.Errorf("Assignee = %q, want %q", issue.Assignee, "john@example.com")
	}
	if issue.IssueType != types.TypeBug {
		t.Errorf("IssueType = %v, want %v", issue.IssueType, types.TypeBug)
	}
	if issue.ExternalRef == nil {
		t.Error("ExternalRef should not be nil")
	}
}

func TestIssueToBeadsWithParent(t *testing.T) {
	config := DefaultMappingConfig()

	linearIssue := &Issue{
		ID:          "uuid-456",
		Identifier:  "PROJ-456",
		Title:       "Child Issue",
		Description: "Child description",
		URL:         "https://linear.app/team/issue/PROJ-456",
		Priority:    3,
		State:       &State{Type: "unstarted", Name: "Todo"},
		Parent:      &Parent{ID: "uuid-123", Identifier: "PROJ-123"},
		CreatedAt:   "2024-01-15T10:00:00Z",
		UpdatedAt:   "2024-01-16T12:00:00Z",
	}

	result := IssueToBeads(linearIssue, config)

	if len(result.Dependencies) != 1 {
		t.Fatalf("Expected 1 dependency, got %d", len(result.Dependencies))
	}
	if result.Dependencies[0].Type != "parent-child" {
		t.Errorf("Dependency type = %q, want %q", result.Dependencies[0].Type, "parent-child")
	}
	if result.Dependencies[0].FromLinearID != "PROJ-456" {
		t.Errorf("FromLinearID = %q, want %q", result.Dependencies[0].FromLinearID, "PROJ-456")
	}
	if result.Dependencies[0].ToLinearID != "PROJ-123" {
		t.Errorf("ToLinearID = %q, want %q", result.Dependencies[0].ToLinearID, "PROJ-123")
	}
	if result.Dependencies[0].Source != DependencySourceParent {
		t.Errorf("Source = %q, want %q", result.Dependencies[0].Source, DependencySourceParent)
	}
}

func TestIssueToBeadsRelationMappedToParentChildKeepsRelationSource(t *testing.T) {
	config := DefaultMappingConfig()
	config.RelationMap["related"] = "parent-child"

	linearIssue := &Issue{
		ID:         "uuid-456",
		Identifier: "PROJ-456",
		Title:      "Related Issue",
		URL:        "https://linear.app/team/issue/PROJ-456",
		Priority:   3,
		State:      &State{Type: "unstarted", Name: "Todo"},
		Relations: &Relations{Nodes: []Relation{
			{
				ID:   "rel-1",
				Type: "related",
				RelatedIssue: struct {
					ID         string `json:"id"`
					Identifier string `json:"identifier"`
				}{ID: "uuid-789", Identifier: "PROJ-789"},
			},
		}},
		CreatedAt: "2024-01-15T10:00:00Z",
		UpdatedAt: "2024-01-16T12:00:00Z",
	}

	result := IssueToBeads(linearIssue, config)

	if len(result.Dependencies) != 1 {
		t.Fatalf("Expected 1 dependency, got %d", len(result.Dependencies))
	}
	dep := result.Dependencies[0]
	if dep.Type != "parent-child" {
		t.Errorf("Dependency type = %q, want %q", dep.Type, "parent-child")
	}
	if dep.Source != DependencySourceRelation {
		t.Errorf("Source = %q, want %q", dep.Source, DependencySourceRelation)
	}
}

func TestBuildLinearToLocalUpdates(t *testing.T) {
	config := DefaultMappingConfig()

	linearIssue := &Issue{
		ID:          "uuid-123",
		Identifier:  "PROJ-123",
		Title:       "Updated Title",
		Description: "Updated description",
		Priority:    1, // Urgent
		State:       &State{Type: "completed", Name: "Done"},
		Assignee:    &User{Name: "Jane Doe", Email: "jane@example.com"},
		Labels:      &Labels{Nodes: []Label{{Name: "feature"}, {Name: "priority"}}},
		UpdatedAt:   "2024-01-20T15:00:00Z",
		CompletedAt: "2024-01-20T14:00:00Z",
	}

	updates := BuildLinearToLocalUpdates(linearIssue, config)

	if updates["title"] != "Updated Title" {
		t.Errorf("title = %v, want %q", updates["title"], "Updated Title")
	}
	if updates["description"] != "Updated description" {
		t.Errorf("description = %v, want %q", updates["description"], "Updated description")
	}
	if updates["priority"] != 0 { // Critical in beads
		t.Errorf("priority = %v, want 0", updates["priority"])
	}
	if updates["status"] != "closed" {
		t.Errorf("status = %v, want %q", updates["status"], "closed")
	}
	if updates["assignee"] != "jane@example.com" {
		t.Errorf("assignee = %v, want %q", updates["assignee"], "jane@example.com")
	}

	labels, ok := updates["labels"].([]string)
	if !ok || len(labels) != 2 {
		t.Errorf("labels = %v, want 2 labels", updates["labels"])
	}
}

func TestBuildLinearToLocalUpdatesNoAssignee(t *testing.T) {
	config := DefaultMappingConfig()

	linearIssue := &Issue{
		ID:          "uuid-123",
		Identifier:  "PROJ-123",
		Title:       "No Assignee",
		Description: "Test",
		Priority:    3,
		State:       &State{Type: "unstarted", Name: "Todo"},
		Assignee:    nil,
		UpdatedAt:   "2024-01-20T15:00:00Z",
	}

	updates := BuildLinearToLocalUpdates(linearIssue, config)

	if updates["assignee"] != "" {
		t.Errorf("assignee = %v, want empty string", updates["assignee"])
	}
}

// mockConfigLoader implements ConfigLoader for testing
type mockConfigLoader struct {
	config map[string]string
}

func (m *mockConfigLoader) GetAllConfig() (map[string]string, error) {
	return m.config, nil
}

func TestLoadMappingConfig(t *testing.T) {
	loader := &mockConfigLoader{
		config: map[string]string{
			"linear.priority_map.0":       "3",
			"linear.state_map.custom":     "in_progress",
			"linear.label_type_map.story": "feature",
			"linear.relation_map.parent":  "parent-child",
			"status.custom":               "review:active",
		},
	}

	config := LoadMappingConfig(loader)

	// Check custom priority mapping
	if config.PriorityMap["0"] != 3 {
		t.Errorf("PriorityMap[0] = %d, want 3", config.PriorityMap["0"])
	}

	// Check custom state mapping
	if config.StateMap["custom"] != "in_progress" {
		t.Errorf("StateMap[custom] = %s, want in_progress", config.StateMap["custom"])
	}
	if config.ExplicitStateMap["custom"] != "in_progress" {
		t.Errorf("ExplicitStateMap[custom] = %s, want in_progress", config.ExplicitStateMap["custom"])
	}

	// Check custom label type mapping
	if config.LabelTypeMap["story"] != "feature" {
		t.Errorf("LabelTypeMap[story] = %s, want feature", config.LabelTypeMap["story"])
	}

	// Check custom relation mapping
	if config.RelationMap["parent"] != "parent-child" {
		t.Errorf("RelationMap[parent] = %s, want parent-child", config.RelationMap["parent"])
	}

	// Check that defaults are preserved
	if config.StateMap["started"] != "in_progress" {
		t.Errorf("StateMap[started] = %s, want in_progress (default preserved)", config.StateMap["started"])
	}

	if len(config.CustomStatuses) != 1 || config.CustomStatuses[0].Name != "review" {
		t.Fatalf("CustomStatuses = %#v, want one entry named review", config.CustomStatuses)
	}
}

func TestLoadMappingConfigDottedStateMapResolvesPushByName(t *testing.T) {
	loader := &mockConfigLoader{
		config: map[string]string{
			"linear.state_map.done": "closed",
		},
	}
	config := LoadMappingConfig(loader)
	cache := &StateCache{
		States: []State{
			{ID: "state-done", Name: "Done", Type: "completed"},
			{ID: "state-monitoring", Name: "Monitoring", Type: "completed"},
		},
	}

	got, err := ResolveStateIDForBeadsStatus(cache, types.StatusClosed, config)
	if err != nil {
		t.Fatalf("ResolveStateIDForBeadsStatus() error = %v", err)
	}
	if got != "state-done" {
		t.Fatalf("ResolveStateIDForBeadsStatus() = %q, want state-done", got)
	}
}

func TestLoadMappingConfigNilLoader(t *testing.T) {
	config := LoadMappingConfig(nil)

	// Should return defaults
	if config.PriorityMap["1"] != 0 {
		t.Errorf("Expected default priority map with nil loader")
	}
}

func TestBuildLinearDescription(t *testing.T) {
	tests := []struct {
		name  string
		issue *types.Issue
		want  string
	}{
		{
			name:  "description only",
			issue: &types.Issue{Description: "Basic description"},
			want:  "Basic description",
		},
		{
			name: "with acceptance criteria",
			issue: &types.Issue{
				Description:        "Main description",
				AcceptanceCriteria: "- Must do X\n- Must do Y",
			},
			want: "Main description\n\n## Acceptance Criteria\n- Must do X\n- Must do Y",
		},
		{
			name: "with all fields",
			issue: &types.Issue{
				Description:        "Main description",
				AcceptanceCriteria: "AC here",
				Design:             "Design notes",
				Notes:              "Additional notes",
			},
			want: "Main description\n\n## Acceptance Criteria\nAC here\n\n## Design\nDesign notes\n\n## Notes\nAdditional notes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildLinearDescription(tt.issue)
			if got != tt.want {
				t.Errorf("BuildLinearDescription() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveStateIDForBeadsStatusRequiresExplicitMappings(t *testing.T) {
	cache := &StateCache{
		States: []State{
			{ID: "state-1", Name: "Todo", Type: "unstarted"},
		},
	}

	_, err := ResolveStateIDForBeadsStatus(cache, types.StatusOpen, DefaultMappingConfig())
	if err == nil {
		t.Fatal("expected missing explicit state map to fail")
	}
	if !strings.Contains(err.Error(), "linear.state_map is not configured") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveStateIDForBeadsStatusRejectsAmbiguousTypeFallback(t *testing.T) {
	cache := &StateCache{
		States: []State{
			{ID: "state-1", Name: "Done", Type: "completed"},
			{ID: "state-2", Name: "Monitoring", Type: "completed"},
		},
	}
	config := DefaultMappingConfig()
	config.ExplicitStateMap["completed"] = "closed"

	_, err := ResolveStateIDForBeadsStatus(cache, types.StatusClosed, config)
	if err == nil {
		t.Fatal("expected ambiguous completed mapping to fail")
	}
	if got := err.Error(); !strings.Contains(got, "type fallback is ambiguous") || !strings.Contains(got, "Done, Monitoring") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveStateIDForBeadsStatusPrefersExplicitStateName(t *testing.T) {
	cache := &StateCache{
		States: []State{
			{ID: "state-1", Name: "Done", Type: "completed"},
			{ID: "state-2", Name: "Monitoring", Type: "completed"},
		},
	}
	config := DefaultMappingConfig()
	config.ExplicitStateMap["done"] = "closed"

	got, err := ResolveStateIDForBeadsStatus(cache, types.StatusClosed, config)
	if err != nil {
		t.Fatalf("ResolveStateIDForBeadsStatus() error = %v", err)
	}
	if got != "state-1" {
		t.Fatalf("ResolveStateIDForBeadsStatus() = %q, want state-1", got)
	}
}

func TestResolveStateIDForBeadsStatusOutboundOverrideResolvesAmbiguousType(t *testing.T) {
	// Reproduces the be-9gt scenario: Linear's default workspace has two
	// "started"-type states. Without an outbound override the push would error
	// with the ambiguity message; with one it resolves cleanly.
	cache := &StateCache{
		States: []State{
			{ID: "state-progress", Name: "In Progress", Type: "started"},
			{ID: "state-review", Name: "In Review", Type: "started"},
		},
	}
	config := DefaultMappingConfig()
	config.ExplicitStateMap["started"] = "in_progress"
	config.OutboundStateMap["in_progress"] = "In Progress"

	got, err := ResolveStateIDForBeadsStatus(cache, types.StatusInProgress, config)
	if err != nil {
		t.Fatalf("ResolveStateIDForBeadsStatus() error = %v", err)
	}
	if got != "state-progress" {
		t.Fatalf("ResolveStateIDForBeadsStatus() = %q, want state-progress", got)
	}
}

func TestResolveStateIDForBeadsStatusOutboundOverrideIsCaseInsensitive(t *testing.T) {
	cache := &StateCache{
		States: []State{
			{ID: "state-progress", Name: "In Progress", Type: "started"},
		},
	}
	config := DefaultMappingConfig()
	config.ExplicitStateMap["started"] = "in_progress"
	config.OutboundStateMap["in_progress"] = "  in progress  " // mixed case, padding

	got, err := ResolveStateIDForBeadsStatus(cache, types.StatusInProgress, config)
	if err != nil {
		t.Fatalf("ResolveStateIDForBeadsStatus() error = %v", err)
	}
	if got != "state-progress" {
		t.Fatalf("ResolveStateIDForBeadsStatus() = %q, want state-progress", got)
	}
}

func TestResolveStateIDForBeadsStatusOutboundOverrideUnknownStateNameErrors(t *testing.T) {
	cache := &StateCache{
		States: []State{
			{ID: "state-progress", Name: "In Progress", Type: "started"},
			{ID: "state-review", Name: "In Review", Type: "started"},
		},
	}
	config := DefaultMappingConfig()
	config.ExplicitStateMap["started"] = "in_progress"
	config.OutboundStateMap["in_progress"] = "Doing"

	_, err := ResolveStateIDForBeadsStatus(cache, types.StatusInProgress, config)
	if err == nil {
		t.Fatal("expected error for unknown outbound state name")
	}
	if got := err.Error(); !strings.Contains(got, "linear.outbound_state_map.in_progress") || !strings.Contains(got, `"Doing"`) {
		t.Fatalf("error message should name the bad config key and value, got: %v", err)
	}
}

func TestResolveStateIDForBeadsStatusOutboundOverrideWinsOverExplicitStateName(t *testing.T) {
	cache := &StateCache{
		States: []State{
			{ID: "state-progress", Name: "In Progress", Type: "started"},
			{ID: "state-review", Name: "In Review", Type: "started"},
		},
	}
	config := DefaultMappingConfig()
	// ExplicitStateMap would route in_progress to "In Review" by name match,
	// but OutboundStateMap takes precedence and picks "In Progress".
	config.ExplicitStateMap["in review"] = "in_progress"
	config.OutboundStateMap["in_progress"] = "In Progress"

	got, err := ResolveStateIDForBeadsStatus(cache, types.StatusInProgress, config)
	if err != nil {
		t.Fatalf("ResolveStateIDForBeadsStatus() error = %v", err)
	}
	if got != "state-progress" {
		t.Fatalf("ResolveStateIDForBeadsStatus() = %q, want state-progress (outbound override should win over explicit name match)", got)
	}
}

func TestResolveStateIDForBeadsStatusAmbiguousErrorPointsAtOutboundOverride(t *testing.T) {
	cache := &StateCache{
		States: []State{
			{ID: "state-progress", Name: "In Progress", Type: "started"},
			{ID: "state-review", Name: "In Review", Type: "started"},
		},
	}
	config := DefaultMappingConfig()
	config.ExplicitStateMap["started"] = "in_progress"

	_, err := ResolveStateIDForBeadsStatus(cache, types.StatusInProgress, config)
	if err == nil {
		t.Fatal("expected ambiguous mapping to fail")
	}
	if got := err.Error(); !strings.Contains(got, "linear.outbound_state_map.in_progress") {
		t.Fatalf("ambiguity error should mention the outbound_state_map escape hatch, got: %v", err)
	}
}

func TestLoadMappingConfigParsesOutboundStateMap(t *testing.T) {
	loader := &mockConfigLoader{
		config: map[string]string{
			"linear.state_map.started":              "in_progress",
			"linear.outbound_state_map.in_progress": "In Progress",
			"linear.outbound_state_map.blocked":     "Blocked",
		},
	}

	config := LoadMappingConfig(loader)

	if got := config.OutboundStateMap["in_progress"]; got != "In Progress" {
		t.Errorf("OutboundStateMap[in_progress] = %q, want %q", got, "In Progress")
	}
	if got := config.OutboundStateMap["blocked"]; got != "Blocked" {
		t.Errorf("OutboundStateMap[blocked] = %q, want %q", got, "Blocked")
	}
	// Defaults still empty for unset keys.
	if got, ok := config.OutboundStateMap["closed"]; ok {
		t.Errorf("OutboundStateMap[closed] = %q, want unset", got)
	}
}

// Regression GH#3754: explicit state_map values that are custom status names
// must not parse as StatusOpen and create phantom matches for beads "open".
func TestResolveStateIDForBeadsStatusCustomValueDoesNotFalseMatchOpen(t *testing.T) {
	cache := &StateCache{
		States: []State{
			{ID: "state-todo", Name: "Todo", Type: "unstarted"},
			{ID: "state-review", Name: "In Review", Type: "unstarted"},
		},
	}
	config := DefaultMappingConfig()
	config.ExplicitStateMap["todo"] = "open"
	config.ExplicitStateMap["in review"] = "review"

	got, err := ResolveStateIDForBeadsStatus(cache, types.StatusOpen, config)
	if err != nil {
		t.Fatalf("ResolveStateIDForBeadsStatus(open) error = %v", err)
	}
	if got != "state-todo" {
		t.Fatalf("ResolveStateIDForBeadsStatus(open) = %q, want state-todo", got)
	}

	got2, err := ResolveStateIDForBeadsStatus(cache, types.Status("review"), config)
	if err != nil {
		t.Fatalf("ResolveStateIDForBeadsStatus(review) error = %v", err)
	}
	if got2 != "state-review" {
		t.Fatalf("ResolveStateIDForBeadsStatus(review) = %q, want state-review", got2)
	}
}

func TestResolveStateIDForBeadsStatusDeferredExplicitMapping(t *testing.T) {
	cache := &StateCache{
		States: []State{
			{ID: "state-icebox", Name: "Icebox", Type: "unstarted"},
		},
	}
	config := DefaultMappingConfig()
	config.ExplicitStateMap["icebox"] = "deferred"

	got, err := ResolveStateIDForBeadsStatus(cache, types.StatusDeferred, config)
	if err != nil {
		t.Fatalf("ResolveStateIDForBeadsStatus() error = %v", err)
	}
	if got != "state-icebox" {
		t.Fatalf("ResolveStateIDForBeadsStatus() = %q, want state-icebox", got)
	}
}

func TestIssueTypeToLinearLabelLookupKeyDeterministic(t *testing.T) {
	cfg := DefaultMappingConfig()
	cfg.LabelTypeMap["enhancement"] = "feature"
	cfg.LabelTypeMap["feature"] = "feature"
	// Lexicographically smallest label key wins when multiple map to the same type.
	if got := IssueTypeToLinearLabelLookupKey(types.TypeFeature, cfg); got != "enhancement" {
		t.Fatalf("IssueTypeToLinearLabelLookupKey(feature) = %q, want enhancement", got)
	}
}

func TestResolveLabelIDsDedupesAndReportsMissing(t *testing.T) {
	cfg := DefaultMappingConfig()
	cache := &LabelCache{
		IDByLowerName: map[string]string{
			"bug":        "id-bug",
			"customer-x": "id-cx",
			"task":       "id-task",
		},
	}
	issue := &types.Issue{
		IssueType: types.TypeBug,
		Labels:    []string{"Customer-X", "ghost"},
	}
	ids, missing := ResolveLabelIDs(issue, cache, cfg)
	sort.Strings(ids)
	want := []string{"id-bug", "id-cx"}
	sort.Strings(want)
	if !slices.Equal(ids, want) {
		t.Fatalf("ResolveLabelIDs ids = %v, want %v", ids, want)
	}
	if len(missing) != 1 || missing[0] != "ghost" {
		t.Fatalf("ResolveLabelIDs missing = %v, want [ghost]", missing)
	}
}

func TestPushFieldsEqualComparesResolvedLabelIDs(t *testing.T) {
	config := DefaultMappingConfig()
	cache := &LabelCache{
		IDByLowerName: map[string]string{
			"task": "id-task",
			"a":    "id-a",
		},
	}
	local := &types.Issue{
		Title:       "x",
		Description: "d",
		Status:      types.StatusOpen,
		Priority:    0, // critical in beads → urgent in Linear (priority 1)
		IssueType:   types.TypeTask,
		Labels:      []string{"A"},
	}
	remote := &Issue{
		Title:       "x",
		Description: "d",
		Priority:    4, // low in Linear — differs from beads P0 mapping
		State:       &State{ID: "s", Name: "Backlog", Type: "backlog"},
		Labels: &Labels{Nodes: []Label{
			{ID: "id-task", Name: "task"},
			{ID: "id-a", Name: "a"},
		}},
	}
	if PushFieldsEqual(local, remote, config, cache) {
		t.Fatal("expected false when Linear priority differs")
	}
	remote.Priority = PriorityToLinear(0, config)
	if !PushFieldsEqual(local, remote, config, cache) {
		t.Fatal("expected true when fields and resolved label ID sets match")
	}
}

func TestPushFieldsEqualIgnoresLocalOnlyDifferences(t *testing.T) {
	config := DefaultMappingConfig()
	local := &types.Issue{
		Title:       "Ship the fix",
		Description: "Main body",
		Notes:       "Local-only notes",
		Status:      types.StatusInProgress,
		Priority:    1,
		IssueType:   types.TypeFeature,
		Labels:      []string{"customer-visible"},
	}
	remote := &Issue{
		Title:       "Ship the fix",
		Description: "Main body\n\n## Notes\nLocal-only notes",
		Priority:    2,
		State:       &State{ID: "state-3", Name: "In Progress", Type: "started"},
	}

	if !PushFieldsEqual(local, remote, config, nil) {
		t.Fatal("expected push fields to compare equal despite local-only issue type and labels")
	}
}

// TestPushFieldsEqualMatchesPushedState pins that PushFieldsEqual accepts the
// workflow state a push of the local status selects, not only one the pull
// maps back to that status: Linear states always carry a type, and the pull
// maps the type first, so "Blocked" (type started), the state a name-keyed
// linear.state_map entry pushes blocked to, reads back as in_progress.
func TestPushFieldsEqualMatchesPushedState(t *testing.T) {
	blocked := &State{ID: "state-blocked", Name: "Blocked", Type: "started"}
	inProgress := &State{ID: "state-progress", Name: "In Progress", Type: "started"}
	onHold := &State{ID: "state-hold", Name: "On Hold", Type: "started"}
	for _, test := range []struct {
		name     string
		stateMap map[string]string // linear.state_map.<key>
		outbound map[string]string // linear.outbound_state_map.<status>
		status   types.Status
		state    *State
		want     bool
	}{
		{
			name:     "name_mapped_echo",
			stateMap: map[string]string{"in progress": "in_progress", "blocked": "blocked"},
			status:   types.StatusBlocked, state: blocked, want: true,
		},
		{
			name:     "moved_to_another_state",
			stateMap: map[string]string{"in progress": "in_progress", "blocked": "blocked"},
			status:   types.StatusBlocked, state: inProgress,
		},
		{
			name:     "outbound_name_echo",
			stateMap: map[string]string{"in progress": "in_progress"},
			outbound: map[string]string{"blocked": "Blocked"},
			status:   types.StatusBlocked, state: blocked, want: true,
		},
		{
			// The outbound name decides the push, as it does in
			// ResolveStateIDForBeadsStatus.
			name:     "outbound_name_over_state_map",
			stateMap: map[string]string{"in progress": "in_progress", "on hold": "blocked"},
			outbound: map[string]string{"blocked": "Blocked"},
			status:   types.StatusBlocked, state: onHold,
		},
		{
			// A custom status reads back as open, so only the push's type
			// rule recognizes it.
			name:     "type_mapped_custom_status",
			stateMap: map[string]string{"started": "review"},
			status:   types.Status("review"), state: inProgress, want: true,
		},
		{
			// A state the pull maps back to the local status still counts,
			// as before, though the push would select "In Progress".
			name:     "pull_mapping_still_counts",
			stateMap: map[string]string{"in progress": "in_progress", "blocked": "blocked"},
			status:   types.StatusInProgress, state: blocked, want: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := DefaultMappingConfig()
			for key, value := range test.stateMap {
				config.StateMap[key] = value
				config.ExplicitStateMap[key] = value
			}
			for key, value := range test.outbound {
				config.OutboundStateMap[key] = value
			}
			local := &types.Issue{Title: "x", Description: "d", Status: test.status, Priority: 2}
			remote := &Issue{Title: "x", Description: "d", Priority: PriorityToLinear(2, config), State: test.state}
			if got := PushFieldsEqual(local, remote, config, nil); got != test.want {
				t.Fatalf("PushFieldsEqual(status %s, state %s) = %v, want %v", test.status, test.state.Name, got, test.want)
			}
		})
	}
}

func TestPushFieldsEqualToBeads(t *testing.T) {
	local := &types.Issue{
		Title:       "Ship the fix",
		Description: "Main body",
		Notes:       "Local-only notes",
		Status:      types.StatusInProgress,
		Priority:    1,
		IssueType:   types.TypeFeature,
		Labels:      []string{"customer-visible"},
	}
	remote := &types.Issue{
		Title:       "Ship the fix",
		Description: "Main body\n\n## Notes\nLocal-only notes",
		Status:      types.StatusInProgress,
		Priority:    1,
		IssueType:   types.TypeTask,
		Labels:      []string{"ignored"},
	}

	if !PushFieldsEqualToBeads(local, remote) {
		t.Fatal("expected beads-form fallback comparison to ignore local-only fields")
	}
}

// team9Description is the Linear description of the issue in GH#8506 after
// a push: Linear stores a blank line after each heading, and the create
// appended bd's idempotency marker.
const team9Description = "DESC-MARK the description.\n\n## Acceptance Criteria\n\nAC-MARK the acceptance criteria.\n\n## Design\n\nDESIGN-MARK the design.\n\n## Notes\n\nNOTES-MARK the notes.\n<!-- bd-idempotency: a590f1df4a90 -->"

var team9Parts = DescriptionParts{
	Description:        "DESC-MARK the description.",
	AcceptanceCriteria: "AC-MARK the acceptance criteria.",
	Design:             "DESIGN-MARK the design.",
	Notes:              "NOTES-MARK the notes.",
}

// linearStoredDescription renders text the way Linear stores a pushed
// description: a blank line after each "## " heading line.
func linearStoredDescription(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		out = append(out, line)
		if strings.HasPrefix(line, "## ") && (i+1 >= len(lines) || lines[i+1] != "") {
			out = append(out, "")
		}
	}
	return strings.Join(out, "\n")
}

func blankLineBefore(field string) string {
	if field == "" {
		return ""
	}
	return "\n" + field
}

// parseLinearDescriptionCases are the texts TestParseLinearDescription pins;
// TestParseLinearDescriptionIsAFixedPoint checks P2 and P3 over them.
var parseLinearDescriptionCases = []struct {
	name string
	text string
	want DescriptionParts
}{
	{
		name: "bd output with every field",
		text: "D\n\n## Acceptance Criteria\nA\n\n## Design\nG\n\n## Notes\nN",
		want: DescriptionParts{Description: "D", AcceptanceCriteria: "A", Design: "G", Notes: "N"},
	},
	{
		name: "bd output with acceptance criteria only",
		text: "D\n\n## Acceptance Criteria\nA",
		want: DescriptionParts{Description: "D", AcceptanceCriteria: "A"},
	},
	{
		name: "bd output with acceptance criteria and notes",
		text: "D\n\n## Acceptance Criteria\nA\n\n## Notes\nN",
		want: DescriptionParts{Description: "D", AcceptanceCriteria: "A", Notes: "N"},
	},
	{
		name: "reporter's TEAM-9 text",
		text: team9Description,
		want: team9Parts,
	},
	{
		name: "marker only",
		text: "<!-- bd-idempotency: a590f1df4a90 -->",
		want: DescriptionParts{},
	},
	{
		name: "description and marker",
		text: "Body\n<!-- bd-idempotency: a590f1df4a90 -->",
		want: DescriptionParts{Description: "Body"},
	},
	{
		name: "mid-text marker",
		text: "Body\n<!-- bd-idempotency: a590f1df4a90 -->\nmore\n\n## Notes\nN",
		want: DescriptionParts{Description: "Body\nmore", Notes: "N"},
	},
	{
		name: "unterminated marker prefix stays",
		text: "Body\n<!-- bd-idempotency: abc",
		want: DescriptionParts{Description: "Body\n<!-- bd-idempotency: abc"},
	},
	{
		name: "quoted marker prefix before another comment stays",
		text: "Markers start with `<!-- bd-idempotency: ` and are hex.\n\nKeep this paragraph.\n\n## Acceptance Criteria\nA\n<!-- reviewer: ok -->",
		want: DescriptionParts{
			Description:        "Markers start with `<!-- bd-idempotency: ` and are hex.\n\nKeep this paragraph.",
			AcceptanceCriteria: "A\n<!-- reviewer: ok -->",
		},
	},
	{
		name: "marker quoted in prose stays",
		text: "Each create adds a `<!-- bd-idempotency: ... -->` marker.",
		want: DescriptionParts{Description: "Each create adds a `<!-- bd-idempotency: ... -->` marker."},
	},
	{
		name: "marker on a line of its own without bd's 12 hex digits stays",
		text: "Body\n<!-- bd-idempotency: a590f1df4a90a590 -->\n<!-- bd-idempotency: A590F1DF4A90 -->",
		want: DescriptionParts{Description: "Body\n<!-- bd-idempotency: a590f1df4a90a590 -->\n<!-- bd-idempotency: A590F1DF4A90 -->"},
	},
	{
		name: "fenced literal heading stays in the description",
		text: "Example:\n\n```markdown\n## Notes\nexample note\n```\n\nAfter the example.",
		want: DescriptionParts{Description: "Example:\n\n```markdown\n## Notes\nexample note\n```\n\nAfter the example."},
	},
	{
		name: "fenced example before a real section",
		text: "Example:\n\n```markdown\n## Notes\nexample note\n```\n\n## Notes\nreal note",
		want: DescriptionParts{Description: "Example:\n\n```markdown\n## Notes\nexample note\n```", Notes: "real note"},
	},
	{
		name: "fenced heading in a section body stays in that section",
		text: "D\n\n## Design\nG\n\n```markdown\n## Notes\nexample\n```",
		want: DescriptionParts{Description: "D", Design: "G\n\n```markdown\n## Notes\nexample\n```"},
	},
	{
		name: "tilde fence holds a backtick run and a heading",
		text: "D\n\n## Notes\nN\n\n~~~\n```\n## Notes\n~~~",
		want: DescriptionParts{Description: "D", Notes: "N\n\n~~~\n```\n## Notes\n~~~"},
	},
	{
		name: "fence closes only on a run at least as long",
		text: "D\n\n## Notes\nN\n````\n```\n## Design\nx\n````",
		want: DescriptionParts{Description: "D", Notes: "N\n````\n```\n## Design\nx\n````"},
	},
	{
		name: "fence indented three spaces hides a heading",
		text: "D\n   ```\n## Notes\nN\n   ```",
		want: DescriptionParts{Description: "D\n   ```\n## Notes\nN\n   ```"},
	},
	{
		name: "line indented four spaces is no fence",
		text: "D\n    ```\n## Notes\nN\n    ```",
		want: DescriptionParts{Description: "D\n    ```", Notes: "N\n    ```"},
	},
	{
		name: "backtick in a backtick fence's info string opens no fence",
		text: "D\n```a`b\n## Notes\nN\n```",
		want: DescriptionParts{Description: "D\n```a`b", Notes: "N\n```"},
	},
	{
		name: "fence never closed hides nothing",
		text: "D\n```\ncut off\n\n## Acceptance Criteria\nA",
		want: DescriptionParts{Description: "D\n```\ncut off", AcceptanceCriteria: "A"},
	},
	{
		name: "notes heading in the description while acceptance criteria is set",
		text: "D\n## Notes\nnot a field\n\n## Acceptance Criteria\nA",
		want: DescriptionParts{Description: "D\n## Notes\nnot a field", AcceptanceCriteria: "A"},
	},
	{
		name: "trailing heading with an empty body stays text",
		text: "D\n\n## Notes\n\n",
		want: DescriptionParts{Description: "D\n\n## Notes\n\n"},
	},
	{
		name: "out-of-order headings split only the final in-order run",
		text: "D\n\n## Notes\nN0\n\n## Design\nG",
		want: DescriptionParts{Description: "D\n\n## Notes\nN0", Design: "G"},
	},
	{
		name: "repeated heading keeps the earlier one in the section above",
		text: "D\n\n## Acceptance Criteria\nA1\n\n## Acceptance Criteria\nA2",
		want: DescriptionParts{Description: "D\n\n## Acceptance Criteria\nA1", AcceptanceCriteria: "A2"},
	},
	{
		name: "field with its own subheadings is not cut",
		text: "D\n\n## Design\n### Storage\nrows\n## Rollout\nslow",
		want: DescriptionParts{Description: "D", Design: "### Storage\nrows\n## Rollout\nslow"},
	},
	{
		name: "section body keeps indentation and inner blank lines",
		text: "DESC\n\n## Notes\n  - item one\n\n  - item two",
		want: DescriptionParts{Description: "DESC", Notes: "  - item one\n\n  - item two"},
	},
	{
		name: "CRLF line ends and trailing spaces on a heading line",
		text: "D\r\n\r\n## Acceptance Criteria  \r\n\r\nA\r\n\r\n## Notes\t\r\nN\r\n",
		want: DescriptionParts{Description: "D", AcceptanceCriteria: "A", Notes: "N"},
	},
	{
		name: "Linear-native description without a trailing run is verbatim",
		text: "# Context\n\n## Notes:\n\nfrom the PM\n\n### Design\n\n## Acceptance criteria\nship it\n",
		want: DescriptionParts{Description: "# Context\n\n## Notes:\n\nfrom the PM\n\n### Design\n\n## Acceptance criteria\nship it\n"},
	},
	{
		name: "text the bug already damaged: two copies and a mid-text marker",
		text: team9Description + "\n\n## Acceptance Criteria\nAC-MARK the acceptance criteria.\n\n## Design\nDESIGN-MARK the design.\n\n## Notes\nNOTES-MARK the notes.",
		want: DescriptionParts{
			Description:        "DESC-MARK the description.\n\n## Acceptance Criteria\n\nAC-MARK the acceptance criteria.\n\n## Design\n\nDESIGN-MARK the design.\n\n## Notes\n\nNOTES-MARK the notes.",
			AcceptanceCriteria: team9Parts.AcceptanceCriteria,
			Design:             team9Parts.Design,
			Notes:              team9Parts.Notes,
		},
	},
}

// TestParseLinearDescription pins the inverse of BuildLinearDescription a
// pull applies to Linear's description (GH#8506).
func TestParseLinearDescription(t *testing.T) {
	for _, tc := range parseLinearDescriptionCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseLinearDescription(tc.text); got != tc.want {
				t.Errorf("ParseLinearDescription(%q)\n got  %#v\n want %#v", tc.text, got, tc.want)
			}
		})
	}
}

// TestParseLinearDescriptionIsAFixedPoint checks that parse inverts build
// (P1), that a parse then build then parse changes nothing, so the text can
// never grow again (P2), and that Linear's stored form, with or without the
// marker, parses as bd's own output does (P3).
func TestParseLinearDescriptionIsAFixedPoint(t *testing.T) {
	const marker = "<!-- bd-idempotency: a590f1df4a90 -->"
	for mask := 0; mask < 16; mask++ {
		local := &types.Issue{}
		if mask&1 != 0 {
			local.Description = "Body line one.\n\n  indented line"
		}
		if mask&2 != 0 {
			local.AcceptanceCriteria = "- [ ] it works\n- [ ] it ships"
		}
		if mask&4 != 0 {
			local.Design = "### Storage\nrows"
		}
		if mask&8 != 0 {
			local.Notes = "note"
		}
		want := DescriptionParts{
			Description:        local.Description,
			AcceptanceCriteria: local.AcceptanceCriteria,
			Design:             local.Design,
			Notes:              local.Notes,
		}
		built := BuildLinearDescription(local)
		for name, text := range map[string]string{
			"bd output":                built,
			"Linear stored":            linearStoredDescription(built),
			"Linear stored and marker": AppendIdempotencyMarker(linearStoredDescription(built), marker),
		} {
			if got := ParseLinearDescription(text); got != want {
				t.Errorf("mask %04b %s: ParseLinearDescription(%q)\n got  %#v\n want %#v", mask, name, text, got, want)
			}
		}
	}

	for _, tc := range parseLinearDescriptionCases {
		first := ParseLinearDescription(tc.text)
		rebuilt := BuildLinearDescription(&types.Issue{
			Description:        first.Description,
			AcceptanceCriteria: first.AcceptanceCriteria,
			Design:             first.Design,
			Notes:              first.Notes,
		})
		if again := ParseLinearDescription(rebuilt); again != first {
			t.Errorf("%s: parse(build(parse(x))) = %#v, want %#v", tc.name, again, first)
		}
		// Linear's blank line after each heading bd wrote; the headings
		// inside the description and the fields are left as they are.
		stored := BuildLinearDescription(&types.Issue{
			Description:        first.Description,
			AcceptanceCriteria: blankLineBefore(first.AcceptanceCriteria),
			Design:             blankLineBefore(first.Design),
			Notes:              blankLineBefore(first.Notes),
		})
		if again := ParseLinearDescription(stored); again != first {
			t.Errorf("%s: parse(Linear stored build(parse(x))) = %#v, want %#v", tc.name, again, first)
		}
	}
}

// TestIssueToBeadsSplitsDescriptionSections checks that a pulled Linear issue
// carries its pushed sections in the bead's fields, not its description.
func TestIssueToBeadsSplitsDescriptionSections(t *testing.T) {
	li := &Issue{
		ID:          "uuid-9",
		Identifier:  "TEAM-9",
		Title:       "fields pushed",
		Description: team9Description,
		URL:         "https://linear.app/team/issue/TEAM-9/fields-pushed",
		CreatedAt:   "2026-11-18T10:00:00Z",
		UpdatedAt:   "2026-11-18T10:05:00Z",
	}
	issue := IssueToBeads(li, DefaultMappingConfig()).Issue.(*types.Issue)
	got := DescriptionParts{
		Description:        issue.Description,
		AcceptanceCriteria: issue.AcceptanceCriteria,
		Design:             issue.Design,
		Notes:              issue.Notes,
	}
	if got != team9Parts {
		t.Fatalf("IssueToBeads fields = %#v, want %#v", got, team9Parts)
	}

	plain := IssueToBeads(&Issue{Identifier: "TEAM-1", Description: "Just a body\n"}, DefaultMappingConfig()).Issue.(*types.Issue)
	if plain.Description != "Just a body\n" || plain.AcceptanceCriteria != "" || plain.Design != "" || plain.Notes != "" {
		t.Fatalf("IssueToBeads of a plain description = %#v, want it verbatim with empty fields", plain)
	}
}

// TestBuildLinearToLocalUpdatesSplitsDescriptionSections checks that the
// conflict-resolution updates write the pushed sections to their fields.
func TestBuildLinearToLocalUpdatesSplitsDescriptionSections(t *testing.T) {
	updates := BuildLinearToLocalUpdates(&Issue{Identifier: "TEAM-9", Description: team9Description}, DefaultMappingConfig())
	want := map[string]string{
		"description":         team9Parts.Description,
		"acceptance_criteria": team9Parts.AcceptanceCriteria,
		"design":              team9Parts.Design,
		"notes":               team9Parts.Notes,
	}
	for key, value := range want {
		if got := updates[key]; got != value {
			t.Errorf("updates[%q] = %#v, want %q", key, got, value)
		}
		if text, _ := updates[key].(string); strings.Contains(text, "bd-idempotency") {
			t.Errorf("updates[%q] = %q carries the idempotency marker", key, text)
		}
	}
}

// TestLinearPullInvertsPushDescription reproduces GH#8506: a pull must invert
// what a push wrote into Linear's description. Pulling the pushed body must not
// fold the Acceptance Criteria/Design/Notes sections or the idempotency marker
// into the bead's description, so push -> pull -> push is a fixed point.
func TestLinearPullInvertsPushDescription(t *testing.T) {
	local := &types.Issue{
		ID:                 "spk-moq",
		Description:        "DESC-MARK the description.",
		AcceptanceCriteria: "AC-MARK the acceptance criteria.",
		Design:             "DESIGN-MARK the design.",
		Notes:              "NOTES-MARK the notes.",
	}
	marker := GenerateIdempotencyMarker(local.ID, "", 0)
	pushed := AppendIdempotencyMarker(BuildLinearDescription(local), marker)

	li := &Issue{
		ID:          "uuid-1",
		Identifier:  "TEAM-9",
		Title:       "fields pushed",
		Description: pushed,
		URL:         "https://linear.app/team/issue/TEAM-9/fields-pushed",
		CreatedAt:   "2026-11-18T10:00:00Z",
		UpdatedAt:   "2026-11-18T10:05:00Z",
	}
	config := DefaultMappingConfig()

	conv := IssueToBeads(li, config)
	pulled := conv.Issue.(*types.Issue)
	if pulled.Description != local.Description {
		t.Errorf("IssueToBeads description = %q, want %q", pulled.Description, local.Description)
	}

	updates := BuildLinearToLocalUpdates(li, config)
	if got := updates["description"]; got != local.Description {
		t.Errorf("BuildLinearToLocalUpdates description = %q, want %q", got, local.Description)
	}

	// The next push builds the description from the pulled bead (which keeps
	// its acceptance_criteria/design/notes); it must not add another copy.
	repulled := *local
	repulled.Description, _ = updates["description"].(string)
	if n := strings.Count(BuildLinearDescription(&repulled), "## Acceptance Criteria"); n != 1 {
		t.Errorf("second push carries %d Acceptance Criteria sections, want 1", n)
	}
}

// TestPushFieldsEqualLinearStoredDescription checks that both push
// comparators see an unchanged bead as equal to the description Linear
// stored for its push (a blank line after each heading, with or without the
// create's idempotency marker), and still see a changed body, section text
// or section presence (GH#8506).
func TestPushFieldsEqualLinearStoredDescription(t *testing.T) {
	config := DefaultMappingConfig()
	const marker = "<!-- bd-idempotency: a590f1df4a90 -->"
	local := &types.Issue{
		Title:              "fields pushed",
		Description:        team9Parts.Description,
		AcceptanceCriteria: team9Parts.AcceptanceCriteria,
		Design:             team9Parts.Design,
		Notes:              team9Parts.Notes,
		Status:             types.StatusInProgress,
		Priority:           2,
	}
	stored := linearStoredDescription(BuildLinearDescription(local))
	remoteFor := func(description string) *Issue {
		return &Issue{
			Title:       local.Title,
			Description: description,
			Priority:    PriorityToLinear(local.Priority, config),
			State:       &State{ID: "state-3", Name: "In Progress", Type: "started"},
		}
	}
	beadsFor := func(description string) map[string]*types.Issue {
		folded := &types.Issue{Title: local.Title, Description: description, Status: local.Status, Priority: local.Priority}
		split := IssueToBeads(remoteFor(description), config).Issue.(*types.Issue)
		split.Status, split.Priority = local.Status, local.Priority
		return map[string]*types.Issue{"folded": folded, "split": split}
	}

	for name, description := range map[string]string{
		"Linear stored":            stored,
		"Linear stored and marker": AppendIdempotencyMarker(stored, marker),
		"reporter's TEAM-9 text":   team9Description,
	} {
		if !PushFieldsEqual(local, remoteFor(description), config, nil) {
			t.Errorf("%s: PushFieldsEqual = false, want the unchanged bead equal to %q", name, description)
		}
		for form, remote := range beadsFor(description) {
			if !PushFieldsEqualToBeads(local, remote) {
				t.Errorf("%s: PushFieldsEqualToBeads(%s remote) = false, want equal", name, form)
			}
		}
	}

	changed := map[string]func(*types.Issue){
		"description body":  func(i *types.Issue) { i.Description = "DESC-MARK an edited description." },
		"section text":      func(i *types.Issue) { i.AcceptanceCriteria = "AC-MARK edited criteria." },
		"dropped section":   func(i *types.Issue) { i.Design = "" },
		"two sections gone": func(i *types.Issue) { i.Notes, i.Design = "", "" },
	}
	for name, edit := range changed {
		other := *local
		edit(&other)
		description := AppendIdempotencyMarker(linearStoredDescription(BuildLinearDescription(&other)), marker)
		if PushFieldsEqual(local, remoteFor(description), config, nil) {
			t.Errorf("%s: PushFieldsEqual = true for remote %q, want unequal", name, description)
		}
		for form, remote := range beadsFor(description) {
			if PushFieldsEqualToBeads(local, remote) {
				t.Errorf("%s: PushFieldsEqualToBeads(%s remote) = true, want unequal", name, form)
			}
		}
		// The other direction: the local bead changed, Linear did not.
		if PushFieldsEqual(&other, remoteFor(AppendIdempotencyMarker(stored, marker)), config, nil) {
			t.Errorf("%s: PushFieldsEqual(edited local) = true, want unequal", name)
		}
	}
}
