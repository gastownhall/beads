package linear

import (
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

	if !PushFieldsEqual(local, remote, config) {
		t.Fatal("expected push fields to compare equal despite local-only issue type and labels")
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
