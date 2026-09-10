package hooks

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// The async path's two promises, which fire-and-forget does NOT include.
//
// "The mutation never fails because a hook did" is what fire-and-forget buys.
// It does not buy "the hook ran": a bd command fires its hooks after the commit
// and returns, and the process exit takes a goroutine that has not reached exec
// yet. Nor does it buy "the command still ends": a script that blocks forever
// would hold the exit forever if the wait were unbounded.

func skipWithoutShellHooks(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		// Same reason as TestRun_Async: no shebang dispatch on Windows.
		t.Skip("hook script execution not supported on Windows - see GH#3800")
	}
}

func writeHook(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0755); err != nil {
		t.Fatalf("write hook: %v", err)
	}
}

// TestWaitLetsAFiredHookFinish is the regression for the hook a short-lived
// command loses at exit: Run returns before the script has done anything, and
// Wait is what makes its effect observable.
func TestWaitLetsAFiredHookFinish(t *testing.T) {
	skipWithoutShellHooks(t)

	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	writeHook(t, dir, HookOnUpdate, "#!/bin/sh\nsleep 0.2\necho fired > \""+marker+"\"\n")

	runner := NewRunner(dir)
	runner.Run(EventUpdate, &types.Issue{ID: "bd-1"})

	if !runner.Wait(10 * time.Second) {
		t.Fatal("Wait timed out on a hook that sleeps 0.2s")
	}
	if _, err := os.ReadFile(marker); err != nil {
		t.Fatalf("hook had not run when Wait returned: %v", err)
	}
}

// TestWaitIsBounded pins the other half: a hook that outlives the budget must
// not hold the command open, and Wait says so rather than blocking.
func TestWaitIsBounded(t *testing.T) {
	skipWithoutShellHooks(t)

	dir := t.TempDir()
	writeHook(t, dir, HookOnUpdate, "#!/bin/sh\nsleep 30\n")

	runner := NewRunner(dir)
	runner.Run(EventUpdate, &types.Issue{ID: "bd-1"})

	start := time.Now()
	if runner.Wait(150 * time.Millisecond) {
		t.Fatal("Wait reported a 30s hook as finished")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Wait took %s for a 150ms budget", elapsed)
	}
}

// TestWaitReturnsWithNothingInFlight covers the common command: it fired no
// hook, or has no hooks directory, and teardown must not pause for it.
func TestWaitReturnsWithNothingInFlight(t *testing.T) {
	runner := NewRunner(t.TempDir())
	start := time.Now()
	if !runner.Wait(10 * time.Second) {
		t.Fatal("Wait timed out with nothing in flight")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Wait took %s with nothing in flight", elapsed)
	}
	// A skipped hook — the directory has no script — must not leave the group
	// held either, or every command would pay the full budget at teardown.
	runner.Run(EventUpdate, &types.Issue{ID: "bd-1"})
	if !runner.Wait(2 * time.Second) {
		t.Fatal("Wait timed out after a hook that did not exist")
	}
}

// TestRunEnforcesThePerHookTimeout pins that the ASYNC path is under the same
// budget the synchronous one is — both call runHook, which owns the timeout and
// the process-group kill. Without it a wedged script would sit in the group
// Wait blocks on, and the bound above would be the only thing between a hung
// hook and a hung command.
func TestRunEnforcesThePerHookTimeout(t *testing.T) {
	skipWithoutShellHooks(t)

	dir := t.TempDir()
	writeHook(t, dir, HookOnUpdate, "#!/bin/sh\nsleep 30\n")

	runner := NewRunner(dir)
	runner.timeout = 200 * time.Millisecond
	runner.Run(EventUpdate, &types.Issue{ID: "bd-1"})

	if !runner.Wait(10 * time.Second) {
		t.Fatal("the async hook outlived its own 200ms timeout: Run does not enforce it")
	}
}
