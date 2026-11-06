//go:build cgo

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// TestResolveAndGetIssueForMutationExactSurfacesRoutedAbbreviationRefusal pins
// the error SELECTION in resolveAndGetIssueForMutationExact, the one part of
// the exact-match contract the rest of this PR's tests cannot reach: they all
// exercise the LOCAL tier, where the refusal is the function's own return
// value. A cross-rig or auto-routed tier mints the same sentinel, and the
// truthful message the comment write paths print (comment.go, comments.go)
// depends on errors.Is seeing it from there too.
//
// The setup is the shape the sentinel exists for: the local store genuinely
// knows nothing about the id, so its error is the plain "no issue found
// matching" — which is FALSE here, because prefix routing finds the
// abbreviation's real target in the other rig and refuses it there.
//
// Both directions are pinned, because either assertion alone passes for the
// wrong reason: an abbreviation of a routed issue must select the ROUTED
// refusal over the local not-found, and an id that names nothing anywhere
// must still come back as a plain not-found rather than claiming an
// abbreviation exists.
//
// NOTE: assigns the dbPath global and uses os.Chdir, so it cannot run in
// parallel (same constraint as TestCheckBeadGateCrossRigPrefixRoute).
func TestResolveAndGetIssueForMutationExactSurfacesRoutedAbbreviationRefusal(t *testing.T) {
	ctx := context.Background()
	townRoot := t.TempDir()
	townBeadsDir := filepath.Join(townRoot, ".beads")
	rigBeadsDir := filepath.Join(townRoot, "rig", ".beads")
	if err := os.MkdirAll(townBeadsDir, 0o755); err != nil {
		t.Fatalf("create town beads dir: %v", err)
	}
	if err := os.MkdirAll(rigBeadsDir, 0o755); err != nil {
		t.Fatalf("create rig beads dir: %v", err)
	}

	// The town store is the local store the comment write paths hold; it never
	// holds the routed id, so it supplies the plain not-found this test is
	// about discarding.
	townDBPath := filepath.Join(townBeadsDir, "dolt")
	townStore := newTestStoreIsolatedDB(t, townDBPath, "hq")

	const routedID = "gt-abcdefgh"
	// One character short of routedID: not an exact id anywhere, but a valid
	// leading-prefix abbreviation of a real issue in the routed rig.
	const routedAbbrev = "gt-abcdefg"
	// Routes to the same rig, but names nothing there in either form.
	const absentRoutedID = "gt-zzzzzzzz"

	rigStore := newTestStoreIsolatedDB(t, filepath.Join(rigBeadsDir, "dolt"), "gt")
	if err := rigStore.CreateIssue(ctx, &types.Issue{
		ID:        routedID,
		Title:     "Routed comment target",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}, "test"); err != nil {
		t.Fatalf("create routed issue %s: %v", routedID, err)
	}
	// Prefix routing opens the target itself, so hand the database back first.
	if err := rigStore.Close(); err != nil {
		t.Fatalf("close rig store: %v", err)
	}

	routesPath := filepath.Join(townBeadsDir, "routes.jsonl")
	if err := os.WriteFile(routesPath, []byte(`{"prefix":"gt-","path":"rig"}`), 0o644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	oldDBPath := dbPath
	dbPath = townDBPath
	t.Cleanup(func() { dbPath = oldDBPath })
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("change to town root: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	// Direction 1: the routed tier's refusal must reach the caller, because
	// the issue it refuses demonstrably exists.
	result, err := resolveAndGetIssueForMutationExact(ctx, townStore, routedAbbrev)
	if result != nil {
		result.Close()
		t.Fatalf("abbreviation %q of routed issue %s resolved instead of being refused", routedAbbrev, routedID)
	}
	if err == nil {
		t.Fatalf("abbreviation %q of routed issue %s returned no error", routedAbbrev, routedID)
	}
	if !errors.Is(err, utils.ErrAbbreviatedIDNotAllowed) {
		t.Errorf("routed abbreviation %q error = %v, want one satisfying errors.Is(err, utils.ErrAbbreviatedIDNotAllowed)\n"+
			"the comment write paths key their truthful message off this sentinel, so a local not-found here prints "+
			"\"no issue found matching\" for an issue the routed rig holds", routedAbbrev, err)
	}

	// Direction 2: an id that names nothing in either store must NOT be
	// reported as an abbreviation refusal — the selection has to be driven by
	// the routed error's own sentinel, not by having routed at all.
	result, err = resolveAndGetIssueForMutationExact(ctx, townStore, absentRoutedID)
	if result != nil {
		result.Close()
		t.Fatalf("absent routed id %q resolved unexpectedly", absentRoutedID)
	}
	if err == nil {
		t.Fatalf("absent routed id %q returned no error", absentRoutedID)
	}
	if errors.Is(err, utils.ErrAbbreviatedIDNotAllowed) {
		t.Errorf("absent routed id %q error = %v, want NOT an abbreviation refusal", absentRoutedID, err)
	}
	if !isNotFoundErr(err) {
		t.Errorf("absent routed id %q error = %v, want a not-found error", absentRoutedID, err)
	}
}

// TestRoutedResolutionSurfacesRoutedAmbiguity pins the same error selection
// for utils.ErrAmbiguousID, in both routed wrappers. A contributor repo
// auto-routes to a planning store holding two issues that share a hash, so the
// routed tier refuses that bare hash as ambiguous while the local store's
// error is the plain not-found, which would deny that either issue exists. A
// bare hash has no prefix to route on, so the auto-routed tier is the one that
// answers.
//
// As above, the control matters as much as the refusal: a hash neither store
// holds must still come back as a plain not-found, not as an ambiguity.
//
// NOTE: uses os.Chdir, so it cannot run in parallel.
func TestRoutedResolutionSurfacesRoutedAmbiguity(t *testing.T) {
	initConfigForTest(t)
	// Viper binds BEADS_ROUTING_MODE / BD_ROUTING_MODE onto routing.mode, which
	// outranks the routing.mode=auto written into the store below, so a host
	// that exports either var would never reach the auto-routed tier (see
	// TestDetermineAutoRoutedRepoPath_UsesSelectedBeadsDirRole).
	t.Setenv("BEADS_ROUTING_MODE", "")
	t.Setenv("BD_ROUTING_MODE", "")
	ctx := context.Background()

	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	planningDir := filepath.Join(tmpDir, "planning")

	runCmd(t, tmpDir, "git", "init", repoDir)
	runCmd(t, repoDir, "git", "config", "beads.role", "contributor")

	sourceStore := newTestStoreIsolatedDB(t, filepath.Join(repoDir, ".beads", "beads.db"), "src")
	if err := sourceStore.SetConfig(ctx, "routing.mode", "auto"); err != nil {
		t.Fatalf("set routing.mode: %v", err)
	}
	if err := sourceStore.SetConfig(ctx, "routing.contributor", planningDir); err != nil {
		t.Fatalf("set routing.contributor: %v", err)
	}

	const sharedHash = "abc123"
	// Names nothing in either store.
	const absentHash = "zzz999"

	planningStore := newTestStoreIsolatedDB(t, filepath.Join(planningDir, ".beads", "beads.db"), "plan")
	for _, id := range []string{"hacker-news-" + sharedHash, "ops-" + sharedHash} {
		if err := planningStore.CreateIssue(ctx, &types.Issue{
			ID:        id,
			Title:     "Issue sharing a hash",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}, "test"); err != nil {
			t.Fatalf("create planning issue %s: %v", id, err)
		}
	}
	if err := planningStore.SetConfig(ctx, "allowed_prefixes", "hacker-news,ops"); err != nil {
		t.Fatalf("set allowed_prefixes: %v", err)
	}
	// Auto-routing opens the planning store itself, so hand the database back first.
	if err := planningStore.Close(); err != nil {
		t.Fatalf("close planning store: %v", err)
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(repoDir); err != nil {
		t.Fatalf("change to repo dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	// Role detection reads the process-wide RepoContext cache; rebuild it from
	// repoDir (see TestOpenRoutedReadStore_ContributorRouting).
	resetRepoContextCachesForTest(t)

	wrappers := []struct {
		name    string
		resolve func(context.Context, storage.DoltStorage, string) (*RoutedResult, error)
	}{
		{"resolveAndGetIssueWithRouting", resolveAndGetIssueWithRouting},
		{"resolveAndGetIssueForMutationExact", resolveAndGetIssueForMutationExact},
	}
	for _, w := range wrappers {
		result, err := w.resolve(ctx, sourceStore, sharedHash)
		if result != nil {
			result.Close()
			t.Fatalf("%s(%q) resolved %s, want an ambiguity refusal", w.name, sharedHash, result.ResolvedID)
		}
		if !errors.Is(err, utils.ErrAmbiguousID) {
			t.Errorf("%s(%q) error = %v, want one satisfying errors.Is(err, utils.ErrAmbiguousID)\n"+
				"the routed store holds two issues with this hash, so a local not-found here denies both exist",
				w.name, sharedHash, err)
		}

		result, err = w.resolve(ctx, sourceStore, absentHash)
		if result != nil {
			result.Close()
			t.Fatalf("%s(%q) resolved %s unexpectedly", w.name, absentHash, result.ResolvedID)
		}
		if errors.Is(err, utils.ErrAmbiguousID) || !isNotFoundErr(err) {
			t.Errorf("%s(%q) error = %v, want a plain not-found", w.name, absentHash, err)
		}
	}

	// bd close resolves its batch through resolveCloseTargets, which repeats
	// the same routing precedence with a shared routed handle.
	results, cleanup, err := resolveCloseTargets(ctx, sourceStore, []string{sharedHash})
	cleanup()
	if len(results) != 0 || !errors.Is(err, utils.ErrAmbiguousID) {
		t.Errorf("resolveCloseTargets(%q) = (%d results, %v), want an error satisfying errors.Is(err, utils.ErrAmbiguousID)",
			sharedHash, len(results), err)
	}
	results, cleanup, err = resolveCloseTargets(ctx, sourceStore, []string{absentHash})
	cleanup()
	if len(results) != 0 || errors.Is(err, utils.ErrAmbiguousID) || !isNotFoundErr(err) {
		t.Errorf("resolveCloseTargets(%q) = (%d results, %v), want a plain not-found", absentHash, len(results), err)
	}
}

// TestPrefixRoutedResolutionSurfacesRoutedAmbiguity pins the same selection
// through the prefix-routed tier (routes.jsonl), which a bare hash never
// reaches, on the shape that most often produces it: a parent deleted while
// both of its children survive. The parent's id abbreviates both children, so
// the rig's resolver refuses it as ambiguous, and the caller must hear that
// rather than the local store's not-found.
//
// It also pins what --if-revision makes of that refusal. bd update, close,
// assign and reopen resolve a guarded write's target through
// resolveAndGetIssueForMutation, and the refusal means the id they were given
// names no row, so it reports the guard's lost race (exit 13) as any other
// gone id does, whether the parent was deleted from the routed rig or from the
// local store.
//
// As above, the control matters as much as the refusal: an id that names
// nothing in the routed rig must still come back as a plain not-found.
//
// NOTE: assigns the dbPath global and uses os.Chdir, so it cannot run in
// parallel.
func TestPrefixRoutedResolutionSurfacesRoutedAmbiguity(t *testing.T) {
	ctx := context.Background()
	townRoot := t.TempDir()
	townBeadsDir := filepath.Join(townRoot, ".beads")
	rigBeadsDir := filepath.Join(townRoot, "rig", ".beads")
	for _, dir := range []string{townBeadsDir, rigBeadsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}

	// Both parents are gone and both children of each survive: localParent's
	// in the town store, which is the local store, and routedParent's in the
	// rig that routes.jsonl sends gt- to.
	const localParent = "hq-loc1"
	const routedParent = "gt-par1"
	// Routes to the same rig, but names nothing there.
	const absentRoutedID = "gt-zzzzzzzz"
	createChildren := func(s storage.DoltStorage, parent string) {
		t.Helper()
		for _, id := range []string{parent + ".1", parent + ".2"} {
			if err := s.CreateIssue(ctx, &types.Issue{
				ID:        id,
				Title:     "Child that outlived its parent",
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			}, "test"); err != nil {
				t.Fatalf("create %s: %v", id, err)
			}
		}
	}

	townDBPath := filepath.Join(townBeadsDir, "dolt")
	townStore := newTestStoreIsolatedDB(t, townDBPath, "hq")
	createChildren(townStore, localParent)

	rigStore := newTestStoreIsolatedDB(t, filepath.Join(rigBeadsDir, "dolt"), "gt")
	createChildren(rigStore, routedParent)
	// Prefix routing opens the target itself, so hand the database back first.
	if err := rigStore.Close(); err != nil {
		t.Fatalf("close rig store: %v", err)
	}

	if err := os.WriteFile(filepath.Join(townBeadsDir, "routes.jsonl"), []byte(`{"prefix":"gt-","path":"rig"}`), 0o644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	oldDBPath := dbPath
	dbPath = townDBPath
	t.Cleanup(func() { dbPath = oldDBPath })
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("change to town root: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	ifRevision := int64(3)
	wrappers := []struct {
		name    string
		resolve func(context.Context, storage.DoltStorage, string) (*RoutedResult, error)
		ids     []string
		guarded bool
	}{
		{"resolveAndGetIssueWithRouting", resolveAndGetIssueWithRouting, []string{routedParent}, false},
		{"resolveAndGetIssueForMutation", resolveAndGetIssueForMutation, []string{routedParent, localParent}, true},
	}
	for _, w := range wrappers {
		for _, id := range w.ids {
			result, err := w.resolve(ctx, townStore, id)
			if result != nil {
				result.Close()
				t.Fatalf("%s(%q) resolved %s, want an ambiguity refusal", w.name, id, result.ResolvedID)
			}
			if !errors.Is(err, utils.ErrAmbiguousID) {
				t.Errorf("%s(%q) error = %v, want one satisfying errors.Is(err, utils.ErrAmbiguousID)\n"+
					"the deleted parent's id abbreviates both of its surviving children", w.name, id, err)
			}
			if !w.guarded {
				continue
			}
			reported, ok := reportIfRevisionTargetGone("updating", id, err, &ifRevision)
			if ee, isExitErr := reported.(*exitError); !ok || !isExitErr || ee.Code != ExitGuardMismatch {
				t.Errorf("reportIfRevisionTargetGone(%q, %v) = (%#v, %v), want (*exitError{Code: %d}, true): "+
					"a guarded write to a deleted parent lost its race, as for any other gone id",
					id, err, reported, ok, ExitGuardMismatch)
			}
		}

		result, err := w.resolve(ctx, townStore, absentRoutedID)
		if result != nil {
			result.Close()
			t.Fatalf("%s(%q) resolved %s unexpectedly", w.name, absentRoutedID, result.ResolvedID)
		}
		if errors.Is(err, utils.ErrAmbiguousID) || !isNotFoundErr(err) {
			t.Errorf("%s(%q) error = %v, want a plain not-found", w.name, absentRoutedID, err)
		}
	}

	// resolveCloseTargets repeats the precedence for bd close's batch path,
	// with its own prefix-routed tier.
	results, cleanup, err := resolveCloseTargets(ctx, townStore, []string{routedParent})
	cleanup()
	if len(results) != 0 || !errors.Is(err, utils.ErrAmbiguousID) {
		t.Errorf("resolveCloseTargets(%q) = (%d results, %v), want an error satisfying errors.Is(err, utils.ErrAmbiguousID)",
			routedParent, len(results), err)
	}
	results, cleanup, err = resolveCloseTargets(ctx, townStore, []string{absentRoutedID})
	cleanup()
	if len(results) != 0 || errors.Is(err, utils.ErrAmbiguousID) || !isNotFoundErr(err) {
		t.Errorf("resolveCloseTargets(%q) = (%d results, %v), want a plain not-found", absentRoutedID, len(results), err)
	}
}
