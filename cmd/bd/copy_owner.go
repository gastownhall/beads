package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/routing"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// A hydrated copy is a row bd repo sync imported from another repository's
// issues.jsonl (CreateIssuesWithFullOptions stamps SourceRepo), or a bead that
// inherited its parent's marker through a discovered-from dependency. When
// that other repository is also the active auto-route target — the contributor
// setup arranges exactly that, routing to the planning repo it hydrates from —
// bd list answers from the owner while every id-based command still finds and
// mutates the stale project copy: a close succeeds and the closed row reappears
// in the next list. The functions below decide, for a row already resolved
// locally, whether the active auto-route target demonstrably owns it, by the
// marker, the id and the creation time together (a compatibility heuristic for
// existing data, not a globally unique ownership token), and hand the caller
// the owner's row instead. A row that does not pass the checks stays local, as
// it always was.

// commandWorkspaceBeadsDir is the .beads directory this process's own store
// lives in: the workspace bd was launched in, whose store the resolvers hold.
// The routed-target comparison below uses it to recognize a target that names
// this very workspace, so it is never reopened as a foreign owner of itself.
func commandWorkspaceBeadsDir() string {
	if dir := resolveCommandBeadsDir(dbPath); dir != "" {
		return dir
	}
	if wd, err := os.Getwd(); err == nil {
		return filepath.Join(wd, ".beads")
	}
	return ""
}

// copyOwnerTarget reports whether issue is a candidate for owner redirection:
// it carries a foreign source marker, the workspace's auto-routing selects a
// target that is not this workspace, and the marker names that target, as
// isPlanningRepoMirror compares them (canonicalized paths). It returns the
// target's expanded repo path when so.
func copyOwnerTarget(ctx context.Context, localStore storage.DoltStorage, issue *types.Issue) (string, bool) {
	if issue == nil || issue.SourceRepo == "" || issue.SourceRepo == "." {
		return "", false
	}
	repoPath, _ := determineAutoRoutedRepoPath(ctx, localStore)
	if repoPath == "" || repoPath == "." {
		return "", false
	}
	target := routing.ExpandPath(repoPath)
	if dir := commandWorkspaceBeadsDir(); dir != "" &&
		utils.CanonicalizePath(filepath.Join(target, ".beads")) == utils.CanonicalizePath(dir) {
		// The active target is this workspace: the local row IS the owner's.
		return "", false
	}
	if utils.CanonicalizePath(routing.ExpandPath(issue.SourceRepo)) != utils.CanonicalizePath(target) {
		return "", false
	}
	return target, true
}

// sameOwnerCreation reports whether copy and owner were created at the same
// instant: the one identity check beside the id a hydrated copy carries that
// its mutable state cannot satisfy. The storage layer keeps second precision,
// so the comparison truncates to seconds in UTC; a zero creation time on either
// row proves nothing and redirects nothing.
func sameOwnerCreation(copy, owner *types.Issue) bool {
	if copy == nil || owner == nil || copy.CreatedAt.IsZero() || owner.CreatedAt.IsZero() {
		return false
	}
	return copy.CreatedAt.UTC().Truncate(time.Second).Equal(owner.CreatedAt.UTC().Truncate(time.Second))
}

// resolveCopyOfActiveAutoRouteTarget is the owner-redirection decision for a
// row the caller resolved locally (routed.go's resolvers): the returned result
// is local unchanged unless issue is an identifiable copy of the active
// auto-route target's row, in which case the target's store — opened writable
// for a write-intent caller, read-only for a read — resolves the same full id
// exactly (never a fuzzy re-resolution) and its row, issue and commit behavior
// are the caller's.
//
// The owner open and query run through the same admission, target-database and
// error-wrapping path any auto-routed open does (openAutoRoutedTargetAt), so a
// target this workspace refuses to open refuses here too. A write-intent
// caller gets that failure as its own, naming the target: falling back to the
// copy would report success for a write that never reached the owner, the very
// no-op this file exists to end. A read keeps the copy, with the reason on the
// debug channel, so `bd show` still answers when the planning store is busy.
//
// The owner row is only a redirect when the target holds the exact id with the
// same creation time; a missing row (the copy is stale, or an inherited marker
// on a bead the owner never held) keeps the local result on every path.
func resolveCopyOfActiveAutoRouteTarget(ctx context.Context, localStore storage.DoltStorage, local *RoutedResult, writable bool) (*RoutedResult, error) {
	target, ok := copyOwnerTarget(ctx, localStore, local.Issue)
	if !ok {
		return local, nil
	}
	ownerStore, err := openAutoRoutedTargetAt(ctx, target, writable)
	if err != nil {
		if !writable {
			debug.Logf("[routing] %s resolved from the project copy: opening the routed store at %s failed: %v\n",
				printedIssueID(local.ResolvedID), remedy.PrintedValue(target), err)
			return local, nil
		}
		return nil, fmt.Errorf("resolving the copy of %s in the routed store at %s: %w",
			printedIssueID(local.ResolvedID), remedy.PrintedValue(target), err)
	}
	owner, err := ownerStore.GetIssue(ctx, local.ResolvedID)
	switch {
	case errors.Is(err, storage.ErrNotFound):
		_ = ownerStore.Close()
		debug.Logf("[routing] %s resolved from the project copy: the routed store at %s holds no issue with that id\n",
			printedIssueID(local.ResolvedID), remedy.PrintedValue(target))
		return local, nil
	case err != nil:
		_ = ownerStore.Close()
		if !writable {
			debug.Logf("[routing] %s resolved from the project copy: reading the routed store at %s failed: %v\n",
				printedIssueID(local.ResolvedID), remedy.PrintedValue(target), err)
			return local, nil
		}
		return nil, fmt.Errorf("resolving the copy of %s in the routed store at %s: %w",
			printedIssueID(local.ResolvedID), remedy.PrintedValue(target), err)
	}
	if !sameOwnerCreation(local.Issue, owner) {
		_ = ownerStore.Close()
		debug.Logf("[routing] %s resolved from the project copy: the issue in the routed store at %s was created at a different time\n",
			printedIssueID(local.ResolvedID), remedy.PrintedValue(target))
		return local, nil
	}
	debug.Logf("[routing] %s is a hydrated copy; resolving the owner's row in the routed store at %s\n",
		printedIssueID(local.ResolvedID), remedy.PrintedValue(target))
	return &RoutedResult{
		Issue:      owner,
		Store:      ownerStore,
		Routed:     true,
		ResolvedID: local.ResolvedID,
		closeFn:    func() { _ = ownerStore.Close() },
		// RedirectedCopy and its owner path let bd delete report the project
		// copy its owner-directed delete leaves behind.
		RedirectedCopy:      true,
		RedirectedCopyOwner: target,
	}, nil
}
