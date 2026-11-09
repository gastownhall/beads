//go:build integration && !windows

package doltserver_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
)

// Server identity pairing lifecycle (GH#4637): a launch bd proves it owns
// must durably record the child's @@server_uuid, so the gate the store's open
// runs has a record to verify against — and a normal stop/start cycle must
// keep the workspace verifiable, since the server's identity is stable across
// restarts of the same data directory.

// serverUUIDOnPort reads @@server_uuid from the server listening on port.
func serverUUIDOnPort(t *testing.T, port int) string {
	t.Helper()
	db := connectMySQL(t, port)
	defer db.Close()
	var uuid string
	if err := db.QueryRow("SELECT @@server_uuid").Scan(&uuid); err != nil {
		t.Fatalf("failed to read the server's @@server_uuid: %v", err)
	}
	return strings.TrimSpace(uuid)
}

// identityRecordFor reads the paired identity record for beadsDir's data root.
func identityRecordFor(t *testing.T, beadsDir string) string {
	t.Helper()
	recordPath := filepath.Join(doltserver.ResolveDoltDir(beadsDir), doltserver.ServerIdentityRecordName)
	data, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("failed to read the identity record at %s: %v", recordPath, err)
	}
	return strings.TrimSpace(string(data))
}

// verifyPaired checks the gate against the live server on port with the
// record beadsDir's data root carries.
func verifyPaired(t *testing.T, beadsDir string, port int) {
	t.Helper()
	db := connectMySQL(t, port)
	defer db.Close()
	exp := doltserver.WorkspaceIdentityExpectation(beadsDir, "127.0.0.1", port)
	if exp == nil {
		t.Fatal("identity verification is unexpectedly disabled for the test workspace")
	}
	if err := doltserver.VerifyServerIdentity(context.Background(), db, *exp); err != nil {
		t.Fatalf("the paired server's identity must verify: %v", err)
	}
}

// TestServerIdentityPairedOnStart is AC-3's core: a fresh Start pairs the
// identity record with the child it launched, and the paired server verifies
// against it.
func TestServerIdentityPairedOnStart(t *testing.T) {
	beadsDir := setupLifecycleTestDir(t)

	state, err := doltserver.Start(beadsDir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = doltserver.Stop(beadsDir) })

	if identityRecordFor(t, beadsDir) != serverUUIDOnPort(t, state.Port) {
		t.Fatalf("identity record does not match the started server's @@server_uuid: record %q, server %q",
			identityRecordFor(t, beadsDir), serverUUIDOnPort(t, state.Port))
	}
	verifyPaired(t, beadsDir, state.Port)
}

// TestServerIdentityPairedAcrossRestart is AC-3's durability half: a normal
// stop/start cycle keeps the workspace verifiable — the restarted server
// re-pairs (the UUID is stable for the data directory) and verifies.
func TestServerIdentityPairedAcrossRestart(t *testing.T) {
	beadsDir := setupLifecycleTestDir(t)

	first, err := doltserver.Start(beadsDir)
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	firstUUID := serverUUIDOnPort(t, first.Port)

	if err := doltserver.Stop(beadsDir); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	second, err := doltserver.Start(beadsDir)
	if err != nil {
		t.Fatalf("second Start: %v", err)
	}
	t.Cleanup(func() { _ = doltserver.Stop(beadsDir) })

	// The record survived the stop (it lives with the data, not the state
	// files) and still names the same server identity.
	if identityRecordFor(t, beadsDir) != firstUUID {
		t.Fatalf("identity record changed across restart: before %q, after %q", firstUUID, identityRecordFor(t, beadsDir))
	}
	if identityRecordFor(t, beadsDir) != serverUUIDOnPort(t, second.Port) {
		t.Fatalf("identity record does not match the restarted server: record %q, server %q",
			identityRecordFor(t, beadsDir), serverUUIDOnPort(t, second.Port))
	}
	verifyPaired(t, beadsDir, second.Port)
}
