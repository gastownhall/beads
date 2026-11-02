//go:build cgo && unix

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// signalCleanupWatchdog bounds how long bd may take to exit after the first
// signal before the test records the transport as left running.
const signalCleanupWatchdog = 30 * time.Second

// sshTransportFixture routes a git+ssh Dolt remote to a local bare repository
// through a fake ssh named by GIT_SSH_COMMAND. Unarmed, the fake hands the
// connection to the real git upload-pack/receive-pack; armed, it registers
// itself, reports readiness and blocks, standing in for a hung transport.
// A git shim ahead of the real git on PATH registers each git the engine
// starts while the fixture is armed, then execs the real git (same PID).
type sshTransportFixture struct {
	state  string // registry, readiness and arm files
	bin    string // the git shim
	ssh    string // the fake ssh
	remote string // the bare repository
	url    string
}

const sshFixtureHost = "fixture.invalid"

func newSSHTransportFixture(t *testing.T) *sshTransportFixture {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git is required for the git+ssh transport fixture: %v", err)
	}
	fx := &sshTransportFixture{state: t.TempDir(), bin: t.TempDir(), remote: filepath.Join(t.TempDir(), "remote.git")}
	fx.ssh = filepath.Join(fx.bin, "fake-ssh")
	fx.url = "git+ssh://" + sshFixtureHost + fx.remote
	t.Cleanup(fx.killRegistered)

	// The engine refuses a remote without a branch, so give it one.
	seed := t.TempDir()
	for _, args := range [][]string{
		{"init", "--bare", fx.remote},
		{"init", "-b", "main", seed},
		{"-C", seed, "-c", "user.email=test@test.com", "-c", "user.name=Test", "commit", "--allow-empty", "-m", "init"},
		{"-C", seed, "push", fx.remote, "main"},
	} {
		if out, err := gitCommand(args...).CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	armed, registry := shellQuoteForTest(fx.path("armed")), shellQuoteForTest(fx.path("registry"))
	gitShim := fmt.Sprintf(`#!/usr/bin/env bash
if [ -e %[1]s ]; then echo "git $$" >> %[2]s; fi
exec %[3]s "$@"
`, armed, registry, shellQuoteForTest(realGit))
	// git runs: fake-ssh [-o SendEnv=GIT_PROTOCOL] HOST "git-upload-pack '/path'".
	fakeSSH := fmt.Sprintf(`#!/usr/bin/env bash
host="${@: -2:1}"
case "${@: -1}" in
  "git-upload-pack '"%[4]s"'") service=upload-pack ;;
  "git-receive-pack '"%[4]s"'") service=receive-pack ;;
  *) service= ;;
esac
if [ "$host" != %[5]s ] || [ -z "$service" ]; then
  echo "fake-ssh: unexpected request: $*" >> %[6]s
  exit 1
fi
if [ -e %[1]s ]; then
  echo "ssh $$ $PPID $(ps -o pgid= -p $$)" >> %[2]s
  echo ready > %[7]s
  exec sleep 300
fi
exec %[3]s "$service" %[4]s
`, armed, registry, shellQuoteForTest(realGit), shellQuoteForTest(fx.remote), shellQuoteForTest(sshFixtureHost),
		shellQuoteForTest(fx.path("unexpected")), shellQuoteForTest(fx.path("ready")))
	for name, body := range map[string]string{"git": gitShim, "fake-ssh": fakeSSH} {
		if err := os.WriteFile(filepath.Join(fx.bin, name), []byte(body), 0o755); err != nil { // #nosec G306 -- test fixture must be executable
			t.Fatal(err)
		}
	}
	return fx
}

func (fx *sshTransportFixture) path(name string) string { return filepath.Join(fx.state, name) }

// env is bd's environment with the fixture's transport in place and a
// transfer timeout long enough that only the test's signal stops a transfer.
func (fx *sshTransportFixture) env(dir string) []string {
	return append(bdEnv(dir),
		"PATH="+fx.bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GIT_SSH_COMMAND="+fx.ssh,
		"GIT_SSH_VARIANT=ssh",
		"BD_DOLT_REMOTE_TIMEOUT=1h",
	)
}

func (fx *sshTransportFixture) arm(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(fx.path("armed"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (fx *sshTransportFixture) disarm() { _ = os.Remove(fx.path("armed")) }

// waitReady blocks until the armed fake ssh is blocking, failing the test if
// bd exits first or the transport never starts.
func (fx *sshTransportFixture) waitReady(t *testing.T, bdDone <-chan struct{}, output func() string) {
	t.Helper()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	deadline := time.After(2 * time.Minute)
	for {
		if _, err := os.Stat(fx.path("ready")); err == nil {
			return
		}
		select {
		case <-tick.C:
		case <-bdDone:
			t.Fatalf("bd exited before the transport blocked:\n%s%s", output(), fx.unexpected())
		case <-deadline:
			t.Fatalf("the armed transport never started:\n%s%s", output(), fx.unexpected())
		}
	}
}

func (fx *sshTransportFixture) unexpected() string {
	data, _ := os.ReadFile(fx.path("unexpected"))
	return string(data)
}

// registered returns the role and PID of each process the armed fixture
// registered.
func (fx *sshTransportFixture) registered(t *testing.T) map[int]string {
	t.Helper()
	data, err := os.ReadFile(fx.path("registry"))
	if err != nil {
		t.Fatalf("fixture registry: %v", err)
	}
	procs := map[int]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			t.Fatalf("malformed registry line %q", line)
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil || pid <= 0 {
			t.Fatalf("malformed registry line %q", line)
		}
		procs[pid] = line
	}
	return procs
}

// killRegistered kills only the processes this fixture registered, after the
// test has recorded its observations.
func (fx *sshTransportFixture) killRegistered() {
	fx.disarm()
	data, err := os.ReadFile(fx.path("registry"))
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		if fields := strings.Fields(line); len(fields) >= 2 {
			if pid, err := strconv.Atoi(fields[1]); err == nil && pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	}
}

// processRunning reports whether pid is a live, non-zombie process. A probe it
// cannot interpret is an error, never proof of absence.
func processRunning(pid int) (bool, string, error) {
	switch err := syscall.Kill(pid, 0); {
	case errors.Is(err, syscall.ESRCH):
		return false, "gone", nil
	case err != nil:
		return false, "", fmt.Errorf("signal 0 to pid %d: %w", pid, err)
	}
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	stat := strings.TrimSpace(string(out))
	if err != nil {
		if stat == "" && errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return false, "gone", nil
		}
		return false, "", fmt.Errorf("ps for pid %d: %w", pid, err)
	}
	if strings.HasPrefix(stat, "Z") {
		return false, "zombie", nil
	}
	return true, stat, nil
}

// lockedBuffer is an io.Writer safe to read while a child writes to it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestEmbeddedRemoteSignalCleanup pins AC-3 of GH#7989: the first SIGTERM or
// SIGINT delivered to bd during an embedded push or pull whose git+ssh
// transport is blocked cancels the operation, so the pinned engine stops its
// transport, bd exits unsuccessfully, no operation-owned transport process
// is left running, and the same database opens again.
func TestEmbeddedRemoteSignalCleanup(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()
	bd := buildEmbeddedBD(t)

	for _, op := range []string{"push", "pull"} {
		for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
			t.Run(op+"/"+sig.String(), func(t *testing.T) {
				t.Parallel()
				testRemoteSignalCleanup(t, bd, op, sig)
			})
		}
	}
}

func testRemoteSignalCleanup(t *testing.T, bd, op string, sig syscall.Signal) {
	fx := newSSHTransportFixture(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "sg")
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = dir
		cmd.Env = fx.env(dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bd %s: %v\n%s%s", strings.Join(args, " "), err, out, fx.unexpected())
		}
		return string(out)
	}
	run("dolt", "remote", "add", "origin", fx.url)
	seeded := bdCreate(t, bd, dir, "seeded", "--type", "task")
	run("dolt", "push")
	if op == "push" {
		// Leave something to push, so the transfer has work to do.
		bdCreate(t, bd, dir, "unpushed", "--type", "task")
	}

	control := exec.Command("sleep", "300")
	control.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := control.Start(); err != nil {
		t.Fatalf("start control process: %v", err)
	}
	t.Cleanup(func() {
		_ = control.Process.Kill()
		_ = control.Wait()
	})

	fx.arm(t)
	var output lockedBuffer
	cmd := exec.Command(bd, "dolt", op)
	cmd.Dir = dir
	cmd.Env = fx.env(dir)
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatalf("start bd dolt %s: %v", op, err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		fx.disarm()
		_ = cmd.Process.Kill()
		<-done
	})
	fx.waitReady(t, done, output.String)

	if err := cmd.Process.Signal(sig); err != nil {
		t.Fatalf("signal bd: %v", err)
	}
	start := time.Now()
	select {
	case <-done:
	case <-time.After(signalCleanupWatchdog):
	}
	elapsed := time.Since(start)

	// Record every observation before any cleanup signal is sent.
	type observation struct {
		line    string
		running bool
		state   string
		err     error
	}
	var transport []observation
	for pid, line := range fx.registered(t) {
		running, state, err := processRunning(pid)
		transport = append(transport, observation{line, running, state, err})
	}
	controlRunning, controlState, controlErr := processRunning(control.Process.Pid)
	exited := false
	select {
	case <-done:
		exited = true
	default:
	}

	if !exited {
		t.Errorf("bd dolt %s still running %s after %s; output:\n%s", op, signalCleanupWatchdog, sig, output.String())
	} else {
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			t.Errorf("bd dolt %s after %s: wait error %v, want an unsuccessful exit; output:\n%s", op, sig, waitErr, output.String())
		} else {
			t.Logf("bd dolt %s exited %s after %s (%v); output:\n%s", op, elapsed.Round(time.Millisecond), sig, waitErr, output.String())
		}
		if !strings.Contains(output.String(), context.Canceled.Error()) {
			t.Errorf("bd dolt %s did not report the canceled operation after %s; output:\n%s", op, sig, output.String())
		}
	}
	if len(transport) == 0 {
		t.Error("the armed transport registered no process")
	}
	for _, o := range transport {
		switch {
		case o.err != nil:
			t.Errorf("cannot tell whether transport process %q stopped: %v", o.line, o.err)
		case o.running:
			t.Errorf("transport process %q still running (state %s) after bd handled %s", o.line, o.state, sig)
		}
	}
	if controlErr != nil || !controlRunning {
		t.Errorf("control process was disturbed: running=%v state=%q err=%v", controlRunning, controlState, controlErr)
	}
	if !exited {
		return
	}

	fx.disarm()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	reopen := exec.CommandContext(ctx, bd, "list", "--json")
	reopen.Dir = dir
	reopen.Env = bdEnv(dir)
	out, err := reopen.CombinedOutput()
	if err != nil {
		t.Fatalf("reopening the database after the interrupted %s: %v\n%s", op, err, out)
	}
	if !strings.Contains(string(out), seeded.ID) {
		t.Errorf("reopened database does not list %s:\n%s", seeded.ID, out)
	}
}
