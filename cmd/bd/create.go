package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/remotecache"
	"github.com/steveyegge/beads/internal/routing"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/timeparsing"
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
			// --dry-run not supported with --file (would need to parse and preview multiple issues)
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			if dryRun {
				FatalError("--dry-run is not supported with --file flag")
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
			// Guard: reject positional args that look like flags (bd-2c0).
			// When --help or other flags bypass Cobra's flag parsing (e.g.,
			// programmatic invocation), they end up here as positional args.
			if strings.HasPrefix(args[0], "-") {
				FatalError("title %q looks like a flag (starts with '-').\n  Run 'bd create --help' for available options.\n  To use this title anyway, pass it explicitly: bd create --title=%q", args[0], args[0])
			}
			title = args[0]
		} else if titleFlag != "" {
			title = titleFlag
		} else {
			FatalError("title required (or use --file to create from markdown)")
		}

		// Get silent flag
		silent, _ := cmd.Flags().GetBool("silent")

		// Warn if creating a test issue in production database (unless silent mode)
		if isTestIssue(title) && !silent && !debug.IsQuiet() {
			fmt.Fprintf(os.Stderr, "%s Creating test issue in production database\n", ui.RenderWarn("⚠"))
			fmt.Fprintf(os.Stderr, "  Title: %q appears to be test data\n", title)
			fmt.Fprintf(os.Stderr, "  Recommendation: Use isolated test database with --db\n")
			fmt.Fprintf(os.Stderr, "    bd --db /tmp/test-beads create %q\n", title)
		}

		// Get field values
		description, _ := getDescriptionFlag(cmd)

		skills, _ := cmd.Flags().GetString("skills")
		if skills != "" {
			if description != "" {
				description += "\n\n"
			}
			description += "## Required Skills\n" + skills
		}

		ctxStr, _ := cmd.Flags().GetString("context")
		if ctxStr != "" {
			if description != "" {
				description += "\n\n"
			}
			description += "## Context\n" + ctxStr
		}

		// Check if description is required by config
		if description == "" && !isTestIssue(title) {
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

		design, _ := getDesignFlag(cmd)
		acceptance, _ := cmd.Flags().GetString("acceptance")
		notes, _ := cmd.Flags().GetString("notes")
		specID, _ := cmd.Flags().GetString("spec-id")

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
		noHistory, _ := cmd.Flags().GetBool("no-history")
		if wisp && noHistory {
			FatalError("--ephemeral and --no-history are mutually exclusive")
		}
		molTypeStr, _ := cmd.Flags().GetString("mol-type")
		var molType types.MolType
		if molTypeStr != "" {
			molType = types.MolType(molTypeStr)
			if !molType.IsValid() {
				FatalError("invalid mol-type %q (must be swarm, patrol, or work)", molTypeStr)
			}
		}

		// Parse wisp type (TTL classification for ephemeral wisps)
		wispTypeStr, _ := cmd.Flags().GetString("wisp-type")
		var wispType types.WispType
		if wispTypeStr != "" {
			wispType = types.WispType(wispTypeStr)
			if !wispType.IsValid() {
				FatalError("invalid wisp-type %q (must be heartbeat, ping, patrol, gc_report, recovery, error, or escalation)", wispTypeStr)
			}
		}

		// Event-specific flags
		eventCategory, _ := cmd.Flags().GetString("event-category")
		eventActor, _ := cmd.Flags().GetString("event-actor")
		eventTarget, _ := cmd.Flags().GetString("event-target")
		eventPayload, _ := cmd.Flags().GetString("event-payload")

		// Validate event-specific flags require --type=event
		if (eventCategory != "" || eventActor != "" || eventTarget != "" || eventPayload != "") && issueType != "event" {
			FatalError("--event-category, --event-actor, --event-target, and --event-payload flags require --type=event")
		}

		// Parse --due flag (GH#820)
		// Uses layered parsing: compact duration → NLP → date-only → RFC3339
		var dueAt *time.Time
		dueStr, _ := cmd.Flags().GetString("due")
		if dueStr != "" {
			t, err := timeparsing.ParseRelativeTime(dueStr, time.Now())
			if err != nil {
				FatalError("invalid --due format %q. Examples: +6h, tomorrow, next monday, 2025-01-15", dueStr)
			}
			dueAt = &t
		}

		// Parse --defer flag (GH#820)
		var deferUntil *time.Time
		deferStr, _ := cmd.Flags().GetString("defer")
		if deferStr != "" {
			t, err := timeparsing.ParseRelativeTime(deferStr, time.Now())
			if err != nil {
				FatalError("invalid --defer format %q. Examples: +1h, tomorrow, next monday, 2025-01-15", deferStr)
			}
			// Warn if defer date is in the past (user probably meant future)
			if t.Before(time.Now()) && !silent && !debug.IsQuiet() {
				fmt.Fprintf(os.Stderr, "%s Defer date %q is in the past. Issue will appear in bd ready immediately.\n",
					ui.RenderWarn("!"), t.Format("2006-01-02 15:04"))
				fmt.Fprintf(os.Stderr, "  Did you mean a future date? Use --defer=+1h or --defer=tomorrow\n")
			}
			deferUntil = &t
		}

		// Parse --metadata flag (GH#1406)
		var metadata json.RawMessage
		if cmd.Flags().Changed("metadata") {
			metadataValue, _ := cmd.Flags().GetString("metadata")
			var metadataJSON string
			if strings.HasPrefix(metadataValue, "@") {
				filePath := metadataValue[1:]
				// #nosec G304 -- user explicitly provides file path via @file.json syntax
				data, err := os.ReadFile(filePath)
				if err != nil {
					FatalError("failed to read metadata file %s: %v", filePath, err)
				}
				metadataJSON = string(data)
			} else {
				metadataJSON = metadataValue
			}
			if !json.Valid([]byte(metadataJSON)) {
				FatalError("invalid JSON in --metadata: must be valid JSON")
			}
			metadata = json.RawMessage(metadataJSON)
		}

		// Validate template based on --validate flag or config
		// Uses LintIssue for field-aware validation: checks --acceptance field too (GH#2468 parity)
		validateTemplate, _ := cmd.Flags().GetBool("validate")
		validationMode := config.GetString("validation.on-create")
		if validateTemplate || validationMode == "error" || validationMode == "warn" {
			lintIssue := &types.Issue{
				IssueType:          types.IssueType(issueType).Normalize(),
				Description:        description,
				AcceptanceCriteria: acceptance,
			}
			if err := validation.LintIssue(lintIssue); err != nil {
				if validateTemplate || validationMode == "error" {
					FatalError("%v", err)
				}
				// warn mode: print warning but proceed
				fmt.Fprintf(os.Stderr, "%s %v\n", ui.RenderWarn("⚠"), err)
			}
		}

		// Handle --dry-run flag (before --rig to ensure it works with cross-rig creation)
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		if dryRun {
			// Build preview issue
			var externalRefPtr *string
			if externalRef != "" {
				externalRefPtr = &externalRef
			}
			previewIssue := &types.Issue{
				Title:              title,
				Description:        description,
				Design:             design,
				AcceptanceCriteria: acceptance,
				Notes:              notes,
				SpecID:             specID,
				Status:             types.StatusOpen,
				Priority:           priority,
				IssueType:          types.IssueType(issueType).Normalize(),
				Assignee:           assignee,
				ExternalRef:        externalRefPtr,
				Ephemeral:          wisp,
				NoHistory:          noHistory,
				CreatedBy:          getActorWithGit(),
				Owner:              getOwner(),
				MolType:            molType,
				WispType:           wispType,
				DueAt:              dueAt,
				DeferUntil:         deferUntil,
				Metadata:           metadata,
				// Event fields
				EventKind: eventCategory,
				Actor:     eventActor,
				Target:    eventTarget,
				Payload:   eventPayload,
			}
			if explicitID != "" {
				previewIssue.ID = explicitID
			}

			if jsonOutput {
				outputJSON(previewIssue)
			} else {
				idDisplay := previewIssue.ID
				if idDisplay == "" {
					idDisplay = "(will be generated)"
				}
				fmt.Printf("%s [DRY RUN] Would create issue:\n", ui.RenderWarn("⚠"))
				fmt.Printf("  ID: %s\n", idDisplay)
				fmt.Printf("  Title: %s\n", previewIssue.Title)
				fmt.Printf("  Type: %s\n", previewIssue.IssueType)
				fmt.Printf("  Priority: P%d\n", previewIssue.Priority)
				fmt.Printf("  Status: %s\n", previewIssue.Status)
				if previewIssue.Assignee != "" {
					fmt.Printf("  Assignee: %s\n", previewIssue.Assignee)
				}
				if previewIssue.Description != "" {
					fmt.Printf("  Description: %s\n", previewIssue.Description)
				}
				if len(labels) > 0 {
					fmt.Printf("  Labels: %s\n", strings.Join(labels, ", "))
				}
				if len(deps) > 0 {
					fmt.Printf("  Dependencies: %s\n", strings.Join(deps, ", "))
				}
				if rigOverride != "" || prefixOverride != "" {
					rig := rigOverride
					if rig == "" {
						rig = prefixOverride
					}
					fmt.Printf("  Target rig: %s\n", rig)
				}
				if eventCategory != "" {
					fmt.Printf("  Event category: %s\n", eventCategory)
				}
			}
			return
		}

		// Auto-route based on explicit ID prefix (if no explicit --rig/--prefix provided)
		// When creating an issue with --id=pq-xxx, automatically route to the database
		// that handles the pq- prefix based on routes.jsonl
		if explicitID != "" && rigOverride == "" && prefixOverride == "" {
			prefix := routing.ExtractPrefix(explicitID)
			if prefix != "" {
				// Load routes from town level
				townBeadsDir, err := findTownBeadsDir()
				if err == nil {
					routes, err := routing.LoadTownRoutes(townBeadsDir)
					if err == nil && len(routes) > 0 {
						// Check if this prefix matches a route to a different rig
						for _, route := range routes {
							if route.Prefix == prefix && route.Path != "" && route.Path != "." {
								// Found a matching route - auto-route to that rig
								rigName := routing.ExtractProjectFromPath(route.Path)
								if rigName != "" {
									createInRig(cmd, rigName, explicitID, title, description, issueType, priority, design, acceptance, notes, assignee, labels, externalRef, specID, wisp, noHistory)
									return
								}
							}
						}
					}
				}
			}
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
			createInRig(cmd, targetRig, explicitID, title, description, issueType, priority, design, acceptance, notes, assignee, labels, externalRef, specID, wisp, noHistory)
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

			// Build routing config with backward compatibility for legacy contributor.* keys.
			// Prefer config.yaml values, but fall back to DB config values set by bd init --contributor.
			routingMode := getRoutingConfigValue(rootCtx, store, "routing.mode")
			contributorRepo := getRoutingConfigValue(rootCtx, store, "routing.contributor")

			// NFR-001: Backward compatibility - fall back to legacy contributor.* keys
			if routingMode == "" {
				if getRoutingConfigValue(rootCtx, store, "contributor.auto_route") == "true" {
					routingMode = "auto"
				}
			}
			if contributorRepo == "" {
				contributorRepo = getRoutingConfigValue(rootCtx, store, "contributor.planning_repo")
			}

			routingConfig := &routing.RoutingConfig{
				Mode:             routingMode,
				DefaultRepo:      getRoutingConfigValue(rootCtx, store, "routing.default"),
				MaintainerRepo:   getRoutingConfigValue(rootCtx, store, "routing.maintainer"),
				ContributorRepo:  contributorRepo,
				ExplicitOverride: repoOverride,
			}

			repoPath = routing.DetermineTargetRepo(routingConfig, userRole, ".")
		}

		// Switch to target repo for multi-repo support (bd-6x6g)
		// When routing to a different repo, we use direct storage access
		var targetStore storage.DoltStorage
		if repoPath != "." {
			if remotecache.IsRemoteURL(repoPath) {
				// Remote URL: pull into cache, open store, push after create
				cache, err := remotecache.DefaultCache()
				if err != nil {
					FatalError("failed to initialize remote cache: %v", err)
				}
				if _, err := cache.Ensure(rootCtx, repoPath); err != nil {
					FatalError("failed to sync remote %s: %v", repoPath, err)
				}
				targetStore, err = cache.OpenStore(rootCtx, repoPath, newDoltStoreFromConfig)
				if err != nil {
					FatalError("failed to open remote store: %v", err)
				}
				// Push changes back to remote after issue creation
				defer func() {
					if pushErr := cache.Push(rootCtx, repoPath); pushErr != nil {
						fmt.Fprintf(os.Stderr, "Warning: failed to push to %s: %v\n", repoPath, pushErr)
					}
				}()
			} else {
				targetBeadsDir := routing.ExpandPath(repoPath)
				debug.Logf("DEBUG: Routing to target repo: %s\n", targetBeadsDir)

				// Ensure target beads directory exists with prefix inheritance
				if err := ensureBeadsDirForPath(rootCtx, targetBeadsDir, store); err != nil {
					FatalError("failed to initialize target repo: %v", err)
				}

				// Open new store for target repo using factory to respect backend config
				targetBeadsDirPath := filepath.Join(targetBeadsDir, ".beads")
				var err error
				targetStore, err = newDoltStoreFromConfig(rootCtx, targetBeadsDirPath)
				if err != nil {
					FatalError("failed to open target store: %v", err)
				}
			}

			// Close the original store before replacing it (it won't be used anymore)
			// Note: We don't defer-close targetStore here because PersistentPostRun
			// will close whatever store is assigned to the global `store` variable.
			// This fixes the "database is closed" error during auto-flush (GH#routing-close-bug).
			if store != nil {
				_ = store.Close() // Best effort cleanup on error path
			}

			// Replace store for remainder of create operation
			store = targetStore
		}

		// Check for conflicting flags
		if explicitID != "" && parentID != "" {
			FatalError("cannot specify both --id and --parent flags")
		}

		// If parent is specified, generate child ID and optionally inherit labels
		var inheritedLabels []string
		if parentID != "" {
			ctx := rootCtx
			// Validate parent exists before generating child ID
			_, err := store.GetIssue(ctx, parentID)
			if err != nil {
				if errors.Is(err, storage.ErrNotFound) {
					FatalError("parent issue %s not found", parentID)
				}
				FatalError("failed to check parent issue: %v", err)
			}
			childID, err := store.GetNextChildID(ctx, parentID)
			if err != nil {
				FatalError("%v", err)
			}
			explicitID = childID // Set as explicit ID for the rest of the flow

			// Inherit parent labels unless --no-inherit-labels is set (GH#2100)
			noInheritLabels, _ := cmd.Flags().GetBool("no-inherit-labels")
			if !noInheritLabels {
				inheritedLabels, _ = store.GetLabels(ctx, parentID)
			}
		}

		// Validate explicit ID format if provided
		if explicitID != "" {
			// Basic format validation for all issue types.
			// Note: Orchestrator-specific agent ID validation (mayor, polecat, witness, etc.)
			// is handled by the orchestrator, not beads core.
			_, err := validation.ValidateIDFormat(explicitID)
			if err != nil {
				FatalError("%v", err)
			}

			// Validate prefix matches database prefix
			ctx := rootCtx

			// Get database prefix and allowed prefixes from config.
			// YAML config takes precedence over DB — in shared-server mode the DB
			// may belong to a different project (GH#2469).
			var dbPrefix, allowedPrefixes string
			if yamlPrefix := config.GetString("issue-prefix"); yamlPrefix != "" {
				dbPrefix = yamlPrefix
			} else {
				dbPrefix, _ = store.GetConfig(ctx, "issue_prefix") // Best effort: empty prefix is a valid fallback
			}
			allowedPrefixes, _ = store.GetConfig(ctx, "allowed_prefixes") // Best effort: empty means no prefix restriction

			// Use ValidateIDPrefixAllowed which handles multi-hyphen prefixes correctly (GH#1135)
			// This checks if the ID starts with an allowed prefix, rather than extracting
			// the prefix first (which can fail for IDs like "hq-cv-test" where "test" looks like a word)
			if err := validation.ValidateIDPrefixAllowed(explicitID, dbPrefix, allowedPrefixes, forceCreate); err != nil {
				FatalError("%v", err)
			}
		}

		var externalRefPtr *string
		if externalRef != "" {
			externalRefPtr = &externalRef
		}

		// Direct mode
		issue := &types.Issue{
			ID:                 explicitID, // Set explicit ID if provided (empty string if not)
			Title:              title,
			Description:        description,
			Design:             design,
			AcceptanceCriteria: acceptance,
			Notes:              notes,
			SpecID:             specID,
			Status:             types.StatusOpen,
			Priority:           priority,
			IssueType:          types.IssueType(issueType).Normalize(),
			Assignee:           assignee,
			ExternalRef:        externalRefPtr,
			EstimatedMinutes:   estimatedMinutes,
			Ephemeral:          wisp,
			NoHistory:          noHistory,
			CreatedBy:          getActorWithGit(),
			Owner:              getOwner(),
			MolType:            molType,
			WispType:           wispType,
			EventKind:          eventCategory,
			Actor:              eventActor,
			Target:             eventTarget,
			Payload:            eventPayload,
			DueAt:              dueAt,
			DeferUntil:         deferUntil,
			Metadata:           metadata,
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

		// Track whether any post-create writes occurred. CreateIssue commits
		// the issue to Dolt internally, but subsequent AddDependency/AddLabel
		// calls only write to the working set. A follow-up Dolt commit is
		// needed to persist them (GH#2009).
		postCreateWrites := false

		// If parent was specified, add parent-child dependency
		if parentID != "" {
			dep := &types.Dependency{
				IssueID:     issue.ID,
				DependsOnID: parentID,
				Type:        types.DepParentChild,
			}
			if err := store.AddDependency(ctx, dep, actor); err != nil {
				WarnError("failed to add parent-child dependency %s -> %s: %v", issue.ID, parentID, err)
			} else {
				postCreateWrites = true
			}
		}

		// Merge inherited parent labels with user-specified labels (GH#2100)
		if len(inheritedLabels) > 0 {
			seen := make(map[string]bool)
			for _, l := range labels {
				seen[l] = true
			}
			for _, l := range inheritedLabels {
				if !seen[l] {
					labels = append(labels, l)
				}
			}
		}

		// Add labels if specified
		for _, label := range labels {
			if err := store.AddLabel(ctx, issue.ID, label, actor); err != nil {
				WarnError("failed to add label %s: %v", label, err)
			} else {
				postCreateWrites = true
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
				// "depends-on" is an alias — keep default direction (new issue depends on target)
				if depType == "depends-on" {
					depType = types.DepBlocks
				}
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
			// When user explicitly says "blocks:X", they mean "new issue blocks X"
			// So X depends on the new issue — swap direction
			if depType == types.DepBlocks && strings.Contains(depSpec, ":") {
				dep.IssueID = dependsOnID
				dep.DependsOnID = issue.ID
			}
			if err := store.AddDependency(ctx, dep, actor); err != nil {
				WarnError("failed to add dependency %s -> %s: %v", issue.ID, dependsOnID, err)
			} else {
				postCreateWrites = true
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
			} else {
				postCreateWrites = true
			}
		}

		// Commit to Dolt. In DoltStore mode, CreateIssue commits the issue
		// row internally, so only post-create metadata (deps, labels) needs
		// a separate commit. In EmbeddedDoltStore mode, CreateIssue writes
		// to the working set without a Dolt commit, so we always commit
		// everything together at the end.
		if isEmbeddedDolt || postCreateWrites {
			commitMsg := fmt.Sprintf("bd: create %s", issue.ID)
			if err := store.Commit(ctx, commitMsg); err != nil && !isDoltNothingToCommit(err) {
				WarnError("failed to commit: %v", err)
			}
		}

		// If issue was routed to a different repo, commit pending changes.
		// Push is NOT done here — periodic sync handles pushes to
		// DoltHub remotes. Per-create pushes caused 22GB of git-remote-cache
		// bloat with dozens of agents creating wisps constantly (hq-glw).
		if repoPath != "." && targetStore != nil {
			if _, err := targetStore.CommitPending(ctx, actor); err != nil {
				debug.Logf("warning: failed to commit routed repo: %v", err)
			}
		}

		// Run create hook
		if hookRunner != nil {
			hookRunner.Run(hooks.EventCreate, issue)
		}

		if jsonOutput {
			outputJSON(issue)
		} else if silent {
			fmt.Println(issue.ID)
		} else {
			fmt.Printf("%s Created issue: %s\n", ui.RenderPass("✓"), formatFeedbackID(issue.ID, issue.Title))
			fmt.Printf("  Priority: P%d\n", issue.Priority)
			fmt.Printf("  Status: %s\n", issue.Status)

			// Show tip after successful create (direct mode only)
			maybeShowTip(store)
		}

		// Track as last touched issue
		SetLastTouchedID(issue.ID)
	},
}

func init() {
	createCmd.Flags().StringP("file", "f", "", "Create multiple issues from markdown file")
	createCmd.Flags().String("title", "", "Issue title (alternative to positional argument)")
	createCmd.Flags().Bool("silent", false, "Output only the issue ID (for scripting)")
	createCmd.Flags().Bool("dry-run", false, "Preview what would be created without actually creating")
	registerPriorityFlag(createCmd, "2")
	createCmd.Flags().StringP("type", "t", "task", "Issue type (bug|feature|task|epic|chore|decision); custom types require types.custom config; aliases: enhancement/feat→feature, dec/adr→decision")
	registerCommonIssueFlags(createCmd)
	createCmd.Flags().String("spec-id", "", "Link to specification document")
	createCmd.Flags().StringSliceP("labels", "l", []string{}, "Labels (comma-separated)")
	createCmd.Flags().String("skills", "", "Required skills for this issue")
	createCmd.Flags().String("context", "", "Additional context for the issue")
	createCmd.Flags().StringSlice("label", []string{}, "Alias for --labels")
	_ = createCmd.Flags().MarkHidden("label") // Only fails if flag missing (caught in tests)
	createCmd.Flags().String("id", "", "Explicit issue ID (e.g., 'bd-42' for partitioning)")
	createCmd.Flags().String("parent", "", "Parent issue ID for hierarchical child (e.g., 'bd-a3f8e9')")
	createCmd.Flags().Bool("no-inherit-labels", false, "Don't inherit labels from parent issue")
	createCmd.Flags().StringSlice("deps", []string{}, "Dependencies in format 'type:id' or 'id' (e.g., 'discovered-from:bd-20,blocks:bd-15' or 'bd-20')")
	createCmd.Flags().String("waits-for", "", "Spawner issue ID to wait for (creates waits-for dependency for fanout gate)")
	createCmd.Flags().String("waits-for-gate", "all-children", "Gate type: all-children (wait for all) or any-children (wait for first)")
	createCmd.Flags().Bool("force", false, "Force creation even if prefix doesn't match database prefix")
	createCmd.Flags().String("repo", "", "Target repository for issue (overrides auto-routing)")
	createCmd.Flags().String("rig", "", "Create issue in a different rig (e.g., --rig beads)")
	createCmd.Flags().String("prefix", "", "Create issue in rig by prefix (e.g., --prefix bd- or --prefix bd or --prefix beads)")
	createCmd.Flags().IntP("estimate", "e", 0, "Time estimate in minutes (e.g., 60 for 1 hour)")
	createCmd.Flags().Bool("ephemeral", false, "Create as ephemeral (short-lived, subject to TTL compaction)")
	createCmd.Flags().Bool("no-history", false, "Skip Dolt commit history without making GC-eligible (for permanent agent beads)")
	createCmd.Flags().String("mol-type", "", "Molecule type: swarm (multi-agent), patrol (recurring ops), work (default)")
	createCmd.Flags().String("wisp-type", "", "Wisp type for TTL-based compaction: heartbeat, ping, patrol, gc_report, recovery, error, escalation")
	createCmd.Flags().Bool("validate", false, "Validate description contains required sections for issue type")
	// Event-specific flags (only valid when --type=event)
	createCmd.Flags().String("event-category", "", "Event category (e.g., patrol.muted, agent.started) (requires --type=event)")
	createCmd.Flags().String("event-actor", "", "Entity URI who caused this event (requires --type=event)")
	createCmd.Flags().String("event-target", "", "Entity URI or bead ID affected (requires --type=event)")
	createCmd.Flags().String("event-payload", "", "Event-specific JSON data (requires --type=event)")
	// Time-based scheduling flags (GH#820)
	// Examples:
	//   --due=+6h           Due in 6 hours
	//   --due=tomorrow      Due tomorrow
	//   --due="next monday" Due next Monday
	//   --due=2025-01-15    Due on specific date
	//   --defer=+1h         Hidden from bd ready for 1 hour
	//   --defer=tomorrow    Hidden until tomorrow
	createCmd.Flags().String("due", "", "Due date/time. Formats: +6h, +1d, +2w, tomorrow, next monday, 2025-01-15")
	createCmd.Flags().String("defer", "", "Defer until date (issue hidden from bd ready until then). Same formats as --due")
	createCmd.Flags().String("metadata", "", "Set custom metadata (JSON string or @file.json to read from file)")
	// Note: --json flag is defined as a persistent flag in main.go, not here
	rootCmd.AddCommand(createCmd)
}

// createInRig creates an issue in a different rig using --rig flag or auto-routing.
// This directly creates in the target rig's database.
func createInRig(cmd *cobra.Command, rigName, explicitID, title, description, issueType string, priority int, design, acceptance, notes, assignee string, labels []string, externalRef, specID string, wisp, noHistory bool) {
	ctx := rootCtx

	// Find the town-level beads directory (where routes.jsonl lives)
	townBeadsDir, err := findTownBeadsDir()
	if err != nil {
		FatalError("cannot use --rig: %v", err)
	}

	// Resolve the target rig's beads directory and prefix
	targetBeadsDir, targetPrefix, err := routing.ResolveBeadsDirForRig(rigName, townBeadsDir)
	if err != nil {
		FatalError("%v", err)
	}

	// Open storage for the target rig using factory to respect backend config
	targetStore, err := newDoltStoreFromConfig(ctx, targetBeadsDir)
	if err != nil {
		FatalError("failed to open rig %q database: %v", rigName, err)
	}
	defer func() {
		if err := targetStore.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to close rig database: %v\n", err)
		}
	}()

	// Prepare prefix override from routes.jsonl for cross-rig creation
	// Strip trailing hyphen - database stores prefix without it (e.g., "aops" not "aops-")
	var prefixOverride string
	if targetPrefix != "" {
		prefixOverride = strings.TrimSuffix(targetPrefix, "-")
	}

	var externalRefPtr *string
	if externalRef != "" {
		externalRefPtr = &externalRef
	}

	// Extract event-specific flags (bd-xwvo fix)
	eventCategory, _ := cmd.Flags().GetString("event-category")
	eventActor, _ := cmd.Flags().GetString("event-actor")
	eventTarget, _ := cmd.Flags().GetString("event-target")
	eventPayload, _ := cmd.Flags().GetString("event-payload")

	// Extract molecule/agent flags (bd-xwvo fix)
	molTypeStr, _ := cmd.Flags().GetString("mol-type")
	var molType types.MolType
	if molTypeStr != "" {
		molType = types.MolType(molTypeStr)
	}
	// Extract wisp type (TTL classification for ephemeral wisps)
	wispTypeStr, _ := cmd.Flags().GetString("wisp-type")
	var wispType types.WispType
	if wispTypeStr != "" {
		wispType = types.WispType(wispTypeStr)
	}

	// Extract time-based scheduling flags (bd-xwvo fix)
	var dueAt *time.Time
	dueStr, _ := cmd.Flags().GetString("due")
	if dueStr != "" {
		t, err := timeparsing.ParseRelativeTime(dueStr, time.Now())
		if err != nil {
			FatalError("invalid --due format %q", dueStr)
		}
		dueAt = &t
	}

	var deferUntil *time.Time
	deferStr, _ := cmd.Flags().GetString("defer")
	if deferStr != "" {
		t, err := timeparsing.ParseRelativeTime(deferStr, time.Now())
		if err != nil {
			FatalError("invalid --defer format %q", deferStr)
		}
		deferUntil = &t
	}

	// Parse --metadata for cross-rig creation
	var metadata json.RawMessage
	if cmd.Flags().Changed("metadata") {
		metadataValue, _ := cmd.Flags().GetString("metadata")
		var metadataJSON string
		if strings.HasPrefix(metadataValue, "@") {
			filePath := metadataValue[1:]
			// #nosec G304 -- user explicitly provides file path via @file.json syntax
			data, err := os.ReadFile(filePath)
			if err != nil {
				FatalError("failed to read metadata file %s: %v", filePath, err)
			}
			metadataJSON = string(data)
		} else {
			metadataJSON = metadataValue
		}
		if !json.Valid([]byte(metadataJSON)) {
			FatalError("invalid JSON in --metadata: must be valid JSON")
		}
		metadata = json.RawMessage(metadataJSON)
	}

	// Create issue with explicit ID if provided, otherwise CreateIssue will generate one
	issue := &types.Issue{
		ID:                 explicitID, // Set explicit ID if provided (empty string if not)
		Title:              title,
		Description:        description,
		Design:             design,
		AcceptanceCriteria: acceptance,
		Notes:              notes,
		SpecID:             specID,
		Status:             types.StatusOpen,
		Priority:           priority,
		IssueType:          types.IssueType(issueType).Normalize(),
		Assignee:           assignee,
		ExternalRef:        externalRefPtr,
		Ephemeral:          wisp,
		NoHistory:          noHistory,
		CreatedBy:          getActorWithGit(),
		Owner:              getOwner(),
		// Event fields (bd-xwvo fix)
		EventKind: eventCategory,
		Actor:     eventActor,
		Target:    eventTarget,
		Payload:   eventPayload,
		// Molecule/agent fields (bd-xwvo fix)
		MolType:  molType,
		WispType: wispType,
		// Time scheduling fields (bd-xwvo fix)
		DueAt:      dueAt,
		DeferUntil: deferUntil,
		Metadata:   metadata,
		// Cross-rig routing: use route prefix instead of database config
		PrefixOverride: prefixOverride,
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
		fmt.Printf("%s Created issue in rig %q: %s\n", ui.RenderPass("✓"), rigName, formatFeedbackID(issue.ID, issue.Title))
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

// formatTimeForRPC converts a *time.Time to RFC3339 string for RPC calls.
// Returns empty string if t is nil, to distinguish "not set" from "set to zero".
func formatTimeForRPC(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339)
}

// ensureBeadsDirForPath ensures a beads directory exists at the target path.
// If the .beads directory doesn't exist, it creates it and initializes with
// the same prefix as the source store (T010, T012: prefix inheritance).
func ensureBeadsDirForPath(ctx context.Context, targetPath string, sourceStore storage.DoltStorage) error {
	beadsDir := filepath.Join(targetPath, ".beads")
	metadataPath := filepath.Join(beadsDir, "metadata.json")

	// Check if beads directory already exists with a Dolt database.
	// metadata.json is the canonical marker for an initialized beads dir.
	if _, err := os.Stat(metadataPath); err == nil {
		return nil
	}

	// Create .beads directory
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		return fmt.Errorf("cannot create .beads directory: %w", err)
	}

	// Initialize database via NewFromConfigWithOptions to respect Dolt config.
	// Set the prefix if source store has one (T012: prefix inheritance).
	if sourceStore != nil {
		sourcePrefix, err := sourceStore.GetConfig(ctx, "issue_prefix")
		if err == nil && sourcePrefix != "" {
			// Open target store temporarily to set prefix.
			// Use newDoltStore with explicit config since the target .beads
			// directory was just created and has no metadata.json yet.
			tempStore, err := newDoltStore(ctx, &dolt.Config{
				BeadsDir:        beadsDir,
				Database:        sourcePrefix,
				CreateIfMissing: true,
			})
			if err != nil {
				return fmt.Errorf("failed to initialize target database: %w", err)
			}
			if err := tempStore.SetConfig(ctx, "issue_prefix", sourcePrefix); err != nil {
				_ = tempStore.Close() // Best effort cleanup on error path
				return fmt.Errorf("failed to set prefix in target store: %w", err)
			}
			if err := tempStore.Close(); err != nil {
				return fmt.Errorf("failed to close target store: %w", err)
			}
		}
	}

	return nil
}
