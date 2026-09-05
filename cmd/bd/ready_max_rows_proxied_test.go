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

// TestReadyRefusesMaxRowsUnderProxiedServerOnClaim pins the --max-rows refusal
// on `bd ready --claim` specifically.
//
// The refusal lives in readyCmd's RunE, above the dispatch into
// runReadyProxiedServer, and --claim is the one ready invocation that takes a
// mutating path on the far side of it. Nothing below that dispatch can enforce
// the cap: runReadyProxiedServer deliberately passes a nil resolver to
// gatherReadyInput, and the proxied claim role never threads MaxRows. So the
// only thing standing between an operator's explicit row cap and a silent
// unbounded claim is this one call, and it is reachable only through RunE --
// the existing gatherReadyInput coverage sits entirely below it and cannot see
// it at all.
//
// Both cap sources are exercised: resolveMaxRows honors --max-rows and
// BEADS_MAX_ROWS, and a guard wired to only the flag would leave every rig that
// sets the env var running uncapped.
func TestReadyRefusesMaxRowsUnderProxiedServerOnClaim(t *testing.T) {
	pinProxiedServerMode(t)
	pinJSONOutput(t, false)

	// The regression safety net. If the refusal ever stops firing, RunE falls
	// through to runReadyProxiedServer, which hits an uninitialized provider
	// and returns its own error rather than dialing a real server -- so the
	// stderr assertion below, not the exit code, is what fails. Both errors
	// carry exit code 1.
	if uowProvider != nil {
		t.Fatal("precondition: uowProvider must be nil so a regression cannot open a real proxied connection")
	}

	for _, tc := range []struct {
		name string
		args []string
		env  string
	}{
		{name: "flag", args: []string{"--claim", "--max-rows", "5"}},
		{name: "env", args: []string{"--claim"}, env: "5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(maxRowsEnvVar, tc.env)

			// A cloned flag set, not readyCmd's own: RunE reads its flags off
			// the command it is handed, and mutating the shared readyCmd would
			// leak --claim into every other test in the package.
			cmd := newReadyFlagsCommand(t, tc.args...)

			var err error
			stderr := captureStderr(t, func() { err = readyCmd.RunE(cmd, nil) })

			assertExitCode(t, err, 1)
			if !strings.Contains(stderr, "--max-rows / BEADS_MAX_ROWS is not supported in proxied-server mode") {
				t.Fatalf("stderr = %q, want the proxied --max-rows refusal", stderr)
			}
		})
	}
}

// TestReadyClaimWithoutLiveCapIsNotRefusedUnderProxiedServer is the other half:
// the refusal is conditioned on a live cap, not on --claim. Without it, a guard
// that rejected every proxied --claim would pass the test above.
//
// This one calls the helper directly rather than going through RunE. With
// nothing to refuse, RunE proceeds into the proxied query path, and the
// assertion would then be about whatever that path does next instead of about
// the guard.
func TestReadyClaimWithoutLiveCapIsNotRefusedUnderProxiedServer(t *testing.T) {
	pinProxiedServerMode(t)
	pinJSONOutput(t, false)
	t.Setenv(maxRowsEnvVar, "")

	// --max-rows 0 is an explicit disable that outranks BEADS_MAX_ROWS, so it
	// is not a live cap either even with the env var set.
	for _, tc := range []struct {
		name string
		args []string
		env  string
	}{
		{name: "no_cap", args: []string{"--claim"}},
		{name: "flag_zero", args: []string{"--claim", "--max-rows", "0"}},
		{name: "flag_zero_overrides_env", args: []string{"--claim", "--max-rows", "0"}, env: "5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(maxRowsEnvVar, tc.env)
			if err := rejectMaxRowsUnderProxiedServer(newReadyFlagsCommand(t, tc.args...)); err != nil {
				t.Fatalf("proxied `bd ready %s` was refused with no live cap: %v", strings.Join(tc.args, " "), err)
			}
		})
	}
}
