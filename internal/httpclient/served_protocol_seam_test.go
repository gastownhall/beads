package httpclient

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/httpapi"
	"github.com/steveyegge/beads/internal/httpclient/wire"
)

// The served tier's PROTOCOL is a dimension of the tier, the same way its
// roles are: every served case builds one client and (unless it shares the
// composition) one server, and which wire protocol those two speak decides what
// the case actually proves. On main the tier speaks and serves v0 only. The BDP
// client-transport slice adds a second protocol, and a case that dials or
// serves AROUND a single switch would keep testing v0 while the tier claims to
// test BDP, passing for the wrong reason.
//
// This file makes that switch a seam before there is anything to switch:
//
//   - servedWireOptions and servedServerConfig are the ONLY places a served
//     client or server learns the tier's protocol. Every served wire.New takes
//     its options from servedWireOptions; every served httpapi.Listen config
//     passes through servedServerConfig first.
//   - A wire witness wraps each helper-built client's transport and checks what
//     was actually SENT, so the seam is proved on the wire and not just in the
//     source: each client is judged at its test's cleanup, the shared
//     composition once in TestMain after the last case. Under v0 nothing may
//     reach BDP's /bdp mount.
//   - TestServedTierDialsThroughTheProtocolSwitch scans the served sources and
//     fails any served test that dials or serves around the seam, unless the
//     site is declared in servedDialsOutsideTheSwitch with its reason.
//
// Turning the dimension on is the BDP client-transport slice's job, and it is
// meant to touch only this file: set opts.Protocol in servedWireOptions and
// cfg.Protocol in servedServerConfig from its switch, replace
// servedProtocolForbids' v0 rule with the protocol-aware one, and declare its
// deliberately-BDP served dials in servedDialsOutsideTheSwitch.
//
// It is deliberately UNTAGGED, for the reason divergence_citation_gate_test.go
// gives: the served harness lives behind //go:build cgo, but the scan reads it
// as TEXT and the falsifications need no embedded engine, so all of it runs in
// the plain test lane, where a gate carrying the cgo tag would go blind.

// servedWireOptions is where every served wire.New takes its options.
//
// On main the tier speaks v0 only, so the one thing it changes is the transport:
// it installs w's recorder in front of whatever transport the caller chose, on a
// COPY of the caller's client. A nil HTTPClient becomes exactly what wire.New
// would have built for one (wire/client.go: &http.Client{Timeout:
// DefaultTimeout} over the default transport), so a served client still runs on
// production's defaults; TestServedWireOptionsChangesOnlyTheTransport pins that.
// The BDP client-transport slice sets opts.Protocol here.
func servedWireOptions(w *servedWitness, opts wire.Options) wire.Options {
	if w == nil {
		panic("servedWireOptions: a served dial needs a witness (newServedWitness(t, ...) or compositionWitness)")
	}
	hc := &http.Client{Timeout: wire.DefaultTimeout}
	if opts.HTTPClient != nil {
		clone := *opts.HTTPClient // never mutate the caller's client
		hc = &clone
	}
	next := hc.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	hc.Transport = servedRecorder{witness: w, next: next}
	opts.HTTPClient = hc
	return opts
}

// servedServerConfig is where every served httpapi.Listen config learns the
// tier's protocol. It runs after the config's source is bound and BEFORE a
// case's own options, so a case can still pin its own protocol (the landing
// sets cfg.Protocol in the same place). On main v0 is the zero Config —
// httpapi.Config has no Protocol field — so it sets nothing; the BDP
// client-transport slice sets cfg.Protocol here.
func servedServerConfig(cfg *httpapi.Config) {
	_ = cfg
}

// servedWitness records what one served client sent and judges it against the
// tier's protocol. Always use it through a pointer.
type servedWitness struct {
	subject string

	mu          sync.Mutex
	sent        int
	forbidden   int
	firstReason string
	// offending is every distinct forbidden "METHOD PATH", in first-seen
	// order; counts tallies each.
	offending []string
	counts    map[string]int
}

// servedWitnessListed bounds how many distinct offending requests a report names.
const servedWitnessListed = 10

func (w *servedWitness) record(method, path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sent++
	reason := servedProtocolForbids(method, path)
	if reason == "" {
		return
	}
	w.forbidden++
	if w.firstReason == "" {
		w.firstReason = reason
	}
	line := method + " " + path
	if w.counts == nil {
		w.counts = map[string]int{}
	}
	if _, seen := w.counts[line]; !seen {
		w.offending = append(w.offending, line)
	}
	w.counts[line]++
}

// report is "" when the client sent nothing the tier's protocol forbids, and
// otherwise names the subject and the offending requests.
//
// There is deliberately no non-vacuity check here: some cases legitimately send
// nothing through their witnessed client (a park can skip right after
// composition(t); TestServedUpdateClaimsAWisp's traffic rides the declared v0
// client). That the recorder sits in the request path at all is what
// TestTheServedWitnessCatchesABDPRequestUnderV0 proves.
func (w *servedWitness) report() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.forbidden == 0 {
		return ""
	}
	listed := make([]string, 0, servedWitnessListed)
	for i, line := range w.offending {
		if i == servedWitnessListed {
			break
		}
		listed = append(listed, fmt.Sprintf("%s (x%d)", line, w.counts[line]))
	}
	msg := fmt.Sprintf("served-tier wire witness: %s sent %d of %d request(s) the tier's protocol forbids (%s): %s",
		w.subject, w.forbidden, w.sent, w.firstReason, strings.Join(listed, ", "))
	if more := len(w.offending) - servedWitnessListed; more > 0 {
		msg += fmt.Sprintf(" [and %d more]", more)
	}
	return msg
}

// servedRecorder is the witness's transport: it records each request's method
// and path and hands the request, untouched, to next. It never reads the body.
type servedRecorder struct {
	witness *servedWitness
	next    http.RoundTripper
}

func (r servedRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.witness.record(req.Method, req.URL.Path)
	return r.next.RoundTrip(req)
}

// servedProtocolForbids is the tier's protocol rule: why a request may not be
// sent, or "" if it may. On main the tier runs v0, which sends nothing to BDP's
// /bdp mount. method is unused under v0; the BDP slice's rule (only POST
// /bdp/call/* and the declared plain ops) needs it, and replaces this body.
func servedProtocolForbids(method, path string) string {
	_ = method
	if strings.HasPrefix(path, "/bdp") {
		return "the tier runs v0, which sends nothing to BDP's /bdp mount"
	}
	return ""
}

// newServedWitness is a witness judged at t's cleanup. Register it before the
// server's cleanups so it runs last, after shutdown.
func newServedWitness(t testing.TB, subject string) *servedWitness {
	t.Helper()
	w := &servedWitness{subject: subject}
	t.Cleanup(func() {
		if r := w.report(); r != "" {
			t.Error(r)
		}
	})
	return w
}

// compositionWitness witnesses the shared composition's client. The composition
// outlives every case, so TestMain judges it once, after the last one.
var compositionWitness = &servedWitness{subject: "the shared served composition"}

// servedDialSite names one served dial: the file's base name, the enclosing
// top-level declaration (a method by its name, without the receiver; "" for a
// var/const block), and the call ("wire.New" or the dialer's identifier).
type servedDialSite struct {
	File string
	Func string
	Call string
}

// servedDialsOutsideTheSwitch are the served dials that deliberately do not go
// through servedWireOptions, each with why. On main every entry is v0-only or
// the product path; a deliberately-BDP served dial (e.g. the fork's
// served_callables_test.go, which dials with Protocol: wire.ProtocolBDP) is
// declared the same way. Each entry must match exactly one dial.
var servedDialsOutsideTheSwitch = map[servedDialSite]string{
	{"served_claim_lifecycle_test.go", "updateClaimSubject", "wire.New"}: "v0-only by design: predatesUpdateClaimTransport plays a bd serve from before #6890 " +
		"by reading v0 requests — it counts POST …/{id}:claim and answers a PATCH whose body carries `claim` with the skew 400 — " +
		"and a client on another protocol sends neither shape (on BDP, TestClaimOnlyUpdateRefusesAWispByNameOnAServerThatPredatesUpdateClaim " +
		"and TestClaimOnlyUpdateHydratesLabelsAndCreatedByForOutputParity fail)",
	{"served_large_apply_timeout_test.go", "TestServedLargeApplyOutlivesTheDefaultClientTimeout", "DialWith"}: "the product dial path by design: " +
		"the case proves the production transport and wire.DefaultTimeout carry a large apply, and a harness-built client would replace exactly that; " +
		"a protocol reaches this dial through the Target the product path reads, not through wire.Options",
}

// servedTierEntryPoints are how a test reaches the served tier. They may be used
// only from served files, so the scan's notion of "served" cannot be dodged by
// putting a served case in a file with another name.
var servedTierEntryPoints = []string{"newServedEnv", "newServedEnvExpecting", "composition", "serveOneProject", "servedEnv", "servedComposition"}

const (
	seamWireImport    = "github.com/steveyegge/beads/internal/httpclient/wire"
	seamHTTPAPIImport = "github.com/steveyegge/beads/internal/httpapi"
)

// servedDialScan is what scanServedDials found.
type servedDialScan struct {
	servedFiles     int
	routedDials     int
	switchedServers int
	dialers         map[string]bool
	violations      []string
}

func isServedFile(name string) bool {
	return strings.HasPrefix(name, "served") && strings.HasSuffix(name, "_test.go")
}

// seamImportName is the name file refers to path by: its alias, or the path's
// last element. A blank or dot import counts as not imported ("").
func seamImportName(file *ast.File, path string) string {
	for _, imp := range file.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != path {
			continue
		}
		if imp.Name != nil {
			if imp.Name.Name == "_" || imp.Name.Name == "." {
				return ""
			}
			return imp.Name.Name
		}
		return path[strings.LastIndex(path, "/")+1:]
	}
	return ""
}

// seamCallee is a call's (qualifier, name): ("", f) for f(...), (pkg, F) for
// pkg.F(...), seeing through parentheses and generic instantiations.
func seamCallee(fun ast.Expr) (string, string) {
	for {
		switch e := fun.(type) {
		case *ast.ParenExpr:
			fun = e.X
		case *ast.IndexExpr:
			fun = e.X
		case *ast.IndexListExpr:
			fun = e.X
		case *ast.Ident:
			return "", e.Name
		case *ast.SelectorExpr:
			if x, ok := e.X.(*ast.Ident); ok {
				return x.Name, e.Sel.Name
			}
			return "", ""
		default:
			return "", ""
		}
	}
}

func seamIsCall(call *ast.CallExpr, qual, name string) bool {
	if name == "" {
		return false
	}
	q, n := seamCallee(call.Fun)
	return q == qual && n == name
}

// seamRawWireNew reports whether call is a wire.New whose options are not a
// DIRECT servedWireOptions call (a variable holding its result does not count).
func seamRawWireNew(call *ast.CallExpr, wireName string) (isNew, raw bool) {
	if wireName == "" || !seamIsCall(call, wireName, "New") {
		return false, false
	}
	if len(call.Args) >= 3 {
		if opts, ok := call.Args[2].(*ast.CallExpr); ok && seamIsCall(opts, "", "servedWireOptions") {
			return true, false
		}
	}
	return true, true
}

func seamDeclName(decl ast.Decl) string {
	if fd, ok := decl.(*ast.FuncDecl); ok {
		return fd.Name.Name
	}
	return ""
}

func seamSortedNames(files map[string]*ast.File) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// seamDialers is every package-level function in a NON-served file that dials:
// a fixpoint seeded by bodies with a raw wire.New or a dialShared call, grown
// through calls to members. dialShared itself is a member: open() dials every
// registered WireDialer through it as a function value the fixpoint cannot
// follow. Served files are excluded so a raw site is reported once, at its line.
func seamDialers(files map[string]*ast.File) map[string]bool {
	type candidate struct {
		body  *ast.BlockStmt
		wire  string
		dials bool
	}
	cands := map[string]*candidate{}
	for _, name := range seamSortedNames(files) {
		if isServedFile(name) {
			continue
		}
		file := files[name]
		wireName := seamImportName(file, seamWireImport)
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || fd.Body == nil {
				continue
			}
			n := fd.Name.Name
			if strings.HasPrefix(n, "Test") || strings.HasPrefix(n, "Benchmark") ||
				strings.HasPrefix(n, "Fuzz") || strings.HasPrefix(n, "Example") {
				continue
			}
			cands[n] = &candidate{body: fd.Body, wire: wireName}
		}
	}
	dialers := map[string]bool{"dialShared": true}
	for changed := true; changed; {
		changed = false
		for name, c := range cands {
			if dialers[name] {
				continue
			}
			found := false
			ast.Inspect(c.body, func(n ast.Node) bool {
				if found {
					return false
				}
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if _, raw := seamRawWireNew(call, c.wire); raw {
					found = true
					return false
				}
				if q, callee := seamCallee(call.Fun); q == "" && dialers[callee] {
					found = true
					return false
				}
				return true
			})
			if found {
				dialers[name] = true
				changed = true
			}
		}
	}
	return dialers
}

// scanServedDials holds every served file to the seam. It is a pure function
// over parsed files so the falsification can drive it with synthetic packages.
//
//   - R1: every wire.New in a served file takes a DIRECT servedWireOptions(...)
//     call as its options, or is declared.
//   - R2: a served file's call to a dialer (seamDialers) is declared.
//   - R3: every httpapi.Listen(cfg) in a served file follows a
//     servedServerConfig(&cfg) in the same top-level declaration.
//   - R4: httpapi.Listen appears in no other _test.go file.
//   - R5: no other _test.go file references a served-tier entry point, and each
//     entry point is still declared in some served file.
//   - R6: each declared site matches exactly one dial, with a reason.
func scanServedDials(fset *token.FileSet, files map[string]*ast.File, declared map[servedDialSite]string) servedDialScan {
	s := servedDialScan{dialers: seamDialers(files)}
	matched := map[servedDialSite]int{}
	served := map[string]bool{}
	where := func(pos token.Pos, fn string) string {
		p := fset.Position(pos)
		loc := filepath.Base(p.Filename) + ":" + strconv.Itoa(p.Line)
		if fn != "" {
			loc += " (" + fn + ")"
		}
		return loc
	}
	dial := func(file, fn, call string, pos token.Pos, what string) {
		site := servedDialSite{File: file, Func: fn, Call: call}
		if _, ok := declared[site]; ok {
			matched[site]++
			return
		}
		s.violations = append(s.violations, fmt.Sprintf(
			"%s: %s — route it through servedWireOptions(newServedWitness(t, ...), ...), or declare servedDialSite{%q, %q, %q} in servedDialsOutsideTheSwitch with why it must dial around the tier's protocol",
			where(pos, fn), what, file, fn, call))
	}

	for _, name := range seamSortedNames(files) {
		file := files[name]
		wireName := seamImportName(file, seamWireImport)
		apiName := seamImportName(file, seamHTTPAPIImport)
		isTest := strings.HasSuffix(name, "_test.go")

		if !isServedFile(name) {
			if !isTest {
				continue
			}
			for _, decl := range file.Decls {
				fn := seamDeclName(decl)
				// R4.
				ast.Inspect(decl, func(n ast.Node) bool {
					if call, ok := n.(*ast.CallExpr); ok && seamIsCall(call, apiName, "Listen") {
						s.violations = append(s.violations, fmt.Sprintf(
							"%s: httpapi.Listen outside a served file — a served server belongs in a served*_test.go file, behind servedServerConfig",
							where(call.Pos(), fn)))
					}
					return true
				})
				// R5. A selector's Sel names a field or method, not the entry
				// point, so only its X is visited.
				var visit func(ast.Node) bool
				visit = func(n ast.Node) bool {
					switch e := n.(type) {
					case *ast.SelectorExpr:
						ast.Inspect(e.X, visit)
						return false
					case *ast.Ident:
						if slices.Contains(servedTierEntryPoints, e.Name) {
							s.violations = append(s.violations, fmt.Sprintf(
								"%s: references served-tier entry point %s outside a served file — move the case into a served*_test.go file so the protocol seam scan sees it",
								where(e.Pos(), fn), e.Name))
						}
					}
					return true
				}
				ast.Inspect(decl, visit)
			}
			continue
		}

		s.servedFiles++
		for _, decl := range file.Decls {
			fn := seamDeclName(decl)
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					served[d.Name.Name] = true
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok {
						served[ts.Name.Name] = true
					}
				}
			}
			// servedServerConfig(&X) calls in this declaration, by position.
			switched := map[string][]token.Pos{}
			ast.Inspect(decl, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !seamIsCall(call, "", "servedServerConfig") || len(call.Args) != 1 {
					return true
				}
				if u, ok := call.Args[0].(*ast.UnaryExpr); ok && u.Op == token.AND {
					if id, ok := u.X.(*ast.Ident); ok {
						switched[id.Name] = append(switched[id.Name], call.Pos())
					}
				}
				return true
			})
			ast.Inspect(decl, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				// R1.
				if isNew, raw := seamRawWireNew(call, wireName); isNew {
					if raw {
						dial(name, fn, "wire.New", call.Pos(), "wire.New dials around servedWireOptions")
					} else {
						s.routedDials++
					}
					return true
				}
				// R3.
				if seamIsCall(call, apiName, "Listen") {
					var cfg *ast.Ident
					if len(call.Args) == 1 {
						cfg, _ = call.Args[0].(*ast.Ident)
					}
					ok := cfg != nil && slices.ContainsFunc(switched[cfg.Name], func(p token.Pos) bool { return p < call.Pos() })
					if ok {
						s.switchedServers++
					} else {
						s.violations = append(s.violations, fmt.Sprintf(
							"%s: httpapi.Listen's config did not go through servedServerConfig first — bind it to a variable cfg and call servedServerConfig(&cfg) before the case's own options and the Listen",
							where(call.Pos(), fn)))
					}
					return true
				}
				// R2.
				if q, callee := seamCallee(call.Fun); q == "" && s.dialers[callee] {
					dial(name, fn, callee, call.Pos(), callee+" dials around the served tier's protocol seam")
				}
				return true
			})
		}
	}

	// R5, the other half: a renamed entry point must fail loudly rather than
	// leave the reference check guarding a name nothing uses.
	for _, ep := range servedTierEntryPoints {
		if !served[ep] {
			s.violations = append(s.violations, fmt.Sprintf(
				"served-tier entry point %s is no longer declared in any served file — update servedTierEntryPoints to its new name", ep))
		}
	}
	// R6.
	for site, reason := range declared {
		if n := matched[site]; n != 1 {
			s.violations = append(s.violations, fmt.Sprintf(
				"servedDialsOutsideTheSwitch declares %+v, which matches %d dial(s) — exactly one is required; remove a stale entry or split an ambiguous one", site, n))
		}
		if strings.TrimSpace(reason) == "" {
			s.violations = append(s.violations, fmt.Sprintf("servedDialsOutsideTheSwitch declares %+v with no reason", site))
		}
	}
	sort.Strings(s.violations)
	return s
}

// servedScanFloors keeps the scan from passing vacuously on the real tree: a
// broken file classifier, import resolution or dialer fixpoint shows up as
// counts that collapse.
func servedScanFloors(s servedDialScan) []string {
	var out []string
	if s.servedFiles < 40 {
		out = append(out, fmt.Sprintf("found %d served files, want at least 40", s.servedFiles))
	}
	if s.routedDials < 5 {
		out = append(out, fmt.Sprintf("found %d wire.New calls through servedWireOptions, want at least 5 (the harness's dial sites)", s.routedDials))
	}
	if s.switchedServers < 3 {
		out = append(out, fmt.Sprintf("found %d httpapi.Listen calls through servedServerConfig, want at least 3", s.switchedServers))
	}
	for _, want := range []string{"Dial", "DialWith", "DialWithCredential", "Handshake", "OpenWith"} {
		if !s.dialers[want] {
			out = append(out, fmt.Sprintf("the dialer fixpoint lost %s", want))
		}
	}
	return out
}

// seamParsePackage parses every .go file in dir, keyed by base name.
func seamParsePackage(t *testing.T, dir string) (*token.FileSet, map[string]*ast.File) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		files[e.Name()] = f
	}
	return fset, files
}

func TestServedTierDialsThroughTheProtocolSwitch(t *testing.T) {
	fset, files := seamParsePackage(t, ".")
	s := scanServedDials(fset, files, servedDialsOutsideTheSwitch)
	for _, v := range s.violations {
		t.Error(v)
	}
	for _, f := range servedScanFloors(s) {
		t.Errorf("the scan guards less than it should: %s", f)
	}
	dialers := make([]string, 0, len(s.dialers))
	for name := range s.dialers {
		dialers = append(dialers, name)
	}
	sort.Strings(dialers)
	t.Logf("served files %d, dials through the seam %d, servers through the seam %d, dialers: %s",
		s.servedFiles, s.routedDials, s.switchedServers, strings.Join(dialers, ", "))
}

const (
	seamWireImportLine    = "import wire \"" + seamWireImport + "\"\n"
	seamHTTPAPIImportLine = "import httpapi \"" + seamHTTPAPIImport + "\"\n"
)

func seamBasePackage() map[string]string {
	return map[string]string{
		"served_base_test.go": "package httpclient\n" +
			"func newServedEnv() {}\nfunc newServedEnvExpecting() {}\nfunc composition() {}\nfunc serveOneProject() {}\n" +
			"type servedEnv struct{}\ntype servedComposition struct{}\n",
		"dial.go": "package httpclient\n" + seamWireImportLine +
			"func DialWith() { wire.New(nil, nil, wire.Options{}) }\nfunc Dial() { DialWith() }\nfunc Handshake() { Dial() }\n" +
			"func OpenWith() { DialWith() }\nfunc DialWithCredential() { DialWith() }\n",
		"helpers_test.go": "package httpclient\n" + seamWireImportLine +
			"func fakeDial() { wire.New(nil, nil, wire.Options{}) }\n",
	}
}

func TestTheDialScanCatchesEveryWayAroundTheSwitch(t *testing.T) {
	const pkg = "package httpclient\n"
	rows := []struct {
		name     string
		files    map[string]string // merged over the base; "" deletes
		declared map[servedDialSite]string
		want     string // "" means the scan must stay clean
	}{
		{name: "a raw wire.New in a served file",
			files: map[string]string{"served_x_test.go": pkg + seamWireImportLine + "func TestX() { wire.New(nil, nil, wire.Options{}) }\n"},
			want:  "served_x_test.go:3 (TestX): wire.New dials around servedWireOptions"},
		{name: "an aliased wire import",
			files: map[string]string{"served_x_test.go": pkg + "import w \"" + seamWireImport + "\"\nfunc TestX() { w.New(nil, nil, w.Options{}) }\n"},
			want:  "wire.New dials around servedWireOptions"},
		{name: "options built away from the call",
			files: map[string]string{"served_x_test.go": pkg + seamWireImportLine + "func TestX() { o := servedWireOptions(nil, wire.Options{}); wire.New(nil, nil, o) }\n"},
			want:  "(TestX): wire.New dials around servedWireOptions"},
		{name: "a product dialer",
			files: map[string]string{"served_x_test.go": pkg + "func TestX() { Dial() }\n"},
			want:  "(TestX): Dial dials around the served tier's protocol seam"},
		{name: "a test-helper dialer",
			files: map[string]string{"served_x_test.go": pkg + "func TestX() { fakeDial() }\n"},
			want:  "(TestX): fakeDial dials around"},
		{name: "Listen without servedServerConfig",
			files: map[string]string{"served_x_test.go": pkg + seamHTTPAPIImportLine + "func serve() { var cfg httpapi.Config; httpapi.Listen(cfg) }\n"},
			want:  "(serve): httpapi.Listen's config did not go through servedServerConfig first"},
		{name: "servedServerConfig after Listen",
			files: map[string]string{"served_x_test.go": pkg + seamHTTPAPIImportLine + "func serve() { var cfg httpapi.Config; httpapi.Listen(cfg); servedServerConfig(&cfg) }\n"},
			want:  "(serve): httpapi.Listen's config did not go through servedServerConfig first"},
		{name: "servedServerConfig on another config",
			files: map[string]string{"served_x_test.go": pkg + seamHTTPAPIImportLine + "func serve() { var cfg, other httpapi.Config; servedServerConfig(&other); httpapi.Listen(cfg) }\n"},
			want:  "(serve): httpapi.Listen's config did not go through servedServerConfig first"},
		{name: "Listen outside a served file",
			files: map[string]string{"other_test.go": pkg + seamHTTPAPIImportLine + "func serve() { var cfg httpapi.Config; servedServerConfig(&cfg); httpapi.Listen(cfg) }\n"},
			want:  "other_test.go:3 (serve): httpapi.Listen outside a served file"},
		{name: "an entry point referenced outside a served file",
			files: map[string]string{"other_test.go": pkg + "func TestY() { newServedEnv() }\n"},
			want:  "other_test.go:2 (TestY): references served-tier entry point newServedEnv"},
		{name: "a stale declaration",
			files:    map[string]string{"served_x_test.go": pkg + seamWireImportLine + "func TestX() { wire.New(nil, nil, servedWireOptions(nil, wire.Options{})) }\n"},
			declared: map[servedDialSite]string{{"served_x_test.go", "TestX", "wire.New"}: "stale"},
			want:     "matches 0 dial(s)"},
		{name: "a declaration with no reason",
			files:    map[string]string{"served_x_test.go": pkg + seamWireImportLine + "func TestX() { wire.New(nil, nil, wire.Options{}) }\n"},
			declared: map[servedDialSite]string{{"served_x_test.go", "TestX", "wire.New"}: " "},
			want:     "with no reason"},
		{name: "an ambiguous declaration",
			files:    map[string]string{"served_x_test.go": pkg + seamWireImportLine + "func TestX() { wire.New(nil, nil, wire.Options{}); wire.New(nil, nil, wire.Options{}) }\n"},
			declared: map[servedDialSite]string{{"served_x_test.go", "TestX", "wire.New"}: "two"},
			want:     "matches 2 dial(s)"},
		{name: "an entry point no longer declared",
			files: map[string]string{"served_base_test.go": pkg +
				"func newServedEnv() {}\nfunc newServedEnvExpecting() {}\nfunc serveOneProject() {}\ntype servedEnv struct{}\ntype servedComposition struct{}\n"},
			want: "entry point composition is no longer declared"},

		// Negative controls: each must stay clean.
		{name: "control: a declared raw dial does not cascade to its callers",
			files: map[string]string{"served_x_test.go": pkg + seamWireImportLine +
				"func f() { wire.New(nil, nil, wire.Options{}) }\nfunc g() { f() }\n"},
			declared: map[servedDialSite]string{{"served_x_test.go", "f", "wire.New"}: "declared for the control"}},
		{name: "control: a routed dial and a switched server",
			files: map[string]string{"served_x_test.go": pkg + seamWireImportLine + seamHTTPAPIImportLine +
				"func serve() { var cfg httpapi.Config; servedServerConfig(&cfg); httpapi.Listen(cfg); wire.New(nil, nil, servedWireOptions(nil, wire.Options{})) }\n"}},
		{name: "control: a fake-server raw dial outside a served file",
			files: map[string]string{"other_test.go": pkg + seamWireImportLine + "func TestY() { wire.New(nil, nil, wire.Options{}) }\n"}},
		{name: "control: a selector named like an entry point",
			files: map[string]string{"other_test.go": pkg + "func TestY() { var x fixture; x.composition() }\n"}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			srcs := seamBasePackage()
			for name, src := range row.files {
				if src == "" {
					delete(srcs, name)
					continue
				}
				srcs[name] = src
			}
			fset := token.NewFileSet()
			files := map[string]*ast.File{}
			for name, src := range srcs {
				f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
				if err != nil {
					t.Fatalf("parse %s: %v", name, err)
				}
				files[name] = f
			}
			s := scanServedDials(fset, files, row.declared)
			if row.want == "" {
				if len(s.violations) != 0 {
					t.Errorf("the scan fired on a control:\n%s", strings.Join(s.violations, "\n"))
				}
				return
			}
			if !slices.ContainsFunc(s.violations, func(v string) bool { return strings.Contains(v, row.want) }) {
				t.Errorf("the scan did not fire; want a violation containing %q, got:\n%s", row.want, strings.Join(s.violations, "\n"))
			}
		})
	}
	t.Run("floors fire on an empty scan", func(t *testing.T) {
		if got := servedScanFloors(servedDialScan{}); len(got) == 0 {
			t.Error("servedScanFloors passed an empty scan, so a scan that found nothing would pass")
		}
	})
}

// seamTB is a testing.TB that collects what a witness reports instead of
// failing the real test, so the falsification can watch the witness fire.
type seamTB struct {
	testing.TB
	cleanups []func()
	errors   []string
}

func (tb *seamTB) Helper()           {}
func (tb *seamTB) Cleanup(f func())  { tb.cleanups = append(tb.cleanups, f) }
func (tb *seamTB) Error(args ...any) { tb.errors = append(tb.errors, fmt.Sprint(args...)) }
func (tb *seamTB) Errorf(format string, args ...any) {
	tb.errors = append(tb.errors, fmt.Sprintf(format, args...))
}

func (tb *seamTB) runCleanups() {
	for i := len(tb.cleanups) - 1; i >= 0; i-- {
		tb.cleanups[i]()
	}
}

// seamStubTransport answers every request 200 {"status":"ok"} and records it.
type seamStubTransport struct {
	mu   sync.Mutex
	seen []string
}

func (s *seamStubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	s.mu.Lock()
	s.seen = append(s.seen, req.Method+" "+req.URL.Path)
	s.mu.Unlock()
	if req.Body != nil {
		_ = req.Body.Close()
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"status":"ok"}`)),
		Request:    req,
	}, nil
}

func TestTheServedWitnessCatchesABDPRequestUnderV0(t *testing.T) {
	tb := &seamTB{}
	w := newServedWitness(tb, "a falsification client")
	stub := &seamStubTransport{}
	caller := &http.Client{Transport: stub}
	base, err := url.Parse("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	client, err := wire.New(base, nil, servedWireOptions(w, wire.Options{HTTPClient: caller}))
	if err != nil {
		t.Fatalf("build the client: %v", err)
	}
	ctx := context.Background()
	if err := client.Health(ctx); err != nil {
		t.Fatalf("Health through the stub: %v", err)
	}
	_ = client.Do(ctx, wire.Request{Op: "probe", Method: http.MethodPost, Path: "/bdp/call/issue/ready"}, nil)

	want := []string{"GET " + wire.PathHealth, "POST /bdp/call/issue/ready"}
	if !slices.Equal(stub.seen, want) {
		t.Errorf("the caller's transport saw %q, want %q: the recorder must chain it", stub.seen, want)
	}
	if caller.Transport != stub {
		t.Error("servedWireOptions replaced the caller's transport in place; it must wrap a copy")
	}

	tb.runCleanups()
	if len(tb.errors) != 1 {
		t.Fatalf("witness errors %q, want one naming POST /bdp/call/issue/ready", tb.errors)
	}
	got := tb.errors[0]
	for _, part := range []string{"a falsification client", "POST /bdp/call/issue/ready (x1)", "/bdp mount", "1 of 2 request(s)"} {
		if !strings.Contains(got, part) {
			t.Errorf("witness report %q does not mention %q", got, part)
		}
	}
	if strings.Contains(got, wire.PathHealth) {
		t.Errorf("witness report %q names %s, which v0 allows", got, wire.PathHealth)
	}
}

func TestServedWireOptionsChangesOnlyTheTransport(t *testing.T) {
	w := &servedWitness{subject: "options"}

	// 1. A nil client becomes what wire.New would build for one.
	got := servedWireOptions(w, wire.Options{})
	if got.HTTPClient == nil || got.HTTPClient.Timeout != wire.DefaultTimeout {
		t.Fatalf("a nil HTTPClient became %+v, want Timeout %v (wire.New's default)", got.HTTPClient, wire.DefaultTimeout)
	}
	rec, ok := got.HTTPClient.Transport.(servedRecorder)
	if !ok {
		t.Fatalf("transport is %T, want servedRecorder", got.HTTPClient.Transport)
	}
	if rec.next != http.DefaultTransport || rec.witness != w {
		t.Errorf("recorder chains %v with witness %p, want http.DefaultTransport and %p", rec.next, rec.witness, w)
	}

	// 2. Every other field passes through.
	in := wire.Options{
		UserAgent:             "seam/1",
		ExpectProjectID:       "proj-seam",
		MaxResponseBytes:      1234,
		MaxRetryAfter:         7 * time.Second,
		ResponseHeaderTimeout: 3 * time.Second,
	}
	out := servedWireOptions(w, in)
	out.HTTPClient = nil
	if !reflect.DeepEqual(out, in) {
		t.Errorf("servedWireOptions changed more than the transport: got %+v, want %+v", out, in)
	}

	// 3. A caller's client is copied, keeping its timeout and transport.
	x := &seamStubTransport{}
	caller := &http.Client{Timeout: 5 * time.Second, Transport: x}
	got = servedWireOptions(w, wire.Options{HTTPClient: caller})
	if got.HTTPClient == caller {
		t.Fatal("servedWireOptions returned the caller's own client; it must wrap a copy")
	}
	if got.HTTPClient.Timeout != 5*time.Second {
		t.Errorf("timeout %v, want the caller's 5s", got.HTTPClient.Timeout)
	}
	if rec, ok := got.HTTPClient.Transport.(servedRecorder); !ok || rec.next != x {
		t.Errorf("transport %#v, want a servedRecorder chaining the caller's transport", got.HTTPClient.Transport)
	}
	if caller.Transport != x {
		t.Error("the caller's client was mutated")
	}

	// 4. A served dial without a witness is a programming error.
	func() {
		defer func() {
			if recover() == nil {
				t.Error("servedWireOptions(nil, ...) did not panic")
			}
		}()
		_ = servedWireOptions(nil, wire.Options{})
	}()
}
