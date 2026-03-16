//go:build cgo

package fix

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

func TestRepoFingerprint_ReinitializePreservesSiblingDatabase(t *testing.T) {
	ctx := context.Background()
	port := fixTestServerPort()
	if port == 0 {
		t.Skip("Dolt test server not available, skipping")
	}

	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	currentDB := uniqueDBName(t)
	siblingDB := uniqueDBName(t)
	cfg := &configfile.Config{
		Backend:        configfile.BackendDolt,
		Database:       "dolt",
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: port,
		DoltDatabase:   currentDB,
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatal(err)
	}

	store, err := dolt.NewFromConfig(ctx, beadsDir)
	if err != nil {
		t.Skipf("skipping: Dolt not available: %v", err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", "rpf"); err != nil {
		_ = store.Close()
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}
	if err := store.CreateIssue(ctx, &types.Issue{
		ID:        "rpf-1",
		Title:     "Old copied issue",
		Status:    "open",
		IssueType: "task",
		Priority:  2,
	}, "test"); err != nil {
		_ = store.Close()
		t.Fatalf("CreateIssue: %v", err)
	}
	_ = store.Close()

	writeTestJSONL(t, beadsDir, []types.Issue{
		{ID: "rpf-2", Title: "Imported issue", Status: "open", IssueType: "task", Priority: 2},
	})

	rootDB := openRootFixTestDB(t, port)
	if _, err := rootDB.Exec("CREATE DATABASE IF NOT EXISTS `" + siblingDB + "`"); err != nil {
		t.Fatalf("create sibling database: %v", err)
	}

	oldReadLine := repoFingerprintReadLine
	defer func() { repoFingerprintReadLine = oldReadLine }()
	responses := []string{"2", "y"}
	repoFingerprintReadLine = func() (string, error) {
		if len(responses) == 0 {
			return "", nil
		}
		response := responses[0]
		responses = responses[1:]
		return response, nil
	}

	if err := RepoFingerprint(repoDir, false); err != nil {
		t.Fatalf("RepoFingerprint returned error: %v", err)
	}

	if !databaseExistsInRootDB(t, rootDB, currentDB) {
		t.Fatalf("current database %q should exist after reinitialize", currentDB)
	}
	if !databaseExistsInRootDB(t, rootDB, siblingDB) {
		t.Fatalf("sibling database %q should still exist after reinitialize", siblingDB)
	}

	verifyStore, err := dolt.NewFromConfig(ctx, beadsDir)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = verifyStore.Close() }()

	issues, err := verifyStore.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "rpf-2" {
		t.Fatalf("reinitialized store has issues %#v, want imported rpf-2 only", issues)
	}
}
