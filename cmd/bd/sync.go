package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// Exit codes for `bd sync`, chosen so a sync timer can branch on the outcome
// without parsing output (wy-jpd3.4). The reference deployment this verb
// generalizes — a two-machine beads federation on a 60-second timer — needs
// exactly three distinctions: it worked, a human must resolve a merge conflict,
// or another replica kept winning the push race and the next tick should just
// try again.
//
//	0  synced (or nothing to do)
//	1  error (transport, auth, storage — the usual bd failure code)
//	2  merge conflict; the sync halted and nothing was pushed. NOT auto-resolved.
//	3  retries exhausted on a transient, self-healing condition (another replica
//	   kept winning the push race, or a concurrent writer kept the working set
//	   dirty); retry on the next tick.
const (
	ExitSyncConflict         = 2
	ExitSyncRetriesExhausted = 3
)

// defaultSyncAttempts bounds the pull->push cycle. Three is the production
// default of the reference implementation: a push race resolves on the first
// retry in practice, and an unbounded loop under a busy fleet never converges.
const defaultSyncAttempts = 3

// Sync outcome statuses, also the "status" value in --json output.
const (
	syncStatusOK               = "ok"
	syncStatusConflict         = "conflict"
	syncStatusRetriesExhausted = "retries-exhausted"
	syncStatusDisabled         = "disabled"
	syncStatusNoRemote         = "no-remote"
)

// syncOutcome is one run of the sync loop.
type syncOutcome struct {
	Status    string   `json:"status"`
	Attempts  int      `json:"attempts"`
	Conflicts []string `json:"conflicts,omitempty"`
	// ConflictsPreexisting distinguishes conflicts that were already live in
	// the working set when sync started (an earlier halted sync or a hand-run
	// merge) from ones this run's pull surfaced.
	ConflictsPreexisting bool `json:"conflicts_preexisting,omitempty"`
	// ConflictsLive reports whether the conflicts are sitting in the working
	// set right now rather than having been aborted away. It is read from
	// which detection source fired, never assumed: the SQL pull route aborts a
	// conflicted merge and restores the working set, while the CLI/git-protocol
	// route deliberately leaves the conflict rows live for the operator.
	ConflictsLive bool `json:"conflicts_live,omitempty"`
	RowsCorrected int  `json:"rows_corrected"`
	// Pulled reports whether an earlier attempt in this run already completed a
	// pull and is_blocked repair, which makes "this run touched nothing" untrue
	// on a later attempt's conflict.
	Pulled        bool   `json:"pulled,omitempty"`
	Pushed        bool   `json:"pushed"`
	PushSkipped   bool   `json:"push_skipped,omitempty"`
	LastPushError string `json:"last_push_error,omitempty"`
	// LastRecomputeError records a retryable is_blocked-repair failure (the
	// working set was dirty). At most one of LastPushError and
	// LastRecomputeError is set at a time: each retry clears the other, so on
	// an exhausted run the one that survives names what the FINAL attempt
	// actually failed on.
	LastRecomputeError string `json:"last_recompute_error,omitempty"`
}

// syncOps is the store surface the loop drives, injected as functions so the
// loop's control flow (the part with the interesting failure modes) is testable
// without a live Dolt remote.
type syncOps struct {
	// pull merges the remote into the local branch. It returns the conflicted
	// table names the merge captured, if any — see runSyncLoop's property (1).
	pull func(context.Context) ([]string, error)
	// conflicts positively reports live merge conflicts (dolt_conflicts).
	conflicts func(context.Context) ([]string, error)
	// recompute runs the full is_blocked recompute and returns rows corrected.
	recompute func(context.Context) (int, error)
	// push publishes local commits to the remote.
	push func(context.Context) error
	// progress reports a step to the operator; may be nil.
	progress func(format string, args ...interface{})
}

func (o syncOps) report(format string, args ...interface{}) {
	if o.progress != nil {
		o.progress(format, args...)
	}
}

// runSyncLoop is the whole federation loop: pull -> positive conflict check ->
// recompute-blocked -> push, retrying a bounded number of times when another
// replica wins the push race.
//
// Two properties are load-bearing and are why this is not just three shell
// lines:
//
//  1. Conflicts are detected POSITIVELY — from structured conflict data, never
//     inferred from the pull's exit status. A pull fails for plenty of reasons
//     that are not conflicts, and it can also leave conflicts behind without
//     failing, so an exit-status guess both invents phantom conflicts and
//     misses real ones. Two structured sources are consulted per attempt, and
//     either one firing means conflict:
//
//     a. the conflicts the merge itself captured — the settle pass aborts a
//     merge it cannot auto-resolve and hands back a MergeConflictsError
//     holding the conflict rows it read BEFORE the abort;
//     b. live rows in dolt_conflicts, which is what a conflict left behind by
//     some earlier operation (a hand-run merge, a halted sync) looks like.
//
//     Source (a) is why the check cannot just be a dolt_conflicts query: by
//     the time a conflicted pull returns, its merge has been aborted and the
//     working set restored, so dolt_conflicts is empty again.
//
//  2. Conflicts are NEVER auto-resolved here. The loop halts before recomputing
//     or pushing and reports the conflict as a distinct exit code, leaving the
//     divergence for an operator. A sync timer that silently picks a side loses
//     work on a schedule.
//
// The recompute between pull and push is not optional bookkeeping: is_blocked
// is denormalized, and a merge that brings in a dependency edge from another
// replica leaves it stale, so `bd ready` silently hides or surfaces the wrong
// work until someone repairs it.
//
// It runs UNCONDITIONALLY, on every attempt. That is deliberate, and it is the
// second thing about this loop that looks like it wants an optimization and
// must not get one. RecomputeAllBlocked is specifically the repair that does
// NOT depend on a merge advancing HEAD (bd-6dnrw.37): it is what recovers an
// is_blocked column left stale by a post-merge recompute that failed after its
// merge committed, or by a conflicted pull an operator resolved BY HAND. Gating
// it on "did this pull merge anything" re-imposes the exact condition it exists
// to escape, and sync manufactures that state itself — it exits 2 on a
// conflict, the operator resolves by hand, and from then on no tick merges
// anything, so a gated repair would never run again while every tick reports
// success. HEAD is also the wrong instrument for the question even when the
// question is right: the pull's own pre-merge auto-commit (GH#2474) moves HEAD
// for purely local dirty state, and an auto-resolved or cascade-repaired merge
// can land in the working set without moving it at all (bd-6dnrw.39). Anyone
// revisiting the cost of this pass must start from storage.StateHasher and the
// pending-recompute marker, not from HEAD.
//
// Returns (outcome, nil) for every outcome the caller maps to an exit code, and
// (outcome, err) only for genuine failures (exit 1).
func runSyncLoop(ctx context.Context, ops syncOps, maxAttempts int) (*syncOutcome, error) {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	out := &syncOutcome{Status: syncStatusOK}

	// Pre-flight. A previous halted sync leaves its conflicts live, and Dolt
	// refuses to merge over them — without this check that shows up as an
	// opaque pull failure (exit 1) instead of the conflict it actually is.
	preConflicts, err := ops.conflicts(ctx)
	if err != nil {
		return out, fmt.Errorf("conflict check: %w", err)
	}
	if len(preConflicts) > 0 {
		out.Status = syncStatusConflict
		out.Conflicts = preConflicts
		out.ConflictsPreexisting = true
		// These came from the live conflict rows by definition, so a consumer
		// asking "is this database conflicted right now" gets a straight yes.
		out.ConflictsLive = true
		return out, nil
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// A push race re-enters the loop; honor cancellation between attempts
		// so ^C or a timer's deadline is not swallowed by the retry budget.
		if err := ctx.Err(); err != nil {
			return out, err
		}
		out.Attempts = attempt
		ops.report("pull (attempt %d/%d)", attempt, maxAttempts)

		merged, pullErr := ops.pull(ctx)

		// Positive conflict check, run whether or not the pull reported an
		// error, and unioned with what the merge itself captured — see
		// property (1) above.
		live, conflictErr := ops.conflicts(ctx)
		if conflicts := unionTables(merged, live); len(conflicts) > 0 {
			out.Status = syncStatusConflict
			out.Conflicts = conflicts
			// Which source fired decides what the operator is told about the
			// state of the database, and the two answers are opposites. Live
			// rows mean the conflict is sitting in the working set right now:
			// that is what the CLI/git-protocol pull route leaves behind, since
			// it deliberately does not abort for the operator (see
			// finishCLIPull). Captured-only means the settle pass aborted the
			// merge and restored the working set. Guessing either way sends the
			// operator looking in the wrong place.
			out.ConflictsLive = len(live) > 0
			return out, nil
		}
		if pullErr != nil {
			return out, fmt.Errorf("pull: %w", pullErr)
		}
		if conflictErr != nil {
			return out, fmt.Errorf("conflict check: %w", conflictErr)
		}

		ops.report("recompute-blocked")
		corrected, err := ops.recompute(ctx)
		if err != nil {
			if !isRecomputeDirtyGraphErr(err) {
				return out, fmt.Errorf("recompute-blocked: %w", err)
			}
			// Not our failure and not a durable one: someone else's
			// uncommitted edit to issues/dependencies landed between our pull
			// and our repair. Treat it exactly like a push race — re-enter the
			// attempt loop, and if the budget runs out report the transient
			// exit so the next tick tries again. Classifying it as a hard
			// error instead left local commits unpublished until a tick
			// happened to catch a clean working set, which on a shared
			// sql-server topology is luck (wy-mlnz2).
			//
			// Two things about the retry are worth knowing before touching it.
			// It is paced by the pull's round trip, not by a sleep — the loop
			// has none. And the retry does not merely WAIT for the other
			// writer: the pull's own pre-merge auto-commit (GH#2474) stages and
			// commits whatever is dirty, so it is often what clears the guard,
			// committing that writer's already-SQL-committed rows under this
			// sync's author. That is pre-existing behavior on attempt 1 of
			// every tick and is data-safe, but a retry repeats the exposure —
			// so this must stay bounded, and must never become a wait loop.
			out.LastRecomputeError = err.Error()
			out.LastPushError = ""
			ops.report("recompute-blocked: working set dirty (concurrent writer) — re-pulling and retrying")
			continue
		}
		out.LastRecomputeError = ""
		out.RowsCorrected += corrected

		// From here on this run has completed a pull and an is_blocked repair,
		// so a conflict on a LATER attempt cannot describe itself as a run that
		// touched nothing.
		out.Pulled = true

		ops.report("push")
		pushErr := ops.push(ctx)
		if pushErr == nil {
			out.Status = syncStatusOK
			out.Pushed = true
			out.LastPushError = ""
			return out, nil
		}
		if !isPushRaceErr(pushErr) {
			return out, fmt.Errorf("push: %w", pushErr)
		}
		// Another replica pushed between our merge and our push, so the remote
		// genuinely moved: loop back to pull and pick up its commits. Anything
		// that is not a fast-forward race cannot converge by retrying and was
		// returned above.
		out.LastPushError = pushErr.Error()
		ops.report("push race (non-fast-forward) — re-pulling and retrying")
	}

	out.Status = syncStatusRetriesExhausted
	return out, nil
}

// pushRacePattern matches the ways a push fails because the remote moved. Kept
// as a message match because the rejection arrives as an untyped error, and
// deliberately narrow: it matches *race signatures* rather than the word
// "rejected". All three routes a real race travels are covered:
//
//   - the SQL procedure says the branch "is behind its remote counterpart";
//   - the CLI route folds git's `! [rejected] ... (non-fast-forward)` in;
//   - the git-blobstore layer behind `git+*` remotes pushes with
//     --force-with-lease, and a lost lease reads as `(stale info)`,
//     `(fetch first)`, or "the remote contains work that you do not have".
//
// A bare "rejected" is NOT enough. A protected branch or a declining
// pre-receive hook also rejects, permanently; treating that as a race means a
// sync timer burns its whole attempt budget every tick and reports exit 3
// ("transient, retry next tick") forever, never surfacing the failure as the
// error it is.
var pushRacePattern = regexp.MustCompile(
	`(?i)behind|fast.?forward|not\s+(an\s+)?ancestor|stale\s+info|fetch\s+first|contains\s+work\s+that\s+you\s+do\s+not\s+have`)

// isPushRaceErr reports whether a push failed because the remote moved under us
// — the one push failure that retrying can fix.
//
// Hard divergence (no common ancestor, or an ancestor primary-key mismatch) is
// explicitly NOT a race: those messages can contain "fast-forward"-adjacent
// wording, and retrying them burns the whole attempt budget on an operation
// that can never converge. They fall through to the generic error path, which
// prints the existing recovery guidance.
func isPushRaceErr(err error) bool {
	if err == nil {
		return false
	}
	if isDivergedHistoryErr(err) || isAncestorPKMismatchErr(err) {
		return false
	}
	return pushRacePattern.MatchString(err.Error())
}

// isRecomputeDirtyGraphErr reports whether the is_blocked repair refused to run
// because the graph tables (issues, dependencies) had uncommitted working-set
// changes — the one recompute failure that retrying can fix.
//
// This is classified from the typed sentinel, never from the message. The guard
// is a foreign package's error text; matching on it would let a reworded guard
// silently demote this back to a hard error, which is exactly the failure being
// fixed (wy-mlnz2).
//
// Why it is retryable at all: on a shared sql-server topology every agent
// shares one working set, so an uncommitted write from ANOTHER agent — no part
// of this sync, and gone as soon as they commit — is what trips the guard. The
// condition is transient, foreign, and self-healing, so the loop's existing
// retry budget is the right response. It is still never *ignored*: the repair
// is not optional (see runSyncLoop), so an exhausted budget halts before the
// push rather than publishing a stale is_blocked.
func isRecomputeDirtyGraphErr(err error) bool {
	return err != nil && errors.Is(err, issueops.ErrBlockedRecomputeDirtyGraph)
}

// bareNoRemotePattern matches Dolt's bare "no remote" wording. `bd dolt push`
// and `bd dolt pull` classify only the "remote ... not found" phrasing, but a
// default-remote fetch on a rig that never configured one fails with
// `Error 1105: no remote` instead, which that phrasing misses.
var bareNoRemotePattern = regexp.MustCompile(`(?i)\bno remote\b`)

// isNoRemoteConfiguredErr reports whether a sync failure *sounds* like the
// benign "no remote configured" case. It is a strict superset of
// isRemoteNotFoundErr because sync is a timer verb: a solo rig that ran
// `bd init` and never added a remote would otherwise fail on every tick with a
// raw Dolt error code, where `bd dolt push` exits 0. The widening is safe only
// because it is a hint, never the decision — hasNoRemoteConfigured must
// independently prove the remotes are empty before anything exits 0, so a
// deleted remote-side repo or a typoed remote name (both of which have a
// remote configured) still fails loudly.
func isNoRemoteConfiguredErr(err error) bool {
	if err == nil {
		return false
	}
	return isRemoteNotFoundErr(err) || bareNoRemotePattern.MatchString(err.Error())
}

// classifyPullError splits a pull failure into the conflicts the merge itself
// captured and the residual error.
//
// The settle pass aborts a merge it will not auto-resolve and restores the
// working set, so dolt_conflicts is empty again by the time anyone could query
// it; the conflict rows it read pre-abort ride back inside the error instead
// (bd-578h9.15). Reading them here is what makes the sync loop's conflict
// detection positive rather than an exit-status guess, and it is the same
// contract PullFrom implements.
func classifyPullError(err error) ([]string, error) {
	var mce *versioncontrolops.MergeConflictsError
	if errors.As(err, &mce) {
		// A conflict error carrying no conflict rows would otherwise vanish
		// here and let the loop recompute and push on top of a failed merge.
		// Both construction sites guard against it today; this keeps the
		// invariant enforced at the consuming end too.
		if tables := conflictTables(mce.Conflicts); len(tables) > 0 {
			return tables, nil
		}
		return nil, err
	}
	return nil, err
}

// unionTables merges two conflicted-table lists, de-duplicated and sorted.
func unionTables(a, b []string) []string {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(a)+len(b))
	var out []string
	for _, list := range [][]string{a, b} {
		for _, name := range list {
			if seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// conflictTables reports the distinct table names with live conflicts, sorted
// for stable output.
func conflictTables(conflicts []storage.Conflict) []string {
	seen := make(map[string]bool, len(conflicts))
	var tables []string
	for _, c := range conflicts {
		name := c.Field
		if name == "" {
			name = "(unknown)"
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		tables = append(tables, name)
	}
	sort.Strings(tables)
	return tables
}

var syncCmd = &cobra.Command{
	Use:     "sync",
	GroupID: "sync",
	Short:   "Pull, check for conflicts, repair is_blocked, and push (the federation loop)",
	Long: `Run one full synchronization cycle against the Dolt remote.

This is the loop every multi-machine beads deployment otherwise hand-rolls in
shell:

  1. pull from the remote
  2. check for merge conflicts POSITIVELY, from the merge's own conflict rows
     and from Dolt's conflict tables — never inferred from the pull's exit
     status, which is not a trustworthy conflict signal in either direction
  3. recompute the denormalized is_blocked flag, so dependency edges merged in
     from another replica do not leave 'bd ready' stale
  4. push, retrying a bounded number of times when another replica wins the
     push race

The repair in step 3 refuses to run while another writer has uncommitted changes
to issues/dependencies. That is transient and not this sync's doing, so it is
retried on the same budget as a push race rather than failing the run.

Conflicts sync cannot resolve safely are NEVER auto-resolved: it halts before
recomputing or pushing and exits 2, and repeated runs keep halting the same way
until an operator resolves the divergence. (The pull underneath does auto-settle
the conflict classes it can settle convergently — machine-local metadata,
audit-only dependency rows, and last-write-wins on issue cells. Anything beyond
those halts here.) Whether the halted merge was aborted or left live in the
working set depends on the pull route, so the halt message reports which.

Exit codes (a sync timer can branch on these without parsing output):

  0  synced, or nothing to do
  1  error (transport, auth, storage)
  2  merge conflict — halted, nothing pushed, resolve it by hand
  3  retries exhausted (push race, or a concurrent writer's dirty working set)
     — transient, nothing pushed, retry on the next tick

This is not 'bd federation sync', which syncs with named peer towns and takes a
--strategy ours|theirs to resolve whatever conflicts it meets. 'bd sync' targets
the configured remote and has no such switch: what it cannot settle, it halts on.

Examples:
  bd sync                        # sync with the default remote
  bd sync --remote mini          # sync with a specific remote
  bd sync --attempts 5           # allow more push-race retries
  bd sync --json                 # machine-parseable outcome`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runSyncCommand,
}

func init() {
	syncCmd.Flags().String("remote", "", "Sync with a specific named remote instead of the default")
	syncCmd.Flags().Int("attempts", defaultSyncAttempts, "Maximum pull/push attempts before reporting a transient retry exhaustion (exit 3)")
	rootCmd.AddCommand(syncCmd)
}

func runSyncCommand(cmd *cobra.Command, _ []string) error {
	if usesProxiedServer() {
		return HandleErrorRespectJSON("sync is not supported in proxied-server mode")
	}
	CheckReadonly("sync")

	evt := metrics.NewCommandEvent("sync")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	if isDoltLocalOnly() {
		if jsonOutput {
			return outputJSON(&syncOutcome{Status: syncStatusDisabled})
		}
		fmt.Println("Remote sync is disabled for this project (dolt.local-only=true).")
		fmt.Println("To re-enable remote sync: bd config unset dolt.local-only")
		return nil
	}

	attempts, _ := cmd.Flags().GetInt("attempts")
	if attempts < 1 {
		return HandleErrorRespectJSON("--attempts must be at least 1 (got %d)", attempts)
	}
	remote, _ := cmd.Flags().GetString("remote")

	st := getStore()
	if st == nil {
		return HandleErrorRespectJSON("no store available")
	}
	recomputer, ok := storage.UnwrapStore(st).(storage.BlockedRecomputer)
	if !ok {
		return HandleErrorRespectJSON("storage backend does not support is_blocked recompute")
	}

	// A no-push rig still wants the pull and the recompute; only the publish
	// step is off. Reporting that as a push failure would make every tick of a
	// local-only mirror look broken.
	noPush := config.GetBool("no-push")

	ops := syncOps{
		pull: func(ctx context.Context) ([]string, error) {
			var err error
			if remote != "" {
				err = st.PullRemote(ctx, remote)
			} else {
				err = st.Pull(ctx)
			}
			return classifyPullError(err)
		},
		conflicts: func(ctx context.Context) ([]string, error) {
			cs, err := st.GetConflicts(ctx)
			if err != nil {
				return nil, err
			}
			return conflictTables(cs), nil
		},
		recompute: func(ctx context.Context) (int, error) {
			return recomputer.RecomputeAllBlocked(ctx)
		},
		push: func(ctx context.Context) error {
			if noPush {
				return nil
			}
			if remote != "" {
				return st.PushRemote(ctx, remote, false)
			}
			return st.Push(ctx)
		},
	}
	// Per-step progress is the non-essential output -q exists to silence: this
	// verb's whole point is running on a short timer.
	if !jsonOutput && !isQuiet() {
		ops.progress = func(format string, args ...interface{}) {
			fmt.Printf("sync: "+format+"\n", args...)
		}
	}

	out, err := runSyncLoop(rootCtx, ops, attempts)
	if noPush && out.Status == syncStatusOK {
		out.Pushed = false
		out.PushSkipped = true
	}
	if err != nil {
		// A rig with no remote at all is the benign solo case the dolt verbs
		// already exit 0 on — only when the emptiness is *confirmed*, and only
		// on the default-remote path: an explicitly named remote that does not
		// exist is a misconfiguration and stays an error.
		if remote == "" && isNoRemoteConfiguredErr(err) && hasNoRemoteConfigured(rootCtx, st) {
			if jsonOutput {
				return outputJSON(&syncOutcome{Status: syncStatusNoRemote})
			}
			printNoRemoteGuidance()
			return nil
		}
		exitErr := HandleErrorRespectJSON("sync failed: %v", err)
		if !jsonOutput {
			printSyncErrorGuidance(remote, err)
		}
		return exitErr
	}

	if jsonOutput {
		if jerr := outputJSON(out); jerr != nil {
			return HandleError("%v", jerr)
		}
	} else {
		printSyncOutcome(out, noPush)
	}

	switch out.Status {
	case syncStatusConflict:
		return &exitError{Code: ExitSyncConflict}
	case syncStatusRetriesExhausted:
		return &exitError{Code: ExitSyncRetriesExhausted}
	default:
		return nil
	}
}

// printSyncErrorGuidance reuses the recovery guidance the dolt push/pull verbs
// print, so a failed sync is as actionable as the hand-rolled loop it replaces.
// The classifiers match on the message, so they still fire through the step
// wrapping runSyncLoop adds.
func printSyncErrorGuidance(remote string, err error) {
	switch {
	case isAncestorPKMismatchErr(err):
		printAncestorPKMismatchGuidance(err)
	case isDivergedHistoryErr(err):
		printDivergedHistoryGuidance("sync")
	case remote != "" && isRemoteNotFoundErr(err):
		fmt.Fprintf(os.Stderr, "\nRemote %q is not configured.\n", remote)
		fmt.Fprintln(os.Stderr, "Use 'bd dolt remote add <name> <url>' to add it.")
		fmt.Fprintln(os.Stderr, "Use 'bd dolt remote list' to see configured remotes.")
	}
}

// syncConflictMessage renders the operator-facing halt report for a conflicted
// sync. It is a pure function of the outcome because the three cases it
// distinguishes are easy to describe wrongly, and a wrong description here is
// worse than none: an operator who is told "nothing was merged" will not go
// looking for the merge commit that is in fact sitting in the local history,
// and one who is told the working set was restored will not go looking for the
// live conflict rows that are in fact blocking every subsequent merge.
func syncConflictMessage(out *syncOutcome) []string {
	lines := []string{"Error: merge conflict — sync halted, nothing pushed."}
	for _, table := range out.Conflicts {
		lines = append(lines, fmt.Sprintf("  conflicted table: %s", table))
	}
	lines = append(lines, "Conflicts sync cannot resolve safely are never auto-resolved.")

	// First: is the database conflicted RIGHT NOW, or was the conflicted merge
	// aborted away? This is the part an operator acts on, and both answers are
	// real — which one applies depends on the pull route, not on luck.
	switch {
	case out.ConflictsPreexisting:
		lines = append(lines,
			"This replica was ALREADY in a conflicted state before this run: the conflict rows",
			"above are live in the working set, left by an earlier halted sync or a hand-run",
			"merge. Nothing was pulled, merged, or pushed this run. Resolve the live conflict",
			"before sync can make progress — Dolt refuses to merge over an unresolved one.")
	case out.ConflictsLive:
		lines = append(lines,
			"The conflict rows above are LIVE in the working set — this pull route leaves them",
			"in place for you rather than aborting. The database is conflicted right now, and",
			"Dolt will refuse to merge over it, so every later sync halts here until you resolve",
			"it. Nothing was pushed.")
	default:
		lines = append(lines,
			"The conflicted merge was aborted and the working set restored, so no local work was",
			"lost — and sync will keep halting here, unchanged, until an operator resolves the",
			"divergence between this replica and the remote. Nothing is lost by waiting.")
	}

	// Second: did anything at all happen before the halt? Only reported when
	// it did, so the common single-attempt case stays short.
	if out.Pulled {
		lines = append(lines,
			"Note: an earlier attempt in this run completed its pull and is_blocked repair before",
			"losing the push race; the retry is what conflicted. That earlier work remains in the",
			"local database and has NOT been published.")
	}
	// A dirty-working-set retry pulls without ever completing its repair, so it
	// leaves out.Pulled false while still having moved local history. Without
	// this the operator is told the run touched nothing, and goes looking in the
	// wrong place for the commits that pull merged.
	if out.LastRecomputeError != "" {
		lines = append(lines,
			"Note: an earlier attempt in this run completed its pull but its is_blocked repair was",
			"blocked by a dirty working set, so it retried. Anything that pull merged is in the local",
			"database, is NOT repaired, and has NOT been published.")
	}
	return lines
}

// syncRetriesExhaustedMessage renders the operator-facing report for exit 3.
// Two different transient conditions land here and they need different next
// steps: a push race is between REPLICAS and resolves by retrying or raising
// --attempts, while a dirty working set is another writer on THIS replica and
// resolves when they commit. Telling an operator "another replica kept winning
// the race" when the real blocker is an uncommitted local edit sends them to
// the wrong machine. A pure function of the outcome, for the same reason
// syncConflictMessage is.
func syncRetriesExhaustedMessage(out *syncOutcome) []string {
	if out.LastRecomputeError != "" {
		lines := []string{
			fmt.Sprintf("Error: is_blocked repair kept finding a dirty working set after %d attempt(s).", out.Attempts),
			fmt.Sprintf("  last recompute error: %s", out.LastRecomputeError),
			"Another writer has uncommitted changes to issues/dependencies on this replica, and the",
			"repair refuses to derive is_blocked from a graph it cannot commit. Nothing was pushed.",
			"This is transient — retry on the next tick, or commit/discard the pending changes.",
			"If EVERY tick reports this, the dirty state is not transient and no tick will ever",
			"publish: resolve it by hand (a table left dirty by constraint violations never clears).",
		}
		return lines
	}
	lines := []string{fmt.Sprintf("Error: push-race retries exhausted after %d attempt(s).", out.Attempts)}
	if out.LastPushError != "" {
		lines = append(lines, fmt.Sprintf("  last push error: %s", out.LastPushError))
	}
	return append(lines,
		"This is transient — another replica kept winning the race. Retry on the next tick, or raise --attempts.")
}

func printSyncOutcome(out *syncOutcome, noPush bool) {
	switch out.Status {
	case syncStatusConflict:
		for _, line := range syncConflictMessage(out) {
			fmt.Fprintln(os.Stderr, line)
		}
	case syncStatusRetriesExhausted:
		for _, line := range syncRetriesExhaustedMessage(out) {
			fmt.Fprintln(os.Stderr, line)
		}
	default:
		if out.RowsCorrected > 0 {
			fmt.Printf("Recomputed is_blocked: %d row(s) corrected.\n", out.RowsCorrected)
		}
		if noPush {
			fmt.Println("Sync complete (push skipped: rig is local-only, no-push: true).")
			return
		}
		fmt.Println("Sync complete.")
	}
}
