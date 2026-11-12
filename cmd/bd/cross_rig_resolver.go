package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/externaldeps"
	"github.com/steveyegge/beads/internal/types"
)

// crossRigTargetOpener opens the workspace at beadsDir read-only, pointed at
// database when it is not empty.
type crossRigTargetOpener func(ctx context.Context, beadsDir, database string) (storage.DoltStorage, error)

// crossRigIssueResolver is the externaldeps.IssueResolver bd wires into the
// external-dependency policy (GH#5784): it reads the bare cross-rig blocking
// targets the source workspace does not hold through that workspace's prefix
// routes (routes.jsonl), the same routes `bd show <id>` follows. routesDir
// names the source workspace's .beads directory and is read at each call; nil
// or "" leaves every target unavailable, which keeps it blocking.
func crossRigIssueResolver(routesDir func() string) externaldeps.IssueResolver {
	return newCrossRigIssueResolver(routesDir, openCrossRigTarget)
}

// newCrossRigIssueResolver is crossRigIssueResolver over open. Each routed
// workspace is opened once per call, read by exact id, and closed before the
// call returns, so a later evaluation observes a foreign close or reopen. A
// target it cannot read is answered unavailable with the reason; only a
// canceled context fails the call.
func newCrossRigIssueResolver(routesDir func() string, open crossRigTargetOpener) externaldeps.IssueResolver {
	return func(ctx context.Context, ids []string) (map[string]*types.Issue, map[string]string, error) {
		found := make(map[string]*types.Issue, len(ids))
		unavailable := make(map[string]string)
		markAll := func(ids []string, reason string) {
			for _, id := range ids {
				unavailable[id] = reason
			}
		}
		dir := ""
		if routesDir != nil {
			dir = routesDir()
		}
		if dir == "" {
			markAll(ids, "no workspace routes to follow")
			return found, unavailable, nil
		}
		routes, err := loadPrefixRoutes(dir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				markAll(ids, "no routes.jsonl in "+dir)
			} else {
				markAll(ids, "reading routes.jsonl: "+err.Error())
			}
			return found, unavailable, nil
		}

		byTarget := make(map[string][]string)
		var targets []string
		for _, id := range ids {
			route := matchPrefixRoute(routes, id)
			if route == nil {
				unavailable[id] = fmt.Sprintf("no route for prefix %q", strings.TrimSuffix(types.ExtractPrefix(id), "-"))
				continue
			}
			if route.Path == "." {
				// The source store was read first and does not hold it.
				unavailable[id] = "not found: its route points to this workspace"
				continue
			}
			target := prefixRouteTargetBeadsDir(dir, route)
			if _, ok := byTarget[target]; !ok {
				targets = append(targets, target)
			}
			byTarget[target] = append(byTarget[target], id)
		}

		for _, target := range targets {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			want := byTarget[target]
			database, err := loadDoltDatabase(target)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				markAll(want, err.Error())
				continue
			}
			store, err := open(ctx, target, database)
			if err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return nil, nil, ctxErr
				}
				markAll(want, fmt.Sprintf("opening %s: %v", target, err))
				continue
			}
			issues, err := store.GetIssuesByIDs(ctx, want)
			_ = store.Close()
			if err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return nil, nil, ctxErr
				}
				markAll(want, fmt.Sprintf("reading %s: %v", target, err))
				continue
			}
			for _, issue := range issues {
				if issue != nil && slices.Contains(want, issue.ID) {
					found[issue.ID] = issue
				}
			}
			for _, id := range want {
				if found[id] == nil {
					unavailable[id] = "not found in " + target
				}
			}
		}
		return found, unavailable, nil
	}
}

// openCrossRigTarget opens a routed cross-rig target read-only, with its
// database selected per open (openForeignStoreReadOnlyDatabase): bd serve may
// reach it from concurrent requests, so it never swaps the process-global
// BEADS_DOLT_SERVER_DATABASE around the open. An empty database leaves the
// target's own resolution in place. The opened store carries no policy
// wrapper, so resolution never recurses.
func openCrossRigTarget(ctx context.Context, beadsDir, database string) (storage.DoltStorage, error) {
	return openForeignStoreReadOnlyDatabase(ctx, beadsDir, database)
}

// crossRigRoutesDir is the launching workspace's .beads directory, whose
// routes.jsonl the policy follows for a bare cross-rig target.
func crossRigRoutesDir() string {
	if dir := resolveCommandBeadsDir(dbPath); dir != "" {
		return dir
	}
	return beads.FindBeadsDir()
}
