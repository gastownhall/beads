package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/journalscan"
)

// The foreign-workspace invariant (foreign_workspace.go) is kept by routing
// every open of a workspace bd was not launched in through the metadata store
// factories through openForeignStore, which runs admitForeignWorkspace first
// (the three direct opens that bypass the factories are listed in
// foreign_workspace.go), and by keeping every migrating configfile.Load off a
// workspace nothing has admitted. Both were enumerated by hand and found
// incomplete, so these censuses pin the enumeration: a reference from a
// top-level function or var not on the list fails here until it is
// classified. The census is per function, so a new call inside one already on
// the list (createCmd's RunE, rootCmd) is not caught.

// metadataFactories are the cmd/bd store factories that open a workspace from
// its own metadata. A foreign open reaches them only through openForeignStore.
var metadataFactories = map[string]bool{
	"newDoltStoreFromConfig":         true,
	"newExistingDoltStoreFromConfig": true,
	"newReadOnlyStoreFromConfig":     true,
	"newPreviewStoreFromConfig":      true,
}

// metadataFactoryReferences are the functions outside the factories' own files
// allowed to reference one, each with the workspace it opens.
var metadataFactoryReferences = map[string]string{
	"foreign_workspace.go:openForeignStore":            "the foreign opener: admits the workspace first (admitForeignWorkspace)",
	"main.go:var rootCmd":                              "own workspace: the root pre-run's identity preview of the workspace admission admitted",
	"direct_mode.go:ensureStoreActiveWithContext":      "own workspace: the discovered workspace, after admission",
	"store_reopen.go:openReadOnlyStoreForDBPath":       "own workspace: reopens the command's own database path",
	"init_identity.go:countConfiguredIssues":           "own workspace: the database the workspace bd init runs in is configured for",
	"init_contributor.go:autoConfigureForkContributor": "a planning .beads bd init just created, empty",
	"bootstrap.go:var openSyncedWarmupStore":           "own workspace: the workspace bd bootstrap just synced",
	"repo.go:var repoSyncCmd":                          "bd-managed remote cache clone, never a user .beads",
	"create.go:var createCmd":                          "bd-managed remote cache clone (the local target opens through openForeignStore)",
	"create.go:openDryRunTargetStore":                  "bd-managed remote cache clone (the local target opens through openForeignStore)",
}

// TestForeignOpensGoThroughOpenForeignStore pins that the metadata factories
// are referenced only from the classified call sites above.
func TestForeignOpensGoThroughOpenForeignStore(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	found := map[string]bool{}
	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			base := filepath.Base(path)
			if base == "store_factory.go" || base == "store_factory_nocgo.go" {
				continue
			}
			forEachTopLevelDecl(file, func(label string, node ast.Node) {
				ast.Inspect(node, func(n ast.Node) bool {
					if id, ok := n.(*ast.Ident); ok && metadataFactories[id.Name] {
						found[base+":"+label] = true
					}
					return true
				})
			})
		}
	}
	checkCensus(t, "metadata factory reference", found, metadataFactoryReferences)
}

// configfileLoadSites are the functions, in the packages the bd binary and the
// Go SDK are built from, that call the migrating configfile.Load, each
// classified. "own" sites load the workspace bd runs in, after admission
// admitted it; "admitted" sites load another workspace only after the
// discovery check on the line before refused what bd run inside it refuses
// (checkDiscoveredBackend, admitForeignWorkspace, RequireDiscoveredDoltBackend,
// LoadDiscoveredForOpen); "guarded" sites load only after a stat shows
// metadata.json exists, so nothing is migrated. A foreign read must not call
// it at all: it reads with configfile.LoadForDiscovery.
var configfileLoadSites = map[string]string{
	"backend_support.go:loadDoltBackendConfig":                         "own: bd dolt/backup commands on the selected workspace, after checkDiscoveredForOpen",
	"backup_auto.go:clientServerShareFilesystem":                       "own",
	"backup_proxied_server.go:syncProjectIDFromProxiedDB":              "own",
	"backup_restore.go:syncProjectIDFromDB":                            "own",
	"bootstrap.go:var bootstrapCmd":                                    "own: the workspace admission admitted",
	"bootstrap.go:cloneFromRemoteWithMode":                             "own: the workspace bd bootstrap or bd init clones into",
	"config_show.go:collectMetadataEntries":                            "own",
	"connect.go:var connectCmd":                                        "admitted: after admitForeignWorkspace",
	"dolt.go:var doltStatusCmd":                                        "own: after checkDiscoveredForOpen",
	"dolt.go:showDoltConfig":                                           "own: after checkDiscoveredForOpen",
	"doctor_health.go:runCheckHealth":                                  "own",
	"init.go:var initCmd":                                              "own: the workspace bd init creates or reinitializes",
	"init.go:applyInitGatewayCredential":                               "own: the workspace bd init creates",
	"init.go:seedInitWorkspaceIdentity":                                "own: the workspace bd init creates",
	"init_proxied_server.go:runInitProxiedServer":                      "own: the workspace bd init creates",
	"main.go:var rootCmd":                                              "own: the workspace admission admitted",
	"main.go:validateWorkspaceIdentity":                                "own: the workspace admission admitted",
	"migrate.go:loadOrCreateConfig":                                    "own",
	"migrate_dolt_mode.go:loadMigrateModeConfig":                       "own",
	"migrate_dolt_mode.go:runMigrateFromProxiedServer":                 "own",
	"migrate_dolt_mode.go:runMigrateToProxiedServer":                   "own",
	"serve.go:serveDatabaseSource":                                     "own: bd serve's workspace",
	"store_factory.go:openDoltStoreFromConfig":                         "admitted: after checkDiscoveredBackend (a foreign open, after admitForeignWorkspace too)",
	"store_factory_nocgo.go:newDoltStoreFromConfig":                    "admitted: after checkDiscoveredBackend (a foreign open, after admitForeignWorkspace too)",
	"uow_factory.go:resolveProxiedServerUOWTopology":                   "own: the command's or bd serve's workspace",
	"uow_factory.go:resolveServerModeUOWTopologyWithTransportResolver": "own: the command's or bd serve's workspace",
	"upgrade.go:var upgradeAckCmd":                                     "own",
	"version_tracking.go:autoMigrateOnVersionBump":                     "own: the workspace admission (or doctor's) admitted",
	"sdk/beads_cgo.go:OpenBestAvailableWith":                           "admitted: after checkDiscoveredBackend (library open)",
	"sdk/beads_nocgo.go:OpenBestAvailableWith":                         "admitted: after checkDiscoveredBackend (library open)",
}

// TestConfigfileLoadCallersAreClassified pins that every call of the
// migrating configfile.Load in the bd binary's command and SDK packages is a
// classified one. bd doctor's packages are not listed site by site: doctor
// loads the workspace it was pointed at after validateDoctorWorkspaceBackend
// admitted it, and reads its additional repos with LoadDiscoveredForOpen
// (multirepo.go); a doctor site loading anything else is reviewed there.
func TestConfigfileLoadCallersAreClassified(t *testing.T) {
	found := map[string]bool{}
	for _, dir := range []string{".", "../.."} {
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
				forEachTopLevelDecl(file, func(label string, node ast.Node) {
					ast.Inspect(node, func(n ast.Node) bool {
						sel, ok := n.(*ast.SelectorExpr)
						if !ok || sel.Sel.Name != "Load" {
							return true
						}
						if x, ok := sel.X.(*ast.Ident); ok && imports[x.Name] == "github.com/steveyegge/beads/internal/configfile" {
							found[prefix+base+":"+label] = true
						}
						return true
					})
				})
			}
		}
	}
	checkCensus(t, "configfile.Load call", found, configfileLoadSites)
}

// forEachTopLevelDecl calls fn for each function declaration and package-level
// var declaration in file, labeled as the journal census labels them
// ("Recv.Func", "var name").
func forEachTopLevelDecl(file *ast.File, fn func(label string, node ast.Node)) {
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			if d.Recv != nil && len(d.Recv.List) > 0 {
				if recv := journalscan.ReceiverTypeName(d.Recv.List[0].Type); recv != "" {
					name = recv + "." + name
				}
			}
			fn(name, d)
		case *ast.GenDecl:
			if d.Tok != token.VAR {
				continue
			}
			for _, spec := range d.Specs {
				if vs, ok := spec.(*ast.ValueSpec); ok && len(vs.Names) > 0 {
					fn("var "+vs.Names[0].Name, vs)
				}
			}
		}
	}
}

// checkCensus fails for a found site the allowlist does not classify, and for
// an allowlisted site that no longer exists, so the list cannot rot.
func checkCensus(t *testing.T, what string, found map[string]bool, allowed map[string]string) {
	t.Helper()
	if len(found) == 0 {
		t.Fatalf("found no %s sites; the scan is not running", what)
	}
	var unclassified, stale []string
	for site := range found {
		if _, ok := allowed[site]; !ok {
			unclassified = append(unclassified, site)
		}
	}
	for site := range allowed {
		if !found[site] {
			stale = append(stale, site)
		}
	}
	sort.Strings(unclassified)
	sort.Strings(stale)
	for _, site := range unclassified {
		t.Errorf("unclassified %s at %s: a path that opens or reads a workspace bd was not launched in must go through openForeignStore / read with configfile.LoadForDiscovery; classify it in this file once it does", what, site)
	}
	for _, site := range stale {
		t.Errorf("classified %s %s no longer exists; remove it from the list", what, site)
	}
}
