//go:build cgo

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestResolveMolBondOperandRouting is the regression test for the mol-bond
// routing bug: `bd mol bond <formula> <bead>` failed with
// "'<bead>' not found (not an issue ID or formula name)" whenever <bead> lived
// in a routed store (a prefix-routed rig, or the contributor planning store),
// even though `bd show <bead>` resolved it. mol bond resolved operands with
// utils.ResolvePartialID against the local store only and never fell through to
// the routing fallback that show/update/close use.
//
// discoverMolBondOperand resolves both operands read-only and records their
// logical store homes before the accepted target is reopened writable.
//
// The cases mirror TestResolveCloseTargets:
//   - maintainer-mode local resolution (no routing involved)
//   - contributor-mode operand that lives only in the routed planning store
func TestResolveMolBondOperandRouting(t *testing.T) {
	cases := []struct {
		name          string
		role          string
		enableRouting bool
		localSeed     string
		planningSeed  string
		operand       string
		wantForbidden bool
		wantStore     string
	}{
		{
			name:      "maintainer_local_issue",
			role:      "maintainer",
			localSeed: "shared-local",
			operand:   "shared-local",
			wantStore: "local",
		},
		{
			name:          "contributor_routed_issue",
			role:          "contributor",
			enableRouting: true,
			planningSeed:  "shared-routed",
			operand:       "shared-routed",
			wantForbidden: true,
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
				// Release the planning store so discovery can open the
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

			disc, err := discoverMolBondOperand(ctx, primaryStore, tc.operand)
			if err != nil {
				t.Fatalf("discoverMolBondOperand(%q): %v", tc.operand, err)
			}
			defer disc.Close()
			if disc.issue == nil || disc.issue.ID != tc.operand || disc.formula != "" {
				t.Fatalf("operand %q discovery = %#v", tc.operand, disc)
			}
			if disc.mutationForbidden != tc.wantForbidden {
				t.Errorf("operand %q: mutationForbidden = %v, want %v", tc.operand, disc.mutationForbidden, tc.wantForbidden)
			}
			if disc.mutationForbidden {
				formula := &molBondDiscovery{operand: "mol-test", formula: "mol-test"}
				if _, _, err := validateMolBondHomes(primaryStore, formula, disc); err == nil || !strings.Contains(err.Error(), "auto-routing") {
					t.Errorf("forbidden auto-routed operand error = %v, want clear refusal", err)
				}
			}
			localKey := storeIdentityKey(primaryStore)
			if tc.wantStore == "local" && disc.storeKey != localKey {
				t.Errorf("operand %q storeKey = %q, want local %q", tc.operand, disc.storeKey, localKey)
			}
			if tc.wantStore == "planning" && disc.storeKey == localKey {
				t.Errorf("operand %q unexpectedly resolved to local store", tc.operand)
			}
		})
	}
}

// TestMolBondPrefixRouting exercises the explicit routes.jsonl path reported in
// #4714 and gastownhall/gastown#4220. It proves that a same-rig pair shares one
// writable handle and can mutate that rig, while a genuinely cross-rig pair is
// rejected before mutation. It also guards issue-first resolution for IDs that
// look like formula names.
func TestMolBondPrefixRouting(t *testing.T) {
	ctx, townStore := setupMolBondPrefixRouting(t, map[string][]string{
		"gt":  {"gt-first", "gt-second"},
		"zz":  {"zz-other"},
		"mol": {"mol-polecat-work"},
	})

	discA, err := discoverMolBondOperand(ctx, townStore, "gt-first")
	if err != nil {
		t.Fatalf("discover first routed operand: %v", err)
	}
	defer discA.Close()
	discB, err := discoverMolBondOperand(ctx, townStore, "gt-second")
	if err != nil {
		t.Fatalf("discover second routed operand: %v", err)
	}
	defer discB.Close()
	targetID, _, err := validateMolBondHomes(townStore, discA, discB)
	if err != nil {
		t.Fatalf("same-rig operands rejected as cross-store: %v", err)
	}
	discA.Close()
	discB.Close()
	routed, err := resolveAndGetIssueForMutation(ctx, townStore, targetID)
	if err != nil {
		t.Fatalf("open accepted target writable: %v", err)
	}
	defer routed.Close()
	opA, err := materializeMolBondOperand(ctx, routed.Store, discA, nil)
	if err != nil {
		t.Fatalf("materialize first operand: %v", err)
	}
	opB, err := materializeMolBondOperand(ctx, routed.Store, discB, nil)
	if err != nil {
		t.Fatalf("materialize second operand: %v", err)
	}
	active := routed.Store
	if _, err := bondMolMol(ctx, active, opA.subgraph.Root, opB.subgraph.Root, types.BondTypeSequential, "test"); err != nil {
		t.Fatalf("bond same-rig operands: %v", err)
	}
	deps, err := active.GetDependenciesWithMetadata(ctx, "gt-second")
	if err != nil {
		t.Fatalf("read routed dependencies after bond: %v", err)
	}
	if len(deps) != 1 || deps[0].ID != "gt-first" {
		t.Fatalf("routed mutation landed incorrectly: dependencies=%v", deps)
	}

	crossA, err := discoverMolBondOperand(ctx, townStore, "gt-first")
	if err != nil {
		t.Fatalf("discover cross-store operand A: %v", err)
	}
	defer crossA.Close()
	crossB, err := discoverMolBondOperand(ctx, townStore, "zz-other")
	if err != nil {
		t.Fatalf("discover cross-store operand B: %v", err)
	}
	defer crossB.Close()
	if _, _, err := validateMolBondHomes(townStore, crossA, crossB); err == nil || !strings.Contains(err.Error(), "different stores/rigs") {
		t.Fatalf("cross-store pair error = %v, want explicit rejection", err)
	}

	formulaLike, err := discoverMolBondOperand(ctx, townStore, "mol-polecat-work")
	if err != nil {
		t.Fatalf("discover formula-like routed issue: %v", err)
	}
	defer formulaLike.Close()
	if formulaLike.issue == nil || formulaLike.issue.ID != "mol-polecat-work" || formulaLike.formula != "" {
		t.Fatalf("formula-like issue did not retain issue-first semantics: %#v", formulaLike)
	}

	routesPath := filepath.Join(filepath.Dir(dbPath), "routes.jsonl")
	routes, err := os.ReadFile(routesPath)
	if err != nil {
		t.Fatalf("read routes for failure case: %v", err)
	}
	routes = append(routes, []byte("{\"prefix\":\"bad-\",\"path\":\"missing-rig\"}\n")...)
	if err := os.WriteFile(routesPath, routes, 0644); err != nil {
		t.Fatalf("write failure route: %v", err)
	}
	if _, err := discoverMolBondOperand(ctx, townStore, "bad-target"); err == nil || !strings.Contains(err.Error(), "no dolt_database") {
		t.Fatalf("matched-route failure = %v, want actionable target metadata error", err)
	}
}

func setupMolBondPrefixRouting(t *testing.T, rigs map[string][]string) (context.Context, storage.DoltStorage) {
	t.Helper()
	initConfigForTest(t)
	ctx := context.Background()
	townDir := t.TempDir()
	townBeadsDir := filepath.Join(townDir, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0755); err != nil {
		t.Fatalf("create town beads dir: %v", err)
	}
	townDBPath := filepath.Join(townBeadsDir, "dolt")
	townStore := newTestStoreIsolatedDB(t, townDBPath, "hq")

	var routes strings.Builder
	for prefix, ids := range rigs {
		rigPath := "rig-" + prefix
		rigDBPath := filepath.Join(townDir, rigPath, ".beads", "dolt")
		rigStore := newTestStoreIsolatedDB(t, rigDBPath, prefix)
		for _, id := range ids {
			seedIssue(t, ctx, rigStore, id)
		}
		if err := rigStore.Close(); err != nil {
			t.Fatalf("close %s routed store: %v", prefix, err)
		}
		fmt.Fprintf(&routes, "{\"prefix\":%q,\"path\":%q}\n", prefix+"-", rigPath)
	}
	if err := os.WriteFile(filepath.Join(townBeadsDir, "routes.jsonl"), []byte(routes.String()), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	oldDBPath := dbPath
	dbPath = townDBPath
	t.Cleanup(func() { dbPath = oldDBPath })
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(townDir); err != nil {
		t.Fatalf("chdir town: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	return ctx, townStore
}
