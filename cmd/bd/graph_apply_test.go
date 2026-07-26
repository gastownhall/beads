package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestDocsGraphPlanExampleValidates pins the --graph doc supplement's example
// to the validator: the canonical example must stay a plan `bd create --graph`
// actually accepts (review caught it shipping with event fields on a task
// node and edges duplicating parent_key). The embedded supplement is the
// source of truth; the docs drift gate keeps the generated CLI reference
// byte-equal to it.
func TestDocsGraphPlanExampleValidates(t *testing.T) {
	const fence = "```json\n"
	start := strings.Index(createGraphPlanSupplement, fence)
	if start < 0 {
		t.Fatal("example JSON block not found in create_graph_plan.md supplement")
	}
	rest := createGraphPlanSupplement[start+len(fence):]
	end := strings.Index(rest, "```")
	if end < 0 {
		t.Fatal("example JSON block not terminated")
	}
	planJSON := []byte(rest[:end])

	if unknown := detectUnknownGraphFields(planJSON); len(unknown) > 0 {
		t.Errorf("documented example uses unknown fields: %v", unknown)
	}
	var plan GraphApplyPlan
	if err := json.Unmarshal(planJSON, &plan); err != nil {
		t.Fatalf("documented example is not valid plan JSON: %v", err)
	}
	if err := validateGraphApplyPlan(&plan, nil, nil, GraphApplyOptions{}); err != nil {
		t.Errorf("documented example rejected by validator: %v", err)
	}
	if _, err := validateGraphApplyStorageClasses(&plan, GraphApplyOptions{}, false); err != nil {
		t.Errorf("documented example rejected by storage-class validation: %v", err)
	}
}

func TestValidateGraphApplyPlanAcceptsCustomTypes(t *testing.T) {
	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "root", Title: "Workflow", Type: "task"},
			{Key: "spec", Title: "Step spec", Type: "spec"},
		},
	}
	if err := validateGraphApplyPlan(plan, []string{"spec"}, nil, GraphApplyOptions{}); err != nil {
		t.Fatalf("expected custom type %q to validate, got %v", "spec", err)
	}
}

func TestValidateGraphApplyPlanRejectsTypeWhenCustomTypesAbsent(t *testing.T) {
	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "spec", Title: "Step spec", Type: "spec"},
		},
	}
	err := validateGraphApplyPlan(plan, nil, nil, GraphApplyOptions{})
	if err == nil {
		t.Fatal("expected custom type to fail when nil customTypes")
	}
	want := `node "spec": invalid issue type: spec`
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

func TestValidateGraphApplyPlanRejectsInvalidTypes(t *testing.T) {
	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "root", Title: "Root", Type: "definitely-not-a-type"},
		},
	}
	err := validateGraphApplyPlan(plan, nil, nil, GraphApplyOptions{})
	if err == nil {
		t.Fatal("expected error for invalid type")
	}
	want := `node "root": invalid issue type: definitely-not-a-type`
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

func TestValidateGraphApplyPlanAcceptsBuiltInTypes(t *testing.T) {
	for _, typ := range []string{"task", "bug", "feature", "epic", "chore", "decision"} {
		plan := &GraphApplyPlan{
			Nodes: []GraphApplyNode{
				{Key: "n1", Title: "Node", Type: typ},
			},
		}
		if err := validateGraphApplyPlan(plan, nil, nil, GraphApplyOptions{}); err != nil {
			t.Errorf("type %q rejected: %v", typ, err)
		}
	}
}

func TestValidateGraphApplyPlanAcceptsEmptyType(t *testing.T) {
	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "n1", Title: "Node", Type: ""},
		},
	}
	if err := validateGraphApplyPlan(plan, nil, nil, GraphApplyOptions{}); err != nil {
		t.Fatalf("empty type rejected: %v", err)
	}
}

// TestValidateGraphApplyPlanAcceptsNewFields verifies that estimate,
// external_ref, parent (alias), and deps are accepted without error. (GH#4064)
func TestValidateGraphApplyPlanAcceptsNewFields(t *testing.T) {
	est := 120
	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{
				Key:         "epic",
				Title:       "Epic node",
				Type:        "epic",
				Estimate:    &est,
				ExternalRef: "gh-42",
			},
			{
				Key:    "child",
				Title:  "Child node",
				Parent: "epic",
				Deps: []GraphApplyNodeDep{
					{Target: "epic", Type: "blocks"},
				},
			},
		},
	}
	if err := validateGraphApplyPlan(plan, nil, nil, GraphApplyOptions{}); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

// TestValidateGraphApplyPlanRejectsNegativeEstimate verifies that a negative
// estimate (via the alias) is caught at validation time — even when
// estimated_minutes is also set and would win the alias fold, so the bad
// value can't be silently discarded by the precedence. (GH#4064)
func TestValidateGraphApplyPlanRejectsNegativeEstimate(t *testing.T) {
	neg := -5
	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "n", Title: "Node", Estimate: &neg},
		},
	}
	err := validateGraphApplyPlan(plan, nil, nil, GraphApplyOptions{})
	if err == nil {
		t.Fatal("expected error for negative estimate")
	}
	if !strings.Contains(err.Error(), "estimate cannot be negative") {
		t.Fatalf("unexpected error: %v", err)
	}

	canonical := 10
	plan.Nodes[0].EstimatedMinutes = &canonical
	err = validateGraphApplyPlan(plan, nil, nil, GraphApplyOptions{})
	if err == nil {
		t.Fatal("expected error for negative estimate alongside estimated_minutes")
	}
	if !strings.Contains(err.Error(), "estimate cannot be negative") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestValidateGraphApplyPlanRejectsEmptyDepTarget verifies that a dep with an
// empty target is caught at validation time. (GH#4064)
func TestValidateGraphApplyPlanRejectsEmptyDepTarget(t *testing.T) {
	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "n", Title: "Node", Deps: []GraphApplyNodeDep{{Target: ""}}},
		},
	}
	err := validateGraphApplyPlan(plan, nil, nil, GraphApplyOptions{})
	if err == nil {
		t.Fatal("expected error for empty dep target")
	}
	if !strings.Contains(err.Error(), "empty target") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestValidateGraphApplyPlanParentAliasResolvesCorrectly verifies that the
// "parent" field works as an alias for "parent_key" in validation. (GH#4064)
func TestValidateGraphApplyPlanParentAliasResolvesCorrectly(t *testing.T) {
	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "root", Title: "Root"},
			{Key: "child", Title: "Child", Parent: "root"},
		},
	}
	if err := validateGraphApplyPlan(plan, nil, nil, GraphApplyOptions{}); err != nil {
		t.Fatalf("parent alias should resolve: %v", err)
	}
}

// TestValidateGraphApplyPlanParentAliasRejectsUnknownKey verifies that the
// "parent" field rejects unknown keys just like "parent_key". (GH#4064)
func TestValidateGraphApplyPlanParentAliasRejectsUnknownKey(t *testing.T) {
	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "child", Title: "Child", Parent: "nonexistent"},
		},
	}
	err := validateGraphApplyPlan(plan, nil, nil, GraphApplyOptions{})
	if err == nil {
		t.Fatal("expected error for unknown parent key via alias")
	}
	if !strings.Contains(err.Error(), "parent key") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestEmitGraphApplyDryRun_ParentAlias verifies that the "parent" alias
// is counted and displayed in dry-run output. (GH#4064)
func TestEmitGraphApplyDryRun_ParentAlias(t *testing.T) {
	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "root", Title: "Root", Type: "epic"},
			{Key: "c1", Title: "Child 1", Parent: "root"},
		},
	}
	out := captureStdout(t, func() error {
		emitGraphApplyDryRun(plan, GraphApplyOptions{})
		return nil
	})
	if !strings.Contains(out, "1 parent-child link(s)") {
		t.Errorf("dry-run should count parent alias as parent-child link:\n%s", out)
	}
	if !strings.Contains(out, "parent_key=root") {
		t.Errorf("dry-run should display resolved parent_key from alias:\n%s", out)
	}
}

// TestDetectUnknownGraphFields_ReporterRepro reproduces the schema-mismatch
// pattern from GH#3367: the user passes 'parent' (a string) and 'blocks' (an
// array) directly on nodes, expecting them to wire hierarchy/dependencies.
// json.Unmarshal silently drops them. detectUnknownGraphFields must surface
// both fields, scoped to the offending nodes.
func TestDetectUnknownGraphFields_ReporterRepro(t *testing.T) {
	// After GH#4064, "parent" is a recognized alias for "parent_key".
	// Only "blocks" (an array, not an edge or dep) remains unknown.
	planJSON := []byte(`{
        "nodes": [
            {"key": "root",   "type": "epic", "title": "Root epic",    "priority": 2},
            {"key": "child1", "type": "task", "title": "Child task 1", "parent": "root", "priority": 2, "blocks": ["child2"]},
            {"key": "child2", "type": "task", "title": "Child task 2", "parent": "root", "priority": 2}
        ]
    }`)

	got := detectUnknownGraphFields(planJSON)
	want := map[string][]string{
		`node["child1"]`: {"blocks"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("detectUnknownGraphFields:\n got=%#v\nwant=%#v", got, want)
	}
}

// TestDetectUnknownGraphFields_KnownSchemaIsClean verifies that a plan using
// only the documented schema (parent_key, edges array) reports no unknowns.
// Guards against the schema lists drifting from the GraphApplyPlan/Node/Edge
// json tags.
func TestDetectUnknownGraphFields_KnownSchemaIsClean(t *testing.T) {
	planJSON := []byte(`{
        "commit_message": "test",
        "nodes": [
            {"key": "root", "title": "Root", "type": "epic", "priority": 2,
             "description": "d", "assignee": "alice", "assign_after_create": false,
             "estimate": 60, "labels": ["a"], "metadata": {"k": "v"},
             "metadata_refs": {"r": "root"}, "external_ref": "gh-1",
             "deps": [{"target": "child", "type": "blocks"}]},
            {"key": "child", "title": "Child", "parent_key": "root",
             "parent_id": "ext-1", "parent": "root"}
        ],
        "edges": [
            {"from_key": "child", "to_key": "root", "type": "blocks"},
            {"from_id": "ext-1", "to_id": "ext-2", "type": "related"}
        ]
    }`)

	if got := detectUnknownGraphFields(planJSON); len(got) != 0 {
		t.Fatalf("expected no unknown fields for canonical schema, got %#v", got)
	}
}

// TestDetectUnknownGraphFields_PlanAndEdgeLevel verifies coverage at the plan
// top level and edge level, not just node level.
func TestDetectUnknownGraphFields_PlanAndEdgeLevel(t *testing.T) {
	planJSON := []byte(`{
        "version": "1.0",
        "nodes": [{"key": "n", "title": "n"}],
        "edges": [{"from_key": "n", "to_key": "n", "weight": 5}]
    }`)

	got := detectUnknownGraphFields(planJSON)
	if !reflect.DeepEqual(got["plan"], []string{"version"}) {
		t.Errorf("plan-level unknowns: got=%v want=[version]", got["plan"])
	}
	if !reflect.DeepEqual(got["edge[0]"], []string{"weight"}) {
		t.Errorf("edge-level unknowns: got=%v want=[weight]", got["edge[0]"])
	}
}

// TestDetectUnknownGraphFields_BadJSON returns empty rather than panicking
// when the plan can't be parsed at the top level. Callers run the strict
// json.Unmarshal afterwards and surface the parse error there.
func TestDetectUnknownGraphFields_BadJSON(t *testing.T) {
	if got := detectUnknownGraphFields([]byte(`{not json`)); len(got) != 0 {
		t.Fatalf("expected empty map for bad JSON, got %#v", got)
	}
}

// TestWarnUnknownGraphFields_HintsForReporterFields asserts that the hint
// text for the two highest-friction fields ('parent', 'blocks' from GH#3367)
// is emitted and points the user at the canonical schema field.
func TestWarnUnknownGraphFields_HintsForReporterFields(t *testing.T) {
	// After GH#4064, "parent" is a recognized field. Only "blocks" triggers a hint.
	var buf bytes.Buffer
	hinted := warnUnknownGraphFields(&buf, map[string][]string{
		`node["c1"]`: {"blocks"},
	})

	out := buf.String()
	if !strings.Contains(out, `unknown field(s): [blocks]`) {
		t.Errorf("warning missing field list: %q", out)
	}
	if !strings.Contains(out, "deps") {
		t.Errorf("expected 'blocks' hint to mention deps: %q", out)
	}

	got := append([]string(nil), hinted...)
	sort.Strings(got)
	want := []string{"blocks"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("hinted fields: got=%v want=%v", got, want)
	}
}

// TestWarnUnknownGraphFields_NoUnknownsIsSilent verifies the warning function
// emits nothing when the input map is empty (the common path for well-formed
// plans).
func TestWarnUnknownGraphFields_NoUnknownsIsSilent(t *testing.T) {
	var buf bytes.Buffer
	warnUnknownGraphFields(&buf, nil)
	if buf.Len() != 0 {
		t.Fatalf("expected silent on empty input, wrote: %q", buf.String())
	}
}

// TestEmitGraphApplyDryRun_Counts verifies the dry-run preview reports the
// node count, edge count, and parent-link count without performing any
// writes. Captures stdout (the dry-run path writes to stdout, with warnings
// going to stderr from the upstream caller).
func TestEmitGraphApplyDryRun_Counts(t *testing.T) {
	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "root", Title: "Root", Type: "epic"},
			{Key: "c1", Title: "Child 1", ParentKey: "root"},
			{Key: "c2", Title: "Child 2", ParentKey: "root"},
		},
		Edges: []GraphApplyEdge{
			{FromKey: "c1", ToKey: "c2", Type: "blocks"},
		},
	}

	out := captureStdout(t, func() error {
		emitGraphApplyDryRun(plan, GraphApplyOptions{})
		return nil
	})

	if !strings.Contains(out, "would create 3 issue(s) and 1 edge(s) (2 parent-child link(s))") {
		t.Errorf("dry-run summary missing or wrong:\n%s", out)
	}
	for _, want := range []string{"root", "c1", "c2", "parent_key=root", "live create may still reject parent-child blocking paths"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run missing %q in output:\n%s", want, out)
		}
	}
}

func TestGraphApplyOptionsValidateRejectsEphemeralNoHistory(t *testing.T) {
	err := (GraphApplyOptions{Ephemeral: true, NoHistory: true}).Validate()
	if err == nil {
		t.Fatal("expected mutually exclusive graph options to be rejected")
	}
	if got, want := err.Error(), "ephemeral and no_history are mutually exclusive"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestValidateGraphApplyPlanRejectsLocalBlockingCycle(t *testing.T) {
	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "a", Title: "A", Type: "task"},
			{Key: "b", Title: "B", Type: "task"},
			{Key: "c", Title: "C", Type: "task"},
		},
		Edges: []GraphApplyEdge{
			{FromKey: "a", ToKey: "b", Type: "blocks"},
			{FromKey: "b", ToKey: "c", Type: "conditional-blocks"},
			{FromKey: "c", ToKey: "a", Type: "blocks"},
		},
	}

	err := validateGraphApplyPlan(plan, nil, nil, GraphApplyOptions{})
	if err == nil {
		t.Fatal("expected local graph cycle to be rejected")
	}
	if got, want := err.Error(), "graph contains a blocking dependency cycle"; !strings.Contains(got, want) {
		t.Fatalf("error = %q, want to contain %q", got, want)
	}
}

func TestValidateGraphApplyPlanReportsDeterministicCycleNode(t *testing.T) {
	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "a", Title: "A", Type: "task"},
			{Key: "b", Title: "B", Type: "task"},
			{Key: "c", Title: "C", Type: "task"},
		},
		Edges: []GraphApplyEdge{
			{FromKey: "b", ToKey: "c", Type: "blocks"},
			{FromKey: "c", ToKey: "a", Type: "blocks"},
			{FromKey: "a", ToKey: "b", Type: "blocks"},
		},
	}

	err := validateGraphApplyPlan(plan, nil, nil, GraphApplyOptions{})
	if err == nil {
		t.Fatal("expected local graph cycle to be rejected")
	}
	if got, want := err.Error(), `graph contains a blocking dependency cycle involving node "a"`; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestValidateGraphApplyPlanAllowsNonBlockingLocalCycle(t *testing.T) {
	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "a", Title: "A", Type: "task"},
			{Key: "b", Title: "B", Type: "task"},
		},
		Edges: []GraphApplyEdge{
			{FromKey: "a", ToKey: "b", Type: "related"},
			{FromKey: "b", ToKey: "a", Type: "related"},
		},
	}

	if err := validateGraphApplyPlan(plan, nil, nil, GraphApplyOptions{}); err != nil {
		t.Fatalf("non-blocking cycle rejected: %v", err)
	}
}

func TestValidateGraphApplyPlanRejectsImplicitParentChildReverseBlockingCycle(t *testing.T) {
	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "root", Title: "Root", Type: "epic"},
			{Key: "child", Title: "Child", Type: "task", ParentKey: "root"},
		},
		Edges: []GraphApplyEdge{
			{FromKey: "root", ToKey: "child", Type: "blocks"},
		},
	}

	err := validateGraphApplyPlan(plan, nil, nil, GraphApplyOptions{})
	if err == nil {
		t.Fatal("expected implicit parent-child plus reverse blocking edge to be rejected")
	}
	if got, want := err.Error(), "blocking dependency cycle"; !strings.Contains(got, want) {
		t.Fatalf("error = %q, want to contain %q", got, want)
	}
}

func TestValidateGraphApplyPlanIgnoresIDOverridesForLocalCycleValidation(t *testing.T) {
	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "a", Title: "A", Type: "task"},
			{Key: "b", Title: "B", Type: "task"},
		},
		Edges: []GraphApplyEdge{
			{FromKey: "a", FromID: "bd-existing", ToKey: "b", Type: "blocks"},
			{FromKey: "b", ToKey: "a", Type: "blocks"},
		},
	}

	if err := validateGraphApplyPlan(plan, nil, nil, GraphApplyOptions{}); err != nil {
		t.Fatalf("ID override edge should not be treated as a local key cycle: %v", err)
	}
}

func TestGraphApplyEdgeIsLocalCycleRelevantOnlyForLocalBlockingEdges(t *testing.T) {
	tests := []struct {
		name string
		edge GraphApplyEdge
		typ  string
		want bool
	}{
		{name: "local default blocks", edge: GraphApplyEdge{FromKey: "a", ToKey: "b"}, want: true},
		{name: "local conditional blocks", edge: GraphApplyEdge{FromKey: "a", ToKey: "b"}, typ: "conditional-blocks", want: true},
		{name: "local nonblocking", edge: GraphApplyEdge{FromKey: "a", ToKey: "b"}, typ: "related"},
		{name: "existing id target", edge: GraphApplyEdge{FromKey: "a", ToID: "bd-123"}, want: false},
		{name: "explicit id overrides key", edge: GraphApplyEdge{FromKey: "a", FromID: "bd-1", ToKey: "b"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := graphApplyEdgeIsLocalCycleRelevant(tt.edge, graphApplyDependencyType(tt.typ))
			if got != tt.want {
				t.Fatalf("localCycleRelevant = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGraphApplyParentDepPairs(t *testing.T) {
	nodes := []GraphApplyNode{
		{Key: "root", Title: "Root"},
		{Key: "child", Title: "Child", ParentKey: "root"},
		{Key: "external-child", Title: "External child", ParentID: "bd-parent"},
	}
	keyToID := map[string]string{
		"root":           "bd-root",
		"child":          "bd-child",
		"external-child": "bd-external-child",
	}

	pairs := graphApplyParentDepPairs(nodes, keyToID)
	for _, pair := range []struct {
		child  string
		parent string
	}{
		{"bd-child", "bd-root"},
		{"bd-external-child", "bd-parent"},
	} {
		if !pairs[graphApplyDepPairKey(pair.child, pair.parent)] {
			t.Fatalf("missing parent dep pair %s -> %s", pair.child, pair.parent)
		}
	}
	if pairs[graphApplyDepPairKey("bd-root", "bd-child")] {
		t.Fatal("unexpected reverse parent dep pair")
	}
}

// TestEmitGraphApplyDryRun_JSON verifies that the dry-run path emits valid
// JSON with the expected structure when jsonOutput is set. This exercises the
// code path that `bd create --graph --dry-run --json` takes, confirming the
// GraphApplyDryRun struct serializes correctly and that no persistence occurs
// (emitGraphApplyDryRun only formats output — it never touches the store).
// Regression coverage for GH#3893.
func TestEmitGraphApplyDryRun_JSON(t *testing.T) {
	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "root", Title: "Root epic", Type: "epic"},
			{Key: "c1", Title: "First child", ParentKey: "root"},
			{Key: "c2", Title: "Second child", ParentKey: "root"},
		},
		Edges: []GraphApplyEdge{
			{FromKey: "c1", ToKey: "c2", Type: "blocks"},
		},
	}

	oldJSON := jsonOutput
	jsonOutput = true
	defer func() { jsonOutput = oldJSON }()

	out := captureStdout(t, func() error {
		emitGraphApplyDryRun(plan, GraphApplyOptions{})
		return nil
	})

	var result GraphApplyDryRun
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("dry-run JSON output is not valid JSON: %v\nraw: %s", err, out)
	}

	if !result.DryRun {
		t.Error("expected dry_run=true in JSON output")
	}
	if result.NodeCount != 3 {
		t.Errorf("node_count = %d, want 3", result.NodeCount)
	}
	if result.EdgeCount != 1 {
		t.Errorf("edge_count = %d, want 1", result.EdgeCount)
	}
	if result.ParentDeps != 2 {
		t.Errorf("parent_deps = %d, want 2", result.ParentDeps)
	}
	if len(result.Nodes) != 3 {
		t.Fatalf("nodes length = %d, want 3", len(result.Nodes))
	}
	// Verify node details are populated correctly.
	rootRow := result.Nodes[0]
	if rootRow.Key != "root" || rootRow.Title != "Root epic" || rootRow.Type != "epic" {
		t.Errorf("root node row = %+v, want key=root title=Root epic type=epic", rootRow)
	}
	c1Row := result.Nodes[1]
	if c1Row.ParentKey != "root" {
		t.Errorf("c1 parent_key = %q, want %q", c1Row.ParentKey, "root")
	}
	// Default priority is P2.
	if rootRow.Priority != 2 {
		t.Errorf("root priority = %d, want 2 (default)", rootRow.Priority)
	}
}

// TestCreateIssuesFromGraph_DryRunDoesNotPersist verifies that
// createIssuesFromGraph with dryRun=true produces dry-run output and does NOT
// call executeGraphApply. Since store is nil (no database initialized), any
// attempt to persist would panic; a successful dry-run completion proves the
// guard is effective. Regression test for GH#3893.
func TestCreateIssuesFromGraph_DryRunDoesNotPersist(t *testing.T) {
	// Ensure store is nil — any persistence attempt would panic.
	oldStore := store
	store = nil
	defer func() { store = oldStore }()

	oldJSON := jsonOutput
	jsonOutput = true
	defer func() { jsonOutput = oldJSON }()

	planJSON := `{
		"nodes": [
			{"key": "a", "title": "Task A", "type": "task"},
			{"key": "b", "title": "Task B", "type": "task", "parent_key": "a"}
		],
		"edges": [
			{"from_key": "b", "to_key": "a", "type": "blocks"}
		]
	}`

	planFile := t.TempDir() + "/plan.json"
	if err := os.WriteFile(planFile, []byte(planJSON), 0o600); err != nil {
		t.Fatalf("write plan file: %v", err)
	}

	out := captureStdout(t, func() error {
		createIssuesFromGraph(planFile, true, GraphApplyOptions{})
		return nil
	})

	var result GraphApplyDryRun
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("output is not valid dry-run JSON: %v\nraw: %s", err, out)
	}
	if !result.DryRun {
		t.Error("expected dry_run=true")
	}
	if result.NodeCount != 2 {
		t.Errorf("node_count = %d, want 2", result.NodeCount)
	}
	// The fixture combines b's parent_key="a" with an explicit b--blocks-->a
	// edge. The dry-run preview counts these independently and does not dedupe
	// the overlapping relationship, so edge_count and parent_deps are each 1.
	// Pin both to catch any regression that starts merging or dropping them.
	if result.EdgeCount != 1 {
		t.Errorf("edge_count = %d, want 1", result.EdgeCount)
	}
	if result.ParentDeps != 1 {
		t.Errorf("parent_deps = %d, want 1", result.ParentDeps)
	}
}

// singleNodePlanErr validates a one-node plan built by mutate and returns the
// validation error (nil when the plan is accepted).
func singleNodePlanErr(customStatuses []string, mutate func(*GraphApplyNode)) error {
	node := GraphApplyNode{Key: "a", Title: "A"}
	mutate(&node)
	plan := &GraphApplyPlan{Nodes: []GraphApplyNode{node}}
	return validateGraphApplyPlan(plan, nil, customStatuses, GraphApplyOptions{})
}

func TestValidateGraphApplyPlanNodeFieldRules(t *testing.T) {
	neg := -5
	on := true
	cases := []struct {
		name           string
		customStatuses []string
		mutate         func(*GraphApplyNode)
		wantErr        string
	}{
		{name: "invalid status", mutate: func(n *GraphApplyNode) { n.Status = "bogus" }, wantErr: "invalid status"},
		{name: "alias type accepted like bd create", mutate: func(n *GraphApplyNode) { n.Type = "feat" }},
		{name: "builtin status accepted", mutate: func(n *GraphApplyNode) { n.Status = "in_progress" }},
		{name: "custom status accepted", customStatuses: []string{"triage"}, mutate: func(n *GraphApplyNode) { n.Status = "triage" }},
		{name: "custom status rejected without config", mutate: func(n *GraphApplyNode) { n.Status = "triage" }, wantErr: "invalid status"},
		{name: "negative estimate", mutate: func(n *GraphApplyNode) { n.EstimatedMinutes = &neg }, wantErr: "estimated_minutes"},
		{name: "priority out of range", mutate: func(n *GraphApplyNode) { p := 9; n.Priority = &p }, wantErr: "priority must be between 0 and 4"},
		{name: "priority bounds accepted", mutate: func(n *GraphApplyNode) { p := 0; n.Priority = &p }},
		{name: "invalid wisp_type", mutate: func(n *GraphApplyNode) { n.WispType = "bogus" }, wantErr: "invalid wisp_type"},
		{name: "valid wisp_type", mutate: func(n *GraphApplyNode) { n.WispType = "heartbeat" }},
		{name: "invalid mol_type", mutate: func(n *GraphApplyNode) { n.MolType = "bogus" }, wantErr: "invalid mol_type"},
		{name: "valid mol_type", mutate: func(n *GraphApplyNode) { n.MolType = "swarm" }},
		{name: "event fields require event type", mutate: func(n *GraphApplyNode) { n.EventKind = "agent.started" }, wantErr: "require type"},
		{name: "event fields on event node", mutate: func(n *GraphApplyNode) {
			n.Type = "event"
			n.EventKind = "agent.started"
			n.Actor = "x"
			n.Target = "y"
			n.Payload = "{}"
		}},
		{name: "node-level ephemeral+no_history conflict", mutate: func(n *GraphApplyNode) { n.Ephemeral = &on; n.NoHistory = &on }, wantErr: "mutually exclusive"},
		{name: "bad explicit id format", mutate: func(n *GraphApplyNode) { n.ID = "nohyphen" }, wantErr: "invalid ID format"},
		{name: "valid explicit id", mutate: func(n *GraphApplyNode) { n.ID = "bd-a1b2c3" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := singleNodePlanErr(tc.customStatuses, tc.mutate)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected plan to validate, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestValidateGraphApplyPlanRejectsDuplicateExplicitIDs(t *testing.T) {
	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "a", Title: "A", ID: "bd-a1b2c3"},
			{Key: "b", Title: "B", ID: "bd-a1b2c3"},
		},
	}
	err := validateGraphApplyPlan(plan, nil, nil, GraphApplyOptions{})
	if err == nil || !strings.Contains(err.Error(), "duplicate explicit id") {
		t.Fatalf("expected duplicate explicit id error, got %v", err)
	}
}

func TestValidateGraphApplyExplicitIDCollisions(t *testing.T) {
	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "a", Title: "A", ID: "bd-taken"},
			{Key: "b", Title: "B"},
		},
	}

	t.Run("nil probe skips", func(t *testing.T) {
		if err := validateGraphApplyExplicitIDCollisions(plan, nil); err != nil {
			t.Fatalf("nil probe: want nil error, got %v", err)
		}
	})

	t.Run("collision rejected with node context", func(t *testing.T) {
		err := validateGraphApplyExplicitIDCollisions(plan, func(id string) (bool, error) {
			return id == "bd-taken", nil
		})
		if err == nil || !strings.Contains(err.Error(), "already exists") ||
			!strings.Contains(err.Error(), "bd-taken") || !strings.Contains(err.Error(), `"a"`) {
			t.Fatalf("expected already-exists error naming id and node, got %v", err)
		}
	})

	t.Run("free id passes and probe skips id-less nodes", func(t *testing.T) {
		var probed []string
		err := validateGraphApplyExplicitIDCollisions(plan, func(id string) (bool, error) {
			probed = append(probed, id)
			return false, nil
		})
		if err != nil {
			t.Fatalf("free id: want nil error, got %v", err)
		}
		if len(probed) != 1 || probed[0] != "bd-taken" {
			t.Fatalf("want exactly the explicit id probed, got %v", probed)
		}
	})

	t.Run("probe error propagates", func(t *testing.T) {
		err := validateGraphApplyExplicitIDCollisions(plan, func(string) (bool, error) {
			return false, fmt.Errorf("store offline")
		})
		if err == nil || !strings.Contains(err.Error(), "store offline") {
			t.Fatalf("expected probe error to propagate, got %v", err)
		}
	})
}

func TestValidateGraphApplyPlanEdgeGateRules(t *testing.T) {
	twoNodes := []GraphApplyNode{
		{Key: "gate", Title: "Gate"},
		{Key: "spawner", Title: "Spawner"},
	}
	cases := []struct {
		name    string
		edge    GraphApplyEdge
		wantErr string
	}{
		{name: "gate requires waits-for", edge: GraphApplyEdge{FromKey: "gate", ToKey: "spawner", Type: "blocks", Gate: "all-children"}, wantErr: "require type"},
		{name: "gate defaults-to-blocks edge rejected", edge: GraphApplyEdge{FromKey: "gate", ToKey: "spawner", Gate: "all-children"}, wantErr: "require type"},
		{name: "invalid gate value", edge: GraphApplyEdge{FromKey: "gate", ToKey: "spawner", Type: "waits-for", Gate: "bogus"}, wantErr: "invalid gate"},
		{name: "spawner_key and spawner_id conflict", edge: GraphApplyEdge{FromKey: "gate", ToKey: "spawner", Type: "waits-for", SpawnerKey: "spawner", SpawnerID: "bd-x"}, wantErr: "cannot specify both"},
		{name: "unknown spawner_key", edge: GraphApplyEdge{FromKey: "gate", ToKey: "spawner", Type: "waits-for", SpawnerKey: "ghost"}, wantErr: "spawner key"},
		{name: "spawner_key must match to_key", edge: GraphApplyEdge{FromKey: "gate", ToKey: "spawner", Type: "waits-for", SpawnerKey: "gate"}, wantErr: "must match to_key"},
		{name: "spawner_key with ambiguous to_id rejected", edge: GraphApplyEdge{FromKey: "gate", ToKey: "spawner", ToID: "bd-ext1", Type: "waits-for", SpawnerKey: "spawner"}, wantErr: "cannot be combined with to_id"},
		{name: "spawner_id must match to_id", edge: GraphApplyEdge{FromKey: "gate", ToID: "bd-ext1", Type: "waits-for", SpawnerID: "bd-other"}, wantErr: "must match to_id"},
		{name: "matching spawner_id accepted", edge: GraphApplyEdge{FromKey: "gate", ToID: "bd-ext1", Type: "waits-for", SpawnerID: "bd-ext1"}},
		{name: "valid gated waits-for edge", edge: GraphApplyEdge{FromKey: "gate", ToKey: "spawner", Type: "waits-for", Gate: "any-children", SpawnerKey: "spawner"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := &GraphApplyPlan{Nodes: twoNodes, Edges: []GraphApplyEdge{tc.edge}}
			err := validateGraphApplyPlan(plan, nil, nil, GraphApplyOptions{})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected plan to validate, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestGraphApplyEdgeDependencyGateMetadata(t *testing.T) {
	resolve := map[string]string{"spawner": "bd-spawn1"}

	t.Run("gate and spawner_key resolve into waits-for metadata", func(t *testing.T) {
		dep, err := types.NewGraphEdgeDependency("bd-from", "bd-to", types.DepWaitsFor, "any-children", "spawner", "", "", resolve)
		if err != nil {
			t.Fatalf("NewGraphEdgeDependency: %v", err)
		}
		var meta types.WaitsForMeta
		if err := json.Unmarshal([]byte(dep.Metadata), &meta); err != nil {
			t.Fatalf("metadata not valid WaitsForMeta JSON: %v", err)
		}
		if meta.Gate != types.WaitsForAnyChildren || meta.SpawnerID != "bd-spawn1" {
			t.Errorf("meta = %+v, want any-children gate with resolved spawner", meta)
		}
	})

	t.Run("gate defaults to all-children when spawner is set", func(t *testing.T) {
		dep, err := types.NewGraphEdgeDependency("bd-from", "bd-to", types.DepWaitsFor, "", "", "bd-ext1", "", resolve)
		if err != nil {
			t.Fatalf("NewGraphEdgeDependency: %v", err)
		}
		var meta types.WaitsForMeta
		if err := json.Unmarshal([]byte(dep.Metadata), &meta); err != nil {
			t.Fatalf("metadata not valid WaitsForMeta JSON: %v", err)
		}
		if meta.Gate != types.WaitsForAllChildren || meta.SpawnerID != "bd-ext1" {
			t.Errorf("meta = %+v, want all-children default with explicit spawner", meta)
		}
	})

	t.Run("plain edge carries no metadata and passes thread_id", func(t *testing.T) {
		dep, err := types.NewGraphEdgeDependency("bd-from", "bd-to", types.DepRepliesTo, "", "", "", "thread-9", resolve)
		if err != nil {
			t.Fatalf("NewGraphEdgeDependency: %v", err)
		}
		if dep.Metadata != "" {
			t.Errorf("Metadata = %q, want empty for ungated edge", dep.Metadata)
		}
		if dep.ThreadID != "thread-9" {
			t.Errorf("ThreadID = %q, want thread-9", dep.ThreadID)
		}
	})

	t.Run("ungated waits-for edge still gets all-children metadata", func(t *testing.T) {
		// '{}' metadata must never be stored for waits-for deps: the gate SQL
		// reads $.gate and NULL poisons its NOT(... AND ...) predicate,
		// unblocking the gate as soon as the first child closes.
		dep, err := types.NewGraphEdgeDependency("bd-from", "bd-to", types.DepWaitsFor, "", "", "", "", resolve)
		if err != nil {
			t.Fatalf("NewGraphEdgeDependency: %v", err)
		}
		var meta types.WaitsForMeta
		if err := json.Unmarshal([]byte(dep.Metadata), &meta); err != nil {
			t.Fatalf("metadata not valid WaitsForMeta JSON (%q): %v", dep.Metadata, err)
		}
		if meta.Gate != types.WaitsForAllChildren {
			t.Errorf("gate = %q, want all-children default for ungated waits-for edge", meta.Gate)
		}
	})

	t.Run("unresolved spawner_key errors instead of writing empty spawner", func(t *testing.T) {
		if _, err := types.NewGraphEdgeDependency("bd-from", "bd-to", types.DepWaitsFor, "", "ghost", "", "", resolve); err == nil {
			t.Fatal("expected error for unresolved spawner key")
		}
	})
}

// TestDetectUnknownGraphFields_CaseInsensitive: encoding/json binds
// case-variant keys to the matching field, so they must not be reported as
// silently dropped — only genuinely unknown names are.
func TestDetectUnknownGraphFields_CaseInsensitive(t *testing.T) {
	planJSON := []byte(`{
        "nodes": [{"key": "n", "title": "N", "Pinned": true, "Bogus": 1}]
    }`)
	got := detectUnknownGraphFields(planJSON)
	if !reflect.DeepEqual(got, map[string][]string{`node["n"]`: {"Bogus"}}) {
		t.Fatalf("case-variant known field misreported: %#v", got)
	}
}

func TestValidateGraphApplyStorageClasses(t *testing.T) {
	on := true
	plan := &GraphApplyPlan{Nodes: []GraphApplyNode{
		{Key: "a", Title: "A"},
		{Key: "b", Title: "B", NoHistory: &on},
	}}

	t.Run("effective flag+node conflict caught at validation time", func(t *testing.T) {
		_, err := validateGraphApplyStorageClasses(plan, GraphApplyOptions{Ephemeral: true}, false)
		if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Fatalf("expected effective ephemeral+no_history conflict, got %v", err)
		}
	})

	t.Run("mixed storage classes rejected only when uniformity is required", func(t *testing.T) {
		if _, err := validateGraphApplyStorageClasses(plan, GraphApplyOptions{}, false); err != nil {
			t.Fatalf("embedded mode allows mixed plans, got %v", err)
		}
		_, err := validateGraphApplyStorageClasses(plan, GraphApplyOptions{}, true)
		if err == nil || !strings.Contains(err.Error(), "uniform") {
			t.Fatalf("expected uniformity error in proxied mode, got %v", err)
		}
	})

	t.Run("resolved useWisp reported from node 0", func(t *testing.T) {
		useWisp, err := validateGraphApplyStorageClasses(&GraphApplyPlan{Nodes: []GraphApplyNode{
			{Key: "a", Title: "A", NoHistory: &on},
			{Key: "b", Title: "B", NoHistory: &on},
		}}, GraphApplyOptions{}, true)
		if err != nil {
			t.Fatalf("uniform wisp plan rejected: %v", err)
		}
		if !useWisp {
			t.Error("useWisp = false, want true for no_history plan")
		}
	})
}

func TestValidateGraphApplyExplicitIDPrefixes(t *testing.T) {
	plan := &GraphApplyPlan{Nodes: []GraphApplyNode{
		{Key: "gen", Title: "Generated"},
		{Key: "pinnedID", Title: "Pinned", ID: "zz-a1b2c3"},
	}}

	if err := validateGraphApplyExplicitIDPrefixes(plan, "bd", "", false); err == nil {
		t.Fatal("expected foreign-prefix explicit id to be rejected")
	} else if !strings.Contains(err.Error(), `node "pinnedID"`) {
		t.Fatalf("error should name the node, got %v", err)
	}
	if err := validateGraphApplyExplicitIDPrefixes(plan, "zz", "", false); err != nil {
		t.Fatalf("matching prefix rejected: %v", err)
	}
	if err := validateGraphApplyExplicitIDPrefixes(plan, "bd", "zz", false); err != nil {
		t.Fatalf("allowed_prefixes rejected: %v", err)
	}
	if err := validateGraphApplyExplicitIDPrefixes(plan, "bd", "", true); err != nil {
		t.Fatalf("--force rejected: %v", err)
	}
}

// TestGraphApplyNodeCoversCreateIssueParams pins full issue-model parity
// between single-issue `bd create` and graph plans: every createIssueParams
// field must be addressable from a GraphApplyNode or listed here with a
// reason. This keeps the schema gap from silently reopening as create grows.
//
// Limitation: the check is name-based — it proves a same-named GraphApplyNode
// field exists, not that graphApplyNodeIssue actually wires it into its
// createIssueParams literal. A new field added everywhere but omitted from
// that literal would still pass here and materialize as a zero value; the
// value round-trip in TestEmbeddedCreate/graph_full_fields is the wiring
// backstop.
func TestGraphApplyNodeCoversCreateIssueParams(t *testing.T) {
	renamed := map[string]string{
		"IssueType":     "Type",
		"InitialStatus": "Status",
	}
	excluded := map[string]string{
		"CreatedBy": "always the ambient actor identity; plans do not impersonate creators",
	}

	nodeType := reflect.TypeOf(GraphApplyNode{})
	paramsType := reflect.TypeOf(createIssueParams{})
	for i := 0; i < paramsType.NumField(); i++ {
		name := paramsType.Field(i).Name
		if _, ok := excluded[name]; ok {
			continue
		}
		if alias, ok := renamed[name]; ok {
			name = alias
		}
		if _, ok := nodeType.FieldByName(name); !ok {
			t.Errorf("createIssueParams field %q is not addressable from graph plans; add it to GraphApplyNode (or the exclusion list with a reason)", name)
		}
	}
}
