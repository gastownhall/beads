//go:build cgo

package main

import (
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// migratePersonalServerLeg runs bd for migrate-personal's Dolt-server leg in
// externalServerTestEnv: a home of its own, BEADS_TEST_SERVER=1 kept (the
// test-database guard's opt-in) and the ambient BEADS_DOLT_* endpoint
// dropped, so each workspace's own metadata.json picks its server and
// database.
type migratePersonalServerLeg struct {
	t   *testing.T
	bd  string
	env []string
}

// run runs bd in dir with args and extra environment, and fails the test
// unless it succeeds; it returns the combined output.
func (l *migratePersonalServerLeg) run(dir string, extraEnv []string, args ...string) string {
	l.t.Helper()
	out, err := runExternalServerBD(l.t, dir, append(slices.Clone(l.env), extraEnv...), args...)
	if err != nil {
		l.t.Fatalf("bd %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return out
}

// initRepo initializes a workspace with the given issue prefix in a new git
// repository, with args selecting its mode, and returns the repository.
func (l *migratePersonalServerLeg) initRepo(prefix string, args ...string) string {
	l.t.Helper()
	dir := l.t.TempDir()
	initGitRepoAt(l.t, dir)
	l.run(dir, nil, append([]string{"init", "--quiet", "--prefix", prefix, "--skip-hooks", "--skip-agents"}, args...)...)
	return dir
}

// initServerRepo is initRepo for a server-mode workspace on a database of its
// own on the test Dolt server, which the test drops when it ends. It returns
// the repository and the database.
func (l *migratePersonalServerLeg) initServerRepo(prefix string) (dir, database string) {
	l.t.Helper()
	database = uniqueTestDBName(l.t)
	l.t.Cleanup(func() { dropTestDatabase(database, testDoltServerPort) })
	return l.initRepo(prefix, "--server", "--external", "--server-host", "127.0.0.1",
		"--server-port", strconv.Itoa(testDoltServerPort), "--database", database), database
}

// create creates an issue with title, and args, in dir.
func (l *migratePersonalServerLeg) create(dir, title string, args ...string) string {
	l.t.Helper()
	out := l.run(dir, nil, append([]string{"create", "--json", title}, args...)...)
	return parseIssueJSON(l.t, []byte(out)).ID
}

// rows runs query through bd sql --csv in dir and returns the rows it prints
// on stdout, without the header.
func (l *migratePersonalServerLeg) rows(dir, query string) []string {
	l.t.Helper()
	cmd := exec.Command(l.bd, "sql", "--csv", query)
	cmd.Dir = dir
	cmd.Env = l.env
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		l.t.Fatalf("bd sql %q in %s: %v\n%s%s", query, dir, err, out, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return lines[1:]
}

// title returns the title of issue id in dir's database, or "" when it holds
// no such issue.
func (l *migratePersonalServerLeg) title(dir, id string) string {
	l.t.Helper()
	rows := l.rows(dir, "SELECT title FROM issues WHERE id = '"+id+"'")
	if len(rows) == 0 {
		return ""
	}
	return rows[0]
}

// commits returns the number of commits in dir's database history.
func (l *migratePersonalServerLeg) commits(dir string) int {
	l.t.Helper()
	n, err := strconv.Atoi(strings.Join(l.rows(dir, "SELECT COUNT(*) FROM dolt_log"), ""))
	if err != nil {
		l.t.Fatalf("dolt_log count in %s: %v", dir, err)
	}
	return n
}

// dirty returns the tables whose changes dir's working set holds.
func (l *migratePersonalServerLeg) dirty(dir string) []string {
	l.t.Helper()
	return l.rows(dir, "SELECT table_name FROM dolt_status ORDER BY table_name")
}

// migrate runs bd migrate-personal --yes, with args, in dir and returns its
// combined output and error.
func (l *migratePersonalServerLeg) migrate(dir string, extraEnv []string, args ...string) (string, error) {
	l.t.Helper()
	return runExternalServerBD(l.t, dir, append(slices.Clone(l.env), extraEnv...), append([]string{"migrate-personal", "--yes"}, args...)...)
}

// requireRefused asserts that migrate-personal in dir is refused with output
// containing want, reporting no move and printing no usage block, as
// requireMigratePersonalRefused does for the embedded tests.
func (l *migratePersonalServerLeg) requireRefused(dir, want string) {
	l.t.Helper()
	out, err := l.migrate(dir, nil)
	if err == nil {
		l.t.Fatalf("bd migrate-personal --yes succeeded, want it refused:\n%s", out)
	}
	if !strings.Contains(out, want) || strings.Contains(out, "Moved") || strings.Contains(out, "Usage:") {
		l.t.Errorf("bd migrate-personal --yes output, want a refusal containing %q, with no move and no usage block:\n%s", want, out)
	}
}

// TestMigratePersonalServerMode is migrate-personal's Dolt-server leg, which
// the embedded tests cannot reach: a server-mode project and a server-mode
// planning repo, each with a database of its own on one server, where the two
// databases differ only by name.
//
//   - A planning repo that is the project, by path or ".", is refused before
//     anything moves (refuseSelfPlanningRepo): there the copy upserts each
//     issue onto itself and the delete removes the only copy.
//   - An ID the planning repo already holds is refused, with both rows kept
//     (refuseIssuesAlreadyInPlanning).
//   - The move lands in the planning repo's own database even when the
//     launcher's BEADS_DOLT_SERVER_DATABASE names the project's
//     (openRoutedTargetStore pins it), and with dolt.auto-commit on it is a
//     commit in each repository.
//   - With batch the project's delete waits in its working set, while the
//     server-mode planning store commits the copy as it writes it: a batch
//     never committed leaves a duplicate, not a loss.
//   - A server-mode project moves issues into an embedded planning repo, which
//     opens in its own mode and gets the copy committed.
func TestMigratePersonalServerMode(t *testing.T) {
	skipIfNoDolt(t)
	t.Parallel()

	l := &migratePersonalServerLeg{t: t, bd: buildBDUnderTest(t), env: externalServerTestEnv(t)}
	project, projectDatabase := l.initServerRepo("mpsv")
	planning, _ := l.initServerRepo("pl")
	setContributor := func(value string) {
		t.Helper()
		l.run(project, nil, "config", "set", "routing.contributor", value)
	}
	requireTitles := func(id, wantProject, wantPlanning string) {
		t.Helper()
		if got := l.title(project, id); got != wantProject {
			t.Errorf("project issue %s title = %q, want %q", id, got, wantProject)
		}
		if got := l.title(planning, id); got != wantPlanning {
			t.Errorf("planning issue %s title = %q, want %q", id, got, wantPlanning)
		}
	}

	const title = "Server-mode personal issue"
	personal := l.create(project, title)

	for _, value := range []string{project, "."} {
		setContributor(value)
		l.requireRefused(project, "is this project's own workspace")
		requireTitles(personal, title, "")
	}

	setContributor(planning)
	const planningTitle = "Planning repo ORIGINAL row"
	l.create(planning, planningTitle, "--id", personal, "--force")
	l.requireRefused(project, "already exists in the planning repo")
	requireTitles(personal, title, planningTitle)
	l.run(planning, nil, "delete", personal, "--force")

	out, err := l.migrate(project, []string{"BEADS_DOLT_SERVER_DATABASE=" + projectDatabase})
	if err != nil || !strings.Contains(out, "Moved 1 issue") {
		t.Fatalf("bd migrate-personal --yes with the project's BEADS_DOLT_SERVER_DATABASE: err = %v, want it to move 1 issue:\n%s", err, out)
	}
	requireTitles(personal, "", title)
	for _, dir := range []string{project, planning} {
		if dirty := l.dirty(dir); len(dirty) != 0 {
			t.Errorf("%s has uncommitted changes after migrate-personal: %v", dir, dirty)
		}
	}

	const batchTitle = "Server-mode personal issue moved in batch mode"
	batched := l.create(project, batchTitle)
	projectCommits, planningCommits := l.commits(project), l.commits(planning)
	if out, err := l.migrate(project, nil, "--dolt-auto-commit", "batch"); err != nil || !strings.Contains(out, "Moved 1 issue") {
		t.Fatalf("bd migrate-personal --yes --dolt-auto-commit batch: err = %v, want it to move 1 issue:\n%s", err, out)
	}
	requireTitles(batched, "", batchTitle)
	if commits, dirty := l.commits(project), l.dirty(project); commits != projectCommits || !slices.Contains(dirty, "issues") {
		t.Errorf("project after migrate-personal in batch mode: %d commits, uncommitted %v; want %d commits and the delete left uncommitted in issues", commits, dirty, projectCommits)
	}
	if commits, dirty := l.commits(planning), l.dirty(planning); commits <= planningCommits || len(dirty) != 0 {
		t.Errorf("server-mode planning repo after migrate-personal in batch mode: %d commits, uncommitted %v; want more than %d commits and nothing uncommitted", commits, dirty, planningCommits)
	}
	l.run(project, nil, "dolt", "commit")

	embeddedPlanning := l.initRepo("pe")
	setContributor(embeddedPlanning)
	const embeddedTitle = "Server-mode personal issue moved into an embedded planning repo"
	toEmbedded := l.create(project, embeddedTitle)
	if out, err := l.migrate(project, nil); err != nil || !strings.Contains(out, "Moved 1 issue") {
		t.Fatalf("bd migrate-personal --yes into an embedded planning repo: err = %v, want it to move 1 issue:\n%s", err, out)
	}
	embeddedBeadsDir := filepath.Join(embeddedPlanning, ".beads")
	if _, dirty := planningLogCountAndDirtyTables(t, embeddedBeadsDir, "pe"); len(dirty) != 0 {
		t.Errorf("embedded planning repo has uncommitted changes after migrate-personal from a server-mode project: %v", dirty)
	}
	if moved, err := openStore(t, embeddedBeadsDir, "pe").GetIssue(t.Context(), toEmbedded); err != nil || moved.Title != embeddedTitle {
		t.Errorf("embedded planning issue %s = %v (err %v), want title %q", toEmbedded, moved, err, embeddedTitle)
	}
	if got := l.title(project, toEmbedded); got != "" {
		t.Errorf("project still holds %s (title %q) after migrate-personal into an embedded planning repo", toEmbedded, got)
	}
	if dirty := l.dirty(project); len(dirty) != 0 {
		t.Errorf("project has uncommitted changes after migrate-personal into an embedded planning repo: %v", dirty)
	}
}
