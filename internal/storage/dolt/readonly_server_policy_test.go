package dolt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestInitializeServerCircuitBreakerHonorsDisableAutoStart(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	originalClean := cleanServerCircuitState
	originalNew := newServerCircuitBreaker
	t.Cleanup(func() {
		cleanServerCircuitState = originalClean
		newServerCircuitBreaker = originalNew
	})

	cleanCalls := 0
	newCalls := 0
	cleanServerCircuitState = func() { cleanCalls++ }
	newServerCircuitBreaker = func(host string, port int, database string) *circuitBreaker {
		newCalls++
		return &circuitBreaker{filePath: filepath.Join(t.TempDir(), "circuit.json")}
	}

	if got := initializeServerCircuitBreaker(&Config{DisableAutoStart: true, ServerPort: 3307}); got != nil {
		t.Fatal("strict read-only (DisableAutoStart) server open created a circuit breaker")
	}
	if cleanCalls != 0 || newCalls != 0 {
		t.Fatalf("strict read-only server open mutated circuit state: clean=%d new=%d", cleanCalls, newCalls)
	}

	if got := initializeServerCircuitBreaker(&Config{ServerPort: 3307}); got == nil {
		t.Fatal("writable server open must retain circuit breaker behavior")
	}
	if cleanCalls != 1 || newCalls != 1 {
		t.Fatalf("writable server open circuit calls: clean=%d new=%d, want 1 each", cleanCalls, newCalls)
	}

	// An ordinary classified-read command sets ReadOnly but not
	// DisableAutoStart, and must retain the writable-open circuit breaker
	// behavior (regression guard for the ReadOnly/DisableAutoStart
	// conflation fixed in this change).
	if got := initializeServerCircuitBreaker(&Config{ReadOnly: true, ServerPort: 3307}); got == nil {
		t.Fatal("ordinary classified-read (ReadOnly without DisableAutoStart) must retain circuit breaker behavior")
	}
	if cleanCalls != 2 || newCalls != 2 {
		t.Fatalf("classified-read server open circuit calls: clean=%d new=%d, want 2 each", cleanCalls, newCalls)
	}
}

// TestServerOpenSweepsCircuitDirOncePerProcess pins the open-time sweep to
// what its doc has always called it: an init-time cleanup. A process opens
// stores more than once — every write command opens a non-mutating preview
// store for the workspace-identity check before its writable one (be-0gfcs),
// and a long-lived embedder opens one per request — and each open re-read
// every breaker file in the directory. Nothing the sweep could find changes
// between two opens of one process, so only the first open sweeps; an explicit
// CleanStaleCircuitBreakerFiles (bd doctor --fix) still sweeps on demand.
func TestServerOpenSweepsCircuitDirOncePerProcess(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	dir := t.TempDir()
	t.Setenv(testCircuitBreakerDirEnv, dir)
	// This test process may already have spent its sweep on an earlier test.
	original := staleCircuitSweep
	staleCircuitSweep = new(sync.Once)
	t.Cleanup(func() { staleCircuitSweep = original })

	staleOpen := func(name string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		data, err := json.Marshal(circuitState{State: circuitOpen, TrippedAt: time.Now().Add(-circuitStaleTTL - time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	exists := func(path string) bool {
		_, err := os.Stat(path)
		return err == nil
	}

	first := staleOpen("beads-dolt-circuit-127-0-0-1-40001-first.json")
	if initializeServerCircuitBreaker(&Config{ServerPort: 3307}) == nil {
		t.Fatal("writable server open must retain circuit breaker behavior")
	}
	if exists(first) {
		t.Fatal("the first server open in the process did not sweep the circuit directory")
	}

	second := staleOpen("beads-dolt-circuit-127-0-0-1-40002-second.json")
	if initializeServerCircuitBreaker(&Config{ServerPort: 3307}) == nil {
		t.Fatal("writable server open must retain circuit breaker behavior")
	}
	if !exists(second) {
		t.Fatal("a second server open in the same process swept the circuit directory again")
	}

	CleanStaleCircuitBreakerFiles()
	if exists(second) {
		t.Fatal("an explicit CleanStaleCircuitBreakerFiles (bd doctor --fix) no longer sweeps")
	}
}

func TestInitializeServerCircuitBreakerSkipsTestMode(t *testing.T) {
	originalClean := cleanServerCircuitState
	originalNew := newServerCircuitBreaker
	t.Cleanup(func() {
		cleanServerCircuitState = originalClean
		newServerCircuitBreaker = originalNew
	})
	t.Setenv("BEADS_TEST_MODE", "1")

	cleanCalls := 0
	newCalls := 0
	cleanServerCircuitState = func() { cleanCalls++ }
	newServerCircuitBreaker = func(string, int, string) *circuitBreaker {
		newCalls++
		return &circuitBreaker{}
	}

	if got := initializeServerCircuitBreaker(&Config{ServerPort: 3307}); got != nil {
		t.Fatal("test-mode server open created a circuit breaker")
	}
	if cleanCalls != 0 || newCalls != 0 {
		t.Fatalf("test-mode server open touched circuit state: clean=%d new=%d", cleanCalls, newCalls)
	}
}

func TestPersistResolvedPortFileHonorsDisableAutoStart(t *testing.T) {
	originalEnsure := ensureResolvedPortFile
	t.Cleanup(func() { ensureResolvedPortFile = originalEnsure })
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_PORT", "")

	calls := 0
	var gotDir string
	var gotPort int
	ensureResolvedPortFile = func(beadsDir string, port int) error {
		calls++
		gotDir, gotPort = beadsDir, port
		return nil
	}

	beadsDir := t.TempDir()
	if err := persistResolvedPortFile(&Config{DisableAutoStart: true, ServerHost: "127.0.0.1", ServerPort: 3307}, beadsDir); err != nil {
		t.Fatalf("strict read-only persist policy: %v", err)
	}
	if calls != 0 {
		t.Fatalf("strict read-only server open repaired port file %d time(s)", calls)
	}

	if err := persistResolvedPortFile(&Config{ServerHost: "127.0.0.1", ServerPort: 3308}, beadsDir); err != nil {
		t.Fatalf("writable persist policy: %v", err)
	}
	if calls != 1 || gotDir != beadsDir || gotPort != 3308 {
		t.Fatalf("writable port persistence = calls:%d dir:%q port:%d", calls, gotDir, gotPort)
	}

	// An ordinary classified-read command sets ReadOnly but not
	// DisableAutoStart, and must retain port-file repair.
	if err := persistResolvedPortFile(&Config{ReadOnly: true, ServerHost: "127.0.0.1", ServerPort: 3309}, beadsDir); err != nil {
		t.Fatalf("classified-read persist policy: %v", err)
	}
	if calls != 2 || gotDir != beadsDir || gotPort != 3309 {
		t.Fatalf("classified-read port persistence = calls:%d dir:%q port:%d", calls, gotDir, gotPort)
	}
}

// TestPersistResolvedPortFileSkipsIPv6Loopback pins that an open of a server
// at the IPv6 loopback, in either spelling, records no port file. The file
// names the port of bd's own server on 127.0.0.1, and doltserver.DefaultConfig
// ignores it for this host; written here, it would name the [::1] server's
// port to whatever next dials 127.0.0.1.
func TestPersistResolvedPortFileSkipsIPv6Loopback(t *testing.T) {
	originalEnsure := ensureResolvedPortFile
	t.Cleanup(func() { ensureResolvedPortFile = originalEnsure })
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_PORT", "")

	calls := 0
	ensureResolvedPortFile = func(string, int) error {
		calls++
		return nil
	}
	for _, host := range []string{"::1", "[::1]"} {
		if err := persistResolvedPortFile(&Config{ServerHost: host, ServerPort: 3307}, t.TempDir()); err != nil {
			t.Fatalf("persist for %q: %v", host, err)
		}
		if calls != 0 {
			t.Fatalf("an open of the server at %q wrote the port file", host)
		}
	}
	if err := persistResolvedPortFile(&Config{ServerHost: "127.0.0.1", ServerPort: 3307}, t.TempDir()); err != nil {
		t.Fatalf("persist for 127.0.0.1: %v", err)
	}
	if calls != 1 {
		t.Fatalf("an open of the server at 127.0.0.1 wrote the port file %d time(s), want once", calls)
	}
}

func TestServerOpenCanAutoStartHonorsDisableAutoStart(t *testing.T) {
	strictReadOnly := &Config{DisableAutoStart: true, AutoStart: true, Path: "/unused", ServerHost: "127.0.0.1"}
	if serverOpenCanAutoStart(strictReadOnly) {
		t.Fatal("strict read-only (DisableAutoStart) server open must never auto-start a server")
	}

	writable := &Config{AutoStart: true, Path: "/unused", ServerHost: "127.0.0.1"}
	if !serverOpenCanAutoStart(writable) {
		t.Fatal("writable server open must retain auto-start behavior")
	}

	// An ordinary classified-read command (bd show, bd list, ...) sets
	// ReadOnly but not DisableAutoStart, and must still be able to
	// auto-start a stopped managed server (regression guard for the
	// ReadOnly/DisableAutoStart conflation; see
	// dolt_autostart_lifecycle_integration_test.go).
	classifiedRead := &Config{ReadOnly: true, AutoStart: true, Path: "/unused", ServerHost: "127.0.0.1"}
	if !serverOpenCanAutoStart(classifiedRead) {
		t.Fatal("ordinary classified-read (ReadOnly without DisableAutoStart) must retain auto-start behavior")
	}
}

func TestDeferWakeSweepEligibleHonorsClassifiedRead(t *testing.T) {
	cases := []struct {
		name           string
		readOnly       bool
		classifiedRead bool
		want           bool
	}{
		{name: "writable open sweeps", readOnly: false, classifiedRead: false, want: true},
		{name: "strict read-only never sweeps", readOnly: true, classifiedRead: false, want: false},
		// An ordinary classified-read command (bd ready, bd list) sets
		// readOnly but the store is genuinely writable underneath, so the
		// defer-wake sweep must still run (regression guard for be-vbhpf:
		// the sweep silently early-returned for every read-only-classified
		// command in server mode, not just strict --readonly/preview).
		{name: "classified-read still sweeps despite readOnly", readOnly: true, classifiedRead: true, want: true},
		// readOnly=false already short-circuits the predicate to true on its
		// own, so this case documents totality (every input combination has
		// an asserted output), not a state any caller actually produces.
		{name: "classifiedRead without readOnly stays eligible", readOnly: false, classifiedRead: true, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deferWakeSweepEligible(tc.readOnly, tc.classifiedRead); got != tc.want {
				t.Fatalf("deferWakeSweepEligible(readOnly=%v, classifiedRead=%v) = %v, want %v", tc.readOnly, tc.classifiedRead, got, tc.want)
			}
		})
	}
}
