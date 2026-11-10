//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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
