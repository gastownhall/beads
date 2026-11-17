package conformance

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// requestField names one exported field of one request struct on the public
// facade.
type requestField struct {
	// Struct is the qualified struct name, e.g. "issueops.UpdateRequest".
	Struct string
	// Field is the field name, e.g. "ExpectedVersion".
	Field string
}

// String renders the fully qualified name the gate reports and waives by.
func (f requestField) String() string { return f.Struct + "." + f.Field }

// facadeSources type-checks packages FROM SOURCE with go/types, resolving an
// import only when it names a facade package.
//
// The role gate's scan resolves receivers by hand, which is enough to tell a
// role method from a lookalike. A field is harder: `req.ExpectedVersion = &v`
// sets UpdateRequest's field or CloseRequest's depending on what req is, and
// req can come from a helper's result, a range variable or a closure's
// parameter. The type checker answers that exactly, so attribution is never a
// guess by field name.
//
// Every other import — the standard library, internal/types, the storage
// packages — is left UNRESOLVED on purpose. go/types reports the failed import
// and carries on, giving every expression that flows through it an invalid
// type, which this census treats as a leaf. That keeps the gate's declared
// inputs to the facade's own sources (the conformance_test target's `data`)
// and costs nothing it needs: a request struct is always declared in a facade
// package, and a type declared anywhere else is a leaf by the census rule.
//
// Type errors are therefore expected and swallowed. Nothing here can fail
// toward a smaller census silently: a type the checker could not resolve
// reaches TestRequestFieldCensusAgreesWithReflection, which compares the
// census against the compiler.
type facadeSources struct {
	fset *token.FileSet
	// dirs maps each facade package's import path to the directory declaring
	// it.
	dirs map[string]string
	// qualifiers maps the same import paths to the names the gate reports by.
	qualifiers map[string]string
	checked    map[string]*types.Package
}

// newFacadeSources serves the real facade packages from the repository at root.
func newFacadeSources(root string) *facadeSources {
	dirs := map[string]string{}
	for path := range facadePackages {
		dirs[path] = filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(strings.TrimPrefix(path, modulePath), "/")))
	}
	return &facadeSources{fset: token.NewFileSet(), dirs: dirs, qualifiers: facadePackages, checked: map[string]*types.Package{}}
}

// Import is the types.Importer the checks run with.
func (s *facadeSources) Import(path string) (*types.Package, error) {
	if pkg, ok := s.checked[path]; ok {
		return pkg, nil
	}
	dir, ok := s.dirs[path]
	if !ok {
		return nil, fmt.Errorf("%s is outside the facade; its types are leaves of the census", path)
	}
	pkg, _, err := s.check(path, dir, nil)
	return pkg, err
}

// check parses dir's non-test sources and type-checks them as path, filling
// info when it is non-nil. It returns the AST beside the package because the
// setter scan reuses the role scan's reachability, which reads the AST.
func (s *facadeSources) check(path, dir string, info *types.Info) (*types.Package, *ast.Package, error) {
	// Parsed into s.fset rather than through parsePackageSources: go/types
	// resolves positions through the FileSet the files were parsed into, and
	// panics on a position it does not hold.
	pkgs, err := parser.ParseDir(s.fset, dir, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing %s: %w", dir, err)
	}
	if len(pkgs) != 1 {
		return nil, nil, fmt.Errorf("%s declares %d packages, want 1", dir, len(pkgs))
	}
	var parsed *ast.Package
	for _, pkg := range pkgs {
		parsed = pkg
	}
	files := make([]*ast.File, 0, len(parsed.Files))
	for _, name := range sortedKeys(parsed.Files) {
		files = append(files, parsed.Files[name])
	}
	conf := types.Config{Importer: s, Error: func(error) {}}
	pkg, _ := conf.Check(path, s.fset, files, info)
	if pkg == nil {
		return nil, nil, fmt.Errorf("type-checking %s produced no package", path)
	}
	if _, ok := s.checked[path]; !ok {
		s.checked[path] = pkg
	}
	return pkg, parsed, nil
}

// requestFieldCensus is the whole role request surface: every exported field of
// every exported struct a facade role method's parameters reach.
func requestFieldCensus(sources *facadeSources) (map[string][]string, error) {
	facade, err := roleFacade()
	if err != nil {
		return nil, err
	}
	return censusRequestFields(sources, facade)
}

// censusRequestFields walks the parameter types of every method of roles,
// keyed "qualifier.Interface", and reports each struct it reaches, keyed
// "qualifier.Struct", with its exported field names sorted.
//
// THE WALK, which reflectRequestFieldCensus repeats on compiled types:
//   - through pointers, slices, arrays and both sides of a map, because a
//     request reaches its nested shapes that way: ApplyItem.Create is a
//     *CreateItem and CreateBatchRequest.Items a []BatchCreateItem;
//   - into every struct a facade package declares, censusing its exported
//     fields and then walking their types;
//   - THROUGH a generic instantiation without censusing it: Field[T] is a
//     wrapper rather than a request shape, so IssuePatch.Title is the field
//     and Field's own Set and Value are not, but a struct reached through a
//     type argument is still reached;
//   - and nowhere else. A type declared outside the facade — internal/types'
//     Issue behind the issueops.Issue alias, time.Time, json.RawMessage — is a
//     leaf: the field holding it is censused and its insides are not. So is a
//     func, an interface, a channel or a basic type.
//
// Anything it cannot classify is REFUSED, for parseFacadeInterfaces' reason: a
// skip is a silently smaller census. That covers an embedded field (its
// promoted fields would vanish), an anonymous struct (its fields have no name
// to waive by) and an unexported struct a request holds.
func censusRequestFields(sources *facadeSources, roles map[string][]string) (map[string][]string, error) {
	census := map[string][]string{}
	var visit func(typ types.Type, via string) error
	visit = func(typ types.Type, via string) error {
		switch t := types.Unalias(typ).(type) {
		case *types.Pointer:
			return visit(t.Elem(), via)
		case *types.Slice:
			return visit(t.Elem(), via)
		case *types.Array:
			return visit(t.Elem(), via)
		case *types.Map:
			if err := visit(t.Key(), via); err != nil {
				return err
			}
			return visit(t.Elem(), via)
		case *types.Struct:
			return fmt.Errorf("%s holds an anonymous struct; the census cannot name its fields", via)
		case *types.Named:
			obj := t.Obj()
			if obj.Pkg() == nil {
				return nil
			}
			qualifier, ok := sources.qualifiers[obj.Pkg().Path()]
			if !ok {
				return nil
			}
			st, ok := t.Underlying().(*types.Struct)
			if !ok {
				return nil
			}
			name := qualifier + "." + obj.Name()
			if t.TypeArgs().Len() == 0 {
				if _, seen := census[name]; seen {
					return nil
				}
				if !obj.Exported() {
					return fmt.Errorf("%s reaches %s, which is unexported; the census cannot say who may set its fields", via, name)
				}
				fields := []string{}
				for i := range st.NumFields() {
					field := st.Field(i)
					if field.Embedded() {
						return fmt.Errorf("%s embeds %s; the census cannot resolve promoted fields", name, field.Name())
					}
					if field.Exported() {
						fields = append(fields, field.Name())
					}
				}
				sort.Strings(fields)
				census[name] = fields
			}
			for i := range st.NumFields() {
				field := st.Field(i)
				if !field.Exported() {
					continue
				}
				if err := visit(field.Type(), name+"."+field.Name()); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, role := range sortedKeys(roles) {
		iface, err := sources.roleInterface(role)
		if err != nil {
			return nil, err
		}
		for i := range iface.NumMethods() {
			method := iface.Method(i)
			params := method.Type().(*types.Signature).Params()
			for j := range params.Len() {
				if err := visit(params.At(j).Type(), role+"."+method.Name()); err != nil {
					return nil, err
				}
			}
		}
	}
	return census, nil
}

// roleInterface resolves a "qualifier.Interface" name to the interface the
// facade package declares.
func (s *facadeSources) roleInterface(role string) (*types.Interface, error) {
	qualifier, name, _ := strings.Cut(role, ".")
	for _, path := range sortedKeys(s.qualifiers) {
		if s.qualifiers[path] != qualifier {
			continue
		}
		pkg, err := s.Import(path)
		if err != nil {
			return nil, err
		}
		obj, ok := pkg.Scope().Lookup(name).(*types.TypeName)
		if !ok {
			return nil, fmt.Errorf("%s names no type its package declares", role)
		}
		iface, ok := obj.Type().Underlying().(*types.Interface)
		if !ok {
			return nil, fmt.Errorf("%s is not an interface", role)
		}
		return iface, nil
	}
	return nil, fmt.Errorf("%s belongs to no facade package", role)
}

// reflectRequestFieldCensus is censusRequestFields on compiled types: the
// independent second opinion TestRequestFieldCensusAgreesWithReflection holds
// the source census to. Its walk is the same walk, rule for rule; a generic
// instantiation is recognized by the brackets reflect puts in its name.
func reflectRequestFieldCensus(roles map[string]reflect.Type, qualifiers map[string]string) (map[string][]string, error) {
	census := map[string][]string{}
	var walk func(t reflect.Type, via string) error
	walk = func(t reflect.Type, via string) error {
		switch t.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			return walk(t.Elem(), via)
		case reflect.Map:
			if err := walk(t.Key(), via); err != nil {
				return err
			}
			return walk(t.Elem(), via)
		case reflect.Struct:
		default:
			return nil
		}
		if t.Name() == "" {
			return fmt.Errorf("%s holds an anonymous struct; the census cannot name its fields", via)
		}
		qualifier, ok := qualifiers[t.PkgPath()]
		if !ok {
			return nil
		}
		name := qualifier + "." + t.Name()
		if !strings.Contains(t.Name(), "[") {
			if _, seen := census[name]; seen {
				return nil
			}
			if !ast.IsExported(t.Name()) {
				return fmt.Errorf("%s reaches %s, which is unexported; the census cannot say who may set its fields", via, name)
			}
			fields := []string{}
			for i := range t.NumField() {
				field := t.Field(i)
				if field.Anonymous {
					return fmt.Errorf("%s embeds %s; the census cannot resolve promoted fields", name, field.Name)
				}
				if field.IsExported() {
					fields = append(fields, field.Name)
				}
			}
			sort.Strings(fields)
			census[name] = fields
		}
		for i := range t.NumField() {
			field := t.Field(i)
			if !field.IsExported() {
				continue
			}
			if err := walk(field.Type, name+"."+field.Name); err != nil {
				return err
			}
		}
		return nil
	}
	for _, name := range sortedKeys(roles) {
		role := roles[name]
		for i := range role.NumMethod() {
			signature := role.Method(i).Type
			for j := range signature.NumIn() {
				if err := walk(signature.In(j), name+"."+role.Method(i).Name); err != nil {
					return nil, err
				}
			}
		}
	}
	return census, nil
}

// scanRequestFieldSetters reports which request fields the contract sources at
// dir, type-checked as path, actually SET, mapped to the functions that set
// them.
//
// A field is set by a keyed composite-literal element whose literal is a
// facade struct (the literal's type comes from the checker, so the elided
// elements of `[]issueops.DependencyEdge{{...}}` count), by an unkeyed element
// in position, or by an assignment or increment whose left-hand side selects
// it. Every facade field a left-hand side passes through counts:
// `req.Patch.Title = v` sets UpdateRequest.Patch and IssuePatch.Title.
// Reading a field sets nothing.
//
// Only declarations the role scan's reachability walk reaches from an exported
// Run entrypoint count, for the reason scanRoleCalls gives: a setter in a
// helper nothing runs vouches for nothing. Package-level variable
// initializers do not count either; none holds a request today.
func scanRequestFieldSetters(sources *facadeSources, path, dir string) (map[requestField][]string, error) {
	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	_, pkg, err := sources.check(path, dir, info)
	if err != nil {
		return nil, err
	}
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
	reachable := reachableFuncs(facts)

	setters := map[requestField]map[string]bool{}
	for _, name := range sortedKeys(pkg.Files) {
		for _, decl := range pkg.Files[name].Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !reachable[funcKey(fn)] {
				continue
			}
			caller := funcKey(fn)
			credit := func(field requestField) {
				if setters[field] == nil {
					setters[field] = map[string]bool{}
				}
				setters[field][caller] = true
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.CompositeLit:
					structName, st := sources.facadeStruct(info.Types[node].Type)
					if st == nil {
						return true
					}
					for i, element := range node.Elts {
						if kv, ok := element.(*ast.KeyValueExpr); ok {
							if key, ok := kv.Key.(*ast.Ident); ok {
								credit(requestField{Struct: structName, Field: key.Name})
							}
							continue
						}
						if i < st.NumFields() {
							credit(requestField{Struct: structName, Field: st.Field(i).Name()})
						}
					}
				case *ast.AssignStmt:
					for _, lhs := range node.Lhs {
						sources.creditAssignedFields(info, lhs, credit)
					}
				case *ast.IncDecStmt:
					sources.creditAssignedFields(info, node.X, credit)
				}
				return true
			})
		}
	}
	out := map[requestField][]string{}
	for field, callers := range setters {
		out[field] = sortedKeys(callers)
	}
	return out, nil
}

// creditAssignedFields credits every facade field a left-hand side selects on
// its way down, so a write into a field's insides counts as setting it.
func (s *facadeSources) creditAssignedFields(info *types.Info, lhs ast.Expr, credit func(requestField)) {
	for {
		switch e := lhs.(type) {
		case *ast.SelectorExpr:
			if selection, ok := info.Selections[e]; ok && selection.Kind() == types.FieldVal {
				// The selection's index path walks embedded fields to the one
				// named; the last hop's struct is the one that declares it.
				recv := selection.Recv()
				path := selection.Index()
				for hop, index := range path {
					structName, st := s.facadeStruct(recv)
					if st == nil {
						break
					}
					field := st.Field(index)
					if hop == len(path)-1 {
						credit(requestField{Struct: structName, Field: field.Name()})
					}
					recv = field.Type()
				}
			}
			lhs = e.X
		case *ast.IndexExpr:
			lhs = e.X
		case *ast.StarExpr:
			lhs = e.X
		case *ast.ParenExpr:
			lhs = e.X
		default:
			return
		}
	}
}

// facadeStruct reports the qualified name and struct of a type, through one
// pointer, when a facade package declares it as a non-generic struct.
func (s *facadeSources) facadeStruct(typ types.Type) (string, *types.Struct) {
	if typ == nil {
		return "", nil
	}
	if ptr, ok := types.Unalias(typ).(*types.Pointer); ok {
		typ = ptr.Elem()
	}
	named, ok := types.Unalias(typ).(*types.Named)
	if !ok || named.Obj().Pkg() == nil || named.TypeArgs().Len() > 0 {
		return "", nil
	}
	qualifier, ok := s.qualifiers[named.Obj().Pkg().Path()]
	if !ok {
		return "", nil
	}
	st, ok := named.Underlying().(*types.Struct)
	if !ok {
		return "", nil
	}
	return qualifier + "." + named.Obj().Name(), st
}

// The census's and the scan's own cases. Each fabricates the shape it is about
// and asserts what the census or the scan makes of it.

// fakeFacade serves one fabricated facade package, at the import path
// example.com/fakeops, from a temporary directory.
func fakeFacade(t *testing.T, source string) (*facadeSources, map[string][]string) {
	t.Helper()
	dir := writeFakeContractPackage(t, source)
	const path = "example.com/fakeops"
	sources := &facadeSources{
		fset:       token.NewFileSet(),
		dirs:       map[string]string{path: dir},
		qualifiers: map[string]string{path: "fakeops"},
		checked:    map[string]*types.Package{},
	}
	roles, err := parseFacadeInterfaces(dir, dir, "fakeops")
	if err != nil {
		t.Fatalf("parsing the fabricated facade: %v", err)
	}
	return sources, roles
}

func TestCensusRequestFieldsWalksPointersSlicesMapsAndTypeArguments(t *testing.T) {
	sources, roles := fakeFacade(t, `package fakeops

import (
	"context"
	"time"
)

type Doer interface {
	Do(ctx context.Context, req DoRequest) error
}

type Field[T any] struct {
	Set   bool
	Value T
}

type DoRequest struct {
	Actor   string
	Items   []Item
	ByKey   map[Key]*Nested
	Wrapped Field[Leaf]
	Hook    func() error
	When    *time.Time
	hidden  int
}

type Item struct{ X int }
type Key struct{ K string }
type Nested struct{ Y int }
type Leaf struct{ Z int }
`)
	census, err := censusRequestFields(sources, roles)
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	want := map[string][]string{
		"fakeops.DoRequest": {"Actor", "ByKey", "Hook", "Items", "When", "Wrapped"},
		"fakeops.Item":      {"X"},
		"fakeops.Key":       {"K"},
		"fakeops.Nested":    {"Y"},
		"fakeops.Leaf":      {"Z"},
	}
	if !reflect.DeepEqual(census, want) {
		t.Errorf("census = %v, want %v (Field[T] is a wrapper and must not appear; its type argument must)", census, want)
	}
}

func TestCensusRequestFieldsRefusesWhatItCannotName(t *testing.T) {
	for _, tc := range []struct {
		name, decls, want string
	}{
		{"an embedded field", "type DoRequest struct {\n\tBase\n\tA string\n}\ntype Base struct{ B int }", "embeds"},
		{"an anonymous struct", "type DoRequest struct {\n\tOpts struct{ A int }\n}", "anonymous struct"},
		{"an unexported struct", "type DoRequest struct {\n\tInner inner\n}\ntype inner struct{ A int }", "unexported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sources, roles := fakeFacade(t, "package fakeops\n\ntype Doer interface {\n\tDo(req DoRequest) error\n}\n\n"+tc.decls+"\n")
			_, err := censusRequestFields(sources, roles)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("census error = %v, want one mentioning %q: a skip here is a silently smaller census", err, tc.want)
			}
		})
	}
}

func TestReflectRequestFieldCensusWalksLikeTheSourceCensus(t *testing.T) {
	type leaf struct{ Z int }
	type (
		Item    struct{ X int }
		Nested  struct{ Y int }
		Wrapper struct{ Inner Nested }
	)
	type DoRequest struct {
		Actor  string
		Items  []Item
		ByName map[string]*Wrapper
		Hook   func() error
		hidden leaf
	}
	type doer interface {
		Do(req DoRequest) error
	}
	here := reflect.TypeOf(Item{}).PkgPath()
	census, err := reflectRequestFieldCensus(
		map[string]reflect.Type{"local.Doer": reflect.TypeOf((*doer)(nil)).Elem()},
		map[string]string{here: "local"},
	)
	if err != nil {
		t.Fatalf("reflection census: %v", err)
	}
	want := map[string][]string{
		"local.DoRequest": {"Actor", "ByName", "Hook", "Items"},
		"local.Item":      {"X"},
		"local.Nested":    {"Y"},
		"local.Wrapper":   {"Inner"},
	}
	if !reflect.DeepEqual(census, want) {
		t.Errorf("reflection census = %v, want %v", census, want)
	}
}

func TestReflectRequestFieldCensusRefusesAnEmbeddedField(t *testing.T) {
	type Base struct{ B int }
	type DoRequest struct {
		Base
		A string
	}
	type doer interface {
		Do(req DoRequest) error
	}
	here := reflect.TypeOf(Base{}).PkgPath()
	_, err := reflectRequestFieldCensus(
		map[string]reflect.Type{"local.Doer": reflect.TypeOf((*doer)(nil)).Elem()},
		map[string]string{here: "local"},
	)
	if err == nil || !strings.Contains(err.Error(), "embeds") {
		t.Errorf("reflection census error = %v, want an embedded-field refusal", err)
	}
}

// scanFakeContracts scans a fabricated contract package against the REAL
// facade, the shape every contract file has.
func scanFakeContracts(t *testing.T, source string) map[requestField][]string {
	t.Helper()
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("locating the repository root: %v", err)
	}
	dir := writeFakeContractPackage(t, source)
	covered, err := scanRequestFieldSetters(newFacadeSources(root), "example.com/fakecontracts", dir)
	if err != nil {
		t.Fatalf("scanning the fabricated package: %v", err)
	}
	return covered
}

func TestScanRequestFieldSettersCreditsEveryWayAContractSetsAField(t *testing.T) {
	covered := scanFakeContracts(t, `package fake

import (
	"context"

	publicops "github.com/steveyegge/beads/issueops"
)

type Fixture struct {
	Lifecycle publicops.Lifecycle
	Editor    publicops.DependencyEditor
	Reader    publicops.Reader
	Applier   publicops.BatchApplier
}

func closeRequestFor(id string) publicops.CloseRequest {
	return publicops.CloseRequest{IssueID: id}
}

func RunEverySetterShape(ctx context.Context, fixture Fixture) {
	version := int64(1)
	// A keyed literal.
	fixture.Lifecycle.Close(ctx, publicops.CloseRequest{ExpectedVersion: &version})
	// Elided elements of a typed slice literal.
	fixture.Editor.AddDependencies(ctx, publicops.AddDependenciesRequest{
		Edges: []publicops.DependencyEdge{{IssueID: "a", DependsOnID: "b"}},
	})
	// An assignment through a helper's result, and a chain into a nested shape.
	update := publicops.UpdateRequest{}
	update.Claim = true
	update.Patch.Title = publicops.Field[string]{Set: true, Value: "t"}
	fixture.Lifecycle.Update(ctx, update)
	reopen := closeRequestFor("x")
	reopen.Force = true
	// A closure's parameter.
	mutate := func(r *publicops.ListRequest) { r.LabelPattern = "p*" }
	list := publicops.ListRequest{}
	mutate(&list)
	fixture.Reader.List(ctx, list)
	// A pointer-held nested item.
	fixture.Applier.ApplyBatch(ctx, publicops.ApplyBatchRequest{
		Items: []publicops.ApplyItem{{Create: &publicops.CreateItem{Ref: "r"}}},
	})
	// A read is not a set.
	if update.ForceClosePolicy {
		return
	}
}
`)
	for _, want := range []string{
		"issueops.CloseRequest.ExpectedVersion",
		"issueops.AddDependenciesRequest.Edges",
		"issueops.DependencyEdge.IssueID",
		"issueops.DependencyEdge.DependsOnID",
		"issueops.UpdateRequest.Claim",
		"issueops.UpdateRequest.Patch",
		"issueops.IssuePatch.Title",
		"issueops.CloseRequest.IssueID",
		"issueops.CloseRequest.Force",
		"issueops.ListRequest.LabelPattern",
		"issueops.ApplyBatchRequest.Items",
		"issueops.ApplyItem.Create",
		"issueops.CreateItem.Ref",
	} {
		if !coversField(covered, want) {
			t.Errorf("%s was not seen as set; the scan found %v", want, covered)
		}
	}
	for _, unwanted := range []string{
		// Same field name, different struct: attribution is by type.
		"issueops.UpdateRequest.ExpectedVersion",
		"issueops.ReopenRequest.ExpectedVersion",
		// Read, never written.
		"issueops.UpdateRequest.ForceClosePolicy",
		// A generic wrapper is not a request shape.
		"issueops.Field.Set",
	} {
		if coversField(covered, unwanted) {
			t.Errorf("%s was credited, but nothing sets it", unwanted)
		}
	}
}

func TestScanRequestFieldSettersIgnoresAHelperNoEntrypointRuns(t *testing.T) {
	covered := scanFakeContracts(t, `package fake

import (
	"context"

	publicops "github.com/steveyegge/beads/issueops"
)

type Fixture struct {
	Lifecycle publicops.Lifecycle
}

func RunClosesAnIssue(ctx context.Context, fixture Fixture) {
	fixture.Lifecycle.Close(ctx, publicops.CloseRequest{IssueID: "a"})
}

func orphanedHelper(ctx context.Context, fixture Fixture) {
	fixture.Lifecycle.Close(ctx, publicops.CloseRequest{Session: "s"})
}
`)
	if !coversField(covered, "issueops.CloseRequest.IssueID") {
		t.Errorf("the entrypoint's setter was not credited; the scan found %v", covered)
	}
	if coversField(covered, "issueops.CloseRequest.Session") {
		t.Errorf("CloseRequest.Session was credited to %v, but only an unreachable helper sets it",
			covered[requestField{Struct: "issueops.CloseRequest", Field: "Session"}])
	}
}

func coversField(covered map[requestField][]string, qualified string) bool {
	for field, callers := range covered {
		if field.String() == qualified && len(callers) > 0 {
			return true
		}
	}
	return false
}
