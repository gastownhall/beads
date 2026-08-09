package main

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestMatchGatesToRuns_ScopesPerGateRepo is the core SF1 regression test:
// queryGitHubRunsInRepo previously built `gh run list` without --repo, and the
// discovery pass loaded every pending gh:run gate and matched those
// current-repo runs into all of them - a gate targeting another repository
// could be assigned a same-named workflow run from the current repo and
// become permanently undiscoverable (the persisted await_id pins it).
//
// matchGatesToRuns must instead query and match each gate only against runs
// from ITS OWN repo.
func TestMatchGatesToRuns_ScopesPerGateRepo(t *testing.T) {
	currentRepoGate := &types.Issue{
		ID:        "bd-local",
		AwaitType: "gh:run",
		AwaitID:   "release.yml",
		CreatedAt: time.Now(),
	}
	crossRepoGate := &types.Issue{
		ID:        "bd-cross",
		AwaitType: "gh:run",
		AwaitID:   "release.yml",
		CreatedAt: time.Now(),
		Metadata:  json.RawMessage(`{"repo":"other-owner/other-repo"}`),
	}

	queryCalls := map[string]int{}
	queryRuns := func(repo, workflowHint string) ([]GHWorkflowRun, error) {
		queryCalls[repo]++
		switch repo {
		case "":
			if workflowHint != "" {
				t.Errorf("current-repo query got workflowHint %q, want empty (only foreign queries are narrowed)", workflowHint)
			}
			return []GHWorkflowRun{{DatabaseID: 111, Name: "release", WorkflowName: "release.yml", Status: "in_progress", CreatedAt: time.Now()}}, nil
		case "other-owner/other-repo":
			if workflowHint != "release.yml" {
				t.Errorf("cross-repo query got workflowHint %q, want %q", workflowHint, "release.yml")
			}
			return []GHWorkflowRun{{DatabaseID: 222, Name: "release", WorkflowName: "release.yml", Status: "in_progress", CreatedAt: time.Now()}}, nil
		default:
			t.Fatalf("unexpected repo queried: %q", repo)
			return nil, nil
		}
	}

	results := matchGatesToRuns([]*types.Issue{currentRepoGate, crossRepoGate}, 30*time.Minute, queryRuns)
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}

	for _, r := range results {
		if r.err != nil {
			t.Fatalf("gate %s: unexpected error: %v", r.gate.ID, r.err)
		}
		if r.run == nil {
			t.Fatalf("gate %s: expected a matched run", r.gate.ID)
		}
		switch r.gate.ID {
		case "bd-local":
			if r.run.DatabaseID != 111 {
				t.Errorf("bd-local matched run %d, want 111 (current repo's run)", r.run.DatabaseID)
			}
		case "bd-cross":
			if r.run.DatabaseID != 222 {
				t.Errorf("bd-cross matched run %d, want 222 (other-owner/other-repo's run) - a cross-repo gate must never be assigned the current repo's run", r.run.DatabaseID)
			}
		}
	}

	if queryCalls[""] != 1 {
		t.Errorf("queried current repo %d times, want 1 (per-repo caching)", queryCalls[""])
	}
	if queryCalls["other-owner/other-repo"] != 1 {
		t.Errorf("queried other-owner/other-repo %d times, want 1", queryCalls["other-owner/other-repo"])
	}
}

// TestMatchGatesToRuns_RejectsInvalidRepoMetadata covers the SF1/SF3
// interaction: a gate with malformed repo metadata must be reported as an
// error, never silently matched against the current repo's runs.
func TestMatchGatesToRuns_RejectsInvalidRepoMetadata(t *testing.T) {
	gate := &types.Issue{
		ID:        "bd-bad-repo",
		AwaitType: "gh:run",
		AwaitID:   "release.yml",
		CreatedAt: time.Now(),
		Metadata:  json.RawMessage(`{"repo":null}`),
	}

	queried := false
	queryRuns := func(repo, workflowHint string) ([]GHWorkflowRun, error) {
		queried = true
		return nil, nil
	}

	results := matchGatesToRuns([]*types.Issue{gate}, 30*time.Minute, queryRuns)
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].err == nil {
		t.Fatal("expected an error for malformed repo metadata, got nil (silent fallback)")
	}
	if results[0].run != nil {
		t.Fatalf("expected no matched run for malformed repo metadata, got %+v", results[0].run)
	}
	if queried {
		t.Error("expected no GitHub query for a gate with malformed repo metadata")
	}
}

// TestMatchGatesToRuns_CachesQueryErrorPerRepo verifies a failing query for a
// repo is attempted once and its error applied to every gate sharing that
// repo, rather than re-querying (or silently matching against an empty run
// list) per gate.
func TestMatchGatesToRuns_CachesQueryErrorPerRepo(t *testing.T) {
	gateA := &types.Issue{ID: "bd-a", AwaitType: "gh:run", AwaitID: "release.yml", CreatedAt: time.Now()}
	gateB := &types.Issue{ID: "bd-b", AwaitType: "gh:run", AwaitID: "release.yml", CreatedAt: time.Now()}

	queryCalls := 0
	wantErr := errors.New("gh run list failed: rate limited")
	queryRuns := func(repo, workflowHint string) ([]GHWorkflowRun, error) {
		queryCalls++
		return nil, wantErr
	}

	results := matchGatesToRuns([]*types.Issue{gateA, gateB}, 30*time.Minute, queryRuns)
	if queryCalls != 1 {
		t.Errorf("queryRuns called %d times, want 1 (cached per repo)", queryCalls)
	}
	for _, r := range results {
		if !errors.Is(r.err, wantErr) {
			t.Errorf("gate %s: err = %v, want %v", r.gate.ID, r.err, wantErr)
		}
	}
}

// TestMatchGatesToRuns_ForeignGateWithoutHintNeverQueried covers the query
// half of the "cross-repo discovery requires a hint" fix: a foreign gate
// with no workflow name hint can never match (matchGateToRun always returns
// nil for it), so matchGatesToRuns must skip the GitHub query entirely for
// it rather than spend an API call on a gate that can't be discovered.
func TestMatchGatesToRuns_ForeignGateWithoutHintNeverQueried(t *testing.T) {
	gate := &types.Issue{
		ID:        "bd-cross-no-hint",
		AwaitType: "gh:run",
		CreatedAt: time.Now(),
		Metadata:  json.RawMessage(`{"repo":"other-owner/other-repo"}`),
	}

	queried := false
	queryRuns := func(repo, workflowHint string) ([]GHWorkflowRun, error) {
		queried = true
		return nil, nil
	}

	results := matchGatesToRuns([]*types.Issue{gate}, 30*time.Minute, queryRuns)
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].err != nil {
		t.Errorf("gate %s: unexpected error: %v", results[0].gate.ID, results[0].err)
	}
	if results[0].run != nil {
		t.Errorf("expected no matched run for a hintless foreign gate, got %+v", results[0].run)
	}
	if queried {
		t.Error("expected no GitHub query for a foreign gate with no workflow hint")
	}
}

// TestGateDiscoveryQueryFailures_DetectsFailedQueries covers the SF-fix
// exit-code regression: before per-repo scoping, any GitHub query error was
// fatal (HandleError, exit 1). After scoping queries per gate repo, a failed
// query became just another per-gate stderr line and the command exited 0
// with "Updated 0 gate(s)". gateDiscoveryQueryFailures is what
// runGateDiscover uses to detect that at least one repo's query failed so it
// can still return HandleError - a wholly-failed discovery must not exit 0.
func TestGateDiscoveryQueryFailures_DetectsFailedQueries(t *testing.T) {
	gateA := &types.Issue{ID: "bd-a", AwaitType: "gh:run", AwaitID: "release.yml", CreatedAt: time.Now()}
	gateB := &types.Issue{ID: "bd-b", AwaitType: "gh:run", AwaitID: "release.yml", CreatedAt: time.Now(),
		Metadata: json.RawMessage(`{"repo":"other-owner/other-repo"}`)}

	wantErrA := errors.New("gh CLI not found")
	wantErrB := errors.New("rate limited")
	queryRuns := func(repo, workflowHint string) ([]GHWorkflowRun, error) {
		if repo == "other-owner/other-repo" {
			return nil, wantErrB
		}
		return nil, wantErrA
	}

	matches := matchGatesToRuns([]*types.Issue{gateA, gateB}, 30*time.Minute, queryRuns)

	failures := gateDiscoveryQueryFailures(matches)
	if len(failures) != 2 {
		t.Fatalf("gateDiscoveryQueryFailures returned %d failures, want 2: %+v", len(failures), failures)
	}
	if !errors.Is(failures[""], wantErrA) {
		t.Errorf("failures[\"\"] = %v, want %v", failures[""], wantErrA)
	}
	if !errors.Is(failures["other-owner/other-repo"], wantErrB) {
		t.Errorf("failures[\"other-owner/other-repo\"] = %v, want %v", failures["other-owner/other-repo"], wantErrB)
	}
}

// TestGateDiscoveryQueryFailures_IgnoresMetadataErrors verifies that invalid
// repo metadata (a per-gate data problem, not a failure to reach GitHub at
// all) is not treated as a query failure - it must not, by itself, make
// runGateDiscover exit non-zero.
func TestGateDiscoveryQueryFailures_IgnoresMetadataErrors(t *testing.T) {
	gate := &types.Issue{ID: "bd-bad-repo", AwaitType: "gh:run", AwaitID: "release.yml", CreatedAt: time.Now(),
		Metadata: json.RawMessage(`{"repo":null}`)}

	queryRuns := func(repo, workflowHint string) ([]GHWorkflowRun, error) {
		t.Fatal("expected no GitHub query for a gate with malformed repo metadata")
		return nil, nil
	}

	matches := matchGatesToRuns([]*types.Issue{gate}, 30*time.Minute, queryRuns)
	if failures := gateDiscoveryQueryFailures(matches); len(failures) != 0 {
		t.Errorf("gateDiscoveryQueryFailures = %+v, want none (metadata errors are not query failures)", failures)
	}
}

// TestBranchFilterForRepo covers the cross-repo-discovery-is-inert fix: an
// AUTO-DETECTED local branch filter must never be forwarded to a foreign
// repo's query - a cross-repo gate's target branch has no relationship to
// the branch checked out locally, so filtering that repo's runs by it would
// match nothing, forever. But an EXPLICIT `--branch` must survive for a
// foreign repo too: it is a deliberate instruction about the target repo's
// branch, not a guess about the local checkout, and dropping it silently
// would make `bd gate discover --branch main` appear to work cross-repo
// while actually doing nothing.
func TestBranchFilterForRepo(t *testing.T) {
	tests := []struct {
		name              string
		localBranchFilter string
		repo              string
		userSpecified     bool
		want              string
	}{
		{name: "current repo keeps auto-detected branch filter", localBranchFilter: "feature/foo", repo: "", userSpecified: false, want: "feature/foo"},
		{name: "current repo keeps explicit branch filter", localBranchFilter: "main", repo: "", userSpecified: true, want: "main"},
		{name: "foreign repo drops auto-detected branch filter", localBranchFilter: "feature/foo", repo: "other-owner/other-repo", userSpecified: false, want: ""},
		{name: "foreign repo drops empty auto-detected branch filter too", localBranchFilter: "", repo: "other-owner/other-repo", userSpecified: false, want: ""},
		{name: "foreign repo keeps explicit branch filter", localBranchFilter: "main", repo: "other-owner/other-repo", userSpecified: true, want: "main"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := branchFilterForRepo(tt.localBranchFilter, tt.repo, tt.userSpecified); got != tt.want {
				t.Errorf("branchFilterForRepo(%q, %q, %v) = %q, want %q", tt.localBranchFilter, tt.repo, tt.userSpecified, got, tt.want)
			}
		})
	}
}

// TestMatchGateToRun_ForeignRepoWithoutHintNeverMatches covers the follow-up
// fix to the cross-repo-discovery-is-inert bug: neutralizing the commit/
// branch heuristics for a foreign repo (see the test below) is not enough by
// itself - a hintless foreign gate can still reach bestScore >= 30 on time
// proximity (+30) plus an in-progress/queued run (+5) alone, and pinning that
// run's ID onto the gate would be just as wrong as the commit/branch
// coincidence this fix set out to prevent (the persisted await_id pins the
// gate permanently). A foreign gate therefore must never match without a
// workflow name hint, full stop - regardless of what score the remaining
// heuristics would otherwise produce.
func TestMatchGateToRun_ForeignRepoWithoutHintNeverMatches(t *testing.T) {
	gate := &types.Issue{
		ID:        "bd-cross-no-hint",
		AwaitType: "gh:run",
		// AwaitID intentionally empty: no workflow name hint.
		CreatedAt: time.Now(),
	}
	runs := []GHWorkflowRun{
		{
			DatabaseID: 777,
			Status:     "in_progress", // +5
			CreatedAt:  time.Now(),    // within 5 minutes of gate.CreatedAt: +30
		},
	}

	// Sanity check: the same run does score >= 30 for a non-foreign gate on
	// time proximity + status alone (commit/branch won't coincidentally
	// match empty HeadSha/HeadBranch against this repo's real values), so
	// the foreign case below isn't vacuously nil for an unrelated reason.
	if got := matchGateToRun(gate, runs, 30*time.Minute, false); got == nil {
		t.Fatal("non-foreign gate: expected time-proximity+status alone to select the run")
	}

	if got := matchGateToRun(gate, runs, 30*time.Minute, true); got != nil {
		t.Errorf("foreign gate without a workflow hint: expected no match, got run %d", got.DatabaseID)
	}
}

// TestMatchGateToRun_NeutralizesLocalHeuristicsForForeignRepo covers the
// scoring half of the cross-repo-discovery-is-inert fix: run.HeadSha and
// run.HeadBranch describe a run in the FOREIGN repo, so comparing them
// against the local checkout's commit/branch is meaningless (and could
// spuriously "match" by coincidence). A run that would only match a
// non-foreign gate by commit/branch heuristics must not match a foreign one.
// (This gate has no workflow hint either, so the result is now also covered
// by TestMatchGateToRun_ForeignRepoWithoutHintNeverMatches's stricter,
// hint-independent guard - both are kept because they document distinct
// failure modes the fix addresses.)
func TestMatchGateToRun_NeutralizesLocalHeuristicsForForeignRepo(t *testing.T) {
	localCommit := getGitCommitForGateDiscovery()
	localBranch := getGitBranchForGateDiscovery()
	if localCommit == "" || localBranch == "" {
		t.Skip("test requires a git checkout with a resolvable commit and branch")
	}

	gate := &types.Issue{
		ID:        "bd-cross",
		AwaitType: "gh:run",
		CreatedAt: time.Now().Add(-2 * time.Hour),
	}
	runs := []GHWorkflowRun{
		{
			DatabaseID: 999,
			HeadSha:    localCommit,
			HeadBranch: localBranch,
			Status:     "completed",
			// 1h away from gate.CreatedAt: outside all three time-proximity
			// bands (5/10/30 min), so only the commit/branch heuristics can
			// produce a score here.
			CreatedAt: time.Now().Add(-time.Hour),
		},
	}

	if got := matchGateToRun(gate, runs, 3*time.Hour, false); got == nil {
		t.Fatal("non-foreign gate: expected commit+branch match to select the run")
	}

	if got := matchGateToRun(gate, runs, 3*time.Hour, true); got != nil {
		t.Errorf("foreign gate: expected no match (commit/branch heuristics neutralized), got run %d", got.DatabaseID)
	}
}

func TestQueryGitHubRunsInRepoWithRunner(t *testing.T) {
	runs, err := queryGitHubRunsInRepoWithRunner(
		"main",
		10,
		"srobroek/agentic-packages",
		"",
		fakeGHRunner(t,
			`[{"databaseId":555,"name":"release","status":"completed","conclusion":"success","workflowName":"release.yml"}]`,
			"run", "list", "--json", "databaseId,displayTitle,headBranch,headSha,name,status,conclusion,createdAt,updatedAt,workflowName,url", "--limit", "10", "--branch", "main", "--repo", "srobroek/agentic-packages",
		),
	)
	if err != nil {
		t.Fatalf("queryGitHubRunsInRepoWithRunner returned error: %v", err)
	}
	if len(runs) != 1 || runs[0].DatabaseID != 555 {
		t.Fatalf("runs = %#v, want one run with database ID 555", runs)
	}
}

func TestQueryGitHubRunsInRepoWithRunner_OmitsRepoFlagForCurrentRepo(t *testing.T) {
	_, err := queryGitHubRunsInRepoWithRunner(
		"",
		10,
		"",
		"",
		fakeGHRunner(t,
			`[]`,
			"run", "list", "--json", "databaseId,displayTitle,headBranch,headSha,name,status,conclusion,createdAt,updatedAt,workflowName,url", "--limit", "10",
		),
	)
	if err != nil {
		t.Fatalf("queryGitHubRunsInRepoWithRunner returned error: %v", err)
	}
}

// TestQueryGitHubRunsInRepoWithRunner_AddsWorkflowFlagForForeignRepo covers
// the narrowing half of the cross-repo-discovery-is-inert fix: a foreign
// repo's query is narrowed with --workflow when matchGatesToRuns has a hint,
// recovering the visibility --limit would otherwise cost an unfiltered
// cross-repo query.
func TestQueryGitHubRunsInRepoWithRunner_AddsWorkflowFlagForForeignRepo(t *testing.T) {
	runs, err := queryGitHubRunsInRepoWithRunner(
		"",
		10,
		"srobroek/agentic-packages",
		"release.yml",
		fakeGHRunner(t,
			`[]`,
			"run", "list", "--json", "databaseId,displayTitle,headBranch,headSha,name,status,conclusion,createdAt,updatedAt,workflowName,url", "--limit", "10", "--repo", "srobroek/agentic-packages", "--workflow", "release.yml",
		),
	)
	if err != nil {
		t.Fatalf("queryGitHubRunsInRepoWithRunner returned error: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs = %#v, want none", runs)
	}
}
