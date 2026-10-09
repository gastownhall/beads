//go:build cgo

package corpus_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi"
	"github.com/steveyegge/beads/internal/httpapi/apigen"
)

// referenceProfile is testdata/reference_server.json: the wire surface of the
// deployed server the corpus must work against. See README.md.
type referenceProfile struct {
	Capabilities   []string             `json:"capabilities"`
	ContextMembers []string             `json:"context_members"`
	Operations     []referenceOperation `json:"operations"`
}

type referenceOperation struct {
	Method      string   `json:"method"`
	Path        string   `json:"path"`
	OperationID string   `json:"operation_id"`
	Query       []string `json:"query"`
	Body        []string `json:"body"`

	pattern *regexp.Regexp
	query   map[string]bool
	body    map[string]bool
}

func loadReferenceProfile(t *testing.T) *referenceProfile {
	t.Helper()
	raw, err := os.ReadFile("testdata/reference_server.json")
	if err != nil {
		t.Fatalf("read the reference server profile: %v", err)
	}
	var p referenceProfile
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("parse testdata/reference_server.json: %v", err)
	}
	if len(p.Capabilities) == 0 || len(p.Operations) == 0 || len(p.ContextMembers) == 0 {
		t.Fatal("testdata/reference_server.json is missing capabilities, operations or context_members")
	}
	for i := range p.Operations {
		op := &p.Operations[i]
		op.pattern = templatePattern(op.Path)
		op.query = setOf(op.Query)
		op.body = setOf(op.Body)
	}
	return &p
}

// templatePattern turns an OpenAPI path template into an anchored regexp. A
// path parameter never spans a "/" or a ":" (AIP-136 custom methods hang off
// the last segment as {id}:verb, and no issue id, config key or memory key
// carries a raw colon), so /issues/{id} does not swallow /issues/{id}:claim.
func templatePattern(tmpl string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for {
		open := strings.IndexByte(tmpl, '{')
		if open < 0 {
			b.WriteString(regexp.QuoteMeta(tmpl))
			break
		}
		closeAt := strings.IndexByte(tmpl[open:], '}')
		b.WriteString(regexp.QuoteMeta(tmpl[:open]))
		b.WriteString(`[^/:]+`)
		tmpl = tmpl[open+closeAt+1:]
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

func setOf(xs []string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// referenceProxy fronts the in-process server and makes it answer the way the
// reference server answers, at the wire level, for everything this suite can
// see from a client:
//
//   - GET /v0/beads/context publishes only the reference ContextResponse
//     members (so no wire_revision: the reference server predates it) and
//     exactly the reference capability set, whatever this build advertises;
//   - a request for an operation the reference document does not publish gets
//     the server's own unrouted 404 problem;
//   - a query parameter or top-level JSON request member the reference
//     operation does not declare gets the server's own 400 unknown_parameter
//     problem naming it — which is how the real reference server answers,
//     since its decoder is strict.
//
// Responses are otherwise passed through untouched. That is deliberately
// generous in one direction (a response member the reference server would not
// send still reaches the client) and is the residual fidelity gap README.md
// records.
type referenceProxy struct {
	profile  *referenceProfile
	upstream *url.URL
	rp       *httputil.ReverseProxy

	requests atomic.Int64

	mu      sync.Mutex
	refused []string // "METHOD path: why", for the per-shape log
}

func newReferenceProxy(t *testing.T, profile *referenceProfile, upstreamAddr string) *referenceProxy {
	t.Helper()
	u, err := url.Parse("http://" + upstreamAddr)
	if err != nil {
		t.Fatalf("parse upstream address: %v", err)
	}
	p := &referenceProxy{profile: profile, upstream: u}
	p.rp = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(u) // also rewrites Host, so the server's Host gate sees its own bind
		},
		FlushInterval: -1, // events:watch streams
	}
	return p
}

func (p *referenceProxy) takeRefusals() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.refused
	p.refused = nil
	return out
}

func (p *referenceProxy) noteRefusal(r *http.Request, why string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refused = append(p.refused, fmt.Sprintf("%s %s: %s", r.Method, r.URL.Path, why))
}

func (p *referenceProxy) match(r *http.Request) *referenceOperation {
	path := r.URL.EscapedPath()
	var best *referenceOperation
	for i := range p.profile.Operations {
		op := &p.profile.Operations[i]
		if op.Method != r.Method || !op.pattern.MatchString(path) {
			continue
		}
		// Prefer the most literal template when two match (none do today).
		if best == nil || len(op.Path) > len(best.Path) {
			best = op
		}
	}
	return best
}

func (p *referenceProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.requests.Add(1)
	if r.Method == http.MethodGet && r.URL.Path == "/v0/beads/context" {
		p.serveContext(w, r)
		return
	}
	op := p.match(r)
	if op == nil {
		p.noteRefusal(r, "operation not published by the reference server")
		detail := "no such route on this server"
		httpapi.Write(w, httpapi.Result{Problem: apigen.Problem{
			Status: http.StatusNotFound,
			Title:  http.StatusText(http.StatusNotFound),
			Code:   string(httpapi.CodeNotFound),
			Detail: &detail,
		}})
		return
	}
	if offender := firstUnknown(slices.Collect(maps.Keys(r.URL.Query())), op.query); offender != "" {
		p.noteRefusal(r, "query parameter "+offender+" unknown to "+op.OperationID)
		httpapi.Write(w, httpapi.InvalidArgument(offender, httpapi.ReasonUnknownParameter,
			"unknown query parameter "+offender))
		return
	}
	if r.Body != nil && r.Body != http.NoBody {
		raw, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if err != nil {
			http.Error(w, "reference proxy: read body: "+err.Error(), http.StatusBadGateway)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		var members map[string]json.RawMessage
		if len(bytes.TrimSpace(raw)) > 0 && json.Unmarshal(raw, &members) == nil {
			if offender := firstUnknown(slices.Collect(maps.Keys(members)), op.body); offender != "" {
				p.noteRefusal(r, "request member "+offender+" unknown to "+op.OperationID)
				httpapi.Write(w, httpapi.InvalidArgument(offender, httpapi.ReasonUnknownParameter,
					"json: unknown field \""+offender+"\""))
				return
			}
		}
	}
	p.rp.ServeHTTP(w, r)
}

// firstUnknown names the smallest key not in known, the same deterministic
// choice the server makes, or "" when every key is known.
func firstUnknown(keys []string, known map[string]bool) string {
	sort.Strings(keys)
	for _, k := range keys {
		if !known[k] {
			return k
		}
	}
	return ""
}

func (p *referenceProxy) serveContext(w http.ResponseWriter, r *http.Request) {
	out := r.Clone(r.Context())
	out.URL.Scheme, out.URL.Host, out.Host = p.upstream.Scheme, p.upstream.Host, p.upstream.Host
	out.RequestURI = ""
	resp, err := http.DefaultTransport.RoundTrip(out)
	if err != nil {
		http.Error(w, "reference proxy: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "reference proxy: "+err.Error(), http.StatusBadGateway)
		return
	}
	if resp.StatusCode != http.StatusOK {
		maps.Copy(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(raw)
		return
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		http.Error(w, "reference proxy: decode context: "+err.Error(), http.StatusBadGateway)
		return
	}
	keep := setOf(p.profile.ContextMembers)
	for k := range body {
		if !keep[k] {
			delete(body, k)
		}
	}
	caps, _ := json.Marshal(p.profile.Capabilities)
	body["capabilities"] = caps
	masked, _ := json.Marshal(body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(masked)
}
