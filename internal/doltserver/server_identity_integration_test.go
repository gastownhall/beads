//go:build integration && !windows

package doltserver_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/testutil/integration"
)

// TestServerIdentity_LifecyclePairsRealServer: a real dolt sql-server that
// Start launches is paired with its data directory, its UUID recorded there;
// the server passes the identity check, and so does the server a normal stop
// and start brings up next: the record survives the stop and the restarted
// server reports the same UUID.
func TestServerIdentity_LifecyclePairsRealServer(t *testing.T) {
	beadsDir := setupLifecycleTestDir(t)
	t.Setenv(doltserver.VerifyServerIdentityEnv, "")
	reg := integration.NewProcessRegistry(t)
	dataDir := doltserver.ResolveDoltDir(beadsDir)
	recordPath := doltserver.ServerIdentityRecordPath(dataDir)

	var firstUUID string
	for round := 1; round <= 2; round++ {
		state, err := doltserver.Start(beadsDir)
		if err != nil {
			t.Fatalf("Start (round %d): %v", round, err)
		}
		if p, err := os.FindProcess(state.PID); err == nil {
			reg.Register(p)
		}

		db := connectMySQL(t, state.Port)
		actual, err := doltserver.QueryServerUUID(context.Background(), db)
		if err != nil {
			t.Fatalf("server UUID (round %d): %v", round, err)
		}
		if round == 1 {
			firstUUID = actual
		} else if actual != firstUUID {
			t.Errorf("round %d: server UUID = %q, want %q unchanged across a normal restart", round, actual, firstUUID)
		}
		record, err := os.ReadFile(recordPath)
		if err != nil {
			t.Fatalf("identity record (round %d): %v", round, err)
		}
		if strings.TrimSpace(string(record)) != actual {
			t.Errorf("round %d: record = %q, want the server's UUID %q", round, record, actual)
		}
		exp := &doltserver.ServerIdentityExpectation{DataDir: dataDir, ServerDir: beadsDir, Host: "127.0.0.1", Port: state.Port}
		if err := doltserver.VerifyServerIdentity(context.Background(), db, exp); err != nil {
			t.Errorf("round %d: identity check against the server Start paired: %v", round, err)
		}
		_ = db.Close()

		if err := doltserver.Stop(beadsDir); err != nil {
			t.Fatalf("Stop (round %d): %v", round, err)
		}
		reg.Deregister(state.PID)

		after, err := os.ReadFile(recordPath)
		if err != nil {
			t.Fatalf("identity record after Stop (round %d): %v", round, err)
		}
		if strings.TrimSpace(string(after)) != actual {
			t.Errorf("round %d: record after Stop = %q, want %q", round, after, actual)
		}
	}
}

// TestServerIdentity_CheckOffStartKeepsTheRecordedUUID: a real dolt
// sql-server that Start launches with the check turned off, after one it
// launched with the check on, reports the UUID recorded for its data
// directory, not dolt's own, so a later open with the check on accepts it,
// and the record is unchanged.
func TestServerIdentity_CheckOffStartKeepsTheRecordedUUID(t *testing.T) {
	beadsDir := setupLifecycleTestDir(t)
	reg := integration.NewProcessRegistry(t)
	dataDir := doltserver.ResolveDoltDir(beadsDir)
	recordPath := doltserver.ServerIdentityRecordPath(dataDir)

	start := func(check string) *doltserver.State {
		t.Helper()
		t.Setenv(doltserver.VerifyServerIdentityEnv, check)
		state, err := doltserver.Start(beadsDir)
		if err != nil {
			t.Fatalf("Start with %s=%q: %v", doltserver.VerifyServerIdentityEnv, check, err)
		}
		if p, err := os.FindProcess(state.PID); err == nil {
			reg.Register(p)
		}
		return state
	}

	checked := start("")
	recorded, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("identity record after the start with the check on: %v", err)
	}
	if err := doltserver.Stop(beadsDir); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	reg.Deregister(checked.PID)

	unchecked := start("false")
	t.Cleanup(func() {
		_ = doltserver.Stop(beadsDir)
		reg.Deregister(unchecked.PID)
	})
	t.Setenv(doltserver.VerifyServerIdentityEnv, "")
	db := connectMySQL(t, unchecked.Port)
	defer db.Close()
	exp := &doltserver.ServerIdentityExpectation{DataDir: dataDir, ServerDir: beadsDir, Host: "127.0.0.1", Port: unchecked.Port}
	if err := doltserver.VerifyServerIdentity(context.Background(), db, exp); err != nil {
		t.Errorf("a checked open of the server Start launched with the check off: %v", err)
	}
	if after, err := os.ReadFile(recordPath); err != nil || string(after) != string(recorded) {
		t.Errorf("identity record after the start with the check off = (%q, %v), want %q unchanged", after, err, recorded)
	}
}

// TestServerIdentity_PerDataRootDespiteAGlobalServerUUID: on a host whose
// global Dolt config persists a server_uuid, which every dolt sql-server
// there reports unless its config sets another, the servers Start launches
// for two data directories still report two UUIDs, neither of them the
// global one, each the one recorded in its own data directory; the identity
// check tells them apart, and a normal restart keeps each.
func TestServerIdentity_PerDataRootDespiteAGlobalServerUUID(t *testing.T) {
	doltBin := integration.RequireDolt(t)
	first := setupLifecycleTestDir(t)
	home := filepath.Dir(first)
	t.Setenv("DOLT_ROOT_PATH", home)
	t.Setenv(doltserver.VerifyServerIdentityEnv, "")
	const global = "11111111-2222-4333-8444-555555555555"
	cmd := exec.Command(doltBin, "config", "--global", "--add", "sqlserver.global.server_uuid", global)
	cmd.Env = doltTestEnv(home)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("persisting a global server_uuid: %v\n%s", err, out)
	}
	second := filepath.Join(home, "second", ".beads")
	if err := os.MkdirAll(second, 0o700); err != nil {
		t.Fatal(err)
	}
	reg := integration.NewProcessRegistry(t)

	start := func(beadsDir string) (string, int) {
		t.Helper()
		state, err := doltserver.Start(beadsDir)
		if err != nil {
			t.Fatalf("Start %s: %v", beadsDir, err)
		}
		if p, err := os.FindProcess(state.PID); err == nil {
			reg.Register(p)
		}
		t.Cleanup(func() {
			_ = doltserver.Stop(beadsDir)
			reg.Deregister(state.PID)
		})
		db := connectMySQL(t, state.Port)
		defer db.Close()
		actual, err := doltserver.QueryServerUUID(context.Background(), db)
		if err != nil {
			t.Fatalf("server UUID of %s: %v", beadsDir, err)
		}
		record, err := os.ReadFile(doltserver.ServerIdentityRecordPath(doltserver.ResolveDoltDir(beadsDir)))
		if err != nil {
			t.Fatalf("identity record of %s: %v", beadsDir, err)
		}
		if strings.TrimSpace(string(record)) != actual {
			t.Errorf("%s: record = %q, want the server's UUID %q", beadsDir, record, actual)
		}
		return actual, state.Port
	}

	firstUUID, firstPort := start(first)
	secondUUID, _ := start(second)
	if firstUUID == global || secondUUID == global {
		t.Fatalf("server UUIDs %q and %q; the global server_uuid %q names every server on the host", firstUUID, secondUUID, global)
	}
	if firstUUID == secondUUID {
		t.Fatalf("both data directories' servers report server UUID %q", firstUUID)
	}

	// The second data directory's record does not name the first server.
	db := connectMySQL(t, firstPort)
	exp := &doltserver.ServerIdentityExpectation{
		DataDir: doltserver.ResolveDoltDir(second), ServerDir: second, Host: "127.0.0.1", Port: firstPort,
	}
	err := doltserver.VerifyServerIdentity(context.Background(), db, exp)
	_ = db.Close()
	if err == nil || !strings.Contains(err.Error(), secondUUID) || !strings.Contains(err.Error(), firstUUID) {
		t.Errorf("checking the first server against the second data directory = %v, want a refusal naming both UUIDs", err)
	}

	if err := doltserver.Stop(first); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if again, _ := start(first); again != firstUUID {
		t.Errorf("after a restart the server reports %q, want %q kept", again, firstUUID)
	}
}
