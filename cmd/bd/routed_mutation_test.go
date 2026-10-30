//go:build cgo

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
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
