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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
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

func newTestServer(t *testing.T, cfg Config) *testServer {
	t.Helper()
	stdout := &bytes.Buffer{}
	stderr := &lockedBuffer{}
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:0"
	}
	if cfg.Provider == nil {
		cfg.Provider = &fakeProvider{}
	}
	cfg.Stdout = stdout
	cfg.Stderr = stderr

	srv, err := Listen(cfg)
	if err != nil {
		t.Fatalf("Listen: %v", err)
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
			p := newHostPolicy(net.ParseIP(tc.bind))
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
// client checks for an operation — never the version string — so it has to be
// derived from what this build actually serves. With the read endpoints landed
// there are no 501 stubs left, which makes the whole v0 vocabulary the expected
// answer; the derivation is what keeps it honest for the next operation, which
// will arrive stubbed.
func TestCapabilitiesAdvertiseEveryImplementedOperation(t *testing.T) {
	ts := newTestServer(t, Config{})

	caps, _ := decodeBody(t, ts.get(t, "/v0/beads/context"))["capabilities"].([]any)
	var got []string
	for _, c := range caps {
		got = append(got, c.(string))
	}
	want := []string{"issues.claim", "issues.get", "issues.list", "ready.list"}
	if !slices.Equal(got, want) {
		t.Errorf("capabilities = %v, want %v", got, want)
	}
}

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

func TestCapabilitiesListImplementedOperationsOnly(t *testing.T) {
	implemented := map[string]bool{}
	for _, rt := range routeTable {
		if rt.implemented && rt.capability != "" {
			implemented[rt.capability] = true
		}
	}
	for _, got := range Capabilities() {
		if !implemented[got] {
			t.Errorf("Capabilities() lists %q, whose handler is not implemented", got)
		}
	}
	if len(Capabilities()) != len(implemented) {
		t.Errorf("Capabilities() = %v, want exactly %v", Capabilities(), implemented)
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
