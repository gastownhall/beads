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
		wantReadOnly  bool   // expected mutation policy for the resolved store
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
			wantReadOnly:  true,
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
			if op.readOnly != tc.wantReadOnly {
				t.Errorf("operand %q: readOnly = %v, want %v", tc.operand, op.readOnly, tc.wantReadOnly)
			}
			if op.readOnly {
				formula := &molBondOperand{cooked: true}
				if _, err := pickBondStore(primaryStore, formula, op); err == nil || !strings.Contains(err.Error(), "auto-routing") {
					t.Errorf("read-only auto-routed operand error = %v, want clear refusal", err)
				}
			}

			assertStoreOrigin(t, tc.operand, tc.wantStore, op.store, primaryStore)
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

	opA, err := resolveMolBondOperand(ctx, townStore, "gt-first", nil)
	if err != nil {
		t.Fatalf("resolve first routed operand: %v", err)
	}
	defer opA.Close()
	opB, err := resolveMolBondOperandWithPreferredStore(ctx, townStore, opA, "gt-second", nil)
	if err != nil {
		t.Fatalf("resolve second routed operand: %v", err)
	}
	defer opB.Close()
	if opA.store != opB.store {
		t.Fatal("same-rig operands did not reuse one routed store handle")
	}
	active, err := pickBondStore(townStore, opA, opB)
	if err != nil {
		t.Fatalf("same-rig operands rejected as cross-store: %v", err)
	}
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

	crossA, err := resolveMolBondOperand(ctx, townStore, "gt-first", nil)
	if err != nil {
		t.Fatalf("resolve cross-store operand A: %v", err)
	}
	defer crossA.Close()
	crossB, err := resolveMolBondOperandWithPreferredStore(ctx, townStore, crossA, "zz-other", nil)
	if err != nil {
		t.Fatalf("resolve cross-store operand B: %v", err)
	}
	defer crossB.Close()
	if _, err := pickBondStore(townStore, crossA, crossB); err == nil || !strings.Contains(err.Error(), "different stores/rigs") {
		t.Fatalf("cross-store pair error = %v, want explicit rejection", err)
	}

	formulaLike, err := resolveMolBondOperand(ctx, townStore, "mol-polecat-work", nil)
	if err != nil {
		t.Fatalf("resolve formula-like routed issue: %v", err)
	}
	defer formulaLike.Close()
	if formulaLike.cooked || formulaLike.subgraph.Root.ID != "mol-polecat-work" {
		t.Fatalf("formula-like issue did not retain issue-first semantics: cooked=%v root=%q", formulaLike.cooked, formulaLike.subgraph.Root.ID)
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
