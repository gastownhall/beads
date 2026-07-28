// Regression tests for be-cfm3z: the Dolt benchmark suite used to resolve
// its server port from ambient BEADS_DOLT_SERVER_PORT/BEADS_DOLT_PORT, which
// gc agent shells set to point at a shared production Dolt server. These
// tests exercise the pure decision helper (benchDoltServerPort) directly —
// no server, no network — so they can never themselves reach a real Dolt
// server, production or otherwise, regardless of ambient env state.
package dolt

import "testing"

func TestBenchDoltServerPort_SkipsWithoutOptIn(t *testing.T) {
	t.Setenv("BEADS_BENCH_DOLT_PORT", "")
	// Simulate the stock gc agent shell, which exports this pointing at the
	// shared production server.
	t.Setenv("BEADS_DOLT_SERVER_PORT", "28231")
	t.Setenv("BEADS_DOLT_PORT", "")

	port, skip, reason := benchDoltServerPort()
	if !skip {
		t.Fatalf("expected skip=true when BEADS_BENCH_DOLT_PORT is unset, got skip=false (port=%d)", port)
	}
	if reason == "" {
		t.Fatal("expected a non-empty skip reason")
	}
}

func TestBenchDoltServerPort_UsesExplicitOptIn(t *testing.T) {
	t.Setenv("BEADS_BENCH_DOLT_PORT", "19999")

	port, skip, reason := benchDoltServerPort()
	if skip {
		t.Fatalf("expected skip=false when BEADS_BENCH_DOLT_PORT is set, got reason %q", reason)
	}
	if port != 19999 {
		t.Fatalf("expected port 19999, got %d", port)
	}
}

func TestBenchDoltServerPort_RejectsInvalidOptIn(t *testing.T) {
	t.Setenv("BEADS_BENCH_DOLT_PORT", "not-a-port")

	_, skip, reason := benchDoltServerPort()
	if !skip {
		t.Fatal("expected skip=true for a non-numeric BEADS_BENCH_DOLT_PORT value")
	}
	if reason == "" {
		t.Fatal("expected a non-empty skip reason for an invalid port")
	}
}

func TestBenchDoltServerPort_RejectsZeroOrNegative(t *testing.T) {
	t.Setenv("BEADS_BENCH_DOLT_PORT", "0")

	_, skip, reason := benchDoltServerPort()
	if !skip {
		t.Fatal("expected skip=true for port 0")
	}
	if reason == "" {
		t.Fatal("expected a non-empty skip reason for port 0")
	}
}
