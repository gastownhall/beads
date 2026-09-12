package graphops_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/graphops"
)

// The six roles are pinned by method set: a new capability is a new role, and
// a role that grew a method would be two questions behind one accessor.
func TestRolesArePinnedByMethodSet(t *testing.T) {
	for _, tc := range []struct {
		name    string
		role    reflect.Type
		methods []string
	}{
		{"Reader", reflect.TypeFor[graphops.Reader](), []string{"Bead", "Beads", "IncidentLinks", "Link", "Links"}},
		{"DescriptorReader", reflect.TypeFor[graphops.DescriptorReader](), []string{"Descriptor", "Descriptors"}},
		{"TypeInstaller", reflect.TypeFor[graphops.TypeInstaller](), []string{"Install"}},
		{"IdentityReader", reflect.TypeFor[graphops.IdentityReader](), []string{"LedgerDurability", "Read"}},
		{"ScopeBootstrapper", reflect.TypeFor[graphops.ScopeBootstrapper](), []string{"Mint"}},
		{"Admin", reflect.TypeFor[graphops.Admin](), []string{"ClearUnverified", "LedgerApply", "LedgerSnapshot", "MarkUnverified", "Promote", "Rotate"}},
	} {
		var got []string
		for i := 0; i < tc.role.NumMethod(); i++ {
			got = append(got, tc.role.Method(i).Name)
		}
		sort.Strings(got)
		if !reflect.DeepEqual(got, tc.methods) {
			t.Errorf("%s methods = %v, want %v", tc.name, got, tc.methods)
		}
		for i := 0; i < tc.role.NumMethod(); i++ {
			m := tc.role.Method(i)
			if m.Type.NumIn() == 0 || m.Type.In(0).String() != "context.Context" {
				t.Errorf("%s.%s must take a context first", tc.name, m.Name)
			}
			if m.Type.NumOut() == 0 || m.Type.Out(m.Type.NumOut()-1).String() != "error" {
				t.Errorf("%s.%s must return an error last", tc.name, m.Name)
			}
		}
	}
}

// NO AUTHORITY FIELDS ON ANY REQUEST TYPE: the witness is the store's. This
// scans every exported field of every request type for the vocabulary an
// authority field would use.
func TestRequestTypesCarryNoAuthority(t *testing.T) {
	forbidden := []string{"authority", "epoch", "installation", "fence", "witness", "lease", "holder", "token", "key", "claim"}
	for _, req := range []any{
		graphops.BeadRequest{}, graphops.LinkRequest{}, graphops.BeadSelectRequest{}, graphops.LinkSelectRequest{},
		graphops.IncidentRequest{}, graphops.DescriptorRequest{}, graphops.InstallRequest{},
		graphops.MintRequest{}, graphops.PromoteRequest{}, graphops.RotateRequest{}, graphops.LedgerRange{},
	} {
		rt := reflect.TypeOf(req)
		for i := 0; i < rt.NumField(); i++ {
			name := strings.ToLower(rt.Field(i).Name)
			for _, word := range forbidden {
				if strings.Contains(name, word) {
					t.Errorf("%s.%s looks like an authority field (%q)", rt.Name(), rt.Field(i).Name, word)
				}
			}
		}
	}
}

// The package's shape, read from its source: the files the design names, no
// writer.go and no Writer (P3), and imports of the standard library and
// beadserrors only — the same claim the graphops-leaf depguard rule makes,
// checked here so a lint-less `go test` still says it.
func TestPackageShapeAndImports(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ok := pkgs["graphops"]
	if !ok || len(pkgs) != 1 {
		t.Fatalf("expected exactly the graphops package, got %v", pkgs)
	}
	want := map[string]bool{"doc.go": false, "errors.go": false, "types.go": false, "laws.go": false, "reader.go": false, "types_role.go": false, "identity.go": false}
	for path, file := range pkg.Files {
		base := path[strings.LastIndex(path, "/")+1:]
		if base == "writer.go" {
			t.Error("writer.go is P3 and must not exist yet")
		}
		if _, named := want[base]; named {
			want[base] = true
		}
		for _, imp := range file.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if p == "github.com/steveyegge/beads/beadserrors" {
				continue
			}
			if strings.Contains(strings.SplitN(p, "/", 2)[0], ".") {
				t.Errorf("%s imports %s: graphops imports the standard library and beadserrors only", base, p)
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if ts, ok := n.(*ast.TypeSpec); ok && ts.Name.Name == "Writer" {
				t.Error("a Writer type is P3 and must not exist yet")
			}
			return true
		})
	}
	for base, present := range want {
		if !present {
			t.Errorf("%s is named by the design and is missing", base)
		}
	}
}
