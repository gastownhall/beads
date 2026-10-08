package main

import (
	"strings"
	"testing"
)

// pinProxiedServerMode routes usesProxiedServer() down the proxied branch for
// the duration of one test. resetCommandContext() is what makes the global
// authoritative: usesProxiedServer prefers cmdCtx whenever an earlier test left
// one behind, so setting the global alone is not enough to pin the route.
func pinProxiedServerMode(t *testing.T) {
	t.Helper()
	restore := proxiedServerMode
	proxiedServerMode = true
	resetCommandContext()
	t.Cleanup(func() {
		proxiedServerMode = restore
		resetCommandContext()
	})
}

const (
	proxiedMaxRowsRefusal   = "--max-rows / BEADS_MAX_ROWS is not supported in proxied-server mode"
	proxiedProviderNotReady = "proxied-server UOW provider not initialized"
)

// TestReadyHonorsMaxRowsUnderProxiedServer pins that a positive row cap no
// longer refuses `bd ready` under --proxied-server, on any arm. It used to:
// RunE refused --max-rows and BEADS_MAX_ROWS because the proxied ready path was
// believed to thread no cap. It does thread one — the ready union in
// internal/storage/domain/db sizes its window from WorkFilter.MaxRows and runs
// EnforceMaxRowsCap on it — so the cap now rides the filter exactly as it does
// on the direct route, and a workspace that exports BEADS_MAX_ROWS for every
// agent can run `bd ready` at all.
//
// The assertion is two-sided. "No refusal" alone would pass a RunE that
// swallowed the error, so the test also requires RunE to have reached the
// proxied dispatch: uowProvider is nil here, and runReadyProxiedServer's own
// message is the proof it got that far without dialing a server.
//
// --claim is in the table because the cap never applied to a claim on the
// direct route either (claimNextRequest carries none), so a refusal there was
// the proxied route alone. --gated is in it because it is the documented alias
// of `bd mol ready --gated` and takes no cap at all.
func TestReadyHonorsMaxRowsUnderProxiedServer(t *testing.T) {
	pinProxiedServerMode(t)
	pinJSONOutput(t, false)

	if uowProvider != nil {
		t.Fatal("precondition: uowProvider must be nil so the dispatch cannot open a real proxied connection")
	}

	for _, tc := range []struct {
		name string
		args []string
		env  string
	}{
		{name: "bulk_flag", args: []string{"--max-rows", "5"}},
		{name: "bulk_env", env: "5"},
		{name: "claim_flag", args: []string{"--claim", "--max-rows", "5"}},
		{name: "claim_env", args: []string{"--claim"}, env: "5"},
		{name: "gated_flag", args: []string{"--gated", "--max-rows", "5"}},
		{name: "gated_env", args: []string{"--gated"}, env: "5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(maxRowsEnvVar, tc.env)
			// A cloned flag set, not readyCmd's own: RunE reads its flags off
			// the command it is handed, and mutating the shared readyCmd would
			// leak these flags into every other test in the package.
			cmd := newReadyFlagsCommand(t, tc.args...)

			var err error
			stderr := captureStderr(t, func() { err = readyCmd.RunE(cmd, nil) })

			if strings.Contains(stderr, proxiedMaxRowsRefusal) {
				t.Fatalf("proxied `bd ready %s` refused a cap the proxied route enforces: %q",
					strings.Join(tc.args, " "), stderr)
			}
			if !strings.Contains(stderr, proxiedProviderNotReady) {
				t.Fatalf("proxied `bd ready %s` did not reach the proxied dispatch: err=%v stderr=%q",
					strings.Join(tc.args, " "), err, stderr)
			}
		})
	}
}

// TestReadyValidatesMaxRowsUnderProxiedServerBeforeTheStore pins where a bad
// cap is caught now that the front door no longer resolves one for `bd ready`:
// in gatherReadyInput, which runReadyProxiedServer calls before it touches the
// provider. A negative --max-rows is still a usage error, on the claim as much
// as on a listing, and it fails before any query runs.
//
// The gated arm is the exception, as on the direct route: there --gated
// dispatches above any cap resolution, so a negative value is never read, and
// the proxied route must not fail a command line the direct route accepts.
func TestReadyValidatesMaxRowsUnderProxiedServerBeforeTheStore(t *testing.T) {
	pinProxiedServerMode(t)
	pinJSONOutput(t, false)
	t.Setenv(maxRowsEnvVar, "")

	if uowProvider != nil {
		t.Fatal("precondition: uowProvider must be nil so the dispatch cannot open a real proxied connection")
	}

	for _, args := range [][]string{
		{"--max-rows", "-1"},
		{"--claim", "--max-rows", "-1"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var err error
			stderr := captureStderr(t, func() { err = readyCmd.RunE(newReadyFlagsCommand(t, args...), nil) })
			assertExitCode(t, err, 1)
			if !strings.Contains(stderr, "--max-rows must be non-negative") {
				t.Fatalf("proxied `bd ready %s` stderr = %q, want the usage error", strings.Join(args, " "), stderr)
			}
			if strings.Contains(stderr, proxiedProviderNotReady) {
				t.Fatalf("proxied `bd ready %s` reached the provider before rejecting the cap: %q", strings.Join(args, " "), stderr)
			}
		})
	}

	t.Run("gated_ignores_the_cap", func(t *testing.T) {
		var err error
		stderr := captureStderr(t, func() {
			err = readyCmd.RunE(newReadyFlagsCommand(t, "--gated", "--max-rows", "-1"), nil)
		})
		if strings.Contains(stderr, "--max-rows must be non-negative") {
			t.Fatalf("proxied `bd ready --gated` read a cap the gated arm never uses: %q", stderr)
		}
		if !strings.Contains(stderr, proxiedProviderNotReady) {
			t.Fatalf("proxied `bd ready --gated` did not reach the proxied dispatch: err=%v stderr=%q", err, stderr)
		}
	})
}

// TestReadyWarnsOnceForMalformedMaxRowsUnderProxiedServer pins the
// malformed-BEADS_MAX_ROWS advisory to one line on the proxied route. The cap
// is resolved exactly once there now, by gatherReadyInput, so the warning
// cannot double.
func TestReadyWarnsOnceForMalformedMaxRowsUnderProxiedServer(t *testing.T) {
	pinProxiedServerMode(t)
	pinJSONOutput(t, false)
	t.Setenv(maxRowsEnvVar, "bogus")

	var err error
	stderr := captureStderr(t, func() { err = readyCmd.RunE(newReadyFlagsCommand(t), nil) })
	if got := strings.Count(stderr, "is not a non-negative integer"); got != 1 {
		t.Fatalf("malformed %s warned %d times, want once (err=%v stderr=%q)", maxRowsEnvVar, got, err, stderr)
	}
	if !strings.Contains(stderr, proxiedProviderNotReady) {
		t.Fatalf("a malformed %s stopped the command; it disables the cap: err=%v stderr=%q", maxRowsEnvVar, err, stderr)
	}
}
