package dolt

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
)

// setupAnyServerStore returns a store on the shared test server when TestMain
// brought one up (Docker), and otherwise starts a private local dolt
// sql-server from the dolt binary — the same recipe as
// TestMultiProcessSchemaInit_DoltVerify — so the test still runs on machines
// without a container runtime.
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
	doltPath, err := exec.LookPath("dolt")
	if err != nil {
		t.Skip("dolt binary not found in PATH")
	}

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0700); err != nil {
		t.Fatalf("mkdir dolt: %v", err)
	}

	env := append(os.Environ(), "HOME="+tmpDir, "DOLT_ROOT_PATH="+tmpDir)
	for _, args := range [][]string{
		{"config", "--global", "--add", "user.name", "test"},
		{"config", "--global", "--add", "user.email", "test@example.com"},
		{"init"},
	} {
		cmd := exec.Command(doltPath, args...)
		cmd.Dir = doltDir
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("dolt %v: %v\n%s", args, err, out)
		}
	}

	t.Setenv("BEADS_DOLT_SHARED_SERVER", "0")
	t.Setenv("BEADS_DOLT_AUTO_START", "1")

	state, err := doltserver.Start(beadsDir)
	if err != nil {
		t.Fatalf("doltserver.Start: %v", err)
	}
	stopServer := func() { _ = doltserver.Stop(beadsDir) }

	ctx, cancel := testContext(t)
	defer cancel()

	dbName := uniqueTestDBName(t)
	adminDSN := doltutil.ServerDSN{Host: "127.0.0.1", Port: state.Port, User: "root"}.String()
	adminDB, err := sql.Open("mysql", adminDSN)
	if err != nil {
		stopServer()
		t.Fatalf("admin connect: %v", err)
	}
	_, err = adminDB.ExecContext(ctx, "CREATE DATABASE `"+dbName+"`")
	adminDB.Close()
	if err != nil {
		stopServer()
		t.Fatalf("create database: %v", err)
	}

	store, err := New(ctx, &Config{
		Path:           doltDir,
		BeadsDir:       beadsDir,
		CommitterName:  "test",
		CommitterEmail: "test@example.com",
		Database:       dbName,
		ServerPort:     state.Port,
		MaxOpenConns:   1, // Required: DOLT_CHECKOUT is session-level
	})
	if err != nil {
		stopServer()
		t.Fatalf("New: %v", err)
	}
	if _, err := initSchemaOnDB(ctx, store.db); err != nil {
		store.Close()
		stopServer()
		t.Fatalf("initSchemaOnDB: %v", err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		store.Close()
		stopServer()
		t.Fatalf("set issue_prefix: %v", err)
	}

	return store, func() {
		store.Close()
		stopServer()
	}
}

// TestWispSeesCustomTypeRegisteredInTransaction verifies that a wisp created
// in the same transaction that registers its custom type validates against
// the fresh registration. Wisp rows are written on the ignored-tables
// session, but the validation context (config, custom_types) must be read
// from the regular session, or in-transaction registration — how
// ensureSubgraphCustomTypes works during a wisp pour — stays invisible
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
