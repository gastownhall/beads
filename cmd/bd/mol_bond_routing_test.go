//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestResolveBondStore is the regression test for GH#4220: `bd mol bond` resolved
// its operands only against the active store, so when the target issue lived in a
// different (routed) database — e.g. invoked from the town root where the active store
// is the town DB but the target lives in a rig DB — the bond failed with
// "'<id>' not found", even though `bd show`/`bd update`/`bd close` routed the same ID
// fine. resolveBondStore performs the same routing fallback and returns the database
// the bond must operate on, so spawned wisps land in the target's database.
//
// Cases:
//   - local target (no routing) → returns the local primary store
//   - routed target → returns the routed store, not the local one
//   - formula operand + routed target → formula is skipped, routed target wins
//   - nothing resolvable (formula only) → falls back to the local store
func TestResolveBondStore(t *testing.T) {
	cases := []struct {
		name          string
		role          string
		enableRouting bool
		localSeed     []string
		planningSeed  []string
		operands      []string
		wantRouted    bool // true: expect a store != local; false: expect the local store
	}{
		{
			name:       "local_target_uses_local_store",
			role:       "maintainer",
			localSeed:  []string{"shared-here"},
			operands:   []string{"mol-some-formula", "shared-here"},
			wantRouted: false,
		},
		{
			name:          "routed_target_uses_routed_store",
			role:          "contributor",
			enableRouting: true,
			planningSeed:  []string{"shared-there"},
			operands:      []string{"mol-some-formula", "shared-there"},
			wantRouted:    true,
		},
		{
			name:          "formula_operand_skipped_routed_target_wins",
			role:          "contributor",
			enableRouting: true,
			planningSeed:  []string{"shared-target"},
			operands:      []string{"shared-target", "mol-polecat-work"},
			wantRouted:    true,
		},
		{
			name:       "no_resolvable_operand_falls_back_to_local",
			role:       "maintainer",
			operands:   []string{"mol-a", "mol-b"},
			wantRouted: false,
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

			workStore, closeWorkStore := resolveBondStore(ctx, primaryStore, tc.operands)
			defer closeWorkStore()

			if workStore == nil {
				t.Fatal("resolveBondStore returned a nil store")
			}
			if tc.wantRouted {
				if workStore == primaryStore {
					t.Errorf("expected a routed store, got the local primary store")
				}
			} else {
				if workStore != primaryStore {
					t.Errorf("expected the local primary store, got a routed store")
				}
			}
		})
	}
}
