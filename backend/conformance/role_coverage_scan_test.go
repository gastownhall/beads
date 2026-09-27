package conformance

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/issueops"
	"github.com/steveyegge/beads/journalops"
	"github.com/steveyegge/beads/memoryops"
)

// roleMethod names one method of one role interface on the public facade.
type roleMethod struct {
	// Role is the qualified interface name, e.g. "issueops.Reader".
	Role string
	// Method is the method name, e.g. "Ready".
	Method string
}

// String renders the fully qualified name the gate reports and waives by.
func (rm roleMethod) String() string { return rm.Role + "." + rm.Method }

// modulePath is this module's import path, used to turn an in-module import
// into the directory that declares it.
const modulePath = "github.com/steveyegge/beads"

// facadePackages maps each facade package's import path to the qualifier this
// gate names its interfaces by. The paths come from real types rather than
// string literals, so moving a package breaks the build here instead of
// quietly emptying the census.
//
// journalops is the third, and it is the entry that proves this census had to
// be a SOURCE parse. Its one role is handed out by no accessor at all — a
// backend publishes the journal by implementing an interface a caller
// type-asserts for — so reflectRoleAccessors below can never see it, exactly as
// it can never see issueops.Importer. A package listed here is censused from
// its declarations, which is what puts a role with no accessor under the
// exhaustiveness gate rather than outside it.
var facadePackages = map[string]string{
	reflect.TypeOf((*issueops.Reader)(nil)).Elem().PkgPath():    "issueops",
	reflect.TypeOf((*journalops.Journal)(nil)).Elem().PkgPath(): "journalops",
	reflect.TypeOf((*memoryops.Memories)(nil)).Elem().PkgPath(): "memoryops",
}

var errorType = reflect.TypeOf((*error)(nil)).Elem()

// repoRoot locates the module root from this file's own path, so the gate
// finds the facade packages without depending on the working directory.
func repoRoot() (string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", ".."), nil
}

// parseFacadeInterfaces reads one facade package's non-test sources and reports
// every exported interface it declares, keyed "qualifier.Interface", with its
// method names.
//
// Source is the census's authority rather than reflection because reflection
// cannot enumerate a package's types: it can only answer questions about types
// something already names, and naming them is the hand-written list this gate
// exists to abolish. An interface the package declares but nothing else in the
// module mentions — issueops.Importer is one today — is invisible to any
// reflection-only census and visible here.
//
// Anything it cannot classify is REFUSED rather than skipped, because a skip is
// a silently smaller census, which is the failure mode the gate is built to
// end. That covers an embedded interface, whose method set this parse cannot
// resolve, and an exported alias that turns out to name an interface: a
// `type Reader = roles.Reader` compat shim would otherwise delete the role from
// the census while reading as an ordinary alias. Aliases to non-interfaces are
// ordinary and pass; the fourteen in issueops today all name structs and
// strings.
func parseFacadeInterfaces(root, dir, qualifier string) (map[string][]string, error) {
	pkgs, err := parsePackageSources(dir, func(name string) bool {
		return !strings.HasSuffix(name, "_test.go")
	})
	if err != nil {
		return nil, err
	}
	roles := map[string][]string{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			imports := importPaths(file)
			for _, decl := range file.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.TYPE {
					continue
				}
				for _, spec := range gd.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || !ast.IsExported(ts.Name.Name) {
						continue
					}
					name := qualifier + "." + ts.Name.Name
					if ts.Assign.IsValid() {
						if err := refuseInterfaceAlias(root, name, ts.Type, imports, pkg); err != nil {
							return nil, err
						}
						continue
					}
					it, ok := ts.Type.(*ast.InterfaceType)
					if !ok {
						continue
					}
					methods := []string{}
					for _, field := range it.Methods.List {
						if len(field.Names) == 0 {
							return nil, fmt.Errorf("%s embeds an interface; this census cannot resolve embedded method sets", name)
						}
						for _, m := range field.Names {
							methods = append(methods, m.Name)
						}
					}
					sort.Strings(methods)
					roles[name] = methods
				}
			}
		}
	}
	return roles, nil
}

// refuseInterfaceAlias fails when an exported alias names an interface, or when
// this parse cannot tell what it names.
func refuseInterfaceAlias(root, name string, target ast.Expr, imports map[string]string, local *ast.Package) error {
	kind, err := classifyAliasTarget(root, target, imports, local)
	if err != nil {
		return fmt.Errorf("%s is an alias the census cannot classify: %w", name, err)
	}
	if kind == aliasNamesInterface {
		return fmt.Errorf("%s aliases an interface; the census would lose the role while the alias reads as ordinary", name)
	}
	return nil
}

type aliasKind int

const (
	aliasNamesInterface aliasKind = iota
	aliasNamesSomethingElse
)

// classifyAliasTarget reports whether an alias target is an interface, parsing
// the declaring package when the target lives in another one.
func classifyAliasTarget(root string, target ast.Expr, imports map[string]string, local *ast.Package) (aliasKind, error) {
	switch e := target.(type) {
	case *ast.IndexExpr:
		return classifyAliasTarget(root, e.X, imports, local)
	case *ast.IndexListExpr:
		return classifyAliasTarget(root, e.X, imports, local)
	case *ast.InterfaceType:
		return aliasNamesInterface, nil
	case *ast.Ident:
		return classifyDeclaredType(local, e.Name)
	case *ast.SelectorExpr:
		pkg, ok := e.X.(*ast.Ident)
		if !ok {
			return 0, fmt.Errorf("its target is not a package-qualified name")
		}
		path := imports[pkg.Name]
		if path != modulePath && !strings.HasPrefix(path, modulePath+"/") {
			return 0, fmt.Errorf("its target %s.%s is outside this module", pkg.Name, e.Sel.Name)
		}
		dir := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(strings.TrimPrefix(path, modulePath), "/")))
		pkgs, err := parsePackageSources(dir, func(name string) bool {
			return !strings.HasSuffix(name, "_test.go")
		})
		if err != nil {
			return 0, err
		}
		for _, declaring := range pkgs {
			if kind, err := classifyDeclaredType(declaring, e.Sel.Name); err == nil {
				return kind, nil
			}
		}
		return 0, fmt.Errorf("its target %s.%s is declared nowhere in %s", pkg.Name, e.Sel.Name, dir)
	default:
		// A structural target — a map, slice, channel or func type — is not a
		// named type and cannot be an interface.
		return aliasNamesSomethingElse, nil
	}
}

// classifyDeclaredType reports whether a package declares name as an interface.
func classifyDeclaredType(pkg *ast.Package, name string) (aliasKind, error) {
	for _, file := range pkg.Files {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name.Name != name {
					continue
				}
				if _, ok := ts.Type.(*ast.InterfaceType); ok {
					return aliasNamesInterface, nil
				}
				return aliasNamesSomethingElse, nil
			}
		}
	}
	return 0, fmt.Errorf("package %s declares no type named %s", pkg.Name, name)
}

// roleFacade is the whole public role surface: every exported interface the
// facade packages declare, with its method set.
func roleFacade() (map[string][]string, error) {
	root, err := repoRoot()
	if err != nil {
		return nil, err
	}
	facade := map[string][]string{}
	for path, qualifier := range facadePackages {
		dir := filepath.Join(root, filepath.Base(path))
		roles, err := parseFacadeInterfaces(root, dir, qualifier)
		if err != nil {
			return nil, err
		}
		for name, methods := range roles {
			facade[name] = methods
		}
	}
	return facade, nil
}

// reflectRoleAccessors reports the facade roles a backend hands out through a
// storage accessor — a method taking nothing and returning (role, error) — with
// method sets taken from the compiler rather than from source. It is the
// independent second opinion the source census is checked against; it cannot
// stand alone, because a role with no accessor never appears in it.
//
// An accessor whose interface belongs to no known facade package is an ERROR,
// not a skip. That is the shape a third facade package would arrive in, and a
// skip would drop every role in it while every other check stayed green.
func reflectRoleAccessors() (map[string][]string, error) {
	surface := reflect.TypeOf((*storage.DoltStorage)(nil)).Elem()
	roles := map[string][]string{}
	for i := range surface.NumMethod() {
		accessor := surface.Method(i)
		signature := accessor.Type
		if signature.NumIn() != 0 || signature.NumOut() != 2 || signature.Out(1) != errorType {
			continue
		}
		role := signature.Out(0)
		if role.Kind() != reflect.Interface {
			continue
		}
		qualifier, ok := facadePackages[role.PkgPath()]
		if !ok {
			return nil, fmt.Errorf("accessor %s hands out %s.%s, which belongs to no known facade package: "+
				"add that package to facadePackages, or this census silently omits every role in it",
				accessor.Name, role.PkgPath(), role.Name())
		}
		methods := make([]string, 0, role.NumMethod())
		for j := range role.NumMethod() {
			methods = append(methods, role.Method(j).Name)
		}
		sort.Strings(methods)
		roles[qualifier+"."+role.Name()] = methods
	}
	return roles, nil
}

// funcFacts is what one function declaration contributes to the scan.
type funcFacts struct {
	// calls are the role methods the function invokes.
	calls map[roleMethod]bool
	// callees are the package functions and methods it calls, by func key.
	callees []string
	// root reports whether it is an exported Run entrypoint.
	root bool
}

// scanRoleCalls reports which role methods the conformance sources at dir
// actually call, mapped to the functions that call them.
//
// It resolves a call's receiver rather than matching method names, so a role
// method named Close is told apart from every other Close in the package. A
// receiver resolves when it is a role-typed parameter, local variable or
// assignment, or a field of a struct declared in the package whose own type is
// a role interface — which is the shape every contract fixture uses.
//
// ONLY exported Run entrypoints are reachability roots. Everything else, a
// method as much as a function, earns its reachability from the call that names
// it: a method resolves through its receiver's type exactly as a role call
// does. Rooting methods outright — which an earlier draft did, by an operator
// precedence slip — let an uncalled probe method vouch for a role method
// nothing ran.
func scanRoleCalls(dir string) (map[roleMethod][]string, error) {
	pkgs, err := parsePackageSources(dir, func(name string) bool {
		return !strings.HasSuffix(name, "_test.go")
	})
	if err != nil {
		return nil, err
	}

	covered := map[roleMethod]map[string]bool{}
	for _, pkg := range pkgs {
		fields := roleTypedFields(pkg)
		results := funcResultTypes(pkg)
		facts := map[string]*funcFacts{}
		for _, file := range pkg.Files {
			imports := importPaths(file)
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				facts[funcKey(fn)] = analyzeFunc(fn, imports, fields, results)
			}
		}
		for name := range reachableFuncs(facts) {
			for target := range facts[name].calls {
				if covered[target] == nil {
					covered[target] = map[string]bool{}
				}
				covered[target][name] = true
			}
		}
	}

	out := map[roleMethod][]string{}
	for target, callers := range covered {
		names := make([]string, 0, len(callers))
		for name := range callers {
			names = append(names, name)
		}
		sort.Strings(names)
		out[target] = names
	}
	return out, nil
}

// reachableFuncs reports the declarations reachable from an exported Run
// entrypoint.
func reachableFuncs(facts map[string]*funcFacts) map[string]bool {
	var queue []string
	for name, fact := range facts {
		if fact.root {
			queue = append(queue, name)
		}
	}
	reachable := map[string]bool{}
	for len(queue) > 0 {
		name := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if reachable[name] || facts[name] == nil {
			continue
		}
		reachable[name] = true
		queue = append(queue, facts[name].callees...)
	}
	return reachable
}

// roleTypedFields maps each struct type in the package to its role-typed
// fields, so `fixture.Closer.CloseBatch(...)` resolves to the role the fixture
// declares rather than to whatever another fixture calls its Closer.
func roleTypedFields(pkg *ast.Package) map[string]map[string]string {
	fields := map[string]map[string]string{}
	for _, file := range pkg.Files {
		imports := importPaths(file)
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					continue
				}
				for _, field := range st.Fields.List {
					role := roleTypeName(field.Type, imports)
					if role == "" {
						continue
					}
					if fields[ts.Name.Name] == nil {
						fields[ts.Name.Name] = map[string]string{}
					}
					for _, name := range field.Names {
						fields[ts.Name.Name][name.Name] = role
					}
				}
			}
		}
	}
	return fields
}

// funcResultTypes maps each package function returning a single package-local
// named type to that type, so `probe := newProbe(...)` gives probe a type and
// the methods called on it resolve to their declarations.
func funcResultTypes(pkg *ast.Package) map[string]string {
	results := map[string]string{}
	for _, file := range pkg.Files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
				continue
			}
			if len(fn.Type.Results.List[0].Names) > 1 {
				continue
			}
			if named := localTypeName(fn.Type.Results.List[0].Type); named != "" {
				results[fn.Name.Name] = named
			}
		}
	}
	return results
}

// analyzeFunc reports the role methods one declaration calls and the package
// functions and methods it calls.
func analyzeFunc(fn *ast.FuncDecl, imports map[string]string, fields map[string]map[string]string, results map[string]string) *funcFacts {
	roleVars := map[string]string{}
	localVars := map[string]string{}
	bind := func(names []*ast.Ident, typ ast.Expr) {
		if role := roleTypeName(typ, imports); role != "" {
			for _, name := range names {
				roleVars[name.Name] = role
			}
			return
		}
		if named := localTypeName(typ); named != "" {
			for _, name := range names {
				localVars[name.Name] = named
			}
		}
	}
	bindParams := func(list *ast.FieldList) {
		if list == nil {
			return
		}
		for _, param := range list.List {
			bind(param.Names, param.Type)
		}
	}
	bindParams(fn.Recv)
	bindParams(fn.Type.Params)

	roleOf := func(expr ast.Expr) string {
		switch e := expr.(type) {
		case *ast.Ident:
			return roleVars[e.Name]
		case *ast.SelectorExpr:
			if base, ok := e.X.(*ast.Ident); ok {
				return fields[localVars[base.Name]][e.Sel.Name]
			}
		}
		return ""
	}

	// Bindings first, so a call site reached before its variable's declaration
	// in traversal order still resolves.
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncLit:
			bindParams(node.Type.Params)
		case *ast.ValueSpec:
			bind(node.Names, node.Type)
		case *ast.AssignStmt:
			if len(node.Lhs) != 1 || len(node.Rhs) != 1 {
				return true
			}
			target, ok := node.Lhs[0].(*ast.Ident)
			if !ok {
				return true
			}
			if role := roleOf(node.Rhs[0]); role != "" {
				roleVars[target.Name] = role
				return true
			}
			if named := constructedTypeName(node.Rhs[0], results); named != "" {
				localVars[target.Name] = named
			}
		}
		return true
	})

	facts := &funcFacts{
		calls: map[roleMethod]bool{},
		root:  fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Run"),
	}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch callee := call.Fun.(type) {
		case *ast.Ident:
			facts.callees = append(facts.callees, callee.Name)
		case *ast.SelectorExpr:
			if role := roleOf(callee.X); role != "" {
				facts.calls[roleMethod{Role: role, Method: callee.Sel.Name}] = true
				return true
			}
			if base, ok := callee.X.(*ast.Ident); ok && localVars[base.Name] != "" {
				facts.callees = append(facts.callees, localVars[base.Name]+"."+callee.Sel.Name)
			}
		}
		return true
	})
	return facts
}

// constructedTypeName reports the package-local type an expression yields, for
// a composite literal or a call to a package function that returns one.
func constructedTypeName(expr ast.Expr, results map[string]string) string {
	switch e := expr.(type) {
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			return constructedTypeName(e.X, results)
		}
	case *ast.CompositeLit:
		return localTypeName(e.Type)
	case *ast.CallExpr:
		if callee, ok := e.Fun.(*ast.Ident); ok {
			return results[callee.Name]
		}
	}
	return ""
}

// funcKey names a declaration for the call graph: bare for a function, and
// receiver-qualified for a method.
func funcKey(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	return localTypeName(fn.Recv.List[0].Type) + "." + fn.Name.Name
}

// parsePackageSources parses one directory's packages, keeping the files accept
// admits.
func parsePackageSources(dir string, accept func(name string) bool) (map[string]*ast.Package, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
		return accept(fi.Name())
	}, 0)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", dir, err)
	}
	return pkgs, nil
}

// importPaths maps each file-local package name to the path it imports.
func importPaths(file *ast.File) map[string]string {
	paths := map[string]string{}
	for _, spec := range file.Imports {
		path := strings.Trim(spec.Path.Value, `"`)
		name := path[strings.LastIndexByte(path, '/')+1:]
		if spec.Name != nil {
			name = spec.Name.Name
		}
		paths[name] = path
	}
	return paths
}

// roleTypeName reports the qualified role name a type expression denotes, or
// "" when it names something outside the facade packages.
func roleTypeName(typ ast.Expr, imports map[string]string) string {
	selector, ok := typ.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	pkg, ok := selector.X.(*ast.Ident)
	if !ok {
		return ""
	}
	qualifier, ok := facadePackages[imports[pkg.Name]]
	if !ok {
		return ""
	}
	return qualifier + "." + selector.Sel.Name
}

// localTypeName reports the package-local type name a type expression denotes,
// dropping one level of pointer.
func localTypeName(typ ast.Expr) string {
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}
	if ident, ok := typ.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

// The scanner's own cases. Each writes a miniature conformance package to a
// temporary directory and asserts what the scan makes of it.

func TestScanRoleCallsResolvesFixtureFieldsHelpersAndAliases(t *testing.T) {
	dir := writeFakeContractPackage(t, `package fake

import (
	"context"

	publicops "github.com/steveyegge/beads/issueops"
)

type ReaderFixture struct {
	Reader publicops.Reader
}

type ClaimerFixture struct {
	// Two fixtures naming one field differently typed: resolving by field
	// name alone would credit the wrong role.
	Claimer publicops.ReadyClaimer
}

func RunReaderGetsAnIssue(ctx context.Context, fixture ReaderFixture) {
	fixture.Reader.Get(ctx, "id")
	readerList(ctx, fixture)
}

func RunReaderReadyThroughAnAlias(ctx context.Context, fixture ReaderFixture) {
	reader := fixture.Reader
	reader.Ready(ctx)
}

func RunClaimerClaimsTheFront(ctx context.Context, fixture ClaimerFixture) {
	fixture.Claimer.ClaimNext(ctx)
}

func readerList(ctx context.Context, fixture ReaderFixture) {
	fixture.Reader.List(ctx)
}
`)
	covered, err := scanRoleCalls(dir)
	if err != nil {
		t.Fatalf("scanning the fabricated package: %v", err)
	}
	for _, want := range []roleMethod{
		{Role: "issueops.Reader", Method: "Get"},
		{Role: "issueops.Reader", Method: "Ready"},
		{Role: "issueops.Reader", Method: "List"},
		{Role: "issueops.ReadyClaimer", Method: "ClaimNext"},
	} {
		if len(covered[want]) == 0 {
			t.Errorf("%s was not seen as covered; scan found %v", want, covered)
		}
	}
	if callers := covered[roleMethod{Role: "issueops.Claimer", Method: "ClaimNext"}]; len(callers) != 0 {
		t.Errorf("ClaimNext was credited to issueops.Claimer (%v); the field is typed ReadyClaimer", callers)
	}
	if callers := covered[roleMethod{Role: "issueops.Reader", Method: "List"}]; len(callers) != 1 || callers[0] != "readerList" {
		t.Errorf("List was credited to %v, want the helper that calls it", callers)
	}
}

func TestScanRoleCallsIgnoresAHelperNoEntrypointRuns(t *testing.T) {
	dir := writeFakeContractPackage(t, `package fake

import (
	"context"

	publicops "github.com/steveyegge/beads/issueops"
)

type ReaderFixture struct {
	Reader publicops.Reader
}

func RunReaderGetsAnIssue(ctx context.Context, fixture ReaderFixture) {
	fixture.Reader.Get(ctx, "id")
}

func orphanedHelper(ctx context.Context, fixture ReaderFixture) {
	fixture.Reader.Ready(ctx)
}
`)
	covered, err := scanRoleCalls(dir)
	if err != nil {
		t.Fatalf("scanning the fabricated package: %v", err)
	}
	if callers := covered[roleMethod{Role: "issueops.Reader", Method: "Ready"}]; len(callers) != 0 {
		t.Errorf("Ready was credited to %v, but only an unreachable helper calls it", callers)
	}
	if len(covered[roleMethod{Role: "issueops.Reader", Method: "Get"}]) == 0 {
		t.Error("Get was not credited to the entrypoint that calls it")
	}
}

// TestScanRoleCallsIgnoresAMethodNoEntrypointRuns is the same rule for methods,
// and it is the one an earlier draft got wrong: it rooted every method
// declaration, so an uncalled probe method silently vouched for a role method
// nothing ran. A method earns its reachability from the call that names it, or
// it earns nothing.
func TestScanRoleCallsIgnoresAMethodNoEntrypointRuns(t *testing.T) {
	dir := writeFakeContractPackage(t, `package fake

import (
	"context"

	publicops "github.com/steveyegge/beads/issueops"
)

type ReaderFixture struct {
	Reader publicops.Reader
}

type probe struct {
	Reader publicops.Reader
}

func newProbe(fixture ReaderFixture) *probe {
	return &probe{Reader: fixture.Reader}
}

func (p *probe) readList(ctx context.Context) {
	p.Reader.List(ctx)
}

func (p *probe) readReady(ctx context.Context) {
	p.Reader.Ready(ctx)
}

func RunReaderListsThroughAProbe(ctx context.Context, fixture ReaderFixture) {
	p := newProbe(fixture)
	p.readList(ctx)
}
`)
	covered, err := scanRoleCalls(dir)
	if err != nil {
		t.Fatalf("scanning the fabricated package: %v", err)
	}
	if callers := covered[roleMethod{Role: "issueops.Reader", Method: "List"}]; len(callers) != 1 || callers[0] != "probe.readList" {
		t.Errorf("List was credited to %v, want the probe method the entrypoint calls", callers)
	}
	if callers := covered[roleMethod{Role: "issueops.Reader", Method: "Ready"}]; len(callers) != 0 {
		t.Errorf("Ready was credited to %v, but only a method no entrypoint calls reads it", callers)
	}
}

func TestScanRoleCallsTellsRoleMethodsApartFromLookalikes(t *testing.T) {
	dir := writeFakeContractPackage(t, `package fake

import (
	"context"

	publicops "github.com/steveyegge/beads/issueops"
)

type LifecycleFixture struct {
	Lifecycle publicops.Lifecycle
	Rows      *rows
}

type rows struct{}

func (r *rows) Close() {}

func RunLifecycleCreates(ctx context.Context, fixture LifecycleFixture) {
	fixture.Lifecycle.Create(ctx, publicops.CreateRequest{})
	fixture.Rows.Close()
}
`)
	covered, err := scanRoleCalls(dir)
	if err != nil {
		t.Fatalf("scanning the fabricated package: %v", err)
	}
	if len(covered[roleMethod{Role: "issueops.Lifecycle", Method: "Create"}]) == 0 {
		t.Error("Create was not credited to the entrypoint that calls it")
	}
	if callers := covered[roleMethod{Role: "issueops.Lifecycle", Method: "Close"}]; len(callers) != 0 {
		t.Errorf("Lifecycle.Close was credited to %v, but only *rows.Close is called", callers)
	}
}

// The census parser's own cases: everything it cannot classify has to be an
// error, because the alternative is a smaller census that still reads green.

func TestParseFacadeInterfacesRefusesAnEmbeddedInterface(t *testing.T) {
	dir := writeFakeContractPackage(t, `package fake

type Reader interface {
	Get()
}

type Wider interface {
	Reader
	List()
}
`)
	if _, err := parseFacadeInterfaces(dir, dir, "fake"); err == nil {
		t.Error("parsing an embedded interface succeeded; the census would silently count zero methods for it")
	}
}

func TestParseFacadeInterfacesRefusesAnAliasedRole(t *testing.T) {
	dir := writeFakeContractPackage(t, `package fake

type shim interface {
	Get()
}

type Reader = shim
`)
	_, err := parseFacadeInterfaces(dir, dir, "fake")
	if err == nil {
		t.Fatal("parsing an interface alias succeeded; the role would vanish from the census")
	}
	if !strings.Contains(err.Error(), "aliases an interface") {
		t.Errorf("error = %v, want it to name the alias as the problem", err)
	}
}

func TestParseFacadeInterfacesRefusesAnAliasItCannotClassify(t *testing.T) {
	dir := writeFakeContractPackage(t, `package fake

import "example.com/elsewhere/roles"

type Reader = roles.Reader
`)
	_, err := parseFacadeInterfaces(dir, dir, "fake")
	if err == nil {
		t.Fatal("parsing an out-of-module alias succeeded; the census cannot tell whether it hid a role")
	}
	if !strings.Contains(err.Error(), "cannot classify") {
		t.Errorf("error = %v, want it to say the alias could not be classified", err)
	}
}

func TestParseFacadeInterfacesAcceptsAnAliasToSomethingElse(t *testing.T) {
	dir := writeFakeContractPackage(t, `package fake

type row struct{}

type Row = row

type Reader interface {
	Get()
}
`)
	roles, err := parseFacadeInterfaces(dir, dir, "fake")
	if err != nil {
		t.Fatalf("an alias to a struct was refused: %v", err)
	}
	if _, ok := roles["fake.Reader"]; !ok {
		t.Errorf("census %v lost the role beside the alias", roles)
	}
}

// writeFakeContractPackage writes source to a temporary directory as a contract
// file and returns the directory.
func writeFakeContractPackage(t *testing.T, source string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fake_contract.go"), []byte(source), 0o600); err != nil {
		t.Fatalf("writing the fabricated package: %v", err)
	}
	return dir
}
