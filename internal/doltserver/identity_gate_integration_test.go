//go:build integration && !windows

package doltserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/testutil/integration"
)

// Server identity gate, matching path (GH#4637, AC-5): a managed endpoint
// with a matching valid record must open normally — and the normal open path
// must stay free of listener/process lookups. The lookup seam exists for the
// failure path's diagnostics only; this test fails if anyone lets one leak
// onto the matching path.

// identityWorkspace boots bd's own managed server on a fresh workspace, the
// way Start's pairing path does, and returns the beads dir and the port.
func identityWorkspace(t *testing.T) (string, int) {
	t.Helper()
	doltBin := integration.RequireDolt(t)

	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("USERPROFILE", tmpDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, ".config"))
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "0")
	t.Setenv("BEADS_DOLT_AUTO_START", "1")
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)

	for _, args := range [][]string{
		{"config", "--global", "--add", "user.name", "beads-test"},
		{"config", "--global", "--add", "user.email", "beads@test"},
	} {
		cmd := exec.Command(doltBin, args...)
		cmd.Env = append(os.Environ(), "HOME="+tmpDir, "DOLT_ROOT_PATH="+tmpDir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("dolt %v failed: %v\n%s", args, err, out)
		}
	}

	beadsDir := filepath.Join(tmpDir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0o700); err != nil {
		t.Fatalf("failed to create dolt dir: %v", err)
	}
	cmd := exec.Command(doltBin, "init")
	cmd.Dir = doltDir
	cmd.Env = append(os.Environ(), "HOME="+tmpDir, "DOLT_ROOT_PATH="+tmpDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dolt init failed: %v\n%s", err, out)
	}

	state, err := Start(beadsDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = Stop(beadsDir) })
	return beadsDir, state.Port
}

// identityTestConn opens a no-database root connection to the started server.
func identityTestConn(t *testing.T, port int) *sql.DB {
	t.Helper()
	db, err := sql.Open("mysql", doltutil.ServerDSN{Host: "127.0.0.1", Port: port, User: "root"}.String())
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	return db
}

// TestServerIdentityMatchingOpenPerformsNoLookup proves the matching open is
// lookup-free: Start paired the record, the gate verifies against it, and the
// listener/process lookup fires only on the failure path (exactly once, for
// the refusal's best-effort diagnostics).
func TestServerIdentityMatchingOpenPerformsNoLookup(t *testing.T) {
	beadsDir, port := identityWorkspace(t)

	origLookup := serverIdentityListenerPID
	lookups := 0
	serverIdentityListenerPID = func(p int) int {
		lookups++
		return 0
	}
	t.Cleanup(func() { serverIdentityListenerPID = origLookup })

	db := identityTestConn(t, port)
	defer db.Close()

	exp := WorkspaceIdentityExpectation(beadsDir, "127.0.0.1", port)
	if exp == nil {
		t.Fatal("identity verification is unexpectedly disabled for the test workspace")
	}

	// The matching open: the record Start paired names this very server.
	if err := VerifyServerIdentity(context.Background(), db, *exp); err != nil {
		t.Fatalf("the matching record must verify: %v", err)
	}
	if lookups != 0 {
		t.Fatalf("the matching open path must not perform a listener/process lookup; %d lookup(s) ran", lookups)
	}

	// The failure path only: a mismatched record is refused, and its
	// diagnostics perform exactly one lookup.
	recordPath := filepath.Join(ResolveDoltDir(beadsDir), ServerIdentityRecordName)
	paired, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("Start must have paired the identity record: %v", err)
	}
	if err := os.WriteFile(recordPath, []byte("00000000-0000-0000-0000-000000000000"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile(recordPath, paired, 0o600) })

	refusal := VerifyServerIdentity(context.Background(), db, *exp)
	if !errors.Is(refusal, ErrServerIdentityMismatch) {
		t.Fatalf("a mismatched record must be refused, got: %v", refusal)
	}
	if lookups != 1 {
		t.Fatalf("the refusal's diagnostics must perform exactly one listener lookup, got %d", lookups)
	}
	var mismatch *ServerIdentityMismatchError
	if !errors.As(refusal, &mismatch) {
		t.Fatalf("refusal must be a *ServerIdentityMismatchError, got %T", refusal)
	}
	if mismatch.RecordedUUID != "00000000-0000-0000-0000-000000000000" {
		t.Fatalf("refusal must carry the mismatched record's identity, got %q", mismatch.RecordedUUID)
	}
	if fmt.Sprint(mismatch.RecordedPID) == "" || mismatch.RecordedPID <= 0 {
		t.Fatalf("refusal should carry the recorded server PID for diagnostics, got %d", mismatch.RecordedPID)
	}
}
