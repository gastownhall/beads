// GH#4470: `bd init --server --external` against a Dolt sql-server whose
// sessions start with @@dolt_transaction_commit = 1. Frozen migrations 0040 and
// 0041 end in in-body CALL DOLT_COMMIT, which such a session has already
// committed for them, so a fresh init died at 0040 with "nothing to commit".
//
// This file MUST NOT carry a cgo build tag: it drives a bd binary via
// subprocess against its own isolated Dolt server.

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/testutil"
)

func TestInitExternalServerUnderTransactionCommit_4470(t *testing.T) {
	// Its own server: the transaction-commit default is server-wide.
	server := testutil.StartIsolatedDoltContainerHandle(t)
	port, err := strconv.Atoi(server.Port)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	admin, err := sql.Open("mysql", doltutil.ServerDSN{Host: "127.0.0.1", Port: port, User: "root"}.String())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.ExecContext(ctx, "SET @@GLOBAL.dolt_transaction_commit = 1"); err != nil {
		t.Fatalf("enable dolt_transaction_commit server-wide: %v", err)
	}
	// Prove new sessions — the ones bd opens — start with it on. A fresh pool:
	// SET GLOBAL leaves the admin session's own value alone.
	probe, err := sql.Open("mysql", doltutil.ServerDSN{Host: "127.0.0.1", Port: port, User: "root"}.String())
	if err != nil {
		t.Fatal(err)
	}
	var enabled int
	err = probe.QueryRowContext(ctx, "SELECT @@SESSION.dolt_transaction_commit").Scan(&enabled)
	_ = probe.Close()
	if err != nil || enabled != 1 {
		t.Fatalf("new session @@dolt_transaction_commit = %d (%v), want 1", enabled, err)
	}

	dir := t.TempDir()
	bd := buildBDForInitTests(t)
	runGitForBootstrapTest(t, dir, "init", "-q")
	runGitForBootstrapTest(t, dir, "config", "core.hooksPath", ".git/hooks")

	stdout, stderr, code := runBDMigrationFreeze(t, bd, dir, "init", "--server", "--external",
		"--server-host", "127.0.0.1", "--server-port", server.Port, "--server-user", "root",
		"--prefix", "tc", "--quiet", "--non-interactive", "--skip-hooks", "--skip-agents")
	if code != 0 || strings.Contains(stderr, "nothing to commit") {
		t.Fatalf("bd init --server --external under dolt_transaction_commit=1 failed (exit %d):\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	var meta struct {
		Database string `json:"dolt_database"`
	}
	data, err := os.ReadFile(filepath.Join(dir, ".beads", "metadata.json"))
	if err != nil {
		t.Fatalf("reading metadata.json: %v", err)
	}
	if err := json.Unmarshal(data, &meta); err != nil || meta.Database == "" {
		t.Fatalf("metadata.json names no dolt_database (%v):\n%s", err, data)
	}
	for _, c := range []struct {
		table string
		want  int
	}{
		{"schema_migrations", schema.LatestVersion()},
		{"ignored_schema_migrations", schema.LatestIgnoredVersion()},
	} {
		var got int
		q := fmt.Sprintf("SELECT COALESCE(MAX(version), 0) FROM `%s`.%s", meta.Database, c.table) //nolint:gosec // database name from bd's own metadata
		if err := admin.QueryRowContext(ctx, q).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", c.table, err)
		}
		if got != c.want {
			t.Errorf("%s version = %d, want %d", c.table, got, c.want)
		}
	}
}
