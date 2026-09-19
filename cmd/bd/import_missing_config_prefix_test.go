//go:build cgo && integration

package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestCLI_Import_ServerDatabaseMissingConfigPrefix_E2E pins that `bd import`
// succeeds against a server-mode database whose `config` table has no
// issue_prefix row, as long as config.yaml carries one.
//
// This is the state an externally-provisioned database is in: config.yaml
// carries issue-prefix (written directly by the provisioner, not via `bd
// init`), but the database's own config table has no issue_prefix row (only
// `bd init`'s SetConfig call writes one). No orchestrator is involved in
// reproducing this — the test simulates it directly by deleting the
// config-table row and writing config.yaml by hand, exactly like
// dolt_metadata_e2e_test.go simulates a pre-Phase-1 database.
//
// Before the fix, NewBatchContext (via ReadConfigPrefix) required this row
// unconditionally, so `bd import` failed with "issue_prefix config is
// missing" even though config.yaml had the prefix and `bd where`/`bd
// context` resolved it correctly.
func TestCLI_Import_ServerDatabaseMissingConfigPrefix_E2E(t *testing.T) {
	skipIfNoDolt(t)

	tmpDir := t.TempDir()
	env := os.Environ()
	database := uniqueTestDBName(t)
	t.Cleanup(func() {
		dropTestDatabase(database, testDoltServerPort)
	})

	initOut, initErr := runBDExecAllowErrorWithEnv(t, tmpDir, env,
		"init", "--backend", "dolt", "--server", "--external",
		"--server-host", "127.0.0.1",
		"--server-port", fmt.Sprintf("%d", testDoltServerPort),
		"--database", database,
		"--prefix", "extdb", "--quiet")
	if initErr != nil {
		t.Fatalf("bd init --server failed: %v\n%s", initErr, initOut)
	}

	// Delete the config-table row bd init just wrote, and write issue-prefix
	// into config.yaml directly (bd init does not do this on its own — an
	// external provisioner writing config.yaml by hand, as an orchestrator
	// would, is what puts a project in this state: config.yaml has the
	// prefix, the database's own config table does not).
	sqlOut, sqlErr := runBDExecAllowErrorWithEnv(t, tmpDir, env, "sql",
		"DELETE FROM config WHERE `key` = 'issue_prefix'")
	if sqlErr != nil {
		t.Fatalf("bd sql DELETE failed: %v\n%s", sqlErr, sqlOut)
	}

	configPath := tmpDir + "/.beads/config.yaml"
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config.yaml: %v", err)
	}
	configBytes = append(configBytes, []byte("\nissue-prefix: extdb\n")...)
	if err := os.WriteFile(configPath, configBytes, 0644); err != nil {
		t.Fatalf("failed to write issue-prefix into config.yaml: %v", err)
	}

	// Confirm the precondition: reads that need the config-table prefix now
	// fail, matching the reported bug's symptom.
	precheckOut, precheckErr := runBDExecAllowErrorWithEnv(t, tmpDir, env, "create", "--title", "should fail before fix")
	if precheckErr == nil {
		t.Fatalf("expected bd create to fail with the config-table row deleted, but it succeeded: %s", precheckOut)
	}
	if !strings.Contains(precheckOut, "issue_prefix config is missing") {
		t.Fatalf("expected 'issue_prefix config is missing', got: %s", precheckOut)
	}

	issue := `{"id":"extdb-1","title":"Externally provisioned issue","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z"}`
	if err := os.WriteFile(tmpDir+"/external.jsonl", []byte(issue+"\n"), 0644); err != nil {
		t.Fatalf("failed to write JSONL fixture: %v", err)
	}

	importOut, importErr := runBDExecAllowErrorWithEnv(t, tmpDir, env, "import", "-i", "external.jsonl")
	if importErr != nil {
		t.Fatalf("bd import failed against a server-mode database missing its config-table prefix: %v\nOutput: %s", importErr, importOut)
	}

	listOut, listErr := runBDExecAllowErrorWithEnv(t, tmpDir, env, "list", "--id", "extdb-1", "--json")
	if listErr != nil {
		t.Fatalf("bd list failed: %v\n%s", listErr, listOut)
	}
	if !strings.Contains(listOut, "extdb-1") {
		t.Fatalf("expected extdb-1 to be imported, but list output was: %s", listOut)
	}
}
