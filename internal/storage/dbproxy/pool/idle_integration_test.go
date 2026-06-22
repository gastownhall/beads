package pool_test

import (
	"testing"
	"time"
)

// TestPool_KeepaliveSurvivesIdleReaping verifies that a backend left idle past
// Dolt's ~40s loopback idle-reaping window is still usable, because the
// keepalive pinger (and the borrow-time liveness ping with lazy reconnect)
// keeps it alive / heals it. This test takes ~45s, so it is gated behind
// BEADS_POOL_TEST_DOLT_ADDR like the others and skipped in -short mode.
func TestPool_KeepaliveSurvivesIdleReaping(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping long idle-reaping test in -short mode")
	}
	addr := doltAddr(t)
	dbName := makeTestDB(t, addr)
	srv, stats := startServer(t, addr, 2)
	db := pooledDB(t, srv.Socket(), dbName, 2)

	var one int
	if err := db.QueryRow("SELECT 1").Scan(&one); err != nil {
		t.Fatalf("initial query: %v", err)
	}
	// Idle well past the ~40s reaping window. Keepalive (1s in tests) must keep
	// backends alive across this gap.
	time.Sleep(45 * time.Second)

	if err := db.QueryRow("SELECT 1").Scan(&one); err != nil {
		t.Fatalf("query after idle: %v", err)
	}
	if one != 1 {
		t.Fatalf("got %d want 1", one)
	}
	if rc := stats.Snapshot().Reconnects; rc > 1 {
		t.Fatalf("too many reconnects (%d); keepalive should have prevented reaping", rc)
	}
	t.Logf("idle survival OK; reconnects=%d", stats.Snapshot().Reconnects)
}
