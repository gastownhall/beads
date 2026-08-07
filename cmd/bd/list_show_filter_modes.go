package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
)

// The modes of `bd list` and `bd show` that consume the FILTER ITSELF rather
// than the page a query answers with, gathered here because that is exactly
// what the guard on the rest of cmd/bd is a statement about.
//
// A forbidigo rule in .golangci.yml makes NAMING types.IssueFilter or
// types.WorkFilter a lint failure anywhere under cmd/bd, and this file is one
// of the exceptions the config lists by name with its reason. So no file
// implementing `bd list` or `bd show` can WRITE a filter — it takes one back
// from a workapi builder instead — and that keeps holding for a file those
// commands are split or renamed into tomorrow, which is why the rule is deny
// by default rather than a list of filenames. What it does NOT establish is
// that every filter over there came from a builder: an inferred assignment
// names no type, so a helper in this file could hand one back.
//
// What lives here is everything that genuinely needs the filter as a value:
// the recursive --parent tree walk, which re-parents a copy of it at every
// level; the --watch poll loop, which re-runs the same filter on a ticker; the
// proxied route's builder call, which has to name the type to hand the filter
// back; and --current, which resolves an id from a deliberately BARE filter
// that must not pick up the default listing's exclusions.
//
// The list is SHORTER by everything it no longer holds. `bd list`'s page is
// issueops.Reader.List's on both routes, and the two filter-consuming modes
// above are what is left over — so this file is now the exception's whole
// remaining reason rather than one of several. It did not leave the config's
// exception list with this commit, and the count in issueops/reader.go's claim
// is unchanged for that reason: three modes here still name the type.
//
// Widening the hole is an edit in the config and a diff a reviewer sees.

// getHierarchicalChildren handles the --tree --parent combination logic.
// baseFilter carries CLI filters (--type, --status, etc.) through the recursive walk.
func getHierarchicalChildren(ctx context.Context, store storage.DoltStorage, dbPath string, parentID string, baseFilter types.IssueFilter) ([]*types.Issue, error) {
	// First verify that the parent issue exists
	var parentIssue *types.Issue
	err := withStorage(ctx, store, dbPath, func(s storage.DoltStorage) error {
		var err error
		parentIssue, err = s.GetIssue(ctx, parentID)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("error checking parent issue: %v", err)
	}
	if parentIssue == nil {
		return nil, fmt.Errorf("parent issue '%s' not found", parentID)
	}

	// Use recursive search to find all descendants using the same logic as --parent filter.
	// The parent itself is NOT included in the result set — only actual children and
	// their descendants. This matches the behavior of --json and --flat (GH#3349).
	allDescendants := make(map[string]*types.Issue)

	err = findAllDescendants(ctx, store, dbPath, parentID, baseFilter, allDescendants)
	if err != nil {
		return nil, fmt.Errorf("error finding descendants: %v", err)
	}

	if len(allDescendants) == 0 {
		return nil, nil
	}

	// Include the parent as the tree root only when descendants exist,
	// so the tree renderer can draw the hierarchy with the parent at the top.
	allDescendants[parentID] = parentIssue

	treeIssues := make([]*types.Issue, 0, len(allDescendants))
	for _, issue := range allDescendants {
		treeIssues = append(treeIssues, issue)
	}

	return treeIssues, nil
}

// findAllDescendants recursively finds all descendants using parent filtering.
// baseFilter carries CLI filters (--type, --status, etc.) so the tree respects them.
func findAllDescendants(ctx context.Context, store storage.DoltStorage, dbPath string, parentID string, baseFilter types.IssueFilter, result map[string]*types.Issue) error {
	var children []*types.Issue
	err := withStorage(ctx, store, dbPath, func(s storage.DoltStorage) error {
		filter := baseFilter
		filter.ParentID = &parentID
		filter.Limit = 0 // unlimited per level to avoid truncating the tree walk
		var err error
		children, err = s.SearchIssues(ctx, "", filter)
		return err
	})
	if err != nil {
		return err
	}

	for _, child := range children {
		if _, exists := result[child.ID]; !exists {
			result[child.ID] = child
			err = findAllDescendants(ctx, store, dbPath, child.ID, baseFilter, result)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// watchIssues polls for changes and re-displays (GH#654)
// Uses polling instead of fsnotify because Dolt stores data in a server-side
// database, not files — file watchers never fire.
type watchListDependencyStore interface {
	GetAllDependencyRecords(ctx context.Context) (map[string][]*types.Dependency, error)
}

func loadWatchedIssues(ctx context.Context, store storage.DoltStorage, filter types.IssueFilter, ready bool, parentID string, sortBy string, reverse bool) ([]*types.Issue, error) {
	if ready {
		issues, err := store.GetReadyWork(ctx, workapi.ReadyFilterFromIssueFilter(workapi.WithFetchOneExtra(filter)))
		if err != nil {
			return nil, err
		}
		workapi.SortIssues(issues, sortBy, reverse)
		return issues, nil
	}

	if parentID != "" {
		issues, err := getHierarchicalChildren(ctx, store, "", parentID, filter)
		if err != nil {
			return nil, err
		}
		// getHierarchicalChildren builds its result from a map, so normalize the
		// slice before snapshot comparison to avoid spurious redraws.
		workapi.SortIssues(issues, "id", false)
		return issues, nil
	}

	issues, err := store.SearchIssues(ctx, "", workapi.WithFetchOneExtra(filter))
	if err != nil {
		return nil, err
	}
	workapi.SortIssues(issues, sortBy, reverse)
	return issues, nil
}

func displayWatchedIssueList(ctx context.Context, store watchListDependencyStore, issues []*types.Issue) {
	var allDeps map[string][]*types.Dependency
	if store != nil {
		deps, err := store.GetAllDependencyRecords(ctx)
		if err == nil {
			allDeps = deps
		}
	}
	displayPrettyListWithDeps(issues, true, allDeps)
}

// watchIssues returns an error only for the initial query — a failure there
// means bd list --watch never displayed anything, so (unlike a mid-poll
// refresh failure, which just logs and keeps the last good snapshot on
// screen) it must propagate to the caller. In particular this lets
// runListCore route a MaxRows cap violation through handleMaxRowsError for
// exit-code-2 semantics instead of watchIssues swallowing it and exiting 0
// (be-x42v.4 follow-up, review MUST-FIX 5).
func watchIssues(ctx context.Context, store storage.DoltStorage, filter types.IssueFilter, ready bool, parentID string, sortBy string, reverse bool, effectiveLimit int) error {
	// Initial display
	issues, err := loadWatchedIssues(ctx, store, filter, ready, parentID, sortBy, reverse)
	if err != nil {
		return err
	}
	// The order is already loadWatchedIssues', which the snapshot comparison
	// below depends on; what is left is the cut and its verdict, and those are
	// the shared epilogue's on every other listing this command has.
	issues, truncated := workapi.FinishPage(issues, "", false, effectiveLimit, false)
	displayWatchedIssueList(ctx, store, issues)
	printTruncationHint(truncated, effectiveLimit)
	lastSnapshot := issueSnapshot(issues)

	fmt.Fprintf(os.Stderr, "\nWatching for changes... (Press Ctrl+C to exit)\n")

	// Handle Ctrl+C — deferred Stop prevents signal handler leak
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	pollInterval := 2 * time.Second
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-sigChan:
			fmt.Fprintf(os.Stderr, "\nStopped watching.\n")
			return nil
		case <-ticker.C:
			issues, err := loadWatchedIssues(ctx, store, filter, ready, parentID, sortBy, reverse)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error refreshing issues: %v\n", err)
				continue
			}
			issues, truncated := workapi.FinishPage(issues, "", false, effectiveLimit, false)
			snap := issueSnapshot(issues)
			if snap != lastSnapshot {
				lastSnapshot = snap
				displayWatchedIssueList(ctx, store, issues)
				printTruncationHint(truncated, effectiveLimit)
				fmt.Fprintf(os.Stderr, "\nWatching for changes... (Press Ctrl+C to exit)\n")
			}
		}
	}
}

func openAndPrepare(ctx context.Context, in listInput) (uow.UnitOfWork, types.IssueFilter, error) {
	uw, err := openProxiedListUOW(ctx)
	if err != nil {
		return nil, types.IssueFilter{}, err
	}
	cfg, err := workapi.LoadUOWListConfig(ctx, uw)
	if err != nil {
		uw.Close(ctx)
		return nil, types.IssueFilter{}, err
	}
	filter, err := workapi.BuildListFilter(in.ListRequest, cfg)
	if err != nil {
		uw.Close(ctx)
		return nil, types.IssueFilter{}, err
	}
	return uw, filter, nil
}

func runListProxiedHierarchicalParent(ctx context.Context, uw uow.UnitOfWork, in listInput, filter types.IssueFilter) error {
	treeIssues, err := gatherProxiedHierarchical(ctx, uw, in.ParentID, filter)
	if err != nil {
		return err
	}
	if len(treeIssues) == 0 {
		fmt.Printf("Issue '%s' has no children\n", in.ParentID)
		return nil
	}

	depsByIssueID, err := loadDepsForIssues(ctx, uw, treeIssues)
	if err != nil {
		return err
	}

	displayPrettyListWithDepsMode(treeIssues, false, depsByIssueID, in.depsMode)
	printSkipLabelsFooter(in.SkipLabels)
	return nil
}

func gatherProxiedHierarchical(ctx context.Context, uw uow.UnitOfWork, parentID string, baseFilter types.IssueFilter) ([]*types.Issue, error) {
	parent, err := uw.IssueUseCase().GetIssue(ctx, parentID)
	if err != nil {
		return nil, fmt.Errorf("error checking parent issue: %w", err)
	}
	if parent == nil {
		return nil, fmt.Errorf("parent issue %q not found", parentID)
	}

	descendants, err := uw.IssueUseCase().GetDescendants(ctx, parentID, baseFilter)
	if err != nil {
		return nil, fmt.Errorf("error finding descendants: %w", err)
	}
	if len(descendants) == 0 {
		return nil, nil
	}

	out := make([]*types.Issue, 0, len(descendants)+1)
	out = append(out, parent)
	out = append(out, descendants...)
	return out, nil
}

type currentIssueSearcher interface {
	SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error)
}

// resolveCurrentIssueID determines the current active issue for the agent.
// Priority: in-progress assigned to actor > hooked > last touched.
func resolveCurrentIssueID(ctx context.Context) string {
	return resolveCurrentIssueIDFrom(ctx, store, getActorWithGit, GetLastTouchedID)
}

func resolveCurrentIssueIDFrom(ctx context.Context, searcher currentIssueSearcher, currentActor func() string, fallback func() string) string {
	if searcher == nil {
		return fallback()
	}
	actor := currentActor()
	if actor == "" {
		return fallback()
	}

	for _, status := range []types.Status{types.StatusInProgress, types.StatusHooked} {
		status := status
		filter := types.IssueFilter{
			Status:   &status,
			Assignee: &actor,
		}
		issues, err := searcher.SearchIssues(ctx, "", filter)
		if err == nil && len(issues) > 0 {
			return issues[0].ID
		}
	}

	return fallback()
}

func resolveCurrentIssueIDProxied(ctx context.Context, uw uow.UnitOfWork) string {
	currentActor := getActorWithGit()
	if currentActor == "" {
		return ""
	}
	for _, status := range []types.Status{types.StatusInProgress, types.StatusHooked} {
		st := status
		filter := types.IssueFilter{Status: &st, Assignee: &currentActor}
		page, err := uw.IssueUseCase().SearchIssues(ctx, "", filter)
		if err == nil && len(page.Items) > 0 {
			return page.Items[0].ID
		}
	}
	return ""
}
