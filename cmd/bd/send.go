package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// validDBName matches safe Dolt database identifiers.
var validDBName = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_\-]*$`)

var (
	sendTo          string
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

The --to flag specifies the target project. On a shared Dolt server, this is
a database name. For federation peers, this is a configured peer name.

Examples:
  bd send bd-123 --to upstream
  bd send bd-123 bd-456 --to sibling-project
  bd send bd-123 --to upstream --dry-run
  bd send bd-123 --to upstream --include-deps`,
	Args: cobra.MinimumNArgs(1),
	Run:  runSend,
}

func init() {
	sendCmd.Flags().StringVar(&sendTo, "to", "", "target project (database name or peer name)")
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

	// Build inbox items
	items := make([]*types.InboxItem, 0, len(issues))
	for _, issue := range issues {
		items = append(items, issueToInboxItem(issue, senderProjectID, depMap))
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
	item := &types.InboxItem{
		InboxID:         uuid.New().String(),
		SenderProjectID: senderProjectID,
		SenderIssueID:   issue.ID,
		Title:           issue.Title,
		Description:     issue.Description,
		Priority:        issue.Priority,
		IssueType:       string(issue.IssueType),
		Status:          string(issue.Status),
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

// sendToTarget writes inbox items to the target project.
// Currently supports shared-server mode via fully-qualified cross-database INSERT.
func sendToTarget(ctx context.Context, s storage.DoltStorage, target string, items []*types.InboxItem) (int, error) {
	// Validate target database name to prevent SQL injection
	if !validDBName.MatchString(target) {
		return 0, fmt.Errorf("invalid target database name %q: must match [a-zA-Z0-9_-]", target)
	}

	rawDB, ok := s.(storage.RawDBAccessor)
	if !ok {
		return 0, fmt.Errorf("send requires shared server mode; raw DB access unavailable")
	}

	// Acquire a dedicated connection to avoid polluting the connection pool
	conn, err := rawDB.UnderlyingDB().Conn(ctx)
	if err != nil {
		return 0, fmt.Errorf("acquiring connection: %w", err)
	}
	defer conn.Close()

	// Remember original database and ensure we restore it on all exit paths
	origDB := getCurrentDatabase(ctx, s)
	if !validDBName.MatchString(origDB) {
		return 0, fmt.Errorf("invalid current database name %q", origDB)
	}

	// Switch to target database on this connection
	if _, err := conn.ExecContext(ctx, "USE `"+target+"`"); err != nil {
		return 0, fmt.Errorf("cannot access target database %q: %w (is the Dolt server shared?)", target, err)
	}
	defer func() {
		// Always restore original database context
		_, _ = conn.ExecContext(ctx, "USE `"+origDB+"`")
	}()

	// Insert all items, then commit once
	sent := 0
	for _, item := range items {
		// Default JSON columns to valid JSON
		labels := item.Labels
		if labels == "" {
			labels = "[]"
		}
		metadata := item.Metadata
		if metadata == "" {
			metadata = "{}"
		}

		_, err = conn.ExecContext(ctx, `
			INSERT INTO beads_inbox (
				inbox_id, sender_project_id, sender_issue_id, title, description,
				priority, issue_type, status, labels, metadata, sender_ref
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE
				title = VALUES(title),
				description = VALUES(description),
				priority = VALUES(priority),
				issue_type = VALUES(issue_type),
				status = VALUES(status),
				labels = VALUES(labels),
				metadata = VALUES(metadata),
				sender_ref = VALUES(sender_ref)
		`,
			item.InboxID,
			item.SenderProjectID,
			item.SenderIssueID,
			item.Title,
			item.Description,
			item.Priority,
			item.IssueType,
			item.Status,
			labels,
			metadata,
			item.SenderRef,
		)
		if err != nil {
			return sent, fmt.Errorf("failed to send %s: %w", item.SenderIssueID, err)
		}
		sent++
	}

	// Single commit for all items in the target database
	if sent > 0 {
		_, err = conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', ?)",
			fmt.Sprintf("inbox: received %d issue(s) from %s", sent, items[0].SenderProjectID))
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: auto-commit in target failed: %v\n", err)
		}
	}

	return sent, nil
}

// getCurrentDatabase returns the current database name from store metadata.
func getCurrentDatabase(ctx context.Context, s storage.DoltStorage) string {
	cfg, err := s.GetConfig(ctx, "dolt_database")
	if err == nil && cfg != "" {
		return cfg
	}
	// Fallback to prefix-derived name
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
