package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Instance is one live herdr-hosted agent of any harness (pi, claude, codex, …).
// Herdr detects the harness. Drain uses session + pane to address the pin, and
// the harness label to choose follow-up vs steer.
type Instance struct {
	Session        string `json:"session"`
	PaneID         string `json:"pane_id"`
	Harness        string `json:"harness"` // herdr "agent" field
	Status         string `json:"status"`
	Title          string `json:"title"`
	Cwd            string `json:"cwd"`
	Focused        bool   `json:"focused"`
	AgentSessionID string `json:"agent_session_id,omitempty"`
}

// HerdrDiscoverer finds live agent instances via herdr session/agent list.
// New harnesses appear automatically once herdr can detect them.
type HerdrDiscoverer struct {
	Bin     string
	Timeout time.Duration
	// runFn, when set, replaces the herdr subprocess. Tests record argv.
	runFn func(args ...string) ([]byte, error)
}

func (h HerdrDiscoverer) bin() string {
	if b := strings.TrimSpace(h.Bin); b != "" {
		return b
	}
	if env := strings.TrimSpace(os.Getenv("HERDR_BIN")); env != "" {
		return env
	}
	if p, err := exec.LookPath("herdr"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "bin", "herdr")
}

func (h HerdrDiscoverer) timeout() time.Duration {
	if h.Timeout > 0 {
		return h.Timeout
	}
	return 15 * time.Second
}

func (h HerdrDiscoverer) run(args ...string) ([]byte, error) {
	if h.runFn != nil {
		return h.runFn(args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), h.timeout())
	defer cancel()
	cmd := exec.CommandContext(ctx, h.bin(), args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("herdr %v: %w (%s)", args, err, msg)
		}
		return nil, fmt.Errorf("herdr %v: %w", args, err)
	}
	return out, nil
}

// ListSessions returns names of running herdr sessions (excludes default).
func (h HerdrDiscoverer) ListSessions() ([]string, error) {
	out, err := h.run("session", "list", "--json")
	if err != nil {
		return nil, err
	}
	var payload struct {
		Sessions []struct {
			Name    string `json:"name"`
			Running bool   `json:"running"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return nil, fmt.Errorf("herdr session list json: %w", err)
	}
	var names []string
	for _, s := range payload.Sessions {
		if !s.Running || s.Name == "" || s.Name == "default" {
			continue
		}
		names = append(names, s.Name)
	}
	sort.Strings(names)
	return names, nil
}

// ListAllInstances walks every running session and collects agents.
func (h HerdrDiscoverer) ListAllInstances() ([]Instance, error) {
	sessions, err := h.ListSessions()
	if err != nil {
		return nil, err
	}
	var all []Instance
	for _, session := range sessions {
		inst, err := h.listSession(session)
		if err != nil {
			return nil, err
		}
		all = append(all, inst...)
	}
	return all, nil
}

func (h HerdrDiscoverer) listSession(session string) ([]Instance, error) {
	out, err := h.run("--session", session, "agent", "list")
	if err != nil {
		return nil, err
	}
	var payload struct {
		Result struct {
			Agents []struct {
				Agent                 string `json:"agent"`
				AgentStatus           string `json:"agent_status"`
				Cwd                   string `json:"cwd"`
				Focused               bool   `json:"focused"`
				PaneID                string `json:"pane_id"`
				TerminalTitle         string `json:"terminal_title"`
				TerminalTitleStripped string `json:"terminal_title_stripped"`
				AgentSession          struct {
					Value string `json:"value"`
				} `json:"agent_session"`
			} `json:"agents"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return nil, fmt.Errorf("herdr agent list %s json: %w", session, err)
	}
	var outI []Instance
	for _, a := range payload.Result.Agents {
		if strings.TrimSpace(a.PaneID) == "" {
			continue
		}
		title := a.TerminalTitleStripped
		if title == "" {
			title = a.TerminalTitle
		}
		outI = append(outI, Instance{
			Session:        session,
			PaneID:         a.PaneID,
			Harness:        a.Agent,
			Status:         a.AgentStatus,
			Title:          title,
			Cwd:            a.Cwd,
			Focused:        a.Focused,
			AgentSessionID: a.AgentSession.Value,
		})
	}
	return outI, nil
}

// Prompt delivers text using ModeForHarness(inst.Harness).
func (h HerdrDiscoverer) Prompt(inst Instance, text string) error {
	return h.Deliver(inst, text, ModeForHarness(inst.Harness))
}

// Deliver submits text with an explicit delivery mode. Follow-up uses the
// harness queue key (with ctrl+j fallback for pi); steer uses herdr agent prompt (Enter).
func (h HerdrDiscoverer) Deliver(inst Instance, text string, mode DeliveryMode) error {
	norm := normalizeHarness(inst.Harness)
	if mode == ModeFollowUp && (norm == "pi" || norm == "pi-signed") {
		pasteCall := deliveryCall{Args: sessionArgs(inst, "pane", "send-text", inst.PaneID, wrapFollowUpPaste(text))}
		if _, err := h.run(pasteCall.Args...); err != nil {
			return err
		}
		keyCall := deliveryCall{Args: sessionArgs(inst, "pane", "send-keys", inst.PaneID, "alt+enter")}
		if _, err := h.run(keyCall.Args...); err != nil {
			fallbackCall := deliveryCall{Args: sessionArgs(inst, "pane", "send-keys", inst.PaneID, "ctrl+j")}
			if _, err2 := h.run(fallbackCall.Args...); err2 != nil {
				return fmt.Errorf("pi follow-up send-keys failed: %w (ctrl+j fallback: %v)", err, err2)
			}
		}
		return nil
	}

	for _, call := range deliveryCalls(inst, text, mode) {
		if _, err := h.run(call.Args...); err != nil {
			return err
		}
	}
	return nil
}

// DefaultPrompt builds the standard beads notify prompt for a record.
//
// The notification is a follow-up reminder, never a steering order
// (captain order 2026-11-01): it must not interrupt, redirect, or park the
// agent that receives it. The old command playbooks made agents over-apply -
// stopping work, stamping, and going idle on a comment's arrival - so the
// body now names what happened, points at the thread, and explicitly
// disclaims any change to the agent's priorities.
func DefaultPrompt(rec Record) string {
	title := rec.Title
	if title == "" {
		title = rec.IssueID
	}
	var b strings.Builder
	fmt.Fprintf(&b,
		"BEADS NOTIFY (%s). Bead %s fired for seat %s: %s\n",
		rec.Kind, rec.IssueID, rec.Seat, title,
	)
	fmt.Fprintf(&b, "%s\n\n", reminderLine(rec.Kind))
	fmt.Fprintf(&b, "Read the thread: bd comments %s   (full detail: bd show %s)\n\n", rec.IssueID, rec.IssueID)
	b.WriteString(reminderFooter)
	return b.String()
}

// reminderFooter is the anti-over-application core: the notification is a
// nudge in case the bead slipped, and nothing in it licenses stopping,
// reprioritizing, or idling.
const reminderFooter = `This is a reminder in case the bead slipped, nothing more. It does not
change your priorities, asks for no status change, and is never a reason to
stop or go idle. Continue your current work and fold this in when it fits.
The bead's own thread carries any real follow-up.`

// reminderLine is the one kind-specific sentence. It names what happened
// without commanding anything.
func reminderLine(kind Kind) string {
	switch kind {
	case KindComment:
		return "New comment on a bead your seat owns."
	case KindStaleClaim:
		return "This bead is still open under your seat; a finished bead closes in beads with a real reason."
	case KindProgress:
		return "The progress trail on this bead wants a delta comment the next time you touch it."
	case KindEscalate:
		return "This bead escalated after repeated misses; it deserves attention when you can give it."
	default:
		return "This bead's due date arrived."
	}
}
