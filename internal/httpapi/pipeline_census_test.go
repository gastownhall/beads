package httpapi

// The request-pipeline census: every endpoint of every mount, held at every
// stage of the request pipeline to the posture its row declares.
//
// route() runs the pipeline for an operation: the bearer credential, the
// Bd-Project-Id stamp, the Bd-Wire-Revision floor and a database slot, in that
// order, each unless the row declares itself exempt; handler() puts the Host
// policy in front of every mount. Each stage has tests of its own, but before
// the mount table every "every route" loop ranged over routeTable, so a
// handler mounted beside the table, or a second pipeline that re-implemented
// some stages and not others, went unseen.
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
// be the one that answers. Three layers, none of which a regeneration can
// relax:
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
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
)

// pipelineProbe is the unknown query parameter every census request carries.
const pipelineProbe = "pipeline_probe"

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
}

// pipelineStages are the stages in the order the pipeline runs them.
//
// WAITING ON #7987 (RequestContext hook): once Config.RequestContext exists,
// add a "hook" stage between auth and project. Its config installs a hook
// that counts its calls and returns &RequestRefusal{Status: 403, Code:
// "census_refused"}; refused is 403 census_refused; exempt is the unrouted
// answers only (the hook runs for every routed request, liveness included);
// booksOp is true; pipelineExemptions["hook"] lists the two unrouted answers;
// and the census also asserts the hook ran exactly once per routed endpoint
// that reached it and never for an unrouted one.
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
		config: func(t *testing.T, cfg *Config) { cfg.Auth = authTokenFile(t, "census-token") },
		exempt: func(e mountEndpoint) bool { return e.row == nil || e.row.authExempt },
		refused: func(a pipelineAnswer) bool {
			return a.status == http.StatusUnauthorized && a.code == string(CodeUnauthenticated)
		},
		booksOp: true,
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
	for i, e := range eps {
		if f, ok := lines[e.method+" "+e.path]; ok {
			answers[i].logged, answers[i].op = true, f["op"]
		}
	}
	return eps, answers, len(provider.openedUOWs())
}

// pipelineRounds are the servers the census stands up after the baseline:
// each stage armed alone, then every run of two or more consecutive stages
// after the Host check, armed together. Alone, a stage shows which endpoints
// run it; together, the first stage an endpoint runs must be the one that
// answers, which holds route()'s order (the credential before the stamp whose
// refusal discloses this server's project id, both before a slot) on every
// endpoint.
func pipelineRounds() [][]*pipelineStage {
	var rounds [][]*pipelineStage
	for i := range pipelineStages {
		rounds = append(rounds, []*pipelineStage{&pipelineStages[i]})
	}
	for i := 1; i+1 < len(pipelineStages); i++ {
		var run []*pipelineStage
		for j := i; j < len(pipelineStages); j++ {
			run = append(run, &pipelineStages[j])
		}
		rounds = append(rounds, run)
	}
	return rounds
}

// roundName is "auth" for one stage, "auth+project+wire+slot" for a run.
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
			var first *pipelineStage
			for _, st := range armed {
				if !st.exempt(e) {
					first = st
					break
				}
			}
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
	for _, v := range censusPipeline(t, nil) {
		t.Error(v)
	}
}

// TestPipelineCensusCatchesABypassedStage is the census's falsification. The
// census is green against a router written to satisfy it, so this serves a
// router with one planted defect on each of five endpoints at once and
// requires the census to report exactly each defect's (round, endpoint) pairs
// and nothing else. A Host policy that answers a foreign Host is a sixth
// defect, run apart because it touches every endpoint.
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
	defects := []defect{
		{
			// A second pipeline that re-implements the credential and the
			// stamp, but not the floor or the slot.
			name: "a gate that skips the wire floor and the slot",
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
			want: []string{"wire " + OpListReadyWork, "slot " + OpListReadyWork, "wire+slot " + OpListReadyWork},
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
			want: []string{"auth+project+wire+slot " + OpListBlockedIssues, "project+wire+slot " + OpListBlockedIssues, "wire+slot " + OpListBlockedIssues},
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
		var got, want []string
		for _, v := range censusPipeline(t, func(s *Server) []mount {
			s.hosts.names["rebind.example"] = true
			return s.mounts()
		}) {
			got = append(got, v.round+" "+v.endpoint)
		}
		for _, e := range mountEndpoints(t, defaultMounts(t)) {
			want = append(want, "host "+e.name())
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("the census reported %d violations %v\nwant one per endpoint, %d %v", len(got), got, len(want), want)
		}
	})
}
