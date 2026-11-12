package dolt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/doltversion"
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

// TestServerIdentityMatchingRecord: a record that names the answering server
// lets the shared endpoint create, write and read its database.
func TestServerIdentityMatchingRecord(t *testing.T) {
	skipIfNoServer(t)
	ctx := context.Background()
	beadsDir, dataDir := serverIdentityShared(t)
	writeIdentityRecord(t, dataDir, testServerUUID(t)+"\n")
	dbName := fmt.Sprintf("test_identity_match_%d", testServerPort)

	cfg := sharedIdentityConfig(t, beadsDir, dbName)
	cfg.CreateIfMissing = true
	store, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("create on the paired server: %v", err)
	}
	_ = store.Close()
	for _, readOnly := range []bool{false, true} {
		cfg := sharedIdentityConfig(t, beadsDir, dbName)
		cfg.ReadOnly = readOnly
		store, err := New(ctx, cfg)
		if err != nil {
			t.Fatalf("open (readOnly=%v) on the paired server: %v", readOnly, err)
		}
		_ = store.Close()
	}
}

// TestServerIdentityExemptEndpoints: endpoints bd does not launch, whose
// workspace declares that something else runs the server, or whose check the
// operator turned off, get no expectation, so the open makes no record read
// or identity query; an eligible endpoint of the same shape does.
func TestServerIdentityExemptEndpoints(t *testing.T) {
	for _, env := range []string{"BEADS_DOLT_SERVER_MODE", "BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT", "BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_AUTO_START"} {
		t.Setenv(env, "")
	}
	const port = 3308

	shared := func(t *testing.T) (*Config, string) {
		beadsDir, _ := serverIdentityShared(t)
		return &Config{BeadsDir: beadsDir, ServerHost: "127.0.0.1", ServerPort: port, ServerPortSharedServer: true}, beadsDir
	}
	owned := func(t *testing.T) (*Config, string) {
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(beadsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
		t.Setenv(doltserver.VerifyServerIdentityEnv, "")
		return &Config{BeadsDir: beadsDir, ServerHost: "127.0.0.1", ServerPort: port, ServerPortSource: doltserver.PortSourcePortFile}, beadsDir
	}

	for name, setup := range map[string]func(t *testing.T) (*Config, string){"shared": shared, "owned": owned} {
		t.Run("eligible/"+name, func(t *testing.T) {
			cfg, beadsDir := setup(t)
			if serverIdentityExpectation(cfg, beadsDir, beadsDir) == nil {
				t.Fatalf("no expectation for the %s endpoint bd launches", name)
			}
		})
	}
	t.Run("eligible/auto-start on", func(t *testing.T) {
		cfg, beadsDir := shared(t)
		t.Setenv("BEADS_DOLT_AUTO_START", "1")
		writeFile(t, filepath.Join(beadsDir, "config.yaml"), "dolt.auto-start: true\n")
		if serverIdentityExpectation(cfg, beadsDir, beadsDir) == nil {
			t.Fatal("auto-start on exempted the shared endpoint")
		}
	})
	t.Run("eligible/invalid setting keeps the check on", func(t *testing.T) {
		cfg, beadsDir := shared(t)
		t.Setenv(doltserver.VerifyServerIdentityEnv, "maybe")
		if serverIdentityExpectation(cfg, beadsDir, beadsDir) == nil {
			t.Fatalf("%s=maybe turned the check off", doltserver.VerifyServerIdentityEnv)
		}
	})

	cases := []struct {
		name  string
		setup func(t *testing.T) (*Config, string)
		edit  func(t *testing.T, cfg *Config, beadsDir string)
	}{
		{"gateway", shared, func(t *testing.T, cfg *Config, _ string) { cfg.Gateway = true }},
		{"proxied", shared, func(t *testing.T, cfg *Config, _ string) { cfg.ProxiedServer = true }},
		{"socket", shared, func(t *testing.T, cfg *Config, _ string) { cfg.ServerSocket = "/tmp/dolt.sock" }},
		{"tls", shared, func(t *testing.T, cfg *Config, _ string) { cfg.ServerTLS = true }},
		{"non-bd host", shared, func(t *testing.T, cfg *Config, _ string) { cfg.ServerHost = "192.0.2.10" }},
		{"disabled by env", shared, func(t *testing.T, _ *Config, _ string) {
			t.Setenv(doltserver.VerifyServerIdentityEnv, "false")
		}},
		{"disabled by config.yaml", shared, func(t *testing.T, _ *Config, beadsDir string) {
			writeFile(t, filepath.Join(beadsDir, "config.yaml"), doltserver.VerifyServerIdentityKey+": false\n")
		}},
		{"disabled by bd config set", shared, func(t *testing.T, _ *Config, beadsDir string) {
			// bd config set writes a yaml-only key to config.yaml, where the
			// check reads it, and refuses a value it would not honor.
			writeFile(t, filepath.Join(beadsDir, "config.yaml"), "")
			if !config.IsYamlOnlyKey(doltserver.VerifyServerIdentityKey) {
				t.Fatalf("%s is not yaml-only, so bd config set stores it where the check never reads it", doltserver.VerifyServerIdentityKey)
			}
			if err := config.SetYamlConfigInDir(beadsDir, doltserver.VerifyServerIdentityKey, "maybe"); err == nil {
				t.Errorf("bd config set accepted %s=maybe", doltserver.VerifyServerIdentityKey)
			}
			if err := config.SetYamlConfigInDir(beadsDir, doltserver.VerifyServerIdentityKey, "false"); err != nil {
				t.Fatal(err)
			}
		}},
		{"caller-explicit port in shared mode", shared, func(t *testing.T, cfg *Config, _ string) {
			cfg.ServerPortSharedServer = false
			cfg.ServerPortSource = doltserver.PortSourceCallerExplicit
		}},
		{"caller-explicit port", owned, func(t *testing.T, cfg *Config, _ string) {
			cfg.ServerPortSource = doltserver.PortSourceCallerExplicit
		}},
		{"env port outside shared mode", owned, func(t *testing.T, cfg *Config, _ string) {
			cfg.ServerPortSource = doltserver.PortSourceEnv
		}},
		{"external host default port", owned, func(t *testing.T, cfg *Config, _ string) {
			cfg.ServerPortSource = doltserver.PortSourceExternalHostDefault
		}},
		{"external server mode", owned, func(t *testing.T, _ *Config, _ string) {
			t.Setenv("BEADS_DOLT_SERVER_MODE", "1")
		}},
		// A shared server that Docker, systemd or an orchestrator runs: bd
		// never launched it, so there is no record of it to compare.
		{"external server mode in shared mode", shared, func(t *testing.T, _ *Config, _ string) {
			t.Setenv("BEADS_DOLT_SERVER_MODE", "1")
		}},
		{"auto-start off by env in shared mode", shared, func(t *testing.T, _ *Config, _ string) {
			t.Setenv("BEADS_DOLT_AUTO_START", "0")
		}},
		{"auto-start off by env for an env port in shared mode", shared, func(t *testing.T, cfg *Config, _ string) {
			cfg.ServerPortSharedServer = false
			cfg.ServerPortSource = doltserver.PortSourceEnv
			t.Setenv("BEADS_DOLT_AUTO_START", "0")
		}},
		{"auto-start off by config.yaml in shared mode", shared, func(t *testing.T, _ *Config, beadsDir string) {
			writeFile(t, filepath.Join(beadsDir, "config.yaml"), "dolt.auto-start: false\n")
		}},
		{"auto-start off for an owned endpoint", owned, func(t *testing.T, _ *Config, beadsDir string) {
			writeFile(t, filepath.Join(beadsDir, "config.yaml"), "dolt:\n  auto-start: off\n")
		}},
		// bd init --external, whose workspace has no config.yaml saying so
		// yet when init opens its store.
		{"declared managed outside bd by the caller in shared mode", shared, func(t *testing.T, cfg *Config, _ string) {
			cfg.ServerManagedOutsideBD = true
		}},
		{"declared managed outside bd by the caller for an owned endpoint", owned, func(t *testing.T, cfg *Config, _ string) {
			cfg.ServerManagedOutsideBD = true
		}},
		{"disabled for an owned endpoint", owned, func(t *testing.T, _ *Config, _ string) {
			t.Setenv(doltserver.VerifyServerIdentityEnv, "off")
		}},
	}
	for _, tc := range cases {
		t.Run("exempt/"+tc.name, func(t *testing.T) {
			cfg, beadsDir := tc.setup(t)
			tc.edit(t, cfg, beadsDir)
			if got := serverIdentityExpectation(cfg, beadsDir, beadsDir); got != nil {
				t.Fatalf("expectation = %+v, want none", *got)
			}
		})
	}
}

// TestServerIdentityExemptOpenIgnoresRecord: with the check turned off, or
// on a port the caller named, a shared endpoint whose record names another
// server opens and creates its database as before, and the record is left
// alone.
func TestServerIdentityExemptOpenIgnoresRecord(t *testing.T) {
	skipIfNoServer(t)
	ctx := context.Background()
	cases := []struct {
		name string
		edit func(t *testing.T, cfg *Config)
	}{
		{"disabled", func(t *testing.T, _ *Config) { t.Setenv(doltserver.VerifyServerIdentityEnv, "false") }},
		{"caller-explicit port", func(t *testing.T, cfg *Config) {
			cfg.ServerPortSharedServer = false
			cfg.ServerPortSource = doltserver.PortSourceCallerExplicit
		}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir, dataDir := serverIdentityShared(t)
			record := writeIdentityRecord(t, dataDir, foreignServerUUID+"\n")
			dbName := fmt.Sprintf("test_identity_exempt_%d_%d", i, testServerPort)
			cfg := sharedIdentityConfig(t, beadsDir, dbName)
			cfg.CreateIfMissing = true
			tc.edit(t, cfg)

			store, err := New(ctx, cfg)
			if err != nil {
				t.Fatalf("exempt open: %v", err)
			}
			_ = store.Close()
			if got, _ := os.ReadFile(record); string(got) != foreignServerUUID+"\n" {
				t.Errorf("record = %q, want it unchanged", got)
			}
		})
	}
}

// TestServerIdentityDisabledOpenPerformsNoGateOperations: with the check
// turned off, an open of an endpoint that would be checked hands the gate
// no expectation, so it reads no record and queries no server UUID, and
// the open succeeds as before. Enabled, and with a value that does not
// parse, the same open runs the check and refuses the unpaired server.
func TestServerIdentityDisabledOpenPerformsNoGateOperations(t *testing.T) {
	skipIfNoServer(t)
	gateOps := 0
	orig := verifyServerIdentity
	verifyServerIdentity = func(ctx context.Context, q doltversion.RowQuerier, exp *doltserver.ServerIdentityExpectation) error {
		if exp != nil {
			gateOps++
		}
		return orig(ctx, q, exp)
	}
	t.Cleanup(func() { verifyServerIdentity = orig })

	cases := []struct {
		name    string
		value   string
		refused bool
	}{
		{"enabled", "", true},
		{"disabled", "0", false},
		{"invalid value", "not-a-boolean", true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir, dataDir := serverIdentityShared(t)
			t.Setenv(doltserver.VerifyServerIdentityEnv, tc.value)
			dbName := fmt.Sprintf("test_identity_gate_ops_%d_%d", i, testServerPort)
			assertDatabaseNotExists(t, testServerPort, dbName)
			t.Cleanup(func() { dropTestDatabase(t, testServerPort, dbName) })
			cfg := sharedIdentityConfig(t, beadsDir, dbName)
			cfg.CreateIfMissing = true
			gateOps = 0

			store, err := New(context.Background(), cfg)
			if store != nil {
				_ = store.Close()
			}
			if tc.refused {
				if !errors.Is(err, doltserver.ErrServerIdentityMismatch) {
					t.Fatalf("open = %v, want ErrServerIdentityMismatch for the unpaired server", err)
				}
				if gateOps != 1 {
					t.Errorf("the check ran %d times, want 1 (refusing on the first pool)", gateOps)
				}
				assertDatabaseNotExists(t, testServerPort, dbName)
			} else {
				if err != nil {
					t.Fatalf("open with the check off: %v", err)
				}
				if gateOps != 0 {
					t.Errorf("the check ran %d times with verification off, want none", gateOps)
				}
			}
			if _, err := os.Stat(doltserver.ServerIdentityRecordPath(dataDir)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the open created an identity record: %v", err)
			}
		})
	}
}
