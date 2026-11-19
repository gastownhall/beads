package httpapi

// The default surface: what a server built from a zero Config answers on paths
// no v0 operation owns.
//
// Before the mount table (mount_table_test.go), every other server invariant
// in this package iterated routeTable, so a handler mounted outside the table,
// or a catch-all wrapper that answers instead of falling through, was
// invisible to all of them on a default server. A whole call namespace mounted
// on every server once passed the entire suite that way. This file closes that
// hole by asking the wire, which still sees what the mount censuses cannot:
// what a default server answers on a path no mount names.
//
// The sweep sends real loopback requests through newTestServer: two postures
// (no auth; a token file, with no credential sent) times twelve paths times
// eight methods, 192 requests, sequential, no body. It checks two layers:
//
//  1. An invariant in code, which no regeneration can relax: every answer is
//     v0's unrouted 404 (the answer to GET /v0/nonsense, itself checked to be
//     404 problem+json, code not_found, carrying this server's request id;
//     HEAD gets the same status and headers with no body), every request left
//     its request log line, and none was booked to an operation.
//  2. A byte golden, testdata/default_surface.golden: status, every header but
//     Date, and the body. Identical answers are stored once as sha256-keyed
//     blocks, plus one row per request, so a regression reads "N of 192
//     requests answer differently" with each changed row's old and new key.
//
// Normalization: this server's request ids read <request-id> (they have a
// fixed length, so Content-Length stays deterministic) and its address reads
// <addr>. Only Date is dropped; this is a drop-list, not an allow-list, so a
// new Allow, Vary, Link or ETag header shows up as a diff.
//
// Regenerate the golden only for a reviewed change, with defaultSurfaceRegen.
//
// Forward note: a server option that selects which protocol surfaces are
// answered must be pinned in defaultSurfacePostures at its zero value and at
// its v0 value, with this golden unchanged byte for byte.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	defaultSurfaceGolden = "default_surface.golden"
	defaultSurfaceRegen  = "BD_UPDATE_GOLDEN=1 go test -tags gms_pure_go ./internal/httpapi -run '^TestDefaultSurfaceAnswersV0sUnrouted404$'"
)

// defaultSurfacePaths are the paths no v0 operation owns that a stray mount
// would most plausibly claim. The last one is the reference answer.
var defaultSurfacePaths = []string{
	// The root.
	"/",
	// The protocol namespace and its subtree; a subtree mount at /bdp/ would
	// make ServeMux redirect /bdp, which is why the client never follows.
	"/bdp",
	"/bdp/",
	// A call listing and one invocation.
	"/bdp/call",
	"/bdp/call/issue/show",
	// Collections.
	"/bdp/beads",
	"/bdp/links",
	// An operations directory.
	"/bdp/operations/createBead",
	// A single-segment resource address.
	"/bd-1",
	// A path outside every namespace.
	"/a/b",
	// Another API version.
	"/v1/beads/ready",
	// An unrouted v0 path: the reference every other answer must equal.
	"/v0/nonsense",
}

// defaultSurfaceMethods covers every method net/http would route, so a mount
// that answers only POST or OPTIONS is still seen.
var defaultSurfaceMethods = []string{
	http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
	http.MethodPatch, http.MethodDelete, http.MethodOptions, http.MethodTrace,
}

// surfacePosture is one server configuration the sweep stands up. No request
// carries a credential: a default server's unrouted answer must not depend on
// whether auth is configured.
type surfacePosture struct {
	name   string
	config func(t *testing.T) Config
}

var defaultSurfacePostures = []surfacePosture{
	{name: "no-auth", config: func(*testing.T) Config { return Config{} }},
	{name: "token-file", config: func(t *testing.T) Config { return Config{Auth: authTokenFile(t, "surface-token")} }},
}

// surfaceAnswer is one normalized answer: status, headers in sorted order
// (one entry per value, in received order), and body.
type surfaceAnswer struct {
	status int
	header []string
	body   string
}

func (a surfaceAnswer) block() string {
	var b strings.Builder
	fmt.Fprintf(&b, "status: %d\n", a.status)
	for _, h := range a.header {
		fmt.Fprintf(&b, "header: %s\n", h)
	}
	fmt.Fprintf(&b, "body: %s\n", strconv.Quote(a.body))
	return b.String()
}

// display is block for a failure message: a body past 512 bytes is cut, since
// what a stray mount answers can be a whole document.
func (a surfaceAnswer) display() string {
	if len(a.body) > 512 {
		a.body = a.body[:512] + fmt.Sprintf("...(%d bytes in all)", len(a.body))
	}
	return a.block()
}

// key names an answer in the golden: the first 12 hex digits of the sha256 of
// its block.
func (a surfaceAnswer) key() string {
	sum := sha256.Sum256([]byte(a.block()))
	return hex.EncodeToString(sum[:])[:12]
}

// surfaceProbe is one request of the sweep, its answer, and what its request
// log line said: whether there was one, and which op it was booked to.
type surfaceProbe struct {
	posture, method, path string
	answer                surfaceAnswer
	op                    string
	logged                bool
}

func (p surfaceProbe) name() string { return p.posture + " " + p.method + " " + p.path }

// sweepDefaultSurface stands up one fresh server per posture (tune, if any,
// runs between Listen and Serve) and sends every path x method to it.
//
// The client never follows redirects, because a redirect is an answer, and
// never asks for compression, so Content-Encoding and Content-Length are
// exactly what the server sent.
//
// The request log lines are joined after each posture's requests by method and
// path. That is safe: withRequestContext writes its line in a defer, and a
// small buffered answer reaches the client only after the handler returns.
func sweepDefaultSurface(t *testing.T, tune func(*Server)) []surfaceProbe {
	t.Helper()
	var probes []surfaceProbe
	for _, posture := range defaultSurfacePostures {
		var tunes []func(*Server)
		if tune != nil {
			tunes = append(tunes, tune)
		}
		ts := newTestServer(t, posture.config(t), tunes...)
		transport := &http.Transport{DisableCompression: true}
		t.Cleanup(transport.CloseIdleConnections)
		client := &http.Client{
			Transport:     transport,
			Timeout:       20 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
		noise := strings.NewReplacer(ts.Addr(), "<addr>")
		ids := regexp.MustCompile(regexp.QuoteMeta(ts.idPrefix) + `-[0-9]+`)
		normalize := func(s string) string { return ids.ReplaceAllString(noise.Replace(s), "<request-id>") }
		start := len(probes)
		for _, path := range defaultSurfacePaths {
			for _, method := range defaultSurfaceMethods {
				req, err := http.NewRequest(method, ts.base+path, nil)
				if err != nil {
					t.Fatalf("new request %s %s: %v", method, path, err)
				}
				resp, err := client.Do(req)
				if err != nil {
					t.Fatalf("%s %s %s: %v", posture.name, method, path, err)
				}
				body, err := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if err != nil {
					t.Fatalf("%s %s %s: read body: %v", posture.name, method, path, err)
				}
				var header []string
				for _, name := range slices.Sorted(maps.Keys(resp.Header)) {
					if name == "Date" {
						continue
					}
					for _, v := range resp.Header[name] {
						header = append(header, name+": "+strconv.Quote(normalize(v)))
					}
				}
				probes = append(probes, surfaceProbe{
					posture: posture.name, method: method, path: path,
					answer: surfaceAnswer{status: resp.StatusCode, header: header, body: normalize(string(body))},
				})
			}
		}
		lines := map[string]map[string]string{}
		for _, line := range strings.Split(ts.stderr.String(), "\n") {
			if !strings.Contains(line, "event=request ") {
				continue
			}
			_, f := eventFields(t, line)
			k := f["method"] + " " + f["path"]
			if _, dup := lines[k]; dup {
				t.Fatalf("two request lines for %s on one server: the sweep sends each request once", k)
			}
			lines[k] = f
		}
		for i := start; i < len(probes); i++ {
			if f, ok := lines[probes[i].method+" "+probes[i].path]; ok {
				probes[i].logged, probes[i].op = true, f["op"]
			}
		}
	}
	return probes
}

// surfaceViolation is one way some requests of the sweep deviated from v0's
// unrouted 404, and which requests did.
type surfaceViolation struct {
	what   string
	probes []string
}

func (v surfaceViolation) String() string {
	return fmt.Sprintf("%d requests %s\nrequests: %s", len(v.probes), v.what, strings.Join(v.probes, ", "))
}

// unroutedViolations holds every probe to the reference: what this server
// answers GET /v0/nonsense, which must itself be v0's unrouted 404.
func unroutedViolations(t *testing.T, probes []surfaceProbe) []surfaceViolation {
	t.Helper()
	var reference *surfaceAnswer
	for _, p := range probes {
		if p.posture == defaultSurfacePostures[0].name && p.method == http.MethodGet && p.path == "/v0/nonsense" {
			reference = &p.answer
		}
	}
	if reference == nil {
		t.Fatal("the sweep did not send GET /v0/nonsense, the reference unrouted answer")
	}
	var problem map[string]any
	if reference.status != http.StatusNotFound ||
		!slices.Contains(reference.header, `Content-Type: "application/problem+json; charset=utf-8"`) ||
		json.Unmarshal([]byte(reference.body), &problem) != nil ||
		problem["code"] != string(CodeNotFound) || problem["request_id"] != "<request-id>" {
		t.Fatalf("GET /v0/nonsense answered\n%sthat is not v0's unrouted 404 (404 problem+json, code %s, this server's request_id)",
			reference.display(), CodeNotFound)
	}
	var out []surfaceViolation
	index := map[string]int{}
	note := func(what, probe string) {
		i, seen := index[what]
		if !seen {
			i = len(out)
			index[what] = i
			out = append(out, surfaceViolation{what: what})
		}
		out[i].probes = append(out[i].probes, probe)
	}
	for _, p := range probes {
		want := *reference
		if p.method == http.MethodHead {
			want.body = ""
		}
		switch {
		case p.answer.block() != want.block():
			note("answered\n"+p.answer.display()+"instead of the unrouted answer\n"+want.display(), p.name())
		case !p.logged:
			note("left no request line: the answer did not pass through withRequestContext", p.name())
		case p.op != "":
			note(fmt.Sprintf("were booked to op %q: an operation answered with the unrouted bytes", p.op), p.name())
		}
	}
	return out
}

// renderDefaultSurface is the golden text: a header comment, each distinct
// answer once under its key, then one row per request in sweep order.
func renderDefaultSurface(probes []surfaceProbe) string {
	var b strings.Builder
	b.WriteString(`# The default surface of internal/httpapi: what a server built from Config{}
# answers, with no credential sent, on paths no v0 operation owns. Every
# answer is v0's unrouted 404 (TestDefaultSurfaceAnswersV0sUnrouted404 holds
# that whatever this file says). A diff here changes what every default
# bd serve answers; regenerate only for a reviewed change:
#   ` + defaultSurfaceRegen + `
# Normalized: Date is dropped, this server's request ids read <request-id>
# and its address <addr>.
`)
	answers := map[string]surfaceAnswer{}
	for _, p := range probes {
		answers[p.answer.key()] = p.answer
	}
	b.WriteString("\n== answers ==\n")
	for _, key := range slices.Sorted(maps.Keys(answers)) {
		fmt.Fprintf(&b, "\nanswer %s\n%s", key, answers[key].block())
	}
	b.WriteString("\n== requests ==\n")
	for _, p := range probes {
		fmt.Fprintf(&b, "%-10s %-7s %-26s %s\n", p.posture, p.method, p.path, p.answer.key())
	}
	return b.String()
}

// TestDefaultSurfaceAnswersV0sUnrouted404 pins the default surface: the
// invariant first (it holds whatever the golden says), then the golden bytes.
func TestDefaultSurfaceAnswersV0sUnrouted404(t *testing.T) {
	probes := sweepDefaultSurface(t, nil)
	for _, v := range unroutedViolations(t, probes) {
		t.Error(v)
	}
	got := renderDefaultSurface(probes)
	path := filepath.Join("testdata", defaultSurfaceGolden)
	if os.Getenv("BD_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		t.Logf("updated %s", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s (regenerate with %s): %v", path, defaultSurfaceRegen, err)
	}
	if got != string(want) {
		t.Errorf("the default surface drifted from %s:\n%s\nregenerate with %s only for a reviewed change",
			path, describeSurfaceDrift(string(want), probes), defaultSurfaceRegen)
	}
}

// describeSurfaceDrift says how probes differ from the golden text want: each
// request whose answer changed, and each answer the golden does not hold.
func describeSurfaceDrift(want string, probes []surfaceProbe) string {
	wantKey := map[string]string{}
	wantAnswer := map[string]bool{}
	section := ""
	for _, line := range strings.Split(want, "\n") {
		switch {
		case strings.HasPrefix(line, "== "):
			section = line
		case strings.HasPrefix(line, "answer "):
			wantAnswer[strings.TrimPrefix(line, "answer ")] = true
		case section == "== requests ==" && line != "":
			if f := strings.Fields(line); len(f) == 4 {
				wantKey[f[0]+" "+f[1]+" "+f[2]] = f[3]
			}
		}
	}
	var b strings.Builder
	changed := 0
	newAnswers := map[string]surfaceAnswer{}
	for _, p := range probes {
		if got := p.answer.key(); wantKey[p.name()] != got {
			changed++
			fmt.Fprintf(&b, "  %-10s %-7s %-26s %s -> %s\n", p.posture, p.method, p.path, orNone(wantKey[p.name()]), got)
		}
		if !wantAnswer[p.answer.key()] {
			newAnswers[p.answer.key()] = p.answer
		}
	}
	fmt.Fprintf(&b, "%d of %d requests answer differently", changed, len(probes))
	for _, key := range slices.Sorted(maps.Keys(newAnswers)) {
		fmt.Fprintf(&b, "\nanswer %s, not in the golden:\n%s", key, newAnswers[key].display())
	}
	if changed == 0 && len(newAnswers) == 0 {
		b.WriteString("; the golden's other lines differ: " + firstSurfaceDifference(want, renderDefaultSurface(probes)))
	}
	return b.String()
}

// orNone shows a request the golden has no row for.
func orNone(key string) string {
	if key == "" {
		return "(none)"
	}
	return key
}

// firstSurfaceDifference reports the first line where want and got differ,
// for a drift no request row explains (the header comment, say).
func firstSurfaceDifference(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		if i >= len(w) || i >= len(g) || w[i] != g[i] {
			line := func(l []string) string {
				if i >= len(l) {
					return "(end of file)"
				}
				return strconv.Quote(l[i])
			}
			return fmt.Sprintf("line %d: want %s, got %s", i+1, line(w), line(g))
		}
	}
	return "none"
}

// mountOnSurface is a newTestServer tune that puts a fresh ServeMux in front
// of the server's handler: h(s) behind the same middleware at each pattern,
// and everything else to the original handler. It runs before Serve, so
// replacing s.http.Handler does not race.
func mountOnSurface(patterns []string, h func(*Server) http.Handler) func(*Server) {
	return func(s *Server) {
		mux := http.NewServeMux()
		for _, p := range patterns {
			mux.Handle(p, s.withRequestContext(s.checkHost(h(s))))
		}
		mux.Handle("/", s.http.Handler)
		s.http.Handler = mux
	}
}

// TestDefaultSurfaceGuardFiresOnAStrayMount is the guard's falsification. A
// guard written against a surface that already satisfies it is permanently
// green, so each arm mounts something in front of a default server and checks
// that exactly the expected requests change golden rows and exactly the
// expected requests trip the invariant:
//   - a document at /bdp/call: status and body changes are seen;
//   - an extra header on the unrouted 404: header-only changes are seen, which
//     needs the drop-list;
//   - a reworded detail of the same length: body-only changes are seen, and
//     HEAD rows carry no body (14 requests, not 16);
//   - the unrouted bytes answered by an operation: invisible to the golden,
//     caught by the invariant's op check.
func TestDefaultSurfaceGuardFiresOnAStrayMount(t *testing.T) {
	honest := sweepDefaultSurface(t, nil)
	if v := unroutedViolations(t, honest); len(v) != 0 {
		t.Fatalf("the default surface is not v0's unrouted 404 to begin with, so no arm below can show anything: %v", v)
	}
	every := func(paths ...string) []string {
		var out []string
		for _, posture := range defaultSurfacePostures {
			for _, path := range paths {
				for _, m := range defaultSurfaceMethods {
					out = append(out, posture.name+" "+m+" "+path)
				}
			}
		}
		slices.Sort(out)
		return out
	}
	everyBut := func(method string, paths ...string) []string {
		return slices.DeleteFunc(every(paths...), func(name string) bool { return strings.Contains(name, " "+method+" ") })
	}
	unroutedBytes := func(s *Server) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.fail(w, r, newResult(CodeNotFound, "no such route on this server"))
		})
	}
	for _, tc := range []struct {
		name         string
		tune         func(*Server)
		goldenLines  []string
		invariantHit []string
	}{
		{
			name: "a document mounted at /bdp/call",
			tune: mountOnSurface([]string{"/bdp/call", "/bdp/call/"}, func(*Server) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"openapi":"3.1.0"}`))
				})
			}),
			goldenLines:  every("/bdp/call", "/bdp/call/issue/show"),
			invariantHit: every("/bdp/call", "/bdp/call/issue/show"),
		},
		{
			name: "an extra header on the unrouted 404",
			tune: mountOnSurface([]string{"/bd-1"}, func(s *Server) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Allow", "GET, HEAD")
					unroutedBytes(s).ServeHTTP(w, r)
				})
			}),
			goldenLines:  every("/bd-1"),
			invariantHit: every("/bd-1"),
		},
		{
			name: "a reworded detail of the same length",
			tune: mountOnSurface([]string{"/v1/beads/ready"}, func(s *Server) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					s.fail(w, r, newResult(CodeNotFound, "no such route on THIS server"))
				})
			}),
			goldenLines:  everyBut(http.MethodHead, "/v1/beads/ready"),
			invariantHit: everyBut(http.MethodHead, "/v1/beads/ready"),
		},
		{
			name: "the unrouted bytes answered by an operation",
			tune: mountOnSurface([]string{"/a/b"}, func(s *Server) http.Handler {
				return s.route(route{op: "strayMount", authExempt: true, projectExempt: true,
					wireRevisionExempt: true, bypassSemaphore: true,
					handler: func(s *Server, w http.ResponseWriter, r *http.Request) { unroutedBytes(s).ServeHTTP(w, r) }})
			}),
			goldenLines:  nil,
			invariantHit: every("/a/b"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mounted := sweepDefaultSurface(t, tc.tune)
			var changed []string
			for i := range honest {
				if honest[i].answer.key() != mounted[i].answer.key() {
					changed = append(changed, honest[i].name())
				}
			}
			slices.Sort(changed)
			if !slices.Equal(changed, tc.goldenLines) {
				t.Errorf("golden lines changed: %d %v\nwant %d %v", len(changed), changed, len(tc.goldenLines), tc.goldenLines)
			}
			if (renderDefaultSurface(honest) == renderDefaultSurface(mounted)) != (len(tc.goldenLines) == 0) {
				t.Errorf("rendered golden equality disagrees with the changed-line count")
			}
			var hit []string
			for _, v := range unroutedViolations(t, mounted) {
				hit = append(hit, v.probes...)
			}
			slices.Sort(hit)
			if !slices.Equal(hit, tc.invariantHit) {
				t.Errorf("the invariant fired for %d requests %v, want %d %v", len(hit), hit, len(tc.invariantHit), tc.invariantHit)
			}
			if len(tc.goldenLines) > 0 {
				want := fmt.Sprintf("%d of %d requests answer differently", len(tc.goldenLines), len(honest))
				if msg := describeSurfaceDrift(renderDefaultSurface(honest), mounted); !strings.Contains(msg, want) {
					t.Errorf("the drift report does not say %q:\n%s", want, msg)
				}
			}
		})
	}
}
