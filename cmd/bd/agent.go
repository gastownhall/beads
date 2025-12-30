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

var agentBackfillLabelsCmd = &cobra.Command{
	Use:   "backfill-labels",
	Short: "Backfill role_type/rig labels on existing agent beads",
	Long: `Backfill role_type and rig labels on existing agent beads.

This command scans all agent beads and:
1. Extracts role_type and rig from description text if fields are empty
2. Sets the role_type and rig fields on the agent bead
3. Adds role_type:<value> and rig:<value> labels for filtering

This enables queries like:
  bd list --type=agent --label=role_type:witness
  bd list --type=agent --label=rig:gastown

Use --dry-run to see what would be changed without making changes.

Examples:
  bd agent backfill-labels           # Backfill all agent beads
  bd agent backfill-labels --dry-run # Preview changes without applying`,
	RunE: runAgentBackfillLabels,
}

var backfillDryRun bool

func init() {
	agentBackfillLabelsCmd.Flags().BoolVar(&backfillDryRun, "dry-run", false, "Preview changes without applying them")
	agentCmd.AddCommand(agentStateCmd)
	agentCmd.AddCommand(agentHeartbeatCmd)
	agentCmd.AddCommand(agentShowCmd)
	agentCmd.AddCommand(agentBackfillLabelsCmd)
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

// runAgentBackfillLabels scans all agent beads and adds role_type/rig labels
func runAgentBackfillLabels(cmd *cobra.Command, args []string) error {
	if !backfillDryRun {
		CheckReadonly("agent backfill-labels")
	}

	ctx := rootCtx

	// List all agent beads
	var agents []*types.Issue
	if daemonClient != nil {
		resp, err := daemonClient.List(&rpc.ListArgs{
			IssueType: "agent",
		})
		if err != nil {
			return fmt.Errorf("failed to list agents: %w", err)
		}
		if err := json.Unmarshal(resp.Data, &agents); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
	} else {
		agentType := types.TypeAgent
		filter := types.IssueFilter{
			IssueType: &agentType,
		}
		var err error
		agents, err = store.SearchIssues(ctx, "", filter)
		if err != nil {
			return fmt.Errorf("failed to list agents: %w", err)
		}
	}

	if len(agents) == 0 {
		fmt.Println("No agent beads found")
		return nil
	}

	updated := 0
	skipped := 0

	for _, agent := range agents {
		// Skip tombstoned agents
		if agent.Status == types.StatusTombstone {
			continue
		}

		// Extract role_type and rig from description if not set in fields
		roleType := agent.RoleType
		rig := agent.Rig

		if roleType == "" || rig == "" {
			// Parse from description
			lines := strings.Split(agent.Description, "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "role_type:") && roleType == "" {
					roleType = strings.TrimSpace(strings.TrimPrefix(line, "role_type:"))
				}
				if strings.HasPrefix(line, "rig:") && rig == "" {
					rig = strings.TrimSpace(strings.TrimPrefix(line, "rig:"))
				}
			}
		}

		// Skip if no role_type or rig found
		if roleType == "" && rig == "" {
			skipped++
			continue
		}

		// Check if labels already exist
		var existingLabels []string
		if daemonClient != nil {
			// Use show to get full issue with labels
			resp, err := daemonClient.Show(&rpc.ShowArgs{ID: agent.ID})
			if err == nil {
				var fullAgent types.Issue
				if err := json.Unmarshal(resp.Data, &fullAgent); err == nil {
					existingLabels = fullAgent.Labels
				}
			}
		} else {
			existingLabels, _ = store.GetLabels(ctx, agent.ID)
		}

		// Determine which labels need to be added
		needsRoleTypeLabel := roleType != "" && !containsLabel(existingLabels, "role_type:"+roleType)
		needsRigLabel := rig != "" && !containsLabel(existingLabels, "rig:"+rig)
		needsFieldUpdate := (roleType != "" && agent.RoleType == "") || (rig != "" && agent.Rig == "")

		if !needsRoleTypeLabel && !needsRigLabel && !needsFieldUpdate {
			skipped++
			continue
		}

		if backfillDryRun {
			fmt.Printf("Would update %s:\n", agent.ID)
			if needsFieldUpdate {
				if roleType != "" && agent.RoleType == "" {
					fmt.Printf("  Set role_type: %s\n", roleType)
				}
				if rig != "" && agent.Rig == "" {
					fmt.Printf("  Set rig: %s\n", rig)
				}
			}
			if needsRoleTypeLabel {
				fmt.Printf("  Add label: role_type:%s\n", roleType)
			}
			if needsRigLabel {
				fmt.Printf("  Add label: rig:%s\n", rig)
			}
			updated++
			continue
		}

		// Update fields if needed
		if needsFieldUpdate {
			updates := map[string]interface{}{}
			if roleType != "" && agent.RoleType == "" {
				updates["role_type"] = roleType
			}
			if rig != "" && agent.Rig == "" {
				updates["rig"] = rig
			}

			if daemonClient != nil {
				updateArgs := &rpc.UpdateArgs{ID: agent.ID}
				if _, ok := updates["role_type"]; ok {
					rt := roleType
					updateArgs.RoleType = &rt
				}
				if _, ok := updates["rig"]; ok {
					r := rig
					updateArgs.Rig = &r
				}
				if _, err := daemonClient.Update(updateArgs); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to update fields for %s: %v\n", agent.ID, err)
				}
			} else {
				if err := store.UpdateIssue(ctx, agent.ID, updates, actor); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to update fields for %s: %v\n", agent.ID, err)
				}
			}
		}

		// Add labels
		if needsRoleTypeLabel {
			label := "role_type:" + roleType
			if daemonClient != nil {
				if _, err := daemonClient.AddLabel(&rpc.LabelAddArgs{ID: agent.ID, Label: label}); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to add label %s to %s: %v\n", label, agent.ID, err)
				}
			} else {
				if err := store.AddLabel(ctx, agent.ID, label, actor); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to add label %s to %s: %v\n", label, agent.ID, err)
				}
			}
		}
		if needsRigLabel {
			label := "rig:" + rig
			if daemonClient != nil {
				if _, err := daemonClient.AddLabel(&rpc.LabelAddArgs{ID: agent.ID, Label: label}); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to add label %s to %s: %v\n", label, agent.ID, err)
				}
			} else {
				if err := store.AddLabel(ctx, agent.ID, label, actor); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to add label %s to %s: %v\n", label, agent.ID, err)
				}
			}
		}

		fmt.Printf("%s Updated %s (role_type:%s, rig:%s)\n", ui.RenderPass("✓"), agent.ID, roleType, rig)
		updated++
	}

	// Trigger auto-flush
	if flushManager != nil && !backfillDryRun {
		flushManager.MarkDirty(false)
	}

	if backfillDryRun {
		fmt.Printf("\nDry run complete: %d would be updated, %d skipped\n", updated, skipped)
	} else {
		fmt.Printf("\nBackfill complete: %d updated, %d skipped\n", updated, skipped)
	}

	return nil
}

// containsLabel checks if a label exists in the list
func containsLabel(labels []string, label string) bool {
	for _, l := range labels {
		if l == label {
			return true
		}
	}
	return false
}
