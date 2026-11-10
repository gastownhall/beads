//go:build windows

package execwin

// End-to-end Windows evidence for the console-spawn policy (GH#6076, AC-6):
// real processes, real consoles, no probe-variable overrides.
//
// The test re-executes its own binary in four roles, selected by
// EXECWIN_SPAWN_MODE:
//
//	launcher-hidden   started DETACHED_PROCESS by the test (a console-less
//	                  bd, as an MCP server or hook gets), it starts the
//	                  Hide-wrapped child and relays its output.
//	hidden-child      started by the console-less launcher through
//	                  execwin.Hide, it must end up attached to a hidden
//	                  console, prove a full pipe exchange with a
//	                  descendant, and relay the descendant's report.
//	console-probe     the descendant: reports whether it has a console and
//	                  whether that console's window is visible.
//	launcher-pinned   console-less like launcher-hidden; starts the
//	                  background-pinned child and exits immediately.
//	pinned-child      started with execwin.BackgroundSysProcAttr and an
//	                  inherited relay pipe (fd 3); reports, survives its
//	                  launcher's exit, probes its own console, reports
//	                  again, and exits.
//
// The discriminating assertion is PROBE-VISIBLE-0: without the policy, a
// console-subsystem child of a console-less parent (and any descendant it
// spawns) gets a brand-new visible console. With Hide or the pinned
// attributes, the console exists but stays hidden.
//
// Off Windows none of this can run; the file is windows-only and the
// runtime evidence is produced by a Windows host. Every pipe exchange is
// bounded, and a failed test still kills the pinned child it started.

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const (
	execwinSpawnMode = "EXECWIN_SPAWN_MODE"
	// spawnRunArgs keeps a re-executed test binary to this test only, so
	// the mode dispatch below runs and nothing else does.
	spawnRunArgs = "-test.run=^TestConsoleSpawnSubprocess$"
	// spawnDeadline bounds every subprocess exchange; Windows CI machines
	// can be slow, and a hang must fail the test, not the runner.
	spawnDeadline = 30 * time.Second
	// pinnedSurvivalDelay is how long the pinned child outlives its
	// launcher before reporting: past any plausible launcher teardown.
	pinnedSurvivalDelay = 1500 * time.Millisecond
)

func TestConsoleSpawnSubprocess(t *testing.T) {
	switch mode := os.Getenv(execwinSpawnMode); mode {
	case "":
		// The test run itself; the assertions are below.
	case "launcher-hidden":
		launcherHiddenMain()
		os.Exit(0)
	case "hidden-child":
		hiddenChildMain()
		os.Exit(0)
	case "console-probe":
		consoleProbeMain()
		os.Exit(0)
	case "launcher-pinned":
		launcherPinnedMain()
		os.Exit(0)
	case "pinned-child":
		pinnedChildMain()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown %s %q\n", execwinSpawnMode, mode)
		os.Exit(2)
	}

	t.Run("HideWrappedChild", func(t *testing.T) {
		out, err := runSpawnBounded(t, spawnLauncherCmd(t, "launcher-hidden", nil))
		if err != nil {
			t.Fatalf("console-less launcher run failed: %v\noutput:\n%s", err, out)
		}
		for _, want := range []string{
			"LAUNCHER-ATTACHED-0",      // the launcher is genuinely console-less
			"CHILD-ATTACHED-1",         // Hide gave the wrapped child a console
			"HIDDEN-CHILD-EXCHANGE-OK", // a full pipe exchange completed
			"PROBE-CONSOLE-1",          // the descendant has a console...
			"PROBE-VISIBLE-0",          // ...and it is not a visible window
			"LAUNCHER-HIDDEN-CHILD-DONE",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("launcher output missing %q (GH#6076: the child's console would be visible)\noutput:\n%s", want, out)
			}
		}
	})

	t.Run("PinnedChildSurvivesParent", func(t *testing.T) {
		relayRead, relayWrite, err := os.Pipe()
		if err != nil {
			t.Fatalf("relay pipe: %v", err)
		}
		defer relayRead.Close()
		launcher := spawnLauncherCmd(t, "launcher-pinned", []*os.File{relayWrite})
		out, err := runSpawnBounded(t, launcher)
		if err != nil {
			relayWrite.Close()
			t.Fatalf("console-less launcher run failed: %v\noutput:\n%s", err, out)
		}
		for _, want := range []string{"PINNED-STARTED", "LAUNCHER-EXITING"} {
			if !strings.Contains(out, want) {
				relayWrite.Close()
				t.Fatalf("launcher output missing %q\noutput:\n%s", want, out)
			}
		}
		// The launcher has exited; drop this end's copy of the write side
		// so the relay EOFs when (and only when) the pinned child exits.
		relayWrite.Close()

		relay := readBounded(t, relayRead)
		pid := 0
		for _, want := range []string{
			"PINNED-READY", // the pinned child is attached to a hidden console
			"PINNED-ALIVE", // ...and still alive well after its launcher exited
			"PROBE-CONSOLE-1",
			"PROBE-VISIBLE-0",
			"PINNED-DONE",
		} {
			if !strings.Contains(relay, want) {
				t.Errorf("pinned child relay missing %q\nrelay:\n%s", want, relay)
			}
		}
		for _, line := range strings.Split(relay, "\n") {
			if fields := strings.Fields(line); len(fields) == 2 && fields[0] == "PINNED-READY" {
				if pid, err = strconv.Atoi(fields[1]); err != nil {
					t.Fatalf("bad PINNED-READY pid %q", fields[1])
				}
			}
		}
		// Cleanup of a failed run: PINNED-DONE means the child exited on
		// its own; without it (or on any error path above) kill the child
		// so a broken test leaves no orphan behind.
		if pid != 0 && !strings.Contains(relay, "PINNED-DONE") {
			if proc, err := os.FindProcess(pid); err == nil {
				_ = proc.Kill()
			}
		}
	})
}

// spawnLauncherCmd builds the console-less launcher process: the test
// binary re-executed in the given mode, started DETACHED_PROCESS (no
// console at all - the state a console-less bd is really in, stricter than
// a hidden one), optionally inheriting extra files (the pinned child's
// relay pipe on fd 3).
func spawnLauncherCmd(t *testing.T, mode string, extra []*os.File) *exec.Cmd {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("test binary path: %v", err)
	}
	cmd := exec.Command(self, spawnRunArgs, "-test.timeout=60s")
	cmd.Env = append(os.Environ(), execwinSpawnMode+"="+mode)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS}
	cmd.ExtraFiles = extra
	return cmd
}

// runSpawnBounded runs cmd with a deadline and returns its combined
// output. A hang is a failed test, never a stuck runner.
func runSpawnBounded(t *testing.T, cmd *exec.Cmd) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return buf.String(), err
	case <-time.After(spawnDeadline):
		_ = cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(spawnDeadline):
			t.Fatalf("subprocess ignored kill for %v; output:\n%s", spawnDeadline, buf.String())
		}
		return buf.String(), fmt.Errorf("timed out after %v", spawnDeadline)
	}
}

// readBounded drains r with a deadline.
func readBounded(t *testing.T, r io.Reader) string {
	t.Helper()
	type result struct {
		data string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := io.ReadAll(r)
		done <- result{string(data), err}
	}()
	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("reading relay pipe: %v", res.err)
		}
		return res.data
	case <-time.After(spawnDeadline):
		t.Fatalf("pinned child relay silent for %v", spawnDeadline)
		return ""
	}
}

// spawnChild builds a re-executed test binary in the given mode. The
// construction is deliberately ungated by the testing framework: the child
// reports through its own pipes, not through test assertions.
func spawnChild(mode string) *exec.Cmd {
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "test binary path:", err)
		os.Exit(1)
	}
	cmd := exec.Command(self, spawnRunArgs, "-test.timeout=60s")
	cmd.Env = append(os.Environ(), execwinSpawnMode+"="+mode)
	return cmd
}

func launcherHiddenMain() {
	if attachedToConsole() {
		fmt.Fprintln(os.Stderr, "launcher-hidden: expected to start console-less (DETACHED_PROCESS)")
		os.Exit(1)
	}
	fmt.Println("LAUNCHER-ATTACHED-0")
	child := Hide(spawnChild("hidden-child"))
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err := child.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "launcher-hidden: hide-wrapped child failed:", err)
		os.Exit(1)
	}
	fmt.Println("LAUNCHER-HIDDEN-CHILD-DONE")
}

func hiddenChildMain() {
	// The launcher has no console; Hide must have given this child a
	// hidden console of its own, so the attachment probe flips true.
	if !attachedToConsole() {
		fmt.Fprintln(os.Stderr, "hidden-child: no console; Hide did not add CREATE_NO_WINDOW")
		os.Exit(1)
	}
	fmt.Println("CHILD-ATTACHED-1")
	// The full pipe exchange with a descendant: start, read its report to
	// completion, relay it.
	probe := Hide(spawnChild("console-probe"))
	out, err := probe.Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "hidden-child: console probe failed:", err)
		os.Exit(1)
	}
	fmt.Println("HIDDEN-CHILD-EXCHANGE-OK")
	fmt.Print(string(out))
}

// x/sys/windows wraps neither GetConsoleWindow (kernel32) nor
// IsWindowVisible (user32), so the probe resolves them itself.
var (
	procGetConsoleWindow = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow")
	procIsWindowVisible  = windows.NewLazySystemDLL("user32.dll").NewProc("IsWindowVisible")
)

func consoleProbeMain() {
	hwnd, _, _ := procGetConsoleWindow.Call()
	if hwnd == 0 {
		fmt.Println("PROBE-CONSOLE-0")
		os.Exit(1)
	}
	visible, _, _ := procIsWindowVisible.Call(hwnd)
	if visible != 0 {
		fmt.Println("PROBE-CONSOLE-1 PROBE-VISIBLE-1")
		os.Exit(1)
	}
	fmt.Println("PROBE-CONSOLE-1 PROBE-VISIBLE-0")
}

func launcherPinnedMain() {
	if attachedToConsole() {
		fmt.Fprintln(os.Stderr, "launcher-pinned: expected to start console-less (DETACHED_PROCESS)")
		os.Exit(1)
	}
	relay := os.NewFile(3, "relay")
	child := spawnChild("pinned-child")
	child.SysProcAttr = BackgroundSysProcAttr()
	child.ExtraFiles = []*os.File{relay}
	if err := child.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "launcher-pinned: pinned child failed to start:", err)
		os.Exit(1)
	}
	fmt.Println("PINNED-STARTED")
	fmt.Println("LAUNCHER-EXITING")
	// Exit without waiting: the pinned child surviving this is the point.
	os.Exit(0)
}

func pinnedChildMain() {
	relay := os.NewFile(3, "relay")
	if !attachedToConsole() {
		fmt.Fprintln(relay, "PINNED-ERROR no console; BackgroundSysProcAttr must pin a hidden one")
		os.Exit(1)
	}
	fmt.Fprintf(relay, "PINNED-READY %d\n", os.Getpid())
	// The launcher exited right after Start; outlive it by a wide margin,
	// then report - surviving that window is the GH#6076 requirement for
	// long-lived children.
	time.Sleep(pinnedSurvivalDelay)
	fmt.Fprintf(relay, "PINNED-ALIVE %d\n", os.Getpid())
	probe := Hide(spawnChild("console-probe"))
	out, err := probe.Output()
	if err != nil {
		fmt.Fprintf(relay, "PINNED-ERROR probe failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(relay, "PINNED-PROBE %s", string(out))
	fmt.Fprintln(relay, "PINNED-DONE")
}
