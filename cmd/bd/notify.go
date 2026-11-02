package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/notify"
	seatregistry "github.com/steveyegge/beads/internal/seats"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"path/filepath"
)

// notifyCmd is the beads-owned delivery surface. Time (due sweep) enqueues;
// drain delivers only to an exact pin in .beads/notify/seats.json.
var notifyCmd = &cobra.Command{
	Use:   "notify",
	Short: "Seat-addressed notify outbox (beads-owned delivery)",
	Long: `The notify outbox is how beads delivers due/repeat fires and comment
pings to assignee seats.

bd due sweep enqueues one row per fired bead under its assignee seat
(.beads/notify/). bd comment and bd comments add enqueue a comment ping
for the assignee (skipping self-comments and unassigned beads).
bd notify drain delivers that seat only to the exact herdr
target recorded in .beads/notify/seats.json (session + pane id, or agent
session id). Delivery uses the pinned pane's harness follow-up submit
when verified (so a due fire does not steer mid-turn), holds until idle
for unverified mid-turn harnesses, and uses Enter/steer otherwise.
There is no fuzzy search: a missing pin, dead pinned target, or blocked
approval dialog leaves the row queued and marks the seat for parent
escalation.

bd notify resolve is a human diagnostic for the pin itself: it prints the
recorded target and whether that exact target is live. There is no scored
or fuzzy match anywhere in beads, because a lookalike that reads like a
target is how a wake once landed on the wrong pane.

Examples:
  bd notify pending
  bd notify resolve --seat wiseman
  bd notify drain --seat wiseman
  bd notify drain                 # current actor only (--actor / BEADS_ACTOR)
  bd notify drain --all
  bd notify drain --seat wiseman --print
  bd notify drain --seat wiseman --exec 'echo "$BD_NOTIFY_ID"'
  bd notify seats
  bd notify holds
  bd notify holds reap --dry-run
  bd notify holds reap --yes`,
}

var notifyPendingCmd = &cobra.Command{
	Use:   "pending",
	Short: "List undelivered notify rows",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		seat, _ := cmd.Flags().GetString("seat")
		if strings.TrimSpace(seat) == "" {
			seat = currentNotifyActor()
			if isUnassignedNotifySeat(seat) {
				if jsonOutput {
					return printJSON([]notify.Record{})
				}
				fmt.Printf("%s no pending notifies\n", ui.RenderAccent("*"))
				return nil
			}
		}
		o, err := openNotifyOutbox()
		if err != nil {
			return err
		}
		if strings.TrimSpace(seat) != "" {
			seat, err = o.ResolveSeat(seat)
			if err != nil {
				return err
			}
		}
		pending, err := o.Pending(seat)
		if err != nil {
			return HandleError("%v", err)
		}
		pending, err = liveNotifyPending(o, seat, pending, false)
		if err != nil {
			return HandleError("%v", err)
		}
		if jsonOutput {
			if pending == nil {
				pending = []notify.Record{}
			}
			return printJSON(pending)
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
	Use:   "seats",
	Short: "List seats that have appeared in the outbox",
	Args:  cobra.NoArgs,
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
			return printJSON(seats)
		}
		for _, s := range seats {
			pending, _ := o.Pending(s)
			fmt.Printf("  %-16s  pending=%d\n", s, len(pending))
		}
		return nil
	},
}

var notifyResolveCmd = &cobra.Command{
	Use:   "resolve",
	Short: "Human diagnostic: show a seat's exact pin and whether it is live",
	Long: `Show the authoritative pin for a seat (from .beads/notify/seats.json)
and whether that exact target is live in herdr.

Delivery is exact: pinned target live -> deliver there; no pin or dead pin
-> hold and escalate. Nothing here is scored or guessed. A moved pane is
rewritten at drain time from the live agent session, and a restarted seat
rebinds itself the next time it runs bd. No external timer is required.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		seat, _ := cmd.Flags().GetString("seat")
		if strings.TrimSpace(seat) == "" {
			return HandleError("notify resolve requires --seat")
		}
		var pin notify.Pin
		var pinned bool
		var pinLive bool
		var pinInst notify.Instance
		o, err := openNotifyOutbox()
		if err == nil {
			pins, perr := o.LoadPins()
			if perr != nil {
				return HandleError("pins: %v", perr)
			}
			seat, perr = o.ResolveSeat(seat)
			if perr != nil {
				return perr
			}
			pin, pinned = pins[seat]
			if pinned && !pin.Valid() {
				pinned = false
			}
		}
		d := notify.HerdrDiscoverer{}
		instances, err := d.ListAllInstances()
		if err != nil {
			return HandleError("herdr discover: %v", err)
		}
		if pinned {
			pinInst, pinLive = notify.FindPinnedInstance(pin, instances)
		}
		if jsonOutput {
			return printJSON(map[string]any{
				"seat":              seat,
				"pin":               pinOrNil(pinned, pin),
				"pin_live":          pinLive,
				"authoritative":     pinned,
				"instances_scanned": len(instances),
			})
		}
		if pinned {
			live := "not live - drain will hold and escalate"
			if pinLive {
				live = fmt.Sprintf("live %s %s harness=%s status=%s", pinInst.Session, pinInst.PaneID, pinInst.Harness, pinInst.Status)
			}
			fmt.Printf("%s seat %s pin: %s %s (%s)\n",
				ui.RenderAccent("*"), seat, pin.Session, pin.PaneID, live)
		} else {
			fmt.Printf("%s seat %s: no pin in .beads/notify/seats.json (drain will hold and escalate)\n",
				ui.RenderWarn("!"), seat)
		}
		return nil
	},
}

func pinOrNil(ok bool, pin notify.Pin) any {
	if !ok {
		return nil
	}
	return pin
}

var notifyDrainCmd = &cobra.Command{
	Use:   "drain",
	Short: "Deliver pending rows for a seat via herdr (or --exec)",
	Long: `Drain pending notify rows for one seat, the current actor, or --all assigned seats.

Default transport is herdr, exact-pin only:
  1. Load .beads/notify/seats.json (seat → session+pane_id or agent_session_id)
  2. Discover live herdr agents
  3. If the pinned target is live, deliver with that pane's harness mode:
     follow_up (pi Option+Enter, cursor/codex Tab), hold-until-idle for
     unverified mid-turn harnesses, or steer (Enter) when idle
  4. Ack on success
  Missing pin, dead pin, or blocked dialog: leave rows queued, mark escalate, never pick a lookalike.

Harness-agnostic: herdr already knows pi/claude/codex/… in each pane.
Drain picks follow-up vs steer from that harness; it does not import firstmate.

Flags:
  --print   print rows only (no herdr, still acks unless --no-ack)
  --exec    override transport with a shell command (BD_NOTIFY_* env)
  --no-ack  leave rows pending after delivery attempt
  --all     drain every assigned seat that has pending rows
Unassigned beads are never notified. Without --seat/--all, only the current
actor (--actor / BEADS_ACTOR) is drained so BEADS NOTIFY is not broadcast.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		seat, _ := cmd.Flags().GetString("seat")
		all, _ := cmd.Flags().GetBool("all")
		limit, _ := cmd.Flags().GetInt("limit")
		execCmd, _ := cmd.Flags().GetString("exec")
		printOnly, _ := cmd.Flags().GetBool("print")
		noAck, _ := cmd.Flags().GetBool("no-ack")

		explicitSeat := strings.TrimSpace(seat) != ""
		actor := currentNotifyActor()
		if !all && !explicitSeat {
			if isUnassignedNotifySeat(actor) {
				return HandleError("notify drain requires --seat, --all, or a current actor (--actor / BEADS_ACTOR)")
			}
			seat = actor
			explicitSeat = true
		}

		o, err := openNotifyOutbox()
		if err != nil {
			return err
		}

		if !all {
			seat, err = o.ResolveSeat(seat)
			if err != nil {
				return err
			}
		}
		releaseDrain, err := seatregistry.LockFile(filepath.Join(o.Dir(), "drain.lock"))
		if err != nil {
			return err
		}
		defer releaseDrain()
		var seats []string
		if all {
			seats, err = o.Seats()
			if err != nil {
				return HandleError("%v", err)
			}
		} else {
			seats = []string{seat}
		}

		// Discover once per drain invocation when using herdr.
		var (
			instances []notify.Instance
			pins      map[string]notify.Pin
			discErr   error
			discover  notify.HerdrDiscoverer
		)
		needHerdr := execCmd == "" && !printOnly
		if needHerdr {
			instances, discErr = discover.ListAllInstances()
			if discErr != nil {
				healSeatRuntime(o, nil, false)
				return HandleError("herdr discover: %v", discErr)
			}
			healSeatRuntime(o, instances, true)
			var perr error
			pins, perr = o.LoadPins()
			if perr != nil {
				return HandleError("pins: %v", perr)
			}
		}

		type seatResult struct {
			Seat           string              `json:"seat"`
			Delivered      int                 `json:"delivered"`
			AckedThru      int64               `json:"acked_through,omitempty"`
			Instance       *notify.Instance    `json:"instance,omitempty"`
			DeliveryMode   notify.DeliveryMode `json:"delivery_mode,omitempty"`
			Error          string              `json:"error,omitempty"`
			Escalate       bool                `json:"escalate,omitempty"`
			HoldReason     string              `json:"hold_reason,omitempty"`
			Classification int                 `json:"classification,omitempty"`
		}
		var results []seatResult

		for _, s := range seats {
			if !shouldDeliverNotifySeat(s, actor, all, explicitSeat && !all) {
				continue
			}
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
			live, err := liveNotifyPending(o, s, pending, !noAck)
			if err != nil {
				return HandleError("%v", err)
			}
			if len(live) == 0 {
				continue
			}

			res := seatResult{Seat: s}
			var inst notify.Instance
			if needHerdr {
				dec := notify.DecideDelivery(s, pins, instances)
				if !dec.OK {
					res.Error = holdError(dec)
					res.Escalate = dec.Escalate
					res.HoldReason = dec.Reason
					res.Classification = dec.Classification
					if dec.Escalate {
						if err := o.SetHold(s, notify.Hold{
							Reason:         dec.Reason,
							Session:        dec.Pin.Session,
							PaneID:         dec.Pin.PaneID,
							AgentSessionID: dec.Pin.AgentSessionID,
							Pending:        len(live),
						}); err != nil {
							return HandleError("hold: %v", err)
						}
					}
					results = append(results, res)
					if !jsonOutput {
						if dec.Escalate {
							fmt.Printf("%s seat %s: %s (%d pending left); escalate\n",
								ui.RenderWarn("!"), s, res.Error, len(live))
						} else {
							fmt.Printf("%s seat %s: %s (%d pending left); holding until idle\n",
								ui.RenderAccent("*"), s, res.Error, len(live))
						}
					}
					continue
				}
				inst = dec.Instance
				res.Instance = &inst
				res.DeliveryMode = dec.Mode
				res.Classification = dec.Classification
			}

			var lastOK int64
			var delivered int
			for _, rec := range live {
				switch {
				case execCmd != "":
					if err := runNotifyExec(execCmd, rec); err != nil {
						res.Error = err.Error()
						res.Classification = notify.ExitFailed
						if exitErr, ok := err.(*exec.ExitError); ok {
							res.Classification = exitErr.ExitCode()
						}
						if !jsonOutput {
							fmt.Fprintf(os.Stderr, "notify drain: seq %d %s: %v\n", rec.Seq, rec.IssueID, err)
						}
						goto finishSeat
					}
				case printOnly:
					if !jsonOutput {
						fmt.Printf("  %d  %s  %s  %s\n", rec.Seq, rec.Kind, rec.IssueID, rec.Title)
					}
				default:
					prompt := notify.DefaultPrompt(rec)
					// Enrich prompt with bead body when possible (best-effort).
					if body := beadNotifyBody(rec.IssueID); body != "" {
						prompt = prompt + "\n\n" + body
					}
					mode := res.DeliveryMode
					if mode == "" {
						mode = notify.ModeForHarness(inst.Harness)
						res.DeliveryMode = mode
					}
					if err := discover.Deliver(inst, prompt, mode); err != nil {
						res.Error = err.Error()
						res.Classification = notify.ExitFailed
						if !jsonOutput {
							fmt.Fprintf(os.Stderr, "notify drain: seq %d herdr %s %s: %v\n", rec.Seq, inst.Session, inst.PaneID, err)
						}
						goto finishSeat
					}
					if !jsonOutput {
						fmt.Printf("  %d  → %s %s (%s/%s) %s\n", rec.Seq, inst.Session, inst.PaneID, inst.Harness, mode, rec.IssueID)
					}
				}
				delivered++
				lastOK = rec.Seq
			}
			lastOK = pending[len(pending)-1].Seq
		finishSeat:
			res.Delivered = delivered
			if !noAck && lastOK > 0 {
				if err := o.AckSeat(s, lastOK); err != nil {
					return HandleError("ack: %v", err)
				}
				res.AckedThru = lastOK
			}
			if needHerdr && res.Error == "" && delivered > 0 {
				if err := o.ClearHold(s); err != nil {
					return HandleError("clear hold: %v", err)
				}
			}
			results = append(results, res)
			if !jsonOutput {
				if res.Error != "" && delivered == 0 {
					// already printed
				} else if res.AckedThru > 0 {
					fmt.Printf("%s seat %s: delivered %d (acked through %d)\n",
						ui.RenderAccent("*"), s, delivered, res.AckedThru)
				} else {
					// ponytail: never claim an ack that did not happen; a summary
					// line that says "acked" under --no-ack reads as data loss.
					fmt.Printf("%s seat %s: delivered %d (not acked: --no-ack)\n",
						ui.RenderAccent("*"), s, delivered)
				}
			}
		}

		if jsonOutput {
			if err := printJSON(results); err != nil {
				return err
			}
		} else if len(results) == 0 {
			fmt.Printf("%s nothing pending\n", ui.RenderAccent("*"))
		}
		if explicitSeat && !all && len(results) == 1 {
			res := results[0]
			if res.Escalate {
				code := res.Classification
				if code == 0 {
					code = 1
				}
				return &exitError{Code: code}
			}
			if res.Error != "" && res.Classification != notify.ExitDeferred {
				code := res.Classification
				if code == 0 || code == notify.ExitDelivered {
					code = notify.ExitFailed
				}
				return &exitError{Code: code}
			}
		}
		return nil
	},
}

var notifyAckCmd = &cobra.Command{
	Use:   "ack",
	Short: "Mark notify rows delivered for a seat up to a seq",
	Args:  cobra.NoArgs,
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
		fmt.Printf("%s acked seat %s through seq %d\n", ui.RenderAccent("*"), seat, upto)
		return nil
	},
}

var notifyHoldsCmd = &cobra.Command{
	Use:   "holds",
	Short: "List or reap parent-escalation holds",
	Long: `Holds mark seats whose drain could not deliver (no-pin, pinned-target-dead,
target-blocked). Detection already works; retention reaps dead-pin and no-pin
holds older than the retention window (default 14d) and acks their pending
outbox rows. Transient holds (hold-until-idle, target-blocked) are never
reaped.

Examples:
  bd notify holds
  bd notify holds --json
  bd notify holds reap --dry-run
  bd notify holds reap --older-than 14d
  bd notify holds reap --older-than 7d --yes`,
}

var notifyHoldsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List current holds (default when no subcommand)",
	Args:  cobra.NoArgs,
	RunE:  runNotifyHoldsList,
}

var notifyHoldsReapCmd = &cobra.Command{
	Use:   "reap",
	Short: "Reap stale dead-pin / no-pin holds past retention",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		older, _ := cmd.Flags().GetString("older-than")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		yes, _ := cmd.Flags().GetBool("yes")
		maxAge, err := parseHoldRetention(older)
		if err != nil {
			return HandleError("%v", err)
		}
		if !dryRun && !yes {
			return HandleError("notify holds reap requires --dry-run or --yes")
		}
		o, err := openNotifyOutbox()
		if err != nil {
			return err
		}
		result, err := o.ReapHolds(maxAge, time.Now().UTC(), dryRun)
		if err != nil {
			return HandleError("%v", err)
		}
		if jsonOutput {
			return printJSON(result)
		}
		action := "would clear"
		if !dryRun {
			action = "cleared"
		}
		pendingSum := 0
		for _, c := range result.Cleared {
			pendingSum += c.PendingAcked
			fmt.Printf("  %-20s  %-18s  age=%-12s  pending_acked=%d\n",
				c.Seat, c.Reason, c.Age, c.PendingAcked)
		}
		fmt.Printf("%s %s %d hold(s) (skipped %d; retention %s; pending_acked %d)%s\n",
			ui.RenderAccent("*"), action, len(result.Cleared), result.Skipped, result.Retention, pendingSum,
			map[bool]string{true: " [dry-run]", false: ""}[dryRun])
		if len(result.Cleared) >= 20 || pendingSum >= 100 {
			fmt.Fprintf(os.Stderr, "notify holds reap: large backlog cleared (%d holds, %d pending) - check seat registry\n",
				len(result.Cleared), pendingSum)
		}
		return nil
	},
}

func runNotifyHoldsList(cmd *cobra.Command, args []string) error {
	o, err := openNotifyOutbox()
	if err != nil {
		return err
	}
	holds, err := o.Holds()
	if err != nil {
		return HandleError("%v", err)
	}
	type row struct {
		Seat      string       `json:"seat"`
		Hold      notify.Hold  `json:"hold"`
	}
	rows := make([]row, 0, len(holds))
	for seat, h := range holds {
		rows = append(rows, row{Seat: seat, Hold: h})
	}
	// Stable order for humans and JSON diffs.
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[j].Seat < rows[i].Seat {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	if jsonOutput {
		return printJSON(rows)
	}
	if len(rows) == 0 {
		fmt.Printf("%s no holds\n", ui.RenderAccent("*"))
		return nil
	}
	for _, r := range rows {
		fmt.Printf("  %-20s  %-18s  pending=%-4d  updated=%s\n",
			r.Seat, r.Hold.Reason, r.Hold.Pending, r.Hold.UpdatedAt)
	}
	fmt.Printf("%s %d hold(s)\n", ui.RenderAccent("*"), len(rows))
	return nil
}

func parseHoldRetention(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return notify.DefaultHoldRetention, nil
	}
	// Go's ParseDuration has no "d"; accept Nd as N*24h before falling through.
	if strings.HasSuffix(s, "d") {
		var days int
		if _, err := fmt.Sscanf(strings.TrimSuffix(s, "d"), "%d", &days); err != nil || days <= 0 {
			return 0, fmt.Errorf("invalid --older-than %q (want Nd like 14d, or Go duration like 336h)", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid --older-than %q (want Nd or Go duration like 336h): %w", s, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("--older-than must be > 0")
	}
	return d, nil
}

// pingCommentAssignee enqueues a pin-addressed notify for the bead's assignee
// after a successful comment. Best-effort: never fails the comment.
func pingCommentAssignee(assignee, issueID, issueTitle, author, text string) {
	if store != nil {
		issue, err := store.GetIssue(rootCtx, issueID)
		if err == nil && issue != nil && issue.Status == types.StatusClosed {
			return
		}
	}
	seat, ok := notify.CommentAssigneeSeat(assignee, author)
	if !ok {
		return
	}
	o, err := openNotifyOutbox()
	if err != nil {
		return
	}
	seat, err = o.ResolveSeat(seat)
	if err != nil {
		fmt.Fprintf(os.Stderr, "notify: %v\n", err)
		return
	}
	if authorID, resolveErr := o.ResolveSeat(author); resolveErr == nil && authorID == seat {
		return
	}
	title := notify.CommentPingTitle(issueTitle, author, text)
	if _, err := o.Enqueue(seat, issueID, notify.KindComment, title); err != nil {
		fmt.Fprintf(os.Stderr, "notify: enqueue comment ping: %v\n", err)
		return
	}
	tryDrainCommentPing(o, seat)
}

// tryDrainCommentPing delivers pending rows for seat through the exact pin.
// No pin → hold+escalate without herdr discovery. Tests skip herdr entirely.
func tryDrainCommentPing(o *notify.Outbox, seat string) {
	release, err := seatregistry.LockFile(filepath.Join(o.Dir(), "drain.lock"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "notify: %v\n", err)
		return
	}
	defer release()
	if os.Getenv("BEADS_TEST_MODE") != "" {
		return
	}
	pending, err := o.Pending(seat)
	if err != nil || len(pending) == 0 {
		return
	}
	live, err := liveNotifyPending(o, seat, pending, true)
	if err != nil || len(live) == 0 {
		return
	}
	discover := notify.HerdrDiscoverer{}
	instances, discErr := discover.ListAllInstances()
	if discErr == nil {
		healSeatRuntime(o, instances, true)
	} else {
		healSeatRuntime(o, nil, false)
	}
	pins, err := o.LoadPins()
	if err != nil {
		return
	}
	pin, havePin := pins[strings.ToLower(strings.TrimSpace(seat))]
	if !havePin || !pin.Valid() {
		_ = o.SetHold(seat, notify.Hold{Reason: notify.HoldNoPin, Pending: len(live)})
		return
	}
	if discErr != nil {
		_ = o.SetHold(seat, notify.Hold{
			Reason: notify.HoldDeadPin, Session: pin.Session, PaneID: pin.PaneID,
			AgentSessionID: pin.AgentSessionID, Pending: len(live),
		})
		return
	}
	dec := notify.DecideDelivery(seat, pins, instances)
	if !dec.OK {
		if dec.Escalate {
			_ = o.SetHold(seat, notify.Hold{
				Reason: dec.Reason, Session: dec.Pin.Session, PaneID: dec.Pin.PaneID,
				AgentSessionID: dec.Pin.AgentSessionID, Pending: len(live),
			})
		}
		return
	}
	var lastOK int64
	mode := dec.Mode
	if mode == "" {
		mode = notify.ModeForHarness(dec.Instance.Harness)
	}
	for i, rec := range live {
		prompt := notify.DefaultPrompt(rec)
		if body := beadNotifyBody(rec.IssueID); body != "" {
			prompt = prompt + "\n\n" + body
		}
		if err := discover.Deliver(dec.Instance, prompt, mode); err != nil {
			break
		}
		lastOK = rec.Seq
		if i == len(live)-1 {
			lastOK = pending[len(pending)-1].Seq
		}
	}
	if lastOK > 0 {
		_ = o.AckSeat(seat, lastOK)
		_ = o.ClearHold(seat)
	}
}

func holdError(dec notify.Delivery) string {
	switch dec.Reason {
	case notify.HoldDeadPin:
		target := strings.TrimSpace(dec.Pin.Session + " " + dec.Pin.PaneID)
		if dec.Pin.AgentSessionID != "" {
			if target == "" {
				target = dec.Pin.AgentSessionID
			} else {
				target = target + " " + dec.Pin.AgentSessionID
			}
		}
		if target == "" {
			target = "pin"
		}
		return "pinned target " + target + " is not live"
	case notify.HoldBlocked:
		target := strings.TrimSpace(dec.Pin.Session + " " + dec.Pin.PaneID)
		if target == "" {
			target = "pin"
		}
		return "pinned target " + target + " is at an approval dialog (blocked)"
	case notify.HoldUntilIdle:
		return fmt.Sprintf("target %s (%s) is working; holding until idle", dec.Instance.PaneID, dec.Instance.Harness)
	default:
		return "no pin in .beads/notify/seats.json"
	}
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

func runNotifyExec(command string, rec notify.Record) error {
	payload, _ := json.Marshal(rec)
	prompt := notify.DefaultPrompt(rec)
	cmd := exec.Command("sh", "-c", command)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("BD_NOTIFY_SEQ=%d", rec.Seq),
		"BD_NOTIFY_SEAT="+rec.Seat,
		"BD_NOTIFY_ID="+rec.IssueID,
		"BD_NOTIFY_KIND="+string(rec.Kind),
		"BD_NOTIFY_TITLE="+rec.Title,
		"BD_NOTIFY_PROMPT="+prompt,
		"BD_NOTIFY_JSON="+string(payload),
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// beadNotifyBody best-effort loads description for richer prompts.
func beadNotifyBody(id string) string {
	if store == nil || id == "" {
		return ""
	}
	issue, err := store.GetIssue(rootCtx, id)
	if err != nil || issue == nil {
		return ""
	}
	body := strings.TrimSpace(issue.Description)
	// The due playbook says "close it in beads"; for a recurring bead the close
	// guard refuses, that advice is wrong, so append the generated rule next to it.
	if hint := issueops.RecurrenceCloseHint(issue); hint != "" {
		if body != "" {
			body += "\n\n"
		}
		body += hint
	}
	return body
}

// enqueueDueSweepNotifies writes seat-addressed outbox rows for a sweep report.
// Best-effort: a notify failure must not fail the sweep clock.
func enqueueDueSweepNotifies(report dueSweepReport) {
	dir := beads.FindBeadsDir()
	if dir == "" {
		return
	}
	o, err := notify.Open(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "notify: open outbox: %v\n", err)
		return
	}
	seats := report.ByAssignee
	escalated := map[string]bool{}
	for _, id := range report.EscalatedIDs {
		escalated[id] = true
	}
	var n int
	for _, seat := range seats {
		if isUnassignedNotifySeat(seat.Assignee) {
			continue
		}
		for _, id := range seat.IDs {
			kind := notify.KindDue
			if escalated[id] {
				kind = notify.KindEscalate
			}
			title := ""
			if store != nil {
				if issue, err := store.GetIssue(rootCtx, id); err == nil && issue != nil {
					title = issue.Title
					// The 24h due-miss grace push re-fires an overdue bead
					// every day, which for a seat holding 200 overdue beads is
					// 200 pings a day of the same "still overdue" fact. A
					// NON-recurring bead therefore pings on its first miss and
					// on each priority raise only; a recurring bead pings on
					// every occurrence, which is the push the cadence exists
					// for.
					if issueops.RepeatWindowQuiet(issue, time.Now().UTC()) {
						// The window is quiet right now: the recurring bead
						// is silent by design and neither the occurrence ping
						// nor the escalate fires outside it.
						continue
					}
					if issue.RepeatPattern == "" && !escalated[id] && issue.DueMissed > 1 {
						continue
					}
				}
			}
			_, added, err := o.EnqueueUnique(seat.Assignee, id, kind, title)
			if err != nil {
				fmt.Fprintf(os.Stderr, "notify: enqueue %s: %v\n", id, err)
				continue
			}
			if added {
				n++
			}
		}
	}
	if n > 0 && !jsonOutput {
		fmt.Printf("  notify enqueued %d\n", n)
	}
}

// heldNotifyLabel marks a bead as held. Beads has no captain-kind hold
// representation, so every held bead is treated as non-dispatchable.
const heldNotifyLabel = "tasks-axi-held"

// liveNotifyPending drops rows whose bead is closed or held. Every pending and
// drain path filters here before any hold or delivery decision. With ack, a
// seat whose rows are all stale has them acked and its hold cleared so they
// cannot re-hold or re-escalate the seat.
func liveNotifyPending(o *notify.Outbox, seat string, pending []notify.Record, ack bool) ([]notify.Record, error) {
	var live []notify.Record
	for _, rec := range pending {
		if !suppressNotifyRecord(rec) {
			live = append(live, rec)
		}
	}
	if len(live) > 0 || len(pending) == 0 || !ack {
		return live, nil
	}
	if err := o.AckSeat(seat, pending[len(pending)-1].Seq); err != nil {
		return nil, err
	}
	return nil, o.ClearHold(seat)
}

func suppressNotifyRecord(rec notify.Record) bool {
	if store == nil {
		return false
	}
	issue, err := store.GetIssue(rootCtx, rec.IssueID)
	if err != nil || issue == nil {
		return false
	}
	labels, err := store.GetLabels(rootCtx, rec.IssueID)
	return err == nil && shouldSuppressNotifyIssue(issue, labels)
}

// shouldSuppressNotifyIssue reports whether a queued row for issue must not be
// delivered. Any row for a held bead is non-dispatchable, whatever its kind.
// A drain acks held rows, so removing the label restores only pings that are
// re-enqueued afterwards, not rows already acked while the bead was held.
func shouldSuppressNotifyIssue(issue *types.Issue, labels []string) bool {
	if issue.Status == types.StatusClosed {
		return true
	}
	for _, l := range labels {
		if l == heldNotifyLabel {
			return true
		}
	}
	return false
}

// enqueueStaleClaimNotifies finds assigned in_progress work quiet longer than
// comment.stale_claim_after and enqueues KindStaleClaim (educational playbook).
// Best-effort; never fails the due clock. Skips ids already due-fired this tick
// and ids with a recent stale-claim row (cooldown = same threshold).
func enqueueStaleClaimNotifies(skipIDs []string) {
	after := issueops.StaleClaimAfter()
	if after <= 0 || store == nil {
		return
	}
	dir := beads.FindBeadsDir()
	if dir == "" {
		return
	}
	o, err := notify.Open(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "notify: open outbox (stale-claim): %v\n", err)
		return
	}
	now := time.Now().UTC()
	stale, err := issueops.FindStaleClaims(rootCtx, store, after, now)
	if err != nil {
		fmt.Fprintf(os.Stderr, "notify: stale-claim scan: %v\n", err)
		return
	}
	skip := map[string]bool{}
	for _, id := range skipIDs {
		skip[strings.TrimSpace(id)] = true
	}
	// Also skip anything already pending (any kind) for this issue.
	pending, _ := o.Pending("")
	for _, rec := range pending {
		skip[rec.IssueID] = true
	}
	cooldownSince := now.Add(-after)
	const maxPerSweep = 10 // ponytail: teach without flooding every seat in one tick
	var n int
	for _, sc := range stale {
		if n >= maxPerSweep {
			break
		}
		if skip[sc.ID] {
			continue
		}
		recent, err := o.HasRecent(sc.ID, notify.KindStaleClaim, cooldownSince)
		if err != nil {
			fmt.Fprintf(os.Stderr, "notify: stale-claim recent %s: %v\n", sc.ID, err)
			continue
		}
		if recent {
			continue
		}
		title := sc.Title
		if isUnassignedNotifySeat(sc.Assignee) {
			continue
		}
		if title == "" {
			title = sc.Reason
		} else {
			title = title + " [" + sc.QuietFor + " quiet]"
		}
		if _, err := o.Enqueue(sc.Assignee, sc.ID, notify.KindStaleClaim, title); err != nil {
			fmt.Fprintf(os.Stderr, "notify: enqueue stale-claim %s: %v\n", sc.ID, err)
			continue
		}
		n++
	}
	if n > 0 && !jsonOutput {
		fmt.Printf("  stale-claim enqueued %d\n", n)
	}
}

func init() {
	notifyPendingCmd.Flags().String("seat", "", "filter by assignee seat")
	notifyResolveCmd.Flags().String("seat", "", "assignee seat (required)")
	notifyDrainCmd.Flags().String("seat", "", "assignee seat to drain")
	notifyDrainCmd.Flags().Bool("all", false, "drain every seat with pending rows")
	notifyDrainCmd.Flags().Int("limit", 0, "max rows per seat (0 = all pending)")
	notifyDrainCmd.Flags().String("exec", "", "override transport shell command (BD_NOTIFY_* env)")
	notifyDrainCmd.Flags().Bool("print", false, "print rows only (no herdr prompt)")
	notifyDrainCmd.Flags().Bool("no-ack", false, "do not mark rows delivered")
	notifyAckCmd.Flags().String("seat", "", "assignee seat (required)")
	notifyAckCmd.Flags().Int64("upto", 0, "ack through this seq (required)")
	notifyHoldsReapCmd.Flags().String("older-than", "14d", "reap dead-pin/no-pin holds older than this (Nd or Go duration)")
	notifyHoldsReapCmd.Flags().Bool("dry-run", false, "report eligible holds without writing")
	notifyHoldsReapCmd.Flags().Bool("yes", false, "apply the reap (required unless --dry-run)")

	// Bare `bd notify holds` lists; `bd notify holds list|reap` for explicit.
	notifyHoldsCmd.RunE = runNotifyHoldsList
	notifyHoldsCmd.AddCommand(notifyHoldsListCmd)
	notifyHoldsCmd.AddCommand(notifyHoldsReapCmd)

	notifyCmd.AddCommand(notifyPendingCmd)
	notifyCmd.AddCommand(notifyResolveCmd)
	notifyCmd.AddCommand(notifyDrainCmd)
	notifyCmd.AddCommand(notifyAckCmd)
	notifyCmd.AddCommand(notifySeatsCmd)
	notifyCmd.AddCommand(notifyHoldsCmd)
	rootCmd.AddCommand(notifyCmd)
}
