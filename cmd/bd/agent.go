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

	// Resolve agent ID with routing support - if not found, we'll auto-create the agent bead
	var agentID string
	var notFound bool
	var routedResult *RoutedResult

	// Check if routing is needed (bypass daemon for cross-repo lookups)
	if needsRouting(agentArg) || daemonClient == nil {
		// Use routed resolution for cross-repo lookups
		var err error
		routedResult, err = resolveAndGetIssueWithRouting(ctx, store, agentArg)
		if err != nil {
			if routedResult != nil {
				routedResult.Close()
			}
			// Check if it's a "not found" error
			if strings.Contains(err.Error(), "no issue found matching") {
				notFound = true
				agentID = agentArg // Use the input as the ID for creation
			} else {
				return fmt.Errorf("failed to resolve agent %s: %w", agentArg, err)
			}
		} else if routedResult != nil && routedResult.Issue != nil {
			agentID = routedResult.ResolvedID
		} else {
			if routedResult != nil {
				routedResult.Close()
			}
			notFound = true
			agentID = agentArg
		}
	} else if daemonClient != nil {
		resp, err := daemonClient.ResolveID(&rpc.ResolveIDArgs{ID: agentArg})
		if err != nil {
			// Check if it's a "not found" error
			if strings.Contains(err.Error(), "no issue found matching") {
				notFound = true
				agentID = agentArg // Use the input as the ID for creation
			} else {
				return fmt.Errorf("failed to resolve agent %s: %w", agentArg, err)
			}
		} else {
			if err := json.Unmarshal(resp.Data, &agentID); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}
		}
	}

	// Determine which store to use (routed or local)
	activeStore := store
	if routedResult != nil && routedResult.Routed {
		activeStore = routedResult.Store
		defer routedResult.Close()
	}

	var agent *types.Issue

	// If agent not found, auto-create it
	if notFound {
		roleType, rig := parseAgentIDFields(agentID)
		agent = &types.Issue{
			ID:        agentID,
			Title:     fmt.Sprintf("Agent: %s", agentID),
			IssueType: types.TypeAgent,
			Status:    types.StatusOpen,
			RoleType:  roleType,
			Rig:       rig,
			CreatedBy: actor,
		}

		if daemonClient != nil && !needsRouting(agentArg) {
			createArgs := &rpc.CreateArgs{
				ID:        agentID,
				Title:     agent.Title,
				IssueType: string(types.TypeAgent),
				RoleType:  roleType,
				Rig:       rig,
				CreatedBy: actor,
			}
			resp, err := daemonClient.Create(createArgs)
			if err != nil {
				return fmt.Errorf("failed to auto-create agent bead %s: %w", agentID, err)
			}
			if err := json.Unmarshal(resp.Data, &agent); err != nil {
				return fmt.Errorf("parsing create response: %w", err)
			}
		} else {
			if err := activeStore.CreateIssue(ctx, agent, actor); err != nil {
				return fmt.Errorf("failed to auto-create agent bead %s: %w", agentID, err)
			}
			// Add role_type and rig labels for filtering
			if roleType != "" {
				if err := activeStore.AddLabel(ctx, agent.ID, "role_type:"+roleType, actor); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to add role_type label: %v\n", err)
				}
			}
			if rig != "" {
				if err := activeStore.AddLabel(ctx, agent.ID, "rig:"+rig, actor); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to add rig label: %v\n", err)
				}
			}
		}
	} else {
		// Get existing agent bead to verify it's an agent
		if routedResult != nil && routedResult.Issue != nil {
			// Already have the issue from routed resolution
			agent = routedResult.Issue
		} else if daemonClient != nil && !needsRouting(agentArg) {
			resp, err := daemonClient.Show(&rpc.ShowArgs{ID: agentID})
			if err != nil {
				return fmt.Errorf("agent bead not found: %s", agentID)
			}
			if err := json.Unmarshal(resp.Data, &agent); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}
		} else {
			var err error
			agent, err = activeStore.GetIssue(ctx, agentID)
			if err != nil || agent == nil {
				return fmt.Errorf("agent bead not found: %s", agentID)
			}
		}

		// Verify agent bead is actually an agent
		if agent.IssueType != "agent" {
			return fmt.Errorf("%s is not an agent bead (type=%s)", agentID, agent.IssueType)
		}
	}

	// Update state and last_activity
	updateLastActivity := true
	if daemonClient != nil && !needsRouting(agentArg) {
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
		if err := activeStore.UpdateIssue(ctx, agentID, updates, actor); err != nil {
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

	// Resolve agent ID with routing support
	var agentID string
	var routedResult *RoutedResult

	// Check if routing is needed (bypass daemon for cross-repo lookups)
	if needsRouting(agentArg) || daemonClient == nil {
		// Use routed resolution for cross-repo lookups
		var err error
		routedResult, err = resolveAndGetIssueWithRouting(ctx, store, agentArg)
		if err != nil {
			if routedResult != nil {
				routedResult.Close()
			}
			return fmt.Errorf("failed to resolve agent %s: %w", agentArg, err)
		}
		if routedResult == nil || routedResult.Issue == nil {
			if routedResult != nil {
				routedResult.Close()
			}
			return fmt.Errorf("agent bead not found: %s", agentArg)
		}
		agentID = routedResult.ResolvedID
	} else if daemonClient != nil {
		resp, err := daemonClient.ResolveID(&rpc.ResolveIDArgs{ID: agentArg})
		if err != nil {
			return fmt.Errorf("failed to resolve agent %s: %w", agentArg, err)
		}
		if err := json.Unmarshal(resp.Data, &agentID); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
	}

	// Determine which store to use (routed or local)
	activeStore := store
	if routedResult != nil && routedResult.Routed {
		activeStore = routedResult.Store
		defer routedResult.Close()
	}

	// Get agent bead to verify it's an agent
	var agent *types.Issue
	if routedResult != nil && routedResult.Issue != nil {
		// Already have the issue from routed resolution
		agent = routedResult.Issue
	} else if daemonClient != nil && !needsRouting(agentArg) {
		resp, err := daemonClient.Show(&rpc.ShowArgs{ID: agentID})
		if err != nil {
			return fmt.Errorf("agent bead not found: %s", agentID)
		}
		if err := json.Unmarshal(resp.Data, &agent); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
	} else {
		var err error
		agent, err = activeStore.GetIssue(ctx, agentID)
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
	if daemonClient != nil && !needsRouting(agentArg) {
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
		if err := activeStore.UpdateIssue(ctx, agentID, updates, actor); err != nil {
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

	// Resolve agent ID with routing support
	var agentID string
	var routedResult *RoutedResult

	// Check if routing is needed (bypass daemon for cross-repo lookups)
	if needsRouting(agentArg) || daemonClient == nil {
		// Use routed resolution for cross-repo lookups
		var err error
		routedResult, err = resolveAndGetIssueWithRouting(ctx, store, agentArg)
		if err != nil {
			if routedResult != nil {
				routedResult.Close()
			}
			return fmt.Errorf("failed to resolve agent %s: %w", agentArg, err)
		}
		if routedResult == nil || routedResult.Issue == nil {
			if routedResult != nil {
				routedResult.Close()
			}
			return fmt.Errorf("agent bead not found: %s", agentArg)
		}
		agentID = routedResult.ResolvedID
		defer routedResult.Close()
	} else if daemonClient != nil {
		resp, err := daemonClient.ResolveID(&rpc.ResolveIDArgs{ID: agentArg})
		if err != nil {
			return fmt.Errorf("failed to resolve agent %s: %w", agentArg, err)
		}
		if err := json.Unmarshal(resp.Data, &agentID); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
	}

	// Get agent bead
	var agent *types.Issue
	if routedResult != nil && routedResult.Issue != nil {
		// Already have the issue from routed resolution
		agent = routedResult.Issue
	} else if daemonClient != nil && !needsRouting(agentArg) {
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

// parseAgentIDFields extracts role_type and rig from an agent bead ID.
// Agent ID patterns:
//   - Town-level: <prefix>-<role> (e.g., gt-mayor) → role="mayor", rig=""
//   - Per-rig singleton: <prefix>-<rig>-<role> (e.g., gt-gastown-witness) → role="witness", rig="gastown"
//   - Per-rig named: <prefix>-<rig>-<role>-<name> (e.g., gt-gastown-polecat-nux) → role="polecat", rig="gastown"
func parseAgentIDFields(agentID string) (roleType, rig string) {
	// Must contain a hyphen to have a prefix
	hyphenIdx := strings.Index(agentID, "-")
	if hyphenIdx <= 0 {
		return "", ""
	}

	// Split into parts after the prefix
	rest := agentID[hyphenIdx+1:] // Skip "<prefix>-"
	parts := strings.Split(rest, "-")

	if len(parts) < 1 {
		return "", ""
	}

	// Known roles for classification
	townLevelRoles := map[string]bool{"mayor": true, "deacon": true}
	rigLevelRoles := map[string]bool{"witness": true, "refinery": true}
	namedRoles := map[string]bool{"crew": true, "polecat": true}

	// Case 1: Town-level roles (gt-mayor, gt-deacon)
	if len(parts) == 1 {
		role := parts[0]
		if townLevelRoles[role] {
			return role, ""
		}
		return "", "" // Unknown format
	}

	// Case 2: Rig-level roles (<prefix>-<rig>-witness, <prefix>-<rig>-refinery)
	if len(parts) == 2 {
		potentialRig, potentialRole := parts[0], parts[1]
		if rigLevelRoles[potentialRole] {
			return potentialRole, potentialRig
		}
		return "", "" // Unknown format
	}

	// Case 3: Named roles (<prefix>-<rig>-crew-<name>, <prefix>-<rig>-polecat-<name>)
	if len(parts) >= 3 {
		potentialRig, potentialRole := parts[0], parts[1]
		if namedRoles[potentialRole] {
			return potentialRole, potentialRig
		}
	}

	return "", "" // Unknown format
}
