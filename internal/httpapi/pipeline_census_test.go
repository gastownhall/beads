package httpapi

// The request-pipeline census: every endpoint of every mount, held at every
// stage of the request pipeline to the posture its row declares.
//
// route() runs the pipeline for an operation: the bearer credential, the
// embedder's Config.RequestContext hook, the Bd-Project-Id stamp, the
// Bd-Wire-Revision floor and a database slot, in that order, each unless the
// row declares itself exempt (the hook has no exemption: it runs for every
// routed request); handler() puts the Host policy in front of every mount.
// Each stage has tests of its own, but before the mount table every "every
// route" loop ranged over routeTable, so a handler mounted beside the table,
// or a second pipeline that re-implemented some stages and not others, went
// unseen.
//
// The census stands up one server per round. A round arms some stages (a
// server-side setting and, where the stage reads one, a request header) and
// sends every endpoint's request (mountEndpoints) to it with an unknown query
// parameter, the probe. The probe makes every answer attributable with no
// database work and no fake that answers each operation: an operation refuses
// it at its own door (400 unknown_parameter naming it), so an endpoint that
// skips every armed stage answers exactly what it answers on an unarmed
// server, its baseline, and one that runs a stage answers that stage's
// refusal. Rounds arm each stage alone, then every run of consecutive stages
// after the Host check together, where the first stage an endpoint runs must
// be the one that answers.
//
// An armed stage refuses every request that runs it, so in those rounds a
// request reaches the stages behind a check only on a server that does not
// configure the check, or when the request presents nothing for it to
// compare. Real traffic gets past the checks instead: a remote client
// presents its credential, bd's client stamps the project it pinned and
// declares its wire revision, an embedder's hook hands on what it accepts. So
// each round of the stages behind the credential runs again behind passes,
// once for each combination of the stages between the Host check and its
// first one: the server configures those stages and gets every request past
// them the way that traffic gets past them (pipelineStage.pass). A pass
// refuses nothing, so the first stage behind the passes that an endpoint runs
// must answer as it does with nothing in front of it. The Host round runs
// once more with every pass: on a server that checks a credential, runs a
// hook and knows its project id. Three layers, none of which a regeneration
// can relax:
//
//  1. Behavior matches the declaration: each answer is the baseline or the
//     refusal its posture predicts, in pipeline order, booked on the request
//     line to the endpoint's op (none for the Host check, which answers before
//     the router), carrying Cache-Control: no-store.
//  2. Declarations match the reviewed lists: the endpoints declaring an
//     exemption from each stage are exactly pipelineExemptions.
//  3. No probe opens a unit of work, on any server.
//
// Forward note: a server option that selects which surfaces are mounted must
// set its value explicitly here, and run this census once per value, each
// against its own reviewed lists.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
)

// pipelineProbe is the unknown query parameter every census request carries.
const pipelineProbe = "pipeline_probe"

// censusToken is the credential a census server checks.
const censusToken = "census-token"

// censusRefused is the code the census's RequestContext hook refuses with.
const censusRefused = "census_refused"

// pipelineStage is one stage of the request pipeline, and how to arm it.
type pipelineStage struct {
	name string
	// config, tune and hold arm the server: before Listen, between Listen and
	// Serve, and once it serves. request arms the request.
	config  func(t *testing.T, cfg *Config)
	tune    func(*Server)
	hold    func(t *testing.T, ts *testServer)
	request func(*http.Request)
	// exempt reads what an endpoint declares about this stage. An unrouted
	// answer is exempt from every stage but the Host policy.
	exempt func(mountEndpoint) bool
	// refused recognizes this stage's refusal.
	refused func(pipelineAnswer) bool
	// booksOp says whether the refusal's request line names the endpoint's op.
	// The Host policy answers before the router, so it names none.
	booksOp bool
	// installsHook says this stage installs a RequestContext hook, which runs
	// once for each routed request that reaches it (wantHookCalls).
	installsHook bool
	// pass, on a stage a request can get past, is the pseudo-stage that
	// configures the stage and gets every request past it: a request
	// presenting what the stage accepts, or a hook that accepts it. Every
	// stage between the Host check and the last has one. The Host check is
	// passed in every round that does not arm it (each request names the
	// server's own address), and so is the slot (one is free).
	pass *pipelineStage
}

// pipelineStages are the stages in the order the pipeline runs them.
var pipelineStages = []pipelineStage{
	{
		name:    "host",
		request: func(r *http.Request) { r.Host = "rebind.example" },
		exempt:  func(mountEndpoint) bool { return false },
		refused: func(a pipelineAnswer) bool {
			return a.status == http.StatusBadRequest && a.code == string(CodeInvalidArgument) && a.param == "Host"
		},
	},
	{
		name:   "auth",
		config: func(t *testing.T, cfg *Config) { cfg.Auth = authTokenFile(t, censusToken) },
		exempt: func(e mountEndpoint) bool { return e.row == nil || e.row.authExempt },
		refused: func(a pipelineAnswer) bool {
			return a.status == http.StatusUnauthorized && a.code == string(CodeUnauthenticated)
		},
		booksOp: true,
		pass:    &pipelineBearer,
	},
	{
		// The embedder's hook, refusing everything it is handed. It runs for
		// every routed request, liveness and the auth-exempt rows included, so
		// only an unrouted answer skips it. probePipeline counts its calls.
		name: "hook",
		config: func(_ *testing.T, cfg *Config) {
			cfg.RequestContext = func(*http.Request) (context.Context, error) {
				return nil, &RequestRefusal{Status: http.StatusForbidden, Code: censusRefused}
			}
		},
		exempt: func(e mountEndpoint) bool { return e.row == nil },
		refused: func(a pipelineAnswer) bool {
			return a.status == http.StatusForbidden && a.code == censusRefused
		},
		booksOp:      true,
		installsHook: true,
		pass:         &pipelinePassingHook,
	},
	{
		name:    "project",
		config:  func(_ *testing.T, cfg *Config) { cfg.Workspace = domain.ContextInfo{ProjectID: serverProjectID} },
		request: func(r *http.Request) { r.Header.Set(ProjectIDHeader, "other-proj") },
		exempt:  func(e mountEndpoint) bool { return e.row == nil || e.row.projectExempt },
		refused: func(a pipelineAnswer) bool {
			return a.status == http.StatusBadRequest && a.reason == string(ReasonProjectMismatch) && a.param == ProjectIDHeader
		},
		booksOp: true,
		pass:    &pipelineMatchingStamp,
	},
	{
		name:    "wire",
		tune:    func(s *Server) { s.minClientWireRevision = 3 },
		request: func(r *http.Request) { r.Header.Set(WireRevisionHeader, "2") },
		exempt:  func(e mountEndpoint) bool { return e.row == nil || e.row.wireRevisionExempt },
		refused: func(a pipelineAnswer) bool {
			return a.status == http.StatusBadRequest && a.reason == string(ReasonWireRevisionUnsupported) && a.param == WireRevisionHeader
		},
		booksOp: true,
		pass:    &pipelineCurrentRevision,
	},
	{
		// Every slot held, and a wait bounded at a millisecond: the shed, not
		// the queue, is what is under test.
		name: "slot",
		tune: func(s *Server) { s.semTimeout = time.Millisecond },
		hold: func(t *testing.T, ts *testServer) {
			for range maxInflight {
				release, err := ts.acquire(context.Background(), &reqInfo{id: "census-holder"})
				if err != nil {
					t.Fatalf("hold a slot: %v", err)
				}
				t.Cleanup(release)
			}
		},
		exempt: func(e mountEndpoint) bool { return e.row == nil || e.row.bypassSemaphore },
		refused: func(a pipelineAnswer) bool {
			return a.status == http.StatusServiceUnavailable && a.code == string(CodeBusy) && a.retryAfter != ""
		},
		booksOp: true,
	},
}

// The passes (pipelineStage.pass). A pass is not a stage: it configures what
// one stage checks and gets every request past it, the way the traffic that
// stage sees gets past it. A stage armed to refuse lets no request through to
// the stages behind it, so without the passes a gate that skipped those stages
// for a request that got past the check would go unseen. A pass refuses
// nothing: every endpoint skips it.
var (
	// The credential the auth stage arms, presented on every request as a
	// remote client presents it: a non-loopback server checks one unless its
	// operator waived it.
	pipelineBearer = pipelineStage{
		name:    "bearer",
		config:  func(t *testing.T, cfg *Config) { cfg.Auth = authTokenFile(t, censusToken) },
		request: func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+censusToken) },
		exempt:  func(mountEndpoint) bool { return true },
	}
	// An embedder's hook that hands every request on with its own context, as
	// the hook contract asks for a request the hook has nothing to say about.
	pipelinePassingHook = pipelineStage{
		name: "passhook",
		config: func(_ *testing.T, cfg *Config) {
			cfg.RequestContext = func(r *http.Request) (context.Context, error) { return r.Context(), nil }
		},
		exempt:       func(mountEndpoint) bool { return true },
		installsHook: true,
	}
	// The server's project id, and a stamp naming it on every request, as bd's
	// client stamps each request once its project is pinned.
	pipelineMatchingStamp = pipelineStage{
		name:    "stamp",
		config:  func(_ *testing.T, cfg *Config) { cfg.Workspace = domain.ContextInfo{ProjectID: serverProjectID} },
		request: func(r *http.Request) { r.Header.Set(ProjectIDHeader, serverProjectID) },
		exempt:  func(mountEndpoint) bool { return true },
	}
	// This build's wire revision, declared on every request as bd's client
	// declares it, against the floor the server ships with.
	pipelineCurrentRevision = pipelineStage{
		name:    "revision",
		request: func(r *http.Request) { r.Header.Set(WireRevisionHeader, strconv.Itoa(CurrentWireRevision)) },
		exempt:  func(mountEndpoint) bool { return true },
	}
)

const (
	unroutedCatchAll     = "unrouted /"
	unroutedCustomMethod = "unrouted POST " + customMethodPattern
)

// pipelineExemptions is the reviewed list: for each stage, exactly the
// endpoints whose posture skips it. It generalizes
// TestProjectExemptRoutesAreExactlyHealthAndContext and
// TestWireRevisionExemptRoutesAreExactlyHealth to every mount, unrouted
// answers included. An entry is added only with a reason a reviewer agrees
// with, next to the column that declares it.
var pipelineExemptions = map[string][]string{
	// Nothing answers a Host this server does not answer to.
	"host": nil,
	// Liveness answers with no credential; a path no operation owns is
	// public spec.
	"auth": {OpHealth, unroutedCatchAll, unroutedCustomMethod},
	// The hook runs for every routed request; only a path no operation owns
	// never reaches it.
	"hook": {unroutedCatchAll, unroutedCustomMethod},
	// Liveness, and the handshake where a client learns the id to stamp.
	"project": {OpGetContext, OpHealth, unroutedCatchAll, unroutedCustomMethod},
	// Liveness only: the handshake is where a floor violation is cheapest.
	"wire": {OpHealth, unroutedCatchAll, unroutedCustomMethod},
	// The two reads that touch no database, and the stream that takes a slot
	// per read instead.
	"slot": {OpGetContext, OpHealth, OpWatchEvents, unroutedCatchAll, unroutedCustomMethod},
}

// pipelineAnswer is what one census request got back, and what its request
// line booked.
type pipelineAnswer struct {
	status              int
	code, reason, param string
	retryAfter          string
	cacheControl        []string
	logged              bool
	op                  string
	// hookCalls is how many times the server's RequestContext hook ran for
	// this request; always 0 on a server with no hook.
	hookCalls int
}

// same reports whether two answers are the same answer: status, problem
// members and the op booked.
func (a pipelineAnswer) same(b pipelineAnswer) bool {
	return a.status == b.status && a.code == b.code && a.reason == b.reason && a.param == b.param && a.op == b.op
}

func (a pipelineAnswer) String() string {
	s := fmt.Sprintf("%d %s", a.status, a.code)
	if a.code == "" {
		s += "(no problem body)"
	}
	if a.reason != "" {
		s += "/" + a.reason
	}
	if a.param != "" {
		s += " naming " + a.param
	}
	return s + fmt.Sprintf(" (op %q)", a.op)
}

// pipelineViolation is one way one endpoint broke the census in one round.
type pipelineViolation struct {
	round, endpoint, what string
}

func (v pipelineViolation) String() string { return v.round + " " + v.endpoint + ": " + v.what }

// censusRig, for the census's own falsification, replaces the router of every
// server the census stands up: it returns the mounts to serve instead of
// s.mounts(), and may change s first. The census itself passes nil and probes
// the router handler() built.
type censusRig func(s *Server) []mount

// probePipeline stands up one server with the given stages armed (none: the
// baseline) and sends every endpoint's request to it. It returns the
// endpoints, their answers in the same order, and how many units of work the
// server opened.
func probePipeline(t *testing.T, armed []*pipelineStage, rig censusRig) ([]mountEndpoint, []pipelineAnswer, int) {
	t.Helper()
	provider := &fakeProvider{}
	cfg := Config{Provider: provider}
	var tunes []func(*Server)
	for _, st := range armed {
		if st.config != nil {
			st.config(t, &cfg)
		}
		if st.tune != nil {
			tunes = append(tunes, st.tune)
		}
	}
	// Every call of the hook an armed stage installed, by method and path:
	// the census holds it to once per routed request that reaches it.
	var hookMu sync.Mutex
	hookCalls := map[string]int{}
	if hook := cfg.RequestContext; hook != nil {
		cfg.RequestContext = func(r *http.Request) (context.Context, error) {
			hookMu.Lock()
			hookCalls[r.Method+" "+r.URL.Path]++
			hookMu.Unlock()
			return hook(r)
		}
	}
	var ms []mount
	tunes = append(tunes, func(s *Server) {
		if rig == nil {
			ms = s.mounts()
			return
		}
		ms = rig(s)
		mux := http.NewServeMux()
		for _, m := range ms {
			mux.Handle(m.pattern, m.handler)
		}
		s.http.Handler = s.withRequestContext(s.checkHost(mux))
	})
	ts := newTestServer(t, cfg, tunes...)
	for _, st := range armed {
		if st.hold != nil {
			st.hold(t, ts)
		}
	}
	eps := mountEndpoints(t, ms)

	answers := make([]pipelineAnswer, len(eps))
	for i, e := range eps {
		req, err := http.NewRequest(e.method, ts.base+e.path+"?"+pipelineProbe+"=1", nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		for _, st := range armed {
			if st.request != nil {
				st.request(req)
			}
		}
		resp, err := ts.client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", e.method, e.path, err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatalf("%s %s: read body: %v", e.method, e.path, err)
		}
		a := pipelineAnswer{status: resp.StatusCode, retryAfter: resp.Header.Get("Retry-After"), cacheControl: resp.Header.Values("Cache-Control")}
		// A body that is not a problem leaves the members empty; the census
		// then reports what answered instead of stopping here.
		var problem map[string]any
		if json.Unmarshal(body, &problem) == nil {
			a.code, _ = problem["code"].(string)
			a.reason, _ = problem["reason"].(string)
			a.param, _ = problem["param"].(string)
		}
		answers[i] = a
	}

	// Each request's line, joined by method and path: one request per
	// endpoint, and no two endpoints share both.
	lines := map[string]map[string]string{}
	for _, line := range strings.Split(ts.stderr.String(), "\n") {
		if !strings.Contains(line, "event=request ") {
			continue
		}
		_, f := eventFields(t, line)
		key := f["method"] + " " + f["path"]
		if _, dup := lines[key]; dup {
			t.Fatalf("two request lines for %s on one server: the census sends each request once", key)
		}
		lines[key] = f
	}
	hookMu.Lock()
	defer hookMu.Unlock()
	for i, e := range eps {
		if f, ok := lines[e.method+" "+e.path]; ok {
			answers[i].logged, answers[i].op = true, f["op"]
		}
		answers[i].hookCalls = hookCalls[e.method+" "+e.path]
	}
	return eps, answers, len(provider.openedUOWs())
}

// pipelineRounds are the servers the census stands up after the baseline,
// each listing what it arms in pipeline order.
//
// First each stage armed alone, then every run of two or more consecutive
// stages after the Host check armed together, each run ending at the last
// stage. Alone, a stage shows which endpoints run it; together, the first
// stage an endpoint runs must be the one that answers, which holds route()'s
// order (the credential before the stamp whose refusal discloses this
// server's project id, both before a slot) on every endpoint.
//
// Then each of those rounds whose first stage is behind the credential runs
// again behind passes, once for every combination of the stages from the
// credential to that first stage. A stage a combination leaves out is one the
// server does not configure: "bearer+stamp+revision+slot" is a remote bd
// serve, which runs no hook, and a client that pinned its project;
// "passhook+project" is an embedder's server that checks no credential.
// Behind the passes, the same order holds for a request that got past every
// stage in front of it.
//
// Last, the Host round runs again with every pass: the Host policy holds on a
// server that checks a credential, runs a hook and knows its project id, for
// a request that would get past all of them.
func pipelineRounds() [][]*pipelineStage {
	stageAt := func(name string) int {
		return slices.IndexFunc(pipelineStages, func(st pipelineStage) bool { return st.name == name })
	}
	host, credential := stageAt("host"), stageAt("auth")
	var rounds, passed [][]*pipelineStage
	arm := func(from, to int) {
		var armed []*pipelineStage
		for i := from; i < to; i++ {
			armed = append(armed, &pipelineStages[i])
		}
		rounds = append(rounds, armed)
		if from <= credential {
			return
		}
		// Bit k of a combination passes stage credential+k.
		for combo := 1; combo < 1<<(from-credential); combo++ {
			var round []*pipelineStage
			for k := range from - credential {
				if p := pipelineStages[credential+k].pass; p != nil && combo&(1<<k) != 0 {
					round = append(round, p)
				}
			}
			passed = append(passed, append(round, armed...))
		}
	}
	for i := range pipelineStages {
		arm(i, i+1)
	}
	for i := 1; i+1 < len(pipelineStages); i++ {
		arm(i, len(pipelineStages))
	}
	everyPass := []*pipelineStage{&pipelineStages[host]}
	for _, st := range pipelineStages {
		if st.pass != nil {
			everyPass = append(everyPass, st.pass)
		}
	}
	return append(append(rounds, passed...), everyPass)
}

// firstRun is the first stage armed in a round that e declares it runs, which
// is the stage that must answer it, or nil when e skips them all.
func firstRun(armed []*pipelineStage, e mountEndpoint) *pipelineStage {
	for _, st := range armed {
		if !st.exempt(e) {
			return st
		}
	}
	return nil
}

// wantHookCalls is how many times the RequestContext hook must run for e in a
// round: once when the round installs a hook and e reaches it, whether the
// hook then refuses e or hands it on, and never otherwise: not for a request
// a stage in front of the hook refused, nor for an unrouted one.
func wantHookCalls(armed []*pipelineStage, e mountEndpoint) int {
	if e.row == nil {
		return 0
	}
	for _, st := range armed {
		if st.installsHook {
			return 1
		}
		if !st.exempt(e) {
			return 0
		}
	}
	return 0
}

// roundName is "auth" for one stage, "auth+hook+project+wire+slot" for a run.
func roundName(armed []*pipelineStage) string {
	var names []string
	for _, st := range armed {
		names = append(names, st.name)
	}
	return strings.Join(names, "+")
}

// censusPipeline runs the whole census and returns every violation.
func censusPipeline(t *testing.T, rig censusRig) []pipelineViolation {
	t.Helper()
	var out []pipelineViolation
	note := func(round, endpoint, format string, args ...any) {
		out = append(out, pipelineViolation{round: round, endpoint: endpoint, what: fmt.Sprintf(format, args...)})
	}
	noDatabaseWork := func(round string, uows int) {
		if uows != 0 {
			note(round, "(server)", "opened %d units of work; every probe must be answered before any database work", uows)
		}
	}

	eps, baseline, uows := probePipeline(t, nil, rig)
	noDatabaseWork("baseline", uows)
	// The baseline: an operation refuses the probe at its own door; an unrouted
	// answer is the catch-all's 404. An endpoint whose baseline is wrong is
	// reported once here and not judged further: its other answers cannot be
	// attributed.
	usable := make([]bool, len(eps))
	for i, e := range eps {
		a := baseline[i]
		switch {
		case !a.logged:
			note("baseline", e.name(), "answered %s with no request line: it never passed through withRequestContext", a)
		case !slices.Equal(a.cacheControl, []string{"no-store"}):
			note("baseline", e.name(), "answered with Cache-Control %q", a.cacheControl)
		case e.row != nil && (a.status != http.StatusBadRequest || a.reason != string(ReasonUnknownParameter) || a.param != pipelineProbe || a.op != e.op()):
			note("baseline", e.name(), "answered %s, not its own refusal of the probe", a)
		case e.row == nil && (a.status != http.StatusNotFound || a.code != string(CodeNotFound) || a.op != ""):
			note("baseline", e.name(), "answered %s, not the unrouted 404", a)
		default:
			usable[i] = true
		}
	}

	for _, armed := range pipelineRounds() {
		round := roundName(armed)
		roundEps, answers, uows := probePipeline(t, armed, rig)
		if len(roundEps) != len(eps) {
			t.Fatalf("%s: %d endpoints, the baseline server had %d", round, len(roundEps), len(eps))
		}
		noDatabaseWork(round, uows)
		for i, e := range eps {
			if !usable[i] {
				continue
			}
			first := firstRun(armed, e)
			a := answers[i]
			switch {
			case !a.logged:
				note(round, e.name(), "answered %s with no request line", a)
			case !slices.Equal(a.cacheControl, []string{"no-store"}):
				note(round, e.name(), "answered with Cache-Control %q", a.cacheControl)
			case first == nil && !a.same(baseline[i]):
				note(round, e.name(), "declares it skips every stage armed here but answered %s, not its baseline %s", a, baseline[i])
			case first != nil && !first.refused(a):
				note(round, e.name(), "should be refused by %s, the first stage armed here it declares it runs, but answered %s", first.name, a)
			case first != nil && first.booksOp && a.op != e.op():
				note(round, e.name(), "was refused by %s, but its request line booked op %q, want %q", first.name, a.op, e.op())
			case first != nil && !first.booksOp && a.op != "":
				note(round, e.name(), "was refused by %s, which answers before the router, but its request line booked op %q", first.name, a.op)
			}
			// Checked apart from the answer, which a hook that hands the
			// request on leaves unchanged.
			if want := wantHookCalls(armed, e); a.hookCalls != want {
				note(round, e.name(), "ran the RequestContext hook %d times, want %d", a.hookCalls, want)
			}
		}
		if len(armed) != 1 {
			continue
		}
		var declared []string
		for _, e := range eps {
			if armed[0].exempt(e) {
				declared = append(declared, e.name())
			}
		}
		slices.Sort(declared)
		want := slices.Clone(pipelineExemptions[round])
		slices.Sort(want)
		if !slices.Equal(declared, want) {
			note(round, "(exemptions)", "the endpoints declaring they skip this stage are %v, the reviewed list is %v: unreviewed %v, no longer declared %v",
				declared, want, notIn(declared, want), notIn(want, declared))
		}
	}
	return out
}

// TestEveryMountedHandlerRunsTheRequestPipeline is the census over the router
// handler() builds for a default server.
func TestEveryMountedHandlerRunsTheRequestPipeline(t *testing.T) {
	if len(pipelineExemptions) != len(pipelineStages) {
		t.Errorf("pipelineExemptions has %d stages, pipelineStages %d: every stage needs its reviewed list", len(pipelineExemptions), len(pipelineStages))
	}
	for _, st := range pipelineStages[1 : len(pipelineStages)-1] {
		if st.pass == nil {
			t.Errorf("stage %s has no pass: no round gets a request past it to the stages behind it", st.name)
		}
	}
	for _, v := range censusPipeline(t, nil) {
		t.Error(v)
	}
}

// TestPipelineCensusCatchesABypassedStage is the census's falsification. The
// census is green against a router written to satisfy it, so this serves a
// router with one planted defect on each of nine endpoints at once and
// requires the census to report exactly each defect's (round, endpoint) pairs
// and nothing else. A Host policy that answers a foreign Host is a tenth
// defect, run apart because it touches every endpoint. The pairs are derived
// from pipelineRounds(), so a stage added to the pipeline moves them with it.
func TestPipelineCensusCatchesABypassedStage(t *testing.T) {
	type defect struct {
		name, op string
		// plant returns the mount that replaces op's.
		plant func(s *Server, m mount) mount
		want  []string
	}
	gate := func(h func(s *Server, rt route) http.Handler) func(*Server, mount) mount {
		return func(s *Server, m mount) mount {
			m.handler = h(s, m.rows[0])
			return m
		}
	}
	// reportedIn is the pairs a defect planted on op's endpoint owes: one for
	// each round the defect breaks, judged from the stages the round arms, the
	// first of them the endpoint runs (nil when it runs none) and the hook
	// calls the census wants for it. A round that arms the Host check is never
	// one of them: it refuses every request before the router reaches a mount.
	reportedIn := func(op string, breaks func(armed []*pipelineStage, first *pipelineStage, hookCalls int) bool) []string {
		e := mountEndpoint{row: &routeTable[slices.IndexFunc(routeTable, func(rt route) bool { return rt.op == op })]}
		var out []string
		for _, armed := range pipelineRounds() {
			if armed[0].name != "host" && breaks(armed, firstRun(armed, e), wantHookCalls(armed, e)) {
				out = append(out, roundName(armed)+" "+op)
			}
		}
		return out
	}
	defects := []defect{
		{
			// A second pipeline that re-implements the credential and the
			// stamp, but not the hook, the floor or the slot.
			name: "a gate that skips the hook, the wire floor and the slot",
			op:   OpListReadyWork,
			plant: gate(func(s *Server, rt route) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					rec := requestInfo(r.Context())
					rec.op = rt.op
					if s.auth != nil && !s.authorize(w, r, rec) {
						return
					}
					if res := s.checkProjectStamp(r); res != nil {
						s.fail(w, r, *res)
						return
					}
					rt.handler(s, w, r)
				})
			}),
			// Every round whose first stage is one the gate does not run (it
			// runs the credential and the stamp), and every round with a hook
			// the request reaches, which the gate never calls.
			want: reportedIn(OpListReadyWork, func(_ []*pipelineStage, first *pipelineStage, hookCalls int) bool {
				return (first != nil && !slices.Contains([]string{"auth", "project"}, first.name)) || hookCalls != 0
			}),
		},
		{
			// Every stage still refuses alone; only the order shows a 401 storm
			// holding the slots that real clients wait for.
			name: "a gate that takes the slot before the credential",
			op:   OpListBlockedIssues,
			plant: gate(func(s *Server, rt route) http.Handler {
				unslotted := rt
				unslotted.bypassSemaphore = true
				h := s.route(unslotted)
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					rec := requestInfo(r.Context())
					release, err := s.acquire(r.Context(), rec)
					if err != nil {
						rec.op = rt.op
						s.failErr(w, r, err)
						return
					}
					defer release()
					h.ServeHTTP(w, r)
				})
			}),
			// Every round that arms the slot behind a stage the endpoint runs,
			// or behind a hook it reaches: the gate takes the slot first.
			want: reportedIn(OpListBlockedIssues, func(armed []*pipelineStage, first *pipelineStage, hookCalls int) bool {
				return first != nil && (first.name != "slot" || hookCalls != 0) &&
					slices.ContainsFunc(armed, func(st *pipelineStage) bool { return st.name == "slot" })
			}),
		},
		{
			// A credential check that ends the pipeline: a request that passes
			// it goes straight to the handler, past the hook, the stamp, the
			// floor and the slot. A server with no credential configured still
			// runs them all, and a request that presents none still gets its
			// 401, so only the rounds behind the bearer can tell.
			name: "a gate that hands an authenticated request to the handler",
			op:   OpGetIssue,
			plant: gate(func(s *Server, rt route) http.Handler {
				h := s.route(rt)
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if s.auth == nil {
						h.ServeHTTP(w, r)
						return
					}
					rec := requestInfo(r.Context())
					rec.op = rt.op
					if s.authorize(w, r, rec) {
						rt.handler(s, w, r)
					}
				})
			}),
			want: reportedIn(OpGetIssue, func(armed []*pipelineStage, first *pipelineStage, hookCalls int) bool {
				return slices.Contains(armed, &pipelineBearer) && (first != nil || hookCalls != 0)
			}),
		},
		{
			// The hook as the last check: a request it hands on goes straight
			// to the handler, past the stamp, the floor and the slot. A hook
			// that refuses still refuses, so only the rounds behind a hook
			// that hands requests on can tell.
			name: "a gate that hands a request the hook accepted to the handler",
			op:   OpCountReadyWork,
			plant: gate(func(s *Server, rt route) http.Handler {
				if s.cfg.RequestContext != nil {
					rt.projectExempt, rt.wireRevisionExempt, rt.bypassSemaphore = true, true, true
				}
				return s.route(rt)
			}),
			want: reportedIn(OpCountReadyWork, func(armed []*pipelineStage, first *pipelineStage, _ int) bool {
				return slices.Contains(armed, &pipelinePassingHook) && first != nil
			}),
		},
		{
			// A matching stamp that ends the pipeline: a request stamped with
			// this server's project, as every pinned bd client stamps it, skips
			// the floor and the slot.
			name: "a gate that hands a request with a matching stamp to the handler",
			op:   OpQueryIssues,
			plant: gate(func(s *Server, rt route) http.Handler {
				h := s.route(rt)
				rt.wireRevisionExempt, rt.bypassSemaphore = true, true
				matched := s.route(rt)
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if stamp := r.Header.Get(ProjectIDHeader); stamp != "" && stamp == s.ctxBody.ProjectId {
						matched.ServeHTTP(w, r)
						return
					}
					h.ServeHTTP(w, r)
				})
			}),
			want: reportedIn(OpQueryIssues, func(armed []*pipelineStage, first *pipelineStage, _ int) bool {
				return slices.Contains(armed, &pipelineMatchingStamp) && first != nil
			}),
		},
		{
			// A wire revision that ends the pipeline once the floor admits it:
			// a request declaring one, as bd's client declares it on every
			// request, skips the slot.
			name: "a gate that hands a request with an admitted wire revision to the handler",
			op:   OpListDependencyCycles,
			plant: gate(func(s *Server, rt route) http.Handler {
				h := s.route(rt)
				rt.bypassSemaphore = true
				admitted := s.route(rt)
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if got, ok := parseStrictNonNegativeInt(r.Header.Get(WireRevisionHeader)); ok && got >= s.minClientWireRevision {
						admitted.ServeHTTP(w, r)
						return
					}
					h.ServeHTTP(w, r)
				})
			}),
			want: reportedIn(OpListDependencyCycles, func(armed []*pipelineStage, first *pipelineStage, _ int) bool {
				return slices.Contains(armed, &pipelineCurrentRevision) && first != nil
			}),
		},
		{
			name: "an answer booked to no op",
			op:   OpGetStats,
			plant: gate(func(s *Server, rt route) http.Handler {
				h := s.route(rt)
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					h.ServeHTTP(w, r)
					requestInfo(r.Context()).op = ""
				})
			}),
			want: []string{"baseline " + OpGetStats},
		},
		{
			name: "an exemption nobody reviewed",
			op:   OpListIssues,
			plant: func(s *Server, m mount) mount {
				rt := m.rows[0]
				rt.wireRevisionExempt = true
				return mount{pattern: m.pattern, handler: s.route(rt), rows: []route{rt}}
			},
			want: []string{"wire (exemptions)"},
		},
		{
			name: "database work before the refusal",
			op:   OpCountIssues,
			plant: gate(func(s *Server, rt route) http.Handler {
				h := s.route(rt)
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_ = s.WithUOW(r.Context(), nil, func(uow.UnitOfWork) error { return nil })
					h.ServeHTTP(w, r)
				})
			}),
			// Every server but the one whose Host refusal comes before any mount.
			want: func() []string {
				out := []string{"baseline (server)"}
				for _, armed := range pipelineRounds() {
					if armed[0].name != "host" {
						out = append(out, roundName(armed)+" (server)")
					}
				}
				return out
			}(),
		},
	}
	planted := func(s *Server) []mount {
		ms := s.mounts()
		for i, m := range ms {
			for _, d := range defects {
				if len(m.rows) == 1 && m.rows[0].op == d.op {
					ms[i] = d.plant(s, m)
				}
			}
		}
		return ms
	}
	got := map[string]bool{}
	for _, v := range censusPipeline(t, planted) {
		got[v.round+" "+v.endpoint] = true
	}
	for _, d := range defects {
		for _, w := range d.want {
			if !got[w] {
				t.Errorf("%s: the census did not report %q", d.name, w)
			}
			delete(got, w)
		}
	}
	for _, extra := range slices.Sorted(maps.Keys(got)) {
		t.Errorf("the census reported %q, which no planted defect explains", extra)
	}

	t.Run("a Host policy that answers a foreign Host", func(t *testing.T) {
		got := map[string]bool{}
		for _, v := range censusPipeline(t, func(s *Server) []mount {
			s.hosts.names["rebind.example"] = true
			return s.mounts()
		}) {
			got[v.round+" "+v.endpoint] = true
		}
		// Every endpoint, in each round that arms the Host check.
		var want []string
		for _, armed := range pipelineRounds() {
			if armed[0].name != "host" {
				continue
			}
			for _, e := range mountEndpoints(t, defaultMounts(t)) {
				want = append(want, roundName(armed)+" "+e.name())
			}
		}
		slices.Sort(want)
		if reported := slices.Sorted(maps.Keys(got)); !slices.Equal(reported, want) {
			t.Errorf("the census reported %d (round, endpoint) pairs %v\nwant every endpoint in each round that arms the Host check, %d %v", len(reported), reported, len(want), want)
		}
	})
}
