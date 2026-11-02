package notify

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/seats"
)

const (
	// PinsFile is the exact seat → herdr target map. Delivery never
	// discovers a pane; it only uses this file.
	PinsFile = "pins.json" // Legacy migration input only.
	// HoldsFile records seats whose drain held for parent escalation.
	HoldsFile = "holds.json"

	// HoldNoPin means the seat has no pin; the row stays queued and escalates.
	HoldNoPin = "no-pin"
	// HoldDeadPin means the pin exists but that exact target is not live; the row stays queued and escalates.
	HoldDeadPin = "pinned-target-dead"
	// HoldBlocked means the target is at an approval dialog or blocked; the row stays queued and escalates.
	HoldBlocked = "target-blocked"
	// HoldUntilIdle means target is working and harness has no verified follow-up queue;
	// the row stays queued for the next turn boundary without parent escalation.
	HoldUntilIdle = "hold-until-idle"

	// Delivery exit classifications matching prototype semantics:
	ExitDelivered = 0
	ExitNoPin     = 5
	ExitDeadPin   = 7
	ExitBlocked   = 8
	ExitDeferred  = 9 // hold-until-idle
	ExitFailed    = 10
)

// Pin is an exact delivery target. Session+pane_id and/or agent_session_id
// must match a live herdr instance with no scoring or lookalike fallback.
type Pin struct {
	Liveness       string `json:"liveness,omitempty"`
	Session        string `json:"session,omitempty"`
	PaneID         string `json:"pane_id,omitempty"`
	AgentSessionID string `json:"agent_session_id,omitempty"`
	Source         string `json:"source,omitempty"`
}

// Valid reports whether the pin names at least one exact locator.
func (p Pin) Valid() bool {
	hasPane := strings.TrimSpace(p.Session) != "" && strings.TrimSpace(p.PaneID) != ""
	hasAgent := strings.TrimSpace(p.AgentSessionID) != ""
	return hasPane || hasAgent
}

// Matches is exact: every field the pin sets must equal the instance.
func (p Pin) Matches(inst Instance) bool {
	if !p.Valid() {
		return false
	}
	if s := strings.TrimSpace(p.Session); s != "" {
		if s != strings.TrimSpace(inst.Session) {
			return false
		}
	}
	if id := strings.TrimSpace(p.PaneID); id != "" {
		if id != strings.TrimSpace(inst.PaneID) {
			return false
		}
	}
	if id := strings.TrimSpace(p.AgentSessionID); id != "" {
		if id != strings.TrimSpace(inst.AgentSessionID) {
			return false
		}
	}
	return true
}

// Delivery is the drain decision for one seat.
type Delivery struct {
	Instance       Instance
	Pin            Pin
	OK             bool
	Escalate       bool
	Reason         string
	Mode           DeliveryMode
	Classification int
}

// DecideDelivery is the delivery resolver.
//
// Semantics:
//   - Unassigned or empty: no delivery.
//   - No pin or dead pin: hold row and mark for parent escalation (exit 5/7).
//   - Blocked dialog (approval dialog open): never type into it; hold row and mark for escalation (exit 8).
//   - Mid-turn working:
//   - Verified follow-up (pi, cursor, codex): deliver as follow-up (Option+Enter or Tab).
//   - Unverified queue (claude, agy, gemini, kimi, omp, muse, unknown): hold-until-idle (exit 9 deferred, row stays queued, no escalation).
//   - Idle/done: deliver immediately with Enter prompt (trivially non-interrupting).
//   - Unknown status:
//   - pi, cursor, codex: safe follow-up.
//   - others: hold-until-idle.
func DecideDelivery(seat string, pins map[string]Pin, live []Instance) Delivery {
	seat = strings.TrimSpace(seat)
	if seat == "" || seat == UnassignedSeat {
		return Delivery{Reason: HoldNoPin, Classification: ExitNoPin}
	}
	if pins == nil {
		return Delivery{Reason: HoldNoPin, Escalate: true, Classification: ExitNoPin}
	}
	pin, ok := pins[seat]
	if !ok || !pin.Valid() {
		return Delivery{Pin: pin, Reason: HoldNoPin, Escalate: true, Classification: ExitNoPin}
	}
	if pin.Liveness == seats.Stale || pin.Liveness == seats.Dead {
		return Delivery{Pin: pin, Reason: HoldDeadPin, Escalate: true, Classification: ExitDeadPin}
	}
	inst, found := FindPinnedInstance(pin, live)
	if !found {
		return Delivery{Pin: pin, Reason: HoldDeadPin, Escalate: true, Classification: ExitDeadPin}
	}

	status := strings.ToLower(strings.TrimSpace(inst.Status))
	prof := ProfileForHarness(inst.Harness)

	switch status {
	case "blocked":
		// Target is at an approval dialog. Never type into it; hold and mark for escalation.
		return Delivery{
			Instance:       inst,
			Pin:            pin,
			OK:             false,
			Escalate:       true,
			Reason:         HoldBlocked,
			Classification: ExitBlocked,
		}

	case "working":
		if prof.HoldUntilIdle {
			// Target is mid-turn and harness lacks verified non-interrupting queue.
			// Hold row for the next turn boundary; do NOT mark for parent escalation.
			return Delivery{
				Instance:       inst,
				Pin:            pin,
				OK:             false,
				Escalate:       false,
				Reason:         HoldUntilIdle,
				Mode:           ModeHoldUntilIdle,
				Classification: ExitDeferred,
			}
		}
		// Verified non-interrupting follow-up submit (pi, cursor, codex)
		return Delivery{
			Instance:       inst,
			Pin:            pin,
			OK:             true,
			Escalate:       false,
			Mode:           ModeFollowUp,
			Classification: ExitDelivered,
		}

	case "idle", "done":
		// Target is not in flight; Enter submit starts a turn without interrupting.
		return Delivery{
			Instance:       inst,
			Pin:            pin,
			OK:             true,
			Escalate:       false,
			Mode:           ModeSteer,
			Classification: ExitDelivered,
		}

	default:
		// "unknown" or unset status
		// Follow-up key harnesses (pi, cursor, codex) are safe to queue
		if prof.SubmitKey != "" {
			return Delivery{
				Instance:       inst,
				Pin:            pin,
				OK:             true,
				Escalate:       false,
				Mode:           ModeFollowUp,
				Classification: ExitDelivered,
			}
		}
		// Conservative fallback for others: hold until idle
		return Delivery{
			Instance:       inst,
			Pin:            pin,
			OK:             false,
			Escalate:       false,
			Reason:         HoldUntilIdle,
			Mode:           ModeHoldUntilIdle,
			Classification: ExitDeferred,
		}
	}
}

// FindPinnedInstance returns the live instance that exactly matches pin.
func FindPinnedInstance(pin Pin, live []Instance) (Instance, bool) {
	var match Instance
	found := false
	for _, inst := range live {
		if pin.Matches(inst) {
			if found {
				return Instance{}, false
			}
			match, found = inst, true
		}
	}
	return match, found
}

// LoadPins projects the registry's exact endpoints into the delivery pin
// shape. When seats.json is ABSENT (workspace not yet migrated) it falls back
// to the legacy pins.json so an un-migrated workspace keeps delivering instead
// of degrading to an exit-5 hold. A missing pins.json is an empty map, not an
// error.
func LoadPins(dir string) (map[string]Pin, error) {
	if _, err := os.Stat(filepath.Join(dir, seats.File)); err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		return loadLegacyPins(dir)
	}
	r, err := seats.Load(dir)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Pin, len(r))
	for id, seat := range r {
		out[id] = Pin{Session: seat.Endpoint.Session, PaneID: seat.Endpoint.PaneID, AgentSessionID: seat.Endpoint.AgentSessionID, Liveness: seat.Liveness}
	}
	return out, nil
}

// legacyPin is one entry of the pre-migration pins.json map.
type legacyPin struct {
	Session        string `json:"session"`
	PaneID         string `json:"pane_id"`
	AgentSessionID string `json:"agent_session_id"`
}

// loadLegacyPins reads the old pins.json read path, kept so an un-migrated
// workspace degrades to working rather than to a no-pin hold.
func loadLegacyPins(dir string) (map[string]Pin, error) {
	data, err := os.ReadFile(filepath.Join(dir, PinsFile)) //nolint:gosec // fixed legacy file in the notify dir
	if os.IsNotExist(err) {
		return map[string]Pin{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read pins: %w", err)
	}
	raw := map[string]legacyPin{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse pins: %w", err)
	}
	out := make(map[string]Pin, len(raw))
	for key, entry := range raw {
		out[key] = Pin{Session: entry.Session, PaneID: entry.PaneID, AgentSessionID: entry.AgentSessionID, Source: "legacy-pins"}
	}
	return out, nil
}

// LoadPins reads this outbox's registry endpoints.
func (o *Outbox) LoadPins() (map[string]Pin, error) {
	return LoadPins(o.dir)
}

// Hold is a durable parent-escalation mark for a seat that could not be
// delivered. Pending rows stay in the outbox.
type Hold struct {
	Reason         string `json:"reason"`
	Session        string `json:"session,omitempty"`
	PaneID         string `json:"pane_id,omitempty"`
	AgentSessionID string `json:"agent_session_id,omitempty"`
	Pending        int    `json:"pending,omitempty"`
	UpdatedAt      string `json:"updated_at"`
}

// SetHold records that drain held this seat (parent should escalate).
func (o *Outbox) SetHold(seat string, h Hold) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	release, lockErr := seats.LockFile(filepath.Join(o.dir, "outbox.lock"))
	if lockErr != nil {
		return lockErr
	}
	defer release()
	seat = strings.ToLower(strings.TrimSpace(seat))
	if seat == "" {
		return fmt.Errorf("notify: hold requires a seat")
	}
	holds, err := o.loadHoldsLocked()
	if err != nil {
		return err
	}
	if h.UpdatedAt == "" {
		h.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	holds[seat] = h
	return o.saveHoldsLocked(holds)
}

// Holds returns the current escalation marks.
func (o *Outbox) Holds() (map[string]Hold, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	release, lockErr := seats.LockFile(filepath.Join(o.dir, "outbox.lock"))
	if lockErr != nil {
		return nil, lockErr
	}
	defer release()
	return o.loadHoldsLocked()
}

// ClearHold drops a seat's escalation mark after a successful delivery.
func (o *Outbox) ClearHold(seat string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	release, lockErr := seats.LockFile(filepath.Join(o.dir, "outbox.lock"))
	if lockErr != nil {
		return lockErr
	}
	defer release()
	seat = strings.ToLower(strings.TrimSpace(seat))
	if seat == "" {
		return fmt.Errorf("notify: clear hold requires a seat")
	}
	holds, err := o.loadHoldsLocked()
	if err != nil {
		return err
	}
	if _, ok := holds[seat]; !ok {
		return nil
	}
	delete(holds, seat)
	return o.saveHoldsLocked(holds)
}

func (o *Outbox) loadHoldsLocked() (map[string]Hold, error) {
	path := filepath.Join(o.dir, HoldsFile)
	data, err := os.ReadFile(path) //nolint:gosec // G304: o.dir is the outbox path; HoldsFile is a constant
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Hold{}, nil
		}
		return nil, err
	}
	out := map[string]Hold{}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("notify: parse holds: %w", err)
	}
	norm := make(map[string]Hold, len(out))
	for k, v := range out {
		norm[strings.ToLower(k)] = v
	}
	return norm, nil
}

func (o *Outbox) saveHoldsLocked(holds map[string]Hold) error {
	path := filepath.Join(o.dir, HoldsFile)
	data, err := json.MarshalIndent(holds, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// DefaultHoldRetention is how long a dead-pin / no-pin hold may live before
// reap. Transient holds (hold-until-idle, target-blocked) are never reaped.
const DefaultHoldRetention = 14 * 24 * time.Hour

// ReapableHoldReason reports whether a hold reason is eligible for retention
// reap. Only escalation marks for seats that will not self-heal without a
// human pin/re-register are reapable.
func ReapableHoldReason(reason string) bool {
	switch reason {
	case HoldNoPin, HoldDeadPin:
		return true
	default:
		return false
	}
}

// ReapedHold is one seat cleared by ReapHolds.
type ReapedHold struct {
	Seat         string `json:"seat"`
	Reason       string `json:"reason"`
	UpdatedAt    string `json:"updated_at"`
	PendingAcked int    `json:"pending_acked"`
	Age          string `json:"age"`
}

// ReapHoldsResult summarizes a retention pass.
type ReapHoldsResult struct {
	Cleared   []ReapedHold `json:"cleared"`
	Skipped   int          `json:"skipped"`
	Retention string       `json:"retention"`
	DryRun    bool         `json:"dry_run"`
}

// ReapHolds removes dead-pin / no-pin holds older than maxAge and acks any
// pending outbox rows for those seats (they are undeliverable until the seat
// re-registers; a later fire enqueues a fresh row). Transient holds
// (hold-until-idle, target-blocked) are never reaped. dryRun reports what
// would change without writing.
func (o *Outbox) ReapHolds(maxAge time.Duration, now time.Time, dryRun bool) (ReapHoldsResult, error) {
	if maxAge <= 0 {
		maxAge = DefaultHoldRetention
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	release, lockErr := seats.LockFile(filepath.Join(o.dir, "outbox.lock"))
	if lockErr != nil {
		return ReapHoldsResult{}, lockErr
	}
	defer release()

	holds, err := o.loadHoldsLocked()
	if err != nil {
		return ReapHoldsResult{}, err
	}
	acks, err := o.loadAcksLocked()
	if err != nil {
		return ReapHoldsResult{}, err
	}
	all, err := o.readAllLocked()
	if err != nil {
		return ReapHoldsResult{}, err
	}

	result := ReapHoldsResult{
		Retention: maxAge.String(),
		DryRun:    dryRun,
		Cleared:   []ReapedHold{},
	}
	changedHolds := false
	changedAcks := false
	for seat, h := range holds {
		if !ReapableHoldReason(h.Reason) {
			result.Skipped++
			continue
		}
		updated, parseErr := time.Parse(time.RFC3339, h.UpdatedAt)
		if parseErr != nil || h.UpdatedAt == "" {
			// Unparseable / missing timestamp: treat as immediately eligible
			// so a corrupt hold cannot stall retention forever.
			updated = time.Time{}
		}
		age := now.Sub(updated)
		if !updated.IsZero() && age < maxAge {
			result.Skipped++
			continue
		}
		pendingAcked := 0
		var maxSeq int64
		for _, rec := range all {
			if !strings.EqualFold(rec.Seat, seat) {
				continue
			}
			if rec.Seq > acks[strings.ToLower(seat)] && rec.Seq > maxSeq {
				maxSeq = rec.Seq
			}
		}
		if maxSeq > acks[strings.ToLower(seat)] {
			for _, rec := range all {
				if strings.EqualFold(rec.Seat, seat) && rec.Seq > acks[strings.ToLower(seat)] && rec.Seq <= maxSeq {
					pendingAcked++
				}
			}
			if !dryRun {
				acks[strings.ToLower(seat)] = maxSeq
				changedAcks = true
			}
		}
		ageLabel := "unknown"
		if !updated.IsZero() {
			ageLabel = age.Round(time.Second).String()
		}
		result.Cleared = append(result.Cleared, ReapedHold{
			Seat:         seat,
			Reason:       h.Reason,
			UpdatedAt:    h.UpdatedAt,
			PendingAcked: pendingAcked,
			Age:          ageLabel,
		})
		if !dryRun {
			delete(holds, seat)
			changedHolds = true
		}
	}
	sort.Slice(result.Cleared, func(i, j int) bool {
		return result.Cleared[i].Seat < result.Cleared[j].Seat
	})
	if dryRun {
		return result, nil
	}
	if changedHolds {
		if err := o.saveHoldsLocked(holds); err != nil {
			return result, err
		}
	}
	if changedAcks {
		if err := o.saveAcksLocked(acks); err != nil {
			return result, err
		}
	}
	return result, nil
}
