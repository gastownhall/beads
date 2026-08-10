package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// These tests are pure: no database, no cgo, no build tag. The whole request
// lifecycle — limits, middleware, logging, shutdown — is exercised against a
// fake unit-of-work provider over a real listener on 127.0.0.1:0, so it runs in
// the PR workflow's unconditional Go test job rather than in a conditional cgo
// shard.

// fakeUOW embeds the interface so any method this test has not stubbed panics
// instead of silently returning a zero value.
type fakeUOW struct {
	uow.UnitOfWork
	mu sync.Mutex
	// Recorded AT Close time. The state of the close context afterwards says
	// nothing: WithUOW cancels it on the way out, as it must.
	closed           bool
	closeErr         error
	closeHasDeadline bool

	// issues is the use case the claim path drives, shared by every unit of
	// work this provider hands out so a retry sees the same state. commits
	// records what Commit was asked to write — for a claim, the audit-trail
	// line the actor is interpolated into.
	issues     *fakeIssues
	readIssues domain.IssueUseCase
	readConfig domain.ConfigUseCase
	commits    []string
}

func (u *fakeUOW) IssueUseCase() domain.IssueUseCase {
	if u.readIssues != nil {
		return u.readIssues
	}
	return u.issues
}

// ConfigUseCase answers the list reader's config load. It is nil for the claim
// path, which never asks for one.
func (u *fakeUOW) ConfigUseCase() domain.ConfigUseCase { return u.readConfig }

// LabelUseCase and DependencyUseCase answer the hydration issueops.Lifecycle
// runs over the row a claim wrote. Neither carries state: the claimed row's
// identity is what these tests assert on, and a fake that invented labels or
// edges would be a second source of truth for it. The full-item shape is pinned
// against the CLI by the parity oracle in cmd/bd, over real Dolt.
func (u *fakeUOW) LabelUseCase() domain.LabelUseCase           { return emptyLabels{} }
func (u *fakeUOW) DependencyUseCase() domain.DependencyUseCase { return emptyDeps{} }

type emptyLabels struct{ domain.LabelUseCase }

func (emptyLabels) GetLabels(context.Context, string) ([]string, error) { return nil, nil }

type emptyDeps struct{ domain.DependencyUseCase }

func (emptyDeps) GetIssueDependencyRecords(context.Context, []string) (map[string][]*types.Dependency, error) {
	return nil, nil
}

// DetectCycleReport answers for a workspace with no cycles. Without it the
// promoted method on the embedded nil interface panics, and the provider-backed
// cycle route would 500 through the panic recovery rather than answering.
func (emptyDeps) DetectCycleReport(context.Context) (issueops.CycleReport, error) {
	return issueops.CycleReport{Cycles: []issueops.Cycle{}}, nil
}

// WalkDependencyTree answers a one-node tree, present for the same reason
// DetectCycleReport is.
//
// A ONE-NODE tree rather than an empty one, because that is what the role
// promises for a root with no edges, and a fake that answered nothing would let
// a handler which dropped the root pass.
func (emptyDeps) WalkDependencyTree(_ context.Context, req issueops.WalkTreeRequest) (issueops.TreeResult, error) {
	return issueops.TreeResult{Nodes: []*types.TreeNode{{Issue: types.Issue{ID: req.RootID}}}}, nil
}

func (u *fakeUOW) Commit(_ context.Context, message string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.commits = append(u.commits, message)
	return nil
}

func (u *fakeUOW) commitMessages() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.commits)
}

func (u *fakeUOW) Close(ctx context.Context) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.closed = true
	u.closeErr = ctx.Err()
	_, u.closeHasDeadline = ctx.Deadline()
}

func (u *fakeUOW) closeState() (closed bool, err error, hasDeadline bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.closed, u.closeErr, u.closeHasDeadline
}

type fakeProvider struct {
	mu    sync.Mutex
	uows  []*fakeUOW
	err   error
	delay time.Duration
	// issues answers the claim path. readIssues, when set, answers the read
	// paths instead, so a read test can record the filter the reader built
	// without teaching the claim fake to answer queries it has no opinion on.
	issues     *fakeIssues
	readIssues domain.IssueUseCase
	readConfig domain.ConfigUseCase
}

func (p *fakeProvider) NewUOW(ctx context.Context) (uow.UnitOfWork, error) {
	if p.delay > 0 {
		select {
		case <-time.After(p.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if p.err != nil {
		return nil, p.err
	}
	u := &fakeUOW{issues: p.issues, readIssues: p.readIssues, readConfig: p.readConfig}
	p.mu.Lock()
	p.uows = append(p.uows, u)
	p.mu.Unlock()
	return u, nil
}

// openedUOWs is the count of units of work this provider handed out — zero is
// the assertion that a refusal happened before any database work.
func (p *fakeProvider) openedUOWs() []*fakeUOW {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.uows)
}

func (p *fakeProvider) Close(context.Context) error { return nil }

// tunableProvider is the provider shape serve expects: one that can be told
// how many connections it may open.
type tunableProvider struct {
	fakeProvider
	limits uow.PoolLimits
	set    bool
}

func (p *tunableProvider) SetPoolLimits(l uow.PoolLimits) {
	p.limits = l
	p.set = true
}

type testServer struct {
	*Server
	stdout *bytes.Buffer
	stderr *lockedBuffer
	client *http.Client
	base   string
}

// lockedBuffer is the stderr sink. The server logs from handler goroutines, so
// reading it from the test goroutine needs the lock.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// newTestServer binds and serves one server for a case. The optional tune
// functions run between Listen and the first accepted connection, which is
// where the millisecond-scale knobs (semTimeout, writeStall, the stream
// cadences and the stream cap) belong: they are fields rather than Config
// members precisely because they are not deployment configuration.
func newTestServer(t *testing.T, cfg Config, tune ...func(*Server)) *testServer {
	t.Helper()
	stdout := &bytes.Buffer{}
	stderr := &lockedBuffer{}
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:0"
	}
	// The default source, for the tests that care about something else. A
	// config that already names a source — either one — keeps it: defaulting a
	// provider onto a roles-backed config would serve the wrong one and pass.
	if cfg.Provider == nil && cfg.Reader == nil && cfg.Claimer == nil && cfg.CycleDetector == nil {
		cfg.Provider = &fakeProvider{}
	}
	cfg.Stdout = stdout
	cfg.Stderr = stderr

	srv, err := Listen(cfg)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	for _, apply := range tune {
		apply(srv)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Error("Serve did not return after shutdown")
		}
	})

	return &testServer{
		Server: srv,
		stdout: stdout,
		stderr: stderr,
		client: &http.Client{Timeout: 20 * time.Second},
		base:   "http://" + srv.Addr(),
	}
}

func (ts *testServer) get(t *testing.T, path string) *http.Response {
	t.Helper()
	resp, err := ts.client.Get(ts.base + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func decodeBody(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return m
}

func TestValidateBindAddr(t *testing.T) {
	for _, tc := range []struct {
		name             string
		addr             string
		allowNonLoopback bool
		wantErr          string
	}{
		{name: "loopback ephemeral", addr: "127.0.0.1:0"},
		{name: "loopback fixed", addr: "127.0.0.1:8080"},
		{name: "ipv6 loopback", addr: "[::1]:8080"},
		{name: "alternate loopback ip", addr: "127.0.0.2:8080"},

		// A name is not a listener specification: it resolves to whatever the
		// resolver says today, so the operator cannot tell from the flag which
		// interfaces they opened. This is the same rule the managed Dolt child
		// lives under.
		{name: "localhost refused", addr: "localhost:8080", wantErr: "numeric IP literal"},
		{name: "hostname refused", addr: "example.internal:8080", wantErr: "numeric IP literal"},

		// A unix socket bypasses the loopback TCP boundary entirely.
		{name: "unix socket refused", addr: "/tmp/bd.sock", wantErr: "unix sockets are not supported"},
		{name: "bare port refused", addr: "8080", wantErr: "HOST:PORT"},
		{name: "no port refused", addr: "127.0.0.1", wantErr: "HOST:PORT"},
		{name: "service name refused", addr: "127.0.0.1:http", wantErr: "port must be a number"},
		{name: "empty host refused", addr: ":8080", wantErr: "numeric IP literal"},

		{name: "non-loopback needs opt-in", addr: "10.0.0.5:8080", wantErr: "--allow-non-loopback"},
		{name: "non-loopback with opt-in", addr: "10.0.0.5:8080", allowNonLoopback: true},
		{name: "wildcard with opt-in", addr: "0.0.0.0:8080", allowNonLoopback: true},
		{name: "wildcard needs opt-in", addr: "0.0.0.0:8080", wantErr: "--allow-non-loopback"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateBindAddr(tc.addr, tc.allowNonLoopback)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateBindAddr(%q, %v) = %v, want no error", tc.addr, tc.allowNonLoopback, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateBindAddr(%q, %v) succeeded, want refusal mentioning %q", tc.addr, tc.allowNonLoopback, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestHealthz(t *testing.T) {
	ts := newTestServer(t, Config{})

	resp := ts.get(t, "/healthz")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	// A cached liveness answer is worse than none.
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if body := decodeBody(t, resp); body["status"] != "ok" {
		t.Errorf("body = %v, want status ok", body)
	}
}

// TestHealthzStaysUpWhileTheDatabaseIsDown pins the property that makes
// /healthz liveness-only, and the reason /context shares it: neither touches
// the provider, so both answer while every database slot is held or the
// database is unreachable.
func TestHealthzStaysUpWhileTheDatabaseIsDown(t *testing.T) {
	dead := &fakeProvider{err: errors.New("dial tcp 127.0.0.1:3306: connect: connection refused")}
	ts := newTestServer(t, Config{Provider: dead})

	for _, path := range []string{"/healthz", "/v0/beads/context"} {
		if resp := ts.get(t, path); resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200 with the database unreachable", path, resp.StatusCode)
		}
	}
}

func TestNoQueryParametersAccepted(t *testing.T) {
	ts := newTestServer(t, Config{})

	for _, tc := range []struct{ path, param string }{
		{"/healthz?verbose=1", "verbose"},
		{"/v0/beads/context?fields=all", "fields"},
		// The degenerate spelling: url.Values keys this under the empty string,
		// and the document promises `param` on every 400 except a body that
		// fails to parse at all. Naming it as "" is the honest answer — the
		// offending parameter name really is empty.
		{"/healthz?=1", ""},
	} {
		resp := ts.get(t, tc.path)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("GET %s status = %d, want 400", tc.path, resp.StatusCode)
		}
		if got := resp.Header.Get("Content-Type"); got != "application/problem+json; charset=utf-8" {
			t.Errorf("Content-Type = %q, want problem+json", got)
		}
		body := decodeBody(t, resp)
		if body["code"] != string(CodeInvalidArgument) {
			t.Errorf("code = %v, want %s", body["code"], CodeInvalidArgument)
		}
		// unknown_parameter, not invalid_value: the client is one version
		// ahead (or typing), and the recovery is to drop the parameter.
		if body["reason"] != string(ReasonUnknownParameter) {
			t.Errorf("reason = %v, want %s", body["reason"], ReasonUnknownParameter)
		}
		param, ok := body["param"].(string)
		if !ok || param != tc.param {
			t.Errorf("GET %s: param = %#v, want %q", tc.path, body["param"], tc.param)
		}
		// And the log line names it too: "which local process keeps sending bad
		// requests, with what" is not answerable from a status alone.
		line := findLogLine(t, ts.stderr.String(), "request_id="+body["request_id"].(string)+" ")
		if !strings.Contains(line, "refused="+logValue(tc.param)) {
			t.Errorf("GET %s: request line does not name the refused parameter:\n%s", tc.path, line)
		}
	}
}

// TestHostHeaderAllowlist is the DNS-rebinding defense. A page that re-resolves
// its own name to 127.0.0.1 reaches this server with plain same-origin
// requests; what it cannot do is change the Host header the browser sends.
func TestHostHeaderAllowlist(t *testing.T) {
	ts := newTestServer(t, Config{})

	// Every spelling of an allowed address is that address. A client that writes
	// the IPv6 loopback the long way, or as an IPv4-mapped literal, is not an
	// attacker — the rebinding attack carries a NAME.
	for _, host := range []string{
		"127.0.0.1", "localhost", "[::1]", "LOCALHOST",
		"[0:0:0:0:0:0:0:1]", "[::ffff:127.0.0.1]",
	} {
		t.Run("allowed/"+host, func(t *testing.T) {
			resp := requestWithHost(t, ts, "/healthz", host+":1234")
			if resp.StatusCode != http.StatusOK {
				t.Errorf("Host %q: status = %d, want 200", host, resp.StatusCode)
			}
		})
	}

	for _, host := range []string{"evil.example", "beads.attacker.test:8080", "192.168.1.10"} {
		t.Run("refused/"+host, func(t *testing.T) {
			resp := requestWithHost(t, ts, "/healthz", host)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("Host %q: status = %d, want 400", host, resp.StatusCode)
			}
			body := decodeBody(t, resp)
			if body["code"] != string(CodeInvalidArgument) || body["param"] != "Host" {
				t.Errorf("body = %v, want invalid_argument on param Host", body)
			}
			// The refusal must be logged like any other request; the middleware
			// runs inside the same per-request record. And it must name the
			// Host it refused: this check IS the rebinding defense, so a probe
			// that leaves no attributable trace is a control nobody can
			// investigate. logValue quotes the attacker-controlled value.
			line := findLogLine(t, ts.stderr.String(), "refused="+logValue(host))
			for _, field := range []string{"status=400", "code=invalid_argument", "remote_addr=127.0.0.1:"} {
				if !strings.Contains(line, field) {
					t.Errorf("Host refusal log line is missing %q:\n%s", field, line)
				}
			}
		})
	}
}

// requestWithHost drives a raw request so the Host header can be a name the
// resolver has never heard of.
func requestWithHost(t *testing.T, ts *testServer, path, host string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.base+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Host = host
	resp, err := ts.client.Do(req)
	if err != nil {
		t.Fatalf("GET %s with Host %q: %v", path, host, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// TestHostPolicyPerBindAddress pins the bind-dependent rule for every bind shape
// the validator accepts. The load-bearing row is the wildcard one: a wildcard
// bind has no single configured address, and the answer is to allow IP LITERALS
// rather than to switch the defense off. A rebound page cannot produce an
// IP-literal Host — the browser sends the hostname from the attacker's URL — so
// this keeps --allow-non-loopback fully usable while a foreign name is still
// refused, including on the serving host's own loopback interface, which is
// rebinding's canonical target.
func TestHostPolicyPerBindAddress(t *testing.T) {
	for _, tc := range []struct {
		name    string
		bind    string
		allow   []string
		refuse  []string
		inLabel string
	}{
		{
			name:   "default loopback",
			bind:   "127.0.0.1",
			allow:  []string{"127.0.0.1:9000", "localhost", "[::1]:9000", "[0:0:0:0:0:0:0:1]"},
			refuse: []string{"evil.example", "10.0.0.5", "beads.attacker.test:8080"},
		},
		{
			// ValidateBindAddr accepts any 127/8 address, so the allowlist must
			// answer to one. Otherwise a server the operator was allowed to
			// configure refuses every client that dials the configured address.
			name:   "alternate loopback",
			bind:   "127.0.0.2",
			allow:  []string{"127.0.0.2:9000", "127.0.0.1", "localhost"},
			refuse: []string{"evil.example"},
		},
		{
			name:   "specific non-loopback",
			bind:   "10.0.0.5",
			allow:  []string{"10.0.0.5:9000", "127.0.0.1", "localhost"},
			refuse: []string{"evil.example", "192.168.1.10", "10.0.0.6"},
		},
		{
			name:    "wildcard",
			bind:    "0.0.0.0",
			allow:   []string{"127.0.0.1", "localhost", "10.0.0.5:9000", "192.168.1.10", "[fe80::1]:9000"},
			refuse:  []string{"evil.example", "beads.attacker.test:8080", "localhost.evil.example"},
			inLabel: "any-ip-literal",
		},
		{
			name:    "ipv6 wildcard",
			bind:    "::",
			allow:   []string{"[::1]", "localhost", "[2001:db8::1]:9000"},
			refuse:  []string{"evil.example"},
			inLabel: "any-ip-literal",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newHostPolicy(net.ParseIP(tc.bind), nil)
			for _, host := range tc.allow {
				if !p.allows(host) {
					t.Errorf("bind %s refuses Host %q, which is one of its own clients", tc.bind, host)
				}
			}
			for _, host := range tc.refuse {
				if p.allows(host) {
					t.Errorf("bind %s accepts Host %q; the rebinding defense is not a defense", tc.bind, host)
				}
			}
			// The policy is never empty and never off: the startup line states
			// what it is, so an operator does not have to derive it.
			if label := p.label(); !strings.Contains(label, tc.inLabel) || label == "" {
				t.Errorf("bind %s startup label = %q, want it to mention %q", tc.bind, label, tc.inLabel)
			}
		})
	}
}

// TestAlternateLoopbackBindAnswersItsOwnClients is the end-to-end half of the
// alternate-loopback row above: a bind ValidateBindAddr blesses must not produce
// a server that 400s every client dialing the address it was configured with.
func TestAlternateLoopbackBindAnswersItsOwnClients(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Skipf("127.0.0.2 is not bindable here: %v", err)
	}
	_ = probe.Close()
	ts := newTestServer(t, Config{Addr: "127.0.0.2:0"})

	resp := requestWithHost(t, ts, "/healthz", ts.Addr())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Host %q against a server bound to it: status = %d, want 200", ts.Addr(), resp.StatusCode)
	}
}

// TestUnroutedPathsKeepTheErrorShape: the document promises that EVERY non-2xx
// byte is problem+json, including from paths no operation owns. net/http's
// default 404 is text/plain, so the catch-all is what makes that true.
func TestUnroutedPathsKeepTheErrorShape(t *testing.T) {
	ts := newTestServer(t, Config{})

	resp := ts.get(t, "/v1/beads/ready")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/problem+json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want problem+json", got)
	}
	body := decodeBody(t, resp)
	if body["code"] != string(CodeNotFound) {
		t.Errorf("code = %v, want %s", body["code"], CodeNotFound)
	}
	if body["request_id"] == nil {
		t.Error("no request_id on the problem body")
	}
}

// TestCapabilitiesAdvertiseEveryImplementedOperation: `capabilities` is how a
// client checks for an operation — never the version string — so what this
// asserts is that the WIRE carries what the route table implies, token for
// token and in order.
//
// THE EXPECTATION IS DERIVED FROM routeTable, NOT SPELLED OUT, and the change
// is worth the sentence it costs. A hand-written golden here was a THIRD copy
// of one list — beside the route table and the document's own vocabulary
// paragraph — and a third copy buys an oracle only if it has an independent
// source. It did not: it was maintained by hand from the other two, so its only
// real behavior was to fail whenever a new operation landed. That is a cost
// pretending to be a check, and the cost fell on the wrong PR: the count slice
// updated this copy, missed cmd/bd's identical one, and turned the proxied
// shard red on a list nothing about the count had changed.
//
// CONTENT IS PINNED ELSEWHERE, AND INDEPENDENTLY.
// TestSpecCapabilityVocabularyMatchesTheRouteTable compares the derived set
// against the `capabilities` prose in openapi.v0.yaml in BOTH directions, and
// that paragraph is written by hand from the operation the author is adding. So
// a route row with a typo'd or invented capability still fails a test — that
// one — and adding an operation still costs a deliberate edit, in the document
// where a client reads the vocabulary rather than in two test files where
// nobody does.
//
// What is left here is the half only this test can see: that the derivation
// reaches the wire at all, through contextResponse, sorted, with the
// `implemented` gate applied.
func TestCapabilitiesAdvertiseEveryImplementedOperation(t *testing.T) {
	ts := newTestServer(t, Config{})

	caps, _ := decodeBody(t, ts.get(t, "/v0/beads/context"))["capabilities"].([]any)
	var got []string
	for _, c := range caps {
		got = append(got, c.(string))
	}

	// Re-derived rather than compared against Capabilities(), which is what
	// this handler already calls: `Capabilities() == Capabilities()` is green
	// for every surface including an empty one. Walking the table here keeps the
	// three things that function does — the `implemented` gate, the empty-token
	// filter, and the sort — observable.
	var want []string
	for _, rt := range routeTable {
		if rt.implemented && rt.capability != "" {
			want = append(want, rt.capability)
		}
	}
	if len(want) == 0 {
		t.Fatal("the route table contributed no capabilities; this case would pass against a server that advertises nothing")
	}
	// The behavior tokens name no route, so the table walk above cannot reach
	// them; project.enforce is advertised in the same list. It is spelled
	// literally here to keep this an independent oracle rather than a second
	// call to the code under test.
	want = append(want, "project.enforce")
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("capabilities = %v, want %v", got, want)
	}
	// Sortedness is asserted on the WIRE rather than inferred from the equality
	// above, because a client is told to treat this as a set it may search.
	// Falsified: reversing Capabilities()' sort turns this red.
	if !slices.IsSorted(got) {
		t.Errorf("capabilities = %v, want them sorted", got)
	}
}

// THE `implemented` GATE IS NOT PINNED HERE, and saying so is the point.
//
// A loop asserting that no unimplemented row is advertised was written, run
// against two mutations, and deleted: it cannot fail. Capabilities() applies the
// gate, and the expectation above re-applies it, so a stub is missing from both
// sides and they agree. Dropping the gate from Capabilities() is unfalsifiable
// for a second reason — there are no 501 rows in v0 at all, so removing a filter
// that filters nothing changes nothing. A green case named for that promise
// would be worse than no case: a reviewer greps for the gate, finds a test named
// for it, and stops looking.
//
// What actually holds it is TestSpecStatusCodesMatchHandlerTable, which fails on
// ANY row with implemented false — 501 is documented nowhere and `not_implemented`
// is deliberately absent from the frozen vocabulary, so a stub cannot land
// without an exemption block that says why. The probe that would upgrade this
// case is a Capabilities() that takes its rows as an argument; that is
// production surgery for a test, and it is not worth it while a stub cannot
// reach main.

// TestClaimPathReachesItsHandler drives the path the DOCUMENT spells, which is
// the one thing route parity cannot check for the claim row: that row declares
// its spec path instead of deriving it from the pattern, because ServeMux
// wildcards match whole segments and `{id}:claim` is not expressible. The parity
// test bounds the shape of that exception; only a request proves the pattern
// actually serves the documented path.
func TestClaimPathReachesItsHandler(t *testing.T) {
	issues := &fakeIssues{issue: seededIssue("bd-1", "alice", types.StatusInProgress)}
	ts, _ := newClaimServer(t, issues)

	resp := ts.claim(t, "/v0/beads/issues/bd-1:claim", `{"actor":"alice"}`)
	// A 404 here means the documented path reaches the catch-all: the route
	// exists in the table, the parity test is green, and the endpoint is
	// unreachable.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST the documented claim path: status = %d, want 200", resp.StatusCode)
	}
	line := findLogLine(t, ts.stderr.String(), "path=/v0/beads/issues/bd-1:claim")
	if !strings.Contains(line, "op="+OpClaimIssue) {
		t.Errorf("the documented claim path is served by another operation:\n%s", line)
	}
}

// TestCapabilitiesListImplementedOperationsOnly: the advertised set is exactly
// the implemented operation tokens UNION the server-wide behavior tokens, and
// nothing else — a token here that backs neither an implemented handler nor a
// declared behavior is a capability a client would check for and never find
// anything behind.
func TestCapabilitiesListImplementedOperationsOnly(t *testing.T) {
	advertised := map[string]bool{}
	for _, rt := range routeTable {
		if rt.implemented && rt.capability != "" {
			advertised[rt.capability] = true
		}
	}
	for _, c := range behaviorCapabilities {
		advertised[c] = true
	}
	for _, got := range Capabilities() {
		if !advertised[got] {
			t.Errorf("Capabilities() lists %q, which is neither an implemented operation nor a behavior capability", got)
		}
	}
	if len(Capabilities()) != len(advertised) {
		t.Errorf("Capabilities() = %v, want exactly %v", Capabilities(), advertised)
	}
}

// TestSemaphoreShedsLoadInsteadOfQueueingForever is the wedge scenario in
// miniature: the database stops answering, every slot fills, and the next
// request must be TOLD to come back rather than parked until the deadline and
// then answered with a non-retryable 500.
//
// It drives acquire directly against a hand-built server so the queue bound and
// the saturation threshold can be milliseconds. The behavior under the real
// constants is the same; only the waiting is shorter.
func TestSemaphoreShedsLoadInsteadOfQueueingForever(t *testing.T) {
	stderr := &lockedBuffer{}
	s := &Server{
		sem:        make(chan struct{}, 1),
		semTimeout: 20 * time.Millisecond,
		semWarn:    time.Nanosecond,
		log:        newTestLogger(stderr),
	}

	held, err := s.acquire(context.Background(), &reqInfo{id: "holder"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	waiter := &reqInfo{id: "waiter"}
	if _, err := s.acquire(context.Background(), waiter); !errors.Is(err, ErrBusy) {
		t.Fatalf("acquire with the slot held = %v, want ErrBusy", err)
	}
	if waiter.semWait <= 0 {
		t.Error("semaphore wait was not recorded; the log line cannot separate a saturated server from a slow database")
	}
	if !strings.Contains(stderr.String(), "event=semaphore_timeout") {
		t.Errorf("a shed request produced no saturation event:\n%s", stderr.String())
	}

	// The shed maps onto a status the document already carries, so no new wire
	// vocabulary enters through the back door.
	res := ClassifyError(ErrBusy)
	if res.Problem.Status != http.StatusServiceUnavailable || res.Problem.Code != string(CodeBusy) {
		t.Fatalf("ErrBusy maps to %d/%s, want 503/busy", res.Problem.Status, res.Problem.Code)
	}
	if res.RetryAfterSeconds <= 0 {
		t.Error("a saturation 503 must carry Retry-After")
	}

	// A wait that eventually succeeds is still a saturation datapoint.
	go func() {
		time.Sleep(5 * time.Millisecond)
		held()
	}()
	slow := &reqInfo{id: "slow"}
	release, err := s.acquire(context.Background(), slow)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	release()
	if !strings.Contains(stderr.String(), "event=semaphore_saturated") {
		t.Errorf("a long but successful wait produced no saturation event:\n%s", stderr.String())
	}
}

// TestStalledResponseWriteReleasesItsSlot closes the same black hole as the
// semaphore's bounded wait, one layer out. The slot is released when the handler
// returns, and the handler returns only after writing the body — so a client
// that opens a request and then stops reading would hold its slot, and the SQL
// connection pinned to it, until the process restarted. Nothing interrupts a
// blocked socket write: not the request deadline, not Shutdown, not client
// cancellation. Only a write deadline does.
//
// The stub handlers in this build emit bodies small enough to fit in net/http's
// buffers, so this drives a streaming handler through the real route() chain —
// which is where the read slices' unlimited list responses will land.
func TestStalledResponseWriteReleasesItsSlot(t *testing.T) {
	stderr := &lockedBuffer{}
	s := &Server{
		sem: make(chan struct{}, 1),
		// Generous: the point is whether the slot comes back at all.
		semTimeout: 5 * time.Second,
		writeStall: 100 * time.Millisecond,
		log:        newTestLogger(stderr),
	}

	writeErr := make(chan error, 1)
	stream := route{op: "stream", handler: func(_ *Server, w http.ResponseWriter, _ *http.Request) {
		body := make([]byte, 32<<10)
		var err error
		for guard := time.Now().Add(30 * time.Second); err == nil && time.Now().Before(guard); {
			_, err = w.Write(body)
		}
		writeErr <- err
	}}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	hs := &http.Server{
		Handler:  s.withRequestContext(s.route(stream)),
		ErrorLog: newTestLogger(stderr),
	}
	go func() { _ = hs.Serve(ln) }()
	t.Cleanup(func() { _ = hs.Close() })

	// A client that asks for the body and never reads a byte of it. Both socket
	// buffers fill, and the server's write blocks in the kernel.
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := io.WriteString(conn, "GET /stream HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n"); err != nil {
		t.Fatalf("write request: %v", err)
	}

	select {
	case err := <-writeErr:
		if err == nil {
			t.Fatal("the response write never blocked; this test proves nothing about a stalled client")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("a client that stopped reading stalled the response write with no bound: the handler never returns, so its database slot and pinned connection are held until the process restarts — while /healthz stays green")
	}

	// And the slot is back, because the handler returned and route()'s deferred
	// release ran.
	release, err := s.acquire(context.Background(), &reqInfo{id: "after"})
	if err != nil {
		t.Fatalf("acquiring a slot after the stalled request = %v; it was never released", err)
	}
	release()
}

// TestPanickingHandlerStaysInTheContract. A panic is the one failure where
// correlating a client report with a server log matters most, and it was the one
// failure with no correlation at all: net/http's per-connection recover prints a
// bare stack to stderr, the client gets a dropped connection instead of
// problem+json, and the request line — the observability floor's one-line-per-
// request guarantee — is never written.
//
// It runs through the real route() chain, so it also pins what already held: the
// deferred slot release and unit-of-work rollback run during unwinding.
func TestPanickingHandlerStaysInTheContract(t *testing.T) {
	stderr := &lockedBuffer{}
	s := &Server{
		sem:        make(chan struct{}, 1),
		semTimeout: 5 * time.Second,
		log:        newTestLogger(stderr),
	}
	boom := route{op: "boom", handler: func(*Server, http.ResponseWriter, *http.Request) {
		panic("handler exploded on bd-1")
	}}
	h := s.withRequestContext(s.route(boom))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v0/beads/issues/bd-1", nil))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (a panic must still answer in the documented shape)", rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); got != "application/problem+json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want problem+json", got)
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not JSON: %v", rr.Body.String(), err)
	}
	if body["code"] != string(CodeInternal) {
		t.Errorf("code = %v, want %s", body["code"], CodeInternal)
	}
	if body["detail"] != staticDetail[CodeInternal] {
		t.Errorf("detail = %v, want the static 5xx detail; a panic message is server state", body["detail"])
	}
	if strings.Contains(rr.Body.String(), "exploded") {
		t.Errorf("the panic value reached the client: %s", rr.Body.String())
	}
	id, _ := body["request_id"].(string)
	if id == "" {
		t.Fatal("no request_id on the problem body: nothing ties a client report to the stack trace")
	}

	// One structured line per request, including this one.
	line := findLogLine(t, stderr.String(), "event=request ")
	for _, want := range []string{"request_id=" + id, "op=boom", "status=500", "code=internal"} {
		if !strings.Contains(line, want) {
			t.Errorf("request log line is missing %q:\n%s", want, line)
		}
	}

	// And the stack, correlated and on one line.
	panicLine := findLogLine(t, stderr.String(), "event=panic")
	for _, want := range []string{"request_id=" + id, "path=/v0/beads/issues/bd-1", "exploded", "stack="} {
		if !strings.Contains(panicLine, want) {
			t.Errorf("panic log line is missing %q:\n%s", want, panicLine)
		}
	}
	if !strings.Contains(panicLine, "TestPanickingHandlerStaysInTheContract") {
		t.Errorf("the logged stack does not reach the panic site:\n%s", panicLine)
	}

	// The database slot came back: route()'s deferred release runs during
	// unwinding, before this middleware recovers.
	release, err := s.acquire(context.Background(), &reqInfo{id: "after"})
	if err != nil {
		t.Fatalf("acquiring a slot after the panic = %v; a panicking handler leaks its slot", err)
	}
	release()
}

// TestAbortHandlerPanicIsStillLogged: net/http documents a panic with
// ErrAbortHandler as "abandon this response silently", so it must not become a
// 500 body — but a request that happened is still a request, and it gets its
// line before the abort is handed back.
func TestAbortHandlerPanicIsStillLogged(t *testing.T) {
	stderr := &lockedBuffer{}
	s := &Server{log: newTestLogger(stderr)}
	h := s.withRequestContext(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))

	rr := httptest.NewRecorder()
	func() {
		defer func() {
			if p := recover(); p != http.ErrAbortHandler {
				t.Errorf("recovered %v, want ErrAbortHandler propagated to net/http", p)
			}
		}()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	}()

	if rr.Body.Len() != 0 {
		t.Errorf("an aborted response wrote a body: %q", rr.Body.String())
	}
	findLogLine(t, stderr.String(), "event=request ")
	if strings.Contains(stderr.String(), "event=panic") {
		t.Errorf("a documented abort was logged as a crash:\n%s", stderr.String())
	}
}

// TestExemptRoutesAnswerUnderSaturation: liveness and identity must stay
// observable while every database slot is held by a long scan — that is when an
// operator most needs them, and it is why those two handlers touch no database.
func TestExemptRoutesAnswerUnderSaturation(t *testing.T) {
	ts := newTestServer(t, Config{})

	rec := &reqInfo{id: "holder"}
	for range maxInflight {
		release, err := ts.acquire(context.Background(), rec)
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		t.Cleanup(release)
	}

	for _, path := range []string{"/healthz", "/v0/beads/context"} {
		if resp := ts.get(t, path); resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d with every slot held, want 200", path, resp.StatusCode)
		}
	}
}

// TestClientHangupIsNotBookedAsAServerFault. A saturated server is exactly when
// clients time out and disconnect, and it is exactly when an operator is reading
// the 500 and request_error counts. Attributing those disconnects to the server
// would make the one signal worth alerting on peak whenever load does.
//
// An expired request deadline is the control row: it keeps the contract's
// specified 500/internal, because nothing about it says the client left.
func TestClientHangupIsNotBookedAsAServerFault(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		wantCode string
		wantErrs bool
	}{
		{
			name:     "client hung up",
			err:      fmt.Errorf("new uow: %w", context.Canceled),
			wantCode: "code=" + string(codeClientClosed),
		},
		{
			name:     "request deadline expired",
			err:      fmt.Errorf("new uow: %w", context.DeadlineExceeded),
			wantCode: "code=" + string(CodeInternal),
			wantErrs: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stderr := &lockedBuffer{}
			s := &Server{log: newTestLogger(stderr)}
			hangup := route{op: "hangup", bypassSemaphore: true,
				handler: func(s *Server, w http.ResponseWriter, r *http.Request) {
					s.failErr(w, r, tc.err)
				}}
			h := s.withRequestContext(s.route(hangup))

			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v0/beads/ready", nil))

			// The wire answer is unchanged either way; it goes to a socket
			// nobody is reading.
			if rr.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", rr.Code)
			}

			line := findLogLine(t, stderr.String(), "event=request ")
			if !strings.Contains(line, tc.wantCode) {
				t.Errorf("request line does not carry %q:\n%s", tc.wantCode, line)
			}
			if got := strings.Contains(stderr.String(), "event=request_error"); got != tc.wantErrs {
				t.Errorf("event=request_error present = %v, want %v:\n%s", got, tc.wantErrs, stderr.String())
			}
		})
	}
}

// TestWithUOWClosesWithADetachedContext is the one that protects the pinned
// connection. Close sends ROLLBACK; if that send fails the transaction layer
// poisons the connection rather than returning it, so closing with the
// request's own canceled context would burn a session on every client
// disconnect.
func TestWithUOWClosesWithADetachedContext(t *testing.T) {
	provider := &fakeProvider{}
	ts := newTestServer(t, Config{Provider: provider})

	ctx, cancel := context.WithCancel(context.Background())
	rec := &reqInfo{id: "detached"}

	err := ts.WithUOW(ctx, rec, func(uow.UnitOfWork) error {
		// The client hangs up mid-request.
		cancel()
		return nil
	})
	if err != nil {
		t.Fatalf("WithUOW: %v", err)
	}

	provider.mu.Lock()
	uows := provider.uows
	provider.mu.Unlock()
	if len(uows) != 1 {
		t.Fatalf("opened %d units of work, want 1", len(uows))
	}

	closed, closeErr, hasDeadline := uows[0].closeState()
	if !closed {
		t.Fatal("unit of work was never closed; the rollback is not guaranteed")
	}
	if closeErr != nil {
		t.Fatalf("close context was already done (%v): the ROLLBACK cannot be sent, and the pinned connection is poisoned rather than returned", closeErr)
	}
	if !hasDeadline {
		t.Error("close context has no deadline; a hung rollback would block shutdown forever")
	}
}

func TestWithUOWRecordsAcquireTimeAndPropagatesErrors(t *testing.T) {
	want := errors.New("dial failed")
	provider := &fakeProvider{err: want, delay: 5 * time.Millisecond}
	ts := newTestServer(t, Config{Provider: provider})

	rec := &reqInfo{}
	err := ts.WithUOW(context.Background(), rec, func(uow.UnitOfWork) error {
		t.Fatal("body ran despite the provider failing")
		return nil
	})
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	if rec.uowWait <= 0 {
		t.Error("uow acquire time was not recorded; the log line cannot separate a slow database from a saturated server")
	}
}

// TestRequestLogLine pins the observability floor. Without these fields an
// operator cannot tell no-traffic from all-traffic-hanging from
// all-traffic-503ing, and the endpoints alone cannot tell them either.
func TestRequestLogLine(t *testing.T) {
	ts := newTestServer(t, Config{})
	ts.get(t, "/healthz")

	line := findLogLine(t, ts.stderr.String(), "event=request ")
	for _, field := range []string{
		"request_id=", "op=health", "method=GET", "path=/healthz",
		"status=200", "duration_ms=", "sem_wait_ms=", "uow_ms=",
		// conns is the connection-cap gauge: the cap is enforced by the
		// listener, so this line is the only place it can be watched climbing.
		// remote_addr answers "which client" — on loopback, "which local
		// process", via the port.
		"conns=", "remote_addr=127.0.0.1:",
	} {
		if !strings.Contains(line, field) {
			t.Errorf("request log line is missing %q:\n%s", field, line)
		}
	}
	// A request nothing refused carries no refused field; it is not noise on
	// every line.
	if strings.Contains(line, "refused=") {
		t.Errorf("request log line carries a refusal it did not make:\n%s", line)
	}
}

// TestConnectionCapSaturationIsAnnounced. netutil.LimitListener parks Accept at
// the cap, so further connections sit in the kernel backlog with nothing on
// stderr — and /healthz needs a fresh accept too, which makes an exhausted cap
// look exactly like no traffic. The semaphore got a saturation event for the
// same reason; this is the connection tier's.
func TestConnectionCapSaturationIsAnnounced(t *testing.T) {
	stderr := &lockedBuffer{}
	s := &Server{maxConns: 2, log: newTestLogger(stderr)}

	s.connState(nil, http.StateNew)
	if strings.Contains(stderr.String(), "event=conn_cap_saturated") {
		t.Fatalf("announced saturation below the cap:\n%s", stderr.String())
	}

	s.connState(nil, http.StateNew)
	line := findLogLine(t, stderr.String(), "event=conn_cap_saturated")
	for _, field := range []string{"conns=2", "max_conns=2"} {
		if !strings.Contains(line, field) {
			t.Errorf("saturation line is missing %q:\n%s", field, line)
		}
	}

	// Edge-triggered: a server sitting at the cap logs once, not once per
	// connection attempt.
	before := strings.Count(stderr.String(), "event=conn_cap_saturated")
	s.connState(nil, http.StateNew)
	if got := strings.Count(stderr.String(), "event=conn_cap_saturated"); got != before {
		t.Errorf("saturation events = %d, want %d: the event repeats while the cap is held", got, before)
	}

	// And it re-arms, so the NEXT exhaustion is its own event rather than
	// silence.
	s.connState(nil, http.StateClosed)
	s.connState(nil, http.StateClosed)
	s.connState(nil, http.StateNew)
	if got := strings.Count(stderr.String(), "event=conn_cap_saturated"); got != before+1 {
		t.Errorf("saturation events = %d, want %d after the cap cleared and filled again", got, before+1)
	}
}

// TestProblemResponsesCorrelateToTheirLogLine: a 5xx body carries a fixed
// detail by design, so request_id is the client's only handle on the log line
// that has the real error.
func TestProblemResponsesCorrelateToTheirLogLine(t *testing.T) {
	ts := newTestServer(t, Config{})

	resp := ts.get(t, "/healthz?bogus=1")
	id, _ := decodeBody(t, resp)["request_id"].(string)
	if id == "" {
		t.Fatal("problem body carries no request_id")
	}

	line := findLogLine(t, ts.stderr.String(), "status=400")
	if !strings.Contains(line, "request_id="+id) {
		t.Errorf("log line does not carry the body's request_id %q:\n%s", id, line)
	}
	if !strings.Contains(line, "code=invalid_argument") {
		t.Errorf("log line does not carry the problem code:\n%s", line)
	}
}

func TestStartupLines(t *testing.T) {
	provider := &tunableProvider{}
	ts := newTestServer(t, Config{
		Provider: provider,
		Mode:     "proxied-server (managed dolt)",
		Workspace: domain.ContextInfo{
			RepoRoot: "/w/repo",
			BeadsDir: "/w/repo/.beads",
			Database: "beads",
		},
	})

	// stdout carries exactly the bound address, so a caller that asked for an
	// ephemeral port can find it without parsing the log.
	out := ts.stdout.String()
	if !strings.Contains(out, ts.Addr()) {
		t.Errorf("stdout %q does not name the bound address %s", out, ts.Addr())
	}
	if lines := strings.Count(strings.TrimSpace(out), "\n"); lines != 0 {
		t.Errorf("stdout has %d extra lines; it must carry the address and nothing else:\n%s", lines, out)
	}

	startup := findLogLine(t, ts.stderr.String(), "event=startup")
	for _, field := range []string{
		"addr=" + ts.Addr(), `mode="proxied-server (managed dolt)"`,
		"workspace=/w/repo", "beads_dir=/w/repo/.beads",
	} {
		if !strings.Contains(startup, field) {
			t.Errorf("startup line is missing %q:\n%s", field, startup)
		}
	}

	limits := findLogLine(t, ts.stderr.String(), "event=limits")
	for _, field := range []string{"max_inflight=", "max_conns=", "sem_wait=", "deadline=", "pool_max_open="} {
		if !strings.Contains(limits, field) {
			t.Errorf("limits line is missing %q:\n%s", field, limits)
		}
	}

	// The pool cap is defense in depth, not semaphore trust: retry attempts and
	// poisoned-connection replacement both escape the semaphore.
	if !provider.set {
		t.Fatal("pool limits were never applied to the provider")
	}
	if provider.limits.MaxOpenConns <= maxInflight {
		t.Errorf("MaxOpenConns = %d, want headroom above the %d in-flight bound",
			provider.limits.MaxOpenConns, maxInflight)
	}
	if provider.limits.ConnMaxIdleTime <= 0 || provider.limits.ConnMaxLifetime <= 0 {
		t.Errorf("idle/lifetime caps unset: %+v", provider.limits)
	}
}

// TestPoolLimitsUnavailableIsAnnounced: running unbounded is a decision, so it
// must not be a silent one.
func TestPoolLimitsUnavailableIsAnnounced(t *testing.T) {
	ts := newTestServer(t, Config{Provider: &fakeProvider{}})
	if !strings.Contains(ts.stderr.String(), "event=pool_limits_unavailable") {
		t.Errorf("a provider with no pool knob was accepted silently:\n%s", ts.stderr.String())
	}
}

func TestShutdownLines(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &lockedBuffer{}
	srv, err := Listen(Config{
		Addr:     "127.0.0.1:0",
		Provider: &fakeProvider{},
		Stdout:   stdout,
		Stderr:   stderr,
	})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()

	resp, err := http.Get("http://" + srv.Addr() + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	_ = resp.Body.Close()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Serve did not return after cancellation")
	}

	log := stderr.String()
	for _, want := range []string{"event=shutdown_start", "event=shutdown_complete"} {
		if !strings.Contains(log, want) {
			t.Errorf("shutdown log is missing %q:\n%s", want, log)
		}
	}

	// The listener is gone: a graceful shutdown that leaves the port bound
	// would make the "TCP bind is the mutual exclusion" claim false.
	if _, err := http.Get("http://" + srv.Addr() + "/healthz"); err == nil {
		t.Error("server still answering after shutdown")
	}
}

// TestSecondBindFails is the whole mutual-exclusion story. There is no lock
// file and no pid file by design; on a fixed port the operating system is the
// arbiter, and the error names the address.
func TestSecondBindFails(t *testing.T) {
	first := newTestServer(t, Config{})

	_, err := Listen(Config{
		Addr:     first.Addr(),
		Provider: &fakeProvider{},
		Stdout:   io.Discard,
		Stderr:   io.Discard,
	})
	if err == nil {
		t.Fatal("a second bind on the same address succeeded")
	}
	if !strings.Contains(err.Error(), first.Addr()) {
		t.Errorf("error %q does not name the address in use", err)
	}
}

func TestListenRefusesAMissingProvider(t *testing.T) {
	_, err := Listen(Config{Addr: "127.0.0.1:0"})
	if err == nil {
		t.Fatal("Listen succeeded with no provider")
	}
}

func TestLogValueQuotesInjectableText(t *testing.T) {
	// A path is caller-controlled, and the log is key=value: a space or a
	// newline in it would otherwise forge fields, or whole lines. The claim
	// route widened the audience for this helper — it records a rejected body
	// member NAME and a rejected Content-Type, both arbitrary caller strings —
	// so the escape-sequence rows below are as load-bearing as the framing ones.
	for _, tc := range []struct{ in, want string }{
		{"/healthz", "/healthz"},
		{"/a b", `"/a b"`},
		{"a=b", `"a=b"`},
		{"line\nbreak", `"line\nbreak"`},
		{"", `""`},
		// C1. U+009B is a one-byte CSI on a VT-conformant console, so an
		// unquoted one paints the terminal of whoever tails the log; U+0085 is
		// a line break there, which forges a log line.
		{"csi\u009b31m", `"csi\u009b31m"`},
		{"nel\u0085forged", `"nel\u0085forged"`},
		// A raw obs-text byte: legal in an HTTP/1 field value, so it reaches
		// this helper from Content-Type as invalid UTF-8 rather than as a rune.
		{"raw\x9b31m", `"raw\x9b31m"`},
		{"\xff\xfe", `"\xff\xfe"`},
		// Unicode line separators split a line for anything that splits on
		// Unicode breaks.
		{"ls\u2028forged", `"ls\u2028forged"`},
	} {
		if got := logValue(tc.in); got != tc.want {
			t.Errorf("logValue(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func newTestLogger(w io.Writer) *log.Logger {
	return log.New(w, "bd serve: ", log.LstdFlags|log.LUTC)
}

func findLogLine(t *testing.T, log, needle string) string {
	t.Helper()
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	t.Fatalf("no log line containing %q in:\n%s", needle, log)
	return ""
}

// ---------------------------------------------------------------------------
// Deployment hardening: bearer auth, the Host allowlist, and operator limits.
//
// All three land on one seam (Config), and the property that ties them
// together is the default: a zero-valued Config is today's server, byte for
// byte. Every test above this line is the proof of that half — none of them
// passes a token, an allowed host or a limit, and all of them still pass.

// authTokenFile writes a token file and returns a verifier over it.
func authTokenFile(t *testing.T, tokens ...string) *TokenFileAuth {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tokens")
	if err := os.WriteFile(path, []byte(strings.Join(tokens, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	auth, err := NewTokenFileAuth(path)
	if err != nil {
		t.Fatalf("NewTokenFileAuth: %v", err)
	}
	return auth
}

// do issues a request with caller-chosen headers, which every auth test needs
// and ts.get cannot express.
func (ts *testServer) do(t *testing.T, method, path string, header http.Header) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, ts.base+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for k, vs := range header {
		// net/http sends Request.Host and ignores a "Host" entry in the header
		// map, so a test that set only the map would silently exercise the
		// default Host and pass against no policy at all.
		if http.CanonicalHeaderKey(k) == "Host" {
			req.Host = vs[0]
			continue
		}
		req.Header[k] = vs
	}
	resp, err := ts.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func bearer(token string) http.Header {
	return http.Header{"Authorization": {"Bearer " + token}}
}

// authedOperations is every route the token guards — the whole table minus the
// one exempt row. Derived rather than listed so a route added without a
// decision about its exemption cannot slip past these tests.
func authedOperations(t *testing.T) []route {
	t.Helper()
	var out []route
	for _, rt := range routeTable {
		if !rt.authExempt {
			out = append(out, rt)
		}
	}
	if len(out) == 0 {
		t.Fatal("no authenticated routes in the table")
	}
	return out
}

// TestBearerAuthGuardsEveryOperationButLiveness is the security deliverable:
// with a token configured, reachability alone is no longer read and claim
// authority. Liveness is the single exemption — a probe must answer with no
// credential — and GET /v0/beads/context is deliberately NOT one: it reveals
// the repo root, the beads directory and the database name.
func TestBearerAuthGuardsEveryOperationButLiveness(t *testing.T) {
	ts := newTestServer(t, Config{Auth: authTokenFile(t, "primary-token", "secondary-token")})

	if resp := ts.get(t, "/healthz"); resp.StatusCode != http.StatusOK {
		t.Errorf("GET /healthz with no credential = %d, want 200: a liveness probe carries none", resp.StatusCode)
	}

	for _, rt := range authedOperations(t) {
		// The rows sharing the custom-method wildcard each answer on their own
		// suffix, so the substitution has to be the ROW's, not the claim's:
		// hard-coding ":claim" would drive every one of them down the claim's
		// path and stop testing the others the moment a third row was added.
		path := strings.NewReplacer(
			"{id}", "bd-1",
			"{"+customMethodPathValue+"}", "bd-1"+rt.customMethod,
		).Replace(rt.pattern)
		for _, tc := range []struct {
			name   string
			header http.Header
		}{
			{"no header", nil},
			{"wrong scheme", http.Header{"Authorization": {"Basic cHc6cHc="}}},
			{"scheme only", http.Header{"Authorization": {"Bearer"}}},
			{"empty token", http.Header{"Authorization": {"Bearer   "}}},
			{"unknown token", bearer("not-the-token")},
			{"near miss", bearer("primary-toke")},
		} {
			t.Run(rt.op+"/"+tc.name, func(t *testing.T) {
				resp := ts.do(t, rt.method, path, tc.header)
				if resp.StatusCode != http.StatusUnauthorized {
					t.Fatalf("%s %s = %d, want 401", rt.method, path, resp.StatusCode)
				}
				if got := resp.Header.Get("WWW-Authenticate"); got != "Bearer" {
					t.Errorf("WWW-Authenticate = %q, want Bearer", got)
				}
				if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
					t.Errorf("Content-Type = %q, want problem+json", ct)
				}
				body := decodeBody(t, resp)
				if body["code"] != string(CodeUnauthenticated) {
					t.Errorf("code = %v, want %s", body["code"], CodeUnauthenticated)
				}
				if id, _ := body["request_id"].(string); id == "" {
					t.Error("a 401 carries no request_id, so it cannot be correlated to its log line")
				}
				if detail, _ := body["detail"].(string); detail != staticDetail[CodeUnauthenticated] {
					t.Errorf("detail = %q, want the fixed string %q", detail, staticDetail[CodeUnauthenticated])
				}
			})
		}
	}

	// Both tokens in the file are accepted: that is the rotation overlap, and
	// it is the only mechanism this server has for it.
	for _, tok := range []string{"primary-token", "secondary-token"} {
		resp := ts.do(t, http.MethodGet, "/v0/beads/context", bearer(tok))
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET /v0/beads/context with token %q = %d, want 200", tok, resp.StatusCode)
		}
	}
}

// TestUnauthenticated401NeverEchoesTheCredential. A presented token is a
// secret, and a 401 that quoted it would write it into every client log and
// every proxy trace. The fixed `detail` is what makes that structural rather
// than a rule to remember; the server log has the same duty.
func TestUnauthenticated401NeverEchoesTheCredential(t *testing.T) {
	const presented = "leaked-secret-value"
	ts := newTestServer(t, Config{Auth: authTokenFile(t, "real-token")})

	resp := ts.do(t, http.MethodGet, "/v0/beads/ready", bearer(presented))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if strings.Contains(string(body), presented) {
		t.Errorf("the 401 body echoes the presented credential:\n%s", body)
	}
	if log := ts.stderr.String(); strings.Contains(log, presented) {
		t.Errorf("the server log records the presented credential:\n%s", log)
	}

	// The refusal is still attributable: a probe that leaves no server-side
	// trace is a control nobody can investigate.
	line := findLogLine(t, ts.stderr.String(), "event=auth_refused")
	for _, field := range []string{"reason=unknown_token", "op=listReadyWork", "request_id="} {
		if !strings.Contains(line, field) {
			t.Errorf("auth_refused line is missing %q:\n%s", field, line)
		}
	}
}

// TestAuthRefusalNamesWhyItWasRefused separates the three client mistakes in
// the log, because "no header" is a misconfigured client and "unknown token" is
// either a rotation in progress or somebody trying tokens.
func TestAuthRefusalNamesWhyItWasRefused(t *testing.T) {
	for _, tc := range []struct {
		name, wantReason string
		header           http.Header
	}{
		{"missing", "missing", nil},
		{"malformed", "malformed", http.Header{"Authorization": {"Basic cHc6cHc="}}},
		{"unknown", "unknown_token", bearer("nope")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := newTestServer(t, Config{Auth: authTokenFile(t, "real-token")})
			ts.do(t, http.MethodGet, "/v0/beads/ready", tc.header)
			line := findLogLine(t, ts.stderr.String(), "event=auth_refused")
			if !strings.Contains(line, "reason="+tc.wantReason) {
				t.Errorf("auth_refused reason is not %q:\n%s", tc.wantReason, line)
			}
		})
	}
}

// TestAuthRefusalCostsNoDatabaseSlot. The check runs before acquire, so a 401
// storm is one SHA-256 per request and can never occupy the slots — or the SQL
// connections pinned to them — that authenticated clients are waiting for.
// Answering 503 busy here would mean refused traffic could starve real traffic.
func TestAuthRefusalCostsNoDatabaseSlot(t *testing.T) {
	stderr := &lockedBuffer{}
	s := &Server{
		sem:        make(chan struct{}, 1),
		semTimeout: 5 * time.Second,
		log:        newTestLogger(stderr),
		auth:       authTokenFile(t, "real-token"),
	}

	held, err := s.acquire(context.Background(), &reqInfo{id: "holder"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	t.Cleanup(held)

	reached := false
	guarded := route{op: "guarded", handler: func(*Server, http.ResponseWriter, *http.Request) { reached = true }}
	h := s.withRequestContext(s.route(guarded))

	rr := httptest.NewRecorder()
	start := time.Now()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v0/beads/ready", nil))

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 — the refusal queued for a slot instead of answering", rr.Code)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Errorf("the refusal waited %v for a database slot", waited)
	}
	if reached {
		t.Error("the handler ran for an unauthenticated request")
	}
	// The slot is still the holder's: the refusal neither took nor leaked one.
	if len(s.sem) != 1 {
		t.Errorf("semaphore holds %d slots, want the holder's 1", len(s.sem))
	}
}

// TestUnroutedPathsStayUnauthenticated pins both halves of one rule: a path
// this document does not define answers 404 with no credential — paths are
// public spec, so refusing them first would disclose nothing and hide nothing —
// while every path it DOES define is guarded.
//
// The custom-method wildcard is where the two meet. It matches every POST under
// /v0/beads/issues/, and dispatchCustomMethod splits the suffix off BEFORE
// s.route, so a segment ending in no registered suffix gets the catch-all's 404
// and never reaches the credential check; a registered suffix reaches its row
// and is refused. Neither half may be "fixed" into the other: 401 on the miss
// would charge an unrouted path for a credential that cannot help it, and 404
// on the hit would hide an operation behind a routing detail.
func TestUnroutedPathsStayUnauthenticated(t *testing.T) {
	ts := newTestServer(t, Config{Auth: authTokenFile(t, "real-token")})

	for _, path := range []string{"/v0/nonsense", "/", "/v0/beads/issues/bd-1/extra"} {
		if resp := ts.get(t, path); resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s with no credential = %d, want 404: paths are public spec", path, resp.StatusCode)
		}
	}

	// Inside the wildcard, a segment ending in no registered suffix is just as
	// unrouted, and answers the same way.
	for _, path := range []string{"/v0/beads/issues/bd-1", "/v0/beads/issues/bd-1:nosuchverb"} {
		if resp := ts.do(t, http.MethodPost, path, nil); resp.StatusCode != http.StatusNotFound {
			t.Errorf("POST %s with no credential = %d, want 404: the suffix is registered nowhere", path, resp.StatusCode)
		}
	}

	// A registered suffix names an operation, and an operation is guarded.
	for _, rt := range authedOperations(t) {
		if rt.customMethod == "" {
			continue
		}
		path := "/v0/beads/issues/bd-1" + rt.customMethod
		if resp := ts.do(t, rt.method, path, nil); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s with no credential = %d, want 401", rt.method, path, resp.StatusCode)
		}
	}
}

// TestListenRefusesAnUnservablePosture: the library enforces the same posture
// rules the CLI does, so a second caller cannot assemble a Config that serves
// the whole surface to a network with no credential.
func TestListenRefusesAnUnservablePosture(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{
			name:    "non-loopback with no credential",
			cfg:     Config{Addr: "0.0.0.0:0", AllowNonLoopback: true},
			wantErr: "--auth-token-file",
		},
		{
			name:    "waiver contradicts a token",
			cfg:     Config{Addr: "0.0.0.0:0", AllowNonLoopback: true, InsecureNoAuth: true, Auth: authTokenFile(t, "tok")},
			wantErr: "--insecure-no-auth",
		},
		{
			name:    "malformed allowed host",
			cfg:     Config{Addr: "127.0.0.1:0", AllowedHosts: []string{"svc.beads.svc:8080"}},
			wantErr: "--allowed-host",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			if cfg.Provider == nil {
				cfg.Provider = &fakeProvider{}
			}
			cfg.Stdout, cfg.Stderr = &bytes.Buffer{}, &lockedBuffer{}
			srv, err := Listen(cfg)
			if err == nil {
				_ = srv.http.Close()
				t.Fatalf("Listen accepted %+v, want a refusal naming %s", tc.cfg, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("refusal %q does not name %s", err, tc.wantErr)
			}
		})
	}
}

// TestAllowedHostsAdmitInClusterNames is the blocker this slice exists to
// clear: in a cluster the client dials a service DNS name, and the
// loopback-only Host policy 400s every such request. Matching stays EXACT —
// there is no wildcard or suffix syntax — so the allowlist is exactly what the
// operator enumerated.
func TestAllowedHostsAdmitInClusterNames(t *testing.T) {
	const svc = "bd-proj.beads.svc.cluster.local"
	ts := newTestServer(t, Config{AllowedHosts: []string{svc, "10.4.2.9"}})

	for _, host := range []string{svc, svc + ":8080", strings.ToUpper(svc), "10.4.2.9", "10.4.2.9:80", "localhost"} {
		resp := ts.do(t, http.MethodGet, "/healthz", http.Header{"Host": {host}})
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Host %q = %d, want 200", host, resp.StatusCode)
		}
	}
	for _, host := range []string{"beads.svc.cluster.local", "evil." + svc, svc + ".evil.example", "10.4.2.10"} {
		resp := ts.do(t, http.MethodGet, "/healthz", http.Header{"Host": {host}})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("Host %q = %d, want 400: the allowlist is exact", host, resp.StatusCode)
		}
	}

	// The startup line states the whole effective policy, so an operator can
	// read what the server answers to rather than deducing it.
	startup := findLogLine(t, ts.stderr.String(), "event=startup")
	if !strings.Contains(startup, svc) || !strings.Contains(startup, "10.4.2.9") {
		t.Errorf("host_allowlist does not name the operator's additions:\n%s", startup)
	}
}

// TestARunningServerAcceptsARotatedToken is the rotation property proved end
// to end, over a real listener, against a real token file, under the REAL
// one-second reload gate — no injected clock. This is the deployment
// requirement stated as a test: rotating a credential must not be a restart,
// and revoking a leaked one must not wait for the pod's next deploy.
//
// It costs a couple of real seconds, which is the point: the gate it is
// waiting out is the one that ships.
func TestARunningServerAcceptsARotatedToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens")
	writeTokenFile(t, path, "original-token\n")
	auth, err := NewTokenFileAuth(path)
	if err != nil {
		t.Fatalf("NewTokenFileAuth: %v", err)
	}
	ts := newTestServer(t, Config{Auth: auth})

	status := func(token string) int {
		t.Helper()
		return ts.do(t, http.MethodGet, "/v0/beads/context", bearer(token)).StatusCode
	}

	if got := status("original-token"); got != http.StatusOK {
		t.Fatalf("the starting token = %d, want 200", got)
	}

	// The overlap the operator writes: new alongside old, clients roll over
	// one at a time, nothing restarts.
	writeTokenFile(t, path, "rotated-token\noriginal-token\n")
	time.Sleep(authReloadInterval + 200*time.Millisecond)
	if got := status("rotated-token"); got != http.StatusOK {
		t.Errorf("the rotated-in token = %d, want 200 — rotation would need a restart", got)
	}
	if got := status("original-token"); got != http.StatusOK {
		t.Errorf("the outgoing token = %d during the overlap, want 200", got)
	}

	// And the removal, which is revocation: the old token stops working while
	// the same process keeps serving the new one.
	writeTokenFile(t, path, "rotated-token\n")
	time.Sleep(authReloadInterval + 200*time.Millisecond)
	if got := status("original-token"); got != http.StatusUnauthorized {
		t.Errorf("the revoked token = %d, want 401 — a leaked token would outlive the process", got)
	}
	if got := status("rotated-token"); got != http.StatusOK {
		t.Errorf("the live token = %d after revocation, want 200", got)
	}
}

// TestValidateAllowedHost covers the entries an operator actually types,
// including the ones copied verbatim out of a Host header.
func TestValidateAllowedHost(t *testing.T) {
	for _, tc := range []struct {
		name, value, wantErr string
	}{
		{name: "service dns name", value: "bd-proj.beads.svc.cluster.local"},
		{name: "short service name", value: "bd-proj.beads.svc"},
		{name: "single label", value: "bd-proj"},
		{name: "ipv4 literal", value: "10.4.2.9"},
		{name: "ipv6 literal", value: "2001:db8::1"},
		// A Host header spells an IPv6 address in brackets, so an operator
		// reading one off the wire types it that way. hostOnly strips them
		// before matching, so it works — and the validation must not refuse
		// it with a message about a port it does not have.
		{name: "bracketed ipv6 literal", value: "[2001:db8::1]"},

		{name: "empty", value: "", wantErr: "empty"},
		{name: "whitespace", value: "bd proj.svc", wantErr: "whitespace"},
		{name: "url", value: "http://bd-proj.beads.svc", wantErr: "URL"},
		{name: "path", value: "bd-proj.beads.svc/v0", wantErr: "URL"},
		{name: "name with a port", value: "bd-proj.beads.svc:8080", wantErr: "port"},
		{name: "bracketed ipv6 with a port", value: "[2001:db8::1]:8080", wantErr: "port"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAllowedHost(tc.value)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateAllowedHost(%q) = %v, want nil", tc.value, err)
				}
				// Accepting it is only half the promise; it has to reach the
				// policy in a form that matches.
				p := newHostPolicy(net.ParseIP("127.0.0.1"), []string{tc.value})
				if !p.allows(tc.value) {
					t.Errorf("%q validates but the policy does not answer to it", tc.value)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateAllowedHost(%q) = nil, want a refusal about %s", tc.value, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("refusal %q does not explain the problem (%s)", err, tc.wantErr)
			}
		})
	}
}
