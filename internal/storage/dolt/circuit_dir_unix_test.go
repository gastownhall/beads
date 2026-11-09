//go:build unix

package dolt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// redirectTempRootForCircuitTest points the production circuit breaker
// directory policy at a private per-test temp root: TMPDIR/TEMP/TMP drive
// os.TempDir() on every platform, and the test override is cleared so the
// production location rules run. Every directory the tests touch stays
// inside the returned root.
func redirectTempRootForCircuitTest(t *testing.T) string {
	t.Helper()
	t.Setenv(testCircuitBreakerDirEnv, "")
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	t.Setenv("TMP", root)
	t.Setenv("TEMP", root)
	return root
}

// perUserCircuitDir returns the per-user circuit state directory the Unix
// location policy must resolve to under root, computed independently of
// circuitBreakerDir so a regression to the unsuffixed shared name fails
// here rather than mirroring the production helper (GH#4223).
func perUserCircuitDir(root string) string {
	return filepath.Join(root, "beads-circuit-"+strconv.Itoa(os.Geteuid()))
}

// TestCircuitBreakerDir_PrivatePerUser pins the per-user location policy
// (GH#4223): on a multi-user host one shared temp-root directory meant one
// user's 0600 state files were unreadable and unwritable by every other
// user, silently discarding their breaker state. Live state must land in a
// uid-suffixed directory created 0700, and the unsuffixed shared directory
// must not be created at all.
func TestCircuitBreakerDir_PrivatePerUser(t *testing.T) {
	root := redirectTempRootForCircuitTest(t)

	cb := maybeNewCircuitBreaker("127.0.0.1", 44444, "peruser")
	if cb == nil {
		t.Fatal("breaker construction over a fresh private temp root must not be disabled")
	}
	recordDialFailure(cb, dialError(syscall.ECONNREFUSED))

	wantDir := perUserCircuitDir(root)
	if got := filepath.Dir(cb.filePath); got != wantDir {
		t.Errorf("state directory = %s, want per-user %s", got, wantDir)
	}
	dirInfo, err := os.Stat(wantDir)
	if err != nil {
		t.Fatalf("per-user state directory must exist: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("per-user state directory mode = %o, want 0700", got)
	}
	fileInfo, err := os.Stat(cb.filePath)
	if err != nil {
		t.Fatalf("recorded failure must persist state: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Errorf("state file mode = %o, want 0600", got)
	}
	if _, err := os.Lstat(filepath.Join(root, "beads-circuit")); !os.IsNotExist(err) {
		t.Errorf("unsuffixed shared directory must not be created (stat err %v)", err)
	}
}

// TestCircuitBreaker_SameUserInstancesShareState verifies the coordination
// the per-user directory preserves: two independently constructed breakers
// for one endpoint and database under one user's temp root share persisted
// state, so a tripped breaker makes the other instance fail fast without
// waiting out the cooldown. No sleep is needed: the state is inspected
// immediately after the threshold with fresh timestamps.
func TestCircuitBreaker_SameUserInstancesShareState(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	root := redirectTempRootForCircuitTest(t)

	cb1 := maybeNewCircuitBreaker("127.0.0.1", 44444, "coord")
	cb2 := maybeNewCircuitBreaker("127.0.0.1", 44444, "coord")
	if cb1 == nil || cb2 == nil {
		t.Fatal("breaker construction over a fresh private temp root must not be disabled")
	}
	if cb1.filePath != cb2.filePath {
		t.Errorf("same endpoint and database must share one state file: %s vs %s", cb1.filePath, cb2.filePath)
	}
	if want := perUserCircuitDir(root); filepath.Dir(cb1.filePath) != want {
		t.Errorf("shared state must live in the per-user directory %s, got %s", want, filepath.Dir(cb1.filePath))
	}

	for i := 0; i < circuitFailureThreshold; i++ {
		cb1.RecordFailure()
	}
	if got := cb2.State(); got != circuitOpen {
		t.Fatalf("second instance state = %q, want %q from persisted state", got, circuitOpen)
	}
	if cb2.Allow() {
		t.Error("second instance must reject during the cooldown via the persisted state")
	}
}

// TestCircuitBreaker_EnsureCircuitDirValidation table-tests the live state
// directory validator through real Lstat results: what is accepted
// (self-owned 0700 and 0755 — another ordinary user cannot change entries
// in either), what is refused (symlink, regular file, group/other-writable,
// foreign owner), and that a path that cannot be established at all stays
// silent fail-open (GH#4223). The foreign-owner row runs without root or a
// second account: the expected owner is the injected one.
func TestCircuitBreaker_EnsureCircuitDirValidation(t *testing.T) {
	root := t.TempDir()

	makeDir := func(name string, mode os.FileMode) string {
		t.Helper()
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	// A symlinked directory and a regular file at the live path.
	linkTarget := makeDir("target", 0o700)
	symlinked := filepath.Join(root, "symlinked")
	if err := os.Symlink(linkTarget, symlinked); err != nil {
		t.Fatal(err)
	}
	regularFile := filepath.Join(root, "regular-file")
	if err := os.WriteFile(regularFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// An uncreatable path: its parent is a regular file.
	uncreatable := filepath.Join(regularFile, "beads-circuit")

	tests := []struct {
		name      string
		dir       string
		ownerFn   func() int
		wantSafe  bool
		wantWarn  bool // a reason must (not) be reported
		wantInMsg string
	}{
		{"self-owned 0700", makeDir("dir-0700", 0o700), nil, true, false, ""},
		{"self-owned 0755", makeDir("dir-0755", 0o755), nil, true, false, ""},
		{"group/other-writable 0770", makeDir("dir-0770", 0o770), nil, false, true, "writable by group"},
		{"group/other-writable 0777", makeDir("dir-0777", 0o777), nil, false, true, "writable by group"},
		{"symlink", symlinked, nil, false, true, "symlink"},
		{"regular file", regularFile, nil, false, true, "not a directory"},
		{"foreign owner", makeDir("dir-foreign", 0o700), func() int { return os.Geteuid() + 1000 }, false, true, "owned by uid"},
		{"uncreatable", uncreatable, nil, false, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.ownerFn != nil {
				orig := circuitDirOwnerFn
				circuitDirOwnerFn = tt.ownerFn
				t.Cleanup(func() { circuitDirOwnerFn = orig })
			}
			usable, reason := ensureCircuitDir(tt.dir)
			if usable != tt.wantSafe {
				t.Errorf("usable = %v, want %v", usable, tt.wantSafe)
			}
			if (reason != nil) != tt.wantWarn {
				t.Errorf("reason = %v, want reported: %v", reason, tt.wantWarn)
			}
			if tt.wantInMsg != "" && (reason == nil || !strings.Contains(reason.Error(), tt.wantInMsg)) {
				t.Errorf("reason = %v, want it to mention %q", reason, tt.wantInMsg)
			}
		})
	}
}

// TestCircuitBreaker_UnsafeDirWarningOncePerProcess pins the reporting
// contract: one warning per process per unsafe path, so a planted directory
// does not turn every store open into a log line, and a second unsafe path
// still gets its own warning.
func TestCircuitBreaker_UnsafeDirWarningOncePerProcess(t *testing.T) {
	root1 := redirectTempRootForCircuitTest(t)
	unsafe1 := perUserCircuitDir(root1)
	target1 := filepath.Join(root1, "target-1")
	if err := os.MkdirAll(target1, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target1, unsafe1); err != nil {
		t.Fatal(err)
	}
	root2 := t.TempDir()
	unsafe2 := filepath.Join(root2, "beads-circuit-"+strconv.Itoa(os.Geteuid()))
	target2 := filepath.Join(root2, "target-2")
	if err := os.MkdirAll(target2, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target2, unsafe2); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		unsafeCircuitDirMu.Lock()
		delete(unsafeCircuitDirWarned, unsafe1)
		delete(unsafeCircuitDirWarned, unsafe2)
		unsafeCircuitDirMu.Unlock()
	})

	// Two constructions over one unsafe path: one warning. The path may
	// also appear inside the reason, so count the warning header, not
	// every mention.
	warningFor := func(dir string) int {
		return strings.Count(buf.String(), "state directory "+dir+" is unsafe")
	}

	if cb := maybeNewCircuitBreaker("127.0.0.1", 44444, "warned"); cb != nil {
		t.Fatal("breaker over an unsafe directory must be disabled")
	}
	if cb := maybeNewCircuitBreaker("127.0.0.1", 44444, "warned"); cb != nil {
		t.Fatal("breaker over an unsafe directory must be disabled")
	}
	if got := warningFor(unsafe1); got != 1 {
		t.Errorf("warnings for %s = %d, want 1 (log: %q)", unsafe1, got, buf.String())
	}

	// A second unsafe path: its own warning.
	t.Setenv("TMPDIR", root2)
	if cb := maybeNewCircuitBreaker("127.0.0.1", 44444, "warned"); cb != nil {
		t.Fatal("breaker over a second unsafe directory must be disabled")
	}
	if got := warningFor(unsafe2); got != 1 {
		t.Errorf("warnings for %s = %d, want 1 (log: %q)", unsafe2, got, buf.String())
	}
}

// TestNewServerMode_ForeignCircuitStateDoesNotBlockDial pins the unsafe
// live-directory rule of GH#4223: a symlink at the resolved live directory
// (readable untrusted state behind it) must disable the breaker rather than
// have server-mode initialization trust what it points at. Base follows the
// directory symlink, reads a fresh open state, and refuses to dial.
func TestNewServerMode_ForeignCircuitStateDoesNotBlockDial(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")
	root := redirectTempRootForCircuitTest(t)

	const port = 44444
	stateDir := filepath.Join(root, "circuit-state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(stateDir, "beads-dolt-circuit-127-0-0-1-44444-foreign.json")
	freshOpen, err := json.Marshal(circuitState{
		State:        circuitOpen,
		Failures:     circuitFailureThreshold,
		FirstFailure: time.Now(),
		LastFailure:  time.Now(),
		TrippedAt:    time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFile, freshOpen, 0o600); err != nil {
		t.Fatal(err)
	}
	// The unsuffixed shared path is the base binary's live directory, and
	// the uid-suffixed one is this fix's: both are symlinks to the same
	// state, so whichever the binary resolves, the open state is only ever
	// reachable through a symlink.
	if err := os.Symlink(stateDir, filepath.Join(root, "beads-circuit")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(stateDir, perUserCircuitDir(root)); err != nil {
		t.Fatal(err)
	}

	origClean, origDial := cleanServerCircuitState, dialServer
	dialCalls := 0
	cleanServerCircuitState = func() {}
	dialServer = func(string, string, time.Duration) (net.Conn, error) {
		dialCalls++
		return nil, dialError(syscall.ECONNREFUSED)
	}
	t.Cleanup(func() { cleanServerCircuitState, dialServer = origClean, origDial })

	beadsDir := t.TempDir()
	cfg := &Config{
		Database:   "foreign",
		BeadsDir:   beadsDir,
		Path:       filepath.Join(beadsDir, "dolt"),
		ServerHost: "127.0.0.1",
		ServerPort: port,
		AutoStart:  false,
	}
	_, err = newServerMode(context.Background(), cfg)
	if err == nil {
		t.Fatal("an open whose dial fails must fail")
	}
	if errors.Is(err, ErrCircuitOpen) {
		t.Errorf("an unsafe live directory must not fail the open closed: %v", err)
	}
	if dialCalls == 0 {
		t.Error("a fresh server dial must be attempted when the breaker is disabled")
	}
	// No breaker state may be read through the unsafe path — the planted
	// fresh open state would have rejected the open — or written through it.
	got, readErr := os.ReadFile(stateFile)
	if readErr != nil {
		t.Fatalf("planted state must remain readable: %v", readErr)
	}
	if string(got) != string(freshOpen) {
		t.Errorf("planted state was modified through the unsafe path: %q", got)
	}
}

// TestCircuitBreaker_UnsafeStateDirIsNotWrittenThrough pins the write half
// of the unsafe live-directory rule: a predictable `.tmp` symlink planted
// inside a symlinked live directory must not redirect writeState to an
// attacker-chosen file. Base writes breaker JSON straight through it.
func TestCircuitBreaker_UnsafeStateDirIsNotWrittenThrough(t *testing.T) {
	root := redirectTempRootForCircuitTest(t)

	const port = 44445
	stateBase := "beads-dolt-circuit-127-0-0-1-44445-unsafe.json"
	stateDir := filepath.Join(root, "circuit-state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(root, "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("sentinel-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	tmpLink := filepath.Join(stateDir, stateBase+".tmp")
	if err := os.Symlink(sentinel, tmpLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(stateDir, filepath.Join(root, "beads-circuit")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(stateDir, perUserCircuitDir(root)); err != nil {
		t.Fatal(err)
	}

	cb := maybeNewCircuitBreaker("127.0.0.1", port, "unsafe")
	recordDialFailure(cb, dialError(syscall.ECONNREFUSED))

	if cb != nil {
		t.Error("breaker over an unsafe live directory must be disabled (nil)")
	}
	got, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("sentinel must remain readable: %v", err)
	}
	if string(got) != "sentinel-bytes" {
		t.Errorf("sentinel was overwritten through the planted .tmp symlink: %q", got)
	}
	if info, statErr := os.Lstat(tmpLink); statErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the .tmp symlink must be left in place (stat err %v)", statErr)
	}
}
