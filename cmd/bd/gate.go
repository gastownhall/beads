package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/httpclient"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/issueops"
)

// gateCmd is the parent command for gate operations
var gateCmd = &cobra.Command{
	Use:     "gate",
	GroupID: "issues",
	Short:   "Manage async coordination gates",
	Long: `Gates are async wait conditions that block workflow steps.

Gates are created automatically when a formula step has a gate field.
They must be closed (manually or via watchers) for the blocked step to proceed.

Gate types:
  human   - Requires manual bd close (Phase 1)
  timer   - Expires after timeout (Phase 2)
  gh:run  - Waits for GitHub workflow (Phase 3)
  gh:pr   - Waits for PR merge (Phase 3)
  gl:pipeline - Waits for GitLab pipeline success
  gl:mr   - Waits for GitLab merge request merge
  bead    - Waits for another bead to close (Phase 4)

For bead gates, await_id may be a bead ID in this rig's database (e.g.,
"bd-abc123") or the historical cross-rig form <rig>:<bead-id>. Cross-rig
targets resolve through the bead ID's prefix route in routes.jsonl.

Examples:
  bd gate list           # Show all open gates
  bd gate list --all     # Show all gates including closed
  bd gate check          # Evaluate all open gates
  bd gate check --type=bead  # Evaluate only bead gates
  bd gate resolve <id>   # Close a gate manually`,
}

// gateListCmd lists gate issues
var gateListCmd = &cobra.Command{
	Use:   "list [issue-id]",
	Short: "List gate issues",
	Long: `List gate issues.

With no argument, lists all gate issues in the current beads database.
With an [issue-id] argument, lists ONLY the gates that block that issue
(its own dependency gates) — not every gate in the database.

By default, shows only open gates. Use --all to include closed gates.`,
	Args:          cobra.MaximumNArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("gate-list")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if usesProxiedServer() {
			return runGateListProxiedServer(cmd, rootCtx, args)
		}

		allFlag, _ := cmd.Flags().GetBool("all")
		limit, _ := cmd.Flags().GetInt("limit")

		ctx := rootCtx

		// Bead-scoped: list only the gates that block this specific issue
		// (its dependency gates), never the whole database. Without this an
		// issue-id argument was silently ignored and the DB-wide list was
		// returned, which could lead a caller to act on unrelated gates.
		if len(args) == 1 {
			target, err := store.GetIssue(ctx, args[0])
			if err != nil {
				return HandleErrorRespectJSON("issue not found: %s", args[0])
			}
			deps, err := store.GetDependencies(ctx, target.ID)
			if err != nil {
				return HandleErrorRespectJSON("%v", err)
			}
			gates := filterIssueGates(deps, allFlag, limit)
			if jsonOutput {
				return outputJSON(gates)
			}
			displayGates(gates, allFlag)
			return nil
		}

		return runGateList(ctx, allFlag, limit)
	},
}

// runGateList lists the workspace's gates on every route through ONE library
// entry, issueops.ListGates, over the route's Querier (openQuerier picks the
// direct store, the proxied server's unit of work, or the remote http
// backend). Only the presentation stays here.
func runGateList(ctx context.Context, allFlag bool, limit int) error {
	querier, err := openQuerier()
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	gates, err := issueops.ListGates(ctx, querier, issueops.GateListRequest{All: allFlag, Limit: limit})
	if err != nil {
		return HandleErrorRespectJSON("%v", err)
	}
	if jsonOutput {
		return outputJSON(gates)
	}
	displayGates(gates, allFlag)
	return nil
}

// filterIssueGates selects the gate-type issues from an issue's dependency set,
// honoring the same open/closed and limit semantics as the DB-wide list path.
// Pulled out as a pure helper so the bead-scoping logic is unit-testable without
// a live store.
func filterIssueGates(deps []*types.Issue, all bool, limit int) []*types.Issue {
	var gates []*types.Issue
	for _, d := range deps {
		if d == nil || d.IssueType != types.IssueType("gate") {
			continue
		}
		if !all && d.Status == types.StatusClosed {
			continue
		}
		gates = append(gates, d)
		if limit > 0 && len(gates) >= limit {
			break
		}
	}
	return gates
}

// displayGates formats and displays gate issues, separating open and closed gates
func displayGates(gates []*types.Issue, showAll bool) {
	if len(gates) == 0 {
		fmt.Println("No gates found.")
		return
	}

	// Separate open and closed gates
	var openGates, closedGates []*types.Issue
	for _, gate := range gates {
		if gate.Status == types.StatusClosed {
			closedGates = append(closedGates, gate)
		} else {
			openGates = append(openGates, gate)
		}
	}

	// Display open gates
	if len(openGates) > 0 {
		fmt.Printf("\n%s Open Gates (%d):\n\n", ui.RenderAccent("⏳"), len(openGates))
		for _, gate := range openGates {
			displaySingleGate(gate)
		}
	}

	// Display closed gates only if --all was used
	if showAll && len(closedGates) > 0 {
		fmt.Printf("\n%s Closed Gates (%d):\n\n", ui.RenderMuted("●"), len(closedGates))
		for _, gate := range closedGates {
			displaySingleGate(gate)
		}
	}

	if len(openGates) == 0 && (!showAll || len(closedGates) == 0) {
		fmt.Println("No gates found.")
		return
	}

	fmt.Printf("To resolve a gate: bd close <gate-id>\n")
}

// displaySingleGate formats and displays a single gate issue
func displaySingleGate(gate *types.Issue) {
	statusSym := "○"
	if gate.Status == types.StatusClosed {
		statusSym = "●"
	}

	// Format gate info
	gateInfo := gate.AwaitType
	if gate.AwaitID != "" {
		gateInfo = fmt.Sprintf("%s %s", gate.AwaitType, gate.AwaitID)
	}

	// Format timeout if present
	timeoutStr := ""
	if gate.Timeout > 0 {
		timeoutStr = fmt.Sprintf(" (timeout: %s)", gate.Timeout)
	}

	// Find blocked step from ID (gate ID format: parent.gate-stepid)
	blockedStep := ""
	if strings.Contains(gate.ID, ".gate-") {
		parts := strings.Split(gate.ID, ".gate-")
		if len(parts) == 2 {
			blockedStep = fmt.Sprintf("%s.%s", parts[0], parts[1])
		}
	}

	fmt.Printf("%s %s - %s%s\n", statusSym, ui.RenderID(gate.ID), gateInfo, timeoutStr)
	if blockedStep != "" {
		fmt.Printf("  Blocks: %s\n", blockedStep)
	}
	fmt.Println()
}

// gateAddWaiterCmd adds a waiter to a gate
var gateAddWaiterCmd = &cobra.Command{
	Use:   "add-waiter <gate-id> <waiter>",
	Short: "Add a waiter to a gate",
	Long: `Register an agent as a waiter on a gate bead.

When the gate closes, the waiter will receive a wake notification via 'bd gate wake'.
The waiter is typically the worker's address (e.g., "my-project/workers/agent-1").

This is used by 'bd done --phase-complete' to register for gate wake notifications.`,
	Args:          cobra.ExactArgs(2),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("gate add-waiter")

		evt := metrics.NewCommandEvent("gate-add-waiter")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		gateID := args[0]
		waiter := args[1]

		reader, err := openIssueReader()
		if err != nil {
			return HandleError("%v", err)
		}
		lifecycle, err := openIssueLifecycle()
		if err != nil {
			return HandleError("%v", err)
		}
		ctx, err := issueOpsContext(rootCtx)
		if err != nil {
			return HandleError("%v", err)
		}

		added, err := addGateWaiter(ctx, reader, lifecycle, gateID, waiter)
		if err != nil {
			return HandleError("%v", err)
		}
		if !added {
			renderGateWaiterAlready(gateID)
			return nil
		}

		commandDidWrite.Store(true)

		renderGateWaiterAdded(gateID, waiter)
		return nil
	},
}

// addGateWaiter's compare-and-set budget. A version miss means another writer
// changed the gate between this command's read and its write; under a convoy
// (a phase finishing together, every worker registering on the same gate) each
// round of simultaneous writes has exactly one winner, so retrying at once
// would fail all but a handful of adders. Each retry therefore waits a
// jittered, doubling backoff first: gateAddWaiterBackoffBase, doubled per miss
// up to gateAddWaiterBackoffCap, each wait drawn uniformly from [d/2, d) so
// the losers of one round do not collide again in the next.
//
// The loop is bounded by gateAddWaiterMaxAttempts writes, not by wall time:
// one round's latency is the server's (a loaded remote server can take
// seconds per read and write), and a wall-clock budget would fail a convoy on
// a slow server after only two or three rounds. Each round has a winner, so
// the bound carries at least that many simultaneous adders; the jitter
// spreads them further. The backoffs themselves add up to at most about 6s,
// and a gate rewritten without pause still fails after the last attempt.
//
// A write lost to contention the store gave up replaying (see
// gateAddWaiterContended) is not one quick round, so it gets a much tighter
// bound of its own, gateAddWaiterContentionRetries. It reaches this loop only
// after the store or bd serve has spent a whole replay budget on it (up to
// 15s, the server-mode Dolt store's and bd serve's), and the wait after a
// busy answer includes the server's Retry-After (5s once bd serve's budget is
// spent). Two retries, like the HTTP client's for a busy read, hold that tail
// to three such writes (three 15s budgets and two 5s waits: about a minute),
// where twenty would take over six minutes. They count against
// gateAddWaiterMaxAttempts too.
const (
	gateAddWaiterMaxAttempts       = 20
	gateAddWaiterContentionRetries = 2
	gateAddWaiterBackoffBase       = 10 * time.Millisecond
	gateAddWaiterBackoffCap        = 400 * time.Millisecond
)

// gateAddWaiterRetryWait is the wait before retry number retry after the
// failed write err: the jittered backoff, plus the server's Retry-After when
// err is a busy answer that carries one. bd serve sends Retry-After on its
// busy answers: 1s when its admission limiter is saturated, and 5s when its
// transaction retry budget is spent, because a convoy back sooner would hold
// slots while it waited, starving reads. It is honored as sent: the wire
// client already bounds the header (wire.Options.MaxRetryAfter). The jitter on
// top keeps adders that were answered busy together from retrying together.
func gateAddWaiterRetryWait(retry int, err error) time.Duration {
	wait := gateAddWaiterBackoff(retry)
	var problem *wire.ProblemError
	if errors.Is(err, wire.ErrBusy) && errors.As(err, &problem) {
		wait += problem.RetryAfter
	}
	return wait
}

// gateAddWaiterBackoff is the wait before retry number retry (1-based):
// jittered in [d/2, d) around d = base * 2^(retry-1), capped.
func gateAddWaiterBackoff(retry int) time.Duration {
	d := gateAddWaiterBackoffBase
	for i := 1; i < retry && d < gateAddWaiterBackoffCap; i++ {
		d *= 2
	}
	if d > gateAddWaiterBackoffCap {
		d = gateAddWaiterBackoffCap
	}
	half := d / 2
	return half + time.Duration(rand.Int64N(int64(d-half))) // #nosec G404 -- retry jitter, not a secret
}

// gateAddWaiterSleep waits d or until ctx is done. It is a variable so tests
// can record the backoff without sleeping.
var gateAddWaiterSleep = func(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// addGateWaiter registers waiter on the gate gateID through the Reader and
// Lifecycle roles, so every route (local, proxied server, and a connected HTTP
// workspace, which sends getIssue then updateIssue behind issues.update.gates)
// runs the same steps. It reports false, writing nothing, when the waiter is
// already registered.
//
// The write is a whole-value waiter list guarded by the row version the read
// returned, so two concurrent adders cannot lose each other's waiter: the
// second write misses its version, re-reads the list with the first waiter in
// it, and writes again. The deduplication is this command's, not the role's;
// the role stores the list it is given.
func addGateWaiter(ctx context.Context, reader issueops.Reader, lifecycle issueops.Lifecycle, gateID, waiter string) (bool, error) {
	var lastErr error
	attempts, contended := 0, 0
	for attempts < gateAddWaiterMaxAttempts && contended <= gateAddWaiterContentionRetries {
		if attempts > 0 {
			if err := gateAddWaiterSleep(ctx, gateAddWaiterRetryWait(attempts, lastErr)); err != nil {
				return false, fmt.Errorf("updating gate: %w", err)
			}
		}
		attempts++
		details, err := reader.Get(ctx, issueops.GetRequest{ID: gateID})
		if errors.Is(err, storage.ErrNotFound) {
			return false, fmt.Errorf("gate not found: %s", gateID)
		}
		if err != nil {
			return false, fmt.Errorf("reading gate %s: %w", gateID, err)
		}
		gate := &details.Issue
		if gate.IssueType != "gate" {
			return false, fmt.Errorf("%s is not a gate issue (type=%s)", gateID, gate.IssueType)
		}
		if slices.Contains(gate.Waiters, waiter) {
			return false, nil
		}

		waiters := append(slices.Clone(gate.Waiters), waiter)
		version := gate.RowVersion
		_, err = lifecycle.Update(ctx, issueops.UpdateRequest{
			Actor:           currentActor(),
			IssueID:         gate.ID,
			Patch:           issueops.IssuePatch{Waiters: issueops.Field[[]string]{Set: true, Value: waiters}},
			ExpectedVersion: &version,
		})
		if err == nil {
			return true, nil
		}
		if !gateAddWaiterLostRound(err) {
			return false, fmt.Errorf("updating gate: %w", err)
		}
		if gateAddWaiterContended(err) {
			contended++
		}
		lastErr = err
	}
	if errors.Is(lastErr, wire.ErrBusy) && gateAddWaiterLanded(ctx, reader, gateID, waiter) {
		return false, nil
	}
	return false, gateAddWaiterExhausted(gateID, attempts, contended, lastErr)
}

// gateAddWaiterLanded reports whether waiter is on the gate gateID, read once
// after addGateWaiter's last write was answered busy. Such a write may have
// landed (see gateAddWaiterLostRound), and every earlier round's re-read
// would have found it, but the last write has no round left; so it gets this
// one read, with no wait first. A failed read reports false.
func gateAddWaiterLanded(ctx context.Context, reader issueops.Reader, gateID, waiter string) bool {
	details, err := reader.Get(ctx, issueops.GetRequest{ID: gateID})
	return err == nil && slices.Contains(details.Issue.Waiters, waiter)
}

// gateAddWaiterLostRound reports whether a failed guarded write is a lost
// round the loop retries: a version miss, or a write lost to contention the
// store gave up replaying (gateAddWaiterContended).
//
// Retrying is safe because of how a round is built, NOT because every busy
// answer means nothing was written. bd serve's own busy answers are no-write,
// but a gateway in front of the server can answer 503 after the write
// committed, and the client maps that uncoded 503 to wire.ErrBusy too. Each
// round re-reads the gate, deduplicates, and writes the whole list guarded by
// the row version it read, so a write that did land is found on the re-read
// (the waiter is already there: nothing more is written) and a stale list can
// never overwrite a newer one. So a retry never adds a waiter twice and never
// drops another adder's.
//
// Everything else is returned at once, including the classes a retry would
// also survive but that are not contention: a refusal, a database bd serve
// cannot reach (wire.ErrDBUnavailable), and a server fault such as an
// indeterminate commit (wire.ErrServerFault, a 500). Those are for the caller
// to see, not for a convoy loop to hammer.
func gateAddWaiterLostRound(err error) bool {
	return errors.Is(err, issueops.ErrVersionMismatch) || gateAddWaiterContended(err)
}

// gateAddWaiterContended reports whether a failed guarded write was lost to
// contention the store gave up replaying. On the direct route that is a
// serialization failure or deadlock, which guarantees a rollback (Dolt and
// MySQL report 1213/1205, a SERIALIZABLE store such as Postgres SQLSTATE
// 40001/40P01): the store's transaction envelope replays one until its budget
// is spent and then returns it. Over HTTP it is wire.ErrBusy, which the client
// maps from bd serve's busy answer (a refused admission, or a spent retry
// budget) and from a 503 whose code it does not know or that has none, such as
// a gateway's. A 503 coded db_unavailable or events_watch_saturated maps to its
// own sentinel, not to this one.
//
// Dolt's 1105 "@autocommit transaction rolled back", which the direct route's
// store also replays, is deliberately not admitted: Dolt raises it for
// conflicts that a merge inside the transaction left behind, while a write
// lost to a concurrent writer, the convoy's case, comes back as 1213.
func gateAddWaiterContended(err error) bool {
	return uow.IsSerializationError(err) || errors.Is(err, wire.ErrBusy)
}

// gateAddWaiterExhausted is addGateWaiter's error once it stops retrying:
// after attempts writes, contended of them answered busy or lost to
// contention, the last with lastErr. Only when none was contended did the gate
// change under every write, so only then does the error say that; otherwise it
// names the contention, so that the operator of a busy server is not sent
// looking for a concurrent writer. It says no write was confirmed, not that
// none landed, because a busy answer does not prove that nothing was written.
func gateAddWaiterExhausted(gateID string, attempts, contended int, lastErr error) error {
	if contended == 0 {
		return fmt.Errorf("updating gate: %s changed on every one of %d attempts: %w", gateID, attempts, lastErr)
	}
	return fmt.Errorf("updating gate: no write to %s was confirmed in %d attempts (%d answered busy or lost to write contention): %w", gateID, attempts, contended, lastErr)
}

// renderGateWaiterAlready and renderGateWaiterAdded are shared by the direct
// and proxied-server routes so `bd gate add-waiter` prints identically on both.
func renderGateWaiterAlready(gateID string) {
	fmt.Printf("Waiter already registered on gate %s\n", gateID)
}

func renderGateWaiterAdded(gateID, waiter string) {
	fmt.Printf("%s Added waiter to gate %s: %s\n", ui.RenderPass("✓"), gateID, waiter)
}

// gateCreateCmd creates an ad-hoc gate issue that blocks another issue
var gateCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a gate that blocks an issue",
	Long: `Create an ad-hoc gate issue that blocks another issue until resolved.

The blocked issue will not appear in 'bd ready' until the gate is resolved
via 'bd gate resolve'.

Gate types:
  human   - Requires manual 'bd gate resolve' (default)
  timer   - Auto-resolves after --timeout duration
  gh:run  - Waits for GitHub Actions workflow
  gh:pr   - Waits for PR merge
  gl:pipeline - Waits for GitLab pipeline success
  gl:mr   - Waits for GitLab merge request merge

GitHub and GitLab gates are checked in the current Git repository unless
--repo names another, or the blocked issue carries a metadata.repo value.

Examples:
  bd gate create --blocks bd-abc
  bd gate create --type=human --blocks bd-abc --reason="Need design review"
  bd gate create --type=timer --blocks bd-abc --timeout=2h
  bd gate create --type=gh:pr --blocks bd-abc --await-id=42
  bd gate create --type=gh:pr --blocks bd-abc --await-id=42 --repo=owner/other-repo
  bd gate create --blocks bd-abc --title="Gate: awaiting owner sign-off"`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("gate create")

		evt := metrics.NewCommandEvent("gate-create")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		in, err := gatherGateCreateInput(cmd)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

		reader, err := openIssueReader()
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		lifecycle, err := openIssueLifecycle()
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		ctx, err := issueOpsContext(rootCtx)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

		gate, targetIssue, err := createGateThroughRoles(ctx, reader, lifecycle, in)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

		commandDidWrite.Store(true)

		if jsonOutput {
			return outputJSON(gate)
		}

		renderGateCreated(gate, targetIssue, in)
		return nil
	},
}

// createGateThroughRoles creates the gate and its blocking edge as ONE Lifecycle.Create:
// the gate row plus a reverse blocks dependency from the blocked issue to the
// new gate, in one transaction and one version commit on every route. A
// connected HTTP workspace sends it as one createIssue carrying the gate
// members, behind issues.create.gates; a server that does not advertise that
// token is refused before any request is sent, and nothing is written.
//
// It returns the created gate (without its dependency records, which
// `bd gate create` has never printed) and the blocked issue.
func createGateThroughRoles(ctx context.Context, reader issueops.Reader, lifecycle issueops.Lifecycle, in gateCreateInput) (*types.Issue, *types.Issue, error) {
	details, err := reader.Get(ctx, issueops.GetRequest{ID: in.blocksID})
	if err != nil {
		// Every target-lookup failure has always been reported as not-found.
		return nil, nil, fmt.Errorf("issue not found: %s", in.blocksID)
	}
	target := &details.Issue

	gate := buildGateIssue(in, target.ID)
	metadata, err := gateMetadataForCreate(in, target)
	if err != nil {
		return nil, nil, err
	}
	gate.Metadata = metadata

	result, err := lifecycle.Create(ctx, issueops.CreateRequest{
		Actor: currentActor(),
		Issue: gate,
		Dependencies: []issueops.CreateDependency{{
			TargetID: target.ID,
			Type:     types.DepBlocks,
			Reverse:  true,
		}},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("creating gate: %w", err)
	}
	created := result.Issue
	created.Dependencies = nil
	created.Comments = nil
	return created, target, nil
}

// gateCreateInput carries `bd gate create`'s parsed flags. Both routes gather
// it through gatherGateCreateInput so they cannot drift on flag semantics.
type gateCreateInput struct {
	blocksID  string
	gateType  string
	reason    string
	awaitID   string
	repo      string
	titleFlag string
	timeout   time.Duration
}

func gatherGateCreateInput(cmd *cobra.Command) (gateCreateInput, error) {
	in := gateCreateInput{}
	in.blocksID, _ = cmd.Flags().GetString("blocks")
	in.gateType, _ = cmd.Flags().GetString("type")
	in.reason, _ = cmd.Flags().GetString("reason")
	in.awaitID, _ = cmd.Flags().GetString("await-id")
	in.repo, _ = cmd.Flags().GetString("repo")
	in.titleFlag, _ = cmd.Flags().GetString("title")
	timeoutStr, _ := cmd.Flags().GetString("timeout")
	if timeoutStr != "" {
		parsed, err := time.ParseDuration(timeoutStr)
		if err != nil {
			return in, fmt.Errorf("invalid timeout: %v", err)
		}
		in.timeout = parsed
	}
	return in, nil
}

// buildGateIssue constructs the ad-hoc gate issue exactly the way the direct
// route always has; the proxied route reuses it for the same reason the
// renderers are shared.
func buildGateIssue(in gateCreateInput, targetID string) *types.Issue {
	title := fmt.Sprintf("Gate: %s", in.gateType)
	if in.awaitID != "" {
		title = fmt.Sprintf("Gate: %s %s", in.gateType, in.awaitID)
	}
	if in.titleFlag != "" {
		title = in.titleFlag
	}

	// types owns the description format because it also owns the read back
	// out of it (types.GateReason), which is what puts the reason on
	// `bd show`'s "Gated by:" line and in the detail view's gated_by.
	desc := types.GateDescription(targetID, in.reason)

	return &types.Issue{
		Title:       title,
		Description: desc,
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.IssueType("gate"),
		AwaitType:   in.gateType,
		AwaitID:     in.awaitID,
		Timeout:     in.timeout,
		CreatedBy:   getActorWithGit(),
		Owner:       getOwner(),
	}
}

// renderGateCreated is shared by the direct and proxied-server routes; the
// first line's "Created gate <id>" is parsed by downstream scripts, so both
// routes must print it identically.
func renderGateCreated(gate, targetIssue *types.Issue, in gateCreateInput) {
	fmt.Printf("%s Created gate %s (type: %s)\n", ui.RenderPass("✓"), ui.RenderID(gate.ID), in.gateType)
	fmt.Printf("  Blocks: %s (%s)\n", targetIssue.ID, targetIssue.Title)
	if in.reason != "" {
		fmt.Printf("  Reason: %s\n", in.reason)
	}
	if in.timeout > 0 {
		fmt.Printf("  Timeout: %s\n", in.timeout)
	}
	fmt.Printf("\nResolve with: bd gate resolve %s\n", gate.ID)
}

// gateShowCmd shows a gate issue
var gateShowCmd = &cobra.Command{
	Use:   "show <gate-id>",
	Short: "Show a gate issue",
	Long: `Display details of a gate issue including its waiters.

This is similar to 'bd show' but validates that the issue is a gate.`,
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if usesProxiedServer() {
			return runGateShowProxiedServer(cmd, rootCtx, args)
		}
		evt := metrics.NewCommandEvent("gate-show")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		gateID := args[0]
		ctx := rootCtx

		var issue *types.Issue
		var err error

		issue, err = store.GetIssue(ctx, gateID)
		if err != nil {
			return HandleErrorRespectJSON("gate not found: %s", gateID)
		}

		if issue.IssueType != "gate" {
			return HandleErrorRespectJSON("%s is not a gate issue (type=%s)", gateID, issue.IssueType)
		}

		if jsonOutput {
			return outputJSON(issue)
		}

		renderGateShow(issue)
		return nil
	},
}

// renderGateShow is shared by the direct and proxied-server routes; downstream
// scripts grep this plain-text output for markers, so both routes must print
// it identically.
func renderGateShow(issue *types.Issue) {
	statusSym := "○"
	if issue.Status == types.StatusClosed {
		statusSym = "●"
	}

	fmt.Printf("%s %s - %s\n", statusSym, ui.RenderID(issue.ID), issue.Title)
	fmt.Printf("  Status: %s\n", issue.Status)
	fmt.Printf("  Await Type: %s\n", issue.AwaitType)
	if issue.AwaitID != "" {
		fmt.Printf("  Await ID: %s\n", issue.AwaitID)
	}
	if issue.Timeout > 0 {
		fmt.Printf("  Timeout: %s\n", issue.Timeout)
	}
	if len(issue.Waiters) > 0 {
		fmt.Printf("  Waiters:\n")
		for _, w := range issue.Waiters {
			fmt.Printf("    - %s\n", w)
		}
	}
	if issue.Description != "" {
		fmt.Printf("  Description: %s\n", issue.Description)
	}
}

// gateResolveCmd manually closes a gate
var gateResolveCmd = &cobra.Command{
	Use:   "resolve <gate-id>",
	Short: "Manually resolve (close) a gate",
	Long: `Close a gate issue to unblock the step waiting on it.

This is equivalent to 'bd close <gate-id>' but with a more explicit name.
Use --reason to provide context for why the gate was resolved.`,
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if usesProxiedServer() {
			return runGateResolveProxiedServer(cmd, rootCtx, args)
		}
		CheckReadonly("gate resolve")

		evt := metrics.NewCommandEvent("gate-resolve")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		gateID := args[0]
		reason, _ := cmd.Flags().GetString("reason")

		ctx := rootCtx
		var issue *types.Issue
		var err error

		issue, err = store.GetIssue(ctx, gateID)
		if err != nil {
			return HandleError("gate not found: %s", gateID)
		}

		if issue.IssueType != "gate" {
			return HandleError("%s is not a gate issue (type=%s)", gateID, issue.IssueType)
		}

		if err := store.CloseIssue(ctx, gateID, reason, currentActor(), ""); err != nil {
			return HandleError("closing gate: %v", err)
		}

		commandDidWrite.Store(true)

		renderGateResolved(gateID, reason)
		return nil
	},
}

// renderGateResolved is shared by the direct and proxied-server routes so
// `bd gate resolve` prints identically on both.
func renderGateResolved(gateID, reason string) {
	fmt.Printf("%s Gate resolved: %s\n", ui.RenderPass("✓"), gateID)
	if reason != "" {
		fmt.Printf("  Reason: %s\n", reason)
	}
}

// gateCheckCmd evaluates gates and closes those that are resolved
var gateCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Evaluate gates and close resolved ones",
	Long: `Evaluate gate conditions and automatically close resolved gates.

By default, checks all open gates. Use --type to filter by gate type.

Gate types:
  gh       - Check all GitHub gates (gh:run and gh:pr)
  gh:run   - Check GitHub Actions workflow runs
  gh:pr    - Check pull request merge status
  gl       - Check all GitLab gates
  gl:pipeline - Check GitLab pipeline status
  gl:mr    - Check GitLab merge request state
  timer    - Check timer gates (auto-expire based on timeout)
  bead     - Check bead gates
  all      - Check all gate types

Forge gates use the gh and glab CLIs to query status:
  - gh:run checks 'gh run view <id> --json status,conclusion'
  - gh:pr checks 'gh pr view <id> --json state,title'
  - gl:pipeline checks the GitLab pipeline API by pipeline ID via glab
  - gl:mr checks the GitLab merge request API by project-local IID via glab

A gate is resolved when:
  - gh:run: status=completed AND conclusion=success
  - gh:pr: state=MERGED
  - gl:pipeline: status=success
  - gl:mr: state=merged
  - timer: current time > created_at + timeout
  - bead: target bead status=closed, or a bead an earlier check saw in
    this rig no longer exists

A gate is escalated when:
  - gh:run: status=completed AND conclusion in (failure, canceled)
  - gh:pr: state=CLOSED
  - gl:pipeline: status in (failed, canceled)
  - gl:mr: state=closed (unmerged)

Examples:
  bd gate check              # Check all gates
  bd gate check --type=gh    # Check only GitHub gates
  bd gate check --type=gh:run # Check only workflow run gates
  bd gate check --type=timer # Check only timer gates
  bd gate check --type=bead  # Check only bead gates
  bd gate check --dry-run    # Show what would happen without changes
  bd gate check --escalate   # Escalate expired/failed gates`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if usesProxiedServer() {
			return runGateCheckProxiedServer(cmd, rootCtx)
		}
		CheckReadonly("gate check")

		evt := metrics.NewCommandEvent("gate-check")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		gateTypeFilter, _ := cmd.Flags().GetString("type")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		escalateFlag, _ := cmd.Flags().GetBool("escalate")
		limit, _ := cmd.Flags().GetInt("limit")

		ctx := rootCtx

		gates, err := listOpenGatesForCheck(ctx, limit)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

		filteredGates := filterCheckableGates(gates, gateTypeFilter)
		if len(filteredGates) == 0 {
			printNoOpenGates(gateTypeFilter)
			return nil
		}

		var persistAwaitID func(gateID, runID string) error
		var recordSeen func(gate *types.Issue) error
		if !dryRun {
			persistAwaitID = func(gateID, runID string) error {
				return gateCheckPersistAwaitIDFunc(ctx, gateID, runID)
			}
			recordSeen = func(gate *types.Issue) error {
				return updateGateThroughLifecycle(ctx, gate.ID, beadGateSeenPatch(gate.AwaitID))
			}
		}

		results := evaluateGates(ctx, filteredGates, time.Now(), routedBeadGateGetter{localStore: store}, persistAwaitID, recordSeen)

		resolvedCount, escalatedCount, errorCount := applyGateCheckResults(
			results, dryRun, escalateFlag,
			func(gate *types.Issue, reason string) error {
				return closeGate(ctx, gate.ID, reason)
			},
		)

		return printGateCheckSummary(len(results), resolvedCount, escalatedCount, errorCount, dryRun)
	},
}

// listOpenGatesForCheck lists the open gates `bd gate check` evaluates through
// the same library entry `bd gate list` uses, issueops.ListGates over the
// route's Querier (openQuerier: the direct store, the proxied server's unit
// of work, or the remote http backend's queryIssues). It is the question the
// raw SearchIssues filter used to ask — the gate type, both planes, closed
// rows excluded, cut at limit — but a remote backend serves it, where the raw
// filter was refused before any gate was read. Templates are left out of the
// query (see listOpenGatesWith); the per-type selection stays with
// filterCheckableGates.
func listOpenGatesForCheck(ctx context.Context, limit int) ([]*types.Issue, error) {
	querier, err := openQuerier()
	if err != nil {
		return nil, err
	}
	return listOpenGatesWith(ctx, querier, limit)
}

// listOpenGatesWith is listOpenGatesForCheck over a given Querier: the open
// gates only (gate check never evaluates a closed one), minus templates, cut at
// limit. The templates go in the query, not after it: a page of a persisted
// proto's gates, newer than the live ones, would otherwise fill --limit and
// leave the sweep nothing to check.
func listOpenGatesWith(ctx context.Context, querier issueops.Querier, limit int) ([]*types.Issue, error) {
	return issueops.ListGates(ctx, querier, issueops.GateListRequest{ExcludeTemplates: true, Limit: limit})
}

type gateCheckResult struct {
	gate      *types.Issue
	resolved  bool
	escalated bool
	reason    string
	err       error
}

// filterCheckableGates selects the listed gates a sweep evaluates: those that
// match typeFilter, minus templates. A template's gate is a blueprint that every
// pour copies, await_id included. Checking one fails every sweep where its
// write is refused (the Lifecycle role's template guard, a served close) and
// rewrites the proto where it is not (a local close, the proxied-server route's
// writes): a closed timer gate, a recorded sighting, or a run ID every later
// pour would await. listOpenGatesWith already leaves templates out of the
// query; the skip here is the backstop for a listing that returns one anyway.
// The direct and the proxied-server routes both select here.
func filterCheckableGates(gates []*types.Issue, typeFilter string) []*types.Issue {
	var out []*types.Issue
	for _, gate := range gates {
		if gate.IsTemplate {
			continue
		}
		if shouldCheckGate(gate, typeFilter) {
			out = append(out, gate)
		}
	}
	return out
}

func printNoOpenGates(typeFilter string) {
	if typeFilter != "" {
		fmt.Printf("No open gates of type '%s' found.\n", typeFilter)
	} else {
		fmt.Println("No open gates found.")
	}
}

// updateGateThroughLifecycle is gate check's one write besides the close: a
// patch to a gate through the Lifecycle role, so every route the direct path
// serves (Dolt, embedded, Postgres, and a connected HTTP workspace, whose role
// sends issues.update) applies it the same way. The legacy map-based
// UpdateIssue is not on the HTTP store at all, so the sighting written through
// it was refused before any request was sent.
func updateGateThroughLifecycle(ctx context.Context, gateID string, patch issueops.IssuePatch) error {
	lifecycle, err := openIssueLifecycle()
	if err != nil {
		return err
	}
	ctx, err = issueOpsContext(ctx)
	if err != nil {
		return err
	}
	if _, err := lifecycle.Update(ctx, issueops.UpdateRequest{Actor: currentActor(), IssueID: gateID, Patch: patch}); err != nil {
		return err
	}
	commandDidWrite.Store(true)
	return nil
}

// awaitIDPatchRefusal is the divergence-ledger row a connected HTTP
// workspace's Lifecycle role refused an await_id edit by, before any request,
// while the wire's issue patch had no await_id member. The row is retired: the
// patch carries await_id behind issues.update.gates, and a server that does
// not advertise that token earns the capability error, which
// persistGateCheckAwaitID returns as the role raised it.
const awaitIDPatchRefusal = "W-IssuePatch.AwaitID"

// persistGateCheckAwaitID records a gh:run gate's discovered run ID through
// the Lifecycle role. The one refusal it rewords is the HTTP wire's
// (awaitIDPatchRefusal), whose own text names the role method and the wire
// operation rather than what the user can do about it. That refusal stays
// this gate's error, so the rest of the sweep runs. Every other error,
// another unsupported capability included, is returned as the role raised it.
func persistGateCheckAwaitID(ctx context.Context, gateID, runID string) error {
	err := updateGateThroughLifecycle(ctx, gateID, issueops.IssuePatch{AwaitID: issueops.Field[string]{Set: true, Value: runID}})
	var refused *httpclient.InexpressibleError
	if errors.As(err, &refused) && refused.Refused != nil && refused.Refused.Row.ID == awaitIDPatchRefusal {
		serverURL := ""
		if refused.Unsup != nil {
			serverURL = refused.Unsup.ServerURL
		}
		return &awaitIDRefusedError{runID: runID, serverURL: serverURL, err: err}
	}
	return err
}

// awaitIDRefusedError is the HTTP wire's await_id refusal, reworded for the
// gate check row that prints it: it names the run, await_id and the server.
// Unwrap keeps the refusal, so errors.As still reaches *storage.ErrUnsupported
// and the ledger row.
type awaitIDRefusedError struct {
	runID     string
	serverURL string
	err       error
}

func (e *awaitIDRefusedError) Error() string {
	server := "this backend"
	if e.serverURL != "" {
		server = "bd serve at " + e.serverURL
	}
	return fmt.Sprintf("run %s cannot be recorded as await_id: %s does not accept an await_id update; check the gate in a local workspace", e.runID, server)
}

func (e *awaitIDRefusedError) Unwrap() error { return e.err }

func evaluateGates(ctx context.Context, gates []*types.Issue, now time.Time, getter issueGetter, persistAwaitID func(gateID, runID string) error, recordSeen func(gate *types.Issue) error) []gateCheckResult {
	results := make([]gateCheckResult, 0, len(gates))
	for _, gate := range gates {
		r := gateCheckResult{gate: gate}
		switch {
		case gate.AwaitType == "gl:pipeline":
			r.resolved, r.escalated, r.reason, r.err = checkGLPipeline(gate)
		case gate.AwaitType == "gl:mr":
			r.resolved, r.escalated, r.reason, r.err = checkGLMR(gate)
		case strings.HasPrefix(gate.AwaitType, "gh:run"):
			r.resolved, r.escalated, r.reason, r.err = checkGHRun(gate, persistAwaitID)
		case strings.HasPrefix(gate.AwaitType, "gh:pr"):
			r.resolved, r.escalated, r.reason, r.err = checkGHPR(gate)
		case gate.AwaitType == "timer":
			r.resolved, r.escalated, r.reason, r.err = checkTimer(gate, now)
		case gate.AwaitType == "bead":
			r.resolved, r.reason, r.err = evaluateBeadGate(ctx, gate, getter, recordSeen)
		default:
			continue
		}
		results = append(results, r)
	}
	return results
}

func applyGateCheckResults(results []gateCheckResult, dryRun, escalate bool, closeResolved func(gate *types.Issue, reason string) error) (resolvedCount, escalatedCount, errorCount int) {
	for _, r := range results {
		if r.err != nil {
			errorCount++
			fmt.Fprintf(os.Stderr, "%s %s: error checking - %v\n",
				ui.RenderFail("✗"), r.gate.ID, r.err)
			continue
		}

		switch {
		case r.resolved:
			resolvedCount++
			if dryRun {
				fmt.Printf("%s %s: would resolve - %s\n",
					ui.RenderPass("✓"), r.gate.ID, r.reason)
				continue
			}
			if closeErr := closeResolved(r.gate, r.reason); closeErr != nil {
				fmt.Fprintf(os.Stderr, "%s %s: error closing - %v\n",
					ui.RenderFail("✗"), r.gate.ID, closeErr)
				errorCount++
			} else {
				fmt.Printf("%s %s: resolved - %s\n",
					ui.RenderPass("✓"), r.gate.ID, r.reason)
			}
		case r.escalated:
			escalatedCount++
			if dryRun {
				fmt.Printf("%s %s: would escalate - %s\n",
					ui.RenderWarn("⚠"), r.gate.ID, r.reason)
				continue
			}
			fmt.Printf("%s %s: ESCALATE - %s\n",
				ui.RenderWarn("⚠"), r.gate.ID, r.reason)
			if escalate {
				escalateGate(r.gate, r.reason)
			}
		default:
			fmt.Printf("%s %s: pending - %s\n",
				ui.RenderAccent("○"), r.gate.ID, r.reason)
		}
	}
	return resolvedCount, escalatedCount, errorCount
}

func printGateCheckSummary(checked, resolvedCount, escalatedCount, errorCount int, dryRun bool) error {
	fmt.Println()
	fmt.Printf("Checked %d gates: %d resolved, %d escalated, %d errors\n",
		checked, resolvedCount, escalatedCount, errorCount)

	if jsonOutput {
		if err := outputJSON(map[string]interface{}{
			"checked":   checked,
			"resolved":  resolvedCount,
			"escalated": escalatedCount,
			"errors":    errorCount,
			"dry_run":   dryRun,
		}); err != nil {
			return err
		}
	}
	if errorCount > 0 {
		// errorCount holds both row kinds applyGateCheckResults prints: a gate
		// whose check failed ("error checking") and a resolved gate whose close
		// failed ("error closing"). Neither is resolved or pending, so the
		// caller must not mistake this run for a clean sweep.
		return fmt.Errorf("%d gate(s) could not be checked or closed", errorCount)
	}
	return nil
}

// shouldCheckGate returns true if the gate matches the type filter
func shouldCheckGate(gate *types.Issue, typeFilter string) bool {
	if typeFilter == "" || typeFilter == "all" {
		return true
	}
	if typeFilter == "gl" {
		return isGitLabGateType(gate.AwaitType)
	}
	if typeFilter == "gh" {
		return strings.HasPrefix(gate.AwaitType, "gh:")
	}
	return gate.AwaitType == typeFilter
}

// ghRunStatus holds the JSON response from 'gh run view'
type ghRunStatus struct {
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Name       string `json:"name"`
}

// ghPRStatus holds the JSON response from 'gh pr view'
type ghPRStatus struct {
	State string `json:"state"`
	Title string `json:"title"`
}

type ghCommandRunner func(args ...string) (stdout, stderr []byte, err error)

func runGHCommand(args ...string) (stdout, stderr []byte, err error) {
	cmd := exec.Command("gh", args...) // #nosec G204 -- callers pass validated values as an argument vector, without a shell
	var stdoutBuffer, stderrBuffer bytes.Buffer
	cmd.Stdout = &stdoutBuffer
	cmd.Stderr = &stderrBuffer
	err = cmd.Run()
	return stdoutBuffer.Bytes(), stderrBuffer.Bytes(), err
}

var (
	discoverRunIDByWorkflowNameFunc = discoverRunIDByWorkflowName
	updateGateAwaitIDFunc           = updateGateAwaitID
	gateCheckPersistAwaitIDFunc     = persistGateCheckAwaitID
	checkGHRunStatusFunc            = checkGHRunStatus
)

// isNumericID returns true if the string contains only digits (a GitHub run ID)
func isNumericID(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// githubRepoFromIssue returns a validated [HOST/]OWNER/REPO value from metadata.repo.
// A missing repo key, or metadata without a repo key at all, means the current
// Git repository should be used. An explicit `"repo":null` or a non-string
// repo value is rejected as malformed rather than silently falling back to
// the current repository - the docs promise malformed values are rejected,
// and a silent fallback here is the dangerous direction (it can point a
// cross-repo check at the wrong repository instead of failing loudly).
func githubRepoFromIssue(issue *types.Issue) (string, error) {
	if issue == nil || len(issue.Metadata) == 0 || string(issue.Metadata) == "null" {
		return "", nil
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(issue.Metadata, &raw); err != nil {
		return "", fmt.Errorf("metadata must be a JSON object: %w", err)
	}
	repoRaw, hasRepo := raw["repo"]
	if !hasRepo {
		return "", nil
	}

	var repoValue interface{}
	if err := json.Unmarshal(repoRaw, &repoValue); err != nil {
		return "", fmt.Errorf("metadata.repo: %w", err)
	}
	if repoValue == nil {
		return "", fmt.Errorf("metadata.repo must not be null")
	}
	repo, ok := repoValue.(string)
	if !ok {
		return "", fmt.Errorf("metadata.repo must be a string, got %T", repoValue)
	}
	if repo == "" {
		return "", nil
	}

	return validateGitHubRepo(repo)
}

// validateGitHubRepo accepts an OWNER/REPO or HOST/OWNER/REPO selector made of
// the characters GitHub allows in those path components, and nothing else:
// the value is passed to `gh --repo`, so a stray shell or URL character is
// rejected here rather than reaching a subprocess argument.
func validateGitHubRepo(repo string) (string, error) {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 && len(parts) != 3 {
		return "", fmt.Errorf("repo %q must use OWNER/REPO or HOST/OWNER/REPO", repo)
	}
	for _, part := range parts {
		if part == "" {
			return "", fmt.Errorf("repo %q contains an empty path component", repo)
		}
		for _, char := range part {
			if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
				(char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
				continue
			}
			return "", fmt.Errorf("repo %q contains invalid character %q", repo, char)
		}
	}

	return repo, nil
}

// isGitHubGateType returns true for gate types whose condition is checked
// against a GitHub repository (gh:run, gh:pr, and any future gh:* type).
func isGitHubGateType(gateType string) bool {
	return strings.HasPrefix(gateType, "gh:")
}

// repoMetadataForGate computes the metadata to store on a new ad-hoc gate,
// inheriting a validated forge-specific repo selector from the blocked issue.
//
// Only forge gates inherit repo; other gate types treat it as unrelated metadata.
func repoMetadataForGate(gateType string, targetIssue *types.Issue) (json.RawMessage, error) {
	if !isGitHubGateType(gateType) && !isGitLabGateType(gateType) {
		return nil, nil
	}
	var repo string
	var err error
	if isGitLabGateType(gateType) {
		repo, err = gitlabRepoFromIssue(targetIssue)
	} else {
		repo, err = githubRepoFromIssue(targetIssue)
	}
	if err != nil {
		return nil, err
	}
	if repo == "" {
		return nil, nil
	}
	metadata, err := json.Marshal(map[string]string{"repo": repo})
	if err != nil {
		return nil, err
	}
	return metadata, nil
}

// gateMetadataForCreate computes the metadata for a new ad-hoc gate from the
// parsed flags: an explicit --repo wins, otherwise the gate inherits the
// blocked issue's validated metadata.repo (repoMetadataForGate). Both create
// routes call this so the flag cannot drift between them.
//
// --repo is only meaningful on GitHub and GitLab gates. Other types refuse
// it because a repo selector nothing reads would look like a working gate.
// Errors are fully worded here (they name the flag or the blocked issue) so
// the callers print them as-is.
func gateMetadataForCreate(in gateCreateInput, targetIssue *types.Issue) (json.RawMessage, error) {
	if in.repo == "" {
		metadata, err := repoMetadataForGate(in.gateType, targetIssue)
		if err != nil {
			forge := "GitHub"
			if isGitLabGateType(in.gateType) {
				forge = "GitLab"
			}
			return nil, fmt.Errorf("invalid %s repository metadata on %s: %w", forge, targetIssue.ID, err)
		}
		return metadata, nil
	}
	if !isGitHubGateType(in.gateType) && !isGitLabGateType(in.gateType) {
		return nil, fmt.Errorf("--repo applies only to GitHub or GitLab gates, not %q", in.gateType)
	}
	repo := in.repo
	var err error
	if isGitLabGateType(in.gateType) {
		_, _, err = gitlabProjectSelector(repo)
	} else {
		repo, err = validateGitHubRepo(repo)
	}
	if err != nil {
		return nil, fmt.Errorf("--repo: %w", err)
	}
	metadata, err := json.Marshal(map[string]string{"repo": repo})
	if err != nil {
		return nil, err
	}
	return metadata, nil
}

// queryGitHubRunsForWorkflow queries recent runs for a specific workflow using gh CLI.
// Returns runs sorted newest-first (GitHub API default).
func queryGitHubRunsForWorkflow(workflow string, limit int) ([]GHWorkflowRun, error) {
	return queryGitHubRunsForWorkflowInRepo(workflow, limit, "")
}

func queryGitHubRunsForWorkflowInRepo(workflow string, limit int, repo string) ([]GHWorkflowRun, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, fmt.Errorf("gh CLI not found: install from https://cli.github.com")
	}
	return queryGitHubRunsForWorkflowInRepoWithRunner(workflow, limit, repo, runGHCommand)
}

func queryGitHubRunsForWorkflowInRepoWithRunner(workflow string, limit int, repo string, runGH ghCommandRunner) ([]GHWorkflowRun, error) {
	args := []string{
		"run", "list",
		"--workflow", workflow,
		"--json", "databaseId,name,status,conclusion,createdAt,workflowName",
		"--limit", fmt.Sprintf("%d", limit),
	}
	if repo != "" {
		args = append(args, "--repo", repo)
	}

	output, stderr, err := runGH(args...)
	if err != nil {
		if len(stderr) > 0 {
			return nil, fmt.Errorf("gh run list --workflow=%s failed: %s", workflow, string(stderr))
		}
		return nil, fmt.Errorf("gh run list: %w", err)
	}

	var runs []GHWorkflowRun
	if err := json.Unmarshal(output, &runs); err != nil {
		return nil, fmt.Errorf("parse gh output: %w", err)
	}

	return runs, nil
}

// discoverRunIDByWorkflowName queries GitHub for the most recent run of a workflow.
// Returns (runID, error). This is ZFC-compliant: "most recent run" is deterministic.
func discoverRunIDByWorkflowName(workflowHint string) (string, error) {
	return discoverRunIDByWorkflowNameInRepo(workflowHint, "")
}

func discoverRunIDByWorkflowNameInRepo(workflowHint, repo string) (string, error) {
	return discoverRunIDByWorkflowNameInRepoWithRunner(workflowHint, repo, runGHCommand)
}

// discoverRunIDByWorkflowNameInRepoWithRunner is the runner-injectable form of
// discoverRunIDByWorkflowNameInRepo. checkGHRunWithRunner's cross-repo branch
// must call this (not discoverRunIDByWorkflowNameInRepo directly) so the same
// injected ghCommandRunner seam used everywhere else in the gh:run/gh:pr
// checks also covers cross-repo discovery, keeping that path unit-testable
// without a live `gh` CLI (standards note on the SF1 review).
func discoverRunIDByWorkflowNameInRepoWithRunner(workflowHint, repo string, runGH ghCommandRunner) (string, error) {
	// Query GitHub directly for this workflow (efficient, avoids limit issues)
	runs, err := queryGitHubRunsForWorkflowInRepoWithRunner(workflowHint, 5, repo, runGH)
	if err != nil {
		return "", fmt.Errorf("failed to query workflow runs: %w", err)
	}

	if len(runs) == 0 {
		return "", fmt.Errorf("no runs found for workflow '%s'", workflowHint)
	}

	// Take the most recent run (gh returns newest-first)
	// This is deterministic: "most recent" is a total ordering by creation time
	return fmt.Sprintf("%d", runs[0].DatabaseID), nil
}

// checkGHRun checks a GitHub Actions workflow run gate.
// When persistAwaitID is nil, workflow-name discovery stays in-memory only.
func checkGHRun(gate *types.Issue, persistAwaitID func(gateID, runID string) error) (resolved, escalated bool, reason string, err error) {
	return checkGHRunWithRunner(gate, persistAwaitID, runGHCommand)
}

func checkGHRunWithRunner(gate *types.Issue, persistAwaitID func(gateID, runID string) error, runGH ghCommandRunner) (resolved, escalated bool, reason string, err error) {
	if gate.AwaitID == "" {
		return false, false, "no run ID specified - set await_id or use workflow name hint", nil
	}

	runID := gate.AwaitID
	repo, repoErr := githubRepoFromIssue(gate)
	if repoErr != nil {
		return false, false, "", repoErr
	}

	// If await_id is a workflow name hint (non-numeric), auto-discover the run ID
	if !isNumericID(gate.AwaitID) {
		var discoveredID string
		var discoverErr error
		if repo == "" {
			discoveredID, discoverErr = discoverRunIDByWorkflowNameFunc(gate.AwaitID)
		} else {
			discoveredID, discoverErr = discoverRunIDByWorkflowNameInRepoWithRunner(gate.AwaitID, repo, runGH)
		}
		if discoverErr != nil {
			return false, false, fmt.Sprintf("workflow hint '%s': %v", gate.AwaitID, discoverErr), nil
		}

		if persistAwaitID != nil {
			// Non-dry-run flows persist the numeric run ID for future checks.
			if updateErr := persistAwaitID(gate.ID, discoveredID); updateErr != nil {
				return false, false, "", fmt.Errorf("failed to update gate with discovered run ID: %w", updateErr)
			}
		}

		runID = discoveredID
	}

	if repo == "" {
		return checkGHRunStatusFunc(runID)
	}
	return checkGHRunStatusInRepoWithRunner(runID, repo, runGH)
}

func checkGHRunStatus(runID string) (resolved, escalated bool, reason string, err error) {
	return checkGHRunStatusInRepo(runID, "")
}

func checkGHRunStatusInRepo(runID, repo string) (resolved, escalated bool, reason string, err error) {
	return checkGHRunStatusInRepoWithRunner(runID, repo, runGHCommand)
}

func checkGHRunStatusInRepoWithRunner(runID, repo string, runGH ghCommandRunner) (resolved, escalated bool, reason string, err error) {
	// Run: gh run view <id> --json status,conclusion,name
	args := []string{"run", "view", runID, "--json", "status,conclusion,name"}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	stdout, stderr, runErr := runGH(args...)
	if runErr != nil {
		// Check if gh CLI is not found
		if strings.Contains(string(stderr), "command not found") ||
			strings.Contains(runErr.Error(), "executable file not found") {
			return false, false, "", fmt.Errorf("gh CLI not installed")
		}
		// Check if run not found
		if strings.Contains(string(stderr), "not found") {
			// Name the repository, as checkGHPRWithRunner does. Real gh
			// reports a missing run as "HTTP 404: Not Found (<api url>)",
			// which this case-sensitive match skips: that returns the error
			// below, whose URL names the repository. Keep the match narrow; a
			// token without access to the repository gets the same 404.
			where := "the current repository"
			if repo != "" {
				where = repo
			}
			return false, true, fmt.Sprintf("workflow run not found: %s in %s", runID, where), nil
		}
		return false, false, "", fmt.Errorf("gh run view failed: %s", string(stderr))
	}

	var status ghRunStatus
	if parseErr := json.Unmarshal(stdout, &status); parseErr != nil {
		return false, false, "", fmt.Errorf("failed to parse gh output: %w", parseErr)
	}

	// Evaluate status
	switch status.Status {
	case "completed":
		switch status.Conclusion {
		case "success":
			return true, false, fmt.Sprintf("workflow '%s' succeeded", status.Name), nil
		case "failure":
			return false, true, fmt.Sprintf("workflow '%s' failed", status.Name), nil
		case "cancelled", "canceled":
			return false, true, fmt.Sprintf("workflow '%s' was canceled", status.Name), nil
		case "skipped":
			return true, false, fmt.Sprintf("workflow '%s' was skipped", status.Name), nil
		default:
			return false, true, fmt.Sprintf("workflow '%s' concluded with %s", status.Name, status.Conclusion), nil
		}
	case "in_progress", "queued", "pending", "waiting":
		return false, false, fmt.Sprintf("workflow '%s' is %s", status.Name, status.Status), nil
	default:
		return false, false, fmt.Sprintf("workflow '%s' status: %s", status.Name, status.Status), nil
	}
}

// checkGHPR checks a GitHub pull request gate
func checkGHPR(gate *types.Issue) (resolved, escalated bool, reason string, err error) {
	return checkGHPRWithRunner(gate, runGHCommand)
}

func checkGHPRWithRunner(gate *types.Issue, runGH ghCommandRunner) (resolved, escalated bool, reason string, err error) {
	if gate.AwaitID == "" {
		return false, false, "no PR number specified", nil
	}

	repo, repoErr := githubRepoFromIssue(gate)
	if repoErr != nil {
		return false, false, "", repoErr
	}

	// Run: gh pr view <id> --json state,title [--repo <repo>]
	args := []string{"pr", "view", gate.AwaitID, "--json", "state,title"}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	stdout, stderr, runErr := runGH(args...)
	if runErr != nil {
		// Check if gh CLI is not found
		if strings.Contains(string(stderr), "command not found") ||
			strings.Contains(runErr.Error(), "executable file not found") {
			return false, false, "", fmt.Errorf("gh CLI not installed")
		}
		// Check if PR not found
		if strings.Contains(string(stderr), "not found") || strings.Contains(string(stderr), "Could not resolve") {
			// Name the repository the number was resolved against: a gate
			// armed for another repository without metadata.repo escalates
			// here on every check, and the bare text never said why.
			where := "the current repository"
			if repo != "" {
				where = repo
			}
			return false, true, fmt.Sprintf("pull request not found: #%s in %s", gate.AwaitID, where), nil
		}
		return false, false, "", fmt.Errorf("gh pr view failed: %s", string(stderr))
	}

	var status ghPRStatus
	if parseErr := json.Unmarshal(stdout, &status); parseErr != nil {
		return false, false, "", fmt.Errorf("failed to parse gh output: %w", parseErr)
	}

	// Evaluate status
	switch status.State {
	case "MERGED":
		return true, false, fmt.Sprintf("PR '%s' was merged", status.Title), nil
	case "CLOSED":
		return false, true, fmt.Sprintf("PR '%s' was closed without merging", status.Title), nil
	case "OPEN":
		return false, false, fmt.Sprintf("PR '%s' is still open", status.Title), nil
	default:
		return false, false, fmt.Sprintf("PR '%s' state: %s", status.Title, status.State), nil
	}
}

// checkTimer checks a timer gate for expiration
// Note: timers resolve but never escalate (escalated is always false by design)
func checkTimer(gate *types.Issue, now time.Time) (resolved, escalated bool, reason string, err error) { //nolint:unparam // escalated intentionally always false
	if gate.Timeout == 0 {
		return false, false, "timer gate without timeout configured", fmt.Errorf("no timeout set")
	}

	expiresAt := gate.CreatedAt.Add(gate.Timeout)
	if now.After(expiresAt) {
		expired := now.Sub(expiresAt).Round(time.Second)
		return true, false, fmt.Sprintf("timer expired %s ago", expired), nil
	}

	remaining := expiresAt.Sub(now).Round(time.Second)
	return false, false, fmt.Sprintf("expires in %s", remaining), nil
}

// issueGetter is the one storage method inspectBeadGate needs, split out so
// tests can fake the lookup without standing up a Dolt store.
type issueGetter interface {
	GetIssue(ctx context.Context, id string) (*types.Issue, error)
}

// beadGateTargetGetter is implemented by getters that also report where the
// awaited bead was looked up. local is true when the answer came from this
// rig's own store; a bead read through a prefix or contributor route is not
// local. Only a local sighting is recorded on the gate (see
// evaluateBeadGate), so a misdirected route can never let a later local miss
// resolve the gate. A getter without this method is treated as local.
type beadGateTargetGetter interface {
	getBeadGateTarget(ctx context.Context, id string) (issue *types.Issue, local bool, err error)
}

// errBeadGateTargetUnconfirmed marks a miss that happened somewhere other
// than this rig's own store: a route matched but its store could not be read,
// or the routed store did not return the bead. Neither proves the bead is
// gone, so the gate stays pending instead of resolving.
var errBeadGateTargetUnconfirmed = errors.New("cannot confirm the awaited bead is gone")

// beadGateNotFound reports whether a lookup failed because the bead does not
// exist, as opposed to the read itself failing.
func beadGateNotFound(err error) bool {
	return gateProxiedNotFound(err) || isNotFoundErr(err)
}

// routedBeadGateGetter gives direct-mode gate checks the same local -> prefix
// route -> contributor fallback used by other read commands. Routed stores are
// opened read-only and closed before the result is returned. A route that
// fails or misses is reported as errBeadGateTargetUnconfirmed instead of as
// the local not-found.
type routedBeadGateGetter struct {
	localStore storage.DoltStorage
}

func (g routedBeadGateGetter) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	issue, _, err := g.getBeadGateTarget(ctx, id)
	return issue, err
}

func (g routedBeadGateGetter) getBeadGateTarget(ctx context.Context, id string) (*types.Issue, bool, error) {
	if g.localStore == nil {
		return nil, false, fmt.Errorf("no local store available")
	}
	issue, err := g.localStore.GetIssue(ctx, id)
	if err == nil && issue != nil {
		return issue, true, nil
	}
	if err != nil && !beadGateNotFound(err) {
		return nil, false, err
	}

	routed, routeErr := prefixRoutedBeadGateTarget(ctx, id)
	if routed == nil {
		var autoErr error
		routed, autoErr = autoRoutedBeadGateTarget(ctx, g.localStore, id)
		if routeErr == nil {
			routeErr = autoErr
		}
	}
	if routed != nil {
		return routed, false, nil
	}
	if routeErr != nil {
		return nil, false, routeErr
	}
	return issue, true, err
}

// prefixRoutedBeadGateTarget looks id up through routes.jsonl. It returns
// (nil, nil) when no route sends id to another rig, so this rig's own answer
// stands, and errBeadGateTargetUnconfirmed when routes.jsonl cannot be read or
// a matched route's rig fails or does not return the bead.
func prefixRoutedBeadGateTarget(ctx context.Context, id string) (*types.Issue, error) {
	beadsDir := resolveCommandBeadsDir(dbPath)
	if beadsDir == "" {
		return nil, nil
	}
	routes, err := loadPrefixRoutes(beadsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: reading routes.jsonl: %w", errBeadGateTargetUnconfirmed, err)
	}
	route := matchPrefixRoute(routes, id)
	if route == nil || route.Path == "." {
		return nil, nil
	}

	result, err := resolveViaPrefixRouting(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("%w: bead %s routes to %s: %w", errBeadGateTargetUnconfirmed, id, route.Path, err)
	}
	defer result.Close()
	if result.Issue == nil {
		return nil, fmt.Errorf("%w: bead %s routes to %s, which did not return it", errBeadGateTargetUnconfirmed, id, route.Path)
	}
	return result.Issue, nil
}

// autoRoutedBeadGateTarget looks id up in the contributor auto-routed store.
// It returns (nil, nil) when no auto-route is configured; like a prefix
// route, a store that cannot be opened or does not return the bead leaves its
// absence unconfirmed.
func autoRoutedBeadGateTarget(ctx context.Context, localStore storage.DoltStorage, id string) (*types.Issue, error) {
	routedStore, routed, _, err := openRoutedReadStore(ctx, localStore)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errBeadGateTargetUnconfirmed, err)
	}
	if !routed {
		return nil, nil
	}
	defer func() { _ = routedStore.Close() }()

	result, err := resolveAndGetFromStore(ctx, routedStore, id, true)
	if err != nil {
		return nil, fmt.Errorf("%w: looking up bead %s in the auto-routed store: %w", errBeadGateTargetUnconfirmed, id, err)
	}
	if result.Issue == nil {
		return nil, fmt.Errorf("%w: the auto-routed store did not return bead %s", errBeadGateTargetUnconfirmed, id)
	}
	return result.Issue, nil
}

// lookupBeadGateTarget asks st for id and reports whether the answer came
// from this rig's own store.
func lookupBeadGateTarget(ctx context.Context, st issueGetter, id string) (*types.Issue, bool, error) {
	if g, ok := st.(beadGateTargetGetter); ok {
		return g.getBeadGateTarget(ctx, id)
	}
	issue, err := st.GetIssue(ctx, id)
	return issue, true, err
}

// beadGateCheck is the outcome of one bead gate lookup. gone means this rig's
// own store answered that the awaited bead does not exist; seenHere means it
// returned the bead, not yet closed.
type beadGateCheck struct {
	resolved bool
	reason   string
	targetID string
	gone     bool
	seenHere bool
}

// beadGateTargetID returns the bead ID named by a bead gate's await_id. A
// plain await_id names a bead in this rig. The historical <rig>:<bead-id>
// form uses the bead ID as the routed lookup key; the rig component is
// retained for compatibility while routes.jsonl remains keyed by bead prefix.
// When there is nothing to look up, it returns the reason the gate stays
// pending instead.
func beadGateTargetID(awaitID string) (targetID, pendingReason string) {
	if awaitID == "" {
		return "", "bead gate has no await_id"
	}
	if !strings.Contains(awaitID, ":") {
		return awaitID, ""
	}
	parts := strings.SplitN(awaitID, ":", 2)
	if parts[0] == "" || parts[1] == "" {
		return "", fmt.Sprintf("invalid cross-rig bead gate %q: expected <rig>:<bead-id>", awaitID)
	}
	return parts[1], ""
}

// inspectBeadGate looks up a bead gate's target. A non-nil err means the
// awaited bead could not be read at all (backend or transport failure): the
// gate is neither resolved nor pending, and the caller reports it as an error
// rather than letting a dead store read as "still waiting". A bead that this
// rig's own store reports missing is not an error either: it is reported gone,
// and resolved with a "no longer exists" reason. A miss anywhere else (see
// errBeadGateTargetUnconfirmed) keeps the gate pending.
//
// The supplied getter owns local-versus-routed lookup policy. The lookup alone
// is not the bead-gate rule: a gone bead resolves the gate only after an
// earlier sighting, so decide through evaluateBeadGate.
func inspectBeadGate(ctx context.Context, st issueGetter, awaitID string) (beadGateCheck, error) {
	targetID, pendingReason := beadGateTargetID(awaitID)
	if pendingReason != "" {
		return beadGateCheck{reason: pendingReason}, nil
	}
	c := beadGateCheck{targetID: targetID}
	if st == nil {
		c.reason = fmt.Sprintf("bead gate %q: no local store available", awaitID)
		return c, nil
	}

	issue, local, err := lookupBeadGateTarget(ctx, st, targetID)
	switch {
	case errors.Is(err, errBeadGateTargetUnconfirmed):
		// Checked before the not-found test below, because the routing error
		// it wraps can itself be a not-found from the routed store.
		c.reason = fmt.Sprintf("bead gate %q: %v", awaitID, err)
	case err != nil && !beadGateNotFound(err):
		return c, fmt.Errorf("bead gate %q: %w", awaitID, err)
	case err != nil || issue == nil:
		// A bead that no longer exists can never close, so a gate awaiting it
		// would stay pending forever. Resolve it.
		c.resolved, c.gone = true, true
		c.reason = fmt.Sprintf("awaited bead %s no longer exists (treated as resolved)", targetID)
	case issue.Status == types.StatusClosed:
		c.resolved = true
		c.reason = fmt.Sprintf("bead %s closed", targetID)
	default:
		c.seenHere = local
		c.reason = fmt.Sprintf("bead %s is %s", targetID, issue.Status)
	}
	return c, nil
}

// beadGateSeenKey is the gate metadata key that records a bd gate check
// seeing the awaited bead in this rig's own store. Its value is the await_id
// that was seen, so retargeting the gate invalidates the record.
const beadGateSeenKey = "await_seen"

// evaluateBeadGate is the bd gate check rule for a bead gate. It adds one
// condition to inspectBeadGate: an awaited bead that does not exist resolves
// the gate only if an earlier check saw it here and the stored gate still
// records that sighting (see rereadBeadGate). An await_id that never named a
// real bead (a typo, a short ID, a rig without a route) keeps the gate pending
// with a diagnostic instead of unblocking its step. getter reads the gate's own
// store. recordSeen, when non-nil, records the first sighting of the bead on
// the gate.
func evaluateBeadGate(ctx context.Context, gate *types.Issue, getter issueGetter, recordSeen func(gate *types.Issue) error) (bool, string, error) {
	c, err := inspectBeadGate(ctx, getter, gate.AwaitID)
	if err != nil {
		return false, "", err
	}
	seen := beadGateTargetSeen(gate)
	if c.gone && !seen {
		return false, fmt.Sprintf("awaited bead %s not found, and no earlier gate check saw it; check the await_id, or close the gate with bd gate resolve", c.targetID), nil
	}
	if c.gone {
		if pendingReason, err := rereadBeadGate(ctx, getter, gate, c.targetID); err != nil || pendingReason != "" {
			return false, pendingReason, err
		}
	}
	if c.seenHere && !seen && recordSeen != nil {
		if err := recordSeen(gate); err != nil {
			return false, "", fmt.Errorf("recording that bead %s exists: %w", c.targetID, err)
		}
	}
	return c.resolved, c.reason, nil
}

// rereadBeadGate reads gate back from getter once its awaited bead, targetID,
// was found gone, and returns a pending reason unless the stored gate still
// waits on the same await_id and records its sighting. The caller's copy can
// predate a rename of the bead: bd rename points the gate at the new ID
// before the bead takes it (see renameIssueKeepingBeadGates), so a check that
// listed the gate before the rename and looks the bead up after it finds the
// old ID gone on a copy that still records the sighting. This read comes after
// that miss, so it sees the gate already moved. An error means the gate could
// not be read at all.
func rereadBeadGate(ctx context.Context, getter issueGetter, gate *types.Issue, targetID string) (string, error) {
	stored, local, err := lookupBeadGateTarget(ctx, getter, gate.ID)
	switch {
	case err != nil && !errors.Is(err, errBeadGateTargetUnconfirmed) && !beadGateNotFound(err):
		return "", fmt.Errorf("reading gate %s back: %w", gate.ID, err)
	case err != nil || stored == nil || !local:
		return fmt.Sprintf("awaited bead %s not found, and the gate could not be read back to confirm it still waits on it; check it again", targetID), nil
	case stored.AwaitID != gate.AwaitID || !beadGateTargetSeen(stored):
		return fmt.Sprintf("awaited bead %s not found, but the gate changed while it was being checked; check it again", targetID), nil
	}
	return "", nil
}

// beadGateTargetSeen reports whether gate metadata records a sighting of the
// gate's current await_id. Unreadable metadata counts as no sighting.
func beadGateTargetSeen(gate *types.Issue) bool {
	return gate != nil && gate.AwaitID != "" && beadGateSeenID(gate) == gate.AwaitID
}

// beadGateSeenID returns the await_id that gate metadata records a sighting
// of, or "" when it records none. Unreadable metadata counts as none.
func beadGateSeenID(gate *types.Issue) string {
	if len(gate.Metadata) == 0 {
		return ""
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(gate.Metadata, &meta); err != nil {
		return ""
	}
	var seen string
	if err := json.Unmarshal(meta[beadGateSeenKey], &seen); err != nil {
		return ""
	}
	return seen
}

// beadGateSeenUpdate is the update that records a sighting of awaitID. It is
// a metadata merge operation, resolved against the row inside the write
// transaction, so other metadata keys on the gate are preserved.
func beadGateSeenUpdate(awaitID string) map[string]interface{} {
	value, _ := json.Marshal(awaitID)
	return map[string]interface{}{
		storageissueops.OpSetMetadata: map[string]json.RawMessage{beadGateSeenKey: value},
	}
}

// beadGateSeenPatch is beadGateSeenUpdate as a Lifecycle patch: the same
// single-key metadata set, which a connected HTTP workspace sends as
// issues.update.
func beadGateSeenPatch(awaitID string) issueops.IssuePatch {
	value, _ := json.Marshal(awaitID)
	return issueops.IssuePatch{Metadata: issueops.MetadataPatch{Set: map[string]json.RawMessage{beadGateSeenKey: value}}}
}

// beadGateRetargetUpdate is the update that points a bead gate at awaitID.
// With dropSeen it also removes the gate's sighting in the same write, for a
// rename step that cannot keep it (see renameIssueKeepingBeadGates).
func beadGateRetargetUpdate(awaitID string, dropSeen bool) map[string]interface{} {
	update := map[string]interface{}{"await_id": awaitID}
	if dropSeen {
		update[storageissueops.OpUnsetMetadata] = []string{beadGateSeenKey}
	}
	return update
}

// closeGate closes a gate issue with the given reason
func closeGate(_ interface{}, gateID, reason string) error {
	if err := store.CloseIssue(rootCtx, gateID, reason, currentActor(), ""); err != nil {
		return err
	}
	commandDidWrite.Store(true)
	return nil
}

// escalateGate sends an escalation for a failed/expired gate
func escalateGate(gate *types.Issue, reason string) {
	topic := fmt.Sprintf("Gate escalation: %s", gate.ID)
	message := fmt.Sprintf("Gate %s needs attention.\nType: %s\nReason: %s\nCreated: %s",
		gate.ID,
		gate.AwaitType,
		reason,
		gate.CreatedAt.Format(time.RFC3339))

	// Call gt escalate if available
	escalateCmd := exec.Command("gt", "escalate", topic, "-s", "HIGH", "-m", message)
	escalateCmd.Stdout = os.Stdout
	escalateCmd.Stderr = os.Stderr
	if err := escalateCmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: escalation failed for %s: %v\n", gate.ID, err)
	}
}

func init() {
	// gate list flags
	gateListCmd.Flags().BoolP("all", "a", false, "Show all gates including closed")
	gateListCmd.Flags().IntP("limit", "n", 50, "Limit results (default 50)")

	// gate resolve flags
	gateResolveCmd.Flags().StringP("reason", "r", "", "Reason for resolving the gate")

	// gate check flags
	gateCheckCmd.Flags().StringP("type", "t", "", "Gate type to check (gh, gh:run, gh:pr, gl, gl:pipeline, gl:mr, timer, bead, all)")
	gateCheckCmd.Flags().Bool("dry-run", false, "Show what would happen without making changes")
	gateCheckCmd.Flags().BoolP("escalate", "e", false, "Escalate failed/expired gates")
	gateCheckCmd.Flags().IntP("limit", "l", 100, "Limit results (default 100)")

	// gate create flags
	gateCreateCmd.Flags().String("blocks", "", "Issue ID to block (required)")
	gateCreateCmd.Flags().StringP("type", "t", "human", "Gate type (human, timer, gh:run, gh:pr, gl:pipeline, gl:mr)")
	gateCreateCmd.Flags().StringP("reason", "r", "", "Reason for the gate")
	gateCreateCmd.Flags().String("await-id", "", "Condition identifier (run ID, PR number, etc.)")
	gateCreateCmd.Flags().String("repo", "", "Repository for GitHub/GitLab gates (GitLab: nested NAMESPACE/PROJECT or https://HOST/NAMESPACE/PROJECT); default: the blocked issue's metadata.repo, else the current repository")
	gateCreateCmd.Flags().String("timeout", "", "Timeout duration (e.g., 2h, 30m)")
	gateCreateCmd.Flags().String("title", "", "Custom gate title (default: \"Gate: <type>\")")
	_ = gateCreateCmd.MarkFlagRequired("blocks")

	// Issue ID completions
	gateShowCmd.ValidArgsFunction = issueIDCompletion
	gateResolveCmd.ValidArgsFunction = issueIDCompletion
	gateAddWaiterCmd.ValidArgsFunction = issueIDCompletion
	gateCreateCmd.ValidArgsFunction = issueIDCompletion

	// Add subcommands
	gateCmd.AddCommand(gateListCmd)
	gateCmd.AddCommand(gateCreateCmd)
	gateCmd.AddCommand(gateShowCmd)
	gateCmd.AddCommand(gateResolveCmd)
	gateCmd.AddCommand(gateCheckCmd)
	gateCmd.AddCommand(gateAddWaiterCmd)

	rootCmd.AddCommand(gateCmd)
}
