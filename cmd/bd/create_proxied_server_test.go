package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func TestBuildCreateIssueFromInput_PopulatesAllFields(t *testing.T) {
	due := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	defer1 := time.Now().UTC().Add(24 * time.Hour)
	est := 90
	meta := json.RawMessage(`{"k":"v"}`)

	in := createInput{
		explicitID:         "bd-1",
		title:              "Title",
		description:        "Desc",
		design:             "Design",
		acceptanceCriteria: "Accept",
		notes:              "Notes",
		specID:             "spec-1",
		priority:           1,
		issueType:          "feat",
		assignee:           "alice",
		externalRef:        "gh-9",
		estimatedMinutes:   &est,
		ephemeral:          true,
		noHistory:          false,
		createdBy:          "tester",
		owner:              "tester@example.com",
		molType:            types.MolType("work"),
		wispType:           types.WispType("heartbeat"),
		eventCategory:      "patrol.muted",
		eventActor:         "agent:foo",
		eventTarget:        "bd-2",
		eventPayload:       `{"x":1}`,
		dueAt:              &due,
		deferUntil:         &defer1,
		metadata:           meta,
	}

	got := buildCreateIssueFromInput(in)

	if got.ID != "bd-1" {
		t.Errorf("ID = %q, want bd-1", got.ID)
	}
	if got.Title != "Title" {
		t.Errorf("Title = %q", got.Title)
	}
	if got.IssueType != types.TypeFeature {
		t.Errorf("IssueType = %q, want feature (normalized from feat)", got.IssueType)
	}
	if got.Priority != 1 {
		t.Errorf("Priority = %d", got.Priority)
	}
	if got.Status != types.StatusDeferred {
		t.Errorf("Status = %q, want %q", got.Status, types.StatusDeferred)
	}
	if got.ExternalRef == nil || *got.ExternalRef != "gh-9" {
		t.Errorf("ExternalRef = %v, want pointer to gh-9", got.ExternalRef)
	}
	if got.EstimatedMinutes == nil || *got.EstimatedMinutes != 90 {
		t.Errorf("EstimatedMinutes = %v, want 90", got.EstimatedMinutes)
	}
	if !got.Ephemeral {
		t.Errorf("Ephemeral = false, want true")
	}
	if got.CreatedBy != "tester" || got.Owner != "tester@example.com" {
		t.Errorf("identity fields wrong: %q / %q", got.CreatedBy, got.Owner)
	}
	if got.MolType != types.MolType("work") || got.WispType != types.WispType("heartbeat") {
		t.Errorf("mol/wisp wrong: %q / %q", got.MolType, got.WispType)
	}
	if got.EventKind != "patrol.muted" || got.Actor != "agent:foo" || got.Target != "bd-2" || got.Payload != `{"x":1}` {
		t.Errorf("event fields wrong: %+v", got)
	}
	if got.DueAt == nil || !got.DueAt.Equal(due) {
		t.Errorf("DueAt = %v, want %v", got.DueAt, due)
	}
	if got.DeferUntil == nil || !got.DeferUntil.Equal(defer1) {
		t.Errorf("DeferUntil = %v, want %v", got.DeferUntil, defer1)
	}
	if string(got.Metadata) != `{"k":"v"}` {
		t.Errorf("Metadata = %s", string(got.Metadata))
	}
}

func TestBuildCreateIssueFromInput_EmptyExternalRefIsNilPointer(t *testing.T) {
	got := buildCreateIssueFromInput(createInput{title: "T", priority: 2, issueType: "task"})
	if got.ExternalRef != nil {
		t.Errorf("ExternalRef = %v, want nil for empty input", got.ExternalRef)
	}
}

func TestBuildCreateIssueFromInput_ExplicitStatusWinsOverDefer(t *testing.T) {
	deferUntil := time.Now().UTC().Add(24 * time.Hour)
	got := buildCreateIssueFromInput(createInput{
		title:      "T",
		priority:   2,
		issueType:  "task",
		status:     "blocked",
		deferUntil: &deferUntil,
	})
	if got.Status != types.StatusBlocked {
		t.Errorf("Status = %q, want %q", got.Status, types.StatusBlocked)
	}
	if got.DeferUntil == nil || !got.DeferUntil.Equal(deferUntil) {
		t.Errorf("DeferUntil = %v, want %v", got.DeferUntil, deferUntil)
	}
}

func TestMaterializeGraphNodeIssue_DefaultsAndOpts(t *testing.T) {
	t.Run("type and priority defaults", func(t *testing.T) {
		issue, err := materializeGraphNodeIssue(GraphApplyNode{Key: "n", Title: "N"}, createInput{createdBy: "t"})
		if err != nil {
			t.Fatalf("materializeGraphNodeIssue: %v", err)
		}
		if issue.IssueType != types.TypeTask {
			t.Errorf("type default = %q, want task", issue.IssueType)
		}
		if issue.Priority != 2 {
			t.Errorf("priority default = %d, want 2", issue.Priority)
		}
		if issue.Status != types.StatusOpen {
			t.Errorf("status = %q, want open", issue.Status)
		}
	})

	t.Run("explicit priority and type", func(t *testing.T) {
		p := 0
		issue, err := materializeGraphNodeIssue(GraphApplyNode{
			Key: "n", Title: "N", Type: "bug", Priority: &p,
		}, createInput{})
		if err != nil {
			t.Fatalf("materializeGraphNodeIssue: %v", err)
		}
		if issue.IssueType != types.TypeBug {
			t.Errorf("type = %q, want bug", issue.IssueType)
		}
		if issue.Priority != 0 {
			t.Errorf("priority = %d, want 0", issue.Priority)
		}
	})

	t.Run("ephemeral and no-history propagate", func(t *testing.T) {
		issue, err := materializeGraphNodeIssue(GraphApplyNode{Key: "n", Title: "N"}, createInput{
			ephemeral: true,
			noHistory: false,
		})
		if err != nil {
			t.Fatalf("materializeGraphNodeIssue: %v", err)
		}
		if !issue.Ephemeral {
			t.Errorf("ephemeral not propagated")
		}
		issue2, err := materializeGraphNodeIssue(GraphApplyNode{Key: "n", Title: "N"}, createInput{
			noHistory: true,
		})
		if err != nil {
			t.Fatalf("materializeGraphNodeIssue: %v", err)
		}
		if !issue2.NoHistory {
			t.Errorf("no_history not propagated")
		}
	})

	t.Run("metadata marshalled to JSON", func(t *testing.T) {
		issue, err := materializeGraphNodeIssue(GraphApplyNode{
			Key: "n", Title: "N",
			Metadata: map[string]string{"a": "1", "b": "2"},
		}, createInput{})
		if err != nil {
			t.Fatalf("materializeGraphNodeIssue: %v", err)
		}
		var roundTrip map[string]string
		if err := json.Unmarshal(issue.Metadata, &roundTrip); err != nil {
			t.Fatalf("metadata not valid JSON: %v", err)
		}
		if roundTrip["a"] != "1" || roundTrip["b"] != "2" {
			t.Errorf("metadata round-trip wrong: %v", roundTrip)
		}
	})

	t.Run("empty metadata leaves Metadata nil", func(t *testing.T) {
		issue, err := materializeGraphNodeIssue(GraphApplyNode{Key: "n", Title: "N"}, createInput{})
		if err != nil {
			t.Fatalf("materializeGraphNodeIssue: %v", err)
		}
		if issue.Metadata != nil {
			t.Errorf("Metadata = %s, want nil for empty input", string(issue.Metadata))
		}
	})

	t.Run("identity fields copied", func(t *testing.T) {
		issue, err := materializeGraphNodeIssue(GraphApplyNode{Key: "n", Title: "N"}, createInput{
			createdBy: "alice",
			owner:     "alice@example.com",
		})
		if err != nil {
			t.Fatalf("materializeGraphNodeIssue: %v", err)
		}
		if issue.CreatedBy != "alice" || issue.Owner != "alice@example.com" {
			t.Errorf("identity copy wrong: %q / %q", issue.CreatedBy, issue.Owner)
		}
	})
}

func TestBuildDomainGraphPlan(t *testing.T) {
	plan := GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "root", Title: "Root", Type: "epic"},
			{Key: "child", Title: "Child", ParentKey: "root", Assignee: "bob", AssignAfterCreate: true,
				MetadataRefs: map[string]string{"parent_id": "root"}, Labels: []string{"a", "b"}},
		},
		Edges: []GraphApplyEdge{
			{FromKey: "child", ToKey: "root", Type: ""},
			{FromKey: "child", ToKey: "root", Type: "related"},
			{FromID: "ext-1", ToID: "ext-2", Type: "blocks"},
		},
	}

	got, err := buildDomainGraphPlan(plan, createInput{createdBy: "t"})
	if err != nil {
		t.Fatalf("buildDomainGraphPlan: %v", err)
	}

	if len(got.Nodes) != 2 {
		t.Fatalf("nodes len = %d", len(got.Nodes))
	}
	if got.Nodes[0].Key != "root" || got.Nodes[0].Issue == nil || got.Nodes[0].Issue.IssueType != types.TypeEpic {
		t.Errorf("root node wrong: %+v", got.Nodes[0])
	}
	c := got.Nodes[1]
	if c.ParentKey != "root" {
		t.Errorf("child ParentKey = %q", c.ParentKey)
	}
	if c.Assignee != "bob" || !c.AssignAfterCreate {
		t.Errorf("child assignee/defer wrong: %+v", c)
	}
	if !reflect.DeepEqual(c.Labels, []string{"a", "b"}) {
		t.Errorf("child labels = %v", c.Labels)
	}
	if c.MetadataRefs["parent_id"] != "root" {
		t.Errorf("child metadata_refs lost: %v", c.MetadataRefs)
	}

	if len(got.Edges) != 3 {
		t.Fatalf("edges len = %d", len(got.Edges))
	}
	if got.Edges[0].Type != types.DepBlocks {
		t.Errorf("empty edge type = %q, want blocks", got.Edges[0].Type)
	}
	if got.Edges[1].Type != types.DependencyType("related") {
		t.Errorf("typed edge = %q", got.Edges[1].Type)
	}
	if got.Edges[2].FromID != "ext-1" || got.Edges[2].ToID != "ext-2" {
		t.Errorf("ID edge lost: %+v", got.Edges[2])
	}
}

func TestParseMarkdownDepSpecs(t *testing.T) {
	tests := []struct {
		name    string
		in      []string
		want    []domain.DependencySpec
		wantErr bool
	}{
		{"empty", nil, nil, false},
		{"whitespace skipped", []string{"  ", ""}, nil, false},
		{"bare id → blocks edge", []string{"bd-1"},
			[]domain.DependencySpec{{Type: types.DepBlocks, TargetID: "bd-1"}}, false},
		{"type:id preserved verbatim (no alias)", []string{"depends-on:bd-2"},
			[]domain.DependencySpec{{Type: types.DependencyType("depends-on"), TargetID: "bd-2"}}, false},
		{"discovered-from preserved", []string{"discovered-from:bd-3"},
			[]domain.DependencySpec{{Type: types.DepDiscoveredFrom, TargetID: "bd-3"}}, false},
		{"whitespace trimmed", []string{"  blocks : bd-4 "},
			[]domain.DependencySpec{{Type: types.DepBlocks, TargetID: "bd-4"}}, false},
		{"empty type rejected", []string{":bd-1"}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseMarkdownDepSpecs(tt.in, "Test Title")
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestParseMarkdownDepSpecs_DoesNotSwapBlocks(t *testing.T) {
	got, err := parseMarkdownDepSpecs([]string{"blocks:bd-5"}, "T")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	want := []domain.DependencySpec{{Type: types.DepBlocks, TargetID: "bd-5"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v (no swap-direction)", got, want)
	}
}

func TestResolveProxiedCustomTypes_PrefersDBTypes(t *testing.T) {
	got := resolveProxiedCustomTypes([]string{"db-a", "db-b"})
	if !reflect.DeepEqual(got, []string{"db-a", "db-b"}) {
		t.Errorf("got %#v, want [db-a db-b] — DB types must win when non-empty", got)
	}
}

func TestResolveProxiedCustomTypes_FallsBackToYAML(t *testing.T) {
	restore := withTestYAMLCustomTypes(t, "molecule,gate,convoy")
	defer restore()

	for _, dbTypes := range [][]string{nil, {}} {
		got := resolveProxiedCustomTypes(dbTypes)
		if !reflect.DeepEqual(got, []string{"molecule", "gate", "convoy"}) {
			t.Errorf("dbTypes=%v: got %#v, want YAML fallback [molecule gate convoy]", dbTypes, got)
		}
	}
}

func TestResolveProxiedCustomTypes_EmptyEverywhere(t *testing.T) {
	restore := withTestYAMLCustomTypes(t, "")
	defer restore()

	got := resolveProxiedCustomTypes(nil)
	if len(got) != 0 {
		t.Errorf("expected empty result, got %#v", got)
	}
}

func withTestYAMLCustomTypes(t *testing.T, customCSV string) func() {
	t.Helper()
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	var content string
	if customCSV != "" {
		content = "types:\n  custom: \"" + customCSV + "\"\n"
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	t.Chdir(tmpDir)

	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	return func() { config.ResetForTesting() }
}
