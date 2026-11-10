package main

import (
	"bufio"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"
)

// Shared plumbing for the tests that run `bd serve` IN-PROCESS.
//
// In-process is not a convenience here: a registered backend is init-time Go
// wiring and OSS registers none, so a spawned `bd` has nothing to register and
// could not reach that arm at all. What that costs is a bound server and a set
// of package globals inside the test binary every other test shares, which is
// what the snapshot-and-restore below exists for.
//
// None of it is cgo-specific, so it lives outside the embedded-Dolt end-to-end
// file that first needed it.

// restoreServeGlobals snapshots the package state one in-process serve run can
// touch and puts it back afterwards, so a registered backend and a bound server
// cannot leak into the tests sharing this binary.
//
// The flag set is part of that state and the least obvious part of it: cobra
// merges every inherited persistent flag into serveCmd's own FlagSet the first
// time it parses one, so a run through rootCmd.Execute leaves `bd serve`
// carrying --json, --db and the rest of the root's surface. That is what
// TestServeFlags reads. ResetFlags plus the command's own registration function
// is the un-merge cobra does not offer.
func restoreServeGlobals(t *testing.T) {
	t.Helper()
	origStore, origDBPath := store, dbPath
	origServer, origProxied := serverMode, proxiedServerMode
	origAddr, origNonLoopback := serveAddr, serveAllowNonLoopback
	origCtx, origCancel := rootCtx, rootCancel
	origCmdCtx, origUseGlobals := cmdCtx, testModeUseGlobals
	t.Cleanup(func() {
		if store != nil && store != origStore {
			store.Close()
		}
		serveCmd.ResetFlags()
		registerServeFlags(serveCmd) // rebinds serveAddr/serveAllowNonLoopback to the defaults
		store, dbPath = origStore, origDBPath
		serverMode, proxiedServerMode = origServer, origProxied
		serveAddr, serveAllowNonLoopback = origAddr, origNonLoopback
		rootCtx, rootCancel = origCtx, origCancel
		cmdCtx, testModeUseGlobals = origCmdCtx, origUseGlobals
		rootCmd.SetArgs(nil)
	})
}

// resetRootPersistentFlags puts every root persistent flag back to the default
// it was declared with, and clears its Changed bit, for the duration of one
// test.
//
// A test binary that runs the root command in-process inherits whatever the
// thousands of tests before it left in those flags and their bound globals —
// and `Changed` is what several PersistentPreRunE branches dispatch on, not the
// value. A stale `--db`/`--database` alone makes the pre-run refuse with
// "--database ... is only supported in proxied-server mode" before the command
// under test ever runs. Reset before, restore after, so this test neither reads
// nor writes that shared state.
func resetRootPersistentFlags(t *testing.T) {
	t.Helper()
	type flagState struct {
		value   string
		changed bool
	}
	before := map[string]flagState{}
	rootCmd.PersistentFlags().VisitAll(func(f *pflag.Flag) {
		before[f.Name] = flagState{value: f.Value.String(), changed: f.Changed}
		if err := f.Value.Set(f.DefValue); err != nil {
			t.Fatalf("reset --%s to its default %q: %v", f.Name, f.DefValue, err)
		}
		f.Changed = false
	})
	t.Cleanup(func() {
		rootCmd.PersistentFlags().VisitAll(func(f *pflag.Flag) {
			state, ok := before[f.Name]
			if !ok {
				return
			}
			_ = f.Value.Set(state.value)
			f.Changed = state.changed
		})
	})
}

// captureStdoutLines redirects os.Stdout and streams its lines. bd serve prints
// the address it bound — the only way to discover an ephemeral port — to
// stdout, and the server is running by the time it does.
func captureStdoutLines(t *testing.T) (<-chan string, func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w

	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()

	var stopped bool
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		os.Stdout = orig
		_ = w.Close()
	}
	t.Cleanup(func() {
		stop()
		_ = r.Close()
	})
	return lines, stop
}

// waitForBoundAddress reads the address `bd serve` printed on stdout, which is
// the only way to discover the port under the ephemeral default. The server is
// already accepting by the time that line is written.
//
// done carries the run's result so a serve that failed before binding is
// reported as its own error rather than as a two-minute timeout.
func waitForBoundAddress(t *testing.T, lines <-chan string, done <-chan error) string {
	t.Helper()
	deadline := time.After(2 * time.Minute)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("bd serve exited before it bound: %v", <-done)
			}
			const prefix = "bd serve: listening on http://"
			if addr, found := strings.CutPrefix(strings.TrimSpace(line), prefix); found {
				return addr
			}
		case <-deadline:
			t.Fatal("bd serve did not print a bound address")
		}
	}
}
