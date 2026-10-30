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
// one init minted locally before it reached the database (#7576).
func TestProxiedServerInitSharedDatabaseAdoptsIdentityAndKeepsFingerprints(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	database, args := sharedProxiedInitArgs(t, sharedProxiedScopedUser)

	a := bdProxiedInit(t, bd, "shid", args...)
	a.database = database
	dbProjectID := readProxiedMetadata(t, openProxiedDB(t, a), "_project_id")
	if dbProjectID == "" {
		t.Fatal("precondition: the first clone's init recorded no _project_id")
	}

	// A fresh clone with no metadata.json: init mints a project id locally,
	// then adopts the database's.
	b := bdProxiedInit(t, bd, "shid", args...)
	b.database = database
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
