package notion

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestBuildPagePropertiesAndPulledIssueRoundTrip(t *testing.T) {
	t.Parallel()

	externalRef := "https://www.notion.so/Task-0123456789abcdef0123456789abcdef"
	issue := &types.Issue{
		ID:          "bd-123",
		Title:       "Sync from Notion",
		Description: "Short summary",
		Status:      types.StatusInProgress,
		Priority:    1,
		IssueType:   types.TypeFeature,
		Assignee:    "osamu",
		Labels:      []string{"sync", "notion"},
		ExternalRef: &externalRef,
	}

	pushIssue, err := PushIssueFromIssue(issue, nil)
	if err != nil {
		t.Fatalf("PushIssueFromIssue returned error: %v", err)
	}
	if pushIssue.Status != "In Progress" {
		t.Fatalf("status = %q, want %q", pushIssue.Status, "In Progress")
	}
	if pushIssue.ExternalRef != "https://www.notion.so/0123456789abcdef0123456789abcdef" {
		t.Fatalf("external_ref = %q", pushIssue.ExternalRef)
	}

	properties := BuildPageProperties(pushIssue)
	statusProp, ok := properties[PropertyStatus].(map[string]interface{})
	if !ok {
		t.Fatalf("status property type = %T", properties[PropertyStatus])
	}
	selectProp, ok := statusProp["select"].(map[string]interface{})
	if !ok || selectProp["name"] != "In Progress" {
		t.Fatalf("status select = %#v", statusProp["select"])
	}

	createdAt := time.Date(2026, 3, 19, 14, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(5 * time.Minute)
	page := Page{
		ID:             "01234567-89ab-cdef-0123-456789abcdef",
		URL:            "https://www.notion.so/Task-0123456789abcdef0123456789abcdef?pvs=4",
		CreatedTime:    createdAt,
		LastEditedTime: updatedAt,
		Properties: map[string]PageProperty{
			PropertyTitle:       {Title: []RichText{{PlainText: "Sync from Notion"}}},
			PropertyBeadsID:     {RichText: []RichText{{PlainText: "bd-123"}}},
			PropertyStatus:      {Select: &SelectOption{Name: "In Progress"}},
			PropertyPriority:    {Select: &SelectOption{Name: "High"}},
			PropertyType:        {Select: &SelectOption{Name: "Feature"}},
			PropertyDescription: {RichText: []RichText{{PlainText: "Short summary"}}},
			PropertyAssignee:    {RichText: []RichText{{PlainText: "osamu"}}},
			PropertyLabels:      {MultiSelect: []SelectOption{{Name: "sync"}, {Name: "notion"}}},
		},
	}

	pulled := PulledIssueFromPage(page)
	if pulled.ExternalRef != "https://www.notion.so/0123456789abcdef0123456789abcdef" {
		t.Fatalf("external ref = %q", pulled.ExternalRef)
	}

	beadsIssue, err := BeadsIssueFromPullIssue(pulled, nil)
	if err != nil {
		t.Fatalf("BeadsIssueFromPullIssue returned error: %v", err)
	}
	if beadsIssue.Status != types.StatusInProgress {
		t.Fatalf("status = %q, want %q", beadsIssue.Status, types.StatusInProgress)
	}
	if beadsIssue.Priority != 1 {
		t.Fatalf("priority = %d, want 1", beadsIssue.Priority)
	}
	if beadsIssue.IssueType != types.TypeFeature {
		t.Fatalf("issue type = %q, want %q", beadsIssue.IssueType, types.TypeFeature)
	}
	if len(beadsIssue.Labels) != 2 {
		t.Fatalf("labels = %v, want 2 labels", beadsIssue.Labels)
	}
}

func TestValidateDataSourceSchema(t *testing.T) {
	t.Parallel()

	ds := &DataSource{
		Properties: map[string]DataSourceProperty{
			PropertyTitle:       {Type: "title"},
			PropertyBeadsID:     {Type: "rich_text"},
			PropertyStatus:      {Type: "select"},
			PropertyPriority:    {Type: "select"},
			PropertyType:        {Type: "select"},
			PropertyDescription: {Type: "rich_text"},
			PropertyAssignee:    {Type: "rich_text"},
		},
	}

	schema := ValidateDataSourceSchema(ds)
	if !schema.Checked {
		t.Fatal("expected schema to be checked")
	}
	if len(schema.Missing) != 1 || schema.Missing[0] != PropertyLabels {
		t.Fatalf("missing = %v, want [%q]", schema.Missing, PropertyLabels)
	}
}
