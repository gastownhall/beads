//go:build cgo && integration

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// cliDualPresenceTables are the tables whose rows for a dual-present ID the
// CLI tests compare before and after a refused update.
var cliDualPresenceTables = []struct{ table, key string }{
	{"issues", "id"}, {"wisps", "id"},
	{"labels", "issue_id"}, {"wisp_labels", "issue_id"},
	{"events", "issue_id"}, {"wisp_events", "issue_id"},
	{"comments", "issue_id"}, {"wisp_comments", "issue_id"},
	{"dependencies", "issue_id"}, {"wisp_dependencies", "issue_id"},
	{"issue_versions", "issue_id"},
}

// dualPresenceCLI is a server-mode workspace on the shared test server, driven
// through the bd binary.
type dualPresenceCLI struct {
	t   *testing.T
	dir string
	env []string
}

func newDualPresenceCLI(t *testing.T) *dualPresenceCLI {
	t.Helper()
	skipIfNoDolt(t)
	w := &dualPresenceCLI{t: t, dir: t.TempDir(), env: os.Environ()}
	database := uniqueTestDBName(t)
	t.Cleanup(func() { dropTestDatabase(database, testDoltServerPort) })
	w.must("init", "--backend", "dolt", "--server", "--external",
		"--server-host", "127.0.0.1",
		"--server-port", fmt.Sprintf("%d", testDoltServerPort),
		"--database", database,
		"--prefix", "dual", "--quiet")
	return w
}

func (w *dualPresenceCLI) run(args ...string) (string, string, error) {
	w.t.Helper()
	return runBDExecSeparated(w.t, testBD, w.dir, w.env, args...)
}

func (w *dualPresenceCLI) must(args ...string) string {
	w.t.Helper()
	stdout, stderr, err := w.run(args...)
	if err != nil {
		w.t.Fatalf("bd %v failed: %v\nstdout:\n%s\nstderr:\n%s", args, err, stdout, stderr)
	}
	return stdout
}

// seedTwin creates a durable issue through bd create, then inserts a wisp
// twin of its ID whose title, notes, priority, metadata and label differ, the
// way a pre-guard JSONL import beside a demoted bead left it (GH#5991). It
// returns the ID.
func (w *dualPresenceCLI) seedTwin() string {
	w.t.Helper()
	out := w.must("create", "durable copy", "-p", "2", "--labels", "durable-label", "--json")
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil || created.ID == "" {
		w.t.Fatalf("parse create output %q: %v", out, err)
	}
	id := created.ID
	w.must("sql", fmt.Sprintf(`INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, ephemeral, metadata)
		VALUES ('%s', 'wisp copy', '', '', '', 'wisp notes', 'open', 3, 'task', 1, '{"copy":"wisp"}')`, id))
	w.must("sql", fmt.Sprintf("INSERT INTO wisp_labels (issue_id, label) VALUES ('%s', 'wisp-label')", id))
	return id
}

// snapshot renders every row naming id across both planes, and the Dolt
// commit count, through bd sql.
func (w *dualPresenceCLI) snapshot(id string) []string {
	w.t.Helper()
	var snap []string
	for _, tk := range cliDualPresenceTables {
		snap = append(snap, tk.table+": "+w.must("sql", "--csv",
			fmt.Sprintf("SELECT * FROM %s WHERE %s = '%s' ORDER BY 1, 2", tk.table, tk.key, id)))
	}
	return append(snap, "dolt_log: "+w.must("sql", "--csv", "SELECT COUNT(*) AS commits FROM dolt_log"))
}

// TestCLI_UpdateDualPresenceRefuses is the GH#5991 report: on a server-mode
// workspace whose ID has a row in both issues and wisps, `bd update --notes`
// used to exit 0 while writing the wisp copy that show never returns. It must
// now exit nonzero, name the ID, both tables and the server-mode doctor
// repair (which keeps the wisp copy), and write nothing.
func TestCLI_UpdateDualPresenceRefuses(t *testing.T) {
	w := newDualPresenceCLI(t)
	id := w.seedTwin()
	before := w.snapshot(id)
	if !strings.Contains(before[0], "durable copy") || !strings.Contains(before[1], "wisp copy") {
		t.Fatalf("fixture is not dual-present:\n%s\n%s", before[0], before[1])
	}

	stdout, stderr, err := w.run("update", id, "--notes", "probe")
	if err == nil {
		t.Fatalf("bd update of dual-present %s exited 0; want a refusal\nstdout:\n%s\nstderr:\n%s", id, stdout, stderr)
	}
	for _, want := range []string{id, "issues", "wisps", "bd doctor --check=validate --fix", "keeping the wisp copy"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("refusal does not mention %q:\n%s", want, stderr)
		}
	}

	after := w.snapshot(id)
	for i := range before {
		if before[i] != after[i] {
			t.Errorf("refused update changed persisted state:\nbefore %s\nafter  %s", before[i], after[i])
		}
	}
}
