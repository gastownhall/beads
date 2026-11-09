package dolt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
)

// Tests for the shared-server identity gate (GH#4637): a managed shared
// endpoint whose answering server's @@server_uuid does not match the record
// bd keeps for the shared data root must be refused before discovery or
// creation. A foreign dolt server behind the configured host:port — the
// suite's own container standing in for the mirrored-listener / forwarded
// port case — must not silently receive bd's databases, schema or issues.
//
// The suite's container is the perfect stand-in for a foreign listener: it
// IS a live dolt server, reachable, the right version, and not the server bd
// recorded for the isolated shared data root. Seeding a mismatched record
// makes the refusal provable; the record is read-only to the open, so the
// seeded value must survive the refusal byte-for-byte.

// serverUUID reads the answering server's @@server_uuid on port.
func serverUUID(t *testing.T, port int) string {
	t.Helper()
	db := rawTestConn(t, port)
	defer db.Close()
	var uuid string
	if err := db.QueryRow("SELECT @@server_uuid").Scan(&uuid); err != nil {
		t.Fatalf("failed to read the test server's @@server_uuid: %v", err)
	}
	return strings.TrimSpace(uuid)
}

// mustSharedRecordPath resolves the identity record's path for the isolated
// shared data root.
func mustSharedRecordPath(t *testing.T) string {
	t.Helper()
	dataDir, err := doltserver.SharedDoltDir()
	if err != nil {
		t.Fatalf("failed to resolve the shared dolt data dir: %v", err)
	}
	return filepath.Join(dataDir, doltserver.ServerIdentityRecordName)
}

// seedSharedIdentityRecord writes a record for the isolated shared data root
// and returns the exact bytes written.
func seedSharedIdentityRecord(t *testing.T, uuid string) []byte {
	t.Helper()
	recordPath := mustSharedRecordPath(t)
	seeded := []byte(uuid + "\n")
	if err := os.WriteFile(recordPath, seeded, 0o600); err != nil {
		t.Fatalf("failed to seed the identity record: %v", err)
	}
	return seeded
}

// sharedIdentityConfig builds a shared-mode open against the test server: the
// suite's own container, playing the foreign listener behind the shared port.
func sharedIdentityConfig(t *testing.T, dbName string) *Config {
	t.Helper()
	beadsDir := t.TempDir()
	return &Config{
		Database:               dbName,
		Path:                   filepath.Join(beadsDir, "dolt"),
		BeadsDir:               beadsDir,
		ServerHost:             "127.0.0.1",
		ServerPort:             testServerPort,
		ServerPortSharedServer: true,
		CreateIfMissing:        true,
	}
}

// TestServerIdentityForeignSharedEndpoint is the gate's core refusal: an init
// (CreateIfMissing) against a shared endpoint whose answering server is not
// the one recorded for the shared data root must fail before the database is
// created, and the refusal must name both identities.
func TestServerIdentityForeignSharedEndpoint(t *testing.T) {
	skipIfNoServer(t)
	t.Setenv("BEADS_SHARED_SERVER_DIR", t.TempDir())
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")

	seeded := "11111111-2222-3333-4444-555555555555"
	recordBytes := seedSharedIdentityRecord(t, seeded)
	actual := serverUUID(t, testServerPort)
	if actual == seeded {
		t.Fatalf("precondition: the answering server's identity %q must differ from the seeded record", actual)
	}

	dbName := fmt.Sprintf("test_identity_foreign_%d", testServerPort)
	assertDatabaseNotExists(t, testServerPort, dbName)
	t.Cleanup(func() { dropTestDatabase(t, testServerPort, dbName) })

	_, err := New(context.Background(), sharedIdentityConfig(t, dbName))
	if !errors.Is(err, doltserver.ErrServerIdentityMismatch) {
		t.Fatalf("expected a server identity refusal, got: %v", err)
	}
	var mismatch *doltserver.ServerIdentityMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("refusal must be a *ServerIdentityMismatchError, got %T: %v", err, err)
	}
	if mismatch.RecordedUUID != seeded || mismatch.ActualUUID != actual {
		t.Fatalf("refusal must carry both identities: recorded %q (want %q), actual %q (want %q)",
			mismatch.RecordedUUID, seeded, mismatch.ActualUUID, actual)
	}
	if msg := err.Error(); !strings.Contains(msg, seeded) || !strings.Contains(msg, actual) {
		t.Fatalf("refusal message must name the recorded and the answering identities, got:\n%s", msg)
	}
	// The refusal happened before creation: nothing was created on the
	// foreign server.
	assertDatabaseNotExists(t, testServerPort, dbName)

	// A normal open never writes the record: the seeded bytes must survive
	// the refusal untouched.
	got, readErr := os.ReadFile(mustSharedRecordPath(t))
	if readErr != nil {
		t.Fatalf("identity record must still exist after the refusal: %v", readErr)
	}
	if string(got) != string(recordBytes) {
		t.Fatalf("identity record was rewritten by a normal open: got %q, want %q", string(got), string(recordBytes))
	}
}

// TestServerIdentityForeignSharedEndpoint_ExistingDB covers the read side of
// the same gap: a database that already exists on the foreign server must not
// be opened either. The direct-connect fast path succeeds (the database
// exists), which is exactly the silent-use case this gate exists to close.
func TestServerIdentityForeignSharedEndpoint_ExistingDB(t *testing.T) {
	skipIfNoServer(t)
	t.Setenv("BEADS_SHARED_SERVER_DIR", t.TempDir())
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")

	seeded := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	seedSharedIdentityRecord(t, seeded)
	if actual := serverUUID(t, testServerPort); actual == seeded {
		t.Fatalf("precondition: the answering server's identity %q must differ from the seeded record", actual)
	}

	dbName := fmt.Sprintf("test_identity_existing_%d", testServerPort)
	createTestDatabase(t, testServerPort, dbName)
	t.Cleanup(func() { dropTestDatabase(t, testServerPort, dbName) })

	cfg := sharedIdentityConfig(t, dbName)
	cfg.CreateIfMissing = false
	_, err := New(context.Background(), cfg)
	if !errors.Is(err, doltserver.ErrServerIdentityMismatch) {
		t.Fatalf("expected a server identity refusal for an existing foreign database, got: %v", err)
	}

	// The same open against the same endpoint must succeed once the record
	// matches the answering server — the gate refuses substitution, not use.
	if err := os.WriteFile(mustSharedRecordPath(t), []byte(serverUUID(t, testServerPort)), 0o600); err != nil {
		t.Fatalf("failed to align the record with the answering server: %v", err)
	}
	store, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("matching identity must open: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("failed to close the matching store: %v", err)
	}
}
