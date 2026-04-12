//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

func TestResolveAndGetIssueWithRouting_FallsBackToRoutesTableDatabase(t *testing.T) {
	ensureTestMode(t)

	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available, skipping")
	}

	ctx := context.Background()
	tmpDir := t.TempDir()

	townBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0o755); err != nil {
		t.Fatalf("mkdir town .beads: %v", err)
	}

	townDBPath := filepath.Join(townBeadsDir, "dolt")
	townStore := newTestStoreIsolatedDB(t, townDBPath, "hq")

	remoteDBName := uniqueTestDBName(t)
	remoteDBPath := filepath.Join(tmpDir, "shadow", ".beads", "dolt")
	writeTestMetadata(t, remoteDBPath, remoteDBName)

	doltNewMutex.Lock()
	remoteStore, err := dolt.New(ctx, &dolt.Config{
		Path:            remoteDBPath,
		ServerHost:      "127.0.0.1",
		ServerPort:      testDoltServerPort,
		Database:        remoteDBName,
		CreateIfMissing: true,
	})
	doltNewMutex.Unlock()
	if err != nil {
		t.Fatalf("create remote store: %v", err)
	}
	t.Cleanup(func() {
		_ = remoteStore.Close()
		dropTestDatabase(remoteDBName, testDoltServerPort)
	})

	if err := remoteStore.SetConfig(ctx, "issue_prefix", "gj"); err != nil {
		t.Fatalf("set remote issue_prefix: %v", err)
	}

	issue := &types.Issue{
		ID:        "gj-er8.10",
		Title:     "Recovered from routes table",
		Status:    types.StatusClosed,
		Priority:  2,
		IssueType: types.TypeFeature,
	}
	if err := remoteStore.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("create remote issue: %v", err)
	}

	// Simulate the stale projection bug: the authoritative routes table still has
	// the prefix, but routes.jsonl no longer includes it.
	staleRoutePath := filepath.Join(tmpDir, remoteDBName)
	if _, err := townStore.DB().ExecContext(ctx, "INSERT INTO routes (prefix, path) VALUES (?, ?)", "gj-", staleRoutePath); err != nil {
		t.Fatalf("insert routes row: %v", err)
	}
	routesJSONL := `{"prefix":"hq-","path":"."}` + "\n" + `{"prefix":"hq-cv-","path":"."}` + "\n"
	if err := os.WriteFile(filepath.Join(townBeadsDir, "routes.jsonl"), []byte(routesJSONL), 0o644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	oldDBPath := dbPath
	dbPath = townDBPath
	t.Cleanup(func() { dbPath = oldDBPath })

	wrappedTownStore := storage.NewHookFiringStore(townStore, nil)
	result, err := resolveAndGetIssueWithRouting(ctx, wrappedTownStore, issue.ID)
	if err != nil {
		t.Fatalf("resolveAndGetIssueWithRouting: %v", err)
	}
	if result == nil {
		t.Fatal("resolveAndGetIssueWithRouting returned nil result")
	}
	defer result.Close()

	if !result.Routed {
		t.Fatal("expected result.Routed to be true")
	}
	if result.ResolvedID != issue.ID {
		t.Fatalf("ResolvedID = %q, want %q", result.ResolvedID, issue.ID)
	}
	if result.Issue.Title != issue.Title {
		t.Fatalf("Issue.Title = %q, want %q", result.Issue.Title, issue.Title)
	}

	gotPrefix, err := result.Store.GetConfig(ctx, "issue_prefix")
	if err != nil {
		t.Fatalf("GetConfig(issue_prefix): %v", err)
	}
	if gotPrefix != "gj" {
		t.Fatalf("issue_prefix = %q, want %q", gotPrefix, "gj")
	}
}
