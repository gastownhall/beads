package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/lockfile"
	"github.com/steveyegge/beads/internal/workspacegate"
)

// failGatesUnsupported makes every gate acquisition fail the way
// workspacegate reports a filesystem that cannot lock (here, Windows' 9P
// redirector reporting busy with no holder) on the set's first gate, and
// counts the calls.
func failGatesUnsupported(t *testing.T) *int {
	t.Helper()
	return failGatesUnsupportedIn(t, "", errors.New("the lock reports busy even on a private file"))
}

// failGatesUnsupportedIn is failGatesUnsupported with the lock answering
// cause, on the first gate of the set whose file sits in dir (the first
// gate when none does). Like workspacegate, the error names that gate.
func failGatesUnsupportedIn(t *testing.T, dir string, cause error) *int {
	t.Helper()
	calls := 0
	old := acquireAllWorkspaceGates
	acquireAllWorkspaceGates = func(_ context.Context, _ workspacegate.Mode, _ workspacegate.Options, gates ...workspacegate.Gate) (*workspacegate.MultiHandle, error) {
		calls++
		failing := gates[0]
		for _, g := range gates {
			if filepath.Dir(g.Path()) == dir {
				failing = g
				break
			}
		}
		return nil, fmt.Errorf("workspacegate: lock %s: %w", failing.Path(),
			&lockfile.UnsupportedError{Op: "probe", Path: failing.Path(), Err: cause})
	}
	t.Cleanup(func() { acquireAllWorkspaceGates = old })
	return &calls
}

// gateFileDir is the directory holding the gate file that guards dir: the
// directory an unsupported-filesystem message names.
func gateFileDir(t *testing.T, dir string) string {
	t.Helper()
	g, err := workspacegate.ForPhysicalRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(g.Path())
}

// An ordinary command on a filesystem that cannot lock runs ungated at once,
// with one warning that says why, instead of waiting out its budget and
// blaming a maintenance operation that is not running.
func TestAcquireCommandWorkspaceGatesUnsupportedFailsOpen(t *testing.T) {
	for _, quiet := range []bool{false, true} {
		t.Run(fmt.Sprintf("quiet=%v", quiet), func(t *testing.T) {
			resetGateTestEnv(t)
			t.Cleanup(releaseWorkspaceGates)
			beadsDir := newGateTestWorkspace(t)
			calls := failGatesUnsupported(t)
			oldQuiet := quietFlag
			quietFlag = quiet
			t.Cleanup(func() { quietFlag = oldQuiet })

			list := &cobra.Command{Use: "list"}
			var err error
			start := time.Now()
			stderr := captureStderr(t, func() {
				err = acquireCommandWorkspaceGates(context.Background(), list, beadsDir)
			})
			if err != nil {
				t.Fatalf("acquireCommandWorkspaceGates = %v, want nil (fail open)", err)
			}
			if waited := time.Since(start); waited > time.Second {
				t.Errorf("took %s, want an immediate return", waited)
			}
			if *calls != 1 {
				t.Errorf("gate acquisition attempted %d times, want once", *calls)
			}
			if workspaceGateHandle != nil {
				t.Error("fail-open path must hold no gate")
			}
			if quiet {
				if stderr != "" {
					t.Errorf("--quiet printed %q, want nothing", stderr)
				}
				return
			}
			if n := strings.Count(stderr, "warning:"); n != 1 {
				t.Errorf("printed %d warnings, want exactly one: %q", n, stderr)
			}
			for _, want := range []string{gateFileDir(t, beadsDir) + " is on a filesystem that does not support file locking", "continuing ungated"} {
				if !strings.Contains(stderr, want) {
					t.Errorf("warning %q missing %q", stderr, want)
				}
			}
		})
	}
}

// ENOLCK is also what a kernel out of lock records answers, so the warning
// names the failure ("no locks available"), not a filesystem that cannot lock.
func TestAcquireCommandWorkspaceGatesNoLocksWarning(t *testing.T) {
	resetGateTestEnv(t)
	t.Cleanup(releaseWorkspaceGates)
	beadsDir := newGateTestWorkspace(t)
	failGatesUnsupportedIn(t, "", syscall.ENOLCK)
	oldQuiet := quietFlag
	quietFlag = false
	t.Cleanup(func() { quietFlag = oldQuiet })

	var err error
	stderr := captureStderr(t, func() {
		err = acquireCommandWorkspaceGates(context.Background(), &cobra.Command{Use: "list"}, beadsDir)
	})
	if err != nil {
		t.Fatalf("acquireCommandWorkspaceGates = %v, want nil (fail open)", err)
	}
	if want := "file locking failed in " + gateFileDir(t, beadsDir) + " (" + syscall.ENOLCK.Error() + ")"; !strings.Contains(stderr, want) {
		t.Errorf("warning %q missing %q", stderr, want)
	}
	if strings.Contains(stderr, "does not support file locking") {
		t.Errorf("warning %q calls ENOLCK a filesystem that cannot lock", stderr)
	}
}

// Maintenance refuses on a filesystem that cannot lock, as it did before,
// but says so instead of claiming live bd activity or a busy workspace, and
// names a remedy that can work instead of waiting.
func TestMaintenanceRefusesUnsupportedFilesystem(t *testing.T) {
	// assertRefusal checks that msg refuses op naming the filesystem that
	// holds gateDir and the remedy, and blames no other bd command.
	assertRefusal := func(t *testing.T, msg, op, gateDir string) {
		t.Helper()
		for _, want := range []string{op + " cannot run here", gateDir + " is on a filesystem that does not support file locking", gateLockUnsupportedHint} {
			if !strings.Contains(msg, want) {
				t.Errorf("refusal %q missing %q", msg, want)
			}
		}
		for _, blame := range []string{"bd activity", "wait for running bd commands"} {
			if strings.Contains(msg, blame) {
				t.Errorf("refusal %q blames other bd commands (%q)", msg, blame)
			}
		}
	}

	t.Run("bd init", func(t *testing.T) {
		resetGateTestEnv(t)
		beadsDir := newGateTestWorkspace(t)
		// The database root is on another filesystem than the workspace,
		// and only its gate cannot lock: the refusal names that filesystem.
		physicalRoot := filepath.Join(t.TempDir(), "dolt-data")
		rootGateDir := gateFileDir(t, physicalRoot)
		failGatesUnsupportedIn(t, rootGateDir, errors.New("the lock reports busy even on a private file"))

		preflightRan := false
		h, err := acquireInitMutationGate(context.Background(), beadsDir, physicalRoot, func() error {
			preflightRan = true
			return nil
		})
		if err == nil {
			_ = h.Release()
			t.Fatal("bd init acquired its gates on a filesystem that cannot lock")
		}
		if !errors.Is(err, lockfile.ErrUnsupported) {
			t.Errorf("error %v does not wrap lockfile.ErrUnsupported", err)
		}
		assertRefusal(t, err.Error(), "bd init", rootGateDir)
		if wsDir := gateFileDir(t, beadsDir); strings.Contains(err.Error(), wsDir) {
			t.Errorf("error %q names the workspace directory %s, not the root that cannot lock", err, wsDir)
		}
		if preflightRan {
			t.Error("preflight ran without the gates")
		}
	})

	t.Run("bd backup restore", func(t *testing.T) {
		resetGateTestEnv(t)
		t.Cleanup(releaseWorkspaceGates)
		beadsDir := newGateTestWorkspace(t)
		failGatesUnsupported(t)
		root := &cobra.Command{Use: "bd"}
		backup := &cobra.Command{Use: "backup"}
		restore := &cobra.Command{Use: "restore [path]"}
		backup.AddCommand(restore)
		root.AddCommand(backup)
		if !commandNeedsExclusiveGate(restore) {
			t.Fatal("backup restore must take the exclusive gate")
		}

		var err error
		stderr := captureStderr(t, func() {
			err = acquireCommandWorkspaceGates(context.Background(), restore, beadsDir)
		})
		if err == nil {
			t.Fatal("backup restore proceeded on a filesystem that cannot lock")
		}
		assertRefusal(t, stderr, "bd backup restore", gateFileDir(t, beadsDir))
		if workspaceGateHandle != nil {
			t.Error("refused restore must hold no gate")
		}
	})

	t.Run("bd migrate", func(t *testing.T) {
		resetGateTestEnv(t)
		beadsDir := newGateTestWorkspace(t)
		calls := failGatesUnsupported(t)
		oldJSON := jsonOutput
		jsonOutput = false
		t.Cleanup(func() { jsonOutput = oldJSON })

		var release func()
		var err error
		stderr := captureStderr(t, func() {
			release, err = acquireMigrateGates(beadsDir, false, "test migrate")
		})
		if err == nil {
			release()
			t.Fatal("bd migrate acquired its gates on a filesystem that cannot lock")
		}
		if *calls != 1 {
			t.Errorf("gate acquisition attempted %d times, want once", *calls)
		}
		assertRefusal(t, stderr, "bd migrate", gateFileDir(t, beadsDir))
	})

	t.Run("bd bootstrap", func(t *testing.T) {
		resetGateTestEnv(t)
		beadsDir := newGateTestWorkspace(t)
		calls := failGatesUnsupported(t)

		err := executeBootstrapPlan(BootstrapPlan{Action: "init", BeadsDir: beadsDir, Database: "beads"}, nil, true)
		if err == nil {
			t.Fatal("bd bootstrap ran on a filesystem that cannot lock")
		}
		if *calls != 1 {
			t.Fatalf("gate acquisition attempted %d times, want once (refused before the gate: %v)", *calls, err)
		}
		if !errors.Is(err, lockfile.ErrUnsupported) {
			t.Errorf("error %v does not wrap lockfile.ErrUnsupported", err)
		}
		assertRefusal(t, err.Error(), "bd bootstrap", gateFileDir(t, beadsDir))
	})
}
