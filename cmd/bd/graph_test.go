package main

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

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
			want:   "   ",
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
		name   string
		node   *GraphNode
		width  int
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
}
