//go:build cgo

package fix

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
)

// newIdentityTestStore is a variant of newFixTestStore that seeds a
// project_id shared by metadata.json and the database, matching what a real
// `bd init` produces.
func newIdentityTestStore(t *testing.T, dir, prefix string) (store *dolt.DoltStore, beadsDir, projectID string) {
	t.Helper()
	testutil.RequireDoltBinary(t)
	ctx := context.Background()

	requireFixDoltContainer(t)
	port := fixTestServerPort()

	beadsDir = filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("create .beads: %v", err)
	}

	// Hash the test name: subtest names blow past Dolt's database-name
	// length limit if used verbatim (bd-nxt5e).
	h := sha256.Sum256([]byte(t.Name() + fmt.Sprintf("%d", time.Now().UnixNano())))
	dbName := "fixident_" + hex.EncodeToString(h[:6])

	projectID = configfile.GenerateProjectID()
	cfg := &configfile.Config{
		Database:       "dolt",
		DoltMode:       configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
		DoltServerPort: port,
		DoltDatabase:   dbName,
		ProjectID:      projectID,
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}

	store, err := dolt.New(ctx, &dolt.Config{
		Path:            filepath.Join(beadsDir, "beads.db"),
		ServerHost:      "127.0.0.1",
		ServerPort:      port,
		Database:        dbName,
		CreateIfMissing: true,
		MaxOpenConns:    1,
	})
	if err != nil {
		t.Fatalf("dolt.New against running test container: %v", err)
	}

	if err := store.SetConfig(ctx, "issue_prefix", prefix); err != nil {
		store.Close()
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}
	if err := store.SetMetadata(ctx, "_project_id", projectID); err != nil {
		store.Close()
		t.Fatalf("SetMetadata(_project_id): %v", err)
	}

	t.Cleanup(func() {
		store.Close()
		dropFixTestDatabase(dbName, port)
	})
	return store, beadsDir, projectID
}

// TestDestructiveFix_AbortsOnProjectIdentityMismatch is the mybd-2qegi
// regression test: destructive doctor --fix paths (remotes.go's openFixDB)
// dial a fresh connection on every call, re-resolving the dolt server port
// independently of whatever connection doctor's read-only checks used. A
// stale .beads/dolt-server.port file (or the shared-mode default port) can
// aim that fresh connection at a different project's database — one that
// commonly shares the same default database name ("beads").
//
// This models the observable effect a stale/wrong port resolution would
// have: the connection openDoltDB opens belongs to a database whose stored
// _project_id does not match the local metadata.json project_id it was
// diagnosed against. verifyFixTargetIdentity must catch that and abort
// before any DELETE/UPDATE runs, regardless of *how* the mismatched
// connection was reached.
func TestDestructiveFix_AbortsOnProjectIdentityMismatch(t *testing.T) {
	dir := t.TempDir()
	store, _, _ := newIdentityTestStore(t, dir, "tst")
	ctx := context.Background()

	// Simulate the stale-port scenario: the connection openDoltDB resolves
	// lands on a database belonging to a *different* project than the local
	// metadata.json describes.
	if err := store.SetMetadata(ctx, "_project_id", "wrong-project-"+configfile.GenerateProjectID()); err != nil {
		t.Fatalf("failed to force a project_id mismatch: %v", err)
	}

	// Plant a mis-keyed dependency row that a real DependencyKeys fix would
	// re-key/delete if it were allowed to proceed against this connection.
	for _, id := range []string{"tst-1", "tst-2"} {
		issue := &types.Issue{
			ID:        id,
			Title:     "mismatch guard test " + id,
			Priority:  2,
			Status:    types.StatusOpen,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
	}
	const randomID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	db := store.UnderlyingDB()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by)
		VALUES (?, 'tst-1', 'tst-2', 'blocks', NOW(), 'test')`, randomID); err != nil {
		t.Fatalf("insert randomly-keyed dependency: %v", err)
	}

	// The destructive fix must refuse to touch this connection.
	err := DependencyKeys(dir, false)
	if err == nil {
		t.Fatal("DependencyKeys should have aborted on project identity mismatch, got nil error")
	}
	if !strings.Contains(err.Error(), "PROJECT IDENTITY MISMATCH") {
		t.Errorf("expected a PROJECT IDENTITY MISMATCH error, got: %v", err)
	}

	// The anomalous row must be untouched — no re-key, no delete.
	var count int
	if scanErr := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM dependencies WHERE id = ?`, randomID).Scan(&count); scanErr != nil {
		t.Fatalf("count dependency row: %v", scanErr)
	}
	if count != 1 {
		t.Errorf("expected the mis-keyed row to survive the aborted fix, found %d matching rows", count)
	}

	// Same guard must apply to the other destructive fix entrypoints that
	// share openDoltDB/openFixDB, including RecomputeBlocked — which
	// orderDoctorFixes (cmd/bd/doctor_fix.go) runs LAST in a `bd doctor
	// --fix` pass, after every graph-mutating fix, and which would otherwise
	// still land a DOLT_COMMIT into the wrong project's history.
	for name, fn := range map[string]func(string) error{
		"CrossTableDuplicates":    func(p string) error { return CrossTableDuplicates(p, false) },
		"OrphanedDependencies":    func(p string) error { return OrphanedDependencies(p, false) },
		"ChildParentDependencies": func(p string) error { return ChildParentDependencies(p, false) },
		"RecomputeBlocked":        RecomputeBlocked,
	} {
		t.Run(name, func(t *testing.T) {
			if err := fn(dir); err == nil {
				t.Fatalf("%s should have aborted on project identity mismatch, got nil error", name)
			} else if !strings.Contains(err.Error(), "PROJECT IDENTITY MISMATCH") {
				t.Errorf("%s: expected a PROJECT IDENTITY MISMATCH error, got: %v", name, err)
			}
		})
	}
}

// TestDestructiveFix_SkipsOnUnverifiableTarget covers the non-fatal-skip
// half of the guard: a workspace whose local metadata.json has no
// project_id (pre-project_id projects, shared-server-mode workspaces that
// never got backfilled, or a user who declined the interactive "Project
// Identity" fix) must not be permanently locked out of every other
// doctor fix — the destructive statements still must not run, but the
// caller gets nil back, not an error, matching the existing "database
// unreachable" skip convention at each call site.
func TestDestructiveFix_SkipsOnUnverifiableTarget(t *testing.T) {
	dir := t.TempDir()
	store, beadsDir, _ := newIdentityTestStore(t, dir, "skp")
	ctx := context.Background()

	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.ProjectID = ""
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("save config: %v", err)
	}

	const randomID = "bbbbbbbb-cccc-dddd-eeee-ffffffffffff"
	for _, id := range []string{"skp-1", "skp-2"} {
		issue := &types.Issue{
			ID:        id,
			Title:     "skip guard test " + id,
			Priority:  2,
			Status:    types.StatusOpen,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", id, err)
		}
	}
	db := store.UnderlyingDB()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by)
		VALUES (?, 'skp-1', 'skp-2', 'blocks', NOW(), 'test')`, randomID); err != nil {
		t.Fatalf("insert randomly-keyed dependency: %v", err)
	}

	if err := DependencyKeys(dir, false); err != nil {
		t.Fatalf("DependencyKeys should skip (nil error) on an unverifiable target, got: %v", err)
	}

	// Still not touched — the skip must be as safe as the abort.
	var count int
	if scanErr := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM dependencies WHERE id = ?`, randomID).Scan(&count); scanErr != nil {
		t.Fatalf("count dependency row: %v", scanErr)
	}
	if count != 1 {
		t.Errorf("expected the mis-keyed row to survive the skipped fix, found %d matching rows", count)
	}
}

// TestVerifyFixTargetIdentity covers verifyFixTargetIdentity directly: match
// passes; mismatch aborts with a plain (non-errUnverifiableFixTarget) error;
// unverifiable targets (missing project_id on either side) return an error
// wrapping errUnverifiableFixTarget. Each case gets its own store so the
// subtests don't depend on run order.
func TestVerifyFixTargetIdentity(t *testing.T) {
	t.Run("matching project ids pass", func(t *testing.T) {
		store, beadsDir, _ := newIdentityTestStore(t, t.TempDir(), "vfi1")
		if err := verifyFixTargetIdentity(store.UnderlyingDB(), beadsDir, nil); err != nil {
			t.Errorf("expected no error for matching project ids, got: %v", err)
		}
	})

	t.Run("mismatched project ids abort with a plain error", func(t *testing.T) {
		store, beadsDir, _ := newIdentityTestStore(t, t.TempDir(), "vfi2")
		ctx := context.Background()
		if err := store.SetMetadata(ctx, "_project_id", "some-other-project"); err != nil {
			t.Fatalf("SetMetadata: %v", err)
		}
		err := verifyFixTargetIdentity(store.UnderlyingDB(), beadsDir, nil)
		if err == nil {
			t.Fatal("expected mismatch error, got nil")
		}
		if !strings.Contains(err.Error(), "PROJECT IDENTITY MISMATCH") {
			t.Errorf("expected PROJECT IDENTITY MISMATCH, got: %v", err)
		}
		if errors.Is(err, errUnverifiableFixTarget) {
			t.Errorf("a confirmed mismatch must not be classified as unverifiable: %v", err)
		}
	})

	t.Run("missing database project_id is unverifiable", func(t *testing.T) {
		store, beadsDir, _ := newIdentityTestStore(t, t.TempDir(), "vfi3")
		ctx := context.Background()
		db := store.UnderlyingDB()
		if _, err := db.ExecContext(ctx, "DELETE FROM metadata WHERE `key` = '_project_id'"); err != nil {
			t.Fatalf("clear _project_id: %v", err)
		}
		err := verifyFixTargetIdentity(db, beadsDir, nil)
		if err == nil {
			t.Fatal("expected unverifiable-target error, got nil")
		}
		if !errors.Is(err, errUnverifiableFixTarget) {
			t.Errorf("expected an errUnverifiableFixTarget error, got: %v", err)
		}
	})

	t.Run("missing local project_id is unverifiable", func(t *testing.T) {
		store, beadsDir, _ := newIdentityTestStore(t, t.TempDir(), "vfi4")
		cfg, err := configfile.Load(beadsDir)
		if err != nil || cfg == nil {
			t.Fatalf("load config: %v", err)
		}
		cfg.ProjectID = ""
		if err := cfg.Save(beadsDir); err != nil {
			t.Fatalf("save config: %v", err)
		}
		verr := verifyFixTargetIdentity(store.UnderlyingDB(), beadsDir, nil)
		if verr == nil {
			t.Fatal("expected unverifiable-target error for missing local project_id, got nil")
		}
		if !errors.Is(verr, errUnverifiableFixTarget) {
			t.Errorf("expected an errUnverifiableFixTarget error, got: %v", verr)
		}
	})

	t.Run("passed-in cfg is used instead of reloading", func(t *testing.T) {
		store, beadsDir, projectID := newIdentityTestStore(t, t.TempDir(), "vfi5")
		// A cfg with a different (wrong) project_id than what's on disk —
		// if verifyFixTargetIdentity ignored the passed-in cfg and reloaded
		// from beadsDir instead, this would incorrectly pass.
		staleCfg := &configfile.Config{ProjectID: "stale-in-memory-id"}
		err := verifyFixTargetIdentity(store.UnderlyingDB(), beadsDir, staleCfg)
		if err == nil {
			t.Fatal("expected mismatch using the passed-in (stale) cfg, got nil")
		}
		if !strings.Contains(err.Error(), "PROJECT IDENTITY MISMATCH") {
			t.Errorf("expected PROJECT IDENTITY MISMATCH, got: %v", err)
		}
		// Sanity: the real on-disk project_id still matches the database.
		onDisk, loadErr := configfile.Load(beadsDir)
		if loadErr != nil || onDisk == nil || onDisk.ProjectID != projectID {
			t.Fatalf("on-disk project_id drifted unexpectedly: %+v, err=%v", onDisk, loadErr)
		}
	})
}
