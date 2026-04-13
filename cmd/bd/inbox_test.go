package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestTopoSortInboxItems(t *testing.T) {
	t.Parallel()

	makeItem := func(id string, blockingDeps ...string) *types.InboxItem {
		item := &types.InboxItem{
			InboxID:         "inbox-" + id,
			SenderProjectID: "proj",
			SenderIssueID:   id,
			Title:           "Issue " + id,
		}
		if len(blockingDeps) > 0 {
			meta, _ := json.Marshal(map[string]interface{}{
				"blocking_deps": blockingDeps,
			})
			item.Metadata = string(meta)
		}
		return item
	}

	ids := func(items []*types.InboxItem) []string {
		out := make([]string, len(items))
		for i, item := range items {
			out[i] = item.SenderIssueID
		}
		return out
	}

	tests := []struct {
		name     string
		items    []*types.InboxItem
		wantFunc func([]string) bool // validate ordering constraints
		desc     string
	}{
		{
			name:  "nil input",
			items: nil,
			wantFunc: func(got []string) bool {
				return len(got) == 0
			},
			desc: "nil returns nil",
		},
		{
			name:  "single item",
			items: []*types.InboxItem{makeItem("A")},
			wantFunc: func(got []string) bool {
				return len(got) == 1 && got[0] == "A"
			},
			desc: "single item passes through",
		},
		{
			name: "no dependencies",
			items: []*types.InboxItem{
				makeItem("A"),
				makeItem("B"),
				makeItem("C"),
			},
			wantFunc: func(got []string) bool {
				return len(got) == 3
			},
			desc: "all items present without deps",
		},
		{
			name: "linear chain A depends on B depends on C",
			items: []*types.InboxItem{
				makeItem("A", "B"),
				makeItem("B", "C"),
				makeItem("C"),
			},
			wantFunc: func(got []string) bool {
				if len(got) != 3 {
					return false
				}
				posOf := map[string]int{}
				for i, id := range got {
					posOf[id] = i
				}
				// C before B, B before A
				return posOf["C"] < posOf["B"] && posOf["B"] < posOf["A"]
			},
			desc: "C comes before B, B before A",
		},
		{
			name: "diamond dependency",
			items: []*types.InboxItem{
				makeItem("A", "B", "C"),
				makeItem("B", "D"),
				makeItem("C", "D"),
				makeItem("D"),
			},
			wantFunc: func(got []string) bool {
				if len(got) != 4 {
					return false
				}
				posOf := map[string]int{}
				for i, id := range got {
					posOf[id] = i
				}
				return posOf["D"] < posOf["B"] &&
					posOf["D"] < posOf["C"] &&
					posOf["B"] < posOf["A"] &&
					posOf["C"] < posOf["A"]
			},
			desc: "D first, A last, B and C in middle",
		},
		{
			name: "circular deps handled gracefully",
			items: []*types.InboxItem{
				makeItem("A", "B"),
				makeItem("B", "A"),
			},
			wantFunc: func(got []string) bool {
				// Both items should be present (appended for circular)
				return len(got) == 2
			},
			desc: "circular deps: all items present",
		},
		{
			name: "external deps ignored",
			items: []*types.InboxItem{
				makeItem("A", "Z"), // Z is not in the batch
				makeItem("B"),
			},
			wantFunc: func(got []string) bool {
				return len(got) == 2
			},
			desc: "deps outside batch are ignored",
		},
		{
			name: "empty metadata treated as no deps",
			items: []*types.InboxItem{
				{InboxID: "i1", SenderIssueID: "A", Metadata: "{}"},
				{InboxID: "i2", SenderIssueID: "B", Metadata: ""},
			},
			wantFunc: func(got []string) bool {
				return len(got) == 2
			},
			desc: "empty/minimal metadata = no deps",
		},
		{
			name: "invalid JSON metadata ignored",
			items: []*types.InboxItem{
				{InboxID: "i1", SenderIssueID: "A", Metadata: "not json"},
				{InboxID: "i2", SenderIssueID: "B"},
			},
			wantFunc: func(got []string) bool {
				return len(got) == 2
			},
			desc: "malformed metadata treated as no deps",
		},
		{
			name: "cross-project ID collision",
			items: []*types.InboxItem{
				// Two different projects both have an issue called "bd-1".
				// Composite keys (project/id) prevent collisions.
				{InboxID: "i1", SenderProjectID: "proj-a", SenderIssueID: "bd-1", Title: "A's issue"},
				{InboxID: "i2", SenderProjectID: "proj-b", SenderIssueID: "bd-1", Title: "B's issue"},
			},
			wantFunc: func(got []string) bool {
				return len(got) == 2
			},
			desc: "colliding SenderIssueIDs from different projects: both preserved",
		},
		{
			name: "cross-project deps only resolve within same project",
			items: []*types.InboxItem{
				// proj-a/child depends on proj-a/parent (same project → resolved)
				// proj-b has its own "parent" which is unrelated
				func() *types.InboxItem {
					meta, _ := json.Marshal(map[string]interface{}{"blocking_deps": []string{"parent"}})
					return &types.InboxItem{
						InboxID: "i1", SenderProjectID: "proj-a", SenderIssueID: "child",
						Title: "A child", Metadata: string(meta),
					}
				}(),
				{InboxID: "i2", SenderProjectID: "proj-a", SenderIssueID: "parent", Title: "A parent"},
				{InboxID: "i3", SenderProjectID: "proj-b", SenderIssueID: "parent", Title: "B unrelated"},
			},
			wantFunc: func(got []string) bool {
				// All 3 items present
				return len(got) == 3
			},
			desc: "3 items from mixed projects all present",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := topoSortInboxItems(tt.items)
			gotIDs := ids(got)
			if !tt.wantFunc(gotIDs) {
				t.Errorf("%s: got order %v", tt.desc, gotIDs)
			}
		})
	}

	// Dedicated test: cross-project dep ordering uses item references
	t.Run("cross-project dep ordering is project-scoped", func(t *testing.T) {
		meta, _ := json.Marshal(map[string]interface{}{"blocking_deps": []string{"parent"}})
		child := &types.InboxItem{
			InboxID: "i1", SenderProjectID: "proj-a", SenderIssueID: "child",
			Title: "A child", Metadata: string(meta),
		}
		parent := &types.InboxItem{
			InboxID: "i2", SenderProjectID: "proj-a", SenderIssueID: "parent",
			Title: "A parent",
		}
		unrelated := &types.InboxItem{
			InboxID: "i3", SenderProjectID: "proj-b", SenderIssueID: "parent",
			Title: "B unrelated",
		}

		got := topoSortInboxItems([]*types.InboxItem{child, parent, unrelated})
		if len(got) != 3 {
			t.Fatalf("got %d items, want 3", len(got))
		}

		// Find positions by InboxID (unique across projects)
		posOf := map[string]int{}
		for i, item := range got {
			posOf[item.InboxID] = i
		}
		if posOf["i2"] > posOf["i1"] {
			t.Errorf("proj-a/parent (i2) should come before proj-a/child (i1), got positions parent=%d child=%d",
				posOf["i2"], posOf["i1"])
		}
	})
}

func TestInboxItemToIssue(t *testing.T) {
	t.Parallel()

	t.Run("basic field mapping", func(t *testing.T) {
		item := &types.InboxItem{
			InboxID:         "inbox-001",
			SenderProjectID: "upstream-proj",
			SenderIssueID:   "up-42",
			Title:           "Fix auth bug",
			Description:     "Auth module panics on nil",
			Priority:        1,
			IssueType:       "bug",
			Status:          "in_progress",
			SenderRef:       "beads://upstream-proj/up-42",
		}

		issue := inboxItemToIssue(item)

		if issue.Title != "Fix auth bug" {
			t.Errorf("Title = %q, want %q", issue.Title, "Fix auth bug")
		}
		if issue.Description != "Auth module panics on nil" {
			t.Errorf("Description = %q", issue.Description)
		}
		if issue.Priority != 1 {
			t.Errorf("Priority = %d, want 1", issue.Priority)
		}
		if issue.IssueType != types.TypeBug {
			t.Errorf("IssueType = %q, want %q", issue.IssueType, types.TypeBug)
		}
		// Status should always be open for new imports
		if issue.Status != types.StatusOpen {
			t.Errorf("Status = %q, want %q", issue.Status, types.StatusOpen)
		}
		if issue.ExternalRef == nil || *issue.ExternalRef != "beads://upstream-proj/up-42" {
			t.Errorf("ExternalRef = %v, want %q", issue.ExternalRef, "beads://upstream-proj/up-42")
		}
		if issue.SourceRepo != "upstream-proj" {
			t.Errorf("SourceRepo = %q, want %q", issue.SourceRepo, "upstream-proj")
		}
	})

	t.Run("labels parsed from JSON", func(t *testing.T) {
		item := &types.InboxItem{
			InboxID:         "inbox-002",
			SenderProjectID: "proj",
			SenderIssueID:   "p-1",
			Title:           "Test",
			Labels:          `["critical","backend","v2"]`,
		}

		issue := inboxItemToIssue(item)

		if len(issue.Labels) != 3 {
			t.Fatalf("Labels len = %d, want 3", len(issue.Labels))
		}
		if issue.Labels[0] != "critical" || issue.Labels[1] != "backend" || issue.Labels[2] != "v2" {
			t.Errorf("Labels = %v, want [critical backend v2]", issue.Labels)
		}
	})

	t.Run("empty labels", func(t *testing.T) {
		for _, labels := range []string{"", "[]"} {
			item := &types.InboxItem{
				InboxID:         "inbox-003",
				SenderProjectID: "proj",
				SenderIssueID:   "p-2",
				Title:           "Test",
				Labels:          labels,
			}
			issue := inboxItemToIssue(item)
			if len(issue.Labels) != 0 {
				t.Errorf("Labels(%q) len = %d, want 0", labels, len(issue.Labels))
			}
		}
	})

	t.Run("malformed labels JSON", func(t *testing.T) {
		item := &types.InboxItem{
			InboxID:         "inbox-004",
			SenderProjectID: "proj",
			SenderIssueID:   "p-3",
			Title:           "Test",
			Labels:          "not valid json",
		}
		issue := inboxItemToIssue(item)
		if len(issue.Labels) != 0 {
			t.Errorf("Labels len = %d, want 0 for malformed JSON", len(issue.Labels))
		}
	})
}

func TestIssueToInboxItem(t *testing.T) {
	t.Parallel()

	t.Run("basic field mapping", func(t *testing.T) {
		issue := &types.Issue{
			ID:          "bd-42",
			Title:       "Important bug",
			Description: "It's broken",
			Priority:    0,
			IssueType:   types.TypeBug,
			Status:      types.StatusInProgress,
		}

		item := issueToInboxItem(issue, "my-project", nil)

		if item.SenderProjectID != "my-project" {
			t.Errorf("SenderProjectID = %q", item.SenderProjectID)
		}
		if item.SenderIssueID != "bd-42" {
			t.Errorf("SenderIssueID = %q", item.SenderIssueID)
		}
		if item.Title != "Important bug" {
			t.Errorf("Title = %q", item.Title)
		}
		if item.Priority != 0 {
			t.Errorf("Priority = %d", item.Priority)
		}
		if item.IssueType != "bug" {
			t.Errorf("IssueType = %q", item.IssueType)
		}
		if item.Status != "in_progress" {
			t.Errorf("Status = %q", item.Status)
		}
		if item.SenderRef != "beads://my-project/bd-42" {
			t.Errorf("SenderRef = %q", item.SenderRef)
		}
		if item.InboxID == "" {
			t.Error("InboxID should be generated (UUID)")
		}
	})

	t.Run("labels serialized to JSON", func(t *testing.T) {
		issue := &types.Issue{
			ID:     "bd-1",
			Title:  "Test",
			Labels: []string{"frontend", "urgent"},
		}

		item := issueToInboxItem(issue, "proj", nil)

		var labels []string
		if err := json.Unmarshal([]byte(item.Labels), &labels); err != nil {
			t.Fatalf("Labels JSON parse: %v", err)
		}
		if len(labels) != 2 || labels[0] != "frontend" || labels[1] != "urgent" {
			t.Errorf("Labels = %v", labels)
		}
	})

	t.Run("empty labels not serialized", func(t *testing.T) {
		issue := &types.Issue{
			ID:    "bd-2",
			Title: "Test",
		}

		item := issueToInboxItem(issue, "proj", nil)

		if item.Labels != "" {
			t.Errorf("Labels = %q, want empty", item.Labels)
		}
	})

	t.Run("blocking deps encoded in metadata", func(t *testing.T) {
		issue := &types.Issue{
			ID:    "bd-10",
			Title: "Child task",
		}
		depMap := map[string][]string{
			"bd-10": {"bd-5", "bd-7"},
		}

		item := issueToInboxItem(issue, "proj", depMap)

		var meta struct {
			BlockingDeps []string `json:"blocking_deps"`
		}
		if err := json.Unmarshal([]byte(item.Metadata), &meta); err != nil {
			t.Fatalf("Metadata JSON parse: %v", err)
		}
		if len(meta.BlockingDeps) != 2 {
			t.Fatalf("BlockingDeps len = %d, want 2", len(meta.BlockingDeps))
		}
	})

	t.Run("no deps means empty metadata", func(t *testing.T) {
		issue := &types.Issue{
			ID:    "bd-20",
			Title: "Independent task",
		}

		item := issueToInboxItem(issue, "proj", map[string][]string{})

		if item.Metadata != "" {
			t.Errorf("Metadata = %q, want empty", item.Metadata)
		}
	})
}

func TestDeduplicateIssues(t *testing.T) {
	t.Parallel()

	t.Run("empty slice", func(t *testing.T) {
		got := deduplicateIssues(nil)
		if len(got) != 0 {
			t.Errorf("len = %d, want 0", len(got))
		}
	})

	t.Run("no duplicates", func(t *testing.T) {
		issues := []*types.Issue{
			{ID: "a"}, {ID: "b"}, {ID: "c"},
		}
		got := deduplicateIssues(issues)
		if len(got) != 3 {
			t.Errorf("len = %d, want 3", len(got))
		}
	})

	t.Run("duplicates removed preserving order", func(t *testing.T) {
		issues := []*types.Issue{
			{ID: "a"}, {ID: "b"}, {ID: "a"}, {ID: "c"}, {ID: "b"},
		}
		got := deduplicateIssues(issues)
		if len(got) != 3 {
			t.Fatalf("len = %d, want 3", len(got))
		}
		if got[0].ID != "a" || got[1].ID != "b" || got[2].ID != "c" {
			t.Errorf("order = [%s %s %s], want [a b c]", got[0].ID, got[1].ID, got[2].ID)
		}
	})
}

func TestInboxFormatDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		dur  time.Duration
		want string
	}{
		{0, "just now"},
		{30 * time.Second, "just now"},
		{5 * time.Minute, "5m"},
		{90 * time.Minute, "1h"},
		{3 * time.Hour, "3h"},
		{25 * time.Hour, "1d"},
		{72 * time.Hour, "3d"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := inboxFormatDuration(tt.dur)
			if got != tt.want {
				t.Errorf("inboxFormatDuration(%v) = %q, want %q", tt.dur, got, tt.want)
			}
		})
	}
}

func TestJoinParts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		parts []string
		want  string
	}{
		{"empty", nil, ""},
		{"single", []string{"hello"}, "hello"},
		{"multiple", []string{"a", "b", "c"}, "a, b, c"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := joinParts(tt.parts)
			if got != tt.want {
				t.Errorf("joinParts(%v) = %q, want %q", tt.parts, got, tt.want)
			}
		})
	}
}
