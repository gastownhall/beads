//go:build cgo && unix

package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// K1, against a real server: the unit-of-work provider `bd serve` builds for a
// server-mode workspace.
//
// A provider has no storage.DoltStorage to ask for a settings plane, so its flag
// comes from viper OR its own database's row, read through a unit of work when
// the provider is constructed. The unit tests prove the read with fakes; what only
// a real server can prove is that the read WORKS at that moment -- a pooled
// connection, a database already selected, a config table already migrated -- and
// that a provider which silently fell back to the environment alone would have
// looked exactly the same from outside: the served mutation succeeds, the response
// is normal, and issue_versions is simply empty.
//
// Runs in the proxied-server lane only (BEADS_TEST_PROXIED_SERVER=1), like the
// events-journal twin, TestServeActivatesTheEventsJournal.

// serverVersionRows counts issue_versions rows in one database on the shared test
// Dolt container.
func serverVersionRows(t *testing.T, port int, database string) int {
	t.Helper()
	dsn := fmt.Sprintf("root:@tcp(127.0.0.1:%d)/%s?parseTime=true", port, database)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("sql.Open %s: %v", dsn, err)
	}
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issue_versions").Scan(&n); err != nil {
		t.Fatalf("count issue_versions in %s: %v", database, err)
	}
	return n
}

// serveOneMutation starts `bd serve` on a server-mode workspace, makes one HTTP
// mutation through the provider serve built, shuts serve down, and returns how many
// issue_versions rows that mutation added.
func serveOneMutation(t *testing.T, bd string, port int, p serverModeProject) int {
	t.Helper()
	before := serverVersionRows(t, port, p.database)

	// Nothing from the developer's shell may switch the feature on for serve.
	env := envWithout(p.env, vhEnvVar)
	sp := startServe(t, bd, p.dir, env)
	status, body := sp.postJSON(t, "/v0/beads/issues:batchCreate",
		`{"actor":"tester","items":[{"title":"served mutation","issue_type":"task"}]}`)
	if status != http.StatusOK {
		t.Fatalf("POST /v0/beads/issues:batchCreate = %d: %v\nstderr:\n%s", status, body, sp.stderr.String())
	}
	sp.shutdown(t)

	return serverVersionRows(t, port, p.database) - before
}

// TestVersionedHistoryServeActivatesTheProviderFromItsOwnRow: two server-mode
// workspaces on one server. The one whose OWN row says on records a served
// mutation; the one with no row does not, and does not inherit the other's answer.
// Neither is helped by the environment, which is unset.
func TestVersionedHistoryServeActivatesTheProviderFromItsOwnRow(t *testing.T) {
	port := requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	enabled := newServerModeProject(t, bd, "vhse")
	quiet := newServerModeProject(t, bd, "vhsq")
	enabled.run(t, bd, "config", "set", versionedHistorySettingKey, "true")

	if got := serveOneMutation(t, bd, port, enabled); got != 1 {
		t.Errorf("a served mutation into a workspace whose row says on added %d issue_versions row(s), want 1: "+
			"the provider serve builds is not reading its own database's row", got)
	}
	if got := serveOneMutation(t, bd, port, quiet); got != 0 {
		t.Errorf("a served mutation into a workspace with no row added %d issue_versions row(s), want 0", got)
	}
}
