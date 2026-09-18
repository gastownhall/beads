//go:build cgo && unix

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// TestManagedLocalProxiedBackupRoundTrip is the claim slice S3 makes: a
// managed-local proxied workspace can take a Dolt-native backup and get its
// data back from it.
//
// It is deliberately a ROUND TRIP rather than a pair of exit-code assertions.
// A backup command that reports success without producing a restorable
// artifact is the failure that matters here, and the only way to rule it out is
// to restore the artifact and look at the issues that come back: the issue
// created BEFORE the backup must survive, and the one created AFTER it must be
// gone. Both directions are load-bearing — a restore that changed nothing would
// pass the first check alone.
//
// Named TestManagedLocalProxied* so the proxied-local smoke lane
// (.github/workflows/proxied-local-smoke.yml) runs it; that lane is the only
// one that sets BEADS_TEST_PROXIED_LOCAL.
func TestManagedLocalProxiedBackupRoundTrip(t *testing.T) {
	requireManagedLocalProxiedEnv(t)

	bd := buildEmbeddedBD(t)
	p := bdManagedLocalInit(t, bd, "bkrt", 5*time.Minute)

	before := bdProxiedCreate(t, bd, p.dir, "survives the restore", "-p", "1")

	// Status first: it is the read-only probe that proves the route exists at
	// all, and it must answer with a real measured size rather than the
	// "optional field omitted" the external topologies produce.
	status := readProxiedBackupStatus(t, bd, p)
	if status.Dolt.Configured {
		t.Fatalf("fresh workspace reports a configured backup: %+v", status.Dolt)
	}
	if status.DatabaseSize.Bytes <= 0 {
		t.Fatalf("backup status measured no database: %+v", status.DatabaseSize)
	}

	dest := filepath.Join(t.TempDir(), "dolt-backup")
	if out, err := bdProxiedRun(t, bd, p.dir, "backup", "init", dest); err != nil {
		t.Fatalf("bd backup init %s: %v\n%s", dest, err, out)
	}
	if out, err := bdProxiedRun(t, bd, p.dir, "backup", "sync"); err != nil {
		t.Fatalf("bd backup sync: %v\n%s", err, out)
	}
	// A Dolt backup is a chunk store with a manifest; an empty directory here
	// would mean the command reported a success it did not perform.
	if _, err := os.Stat(filepath.Join(dest, "manifest")); err != nil {
		t.Fatalf("backup sync produced no manifest at %s: %v", dest, err)
	}

	after := bdProxiedCreate(t, bd, p.dir, "must not survive the restore", "-p", "1")

	out, err := bdProxiedRun(t, bd, p.dir, "backup", "restore", dest, "--force", "--json")
	if err != nil {
		t.Fatalf("bd backup restore %s --force: %v\n%s", dest, err, out)
	}
	var result struct {
		Restored bool   `json:"restored"`
		Source   string `json:"source"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("parse restore JSON: %v\n%s", err, out)
	}
	if !result.Restored || result.Source != dest {
		t.Fatalf("unexpected restore result: %+v; want restored=true, source=%s", result, dest)
	}

	// The restore quiesces the topology and leaves it down, so the next command
	// relaunches against the restored data. Assert that rather than assume it:
	// a restore that ran with the old server still up would be the undefined
	// case this design exists to avoid.
	for _, name := range []string{proxy.PIDFileName, server.PIDFileName} {
		if _, err := os.Stat(filepath.Join(p.proxyRoot, name)); err == nil {
			t.Fatalf("restore left %s in place; the topology should be quiescent afterwards", name)
		}
	}

	issues := bdProxiedListJSON(t, bd, p)
	var ids []string
	for _, issue := range issues {
		ids = append(ids, issue.ID)
	}
	if !slices.Contains(ids, before.ID) {
		t.Fatalf("restore lost the issue the backup contained (%s); got %v", before.ID, ids)
	}
	if slices.Contains(ids, after.ID) {
		t.Fatalf("restore did not replace the database: %s was created after the backup and is still here; got %v",
			after.ID, ids)
	}

	// The restore registers its source as the backup destination, so a plain
	// `bd backup sync` works straight afterwards with no second init.
	restored := readProxiedBackupStatus(t, bd, p)
	if !restored.Dolt.Configured || !strings.HasSuffix(restored.Dolt.BackupURL, dest) {
		t.Fatalf("restore did not register %s as the backup destination: %+v", dest, restored.Dolt)
	}
	if out, err := bdProxiedRun(t, bd, p.dir, "backup", "sync"); err != nil {
		t.Fatalf("bd backup sync after restore: %v\n%s", err, out)
	}

	if out, err := bdProxiedRun(t, bd, p.dir, "backup", "remove"); err != nil {
		t.Fatalf("bd backup remove: %v\n%s", err, out)
	}
	removed := readProxiedBackupStatus(t, bd, p)
	if removed.Dolt.Configured {
		t.Fatalf("backup remove left the destination configured: %+v", removed.Dolt)
	}
	// Removing the destination unregisters the remote; it does not delete the
	// backup, which is what makes `bd backup remove` safe to run.
	if _, err := os.Stat(filepath.Join(dest, "manifest")); err != nil {
		t.Fatalf("backup remove deleted the backup data at %s: %v", dest, err)
	}
	if out, err := bdProxiedRun(t, bd, p.dir, "backup", "sync"); err == nil {
		t.Fatalf("bd backup sync succeeded with no destination configured:\n%s", out)
	}
}

// A backup with a malformed migration table can replace the database but fail
// the following provider construction at schema initialization. That failure must
// still leave both processes down, even though no provider was returned.
func TestManagedLocalProxiedBackupRestoreReopenFailure(t *testing.T) {
	requireManagedLocalProxiedEnv(t)
	bd := buildEmbeddedBD(t)
	p := bdManagedLocalInit(t, bd, "bkrf", 5*time.Minute)
	dest := filepath.Join(t.TempDir(), "malformed-backup")
	if out, err := bdProxiedRun(t, bd, p.dir, "backup", "init", dest); err != nil {
		t.Fatalf("backup init: %v\n%s", err, out)
	}

	// Damage the migration table in the backup, then repair the live table so
	// the command's initial provider can open normally. Only its post-restore
	// constructor will see the malformed table and fail after starting Dolt.
	held := openHeldManagedProxiedConn(t, bd, p)
	ctx := context.Background()
	if _, err := held.Conn.ExecContext(ctx, "ALTER TABLE schema_migrations RENAME COLUMN version TO broken_version"); err != nil {
		t.Fatalf("damage migration table: %v", err)
	}
	if _, err := held.Conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'Simulate a malformed migration table in the backup')"); err != nil {
		t.Fatalf("commit malformed migration table: %v", err)
	}
	if err := versioncontrolops.BackupSync(ctx, held.Conn, proxiedBackupTargetName); err != nil {
		t.Fatalf("back up malformed schema: %v", err)
	}
	if _, err := held.Conn.ExecContext(ctx, "ALTER TABLE schema_migrations RENAME COLUMN broken_version TO version"); err != nil {
		t.Fatalf("repair live migration table: %v", err)
	}
	held.Release()

	out, err := bdProxiedRun(t, bd, p.dir, "backup", "restore", dest, "--force")
	if err == nil {
		t.Fatalf("restore unexpectedly reopened a malformed schema:\n%s", out)
	}
	if !strings.Contains(string(out), "the restored database did not reopen") || !strings.Contains(string(out), "schema") {
		t.Fatalf("expected post-restore schema initialization failure, got:\n%s", out)
	}
	if strings.Contains(string(out), "restored and reconciled") {
		t.Fatalf("failed reopen reported successful reconciliation:\n%s", out)
	}
	for _, name := range []string{proxy.PIDFileName, server.PIDFileName} {
		if _, err := os.Stat(filepath.Join(p.proxyRoot, name)); !os.IsNotExist(err) {
			t.Errorf("failed reopen should remove %s; stat error: %v", name, err)
		}
	}
}

type proxiedBackupStatus struct {
	Dolt struct {
		Configured bool   `json:"configured"`
		BackupURL  string `json:"backup_url"`
		BackupName string `json:"backup_name"`
	} `json:"dolt"`
	DatabaseSize struct {
		Bytes int64 `json:"bytes"`
	} `json:"database_size"`
}

func readProxiedBackupStatus(t *testing.T, bd string, p proxiedProject) proxiedBackupStatus {
	t.Helper()
	out, err := bdProxiedRun(t, bd, p.dir, "backup", "status", "--json")
	if err != nil {
		t.Fatalf("bd backup status --json: %v\n%s", err, out)
	}
	start := strings.Index(string(out), "{")
	if start < 0 {
		t.Fatalf("bd backup status --json produced no JSON:\n%s", out)
	}
	var status proxiedBackupStatus
	if err := json.Unmarshal(out[start:], &status); err != nil {
		t.Fatalf("parse backup status JSON: %v\n%s", err, out)
	}
	return status
}
