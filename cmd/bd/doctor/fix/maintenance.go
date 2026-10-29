package fix

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

// StaleClosedIssues deletes stale closed issues.
// This is the fix handler for the "Stale Closed Issues" doctor check.
//
// This fix is DISABLED by default (stale_closed_issues_days=0). Users must
// explicitly set a positive threshold in metadata.json to enable cleanup.
func StaleClosedIssues(path string) error {
	beadsDir, err := resolvedWorkspaceBeadsDir(path)
	if err != nil {
		return err
	}

	// Load config and check if cleanup is enabled
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Get threshold; 0 means disabled
	var thresholdDays int
	if cfg != nil {
		thresholdDays = cfg.GetStaleClosedIssuesDays()
	}

	if thresholdDays == 0 {
		fmt.Println("  Stale closed issues cleanup disabled (set stale_closed_issues_days to enable)")
		return nil
	}
	warnIgnoredYAMLRetainedTypes()

	// Open database using factory to respect backend configuration (bd-m2jr:
	// SQLite fallback fix). This handler DELETES issues, so it opens through
	// the bead-mutating factory: the deletes are journaled like any other.
	ctx := context.Background()
	store, err := openBeadMutatingStore(ctx, beadsDir)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer func() { _ = store.Close() }()

	// Find closed issues older than configured threshold
	cutoff := time.Now().AddDate(0, 0, -thresholdDays)
	statusClosed := types.StatusClosed
	filter := types.IssueFilter{
		Status:       &statusClosed,
		ClosedBefore: &cutoff,
	}

	// The retained types are read from the workspace database the deletes go
	// to, on the snapshot the closed issues are read from. A read error fails
	// the fix: deleting on a guess would take beads whose ids must be retired,
	// and this fix retires none.
	issues, retainedSetting, err := store.SearchIssuesWithConfig(ctx, "", filter, issueops.SettingKeyTypesRetained)
	if err != nil {
		return fmt.Errorf("failed to query issues: %w", err)
	}
	deletable, skipped, retained := selectStaleClosed(issues, retainedSetting)

	var deleted int
	for _, issue := range deletable {
		if err := store.DeleteIssue(ctx, issue.ID); err != nil {
			fmt.Printf("  Warning: failed to delete %s: %v\n", issue.ID, err)
			continue
		}
		deleted++
	}

	if deleted == 0 && skipped == 0 && retained == 0 {
		fmt.Println("  No stale closed issues to clean up")
	} else {
		if deleted > 0 {
			fmt.Printf("  Cleaned up %d stale closed issue(s) (older than %d days)\n", deleted, thresholdDays)
		}
		if skipped > 0 {
			fmt.Printf("  Skipped %d pinned issue(s)\n", skipped)
		}
		if retained > 0 {
			fmt.Printf("  Kept %d closed issue(s) of a retained type (types.retained) — only bd prune/purge --retain-ids deletes them\n", retained)
		}
	}

	return nil
}

// selectStaleClosed narrows the stale closed issues to the ones the fix may
// delete: pinned issues are skipped, and so are issues of a type the
// workspace retains (retainedSetting is the stored value of types.retained).
// The fix deletes row by row and writes no tombstone, so a retained issue's id
// — an idempotency key — is never its to free.
func selectStaleClosed(issues []*types.Issue, retainedSetting string) (deletable []*types.Issue, pinned, retained int) {
	unpinned := make([]*types.Issue, 0, len(issues))
	for _, issue := range issues {
		if issue.Pinned {
			pinned++
			continue
		}
		unpinned = append(unpinned, issue)
	}
	deletable, retained = workapi.PartitionSweepRetainedTypes(unpinned, workapi.RetainedTypesFromSetting(retainedSetting))
	return deletable, pinned, retained
}

// warnIgnoredYAMLRetainedTypes prints config.RetainedTypesYAMLWarning, if
// there is one, before the fix selects: like the other commands that honor
// types.retained, it reads the setting from the workspace database only, so a
// key in config.yaml protects nothing from it.
func warnIgnoredYAMLRetainedTypes() {
	if msg := config.RetainedTypesYAMLWarning(); msg != "" {
		fmt.Printf("  Warning: %s\n", msg)
	}
}

// PatrolPollution deletes patrol digest and session ended beads that pollute the database.
// This is the fix handler for the "Patrol Pollution" doctor check.
//
// It removes beads matching:
// - Patrol digests: titles matching "Digest: mol-*-patrol"
// - Session ended beads: titles matching "Session ended: *"
//
// After deletion, cleans up any orphaned data.
func PatrolPollution(path string) error {
	beadsDir, err := resolvedWorkspaceBeadsDir(path)
	if err != nil {
		return err
	}

	// Open database using factory to respect backend configuration (bd-m2jr:
	// SQLite fallback fix). This handler DELETES issues, so it opens through
	// the bead-mutating factory: the deletes are journaled like any other.
	ctx := context.Background()
	store, err := openBeadMutatingStore(ctx, beadsDir)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer func() { _ = store.Close() }()

	// Get all issues and identify pollution
	ephemeral := false
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{Ephemeral: &ephemeral})
	if err != nil {
		return fmt.Errorf("failed to query issues: %w", err)
	}

	var patrolDigestCount, sessionBeadCount int
	var toDelete []string

	for _, issue := range issues {
		title := issue.Title

		// Check for patrol digest pattern: "Digest: mol-*-patrol"
		if strings.HasPrefix(title, "Digest: mol-") && strings.HasSuffix(title, "-patrol") {
			patrolDigestCount++
			toDelete = append(toDelete, issue.ID)
			continue
		}

		// Check for session ended pattern: "Session ended: *"
		if strings.HasPrefix(title, "Session ended:") {
			sessionBeadCount++
			toDelete = append(toDelete, issue.ID)
		}
	}

	if len(toDelete) == 0 {
		fmt.Println("  No patrol pollution beads to delete")
		return nil
	}

	// Delete all pollution beads
	var deleted int
	for _, id := range toDelete {
		if err := store.DeleteIssue(ctx, id); err != nil {
			fmt.Printf("  Warning: failed to delete %s: %v\n", id, err)
			continue
		}
		deleted++
	}

	// Report results
	if patrolDigestCount > 0 {
		fmt.Printf("  Deleted %d patrol digest bead(s)\n", patrolDigestCount)
	}
	if sessionBeadCount > 0 {
		fmt.Printf("  Deleted %d session ended bead(s)\n", sessionBeadCount)
	}
	fmt.Printf("  Total: %d pollution bead(s) removed\n", deleted)

	return nil
}
