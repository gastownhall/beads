package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseMarkdownFile(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected []*IssueTemplate
		wantErr  bool
	}{
		{
			name: "simple issue",
			content: `## Fix authentication bug

This is a critical bug in the auth system.

### Priority
1

### Type
bug
`,
			expected: []*IssueTemplate{
				{
					Title:       "Fix authentication bug",
					Description: "This is a critical bug in the auth system.",
					Priority:    1,
					IssueType:   "bug",
				},
			},
		},
		{
			name: "multiple issues",
			content: `## First Issue

Description for first issue.

### Priority
0

### Type
feature

## Second Issue

Description for second issue.

### Priority
2

### Type
task
`,
			expected: []*IssueTemplate{
				{
					Title:       "First Issue",
					Description: "Description for first issue.",
					Priority:    0,
					IssueType:   "feature",
				},
				{
					Title:       "Second Issue",
					Description: "Description for second issue.",
					Priority:    2,
					IssueType:   "task",
				},
			},
		},
		{
			name: "issue with all fields",
			content: `## Comprehensive Issue

Initial description text.

### Priority
1

### Type
feature

### Description
Detailed description here.
Multi-line support.

### Design
Design notes go here.

### Acceptance Criteria
- Must do this
- Must do that

### Assignee
alice

### Labels
backend, urgent

### Dependencies
bd-10, bd-20
`,
			expected: []*IssueTemplate{
				{
					Title:              "Comprehensive Issue",
					Description:        "Detailed description here.\nMulti-line support.",
					Design:             "Design notes go here.",
					AcceptanceCriteria: "- Must do this\n- Must do that",
					Priority:           1,
					IssueType:          "feature",
					Assignee:           "alice",
					Labels:             []string{"backend", "urgent"},
					Dependencies:       []string{"bd-10", "bd-20"},
				},
			},
		},
		{
			name: "dependencies with types",
			content: `## Issue with typed dependencies

### Priority
2

### Type
task

### Dependencies
blocks:bd-10, discovered-from:bd-20
`,
			expected: []*IssueTemplate{
				{
					Title:        "Issue with typed dependencies",
					Priority:     2,
					IssueType:    "task",
					Dependencies: []string{"blocks:bd-10", "discovered-from:bd-20"},
				},
			},
		},
		{
			name: "default values",
			content: `## Minimal Issue

Just a title and description.
`,
			expected: []*IssueTemplate{
				{
					Title:       "Minimal Issue",
					Description: "Just a title and description.",
					Priority:    2,      // default
					IssueType:   "task", // default
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp file
			tmpDir := t.TempDir()
			tmpFile := filepath.Join(tmpDir, "test.md")
			if err := os.WriteFile(tmpFile, []byte(tt.content), 0600); err != nil {
				t.Fatalf("Failed to create test file: %v", err)
			}

			// Parse file
			got, err := parseMarkdownFile(tmpFile)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseMarkdownFile() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if len(got) != len(tt.expected) {
				t.Errorf("parseMarkdownFile() got %d issues, want %d", len(got), len(tt.expected))
				return
			}

			// Compare each issue
			for i, gotIssue := range got {
				wantIssue := tt.expected[i]

				if gotIssue.Title != wantIssue.Title {
					t.Errorf("Issue %d: Title = %q, want %q", i, gotIssue.Title, wantIssue.Title)
				}
				if gotIssue.Description != wantIssue.Description {
					t.Errorf("Issue %d: Description = %q, want %q", i, gotIssue.Description, wantIssue.Description)
				}
				if gotIssue.Priority != wantIssue.Priority {
					t.Errorf("Issue %d: Priority = %d, want %d", i, gotIssue.Priority, wantIssue.Priority)
				}
				if gotIssue.IssueType != wantIssue.IssueType {
					t.Errorf("Issue %d: IssueType = %q, want %q", i, gotIssue.IssueType, wantIssue.IssueType)
				}
				if gotIssue.Design != wantIssue.Design {
					t.Errorf("Issue %d: Design = %q, want %q", i, gotIssue.Design, wantIssue.Design)
				}
				if gotIssue.AcceptanceCriteria != wantIssue.AcceptanceCriteria {
					t.Errorf("Issue %d: AcceptanceCriteria = %q, want %q", i, gotIssue.AcceptanceCriteria, wantIssue.AcceptanceCriteria)
				}
				if gotIssue.Assignee != wantIssue.Assignee {
					t.Errorf("Issue %d: Assignee = %q, want %q", i, gotIssue.Assignee, wantIssue.Assignee)
				}

				// Compare slices
				if !stringSlicesEqual(gotIssue.Labels, wantIssue.Labels) {
					t.Errorf("Issue %d: Labels = %v, want %v", i, gotIssue.Labels, wantIssue.Labels)
				}
				if !stringSlicesEqual(gotIssue.Dependencies, wantIssue.Dependencies) {
					t.Errorf("Issue %d: Dependencies = %v, want %v", i, gotIssue.Dependencies, wantIssue.Dependencies)
				}
			}
		})
	}
}

func TestParseMarkdownFile_FileNotFound(t *testing.T) {
	_, err := parseMarkdownFile("/nonexistent/file.md")
	if err == nil {
		t.Error("Expected error for non-existent file, got nil")
	}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
