//go:build cgo

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// TestDoltDropDatabaseDirectServer is AC-7 of GH#6099 on a direct Dolt
// server: `bd dolt drop-database <name> --yes --json` drops only that
// database, reports the documented JSON, and leaves it recoverable with
// DOLT_UNDROP (no purge ran).
func TestDoltDropDatabaseDirectServer(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("no test Dolt server running")
	}

	binPath := buildBDUnderTest(t)
	repoDir := t.TempDir()
	writeServerRepo(t, repoDir, "drop_named_ws_db", "127.0.0.1", "drop-named-test-origin", testDoltServerPort)

	dsn := doltutil.ServerDSN{Host: "127.0.0.1", Port: testDoltServerPort, User: "root"}.String()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("connect to test dolt server: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()

	stamp := time.Now().UnixNano()
	target := fmt.Sprintf("dropnamed_target_%d", stamp)
	bystander := fmt.Sprintf("dropnamed_keep_%d", stamp)
	for _, name := range []string{target, bystander} {
		mustExec(t, ctx, db, fmt.Sprintf("CREATE DATABASE `%s`", name))
	}
	t.Cleanup(func() {
		for _, name := range []string{target, bystander} {
			_, _ = db.ExecContext(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", name))
		}
	})

	out := string(runBDCommand(t, binPath, repoDir, nil, "dolt", "drop-database", target, "--yes", "--json"))
	var got dropDatabaseResult
	if i := strings.Index(out, "{"); i < 0 || json.Unmarshal([]byte(out[i:]), &got) != nil {
		t.Fatalf("drop-database --json output is not the result object:\n%s", out)
	}
	if got != (dropDatabaseResult{Dropped: target, RecoverableViaUndrop: true}) {
		t.Fatalf("result = %+v, want dropped=%s recoverable_via_undrop=true", got, target)
	}

	exists := func(name string) bool {
		var n int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = ?", name).Scan(&n); err != nil {
			t.Fatalf("schemata lookup: %v", err)
		}
		return n == 1
	}
	if exists(target) {
		t.Fatalf("%s still exists after drop-database", target)
	}
	if !exists(bystander) {
		t.Fatalf("drop-database %s also removed %s", target, bystander)
	}
	undropCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := db.ExecContext(undropCtx, fmt.Sprintf("CALL DOLT_UNDROP('%s')", target)); err != nil {
		t.Fatalf("DOLT_UNDROP(%q) failed: the drop was purged or never recoverable: %v", target, err)
	}
}
