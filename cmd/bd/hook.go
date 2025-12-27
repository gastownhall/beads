package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
)

// hookCmd inspects what's on an agent's hook
var hookCmd = &cobra.Command{
	Use:   "hook",
	Short: "Inspect what's on an agent's hook",
	Long: `Show what mol is pinned to an agent's hook.

The hook is an agent's attachment point for work in the molecular chemistry
metaphor. This command shows what work is currently pinned to the agent.

Examples:
  bd hook                          # Show what's on my hook
  bd hook --agent deacon           # Show deacon's hook
  bd hook --agent polecat-ace      # Show specific polecat's hook`,
	Args: cobra.NoArgs,
	Run:  runHook,
}

func runHook(cmd *cobra.Command, args []string) {
	ctx := rootCtx
	agentName, _ := cmd.Flags().GetString("agent")

	if agentName == "" {
		agentName = actor
	}

	var issues []*types.Issue

	// Query for pinned issues assigned to this agent
	if daemonClient != nil {
		pinned := true
		listArgs := &rpc.ListArgs{
			Pinned:   &pinned,
			Assignee: agentName,
		}
		resp, err := daemonClient.List(listArgs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error querying hook: %v\n", err)
			os.Exit(1)
		}
		if err := json.Unmarshal(resp.Data, &issues); err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
			os.Exit(1)
		}
	} else if store != nil {
		var err error
		pinned := true
		filter := types.IssueFilter{
			Pinned:   &pinned,
			Assignee: &agentName,
		}
		issues, err = store.SearchIssues(ctx, "", filter)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error querying hook: %v\n", err)
			os.Exit(1)
		}
	} else {
		fmt.Fprintf(os.Stderr, "Error: no database connection\n")
		os.Exit(1)
	}

	if jsonOutput {
		type hookResult struct {
			Agent  string         `json:"agent"`
			Pinned []*types.Issue `json:"pinned"`
		}
		outputJSON(hookResult{Agent: agentName, Pinned: issues})
		return
	}

	fmt.Printf("Hook: %s\n", agentName)
	if len(issues) == 0 {
		fmt.Printf("  (empty)\n")
		return
	}

	for _, issue := range issues {
		phase := "mol"
		if issue.Ephemeral {
			phase = "ephemeral"
		}
		fmt.Printf("  📌 %s (%s) - %s\n", issue.ID, phase, issue.Status)
		fmt.Printf("     %s\n", issue.Title)
	}
}

func init() {
	hookCmd.Flags().String("agent", "", "Agent to inspect (default: current agent)")

	rootCmd.AddCommand(hookCmd)
}
