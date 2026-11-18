//go:build cgo

package main

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// storedPeerTier reads a peer's sovereignty column straight from the embedded
// database, independent of add-peer's own report (bd sql is not available in
// embedded mode). Call it between bd subprocesses, never while one runs.
func storedPeerTier(t *testing.T, beadsDir, name string) string {
	t.Helper()
	database := ""
	if cfg, _ := configfile.Load(beadsDir); cfg != nil {
		database = cfg.GetDoltDatabase()
	}
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer func() {
		if err := cleanup(); err != nil {
			t.Fatalf("close embedded SQL: %v", err)
		}
	}()
	var sov string
	if err := db.QueryRowContext(t.Context(),
		"SELECT COALESCE(sovereignty, '') FROM federation_peers WHERE name = ?", name).Scan(&sov); err != nil {
		t.Fatalf("read stored sovereignty for %s: %v", name, err)
	}
	return sov
}

// assertAddPeerJSON parses add-peer --json output and pins its key set.
func assertAddPeerJSON(t *testing.T, out string) map[string]json.RawMessage {
	t.Helper()
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("failed to parse add-peer --json: %v\n%s", err, out)
	}
	// schema_version comes from the outputJSON envelope, not from this command.
	assertExactJSONKeys(t, "add-peer --json", payload, "added", "url", "has_auth", "sovereignty", "schema_version")
	return payload
}

// addPeerJSONSovereignty returns add-peer --json's sovereignty value.
func addPeerJSONSovereignty(t *testing.T, payload map[string]json.RawMessage) string {
	t.Helper()
	var sov string
	if err := json.Unmarshal(payload["sovereignty"], &sov); err != nil {
		t.Fatalf("add-peer --json sovereignty is not a string: %v (%s)", err, payload["sovereignty"])
	}
	return sov
}
