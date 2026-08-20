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

// TestDiscoverMolBondOperandRouting is the regression test for the mol-bond
// routing bug (#4714): `bd mol bond <formula> <bead>` failed with
// "'<bead>' not found as issue or formula" whenever <bead> lived in a routed
// store (a prefix-routed rig, or the contributor planning store), even though
// `bd show <bead>` resolved it. mol bond resolved operands against the local
// store only and never fell through to the routing fallback that
// show/update/close use.
//
// discoverMolBondOperand resolves through the same local, prefix-route, and
// contributor auto-route order, read-only, before the accepted home is reopened
// writable.
//
// The cases mirror TestResolveCloseTargets:
//   - maintainer-mode local resolution (no routing involved)
//   - contributor-mode operand that lives only in the routed planning store
func TestDiscoverMolBondOperandRouting(t *testing.T) {
	cases := []struct {
		name          string
		role          string
		enableRouting bool
		routingKey    string
		localSeed     string
		planningSeed  string
		operand       string
		wantForbidden bool
		wantWritable  bool
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
			routingKey:    "routing.contributor",
			planningSeed:  "shared-routed",
			operand:       "shared-routed",
			wantForbidden: true,
			wantStore:     "planning",
		},
		{
			name:          "maintainer_routed_issue",
			role:          "maintainer",
			enableRouting: true,
			routingKey:    "routing.maintainer",
			planningSeed:  "shared-maintainer",
			operand:       "shared-maintainer",
			wantWritable:  true,
			wantStore:     "planning",
		},
		{
			name:          "default_routed_issue",
			role:          "maintainer",
			enableRouting: true,
			routingKey:    "routing.default",
			planningSeed:  "shared-default",
			operand:       "shared-default",
			wantWritable:  true,
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
			// Force repo-local hooks so tests ignore any global hooksPath override.
			runCmd(t, repoDir, "git", "config", "core.hooksPath", ".git/hooks")
			runCmd(t, repoDir, "git", "config", "beads.role", tc.role)

			primaryStore := newTestStoreIsolatedDB(t, filepath.Join(repoDir, ".beads", "beads.db"), "shared")
			ctx := context.Background()

			if tc.enableRouting {
				if err := primaryStore.SetConfig(ctx, "routing.mode", "auto"); err != nil {
					t.Fatalf("set routing.mode: %v", err)
				}
				if err := primaryStore.SetConfig(ctx, tc.routingKey, planningDir); err != nil {
					t.Fatalf("set %s: %v", tc.routingKey, err)
				}
			}

			if tc.localSeed != "" {
				seedIssue(t, ctx, primaryStore, tc.localSeed)
			}
			if tc.planningSeed != "" {
				planningStore := newTestStoreIsolatedDB(t, filepath.Join(planningDir, ".beads", "beads.db"), "shared")
				seedIssue(t, ctx, planningStore, tc.planningSeed)
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

			disc, err := discoverMolBondOperand(ctx, primaryStore, tc.operand, nil)
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
			localKey := dependencyStoreKey(primaryStore)
			if tc.wantStore == "local" && disc.storeKey != localKey {
				t.Errorf("operand %q storeKey = %q, want local %q", tc.operand, disc.storeKey, localKey)
			}
			if tc.wantStore == "planning" && disc.storeKey == localKey {
				t.Errorf("operand %q unexpectedly resolved to local store", tc.operand)
			}
			if tc.wantWritable {
				disc.Close()
				rr, err := resolveAndGetIssueForMutation(ctx, primaryStore, tc.operand)
				if err != nil {
					t.Fatalf("resolveAndGetIssueForMutation(%q): %v", tc.operand, err)
				}
				defer rr.Close()
				if rr.MutationForbidden {
					t.Fatalf("operand %q: maintainer/default route unexpectedly forbids mutation", tc.operand)
				}
				routedStore, ok := storage.UnwrapStore(rr.Store).(interface{ IsReadOnly() bool })
				if !ok {
					t.Fatalf("operand %q: routed store %T does not expose read-only state", tc.operand, rr.Store)
				}
				if routedStore.IsReadOnly() {
					t.Fatalf("operand %q: maintainer/default mutation route reopened read-only", tc.operand)
				}
			}
		})
	}
}

// TestDiscoverMolBondOperandFormulaWinsAmbiguousIssuePrefix preserves the
// issue-first contract without making an ambiguous issue prefix shadow a valid
// formula of the same name. The issue lookup error is actionable only when the
// formula parser also rejects the operand.
func TestDiscoverMolBondOperandFormulaWinsAmbiguousIssuePrefix(t *testing.T) {
	initConfigForTest(t)
	ctx := context.Background()

	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	localStore := newTestStoreIsolatedDB(t, filepath.Join(beadsDir, "beads.db"), "amb")
	seedIssue(t, ctx, localStore, "ambiguous-formula-one")
	seedIssue(t, ctx, localStore, "ambiguous-formula-two")

	formulaDir := filepath.Join(beadsDir, "formulas")
	if err := os.MkdirAll(formulaDir, 0o755); err != nil {
		t.Fatalf("create formula directory: %v", err)
	}
	const name = "ambiguous-formula"
	formula := []byte(`formula = "ambiguous-formula"
version = 1
type = "workflow"

[[steps]]
id = "step"
title = "Step"
type = "task"
`)
	if err := os.WriteFile(filepath.Join(formulaDir, name+".formula.toml"), formula, 0o644); err != nil {
		t.Fatalf("write formula: %v", err)
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	if err := os.Chdir(repoDir); err != nil {
		t.Fatalf("chdir repoDir: %v", err)
	}

	disc, err := discoverMolBondOperand(ctx, localStore, name, nil)
	if err != nil {
		t.Fatalf("discoverMolBondOperand(%q): %v", name, err)
	}
	defer disc.Close()
	if disc.issue != nil || disc.formula != name {
		t.Fatalf("ambiguous issue prefix should fall back to formula: %#v", disc)
	}
}

// TestMolBondPrefixRoutedWriteThrough verifies the bond MUTATION against a
// prefix-routed rig store, not just operand resolution: both operands live in
// a routed rig, read-only validation accepts the same-rig pair, the selected
// store reopens writable, and the dependency is visible
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

	otherRigBeadsDir := filepath.Join(tmpDir, "other-rig", ".beads")
	if err := os.MkdirAll(otherRigBeadsDir, 0755); err != nil {
		t.Fatalf("create other rig beads dir: %v", err)
	}
	otherStore := newTestStoreIsolatedDB(t, filepath.Join(otherRigBeadsDir, "dolt"), "zz")
	seedIssue(t, ctx, otherStore, "zz-other")
	otherStore.Close()

	routesPath := filepath.Join(townBeadsDir, "routes.jsonl")
	routes := []byte("{\"prefix\":\"gt-\",\"path\":\"rig\"}\n{\"prefix\":\"zz-\",\"path\":\"other-rig\"}\n")
	if err := os.WriteFile(routesPath, routes, 0644); err != nil {
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

	discA, err := discoverMolBondOperand(ctx, townStore, "gt-mol1", nil)
	if err != nil {
		t.Fatalf("discover first routed operand: %v", err)
	}
	defer discA.Close()
	discB, err := discoverMolBondOperand(ctx, townStore, "gt-mol2", nil)
	if err != nil {
		t.Fatalf("discover second routed operand: %v", err)
	}
	defer discB.Close()
	targetID, targetKey, err := validateMolBondHomes(townStore, discA, discB)
	if err != nil {
		t.Fatalf("validate same-rig operands: %v", err)
	}
	discA.Close()
	discB.Close()

	routed, err := resolveAndGetIssueForMutation(ctx, townStore, targetID)
	if err != nil {
		t.Fatalf("open accepted target writable: %v", err)
	}
	defer routed.Close()
	if got := dependencyStoreKey(routed.Store); got != targetKey {
		t.Fatalf("writable reopen changed store: got %q, want %q", got, targetKey)
	}
	opA, err := materializeMolBondOperand(ctx, routed.Store, discA, nil)
	if err != nil {
		t.Fatalf("materialize first operand: %v", err)
	}
	opB, err := materializeMolBondOperand(ctx, routed.Store, discB, nil)
	if err != nil {
		t.Fatalf("materialize second operand: %v", err)
	}
	if _, err := bondMolMol(ctx, routed.Store, opA.subgraph.Root, opB.subgraph.Root, types.BondTypeSequential, "test"); err != nil {
		t.Fatalf("bondMolMol against the routed store: %v", err)
	}
	routed.Close()

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
	rr.Close()

	crossA, err := discoverMolBondOperand(ctx, townStore, "gt-mol1", nil)
	if err != nil {
		t.Fatalf("discover cross-store operand A: %v", err)
	}
	defer crossA.Close()
	crossB, err := discoverMolBondOperand(ctx, townStore, "zz-other", nil)
	if err != nil {
		t.Fatalf("discover cross-store operand B: %v", err)
	}
	defer crossB.Close()
	if _, _, err := validateMolBondHomes(townStore, crossA, crossB); err == nil || !strings.Contains(err.Error(), "different stores/rigs") {
		t.Fatalf("cross-store pair error = %v, want explicit rejection", err)
	}
	crossA.Close()
	crossB.Close()

	routes = append(routes, []byte("{\"prefix\":\"bad-\",\"path\":\"missing-rig\"}\n")...)
	if err := os.WriteFile(routesPath, routes, 0644); err != nil {
		t.Fatalf("write matched failure route: %v", err)
	}
	if _, err := discoverMolBondOperand(ctx, townStore, "bad-target", nil); err == nil || !strings.Contains(err.Error(), "no dolt_database") {
		t.Fatalf("matched-route failure = %v, want actionable target metadata error", err)
	}

	drift, err := discoverMolBondOperand(ctx, townStore, "zz-other", nil)
	if err != nil {
		t.Fatalf("discover drift fixture: %v", err)
	}
	_, driftKey, err := validateMolBondHomes(townStore, drift)
	if err != nil {
		t.Fatalf("validate drift fixture: %v", err)
	}
	drift.Close()
	seedIssue(t, ctx, townStore, "zz-other")
	reopened, err := resolveAndGetIssueForMutation(ctx, townStore, "zz-other")
	if err != nil {
		t.Fatalf("reopen drift fixture: %v", err)
	}
	defer reopened.Close()
	if err := verifyMolBondWritableHome(reopened, driftKey); err == nil || !strings.Contains(err.Error(), "changed between discovery") {
		t.Fatalf("reopen identity mismatch = %v, want retryable safety error", err)
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
	// must NOT compare as the same store, or validation would silently
	// allow the cross-store bonds it exists to reject.
	if sameBondStore(townStore, rrA.Store) {
		t.Fatal("sameBondStore: handles onto different databases must not compare as the same store")
	}

	wrapped := storage.NewHookFiringStore(rrA.Store, nil)
	if !sameBondStore(wrapped, rrB.Store) {
		t.Fatal("sameBondStore: decorator-wrapped and raw handles onto the same database must compare equal")
	}
}

// TestMolBondAutoRoutedReadOnlyFailsFast verifies the write-path guard for
// contributor auto-routing: the planning store deliberately opens read-only
// (it hydrates a foreign project that must never be mutated), so a bond pinned
// to it must fail fast during read-only validation — before any
// mutation is attempted — rather than surface a store-layer write refusal
// halfway through the bond.
func TestMolBondAutoRoutedReadOnlyFailsFast(t *testing.T) {
	initConfigForTest(t)
	ctx := context.Background()

	tmpDir := t.TempDir()
	repoDir := filepath.Join(tmpDir, "repo")
	planningDir := filepath.Join(tmpDir, "planning")

	runCmd(t, tmpDir, "git", "init", repoDir)
	// Force repo-local hooks so tests ignore any global hooksPath override.
	runCmd(t, repoDir, "git", "config", "core.hooksPath", ".git/hooks")
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

	discA, err := discoverMolBondOperand(ctx, primaryStore, "shared-ro1", nil)
	if err != nil {
		t.Fatalf("discover first auto-routed operand: %v", err)
	}
	defer discA.Close()
	discB, err := discoverMolBondOperand(ctx, primaryStore, "shared-ro2", nil)
	if err != nil {
		t.Fatalf("discover second auto-routed operand: %v", err)
	}
	defer discB.Close()

	_, _, err = validateMolBondHomes(primaryStore, discA, discB)
	if err == nil {
		t.Fatal("validateMolBondHomes should reject a bond pinned to the contributor auto-routed store")
	}
	if !strings.Contains(err.Error(), "auto-routing") || !strings.Contains(err.Error(), "forbids mutation") {
		t.Errorf("fail-fast error should explain the forbidden contributor auto-route, got: %v", err)
	}
}
