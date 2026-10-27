//go:build cgo && integration

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestCLI_Bootstrap_ServerDBMissingPrefixSeedsFromConfigYaml_E2E pins the
// GH#3494 repair on server mode: a workspace whose server database exists but
// has no issue_prefix row (config.yaml carries the prefix — the shape an
// external provisioner or a gc init-style setup leaves) runs `bd bootstrap`
// and ends with the prefix seeded on the database, `bd config get` answering
// it, and `bd create` minting IDs under it — without any embedded shadow
// database being created and without claiming a fresh database was made.
func TestCLI_Bootstrap_ServerDBMissingPrefixSeedsFromConfigYaml_E2E(t *testing.T) {
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
		"--prefix", "la", "--quiet")
	if initErr != nil {
		t.Fatalf("bd init --server failed: %v\n%s", initErr, initOut)
	}

	// Drop the config-table row bd init wrote: the database now exists with no
	// issue_prefix, exactly like an externally provisioned database.
	sqlOut, sqlErr := runBDExecAllowErrorWithEnv(t, tmpDir, env, "sql",
		"DELETE FROM config WHERE `key` = 'issue_prefix'")
	if sqlErr != nil {
		t.Fatalf("bd sql DELETE failed: %v\n%s", sqlErr, sqlOut)
	}

	// The GH#3494 workspace shape: no project_id in metadata.json (nothing
	// marks the workspace as already initialized elsewhere), no local shadow
	// database, and config.yaml naming the prefix with auto-start pinned off
	// so every command dials the external server.
	metaPath := filepath.Join(tmpDir, ".beads", "metadata.json")
	metaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read metadata.json: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		t.Fatalf("parse metadata.json: %v\n%s", err, metaBytes)
	}
	delete(meta, "project_id")
	metaBytes, err = json.MarshalIndent(meta, "", "  ")
	if err != nil {
		t.Fatalf("re-encode metadata.json: %v", err)
	}
	if err := os.WriteFile(metaPath, metaBytes, 0o644); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}

	// Rewrite config.yaml through a map round-trip. bd init leaves it as a
	// comment-only template (every key, including issue-prefix, commented
	// out — the document root parses as nil), so the fixture must set real
	// keys the way an external provisioner would, not append text beside
	// the template.
	configPath := filepath.Join(tmpDir, ".beads", "config.yaml")
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	var configYAML map[string]any
	if err := yaml.Unmarshal(configBytes, &configYAML); err != nil {
		t.Fatalf("parse config.yaml: %v\n%s", err, configBytes)
	}
	if configYAML == nil {
		// The comment-only template has no root mapping.
		configYAML = map[string]any{}
	}
	configYAML["issue-prefix"] = "la"
	doltBlock, _ := configYAML["dolt"].(map[string]any)
	if doltBlock == nil {
		doltBlock = map[string]any{}
	}
	doltBlock["auto-start"] = false
	configYAML["dolt"] = doltBlock
	configBytes, err = yaml.Marshal(configYAML)
	if err != nil {
		t.Fatalf("re-encode config.yaml: %v", err)
	}
	if err := os.WriteFile(configPath, configBytes, 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	if _, err := os.Stat(filepath.Join(tmpDir, ".beads", "embeddeddolt")); err == nil {
		t.Fatal("precondition: init --server --external created an embedded shadow database")
	}

	// Precondition: the symptom. The prefix lives only in config.yaml, which
	// does not satisfy the database-backed read.
	preOut, preErr := runBDExecAllowErrorWithEnv(t, tmpDir, env, "config", "get", "issue_prefix")
	if preErr != nil {
		t.Fatalf("bd config get failed before bootstrap: %v\n%s", preErr, preOut)
	}
	if !strings.Contains(preOut, "not set") {
		t.Fatalf("precondition: bd config get issue_prefix = %q, want it to report not set", strings.TrimSpace(preOut))
	}

	bootOut, bootErr := runBDExecAllowErrorWithEnv(t, tmpDir, env, "bootstrap", "--yes")
	if bootErr != nil {
		t.Fatalf("bd bootstrap --yes failed: %v\n%s", bootErr, bootOut)
	}
	if !strings.Contains(bootOut, `Set issue_prefix "la" on existing database`) {
		t.Fatalf("bootstrap should seed the prefix on the existing database, got:\n%s", bootOut)
	}
	if strings.Contains(bootOut, "Created fresh database") {
		t.Fatalf("bootstrap must not claim a fresh database was created:\n%s", bootOut)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, ".beads", "embeddeddolt")); err == nil {
		t.Fatal("bootstrap created an embedded shadow database; the server workspace must be served by its server")
	}

	getOut, getErr := runBDExecAllowErrorWithEnv(t, tmpDir, env, "config", "get", "issue_prefix")
	if getErr != nil {
		t.Fatalf("bd config get failed after bootstrap: %v\n%s", getErr, getOut)
	}
	if got := strings.TrimSpace(getOut); got != "la" {
		t.Fatalf("bd config get issue_prefix = %q, want la (seeded on the server database)", got)
	}

	createOut, createErr := runBDExecAllowErrorWithEnv(t, tmpDir, env,
		"create", "--title", "seeded prefix", "--type", "task", "--json")
	if createErr != nil {
		t.Fatalf("bd create failed after bootstrap: %v\n%s", createErr, createOut)
	}
	if !strings.Contains(createOut, `"id": "la-`) && !strings.Contains(createOut, `"id":"la-`) {
		t.Fatalf("bd create should mint an la- ID, got:\n%s", createOut)
	}
}
