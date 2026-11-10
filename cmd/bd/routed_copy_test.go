//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// copyFixture is a maintainer checkout whose commands auto-route to a
// planning workspace, and which holds `bd repo sync` copies of some of the
// planning issues. Both workspaces are embedded.
type copyFixture struct {
	project          storage.DoltStorage
	repoDir          string
	projectBeadsDir  string
	planningDir      string
	planningBeadsDir string
}

const (
	copyFixtureProjectDB  = "proj"
	copyFixturePlanningDB = "plan"
)

// newCopyFixture builds a copyFixture: planning holds planningIDs, and the
// project holds a copy of each of copiedIDs (a subset of planningIDs) as bd
// repo sync imports it, source_repo naming the planning repository, plus its
// own projectIDs. It installs the project as the command store and changes
// into the project checkout.
func newCopyFixture(t *testing.T, planningIDs, copiedIDs, projectIDs []string) *copyFixture {
	t.Helper()
	saveStorageMode(t)
	saveAndRestoreGlobals(t)
	ensureCleanGlobalState(t)
	serverMode, proxiedServerMode = false, false
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	t.Setenv("NO_COLOR", "1")
	initConfigForTest(t)
	neutralizeRoutingModeEnv(t)
	ctx := context.Background()

	actorMu.Lock()
	savedActor, savedPending := actor, actorGitFallbackPending
	actor, actorGitFallbackPending = "alice", false
	actorMu.Unlock()
	savedRootCtx, savedJSON, savedQuiet, savedDidWrite := rootCtx, jsonOutput, quietFlag, commandDidWrite.Load()
	rootCtx, jsonOutput, quietFlag = ctx, false, false
	t.Cleanup(func() {
		actorMu.Lock()
		actor, actorGitFallbackPending = savedActor, savedPending
		actorMu.Unlock()
		rootCtx, jsonOutput, quietFlag = savedRootCtx, savedJSON, savedQuiet
		commandDidWrite.Store(savedDidWrite)
		commandDeletedIssueIDs.reset()
	})

	tmpDir := t.TempDir()
	f := &copyFixture{
		repoDir:     filepath.Join(tmpDir, "repo"),
		planningDir: filepath.Join(tmpDir, "planning"),
	}
	f.projectBeadsDir = filepath.Join(f.repoDir, ".beads")
	f.planningBeadsDir = filepath.Join(f.planningDir, ".beads")

	runCmd(t, tmpDir, "git", "init", f.repoDir)
	runCmd(t, f.repoDir, "git", "config", "core.hooksPath", ".git/hooks")
	runCmd(t, f.repoDir, "git", "config", "beads.role", "maintainer")

	writeEmbeddedMetadata(t, f.planningBeadsDir, copyFixturePlanningDB)
	seedEmbeddedSchema(t, f.planningDir, copyFixturePlanningDB)
	planning, err := embeddeddolt.Open(ctx, f.planningBeadsDir, copyFixturePlanningDB, "main")
	if err != nil {
		t.Fatalf("open planning: %v", err)
	}
	if err := planning.SetConfig(ctx, "issue_prefix", "pl"); err != nil {
		t.Fatalf("set planning issue_prefix: %v", err)
	}
	originals := map[string]*types.Issue{}
	for _, id := range planningIDs {
		issue := &types.Issue{ID: id, Title: "planning " + id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := planning.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("seed planning %s: %v", id, err)
		}
		stored, err := planning.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("read planning %s: %v", id, err)
		}
		originals[id] = stored
	}
	if err := planning.Commit(ctx, "seed"); err != nil {
		t.Fatalf("commit planning seed: %v", err)
	}
	if err := planning.Close(); err != nil {
		t.Fatalf("close planning: %v", err)
	}

	writeEmbeddedMetadata(t, f.projectBeadsDir, copyFixtureProjectDB)
	seedEmbeddedSchema(t, f.repoDir, copyFixtureProjectDB)
	project, err := embeddeddolt.Open(ctx, f.projectBeadsDir, copyFixtureProjectDB, "main")
	if err != nil {
		t.Fatalf("open project: %v", err)
	}
	t.Cleanup(func() { _ = project.Close() })
	for key, value := range map[string]string{
		"issue_prefix":       "pr",
		"routing.mode":       "auto",
		"routing.maintainer": f.planningDir,
	} {
		if err := project.SetConfig(ctx, key, value); err != nil {
			t.Fatalf("set project %s: %v", key, err)
		}
	}
	var copies []*types.Issue
	for _, id := range copiedIDs {
		c := *originals[id]
		c.SourceRepo = f.planningDir
		copies = append(copies, &c)
	}
	if len(copies) > 0 {
		// As bd repo sync imports them (cmd/bd/repo.go).
		if err := project.CreateIssuesWithFullOptions(ctx, copies, "repo-sync", storage.BatchCreateOptions{SkipPrefixValidation: true}); err != nil {
			t.Fatalf("import copies: %v", err)
		}
	}
	for _, id := range projectIDs {
		issue := &types.Issue{ID: id, Title: "project " + id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := project.CreateIssuesWithFullOptions(ctx, []*types.Issue{issue}, "test", storage.BatchCreateOptions{SkipPrefixValidation: true}); err != nil {
			t.Fatalf("seed project %s: %v", id, err)
		}
	}
	if err := project.Commit(ctx, "seed"); err != nil {
		t.Fatalf("commit project seed: %v", err)
	}
	f.project = project

	store = project
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()
	dbPath = filepath.Join(f.projectBeadsDir, "dolt")
	t.Setenv("BEADS_DIR", f.projectBeadsDir)
	t.Chdir(f.repoDir)
	resetRepoContextCachesForTest(t)
	return f
}

// planningIssue returns id as the planning workspace's HEAD holds it, or nil
// when it holds none.
func (f *copyFixture) planningIssue(t *testing.T, id string) *types.Issue {
	t.Helper()
	return headIssueOrNil(t, f.planningBeadsDir, copyFixturePlanningDB, id)
}

// projectIssue returns id as the project store holds it, or nil.
func (f *copyFixture) projectIssue(t *testing.T, id string) *types.Issue {
	t.Helper()
	issue, err := f.project.GetIssue(t.Context(), id)
	if err != nil {
		if isNotFoundErr(err) {
			return nil
		}
		t.Fatalf("read project %s: %v", id, err)
	}
	return issue
}

// requireCopyUnchanged fails unless the project still holds its open,
// unmodified copy of id.
func (f *copyFixture) requireCopyUnchanged(t *testing.T, id string) {
	t.Helper()
	c := f.projectIssue(t, id)
	if c == nil {
		t.Fatalf("the project copy of %s is gone", id)
	}
	if c.Status != types.StatusOpen || c.Priority != 2 || c.Assignee != "" {
		t.Fatalf("the project copy of %s changed: status %s, priority %d, assignee %q", id, c.Status, c.Priority, c.Assignee)
	}
}

// headIssueOrNil returns id as the HEAD commit of beadsDir's embedded
// database holds it, or nil when it holds none.
func headIssueOrNil(t *testing.T, beadsDir, database, id string) *types.Issue {
	t.Helper()
	s, err := embeddeddolt.Open(t.Context(), beadsDir, database, "main")
	if err != nil {
		t.Fatalf("open %s: %v", beadsDir, err)
	}
	defer func() { _ = s.Close() }()
	issue, err := s.AsOf(t.Context(), id, "HEAD")
	if err != nil {
		if isNotFoundErr(err) {
			return nil
		}
		t.Fatalf("read %s at HEAD: %v", id, err)
	}
	return issue
}

// runCopyCmd runs cmd in-process with flags set for this run only, and
// returns its streams and exit status.
func runCopyCmd(t *testing.T, cmd *cobra.Command, flags map[string]string, args ...string) runResult {
	t.Helper()
	defer resetCommandFlagsToDefaults(cmd)
	for name, value := range flags {
		if err := cmd.Flags().Set(name, value); err != nil {
			t.Fatalf("set --%s=%q: %v", name, value, err)
		}
	}
	var err error
	stdout, stderr := captureStdio(t, func() { err = cmd.RunE(cmd, args) })
	return runResult{stdout: stdout, stderr: stderr, exitCode: parityExitCode(err), err: err}
}

// TestCloseOfLocalCopyClosesAutoRouteOwner pins that bd close of an issue the
// project holds only as a bd repo sync copy of an issue in its auto-route
// target closes that issue, the one bd list shows, and leaves the copy as it
// is. Closing the copy instead reported success while bd list kept showing
// the issue open (GH#6058).
func TestCloseOfLocalCopyClosesAutoRouteOwner(t *testing.T) {
	f := newCopyFixture(t, []string{"pl-x"}, []string{"pl-x"}, nil)

	res := runCopyCmd(t, closeCmd, map[string]string{"reason": "done"}, "pl-x")
	if res.exitCode != 0 {
		t.Fatalf("bd close pl-x: exit %d, err %v\nstderr: %s", res.exitCode, res.err, res.stderr)
	}
	owner := f.planningIssue(t, "pl-x")
	if owner == nil || owner.Status != types.StatusClosed {
		t.Fatalf("planning pl-x after bd close = %v, want it closed at HEAD", issueStatusOrNil(owner))
	}
	f.requireCopyUnchanged(t, "pl-x")
	if !strings.Contains(res.stdout, "pl-x") {
		t.Fatalf("bd close stdout = %q, want it to report pl-x", res.stdout)
	}
}

// issueStatusOrNil describes issue by its status, for a failure message.
func issueStatusOrNil(issue *types.Issue) string {
	if issue == nil {
		return "absent"
	}
	return string(issue.Status)
}

// TestDeleteOfLocalCopyDeletesAutoRouteOwner pins that bd delete --force of an
// issue the project holds only as a copy of an issue in its auto-route target
// deletes that issue, so routed bd list no longer shows it, keeps the copy,
// and says once on stderr that the copy remains, unless --quiet.
func TestDeleteOfLocalCopyDeletesAutoRouteOwner(t *testing.T) {
	f := newCopyFixture(t, []string{"pl-x", "pl-y"}, []string{"pl-x", "pl-y"}, nil)
	const note = "this project still holds the copy bd repo sync imported"

	res := runCopyCmd(t, deleteCmd, map[string]string{"force": "true"}, "pl-x")
	if res.exitCode != 0 {
		t.Fatalf("bd delete pl-x --force: exit %d, err %v\nstderr: %s", res.exitCode, res.err, res.stderr)
	}
	if owner := f.planningIssue(t, "pl-x"); owner != nil {
		t.Fatalf("planning pl-x after bd delete = %s, want it gone at HEAD", issueStatusOrNil(owner))
	}
	f.requireCopyUnchanged(t, "pl-x")
	if got := strings.Count(res.stderr, note); got != 1 {
		t.Fatalf("bd delete stderr holds the retained-copy note %d times, want once:\n%s", got, res.stderr)
	}
	if !strings.Contains(res.stderr, "bd delete pl-x --force") {
		t.Fatalf("the retained-copy note does not name the local cleanup:\n%s", res.stderr)
	}

	quietFlag = true
	res = runCopyCmd(t, deleteCmd, map[string]string{"force": "true"}, "pl-y")
	quietFlag = false
	if res.exitCode != 0 {
		t.Fatalf("bd delete pl-y --force --quiet: exit %d, err %v\nstderr: %s", res.exitCode, res.err, res.stderr)
	}
	if owner := f.planningIssue(t, "pl-y"); owner != nil {
		t.Fatalf("planning pl-y after bd delete = %s, want it gone at HEAD", issueStatusOrNil(owner))
	}
	f.requireCopyUnchanged(t, "pl-y")
	if strings.Contains(res.stderr, note) {
		t.Fatalf("--quiet bd delete printed the retained-copy note:\n%s", res.stderr)
	}
}

// TestDeleteRetainedCopyNoteOnlyAfterACopyDelete pins when bd delete prints
// the retained-copy note: on stderr after the owner of a local copy deleted
// it, --json too, with stdout the same JSON a delete prints anyway; never for
// a preview, which deletes nothing, nor for an issue only the owner holds.
func TestDeleteRetainedCopyNoteOnlyAfterACopyDelete(t *testing.T) {
	f := newCopyFixture(t, []string{"pl-x", "pl-y", "pl-z"}, []string{"pl-x", "pl-y"}, nil)
	const note = "this project still holds the copy bd repo sync imported"

	jsonOutput = true
	res := runCopyCmd(t, deleteCmd, map[string]string{"force": "true"}, "pl-x")
	jsonOutput = false
	if res.exitCode != 0 {
		t.Fatalf("bd delete pl-x --force --json: exit %d, err %v\nstderr: %s", res.exitCode, res.err, res.stderr)
	}
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(res.stdout), &out); err != nil {
		t.Fatalf("bd delete --json stdout is not one JSON object: %v\n%s", err, res.stdout)
	}
	if out["deleted"] != "pl-x" || out["schema_version"] == nil || len(out) != 4 {
		t.Fatalf("bd delete --json stdout = %v, want the single-issue keys with deleted pl-x", out)
	}
	if strings.Contains(res.stdout, note) {
		t.Fatalf("bd delete --json printed the retained-copy note on stdout:\n%s", res.stdout)
	}
	if got := strings.Count(res.stderr, note); got != 1 {
		t.Fatalf("bd delete --json stderr holds the retained-copy note %d times, want once:\n%s", got, res.stderr)
	}
	if f.planningIssue(t, "pl-x") != nil {
		t.Fatal("planning pl-x survived bd delete --force --json")
	}

	res = runCopyCmd(t, deleteCmd, nil, "pl-y")
	if res.exitCode != 0 {
		t.Fatalf("bd delete pl-y (preview): exit %d, err %v\nstderr: %s", res.exitCode, res.err, res.stderr)
	}
	if strings.Contains(res.stderr, note) || strings.Contains(res.stdout, note) {
		t.Fatalf("a delete preview printed the retained-copy note:\nstdout: %s\nstderr: %s", res.stdout, res.stderr)
	}
	if f.planningIssue(t, "pl-y") == nil {
		t.Fatal("a delete preview deleted planning pl-y")
	}
	f.requireCopyUnchanged(t, "pl-y")

	res = runCopyCmd(t, deleteCmd, map[string]string{"force": "true"}, "pl-z")
	if res.exitCode != 0 {
		t.Fatalf("bd delete pl-z --force: exit %d, err %v\nstderr: %s", res.exitCode, res.err, res.stderr)
	}
	if f.planningIssue(t, "pl-z") != nil {
		t.Fatal("planning pl-z survived bd delete --force")
	}
	if strings.Contains(res.stderr, note) {
		t.Fatalf("deleting an issue only the owner holds printed the retained-copy note:\n%s", res.stderr)
	}
}
