package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/metrics"
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
	Use:           "create [title]",
	GroupID:       "issues",
	Aliases:       []string{"new"},
	Short:         "Create a new issue (or batch from markdown/graph JSON)",
	Args:          cobra.MinimumNArgs(0),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("create")

		evt := metrics.NewCommandEvent("create")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		if usesProxiedServer() {
			in, err := gatherCreateInput(cmd, args)
			if err != nil {
				return err
			}
			runCreateProxiedServer(cmd, rootCtx, in)
			return nil
		}
		file, _ := cmd.Flags().GetString("file")
		graphFile, _ := cmd.Flags().GetString("graph")

		if file != "" {
			if graphFile != "" {
				return HandleError("cannot specify both --file and --graph")
			}
			if len(args) > 0 {
				return HandleError("cannot specify both title and --file flag")
			}
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			if dryRun {
				return HandleError("--dry-run is not supported with --file flag")
			}
			return createIssuesFromMarkdown(cmd, file)
		}

		if graphFile != "" {
			if len(args) > 0 {
				return HandleError("cannot specify both title and --graph flag")
			}
			graphDryRun, _ := cmd.Flags().GetBool("dry-run")
			wisp, _ := cmd.Flags().GetBool("ephemeral")
			noHistory, _ := cmd.Flags().GetBool("no-history")
			graphOpts := GraphApplyOptions{
				Ephemeral: wisp,
				NoHistory: noHistory,
			}
			if err := graphOpts.Validate(); err != nil {
				return HandleError("invalid graph options: %v", err)
			}
			return createIssuesFromGraph(graphFile, graphDryRun, graphOpts)
		}

		titleFlag, _ := cmd.Flags().GetString("title")
		var title string

		if len(args) > 0 && titleFlag != "" {
			if args[0] != titleFlag {
				return HandleError("cannot specify different titles as both positional argument and --title flag\n  Positional: %q\n  --title:    %q", args[0], titleFlag)
			}
			title = args[0]
		} else if len(args) > 0 {
			if strings.HasPrefix(args[0], "-") {
				return HandleError("title %q looks like a flag (starts with '-').\n  Run 'bd create --help' for available options.\n  To use this title anyway, pass it explicitly: bd create --title=%q", args[0], args[0])
			}
			title = args[0]
		} else if titleFlag != "" {
			title = titleFlag
		} else {
			return HandleError("title required (or use --file to create from markdown)")
		}

		// Get silent flag
		silent, _ := cmd.Flags().GetBool("silent")

		// Warn if creating a test issue in a database with existing issues.
		// A brand-new repo with zero issues is not a "production database" (#2898).
		if isTestIssue(title) && !silent && !debug.IsQuiet() {
			if stats, err := store.GetStatistics(context.Background()); err == nil && stats != nil && stats.TotalIssues >= 5 {
				fmt.Fprintf(os.Stderr, "%s Creating test issue in production database\n", ui.RenderWarn("⚠"))
				fmt.Fprintf(os.Stderr, "  Title: %q appears to be test data\n", title)
				fmt.Fprintf(os.Stderr, "  Recommendation: Use isolated test database with --db\n")
				fmt.Fprintf(os.Stderr, "    bd --db /tmp/test-beads create %q\n", title)
			}
		}

		description, _, err := getDescriptionFlag(cmd)
		if err != nil {
			return err
		}

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

		if description == "" && !isTestIssue(title) {
			if config.GetBool("create.require-description") {
				return HandleError("description is required (set create.require-description: false in config.yaml to disable)")
			}
		}

		design, _, err := getDesignFlag(cmd)
		if err != nil {
			return err
		}
		acceptance, _ := cmd.Flags().GetString("acceptance")
		notes, _ := cmd.Flags().GetString("notes")
		specID, _ := cmd.Flags().GetString("spec-id")

		priorityStr, _ := cmd.Flags().GetString("priority")
		priority, err := validation.ValidatePriority(priorityStr)
		if err != nil {
			return HandleError("%v", err)
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
		wisp, _ := cmd.Flags().GetBool("ephemeral")
		noHistory, _ := cmd.Flags().GetBool("no-history")
		if wisp && noHistory {
			return HandleError("--ephemeral and --no-history are mutually exclusive")
		}
		molTypeStr, _ := cmd.Flags().GetString("mol-type")
		var molType types.MolType
		if molTypeStr != "" {
			molType = types.MolType(molTypeStr)
			if !molType.IsValid() {
				return HandleError("invalid mol-type %q (must be swarm, patrol, or work)", molTypeStr)
			}
		}

		wispTypeStr, _ := cmd.Flags().GetString("wisp-type")
		var wispType types.WispType
		if wispTypeStr != "" {
			wispType = types.WispType(wispTypeStr)
			if !wispType.IsValid() {
				return HandleError("invalid wisp-type %q (must be heartbeat, ping, patrol, gc_report, recovery, error, or escalation)", wispTypeStr)
			}
		}

		eventCategory, _ := cmd.Flags().GetString("event-category")
		eventActor, _ := cmd.Flags().GetString("event-actor")
		eventTarget, _ := cmd.Flags().GetString("event-target")
		eventPayload, _ := cmd.Flags().GetString("event-payload")

		if (eventCategory != "" || eventActor != "" || eventTarget != "" || eventPayload != "") && issueType != "event" {
			return HandleError("--event-category, --event-actor, --event-target, and --event-payload flags require --type=event")
		}

		var dueAt *time.Time
		dueStr, _ := cmd.Flags().GetString("due")
		if dueStr != "" {
			t, err := timeparsing.ParseRelativeTime(dueStr, time.Now())
			if err != nil {
				return HandleError("invalid --due format %q. Examples: +6h, tomorrow, next monday, 2025-01-15", dueStr)
			}
			dueAt = &t
		}

		var deferUntil *time.Time
		deferStr, _ := cmd.Flags().GetString("defer")
		if deferStr != "" {
			t, err := timeparsing.ParseRelativeTime(deferStr, time.Now())
			if err != nil {
				return HandleError("invalid --defer format %q. Examples: +1h, tomorrow, next monday, 2025-01-15", deferStr)
			}
			// Warn if defer date is in the past (user probably meant future)
			if t.Before(time.Now()) && !silent && !debug.IsQuiet() {
				fmt.Fprintf(os.Stderr, "%s Defer date %q is in the past. Issue will appear in bd ready immediately.\n",
					ui.RenderWarn("!"), t.Format("2006-01-02 15:04"))
				fmt.Fprintf(os.Stderr, "  Did you mean a future date? Use --defer=+1h or --defer=tomorrow\n")
			}
			deferUntil = &t
		}

		var metadata json.RawMessage
		if cmd.Flags().Changed("metadata") {
			metadataValue, _ := cmd.Flags().GetString("metadata")
			var metadataJSON string
			if strings.HasPrefix(metadataValue, "@") {
				filePath := metadataValue[1:]
				// #nosec G304 -- user explicitly provides file path via @file.json syntax
				data, err := os.ReadFile(filePath)
				if err != nil {
					return HandleError("failed to read metadata file %s: %v", filePath, err)
				}
				metadataJSON = string(data)
			} else {
				metadataJSON = metadataValue
			}
			if !json.Valid([]byte(metadataJSON)) {
				return HandleError("invalid JSON in --metadata: must be valid JSON")
			}
			metadata = json.RawMessage(metadataJSON)
		}

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
					return HandleError("%v", err)
				}
				fmt.Fprintf(os.Stderr, "%s %v\n", ui.RenderWarn("⚠"), err)
			}
		}

		dryRun, _ := cmd.Flags().GetBool("dry-run")

		var estimatedMinutes *int
		if cmd.Flags().Changed("estimate") {
			est, _ := cmd.Flags().GetInt("estimate")
			if est < 0 {
				return HandleError("estimate must be a non-negative number of minutes")
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

		renderDryRun := func() error {
			previewIssue := buildCreateIssue(createIssueParams{
				ID:                 explicitID,
				Title:              title,
				Description:        description,
				Design:             design,
				AcceptanceCriteria: acceptance,
				Notes:              notes,
				SpecID:             specID,
				Priority:           priority,
				IssueType:          types.IssueType(issueType).Normalize(),
				Assignee:           assignee,
				ExternalRef:        externalRef,
				EstimatedMinutes:   estimatedMinutes,
				Ephemeral:          wisp,
				NoHistory:          noHistory,
				CreatedBy:          getActorWithGit(),
				Owner:              getOwner(),
				Labels:             labels,
				MolType:            molType,
				WispType:           wispType,
				DueAt:              dueAt,
				DeferUntil:         deferUntil,
				Metadata:           metadata,
				EventKind:          eventCategory,
				Actor:              eventActor,
				Target:             eventTarget,
				Payload:            eventPayload,
			})

			if jsonOutput {
				return outputJSON(previewIssue)
			}
			renderCreateDryRunPreview(previewIssue, labels, deps)
			return nil
		}

		if dryRun && parentID == "" {
			return renderDryRun()
		}

		var targetStore storage.DoltStorage
		var remoteCache *remotecache.Cache
		if !dryRun && repoPath != "." {
			if remotecache.IsRemoteURL(repoPath) {
				var err error
				remoteCache, err = remotecache.DefaultCache()
				if err != nil {
					return HandleError("failed to initialize remote cache: %v", err)
				}
				if _, err := remoteCache.Ensure(rootCtx, repoPath); err != nil {
					return HandleError("failed to sync remote %s: %v", repoPath, err)
				}
				targetStore, err = remoteCache.OpenStore(rootCtx, repoPath, newDoltStoreFromConfig)
				if err != nil {
					return HandleError("failed to open remote store: %v", err)
				}
			} else {
				targetBeadsDir := routing.ExpandPath(repoPath)
				debug.Logf("DEBUG: Routing to target repo: %s\n", targetBeadsDir)

				if err := ensureBeadsDirForPath(rootCtx, targetBeadsDir, store); err != nil {
					return HandleError("failed to initialize target repo: %v", err)
				}

				targetBeadsDirPath := filepath.Join(targetBeadsDir, ".beads")
				var err error
				targetStore, err = newDoltStoreFromConfig(rootCtx, targetBeadsDirPath)
				if err != nil {
					return HandleError("failed to open target store: %v", err)
				}
			}

			// Close the original store before replacing it (it won't be used anymore)
			// Note: We don't defer-close targetStore here because PersistentPostRun
			// will close whatever store is assigned to the global `store` variable.
			// This fixes the "database is closed" error during auto-flush (GH#routing-close-bug).
			if store != nil {
				_ = store.Close() // Best effort cleanup on error path
			}

			// Replace store for remainder of create operation.
			// Must use setStore to sync cmdCtx.Store — a bare `store = targetStore`
			// leaves cmdCtx.Store pointing at the closed original, which causes
			// "store is closed" in PostRun tip auto-commit (GH#tip-closed-bug).
			setStore(targetStore)
		}

		if explicitID != "" && parentID != "" {
			return HandleError("cannot specify both --id and --parent flags")
		}

		parentLookupStore := store
		if dryRun && repoPath != "." {
			var err error
			parentLookupStore, err = openDryRunTargetStore(rootCtx, repoPath)
			if err != nil {
				return HandleError("%v", err)
			}
			defer func() { _ = parentLookupStore.Close() }()
		}

		var inheritedLabels []string
		if parentID != "" {
			ctx := rootCtx
			_, err := parentLookupStore.GetIssue(ctx, parentID)
			if err != nil {
				if errors.Is(err, storage.ErrNotFound) {
					return HandleError("parent issue %s not found", parentID)
				}
				return HandleError("failed to check parent issue: %v", err)
			}

			noInheritLabels, _ := cmd.Flags().GetBool("no-inherit-labels")
			if !noInheritLabels {
				inheritedLabels, _ = parentLookupStore.GetLabels(ctx, parentID)
			}
		}

		labels = mergeCreateLabels(labels, inheritedLabels)

		if dryRun {
			return renderDryRun()
		}

		createCtx := rootCtx
		if parentID != "" {
			childID, err := store.GetNextChildID(rootCtx, parentID)
			if err != nil {
				return HandleError("%v", err)
			}
			explicitID = childID
			createCtx = storage.WithReservedChildCounter(createCtx, parentID, childID)
		}

		if explicitID != "" {
			_, err := validation.ValidateIDFormat(explicitID)
			if err != nil {
				return HandleError("%v", err)
			}

			ctx := createCtx

			var dbPrefix, allowedPrefixes string
			if yamlPrefix := config.GetString("issue-prefix"); yamlPrefix != "" {
				dbPrefix = yamlPrefix
			} else {
				dbPrefix, _ = store.GetConfig(ctx, "issue_prefix")
			}
			allowedPrefixes, _ = store.GetConfig(ctx, "allowed_prefixes")

			if err := validation.ValidateIDPrefixAllowed(explicitID, dbPrefix, allowedPrefixes, forceCreate); err != nil {
				return HandleError("%v", err)
			}
		}

		issue := buildCreateIssue(createIssueParams{
			ID:                 explicitID,
			Title:              title,
			Description:        description,
			Design:             design,
			AcceptanceCriteria: acceptance,
			Notes:              notes,
			SpecID:             specID,
			Priority:           priority,
			IssueType:          types.IssueType(issueType).Normalize(),
			Assignee:           assignee,
			ExternalRef:        externalRef,
			EstimatedMinutes:   estimatedMinutes,
			Ephemeral:          wisp,
			NoHistory:          noHistory,
			CreatedBy:          getActorWithGit(),
			Owner:              getOwner(),
			Labels:             labels,
			MolType:            molType,
			WispType:           wispType,
			EventKind:          eventCategory,
			Actor:              eventActor,
			Target:             eventTarget,
			Payload:            eventPayload,
			DueAt:              dueAt,
			DeferUntil:         deferUntil,
			Metadata:           metadata,
		})

		ctx := createCtx

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
			return HandleError("%v", err)
		}

		// Track whether any post-create writes occurred. CreateIssue commits
		// the issue and its initial labels to Dolt internally, but subsequent
		// AddDependency calls only write to the working set. A follow-up Dolt
		// commit is needed to persist them (GH#2009).
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

		// Add dependencies if specified (format: type:id or just id for default "blocks" type)
		for _, depSpec := range deps {
			depSpec = strings.TrimSpace(depSpec)
			if depSpec == "" {
				continue
			}

			var depType types.DependencyType
			var dependsOnID string
			swapDirection := false

			if strings.Contains(depSpec, ":") {
				parts := strings.SplitN(depSpec, ":", 2)
				if len(parts) != 2 {
					WarnError("invalid dependency format '%s', expected 'type:id' or 'id'", depSpec)
					continue
				}
				rawType := types.DependencyType(strings.TrimSpace(parts[0]))
				dependsOnID = strings.TrimSpace(parts[1])

				switch rawType {
				case "depends-on", "blocked-by":
					// Alias: the new issue depends on the target. Store as a blocks edge.
					depType = types.DepBlocks
				case types.DepBlocks:
					// Explicit "blocks:X" means the new issue blocks X, so store X -> new issue.
					depType = types.DepBlocks
					swapDirection = true
				default:
					depType = rawType
				}
			} else {
				depType = types.DepBlocks
				dependsOnID = depSpec
			}

			if !depType.IsValid() {
				return HandleErrorRespectJSON("invalid dependency type %q (must be non-empty, max 50 chars); valid types: %s", depType, createDepsAcceptedTypeList())
			}
			if !depType.IsWellKnown() {
				return HandleErrorRespectJSON("unknown dependency type %q; valid types: %s", depType, createDepsAcceptedTypeList())
			}

			dep := &types.Dependency{
				IssueID:     issue.ID,
				DependsOnID: dependsOnID,
				Type:        depType,
			}
			if swapDirection {
				dep.IssueID = dependsOnID
				dep.DependsOnID = issue.ID
			}
			if err := store.AddDependency(ctx, dep, actor); err != nil {
				WarnError("failed to add dependency %s -> %s: %v", issue.ID, dependsOnID, err)
			} else {
				postCreateWrites = true
			}
		}

		if waitsFor != "" {
			gate := waitsForGate
			if gate == "" {
				gate = types.WaitsForAllChildren
			}
			if gate != types.WaitsForAllChildren && gate != types.WaitsForAnyChildren {
				return HandleError("invalid --waits-for-gate value '%s' (valid: all-children, any-children)", gate)
			}

			meta := types.WaitsForMeta{
				Gate: gate,
			}
			metaJSON, err := json.Marshal(meta)
			if err != nil {
				return HandleError("failed to serialize waits-for metadata: %v", err)
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

		shouldCommit, err := shouldCommitCreatePostWrites(issue, postCreateWrites)
		if err != nil {
			return HandleError("dolt auto-commit failed: %v", err)
		}
		if shouldCommit {
			commitMsg := fmt.Sprintf("bd: create %s", issue.ID)
			if err := store.Commit(ctx, commitMsg); err != nil && !isDoltNothingToCommit(err) {
				WarnError("failed to commit: %v", err)
			}
		}

		if repoPath != "." && targetStore != nil {
			if err := commitPendingIfEmbedded(ctx, targetStore, actor, doltAutoCommitParams{
				Command:  "create",
				IssueIDs: []string{issue.ID},
			}); err != nil {
				debug.Logf("warning: failed to commit routed repo: %v", err)
			}
		}

		if remoteCache != nil {
			if pushErr := remoteCache.Push(rootCtx, repoPath); pushErr != nil {
				return HandleError("failed to push to %s: %v\nThe issue was created locally but not synced to the remote.", repoPath, pushErr)
			}
		}

		if jsonOutput {
			if err := outputJSON(issue); err != nil {
				return err
			}
		} else if silent {
			fmt.Println(issue.ID)
		} else {
			fmt.Printf("%s Created issue: %s\n", ui.RenderPass("✓"), formatFeedbackID(issue.ID, issue.Title))
			fmt.Printf("  Priority: P%d\n", issue.Priority)
			fmt.Printf("  Status: %s\n", issue.Status)

			maybeShowTip(store)
		}

		SetLastTouchedID(issue.ID)
		return nil
	},
}

type createIssueParams struct {
	ID                 string
	Title              string
	Description        string
	Design             string
	AcceptanceCriteria string
	Notes              string
	SpecID             string
	Priority           int
	IssueType          types.IssueType
	Assignee           string
	ExternalRef        string
	EstimatedMinutes   *int
	Ephemeral          bool
	NoHistory          bool
	CreatedBy          string
	Owner              string
	Labels             []string
	MolType            types.MolType
	WispType           types.WispType
	EventKind          string
	Actor              string
	Target             string
	Payload            string
	DueAt              *time.Time
	DeferUntil         *time.Time
	Metadata           json.RawMessage
}

func buildCreateIssue(params createIssueParams) *types.Issue {
	var externalRefPtr *string
	if params.ExternalRef != "" {
		externalRefPtr = &params.ExternalRef
	}

	status := types.StatusOpen
	if params.DeferUntil != nil && params.DeferUntil.After(time.Now()) {
		status = types.StatusDeferred
	}

	return &types.Issue{
		ID:                 params.ID,
		Title:              params.Title,
		Description:        params.Description,
		Design:             params.Design,
		AcceptanceCriteria: params.AcceptanceCriteria,
		Notes:              params.Notes,
		SpecID:             params.SpecID,
		Status:             status,
		Priority:           params.Priority,
		IssueType:          params.IssueType,
		Assignee:           params.Assignee,
		ExternalRef:        externalRefPtr,
		EstimatedMinutes:   params.EstimatedMinutes,
		Ephemeral:          params.Ephemeral,
		NoHistory:          params.NoHistory,
		CreatedBy:          params.CreatedBy,
		Owner:              params.Owner,
		Labels:             append([]string(nil), params.Labels...),
		MolType:            params.MolType,
		WispType:           params.WispType,
		EventKind:          params.EventKind,
		Actor:              params.Actor,
		Target:             params.Target,
		Payload:            params.Payload,
		DueAt:              params.DueAt,
		DeferUntil:         params.DeferUntil,
		Metadata:           params.Metadata,
	}
}

func mergeCreateLabels(labels, inheritedLabels []string) []string {
	merged := make([]string, 0, len(labels)+len(inheritedLabels))
	seen := make(map[string]struct{}, len(labels)+len(inheritedLabels))
	for _, label := range labels {
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		merged = append(merged, label)
	}
	for _, label := range inheritedLabels {
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		merged = append(merged, label)
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

func renderCreateDryRunPreview(issue *types.Issue, labels, deps []string) {
	idDisplay := issue.ID
	if idDisplay == "" {
		idDisplay = "(will be generated)"
	}
	fmt.Printf("%s [DRY RUN] Would create issue:\n", ui.RenderWarn("⚠"))
	fmt.Printf("  ID: %s\n", idDisplay)
	fmt.Printf("  Title: %s\n", issue.Title)
	fmt.Printf("  Type: %s\n", issue.IssueType)
	fmt.Printf("  Priority: P%d\n", issue.Priority)
	fmt.Printf("  Status: %s\n", issue.Status)
	if issue.Assignee != "" {
		fmt.Printf("  Assignee: %s\n", issue.Assignee)
	}
	if issue.Description != "" {
		fmt.Printf("  Description: %s\n", issue.Description)
	}
	if len(labels) > 0 {
		fmt.Printf("  Labels: %s\n", strings.Join(labels, ", "))
	}
	if len(deps) > 0 {
		fmt.Printf("  Dependencies: %s\n", strings.Join(deps, ", "))
	}
	if issue.EventKind != "" {
		fmt.Printf("  Event category: %s\n", issue.EventKind)
	}
}

func shouldCommitCreatePostWrites(_ *types.Issue, _ bool) (bool, error) {
	if isEmbeddedMode() {
		if strings.TrimSpace(doltAutoCommit) == "" {
			return true, nil
		}
		mode, err := getDoltAutoCommitMode()
		if err != nil {
			return false, err
		}
		return mode == doltAutoCommitOn, nil
	}
	return false, nil
}

func createDepsAcceptedTypeList() string {
	names := []string{"blocked-by", "depends-on"}
	for _, depType := range types.WellKnownDependencyTypes() {
		names = append(names, string(depType))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func init() {
	createCmd.Flags().StringP("file", "f", "", "Create multiple issues from markdown file")
	createCmd.Flags().String("graph", "", "Create a graph of issues with dependencies from JSON plan file")
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

// formatTimeForRPC converts a *time.Time to RFC3339 string for RPC calls.
// Returns empty string if t is nil, to distinguish "not set" from "set to zero".
func formatTimeForRPC(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339)
}

func openDryRunTargetStore(ctx context.Context, repoPath string) (storage.DoltStorage, error) {
	if remotecache.IsRemoteURL(repoPath) {
		cache, err := remotecache.DefaultCache()
		if err != nil {
			return nil, fmt.Errorf("failed to initialize remote cache: %w", err)
		}
		// The dry-run parent lookup only reads from this cached remote store.
		// Do not add writes here; dry-runs must not mutate cached remotes.
		store, err := cache.OpenStore(ctx, repoPath, newDoltStoreFromConfig)
		if err != nil {
			return nil, fmt.Errorf("dry-run parent lookup requires an existing cached remote store for %s: %w", repoPath, err)
		}
		return store, nil
	}

	targetPath := routing.ExpandPath(repoPath)
	beadsDir := filepath.Join(targetPath, ".beads")
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	if _, err := os.Stat(metadataPath); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("target repo %s is not initialized; refusing to initialize it during dry-run", targetPath)
		}
		return nil, fmt.Errorf("failed to inspect target repo %s: %w", targetPath, err)
	}

	store, err := newDoltStoreFromConfig(ctx, beadsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to open target store for dry-run: %w", err)
	}
	return store, nil
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
			// Sanitize prefix for SQL database name (same as bd init).
			dbName := strings.ReplaceAll(sourcePrefix, "-", "_")

			// Open target store temporarily to set prefix.
			// Use newDoltStore with explicit config since the target .beads
			// directory was just created and has no metadata.json yet.
			tempStore, err := newDoltStore(ctx, &dolt.Config{
				BeadsDir:        beadsDir,
				Database:        dbName,
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

			// Write metadata.json so newDoltStoreFromConfig can find the
			// correct database name on subsequent opens (GH#2988).
			cfg := configfile.DefaultConfig()
			cfg.Backend = configfile.BackendDolt
			cfg.DoltDatabase = dbName
			cfg.DoltMode = configfile.DoltModeEmbedded
			cfg.ProjectID = configfile.GenerateProjectID()
			if err := cfg.Save(beadsDir); err != nil {
				return fmt.Errorf("failed to write metadata.json: %w", err)
			}
		}
	}

	return nil
}
