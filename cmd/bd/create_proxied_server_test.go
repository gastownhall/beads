package main

import (
	"encoding/json"
	"fmt"
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
		issue := nodeIssueFromInput(t, GraphApplyNode{Key: "n", Title: "N"}, createInput{createdBy: "t"})
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

// TestBuildDomainGraphPlan_AliasesAndDeps pins the proxied path's handling of
// the parent/estimate aliases and per-node deps — all three were silently
// dropped before reaching the domain apply (review finding).
func TestBuildDomainGraphPlan_AliasesAndDeps(t *testing.T) {
	est := 90
	canonical := 30
	plan := GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "root", Title: "Root"},
			{Key: "child", Title: "Child", Parent: "root", Estimate: &est,
				Deps: []GraphApplyNodeDep{{Target: "root"}, {Target: "ext-1", Type: "related"}}},
			{Key: "both", Title: "Both", EstimatedMinutes: &canonical, Estimate: &est},
		},
	}

	got, err := buildDomainGraphPlan(plan, createInput{createdBy: "t"})
	if err != nil {
		t.Fatalf("buildDomainGraphPlan: %v", err)
	}

	c := got.Nodes[1]
	if c.ParentKey != "root" {
		t.Errorf("parent alias not folded into ParentKey: %q", c.ParentKey)
	}
	if c.Issue.EstimatedMinutes == nil || *c.Issue.EstimatedMinutes != 90 {
		t.Errorf("estimate alias lost: %v", c.Issue.EstimatedMinutes)
	}
	if len(c.Deps) != 2 {
		t.Fatalf("deps len = %d, want 2", len(c.Deps))
	}
	if c.Deps[0].Target != "root" || c.Deps[0].Type != types.DepBlocks {
		t.Errorf("dep 0 = %+v, want target root type blocks", c.Deps[0])
	}
	if c.Deps[1].Target != "ext-1" || c.Deps[1].Type != types.DependencyType("related") {
		t.Errorf("dep 1 = %+v", c.Deps[1])
	}

	b := got.Nodes[2]
	if b.Issue.EstimatedMinutes == nil || *b.Issue.EstimatedMinutes != 30 {
		t.Errorf("estimated_minutes should win over the alias: %v", b.Issue.EstimatedMinutes)
	}
}

// TestBuildDomainGraphPlanCoversEdgeFields pins the hand-maintained field
// copies in buildDomainGraphPlan: every GraphApplyEdge and GraphApplyNodeDep
// field must survive the projection into its domain counterpart. This is the
// edge-side twin of TestGraphApplyNodeCoversCreateIssueParams — the proxied
// path silently dropping fields (gate/spawner/thread, node deps) is the
// regression class this branch fixes.
func TestBuildDomainGraphPlanCoversEdgeFields(t *testing.T) {
	fill := func(typ reflect.Type) reflect.Value {
		v := reflect.New(typ).Elem()
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if f.Type.Kind() != reflect.String {
				t.Fatalf("%s field %s: unhandled kind %s; extend this parity test", typ.Name(), f.Name, f.Type.Kind())
			}
			v.Field(i).SetString(fmt.Sprintf("v%d", i))
		}
		return v
	}
	assertCopied := func(src reflect.Value, dst reflect.Value) {
		typ := src.Type()
		for i := 0; i < typ.NumField(); i++ {
			name := typ.Field(i).Name
			df := dst.FieldByName(name)
			if !df.IsValid() {
				t.Errorf("domain.%s has no field %q for %s.%s", dst.Type().Name(), name, typ.Name(), name)
				continue
			}
			if got, want := df.String(), src.Field(i).String(); got != want {
				t.Errorf("%s.%s = %q after projection, want %q (dropped in buildDomainGraphPlan?)", dst.Type().Name(), name, got, want)
			}
		}
	}

	edge := fill(reflect.TypeOf(GraphApplyEdge{}))
	dep := fill(reflect.TypeOf(GraphApplyNodeDep{}))
	plan := GraphApplyPlan{
		Nodes: []GraphApplyNode{{Key: "k", Title: "T",
			Deps: []GraphApplyNodeDep{dep.Interface().(GraphApplyNodeDep)}}},
		Edges: []GraphApplyEdge{edge.Interface().(GraphApplyEdge)},
	}
	got, err := buildDomainGraphPlan(plan, createInput{createdBy: "t"})
	if err != nil {
		t.Fatalf("buildDomainGraphPlan: %v", err)
	}
	assertCopied(edge, reflect.ValueOf(got.Edges[0]))
	assertCopied(dep, reflect.ValueOf(got.Nodes[0].Deps[0]))
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
