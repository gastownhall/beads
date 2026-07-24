//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestResolveMolBondOperandsRouting is the regression test for the mol-bond
// routing bug (#4714): `bd mol bond <formula> <bead>` failed with
// "'<bead>' not found as issue or formula" whenever <bead> lived in a routed
// store (a prefix-routed rig, or the contributor planning store), even though
// `bd show <bead>` resolved it. mol bond resolved operands against the local
// store only and never fell through to the routing fallback that
// show/update/close use.
//
// resolveMolBondOperands now mirrors resolveCloseTargets (local store, then
// prefix routing, then the shared contributor auto-routed store), so an
// operand that exists only in a routed store resolves and reports the routed
// store as its owner.
//
// The cases mirror TestResolveCloseTargets:
//   - maintainer-mode local resolution (no routing involved)
//   - contributor-mode operands that live only in the routed planning store;
//     both operands must share one routed handle, and the planning store opens
//     read-only so the operands are marked non-writable
func TestResolveMolBondOperandsRouting(t *testing.T) {
	cases := []struct {
		name          string
		role          string   // git config beads.role value
		enableRouting bool     // set routing.mode=auto + routing.contributor=<planningDir>
		localSeed     []string // issue IDs to create in the primary (local) store
		planningSeed  []string // issue IDs to create in the routed planning store
		operands      [2]string
		wantRouted    bool // expected molBondOperand.routed for both operands
		wantWritable  bool // expected molBondOperand.writable for both operands
	}{
		{
			name:         "maintainer_local_issues",
			role:         "maintainer",
			localSeed:    []string{"shared-local1", "shared-local2"},
			operands:     [2]string{"shared-local1", "shared-local2"},
			wantRouted:   false,
			wantWritable: true,
		},
		{
			name:          "contributor_routed_issues_share_one_readonly_handle",
			role:          "contributor",
			enableRouting: true,
			planningSeed:  []string{"shared-r1", "shared-r2"},
			operands:      [2]string{"shared-r1", "shared-r2"},
			wantRouted:    true,
			wantWritable:  false,
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
				// Release the planning store so resolveMolBondOperands can open
				// the routed store through the normal command path.
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

			ops, cleanup, err := resolveMolBondOperands(ctx, primaryStore, tc.operands, nil)
			if err != nil {
				t.Fatalf("resolveMolBondOperands(%v): %v", tc.operands, err)
			}
			defer cleanup()

			for i, op := range ops {
				operand := tc.operands[i]
				if op.subgraph == nil || op.subgraph.Root == nil {
					t.Fatalf("operand %q: missing resolved subgraph", operand)
				}
				if op.subgraph.Root.ID != operand {
					t.Errorf("operand %q: Root.ID = %q, want %q", operand, op.subgraph.Root.ID, operand)
				}
				if op.cooked {
					t.Errorf("operand %q: cooked = true, want false (it is an existing issue)", operand)
				}
				if op.routed != tc.wantRouted {
					t.Errorf("operand %q: routed = %v, want %v", operand, op.routed, tc.wantRouted)
				}
				if op.writable != tc.wantWritable {
					t.Errorf("operand %q: writable = %v, want %v", operand, op.writable, tc.wantWritable)
				}
				if !tc.wantRouted && op.store != primaryStore {
					t.Errorf("operand %q: store should be the local primary store", operand)
				}
				if tc.wantRouted && op.store == primaryStore {
					t.Errorf("operand %q: store should be the routed planning store, not the local one", operand)
				}
			}

			// Both routed operands must share one routed handle: identity is
			// what pickBondStore checks first, and a second open of the same
			// database would be wasted work.
			if tc.wantRouted && ops[0].store != ops[1].store {
				t.Errorf("routed operands should share one routed store handle")
			}
		})
	}
}

// TestMolBondPrefixRoutedWriteThrough verifies the bond MUTATION against a
// prefix-routed rig store, not just operand resolution: both operands live in
// a routed rig, pickBondStore selects the routed store (a same-rig pair must
// not be rejected as cross-store), the bond dependency write succeeds because
// the prefix-routed store opened write-intent, and the dependency is visible
// afterward through a fresh routed read — proving the write committed in the
// routed database rather than the local one.
//
// NOTE: This test uses os.Chdir and cannot run in parallel with other tests.
func TestMolBondPrefixRoutedWriteThrough(t *testing.T) {
	initConfigForTest(t)
	ctx := context.Background()

	tmpDir := t.TempDir()
	townBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0755); err != nil {
		t.Fatalf("create town beads dir: %v", err)
	}
	rigBeadsDir := filepath.Join(tmpDir, "rig", ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatalf("create rig beads dir: %v", err)
	}

	townDBPath := filepath.Join(townBeadsDir, "dolt")
	townStore := newTestStoreIsolatedDB(t, townDBPath, "hq")

	rigDBPath := filepath.Join(rigBeadsDir, "dolt")
	rigStore := newTestStoreIsolatedDB(t, rigDBPath, "gt")
	seedIssue(t, ctx, rigStore, "gt-mol1")
	seedIssue(t, ctx, rigStore, "gt-mol2")
	// Release the rig store before routing reopens it.
	rigStore.Close()

	routesPath := filepath.Join(townBeadsDir, "routes.jsonl")
	if err := os.WriteFile(routesPath, []byte(`{"prefix":"gt-","path":"rig"}`), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	oldDbPath := dbPath
	dbPath = townDBPath
	t.Cleanup(func() { dbPath = oldDbPath })

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	ops, cleanup, err := resolveMolBondOperands(ctx, townStore, [2]string{"gt-mol1", "gt-mol2"}, nil)
	if err != nil {
		t.Fatalf("resolveMolBondOperands: %v", err)
	}
	for i, operand := range []string{"gt-mol1", "gt-mol2"} {
		if !ops[i].routed {
			cleanup()
			t.Fatalf("operand %q: routed = false, want true", operand)
		}
		if !ops[i].writable {
			cleanup()
			t.Fatalf("operand %q: writable = false, want true (prefix routing is write-intent)", operand)
		}
	}
	if ops[0].store != ops[1].store {
		cleanup()
		t.Fatal("same-rig operands should share one routed store handle")
	}

	// A same-rig pair must not be rejected as a cross-store bond.
	activeStore, err := pickBondStore(townStore, ops[0], ops[1])
	if err != nil {
		cleanup()
		t.Fatalf("pickBondStore rejected a same-rig pair: %v", err)
	}
	if activeStore != ops[0].store {
		cleanup()
		t.Fatal("pickBondStore should select the routed store that owns the operands")
	}

	// The actual mutation: this is what a resolution-only test misses. The
	// routed store opened write-intent, so the dependency write must succeed.
	if _, err := bondMolMol(ctx, activeStore, ops[0].subgraph.Root, ops[1].subgraph.Root, types.BondTypeSequential, "test"); err != nil {
		cleanup()
		t.Fatalf("bondMolMol against the routed store: %v", err)
	}
	cleanup()

	// Verify through a fresh routed read that the bond committed in the rig
	// database: gt-mol2 (B) now depends on gt-mol1 (A).
	rr, err := resolveViaPrefixRoutingWithAccess(ctx, "gt-mol2", false)
	if err != nil {
		t.Fatalf("reopening routed store to verify the write: %v", err)
	}
	defer rr.Close()
	deps, err := rr.Store.GetDependenciesWithMetadata(ctx, "gt-mol2")
	if err != nil {
		t.Fatalf("reading dependencies from the routed store: %v", err)
	}
	found := false
	for _, dep := range deps {
		if dep.ID == "gt-mol1" && dep.DependencyType == types.DepBlocks {
			found = true
		}
	}
	if !found {
		t.Fatalf("bond did not commit in the routed store: gt-mol2 dependencies = %+v", deps)
	}
}

// TestSameBondStoreSeparateHandles is the regression test for the store
// identity check: two separately-opened handles onto the same routed database
// (each route-open constructs a fresh store in server mode) must compare as
// the same store, not be rejected as a cross-store bond.
//
// NOTE: This test uses os.Chdir and cannot run in parallel with other tests.
func TestSameBondStoreSeparateHandles(t *testing.T) {
	initConfigForTest(t)
	ctx := context.Background()

	tmpDir := t.TempDir()
	townBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(townBeadsDir, 0755); err != nil {
		t.Fatalf("create town beads dir: %v", err)
	}
	rigBeadsDir := filepath.Join(tmpDir, "rig", ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatalf("create rig beads dir: %v", err)
	}

	townDBPath := filepath.Join(townBeadsDir, "dolt")
	townStore := newTestStoreIsolatedDB(t, townDBPath, "hq")

	rigDBPath := filepath.Join(rigBeadsDir, "dolt")
	rigStore := newTestStoreIsolatedDB(t, rigDBPath, "gt")
	seedIssue(t, ctx, rigStore, "gt-idn1")
	rigStore.Close()

	routesPath := filepath.Join(townBeadsDir, "routes.jsonl")
	if err := os.WriteFile(routesPath, []byte(`{"prefix":"gt-","path":"rig"}`), 0644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	oldDbPath := dbPath
	dbPath = townDBPath
	t.Cleanup(func() { dbPath = oldDbPath })

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	rrA, err := resolveViaPrefixRoutingWithAccess(ctx, "gt-idn1", true)
	if err != nil {
		t.Fatalf("first route-open: %v", err)
	}
	defer rrA.Close()
	rrB, err := resolveViaPrefixRoutingWithAccess(ctx, "gt-idn1", true)
	if err != nil {
		t.Fatalf("second route-open: %v", err)
	}
	defer rrB.Close()

	if rrA.Store == rrB.Store {
		t.Fatal("test precondition: expected two distinct store handles from separate route-opens")
	}
	if !sameBondStore(rrA.Store, rrB.Store) {
		t.Fatal("sameBondStore: two handles onto the same routed database must compare as the same store")
	}

	// The negative case matters just as much: genuinely different databases
	// must NOT compare as the same store, or pickBondStore would silently
	// allow the cross-store bonds it exists to reject.
	if sameBondStore(townStore, rrA.Store) {
		t.Fatal("sameBondStore: handles onto different databases must not compare as the same store")
	}
}

// TestMolBondAutoRoutedReadOnlyFailsFast verifies the write-path guard for
// contributor auto-routing: the planning store deliberately opens read-only
// (it hydrates a foreign project that must never be mutated), so a bond pinned
// to it must fail fast with a clear message from pickBondStore — before any
// mutation is attempted — rather than surface a store-layer write refusal
// halfway through the bond.
func TestMolBondAutoRoutedReadOnlyFailsFast(t *testing.T) {
	initConfigForTest(t)
	ctx := context.Background()

	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	planningDir := filepath.Join(tmpDir, "planning")

	runCmd(t, tmpDir, "git", "init", repoDir)
	runCmd(t, repoDir, "git", "config", "beads.role", "contributor")

	primaryStore := newTestStoreIsolatedDB(t, filepath.Join(repoDir, ".beads", "beads.db"), "shared")
	if err := primaryStore.SetConfig(ctx, "routing.mode", "auto"); err != nil {
		t.Fatalf("set routing.mode: %v", err)
	}
	if err := primaryStore.SetConfig(ctx, "routing.contributor", planningDir); err != nil {
		t.Fatalf("set routing.contributor: %v", err)
	}

	planningStore := newTestStoreIsolatedDB(t, filepath.Join(planningDir, ".beads", "beads.db"), "shared")
	seedIssue(t, ctx, planningStore, "shared-ro1")
	seedIssue(t, ctx, planningStore, "shared-ro2")
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

	ops, cleanup, err := resolveMolBondOperands(ctx, primaryStore, [2]string{"shared-ro1", "shared-ro2"}, nil)
	if err != nil {
		t.Fatalf("resolveMolBondOperands: %v", err)
	}
	defer cleanup()

	_, err = pickBondStore(primaryStore, ops[0], ops[1])
	if err == nil {
		t.Fatal("pickBondStore should reject a bond pinned to the read-only auto-routed store")
	}
	if !strings.Contains(err.Error(), "read-only") || !strings.Contains(err.Error(), "auto-routed") {
		t.Errorf("fail-fast error should explain the read-only auto-routed store, got: %v", err)
	}
}
