// Package storage — merge slot helpers shared across DoltStore and
// EmbeddedDoltStore.  Both stores satisfy Storage, so all logic can be
// expressed in terms of the interface methods.
package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// mergeSlotLabel is the label attached to every merge slot bead so that
// tooling can find it without knowing the exact ID.
const mergeSlotLabel = "gt:slot"

// slotMeta holds the merge slot state stored in the issue Metadata field.
type slotMeta struct {
	Holder  string   `json:"holder,omitempty"`
	Waiters []string `json:"waiters,omitempty"`
}

// MergeSlotID returns the canonical merge slot bead ID for the store,
// derived from the issue_prefix config key (e.g. "gt" → "gt-merge-slot").
// Falls back to "bd-merge-slot" when the prefix is not configured.
func MergeSlotID(ctx context.Context, s Storage) string {
	prefix := "bd"
	if p, err := s.GetConfig(ctx, "issue_prefix"); err == nil && p != "" {
		prefix = strings.TrimSuffix(p, "-")
	}
	return prefix + "-merge-slot"
}

// MergeSlotCreateImpl is the shared implementation of Storage.MergeSlotCreate.
func MergeSlotCreateImpl(ctx context.Context, s Storage, actor string) (*types.Issue, error) {
	slotID := MergeSlotID(ctx, s)

	// Idempotent: return existing slot without error.
	if existing, err := s.GetIssue(ctx, slotID); err == nil && existing != nil {
		return existing, nil
	}

	issue := &types.Issue{
		ID:          slotID,
		Title:       "Merge Slot",
		Description: "Exclusive access slot for serialized conflict resolution in the merge queue.",
		IssueType:   types.TypeTask,
		Status:      types.StatusOpen,
		Priority:    0,
	}
	if err := s.CreateIssue(ctx, issue, actor); err != nil {
		return nil, fmt.Errorf("merge-slot create: %w", err)
	}
	if err := s.AddLabel(ctx, slotID, mergeSlotLabel, actor); err != nil {
		// Non-fatal: label is cosmetic.
		_ = err
	}
	return s.GetIssue(ctx, slotID)
}

// MergeSlotCheckImpl is the shared implementation of Storage.MergeSlotCheck.
func MergeSlotCheckImpl(ctx context.Context, s Storage) (*MergeSlotStatus, error) {
	slotID := MergeSlotID(ctx, s)
	slot, err := s.GetIssue(ctx, slotID)
	if err != nil || slot == nil {
		return nil, fmt.Errorf("merge slot not found: %s (run 'bd merge-slot create' first): %w",
			slotID, ErrNotFound)
	}
	meta := parseSlotMeta(slot)
	return &MergeSlotStatus{
		SlotID:    slotID,
		Available: slot.Status == types.StatusOpen,
		Holder:    meta.Holder,
		Waiters:   meta.Waiters,
	}, nil
}

// MergeSlotAcquireImpl is the shared implementation of Storage.MergeSlotAcquire.
// It uses RunInTransaction to ensure atomic check-and-set, preventing two
// agents from simultaneously acquiring the slot.
func MergeSlotAcquireImpl(ctx context.Context, s Storage, holder, actor string, wait bool) (*MergeSlotResult, error) {
	if holder == "" {
		return nil, fmt.Errorf("merge-slot acquire: holder must not be empty")
	}

	slotID := MergeSlotID(ctx, s)
	var result MergeSlotResult
	result.SlotID = slotID

	err := s.RunInTransaction(ctx,
		fmt.Sprintf("bd: acquire merge slot %s for %s", slotID, holder),
		func(tx Transaction) error {
			slot, err := tx.GetIssue(ctx, slotID)
			if err != nil || slot == nil {
				return fmt.Errorf("merge slot not found: %s (run 'bd merge-slot create' first)", slotID)
			}

			meta := parseSlotMeta(slot)
			result.Holder = meta.Holder

			if slot.Status != types.StatusOpen {
				// Slot is held.
				if wait {
					alreadyWaiting := false
					for _, w := range meta.Waiters {
						if w == holder {
							alreadyWaiting = true
							break
						}
					}
					if !alreadyWaiting {
						meta.Waiters = append(meta.Waiters, holder)
					}
					metaStr, err := encodeSlotMeta(meta)
					if err != nil {
						return fmt.Errorf("failed to encode slot metadata: %w", err)
					}
					if err := tx.UpdateIssue(ctx, slot.ID, map[string]interface{}{"metadata": metaStr}, actor); err != nil {
						return fmt.Errorf("failed to add to waiters: %w", err)
					}
					result.Waiting = true
					result.Position = len(meta.Waiters)
				}
				return nil
			}

			// Slot is available — acquire it atomically.
			newMeta := slotMeta{Holder: holder, Waiters: meta.Waiters}
			metaStr, err := encodeSlotMeta(newMeta)
			if err != nil {
				return fmt.Errorf("failed to encode slot metadata: %w", err)
			}
			if err := tx.UpdateIssue(ctx, slot.ID, map[string]interface{}{
				"status":   types.StatusInProgress,
				"metadata": metaStr,
			}, actor); err != nil {
				return fmt.Errorf("failed to acquire slot: %w", err)
			}
			result.Acquired = true
			result.Holder = holder
			return nil
		},
	)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// MergeSlotReleaseImpl is the shared implementation of Storage.MergeSlotRelease.
func MergeSlotReleaseImpl(ctx context.Context, s Storage, holder, actor string) error {
	slotID := MergeSlotID(ctx, s)

	return s.RunInTransaction(ctx,
		fmt.Sprintf("bd: release merge slot %s", slotID),
		func(tx Transaction) error {
			slot, err := tx.GetIssue(ctx, slotID)
			if err != nil || slot == nil {
				return fmt.Errorf("merge slot not found: %s", slotID)
			}

			meta := parseSlotMeta(slot)

			if holder != "" && meta.Holder != holder {
				return fmt.Errorf("slot held by %s, not %s", meta.Holder, holder)
			}

			if slot.Status == types.StatusOpen {
				// Already released; idempotent.
				return nil
			}

			newMeta := slotMeta{Waiters: meta.Waiters}
			metaStr, err := encodeSlotMeta(newMeta)
			if err != nil {
				return fmt.Errorf("failed to encode slot metadata: %w", err)
			}
			return tx.UpdateIssue(ctx, slot.ID, map[string]interface{}{
				"status":   types.StatusOpen,
				"metadata": metaStr,
			}, actor)
		},
	)
}

// parseSlotMeta extracts the holder and waiters from an issue's Metadata field.
func parseSlotMeta(issue *types.Issue) slotMeta {
	var meta slotMeta
	if len(issue.Metadata) > 0 {
		_ = json.Unmarshal(issue.Metadata, &meta)
	}
	return meta
}

// encodeSlotMeta serializes slot metadata to a JSON string for storage via UpdateIssue.
func encodeSlotMeta(meta slotMeta) (string, error) {
	b, err := json.Marshal(meta)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
