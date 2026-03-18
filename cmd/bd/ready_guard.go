package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// isCrewActor returns true if the actor path indicates a crew role.
// Crew actors follow the pattern: <rig>/crew/<name>
// e.g., "gastown/crew/zhora", "cfutons/crew/melania"
func isCrewActor(actor string) bool {
	parts := strings.Split(actor, "/")
	return len(parts) >= 2 && parts[len(parts)-2] == "crew"
}

// getInProgressBeads returns in_progress beads assigned to actorName.
// Returns nil slice on any error (best-effort: guard should not break the command).
func getInProgressBeads(ctx context.Context, s storage.Storage, actorName string) []*types.Issue {
	assignee := actorName
	work, err := s.GetReadyWork(ctx, types.WorkFilter{
		Status:   types.StatusInProgress,
		Assignee: &assignee,
	})
	if err != nil {
		return nil
	}
	return work
}

// applyReadyGuard enforces the concurrent-work guard for bd ready.
//
//   - Crew agents (path contains /crew/): hard block — prints error and exits non-zero.
//   - Polecat agents: soft warn — prints warning, then continues showing results.
//
// If allowConcurrent is true the guard is bypassed entirely.
func applyReadyGuard(ctx context.Context, s storage.Storage, actorName string, allowConcurrent bool) {
	if allowConcurrent || actorName == "" {
		return
	}
	inProgress := getInProgressBeads(ctx, s, actorName)
	if len(inProgress) == 0 {
		return
	}
	firstID := inProgress[0].ID
	msg := fmt.Sprintf("You have open work: %s. Finish or hand off before claiming more.", firstID)
	if isCrewActor(actorName) {
		fmt.Fprintf(os.Stderr, "%s %s\n", ui.RenderFail("✗"), msg)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "%s %s\n\n", ui.RenderWarn("⚠"), msg)
}

// applyClaimGuard enforces the concurrent-work guard for bd update --claim.
//
//   - Crew agents: hard block — returns an error (caller prints and continues to next issue).
//   - Polecat agents: soft warn — prints warning, then allows claim.
//
// If allowConcurrent is true the guard is bypassed entirely.
// Returns an error string to print and skip (non-nil = do not claim), or "" to continue.
func applyClaimGuard(ctx context.Context, s storage.Storage, actorName string, allowConcurrent bool) string {
	if allowConcurrent || actorName == "" {
		return ""
	}
	inProgress := getInProgressBeads(ctx, s, actorName)
	if len(inProgress) == 0 {
		return ""
	}
	firstID := inProgress[0].ID
	msg := fmt.Sprintf("You have open work: %s. Finish or hand off before claiming more.", firstID)
	if isCrewActor(actorName) {
		return msg // Caller treats non-empty return as hard block
	}
	fmt.Fprintf(os.Stderr, "%s %s\n", ui.RenderWarn("⚠"), msg)
	return "" // Warn but allow claim
}
