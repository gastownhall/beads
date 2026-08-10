package dolt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
)

// setupAnyServerStore returns a store on the shared test server when TestMain
// brought one up (Docker), and otherwise starts a private local dolt
// sql-server from the dolt binary — the same recipe as setupWispCascadeStore
// — so the test still runs on machines without a container runtime.
func setupAnyServerStore(t *testing.T) (*DoltStore, func()) {
	t.Helper()
	if testServerPort != 0 {
		return setupTestStore(t)
	}
	return setupLocalServerStore(t)
}

// setupLocalServerStore starts a dedicated dolt sql-server in a temp dir and
// opens a DoltStore against it. Not parallel: it mutates process env via
// t.Setenv, which is only safe here because every shared-server test skips
// when this path is taken (no container).
func setupLocalServerStore(t *testing.T) (*DoltStore, func()) {
	t.Helper()
	testutil.RequireDoltBinary(t)

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}

	t.Setenv("BEADS_DOLT_SHARED_SERVER", "0")
	t.Setenv("BEADS_DOLT_AUTO_START", "1")

	state, err := doltserver.Start(beadsDir)
	if err != nil {
		t.Fatalf("doltserver.Start: %v", err)
	}
	t.Cleanup(func() { _ = doltserver.Stop(beadsDir) })

	ctx, cancel := testContext(t)
	defer cancel()

	store, err := New(ctx, &Config{
		Path:            filepath.Join(beadsDir, "dolt"),
		BeadsDir:        beadsDir,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		Database:        uniqueTestDBName(t),
		ServerHost:      "127.0.0.1",
		ServerPort:      state.Port,
		ServerUser:      "root",
		CreateIfMissing: true, // creates and schema-inits the fresh database
		MaxOpenConns:    1,    // Required: DOLT_CHECKOUT is session-level
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}

	return store, func() {}
}

// TestWispSeesCustomTypeRegisteredInTransaction verifies that a wisp created
// in the same transaction that registers its custom type validates against
// the fresh registration. Wisp rows are written on the ignored-tables
// session, but the validation context (config, custom_types) must be read
// from the regular session, or in-transaction registration stays invisible
// (GH#5443).
func TestWispSeesCustomTypeRegisteredInTransaction(t *testing.T) {
	store, cleanup := setupAnyServerStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	wisp := &types.Issue{
		Title:     "custom-typed wisp",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.IssueType("duty"),
		Ephemeral: true,
	}
	batchWisp := &types.Issue{
		Title:     "custom-typed wisp via batch",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.IssueType("duty"),
		Ephemeral: true,
	}
	err := store.RunInTransaction(ctx, "register type then create wisps", func(tx storage.Transaction) error {
		if err := tx.SetConfig(ctx, "types.custom", `["duty"]`); err != nil {
			return err
		}
		if err := tx.CreateIssue(ctx, wisp, "test-user"); err != nil {
			return err
		}
		return tx.CreateIssues(ctx, []*types.Issue{batchWisp}, "test-user")
	})
	if err != nil {
		t.Fatalf("wisp create after in-tx type registration failed: %v", err)
	}

	for _, id := range []string{wisp.ID, batchWisp.ID} {
		got, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("GetIssue(%s) failed for created wisp: %v", id, err)
		}
		if got.IssueType != types.IssueType("duty") {
			t.Errorf("IssueType of %s = %q, want %q", id, got.IssueType, "duty")
		}
	}
}
