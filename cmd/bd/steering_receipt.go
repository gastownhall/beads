package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

const (
	steeringReceiptKind           = "owner-steering"
	steeringReconciliationMarker  = "Steering reconciliation"
	steeringRequireReconciliation = "steering.require_reconciliation_on_close"
	steeringPayloadHashPrefix     = "payload_sha256:"
)

// steeringReceipt is the durable, secret-free record a provider hook writes
// onto the owning Bead. The raw prompt is never stored.
type steeringReceipt struct {
	Provider   string
	Event      string
	ThreadID   string
	TurnID     string
	EventID    string
	PayloadSHA string
}

func hashSteeringPayload(payload string) string {
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func unavailableIdentity(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unavailable"
	}
	return value
}

func formatSteeringReceipt(r steeringReceipt) string {
	return strings.Join([]string{
		"Durable steering receipt v1",
		"kind: " + steeringReceiptKind,
		"provider: " + r.Provider,
		"event: " + r.Event,
		"thread_id: " + unavailableIdentity(r.ThreadID),
		"turn_id: " + unavailableIdentity(r.TurnID),
		"event_id: " + unavailableIdentity(r.EventID),
		steeringPayloadHashPrefix + " " + r.PayloadSHA,
		"raw_prompt: omitted",
	}, "\n") + "\n"
}

func commentHasPayloadHash(text, payloadSHA string) bool {
	needle := steeringPayloadHashPrefix + " " + payloadSHA
	alt := steeringPayloadHashPrefix + "=" + payloadSHA
	return strings.Contains(text, needle) || strings.Contains(text, alt)
}

func commentsHavePayloadHash(texts []string, payloadSHA string) bool {
	for _, text := range texts {
		if commentHasPayloadHash(text, payloadSHA) {
			return true
		}
	}
	return false
}

// unreconciledSteeringReceipts reports receipt comments that appear after the
// last "Steering reconciliation" marker.
func unreconciledSteeringReceipts(texts []string) []string {
	lastReconcile := -1
	for i, text := range texts {
		if strings.Contains(text, steeringReconciliationMarker) {
			lastReconcile = i
		}
	}
	var pending []string
	for i, text := range texts {
		if i <= lastReconcile {
			continue
		}
		if strings.Contains(text, "kind: "+steeringReceiptKind) && strings.Contains(text, steeringPayloadHashPrefix) {
			pending = append(pending, text)
		}
	}
	return pending
}

type commentTextRow struct {
	Text string `json:"text"`
}

// steeringOwningBeadID returns the Bead that should receive the receipt.
// Tests stub this; production uses last-touched.
var steeringOwningBeadID = GetLastTouchedID

// steeringCommentLister lists comment bodies for an issue. Tests stub this so
// hooks stay no-store like bd prime.
var steeringCommentLister = func(ctx context.Context, issueID string) ([]string, error) {
	// #nosec G702 -- os.Args[0] is this bd binary; issueID is a bead id.
	cmd := exec.CommandContext(ctx, os.Args[0], "comments", issueID, "--json")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("bd comments %s: %w: %s", issueID, err, strings.TrimSpace(string(out)))
	}
	var rows []commentTextRow
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("bd comments %s: parse: %w", issueID, err)
	}
	texts := make([]string, 0, len(rows))
	for _, row := range rows {
		texts = append(texts, row.Text)
	}
	return texts, nil
}

var steeringCommentAdder = func(ctx context.Context, issueID, text string) error {
	// #nosec G702 -- os.Args[0] is this bd binary; issueID is a bead id.
	cmd := exec.CommandContext(ctx, os.Args[0], "comments", "add", issueID, text)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("bd comments add %s: %w: %s", issueID, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// recordSteeringOnOwningBead writes a secret-free receipt onto last-touched.
// Missing owning Bead is a no-op (cannot invent an id). Duplicate payload
// hashes are skipped. Errors are returned for the caller to swallow or surface.
func recordSteeringOnOwningBead(ctx context.Context, r steeringReceipt, payload string) error {
	issueID := steeringOwningBeadID()
	if issueID == "" {
		return nil
	}
	if strings.TrimSpace(payload) == "" {
		return nil
	}
	r.PayloadSHA = hashSteeringPayload(payload)
	texts, err := steeringCommentLister(ctx, issueID)
	if err != nil {
		return err
	}
	if commentsHavePayloadHash(texts, r.PayloadSHA) {
		return nil
	}
	return steeringCommentAdder(ctx, issueID, formatSteeringReceipt(r))
}

func refuseCloseForUnreconciledSteering(force, require bool, texts []string) string {
	if force || !require {
		return ""
	}
	n := len(unreconciledSteeringReceipts(texts))
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%d unreconciled steering receipt(s); add a comment containing %q or pass --force", n, steeringReconciliationMarker)
}

func steeringCommentTexts(comments []*types.Comment) []string {
	texts := make([]string, 0, len(comments))
	for _, c := range comments {
		if c != nil {
			texts = append(texts, c.Text)
		}
	}
	return texts
}
