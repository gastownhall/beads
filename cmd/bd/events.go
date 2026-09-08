package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/eventsjournal"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/uow"
)

// eventFollowPollInterval is how often `bd events tail --follow` polls the
// journal table for new rows. The journal is a local table read, so polling is
// cheap; a one-second cadence keeps a live consumer responsive without busy-waiting.
const eventFollowPollInterval = time.Second

// bd events reads and manages the durable events journal
// (bd_events_journal). The journal is an append-only, seq-ordered record of
// every committed issue mutation, written in the same transaction as the
// mutation. Scripts and integrations tail it to replay the exact history of a
// workspace. It is OFF by default; enable with `bd config set events-journal
// true` (or BD_EVENTS_JOURNAL=1).

var eventsCmd = &cobra.Command{
	Use:     "events",
	GroupID: "maint",
	Short:   "Read and manage the durable events journal",
	Long: `Read and manage the durable events journal (bd_events_journal).

The journal records every committed issue mutation as an ordered, replayable
row. Enable it with 'bd config set events-journal true' (or
BD_EVENTS_JOURNAL=1). Records are emitted only while it is enabled.

Retention is automatic: an enabled journal is bounded to the retention floors
(events-journal-retain-days / -rows, 7 days / 100k rows by default) without
anyone running a command. Disable both floors for an unbounded ledger, or
events-journal-auto-prune for manual control; 'bd events prune' remains for an
earlier, on-demand cut below the floors.

Coverage and scope:
  - Every mutation through bd's normal write paths (create, update, close,
    reopen, delete, claim, dependency add/remove, label add/remove, comment) is
    journaled in the same transaction as the change. Raw DML run through
    'bd sql' bypasses those paths and is NOT journaled — a known non-coverage.
  - The journal is per-branch working-set state (dolt_ignored): it records the
    mutations committed on the writer's active branch. Rows arrive by direct
    write, not by merge, so a consumer must read the journal on the same branch
    the writer commits to; a branch checkout or merge does not carry journal
    rows across branches.
  - For the same reason the journal is per REPLICA. 'bd dolt pull' and the
    changes a merge settles into this clone are not journaled: those rows
    arrived as data, not as local mutations, and nothing on this clone wrote
    them through the mutation seam. A consumer that mirrors a synced workspace
    must re-baseline (a fresh export or a full re-read) after a sync, because
    the journal describes only what THIS clone mutated.
    Each replica also has its OWN seq space, counted from its own first
    mutation. A checkpoint taken against one replica is meaningless against
    another — the same seq names a different record, and a seq above the other
    replica's head reads as "caught up" and stalls forever. Track a checkpoint
    per replica, and re-baseline rather than carry one across.
  - A few writes that happen while a store is being OPENED are unjournaled by
    design: schema migrations and the version reconciliation that runs before
    the workspace's configuration has been applied to the store. They touch
    schema and clone-local metadata, never a bead, so a replaying consumer has
    nothing to apply them to.
  - Dependency records are not symmetric, in two ways.
    Count: a dep_add is emitted for every accepted add, INCLUDING an idempotent
    same-type re-add that only refreshes the edge's metadata. The audit 'events'
    table deduplicates that case and writes nothing; the journal does not. Treat
    dep_add as an upsert of the edge, not as proof the edge is new. A dep_remove
    naming an edge that is already gone emits nothing at all.
    Payload: dep.metadata differs in provenance between the two ops. On dep_add
    it is the value being written, as the caller supplied it; on dep_remove it is
    the raw stored column read back just before the delete. The two can differ
    byte for byte while meaning the same thing, so compare parsed values.

Structural dependency edits — the ones bd wires up itself rather than a 'bd dep'
verb — write no audit event but DO journal, by design: a replaying consumer
needs the edge either way.`,
}

var eventsTailCmd = &cobra.Command{
	Use:   "tail",
	Short: "Print journal records after a sequence number (JSON lines)",
	Long: `Print events journal records with seq greater than --since, in order.

Each line is a JSON record:
  {"seq":N,"ts":"...","op":"create|update|close|delete|dep_add|dep_remove|comment",
   "issue_id":"...","actor":"...","issue":{...|null},"dep":{"kind":..,"target":..,"metadata":..},"comment":{...}}

Record contract (stable for external consumers):
  seq       int64   counter-assigned inside the mutation's transaction; gapless,
                    strictly increasing in commit order, never reused or reset
  ts        string  UTC insert time, stamped inside the committing transaction
  op        string  one of the seven ops above
  issue_id  string  the mutated issue's id
  actor     string  the acting identity that performed the mutation, as resolved
                    for the audit-events table (on a comment row: the comment's
                    author); empty (omitted) when the mutation path has no
                    actor — derived maintenance, deletes (other than a rename's
                    synthetic delete), and rows older than the column. Never
                    user attribution when empty.
  issue     object  full issue state AFTER the mutation; null on delete
  dep       object  {"kind","target","metadata"} for dep_add / dep_remove; omitted otherwise
  comment   object  {"id","author","text","created_at","source"} for comment; omitted otherwise

Poll with the highest seq seen to consume new mutations incrementally, or pass
--follow to keep printing new records as they are committed (Ctrl-C to stop).

Retention boundary: if --since is below the oldest retained record — the prefix
you asked for was pruned — the read FAILS instead of silently skipping ahead or
returning an empty success. With --json the failure carries
{"code":"events_journal_truncated","since":N,"floor":F,"head":H}: floor is the
oldest seq still retained, head the highest ever assigned. Resume from floor-1
to continue with a known gap, or rebuild from a full export.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		since, _ := cmd.Flags().GetInt64("since")
		limit, _ := cmd.Flags().GetInt("limit")
		follow, _ := cmd.Flags().GetBool("follow")
		// A negative checkpoint is a caller bug — most likely arithmetic on an
		// empty cursor. `seq > -5` would quietly serve the whole journal as if
		// it were a legitimate resume, so say so instead.
		if since < 0 {
			return HandleErrorRespectJSON("--since must be zero or a positive sequence number (got %d); use 0 to read from the beginning", since)
		}
		return runEventsTail(rootCtx, since, limit, follow)
	},
}

var eventsExportCmd = &cobra.Command{
	Use:   "export",
	Short: "Print the entire journal from the beginning (JSON lines)",
	Long: `Print every events journal record from seq 1, in order, as JSON lines.

Equivalent to 'bd events tail --since 0'. Like tail, it FAILS rather than
present a pruned journal's surviving suffix as a complete history.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		limit, _ := cmd.Flags().GetInt("limit")
		return runEventsTail(rootCtx, 0, limit, false)
	},
}

var eventsPruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Delete journal records below a sequence number (retention)",
	Long: `Delete events journal records with seq less than --before.

Retention is already enforced automatically: after a mutating command commits,
and on a timer in 'bd serve', bd deletes everything the floors below do not
protect. This command is for an EARLIER, on-demand cut BELOW the floors — after
a consumer has durably processed a span you do not want to wait out. It cannot
cut deeper than the floors: shrinking the retained window itself means lowering
them. The journal is clone-local operational state, so pruning never affects
issue data.

Two retention floors compose onto --before and can only reduce what a prune
removes. They bound the automatic prune and this one identically:
  events-journal-retain-days   keep every row younger than N days (default 7)
  events-journal-retain-rows   always keep the newest N rows (default 100000)

Set BOTH floors to 0 for an unbounded ledger: automatic pruning then does
nothing, and this command becomes the only thing that deletes a record. To keep
the floors but own deletion yourself, set events-journal-auto-prune false.

Note the floors are time-based and count-based — they are NOT a consumer
watermark. They protect only the recent window; a consumer that has fallen
further behind than both floors allow will be pruned past and lose records.
Consumers are responsible for tracking their own watermark (the highest seq they
have durably processed) and for sizing the floors to the longest outage they
intend to survive. Pruned history cannot be recovered from the workspace — the
journal is the only local copy. Pruning frees rows, not disk: pair it with
'dolt gc' to reclaim the space, since the table is working-set (dolt_ignored)
state that ordinary Dolt commits never garbage-collect.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		before, _ := cmd.Flags().GetInt64("before")
		if before <= 0 {
			return HandleErrorRespectJSON("--before must be a positive sequence number")
		}
		return runEventsPrune(rootCtx, before)
	},
}

func init() {
	eventsTailCmd.Flags().Int64("since", 0, "return records with seq greater than this value")
	eventsTailCmd.Flags().Int("limit", 0, "maximum number of records to return (0 = no limit)")
	eventsTailCmd.Flags().Bool("follow", false, "keep printing new records as they are committed (Ctrl-C to stop)")
	eventsExportCmd.Flags().Int("limit", 0, "maximum number of records to return (0 = no limit)")
	eventsPruneCmd.Flags().Int64("before", 0, "delete records with seq less than this value")

	eventsCmd.AddCommand(eventsTailCmd)
	eventsCmd.AddCommand(eventsExportCmd)
	eventsCmd.AddCommand(eventsPruneCmd)
	rootCmd.AddCommand(eventsCmd)
}

// reportEventsTruncated renders a pruned-past checkpoint as a machine-readable
// failure. A consumer must be able to branch on this without parsing prose, so
// JSON mode carries the code and the window it can still serve; the caller then
// decides whether to resume from floor-1 and accept the gap or rebuild.
//
// streaming says the failure interrupts a JSONL stream that has already emitted
// records — the --follow poll. There the error must be ONE line of JSON on the
// same stream, because the consumer on the other end is a line reader: a
// pretty-printed multi-line object dropped into the middle of its input is not
// something it can parse, and it would see a stream that simply stops with
// garbage after the last good record. Same fields, same code, same exit status;
// only the framing follows the stream it interrupts.
func reportEventsTruncated(err error, streaming bool) error {
	var trunc *storage.EventsJournalTruncatedError
	if !errors.As(err, &trunc) {
		return HandleErrorRespectJSON("reading events journal: %v", err)
	}
	if jsonOutput {
		payload := map[string]any{
			"error": trunc.Error(),
			"code":  storage.EventsJournalTruncatedCode,
			"since": trunc.Since,
			"floor": trunc.Floor,
			"head":  trunc.Head,
		}
		if streaming {
			if encErr := json.NewEncoder(os.Stdout).Encode(payload); encErr != nil {
				return encErr
			}
			return &exitError{Code: 1}
		}
		if encErr := outputJSON(payload); encErr != nil {
			return encErr
		}
		return &exitError{Code: 1}
	}
	return HandleErrorWithHint(trunc.Error(),
		fmt.Sprintf("resume with --since %d to continue from the oldest retained record (accepting the gap), or re-import from scratch", trunc.Floor-1))
}

func runEventsTail(ctx context.Context, since int64, limit int, follow bool) error {
	enc := json.NewEncoder(os.Stdout)
	emit := func(from int64) (int64, error) {
		rows, err := readJournal(ctx, from, limit)
		if err != nil {
			return from, err
		}
		for _, r := range rows {
			if err := enc.Encode(r); err != nil {
				return from, err
			}
			if r.Seq > from {
				from = r.Seq
			}
		}
		return from, nil
	}

	last, err := emit(since)
	if err != nil {
		return reportEventsTruncated(err, false)
	}
	if !follow {
		return nil
	}
	// Follow: poll for rows with seq beyond the last one emitted. The journal is
	// a local table read, so a modest poll cadence is cheap. Stop on Ctrl-C
	// (rootCtx is signal-aware), reporting no error for a clean interruption.
	ticker := time.NewTicker(eventFollowPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if last, err = emit(last); err != nil {
				return reportEventsTruncated(err, true)
			}
		}
	}
}

func runEventsPrune(ctx context.Context, before int64) error {
	retainDays := config.GetInt("events-journal-retain-days")
	retainRows := config.GetInt("events-journal-retain-rows")
	n, err := pruneJournal(ctx, before, retainDays, retainRows)
	if err != nil {
		return HandleErrorRespectJSON("pruning events journal: %v", err)
	}
	return reportEventsPruned(n, before)
}

func reportEventsPruned(n, before int64) error {
	if jsonOutput {
		return outputJSON(map[string]any{"pruned": n})
	}
	fmt.Printf("Pruned %d events journal record(s) below seq %d\n", n, before)
	return nil
}

// journalAccessor returns the active store's events-journal capability. The
// embedded store and the server-mode store both provide it (via their own
// transaction machinery); a backend that does not is reported as unsupported.
func journalAccessor() (storage.EventsJournalAccessor, error) {
	if store == nil {
		return nil, fmt.Errorf("no database connection available (%s)", diagHint())
	}
	acc, ok := storage.UnwrapStore(store).(storage.EventsJournalAccessor)
	if !ok {
		return nil, fmt.Errorf("storage backend does not support the events journal")
	}
	return acc, nil
}

// readJournal reads records with seq greater than since from the active
// storage seam. Proxied-server mode uses its transaction-bound UOW journal
// capability; direct stores use EventsJournalAccessor.
//
// The projection onto the published envelope is eventsjournal.Records, the same
// one GET /v0/beads/events serves from — see the note on eventsjournal.Record
// for why there is exactly one.
func readJournal(ctx context.Context, since int64, limit int) ([]eventsjournal.Record, error) {
	var rows []storage.EventsJournalRow
	if usesProxiedServer() {
		if uowProvider == nil {
			return nil, fmt.Errorf("no proxied-server unit-of-work provider available")
		}
		uw, err := uowProvider.NewUOW(ctx)
		if err != nil {
			return nil, err
		}
		defer uw.Close(ctx)
		rows, err = uw.EventsJournalUseCase().Read(ctx, since, limit)
		if err != nil {
			return nil, err
		}
	} else {
		acc, err := journalAccessor()
		if err != nil {
			return nil, err
		}
		rows, err = acc.ReadEventsJournal(ctx, since, limit)
		if err != nil {
			return nil, err
		}
	}
	return eventsjournal.Records(rows), nil
}

// pruneJournal deletes records below before honoring the retain floors.
func pruneJournal(ctx context.Context, before int64, retainDays, retainRows int) (int64, error) {
	if usesProxiedServer() {
		if uowProvider == nil {
			return 0, fmt.Errorf("no proxied-server unit-of-work provider available")
		}
		// The journal table is dolt_ignored, so the delete must persist into the
		// working set WITHOUT minting a Dolt commit — the same ephemeral commit
		// discipline lease writes use.
		return uow.RunTxEphemeral(ctx, uowProvider, func(ctx context.Context, uw uow.UnitOfWork) (int64, error) {
			return uw.EventsJournalUseCase().Prune(ctx, before, retainDays, retainRows)
		})
	}
	acc, err := journalAccessor()
	if err != nil {
		return 0, err
	}
	return acc.PruneEventsJournal(ctx, before, retainDays, retainRows)
}
