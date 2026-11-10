//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// foreignPrefixHintMarker is a phrase only the foreign-prefix miss hint
// (foreignPrefixMissHint) prints; existingHintMarker one only
// showNotFoundHint prints.
const (
	foreignPrefixHintMarker = "does not mean the issue is absent"
	existingHintMarker      = "never existed"
)

// createShowRoutingIssue creates id in s, failing the test on error.
func createShowRoutingIssue(t *testing.T, s storage.DoltStorage, id string) {
	t.Helper()
	if err := s.CreateIssue(context.Background(), &types.Issue{
		ID:        id,
		Title:     "Routed show fixture " + id,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}, "test"); err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
}

// useShowSeat makes bd show run as if launched from seatDir with local as
// its store and localDBPath as its database, restoring every global and the
// working directory afterwards.
//
// NOTE: assigns package globals and uses os.Chdir, so callers cannot run in
// parallel.
func useShowSeat(t *testing.T, seatDir, localDBPath string, local storage.DoltStorage) {
	t.Helper()
	ensureCleanGlobalState(t)
	saveAndRestoreGlobals(t)
	oldCtx, oldJSON, oldProxied := rootCtx, jsonOutput, proxiedServerMode
	t.Cleanup(func() { rootCtx, jsonOutput, proxiedServerMode = oldCtx, oldJSON, oldProxied })
	store, rootCtx, jsonOutput, proxiedServerMode = local, context.Background(), false, false
	dbPath = localDBPath

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(seatDir); err != nil {
		t.Fatalf("change to %s: %v", seatDir, err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
}

// runShowDirect runs bd show's direct route for id and returns its stderr and
// error.
func runShowDirect(t *testing.T, id string) (string, error) {
	t.Helper()
	var err error
	stderr := captureStderr(t, func() { err = showCmd.RunE(showCmd, []string{id}) })
	return stderr, err
}

// rigSeatTown builds the GH#5259 layout: a town store issuing "hq" at
// <town>/.beads and a rig store issuing "op" at <town>/openclaw/.beads, the
// rig's .beads holding rigRoutes as its routes.jsonl (none when empty). The
// town store holds townIDs and is closed, so a route reopens it by path as bd
// would. It returns the rig's directory, its database path and its store.
func rigSeatTown(t *testing.T, rigRoutes string, townIDs ...string) (string, string, storage.DoltStorage) {
	t.Helper()
	townRoot := t.TempDir()
	townBeadsDir := filepath.Join(townRoot, ".beads")
	rigDir := filepath.Join(townRoot, "openclaw")
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	for _, dir := range []string{townBeadsDir, rigBeadsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}

	townStore := newTestStoreIsolatedDB(t, filepath.Join(townBeadsDir, "dolt"), "hq")
	for _, id := range townIDs {
		createShowRoutingIssue(t, townStore, id)
	}
	// Prefix routing opens the target itself, so hand the database back first.
	if err := townStore.Close(); err != nil {
		t.Fatalf("close town store: %v", err)
	}

	rigDBPath := filepath.Join(rigBeadsDir, "dolt")
	rigStore := newTestStoreIsolatedDB(t, rigDBPath, "op")
	if rigRoutes != "" {
		if err := os.WriteFile(filepath.Join(rigBeadsDir, "routes.jsonl"), []byte(rigRoutes+"\n"), 0o644); err != nil {
			t.Fatalf("write routes.jsonl: %v", err)
		}
	}
	return rigDir, rigDBPath, rigStore
}

// TestShowPrefixRoute_RoutedMissKeepsHint pins AC-2 for the prefix tier
// (GH#5259): when the rig's routes.jsonl sends hq- to the town store and the
// town store misses too, a routed store was searched, so bd show keeps the
// existing hint rather than saying no other store was asked.
func TestShowPrefixRoute_RoutedMissKeepsHint(t *testing.T) {
	rigDir, rigDBPath, rigStore := rigSeatTown(t, `{"prefix":"hq-","path":".."}`)
	useShowSeat(t, rigDir, rigDBPath, rigStore)

	const absent = "hq-zzzz9"
	stderr, err := runShowDirect(t, absent)
	if err == nil {
		t.Errorf("bd show %s: want the not-found exit, got nil", absent)
	}
	if !strings.Contains(stderr, "Issue "+absent+" not found") || !strings.Contains(stderr, existingHintMarker) || strings.Contains(stderr, foreignPrefixHintMarker) {
		t.Errorf("bd show %s stderr = %q, want the not-found line and the existing hint only", absent, stderr)
	}

	result, searched, err := resolveAndGetIssueWithRoutingSearchScope(rootCtx, rigStore, absent)
	if result != nil {
		result.Close()
		t.Fatalf("resolved %s unexpectedly", absent)
	}
	if !isNotFoundErr(err) || !searched {
		t.Errorf("scope for %s = (searched %v, %v), want a not-found the prefix-routed store answered", absent, searched, err)
	}
}

// TestShowPrefixRoute_RefusalAndFailureAreNotHints pins AC-2's other edge
// (GH#5259): a routed tier's refusal (an ambiguous ID in the routed store)
// and a routing failure (a route whose target cannot be opened) still answer
// with "Error fetching", as before; neither turns into a miss hint.
func TestShowPrefixRoute_RefusalAndFailureAreNotHints(t *testing.T) {
	rigRoutes := `{"prefix":"hq-","path":".."}` + "\n" + `{"prefix":"gt-","path":"ghost"}`
	rigDir, rigDBPath, rigStore := rigSeatTown(t, rigRoutes, "hq-zza1", "hq-zza2")
	useShowSeat(t, rigDir, rigDBPath, rigStore)

	for _, tc := range []struct {
		id   string
		want string
	}{
		{"hq-zza", "ambiguous issue ID"}, // routed refusal: two issues share the abbreviation
		{"gt-gea4h", "prefix route"},     // routing failure: the route's target cannot be opened
	} {
		stderr, err := runShowDirect(t, tc.id)
		if err == nil {
			t.Errorf("bd show %s: want an exit error, got nil", tc.id)
		}
		if !strings.Contains(stderr, "Error fetching "+tc.id) || !strings.Contains(stderr, tc.want) {
			t.Errorf("bd show %s: stderr = %q\nwant 'Error fetching' with %q and no miss hint", tc.id, stderr, tc.want)
		}
		if strings.Contains(stderr, "Hint:") {
			t.Errorf("bd show %s: a routed refusal/failure turned into a miss hint;\nstderr: %s", tc.id, stderr)
		}
	}
}

// TestShowAutoRoute_OwnerSearchedKeepsHint pins AC-2 for the auto tier
// (GH#5259): a contributor workspace whose auto-route target was searched
// for the id has not missed in its own store alone, so bd show keeps the
// existing hint, whether or not the target issues the requested prefix (any
// routed search counts). Prefix-route misses and refusals before the auto
// tier keep their existing outcomes, and none turns into the new hint.
func TestShowAutoRoute_OwnerSearchedKeepsHint(t *testing.T) {
	for _, tc := range []struct {
		name           string
		planningPrefix string
	}{
		{name: "auto target issues the requested prefix", planningPrefix: "hq"},
		{name: "auto target issues an unrelated prefix", planningPrefix: "plan"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initConfigForTest(t)
			// Viper binds BEADS_ROUTING_MODE / BD_ROUTING_MODE onto routing.mode,
			// which outranks the routing.mode=auto written into the store below.
			t.Setenv("BEADS_ROUTING_MODE", "")
			t.Setenv("BD_ROUTING_MODE", "")
			ctx := context.Background()

			tmpDir := t.TempDir()
			repoDir := filepath.Join(tmpDir, "repo")
			planningDir := filepath.Join(tmpDir, "planning")
			runCmd(t, tmpDir, "git", "init", repoDir)
			runCmd(t, repoDir, "git", "config", "beads.role", "contributor")

			sourceDBPath := filepath.Join(repoDir, ".beads", "dolt")
			sourceStore := newTestStoreIsolatedDB(t, sourceDBPath, "src")
			if err := sourceStore.SetConfig(ctx, "routing.mode", "auto"); err != nil {
				t.Fatalf("set routing.mode: %v", err)
			}
			if err := sourceStore.SetConfig(ctx, "routing.contributor", planningDir); err != nil {
				t.Fatalf("set routing.contributor: %v", err)
			}

			// rt- routes to a rig where rt-par1 and rt-par2 each abbreviate two
			// children, so the prefix tier refuses both as ambiguous.
			if err := os.WriteFile(filepath.Join(repoDir, ".beads", "routes.jsonl"), []byte(`{"prefix":"rt-","path":"rig"}`+"\n"), 0o644); err != nil {
				t.Fatalf("write routes.jsonl: %v", err)
			}
			rigStore := newTestStoreIsolatedDB(t, filepath.Join(repoDir, "rig", ".beads", "dolt"), "rt")
			for _, id := range []string{"rt-par1.1", "rt-par1.2", "rt-par2.1", "rt-par2.2"} {
				createShowRoutingIssue(t, rigStore, id)
			}
			if err := rigStore.Close(); err != nil {
				t.Fatalf("close rig store: %v", err)
			}

			planningStore := newTestStoreIsolatedDB(t, filepath.Join(planningDir, ".beads", "dolt"), tc.planningPrefix)
			if err := planningStore.SetConfig(ctx, "allowed_prefixes", "rt"); err != nil {
				t.Fatalf("set allowed_prefixes: %v", err)
			}
			for _, id := range []string{"rt-xyz9", "rt-par1"} {
				createShowRoutingIssue(t, planningStore, id)
			}
			// Auto-routing opens the planning store itself, so hand the database back first.
			if err := planningStore.Close(); err != nil {
				t.Fatalf("close planning store: %v", err)
			}

			useShowSeat(t, repoDir, sourceDBPath, sourceStore)
			// Role detection reads the process-wide RepoContext cache; rebuild it from repoDir.
			resetRepoContextCachesForTest(t)

			const absent = "hq-zzzz9"
			for _, c := range []struct {
				id    string
				found bool
				want  string // stderr must contain this when the id is not found
			}{
				// No prefix route; the auto target missed.
				{id: absent, want: existingHintMarker},
				// The prefix route missed, then the auto target missed.
				{id: "rt-zzzz9", want: existingHintMarker},
				// The prefix route missed, then the auto target found it.
				{id: "rt-xyz9", found: true},
				// The prefix route refused it, then the auto target found it.
				{id: "rt-par1", found: true},
				// The prefix route refused it and the auto target missed: the
				// refusal answers, with no hint at all.
				{id: "rt-par2", want: "Error fetching rt-par2"},
			} {
				stderr, err := runShowDirect(t, c.id)
				if c.found {
					if err != nil || strings.Contains(stderr, c.id) {
						t.Errorf("bd show %s = (%v, stderr %q), want it found", c.id, err, stderr)
					}
					continue
				}
				if err == nil {
					t.Errorf("bd show %s: want the not-found exit, got nil", c.id)
				}
				if !strings.Contains(stderr, c.want) || strings.Contains(stderr, foreignPrefixHintMarker) {
					t.Errorf("bd show %s stderr = %q, want %q and not the foreign-prefix hint", c.id, stderr, c.want)
				}
			}

			result, searched, err := resolveAndGetIssueWithRoutingSearchScope(ctx, sourceStore, absent)
			if result != nil {
				result.Close()
				t.Fatalf("resolved %s unexpectedly", absent)
			}
			if !isNotFoundErr(err) || !searched {
				t.Errorf("scope for %s = (searched %v, %v), want a not-found the auto-routed store answered", absent, searched, err)
			}
		})
	}
}
