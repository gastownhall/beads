package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/domain"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
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

// TestReadyGatedSkipsMaxRowsRefusalUnderProxiedServer pins the third arm.
// `bd ready --gated` and `bd mol ready --gated` are documented as the same
// gate-resume scan, and on the proxied route they literally are one:
// runReadyProxiedGated discards its readyInput and calls the same
// findGateReadyMolecules as runMolReadyGatedProxiedServer. Neither threads a
// row cap, so a cap must not stop either spelling for anyone with
// BEADS_MAX_ROWS exported.
//
// The assertion is two-sided on purpose. A guard that swallowed the error
// entirely would pass a "no refusal" check alone, so the test also requires
// RunE to have reached the proxied dispatch -- uowProvider is nil here, so
// runReadyProxiedServer's own message is the proof it got that far.
func TestReadyGatedSkipsMaxRowsRefusalUnderProxiedServer(t *testing.T) {
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
		{name: "flag", args: []string{"--gated", "--max-rows", "5"}},
		{name: "env", args: []string{"--gated"}, env: "5"},
		// As on the direct route, where --gated dispatches above any cap
		// resolution, a value the arm never reads cannot fail it or warn.
		{name: "negative_flag", args: []string{"--gated", "--max-rows", "-1"}},
		{name: "malformed_env", args: []string{"--gated"}, env: "bogus"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(maxRowsEnvVar, tc.env)
			cmd := newReadyFlagsCommand(t, tc.args...)

			var err error
			stderr := captureStderr(t, func() { err = readyCmd.RunE(cmd, nil) })

			for _, bad := range []string{
				"not supported in proxied-server mode",
				"must be non-negative",
				"is not a non-negative integer",
				"too many rows",
			} {
				if strings.Contains(stderr, bad) {
					t.Fatalf("proxied `bd ready %s` consulted a cap the gated arm never uses: %q",
						strings.Join(tc.args, " "), stderr)
				}
			}
			if !strings.Contains(stderr, "proxied-server UOW provider not initialized") {
				t.Fatalf("proxied `bd ready %s` did not reach the proxied dispatch: err=%v stderr=%q",
					strings.Join(tc.args, " "), err, stderr)
			}
		})
	}
}

// TestReproBd7fnvhpProxiedMaxRowsHonored asserts the expected behaviour from
// bd-7fnvhp (PR #7408): under --proxied-server, a positive BEADS_MAX_ROWS cap is
// honored by `bd ready`, `bd graph` and `bd find-duplicates` (as `bd list` and
// `bd dep tree` already do), so the pre-provider front door must not refuse it.
func TestReproBd7fnvhpProxiedMaxRowsHonored(t *testing.T) {
	pinJSONOutput(t, false)
	for _, path := range []string{"ready", "graph", "find-duplicates"} {
		t.Run(path, func(t *testing.T) {
			t.Setenv(maxRowsEnvVar, "5")
			cmd := realCommandAtPath(t, path)
			var err error
			stderr := captureStderr(t, func() { err = validateProxyCapabilitiesBeforeProvider(cmd) })
			if err != nil {
				t.Fatalf("proxied `bd %s` with %s=5 was refused before the provider: %v (stderr=%q)", path, maxRowsEnvVar, err, stderr)
			}
		})
	}
}

// TestReadyValidatesMaxRowsUnderProxiedServerBeforeTheStore pins where a
// negative --max-rows is rejected now that the front door no longer resolves
// the cap: in RunE, before the route touches the provider. uowProvider is nil
// here, so a validation that moved below the store would surface as the
// provider's own message instead of the usage error.
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
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cmd := newReadyFlagsCommand(t, args...)
			var err error
			stderr := captureStderr(t, func() { err = readyCmd.RunE(cmd, nil) })
			assertExitCode(t, err, 1)
			if !strings.Contains(stderr, "--max-rows must be non-negative") {
				t.Fatalf("stderr = %q, want the negative --max-rows usage error", stderr)
			}
			if strings.Contains(stderr, "provider not initialized") {
				t.Fatalf("negative --max-rows reached the provider before it was rejected: %q", stderr)
			}
		})
	}

	// A claim conflict is decided before the cap is resolved, so a command
	// line that is also wrong about the cap reports the flag pair - the order
	// the direct route has always reported - and a positive cap does not
	// excuse the conflict. Neither reaches the provider, so neither claims.
	for _, args := range [][]string{
		{"--claim", "--gated", "--max-rows", "-1"},
		{"--claim", "--gated", "--max-rows", "1"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cmd := newReadyFlagsCommand(t, args...)
			var err error
			stderr := captureStderr(t, func() { err = readyCmd.RunE(cmd, nil) })
			assertExitCode(t, err, 1)
			if !strings.Contains(stderr, "--claim cannot be combined with --gated") {
				t.Fatalf("stderr = %q, want the claim/gated conflict decided before the cap", stderr)
			}
			if strings.Contains(stderr, "provider not initialized") {
				t.Fatalf("an invalid claim command line reached the provider: %q", stderr)
			}
		})
	}
}

// TestReadyReachesProxiedDispatchUnderPositiveMaxRows pins that a positive
// cap from either source no longer stops `bd ready` in RunE under
// --proxied-server, on the listing and on --claim. The assertion is
// two-sided: "no refusal" alone would pass a RunE that swallowed the error,
// so the test also requires RunE to have reached the proxied dispatch -
// uowProvider is nil here, and runReadyProxiedServer's own message is the
// proof it got that far without dialing a server.
//
// --claim is in the table because the cap never applied to a claim on the
// direct route either (claimNextRequest carries none), so a refusal there was
// the proxied route alone.
func TestReadyReachesProxiedDispatchUnderPositiveMaxRows(t *testing.T) {
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
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(maxRowsEnvVar, tc.env)
			// A cloned flag set, not readyCmd's own: RunE reads its flags off
			// the command it is handed, and mutating the shared readyCmd would
			// leak these flags into every other test in the package.
			cmd := newReadyFlagsCommand(t, tc.args...)

			var err error
			stderr := captureStderr(t, func() { err = readyCmd.RunE(cmd, nil) })

			if strings.Contains(stderr, "not supported in proxied-server mode") {
				t.Fatalf("proxied `bd ready %s` refused a cap the proxied route enforces: %q",
					strings.Join(tc.args, " "), stderr)
			}
			if !strings.Contains(stderr, "proxied-server UOW provider not initialized") {
				t.Fatalf("proxied `bd ready %s` did not reach the proxied dispatch: err=%v stderr=%q",
					strings.Join(tc.args, " "), err, stderr)
			}
		})
	}
}

// TestReadyWarnsOnceForMalformedMaxRowsUnderProxiedServer pins the
// malformed-BEADS_MAX_ROWS advisory to one line on the proxied route: the
// value disables the cap and the command goes on, and the gatherer resolves
// the cap exactly once, so no second resolution site may repeat the warning.
func TestReadyWarnsOnceForMalformedMaxRowsUnderProxiedServer(t *testing.T) {
	pinProxiedServerMode(t)
	pinJSONOutput(t, false)
	t.Setenv(maxRowsEnvVar, "bogus")

	if uowProvider != nil {
		t.Fatal("precondition: uowProvider must be nil so the run stops at the provider sentinel")
	}

	var err error
	stderr := captureStderr(t, func() { err = readyCmd.RunE(newReadyFlagsCommand(t), nil) })
	if !strings.Contains(stderr, "proxied-server UOW provider not initialized") {
		t.Fatalf("a malformed %s stopped the command; it disables the cap: err=%v stderr=%q", maxRowsEnvVar, err, stderr)
	}
	if n := strings.Count(stderr, fmt.Sprintf("Warning: %s=%q is not a non-negative integer", maxRowsEnvVar, "bogus")); n != 1 {
		t.Fatalf("malformed %s warned %d times, want exactly once: %q", maxRowsEnvVar, n, stderr)
	}
}

// cappedReadyIssueUC serves the ready reads the way the proxied store does
// with a cap: storage enforces filter.MaxRows over its ready front of `found`
// rows and returns *issueops.ErrTooManyRows, wrapped as the domain use case
// wraps it. It records every filter it was handed so a test can check the cap
// reached the store.
type cappedReadyIssueUC struct {
	domain.IssueUseCase // any other method is a nil call: the listing must not reach one
	found               int
	filters             *[]types.WorkFilter
}

func (s cappedReadyIssueUC) WakeExpiredDefers(context.Context) (int, int, error) { return 0, 0, nil }

func (s cappedReadyIssueUC) capErr(name string, filter types.WorkFilter) error {
	*s.filters = append(*s.filters, filter)
	if filter.MaxRows > 0 && s.found > filter.MaxRows {
		return fmt.Errorf("%s: %w", name, &storageissueops.ErrTooManyRows{
			Found: s.found, Cap: filter.MaxRows, Source: filter.MaxRowsSource,
		})
	}
	return nil
}

func (s cappedReadyIssueUC) GetReadyWork(_ context.Context, filter types.WorkFilter) (domain.SearchPage, error) {
	return domain.SearchPage{}, s.capErr("GetReadyWork", filter)
}

func (s cappedReadyIssueUC) GetReadyWorkWithCounts(_ context.Context, filter types.WorkFilter) (domain.SearchCountsPage, error) {
	return domain.SearchCountsPage{}, s.capErr("GetReadyWorkWithCounts", filter)
}

type cappedReadyUOW struct {
	uow.UnitOfWork
	issues domain.IssueUseCase
}

func (u cappedReadyUOW) Close(context.Context)                {}
func (u cappedReadyUOW) Commit(context.Context, string) error { return nil }
func (u cappedReadyUOW) IssueUseCase() domain.IssueUseCase    { return u.issues }

type cappedReadyProvider struct{ issues domain.IssueUseCase }

func (p cappedReadyProvider) NewUOW(context.Context) (uow.UnitOfWork, error) {
	return cappedReadyUOW{issues: p.issues}, nil
}

func (p cappedReadyProvider) Close(context.Context) error { return nil }

// withCappedReadyProvider points the proxied route at a store whose ready
// front holds `found` rows and returns the filters it is handed.
func withCappedReadyProvider(t *testing.T, found int) *[]types.WorkFilter {
	t.Helper()
	filters := &[]types.WorkFilter{}
	oldProvider, oldCtx := uowProvider, rootCtx
	uowProvider = cappedReadyProvider{issues: cappedReadyIssueUC{found: found, filters: filters}}
	rootCtx = context.Background()
	t.Cleanup(func() { uowProvider, rootCtx = oldProvider, oldCtx })
	return filters
}

// TestReadyHonorsMaxRowsUnderProxiedServer pins AC-1 of bd-7fnvhp at the
// command: a positive cap from either source reaches the proxied ready read,
// and the store's overage leaves through handleMaxRowsError as on the direct
// route - exit 2, the source and value named on stderr, nothing on stdout -
// instead of the proxy.max_rows.unsupported refusal the route used to raise.
func TestReadyHonorsMaxRowsUnderProxiedServer(t *testing.T) {
	pinProxiedServerMode(t)

	for _, tc := range []struct {
		name   string
		args   []string
		env    string
		source string
	}{
		{name: "flag", args: []string{"--max-rows", "1"}, source: "--max-rows"},
		{name: "env", env: "1", source: maxRowsEnvVar},
	} {
		for _, asJSON := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%v", tc.name, asJSON), func(t *testing.T) {
				pinJSONOutput(t, asJSON)
				t.Setenv(maxRowsEnvVar, tc.env)
				filters := withCappedReadyProvider(t, 3)
				cmd := newReadyFlagsCommand(t, tc.args...)

				var err error
				stdout, stderr := captureStdio(t, func() { err = readyCmd.RunE(cmd, nil) })

				assertExitCode(t, err, 2)
				want := fmt.Sprintf("Error: too many rows: 3 found, %s=1 exceeded.", tc.source)
				if !strings.Contains(stderr, want) {
					t.Fatalf("stderr = %q, want %q", stderr, want)
				}
				if strings.Contains(stderr, "not supported in proxied-server mode") {
					t.Fatalf("the cap was refused instead of enforced: %q", stderr)
				}
				if stdout != "" {
					t.Fatalf("stdout = %q, want nothing on a cap overage", stdout)
				}
				if len(*filters) != 1 || (*filters)[0].MaxRows != 1 || (*filters)[0].MaxRowsSource != tc.source {
					t.Fatalf("store filters = %+v, want one read capped at 1 from %s", *filters, tc.source)
				}
			})
		}
	}
}
