package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/atomicfile"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
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
//	4  the dirty working set blocking the is_blocked repair is NOT transient: the
//	   same pending graph edits have blocked every tick for a while and nothing
//	   is advancing. Retrying will never publish; an operator must resolve it.
const (
	ExitSyncConflict         = 2
	ExitSyncRetriesExhausted = 3
	ExitSyncDirtyStuck       = 4
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
	syncStatusDirtyStuck       = "dirty-stuck"
	syncStatusDisabled         = "disabled"
	syncStatusNoRemote         = "no-remote"
)

// Kinds of per-attempt transient failure recorded in syncOutcome.Transients.
const (
	syncTransientPushRace   = "push-race"
	syncTransientDirtyGraph = "dirty-graph"
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
	// DiscardedPullError records a genuine pull failure (transport, auth) that
	// this run reports as a conflict instead, because live conflict rows from
	// a DIFFERENT cause (e.g. another writer on a shared sql-server) were also
	// found at the same instant. The conflict report is correct — the database
	// really is conflicted — but attributing the halt to that conflict alone
	// would hide a real transport error the operator also needs to see.
	DiscardedPullError string `json:"discarded_pull_error,omitempty"`
	// Transients is every transient failure this run hit, in attempt order.
	// LastPushError/LastRecomputeError answer "what did the FINAL attempt fail
	// on" — deliberately, since that is what the operator's next step depends
	// on — and because each retry clears the other they cannot answer "what did
	// this run actually fight". A run that lost a push race and then hit a
	// dirty working set reports only the second in those two fields; both are
	// here (wy-wub2s, from the wy-mlnz2 review's F7/F8).
	Transients []syncTransient `json:"transients,omitempty"`
	// DirtyGraphFingerprint identifies the pending graph edits that blocked the
	// is_blocked repair, when every blocked attempt in this run saw the SAME
	// ones. Empty means either no attempt was blocked, or the working set was
	// visibly moving between attempts, or the evidence was unavailable — in all
	// of which cases there is nothing to compare across ticks. It is an opaque
	// token: compare for equality, never parse.
	DirtyGraphFingerprint string `json:"dirty_graph_fingerprint,omitempty"`
	// DirtyGraphStuckTicks counts consecutive sync runs, including this one,
	// that exhausted their budget against this same fingerprint. Set by the
	// caller from the persisted marker, not by the loop.
	DirtyGraphStuckTicks int `json:"dirty_graph_stuck_ticks,omitempty"`
	// ConstraintViolations names the graph-table (issues, dependencies)
	// constraint violations that escalated this run straight to dirty-stuck
	// (exit 4) on the attempt that found them, instead of waiting out
	// syncStuckTicks. Empty means this run's dirty-stuck status, if any, came
	// from the tick-count inference instead (wy-mhouc).
	ConstraintViolations []storage.ConstraintViolation `json:"constraint_violations,omitempty"`
}

// syncTransient is one attempt's transient failure.
type syncTransient struct {
	Attempt int    `json:"attempt"`
	Kind    string `json:"kind"`
	Error   string `json:"error,omitempty"`
}

// sawTransient reports whether any attempt failed with kind.
func (o *syncOutcome) sawTransient(kind string) bool {
	for _, t := range o.Transients {
		if t.Kind == kind {
			return true
		}
	}
	return false
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
	// dirtyFingerprint identifies the pending graph edits currently blocking
	// the is_blocked repair (issueops.DirtyGraphFingerprint semantics: "" means
	// clean, an error means the evidence is unavailable). May be nil, which the
	// loop treats exactly like unavailable evidence — it never escalates on a
	// question it could not ask.
	dirtyFingerprint func(context.Context) (string, error)
	// mergeBlockers reports schema conflicts, constraint violations, and
	// merge state for the current working set (storage.MergeBlockerInspector).
	// Used to tell a graph table stuck on constraint violations no writer
	// will ever commit from a merely busy one, on the FIRST blocked attempt
	// rather than waiting out syncStuckTicks. May be nil, which the loop
	// treats exactly like a probe failure — it never escalates on a question
	// it could not ask.
	mergeBlockers func(context.Context) (storage.MergeBlockers, error)
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
	var evidence dirtyEvidence

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
			// pullErr only describes the conflict itself when the merge
			// captured it (merged is non-empty). If merged is empty, this
			// error is unrelated to the live conflict rows — e.g. a transport
			// failure racing another replica's already-conflicted state on a
			// shared sql-server — and returning nil below would silently
			// drop it. Surface it instead of discarding it.
			if pullErr != nil && len(merged) == 0 {
				out.DiscardedPullError = pullErr.Error()
			}
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
			out.Transients = append(out.Transients, syncTransient{
				Attempt: attempt, Kind: syncTransientDirtyGraph, Error: err.Error(),
			})
			// Evidence for the STUCK-vs-BUSY question the retry alone cannot
			// answer. Sampled per blocked attempt and folded down at the end:
			// see dirtyEvidence.fold. Sampling failures are recorded, not
			// returned — a run whose evidence is unavailable still retries and
			// still reports the transient exit, exactly as before.
			evidence.observe(ops.sample(ctx))

			// Positive escalation (wy-mhouc): a graph table left dirty by
			// constraint violations no writer will ever commit is knowable
			// right now, from storage.MergeBlockerInspector, rather than
			// waited out over syncStuckTicks consecutive ticks. A nil hook or
			// a failed probe reports nothing — unavailable evidence must
			// never escalate — so this only ever narrows, never replaces, the
			// tick-based inference below.
			if violations := graphConstraintViolations(ctx, ops); len(violations) > 0 {
				out.Status = syncStatusDirtyStuck
				out.ConstraintViolations = violations
				ops.report("recompute-blocked: constraint violations on the dirty graph table(s) — escalating")
				return out, nil
			}
			ops.report("recompute-blocked: working set dirty (concurrent writer) — re-pulling and retrying")
			continue
		}
		out.LastRecomputeError = ""
		// The repair ran, so whatever was dirty cleared: this run has SEEN the
		// working set advance, and any earlier blocked attempt was transient by
		// demonstration. Dropping the samples is what keeps a later push-race
		// exhaustion from inheriting stuck-looking evidence.
		evidence = dirtyEvidence{}
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
		out.Transients = append(out.Transients, syncTransient{
			Attempt: attempt, Kind: syncTransientPushRace, Error: pushErr.Error(),
		})
		ops.report("push race (non-fast-forward) — re-pulling and retrying")
	}

	out.Status = syncStatusRetriesExhausted
	out.DirtyGraphFingerprint = evidence.fold()
	return out, nil
}

// dirtyEvidence accumulates one fingerprint per blocked attempt.
type dirtyEvidence struct {
	samples []string
	// unavailable records that at least one sample could not be taken, which
	// disqualifies the whole run: a fold over the attempts we happened to see
	// would claim "nothing changed" about attempts we never looked at.
	unavailable bool
}

func (e *dirtyEvidence) observe(fingerprint string, err error) {
	if err != nil || fingerprint == "" {
		// An error means the evidence is unavailable. So does "" here, which
		// says the graph tables were CLEAN by the time we looked — the guard
		// fired and then the other writer committed, i.e. exactly the transient
		// case, and a value that is not a fingerprint must never be compared as
		// one.
		e.unavailable = true
		return
	}
	e.samples = append(e.samples, fingerprint)
}

// fold returns the fingerprint common to every blocked attempt, or "" when the
// run proves nothing: no samples, an unavailable one, or a working set that
// visibly moved between attempts (a busy fleet, which must never escalate).
func (e *dirtyEvidence) fold() string {
	if e.unavailable || len(e.samples) == 0 {
		return ""
	}
	for _, s := range e.samples[1:] {
		if s != e.samples[0] {
			return ""
		}
	}
	return e.samples[0]
}

// sample reads the current dirty-graph fingerprint, treating an absent hook as
// unavailable evidence.
func (o syncOps) sample(ctx context.Context) (string, error) {
	if o.dirtyFingerprint == nil {
		return "", errors.New("dirty-graph evidence not available")
	}
	return o.dirtyFingerprint(ctx)
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

// graphConstraintViolations reports the constraint violations, if any,
// outstanding on the graph tables (issues, dependencies) — the same table set
// isRecomputeDirtyGraphErr just found dirty. It is what lets the loop tell a
// table that is dirty because of a constraint violation no writer will ever
// commit from one that is merely being written to right now: the former is
// knowable positively, on the spot, from storage.MergeBlockerInspector,
// instead of waited out over several ticks (wy-mhouc).
//
// A nil hook or a failed probe reports no violations. That is deliberate:
// this only ever narrows an already-transient classification to a stronger
// one, so unavailable evidence must fall back to "keep retrying", never
// invent a violation it could not confirm.
func graphConstraintViolations(ctx context.Context, ops syncOps) []storage.ConstraintViolation {
	if ops.mergeBlockers == nil {
		return nil
	}
	blockers, err := ops.mergeBlockers(ctx)
	if err != nil {
		return nil
	}
	var out []storage.ConstraintViolation
	for _, v := range blockers.ConstraintViolations {
		if issueops.IsBlockedRecomputeGraphTable(v.Table) {
			out = append(out, v)
		}
	}
	return out
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
retried on the same budget as a push race rather than failing the run. A working
set that is NOT transient exits 4 instead, because no amount of retrying will
ever publish and only an operator can clear it. Two kinds of evidence say so:
constraint violations on the dirty tables are detected positively and escalate
on the very attempt that finds them; an abandoned uncommitted edit has no such
positive signal, so it is only inferred once the same pending graph edits have
blocked every attempt of several consecutive runs.

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
  4  the dirty working set is stuck, not busy: identical pending graph edits
     blocked every attempt of several consecutive runs — nothing pushed, and no
     later tick will publish until an operator clears it

On the default-remote path, a rig with no Dolt remote yet but a git origin
configured adopts that origin as its Dolt remote first, exactly as 'bd dolt push'
does — so 'bd sync' works as a first-time federation bring-up step instead of
reporting 'no remote' and doing nothing. Passing --remote never adopts anything.

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
	syncCmd.Flags().BoolP("yes", "y", false, "Consent to adopting a Dolt remote derived from git origin when none is configured")
	syncCmd.Flags().Bool("no-adopt", false, "Never derive a Dolt remote from git origin (also BD_NO_REMOTE_ADOPT=1)")
	rootCmd.AddCommand(syncCmd)
}

// syncAdoptGitOrigin is runSyncCommand's git-origin adoption step, held in a
// variable purely as a test seam. Adoption's own machinery — resolving the
// active workspace, shelling out to `git remote get-url origin`, writing
// sync.remote into config.yaml and committing it — is exercised against the
// real thing in dolt_test.go; letting it run for real from a runSyncCommand
// unit test would mutate whatever repo the tests happen to be run from. The
// production binding is pinned by TestSyncAdoptGitOriginIsWiredToAdoption.
var syncAdoptGitOrigin func(context.Context, storage.DoltStorage, adoptPolicy, adoptOptIn) (bool, error) = adoptGitOriginRemoteForPush

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

	// Mirror what `bd dolt push` does before it pushes (dolt.go): a rig whose
	// git origin implies a Dolt remote is not a remote-less rig, so adopt that
	// remote before the loop runs.
	//
	// Without this the two verbs disagree about the same rig. On a first-time
	// federation rig — git origin configured, no Dolt remote registered yet —
	// `bd dolt push` adopts origin and pushes, while `bd sync` pulls, fails with
	// Dolt's bare no-remote wording, and the confirmed-no-remote gate below
	// agrees the rig has none (nothing ever adopted it), so bring-up by `bd sync`
	// reports status=no-remote and exits 0, silently doing nothing (wy-gpzg7).
	//
	// Default-remote path only: an explicit --remote names a remote the operator
	// expects to already exist, and inventing a different one there would sync
	// somewhere they never asked for — the same reason the no-remote exit-0 gate
	// is default-remote-only. Unlike `bd dolt push` this runs even under
	// no-push, because sync's *pull* needs the remote as much as the push does;
	// a no-push rig is a local-only mirror, not a rig forbidden to learn where it
	// mirrors from. On a rig that already has a remote — listed in dolt_remotes
	// or persisted on disk — adoption is a hasConfiguredRemote no-op, so the
	// steady-state cost is the one listing the error path already paid.
	if remote == "" {
		// Same consent gate as `bd dolt push` (#5068): sync publishes the same
		// history to the same derived remote, so it cannot be the soft way in.
		syncYes, _ := cmd.Flags().GetBool("yes")
		syncNoAdopt, _ := cmd.Flags().GetBool("no-adopt")
		adopted, adoptErr := syncAdoptGitOrigin(rootCtx, st, currentAdoptPolicy(syncYes, syncNoAdopt, stdinIsTerminal(), jsonOutput), syncAdoptOptIn)
		if adoptErr != nil {
			return HandleErrorRespectJSON("sync failed: adopting git origin as Dolt remote: %v", adoptErr)
		}
		if adopted && !jsonOutput && !isQuiet() {
			fmt.Println("sync: configured Dolt remote origin from git origin.")
		}
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
		// Evidence for the stuck-vs-busy question, read straight from the
		// working set rather than parsed out of the guard's message. Absent
		// raw-SQL access the hook stays nil and the loop simply never escalates.
		dirtyFingerprint: dirtyGraphFingerprintOp(st),
		mergeBlockers:    mergeBlockersOp(st),
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
			// This is the benign case, not an error -q means to keep: a solo
			// rig with no remote configured at all should not print ~15 lines
			// of onboarding guidance every tick of an unattended timer.
			if !isQuiet() {
				printNoRemoteGuidance()
			}
			return nil
		}
		exitErr := HandleErrorRespectJSON("sync failed: %v", err)
		if !jsonOutput {
			printSyncErrorGuidance(remote, err)
		}
		return exitErr
	}

	// Cross-tick half of the stuck detector, before any reporting so the output
	// and the exit code agree on what this run was.
	applyDirtyProgress(out, time.Now())

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
	case syncStatusDirtyStuck:
		return &exitError{Code: ExitSyncDirtyStuck}
	default:
		return nil
	}
}

// mergeBlockersOp builds the loop's positive constraint-violation hook, or nil
// when this store cannot answer the question — an unimplemented interface
// must leave the detector silent, never guessing. Bound to the same st the
// rest of ops closes over rather than the package's legacy store global,
// which getStore() can diverge from once cmdCtx is in play.
func mergeBlockersOp(st storage.DoltStorage) func(context.Context) (storage.MergeBlockers, error) {
	inspector, ok := storage.UnwrapStore(st).(storage.MergeBlockerInspector)
	if !ok {
		return nil
	}
	return inspector.GetMergeBlockers
}

// dirtyGraphFingerprintOp builds the loop's dirty-graph evidence hook, or nil
// when this store cannot answer the question — an unimplemented interface must
// leave the detector silent, never guessing.
func dirtyGraphFingerprintOp(st storage.DoltStorage) func(context.Context) (string, error) {
	accessor, ok := storage.UnwrapStore(st).(storage.RawDBAccessor)
	if !ok {
		return nil
	}
	db := accessor.DB()
	if db == nil {
		return nil
	}
	return func(ctx context.Context) (string, error) {
		return issueops.DirtyGraphFingerprint(ctx, db)
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
			"before sync can make progress — Dolt refuses to merge over an unresolved one.",
			"Inspect with: bd conflicts list / bd conflicts show",
			conflictsResolveHint)
	case out.ConflictsLive:
		lines = append(lines,
			"The conflict rows above are LIVE in the working set — this pull route leaves them",
			"in place for you rather than aborting. The database is conflicted right now, and",
			"Dolt will refuse to merge over it, so every later sync halts here until you resolve",
			"it. Nothing was pushed.",
			"Inspect with: bd conflicts list / bd conflicts show",
			conflictsResolveHint)
	default:
		lines = append(lines,
			"The conflicted merge was aborted and the working set restored, so no local work was",
			"lost — and sync will keep halting here, unchanged, until an operator resolves the",
			"divergence between this replica and the remote. Nothing is lost by waiting.",
			"'bd conflicts list' will show nothing right now — this pull route restores the working",
			"set instead of leaving conflicts live for it. The same conflict recurs on every retry",
			"until the two histories are reconciled by hand (see 'bd vc merge --help' for a route",
			"that leaves conflicts live and resolvable instead of aborting).")
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
	// The conflict rows above are real, but this run ALSO hit a pull error
	// unrelated to them — surfacing only the conflict would hide it.
	if out.DiscardedPullError != "" {
		lines = append(lines,
			"Note: this run's pull also failed with an error unrelated to the conflict above:",
			fmt.Sprintf("  pull error: %s", out.DiscardedPullError),
			"That failure is reported here only — resolving the conflict will not fix it.")
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
			"If it is NOT transient (a table left dirty by constraint violations never clears), the",
			"next few runs will see the identical pending edits and escalate to exit 4 rather than",
			"reporting this forever.",
		}
		return append(lines, syncMixedTransientNote(out)...)
	}
	lines := []string{fmt.Sprintf("Error: push-race retries exhausted after %d attempt(s).", out.Attempts)}
	if out.LastPushError != "" {
		lines = append(lines, fmt.Sprintf("  last push error: %s", out.LastPushError))
	}
	lines = append(lines,
		"This is transient — another replica kept winning the race. Retry on the next tick, or raise --attempts.")
	return append(lines, syncMixedTransientNote(out)...)
}

// syncMixedTransientNote reports the transient conditions this run fought that
// the headline does not name. The headline is about the FINAL attempt, which is
// the right thing to act on; without this an operator reading "push-race
// retries exhausted" has no way to know a dirty working set also ate an attempt
// of the budget, and would raise --attempts when the real story is contention
// on two different axes (wy-wub2s).
func syncMixedTransientNote(out *syncOutcome) []string {
	if !out.sawTransient(syncTransientPushRace) || !out.sawTransient(syncTransientDirtyGraph) {
		return nil
	}
	return []string{
		"Note: this run hit BOTH transient conditions — a lost push race and a dirty working set.",
		"The report above names what the final attempt failed on; --json lists every attempt under",
		"\"transients\".",
	}
}

// syncStuckMessage renders the operator-facing report for exit 4: the dirty
// working set is not going to clear on its own.
//
// This is the escalation exit 3 cannot make. A permanently-dirty graph table —
// constraint violations no writer will ever commit, an abandoned uncommitted
// edit — reports the same "transient, retry on the next tick" forever, so no
// tick ever publishes and nothing in the output ever changes to say so
// (wy-wub2s). Two distinct kinds of evidence can drive this: out.ConstraintViolations
// is a POSITIVE read, from storage.MergeBlockerInspector, naming exactly what
// is stuck and why on the very attempt that found it (wy-mhouc); absent that,
// out.DirtyGraphStuckTicks is the fallback INFERENCE — the pending graph edits
// have been byte-identical across every attempt of the last N runs, so this is
// a stuck table rather than a busy fleet, without knowing the specific cause.
func syncStuckMessage(out *syncOutcome) []string {
	if len(out.ConstraintViolations) > 0 {
		lines := []string{
			"Error: the is_blocked repair is blocked by constraint violations no writer will ever commit.",
		}
		for _, v := range out.ConstraintViolations {
			lines = append(lines, fmt.Sprintf("  constraint violation: %s (%d row(s))", v.Table, v.Count))
		}
		if out.LastRecomputeError != "" {
			lines = append(lines, fmt.Sprintf("  last recompute error: %s", out.LastRecomputeError))
		}
		lines = append(lines,
			"A constraint violation is not something any commit resolves — the auto-repair path already",
			"declined it, so retrying can never publish. Resolve it by hand, then the next tick syncs",
			"normally:",
			"  bd vc status                 # what is dirty",
			"  bd conflicts list            # constraint violations / conflicts holding it dirty",
			"  bd vc commit -m '...'        # commit the pending changes, if they are wanted",
			"Nothing was pushed. This exit is deliberately distinct from exit 3 so a sync timer can page",
			"instead of retrying forever.")
		return append(lines, syncMixedTransientNote(out)...)
	}
	lines := []string{
		fmt.Sprintf("Error: the is_blocked repair has been blocked by the SAME pending graph edits for %d consecutive sync run(s).", out.DirtyGraphStuckTicks),
	}
	if out.LastRecomputeError != "" {
		lines = append(lines, fmt.Sprintf("  last recompute error: %s", out.LastRecomputeError))
	}
	lines = append(lines,
		"Nothing is advancing: every attempt saw an identical set of uncommitted changes to",
		"issues/dependencies, so this is not a concurrent writer that is about to commit. Retrying",
		"cannot publish — the repair refuses to derive is_blocked from a graph it cannot commit, so",
		"local commits stay unpublished until an operator clears the working set.",
		"Resolve it by hand, then the next tick syncs normally:",
		"  bd vc status                 # what is dirty",
		"  bd conflicts list            # constraint violations / conflicts holding it dirty",
		"  bd vc commit -m '...'        # commit the pending changes, if they are wanted",
		"Nothing was pushed. This exit is deliberately distinct from exit 3 so a sync timer can page",
		"instead of retrying forever.")
	return append(lines, syncMixedTransientNote(out)...)
}

// syncStuckTicks is how many consecutive exhausted runs against byte-identical
// pending graph edits it takes before sync calls the working set stuck rather
// than busy.
//
// It is deliberately more than one. A single run's attempts are paced by one
// pull round trip each, so a fleet writing in bursts really can show the same
// fingerprint for the whole budget; requiring the evidence to survive several
// runs — minutes apart on a timer — is what keeps a busy shared server from
// being escalated as a stuck one. Every intervening run that publishes, or that
// sees any different pending edits, resets the count to zero.
const syncStuckTicks = 3

// syncStateFile holds the cross-tick half of the stuck detector, beside the
// auto-export state. It is local scratch, never version-controlled: writing this
// evidence into the database would add to the very dirty working set it is
// evidence about.
const syncStateFile = "sync-state.json"

// syncState is what one sync run leaves behind for the next one.
type syncState struct {
	// DirtyGraphFingerprint is the opaque token from the last exhausted run.
	DirtyGraphFingerprint string `json:"dirty_graph_fingerprint,omitempty"`
	// StuckTicks counts consecutive exhausted runs that saw it.
	StuckTicks int       `json:"stuck_ticks,omitempty"`
	FirstSeen  time.Time `json:"first_seen,omitempty"`
}

// classifyDirtyProgress folds this run's evidence into the persisted marker and
// reports the marker the next run should see, plus whether this run escalates.
//
// Pure, so the escalation rule is testable without a clock, a filesystem, or a
// Dolt server. Any outcome that is not an exhausted-on-dirty run clears the
// marker: a run that published, conflicted, or exhausted on a push race is
// evidence that this replica is not wedged on pending graph edits.
func classifyDirtyProgress(out *syncOutcome, prev *syncState, now time.Time) (*syncState, bool) {
	// LastRecomputeError, not just any dirty transient: the marker is about the
	// condition that is still blocking us as the run ends.
	blocked := out.Status == syncStatusRetriesExhausted &&
		out.LastRecomputeError != "" &&
		out.DirtyGraphFingerprint != ""
	if !blocked {
		return &syncState{}, false
	}
	next := &syncState{DirtyGraphFingerprint: out.DirtyGraphFingerprint, StuckTicks: 1, FirstSeen: now}
	if prev != nil && prev.DirtyGraphFingerprint == out.DirtyGraphFingerprint {
		next.StuckTicks = prev.StuckTicks + 1
		if !prev.FirstSeen.IsZero() {
			next.FirstSeen = prev.FirstSeen
		}
	}
	return next, next.StuckTicks >= syncStuckTicks
}

// applyDirtyProgress runs the cross-tick half of the detector: load the marker,
// classify, persist, and promote the outcome to the stuck status when the
// evidence has survived long enough.
//
// Failure to read or write the marker is not fatal and not reported: the detector
// is an escalation on top of a working retry, so a rig with no .beads directory
// (or an unwritable one) keeps the pre-existing exit-3 behavior instead of losing
// the sync.
func applyDirtyProgress(out *syncOutcome, now time.Time) {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return
	}
	next, stuck := classifyDirtyProgress(out, loadSyncState(beadsDir), now)
	saveSyncState(beadsDir, next)
	out.DirtyGraphStuckTicks = next.StuckTicks
	if stuck {
		out.Status = syncStatusDirtyStuck
	}
}

func loadSyncState(beadsDir string) *syncState {
	data, err := os.ReadFile(filepath.Join(beadsDir, syncStateFile)) //nolint:gosec // path is the resolved .beads dir
	if err != nil {
		return &syncState{}
	}
	var state syncState
	if err := json.Unmarshal(data, &state); err != nil {
		return &syncState{}
	}
	return &state
}

func saveSyncState(beadsDir string, state *syncState) {
	path := filepath.Join(beadsDir, syncStateFile)
	if state == nil || state.DirtyGraphFingerprint == "" {
		// Nothing to remember. Remove rather than write an empty marker so a
		// healthy rig does not carry stale scratch around.
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			debug.Logf("sync: failed to clear %s: %v\n", path, err)
		}
		return
	}
	data, err := json.Marshal(state)
	if err != nil {
		debug.Logf("sync: failed to marshal sync state: %v\n", err)
		return
	}
	if err := atomicfile.WriteFile(path, data, 0o600); err != nil {
		debug.Logf("sync: failed to save sync state: %v\n", err)
	}
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
	case syncStatusDirtyStuck:
		for _, line := range syncStuckMessage(out) {
			fmt.Fprintln(os.Stderr, line)
		}
	default:
		// The success path is exactly the non-essential output -q exists to
		// silence — this verb's whole point is running unattended on a short
		// timer. The conflict and retries-exhausted branches above are NOT
		// gated: -q means "errors only", and those are the errors.
		if isQuiet() {
			return
		}
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
