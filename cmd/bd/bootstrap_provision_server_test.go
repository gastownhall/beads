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

// filterEnvPrefixes drops every env entry starting with one of the prefixes.
func filterEnvPrefixes(env []string, prefixes ...string) []string {
	out := make([]string, 0, len(env))
	for _, pair := range env {
		drop := false
		for _, prefix := range prefixes {
			if strings.HasPrefix(pair, prefix) {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, pair)
		}
	}
	return out
}

// serverBootstrapProvisionFixture builds the AC server-mode workspace whose
// database does not exist yet: bd init provisions the workspace against the
// test server (creating liveDB), then the workspace is re-pointed at
// targetDB — a name nothing has created — with project_id stripped, so
// bootstrap sees a fresh workspace that must provision targetDB itself (with
// project_id set, the plan-time refusal for already-initialized projects
// takes over instead). liveDB stays on the server deliberately: it is a
// second schema-bearing database, which keeps bootstrap's metadata repair
// from adopting "the only server database with Beads schema" and re-pointing
// the workspace at a database that is not its own. config.yaml names the
// prefix the way an external provisioner would.
func serverBootstrapProvisionFixture(t *testing.T, liveDB, targetDB string) (dir string, env []string) {
	t.Helper()
	skipIfNoDolt(t)

	dir = t.TempDir()
	env = os.Environ()

	initOut, initErr := runBDExecAllowErrorWithEnv(t, dir, env,
		"init", "--backend", "dolt", "--server", "--external",
		"--server-host", "127.0.0.1",
		"--server-port", fmt.Sprintf("%d", testDoltServerPort),
		"--database", liveDB,
		"--prefix", "la", "--quiet")
	if initErr != nil {
		t.Fatalf("bd init --server failed: %v\n%s", initErr, initOut)
	}

	// Fresh-workspace shape aimed at a database that does not exist yet.
	metaPath := filepath.Join(dir, ".beads", "metadata.json")
	metaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read metadata.json: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		t.Fatalf("parse metadata.json: %v\n%s", err, metaBytes)
	}
	delete(meta, "project_id")
	meta["dolt_database"] = targetDB
	metaBytes, err = json.MarshalIndent(meta, "", "  ")
	if err != nil {
		t.Fatalf("re-encode metadata.json: %v", err)
	}
	if err := os.WriteFile(metaPath, metaBytes, 0o644); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}

	// bd init leaves config.yaml a comment-only template; set real keys the
	// way an external provisioner would (see the AC-1 twin fixture).
	configPath := filepath.Join(dir, ".beads", "config.yaml")
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	var configYAML map[string]any
	if err := yaml.Unmarshal(configBytes, &configYAML); err != nil {
		t.Fatalf("parse config.yaml: %v\n%s", err, configBytes)
	}
	if configYAML == nil {
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
	return dir, env
}

// TestCLI_Bootstrap_ServerModeNoDB_CreatesOnServer_E2E pins GH#3494's
// server-mode half: a server-mode workspace with no recovery source whose
// database does not exist yet provisions that database ON THE SERVER through
// the same store selection the read path uses. The hand-built embedded open
// this replaced reported "Created fresh database" while writing an embedded
// shadow database (.beads/embeddeddolt) no bd command ever read.
func TestCLI_Bootstrap_ServerModeNoDB_CreatesOnServer_E2E(t *testing.T) {
	liveDB := uniqueTestDBName(t)
	targetDB := uniqueTestDBName(t)
	t.Cleanup(func() {
		dropTestDatabase(liveDB, testDoltServerPort)
		dropTestDatabase(targetDB, testDoltServerPort)
	})
	dir, env := serverBootstrapProvisionFixture(t, liveDB, targetDB)

	bootOut, bootErr := runBDExecAllowErrorWithEnv(t, dir, env, "bootstrap", "--yes")
	if bootErr != nil {
		t.Fatalf("bd bootstrap --yes failed: %v\n%s", bootErr, bootOut)
	}
	if !strings.Contains(bootOut, `Created fresh database with prefix "la"`) {
		t.Fatalf("bootstrap should create the database on the server and say so, got:\n%s", bootOut)
	}
	if _, err := os.Stat(filepath.Join(dir, ".beads", "embeddeddolt")); err == nil {
		t.Fatal("bootstrap created an embedded shadow database; a server-mode workspace must be provisioned on its server")
	}

	getOut, getErr := runBDExecAllowErrorWithEnv(t, dir, env, "config", "get", "issue_prefix")
	if getErr != nil {
		t.Fatalf("bd config get failed after bootstrap: %v\n%s", getErr, getOut)
	}
	if got := strings.TrimSpace(getOut); got != "la" {
		t.Fatalf("bd config get issue_prefix = %q, want la (written to the server database)", got)
	}

	createOut, createErr := runBDExecAllowErrorWithEnv(t, dir, env,
		"create", "--title", "provisioned on server", "--type", "task", "--json")
	if createErr != nil {
		t.Fatalf("bd create failed after bootstrap: %v\n%s", createErr, createOut)
	}
	if !strings.Contains(createOut, `"id": "la-`) && !strings.Contains(createOut, `"id":"la-`) {
		t.Fatalf("bd create should mint an la- ID against the server database, got:\n%s", createOut)
	}
}

// TestCLI_Bootstrap_ServerModeNoDB_JSONLImport_E2E is the jsonl-import
// action's share of the same property: with .beads/issues.jsonl present and
// no database, bootstrap provisions on the server and imports there — no
// embedded shadow. (The restore action shares the identical construction.)
func TestCLI_Bootstrap_ServerModeNoDB_JSONLImport_E2E(t *testing.T) {
	liveDB := uniqueTestDBName(t)
	targetDB := uniqueTestDBName(t)
	t.Cleanup(func() {
		dropTestDatabase(liveDB, testDoltServerPort)
		dropTestDatabase(targetDB, testDoltServerPort)
	})
	dir, env := serverBootstrapProvisionFixture(t, liveDB, targetDB)

	issue := `{"id":"la-1","title":"Imported on provision","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(dir, ".beads", "issues.jsonl"), []byte(issue+"\n"), 0o644); err != nil {
		t.Fatalf("write issues.jsonl: %v", err)
	}

	bootOut, bootErr := runBDExecAllowErrorWithEnv(t, dir, env, "bootstrap", "--yes")
	if bootErr != nil {
		t.Fatalf("bd bootstrap --yes failed: %v\n%s", bootErr, bootOut)
	}
	if !strings.Contains(bootOut, "Imported 1 issue") {
		t.Fatalf("bootstrap should import the JSONL into the provisioned server database, got:\n%s", bootOut)
	}
	if _, err := os.Stat(filepath.Join(dir, ".beads", "embeddeddolt")); err == nil {
		t.Fatal("bootstrap created an embedded shadow database; the import belongs on the server database")
	}

	listOut, listErr := runBDExecAllowErrorWithEnv(t, dir, env, "list", "--id", "la-1", "--json")
	if listErr != nil {
		t.Fatalf("bd list failed after bootstrap: %v\n%s", listErr, listOut)
	}
	if !strings.Contains(listOut, "la-1") {
		t.Fatalf("expected la-1 on the server database after import, got:\n%s", listOut)
	}
}

// TestCLI_Bootstrap_ServerModeNoDB_UnreachableServerFails_E2E pins that a
// server-mode workspace whose server cannot be reached fails bootstrap
// non-zero instead of reporting "Created fresh database": the workspace's
// database lives on that server, and silently provisioning an embedded
// shadow would strand every later command on an empty database (GH#3494).
//
// The server is made unreachable by resolving NO endpoint at all (no port in
// metadata, no BEADS_DOLT_* override, auto-start pinned off). This is a pin
// rather than a base-discriminating test: at the branch base bootstrap also
// exited non-zero here, at the metadata-repair guard — the wrong-store
// provisioning the fix targets shows its base failure in the two tests
// above, against a reachable server. This test keeps the no-false-success
// property true end to end.
func TestCLI_Bootstrap_ServerModeNoDB_UnreachableServerFails_E2E(t *testing.T) {
	liveDB := uniqueTestDBName(t)
	targetDB := uniqueTestDBName(t)
	t.Cleanup(func() {
		dropTestDatabase(liveDB, testDoltServerPort)
		dropTestDatabase(targetDB, testDoltServerPort)
	})
	dir, env := serverBootstrapProvisionFixture(t, liveDB, targetDB)

	metaPath := filepath.Join(dir, ".beads", "metadata.json")
	metaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read metadata.json: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		t.Fatalf("parse metadata.json: %v\n%s", err, metaBytes)
	}
	delete(meta, "dolt_server_port")
	delete(meta, "dolt_server_host")
	metaBytes, err = json.MarshalIndent(meta, "", "  ")
	if err != nil {
		t.Fatalf("re-encode metadata.json: %v", err)
	}
	if err := os.WriteFile(metaPath, metaBytes, 0o644); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}
	env = filterEnvPrefixes(env,
		"BEADS_DOLT_PORT=", "BEADS_DOLT_SERVER_PORT=",
		"BEADS_DOLT_HOST=", "BEADS_DOLT_SERVER_HOST=",
		"BEADS_DOLT_AUTO_START=")

	bootOut, bootErr := runBDExecAllowErrorWithEnv(t, dir, env, "bootstrap", "--yes")
	t.Logf("bootstrap with no reachable server (err=%v):\n%s", bootErr, bootOut)
	if bootErr == nil {
		t.Fatalf("bootstrap with no reachable server must fail, reported success:\n%s", bootOut)
	}
	if strings.Contains(bootOut, "Created fresh database") {
		t.Fatalf("bootstrap must not claim a fresh database with no reachable server:\n%s", bootOut)
	}
	if _, err := os.Stat(filepath.Join(dir, ".beads", "embeddeddolt")); err == nil {
		t.Fatal("bootstrap with no reachable server created an embedded shadow database")
	}
}
