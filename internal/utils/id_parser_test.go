package utils

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage/memory"
	"github.com/steveyegge/beads/internal/types"
)

func TestParseIssueID(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		prefix   string
		expected string
	}{
		{
			name:     "already has prefix",
			input:    "bd-a3f8e9",
			prefix:   "bd-",
			expected: "bd-a3f8e9",
		},
		{
			name:     "missing prefix",
			input:    "a3f8e9",
			prefix:   "bd-",
			expected: "bd-a3f8e9",
		},
		{
			name:     "hierarchical with prefix",
			input:    "bd-a3f8e9.1.2",
			prefix:   "bd-",
			expected: "bd-a3f8e9.1.2",
		},
		{
			name:     "hierarchical without prefix",
			input:    "a3f8e9.1.2",
			prefix:   "bd-",
			expected: "bd-a3f8e9.1.2",
		},
		{
			name:     "custom prefix with ID",
			input:    "ticket-123",
			prefix:   "ticket-",
			expected: "ticket-123",
		},
		{
			name:     "custom prefix without ID",
			input:    "123",
			prefix:   "ticket-",
			expected: "ticket-123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseIssueID(tt.input, tt.prefix)
			if result != tt.expected {
				t.Errorf("ParseIssueID(%q, %q) = %q; want %q", tt.input, tt.prefix, result, tt.expected)
			}
		})
	}
}

func TestResolvePartialID(t *testing.T) {
	ctx := context.Background()
	store := memory.New("")
	
	// Create test issues with sequential IDs (current implementation)
	// When hash IDs (bd-165) are implemented, these can be hash-based
	issue1 := &types.Issue{
		ID:        "bd-1",
		Title:     "Test Issue 1",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	issue2 := &types.Issue{
		ID:        "bd-2",
		Title:     "Test Issue 2",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	issue3 := &types.Issue{
		ID:        "bd-10",
		Title:     "Test Issue 3",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	
	if err := store.CreateIssue(ctx, issue1, "test"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateIssue(ctx, issue2, "test"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateIssue(ctx, issue3, "test"); err != nil {
		t.Fatal(err)
	}
	
	// Set config for prefix
	if err := store.SetConfig(ctx, "issue_prefix", "bd-"); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		input       string
		expected    string
		shouldError bool
		errorMsg    string
	}{
		{
			name:     "exact match with prefix",
			input:    "bd-1",
			expected: "bd-1",
		},
		{
			name:     "exact match without prefix",
			input:    "1",
			expected: "bd-1",
		},
		{
			name:     "exact match with prefix (two digits)",
			input:    "bd-10",
			expected: "bd-10",
		},
		{
			name:     "exact match without prefix (two digits)",
			input:    "10",
			expected: "bd-10",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ResolvePartialID(ctx, store, tt.input)
			
			if tt.shouldError {
				if err == nil {
					t.Errorf("ResolvePartialID(%q) expected error containing %q, got nil", tt.input, tt.errorMsg)
				} else if tt.errorMsg != "" && !contains(err.Error(), tt.errorMsg) {
					t.Errorf("ResolvePartialID(%q) error = %q; want error containing %q", tt.input, err.Error(), tt.errorMsg)
				}
			} else {
				if err != nil {
					t.Errorf("ResolvePartialID(%q) unexpected error: %v", tt.input, err)
				}
				if result != tt.expected {
					t.Errorf("ResolvePartialID(%q) = %q; want %q", tt.input, result, tt.expected)
				}
			}
		})
	}
}

func TestResolvePartialIDs(t *testing.T) {
	ctx := context.Background()
	store := memory.New("")
	
	// Create test issues
	issue1 := &types.Issue{
		ID:        "bd-1",
		Title:     "Test Issue 1",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	issue2 := &types.Issue{
		ID:        "bd-2",
		Title:     "Test Issue 2",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	
	if err := store.CreateIssue(ctx, issue1, "test"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateIssue(ctx, issue2, "test"); err != nil {
		t.Fatal(err)
	}
	
	if err := store.SetConfig(ctx, "issue_prefix", "bd-"); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		inputs      []string
		expected    []string
		shouldError bool
	}{
		{
			name:     "resolve multiple IDs without prefix",
			inputs:   []string{"1", "2"},
			expected: []string{"bd-1", "bd-2"},
		},
		{
			name:     "resolve mixed full and partial IDs",
			inputs:   []string{"bd-1", "2"},
			expected: []string{"bd-1", "bd-2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ResolvePartialIDs(ctx, store, tt.inputs)
			
			if tt.shouldError {
				if err == nil {
					t.Errorf("ResolvePartialIDs(%v) expected error, got nil", tt.inputs)
				}
			} else {
				if err != nil {
					t.Errorf("ResolvePartialIDs(%v) unexpected error: %v", tt.inputs, err)
				}
				if len(result) != len(tt.expected) {
					t.Errorf("ResolvePartialIDs(%v) returned %d results; want %d", tt.inputs, len(result), len(tt.expected))
				}
				for i := range result {
					if result[i] != tt.expected[i] {
						t.Errorf("ResolvePartialIDs(%v)[%d] = %q; want %q", tt.inputs, i, result[i], tt.expected[i])
					}
				}
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && 
		(s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || 
		findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
