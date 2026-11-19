//go:build integration && !windows

package doltserver

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/testutil/integration"
)

// startRealIdleTestServer starts a real dolt sql-server for an idle-watch
// test and returns its state.
func startRealIdleTestServer(t *testing.T) (string, *State) {
	t.Helper()
	doltBin := integration.RequireDolt(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DOLT_ROOT_PATH", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "0")
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	for _, args := range [][]string{
		{"config", "--global", "--add", "user.name", "beads-test"},
		{"config", "--global", "--add", "user.email", "beads@test"},
	} {
		if out, err := exec.Command(doltBin, args...).CombinedOutput(); err != nil {
			t.Fatalf("dolt %v: %v\n%s", args, err, out)
		}
	}
	beadsDir := filepath.Join(home, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(doltBin, "init")
	cmd.Dir = doltDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dolt init: %v\n%s", err, out)
	}
	state, err := Start(beadsDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = Stop(beadsDir) })
	return beadsDir, state
}

// TestIdleWatch_RealServerActivity reads a real server's counters: the
// watcher's own polls are not activity, while a client that connects and
// leaves between two polls, or one that holds a connection, is.
func TestIdleWatch_RealServerActivity(t *testing.T) {
	_, state := startRealIdleTestServer(t)
	ctx := context.Background()
	t0 := time.Now()
	tr := newIdleTracker(time.Hour, t0)
	poll := func(step int) bool {
		t.Helper()
		st, err := readServerStatus(ctx, "127.0.0.1", state.Port)
		if err != nil {
			t.Fatalf("readServerStatus: %v", err)
		}
		return tr.observe(t0.Add(time.Duration(step)*time.Minute), idleSample{status: st})
	}
	poll(0) // baseline
	if poll(1) {
		t.Fatal("a poll with no client counted as activity")
	}

	client, err := sql.Open("mysql", doltutil.ServerDSN{Host: "127.0.0.1", Port: state.Port, User: "root"}.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := client.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	if !poll(2) {
		t.Error("a client that connected and left between polls was not seen")
	}
	if poll(3) {
		t.Error("a quiet poll after the client left counted as activity")
	}

	held, err := sql.Open("mysql", doltutil.ServerDSN{Host: "127.0.0.1", Port: state.Port, User: "root"}.String())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	conn, err := held.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	poll(4) // the connection's own accept
	if !poll(5) {
		t.Error("a held connection was not seen as activity")
	}
}
