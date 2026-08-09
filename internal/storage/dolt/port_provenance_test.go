package dolt

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/doltserver"
)

// These tests cover the auto-start port-provenance fail-closed behavior
// (GH#4052): when the configured Dolt server port is unreachable and
// auto-start ends up on a different port, bd must fail closed instead of
// silently retargeting when the configured port came from an authoritative
// source (env var, project/global config.yaml, metadata.json). Retargeting
// stays unchanged when the source is bd's own port-file bookkeeping, or when
// no port was ever configured (ServerPort == 0).
//
// stubEnsureRunningDetailed replaces the package-level ensureRunningDetailed
// var so these tests never spawn a real dolt sql-server process.
func stubEnsureRunningDetailed(t *testing.T, port int, startedByUs bool, err error) {
	t.Helper()
	orig := ensureRunningDetailed
	t.Cleanup(func() { ensureRunningDetailed = orig })
	ensureRunningDetailed = func(beadsDir string) (int, bool, error) {
		return port, startedByUs, err
	}
}

// baseAutoStartCfg returns a Config that will reach the auto-start branch of
// newServerMode: the initial dial to ServerPort fails (nothing listens on
// port 1), auto-start is permitted, and BEADS_TEST_MODE disables the real
// circuit breaker so no state files are touched.
func baseAutoStartCfg(t *testing.T, database string, serverPort int, source doltserver.PortSource) *Config {
	t.Helper()
	t.Setenv("BEADS_TEST_MODE", "1")
	return &Config{
		Database:         database,
		Path:             t.TempDir(),
		ServerHost:       "127.0.0.1",
		ServerPort:       serverPort,
		ServerPortSource: source,
		AutoStart:        true,
		DisableAutoStart: false,
	}
}

// TestNewServerMode_AuthoritativePortSource_RetargetedPort_FailsClosed is the
// regression test: an authoritative port source (env var here) plus a
// changed auto-start port must fail closed with an explanatory error, not
// silently retarget. Verified to genuinely discriminate: reverting the
// store.go fail-closed check makes this test fail (see session report).
func TestNewServerMode_AuthoritativePortSource_RetargetedPort_FailsClosed(t *testing.T) {
	const configuredPort = 1 // unreachable: nothing listens on port 1
	const newPort = 54321
	cfg := baseAutoStartCfg(t, "test_port_provenance_env", configuredPort, doltserver.PortSourceEnv)
	stubEnsureRunningDetailed(t, newPort, false, nil)

	_, err := newServerMode(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected fail-closed error when an authoritative port source disagrees with auto-start's port")
	}
	msg := err.Error()
	for _, want := range []string{
		strconv.Itoa(configuredPort),
		strconv.Itoa(newPort),
		"will not silently use",
		"bd dolt start",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q: %s", want, msg)
		}
	}
	if cfg.ServerPort != configuredPort {
		t.Errorf("cfg.ServerPort mutated to %d on fail-closed path, want unchanged %d", cfg.ServerPort, configuredPort)
	}
}

// TestNewServerMode_AllAuthoritativeSources_FailClosed exercises each
// authoritative PortSource, not just env, to guard against a future source
// being added to the authoritative set without updating IsAuthoritative (or
// vice versa).
func TestNewServerMode_AllAuthoritativeSources_FailClosed(t *testing.T) {
	sources := []doltserver.PortSource{
		doltserver.PortSourceEnv,
		doltserver.PortSourceDoltConfigYaml,
		doltserver.PortSourceConfigYaml,
		doltserver.PortSourceMetadataJSON,
	}
	for _, src := range sources {
		src := src
		t.Run(string(src), func(t *testing.T) {
			cfg := baseAutoStartCfg(t, "test_port_provenance_"+string(src), 1, src)
			stubEnsureRunningDetailed(t, 54322, false, nil)

			_, err := newServerMode(context.Background(), cfg)
			if err == nil {
				t.Fatalf("expected fail-closed error for authoritative source %q", src)
			}
			if !strings.Contains(err.Error(), "will not silently use") {
				t.Errorf("source %q: expected fail-closed message, got: %v", src, err)
			}
		})
	}
}

// TestNewServerMode_PortFileSource_RetargetedPort_StillRetargets guards
// against the UX regression: bd's own port-file bookkeeping must keep
// today's silent-retarget-with-warning behavior. Since the retargeted port
// (54323) is not a real server either, the open still ultimately fails, but
// via the old "auto-started but still unreachable" path, not the new
// fail-closed error — proving cfg.ServerPort was updated and a connection
// was attempted on the new port.
func TestNewServerMode_PortFileSource_RetargetedPort_StillRetargets(t *testing.T) {
	const configuredPort = 1
	const newPort = 54323
	cfg := baseAutoStartCfg(t, "test_port_provenance_portfile", configuredPort, doltserver.PortSourcePortFile)
	stubEnsureRunningDetailed(t, newPort, false, nil)

	_, err := newServerMode(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected a connection error (no real server listening on the retargeted port)")
	}
	if strings.Contains(err.Error(), "will not silently use") {
		t.Fatalf("port-file source must not trigger the fail-closed path, got: %v", err)
	}
	if !strings.Contains(err.Error(), "auto-started but still unreachable") {
		t.Fatalf("expected the existing auto-start-unreachable error, got: %v", err)
	}
	if cfg.ServerPort != newPort {
		t.Errorf("cfg.ServerPort = %d, want retargeted to %d", cfg.ServerPort, newPort)
	}
}

// TestNewServerMode_UnsetPortSource_RetargetedPort_StillRetargets guards the
// same UX-preservation case for a Config built without ServerPortSource set
// at all (zero value == PortSourceUnset), matching any caller that
// constructs Config directly rather than through applyConfigDefaults.
func TestNewServerMode_UnsetPortSource_RetargetedPort_StillRetargets(t *testing.T) {
	const configuredPort = 1
	const newPort = 54324
	cfg := baseAutoStartCfg(t, "test_port_provenance_unset", configuredPort, doltserver.PortSourceUnset)
	stubEnsureRunningDetailed(t, newPort, false, nil)

	_, err := newServerMode(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected a connection error (no real server listening on the retargeted port)")
	}
	if strings.Contains(err.Error(), "will not silently use") {
		t.Fatalf("unset port source must not trigger the fail-closed path, got: %v", err)
	}
	if cfg.ServerPort != newPort {
		t.Errorf("cfg.ServerPort = %d, want retargeted to %d", cfg.ServerPort, newPort)
	}
}

// TestNewServerMode_ZeroServerPort_Unaffected verifies cfg.ServerPort == 0
// (never resolved) is unaffected by the provenance check regardless of
// source: auto-start always adopts the ephemeral port it allocated.
func TestNewServerMode_ZeroServerPort_Unaffected(t *testing.T) {
	const newPort = 54325
	cfg := baseAutoStartCfg(t, "test_port_provenance_zero", 0, doltserver.PortSourceEnv)
	stubEnsureRunningDetailed(t, newPort, false, nil)

	_, err := newServerMode(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected a connection error (no real server listening on the allocated port)")
	}
	if strings.Contains(err.Error(), "will not silently use") {
		t.Fatalf("cfg.ServerPort == 0 must never trigger the fail-closed path, got: %v", err)
	}
	if cfg.ServerPort != newPort {
		t.Errorf("cfg.ServerPort = %d, want adopted ephemeral port %d", cfg.ServerPort, newPort)
	}
}

// --- Shared-server mode (GH#4052 primary scenario) ---
//
// In shared-server mode, DefaultConfig resolves the port from the *shared*
// server directory's port sources (or falls back to DefaultSharedServerPort
// when none resolve one), but EnsureRunningDetailed(resolvedBeadsDir) in
// newServerMode's auto-start branch always starts a *repo-local* server.
// That is a different database than the shared one, so a port change here
// must fail closed regardless of ServerPortSource — including
// PortSourcePortFile, which is authoritative-false and would otherwise
// silently retarget (this is exactly the scenario the title of GH#4052
// describes: "write commands report success when Dolt is unreachable").

// baseAutoStartCfgShared is baseAutoStartCfg plus ServerPortSharedServer set,
// modeling a Config built by applyConfigDefaults in shared-server mode.
func baseAutoStartCfgShared(t *testing.T, database string, serverPort int, source doltserver.PortSource) *Config {
	t.Helper()
	cfg := baseAutoStartCfg(t, database, serverPort, source)
	cfg.ServerPortSharedServer = true
	return cfg
}

// TestNewServerMode_SharedServerResolvedPort_PortFileSource_FailsClosed is
// the regression test for the gap identified in review: a shared-server-mode
// port sourced from the (shared) port file — non-authoritative by
// PortSource.IsAuthoritative() alone — must still fail closed, because
// auto-start would create a repo-local server, not reconnect to the shared
// one. Reverting the ServerPortSharedServer half of the condition in
// newServerMode makes this test fail (confirmed; see session report).
func TestNewServerMode_SharedServerResolvedPort_PortFileSource_FailsClosed(t *testing.T) {
	const configuredPort = 1
	const newPort = 54326
	cfg := baseAutoStartCfgShared(t, "test_port_provenance_shared_portfile", configuredPort, doltserver.PortSourcePortFile)
	stubEnsureRunningDetailed(t, newPort, false, nil)

	_, err := newServerMode(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected fail-closed error when a shared-server-resolved port disagrees with auto-start's port")
	}
	msg := err.Error()
	for _, want := range []string{
		strconv.Itoa(configuredPort),
		strconv.Itoa(newPort),
		"Shared Dolt server",
		"different database",
		"bd dolt start",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q: %s", want, msg)
		}
	}
	if cfg.ServerPort != configuredPort {
		t.Errorf("cfg.ServerPort mutated to %d on fail-closed path, want unchanged %d", cfg.ServerPort, configuredPort)
	}
}

// --- Port-file restoration on fail-closed (GH#4052 round 3) ---
//
// EnsureRunningDetailed writes serverDir's port file with the actual
// listening port *before* newServerMode's fail-closed checks run — see
// doltserver.Start's writePortFile, and the adopt-existing-server path's
// EnsurePortFile. Left in place, that write survives a fail-closed return:
// the port file is bd's own bookkeeping and the second-highest-precedence
// port source (above config.yaml/metadata.json, below only the env var), so
// a second, otherwise-identical invocation would resolve PortSourcePortFile
// (non-authoritative) instead of the authoritative source that just failed,
// silently adopt the server this invocation declined to use, and succeed —
// disarming the guard after exactly one invocation. newServerMode must
// restore the port file's pre-call state (including "did not exist before")
// on every fail-closed return.

// stubEnsureRunningDetailedWithPortFileWrite stubs ensureRunningDetailed to
// additionally replicate EnsureRunningDetailed's real disk side effect: it
// writes serverDir's port file with the "new" port before returning, exactly
// as doltserver.Start/EnsurePortFile do ahead of newServerMode's fail-closed
// checks. Without this, the plain stubEnsureRunningDetailed helper leaves no
// on-disk state for the restoration tests below to exercise.
func stubEnsureRunningDetailedWithPortFileWrite(t *testing.T, port int, startedByUs bool) {
	t.Helper()
	orig := ensureRunningDetailed
	t.Cleanup(func() { ensureRunningDetailed = orig })
	ensureRunningDetailed = func(beadsDir string) (int, bool, error) {
		serverDir := doltserver.ResolveServerDir(beadsDir)
		portFile := filepath.Join(serverDir, doltserver.PortFileName)
		if err := os.WriteFile(portFile, []byte(strconv.Itoa(port)), 0o600); err != nil {
			t.Fatalf("simulating production port-file write: %v", err)
		}
		return port, startedByUs, nil
	}
}

// TestNewServerMode_AuthoritativeSource_FailClosed_RestoresAbsentPortFile is
// the "did not exist before" half of the restoration regression test: no
// port file existed prior to the call, so the fail-closed return must leave
// none behind either.
func TestNewServerMode_AuthoritativeSource_FailClosed_RestoresAbsentPortFile(t *testing.T) {
	beadsDir := t.TempDir()
	const configuredPort = 1
	const newPort = 54401
	t.Setenv("BEADS_TEST_MODE", "1")
	cfg := &Config{
		Database:         "test_port_provenance_restore_absent",
		BeadsDir:         beadsDir,
		Path:             filepath.Join(beadsDir, "dolt"),
		ServerHost:       "127.0.0.1",
		ServerPort:       configuredPort,
		ServerPortSource: doltserver.PortSourceConfigYaml,
		AutoStart:        true,
	}
	stubEnsureRunningDetailedWithPortFileWrite(t, newPort, false)

	if got := doltserver.ReadPortFile(beadsDir); got != 0 {
		t.Fatalf("precondition: port file unexpectedly present (%d)", got)
	}

	_, err := newServerMode(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected fail-closed error")
	}
	if !strings.Contains(err.Error(), "will not silently use") {
		t.Fatalf("expected fail-closed message, got: %v", err)
	}
	if got := doltserver.ReadPortFile(beadsDir); got != 0 {
		t.Fatalf("port file not restored to absent: got port %d (fail-closed left auto-start's port-file write in place — this re-disarms the guard on a retry)", got)
	}
}

// TestNewServerMode_AuthoritativeSource_FailClosed_RestoresPriorPortFileBytes
// is the "prior bookkeeping existed" half: a stale ephemeral port from a
// previous run is already recorded; the fail-closed return must restore
// those exact bytes, not the new port auto-start just wrote.
func TestNewServerMode_AuthoritativeSource_FailClosed_RestoresPriorPortFileBytes(t *testing.T) {
	beadsDir := t.TempDir()
	const configuredPort = 1
	const priorPort = 9999
	const newPort = 54402
	t.Setenv("BEADS_TEST_MODE", "1")
	portFilePath := filepath.Join(beadsDir, doltserver.PortFileName)
	priorBytes := []byte(strconv.Itoa(priorPort))
	if err := os.WriteFile(portFilePath, priorBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{
		Database:         "test_port_provenance_restore_bytes",
		BeadsDir:         beadsDir,
		Path:             filepath.Join(beadsDir, "dolt"),
		ServerHost:       "127.0.0.1",
		ServerPort:       configuredPort,
		ServerPortSource: doltserver.PortSourceConfigYaml,
		AutoStart:        true,
	}
	stubEnsureRunningDetailedWithPortFileWrite(t, newPort, false)

	_, err := newServerMode(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected fail-closed error")
	}

	gotBytes, readErr := os.ReadFile(portFilePath)
	if readErr != nil {
		t.Fatalf("port file missing after fail-closed return: %v", readErr)
	}
	if string(gotBytes) != string(priorBytes) {
		t.Fatalf("port file not byte-identical after restore: got %q, want %q", gotBytes, priorBytes)
	}
}

// TestNewServerMode_SharedServer_FailClosed_RestoresPortFile is the same
// byte-identical-restoration assertion for the shared-server-mode fail-closed
// path (ServerPortSharedServer true).
func TestNewServerMode_SharedServer_FailClosed_RestoresPortFile(t *testing.T) {
	beadsDir := t.TempDir()
	const configuredPort = 1
	const priorPort = 8888
	const newPort = 54407
	t.Setenv("BEADS_TEST_MODE", "1")
	portFilePath := filepath.Join(beadsDir, doltserver.PortFileName)
	priorBytes := []byte(strconv.Itoa(priorPort))
	if err := os.WriteFile(portFilePath, priorBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{
		Database:               "test_port_provenance_shared_restore",
		BeadsDir:               beadsDir,
		Path:                   filepath.Join(beadsDir, "dolt"),
		ServerHost:             "127.0.0.1",
		ServerPort:             configuredPort,
		ServerPortSource:       doltserver.PortSourcePortFile,
		ServerPortSharedServer: true,
		AutoStart:              true,
	}
	stubEnsureRunningDetailedWithPortFileWrite(t, newPort, false)

	_, err := newServerMode(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected fail-closed error")
	}
	if !strings.Contains(err.Error(), "Shared Dolt server") {
		t.Fatalf("expected shared-server fail-closed message, got: %v", err)
	}

	gotBytes, readErr := os.ReadFile(portFilePath)
	if readErr != nil {
		t.Fatalf("port file missing after fail-closed return: %v", readErr)
	}
	if string(gotBytes) != string(priorBytes) {
		t.Fatalf("port file not byte-identical after restore: got %q, want %q", gotBytes, priorBytes)
	}
}

// TestNewServerMode_AuthoritativeSource_SecondCallStillFailsClosed is the
// direct regression test for the user-visible bug: a SECOND, independent
// invocation with freshly-resolved config (as a retrying user running
// `bd close` again would produce) must still fail closed. It resolves the
// port source via the real doltserver.DefaultConfig/config.yaml precedence
// chain (not a hand-set field) so the port file's on-disk state genuinely
// participates in resolution, exactly like a real second `bd` invocation
// would. Before the fix, the first call's stray port-file write flipped the
// second call's resolved source to PortSourcePortFile (non-authoritative),
// silently disarming the guard.
func TestNewServerMode_AuthoritativeSource_SecondCallStillFailsClosed(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "0")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")

	beadsDir := t.TempDir()
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const configuredPort = 1 // unreachable: nothing listens on port 1
	body := []byte(fmt.Sprintf("listener:\n  host: 127.0.0.1\n  port: %d\n", configuredPort))
	if err := os.WriteFile(filepath.Join(doltDir, "config.yaml"), body, 0o600); err != nil {
		t.Fatal(err)
	}

	buildCfg := func(t *testing.T, database string) *Config {
		t.Helper()
		dc := doltserver.DefaultConfig(beadsDir)
		if dc.Port != configuredPort {
			t.Fatalf("precondition: DefaultConfig resolved port %d, want %d (from dolt config.yaml)", dc.Port, configuredPort)
		}
		if dc.PortSource != doltserver.PortSourceDoltConfigYaml {
			t.Fatalf("precondition: DefaultConfig resolved source %q, want %q — the port file left over from a prior call is shadowing the authoritative source", dc.PortSource, doltserver.PortSourceDoltConfigYaml)
		}
		return &Config{
			// Not a test-mode database name: this test deliberately runs
			// with BEADS_TEST_MODE=0 to exercise the untracked (non-test)
			// auto-start-stop path in newServerMode/undoRejectedAutoStart.
			Database:               database,
			BeadsDir:               beadsDir,
			Path:                   doltDir,
			ServerHost:             dc.Host,
			ServerPort:             dc.Port,
			ServerPortSource:       dc.PortSource,
			ServerPortSharedServer: dc.PortSharedServer,
			AutoStart:              true,
		}
	}

	// First invocation: auto-start ends up on a different port; must fail closed.
	stubEnsureRunningDetailedWithPortFileWrite(t, 54403, true)
	_, err := newServerMode(context.Background(), buildCfg(t, "prod_second_call_1"))
	if err == nil {
		t.Fatal("expected fail-closed error on first invocation")
	}
	if !strings.Contains(err.Error(), "will not silently use") {
		t.Fatalf("expected fail-closed message on first call, got: %v", err)
	}

	// Second, independent invocation with freshly-resolved config: must
	// STILL fail closed. This is the actual user-visible regression
	// (GH#4052 round 3) — the guard must not disarm itself after one call.
	stubEnsureRunningDetailedWithPortFileWrite(t, 54404, true)
	_, err = newServerMode(context.Background(), buildCfg(t, "prod_second_call_2"))
	if err == nil {
		t.Fatal("expected fail-closed error on second invocation — the port-file restore/stop is missing or broken")
	}
	if !strings.Contains(err.Error(), "will not silently use") {
		t.Fatalf("expected fail-closed message on second call, got: %v", err)
	}
}

// --- undoRejectedAutoStart: server-stop behavior ---
//
// startedByUs distinguishes "we spawned this server speculatively and are
// now declining to use it" (must stop it) from "we adopted an
// already-running server" (must leave it alone — we didn't start it, and
// something else may still be using it).

// stubStopRejectedAutoStartedServer replaces the package-level
// stopRejectedAutoStartedServer var with a call-counting stub, so these
// tests never attempt to signal a real process.
func stubStopRejectedAutoStartedServer(t *testing.T) *int {
	t.Helper()
	calls := new(int)
	orig := stopRejectedAutoStartedServer
	t.Cleanup(func() { stopRejectedAutoStartedServer = orig })
	stopRejectedAutoStartedServer = func(beadsDir string) error {
		*calls++
		return nil
	}
	return calls
}

func TestUndoRejectedAutoStart_StartedByUs_StopsServer(t *testing.T) {
	calls := stubStopRejectedAutoStartedServer(t)
	serverDir := t.TempDir()

	undoRejectedAutoStart(serverDir, true /* startedByUs */, "" /* autoStartedDir: untracked */, doltserver.PortFileSnapshot{}, nil)

	if *calls != 1 {
		t.Fatalf("stopRejectedAutoStartedServer called %d times, want 1 (we spawned the server and declined to use it)", *calls)
	}
}

func TestUndoRejectedAutoStart_AdoptedServer_NotStopped(t *testing.T) {
	calls := stubStopRejectedAutoStartedServer(t)
	serverDir := t.TempDir()

	undoRejectedAutoStart(serverDir, false /* startedByUs: adopted pre-existing */, "", doltserver.PortFileSnapshot{}, nil)

	if *calls != 0 {
		t.Fatalf("stopRejectedAutoStartedServer called %d times, want 0 (an adopted pre-existing server must not be stopped)", *calls)
	}
}

// TestUndoRejectedAutoStart_PortFileSnapshotError_SkipsRestore verifies that
// a failed snapshot (snapErr != nil) does not trigger a restore attempt — a
// zero-value snapshot in that case does not mean "file did not exist", so
// blindly restoring it could wrongly delete a port file that was never
// actually read.
func TestUndoRejectedAutoStart_PortFileSnapshotError_SkipsRestore(t *testing.T) {
	stubStopRejectedAutoStartedServer(t)
	beadsDir := t.TempDir()
	portFilePath := filepath.Join(beadsDir, doltserver.PortFileName)
	if err := os.WriteFile(portFilePath, []byte("54408"), 0o600); err != nil {
		t.Fatal(err)
	}

	undoRejectedAutoStart(beadsDir, true, "", doltserver.PortFileSnapshot{}, fmt.Errorf("simulated snapshot read failure"))

	if _, err := os.Stat(portFilePath); err != nil {
		t.Fatalf("port file should have been left alone after a snapshot error, stat err = %v", err)
	}
}

// TestNewServerMode_SharedServerFixedPortFallback_FailsClosed covers the
// second shared-mode branch in doltserver.DefaultConfig: no port source
// resolved a value, so it fell back to DefaultSharedServerPort
// (PortSourceUnset, ServerPortSharedServer true). That must also fail closed.
func TestNewServerMode_SharedServerFixedPortFallback_FailsClosed(t *testing.T) {
	const configuredPort = 1
	const newPort = 54327
	cfg := baseAutoStartCfgShared(t, "test_port_provenance_shared_fallback", configuredPort, doltserver.PortSourceUnset)
	stubEnsureRunningDetailed(t, newPort, false, nil)

	_, err := newServerMode(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected fail-closed error for the shared-mode fixed-port fallback case")
	}
	if !strings.Contains(err.Error(), "Shared Dolt server") {
		t.Errorf("expected shared-server fail-closed message, got: %v", err)
	}
	if cfg.ServerPort != configuredPort {
		t.Errorf("cfg.ServerPort mutated to %d on fail-closed path, want unchanged %d", cfg.ServerPort, configuredPort)
	}
}
