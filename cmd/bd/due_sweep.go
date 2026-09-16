package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/ui"
)

// scheduledSweeper is the capability `bd due sweep` needs: run the two lazy
// time-based sweeps NOW and say what fired.
//
// It is reached by walking Unwrap() rather than by widening storage.Storage,
// the same way storeExportSource reaches the wisp partitioner: the capability
// belongs to the two concrete stores that own a transaction, and every
// decorator between here and them (telemetry, hook firing) has nothing to add
// to a sweep — there is no create, update or close hook for a deadline
// arriving, only the audit event the sweep already writes.
type scheduledSweeper interface {
	RunScheduledSweeps(ctx context.Context) (issueops.ScheduledSweepResult, error)
}

// dueSweepReport is what one sweep did. It is the timer's payload: counts to
// put in a summary line, and ids so a human reading the line can go look.
type dueSweepReport struct {
	SweptAt      string   `json:"swept_at"`
	Summary      string   `json:"summary"`
	DueFired     int      `json:"due_fired"`
	DueIDs       []string `json:"due_ids,omitempty"`
	DueWisps     int      `json:"due_wisps"`
	Escalated    int      `json:"escalated"`
	EscalatedIDs []string `json:"escalated_ids,omitempty"`
	// EscalatedWisps is reported separately rather than folded into Escalated
	// for the reason DueWisps is: the two planes are different namespaces, and
	// a count that mixed them would make a wisp-plane escalation look like
	// work a human should go read.
	EscalatedWisps int      `json:"escalated_wisps"`
	DefersWoken    int      `json:"defers_woken"`
	DeferIDs       []string `json:"defer_ids,omitempty"`
	DeferWisps     int      `json:"defer_wisps"`
}

// Summary is the one line an external clock publishes. It names both sweeps
// because they run in one pass and a reader who sees only the due half cannot
// tell a quiet clock from a half-broken one.
func (r dueSweepReport) summaryLine() string {
	return fmt.Sprintf("%d due, %d escalated, %d defer(s) woken",
		r.DueFired, r.Escalated, r.DefersWoken)
}

// dueCmd groups the due-date commands. Today the group holds only `sweep`;
// it exists as a group rather than a top-level `bd due-sweep` because due
// dates are a small family (inspect, repair, sweep) rather than one verb.
var dueCmd = &cobra.Command{
	Use:   "due",
	Short: "Inspect and repair due dates",
	Long: `Commands for due dates.

  sweep  fire the beads whose due date has arrived (the external clock's seam)

See 'bd due sweep --help'.`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

var dueSweepCmd = &cobra.Command{
	Use:   "sweep",
	Short: "Fire every bead whose due date has arrived",
	Long: `Run the due-date sweep now and report what fired.

Beads fire lazily on ready-work reads, which is a latency FLOOR, not a clock:
a workspace nobody reads never fires anything. This command is the seam an
external clock stands on — a timer runs it on a fixed cadence, reads the
summary, and publishes it onto whatever rail consumes due beads.

Each bead whose due date has arrived records a 'due' audit event (the rail
'bd events' already carries), has its miss counted, and has its due date moved
forward by one grace interval, so it nags again rather than going silent. A
bead that reaches three misses has its priority raised once. Expired dated
defers wake in the same pass, because both are time-based sweeps and sharing
the transaction costs one round trip instead of two.

Unlike the lazy sweep behind a ready read, this one is NOT advisory: a sweep
that could not run exits non-zero, so a clock can tell a quiet workspace from
a broken one.

Examples:
  bd due sweep                         # fire what is due, print a summary
  bd due sweep --json                  # same, machine-readable`,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("due-sweep")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		// Proxied mode is refused rather than half-supported, exactly as
		// `bd due backfill` refuses it. The server runs this sweep itself on
		// every ready read it serves, so a proxied client asking for one is
		// asking the wrong process: the clock belongs beside the database,
		// where a failure is visible and a summary means something.
		if usesProxiedServer() {
			return HandleErrorRespectJSON("due sweep is not supported in proxied-server mode\n  The server sweeps on its own reads. Run the clock against the database directly (a local `bd` in the workspace, or on the server host).")
		}

		if store == nil {
			return HandleErrorWithHint("database not initialized", diagHint())
		}
		CheckReadonly("due sweep")

		sweeper, ok := findScheduledSweeper(store)
		if !ok {
			return HandleErrorRespectJSON("this storage backend cannot run a due sweep on demand")
		}

		swept, err := sweeper.RunScheduledSweeps(rootCtx)
		if err != nil {
			return HandleError("due sweep: %v", err)
		}

		report := dueSweepReport{
			SweptAt:        time.Now().UTC().Format(time.RFC3339),
			DueFired:       len(swept.Due.Issues),
			DueIDs:         swept.Due.Issues,
			DueWisps:       len(swept.Due.Wisps),
			Escalated:      len(swept.Due.Escalated),
			EscalatedIDs:   swept.Due.Escalated,
			EscalatedWisps: len(swept.Due.EscalatedWisps),
			DefersWoken:    len(swept.Defers.Issues),
			DeferIDs:       swept.Defers.Issues,
			DeferWisps:     len(swept.Defers.Wisps),
		}
		// The summary is a FIELD, not only a rendering, so an external clock
		// can publish the line with one grep instead of reassembling it from
		// counts — and so the line a human reads and the line a rail carries
		// are the same string.
		report.Summary = report.summaryLine()
		if jsonOutput {
			return outputJSON(report)
		}
		printDueSweepReport(report)
		return nil
	},
}

// findScheduledSweeper walks the decorator chain down to the store that can
// run a sweep. The store global is decorator-wrapped (telemetry, hook firing);
// Unwrap() is how every other capability reaches past them.
func findScheduledSweeper(s storage.DoltStorage) (scheduledSweeper, bool) {
	for s != nil {
		if sweeper, ok := s.(scheduledSweeper); ok {
			return sweeper, true
		}
		u, ok := s.(interface{ Unwrap() storage.DoltStorage })
		if !ok {
			return nil, false
		}
		s = u.Unwrap()
	}
	return nil, false
}

func printDueSweepReport(r dueSweepReport) {
	fmt.Printf("%s %s\n", ui.RenderAccent("*"), r.summaryLine())
	escalated := make(map[string]bool, len(r.EscalatedIDs))
	for _, id := range r.EscalatedIDs {
		escalated[id] = true
	}
	for _, id := range r.DueIDs {
		if escalated[id] {
			fmt.Printf("  due   %s %s\n", id, ui.RenderWarn("(escalated)"))
			continue
		}
		fmt.Printf("  due   %s\n", id)
	}
	for _, id := range r.DeferIDs {
		fmt.Printf("  woken %s\n", id)
	}
	if r.DueWisps > 0 || r.DeferWisps > 0 || r.EscalatedWisps > 0 {
		fmt.Printf("  (%d due, %d escalated, %d woken in the wisp plane)\n",
			r.DueWisps, r.EscalatedWisps, r.DeferWisps)
	}
}

func init() {
	dueCmd.AddCommand(dueSweepCmd)
	rootCmd.AddCommand(dueCmd)
}
