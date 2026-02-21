package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

type doltAutoCommitParams struct {
	// Command is the top-level bd command name (e.g., "create", "update").
	Command string
	// IssueIDs are the primary issue IDs affected by the command (optional).
	IssueIDs []string
	// MessageOverride, if non-empty, is used verbatim.
	MessageOverride string
}

// maybeAutoCommit creates a Dolt commit after a successful write command when enabled.
//
// Semantics:
// - Only applies when dolt auto-commit is "on" AND the active store is versioned (Dolt).
// - In "batch" mode, commits are deferred — changes accumulate in the working set
//   until an explicit commit point (bd sync, bd dolt commit).
// - Uses Dolt's "commit all" behavior under the hood (DOLT_COMMIT -Am).
// - Treats "nothing to commit" as a no-op.
func maybeAutoCommit(ctx context.Context, p doltAutoCommitParams) error {
	mode, err := getDoltAutoCommitMode()
	if err != nil {
		return err
	}
	// In batch mode, skip per-command commits. Changes stay in the working set
	// and are committed at logical boundaries (bd sync, bd dolt commit).
	if mode != doltAutoCommitOn {
		return nil
	}

	st := getStore()
	if st == nil {
		return nil
	}

	msg := p.MessageOverride
	if strings.TrimSpace(msg) == "" {
		msg = formatDoltAutoCommitMessage(p.Command, getActor(), p.IssueIDs)
	}

	if err := st.Commit(ctx, msg); err != nil {
		if isDoltNothingToCommit(err) {
			return nil
		}
		return err
	}
	return nil
}

func isDoltNothingToCommit(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	// Dolt commonly reports "nothing to commit".
	if strings.Contains(s, "nothing to commit") {
		return true
	}
	// Some versions/paths may report "no changes".
	if strings.Contains(s, "no changes") && strings.Contains(s, "commit") {
		return true
	}
	return false
}

func formatDoltAutoCommitMessage(cmd string, actor string, issueIDs []string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		cmd = "write"
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		actor = "unknown"
	}

	ids := make([]string, 0, len(issueIDs))
	seen := make(map[string]bool, len(issueIDs))
	for _, id := range issueIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	slices.Sort(ids)

	const maxIDs = 5
	if len(ids) > maxIDs {
		ids = ids[:maxIDs]
	}

	if len(ids) == 0 {
		return fmt.Sprintf("bd: %s (auto-commit) by %s", cmd, actor)
	}
	return fmt.Sprintf("bd: %s (auto-commit) by %s [%s]", cmd, actor, strings.Join(ids, ", "))
}
