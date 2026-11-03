package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"

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

// stubReadyIssues answers exactly the reads the proxied ready route makes: the
// two ready listings and the advisory defer-wake sweep that runs before them.
// Its listings run the storage layer's own window math (see readyPage), so
// what reaches the command — and what the command does with it — is the real
// contract, not a stub's imitation.
type stubReadyIssues struct {
	domain.IssueUseCase // any other method is a nil call: these paths must not reach one
	items               []*types.Issue

	// filter records what the listing actually ran with, so a test can prove
	// the cap reached the query.
	filter types.WorkFilter
}

func (s *stubReadyIssues) WakeExpiredDefers(context.Context) (int, int, error) {
	return 0, 0, nil
}

// readyPage answers one ready listing the way the storage layer's window does
// (readyWindowForFilter + finishWindow in internal/storage/domain/db): the
// same SearchProbeLimit sizing — probe row and limit==cap bump included —
// the same EnforceMaxRowsCap count over what the window TOUCHED, skipped
// rows included, then the offset skip and the page trim that produces the
// has-more verdict. The slice stands in for the SQL LIMIT clause; the two
// exported issueops helpers are the parts of that pipeline that decide what
// a cap or a page does, so a route test here runs against the real window
// math, not a lookalike — a cap the set fits inside is invisible in the
// answer exactly as in the store.
func (s *stubReadyIssues) readyPage(filter types.WorkFilter) ([]*types.Issue, bool, error) {
	s.filter = filter
	// The window the cap is sized against is the one the query touches, so
	// an offset the epilogue skips stays inside the bound.
	touched := filter.Limit
	if filter.MaxRows > 0 && filter.Offset > 0 && filter.Limit > 0 {
		touched += filter.Offset
	}
	bound, rowCap := storageissueops.SearchProbeLimit(touched, filter.MaxRows)
	rows := s.items
	goSkip := 0
	switch {
	case bound <= 0:
		// No LIMIT clause; the epilogue skips the offset Go-side.
		goSkip = filter.Offset
	case filter.MaxRows > 0 && filter.Offset > 0 && filter.Limit > 0:
		// skipHere: the cap keeps the skipped rows inside the bound, and
		// the epilogue does the skipping.
		if len(rows) > bound {
			rows = rows[:bound]
		}
		goSkip = filter.Offset
	case filter.Offset > 0:
		// The engine skips: LIMIT bound OFFSET n.
		if filter.Offset >= len(rows) {
			rows = rows[:0]
		} else {
			rows = rows[filter.Offset:]
		}
		if len(rows) > bound {
			rows = rows[:bound]
		}
	default:
		if len(rows) > bound {
			rows = rows[:bound]
		}
	}
	if err := storageissueops.EnforceMaxRowsCap(len(rows), rowCap, filter.MaxRowsSource); err != nil {
		return nil, false, fmt.Errorf("ready work union: %w", err)
	}
	if goSkip > 0 {
		if goSkip >= len(rows) {
			rows = rows[:0]
		} else {
			rows = rows[goSkip:]
		}
	}
	if filter.Limit > 0 && len(rows) > filter.Limit {
		return rows[:filter.Limit], true, nil
	}
	return rows, false, nil
}

func (s *stubReadyIssues) GetReadyWork(_ context.Context, filter types.WorkFilter) (domain.SearchPage, error) {
	items, hasMore, err := s.readyPage(filter)
	if err != nil {
		return domain.SearchPage{}, err
	}
	return domain.SearchPage{Items: items, HasMore: hasMore}, nil
}

func (s *stubReadyIssues) GetReadyWorkWithCounts(_ context.Context, filter types.WorkFilter) (domain.SearchCountsPage, error) {
	items, hasMore, err := s.readyPage(filter)
	if err != nil {
		return domain.SearchCountsPage{}, err
	}
	counts := make([]*types.IssueWithCounts, len(items))
	for i, issue := range items {
		counts[i] = &types.IssueWithCounts{Issue: issue}
	}
	return domain.SearchCountsPage{Items: counts, HasMore: hasMore}, nil
}

// stubReadyDeps answers buildParentEpicMapProxied's one read with "no
// dependencies", so the listing renders without touching a dependency store.
type stubReadyDeps struct {
	domain.DependencyUseCase
}

func (stubReadyDeps) GetForIssueIDs(context.Context, []string) (map[string][]*types.Dependency, error) {
	return nil, nil
}

type stubReadyUOW struct {
	uow.UnitOfWork
	issues domain.IssueUseCase
}

func (u stubReadyUOW) Close(context.Context)                       {}
func (u stubReadyUOW) IssueUseCase() domain.IssueUseCase           { return u.issues }
func (u stubReadyUOW) DependencyUseCase() domain.DependencyUseCase { return stubReadyDeps{} }
func (u stubReadyUOW) LabelUseCase() domain.LabelUseCase           { return nil }
func (u stubReadyUOW) CommentUseCase() domain.CommentUseCase       { return nil }

// stubReadyProvider serves the proxied ready route. The real provider's
// capability accessors are where decorators attach; a test that needs the
// claim or counter surface embeds this and adds the accessor.
type stubReadyProvider struct {
	issues domain.IssueUseCase
}

func (p stubReadyProvider) NewUOW(context.Context) (uow.UnitOfWork, error) {
	return stubReadyUOW{issues: p.issues}, nil
}

func (p stubReadyProvider) Close(context.Context) error { return nil }

// withStubbedProxiedReady points the proxied ready route at a stub whose
// ready set is items, and pins the route to proxied for the test.
func withStubbedProxiedReady(t *testing.T, items []*types.Issue) *stubReadyIssues {
	t.Helper()
	stub := &stubReadyIssues{items: items}
	oldProvider := uowProvider
	uowProvider = stubReadyProvider{issues: stub}
	pinProxiedServerMode(t)
	t.Cleanup(func() { uowProvider = oldProvider })
	return stub
}

// runReadyCaptured runs readyCmd.RunE under captured streams and returns the
// error with everything the invocation printed.
func runReadyCaptured(t *testing.T, cmd *cobra.Command) (error, string, string) {
	t.Helper()
	stdioMutex.Lock()
	defer stdioMutex.Unlock()
	// RunE hands runReadyProxiedServer the global rootCtx, and a test never
	// went through Execute to set it — nil would panic in the defer-wake
	// sweep's first ctx use. Pin a background ctx, as the proxied backup
	// tests do, and put it back.
	oldRootCtx := rootCtx
	rootCtx = context.Background()
	defer func() { rootCtx = oldRootCtx }()
	oldStdout, oldStderr := os.Stdout, os.Stderr
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout, os.Stderr = wOut, wErr
	var runErr error
	stdoutCh, stderrCh := make(chan string, 1), make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(rOut)
		stdoutCh <- buf.String()
	}()
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(rErr)
		stderrCh <- buf.String()
	}()
	runErr = readyCmd.RunE(cmd, nil)
	wOut.Close()
	wErr.Close()
	os.Stdout, os.Stderr = oldStdout, oldStderr
	stdout, stderr := <-stdoutCh, <-stderrCh
	_ = rOut.Close()
	_ = rErr.Close()
	return runErr, stdout, stderr
}

func stubReadyIssue(i int) *types.Issue {
	return &types.Issue{
		ID:     fmt.Sprintf("bd-stub%d", i),
		Title:  fmt.Sprintf("Stub ready issue %d", i),
		Status: types.StatusOpen,
	}
}

// TestReadyHonorsMaxRowsUnderProxiedServer is the bd-7fnvhp regression: a
// positive --max-rows / BEADS_MAX_ROWS cap is honored by proxied `bd ready`
// rather than refused. The front door used to reject it before the provider
// opened, and the RunE used to reject it again behind the dispatch — three
// commands' worth of refusals for a cap the proxied ready query has been able
// to enforce all along (its window sizes itself from filter.MaxRows).
//
// The stub runs the storage layer's own window math, so a green run proves
// the whole chain: no refusal anywhere ahead of the query, the cap resolved
// onto the filter the gatherer built (flag and env, with their source
// strings), the query's typed overage error mapped to exit 2 with the
// three-line stderr diagnosis, and stdout left empty so a jq pipeline sees
// nothing half-rendered. The overage the diagnosis names is what the window
// matched — cap plus the one probe row that detects truncation, not the
// whole set (SearchProbeLimit sizes an unbounded page's bound to cap+1).
func TestReadyHonorsMaxRowsUnderProxiedServer(t *testing.T) {
	for _, tc := range []struct {
		name string
		json bool
		args []string
		env  string
		// wantSource is the attribution the storage error must carry.
		wantSource string
	}{
		{name: "flag_text", args: []string{"--max-rows", "1"}, wantSource: "--max-rows"},
		{name: "flag_json", json: true, args: []string{"--max-rows", "1"}, wantSource: "--max-rows"},
		{name: "env_text", env: "1", wantSource: maxRowsEnvVar},
		{name: "env_json", json: true, env: "1", wantSource: maxRowsEnvVar},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pinJSONOutput(t, tc.json)
			t.Setenv(maxRowsEnvVar, tc.env)
			stub := withStubbedProxiedReady(t, []*types.Issue{stubReadyIssue(1), stubReadyIssue(2), stubReadyIssue(3)})

			cmd := newReadyFlagsCommand(t, tc.args...)
			err, stdout, stderr := runReadyCaptured(t, cmd)

			if code, ok := exitCodeFromError(err); !ok || code != 2 {
				t.Fatalf("exit = %v (code=%d typed=%v), want 2 (stderr=%q)", err, code, ok, stderr)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want it empty so a --json pipeline reads no half-rendered rows", stdout)
			}
			wantFirst := fmt.Sprintf("Error: too many rows: 2 found, %s=1 exceeded.", tc.wantSource)
			if got := strings.SplitN(stderr, "\n", 2)[0]; got != wantFirst {
				t.Errorf("stderr first line = %q, want %q", got, wantFirst)
			}
			if strings.Count(stderr, "Refine the query") != 1 {
				t.Errorf("expected the one three-line cap diagnosis on stderr, got: %q", stderr)
			}
			if strings.Contains(stderr, "not supported in proxied-server mode") {
				t.Errorf("the unsupported-cap refusal still fired: %q", stderr)
			}
			if stub.filter.MaxRows != 1 || stub.filter.MaxRowsSource != tc.wantSource {
				t.Errorf("the ready query ran with MaxRows=%d Source=%q, want the cap the flag/env set (%s)",
					stub.filter.MaxRows, stub.filter.MaxRowsSource, tc.wantSource)
			}
		})
	}
}

// TestReadyValidatesMaxRowsUnderProxiedServerBeforeTheStore pins where a
// negative cap is rejected on the proxied route. The front door used to
// resolve it before the provider opened; with the cap honored there instead,
// the gatherer is the one place it is validated — ahead of any store call.
// The nil provider is the sentinel: RunE must fail on the flag itself, not
// reach the provider error a passing validation would fall through to.
func TestReadyValidatesMaxRowsUnderProxiedServerBeforeTheStore(t *testing.T) {
	pinProxiedServerMode(t)
	pinJSONOutput(t, false)
	if uowProvider != nil {
		t.Fatal("precondition: uowProvider must be nil so a passing validation cannot open a real proxied connection")
	}

	var err error
	stderr := captureStderr(t, func() { err = readyCmd.RunE(newReadyFlagsCommand(t, "--max-rows", "-1"), nil) })
	assertExitCode(t, err, 1)
	if !strings.Contains(stderr, "--max-rows must be non-negative") {
		t.Fatalf("stderr = %q, want the negative-cap usage error", stderr)
	}
	if strings.Contains(stderr, "proxied-server UOW provider not initialized") {
		t.Fatalf("the negative cap reached the proxied dispatch: %q", stderr)
	}

	// A claim conflict is decided before the cap is resolved, so the doubly
	// invalid `--claim --gated --max-rows -1` reports the flag pair, not the
	// cap — the same order the direct route has always reported.
	stderr = captureStderr(t, func() { err = readyCmd.RunE(newReadyFlagsCommand(t, "--claim", "--gated", "--max-rows", "-1"), nil) })
	assertExitCode(t, err, 1)
	if !strings.Contains(stderr, "--claim cannot be combined with --gated") {
		t.Fatalf("stderr = %q, want the claim/gated conflict decided before the cap", stderr)
	}
}

// TestReadyWarnsOnceForMalformedMaxRowsUnderProxiedServer pins the
// malformed-env advisory on the proxied route: the value disables the cap and
// the command goes on, and the warning appears exactly once — the gatherer
// resolves the cap once, and no second resolution site may repeat it.
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
		t.Fatalf("the run should have proceeded past the cap to the provider sentinel: err=%v stderr=%q", err, stderr)
	}
	if n := strings.Count(stderr, fmt.Sprintf("Warning: %s=%q is not a non-negative integer", maxRowsEnvVar, "bogus")); n != 1 {
		t.Fatalf("malformed %s warning appears %d times, want exactly 1: %q", maxRowsEnvVar, n, stderr)
	}
}

// TestReadyGatedIgnoresMaxRowsUnderProxiedServer keeps the --gated exemption
// whole across the change: `bd ready --gated` and `bd mol ready --gated` are
// one documented command line, and neither resolves a cap — not a positive
// one, not a malformed one. The proxied route passes the gatherer a nil
// resolver on that arm, exactly as the direct route never reaches resolution
// at all.
func TestReadyGatedIgnoresMaxRowsUnderProxiedServer(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		env  string
	}{
		{name: "flag", args: []string{"--gated", "--max-rows", "5"}},
		{name: "env", args: []string{"--gated"}, env: "5"},
		{name: "malformed_env", args: []string{"--gated"}, env: "bogus"},
		{name: "negative_flag", args: []string{"--gated", "--max-rows", "-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pinProxiedServerMode(t)
			pinJSONOutput(t, false)
			t.Setenv(maxRowsEnvVar, tc.env)
			if uowProvider != nil {
				t.Fatal("precondition: uowProvider must be nil so the dispatch cannot open a real proxied connection")
			}

			var err error
			stderr := captureStderr(t, func() { err = readyCmd.RunE(newReadyFlagsCommand(t, tc.args...), nil) })
			for _, bad := range []string{
				"not supported in proxied-server mode",
				"must be non-negative",
				"is not a non-negative integer",
				"too many rows",
			} {
				if strings.Contains(stderr, bad) {
					t.Errorf("the gated arm consulted a cap it never uses: %q", stderr)
				}
			}
			if !strings.Contains(stderr, "proxied-server UOW provider not initialized") {
				t.Errorf("proxied `bd ready --gated` did not reach the proxied dispatch: err=%v stderr=%q", err, stderr)
			}
		})
	}
}

// TestReadyMaxRowsPaginatesUnderProxiedServer is the AC-2 parity: a cap the
// ready set fits inside is invisible in the answer. Each capped invocation is
// run beside the same invocation without the cap and must produce identical
// stdout — rows, order and the has-more verdict the pagination metadata
// carries — because the probe row that detects "more matched" is fetched
// inside the window (SearchProbeLimit bumps the cap with it at limit==cap),
// not onto the page.
//
// The one page a cap may refuse is the one that reaches past its own skipped
// rows: `--offset 2 --limit 1 --max-rows 2` touches three rows — two skipped
// plus the page — and pays the cap on what it touched. The same page with no
// offset stays inside the cap, and the same offset with no cap keeps paging
// exactly as before.
func TestReadyMaxRowsPaginatesUnderProxiedServer(t *testing.T) {
	for _, tc := range []struct {
		name string
		json bool
		args []string // the capped page
		env  string
		// same is the same page with no cap, run beside it; the capped
		// answer must be identical to it.
		same       []string
		wantCap    int
		wantSource string
	}{
		{name: "flag_cap_fits_set", args: []string{"--max-rows", "3"}, same: nil, wantCap: 3, wantSource: "--max-rows"},
		{name: "env_cap_fits_set", env: "3", same: nil, wantCap: 3, wantSource: maxRowsEnvVar},
		{name: "flag_page_below_cap", args: []string{"--limit", "1", "--max-rows", "2"}, same: []string{"--limit", "1"}, wantCap: 2, wantSource: "--max-rows"},
		{name: "env_page_below_cap", env: "2", args: []string{"--limit", "1"}, same: []string{"--limit", "1"}, wantCap: 2, wantSource: maxRowsEnvVar},
		{name: "flag_page_at_cap", args: []string{"--limit", "2", "--max-rows", "2"}, same: []string{"--limit", "2"}, wantCap: 2, wantSource: "--max-rows"},
		{name: "env_page_at_cap", env: "2", args: []string{"--limit", "2"}, same: []string{"--limit", "2"}, wantCap: 2, wantSource: maxRowsEnvVar},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pinJSONOutput(t, tc.json)
			stub := withStubbedProxiedReady(t, []*types.Issue{stubReadyIssue(1), stubReadyIssue(2), stubReadyIssue(3)})

			// The baseline runs first, before the env cap exists for it.
			baseErr, want, baseStderr := runReadyCaptured(t, newReadyFlagsCommand(t, tc.same...))
			if baseErr != nil {
				t.Fatalf("uncapped baseline failed: %v (stderr=%q)", baseErr, baseStderr)
			}
			t.Setenv(maxRowsEnvVar, tc.env)
			err, got, stderr := runReadyCaptured(t, newReadyFlagsCommand(t, tc.args...))
			if err != nil {
				t.Fatalf("capped run failed where the set fits inside the cap: %v (stderr=%q)", err, stderr)
			}
			if got != want {
				t.Errorf("capped stdout differs from the uncapped page:\ncapped:\n%s\nuncapped:\n%s", got, want)
			}
			if stub.filter.MaxRows != tc.wantCap || stub.filter.MaxRowsSource != tc.wantSource {
				t.Errorf("the ready query ran with MaxRows=%d Source=%q, want cap %d from %s",
					stub.filter.MaxRows, stub.filter.MaxRowsSource, tc.wantCap, tc.wantSource)
			}
		})
	}

	for _, tc := range []struct {
		name       string
		json       bool
		args       []string
		env        string
		wantSource string
	}{
		{name: "offset_reaches_past_cap_flag", args: []string{"--offset", "2", "--limit", "1", "--max-rows", "2"}, wantSource: "--max-rows"},
		{name: "offset_reaches_past_cap_env", env: "2", args: []string{"--offset", "2", "--limit", "1"}, wantSource: maxRowsEnvVar},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pinJSONOutput(t, tc.json)
			t.Setenv(maxRowsEnvVar, tc.env)
			stub := withStubbedProxiedReady(t, []*types.Issue{stubReadyIssue(1), stubReadyIssue(2), stubReadyIssue(3)})

			err, stdout, stderr := runReadyCaptured(t, newReadyFlagsCommand(t, tc.args...))
			if code, ok := exitCodeFromError(err); !ok || code != 2 {
				t.Fatalf("exit = %v (code=%d typed=%v), want 2 (stderr=%q)", err, code, ok, stderr)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want it empty so a --json pipeline reads no half-rendered rows", stdout)
			}
			wantFirst := fmt.Sprintf("Error: too many rows: 3 found, %s=2 exceeded.", tc.wantSource)
			if got := strings.SplitN(stderr, "\n", 2)[0]; got != wantFirst {
				t.Errorf("stderr first line = %q, want %q", got, wantFirst)
			}
			if stub.filter.MaxRows != 2 || stub.filter.Limit != 1 || stub.filter.Offset != 2 {
				t.Errorf("the ready query ran with MaxRows=%d Limit=%d Offset=%d, want cap 2 over a 1-row page at offset 2",
					stub.filter.MaxRows, stub.filter.Limit, stub.filter.Offset)
			}
		})
	}

	// The same offset page with no cap still pages: the cap change must not
	// have moved where the offset is applied. Both formats render the one
	// row the page selects and neither of the two it skips.
	for _, tc := range []struct {
		name string
		json bool
	}{
		{name: "offset_uncapped_text"},
		{name: "offset_uncapped_json", json: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pinJSONOutput(t, tc.json)
			withStubbedProxiedReady(t, []*types.Issue{stubReadyIssue(1), stubReadyIssue(2), stubReadyIssue(3)})

			err, stdout, stderr := runReadyCaptured(t, newReadyFlagsCommand(t, "--offset", "2", "--limit", "1"))
			if err != nil {
				t.Fatalf("uncapped offset page failed: %v (stderr=%q)", err, stderr)
			}
			if !strings.Contains(stdout, "bd-stub3") || strings.Contains(stdout, "bd-stub1") || strings.Contains(stdout, "bd-stub2") {
				t.Errorf("offset page stdout = %q, want only bd-stub3", stdout)
			}
		})
	}
}
