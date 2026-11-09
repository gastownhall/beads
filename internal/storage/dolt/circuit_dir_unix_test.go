//go:build unix

package dolt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// privateCircuitTempRoot points os.TempDir() at a fresh test-owned directory
// and clears the suite's circuit-directory override, so the production
// resolver picks the live directory. Every path it can produce stays under
// the returned root.
func privateCircuitTempRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv(testCircuitBreakerDirEnv, "")
	t.Setenv("TMPDIR", root)
	return root
}

// GH#4223: state lives in a directory private to the effective uid, so one
// user's 0600 files can never shadow or block another user's breaker.
func TestCircuitBreakerDir_PrivatePerUser(t *testing.T) {
	root := privateCircuitTempRoot(t)
	wantDir := filepath.Join(root, fmt.Sprintf("beads-circuit-%d", os.Geteuid()))

	cb := maybeNewCircuitBreaker("127.0.0.1", 44444, "per_user")
	if cb == nil {
		t.Fatal("maybeNewCircuitBreaker returned nil for a private temp root")
	}
	recordDialFailure(cb, syscall.ECONNREFUSED)

	if got := filepath.Dir(cb.filePath); got != wantDir {
		t.Fatalf("breaker state dir = %q, want %q", got, wantDir)
	}
	dirInfo, err := os.Lstat(wantDir)
	if err != nil {
		t.Fatalf("stat live dir: %v", err)
	}
	if !dirInfo.IsDir() || dirInfo.Mode().Perm() != 0o700 {
		t.Errorf("live dir mode = %v, want a 0700 directory", dirInfo.Mode())
	}
	fileInfo, err := os.Stat(cb.filePath)
	if err != nil {
		t.Fatalf("state file not written: %v", err)
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Errorf("state file mode = %v, want 0600", fileInfo.Mode().Perm())
	}
	if _, err := os.Lstat(filepath.Join(root, "beads-circuit")); !os.IsNotExist(err) {
		t.Errorf("shared beads-circuit directory was created (stat err %v)", err)
	}
}

// The per-user directory keeps the breaker's purpose for one user: a process
// that trips it makes every other process of that user fail fast through the
// persisted state until the cooldown elapses.
func TestCircuitBreaker_SameUserInstancesSharePersistedState(t *testing.T) {
	privateCircuitTempRoot(t)
	t.Setenv("BEADS_TEST_MODE", "") // need real breaker behavior

	tripper := maybeNewCircuitBreaker("127.0.0.1", 44445, "shared_db")
	reader := maybeNewCircuitBreaker("127.0.0.1", 44445, "shared_db")
	if tripper == nil || reader == nil {
		t.Fatal("maybeNewCircuitBreaker returned nil for a private temp root")
	}
	if tripper == reader || tripper.filePath != reader.filePath {
		t.Fatalf("want two instances on one state file, got %q and %q", tripper.filePath, reader.filePath)
	}
	if !reader.Allow() {
		t.Fatal("a fresh breaker should allow")
	}
	for i := 0; i < circuitFailureThreshold; i++ {
		tripper.RecordFailure()
	}
	if reader.Allow() {
		t.Fatal("the other instance allowed a request during the cooldown of a tripped breaker")
	}
	if got := reader.State(); got != circuitOpen {
		t.Errorf("other instance state = %q, want %q", got, circuitOpen)
	}
}

// plantSymlinkedCircuitDir makes each of linkPaths a symlink to one
// test-owned directory holding a fresh open state for 127.0.0.1:port/database,
// as another local user could plant at the predictable live path. It returns
// the state file behind the links.
func plantSymlinkedCircuitDir(t *testing.T, port int, database string, linkPaths ...string) string {
	t.Helper()
	target := t.TempDir()
	data, err := json.Marshal(circuitState{State: circuitOpen, Failures: circuitFailureThreshold, TrippedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(target, fmt.Sprintf("beads-dolt-circuit-127-0-0-1-%d-%s.json", port, database))
	if err := os.WriteFile(stateFile, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, linkPath := range linkPaths {
		if err := os.Symlink(target, linkPath); err != nil {
			t.Fatal(err)
		}
	}
	return stateFile
}

// liveCircuitDirCandidates names, independently of the resolver under test,
// both live directory paths a binary may use under root: the unsuffixed one
// older binaries shared and the uid-suffixed one this user owns. Planting at
// both keeps a regression meaningful whichever path the binary resolves.
func liveCircuitDirCandidates(root string) []string {
	return []string{
		filepath.Join(root, "beads-circuit"),
		filepath.Join(root, fmt.Sprintf("beads-circuit-%d", os.Geteuid())),
	}
}

// captureCircuitLog redirects the standard logger, where the breaker reports,
// for the rest of the test.
func captureCircuitLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	orig, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(orig)
		log.SetFlags(flags)
	})
	return &buf
}

// GH#4223: breaker state another user can plant at the predictable live path
// must not gate this user's connection. A symlinked live directory disables
// the breaker for the process, so the open dials the server instead of
// failing fast on state it does not own.
func TestNewServerMode_ForeignCircuitStateDoesNotBlockDial(t *testing.T) {
	const port = 14569
	root := privateCircuitTempRoot(t)
	t.Setenv("BEADS_TEST_MODE", "") // keep the real breaker
	stateFile := plantSymlinkedCircuitDir(t, port, "foreign_state", liveCircuitDirCandidates(root)...)
	before, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	captureCircuitLog(t)

	origClean, origDial := cleanServerCircuitState, dialServer
	t.Cleanup(func() { cleanServerCircuitState, dialServer = origClean, origDial })
	cleanServerCircuitState = func() {} // never sweep a real shared temp location
	dials := 0
	dialServer = func(string, string, time.Duration) (net.Conn, error) {
		dials++
		return nil, dialError(syscall.ECONNREFUSED)
	}
	stubEnsureRunningDetailed(t, port, false, nil)

	beadsDir := t.TempDir()
	_, err = newServerMode(context.Background(), &Config{
		Database:   "foreign_state",
		BeadsDir:   beadsDir,
		Path:       filepath.Join(beadsDir, "dolt"),
		ServerHost: "127.0.0.1",
		ServerPort: port,
	})
	if errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("open failed fast on planted breaker state: %v", err)
	}
	if dials == 0 {
		t.Fatalf("open never dialed the server (err %v)", err)
	}
	after, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("planted state was rewritten through the symlinked directory:\nbefore %s\nafter  %s", before, after)
	}
}

// A planted ".tmp" symlink inside an unsafe live directory must not redirect
// the breaker's state write to a file of the attacker's choosing.
func TestCircuitBreaker_UnsafeStateDirIsNotWrittenThrough(t *testing.T) {
	const port = 14570
	root := privateCircuitTempRoot(t)
	stateFile := plantSymlinkedCircuitDir(t, port, "write_through", liveCircuitDirCandidates(root)...)
	sentinel := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(sentinel, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	tmpLink := stateFile + ".tmp"
	if err := os.Symlink(sentinel, tmpLink); err != nil {
		t.Fatal(err)
	}
	captureCircuitLog(t)

	cb := maybeNewCircuitBreaker("127.0.0.1", port, "write_through")
	recordDialFailure(cb, syscall.ECONNREFUSED)

	if cb != nil {
		t.Errorf("breaker enabled over an unsafe directory: %q", cb.filePath)
	}
	got, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "sentinel" {
		t.Errorf("breaker state was written through the planted .tmp symlink: %q", got)
	}
	if info, err := os.Lstat(tmpLink); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the planted .tmp symlink was replaced (lstat err %v)", err)
	}
}

// A live directory that cannot be created at all (its parent is a regular
// file) disables the breaker like any other breaker I/O failure: no breaker,
// and no unsafe-path warning, since nothing was planted there.
func TestCircuitBreaker_UncreatableDirDisablesSilently(t *testing.T) {
	logBuf := captureCircuitLog(t)
	parent := filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(parent, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(testCircuitBreakerDirEnv, filepath.Join(parent, "beads-circuit"))

	if cb := maybeNewCircuitBreaker("127.0.0.1", 14573, "uncreatable"); cb != nil {
		t.Fatalf("breaker enabled over an uncreatable directory: %q", cb.filePath)
	}
	if logged := logBuf.String(); logged != "" {
		t.Errorf("uncreatable directory logged a warning:\n%s", logged)
	}
}

// One unsafe directory warns once per process however many stores open over
// it; a second unsafe directory gets its own warning, naming its path, the
// problem, and a remedy.
func TestCircuitBreaker_UnsafeDirWarnsOncePerPath(t *testing.T) {
	logBuf := captureCircuitLog(t)

	linked := filepath.Join(t.TempDir(), "linked")
	plantSymlinkedCircuitDir(t, 14571, "warn", linked)
	t.Setenv(testCircuitBreakerDirEnv, linked)
	for i := 0; i < 2; i++ {
		if cb := maybeNewCircuitBreaker("127.0.0.1", 14571, "warn"); cb != nil {
			t.Fatalf("breaker enabled over symlinked directory %q", cb.filePath)
		}
	}

	notDir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(notDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(testCircuitBreakerDirEnv, notDir)
	if cb := maybeNewCircuitBreaker("127.0.0.1", 14571, "warn"); cb != nil {
		t.Fatalf("breaker enabled over regular file %q", cb.filePath)
	}

	logged := logBuf.String()
	if n := strings.Count(logged, linked+" "); n != 1 {
		t.Errorf("warnings for %s = %d, want 1; log:\n%s", linked, n, logged)
	}
	if n := strings.Count(logged, notDir+" "); n != 1 {
		t.Errorf("warnings for %s = %d, want 1; log:\n%s", notDir, n, logged)
	}
	for _, want := range []string{"symlink", "not a directory", "TMPDIR"} {
		if !strings.Contains(logged, want) {
			t.Errorf("warning does not mention %q; log:\n%s", want, logged)
		}
	}
}

// The sweep validates the live directory as construction does: it neither
// reads nor removes state through a symlinked one.
func TestCleanStaleCircuitBreakerFiles_SkipsUnsafeLiveDir(t *testing.T) {
	captureCircuitLog(t)
	linked := filepath.Join(t.TempDir(), "linked")
	stateFile := plantSymlinkedCircuitDir(t, 14572, "sweep", linked)
	stale, err := json.Marshal(circuitState{State: circuitOpen, TrippedAt: time.Now().Add(-circuitStaleTTL - time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFile, stale, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(testCircuitBreakerDirEnv, linked) // the override also skips legacy directories

	CleanStaleCircuitBreakerFiles()

	if _, err := os.Stat(stateFile); err != nil {
		t.Errorf("sweep removed state through a symlinked live directory: %v", err)
	}
}

// The live-directory validator, against real Lstat results. The foreign-owner
// row stands in for another user through circuitDirOwnerUID, so it runs
// without root.
func TestCircuitDirProblem(t *testing.T) {
	selfUID := os.Geteuid()
	tests := []struct {
		name     string
		setup    func(t *testing.T, path string)
		ownerUID int
		want     string // substring of the problem; "" means safe
	}{
		{"self-owned 0700", mkdirMode(0o700), selfUID, ""},
		{"self-owned 0755", mkdirMode(0o755), selfUID, ""},
		{"group-writable 0770", mkdirMode(0o770), selfUID, "writable by group or others"},
		{"world-writable 0777", mkdirMode(0o777), selfUID, "writable by group or others"},
		{"sticky world-writable 1777", mkdirMode(0o777 | os.ModeSticky), selfUID, "writable by group or others"},
		{"foreign-owned 0700", mkdirMode(0o700), selfUID + 1, "is owned by uid"},
		{"symlink to a self-owned directory", func(t *testing.T, path string) {
			if err := os.Symlink(t.TempDir(), path); err != nil {
				t.Fatal(err)
			}
		}, selfUID, "is a symlink"},
		{"regular file", func(t *testing.T, path string) {
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}, selfUID, "is not a directory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := circuitDirOwnerUID
			t.Cleanup(func() { circuitDirOwnerUID = orig })
			circuitDirOwnerUID = func() int { return tt.ownerUID }

			path := filepath.Join(t.TempDir(), "beads-circuit-test")
			tt.setup(t, path)
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			got := circuitDirProblem(info)
			if tt.want == "" && got != "" {
				t.Errorf("circuitDirProblem = %q, want safe", got)
			}
			if tt.want != "" && !strings.Contains(got, tt.want) {
				t.Errorf("circuitDirProblem = %q, want it to contain %q", got, tt.want)
			}
		})
	}
}

// mkdirMode creates a directory and sets its mode exactly, past the umask.
func mkdirMode(mode os.FileMode) func(t *testing.T, path string) {
	return func(t *testing.T, path string) {
		t.Helper()
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0o700) })
	}
}

// writeCircuitFile writes breaker state (or raw bytes) with a mode and mtime.
func writeCircuitFile(t *testing.T, dir, name string, content []byte, mode os.FileMode, mtime time.Time) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return path
}

func circuitJSON(t *testing.T, state circuitState) []byte {
	t.Helper()
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// The sweep applies the expiry rules in the validated live directory and in
// the real legacy ones, leaves retained entries exactly as they were, never
// creates an absent legacy directory and never follows a symlinked one.
func TestSweepCircuitBreakerDirs_LiveAndLegacy(t *testing.T) {
	captureCircuitLog(t)
	root := t.TempDir()
	now := time.Now()
	old := now.Add(-closedSweepTTL - time.Hour)
	staleOpen := circuitJSON(t, circuitState{State: circuitOpen, TrippedAt: now.Add(-circuitStaleTTL - time.Hour)})
	staleHalfOpen := circuitJSON(t, circuitState{State: circuitHalfOpen, LastFailure: now.Add(-circuitStaleTTL - time.Hour)})
	freshOpen := circuitJSON(t, circuitState{State: circuitOpen, TrippedAt: now})
	closed := circuitJSON(t, circuitState{State: circuitClosed})

	type entry struct {
		path    string
		content []byte
		mode    os.FileMode
	}
	populate := func(dir string) (removed []string, kept []entry) {
		removed = []string{
			writeCircuitFile(t, dir, "beads-dolt-circuit-127-0-0-1-1001.json", staleOpen, 0o600, now),
			writeCircuitFile(t, dir, "beads-dolt-circuit-127-0-0-1-1002.json", staleHalfOpen, 0o600, now),
			writeCircuitFile(t, dir, "beads-dolt-circuit-127-0-0-1-1003.json", closed, 0o600, old),
			writeCircuitFile(t, dir, "beads-dolt-circuit-127-0-0-1-1004.json", []byte("{corrupt"), 0o600, now),
			writeCircuitFile(t, dir, "beads-dolt-circuit-0.json", freshOpen, 0o600, now),
		}
		for _, k := range []struct {
			name    string
			content []byte
			mode    os.FileMode
		}{
			{"beads-dolt-circuit-127-0-0-1-2001.json", freshOpen, 0o600},
			{"beads-dolt-circuit-127-0-0-1-2002.json", closed, 0o644},
		} {
			kept = append(kept, entry{writeCircuitFile(t, dir, k.name, k.content, k.mode, now), k.content, k.mode})
		}
		return removed, kept
	}

	live := filepath.Join(root, "live")
	if err := os.Mkdir(live, 0o700); err != nil {
		t.Fatal(err)
	}
	liveRemoved, liveKept := populate(live)
	legacy := filepath.Join(root, "legacy")
	if err := os.Mkdir(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	legacyRemoved, legacyKept := populate(legacy)
	absent := filepath.Join(root, "absent")
	linkTarget := t.TempDir()
	linkedStale := writeCircuitFile(t, linkTarget, "beads-dolt-circuit-127-0-0-1-3001.json", staleOpen, 0o600, now)
	linkedPort0 := writeCircuitFile(t, linkTarget, "beads-dolt-circuit-0.json", freshOpen, 0o600, now)
	linked := filepath.Join(root, "linked")
	if err := os.Symlink(linkTarget, linked); err != nil {
		t.Fatal(err)
	}

	sweepCircuitBreakerDirs(live, []string{live, legacy, absent, linked, legacy})

	for _, path := range append(liveRemoved, legacyRemoved...) {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("expired entry survived the sweep: %s (stat err %v)", path, err)
		}
	}
	for _, k := range append(liveKept, legacyKept...) {
		got, err := os.ReadFile(k.path)
		if err != nil {
			t.Errorf("retained entry removed: %s: %v", k.path, err)
			continue
		}
		if !bytes.Equal(got, k.content) {
			t.Errorf("retained entry %s content = %s, want %s", k.path, got, k.content)
		}
		if info, err := os.Lstat(k.path); err == nil && info.Mode().Perm() != k.mode {
			t.Errorf("retained entry %s mode = %v, want %v", k.path, info.Mode().Perm(), k.mode)
		}
	}
	if _, err := os.Lstat(absent); !os.IsNotExist(err) {
		t.Errorf("absent legacy directory was created (stat err %v)", err)
	}
	for _, path := range []string{linkedStale, linkedPort0} {
		if _, err := os.Lstat(path); err != nil {
			t.Errorf("sweep traversed the symlinked legacy directory and removed %s: %v", path, err)
		}
	}
}

// The legacy directories are the unsuffixed one under the current temp root
// and the historical hardcoded /tmp one.
func TestLegacyCircuitBreakerDirs(t *testing.T) {
	root := privateCircuitTempRoot(t)
	got := legacyCircuitBreakerDirs()
	want := []string{filepath.Join(root, "beads-circuit"), filepath.Join("/tmp", "beads-circuit")}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("legacyCircuitBreakerDirs() = %q, want %q", got, want)
	}
}

// The test override isolates the sweep from every legacy directory, the
// unsuffixed one under the temp root included.
func TestCleanStaleCircuitBreakerFiles_OverrideSkipsLegacyDirs(t *testing.T) {
	captureCircuitLog(t)
	root := privateCircuitTempRoot(t)
	t.Setenv(testCircuitBreakerDirEnv, filepath.Join(t.TempDir(), "circuit"))
	legacy := filepath.Join(root, "beads-circuit")
	if err := os.Mkdir(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := writeCircuitFile(t, legacy, "beads-dolt-circuit-127-0-0-1-4001.json",
		circuitJSON(t, circuitState{State: circuitOpen, TrippedAt: time.Now().Add(-circuitStaleTTL - time.Hour)}), 0o600, time.Now())

	CleanStaleCircuitBreakerFiles()

	if _, err := os.Lstat(stale); err != nil {
		t.Errorf("sweep under the test override reached a legacy directory: %v", err)
	}
}
