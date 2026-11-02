package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// doctorDeleteScan names the code bd doctor runs, keyed by directory
// (relative to cmd/bd) with the prefix its sites are reported under. In cmd/bd
// itself the scan covers the doctor command files (doctor*.go) and the
// package-level functions they reach.
var doctorDeleteScan = map[string]string{
	"doctor":     "doctor/",
	"doctor/fix": "doctor/fix/",
	".":          "",
}

// doctorDeleteFuncs are the deletions the guard looks for, by import path.
var doctorDeleteFuncs = map[string]map[string]bool{
	"os":                    {"Remove": true, "RemoveAll": true},
	"syscall":               {"Unlink": true, "Rmdir": true},
	"golang.org/x/sys/unix": {"Unlink": true, "Unlinkat": true, "Rmdir": true},
}

// doctorDeleteExemption is a function in bd doctor's code that deletes
// without fix.RemoveDisposable: the exact delete calls it makes, in source
// order, and why it may. Pinning the calls means a new delete added inside an
// exempted function fails the guard like one anywhere else.
type doctorDeleteExemption struct {
	calls  []string
	reason string
}

// doctorDeleteExemptions are the deletions in bd doctor's code that do not go
// through fix.RemoveDisposable. Everything else that deletes must call
// fix.RemoveDisposable, which refuses database state whatever its caller
// planned.
var doctorDeleteExemptions = map[string]doctorDeleteExemption{
	"doctor/fix/artifact_plan.go:RemoveDisposable": {
		calls:  []string{"os.RemoveAll", "os.Remove"},
		reason: "the deletion primitive itself",
	},
	"doctor/installation.go:probeDirectoryWrite": {
		calls:  []string{"os.Remove"},
		reason: "removes only the unique probe file this call created with os.CreateTemp",
	},
	"doctor/perf.go:startCPUProfile": {
		calls:  []string{"os.Remove"},
		reason: "removes only the unique profile file this call created with os.CreateTemp when profiling cannot start",
	},
	"doctor/fix/locks.go:StaleLockFiles": {
		calls:  []string{"os.Remove", "os.Remove", "os.Remove"},
		reason: "bd's own lock files in the admitted workspace, by fixed name and age, never data; the Lock Files check reports the same names and thresholds",
	},
	"doctor/fix/repo_fingerprint.go:RepoFingerprint": {
		calls:  []string{"os.RemoveAll", "os.Remove", "os.Remove"},
		reason: "the interactive Reinitialize database choice, confirmed a second time with the path named; --yes never takes it; the last call is the sidecar loop, which removes only non-symlink -wal/-shm files beside the database it just deleted — RemoveDisposable itself would refuse them as database state",
	},
	"doctor/fix/database_integrity.go:doltIntegrityRecovery": {
		calls:  []string{"os.RemoveAll"},
		reason: "removes only what its own bd init --force created after renaming the original database aside, then restores the original",
	},
	"doctor/fix/fs.go:var removeFile": {
		calls:  []string{"os.Remove"},
		reason: "test seam for moveFile, which has no production caller",
	},
	"init_stealth.go:removeBeadsProjectGitignoreSection": {
		calls:  []string{"os.Remove"},
		reason: "the Project Gitignore fix in a stealth repo removes <repo>/.gitignore only when it is empty once bd's own managed section is gone",
	},
	"migrate_hooks_apply.go:retireHookSidecar": {
		calls:  []string{"os.Remove"},
		reason: "the Pending Migrations fix removes a hook sidecar only after an identical copy exists at its destination",
	},
}

// doctorRemoveDisposableCallers are the functions in bd doctor's code that
// delete through fix.RemoveDisposable, and how each establishes that what it
// removes is its own. RemoveDisposable refuses database state, but not a
// nested workspace's metadata.json or config.yaml; ownership is the caller's
// half of the invariant, so a new caller must name how it establishes it.
var doctorRemoveDisposableCallers = map[string]string{
	"doctor/fix/artifacts.go:ClassicArtifacts": "removes only the paths fix.PlanArtifacts marked SafeDelete, the plan the Classic Artifacts scan reports",
	"doctor/maintenance.go:FixStaleMQFiles":    "removes only the mq/*.json files legacyMQFiles counts for the Legacy MQ Files check, and mq/ once it is empty",
}

// TestDoctorRemoveDisposableCallersEstablishOwnership pins who deletes
// through fix.RemoveDisposable: every function that references it must be in
// doctorRemoveDisposableCallers with the way it establishes ownership.
func TestDoctorRemoveDisposableCallersEstablishOwnership(t *testing.T) {
	_, callers := scanDoctorDeleteSites(t)
	for _, key := range sortedDeleteKeys(callers) {
		reason, ok := doctorRemoveDisposableCallers[key]
		if !ok {
			t.Errorf("%s deletes through fix.RemoveDisposable, which refuses database state but not someone else's files: add it to doctorRemoveDisposableCallers with how it establishes that what it removes is its own", key)
			continue
		}
		if strings.TrimSpace(reason) == "" {
			t.Errorf("%s has an empty ownership reason", key)
		}
	}
	for key := range doctorRemoveDisposableCallers {
		if _, ok := callers[key]; !ok {
			t.Errorf("%q no longer calls fix.RemoveDisposable; remove it from doctorRemoveDisposableCallers", key)
		}
	}
}

// TestDoctorDeletesGoThroughRemoveDisposable holds every file deletion in bd
// doctor's code to one gate: a reference to os.Remove, os.RemoveAll or an
// unlink outside fix.RemoveDisposable fails unless doctorDeleteExemptions
// names the function and pins its exact delete calls, with a reason. A new
// cleanup that bypasses the gate, and with it the refusal to delete database
// state, is caught here, including a new delete inside an exempted function.
//
// It scans the doctor and doctor/fix packages whole, and in cmd/bd the
// doctor*.go command files plus every package-level function reachable,
// through calls or function references within cmd/bd, from their functions
// and their package-level declarations (doctorCmd's RunE included); a name
// reaches every function declared with it (cgo and nocgo variants alike).
// Honest limits: it does not follow method calls or package-level variables
// those functions reference, and it does not see deletes inside helpers in
// other packages (internal/doltserver, internal/storage/dolt and the like);
// those are inventoried, with the reason each is outside this class, in the
// change that added this guard.
func TestDoctorDeletesGoThroughRemoveDisposable(t *testing.T) {
	sites, _ := scanDoctorDeleteSites(t)
	if len(sites) == 0 {
		t.Fatal("found no deletion sites at all; the scan is not looking at bd doctor's code")
	}
	seen := map[string]bool{}
	for _, key := range sortedDeleteKeys(sites) {
		exemption, ok := doctorDeleteExemptions[key]
		if !ok {
			t.Errorf("%s deletes with %s outside fix.RemoveDisposable: route it through RemoveDisposable, which refuses database state, or add it to doctorDeleteExemptions with a reason",
				key, strings.Join(sites[key], ", "))
			continue
		}
		seen[key] = true
		if strings.TrimSpace(exemption.reason) == "" {
			t.Errorf("%s has an empty exemption reason", key)
		}
		if !reflect.DeepEqual(sites[key], exemption.calls) {
			t.Errorf("%s now deletes with %s, but its exemption covers only %s: route the new delete through fix.RemoveDisposable, or update the exemption and its reason",
				key, strings.Join(sites[key], ", "), strings.Join(exemption.calls, ", "))
		}
	}
	for key := range doctorDeleteExemptions {
		if !seen[key] {
			t.Errorf("exemption %q no longer matches a deletion site; remove it", key)
		}
	}
}

// scanDoctorDeleteSites returns the direct delete calls in bd doctor's code,
// by function key, and the functions that reference fix.RemoveDisposable.
func scanDoctorDeleteSites(t *testing.T) (map[string][]string, map[string][]string) {
	t.Helper()
	out := map[string][]string{}
	callers := map[string][]string{}
	for dir, prefix := range doctorDeleteScan {
		fset := token.NewFileSet()
		pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
			return !strings.HasSuffix(fi.Name(), "_test.go")
		}, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", dir, err)
		}
		for _, pkg := range pkgs {
			type fileDecl struct {
				base    string
				imports map[string]string
				decl    ast.Decl
			}
			var decls []fileDecl
			// funcs maps each name to every package-level function declared
			// with it: init, and the cgo/nocgo variants of one function,
			// share a name, and each body is explored.
			funcs := map[string][]*ast.FuncDecl{}
			var seeds []*ast.FuncDecl
			var seedDecls []ast.Decl
			for path, file := range pkg.Files {
				imports := map[string]string{}
				for _, spec := range file.Imports {
					importPath, err := strconv.Unquote(spec.Path.Value)
					if err != nil {
						t.Fatal(err)
					}
					name := importPath[strings.LastIndex(importPath, "/")+1:]
					if spec.Name != nil {
						name = spec.Name.Name
					}
					imports[name] = importPath
				}
				base := filepath.Base(path)
				for _, decl := range file.Decls {
					decls = append(decls, fileDecl{base: base, imports: imports, decl: decl})
					fd, isFunc := decl.(*ast.FuncDecl)
					if isFunc && fd.Recv == nil {
						funcs[fd.Name.Name] = append(funcs[fd.Name.Name], fd)
					}
					if !strings.HasPrefix(base, "doctor") {
						continue
					}
					if isFunc {
						seeds = append(seeds, fd)
					} else {
						// Package-level declarations, such as doctorCmd and
						// the RunE func literal in it.
						seedDecls = append(seedDecls, decl)
					}
				}
			}
			// In cmd/bd, scan the doctor*.go files and what they reach.
			inScope := func(base string, decl ast.Decl) bool { return true }
			if dir == "." {
				reached := reachableFuncs(funcs, seeds, seedDecls)
				inScope = func(base string, decl ast.Decl) bool {
					if strings.HasPrefix(base, "doctor") {
						return true
					}
					fd, ok := decl.(*ast.FuncDecl)
					return ok && reached[fd]
				}
			}
			for _, fd := range decls {
				if !inScope(fd.base, fd.decl) {
					continue
				}
				record := func(key string, node ast.Node) {
					ast.Inspect(node, func(n ast.Node) bool {
						// A bare RemoveDisposable inside package fix, other
						// than its own declaration, or fix.RemoveDisposable.
						if id, ok := n.(*ast.Ident); ok && dir == "doctor/fix" && id.Name == "RemoveDisposable" && key != prefix+"artifact_plan.go:RemoveDisposable" {
							callers[key] = append(callers[key], id.Name)
						}
						if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "RemoveDisposable" {
							if x, ok := sel.X.(*ast.Ident); ok && fd.imports[x.Name] == "github.com/steveyegge/beads/cmd/bd/doctor/fix" {
								callers[key] = append(callers[key], x.Name+"."+sel.Sel.Name)
							}
						}
						sel, ok := n.(*ast.SelectorExpr)
						if !ok {
							return true
						}
						ident, ok := sel.X.(*ast.Ident)
						if !ok {
							return true
						}
						if doctorDeleteFuncs[fd.imports[ident.Name]][sel.Sel.Name] {
							out[key] = append(out[key], ident.Name+"."+sel.Sel.Name)
						}
						return true
					})
				}
				switch d := fd.decl.(type) {
				case *ast.FuncDecl:
					record(prefix+fd.base+":"+d.Name.Name, d)
				case *ast.GenDecl:
					for _, spec := range d.Specs {
						if vs, ok := spec.(*ast.ValueSpec); ok && len(vs.Names) > 0 {
							record(prefix+fd.base+":var "+vs.Names[0].Name, vs)
						}
					}
				}
			}
		}
	}
	return out, callers
}

// reachableFuncs returns the package-level functions reachable from the
// seed functions and the seed declarations (package-level vars such as
// doctorCmd, whose RunE is a func literal) through calls or references by
// name within the same package. A name reaches every function declared with
// it.
func reachableFuncs(funcs map[string][]*ast.FuncDecl, seeds []*ast.FuncDecl, seedDecls []ast.Decl) map[*ast.FuncDecl]bool {
	reached := map[*ast.FuncDecl]bool{}
	var queue []*ast.FuncDecl
	enqueue := func(fd *ast.FuncDecl) {
		if !reached[fd] {
			reached[fd] = true
			queue = append(queue, fd)
		}
	}
	visit := func(node ast.Node) {
		ast.Inspect(node, func(n ast.Node) bool {
			// init cannot be referenced; an identifier named init is a
			// field or a local, not one of the package's init functions.
			if id, ok := n.(*ast.Ident); ok && id.Name != "init" {
				for _, fd := range funcs[id.Name] {
					enqueue(fd)
				}
			}
			return true
		})
	}
	for _, fd := range seeds {
		enqueue(fd)
	}
	for _, decl := range seedDecls {
		visit(decl)
	}
	for len(queue) > 0 {
		fd := queue[0]
		queue = queue[1:]
		if fd.Body != nil {
			visit(fd.Body)
		}
	}
	return reached
}

func sortedDeleteKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestDoctorDeleteGuardReachesRunEAndEveryVariant pins the guard's own
// reachability: a function called only from a command's RunE func literal is
// reached, every function sharing a name (cgo and nocgo variants) is reached,
// and an unrelated init is not.
func TestDoctorDeleteGuardReachesRunEAndEveryVariant(t *testing.T) {
	sources := map[string]string{
		"doctor_x.go":    "package main\nvar xCmd = &cmd{RunE: func() error { return guarded() }}\nfunc init() { register(xCmd) }\nfunc register(any) {}\ntype cmd struct{ RunE func() error }\n",
		"guard_cgo.go":   "package main\nfunc guarded() error { return helper() }\n",
		"guard_nocgo.go": "package main\nfunc guarded() error { return nil }\nfunc helper() error { return nil }\n",
		"other.go":       "package main\nfunc init() { unrelated() }\nfunc unrelated() {}\n",
	}
	fset := token.NewFileSet()
	funcs := map[string][]*ast.FuncDecl{}
	var seeds []*ast.FuncDecl
	var seedDecls []ast.Decl
	for name, src := range sources {
		file, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fd, isFunc := decl.(*ast.FuncDecl)
			if isFunc {
				funcs[fd.Name.Name] = append(funcs[fd.Name.Name], fd)
			}
			if strings.HasPrefix(name, "doctor") {
				if isFunc {
					seeds = append(seeds, fd)
				} else {
					seedDecls = append(seedDecls, decl)
				}
			}
		}
	}
	reached := reachableFuncs(funcs, seeds, seedDecls)
	count := map[string]int{}
	for fd := range reached {
		count[fd.Name.Name]++
	}
	want := map[string]int{"init": 1, "register": 1, "guarded": 2, "helper": 1}
	if !reflect.DeepEqual(count, want) {
		t.Fatalf("reached functions (name: declarations) = %v, want %v", count, want)
	}
}
