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

// Server identity gate (GH#4637): a managed endpoint whose answering server
// is not the one bd recorded for its data root is refused before the open
// lists, creates or reads any database there.

// serverIdentityShared puts the test in shared-server mode with a private
// shared directory and returns a workspace .beads directory and the shared
// data root, whose identity record the shared endpoint is checked against.
func serverIdentityShared(t *testing.T) (beadsDir, dataDir string) {
	t.Helper()
	beadsDir = filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_SHARED_SERVER_DIR", t.TempDir())
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	t.Setenv(doltserver.VerifyServerIdentityEnv, "")
	dataDir, err := doltserver.SharedDoltPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return beadsDir, dataDir
}

// sharedIdentityConfig is a store config for the shared endpoint at the test
// server's port.
func sharedIdentityConfig(t *testing.T, beadsDir, dbName string) *Config {
	return &Config{
		Path: t.TempDir(), BeadsDir: beadsDir, ServerHost: "127.0.0.1", ServerPort: testServerPort,
		ServerPortSharedServer: true, Database: dbName, MaxOpenConns: 1,
	}
}

// testServerUUID is the test server's @@server_uuid.
func testServerUUID(t *testing.T) string {
	t.Helper()
	db := rawTestConn(t, testServerPort)
	defer db.Close()
	uuid, err := doltserver.QueryServerUUID(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	return uuid
}

func writeIdentityRecord(t *testing.T, dataDir, content string) string {
	t.Helper()
	path := doltserver.ServerIdentityRecordPath(dataDir)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// seedSharedDatabase creates dbName with a bd schema, with the gate turned
// off, so a later open has an existing project database to refuse.
func seedSharedDatabase(t *testing.T, beadsDir, dbName string) {
	t.Helper()
	t.Setenv(doltserver.VerifyServerIdentityEnv, "false")
	cfg := sharedIdentityConfig(t, beadsDir, dbName)
	cfg.CreateIfMissing = true
	seed, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("seed %s: %v", dbName, err)
	}
	_ = seed.Close()
	t.Setenv(doltserver.VerifyServerIdentityEnv, "")
}

const foreignServerUUID = "00000000-1111-4222-8333-444444444444"

// TestServerIdentityForeignSharedEndpoint: the shared endpoint's record names
// another server than the one answering. Creating a database there, and
// reading or writing an existing one, is refused with the identity error
// naming both UUIDs, nothing is created, and the record is left alone.
func TestServerIdentityForeignSharedEndpoint(t *testing.T) {
	skipIfNoServer(t)
	ctx := context.Background()
	actual := testServerUUID(t)

	t.Run("create", func(t *testing.T) {
		beadsDir, dataDir := serverIdentityShared(t)
		record := writeIdentityRecord(t, dataDir, foreignServerUUID+"\n")
		dbName := fmt.Sprintf("test_identity_create_%d", testServerPort)
		cfg := sharedIdentityConfig(t, beadsDir, dbName)
		cfg.CreateIfMissing = true

		store, err := New(ctx, cfg)
		if err == nil {
			_ = store.Close()
			t.Fatalf("open of a foreign shared endpoint succeeded")
		}
		if !errors.Is(err, doltserver.ErrServerIdentityMismatch) {
			t.Fatalf("open error = %v, want ErrServerIdentityMismatch", err)
		}
		for _, want := range []string{foreignServerUUID, actual, record} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("open error does not name %q:\n%v", want, err)
			}
		}
		assertDatabaseNotExists(t, testServerPort, dbName)
		if got, _ := os.ReadFile(record); string(got) != foreignServerUUID+"\n" {
			t.Errorf("record = %q, want it unchanged", got)
		}
	})

	for _, readOnly := range []bool{true, false} {
		t.Run(fmt.Sprintf("existing/readOnly=%v", readOnly), func(t *testing.T) {
			beadsDir, dataDir := serverIdentityShared(t)
			dbName := fmt.Sprintf("test_identity_existing_%v_%d", readOnly, testServerPort)
			seedSharedDatabase(t, beadsDir, dbName)
			record := writeIdentityRecord(t, dataDir, foreignServerUUID+"\n")

			cfg := sharedIdentityConfig(t, beadsDir, dbName)
			cfg.ReadOnly = readOnly
			store, err := New(ctx, cfg)
			if err == nil {
				_ = store.Close()
				t.Fatalf("open of an existing database on a foreign shared endpoint succeeded")
			}
			if !errors.Is(err, doltserver.ErrServerIdentityMismatch) {
				t.Fatalf("open error = %v, want ErrServerIdentityMismatch", err)
			}
			if !strings.Contains(err.Error(), actual) || !strings.Contains(err.Error(), foreignServerUUID) {
				t.Errorf("open error does not name both UUIDs:\n%v", err)
			}
			if got, _ := os.ReadFile(record); string(got) != foreignServerUUID+"\n" {
				t.Errorf("record = %q, want it unchanged", got)
			}
		})
	}
}

// TestServerIdentityUnusableRecord: a server already listens on the shared
// endpoint but the record that should name it is absent, blank, malformed or
// unreadable. The open is refused with an error that says which, and neither
// a record nor the database is created.
func TestServerIdentityUnusableRecord(t *testing.T) {
	skipIfNoServer(t)
	ctx := context.Background()
	actual := testServerUUID(t)

	cases := []struct {
		name    string
		prepare func(t *testing.T, record string)
		problem string
	}{
		{"absent", func(t *testing.T, record string) {}, "does not exist"},
		{"blank", func(t *testing.T, record string) { writeFile(t, record, " \n") }, "is empty"},
		{"malformed", func(t *testing.T, record string) { writeFile(t, record, "not-a-uuid\n") }, "is malformed"},
		{"unreadable", func(t *testing.T, record string) {
			if err := os.Mkdir(record, 0o755); err != nil {
				t.Fatal(err)
			}
		}, "could not be read"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir, dataDir := serverIdentityShared(t)
			record := doltserver.ServerIdentityRecordPath(dataDir)
			tc.prepare(t, record)
			before, beforeErr := os.Lstat(record)
			var beforeContent []byte
			if beforeErr == nil && before.Mode().IsRegular() {
				beforeContent, _ = os.ReadFile(record)
			}

			dbName := fmt.Sprintf("test_identity_record_%d_%d", i, testServerPort)
			cfg := sharedIdentityConfig(t, beadsDir, dbName)
			cfg.CreateIfMissing = true
			store, err := New(ctx, cfg)
			if err == nil {
				_ = store.Close()
				t.Fatalf("open with a %s identity record succeeded", tc.name)
			}
			if !errors.Is(err, doltserver.ErrServerIdentityMismatch) {
				t.Fatalf("open error = %v, want ErrServerIdentityMismatch", err)
			}
			for _, want := range []string{tc.problem, record, actual, "bd dolt stop && bd dolt start"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("open error does not name %q:\n%v", want, err)
				}
			}
			assertDatabaseNotExists(t, testServerPort, dbName)

			after, afterErr := os.Lstat(record)
			switch {
			case beforeErr != nil:
				if afterErr == nil {
					t.Errorf("open created the identity record %s", record)
				}
			case afterErr != nil || after.Mode() != before.Mode():
				t.Errorf("open replaced the identity record %s", record)
			case before.Mode().IsRegular():
				if got, _ := os.ReadFile(record); string(got) != string(beforeContent) {
					t.Errorf("record = %q, want it unchanged (%q)", got, beforeContent)
				}
			}
		})
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
