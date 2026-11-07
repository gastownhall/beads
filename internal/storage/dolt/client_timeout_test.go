package dolt

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// Pins the discriminator from ga-2xwhcz: the MySQL driver reports a client
// deadline kill and a genuinely dead server with the SAME string ("invalid
// connection"), so only elapsed-vs-deadline separates them. During the
// 2026-08-31 stalls that ambiguity sent responders at connectivity while the
// real fault was a slow server.
func TestLooksLikeClientReadTimeout(t *testing.T) {
	invalid := errors.New("invalid connection")
	badConn := errors.New("driver: bad connection")
	other := errors.New("Error 1049 (HY000): database not found: nope")

	cases := []struct {
		name     string
		err      error
		elapsed  time.Duration
		deadline time.Duration
		want     bool
	}{
		{"deadline kill at the deadline", invalid, defaultPoolReadTimeout, defaultPoolReadTimeout, true},
		{"deadline kill just past it", invalid, defaultPoolReadTimeout + time.Second, defaultPoolReadTimeout, true},
		{"bad-connection form counts too", badConn, defaultPoolReadTimeout, defaultPoolReadTimeout, true},
		// A server that vanishes does not wait for our deadline to expire.
		{"fast failure is a real connection loss", invalid, 200 * time.Millisecond, defaultPoolReadTimeout, false},
		{"fast bad-connection is a real loss", badConn, time.Second, defaultPoolReadTimeout, false},
		// A slow non-connection error is slow, but it is not this class.
		{"unrelated error, even when slow", other, 30 * time.Second, defaultPoolReadTimeout, false},
		{"nil is never a timeout", nil, time.Minute, defaultPoolReadTimeout, false},
		// The deadline is the knob's value, not a hard-coded 10s: on a rig at
		// 60s a 15s failure is NOT a deadline kill, and the old constant
		// claimed it was (lens C3).
		{"raised knob: below the real deadline is not a kill", invalid, 15 * time.Second, time.Minute, false},
		{"raised knob: at the real deadline is a kill", invalid, time.Minute, time.Minute, true},
		// No read deadline on the connection means no deadline kill is possible.
		{"no deadline configured", invalid, time.Hour, 0, false},
	}
	for _, tc := range cases {
		if got := looksLikeClientReadTimeout(tc.err, tc.elapsed, tc.deadline); got != tc.want {
			t.Errorf("%s: looksLikeClientReadTimeout(%v, %v, %v) = %v, want %v",
				tc.name, tc.err, tc.elapsed, tc.deadline, got, tc.want)
		}
	}
}

func TestExplainClientReadTimeoutNamesTheRealCause(t *testing.T) {
	orig := errors.New("invalid connection")
	got := explainClientReadTimeout(orig, defaultPoolReadTimeout+250*time.Millisecond, defaultPoolReadTimeout, "schema init")
	if got == nil {
		t.Fatal("explainClientReadTimeout returned nil for a deadline kill")
	}
	msg := got.Error()
	for _, want := range []string{"schema init", "server is SLOW", "not the connection broken"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q missing %q — it must not blame the transport", msg, want)
		}
	}
	// The original must stay unwrapped-reachable so existing classification
	// (isRetryableError and callers matching on the driver string) is unchanged.
	if !errors.Is(got, orig) {
		t.Error("wrapped error lost the original; retry classification would change")
	}
	if !isRetryableError(got) {
		t.Error("wrapping must NOT alter retry classification — that is a separate, measured decision")
	}
}

func TestExplainClientReadTimeoutPassesThroughOtherErrors(t *testing.T) {
	orig := errors.New("Error 1049 (HY000): database not found: nope")
	if got := explainClientReadTimeout(orig, time.Minute, defaultPoolReadTimeout, "schema init"); got != orig {
		t.Errorf("unrelated error was rewritten: %v", got)
	}
	if got := explainClientReadTimeout(nil, time.Minute, defaultPoolReadTimeout, "schema init"); got != nil {
		t.Errorf("nil error was rewritten: %v", got)
	}
	// A fast connection loss is a REAL transport failure and must keep saying so.
	fast := errors.New("invalid connection")
	if got := explainClientReadTimeout(fast, 100*time.Millisecond, defaultPoolReadTimeout, "schema init"); got != fast {
		t.Errorf("fast connection loss was rewritten as a timeout: %v", got)
	}
	// A connection with no read deadline (openMigrationDB, embedded mode) can
	// never suffer a deadline kill, however long the call took.
	if got := explainClientReadTimeout(fast, time.Hour, 0, "schema init"); got != fast {
		t.Errorf("deadline-less connection was blamed on a deadline: %v", got)
	}
}

// The DSN and the classifier must read one value; drift between them would make
// the discriminator silently wrong. buildServerDSN writes the deadline and
// DoltStore.poolReadDeadline reads it back, so every input to the deadline —
// the knob (dolt.pool-read-timeout / BEADS_DOLT_POOL_READ_TIMEOUT, be-4at) and
// the per-command PoolReadTimeoutFallback (wy-sbgucn) — reaches both without
// the classifier having to re-derive any of that precedence. A second
// hard-coded constant is what blamed a rig running at 60s on "the 10s client
// read deadline" — the real value at 1/6 scale.
func TestPoolReadTimeoutIsTheDSNValue(t *testing.T) {
	cases := []struct {
		name     string
		knob     time.Duration
		fallback time.Duration
		wantDSN  string
		want     time.Duration
	}{
		{"unset knob keeps the built-in default", 0, 0, "readTimeout=10s", defaultPoolReadTimeout},
		{"knob at 60s reaches the DSN and the blame text", time.Minute, 0, "readTimeout=1m0s", time.Minute},
		// PoolReadTimeoutFallback replaces the built-in default when nothing
		// set the knob, so the classifier must name IT and not 10s.
		{"fallback replaces the built-in default", 0, 5 * time.Minute, "readTimeout=5m0s", 5 * time.Minute},
		// An explicit knob still wins over the fallback, in the DSN and
		// therefore in the blame text too.
		{"explicit knob beats the fallback", time.Minute, 5 * time.Minute, "readTimeout=1m0s", time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				ServerHost:              "127.0.0.1",
				ServerPort:              51361,
				ServerUser:              "root",
				PoolReadTimeout:         tc.knob,
				PoolReadTimeoutFallback: tc.fallback,
			}
			dsn := buildServerDSN(cfg, "hq")
			if !strings.Contains(dsn, tc.wantDSN) {
				t.Fatalf("DSN %q does not carry %s", dsn, tc.wantDSN)
			}
			s := &DoltStore{connStr: dsn}
			if got := s.poolReadDeadline(); got != tc.want {
				t.Fatalf("poolReadDeadline() = %v, want %v — classifier and DSN have drifted", got, tc.want)
			}
			// The blame text must name the deadline the DSN was built with.
			msg := explainClientReadTimeout(
				errors.New("invalid connection"), tc.want+time.Second, s.poolReadDeadline(), "schema init").Error()
			if !strings.Contains(msg, tc.want.String()) {
				t.Errorf("blame text %q does not name the %s deadline it was built with", msg, tc.want)
			}
			if tc.want != defaultPoolReadTimeout && strings.Contains(msg, defaultPoolReadTimeout.String()) {
				t.Errorf("blame text %q still names the built-in %s instead of the configured %s",
					msg, defaultPoolReadTimeout, tc.want)
			}
		})
	}
}
