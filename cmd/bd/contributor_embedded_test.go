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

// TestEmbeddedForkAutoContributorRoutedWrites covers the fork auto-configured
// contributor flow from GH#8039: default bd init in a git repository with
// origin and upstream remotes configures contributor routing on its own (no
// --contributor wizard, no hand-set routing config), every created issue
// lands in the planning repository's database, and the routed mutations —
// update --claim and dep add — commit there through the writable auto-route
// open. v1.3.2 opened the auto-routed target read-only even for write-intent
// callers, so those mutations failed with "embeddeddolt: store is read-only"
// while bd create, which writes the planning store directly, kept working
// (fixed for every auto-route rule by #7622). Unlike
// TestEmbeddedContributorRoutedWritesCommitInPlanning, this test must not
// configure routing itself: it exercises fork detection.
func TestEmbeddedForkAutoContributorRoutedWrites(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt contributor tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	dir := t.TempDir()
	initGitRepoAt(t, dir)

	// Bare origin and upstream remotes — the fork shape fork detection keys
	// on (detectForkSetup reads the upstream remote).
	origin := filepath.Join(dir, "origin.git")
	upstream := filepath.Join(dir, "upstream.git")
	for _, bareRepo := range []string{origin, upstream} {
		if err := os.MkdirAll(bareRepo, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", bareRepo, err)
		}
		cmd := gitCommand("init", "--bare")
		cmd.Dir = bareRepo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init --bare %s failed: %v\n%s", bareRepo, err, out)
		}
	}
	for name, url := range map[string]string{"origin": origin, "upstream": upstream} {
		cmd := gitCommand("remote", "add", name, url)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git remote add %s failed: %v\n%s", name, err, out)
		}
	}

	// Default init with no role flag, then setup, the reporter's sequence.
	// bdEnv pins HOME inside dir, so the planning repository auto-configuration
	// creates lands at dir/.beads-planning and nothing leaks to the host.
	runBDInit(t, bd, dir, "--prefix", "fk")
	bdSetup(t, bd, dir, "opencode")

	first := bdCreate(t, bd, dir, "Fork planning issue")
	second := bdCreate(t, bd, dir, "Fork second issue")
	if !strings.HasPrefix(first.ID, "fk-") || !strings.HasPrefix(second.ID, "fk-") {
		t.Fatalf("expected fk- prefixed issue IDs, got %q and %q", first.ID, second.ID)
	}

	// The first routed create adopts the planning store fork init seeded and
	// writes the metadata.json naming its database. Read the name back rather
	// than assuming it equals the project prefix (GH#2988).
	planningBeadsDir := filepath.Join(dir, ".beads-planning", ".beads")
	cfg, err := configfile.Load(planningBeadsDir)
	if err != nil {
		t.Fatalf("load %s: %v", planningBeadsDir, err)
	}
	planningDatabase := ""
	if cfg != nil {
		planningDatabase = cfg.GetDoltDatabase()
	}
	if planningDatabase == "" {
		t.Fatal("planning metadata names no dolt_database")
	}

	// Both issues live in the planning database; neither in the project's
	// own fk database.
	assertIssueInStore(t, planningBeadsDir, planningDatabase, first.ID)
	assertIssueInStore(t, planningBeadsDir, planningDatabase, second.ID)
	projectBeadsDir := filepath.Join(dir, ".beads")
	assertIssueNotInStore(t, projectBeadsDir, "fk", first.ID)
	assertIssueNotInStore(t, projectBeadsDir, "fk", second.ID)
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
