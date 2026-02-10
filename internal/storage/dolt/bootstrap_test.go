//go:build cgo

package dolt

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestBootstrapFromJSONL(t *testing.T) {
	// Create temp directory structure
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	// Create test JSONL file with issues
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	issues := []types.Issue{
		{
			ID:          "test-001",
			Title:       "First issue",
			Description: "Test description 1",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeTask,
			CreatedAt:   time.Now().Add(-time.Hour),
			UpdatedAt:   time.Now(),
		},
		{
			ID:          "test-002",
			Title:       "Second issue",
			Description: "Test description 2",
			Status:      types.StatusInProgress,
			Priority:    1,
			IssueType:   types.TypeBug,
			CreatedAt:   time.Now().Add(-2 * time.Hour),
			UpdatedAt:   time.Now().Add(-30 * time.Minute),
			Labels:      []string{"urgent", "backend"},
		},
		{
			ID:        "test-003",
			Title:     "Closed issue",
			Status:    types.StatusClosed,
			Priority:  3,
			IssueType: types.TypeTask,
		},
	}

	var jsonlContent strings.Builder
	for _, issue := range issues {
		data, err := json.Marshal(issue)
		if err != nil {
			t.Fatalf("failed to marshal issue: %v", err)
		}
		jsonlContent.Write(data)
		jsonlContent.WriteString("\n")
	}

	if err := os.WriteFile(jsonlPath, []byte(jsonlContent.String()), 0644); err != nil {
		t.Fatalf("failed to write JSONL: %v", err)
	}

	// Perform bootstrap
	ctx := context.Background()
	bootstrapped, result, err := Bootstrap(ctx, BootstrapConfig{
		BeadsDir:    beadsDir,
		DoltPath:    doltDir,
		LockTimeout: 10 * time.Second,
	})

	if err != nil {
		t.Fatalf("bootstrap failed: %v", err)
	}
	if !bootstrapped {
		t.Fatal("expected bootstrap to be performed")
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Verify results
	if result.IssuesImported != 3 {
		t.Errorf("expected 3 issues imported, got %d", result.IssuesImported)
	}
	if result.PrefixDetected != "test" {
		t.Errorf("expected prefix 'test', got '%s'", result.PrefixDetected)
	}

	// Open store and verify issues were imported
	store, err := New(ctx, &Config{Path: doltDir})
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer store.Close()

	// Check prefix was set
	prefix, err := store.GetConfig(ctx, "issue_prefix")
	if err != nil {
		t.Fatalf("failed to get config: %v", err)
	}
	if prefix != "test" {
		t.Errorf("expected prefix 'test', got '%s'", prefix)
	}

	// Check issues exist
	issue1, err := store.GetIssue(ctx, "test-001")
	if err != nil {
		t.Fatalf("failed to get issue: %v", err)
	}
	if issue1 == nil {
		t.Fatal("expected issue test-001 to exist")
	}
	if issue1.Title != "First issue" {
		t.Errorf("expected title 'First issue', got '%s'", issue1.Title)
	}

	// Check labels were imported
	issue2, err := store.GetIssue(ctx, "test-002")
	if err != nil {
		t.Fatalf("failed to get issue: %v", err)
	}
	if issue2 == nil {
		t.Fatal("expected issue test-002 to exist")
	}
	if len(issue2.Labels) != 2 {
		t.Errorf("expected 2 labels, got %d", len(issue2.Labels))
	}

	// Verify closed_at was set for closed issue
	issue3, err := store.GetIssue(ctx, "test-003")
	if err != nil {
		t.Fatalf("failed to get issue: %v", err)
	}
	if issue3 == nil {
		t.Fatal("expected issue test-003 to exist")
	}
	if issue3.ClosedAt == nil {
		t.Error("expected closed_at to be set for closed issue")
	}
}

func TestBootstrapNoOpWhenDoltExists(t *testing.T) {
	// Create temp directory structure with existing Dolt DB
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	// Create a Dolt store first
	ctx := context.Background()
	store, err := New(ctx, &Config{Path: doltDir})
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", "existing"); err != nil {
		t.Fatalf("failed to set config: %v", err)
	}
	store.Close()

	// Create JSONL file
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	issue := types.Issue{
		ID:     "new-001",
		Title:  "New issue",
		Status: types.StatusOpen,
	}
	data, _ := json.Marshal(issue)
	if err := os.WriteFile(jsonlPath, append(data, '\n'), 0644); err != nil {
		t.Fatalf("failed to write JSONL: %v", err)
	}

	// Attempt bootstrap - should be no-op
	bootstrapped, result, err := Bootstrap(ctx, BootstrapConfig{
		BeadsDir:    beadsDir,
		DoltPath:    doltDir,
		LockTimeout: 10 * time.Second,
	})

	if err != nil {
		t.Fatalf("bootstrap failed: %v", err)
	}
	if bootstrapped {
		t.Error("expected no bootstrap when Dolt already exists")
	}
	if result != nil {
		t.Error("expected nil result when no bootstrap performed")
	}

	// Verify original prefix preserved
	store, err = New(ctx, &Config{Path: doltDir})
	if err != nil {
		t.Fatalf("failed to reopen store: %v", err)
	}
	defer store.Close()

	prefix, _ := store.GetConfig(ctx, "issue_prefix")
	if prefix != "existing" {
		t.Errorf("expected prefix 'existing', got '%s'", prefix)
	}
}

func TestBootstrapNoOpWhenNoJSONL(t *testing.T) {
	// Create temp directory structure without JSONL
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	// Attempt bootstrap - should be no-op
	ctx := context.Background()
	bootstrapped, result, err := Bootstrap(ctx, BootstrapConfig{
		BeadsDir:    beadsDir,
		DoltPath:    doltDir,
		LockTimeout: 10 * time.Second,
	})

	if err != nil {
		t.Fatalf("bootstrap failed: %v", err)
	}
	if bootstrapped {
		t.Error("expected no bootstrap when no JSONL exists")
	}
	if result != nil {
		t.Error("expected nil result when no bootstrap performed")
	}
}

func TestBootstrapGracefulDegradation(t *testing.T) {
	// Create temp directory structure
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	// Create JSONL with some malformed lines
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	goodIssue := types.Issue{
		ID:     "test-001",
		Title:  "Good issue",
		Status: types.StatusOpen,
	}
	goodData, _ := json.Marshal(goodIssue)

	content := string(goodData) + "\n" +
		"{invalid json}\n" +
		"<<<<<<< HEAD\n" + // Git conflict marker
		string(goodData) + "\n" // Duplicate - will be skipped

	if err := os.WriteFile(jsonlPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write JSONL: %v", err)
	}

	// Perform bootstrap
	ctx := context.Background()
	bootstrapped, result, err := Bootstrap(ctx, BootstrapConfig{
		BeadsDir:    beadsDir,
		DoltPath:    doltDir,
		LockTimeout: 10 * time.Second,
	})

	if err != nil {
		t.Fatalf("bootstrap failed: %v", err)
	}
	if !bootstrapped {
		t.Fatal("expected bootstrap to be performed")
	}

	// Should have parse errors for malformed lines
	if len(result.ParseErrors) != 2 {
		t.Errorf("expected 2 parse errors, got %d", len(result.ParseErrors))
	}

	// Should have imported the good issue
	if result.IssuesImported != 1 {
		t.Errorf("expected 1 issue imported, got %d", result.IssuesImported)
	}

	// Duplicate should be skipped (not errored)
	if result.IssuesSkipped != 1 {
		t.Errorf("expected 1 issue skipped, got %d", result.IssuesSkipped)
	}
}

func TestParseJSONLWithErrors(t *testing.T) {
	// Create temp file with mixed content
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "test.jsonl")

	goodIssue := types.Issue{
		ID:     "test-001",
		Title:  "Good issue",
		Status: types.StatusOpen,
	}
	goodData, _ := json.Marshal(goodIssue)

	content := string(goodData) + "\n" +
		"\n" + // Empty line - should be skipped
		"   \n" + // Whitespace line - should be skipped
		"{broken\n" + // Invalid JSON
		"<<<<<<< HEAD\n" + // Git conflict
		"=======\n" + // Git conflict
		">>>>>>> branch\n" + // Git conflict
		string(goodData) + "\n" // Another good line

	if err := os.WriteFile(jsonlPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	issues, errors := parseJSONLWithErrors(jsonlPath)

	if len(issues) != 2 {
		t.Errorf("expected 2 issues, got %d", len(issues))
	}

	// Should have 4 parse errors: 1 invalid JSON + 3 conflict markers
	if len(errors) != 4 {
		t.Errorf("expected 4 parse errors, got %d: %+v", len(errors), errors)
	}
}

func TestDetectPrefixFromIssues(t *testing.T) {
	issues := []*types.Issue{
		{ID: "proj-001"},
		{ID: "proj-002"},
		{ID: "proj-003"},
		{ID: "other-001"},
	}

	prefix := detectPrefixFromIssues(issues)
	if prefix != "proj" {
		t.Errorf("expected prefix 'proj', got '%s'", prefix)
	}
}

func TestBootstrapWithRoutesAndInteractions(t *testing.T) {
	// Create temp directory structure
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	// Create test issues JSONL
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	issue := types.Issue{
		ID:     "test-001",
		Title:  "Test issue",
		Status: types.StatusOpen,
	}
	data, _ := json.Marshal(issue)
	if err := os.WriteFile(jsonlPath, append(data, '\n'), 0644); err != nil {
		t.Fatalf("failed to write issues JSONL: %v", err)
	}

	// Create test routes JSONL
	routesPath := filepath.Join(beadsDir, "routes.jsonl")
	routesContent := `{"prefix":"test-","path":"."}
{"prefix":"other-","path":"other/rig"}
`
	if err := os.WriteFile(routesPath, []byte(routesContent), 0644); err != nil {
		t.Fatalf("failed to write routes JSONL: %v", err)
	}

	// Create test interactions JSONL
	interactionsPath := filepath.Join(beadsDir, "interactions.jsonl")
	interactionsContent := `{"id":"int-001","kind":"llm_call","created_at":"2025-01-20T10:00:00Z","actor":"test-agent","model":"claude-3"}
{"id":"int-002","kind":"tool_call","created_at":"2025-01-20T10:01:00Z","actor":"test-agent","tool_name":"bash","exit_code":0}
`
	if err := os.WriteFile(interactionsPath, []byte(interactionsContent), 0644); err != nil {
		t.Fatalf("failed to write interactions JSONL: %v", err)
	}

	// Perform bootstrap
	ctx := context.Background()
	bootstrapped, result, err := Bootstrap(ctx, BootstrapConfig{
		BeadsDir:    beadsDir,
		DoltPath:    doltDir,
		LockTimeout: 10 * time.Second,
	})

	if err != nil {
		t.Fatalf("bootstrap failed: %v", err)
	}
	if !bootstrapped {
		t.Fatal("expected bootstrap to be performed")
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Verify issues imported
	if result.IssuesImported != 1 {
		t.Errorf("expected 1 issue imported, got %d", result.IssuesImported)
	}

	// Verify routes imported
	if result.RoutesImported != 2 {
		t.Errorf("expected 2 routes imported, got %d", result.RoutesImported)
	}

	// Verify interactions imported
	if result.InteractionsImported != 2 {
		t.Errorf("expected 2 interactions imported, got %d", result.InteractionsImported)
	}

	// Open store and verify data
	store, err := New(ctx, &Config{Path: doltDir})
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer store.Close()

	// Verify routes table
	var routeCount int
	err = store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM routes").Scan(&routeCount)
	if err != nil {
		t.Fatalf("failed to count routes: %v", err)
	}
	if routeCount != 2 {
		t.Errorf("expected 2 routes in table, got %d", routeCount)
	}

	// Verify interactions table
	var interactionCount int
	err = store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM interactions").Scan(&interactionCount)
	if err != nil {
		t.Fatalf("failed to count interactions: %v", err)
	}
	if interactionCount != 2 {
		t.Errorf("expected 2 interactions in table, got %d", interactionCount)
	}
}

func TestParseJSONLValidation(t *testing.T) {
	tmpDir := t.TempDir()
	jsonlPath := filepath.Join(tmpDir, "test.jsonl")

	// Create issues with various validation problems
	now := time.Now()

	t.Run("empty ID rejected", func(t *testing.T) {
		issue := types.Issue{Title: "No ID", Status: types.StatusOpen, IssueType: types.TypeTask}
		data, _ := json.Marshal(issue)
		if err := os.WriteFile(jsonlPath, append(data, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
		issues, errs := parseJSONLWithErrors(jsonlPath)
		if len(issues) != 0 {
			t.Errorf("expected 0 issues, got %d", len(issues))
		}
		if len(errs) != 1 || !strings.Contains(errs[0].Message, "empty ID") {
			t.Errorf("expected empty ID error, got %+v", errs)
		}
	})

	t.Run("invalid status rejected", func(t *testing.T) {
		// Marshal manually to inject invalid status
		content := `{"id":"test-1","title":"Bad status","status":"opne","type":"task"}` + "\n"
		if err := os.WriteFile(jsonlPath, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		issues, errs := parseJSONLWithErrors(jsonlPath)
		if len(issues) != 0 {
			t.Errorf("expected 0 issues, got %d", len(issues))
		}
		if len(errs) != 1 || !strings.Contains(errs[0].Message, "invalid status") {
			t.Errorf("expected invalid status error, got %+v", errs)
		}
	})

	t.Run("closed_at fixed for non-closed", func(t *testing.T) {
		issue := types.Issue{
			ID: "test-1", Title: "Open with closed_at", Status: types.StatusOpen,
			IssueType: types.TypeTask, ClosedAt: &now,
		}
		data, _ := json.Marshal(issue)
		if err := os.WriteFile(jsonlPath, append(data, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
		issues, errs := parseJSONLWithErrors(jsonlPath)
		if len(errs) != 0 {
			t.Errorf("expected no errors, got %+v", errs)
		}
		if len(issues) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(issues))
		}
		if issues[0].ClosedAt != nil {
			t.Error("expected closed_at to be cleared for open issue")
		}
	})

	t.Run("tombstone missing deleted_at gets fixed", func(t *testing.T) {
		issue := types.Issue{
			ID: "test-1", Title: "Tombstone no deleted_at", Status: types.StatusTombstone,
			IssueType: types.TypeTask,
		}
		data, _ := json.Marshal(issue)
		if err := os.WriteFile(jsonlPath, append(data, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
		issues, errs := parseJSONLWithErrors(jsonlPath)
		if len(errs) != 0 {
			t.Errorf("expected no errors, got %+v", errs)
		}
		if len(issues) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(issues))
		}
		if issues[0].DeletedAt == nil {
			t.Error("expected deleted_at to be set for tombstone")
		}
	})

	t.Run("non-tombstone with deleted_at gets cleared", func(t *testing.T) {
		issue := types.Issue{
			ID: "test-1", Title: "Open with deleted_at", Status: types.StatusOpen,
			IssueType: types.TypeTask, DeletedAt: &now,
		}
		data, _ := json.Marshal(issue)
		if err := os.WriteFile(jsonlPath, append(data, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
		issues, errs := parseJSONLWithErrors(jsonlPath)
		if len(errs) != 0 {
			t.Errorf("expected no errors, got %+v", errs)
		}
		if len(issues) != 1 {
			t.Fatalf("expected 1 issue, got %d", len(issues))
		}
		if issues[0].DeletedAt != nil {
			t.Error("expected deleted_at to be cleared for open issue")
		}
	})
}

func TestBootstrapTombstoneResurrectionProtection(t *testing.T) {
	// If JSONL has both a live and tombstone version of the same ID,
	// the tombstone should win (prevent resurrection)
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	liveIssue := types.Issue{
		ID: "test-001", Title: "Live version", Status: types.StatusOpen, IssueType: types.TypeTask,
	}
	tombstoneIssue := types.Issue{
		ID: "test-001", Title: "Live version", Status: types.StatusTombstone,
		IssueType: types.TypeTask, DeletedAt: &now,
	}

	liveData, _ := json.Marshal(liveIssue)
	tombData, _ := json.Marshal(tombstoneIssue)

	// Live version appears first, tombstone second (chronological order)
	content := string(liveData) + "\n" + string(tombData) + "\n"
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	bootstrapped, result, err := Bootstrap(ctx, BootstrapConfig{
		BeadsDir: beadsDir, DoltPath: doltDir, LockTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("bootstrap failed: %v", err)
	}
	if !bootstrapped {
		t.Fatal("expected bootstrap")
	}

	// Should import the tombstone version, skip the live version
	if result.IssuesImported != 1 {
		t.Errorf("expected 1 imported, got %d", result.IssuesImported)
	}
	if result.IssuesSkipped != 1 {
		t.Errorf("expected 1 skipped, got %d", result.IssuesSkipped)
	}

	// Verify the imported issue is the tombstone
	store, err := New(ctx, &Config{Path: doltDir})
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	defer store.Close()

	issue, err := store.GetIssue(ctx, "test-001")
	if err != nil {
		t.Fatalf("failed to get issue: %v", err)
	}
	if issue == nil {
		t.Fatal("expected issue to exist")
	}
	if issue.Status != types.StatusTombstone {
		t.Errorf("expected tombstone status, got %s", issue.Status)
	}
}

func TestBootstrapDuplicateExternalRef(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	extRef := "https://linear.app/team/PROJ-123"
	issue1 := types.Issue{
		ID: "test-001", Title: "First", Status: types.StatusOpen,
		IssueType: types.TypeTask, ExternalRef: &extRef,
	}
	issue2 := types.Issue{
		ID: "test-002", Title: "Second with same ref", Status: types.StatusOpen,
		IssueType: types.TypeTask, ExternalRef: &extRef,
	}

	data1, _ := json.Marshal(issue1)
	data2, _ := json.Marshal(issue2)
	content := string(data1) + "\n" + string(data2) + "\n"

	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	bootstrapped, result, err := Bootstrap(ctx, BootstrapConfig{
		BeadsDir: beadsDir, DoltPath: doltDir, LockTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("bootstrap failed: %v", err)
	}
	if !bootstrapped {
		t.Fatal("expected bootstrap")
	}

	// First issue imported, second skipped (duplicate external_ref)
	if result.IssuesImported != 1 {
		t.Errorf("expected 1 imported, got %d", result.IssuesImported)
	}
	if result.IssuesSkipped != 1 {
		t.Errorf("expected 1 skipped, got %d", result.IssuesSkipped)
	}
}

func TestBootstrapWithoutOptionalFiles(t *testing.T) {
	// Test that bootstrap succeeds when routes.jsonl and interactions.jsonl don't exist
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create beads dir: %v", err)
	}

	// Create only issues JSONL
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	issue := types.Issue{
		ID:     "test-001",
		Title:  "Test issue",
		Status: types.StatusOpen,
	}
	data, _ := json.Marshal(issue)
	if err := os.WriteFile(jsonlPath, append(data, '\n'), 0644); err != nil {
		t.Fatalf("failed to write issues JSONL: %v", err)
	}

	// Perform bootstrap - should succeed even without routes.jsonl and interactions.jsonl
	ctx := context.Background()
	bootstrapped, result, err := Bootstrap(ctx, BootstrapConfig{
		BeadsDir:    beadsDir,
		DoltPath:    doltDir,
		LockTimeout: 10 * time.Second,
	})

	if err != nil {
		t.Fatalf("bootstrap failed: %v", err)
	}
	if !bootstrapped {
		t.Fatal("expected bootstrap to be performed")
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Verify issues imported
	if result.IssuesImported != 1 {
		t.Errorf("expected 1 issue imported, got %d", result.IssuesImported)
	}

	// Routes and interactions should be 0 (files don't exist)
	if result.RoutesImported != 0 {
		t.Errorf("expected 0 routes imported, got %d", result.RoutesImported)
	}
	if result.InteractionsImported != 0 {
		t.Errorf("expected 0 interactions imported, got %d", result.InteractionsImported)
	}
}
