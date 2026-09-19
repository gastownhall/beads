package dolt

import (
	"path/filepath"
	"testing"
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
