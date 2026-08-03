//go:build cgo

package fix

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
)

// bd-7bpkd: DOLT_RESET('--hard') swaps the tracked issues table's backing
// object and silently drops the FKs that clone-local (dolt_ignored) tables
// hold on it — enforcement stops, orphans accumulate, and nothing re-links
// the constraint (a server restart does not). This drives the severance the
// way production hits it (reset across a commit that touched issues) and
// verifies scan → heal → enforcement restored.
func TestRelinkSeveredCloneLocalFKs_AfterHardReset(t *testing.T) {
	port := fixTestServerPort()
	if port == 0 {
		t.Skip("Dolt test server not available, skipping")
	}

	tmpDir := t.TempDir()
	ctx := context.Background()

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}
	h := sha256.Sum256([]byte(t.Name() + fmt.Sprintf("%d", time.Now().UnixNano())))
	dbName := "fixtest_" + hex.EncodeToString(h[:6])
	store, err := dolt.New(ctx, &dolt.Config{
		Path:            filepath.Join(beadsDir, "beads.db"),
		ServerHost:      "127.0.0.1",
		ServerPort:      port,
		Database:        dbName,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Skipf("skipping: Dolt not available: %v", err)
	}
	t.Cleanup(func() {
		store.Close()
		dropFixTestDatabase(dbName, port)
	})
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("failed to set issue_prefix: %v", err)
	}

	issue := &types.Issue{Title: "anchor", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("failed to create issue: %v", err)
	}

	db := store.UnderlyingDB()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}

	exec("CALL DOLT_ADD('-A')")
	exec("CALL DOLT_COMMIT('-m', 'baseline', '--allow-empty')")
	var baseline string
	if err := db.QueryRowContext(ctx, "SELECT HASHOF('HEAD')").Scan(&baseline); err != nil {
		t.Fatalf("capture baseline: %v", err)
	}

	// A second commit that touches issues, so the reset below swaps its
	// backing object (a no-op reset does not sever anything).
	second := &types.Issue{Title: "swapped away", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, second, "test"); err != nil {
		t.Fatalf("failed to create second issue: %v", err)
	}
	// The store may have auto-committed the write already; --allow-empty keeps
	// this from failing either way. What matters is that HEAD's issues content
	// differs from baseline so the reset below swaps the table's backing object.
	exec("CALL DOLT_ADD('-A')")
	exec("CALL DOLT_COMMIT('-m', 'second', '--allow-empty')")

	if severed, err := scanSeveredCloneLocalFKs(db); err != nil {
		t.Fatalf("pre-reset scan: %v", err)
	} else if len(severed) != 0 {
		t.Fatalf("pre-reset scan = %v, want none severed", severed)
	}

	// Drift guard: the static CloneLocalFKs spec must cover exactly the FKs a
	// freshly migrated store puts on clone-local tables. A new FK added by a
	// migration without a spec entry would silently never be healed.
	rows, err := db.QueryContext(ctx, `
		SELECT tc.TABLE_NAME, tc.CONSTRAINT_NAME
		FROM information_schema.TABLE_CONSTRAINTS tc
		WHERE tc.TABLE_SCHEMA = DATABASE()
		  AND tc.CONSTRAINT_TYPE = 'FOREIGN KEY'
		  AND (tc.TABLE_NAME = 'events' OR tc.TABLE_NAME = 'leases' OR tc.TABLE_NAME = 'local_metadata'
		       OR tc.TABLE_NAME = 'repo_mtimes' OR tc.TABLE_NAME = 'wisps' OR tc.TABLE_NAME LIKE 'wisp\_%')`)
	if err != nil {
		t.Fatalf("enumerate clone-local FKs: %v", err)
	}
	defer rows.Close()
	live := map[string]bool{}
	for rows.Next() {
		var table, constraint string
		if err := rows.Scan(&table, &constraint); err != nil {
			t.Fatalf("scan constraint row: %v", err)
		}
		live[table+"."+constraint] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("constraint rows: %v", err)
	}
	spec := map[string]bool{}
	for _, fk := range CloneLocalFKs {
		spec[fk.Table+"."+fk.Constraint] = true
		if !live[fk.Table+"."+fk.Constraint] {
			t.Errorf("spec FK %s.%s not present on a freshly migrated store", fk.Table, fk.Constraint)
		}
	}
	for key := range live {
		if !spec[key] {
			t.Errorf("clone-local FK %s exists in a fresh store but is missing from CloneLocalFKs — it would never be healed", key)
		}
	}
	if t.Failed() {
		t.FailNow()
	}

	exec("CALL DOLT_RESET('--hard', ?)", baseline)

	severed, err := scanSeveredCloneLocalFKs(db)
	if err != nil {
		t.Fatalf("post-reset scan: %v", err)
	}
	if len(severed) != len(CloneLocalFKs) {
		t.Fatalf("post-reset scan found %d severed FK(s) (%v), want all %d", len(severed), severed, len(CloneLocalFKs))
	}

	// Enforcement is off: an orphan audit row goes straight in.
	exec("INSERT INTO events (id, issue_id, event_type, actor) VALUES ('11111111-1111-1111-1111-111111111111', 'bd-no-such-issue', 'created', 'test')")

	if err := relinkSeveredCloneLocalFKs(db, true); err != nil {
		t.Fatalf("relink: %v", err)
	}

	if severed, err := scanSeveredCloneLocalFKs(db); err != nil {
		t.Fatalf("post-heal scan: %v", err)
	} else if len(severed) != 0 {
		t.Fatalf("post-heal scan = %v, want none severed", severed)
	}

	var orphans int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM events WHERE issue_id = 'bd-no-such-issue'").Scan(&orphans); err != nil {
		t.Fatalf("count orphans: %v", err)
	}
	if orphans != 0 {
		t.Fatalf("orphan rows after heal = %d, want 0", orphans)
	}

	// The re-added FK resolves against the current root and enforces again.
	_, err = db.ExecContext(ctx,
		"INSERT INTO events (id, issue_id, event_type, actor) VALUES ('22222222-2222-2222-2222-222222222222', 'bd-still-no-issue', 'created', 'test')")
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "foreign key") {
		t.Fatalf("bogus insert after heal: err = %v, want foreign key violation", err)
	}
}
