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
	"github.com/steveyegge/beads/internal/types"
)

// stubGraphIssues answers the two reads the proxied graph route makes through
// the issue role: the single-issue GetIssue and the per-status SearchIssues
// sweeps of --all. Its search runs the storage layer's own window math
// (SearchProbeLimit sizing, EnforceMaxRowsCap over what the window touched),
// so the cap each per-status filter carries is enforced exactly where the
// real search enforces it.
type stubGraphIssues struct {
	domain.IssueUseCase // anything else is a nil call: these paths must not reach one

	issues []*types.Issue

	// filters records every per-status search the route ran, so a test can
	// prove the cap reached each one.
	filters []types.IssueFilter
}

func (s *stubGraphIssues) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	for _, iss := range s.issues {
		if iss.ID == id {
			return iss, nil
		}
	}
	return nil, nil
}

func (s *stubGraphIssues) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) (domain.SearchPage, error) {
	s.filters = append(s.filters, filter)
	var rows []*types.Issue
	for _, iss := range s.issues {
		if filter.Status != nil && iss.Status != *filter.Status {
			continue
		}
		rows = append(rows, iss)
	}
	// The graph searches run unbounded pages, so the window is the cap's
	// bound plus its one probe row; the cap counts what that window matched.
	bound, rowCap := storageissueops.SearchProbeLimit(0, filter.MaxRows)
	if bound > 0 && len(rows) > bound {
		rows = rows[:bound]
	}
	if err := storageissueops.EnforceMaxRowsCap(len(rows), rowCap, filter.MaxRowsSource); err != nil {
		return domain.SearchPage{}, fmt.Errorf("failed to search issues: %w", err)
	}
	return domain.SearchPage{Items: rows}, nil
}

// stubGraphDeps answers the two dependency reads the proxied graph loaders
// make: ListWithIssueMetadata (the BFS's neighbor step) and
// GetIssueDependencyRecords (the post-walk edge collection). edges maps an
// issue ID to its neighbors in both directions, which is the only distinction
// the loaders never ask ListWithIssueMetadata to make.
type stubGraphDeps struct {
	domain.DependencyUseCase
	byID  map[string]*types.Issue
	edges map[string][]string
}

func (s *stubGraphDeps) ListWithIssueMetadata(_ context.Context, sourceID string, _ domain.DepListFilter) ([]*types.IssueWithDependencyMetadata, error) {
	var metas []*types.IssueWithDependencyMetadata
	for _, n := range s.edges[sourceID] {
		if iss, ok := s.byID[n]; ok {
			metas = append(metas, &types.IssueWithDependencyMetadata{Issue: *iss})
		}
	}
	return metas, nil
}

func (s *stubGraphDeps) GetIssueDependencyRecords(_ context.Context, ids []string) (map[string][]*types.Dependency, error) {
	recs := make(map[string][]*types.Dependency, len(ids))
	for _, id := range ids {
		for _, n := range s.edges[id] {
			recs[id] = append(recs[id], &types.Dependency{IssueID: id, DependsOnID: n})
		}
	}
	return recs, nil
}

// withStubbedProxiedGraph points the proxied graph route at a stub issue set
// whose dependency plane is edges, and pins the route to proxied for the test.
func withStubbedProxiedGraph(t *testing.T, issues []*types.Issue, edges map[string][]string) *stubGraphIssues {
	t.Helper()
	byID := make(map[string]*types.Issue, len(issues))
	for _, iss := range issues {
		byID[iss.ID] = iss
	}
	stub := &stubGraphIssues{issues: issues}
	oldProvider := uowProvider
	uowProvider = stubReadyProvider{issues: stub, deps: &stubGraphDeps{byID: byID, edges: edges}}
	pinProxiedServerMode(t)
	t.Cleanup(func() { uowProvider = oldProvider })
	return stub
}

// runGraphCaptured runs graphCmd.RunE under a captured writer and captured
// stderr, with --all pinned per call and --max-rows the only flag the test
// command registers. Graph rendering goes to cmd.OutOrStdout, so `got` is
// everything the invocation rendered.
func runGraphCaptured(t *testing.T, all bool, args ...string) (error, string, string) {
	t.Helper()
	stdioMutex.Lock()
	defer stdioMutex.Unlock()
	oldAll := graphAll
	graphAll = all
	oldRootCtx := rootCtx
	rootCtx = context.Background()
	defer func() {
		graphAll = oldAll
		rootCtx = oldRootCtx
	}()

	cmd := &cobra.Command{Use: "graph"}
	cmd.Flags().Int("max-rows", 0, "defensive row cap")
	if err := cmd.Flags().Parse(args); err != nil {
		t.Fatalf("parse graph args %v: %v", args, err)
	}
	var out bytes.Buffer
	cmd.SetOut(&out)

	oldStderr := os.Stderr
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = wErr
	stderrCh := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(rErr)
		stderrCh <- buf.String()
	}()
	runErr := graphCmd.RunE(cmd, cmd.Flags().Args())
	wErr.Close()
	os.Stderr = oldStderr
	stderr := <-stderrCh
	_ = rErr.Close()
	return runErr, out.String(), stderr
}

func stubGraphIssue(id string, status types.Status) *types.Issue {
	return &types.Issue{
		ID:     id,
		Title:  "Graph stub " + id,
		Status: status,
	}
}

// TestGraphHonorsMaxRowsUnderProxiedServer is the single-issue half of
// bd-7fnvhp: a proxied graph over a three-node connected component honors
// the cap rather than refusing it. The whole subgraph is walked first (the
// loaders are per-ID BFS steps, no filter to thread a cap through), then the
// component's node count is checked against the cap — so cap 2 over the
// three-node component exits 2 naming the source with nothing rendered,
// and cap 3 answers with exactly the uncapped graph's output.
func TestGraphHonorsMaxRowsUnderProxiedServer(t *testing.T) {
	chain := []*types.Issue{
		stubGraphIssue("bd-g1", types.StatusOpen),
		stubGraphIssue("bd-g2", types.StatusOpen),
		stubGraphIssue("bd-g3", types.StatusOpen),
	}
	edges := map[string][]string{
		"bd-g1": {"bd-g2"},
		"bd-g2": {"bd-g1", "bd-g3"},
		"bd-g3": {"bd-g2"},
	}

	for _, tc := range []struct {
		name       string
		args       []string
		env        string
		wantSource string
	}{
		{name: "cap_over_flag", args: []string{"--max-rows", "2", "bd-g1"}, wantSource: "--max-rows"},
		{name: "cap_over_env", env: "2", args: []string{"bd-g1"}, wantSource: maxRowsEnvVar},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(maxRowsEnvVar, tc.env)
			withStubbedProxiedGraph(t, chain, edges)

			err, got, stderr := runGraphCaptured(t, false, tc.args...)
			if code, ok := exitCodeFromError(err); !ok || code != 2 {
				t.Fatalf("exit = %v (code=%d typed=%v), want 2 (stderr=%q)", err, code, ok, stderr)
			}
			if got != "" {
				t.Errorf("stdout = %q, want nothing rendered under a refused cap", got)
			}
			wantFirst := fmt.Sprintf("Error: too many rows: 3 found, %s=2 exceeded.", tc.wantSource)
			if got := strings.SplitN(stderr, "\n", 2)[0]; got != wantFirst {
				t.Errorf("stderr first line = %q, want %q", got, wantFirst)
			}
			if strings.Contains(stderr, "not supported in proxied-server mode") {
				t.Errorf("the stale unsupported-cap refusal still fired: %q", stderr)
			}
		})
	}

	for _, tc := range []struct {
		name string
		args []string
		env  string
	}{
		{name: "cap_fits_flag", args: []string{"--max-rows", "3", "bd-g1"}},
		{name: "cap_fits_env", env: "3", args: []string{"bd-g1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(maxRowsEnvVar, tc.env)
			withStubbedProxiedGraph(t, chain, edges)

			// The uncapped invocation first; the capped graph must render
			// byte-identically.
			baseErr, want, baseStderr := runGraphCaptured(t, false, "bd-g1")
			if baseErr != nil {
				t.Fatalf("uncapped graph failed: %v (stderr=%q)", baseErr, baseStderr)
			}
			for _, id := range []string{"bd-g1", "bd-g2", "bd-g3"} {
				if !strings.Contains(want, id) {
					t.Fatalf("test setup: uncapped graph does not show %s:\n%s", id, want)
				}
			}
			err, got, stderr := runGraphCaptured(t, false, tc.args...)
			if err != nil {
				t.Fatalf("graph under a cap the component fits inside failed: %v (stderr=%q)", err, stderr)
			}
			if got != want {
				t.Errorf("capped graph differs from the uncapped one:\ncapped:\n%s\nuncapped:\n%s", got, want)
			}
		})
	}

	// The negative flag is a usage error raised in RunE, ahead of any ID
	// lookup: the ID does not resolve, and the run must say so about the
	// flag, not the ID. The nil provider is the sentinel — a passing
	// validation would open a UOW, and there is none to open.
	t.Run("negative_cap_precedes_id_lookup", func(t *testing.T) {
		pinProxiedServerMode(t)
		if uowProvider != nil {
			t.Fatal("precondition: uowProvider must be nil so the run cannot reach an ID lookup")
		}
		cmd := &cobra.Command{Use: "graph"}
		cmd.Flags().Int("max-rows", 0, "defensive row cap")
		if err := cmd.Flags().Parse([]string{"--max-rows", "-1"}); err != nil {
			t.Fatalf("parse graph args: %v", err)
		}
		var err error
		stderr := captureStderr(t, func() { err = graphCmd.RunE(cmd, []string{"bd-nosuch"}) })
		assertExitCode(t, err, 1)
		if !strings.Contains(stderr, "--max-rows must be non-negative") {
			t.Fatalf("stderr = %q, want the negative-cap usage error", stderr)
		}
		if strings.Contains(stderr, "not found") {
			t.Fatalf("the negative cap reached the ID lookup: %q", stderr)
		}
	})
}

// TestGraphAllHonorsMaxRowsUnderProxiedServer is the --all half: the cap
// binds each per-status search (open, in_progress, blocked), the direct
// route's documented behavior, not the union. Two issues per status stay
// whole under cap 2 — all six nodes appear, from both cap sources — while
// cap 1 refuses the first status whose window matched two, naming its
// source, with nothing rendered.
func TestGraphAllHonorsMaxRowsUnderProxiedServer(t *testing.T) {
	six := []*types.Issue{
		stubGraphIssue("bd-o1", types.StatusOpen),
		stubGraphIssue("bd-o2", types.StatusOpen),
		stubGraphIssue("bd-p1", types.StatusInProgress),
		stubGraphIssue("bd-p2", types.StatusInProgress),
		stubGraphIssue("bd-b1", types.StatusBlocked),
		stubGraphIssue("bd-b2", types.StatusBlocked),
	}

	for _, tc := range []struct {
		name       string
		args       []string
		env        string
		wantSource string
	}{
		{name: "per_status_cap_flag", args: []string{"--max-rows", "2"}, wantSource: "--max-rows"},
		{name: "per_status_cap_env", env: "2", wantSource: maxRowsEnvVar},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(maxRowsEnvVar, tc.env)
			stub := withStubbedProxiedGraph(t, six, nil)

			err, got, stderr := runGraphCaptured(t, true, tc.args...)
			if err != nil {
				t.Fatalf("graph --all under a per-status cap the set fits inside failed: %v (stderr=%q)", err, stderr)
			}
			for _, id := range []string{"bd-o1", "bd-o2", "bd-p1", "bd-p2", "bd-b1", "bd-b2"} {
				if !strings.Contains(got, id) {
					t.Errorf("graph --all output is missing %s:\n%s", id, got)
				}
			}
			if len(stub.filters) != 3 {
				t.Fatalf("the route ran %d per-status searches, want 3", len(stub.filters))
			}
			for _, f := range stub.filters {
				if f.MaxRows != 2 || f.MaxRowsSource != tc.wantSource {
					t.Errorf("a per-status search ran with MaxRows=%d Source=%q, want cap 2 from %s",
						f.MaxRows, f.MaxRowsSource, tc.wantSource)
				}
			}
		})
	}

	for _, tc := range []struct {
		name       string
		args       []string
		env        string
		wantSource string
	}{
		{name: "cap_over_flag", args: []string{"--max-rows", "1"}, wantSource: "--max-rows"},
		{name: "cap_over_env", env: "1", wantSource: maxRowsEnvVar},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(maxRowsEnvVar, tc.env)
			withStubbedProxiedGraph(t, six, nil)

			err, got, stderr := runGraphCaptured(t, true, tc.args...)
			if code, ok := exitCodeFromError(err); !ok || code != 2 {
				t.Fatalf("exit = %v (code=%d typed=%v), want 2 (stderr=%q)", err, code, ok, stderr)
			}
			if got != "" {
				t.Errorf("stdout = %q, want nothing rendered under a refused cap", got)
			}
			wantFirst := fmt.Sprintf("Error: too many rows: 2 found, %s=1 exceeded.", tc.wantSource)
			if got := strings.SplitN(stderr, "\n", 2)[0]; got != wantFirst {
				t.Errorf("stderr first line = %q, want %q", got, wantFirst)
			}
		})
	}
}
