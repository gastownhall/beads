package main

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
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
	if got.Description != "Desc" || got.Design != "Design" || got.AcceptanceCriteria != "Accept" || got.Notes != "Notes" || got.SpecID != "spec-1" {
		t.Errorf("content fields = %+v", got)
	}
	if got.IssueType != types.TypeFeature {
		t.Errorf("IssueType = %q, want feature (normalized from feat)", got.IssueType)
	}
	if got.Priority != 1 {
		t.Errorf("Priority = %d", got.Priority)
	}
	if got.Assignee != "alice" {
		t.Errorf("Assignee = %q, want alice", got.Assignee)
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
	if !got.Ephemeral || got.NoHistory {
		t.Errorf("storage flags = ephemeral:%t no_history:%t, want true:false", got.Ephemeral, got.NoHistory)
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

// nodeIssueFromInput mirrors buildDomainGraphPlan's per-node materialization
// so unit tests can exercise graphApplyNodeIssue with createInput-level opts.
func nodeIssueFromInput(t *testing.T, node GraphApplyNode, in createInput) *types.Issue {
	t.Helper()
	issue, err := graphApplyNodeIssue(node, in.graphApplyOptions(), in.createdBy, in.owner)
	if err != nil {
		t.Fatalf("graphApplyNodeIssue: %v", err)
	}
	return issue
}

func TestGraphApplyNodeIssue_DefaultsAndOpts(t *testing.T) {
	t.Run("type and priority defaults", func(t *testing.T) {
		node := GraphApplyNode{Key: "n", Title: "N"}
		issue := nodeIssueFromInput(t, node, createInput{createdBy: "t"})
		if issue.IssueType != types.TypeTask {
			t.Errorf("type default = %q, want task", issue.IssueType)
		}
		// A node without a priority leaves it to the role's default
		// (CreateItem.DefaultPriority), so the materialized issue carries none;
		// the dry-run preview shows the library default the apply will store.
		if issue.Priority != 0 {
			t.Errorf("materialized priority = %d, want 0 (the role applies the default)", issue.Priority)
		}
		if got := graphApplyPreviewPriority(node, issue); got != issueops.DefaultCreatePriority {
			t.Errorf("preview priority = %d, want the library default %d", got, issueops.DefaultCreatePriority)
		}
		if issue.Status != types.StatusOpen {
			t.Errorf("status = %q, want open", issue.Status)
		}
	})

	t.Run("explicit priority and type", func(t *testing.T) {
		p := 0
		issue := nodeIssueFromInput(t, GraphApplyNode{
			Key: "n", Title: "N", Type: "bug", Priority: &p,
		}, createInput{})
		if issue.IssueType != types.TypeBug {
			t.Errorf("type = %q, want bug", issue.IssueType)
		}
		if issue.Priority != 0 {
			t.Errorf("priority = %d, want 0", issue.Priority)
		}
	})

	t.Run("ephemeral and no-history propagate", func(t *testing.T) {
		issue := nodeIssueFromInput(t, GraphApplyNode{Key: "n", Title: "N"}, createInput{
			ephemeral: true,
			noHistory: false,
		})
		if !issue.Ephemeral {
			t.Errorf("ephemeral not propagated")
		}
		issue2 := nodeIssueFromInput(t, GraphApplyNode{Key: "n", Title: "N"}, createInput{
			noHistory: true,
		})
		if !issue2.NoHistory {
			t.Errorf("no_history not propagated")
		}
	})

	t.Run("per-node storage class overrides plan flags", func(t *testing.T) {
		off := false
		issue := nodeIssueFromInput(t, GraphApplyNode{Key: "n", Title: "N", Ephemeral: &off}, createInput{
			ephemeral: true,
		})
		if issue.Ephemeral {
			t.Errorf("node-level ephemeral=false should override --ephemeral")
		}
		on := true
		issue2 := nodeIssueFromInput(t, GraphApplyNode{Key: "n", Title: "N", NoHistory: &on}, createInput{})
		if !issue2.NoHistory {
			t.Errorf("node-level no_history=true not applied")
		}
	})

	t.Run("conflicting effective storage class errors", func(t *testing.T) {
		on := true
		_, err := graphApplyNodeIssue(GraphApplyNode{Key: "n", Title: "N", NoHistory: &on}, GraphApplyOptions{Ephemeral: true}, "", "")
		if err == nil {
			t.Fatal("expected error for effective ephemeral+no_history")
		}
	})

	t.Run("metadata marshalled to JSON", func(t *testing.T) {
		issue := nodeIssueFromInput(t, GraphApplyNode{
			Key: "n", Title: "N",
			Metadata: map[string]json.RawMessage{"a": json.RawMessage(`"1"`), "b": json.RawMessage(`2`)},
		}, createInput{})
		var roundTrip map[string]any
		if err := json.Unmarshal(issue.Metadata, &roundTrip); err != nil {
			t.Fatalf("metadata not valid JSON: %v", err)
		}
		if roundTrip["a"] != "1" || roundTrip["b"] != float64(2) {
			t.Errorf("metadata round-trip wrong: %v", roundTrip)
		}
	})

	t.Run("empty metadata leaves Metadata nil", func(t *testing.T) {
		issue := nodeIssueFromInput(t, GraphApplyNode{Key: "n", Title: "N"}, createInput{})
		if issue.Metadata != nil {
			t.Errorf("Metadata = %s, want nil for empty input", string(issue.Metadata))
		}
	})

	t.Run("identity fields copied", func(t *testing.T) {
		issue := nodeIssueFromInput(t, GraphApplyNode{Key: "n", Title: "N"}, createInput{
			createdBy: "alice",
			owner:     "alice@example.com",
		})
		if issue.CreatedBy != "alice" || issue.Owner != "alice@example.com" {
			t.Errorf("identity copy wrong: %q / %q", issue.CreatedBy, issue.Owner)
		}
	})

	t.Run("node owner overrides ambient owner", func(t *testing.T) {
		issue := nodeIssueFromInput(t, GraphApplyNode{Key: "n", Title: "N", Owner: "bob@example.com"}, createInput{
			owner: "alice@example.com",
		})
		if issue.Owner != "bob@example.com" {
			t.Errorf("Owner = %q, want node override", issue.Owner)
		}
	})

	t.Run("native content and planning fields copied", func(t *testing.T) {
		est := 90
		issue := nodeIssueFromInput(t, GraphApplyNode{
			Key: "n", Title: "N",
			Design:             "d",
			AcceptanceCriteria: "ac",
			Notes:              "notes",
			SpecID:             "spec-1",
			ExternalRef:        "gh-9",
			EstimatedMinutes:   &est,
			WispType:           "heartbeat",
			MolType:            "swarm",
			Pinned:             true,
			Status:             "in_progress",
			ID:                 "bd-abc123",
		}, createInput{})
		if issue.Design != "d" || issue.AcceptanceCriteria != "ac" || issue.Notes != "notes" || issue.SpecID != "spec-1" {
			t.Errorf("content fields lost: %+v", issue)
		}
		if issue.ExternalRef == nil || *issue.ExternalRef != "gh-9" {
			t.Errorf("ExternalRef = %v", issue.ExternalRef)
		}
		if issue.EstimatedMinutes == nil || *issue.EstimatedMinutes != 90 {
			t.Errorf("EstimatedMinutes = %v", issue.EstimatedMinutes)
		}
		if issue.WispType != types.WispType("heartbeat") || issue.MolType != types.MolType("swarm") {
			t.Errorf("wisp/mol type lost: %q %q", issue.WispType, issue.MolType)
		}
		if !issue.Pinned {
			t.Errorf("Pinned lost")
		}
		if issue.Status != types.StatusInProgress {
			t.Errorf("Status = %q, want in_progress", issue.Status)
		}
		if issue.ID != "bd-abc123" {
			t.Errorf("ID = %q, want explicit ID", issue.ID)
		}
	})
}

func TestParseMarkdownDependencies(t *testing.T) {
	tests := []struct {
		name    string
		in      []string
		want    []issueops.CreateDependency
		wantErr bool
	}{
		{"empty", nil, nil, false},
		{"whitespace skipped", []string{"  ", ""}, nil, false},
		{"bare id → blocks edge", []string{"bd-1"},
			[]issueops.CreateDependency{{Type: types.DepBlocks, TargetID: "bd-1"}}, false},
		{"type:id preserved verbatim (no alias)", []string{"depends-on:bd-2"},
			[]issueops.CreateDependency{{Type: types.DependencyType("depends-on"), TargetID: "bd-2"}}, false},
		{"discovered-from preserved", []string{"discovered-from:bd-3"},
			[]issueops.CreateDependency{{Type: types.DepDiscoveredFrom, TargetID: "bd-3"}}, false},
		{"whitespace trimmed", []string{"  blocks : bd-4 "},
			[]issueops.CreateDependency{{Type: types.DepBlocks, TargetID: "bd-4"}}, false},
		{"empty type rejected", []string{":bd-1"}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseMarkdownDependencies(tt.in, "Test Title")
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

func TestParseMarkdownDependencies_DoesNotSwapBlocks(t *testing.T) {
	got, err := parseMarkdownDependencies([]string{"blocks:bd-5"}, "T")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	want := []issueops.CreateDependency{{Type: types.DepBlocks, TargetID: "bd-5"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v (no swap-direction)", got, want)
	}
}
