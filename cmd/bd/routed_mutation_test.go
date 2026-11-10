//go:build cgo

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
	"github.com/steveyegge/beads/issueops"
)

// setupAutoRoute builds a git repo with the given beads.role whose store
// auto-routes, through routingKey (routing.maintainer, routing.contributor,
// ...), to a planning workspace holding the issue "shared-hub" and any
// extraSeeds, and changes into the repo. It returns the repo's store and the
// repo and planning directories.
func setupAutoRoute(t *testing.T, role, routingKey string, extraSeeds ...string) (storage.DoltStorage, string, string) {
	t.Helper()
	initConfigForTest(t)
	neutralizeRoutingModeEnv(t)
	ctx := context.Background()

	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	planningDir := filepath.Join(tmpDir, "planning")

	runCmd(t, tmpDir, "git", "init", repoDir)
	// Force repo-local hooks so tests ignore any global hooksPath override.
	runCmd(t, repoDir, "git", "config", "core.hooksPath", ".git/hooks")
	runCmd(t, repoDir, "git", "config", "beads.role", role)

	primaryStore := newTestStoreIsolatedDB(t, filepath.Join(repoDir, ".beads", "beads.db"), "shared")
	if err := primaryStore.SetConfig(ctx, "routing.mode", "auto"); err != nil {
		t.Fatalf("set routing.mode: %v", err)
	}
	if err := primaryStore.SetConfig(ctx, routingKey, planningDir); err != nil {
		t.Fatalf("set %s: %v", routingKey, err)
	}

	planningStore := newTestStoreIsolatedDB(t, filepath.Join(planningDir, ".beads", "beads.db"), "shared")
	seedIssue(t, ctx, planningStore, "shared-hub")
	for _, id := range extraSeeds {
		seedIssue(t, ctx, planningStore, id)
	}
	if err := planningStore.Close(); err != nil {
		t.Fatalf("close planning store: %v", err)
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	if err := os.Chdir(repoDir); err != nil {
		t.Fatalf("chdir repoDir: %v", err)
	}
	// Role detection reads the process-wide RepoContext cache, which an
	// earlier test in this binary may have built for its own, now-deleted
	// repo. Rebuild it from repoDir.
	resetRepoContextCachesForTest(t)

	return primaryStore, repoDir, planningDir
}

// neutralizeRoutingModeEnv clears the environment variables that override the
// routing.mode a routing test writes into its store. Viper binds
// BEADS_ROUTING_MODE / BD_ROUTING_MODE onto the routing.mode key
// (internal/config), which outranks the store's routing.mode=auto. Hosts that
// export either var (developer shells and agent runtimes do) would otherwise
// read routing.mode=off and never route.
func neutralizeRoutingModeEnv(t *testing.T) {
	t.Helper()
	t.Setenv("BEADS_ROUTING_MODE", "")
	t.Setenv("BD_ROUTING_MODE", "")
}

// routedStoreIsReadOnly reports whether a routed store was opened read-only.
// The tests check this on the store itself because a server-mode read-only
// open does not refuse a write, so a write that goes through cannot tell the
// two opens apart.
func routedStoreIsReadOnly(t *testing.T, s storage.DoltStorage) bool {
	t.Helper()
	routedStore, ok := storage.UnwrapStore(s).(interface{ IsReadOnly() bool })
	if !ok {
		t.Fatalf("routed store %T does not expose read-only state", s)
	}
	return routedStore.IsReadOnly()
}

// requireWritableAutoRoute fails unless the write-intent resolution rr, made
// for what, came through an auto-route that opened its target writable.
func requireWritableAutoRoute(t *testing.T, what string, rr *RoutedResult) {
	t.Helper()
	if !rr.Routed {
		t.Fatalf("%s resolved %s without routing; want the auto-route", what, rr.ResolvedID)
	}
	if routedStoreIsReadOnly(t, rr.Store) {
		t.Fatalf("%s: the auto-route opened its target read-only for a write", what)
	}
}

// TestMaintainerAutoRouteMutationWritesThrough covers the widened
// openAutoRoutedStore behavior for write-intent callers beyond mol bond:
// resolveAndGetIssueForMutation is the resolver bd update uses, and a
// maintainer auto-routed store must open writable so the mutation actually
// commits in the routed database. (TestContributorAutoRouteWritesThrough
// covers the contributor route.)
func TestMaintainerAutoRouteMutationWritesThrough(t *testing.T) {
	primaryStore, _, _ := setupAutoRoute(t, "maintainer", "routing.maintainer")
	ctx := context.Background()

	rr, err := resolveAndGetIssueForMutation(ctx, primaryStore, "shared-hub")
	if err != nil {
		t.Fatalf("resolveAndGetIssueForMutation: %v", err)
	}
	defer rr.Close()
	requireWritableAutoRoute(t, "update", rr)
	if err := rr.Store.UpdateIssue(ctx, rr.ResolvedID, map[string]interface{}{"priority": 0}, "test"); err != nil {
		t.Fatalf("update through the maintainer auto-route: %v", err)
	}
	rr.Close()

	// Verify through a fresh routed read that the update committed in the
	// planning database, not in a doomed in-memory handle.
	reread, err := resolveAndGetIssueWithRouting(ctx, primaryStore, "shared-hub")
	if err != nil {
		t.Fatalf("re-reading the routed issue: %v", err)
	}
	defer reread.Close()
	if reread.Issue.Priority != 0 {
		t.Fatalf("priority = %d after routed update, want 0", reread.Issue.Priority)
	}
}

// TestMaintainerAutoRouteExactMutationWritesThrough is the same pin for
// resolveAndGetIssueForMutationExact, the exact-match resolver bd comment and
// bd comments add use (#5393): the maintainer auto-route must open writable,
// as any other write-intent resolution does, and a comment written through it
// must commit in the routed database.
func TestMaintainerAutoRouteExactMutationWritesThrough(t *testing.T) {
	primaryStore, _, _ := setupAutoRoute(t, "maintainer", "routing.maintainer")
	ctx := context.Background()

	rr, err := resolveAndGetIssueForMutationExact(ctx, primaryStore, "shared-hub")
	if err != nil {
		t.Fatalf("resolveAndGetIssueForMutationExact: %v", err)
	}
	defer rr.Close()
	requireWritableAutoRoute(t, "exact-match comment", rr)
	if _, err := addCommentDirect(ctx, rr.Store, rr.ResolvedID, "test", "routed comment"); err != nil {
		t.Fatalf("comment through the maintainer auto-route: %v", err)
	}
	rr.Close()

	reread, err := resolveAndGetIssueWithRouting(ctx, primaryStore, "shared-hub")
	if err != nil {
		t.Fatalf("re-reading the routed issue: %v", err)
	}
	defer reread.Close()
	comments, err := reread.Store.GetIssueComments(ctx, reread.ResolvedID)
	if err != nil {
		t.Fatalf("reading routed comments: %v", err)
	}
	if len(comments) != 1 || comments[0].Text != "routed comment" {
		t.Fatalf("comments = %+v after the routed comment write, want the one written", comments)
	}
}

// TestAutoRouteCloseTargetWriteIntent covers bd close's shared auto-routed
// store, which resolveCloseTargets opens with the same write intent as
// resolveAndGetIssueForMutation: a maintainer auto-route opens writable and the
// close commits in the routed database. (TestContributorAutoRouteWritesThrough
// covers the contributor route.)
func TestAutoRouteCloseTargetWriteIntent(t *testing.T) {
	t.Run("maintainer_writes_through", func(t *testing.T) {
		primaryStore, _, _ := setupAutoRoute(t, "maintainer", "routing.maintainer")
		ctx := context.Background()

		results, cleanup, err := resolveCloseTargets(ctx, primaryStore, []string{"shared-hub"})
		if err != nil {
			t.Fatalf("resolveCloseTargets: %v", err)
		}
		defer cleanup()
		if len(results) != 1 || !results[0].Routed {
			t.Fatalf("results = %+v, want one auto-routed close target", results)
		}
		if routedStoreIsReadOnly(t, results[0].Store) {
			t.Fatalf("maintainer auto-route opened read-only for bd close")
		}
		if err := results[0].Store.CloseIssue(ctx, results[0].ResolvedID, "done", "test", ""); err != nil {
			t.Fatalf("close through the maintainer auto-route: %v", err)
		}
		cleanup()

		reread, err := resolveAndGetIssueWithRouting(ctx, primaryStore, "shared-hub")
		if err != nil {
			t.Fatalf("re-reading the routed issue: %v", err)
		}
		defer reread.Close()
		if reread.Issue.Status != types.StatusClosed {
			t.Fatalf("status = %s after the routed close, want %s", reread.Issue.Status, types.StatusClosed)
		}
	})
}

// TestContributorAutoRouteWritesThrough pins the contributor side of the
// write-intent resolvers: a contributor's auto-route target is their own
// planning repository, the one bd create writes to, so bd update, bd comment
// and bd close of an issue found there commit there (GH#2345, beads-0km).
// Each route must open writable, since a server-mode read-only open does not
// refuse a write (routedStoreIsReadOnly), while a read through the same route
// stays read-only. An id the route does not hold is still a plain not-found.
func TestContributorAutoRouteWritesThrough(t *testing.T) {
	primaryStore, _, _ := setupAutoRoute(t, "contributor", "routing.contributor")
	ctx := context.Background()

	writes := []struct {
		name  string
		write func(id string) error
	}{
		{"update (resolveAndGetIssueForMutation)", func(id string) error {
			rr, err := resolveAndGetIssueForMutation(ctx, primaryStore, id)
			if err != nil {
				return err
			}
			defer rr.Close()
			requireWritableAutoRoute(t, "update", rr)
			return rr.Store.UpdateIssue(ctx, rr.ResolvedID, map[string]interface{}{"priority": 0}, "test")
		}},
		{"comment (resolveAndGetIssueForMutationExact)", func(id string) error {
			rr, err := resolveAndGetIssueForMutationExact(ctx, primaryStore, id)
			if err != nil {
				return err
			}
			defer rr.Close()
			requireWritableAutoRoute(t, "comment", rr)
			_, err = addCommentDirect(ctx, rr.Store, rr.ResolvedID, "test", "routed comment")
			return err
		}},
		{"close (resolveCloseTargets)", func(id string) error {
			results, cleanup, err := resolveCloseTargets(ctx, primaryStore, []string{id})
			defer cleanup()
			if err != nil {
				return err
			}
			requireWritableAutoRoute(t, "close", results[0])
			return results[0].Store.CloseIssue(ctx, results[0].ResolvedID, "done", "test", "")
		}},
	}
	for _, w := range writes {
		if err := w.write("shared-hub"); err != nil {
			t.Fatalf("%s of a contributor-routed issue: %v", w.name, err)
		}
		if err := w.write("shared-zzzz"); !isNotFoundErr(err) {
			t.Errorf("%s of an id the route does not hold: err = %v, want not-found", w.name, err)
		}
	}

	// Verify through a fresh routed read that every write committed in the
	// planning database.
	reread, err := resolveAndGetIssueWithRouting(ctx, primaryStore, "shared-hub")
	if err != nil {
		t.Fatalf("re-reading the routed issue: %v", err)
	}
	defer reread.Close()
	if !reread.Routed {
		t.Fatalf("re-read resolved shared-hub without routing; want the contributor auto-route")
	}
	if !routedStoreIsReadOnly(t, reread.Store) {
		t.Fatalf("the contributor auto-route opened writable for a read")
	}
	if reread.Issue.Priority != 0 || reread.Issue.Status != types.StatusClosed {
		t.Fatalf("priority = %d, status = %s after the routed writes, want 0 and %s", reread.Issue.Priority, reread.Issue.Status, types.StatusClosed)
	}
	comments, err := reread.Store.GetIssueComments(ctx, reread.ResolvedID)
	if err != nil {
		t.Fatalf("reading routed comments: %v", err)
	}
	if len(comments) != 1 || comments[0].Text != "routed comment" {
		t.Fatalf("comments = %+v after the routed comment write, want the one written", comments)
	}
}

// TestRoutedMutationResolversReportRoutingFailures pins that every
// write-intent resolver reports a route it cannot follow as that failure, not
// as the local "no issue found matching", which would send the user looking
// for an issue that exists. Each routing tier gets a target whose
// metadata.json is unloadable or names no dolt_database, which a write refuses
// on either tier.
func TestRoutedMutationResolversReportRoutingFailures(t *testing.T) {
	t.Run("auto_route_store_fails_to_open", func(t *testing.T) {
		primaryStore, _, planningDir := setupAutoRoute(t, "maintainer", "routing.maintainer")
		// Corrupted after the issue is seeded, so the route resolves but its
		// store cannot open.
		if err := os.WriteFile(filepath.Join(planningDir, ".beads", "metadata.json"), []byte("{not json"), 0o600); err != nil {
			t.Fatalf("corrupt planning metadata.json: %v", err)
		}
		requireMutationResolversReport(t, primaryStore, "shared-hub", "failed to open routed store")
	})

	t.Run("auto_route_target_names_no_database", func(t *testing.T) {
		primaryStore, repoDir, planningDir := setupAutoRoute(t, "contributor", "routing.contributor")
		// With no dolt_database of the target's own, the open would take the
		// launcher's from BEADS_DOLT_SERVER_DATABASE.
		launcherDB, err := loadDoltDatabase(filepath.Join(repoDir, ".beads"))
		if err != nil || launcherDB == "" {
			t.Fatalf("the launcher's metadata.json names no dolt_database (err = %v)", err)
		}
		t.Setenv("BEADS_DOLT_SERVER_DATABASE", launcherDB)
		writeTestMetadata(t, filepath.Join(planningDir, ".beads", "beads.db"), "")
		requireMutationResolversReport(t, primaryStore, "shared-hub", "no dolt_database field")
	})

	t.Run("prefix_route_target_unloadable", func(t *testing.T) {
		initConfigForTest(t)
		tmpDir := t.TempDir()
		townBeadsDir := filepath.Join(tmpDir, ".beads")
		rigBeadsDir := filepath.Join(tmpDir, "rig", ".beads")
		for _, dir := range []string{townBeadsDir, rigBeadsDir} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("create %s: %v", dir, err)
			}
		}
		townDBPath := filepath.Join(townBeadsDir, "dolt")
		townStore := newTestStoreIsolatedDB(t, townDBPath, "hq")
		if err := os.WriteFile(filepath.Join(townBeadsDir, "routes.jsonl"), []byte("{\"prefix\":\"gt-\",\"path\":\"rig\"}\n"), 0o600); err != nil {
			t.Fatalf("write routes.jsonl: %v", err)
		}
		if err := os.WriteFile(filepath.Join(rigBeadsDir, "metadata.json"), []byte("{not json"), 0o600); err != nil {
			t.Fatalf("write rig metadata.json: %v", err)
		}

		oldDbPath := dbPath
		dbPath = townDBPath
		t.Cleanup(func() { dbPath = oldDbPath })
		oldWD, err := os.Getwd()
		if err != nil {
			t.Fatalf("getwd: %v", err)
		}
		t.Cleanup(func() { _ = os.Chdir(oldWD) })
		if err := os.Chdir(tmpDir); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		requireMutationResolversReport(t, townStore, "gt-hub", "no dolt_database configured")
	})
}

// TestRoutedWriteDoesNotCreateTargetDatabase pins that a write-intent routed
// open refuses an embedded target that holds no database yet instead of
// creating one there. The open runs before the lookup it serves, so an id that
// misses would leave an empty database behind, and `bd bootstrap` would then
// take it for the real one and skip the clone from sync.remote. Each tier
// routes to a fresh clone: an embedded metadata.json and no embeddeddolt
// directory.
func TestRoutedWriteDoesNotCreateTargetDatabase(t *testing.T) {
	// Server mode from the environment would send the target's open to a
	// server, which never creates a missing database; this pins the embedded
	// open.
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")

	t.Run("auto_route", func(t *testing.T) {
		primaryStore, _, _ := setupAutoRoute(t, "maintainer", "routing.maintainer")
		targetDir := filepath.Join(t.TempDir(), "fresh-clone")
		targetBeadsDir := filepath.Join(targetDir, ".beads")
		writeEmbeddedMetadata(t, targetBeadsDir, "fresh")
		if err := primaryStore.SetConfig(context.Background(), "routing.maintainer", targetDir); err != nil {
			t.Fatalf("set routing.maintainer: %v", err)
		}

		requireMutationResolversReport(t, primaryStore, "shared-zzzz", "no embedded database")
		requireNoEmbeddedDatabase(t, targetBeadsDir)
	})

	t.Run("prefix_route", func(t *testing.T) {
		initConfigForTest(t)
		tmpDir := t.TempDir()
		townBeadsDir := filepath.Join(tmpDir, ".beads")
		rigBeadsDir := filepath.Join(tmpDir, "rig", ".beads")
		townDBPath := filepath.Join(townBeadsDir, "dolt")
		townStore := newTestStoreIsolatedDB(t, townDBPath, "hq")
		if err := os.WriteFile(filepath.Join(townBeadsDir, "routes.jsonl"), []byte("{\"prefix\":\"gt-\",\"path\":\"rig\"}\n"), 0o600); err != nil {
			t.Fatalf("write routes.jsonl: %v", err)
		}
		writeEmbeddedMetadata(t, rigBeadsDir, "rig")

		oldDbPath := dbPath
		dbPath = townDBPath
		t.Cleanup(func() { dbPath = oldDbPath })
		oldWD, err := os.Getwd()
		if err != nil {
			t.Fatalf("getwd: %v", err)
		}
		t.Cleanup(func() { _ = os.Chdir(oldWD) })
		if err := os.Chdir(tmpDir); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		requireMutationResolversReport(t, townStore, "gt-zzzz", "no embedded database")
		requireNoEmbeddedDatabase(t, rigBeadsDir)
	})
}

// writeEmbeddedMetadata writes the metadata.json of an embedded workspace that
// names database, as a fresh clone has before `bd bootstrap` runs.
func writeEmbeddedMetadata(t *testing.T, beadsDir, database string) {
	t.Helper()
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create %s: %v", beadsDir, err)
	}
	cfg := &configfile.Config{
		Database:     "dolt",
		Backend:      configfile.BackendDolt,
		DoltMode:     configfile.DoltModeEmbedded,
		DoltDatabase: database,
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("write %s metadata.json: %v", beadsDir, err)
	}
}

// requireNoEmbeddedDatabase fails if anything created beadsDir's embedded
// database directory.
func requireNoEmbeddedDatabase(t *testing.T, beadsDir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(beadsDir, "embeddeddolt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s: embeddeddolt stat err = %v after a routed write missed, want it absent", beadsDir, err)
	}
}

// requireMutationResolversReport resolves id through every write-intent
// resolver and fails unless each returns the routing failure containing want.
func requireMutationResolversReport(t *testing.T, localStore storage.DoltStorage, id, want string) {
	t.Helper()
	ctx := context.Background()
	resolvers := []struct {
		name    string
		resolve func() error
	}{
		{"resolveAndGetIssueForMutation", func() error {
			rr, err := resolveAndGetIssueForMutation(ctx, localStore, id)
			if err == nil {
				rr.Close()
			}
			return err
		}},
		{"resolveAndGetIssueForMutationExact", func() error {
			rr, err := resolveAndGetIssueForMutationExact(ctx, localStore, id)
			if err == nil {
				rr.Close()
			}
			return err
		}},
		{"resolveCloseTargets", func() error {
			_, cleanup, err := resolveCloseTargets(ctx, localStore, []string{id})
			cleanup()
			return err
		}},
	}
	for _, r := range resolvers {
		err := r.resolve()
		if err == nil || isNotFoundErr(err) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want the routing failure %q", r.name, err, want)
		}
	}
}

// TestAutoRouteOpensTargetDatabaseDespiteEnv pins that an auto-routed store
// opens the target's own dolt_database. BEADS_DOLT_SERVER_DATABASE outranks
// metadata.json and names the launching workspace's database (set by the user,
// or by preserveRedirectSourceDatabase after a redirect), so without the
// override a routed open connects to the launcher's database, where the issue
// is not, and a writable one would commit there.
func TestAutoRouteOpensTargetDatabaseDespiteEnv(t *testing.T) {
	primaryStore, repoDir, _ := setupAutoRoute(t, "maintainer", "routing.maintainer")
	ctx := context.Background()
	launcherDB, err := loadDoltDatabase(filepath.Join(repoDir, ".beads"))
	if err != nil || launcherDB == "" {
		t.Fatalf("the launcher's metadata.json names no dolt_database (err = %v)", err)
	}
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", launcherDB)

	resolvers := []struct {
		name    string
		resolve func(context.Context, storage.DoltStorage, string) (*RoutedResult, error)
	}{
		{"resolveAndGetIssueWithRouting", resolveAndGetIssueWithRouting},
		{"resolveAndGetIssueForMutation", resolveAndGetIssueForMutation},
	}
	for _, r := range resolvers {
		rr, err := r.resolve(ctx, primaryStore, "shared-hub")
		if err != nil {
			t.Fatalf("%s with BEADS_DOLT_SERVER_DATABASE=%s: %v", r.name, launcherDB, err)
		}
		routed := rr.Routed
		rr.Close()
		if !routed {
			t.Fatalf("%s resolved shared-hub without routing", r.name)
		}
		if got := os.Getenv("BEADS_DOLT_SERVER_DATABASE"); got != launcherDB {
			t.Fatalf("%s left BEADS_DOLT_SERVER_DATABASE = %q, want %q restored", r.name, got, launcherDB)
		}
	}
}

// TestAutoRouteMissWithoutMetadataIgnoresEnv pins that an auto-route to a
// target with no metadata.json opens the only database the target holds,
// whatever BEADS_DOLT_SERVER_DATABASE says at use. Contributor setup names that
// database after the variable when it is set and the default database
// otherwise, and the target's open resolves its name the same way, the
// variable first, so a launcher whose variable differed from setup's used to
// read a database that is not there and, writing, create one beside setup's:
// an embedded writable open creates the database it is given when the target
// holds another. Each target has the shape autoConfigureForkContributor
// leaves, one embedded database and no metadata.json, and a miss, read or
// written, reports not found and leaves it holding only that database.
func TestAutoRouteMissWithoutMetadataIgnoresEnv(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	primaryStore, repoDir, _ := setupAutoRoute(t, "maintainer", "routing.maintainer")
	launcherDB, err := loadDoltDatabase(filepath.Join(repoDir, ".beads"))
	if err != nil || launcherDB == "" || launcherDB == configfile.DefaultDoltDatabase {
		t.Fatalf("the launcher's metadata.json names dolt_database %q (err = %v), want one of its own", launcherDB, err)
	}

	tests := []struct {
		name     string
		targetDB string // the database setup created
		useDB    string // BEADS_DOLT_SERVER_DATABASE at use
	}{
		{"setup_without_variable", configfile.DefaultDoltDatabase, launcherDB},
		{"setup_with_variable", launcherDB, launcherDB},
		{"setup_with_variable_unset_at_use", launcherDB, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("BEADS_DOLT_SERVER_DATABASE", tt.useDB)
			targetDir := filepath.Join(t.TempDir(), "planning")
			seedEmbeddedSchema(t, targetDir, tt.targetDB)
			if err := primaryStore.SetConfig(context.Background(), "routing.maintainer", targetDir); err != nil {
				t.Fatalf("set routing.maintainer: %v", err)
			}

			for _, writable := range []bool{false, true} {
				rr, err := resolveAndGetIssueWithRoutingAccess(context.Background(), primaryStore, "shared-zzzz", writable)
				if err == nil {
					rr.Close()
					t.Fatalf("resolved shared-zzzz (writable %v), which no workspace holds", writable)
				}
				if !isNotFoundErr(err) {
					t.Fatalf("resolve shared-zzzz (writable %v): err = %v, want not found", writable, err)
				}
			}
			entries, err := os.ReadDir(filepath.Join(targetDir, ".beads", "embeddeddolt"))
			if err != nil {
				t.Fatalf("read the target's embeddeddolt: %v", err)
			}
			var databases []string
			for _, entry := range entries {
				if entry.IsDir() {
					databases = append(databases, entry.Name())
				}
			}
			if len(databases) != 1 || databases[0] != tt.targetDB {
				t.Fatalf("the target holds databases %v after a routed miss, want only %q", databases, tt.targetDB)
			}
		})
	}
}

// TestRoutedWriteRetryCommitsFailedCommit pins that the retry of a routed write
// whose commit failed commits it. The write reaches the target's working set
// before the commit runs, so the retry finds the issue already released or
// closed and refuses; nothing else commits a routed target, so the refusal
// commits the pending write first. The run whose commit failed reports no
// success: bd unclaim neither prints the release nor emits it in --json. The
// launcher and the rig are embedded, and the commit fails on an invalid
// auto-commit mode, after the write.
func TestRoutedWriteRetryCommitsFailedCommit(t *testing.T) {
	saveStorageMode(t)
	saveAndRestoreGlobals(t)
	serverMode, proxiedServerMode = false, false
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	initConfigForTest(t)
	ctx := context.Background()

	actorMu.Lock()
	savedActor, savedPending := actor, actorGitFallbackPending
	actor, actorGitFallbackPending = "alice", false
	actorMu.Unlock()
	savedRootCtx, savedJSON, savedDidWrite := rootCtx, jsonOutput, commandDidWrite.Load()
	rootCtx = ctx
	t.Cleanup(func() {
		actorMu.Lock()
		actor, actorGitFallbackPending = savedActor, savedPending
		actorMu.Unlock()
		rootCtx, jsonOutput = savedRootCtx, savedJSON
		commandDidWrite.Store(savedDidWrite)
	})

	tmpDir := t.TempDir()
	townBeadsDir := filepath.Join(tmpDir, ".beads")
	rigDir := filepath.Join(tmpDir, "rig")
	rigBeadsDir := filepath.Join(rigDir, ".beads")

	writeEmbeddedMetadata(t, rigBeadsDir, "rig")
	seedEmbeddedSchema(t, rigDir, "rig")
	rig, err := embeddeddolt.Open(ctx, rigBeadsDir, "rig", "main")
	if err != nil {
		t.Fatalf("open rig: %v", err)
	}
	if err := rig.SetConfig(ctx, "issue_prefix", "gt"); err != nil {
		t.Fatalf("set rig issue_prefix: %v", err)
	}
	for _, issue := range []*types.Issue{
		{ID: "gt-rel1", Status: types.StatusInProgress, Assignee: "alice"},
		{ID: "gt-rel2", Status: types.StatusInProgress, Assignee: "alice"},
		{ID: "gt-resp", Status: types.StatusOpen},
		{ID: "gt-dism", Status: types.StatusOpen},
	} {
		issue.Title, issue.Priority, issue.IssueType = "routed "+issue.ID, 2, types.TypeTask
		if err := rig.CreateIssue(ctx, issue, "test"); err != nil {
			t.Fatalf("seed %s: %v", issue.ID, err)
		}
	}
	if err := rig.Commit(ctx, "seed"); err != nil {
		t.Fatalf("commit rig seed: %v", err)
	}
	if err := rig.Close(); err != nil {
		t.Fatalf("close rig: %v", err)
	}

	writeEmbeddedMetadata(t, townBeadsDir, "hq")
	seedEmbeddedSchema(t, tmpDir, "hq")
	town, err := embeddeddolt.Open(ctx, townBeadsDir, "hq", "main")
	if err != nil {
		t.Fatalf("open town: %v", err)
	}
	t.Cleanup(func() { _ = town.Close() })
	if err := town.SetConfig(ctx, "issue_prefix", "hq"); err != nil {
		t.Fatalf("set town issue_prefix: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townBeadsDir, "routes.jsonl"), []byte("{\"prefix\":\"gt-\",\"path\":\"rig\"}\n"), 0o600); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}
	store = town
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()
	dbPath = filepath.Join(townBeadsDir, "dolt")
	t.Chdir(tmpDir)

	unclaim := func(id string) error { return unclaimCmd.RunE(unclaimCmd, []string{id}) }
	released := func(issue *types.Issue) bool { return issue.Assignee == "" && issue.Status == types.StatusOpen }
	closed := func(issue *types.Issue) bool { return issue.Status == types.StatusClosed }
	cases := []struct {
		name    string
		id      string
		json    bool
		run     func(id string) error
		written func(*types.Issue) bool
		refusal string // the retry's, on the write it finds pending
	}{
		{"unclaim", "gt-rel1", false, unclaim, released, "issue gt-rel1 is not assigned"},
		{"unclaim --json", "gt-rel2", true, unclaim, released, "issue gt-rel2 is not assigned"},
		{"human respond", "gt-resp", false, func(id string) error {
			return humanRespondCmd.RunE(humanRespondCmd, []string{id, "approved"})
		}, closed, "issue gt-resp is already closed"},
		{"human dismiss", "gt-dism", false, func(id string) error {
			return humanDismissCmd.RunE(humanDismissCmd, []string{id})
		}, closed, "issue gt-dism is already closed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			jsonOutput = tc.json
			var err error
			run := func() (string, string) {
				return captureStdio(t, func() { err = tc.run(tc.id) })
			}

			doltAutoCommit = "bogus"
			stdout, stderr := run()
			if err == nil || stdout != "" {
				t.Fatalf("run whose commit failed: err = %v, stdout = %q; want a failure and no output", err, stdout)
			}
			if !strings.Contains(stderr, `invalid --dolt-auto-commit="bogus"`) {
				t.Fatalf("run whose commit failed: stderr = %q, want the commit failure", stderr)
			}
			if tc.written(committedIssue(t, rigBeadsDir, "rig", tc.id)) {
				t.Fatalf("%s: the rig's HEAD holds the write whose commit failed", tc.id)
			}

			doltAutoCommit = string(doltAutoCommitOn)
			_, stderr = run()
			if err == nil || !strings.Contains(stderr, tc.refusal) {
				t.Fatalf("retry: err = %v, stderr = %q; want the refusal %q", err, stderr, tc.refusal)
			}
			if !tc.written(committedIssue(t, rigBeadsDir, "rig", tc.id)) {
				t.Fatalf("%s: the retry left the write uncommitted", tc.id)
			}
		})
	}
}

// setupAutoRouteWithHydratedCopy extends setupAutoRoute with the rows the bug
// report's workflow leaves in the project: an exact copy of the planning
// store's "shared-hub" and of each extraCopies id, imported with the options
// bd repo sync imports with (CreateIssuesWithFullOptions, prefix validation
// skipped) and carrying the planning repo's path as their source marker and
// the planning rows' own creation times, so each copy is identifiable as its
// planning row's mirror. It returns the project's store and the repo and
// planning directories.
func setupAutoRouteWithHydratedCopy(t *testing.T, role, routingKey string, extraCopies ...string) (storage.DoltStorage, string, string) {
	t.Helper()
	primaryStore, repoDir, planningDir := setupAutoRoute(t, role, routingKey, extraCopies...)
	ctx := context.Background()

	// Read the planning rows through the same foreign read a routed open
	// uses, so the copies' provenance is exactly what a real import recorded.
	planningStore, err := openForeignStore(ctx, filepath.Join(planningDir, ".beads"), foreignRead, "test fixture open")
	if err != nil {
		t.Fatalf("open the planning store for the import: %v", err)
	}
	defer func() { _ = planningStore.Close() }()
	ids := []string{"shared-hub"}
	ids = append(ids, extraCopies...)
	rows := make([]*types.Issue, 0, len(ids))
	for _, id := range ids {
		owner, err := planningStore.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("read the planning row %s to copy: %v", id, err)
		}
		rows = append(rows, owner)
	}

	for _, owner := range rows {
		copy := *owner
		copy.SourceRepo = planningDir
		if err := primaryStore.CreateIssuesWithFullOptions(ctx, []*types.Issue{&copy}, "repo-sync", storage.BatchCreateOptions{
			SkipPrefixValidation: true,
		}); err != nil {
			t.Fatalf("import the planning row %s's copy into the project: %v", owner.ID, err)
		}
	}
	return primaryStore, repoDir, planningDir
}

// reopenAutoRouteTarget opens the planning store read-only again, the way
// bd's next routed read would, so durability is asserted through a fresh
// handle rather than the one the test just mutated through.
func reopenAutoRouteTarget(t *testing.T, planningDir string) storage.DoltStorage {
	t.Helper()
	s, err := openForeignStore(context.Background(), filepath.Join(planningDir, ".beads"), foreignRead, "test fixture open")
	if err != nil {
		t.Fatalf("reopen the planning store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// requireRedirectedCopy fails unless rr is the owner redirection of a
// hydrated copy: routed, and marked as a redirected copy with its owner path.
func requireRedirectedCopy(t *testing.T, what string, rr *RoutedResult, planningDir string) {
	t.Helper()
	if !rr.Routed || !rr.RedirectedCopy {
		t.Fatalf("%s resolved the project copy as local (Routed=%v, RedirectedCopy=%v); want the redirect to the owner in %s", what, rr.Routed, rr.RedirectedCopy, planningDir)
	}
	if rr.RedirectedCopyOwner != planningDir {
		t.Fatalf("%s redirected to owner %q, want %q", what, rr.RedirectedCopyOwner, planningDir)
	}
}

// TestAutoRouteResolvesPlanningRepoMirrorToOwner pins the two-store selection
// the bug report's workflow needs (GH#6058): with an active auto-route to a
// planning store and a project copy of one of its rows — what bd repo sync
// hydrated, marked with the planning repo's path and creation time — every
// id-based resolver hands the command the planning store's own row, so the
// change lands where bd list reads from and the project copy stays as it was.
// At the base the resolvers return the copy (Routed=false), which is the bug:
// a close succeeds against the copy while the routed list keeps showing the
// owner open.
func TestAutoRouteResolvesPlanningRepoMirrorToOwner(t *testing.T) {
	t.Run("close (resolveCloseTargets)", func(t *testing.T) {
		primaryStore, _, planningDir := setupAutoRouteWithHydratedCopy(t, "maintainer", "routing.maintainer")
		ctx := context.Background()

		results, cleanup, err := resolveCloseTargets(ctx, primaryStore, []string{"shared-hub"})
		if err != nil {
			t.Fatalf("resolveCloseTargets of a hydrated copy: %v", err)
		}
		defer cleanup()
		if len(results) != 1 {
			t.Fatalf("results = %d, want 1", len(results))
		}
		requireRedirectedCopy(t, "close", results[0], planningDir)
		if routedStoreIsReadOnly(t, results[0].Store) {
			t.Fatalf("the owner redirect opened the planning store read-only for bd close")
		}
		if err := results[0].Store.CloseIssue(ctx, results[0].ResolvedID, "done", "test", ""); err != nil {
			t.Fatalf("close through the owner redirect: %v", err)
		}
		cleanup()

		owner, err := reopenAutoRouteTarget(t, planningDir).GetIssue(ctx, "shared-hub")
		if err != nil {
			t.Fatalf("reread the planning row after the routed close: %v", err)
		}
		if owner.Status != types.StatusClosed {
			t.Fatalf("planning status = %s after the routed close, want %s", owner.Status, types.StatusClosed)
		}
		// The project copy is untouched: still open, still the stale mirror.
		copyRow, err := primaryStore.GetIssue(ctx, "shared-hub")
		if err != nil {
			t.Fatalf("reread the project copy: %v", err)
		}
		if copyRow.Status != types.StatusOpen {
			t.Fatalf("project copy status = %s after the routed close, want it left %s", copyRow.Status, types.StatusOpen)
		}
	})

	t.Run("update (resolveAndGetIssueForMutation)", func(t *testing.T) {
		primaryStore, _, planningDir := setupAutoRouteWithHydratedCopy(t, "maintainer", "routing.maintainer")
		ctx := context.Background()

		result, err := resolveAndGetIssueForMutation(ctx, primaryStore, "shared-hub")
		if err != nil {
			t.Fatalf("resolveAndGetIssueForMutation of a hydrated copy: %v", err)
		}
		requireRedirectedCopy(t, "update", result, planningDir)
		if routedStoreIsReadOnly(t, result.Store) {
			t.Fatalf("the owner redirect opened the planning store read-only for bd update")
		}
		if err := result.Store.UpdateIssue(ctx, result.ResolvedID, map[string]interface{}{"title": "owner's new title"}, "test"); err != nil {
			t.Fatalf("update through the owner redirect: %v", err)
		}
		result.Close()

		owner, err := reopenAutoRouteTarget(t, planningDir).GetIssue(ctx, "shared-hub")
		if err != nil {
			t.Fatalf("reread the planning row after the routed update: %v", err)
		}
		if owner.Title != "owner's new title" {
			t.Fatalf("planning title = %q after the routed update, want %q", owner.Title, "owner's new title")
		}
		// The project copy keeps the stale title it was hydrated with.
		copyRow, err := primaryStore.GetIssue(ctx, "shared-hub")
		if err != nil {
			t.Fatalf("reread the project copy: %v", err)
		}
		if copyRow.Title != "test issue shared-hub" {
			t.Fatalf("project copy title = %q after the routed update, want it left %q", copyRow.Title, "test issue shared-hub")
		}
	})

	t.Run("comment (resolveAndGetIssueForMutationExact)", func(t *testing.T) {
		primaryStore, _, planningDir := setupAutoRouteWithHydratedCopy(t, "maintainer", "routing.maintainer")
		ctx := context.Background()

		result, err := resolveAndGetIssueForMutationExact(ctx, primaryStore, "shared-hub")
		if err != nil {
			t.Fatalf("resolveAndGetIssueForMutationExact of a hydrated copy: %v", err)
		}
		requireRedirectedCopy(t, "comment", result, planningDir)
		if routedStoreIsReadOnly(t, result.Store) {
			t.Fatalf("the owner redirect opened the planning store read-only for bd comment")
		}
		if _, err := addCommentDirect(ctx, result.Store, result.ResolvedID, "test", "comment through the redirect"); err != nil {
			t.Fatalf("comment through the owner redirect: %v", err)
		}
		result.Close()

		comments, err := reopenAutoRouteTarget(t, planningDir).GetIssueComments(ctx, "shared-hub")
		if err != nil {
			t.Fatalf("reread the planning comments after the routed comment: %v", err)
		}
		if len(comments) != 1 || comments[0].Text != "comment through the redirect" {
			t.Fatalf("planning comments = %+v, want the one written through the redirect", comments)
		}
		copyComments, err := primaryStore.GetIssueComments(ctx, "shared-hub")
		if err != nil {
			t.Fatalf("reread the project copy's comments: %v", err)
		}
		if len(copyComments) != 0 {
			t.Fatalf("project copy comments = %+v, want it left without any", copyComments)
		}
	})

	t.Run("read (resolveAndGetIssueWithRouting)", func(t *testing.T) {
		primaryStore, _, planningDir := setupAutoRouteWithHydratedCopy(t, "maintainer", "routing.maintainer")
		ctx := context.Background()

		// Diverge the owner from the copy, the way any planning-side change
		// does: bd show of the copy must answer with the owner's current
		// state, not the stale mirror.
		planningStore, err := openForeignStore(ctx, filepath.Join(planningDir, ".beads"), foreignWrite, "test fixture open")
		if err != nil {
			t.Fatalf("open the planning store for the diverging update: %v", err)
		}
		if err := planningStore.UpdateIssue(ctx, "shared-hub", map[string]interface{}{"title": "planning moved on"}, "test"); err != nil {
			t.Fatalf("update the planning row: %v", err)
		}
		if err := planningStore.Close(); err != nil {
			t.Fatalf("close the planning store: %v", err)
		}

		result, err := resolveAndGetIssueWithRouting(ctx, primaryStore, "shared-hub")
		if err != nil {
			t.Fatalf("resolveAndGetIssueWithRouting of a hydrated copy: %v", err)
		}
		defer result.Close()
		requireRedirectedCopy(t, "show", result, planningDir)
		if result.Issue.Title != "planning moved on" {
			t.Fatalf("resolved title = %q, want the planning owner's current state %q", result.Issue.Title, "planning moved on")
		}
		copyRow, err := primaryStore.GetIssue(ctx, "shared-hub")
		if err != nil {
			t.Fatalf("reread the project copy: %v", err)
		}
		if copyRow.Title != "test issue shared-hub" {
			t.Fatalf("project copy title = %q after the routed read, want it left %q", copyRow.Title, "test issue shared-hub")
		}
	})
}

// importPlanningCopy reads the planning row "shared-hub" through the foreign
// read a routed open uses and imports a copy of it into the project store with
// the given source marker and creation time — what bd repo sync hydrated, with
// exactly the knobs the edge cases below vary.
func importPlanningCopy(t *testing.T, primaryStore storage.DoltStorage, planningDir, marker string, createdAt func(owner time.Time) time.Time) {
	t.Helper()
	ctx := context.Background()
	planningStore, err := openForeignStore(ctx, filepath.Join(planningDir, ".beads"), foreignRead, "test fixture open")
	if err != nil {
		t.Fatalf("open the planning store for the import: %v", err)
	}
	defer func() { _ = planningStore.Close() }()
	owner, err := planningStore.GetIssue(ctx, "shared-hub")
	if err != nil {
		t.Fatalf("read the planning row to copy: %v", err)
	}
	cp := *owner
	cp.SourceRepo = marker
	if createdAt != nil {
		cp.CreatedAt = createdAt(owner.CreatedAt)
	}
	if err := primaryStore.CreateIssuesWithFullOptions(ctx, []*types.Issue{&cp}, "repo-sync", storage.BatchCreateOptions{
		SkipPrefixValidation: true,
	}); err != nil {
		t.Fatalf("import the copy into the project: %v", err)
	}
}

// TestHydratedCopyRedirectEdgeCases pins the identity checks the owner
// redirect requires (GH#6058): a local row redirects to the active auto-route
// target only when its source marker names that target and its id and
// creation time identify it as that target's row. Every other row keeps its
// local resolution, so no command mutates a foreign row by mistake, and a
// local resolution really does write the project store. The controls in the
// table prove the fixture redirects when the checks pass, so the local
// expectations are not vacuous.
func TestHydratedCopyRedirectEdgeCases(t *testing.T) {
	cases := []struct {
		name       string
		routingKey string                                   // route config naming the target; default routing.maintainer
		routeOff   bool                                     // routing.mode=off: no active route at all
		target     func(repoDir, planningDir string) string // what the route names; default planningDir
		marker     func(repoDir, planningDir string) string
		createdAt  func(owner time.Time) time.Time
		wantLocal  bool
	}{
		{
			name:      "matching marker and creation redirect (control)",
			marker:    func(_, planningDir string) string { return planningDir },
			createdAt: func(owner time.Time) time.Time { return owner },
		},
		{
			name:      "empty marker stays local",
			marker:    func(_, _ string) string { return "" },
			createdAt: func(owner time.Time) time.Time { return owner },
			wantLocal: true,
		},
		{
			name:      "dot marker stays local",
			marker:    func(_, _ string) string { return "." },
			createdAt: func(owner time.Time) time.Time { return owner },
			wantLocal: true,
		},
		{
			name: "different repo marker stays local",
			marker: func(repoDir, _ string) string {
				return filepath.Join(filepath.Dir(repoDir), "elsewhere")
			},
			createdAt: func(owner time.Time) time.Time { return owner },
			wantLocal: true,
		},
		{
			name:      "differing creation time stays local",
			marker:    func(_, planningDir string) string { return planningDir },
			createdAt: func(owner time.Time) time.Time { return owner.Add(time.Minute) },
			wantLocal: true,
		},
		{
			name: "canonical path alias in the marker redirects (control)",
			marker: func(_, planningDir string) string {
				alias := planningDir + "-alias"
				if err := os.Symlink(planningDir, alias); err != nil {
					t.Fatalf("symlink %s -> %s: %v", alias, planningDir, err)
				}
				return alias
			},
			createdAt: func(owner time.Time) time.Time { return owner },
		},
		{
			name:      "no active route stays local",
			routeOff:  true,
			marker:    func(_, planningDir string) string { return planningDir },
			createdAt: func(owner time.Time) time.Time { return owner },
			wantLocal: true,
		},
		{
			name:       "default auto-route redirects (control)",
			routingKey: "routing.default",
			marker:     func(_, planningDir string) string { return planningDir },
			createdAt:  func(owner time.Time) time.Time { return owner },
		},
		{
			name:      "target equal to this workspace stays local",
			target:    func(repoDir, _ string) string { return repoDir },
			marker:    func(repoDir, _ string) string { return repoDir },
			createdAt: func(owner time.Time) time.Time { return owner },
			wantLocal: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			routingKey := tc.routingKey
			if routingKey == "" {
				routingKey = "routing.maintainer"
			}
			primaryStore, repoDir, planningDir := setupAutoRoute(t, "maintainer", routingKey)
			ctx := context.Background()
			if tc.target != nil {
				if err := primaryStore.SetConfig(ctx, routingKey, tc.target(repoDir, planningDir)); err != nil {
					t.Fatalf("retarget %s: %v", routingKey, err)
				}
			}
			if tc.routeOff {
				if err := primaryStore.SetConfig(ctx, "routing.mode", "off"); err != nil {
					t.Fatalf("set routing.mode=off: %v", err)
				}
			}
			importPlanningCopy(t, primaryStore, planningDir, tc.marker(repoDir, planningDir), tc.createdAt)

			result, err := resolveAndGetIssueForMutationExact(ctx, primaryStore, "shared-hub")
			if err != nil {
				t.Fatalf("resolveAndGetIssueForMutationExact: %v", err)
			}
			defer result.Close()
			if !tc.wantLocal {
				requireRedirectedCopy(t, tc.name, result, planningDir)
				return
			}
			if result.Routed {
				t.Fatalf("%s: resolution routed to %q; want it kept local", tc.name, result.RedirectedCopyOwner)
			}
			// A row kept local writes the project store only: the foreign
			// planning row must not feel the edit.
			if err := result.Store.UpdateIssue(ctx, result.ResolvedID, map[string]interface{}{"title": "local edit"}, "test"); err != nil {
				t.Fatalf("update through the kept-local resolution: %v", err)
			}
			owner, err := reopenAutoRouteTarget(t, planningDir).GetIssue(ctx, "shared-hub")
			if err != nil {
				t.Fatalf("reread the planning row after the local update: %v", err)
			}
			if owner.Title == "local edit" {
				t.Fatalf("%s: the local update reached the planning store's row", tc.name)
			}
		})
	}

	t.Run("inherited marker on a row the owner never held", func(t *testing.T) {
		primaryStore, _, planningDir := setupAutoRoute(t, "maintainer", "routing.maintainer")
		ctx := context.Background()

		// A discovered-from child inherits its parent's source marker without
		// the planning store ever holding its row.
		child := &types.Issue{
			ID:         "shared-hub-child",
			Title:      "discovered-from child",
			Status:     types.StatusOpen,
			Priority:   2,
			IssueType:  types.TypeTask,
			SourceRepo: planningDir,
			CreatedAt:  time.Now().UTC(),
		}
		if err := primaryStore.CreateIssuesWithFullOptions(ctx, []*types.Issue{child}, "repo-sync", storage.BatchCreateOptions{
			SkipPrefixValidation: true,
		}); err != nil {
			t.Fatalf("import the child row: %v", err)
		}

		for _, resolve := range []struct {
			what string
			call func() (*RoutedResult, error)
		}{
			{"mutation (resolveAndGetIssueForMutationExact)", func() (*RoutedResult, error) {
				return resolveAndGetIssueForMutationExact(ctx, primaryStore, "shared-hub-child")
			}},
			{"read (resolveAndGetIssueWithRouting)", func() (*RoutedResult, error) {
				return resolveAndGetIssueWithRouting(ctx, primaryStore, "shared-hub-child")
			}},
		} {
			result, err := resolve.call()
			if err != nil {
				t.Fatalf("%s of the inherited-marker row: %v", resolve.what, err)
			}
			if result.Routed {
				t.Fatalf("%s routed the inherited-marker row to %q; want it kept local", resolve.what, result.RedirectedCopyOwner)
			}
			if result.Issue.ID != "shared-hub-child" {
				t.Fatalf("%s returned issue %q; want the local child", resolve.what, result.Issue.ID)
			}
			result.Close()
		}

		// Missing owner, local row: the cleanup delete bd delete runs still
		// works, locally only.
		result, err := resolveAndGetIssueForMutationExact(ctx, primaryStore, "shared-hub-child")
		if err != nil {
			t.Fatalf("resolve the child for the cleanup delete: %v", err)
		}
		defer result.Close()
		deleter, err := result.Store.Deleter()
		if err != nil {
			t.Fatalf("Deleter for the local row: %v", err)
		}
		opsCtx, err := issueOpsContext(ctx)
		if err != nil {
			t.Fatalf("issueOpsContext: %v", err)
		}
		if _, err := deleter.Delete(opsCtx, issueops.DeleteRequest{Actor: "test", IDs: []string{result.ResolvedID}, Force: true}); err != nil {
			t.Fatalf("local-only cleanup delete of the child: %v", err)
		}
		if _, err := primaryStore.GetIssue(ctx, "shared-hub-child"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("the local-only cleanup delete did not remove the row: err = %v", err)
		}
	})

	t.Run("native row stays operable while the planning store is unavailable", func(t *testing.T) {
		primaryStore, _, planningDir := setupAutoRoute(t, "maintainer", "routing.maintainer")
		ctx := context.Background()
		seedIssue(t, ctx, primaryStore, "local-only")

		// The planning workspace disappears entirely; a native row, with no
		// source marker, must keep resolving and mutating locally.
		if err := os.RemoveAll(planningDir); err != nil {
			t.Fatalf("remove the planning workspace: %v", err)
		}
		result, err := resolveAndGetIssueForMutationExact(ctx, primaryStore, "local-only")
		if err != nil {
			t.Fatalf("resolve a native row with the planning store gone: %v", err)
		}
		defer result.Close()
		if result.Routed {
			t.Fatalf("the native row routed to %q with no marker and no planning store", result.RedirectedCopyOwner)
		}
		if err := result.Store.CloseIssue(ctx, result.ResolvedID, "done", "test", ""); err != nil {
			t.Fatalf("close the native row with the planning store gone: %v", err)
		}
	})

	t.Run("partial id resolves locally once, then exactly in the owner", func(t *testing.T) {
		primaryStore, _, planningDir := setupAutoRouteWithHydratedCopy(t, "maintainer", "routing.maintainer")
		ctx := context.Background()

		result, err := resolveAndGetIssueForMutation(ctx, primaryStore, "shared-h")
		if err != nil {
			t.Fatalf("resolveAndGetIssueForMutation by partial id: %v", err)
		}
		defer result.Close()
		requireRedirectedCopy(t, "partial id", result, planningDir)
		if result.ResolvedID != "shared-hub" {
			t.Fatalf("partial id resolved to %q, want the owner's full id after one local resolution", result.ResolvedID)
		}

		// The exact resolver still refuses the abbreviation instead of
		// handing a delete or comment something the id only abbreviates.
		if _, err := resolveAndGetIssueForMutationExact(ctx, primaryStore, "shared-h"); !errors.Is(err, utils.ErrAbbreviatedIDNotAllowed) {
			t.Fatalf("exact resolver error = %v, want utils.ErrAbbreviatedIDNotAllowed", err)
		}
	})
}

// TestSameOwnerCreationIdentifiesOnlyRealMatches pins the creation-time half
// of the owner identity directly. A stored row can no longer hold a zero
// creation time (PrepareIssueForInsert defaults it), so the zero cases are
// unit assertions: the check must refuse to identify anything on a zero time
// or a missing row, where no store round trip can demonstrate it.
func TestSameOwnerCreationIdentifiesOnlyRealMatches(t *testing.T) {
	created := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	owner := &types.Issue{CreatedAt: created}

	if !sameOwnerCreation(&types.Issue{CreatedAt: created.Add(500 * time.Millisecond)}, owner) {
		t.Fatal("sub-second differences must identify: storage keeps second precision")
	}
	if sameOwnerCreation(&types.Issue{CreatedAt: created.Add(time.Minute)}, owner) {
		t.Fatal("a creation a minute apart identified an owner")
	}
	if sameOwnerCreation(&types.Issue{CreatedAt: time.Time{}}, owner) {
		t.Fatal("a zero creation time on the copy identified an owner")
	}
	if sameOwnerCreation(&types.Issue{CreatedAt: created}, &types.Issue{CreatedAt: time.Time{}}) {
		t.Fatal("a zero creation time on the owner identified the copy")
	}
	if sameOwnerCreation(nil, owner) || sameOwnerCreation(&types.Issue{CreatedAt: created}, nil) {
		t.Fatal("a missing row identified an owner")
	}
}

// TestMutationOfHydratedCopyRefusesUnreachableOwner pins the failure mode the
// owner redirect must have (GH#6058): when the copy's source marker names the
// active target but that target cannot be opened, every mutation resolver
// refuses — reporting success for a write that never reached the owner is the
// silent no-op this change exists to end — naming the target and leaving the
// project row untouched. A read still answers from the copy: a busy or broken
// planning store must not break bd show.
func TestMutationOfHydratedCopyRefusesUnreachableOwner(t *testing.T) {
	primaryStore, _, planningDir := setupAutoRouteWithHydratedCopy(t, "maintainer", "routing.maintainer")
	ctx := context.Background()
	// Corrupted after the copy is imported, so the marker matches and the
	// owner's open fails exactly where the redirect needs it.
	if err := os.WriteFile(filepath.Join(planningDir, ".beads", "metadata.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt planning metadata.json: %v", err)
	}

	for _, resolve := range []struct {
		name string
		call func() (*RoutedResult, error)
	}{
		{"exact mutation (bd delete, bd comment)", func() (*RoutedResult, error) {
			return resolveAndGetIssueForMutationExact(ctx, primaryStore, "shared-hub")
		}},
		{"ordinary mutation (bd update)", func() (*RoutedResult, error) {
			return resolveAndGetIssueForMutation(ctx, primaryStore, "shared-hub")
		}},
		{"close (resolveCloseTargets)", func() (*RoutedResult, error) {
			results, cleanup, err := resolveCloseTargets(ctx, primaryStore, []string{"shared-hub"})
			if err == nil {
				cleanup()
				if len(results) != 1 {
					t.Fatalf("resolveCloseTargets returned %d results, want 1", len(results))
				}
				return results[0], nil
			}
			return nil, err
		}},
	} {
		result, err := resolve.call()
		if err == nil {
			if result != nil {
				result.Close()
			}
			t.Fatalf("%s of the copy succeeded while the planning store cannot open; want a refusal naming %s", resolve.name, planningDir)
		}
		if !strings.Contains(err.Error(), planningDir) || !strings.Contains(err.Error(), "failed to open routed store") {
			t.Fatalf("%s error = %v; want it to name the planning store and its open failure", resolve.name, err)
		}
		// The refusal leaves the project row unchanged.
		copyRow, err := primaryStore.GetIssue(ctx, "shared-hub")
		if err != nil {
			t.Fatalf("reread the project copy after the refusal: %v", err)
		}
		if copyRow.Status != types.StatusOpen {
			t.Fatalf("project copy status = %s after %s's refusal, want it left %s", copyRow.Status, resolve.name, types.StatusOpen)
		}
	}

	read, err := resolveAndGetIssueWithRouting(ctx, primaryStore, "shared-hub")
	if err != nil {
		t.Fatalf("read of the copy while the planning store cannot open: %v", err)
	}
	defer read.Close()
	if read.Routed {
		t.Fatalf("read routed to the unreachable planning store (%v)", read.RedirectedCopyOwner)
	}
	if read.Issue.ID != "shared-hub" || read.Issue.Status != types.StatusOpen {
		t.Fatalf("read returned %+v; want the local copy's own row", read.Issue)
	}
}

// wireDeleteBatchGlobals points deleteBatchReporting's process-wide plumbing
// at s, standing in for what command dispatch would have set: the store
// handle the command path reads, its activation flag, dbPath (whose .beads
// directory prefix routing reads routes.jsonl from), the root context the
// resolvers run on, and the acting user. The prior values are restored on
// cleanup, unlike the command itself, which never runs twice in one process.
func wireDeleteBatchGlobals(t *testing.T, s storage.DoltStorage, db string) {
	t.Helper()
	storeMutex.Lock()
	oldStore, oldActive := store, storeActive
	store, storeActive = s, true
	storeMutex.Unlock()
	oldDBPath, oldRootCtx := dbPath, rootCtx
	dbPath, rootCtx = db, context.Background()
	actorMu.Lock()
	oldActor, oldPending := actor, actorGitFallbackPending
	actor, actorGitFallbackPending = "alice", false
	actorMu.Unlock()
	t.Cleanup(func() {
		storeMutex.Lock()
		store, storeActive = oldStore, oldActive
		storeMutex.Unlock()
		dbPath, rootCtx = oldDBPath, oldRootCtx
		actorMu.Lock()
		actor, actorGitFallbackPending = oldActor, oldPending
		actorMu.Unlock()
	})
}

// TestDeleteBatchRejectsMixedStores pins the batch half of the two-store
// safety GH#6058 adds: bd delete resolves each id on its own, so a batch can
// span ids that live in different stores — a hydrated copy whose delete
// belongs to the auto-route owner next to a project-only row, or next to an
// id a prefix route serves from another rig. The batch then commits through
// ONE store (the first routed handle, the project store when nothing
// routed), so such a mix would submit ids to a store that cannot see them.
// The batch must refuse before deleting anything and say to split it into one
// bd delete per store; a batch whose ids land in one store keeps deleting,
// however many handles the resolution opened along the way (AC-7).
func TestDeleteBatchRejectsMixedStores(t *testing.T) {
	t.Run("hydrated copy and project-only row refused, in either order", func(t *testing.T) {
		primaryStore, repoDir, planningDir := setupAutoRouteWithHydratedCopy(t, "maintainer", "routing.maintainer")
		wireDeleteBatchGlobals(t, primaryStore, filepath.Join(repoDir, ".beads", "beads.db"))
		ctx := context.Background()
		// A row only the project store holds, unlike the imported copy.
		seedIssue(t, ctx, primaryStore, "shared-only")

		for _, order := range [][]string{{"shared-hub", "shared-only"}, {"shared-only", "shared-hub"}} {
			err := deleteBatchReporting(order, true, false, false, false, nil, nil)
			if err == nil || !strings.Contains(err.Error(), "run a separate bd delete per store") {
				t.Fatalf("deleteBatch(%v) = %v, want the mixed-store refusal", order, err)
			}
			// The refusal names each destination and its ids, so the user can
			// split the batch without re-deriving what went where.
			for _, want := range []string{planningDir, "shared-hub", "shared-only"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("refusal %q does not name %s", err.Error(), want)
				}
			}
			// Refused BEFORE deleting: the planning owner's row, the project
			// copy and the project-only row all survive.
			if _, err := reopenAutoRouteTarget(t, planningDir).GetIssue(ctx, "shared-hub"); err != nil {
				t.Fatalf("planning row shared-hub after the refusal: %v", err)
			}
			for _, id := range []string{"shared-hub", "shared-only"} {
				if _, err := primaryStore.GetIssue(ctx, id); err != nil {
					t.Fatalf("project row %s after the refusal: %v", id, err)
				}
			}
		}
	})

	t.Run("two copies of one owner store delete together", func(t *testing.T) {
		primaryStore, repoDir, planningDir := setupAutoRouteWithHydratedCopy(t, "maintainer", "routing.maintainer", "shared-two")
		wireDeleteBatchGlobals(t, primaryStore, filepath.Join(repoDir, ".beads", "beads.db"))
		ctx := context.Background()

		// Each id's resolution opens the owner through its own handle; both
		// handles land in the same destination, so the batch proceeds.
		if err := deleteBatchReporting([]string{"shared-hub", "shared-two"}, true, false, false, false, nil, nil); err != nil {
			t.Fatalf("deleteBatch of two copies of one owner store: %v", err)
		}
		planning := reopenAutoRouteTarget(t, planningDir)
		for _, id := range []string{"shared-hub", "shared-two"} {
			if _, err := planning.GetIssue(ctx, id); err == nil {
				t.Fatalf("%s still stands in the planning owner's store after the redirected batch delete", id)
			}
			// The deletes were redirects, so the project's copies remain.
			if _, err := primaryStore.GetIssue(ctx, id); err != nil {
				t.Fatalf("project copy %s should remain after the redirected delete: %v", id, err)
			}
		}
	})

	t.Run("hydrated copy and prefix-routed rig refused", func(t *testing.T) {
		primaryStore, repoDir, planningDir := setupAutoRouteWithHydratedCopy(t, "maintainer", "routing.maintainer")
		ctx := context.Background()

		// A second routed destination, the rig-town way: a prefix route from
		// the project's .beads to a rig store. lookupPrefixRoute joins the
		// route's path onto the repo root, so the rig (a sibling of the repo)
		// is named relative to it, the way a real town's routes.jsonl does.
		rigDir := filepath.Join(filepath.Dir(repoDir), "rig")
		rigStore := newTestStoreIsolatedDB(t, filepath.Join(rigDir, ".beads", "beads.db"), "gt")
		seedIssue(t, ctx, rigStore, "gt-rig-1")
		if err := rigStore.Close(); err != nil {
			t.Fatalf("close the rig store: %v", err)
		}
		route := "{\"prefix\":\"gt-\",\"path\":\"../rig\"}\n"
		if err := os.WriteFile(filepath.Join(repoDir, ".beads", "routes.jsonl"), []byte(route), 0o600); err != nil {
			t.Fatalf("write routes.jsonl: %v", err)
		}
		wireDeleteBatchGlobals(t, primaryStore, filepath.Join(repoDir, ".beads", "beads.db"))

		err := deleteBatchReporting([]string{"shared-hub", "gt-rig-1"}, true, false, false, false, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "run a separate bd delete per store") {
			t.Fatalf("deleteBatch of a redirected copy and a prefix-routed id = %v, want the mixed-store refusal", err)
		}
		// Neither routed destination lost a row.
		if _, err := reopenAutoRouteTarget(t, planningDir).GetIssue(ctx, "shared-hub"); err != nil {
			t.Fatalf("planning row shared-hub after the refusal: %v", err)
		}
		if _, err := primaryStore.GetIssue(ctx, "shared-hub"); err != nil {
			t.Fatalf("project copy shared-hub after the refusal: %v", err)
		}
		if _, err := reopenAutoRouteTarget(t, rigDir).GetIssue(ctx, "gt-rig-1"); err != nil {
			t.Fatalf("rig row gt-rig-1 after the refusal: %v", err)
		}
	})
}

// TestVerboseLogsStoreSelection pins the store-selection diagnostics GH#6058
// needs: under --verbose (or BD_DEBUG=1, the same channel), resolving through
// a route logs, on stderr, which workspace was opened, the access intent,
// and the selection that reached for it — the split-brain workflow (bd list
// reads one store while bd close writes another) is otherwise undiagnosable
// from the CLI alone. The lines never touch stdout, so --json and every
// machine reader stay unaffected.
func TestVerboseLogsStoreSelection(t *testing.T) {
	t.Run("auto-route open, write intent", func(t *testing.T) {
		primaryStore, _, planningDir := setupAutoRoute(t, "maintainer", "routing.maintainer")
		ctx := context.Background()

		var result *RoutedResult
		stderr := captureStderr(t, func() {
			debug.SetVerbose(true)
			defer debug.SetVerbose(false)
			var err error
			result, err = resolveAndGetIssueForMutation(ctx, primaryStore, "shared-hub")
			if err != nil {
				t.Fatalf("resolveAndGetIssueForMutation: %v", err)
			}
		})
		defer result.Close()
		want := fmt.Sprintf("store: %s write (auto-route)\n", filepath.Join(planningDir, ".beads"))
		if !strings.Contains(stderr, want) {
			t.Fatalf("verbose stderr does not carry %q:\n%s", want, stderr)
		}
	})

	t.Run("copy owner redirect, write intent", func(t *testing.T) {
		primaryStore, _, planningDir := setupAutoRouteWithHydratedCopy(t, "maintainer", "routing.maintainer")
		ctx := context.Background()

		var result *RoutedResult
		stderr := captureStderr(t, func() {
			debug.SetVerbose(true)
			defer debug.SetVerbose(false)
			var err error
			result, err = resolveAndGetIssueForMutationExact(ctx, primaryStore, "shared-hub")
			if err != nil {
				t.Fatalf("resolveAndGetIssueForMutationExact: %v", err)
			}
		})
		defer result.Close()
		want := fmt.Sprintf("store: %s write (copy owner)\n", filepath.Join(planningDir, ".beads"))
		if !strings.Contains(stderr, want) {
			t.Fatalf("verbose stderr does not carry %q:\n%s", want, stderr)
		}
	})

	t.Run("copy owner redirect, read intent", func(t *testing.T) {
		primaryStore, _, planningDir := setupAutoRouteWithHydratedCopy(t, "maintainer", "routing.maintainer")
		ctx := context.Background()

		var result *RoutedResult
		stderr := captureStderr(t, func() {
			debug.SetVerbose(true)
			defer debug.SetVerbose(false)
			var err error
			result, err = resolveAndGetIssueWithRouting(ctx, primaryStore, "shared-hub")
			if err != nil {
				t.Fatalf("resolveAndGetIssueWithRouting: %v", err)
			}
		})
		defer result.Close()
		want := fmt.Sprintf("store: %s read (copy owner)\n", filepath.Join(planningDir, ".beads"))
		if !strings.Contains(stderr, want) {
			t.Fatalf("verbose stderr does not carry %q:\n%s", want, stderr)
		}
	})
}

// committedIssue returns id as the HEAD commit of beadsDir's database holds
// it, without what the working set holds uncommitted.
func committedIssue(t *testing.T, beadsDir, database, id string) *types.Issue {
	t.Helper()
	s, err := embeddeddolt.Open(t.Context(), beadsDir, database, "main")
	if err != nil {
		t.Fatalf("open %s: %v", beadsDir, err)
	}
	defer func() { _ = s.Close() }()
	issue, err := s.AsOf(t.Context(), id, "HEAD")
	if err != nil {
		t.Fatalf("read %s at HEAD: %v", id, err)
	}
	return issue
}
