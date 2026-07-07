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

	// Phase 1: Copy everything to the planning DB before touching the project
	// DB. The copy is all-or-error — any failed step rolls back the planning-DB
	// rows we created and aborts without deleting anything from the project DB,
	// so a partial copy can never be mistaken for success and lose data
	// (maphew review, be-e2nb).
	//
	// Phase 1a: create every issue row first, so same-prefix dependencies
	// between two migrated issues resolve in phase 1b regardless of copy order.
	var created []string
	rollback := func() {
		if len(created) > 0 {
			_, _ = planningStore.DeleteIssues(ctx, created, false, false, false)
		}
	}
	for _, issue := range personal {
		if err := planningStore.CreateIssue(ctx, issue, identity); err != nil {
			rollback()
			return fmt.Errorf("failed to copy issue %s to planning DB: %w", issue.ID, err)
		}
		created = append(created, issue.ID)
	}

	// Phase 1b: copy labels, dependency records, and comments for every issue.
	for _, issue := range personal {
		if err := copyIssueRelations(ctx, store, planningStore, issue, identity); err != nil {
			rollback()
			return fmt.Errorf("failed to copy %s to planning DB: %w", issue.ID, err)
		}
	}

	// Phase 2: Delete all from project DB in a single transaction. The planning
	// copy is complete at this point. If the delete fails, the planning repo
	// already holds the good copy and the project DB is untouched, so we tell
	// the user to resolve the duplicate rather than silently losing data.
	ids := make([]string, len(personal))
	for i, issue := range personal {
		ids[i] = issue.ID
	}
	if _, err := store.DeleteIssues(ctx, ids, false, false, false); err != nil {
		return fmt.Errorf("issues copied to planning repo %s but deleting them from the project DB failed; resolve the duplicate manually: %w", planningPath, err)
	}

	fmt.Printf("\n%s Moved %d %s to %s\n",
		ui.RenderPass("✓"),
		len(personal),
		pluralIssue(len(personal)),
		ui.RenderAccent(planningPath))
	return nil
}

// copyIssueRelations copies the labels, dependency records, and comments for a
// single issue from src to dst. The issue row itself must already exist in dst
// (created in phase 1a of runMigratePersonal). Every step — including the reads
// from src — is fatal on error so the caller can abort the migration before any
// source data is deleted; a partial copy must never be mistaken for success
// (maphew review, be-e2nb).
func copyIssueRelations(ctx context.Context, src, dst storage.DoltStorage, issue *types.Issue, actor string) error {
	labels, err := src.GetLabels(ctx, issue.ID)
	if err != nil {
		return fmt.Errorf("read labels for %s: %w", issue.ID, err)
	}
	for _, label := range labels {
		if err := dst.AddLabel(ctx, issue.ID, label, actor); err != nil {
			return fmt.Errorf("copy label %q for %s: %w", label, issue.ID, err)
		}
	}

	deps, err := src.GetDependencyRecords(ctx, issue.ID)
	if err != nil {
		return fmt.Errorf("read dependencies for %s: %w", issue.ID, err)
	}
	for _, dep := range deps {
		if err := dst.AddDependency(ctx, dep, actor); err != nil {
			return fmt.Errorf("copy dependency %s→%s: %w", dep.IssueID, dep.DependsOnID, err)
		}
	}

	comments, err := src.GetIssueComments(ctx, issue.ID)
	if err != nil {
		return fmt.Errorf("read comments for %s: %w", issue.ID, err)
	}
	for _, c := range comments {
		// ImportIssueComment writes a structured comment row preserving the
		// original author and created_at. AddComment would instead record an
		// event-style entry that GetIssueComments never returns, silently
		// dropping the comment from the migrated issue (maphew review, be-e2nb).
		if _, err := dst.ImportIssueComment(ctx, issue.ID, c.Author, c.Text, c.CreatedAt); err != nil {
			return fmt.Errorf("copy comment for %s: %w", issue.ID, err)
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
