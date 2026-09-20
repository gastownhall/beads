//go:build cgo

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
)

// TestApplyFixList_ChildParentRecomputesBlocked guards #6608: when the graph is
// already consistently deadlocked at scan time (is_blocked matches the
// child→parent blocking dep), CheckBlockedState passes so "Blocked State" is
// not in the fix list. Removing the offending dependency under
// --fix-child-parent must still recompute is_blocked, or the freed issue stays
// blocked with no dependency left to explain it.
func TestApplyFixList_ChildParentRecomputesBlocked(t *testing.T) {
	testutil.RequireDoltBinary(t)
	if testDoltServerPort == 0 {
		t.Skip("skipping: Dolt test server not running")
	}
	ctx := context.Background()

	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte(fmt.Sprintf("%s%d", t.Name(), time.Now().UnixNano())))
	dbName := "fixblocked_" + hex.EncodeToString(h[:6])
	projectID := configfile.GenerateProjectID()
	cfg := &configfile.Config{
		Database:       "dolt",
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: testDoltServerPort,
		DoltDatabase:   dbName,
		ProjectID:      projectID,
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatal(err)
	}
	store, err := dolt.New(ctx, &dolt.Config{
		Path:            filepath.Join(beadsDir, "beads.db"),
		ServerHost:      "127.0.0.1",
		ServerPort:      testDoltServerPort,
		Database:        dbName,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("dolt.New: %v", err)
	}
	t.Cleanup(func() {
		store.Close()
		if db := store.UnderlyingDB(); db != nil {
			_, _ = db.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
		}
	})
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetMetadata(ctx, "_project_id", projectID); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"bd-abc", "bd-abc.1"} {
		issue := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, IssueType: types.TypeTask, CreatedAt: time.Now()}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatal(err)
		}
	}
	// Child waits on its open parent: a consistent deadlock, so is_blocked=1
	// already matches the dependency graph.
	dep := &types.Dependency{IssueID: "bd-abc.1", DependsOnID: "bd-abc", Type: types.DepBlocks, CreatedAt: time.Now(), CreatedBy: "test"}
	if err := store.AddDependency(ctx, dep, "test"); err != nil {
		t.Fatal(err)
	}

	isBlocked := func() int {
		t.Helper()
		var v int
		if err := store.UnderlyingDB().QueryRow("SELECT is_blocked FROM issues WHERE id = ?", "bd-abc.1").Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if got := isBlocked(); got != 1 {
		t.Fatalf("precondition: is_blocked = %d, want 1 (consistent deadlock)", got)
	}

	oldFlag := doctorFixChildParent
	doctorFixChildParent = true
	t.Cleanup(func() { doctorFixChildParent = oldFlag })

	// Only Child-Parent Dependencies: Blocked State is not in the list.
	applyFixList(dir, []doctorCheck{{Name: "Child-Parent Dependencies"}})

	var deps int
	if err := store.UnderlyingDB().QueryRow("SELECT COUNT(*) FROM dependencies").Scan(&deps); err != nil {
		t.Fatal(err)
	}
	if deps != 0 {
		t.Fatalf("dependencies = %d, want 0 after fix", deps)
	}
	if got := isBlocked(); got != 0 {
		t.Errorf("is_blocked = %d after removing the only blocking dep, want 0 (stale)", got)
	}
}
