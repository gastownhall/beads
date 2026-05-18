package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/routing"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var migratePersonalCmd = &cobra.Command{
	Use:   "migrate-personal",
	Short: "Move personal planning issues from the project database to your planning repo",
	Long: `Identify issues you created in the project database and move them to your
personal planning repository (~/.beads-planning by default).

This is a one-time migration for contributors who created personal planning
issues before contributor routing was configured.

The command:
  1. Finds all issues in the project database created by your git identity
  2. Shows you the list and asks for confirmation
  3. Moves them to the planning repo configured in routing.contributor

EXAMPLES:
  bd migrate-personal        # Interactive: show list and prompt
  bd migrate-personal -y     # Non-interactive: skip confirmation`,
	GroupID: "setup",
	RunE:    runMigratePersonal,
}

var migratePersonalYes bool

func init() {
	migratePersonalCmd.Flags().BoolVarP(&migratePersonalYes, "yes", "y", false, "Skip confirmation prompt")
	rootCmd.AddCommand(migratePersonalCmd)
}

func runMigratePersonal(cmd *cobra.Command, args []string) error {
	ctx := rootCtx

	identity := getActorWithGit()

	// Find personal issues in project DB
	filter := types.IssueFilter{Limit: 0}
	allIssues, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		return fmt.Errorf("failed to search issues: %w", err)
	}

	var personal []*types.Issue
	for _, issue := range allIssues {
		if issue.CreatedBy == identity {
			personal = append(personal, issue)
		}
	}

	if len(personal) == 0 {
		fmt.Printf("Nothing to migrate — no issues in the project database were created by %s\n", ui.RenderAccent(identity))
		return nil
	}

	// Show list
	fmt.Printf("\n%s personal issues created by %s found in project database:\n\n",
		ui.RenderBold(fmt.Sprintf("%d", len(personal))), ui.RenderAccent(identity))
	for _, issue := range personal {
		fmt.Printf("  %s  %s\n", ui.RenderAccent(issue.ID), issue.Title)
	}
	fmt.Println()

	// Get planning repo path
	planningRaw := getRoutingConfigValue(ctx, store, "routing.contributor")
	if planningRaw == "" {
		// Backward compat
		planningRaw = getRoutingConfigValue(ctx, store, "contributor.planning_repo")
	}
	if planningRaw == "" {
		return fmt.Errorf("no planning repository configured; run 'bd init --contributor' or 'bd init' in a fork repo first")
	}
	planningPath := routing.ExpandPath(planningRaw)

	// Validate planning DB path before any destructive operation
	planningBeadsDir := filepath.Join(planningPath, ".beads")
	if _, err := os.Stat(planningBeadsDir); os.IsNotExist(err) {
		return fmt.Errorf("planning repo at %s has no .beads directory; initialize it first with 'bd init'", planningPath)
	}

	fmt.Printf("Move to planning repo: %s\n\n", ui.RenderAccent(planningPath))

	// Prompt for confirmation
	if !migratePersonalYes {
		fmt.Printf("Move %d issues? [Y/n]: ", len(personal))
		reader := bufio.NewReader(os.Stdin)
		response, err := readLineWithContext(ctx, reader, os.Stdin)
		if err != nil {
			if isCanceled(err) {
				fmt.Fprintln(os.Stderr, "Aborted.")
				return nil
			}
			response = ""
		}
		response = strings.TrimSpace(strings.ToLower(response))
		if response == "n" || response == "no" {
			fmt.Println("Aborted — no changes made.")
			return nil
		}
	}

	// Open planning store (writable)
	planningDBPath := doltserver.ResolveDoltDir(planningBeadsDir)
	planningStore, err := dolt.New(ctx, &dolt.Config{
		Path:     planningDBPath,
		BeadsDir: planningBeadsDir,
	})
	if err != nil {
		return fmt.Errorf("failed to open planning store at %s: %w", planningPath, err)
	}
	defer func() { _ = planningStore.Close() }()

	// Phase 1: Copy all issues to planning DB. No deletes yet — if any copy
	// fails we abort before touching the project DB.
	for _, issue := range personal {
		if err := copyIssueToPlanningDB(ctx, store, planningStore, issue, identity); err != nil {
			return fmt.Errorf("failed to copy issue %s to planning DB: %w", issue.ID, err)
		}
	}

	// Phase 2: Delete all from project DB in a single transaction.
	ids := make([]string, len(personal))
	for i, issue := range personal {
		ids[i] = issue.ID
	}
	if _, err := store.DeleteIssues(ctx, ids, false, false, false); err != nil {
		return fmt.Errorf("failed to delete migrated issues from project DB: %w", err)
	}

	fmt.Printf("\n%s Moved %d %s to %s\n",
		ui.RenderPass("✓"),
		len(personal),
		pluralIssue(len(personal)),
		ui.RenderAccent(planningPath))
	return nil
}

// copyIssueToPlanningDB copies issue (with labels, deps, comments) to dst. Does not delete from src.
func copyIssueToPlanningDB(ctx context.Context, src, dst storage.DoltStorage, issue *types.Issue, actor string) error {
	if err := dst.CreateIssue(ctx, issue, actor); err != nil {
		return fmt.Errorf("insert into planning DB: %w", err)
	}

	// Copy labels
	labels, err := src.GetLabels(ctx, issue.ID)
	if err == nil {
		for _, label := range labels {
			if addErr := dst.AddLabel(ctx, issue.ID, label, actor); addErr != nil {
				// Non-fatal: log and continue
				fmt.Fprintf(os.Stderr, "Warning: failed to copy label %q for %s: %v\n", label, issue.ID, addErr)
			}
		}
	}

	// Copy dependency records
	deps, err := src.GetDependencyRecords(ctx, issue.ID)
	if err == nil {
		for _, dep := range deps {
			if addErr := dst.AddDependency(ctx, dep, actor); addErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to copy dependency %s→%s: %v\n", dep.IssueID, dep.DependsOnID, addErr)
			}
		}
	}

	// Copy comments
	comments, err := src.GetIssueComments(ctx, issue.ID)
	if err == nil {
		for _, c := range comments {
			if addErr := dst.AddComment(ctx, issue.ID, c.Author, c.Text); addErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to copy comment for %s: %v\n", issue.ID, addErr)
			}
		}
	}

	return nil
}

func pluralIssue(n int) string {
	if n == 1 {
		return "issue"
	}
	return "issues"
}
