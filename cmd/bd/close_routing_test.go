//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestResolveCloseTargets is the regression test for beads-0km: bd close
// failed for issues that lived only in the routed contributor planning store,
// while bd show / bd update succeeded for the same IDs because they routed via
// resolveAndGetIssueWithRouting and close did not. The fix uses
// resolveCloseTargets, which performs the same routing fallback and shares one
// routed-store handle across IDs to avoid the GH#3586 flock-deadlock pattern
// when multiple IDs route to the same target.
//
// The cases cover:
//   - maintainer-mode local resolution (no routing involved)
//   - contributor-mode batch where every ID lives in the planning store
//     (must share one routed-store handle to avoid the deadlock)
//   - contributor-mode mixed batch with one local ID and one routed ID
//     (validates the per-result store routing and the mutatedStores fan-out)
func TestResolveCloseTargets(t *testing.T) {
	cases := []struct {
		name            string
		role            string   // git config beads.role value
		enableRouting   bool     // set routing.mode=auto + routing.contributor=<planningDir>
		localSeed       []string // IDs to create in the primary (local) store
		planningSeed    []string // IDs to create in the planning store; non-nil also creates the dir
		inputIDs        []string // argument to resolveCloseTargets
		wantRoutedFlag  []bool   // expected RoutedResult.Routed per result
		wantStoreOrigin []string // per result: "local" (== primaryStore) or "planning" (routed handle)
	}{
		{
			name:            "maintainer_local_only",
			role:            "maintainer",
			enableRouting:   false,
			localSeed:       []string{"shared-local"},
			inputIDs:        []string{"shared-local"},
			wantRoutedFlag:  []bool{false},
			wantStoreOrigin: []string{"local"},
		},
		{
			name:            "contributor_batch_all_routed_shares_one_handle",
			role:            "contributor",
			enableRouting:   true,
			planningSeed:    []string{"shared-aaa", "shared-bbb"},
			inputIDs:        []string{"shared-aaa", "shared-bbb"},
			wantRoutedFlag:  []bool{true, true},
			wantStoreOrigin: []string{"planning", "planning"},
		},
		{
			name:            "contributor_mixed_local_and_routed",
			role:            "contributor",
			enableRouting:   true,
			localSeed:       []string{"shared-here"},
			planningSeed:    []string{"shared-there"},
			inputIDs:        []string{"shared-here", "shared-there"},
			wantRoutedFlag:  []bool{false, true},
			wantStoreOrigin: []string{"local", "planning"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			initConfigForTest(t)

			tmpDir := t.TempDir()
			repoDir := filepath.Join(tmpDir, "repo")
			planningDir := filepath.Join(tmpDir, "planning")

			runCmd(t, tmpDir, "git", "init", repoDir)
			runCmd(t, repoDir, "git", "config", "beads.role", tc.role)

			primaryStore := newTestStoreIsolatedDB(t, filepath.Join(repoDir, ".beads", "beads.db"), "shared")
			ctx := context.Background()

			if tc.enableRouting {
				if err := primaryStore.SetConfig(ctx, "routing.mode", "auto"); err != nil {
					t.Fatalf("set routing.mode: %v", err)
				}
				if err := primaryStore.SetConfig(ctx, "routing.contributor", planningDir); err != nil {
					t.Fatalf("set routing.contributor: %v", err)
				}
			}

			for _, id := range tc.localSeed {
				seedIssue(t, ctx, primaryStore, id)
			}

			if len(tc.planningSeed) > 0 {
				planningStore := newTestStoreIsolatedDB(t, filepath.Join(planningDir, ".beads", "beads.db"), "shared")
				for _, id := range tc.planningSeed {
					seedIssue(t, ctx, planningStore, id)
				}
				// Release the planning store's flock so resolveCloseTargets'
				// openRoutedReadStore call can acquire it.
				if err := planningStore.Close(); err != nil {
					t.Fatalf("close planning store: %v", err)
				}
			}

			oldWD, err := os.Getwd()
			if err != nil {
				t.Fatalf("getwd: %v", err)
			}
			t.Cleanup(func() { _ = os.Chdir(oldWD) })
			if err := os.Chdir(repoDir); err != nil {
				t.Fatalf("chdir repoDir: %v", err)
			}

			results, cleanup, err := resolveCloseTargets(ctx, primaryStore, tc.inputIDs)
			if err != nil {
				t.Fatalf("resolveCloseTargets: %v", err)
			}
			defer cleanup()

			if len(results) != len(tc.inputIDs) {
				t.Fatalf("got %d results, want %d", len(results), len(tc.inputIDs))
			}

			// Track the first routed-store handle we see so we can assert that
			// every subsequent routed result reuses it (the dedupe that prevents
			// the GH#3586 flock deadlock).
			var sharedRoutedStore storage.DoltStorage

			for i, r := range results {
				if r == nil || r.Issue == nil {
					t.Fatalf("result[%d] missing Issue", i)
				}
				if r.ResolvedID != tc.inputIDs[i] {
					t.Errorf("result[%d].ResolvedID = %q, want %q", i, r.ResolvedID, tc.inputIDs[i])
				}
				if r.Routed != tc.wantRoutedFlag[i] {
					t.Errorf("result[%d].Routed = %v, want %v", i, r.Routed, tc.wantRoutedFlag[i])
				}

				switch tc.wantStoreOrigin[i] {
				case "local":
					if r.Store != primaryStore {
						t.Errorf("result[%d].Store should be the local primary store", i)
					}
				case "planning":
					if r.Store == nil {
						t.Errorf("result[%d].Store is nil", i)
					}
					if r.Store == primaryStore {
						t.Errorf("result[%d].Store should be the routed planning store, not the local one", i)
					}
					if sharedRoutedStore == nil {
						sharedRoutedStore = r.Store
					} else if r.Store != sharedRoutedStore {
						t.Errorf("result[%d].Store: routed handles should be deduped to one shared handle (flock-deadlock guard)", i)
					}
				default:
					t.Fatalf("unknown wantStoreOrigin[%d] = %q", i, tc.wantStoreOrigin[i])
				}
			}
		})
	}
}

// seedIssue creates a minimal open task in the given store for test fixtures.
func seedIssue(t *testing.T, ctx context.Context, s storage.DoltStorage, id string) {
	t.Helper()
	issue := &types.Issue{
		ID:        id,
		Title:     "test issue " + id,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := s.CreateIssue(ctx, issue, "test"); err != nil {
		t.Fatalf("seed issue %s: %v", id, err)
	}
}
