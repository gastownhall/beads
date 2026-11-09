//go:build linux

package doltserver

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	// A shell blocked on stdin is a deterministic stand-in with no child
	// processes. Its argv[0] is "dolt" while the executable stays a real
	// /bin/sh path: the tightened identity gate (#6186) requires the
	// executable's basename to be dolt plus an argument exactly sql-server.
	cmd := exec.Command("/bin/sh", "-c", "read line", "sql-server")
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
	// The stand-in must look like a dolt server to the identity probe the
	// sweep consults before signaling, or the owning-suite half below would
	// vacuously pass.
	if !isDoltServerProcess(cmd.Process.Pid) {
		t.Fatalf("stand-in pid %d failed the identity probe; the owning-suite sweep would vacuously pass", cmd.Process.Pid)
	}
	if err := os.Remove(cwd); err != nil {
		t.Fatal(err)
	}
	if got, deleted := readProcCwd(cmd.Process.Pid); !deleted || got == "" {
		t.Fatalf("stand-in cwd = %q, deleted = %v; want a deleted cwd", got, deleted)
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

// TestSweepSuiteTestServersSparesNonDoltArgv0 is the reported regression
// (#6186): a live process whose command line merely MENTIONS dolt and
// sql-server is not a dolt server, even when it sits under a root the caller
// vouches for. The old substring identity selected it and signaled it; the
// tightened predicate must spare it. The public sweep and the real signal
// path are exercised, so this fails behaviorally at the base.
func TestSweepSuiteTestServersSparesNonDoltArgv0(t *testing.T) {
	t.Setenv(AllowLeakEnv, "")
	root := t.TempDir()
	cwd := filepath.Join(root, "log-tailer")
	if err := os.Mkdir(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	// /bin/sh is the executable; the words appear only in the arguments, the
	// exact shape of `tail -f dolt-sql-server.log` run by a helper script.
	cmd := exec.Command("/bin/sh", "-c", "read line", "dolt", "sql-server")
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
	select {
	case <-exited:
		t.Fatal("stand-in exited before the sweep ran")
	default:
	}

	var swept []SweptServer
	var code int
	out := captureStderr(t, func() {
		swept = SweepSuiteTestServers(root)
		code = ApplyLeakPolicy("spare suite", 0, swept)
	})
	if len(swept) != 0 || code != 0 || out != "" {
		t.Fatalf("sweep of non-dolt argv[0]: swept=%v code=%d stderr=%q; want nothing reported", swept, code, out)
	}
	if isDoltServerProcess(cmd.Process.Pid) {
		t.Error("isDoltServerProcess() = true for a /bin/sh stand-in, want false")
	}
	select {
	case <-exited:
		t.Fatal("the sweep killed a non-dolt process whose command line mentioned dolt and sql-server")
	default:
	}
}

// TestProcArgv tables the /proc cmdline parser directly, including the
// shapes only the kernel produces: one final NUL terminator, no terminator
// for a process caught mid-exec, and empty arguments in every position
// (#6186). The identity gate consumes this slice at both the gather and the
// revalidation sites, so a parser that dropped or collapsed empty arguments
// would shift a later argument into argv[0]'s place and read a non-dolt
// process as a server.
func TestProcArgv(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
		want []string
	}{
		{"terminating NUL", []byte("dolt\x00sql-server\x00"), []string{"dolt", "sql-server"}},
		{"missing terminator", []byte("dolt\x00sql-server"), []string{"dolt", "sql-server"}},
		{"leading empty argument", []byte("\x00dolt\x00sql-server\x00"), []string{"", "dolt", "sql-server"}},
		{"interior empty argument", []byte("dolt\x00\x00sql-server\x00"), []string{"dolt", "", "sql-server"}},
		{"empty input", []byte(nil), nil},
		{"bare NUL is one empty argument", []byte("\x00"), []string{""}},
		{"path with spaces stays one element", []byte("/opt/my tools/dolt\x00sql-server\x00"), []string{"/opt/my tools/dolt", "sql-server"}},
		{"a single flattened process title", []byte("dolt sql-server\x00"), []string{"dolt sql-server"}},
	}
	for _, tc := range cases {
		if got := procArgv(tc.raw); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: procArgv(%q) = %q, want %q", tc.name, tc.raw, got, tc.want)
		}
	}

	// The boundaries above must be what the identity gate sees: an empty
	// argv[0] never becomes the next argument, and "sql-server" inside a
	// larger argument is not an argument of its own.
	if isDoltServerArgv(procArgv([]byte("\x00dolt\x00sql-server\x00"))) {
		t.Error("empty argv[0] read as dolt: the next argument shifted into its place")
	}
	if isDoltServerArgv(procArgv([]byte("dolt\x00restart sql-server\x00"))) {
		t.Error("sql-server inside a larger argument read as an exact argument")
	}
	if !isDoltServerArgv(procArgv([]byte("/opt/my tools/dolt\x00sql-server\x00"))) {
		t.Error("space-containing executable path not recognized")
	}
}

// startArgvStandIn starts a /bin/sh blocked on stdin whose argv[0] is argv0 —
// os/exec passes Args as the child's argv verbatim, so a stand-in can present
// any argv[0] while the executable stays a real shell. The returned channel
// closes when the child exits; cleanup closes the pipe, kills, and waits.
func startArgvStandIn(t *testing.T, argv0 string, args ...string) (*exec.Cmd, chan struct{}) {
	t.Helper()
	cmd := exec.Command("/bin/sh", args...)
	cmd.Args[0] = argv0
	cmd.Dir = t.TempDir()
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
	return cmd, exited
}

// TestSweepProcArgvBoundaries exercises the two /proc sites that read a
// process's arguments — gathering and the pre-signal revalidation probe —
// with live children whose argument vectors only a NUL-preserving parser
// can read correctly (#6186). Flattening the NUL-delimited /proc cmdline to
// spaces and re-splitting loses the boundaries these cases pin: an
// executable path containing spaces stops being argv[0], and a word inside
// a larger argument becomes an argument of its own.
func TestSweepProcArgvBoundaries(t *testing.T) {
	t.Run("space-containing executable path is recognized", func(t *testing.T) {
		cmd, exited := startArgvStandIn(t, "/opt/my tools/dolt", "-c", "read line", "sql-server")
		select {
		case <-exited:
			t.Fatal("stand-in exited before its argv could be read")
		default:
		}

		if !isDoltServerProcess(cmd.Process.Pid) {
			t.Error("isDoltServerProcess() = false for an executable path containing spaces, want true")
		}
		var gathered *serverCandidate
		for _, c := range gatherDoltServerCandidates() {
			if c.pid == cmd.Process.Pid {
				candidate := c
				gathered = &candidate
				break
			}
		}
		if gathered == nil {
			t.Fatalf("gatherDoltServerCandidates() did not collect pid %d whose argv[0] is %q", cmd.Process.Pid, "/opt/my tools/dolt")
		}
		if gathered.cwd != cmd.Dir {
			t.Errorf("gathered candidate cwd = %q, want %q", gathered.cwd, cmd.Dir)
		}
	})

	t.Run("sql-server only inside a larger argument is rejected", func(t *testing.T) {
		cmd, exited := startArgvStandIn(t, "dolt", "-c", "read line", "restart sql-server")
		select {
		case <-exited:
			t.Fatal("stand-in exited before its argv could be read")
		default:
		}

		if isDoltServerProcess(cmd.Process.Pid) {
			t.Error("isDoltServerProcess() = true for a child whose only sql-server is inside a larger argument, want false")
		}
		for _, c := range gatherDoltServerCandidates() {
			if c.pid == cmd.Process.Pid {
				t.Fatalf("gatherDoltServerCandidates() collected pid %d whose only sql-server is inside a larger argument", c.pid)
			}
		}
	})
}
