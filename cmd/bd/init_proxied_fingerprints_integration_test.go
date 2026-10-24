//go:build cgo

package main

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/testutil"
)

// readMetadataValue reads one row from the shared database's generic
// key/value metadata table, past bd entirely, the same way
// TestProxiedServerExternalCreate reads the issues table directly.
func readMetadataValue(t *testing.T, port, database, key string) string {
	t.Helper()
	dsn := fmt.Sprintf("root:@tcp(127.0.0.1:%s)/%s?parseTime=true", port, database)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("sql.Open %s: %v", dsn, err)
	}
	defer func() { _ = db.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var value string
	err = db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE `key` = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return ""
	}
	if err != nil {
		t.Fatalf("SELECT metadata[%q] from %s: %v", key, database, err)
	}
	return value
}

// TestProxiedServerInitDoesNotWriteSharedFingerprints pins GH#7577: repo_id and
// clone_id are per-clone fingerprints, but the shared database's metadata
// table has exactly one row per key, so writing them there on every init was
// last-init-wins — a second clone's init silently corrupted the first
// clone's recorded fingerprint, and the cross-project mismatch diagnostics
// the fingerprint exists for read whichever clone happened to init last.
//
// Two clones init against the SAME shared external database (same prefix, so
// the same sanitized database name): the first bootstraps it, the second
// adopts the identity it finds. Neither write must leave a repo_id/clone_id
// row in the shared metadata table — there is no per-clone destination for
// them in proxied-server mode, so the fix is to not write them there at all,
// the same way team-server mode already does not.
func TestProxiedServerInitDoesNotWriteSharedFingerprints(t *testing.T) {
	requireProxiedServerEnv(t)

	bd := buildEmbeddedBD(t)
	port := testutil.StartIsolatedDoltContainer(t)

	const prefix = "idshare"
	database := sanitizePrefixForDB(prefix)

	cloneA := bdProxiedInit(t, bd, prefix,
		"--proxied-server-external-host", "127.0.0.1",
		"--proxied-server-external-port", port,
	)
	if got := readMetadataValue(t, port, database, "repo_id"); got != "" {
		t.Errorf("after clone A's init, shared repo_id = %q, want empty: proxied init must not write a per-clone fingerprint into the shared database", got)
	}
	if got := readMetadataValue(t, port, database, "clone_id"); got != "" {
		t.Errorf("after clone A's init, shared clone_id = %q, want empty", got)
	}

	cloneB := bdProxiedInit(t, bd, prefix,
		"--proxied-server-external-host", "127.0.0.1",
		"--proxied-server-external-port", port,
	)
	if got := readMetadataValue(t, port, database, "repo_id"); got != "" {
		t.Errorf("after clone B's init (an ADOPT, not a bootstrap), shared repo_id = %q, want still empty", got)
	}
	if got := readMetadataValue(t, port, database, "clone_id"); got != "" {
		t.Errorf("after clone B's init, shared clone_id = %q, want still empty", got)
	}

	// Both clones share the one database; nothing above should have failed
	// either init, and both local directories should exist and be distinct.
	if cloneA.dir == cloneB.dir {
		t.Fatalf("test setup: both clones resolved to the same local directory %q", cloneA.dir)
	}
}
