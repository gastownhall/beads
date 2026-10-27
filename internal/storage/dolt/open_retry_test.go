package dolt

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/doltserver"
)

// Tests for the config-gated open-retry budget (dolt.open-retry-budget,
// GH#4379). They stub openProbeDial / openProbeDialContext so they run without
// a live Dolt server, in the style of socket_fallback_test.go.
//
// Every test here drives the REAL newServerMode. The budget's mode gate is its
// call sites inside it -- the branch taken when serverOpenCanAutoStart says bd
// cannot start a server here, and the auto-start branch when EnsureRunning
// refuses to start an externally managed one -- so a test that called
// retryOpenProbe directly would prove nothing about which opens reach it.

// openRetryDialLog counts the two dial seams separately. Keeping them apart is
// the whole point: "the default path is untouched" means the legacy,
// context-free openProbeDial ran exactly once and the retry loop's
// context-aware dial ran zero times.
type openRetryDialLog struct {
	mu             sync.Mutex
	start          time.Time
	legacy         int
	ctxDials       int
	retries        []retryDial
	legacyTimeouts []time.Duration
}

// retryDial records WHEN a retry dial started and WITH WHICH per-attempt
// timeout. Both are needed to check that the loop never plans to dial past its
// deadline: offset+timeout must stay inside the budget.
type retryDial struct {
	offset  time.Duration
	timeout time.Duration
}

func (l *openRetryDialLog) counts() (int, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.legacy, l.ctxDials
}

func (l *openRetryDialLog) dials() []retryDial {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]retryDial(nil), l.retries...)
}

// legacyDialTimeouts returns the per-dial timeout each LEGACY probe was handed,
// in order. Recorded because the first probe's timeout is the one thing the
// budget promises never to shorten: without this the probe could be cut to a
// nanosecond and every assertion in this file would stay green, since they all
// count dials rather than measure them.
func (l *openRetryDialLog) legacyDialTimeouts() []time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]time.Duration(nil), l.legacyTimeouts...)
}

// wantFirstProbeTimeout is newServerMode's fail-fast probe timeout, written
// out rather than read from the code under test: a baseline learned from the
// implementation moves with it, and this value is the budget's central
// promise -- the first probe is never shortened, so no setting can make bd
// less patient than the default.
const wantFirstProbeTimeout = 500 * time.Millisecond

// assertFirstProbeTimeout checks the fail-fast probe kept its full timeout.
// Every test that reaches the probe can call it, so a change to that timeout
// cannot hide in whichever test happens not to look.
func assertFirstProbeTimeout(t *testing.T, log *openRetryDialLog) {
	t.Helper()
	timeouts := log.legacyDialTimeouts()
	if len(timeouts) == 0 {
		t.Fatal("no legacy probe was made, so its timeout cannot be asserted")
	}
	if timeouts[0] != wantFirstProbeTimeout {
		t.Fatalf("first probe timeout = %v, want %v: the budget must never shorten the fail-fast probe", timeouts[0], wantFirstProbeTimeout)
	}
}

var errStubRefused = errors.New("dial tcp 127.0.0.1:1: connect: connection refused")

// stubOpenRetryDials replaces both dial seams. legacyErr is what the fail-fast
// probe returns; ctxResults are consumed in order by the retry loop, a nil
// entry meaning "the server answered". Results beyond the slice repeat the
// last one, so a test can say "always fails" with a single entry.
func stubOpenRetryDials(t *testing.T, legacyErr error, ctxResults []error) *openRetryDialLog {
	t.Helper()
	log := &openRetryDialLog{start: time.Now()}

	origLegacy := openProbeDial
	origCtx := openProbeDialContext
	t.Cleanup(func() {
		openProbeDial = origLegacy
		openProbeDialContext = origCtx
	})

	openProbeDial = func(network, addr string, timeout time.Duration) (net.Conn, error) {
		log.mu.Lock()
		log.legacy++
		log.legacyTimeouts = append(log.legacyTimeouts, timeout)
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
		log.retries = append(log.retries, retryDial{offset: time.Since(log.start), timeout: timeout})
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
	writeBudgetConfigIn(t, beadsDir, kv)
	return beadsDir
}

// writeBudgetConfigIn is writeBudgetConfig for a .beads directory that already
// exists, such as a workspace whose metadata.json a test wrote first.
func writeBudgetConfigIn(t *testing.T, beadsDir string, kv map[string]string) {
	t.Helper()
	if len(kv) == 0 {
		return
	}
	var b strings.Builder
	b.WriteString("dolt:\n")
	for k, v := range kv {
		fmt.Fprintf(&b, "  %s: %q\n", strings.TrimPrefix(k, "dolt."), v)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
}

// externalOpenConfig is an open against a server bd does not manage: no data
// path, so serverOpenCanAutoStart is false and newServerMode takes the
// fail-fast branch the budget gates.
//
// ServerMode is set because it is the second half of that gate, and it is the
// half a fixture can get wrong silently: it is the predicate the CLI store
// factory routes on (newDoltStore sends a config to dolt.New only under
// `if cfg.ServerMode`), so an open that reaches newServerMode from the CLI
// always carries it. A fixture without it would describe a workspace bd would
// have opened embedded, and every budget-on assertion below would then be
// asserting the wrong path.
func externalOpenConfig(beadsDir string) *Config {
	return &Config{
		Database:   "test_open_retry",
		ServerHost: "127.0.0.1",
		ServerPort: 1,
		AutoStart:  false,
		ServerMode: true,
		BeadsDir:   beadsDir,
	}
}

// --- default off -------------------------------------------------------

// The budget is opt-in. With no key set, the open must make exactly one dial
// through the legacy context-free seam and return today's error text -- and it
// must do so for a canceled and a short-deadline context too, because
// net.DialTimeout cannot see either and the default path must stay that way.
func TestOpenRetryOffMakesOneLegacyDialAndKeepsTheError(t *testing.T) {
	// Written out in full rather than captured from the first run: a baseline
	// learned from the code under test moves with it, so a change that
	// rewrote the cause or the hint consistently would still pass.
	wantErr := "Dolt server unreachable at 127.0.0.1:1: " + errStubRefused.Error() +
		"\n\nThe Dolt server may not be running. Try:\n  bd dolt start"

	for _, tc := range []struct {
		name string
		ctx  func(t *testing.T) context.Context
	}{
		{"background", func(*testing.T) context.Context { return context.Background() }},
		{"already-canceled", func(t *testing.T) context.Context {
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
			// With the budget absent nothing may touch the probe's timeout
			// either: shortening it would degrade the default open, and
			// counting dials cannot see that.
			assertFirstProbeTimeout(t, log)
			if err.Error() != wantErr {
				t.Fatalf("default-path error changed:\n got: %q\nwant: %q", err.Error(), wantErr)
			}
			if !errors.Is(err, errStubRefused) {
				t.Fatalf("default-path error stopped wrapping the dial cause: %v", err)
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
	assertFirstProbeTimeout(t, log)
}

// Exhaustion is bounded by the budget, and every retry is planned to finish
// inside it.
//
// The budget must be large enough to buy real retries: the shared backoff's
// first delay is 250-750ms, so a sub-second budget breaks out of the loop
// before dialing anything and would prove only that the loop terminates.
func TestOpenRetryExhaustionIsBounded(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "1")
	const budget = 2 * time.Second
	log := stubOpenRetryDials(t, errStubRefused, []error{errStubRefused})
	cfg := externalOpenConfig(writeBudgetConfig(t, map[string]string{config.OpenRetryBudgetKey: budget.String()}))

	start := time.Now()
	_, err := newServerMode(context.Background(), cfg)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected the open to fail once the budget was spent")
	}
	// The budget is spent in full here, which is where a first probe borrowing
	// from it would be least visible.
	assertFirstProbeTimeout(t, log)
	dials := log.dials()
	if len(dials) < 2 {
		t.Fatalf("retry dials = %d, want at least 2: the budget did not buy real retries, so nothing about the loop was exercised", len(dials))
	}
	for i, d := range dials {
		if d.timeout <= 0 || d.timeout > 500*time.Millisecond {
			t.Fatalf("retry %d dial timeout = %v, want a positive value no larger than the 500ms probe timeout", i+1, d.timeout)
		}
		// The clamp's contract: a retry never PLANS to run past the deadline.
		if d.offset+d.timeout > budget+50*time.Millisecond {
			t.Fatalf("retry %d starts at %v with a %v timeout, which reaches past the %v budget: the remaining-budget clamp is not applied", i+1, d.offset, d.timeout, budget)
		}
	}
	// budget + the 500ms fail-fast probe timeout, which is deliberately
	// outside the budget, + slack for the MySQL dial the open still attempts.
	if ceiling := budget + 500*time.Millisecond + time.Second; elapsed > ceiling {
		t.Fatalf("exhaustion took %v, over the %v ceiling: the loop is not bounded by the budget", elapsed, ceiling)
	}
	if !strings.Contains(err.Error(), "still unreachable after") {
		t.Fatalf("exhaustion error lost its attempt/budget detail: %v", err)
	}
}

// A budget smaller than the first probe's own timeout buys nothing and must
// cost nothing: the fail-fast probe keeps its full 500ms and no retry starts.
// The budget-on tests above use 2s and 5s, both above the probe timeout, so
// without this case a clamp of the FIRST probe to a sub-500ms budget would
// go unnoticed.
func TestOpenRetryTinyBudgetKeepsTheFirstProbeIntact(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "1")
	log := stubOpenRetryDials(t, errStubRefused, nil)
	cfg := externalOpenConfig(writeBudgetConfig(t, map[string]string{config.OpenRetryBudgetKey: "1ns"}))

	if _, err := newServerMode(context.Background(), cfg); err == nil {
		t.Fatal("expected the fail-fast unreachable error")
	}
	legacy, ctxDials := log.counts()
	if legacy != 1 || ctxDials != 0 {
		t.Fatalf("legacy=%d ctx=%d, want 1 and 0: a 1ns budget must degrade to exactly the fail-fast open", legacy, ctxDials)
	}
	assertFirstProbeTimeout(t, log)
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

// errnoBehindOpaqueText carries an errno behind a message that does not name
// it, so only a classifier that reads the errno can tell what the dial hit.
type errnoBehindOpaqueText struct{ errno syscall.Errno }

func (e errnoBehindOpaqueText) Error() string { return "dial tcp 127.0.0.1:1: connect failed" }
func (e errnoBehindOpaqueText) Unwrap() error { return e.errno }

// A refusal is retried whatever words it arrives in. A Windows client is
// refused with Winsock's WSAECONNREFUSED, errno 10061, which formats as "No
// connection could be made because the target machine actively refused it."
// there (or a localized form) and as "errno 10061" elsewhere: neither says
// "connection refused", the only refusal the text classifier knows. The
// Windows row is the error a Windows dial returns, built by hand with the
// errno spelled as a number so that it runs on every platform.
//
// The first probe AND the first retry fail with the refusal, so the case
// fails if either classification site reads only the text.
func TestOpenRetryRetriesARefusalByItsErrno(t *testing.T) {
	for _, tc := range []struct {
		name    string
		refused error
	}{
		{"windows WSAECONNREFUSED", &net.OpError{
			Op:   "dial",
			Net:  "tcp",
			Addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1},
			Err:  os.NewSyscallError("connectex", syscall.Errno(10061)),
		}},
		{"ECONNREFUSED behind text that does not say so", errnoBehindOpaqueText{syscall.ECONNREFUSED}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if text := strings.ToLower(tc.refused.Error()); strings.Contains(text, "connection refused") {
				t.Fatalf("fixture %q carries the POSIX wording, so it cannot show that the errno alone is recognized", text)
			}
			t.Setenv("BEADS_TEST_MODE", "1")
			log := stubOpenRetryDials(t, tc.refused, []error{tc.refused, nil})
			cfg := externalOpenConfig(writeBudgetConfig(t, map[string]string{config.OpenRetryBudgetKey: "5s"}))

			_, err := newServerMode(context.Background(), cfg)
			if err != nil && strings.Contains(err.Error(), "Dolt server unreachable at") {
				t.Fatalf("a refused dial ended the open instead of being retried: %v", err)
			}
			legacy, ctxDials := log.counts()
			if legacy != 1 || ctxDials != 2 {
				t.Fatalf("dials legacy=%d ctx=%d, want 1 and 2: the refused first probe and the refused first retry must both be retried", legacy, ctxDials)
			}
			assertFirstProbeTimeout(t, log)
		})
	}
}

// --- the maintainer's ask: modes the budget must not reach ---------------

// A bd-managed localhost server recovers by STARTING a server, so the budget
// must not engage even when it is set. This is the case newServerMode's
// auto-start branch handles, and its post-auto-start dial must stay the bare
// legacy one.
//
// The fixture carries ServerMode, because the exclusion under test is the
// AUTO-START BRANCH, where it starts or adopts a server, and nothing else (a
// refused start is TestOpenRetryAppliesWhenAutoStartIsRefused's case). Without
// it the config would fail the
// budget's other term as well, and the test would report a zero retry count
// whatever the auto-start dispatch did -- so moving the guarded retry block
// ahead of that dispatch would leave this test green while managed opens
// started waiting. The second row is the control that makes the first one
// mean something: the SAME fixture with auto-start off is no longer a managed
// open, and it retries.
func TestOpenRetryNotEngagedForManagedLocalhostOpen(t *testing.T) {
	for _, tc := range []struct {
		name       string
		autoStart  bool
		wantLegacy int
		wantRetry  bool
	}{
		{
			name:       "bd manages this server",
			autoStart:  true,
			wantLegacy: 2, // fail-fast probe + post-auto-start dial
		},
		{
			name:       "same fixture, bd does not manage it",
			autoStart:  false,
			wantLegacy: 1,
			wantRetry:  true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BEADS_TEST_MODE", "1")
			// The retry, if one happens, succeeds immediately: this test asks
			// WHETHER the loop was entered, and an exhaustion run would spend
			// the whole 30s budget to answer the same question.
			log := stubOpenRetryDials(t, errStubRefused, []error{nil})

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
				AutoStart:  tc.autoStart,
				// The budget's mode term. The exclusion asserted here is the
				// auto-start branch, so this must be TRUE or the zero retry
				// count would be about the mode instead.
				ServerMode: true,
			}
			// Positive control on the budget itself, so a zero retry count is
			// about the branch and not about an unreadable key.
			if got := openRetryBudget(beadsDir); got != 30*time.Second {
				t.Fatalf("openRetryBudget = %v, want 30s: the fixture would prove nothing", got)
			}
			if got := serverOpenCanAutoStart(cfg); got != tc.autoStart {
				t.Fatalf("serverOpenCanAutoStart = %v, want %v: the fixture no longer describes the open this row is about", got, tc.autoStart)
			}

			_, _ = newServerMode(context.Background(), cfg)

			legacy, ctxDials := log.counts()
			if tc.wantRetry {
				if ctxDials == 0 {
					t.Fatal("no retry dial: without this control a zero retry count in the row above would prove nothing")
				}
			} else if ctxDials != 0 {
				t.Fatalf("retry dials = %d, want 0: a bd-managed localhost open must never reach the budget", ctxDials)
			}
			if legacy != tc.wantLegacy {
				t.Fatalf("legacy dials = %d, want %d, all through the context-free seam", legacy, tc.wantLegacy)
			}
		})
	}
}

// refusedAutoStartOpen is the open bd init makes for `bd init --server
// --server-port N` against a port nothing listens on. init builds its config by
// hand with auto-start on (cmd/bd/init.go), so the open takes the AUTO-START
// branch, although the port, caller-explicit and promoted into
// BEADS_DOLT_SERVER_PORT, declares the server externally managed and the real
// EnsureRunning refuses to start one. budget "" leaves the key unset. It
// returns the config and the endpoint it dials.
func refusedAutoStartOpen(t *testing.T, budget string) (*Config, string) {
	t.Helper()
	externalPortAdviceEnv(t)
	t.Setenv("BEADS_TEST_MODE", "1")
	port := deadLoopbackPort(t)
	t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(port))
	beadsDir := externalPortAdviceWorkspace(t, 0)
	if budget != "" {
		writeBudgetConfigIn(t, beadsDir, map[string]string{config.OpenRetryBudgetKey: budget})
	}
	cfg := &Config{
		Path:             filepath.Join(beadsDir, "dolt"),
		BeadsDir:         beadsDir,
		Database:         "advice",
		ServerHost:       "127.0.0.1",
		ServerPort:       port,
		ServerPortSource: doltserver.PortSourceCallerExplicit,
		AutoStart:        true,
		OpenedByInit:     true,
		ServerMode:       true,
	}
	if !serverOpenCanAutoStart(cfg) {
		t.Fatal("the fixture does not take the auto-start branch, so it says nothing about that branch's refusal")
	}
	return cfg, net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
}

// An open that takes the auto-start branch and is REFUSED a start, because the
// workspace's server is externally managed, started nothing: it is an open
// against a server bd does not manage, the open the budget is for, and it must
// wait as the same workspace does with auto-start off. The named-port rows run
// the real EnsureRunning, so they break if its refusal stops reaching the
// budget for any reason. The spent-budget row keeps the advice the refusal gave
// before the budget, which names the setting and never suggests `bd dolt
// start`; the off row keeps the default, one probe and no retry. The last row
// stands in a refusal no setting names, which bd reports in EnsureRunning's own
// words.
func TestOpenRetryAppliesWhenAutoStartIsRefused(t *testing.T) {
	t.Run("named port, the retry reaches the server", func(t *testing.T) {
		cfg, _ := refusedAutoStartOpen(t, "5s")
		log := stubOpenRetryDials(t, errStubRefused, []error{errStubRefused, nil})

		_, err := newServerMode(context.Background(), cfg)
		if err != nil && strings.Contains(err.Error(), "Dolt server unreachable at") {
			t.Fatalf("the refused auto-start ended the open without the budget: %v", err)
		}
		if legacy, ctxDials := log.counts(); legacy != 1 || ctxDials != 2 {
			t.Fatalf("dials legacy=%d ctx=%d, want 1 and 2: the fail-fast probe, then a refused retry and the one that reached the server", legacy, ctxDials)
		}
		assertFirstProbeTimeout(t, log)
	})

	t.Run("named port, the budget is spent", func(t *testing.T) {
		cfg, addr := refusedAutoStartOpen(t, "1500ms")
		log := stubOpenRetryDials(t, errStubRefused, []error{errStubRefused})

		_, err := newServerMode(context.Background(), cfg)
		if err == nil {
			t.Fatal("expected the open to fail once the budget was spent")
		}
		if _, ctxDials := log.counts(); ctxDials == 0 {
			t.Fatalf("no retry dial: the refused auto-start skipped the budget:\n%v", err)
		}
		msg := err.Error()
		for _, want := range []string{"--server-port declares the Dolt server at " + addr + " externally managed", "still unreachable after"} {
			if !strings.Contains(msg, want) {
				t.Errorf("error does not contain %q:\n%s", want, msg)
			}
		}
		for _, banned := range []string{"bd dolt start", "auto-start failed"} {
			if strings.Contains(msg, banned) {
				t.Errorf("error contains %q:\n%s", banned, msg)
			}
		}
	})

	t.Run("named port, budget off", func(t *testing.T) {
		cfg, addr := refusedAutoStartOpen(t, "")
		if got := openRetryBudget(cfg.BeadsDir); got != 0 {
			t.Fatalf("openRetryBudget = %v, want 0: the row is about the default", got)
		}
		log := stubOpenRetryDials(t, errStubRefused, nil)

		_, err := newServerMode(context.Background(), cfg)
		if err == nil {
			t.Fatal("expected the fail-fast unreachable error")
		}
		if legacy, ctxDials := log.counts(); legacy != 1 || ctxDials != 0 {
			t.Fatalf("dials legacy=%d ctx=%d, want 1 and 0: without the key the refusal must stay the fail-fast open", legacy, ctxDials)
		}
		if want := "--server-port declares the Dolt server at " + addr + " externally managed"; !strings.Contains(err.Error(), want) {
			t.Fatalf("error does not contain %q:\n%s", want, err)
		}
	})

	t.Run("named port, the open is canceled during the wait", func(t *testing.T) {
		cfg, _ := refusedAutoStartOpen(t, "5s")
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		log := stubOpenRetryDials(t, errStubRefused, []error{errStubRefused})
		stubbed := openProbeDialContext
		openProbeDialContext = func(dialCtx context.Context, network, addr string, timeout time.Duration) (net.Conn, error) {
			cancel()
			return stubbed(dialCtx, network, addr, timeout)
		}

		_, err := newServerMode(ctx, cfg)
		if err == nil {
			t.Fatal("expected the canceled open to fail")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled open lost its context cause:\n%v", err)
		}
		if _, ctxDials := log.counts(); ctxDials != 1 {
			t.Fatalf("retry dials = %d, want 1: the open must end at the cancel, not dial on", ctxDials)
		}
		// A canceled open says nothing about the server, so it carries no
		// advice about one.
		if strings.Contains(err.Error(), "externally managed") {
			t.Fatalf("canceled open carries advice about the server:\n%v", err)
		}
	})

	t.Run("a refusal no setting names", func(t *testing.T) {
		externalPortAdviceEnv(t)
		t.Setenv("BEADS_TEST_MODE", "1")
		port := deadLoopbackPort(t)
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		beadsDir := externalPortAdviceWorkspace(t, 0)
		writeBudgetConfigIn(t, beadsDir, map[string]string{config.OpenRetryBudgetKey: "5s"})
		origEnsure := ensureRunningDetailed
		t.Cleanup(func() { ensureRunningDetailed = origEnsure })
		ensureRunningDetailed = func(string) (int, bool, error) {
			return 0, false, fmt.Errorf("Dolt server is not running on port %d: %w", port, doltserver.ErrExternallyManaged)
		}
		cfg := &Config{
			Path:             filepath.Join(beadsDir, "dolt"),
			BeadsDir:         beadsDir,
			Database:         "advice",
			ServerHost:       "127.0.0.1",
			ServerPort:       port,
			ServerPortSource: doltserver.PortSourceConfigYaml,
			AutoStart:        true,
			ServerMode:       true,
		}
		if !serverOpenCanAutoStart(cfg) {
			t.Fatal("the fixture does not take the auto-start branch")
		}
		if hint := externallyManagedPortHint(cfg, beadsDir, addr); hint != "" {
			t.Fatalf("a setting names this port (%q), so the row is not about a refusal no setting names", hint)
		}
		log := stubOpenRetryDials(t, errStubRefused, []error{nil})

		_, err := newServerMode(context.Background(), cfg)
		if err != nil && strings.Contains(err.Error(), "Dolt server unreachable at") {
			t.Fatalf("the refused auto-start ended the open without the budget: %v", err)
		}
		if legacy, ctxDials := log.counts(); legacy != 1 || ctxDials != 1 {
			t.Fatalf("dials legacy=%d ctx=%d, want 1 and 1", legacy, ctxDials)
		}
	})
}

// Embedded mode has no sql-server to dial and never reaches newServerMode: it
// is opened by a different backend entirely. The reachable in-package
// guarantee is that an embedded project never resolves into a server open, so
// no probe -- and therefore no retry -- can happen for it. Both dial seams are
// stubbed so a future refactor that routed embedded through the server path
// would light this up rather than silently start retrying.
func TestOpenRetryNotReachableFromEmbeddedMode(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "1")
	// Hermeticity: the package test harness exports the test server's port
	// variables, and ResolveServerMode treats an ambient port variable as a
	// server-mode signal (#5934), which would resolve this embedded fixture
	// as external.
	t.Setenv("BEADS_CENTRAL_CONFIG", filepath.Join(t.TempDir(), "absent-central-config.json"))
	for _, key := range []string{
		"BEADS_DOLT_SERVER_MODE", "BEADS_DOLT_SHARED_SERVER",
		"BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT",
	} {
		t.Setenv(key, "")
	}
	log := stubOpenRetryDials(t, errStubRefused, nil)

	beadsDir := writeBudgetConfig(t, map[string]string{config.OpenRetryBudgetKey: "30s"})
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"),
		[]byte(`{"backend":"dolt","dolt_mode":"embedded"}`), 0o644); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}
	if mode := doltserver.ResolveServerMode(beadsDir); mode != ServerModeEmbedded {
		t.Fatalf("ResolveServerMode = %v, want embedded: the fixture no longer describes an embedded project", mode)
	}
	// A positive control on the budget itself: the key IS set here, so a zero
	// retry count below is about the mode, not about an unset budget.
	if got := openRetryBudget(beadsDir); got != 30*time.Second {
		t.Fatalf("openRetryBudget = %v, want 30s: the fixture would prove nothing", got)
	}

	cfg := &Config{Path: filepath.Join(beadsDir, "dolt"), BeadsDir: beadsDir}
	ApplyCLIAutoStart(beadsDir, cfg)
	if serverOpenCanAutoStart(cfg) {
		t.Fatal("embedded project resolved as a bd-managed server open")
	}
	if legacy, ctxDials := log.counts(); legacy != 0 || ctxDials != 0 {
		t.Fatalf("dials legacy=%d ctx=%d, want 0 and 0: resolving an embedded project must not probe a server at all", legacy, ctxDials)
	}
}

// The storage constructors do NOT take the workspace mode from the caller:
// NewFromConfig and NewFromConfigWithOptions build a Config, fill it in
// applyResolvedConfig, and hand it to New, which always calls newServerMode.
// Two kinds of caller arrive there and the budget must treat them differently.
//
//   - The CLI store factory's server branches -- routed creates
//     (store_factory.go NewFromConfig) and cross-workspace read-only opens
//     (NewFromConfigWithOptions with ReadOnly), plus their non-CGO twins --
//     have already classified the workspace as server-backed and call the
//     constructors without restating it. Those opens are what the budget is
//     for: during an external server restart they must wait, not fail fast.
//   - The diagnostics -- bd config drift, bd config apply, bd doctor's config
//     reader -- pass DisableAutoStart against whatever workspace they find,
//     embedded included, whose "server" is a TCP port with nothing behind it.
//     Waiting there is pure delay.
//
// applyResolvedConfig separates them by deriving cfg.ServerMode from the
// workspace's own metadata, the same classification cmd/bd/main.go applies
// before routing the primary CLI path. So every row below varies the METADATA
// and the constructor and NEVER hand-sets the flag: a row that supplied it
// would assert its own fixture instead of the wiring. Rows 1 and 5 differ only
// in the metadata, as do rows 3 and 4, so the metadata is shown to be what
// decides; rows 1 and 3 differ only in DisableAutoStart and agree, so the
// budget does not hang off that flag.
func TestOpenRetryFollowsTheWorkspaceModeThroughTheConstructors(t *testing.T) {
	const serverMetadata = `{"backend":"dolt","dolt_mode":"server","dolt_server_port":1,"dolt_database":"test_open_retry"}`
	const embeddedMetadata = `{"backend":"dolt","dolt_mode":"embedded","dolt_database":"test_open_retry"}`
	const proxiedMetadata = `{"backend":"dolt","dolt_mode":"proxied-server","dolt_database":"test_open_retry"}`

	// The two constructor shapes the routed store-factory branches use.
	newFromConfig := func(ctx context.Context, beadsDir string) {
		_, _ = NewFromConfig(ctx, beadsDir)
	}
	newReadOnly := func(ctx context.Context, beadsDir string) {
		_, _ = NewFromConfigWithOptions(ctx, beadsDir, &Config{ReadOnly: true})
	}
	newDiagnostic := func(ctx context.Context, beadsDir string) {
		_, _ = NewFromConfigWithOptions(ctx, beadsDir, &Config{ReadOnly: true, DisableAutoStart: true})
	}
	// The shape bd doctor's own stores use: NewFromConfigWithCLIOptions with a
	// plain read-only config and no DisableAutoStart. Every WithCLIOptions
	// caller in the tree is doctor, and the auto-start suppression there comes
	// from ApplyCLIAutoStart resolving ServerModeExternal rather than from the
	// caller asking for it -- a different route into the same gated branch,
	// which is why counting it here rather than assuming it matters.
	newCLIDiagnostic := func(ctx context.Context, beadsDir string) {
		_, _ = NewFromConfigWithCLIOptions(ctx, beadsDir, &Config{ReadOnly: true})
	}

	for _, tc := range []struct {
		name     string
		open     func(context.Context, string)
		metadata string
		// sharedServer turns on the shared-server env for this row, the one
		// setting that makes IsDoltServerMode answer true regardless of the
		// metadata mode.
		sharedServer bool
		wantRetry    bool
	}{
		{
			name:      "routed create, external workspace",
			open:      newFromConfig,
			metadata:  serverMetadata,
			wantRetry: true,
		},
		{
			name:      "routed read-only open, external workspace",
			open:      newReadOnly,
			metadata:  serverMetadata,
			wantRetry: true,
		},
		{
			name:      "diagnostic caller, external workspace",
			open:      newDiagnostic,
			metadata:  serverMetadata,
			wantRetry: true,
		},
		{
			// bd doctor's own stores. The residual list used to claim these
			// opens stayed fail-fast; they do not, and this row is what keeps
			// the claim and the code from drifting apart again.
			name:      "CLI diagnostic caller, external workspace",
			open:      newCLIDiagnostic,
			metadata:  serverMetadata,
			wantRetry: true,
		},
		{
			name:     "diagnostic caller, embedded workspace",
			open:     newDiagnostic,
			metadata: embeddedMetadata,
		},
		{
			// A proxied workspace is its own backend: under shared-server
			// mode the storage-mode reader still says "server", and the
			// derivation must not turn that into a retry against the shared
			// endpoint the proxy never dials.
			name:         "diagnostic caller, proxied workspace under shared-server",
			open:         newDiagnostic,
			metadata:     proxiedMetadata,
			sharedServer: true,
		},
		{
			// Positive control for the row above: the same env with a plain
			// server workspace keeps the budget, so the proxied row's zero is
			// about the mode and not about the env.
			name:         "diagnostic caller, server workspace under shared-server",
			open:         newDiagnostic,
			metadata:     serverMetadata,
			sharedServer: true,
			wantRetry:    true,
		},
		{
			name:     "library caller, embedded workspace",
			open:     newFromConfig,
			metadata: embeddedMetadata,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BEADS_TEST_MODE", "1")
			// Hermeticity: this test walks the real resolution chain, which
			// consults a machine-level central config and the mode env vars.
			// A developer machine that happens to point at a shared server
			// would otherwise flip the embedded rows into server mode and the
			// test would pass for the wrong reason.
			t.Setenv("BEADS_CENTRAL_CONFIG", filepath.Join(t.TempDir(), "absent-central-config.json"))
			for _, key := range []string{
				"BEADS_DOLT_SERVER_MODE", "BEADS_DOLT_SHARED_SERVER",
				"BEADS_DOLT_SERVER_HOST", "BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT",
			} {
				t.Setenv(key, "")
			}
			if tc.sharedServer {
				t.Setenv("BEADS_DOLT_SHARED_SERVER", "1")
			}

			// The retry, if one happens, succeeds immediately: this test asks
			// WHETHER the loop was entered, and an exhaustion run would spend
			// a real budget to answer the same question.
			log := stubOpenRetryDials(t, errStubRefused, []error{nil})
			beadsDir := writeBudgetConfig(t, map[string]string{config.OpenRetryBudgetKey: "5s"})
			if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(tc.metadata), 0o644); err != nil {
				t.Fatalf("write metadata.json: %v", err)
			}
			// Positive control on the budget itself, so a zero retry count is
			// about the gate and not about an unreadable key.
			if got := openRetryBudget(beadsDir); got != 5*time.Second {
				t.Fatalf("openRetryBudget = %v, want 5s: the fixture would prove nothing", got)
			}

			tc.open(context.Background(), beadsDir)

			legacy, ctxDials := log.counts()
			if legacy != 1 {
				t.Fatalf("legacy dials = %d, want exactly the one fail-fast probe", legacy)
			}
			if tc.wantRetry {
				if ctxDials == 0 {
					t.Fatal("no retry dial: a server-backed workspace opened through the constructors must keep the budget, and the zero rows prove nothing without this control")
				}
				return
			}
			if ctxDials != 0 {
				t.Fatalf("retry dials = %d, want 0: an open that the store factory would not have routed to the server backend must not wait", ctxDials)
			}
		})
	}
}

// --- circuit breaker accounting -----------------------------------------

// One failed open records exactly one failure, and an open the CALLER ended
// records none: a canceled open is evidence about the caller, not about the
// server, and five of them would otherwise trip the breaker against a healthy
// server.
func TestOpenRetryBreakerAccounting(t *testing.T) {
	for _, tc := range []struct {
		name         string
		budget       string
		cancelAfter  time.Duration
		wantRetries  bool
		wantFailures int
	}{
		{name: "budget off", wantFailures: 1},
		// 3s buys several dials past the 250-750ms first backoff, so this row
		// really distinguishes "one failure per open" from "one per attempt".
		{name: "budget on, exhausted", budget: "3s", wantRetries: true, wantFailures: 1},
		// 4s, not less: the shared backoff's first two delays can sum to
		// ~2.44s in the worst case, and a row that cancels before the
		// SECOND dial cannot distinguish per-open from per-attempt
		// accounting either.
		{name: "budget on, caller cancels", budget: "30s", cancelAfter: 4 * time.Second, wantRetries: true, wantFailures: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The breaker is nil under BEADS_TEST_MODE, so this test has to
			// run without it while keeping its own isolated state dir.
			t.Setenv("BEADS_TEST_MODE", "")
			t.Setenv(testCircuitBreakerDirEnv, filepath.Join(t.TempDir(), "circuit"))
			log := stubOpenRetryDials(t, errStubRefused, []error{errStubRefused})

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

			_, ctxDials := log.counts()
			if tc.wantRetries && ctxDials < 2 {
				t.Fatalf("retry dials = %d, want at least 2: this row cannot tell one failure per OPEN from one per ATTEMPT unless several attempts happened", ctxDials)
			}
			if !tc.wantRetries && ctxDials != 0 {
				t.Fatalf("retry dials = %d, want 0 with the budget off", ctxDials)
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

// A dial that lands after the caller canceled must not be reported as a
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
		t.Fatal("expected a canceled open to fail rather than report success")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled open lost its context cause: %v", err)
	}
	// The error must be the RETRY loop's, not one raised later by the MySQL
	// open. Without this the test passes with the in-loop cancellation guard
	// deleted: the probe would be drained by the shared success path and the
	// SQL open would then fail with context.Canceled anyway, satisfying every
	// other assertion here while the loop reported a canceled dial as a
	// success.
	if !strings.Contains(err.Error(), "open retry canceled after") {
		t.Fatalf("a dial that landed after cancellation was reported as a successful probe; error came from further down instead of the retry loop: %v", err)
	}
	if probe == nil {
		t.Fatal("the retry dial never ran; nothing was disposed of")
	}
	closes, reads, deadlines := probe.stats()
	if closes != 1 {
		t.Fatalf("probe closes = %d, want 1: a probe abandoned after a canceled success leaks the connection", closes)
	}
	if reads == 0 || deadlines == 0 {
		t.Fatalf("probe reads=%d deadlines=%d, want both > 0: the connection was closed BARE instead of drained, which is the RST that crashes dolt sql-server (GH#4132)", reads, deadlines)
	}
}

// An open the caller ended reports that it was ended, however its dial failed.
// newServerMode keeps such an open out of the circuit breaker by asking the
// CONTEXT, so an open canceled while its dial failed non-retryably (an unknown
// host) took that path, but the error it returned wrapped only the dial
// failure: errors.Is could not see the cancellation a caller has to tell apart
// from an outage. Both context errors are checked, on the first probe and on a
// retry dial that fails non-retryably once the context has ended.
func TestOpenRetryCanceledOpenKeepsItsContextCause(t *testing.T) {
	errUnknownHost := errors.New("dial tcp: lookup nowhere.invalid: no such host")
	for _, tc := range []struct {
		name string
		// ctx returns the open's context and the function that ends it.
		ctx        func(t *testing.T) (context.Context, context.CancelFunc)
		firstProbe error
		// cancelInRetryDial ends the context inside the first retry dial,
		// which then fails with errUnknownHost.
		cancelInRetryDial bool
		wantCause         error
		wantRetryDials    int
	}{
		{
			name: "already canceled, first probe fails non-retryably",
			ctx: func(t *testing.T) (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, cancel
			},
			firstProbe: errUnknownHost,
			wantCause:  context.Canceled,
		},
		{
			name: "deadline passed, first probe fails non-retryably",
			ctx: func(t *testing.T) (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			},
			firstProbe: errUnknownHost,
			wantCause:  context.DeadlineExceeded,
		},
		{
			name: "canceled during a retry dial that fails non-retryably",
			ctx: func(t *testing.T) (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
			firstProbe:        errStubRefused,
			cancelInRetryDial: true,
			wantCause:         context.Canceled,
			wantRetryDials:    1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BEADS_TEST_MODE", "1")
			captureOpenRetryNotice(t)
			ctx, cancel := tc.ctx(t)
			t.Cleanup(cancel)
			log := stubOpenRetryDials(t, tc.firstProbe, []error{errUnknownHost})
			if tc.cancelInRetryDial {
				stubbed := openProbeDialContext
				openProbeDialContext = func(dialCtx context.Context, network, addr string, timeout time.Duration) (net.Conn, error) {
					cancel()
					return stubbed(dialCtx, network, addr, timeout)
				}
			}
			cfg := externalOpenConfig(writeBudgetConfig(t, map[string]string{config.OpenRetryBudgetKey: "30s"}))

			_, err := newServerMode(ctx, cfg)
			if err == nil {
				t.Fatal("expected the canceled open to fail")
			}
			legacy, ctxDials := log.counts()
			if legacy != 1 || ctxDials != tc.wantRetryDials {
				t.Fatalf("dials legacy=%d ctx=%d, want 1 and %d", legacy, ctxDials, tc.wantRetryDials)
			}
			assertFirstProbeTimeout(t, log)
			if !errors.Is(err, tc.wantCause) {
				t.Fatalf("canceled open lost its context cause: errors.Is(err, %v) = false for %q", tc.wantCause, err)
			}
			if !errors.Is(err, errUnknownHost) {
				t.Fatalf("canceled open lost the dial failure it reports: %q", err)
			}
			// The canceled return omits the outage advice, as it omits the
			// breaker failure: the caller ended this open, the server did not.
			if strings.Contains(err.Error(), "bd dolt start") {
				t.Fatalf("canceled open took the outage path, advice and breaker failure included: %q", err)
			}
		})
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

// The MERGED half of the same contract. The table above covers the directory
// fallback; this covers precedence, which a directory-only reader would pass
// while silently ignoring BEADS_DIR, the user-level files and every other
// source config.Initialize folds in.
//
// Both keys are given a merged value that CONFLICTS with the directory value,
// and both must return the merged one.
func TestOpenRetryBudgetSiblingScopeParity_MergedConfigWins(t *testing.T) {
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"),
		[]byte("dolt:\n  auto-start: \"false\"\n  open-retry-budget: \"9s\"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Sanity: with nothing merged, both keys come from the directory. This is
	// the positive control that makes the assertions below meaningful.
	if got := configStringForDir(beadsDir, config.OpenRetryBudgetKey); got != "9s" {
		t.Fatalf("directory fallback broken before the merged case ran: got %q", got)
	}

	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	t.Cleanup(config.ResetForTesting)
	config.Set("dolt.auto-start", "true")
	config.Set(config.OpenRetryBudgetKey, "45s")

	if got := configStringForDir(beadsDir, "dolt.auto-start"); got != "true" {
		t.Fatalf("dolt.auto-start = %q, want the merged value \"true\"; the fixture is wrong, not the budget", got)
	}
	if got := configStringForDir(beadsDir, config.OpenRetryBudgetKey); got != "45s" {
		t.Fatalf("%s = %q, want the merged value \"45s\": a directory-only reader ignores every merged source (BEADS_DIR, user-level config) that dolt.auto-start honors", config.OpenRetryBudgetKey, got)
	}
	// Assert the BUDGET READER too, not only the shared helper: a reader that
	// stopped calling configStringForDir and went straight to
	// config.GetStringFromDir would satisfy every assertion above while
	// silently dropping sibling scope.
	if got := openRetryBudget(beadsDir); got != 45*time.Second {
		t.Fatalf("openRetryBudget = %v, want 45s from the merged config (the directory says 9s): the budget reader left the sibling key's scope rules", got)
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

// --- remaining-budget clamp ---------------------------------------------

// The clamp, as a pure function: no clock, no loop, every boundary named.
// Extracted for exactly this reason -- inline it was decided by the loop's own
// timing, so a mutation that removed it stayed green at -count=5 against the
// timed exhaustion test.
func TestClampDialTimeout(t *testing.T) {
	const timeout = 500 * time.Millisecond
	for _, tc := range []struct {
		name      string
		remaining time.Duration
		want      time.Duration
	}{
		{name: "budget outlasts the dial", remaining: 2 * time.Second, want: timeout},
		{name: "exactly one dial left", remaining: timeout, want: timeout},
		{name: "less than a dial left", remaining: 120 * time.Millisecond, want: 120 * time.Millisecond},
		{name: "a nanosecond left", remaining: time.Nanosecond, want: time.Nanosecond},
		// Unreachable from the loop, which breaks on a spent deadline first.
		// Asserted anyway: mapping these to timeout would hand a later caller
		// a full-length dial on an expired budget.
		{name: "budget spent", remaining: 0, want: 0},
		{name: "budget overspent", remaining: -3 * time.Second, want: -3 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampDialTimeout(tc.remaining, timeout); got != tc.want {
				t.Fatalf("clampDialTimeout(%v, %v) = %v, want %v", tc.remaining, timeout, got, tc.want)
			}
		})
	}
}

// The clamp is also WIRED: the loop hands the dialer the clamped value rather
// than the caller's timeout. A pure test cannot see that, and the timed
// exhaustion test could not either, so this drives retryOpenProbe with a
// per-attempt timeout far larger than the whole budget. Every retry then has
// less budget left than one dial would take, which makes the clamp decisive on
// EVERY attempt regardless of how the randomized backoff falls -- the property
// the exhaustion test lacked.
//
// Calling retryOpenProbe directly is deliberate here and proves nothing about
// WHICH opens reach it; the constructor table above owns that question.
func TestOpenRetryClampsThePerAttemptTimeoutToTheBudget(t *testing.T) {
	const (
		budget = time.Second
		// Longer than the budget by two orders of magnitude: unclamped, the
		// recorded timeout is this value and the assertion below cannot pass.
		timeout = time.Minute
	)
	log := stubOpenRetryDials(t, errStubRefused, []error{errStubRefused})

	conn, err := retryOpenProbe(context.Background(), "tcp", "127.0.0.1:1", timeout, budget, errStubRefused)
	if conn != nil {
		t.Fatal("expected no connection from an exhausted budget")
	}
	if err == nil {
		t.Fatal("expected the exhaustion error")
	}

	dials := log.dials()
	if len(dials) == 0 {
		t.Fatal("no retry dial was made, so the clamp was never exercised")
	}
	for i, d := range dials {
		if d.timeout <= 0 {
			t.Fatalf("retry %d dial timeout = %v, want a positive value", i+1, d.timeout)
		}
		if d.timeout > budget {
			t.Fatalf("retry %d dial timeout = %v, larger than the whole %v budget: the loop handed the dialer the raw per-attempt timeout instead of the remaining budget", i+1, d.timeout, budget)
		}
		// The clamp's actual contract is the REMAINING budget, not the whole
		// one. Without this line the test passes on a loop that clamps to the
		// total budget -- every dial would be within the budget and none
		// would be within the DEADLINE, because each starts after a backoff
		// the total-budget form does not subtract.
		if d.offset+d.timeout > budget+50*time.Millisecond {
			t.Fatalf("retry %d starts at %v with a %v timeout, which plans to run past the %v deadline: the loop clamped to the total budget rather than what is left of it", i+1, d.offset, d.timeout, budget)
		}
		// And from below: the clamp hands the dialer what is LEFT, not less.
		// Without this line a loop that shortens every attempt (down to a
		// nanosecond) passes, because the stub ignores the timeout it is given.
		if d.offset+d.timeout < budget-50*time.Millisecond {
			t.Fatalf("retry %d starts at %v with a %v timeout, well short of the %v left of the budget: the loop shortened the attempt below what the clamp allows", i+1, d.offset, d.timeout, budget-d.offset)
		}
	}
}

// TestOpenRetryKeepsThePerAttemptTimeoutWhenTheBudgetHasRoom is the other
// half of the clamp's contract: while more than one per-attempt timeout is
// left of the budget, the dialer gets the FULL per-attempt timeout, not
// less. The test above cannot see a loop that shortens every attempt by a
// constant factor (its per-attempt timeout dwarfs the budget, so every dial
// is clamped anyway); here the budget dwarfs the timeout, so a halved
// timeout is a halved dial.
func TestOpenRetryKeepsThePerAttemptTimeoutWhenTheBudgetHasRoom(t *testing.T) {
	const (
		budget  = 3 * time.Second
		timeout = 200 * time.Millisecond
	)
	log := stubOpenRetryDials(t, errStubRefused, []error{errStubRefused})

	conn, err := retryOpenProbe(context.Background(), "tcp", "127.0.0.1:1", timeout, budget, errStubRefused)
	if conn != nil {
		t.Fatal("expected no connection from an exhausted budget")
	}
	if err == nil {
		t.Fatal("expected the exhaustion error")
	}

	checked := 0
	for i, d := range log.dials() {
		left := budget - d.offset
		if left < timeout+50*time.Millisecond {
			continue // the clamp is decisive here; the sibling test owns this case
		}
		checked++
		// Exact, not approximate: with room to spare the clamp returns the
		// per-attempt timeout unchanged, so any tolerance here is a reduction
		// the loop could apply unnoticed (a 50 ms band let timeout*3/4 pass).
		if d.timeout != timeout {
			t.Fatalf("retry %d dial timeout = %v with %v of the budget left, want exactly the %v per-attempt timeout: the loop shortened an attempt the clamp had room for", i+1, d.timeout, left, timeout)
		}
	}
	if checked == 0 {
		t.Fatal("no retry dial started with more than one per-attempt timeout left, so the full-timeout contract was never exercised")
	}
}

// --- the retry is visible -----------------------------------------------

// captureOpenRetryNotice redirects the notice writer for one test and returns
// what the open printed.
func captureOpenRetryNotice(t *testing.T) *strings.Builder {
	t.Helper()
	var buf strings.Builder
	orig := openRetryNotice
	openRetryNotice = &buf
	t.Cleanup(func() { openRetryNotice = orig })
	return &buf
}

// The loop's per-attempt detail goes to debug.Logf, which is silent unless
// BEADS_DEBUG is set, so a budget-long wait looked like a hang. One stderr
// line makes it legible -- but only when a wait actually happens, and only
// once per open however many attempts it takes.
func TestOpenRetryNoticePrintsOncePerWaitingOpen(t *testing.T) {
	for _, tc := range []struct {
		name       string
		budget     string
		firstProbe error
		wantNotice bool
	}{
		{
			// The default path must stay silent.
			name:       "budget off",
			firstProbe: errStubRefused,
		},
		{
			name:       "budget on, server unreachable",
			budget:     "2s",
			firstProbe: errStubRefused,
			wantNotice: true,
		},
		{
			// Nothing was waited for, so nothing is announced. This is the
			// row that fails if the notice moves to the top of
			// retryOpenProbe, which a budget too small to buy a retry still
			// reaches.
			name:       "budget on, too small to buy a retry",
			budget:     "1ns",
			firstProbe: errStubRefused,
		},
		{
			// The retry loop is never entered at all.
			name:   "budget on, first probe succeeds",
			budget: "30s",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BEADS_TEST_MODE", "1")
			buf := captureOpenRetryNotice(t)
			log := stubOpenRetryDials(t, tc.firstProbe, []error{errStubRefused})

			kv := map[string]string{}
			if tc.budget != "" {
				kv[config.OpenRetryBudgetKey] = tc.budget
			}
			cfg := externalOpenConfig(writeBudgetConfig(t, kv))
			_, _ = newServerMode(context.Background(), cfg)

			got := buf.String()
			if !tc.wantNotice {
				if got != "" {
					t.Fatalf("notice = %q, want nothing: this open never waited", got)
				}
				return
			}
			// Positive control: a row that expects the notice must really
			// have retried, or it would be asserting on the wrong path.
			if _, ctxDials := log.counts(); ctxDials == 0 {
				t.Fatal("no retry dial happened, so this row proves nothing about the notice")
			}
			if n := strings.Count(got, "retrying for up to"); n != 1 {
				t.Fatalf("notice printed %d times, want exactly 1 per open:\n%s", n, got)
			}
			if !strings.Contains(got, cfg.ServerHost) {
				t.Fatalf("notice does not name the address it is waiting on: %q", got)
			}
			if !strings.Contains(got, config.OpenRetryBudgetKey) {
				t.Fatalf("notice does not name the key that set the wait: %q", got)
			}
			if !strings.Contains(got, "2s") {
				t.Fatalf("notice does not name the budget it will wait for: %q", got)
			}
		})
	}
}

// The retry loop names the endpoint twice beyond the first probe: in its
// stderr notice, and in the error a later dial returns, which repeats the
// address the way the first dial's did. That address comes from
// metadata.json, which a cloned repository commits, so both must go through
// the remedy printer exactly as newServerMode's own messages do
// (TestNewServerModeNamesConfiguredEndpointEscaped). The stubbed dial errors
// repeat the address, as a real dial error does, so a raw control character
// in either output can only have come from a missed printer.
//
// The two rows end the open on its two different returns, and each asserts
// the one it took: a spent budget's error, and a canceled open's, which
// newServerMode builds on its own. The cancellation comes from the retry dial
// itself rather than from a timer, so the canceled row cannot pass by
// spending its budget before its deadline.
func TestOpenRetryNamesTheEndpointEscaped(t *testing.T) {
	for _, tc := range []struct {
		name string
		// cancelInRetryDial ends the open's context inside the first retry
		// dial, which then fails with the refusal as every other dial does.
		cancelInRetryDial bool
		wantCanceled      bool
		wantRetryDials    int // 0: any number, as a spent budget allows
		wantText          string
	}{
		{name: "budget exhausted", wantText: "still unreachable after"},
		{name: "open canceled mid-retry", cancelInRetryDial: true, wantCanceled: true, wantRetryDials: 1,
			wantText: "open retry canceled after"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BEADS_TEST_MODE", "1")
			buf := captureOpenRetryNotice(t)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			cfg := externalOpenConfig(writeBudgetConfig(t, map[string]string{config.OpenRetryBudgetKey: "2s"}))
			cfg.ServerHost = hostileEndpoint
			refused := fmt.Errorf("dial tcp %s: connect: connection refused",
				net.JoinHostPort(hostileEndpoint, "1"))
			log := stubOpenRetryDials(t, refused, []error{refused})
			if tc.cancelInRetryDial {
				stubbed := openProbeDialContext
				openProbeDialContext = func(dialCtx context.Context, network, addr string, timeout time.Duration) (net.Conn, error) {
					cancel()
					return stubbed(dialCtx, network, addr, timeout)
				}
			}

			_, err := newServerMode(ctx, cfg)
			if err == nil {
				t.Fatal("open succeeded with no server at the configured endpoint")
			}
			_, ctxDials := log.counts()
			if ctxDials == 0 {
				t.Fatal("no retry dial happened, so this row proves nothing about the retry path")
			}
			if tc.wantRetryDials > 0 && ctxDials != tc.wantRetryDials {
				t.Fatalf("retry dials = %d, want %d: the open must end on the dial that canceled it", ctxDials, tc.wantRetryDials)
			}
			if errors.Is(err, context.Canceled) != tc.wantCanceled {
				t.Fatalf("errors.Is(err, context.Canceled) = %v, want %v: the open did not end on the return this row covers: %q",
					!tc.wantCanceled, tc.wantCanceled, err)
			}
			if !strings.Contains(err.Error(), tc.wantText) {
				t.Fatalf("the open's error is not the one this row covers, which says %q: %q", tc.wantText, err)
			}
			requireNoRawTerminalControl(t, err.Error())
			if !strings.Contains(err.Error(), "connection refused") {
				t.Errorf("the open's error lost the dial failure: %q", err)
			}
			notice := buf.String()
			if !strings.Contains(notice, "retrying for up to") {
				t.Fatalf("no retry notice printed, so this row proves nothing about it: %q", notice)
			}
			requireNoRawTerminalControl(t, notice)
		})
	}
}

// The loop's per-attempt detail goes to debug.Logf, which BD_DEBUG and -v turn
// on, and its three lines -- on entry, after each failed retry, and on the
// retry that reaches the server -- name the endpoint the notice names, two of
// them beside the dial error that repeats it. debug.Logf writes to stderr
// unfiltered, so each line must spell both through the remedy printer itself.
// retryOpenProbe is called directly, with the first error as the dialer wrote
// it, because these lines are its own output; which opens reach it is the
// question the newServerMode tests above answer. The capture swaps os.Stderr
// and debug's verbose flag, so this test must never run in parallel.
func TestOpenRetryDebugLinesNameTheEndpointEscaped(t *testing.T) {
	_ = captureOpenRetryNotice(t)
	addr := net.JoinHostPort(hostileEndpoint, "1")
	refused := fmt.Errorf("dial tcp %s: connect: connection refused", addr)
	stubOpenRetryDials(t, refused, []error{refused, nil})

	var err error
	out := captureRouteLog(t, func() {
		_, err = retryOpenProbe(context.Background(), "tcp", addr, 500*time.Millisecond, 5*time.Second, refused)
	})
	if err != nil {
		t.Fatalf("the retry that reached the server returned an error: %q", err)
	}
	for _, line := range []struct{ name, marker string }{
		{"entry", " unreachable ("},
		{"failed retry", "open attempt 2 for "},
		{"server reached", " reachable after 3 open attempts"},
	} {
		t.Run(line.name, func(t *testing.T) {
			for _, l := range strings.Split(out, "\n") {
				if strings.Contains(l, line.marker) {
					requireNoRawTerminalControl(t, l)
					return
				}
			}
			t.Fatalf("no %s line in the debug output, so this row proves nothing about it:\n%q", line.name, out)
		})
	}
}
