package httpapi

// The mount table: what the router holds, and the guard that it holds nothing
// else.
//
// s.mounts() is every registration handler() makes. The censuses that hold a
// request-pipeline property to "every route" range over it (through
// mountEndpoints) rather than over routeTable, so a handler mounted beside the
// table is held to the same properties as a row in it. That only means
// something if nothing can be mounted around the list, which is what
// TestNothingIsMountedOutsideTheMountTable reads off the package's source.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// mountEndpoint is one answer the router gives, and one request that reaches
// it: an operation a mount dispatches to, or the unrouted answer a mount gives
// a request it holds but no row claims (the catch-all's 404, or a
// custom-method suffix no row registers).
type mountEndpoint struct {
	// pattern is the registration of the mount that answers.
	pattern string
	// row is the operation answering, or nil for the unrouted answer.
	row          *route
	method, path string
}

// name labels the endpoint in census lists and failures: its op, or
// "unrouted <pattern>".
func (e mountEndpoint) name() string {
	if e.row != nil {
		return e.row.op
	}
	return "unrouted " + e.pattern
}

// op is what the endpoint's request line books: its row's op, or nothing.
func (e mountEndpoint) op() string {
	if e.row != nil {
		return e.row.op
	}
	return ""
}

// probeWildcards fills the path wildcards the route table uses. A mount with
// any other wildcard fails mountEndpoints until it gets a value here.
var probeWildcards = map[string]string{"id": "bd-1", "key": "k", customMethodPathValue: "bd-1"}

// unregisteredCustomMethod is a suffix no row registers.
const unregisteredCustomMethod = ":nosuchverb"

// mountEndpoints flattens a mount list into its endpoints, in mount order: one
// per row the router can spell, one per row sharing a custom-method key plus
// that key's unrouted answer, and one for a mount with no rows.
//
// Every endpoint's request is then routed through a ServeMux built from the
// same list, so a request that lands on a different mount fails here rather
// than passing a census for the wrong reason.
func mountEndpoints(t *testing.T, ms []mount) []mountEndpoint {
	t.Helper()
	var out []mountEndpoint
	for _, m := range ms {
		method, path, _ := strings.Cut(m.pattern, " ")
		if path == "" {
			method, path = http.MethodGet, m.pattern
		}
		fill := func(suffix string) string {
			segs := strings.Split(path, "/")
			for i, seg := range segs {
				name, ok := strings.CutPrefix(seg, "{")
				if !ok {
					continue
				}
				name = strings.TrimSuffix(name, "}")
				value, known := probeWildcards[name]
				if !known {
					t.Fatalf("mount %q has a wildcard {%s} no endpoint knows how to fill; add a value to probeWildcards", m.pattern, name)
				}
				if name == customMethodPathValue {
					value += suffix
				}
				segs[i] = value
			}
			return strings.Join(segs, "/")
		}
		switch {
		case len(m.rows) == 0:
			// A mount no operation owns is probed at a path it holds and no
			// other mount spells: a subtree pattern gets a segment appended.
			p := fill("")
			if strings.HasSuffix(p, "/") {
				p += "no-such-route"
			}
			out = append(out, mountEndpoint{pattern: m.pattern, method: method, path: p})
		case len(m.rows) == 1 && m.rows[0].customMethod == "":
			out = append(out, mountEndpoint{pattern: m.pattern, row: &m.rows[0], method: method, path: fill("")})
		default:
			for i := range m.rows {
				out = append(out, mountEndpoint{pattern: m.pattern, row: &m.rows[i], method: method, path: fill(m.rows[i].customMethod)})
			}
			out = append(out, mountEndpoint{pattern: m.pattern, method: method, path: fill(unregisteredCustomMethod)})
		}
	}

	mux := http.NewServeMux()
	for _, m := range ms {
		mux.Handle(m.pattern, m.handler)
	}
	for _, e := range out {
		if _, got := mux.Handler(httptest.NewRequest(e.method, e.path, nil)); got != e.pattern {
			t.Fatalf("%s: %s %s routes to mount %q, not to %q", e.name(), e.method, e.path, got, e.pattern)
		}
	}
	return out
}

// defaultMounts is what a server built from a zero Config mounts.
func defaultMounts(t *testing.T) []mount {
	t.Helper()
	return newTestServer(t, Config{}).mounts()
}

// defaultRows is every operation a default server's mounts answer, in mount
// order: what the declaration checks range over.
func defaultRows(t *testing.T) []route {
	t.Helper()
	var out []route
	for _, m := range defaultMounts(t) {
		out = append(out, m.rows...)
	}
	return out
}

// notIn is the members of a that b lacks.
func notIn(a, b []string) []string {
	var out []string
	for _, s := range a {
		if !slices.Contains(b, s) {
			out = append(out, s)
		}
	}
	return out
}

// TestDefaultServerMountsOnlyTheRouteTable: a server built from a zero Config
// mounts the route table and nothing else. One registration per row the router
// can spell, one per custom-method key the rows share, and the catch-all; each
// row answered by exactly one mount; the catch-all the only mount that answers
// no operation.
//
// The expectation is re-derived from routeTable here rather than compared
// against mounts() with itself. A mount added to every server (a second
// protocol's namespace, a document, a debug handler) fails it by name; one
// added only behind a Config option leaves it green and must bring that
// option's own expected set. TestNothingIsMountedOutsideTheMountTable is what
// makes mounts() the router's whole registration list.
func TestDefaultServerMountsOnlyTheRouteTable(t *testing.T) {
	ms := defaultMounts(t)

	want := []string{"/"}
	var wantRows []string
	for _, rt := range routeTable {
		if key := rt.method + " " + rt.pattern; !slices.Contains(want, key) {
			want = append(want, key)
		}
		wantRows = append(wantRows, rt.op)
	}
	var got, gotRows []string
	dispatchers, rowless := 0, 0
	for _, m := range ms {
		got = append(got, m.pattern)
		for _, rt := range m.rows {
			gotRows = append(gotRows, rt.op)
		}
		switch {
		case len(m.rows) == 0:
			rowless++
			if m.pattern != "/" {
				t.Errorf("mount %q answers no operation, so no row declares what the request pipeline owes it", m.pattern)
			}
		case m.rows[0].customMethod != "":
			dispatchers++
			for _, rt := range m.rows {
				if rt.customMethod == "" || rt.method+" "+rt.pattern != m.pattern {
					t.Errorf("mount %q shares its registration with %s, which is not a custom-method row on that pattern", m.pattern, rt.op)
				}
			}
		case len(m.rows) > 1:
			t.Errorf("mount %q answers %d operations but is not a custom-method dispatcher", m.pattern, len(m.rows))
		}
	}
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("a default server mounts %d patterns, want the route table's %d plus the catch-all:\nextra:   %v\nmissing: %v",
			len(got), len(want)-1, notIn(got, want), notIn(want, got))
	}
	slices.Sort(wantRows)
	slices.Sort(gotRows)
	if !slices.Equal(gotRows, wantRows) {
		t.Errorf("the mounts answer operations %v, want each route table row exactly once %v", gotRows, wantRows)
	}
	if rowless != 1 {
		t.Errorf("%d mounts answer no operation, want exactly the catch-all", rowless)
	}

	// And every endpoint the censuses probe reaches its own mount.
	if eps, want := mountEndpoints(t, ms), len(routeTable)+dispatchers+rowless; len(eps) != want {
		t.Errorf("%d endpoints, want one per row, one per dispatcher's unregistered suffix and one per rowless mount (%d)", len(eps), want)
	}
}

// TestNothingIsMountedOutsideTheMountTable reads the package's non-test source
// and holds it to one registration site: handler() ranging over s.mounts(),
// registering each mount's pattern and handler on the one ServeMux it builds,
// and returning that mux behind checkHost and withRequestContext and nothing
// else.
//
// Every other way onto the wire it can see is a stray mount: a Handle or
// HandleFunc call anywhere else (http.Handle included, which is the
// DefaultServeMux) or one taken as a method value, a second ServeMux, an
// http.Server whose Handler is not s.handler() (a literal with no Handler,
// new(http.Server) and a zero http.Server serve the DefaultServeMux), a call
// to http.Serve, http.ServeTLS, http.ListenAndServe or http.ListenAndServeTLS,
// a reassigned Handler, or a wrapper around the router. Each would answer
// requests no census sees, which is how a second request pipeline ships with
// the whole suite green. To serve something new, add it to s.mounts() with
// rows that declare its posture; the censuses then hold it to that posture.
//
// Structural on purpose, like TestEveryTimedProviderAccessorBindsToTheWrapper:
// a behavioral census can only probe what it enumerates, and this is what
// makes s.mounts() the whole enumeration. It reads this package's syntax, so
// it needs net/http imported as http, and it cannot see what code in another
// package does with a router or a handler this package hands it.
func TestNothingIsMountedOutsideTheMountTable(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package sources: %v", err)
	}
	files := map[string]*ast.File{}
	for _, pkg := range pkgs {
		for path, f := range pkg.Files {
			files[filepath.Base(path)] = f
		}
	}
	// Under Bazel the source is the test's data (:repo_go_srcs); a narrowed
	// data list would leave this scan reading nothing and passing.
	for _, name := range []string{"server.go", "routes.go"} {
		if files[name] == nil {
			t.Fatalf("%s was not among the %d sources parsed; the scan cannot see the router", name, len(files))
		}
	}
	sanctioned, strays := mountRegistrations(fset, files)
	for _, s := range strays {
		t.Error(s)
	}
	if sanctioned != 1 {
		t.Errorf("found %d registrations of the shape handler() uses, want exactly 1: the scan no longer recognizes the router", sanctioned)
	}
}

// mountRegistrations finds every way the given sources put a handler on the
// wire. sanctioned counts registrations of the one allowed shape; strays
// describes every other, with its position.
func mountRegistrations(fset *token.FileSet, files map[string]*ast.File) (sanctioned int, strays []string) {
	for _, name := range slices.Sorted(maps.Keys(files)) {
		// Every rule below reads net/http by the name http.
		for _, imp := range files[name].Imports {
			if imp.Path.Value == `"net/http"` && imp.Name != nil && imp.Name.Name != "http" {
				strays = append(strays, fmt.Sprintf("%s: net/http imported as %s, which hides every use of it from this scan: import it as http",
					fset.Position(imp.Pos()), imp.Name.Name))
			}
		}
		for _, decl := range files[name].Decls {
			where := "a package-level declaration"
			fn, isFunc := decl.(*ast.FuncDecl)
			if isFunc {
				where = declName(fn)
			}
			stray := func(n ast.Node, what string) {
				strays = append(strays, fmt.Sprintf("%s: %s in %s: mount it through s.mounts() instead", fset.Position(n.Pos()), what, where))
			}
			isRouter := where == "(*Server).handler"
			// The one allowed registration: inside handler(), in a range over
			// s.mounts(), mux.Handle(<v>.pattern, <v>.handler).
			allowed := map[*ast.CallExpr]bool{}
			if isRouter {
				returns := 0
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					switch n := n.(type) {
					case *ast.FuncLit:
						return false
					case *ast.ReturnStmt:
						// handler() hands back that mux behind the Host check and the
						// request context, and nothing else.
						returns++
						if len(n.Results) != 1 || types.ExprString(n.Results[0]) != "s.withRequestContext(s.checkHost(mux))" {
							got := "nothing"
							if len(n.Results) > 0 {
								got = types.ExprString(n.Results[0])
							}
							stray(n, "the router is returned as "+got+", not s.withRequestContext(s.checkHost(mux))")
						}
					case *ast.RangeStmt:
						v, _ := n.Value.(*ast.Ident)
						if v == nil || types.ExprString(n.X) != "s.mounts()" {
							return true
						}
						ast.Inspect(n.Body, func(n ast.Node) bool {
							if call, ok := n.(*ast.CallExpr); ok && types.ExprString(call.Fun) == "mux.Handle" && len(call.Args) == 2 &&
								types.ExprString(call.Args[0]) == v.Name+".pattern" && types.ExprString(call.Args[1]) == v.Name+".handler" {
								allowed[call] = true
							}
							return true
						})
					}
					return true
				})
				if returns == 0 {
					stray(fn, "a router that is never returned")
				}
			}
			newMuxes := 0
			// The selectors a call invokes. Inspect visits a call before its
			// function, so a Handle or HandleFunc selector missing here when the
			// selector itself is visited is a method value: a registration whose
			// call this scan cannot see.
			called := map[*ast.SelectorExpr]bool{}
			ast.Inspect(decl, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.CallExpr:
					if types.ExprString(n.Fun) == "new" && len(n.Args) == 1 && types.ExprString(n.Args[0]) == "http.Server" {
						stray(n, "new(http.Server), which serves http.DefaultServeMux")
					}
					sel, ok := n.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					called[sel] = true
					switch fun := types.ExprString(sel); {
					case (sel.Sel.Name == "Handle" || sel.Sel.Name == "HandleFunc") && len(n.Args) == 2:
						if allowed[n] {
							sanctioned++
						} else {
							stray(n, fun+"("+types.ExprString(n.Args[0])+", ...)")
						}
					case fun == "http.NewServeMux":
						if newMuxes++; !isRouter || newMuxes > 1 {
							stray(n, "a second http.NewServeMux()")
						}
					case slices.Contains([]string{"http.Serve", "http.ServeTLS", "http.ListenAndServe", "http.ListenAndServeTLS"}, fun):
						// Each serves the handler it is given, and a nil one is
						// the DefaultServeMux.
						stray(n, "a second server, "+fun+"(...)")
					}
				case *ast.SelectorExpr:
					switch s := types.ExprString(n); {
					case s == "http.ServeMux" || s == "http.DefaultServeMux":
						stray(n, s)
					case (n.Sel.Name == "Handle" || n.Sel.Name == "HandleFunc") && !called[n]:
						stray(n, s+" taken as a value")
					}
				case *ast.CompositeLit:
					if types.ExprString(n.Type) != "http.Server" {
						return true
					}
					handler := false
					for _, elt := range n.Elts {
						if kv, ok := elt.(*ast.KeyValueExpr); ok && types.ExprString(kv.Key) == "Handler" {
							handler = true
							if types.ExprString(kv.Value) != "s.handler()" {
								stray(kv, "an http.Server serving "+types.ExprString(kv.Value))
							}
						}
					}
					if !handler {
						stray(n, "an http.Server with no Handler, which serves http.DefaultServeMux")
					}
				case *ast.ValueSpec:
					if n.Type != nil && types.ExprString(n.Type) == "http.Server" {
						stray(n, "a zero http.Server, which serves http.DefaultServeMux")
					}
				case *ast.AssignStmt:
					for _, lhs := range n.Lhs {
						if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "Handler" {
							stray(n, types.ExprString(lhs)+" reassigned")
						}
					}
				}
				return true
			})
		}
	}
	return sanctioned, strays
}

// declName spells a declaration the way a failure names it:
// "(*Server).handler" for a method, "Listen" for a function.
func declName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	return "(" + types.ExprString(fn.Recv.List[0].Type) + ")." + fn.Name.Name
}

// TestStrayMountRuleFires is the scan's falsification. It is green against a
// package written to satisfy it, so each case is synthetic source that mounts
// around the table one way and must be reported.
func TestStrayMountRuleFires(t *testing.T) {
	const router = `package httpapi
func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	for _, m := range s.mounts() {
		mux.Handle(m.pattern, m.handler)
	}
	return s.withRequestContext(s.checkHost(mux))
}
func Listen(cfg Config) (*Server, error) {
	s := &Server{}
	s.http = &http.Server{Handler: s.handler(), ReadTimeout: readTimeout}
	return s, nil
}
`
	scan := func(t *testing.T, sources map[string]string) (int, []string) {
		t.Helper()
		fset := token.NewFileSet()
		files := map[string]*ast.File{}
		for name, src := range sources {
			f, err := parser.ParseFile(fset, name, src, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", name, err)
			}
			files[name] = f
		}
		return mountRegistrations(fset, files)
	}
	if n, strays := scan(t, map[string]string{"server.go": router}); n != 1 || len(strays) != 0 {
		t.Fatalf("the sanctioned shape: %d registrations, strays %v; want 1 and none", n, strays)
	}

	// Another file mounting around the table.
	for _, tc := range []struct{ name, src, want string }{
		{"a namespace mounted by its own function", `func (s *Server) mountCalls(mux *http.ServeMux) { mux.Handle("POST /bdp/call/{name}", s.calls()) }`,
			`mux.Handle("POST /bdp/call/{name}", ...) in (*Server).mountCalls`},
		{"the DefaultServeMux", `func init() { http.HandleFunc("/debug", debug) }`, `http.HandleFunc("/debug", ...) in init`},
		{"a second router", `func (s *Server) admin() http.Handler { return http.NewServeMux() }`, "a second http.NewServeMux() in (*Server).admin"},
		{"a wrapper in front of the router", `func (s *Server) serve() { s.http.Handler = s.coreOr(s.http.Handler) }`, "s.http.Handler reassigned in (*Server).serve"},
		{"a second server", `func (s *Server) debug() *http.Server { return &http.Server{Handler: s.pprof()} }`, "an http.Server serving s.pprof() in (*Server).debug"},
		{"a registration in a package-level function literal", `var mountLater = func(m interface{ Handle(string, http.Handler) }) { m.Handle("/bdp/", nil) }`,
			`m.Handle("/bdp/", ...) in a package-level declaration`},
		{"the DefaultServeMux on a debug port", `func (s *Server) debug() { go func() { _ = http.ListenAndServe("127.0.0.1:6060", nil) }() }`,
			"a second server, http.ListenAndServe(...) in (*Server).debug"},
		{"a listener served beside the router", `func (s *Server) debug(ln net.Listener) error { return http.Serve(ln, s.pprof()) }`,
			"a second server, http.Serve(...) in (*Server).debug"},
		{"a TLS listener served beside the router", `func (s *Server) debug(ln net.Listener) error { return http.ServeTLS(ln, s.pprof(), "cert", "key") }`,
			"a second server, http.ServeTLS(...) in (*Server).debug"},
		{"the DefaultServeMux over TLS", `func (s *Server) debug() error { return http.ListenAndServeTLS(":6443", "cert", "key", nil) }`,
			"a second server, http.ListenAndServeTLS(...) in (*Server).debug"},
		{"a server with no Handler", `func (s *Server) debug() *http.Server { return &http.Server{Addr: "127.0.0.1:6060"} }`,
			"an http.Server with no Handler, which serves http.DefaultServeMux in (*Server).debug"},
		{"a server from new", `func (s *Server) debug() *http.Server { return new(http.Server) }`,
			"new(http.Server), which serves http.DefaultServeMux in (*Server).debug"},
		{"a zero server", `func (s *Server) debug() error { var srv http.Server; return srv.ListenAndServe() }`,
			"a zero http.Server, which serves http.DefaultServeMux in (*Server).debug"},
		{"net/http under another name", "import nethttp \"net/http\"\nfunc (s *Server) debug() { _ = nethttp.ListenAndServe(\":6060\", nil) }",
			"net/http imported as nethttp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, strays := scan(t, map[string]string{"server.go": router, "extra.go": "package httpapi\n" + tc.src})
			hits := 0
			for _, s := range strays {
				if strings.Contains(s, tc.want) {
					hits++
				}
			}
			if n != 1 || hits != 1 {
				t.Errorf("%d sanctioned registrations and strays %v; want 1 and exactly one stray containing %q", n, strays, tc.want)
			}
		})
	}

	// handler() itself: only the range over s.mounts() is sanctioned, and only
	// the plain wrap is returned.
	for _, tc := range []struct{ name, from, to, want string }{
		{"a registration beside the loop", "\treturn s.withRequestContext", "\tmux.Handle(\"/bdp/\", s.bdp())\n\treturn s.withRequestContext",
			`mux.Handle("/bdp/", ...) in (*Server).handler`},
		{"a wrapper inside the router", "s.checkHost(mux)", "s.checkHost(s.coreOr(mux))",
			"the router is returned as s.withRequestContext(s.checkHost(s.coreOr(mux)))"},
		{"a second return on a branch", "\treturn s.withRequestContext", "\tif s.cfg.Both {\n\t\treturn s.coreOr(mux)\n\t}\n\treturn s.withRequestContext",
			"the router is returned as s.coreOr(mux)"},
		{"a loop over another list", "s.mounts()", "s.bdpMounts()", "mux.Handle(m.pattern, ...) in (*Server).handler"},
		{"a handler wrapped at registration", "mux.Handle(m.pattern, m.handler)", "mux.Handle(m.pattern, s.gate(m.handler))",
			"mux.Handle(m.pattern, ...) in (*Server).handler"},
		{"a registration through a method value", "\treturn s.withRequestContext", "\tregister := mux.Handle\n\tregister(\"GET /v0/debug/raw\", nil)\n\treturn s.withRequestContext",
			"mux.Handle taken as a value in (*Server).handler"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, strays := scan(t, map[string]string{"server.go": strings.Replace(router, tc.from, tc.to, 1)})
			if len(strays) != 1 || !strings.Contains(strays[0], tc.want) {
				t.Errorf("strays %v, want exactly one containing %q", strays, tc.want)
			}
		})
	}
}
