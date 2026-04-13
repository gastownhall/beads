package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/timeparsing"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var (
	sendTo          string
	sendExpires     string
	sendDryRun      bool
	sendIncludeDeps bool
)

var sendCmd = &cobra.Command{
	Use:     "send <issue-id> [issue-id...]",
	GroupID: "sync",
	Short:   "Send issues to another project's inbox",
	Long: `Send one or more issues to another project's inbox for cross-project tracking.

The receiving project can review and import inbox items using 'bd inbox'.
Sends are idempotent: resending the same issue updates the inbox entry.

The --to flag specifies the target project's database name on the shared Dolt server.
Federation peer support is planned but not yet implemented.

Examples:
  bd send bd-123 --to upstream
  bd send bd-123 bd-456 --to sibling-project
  bd send bd-123 --to upstream --dry-run
  bd send bd-123 --to upstream --include-deps
  bd send bd-123 --to upstream --expires 7d`,
	Args: cobra.MinimumNArgs(1),
	Run:  runSend,
}

func init() {
	sendCmd.Flags().StringVar(&sendTo, "to", "", "target project (database name or peer name)")
	sendCmd.Flags().StringVar(&sendExpires, "expires", "", "expiry duration (e.g., 7d, +1w, 2026-12-31)")
	sendCmd.Flags().BoolVar(&sendDryRun, "dry-run", false, "preview what would be sent without writing")
	sendCmd.Flags().BoolVar(&sendIncludeDeps, "include-deps", false, "also send blocking dependencies")
	_ = sendCmd.MarkFlagRequired("to")
	sendCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(sendCmd)
}

func runSend(cmd *cobra.Command, args []string) {
	CheckReadonly("send")
	ctx := rootCtx
	s := getStore()

	// Get local project ID for sender identification
	senderProjectID, err := s.GetMetadata(ctx, "_project_id")
	if err != nil || senderProjectID == "" {
		FatalErrorRespectJSON("cannot determine local project ID: ensure database is initialized")
	}

	// Resolve issue IDs
	var issues []*types.Issue
	for _, id := range args {
		result, err := resolveAndGetIssueWithRouting(ctx, s, id)
		if err != nil {
			FatalErrorRespectJSON("issue %s: %v", id, err)
		}
		issues = append(issues, result.Issue)
	}

	// Optionally collect blocking dependencies
	if sendIncludeDeps {
		depIssues, err := collectBlockingDeps(ctx, s, issues)
		if err != nil {
			FatalErrorRespectJSON("resolving dependencies: %v", err)
		}
		issues = deduplicateIssues(append(depIssues, issues...))
	}

	// Hydrate labels for each issue
	for _, issue := range issues {
		if labels, err := s.GetLabels(ctx, issue.ID); err == nil {
			issue.Labels = labels
		}
	}

	// Build dependency map for metadata (sender_issue_id → [blocking dep sender_issue_ids])
	depMap := make(map[string][]string)
	for _, issue := range issues {
		records, err := s.GetDependencyRecords(ctx, issue.ID)
		if err != nil {
			continue
		}
		for _, dep := range records {
			if dep.Type == types.DepBlocks || dep.Type == types.DepParentChild {
				depMap[issue.ID] = append(depMap[issue.ID], dep.DependsOnID)
			}
		}
	}

	// Parse expiry if specified
	var expiresAt *time.Time
	if sendExpires != "" {
		t, err := timeparsing.ParseRelativeTime(sendExpires, time.Now())
		if err != nil {
			FatalErrorRespectJSON("invalid --expires value %q: %v", sendExpires, err)
		}
		expiresAt = &t
	}

	// Build inbox items
	items := make([]*types.InboxItem, 0, len(issues))
	for _, issue := range issues {
		item := issueToInboxItem(issue, senderProjectID, depMap)
		item.ExpiresAt = expiresAt
		items = append(items, item)
	}

	if sendDryRun {
		printSendDryRun(items)
		return
	}

	// Send to target
	sent, err := sendToTarget(ctx, s, sendTo, items)
	if err != nil {
		FatalErrorRespectJSON("send failed: %v", err)
	}

	printSendResult(sent, sendTo)
}

// issueToInboxItem converts a local issue to an inbox item for sending.
func issueToInboxItem(issue *types.Issue, senderProjectID string, depMap map[string][]string) *types.InboxItem {
	// Serialize labels to JSON
	var labelsJSON string
	if len(issue.Labels) > 0 {
		if b, err := json.Marshal(issue.Labels); err == nil {
			labelsJSON = string(b)
		}
	}

	item := &types.InboxItem{
		InboxID:         uuid.New().String(),
		SenderProjectID: senderProjectID,
		SenderIssueID:   issue.ID,
		Title:           issue.Title,
		Description:     issue.Description,
		Priority:        issue.Priority,
		IssueType:       string(issue.IssueType),
		Status:          string(issue.Status),
		Labels:          labelsJSON,
		SenderRef:       fmt.Sprintf("beads://%s/%s", senderProjectID, issue.ID),
	}
	// Encode blocking dependencies in metadata for dependency-aware import
	if deps, ok := depMap[issue.ID]; ok && len(deps) > 0 {
		depsJSON, _ := json.Marshal(map[string]interface{}{
			"blocking_deps": deps,
		})
		item.Metadata = string(depsJSON)
	}
	return item
}

// sendToTarget delegates to the store's SendToInbox method.
func sendToTarget(ctx context.Context, s storage.DoltStorage, target string, items []*types.InboxItem) (int, error) {
	return s.SendToInbox(ctx, target, items)
}

// getCurrentDatabase returns the current database name from store metadata.
func getCurrentDatabase(ctx context.Context, s storage.DoltStorage) string {
	cfg, err := s.GetConfig(ctx, "dolt_database")
	if err == nil && cfg != "" {
		return cfg
	}
	prefix, err := s.GetConfig(ctx, "issue_prefix")
	if err == nil && prefix != "" {
		return prefix
	}
	return ""
}

// collectBlockingDeps gathers all issues that block the given issues.
func collectBlockingDeps(ctx context.Context, s storage.DoltStorage, issues []*types.Issue) ([]*types.Issue, error) {
	seen := make(map[string]bool)
	for _, issue := range issues {
		seen[issue.ID] = true
	}

	var deps []*types.Issue
	for _, issue := range issues {
		blocking, err := s.GetDependencies(ctx, issue.ID)
		if err != nil {
			return nil, fmt.Errorf("getting deps for %s: %w", issue.ID, err)
		}
		for _, dep := range blocking {
			if !seen[dep.ID] {
				seen[dep.ID] = true
				deps = append(deps, dep)
			}
		}
	}
	return deps, nil
}

// deduplicateIssues removes duplicate issues by ID, preserving order.
func deduplicateIssues(issues []*types.Issue) []*types.Issue {
	seen := make(map[string]bool)
	result := make([]*types.Issue, 0, len(issues))
	for _, issue := range issues {
		if !seen[issue.ID] {
			seen[issue.ID] = true
			result = append(result, issue)
		}
	}
	return result
}

func printSendDryRun(items []*types.InboxItem) {
	if jsonOutput {
		outputJSON(items)
		return
	}
	fmt.Printf("%s Would send %d issue(s) to %s:\n", ui.RenderAccent("→"), len(items), sendTo)
	for _, item := range items {
		fmt.Printf("  %s %s (P%d, %s)\n",
			ui.RenderAccent(item.SenderIssueID),
			item.Title,
			item.Priority,
			item.IssueType,
		)
	}
}

func printSendResult(sent int, target string) {
	if jsonOutput {
		outputJSON(map[string]interface{}{
			"sent":   sent,
			"target": target,
		})
		return
	}
	fmt.Printf("%s Sent %d issue(s) to %s inbox (pending import)\n",
		ui.RenderPass("✓"), sent, target)
}
