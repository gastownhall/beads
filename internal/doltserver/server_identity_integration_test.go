//go:build integration && !windows

package doltserver_test

import (
	"context"
	"os"
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
