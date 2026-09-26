package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/notify"
	"github.com/steveyegge/beads/internal/ui"
)

var notifyCmd = &cobra.Command{
	Use:   "notify",
	Short: "Queue and deliver notifications for beads that came due",
	Long: `A durable, transport-agnostic outbox for beads that need someone told.

Firing a due date and DELIVERING it are different problems. 'bd due sweep'
answers "what came due" — but a report nobody reads is the same as no clock at
all, and a sweep that printed to a terminal nobody was watching has told no
one. This is the queue between the two.

Each fired bead becomes a durable row addressed to a SEAT — its assignee, or
"unassigned" when it has none, because unowned work coming due is the news
most worth keeping. Rows live in an append-only JSONL ledger under
.beads/notify/ and stay pending until acked, so a delivery that failed is
retried on the next drain rather than lost.

beads deliberately knows nothing about HOW a seat is reached. 'bd notify drain'
prints rows by default, or hands each one to a command of your choosing with
--exec; that command is where the transport lives — a chat webhook, a mailer,
a terminal multiplexer, whatever your site already runs. Routing policy belongs
outside the issue tracker.

An --exec command receives the row in the environment:

  BD_NOTIFY_SEQ    monotonic sequence number
  BD_NOTIFY_SEAT   the seat the row is addressed to
  BD_NOTIFY_ID     the bead id
  BD_NOTIFY_KIND   due | escalate | defer | manual
  BD_NOTIFY_TITLE  the bead's title
  BD_NOTIFY_JSON   the whole row as JSON

A row is acked only after its command exits zero, and a failing command stops
that seat's drain where it failed, so nothing is silently skipped.

Examples:
  bd notify pending                                   # what is waiting
  bd notify seats                                     # who has mail
  bd notify drain --all                               # print every seat's rows
  bd notify drain --seat alice --exec 'notify-send "$BD_NOTIFY_TITLE"'
  bd notify drain --all --no-ack                      # preview, leave pending
  bd notify send --seat alice --issue bd-a1b2         # enqueue by hand
  bd notify ack --seat alice --upto 42                # mark delivered`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

var notifyPendingCmd = &cobra.Command{
	Use:           "pending",
	Short:         "List undelivered notify rows",
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		seat, _ := cmd.Flags().GetString("seat")
		o, err := openNotifyOutbox()
		if err != nil {
			return err
		}
		pending, err := o.Pending(seat)
		if err != nil {
			return HandleError("%v", err)
		}
		if jsonOutput {
			return outputJSON(pending)
		}
		if len(pending) == 0 {
			fmt.Printf("%s no pending notifies\n", ui.RenderAccent("*"))
			return nil
		}
		for _, r := range pending {
			fmt.Printf("  %d  %-12s  %-8s  %s  %s\n", r.Seq, r.Seat, r.Kind, r.IssueID, r.Title)
		}
		fmt.Printf("%s %d pending\n", ui.RenderAccent("*"), len(pending))
		return nil
	},
}

var notifySeatsCmd = &cobra.Command{
	Use:           "seats",
	Short:         "List seats that have appeared in the outbox",
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		o, err := openNotifyOutbox()
		if err != nil {
			return err
		}
		seats, err := o.Seats()
		if err != nil {
			return HandleError("%v", err)
		}
		if jsonOutput {
			return outputJSON(seats)
		}
		for _, s := range seats {
			pending, _ := o.Pending(s)
			fmt.Printf("  %-16s  pending=%d\n", s, len(pending))
		}
		return nil
	},
}

var notifySendCmd = &cobra.Command{
	Use:           "send",
	Short:         "Enqueue a notify row by hand",
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		seat, _ := cmd.Flags().GetString("seat")
		issueID, _ := cmd.Flags().GetString("issue")
		if strings.TrimSpace(seat) == "" || strings.TrimSpace(issueID) == "" {
			return HandleError("notify send requires --seat and --issue")
		}
		o, err := openNotifyOutbox()
		if err != nil {
			return err
		}
		title := ""
		if store != nil {
			if issue, err := store.GetIssue(rootCtx, issueID); err == nil && issue != nil {
				title = issue.Title
			}
		}
		rec, err := o.Enqueue(seat, issueID, notify.KindManual, title)
		if err != nil {
			return HandleError("%v", err)
		}
		if jsonOutput {
			return outputJSON(rec)
		}
		fmt.Printf("%s queued %d for seat %s\n", ui.RenderAccent("*"), rec.Seq, rec.Seat)
		return nil
	},
}

// notifySeatResult is one seat's drain outcome. It is the JSON shape a caller
// scripts against, so a partially-drained seat reports what it managed and why
// it stopped rather than only failing.
type notifySeatResult struct {
	Seat      string `json:"seat"`
	Delivered int    `json:"delivered"`
	AckedThru int64  `json:"acked_through,omitempty"`
	Error     string `json:"error,omitempty"`
}

var notifyDrainCmd = &cobra.Command{
	Use:   "drain",
	Short: "Deliver pending rows for a seat (print, or --exec a transport)",
	Long: `Deliver a seat's pending rows, oldest first.

With no --exec, rows are PRINTED: beads has no opinion about how a seat is
reached, and printing is the transport every environment already has. Point
--exec at whatever your site runs to make delivery real.

Rows are acked as they succeed, so a drain interrupted halfway leaves the rest
pending for the next run instead of losing them. A failing --exec command stops
that seat's drain at the failure — later rows stay pending rather than being
skipped past a problem nobody saw.

Examples:
  bd notify drain --all
  bd notify drain --seat alice --exec 'notify-send "$BD_NOTIFY_TITLE"'
  bd notify drain --all --limit 20
  bd notify drain --all --no-ack        # preview without consuming`,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		seat, _ := cmd.Flags().GetString("seat")
		all, _ := cmd.Flags().GetBool("all")
		execCmd, _ := cmd.Flags().GetString("exec")
		noAck, _ := cmd.Flags().GetBool("no-ack")
		limit, _ := cmd.Flags().GetInt("limit")
		if !all && strings.TrimSpace(seat) == "" {
			return HandleError("notify drain requires --seat or --all")
		}

		o, err := openNotifyOutbox()
		if err != nil {
			return err
		}
		seats := []string{seat}
		if all {
			seats, err = o.Seats()
			if err != nil {
				return HandleError("%v", err)
			}
		}

		var results []notifySeatResult
		for _, s := range seats {
			pending, err := o.Pending(s)
			if err != nil {
				return HandleError("%v", err)
			}
			if limit > 0 && len(pending) > limit {
				pending = pending[:limit]
			}
			if len(pending) == 0 {
				continue
			}

			result := notifySeatResult{Seat: s}
			var lastOK int64
			for _, rec := range pending {
				if execCmd == "" {
					if !jsonOutput {
						fmt.Printf("  %d  %-8s  %s  %s\n", rec.Seq, rec.Kind, rec.IssueID, rec.Title)
					}
				} else if err := runNotifyExec(execCmd, rec); err != nil {
					// Stop this seat HERE rather than continuing: the rows are
					// ordered, and delivering later ones past a failure would
					// ack around a row nobody ever saw.
					result.Error = err.Error()
					if !jsonOutput {
						fmt.Fprintf(os.Stderr, "notify drain: seq %d %s: %v\n", rec.Seq, rec.IssueID, err)
					}
					break
				}
				result.Delivered++
				lastOK = rec.Seq
			}

			if !noAck && lastOK > 0 {
				if err := o.AckSeat(s, lastOK); err != nil {
					return HandleError("ack: %v", err)
				}
				result.AckedThru = lastOK
			}
			results = append(results, result)
			if !jsonOutput && result.Delivered > 0 {
				fmt.Printf("%s seat %s: delivered %d (acked through %d)\n",
					ui.RenderAccent("*"), s, result.Delivered, lastOK)
			}
		}

		if jsonOutput {
			return outputJSON(results)
		}
		if len(results) == 0 {
			fmt.Printf("%s nothing pending\n", ui.RenderAccent("*"))
		}
		return nil
	},
}

var notifyAckCmd = &cobra.Command{
	Use:           "ack",
	Short:         "Mark notify rows delivered for a seat up to a seq",
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		seat, _ := cmd.Flags().GetString("seat")
		upto, _ := cmd.Flags().GetInt64("upto")
		if strings.TrimSpace(seat) == "" || upto <= 0 {
			return HandleError("notify ack requires --seat and --upto")
		}
		o, err := openNotifyOutbox()
		if err != nil {
			return err
		}
		if err := o.AckSeat(seat, upto); err != nil {
			return HandleError("%v", err)
		}
		if !jsonOutput {
			fmt.Printf("%s seat %s acked through %d\n", ui.RenderAccent("*"), seat, upto)
		}
		return nil
	},
}

// runNotifyExec hands one row to the operator's transport command. The row
// reaches it through the environment rather than argv so a title carrying
// shell metacharacters cannot become part of the command.
func runNotifyExec(command string, rec notify.Record) error {
	payload, _ := json.Marshal(rec)
	// #nosec G204 -- the command is the operator's own --exec string; the row
	// is passed in the environment, never interpolated into it.
	cmd := exec.Command("sh", "-c", command)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("BD_NOTIFY_SEQ=%d", rec.Seq),
		"BD_NOTIFY_SEAT="+rec.Seat,
		"BD_NOTIFY_ID="+rec.IssueID,
		"BD_NOTIFY_KIND="+string(rec.Kind),
		"BD_NOTIFY_TITLE="+rec.Title,
		"BD_NOTIFY_JSON="+string(payload),
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func openNotifyOutbox() (*notify.Outbox, error) {
	dir := beads.FindBeadsDir()
	if dir == "" {
		return nil, HandleErrorWithHint("no .beads directory found", "run from a beads workspace")
	}
	o, err := notify.Open(dir)
	if err != nil {
		return nil, HandleError("%v", err)
	}
	return o, nil
}

// enqueueDueSweepNotifies turns a sweep report into outbox rows, one per bead
// that fired, addressed to its assignee.
//
// Failure is reported and swallowed: the sweep's job is timekeeping, and a
// clock that exits non-zero because delivery is misconfigured would make an
// operator think no beads came due. The rows it could not write are simply not
// delivered; the sweep's own report still names every id.
func enqueueDueSweepNotifies(report dueSweepReport) {
	if len(report.DueIDs) == 0 {
		return
	}
	dir := beads.FindBeadsDir()
	if dir == "" {
		return
	}
	o, err := notify.Open(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "notify: open outbox: %v\n", err)
		return
	}
	escalated := make(map[string]bool, len(report.EscalatedIDs))
	for _, id := range report.EscalatedIDs {
		escalated[id] = true
	}
	var queued int
	for _, id := range report.DueIDs {
		kind := notify.KindDue
		if escalated[id] {
			kind = notify.KindEscalate
		}
		seat := notify.UnassignedSeat
		title := ""
		if store != nil {
			if issue, err := store.GetIssue(rootCtx, id); err == nil && issue != nil {
				title = issue.Title
				if assignee := strings.TrimSpace(issue.Assignee); assignee != "" {
					seat = assignee
				}
			}
		}
		if _, err := o.Enqueue(seat, id, kind, title); err != nil {
			fmt.Fprintf(os.Stderr, "notify: enqueue %s: %v\n", id, err)
			continue
		}
		queued++
	}
	if queued > 0 && !jsonOutput {
		fmt.Printf("  notify queued %d\n", queued)
	}
}

func init() {
	notifyPendingCmd.Flags().String("seat", "", "Only this seat's rows")
	notifySeatsCmd.Flags().String("seat", "", "")
	_ = notifySeatsCmd.Flags().MarkHidden("seat")
	notifySendCmd.Flags().String("seat", "", "Seat to address the row to")
	notifySendCmd.Flags().String("issue", "", "Bead the row is about")
	notifyDrainCmd.Flags().String("seat", "", "Seat to drain")
	notifyDrainCmd.Flags().Bool("all", false, "Drain every seat with pending rows")
	notifyDrainCmd.Flags().String("exec", "", "Run this command per row; the row arrives in BD_NOTIFY_* environment variables")
	notifyDrainCmd.Flags().Bool("no-ack", false, "Deliver without acking, so the rows stay pending")
	notifyDrainCmd.Flags().Int("limit", 0, "Deliver at most this many rows per seat (0 = no limit)")
	notifyAckCmd.Flags().String("seat", "", "Seat to ack")
	notifyAckCmd.Flags().Int64("upto", 0, "Ack rows up to and including this seq")

	notifyCmd.AddCommand(notifyPendingCmd, notifySeatsCmd, notifySendCmd, notifyDrainCmd, notifyAckCmd)
	rootCmd.AddCommand(notifyCmd)
}
