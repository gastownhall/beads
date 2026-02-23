//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

func TestSqlCommand(t *testing.T) {
	tempDir := t.TempDir()
	testDBPath := filepath.Join(tempDir, ".beads", "test.db")

	if err := os.MkdirAll(filepath.Dir(testDBPath), 0755); err != nil {
		t.Fatalf("Failed to create .beads directory: %v", err)
	}

	testStore, err := dolt.New(context.Background(), &dolt.Config{Path: testDBPath})
	if err != nil {
		t.Skipf("skipping: Dolt server not available: %v", err)
	}
	defer testStore.Close()

	ctx := context.Background()

	if err := testStore.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("Failed to set issue prefix: %v", err)
	}

	// Create test data
	testIssues := []*types.Issue{
		{
			Title:     "Test issue one",
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		},
		{
			Title:     "Test issue two",
			Status:    types.StatusClosed,
			Priority:  2,
			IssueType: types.TypeBug,
		},
	}

	for _, issue := range testIssues {
		if err := testStore.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("Failed to create test issue: %v", err)
		}
	}

	// Save/restore globals
	oldStore := store
	oldCtx := rootCtx
	oldJSON := jsonOutput
	defer func() {
		store = oldStore
		rootCtx = oldCtx
		jsonOutput = oldJSON
	}()

	store = testStore
	rootCtx = ctx

	t.Run("select count", func(t *testing.T) {
		jsonOutput = true
		output := captureStdout(t, func() error {
			sqlCmd.Run(sqlCmd, []string{"SELECT COUNT(*) as count FROM issues"})
			return nil
		})

		var result []map[string]interface{}
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatalf("Failed to parse JSON output: %v\nOutput: %s", err, output)
		}

		if len(result) != 1 {
			t.Fatalf("Expected 1 row, got %d", len(result))
		}
		count, ok := result[0]["count"]
		if !ok {
			t.Fatal("Missing 'count' column in result")
		}
		if count.(float64) != 2 {
			t.Errorf("Expected count=2, got %v", count)
		}
	})

	t.Run("select with filter", func(t *testing.T) {
		jsonOutput = true
		output := captureStdout(t, func() error {
			sqlCmd.Run(sqlCmd, []string{`SELECT id, title FROM issues WHERE status = 'open'`})
			return nil
		})

		var result []map[string]interface{}
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatalf("Failed to parse JSON output: %v\nOutput: %s", err, output)
		}

		if len(result) != 1 {
			t.Fatalf("Expected 1 open issue, got %d", len(result))
		}
		title := result[0]["title"].(string)
		if title != "Test issue one" {
			t.Errorf("Expected 'Test issue one', got %q", title)
		}
	})

	t.Run("empty result json", func(t *testing.T) {
		jsonOutput = true
		output := captureStdout(t, func() error {
			sqlCmd.Run(sqlCmd, []string{`SELECT * FROM issues WHERE title = 'nonexistent'`})
			return nil
		})

		var result []map[string]interface{}
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatalf("Failed to parse JSON output: %v\nOutput: %s", err, output)
		}

		if len(result) != 0 {
			t.Errorf("Expected 0 rows, got %d", len(result))
		}
	})

	t.Run("table output", func(t *testing.T) {
		jsonOutput = false
		output := captureStdout(t, func() error {
			sqlCmd.Run(sqlCmd, []string{"SELECT COUNT(*) as count FROM issues"})
			return nil
		})

		if !strings.Contains(output, "count") {
			t.Errorf("Expected table header 'count' in output: %s", output)
		}
		if !strings.Contains(output, "(1 rows)") {
			t.Errorf("Expected '(1 rows)' in output: %s", output)
		}
	})

	t.Run("empty table output", func(t *testing.T) {
		jsonOutput = false
		output := captureStdout(t, func() error {
			sqlCmd.Run(sqlCmd, []string{`SELECT * FROM issues WHERE title = 'nonexistent'`})
			return nil
		})

		if !strings.Contains(output, "(0 rows)") {
			t.Errorf("Expected '(0 rows)' in output: %s", output)
		}
	})
}

func TestSqlCommandInit(t *testing.T) {
	found := false
	for _, cmd := range rootCmd.Commands() {
		if cmd.Use == "sql <query>" {
			found = true
			if cmd.GroupID != "maint" {
				t.Errorf("Expected GroupID 'maint', got %q", cmd.GroupID)
			}
			break
		}
	}
	if !found {
		t.Error("sql command not registered with rootCmd")
	}
}
