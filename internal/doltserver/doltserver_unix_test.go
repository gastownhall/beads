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
			snapshot: "S  101 /usr/local/bin/dolt sql-server --port 3306\n",
			want:     []int{101},
		},
		{
			name:     "profiled command",
			snapshot: "Sl 102 dolt --prof cpu --prof-path /tmp/dolt-pprof sql-server\n",
			want:     []int{102},
		},
		{
			name:     "ordered loose false positive",
			snapshot: "S 103 some-tool says dolt then sql-server\n",
			want:     []int{103},
		},
		{
			name:     "sql server before dolt is rejected",
			snapshot: "S 104 sql-server then dolt\n",
		},
		{
			name:     "case sensitive command matching",
			snapshot: "S 105 Dolt sql-server\nS 106 dolt SQL-SERVER\n",
		},
		{
			name:     "zombie and dead states including modifiers are rejected",
			snapshot: "Z 107 dolt sql-server\nZ+ 108 dolt sql-server\nX 109 dolt sql-server\nX< 110 dolt sql-server\n",
		},
		{
			name:     "valid state prefix with modifier is accepted",
			snapshot: "S+ 111 dolt sql-server\n",
			want:     []int{111},
		},
		{
			name: "malformed and invalid rows are rejected",
			snapshot: "\n" +
				"S not-a-pid dolt sql-server\n" +
				"S 0 dolt sql-server\n" +
				"S -1 dolt sql-server\n" +
				"112\n" +
				"S 113\n" +
				"S 115 \n",
		},
		{
			// GH#7121: macOS 26 prints an empty state column for many
			// processes; the path must not be consumed as the state.
			name:     "blank state keeps the whole command",
			snapshot: "      1466 /Users/u/.nix-profile/bin/dolt sql-server --config /Users/u/.beads/shared-server/dolt-server-config.yaml\n",
			want:     []int{1466},
		},
		{
			name:     "blank state separated by a tab",
			snapshot: "114\tdolt sql-server\n",
			want:     []int{114},
		},
		{
			name:     "blank state with bare dolt and extra spaces",
			snapshot: "  117   dolt   sql-server  \n",
			want:     []int{117},
		},
		{
			name:     "blank state command starting with a state-like word",
			snapshot: "116 Z dolt sql-server\n",
			want:     []int{116},
		},
		{
			name:     "blank state profiled command",
			snapshot: "123 dolt --prof cpu sql-server\n1469 /Users/u/.nix-profile/bin/dolt --prof cpu sql-server\n",
			want:     []int{123, 1469},
		},
		{
			name:     "blank state keeps ordered case sensitive matching",
			snapshot: "121 sql-server then dolt\n122 Dolt sql-server\n124 dolt SQL-SERVER\n",
		},
		{
			name:     "mixed populated and blank states",
			snapshot: "Ss 301 dolt sql-server\n302 dolt sql-server\nZ 303 dolt sql-server\nS 304 dolt sql-server\n",
			want:     []int{301, 302, 304},
		},
		{
			name:     "running state with modifier is accepted",
			snapshot: "R+ 118 dolt sql-server\n",
			want:     []int{118},
		},
		{
			name:     "linux tracing-stop state t keeps a live server",
			snapshot: "t 401 dolt sql-server\ntl 402 dolt sql-server\n",
			want:     []int{401, 402},
		},
		{
			name:     "historical and freebsd primary states L and W keep a live server",
			snapshot: "L 403 dolt sql-server\nW 404 dolt sql-server\nLs+ 405 dolt sql-server\n",
			want:     []int{403, 404, 405},
		},
		{
			name:     "freebsd capability and jail modifiers keep a live server",
			snapshot: "SJ 406 dolt sql-server\nSC+ 407 dolt sql-server\nSs+CJ 408 dolt sql-server\n",
			want:     []int{406, 407, 408},
		},
		{
			name: "absolute executable path containing spaces is recognised",
			snapshot: "S 409 /Users/John Smith/bin/dolt sql-server --config x.yaml\n" +
				"S 410 /Users/u/Library/Application Support/tools/dolt sql-server\n" +
				"Sl 411 /Users/John Smith/bin/dolt --prof cpu sql-server\n",
			want: []int{409, 410, 411},
		},
		{
			name: "blank state with spaced executable path",
			snapshot: "412 /Users/John Smith/bin/dolt sql-server\n" +
				"413 /Users/u/Library/Application Support/tools/dolt sql-server --port 3307\n",
			want: []int{412, 413},
		},
		{
			name: "zombie and dead states are rejected for spaced executable path",
			snapshot: "Z 415 /Users/John Smith/bin/dolt sql-server\n" +
				"X< 416 /Users/u/Library/Application Support/tools/dolt sql-server\n",
		},
		{
			name: "numeric-looking malformed fields are rejected",
			snapshot: "+119 dolt sql-server\n" +
				"-120 dolt sql-server\n" +
				"1x2 dolt sql-server\n" +
				"0 dolt sql-server\n" +
				"99999999999999999999 dolt sql-server\n" +
				"S 99999999999999999999 dolt sql-server\n" +
				"S dolt sql-server\n",
		},
		{
			name:     "preserves source order",
			snapshot: "S 202 dolt sql-server\nS 201 dolt sql-server\nR 203 dolt sql-server\n",
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

// TestReadDoltProcessPIDsListsLiveServer runs the reader against the system
// ps, so the parser is held to the column format the reader requests. A shell
// blocked on stdin, with dolt and sql-server in its arguments, stands in for
// a server. Linux populates the state column, so this pins the requested
// column format; the blank-state regression itself is covered by the
// recorded rows in TestParseDoltProcessPIDs (GH#7121).
func TestReadDoltProcessPIDsListsLiveServer(t *testing.T) {
	if _, err := exec.LookPath("ps"); err != nil {
		t.Skip("ps not available")
	}
	cmd := exec.Command("/bin/sh", "-c", "read line", "dolt", "sql-server")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
	})

	pids, err := readDoltProcessPIDs()
	if err != nil {
		t.Fatalf("readDoltProcessPIDs() error = %v", err)
	}
	for _, pid := range pids {
		if pid == cmd.Process.Pid {
			return
		}
	}
	t.Fatalf("readDoltProcessPIDs() = %v, want it to include stand-in PID %d", pids, cmd.Process.Pid)
}
