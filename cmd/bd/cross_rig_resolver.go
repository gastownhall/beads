package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
// routes (crossRigRoutes: its own routes.jsonl, else the enclosing
// orchestrator's), the same routes `bd show <id>` follows. routesDir names the
// source workspace's .beads directory and is read at each call; nil or ""
// leaves every target unavailable, which keeps it blocking.
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
		dir := ""
		if routesDir != nil {
			dir = routesDir()
		}
		plan := planCrossRigReads(dir, ids)
		found := make(map[string]*types.Issue, len(ids))
		for _, target := range plan.targets {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			if err := readCrossRigTarget(ctx, open, target, plan.byTarget[target], found, plan.unavailable); err != nil {
				return nil, nil, err
			}
		}
		return found, plan.unavailable, nil
	}
}

// crossRigReadPlan groups the ids one resolver call reads by the routed
// workspace that holds them, in first-seen order. unavailable already answers
// every id no route leads to another workspace for.
type crossRigReadPlan struct {
	targets     []string
	byTarget    map[string][]string
	unavailable map[string]string
}

func (p crossRigReadPlan) markAll(ids []string, reason string) {
	for _, id := range ids {
		p.unavailable[id] = reason
	}
}

// planCrossRigReads routes each of ids through the routes of the source
// workspace at dir (crossRigRoutes).
func planCrossRigReads(dir string, ids []string) crossRigReadPlan {
	plan := crossRigReadPlan{byTarget: make(map[string][]string), unavailable: make(map[string]string)}
	if dir == "" {
		plan.markAll(ids, "no workspace routes to follow")
		return plan
	}
	routes, routesDir, err := crossRigRoutes(dir)
	if err != nil {
		plan.markAll(ids, crossRigRoutesUnreadable(dir, routesDir, err))
		return plan
	}
	for _, id := range ids {
		route := matchPrefixRoute(routes, id)
		if route == nil {
			plan.unavailable[id] = fmt.Sprintf("no route for prefix %q", strings.TrimSuffix(types.ExtractPrefix(id), "-"))
			continue
		}
		target := prefixRouteTargetBeadsDir(routesDir, route)
		if sameBeadsDir(target, dir) {
			// The source store was read first and does not hold it.
			plan.unavailable[id] = "not found: its route points to this workspace"
			continue
		}
		if _, ok := plan.byTarget[target]; !ok {
			plan.targets = append(plan.targets, target)
		}
		plan.byTarget[target] = append(plan.byTarget[target], id)
	}
	return plan
}

// readCrossRigTarget reads want from the routed workspace at target into
// found, and answers each id it cannot read there in unavailable. Only a
// canceled context is an error.
func readCrossRigTarget(ctx context.Context, open crossRigTargetOpener, target string, want []string, found map[string]*types.Issue, unavailable map[string]string) error {
	markAll := func(reason string) {
		for _, id := range want {
			unavailable[id] = reason
		}
	}
	database, err := loadDoltDatabase(target)
	if err == nil && database == "" {
		err = fmt.Errorf("%s has no dolt_database field", filepath.Join(target, "metadata.json"))
	}
	if err != nil {
		// An open with no database would fall back to the launching
		// workspace's (BEADS_DOLT_SERVER_DATABASE) and read the wrong rig,
		// which resolveViaPrefixRoutingWithAccess refuses for the same route.
		markAll("target rig has no dolt_database configured: " + err.Error())
		return nil
	}
	store, err := open(ctx, target, database)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		markAll(fmt.Sprintf("opening %s: %v", target, err))
		return nil
	}
	issues, err := store.GetIssuesByIDs(ctx, want)
	_ = store.Close()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		markAll(fmt.Sprintf("reading %s: %v", target, err))
		return nil
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
	return nil
}

// crossRigRoutes loads the prefix routes a bare cross-rig target of the
// workspace at dir resolves through, with the .beads directory they came
// from, whose parent their paths are relative to (prefixRouteTargetBeadsDir).
// It reads the workspace's own routes.jsonl and, when that file does not
// exist, the enclosing orchestrator's ($GT_ROOT/.beads/routes.jsonl): an
// orchestrator such as Gas Town writes routes only at its town root, so a rig
// with none of its own resolves the town's prefixes the way the town root
// does (GH#5784). A file missing from both places wraps fs.ErrNotExist.
func crossRigRoutes(dir string) ([]prefixRoute, string, error) {
	routes, err := loadPrefixRoutes(dir)
	if !errors.Is(err, fs.ErrNotExist) {
		return routes, dir, err
	}
	town := crossRigOrchestratorBeadsDir()
	if town == "" || sameBeadsDir(town, dir) {
		return nil, dir, err
	}
	townRoutes, townErr := loadPrefixRoutes(town)
	if errors.Is(townErr, fs.ErrNotExist) {
		return nil, dir, err
	}
	return townRoutes, town, townErr
}

// crossRigOrchestratorBeadsDir is the enclosing orchestrator's .beads
// directory, $GT_ROOT/.beads (bd imports GT_ROOT from a rig's .beads/.env),
// or "" outside an orchestrator.
func crossRigOrchestratorBeadsDir() string {
	if root := os.Getenv("GT_ROOT"); root != "" {
		return filepath.Join(root, ".beads")
	}
	return ""
}

// crossRigRoutesUnreadable is why every target is unavailable when
// crossRigRoutes(dir) failed with err reading routesDir.
func crossRigRoutesUnreadable(dir, routesDir string, err error) string {
	if !errors.Is(err, fs.ErrNotExist) {
		return "reading " + filepath.Join(routesDir, "routes.jsonl") + ": " + err.Error()
	}
	if town := crossRigOrchestratorBeadsDir(); town != "" && !sameBeadsDir(town, dir) {
		return "no routes.jsonl in " + dir + " or " + town
	}
	return "no routes.jsonl in " + dir
}

// crossRigRoutesFollowable reports whether a bare cross-rig target of the
// workspace at dir could resolve at all: its routes load (crossRigRoutes) and
// one of them leads to another workspace. A view that finds none skips its
// edge read, since no foreign row is possible.
func crossRigRoutesFollowable(dir string) bool {
	if dir == "" {
		return false
	}
	routes, routesDir, err := crossRigRoutes(dir)
	if err != nil {
		return false
	}
	for i := range routes {
		if !sameBeadsDir(prefixRouteTargetBeadsDir(routesDir, &routes[i]), dir) {
			return true
		}
	}
	return false
}

// sameBeadsDir reports whether two .beads paths name one directory.
func sameBeadsDir(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
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
