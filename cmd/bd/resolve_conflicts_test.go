package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/merge"
	"github.com/steveyegge/beads/internal/types"
)

// Helper to parse time or panic
func mustParseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic("invalid time: " + s)
	}
	return t
}

// testMergeIssue creates a merge.Issue from a JSON string
func testMergeIssue(jsonStr string) merge.Issue {
	var issue merge.Issue
	if err := json.Unmarshal([]byte(jsonStr), &issue); err != nil {
		panic("invalid JSON in test: " + err.Error())
	}
	issue.RawLine = jsonStr
	return issue
}

func TestDefaultResolveConflictsFilePath(t *testing.T) {
	tests := []struct {
		name string
		root string
	}{
		{name: "relative root", root: "."},
		{name: "absolute root", root: "/tmp/workspace"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := defaultResolveConflictsFilePath(tt.root)
			want := filepath.Join(tt.root, ".beads", "issues.jsonl")
			if got != want {
				t.Fatalf("defaultResolveConflictsFilePath(%q) = %q, want %q", tt.root, got, want)
			}
		})
	}
}

func TestParseConflicts(t *testing.T) {
	tests := []struct {
		name           string
		content        string
		wantConflicts  int
		wantCleanLines int
		wantErr        bool
	}{
		{
			name: "no conflicts",
			content: `{"id":"bd-1","title":"Issue 1"}
{"id":"bd-2","title":"Issue 2"}`,
			wantConflicts:  0,
			wantCleanLines: 2,
			wantErr:        false,
		},
		{
			name: "single conflict",
			content: `{"id":"bd-1","title":"Issue 1"}
<<<<<<< HEAD
{"id":"bd-2","title":"Issue 2 local"}
=======
{"id":"bd-2","title":"Issue 2 remote"}
>>>>>>> branch
{"id":"bd-3","title":"Issue 3"}`,
			wantConflicts:  1,
			wantCleanLines: 2,
			wantErr:        false,
		},
		{
			name: "multiple conflicts",
			content: `<<<<<<< HEAD
{"id":"bd-1","title":"Local 1"}
=======
{"id":"bd-1","title":"Remote 1"}
>>>>>>> branch
{"id":"bd-2","title":"Clean line"}
<<<<<<< HEAD
{"id":"bd-3","title":"Local 3"}
=======
{"id":"bd-3","title":"Remote 3"}
>>>>>>> other-branch`,
			wantConflicts:  2,
			wantCleanLines: 1,
			wantErr:        false,
		},
		{
			name: "unclosed conflict",
			content: `<<<<<<< HEAD
{"id":"bd-1","title":"Local"}
=======
{"id":"bd-1","title":"Remote"}`,
			wantConflicts:  0,
			wantCleanLines: 0,
			wantErr:        true,
		},
		{
			name: "nested conflict error",
			content: `<<<<<<< HEAD
<<<<<<< HEAD
{"id":"bd-1","title":"Nested"}
=======
{"id":"bd-1","title":"Remote"}
>>>>>>> branch`,
			wantConflicts:  0,
			wantCleanLines: 0,
			wantErr:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conflicts, cleanLines, err := parseConflicts(tt.content)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if len(conflicts) != tt.wantConflicts {
				t.Errorf("Got %d conflicts, want %d", len(conflicts), tt.wantConflicts)
			}

			if len(cleanLines) != tt.wantCleanLines {
				t.Errorf("Got %d clean lines, want %d", len(cleanLines), tt.wantCleanLines)
			}
		})
	}
}

func TestParseConflictsLabels(t *testing.T) {
	content := `<<<<<<< HEAD
{"id":"bd-1","title":"Local"}
=======
{"id":"bd-1","title":"Remote"}
>>>>>>> feature-branch`

	conflicts, _, err := parseConflicts(content)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(conflicts) != 1 {
		t.Fatalf("Expected 1 conflict, got %d", len(conflicts))
	}

	if conflicts[0].LeftLabel != "HEAD" {
		t.Errorf("Expected left label 'HEAD', got %q", conflicts[0].LeftLabel)
	}

	if conflicts[0].RightLabel != "feature-branch" {
		t.Errorf("Expected right label 'feature-branch', got %q", conflicts[0].RightLabel)
	}

	if len(conflicts[0].LeftSide) != 1 {
		t.Errorf("Expected 1 left line, got %d", len(conflicts[0].LeftSide))
	}

	if len(conflicts[0].RightSide) != 1 {
		t.Errorf("Expected 1 right line, got %d", len(conflicts[0].RightSide))
	}
}

func TestResolveConflict(t *testing.T) {
	tests := []struct {
		name            string
		conflict        conflictRegion
		wantIssueID     string
		wantResContains string
	}{
		{
			name: "merge same issue different titles",
			conflict: conflictRegion{
				StartLine:  1,
				EndLine:    5,
				LeftSide:   []string{`{"id":"bd-1","title":"Local Title","updated_at":"2024-01-02T00:00:00Z"}`},
				RightSide:  []string{`{"id":"bd-1","title":"Remote Title","updated_at":"2024-01-01T00:00:00Z"}`},
				LeftLabel:  "HEAD",
				RightLabel: "branch",
			},
			wantIssueID:     "bd-1",
			wantResContains: "merged",
		},
		{
			name: "left only valid JSON",
			conflict: conflictRegion{
				StartLine:  1,
				EndLine:    5,
				LeftSide:   []string{`{"id":"bd-1","title":"Valid"}`},
				RightSide:  []string{`not valid json`},
				LeftLabel:  "HEAD",
				RightLabel: "branch",
			},
			wantIssueID:     "bd-1",
			wantResContains: "left_only_valid",
		},
		{
			name: "right only valid JSON",
			conflict: conflictRegion{
				StartLine:  1,
				EndLine:    5,
				LeftSide:   []string{`invalid json here`},
				RightSide:  []string{`{"id":"bd-2","title":"Valid"}`},
				LeftLabel:  "HEAD",
				RightLabel: "branch",
			},
			wantIssueID:     "bd-2",
			wantResContains: "right_only_valid",
		},
		{
			name: "both unparseable",
			conflict: conflictRegion{
				StartLine:  1,
				EndLine:    5,
				LeftSide:   []string{`not json`},
				RightSide:  []string{`also not json`},
				LeftLabel:  "HEAD",
				RightLabel: "branch",
			},
			wantIssueID:     "",
			wantResContains: "kept_both_unparseable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, info := resolveConflict(tt.conflict, 1)

			if tt.wantIssueID != "" && info.IssueID != tt.wantIssueID {
				t.Errorf("Got issue ID %q, want %q", info.IssueID, tt.wantIssueID)
			}

			if !strings.Contains(info.Resolution, tt.wantResContains) {
				t.Errorf("Resolution %q doesn't contain %q", info.Resolution, tt.wantResContains)
			}
		})
	}
}

func TestMergeIssueConflict(t *testing.T) {
	t.Run("title picks later updated_at", func(t *testing.T) {
		left := testMergeIssue(`{"id":"bd-1","title":"Old Title","updated_at":"2024-01-01T00:00:00Z"}`)
		right := testMergeIssue(`{"id":"bd-1","title":"New Title","updated_at":"2024-01-02T00:00:00Z"}`)

		result := mergeIssueConflict(left, right)

		if result.Title != "New Title" {
			t.Errorf("Expected title 'New Title', got %q", result.Title)
		}
	})

	t.Run("closed status wins", func(t *testing.T) {
		left := testMergeIssue(`{"id":"bd-1","status":"open"}`)
		right := testMergeIssue(`{"id":"bd-1","status":"closed"}`)

		result := mergeIssueConflict(left, right)

		if result.Status != types.StatusClosed {
			t.Errorf("Expected status 'closed', got %q", result.Status)
		}
	})

	t.Run("higher priority wins", func(t *testing.T) {
		left := testMergeIssue(`{"id":"bd-1","priority":2}`)
		right := testMergeIssue(`{"id":"bd-1","priority":1}`)

		result := mergeIssueConflict(left, right)

		if result.Priority != 1 {
			t.Errorf("Expected priority 1, got %d", result.Priority)
		}
	})

	t.Run("notes concatenate when different", func(t *testing.T) {
		left := testMergeIssue(`{"id":"bd-1","notes":"Note A"}`)
		right := testMergeIssue(`{"id":"bd-1","notes":"Note B"}`)

		result := mergeIssueConflict(left, right)

		if !strings.Contains(result.Notes, "Note A") || !strings.Contains(result.Notes, "Note B") {
			t.Errorf("Expected concatenated notes, got %q", result.Notes)
		}
	})

	t.Run("dependencies union", func(t *testing.T) {
		left := merge.Issue{
			Issue: types.Issue{
				ID: "bd-1",
				Dependencies: []*types.Dependency{
					{IssueID: "bd-1", DependsOnID: "bd-2", Type: types.DepBlocks},
				},
			},
		}
		right := merge.Issue{
			Issue: types.Issue{
				ID: "bd-1",
				Dependencies: []*types.Dependency{
					{IssueID: "bd-1", DependsOnID: "bd-3", Type: types.DepBlocks},
				},
			},
		}

		result := mergeIssueConflict(left, right)

		if len(result.Dependencies) != 2 {
			t.Errorf("Expected 2 dependencies, got %d", len(result.Dependencies))
		}
	})
}

func TestTimeHelpers(t *testing.T) {
	t.Run("isTimeAfter", func(t *testing.T) {
		tests := []struct {
			t1, t2 string
			want   bool
		}{
			{"2024-01-02T00:00:00Z", "2024-01-01T00:00:00Z", true},
			{"2024-01-01T00:00:00Z", "2024-01-02T00:00:00Z", false},
			{"2024-01-01T00:00:00Z", "2024-01-01T00:00:00Z", true}, // left wins on tie
		}

		for _, tt := range tests {
			got := isTimeAfter(mustParseTime(tt.t1), mustParseTime(tt.t2))
			if got != tt.want {
				t.Errorf("isTimeAfter(%q, %q) = %v, want %v", tt.t1, tt.t2, got, tt.want)
			}
		}
	})

	t.Run("maxTimeVal", func(t *testing.T) {
		tests := []struct {
			t1, t2, want string
		}{
			{"2024-01-02T00:00:00Z", "2024-01-01T00:00:00Z", "2024-01-02T00:00:00Z"},
			{"2024-01-01T00:00:00Z", "2024-01-02T00:00:00Z", "2024-01-02T00:00:00Z"},
		}

		for _, tt := range tests {
			got := maxTimeVal(mustParseTime(tt.t1), mustParseTime(tt.t2))
			want := mustParseTime(tt.want)
			if !got.Equal(want) {
				t.Errorf("maxTimeVal(%q, %q) = %v, want %v", tt.t1, tt.t2, got, want)
			}
		}
	})

	t.Run("pickByUpdatedAt", func(t *testing.T) {
		tests := []struct {
			left, right, leftTime, rightTime, want string
		}{
			{"A", "B", "2024-01-02T00:00:00Z", "2024-01-01T00:00:00Z", "A"},
			{"A", "B", "2024-01-01T00:00:00Z", "2024-01-02T00:00:00Z", "B"},
			{"Same", "Same", "2024-01-01T00:00:00Z", "2024-01-02T00:00:00Z", "Same"},
		}

		for _, tt := range tests {
			got := pickByUpdatedAt(tt.left, tt.right, mustParseTime(tt.leftTime), mustParseTime(tt.rightTime))
			if got != tt.want {
				t.Errorf("pickByUpdatedAt(%q, %q, ...) = %q, want %q", tt.left, tt.right, got, tt.want)
			}
		}
	})
}

func TestResolveConflictsEndToEnd(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bd-resolve-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a file with conflicts
	conflictFile := filepath.Join(tmpDir, "test.jsonl")
	content := `{"id":"bd-1","title":"Clean issue"}
<<<<<<< HEAD
{"id":"bd-2","title":"Local version","updated_at":"2024-01-02T00:00:00Z"}
=======
{"id":"bd-2","title":"Remote version","updated_at":"2024-01-01T00:00:00Z"}
>>>>>>> remote-branch
{"id":"bd-3","title":"Another clean issue"}`

	if err := os.WriteFile(conflictFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Parse conflicts
	conflicts, cleanLines, err := parseConflicts(content)
	if err != nil {
		t.Fatalf("Failed to parse conflicts: %v", err)
	}

	if len(conflicts) != 1 {
		t.Errorf("Expected 1 conflict, got %d", len(conflicts))
	}

	if len(cleanLines) != 2 {
		t.Errorf("Expected 2 clean lines, got %d", len(cleanLines))
	}

	// Resolve the conflict
	resolved, info := resolveConflict(conflicts[0], 1)
	if info.Resolution != "merged" {
		t.Errorf("Expected resolution 'merged', got %q", info.Resolution)
	}

	if info.IssueID != "bd-2" {
		t.Errorf("Expected issue ID 'bd-2', got %q", info.IssueID)
	}

	// The resolved content should contain the local title (later updated_at)
	if len(resolved) != 1 {
		t.Fatalf("Expected 1 resolved line, got %d", len(resolved))
	}

	if !strings.Contains(resolved[0], "Local version") {
		t.Errorf("Expected resolved content to contain 'Local version', got %q", resolved[0])
	}
}
