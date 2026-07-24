package dolt

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// preLeaseMoveVersion is the last schema version where leases were columns on
// the issues table (0054 added them; 0055 moved them to the ephemeral leases
// table, bd-lrgn1).
const preLeaseMoveVersion = 54

// TestMigration0055MovesLiveLeases is acceptance criterion (3) of bd-lrgn1:
// upgrading a workspace from the column schema moves live lease values into
// the leases table (so in-flight claims stay reclaimable across the upgrade)
// and drops the issues lease columns. row_lock stays — it is the general
// write-serialization cell, not a lease column.
func TestMigration0055MovesLiveLeases(t *testing.T) {
	skipIfNoDolt(t)
	acquireTestSlot()
	t.Cleanup(releaseTestSlot)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)

	dbName := uniqueTestDBName(t)
	admin, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testServerPort, User: "root", Timeout: 10 * time.Second,
	}.String())
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	t.Cleanup(func() { admin.Close() })
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+dbName+"`"); err != nil {
		t.Fatalf("create database: %v", err)
	}

	db, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testServerPort, User: "root", Database: dbName, Timeout: 10 * time.Second,
	}.String())
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin connection: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}

	if _, err := schema.MigrateUpTo(ctx, conn, preLeaseMoveVersion); err != nil {
		t.Fatalf("migrate to %04d: %v", preLeaseMoveVersion, err)
	}

	// Seed the column-schema claim states an upgrading workspace can hold:
	// a live claim (future lease), a dead worker's claim (expired lease),
	// and an idle open issue (no lease).
	liveExpiry := time.Now().UTC().Add(45 * time.Minute).Truncate(time.Second)
	liveHeartbeat := time.Now().UTC().Truncate(time.Second)
	deadExpiry := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	exec("INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, assignee, lease_expires_at, heartbeat_at, row_lock) "+
		"VALUES ('mig-live', 'live claim', '', '', '', '', 'in_progress', 2, 'task', 'alice', ?, ?, 42)",
		liveExpiry, liveHeartbeat)
	exec("INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, assignee, lease_expires_at, heartbeat_at, row_lock) "+
		"VALUES ('mig-dead', 'dead claim', '', '', '', '', 'in_progress', 2, 'task', 'ghost', ?, ?, 43)",
		deadExpiry, deadExpiry.Add(-5*time.Minute))
	exec("INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) " +
		"VALUES ('mig-idle', 'idle', '', '', '', '', 'open', 2, 'task')")
	exec("CALL DOLT_COMMIT('-Am', 'column-schema workspace with live leases')")

	// The production upgrade.
	if _, err := schema.MigrateUp(ctx, conn); err != nil {
		t.Fatalf("MigrateUp to HEAD: %v", err)
	}

	// The issues lease columns are gone; row_lock survives.
	var colCount int
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'issues' AND COLUMN_NAME IN ('lease_expires_at', 'heartbeat_at')",
	).Scan(&colCount); err != nil {
		t.Fatalf("check dropped columns: %v", err)
	}
	if colCount != 0 {
		t.Errorf("issues still carries %d lease column(s), want 0 after 0055", colCount)
	}
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'issues' AND COLUMN_NAME = 'row_lock'",
	).Scan(&colCount); err != nil {
		t.Fatalf("check row_lock survives: %v", err)
	}
	if colCount != 1 {
		t.Errorf("issues.row_lock missing after 0055 — it must stay (general write-serialization cell)")
	}

	// Both claims' lease values moved into the leases table intact.
	type leaseRow struct {
		holder    string
		expires   time.Time
		heartbeat time.Time
	}
	readLease := func(id string) (leaseRow, bool) {
		var lr leaseRow
		err := conn.QueryRowContext(ctx,
			"SELECT holder, lease_expires_at, heartbeat_at FROM leases WHERE issue_id = ?", id,
		).Scan(&lr.holder, &lr.expires, &lr.heartbeat)
		if err == sql.ErrNoRows {
			return lr, false
		}
		if err != nil {
			t.Fatalf("read lease row %s: %v", id, err)
		}
		return lr, true
	}

	live, ok := readLease("mig-live")
	if !ok {
		t.Fatal("live claim's lease was not moved into the leases table")
	}
	if live.holder != "alice" || !live.expires.Equal(liveExpiry) {
		t.Errorf("live lease moved wrong: holder=%q expires=%v, want alice/%v", live.holder, live.expires, liveExpiry)
	}
	dead, ok := readLease("mig-dead")
	if !ok {
		t.Fatal("expired claim's lease was not moved — it must stay reclaimable across the upgrade")
	}
	if dead.holder != "ghost" || !dead.expires.Equal(deadExpiry) {
		t.Errorf("dead lease moved wrong: holder=%q expires=%v, want ghost/%v", dead.holder, dead.expires, deadExpiry)
	}
	if _, ok := readLease("mig-idle"); ok {
		t.Error("idle issue grew a lease row during migration")
	}

	// The dead worker's claim is reclaimable through the normal path.
	reclaimed, err := issueops.ReclaimExpiredLeasesInTx(ctx, conn, time.Now().UTC(), "reaper")
	if err != nil {
		t.Fatalf("reclaim after migration: %v", err)
	}
	if len(reclaimed) != 1 || reclaimed[0].ID != "mig-dead" || reclaimed[0].PreviousOwner != "ghost" {
		t.Fatalf("reclaimed = %+v, want [{mig-dead ghost}]", reclaimed)
	}
	var status string
	if err := conn.QueryRowContext(ctx, "SELECT status FROM issues WHERE id = 'mig-dead'").Scan(&status); err != nil {
		t.Fatalf("read reclaimed status: %v", err)
	}
	if status != "open" {
		t.Errorf("reclaimed issue status = %q, want open", status)
	}
	if _, still := readLease("mig-dead"); still {
		t.Error("reclaim left the dead lease row behind")
	}
	if _, ok := readLease("mig-live"); !ok {
		t.Error("reclaim touched the live lease")
	}
}
