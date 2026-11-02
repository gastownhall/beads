//go:build cgo

package main

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestDoltRemoteSIGTERMReapsTransport pins GH#7989's shutdown contract for the
// CLI verbs: `bd dolt push` and `bd dolt pull` run their transfer on the
// signal-aware root context, so the first SIGTERM is observed — bd exits
// unsuccessfully after its cleanup instead of hanging on the detached
// context.Background() it used before — no transport descendant the operation
// spawned survives it, and the database reopens afterwards.
//
// The remote is a git+ssh URL pointed at a listener that accepts and then
// never speaks: the ssh transport connects, waits for a banner that never
// arrives, and the transfer blocks exactly the way a hung remote does.
func TestDoltRemoteSIGTERMReapsTransport(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt shutdown tests")
	}
	if runtime.GOOS == windowsOS {
		t.Skip("process-group teardown assertions are POSIX-only")
	}

	for _, verb := range []string{"push", "pull"} {
		t.Run(verb, func(t *testing.T) {
			bd := buildEmbeddedBD(t)
			home := t.TempDir()
			project := filepath.Join(home, "project")
			if err := os.MkdirAll(project, 0o750); err != nil {
				t.Fatal(err)
			}
			if err := runCommandInDir(project, "git", "init"); err != nil {
				t.Fatalf("git init failed: %v", err)
			}
			_ = runCommandInDir(project, "git", "config", "user.email", "sigterm@example.com")
			_ = runCommandInDir(project, "git", "config", "user.name", "SIGTERM Fixture")

			// The silent remote: accept every connection and never send a
			// byte, so the ssh transport blocks waiting for its banner.
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			accepted := make(chan net.Conn, 8)
			listenerDone := make(chan struct{})
			defer func() {
				// Closing the listener ends the accept loop; the accepted
				// sockets are then closed so a transport still parked on one
				// (a red-run orphan) sees EOF instead of outliving the test.
				_ = ln.Close()
				<-listenerDone
				for {
					select {
					case c := <-accepted:
						_ = c.Close()
						continue
					default:
					}
					break
				}
			}()
			go func() {
				defer close(listenerDone)
				for {
					conn, err := ln.Accept()
					if err != nil {
						return
					}
					select {
					case accepted <- conn:
					default:
					}
				}
			}()

			env := bdEnv(home)
			env = append(env,
				"BD_DOLT_MODE=embedded",
				"DOLT_ROOT_PATH="+home,
				"BD_EVENTS_JOURNAL=false",
				"GIT_TERMINAL_PROMPT=0",
				"SSH_ASKPASS_REQUIRE=never",
				"GIT_SSH_COMMAND=ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes -o ConnectTimeout=10",
			)
			remoteURL := fmt.Sprintf("git+ssh://user@127.0.0.1:%d/repo.git", ln.Addr().(*net.TCPAddr).Port)

			for _, invocation := range [][]string{
				{"init", "--prefix", "sigterm", "--quiet", "--non-interactive", "--skip-agents", "--role", "maintainer"},
				{"create", "survives the aborted push", "--json"},
				{"dolt", "remote", "add", "origin", remoteURL},
			} {
				cmd := exec.Command(bd, invocation...)
				cmd.Dir, cmd.Env = project, env
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("bd %s failed: %v\n%s", strings.Join(invocation, " "), err, out)
				}
			}

			transfer := exec.Command(bd, "dolt", verb)
			transfer.Dir, transfer.Env = project, env
			// The transfer's whole subtree — bd, git, ssh — is a process
			// group of its own so cleanup can reap every descendant.
			transfer.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			var out bytes.Buffer
			transfer.Stdout, transfer.Stderr = &out, &out
			if err := transfer.Start(); err != nil {
				t.Fatalf("start bd dolt %s: %v", verb, err)
			}
			pgid := transfer.Process.Pid
			t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })

			// The transport is blocked once ssh has reached the listener.
			select {
			case c := <-accepted:
				// Requeue: the closed-connection assertions below drain the
				// same channel.
				accepted <- c
			case <-time.After(60 * time.Second):
				t.Fatalf("bd dolt %s never reached the remote transport (output: %s)", verb, out.String())
			}

			// First signal: bd must observe it, unwind, and exit
			// unsuccessfully. No early os.Exit: the cleanup runs first.
			if err := transfer.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatalf("signal bd: %v", err)
			}
			waitErr := make(chan error, 1)
			go func() { waitErr <- transfer.Wait() }()
			select {
			case err := <-waitErr:
				if err == nil {
					t.Fatalf("bd dolt %s exited 0 on SIGTERM (output: %s)", verb, out.String())
				}
			case <-time.After(45 * time.Second):
				t.Fatalf("bd dolt %s still running 45s after SIGTERM; the transfer ignored the root context (output: %s)", verb, out.String())
			}

			// No operation-owned transport descendants: every connection ssh
			// opened must be closed once bd exited.
			drainConns := func() []net.Conn {
				var conns []net.Conn
				for {
					select {
					case c := <-accepted:
						conns = append(conns, c)
						continue
					default:
					}
					break
				}
				return conns
			}
			conns := drainConns()
			if len(conns) == 0 {
				t.Fatal("no transport connection was recorded; the fixture lost the accepted conn")
			}
			for _, conn := range conns {
				// Closure, not silence: an ssh client sends its banner the
				// moment it connects, so data on the socket is expected;
				// only EOF (or reset) proves the transport is gone.
				_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
				buf := make([]byte, 256)
				closed := false
				for {
					_, err := conn.Read(buf)
					if err == nil {
						continue
					}
					var netErr net.Error
					if errors.As(err, &netErr) && netErr.Timeout() {
						break
					}
					closed = true
					break
				}
				if !closed {
					t.Fatalf("a transport connection is still open after bd exited; the ssh child was orphaned (bd output: %s)", out.String())
				}
				_ = conn.Close()
			}

			// The database reopens: a fresh bd command against the same
			// workspace succeeds.
			reopen := exec.Command(bd, "list", "--json")
			reopen.Dir, reopen.Env = project, env
			if roOut, err := reopen.CombinedOutput(); err != nil {
				t.Fatalf("database did not reopen after the aborted %s: %v\n%s", verb, err, roOut)
			}
		})
	}
}
