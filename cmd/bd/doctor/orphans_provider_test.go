package doctor

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

// mockIssueProvider implements types.IssueProvider for testing FindOrphanedIssues
type mockIssueProvider struct {
	issues []*types.Issue
	prefix string
	err    error // If set, GetOpenIssues returns this error
}

func (m *mockIssueProvider) GetOpenIssues(ctx context.Context) ([]*types.Issue, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.issues, nil
}

func (m *mockIssueProvider) GetIssuePrefix() string {
	if m.prefix == "" {
		return "bd"
	}
	return m.prefix
}

// Ensure mockIssueProvider implements types.IssueProvider
var _ types.IssueProvider = (*mockIssueProvider)(nil)

// setupTestGitRepo creates a git repo with specified commits for testing
func setupTestGitRepo(t *testing.T, commits []string) string {
	t.Helper()
	dir := t.TempDir()

	// Initialize git repo from cached template
	initGitTemplate()
	if gitTemplateErr != nil {
		t.Fatalf("git template init failed: %v", gitTemplateErr)
	}
	if err := copyGitDir(gitTemplateDir, dir); err != nil {
		t.Fatalf("failed to copy git template: %v", err)
	}

	// Create commits
	for i, msg := range commits {
		testFile := filepath.Join(dir, "file.txt")
		content := []byte("content " + string(rune('0'+i)))
		if err := os.WriteFile(testFile, content, 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}
		cmd := exec.Command("git", "add", "file.txt")
		cmd.Dir = dir
		_ = cmd.Run()
		cmd = exec.Command("git", "commit", "-m", msg)
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to create commit: %v", err)
		}
	}

	return dir
}

// TestFindOrphanedIssues_WithMockProvider tests FindOrphanedIssues with various mock providers
func TestFindOrphanedIssues_WithMockProvider(t *testing.T) {
	tests := map[string]struct {
		provider *mockIssueProvider
		commits  []string
		expected int    // Number of orphans expected
		issueID  string // Expected issue ID in orphans (if expected > 0)
	}{
		"UT-01: Basic orphan detection": {
			provider: &mockIssueProvider{
				issues: []*types.Issue{
					{ID: "bd-abc", Title: "Test issue", Status: types.StatusOpen},
				},
				prefix: "bd",
			},
			commits:  []string{"Initial commit", "Fix bug (bd-abc)"},
			expected: 1,
			issueID:  "bd-abc",
		},
		"UT-02: No orphans when no matching commits": {
			provider: &mockIssueProvider{
				issues: []*types.Issue{
					{ID: "bd-xyz", Title: "Test issue", Status: types.StatusOpen},
				},
				prefix: "bd",
			},
			commits:  []string{"Initial commit", "Some other change"},
			expected: 0,
		},
		"UT-03: Custom prefix TEST": {
			provider: &mockIssueProvider{
				issues: []*types.Issue{
					{ID: "TEST-001", Title: "Test issue", Status: types.StatusOpen},
				},
				prefix: "TEST",
			},
			commits:  []string{"Initial commit", "Implement feature (TEST-001)"},
			expected: 1,
			issueID:  "TEST-001",
		},
		"UT-04: Multiple orphans": {
			provider: &mockIssueProvider{
				issues: []*types.Issue{
					{ID: "bd-aaa", Title: "Issue A", Status: types.StatusOpen},
					{ID: "bd-bbb", Title: "Issue B", Status: types.StatusOpen},
					{ID: "bd-ccc", Title: "Issue C", Status: types.StatusOpen},
				},
				prefix: "bd",
			},
			commits:  []string{"Initial commit", "Fix (bd-aaa)", "Fix (bd-ccc)"},
			expected: 2,
		},
		"UT-06: In-progress issues included": {
			provider: &mockIssueProvider{
				issues: []*types.Issue{
					{ID: "bd-wip", Title: "Work in progress", Status: types.StatusInProgress},
				},
				prefix: "bd",
			},
			commits:  []string{"Initial commit", "WIP (bd-wip)"},
			expected: 1,
			issueID:  "bd-wip",
		},
		"UT-08: Empty provider returns empty slice": {
			provider: &mockIssueProvider{
				issues: []*types.Issue{},
				prefix: "bd",
			},
			commits:  []string{"Initial commit", "Some change (bd-xxx)"},
			expected: 0,
		},
		"UT-09: Hierarchical IDs": {
			provider: &mockIssueProvider{
				issues: []*types.Issue{
					{ID: "bd-abc.1", Title: "Subtask", Status: types.StatusOpen},
				},
				prefix: "bd",
			},
			commits:  []string{"Initial commit", "Fix subtask (bd-abc.1)"},
			expected: 1,
			issueID:  "bd-abc.1",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			gitDir := setupTestGitRepo(t, tt.commits)

			orphans, err := FindOrphanedIssues(gitDir, tt.provider)
			if err != nil {
				t.Fatalf("FindOrphanedIssues returned error: %v", err)
			}

			if len(orphans) != tt.expected {
				t.Errorf("expected %d orphans, got %d", tt.expected, len(orphans))
			}

			if tt.issueID != "" && len(orphans) > 0 {
				found := false
				for _, o := range orphans {
					if o.IssueID == tt.issueID {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected to find orphan with ID %q, but didn't", tt.issueID)
				}
			}
		})
	}
}

// TestFindOrphanedIssues_CrossRepo tests cross-repo orphan detection (IT-02).
// This is the key test that validates the --db flag is honored.
// The test creates:
//   - A "planning" directory with a mock provider (simulating external DB)
//   - A "code" directory with git commits referencing the planning issues
//
// The test asserts that FindOrphanedIssues uses the provider's issues/prefix,
// NOT any local .beads/ directory.
func TestFindOrphanedIssues_CrossRepo(t *testing.T) {
	// Setup: code repo with commits referencing PLAN-xxx issues
	codeDir := setupTestGitRepo(t, []string{
		"Initial commit",
		"Implement feature (PLAN-001)",
		"Fix bug (PLAN-002)",
	})

	// Simulate planning repo's provider (this would normally come from --db flag)
	planningProvider := &mockIssueProvider{
		issues: []*types.Issue{
			{ID: "PLAN-001", Title: "Feature A", Status: types.StatusOpen},
			{ID: "PLAN-002", Title: "Bug B", Status: types.StatusOpen},
			{ID: "PLAN-003", Title: "Not referenced", Status: types.StatusOpen},
		},
		prefix: "PLAN",
	}

	// Create a LOCAL .beads/ in the code repo to verify it's NOT used
	localBeadsDir := filepath.Join(codeDir, ".beads")
	if err := os.MkdirAll(localBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	localDBPath := filepath.Join(localBeadsDir, "beads.db")
	localDB, err := sql.Open("sqlite3", localDBPath)
	if err != nil {
		t.Fatal(err)
	}
	// Local DB has different prefix and issues - should NOT be used
	_, err = localDB.Exec(`
		CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT);
		CREATE TABLE issues (id TEXT PRIMARY KEY, status TEXT, title TEXT);
		INSERT INTO config (key, value) VALUES ('issue_prefix', 'LOCAL');
		INSERT INTO issues (id, status, title) VALUES ('LOCAL-999', 'open', 'Local issue');
	`)
	if err != nil {
		t.Fatal(err)
	}
	localDB.Close()

	// Call FindOrphanedIssues with the planning provider
	orphans, err := FindOrphanedIssues(codeDir, planningProvider)
	if err != nil {
		t.Fatalf("FindOrphanedIssues returned error: %v", err)
	}

	// Assert: Should find 2 orphans (PLAN-001, PLAN-002) from planning provider
	if len(orphans) != 2 {
		t.Errorf("expected 2 orphans, got %d", len(orphans))
	}

	// Assert: Should NOT find LOCAL-999 (proves local .beads/ was ignored)
	for _, o := range orphans {
		if o.IssueID == "LOCAL-999" {
			t.Error("found LOCAL-999 orphan - local .beads/ was incorrectly used")
		}
		if o.IssueID != "PLAN-001" && o.IssueID != "PLAN-002" {
			t.Errorf("unexpected orphan ID: %s", o.IssueID)
		}
	}

	// Assert: PLAN-003 should NOT be in orphans (not referenced in commits)
	for _, o := range orphans {
		if o.IssueID == "PLAN-003" {
			t.Error("found PLAN-003 orphan - it was not referenced in commits")
		}
	}
}

// TestFindOrphanedIssues_LocalProvider tests backward compatibility (RT-01).
// This tests the findOrphanedIssuesFromPath function which creates a LocalProvider
// from the local .beads/ directory.
func TestFindOrphanedIssues_LocalProvider(t *testing.T) {
	dir := t.TempDir()

	// Initialize git repo from cached template
	initGitTemplate()
	if gitTemplateErr != nil {
		t.Fatalf("git template init failed: %v", gitTemplateErr)
	}
	if err := copyGitDir(gitTemplateDir, dir); err != nil {
		t.Fatalf("failed to copy git template: %v", err)
	}

	// Create .beads directory and database
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(beadsDir, "beads.db")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}

	_, err = db.Exec(`
		CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT);
		CREATE TABLE issues (id TEXT PRIMARY KEY, status TEXT, title TEXT);
		INSERT INTO config (key, value) VALUES ('issue_prefix', 'bd');
		INSERT INTO issues (id, status, title) VALUES ('bd-local', 'open', 'Local test');
	`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Create commits with issue reference
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", "test.txt")
	cmd.Dir = dir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "Fix (bd-local)")
	cmd.Dir = dir
	_ = cmd.Run()

	// Use findOrphanedIssuesFromPath (the backward-compatible wrapper)
	orphans, err := findOrphanedIssuesFromPath(dir)
	if err != nil {
		t.Fatalf("findOrphanedIssuesFromPath returned error: %v", err)
	}

	if len(orphans) != 1 {
		t.Errorf("expected 1 orphan, got %d", len(orphans))
	}

	if len(orphans) > 0 && orphans[0].IssueID != "bd-local" {
		t.Errorf("expected orphan ID bd-local, got %s", orphans[0].IssueID)
	}
}

// TestFindOrphanedIssues_ProviderError tests error handling (UT-07).
// When provider returns an error, FindOrphanedIssues should return empty slice.
func TestFindOrphanedIssues_ProviderError(t *testing.T) {
	gitDir := setupTestGitRepo(t, []string{"Initial commit", "Fix (bd-abc)"})

	provider := &mockIssueProvider{
		err: errors.New("provider error: database unavailable"),
	}

	orphans, err := FindOrphanedIssues(gitDir, provider)

	// Should return empty slice, no error (graceful degradation)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
	if len(orphans) != 0 {
		t.Errorf("expected 0 orphans on provider error, got %d", len(orphans))
	}
}

// TestFindOrphanedIssues_NotGitRepo tests behavior in non-git directory (IT-04).
func TestFindOrphanedIssues_NotGitRepo(t *testing.T) {
	dir := t.TempDir() // No git init

	provider := &mockIssueProvider{
		issues: []*types.Issue{
			{ID: "bd-test", Title: "Test", Status: types.StatusOpen},
		},
		prefix: "bd",
	}

	orphans, err := FindOrphanedIssues(dir, provider)

	// Should return empty slice, no error
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
	if len(orphans) != 0 {
		t.Errorf("expected 0 orphans in non-git dir, got %d", len(orphans))
	}
}

// TestFindOrphanedIssues_IntegrationCrossRepo tests a realistic cross-repo setup (IT-02 full).
// This creates real SQLite databases in two directories and verifies the full flow.
func TestFindOrphanedIssues_IntegrationCrossRepo(t *testing.T) {
	// Create two directories: planning (has DB) and code (has git)
	planningDir := t.TempDir()
	codeDir := t.TempDir()

	// Setup planning database
	planningBeadsDir := filepath.Join(planningDir, ".beads")
	if err := os.MkdirAll(planningBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	planningDBPath := filepath.Join(planningBeadsDir, "beads.db")
	planningDB, err := sql.Open("sqlite3", planningDBPath)
	if err != nil {
		t.Fatal(err)
	}

	_, err = planningDB.Exec(`
		CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT);
		CREATE TABLE issues (id TEXT PRIMARY KEY, status TEXT, title TEXT);
		INSERT INTO config (key, value) VALUES ('issue_prefix', 'PLAN');
		INSERT INTO issues (id, status, title) VALUES ('PLAN-001', 'open', 'Feature A');
		INSERT INTO issues (id, status, title) VALUES ('PLAN-002', 'in_progress', 'Feature B');
	`)
	if err != nil {
		t.Fatal(err)
	}
	planningDB.Close()

	// Setup code repo with git (from cached template)
	initGitTemplate()
	if gitTemplateErr != nil {
		t.Fatalf("git template init failed: %v", gitTemplateErr)
	}
	if err := copyGitDir(gitTemplateDir, codeDir); err != nil {
		t.Fatalf("failed to copy git template: %v", err)
	}

	// Create commits referencing planning issues
	testFile := filepath.Join(codeDir, "main.go")
	if err := os.WriteFile(testFile, []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", "main.go")
	cmd.Dir = codeDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "Implement (PLAN-001)")
	cmd.Dir = codeDir
	_ = cmd.Run()

	// Create a real LocalProvider from the planning database
	provider, err := storage.NewLocalProvider(planningDBPath)
	if err != nil {
		t.Fatalf("failed to create LocalProvider: %v", err)
	}
	defer provider.Close()

	// Run FindOrphanedIssues with the cross-repo provider
	orphans, err := FindOrphanedIssues(codeDir, provider)
	if err != nil {
		t.Fatalf("FindOrphanedIssues returned error: %v", err)
	}

	// Should find 1 orphan (PLAN-001)
	if len(orphans) != 1 {
		t.Errorf("expected 1 orphan, got %d", len(orphans))
	}

	if len(orphans) > 0 && orphans[0].IssueID != "PLAN-001" {
		t.Errorf("expected orphan PLAN-001, got %s", orphans[0].IssueID)
	}
}

// TestLocalProvider_Methods tests the LocalProvider implementation directly.
func TestLocalProvider_Methods(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Create test database
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}

	_, err = db.Exec(`
		CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT);
		CREATE TABLE issues (id TEXT PRIMARY KEY, status TEXT, title TEXT);
		INSERT INTO config (key, value) VALUES ('issue_prefix', 'CUSTOM');
		INSERT INTO issues (id, status, title) VALUES ('CUSTOM-001', 'open', 'Open issue');
		INSERT INTO issues (id, status, title) VALUES ('CUSTOM-002', 'in_progress', 'WIP issue');
		INSERT INTO issues (id, status, title) VALUES ('CUSTOM-003', 'closed', 'Closed issue');
	`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Create provider
	provider, err := storage.NewLocalProvider(dbPath)
	if err != nil {
		t.Fatalf("storage.NewLocalProvider failed: %v", err)
	}
	defer provider.Close()

	// Test GetIssuePrefix
	prefix := provider.GetIssuePrefix()
	if prefix != "CUSTOM" {
		t.Errorf("expected prefix CUSTOM, got %s", prefix)
	}

	// Test GetOpenIssues
	issues, err := provider.GetOpenIssues(context.Background())
	if err != nil {
		t.Fatalf("GetOpenIssues failed: %v", err)
	}

	// Should return 2 issues (open + in_progress), not the closed one
	if len(issues) != 2 {
		t.Errorf("expected 2 issues, got %d", len(issues))
	}

	// Verify issue IDs
	ids := make(map[string]bool)
	for _, issue := range issues {
		ids[issue.ID] = true
	}

	if !ids["CUSTOM-001"] {
		t.Error("expected CUSTOM-001 in open issues")
	}
	if !ids["CUSTOM-002"] {
		t.Error("expected CUSTOM-002 in open issues")
	}
	if ids["CUSTOM-003"] {
		t.Error("CUSTOM-003 (closed) should not be in open issues")
	}
}

// TestLocalProvider_DefaultPrefix tests that LocalProvider returns "bd" when no prefix configured.
func TestLocalProvider_DefaultPrefix(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Create database without issue_prefix config
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}

	_, err = db.Exec(`
		CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT);
		CREATE TABLE issues (id TEXT PRIMARY KEY, status TEXT, title TEXT);
	`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	provider, err := storage.NewLocalProvider(dbPath)
	if err != nil {
		t.Fatalf("storage.NewLocalProvider failed: %v", err)
	}
	defer provider.Close()

	prefix := provider.GetIssuePrefix()
	if prefix != "bd" {
		t.Errorf("expected default prefix 'bd', got %s", prefix)
	}
}
