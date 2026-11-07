//go:build cgo

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// The TestEmbeddedContributorCreate* tests exercise the full contributor mode flow in
// embedded Dolt: init → contributor wizard → create. This is the exact
// scenario that triggered GH#2988 ("no database selected" when the planning
// repo's .beads directory has no metadata.json).
func TestEmbeddedContributorCreateRoutesToPlanning(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt contributor tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("create_routes_to_planning_repo", func(t *testing.T) {
		dir, planningDir := initContributor(t, bd, "cr")

		// Create an issue — should route to the planning repo, not the project.
		issue := bdCreate(t, bd, dir, "Contributor issue")
		if issue.ID == "" {
			t.Fatal("expected issue ID")
		}
		if !strings.HasPrefix(issue.ID, "cr-") {
			t.Errorf("ID should have prefix cr-, got %q", issue.ID)
		}

		// Verify the issue landed in the planning repo's embedded store.
		// The planning store database name is the sanitized prefix (same as source).
		planningBeadsDir := filepath.Join(planningDir, ".beads")
		assertIssueInStore(t, planningBeadsDir, "cr", issue.ID)

		// Verify the issue is NOT in the project's store.
		projectBeadsDir := filepath.Join(dir, ".beads")
		assertIssueNotInStore(t, projectBeadsDir, "cr", issue.ID)
	})
}

func TestEmbeddedContributorCreateShowReadsRouted(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt contributor tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("show_reads_routed_issue", func(t *testing.T) {
		dir, _ := initContributor(t, bd, "sh")

		issue := bdCreate(t, bd, dir, "Show me")
		shown := bdShow(t, bd, dir, issue.ID)
		if shown.Title != "Show me" {
			t.Errorf("title: got %q, want %q", shown.Title, "Show me")
		}
	})
}

func TestEmbeddedContributorCreateMultiple(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt contributor tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("multiple_creates_succeed", func(t *testing.T) {
		dir, planningDir := initContributor(t, bd, "mc")

		ids := make(map[string]bool)
		for i := 0; i < 3; i++ {
			issue := bdCreate(t, bd, dir, fmt.Sprintf("Issue %d", i))
			if issue.ID == "" {
				t.Fatalf("create %d: expected issue ID", i)
			}
			if ids[issue.ID] {
				t.Fatalf("create %d: duplicate ID %q", i, issue.ID)
			}
			ids[issue.ID] = true
		}

		// All three should be in the planning store.
		planningBeadsDir := filepath.Join(planningDir, ".beads")
		for id := range ids {
			assertIssueInStore(t, planningBeadsDir, "mc", id)
		}
	})
}

// TestEmbeddedContributorRoutedWritesCommitInPlanning runs, through the bd
// binary in a contributor checkout, the commands that change an issue living in
// the planning repository: each writes the change there and advances the
// planning database's HEAD, as the TestEmbeddedRoutedSiblingWritesCommitTargetHead*
// tests pin for a prefix route. While the contributor auto-route opened its
// target read-only, the embedded store refused every one of these writes.
func TestEmbeddedContributorRoutedWritesCommitInPlanning(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt contributor tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, planningDir := initContributor(t, bd, "cw")
	planningBeadsDir := filepath.Join(planningDir, ".beads")
	issue := bdCreate(t, bd, dir, "Planning issue")
	question := bdCreate(t, bd, dir, "Planning question")
	dismissed := bdCreate(t, bd, dir, "Planning request")

	for _, step := range []struct {
		name string
		args []string
	}{
		{"update --claim", []string{"update", issue.ID, "--claim"}},
		{"unclaim", []string{"unclaim", issue.ID}},
		{"comment", []string{"comment", issue.ID, "planning comment"}},
		{"close", []string{"close", issue.ID, "--reason", "done"}},
		{"human respond", []string{"human", "respond", question.ID, "--response", "answered"}},
		{"human dismiss", []string{"human", "dismiss", dismissed.ID, "--reason", "not needed"}},
	} {
		before := embeddedCurrentCommit(t, planningBeadsDir, "cw")
		bdCommand(t, bd, dir, step.args...)
		assertEmbeddedHeadAdvanced(t, planningBeadsDir, "cw", before, step.name)
	}

	planningStore := openStore(t, planningBeadsDir, "cw")
	for _, id := range []string{issue.ID, question.ID, dismissed.ID} {
		got, err := planningStore.GetIssue(t.Context(), id)
		if err != nil {
			t.Fatalf("GetIssue(%s) in the planning store: %v", id, err)
		}
		if got.Status != types.StatusClosed {
			t.Errorf("%s status in the planning store = %q, want %q", id, got.Status, types.StatusClosed)
		}
	}
	comments, err := planningStore.GetIssueComments(t.Context(), issue.ID)
	if err != nil {
		t.Fatalf("GetIssueComments: %v", err)
	}
	found := false
	for _, c := range comments {
		if c.Text == "planning comment" {
			found = true
		}
	}
	if !found {
		t.Errorf("the comment did not land in the planning store: %+v", comments)
	}
	assertIssueNotInStore(t, filepath.Join(dir, ".beads"), "cw", issue.ID)
}

// TestEmbeddedContributorRoutingMissingPlanningRepo runs the contributor
// fork flow into its failure mode: the planning repository routing selected
// is deleted (or never cloned to this machine), and every command that needs
// it — update, close, list, ready — must fail naming the target and how to
// recover, without recreating it. A later bd create initializes an empty
// planning store there and succeeds: recovery for new issues, not a
// restoration of the lost ones (GH#5957).
func TestEmbeddedContributorRoutingMissingPlanningRepo(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt contributor tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir := t.TempDir()
	initGitRepoAt(t, dir)
	for _, name := range []string{"origin", "upstream"} {
		bare := filepath.Join(t.TempDir(), name+".git")
		if out, err := gitCommand("init", "--bare", bare).CombinedOutput(); err != nil {
			t.Fatalf("git init --bare %s: %v\n%s", bare, err, out)
		}
		cmd := gitCommand("remote", "add", name, bare)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git remote add %s: %v\n%s", name, err, out)
		}
	}
	// Fork setup: routing.contributor is ~/.beads-planning (HOME is dir).
	runBDInit(t, bd, dir, "--prefix", "mp")
	planningDir := filepath.Join(dir, ".beads-planning")
	planningBeadsDir := filepath.Join(planningDir, ".beads")

	issue := bdCreate(t, bd, dir, "Planning issue that will be lost")
	assertIssueInStore(t, planningBeadsDir, "beads", issue.ID)

	// The planning repository is gone.
	if err := os.RemoveAll(planningDir); err != nil {
		t.Fatalf("RemoveAll(planningDir): %v", err)
	}

	guidance := []string{
		planningDir,
		"contributor routing selected this planning repository",
		"restore the planning repository",
		"git config beads.role maintainer",
	}
	assertGuidance := func(command, out string) {
		t.Helper()
		for _, want := range guidance {
			if !strings.Contains(out, want) {
				t.Errorf("bd %s after the planning repo was lost does not name %q:\n%s", command, want, out)
			}
		}
	}

	assertGuidance("update", bdUpdateFail(t, bd, dir, issue.ID, "--claim"))
	assertGuidance("close", bdCloseFail(t, bd, dir, issue.ID, "--reason", "done"))
	assertGuidance("list", bdListFail(t, bd, dir))

	// bd ready has no dedicated failure helper: run it inline the same way.
	readyCmd := exec.Command(bd, "ready")
	readyCmd.Dir = dir
	readyCmd.Env = bdEnv(dir)
	readyOut, err := readyCmd.CombinedOutput()
	if err == nil {
		t.Fatalf("bd ready succeeded after the planning repo was lost:\n%s", readyOut)
	}
	assertGuidance("ready", string(readyOut))

	// The failed commands must not have recreated the planning repository.
	if _, statErr := os.Stat(planningDir); !os.IsNotExist(statErr) {
		t.Fatalf("planning repo at %s was recreated by the failed commands (stat err = %v)", planningDir, statErr)
	}

	// A later create initializes an empty planning store there and succeeds;
	// the lost issue is not restored. The fresh store's database carries the
	// source prefix (T010/T012 prefix inheritance), not the deleted fork
	// setup's "beads".
	fresh := bdCreate(t, bd, dir, "Created after the planning repo was lost")
	if fresh.ID == "" {
		t.Fatal("expected an issue ID from bd create after the planning repo was lost")
	}
	assertIssueInStore(t, planningBeadsDir, "mp", fresh.ID)
	assertIssueNotInStore(t, planningBeadsDir, "mp", issue.ID)

	listOut := bdCommand(t, bd, dir, "list", "--json")
	if !strings.Contains(listOut, fresh.ID) {
		t.Errorf("bd list --json does not report the new issue %s:\n%s", fresh.ID, listOut)
	}
	if strings.Contains(listOut, issue.ID) {
		t.Errorf("bd list --json still reports the lost issue %s:\n%s", issue.ID, listOut)
	}
}

// initContributor sets up a project with contributor mode enabled and returns
// the project dir and planning dir paths. The contributor wizard runs via
// subprocess with stdin piped to answer the interactive prompts.
func initContributor(t *testing.T, bd, prefix string) (projectDir, planningDir string) {
	t.Helper()

	projectDir = t.TempDir()
	planningDir = filepath.Join(t.TempDir(), "planning")
	initGitRepoAt(t, projectDir)

	// Run bd init with --contributor flag.
	// The wizard prompts (no upstream remote):
	//   1. "Continue with contributor setup? [y/N]:" → "y"
	//   2. "Planning repo path [press Enter for default]:" → planningDir
	args := []string{"init", "--quiet", "--prefix", prefix, "--contributor"}
	cmd := exec.Command(bd, args...)
	cmd.Dir = projectDir
	cmd.Env = append(bdEnv(projectDir), "BD_NON_INTERACTIVE=0")
	cmd.Stdin = strings.NewReader("y\n" + planningDir + "\n")
	stdout, stderr, err := runCommandBuffers(t, cmd)
	if err != nil {
		t.Fatalf("bd init --contributor failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	// Sanity: planning .beads dir should exist (created by wizard).
	planningBeadsDir := filepath.Join(planningDir, ".beads")
	requireFile(t, planningBeadsDir)

	// Sanity: beads.role should be "contributor".
	roleCmd := gitCommand("config", "beads.role")
	roleCmd.Dir = projectDir
	roleOut, err := roleCmd.Output()
	if err != nil {
		t.Fatalf("git config beads.role failed: %v", err)
	}
	if got := strings.TrimSpace(string(roleOut)); got != "contributor" {
		t.Fatalf("beads.role: got %q, want %q", got, "contributor")
	}

	return projectDir, planningDir
}

// assertIssueInStore verifies that an issue with the given ID exists in the
// embedded Dolt store at beadsDir.
func assertIssueInStore(t *testing.T, beadsDir, database, issueID string) {
	t.Helper()
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), dataDir, database, "main")
	if err != nil {
		t.Fatalf("OpenSQL(%s, %s): %v", beadsDir, database, err)
	}
	defer cleanup()

	var count int
	err = db.QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM issues WHERE id = ?", issueID).Scan(&count)
	if err != nil {
		t.Fatalf("query issues for %s: %v", issueID, err)
	}
	if count == 0 {
		t.Errorf("expected issue %s in store at %s, not found", issueID, beadsDir)
	}
}

// assertIssueNotInStore verifies that an issue with the given ID does NOT
// exist in the embedded Dolt store at beadsDir.
func assertIssueNotInStore(t *testing.T, beadsDir, database, issueID string) {
	t.Helper()
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), dataDir, database, "main")
	if err != nil {
		t.Fatalf("OpenSQL(%s, %s): %v", beadsDir, database, err)
	}
	defer cleanup()

	var count int
	err = db.QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM issues WHERE id = ?", issueID).Scan(&count)
	if err != nil {
		t.Fatalf("query issues for %s: %v", issueID, err)
	}
	if count != 0 {
		t.Errorf("issue %s should NOT be in store at %s, but found %d rows", issueID, beadsDir, count)
	}
}
