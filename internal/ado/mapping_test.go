package ado

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

func TestPriorityToBeads(t *testing.T) {
	m := NewFieldMapper(nil, nil)

	tests := []struct {
		name  string
		input interface{}
		want  int
	}{
		{"ADO 1 → beads 0", float64(1), 0},
		{"ADO 2 → beads 1", float64(2), 1},
		{"ADO 3 → beads 2", float64(3), 2},
		{"ADO 4 → beads 3", float64(4), 3},
		{"ADO 0 invalid → default 2", float64(0), 2},
		{"ADO 5 invalid → default 2", float64(5), 2},
		{"nil → default 2", nil, 2},
		{"string wrong type → default 2", "2", 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := m.PriorityToBeads(tt.input)
			if got != tt.want {
				t.Errorf("PriorityToBeads(%v) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestPriorityToTracker(t *testing.T) {
	m := NewFieldMapper(nil, nil)

	tests := []struct {
		name  string
		input int
		want  int
	}{
		{"beads 0 → ADO 1", 0, 1},
		{"beads 1 → ADO 2", 1, 2},
		{"beads 2 → ADO 3", 2, 3},
		{"beads 3 → ADO 4", 3, 4},
		{"beads 4 → ADO 4 lossy", 4, 4},
		{"beads -1 → ADO 3 default", -1, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := m.PriorityToTracker(tt.input)
			if got != tt.want {
				t.Errorf("PriorityToTracker(%d) = %v, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestSeverityForBug(t *testing.T) {
	fm := NewFieldMapper(nil, nil)
	m := fm.(*adoFieldMapper)

	tests := []struct {
		name     string
		priority int
		want     string
	}{
		{"P0 → 1 - Critical", 0, "1 - Critical"},
		{"P1 → 2 - High", 1, "2 - High"},
		{"P2 → 3 - Medium", 2, "3 - Medium"},
		{"P3 → 4 - Low", 3, "4 - Low"},
		{"P4 → 4 - Low", 4, "4 - Low"},
		{"negative → 3 - Medium default", -1, "3 - Medium"},
		{"out of range → 3 - Medium default", 99, "3 - Medium"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := m.SeverityForBug(tt.priority)
			if got != tt.want {
				t.Errorf("SeverityForBug(%d) = %q, want %q", tt.priority, got, tt.want)
			}
		})
	}
}

func TestStatusToBeads_Defaults(t *testing.T) {
	m := NewFieldMapper(nil, nil)

	tests := []struct {
		name  string
		input interface{}
		want  types.Status
	}{
		{"New → open", "New", types.StatusOpen},
		{"Active → in_progress", "Active", types.StatusInProgress},
		{"Removed → deferred", "Removed", types.StatusDeferred},
		{"Closed → closed", "Closed", types.StatusClosed},
		{"Resolved → closed", "Resolved", types.StatusClosed},
		{"empty → open", "", types.StatusOpen},
		{"unknown → open", "CustomState", types.StatusOpen},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := m.StatusToBeads(tt.input)
			if got != tt.want {
				t.Errorf("StatusToBeads(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestStatusToBeads_NonStringInput(t *testing.T) {
	m := NewFieldMapper(nil, nil)

	tests := []struct {
		name  string
		input interface{}
		want  types.Status
	}{
		{"nil → open", nil, types.StatusOpen},
		{"int → open", 42, types.StatusOpen},
		{"float → open", 3.14, types.StatusOpen},
		{"bool → open", true, types.StatusOpen},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := m.StatusToBeads(tt.input)
			if got != tt.want {
				t.Errorf("StatusToBeads(%v) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestStatusToTracker_DefaultCase(t *testing.T) {
	m := NewFieldMapper(nil, nil)

	got := m.StatusToTracker(types.Status("unknown_status"))
	if got != "New" {
		t.Errorf("StatusToTracker(unknown_status) = %v, want %q", got, "New")
	}
}

func TestStatusToTracker_CustomMapOverridesDefault(t *testing.T) {
	m := NewFieldMapper(
		map[string]string{
			"open":        "To Do",
			"in_progress": "Doing",
			"closed":      "Finished",
			"blocked":     "On Hold",
			"deferred":    "Parked",
		},
		nil,
	)

	tests := []struct {
		name  string
		input types.Status
		want  string
	}{
		{"custom open → To Do", types.StatusOpen, "To Do"},
		{"custom in_progress → Doing", types.StatusInProgress, "Doing"},
		{"custom closed → Finished", types.StatusClosed, "Finished"},
		{"custom blocked → On Hold", types.StatusBlocked, "On Hold"},
		{"custom deferred → Parked", types.StatusDeferred, "Parked"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := m.StatusToTracker(tt.input)
			if got != tt.want {
				t.Errorf("StatusToTracker(%q) = %v, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestTypeToBeads_NonStringInput(t *testing.T) {
	m := NewFieldMapper(nil, nil)

	tests := []struct {
		name  string
		input interface{}
		want  types.IssueType
	}{
		{"nil → task", nil, types.TypeTask},
		{"int → task", 42, types.TypeTask},
		{"bool → task", true, types.TypeTask},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := m.TypeToBeads(tt.input)
			if got != tt.want {
				t.Errorf("TypeToBeads(%v) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestTypeToBeads_UnknownWithDefault(t *testing.T) {
	m := NewFieldMapper(nil, nil)

	got := m.TypeToBeads("CustomWorkItemType")
	if got != types.TypeTask {
		t.Errorf("TypeToBeads(CustomWorkItemType) = %q, want %q", got, types.TypeTask)
	}
}

func TestTypeToBeads_CustomMap(t *testing.T) {
	m := NewFieldMapper(nil, map[string]string{"feature": "Product Backlog Item", "bug": "Defect"})

	tests := []struct {
		name  string
		input string
		want  types.IssueType
	}{
		{"custom PBI → feature", "Product Backlog Item", types.TypeFeature},
		{"custom Defect → bug", "Defect", types.TypeBug},
		{"custom case-insensitive", "product backlog item", types.TypeFeature},
		{"fallthrough Task → task", "Task", types.TypeTask},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := m.TypeToBeads(tt.input)
			if got != tt.want {
				t.Errorf("TypeToBeads(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestTypeToTracker_DefaultCase(t *testing.T) {
	m := NewFieldMapper(nil, nil)

	got := m.TypeToTracker(types.IssueType("unknown_type"))
	if got != "Task" {
		t.Errorf("TypeToTracker(unknown_type) = %v, want %q", got, "Task")
	}
}

func TestTypeToTracker_CustomMapAllTypes(t *testing.T) {
	m := NewFieldMapper(nil, map[string]string{
		"bug":     "Defect",
		"feature": "Product Backlog Item",
		"task":    "Work Item",
		"epic":    "Initiative",
		"chore":   "Maintenance",
	})

	tests := []struct {
		name  string
		input types.IssueType
		want  string
	}{
		{"custom bug → Defect", types.TypeBug, "Defect"},
		{"custom feature → PBI", types.TypeFeature, "Product Backlog Item"},
		{"custom task → Work Item", types.TypeTask, "Work Item"},
		{"custom epic → Initiative", types.TypeEpic, "Initiative"},
		{"custom chore → Maintenance", types.TypeChore, "Maintenance"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := m.TypeToTracker(tt.input)
			if got != tt.want {
				t.Errorf("TypeToTracker(%q) = %v, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestStatusToBeads_CustomMap(t *testing.T) {
	m := NewFieldMapper(
		map[string]string{"in_progress": "Doing", "closed": "Finished"},
		nil,
	)

	tests := []struct {
		name  string
		input string
		want  types.Status
	}{
		{"custom Doing → in_progress", "Doing", types.StatusInProgress},
		{"custom Finished → closed", "Finished", types.StatusClosed},
		{"fallthrough New → open", "New", types.StatusOpen},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := m.StatusToBeads(tt.input)
			if got != tt.want {
				t.Errorf("StatusToBeads(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestStatusToTracker_Defaults(t *testing.T) {
	m := NewFieldMapper(nil, nil)

	tests := []struct {
		name  string
		input types.Status
		want  string
	}{
		{"open → New", types.StatusOpen, "New"},
		{"in_progress → Active", types.StatusInProgress, "Active"},
		{"blocked → Active", types.StatusBlocked, "Active"},
		{"deferred → Removed", types.StatusDeferred, "Removed"},
		{"closed → Closed", types.StatusClosed, "Closed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := m.StatusToTracker(tt.input)
			if got != tt.want {
				t.Errorf("StatusToTracker(%q) = %v, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestStatusToTracker_CustomMap(t *testing.T) {
	m := NewFieldMapper(
		map[string]string{"in_progress": "Doing", "closed": "Finished"},
		nil,
	)

	tests := []struct {
		name  string
		input types.Status
		want  string
	}{
		{"custom in_progress → Doing", types.StatusInProgress, "Doing"},
		{"custom closed → Finished", types.StatusClosed, "Finished"},
		{"fallthrough open → New", types.StatusOpen, "New"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := m.StatusToTracker(tt.input)
			if got != tt.want {
				t.Errorf("StatusToTracker(%q) = %v, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestTypeToBeads_Defaults(t *testing.T) {
	m := NewFieldMapper(nil, nil)

	tests := []struct {
		name  string
		input interface{}
		want  types.IssueType
	}{
		{"Bug → bug", "Bug", types.TypeBug},
		{"User Story → feature", "User Story", types.TypeFeature},
		{"Task → task", "Task", types.TypeTask},
		{"Epic → epic", "Epic", types.TypeEpic},
		{"bug lowercase → bug", "bug", types.TypeBug},
		{"user story lowercase → feature", "user story", types.TypeFeature},
		{"empty → task", "", types.TypeTask},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := m.TypeToBeads(tt.input)
			if got != tt.want {
				t.Errorf("TypeToBeads(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestTypeToBeads_Scrum(t *testing.T) {
	m := NewFieldMapper(nil, nil)

	got := m.TypeToBeads("Product Backlog Item")
	if got != types.TypeFeature {
		t.Errorf("TypeToBeads(Product Backlog Item) = %q, want %q", got, types.TypeFeature)
	}
}

func TestTypeToTracker_Defaults(t *testing.T) {
	m := NewFieldMapper(nil, nil)

	tests := []struct {
		name  string
		input types.IssueType
		want  string
	}{
		{"bug → Bug", types.TypeBug, "Bug"},
		{"feature → User Story", types.TypeFeature, "User Story"},
		{"task → Task", types.TypeTask, "Task"},
		{"epic → Epic", types.TypeEpic, "Epic"},
		{"chore → Task", types.TypeChore, "Task"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := m.TypeToTracker(tt.input)
			if got != tt.want {
				t.Errorf("TypeToTracker(%q) = %v, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestTypeToTracker_CustomMap(t *testing.T) {
	m := NewFieldMapper(nil, map[string]string{"feature": "Product Backlog Item"})

	got := m.TypeToTracker(types.TypeFeature)
	if got != "Product Backlog Item" {
		t.Errorf("TypeToTracker(feature) = %v, want %q", got, "Product Backlog Item")
	}

	// Non-overridden types still use defaults.
	got = m.TypeToTracker(types.TypeBug)
	if got != "Bug" {
		t.Errorf("TypeToTracker(bug) = %v, want %q", got, "Bug")
	}
}

func TestIssueToBeads(t *testing.T) {
	m := NewFieldMapper(nil, nil)

	wi := &WorkItem{
		ID:  42,
		Rev: 7,
		URL: "https://dev.azure.com/myorg/myproject/_apis/wit/workItems/42",
		Fields: map[string]interface{}{
			FieldTitle:         "Fix login bug",
			FieldDescription:   "<p>Users cannot log in</p>",
			FieldPriority:      float64(1),
			FieldState:         "Active",
			FieldWorkItemType:  "Bug",
			FieldTags:          "urgent; beads:blocked; frontend",
			FieldAreaPath:      "MyProject\\Team1",
			FieldIterationPath: "MyProject\\Sprint 5",
			FieldStoryPoints:   float64(3),
			FieldRemainingWork: float64(2),
			FieldAssignedTo: map[string]interface{}{
				"displayName": "Alice Smith",
				"uniqueName":  "alice@example.com",
			},
		},
	}

	ti := &tracker.TrackerIssue{
		ID:  "42",
		Raw: wi,
	}

	conv := m.IssueToBeads(ti)
	if conv == nil {
		t.Fatal("IssueToBeads returned nil")
	}

	issue := conv.Issue
	if issue.Title != "Fix login bug" {
		t.Errorf("Title = %q, want %q", issue.Title, "Fix login bug")
	}
	if issue.Description != "Users cannot log in" {
		t.Errorf("Description = %q, want %q", issue.Description, "Users cannot log in")
	}
	if issue.Priority != 0 {
		t.Errorf("Priority = %d, want 0", issue.Priority)
	}
	// Active + beads:blocked tag → blocked status.
	if issue.Status != types.StatusBlocked {
		t.Errorf("Status = %q, want %q", issue.Status, types.StatusBlocked)
	}
	if issue.IssueType != types.TypeBug {
		t.Errorf("IssueType = %q, want %q", issue.IssueType, types.TypeBug)
	}
	if issue.Owner != "Alice Smith" {
		t.Errorf("Owner = %q, want %q", issue.Owner, "Alice Smith")
	}

	// Labels should exclude beads:* tags.
	wantLabels := []string{"urgent", "frontend"}
	if len(issue.Labels) != len(wantLabels) {
		t.Fatalf("Labels = %v, want %v", issue.Labels, wantLabels)
	}
	for i, l := range issue.Labels {
		if l != wantLabels[i] {
			t.Errorf("Labels[%d] = %q, want %q", i, l, wantLabels[i])
		}
	}

	// External ref should be the web URL.
	wantRef := "https://dev.azure.com/myorg/myproject/_workitems/edit/42"
	if issue.ExternalRef == nil || *issue.ExternalRef != wantRef {
		got := "<nil>"
		if issue.ExternalRef != nil {
			got = *issue.ExternalRef
		}
		t.Errorf("ExternalRef = %s, want %s", got, wantRef)
	}

	// Verify metadata preservation.
	if len(issue.Metadata) == 0 {
		t.Fatal("Metadata is empty")
	}
	var meta map[string]interface{}
	if err := json.Unmarshal(issue.Metadata, &meta); err != nil {
		t.Fatalf("failed to unmarshal metadata: %v", err)
	}
	if meta["ado.area_path"] != "MyProject\\Team1" {
		t.Errorf("metadata ado.area_path = %v, want %q", meta["ado.area_path"], "MyProject\\Team1")
	}
	if meta["ado.iteration_path"] != "MyProject\\Sprint 5" {
		t.Errorf("metadata ado.iteration_path = %v, want %q", meta["ado.iteration_path"], "MyProject\\Sprint 5")
	}
	if meta["ado.story_points"] != float64(3) {
		t.Errorf("metadata ado.story_points = %v, want 3", meta["ado.story_points"])
	}
	if meta["ado.rev"] != float64(7) {
		t.Errorf("metadata ado.rev = %v, want 7", meta["ado.rev"])
	}
}

func TestIssueToBeads_NilRaw(t *testing.T) {
	m := NewFieldMapper(nil, nil)

	// nil TrackerIssue.
	if conv := m.IssueToBeads(nil); conv != nil {
		t.Error("IssueToBeads(nil) should return nil")
	}

	// Wrong Raw type.
	ti := &tracker.TrackerIssue{Raw: "not a WorkItem"}
	if conv := m.IssueToBeads(ti); conv != nil {
		t.Error("IssueToBeads(wrong type) should return nil")
	}

	// Nil Raw.
	ti = &tracker.TrackerIssue{Raw: (*WorkItem)(nil)}
	if conv := m.IssueToBeads(ti); conv != nil {
		t.Error("IssueToBeads(nil WorkItem) should return nil")
	}
}

func TestIssueToTracker(t *testing.T) {
	m := NewFieldMapper(nil, nil)

	meta, _ := json.Marshal(map[string]interface{}{
		"ado.area_path":      "Project\\TeamA",
		"ado.iteration_path": "Project\\Sprint 3",
		"ado.story_points":   float64(5),
	})

	issue := &types.Issue{
		Title:       "Implement login",
		Description: "Add OAuth2 support",
		Status:      types.StatusInProgress,
		Priority:    1,
		IssueType:   types.TypeFeature,
		Labels:      []string{"auth", "backend"},
		Metadata:    json.RawMessage(meta),
	}

	fields := m.IssueToTracker(issue)

	if fields[FieldTitle] != "Implement login" {
		t.Errorf("Title = %v, want %q", fields[FieldTitle], "Implement login")
	}
	if fields[FieldState] != "Active" {
		t.Errorf("State = %v, want %q", fields[FieldState], "Active")
	}
	if fields[FieldPriority] != 2 {
		t.Errorf("Priority = %v, want 2", fields[FieldPriority])
	}
	if fields[FieldTags] != "auth; backend" {
		t.Errorf("Tags = %v, want %q", fields[FieldTags], "auth; backend")
	}

	// Description should be HTML.
	desc, ok := fields[FieldDescription].(string)
	if !ok || desc == "" {
		t.Error("Description should be non-empty HTML string")
	}

	// Metadata fields should be restored.
	if fields[FieldAreaPath] != "Project\\TeamA" {
		t.Errorf("AreaPath = %v, want %q", fields[FieldAreaPath], "Project\\TeamA")
	}
	if fields[FieldIterationPath] != "Project\\Sprint 3" {
		t.Errorf("IterationPath = %v, want %q", fields[FieldIterationPath], "Project\\Sprint 3")
	}
	if fields[FieldStoryPoints] != float64(5) {
		t.Errorf("StoryPoints = %v, want 5", fields[FieldStoryPoints])
	}

	// Non-bug types should not have Severity set.
	if _, hasSeverity := fields[FieldSeverity]; hasSeverity {
		t.Errorf("Severity should not be set for Feature type, got %v", fields[FieldSeverity])
	}
}

func TestIssueToTracker_BugSetsSeverity(t *testing.T) {
	m := NewFieldMapper(nil, nil)

	tests := []struct {
		name         string
		priority     int
		wantSeverity string
	}{
		{"P0 bug → 1 - Critical", 0, "1 - Critical"},
		{"P1 bug → 2 - High", 1, "2 - High"},
		{"P2 bug → 3 - Medium", 2, "3 - Medium"},
		{"P3 bug → 4 - Low", 3, "4 - Low"},
		{"P4 bug → 4 - Low", 4, "4 - Low"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := &types.Issue{
				Title:     "Bug issue",
				Status:    types.StatusOpen,
				Priority:  tt.priority,
				IssueType: types.TypeBug,
			}
			fields := m.IssueToTracker(issue)

			sev, ok := fields[FieldSeverity].(string)
			if !ok {
				t.Fatalf("Severity field missing for Bug type")
			}
			if sev != tt.wantSeverity {
				t.Errorf("Severity = %q, want %q", sev, tt.wantSeverity)
			}
		})
	}
}

func TestIssueToTracker_NonBugNoSeverity(t *testing.T) {
	m := NewFieldMapper(nil, nil)

	nonBugTypes := []types.IssueType{
		types.TypeFeature,
		types.TypeTask,
		types.TypeEpic,
		types.TypeChore,
	}

	for _, it := range nonBugTypes {
		t.Run(string(it), func(t *testing.T) {
			issue := &types.Issue{
				Title:     "Non-bug issue",
				Status:    types.StatusOpen,
				Priority:  1,
				IssueType: it,
			}
			fields := m.IssueToTracker(issue)

			if _, hasSeverity := fields[FieldSeverity]; hasSeverity {
				t.Errorf("Severity should not be set for %s type, got %v", it, fields[FieldSeverity])
			}
		})
	}
}

func TestIssueToTracker_CustomBugTypeName(t *testing.T) {
	// When a custom type map maps "bug" to "Defect", Severity should NOT be set
	// because the type name is "Defect", not "Bug".
	m := NewFieldMapper(nil, map[string]string{"bug": "Defect"})

	issue := &types.Issue{
		Title:     "Custom bug type",
		Status:    types.StatusOpen,
		Priority:  0,
		IssueType: types.TypeBug,
	}
	fields := m.IssueToTracker(issue)

	if _, hasSeverity := fields[FieldSeverity]; hasSeverity {
		t.Errorf("Severity should not be set when bug maps to 'Defect' (not 'Bug'), got %v", fields[FieldSeverity])
	}
}

func TestParseTags(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"semicolon separated", "tag1; tag2; tag3", []string{"tag1", "tag2", "tag3"}},
		{"no spaces", "tag1;tag2;tag3", []string{"tag1", "tag2", "tag3"}},
		{"empty string", "", nil},
		{"whitespace only", "  ", nil},
		{"single tag", "solo", []string{"solo"}},
		{"trailing semicolons", "a; b; ", []string{"a", "b"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseTags(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("parseTags(%q) = %v, want %v", tt.input, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("parseTags(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestBuildTagString(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  string
	}{
		{"multiple tags", []string{"tag1", "tag2"}, "tag1; tag2"},
		{"single tag", []string{"solo"}, "solo"},
		{"empty slice", []string{}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildTagString(tt.input)
			if got != tt.want {
				t.Errorf("buildTagString(%v) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestFilterBeadsTags(t *testing.T) {
	input := []string{"bug", "beads:blocked", "urgent"}
	got := filterBeadsTags(input)
	want := []string{"bug", "urgent"}

	if len(got) != len(want) {
		t.Fatalf("filterBeadsTags(%v) = %v, want %v", input, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("filterBeadsTags[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestExtractAssignedTo(t *testing.T) {
	tests := []struct {
		name  string
		input interface{}
		want  string
	}{
		{"nil → empty", nil, ""},
		{"string value", "alice@example.com", "alice@example.com"},
		{"map with displayName", map[string]interface{}{"displayName": "Alice Smith", "uniqueName": "alice@example.com"}, "Alice Smith"},
		{"map with only uniqueName", map[string]interface{}{"uniqueName": "alice@example.com"}, ""},
		{"map with no names", map[string]interface{}{"id": "123"}, ""},
		{"empty map", map[string]interface{}{}, ""},
		{"non-string displayName", map[string]interface{}{"displayName": 42}, ""},
		{"non-map non-string type", 42, ""},
		{"boolean type", true, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractAssignedTo(tt.input)
			if got != tt.want {
				t.Errorf("extractAssignedTo(%v) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestRestoreMetadata(t *testing.T) {
	tests := []struct {
		name       string
		metadata   json.RawMessage
		wantFields map[string]interface{}
	}{
		{
			name:       "nil metadata",
			metadata:   nil,
			wantFields: map[string]interface{}{},
		},
		{
			name:       "empty metadata",
			metadata:   json.RawMessage([]byte{}),
			wantFields: map[string]interface{}{},
		},
		{
			name:       "invalid JSON metadata",
			metadata:   json.RawMessage([]byte(`not json`)),
			wantFields: map[string]interface{}{},
		},
		{
			name:     "only area_path",
			metadata: json.RawMessage(`{"ado.area_path":"Project\\Team"}`),
			wantFields: map[string]interface{}{
				FieldAreaPath: "Project\\Team",
			},
		},
		{
			name:     "only iteration_path",
			metadata: json.RawMessage(`{"ado.iteration_path":"Sprint 1"}`),
			wantFields: map[string]interface{}{
				FieldIterationPath: "Sprint 1",
			},
		},
		{
			name:     "only story_points",
			metadata: json.RawMessage(`{"ado.story_points":5}`),
			wantFields: map[string]interface{}{
				FieldStoryPoints: float64(5),
			},
		},
		{
			name:     "all metadata fields",
			metadata: json.RawMessage(`{"ado.area_path":"A","ado.iteration_path":"B","ado.story_points":8}`),
			wantFields: map[string]interface{}{
				FieldAreaPath:      "A",
				FieldIterationPath: "B",
				FieldStoryPoints:   float64(8),
			},
		},
		{
			name:       "unrelated metadata keys ignored",
			metadata:   json.RawMessage(`{"ado.rev":3,"custom_field":"value"}`),
			wantFields: map[string]interface{}{},
		},
		{
			name:     "severity metadata restored",
			metadata: json.RawMessage(`{"ado.severity":"2 - High"}`),
			wantFields: map[string]interface{}{
				FieldSeverity: "2 - High",
			},
		},
		{
			name:     "all metadata fields including severity",
			metadata: json.RawMessage(`{"ado.area_path":"A","ado.iteration_path":"B","ado.story_points":8,"ado.severity":"1 - Critical"}`),
			wantFields: map[string]interface{}{
				FieldAreaPath:      "A",
				FieldIterationPath: "B",
				FieldStoryPoints:   float64(8),
				FieldSeverity:      "1 - Critical",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := &types.Issue{Metadata: tt.metadata}
			fields := map[string]interface{}{}
			restoreMetadata(issue, fields)

			for k, want := range tt.wantFields {
				got, ok := fields[k]
				if !ok {
					t.Errorf("expected field %q to be set", k)
					continue
				}
				if got != want {
					t.Errorf("fields[%q] = %v, want %v", k, got, want)
				}
			}
			// Ensure no extra fields were set.
			for k := range fields {
				if _, ok := tt.wantFields[k]; !ok {
					t.Errorf("unexpected field %q = %v", k, fields[k])
				}
			}
		})
	}
}

func TestBlockedStatusRoundTrip(t *testing.T) {
	m := NewFieldMapper(nil, nil)

	// Push direction: blocked beads issue → ADO Active + beads:blocked tag.
	issue := &types.Issue{
		Title:    "Blocked task",
		Status:   types.StatusBlocked,
		Priority: 2,
		Labels:   []string{"urgent"},
	}
	fields := m.IssueToTracker(issue)
	if fields[FieldState] != "Active" {
		t.Errorf("push: State = %v, want %q", fields[FieldState], "Active")
	}
	tagStr, ok := fields[FieldTags].(string)
	if !ok {
		t.Fatal("push: Tags field missing")
	}
	if !hasBeadsTag(tagStr, "beads:blocked") {
		t.Errorf("push: Tags = %q, want beads:blocked present", tagStr)
	}

	// Pull direction: ADO Active + beads:blocked → beads blocked.
	wi := &WorkItem{
		ID: 99,
		Fields: map[string]interface{}{
			FieldTitle:        "Blocked task",
			FieldState:        "Active",
			FieldPriority:     float64(3),
			FieldWorkItemType: "Task",
			FieldTags:         "urgent; beads:blocked",
		},
	}
	ti := &tracker.TrackerIssue{ID: "99", Raw: wi}
	conv := m.IssueToBeads(ti)
	if conv == nil {
		t.Fatal("pull: IssueToBeads returned nil")
	}
	if conv.Issue.Status != types.StatusBlocked {
		t.Errorf("pull: Status = %q, want %q", conv.Issue.Status, types.StatusBlocked)
	}
	// beads:blocked should be filtered from user-visible labels.
	for _, l := range conv.Issue.Labels {
		if l == "beads:blocked" {
			t.Error("pull: beads:blocked should not appear in Labels")
		}
	}
}

func TestBlockedStatusPush_NoLabels(t *testing.T) {
	m := NewFieldMapper(nil, nil)

	// Blocked issue with no labels should still get beads:blocked tag.
	issue := &types.Issue{
		Title:    "Blocked no labels",
		Status:   types.StatusBlocked,
		Priority: 2,
	}
	fields := m.IssueToTracker(issue)
	tagStr, ok := fields[FieldTags].(string)
	if !ok || !hasBeadsTag(tagStr, "beads:blocked") {
		t.Errorf("Tags = %v, want beads:blocked present", fields[FieldTags])
	}
}

func TestActiveWithoutBlockedTag_StaysInProgress(t *testing.T) {
	m := NewFieldMapper(nil, nil)

	wi := &WorkItem{
		ID: 100,
		Fields: map[string]interface{}{
			FieldTitle:        "Active task",
			FieldState:        "Active",
			FieldPriority:     float64(2),
			FieldWorkItemType: "Task",
			FieldTags:         "frontend",
		},
	}
	ti := &tracker.TrackerIssue{ID: "100", Raw: wi}
	conv := m.IssueToBeads(ti)
	if conv.Issue.Status != types.StatusInProgress {
		t.Errorf("Status = %q, want %q", conv.Issue.Status, types.StatusInProgress)
	}
}

func TestPriorityRoundTrip(t *testing.T) {
	m := NewFieldMapper(nil, nil)

	tests := []struct {
		name          string
		beadsPriority int
		wantADO       int
		wantRoundTrip int
	}{
		{"priority 3 round-trips", 3, 4, 3},
		{"priority 4 round-trips", 4, 4, 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Push: beads → ADO.
			issue := &types.Issue{
				Title:    "Priority test",
				Priority: tt.beadsPriority,
				Status:   types.StatusOpen,
			}
			fields := m.IssueToTracker(issue)
			if fields[FieldPriority] != tt.wantADO {
				t.Fatalf("push: ADO priority = %v, want %d", fields[FieldPriority], tt.wantADO)
			}

			// Verify beads_priority stored in metadata.
			var meta map[string]interface{}
			if err := json.Unmarshal(issue.Metadata, &meta); err != nil {
				t.Fatalf("push: failed to unmarshal metadata: %v", err)
			}
			wantBP := json.Number(fmt.Sprintf("%d", tt.beadsPriority))
			gotBP, ok := meta["beads_priority"].(string)
			if !ok {
				t.Fatalf("push: beads_priority not found in metadata, got %v", meta["beads_priority"])
			}
			if gotBP != wantBP.String() {
				t.Fatalf("push: beads_priority = %q, want %q", gotBP, wantBP)
			}

			// Pull: ADO → beads with beads_priority in TrackerIssue metadata.
			wi := &WorkItem{
				ID: 50,
				Fields: map[string]interface{}{
					FieldTitle:        "Priority test",
					FieldState:        "New",
					FieldPriority:     float64(tt.wantADO),
					FieldWorkItemType: "Task",
				},
			}
			ti := &tracker.TrackerIssue{
				ID:  "50",
				Raw: wi,
				Metadata: map[string]interface{}{
					"beads_priority": fmt.Sprintf("%d", tt.beadsPriority),
				},
			}
			conv := m.IssueToBeads(ti)
			if conv == nil {
				t.Fatal("pull: IssueToBeads returned nil")
			}
			if conv.Issue.Priority != tt.wantRoundTrip {
				t.Errorf("pull: Priority = %d, want %d", conv.Issue.Priority, tt.wantRoundTrip)
			}
		})
	}
}

func TestPriorityNoMetadata_DefaultsTo3(t *testing.T) {
	m := NewFieldMapper(nil, nil)

	// ADO priority 4 without beads_priority metadata → defaults to beads 3.
	wi := &WorkItem{
		ID: 51,
		Fields: map[string]interface{}{
			FieldTitle:        "No metadata",
			FieldState:        "New",
			FieldPriority:     float64(4),
			FieldWorkItemType: "Task",
		},
	}
	ti := &tracker.TrackerIssue{ID: "51", Raw: wi}
	conv := m.IssueToBeads(ti)
	if conv.Issue.Priority != 3 {
		t.Errorf("Priority = %d, want 3", conv.Issue.Priority)
	}
}

func TestSeverityRoundTrip(t *testing.T) {
	m := NewFieldMapper(nil, nil)

	// Pull: ADO Bug with Severity → beads issue with severity in metadata.
	wi := &WorkItem{
		ID:  60,
		Rev: 1,
		URL: "https://dev.azure.com/org/proj/_apis/wit/workItems/60",
		Fields: map[string]interface{}{
			FieldTitle:        "Crash on login",
			FieldState:        "New",
			FieldPriority:     float64(1),
			FieldWorkItemType: "Bug",
			FieldSeverity:     "2 - High",
		},
	}
	ti := &tracker.TrackerIssue{ID: "60", Raw: wi}
	conv := m.IssueToBeads(ti)
	if conv == nil {
		t.Fatal("pull: IssueToBeads returned nil")
	}

	// Verify severity is preserved in metadata.
	var meta map[string]interface{}
	if err := json.Unmarshal(conv.Issue.Metadata, &meta); err != nil {
		t.Fatalf("pull: failed to unmarshal metadata: %v", err)
	}
	if meta["ado.severity"] != "2 - High" {
		t.Errorf("pull: ado.severity = %v, want %q", meta["ado.severity"], "2 - High")
	}

	// Push: beads bug issue → ADO fields should include Severity.
	fields := m.IssueToTracker(conv.Issue)
	sev, ok := fields[FieldSeverity].(string)
	if !ok {
		t.Fatal("push: Severity field missing")
	}
	// The metadata-restored severity ("2 - High") should take precedence.
	if sev != "2 - High" {
		t.Errorf("push: Severity = %q, want %q", sev, "2 - High")
	}
}

func TestHasBeadsTag(t *testing.T) {
	tests := []struct {
		name   string
		tagStr string
		tag    string
		want   bool
	}{
		{"present", "urgent; beads:blocked; frontend", "beads:blocked", true},
		{"absent", "urgent; frontend", "beads:blocked", false},
		{"empty string", "", "beads:blocked", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasBeadsTag(tt.tagStr, tt.tag)
			if got != tt.want {
				t.Errorf("hasBeadsTag(%q, %q) = %v, want %v", tt.tagStr, tt.tag, got, tt.want)
			}
		})
	}
}
