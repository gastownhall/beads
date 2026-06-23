//go:build cgo

package fix

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
)

// TestDependencyKeys_RekeysAndRemovesLeftovers exercises the bd-6dnrw.17
// scan + repair: a dependency row carrying a per-clone-random id (the
// 0043-era DEFAULT (UUID()) shape) is re-keyed to depid.New, and a malformed
// row with no target (which the migration backfill deliberately skips) is
// removed.
func TestDependencyKeys_RekeysAndRemovesLeftovers(t *testing.T) {
	testutil.RequireDoltBinary(t)

	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("create .beads: %v", err)
	}
	cfg := &configfile.Config{
		Database: "dolt",
		Backend:  configfile.BackendDolt,
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save config: %v", err)
	}

	// Unique database name: the test Dolt container may outlive a single run.
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("rand: %v", err)
	}
	dbName := "fixdepkeys_" + hex.EncodeToString(buf)

	ctx := context.Background()
	store, err := dolt.New(ctx, &dolt.Config{
		Path:            filepath.Join(beadsDir, "dolt"),
		Database:        dbName,
		CreateIfMissing: true,
		MaxOpenConns:    1,
	})
	if err != nil {
		t.Skipf("skipping: Dolt server not available: %v", err)
	}
	defer func() { _ = store.Close() }()

	if err := store.SetConfig(ctx, "issue_prefix", "tst"); err != nil {
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}

	for _, id := range []string{"tst-1", "tst-2"} {
		issue := &types.Issue{
			ID:        id,
			Title:     "dep key test " + id,
			Priority:  2,
			Status:    types.StatusOpen,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
	}

	db := store.UnderlyingDB()

	// A randomly-keyed edge, as DEFAULT (UUID()) would have minted it.
	const randomID = "12345678-1234-1234-1234-123456789abc"
	if _, err := db.ExecContext(ctx, `
		INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by)
		VALUES (?, 'tst-1', 'tst-2', 'blocks', NOW(), 'test')`, randomID); err != nil {
		t.Fatalf("insert randomly-keyed dependency: %v", err)
	}

	// A targetless row: drop the guard constraint to simulate pre-guard data.
	const orphanID = "deadbeef-dead-dead-dead-deaddeadbeef"
	if _, err := db.ExecContext(ctx, `ALTER TABLE dependencies DROP CHECK ck_dep_one_target`); err != nil {
		t.Fatalf("drop ck_dep_one_target: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO dependencies (id, issue_id, type, created_at, created_by)
		VALUES (?, 'tst-1', 'blocks', NOW(), 'test')`, orphanID); err != nil {
		t.Fatalf("insert targetless dependency: %v", err)
	}

	wantID := depid.New("tst-1", "tst-2")

	anomalies, err := ScanDependencyKeys(ctx, db)
	if err != nil {
		t.Fatalf("ScanDependencyKeys: %v", err)
	}
	if len(anomalies) != 1 || anomalies[0].Table != "dependencies" {
		t.Fatalf("expected anomalies in dependencies only, got %+v", anomalies)
	}
	if got := anomalies[0].MisKeyed; len(got) != 1 || got[0] != [2]string{randomID, wantID} {
		t.Errorf("MisKeyed = %v, want [[%s %s]]", got, randomID, wantID)
	}
	if got := anomalies[0].NullTarget; len(got) != 1 || got[0] != orphanID {
		t.Errorf("NullTarget = %v, want [%s]", got, orphanID)
	}

	if err := repairDependencyKeys(ctx, db, true); err != nil {
		t.Fatalf("repairDependencyKeys: %v", err)
	}

	anomalies, err = ScanDependencyKeys(ctx, db)
	if err != nil {
		t.Fatalf("ScanDependencyKeys after fix: %v", err)
	}
	if len(anomalies) != 0 {
		t.Errorf("expected no anomalies after fix, got %+v", anomalies)
	}

	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM dependencies WHERE id = ?`, wantID).Scan(&count); err != nil {
		t.Fatalf("count re-keyed row: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly one row under deterministic id %s, got %d", wantID, count)
	}
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM dependencies WHERE id IN (?, ?)`, randomID, orphanID).Scan(&count); err != nil {
		t.Fatalf("count leftover rows: %v", err)
	}
	if count != 0 {
		t.Errorf("expected leftover rows gone, found %d", count)
	}
}
