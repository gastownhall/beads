package dolt

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/doltserver"
)

// Tests for the config-gated open-retry budget (dolt.open-retry-budget,
// GH#4379). They stub openProbeDial / openProbeDialContext so they run without
// a live Dolt server, in the style of socket_fallback_test.go.
//
// Every test here drives the REAL newServerMode. The budget's mode gate is a
// call site inside it -- the branch taken when serverOpenCanAutoStart says bd
// cannot start a server here -- so a test that called retryOpenProbe directly
// would prove nothing about which opens reach it.

// openRetryDialLog counts the two dial seams separately. Keeping them apart is
// the whole point: "the default path is untouched" means the legacy,
// context-free openProbeDial ran exactly once and the retry loop's
// context-aware dial ran zero times.
type openRetryDialLog struct {
	mu       sync.Mutex
	legacy   int
	ctxDials int
	timeouts []time.Duration
}

func (l *openRetryDialLog) counts() (int, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.legacy, l.ctxDials
}

var errStubRefused = errors.New("dial tcp 127.0.0.1:1: connect: connection refused")

// stubOpenRetryDials replaces both dial seams. legacyErr is what the fail-fast
// probe returns; ctxResults are consumed in order by the retry loop, a nil
// entry meaning "the server answered". Results beyond the slice repeat the
// last one, so a test can say "always fails" with a single entry.
func stubOpenRetryDials(t *testing.T, legacyErr error, ctxResults []error) *openRetryDialLog {
	t.Helper()
	log := &openRetryDialLog{}

	origLegacy := openProbeDial
	origCtx := openProbeDialContext
	t.Cleanup(func() {
		openProbeDial = origLegacy
		openProbeDialContext = origCtx
	})

	openProbeDial = func(network, addr string, timeout time.Duration) (net.Conn, error) {
		log.mu.Lock()
		log.legacy++
		log.mu.Unlock()
		if legacyErr != nil {
			return nil, legacyErr
		}
		return stubProbeConn(t), nil
	}
	openProbeDialContext = func(ctx context.Context, network, addr string, timeout time.Duration) (net.Conn, error) {
		log.mu.Lock()
		log.ctxDials++
		idx := log.ctxDials - 1
		log.timeouts = append(log.timeouts, timeout)
		log.mu.Unlock()
		if len(ctxResults) == 0 {
			return nil, legacyErr
		}
		if idx >= len(ctxResults) {
			idx = len(ctxResults) - 1
		}
		if err := ctxResults[idx]; err != nil {
			return nil, err
		}
		return stubProbeConn(t), nil
	}
	return log
}

// recordingConn is a real net.Conn that records how it was disposed of. The
// recording is the whole point of the cancel-after-success test: "was it
// drained" and "was it closed" are DIFFERENT questions, and a fixture whose
// peer is already closed answers neither -- every write fails on such a pipe
// whether or not the code under test touched the connection.
type recordingConn struct {
	net.Conn
	peer            net.Conn
	mu              sync.Mutex
	closes          int
	reads           int
	readDeadlineSet int
}

func (c *recordingConn) Read(b []byte) (int, error) {
	c.mu.Lock()
	c.reads++
	c.mu.Unlock()
	return c.Conn.Read(b)
}

func (c *recordingConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.readDeadlineSet++
	c.mu.Unlock()
	return c.Conn.SetReadDeadline(t)
}

func (c *recordingConn) Close() error {
	c.mu.Lock()
	c.closes++
	c.mu.Unlock()
	return c.Conn.Close()
}

func (c *recordingConn) stats() (closes, reads, deadlines int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closes, c.reads, c.readDeadlineSet
}

// stubProbeConn returns a connected, LIVE net.Conn: the peer stays open, so a
// read blocks until its deadline instead of returning at once. That is what
// makes "the probe was drained" observable -- DrainAndCloseProbe sets a read
// deadline and reads, a bare Close does neither.
func stubProbeConn(t *testing.T) *recordingConn {
	t.Helper()
	a, b := net.Pipe()
	c := &recordingConn{Conn: a, peer: b}
	t.Cleanup(func() {
		_ = a.Close()
		_ = b.Close()
	})
	return c
}

// writeBudgetConfig writes a .beads/config.yaml carrying the given key/value in
// the nested form the dolt.* keys use, and returns the .beads path.
func writeBudgetConfig(t *testing.T, kv map[string]string) string {
	t.Helper()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if len(kv) == 0 {
		return beadsDir
	}
	var b strings.Builder
	b.WriteString("dolt:\n")
	for k, v := range kv {
		fmt.Fprintf(&b, "  %s: %q\n", strings.TrimPrefix(k, "dolt."), v)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	return beadsDir
}

// externalOpenConfig is an open against a server bd does not manage: no data
// path, so serverOpenCanAutoStart is false and newServerMode takes the
// fail-fast branch the budget gates.
func externalOpenConfig(beadsDir string) *Config {
	return &Config{
		Database:   "test_open_retry",
		ServerHost: "127.0.0.1",
		ServerPort: 1,
		AutoStart:  false,
		BeadsDir:   beadsDir,
	}
}

// --- default off -------------------------------------------------------

// The budget is opt-in. With no key set, the open must make exactly one dial
// through the legacy context-free seam and return today's error text -- and it
// must do so for a cancelled and a short-deadline context too, because
// net.DialTimeout cannot see either and the default path must stay that way.
func TestOpenRetryOffMakesOneLegacyDialAndKeepsTheError(t *testing.T) {
	baseline := ""
	for _, tc := range []struct {
		name string
		ctx  func(t *testing.T) context.Context
	}{
		{"background", func(*testing.T) context.Context { return context.Background() }},
		{"already-cancelled", func(t *testing.T) context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}},
		{"short-deadline", func(t *testing.T) context.Context {
			ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
			t.Cleanup(cancel)
			return ctx
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BEADS_TEST_MODE", "1")
			log := stubOpenRetryDials(t, errStubRefused, nil)
			cfg := externalOpenConfig(writeBudgetConfig(t, nil))

			_, err := newServerMode(tc.ctx(t), cfg)
			if err == nil {
				t.Fatal("expected the fail-fast unreachable error")
			}
			legacy, ctxDials := log.counts()
			if legacy != 1 || ctxDials != 0 {
				t.Fatalf("dials legacy=%d ctx=%d, want 1 and 0: the default path must not retry and must not become context-aware", legacy, ctxDials)
			}
			if !strings.Contains(err.Error(), "Dolt server unreachable at 127.0.0.1:1") {
				t.Fatalf("error text changed on the default path: %v", err)
			}
			if baseline == "" {
				baseline = err.Error()
			} else if err.Error() != baseline {
				t.Fatalf("default-path error differs by context:\n got: %s\nwant: %s", err.Error(), baseline)
			}
		})
	}
}

// A value that cannot buy anything -- unparseable, negative, zero -- is off,
// not an error and not a partial engage.
func TestOpenRetryUnusableValuesAreOff(t *testing.T) {
	for _, value := range []string{"", "0", "banana", "-5s", "-1"} {
		t.Run("value="+value, func(t *testing.T) {
			t.Setenv("BEADS_TEST_MODE", "1")
			log := stubOpenRetryDials(t, errStubRefused, nil)
			cfg := externalOpenConfig(writeBudgetConfig(t, map[string]string{config.OpenRetryBudgetKey: value}))

			if _, err := newServerMode(context.Background(), cfg); err == nil {
				t.Fatal("expected the fail-fast unreachable error")
			}
			if legacy, ctxDials := log.counts(); legacy != 1 || ctxDials != 0 {
				t.Fatalf("value %q engaged the budget: legacy=%d ctx=%d, want 1 and 0", value, legacy, ctxDials)
			}
		})
	}
}

// --- budget on ---------------------------------------------------------

// With the budget on, a retryable failure is retried and a later success ends
// the probe: the open gets past the connectivity check and fails further in,
// on the MySQL connection, not on "Dolt server unreachable".
func TestOpenRetryOnSucceedsOnLaterAttempt(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "1")
	log := stubOpenRetryDials(t, errStubRefused, []error{errStubRefused, nil})
	cfg := externalOpenConfig(writeBudgetConfig(t, map[string]string{config.OpenRetryBudgetKey: "5s"}))

	_, err := newServerMode(context.Background(), cfg)
	if err != nil && strings.Contains(err.Error(), "Dolt server unreachable at") {
		t.Fatalf("probe still reported unreachable after a successful retry: %v", err)
	}
	legacy, ctxDials := log.counts()
	if legacy != 1 {
		t.Fatalf("legacy dials = %d, want exactly the one fail-fast probe", legacy)
	}
	if ctxDials != 2 {
		t.Fatalf("retry dials = %d, want 2 (one failure then the success)", ctxDials)
	}
}

// Exhaustion is bounded by the budget. The ceiling is the budget plus one
// probe timeout, because the first probe is deliberately outside the budget.
func TestOpenRetryExhaustionIsBounded(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "1")
	const budget = 300 * time.Millisecond
	stubOpenRetryDials(t, errStubRefused, []error{errStubRefused})
	cfg := externalOpenConfig(writeBudgetConfig(t, map[string]string{config.OpenRetryBudgetKey: budget.String()}))

	start := time.Now()
	_, err := newServerMode(context.Background(), cfg)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected the open to fail once the budget was spent")
	}
	// budget + the 500ms fail-fast probe timeout + slack for the MySQL dial
	// the open still attempts after the probe branch returns.
	if ceiling := budget + 500*time.Millisecond + 2*time.Second; elapsed > ceiling {
		t.Fatalf("exhaustion took %v, over the %v ceiling: the loop is not bounded by the budget", elapsed, ceiling)
	}
	if !strings.Contains(err.Error(), "still unreachable after") {
		t.Fatalf("exhaustion error lost its attempt/budget detail: %v", err)
	}
}

// A non-retryable failure is not retried at all: the budget buys time for a
// restarting server, not for a misconfigured one.
func TestOpenRetryNonRetryableFailsImmediately(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "1")
	log := stubOpenRetryDials(t, errors.New("dial tcp: lookup nowhere.invalid: no such host"), nil)
	cfg := externalOpenConfig(writeBudgetConfig(t, map[string]string{config.OpenRetryBudgetKey: "30s"}))

	if _, err := newServerMode(context.Background(), cfg); err == nil {
		t.Fatal("expected the open to fail")
	}
	if _, ctxDials := log.counts(); ctxDials != 0 {
		t.Fatalf("retry dials = %d, want 0 for a non-retryable dial error", ctxDials)
	}
}

// --- the maintainer's ask: modes the budget must not reach ---------------

// A bd-managed localhost server recovers by STARTING a server, so the budget
// must not engage even when it is set. This is the case newServerMode's
// auto-start branch handles, and its post-auto-start dial must stay the bare
// legacy one.
func TestOpenRetryNotEngagedForManagedLocalhostOpen(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "1")
	log := stubOpenRetryDials(t, errStubRefused, nil)

	origEnsure := ensureRunningDetailed
	t.Cleanup(func() { ensureRunningDetailed = origEnsure })
	ensureRunningDetailed = func(beadsDir string) (int, bool, error) {
		return 1, false, nil // adopted an existing server on the same port
	}

	beadsDir := writeBudgetConfig(t, map[string]string{config.OpenRetryBudgetKey: "30s"})
	cfg := &Config{
		Database:   "test_open_retry_managed",
		Path:       filepath.Join(beadsDir, "dolt"),
		BeadsDir:   beadsDir,
		ServerHost: "127.0.0.1",
		ServerPort: 1,
		AutoStart:  true, // bd manages this one
	}
	if !serverOpenCanAutoStart(cfg) {
		t.Fatal("fixture no longer describes a bd-managed localhost open; the test would pass vacuously")
	}

	_, _ = newServerMode(context.Background(), cfg)

	legacy, ctxDials := log.counts()
	if ctxDials != 0 {
		t.Fatalf("retry dials = %d, want 0: a bd-managed localhost open must never reach the budget", ctxDials)
	}
	if legacy != 2 {
		t.Fatalf("legacy dials = %d, want 2 (fail-fast probe + post-auto-start dial), both through the context-free seam", legacy)
	}
}

// Embedded mode has no sql-server to dial and never reaches newServerMode.
// Its structural guarantee is that auto-start resolution refuses to treat it
// as a server open at all; assert the mode itself so a future refactor that
// routed embedded through the server path would have to face this test.
func TestOpenRetryNotReachableFromEmbeddedMode(t *testing.T) {
	beadsDir := writeBudgetConfig(t, map[string]string{config.OpenRetryBudgetKey: "30s"})
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"),
		[]byte(`{"backend":"dolt","dolt_mode":"embedded"}`), 0o644); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}
	if mode := doltserver.ResolveServerMode(beadsDir); mode != ServerModeEmbedded {
		t.Fatalf("ResolveServerMode = %v, want embedded: the fixture no longer describes an embedded project", mode)
	}
	cfg := &Config{Path: filepath.Join(beadsDir, "dolt"), BeadsDir: beadsDir}
	ApplyCLIAutoStart(beadsDir, cfg)
	// An embedded project is opened by the embedded backend, not by
	// newServerMode; nothing here dials, so nothing here can retry.
	if serverOpenCanAutoStart(cfg) {
		t.Fatal("embedded project resolved as a bd-managed server open")
	}
}

// --- circuit breaker accounting -----------------------------------------

// One failed open records exactly one failure, and an open the CALLER ended
// records none: a cancelled open is evidence about the caller, not about the
// server, and five of them would otherwise trip the breaker against a healthy
// server.
func TestOpenRetryBreakerAccounting(t *testing.T) {
	for _, tc := range []struct {
		name         string
		budget       string
		cancelAfter  time.Duration
		wantFailures int
	}{
		{"budget off", "", 0, 1},
		{"budget on, exhausted", "200ms", 0, 1},
		{"budget on, caller cancels", "30s", 150 * time.Millisecond, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The breaker is nil under BEADS_TEST_MODE, so this test has to
			// run without it while keeping its own isolated state dir.
			t.Setenv("BEADS_TEST_MODE", "")
			t.Setenv(testCircuitBreakerDirEnv, filepath.Join(t.TempDir(), "circuit"))
			stubOpenRetryDials(t, errStubRefused, []error{errStubRefused})

			kv := map[string]string{}
			if tc.budget != "" {
				kv[config.OpenRetryBudgetKey] = tc.budget
			}
			cfg := externalOpenConfig(writeBudgetConfig(t, kv))
			cfg.Database = fmt.Sprintf("test_open_retry_breaker_%d", time.Now().UnixNano())

			ctx := context.Background()
			if tc.cancelAfter > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.cancelAfter)
				defer cancel()
			}
			if _, err := newServerMode(ctx, cfg); err == nil {
				t.Fatal("expected the open to fail")
			}

			cb := newServerCircuitBreaker(cfg.ServerHost, cfg.ServerPort, cfg.Database)
			if cb == nil {
				t.Fatal("no breaker for this endpoint; the assertion below could not fail")
			}
			if got := cb.readState().Failures; got != tc.wantFailures {
				t.Fatalf("breaker failures = %d, want %d", got, tc.wantFailures)
			}
		})
	}
}

// A dial that lands after the caller cancelled must not be reported as a
// success, and the connection it produced must be DRAINED rather than closed
// bare: a bare Close on an unread probe makes the OS send RST, which dolt
// sql-server can crash on (GH#4132, #4133). Asserting only "it was closed"
// would pass on the bare Close this test exists to forbid, so it asserts the
// drain -- a read under a deadline -- as well.
func TestOpenRetryCancelAfterSuccessDrainsTheProbe(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "1")

	ctx, cancel := context.WithCancel(context.Background())
	var probe *recordingConn
	origCtxDial := openProbeDialContext
	origLegacy := openProbeDial
	t.Cleanup(func() {
		openProbeDialContext = origCtxDial
		openProbeDial = origLegacy
	})
	openProbeDial = func(string, string, time.Duration) (net.Conn, error) { return nil, errStubRefused }
	openProbeDialContext = func(context.Context, string, string, time.Duration) (net.Conn, error) {
		// The dial succeeds, then the caller cancels before the loop looks.
		probe = stubProbeConn(t)
		cancel()
		return probe, nil
	}

	cfg := externalOpenConfig(writeBudgetConfig(t, map[string]string{config.OpenRetryBudgetKey: "5s"}))
	_, err := newServerMode(ctx, cfg)
	if err == nil {
		t.Fatal("expected a cancelled open to fail rather than report success")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled open lost its context cause: %v", err)
	}
	if probe == nil {
		t.Fatal("the retry dial never ran; nothing was disposed of")
	}
	closes, reads, deadlines := probe.stats()
	if closes != 1 {
		t.Fatalf("probe closes = %d, want 1: a probe abandoned after a cancelled success leaks the connection", closes)
	}
	if reads == 0 || deadlines == 0 {
		t.Fatalf("probe reads=%d deadlines=%d, want both > 0: the connection was closed BARE instead of drained, which is the RST that crashes dolt sql-server (GH#4132)", reads, deadlines)
	}
}

// --- dialer wiring ------------------------------------------------------

// The per-attempt timeout must reach the dialer. Reading the field back is the
// only form of this check that the environment cannot satisfy on its own: a
// network that REJECTS the address cannot exhibit a timeout at all, so an
// elapsed-time assertion would pass with the timeout dropped.
func TestOpenRetryDialerCarriesThePerAttemptTimeout(t *testing.T) {
	for _, want := range []time.Duration{250 * time.Millisecond, 3 * time.Second} {
		d := newServerDialer(want)
		if d.Timeout != want {
			t.Fatalf("newServerDialer(%v).Timeout = %v, want %v", want, d.Timeout, want)
		}
	}
}

// The backoff ceiling must follow the budget, not the shared 30s server-retry
// ceiling. Without this the loop would call it a day at 30s and silently
// ignore the rest of an operator's larger budget -- a failure the timed
// exhaustion test cannot see, because it uses a sub-second budget where the
// deadline check fires first. This is why newOpenRetryBackoff exists.
func TestOpenRetryBackoffCeilingFollowsTheBudget(t *testing.T) {
	for _, budget := range []time.Duration{200 * time.Millisecond, serverRetryMaxElapsed, 2 * time.Minute} {
		if got := newOpenRetryBackoff(budget).MaxElapsedTime; got != budget {
			t.Fatalf("newOpenRetryBackoff(%v).MaxElapsedTime = %v, want %v", budget, got, budget)
		}
	}
	if got := newOpenRetryBackoff(2 * time.Minute).MaxElapsedTime; got <= serverRetryMaxElapsed {
		t.Fatalf("a budget above the shared %v ceiling did not raise it (got %v): the loop would stop early and ignore the rest of the budget", serverRetryMaxElapsed, got)
	}
	// A non-positive budget cannot reach the loop, but must still leave a
	// bounded schedule rather than an unbounded one.
	if got := newOpenRetryBackoff(0).MaxElapsedTime; got != serverRetryMaxElapsed {
		t.Fatalf("newOpenRetryBackoff(0).MaxElapsedTime = %v, want the default %v", got, serverRetryMaxElapsed)
	}
}

// --- the contract with the sibling key ----------------------------------

// dolt.open-retry-budget must resolve with the same scope rules as
// dolt.auto-start. Both go through configStringForDir, so this test compares
// the two keys over identical fixtures; re-spelling either reader as a plain
// config.GetString turns the directory rows red.
func TestOpenRetryBudgetSiblingScopeParity(t *testing.T) {
	const budget = "45s"
	for _, tc := range []struct {
		name    string
		yaml    string
		wantRaw string
	}{
		{"absent", "", ""},
		{"nested in the project config.yaml", "dolt:\n  auto-start: \"false\"\n  open-retry-budget: \"" + budget + "\"\n", "set"},
		{"nested with other dolt keys around it", "dolt:\n  debug: \"true\"\n  auto-start: \"false\"\n  open-retry-budget: \"" + budget + "\"\n  shared-server: \"false\"\n", "set"},
		{"empty dolt block", "dolt:\n", ""},
		{"unrelated top-level keys only", "export:\n  path: beads.jsonl\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir := filepath.Join(t.TempDir(), ".beads")
			if err := os.MkdirAll(beadsDir, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if tc.yaml != "" {
				if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(tc.yaml), 0o644); err != nil {
					t.Fatalf("write: %v", err)
				}
			}

			sibling := configStringForDir(beadsDir, "dolt.auto-start")
			mine := configStringForDir(beadsDir, config.OpenRetryBudgetKey)

			if tc.wantRaw == "" {
				if sibling != "" || mine != "" {
					t.Fatalf("expected both keys unresolved, got auto-start=%q budget=%q", sibling, mine)
				}
				return
			}
			// Both keys are present in the fixture, so both must resolve. A
			// reader that lost the directory fallback returns "" for its key
			// while the other still answers -- which is exactly the drift
			// this test exists to catch.
			if sibling == "" {
				t.Fatal("dolt.auto-start did not resolve; the fixture is wrong, not the budget")
			}
			if mine != budget {
				t.Fatalf("%s = %q, want %q: it no longer resolves with the same scope rules as dolt.auto-start", config.OpenRetryBudgetKey, mine, budget)
			}
		})
	}
}

// The runtime reader and `bd config set` share one parser, so a value the
// validator accepts can never resolve to something the validator never saw.
func TestOpenRetryBudgetReaderMatchesValidator(t *testing.T) {
	for _, value := range []string{"30s", "2m", "500ms", "1m30s", "45", "0", "", "banana", "-5s", "18446744074", "-9223372037"} {
		beadsDir := writeBudgetConfig(t, map[string]string{config.OpenRetryBudgetKey: value})
		got := openRetryBudget(beadsDir)
		want, err := config.ParseOpenRetryBudget(value)
		if err != nil {
			want = 0
		}
		if got != want {
			t.Fatalf("openRetryBudget(%q) = %v, want %v", value, got, want)
		}
		if got < 0 {
			t.Fatalf("openRetryBudget(%q) resolved negative (%v); the loop would be unbounded", value, got)
		}
	}
}
