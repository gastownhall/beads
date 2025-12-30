package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/routing"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/validation"
)

var createCmd = &cobra.Command{
	Use:     "create [title]",
	GroupID: "issues",
	Aliases: []string{"new"},
	Short:   "Create a new issue (or multiple issues from markdown file)",
	Args:    cobra.MinimumNArgs(0), // Changed to allow no args when using -f
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("create")
		file, _ := cmd.Flags().GetString("file")

		// If file flag is provided, parse markdown and create multiple issues
		if file != "" {
			if len(args) > 0 {
				FatalError("cannot specify both title and --file flag")
			}
			createIssuesFromMarkdown(cmd, file)
			return
		}

		// Original single-issue creation logic
		// Get title from flag or positional argument
		titleFlag, _ := cmd.Flags().GetString("title")
		var title string

		if len(args) > 0 && titleFlag != "" {
			// Both provided - check if they match
			if args[0] != titleFlag {
				FatalError("cannot specify different titles as both positional argument and --title flag\n  Positional: %q\n  --title:    %q", args[0], titleFlag)
			}
			title = args[0] // They're the same, use either
		} else if len(args) > 0 {
			title = args[0]
		} else if titleFlag != "" {
			title = titleFlag
		} else {
			FatalError("title required (or use --file to create from markdown)")
		}

		// Get silent flag
		silent, _ := cmd.Flags().GetBool("silent")

		// Warn if creating a test issue in production database (unless silent mode)
		if strings.HasPrefix(strings.ToLower(title), "test") && !silent && !debug.IsQuiet() {
			fmt.Fprintf(os.Stderr, "%s Creating issue with 'Test' prefix in production database.\n", ui.RenderWarn("⚠"))
			fmt.Fprintf(os.Stderr, "  For testing, consider using: BEADS_DB=/tmp/test.db ./bd create \"Test issue\"\n")
		}

		// Get field values
		description, _ := getDescriptionFlag(cmd)

		// Check if description is required by config
		if description == "" && !strings.Contains(strings.ToLower(title), "test") {
			if config.GetBool("create.require-description") {
				FatalError("description is required (set create.require-description: false in config.yaml to disable)")
			}
			// Warn if creating an issue without a description (unless silent mode)
			if !silent && !debug.IsQuiet() {
				fmt.Fprintf(os.Stderr, "%s Creating issue without description.\n", ui.RenderWarn("⚠"))
				fmt.Fprintf(os.Stderr, "  Issues without descriptions lack context for future work.\n")
				fmt.Fprintf(os.Stderr, "  Consider adding --description=\"Why this issue exists and what needs to be done\"\n")
			}
		}

		design, _ := cmd.Flags().GetString("design")
		acceptance, _ := cmd.Flags().GetString("acceptance")

		// Parse priority (supports both "1" and "P1" formats)
		priorityStr, _ := cmd.Flags().GetString("priority")
		priority, err := validation.ValidatePriority(priorityStr)
		if err != nil {
			FatalError("%v", err)
		}

		issueType, _ := cmd.Flags().GetString("type")
		assignee, _ := cmd.Flags().GetString("assignee")

		labels, _ := cmd.Flags().GetStringSlice("labels")
		labelAlias, _ := cmd.Flags().GetStringSlice("label")
		if len(labelAlias) > 0 {
			labels = append(labels, labelAlias...)
		}

		explicitID, _ := cmd.Flags().GetString("id")
		parentID, _ := cmd.Flags().GetString("parent")
		externalRef, _ := cmd.Flags().GetString("external-ref")
		deps, _ := cmd.Flags().GetStringSlice("deps")
		waitsFor, _ := cmd.Flags().GetString("waits-for")
		waitsForGate, _ := cmd.Flags().GetString("waits-for-gate")
		forceCreate, _ := cmd.Flags().GetBool("force")
		repoOverride, _ := cmd.Flags().GetString("repo")
		rigOverride, _ := cmd.Flags().GetString("rig")
		prefixOverride, _ := cmd.Flags().GetString("prefix")
		wisp, _ := cmd.Flags().GetBool("ephemeral")
		molTypeStr, _ := cmd.Flags().GetString("mol-type")
		var molType types.MolType
		if molTypeStr != "" {
			molType = types.MolType(molTypeStr)
			if !molType.IsValid() {
				FatalError("invalid mol-type %q (must be swarm, patrol, or work)", molTypeStr)
			}
		}

		// Agent-specific flags
		roleType, _ := cmd.Flags().GetString("role-type")
		agentRig, _ := cmd.Flags().GetString("agent-rig")

		// Validate agent-specific flags require --type=agent
		if (roleType != "" || agentRig != "") && issueType != "agent" {
			FatalError("--role-type and --agent-rig flags require --type=agent")
		}

		// Handle --rig or --prefix flag: create issue in a different rig
		// Both flags use the same forgiving lookup (accepts rig names or prefixes)
		targetRig := rigOverride
		if prefixOverride != "" {
			if targetRig != "" {
				FatalError("cannot specify both --rig and --prefix flags")
			}
			targetRig = prefixOverride
		}
		if targetRig != "" {
			createInRig(cmd, targetRig, title, description, issueType, priority, design, acceptance, assignee, labels, externalRef, wisp)
			return
		}

		// Get estimate if provided
		var estimatedMinutes *int
		if cmd.Flags().Changed("estimate") {
			est, _ := cmd.Flags().GetInt("estimate")
			if est < 0 {
				FatalError("estimate must be a non-negative number of minutes")
			}
			estimatedMinutes = &est
		}
		// Use global jsonOutput set by PersistentPreRun

		// Determine target repository using routing logic
		repoPath := "." // default to current directory
		if cmd.Flags().Changed("repo") {
			// Explicit --repo flag overrides auto-routing
			repoPath = repoOverride
		} else {
			// Auto-routing based on user role
			userRole, err := routing.DetectUserRole(".")
			if err != nil {
				debug.Logf("Warning: failed to detect user role: %v\n", err)
			}
			
			routingConfig := &routing.RoutingConfig{
				Mode:             config.GetString("routing.mode"),
				DefaultRepo:      config.GetString("routing.default"),
				MaintainerRepo:   config.GetString("routing.maintainer"),
				ContributorRepo:  config.GetString("routing.contributor"),
				ExplicitOverride: repoOverride,
			}
			
			repoPath = routing.DetermineTargetRepo(routingConfig, userRole, ".")
		}
		
		// TODO(bd-6x6g): Switch to target repo for multi-repo support
		// For now, we just log the target repo in debug mode
		if repoPath != "." {
			debug.Logf("DEBUG: Target repo: %s\n", repoPath)
		}

		// Check for conflicting flags
		if explicitID != "" && parentID != "" {
			FatalError("cannot specify both --id and --parent flags")
		}

		// If parent is specified, generate child ID
		// In daemon mode, the parent will be sent to the RPC handler
		// In direct mode, we generate the child ID here
		if parentID != "" && daemonClient == nil {
			ctx := rootCtx
			// Validate parent exists before generating child ID
			parentIssue, err := store.GetIssue(ctx, parentID)
			if err != nil {
				FatalError("failed to check parent issue: %v", err)
			}
			if parentIssue == nil {
				FatalError("parent issue %s not found", parentID)
			}
			childID, err := store.GetNextChildID(ctx, parentID)
			if err != nil {
				FatalError("%v", err)
			}
			explicitID = childID // Set as explicit ID for the rest of the flow
		}

		// Validate explicit ID format if provided
		if explicitID != "" {
			requestedPrefix, err := validation.ValidateIDFormat(explicitID)
			if err != nil {
				FatalError("%v", err)
			}

			// Validate prefix matches database prefix
			ctx := rootCtx

			// Get database prefix from config
			var dbPrefix string
			if daemonClient != nil {
				// Daemon mode - use RPC to get config
				configResp, err := daemonClient.GetConfig(&rpc.GetConfigArgs{Key: "issue_prefix"})
				if err == nil {
					dbPrefix = configResp.Value
				}
				// If error, continue without validation (non-fatal)
			} else {
				// Direct mode - check config
				dbPrefix, _ = store.GetConfig(ctx, "issue_prefix")
			}

			if err := validation.ValidatePrefix(requestedPrefix, dbPrefix, forceCreate); err != nil {
				FatalError("%v", err)
			}

			// Validate agent ID pattern if type is agent
			if issueType == "agent" {
				if err := validation.ValidateAgentID(explicitID); err != nil {
					FatalError("invalid agent ID: %v", err)
				}
			}
		}

		var externalRefPtr *string
		if externalRef != "" {
			externalRefPtr = &externalRef
		}

		// If daemon is running, use RPC
		if daemonClient != nil {
			createArgs := &rpc.CreateArgs{
				ID:                 explicitID,
				Parent:             parentID,
				Title:              title,
				Description:        description,
				IssueType:          issueType,
				Priority:           priority,
				Design:             design,
				AcceptanceCriteria: acceptance,
				Assignee:           assignee,
				ExternalRef:        externalRef,
				EstimatedMinutes:   estimatedMinutes,
				Labels:             labels,
				Dependencies:       deps,
				WaitsFor:           waitsFor,
				WaitsForGate:       waitsForGate,
				Ephemeral:          wisp,
				CreatedBy:          getActorWithGit(),
				MolType:            string(molType),
				RoleType:           roleType,
				Rig:                agentRig,
			}

			resp, err := daemonClient.Create(createArgs)
			if err != nil {
				FatalError("%v", err)
			}

			// Parse response to get issue for hook
			var issue types.Issue
			if err := json.Unmarshal(resp.Data, &issue); err != nil {
				FatalError("parsing response: %v", err)
			}

			// Run create hook
			if hookRunner != nil {
				hookRunner.Run(hooks.EventCreate, &issue)
			}

			if jsonOutput {
				fmt.Println(string(resp.Data))
			} else if silent {
				fmt.Println(issue.ID)
			} else {
				fmt.Printf("%s Created issue: %s\n", ui.RenderPass("✓"), issue.ID)
				fmt.Printf("  Title: %s\n", issue.Title)
				fmt.Printf("  Priority: P%d\n", issue.Priority)
				fmt.Printf("  Status: %s\n", issue.Status)
			}
			return
		}

		// Direct mode
		issue := &types.Issue{
			ID:                 explicitID, // Set explicit ID if provided (empty string if not)
			Title:              title,
			Description:        description,
			Design:             design,
			AcceptanceCriteria: acceptance,
			Status:             types.StatusOpen,
			Priority:           priority,
			IssueType:          types.IssueType(issueType),
			Assignee:           assignee,
			ExternalRef:        externalRefPtr,
			EstimatedMinutes:   estimatedMinutes,
			Ephemeral:          wisp,
			CreatedBy:          getActorWithGit(),
			MolType:            molType,
			RoleType:           roleType,
			Rig:                agentRig,
		}

		ctx := rootCtx
		
		// Check if any dependencies are discovered-from type
		// If so, inherit source_repo from the parent issue
		var discoveredFromParentID string
		for _, depSpec := range deps {
			depSpec = strings.TrimSpace(depSpec)
			if depSpec == "" {
				continue
			}
			
			var depType types.DependencyType
			var dependsOnID string
			
			if strings.Contains(depSpec, ":") {
				parts := strings.SplitN(depSpec, ":", 2)
				if len(parts) == 2 {
					depType = types.DependencyType(strings.TrimSpace(parts[0]))
					dependsOnID = strings.TrimSpace(parts[1])
					
					if depType == types.DepDiscoveredFrom && dependsOnID != "" {
						discoveredFromParentID = dependsOnID
						break
					}
				}
			}
		}
		
		// If we found a discovered-from dependency, inherit source_repo from parent
		if discoveredFromParentID != "" {
			parentIssue, err := store.GetIssue(ctx, discoveredFromParentID)
			if err == nil && parentIssue.SourceRepo != "" {
				issue.SourceRepo = parentIssue.SourceRepo
			}
			// If error getting parent or parent has no source_repo, continue with default
		}
		
		if err := store.CreateIssue(ctx, issue, actor); err != nil {
			FatalError("%v", err)
		}

		// If parent was specified, add parent-child dependency
		if parentID != "" {
			dep := &types.Dependency{
				IssueID:     issue.ID,
				DependsOnID: parentID,
				Type:        types.DepParentChild,
			}
			if err := store.AddDependency(ctx, dep, actor); err != nil {
				WarnError("failed to add parent-child dependency %s -> %s: %v", issue.ID, parentID, err)
			}
		}

		// Add labels if specified
		for _, label := range labels {
			if err := store.AddLabel(ctx, issue.ID, label, actor); err != nil {
				WarnError("failed to add label %s: %v", label, err)
			}
		}

		// Auto-add role_type/rig labels for agent beads (enables filtering queries)
		if issue.IssueType == types.TypeAgent {
			if issue.RoleType != "" {
				agentLabel := "role_type:" + issue.RoleType
				if err := store.AddLabel(ctx, issue.ID, agentLabel, actor); err != nil {
					WarnError("failed to add role_type label: %v", err)
				}
			}
			if issue.Rig != "" {
				rigLabel := "rig:" + issue.Rig
				if err := store.AddLabel(ctx, issue.ID, rigLabel, actor); err != nil {
					WarnError("failed to add rig label: %v", err)
				}
			}
		}

		// Add dependencies if specified (format: type:id or just id for default "blocks" type)
		for _, depSpec := range deps {
			// Skip empty specs (e.g., from trailing commas)
			depSpec = strings.TrimSpace(depSpec)
			if depSpec == "" {
				continue
			}

			var depType types.DependencyType
			var dependsOnID string

			// Parse format: "type:id" or just "id" (defaults to "blocks")
			if strings.Contains(depSpec, ":") {
				parts := strings.SplitN(depSpec, ":", 2)
				if len(parts) != 2 {
					WarnError("invalid dependency format '%s', expected 'type:id' or 'id'", depSpec)
					continue
				}
				depType = types.DependencyType(strings.TrimSpace(parts[0]))
				dependsOnID = strings.TrimSpace(parts[1])
			} else {
				// Default to "blocks" if no type specified
				depType = types.DepBlocks
				dependsOnID = depSpec
			}

			// Validate dependency type
			if !depType.IsValid() {
				WarnError("invalid dependency type '%s' (valid: blocks, related, parent-child, discovered-from)", depType)
				continue
			}

			// Add the dependency
			dep := &types.Dependency{
				IssueID:     issue.ID,
				DependsOnID: dependsOnID,
				Type:        depType,
			}
			if err := store.AddDependency(ctx, dep, actor); err != nil {
				WarnError("failed to add dependency %s -> %s: %v", issue.ID, dependsOnID, err)
			}
		}

		// Add waits-for dependency if specified
		if waitsFor != "" {
			// Validate gate type
			gate := waitsForGate
			if gate == "" {
				gate = types.WaitsForAllChildren
			}
			if gate != types.WaitsForAllChildren && gate != types.WaitsForAnyChildren {
				FatalError("invalid --waits-for-gate value '%s' (valid: all-children, any-children)", gate)
			}

			// Create metadata JSON
			meta := types.WaitsForMeta{
				Gate: gate,
			}
			metaJSON, err := json.Marshal(meta)
			if err != nil {
				FatalError("failed to serialize waits-for metadata: %v", err)
			}

			dep := &types.Dependency{
				IssueID:     issue.ID,
				DependsOnID: waitsFor,
				Type:        types.DepWaitsFor,
				Metadata:    string(metaJSON),
			}
			if err := store.AddDependency(ctx, dep, actor); err != nil {
				WarnError("failed to add waits-for dependency %s -> %s: %v", issue.ID, waitsFor, err)
			}
		}

		// Schedule auto-flush
		markDirtyAndScheduleFlush()

		// Run create hook
		if hookRunner != nil {
			hookRunner.Run(hooks.EventCreate, issue)
		}

		if jsonOutput {
			outputJSON(issue)
		} else if silent {
			fmt.Println(issue.ID)
		} else {
			fmt.Printf("%s Created issue: %s\n", ui.RenderPass("✓"), issue.ID)
			fmt.Printf("  Title: %s\n", issue.Title)
			fmt.Printf("  Priority: P%d\n", issue.Priority)
			fmt.Printf("  Status: %s\n", issue.Status)

			// Show tip after successful create (direct mode only)
			maybeShowTip(store)
		}
	},
}

func init() {
	createCmd.Flags().StringP("file", "f", "", "Create multiple issues from markdown file")
	createCmd.Flags().String("title", "", "Issue title (alternative to positional argument)")
	createCmd.Flags().Bool("silent", false, "Output only the issue ID (for scripting)")
	registerPriorityFlag(createCmd, "2")
	createCmd.Flags().StringP("type", "t", "task", "Issue type (bug|feature|task|epic|chore|merge-request|molecule|gate|agent|role|convoy)")
	registerCommonIssueFlags(createCmd)
	createCmd.Flags().StringSliceP("labels", "l", []string{}, "Labels (comma-separated)")
	createCmd.Flags().StringSlice("label", []string{}, "Alias for --labels")
	_ = createCmd.Flags().MarkHidden("label") // Only fails if flag missing (caught in tests)
	createCmd.Flags().String("id", "", "Explicit issue ID (e.g., 'bd-42' for partitioning)")
	createCmd.Flags().String("parent", "", "Parent issue ID for hierarchical child (e.g., 'bd-a3f8e9')")
	createCmd.Flags().StringSlice("deps", []string{}, "Dependencies in format 'type:id' or 'id' (e.g., 'discovered-from:bd-20,blocks:bd-15' or 'bd-20')")
	createCmd.Flags().String("waits-for", "", "Spawner issue ID to wait for (creates waits-for dependency for fanout gate)")
	createCmd.Flags().String("waits-for-gate", "all-children", "Gate type: all-children (wait for all) or any-children (wait for first)")
	createCmd.Flags().Bool("force", false, "Force creation even if prefix doesn't match database prefix")
	createCmd.Flags().String("repo", "", "Target repository for issue (overrides auto-routing)")
	createCmd.Flags().String("rig", "", "Create issue in a different rig (e.g., --rig beads)")
	createCmd.Flags().String("prefix", "", "Create issue in rig by prefix (e.g., --prefix bd- or --prefix bd or --prefix beads)")
	createCmd.Flags().IntP("estimate", "e", 0, "Time estimate in minutes (e.g., 60 for 1 hour)")
	createCmd.Flags().Bool("ephemeral", false, "Create as ephemeral (ephemeral, not exported to JSONL)")
	createCmd.Flags().String("mol-type", "", "Molecule type: swarm (multi-polecat), patrol (recurring ops), work (default)")
	// Agent-specific flags (only valid when --type=agent)
	createCmd.Flags().String("role-type", "", "Agent role type: polecat|crew|witness|refinery|mayor|deacon (requires --type=agent)")
	createCmd.Flags().String("agent-rig", "", "Agent's rig name (requires --type=agent)")
	// Note: --json flag is defined as a persistent flag in main.go, not here
	rootCmd.AddCommand(createCmd)
}

// createInRig creates an issue in a different rig using --rig flag.
// This bypasses the normal daemon/direct flow and directly creates in the target rig.
func createInRig(cmd *cobra.Command, rigName, title, description, issueType string, priority int, design, acceptance, assignee string, labels []string, externalRef string, wisp bool) {
	ctx := rootCtx

	// Find the town-level beads directory (where routes.jsonl lives)
	townBeadsDir, err := findTownBeadsDir()
	if err != nil {
		FatalError("cannot use --rig: %v", err)
	}

	// Resolve the target rig's beads directory
	targetBeadsDir, _, err := routing.ResolveBeadsDirForRig(rigName, townBeadsDir)
	if err != nil {
		FatalError("%v", err)
	}

	// Open storage for the target rig
	targetDBPath := filepath.Join(targetBeadsDir, "beads.db")
	targetStore, err := sqlite.New(ctx, targetDBPath)
	if err != nil {
		FatalError("failed to open rig %q database: %v", rigName, err)
	}
	defer func() {
		if err := targetStore.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to close rig database: %v\n", err)
		}
	}()

	var externalRefPtr *string
	if externalRef != "" {
		externalRefPtr = &externalRef
	}

	// Create issue without ID - CreateIssue will generate one with the correct prefix
	issue := &types.Issue{
		Title:              title,
		Description:        description,
		Design:             design,
		AcceptanceCriteria: acceptance,
		Status:             types.StatusOpen,
		Priority:           priority,
		IssueType:          types.IssueType(issueType),
		Assignee:           assignee,
		ExternalRef:        externalRefPtr,
		Ephemeral:          wisp,
		CreatedBy:          getActorWithGit(),
	}

	if err := targetStore.CreateIssue(ctx, issue, actor); err != nil {
		FatalError("failed to create issue in rig %q: %v", rigName, err)
	}

	// Add labels if specified
	for _, label := range labels {
		if err := targetStore.AddLabel(ctx, issue.ID, label, actor); err != nil {
			WarnError("failed to add label %s: %v", label, err)
		}
	}

	// Get silent flag
	silent, _ := cmd.Flags().GetBool("silent")

	if jsonOutput {
		outputJSON(issue)
	} else if silent {
		fmt.Println(issue.ID)
	} else {
		fmt.Printf("%s Created issue in rig %q: %s\n", ui.RenderPass("✓"), rigName, issue.ID)
		fmt.Printf("  Title: %s\n", issue.Title)
		fmt.Printf("  Priority: P%d\n", issue.Priority)
		fmt.Printf("  Status: %s\n", issue.Status)
	}
}

// findTownBeadsDir finds the town-level .beads directory (where routes.jsonl lives).
// It walks up from the current directory looking for a .beads directory with routes.jsonl.
func findTownBeadsDir() (string, error) {
	// Start from current directory and walk up
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	for {
		beadsDir := filepath.Join(dir, ".beads")
		routesFile := filepath.Join(beadsDir, routing.RoutesFileName)

		// Check if this .beads directory has routes.jsonl
		if _, err := os.Stat(routesFile); err == nil {
			return beadsDir, nil
		}

		// Move up one directory
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root
			break
		}
		dir = parent
	}

	return "", fmt.Errorf("no routes.jsonl found in any parent .beads directory")
}
