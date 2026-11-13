//go:build cgo

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
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

// TestEmbeddedForkAutoContributorRoutedWrites replays GH#8039 on a default
// embedded init in a fork checkout (origin and upstream remotes, no role or
// routing chosen): init's fork auto-configuration turns on contributor
// auto-routing, so bd create lands in the planning repository, and the
// commands that change those issues must then write there too.
func TestEmbeddedForkAutoContributorRoutedWrites(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt contributor tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir := t.TempDir()
	initGitRepoAt(t, dir)
	for _, name := range []string{"origin", "upstream"} {
		bare := filepath.Join(dir, name+".git")
		if err := os.MkdirAll(bare, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", bare, err)
		}
		initBare := gitCommand("init", "--bare")
		initBare.Dir = bare
		if out, err := initBare.CombinedOutput(); err != nil {
			t.Fatalf("git init --bare %s failed: %v\n%s", bare, err, out)
		}
		addRemote := gitCommand("remote", "add", name, bare)
		addRemote.Dir = dir
		if out, err := addRemote.CombinedOutput(); err != nil {
			t.Fatalf("git remote add %s failed: %v\n%s", name, err, out)
		}
	}

	// Default init: no --contributor, no --role, no routing config set by hand.
	runBDInit(t, bd, dir, "--prefix", "fk")
	bdCommand(t, bd, dir, "setup", "opencode")

	projectBeadsDir := filepath.Join(dir, ".beads")
	planningBeadsDir := filepath.Join(dir, ".beads-planning", ".beads")

	first := bdCreate(t, bd, dir, "test1")
	second := bdCreate(t, bd, dir, "test2")

	// The planning store's database is whatever its metadata names once the
	// first create adopted it; do not assume it matches the project prefix.
	planningCfg, err := configfile.Load(planningBeadsDir)
	if err != nil {
		t.Fatalf("load planning metadata: %v", err)
	}
	if planningCfg == nil {
		t.Fatalf("planning repository %s has no metadata.json after create", planningBeadsDir)
	}
	planningDB := planningCfg.GetDoltDatabase()
	for _, issue := range []*types.Issue{first, second} {
		assertIssueInStore(t, planningBeadsDir, planningDB, issue.ID)
		assertIssueNotInStore(t, projectBeadsDir, "fk", issue.ID)
	}

	// The reported failure: bd update --claim opened the auto-routed planning
	// store read-only and the embedded store refused the write with
	// "embeddeddolt: store is read-only".
	before := embeddedCurrentCommit(t, planningBeadsDir, planningDB)
	bdCommand(t, bd, dir, "update", first.ID, "--claim")
	assertEmbeddedHeadAdvanced(t, planningBeadsDir, planningDB, before, "update --claim")

	// bd dep add failed the same way in the report.
	before = embeddedCurrentCommit(t, planningBeadsDir, planningDB)
	bdCommand(t, bd, dir, "dep", "add", first.ID, second.ID)
	assertEmbeddedHeadAdvanced(t, planningBeadsDir, planningDB, before, "dep add")

	// Read back only after every bd subprocess has run: the inspection handle
	// holds the planning store open until the test ends.
	planningStore := openStore(t, planningBeadsDir, planningDB)
	claimed, err := planningStore.GetIssue(t.Context(), first.ID)
	if err != nil {
		t.Fatalf("GetIssue(%s) in the planning store: %v", first.ID, err)
	}
	if claimed.Status != types.StatusInProgress {
		t.Errorf("%s status in the planning store = %q, want %q", first.ID, claimed.Status, types.StatusInProgress)
	}
	if claimed.Assignee == "" {
		t.Errorf("%s has no assignee in the planning store after --claim", first.ID)
	}
	deps, err := planningStore.GetDependencyRecords(t.Context(), first.ID)
	if err != nil {
		t.Fatalf("GetDependencyRecords(%s) in the planning store: %v", first.ID, err)
	}
	blocked := false
	for _, dep := range deps {
		if dep.DependsOnID == second.ID && dep.Type == types.DepBlocks {
			blocked = true
		}
	}
	if !blocked {
		t.Errorf("planning store has no blocks dependency %s -> %s: %+v", first.ID, second.ID, deps)
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
