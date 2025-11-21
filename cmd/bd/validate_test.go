package main

import (
	"context"
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestParseChecks(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		want      []string
		wantError bool
	}{
		{
			name:  "empty returns all defaults",
			input: "",
			want:  []string{"orphans", "duplicates", "pollution", "conflicts"},
		},
		{
			name:  "single check",
			input: "orphans",
			want:  []string{"orphans"},
		},
		{
			name:  "multiple checks",
			input: "orphans,duplicates",
			want:  []string{"orphans", "duplicates"},
		},
		{
			name:  "synonym dupes->duplicates",
			input: "dupes",
			want:  []string{"duplicates"},
		},
		{
			name:  "synonym git-conflicts->conflicts",
			input: "git-conflicts",
			want:  []string{"conflicts"},
		},
		{
			name:  "mixed with whitespace",
			input: " orphans , duplicates , pollution ",
			want:  []string{"orphans", "duplicates", "pollution"},
		},
		{
			name:  "deduplication",
			input: "orphans,orphans,duplicates",
			want:  []string{"orphans", "duplicates"},
		},
		{
			name:      "invalid check",
			input:     "orphans,invalid,duplicates",
			wantError: true,
		},
		{
			name:  "empty parts ignored",
			input: "orphans,,duplicates",
			want:  []string{"orphans", "duplicates"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseChecks(tt.input)
			if tt.wantError {
				if err == nil {
					t.Errorf("parseChecks(%q) expected error, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseChecks(%q) unexpected error: %v", tt.input, err)
			}
			if len(got) != len(tt.want) {
				t.Errorf("parseChecks(%q) length = %d, want %d", tt.input, len(got), len(tt.want))
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("parseChecks(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestValidationResultsHasFailures(t *testing.T) {
	tests := []struct {
		name   string
		checks map[string]checkResult
		want   bool
	}{
		{
			name: "no failures - all clean",
			checks: map[string]checkResult{
				"orphans": {issueCount: 0, fixedCount: 0},
				"dupes":   {issueCount: 0, fixedCount: 0},
			},
			want: false,
		},
		{
			name: "has error",
			checks: map[string]checkResult{
				"orphans": {err: os.ErrNotExist},
			},
			want: true,
		},
		{
			name: "issues found but not all fixed",
			checks: map[string]checkResult{
				"orphans": {issueCount: 5, fixedCount: 3},
			},
			want: true,
		},
		{
			name: "issues found and all fixed",
			checks: map[string]checkResult{
				"orphans": {issueCount: 5, fixedCount: 5},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &validationResults{checks: tt.checks}
			got := r.hasFailures()
			if got != tt.want {
				t.Errorf("hasFailures() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidationResultsToJSON(t *testing.T) {
	r := &validationResults{
		checks: map[string]checkResult{
			"orphans": {
				issueCount: 3,
				fixedCount: 2,
				suggestions: []string{"Run bd repair"},
			},
			"dupes": {
				issueCount: 0,
				fixedCount: 0,
			},
		},
	}

	output := r.toJSON()

	if output["total_issues"] != 3 {
		t.Errorf("total_issues = %v, want 3", output["total_issues"])
	}
	if output["total_fixed"] != 2 {
		t.Errorf("total_fixed = %v, want 2", output["total_fixed"])
	}
	if output["healthy"] != false {
		t.Errorf("healthy = %v, want false", output["healthy"])
	}

	checks := output["checks"].(map[string]interface{})
	orphans := checks["orphans"].(map[string]interface{})
	if orphans["issue_count"] != 3 {
		t.Errorf("orphans issue_count = %v, want 3", orphans["issue_count"])
	}
	if orphans["fixed_count"] != 2 {
		t.Errorf("orphans fixed_count = %v, want 2", orphans["fixed_count"])
	}
}

func TestValidateOrphanedDeps(t *testing.T) {
	ctx := context.Background()

	allIssues := []*types.Issue{
		{
			ID: "bd-1",
			Dependencies: []*types.Dependency{
				{DependsOnID: "bd-2", Type: types.DepBlocks},
				{DependsOnID: "bd-999", Type: types.DepBlocks}, // orphaned
			},
		},
		{
			ID: "bd-2",
		},
	}

	result := validateOrphanedDeps(ctx, allIssues, false)

	if result.issueCount != 1 {
		t.Errorf("issueCount = %d, want 1", result.issueCount)
	}
	if result.fixedCount != 0 {
		t.Errorf("fixedCount = %d, want 0 (fix=false)", result.fixedCount)
	}
	if len(result.suggestions) == 0 {
		t.Error("expected suggestions")
	}
}

func TestValidateDuplicates(t *testing.T) {
	ctx := context.Background()

	allIssues := []*types.Issue{
		{
			ID:    "bd-1",
			Title: "Same title",
		},
		{
			ID:    "bd-2",
			Title: "Same title",
		},
		{
			ID:    "bd-3",
			Title: "Different",
		},
	}

	result := validateDuplicates(ctx, allIssues, false)

	// Should find 1 duplicate (bd-2 is duplicate of bd-1)
	if result.issueCount != 1 {
		t.Errorf("issueCount = %d, want 1", result.issueCount)
	}
	if len(result.suggestions) == 0 {
		t.Error("expected suggestions")
	}
}

func TestValidatePollution(t *testing.T) {
	ctx := context.Background()

	allIssues := []*types.Issue{
		{
			ID:    "test-1",
			Title: "Test issue",
		},
		{
			ID:    "bd-1",
			Title: "Normal issue",
		},
	}

	result := validatePollution(ctx, allIssues, false)

	// Should detect test-1 as pollution
	if result.issueCount != 1 {
		t.Errorf("issueCount = %d, want 1", result.issueCount)
	}
}

func TestValidateGitConflicts_NoFile(t *testing.T) {
	ctx := context.Background()

	// Create temp dir without JSONL
	tmpDir := t.TempDir()
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Override dbPath to point to temp dir (file won't exist)
	originalDBPath := dbPath
	dbPath = tmpDir + "/nonexistent.db"
	defer func() { dbPath = originalDBPath }()

	result := validateGitConflicts(ctx, false)

	if result.issueCount != 0 {
		t.Errorf("issueCount = %d, want 0 (no file)", result.issueCount)
	}
	if result.err != nil {
		t.Errorf("unexpected error: %v", result.err)
	}
}

func TestValidateGitConflicts_WithMarkers(t *testing.T) {
	ctx := context.Background()

	// Create temp JSONL with conflict markers
	tmpDir := t.TempDir()
	jsonlPath := tmpDir + "/test.jsonl"
	content := `{"id":"bd-1"}
<<<<<<< HEAD
{"id":"bd-2","title":"Version A"}
=======
{"id":"bd-2","title":"Version B"}
>>>>>>> main
{"id":"bd-3"}`

	if err := os.WriteFile(jsonlPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	// Override dbPath to point to temp file
	originalDBPath := dbPath
	dbPath = jsonlPath
	defer func() { dbPath = originalDBPath }()

	result := validateGitConflicts(ctx, false)

	if result.issueCount != 1 {
		t.Errorf("issueCount = %d, want 1 (conflict found)", result.issueCount)
	}
	if len(result.suggestions) == 0 {
		t.Error("expected suggestions for conflict resolution")
	}
}

func TestValidateGitConflicts_Clean(t *testing.T) {
	ctx := context.Background()

	// Create temp JSONL without conflicts
	tmpDir := t.TempDir()
	jsonlPath := tmpDir + "/test.jsonl"
	content := `{"id":"bd-1","title":"Normal"}
{"id":"bd-2","title":"Also normal"}`

	if err := os.WriteFile(jsonlPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	// Override dbPath to point to temp file
	originalDBPath := dbPath
	dbPath = jsonlPath
	defer func() { dbPath = originalDBPath }()

	result := validateGitConflicts(ctx, false)

	if result.issueCount != 0 {
		t.Errorf("issueCount = %d, want 0 (clean file)", result.issueCount)
	}
	if result.err != nil {
		t.Errorf("unexpected error: %v", result.err)
	}
}
