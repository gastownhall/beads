package linear

import (
	"context"
	"fmt"
	"strings"
)

// RollbackMutation describes a compensating operation to undo a sync item.
type RollbackMutation struct {
	BeadID    string            `json:"bead_id"`
	LinearID  string            `json:"linear_id,omitempty"`
	Action    string            `json:"action"`
	Direction string            `json:"direction"`
	Fields    map[string]string `json:"fields,omitempty"`
}

// GenerateRollbackMutations reads sync history for a given run and produces
// compensating mutations that would undo the sync's effects.
//
// For created issues: generates a delete mutation.
// For updated issues: generates an update mutation restoring before_values.
// For failed/skipped items: no mutation needed.
func GenerateRollbackMutations(ctx context.Context, histDB *SyncHistoryDB, syncRunID string) ([]RollbackMutation, error) {
	run, err := histDB.GetSyncRun(ctx, syncRunID)
	if err != nil {
		return nil, fmt.Errorf("fetching sync run: %w", err)
	}
	if run == nil {
		return nil, fmt.Errorf("sync run %s not found", syncRunID)
	}

	items, err := histDB.GetSyncRunItems(ctx, syncRunID)
	if err != nil {
		return nil, fmt.Errorf("fetching sync items: %w", err)
	}

	var mutations []RollbackMutation
	for _, item := range items {
		switch item.Outcome {
		case "created":
			mutations = append(mutations, rollbackCreated(item))
		case "updated":
			if m := rollbackUpdated(item); m != nil {
				mutations = append(mutations, *m)
			}
		}
	}
	return mutations, nil
}

// RollbackScript generates a human-readable script (bd commands) that would
// undo the effects of a sync run.
func RollbackScript(mutations []RollbackMutation) string {
	if len(mutations) == 0 {
		return "# No rollback actions needed for this sync run.\n"
	}

	var b strings.Builder
	b.WriteString("#!/bin/bash\n")
	b.WriteString("# Rollback script generated from sync history.\n")
	b.WriteString("# Review each command before executing.\n\n")

	for _, m := range mutations {
		b.WriteString(fmt.Sprintf("# %s: %s (direction: %s)\n", m.Action, m.BeadID, m.Direction))
		switch m.Action {
		case "delete_local":
			b.WriteString(fmt.Sprintf("bd delete %s\n", m.BeadID))
		case "restore_local":
			b.WriteString(fmt.Sprintf("bd update %s", m.BeadID))
			for k, v := range m.Fields {
				b.WriteString(fmt.Sprintf(" --%s=%q", k, v))
			}
			b.WriteString("\n")
		case "delete_remote":
			b.WriteString(fmt.Sprintf("# Manual action: delete issue %s from Linear\n", m.LinearID))
		case "restore_remote":
			b.WriteString(fmt.Sprintf("# Manual action: restore issue %s in Linear to previous state\n", m.LinearID))
			for k, v := range m.Fields {
				b.WriteString(fmt.Sprintf("#   %s = %q\n", k, v))
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func rollbackCreated(item SyncItem) RollbackMutation {
	if item.Direction == "pull" {
		return RollbackMutation{
			BeadID:    item.BeadID,
			LinearID:  item.LinearID,
			Action:    "delete_local",
			Direction: item.Direction,
		}
	}
	return RollbackMutation{
		BeadID:    item.BeadID,
		LinearID:  item.LinearID,
		Action:    "delete_remote",
		Direction: item.Direction,
	}
}

func rollbackUpdated(item SyncItem) *RollbackMutation {
	if len(item.BeforeValues) == 0 {
		return nil
	}
	if item.Direction == "pull" {
		return &RollbackMutation{
			BeadID:    item.BeadID,
			LinearID:  item.LinearID,
			Action:    "restore_local",
			Direction: item.Direction,
			Fields:    item.BeforeValues,
		}
	}
	return &RollbackMutation{
		BeadID:    item.BeadID,
		LinearID:  item.LinearID,
		Action:    "restore_remote",
		Direction: item.Direction,
		Fields:    item.BeforeValues,
	}
}
