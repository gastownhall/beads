package main

import (
	"bytes"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestValidateGraphApplyPlanAcceptsCustomTypes(t *testing.T) {
	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "root", Title: "Workflow", Type: "task"},
			{Key: "spec", Title: "Step spec", Type: "spec"},
		},
	}
	if err := validateGraphApplyPlan(plan, []string{"spec"}); err != nil {
		t.Fatalf("expected custom type %q to validate, got %v", "spec", err)
	}
}

func TestValidateGraphApplyPlanRejectsTypeWhenCustomTypesAbsent(t *testing.T) {
	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "spec", Title: "Step spec", Type: "spec"},
		},
	}
	err := validateGraphApplyPlan(plan, nil)
	if err == nil {
		t.Fatal("expected custom type to fail when nil customTypes")
	}
	want := `node "spec": invalid type "spec"`
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
	err := validateGraphApplyPlan(plan, nil)
	if err == nil {
		t.Fatal("expected error for invalid type")
	}
	want := `node "root": invalid type "definitely-not-a-type"`
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
		if err := validateGraphApplyPlan(plan, nil); err != nil {
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
	if err := validateGraphApplyPlan(plan, nil); err != nil {
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
	if err := validateGraphApplyPlan(plan, nil); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

// TestValidateGraphApplyPlanRejectsNegativeEstimate verifies that a negative
// estimate is caught at validation time. (GH#4064)
func TestValidateGraphApplyPlanRejectsNegativeEstimate(t *testing.T) {
	neg := -5
	plan := &GraphApplyPlan{
		Nodes: []GraphApplyNode{
			{Key: "n", Title: "Node", Estimate: &neg},
		},
	}
	err := validateGraphApplyPlan(plan, nil)
	if err == nil {
		t.Fatal("expected error for negative estimate")
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
	err := validateGraphApplyPlan(plan, nil)
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
	if err := validateGraphApplyPlan(plan, nil); err != nil {
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
	err := validateGraphApplyPlan(plan, nil)
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
		emitGraphApplyDryRun(plan)
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

// TestKnownGraphFieldSetsMatchStructTags is a guardrail: the
// knownGraphPlanFields / knownGraphNodeFields / knownGraphEdgeFields sets
// must match the json tags on the corresponding structs so that adding a
// new field on the schema doesn't silently re-introduce the false-positive
// warning that GH#3367 was trying to remove. Reflection lets us spot drift
// at test time without forcing manual upkeep on the schema author.
func TestKnownGraphFieldSetsMatchStructTags(t *testing.T) {
	check := func(name string, sample interface{}, known map[string]struct{}) {
		t.Helper()
		typ := reflect.TypeOf(sample)
		tagged := make(map[string]struct{})
		for i := 0; i < typ.NumField(); i++ {
			tag := typ.Field(i).Tag.Get("json")
			if tag == "" || tag == "-" {
				continue
			}
			if comma := strings.IndexByte(tag, ','); comma >= 0 {
				tag = tag[:comma]
			}
			tagged[tag] = struct{}{}
		}
		for k := range tagged {
			if _, ok := known[k]; !ok {
				t.Errorf("%s: json tag %q present on struct but missing from known set (would be flagged as unknown)", name, k)
			}
		}
		for k := range known {
			if _, ok := tagged[k]; !ok {
				t.Errorf("%s: %q in known set but not on struct (stale entry)", name, k)
			}
		}
	}
	check("GraphApplyPlan", GraphApplyPlan{}, knownGraphPlanFields)
	check("GraphApplyNode", GraphApplyNode{}, knownGraphNodeFields)
	check("GraphApplyEdge", GraphApplyEdge{}, knownGraphEdgeFields)
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
		emitGraphApplyDryRun(plan)
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

	err := validateGraphApplyPlan(plan, nil)
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

	err := validateGraphApplyPlan(plan, nil)
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

	if err := validateGraphApplyPlan(plan, nil); err != nil {
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

	err := validateGraphApplyPlan(plan, nil)
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

	if err := validateGraphApplyPlan(plan, nil); err != nil {
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
