package main

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"sort"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/timeparsing"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/workapi"
)

// defaultBackfillInterval is how far past its own creation a backfilled bead
// is dated. Seven days is a working week: long enough that a backfill does not
// declare a repository's entire history overdue on the day it runs, short
// enough that the dates mean something.
const defaultBackfillInterval = 7 * 24 * time.Hour

// backfillReportSample caps how many rows the human-readable report prints in
// full. The counts above it are exact; this only bounds the scroll.
const backfillReportSample = 20

// dueBackfillRow is one bead the backfill would change.
type dueBackfillRow struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	IssueType   string    `json:"issue_type"`
	Priority    int       `json:"priority"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	ProposedDue time.Time `json:"proposed_due_at"`
	// Floored marks a date that created_at + interval would have put in the
	// past, so it was raised to now + interval instead.
	Floored bool `json:"floored,omitempty"`
}

// dueBackfillReport is the whole dry run: what would change, what was skipped
// and why, and nothing else.
type dueBackfillReport struct {
	Applied       bool             `json:"applied"`
	Interval      string           `json:"interval"`
	Scanned       int              `json:"scanned"`
	Candidates    int              `json:"candidates"`
	Floored       int              `json:"floored"`
	ByType        map[string]int   `json:"by_type"`
	ByStatus      map[string]int   `json:"by_status"`
	OldestDue     *time.Time       `json:"oldest_proposed_due_at,omitempty"`
	NewestDue     *time.Time       `json:"newest_proposed_due_at,omitempty"`
	AlreadyDated  int              `json:"already_dated"`
	SkippedClosed int              `json:"skipped_closed"`
	SkippedExempt map[string]int   `json:"skipped_exempt"`
	Rows          []dueBackfillRow `json:"rows"`
	Updated       int              `json:"updated"`
	Failed        []string         `json:"failed,omitempty"`
}

var dueCmd = &cobra.Command{
	Use:   "due",
	Short: "Inspect and repair due dates",
	Long: `Commands for due dates.

  backfill  give legacy beads that predate the due-date invariant a due date

See 'bd due backfill --help'.`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

var dueBackfillCmd = &cobra.Command{
	Use:   "backfill",
	Short: "Report (or apply) due dates for beads that have none",
	Long: `Give every open bead that predates the due-date invariant a due date.

REPORTS BY DEFAULT, WRITES ONLY WITH --apply. Dating a repository's whole
history is a judgement call about other people's work, so the default run
changes nothing: it prints what it would do and stops. Read the report, then
re-run with --apply if it looks right.

Each candidate is dated --interval past its OWN creation (default +7d), not
past today, so the relative order of a backlog survives the backfill. A bead
older than the interval would land in the past and fire on the next ready
read, so its date is instead spread across the window starting --interval past
now — placed by the bead's own id, so the report and --apply agree, and a whole
legacy backlog does not come due in one herd; the report marks those rows.

Beads that are not work awaiting completion are skipped: events, wisps,
templates, federated rows, and anything already closed. So is anything that
already has a due date - a backfill never overwrites one. The report counts
every skipped class so nothing is excluded silently.

Examples:
  bd due backfill                      # dry run: report only, writes nothing
  bd due backfill --json               # same report, machine-readable
  bd due backfill --interval=+14d      # propose two weeks instead of one
  bd due backfill --apply              # write the dates the report described`,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("due-backfill")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		interval := defaultBackfillInterval
		if raw, _ := cmd.Flags().GetString("interval"); raw != "" {
			parsed, err := parseBackfillInterval(raw)
			if err != nil {
				return HandleError("%v", err)
			}
			interval = parsed
		}
		apply, _ := cmd.Flags().GetBool("apply")

		// Proxied mode is refused rather than half-supported. The backfill is a
		// whole-database audit: it reads every bead and writes a date onto each
		// candidate, and doing that one HTTP round trip at a time is a different
		// operation with different failure modes than the local one this
		// command's report describes. A one-shot maintenance pass belongs
		// against the database itself.
		if usesProxiedServer() {
			return HandleErrorRespectJSON("due backfill is not supported in proxied-server mode\n  Run it against the database directly (a local `bd` in the workspace, or on the server host).")
		}

		if store == nil {
			return HandleErrorWithHint("database not initialized", diagHint())
		}
		if apply {
			CheckReadonly("due backfill")
		}

		ctx := rootCtx
		issues, err := store.SearchIssues(ctx, "", workapi.BuildDueBackfillFilter())
		if err != nil {
			return HandleError("scanning issues: %v", err)
		}

		report := planDueBackfill(issues, interval, time.Now().UTC())
		if apply {
			applyDueBackfill(ctx, &report)
		}
		if jsonOutput {
			return printJSON(report)
		}
		printDueBackfillReport(report)
		return nil
	},
}

// spreadFlooredDue places a bead whose own creation date would put it in the
// past somewhere inside the interval window that starts at floor, rather than
// exactly on floor.
//
// Dating each bead from its OWN created_at is what keeps a backlog's relative
// order through a backfill — but every bead older than the interval collapses
// onto the same floor, and in a repository being backfilled for the first time
// that is most of them. Stacking a whole legacy backlog on one instant means
// the clock fires thousands of beads in a single sweep: thousands of events in
// one transaction, one summary line that says only "2000 due", and an
// escalation signal that has told nobody anything. Spreading them turns that
// cliff into a slope.
//
// The offset is DERIVED FROM THE ID, not drawn at random, because the report a
// human reads and the dates `--apply` writes must be the same dates. A random
// offset would make the dry run a description of a different backfill than the
// one that eventually runs, which is the one thing the report exists to
// prevent.
func spreadFlooredDue(id string, floor time.Time, interval time.Duration) time.Time {
	if interval <= 0 {
		return floor
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(id))
	return floor.Add(time.Duration(h.Sum64() % uint64(interval))).UTC()
}

// parseBackfillInterval reads the offset a proposed due date sits past its
// bead's creation. It takes the compact-duration spelling the rest of the CLI
// uses (+7d, +2w, +12h) and nothing else: a cron expression is a SCHEDULE, and
// has no answer to "how far past created_at", so it is refused here rather
// than silently reinterpreted.
//
// Calendar units are resolved against a fixed instant, so +1m is that month's
// length rather than a floating one. For an interval this coarse that is
// close enough, and a caller who cares can say +30d.
func parseBackfillInterval(raw string) (time.Duration, error) {
	base := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	end, err := timeparsing.ParseCompactDuration(raw, base)
	if err != nil {
		return 0, fmt.Errorf("invalid --interval %q: expected an offset like +7d, +2w or +12h", raw)
	}
	d := end.Sub(base)
	if d <= 0 {
		return 0, fmt.Errorf("invalid --interval %q: must be a positive offset", raw)
	}
	return d, nil
}

// planDueBackfill is the dry run: it decides what WOULD change and summarizes
// it, touching nothing. Keeping the decision here — pure, over a slice — is
// what lets the report and the write agree by construction: --apply runs this
// same plan and then writes exactly its rows.
func planDueBackfill(issues []*types.Issue, interval time.Duration, now time.Time) dueBackfillReport {
	report := dueBackfillReport{
		Interval:      interval.String(),
		Scanned:       len(issues),
		ByType:        map[string]int{},
		ByStatus:      map[string]int{},
		SkippedExempt: map[string]int{},
	}
	floor := now.Add(interval).UTC()
	for _, issue := range issues {
		if issue == nil {
			continue
		}
		if issue.DueAt != nil {
			report.AlreadyDated++
			continue
		}
		if issue.Status == types.StatusClosed {
			report.SkippedClosed++
			continue
		}
		if reason := issueops.DueRequiredExemptReason(issue); reason != "" {
			report.SkippedExempt[reason]++
			continue
		}
		due := issue.CreatedAt.Add(interval).UTC()
		floored := !due.After(now)
		if floored {
			due = spreadFlooredDue(issue.ID, floor, interval)
			report.Floored++
		}
		report.Candidates++
		report.ByType[string(issue.IssueType.Normalize())]++
		report.ByStatus[string(issue.Status)]++
		if report.OldestDue == nil || due.Before(*report.OldestDue) {
			d := due
			report.OldestDue = &d
		}
		if report.NewestDue == nil || due.After(*report.NewestDue) {
			d := due
			report.NewestDue = &d
		}
		report.Rows = append(report.Rows, dueBackfillRow{
			ID:          issue.ID,
			Title:       issue.Title,
			IssueType:   string(issue.IssueType.Normalize()),
			Priority:    issue.Priority,
			Status:      string(issue.Status),
			CreatedAt:   issue.CreatedAt.UTC(),
			ProposedDue: due,
			Floored:     floored,
		})
	}
	// Oldest first: a reviewer reading the report wants the most overdue end of
	// the backlog, which is where a wrong interval shows up first.
	sort.Slice(report.Rows, func(i, j int) bool {
		if report.Rows[i].CreatedAt.Equal(report.Rows[j].CreatedAt) {
			return report.Rows[i].ID < report.Rows[j].ID
		}
		return report.Rows[i].CreatedAt.Before(report.Rows[j].CreatedAt)
	})
	return report
}

// applyDueBackfill writes the plan. One update per bead rather than one bulk
// statement: the store interface is what every backend shares, and a partial
// failure then reports exactly which beads it could not date instead of
// rolling the whole backlog back.
//
// ponytail: a repository with a six-figure backlog will want a bulk path;
// until one exists, the report tells you the size before you commit to it.
func applyDueBackfill(ctx context.Context, report *dueBackfillReport) {
	report.Applied = true
	for _, row := range report.Rows {
		updates := map[string]interface{}{
			"due_at": row.ProposedDue,
		}
		if err := store.UpdateIssue(ctx, row.ID, updates, actor); err != nil {
			report.Failed = append(report.Failed, fmt.Sprintf("%s: %v", row.ID, err))
			continue
		}
		report.Updated++
	}
}

func printDueBackfillReport(report dueBackfillReport) {
	if report.Applied {
		fmt.Printf("%s Backfilled %d due date(s)\n", ui.RenderAccent("*"), report.Updated)
		for _, failure := range report.Failed {
			fmt.Fprintf(os.Stderr, "%s %s\n", ui.RenderWarn("!"), failure)
		}
		return
	}

	fmt.Printf("%s DRY RUN - nothing was written\n\n", ui.RenderWarn("!"))
	fmt.Printf("  scanned           %d\n", report.Scanned)
	fmt.Printf("  already dated     %d\n", report.AlreadyDated)
	fmt.Printf("  skipped closed    %d  (closed work is never dated)\n", report.SkippedClosed)
	fmt.Printf("  skipped exempt    %d  (%s)\n", sumCountMap(report.SkippedExempt), formatCountMap(report.SkippedExempt))
	fmt.Printf("  would backfill    %d\n", report.Candidates)
	if report.Candidates == 0 {
		fmt.Printf("\nEvery bead that needs a due date already has one.\n")
		return
	}
	fmt.Printf("  interval          %s past each bead's own created_at\n", report.Interval)
	fmt.Printf("  floored           %d  (older than the interval; dated %s past now instead)\n", report.Floored, report.Interval)
	if report.OldestDue != nil && report.NewestDue != nil {
		fmt.Printf("  proposed range    %s .. %s\n",
			report.OldestDue.Format("2006-01-02"), report.NewestDue.Format("2006-01-02"))
	}

	fmt.Printf("\n  by type:   %s\n", formatCountMap(report.ByType))
	fmt.Printf("  by status: %s\n", formatCountMap(report.ByStatus))

	shown := report.Rows
	if len(shown) > backfillReportSample {
		shown = shown[:backfillReportSample]
	}
	fmt.Printf("\n  oldest %d of %d:\n", len(shown), len(report.Rows))
	for _, row := range shown {
		mark := "  "
		if row.Floored {
			mark = " *"
		}
		fmt.Printf("    %-14s  created %s  ->  due %s%s  %s\n",
			row.ID, row.CreatedAt.Format("2006-01-02"), row.ProposedDue.Format("2006-01-02"), mark,
			truncateForReport(row.Title, 48))
	}
	if report.Floored > 0 {
		fmt.Printf("    * floored to now + interval\n")
	}
	if len(report.Rows) > len(shown) {
		fmt.Printf("    ... and %d more (use --json for the full list)\n", len(report.Rows)-len(shown))
	}
	fmt.Printf("\nRe-run with --apply to write these dates.\n")
}

// formatCountMap renders a count map in a stable order, so two runs of the same
// report are diffable.
func formatCountMap(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := ""
	for i, key := range keys {
		if i > 0 {
			out += ", "
		}
		out += fmt.Sprintf("%s=%d", key, counts[key])
	}
	if out == "" {
		return "(none)"
	}
	return out
}

func sumCountMap(counts map[string]int) int {
	total := 0
	for _, n := range counts {
		total += n
	}
	return total
}

func truncateForReport(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-1]) + "\u2026"
}

func printJSON(v any) error {
	encoded, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return HandleError("encoding report: %v", err)
	}
	fmt.Println(string(encoded))
	return nil
}

func init() {
	dueBackfillCmd.Flags().Bool("apply", false, "Write the due dates. Without this the command only reports.")
	dueBackfillCmd.Flags().String("interval", "+7d", "How far past each bead's created_at the proposed due date lands")
	dueCmd.AddCommand(dueBackfillCmd)
	rootCmd.AddCommand(dueCmd)
}
