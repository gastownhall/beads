package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/journalscan"
)

// Journal coverage has three halves, and this is the third. The issueops guards
// prove the mutation seam EMITS. The dolt scope guard proves the store binds
// activation to the transaction a mutation runs in. This one proves the
// activation setting reaches the plumbing at all — that every store and
// unit-of-work provider bd builds is built by something that turns the journal
// on according to its workspace's config.
//
// It exists because that half was enumerated by hand and was wrong three times
// running. First the setting was process-global. Then it was applied at the two
// root pre-run call sites, which missed `bd serve` (which builds its own
// provider for server-mode workspaces), routed creates and remote-cache
// hydration (which open a SECOND store for another workspace), the pluggable
// backend registry arm, and the personal-migration planning store. Every one of
// those ran with the journal off while the command reported success — and an
// empty journal is indistinguishable from a quiet one, so nothing surfaced.
//
// The check is deliberately syntactic and local: a function that calls a store
// or provider CONSTRUCTOR must call an activation helper in its OWN body, or
// carry a written exemption. No call-graph fixpoint, because a fixpoint is what
// lets "the caller does it" drift back in — and the caller is exactly what kept
// being wrong.

// storeConstructors are the low-level opens that hand back a store or a
// unit-of-work provider, keyed by IMPORT PATH and function name.
//
// Keying on the path rather than the local package identifier is what makes an
// aliased import (`sql "…/storage/dolt"`) still resolve: the scanner reads each
// file's import table and maps the identifier back to its path, so renaming the
// import cannot make a construction site vanish from the guard entirely — which
// is the worst failure mode a guard like this can have, because it reports
// success while checking nothing.
var storeConstructors = map[string]map[string]bool{
	"github.com/steveyegge/beads/internal/storage/dolt": {
		"New":                         true,
		"NewFromConfig":               true,
		"NewFromConfigWithOptions":    true,
		"NewFromConfigWithCLIOptions": true,
	},
	"github.com/steveyegge/beads/internal/storage/embeddeddolt": {
		"Open":                       true,
		"OpenReadOnly":               true,
		"OpenForReadOnlyCommand":     true,
		"OpenForPreviewCommand":      true,
		"OpenForWorkingSetReconcile": true,
	},
	"github.com/steveyegge/beads/internal/storage/uow": {
		"NewDoltServerUOWProvider":         true,
		"NewExternalDoltServerUOWProvider": true,
	},
	// The pluggable backend registry: Lookup is what turns a configured backend
	// name into something that can Open, so a function that calls it is
	// constructing a store just as much as the direct opens above.
	"github.com/steveyegge/beads/internal/storage/backends": {"Lookup": true},
}

// activationCalls are the helpers that apply a workspace's configured
// activation to the thing just constructed. Both the local cmd/bd wrappers and
// the shared package they delegate to count, because bd doctor's repair
// handlers cannot import package main and call the package directly.
var activationCalls = map[string]bool{
	"activateEventsJournalStore":    true,
	"activateEventsJournalProvider": true,
}

// qualifiedActivationCalls are the same, keyed by import path and function.
var qualifiedActivationCalls = map[string]map[string]bool{
	"github.com/steveyegge/beads/internal/eventsjournal": {
		"ActivateStore": true,
		"Apply":         true,
	},
}

// scannedPackages are the directories searched for construction sites: every
// package that builds a store or provider for the bd binary, plus the standalone
// embedded-Dolt utility so it is accounted for rather than merely unnoticed.
// Each maps to the prefix its sites are keyed under.
var scannedPackages = map[string]string{
	".":          "",
	"doctor":     "doctor/",
	"doctor/fix": "doctor/fix/",
	"../../internal/storage/embeddeddolt/cmd": "embeddeddolt-cmd/",
}

// constructionExemptions are construction sites that legitimately do NOT
// activate the events journal, each with a reason. Keyed by
// "<pkg>/<file>:<Recv.Func>" so the two build-tag twins of a factory are
// checked separately — a divergence between the CGO and non-CGO paths is
// exactly the kind of gap a name-keyed list would hide.
//
// A key ending in "/" exempts a whole package. That is used once, for bd
// doctor's CHECK half, where ~30 read-only sites share one true reason. It is
// deliberately NOT used for the repair half: a blanket there was how a false
// reason ("workspace repairs, not bead mutations") covered three handlers that
// delete and create issues.
//
// The staleness check below fails if an exemption stops matching a real
// construction site, so an exemption cannot rot into a permanent excuse.
var constructionExemptions = map[string]string{
	// Non-mutating opens. Every arm returns a store that refuses writes
	// (OpenReadOnly / OpenForPreviewCommand / a ReadOnly server config), so
	// there is no mutation for a journal row to accompany.
	"store_factory.go:openNonMutatingStoreFromConfig":   "read-only/preview open: the store refuses writes, so no mutation can go unrecorded",
	"store_factory_nocgo.go:newReadOnlyStoreFromConfig": "read-only open: the store refuses writes, so no mutation can go unrecorded",

	// Store-open-time reconciliation, which runs BEFORE the command's own store
	// exists and therefore before any workspace config could be applied to it.
	// It writes only local_metadata, which is dolt-ignored working-set state and
	// not a bead mutation. Journaling it would record a row for something a
	// replay consumer has no bead to apply it to.
	"version_tracking.go:autoMigrateOnVersionBump": "store-open-time version reconciliation: writes only dolt-ignored local_metadata, never a bead, and runs before the command's own store is configured",

	// Config reads through a throwaway store. GetConfig only; nothing here
	// mutates a bead.
	"ado.go:getADOConfigValue": "throwaway store used for a single GetConfig read; no bead mutation is possible through it",

	// `bd config apply` / drift detection open a store to read and reconcile the
	// workspace's Dolt REMOTE configuration. That is workspace state, not bead
	// rows, so there is nothing for a replay consumer to apply.
	"config_apply.go:applyRemote":      "reconciles the Dolt remote configuration, not beads: workspace state a replay consumer has no bead to apply it to",
	"config_drift.go:checkRemoteDrift": "reads the Dolt remote configuration to report drift; no bead mutation",

	// bd doctor's CHECKS: read-only inspection of workspace and bead state.
	// They report, they never write.
	//
	// This blanket once covered the repair half too, with the same "not bead
	// mutations" reason — and that reason was FALSE. Three repair handlers
	// delete or create issues (see openBeadMutatingStore in doctor/fix), and
	// they now open through an activating factory instead of inheriting an
	// exemption that was never true of them. That is the failure mode a
	// package-wide exemption invites, so this one states what it covers rather
	// than which directory it lives in: a check that grows a WRITE path must
	// leave it.
	"doctor/": "bd doctor checks: read-only inspection; they report state and never write it",

	// bd doctor's REPAIRS that remain exempt: they write WORKSPACE state, and
	// each is named individually so the reason can be checked against the
	// handler rather than assumed from its directory. The three bead-mutating
	// handlers are not here — StaleClosedIssues and PatrolPollution (which
	// DELETE issues) and the two fresh-clone import paths (which CREATE them)
	// open through openBeadMutatingStore and journal like any other mutation.
	"doctor/fix/metadata.go:FixMissingMetadata":            "writes bd_version / repo_id / clone_id workspace metadata, never a bead",
	"doctor/fix/metadata.go:FixProjectIdentity":            "writes the workspace's _project_id, never a bead",
	"doctor/fix/repo_fingerprint.go:RepoFingerprint":       "rewrites the workspace repo_id fingerprint, never a bead",
	"doctor/fix/repo_fingerprint.go:updateRepoIDInProcess": "rewrites the workspace repo_id fingerprint in-process, never a bead",

	// The root pre-run probes the backend registry to choose WHICH factory to
	// call; the construction itself is delegated to newRegisteredBackendStore,
	// which activates. This is the one place the guard's "Lookup means
	// construction" heuristic over-reports, because Lookup here decides a
	// branch rather than opening anything.
	"main.go:var rootCmd": "probes backends.Lookup to select a factory; the store is constructed by newRegisteredBackendStore, which activates",

	// A standalone developer utility binary, not bd. It has no workspace config
	// to read and never runs as part of a bd command.
	"embeddeddolt-cmd/main.go:main": "standalone embeddeddolt debug utility, not the bd binary; no workspace config and no bd command context",
}

func TestEveryStoreConstructionActivatesTheEventsJournal(t *testing.T) {
	sites := scanStoreConstructionSites(t)
	if len(sites) == 0 {
		t.Fatal("found no store construction sites — the constructor set or the scan changed and this guard is not actually running")
	}

	seenExempt := map[string]bool{}
	checked := 0
	for _, key := range sortedKeys(sites) {
		site := sites[key]
		if exemption, reason, ok := exemptionFor(key); ok {
			if strings.TrimSpace(reason) == "" {
				t.Errorf("%s has an empty exemption reason", key)
			}
			seenExempt[exemption] = true
			continue
		}
		checked++
		if !site.activates {
			t.Errorf("%s constructs a store or unit-of-work provider (%s) but never calls %v on it — "+
				"every mutation through it journals NOTHING while the command reports success. "+
				"Apply activation in this function (see newDoltStore for the idiom), or add it to "+
				"constructionExemptions with a reason.",
				key, strings.Join(site.constructors, ", "), sortedKeys(activationCalls))
		}
	}
	if checked == 0 {
		t.Fatal("every construction site is exempt — the guard is not actually checking anything")
	}
	for key := range constructionExemptions {
		if !seenExempt[key] {
			t.Errorf("exemption %q no longer matches a store construction site — remove it", key)
		}
	}
}

// exemptionFor resolves a site key against the exemption map: an exact match
// first, then the longest package prefix. It returns the exemption's own key so
// the staleness check can tell which entry is still earning its place.
func exemptionFor(key string) (exemption, reason string, ok bool) {
	if reason, found := constructionExemptions[key]; found {
		return key, reason, true
	}
	best := ""
	for candidate := range constructionExemptions {
		if !strings.HasSuffix(candidate, "/") || !strings.HasPrefix(key, candidate) {
			continue
		}
		if len(candidate) > len(best) {
			best = candidate
		}
	}
	if best == "" {
		return "", "", false
	}
	return best, constructionExemptions[best], true
}

type constructionSite struct {
	constructors []string
	activates    bool
}

// scanStoreConstructionSites parses each scanned package and returns one entry
// per function that calls a store/provider constructor, keyed by
// "<pkg>/<file>:<Recv.Func>".
//
// Parsing is done here rather than through journalscan.ParsePackage for three
// reasons: the key must include the FILE (so `store_factory.go` and
// `store_factory_nocgo.go` are checked as the separate build-tag twins they
// are, instead of one silently overwriting the other in a name-keyed map); the
// constructor match must see which PACKAGE a selector belongs to (`dolt.New`,
// not `New`); and that package must be resolved through the file's import
// table, so an aliased import still matches. The AST approach is journalscan's;
// only the key and the matcher differ.
//
// Honest limits, stated rather than papered over. The scan sees calls in
// function bodies (including function literals nested in them) and in the
// initializers of top-level var declarations. It does NOT resolve a constructor
// reached through a method value or a variable holding a function — and a
// brand-new package that opens stores is invisible until it is added to
// scannedPackages and storeConstructors. Those are the same shape of gap every
// name-based guard has; what matters is that the ones that HAVE bitten (a
// missed call site, a build-tag twin, an alias) are all covered.
func scanStoreConstructionSites(t *testing.T) map[string]*constructionSite {
	t.Helper()
	out := map[string]*constructionSite{}
	for _, dir := range sortedKeys(scannedPackages) {
		prefix := scannedPackages[dir]
		fset := token.NewFileSet()
		pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
			return !strings.HasSuffix(fi.Name(), "_test.go")
		}, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", dir, err)
		}
		for _, pkg := range pkgs {
			for path, file := range pkg.Files {
				base := filepath.Base(path)
				imports := fileImports(t, file)
				for _, decl := range file.Decls {
					switch d := decl.(type) {
					case *ast.FuncDecl:
						name := d.Name.Name
						if d.Recv != nil && len(d.Recv.List) > 0 {
							if recv := journalscan.ReceiverTypeName(d.Recv.List[0].Type); recv != "" {
								name = recv + "." + name
							}
						}
						if site := inspectConstructionSite(d, imports); site != nil {
							out[prefix+base+":"+name] = site
						}
					case *ast.GenDecl:
						// Package-level `var x = func() {...}` bodies open stores in
						// exactly the same way a func declaration does, and cost one
						// extra case to cover.
						if d.Tok != token.VAR {
							continue
						}
						for _, spec := range d.Specs {
							vs, ok := spec.(*ast.ValueSpec)
							if !ok || len(vs.Names) == 0 {
								continue
							}
							if site := inspectConstructionSite(d, imports); site != nil {
								out[prefix+base+":var "+vs.Names[0].Name] = site
							}
							break
						}
					}
				}
			}
		}
	}
	return out
}

// fileImports maps each import's local identifier to its path, honoring an
// explicit alias. Dot and blank imports are skipped: neither can produce a
// `pkg.Func` selector.
func fileImports(t *testing.T, file *ast.File) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatalf("unquote import %s: %v", spec.Path.Value, err)
		}
		name := path[strings.LastIndex(path, "/")+1:]
		if spec.Name != nil {
			if spec.Name.Name == "." || spec.Name.Name == "_" {
				continue
			}
			name = spec.Name.Name
		}
		out[name] = path
	}
	return out
}

func inspectConstructionSite(node ast.Node, imports map[string]string) *constructionSite {
	seen := map[string]bool{}
	site := &constructionSite{}
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			ident, ok := fun.X.(*ast.Ident)
			if !ok {
				return true
			}
			// Resolve the local identifier to the imported PATH, so an alias
			// still matches and a same-named local variable does not.
			path, imported := imports[ident.Name]
			if !imported {
				return true
			}
			if storeConstructors[path][fun.Sel.Name] {
				qualified := ident.Name + "." + fun.Sel.Name
				if !seen[qualified] {
					seen[qualified] = true
					site.constructors = append(site.constructors, qualified)
				}
			}
			if qualifiedActivationCalls[path][fun.Sel.Name] {
				site.activates = true
			}
		case *ast.Ident:
			if activationCalls[fun.Name] {
				site.activates = true
			}
		}
		return true
	})
	if len(site.constructors) == 0 {
		return nil
	}
	sort.Strings(site.constructors)
	return site
}
