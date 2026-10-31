//go:build cgo && integration

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

// TestCLI_ServerModeConfigWritesCommit_E2E is the user-level pin for GH#4078:
// on the direct SQL-server route, every config-table write verb must leave no
// config dirt in the working set. Before the fix each of them committed the SQL
// transaction only, so the rows (and the custom_statuses / custom_types lookup
// tables SetConfig projects from status.custom and types.custom) sat
// uncommitted: never pushed, and a dirty internal key refused the next pull.
//
// The secret-key step pins the yaml-only branch of `bd config unset`: the
// leaked credential row it deletes must be committed too, or HEAD (what
// `bd dolt push` publishes) keeps it.
//
// The last steps pin the kv.* screen on memory writes: a dirty internal config
// key that the memory write did not make (here the issue_prefix of an unwired
// `bd rename-prefix`) is named and left alone, and the memory command still
// succeeds with a warning, because its own write has landed.
func TestCLI_ServerModeConfigWritesCommit_E2E(t *testing.T) {
	skipIfNoDolt(t)

	tmpDir := t.TempDir()
	env := os.Environ()
	database := uniqueTestDBName(t)
	t.Cleanup(func() {
		dropTestDatabase(database, testDoltServerPort)
	})

	bd := func(args ...string) (string, error) {
		return runBDExecAllowErrorWithEnv(t, tmpDir, env, args...)
	}
	mustBD := func(args ...string) string {
		t.Helper()
		out, err := bd(args...)
		if err != nil {
			t.Fatalf("bd %v failed: %v\n%s", args, err, out)
		}
		return out
	}
	// dirtyConfigTables reports which of config and its lookup tables are dirty,
	// comma-joined in name order ("" when all are clean).
	dirtyConfigTables := func() string {
		t.Helper()
		out := mustBD("sql", "--csv",
			"SELECT table_name FROM dolt_status WHERE table_name IN ('config', 'custom_statuses', 'custom_types') ORDER BY table_name")
		var dirty []string
		for _, line := range strings.Split(out, "\n") {
			switch name := strings.TrimSpace(line); name {
			case "config", "custom_statuses", "custom_types":
				dirty = append(dirty, name)
			}
		}
		return strings.Join(dirty, ",")
	}

	mustBD("init", "--backend", "dolt", "--server", "--external",
		"--server-host", "127.0.0.1",
		"--server-port", fmt.Sprintf("%d", testDoltServerPort),
		"--database", database,
		"--prefix", "cfgc", "--quiet")
	if got := dirtyConfigTables(); got != "" {
		t.Fatalf("precondition: %s dirty right after bd init", got)
	}

	for _, step := range [][]string{
		{"remember", "--key", "gh4078", "server-mode memory"},
		{"config", "set", "status.custom", "gh4078review:active"},
		{"config", "set-many", "types.custom=gh4078kind", "status.custom=gh4078review:active,gh4078parked:frozen"},
		{"config", "unset", "types.custom"},
		{"forget", "gh4078"},
		{"kv", "set", "gh7523", "server-mode kv"},
		{"kv", "clear", "gh7523"},
	} {
		mustBD(step...)
		if got := dirtyConfigTables(); got != "" {
			t.Fatalf("after bd %s: %s left uncommitted in the working set (GH#4078)", strings.Join(step, " "), got)
		}
	}

	// A bd that predates notion.token moving to config.yaml stored it in the
	// config table, and that row was committed and pushed.
	mustBD("sql", "INSERT INTO config (`key`, value) VALUES ('notion.token', 'gh4078-leaked-credential')")
	mustBD("dolt", "commit", "-m", "gh4078: an older bd stored notion.token in the database")
	if got := dirtyConfigTables(); got != "" {
		t.Fatalf("precondition: %s dirty after bd dolt commit", got)
	}
	if out := mustBD("config", "unset", "notion.token"); !strings.Contains(out, "Also removed a stored notion.token row") {
		t.Fatalf("bd config unset notion.token did not remove the stored row:\n%s", out)
	}
	if got := dirtyConfigTables(); got != "" {
		t.Fatalf("after bd config unset notion.token: %s left uncommitted, so HEAD still publishes the credential (GH#4078)", got)
	}

	mustBD("rename-prefix", "rvx-")
	if got := dirtyConfigTables(); got != "config" {
		t.Fatalf("precondition: dirty = %q after bd rename-prefix, want its unwired issue_prefix write in config", got)
	}
	stdout, stderr, err := runBDExecSeparated(t, testBD, tmpDir, env, "remember", "--key", "gh4078-screened", "screened memory", "--json")
	if err != nil {
		t.Fatalf("bd remember failed after its write landed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	var remembered struct {
		Key             string   `json:"key"`
		Committed       *bool    `json:"committed"`
		DirtyConfigKeys []string `json:"dirty_config_keys"`
	}
	if err := json.Unmarshal([]byte(stdout), &remembered); err != nil {
		t.Fatalf("bd remember --json stdout is not one JSON object: %v\n%s", err, stdout)
	}
	if remembered.Key != "gh4078-screened" || remembered.Committed == nil || *remembered.Committed ||
		!slices.Equal(remembered.DirtyConfigKeys, []string{"issue_prefix"}) {
		t.Fatalf("bd remember --json = %s, want key gh4078-screened, committed false, dirty_config_keys [issue_prefix]", stdout)
	}
	if !strings.Contains(stderr, "Warning:") || !strings.Contains(stderr, "issue_prefix") {
		t.Fatalf("bd remember stderr does not warn about the refused commit naming issue_prefix:\n%s", stderr)
	}
	if out := mustBD("recall", "gh4078-screened"); !strings.Contains(out, "screened memory") {
		t.Fatalf("the screened memory was not stored:\n%s", out)
	}
	if out := mustBD("forget", "gh4078-screened"); !strings.Contains(out, "Warning:") || !strings.Contains(out, "issue_prefix") {
		t.Fatalf("bd forget did not warn about the refused commit naming issue_prefix:\n%s", out)
	}
	if got := dirtyConfigTables(); got != "config" {
		t.Fatalf("after the refused memory commits: dirty = %q, want config left for bd dolt commit", got)
	}
	mustBD("dolt", "commit", "-m", "gh4078: commit the screened config")
	if got := dirtyConfigTables(); got != "" {
		t.Fatalf("after bd dolt commit: %s still dirty", got)
	}
}
