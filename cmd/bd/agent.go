package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

// Valid agent states for state command
var validAgentStates = map[string]bool{
	"idle":     true, // Agent is waiting for work
	"spawning": true, // Agent is starting up
	"running":  true, // Agent is executing (general)
	"working":  true, // Agent is actively working on a task
	"stuck":    true, // Agent is blocked and needs help
	"done":     true, // Agent completed its current work
	"stopped":  true, // Agent has cleanly shut down
	"dead":     true, // Agent died without clean shutdown
}

var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Manage agent bead state",
	Long: `Manage state on agent beads for ZFC-compliant state reporting.

Agent beads (type=agent) can self-report their state using these commands.
This enables the Witness and other monitoring systems to track agent health.

States:
  idle      - Agent is waiting for work
  spawning  - Agent is starting up
  running   - Agent is executing (general)
  working   - Agent is actively working on a task
  stuck     - Agent is blocked and needs help
  done      - Agent completed its current work
  stopped   - Agent has cleanly shut down
  dead      - Agent died without clean shutdown (set by Witness via timeout)

Examples:
  bd agent state gt-emma running     # Set emma's state to running
  bd agent heartbeat gt-emma         # Update emma's last_activity timestamp
  bd agent show gt-emma              # Show emma's agent details`,
}

var agentStateCmd = &cobra.Command{
	Use:   "state <agent> <state>",
	Short: "Set agent state",
	Long: `Set the state of an agent bead.

This updates both the agent_state field and the last_activity timestamp.
Use this for ZFC-compliant state reporting.

Valid states: idle, spawning, running, working, stuck, done, stopped, dead

Examples:
  bd agent state gt-emma running   # Set state to running
  bd agent state gt-mayor idle     # Set state to idle`,
	Args: cobra.ExactArgs(2),
	RunE: runAgentState,
}

var agentHeartbeatCmd = &cobra.Command{
	Use:   "heartbeat <agent>",
	Short: "Update agent last_activity timestamp",
	Long: `Update the last_activity timestamp of an agent bead without changing state.

Use this for periodic heartbeats to indicate the agent is still alive.
The Witness can use this to detect dead agents via timeout.

Examples:
  bd agent heartbeat gt-emma   # Update emma's last_activity
  bd agent heartbeat gt-mayor  # Update mayor's last_activity`,
	Args: cobra.ExactArgs(1),
	RunE: runAgentHeartbeat,
}

var agentShowCmd = &cobra.Command{
	Use:   "show <agent>",
	Short: "Show agent bead details",
	Long: `Show detailed information about an agent bead.

Displays agent-specific fields including state, last_activity, hook, and role.

Examples:
  bd agent show gt-emma   # Show emma's agent details
  bd agent show gt-mayor  # Show mayor's agent details`,
	Args: cobra.ExactArgs(1),
	RunE: runAgentShow,
}

func init() {
	agentCmd.AddCommand(agentStateCmd)
	agentCmd.AddCommand(agentHeartbeatCmd)
	agentCmd.AddCommand(agentShowCmd)
	rootCmd.AddCommand(agentCmd)
}

func runAgentState(cmd *cobra.Command, args []string) error {
	CheckReadonly("agent state")

	agentArg := args[0]
	state := strings.ToLower(args[1])

	// Validate state
	if !validAgentStates[state] {
		validList := []string{}
		for s := range validAgentStates {
			validList = append(validList, s)
		}
		return fmt.Errorf("invalid state %q; valid states: %s", state, strings.Join(validList, ", "))
	}

	ctx := rootCtx

	// Resolve agent ID
	var agentID string
	if daemonClient != nil {
		resp, err := daemonClient.ResolveID(&rpc.ResolveIDArgs{ID: agentArg})
		if err != nil {
			return fmt.Errorf("failed to resolve agent %s: %w", agentArg, err)
		}
		if err := json.Unmarshal(resp.Data, &agentID); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
	} else {
		var err error
		agentID, err = utils.ResolvePartialID(ctx, store, agentArg)
		if err != nil {
			return fmt.Errorf("failed to resolve agent %s: %w", agentArg, err)
		}
	}

	// Get agent bead to verify it's an agent
	var agent *types.Issue
	if daemonClient != nil {
		resp, err := daemonClient.Show(&rpc.ShowArgs{ID: agentID})
		if err != nil {
			return fmt.Errorf("agent bead not found: %s", agentID)
		}
		if err := json.Unmarshal(resp.Data, &agent); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
	} else {
		var err error
		agent, err = store.GetIssue(ctx, agentID)
		if err != nil || agent == nil {
			return fmt.Errorf("agent bead not found: %s", agentID)
		}
	}

	// Verify agent bead is actually an agent
	if agent.IssueType != "agent" {
		return fmt.Errorf("%s is not an agent bead (type=%s)", agentID, agent.IssueType)
	}

	// Update state and last_activity
	updateLastActivity := true
	if daemonClient != nil {
		_, err := daemonClient.Update(&rpc.UpdateArgs{
			ID:           agentID,
			AgentState:   &state,
			LastActivity: &updateLastActivity,
		})
		if err != nil {
			return fmt.Errorf("failed to update agent state: %w", err)
		}
	} else {
		updates := map[string]interface{}{
			"agent_state":   state,
			"last_activity": time.Now(),
		}
		if err := store.UpdateIssue(ctx, agentID, updates, actor); err != nil {
			return fmt.Errorf("failed to update agent state: %w", err)
		}
	}

	// Trigger auto-flush
	if flushManager != nil {
		flushManager.MarkDirty(false)
	}

	if jsonOutput {
		result := map[string]interface{}{
			"agent":         agentID,
			"agent_state":   state,
			"last_activity": time.Now().Format(time.RFC3339),
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	fmt.Printf("%s %s state=%s\n", ui.RenderPass("✓"), agentID, state)
	return nil
}

func runAgentHeartbeat(cmd *cobra.Command, args []string) error {
	CheckReadonly("agent heartbeat")

	agentArg := args[0]

	ctx := rootCtx

	// Resolve agent ID
	var agentID string
	if daemonClient != nil {
		resp, err := daemonClient.ResolveID(&rpc.ResolveIDArgs{ID: agentArg})
		if err != nil {
			return fmt.Errorf("failed to resolve agent %s: %w", agentArg, err)
		}
		if err := json.Unmarshal(resp.Data, &agentID); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
	} else {
		var err error
		agentID, err = utils.ResolvePartialID(ctx, store, agentArg)
		if err != nil {
			return fmt.Errorf("failed to resolve agent %s: %w", agentArg, err)
		}
	}

	// Get agent bead to verify it's an agent
	var agent *types.Issue
	if daemonClient != nil {
		resp, err := daemonClient.Show(&rpc.ShowArgs{ID: agentID})
		if err != nil {
			return fmt.Errorf("agent bead not found: %s", agentID)
		}
		if err := json.Unmarshal(resp.Data, &agent); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
	} else {
		var err error
		agent, err = store.GetIssue(ctx, agentID)
		if err != nil || agent == nil {
			return fmt.Errorf("agent bead not found: %s", agentID)
		}
	}

	// Verify agent bead is actually an agent
	if agent.IssueType != "agent" {
		return fmt.Errorf("%s is not an agent bead (type=%s)", agentID, agent.IssueType)
	}

	// Update only last_activity
	updateLastActivity := true
	if daemonClient != nil {
		_, err := daemonClient.Update(&rpc.UpdateArgs{
			ID:           agentID,
			LastActivity: &updateLastActivity,
		})
		if err != nil {
			return fmt.Errorf("failed to update agent heartbeat: %w", err)
		}
	} else {
		updates := map[string]interface{}{
			"last_activity": time.Now(),
		}
		if err := store.UpdateIssue(ctx, agentID, updates, actor); err != nil {
			return fmt.Errorf("failed to update agent heartbeat: %w", err)
		}
	}

	// Trigger auto-flush
	if flushManager != nil {
		flushManager.MarkDirty(false)
	}

	if jsonOutput {
		result := map[string]interface{}{
			"agent":         agentID,
			"last_activity": time.Now().Format(time.RFC3339),
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	fmt.Printf("%s %s heartbeat\n", ui.RenderPass("✓"), agentID)
	return nil
}

func runAgentShow(cmd *cobra.Command, args []string) error {
	agentArg := args[0]

	ctx := rootCtx

	// Resolve agent ID
	var agentID string
	if daemonClient != nil {
		resp, err := daemonClient.ResolveID(&rpc.ResolveIDArgs{ID: agentArg})
		if err != nil {
			return fmt.Errorf("failed to resolve agent %s: %w", agentArg, err)
		}
		if err := json.Unmarshal(resp.Data, &agentID); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
	} else {
		var err error
		agentID, err = utils.ResolvePartialID(ctx, store, agentArg)
		if err != nil {
			return fmt.Errorf("failed to resolve agent %s: %w", agentArg, err)
		}
	}

	// Get agent bead
	var agent *types.Issue
	if daemonClient != nil {
		resp, err := daemonClient.Show(&rpc.ShowArgs{ID: agentID})
		if err != nil {
			return fmt.Errorf("agent bead not found: %s", agentID)
		}
		if err := json.Unmarshal(resp.Data, &agent); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
	} else {
		var err error
		agent, err = store.GetIssue(ctx, agentID)
		if err != nil || agent == nil {
			return fmt.Errorf("agent bead not found: %s", agentID)
		}
	}

	// Verify agent bead is actually an agent
	if agent.IssueType != "agent" {
		return fmt.Errorf("%s is not an agent bead (type=%s)", agentID, agent.IssueType)
	}

	if jsonOutput {
		result := map[string]interface{}{
			"id":            agentID,
			"title":         agent.Title,
			"agent_state":   emptyToNil(string(agent.AgentState)),
			"last_activity": formatTimeOrNil(agent.LastActivity),
			"hook_bead":     emptyToNil(agent.HookBead),
			"role_bead":     emptyToNil(agent.RoleBead),
			"role_type":     emptyToNil(agent.RoleType),
			"rig":           emptyToNil(agent.Rig),
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}

	// Human-readable output
	fmt.Printf("Agent: %s\n", agentID)
	fmt.Printf("Title: %s\n", agent.Title)
	fmt.Println()
	fmt.Println("State:")
	if agent.AgentState != "" {
		fmt.Printf("  agent_state: %s\n", agent.AgentState)
	} else {
		fmt.Println("  agent_state: (not set)")
	}
	if agent.LastActivity != nil {
		fmt.Printf("  last_activity: %s (%s ago)\n",
			agent.LastActivity.Format(time.RFC3339),
			time.Since(*agent.LastActivity).Round(time.Second))
	} else {
		fmt.Println("  last_activity: (not set)")
	}
	fmt.Println()
	fmt.Println("Identity:")
	if agent.RoleType != "" {
		fmt.Printf("  role_type: %s\n", agent.RoleType)
	} else {
		fmt.Println("  role_type: (not set)")
	}
	if agent.Rig != "" {
		fmt.Printf("  rig: %s\n", agent.Rig)
	} else {
		fmt.Println("  rig: (not set)")
	}
	fmt.Println()
	fmt.Println("Slots:")
	if agent.HookBead != "" {
		fmt.Printf("  hook: %s\n", agent.HookBead)
	} else {
		fmt.Println("  hook: (empty)")
	}
	if agent.RoleBead != "" {
		fmt.Printf("  role: %s\n", agent.RoleBead)
	} else {
		fmt.Println("  role: (empty)")
	}

	return nil
}

// formatTimeOrNil returns the time formatted as RFC3339 or nil if nil
func formatTimeOrNil(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	return t.Format(time.RFC3339)
}
