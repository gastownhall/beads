//go:build cgo

package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/testutil"
)

// TestInitServerIdentityGlobalDatabaseRefusesForeignServer: bd init in shared
// mode creates beads_global only on the server bd recorded for the shared
// data directory (GH#4637). A record that names another server, or one that
// is absent, malformed or unreadable, refuses EnsureGlobalDatabase with the
// identity error, init treats it as fatal, and beads_global does not exist.
func TestInitServerIdentityGlobalDatabaseRefusesForeignServer(t *testing.T) {
	server := testutil.StartIsolatedDoltContainerHandle(t)
	port, err := strconv.Atoi(server.Port)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		prepare func(t *testing.T, record string)
		problem string
	}{
		{"mismatched", func(t *testing.T, record string) {
			writeInitIdentityRecord(t, record, "00000000-1111-4222-8333-444444444444\n")
		}, "00000000-1111-4222-8333-444444444444"},
		{"absent", func(t *testing.T, record string) {}, "does not exist"},
		{"blank", func(t *testing.T, record string) { writeInitIdentityRecord(t, record, "  \n") }, "is empty"},
		{"malformed", func(t *testing.T, record string) { writeInitIdentityRecord(t, record, "not-a-uuid\n") }, "is malformed"},
		{"unreadable", func(t *testing.T, record string) {
			if err := os.Mkdir(record, 0o755); err != nil {
				t.Fatal(err)
			}
		}, "could not be read"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir := filepath.Join(t.TempDir(), ".beads")
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
			record := doltserver.ServerIdentityRecordPath(dataDir)
			tc.prepare(t, record)

			identity := initGlobalDatabaseIdentity(beadsDir, "127.0.0.1", port, false)
			if identity == nil {
				t.Fatal("initGlobalDatabaseIdentity = nil for the shared server bd runs, want an expectation")
			}
			err = doltserver.EnsureGlobalDatabase("127.0.0.1", port, "root", "", identity)
			if !errors.Is(err, doltserver.ErrServerIdentityMismatch) {
				t.Fatalf("EnsureGlobalDatabase = %v, want ErrServerIdentityMismatch before CREATE DATABASE", err)
			}
			if !initGlobalDatabaseFatal(err) {
				t.Errorf("init does not stop on %v", err)
			}
			for _, want := range []string{tc.problem, record} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("EnsureGlobalDatabase error does not name %q:\n%v", want, err)
				}
			}
			if globalDatabaseExists(t, port) {
				t.Errorf("%s exists after the refused EnsureGlobalDatabase", doltserver.GlobalDatabaseName)
			}
		})
	}
}

func writeInitIdentityRecord(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// globalDatabaseExists reports whether beads_global exists on the server at
// port.
func globalDatabaseExists(t *testing.T, port int) bool {
	t.Helper()
	admin, err := sql.Open("mysql", fmt.Sprintf("root@tcp(127.0.0.1:%d)/", port))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var name string
	switch err := admin.QueryRowContext(ctx, "SELECT schema_name FROM information_schema.schemata WHERE schema_name = ?", doltserver.GlobalDatabaseName).Scan(&name); {
	case err == nil:
		return true
	case errors.Is(err, sql.ErrNoRows):
		return false
	default:
		t.Fatalf("look for %s: %v", doltserver.GlobalDatabaseName, err)
		return false
	}
}

// TestInitServerIdentityExemptGlobalDatabase: init expects nothing of a port
// the caller named with --server-port, a host bd does not run a server at, or
// a shared server whose check is turned off, so beads_global is created as
// before even though the shared data directory's record names another server.
func TestInitServerIdentityExemptGlobalDatabase(t *testing.T) {
	server := testutil.StartIsolatedDoltContainerHandle(t)
	port, err := strconv.Atoi(server.Port)
	if err != nil {
		t.Fatal(err)
	}
	beadsDir := filepath.Join(t.TempDir(), ".beads")
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
	record := doltserver.ServerIdentityRecordPath(dataDir)
	writeInitIdentityRecord(t, record, "00000000-1111-4222-8333-444444444444\n")

	if initGlobalDatabaseIdentity(beadsDir, "127.0.0.1", port, false) == nil {
		t.Fatal("no expectation for the shared server bd runs")
	}
	if got := initGlobalDatabaseIdentity(beadsDir, "192.0.2.10", port, false); got != nil {
		t.Errorf("expectation for a host bd does not run a server at = %+v, want none", *got)
	}
	t.Run("disabled", func(t *testing.T) {
		t.Setenv(doltserver.VerifyServerIdentityEnv, "false")
		if got := initGlobalDatabaseIdentity(beadsDir, "127.0.0.1", port, false); got != nil {
			t.Errorf("expectation with the check off = %+v, want none", *got)
		}
	})

	identity := initGlobalDatabaseIdentity(beadsDir, "127.0.0.1", port, true)
	if identity != nil {
		t.Fatalf("expectation for a --server-port port = %+v, want none", *identity)
	}
	if err := doltserver.EnsureGlobalDatabase("127.0.0.1", port, "root", "", identity); err != nil {
		t.Fatalf("EnsureGlobalDatabase on a --server-port port: %v", err)
	}
	if !globalDatabaseExists(t, port) {
		t.Errorf("%s was not created", doltserver.GlobalDatabaseName)
	}
	if got, _ := os.ReadFile(record); string(got) != "00000000-1111-4222-8333-444444444444\n" {
		t.Errorf("record = %q, want it unchanged", got)
	}
}

// TestInitServerIdentitySharedRefusesForeignIdentity runs the refusal through
// bd init itself: a first shared-server init starts the shared server and
// pairs its record; with that server still up, its beads_global removed and
// the record naming another server, a second init fails with the identity
// error, creates no beads_global and leaves the record as it was.
func TestInitServerIdentitySharedRefusesForeignIdentity(t *testing.T) {
	skipIfNoDolt(t)
	bd := buildBDForInitTests(t)
	port, err := testutil.FindFreePort()
	if err != nil {
		t.Fatal(err)
	}
	sharedDir := t.TempDir()
	t.Setenv("BEADS_SHARED_SERVER_DIR", sharedDir)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
	t.Setenv(doltserver.VerifyServerIdentityEnv, "")
	stopDoltServerCleanup(t, sharedDir)
	env := append(os.Environ(),
		"BEADS_DOLT_SHARED_SERVER=1",
		"BEADS_DOLT_SERVER_PORT="+strconv.Itoa(port),
		"BEADS_SHARED_SERVER_DIR="+sharedDir,
		doltserver.VerifyServerIdentityEnv+"=",
	)
	runInit := func() ([]byte, error) {
		cmd := exec.Command(bd, "init", "--prefix", "identity", "--skip-hooks", "--quiet")
		cmd.Dir = t.TempDir()
		cmd.Env = env
		return cmd.CombinedOutput()
	}

	if out, err := runInit(); err != nil {
		t.Fatalf("first bd init (pairing): %v\n%s", err, out)
	}
	dataDir, err := doltserver.SharedDoltPath()
	if err != nil {
		t.Fatal(err)
	}
	record := doltserver.ServerIdentityRecordPath(dataDir)
	paired, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("first init did not pair the identity record: %v", err)
	}
	admin, err := sql.Open("mysql", fmt.Sprintf("root@tcp(127.0.0.1:%d)/", port))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	actual, err := doltserver.QueryServerUUID(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(paired)) != actual {
		t.Fatalf("first init paired %q, but the shared server reports %q", paired, actual)
	}

	if _, err := admin.ExecContext(ctx, "DROP DATABASE IF EXISTS "+doltserver.GlobalDatabaseName); err != nil {
		t.Fatalf("drop %s: %v", doltserver.GlobalDatabaseName, err)
	}
	const foreign = "00000000-1111-4222-8333-444444444444"
	writeInitIdentityRecord(t, record, foreign+"\n")

	out, err := runInit()
	if err == nil {
		t.Fatalf("second bd init succeeded against a server its record does not name:\n%s", out)
	}
	for _, want := range []string{"refusing the Dolt server at", foreign, actual} {
		if !strings.Contains(string(out), want) {
			t.Errorf("second bd init output does not name %q:\n%s", want, out)
		}
	}
	if globalDatabaseExists(t, port) {
		t.Errorf("%s exists after the refused bd init", doltserver.GlobalDatabaseName)
	}
	if got, _ := os.ReadFile(record); string(got) != foreign+"\n" {
		t.Errorf("record = %q after the refused init, want it unchanged", got)
	}
}
