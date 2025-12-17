package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

var relateCmd = &cobra.Command{
	Use:   "relate <id1> <id2>",
	Short: "Create a bidirectional relates_to link between issues",
	Long: `Create a loose 'see also' relationship between two issues.

The relates_to link is bidirectional - both issues will reference each other.
This enables knowledge graph connections without blocking or hierarchy.

Examples:
  bd relate bd-abc bd-xyz    # Link two related issues
  bd relate bd-123 bd-456    # Create see-also connection`,
	Args: cobra.ExactArgs(2),
	RunE: runRelate,
}

var unrelateCmd = &cobra.Command{
	Use:   "unrelate <id1> <id2>",
	Short: "Remove a relates_to link between issues",
	Long: `Remove a relates_to relationship between two issues.

Removes the link in both directions.

Example:
  bd unrelate bd-abc bd-xyz`,
	Args: cobra.ExactArgs(2),
	RunE: runUnrelate,
}

func init() {
	rootCmd.AddCommand(relateCmd)
	rootCmd.AddCommand(unrelateCmd)
}

func runRelate(cmd *cobra.Command, args []string) error {
	CheckReadonly("relate")

	ctx := rootCtx

	// Resolve partial IDs
	var id1, id2 string
	if daemonClient != nil {
		resp1, err := daemonClient.ResolveID(&rpc.ResolveIDArgs{ID: args[0]})
		if err != nil {
			return fmt.Errorf("failed to resolve %s: %w", args[0], err)
		}
		if err := json.Unmarshal(resp1.Data, &id1); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
		resp2, err := daemonClient.ResolveID(&rpc.ResolveIDArgs{ID: args[1]})
		if err != nil {
			return fmt.Errorf("failed to resolve %s: %w", args[1], err)
		}
		if err := json.Unmarshal(resp2.Data, &id2); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
	} else {
		var err error
		id1, err = utils.ResolvePartialID(ctx, store, args[0])
		if err != nil {
			return fmt.Errorf("failed to resolve %s: %w", args[0], err)
		}
		id2, err = utils.ResolvePartialID(ctx, store, args[1])
		if err != nil {
			return fmt.Errorf("failed to resolve %s: %w", args[1], err)
		}
	}

	if id1 == id2 {
		return fmt.Errorf("cannot relate an issue to itself")
	}

	// Get both issues
	var issue1, issue2 *types.Issue
	if daemonClient != nil {
		resp1, err := daemonClient.Show(&rpc.ShowArgs{ID: id1})
		if err != nil {
			return fmt.Errorf("failed to get issue %s: %w", id1, err)
		}
		if err := json.Unmarshal(resp1.Data, &issue1); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
		resp2, err := daemonClient.Show(&rpc.ShowArgs{ID: id2})
		if err != nil {
			return fmt.Errorf("failed to get issue %s: %w", id2, err)
		}
		if err := json.Unmarshal(resp2.Data, &issue2); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
	} else {
		var err error
		issue1, err = store.GetIssue(ctx, id1)
		if err != nil {
			return fmt.Errorf("failed to get issue %s: %w", id1, err)
		}
		issue2, err = store.GetIssue(ctx, id2)
		if err != nil {
			return fmt.Errorf("failed to get issue %s: %w", id2, err)
		}
	}

	if issue1 == nil {
		return fmt.Errorf("issue not found: %s", id1)
	}
	if issue2 == nil {
		return fmt.Errorf("issue not found: %s", id2)
	}

	// Add id2 to issue1's relates_to if not already present
	if !contains(issue1.RelatesTo, id2) {
		newRelatesTo1 := append(issue1.RelatesTo, id2)
		if err := store.UpdateIssue(ctx, id1, map[string]interface{}{
			"relates_to": formatRelatesTo(newRelatesTo1),
		}, actor); err != nil {
			return fmt.Errorf("failed to update %s: %w", id1, err)
		}
	}

	// Add id1 to issue2's relates_to if not already present
	if !contains(issue2.RelatesTo, id1) {
		newRelatesTo2 := append(issue2.RelatesTo, id1)
		if err := store.UpdateIssue(ctx, id2, map[string]interface{}{
			"relates_to": formatRelatesTo(newRelatesTo2),
		}, actor); err != nil {
			return fmt.Errorf("failed to update %s: %w", id2, err)
		}
	}

	// Trigger auto-flush
	if flushManager != nil {
		flushManager.MarkDirty(false)
	}

	if jsonOutput {
		result := map[string]interface{}{
			"id1":     id1,
			"id2":     id2,
			"related": true,
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	green := color.New(color.FgGreen).SprintFunc()
	fmt.Printf("%s Linked %s ↔ %s\n", green("✓"), id1, id2)
	return nil
}

func runUnrelate(cmd *cobra.Command, args []string) error {
	CheckReadonly("unrelate")

	ctx := rootCtx

	// Resolve partial IDs
	var id1, id2 string
	if daemonClient != nil {
		resp1, err := daemonClient.ResolveID(&rpc.ResolveIDArgs{ID: args[0]})
		if err != nil {
			return fmt.Errorf("failed to resolve %s: %w", args[0], err)
		}
		if err := json.Unmarshal(resp1.Data, &id1); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
		resp2, err := daemonClient.ResolveID(&rpc.ResolveIDArgs{ID: args[1]})
		if err != nil {
			return fmt.Errorf("failed to resolve %s: %w", args[1], err)
		}
		if err := json.Unmarshal(resp2.Data, &id2); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
	} else {
		var err error
		id1, err = utils.ResolvePartialID(ctx, store, args[0])
		if err != nil {
			return fmt.Errorf("failed to resolve %s: %w", args[0], err)
		}
		id2, err = utils.ResolvePartialID(ctx, store, args[1])
		if err != nil {
			return fmt.Errorf("failed to resolve %s: %w", args[1], err)
		}
	}

	// Get both issues
	var issue1, issue2 *types.Issue
	if daemonClient != nil {
		resp1, err := daemonClient.Show(&rpc.ShowArgs{ID: id1})
		if err != nil {
			return fmt.Errorf("failed to get issue %s: %w", id1, err)
		}
		if err := json.Unmarshal(resp1.Data, &issue1); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
		resp2, err := daemonClient.Show(&rpc.ShowArgs{ID: id2})
		if err != nil {
			return fmt.Errorf("failed to get issue %s: %w", id2, err)
		}
		if err := json.Unmarshal(resp2.Data, &issue2); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
	} else {
		var err error
		issue1, err = store.GetIssue(ctx, id1)
		if err != nil {
			return fmt.Errorf("failed to get issue %s: %w", id1, err)
		}
		issue2, err = store.GetIssue(ctx, id2)
		if err != nil {
			return fmt.Errorf("failed to get issue %s: %w", id2, err)
		}
	}

	if issue1 == nil {
		return fmt.Errorf("issue not found: %s", id1)
	}
	if issue2 == nil {
		return fmt.Errorf("issue not found: %s", id2)
	}

	// Remove id2 from issue1's relates_to
	newRelatesTo1 := remove(issue1.RelatesTo, id2)
	if err := store.UpdateIssue(ctx, id1, map[string]interface{}{
		"relates_to": formatRelatesTo(newRelatesTo1),
	}, actor); err != nil {
		return fmt.Errorf("failed to update %s: %w", id1, err)
	}

	// Remove id1 from issue2's relates_to
	newRelatesTo2 := remove(issue2.RelatesTo, id1)
	if err := store.UpdateIssue(ctx, id2, map[string]interface{}{
		"relates_to": formatRelatesTo(newRelatesTo2),
	}, actor); err != nil {
		return fmt.Errorf("failed to update %s: %w", id2, err)
	}

	// Trigger auto-flush
	if flushManager != nil {
		flushManager.MarkDirty(false)
	}

	if jsonOutput {
		result := map[string]interface{}{
			"id1":       id1,
			"id2":       id2,
			"unrelated": true,
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	green := color.New(color.FgGreen).SprintFunc()
	fmt.Printf("%s Unlinked %s ↔ %s\n", green("✓"), id1, id2)
	return nil
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func remove(slice []string, item string) []string {
	result := make([]string, 0, len(slice))
	for _, s := range slice {
		if s != item {
			result = append(result, s)
		}
	}
	return result
}

func formatRelatesTo(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	data, _ := json.Marshal(ids)
	return string(data)
}
