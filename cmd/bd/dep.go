// Package main implements the bd CLI dependency management commands.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

// isChildOf returns true if childID is a hierarchical child of parentID.
// For example, "bd-abc.1" is a child of "bd-abc", and "bd-abc.1.2" is a child of "bd-abc.1".
func isChildOf(childID, parentID string) bool {
	// A child ID has the format "parentID.N" or "parentID.N.M" etc.
	// Use ParseHierarchicalID to get the actual parent
	_, actualParentID, depth := types.ParseHierarchicalID(childID)
	if depth == 0 {
		return false // Not a hierarchical ID
	}
	// Check if the immediate parent matches
	if actualParentID == parentID {
		return true
	}
	// Also check if parentID is an ancestor (e.g., "bd-abc" is parent of "bd-abc.1.2")
	return strings.HasPrefix(childID, parentID+".")
}

var depCmd = &cobra.Command{
	Use:     "dep",
	GroupID: "deps",
	Short:   "Manage dependencies",
}

var depAddCmd = &cobra.Command{
	Use:   "add [issue-id] [depends-on-id]",
	Short: "Add a dependency",
	Long: `Add a dependency between two issues.

The depends-on-id can be:
  - A local issue ID (e.g., bd-xyz)
  - An external reference: external:<project>:<capability>

External references are stored as-is and resolved at query time using
the external_projects config. They block the issue until the capability
is "shipped" in the target project.

Examples:
  bd dep add bd-42 bd-41                              # Local dependency
  bd dep add gt-xyz external:beads:mol-run-assignee   # Cross-project dependency`,
	Args: cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("dep add")
		depType, _ := cmd.Flags().GetString("type")

		ctx := rootCtx

		// Resolve partial IDs first
		var fromID, toID string

		// Check if toID is an external reference (don't resolve it)
		isExternalRef := strings.HasPrefix(args[1], "external:")

		if daemonClient != nil {
			resolveArgs := &rpc.ResolveIDArgs{ID: args[0]}
			resp, err := daemonClient.ResolveID(resolveArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving issue ID %s: %v\n", args[0], err)
				os.Exit(1)
			}
			if err := json.Unmarshal(resp.Data, &fromID); err != nil {
				fmt.Fprintf(os.Stderr, "Error unmarshaling resolved ID: %v\n", err)
				os.Exit(1)
			}

			if isExternalRef {
				// External references are stored as-is
				toID = args[1]
				// Validate format: external:<project>:<capability>
				if err := validateExternalRef(toID); err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
			} else {
				resolveArgs = &rpc.ResolveIDArgs{ID: args[1]}
				resp, err = daemonClient.ResolveID(resolveArgs)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error resolving dependency ID %s: %v\n", args[1], err)
					os.Exit(1)
				}
				if err := json.Unmarshal(resp.Data, &toID); err != nil {
					fmt.Fprintf(os.Stderr, "Error unmarshaling resolved ID: %v\n", err)
					os.Exit(1)
				}
			}
		} else {
			var err error
			fromID, err = utils.ResolvePartialID(ctx, store, args[0])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving issue ID %s: %v\n", args[0], err)
				os.Exit(1)
			}

			if isExternalRef {
				// External references are stored as-is
				toID = args[1]
				// Validate format: external:<project>:<capability>
				if err := validateExternalRef(toID); err != nil {
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					os.Exit(1)
				}
			} else {
				toID, err = utils.ResolvePartialID(ctx, store, args[1])
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error resolving dependency ID %s: %v\n", args[1], err)
					os.Exit(1)
				}
			}
		}

		// Check for child→parent dependency anti-pattern (bd-nim5)
		// This creates a deadlock: child can't start (parent open), parent can't close (children not done)
		if isChildOf(fromID, toID) {
			fmt.Fprintf(os.Stderr, "Error: Cannot add dependency: %s is already a child of %s.\n", fromID, toID)
			fmt.Fprintf(os.Stderr, "Children inherit dependency on parent completion via hierarchy.\n")
			fmt.Fprintf(os.Stderr, "Adding an explicit dependency would create a deadlock.\n")
			os.Exit(1)
		}

		// If daemon is running, use RPC
		if daemonClient != nil {
			depArgs := &rpc.DepAddArgs{
				FromID:  fromID,
				ToID:    toID,
				DepType: depType,
			}

			resp, err := daemonClient.AddDependency(depArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			if jsonOutput {
				fmt.Println(string(resp.Data))
				return
			}

			fmt.Printf("%s Added dependency: %s depends on %s (%s)\n",
				ui.RenderPass("✓"), args[0], args[1], depType)
			return
		}

		// Direct mode
		dep := &types.Dependency{
			IssueID:     fromID,
			DependsOnID: toID,
			Type:        types.DependencyType(depType),
		}

		if err := store.AddDependency(ctx, dep, actor); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		// Schedule auto-flush
		markDirtyAndScheduleFlush()

		// Check for cycles after adding dependency
		cycles, err := store.DetectCycles(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to check for cycles: %v\n", err)
		} else if len(cycles) > 0 {
			fmt.Fprintf(os.Stderr, "\n%s Warning: Dependency cycle detected!\n", ui.RenderWarn("⚠"))
			fmt.Fprintf(os.Stderr, "This can hide issues from the ready work list and cause confusion.\n\n")
			fmt.Fprintf(os.Stderr, "Cycle path:\n")
			for _, cycle := range cycles {
				for j, issue := range cycle {
					if j == 0 {
						fmt.Fprintf(os.Stderr, "  %s", issue.ID)
					} else {
						fmt.Fprintf(os.Stderr, " → %s", issue.ID)
					}
				}
				if len(cycle) > 0 {
					fmt.Fprintf(os.Stderr, " → %s", cycle[0].ID)
				}
				fmt.Fprintf(os.Stderr, "\n")
			}
			fmt.Fprintf(os.Stderr, "\nRun 'bd dep cycles' for detailed analysis.\n\n")
		}

		if jsonOutput {
			outputJSON(map[string]interface{}{
				"status":        "added",
				"issue_id":      fromID,
				"depends_on_id": toID,
				"type":          depType,
			})
			return
		}

		fmt.Printf("%s Added dependency: %s depends on %s (%s)\n",
			ui.RenderPass("✓"), fromID, toID, depType)
	},
}

var depRemoveCmd = &cobra.Command{
	Use:   "remove [issue-id] [depends-on-id]",
	Short: "Remove a dependency",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("dep remove")
		ctx := rootCtx
		
		// Resolve partial IDs first
		var fromID, toID string
		if daemonClient != nil {
			resolveArgs := &rpc.ResolveIDArgs{ID: args[0]}
			resp, err := daemonClient.ResolveID(resolveArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving issue ID %s: %v\n", args[0], err)
				os.Exit(1)
			}
			if err := json.Unmarshal(resp.Data, &fromID); err != nil {
				fmt.Fprintf(os.Stderr, "Error unmarshaling resolved ID: %v\n", err)
				os.Exit(1)
			}
			
			resolveArgs = &rpc.ResolveIDArgs{ID: args[1]}
			resp, err = daemonClient.ResolveID(resolveArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving dependency ID %s: %v\n", args[1], err)
				os.Exit(1)
			}
			if err := json.Unmarshal(resp.Data, &toID); err != nil {
				fmt.Fprintf(os.Stderr, "Error unmarshaling resolved ID: %v\n", err)
				os.Exit(1)
			}
		} else {
			var err error
			fromID, err = utils.ResolvePartialID(ctx, store, args[0])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving issue ID %s: %v\n", args[0], err)
				os.Exit(1)
			}
			
			toID, err = utils.ResolvePartialID(ctx, store, args[1])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving dependency ID %s: %v\n", args[1], err)
				os.Exit(1)
			}
		}

		// If daemon is running, use RPC
		if daemonClient != nil {
			depArgs := &rpc.DepRemoveArgs{
				FromID: fromID,
				ToID:   toID,
			}

			resp, err := daemonClient.RemoveDependency(depArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			if jsonOutput {
				fmt.Println(string(resp.Data))
				return
			}

			fmt.Printf("%s Removed dependency: %s no longer depends on %s\n",
				ui.RenderPass("✓"), fromID, toID)
			return
		}

		// Direct mode
		fullFromID := fromID
		fullToID := toID
		
		if err := store.RemoveDependency(ctx, fullFromID, fullToID, actor); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		// Schedule auto-flush
		markDirtyAndScheduleFlush()

		if jsonOutput {
			outputJSON(map[string]interface{}{
				"status":        "removed",
				"issue_id":      fullFromID,
				"depends_on_id": fullToID,
			})
			return
		}

		fmt.Printf("%s Removed dependency: %s no longer depends on %s\n",
			ui.RenderPass("✓"), fullFromID, fullToID)
	},
}

var depTreeCmd = &cobra.Command{
	Use:   "tree [issue-id]",
	Short: "Show dependency tree",
	Long: `Show dependency tree rooted at the given issue.

By default, shows dependencies (what blocks this issue). Use --direction to control:
  - down: Show dependencies (what blocks this issue) - default
  - up:   Show dependents (what this issue blocks)
  - both: Show full graph in both directions

Examples:
  bd dep tree gt-0iqq                    # Show what blocks gt-0iqq
  bd dep tree gt-0iqq --direction=up     # Show what gt-0iqq blocks
  bd dep tree gt-0iqq --status=open      # Only show open issues
  bd dep tree gt-0iqq --depth=3          # Limit to 3 levels deep`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := rootCtx

		// Resolve partial ID first
		var fullID string
		if daemonClient != nil {
			resolveArgs := &rpc.ResolveIDArgs{ID: args[0]}
			resp, err := daemonClient.ResolveID(resolveArgs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving issue ID %s: %v\n", args[0], err)
				os.Exit(1)
			}
			if err := json.Unmarshal(resp.Data, &fullID); err != nil {
				fmt.Fprintf(os.Stderr, "Error unmarshaling resolved ID: %v\n", err)
				os.Exit(1)
			}
		} else {
			var err error
			fullID, err = utils.ResolvePartialID(ctx, store, args[0])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving %s: %v\n", args[0], err)
				os.Exit(1)
			}
		}

		// If daemon is running but doesn't support this command, use direct storage
		if daemonClient != nil && store == nil {
			var err error
			store, err = sqlite.New(rootCtx, dbPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: failed to open database: %v\n", err)
				os.Exit(1)
			}
			defer func() { _ = store.Close() }()
		}

		showAllPaths, _ := cmd.Flags().GetBool("show-all-paths")
		maxDepth, _ := cmd.Flags().GetInt("max-depth")
		reverse, _ := cmd.Flags().GetBool("reverse")
		direction, _ := cmd.Flags().GetString("direction")
		statusFilter, _ := cmd.Flags().GetString("status")
		formatStr, _ := cmd.Flags().GetString("format")

		// Handle --direction flag (takes precedence over deprecated --reverse)
		if direction == "" && reverse {
			direction = "up"
		} else if direction == "" {
			direction = "down"
		}

		// Validate direction
		if direction != "down" && direction != "up" && direction != "both" {
			fmt.Fprintf(os.Stderr, "Error: --direction must be 'down', 'up', or 'both'\n")
			os.Exit(1)
		}

		if maxDepth < 1 {
			fmt.Fprintf(os.Stderr, "Error: --max-depth must be >= 1\n")
			os.Exit(1)
		}

		// For "both" direction, we need to fetch both trees and merge them
		var tree []*types.TreeNode
		var err error

		if direction == "both" {
			// Get dependencies (down) - what blocks this issue
			downTree, err := store.GetDependencyTree(ctx, fullID, maxDepth, showAllPaths, false)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			// Get dependents (up) - what this issue blocks
			upTree, err := store.GetDependencyTree(ctx, fullID, maxDepth, showAllPaths, true)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}

			// Merge: root appears once, dependencies below, dependents above
			// We'll show dependents first (with negative-like positioning conceptually),
			// then root, then dependencies
			tree = mergeBidirectionalTrees(downTree, upTree, fullID)
		} else {
			tree, err = store.GetDependencyTree(ctx, fullID, maxDepth, showAllPaths, direction == "up")
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}

		// Apply status filter if specified
		if statusFilter != "" {
			tree = filterTreeByStatus(tree, types.Status(statusFilter))
		}

		// Handle mermaid format
		if formatStr == "mermaid" {
			outputMermaidTree(tree, args[0])
			return
		}

		if jsonOutput {
			// Always output array, even if empty
			if tree == nil {
				tree = []*types.TreeNode{}
			}
			outputJSON(tree)
			return
		}

		if len(tree) == 0 {
			switch direction {
			case "up":
				fmt.Printf("\n%s has no dependents\n", fullID)
			case "both":
				fmt.Printf("\n%s has no dependencies or dependents\n", fullID)
			default:
				fmt.Printf("\n%s has no dependencies\n", fullID)
			}
			return
		}

		switch direction {
		case "up":
			fmt.Printf("\n%s Dependent tree for %s:\n\n", ui.RenderAccent("🌲"), fullID)
		case "both":
			fmt.Printf("\n%s Full dependency graph for %s:\n\n", ui.RenderAccent("🌲"), fullID)
		default:
			fmt.Printf("\n%s Dependency tree for %s:\n\n", ui.RenderAccent("🌲"), fullID)
		}

		// Render tree with proper connectors
		renderTree(tree, maxDepth, direction)
		fmt.Println()
	},
}

var depCyclesCmd = &cobra.Command{
	Use:   "cycles",
	Short: "Detect dependency cycles",
	Run: func(cmd *cobra.Command, args []string) {
		// If daemon is running but doesn't support this command, use direct storage
		if daemonClient != nil && store == nil {
			var err error
			store, err = sqlite.New(rootCtx, dbPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: failed to open database: %v\n", err)
				os.Exit(1)
			}
			defer func() { _ = store.Close() }()
		}

		ctx := rootCtx
		cycles, err := store.DetectCycles(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if jsonOutput {
			// Always output array, even if empty
			if cycles == nil {
				cycles = [][]*types.Issue{}
			}
			outputJSON(cycles)
			return
		}

		if len(cycles) == 0 {
			fmt.Printf("\n%s No dependency cycles detected\n\n", ui.RenderPass("✓"))
			return
		}

		fmt.Printf("\n%s Found %d dependency cycles:\n\n", ui.RenderFail("⚠"), len(cycles))
		for i, cycle := range cycles {
			fmt.Printf("%d. Cycle involving:\n", i+1)
			for _, issue := range cycle {
				fmt.Printf("   - %s: %s\n", issue.ID, issue.Title)
			}
			fmt.Println()
		}
	},
}

// outputMermaidTree outputs a dependency tree in Mermaid.js flowchart format
func outputMermaidTree(tree []*types.TreeNode, rootID string) {
	if len(tree) == 0 {
		fmt.Println("flowchart TD")
		fmt.Printf("  %s[\"No dependencies\"]\n", rootID)
		return
	}

	fmt.Println("flowchart TD")

	// Output nodes
	nodesSeen := make(map[string]bool)
	for _, node := range tree {
		if !nodesSeen[node.ID] {
			emoji := getStatusEmoji(node.Status)
			label := fmt.Sprintf("%s %s: %s", emoji, node.ID, node.Title)
			// Escape quotes and backslashes in label
			label = strings.ReplaceAll(label, "\\", "\\\\")
			label = strings.ReplaceAll(label, "\"", "\\\"")
			fmt.Printf("  %s[\"%s\"]\n", node.ID, label)

			nodesSeen[node.ID] = true
		}
	}

	fmt.Println()

	// Output edges - use explicit parent relationships from ParentID
	for _, node := range tree {
		if node.ParentID != "" && node.ParentID != node.ID {
			fmt.Printf("  %s --> %s\n", node.ParentID, node.ID)
		}
	}
}

// getStatusEmoji returns a symbol indicator for a given status
func getStatusEmoji(status types.Status) string {
	switch status {
	case types.StatusOpen:
		return "☐" // U+2610 Ballot Box
	case types.StatusInProgress:
		return "◧" // U+25E7 Square Left Half Black
	case types.StatusBlocked:
		return "⚠" // U+26A0 Warning Sign
	case types.StatusDeferred:
		return "❄" // U+2744 Snowflake (on ice)
	case types.StatusClosed:
		return "☑" // U+2611 Ballot Box with Check
	default:
		return "?"
	}
}

// treeRenderer holds state for rendering a tree with proper connectors
type treeRenderer struct {
	// Track which nodes we've already displayed (for "shown above" handling)
	seen map[string]bool
	// Track connector state at each depth level (true = has more siblings)
	activeConnectors []bool
	// Maximum depth reached
	maxDepth int
	// Direction of traversal
	direction string
}

// renderTree renders the tree with proper box-drawing connectors
func renderTree(tree []*types.TreeNode, maxDepth int, direction string) {
	if len(tree) == 0 {
		return
	}

	r := &treeRenderer{
		seen:             make(map[string]bool),
		activeConnectors: make([]bool, maxDepth+1),
		maxDepth:         maxDepth,
		direction:        direction,
	}

	// Build a map of parent -> children for proper sibling tracking
	children := make(map[string][]*types.TreeNode)
	var root *types.TreeNode

	for _, node := range tree {
		if node.Depth == 0 {
			root = node
		} else {
			children[node.ParentID] = append(children[node.ParentID], node)
		}
	}

	if root == nil && len(tree) > 0 {
		root = tree[0]
	}

	// Render recursively from root
	r.renderNode(root, children, 0, true)
}

// renderNode renders a single node and its children
func (r *treeRenderer) renderNode(node *types.TreeNode, children map[string][]*types.TreeNode, depth int, isLast bool) {
	if node == nil {
		return
	}

	// Build the prefix with connectors
	var prefix strings.Builder

	// Add vertical lines for active parent connectors
	for i := 0; i < depth; i++ {
		if r.activeConnectors[i] {
			prefix.WriteString("│   ")
		} else {
			prefix.WriteString("    ")
		}
	}

	// Add the branch connector for non-root nodes
	if depth > 0 {
		if isLast {
			prefix.WriteString("└── ")
		} else {
			prefix.WriteString("├── ")
		}
	}

	// Check if we've seen this node before (diamond dependency)
	if r.seen[node.ID] {
		fmt.Printf("%s%s (shown above)\n", prefix.String(), ui.RenderMuted(node.ID))
		return
	}
	r.seen[node.ID] = true

	// Format the node line
	line := formatTreeNode(node)

	// Add truncation warning if at max depth and has children
	if node.Truncated || (depth == r.maxDepth && len(children[node.ID]) > 0) {
		line += ui.RenderWarn(" …")
	}

	fmt.Printf("%s%s\n", prefix.String(), line)

	// Render children
	nodeChildren := children[node.ID]
	for i, child := range nodeChildren {
		// Update connector state for this depth
		// For depth 0 (root level), never show vertical connector since root has no siblings
		if depth > 0 {
			r.activeConnectors[depth] = (i < len(nodeChildren)-1)
		}
		r.renderNode(child, children, depth+1, i == len(nodeChildren)-1)
	}
}

// formatTreeNode formats a single tree node with status, ready indicator, etc.
func formatTreeNode(node *types.TreeNode) string {
	// Handle external dependencies specially (bd-vks2)
	if IsExternalRef(node.ID) {
		// External deps use their title directly which includes the status indicator
		var idStr string
		switch node.Status {
		case types.StatusClosed:
			idStr = ui.StatusClosedStyle.Render(node.Title)
		case types.StatusBlocked:
			idStr = ui.StatusBlockedStyle.Render(node.Title)
		default:
			idStr = node.Title
		}
		return fmt.Sprintf("%s (external)", idStr)
	}

	// Color the ID based on status
	var idStr string
	switch node.Status {
	case types.StatusOpen:
		idStr = ui.StatusOpenStyle.Render(node.ID)
	case types.StatusInProgress:
		idStr = ui.StatusInProgressStyle.Render(node.ID)
	case types.StatusBlocked:
		idStr = ui.StatusBlockedStyle.Render(node.ID)
	case types.StatusClosed:
		idStr = ui.StatusClosedStyle.Render(node.ID)
	default:
		idStr = node.ID
	}

	// Build the line
	line := fmt.Sprintf("%s: %s [P%d] (%s)",
		idStr, node.Title, node.Priority, node.Status)

	// Add READY indicator for open issues (those that could be worked on)
	// An issue is ready if it's open and has no blocking dependencies
	// (In the tree view, depth 0 with status open implies ready in the "down" direction)
	if node.Status == types.StatusOpen && node.Depth == 0 {
		line += " " + ui.PassStyle.Bold(true).Render("[READY]")
	}

	return line
}

// filterTreeByStatus filters the tree to only include nodes with the given status
// Note: keeps parent chain to maintain tree structure
func filterTreeByStatus(tree []*types.TreeNode, status types.Status) []*types.TreeNode {
	if len(tree) == 0 {
		return tree
	}

	// First pass: identify which nodes match the status
	matches := make(map[string]bool)
	for _, node := range tree {
		if node.Status == status {
			matches[node.ID] = true
		}
	}

	// If no matches, return empty
	if len(matches) == 0 {
		return []*types.TreeNode{}
	}

	// Second pass: keep matching nodes and their ancestors
	// Build parent map
	parentOf := make(map[string]string)
	for _, node := range tree {
		if node.ParentID != "" && node.ParentID != node.ID {
			parentOf[node.ID] = node.ParentID
		}
	}

	// Mark all ancestors of matching nodes
	keep := make(map[string]bool)
	for id := range matches {
		keep[id] = true
		// Walk up to root
		current := id
		for {
			parent, ok := parentOf[current]
			if !ok || parent == current {
				break
			}
			keep[parent] = true
			current = parent
		}
	}

	// Filter the tree
	var filtered []*types.TreeNode
	for _, node := range tree {
		if keep[node.ID] {
			filtered = append(filtered, node)
		}
	}

	return filtered
}

// mergeBidirectionalTrees merges up and down trees into a single visualization
// The root appears once, with dependencies shown below and dependents shown above
func mergeBidirectionalTrees(downTree, upTree []*types.TreeNode, rootID string) []*types.TreeNode {
	// For bidirectional display, we show the down tree (dependencies) as the main tree
	// and add a visual separator with the up tree (dependents)
	//
	// For simplicity, we'll just return the down tree for now
	// A more sophisticated implementation would show both with visual separation

	// Find root in each tree
	var result []*types.TreeNode

	// Add dependents section if any (excluding root)
	hasUpNodes := false
	for _, node := range upTree {
		if node.ID != rootID {
			hasUpNodes = true
			break
		}
	}

	if hasUpNodes {
		// Add a header node for dependents section
		// We'll mark these with negative depth for visual distinction
		for _, node := range upTree {
			if node.ID == rootID {
				continue // Skip root, we'll add it once from down tree
			}
			// Clone node and mark it as "up" direction
			upNode := *node
			upNode.Depth = node.Depth // Keep original depth
			result = append(result, &upNode)
		}
	}

	// Add the down tree (dependencies)
	result = append(result, downTree...)

	return result
}

// validateExternalRef validates the format of an external dependency reference.
// Valid format: external:<project>:<capability>
func validateExternalRef(ref string) error {
	if !strings.HasPrefix(ref, "external:") {
		return fmt.Errorf("external reference must start with 'external:'")
	}

	parts := strings.SplitN(ref, ":", 3)
	if len(parts) != 3 {
		return fmt.Errorf("invalid external reference format: expected 'external:<project>:<capability>', got '%s'", ref)
	}

	project := parts[1]
	capability := parts[2]

	if project == "" {
		return fmt.Errorf("external reference missing project name")
	}
	if capability == "" {
		return fmt.Errorf("external reference missing capability name")
	}

	return nil
}

// IsExternalRef returns true if the dependency reference is an external reference.
func IsExternalRef(ref string) bool {
	return strings.HasPrefix(ref, "external:")
}

// ParseExternalRef parses an external reference into project and capability.
// Returns empty strings if the format is invalid.
func ParseExternalRef(ref string) (project, capability string) {
	if !IsExternalRef(ref) {
		return "", ""
	}
	parts := strings.SplitN(ref, ":", 3)
	if len(parts) != 3 {
		return "", ""
	}
	return parts[1], parts[2]
}

func init() {
	depAddCmd.Flags().StringP("type", "t", "blocks", "Dependency type (blocks|related|parent-child|discovered-from)")
	// Note: --json flag is defined as a persistent flag in main.go, not here

	// Note: --json flag is defined as a persistent flag in main.go, not here

	depTreeCmd.Flags().Bool("show-all-paths", false, "Show all paths to nodes (no deduplication for diamond dependencies)")
	depTreeCmd.Flags().IntP("max-depth", "d", 50, "Maximum tree depth to display (safety limit)")
	depTreeCmd.Flags().Bool("reverse", false, "Show dependent tree (deprecated: use --direction=up)")
	depTreeCmd.Flags().String("direction", "", "Tree direction: 'down' (dependencies), 'up' (dependents), or 'both'")
	depTreeCmd.Flags().String("status", "", "Filter to only show issues with this status (open, in_progress, blocked, deferred, closed)")
	depTreeCmd.Flags().String("format", "", "Output format: 'mermaid' for Mermaid.js flowchart")
	// Note: --json flag is defined as a persistent flag in main.go, not here

	// Note: --json flag is defined as a persistent flag in main.go, not here

	depCmd.AddCommand(depAddCmd)
	depCmd.AddCommand(depRemoveCmd)
	depCmd.AddCommand(depTreeCmd)
	depCmd.AddCommand(depCyclesCmd)
	rootCmd.AddCommand(depCmd)
}
