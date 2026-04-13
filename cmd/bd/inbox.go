package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var inboxDryRun bool

var inboxCmd = &cobra.Command{
	Use:     "inbox",
	GroupID: "sync",
	Short:   "Manage cross-project inbox items",
	Long: `View and manage issues sent from other projects.

Other projects can send issues to your inbox using 'bd send'. Use this
command to list, import, reject, or clean up inbox items.

Examples:
  bd inbox                        # list pending inbox items
  bd inbox list                   # same as above
  bd inbox import                 # import all pending items as real issues
  bd inbox import <inbox-id>      # import specific item
  bd inbox reject <inbox-id>      # reject an item with reason
  bd inbox clean                  # remove processed items`,
	Run: runInboxList,
}

var inboxListCmd = &cobra.Command{
	Use:   "list",
	Short: "List pending inbox items",
	Run:   runInboxList,
}

var inboxImportCmd = &cobra.Command{
	Use:   "import [inbox-id]",
	Short: "Import inbox items as real issues",
	Long: `Import pending inbox items into your issue database.

Without arguments, imports all pending items. With an inbox-id argument,
imports only that specific item. Parent issues are imported first when
dependencies are present.

Examples:
  bd inbox import                 # import all pending
  bd inbox import abc-123         # import specific item`,
	Run: runInboxImport,
}

var inboxRejectCmd = &cobra.Command{
	Use:   "reject <inbox-id> [reason]",
	Short: "Reject an inbox item",
	Args:  cobra.RangeArgs(1, 2),
	Run:   runInboxReject,
}

var inboxCleanCmd = &cobra.Command{
	Use:   "clean",
	Short: "Remove imported, rejected, and expired inbox items",
	Run:   runInboxClean,
}

var inboxWatchInterval int

var inboxWatchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Poll inbox for new items and log notifications",
	Long: `Run a persistent polling loop that checks for pending inbox items
and logs when new items arrive. Useful for long-running server deployments
where agents may not call bd ready frequently.

The loop runs until interrupted (Ctrl-C). Use --interval to control the
polling frequency in seconds (default 60).

Examples:
  bd inbox watch                  # poll every 60s
  bd inbox watch --interval 30    # poll every 30s`,
	Run: runInboxWatch,
}

func init() {
	inboxCmd.AddCommand(inboxListCmd)
	inboxCmd.AddCommand(inboxImportCmd)
	inboxCmd.AddCommand(inboxRejectCmd)
	inboxCmd.AddCommand(inboxCleanCmd)
	inboxCmd.AddCommand(inboxWatchCmd)
	inboxCleanCmd.Flags().BoolVar(&inboxDryRun, "dry-run", false, "preview what would be cleaned")
	inboxWatchCmd.Flags().IntVar(&inboxWatchInterval, "interval", 60, "polling interval in seconds")
	rootCmd.AddCommand(inboxCmd)
}

func runInboxList(cmd *cobra.Command, args []string) {
	ctx := rootCtx
	s := getStore()

	items, err := s.GetPendingInboxItems(ctx)
	if err != nil {
		FatalErrorRespectJSON("failed to list inbox: %v", err)
	}

	if jsonOutput {
		outputJSON(items)
		return
	}

	if len(items) == 0 {
		fmt.Println("No pending inbox items.")
		return
	}

	fmt.Printf("%s %d pending inbox item(s):\n\n", ui.RenderAccent("📬"), len(items))
	for _, item := range items {
		age := time.Since(item.CreatedAt).Truncate(time.Minute)
		fmt.Printf("  %s  %s\n", ui.RenderMuted(item.InboxID[:8]), item.Title)
		fmt.Printf("       From: %s/%s  P%d %s  %s ago\n",
			item.SenderProjectID, item.SenderIssueID,
			item.Priority, item.IssueType,
			inboxFormatDuration(age),
		)
	}
}

func runInboxImport(cmd *cobra.Command, args []string) {
	CheckReadonly("inbox import")
	ctx := rootCtx
	s := getStore()
	actor := getActor()

	var items []*types.InboxItem
	var err error

	if len(args) > 0 {
		// Import specific item
		item, err := s.GetInboxItem(ctx, args[0])
		if err != nil {
			FatalErrorRespectJSON("inbox item %s: %v", args[0], err)
		}
		if item.ImportedAt != nil {
			FatalErrorRespectJSON("inbox item %s already imported as %s", args[0], item.ImportedIssueID)
		}
		if item.RejectedAt != nil {
			FatalErrorRespectJSON("inbox item %s was rejected", args[0])
		}
		items = []*types.InboxItem{item}
	} else {
		items, err = s.GetPendingInboxItems(ctx)
		if err != nil {
			FatalErrorRespectJSON("failed to list inbox: %v", err)
		}
	}

	if len(items) == 0 {
		if jsonOutput {
			outputJSON(map[string]interface{}{"imported": 0})
		} else {
			fmt.Println("No pending inbox items to import.")
		}
		return
	}

	imported := importInboxItems(ctx, s, items, actor)

	if jsonOutput {
		outputJSON(map[string]interface{}{
			"imported": len(imported),
			"issues":   imported,
		})
		return
	}

	// Group by sender for summary
	bySender := make(map[string]int)
	for _, item := range imported {
		bySender[item.SenderProjectID]++
	}
	parts := make([]string, 0, len(bySender))
	for proj, count := range bySender {
		parts = append(parts, fmt.Sprintf("%d from %s", count, proj))
	}
	fmt.Printf("%s Imported %d issue(s) from inbox (%s)\n",
		ui.RenderPass("✓"), len(imported), joinParts(parts))
}

// importInboxItems creates real issues from inbox items and marks them imported.
// Items are topologically sorted so dependencies are imported before dependents.
// Duplicate imports are prevented by checking for existing issues with matching sender ref.
func importInboxItems(ctx context.Context, s storage.DoltStorage, items []*types.InboxItem, actor string) []*types.InboxItem {
	// Topological sort: items whose blocking deps are satisfied first
	sorted := topoSortInboxItems(items)

	var imported []*types.InboxItem
	for _, item := range sorted {
		// Duplicate prevention: skip if an issue with this sender ref already exists
		senderRef := fmt.Sprintf("beads://%s/%s", item.SenderProjectID, item.SenderIssueID)
		if existing, err := s.GetIssueByExternalRef(ctx, senderRef); err == nil && existing != nil {
			// Already imported — just mark the inbox item
			if err := s.MarkInboxItemImported(ctx, item.InboxID, existing.ID); err != nil {
				fmt.Fprintf(os.Stderr, "  %s warning: failed to mark duplicate %s: %v\n",
					ui.RenderWarn("⚠"), item.SenderIssueID, err)
			}
			if !jsonOutput {
				fmt.Printf("  %s %s already imported as %s (skipped)\n",
					ui.RenderMuted("–"), item.SenderIssueID, existing.ID)
			}
			continue
		}

		issue := inboxItemToIssue(item)
		if err := s.CreateIssue(ctx, issue, actor); err != nil {
			fmt.Printf("  %s Failed to import %s: %v\n", ui.RenderFail("✗"), item.SenderIssueID, err)
			continue
		}
		if err := s.MarkInboxItemImported(ctx, item.InboxID, issue.ID); err != nil {
			fmt.Printf("  %s Imported %s as %s but failed to update inbox: %v\n",
				ui.RenderWarn("⚠"), item.SenderIssueID, issue.ID, err)
		}
		item.ImportedIssueID = issue.ID
		imported = append(imported, item)

		if !jsonOutput {
			fmt.Printf("  %s %s → %s (%s)\n",
				ui.RenderPass("✓"), item.SenderIssueID, issue.ID, issue.Title)
		}
	}
	return imported
}

// topoSortInboxItems sorts inbox items so that dependencies come before dependents.
// Uses metadata.blocking_deps to determine ordering.
func topoSortInboxItems(items []*types.InboxItem) []*types.InboxItem {
	if len(items) <= 1 {
		return items
	}

	// Build index: sender_issue_id → item
	byIssueID := make(map[string]*types.InboxItem, len(items))
	for _, item := range items {
		byIssueID[item.SenderIssueID] = item
	}

	// Parse dependency edges from metadata
	depEdges := make(map[string][]string) // issue_id → [dep_issue_ids in this batch]
	for _, item := range items {
		if item.Metadata == "" || item.Metadata == "{}" {
			continue
		}
		var meta struct {
			BlockingDeps []string `json:"blocking_deps"`
		}
		if err := json.Unmarshal([]byte(item.Metadata), &meta); err != nil {
			continue
		}
		for _, depID := range meta.BlockingDeps {
			if _, inBatch := byIssueID[depID]; inBatch {
				depEdges[item.SenderIssueID] = append(depEdges[item.SenderIssueID], depID)
			}
		}
	}

	// Kahn's algorithm for topological sort
	inDegree := make(map[string]int, len(items))
	for _, item := range items {
		inDegree[item.SenderIssueID] = 0
	}
	for id, deps := range depEdges {
		inDegree[id] = len(deps)
	}

	var queue []string
	for _, item := range items {
		if inDegree[item.SenderIssueID] == 0 {
			queue = append(queue, item.SenderIssueID)
		}
	}

	var sorted []*types.InboxItem
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		sorted = append(sorted, byIssueID[id])

		// Find items that depend on this one
		for depID, deps := range depEdges {
			for _, d := range deps {
				if d == id {
					inDegree[depID]--
					if inDegree[depID] == 0 {
						queue = append(queue, depID)
					}
				}
			}
		}
	}

	// Append any items not reached (circular deps) in original order
	inSorted := make(map[string]bool, len(sorted))
	for _, item := range sorted {
		inSorted[item.SenderIssueID] = true
	}
	for _, item := range items {
		if !inSorted[item.SenderIssueID] {
			sorted = append(sorted, item)
		}
	}

	return sorted
}

// inboxItemToIssue converts an inbox item into a new local issue.
func inboxItemToIssue(item *types.InboxItem) *types.Issue {
	return &types.Issue{
		Title:       item.Title,
		Description: item.Description,
		Priority:    item.Priority,
		IssueType:   types.IssueType(item.IssueType),
		Status:      types.StatusOpen,
		ExternalRef: func() *string { s := item.SenderRef; return &s }(),
		SourceRepo:  item.SenderProjectID,
	}
}

func runInboxReject(cmd *cobra.Command, args []string) {
	CheckReadonly("inbox reject")
	ctx := rootCtx
	s := getStore()

	inboxID := args[0]
	reason := "rejected"
	if len(args) > 1 {
		reason = args[1]
	}

	item, err := s.GetInboxItem(ctx, inboxID)
	if err != nil {
		FatalErrorRespectJSON("inbox item %s: %v", inboxID, err)
	}
	if item.ImportedAt != nil {
		FatalErrorRespectJSON("inbox item %s already imported as %s", inboxID, item.ImportedIssueID)
	}

	if err := s.MarkInboxItemRejected(ctx, inboxID, reason); err != nil {
		FatalErrorRespectJSON("failed to reject: %v", err)
	}

	if jsonOutput {
		outputJSON(map[string]interface{}{
			"inbox_id": inboxID,
			"rejected": true,
			"reason":   reason,
		})
	} else {
		fmt.Printf("%s Rejected inbox item %s (%s)\n",
			ui.RenderPass("✓"), ui.RenderMuted(inboxID[:8]), reason)
	}
}

func runInboxClean(cmd *cobra.Command, args []string) {
	CheckReadonly("inbox clean")
	ctx := rootCtx
	s := getStore()

	if inboxDryRun {
		// For dry-run, just count what would be cleaned
		count, err := countCleanableInbox(ctx, s)
		if err != nil {
			FatalErrorRespectJSON("failed to count: %v", err)
		}
		if jsonOutput {
			outputJSON(map[string]interface{}{"would_remove": count})
		} else {
			fmt.Printf("Would remove %d processed inbox item(s)\n", count)
		}
		return
	}

	removed, err := s.CleanInbox(ctx)
	if err != nil {
		FatalErrorRespectJSON("failed to clean inbox: %v", err)
	}

	if jsonOutput {
		outputJSON(map[string]interface{}{"removed": removed})
	} else {
		fmt.Printf("%s Cleaned %d inbox item(s)\n", ui.RenderPass("✓"), removed)
	}
}

// countCleanableInbox counts items that would be removed by clean.
func countCleanableInbox(ctx context.Context, s storage.DoltStorage) (int64, error) {
	rawDB, ok := s.(storage.RawDBAccessor)
	if !ok {
		return 0, fmt.Errorf("raw DB access unavailable")
	}
	var count int64
	err := rawDB.UnderlyingDB().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM beads_inbox
		WHERE imported_at IS NOT NULL
		   OR rejected_at IS NOT NULL
		   OR (expires_at IS NOT NULL AND expires_at <= NOW())
	`).Scan(&count)
	return count, err
}

// inboxFormatDuration formats a duration into a human-readable string.
func inboxFormatDuration(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// joinParts joins string parts with commas.
func joinParts(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	result := parts[0]
	for _, p := range parts[1:] {
		result += ", " + p
	}
	return result
}

func runInboxWatch(cmd *cobra.Command, args []string) {
	ctx := rootCtx
	s := getStore()

	if inboxWatchInterval < 1 {
		FatalErrorRespectJSON("interval must be at least 1 second")
	}

	interval := time.Duration(inboxWatchInterval) * time.Second
	fmt.Printf("%s Watching inbox (polling every %s, Ctrl-C to stop)\n",
		ui.RenderAccent("📬"), interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var lastCount int64

	// Check immediately on start
	checkInbox(ctx, s, &lastCount)

	for {
		select {
		case <-ticker.C:
			checkInbox(ctx, s, &lastCount)
		case <-ctx.Done():
			fmt.Println("\nInbox watch stopped.")
			return
		}
	}
}

func checkInbox(ctx context.Context, s storage.DoltStorage, lastCount *int64) {
	count, err := s.CountPendingInbox(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s inbox poll error: %v\n",
			ui.RenderWarn("⚠"), err)
		return
	}

	if count > 0 && count != *lastCount {
		now := time.Now().Format("15:04:05")
		fmt.Printf("[%s] %s %d issue(s) pending in inbox\n",
			ui.RenderMuted(now), ui.RenderAccent("📬"), count)
	}
	*lastCount = count
}
