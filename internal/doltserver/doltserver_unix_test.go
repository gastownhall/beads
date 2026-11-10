//go:build !windows

package doltserver

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// startStaleServerFiles writes a pid file naming an unrelated process (a
// sleep child, as if the PID had been reused) and a port file. The returned
// channel receives the child's exit.
func startStaleServerFiles(t *testing.T, dir string) chan error {
	t.Helper()
	t.Setenv("GT_ROOT", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")

	child := exec.Command("sleep", "300")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- child.Wait() }()
	t.Cleanup(func() {
		_ = child.Process.Kill()
		<-exited
	})
	if err := os.WriteFile(pidPath(dir), []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writePortFile(dir, 14599); err != nil {
		t.Fatal(err)
	}
	return exited
}

func expectNotSignaled(t *testing.T, exited chan error) {
	t.Helper()
	select {
	case err := <-exited:
		exited <- err // let the cleanup finish
		t.Errorf("the unrelated process was signaled: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
}

func TestStopDoesNotSignalUnverifiedProcess(t *testing.T) {
	dir := t.TempDir()
	exited := startStaleServerFiles(t, dir)
	stubProcessList(t, func() ([]int, error) { return nil, errProcessListUnreadable })

	err := Stop(dir)
	if err == nil || errors.Is(err, ErrServerNotRunning) {
		t.Errorf("Stop = %v, want an error refusing the unverified PID", err)
	} else {
		// Where ps never runs, the refusal is the operator's way out: it names
		// the state files to remove if the PID is not a dolt sql-server.
		for _, path := range []string{pidPath(dir), portPath(dir)} {
			if !strings.Contains(err.Error(), path) {
				t.Errorf("refusal does not name %s: %v", path, err)
			}
		}
	}
	expectNotSignaled(t, exited)
	for _, path := range []string{pidPath(dir), portPath(dir)} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected %s to be kept: %v", filepath.Base(path), err)
		}
	}
}

func TestStopDoesNotSignalReusedPIDWhenListBecomesReadable(t *testing.T) {
	dir := t.TempDir()
	exited := startStaleServerFiles(t, dir)
	// The first read (inside IsRunning) fails, the second (in stopLocked) works
	// and does not contain the PID.
	calls := 0
	stubProcessList(t, func() ([]int, error) {
		calls++
		if calls == 1 {
			return nil, errProcessListUnreadable
		}
		return nil, nil
	})

	if err := Stop(dir); !errors.Is(err, ErrServerNotRunning) {
		t.Errorf("Stop = %v, want ErrServerNotRunning for a reused PID", err)
	}
	expectNotSignaled(t, exited)
	for _, path := range []string{pidPath(dir), portPath(dir)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("expected %s to be removed, stat err = %v", filepath.Base(path), err)
		}
	}
}

// With no port recorded for the unverified PID, no other bd process reaches a
// server through its pid file: Stop removes it, as before GH#7214, but never
// signals the PID.
func TestStopWithoutPortDoesNotSignalUnverifiedProcess(t *testing.T) {
	dir := t.TempDir()
	exited := startStaleServerFiles(t, dir)
	if err := os.Remove(portPath(dir)); err != nil {
		t.Fatal(err)
	}
	stubProcessList(t, func() ([]int, error) { return nil, errProcessListUnreadable })

	if err := Stop(dir); !errors.Is(err, ErrServerNotRunning) {
		t.Errorf("Stop = %v, want ErrServerNotRunning", err)
	}
	expectNotSignaled(t, exited)
	if _, err := os.Stat(pidPath(dir)); !os.IsNotExist(err) {
		t.Errorf("expected the pid file to be removed, stat err = %v", err)
	}
}

// Restart over the same state signals nothing on its way to the start.
func TestRestartWithoutPortDoesNotSignalUnverifiedProcess(t *testing.T) {
	dir := t.TempDir()
	exited := startStaleServerFiles(t, dir)
	if err := os.Remove(portPath(dir)); err != nil {
		t.Fatal(err)
	}
	stubProcessList(t, func() ([]int, error) { return nil, errProcessListUnreadable })
	t.Setenv("PATH", t.TempDir()) // no dolt binary: the start after the stop fails

	if _, err := Restart(dir); err == nil {
		t.Error("Restart succeeded without a dolt binary")
	}
	expectNotSignaled(t, exited)
}

// stubSignalZero replaces the signal-0 check for the test.
func stubSignalZero(t *testing.T, signal func(pid int) error) {
	t.Helper()
	orig := signalZero
	signalZero = signal
	t.Cleanup(func() { signalZero = orig })
}

// errNotPermitted is what signal 0 returns for a process that belongs to
// another account.
var errNotPermitted = os.NewSyscallError("kill", syscall.EPERM)

// GH#7214: kill(pid, 0) answering EPERM means the process exists but belongs
// to another account, so IsRunning must count it as alive and keep its state.
func TestIsRunningKeepsStateForProcessItMayNotSignal(t *testing.T) {
	dir := t.TempDir()
	writeTrackedStateForLiveProcess(t, dir, 14567)
	stubSignalZero(t, func(int) error { return errNotPermitted })
	stubProcessList(t, func() ([]int, error) { return []int{os.Getpid()}, nil })

	state, err := IsRunning(dir)
	if err != nil {
		t.Fatalf("IsRunning error: %v", err)
	}
	if !state.Running || state.PID != os.Getpid() || state.Port != 14567 {
		t.Errorf("IsRunning = %+v, want Running with PID %d and port 14567", state, os.Getpid())
	}
	assertStateFilesKept(t, dir, 14567)
}

// With no port recorded or configured, IsRunning stops a listed dolt
// sql-server for a restart, but not one this user may not signal (EPERM): no
// signal would reach it. It reports the server not running and keeps the pid
// file, which a start replaces.
func TestIsRunningDoesNotStopProcessItMayNotSignalWithoutPort(t *testing.T) {
	dir := t.TempDir()
	exited := startStaleServerFiles(t, dir)
	if err := os.Remove(portPath(dir)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	if port := DefaultConfig(dir).Port; port != 0 {
		t.Fatalf("precondition: DefaultConfig resolved port %d, want none", port)
	}
	data, err := os.ReadFile(pidPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	stubSignalZero(t, func(int) error { return errNotPermitted })
	stubProcessList(t, func() ([]int, error) { return []int{pid}, nil })

	state, err := IsRunning(dir)
	if err != nil {
		t.Fatalf("IsRunning error: %v", err)
	}
	if state.Running {
		t.Errorf("IsRunning = %+v, want Running=false with no port", state)
	}
	expectNotSignaled(t, exited)
	if _, err := os.Stat(pidPath(dir)); err != nil {
		t.Errorf("expected the pid file to be kept: %v", err)
	}
}

// The schema-migration ownership proof stays strict: a recorded PID recycled
// to another account's process answers EPERM, and must not prove that this
// workspace manages the server on the port.
func TestManagesLiveServerOnPortRejectsPIDItMayNotSignal(t *testing.T) {
	dir := t.TempDir()
	writeTrackedStateForLiveProcess(t, dir, 3307)
	if !ManagesLiveServerOnPort(dir, 3307) {
		t.Fatal("precondition: a live PID this user may signal is proof")
	}
	stubSignalZero(t, func(int) error { return errNotPermitted })
	if ManagesLiveServerOnPort(dir, 3307) {
		t.Error("a PID this user may not signal (EPERM) must not prove ownership")
	}
}

func TestAliveAfterSignalZero(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"signal delivered", nil, true},
		{"bare EPERM", syscall.EPERM, true},
		{"kill EPERM", os.NewSyscallError("kill", syscall.EPERM), true},
		{"pidfd_send_signal EPERM", os.NewSyscallError("pidfd_send_signal", syscall.EPERM), true},
		{"process done", os.ErrProcessDone, false},
		{"kill ESRCH", os.NewSyscallError("kill", syscall.ESRCH), false},
		{"bare ESRCH", syscall.ESRCH, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := aliveAfterSignalZero(tt.err); got != tt.want {
				t.Errorf("aliveAfterSignalZero(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestParseDoltProcessPIDs(t *testing.T) {
	tests := []struct {
		name     string
		snapshot string
		want     []int
	}{
		{
			name:     "ordinary command",
			snapshot: "  101 S /usr/local/bin/dolt sql-server --port 3306\n",
			want:     []int{101},
		},
		{
			name:     "profiled command",
			snapshot: "102 Sl dolt --prof cpu --prof-path /tmp/dolt-pprof sql-server\n",
			want:     []int{102},
		},
		{
			name:     "ordered loose false positive",
			snapshot: "103 S some-tool says dolt then sql-server\n",
			want:     []int{103},
		},
		{
			name:     "sql server before dolt is rejected",
			snapshot: "104 S sql-server then dolt\n",
		},
		{
			name:     "case sensitive command matching",
			snapshot: "105 S Dolt sql-server\n106 S dolt SQL-SERVER\n",
		},
		{
			name:     "zombie and dead states including modifiers are rejected",
			snapshot: "107 Z dolt sql-server\n108 Z+ dolt sql-server\n109 X dolt sql-server\n110 X< dolt sql-server\n",
		},
		{
			name:     "valid state prefix with modifier is accepted",
			snapshot: "111 S+ dolt sql-server\n",
			want:     []int{111},
		},
		{
			// GH#7121: macOS 26 prints an empty state column, so the state is
			// optional and the executable must survive the missing field.
			name: "empty and present states mix without losing rows",
			snapshot: "114      /usr/local/bin/dolt sql-server --port 3306\n" +
				"115 S    /usr/local/bin/dolt sql-server --port 3307\n" +
				"1466      /Users/u/.nix-profile/bin/dolt sql-server --config /Users/u/.beads/shared-server/dolt-server-config.yaml\n",
			want: []int{114, 115, 1466},
		},
		{
			name:     "tab-separated empty-state row is accepted",
			snapshot: "114\tdolt sql-server\n",
			want:     []int{114},
		},
		{
			// State letters across Linux, macOS and the BSDs, plus the
			// empty-state macOS form, all leave a live row eligible.
			name: "live state codes are accepted across platforms",
			snapshot: "301 S /usr/local/bin/dolt sql-server\n" +
				"302 Ss /usr/local/bin/dolt sql-server\n" +
				"303 Sl /usr/local/bin/dolt sql-server\n" +
				"304 R+ /usr/local/bin/dolt sql-server\n" +
				"305 D /usr/local/bin/dolt sql-server\n" +
				"306 I /usr/local/bin/dolt sql-server\n" +
				"307 U /usr/local/bin/dolt sql-server\n" +
				"308 t /usr/local/bin/dolt sql-server\n" +
				"309 L /usr/local/bin/dolt sql-server\n" +
				"310 W /usr/local/bin/dolt sql-server\n" +
				"311 SC /usr/local/bin/dolt sql-server\n" +
				"312 SJ /usr/local/bin/dolt sql-server\n" +
				"313 SX /usr/local/bin/dolt sql-server\n" +
				"314 SsJ /usr/local/bin/dolt sql-server\n" +
				"315 /usr/local/bin/dolt sql-server\n",
			want: []int{301, 302, 303, 304, 305, 306, 307, 308, 309, 310, 311, 312, 313, 314, 315},
		},
		{
			// The zombie/dead exclusion reads the first field after the PID
			// before the optional state is consumed, so an unrecognized
			// modifier such as a synthetic Zp cannot turn a dead row into a
			// match.
			name:     "dead-state modifiers are excluded, recognized or not",
			snapshot: "316 Zs dolt sql-server\n317 Zp dolt sql-server\n318 Xp dolt sql-server\n",
		},
		{
			name:     "combined state modifiers keep a live server",
			snapshot: "121 tl dolt sql-server\n124 Ls+ dolt sql-server\n126 SC+ dolt sql-server\n127 Ss+CJ dolt sql-server\n",
			want:     []int{121, 124, 126, 127},
		},
		{
			name: "zombie and dead states are rejected for spaced executable path",
			snapshot: "307 Z /Users/John Smith/bin/dolt sql-server\n" +
				"308 X< /Users/u/Library/Application Support/tools/dolt sql-server\n",
		},
		{
			name: "malformed and invalid rows are rejected",
			snapshot: "\n" +
				"not-a-pid S dolt sql-server\n" +
				"0 S dolt sql-server\n" +
				"-1 S dolt sql-server\n" +
				"112\n" +
				"113 S\n" +
				"115 S \n",
		},
		{
			name:     "preserves source order",
			snapshot: "202 S dolt sql-server\n201 S dolt sql-server\n203 R dolt sql-server\n",
			want:     []int{202, 201, 203},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseDoltProcessPIDs([]byte(tt.snapshot)); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseDoltProcessPIDs() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestParseDoltProcessPIDsEmptyState covers macOS ps rows whose state column
// is blank (#7121): the live dolt sql-server must still be recognized.
func TestParseDoltProcessPIDsEmptyState(t *testing.T) {
	snapshot := "  114      /usr/local/bin/dolt sql-server --port 3306\n" +
		"  115 S    /usr/local/bin/dolt sql-server --port 3307\n"
	want := []int{114, 115}
	if got := parseDoltProcessPIDs([]byte(snapshot)); !reflect.DeepEqual(got, want) {
		t.Fatalf("parseDoltProcessPIDs() = %v, want %v", got, want)
	}
}
