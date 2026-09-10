package eventsjournal

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

// Bounded-by-default retention.
//
// A journal that only ever grows is a workspace-sized foot-gun: the feature is
// off by default, so the operator who turns it on is not the one who discovers,
// months later, that a table nobody reads has become the largest thing in the
// database. Retention floors already describe the window a consumer is
// guaranteed; this applies them without being asked, so "enabled" means
// "bounded" unless the operator says otherwise.
//
// The shape is SQLite's WAL auto-checkpoint, not a background compactor: the
// WRITER pays, immediately after its own commit, only when a cheap persisted
// threshold says work is due, and never for more than a fixed slice of it. Dolt
// does the same thing one layer down with auto-GC. Neither needs a daemon, and
// neither can turn a user's command into an unbounded wait.
//
// Three properties are load-bearing:
//
//   - The POLICY is the floors, and it is not implemented here.
//     ComputeEventsAutoPruneBoundInTx hands the existing resolver an unbounded
//     request and keeps what the floors leave. Both floors disabled means the
//     operator chose an unbounded ledger, and auto-prune becomes a no-op.
//   - Nothing runs inside the mutation's transaction. The runner opens its own,
//     after the write has committed.
//   - Nothing here can fail a command. Every error is reported to the caller,
//     which logs it and continues; a workspace whose maintenance is broken must
//     still be a workspace whose `bd create` works.
//
// SCOPE, stated because "automatic" invites a broader reading than it earns: a
// pass maintains the workspace whose command triggered it. A routed write
// (`bd create --repo ../other`) journals into the TARGET, but the trigger runs
// against the launcher's own workspace and its own plumbing — deliberately, so
// one command cannot open maintenance transactions on an arbitrary number of
// databases it merely wrote to in passing. A workspace that is only ever
// written from elsewhere therefore relies on commands run in it, or on its own
// `bd serve`, for retention. Documented in docs/reference/events-journal.md.
type autoPruneState struct {
	// TS is when auto-prune last ran here.
	TS time.Time `json:"ts"`
	// Head is the journal head it saw then. The difference against the current
	// head is the volume half of the throttle.
	Head int64 `json:"head"`
}

const (
	// AutoPruneConfigKey turns automatic retention on and off. Default TRUE:
	// see the file comment. Setting both floors to 0 is the other way to get an
	// unbounded journal, and the honest one — this key exists for a consumer
	// that wants the floors respected on reads but wants to own deletion
	// itself.
	AutoPruneConfigKey = "events-journal-auto-prune"

	// AutoPruneEnvVar is the environment override for AutoPruneConfigKey,
	// matching viper's BD_ prefix and hyphen-to-underscore mapping.
	AutoPruneEnvVar = "BD_EVENTS_JOURNAL_AUTO_PRUNE"

	// AutoPruneSlotKey names the throttle watermark in local_metadata — the
	// clone-local, dolt_ignored metadata table. It belongs on the ignored plane
	// for the same reason the journal does: it describes what THIS clone has
	// maintained, and syncing it would make one clone's maintenance schedule
	// another's.
	AutoPruneSlotKey = "events_journal_autoprune"

	// DefaultAutoPruneInterval is how long a workspace goes between passes on
	// time alone. An hour is short enough that an idle-but-writing workspace
	// stays inside its floors and long enough that a burst of commands pays for
	// at most one pass between them.
	DefaultAutoPruneInterval = time.Hour

	// DefaultAutoPruneVolumeRows is the second trigger: a pass also becomes due
	// once the journal has advanced this far since the last one, whatever the
	// clock says. Without it, a bulk import that writes a million records in ten
	// minutes would be bounded by nothing until the hour was up. 5k is a
	// fraction of the 100k rows floor, so the volume trigger fires long before
	// the default floor is under any pressure.
	DefaultAutoPruneVolumeRows = 5000
)

// AutoPruneEnabledFor reports whether automatic retention is on for the
// workspace rooted at beadsDir, with the same precedence as journal activation
// (env, then that workspace's own config.yaml) but defaulting to TRUE.
func AutoPruneEnabledFor(beadsDir string) bool {
	return resolveEnabledFor(AutoPruneEnvVar, AutoPruneConfigKey, beadsDir, true)
}

// AutoPruneOptions is what a pass needs beyond the plumbing. The zero value is
// the shipped policy: production callers set only the two floors.
type AutoPruneOptions struct {
	// RetainDays and RetainRows are the configured floors. Both 0 disables
	// auto-prune entirely — an unbounded journal by explicit choice.
	RetainDays int
	RetainRows int
	// Now is the clock. Zero means time.Now().UTC().
	Now time.Time
	// Interval and VolumeRows override the throttle thresholds. Zero means the
	// defaults above. They are constants rather than configuration on purpose:
	// they tune how often maintenance amortizes, which is an implementation
	// detail of the amortization, not a promise to anyone.
	Interval   time.Duration
	VolumeRows int64
	// Timeout bounds the whole pass. Zero means DefaultAutoPrunePassTimeout.
	Timeout time.Duration
}

func (o AutoPruneOptions) resolved() AutoPruneOptions {
	if o.Now.IsZero() {
		o.Now = time.Now().UTC()
	}
	if o.Interval <= 0 {
		o.Interval = DefaultAutoPruneInterval
	}
	if o.VolumeRows <= 0 {
		o.VolumeRows = DefaultAutoPruneVolumeRows
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultAutoPrunePassTimeout
	}
	return o
}

// DefaultAutoPrunePassTimeout is the ceiling on what a user's command can be
// made to wait for maintenance it did not ask for.
//
// It is the second half of the humility the no-retry maintenance transaction
// buys (see DoltStore.RunEventsMaintenanceTx): the retry loop is the known way
// to spend a minute losing races, and this bounds every other way — a
// contended table, a slow disk, a plumbing that blocks on a pool. Abandoning a
// pass costs nothing: each batch commits on its own, so the work already done
// stands, the watermark is already stamped, and the next trigger resumes. A
// workspace slow enough to time out drains one batch per pass instead of three,
// which is still bounded and still unattended.
const DefaultAutoPrunePassTimeout = 30 * time.Second

// AutoPrune runs at most one throttled, capped auto-prune pass against runner
// and returns how many rows it deleted. It returns (0, nil) when the throttle
// says nothing is due, which is the answer almost every time it is called.
//
// The sequence is four steps, each in its own transaction:
//
//  1. Is anything due? ONE query, and on the common answer that is the entire
//     cost of the call.
//  2. Stamp the watermark, and COMMIT it before anything else is attempted.
//     That transaction is its own for a reason: everything after it — resolving
//     the bound, the deletes — can fail on a locked table, a killed process, a
//     full disk, and the watermark has to survive that failure or every
//     subsequent command retries the same failing pass. Maintenance that cannot
//     succeed must degrade to no maintenance, never to a per-command penalty.
//     Sharing a transaction with the bound read would roll the stamp back with
//     the read that failed, which is exactly the shape that promise excludes.
//  3. Resolve the bound and delete the prefix in capped batches, one
//     transaction each.
//  4. Stop at the cap. Whatever is left waits for the next pass.
//
// Concurrency needs no coordination. Two processes that both find a pass due
// resolve the same bound and delete the same prefix; the loser of the race
// deletes fewer rows (or none) and both are correct, because a prefix delete
// that already happened is indistinguishable from one that just did.
func AutoPrune(ctx context.Context, runner issueops.EventsMaintenanceRunner, opts AutoPruneOptions) (int64, error) {
	opts = opts.resolved()
	if runner == nil {
		return 0, fmt.Errorf("events journal: no maintenance plumbing")
	}
	if opts.RetainDays <= 0 && opts.RetainRows <= 0 {
		// An unbounded ledger by explicit configuration. Costs nothing: not even
		// the throttle read runs.
		return 0, nil
	}

	// Every transaction below runs under the pass budget, including the ones a
	// caller cannot see failing (the CLI trigger discards this context on the
	// way out of PostRunE).
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	var (
		due  bool
		head int64
	)
	if err := runner.RunEventsMaintenanceTx(ctx, func(ctx context.Context, tx issueops.DBTX) error {
		watermark, currentHead, err := issueops.ReadEventsAutoPruneStateInTx(ctx, tx, AutoPruneSlotKey)
		if err != nil {
			return err
		}
		head = currentHead
		due = autoPruneDue(watermark, currentHead, opts)
		return nil
	}); err != nil {
		return 0, err
	}
	if !due || head <= 0 {
		return 0, nil
	}

	stamp, err := json.Marshal(autoPruneState{TS: opts.Now, Head: head})
	if err != nil {
		return 0, fmt.Errorf("events journal: encode auto-prune watermark: %w", err)
	}
	if err := runner.RunEventsMaintenanceTx(ctx, func(ctx context.Context, tx issueops.DBTX) error {
		return issueops.SetEventsAutoPruneStateInTx(ctx, tx, AutoPruneSlotKey, string(stamp))
	}); err != nil {
		return 0, err
	}

	var (
		bound int64
		skip  bool
	)
	if err := runner.RunEventsMaintenanceTx(ctx, func(ctx context.Context, tx issueops.DBTX) error {
		var boundErr error
		bound, skip, boundErr = issueops.ComputeEventsAutoPruneBoundInTx(ctx, tx, opts.RetainDays, opts.RetainRows, opts.Now)
		return boundErr
	}); err != nil {
		return 0, err
	}
	if skip {
		return 0, nil
	}

	var deleted int64
	for range issueops.EventsAutoPruneMaxBatches {
		var n int64
		if err := runner.RunEventsMaintenanceTx(ctx, func(ctx context.Context, tx issueops.DBTX) error {
			var pruneErr error
			n, pruneErr = issueops.PruneEventsBatchInTx(ctx, tx, bound, issueops.EventsAutoPruneBatchRows)
			return pruneErr
		}); err != nil {
			// Report what did land: a partial drain is progress, and the caller
			// logs both halves.
			return deleted, err
		}
		deleted += n
		if n < issueops.EventsAutoPruneBatchRows {
			break
		}
	}
	return deleted, nil
}

// DefaultAutoPruneTickInterval is how often a long-lived process (bd serve)
// ASKS whether a pass is due. It is deliberately shorter than
// DefaultAutoPruneInterval: the tick is a poll, the persisted watermark is the
// schedule. Polling more often than the interval is what lets the volume
// trigger fire promptly under a write burst, and costs one indexed query per
// tick when it does not.
const DefaultAutoPruneTickInterval = 5 * time.Minute

// TickAutoPrune runs a pass on every tick until ctx is done, reporting each
// outcome to report (which may be nil). It returns when ctx is done, so a
// caller that wants it in the background runs it in a goroutine — see
// StartAutoPruneTicker.
//
// The tick channel is a parameter rather than an interval so the loop is
// testable without waiting on a real clock, and so the caller keeps ownership
// of the ticker it must stop.
func TickAutoPrune(ctx context.Context, runner issueops.EventsMaintenanceRunner, tick <-chan time.Time, opts AutoPruneOptions, report func(int64, error)) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-tick:
			if !ok {
				return
			}
			// Each pass re-reads the clock through opts.resolved(); a ticker
			// that inherited one fixed Now would compute every retain-days
			// cutoff from process start.
			pass := opts
			pass.Now = time.Time{}
			n, err := AutoPrune(ctx, runner, pass)
			// A pass interrupted by shutdown fails with the canceled context;
			// that is the process stopping, not a maintenance problem, and
			// reporting it would put an error in the log of every clean
			// shutdown.
			if report != nil && ctx.Err() == nil {
				report(n, err)
			}
		}
	}
}

// StartAutoPruneTicker runs the maintenance loop in the background and returns
// the function that stops it. The returned stop WAITS for the loop to exit, so
// a server can guarantee no maintenance transaction is in flight past shutdown
// — a delete racing a closing connection pool is a confusing error in a log
// nobody is watching by then.
func StartAutoPruneTicker(ctx context.Context, runner issueops.EventsMaintenanceRunner, every time.Duration, opts AutoPruneOptions, report func(int64, error)) func() {
	if every <= 0 {
		every = DefaultAutoPruneTickInterval
	}
	// The loop gets a context of its own so stop() can end it on a path where
	// the parent is still live — a server that failed to bind, or any early
	// return before the signal context is canceled. Waiting on a loop only the
	// parent could release would deadlock exactly there.
	loopCtx, cancel := context.WithCancel(ctx)
	ticker := time.NewTicker(every)
	done := make(chan struct{})
	go func() {
		defer close(done)
		TickAutoPrune(loopCtx, runner, ticker.C, opts, report)
	}()
	return func() {
		cancel()
		ticker.Stop()
		<-done
	}
}

// autoPruneClockSkewTolerance is how far into the future a watermark may sit
// before it is treated as unusable rather than as a schedule.
//
// The stamp is a wall-clock time written by whichever process last ran a pass,
// so an NTP step forward, a VM restored from a snapshot, or a container that
// booted with a bad clock can leave a timestamp hours or years ahead. Compared
// naively, `now - stamp` is then negative forever and the interval trigger
// never fires again — retention silently stops on a workspace that looks
// healthy. Reading a far-future stamp as DUE costs one extra pass and rewrites
// the slot with a sane time; the tolerance keeps ordinary sub-minute skew from
// defeating the throttle it exists to enforce.
const autoPruneClockSkewTolerance = 5 * time.Minute

// autoPruneDue reads the persisted watermark against the current head. An
// absent, unparseable or future-dated watermark is DUE: a workspace that has
// never pruned, or whose slot was truncated by a hand-edit or a partial
// restore, or stamped by a clock since corrected, should get a pass rather than
// be excluded from maintenance forever by a value nobody can use. The pass
// rewrites the slot, so all three self-heal on first use.
func autoPruneDue(watermark string, head int64, opts AutoPruneOptions) bool {
	if watermark == "" {
		return true
	}
	var state autoPruneState
	if err := json.Unmarshal([]byte(watermark), &state); err != nil || state.TS.IsZero() {
		return true
	}
	elapsed := opts.Now.Sub(state.TS)
	if elapsed < -autoPruneClockSkewTolerance {
		return true
	}
	if elapsed >= opts.Interval {
		return true
	}
	return head-state.Head >= opts.VolumeRows
}
