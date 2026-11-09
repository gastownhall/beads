package main

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// graphReadFake serves the per-issue reads the bd graph loaders fall back on and
// counts them. It answers the way GetIssuesByIDs does: durable issues by id,
// then wisps by id, without the ids that name no issue.
type graphReadFake struct {
	storage.DoltStorage

	issues map[string]*types.Issue
	wisps  map[string]bool
	edges  []*types.Dependency

	// refuse makes the walk reads and the batched record read fail, as bd
	// connect's HTTP backend does.
	refuse bool

	dependencyBatches int
	perIssueWalkReads int
	perIssueRecords   int
}

var errGraphReadRefused = errors.New("read refused")

func newGraphReadFake(ids []string, wisps []string, edges [][2]string) *graphReadFake {
	f := &graphReadFake{issues: make(map[string]*types.Issue), wisps: make(map[string]bool)}
	for _, id := range ids {
		f.issues[id] = &types.Issue{ID: id, Title: id, Status: types.StatusOpen}
	}
	for _, id := range wisps {
		f.issues[id] = &types.Issue{ID: id, Title: id, Status: types.StatusOpen}
		f.wisps[id] = true
	}
	for _, e := range edges {
		f.edges = append(f.edges, &types.Dependency{IssueID: e[0], DependsOnID: e[1], Type: types.DepBlocks})
	}
	return f
}

func (f *graphReadFake) hydrate(ids []string) []*types.Issue {
	var durable, wisps []string
	for _, id := range ids {
		if _, ok := f.issues[id]; !ok {
			continue
		}
		if f.wisps[id] {
			wisps = append(wisps, id)
		} else {
			durable = append(durable, id)
		}
	}
	sort.Strings(durable)
	sort.Strings(wisps)
	var out []*types.Issue
	for _, id := range append(durable, wisps...) {
		out = append(out, f.issues[id])
	}
	return out
}

func (f *graphReadFake) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	return f.issues[id], nil
}

func (f *graphReadFake) GetDependents(_ context.Context, id string) ([]*types.Issue, error) {
	f.perIssueWalkReads++
	if f.refuse {
		return nil, errGraphReadRefused
	}
	var far []string
	for _, e := range f.edges {
		if e.DependsOnID == id {
			far = append(far, e.IssueID)
		}
	}
	return f.hydrate(far), nil
}

func (f *graphReadFake) GetDependencies(_ context.Context, id string) ([]*types.Issue, error) {
	f.perIssueWalkReads++
	if f.refuse {
		return nil, errGraphReadRefused
	}
	var far []string
	for _, e := range f.edges {
		if e.IssueID == id {
			far = append(far, e.DependsOnID)
		}
	}
	return f.hydrate(far), nil
}

func (f *graphReadFake) GetDependencyRecords(_ context.Context, id string) ([]*types.Dependency, error) {
	f.perIssueRecords++
	var out []*types.Dependency
	for _, e := range f.edges {
		if e.IssueID == id {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *graphReadFake) GetDependencyRecordsForIssues(_ context.Context, ids []string) (map[string][]*types.Dependency, error) {
	f.dependencyBatches++
	if f.refuse {
		return nil, errGraphReadRefused
	}
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	out := make(map[string][]*types.Dependency)
	for _, e := range f.edges {
		if wanted[e.IssueID] {
			out[e.IssueID] = append(out[e.IssueID], e)
		}
	}
	return out, nil
}

func (f *graphReadFake) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) ([]*types.Issue, error) {
	if filter.Status == nil || *filter.Status != types.StatusOpen {
		return nil, nil
	}
	ids := make([]string, 0, len(f.issues))
	for id := range f.issues {
		ids = append(ids, id)
	}
	return f.hydrate(ids), nil
}

// graphComponentFake adds the optional component read to graphReadFake.
type graphComponentFake struct {
	*graphReadFake
	component *storage.Component
	err       error
	calls     int
}

func (f *graphComponentFake) ReadComponent(context.Context, *types.Issue) (*storage.Component, error) {
	f.calls++
	return f.component, f.err
}

func graphIssueIDs(issues []*types.Issue) []string {
	ids := make([]string, 0, len(issues))
	for _, issue := range issues {
		ids = append(ids, issue.ID)
	}
	return ids
}

// graphWalkFixture is a three-level component around r. r also points at gone,
// which names no issue.
func graphWalkFixture() *graphReadFake {
	return newGraphReadFake(
		[]string{"r", "m", "z", "c", "d", "s", "y", "k", "b"},
		[]string{"aw"},
		[][2]string{
			{"z", "r"}, {"m", "r"}, {"r", "c"}, {"r", "aw"}, {"r", "gone"},
			{"m", "s"}, {"m", "d"}, {"y", "z"}, {"s", "z"}, {"k", "c"}, {"aw", "b"},
		},
	)
}

const graphWalkFixtureEdges = 10 // every edge but the one to the missing issue

var graphWalkFixtureOrder = []string{"r", "m", "z", "c", "aw", "d", "s", "y", "k", "b"}

func TestLoadGraphSubgraphUsesTheComponentRead(t *testing.T) {
	root := &types.Issue{ID: "r"}
	child := &types.Issue{ID: "c"}
	f := &graphComponentFake{
		graphReadFake: graphWalkFixture(),
		component: &storage.Component{
			Issues: []*types.Issue{root, child},
			Outgoing: map[string][]*types.Dependency{
				"r": {
					{IssueID: "r", DependsOnID: "c", Type: types.DepBlocks},
					{IssueID: "r", DependsOnID: "elsewhere", Type: types.DepBlocks},
				},
			},
		},
	}
	sub, err := loadGraphSubgraph(context.Background(), f, "r")
	if err != nil {
		t.Fatalf("loadGraphSubgraph: %v", err)
	}
	if got, want := graphIssueIDs(sub.Issues), []string{"r", "c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("issues = %v, want %v", got, want)
	}
	if sub.IssueMap["c"] != child {
		t.Errorf("IssueMap[c] = %v, want the issue the component read returned", sub.IssueMap["c"])
	}
	if len(sub.Dependencies) != 1 || sub.Dependencies[0].DependsOnID != "c" {
		t.Errorf("dependencies = %v, want only the edge inside the component", sub.Dependencies)
	}
	if f.perIssueWalkReads != 0 || f.perIssueRecords != 0 {
		t.Errorf("per-issue reads = %d walk, %d records, want none", f.perIssueWalkReads, f.perIssueRecords)
	}
}

func TestLoadGraphSubgraphWalksPerIssueWhenTheComponentReadFails(t *testing.T) {
	f := &graphComponentFake{graphReadFake: graphWalkFixture(), err: errGraphReadRefused}
	sub, err := loadGraphSubgraph(context.Background(), f, "r")
	if err != nil {
		t.Fatalf("loadGraphSubgraph: %v", err)
	}
	if f.calls != 1 {
		t.Errorf("component reads = %d, want 1", f.calls)
	}
	if got := graphIssueIDs(sub.Issues); !reflect.DeepEqual(got, graphWalkFixtureOrder) {
		t.Errorf("issues = %v, want %v", got, graphWalkFixtureOrder)
	}
	if len(sub.Dependencies) != graphWalkFixtureEdges {
		t.Errorf("dependencies = %d, want %d", len(sub.Dependencies), graphWalkFixtureEdges)
	}
}

func TestLoadGraphSubgraphWalksPerIssueWithoutTheCapability(t *testing.T) {
	sub, err := loadGraphSubgraph(context.Background(), graphWalkFixture(), "r")
	if err != nil {
		t.Fatalf("loadGraphSubgraph: %v", err)
	}
	if got := graphIssueIDs(sub.Issues); !reflect.DeepEqual(got, graphWalkFixtureOrder) {
		t.Errorf("issues = %v, want %v", got, graphWalkFixtureOrder)
	}
	if len(sub.Dependencies) != graphWalkFixtureEdges {
		t.Errorf("dependencies = %d, want %d", len(sub.Dependencies), graphWalkFixtureEdges)
	}
}

// bd connect's HTTP backend refuses the walk reads. Its front-door test pins the
// result: exit 0 with the root alone. A refused read must not fail the command.
func TestLoadGraphSubgraphRefusedWalkLeavesRoot(t *testing.T) {
	f := graphWalkFixture()
	f.refuse = true
	sub, err := loadGraphSubgraph(context.Background(), f, "r")
	if err != nil {
		t.Fatalf("loadGraphSubgraph: %v", err)
	}
	if got, want := graphIssueIDs(sub.Issues), []string{"r"}; !reflect.DeepEqual(got, want) {
		t.Errorf("issues = %v, want %v", got, want)
	}
}

func TestLoadAllGraphSubgraphsReadsDependenciesInOneBatch(t *testing.T) {
	f := graphWalkFixture()
	subs, err := loadAllGraphSubgraphs(context.Background(), f, 0, "")
	if err != nil {
		t.Fatalf("loadAllGraphSubgraphs: %v", err)
	}
	if f.perIssueRecords != 0 || f.dependencyBatches != 1 {
		t.Errorf("per-issue reads = %d, batches = %d, want 0 and 1", f.perIssueRecords, f.dependencyBatches)
	}
	var total int
	for _, sub := range subs {
		total += len(sub.Dependencies)
	}
	if total != graphWalkFixtureEdges {
		t.Errorf("dependencies = %d, want %d", total, graphWalkFixtureEdges)
	}
}

func TestLoadAllGraphSubgraphsFallsBackToPerIssueReads(t *testing.T) {
	f := graphWalkFixture()
	f.refuse = true
	subs, err := loadAllGraphSubgraphs(context.Background(), f, 0, "")
	if err != nil {
		t.Fatalf("loadAllGraphSubgraphs: %v", err)
	}
	if f.perIssueRecords != len(f.issues) {
		t.Errorf("per-issue reads = %d, want %d", f.perIssueRecords, len(f.issues))
	}
	var total int
	for _, sub := range subs {
		total += len(sub.Dependencies)
	}
	if total != graphWalkFixtureEdges {
		t.Errorf("dependencies = %d, want %d", total, graphWalkFixtureEdges)
	}
}

func TestTruncateTitle(t *testing.T) {
	tests := []struct {
		name   string
		title  string
		maxLen int
		want   string
	}{
		{
			name:   "no truncation needed",
			title:  "Short title",
			maxLen: 20,
			want:   "Short title",
		},
		{
			name:   "exact length",
			title:  "Exact",
			maxLen: 5,
			want:   "Exact",
		},
		{
			name:   "needs truncation",
			title:  "This is a very long title that needs to be truncated",
			maxLen: 20,
			want:   "This is a very long…",
		},
		{
			name:   "unicode safe",
			title:  "日本語タイトル",
			maxLen: 5,
			want:   "日本語タ…",
		},
		{
			name:   "empty string",
			title:  "",
			maxLen: 10,
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateTitle(tt.title, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateTitle(%q, %d) = %q, want %q", tt.title, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestPadRight(t *testing.T) {
	tests := []struct {
		name  string
		s     string
		width int
		want  string
	}{
		{
			name:  "needs padding",
			s:     "abc",
			width: 6,
			want:  "abc   ",
		},
		{
			name:  "exact width",
			s:     "exact",
			width: 5,
			want:  "exact",
		},
		{
			name:  "truncates when too long",
			s:     "toolong",
			width: 4,
			want:  "tool",
		},
		{
			name:  "empty string",
			s:     "",
			width: 3,
			want:  "   ",
		},
		{
			name:  "unicode safe",
			s:     "日本",
			width: 5,
			want:  "日本   ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := padRight(tt.s, tt.width)
			if got != tt.want {
				t.Errorf("padRight(%q, %d) = %q, want %q", tt.s, tt.width, got, tt.want)
			}
		})
	}
}

func TestRenderNodeBox(t *testing.T) {
	tests := []struct {
		name     string
		node     *GraphNode
		width    int
		notEmpty bool
	}{
		{
			name: "open status",
			node: &GraphNode{
				Issue: &types.Issue{
					ID:     "test-1",
					Title:  "Test Issue",
					Status: types.StatusOpen,
				},
			},
			width:    20,
			notEmpty: true,
		},
		{
			name: "in progress status",
			node: &GraphNode{
				Issue: &types.Issue{
					ID:     "test-2",
					Title:  "In Progress Issue",
					Status: types.StatusInProgress,
				},
			},
			width:    25,
			notEmpty: true,
		},
		{
			name: "blocked status",
			node: &GraphNode{
				Issue: &types.Issue{
					ID:     "test-3",
					Title:  "Blocked Issue",
					Status: types.StatusBlocked,
				},
			},
			width:    20,
			notEmpty: true,
		},
		{
			name: "closed status",
			node: &GraphNode{
				Issue: &types.Issue{
					ID:     "test-4",
					Title:  "Closed Issue",
					Status: types.StatusClosed,
				},
			},
			width:    20,
			notEmpty: true,
		},
		{
			name: "unknown status",
			node: &GraphNode{
				Issue: &types.Issue{
					ID:     "test-5",
					Title:  "Unknown Status",
					Status: "unknown",
				},
			},
			width:    20,
			notEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderNodeBox(tt.node, tt.width)
			if tt.notEmpty && len(got) == 0 {
				t.Error("renderNodeBox() returned empty string")
			}
			// Verify the output contains expected elements
			if !contains(got, tt.node.Issue.ID) {
				t.Errorf("renderNodeBox() output missing issue ID %s", tt.node.Issue.ID)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && stringContains(s, substr)))
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestComputeDependencyCounts(t *testing.T) {
	t.Run("nil subgraph", func(t *testing.T) {
		blocks, blockedBy := computeDependencyCounts(nil)
		if len(blocks) != 0 {
			t.Errorf("expected empty blocks map for nil subgraph, got %d entries", len(blocks))
		}
		if len(blockedBy) != 0 {
			t.Errorf("expected empty blockedBy map for nil subgraph, got %d entries", len(blockedBy))
		}
	})

	t.Run("empty subgraph", func(t *testing.T) {
		subgraph := &TemplateSubgraph{
			Root:         &types.Issue{ID: "root-1"},
			Issues:       []*types.Issue{},
			Dependencies: []*types.Dependency{},
			IssueMap:     make(map[string]*types.Issue),
		}
		blocks, blockedBy := computeDependencyCounts(subgraph)
		if len(blocks) != 0 {
			t.Errorf("expected empty blocks map, got %d entries", len(blocks))
		}
		if len(blockedBy) != 0 {
			t.Errorf("expected empty blockedBy map, got %d entries", len(blockedBy))
		}
	})

	t.Run("single blocking dependency", func(t *testing.T) {
		subgraph := &TemplateSubgraph{
			Root: &types.Issue{ID: "root-1"},
			Dependencies: []*types.Dependency{
				{IssueID: "issue-2", DependsOnID: "issue-1", Type: types.DepBlocks},
			},
		}
		blocks, blockedBy := computeDependencyCounts(subgraph)
		if blocks["issue-1"] != 1 {
			t.Errorf("expected issue-1 to block 1, got %d", blocks["issue-1"])
		}
		if blockedBy["issue-2"] != 1 {
			t.Errorf("expected issue-2 to be blocked by 1, got %d", blockedBy["issue-2"])
		}
	})

	t.Run("multiple dependencies", func(t *testing.T) {
		subgraph := &TemplateSubgraph{
			Root: &types.Issue{ID: "root-1"},
			Dependencies: []*types.Dependency{
				{IssueID: "issue-2", DependsOnID: "issue-1", Type: types.DepBlocks},
				{IssueID: "issue-3", DependsOnID: "issue-1", Type: types.DepBlocks},
				{IssueID: "issue-3", DependsOnID: "issue-2", Type: types.DepBlocks},
			},
		}
		blocks, blockedBy := computeDependencyCounts(subgraph)
		// issue-1 blocks 2 issues (issue-2 and issue-3)
		if blocks["issue-1"] != 2 {
			t.Errorf("expected issue-1 to block 2, got %d", blocks["issue-1"])
		}
		// issue-2 blocks 1 issue (issue-3)
		if blocks["issue-2"] != 1 {
			t.Errorf("expected issue-2 to block 1, got %d", blocks["issue-2"])
		}
		// issue-3 is blocked by 2 issues
		if blockedBy["issue-3"] != 2 {
			t.Errorf("expected issue-3 to be blocked by 2, got %d", blockedBy["issue-3"])
		}
	})

	t.Run("ignores non-blocks dependencies", func(t *testing.T) {
		subgraph := &TemplateSubgraph{
			Root: &types.Issue{ID: "root-1"},
			Dependencies: []*types.Dependency{
				{IssueID: "issue-2", DependsOnID: "issue-1", Type: types.DepParentChild},
				{IssueID: "issue-3", DependsOnID: "issue-1", Type: types.DepRelatesTo},
			},
		}
		blocks, blockedBy := computeDependencyCounts(subgraph)
		if len(blocks) != 0 {
			t.Errorf("expected empty blocks map for non-blocks deps, got %d entries", len(blocks))
		}
		if len(blockedBy) != 0 {
			t.Errorf("expected empty blockedBy map for non-blocks deps, got %d entries", len(blockedBy))
		}
	})
}

func TestRenderNodeBoxWithDeps(t *testing.T) {
	tests := []struct {
		name           string
		node           *GraphNode
		width          int
		blocksCount    int
		blockedByCount int
		wantContains   []string
	}{
		{
			name: "no dependencies",
			node: &GraphNode{
				Issue: &types.Issue{ID: "test-1", Title: "Test", Status: types.StatusOpen},
			},
			width:          20,
			blocksCount:    0,
			blockedByCount: 0,
			wantContains:   []string{"test-1", "Test"},
		},
		{
			name: "with blocks count",
			node: &GraphNode{
				Issue: &types.Issue{ID: "test-2", Title: "Blocker", Status: types.StatusOpen},
			},
			width:          20,
			blocksCount:    3,
			blockedByCount: 0,
			wantContains:   []string{"test-2", "Blocker", "blocks:3"},
		},
		{
			name: "with needs count",
			node: &GraphNode{
				Issue: &types.Issue{ID: "test-3", Title: "Blocked", Status: types.StatusBlocked},
			},
			width:          20,
			blocksCount:    0,
			blockedByCount: 2,
			wantContains:   []string{"test-3", "Blocked", "needs:2"},
		},
		{
			name: "with both counts",
			node: &GraphNode{
				Issue: &types.Issue{ID: "test-4", Title: "Middle", Status: types.StatusInProgress},
			},
			width:          25,
			blocksCount:    1,
			blockedByCount: 2,
			wantContains:   []string{"test-4", "Middle", "blocks:1", "needs:2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderNodeBoxWithDeps(tt.node, tt.width, tt.blocksCount, tt.blockedByCount)
			for _, want := range tt.wantContains {
				if !stringContains(got, want) {
					t.Errorf("renderNodeBoxWithDeps() missing %q in output:\n%s", want, got)
				}
			}
		})
	}
}

func TestComputeLayout(t *testing.T) {
	t.Run("empty subgraph", func(t *testing.T) {
		subgraph := &TemplateSubgraph{
			Root:         &types.Issue{ID: "root-1", Title: "Root"},
			Issues:       []*types.Issue{},
			Dependencies: []*types.Dependency{},
			IssueMap:     make(map[string]*types.Issue),
		}
		layout := computeLayout(subgraph)
		if layout == nil {
			t.Fatal("computeLayout returned nil")
		}
		if layout.RootID != "root-1" {
			t.Errorf("RootID = %q, want %q", layout.RootID, "root-1")
		}
	})

	t.Run("single issue", func(t *testing.T) {
		issue := &types.Issue{ID: "test-1", Title: "Test Issue"}
		subgraph := &TemplateSubgraph{
			Root:         issue,
			Issues:       []*types.Issue{issue},
			Dependencies: []*types.Dependency{},
			IssueMap:     map[string]*types.Issue{"test-1": issue},
		}
		layout := computeLayout(subgraph)
		if len(layout.Nodes) != 1 {
			t.Errorf("len(Nodes) = %d, want 1", len(layout.Nodes))
		}
		if layout.Nodes["test-1"].Layer != 0 {
			t.Errorf("Node layer = %d, want 0", layout.Nodes["test-1"].Layer)
		}
	})

	t.Run("with dependencies", func(t *testing.T) {
		issue1 := &types.Issue{ID: "test-1", Title: "First"}
		issue2 := &types.Issue{ID: "test-2", Title: "Second"}
		dep := &types.Dependency{
			IssueID:     "test-2",
			DependsOnID: "test-1",
			Type:        types.DepBlocks,
		}
		subgraph := &TemplateSubgraph{
			Root:         issue1,
			Issues:       []*types.Issue{issue1, issue2},
			Dependencies: []*types.Dependency{dep},
			IssueMap:     map[string]*types.Issue{"test-1": issue1, "test-2": issue2},
		}
		layout := computeLayout(subgraph)
		if len(layout.Nodes) != 2 {
			t.Errorf("len(Nodes) = %d, want 2", len(layout.Nodes))
		}
		// test-1 has no dependencies, should be layer 0
		if layout.Nodes["test-1"].Layer != 0 {
			t.Errorf("test-1 layer = %d, want 0", layout.Nodes["test-1"].Layer)
		}
		// test-2 depends on test-1, should be layer 1
		if layout.Nodes["test-2"].Layer != 1 {
			t.Errorf("test-2 layer = %d, want 1", layout.Nodes["test-2"].Layer)
		}
	})

	t.Run("children lifted to parent layer (GH#1748)", func(t *testing.T) {
		// Scenario: epic is blocked by a blocker (layer 1),
		// children have no blocking deps but should be in the same layer as parent.
		blocker := &types.Issue{ID: "blocker-1", Title: "Blocker issue"}
		epic := &types.Issue{ID: "epic-1", Title: "My Epic", IssueType: "epic"}
		child1 := &types.Issue{ID: "child-1", Title: "Task A"}
		child2 := &types.Issue{ID: "child-2", Title: "Task B"}
		subgraph := &TemplateSubgraph{
			Root:   blocker,
			Issues: []*types.Issue{blocker, epic, child1, child2},
			Dependencies: []*types.Dependency{
				{IssueID: "epic-1", DependsOnID: "blocker-1", Type: types.DepBlocks},
				{IssueID: "child-1", DependsOnID: "epic-1", Type: types.DepParentChild},
				{IssueID: "child-2", DependsOnID: "epic-1", Type: types.DepParentChild},
			},
			IssueMap: map[string]*types.Issue{
				"blocker-1": blocker, "epic-1": epic,
				"child-1": child1, "child-2": child2,
			},
		}
		layout := computeLayout(subgraph)
		// blocker has no deps → layer 0
		if layout.Nodes["blocker-1"].Layer != 0 {
			t.Errorf("blocker layer = %d, want 0", layout.Nodes["blocker-1"].Layer)
		}
		// epic is blocked → layer 1
		if layout.Nodes["epic-1"].Layer != 1 {
			t.Errorf("epic layer = %d, want 1", layout.Nodes["epic-1"].Layer)
		}
		// children should be lifted to parent's layer (1), not layer 0
		if layout.Nodes["child-1"].Layer != 1 {
			t.Errorf("child-1 layer = %d, want 1 (should match parent)", layout.Nodes["child-1"].Layer)
		}
		if layout.Nodes["child-2"].Layer != 1 {
			t.Errorf("child-2 layer = %d, want 1 (should match parent)", layout.Nodes["child-2"].Layer)
		}
	})

	t.Run("child with own blocker stays in higher layer", func(t *testing.T) {
		// If a child has its own blocking dep putting it higher than the parent,
		// keep the child's higher layer.
		blocker := &types.Issue{ID: "blocker-1", Title: "Blocker"}
		epic := &types.Issue{ID: "epic-1", Title: "Epic", IssueType: "epic"}
		child := &types.Issue{ID: "child-1", Title: "Child"}
		subgraph := &TemplateSubgraph{
			Root:   blocker,
			Issues: []*types.Issue{blocker, epic, child},
			Dependencies: []*types.Dependency{
				{IssueID: "epic-1", DependsOnID: "blocker-1", Type: types.DepBlocks},
				{IssueID: "child-1", DependsOnID: "epic-1", Type: types.DepParentChild},
				{IssueID: "child-1", DependsOnID: "blocker-1", Type: types.DepBlocks},
			},
			IssueMap: map[string]*types.Issue{
				"blocker-1": blocker, "epic-1": epic, "child-1": child,
			},
		}
		layout := computeLayout(subgraph)
		// child blocked by blocker → layer 1 (same as epic, which is also correct)
		if layout.Nodes["child-1"].Layer < layout.Nodes["epic-1"].Layer {
			t.Errorf("child layer = %d, should be >= parent layer %d",
				layout.Nodes["child-1"].Layer, layout.Nodes["epic-1"].Layer)
		}
	})

	t.Run("nil root does not panic", func(t *testing.T) {
		subgraph := &TemplateSubgraph{
			Root:         nil,
			Issues:       []*types.Issue{{ID: "orphan-1", Title: "Orphan"}},
			Dependencies: []*types.Dependency{},
			IssueMap:     map[string]*types.Issue{"orphan-1": {ID: "orphan-1", Title: "Orphan"}},
		}
		layout := computeLayout(subgraph)
		if layout == nil {
			t.Fatal("computeLayout returned nil")
		}
		if layout.RootID != "" {
			t.Errorf("RootID = %q, want empty string for nil root", layout.RootID)
		}
		if len(layout.Nodes) != 1 {
			t.Errorf("len(Nodes) = %d, want 1", len(layout.Nodes))
		}
	})
}

func TestFilterSubgraphOpen(t *testing.T) {
	t.Run("nil subgraph", func(t *testing.T) {
		if filterSubgraphOpen(nil) != nil {
			t.Error("expected nil for nil input")
		}
	})

	t.Run("all closed returns nil", func(t *testing.T) {
		sg := &TemplateSubgraph{
			Root:   &types.Issue{ID: "a", Status: types.StatusClosed},
			Issues: []*types.Issue{{ID: "a", Status: types.StatusClosed}},
			IssueMap: map[string]*types.Issue{
				"a": {ID: "a", Status: types.StatusClosed},
			},
		}
		if filterSubgraphOpen(sg) != nil {
			t.Error("expected nil when all issues are closed")
		}
	})

	t.Run("removes closed issues", func(t *testing.T) {
		open := &types.Issue{ID: "open-1", Title: "Open", Status: types.StatusOpen, Priority: 1}
		closed := &types.Issue{ID: "closed-1", Title: "Closed", Status: types.StatusClosed, Priority: 0}
		sg := &TemplateSubgraph{
			Root:   open,
			Issues: []*types.Issue{open, closed},
			IssueMap: map[string]*types.Issue{
				"open-1": open, "closed-1": closed,
			},
		}
		f := filterSubgraphOpen(sg)
		if f == nil {
			t.Fatal("expected non-nil filtered subgraph")
		}
		if len(f.Issues) != 1 {
			t.Errorf("len(Issues) = %d, want 1", len(f.Issues))
		}
		if f.Issues[0].ID != "open-1" {
			t.Errorf("remaining issue = %q, want open-1", f.Issues[0].ID)
		}
	})

	t.Run("removes deferred issues", func(t *testing.T) {
		open := &types.Issue{ID: "open-1", Title: "Open", Status: types.StatusOpen, Priority: 1}
		deferred := &types.Issue{ID: "def-1", Title: "Deferred", Status: types.StatusDeferred, Priority: 2}
		sg := &TemplateSubgraph{
			Root:   open,
			Issues: []*types.Issue{open, deferred},
			IssueMap: map[string]*types.Issue{
				"open-1": open, "def-1": deferred,
			},
		}
		f := filterSubgraphOpen(sg)
		if len(f.Issues) != 1 || f.Issues[0].ID != "open-1" {
			t.Errorf("expected only open-1, got %d issues", len(f.Issues))
		}
	})

	t.Run("transitive closure through closed node", func(t *testing.T) {
		// A(open) blocks B(closed) blocks C(open) → synthetic A blocks C
		a := &types.Issue{ID: "a", Title: "A", Status: types.StatusOpen, Priority: 1}
		b := &types.Issue{ID: "b", Title: "B", Status: types.StatusClosed, Priority: 2}
		c := &types.Issue{ID: "c", Title: "C", Status: types.StatusOpen, Priority: 2}
		sg := &TemplateSubgraph{
			Root:   a,
			Issues: []*types.Issue{a, b, c},
			Dependencies: []*types.Dependency{
				{IssueID: "b", DependsOnID: "a", Type: types.DepBlocks},
				{IssueID: "c", DependsOnID: "b", Type: types.DepBlocks},
			},
			IssueMap: map[string]*types.Issue{"a": a, "b": b, "c": c},
		}
		f := filterSubgraphOpen(sg)
		if f == nil {
			t.Fatal("expected non-nil")
		}
		if len(f.Issues) != 2 {
			t.Fatalf("expected 2 issues, got %d", len(f.Issues))
		}

		// Should have a synthetic blocks edge from a→c
		foundEdge := false
		for _, dep := range f.Dependencies {
			if dep.Type == types.DepBlocks && dep.DependsOnID == "a" && dep.IssueID == "c" {
				foundEdge = true
			}
		}
		if !foundEdge {
			t.Error("expected transitive blocking edge a→c, not found")
		}
	})

	t.Run("preserves direct edges between open nodes", func(t *testing.T) {
		a := &types.Issue{ID: "a", Title: "A", Status: types.StatusOpen, Priority: 1}
		b := &types.Issue{ID: "b", Title: "B", Status: types.StatusInProgress, Priority: 2}
		sg := &TemplateSubgraph{
			Root:   a,
			Issues: []*types.Issue{a, b},
			Dependencies: []*types.Dependency{
				{IssueID: "b", DependsOnID: "a", Type: types.DepBlocks},
			},
			IssueMap: map[string]*types.Issue{"a": a, "b": b},
		}
		f := filterSubgraphOpen(sg)
		if len(f.Dependencies) != 1 {
			t.Errorf("expected 1 dep, got %d", len(f.Dependencies))
		}
	})

	t.Run("root fallback when root is closed", func(t *testing.T) {
		root := &types.Issue{ID: "root", Title: "Root", Status: types.StatusClosed, Priority: 0}
		hi := &types.Issue{ID: "hi", Title: "High", Status: types.StatusOpen, Priority: 1}
		lo := &types.Issue{ID: "lo", Title: "Low", Status: types.StatusOpen, Priority: 3}
		sg := &TemplateSubgraph{
			Root:   root,
			Issues: []*types.Issue{root, hi, lo},
			IssueMap: map[string]*types.Issue{
				"root": root, "hi": hi, "lo": lo,
			},
		}
		f := filterSubgraphOpen(sg)
		if f.Root == nil || f.Root.ID != "hi" {
			rootID := "<nil>"
			if f.Root != nil {
				rootID = f.Root.ID
			}
			t.Errorf("expected root=hi (highest priority), got %s", rootID)
		}
	})

	t.Run("no duplicate edges", func(t *testing.T) {
		// Direct edge a→c exists AND transitive path a→(closed b)→c exists.
		// Should produce exactly one blocking edge a→c, not two.
		a := &types.Issue{ID: "a", Title: "A", Status: types.StatusOpen, Priority: 1}
		b := &types.Issue{ID: "b", Title: "B", Status: types.StatusClosed, Priority: 2}
		c := &types.Issue{ID: "c", Title: "C", Status: types.StatusOpen, Priority: 2}
		sg := &TemplateSubgraph{
			Root:   a,
			Issues: []*types.Issue{a, b, c},
			Dependencies: []*types.Dependency{
				{IssueID: "b", DependsOnID: "a", Type: types.DepBlocks},
				{IssueID: "c", DependsOnID: "b", Type: types.DepBlocks},
				{IssueID: "c", DependsOnID: "a", Type: types.DepBlocks},
			},
			IssueMap: map[string]*types.Issue{"a": a, "b": b, "c": c},
		}
		f := filterSubgraphOpen(sg)
		blockCount := 0
		for _, dep := range f.Dependencies {
			if dep.Type == types.DepBlocks && dep.DependsOnID == "a" && dep.IssueID == "c" {
				blockCount++
			}
		}
		if blockCount != 1 {
			t.Errorf("expected exactly 1 blocking edge a→c, got %d", blockCount)
		}
	})

	t.Run("preserves non-blocks deps between open nodes", func(t *testing.T) {
		a := &types.Issue{ID: "a", Title: "A", Status: types.StatusOpen, Priority: 1}
		b := &types.Issue{ID: "b", Title: "B", Status: types.StatusOpen, Priority: 2}
		sg := &TemplateSubgraph{
			Root:   a,
			Issues: []*types.Issue{a, b},
			Dependencies: []*types.Dependency{
				{IssueID: "b", DependsOnID: "a", Type: types.DepParentChild},
			},
			IssueMap: map[string]*types.Issue{"a": a, "b": b},
		}
		f := filterSubgraphOpen(sg)
		if len(f.Dependencies) != 1 || f.Dependencies[0].Type != types.DepParentChild {
			t.Error("expected parent-child dep to be preserved")
		}
	})
}
