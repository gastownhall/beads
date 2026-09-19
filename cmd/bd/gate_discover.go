package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// GHWorkflowRun represents a GitHub workflow run from `gh run list --json`
type GHWorkflowRun struct {
	DatabaseID   int64     `json:"databaseId"`
	DisplayTitle string    `json:"displayTitle"`
	HeadBranch   string    `json:"headBranch"`
	HeadSha      string    `json:"headSha"`
	Name         string    `json:"name"`
	Status       string    `json:"status"`
	Conclusion   string    `json:"conclusion,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
	WorkflowName string    `json:"workflowName"`
	URL          string    `json:"url"`
}

// gateDiscoverCmd discovers GitHub run IDs for gh:run gates
var gateDiscoverCmd = &cobra.Command{
	Use:   "discover",
	Short: "Discover await_id for gh:run gates",
	Long: `Discovers GitHub workflow run IDs for gates awaiting CI/CD completion.

This command finds open gates with await_type="gh:run" that don't have an await_id,
queries recent GitHub workflow runs, and matches them using heuristics:
  - Branch name matching
  - Commit SHA matching
  - Time proximity (runs within 5 minutes of gate creation)

Once matched, the gate's await_id is updated with the GitHub run ID, enabling
subsequent polling to check the run's status.

A gate whose metadata.repo targets another repository is only matched
against runs queried from that repository, never against the current
repository's runs of a same-named workflow.

Examples:
  bd gate discover           # Auto-discover run IDs for all matching gates
  bd gate discover --dry-run # Preview what would be matched (no updates)
  bd gate discover --branch main --limit 10  # Only match runs on 'main' branch`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runGateDiscover,
}

func init() {
	gateDiscoverCmd.Flags().BoolP("dry-run", "n", false, "Preview mode: show matches without updating")
	gateDiscoverCmd.Flags().StringP("branch", "b", "", "Filter runs by branch (default: current branch)")
	gateDiscoverCmd.Flags().IntP("limit", "l", 10, "Max runs to query from GitHub")
	gateDiscoverCmd.Flags().DurationP("max-age", "a", 30*time.Minute, "Max age for gate/run matching")

	gateCmd.AddCommand(gateDiscoverCmd)
}

func runGateDiscover(cmd *cobra.Command, args []string) error {
	if usesProxiedServer() {
		return HandleErrorRespectJSON("gate discover is not supported in proxied-server mode")
	}
	CheckReadonly("gate discover")

	evt := metrics.NewCommandEvent("gate-discover")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	dryRun, _ := cmd.Flags().GetBool("dry-run")
	branchFilter, _ := cmd.Flags().GetString("branch")
	limit, _ := cmd.Flags().GetInt("limit")
	maxAge, _ := cmd.Flags().GetDuration("max-age")

	ctx := rootCtx

	gates, err := findPendingGates()
	if err != nil {
		return HandleError("finding gates: %v", err)
	}

	if len(gates) == 0 {
		fmt.Println("No pending gh:run gates found (all gates have numeric run IDs)")
		return nil
	}

	fmt.Printf("%s Found %d gate(s) awaiting run ID discovery\n\n", ui.RenderAccent("🔍"), len(gates))

	// userSpecifiedBranch must be captured BEFORE the auto-detect fallback
	// below overwrites branchFilter with the local branch - otherwise
	// branchFilterForRepo can never tell "the user explicitly asked to
	// filter by this branch" apart from "this is just the local branch we
	// defaulted to", and an explicit `--branch` is dropped for every
	// cross-repo gate exactly like the un-requested auto-detected one is.
	userSpecifiedBranch := branchFilter != ""
	if !userSpecifiedBranch {
		branchFilter = getGitBranchForGateDiscovery()
	}

	// Scope run discovery per gate's own repo selector (SF1): a gate whose
	// metadata targets another repository must only be matched against runs
	// queried FROM that repository, never against a same-named workflow run
	// from the current repo. matchGatesToRuns groups gates by their
	// validated repo and issues one query per distinct repo.
	matches := matchGatesToRuns(gates, maxAge, func(repo, workflowHint string) ([]GHWorkflowRun, error) {
		return queryGitHubRunsInRepo(branchFilterForRepo(branchFilter, repo, userSpecifiedBranch), limit, repo, workflowHint)
	})

	// Step 3/4: report matches (and, outside dry-run, persist them)
	matchCount := 0
	for _, m := range matches {
		if m.err != nil {
			fmt.Fprintf(os.Stderr, "  %s %s - %v\n",
				ui.RenderFail("✗"), ui.RenderID(m.gate.ID), m.err)
			continue
		}
		if m.run == nil {
			if jsonOutput {
				continue
			}
			fmt.Printf("  %s %s - no matching run found\n",
				ui.RenderFail("✗"), ui.RenderID(m.gate.ID))
			continue
		}

		matchCount++
		runIDStr := strconv.FormatInt(m.run.DatabaseID, 10)

		if dryRun {
			fmt.Printf("  %s %s → run %s (%s) [dry-run]\n",
				ui.RenderPass("✓"), ui.RenderID(m.gate.ID), runIDStr, m.run.Status)
			continue
		}

		if err := updateGateAwaitID(ctx, m.gate.ID, runIDStr); err != nil {
			fmt.Fprintf(os.Stderr, "  %s %s - update failed: %v\n",
				ui.RenderFail("✗"), ui.RenderID(m.gate.ID), err)
			continue
		}

		fmt.Printf("  %s %s → run %s (%s)\n",
			ui.RenderPass("✓"), ui.RenderID(m.gate.ID), runIDStr, m.run.Status)
	}

	fmt.Println()
	if dryRun {
		fmt.Printf("Would update %d gate(s). Run without --dry-run to apply.\n", matchCount)
	} else {
		fmt.Printf("Updated %d gate(s) with discovered run IDs.\n", matchCount)
	}

	// A GitHub query failure (gh missing, unauthenticated, rate limited, ...)
	// is fatal, matching pre-multi-repo behavior: before per-repo scoping,
	// any query error returned HandleError immediately. Per-gate detail was
	// already reported above; report a summary here and exit non-zero so a
	// wholly-failed discovery is never mistaken for "0 gates matched".
	if failures := gateDiscoveryQueryFailures(matches); len(failures) > 0 {
		repos := make([]string, 0, len(failures))
		for repo := range failures {
			repos = append(repos, repo)
		}
		sort.Strings(repos)
		details := make([]string, 0, len(repos))
		for _, repo := range repos {
			label := repo
			if label == "" {
				label = "current repo"
			}
			details = append(details, fmt.Sprintf("%s: %v", label, failures[repo]))
		}
		return HandleError("querying GitHub runs failed for %d repo(s): %s", len(failures), strings.Join(details, "; "))
	}

	return nil
}

// gateDiscoveryMatch pairs a gate with the run matched for it, or an error
// explaining why it could not be matched (invalid repo metadata, or the
// GitHub query for its repo failed).
type gateDiscoveryMatch struct {
	gate *types.Issue
	run  *GHWorkflowRun
	err  error
}

// gateQueryError wraps a failed GitHub query for a specific repo. It is
// distinguished from other gateDiscoveryMatch errors (e.g. invalid repo
// metadata) so runGateDiscover can tell "we couldn't even ask GitHub"
// (gh missing, unauthenticated, rate limited, ...) apart from a per-gate
// data problem: a wholly-failed discovery must exit non-zero, matching the
// pre-multi-repo behavior where a query failure was fatal.
type gateQueryError struct {
	repo string
	err  error
}

func (e *gateQueryError) Error() string { return e.err.Error() }
func (e *gateQueryError) Unwrap() error { return e.err }

// gateDiscoveryQueryFailures returns the distinct repos whose GitHub query
// failed among matches, keyed by repo ("" meaning the current repository)
// with the query error that repo produced.
func gateDiscoveryQueryFailures(matches []gateDiscoveryMatch) map[string]error {
	failures := make(map[string]error)
	for _, m := range matches {
		var qe *gateQueryError
		if errors.As(m.err, &qe) {
			failures[qe.repo] = qe.err
		}
	}
	return failures
}

// branchFilterForRepo returns the branch filter to use when querying a gate's
// repo. It always applies the branch filter to the current repo (repo ==
// ""). For a foreign repo it drops an auto-detected local branch - a
// cross-repo gate's target branch has no relationship to the branch checked
// out locally, so `gh run list --repo <other> --branch <local-branch>` would
// filter out every run in that repo and the gate would never be discoverable
// (this was the cross-repo-discovery-is-inert bug) - but keeps a branch the
// user explicitly passed via `--branch`: an explicit filter is a deliberate
// instruction about the TARGET repo's branch, not a guess about the local
// checkout, and dropping it silently for cross-repo gates would make
// `bd gate discover --branch main` appear to work while doing nothing.
func branchFilterForRepo(localBranchFilter, repo string, userSpecified bool) string {
	if repo != "" && !userSpecified {
		return ""
	}
	return localBranchFilter
}

// matchGatesToRuns scopes run discovery per gate's own repo selector (SF1).
// Gates are grouped by their validated metadata.repo (via githubRepoFromIssue;
// "" means the current repository), and queryRuns is called at most once per
// distinct (repo, workflow hint) pair among the given gates. A gate is only
// ever matched against runs queried from ITS repo - never against another
// repo's runs of a same-named workflow, which would otherwise persist the
// wrong await_id permanently (the persisted ID pins the gate).
//
// queryRuns receives a workflowHint - the gate's AwaitID workflow name hint,
// non-empty only for a foreign (cross-repo) query - so it can narrow the
// `gh run list` call with --workflow. Without that narrowing, a busy foreign
// repo's unfiltered recent-run list (capped by --limit) might never surface
// the specific workflow a gate is waiting on. The current repo's query is
// never narrowed this way (workflowHint is always "" for it), matching
// pre-existing `bd gate discover` behavior for local gates.
func matchGatesToRuns(gates []*types.Issue, maxAge time.Duration, queryRuns func(repo, workflowHint string) ([]GHWorkflowRun, error)) []gateDiscoveryMatch {
	runsByKey := make(map[string][]GHWorkflowRun)
	queryErrByKey := make(map[string]error)
	results := make([]gateDiscoveryMatch, 0, len(gates))

	for _, gate := range gates {
		repo, repoErr := githubRepoFromIssue(gate)
		if repoErr != nil {
			results = append(results, gateDiscoveryMatch{gate: gate, err: fmt.Errorf("invalid repo metadata: %w", repoErr)})
			continue
		}

		foreign := repo != ""
		hint := getWorkflowNameHint(gate)

		// Cross-repo discovery requires a workflow hint. With local-commit/
		// local-branch heuristics neutralized for a foreign repo (see
		// matchGateToRun), a hintless gate could only ever score on time
		// proximity alone and risk pinning the wrong run in another
		// repository permanently. Skip the query entirely rather than spend
		// a GitHub API call on a gate that can never match.
		if foreign && hint == "" {
			results = append(results, gateDiscoveryMatch{gate: gate})
			continue
		}

		queryHint := ""
		key := repo
		if foreign {
			queryHint = hint
			key = repo + "\x1f" + hint
		}

		runs, cached := runsByKey[key]
		if !cached {
			if qErr, queried := queryErrByKey[key]; queried {
				results = append(results, gateDiscoveryMatch{gate: gate, err: &gateQueryError{repo: repo, err: qErr}})
				continue
			}
			queried, err := queryRuns(repo, queryHint)
			if err != nil {
				queryErrByKey[key] = err
				results = append(results, gateDiscoveryMatch{gate: gate, err: &gateQueryError{repo: repo, err: err}})
				continue
			}
			runsByKey[key] = queried
			runs = queried
		}

		// A gate's local-commit/local-branch heuristics only make sense
		// against the current repo's runs; a foreign repo (repo != "") never
		// shares a commit SHA or branch name with the local checkout, so
		// those heuristics are neutralized for it (see matchGateToRun).
		results = append(results, gateDiscoveryMatch{gate: gate, run: matchGateToRun(gate, runs, maxAge, foreign)})
	}

	return results
}

// isNumericRunID returns true if the string looks like a GitHub numeric run ID.
// This is a local alias for consistency - the canonical implementation is isNumericID in gate.go.
func isNumericRunID(s string) bool {
	return isNumericID(s)
}

// needsDiscovery returns true if a gh:run gate needs run ID discovery.
// This is true when AwaitID is empty OR contains a non-numeric workflow name hint.
func needsDiscovery(g *types.Issue) bool {
	if g.AwaitType != "gh:run" {
		return false
	}
	// Empty AwaitID or non-numeric (workflow name hint) needs discovery
	return g.AwaitID == "" || !isNumericRunID(g.AwaitID)
}

// getWorkflowNameHint extracts the workflow name hint from AwaitID if present.
// Returns empty string if AwaitID is empty or numeric (already resolved).
func getWorkflowNameHint(g *types.Issue) string {
	if g.AwaitID == "" || isNumericRunID(g.AwaitID) {
		return ""
	}
	return g.AwaitID
}

// workflowNameMatches checks if a workflow hint matches a GitHub workflow run.
// It handles various naming conventions:
//   - Exact match (case-insensitive)
//   - Hint with .yml/.yaml suffix vs display name without
//   - Hint without suffix vs filename with .yml/.yaml
func workflowNameMatches(hint, workflowName, runName string) bool {
	// Normalize hint by removing .yml/.yaml suffix for comparison
	hintBase := strings.TrimSuffix(strings.TrimSuffix(hint, ".yml"), ".yaml")

	// Exact matches (case-insensitive)
	if strings.EqualFold(workflowName, hint) || strings.EqualFold(runName, hint) {
		return true
	}

	// Match hint base against workflow display name
	if strings.EqualFold(workflowName, hintBase) {
		return true
	}

	// Match hint (with suffix added) against run filename
	if strings.EqualFold(runName, hintBase+".yml") || strings.EqualFold(runName, hintBase+".yaml") {
		return true
	}

	return false
}

// findPendingGates returns open gh:run gates that need run ID discovery.
// This includes gates with empty AwaitID OR non-numeric AwaitID (workflow name hint).
func findPendingGates() ([]*types.Issue, error) {
	var gates []*types.Issue

	gateType := types.IssueType("gate")
	filter := types.IssueFilter{
		IssueType:     &gateType,
		ExcludeStatus: []types.Status{types.StatusClosed},
	}

	allGates, err := store.SearchIssues(rootCtx, "", filter)
	if err != nil {
		return nil, fmt.Errorf("search gates: %w", err)
	}

	for _, g := range allGates {
		if needsDiscovery(g) {
			gates = append(gates, g)
		}
	}

	return gates, nil
}

// getGitBranchForGateDiscovery returns the current git branch name
// Uses CWD repo context since this is for user's project CI discovery
func getGitBranchForGateDiscovery() string {
	rc, err := beads.GetRepoContext()
	if err != nil {
		return "main" // Default fallback
	}

	cmd := rc.GitCmdCWD(context.Background(), "rev-parse", "--abbrev-ref", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return "main" // Default fallback
	}
	return strings.TrimSpace(string(output))
}

// getGitCommitForGateDiscovery returns the current git commit SHA
// Uses CWD repo context since this is for user's project CI discovery
func getGitCommitForGateDiscovery() string {
	rc, err := beads.GetRepoContext()
	if err != nil {
		return ""
	}

	cmd := rc.GitCmdCWD(context.Background(), "rev-parse", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// queryGitHubRunsInRepo queries recent workflow runs from GitHub using gh
// CLI, scoped to repo ("" means the current repository) and optionally
// narrowed to a single workflow (workflow == "" queries all workflows). This
// is the query path for `bd gate discover`'s branch/heuristic matching (SF1)
// - distinct from queryGitHubRunsForWorkflowInRepo in gate.go, which filters
// by a specific --workflow name for the direct await_id discovery used by
// `bd gate check`. matchGatesToRuns only ever passes a non-empty workflow for
// a foreign repo, to recover the visibility --limit would otherwise cost an
// unfiltered cross-repo query (see matchGatesToRuns).
func queryGitHubRunsInRepo(branch string, limit int, repo string, workflow string) ([]GHWorkflowRun, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, fmt.Errorf("gh CLI not found: install from https://cli.github.com")
	}
	return queryGitHubRunsInRepoWithRunner(branch, limit, repo, workflow, runGHCommand)
}

func queryGitHubRunsInRepoWithRunner(branch string, limit int, repo string, workflow string, runGH ghCommandRunner) ([]GHWorkflowRun, error) {
	args := []string{
		"run", "list",
		"--json", "databaseId,displayTitle,headBranch,headSha,name,status,conclusion,createdAt,updatedAt,workflowName,url",
		"--limit", strconv.Itoa(limit),
	}

	if branch != "" {
		args = append(args, "--branch", branch)
	}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	if workflow != "" {
		args = append(args, "--workflow", workflow)
	}

	output, stderr, err := runGH(args...)
	if err != nil {
		if len(stderr) > 0 {
			return nil, fmt.Errorf("gh run list failed: %s", string(stderr))
		}
		return nil, fmt.Errorf("gh run list: %w", err)
	}

	var runs []GHWorkflowRun
	if err := json.Unmarshal(output, &runs); err != nil {
		return nil, fmt.Errorf("parse gh output: %w", err)
	}

	return runs, nil
}

// matchGateToRun finds the best matching run for a gate using heuristics.
// If the gate has a workflow name hint in AwaitID, only runs matching that workflow are considered.
//
// foreignRepo must be true when runs were queried from a repo other than the
// current one (SF1: a gate whose metadata.repo targets another repository).
// In that case the local commit SHA and branch name are meaningless - they
// describe the current checkout, not the foreign repo - so the commit/branch
// heuristics are skipped entirely rather than comparing against them anyway.
func matchGateToRun(gate *types.Issue, runs []GHWorkflowRun, maxAge time.Duration, foreignRepo bool) *GHWorkflowRun {
	workflowHint := getWorkflowNameHint(gate)

	// Cross-repo discovery requires a workflow hint. With the commit/branch
	// heuristics below neutralized for a foreign repo, a hintless gate could
	// otherwise reach bestScore >= 30 on time proximity (+ in-progress/queued
	// status) alone and pin the wrong run in another repository permanently -
	// matchGatesToRuns already skips the query for this case, but guard here
	// too so any other caller gets the same safety.
	if foreignRepo && workflowHint == "" {
		return nil
	}

	now := time.Now()
	var currentCommit, currentBranch string
	if !foreignRepo {
		currentCommit = getGitCommitForGateDiscovery()
		currentBranch = getGitBranchForGateDiscovery()
	}

	var bestMatch *GHWorkflowRun
	var bestScore int

	for i := range runs {
		run := &runs[i]
		score := 0

		// Skip runs that are too old
		if now.Sub(run.CreatedAt) > maxAge {
			continue
		}

		// If gate has a workflow name hint, require matching workflow
		// Match against both WorkflowName (display name) and Name (filename)
		if workflowHint != "" {
			workflowMatches := workflowNameMatches(workflowHint, run.WorkflowName, run.Name)
			if !workflowMatches {
				continue // Skip runs that don't match the workflow hint
			}
			// Workflow match is a strong signal
			score += 200
		}

		// Heuristic 1: Commit SHA match (strongest signal after workflow
		// match). Skipped for a foreign repo (currentCommit is "" there).
		if currentCommit != "" && run.HeadSha == currentCommit {
			score += 100
		}

		// Heuristic 2: Branch match. Skipped for a foreign repo (see
		// Heuristic 1) and mirrors its currentCommit != "" guard: if local
		// branch detection failed, currentBranch is "" and a run with an
		// empty HeadBranch (if GitHub ever returns one) must not
		// accidentally "match" it.
		if !foreignRepo && currentBranch != "" && run.HeadBranch == currentBranch {
			score += 50
		}

		// Heuristic 3: Time proximity to gate creation
		// Closer in time = higher score
		timeDiff := run.CreatedAt.Sub(gate.CreatedAt).Abs()
		if timeDiff < 5*time.Minute {
			score += 30
		} else if timeDiff < 10*time.Minute {
			score += 20
		} else if timeDiff < 30*time.Minute {
			score += 10
		}

		// Heuristic 4: Prefer in_progress or queued runs (more likely to be current)
		if run.Status == "in_progress" || run.Status == "queued" {
			score += 5
		}

		if score > bestScore {
			bestScore = score
			bestMatch = run
		}
	}

	// Require at least some confidence in the match
	// With workflow hint, workflow match (200) alone is sufficient
	// Without workflow hint, require branch or commit match (30+ from time proximity)
	if bestScore >= 30 {
		return bestMatch
	}

	return nil
}

// updateGateAwaitID updates a gate's await_id field
func updateGateAwaitID(_ interface{}, gateID, runID string) error {
	updates := map[string]interface{}{
		"await_id": runID,
	}
	if err := store.UpdateIssue(rootCtx, gateID, updates, actor); err != nil {
		return err
	}
	return nil
}
