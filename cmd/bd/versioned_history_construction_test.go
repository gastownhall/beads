package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/journalscan"
)

// The twin of TestEveryStoreConstructionActivatesTheEventsJournal, for the other
// per-store switch bd resolves when it opens a store: dual-write issue-version
// history.
//
// It exists for the same reason and has the same failure shape. Activation that
// is applied by whichever command happens to use a store, rather than by the
// factory that builds it, reaches only the stores those commands open. The first
// cut of this feature was wired in wireStorageDecorators, which the root pre-run
// calls on the ONE store a command opens for its own workspace. A routed update
// and close (routed.go), a routed create (`bd create --repo`) and the direct-mode
// open each build their OWN write-capable store through newDoltStoreFromConfig,
// ran with history off, and reported success -- leaving issue_versions with
// silent gaps in a workspace that had asked for it. Nothing in the process can
// tell: a missing version row looks exactly like a quiet one.
//
// The scan, the constructor set, the scanned packages and the exemption list are
// the events-journal guard's, shared rather than copied. A store that refuses
// writes, a store used only for a config read and a store that writes only
// workspace state are exempt from both for the same reason: there is no bead
// mutation for a journal row OR a version row to accompany, and one list means
// an exemption cannot be true of one feature and quietly forgotten for the other.
//
// Limits, stated rather than papered over. The check is syntactic and local, like
// its twin: a function that calls a store or provider constructor must call an
// activation helper in its OWN body. bd doctor is exempted as a package by the
// shared list, so the repair opener in doctor/fix/store.go is held to the
// property by its own test in that package
// (TestVersionedHistoryDoctorRepairOpenerUsesTheSharedActivation).

// versionedHistoryActivationCalls are the cmd/bd helpers that apply a store's own
// configured switch to the thing just constructed.
var versionedHistoryActivationCalls = map[string]bool{
	"activateVersionedHistoryStore":    true,
	"activateVersionedHistoryProvider": true,
}

// qualifiedVersionedHistoryActivationCalls are the same, keyed by import path and
// function, for callers that cannot import package main.
//
// Apply is deliberately NOT here, unlike its events-journal twin. Apply binds a
// value somebody else resolved; it is ActivateStore/ActivateProvider that resolve
// the switch, so a caller that only Applies has decided the answer by hand.
var qualifiedVersionedHistoryActivationCalls = map[string]map[string]bool{
	"github.com/steveyegge/beads/internal/versionedhistory": {
		"ActivateStore":    true,
		"ActivateProvider": true,
	},
}

// versionedHistoryConstructionViolations returns one message per non-exempt
// construction site that never calls a versioned-history activation helper. It
// is the guard's whole verdict, factored out so the mutation probe below asks the
// SAME question of a factory whose activation has been deleted.
func versionedHistoryConstructionViolations(sites map[string]*constructionSite) []string {
	var out []string
	for _, key := range sortedKeys(sites) {
		site := sites[key]
		if _, reason, ok := exemptionFor(key); ok {
			if strings.TrimSpace(reason) == "" {
				out = append(out, fmt.Sprintf("%s has an empty exemption reason", key))
			}
			continue
		}
		if !site.activatesVersionedHistory {
			out = append(out, fmt.Sprintf("%s constructs a store or unit-of-work provider (%s) but never calls %v on it -- "+
				"every mutation through it mints NO issue_versions row while the command reports success. "+
				"Apply activation in this function (see newDoltStore for the idiom), or add it to "+
				"constructionExemptions with a reason that is true of issue versions as well as the events journal.",
				key, strings.Join(site.constructors, ", "), sortedKeys(versionedHistoryActivationCalls)))
		}
	}
	return out
}

func TestEveryStoreConstructionActivatesVersionedHistory(t *testing.T) {
	sites := scanStoreConstructionSites(t)
	if len(sites) == 0 {
		t.Fatal("found no store construction sites -- the constructor set or the scan changed and this guard is not actually running")
	}
	checked := 0
	for key := range sites {
		if _, _, exempt := exemptionFor(key); !exempt {
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("every construction site is exempt -- the guard is not actually checking anything")
	}
	for _, v := range versionedHistoryConstructionViolations(sites) {
		t.Error(v)
	}
}

// TestVersionedHistoryGuardSeesTheFactoriesEveryWriteUses pins that the scan
// really reaches the sites this feature was wrong about, in BOTH build-tag
// twins. A guard that quietly stops seeing a factory reports success while
// checking nothing, which is the worst way for one to fail.
//
// routed.go, create.go and direct_mode.go do not construct a store themselves;
// they call newDoltStoreFromConfig, so it is that factory the guard has to hold.
func TestVersionedHistoryGuardSeesTheFactoriesEveryWriteUses(t *testing.T) {
	sites := scanStoreConstructionSites(t)
	for _, key := range versionedHistoryFactoryKeys {
		site, ok := sites[key]
		if !ok {
			t.Errorf("the construction scan no longer sees %s", key)
			continue
		}
		if _, _, exempt := exemptionFor(key); exempt {
			t.Errorf("%s is exempt from the guard, but it opens a write-capable plumbing", key)
			continue
		}
		if !site.activatesVersionedHistory {
			t.Errorf("%s does not activate versioned history", key)
		}
	}
}

// versionedHistoryFactoryKeys are the construction sites every routed, direct and
// served write goes through. Each is named so that a factory disappearing from
// the scan fails a test instead of shrinking the guard.
var versionedHistoryFactoryKeys = []string{
	"store_factory.go:newDoltStore",
	"store_factory.go:newDoltStoreFromConfig",
	"store_factory.go:newRegisteredBackendStore",
	"store_factory_nocgo.go:newDoltStore",
	"store_factory_nocgo.go:newDoltStoreFromConfig",
	"store_factory_nocgo.go:newRegisteredBackendStore",
	"migrate_personal.go:openMigrationPlanningStore",
	"uow_factory.go:newExternalProxiedServerUOWProvider",
	"uow_factory.go:newManagedProxiedServerUOWProvider",
}

// TestVersionedHistoryConstructionGuardFailsWhenAFactoryLosesItsActivation is the
// mutation probe. For each guarded factory it takes the factory's real source,
// deletes the versioned-history activation from THAT function alone, re-runs the
// scanner's own inspection on the result, and asks the guard's own verdict
// function whether it now objects.
//
// It answers a different question from the guard above. The guard says "every
// factory activates today". This says "the guard would notice if one stopped":
// a detector that matched too loosely (any function that merely mentions the
// helper's name, a substring in a comment, the wrong build-tag twin) would pass
// the guard forever and catch nothing. The probe also checks that the deletion
// removed the versioned-history activation ONLY, leaving the events-journal
// activation standing, so a probe that over-deleted cannot pass for the right
// reason.
func TestVersionedHistoryConstructionGuardFailsWhenAFactoryLosesItsActivation(t *testing.T) {
	sites := scanStoreConstructionSites(t)

	probed := 0
	for _, key := range versionedHistoryFactoryKeys {
		site, ok := sites[key]
		if !ok {
			t.Errorf("the construction scan no longer sees %s", key)
			continue
		}
		fn, imports := parseConstructionSiteFunc(t, key)
		before := inspectConstructionSite(fn, imports)
		if before == nil || !before.activatesVersionedHistory {
			t.Errorf("%s has no versioned-history activation to delete, so this probe cannot show that removing it is noticed", key)
			continue
		}

		dropped := dropVersionedHistoryActivation(fn, imports)
		if dropped == 0 {
			t.Errorf("%s: the scanner sees an activation but the probe found no statement to delete -- the probe and the scanner disagree about what activation looks like", key)
			continue
		}
		after := inspectConstructionSite(fn, imports)
		if after == nil {
			t.Errorf("%s: deleting the activation also deleted the construction itself, so the probe proved nothing; the factory must keep the open and its activation as separate statements", key)
			continue
		}
		if after.activates != site.activates {
			t.Errorf("%s: the probe changed the events-journal activation (%v -> %v); it must remove the versioned-history activation only", key, site.activates, after.activates)
		}
		if after.activatesVersionedHistory {
			t.Errorf("%s: still reads as activating versioned history after its activation was deleted -- the detector is too loose", key)
			continue
		}
		violations := versionedHistoryConstructionViolations(map[string]*constructionSite{key: after})
		if len(violations) == 0 {
			t.Errorf("%s: the guard raised no violation for a factory whose versioned-history activation was deleted", key)
			continue
		}
		if !strings.Contains(violations[0], key) {
			t.Errorf("%s: the violation does not name the factory: %q", key, violations[0])
		}
		probed++
	}
	if probed != len(versionedHistoryFactoryKeys) {
		t.Errorf("probed %d of %d guarded factories", probed, len(versionedHistoryFactoryKeys))
	}
}

// parseConstructionSiteFunc re-parses the source file a construction-site key
// names and returns the function declaration together with that file's import
// table. It parses afresh on every call because the probe mutates the tree.
func parseConstructionSiteFunc(t *testing.T, key string) (*ast.FuncDecl, map[string]string) {
	t.Helper()
	dir, file, name := "", "", ""
	for candidatePrefix, candidateDir := range prefixToScannedDir() {
		if strings.HasPrefix(key, candidatePrefix) && len(candidatePrefix) >= len(dir) {
			rest := strings.TrimPrefix(key, candidatePrefix)
			if before, after, ok := strings.Cut(rest, ":"); ok && !strings.Contains(before, "/") {
				dir, file, name = candidateDir, before, after
			}
		}
	}
	if file == "" {
		t.Fatalf("cannot locate the source of construction site %q", key)
	}
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, filepath.Join(dir, file), nil, 0)
	if err != nil {
		t.Fatalf("parse %s/%s: %v", dir, file, err)
	}
	imports := fileImports(t, parsed)
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		got := fn.Name.Name
		if fn.Recv != nil && len(fn.Recv.List) > 0 {
			if recv := journalscan.ReceiverTypeName(fn.Recv.List[0].Type); recv != "" {
				got = recv + "." + got
			}
		}
		if got == name {
			return fn, imports
		}
	}
	t.Fatalf("construction site %q: no function %q in %s/%s", key, name, dir, file)
	return nil, nil
}

// prefixToScannedDir inverts scannedPackages: the key prefix each package's sites
// carry, mapped back to the directory the scanner read them from.
func prefixToScannedDir() map[string]string {
	out := make(map[string]string, len(scannedPackages))
	for dir, prefix := range scannedPackages {
		out[prefix] = dir
	}
	return out
}

// dropVersionedHistoryActivation deletes, in place and at any depth, every simple
// statement in fn that calls a versioned-history activation helper, and returns
// how many it removed. Compound statements are recursed into rather than removed,
// so the surrounding control flow (and any events-journal activation beside it)
// survives.
func dropVersionedHistoryActivation(fn *ast.FuncDecl, imports map[string]string) int {
	dropped := 0
	filter := func(list []ast.Stmt) []ast.Stmt {
		out := make([]ast.Stmt, 0, len(list))
		for _, st := range list {
			switch st.(type) {
			case *ast.DeferStmt, *ast.ExprStmt, *ast.AssignStmt, *ast.GoStmt:
				if callsVersionedHistoryActivation(st, imports) {
					dropped++
					continue
				}
			}
			out = append(out, st)
		}
		return out
	}
	ast.Inspect(fn, func(n ast.Node) bool {
		switch b := n.(type) {
		case *ast.BlockStmt:
			b.List = filter(b.List)
		case *ast.CaseClause:
			b.Body = filter(b.Body)
		case *ast.CommClause:
			b.Body = filter(b.Body)
		}
		return true
	})
	return dropped
}

func callsVersionedHistoryActivation(node ast.Node, imports map[string]string) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			if versionedHistoryActivationCalls[fun.Name] {
				found = true
			}
		case *ast.SelectorExpr:
			if ident, ok := fun.X.(*ast.Ident); ok {
				if path, imported := imports[ident.Name]; imported && qualifiedVersionedHistoryActivationCalls[path][fun.Sel.Name] {
					found = true
				}
			}
		}
		return !found
	})
	return found
}
