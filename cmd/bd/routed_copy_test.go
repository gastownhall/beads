//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
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
// own projectIDs. A copied id planning does not hold is copied as if planning
// had held it then. Each of editCopies is applied to every copy before the
// import. It installs the project as the command store and changes into the
// project checkout.
func newCopyFixture(t *testing.T, planningIDs, copiedIDs, projectIDs []string, editCopies ...func(f *copyFixture, c *types.Issue)) *copyFixture {
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
		original := originals[id]
		if original == nil {
			original = &types.Issue{ID: id, Title: "planning " + id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, CreatedAt: time.Now().UTC()}
		}
		c := *original
		c.SourceRepo = f.planningDir
		for _, edit := range editCopies {
			edit(f, &c)
		}
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

// TestWritesToLocalCopyLandInAutoRouteOwner pins that bd update --claim, bd
// update and bd comment of an issue the project holds only as a copy of an
// issue in its auto-route target change that issue, durably, and leave the
// copy as it is.
func TestWritesToLocalCopyLandInAutoRouteOwner(t *testing.T) {
	f := newCopyFixture(t, []string{"pl-c", "pl-u", "pl-m"}, []string{"pl-c", "pl-u", "pl-m"}, nil)

	for _, step := range []struct {
		cmd   *cobra.Command
		flags map[string]string
		args  []string
	}{
		{updateCmd, map[string]string{"claim": "true"}, []string{"pl-c"}},
		{updateCmd, map[string]string{"priority": "0"}, []string{"pl-u"}},
		{commentCmd, nil, []string{"pl-m", "routed comment"}},
	} {
		res := runCopyCmd(t, step.cmd, step.flags, step.args...)
		if res.exitCode != 0 {
			t.Fatalf("bd %s %v: exit %d, err %v\nstderr: %s", step.cmd.Name(), step.args, res.exitCode, res.err, res.stderr)
		}
	}

	if got := f.planningIssue(t, "pl-c"); got == nil || got.Assignee != "alice" || got.Status != types.StatusInProgress {
		t.Errorf("planning pl-c after bd update --claim = %s, want claimed by alice at HEAD", issueStatusOrNil(got))
	}
	if got := f.planningIssue(t, "pl-u"); got == nil || got.Priority != 0 {
		t.Errorf("planning pl-u after bd update --priority 0 = %s, want priority 0 at HEAD", issueStatusOrNil(got))
	}
	if !planningHeadHasComment(t, f, "pl-m", "routed comment") {
		t.Errorf("the comment on pl-m did not land in the planning workspace's HEAD")
	}
	for _, id := range []string{"pl-c", "pl-u", "pl-m"} {
		f.requireCopyUnchanged(t, id)
	}
	comments, err := f.project.GetIssueComments(t.Context(), "pl-m")
	if err != nil {
		t.Fatalf("read project comments: %v", err)
	}
	if len(comments) != 0 {
		t.Errorf("the project copy of pl-m gained comments %+v", comments)
	}
}

// planningHeadHasComment reports whether the planning workspace holds a
// committed comment on id reading text: the comment is in its store and its
// working set has nothing uncommitted.
func planningHeadHasComment(t *testing.T, f *copyFixture, id, text string) bool {
	t.Helper()
	s, err := embeddeddolt.Open(t.Context(), f.planningBeadsDir, copyFixturePlanningDB, "main")
	if err != nil {
		t.Fatalf("open planning: %v", err)
	}
	defer func() { _ = s.Close() }()
	status, err := s.Status(t.Context())
	if err != nil {
		t.Fatalf("planning status: %v", err)
	}
	if len(status.Staged)+len(status.Unstaged) != 0 {
		t.Errorf("planning workspace has uncommitted changes %+v", status)
	}
	comments, err := s.GetIssueComments(t.Context(), id)
	if err != nil {
		t.Fatalf("read planning comments on %s: %v", id, err)
	}
	for _, c := range comments {
		if c.Text == text {
			return true
		}
	}
	return false
}

// updatePlanning applies updates to id in the planning workspace and commits
// them, as work done there after bd repo sync took its copy.
func (f *copyFixture) updatePlanning(t *testing.T, id string, updates map[string]interface{}) {
	t.Helper()
	s, err := embeddeddolt.Open(t.Context(), f.planningBeadsDir, copyFixturePlanningDB, "main")
	if err != nil {
		t.Fatalf("open planning: %v", err)
	}
	defer func() { _ = s.Close() }()
	if err := s.UpdateIssue(t.Context(), id, updates, "bob"); err != nil {
		t.Fatalf("update planning %s: %v", id, err)
	}
	if err := s.Commit(t.Context(), "planning work"); err != nil {
		t.Fatalf("commit planning work: %v", err)
	}
}

// TestShowOfLocalCopyReadsAutoRouteOwner pins that bd show of an issue the
// project holds only as a copy of an issue in its auto-route target reports
// the target's current state, not the stale copy.
func TestShowOfLocalCopyReadsAutoRouteOwner(t *testing.T) {
	f := newCopyFixture(t, []string{"pl-s"}, []string{"pl-s"}, nil)
	f.updatePlanning(t, "pl-s", map[string]interface{}{
		"status":   string(types.StatusInProgress),
		"priority": 0,
		"assignee": "bob",
	})

	jsonOutput = true
	res := runCopyCmd(t, showCmd, nil, "pl-s")
	if res.exitCode != 0 {
		t.Fatalf("bd show pl-s: exit %d, err %v\nstderr: %s", res.exitCode, res.err, res.stderr)
	}
	var shown []struct {
		ID       string       `json:"id"`
		Status   types.Status `json:"status"`
		Priority int          `json:"priority"`
		Assignee string       `json:"assignee"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &shown); err != nil {
		t.Fatalf("parse bd show --json: %v\n%s", err, res.stdout)
	}
	if len(shown) != 1 || shown[0].ID != "pl-s" {
		t.Fatalf("bd show --json pl-s = %+v, want pl-s alone", shown)
	}
	if got := shown[0]; got.Status != types.StatusInProgress || got.Priority != 0 || got.Assignee != "bob" {
		t.Errorf("bd show pl-s = status %s, priority %d, assignee %q; want the planning workspace's in_progress, 0, \"bob\"", got.Status, got.Priority, got.Assignee)
	}
	f.requireCopyUnchanged(t, "pl-s")
}

// TestLocalRowsFailingCopyIdentityStayLocal pins that a local row that is not
// an identifiable copy of the issue the auto-route target holds under its id
// resolves locally: a write changes the local row and leaves the target's
// row as it is.
func TestLocalRowsFailingCopyIdentityStayLocal(t *testing.T) {
	for _, tc := range []struct {
		name     string
		planning []string
		edit     func(f *copyFixture, c *types.Issue)
		after    func(t *testing.T, f *copyFixture)
	}{
		{
			name:     "source repo names another repository",
			planning: []string{"pl-a"},
			edit: func(f *copyFixture, c *types.Issue) {
				c.SourceRepo = filepath.Join(filepath.Dir(f.planningDir), "elsewhere")
			},
		},
		{
			name:     "no source repo",
			planning: []string{"pl-a"},
			edit:     func(_ *copyFixture, c *types.Issue) { c.SourceRepo = "" },
		},
		{
			name:     "created_at differs",
			planning: []string{"pl-a"},
			edit:     func(_ *copyFixture, c *types.Issue) { c.CreatedAt = c.CreatedAt.Add(-time.Hour) },
		},
		{
			name:     "target lacks the id",
			planning: []string{"pl-b"},
		},
		{
			name:     "no route",
			planning: []string{"pl-a"},
			after: func(t *testing.T, f *copyFixture) {
				if err := f.project.SetConfig(t.Context(), "routing.mode", ""); err != nil {
					t.Fatalf("clear routing.mode: %v", err)
				}
			},
		},
		{
			name:     "target is this workspace",
			planning: []string{"pl-a"},
			edit:     func(f *copyFixture, c *types.Issue) { c.SourceRepo = f.repoDir },
			after: func(t *testing.T, f *copyFixture) {
				if err := f.project.SetConfig(t.Context(), "routing.maintainer", f.repoDir); err != nil {
					t.Fatalf("route to this workspace: %v", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var edits []func(*copyFixture, *types.Issue)
			if tc.edit != nil {
				edits = append(edits, tc.edit)
			}
			f := newCopyFixture(t, tc.planning, []string{"pl-a"}, nil, edits...)
			if tc.after != nil {
				tc.after(t, f)
			}

			res := runCopyCmd(t, updateCmd, map[string]string{"priority": "0"}, "pl-a")
			if res.exitCode != 0 {
				t.Fatalf("bd update pl-a: exit %d, err %v\nstderr: %s", res.exitCode, res.err, res.stderr)
			}
			if got := f.projectIssue(t, "pl-a"); got == nil || got.Priority != 0 {
				t.Errorf("project pl-a after bd update --priority 0 = %s, want the local row at priority 0", issuePriorityOrNil(got))
			}
			if got := f.planningIssue(t, "pl-a"); got != nil && got.Priority != 2 {
				t.Errorf("planning pl-a changed to priority %d; a local row must not write the target's row", got.Priority)
			}

			res = runCopyCmd(t, deleteCmd, map[string]string{"force": "true"}, "pl-a")
			if res.exitCode != 0 {
				t.Fatalf("bd delete pl-a --force: exit %d, err %v\nstderr: %s", res.exitCode, res.err, res.stderr)
			}
			if got := f.projectIssue(t, "pl-a"); got != nil {
				t.Errorf("project pl-a survived bd delete --force; the local row should be the one deleted")
			}
			if strings.Contains(res.stderr, "still holds the copy") {
				t.Errorf("bd delete of a local row printed the retained-copy note:\n%s", res.stderr)
			}
			if slices.Contains(tc.planning, "pl-a") && f.planningIssue(t, "pl-a") == nil {
				t.Errorf("bd delete of a local row deleted the target's pl-a")
			}
		})
	}
}

func issuePriorityOrNil(issue *types.Issue) string {
	if issue == nil {
		return "absent"
	}
	return fmt.Sprintf("priority %d", issue.Priority)
}

// TestCopyIdentityEdgeCases pins the rest of the copy identity a redirect
// needs, with controls that redirect so the local cases are not vacuous: a
// source_repo naming the target through a symlink, or a target routing.default
// selects, still redirects; a "." source_repo stays local. A row the target
// never held stays local for reads too, a native row stays writable with the
// target gone, and a partial id resolves locally once, then exactly in the
// target, while exact resolution still refuses the abbreviation.
func TestCopyIdentityEdgeCases(t *testing.T) {
	for _, tc := range []struct {
		name      string
		edit      func(f *copyFixture, c *types.Issue)
		after     func(t *testing.T, f *copyFixture)
		wantOwner bool
	}{
		{
			name:      "identifiable copy (control)",
			wantOwner: true,
		},
		{
			name: "source repo names the target through a symlink (control)",
			edit: func(f *copyFixture, c *types.Issue) {
				alias := f.planningDir + "-alias"
				if err := os.Symlink(f.planningDir, alias); err != nil {
					t.Fatalf("symlink %s -> %s: %v", alias, f.planningDir, err)
				}
				c.SourceRepo = alias
			},
			wantOwner: true,
		},
		{
			name: "routing.default selects the target (control)",
			after: func(t *testing.T, f *copyFixture) {
				for key, value := range map[string]string{"routing.maintainer": "", "routing.default": f.planningDir} {
					if err := f.project.SetConfig(t.Context(), key, value); err != nil {
						t.Fatalf("set %s: %v", key, err)
					}
				}
			},
			wantOwner: true,
		},
		{
			name: "dot source repo",
			edit: func(_ *copyFixture, c *types.Issue) { c.SourceRepo = "." },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var edits []func(*copyFixture, *types.Issue)
			if tc.edit != nil {
				edits = append(edits, tc.edit)
			}
			f := newCopyFixture(t, []string{"pl-a"}, []string{"pl-a"}, nil, edits...)
			if tc.after != nil {
				tc.after(t, f)
			}

			res := runCopyCmd(t, updateCmd, map[string]string{"priority": "0"}, "pl-a")
			if res.exitCode != 0 {
				t.Fatalf("bd update pl-a: exit %d, err %v\nstderr: %s", res.exitCode, res.err, res.stderr)
			}
			owner, local := f.planningIssue(t, "pl-a"), f.projectIssue(t, "pl-a")
			if tc.wantOwner {
				if owner == nil || owner.Priority != 0 {
					t.Fatalf("planning pl-a after bd update --priority 0 = %s, want priority 0 at HEAD", issuePriorityOrNil(owner))
				}
				f.requireCopyUnchanged(t, "pl-a")
				return
			}
			if local == nil || local.Priority != 0 {
				t.Errorf("project pl-a after bd update --priority 0 = %s, want the local row at priority 0", issuePriorityOrNil(local))
			}
			if owner == nil || owner.Priority != 2 {
				t.Errorf("planning pl-a after a local update = %s, want it untouched at priority 2", issuePriorityOrNil(owner))
			}
		})
	}

	t.Run("a row the target never held reads locally", func(t *testing.T) {
		f := newCopyFixture(t, []string{"pl-a"}, []string{"pl-c"}, nil)
		for _, resolve := range []struct {
			what string
			call func() (*RoutedResult, error)
		}{
			{"read", func() (*RoutedResult, error) { return resolveAndGetIssueWithRouting(t.Context(), f.project, "pl-c") }},
			{"mutation", func() (*RoutedResult, error) { return resolveAndGetIssueForMutation(t.Context(), f.project, "pl-c") }},
		} {
			result, err := resolve.call()
			if err != nil {
				t.Fatalf("%s resolution of pl-c: %v", resolve.what, err)
			}
			if result.Routed || result.copyOwner != "" {
				t.Errorf("%s resolution of pl-c routed to %q; want the local row", resolve.what, result.copyOwner)
			}
			result.Close()
		}
	})

	t.Run("a native row stays writable with the target gone", func(t *testing.T) {
		f := newCopyFixture(t, []string{"pl-a"}, []string{"pl-a"}, []string{"pr-n"})
		if err := os.RemoveAll(f.planningDir); err != nil {
			t.Fatalf("remove the planning workspace: %v", err)
		}
		res := runCopyCmd(t, closeCmd, map[string]string{"reason": "done"}, "pr-n")
		if res.exitCode != 0 {
			t.Fatalf("bd close pr-n with the target gone: exit %d, err %v\nstderr: %s", res.exitCode, res.err, res.stderr)
		}
		if got := f.projectIssue(t, "pr-n"); got == nil || got.Status != types.StatusClosed {
			t.Fatalf("project pr-n after bd close = %s, want closed", issueStatusOrNil(got))
		}
	})

	t.Run("a partial id resolves locally once, then exactly in the target", func(t *testing.T) {
		f := newCopyFixture(t, []string{"pl-abc"}, []string{"pl-abc"}, nil)
		res := runCopyCmd(t, updateCmd, map[string]string{"priority": "0"}, "pl-ab")
		if res.exitCode != 0 {
			t.Fatalf("bd update pl-ab: exit %d, err %v\nstderr: %s", res.exitCode, res.err, res.stderr)
		}
		if owner := f.planningIssue(t, "pl-abc"); owner == nil || owner.Priority != 0 {
			t.Fatalf("planning pl-abc after bd update pl-ab --priority 0 = %s, want priority 0 at HEAD", issuePriorityOrNil(owner))
		}
		f.requireCopyUnchanged(t, "pl-abc")

		if _, err := resolveAndGetIssueForMutationExact(t.Context(), f.project, "pl-ab"); !errors.Is(err, utils.ErrAbbreviatedIDNotAllowed) {
			t.Fatalf("exact resolution of pl-ab: err %v, want utils.ErrAbbreviatedIDNotAllowed", err)
		}
	})
}

// TestSameCreatedAtIdentifiesOnlyKnownEqualSeconds pins the creation-time half
// of the copy identity: equal to the second a DATETIME keeps, in any zone,
// and never on a zero time, which no stored row can hold.
func TestSameCreatedAtIdentifiesOnlyKnownEqualSeconds(t *testing.T) {
	created := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		a, b time.Time
		want bool
	}{
		{"same instant", created, created, true},
		{"sub-second apart", created.Add(500 * time.Millisecond), created, true},
		{"same instant in another zone", created.In(time.FixedZone("UTC+5", 5*3600)), created, true},
		{"a minute apart", created.Add(time.Minute), created, false},
		{"zero copy time", time.Time{}, created, false},
		{"zero owner time", created, time.Time{}, false},
		{"both zero", time.Time{}, time.Time{}, false},
	} {
		if got := sameCreatedAt(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: sameCreatedAt(%v, %v) = %v, want %v", tc.name, tc.a, tc.b, got, tc.want)
		}
	}
}

// TestWritesToLocalCopyOfUnreadableOwnerFail pins that a write to a copy whose
// auto-route target cannot be opened fails, naming the target, and leaves the
// copy as it is, while a read falls back to the copy.
func TestWritesToLocalCopyOfUnreadableOwnerFail(t *testing.T) {
	ids := []string{"pl-u", "pl-c", "pl-d", "pl-m"}
	f := newCopyFixture(t, ids, ids, nil)
	if err := os.WriteFile(filepath.Join(f.planningBeadsDir, "metadata.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("break planning metadata: %v", err)
	}

	for _, step := range []struct {
		cmd   *cobra.Command
		flags map[string]string
		args  []string
	}{
		{updateCmd, map[string]string{"priority": "0"}, []string{"pl-u"}},
		{closeCmd, map[string]string{"reason": "done"}, []string{"pl-c"}},
		{deleteCmd, map[string]string{"force": "true"}, []string{"pl-d"}},
		{commentCmd, nil, []string{"pl-m", "routed comment"}},
	} {
		res := runCopyCmd(t, step.cmd, step.flags, step.args...)
		if res.exitCode == 0 {
			t.Errorf("bd %s %v of a copy whose target cannot be opened exited 0, want a failure", step.cmd.Name(), step.args)
		}
		reported := res.stderr
		if res.err != nil {
			reported += res.err.Error()
		}
		if !strings.Contains(reported, f.planningDir) {
			t.Errorf("bd %s %v failure does not name the target %s:\n%s", step.cmd.Name(), step.args, f.planningDir, reported)
		}
		f.requireCopyUnchanged(t, step.args[0])
	}
	comments, err := f.project.GetIssueComments(t.Context(), "pl-m")
	if err != nil {
		t.Fatalf("read project comments: %v", err)
	}
	if len(comments) != 0 {
		t.Errorf("the project copy of pl-m gained comments %+v", comments)
	}

	jsonOutput = true
	res := runCopyCmd(t, showCmd, nil, "pl-u")
	if res.exitCode != 0 || !strings.Contains(res.stdout, `"pl-u"`) {
		t.Errorf("bd show pl-u of a copy whose target cannot be opened: exit %d, stdout %q, stderr %q; want the copy", res.exitCode, res.stdout, res.stderr)
	}
}

// TestDeleteBatchAcrossWorkspacesRefused pins that a bd delete batch whose ids
// resolve in different workspaces fails before deleting anything, while a
// batch resolving in one workspace deletes as before.
func TestDeleteBatchAcrossWorkspacesRefused(t *testing.T) {
	// pl-n is a native project row whose id the planning workspace also
	// holds for a different issue: it resolves locally.
	f := newCopyFixture(t, []string{"pl-x", "pl-y", "pl-z", "pl-n"}, []string{"pl-x", "pl-y", "pl-z"}, []string{"pl-n"})

	res := runCopyCmd(t, deleteCmd, map[string]string{"force": "true"}, "pl-x", "pl-n")
	if res.exitCode == 0 {
		t.Errorf("bd delete --force pl-x pl-n across two workspaces exited 0, want a refusal")
	}
	reported := res.stderr
	if res.err != nil {
		reported += res.err.Error()
	}
	if !strings.Contains(reported, "separate") {
		t.Errorf("the refusal does not say to submit separate commands:\n%s", reported)
	}
	for _, id := range []string{"pl-x", "pl-n"} {
		if f.planningIssue(t, id) == nil {
			t.Errorf("the refused batch deleted planning %s", id)
		}
		if f.projectIssue(t, id) == nil {
			t.Errorf("the refused batch deleted project %s", id)
		}
	}

	res = runCopyCmd(t, deleteCmd, map[string]string{"force": "true"}, "pl-y", "pl-z")
	if res.exitCode != 0 {
		t.Fatalf("bd delete --force pl-y pl-z in one workspace: exit %d, err %v\nstderr: %s", res.exitCode, res.err, res.stderr)
	}
	for _, id := range []string{"pl-y", "pl-z"} {
		if f.planningIssue(t, id) != nil {
			t.Errorf("planning %s survived bd delete --force", id)
		}
	}
}

// TestDeleteBatchMixedDestinationsRefusedInEitherOrder pins the refusal for
// a copy beside a project-only row with the project row first too, and for
// a copy beside an id a prefix route serves from a rig: two routed
// destinations, neither of which loses a row.
func TestDeleteBatchMixedDestinationsRefusedInEitherOrder(t *testing.T) {
	f := newCopyFixture(t, []string{"pl-x"}, []string{"pl-x"}, []string{"pr-n"})

	reported := func(res runResult) string {
		if res.err != nil {
			return res.stderr + res.err.Error()
		}
		return res.stderr
	}
	res := runCopyCmd(t, deleteCmd, map[string]string{"force": "true"}, "pr-n", "pl-x")
	if res.exitCode == 0 || !strings.Contains(reported(res), "separate bd delete") {
		t.Fatalf("bd delete --force pr-n pl-x: exit %d; want the mixed-destination refusal:\n%s", res.exitCode, reported(res))
	}
	for _, want := range []string{"pr-n in this workspace", "pl-x in " + f.planningDir} {
		if !strings.Contains(reported(res), want) {
			t.Errorf("the refusal does not name %q:\n%s", want, reported(res))
		}
	}
	if f.projectIssue(t, "pr-n") == nil || f.planningIssue(t, "pl-x") == nil {
		t.Fatal("the refused batch deleted a row")
	}
	f.requireCopyUnchanged(t, "pl-x")

	rigDir := filepath.Join(filepath.Dir(f.repoDir), "rig")
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	writeEmbeddedMetadata(t, rigBeadsDir, "rig")
	seedEmbeddedSchema(t, rigDir, "rig")
	rig, err := embeddeddolt.Open(t.Context(), rigBeadsDir, "rig", "main")
	if err != nil {
		t.Fatalf("open rig: %v", err)
	}
	if err := rig.SetConfig(t.Context(), "issue_prefix", "gt"); err != nil {
		t.Fatalf("set rig issue_prefix: %v", err)
	}
	if err := rig.CreateIssue(t.Context(), &types.Issue{ID: "gt-r1", Title: "rig gt-r1", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "test"); err != nil {
		t.Fatalf("seed rig gt-r1: %v", err)
	}
	if err := rig.Commit(t.Context(), "seed"); err != nil {
		t.Fatalf("commit rig seed: %v", err)
	}
	if err := rig.Close(); err != nil {
		t.Fatalf("close rig: %v", err)
	}
	if err := os.WriteFile(filepath.Join(f.projectBeadsDir, "routes.jsonl"), []byte(`{"prefix":"gt-","path":"../rig"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	res = runCopyCmd(t, deleteCmd, map[string]string{"force": "true"}, "pl-x", "gt-r1")
	if res.exitCode == 0 || !strings.Contains(reported(res), "separate bd delete") {
		t.Fatalf("bd delete --force pl-x gt-r1: exit %d; want the mixed-destination refusal:\n%s", res.exitCode, reported(res))
	}
	if f.planningIssue(t, "pl-x") == nil {
		t.Error("the refused batch deleted planning pl-x")
	}
	if headIssueOrNil(t, rigBeadsDir, "rig", "gt-r1") == nil {
		t.Error("the refused batch deleted rig gt-r1")
	}
	f.requireCopyUnchanged(t, "pl-x")
}

// TestDeleteDestinationIsWorkspaceAndDatabase pins what makes two delete
// destinations one: the canonical .beads directory and the database selected
// there, not the handle. Two databases of one workspace are two, and a route
// back to this workspace's own database is this workspace.
func TestDeleteDestinationIsWorkspaceAndDatabase(t *testing.T) {
	f := newCopyFixture(t, nil, nil, nil)
	alias := f.planningDir + "-alias"
	if err := os.Symlink(f.planningDir, alias); err != nil {
		t.Fatalf("symlink %s -> %s: %v", alias, f.planningDir, err)
	}
	routed := func(beadsDir, database string) *RoutedResult {
		return &RoutedResult{Routed: true, beadsDir: beadsDir, database: database}
	}
	planA := routed(f.planningBeadsDir, copyFixturePlanningDB).deleteDestination()
	for _, tc := range []struct {
		name string
		got  deleteDestination
		same bool
	}{
		{"another handle on the same database", routed(f.planningBeadsDir, copyFixturePlanningDB).deleteDestination(), true},
		{"the same database through a symlinked path", routed(filepath.Join(alias, ".beads"), copyFixturePlanningDB).deleteDestination(), true},
		{"another database of the same workspace", routed(f.planningBeadsDir, "other").deleteDestination(), false},
		{"this workspace", (&RoutedResult{}).deleteDestination(), false},
	} {
		if (tc.got.key == planA.key) != tc.same {
			t.Errorf("%s: key %q vs %q, want same=%v", tc.name, tc.got.key, planA.key, tc.same)
		}
	}
	if got := routed(f.projectBeadsDir, copyFixtureProjectDB).deleteDestination(); got.key != "" || got.where != "this workspace" {
		t.Errorf("a route to this workspace's own database = %+v, want this workspace", got)
	}
	if got := routed(f.projectBeadsDir, "other").deleteDestination(); got.key == "" {
		t.Error("another database of this workspace keyed as this workspace")
	}
	if err := refuseMixedDeleteDestinations([]string{"pl-1", "pl-2"}, []deleteDestination{planA, routed(f.planningBeadsDir, "other").deleteDestination()}); err == nil {
		t.Error("a batch across two databases of one workspace was not refused")
	}
}

// TestResolveDeleteBatchReleasesWhatItKeeps pins the handles a delete batch's
// resolution holds: it keeps the first routed resolution open for the batch
// and closes every other one at once, and an id that fails after it closes
// the kept one as well.
func TestResolveDeleteBatchReleasesWhatItKeeps(t *testing.T) {
	closes := map[string]int{}
	resolver := func(failing map[string]error) func(string) (*RoutedResult, error) {
		return func(id string) (*RoutedResult, error) {
			if err := failing[id]; err != nil {
				return nil, err
			}
			return &RoutedResult{Routed: true, ResolvedID: id, Issue: &types.Issue{ID: id}, closeFn: func() { closes[id]++ }}, nil
		}
	}

	batch, err := resolveDeleteBatch([]string{"pl-a", "pl-b", "pl-gone"}, resolver(map[string]error{"pl-gone": storage.ErrNotFound}))
	if err != nil {
		t.Fatalf("resolveDeleteBatch: %v", err)
	}
	if batch.routed == nil || batch.routed.ResolvedID != "pl-a" || closes["pl-a"] != 0 || closes["pl-b"] != 1 {
		t.Fatalf("kept %v with closes %v, want pl-a kept open and pl-b closed once", batch.routed, closes)
	}
	if !slices.Equal(batch.notFound, []string{"pl-gone"}) || !slices.Equal(batch.resolvedIDs, []string{"pl-a", "pl-b"}) {
		t.Fatalf("resolved %v, not found %v", batch.resolvedIDs, batch.notFound)
	}
	batch.routed.Close()

	for _, failure := range []error{
		errors.New("owner unreadable"),
		&utils.AbbreviatedIDError{Input: "pl-c", Matches: []string{"pl-c1", "pl-c2"}},
	} {
		clear(closes)
		if _, err := resolveDeleteBatch([]string{"pl-a", "pl-b", "pl-c"}, resolver(map[string]error{"pl-c": failure})); err == nil {
			t.Fatalf("resolveDeleteBatch with pl-c failing (%v) succeeded", failure)
		}
		if closes["pl-a"] != 1 || closes["pl-b"] != 1 {
			t.Errorf("pl-c failing (%v) left closes %v, want pl-a and pl-b closed once each", failure, closes)
		}
	}
}

// TestStoreOpensLoggedUnderVerbose pins that --verbose logs each store a
// command opens, with its access and the reason, on stderr, and leaves the
// command's stdout as it was.
func TestStoreOpensLoggedUnderVerbose(t *testing.T) {
	f := newCopyFixture(t, []string{"pl-v"}, []string{"pl-v"}, nil)
	jsonOutput = true

	quiet := runCopyCmd(t, showCmd, nil, "pl-v")
	if quiet.exitCode != 0 {
		t.Fatalf("bd show pl-v: exit %d, err %v\nstderr: %s", quiet.exitCode, quiet.err, quiet.stderr)
	}
	if strings.Contains(quiet.stderr, "store: ") {
		t.Errorf("bd show without --verbose logged store opens:\n%s", quiet.stderr)
	}

	debug.SetVerbose(true)
	t.Cleanup(func() { debug.SetVerbose(false) })
	verbose := runCopyCmd(t, showCmd, nil, "pl-v")
	if verbose.exitCode != 0 {
		t.Fatalf("bd show pl-v --verbose: exit %d, err %v\nstderr: %s", verbose.exitCode, verbose.err, verbose.stderr)
	}
	if verbose.stdout != quiet.stdout {
		t.Errorf("--verbose changed stdout:\nwithout: %s\nwith:    %s", quiet.stdout, verbose.stdout)
	}
	want := "store: " + f.planningBeadsDir + " read (auto-route: maintainer)\n"
	if !strings.Contains(verbose.stderr, want) {
		t.Errorf("bd show pl-v --verbose stderr lacks %q:\n%s", want, verbose.stderr)
	}
}

// TestStoreOpenLogNamesRuleAndAccess pins the reason and access of an
// auto-routed store's log line: the rule that selected the target, read for
// a read and write for a mutation, logged once.
func TestStoreOpenLogNamesRuleAndAccess(t *testing.T) {
	for _, rule := range []string{"maintainer", "default"} {
		t.Run(rule, func(t *testing.T) {
			f := newCopyFixture(t, []string{"pl-v"}, []string{"pl-v"}, nil)
			if rule == "default" {
				for key, value := range map[string]string{"routing.maintainer": "", "routing.default": f.planningDir} {
					if err := f.project.SetConfig(t.Context(), key, value); err != nil {
						t.Fatalf("set %s: %v", key, err)
					}
				}
			}
			debug.SetVerbose(true)
			t.Cleanup(func() { debug.SetVerbose(false) })

			read := runCopyCmd(t, showCmd, nil, "pl-v")
			if read.exitCode != 0 {
				t.Fatalf("bd show pl-v: exit %d, err %v\nstderr: %s", read.exitCode, read.err, read.stderr)
			}
			if want := "store: " + f.planningBeadsDir + " read (auto-route: " + rule + ")\n"; !strings.Contains(read.stderr, want) {
				t.Errorf("bd show pl-v stderr lacks %q:\n%s", want, read.stderr)
			}
			write := runCopyCmd(t, updateCmd, map[string]string{"priority": "0"}, "pl-v")
			if write.exitCode != 0 {
				t.Fatalf("bd update pl-v: exit %d, err %v\nstderr: %s", write.exitCode, write.err, write.stderr)
			}
			if want := "store: " + f.planningBeadsDir + " write (auto-route: " + rule + ")\n"; strings.Count(write.stderr, want) != 1 {
				t.Errorf("bd update pl-v stderr does not log %q once:\n%s", want, write.stderr)
			}
		})
	}
}

// TestProjectStoreLazyOpenLoggedOnlyOnSuccess pins the project store's log
// line when a command opens it lazily (ensureStoreActive), and that an open
// that fails logs no line.
func TestProjectStoreLazyOpenLoggedOnlyOnSuccess(t *testing.T) {
	f := newCopyFixture(t, nil, nil, nil)
	if err := f.project.Close(); err != nil {
		t.Fatalf("close the fixture's project store: %v", err)
	}
	storeMutex.Lock()
	store, storeActive = nil, false
	storeMutex.Unlock()
	debug.SetVerbose(true)
	t.Cleanup(func() { debug.SetVerbose(false) })

	var err error
	stderr := captureStderr(t, func() { err = ensureStoreActiveWithContext(t.Context()) })
	if err != nil {
		t.Fatalf("ensureStoreActiveWithContext: %v", err)
	}
	t.Cleanup(func() { _ = getStore().Close() })
	if want := "store: " + f.projectBeadsDir + " write (project)\n"; strings.Count(stderr, want) != 1 {
		t.Errorf("the lazy project open does not log %q once:\n%s", want, stderr)
	}
	stderr = captureStderr(t, func() { err = ensureStoreActiveWithContext(t.Context()) })
	if err != nil || strings.Contains(stderr, "store: ") {
		t.Errorf("ensureStoreActive with the store already open: err %v, stderr:\n%s", err, stderr)
	}

	missing := filepath.Join(t.TempDir(), ".beads")
	writeEmbeddedMetadata(t, missing, "missing")
	stderr = captureStderr(t, func() {
		var s storage.DoltStorage
		s, err = openForeignStore(t.Context(), missing, foreignRead, "test")
		if err == nil {
			_ = s.Close()
		}
	})
	if err == nil {
		t.Fatalf("opening %s, which holds no database, succeeded", missing)
	}
	if strings.Contains(stderr, "store: ") {
		t.Errorf("a failed open logged a store line:\n%s", stderr)
	}
}

// TestStoreOpenLogSpellsValuesAsPrinted pins that the store-open log spells
// the directory and the reason by remedy.PrintedValue, so a control
// character in either cannot redraw the terminal or split the line.
func TestStoreOpenLogSpellsValuesAsPrinted(t *testing.T) {
	debug.SetVerbose(true)
	t.Cleanup(func() { debug.SetVerbose(false) })
	stderr := captureStderr(t, func() { logStoreOpen("/tmp/a\x1b[2Jb/.beads", false, "prefix route: x\ny") })
	if strings.ContainsRune(stderr, '\x1b') || strings.Count(stderr, "\n") != 1 || !strings.HasSuffix(stderr, "\n") {
		t.Fatalf("the log line carries a raw control character: %q", stderr)
	}
	for _, want := range []string{`\x1b[2Jb/.beads"`, `"prefix route: x\ny"`} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the log line %q lacks %q", stderr, want)
		}
	}
}
