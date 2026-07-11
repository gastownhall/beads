//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// TestResolveMolBondOperandRouting is the regression test for the mol-bond
// routing bug: `bd mol bond <formula> <bead>` failed with
// "'<bead>' not found (not an issue ID or formula name)" whenever <bead> lived
// in a routed store (a prefix-routed rig, or the contributor planning store),
// even though `bd show <bead>` resolved it. mol bond resolved operands with
// utils.ResolvePartialID against the local store only and never fell through to
// the routing fallback that show/update/close use.
//
// resolveMolBondOperand now routes operand resolution through
// resolveAndGetIssueForMutation, so an operand that exists only in a routed
// store resolves and reports the routed store as its owner. The bond mutation
// then runs against that store instead of the local one.
//
// The cases mirror TestResolveCloseTargets:
//   - maintainer-mode local resolution (no routing involved)
//   - contributor-mode operand that lives only in the routed planning store
func TestResolveMolBondOperandRouting(t *testing.T) {
	cases := []struct {
		name          string
		role          string // git config beads.role value
		enableRouting bool   // set routing.mode=auto + routing.contributor=<planningDir>
		localSeed     string // issue ID to create in the primary (local) store, if non-empty
		planningSeed  string // issue ID to create in the routed planning store, if non-empty
		operand       string // argument to resolveMolBondOperand
		wantRouted    bool   // expected molBondOperand.routed
		wantStore     string // "local" (== primaryStore) or "planning" (routed handle)
	}{
		{
			name:       "maintainer_local_issue",
			role:       "maintainer",
			localSeed:  "shared-local",
			operand:    "shared-local",
			wantRouted: false,
			wantStore:  "local",
		},
		{
			name:          "contributor_routed_issue",
			role:          "contributor",
			enableRouting: true,
			planningSeed:  "shared-routed",
			operand:       "shared-routed",
			wantRouted:    true,
			wantStore:     "planning",
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

			if tc.localSeed != "" {
				seedIssue(t, ctx, primaryStore, tc.localSeed)
			}
			if tc.planningSeed != "" {
				planningStore := newTestStoreIsolatedDB(t, filepath.Join(planningDir, ".beads", "beads.db"), "shared")
				seedIssue(t, ctx, planningStore, tc.planningSeed)
				// Release the planning store so resolveMolBondOperand can open the
				// routed store through the normal command path.
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

			op, err := resolveMolBondOperand(ctx, primaryStore, tc.operand, nil)
			if err != nil {
				t.Fatalf("resolveMolBondOperand(%q): %v", tc.operand, err)
			}
			defer op.Close()

			if op.subgraph == nil || op.subgraph.Root == nil {
				t.Fatalf("operand %q: missing resolved subgraph", tc.operand)
			}
			if op.subgraph.Root.ID != tc.operand {
				t.Errorf("operand %q: Root.ID = %q, want %q", tc.operand, op.subgraph.Root.ID, tc.operand)
			}
			if op.cooked {
				t.Errorf("operand %q: cooked = true, want false (it is an existing issue)", tc.operand)
			}
			if op.routed != tc.wantRouted {
				t.Errorf("operand %q: routed = %v, want %v", tc.operand, op.routed, tc.wantRouted)
			}

			assertStoreOrigin(t, tc.operand, tc.wantStore, op.store, primaryStore)
		})
	}
}

func assertStoreOrigin(t *testing.T, operand, want string, got, local storage.DoltStorage) {
	t.Helper()
	switch want {
	case "local":
		if got != local {
			t.Errorf("operand %q: store should be the local primary store", operand)
		}
	case "planning":
		if got == nil {
			t.Errorf("operand %q: store is nil", operand)
		}
		if got == local {
			t.Errorf("operand %q: store should be the routed planning store, not the local one", operand)
		}
	default:
		t.Fatalf("operand %q: unknown wantStore %q", operand, want)
	}
}
