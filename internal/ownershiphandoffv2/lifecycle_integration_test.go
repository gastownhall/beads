//go:build integration && !windows

package ownershiphandoffv2

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
)

// These tests drive the verbs against a real `dolt sql-server` standing in for
// the caller's server. The test starts and stops it; bd never does. That split
// is the point of the whole design, so a fixture that let bd start the "legacy"
// server would be testing something else.

// fixture is one workspace with a caller-owned server in front of it.
type fixture struct {
	t          *testing.T
	root       string
	beadsDir   string
	dataDir    string
	database   string
	legacyPort int
	legacy     *exec.Cmd
	stopped    bool
}

// requireDolt is the named wrapper this package's integration boundary goes
// through. It is an environment check, not a testing.Short() skip: repo policy
// reserves testing.Short() for true runtime, stress and large-fixture skips
// (scripts/check-testing-short.sh), and an integration boundary is none of
// those. Same shape as internal/storage/dbproxy/server's requireDolt.
func requireDolt(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("dolt")
	if err != nil {
		t.Skipf("dolt is not on PATH: %v", err)
	}
	return bin
}

// newFixture builds a workspace that looks like one a caller manages directly:
// metadata.json pins dolt_server_port (which is what makes bd's own resolver
// call it externally managed), the data dir holds an initialized database, and
// a dolt sql-server the TEST owns is serving it.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	requireDolt(t)

	root := canonicalTempDir(t)
	f := &fixture{
		t:        t,
		root:     root,
		beadsDir: BeadsDir(root),
		dataDir:  filepath.Join(BeadsDir(root), "dolt"),
		database: "scope_db",
	}
	if err := os.MkdirAll(filepath.Join(f.dataDir, f.database), 0o755); err != nil {
		t.Fatalf("mkdir data dir: %v", err)
	}

	// A private DOLT_ROOT_PATH keeps this test off the box's global dolt
	// config, and gives `dolt init` the identity it refuses to run without —
	// including the bare `dolt init` that bd's own EnsureDoltInit runs.
	doltRoot := filepath.Join(root, "doltroot")
	if err := os.MkdirAll(filepath.Join(doltRoot, ".dolt"), 0o755); err != nil {
		t.Fatalf("mkdir dolt root: %v", err)
	}
	globalCfg := `{"user.name":"bd handoff test","user.email":"handoff@example.invalid"}`
	if err := os.WriteFile(filepath.Join(doltRoot, ".dolt", "config_global.json"), []byte(globalCfg), 0o600); err != nil {
		t.Fatalf("write dolt global config: %v", err)
	}
	t.Setenv("DOLT_ROOT_PATH", doltRoot)
	// These would otherwise redirect the resolvers this test is asserting on.
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_TEST_MODE", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "")

	copyTree(t, schemaTemplate(t), f.dataDir)
	f.legacyPort = f.freePort()
	f.writeMetadata(f.legacyPort)
	f.startLegacy()
	return f
}

func (f *fixture) freePort() int {
	f.t.Helper()
	port, err := freeLoopbackPort("127.0.0.1")
	if err != nil {
		f.t.Fatalf("allocate port: %v", err)
	}
	return port
}

// writeMetadata records the workspace as one whose server someone else runs:
// backend dolt, an explicit dolt_server_port, and the project identity the
// request must match.
func (f *fixture) writeMetadata(serverPort int) {
	f.t.Helper()
	cfg := configfile.DefaultConfig()
	cfg.Backend = configfile.BackendDolt
	cfg.DoltMode = configfile.DoltModeServer
	cfg.DoltServerPort = serverPort
	cfg.ProjectID = "workspace-under-test"
	if err := cfg.Save(f.beadsDir); err != nil {
		f.t.Fatalf("write metadata.json: %v", err)
	}
}

// startLegacy runs the caller's server. cmd.Dir is the data dir so the process
// is bindable to this workspace by working directory as well as by the noms
// LOCK it holds — both bindings §3 can report.
func (f *fixture) startLegacy() {
	f.t.Helper()
	logPath := filepath.Join(f.root, fmt.Sprintf("legacy-%d.log", f.legacyPort))
	log, err := os.Create(logPath) //nolint:gosec // test artifact
	if err != nil {
		f.t.Fatalf("create legacy log: %v", err)
	}
	cmd := exec.Command("dolt", "sql-server",
		"--host", "127.0.0.1", "--port", strconv.Itoa(f.legacyPort), "--data-dir", f.dataDir)
	cmd.Dir = f.dataDir
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		f.t.Fatalf("start legacy server: %v", err)
	}
	f.legacy, f.stopped = cmd, false
	f.t.Cleanup(f.stopLegacy)

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if handshakes("127.0.0.1", f.legacyPort) {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	contents, _ := os.ReadFile(logPath) //nolint:gosec,errcheck // diagnostic only
	f.t.Fatalf("legacy server never greeted on port %d\n%s", f.legacyPort, contents)
}

// stopLegacy is the caller doing its job. bd must never do this, which is why
// it lives on the fixture and not in the package.
func (f *fixture) stopLegacy() {
	f.t.Helper()
	if f.stopped || f.legacy == nil || f.legacy.Process == nil {
		return
	}
	f.stopped = true
	_ = syscall.Kill(-f.legacy.Process.Pid, syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _, _ = f.legacy.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		_ = syscall.Kill(-f.legacy.Process.Pid, syscall.SIGKILL)
		<-done
	}
	// The port lingers briefly after the process exits; gate (d) reads the
	// kernel's view, so wait for it rather than racing it.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, outcome := doltserver.ResolvePortHolderInDir(f.legacyPort, ""); outcome == doltserver.PortHolderNoHolder {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (f *fixture) opts() Options {
	return Options{
		Root:      f.root,
		Database:  f.database,
		Workspace: "workspace-under-test",
		Endpoint:  Endpoint{Host: "127.0.0.1", Port: f.legacyPort},
		BDVersion: "test",
	}
}

func (f *fixture) run(verb string) (Result, error) {
	f.t.Helper()
	return Run(context.Background(), verb, f.opts())
}

// runWith drives a verb with seams applied, for the crash-injection tests.
func (f *fixture) runWith(verb string, adjust func(*Options)) (Result, error) {
	f.t.Helper()
	opts := f.opts()
	adjust(&opts)
	return Run(context.Background(), verb, opts)
}

// mustRun fails the test unless the verb reached its phase.
func (f *fixture) mustRun(verb string) Result {
	f.t.Helper()
	result, err := f.run(verb)
	if err != nil || !result.Reached(verb) {
		f.t.Fatalf("%s did not reach its phase: phase=%s code=%s err=%v",
			verb, result.Phase, result.ErrorCode, err)
	}
	return result
}

// stopTargetIfRunning kills a replacement bd started, so a test that ends
// mid-transfer does not leave one behind.
func (f *fixture) stopTargetIfRunning() {
	f.t.Helper()
	j, existed, err := loadExisting(JournalPath(f.root))
	if err != nil || !existed || j.Target.PID <= 0 {
		return
	}
	_, _ = stopByIdentity(j.Target.PID, j.Target.Birth)
}

// The whole forward track, including the thing that makes commit meaningful:
// metadata.json pins dolt_server_port at prepare, commit clears it, and bd's
// own resolvers then agree the workspace is bd's.
func TestForwardTransferClearsTheServerPortAndResolversAgree(t *testing.T) {
	f := newFixture(t)
	t.Cleanup(f.stopTargetIfRunning)

	if mode := doltserver.ResolveServerMode(f.beadsDir); mode != doltserver.ServerModeExternal {
		t.Fatalf("fixture is not externally managed to begin with: %s", mode)
	}

	prepared := f.mustRun(VerbPrepare)
	if prepared.Phase != PhasePrepared {
		t.Fatalf("prepare left phase %s", prepared.Phase)
	}
	journal := f.journal()
	if journal.Snapshot.MetadataDoltServerPort != f.legacyPort {
		t.Fatalf("snapshot recorded dolt_server_port %d, want %d",
			journal.Snapshot.MetadataDoltServerPort, f.legacyPort)
	}
	if len(journal.Snapshot.CommitWriteSet) != len(commitWriteSetKeys) {
		t.Fatalf("snapshot write set is %v", journal.Snapshot.CommitWriteSet)
	}
	if journal.Snapshot.Sentinels.IssuesState != TablePresent || journal.Snapshot.Sentinels.FirstIssue != "bd-0001" {
		t.Fatalf("sentinels did not capture the seeded scope: %+v", journal.Snapshot.Sentinels)
	}
	// §3: bd resolved the caller's process itself, not from the hint.
	if !journal.LegacyInstance.Resolved {
		t.Fatalf("bd did not resolve the legacy instance: %s", journal.LegacyInstance.Reason)
	}
	if journal.LegacyInstance.BoundBy == "" {
		t.Fatal("legacy instance has no binding evidence")
	}

	f.stopLegacy()
	stopped := f.mustRun(VerbLegacyGone)
	gates := stopped.Evidence[string(PhaseOldOwnerStopped)].Gates
	// Two of the four gates depend on a port-holder lookup, and only the procfs
	// one can tell a free port from a failed lookup. Where it cannot, the
	// contract licenses "unavailable" — and requires that it never be recorded
	// as passed. Assert that distinction rather than demanding four passes
	// everywhere, which would be asserting a platform rather than the design.
	for _, gate := range []string{"endpoint_quiet", "data_dir_unlocked"} {
		if got := gates[gate]; got != GatePassed {
			t.Errorf("gate %s is %q, want %q", gate, got, GatePassed)
		}
	}
	for _, gate := range []string{"legacy_instance_gone", "port_released"} {
		got := gates[gate]
		if doltserver.PortHolderSource() == "proc" {
			if got != GatePassed {
				t.Errorf("gate %s is %q, want %q where the procfs lookup is available", gate, got, GatePassed)
			}
			continue
		}
		if got != GatePassed && got != GateUnavailable {
			t.Errorf("gate %s is %q; off procfs it may only pass or be unavailable", gate, got)
		}
	}

	f.mustRun(VerbConfigure)
	target := f.journal().Target
	if target.PID <= 0 || target.Birth == "" || len(target.LaunchID) != 32 {
		t.Fatalf("configure did not record a strict identity: %+v", target)
	}
	if target.Port == f.legacyPort {
		t.Fatal("the replacement reused the caller's port instead of a fresh one")
	}

	f.mustRun(VerbVerify)
	f.mustRun(VerbCommit)

	// The post-write assertion already checked these; assert them here too, so
	// a regression that weakened the assertion cannot pass silently.
	if mode := doltserver.ResolveServerMode(f.beadsDir); mode != doltserver.ServerModeOwned {
		t.Errorf("after commit ResolveServerMode is %s, want owned", mode)
	}
	if !doltserver.ManagesLiveServerOnPort(f.beadsDir, target.Port) {
		t.Errorf("after commit bd does not manage the live server on port %d", target.Port)
	}
	if !doltserver.ResolveAutoStartForDir(f.beadsDir) {
		t.Error("after commit auto-start does not resolve true")
	}
	cfg, err := configfile.Load(f.beadsDir)
	if err != nil {
		t.Fatalf("reload metadata.json: %v", err)
	}
	if cfg.DoltServerPort != 0 {
		t.Errorf("commit left dolt_server_port %d; the resolver will keep calling this external", cfg.DoltServerPort)
	}
	if cfg.DoltMode != configfile.DoltModeServer {
		t.Errorf("commit left dolt_mode %q", cfg.DoltMode)
	}
	if err := CheckNormalOpen(f.beadsDir); err != nil {
		t.Errorf("a committed journal still fences ordinary opens: %v", err)
	}

	// Replaying a committed transfer is a no-op that says so, and exits zero.
	replay, err := f.run(VerbCommit)
	if ErrorCode(err) != CodeAlreadyCommitted {
		t.Fatalf("replayed commit returned %q, want %q", ErrorCode(err), CodeAlreadyCommitted)
	}
	if !replay.Reached(VerbCommit) {
		t.Fatal("replayed commit does not report the phase as reached")
	}
}

func (f *fixture) journal() Journal {
	f.t.Helper()
	j, err := Load(JournalPath(f.root))
	if err != nil {
		f.t.Fatalf("load journal: %v", err)
	}
	return j
}

// legacy-gone against a running server refuses, and mutates nothing: the whole
// point is that the caller can stop its server and run the verb again.
func TestLegacyStillAliveRefusesWithoutMutating(t *testing.T) {
	f := newFixture(t)
	f.mustRun(VerbPrepare)
	before := f.workspaceFingerprint()

	result, err := f.run(VerbLegacyGone)
	if ErrorCode(err) != CodeLegacyAlive {
		t.Fatalf("legacy-gone returned %q, want %q (err: %v)", ErrorCode(err), CodeLegacyAlive, err)
	}
	if result.Phase != PhasePrepared {
		t.Fatalf("a refusal advanced the phase to %s", result.Phase)
	}
	if result.Mutates {
		t.Fatal("a refusal reported a mutation")
	}
	if after := f.workspaceFingerprint(); after != before {
		t.Fatalf("legacy-gone changed the workspace:\nbefore %s\nafter  %s", before, after)
	}
}

// A foreign listener holding the port is the case gate (a) alone cannot catch:
// it never speaks MySQL, so the endpoint looks quiet. Gate (d) is what refuses.
func TestForeignListenerOnLegacyPortRefuses(t *testing.T) {
	f := newFixture(t)
	f.mustRun(VerbPrepare)
	f.stopLegacy()

	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(f.legacyPort)))
	if err != nil {
		t.Skipf("could not re-bind the legacy port to model a foreign listener: %v", err)
	}
	defer listener.Close() //nolint:errcheck // test cleanup

	result, err := f.run(VerbLegacyGone)
	if ErrorCode(err) != CodeLegacyAlive {
		t.Fatalf("legacy-gone returned %q, want %q (err: %v)", ErrorCode(err), CodeLegacyAlive, err)
	}
	if result.Phase != PhasePrepared {
		t.Fatalf("a refusal advanced the phase to %s", result.Phase)
	}
	gates := result.Evidence[string(PhasePrepared)+":attempt"].Gates
	if gates["endpoint_quiet"] != GatePassed {
		t.Errorf("the endpoint gate did not pass; this test is not exercising gate (d)")
	}
	if gates["port_released"] == GatePassed {
		t.Error("gate (d) passed with a foreign listener on the port")
	}
}

// bd must not take over a scope it already owns, and must not mistake its own
// server for the caller's.
func TestBDServerPresentRefusesPrepare(t *testing.T) {
	f := newFixture(t)
	// A live bd record for this root: our own pid, and the port bd would be
	// serving on. ManagesLiveServerOnPort needs both files to agree.
	if err := os.WriteFile(filepath.Join(f.beadsDir, doltserver.PIDFileName),
		[]byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatalf("write pid file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(f.beadsDir, doltserver.PortFileName),
		[]byte(strconv.Itoa(f.legacyPort)), 0o600); err != nil {
		t.Fatalf("write port file: %v", err)
	}

	result, err := f.run(VerbPrepare)
	if ErrorCode(err) != CodeBDServerPresent {
		t.Fatalf("prepare returned %q, want %q (err: %v)", ErrorCode(err), CodeBDServerPresent, err)
	}
	if result.Phase == PhasePrepared {
		t.Fatal("prepare advanced despite a live bd server record")
	}
}

// workspaceFingerprint is every artifact the transfer can touch, as one
// comparable string. A refusal that changes any of them has mutated the
// caller's scope.
func (f *fixture) workspaceFingerprint() string {
	f.t.Helper()
	var parts []string
	for _, name := range []string{
		configfile.ConfigFileName, "config.yaml",
		doltserver.PortFileName, doltserver.PIDFileName,
	} {
		path := filepath.Join(f.beadsDir, name)
		art, err := captureArtifact(path)
		if err != nil {
			f.t.Fatalf("fingerprint %s: %v", path, err)
		}
		parts = append(parts, fmt.Sprintf("%s present=%v mode=%v sha=%x", name, art.Present, art.Mode, art.Data))
	}
	return strings.Join(parts, "\n")
}

// treeFingerprint lists every path under dir with its size, so a test can prove
// `dolt init` was undone exactly — not merely that the directory still exists.
func treeFingerprint(t *testing.T, dir string) string {
	t.Helper()
	var lines []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		if info.IsDir() {
			lines = append(lines, rel+"/")
			return nil
		}
		lines = append(lines, fmt.Sprintf("%s %d", rel, info.Size()))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return strings.Join(lines, "\n")
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
