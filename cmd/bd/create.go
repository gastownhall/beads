package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/routing"
	"github.com/steveyegge/beads/internal/rpc"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/validation"
)

var createCmd = &cobra.Command{
	Use:     "create [title]",
	Aliases: []string{"new"},
	Short:   "Create a new issue (or multiple issues from markdown file)",
	Args:    cobra.MinimumNArgs(0), // Changed to allow no args when using -f
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("create")
		file, _ := cmd.Flags().GetString("file")
		fromTemplate, _ := cmd.Flags().GetString("from-template")

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

		// Warn if creating a test issue in production database
		if strings.HasPrefix(strings.ToLower(title), "test") {
			yellow := color.New(color.FgYellow).SprintFunc()
			fmt.Fprintf(os.Stderr, "%s Creating issue with 'Test' prefix in production database.\n", yellow("⚠"))
			fmt.Fprintf(os.Stderr, "  For testing, consider using: BEADS_DB=/tmp/test.db ./bd create \"Test issue\"\n")
		}

		// Load template if specified
		var tmpl *Template
		if fromTemplate != "" {
			var err error
			tmpl, err = loadTemplate(fromTemplate)
			if err != nil {
				FatalError("%v", err)
			}
		}

		// Get field values, preferring explicit flags over template defaults
		description, _ := getDescriptionFlag(cmd)
		if description == "" && tmpl != nil {
			description = tmpl.Description
		}

		// Warn if creating an issue without a description (unless it's a test issue)
		if description == "" && !strings.Contains(strings.ToLower(title), "test") {
			yellow := color.New(color.FgYellow).SprintFunc()
			fmt.Fprintf(os.Stderr, "%s Creating issue without description.\n", yellow("⚠"))
			fmt.Fprintf(os.Stderr, "  Issues without descriptions lack context for future work.\n")
			fmt.Fprintf(os.Stderr, "  Consider adding --description=\"Why this issue exists and what needs to be done\"\n")
		}

		design, _ := cmd.Flags().GetString("design")
		if design == "" && tmpl != nil {
			design = tmpl.Design
		}

		acceptance, _ := cmd.Flags().GetString("acceptance")
		if acceptance == "" && tmpl != nil {
			acceptance = tmpl.AcceptanceCriteria
		}
		
		// Parse priority (supports both "1" and "P1" formats)
		priorityStr, _ := cmd.Flags().GetString("priority")
		priority, err := validation.ValidatePriority(priorityStr)
		if err != nil {
			FatalError("%v", err)
		}
		if cmd.Flags().Changed("priority") == false && tmpl != nil {
			priority = tmpl.Priority
		}

		issueType, _ := cmd.Flags().GetString("type")
		if !cmd.Flags().Changed("type") && tmpl != nil && tmpl.Type != "" {
			// Flag not explicitly set and template has a type, use template
			issueType = tmpl.Type
		}

		assignee, _ := cmd.Flags().GetString("assignee")

		labels, _ := cmd.Flags().GetStringSlice("labels")
		labelAlias, _ := cmd.Flags().GetStringSlice("label")
		if len(labelAlias) > 0 {
			labels = append(labels, labelAlias...)
		}
		if len(labels) == 0 && tmpl != nil && len(tmpl.Labels) > 0 {
			labels = tmpl.Labels
		}

		explicitID, _ := cmd.Flags().GetString("id")
		parentID, _ := cmd.Flags().GetString("parent")
		externalRef, _ := cmd.Flags().GetString("external-ref")
		deps, _ := cmd.Flags().GetStringSlice("deps")
		forceCreate, _ := cmd.Flags().GetBool("force")
		repoOverride, _ := cmd.Flags().GetString("repo")

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
		
		// TODO: Switch to target repo for multi-repo support (bd-4ms)
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
				// TODO(bd-g5p7): Add RPC method to get config in daemon mode
				// For now, skip validation in daemon mode (needs RPC enhancement)
			} else {
				// Direct mode - check config
				dbPrefix, _ = store.GetConfig(ctx, "issue_prefix")
			}

			if err := validation.ValidatePrefix(requestedPrefix, dbPrefix, forceCreate); err != nil {
				FatalError("%v", err)
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
			}

			resp, err := daemonClient.Create(createArgs)
			if err != nil {
				FatalError("%v", err)
			}

			if jsonOutput {
				fmt.Println(string(resp.Data))
			} else {
				var issue types.Issue
				if err := json.Unmarshal(resp.Data, &issue); err != nil {
					FatalError("parsing response: %v", err)
				}
				green := color.New(color.FgGreen).SprintFunc()
				fmt.Printf("%s Created issue: %s\n", green("✓"), issue.ID)
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

		// Schedule auto-flush
		markDirtyAndScheduleFlush()

		if jsonOutput {
			outputJSON(issue)
		} else {
			green := color.New(color.FgGreen).SprintFunc()
			fmt.Printf("%s Created issue: %s\n", green("✓"), issue.ID)
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
	createCmd.Flags().String("from-template", "", "Create issue from template (e.g., 'epic', 'bug', 'feature')")
	createCmd.Flags().String("title", "", "Issue title (alternative to positional argument)")
	registerPriorityFlag(createCmd, "2")
	createCmd.Flags().StringP("type", "t", "task", "Issue type (bug|feature|task|epic|chore)")
	registerCommonIssueFlags(createCmd)
	createCmd.Flags().StringSliceP("labels", "l", []string{}, "Labels (comma-separated)")
	createCmd.Flags().StringSlice("label", []string{}, "Alias for --labels")
	_ = createCmd.Flags().MarkHidden("label")
	createCmd.Flags().String("id", "", "Explicit issue ID (e.g., 'bd-42' for partitioning)")
	createCmd.Flags().String("parent", "", "Parent issue ID for hierarchical child (e.g., 'bd-a3f8e9')")
	createCmd.Flags().StringSlice("deps", []string{}, "Dependencies in format 'type:id' or 'id' (e.g., 'discovered-from:bd-20,blocks:bd-15' or 'bd-20')")
	createCmd.Flags().Bool("force", false, "Force creation even if prefix doesn't match database prefix")
	createCmd.Flags().String("repo", "", "Target repository for issue (overrides auto-routing)")
	createCmd.Flags().IntP("estimate", "e", 0, "Time estimate in minutes (e.g., 60 for 1 hour)")
	// Note: --json flag is defined as a persistent flag in main.go, not here
	rootCmd.AddCommand(createCmd)
}
