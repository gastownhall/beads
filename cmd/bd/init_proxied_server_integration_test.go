//go:build cgo

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os/exec"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// TestProxiedServerInitSharedDatabaseAdoptsIdentityAndKeepsFingerprints drives
// several clones through plain (non --team-server) `bd init --proxied-server`
// against ONE shared database. A later clone adopts the identity the first
// one recorded, and metadata.json must carry that adopted id rather than the
// one init minted locally before it reached the database (#7576). No clone
// records its repo_id/clone_id fingerprints in the shared metadata table, where
// each init would overwrite the previous clone's (#7577).
func TestProxiedServerInitSharedDatabaseAdoptsIdentityAndKeepsFingerprints(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	database, args := sharedProxiedInitArgs(t, sharedProxiedScopedUser)

	a := bdProxiedInit(t, bd, "shid", args...)
	a.database = database
	dbA := openProxiedDB(t, a)
	dbProjectID := readProxiedMetadata(t, dbA, "_project_id")
	if dbProjectID == "" {
		t.Fatal("precondition: the first clone's init recorded no _project_id")
	}

	// Every clone of a proxied server shares this metadata table, so a
	// per-clone fingerprint has no row to live in (#7577).
	var fingerprints int
	if err := dbA.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM metadata WHERE `key` IN ('repo_id', 'clone_id')").Scan(&fingerprints); err != nil {
		t.Fatalf("count fingerprint rows: %v", err)
	}
	if fingerprints != 0 {
		t.Errorf("AC-2: bootstrapping init wrote %d repo_id/clone_id row(s) into the shared metadata table, want 0", fingerprints)
	}

	// The rows an older bd left behind belong to some other clone; a later
	// init must leave them alone.
	if _, err := dbA.ExecContext(context.Background(),
		"REPLACE INTO metadata (`key`, value) VALUES ('repo_id', 'seed-repo'), ('clone_id', 'seed-clone')"); err != nil {
		t.Fatalf("seed fingerprint rows: %v", err)
	}

	// A fresh clone with no metadata.json: init mints a project id locally,
	// then adopts the database's.
	b := bdProxiedInit(t, bd, "shid", args...)
	b.database = database
	dbB := openProxiedDB(t, b)
	if got := readProxiedMetadata(t, dbB, "repo_id"); got != "seed-repo" {
		t.Errorf("AC-3: second clone's init left repo_id = %q, want the earlier %q", got, "seed-repo")
	}
	if got := readProxiedMetadata(t, dbB, "clone_id"); got != "seed-clone" {
		t.Errorf("AC-3: second clone's init left clone_id = %q, want the earlier %q", got, "seed-clone")
	}
	cfg, err := configfile.Load(b.beadsDir)
	if err != nil || cfg == nil {
		t.Fatalf("load second clone metadata.json: %v", err)
	}
	if cfg.ProjectID != dbProjectID {
		t.Errorf("AC-1: second clone metadata.json project_id = %q, want the database's _project_id %q", cfg.ProjectID, dbProjectID)
	}
	if committed := gitShowFile(t, b.dir, "HEAD:.beads/metadata.json"); committed != "" {
		var committedCfg configfile.Config
		if err := json.Unmarshal([]byte(committed), &committedCfg); err != nil {
			t.Fatalf("parse committed metadata.json: %v\n%s", err, committed)
		}
		if committedCfg.ProjectID != dbProjectID {
			t.Errorf("AC-1: second clone committed metadata.json project_id = %q, want %q", committedCfg.ProjectID, dbProjectID)
		}
	}

	// A database carrying a prefix but no _project_id is identified all the
	// same, so a further clone adopts it — and must keep its own minted id
	// rather than save the empty one.
	if _, err := dbB.ExecContext(context.Background(), "DELETE FROM metadata WHERE `key` = '_project_id'"); err != nil {
		t.Fatalf("drop _project_id: %v", err)
	}
	c := bdProxiedInit(t, bd, "shid", args...)
	c.database = database
	cfg, err = configfile.Load(c.beadsDir)
	if err != nil || cfg == nil {
		t.Fatalf("load third clone metadata.json: %v", err)
	}
	if cfg.ProjectID == "" {
		t.Error("AC-4: third clone adopted a prefix-only database and saved an empty metadata.json project_id")
	}
	if got := readProxiedMetadata(t, openProxiedDB(t, c), "_project_id"); got != "" {
		t.Errorf("third clone's init recorded _project_id %q on a prefix-only database it adopted", got)
	}
}

// readProxiedMetadata reads key from the database's metadata table, "" when
// the row is absent.
func readProxiedMetadata(t *testing.T, db *sql.DB, key string) string {
	t.Helper()
	var value string
	err := db.QueryRowContext(context.Background(), "SELECT value FROM metadata WHERE `key` = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return ""
	}
	if err != nil {
		t.Fatalf("read metadata %s: %v", key, err)
	}
	return value
}

// gitShowFile returns `git show <rev>` in dir, "" when the object is absent
// (init commits .beads/ only when the clone is a git repository with a
// working tree it may commit to).
func gitShowFile(t *testing.T, dir, rev string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "show", rev).Output()
	if err != nil {
		return ""
	}
	return string(out)
}
