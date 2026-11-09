//go:build cgo

package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
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
