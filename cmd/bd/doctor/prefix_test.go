package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestPrefixDetection_MultiplePrefixes tests CountJSONLIssues with mixed prefixes
func TestPrefixDetection_MultiplePrefixes(t *testing.T) {
	tests := []struct {
		name             string
		content          string
		expectedCount    int
		expectedPrefixes map[string]int
	}{
		{
			name: "single prefix",
			content: `{"id":"bd-1","title":"Issue 1"}
{"id":"bd-2","title":"Issue 2"}
{"id":"bd-3","title":"Issue 3"}
`,
			expectedCount: 3,
			expectedPrefixes: map[string]int{
				"bd": 3,
			},
		},
		{
			name: "two prefixes evenly distributed",
			content: `{"id":"bd-1","title":"Issue 1"}
{"id":"proj-2","title":"Issue 2"}
{"id":"bd-3","title":"Issue 3"}
{"id":"proj-4","title":"Issue 4"}
`,
			expectedCount: 4,
			expectedPrefixes: map[string]int{
				"bd":   2,
				"proj": 2,
			},
		},
		{
			name: "three prefixes after merge",
			content: `{"id":"bd-1","title":"Issue 1"}
{"id":"proj-2","title":"Issue 2"}
{"id":"beads-3","title":"Issue 3"}
{"id":"bd-4","title":"Issue 4"}
{"id":"proj-5","title":"Issue 5"}
`,
			expectedCount: 5,
			expectedPrefixes: map[string]int{
				"bd":    2,
				"proj":  2,
				"beads": 1,
			},
		},
		{
			name: "multiple prefixes with clear majority",
			content: `{"id":"bd-1","title":"Issue 1"}
{"id":"bd-2","title":"Issue 2"}
{"id":"bd-3","title":"Issue 3"}
{"id":"bd-4","title":"Issue 4"}
{"id":"bd-5","title":"Issue 5"}
{"id":"bd-6","title":"Issue 6"}
{"id":"bd-7","title":"Issue 7"}
{"id":"proj-8","title":"Issue 8"}
{"id":"beads-9","title":"Issue 9"}
`,
			expectedCount: 9,
			expectedPrefixes: map[string]int{
				"bd":    7,
				"proj":  1,
				"beads": 1,
			},
		},
		{
			name: "prefix mismatch scenario after branch merge",
			content: `{"id":"feature-1","title":"Feature branch issue"}
{"id":"feature-2","title":"Feature branch issue"}
{"id":"main-3","title":"Main branch issue"}
{"id":"main-4","title":"Main branch issue"}
{"id":"main-5","title":"Main branch issue"}
`,
			expectedCount: 5,
			expectedPrefixes: map[string]int{
				"feature": 2,
				"main":    3,
			},
		},
		{
			name: "legacy and new prefixes mixed",
			content: `{"id":"beads-1","title":"Old prefix"}
{"id":"beads-2","title":"Old prefix"}
{"id":"bd-3","title":"New prefix"}
{"id":"bd-4","title":"New prefix"}
{"id":"bd-5","title":"New prefix"}
{"id":"bd-6","title":"New prefix"}
`,
			expectedCount: 6,
			expectedPrefixes: map[string]int{
				"beads": 2,
				"bd":    4,
			},
		},
		{
			name: "prefix with multiple dashes",
			content: `{"id":"my-project-123","title":"Issue 1"}
{"id":"my-project-456","title":"Issue 2"}
{"id":"other-proj-789","title":"Issue 3"}
`,
			expectedCount: 3,
			expectedPrefixes: map[string]int{
				"my-project":  2,
				"other-proj": 1,
			},
		},
		{
			name: "issue IDs without dashes",
			content: `{"id":"abc123","title":"No dash ID"}
{"id":"def456","title":"No dash ID"}
{"id":"bd-1","title":"Normal ID"}
`,
			expectedCount: 3,
			expectedPrefixes: map[string]int{
				"abc123": 1,
				"def456": 1,
				"bd":     1,
			},
		},
		{
			name: "empty lines and whitespace",
			content: `{"id":"bd-1","title":"Issue 1"}

{"id":"bd-2","title":"Issue 2"}

{"id":"proj-3","title":"Issue 3"}
`,
			expectedCount: 3,
			expectedPrefixes: map[string]int{
				"bd":   2,
				"proj": 1,
			},
		},
		{
			name: "tombstones mixed with regular issues",
			content: `{"id":"bd-1","title":"Issue 1","status":"open"}
{"id":"bd-2","title":"Issue 2","status":"tombstone"}
{"id":"proj-3","title":"Issue 3","status":"closed"}
{"id":"bd-4","title":"Issue 4","status":"tombstone"}
`,
			expectedCount: 4,
			expectedPrefixes: map[string]int{
				"bd":   3,
				"proj": 1,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
			if err := os.WriteFile(jsonlPath, []byte(tt.content), 0600); err != nil {
				t.Fatalf("failed to create JSONL: %v", err)
			}

			count, prefixes, err := CountJSONLIssues(jsonlPath)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if count != tt.expectedCount {
				t.Errorf("expected count %d, got %d", tt.expectedCount, count)
			}

			if len(prefixes) != len(tt.expectedPrefixes) {
				t.Errorf("expected %d unique prefixes, got %d", len(tt.expectedPrefixes), len(prefixes))
			}

			for expectedPrefix, expectedCount := range tt.expectedPrefixes {
				actualCount, found := prefixes[expectedPrefix]
				if !found {
					t.Errorf("expected prefix %q not found in results", expectedPrefix)
					continue
				}
				if actualCount != expectedCount {
					t.Errorf("prefix %q: expected count %d, got %d", expectedPrefix, expectedCount, actualCount)
				}
			}
		})
	}
}

// TestPrefixDetection_MalformedJSON tests handling of malformed JSON with prefix detection
func TestPrefixDetection_MalformedJSON(t *testing.T) {
	tests := []struct {
		name          string
		content       string
		expectedCount int
		expectError   bool
	}{
		{
			name: "some invalid lines",
			content: `{"id":"bd-1","title":"Valid"}
invalid json line
{"id":"bd-2","title":"Valid"}
not-json
{"id":"proj-3","title":"Valid"}
`,
			expectedCount: 3,
			expectError:   true,
		},
		{
			name: "missing id field",
			content: `{"id":"bd-1","title":"Valid"}
{"title":"No ID field"}
{"id":"bd-2","title":"Valid"}
`,
			expectedCount: 2,
			expectError:   false,
		},
		{
			name: "id field is not string",
			content: `{"id":"bd-1","title":"Valid"}
{"id":123,"title":"Numeric ID"}
{"id":"bd-2","title":"Valid"}
`,
			expectedCount: 2,
			expectError:   false,
		},
		{
			name: "empty id field",
			content: `{"id":"bd-1","title":"Valid"}
{"id":"","title":"Empty ID"}
{"id":"bd-2","title":"Valid"}
`,
			expectedCount: 2,
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
			if err := os.WriteFile(jsonlPath, []byte(tt.content), 0600); err != nil {
				t.Fatalf("failed to create JSONL: %v", err)
			}

			count, _, err := CountJSONLIssues(jsonlPath)

			if tt.expectError && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			if count != tt.expectedCount {
				t.Errorf("expected count %d, got %d", tt.expectedCount, count)
			}
		})
	}
}

// TestPrefixDetection_MostCommonPrefix tests the logic for detecting the most common prefix
func TestPrefixDetection_MostCommonPrefix(t *testing.T) {
	tests := []struct {
		name                 string
		content              string
		expectedMostCommon   string
		expectedMostCommonCount int
	}{
		{
			name: "clear majority",
			content: `{"id":"bd-1"}
{"id":"bd-2"}
{"id":"bd-3"}
{"id":"bd-4"}
{"id":"proj-5"}
`,
			expectedMostCommon:   "bd",
			expectedMostCommonCount: 4,
		},
		{
			name: "tied prefixes - first alphabetically",
			content: `{"id":"alpha-1"}
{"id":"alpha-2"}
{"id":"beta-3"}
{"id":"beta-4"}
`,
			expectedMostCommon:   "alpha",
			expectedMostCommonCount: 2,
		},
		{
			name: "three-way split with clear leader",
			content: `{"id":"primary-1"}
{"id":"primary-2"}
{"id":"primary-3"}
{"id":"secondary-4"}
{"id":"tertiary-5"}
`,
			expectedMostCommon:   "primary",
			expectedMostCommonCount: 3,
		},
		{
			name: "after merge conflict resolution",
			content: `{"id":"main-1"}
{"id":"main-2"}
{"id":"main-3"}
{"id":"main-4"}
{"id":"main-5"}
{"id":"feature-6"}
{"id":"feature-7"}
{"id":"hotfix-8"}
`,
			expectedMostCommon:   "main",
			expectedMostCommonCount: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
			if err := os.WriteFile(jsonlPath, []byte(tt.content), 0600); err != nil {
				t.Fatalf("failed to create JSONL: %v", err)
			}

			_, prefixes, err := CountJSONLIssues(jsonlPath)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			var mostCommonPrefix string
			maxCount := 0
			for prefix, count := range prefixes {
				if count > maxCount || (count == maxCount && prefix < mostCommonPrefix) {
					maxCount = count
					mostCommonPrefix = prefix
				}
			}

			if mostCommonPrefix != tt.expectedMostCommon {
				t.Errorf("expected most common prefix %q, got %q", tt.expectedMostCommon, mostCommonPrefix)
			}
			if maxCount != tt.expectedMostCommonCount {
				t.Errorf("expected most common count %d, got %d", tt.expectedMostCommonCount, maxCount)
			}
		})
	}
}

// TestPrefixMismatchDetection tests detection of prefix mismatches that should trigger warnings
func TestPrefixMismatchDetection(t *testing.T) {
	tests := []struct {
		name            string
		jsonlContent    string
		dbPrefix        string
		shouldWarn      bool
		description     string
	}{
		{
			name: "perfect match",
			jsonlContent: `{"id":"bd-1"}
{"id":"bd-2"}
{"id":"bd-3"}
`,
			dbPrefix:    "bd",
			shouldWarn:  false,
			description: "all issues use database prefix",
		},
		{
			name: "complete mismatch",
			jsonlContent: `{"id":"proj-1"}
{"id":"proj-2"}
{"id":"proj-3"}
`,
			dbPrefix:    "bd",
			shouldWarn:  true,
			description: "no issues use database prefix",
		},
		{
			name: "majority mismatch",
			jsonlContent: `{"id":"proj-1"}
{"id":"proj-2"}
{"id":"proj-3"}
{"id":"proj-4"}
{"id":"bd-5"}
`,
			dbPrefix:    "bd",
			shouldWarn:  true,
			description: "80% of issues use wrong prefix",
		},
		{
			name: "minority mismatch",
			jsonlContent: `{"id":"bd-1"}
{"id":"bd-2"}
{"id":"bd-3"}
{"id":"bd-4"}
{"id":"proj-5"}
`,
			dbPrefix:    "bd",
			shouldWarn:  false,
			description: "only 20% use wrong prefix, not majority",
		},
		{
			name: "exactly half mismatch",
			jsonlContent: `{"id":"bd-1"}
{"id":"bd-2"}
{"id":"proj-3"}
{"id":"proj-4"}
`,
			dbPrefix:    "bd",
			shouldWarn:  false,
			description: "50-50 split should not warn",
		},
		{
			name: "just over majority threshold",
			jsonlContent: `{"id":"bd-1"}
{"id":"bd-2"}
{"id":"proj-3"}
{"id":"proj-4"}
{"id":"proj-5"}
`,
			dbPrefix:    "bd",
			shouldWarn:  true,
			description: "60% use wrong prefix, should warn",
		},
		{
			name: "multiple wrong prefixes",
			jsonlContent: `{"id":"proj-1"}
{"id":"feature-2"}
{"id":"hotfix-3"}
{"id":"bd-4"}
`,
			dbPrefix:    "bd",
			shouldWarn:  false, // no single prefix has majority, so no warning
			description: "75% use various wrong prefixes but no single majority",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
			if err := os.WriteFile(jsonlPath, []byte(tt.jsonlContent), 0600); err != nil {
				t.Fatalf("failed to create JSONL: %v", err)
			}

			jsonlCount, jsonlPrefixes, err := CountJSONLIssues(jsonlPath)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			var mostCommonPrefix string
			maxCount := 0
			for prefix, count := range jsonlPrefixes {
				if count > maxCount {
					maxCount = count
					mostCommonPrefix = prefix
				}
			}

			shouldWarn := mostCommonPrefix != tt.dbPrefix && maxCount > jsonlCount/2

			if shouldWarn != tt.shouldWarn {
				t.Errorf("%s: expected shouldWarn=%v, got %v (most common: %s with %d/%d issues)",
					tt.description, tt.shouldWarn, shouldWarn, mostCommonPrefix, maxCount, jsonlCount)
			}
		})
	}
}

// TestPrefixRenaming_SimulatedMergeConflict tests prefix handling in merge conflict scenarios
func TestPrefixRenaming_SimulatedMergeConflict(t *testing.T) {
	t.Run("merge from branch with different prefix", func(t *testing.T) {
		tmpDir := t.TempDir()
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

		content := `{"id":"main-1","title":"Main branch issue"}
{"id":"main-2","title":"Main branch issue"}
{"id":"main-3","title":"Main branch issue"}
{"id":"feature-4","title":"Feature branch issue - added in merge"}
{"id":"feature-5","title":"Feature branch issue - added in merge"}
`
		if err := os.WriteFile(jsonlPath, []byte(content), 0600); err != nil {
			t.Fatalf("failed to create JSONL: %v", err)
		}

		count, prefixes, err := CountJSONLIssues(jsonlPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if count != 5 {
			t.Errorf("expected 5 issues, got %d", count)
		}

		if prefixes["main"] != 3 {
			t.Errorf("expected 3 'main' prefix issues, got %d", prefixes["main"])
		}

		if prefixes["feature"] != 2 {
			t.Errorf("expected 2 'feature' prefix issues, got %d", prefixes["feature"])
		}
	})

	t.Run("three-way merge with different prefixes", func(t *testing.T) {
		tmpDir := t.TempDir()
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

		content := `{"id":"main-1","title":"Main"}
{"id":"feature-a-2","title":"Feature A"}
{"id":"feature-b-3","title":"Feature B"}
{"id":"main-4","title":"Main"}
{"id":"feature-a-5","title":"Feature A"}
`
		if err := os.WriteFile(jsonlPath, []byte(content), 0600); err != nil {
			t.Fatalf("failed to create JSONL: %v", err)
		}

		count, prefixes, err := CountJSONLIssues(jsonlPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if count != 5 {
			t.Errorf("expected 5 issues, got %d", count)
		}

		expectedPrefixes := map[string]int{
			"main":      2,
			"feature-a": 2,
			"feature-b": 1,
		}

		for prefix, expectedCount := range expectedPrefixes {
			if prefixes[prefix] != expectedCount {
				t.Errorf("prefix %q: expected %d, got %d", prefix, expectedCount, prefixes[prefix])
			}
		}
	})
}

// TestPrefixExtraction_EdgeCases tests edge cases in prefix extraction logic
func TestPrefixExtraction_EdgeCases(t *testing.T) {
	tests := []struct {
		name           string
		issueID        string
		expectedPrefix string
	}{
		{
			name:           "standard format",
			issueID:        "bd-123",
			expectedPrefix: "bd",
		},
		{
			name:           "multi-part prefix",
			issueID:        "my-project-abc",
			expectedPrefix: "my-project",
		},
		{
			name:           "no dash",
			issueID:        "abc123",
			expectedPrefix: "abc123",
		},
		{
			name:           "trailing dash",
			issueID:        "bd-",
			expectedPrefix: "bd",
		},
		{
			name:           "leading dash",
			issueID:        "-123",
			expectedPrefix: "",
		},
		{
			name:           "multiple consecutive dashes",
			issueID:        "bd--123",
			expectedPrefix: "bd-",
		},
		{
			name:           "numeric prefix",
			issueID:        "2024-123",
			expectedPrefix: "2024",
		},
		{
			name:           "mixed case",
			issueID:        "BD-123",
			expectedPrefix: "BD",
		},
		{
			name:           "very long prefix",
			issueID:        "this-is-a-very-long-project-prefix-name-123",
			expectedPrefix: "this-is-a-very-long-project-prefix-name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
			content := `{"id":"` + tt.issueID + `","title":"Test"}` + "\n"
			if err := os.WriteFile(jsonlPath, []byte(content), 0600); err != nil {
				t.Fatalf("failed to create JSONL: %v", err)
			}

			_, prefixes, err := CountJSONLIssues(jsonlPath)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if _, found := prefixes[tt.expectedPrefix]; !found && tt.expectedPrefix != "" {
				t.Errorf("expected prefix %q not found, got prefixes: %v", tt.expectedPrefix, prefixes)
			}
		})
	}
}

// TestPrefixDetection_LargeScale tests prefix detection with larger datasets
func TestPrefixDetection_LargeScale(t *testing.T) {
	t.Run("1000 issues single prefix", func(t *testing.T) {
		tmpDir := t.TempDir()
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

		f, err := os.Create(jsonlPath)
		if err != nil {
			t.Fatalf("failed to create JSONL: %v", err)
		}
		defer f.Close()

		for i := 1; i <= 1000; i++ {
			fmt.Fprintf(f, `{"id":"bd-%d","title":"Issue"}`+"\n", i)
		}

		count, prefixes, err := CountJSONLIssues(jsonlPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if count != 1000 {
			t.Errorf("expected 1000 issues, got %d", count)
		}

		if prefixes["bd"] != 1000 {
			t.Errorf("expected 1000 'bd' prefix issues, got %d", prefixes["bd"])
		}
	})

	t.Run("1000 issues mixed prefixes", func(t *testing.T) {
		tmpDir := t.TempDir()
		jsonlPath := filepath.Join(tmpDir, "issues.jsonl")

		f, err := os.Create(jsonlPath)
		if err != nil {
			t.Fatalf("failed to create JSONL: %v", err)
		}
		defer f.Close()

		for i := 1; i <= 700; i++ {
			fmt.Fprintf(f, `{"id":"bd-%d"}`+"\n", i)
		}
		for i := 1; i <= 200; i++ {
			fmt.Fprintf(f, `{"id":"proj-%d"}`+"\n", i)
		}
		for i := 1; i <= 100; i++ {
			fmt.Fprintf(f, `{"id":"feat-%d"}`+"\n", i)
		}

		count, prefixes, err := CountJSONLIssues(jsonlPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if count != 1000 {
			t.Errorf("expected 1000 issues, got %d", count)
		}

		expected := map[string]int{
			"bd":   700,
			"proj": 200,
			"feat": 100,
		}

		for prefix, expectedCount := range expected {
			if prefixes[prefix] != expectedCount {
				t.Errorf("prefix %q: expected %d, got %d", prefix, expectedCount, prefixes[prefix])
			}
		}
	})
}
