package doltserver

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// A port this process is listening on must resolve to this process. The whole
// ownership transfer rests on this lookup being able to name the holder of an
// endpoint, so prove it against a listener whose owner is known for certain.
func TestResolvePortHolderFindsThisProcess(t *testing.T) {
	if PortHolderSource() == "" {
		t.Skipf("no port-holder lookup on %s", runtime.GOOS)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close() //nolint:errcheck // test cleanup
	port := listener.Addr().(*net.TCPAddr).Port

	pid, boundBy, outcome := ResolvePortHolderInDir(port, "")
	if outcome != PortHolderHeld {
		t.Fatalf("a port this process is listening on resolved %s", outcome)
	}
	if pid != os.Getpid() {
		t.Fatalf("resolved pid %d, want this process (%d)", pid, os.Getpid())
	}
	if boundBy != "" {
		t.Errorf("an unbound lookup reported binding %q", boundBy)
	}
}

// A port nobody holds is NoHolder, which is a different answer from
// Undetermined and the only one that lets a release gate pass.
func TestResolvePortHolderReportsAFreePortAsNoHolder(t *testing.T) {
	if runtime.GOOS != "linux" {
		// Elsewhere findPIDOnPort cannot separate "nothing listening" from
		// "the helper failed", so this distinction is not available.
		t.Skipf("only the procfs lookup can distinguish a free port from a failed lookup (%s)", runtime.GOOS)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	_, _, outcome := ResolvePortHolderInDir(port, "")
	if outcome != PortHolderNoHolder {
		t.Fatalf("a closed port resolved %s, want %s", outcome, PortHolderNoHolder)
	}
}

// An invalid port is a question the lookup cannot answer, not an answer of
// "free". A caller that read it as free would pass a gate on nothing.
func TestResolvePortHolderRefusesAnInvalidPort(t *testing.T) {
	if _, _, outcome := ResolvePortHolderInDir(0, ""); outcome != PortHolderUndetermined {
		t.Fatalf("port 0 resolved %s, want %s", outcome, PortHolderUndetermined)
	}
}

// The directory binding: a process holding an open descriptor under dir is
// reported "fd-lock", which is the binding that survives a chdir. This is the
// half lsof's cwd view cannot provide and the reason the Linux lookup reads
// procfs rather than shelling out.
func TestResolvePortHolderBindsByOpenDescriptor(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("fd binding is procfs-only (%s)", runtime.GOOS)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks(t.TempDir()): %v", err)
	}
	held, err := os.Create(filepath.Join(dir, "LOCK")) //nolint:gosec // test fixture
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer held.Close() //nolint:errcheck // test cleanup

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close() //nolint:errcheck // test cleanup
	port := listener.Addr().(*net.TCPAddr).Port

	pid, boundBy, outcome := ResolvePortHolderInDir(port, dir)
	if outcome != PortHolderHeld {
		t.Fatalf("lookup resolved %s for a process holding a descriptor under %s", outcome, dir)
	}
	if pid != os.Getpid() {
		t.Fatalf("resolved pid %d, want %d", pid, os.Getpid())
	}
	if boundBy != "fd-lock" {
		t.Errorf("binding is %q, want %q", boundBy, "fd-lock")
	}
}

// A listener that has nothing to do with the directory is not this workspace's
// server. NoHolder here means "not the one you asked about", which is a real
// answer — unlike Undetermined, which means bd could not look.
func TestResolvePortHolderRejectsAnUnrelatedDirectory(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("directory binding is procfs-only (%s)", runtime.GOOS)
	}
	unrelated, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks(t.TempDir()): %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close() //nolint:errcheck // test cleanup

	_, _, outcome := ResolvePortHolderInDir(listener.Addr().(*net.TCPAddr).Port, unrelated)
	if outcome == PortHolderHeld {
		t.Fatal("a listener was bound to a directory it has nothing to do with")
	}
}

// The evidence label must name the mechanism actually compiled in, because a
// journal records it next to a gate that depended on it.
func TestPortHolderSourceNamesThisPlatformsMechanism(t *testing.T) {
	want := map[string]string{
		"linux":   "proc",
		"darwin":  "lsof",
		"windows": "netstat",
	}[runtime.GOOS]
	if got := PortHolderSource(); got != want {
		t.Fatalf("PortHolderSource() = %q on %s, want %q", got, runtime.GOOS, want)
	}
}

func TestPortHolderOutcomeStringsAreStable(t *testing.T) {
	for outcome, want := range map[PortHolderOutcome]string{
		PortHolderHeld:         "held",
		PortHolderNoHolder:     "no_holder",
		PortHolderUndetermined: "undetermined",
	} {
		if got := outcome.String(); got != want {
			t.Errorf("outcome %d renders %q, want %q", int(outcome), got, want)
		}
	}
}

// EnsureDoltInit is the exported form of beads' own init predicate. Assert it
// agrees with the predicate the handoff gates use, rather than that it returns
// nil: a wrapper that swallowed the work would also return nil.
func TestEnsureDoltInitCreatesADoltRoot(t *testing.T) {
	if _, err := os.Stat("/proc/self"); err != nil && runtime.GOOS == "linux" {
		t.Skip("no procfs")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "scope_db")
	if _, err := os.Stat(filepath.Join(target, ".dolt")); err == nil {
		t.Fatal("fixture is already initialized")
	}
	// Only assert the observable contract when dolt is present; the exported
	// wrapper must not invent an answer when it is not.
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skipf("dolt is not on PATH: %v", err)
	}
	t.Setenv("DOLT_ROOT_PATH", dir)
	if err := os.MkdirAll(filepath.Join(dir, ".dolt"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := `{"user.name":"bd test","user.email":"test@example.invalid"}`
	if err := os.WriteFile(filepath.Join(dir, ".dolt", "config_global.json"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write dolt config: %v", err)
	}
	if err := EnsureDoltInit(target); err != nil {
		t.Fatalf("EnsureDoltInit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, ".dolt")); err != nil {
		t.Fatalf("EnsureDoltInit returned nil but made no dolt root: %v", err)
	}
}

// ResolveAutoStartForDir must answer for the directory it is given, not for the
// ambient process config — that is the whole reason it exists alongside
// IsAutoStartDisabled.
func TestResolveAutoStartForDirAnswersPerWorkspace(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "")
	t.Setenv("BEADS_DOLT_SERVER_MODE", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")

	owned := t.TempDir()
	if !ResolveAutoStartForDir(owned) {
		t.Error("a workspace with no external server does not resolve to auto-start")
	}

	external := t.TempDir()
	metadata := `{"backend":"dolt","dolt_server_port":3307}`
	if err := os.WriteFile(filepath.Join(external, "metadata.json"), []byte(metadata), 0o600); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}
	if ResolveServerMode(external) != ServerModeExternal {
		t.Fatalf("fixture did not resolve as external")
	}
	if ResolveAutoStartForDir(external) {
		t.Error("a workspace with an explicit dolt_server_port resolves to auto-start")
	}
	// And the two directories disagree in the same process, which is the point.
	if !ResolveAutoStartForDir(owned) {
		t.Error("the external workspace changed the answer for an unrelated one")
	}
}
