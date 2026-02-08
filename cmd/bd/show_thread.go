package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

// showMessageThread displays a full conversation thread for a message
func showMessageThread(ctx context.Context, messageID string, jsonOutput bool) {
	// Get the starting message
	var startMsg *types.Issue
	var err error

	if daemonClient != nil {
		resp, err := daemonClient.Show(&rpc.ShowArgs{ID: messageID})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching message %s: %v\n", messageID, err)
			os.Exit(1)
		}
		if err := json.Unmarshal(resp.Data, &startMsg); err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
			os.Exit(1)
		}
	} else {
		startMsg, err = store.GetIssue(ctx, messageID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching message %s: %v\n", messageID, err)
			os.Exit(1)
		}
	}

	if startMsg == nil {
		fmt.Fprintf(os.Stderr, "Message %s not found\n", messageID)
		os.Exit(1)
	}

	// Find the root of the thread by following replies-to dependencies upward
	// Per Decision 004, RepliesTo is now stored as a dependency, not an Issue field
	rootMsg := startMsg
	seen := make(map[string]bool)
	seen[rootMsg.ID] = true

	for {
		// Find parent via replies-to dependency
		parentID := findRepliesTo(ctx, rootMsg.ID, daemonClient, store)
		if parentID == "" {
			break // No parent, this is the root
		}
		if seen[parentID] {
			break // Avoid infinite loops
		}
		seen[parentID] = true

		var parentMsg *types.Issue
		if daemonClient != nil {
			resp, err := daemonClient.Show(&rpc.ShowArgs{ID: parentID})
			if err != nil {
				break // Parent not found, use current as root
			}
			if err := json.Unmarshal(resp.Data, &parentMsg); err != nil {
				break
			}
		} else {
			parentMsg, _ = store.GetIssue(ctx, parentID)
		}
		if parentMsg == nil {
			break
		}
		rootMsg = parentMsg
	}

	// Now collect all messages in the thread
	// Start from root and find all replies
	// Build a map of child ID -> parent ID for display purposes
	threadMessages := []*types.Issue{rootMsg}
	threadIDs := map[string]bool{rootMsg.ID: true}
	repliesTo := map[string]string{} // child ID -> parent ID
	queue := []string{rootMsg.ID}

	// BFS to find all replies
	for len(queue) > 0 {
		currentID := queue[0]
		queue = queue[1:]

		// Find all messages that reply to currentID via replies-to dependency
		// Per Decision 004, replies are found via dependents with type replies-to
		replies := findReplies(ctx, currentID, daemonClient, store)

		for _, reply := range replies {
			if threadIDs[reply.ID] {
				continue // Already seen
			}
			threadMessages = append(threadMessages, reply)
			threadIDs[reply.ID] = true
			repliesTo[reply.ID] = currentID // Track parent for display
			queue = append(queue, reply.ID)
		}
	}

	// Sort by creation time
	slices.SortFunc(threadMessages, func(a, b *types.Issue) int {
		return a.CreatedAt.Compare(b.CreatedAt)
	})

	if jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(threadMessages)
		return
	}

	// Display the thread
	fmt.Printf("\n%s Thread: %s\n", ui.RenderAccent("📬"), rootMsg.Title)
	fmt.Println(strings.Repeat("─", 66))

	for _, msg := range threadMessages {
		// Show indent based on depth (count replies_to chain using our map)
		depth := 0
		parent := repliesTo[msg.ID]
		for parent != "" && depth < 5 {
			depth++
			parent = repliesTo[parent]
		}
		indent := strings.Repeat("  ", depth)

		// Format timestamp
		timeStr := msg.CreatedAt.Format("2006-01-02 15:04")

		// Status indicator
		statusIcon := "📧"
		if msg.Status == types.StatusClosed {
			statusIcon = "✓"
		}

		fmt.Printf("%s%s %s %s\n", indent, statusIcon, ui.RenderAccent(msg.ID), ui.RenderMuted(timeStr))
		fmt.Printf("%s  From: %s  To: %s\n", indent, msg.Sender, msg.Assignee)
		if parentID := repliesTo[msg.ID]; parentID != "" {
			fmt.Printf("%s  Re: %s\n", indent, parentID)
		}
		fmt.Printf("%s  %s: %s\n", indent, ui.RenderMuted("Subject"), msg.Title)
		if msg.Description != "" {
			// Indent the body
			bodyLines := strings.Split(msg.Description, "\n")
			for _, line := range bodyLines {
				fmt.Printf("%s  %s\n", indent, line)
			}
		}
		fmt.Println()
	}

	fmt.Printf("Total: %d messages in thread\n\n", len(threadMessages))
}

// findRepliesTo finds the parent ID that this issue replies to via replies-to dependency.
// Returns empty string if no parent found.
func findRepliesTo(ctx context.Context, issueID string, daemonClient *rpc.Client, store storage.Storage) string {
	if daemonClient != nil {
		// In daemon mode, use Show to get dependencies with metadata
		resp, err := daemonClient.Show(&rpc.ShowArgs{ID: issueID})
		if err != nil {
			return ""
		}
		// Parse the full show response to get dependencies
		type showResponse struct {
			Dependencies []struct {
				ID             string `json:"id"`
				DependencyType string `json:"dependency_type"`
			} `json:"dependencies"`
		}
		var details showResponse
		if err := json.Unmarshal(resp.Data, &details); err != nil {
			return ""
		}
		for _, dep := range details.Dependencies {
			if dep.DependencyType == string(types.DepRepliesTo) {
				return dep.ID
			}
		}
		return ""
	}
	// Direct mode - query storage
	deps, err := store.GetDependencyRecords(ctx, issueID)
	if err != nil {
		return ""
	}
	for _, dep := range deps {
		if dep.Type == types.DepRepliesTo {
			return dep.DependsOnID
		}
	}
	return ""
}

// findReplies finds all issues that reply to this issue via replies-to dependency.
func findReplies(ctx context.Context, issueID string, daemonClient *rpc.Client, store storage.Storage) []*types.Issue {
	if daemonClient != nil {
		// In daemon mode, use Show to get dependents with metadata
		resp, err := daemonClient.Show(&rpc.ShowArgs{ID: issueID})
		if err != nil {
			return nil
		}
		// Parse the full show response to get dependents
		type showResponse struct {
			Dependents []struct {
				types.Issue
				DependencyType string `json:"dependency_type"`
			} `json:"dependents"`
		}
		var details showResponse
		if err := json.Unmarshal(resp.Data, &details); err != nil {
			return nil
		}
		var replies []*types.Issue
		for _, dep := range details.Dependents {
			if dep.DependencyType == string(types.DepRepliesTo) {
				issue := dep.Issue // Copy to avoid aliasing
				replies = append(replies, &issue)
			}
		}
		return replies
	}
	// Direct mode - query storage
	deps, err := store.GetDependentsWithMetadata(ctx, issueID)
	if err != nil {
		return nil
	}
	var replies []*types.Issue
	for _, dep := range deps {
		if dep.DependencyType == types.DepRepliesTo {
			issue := dep.Issue // Copy to avoid aliasing
			replies = append(replies, &issue)
		}
	}
	return replies
}
