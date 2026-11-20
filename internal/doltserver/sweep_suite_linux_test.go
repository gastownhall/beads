//go:build linux

package doltserver

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"
)

// Exercise the public sweep and real signal path in the order parallel suites
// can finish: clean B first, leaking A second. Linux retains a deleted cwd's
// path in /proc, so this does not depend on a Dolt installation or lsof.
func TestSweepSuiteTestServersPreservesForeignLeak(t *testing.T) {
	t.Setenv(AllowLeakEnv, "")
	rootA, rootB := t.TempDir(), t.TempDir()
	cwd := filepath.Join(rootA, "leaked-server")
	if err := os.Mkdir(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	// The sweep recognizes argv[0] dolt with a sql-server argument. A shell
	// blocked on stdin, started under the argv[0] "dolt", is a deterministic
	// stand-in with no child processes.
	cmd := exec.Command("/bin/sh", "-c", "read line", "dolt", "sql-server")
	cmd.Args[0] = "dolt"
	cmd.Dir = cwd
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
	waitForExecArgv(t, cmd.Process.Pid)
	if err := os.Remove(cwd); err != nil {
		t.Fatal(err)
	}
	if got, deleted := readProcCwd(cmd.Process.Pid); !deleted || got == "" {
		t.Fatalf("stand-in cwd = %q, deleted = %v; want a deleted cwd", got, deleted)
	}
	// A stand-in the sweep cannot recognize would make every "not swept"
	// assertion below pass vacuously.
	if !isDoltServerProcess(cmd.Process.Pid) {
		t.Fatalf("stand-in %d is not recognized as a dolt sql-server", cmd.Process.Pid)
	}

	for _, roots := range [][]string{nil, {rootB}} {
		var swept []SweptServer
		var code int
		out := captureStderr(t, func() {
			swept = SweepSuiteTestServers(roots...)
			code = ApplyLeakPolicy("clean B", 0, swept)
		})
		if len(swept) != 0 || code != 0 || out != "" {
			t.Fatalf("clean sweep %v: swept=%v code=%d stderr=%q", roots, swept, code, out)
		}
		select {
		case <-exited:
			t.Fatal("clean suite consumed the leaking suite's evidence")
		default:
		}
	}

	var swept []SweptServer
	var code int
	captureStderr(t, func() {
		swept = SweepSuiteTestServers(rootA)
		code = ApplyLeakPolicy("leaking A", 0, swept)
	})
	if len(swept) != 1 || swept[0].PID != cmd.Process.Pid || code != 1 {
		t.Fatalf("owning suite: swept=%v code=%d; want its server and failure", swept, code)
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("owning suite left its leaked server running")
	}
}

// waitForExecArgv waits until pid's /proc/<pid>/cmdline is non-empty. Go's
// cmd.Start returns once exec has closed its close-on-exec pipe, which the
// kernel does before it sets up the new image's argument area, so a read
// right after Start can briefly see an empty command line.
func waitForExecArgv(t *testing.T, pid int) {
	t.Helper()
	path := filepath.Join("/proc", strconv.Itoa(pid), "cmdline")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if raw, err := os.ReadFile(path); err == nil && len(raw) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("process %d never exposed its argv in %s", pid, path)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestSweepSuiteTestServersSparesNonDoltArgv0 is the #6186 hazard: a process
// that is not dolt but whose command line mentions both "dolt" and
// "sql-server" (here a shell, as a `tail -f dolt-sql-server.log` would be)
// sits inside a suite-owned root. The cwd gate admits it, so only the
// identity check stands between it and SIGTERM/SIGKILL.
func TestSweepSuiteTestServersSparesNonDoltArgv0(t *testing.T) {
	t.Setenv(AllowLeakEnv, "")
	root := t.TempDir()
	cmd := exec.Command("/bin/sh", "-c", "read line", "dolt", "sql-server")
	cmd.Dir = root
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
	pid := cmd.Process.Pid
	waitForExecArgv(t, pid)
	if cwd, _ := readProcCwd(pid); cwd == "" {
		t.Fatalf("stand-in %d has no readable cwd; the sweep could not have reached it", pid)
	}

	var swept []SweptServer
	var code int
	out := captureStderr(t, func() {
		swept = SweepSuiteTestServers(root)
		code = ApplyLeakPolicy("spare suite", 0, swept)
	})
	if len(swept) != 0 || code != 0 || out != "" {
		t.Errorf("sweep of non-dolt argv[0] %d: swept=%v code=%d stderr=%q; want nothing reported", pid, swept, code, out)
	}
	if isDoltServerProcess(pid) {
		t.Errorf("isDoltServerProcess(%d) = true for argv[0] /bin/sh", pid)
	}
	select {
	case <-exited:
		t.Fatal("sweep signaled a non-dolt process")
	case <-time.After(500 * time.Millisecond):
	}
}

// TestProcArgv pins how /proc/<pid>/cmdline bytes become argv: split on NUL
// after dropping one terminating NUL, keeping every argument's boundaries.
// Flattening to spaces (as the sweep once did) lets an empty argv[0] shift
// "dolt" into its place, splits an executable path containing a space, and
// turns words inside one argument into separate arguments.
func TestProcArgv(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		want   []string
		server bool
	}{
		{"terminating NUL", "dolt\x00sql-server\x00", []string{"dolt", "sql-server"}, true},
		{"missing terminator", "dolt\x00sql-server", []string{"dolt", "sql-server"}, true},
		{"path with spaces", "/Users/dev/My Tools/dolt\x00sql-server\x00", []string{"/Users/dev/My Tools/dolt", "sql-server"}, true},
		{"empty argv[0] is kept, never shifted", "\x00dolt\x00sql-server\x00", []string{"", "dolt", "sql-server"}, false},
		{"interior empty argument is kept", "dolt\x00\x00sql-server\x00", []string{"dolt", "", "sql-server"}, true},
		{"trailing empty argument is kept", "dolt\x00sql-server\x00\x00", []string{"dolt", "sql-server", ""}, true},
		{"sql-server inside a larger argument", "dolt\x00commit\x00-m\x00restart sql-server\x00", []string{"dolt", "commit", "-m", "restart sql-server"}, false},
		{"single flattened process title", "dolt sql-server -P 1", []string{"dolt sql-server -P 1"}, false},
		{"empty input", "", nil, false},
		{"lone terminator", "\x00", []string{""}, false},
	}
	for _, tc := range cases {
		got := procArgv([]byte(tc.raw))
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: procArgv(%q) = %q, want %q", tc.name, tc.raw, got, tc.want)
		}
		if server := isDoltServerArgv(got); server != tc.server {
			t.Errorf("%s: isDoltServerArgv(procArgv(%q)) = %v, want %v", tc.name, tc.raw, server, tc.server)
		}
	}
}

// TestSweepReadsExactProcArgv runs real processes and checks both places the
// Linux sweep reads /proc: candidate gathering and the pre-signal
// revalidation (isDoltServerProcess). An executable path with a space ending
// in /dolt is a server; a process whose only "sql-server" is inside a larger
// argument is not.
func TestSweepReadsExactProcArgv(t *testing.T) {
	root := t.TempDir()
	toolDir := filepath.Join(root, "My Tools")
	if err := os.Mkdir(toolDir, 0o700); err != nil {
		t.Fatal(err)
	}
	spacedDolt := filepath.Join(toolDir, "dolt")
	if err := os.Symlink("/bin/sh", spacedDolt); err != nil {
		t.Fatal(err)
	}

	start := func(cmd *exec.Cmd) int {
		t.Helper()
		cmd.Dir = root
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		exited := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(exited)
		}()
		t.Cleanup(func() {
			_ = stdin.Close()
			_ = cmd.Process.Kill()
			<-exited
		})
		waitForExecArgv(t, cmd.Process.Pid)
		return cmd.Process.Pid
	}
	// argv[0] is the spaced path itself; "sql-server" is a separate argument.
	spaced := start(exec.Command(spacedDolt, "-c", "read line", "x", "sql-server"))
	// argv[0] is "dolt", but "sql-server" only appears inside the script body.
	embedded := exec.Command("/bin/sh", "-c", "read line # sql-server")
	embedded.Args[0] = "dolt"
	inside := start(embedded)

	gathered := map[int]bool{}
	for _, c := range gatherDoltServerCandidates() {
		gathered[c.pid] = true
	}
	if !gathered[spaced] || !isDoltServerProcess(spaced) {
		t.Errorf("space-containing path ending in /dolt: gathered=%v revalidated=%v, want both true",
			gathered[spaced], isDoltServerProcess(spaced))
	}
	if gathered[inside] || isDoltServerProcess(inside) {
		t.Errorf("sql-server inside a larger argument: gathered=%v revalidated=%v, want both false",
			gathered[inside], isDoltServerProcess(inside))
	}
}
