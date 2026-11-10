//go:build windows

package execwin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// The subprocess test re-executes this test binary in a role named by
// subprocessRoleEnv. TestMain runs the role instead of the tests, and each
// role prints one JSON report line on stdout:
//
//	test (console or not)
//	└─ launcher, DETACHED_PROCESS: no console, like bd under an editor or agent
//	   ├─ hidden child, execwin.Hide           ─ descendant, plain exec.Command
//	   └─ background child, BackgroundSysProcAttr ─ descendant, plain exec.Command
//
// Each child and each descendant reports whether it has a console and whether
// that console has a visible window. The descendants are ordinary console
// children of the child, so they show whether the hidden console bd gave the
// child is inherited instead of a fresh, visible one being allocated.
const subprocessRoleEnv = "BEADS_EXECWIN_TEST_ROLE"

const (
	roleLauncher   = "launcher"
	roleHidden     = "hidden-child"
	roleBackground = "background-child"
	roleDescendant = "descendant"
)

// subprocessTimeout bounds every pipe exchange and child wait. The background
// child also exits on its own after backgroundLifetime if nothing kills it.
const (
	subprocessTimeout  = time.Minute
	backgroundLifetime = 2 * time.Minute
)

// consoleReport is what each child and descendant says about itself.
type consoleReport struct {
	Role       string         `json:"role"`
	PID        int            `json:"pid"`
	Attached   bool           `json:"attached"` // GetConsoleCP != 0
	Window     bool           `json:"window"`   // GetConsoleWindow != NULL
	Visible    bool           `json:"visible"`  // IsWindowVisible(GetConsoleWindow())
	Descendant *consoleReport `json:"descendant,omitempty"`
	Err        string         `json:"err,omitempty"`
}

// launcherReport is the launcher's own state plus its children's reports.
type launcherReport struct {
	Attached   bool          `json:"attached"`
	Hidden     consoleReport `json:"hidden"`
	Background consoleReport `json:"background"`
	Err        string        `json:"err,omitempty"`
}

var (
	procGetConsoleWindow = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow")
	procIsWindowVisible  = windows.NewLazySystemDLL("user32.dll").NewProc("IsWindowVisible")
)

func TestMain(m *testing.M) {
	if role := os.Getenv(subprocessRoleEnv); role != "" {
		os.Exit(runSubprocessRole(role))
	}
	os.Exit(m.Run())
}

// TestConsoleSpawnSubprocessHidesDescendantConsoles is AC-6: from a launcher
// with no console, a Hide-wrapped child and a background-pinned child each
// get a console without a visible window, their console descendants inherit
// it rather than opening windows of their own, and the background child
// outlives the launcher that started it.
func TestConsoleSpawnSubprocessHidesDescendantConsoles(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), subprocessTimeout)
	defer cancel()
	launcher, err := roleCommand(ctx, roleLauncher)
	if err != nil {
		t.Fatal(err)
	}
	launcher.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS}
	out, err := launcher.Output()
	if err != nil {
		t.Fatalf("launcher: %v\nstdout: %s", err, out)
	}
	var rep launcherReport
	if err := json.Unmarshal(out, &rep); err != nil {
		t.Fatalf("launcher report %q: %v", out, err)
	}
	if rep.Background.PID != 0 {
		h := openBackgroundChild(t, rep.Background.PID)
		// The launcher has exited, so this is the child outliving its
		// immediate parent: still running half a second later.
		if ev, err := windows.WaitForSingleObject(h, 500); err != nil || ev != uint32(windows.WAIT_TIMEOUT) {
			t.Errorf("background child %d exited with its launcher (wait = %#x, %v); want it alive", rep.Background.PID, ev, err)
		}
	}
	if rep.Err != "" {
		t.Fatalf("launcher: %s", rep.Err)
	}
	if rep.Attached {
		t.Fatal("launcher has a console although it was started with DETACHED_PROCESS; the test proves nothing")
	}
	checkHiddenConsole(t, rep.Hidden, roleHidden)
	checkHiddenConsole(t, rep.Background, roleBackground)
}

// checkHiddenConsole asserts that r and its descendant both completed their
// exchange and sit on a console with no visible window.
func checkHiddenConsole(t *testing.T, r consoleReport, role string) {
	t.Helper()
	if r.Err != "" {
		t.Errorf("%s: %s", role, r.Err)
		return
	}
	if r.Role != role {
		t.Errorf("%s: report came from role %q", role, r.Role)
	}
	if !r.Attached || r.Visible {
		t.Errorf("%s: attached=%v visible=%v; want a console without a visible window", role, r.Attached, r.Visible)
	}
	d := r.Descendant
	switch {
	case d == nil:
		t.Errorf("%s: no report from its console descendant", role)
	case d.Err != "":
		t.Errorf("%s descendant: %s", role, d.Err)
	case !d.Attached || d.Visible:
		t.Errorf("%s descendant: attached=%v visible=%v; want it to inherit the hidden console", role, d.Attached, d.Visible)
	}
}

// openBackgroundChild opens the background child for the liveness check and
// registers its termination, so it never outlives the test.
func openBackgroundChild(t *testing.T, pid int) windows.Handle {
	t.Helper()
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		t.Fatalf("open background child %d: %v (it may already have exited)", pid, err)
	}
	t.Cleanup(func() {
		_ = windows.TerminateProcess(h, 1)
		_, _ = windows.WaitForSingleObject(h, uint32(subprocessTimeout/time.Millisecond))
		_ = windows.CloseHandle(h)
	})
	return h
}

// roleCommand re-executes the test binary in role. No test runs in it:
// TestMain dispatches on the role first.
func roleCommand(ctx context.Context, role string) (*exec.Cmd, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, self, "-test.run=^$")
	cmd.Env = roleEnv(role)
	cmd.WaitDelay = 10 * time.Second
	return cmd, nil
}

// roleEnv is this process's environment with the role replaced by role.
func roleEnv(role string) []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, subprocessRoleEnv+"=") {
			env = append(env, kv)
		}
	}
	return append(env, subprocessRoleEnv+"="+role)
}

func runSubprocessRole(role string) int {
	var v any
	switch role {
	case roleLauncher:
		v = runLauncher()
	case roleHidden, roleBackground, roleDescendant:
		v = selfReport(role)
	default:
		v = consoleReport{Role: role, Err: "unknown role"}
	}
	if err := json.NewEncoder(os.Stdout).Encode(v); err != nil {
		return 1
	}
	if role == roleBackground {
		// Stay alive for the liveness check; the test terminates us, and
		// this bound keeps a stray child from living on if it cannot.
		time.Sleep(backgroundLifetime)
	}
	return 0
}

// selfReport describes this process's console and, for a child, that of a
// plain console descendant it starts and waits for.
func selfReport(role string) consoleReport {
	r := consoleReport{Role: role, PID: os.Getpid()}
	r.Attached = consoleAttached()
	if hwnd, _, _ := procGetConsoleWindow.Call(); hwnd != 0 {
		r.Window = true
		visible, _, _ := procIsWindowVisible.Call(hwnd)
		r.Visible = visible != 0
	}
	if role == roleDescendant {
		return r
	}
	ctx, cancel := context.WithTimeout(context.Background(), subprocessTimeout)
	defer cancel()
	cmd, err := roleCommand(ctx, roleDescendant)
	if err != nil {
		r.Err = err.Error()
		return r
	}
	d, err := readReport[consoleReport](cmd, false)
	if err != nil {
		r.Err = "descendant: " + err.Error()
		return r
	}
	r.Descendant = &d
	return r
}

// runLauncher starts both children from a process without a console. It
// waits for the hidden child but not the background one, which it leaves
// running when it exits.
func runLauncher() launcherReport {
	rep := launcherReport{Attached: consoleAttached()}
	ctx, cancel := context.WithTimeout(context.Background(), subprocessTimeout)
	defer cancel()

	cmd, err := roleCommand(ctx, roleHidden)
	if err != nil {
		rep.Err = err.Error()
		return rep
	}
	hidden := Hide(cmd)
	if hidden.SysProcAttr == nil || hidden.SysProcAttr.CreationFlags&createNoWindow == 0 {
		rep.Err = "Hide did not add CREATE_NO_WINDOW in a process without a console"
		return rep
	}
	if rep.Hidden, err = readReport[consoleReport](hidden, false); err != nil {
		rep.Err = "hidden child: " + err.Error()
		return rep
	}

	// Not CommandContext: the child must outlive this process, and so this
	// context too.
	self, err := os.Executable()
	if err != nil {
		rep.Err = err.Error()
		return rep
	}
	background := exec.Command(self, "-test.run=^$")
	background.Env = roleEnv(roleBackground)
	background.SysProcAttr = BackgroundSysProcAttr()
	if rep.Background, err = readReport[consoleReport](background, true); err != nil {
		rep.Err = "background child: " + err.Error()
	}
	return rep
}

// readReport starts cmd and decodes the first line it prints. Unless detach
// is set it then waits for cmd; a detached cmd is left running.
func readReport[T any](cmd *exec.Cmd, detach bool) (T, error) {
	var v T
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return v, err
	}
	if err := cmd.Start(); err != nil {
		return v, err
	}
	type result struct {
		line string
		err  error
	}
	got := make(chan result, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		got <- result{line, err}
	}()
	var res result
	select {
	case res = <-got:
	case <-time.After(subprocessTimeout):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return v, fmt.Errorf("no report within %v", subprocessTimeout)
	}
	if res.err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return v, fmt.Errorf("read report: %w", res.err)
	}
	if err := json.Unmarshal([]byte(res.line), &v); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return v, fmt.Errorf("decode report %q: %w", res.line, err)
	}
	if detach {
		return v, cmd.Process.Release()
	}
	return v, cmd.Wait()
}
