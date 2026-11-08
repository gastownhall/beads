//go:build cgo && integration

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
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

// twinListingQuery is the query the refusal says to run before the repair. It
// selects what fix.CrossTableDuplicates selects: every ID the repair rewrites.
const twinListingQuery = "SELECT id FROM issues WHERE id IN (SELECT id FROM wisps)"

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

// create runs bd create for a durable issue and returns its ID.
func (w *dualPresenceCLI) create(title string, args ...string) string {
	w.t.Helper()
	out := w.must(append([]string{"create", title, "--json"}, args...)...)
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil || created.ID == "" {
		w.t.Fatalf("parse create output %q: %v", out, err)
	}
	return created.ID
}

// seedTwin creates a durable issue through bd create, then gives it a wisp
// twin (addWispTwin). It returns the ID.
func (w *dualPresenceCLI) seedTwin() string {
	w.t.Helper()
	id := w.create("durable copy", "-p", "2", "--labels", "durable-label")
	w.addWispTwin(id)
	return id
}

// addWispTwin inserts a wisp row for the durable issue id whose title, notes,
// priority, metadata and label differ, the way a pre-guard JSONL import
// beside a demoted bead left it (GH#5991).
func (w *dualPresenceCLI) addWispTwin(id string) {
	w.t.Helper()
	w.must("sql", fmt.Sprintf(`INSERT INTO wisps (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, ephemeral, metadata)
		VALUES ('%s', 'wisp copy', '', '', '', 'wisp notes', 'open', 3, 'task', 1, '{"copy":"wisp"}')`, id))
	w.must("sql", fmt.Sprintf("INSERT INTO wisp_labels (issue_id, label) VALUES ('%s', 'wisp-label')", id))
}

// dependents is `bd dep list <id> --direction=up`, the listing the refusal
// says to record before the repair, as sorted "<dependent> <type>" pairs.
func (w *dualPresenceCLI) dependents(id string) []string {
	w.t.Helper()
	out := w.must("dep", "list", id, "--direction=up", "--json")
	var related []struct {
		ID             string `json:"id"`
		DependencyType string `json:"dependency_type"`
	}
	if err := json.Unmarshal([]byte(out), &related); err != nil {
		w.t.Fatalf("parse dep list output %q: %v", out, err)
	}
	pairs := make([]string, 0, len(related))
	for _, r := range related {
		pairs = append(pairs, r.ID+" "+r.DependencyType)
	}
	sort.Strings(pairs)
	return pairs
}

// twinIDs runs twinListingQuery through bd sql, as the refusal says to before
// the repair, and returns the IDs it prints, sorted.
func (w *dualPresenceCLI) twinIDs() []string {
	w.t.Helper()
	lines := strings.Split(strings.TrimSpace(w.must("sql", "--csv", twinListingQuery)), "\n")
	if strings.TrimSpace(lines[0]) != "id" {
		w.t.Fatalf("bd sql %q printed no id header:\n%s", twinListingQuery, strings.Join(lines, "\n"))
	}
	ids := make([]string, 0, len(lines)-1)
	for _, line := range lines[1:] {
		ids = append(ids, strings.TrimSpace(line))
	}
	sort.Strings(ids)
	return ids
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
// repair (which keeps the wisp copy and drops other issues' edges to the
// issues copy of every such ID, so the refusal says to list those IDs and
// record their dependents first), and write nothing.
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
	for _, want := range []string{id, "issues", "wisps", "bd doctor --check=validate --fix", "--yes", "keeping the wisp copy",
		"every other issue's dependency on it", "bd sql '" + twinListingQuery + "'", "bd dep list <id> --direction=up", "bd dep add"} {
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

// TestCLI_UpdateDualPresenceRepairThenUpdate is the recovery the refusal points
// at: the update refuses first, then the existing server-mode doctor repair
// deletes the issues copy and keeps the wisp copy, after which a notes update
// succeeds and is what show returns, and --persistent leaves one durable row
// carrying it and no wisp row. The issue's child and the issue it blocks
// predate the twin, so their edges point at the issues copy: the repair's
// delete takes them with it, as the refusal warns. A second twin, which no
// update touched, has a dependent of its own, and the repair drops that edge
// too, so the refusal says to list every twin first. The edges recorded with
// bd dep list for each listed twin re-add onto its wisp copy, and the refused
// issue's survive --persistent.
func TestCLI_UpdateDualPresenceRepairThenUpdate(t *testing.T) {
	w := newDualPresenceCLI(t)
	id := w.create("durable copy", "-p", "2", "--labels", "durable-label")
	child := w.create("child", "--parent", id)
	blocked := w.create("blocked")
	w.must("dep", "add", blocked, id)
	w.addWispTwin(id)
	other := w.create("other durable copy")
	otherBlocked := w.create("blocked by the other twin")
	w.must("dep", "add", otherBlocked, other)
	w.addWispTwin(other)
	twins := []string{id, other}
	sort.Strings(twins)
	wantDependents := map[string][]string{
		id:    {blocked + " blocks", child + " parent-child"},
		other: {otherBlocked + " blocks"},
	}
	for _, pairs := range wantDependents {
		sort.Strings(pairs)
	}
	requireDependents := func(step, twin string) {
		t.Helper()
		if got := w.dependents(twin); !slices.Equal(got, wantDependents[twin]) {
			t.Fatalf("%s: bd dep list %s --direction=up = %q, want %q", step, twin, got, wantDependents[twin])
		}
	}
	rows := func(table string) string {
		return w.must("sql", "--csv", fmt.Sprintf("SELECT title, notes FROM %s WHERE id = '%s'", table, id))
	}
	// value is the single cell a one-column query prints under its header.
	value := func(query string) string {
		t.Helper()
		lines := strings.Split(strings.TrimSpace(w.must("sql", "--csv", query)), "\n")
		if len(lines) != 2 {
			t.Fatalf("bd sql %q printed %d lines, want a header and one row:\n%s", query, len(lines), strings.Join(lines, "\n"))
		}
		return strings.TrimSpace(lines[1])
	}
	requireCounts := func(step, twin, issues, wisps string) {
		t.Helper()
		if got := value(fmt.Sprintf("SELECT COUNT(*) FROM issues WHERE id = '%s'", twin)); got != issues {
			t.Fatalf("%s: issues rows for %s = %s, want %s", step, twin, got, issues)
		}
		if got := value(fmt.Sprintf("SELECT COUNT(*) FROM wisps WHERE id = '%s'", twin)); got != wisps {
			t.Fatalf("%s: wisps rows for %s = %s, want %s", step, twin, got, wisps)
		}
	}
	for _, twin := range twins {
		requireCounts("fixture", twin, "1", "1")
	}
	if !strings.Contains(rows("issues"), "durable copy") || !strings.Contains(rows("wisps"), "wisp copy") {
		t.Fatalf("fixture is not dual-present:\nissues %s\nwisps %s", rows("issues"), rows("wisps"))
	}

	// Before the repair the same update refuses, names the repair and
	// writes nothing.
	before := w.snapshot(id)
	if stdout, stderr, err := w.run("update", id, "--notes", "probe", "--force"); err == nil {
		t.Fatalf("bd update of dual-present %s exited 0 before the repair\nstdout:\n%s\nstderr:\n%s", id, stdout, stderr)
	} else if !strings.Contains(stderr, id) || !strings.Contains(stderr, "bd doctor --check=validate --fix") {
		t.Errorf("pre-repair refusal does not name %s and the repair:\n%s", id, stderr)
	}
	after := w.snapshot(id)
	for i := range before {
		if before[i] != after[i] {
			t.Errorf("pre-repair refused update changed persisted state:\nbefore %s\nafter  %s", before[i], after[i])
		}
	}

	// The refusal's steps before the repair: list every twin, not only the
	// refused one, and record each listed twin's dependents.
	listed := w.twinIDs()
	if !slices.Equal(listed, twins) {
		t.Fatalf("before the repair: bd sql %q listed %q, want %q", twinListingQuery, listed, twins)
	}
	recorded := make(map[string][]string, len(listed))
	for _, twin := range listed {
		recorded[twin] = w.dependents(twin)
		if !slices.Equal(recorded[twin], wantDependents[twin]) {
			t.Fatalf("before the repair: bd dep list %s --direction=up = %q, want %q", twin, recorded[twin], wantDependents[twin])
		}
	}

	// --yes stands in for the confirmation a terminal user gives. The fix
	// re-runs the checks and exits nonzero while any other check still warns,
	// so its exit status is not the contract; the rows are.
	if stdout, stderr, err := w.run("doctor", "--check=validate", "--fix", "--yes"); err != nil {
		t.Logf("bd doctor --check=validate --fix --yes: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if got := w.twinIDs(); len(got) != 0 {
		t.Fatalf("after the repair: bd sql %q still lists %q", twinListingQuery, got)
	}
	for _, twin := range twins {
		requireCounts("after the repair", twin, "0", "1")
	}
	if got := value(fmt.Sprintf("SELECT notes FROM wisps WHERE id = '%s'", id)); got != "wisp notes" {
		t.Fatalf("doctor repair did not keep the wisp notes: got %q", got)
	}
	if got := rows("wisps"); !strings.Contains(got, "wisp copy") {
		t.Fatalf("doctor repair did not keep the wisp copy:\n%s", got)
	}
	// The repair deletes the issues copy of every twin with foreign-key checks
	// on, and both dependency tables cascade a delete of their target, so the
	// edges that pointed at each copy go too, the untouched twin's included. A
	// repair that keeps them must change the refusal, the bd update help and
	// this check together.
	for _, twin := range twins {
		if got := w.dependents(twin); len(got) != 0 {
			t.Errorf("after the repair: bd dep list %s --direction=up = %q; the refusal says the repair removes these edges", twin, got)
		}
	}
	for _, twin := range listed {
		for _, pair := range recorded[twin] {
			dependent, depType, _ := strings.Cut(pair, " ")
			w.must("dep", "add", dependent, twin, "--type", depType)
		}
		requireDependents("after re-adding the edges", twin)
	}

	w.must("update", id, "--notes", "probe", "--force")
	var shown []struct {
		Title string `json:"title"`
		Notes string `json:"notes"`
	}
	if out := w.must("show", id, "--json"); json.Unmarshal([]byte(out), &shown) != nil || len(shown) != 1 {
		t.Fatalf("parse show output:\n%s", out)
	}
	if shown[0].Title != "wisp copy" || shown[0].Notes != "probe" {
		t.Fatalf("show after update = %+v, want the wisp copy with notes %q", shown[0], "probe")
	}

	w.must("update", id, "--persistent")
	requireCounts("after --persistent", id, "1", "0")
	if got := value(fmt.Sprintf("SELECT notes FROM issues WHERE id = '%s'", id)); got != "probe" {
		t.Errorf("after --persistent: durable notes = %q, want %q", got, "probe")
	}
	requireDependents("after --persistent", id)
}
