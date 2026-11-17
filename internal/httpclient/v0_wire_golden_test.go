package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi"
	"github.com/steveyegge/beads/internal/httpapi/spec"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"gopkg.in/yaml.v3"
)

// The v0 client wire golden (G2(a)).
//
// Every row of v0WireRows drives one role call through a Store whose
// transport is the production one: a sidecar written by SaveTarget and read
// back by LoadTarget, dialed by Dial with zero DialOptions, so the request
// building, stamping, credential ladder and http.Transport are exactly what a
// `bd` process connected with `bd connect` uses. The server is a recorder that
// answers each operation with a minimal 2xx and keeps what arrived: method,
// raw request-target, headers and body. Each row's transcript is compared
// byte for byte with testdata/v0_wire/<row>.golden.
//
// Regenerate after an intentional wire change, then review the diff like code:
//
//	BD_UPDATE_GOLDEN=1 CGO_ENABLED=1 go test -tags gms_pure_go ./internal/httpclient -run '^TestV0ClientRequestsAreFrozen$'
const (
	v0WireGoldenDir   = "testdata/v0_wire"
	v0WireGoldenRegen = "BD_UPDATE_GOLDEN=1 CGO_ENABLED=1 go test -tags gms_pure_go ./internal/httpclient -run '^TestV0ClientRequestsAreFrozen$'"

	v0WireProjectID = "proj-v0-wire-golden"
	// #nosec G101 -- a fixed test credential the recorder checks and masks.
	v0WireToken         = "v0-wire-golden-token"
	v0WireTokenRendered = "Bearer <BEADS_HTTP_TOKEN>"
	v0WireBdVersion     = "0.0.0-v0-wire-golden"
)

// ---------------------------------------------------------------------------
// The static census: every wire.Request literal in the two client packages.

// v0WireSite is one `wire.Request{...}` composite literal in a non-test file of
// internal/httpclient, or one `Request{...}` in internal/httpclient/wire. The
// literal a factory returns is one site per func that calls the factory.
type v0WireSite struct {
	// Key is the file and enclosing func, "role_reader.go:(httpReader).Get";
	// a factory's caller adds itself: "list_walk.go:listRequest@(*Store).sortedPage".
	Key     string
	Frame   string // runtime frame name: "httpclient.httpReader.Get", "wire.(*Client).ClaimIssue"
	OpConst string // the Op: member's constant name, "" when it is not a constant
}

const v0WireModule = "github.com/steveyegge/beads/internal/httpclient"

// v0WireLiteralSites parses the non-test Go files of this package (".") and
// of the wire package ("wire"); both are runfiles under Bazel
// (BUILD.bazel: glob(["*.go"]) and //internal/httpclient/wire:repo_go_srcs).
//
// Attribution goes by function, so a function holds at most one literal: a
// second one (say a BDP twin in a capability branch of a v0 function) fails
// every census test until it moves to a function of its own, which no row or
// waiver can stand in for.
func v0WireLiteralSites(t *testing.T) map[string]*v0WireSite {
	t.Helper()
	sites := map[string]*v0WireSite{}
	type pkgDir struct{ dir, pkg, typ string }
	keyPrefix := map[string]string{".": "", "wire": "wire/"}
	factories := map[string][]*v0WireSite{} // "<pkg>.<func>" -> the literals it returns
	type call struct{ callee, display, frame string }
	var calls []call
	for _, d := range []pkgDir{{".", "httpclient", "wire.Request"}, {"wire", "wire", "Request"}} {
		files, err := filepath.Glob(filepath.Join(d.dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		sort.Strings(files)
		n := 0
		bctx := build.Default
		bctx.CgoEnabled = true
		bctx.BuildTags = []string{"gms_pure_go"}
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			if ok, err := bctx.MatchFile(filepath.Dir(path), filepath.Base(path)); err != nil || !ok {
				continue
			}
			n++
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			for _, decl := range f.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				display, frame := v0WireFuncNames(d.pkg, fd)
				returns := false
				if fd.Type.Results != nil {
					for _, r := range fd.Type.Results.List {
						returns = returns || v0WireExprText(r.Type) == d.typ
					}
				}
				ord := 0
				ast.Inspect(fd.Body, func(node ast.Node) bool {
					switch x := node.(type) {
					case *ast.CompositeLit:
						if x.Type == nil || v0WireExprText(x.Type) != d.typ {
							return true
						}
						ord++
						key := keyPrefix[d.dir] + filepath.Base(path) + ":" + display
						if ord > 1 {
							key += "#" + strconv.Itoa(ord)
						}
						s := &v0WireSite{Key: key, Frame: frame}
						for _, el := range x.Elts {
							if kv, ok := el.(*ast.KeyValueExpr); ok && v0WireExprText(kv.Key) == "Op" {
								switch v := kv.Value.(type) {
								case *ast.SelectorExpr:
									s.OpConst = v.Sel.Name
								case *ast.Ident:
									s.OpConst = v.Name
								}
							}
						}
						if returns {
							name := d.pkg + "." + fd.Name.Name
							factories[name] = append(factories[name], s)
						} else {
							sites[key] = s
						}
					case *ast.CallExpr:
						if id, ok := x.Fun.(*ast.Ident); ok {
							calls = append(calls, call{callee: d.pkg + "." + id.Name, display: display, frame: frame})
						}
					}
					return true
				})
			}
		}
		if n == 0 {
			t.Fatalf("no non-test Go source under %s: under Bazel it must be in the test's data", d.dir)
		}
	}
	// A factory, a func that RETURNS the literal (list_walk.go's listRequest),
	// is never on the stack when the request is sent. Its literal is one site
	// per func that calls it by name, so each of those send paths needs a row
	// of its own. A factory nobody calls by name stays one site, which no
	// request reaches, so the census reports it rather than losing it.
	for name, literals := range factories {
		for _, f := range literals {
			called := false
			for _, c := range calls {
				if c.callee != name {
					continue
				}
				called = true
				key := f.Key + "@" + c.display
				sites[key] = &v0WireSite{Key: key, Frame: c.frame, OpConst: f.OpConst}
			}
			if !called {
				sites[f.Key] = f
			}
		}
	}
	// Attribution is per function: each frame must name one site.
	frames := map[string]string{}
	for key, s := range sites {
		if prev, ok := frames[s.Frame]; ok && prev != key {
			t.Fatalf("%s and %s both attribute to frame %s: the census attributes a request to the function on the stack, so give each literal its own function", prev, key, s.Frame)
		}
		frames[s.Frame] = key
	}
	return sites
}

func v0WireFuncNames(pkg string, fd *ast.FuncDecl) (display, frame string) {
	name := fd.Name.Name
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return name, pkg + "." + name
	}
	switch rt := fd.Recv.List[0].Type.(type) {
	case *ast.StarExpr:
		recv := v0WireExprText(rt.X)
		return "(*" + recv + ")." + name, pkg + ".(*" + recv + ")." + name
	default:
		recv := v0WireExprText(rt)
		return "(" + recv + ")." + name, pkg + "." + recv + "." + name
	}
}

func v0WireExprText(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return v0WireExprText(x.X) + "." + x.Sel.Name
	case *ast.StarExpr:
		return "*" + v0WireExprText(x.X)
	}
	return fmt.Sprintf("%T", e)
}

// v0WireOpConstants reads wire/paths.go's Op* constants: name -> operationId.
func v0WireOpConstants(t *testing.T) map[string]string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join("wire", "paths.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse wire/paths.go: %v", err)
	}
	out := map[string]string{}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "Op") || i >= len(vs.Values) {
					continue
				}
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					out[name.Name], _ = strconv.Unquote(lit.Value)
				}
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// The published route table, the recorder's oracle for which operation a
// request addressed.

type v0WireRoute struct {
	method, template, op string
	re                   *regexp.Regexp
}

var v0WireParamRe = regexp.MustCompile(`\{[^}]+\}`)

func v0WireRoutes(t *testing.T) []v0WireRoute {
	t.Helper()
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `yaml:"operationId"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(spec.OpenAPIV0(), &doc); err != nil {
		t.Fatalf("parse the wire contract: %v", err)
	}
	var routes []v0WireRoute
	for template, methods := range doc.Paths {
		parts := v0WireParamRe.Split(template, -1)
		for i := range parts {
			parts[i] = regexp.QuoteMeta(parts[i])
		}
		// A path argument is one escaped segment: escapeSegment leaves no
		// literal '/' or ':' in it, which is what keeps `{id}:claim` apart.
		re := regexp.MustCompile("^" + strings.Join(parts, `[^/:?#]+`) + "$")
		for method, op := range methods {
			routes = append(routes, v0WireRoute{method: strings.ToUpper(method), template: template, op: op.OperationID, re: re})
		}
	}
	return routes
}

func v0WireResolve(routes []v0WireRoute, method, rawPath string) (string, error) {
	var hits []string
	for _, r := range routes {
		if r.method == method && r.re.MatchString(rawPath) {
			hits = append(hits, r.op)
		}
	}
	if len(hits) != 1 {
		return "", fmt.Errorf("%s %s addresses %d published operations %v, want exactly one", method, rawPath, len(hits), hits)
	}
	return hits[0], nil
}

// ---------------------------------------------------------------------------
// The recorder.

type v0WireExchange struct {
	Op         string
	Method     string
	RequestURI string
	Header     http.Header
	Body       []byte
}

type v0WireRecorder struct {
	routes  []v0WireRoute
	answers map[string]string
	mu      sync.Mutex
	got     []v0WireExchange
	errs    []string
}

func (rec *v0WireRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	path, _, _ := strings.Cut(r.RequestURI, "?")
	op, rerr := v0WireResolve(rec.routes, r.Method, path)
	rec.mu.Lock()
	rec.got = append(rec.got, v0WireExchange{Op: op, Method: r.Method, RequestURI: r.RequestURI, Header: r.Header.Clone(), Body: body})
	if err != nil {
		rec.errs = append(rec.errs, fmt.Sprintf("reading the body of %s %s: %v", r.Method, r.RequestURI, err))
	}
	if rerr != nil {
		rec.errs = append(rec.errs, rerr.Error())
	}
	rec.mu.Unlock()
	if rerr != nil {
		http.Error(w, rerr.Error(), http.StatusNotFound)
		return
	}
	if op == "watchEvents" {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, ": v0 wire golden\n\n")
		return
	}
	answer, ok := rec.answers[op]
	if !ok {
		rec.mu.Lock()
		rec.errs = append(rec.errs, "no canned answer for "+op)
		rec.mu.Unlock()
		http.Error(w, "no canned answer", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, answer)
}

// v0WireContext is getContext's answer: the build's own capability list
// (httpapi.Capabilities, what a current bd serve advertises) less the tokens a
// row withholds to drive a fallback path.
func v0WireContext(t *testing.T, without []string) string {
	t.Helper()
	all := httpapi.Capabilities()
	caps := make([]string, 0, len(all))
	for _, c := range all {
		if !slices.Contains(without, c) {
			caps = append(caps, c)
		}
	}
	for _, w := range without {
		if !slices.Contains(all, w) {
			t.Fatalf("a row withholds %q, which httpapi.Capabilities() does not advertise", w)
		}
	}
	body, err := json.Marshal(map[string]any{
		"api_version": "v0", "backend": "dolt", "bd_version": v0WireBdVersion, "capabilities": caps,
		"database": "beads", "dolt_mode": "embedded", "min_client_wire_revision": wire.ClientWireRevision,
		"project_id": v0WireProjectID, "schema_version": 1, "wire_revision": wire.ClientWireRevision,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// ---------------------------------------------------------------------------
// Attribution: which literal site sent each request.
//
// A request is attributed to the innermost function on the sender's stack
// that is a site: one that holds a literal, or calls a factory of one. That is
// the literal that built the request everywhere but the stream door:
// wire.(*Client).dialStream builds watchEvents with http.NewRequestWithContext,
// and the census reaches it only through the Request{Op: OpWatchEvents} literal
// it holds for a refused connect. Moving that literal out of dialStream
// reports watchEvents as sent from no literal.

var v0WireClosureRe = regexp.MustCompile(`\.(func|gowrap|deferwrap)?\d+$`)

// v0WireEnclosing strips closure suffixes (".func1", ".func1.2", ".gowrap1")
// so a literal inside a func literal attributes to its FuncDecl.
func v0WireEnclosing(frame string) string {
	for {
		stripped := v0WireClosureRe.ReplaceAllString(frame, "")
		if stripped == frame {
			return frame
		}
		frame = stripped
	}
}

type v0WireAttributor struct {
	byFrame map[string]string // runtime frame name -> site key
	mu      sync.Mutex
	sites   []string // one per request, in send order; "" when no site sent it
}

func newV0WireAttributor(sites map[string]*v0WireSite) *v0WireAttributor {
	a := &v0WireAttributor{byFrame: map[string]string{}}
	for key, s := range sites {
		a.byFrame[s.Frame] = key
	}
	return a
}

// trace hooks GetConn, which net/http calls on the goroutine that called
// Client.Do, once per request, before any connection is chosen: the stack it
// sees is the role call's.
func (a *v0WireAttributor) trace(ctx context.Context) context.Context {
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GetConn: func(string) {
		pcs := make([]uintptr, 256)
		frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs)])
		site := ""
		for {
			fr, more := frames.Next()
			name := fr.Function
			switch {
			case strings.HasPrefix(name, v0WireModule+"/wire."):
				name = "wire." + strings.TrimPrefix(name, v0WireModule+"/wire.")
			case strings.HasPrefix(name, v0WireModule+"."):
				name = "httpclient." + strings.TrimPrefix(name, v0WireModule+".")
			default:
				name = ""
			}
			if key, ok := a.byFrame[v0WireEnclosing(name)]; ok && name != "" {
				site = key
				break
			}
			if !more {
				break
			}
		}
		a.mu.Lock()
		a.sites = append(a.sites, site)
		a.mu.Unlock()
	}})
}

// ---------------------------------------------------------------------------
// Running one row.

type v0WireCall struct {
	ctx   context.Context
	store *Store
	conn  *Conn
}

// v0WireMust unwraps a role accessor, which refuses only a Store with no
// transport: every row's Store has one.
func v0WireMust[T any](role T, err error) T {
	if err != nil {
		panic(fmt.Sprintf("role accessor on a dialed Store: %v", err))
	}
	return role
}

type v0WireRow struct {
	name        string            // "<operationId>" or "<operationId>.<variant>": the golden's file name
	withoutCaps []string          // capability tokens getContext withholds
	answers     map[string]string // per-operation answers that replace v0WireAnswers
	call        func(c v0WireCall) error
}

type v0WireResult struct {
	row       v0WireRow
	exchanges []v0WireExchange
	sites     []string
}

func v0WireIsolate(t *testing.T) {
	t.Helper()
	t.Setenv(TokenEnv, "127.0.0.1="+v0WireToken)
	t.Setenv(TokenCommandEnv, "")
	t.Setenv(CAFileEnv, "")
	t.Setenv(AllowInsecureCredentialEnv, "")
	t.Setenv(ResponseHeaderTimeoutEnv, "")
	t.Setenv("BEADS_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "no-credentials"))
}

func runV0WireRow(t *testing.T, sites map[string]*v0WireSite, routes []v0WireRoute, row v0WireRow) v0WireResult {
	t.Helper()
	answers := map[string]string{"getContext": v0WireContext(t, row.withoutCaps)}
	for op, body := range v0WireAnswers {
		answers[op] = body
	}
	for op, body := range row.answers {
		answers[op] = body
	}
	rec := &v0WireRecorder{routes: routes, answers: answers}
	srv := httptest.NewUnstartedServer(rec)
	// One connection per request: a pooled connection to a server an earlier
	// row closed would be retried by net/http and seen twice by the trace.
	srv.Config.SetKeepAlivesEnabled(false)
	srv.Start()
	defer srv.Close()
	if tr, ok := baselineTransport().(*http.Transport); ok {
		tr.CloseIdleConnections()
	}

	beadsDir := t.TempDir()
	if err := SaveTarget(beadsDir, Target{BaseURL: mustParseURL(t, srv.URL), ExpectProjectID: v0WireProjectID}); err != nil {
		t.Fatalf("%s: SaveTarget: %v", row.name, err)
	}
	target, err := LoadTarget(beadsDir)
	if err != nil {
		t.Fatalf("%s: LoadTarget: %v", row.name, err)
	}
	conn, err := Dial(target, DialOptions{})
	if err != nil {
		t.Fatalf("%s: Dial: %v", row.name, err)
	}
	attr := newV0WireAttributor(sites)
	if err := row.call(v0WireCall{ctx: attr.trace(context.Background()), store: New(target, conn, nil), conn: conn}); err != nil {
		t.Errorf("%s: the role call failed: %v", row.name, err)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, e := range rec.errs {
		t.Errorf("%s: %s", row.name, e)
	}
	attr.mu.Lock()
	defer attr.mu.Unlock()
	return v0WireResult{row: row, exchanges: rec.got, sites: attr.sites}
}

// ---------------------------------------------------------------------------
// Rendering and comparison.

var v0WireVersionRe = regexp.MustCompile(`\d+\.\d+\.\d+[0-9A-Za-z.+-]*`)

func renderV0WireTranscript(ex []v0WireExchange) string {
	var b strings.Builder
	for i, e := range ex {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "=== request %d of %d: %s\n", i+1, len(ex), e.Op)
		path, query, _ := strings.Cut(e.RequestURI, "?")
		fmt.Fprintf(&b, "%s %s\n", e.Method, path)
		if query != "" {
			for _, kv := range strings.Split(query, "&") {
				fmt.Fprintf(&b, "  ?%s\n", kv)
			}
		}
		keys := make([]string, 0, len(e.Header))
		for k := range e.Header {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			for _, v := range e.Header[k] {
				switch k {
				case "Authorization":
					if v == "Bearer "+v0WireToken {
						v = v0WireTokenRendered
					}
				case "User-Agent":
					v = v0WireVersionRe.ReplaceAllString(v, "<version>")
				}
				fmt.Fprintf(&b, "%s: %s\n", k, v)
			}
		}
		if len(e.Body) > 0 {
			b.WriteString("\n")
			var indented bytes.Buffer
			if json.Indent(&indented, e.Body, "", "  ") == nil {
				b.Write(indented.Bytes())
				b.WriteString("\n")
			} else {
				fmt.Fprintf(&b, "(not JSON) %q\n", e.Body)
			}
		}
	}
	return b.String()
}

// v0WireUnifiedDiff is a unified line diff of want (the golden) and got (what
// was sent) with three lines of context, or "" when they are equal.
func v0WireUnifiedDiff(name, want, got string) string {
	if want == got {
		return ""
	}
	split := func(s string) []string {
		lines := strings.SplitAfter(s, "\n")
		if lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		return lines
	}
	a, b := split(want), split(got)
	// lcs[i][j] is the length of the longest common subsequence of a[i:], b[j:].
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	type line struct {
		op     byte
		text   string
		ai, bi int // 0-based positions in a and b this line occupies or follows
	}
	var lines []line
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			lines = append(lines, line{' ', a[i], i, j})
			i, j = i+1, j+1
		case i < len(a) && (j == len(b) || lcs[i+1][j] >= lcs[i][j+1]):
			lines = append(lines, line{'-', a[i], i, j})
			i++
		default:
			lines = append(lines, line{'+', b[j], i, j})
			j++
		}
	}
	const context = 3
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s (golden)\n+++ %s (sent)\n", name, name)
	for k := 0; k < len(lines); {
		if lines[k].op == ' ' {
			k++
			continue
		}
		start, end := max(0, k-context), k
		for end < len(lines) {
			if lines[end].op != ' ' {
				end++
				continue
			}
			run := end
			for run < len(lines) && lines[run].op == ' ' {
				run++
			}
			if run == len(lines) || run-end > 2*context {
				end = min(len(lines), end+context)
				break
			}
			end = run
		}
		var aCount, bCount int
		for _, l := range lines[start:end] {
			if l.op != '+' {
				aCount++
			}
			if l.op != '-' {
				bCount++
			}
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", lines[start].ai+1, aCount, lines[start].bi+1, bCount)
		for _, l := range lines[start:end] {
			out.WriteByte(l.op)
			out.WriteString(strings.TrimSuffix(l.text, "\n"))
			out.WriteByte('\n')
		}
		k = end
	}
	return out.String()
}

// ---------------------------------------------------------------------------
// The census.

// v0WireCensusWaivers names an Op constant or a literal site no row can drive,
// with the reason. Shrink-only: v0WireCensusWaiverCeiling is its size today.
var v0WireCensusWaivers = map[string]string{}

const v0WireCensusWaiverCeiling = 0

func v0WireCensus(sites map[string]*v0WireSite, ops map[string]string, waivers map[string]string, results []v0WireResult) []string {
	var problems []string
	exercisedSites := map[string]bool{}
	exercisedOps := map[string]bool{}
	for _, r := range results {
		for i, key := range r.sites {
			if key == "" {
				problems = append(problems, fmt.Sprintf("%s: request %d was sent from no wire.Request literal the census knows: no function on its stack holds one or calls a factory of one (a new construction pattern, or a literal moved out of the function that sends?)", r.row.name, i+1))
				continue
			}
			exercisedSites[key] = true
		}
		for _, e := range r.exchanges {
			exercisedOps[e.Op] = true
		}
	}
	for key := range sites {
		_, waived := waivers[key]
		switch {
		case exercisedSites[key] && waived:
			problems = append(problems, fmt.Sprintf("site %s is waived but a row drives it: delete the waiver", key))
		case !exercisedSites[key] && !waived:
			problems = append(problems, fmt.Sprintf("site %s (Op %s) is driven by no row: add one to v0WireRows", key, sites[key].OpConst))
		}
	}
	for name, op := range ops {
		_, waived := waivers[name]
		switch {
		case exercisedOps[op] && waived:
			problems = append(problems, fmt.Sprintf("%s is waived but a row sends %s: delete the waiver", name, op))
		case !exercisedOps[op] && !waived:
			problems = append(problems, fmt.Sprintf("%s (%s) is sent by no row: add one to v0WireRows", name, op))
		}
	}
	for key, reason := range waivers {
		if _, isSite := sites[key]; !isSite && ops[key] == "" {
			problems = append(problems, fmt.Sprintf("waiver %q names no literal site and no Op constant", key))
		}
		if strings.TrimSpace(reason) == "" {
			problems = append(problems, fmt.Sprintf("waiver %q gives no reason", key))
		}
	}
	if len(waivers) > v0WireCensusWaiverCeiling {
		problems = append(problems, fmt.Sprintf("%d census waivers, ceiling %d: waivers only shrink", len(waivers), v0WireCensusWaiverCeiling))
	}
	sort.Strings(problems)
	return problems
}

// v0WireOpsAgree reports requests whose literal site names a different
// operation than the published route the request reached.
func v0WireOpsAgree(sites map[string]*v0WireSite, ops map[string]string, results []v0WireResult) []string {
	var problems []string
	for _, r := range results {
		if len(r.sites) != len(r.exchanges) {
			problems = append(problems, fmt.Sprintf("%s: the client sent %d requests and the recorder saw %d (a retried attempt?)", r.row.name, len(r.sites), len(r.exchanges)))
			continue
		}
		for i, e := range r.exchanges {
			s := sites[r.sites[i]]
			if s == nil {
				continue
			}
			if want := ops[s.OpConst]; want != e.Op {
				problems = append(problems, fmt.Sprintf("%s: request %d came from %s (Op %s = %q) but reached %s %s, operation %q",
					r.row.name, i+1, s.Key, s.OpConst, want, e.Method, e.RequestURI, e.Op))
			}
		}
		op, _, _ := strings.Cut(r.row.name, ".")
		if !slices.ContainsFunc(r.exchanges, func(e v0WireExchange) bool { return e.Op == op }) {
			problems = append(problems, fmt.Sprintf("%s: the row is named for %s, which it never sends", r.row.name, op))
		}
	}
	return problems
}

// ---------------------------------------------------------------------------
// The tests.

func runV0WireTable(t *testing.T) (map[string]*v0WireSite, map[string]string, []v0WireResult) {
	t.Helper()
	v0WireIsolate(t)
	sites, ops, routes := v0WireLiteralSites(t), v0WireOpConstants(t), v0WireRoutes(t)
	var results []v0WireResult
	seen := map[string]bool{}
	for _, row := range v0WireRows() {
		if seen[row.name] {
			t.Fatalf("two rows are named %s", row.name)
		}
		seen[row.name] = true
		results = append(results, runV0WireRow(t, sites, routes, row))
	}
	return sites, ops, results
}

func TestV0ClientRequestsAreFrozen(t *testing.T) {
	sites, _, results := runV0WireTable(t)
	routes := v0WireRoutes(t)
	update := os.Getenv("BD_UPDATE_GOLDEN") == "1"
	want := map[string]bool{}
	for _, r := range results {
		file := r.row.name + ".golden"
		want[file] = true
		got := renderV0WireTranscript(r.exchanges)
		// A rendering that depended on map order would pass the run that
		// wrote the golden and flake afterwards.
		if again := renderV0WireTranscript(runV0WireRow(t, sites, routes, r.row).exchanges); again != got {
			t.Errorf("%s: two runs sent different requests:\n%s", r.row.name, v0WireUnifiedDiff(file, got, again))
			continue
		}
		path := filepath.Join(v0WireGoldenDir, file)
		if update {
			if err := os.MkdirAll(v0WireGoldenDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		golden, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: read %s (regenerate with %s): %v", r.row.name, path, v0WireGoldenRegen, err)
			continue
		}
		if diff := v0WireUnifiedDiff(path, string(golden), got); diff != "" {
			t.Errorf("the v0 client sent different requests for %s.\n%s\nIf the change is intended, regenerate with\n  %s\nand review the testdata diff.", r.row.name, diff, v0WireGoldenRegen)
		}
	}
	entries, err := os.ReadDir(v0WireGoldenDir)
	if err != nil {
		t.Fatalf("read %s: %v", v0WireGoldenDir, err)
	}
	for _, e := range entries {
		if want[e.Name()] {
			continue
		}
		if update {
			_ = os.Remove(filepath.Join(v0WireGoldenDir, e.Name()))
			continue
		}
		t.Errorf("%s/%s belongs to no row of v0WireRows; delete it or regenerate with %s", v0WireGoldenDir, e.Name(), v0WireGoldenRegen)
	}
}

func TestEveryWireOpHasAGoldenRow(t *testing.T) {
	sites, ops, results := runV0WireTable(t)
	if len(ops) == 0 || len(sites) == 0 {
		t.Fatalf("the census read %d Op constants and %d literal sites; the scan is broken", len(ops), len(sites))
	}
	published := map[string]bool{}
	for _, r := range v0WireRoutes(t) {
		published[r.op] = true
	}
	for name, op := range ops {
		if !published[op] {
			t.Errorf("wire.%s = %q names no operation openapi.v0.yaml publishes", name, op)
		}
	}
	for _, p := range v0WireCensus(sites, ops, v0WireCensusWaivers, results) {
		t.Error(p)
	}
	for _, p := range v0WireOpsAgree(sites, ops, results) {
		t.Error(p)
	}
	t.Logf("%d rows cover %d Op constants and %d wire.Request literal sites", len(results), len(ops), len(sites))
}

// TestV0WireGoldenCatchesADrift is the golden's own falsification.
func TestV0WireGoldenCatchesADrift(t *testing.T) {
	sites, ops, results := runV0WireTable(t)
	byName := map[string]v0WireResult{}
	for _, r := range results {
		byName[r.row.name] = r
	}
	for _, tc := range []struct {
		name, row, want string
		mutate          func(e *v0WireExchange)
	}{
		{"an extra header", "updateIssue", "+X-Bd-Extra: 1", func(e *v0WireExchange) { e.Header.Set("X-Bd-Extra", "1") }},
		{"a renamed body member", "updateIssue", "+  \"expectedVersion\": \"7\"", func(e *v0WireExchange) {
			e.Body = bytes.Replace(e.Body, []byte(`"expected_version"`), []byte(`"expectedVersion"`), 1)
		}},
		{"a path argument leaking into the query", "claimIssue", "+  ?id=bd-1", func(e *v0WireExchange) { e.RequestURI += "?id=bd-1" }},
		{"a changed path", "closeIssue", "+POST /bdp/call/issue/close", func(e *v0WireExchange) { e.RequestURI = "/bdp/call/issue/close" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, ok := byName[tc.row]
			if !ok {
				t.Fatalf("no row %s", tc.row)
			}
			path := filepath.Join(v0WireGoldenDir, tc.row+".golden")
			golden, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if diff := v0WireUnifiedDiff(path, string(golden), renderV0WireTranscript(r.exchanges)); diff != "" {
				t.Fatalf("the honest transcript already differs from the golden:\n%s", diff)
			}
			drifted := slices.Clone(r.exchanges)
			last := &drifted[len(drifted)-1]
			last.Header, last.Body = last.Header.Clone(), bytes.Clone(last.Body)
			tc.mutate(last)
			diff := v0WireUnifiedDiff(path, string(golden), renderV0WireTranscript(drifted))
			if !strings.Contains(diff, tc.want+"\n") {
				t.Fatalf("%s produced no diff line %q:\n%s", tc.name, tc.want, diff)
			}
			t.Logf("%s:\n%s", tc.name, diff)
		})
	}
	for _, tc := range []struct {
		name, row string
		want      []string
	}{
		{"a deleted row", "releaseIssue", []string{"site wire/writes.go:(*Client).ReleaseIssue (Op OpReleaseIssue) is driven by no row", "OpReleaseIssue (releaseIssue) is sent by no row"}},
		// listIssues has three senders through the listRequest factory, and
		// other rows still send the operation: what goes missing is the
		// default `bd list` request, which only this row drives.
		{"a deleted row behind a factory", "listIssues", []string{"site list_walk.go:listRequest@(*Store).sortedPage (Op OpListIssues) is driven by no row"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var kept []v0WireResult
			for _, r := range results {
				if r.row.name != tc.row {
					kept = append(kept, r)
				}
			}
			got := strings.Join(v0WireCensus(sites, ops, nil, kept), "\n")
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("dropping the %s row: the census lacks %q:\n%s", tc.row, want, got)
				}
			}
		})
	}
	t.Run("a request from no literal site", func(t *testing.T) {
		r := runV0WireRow(t, sites, v0WireRoutes(t), v0WireRow{name: "getIssue.unattributed", call: func(c v0WireCall) error {
			return c.conn.Do(c.ctx, wire.Request{Op: wire.OpGetIssue, Method: http.MethodGet, Path: "/v0/beads/issues/bd-1"}, nil)
		}})
		got := strings.Join(v0WireCensus(sites, ops, nil, append(slices.Clone(results), r)), "\n")
		if want := "getIssue.unattributed: request 1 was sent from no wire.Request literal"; !strings.Contains(got, want) {
			t.Errorf("a request the test itself built: the census lacks %q:\n%s", want, got)
		}
	})
	t.Run("a site that names another operation", func(t *testing.T) {
		claim := byName["claimIssue"]
		forged := v0WireResult{row: claim.row, exchanges: slices.Clone(claim.exchanges), sites: slices.Clone(claim.sites)}
		forged.sites[len(forged.sites)-1] = "wire/writes.go:(*Client).CloseIssue"
		got := strings.Join(v0WireOpsAgree(sites, ops, []v0WireResult{forged}), "\n")
		if want := "came from wire/writes.go:(*Client).CloseIssue (Op OpCloseIssue = \"closeIssue\") but reached POST /v0/beads/issues/bd-1:claim"; !strings.Contains(got, want) {
			t.Errorf("the op cross-check lacks %q:\n%s", want, got)
		}
	})
	t.Run("waivers", func(t *testing.T) {
		got := strings.Join(v0WireCensus(sites, ops, map[string]string{
			"wire/writes.go:(*Client).ClaimIssue": "driven anyway",
			"nowhere.go:gone":                     "stale",
			"OpClaimIssue":                        "",
		}, results), "\n")
		for _, want := range []string{
			"site wire/writes.go:(*Client).ClaimIssue is waived but a row drives it",
			"waiver \"nowhere.go:gone\" names no literal site and no Op constant",
			"waiver \"OpClaimIssue\" gives no reason",
			"3 census waivers, ceiling 0",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("the census lacks %q:\n%s", want, got)
			}
		}
	})
}
