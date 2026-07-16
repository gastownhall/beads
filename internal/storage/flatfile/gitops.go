// gitops.go implements the VersionControl, HistoryViewer, RemoteStore, and
// SyncStore interfaces using git commands on the .beads/ directory.
// FederationStore is NOT implemented (flat-file syncs through plain git
// remotes); it resolves to the generated typed-unsupported shell.
package flatfile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// gitDir returns the repo root (parent of .beads/).
func (s *FlatFileStore) gitDir() string {
	return filepath.Dir(s.beadsDir)
}

// beadsPathspec returns the store directory's git pathspec relative to
// gitDir (its parent). The basename is not always ".beads" — BEADS_DIR may
// point anywhere — and git treats a non-matching pathspec as "no files":
// status reports clean and add/commit silently touch nothing, so the version
// commit is skipped without error.
func (s *FlatFileStore) beadsPathspec() string {
	return filepath.Base(s.beadsDir) + "/"
}

// gitIdentityEnv returns fallback identity env vars when the repo has no
// usable git identity (fresh machines, CI runners): `git var
// GIT_COMMITTER_IDENT` exits non-zero with "empty ident name" when neither
// config nor environment supply one, and every auto-commit would fail the
// same way. The fallback is injected via environment ONLY in that case, so a
// real configured identity always wins (env would otherwise override config).
// Probed once per store: identity config is not expected to change mid-process.
func (s *FlatFileStore) gitIdentityEnv(ctx context.Context) []string {
	s.gitIdentOnce.Do(func() {
		probe := exec.CommandContext(ctx, "git", "var", "GIT_COMMITTER_IDENT")
		probe.Dir = s.gitDir()
		if err := probe.Run(); err != nil {
			s.gitIdentEnv = []string{
				"GIT_AUTHOR_NAME=beads",
				"GIT_AUTHOR_EMAIL=beads@localhost",
				"GIT_COMMITTER_NAME=beads",
				"GIT_COMMITTER_EMAIL=beads@localhost",
			}
		}
	})
	return s.gitIdentEnv
}

// git runs a git command in the repo root and returns stdout.
func (s *FlatFileStore) git(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = s.gitDir()
	if identEnv := s.gitIdentityEnv(ctx); len(identEnv) > 0 {
		cmd.Env = append(os.Environ(), identEnv...)
	}
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), string(ee.Stderr))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

// gitErrContains reports whether err (from s.git, which embeds git's stderr)
// matches one of the expected benign failure messages. Anything else — missing
// git binary, corrupted repo, canceled context — must be propagated, not
// collapsed into an empty result.
func gitErrContains(err error, substrs ...string) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, sub := range substrs {
		if strings.Contains(msg, sub) {
			return true
		}
	}
	return false
}

// validateGitArg rejects strings starting with "-" to prevent git flag injection.
func validateGitArg(name, value string) error {
	if strings.HasPrefix(value, "-") {
		return fmt.Errorf("flatfile: invalid %s %q: must not start with '-'", name, value)
	}
	return nil
}

// ── VersionControl ───────────────────────────────────────────────────

func (s *FlatFileStore) Branch(ctx context.Context, name string) error {
	if err := validateGitArg("branch name", name); err != nil {
		return err
	}
	_, err := s.git(ctx, "branch", name)
	return err
}

// Checkout rewrites .beads/ files in the working tree, so — like every
// working-tree-mutating git op below (Merge, ResolveConflicts, Pull,
// PullRemote, PullFrom, Sync) — it takes the store's write lock: without it a
// concurrent transaction could journal a pre-image, have git rewrite the file,
// and then roll back the journal, silently reverting the checked-out/merged/
// pulled state (and a writeIssue rename in flight could clobber a just-merged
// file or abort the merge on "local changes would be overwritten").
func (s *FlatFileStore) Checkout(ctx context.Context, branch string) error {
	if err := validateGitArg("branch name", branch); err != nil {
		return err
	}
	defer s.lockWrite()()
	_, err := s.git(ctx, "checkout", branch)
	return err
}

func (s *FlatFileStore) CurrentBranch(ctx context.Context) (string, error) {
	return s.git(ctx, "rev-parse", "--abbrev-ref", "HEAD")
}

func (s *FlatFileStore) DeleteBranch(ctx context.Context, branch string) error {
	if err := validateGitArg("branch name", branch); err != nil {
		return err
	}
	_, err := s.git(ctx, "branch", "-d", branch)
	return err
}

func (s *FlatFileStore) ListBranches(ctx context.Context) ([]string, error) {
	out, err := s.git(ctx, "branch", "--list", "--format=%(refname:short)")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

func (s *FlatFileStore) Commit(ctx context.Context, message string) error {
	// No git work tree → no version layer to commit to: the JSON files ARE
	// the store. The auto-commit path (maybeAutoCommitStore) routes every
	// mutating command through Commit, so this must be a no-op, not an error
	// that fails the command — mirrors commitTransaction's guard.
	if _, err := s.git(ctx, "rev-parse", "--is-inside-work-tree"); err != nil {
		return nil
	}
	// Unchanged tree → no-op, mirroring embeddeddolt's nothing-to-commit
	// handling. Without this, an idempotent write (e.g. re-adding an existing
	// dependency edge) leaves the tree clean and the pathspec commit exits 1
	// with "nothing to commit" on STDOUT — invisible to the stderr-only error
	// wrapper, so the whole command fails instead of no-oping.
	out, err := s.git(ctx, "status", "--porcelain", s.beadsPathspec())
	if err != nil {
		return err
	}
	if out == "" {
		return nil
	}
	// The add error must propagate: the pathspec commit below exits 0 with
	// only the TRACKED files, so a failed add (index.lock held, perms, disk
	// full) would silently drop new issue files from the commit.
	if _, err := s.git(ctx, "add", s.beadsPathspec()); err != nil {
		return err
	}
	_, err = s.git(ctx, "commit", "-m", message, "--", s.beadsPathspec())
	return err
}

func (s *FlatFileStore) CommitWithConfig(ctx context.Context, message string) error {
	return s.Commit(ctx, message)
}

// CommitMergeResolution concludes a merge whose conflicts were resolved with an
// explicit strategy. Flat-file storage stages the whole .beads/ tree (config
// files included), so a config-only resolution is never dropped the way it is
// under Dolt's config-excluding Commit (GH#2455/#2474). Unlike Commit it must
// NOT restrict the commit to a pathspec: git forbids a partial commit while
// MERGE_HEAD exists ("cannot do a partial commit during a merge"), and only a
// plain commit concludes the merge.
func (s *FlatFileStore) CommitMergeResolution(ctx context.Context, message string) error {
	if _, err := s.git(ctx, "add", ".beads/"); err != nil {
		return err
	}
	_, err := s.git(ctx, "commit", "-m", message)
	return err
}

func (s *FlatFileStore) CommitPending(ctx context.Context, _ string) (bool, error) {
	// Outside any git work tree there is no version layer to commit to: the
	// JSON files ARE the store, git is optional versioning on top. Auto-commit
	// (every mutating command funnels here via maybeAutoCommitStore) must be a
	// no-op rather than fail the command — mirrors commitTransaction's guard.
	// Explicit VC operations (Commit et al.) still error, keeping `bd commit`
	// honest in an unversioned workspace.
	if _, err := s.git(ctx, "rev-parse", "--is-inside-work-tree"); err != nil {
		return false, nil
	}
	out, err := s.git(ctx, "status", "--porcelain", ".beads/")
	if err != nil {
		return false, err
	}
	if out == "" {
		return false, nil
	}
	// Like Commit: a swallowed add error would let the pathspec commit
	// succeed with new issue files silently left untracked.
	if _, err := s.git(ctx, "add", ".beads/"); err != nil {
		return false, err
	}
	// Pathspec-scoped like Commit: an unrestricted commit would sweep the
	// user's own staged files into a bd auto-commit.
	_, err = s.git(ctx, "commit", "-m", "bd: auto-commit pending changes", "--", ".beads/")
	return err == nil, err
}

func (s *FlatFileStore) CommitExists(ctx context.Context, commitHash string) (bool, error) {
	if err := validateGitArg("commit hash", commitHash); err != nil {
		return false, err
	}
	_, err := s.git(ctx, "cat-file", "-t", commitHash)
	if err != nil {
		// Missing object is the expected "no" answer; anything else
		// (broken repo, missing git, canceled context) is an error.
		if gitErrContains(err, "Not a valid object name", "could not get object info") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *FlatFileStore) GetCurrentCommit(ctx context.Context) (string, error) {
	return s.git(ctx, "rev-parse", "HEAD")
}

func (s *FlatFileStore) Status(ctx context.Context) (*storage.Status, error) {
	out, err := s.git(ctx, "status", "--porcelain", ".beads/")
	if err != nil {
		return nil, err
	}
	var staged, unstaged []storage.StatusEntry
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		indexStatus := line[0]
		workStatus := line[1]
		file := strings.TrimSpace(line[3:])

		status := "modified"
		if indexStatus == '?' || workStatus == '?' {
			status = "new"
		} else if indexStatus == 'D' || workStatus == 'D' {
			status = "deleted"
		}

		if indexStatus != ' ' && indexStatus != '?' {
			staged = append(staged, storage.StatusEntry{Table: file, Status: status})
		}
		if workStatus != ' ' {
			unstaged = append(unstaged, storage.StatusEntry{Table: file, Status: status})
		}
	}
	return &storage.Status{Staged: staged, Unstaged: unstaged}, nil
}

func (s *FlatFileStore) Log(ctx context.Context, limit int) ([]storage.CommitInfo, error) {
	args := []string{"log", fmt.Sprintf("-%d", limit), "--format=%H|%an|%ae|%aI|%s", "--", ".beads/"}
	out, err := s.git(ctx, args...)
	if err != nil {
		if gitErrContains(err, "does not have any commits yet") {
			return nil, nil
		}
		return nil, err
	}
	if out == "" {
		return nil, nil
	}

	var commits []storage.CommitInfo
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "|", 5)
		if len(parts) < 5 {
			continue
		}
		date, _ := time.Parse(time.RFC3339, parts[3])
		commits = append(commits, storage.CommitInfo{
			Hash:    parts[0],
			Author:  parts[1],
			Email:   parts[2],
			Date:    date,
			Message: parts[4],
		})
	}
	return commits, nil
}

func (s *FlatFileStore) Merge(ctx context.Context, branch string) ([]storage.Conflict, error) {
	if err := validateGitArg("branch", branch); err != nil {
		return nil, err
	}
	defer s.lockWrite()() // working-tree-mutating: see Checkout
	_, err := s.git(ctx, "merge", branch)
	if err != nil {
		conflicts, _ := s.GetConflicts(ctx)
		return conflicts, err
	}
	return nil, nil
}

func (s *FlatFileStore) GetConflicts(ctx context.Context) ([]storage.Conflict, error) {
	out, err := s.git(ctx, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	var conflicts []storage.Conflict
	for _, file := range strings.Split(out, "\n") {
		if strings.HasPrefix(file, ".beads/issues/") {
			id := strings.TrimSuffix(strings.TrimPrefix(file, ".beads/issues/"), ".json")
			conflicts = append(conflicts, storage.Conflict{IssueID: id})
		}
	}
	return conflicts, nil
}

// ResolveConflicts resolves every unmerged path under .beads/ toward the
// chosen side. A blanket 'git checkout --ours/--theirs .beads/' fails on
// modify/delete conflicts whenever the chosen side is the one that deleted
// ("path ... does not have our/their version"), which the delete cascades
// make routine; so the unmerged paths are enumerated with their index stages
// (2 = ours, 3 = theirs) and each path is checked out where the chosen stage
// exists and 'git rm'ed where the chosen side deleted. Checked-out entries
// stay unmerged until CommitMergeResolution's 'git add' stages them.
func (s *FlatFileStore) ResolveConflicts(ctx context.Context, _ string, strategy string) error {
	var flag, keepStage string
	switch strategy {
	case "ours":
		flag, keepStage = "--ours", "2"
	case "theirs":
		flag, keepStage = "--theirs", "3"
	default:
		return fmt.Errorf("flatfile: unknown conflict strategy: %s", strategy)
	}
	defer s.lockWrite()() // working-tree-mutating: see Checkout

	out, err := s.git(ctx, "ls-files", "-u", "--", ".beads/")
	if err != nil {
		return err
	}
	if out == "" {
		return nil
	}
	// Lines are "<mode> <sha> <stage>\t<path>"; a path repeats per stage.
	seen := make(map[string]bool)
	hasKeepStage := make(map[string]bool)
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			continue
		}
		fields := strings.Fields(line[:tab])
		if len(fields) < 3 {
			continue
		}
		path := line[tab+1:]
		if !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
		if fields[2] == keepStage {
			hasKeepStage[path] = true
		}
	}
	var keep, drop []string
	for _, path := range paths {
		if hasKeepStage[path] {
			keep = append(keep, path)
		} else {
			drop = append(drop, path)
		}
	}
	if len(keep) > 0 {
		if _, err := s.git(ctx, append([]string{"checkout", flag, "--"}, keep...)...); err != nil {
			return err
		}
	}
	if len(drop) > 0 {
		if _, err := s.git(ctx, append([]string{"rm", "-f", "-q", "--"}, drop...)...); err != nil {
			return err
		}
	}
	return nil
}

// ── HistoryViewer ────────────────────────────────────────────────────

func (s *FlatFileStore) History(ctx context.Context, issueID string) ([]*storage.HistoryEntry, error) {
	if err := validateID(issueID); err != nil {
		return nil, err
	}
	file := fmt.Sprintf(".beads/issues/%s.json", issueID)
	out, err := s.git(ctx, "log", "--format=%H|%an|%aI", "--", file)
	if err != nil {
		if gitErrContains(err, "does not have any commits yet") {
			return nil, nil
		}
		return nil, err
	}
	if out == "" {
		return nil, nil
	}

	var entries []*storage.HistoryEntry
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "|", 3)
		if len(parts) < 3 {
			continue
		}
		date, _ := time.Parse(time.RFC3339, parts[2])
		entry := &storage.HistoryEntry{
			CommitHash: parts[0],
			Committer:  parts[1],
			CommitDate: date,
		}
		// Try to load the issue at this commit.
		if content, err := s.git(ctx, "show", parts[0]+":"+file); err == nil {
			var issue types.Issue
			if jsonErr := json.Unmarshal([]byte(content), &issue); jsonErr == nil {
				entry.Issue = &issue
			}
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (s *FlatFileStore) AsOf(ctx context.Context, issueID string, ref string) (*types.Issue, error) {
	if err := validateID(issueID); err != nil {
		return nil, err
	}
	if err := validateGitArg("ref", ref); err != nil {
		return nil, err
	}
	file := fmt.Sprintf(".beads/issues/%s.json", issueID)
	content, err := s.git(ctx, "show", ref+":"+file)
	if err != nil {
		// Only "the issue/ref genuinely isn't there" reads as ErrNotFound
		// (mirroring CommitExists); a canceled context, missing git binary,
		// or corrupted repo must surface as the error it is, not as "the
		// issue never existed at that ref".
		if gitErrContains(err,
			"does not exist in",
			"exists on disk, but not in",
			"Not a valid object name",
			"invalid object name") {
			return nil, storage.ErrNotFound
		}
		return nil, err
	}
	var issue types.Issue
	if err := json.Unmarshal([]byte(content), &issue); err != nil {
		return nil, err
	}
	return &issue, nil
}

func (s *FlatFileStore) Diff(ctx context.Context, fromRef, toRef string) ([]*storage.DiffEntry, error) {
	if err := validateGitArg("from ref", fromRef); err != nil {
		return nil, err
	}
	if err := validateGitArg("to ref", toRef); err != nil {
		return nil, err
	}
	out, err := s.git(ctx, "diff", "--name-status", fromRef+".."+toRef, "--", ".beads/issues/")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}

	var entries []*storage.DiffEntry
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		status := parts[0]
		file := parts[1]
		if !strings.HasPrefix(file, ".beads/issues/") {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(file, ".beads/issues/"), ".json")

		var diffType string
		switch status {
		case "A":
			diffType = "added"
		case "M":
			diffType = "modified"
		case "D":
			diffType = "removed"
		default:
			diffType = "modified"
		}

		entries = append(entries, &storage.DiffEntry{
			IssueID:  id,
			DiffType: diffType,
		})
	}
	return entries, nil
}

// ── RemoteStore ──────────────────────────────────────────────────────

func (s *FlatFileStore) AddRemote(ctx context.Context, name, url string) error {
	if err := validateGitArg("remote name", name); err != nil {
		return err
	}
	if err := validateGitArg("remote url", url); err != nil {
		return err
	}
	_, err := s.git(ctx, "remote", "add", name, url)
	return err
}

func (s *FlatFileStore) RemoveRemote(ctx context.Context, name string) error {
	if err := validateGitArg("remote name", name); err != nil {
		return err
	}
	_, err := s.git(ctx, "remote", "remove", name)
	return err
}

func (s *FlatFileStore) HasRemote(ctx context.Context, name string) (bool, error) {
	out, err := s.git(ctx, "remote")
	if err != nil {
		return false, err
	}
	for _, r := range strings.Split(out, "\n") {
		if strings.TrimSpace(r) == name {
			return true, nil
		}
	}
	return false, nil
}

func (s *FlatFileStore) ListRemotes(ctx context.Context) ([]storage.RemoteInfo, error) {
	out, err := s.git(ctx, "remote", "-v")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	seen := make(map[string]bool)
	var remotes []storage.RemoteInfo
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		name := parts[0]
		if seen[name] {
			continue
		}
		seen[name] = true
		remotes = append(remotes, storage.RemoteInfo{Name: name, URL: parts[1]})
	}
	return remotes, nil
}

func (s *FlatFileStore) Push(ctx context.Context) error {
	_, err := s.git(ctx, "push")
	return err
}

func (s *FlatFileStore) Pull(ctx context.Context) error {
	defer s.lockWrite()() // working-tree-mutating: see Checkout
	_, err := s.git(ctx, "pull")
	return err
}

func (s *FlatFileStore) ForcePush(ctx context.Context) error {
	_, err := s.git(ctx, "push", "--force")
	return err
}

func (s *FlatFileStore) PushRemote(ctx context.Context, remote string, force bool) error {
	if err := validateGitArg("remote", remote); err != nil {
		return err
	}
	args := []string{"push", remote}
	if force {
		args = append(args, "--force")
	}
	_, err := s.git(ctx, args...)
	return err
}

func (s *FlatFileStore) PullRemote(ctx context.Context, remote string) error {
	if err := validateGitArg("remote", remote); err != nil {
		return err
	}
	defer s.lockWrite()() // working-tree-mutating: see Checkout
	_, err := s.git(ctx, "pull", remote)
	return err
}

func (s *FlatFileStore) Fetch(ctx context.Context, peer string) error {
	if err := validateGitArg("peer", peer); err != nil {
		return err
	}
	_, err := s.git(ctx, "fetch", peer)
	return err
}

func (s *FlatFileStore) PushTo(ctx context.Context, peer string) error {
	if err := validateGitArg("peer", peer); err != nil {
		return err
	}
	_, err := s.git(ctx, "push", peer)
	return err
}

func (s *FlatFileStore) PullFrom(ctx context.Context, peer string) ([]storage.Conflict, error) {
	if err := validateGitArg("peer", peer); err != nil {
		return nil, err
	}
	defer s.lockWrite()() // working-tree-mutating: see Checkout
	_, err := s.git(ctx, "pull", peer)
	if err != nil {
		conflicts, _ := s.GetConflicts(ctx)
		return conflicts, err
	}
	return nil, nil
}

// ── SyncStore ────────────────────────────────────────────────────────

func (s *FlatFileStore) Sync(ctx context.Context, peer string, _ string) (*storage.SyncResult, error) {
	if err := validateGitArg("peer", peer); err != nil {
		return nil, err
	}
	defer s.lockWrite()() // working-tree-mutating (merge step): see Checkout
	start := time.Now()
	result := &storage.SyncResult{Peer: peer, StartTime: start}

	// Sync against the peer's copy of the branch we are on, mirroring the
	// Dolt backend's peer/s.branch (federation.go); hardcoding "main" broke
	// master-default repos and merged the wrong branch into feature branches.
	branch, err := s.CurrentBranch(ctx)
	if err != nil {
		result.Error = err
		result.EndTime = time.Now()
		return result, err
	}

	_, err = s.git(ctx, "fetch", peer)
	if err != nil {
		result.Error = err
		result.EndTime = time.Now()
		return result, err
	}
	result.Fetched = true

	_, err = s.git(ctx, "merge", peer+"/"+branch)
	if err != nil {
		// Return immediately: staging or committing here would collapse the
		// unmerged index entries and persist conflict-markered JSON as if
		// resolved. The caller resolves via ResolveConflicts +
		// CommitMergeResolution (matching the Dolt Sync flow, which also
		// stops at merge failure).
		result.Conflicts, _ = s.GetConflicts(ctx)
		result.Error = err
		result.EndTime = time.Now()
		return result, err
	}
	result.Merged = true

	_, pushErr := s.git(ctx, "push", peer)
	if pushErr != nil {
		result.PushError = pushErr
	} else {
		result.Pushed = true
	}

	result.EndTime = time.Now()
	return result, result.Error
}

func (s *FlatFileStore) SyncStatus(ctx context.Context, peer string) (*storage.SyncStatus, error) {
	if err := validateGitArg("peer", peer); err != nil {
		return nil, err
	}
	branch, err := s.CurrentBranch(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.git(ctx, "rev-list", "--left-right", "--count", "HEAD..."+peer+"/"+branch)
	if err != nil {
		// Dolt parity: when the peer ref is not available locally (not yet
		// fetched), return a partial result with the -1 "unknown" sentinel
		// rather than a false 0/0 "in sync".
		return &storage.SyncStatus{Peer: peer, LocalAhead: -1, LocalBehind: -1}, nil
	}
	parts := strings.Fields(out)
	ahead, behind := 0, 0
	if len(parts) >= 2 {
		ahead, _ = strconv.Atoi(parts[0])
		behind, _ = strconv.Atoi(parts[1])
	}
	return &storage.SyncStatus{
		Peer:        peer,
		LocalAhead:  ahead,
		LocalBehind: behind,
	}, nil
}
