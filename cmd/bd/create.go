package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/remotecache"
	"github.com/steveyegge/beads/internal/routing"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/timeparsing"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
	"github.com/steveyegge/beads/internal/validation"
	"github.com/steveyegge/beads/issueops"
)

// validateCreateArgs runs as cobra's Args validation, which executes before
// PersistentPreRunE opens the store or runs migrations. It reuses
// resolveTitle — the same shared validator gatherCreateInput calls for the
// proxied-server create path — so a whitespace-only title (GH#4771) is
// rejected identically for both backends, and before any invocation that is
// guaranteed to fail wastes a store open/migration.
func validateCreateArgs(cmd *cobra.Command, args []string) error {
	markdownFile, _ := cmd.Flags().GetString("file")
	graphFile, _ := cmd.Flags().GetString("graph")
	titleFlag, _ := cmd.Flags().GetString("title")

	_, err := resolveTitle(args, titleFlag, markdownFile, graphFile)
	return err
}

var createCmd = &cobra.Command{
	Use:           "create [title]",
	GroupID:       "issues",
	Aliases:       []string{"new"},
	Short:         "Create a new issue (or batch from markdown/graph JSON)",
	Args:          cobra.MatchAll(cobra.MaximumNArgs(1), validateCreateArgs),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("create") // also covers the migration freeze check (dc-6jaq)

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
			return runCreateProxiedServer(cmd, rootCtx, in)
		}
		file, _ := cmd.Flags().GetString("file")
		graphFile, _ := cmd.Flags().GetString("graph")

		if file != "" {
			// gatherCreateInput repeats the --file argument checks and applies
			// the plan-wide flags this route used to accept and ignore
			// (--ephemeral, --no-history, --mol-type, --validate). It is the
			// same input the proxied route reads, which is what lets both build
			// one issueops.CreateBatchRequest.
			in, err := gatherCreateInput(cmd, args)
			if err != nil {
				return err
			}
			return createIssuesFromMarkdown(rootCtx, in)
		}

		if graphFile != "" {
			if len(args) > 0 {
				return HandleError("cannot specify both title and --graph flag")
			}
			graphDryRun, _ := cmd.Flags().GetBool("dry-run")
			graphOpts := graphApplyOptionsFromFlags(cmd)
			if err := graphOpts.Validate(); err != nil {
				return HandleError("invalid graph options: %v", err)
			}
			if err := rejectSingleIssueFlagsForGraph(cmd); err != nil {
				return err
			}
			return createIssuesFromGraph(graphFile, graphDryRun, graphOpts)
		}

		titleFlag, _ := cmd.Flags().GetString("title")
		title, err := resolveTitle(args, titleFlag, "", "")
		if err != nil {
			return err
		}

		// Get silent flag
		silent, _ := cmd.Flags().GetBool("silent")

		// Warn if creating a test issue in a database with existing issues.
		// A brand-new repo with zero issues is not a "production database" (#2898).
		// store is nil here for `create --repo=<remote URL>` from a directory with
		// no local .beads/: PersistentPreRunE skips local store init entirely for
		// that path, so this check is best-effort only.
		if store != nil && isTestIssue(title) && !silent && !debug.IsQuiet() {
			if stats, err := store.GetStatistics(context.Background()); err == nil && stats != nil && stats.TotalIssues >= 5 {
				fmt.Fprintf(os.Stderr, "%s Creating test issue in production database\n", ui.RenderWarn("⚠"))
				fmt.Fprintf(os.Stderr, "  Title: %q appears to be test data\n", title)
				fmt.Fprintf(os.Stderr, "  Recommendation: Use isolated test database with --db\n")
				fmt.Fprintf(os.Stderr, "    bd --db /tmp/test-beads create %q\n", title)
			}
		}

		description, descriptionChanged, err := getDescriptionFlag(cmd)
		if err != nil {
			return err
		}
		if err := validateDescriptionUpdate(cmd, description, descriptionChanged); err != nil {
			return HandleError("%v", err)
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
		// No --priority asks the role for its default (CreateRequest.
		// DefaultPriority). The flag's displayed default is that same library
		// constant, so the dry-run preview below still shows what will land.
		priorityDefault := !cmd.Flags().Changed("priority")

		issueType, _ := cmd.Flags().GetString("type")
		assignee, _ := cmd.Flags().GetString("assignee")
		statusFlag, _ := cmd.Flags().GetString("status")
		if statusFlag != "" {
			if !types.Status(statusFlag).IsValidWithCustom(loadEmbeddedCustomStatuses()) {
				return HandleErrorRespectJSON("invalid status %q (built-in: open, in_progress, blocked, deferred, closed, pinned, hooked; or configure custom statuses via 'bd config set status.custom')", statusFlag)
			}
		}

		labels, _ := cmd.Flags().GetStringSlice("labels")
		labelAlias, _ := cmd.Flags().GetStringSlice("label")
		if len(labelAlias) > 0 {
			labels = append(labels, labelAlias...)
		}
		labels = utils.NormalizeLabels(labels)
		warnLabelsContainingWhitespace(labels)

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
		storageClassFlag, _ := cmd.Flags().GetString("storage-class")
		storageClass, wisp, err := resolveCreateStorageClass(storageClassFlag, types.IssueType(issueType), wisp, noHistory)
		if err != nil {
			return HandleError("%v", err)
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
				return HandleError("invalid --due format %q. %s", dueStr, deferUntilFormatHint)
			}
			dueAt = &t
		}

		var deferUntil *time.Time
		deferStr, _ := cmd.Flags().GetString("defer")
		if deferStr != "" {
			t, err := timeparsing.ParseRelativeTime(deferStr, time.Now())
			if err != nil {
				return HandleError("invalid --defer format %q. %s", deferStr, deferUntilFormatHint)
			}
			// Warn if defer date is in the past (user probably meant future)
			if t.Before(time.Now()) && !silent && !debug.IsQuiet() {
				fmt.Fprintf(os.Stderr, "%s Defer date %q is in the past. Issue will appear in bd ready immediately.\n",
					ui.RenderWarn("!"), t.Local().Format("2006-01-02 15:04"))
				fmt.Fprintf(os.Stderr, "  Did you mean a future date? Use --defer=+1h or --defer=tomorrow\n")
			}
			deferUntil = &t
		}

		var metadata json.RawMessage
		if cmd.Flags().Changed("metadata") {
			metadataValue, _ := cmd.Flags().GetString("metadata")
			parsed, err := readMetadataFlag(metadataValue)
			if err != nil {
				return HandleError("%v", err)
			}
			metadata = parsed
		}

		validateTemplate, _ := cmd.Flags().GetBool("validate")
		validationMode := config.GetString("validation.on-create")
		if validateTemplate || validationMode == "error" || validationMode == "warn" {
			lintIssue := &types.Issue{
				IssueType:          types.IssueType(issueType).Normalize(),
				Title:              title,
				Description:        description,
				Design:             design,
				AcceptanceCriteria: acceptance,
				Notes:              notes,
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

			// Auto-routing based on user role, detected only once the resolved
			// config shows routing will read it.
			userRole := detectUserRoleForRouting(routingConfig)
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
				StorageClass:       storageClass,
				CreatedBy:          getActorWithGit(),
				Owner:              getOwner(),
				Labels:             labels,
				MolType:            molType,
				WispType:           wispType,
				InitialStatus:      statusFlag,
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
			// The preview opens no target, but it refuses a local one in a
			// format this build cannot open or that admission refuses as a
			// historical SQLite workspace, as the create it previews would and
			// in its words,
			// and one with no metadata.json the create would refuse to
			// initialize (refuseUninitializableTarget). Neither check writes.
			if err := checkRepoTargetFormat(repoPath); err != nil {
				return handleRepoTargetInitError(routing.ExpandPath(repoPath), err)
			}
			allowCreate := !isAmbiguousRepoTarget(cmd.Flags().Changed("repo"), repoOverride)
			if targetBeadsDir, err := checkRepoTargetInitializable(repoPath, allowCreate); err != nil {
				return handleRepoTargetInitError(targetBeadsDir, err)
			}
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
					return HandleError("failed to sync remote %s: %v", remedy.PrintedValue(repoPath), err)
				}
				targetStore, err = remoteCache.OpenStore(rootCtx, repoPath, newDoltStoreFromConfig)
				if err != nil {
					return HandleError("failed to open remote store: %v", err)
				}
			} else {
				targetBeadsDir := routing.ExpandPath(repoPath)
				debug.Logf("DEBUG: Routing to target repo: %s\n", targetBeadsDir)

				// Auto-routed paths (routing.mode: auto, routing.default)
				// come from config, not caller input, so they're always
				// allowed to auto-vivify as before; only an explicit --repo
				// flag value can be ambiguous.
				allowCreate := !isAmbiguousRepoTarget(cmd.Flags().Changed("repo"), repoOverride)
				if err := ensureBeadsDirForPath(rootCtx, targetBeadsDir, store, allowCreate); err != nil {
					return handleRepoTargetInitError(targetBeadsDir, err)
				}

				targetBeadsDirPath := filepath.Join(targetBeadsDir, ".beads")
				var err error
				targetStore, err = openForeignStore(rootCtx, targetBeadsDirPath, foreignCreate)
				if err != nil {
					return handleCreateTargetStoreError(err, targetBeadsDirPath, "failed to open target store: %v")
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
				targetPath := repoPath
				if !remotecache.IsRemoteURL(repoPath) {
					targetPath = filepath.Join(routing.ExpandPath(repoPath), ".beads")
				}
				return handleCreateTargetStoreError(err, targetPath, "%v")
			}
			defer func() { _ = parentLookupStore.Close() }()
		}

		noInheritLabels, _ := cmd.Flags().GetBool("no-inherit-labels")
		inheritParentLabels := parentID != "" && !noInheritLabels
		if parentID != "" {
			// A fail-fast with the message this command has always printed;
			// the create role refuses a missing parent on its own as well.
			ctx := rootCtx
			_, err := parentLookupStore.GetIssue(ctx, parentID)
			if err != nil {
				if errors.Is(err, storage.ErrNotFound) {
					return HandleError("parent issue %s not found", parentID)
				}
				return HandleError("failed to check parent issue: %v", err)
			}
		}

		if dryRun {
			// The preview shows what the create role would inherit; the real
			// create asks the role to inherit (CreateRequest.InheritLabelsFromParent)
			// inside its own transaction. A failed read is reported, never
			// previewed as "no labels".
			if inheritParentLabels {
				inherited, err := parentLookupStore.GetLabels(rootCtx, parentID)
				if err != nil {
					return HandleError("failed to read the labels of parent %s: %v", parentID, err)
				}
				labels = mergeCreateLabels(labels, inherited)
			}
			return renderDryRun()
		}

		// Parse every requested dependency edge BEFORE reserving a child ID
		// or creating anything so a malformed spec aborts with no burned
		// child ID and no orphan issue behind it.
		depSpecs, err := parseDepSpecs(deps)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		waitsForSpec, err := buildWaitsFor(waitsFor, waitsForGate, cmd.Flags().Changed("waits-for-gate"))
		if err != nil {
			return HandleError("%v", err)
		}

		createCtx := rootCtx
		// The --parent child id is minted by the Lifecycle role, on every
		// backend and route: a create with a ParentID and no id of its own is
		// <parent>.<n>, minted inside the create's own transaction (conformance
		// RunIssueOperationsCreateUnderAParentMintsTheNextChildID, served leg
		// included), and the role stages the advanced counter itself. An
		// explicit --id never reaches here with a parent (refused above).

		if explicitID != "" {
			_, err := validation.ValidateIDFormat(explicitID)
			if err != nil {
				return HandleError("%v", err)
			}

			// Validate prefix matches database prefix (YAML config takes
			// precedence over DB, except under --global — see
			// loadEmbeddedIDPrefixes).
			dbPrefix, allowedPrefixes := loadEmbeddedIDPrefixes()

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
			StorageClass:       storageClass,
			Owner:              getOwner(),
			Labels:             labels,
			MolType:            molType,
			WispType:           wispType,
			EventKind:          eventCategory,
			Actor:              eventActor,
			Target:             eventTarget,
			Payload:            eventPayload,
			InitialStatus:      statusFlag,
			DueAt:              dueAt,
			DeferUntil:         deferUntil,
			Metadata:           metadata,
		})

		ctx := createCtx

		// Resolve partial --deps targets the way `bd dep add` does, so a bare
		// slug becomes a qualified id rather than a dangling edge, and an
		// unknown target fails closed here instead of reaching the write.
		resolvedDepSpecs, resolveErr := resolveDepSpecTargets(ctx, store, depSpecs)
		if resolveErr != nil {
			return HandleErrorRespectJSON("%v", resolveErr)
		}
		depSpecs = resolvedDepSpecs

		// If a discovered-from dependency is present, inherit source_repo
		// from the referenced parent issue. Reuse the already-parsed specs
		// (not the raw --deps strings) so this can't drift from parseDepSpec's
		// normalization rules.
		if dfParent := discoveredFromParentSpec(depSpecs); dfParent != "" {
			parentIssue, err := store.GetIssue(ctx, dfParent)
			if err == nil && parentIssue.SourceRepo != "" {
				issue.SourceRepo = parentIssue.SourceRepo
			}
			// If error getting parent or parent has no source_repo, continue with default
		}

		ops, err := writeOps(store)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		opsCtx, err := issueOpsContext(ctx)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		// The parent's labels are the role's to inherit, inside the create.
		if priorityDefault {
			issue.Priority = 0
		}
		result, err := ops.Create(opsCtx, issueops.CreateRequest{
			Actor:                   currentActor(),
			Issue:                   issue,
			ParentID:                parentID,
			InheritLabelsFromParent: inheritParentLabels,
			Dependencies:            createDependencyRequests(depSpecs),
			WaitsFor:                waitsForRequest(waitsForSpec),
			ForceIDPrefix:           forceCreate,
			IDPrefix:                createIDPrefixOverride(),
			DefaultPriority:         priorityDefault,
		})
		if err != nil {
			if handled, rendered := reportCreateOfRetiredID(err); handled {
				return rendered
			}
			// RULING R1: an occupied --id is a refusal, not a silent full-row
			// upsert reported as success.
			if errors.Is(err, storage.ErrAlreadyExists) && explicitID != "" {
				return HandleErrorRespectJSON("%s already exists; use bd update, or bd import for upsert semantics", explicitID)
			}
			return HandleErrorRespectJSON("%v", err)
		}
		// Every post-write read comes from the facade's result snapshot, never
		// from the local struct: the facade clones its request, so the local
		// struct still has no ID for an auto-minted create and no persisted
		// timestamps. Dependencies and comments are dropped because `bd create`
		// has never printed them.
		created := result.Issue
		created.Dependencies = nil
		created.Comments = nil

		edges := createDepEdges{parentID: parentID, specs: depSpecs, waitsFor: waitsForSpec}
		if edges.empty() {
			// Bare create: preserve the embedded-mode follow-up Dolt commit.
			// The deps path commits inside its transaction instead.
			shouldCommit, err := shouldCommitCreatePostWrites(created, false)
			if err != nil {
				return HandleError("dolt auto-commit failed: %v", err)
			}
			if shouldCommit {
				commitMsg := fmt.Sprintf("bd: create %s", created.ID)
				if err := store.Commit(ctx, commitMsg); err != nil && !isDoltNothingToCommit(err) {
					WarnError("failed to commit: %v", err)
				}
			}
		}

		if repoPath != "." && targetStore != nil {
			if err := commitPendingIfEmbedded(ctx, targetStore, currentActor(), doltAutoCommitParams{
				Command:  "create",
				IssueIDs: []string{created.ID},
			}); err != nil {
				debug.Logf("warning: failed to commit routed repo: %v", err)
			}
		}

		if remoteCache != nil {
			if pushErr := remoteCache.Push(rootCtx, repoPath); pushErr != nil {
				return HandleError("failed to push to %s: %v\nThe issue was created locally but not synced to the remote.", remedy.PrintedValue(repoPath), pushErr)
			}
		}

		if jsonOutput {
			if err := outputJSON(created); err != nil {
				return err
			}
		} else if silent {
			fmt.Println(created.ID)
		} else {
			debug.PrintNormal("%s Created issue: %s\n", ui.RenderPass("✓"), formatFeedbackID(created.ID, created.Title))
			debug.PrintNormal("  Priority: P%d\n", created.Priority)
			debug.PrintNormal("  Status: %s\n", created.Status)

			maybeShowTip(store)
		}

		SetLastTouchedID(created.ID)
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
	StorageClass       types.StorageClass
	CreatedBy          string
	Owner              string
	Labels             []string
	MolType            types.MolType
	WispType           types.WispType
	EventKind          string
	Actor              string
	Target             string
	Payload            string
	InitialStatus      string
	DueAt              *time.Time
	DeferUntil         *time.Time
	Metadata           json.RawMessage
}

// resolveStorageClass resolves the requested storage class at create time
// (Protocol v0.1 C1.3): the explicit --storage-class flag wins; otherwise the
// per-type config default storage-class.<type> applies; otherwise unset.
// The parsed class is returned verbatim, including versioned. The caller must
// normalize versioned to the unset marker (the marker is omitted when
// versioned, C2.4) only AFTER plane-conflict validation, so an explicit durable
// request paired with a wisp-plane flag is rejected rather than silently erased
// into an effective-ephemeral row. Values are validated wherever they came
// from: a bad flag is a usage error, a bad config value is a config bug and
// fails just as loudly.
func resolveStorageClass(explicit string, issueType types.IssueType) (types.StorageClass, error) {
	raw := explicit
	if raw == "" {
		raw = config.GetString("storage-class." + string(issueType))
		if raw == "" {
			return "", nil
		}
	}
	class, err := types.ParseStorageClass(raw)
	if err != nil {
		if explicit == "" {
			return "", fmt.Errorf("config storage-class.%s: %w", issueType, err)
		}
		return "", err
	}
	return class, nil
}

// reconcileStorageClassPlane applies flag-over-config precedence between a
// resolved storage class and the effective wisp plane (Protocol v0.1 §C1.3).
// A wisp-plane record is ephemeral by construction, so a durable class
// (versioned/unversioned) cannot ride on it. explicit reports whether the class
// came from an explicit flag/field rather than a per-type config default:
//   - explicit durable class + wisp plane -> conflict=true; the caller rejects
//     it so the durable intent is preserved rather than silently collapsed into
//     an effective-ephemeral record;
//   - config-derived durable class + wisp plane -> cleared, yielding to the
//     explicit --ephemeral/--no-history plane.
//
// versioned normalizes to the unset marker (C2.4) only after the check, so the
// durable request survives long enough to be honored, rejected, or yielded. On
// conflict the class is returned verbatim so the caller can name it in the error.
func reconcileStorageClassPlane(class types.StorageClass, explicit, wispPlane bool) (types.StorageClass, bool) {
	if class != "" && wispPlane {
		if explicit {
			return class, true
		}
		class = "" // config default yields to the explicit wisp-plane flag
	}
	return class.Normalize(), false
}

// resolveCreateStorageClass is the ONE create-time storage-class decision every
// `bd create` CLI door makes: parse the flag (or fall back to the per-type
// storage-class.<type> config default), route the ephemeral spelling to the wisp
// plane, and reject a durable class that contradicts the plane. Every door goes
// through it so the direct, proxied, and --file routes cannot answer differently;
// graphApplyNodeStorageClass is the per-node mirror, which additionally honors a
// node's own pointer overrides.
//
// flagValue is the raw --storage-class spelling ("" when unset). wisp/noHistory
// are the plane flags as the caller read them; the returned wisp is the plane
// AFTER the ephemeral spelling has been folded in (Protocol v0.1 C1.4). The
// returned class is already normalized (C2.4), so it can go straight onto the
// issue.
func resolveCreateStorageClass(flagValue string, issueType types.IssueType, wisp, noHistory bool) (types.StorageClass, bool, error) {
	class, err := resolveStorageClass(flagValue, issueType.Normalize())
	if err != nil {
		return "", wisp, err
	}
	// --storage-class ephemeral is the spelled-out spelling of --ephemeral
	// (Protocol v0.1 C1.4: the wisp plane is today's ephemeral-class
	// implementation). It routes to the wisp path exactly like the flag; the
	// --ephemeral/--no-history mutual exclusion still applies.
	if class == types.StorageClassEphemeral {
		if noHistory {
			return "", wisp, errors.New("--storage-class ephemeral and --no-history are mutually exclusive")
		}
		wisp = true
		class = "" // wisp-plane rows derive ephemeral class (C1.2); no marker cell needed
	}
	// Reconcile the requested durable class with the effective wisp plane
	// (flag > config, Protocol v0.1 §C1.3): an explicit --storage-class
	// contradicts --ephemeral/--no-history and is rejected, so the durable
	// intent is preserved rather than silently collapsed into an
	// effective-ephemeral record; a per-type config default yields to the
	// explicit flag. versioned normalizes to the unset marker only after the
	// check (C2.4).
	class, conflict := reconcileStorageClassPlane(class, flagValue != "", wisp || noHistory)
	if conflict {
		return "", wisp, fmt.Errorf("--storage-class %s conflicts with --ephemeral/--no-history: wisp-plane records are storage class ephemeral", class)
	}
	return class, wisp, nil
}

func buildCreateIssue(params createIssueParams) *types.Issue {
	var externalRefPtr *string
	if params.ExternalRef != "" {
		externalRefPtr = &params.ExternalRef
	}

	status := types.StatusOpen
	if params.InitialStatus != "" {
		status = types.Status(params.InitialStatus)
	} else if params.DeferUntil != nil && params.DeferUntil.After(time.Now()) {
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
		StorageClass:       params.StorageClass,
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

// createIDPrefixOverride is the prefix an explicit --id must match, when the
// WORKSPACE knows better than the database does.
//
// config.yaml's `issue-prefix` wins over the database's, except under --global
// where the shared database is authoritative (GH#4957, selectCreateIDPrefix).
// Only a front door can read config.yaml — a shared server's database knows
// only its own prefix — so both routes resolve it here and hand it to the role
// as CreateRequest.IDPrefix. Empty means "the substrate's prefix is right",
// which is the ordinary case.
func createIDPrefixOverride() string {
	if globalFlag {
		return ""
	}
	return overlayYAMLPrefix("")
}

func selectCreateIDPrefix(global bool, yamlPrefix, storePrefix string) string {
	if global {
		return storePrefix
	}
	if yamlPrefix != "" {
		return yamlPrefix
	}
	return storePrefix
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
	return embeddedWritesCommitNow()
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
	createCmd.Flags().StringP("file", "f", "", "Create one issue per ## heading (### fields attach to that issue); for title plus description-from-file, use --body-file")
	createCmd.Flags().String("graph", "", "Create a graph of issues with dependencies from JSON plan file")
	createCmd.Flags().String("title", "", "Issue title (alternative to positional argument)")
	createCmd.Flags().Bool("silent", false, "Output only the issue ID (for scripting)")
	createCmd.Flags().Bool("dry-run", false, "Preview what would be created without actually creating")
	registerPriorityFlag(createCmd, strconv.Itoa(issueops.DefaultCreatePriority))
	createCmd.Flags().StringP("type", "t", "task", "Issue type (bug|feature|task|epic|chore|decision|spike|story|milestone); custom types require types.custom config; aliases: enhancement/feat→feature, dec/adr→decision")
	createCmd.Flags().StringP("status", "s", "", "Initial status")
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
	createCmd.Flags().StringSlice("deps", []string{}, "Dependencies as 'type:id' or bare 'id'. Bare 'id', 'depends-on:id', and 'blocked-by:id' all make THIS issue depend on id; 'blocks:id' reverses direction (id depends on this issue). E.g. 'blocked-by:bd-20,discovered-from:bd-15'")
	createCmd.Flags().String("waits-for", "", "Spawner issue ID to wait for (creates waits-for dependency for fanout gate)")
	createCmd.Flags().String("waits-for-gate", "all-children", "Gate type: all-children (wait for all) or any-children (wait for first)")
	createCmd.Flags().Bool("force", false, "Force creation even if prefix doesn't match database prefix")
	createCmd.Flags().String("repo", "", "Target repository for issue (overrides auto-routing)")
	createCmd.Flags().IntP("estimate", "e", 0, "Time estimate in minutes (e.g., 60 for 1 hour)")
	createCmd.Flags().Bool("ephemeral", false, "Create as ephemeral (short-lived, subject to TTL compaction)")
	createCmd.Flags().Bool("no-history", false, "Skip Dolt commit history without making GC-eligible (for permanent agent beads)")
	createCmd.Flags().String("storage-class", "", "Storage class: versioned, unversioned, or ephemeral (default: storage-class.<type> config, else versioned)")
	createCmd.Flags().String("mol-type", "", "Molecule type: swarm (multi-agent), patrol (recurring ops), work (default)")
	createCmd.Flags().String("wisp-type", "", "Wisp type for TTL-based compaction: heartbeat, ping, patrol, gc_report, recovery, error, escalation")
	createCmd.Flags().Bool("validate", false, "Validate description contains required sections for issue type")
	createCmd.Flags().Bool("allow-empty-description", false, "Allow empty description input from stdin or file")
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
	createCmd.Flags().String("metadata", "", "Set custom metadata (JSON object, or @file.json to read from file)")
	// Note: --json flag is defined as a persistent flag in main.go, not here
	rootCmd.AddCommand(createCmd)
}

// handleCreateTargetStoreError preserves the existing text path for ordinary
// store failures while rendering capability refusals as typed JSON when asked,
// a target in a format this build cannot open as the workspace refusal, and a
// target bd's admission refuses as a legacy layout, or that
// refuseExistingCreateTarget refuses, as handleRepoTargetInitError renders it
// ("target repo X: ..."). targetPath is the target's .beads, or a remote URL.
func handleCreateTargetStoreError(err error, targetPath, fallbackFormat string) error {
	if isUnsupportedWorkspaceFormat(err) {
		return handleWorkspaceGuardError(err)
	}
	var existing *existingTargetRefusal
	if isLegacyUpgradeRefusal(err) || errors.As(err, &existing) {
		repo := targetPath
		if filepath.Base(repo) == ".beads" {
			repo = filepath.Dir(repo)
		}
		return handleRepoTargetInitError(repo, err)
	}
	var capErr *ProxyCapabilityError
	if !errors.As(err, &capErr) {
		// HandleError spells the message through remedy.PrintedText, so an
		// open error that repeats the target path cannot redraw the line.
		return HandleError(fallbackFormat, err)
	}

	withContext := *capErr
	withContext.Message = fmt.Sprintf("failed to open create target %s: %s", remedy.EscapedValue(targetPath), capErr.Message)
	if capErr.Code == "proxy.store.unrouted" {
		withContext.Message = fmt.Sprintf("create target %s is a proxied-server workspace; bd cannot open it as a direct store", remedy.EscapedValue(targetPath))
	}
	return HandleProxyCapabilityError(&withContext)
}

// formatTimeForRPC converts a *time.Time to RFC3339 string for RPC calls.
// Returns empty string if t is nil, to distinguish "not set" from "set to zero".
func formatTimeForRPC(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339)
}

// openDryRunTargetStore opens the store a `create --dry-run --repo <other>`
// resolves --parent against. It is read-only on BOTH paths and must stay that
// way: newDoltStoreFromConfig runs schema initialization on whatever it opens
// and can rename a legacy hyphenated database and rewrite the target's
// metadata.json on the way (GH#3231), so using it here would have a dry-run
// mutate a repository the user only named as a lookup target — the same
// migrate-at-open trap this preview policy exists to close, one repo over.
// newPreviewStoreFromConfig is the non-mutating factory for a foreign
// project (bd-6dnrw.32), relaxed for previews exactly as the root pre-run
// relaxes the command's own store.
func openDryRunTargetStore(ctx context.Context, repoPath string) (storage.DoltStorage, error) {
	if remotecache.IsRemoteURL(repoPath) {
		cache, err := remotecache.DefaultCache()
		if err != nil {
			return nil, fmt.Errorf("failed to initialize remote cache: %w", err)
		}
		// The dry-run parent lookup only reads from this cached remote store.
		// Do not add writes here; dry-runs must not mutate cached remotes.
		store, err := cache.OpenStore(ctx, repoPath, newPreviewStoreFromConfig)
		if err != nil {
			return nil, fmt.Errorf("dry-run parent lookup requires an existing cached remote store for %s: %w", remedy.PrintedValue(repoPath), err)
		}
		return store, nil
	}

	targetPath := routing.ExpandPath(repoPath)
	beadsDir := filepath.Join(targetPath, ".beads")
	// A target in a format this build cannot open, or one admission refuses
	// as a historical SQLite workspace, is refused as that, even one marked
	// only by a legacy config.json, which the check below would report as
	// not initialized.
	if err := checkDiscoveredForOpen(beadsDir); err != nil {
		return nil, err
	}
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	if _, err := os.Stat(metadataPath); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("target repo %s is not initialized; refusing to initialize it during dry-run", remedy.PrintedValue(targetPath))
		}
		return nil, fmt.Errorf("failed to inspect target repo %s: %w", remedy.PrintedValue(targetPath), remedy.PrintedError(err))
	}

	// The create it previews refuses this target before it opens it
	// (ensureBeadsDirForPath); so does the preview, in the same words.
	if err := refuseExistingCreateTarget(beadsDir); err != nil {
		return nil, err
	}
	store, err := openForeignStore(ctx, beadsDir, foreignPreview)
	if err != nil {
		return nil, fmt.Errorf("failed to open target store for dry-run: %w", remedy.PrintedTextError(err))
	}
	return store, nil
}

// checkRepoTargetFormat refuses a local create target in a format this build
// cannot open, or one admission refuses as a historical SQLite workspace
// (checkDiscoveredForOpen), reading its metadata without opening or migrating
// it. The
// command's own workspace (".") has passed admission already, and a remote
// target has no local metadata to read, so both pass.
func checkRepoTargetFormat(repoPath string) error {
	if repoPath == "." || remotecache.IsRemoteURL(repoPath) {
		return nil
	}
	return checkDiscoveredForOpen(filepath.Join(routing.ExpandPath(repoPath), ".beads"))
}

// checkRepoTargetInitializable runs, for a local create target, the refusals
// ensureBeadsDirForPath applies before it opens anything: for a .beads with a
// metadata.json, refuseExistingCreateTarget, and for one without,
// refuseUninitializableTarget, so a dry-run preview refuses what the create it
// previews would. It opens no database, so the refusal of an adopted
// database's uncommitted changes (refuseUncommittedTargetChanges) is left to
// the create. It writes nothing. The command's own workspace and a remote
// target pass, and so does a target with no metadata.json that the create
// would not initialize at all (allowCreate false). It returns the target path
// for handleRepoTargetInitError.
func checkRepoTargetInitializable(repoPath string, allowCreate bool) (string, error) {
	if repoPath == "." || remotecache.IsRemoteURL(repoPath) {
		return "", nil
	}
	targetPath := routing.ExpandPath(repoPath)
	beadsDir := filepath.Join(targetPath, ".beads")
	if _, err := os.Stat(filepath.Join(beadsDir, "metadata.json")); err == nil {
		return targetPath, refuseExistingCreateTarget(beadsDir)
	}
	if !allowCreate {
		return "", nil
	}
	return targetPath, refuseUninitializableTarget(beadsDir)
}

// handleRepoTargetInitError renders a create target's initialization failure
// (ensureBeadsDirForPath, or the preview's checkRepoTargetInitializable).
func handleRepoTargetInitError(targetPath string, err error) error {
	if isUnsupportedWorkspaceFormat(err) {
		return handleWorkspaceGuardError(err)
	}
	if isLegacyUpgradeRefusal(err) {
		// bd's own admission refusal of the target, named, as bd renders it
		// there: nothing was initialized.
		return handleWorkspaceGuardError(fmt.Errorf("target repo %s: %w", remedy.PrintedValue(targetPath), err))
	}
	var existing *existingTargetRefusal
	if errors.As(err, &existing) {
		return HandleError("target repo %s: %s", remedy.PrintedValue(targetPath), existing.msg)
	}
	return HandleError("failed to initialize target repo: %v", err)
}

// isAmbiguousRepoTarget reports whether an explicit --repo value is a
// bare/relative filesystem path (not absolute, not "~/"-prefixed). Such a
// value silently resolves against the current working directory (see
// routing.ExpandPath) rather than failing, so a misresolved value (e.g. a
// typo) previously wrote a bead into a brand-new, disconnected database
// instead of erroring (bd-8d3f).
func isAmbiguousRepoTarget(repoFlagChanged bool, repoOverride string) bool {
	return repoFlagChanged && !filepath.IsAbs(repoOverride) && !strings.HasPrefix(repoOverride, "~/")
}

// ensureBeadsDirForPath ensures a beads directory exists at the target path.
// A .beads that already has a metadata.json is left as it is, unless
// refuseExistingCreateTarget refuses it (bd run there refuses it as a legacy
// Dolt workspace, or the open would create an embedded database in a server
// workspace), with nothing written. Otherwise the
// metadata.json written here decides what bd opens in the target from then on
// (it outranks the target's config.yaml), so it must name what the .beads
// already holds rather than hide it:
//
//   - a .beads refuseUninitializableTarget refuses (one whose own config selects
//     Dolt server mode, one bd's own admission refuses, a fresh clone of a
//     SQLite-era project, or of a project from before bd wrote config.json,
//     bd run there finds no workspace in, one holding several databases) is
//     refused, with nothing written;
//   - one that holds exactly one embedded database keeps it
//     (adoptTargetDatabase), the database fork setup creates among them,
//     unless its working set already holds changes the create's commit would
//     publish (refuseUncommittedTargetChanges);
//   - any other is given a new database with the same prefix as the source
//     store (T010, T012: prefix inheritance).
//
// bd migrate-personal applies the same rule to the planning repo
// (refuseUninitializablePlanningRepo, initializePlanningRepo).
//
// When allowCreate is false, a target with no existing workspace is refused
// instead of fabricated — see isAmbiguousRepoTarget.
func ensureBeadsDirForPath(ctx context.Context, targetPath string, sourceStore storage.DoltStorage, allowCreate bool) error {
	beadsDir := filepath.Join(targetPath, ".beads")
	metadataPath := filepath.Join(beadsDir, "metadata.json")

	// A target in a format this build cannot open, or a historical SQLite
	// workspace, is refused before anything below: one marked only by a
	// legacy config.json or by the graph format marker file has no
	// metadata.json, so it would otherwise be initialized here, with an Issue
	// database and a metadata.json written beside it, and one whose
	// metadata.json names its SQLite database would be opened by the create,
	// which creates an empty embedded database beside it.
	if err := checkDiscoveredForOpen(beadsDir); err != nil {
		return err
	}

	// Check if beads directory already exists with a Dolt database.
	// metadata.json is the canonical marker for an initialized beads dir.
	// A legacy Dolt workspace has one too, and the open that follows would
	// create an embedded database beside its Dolt root, which hides that
	// data and silences admission there: refuse it as admission does
	// (refuseExistingCreateTarget).
	if _, err := os.Stat(metadataPath); err == nil {
		return refuseExistingCreateTarget(beadsDir)
	}

	if !allowCreate {
		return fmt.Errorf("no beads workspace found at %s and --repo's value is a relative/bare path, so it won't be auto-created here (this is likely not the target you intended). Pass an absolute or \"~/\"-prefixed --repo path to an existing workspace instead", remedy.PrintedValue(targetPath))
	}

	if err := refuseUninitializableTarget(beadsDir); err != nil {
		return err
	}
	if databases := embeddeddolt.Databases(beadsDir); len(databases) == 1 {
		return adoptTargetDatabase(ctx, beadsDir, databases[0], sourceStore, func(target storage.DoltStorage) error {
			return refuseUncommittedTargetChanges(ctx, target, beadsDir, databases[0])
		})
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
			return saveEmbeddedTargetMetadata(beadsDir, dbName, configfile.GenerateProjectID())
		}
	}

	return nil
}

// refuseUninitializableTarget refuses a .beads with no metadata.json that
// ensureBeadsDirForPath must not initialize, because the metadata.json it
// writes would hide what the .beads already holds from bd run in that
// repository. It writes nothing. Refused are:
//
//   - one whose own config.yaml (or the config.local.yaml beside it) selects
//     Dolt server mode, by dolt.mode: server (the shape gc provisions) or, with
//     no dolt.mode, by a non-local dolt.host: bd there opens its server
//     database, and a metadata.json pinning dolt_mode "embedded" outranks
//     that file. A config it cannot read is refused too, since it may say
//     the same; a null left by commenting a key out reads as unset, as bd
//     reads it there;
//   - one bd's own command admission refuses there (admitForeignWorkspace),
//     such as a SQLite-era repo or a legacy Dolt root: a fresh database
//     beside it would hide its data and silence that refusal;
//   - one whose legacy config.json does not load (malformed, not a JSON
//     object, or unreadable), with admission's "loading config: ..." error:
//     bd run there refuses it, and the metadata.json written here would
//     replace it as the workspace's pointer;
//   - one holding a legacy config.json beside nothing discovery treats as a
//     workspace (beads.HasProjectFiles), such as a fresh clone of a SQLite-era
//     project, with the "no beads database found" error a foreign open of it
//     returns (openForeignStore): bd run there finds no workspace and writes
//     nothing, and the metadata.json written here would make it one that
//     hides its config.json and issues.jsonl behind an empty database;
//   - one holding a non-empty issues.jsonl beside nothing discovery treats
//     as a workspace and no redirect, such as a fresh clone of a project from
//     before bd wrote config.json, with the same error, naming
//     bd init --from-jsonl: bd run there finds no workspace, and the issue
//     written into the new database would keep bd's upgrade auto-import from
//     ever loading issues.jsonl;
//   - one holding several embedded databases: nothing records which one
//     holds the issues (bd run there opens the default database).
//
// A .beads holding one embedded database is left to adoptTargetDatabase. A
// .beads in a format this build cannot open is refused as that first, as bd's
// admission refuses it (checkDiscoveredForOpen). Its own refusals are
// targetRefusals; bd's admission refusals are returned as that admission
// returns them, and the no-workspace one wraps ErrNoBeadsDatabase, as
// openForeignStore's does.
func refuseUninitializableTarget(beadsDir string) error {
	// A workspace in a format this build cannot open is refused as that,
	// before its config is read: the format fence's refusal (and its exit
	// code) does not depend on the mode the config selects.
	if err := checkDiscoveredForOpen(beadsDir); err != nil {
		return err
	}
	// repo is the target as its refusals name it (remedy.PrintedValue), so a
	// path holding a control character cannot redraw the bd init remedy
	// printed after it. A config read error names the config path, so it is
	// spelled the same way.
	repo := remedy.PrintedValue(filepath.Dir(beadsDir))
	mode, _, err := config.WorkspaceYamlSettingWithLocal(beadsDir, "dolt.mode")
	if err != nil {
		return refuseTarget("%s has no metadata.json and its dolt.mode cannot be read (%s); not initializing it, since it may select Dolt server mode", repo, remedy.PrintedValue(err.Error()))
	}
	// host is the target's own dolt.host when bd does not run its server
	// there (configfile.BdRunsServerAt): another machine, the IPv6 loopback
	// or another loopback address. bd init does not read it from config.yaml
	// (it takes the host from --server-host or BEADS_DOLT_SERVER_HOST and
	// otherwise dials 127.0.0.1, where it starts bd's own server), so a
	// remedy for such a target must pass it.
	host, _, hostErr := config.WorkspaceYamlSettingWithLocal(beadsDir, "dolt.host")
	if hostErr != nil || configfile.BdRunsServerAt(host) {
		host = ""
	}
	if strings.EqualFold(mode, configfile.DoltModeServer) {
		initCmd, hostNote := serverInitRemedy(runtime.GOOS, "bd init", beadsDir, host)
		return refuseTarget("%s has no metadata.json and its own config selects Dolt server mode (dolt.mode: server); not initializing it as an embedded workspace, which would hide its server data. Run: %s there%s, naming the server database it uses (%q unless BEADS_DOLT_SERVER_DATABASE names another), then rerun", repo, initCmd, hostNote, configfile.DefaultDoltDatabase)
	}
	if mode == "" {
		// With no dolt.mode, a non-local dolt.host selects server mode there
		// too (HostImpliesServerMode's config.yaml tier), and the dolt_mode the
		// metadata.json below pins switches that inference off.
		if hostErr != nil {
			return refuseTarget("%s has no metadata.json and its dolt.host cannot be read (%s); not initializing it, since it may select Dolt server mode", repo, remedy.PrintedValue(hostErr.Error()))
		}
		// Mode inference asks whether the host is this machine
		// (configfile.IsLocalHostString), not whether bd runs its server
		// there: the IPv6 loopback selects nothing here.
		if host != "" && !configfile.IsLocalHostString(host) {
			// The host is named escaped (remedy.EscapedValue), as hostNote
			// names one the remedy leaves as a placeholder.
			initCmd, hostNote := serverInitRemedy(runtime.GOOS, "bd init --server", beadsDir, host)
			return refuseTarget("%s has no metadata.json and its own config selects Dolt server mode (dolt.host: %s); not initializing it as an embedded workspace, which would hide its server data. Run: %s there%s, naming the server database it uses (%q unless BEADS_DOLT_SERVER_DATABASE names another), then rerun", repo, remedy.EscapedValue(host), initCmd, hostNote, configfile.DefaultDoltDatabase)
		}
	}
	// Metadata that does not load (a malformed, non-object or unreadable
	// legacy config.json) is refused as admission refuses it ("loading
	// config: ..."). admitForeignWorkspace leaves it to the caller's own
	// Load, which fails without writing; the caller here writes a
	// metadata.json and a database before any Load runs.
	if _, err := loadAndCheckWorkspaceFormat(beadsDir); err != nil {
		return err
	}
	if _, err := admitForeignWorkspace(beadsDir); err != nil {
		return err
	}
	// A legacy config.json beside nothing discovery treats as a workspace
	// (beads.HasProjectFiles) is most often a fresh clone of a SQLite-era
	// project, which keeps its tracked config.json and issues.jsonl but not
	// the ignored *.db. bd run there finds no workspace and writes nothing,
	// and the historical SQLite check above needs the database to refuse it.
	// The metadata.json and database written here would make it a workspace
	// that hides those files behind an empty database, so it is refused with
	// the error a foreign open of it returns (openForeignStore), followed by
	// the reason.
	if _, err := os.Lstat(filepath.Join(beadsDir, configfile.LegacyConfigFileName)); err == nil && !beads.HasProjectFiles(beadsDir) {
		return fmt.Errorf("%w at %s: it has a legacy config.json but no database (a fresh clone of a SQLite-era project, say), so bd run there finds no workspace; not initializing it, which would hide what it holds behind an empty database", ErrNoBeadsDatabase, remedy.PrintedValue(beadsDir))
	}
	// So is an issues.jsonl beside nothing discovery treats as a workspace,
	// most often a fresh clone of a project from before bd wrote config.json,
	// which tracked only issues.jsonl and ignored its *.db. bd run there finds
	// no workspace, and once bd init gives it a database, bd's upgrade
	// auto-import (maybeAutoImportJSONL) loads issues.jsonl into it, but only
	// while it is empty: the issue written here would keep it from ever
	// loading. The file is the default import path, since the target has no
	// config.yaml of its own to name another. bd init --from-jsonl in the
	// repository imports it. An empty issues.jsonl holds nothing to hide (the
	// size check is a cheap stand-in for "holds issues"), and a .beads that
	// redirects elsewhere is not the one bd run there reads.
	if info, err := os.Stat(filepath.Join(beadsDir, defaultImportJSONLPath)); err == nil && info.Mode().IsRegular() && info.Size() > 0 &&
		!beads.HasProjectFiles(beadsDir) && beads.FollowRedirect(beadsDir) == beadsDir {
		return fmt.Errorf("%w at %s: it has %s but no database (a fresh clone of a project from before bd wrote config.json, say), so bd run there does not treat it as a workspace; not initializing it, which would hide what it holds behind an empty database. Run `bd init --from-jsonl` in %s, which imports it, then rerun", ErrNoBeadsDatabase, remedy.PrintedValue(beadsDir), defaultImportJSONLPath, repo)
	}
	if databases := embeddeddolt.Databases(beadsDir); len(databases) > 1 {
		return refuseTarget("%s has no metadata.json and holds several databases (%s); give it a metadata.json whose dolt_database names the one to use, then rerun", repo, remedy.PrintedList(databases, ", "))
	}
	return nil
}

// refuseExistingCreateTarget refuses a create target .beads that already has
// a metadata.json, before the writable open that follows
// (newDoltStoreFromConfig, which would create an embedded database there, or
// migrate-personal's routed open, which would open one bd run there does not
// read, or fail for the one it lacks). It writes nothing. Refused are:
//
//   - one bd run inside it refuses as a legacy Dolt workspace, or in a format
//     this build cannot open, or as a historical SQLite workspace
//     (admitForeignWorkspace), with that refusal: an embedded database beside
//     a legacy Dolt root hides that data and turns the refusal bd prints there
//     into an empty workspace;
//   - one whose own config selects Dolt server mode, where its metadata names
//     no dolt_mode, when this process would open it embedded
//     (existingTargetRefusal): store selection takes dolt.mode from the
//     launching workspace's config, so the open would use an embedded
//     database beside the server data bd run there reads.
//
// Metadata naming a removed or unrecognized backend, and metadata that does
// not load, are left to the open, which refuses them in its own words, as it
// does on a target that has no other problem.
func refuseExistingCreateTarget(beadsDir string) error {
	cfg, err := admitForeignWorkspace(beadsDir)
	if err != nil {
		if isLegacyUpgradeRefusal(err) || isUnsupportedWorkspaceFormat(err) {
			return err
		}
		return nil
	}
	if cfg == nil {
		return nil
	}
	if targetSelectsServerMode(beadsDir, cfg) && selectsEmbeddedStore(beadsDir, cfg) {
		const reason = "its own config selects Dolt server mode, but its metadata.json names no dolt_mode, so bd here would open it as an embedded workspace beside the server data bd there reads. "
		return &existingTargetRefusal{
			msg:         reason + existingTargetServerRemedy(beadsDir, cfg, true),
			planningMsg: reason + existingTargetServerRemedy(beadsDir, cfg, false),
		}
	}
	return nil
}

// existingTargetServerRemedy is the way out refuseExistingCreateTarget gives
// for a target, beadsDir, with metadata cfg, whose own config selects Dolt
// server mode: the metadata.json edit, then "then rerun; nothing was
// created". An open from another workspace reads the target's metadata.json
// but the launching workspace's config.yaml, so it does not see the
// target's own config.yaml dolt.host or dolt.port. When the host the
// target itself dials (cfg.GetDoltServerHost under the target's own
// configuration, config.WithBeadsDir: its metadata.json dolt_server_host
// over its dolt.host, where config.local.yaml beside config.yaml outranks
// it) is one bd does not run its server at (configfile.BdRunsServerAt), the
// edit names that host, escaped (remedy.EscapedValue) since it is the
// target repository's content, and the port bd run there dials, or asks
// for the port its server listens on when nothing names one. Setting only
// dolt_mode would dial another server, or auto-start a server of bd's own
// inside the target.
//
// Both opens, from here and from bd run there, take the target's port as
// doltserver's port chain ranks it: its .beads/dolt-server.port, which bd
// writes for the server it reaches in the target (doltserver.ReadPortFile;
// read only at a host bd runs its server at), then the listener.port of
// the Dolt server config.yaml in its data directory
// (doltserver.DoltConfigYamlPort), then the dolt.port each reads, then its
// metadata.json dolt_server_port. Only that dolt.port differs: the open
// from here reads this process's (existingTargetLaunchPort), bd there the
// one the target's own config.yaml or config.local.yaml names, and each
// reads the user-level config.yaml's where its own names none. With no
// port at all, at a host bd runs its server at, each starts a server of
// bd's own for the target. The ports are read directly: resolving the
// chain would print dolt_server_port's deprecation warning above a remedy
// that names that field.
//
// The rerun is offered only where it dials the port bd there dials
// (firstPort over the two chains), since once dolt_mode is set nothing
// refuses it. Elsewhere bd create's remedy is the edit and the write inside
// the target. writeInside is whether that write is a way out for the
// caller: bd create can be run inside the target, bd migrate-personal
// (which moves issues out of the launching workspace into it) cannot, so
// its remedy says this write cannot be aimed at the target's server from
// here.
//
// At a host bd runs its server at, bd create's remedy is that write for
// every target whose own config names dolt.port and that has no
// listener.port: of the target's files only its port file ranks above
// that dolt.port, and bd removes it once it finds that server stopped,
// after which an open from here may dial another port. Metadata's
// dolt_server_port is not offered there: it would declare a server bd runs
// for the target externally managed for its own commands. At another host,
// where neither open reads the port file, the edit's dolt_server_port is
// the port bd there dials, which the dolt.port bd here reads outranks; with
// a listener.port, which both opens dial, the edit names no port.
func existingTargetServerRemedy(beadsDir string, cfg *configfile.Config, writeInside bool) string {
	const modeOnly = "Set \"dolt_mode\": \"server\" in its .beads/metadata.json, then rerun; nothing was created"
	const modeThenInside = "Set \"dolt_mode\": \"server\" in its .beads/metadata.json, then make the write inside that repository, where bd dials the port its own config names, which bd here does not read; nothing was created"
	// A config file that does not load fails the same way for the target's
	// own commands, which run with what did load.
	var host string
	var targetPort int
	_ = config.WithBeadsDir(beadsDir, func() {
		host = cfg.GetDoltServerHost()
		targetPort = configDoltPort()
	})
	launchPort, launchConfig := existingTargetLaunchPort()
	ownPort := workspaceDoltPort(beadsDir)
	listenerPort := doltserver.DoltConfigYamlPort(beadsDir)
	if configfile.BdRunsServerAt(host) {
		portFile := doltserver.ReadPortFile(beadsDir)
		sameServer := firstPort(portFile, listenerPort, launchPort, cfg.DoltServerPort) == firstPort(portFile, listenerPort, targetPort, cfg.DoltServerPort)
		switch {
		case ownPort > 0 && listenerPort == 0 && writeInside:
			return modeThenInside
		case sameServer:
			return modeOnly
		case ownPort > 0:
			return fmt.Sprintf("Its own config names dolt.port %d, which bd here does not read, and it has no .beads/dolt-server.port, which bd here would dial first, so even once its .beads/metadata.json sets \"dolt_mode\": \"server\", bd migrate-personal cannot aim this write at that port, and a \"dolt_server_port\" there would declare the server bd runs for it externally managed; nothing was created", ownPort)
		case writeInside:
			return fmt.Sprintf("Set \"dolt_mode\": \"server\" in its .beads/metadata.json, then make the write inside that repository, where bd dials the server its own config names: it has no .beads/dolt-server.port, which bd here would dial first, so bd here dials dolt.port %d, which %s names; nothing was created", launchPort, launchConfig)
		}
		return fmt.Sprintf("%s names dolt.port %d, which bd here dials for it once its .beads/metadata.json sets \"dolt_mode\": \"server\", since it has no .beads/dolt-server.port, which bd here would dial first, so bd migrate-personal cannot aim this write at its server unless that server listens on that port; nothing was created", capitalize(launchConfig), launchPort)
	}
	inside := ""
	if writeInside {
		inside = ", or make the write inside that repository, where bd dials the server its own config names"
	}
	if listenerPort > 0 {
		return fmt.Sprintf("Set \"dolt_mode\": \"server\" and \"dolt_server_host\": %s in its .beads/metadata.json, then rerun (bd here and bd there both dial port %d, which the listener.port of the Dolt server config.yaml in its data directory names)%s; nothing was created", remedy.EscapedValue(host), listenerPort, inside)
	}
	port := firstPort(targetPort, cfg.DoltServerPort)
	ok := port > 0
	if launchPort > 0 && port != launchPort {
		switch {
		case writeInside && !ok:
			return fmt.Sprintf("Set \"dolt_mode\": \"server\", \"dolt_server_host\": %s and \"dolt_server_port\" (the port its server listens on) in its .beads/metadata.json, then make the write inside that repository: %s names dolt.port %d, which outranks that port when bd here opens it; nothing was created", remedy.EscapedValue(host), launchConfig, launchPort)
		case writeInside:
			return fmt.Sprintf("Set \"dolt_mode\": \"server\", \"dolt_server_host\": %s and \"dolt_server_port\": %d in its .beads/metadata.json, then make the write inside that repository, where bd dials the server its own config names: %s names dolt.port %d, which outranks that port when bd here opens it; nothing was created", remedy.EscapedValue(host), port, launchConfig, launchPort)
		case !ok:
			return fmt.Sprintf("%s names dolt.port %d, which outranks any \"dolt_server_port\" in its .beads/metadata.json when bd here opens it, so bd migrate-personal cannot aim this write at its server at %s unless that server listens on that port; nothing was created", capitalize(launchConfig), launchPort, remedy.EscapedValue(host))
		}
		return fmt.Sprintf("%s names dolt.port %d, which outranks any \"dolt_server_port\" in its .beads/metadata.json when bd here opens it, so bd migrate-personal cannot aim this write at port %d, the port bd there dials for its server at %s; nothing was created", capitalize(launchConfig), launchPort, port, remedy.EscapedValue(host))
	}
	if !ok {
		// Nothing names it a port: bd there refuses the unresolved port
		// too, so no write inside it is offered, and none is made up.
		return fmt.Sprintf("Set \"dolt_mode\": \"server\", \"dolt_server_host\": %s and \"dolt_server_port\" (the port its server listens on) in its .beads/metadata.json, then rerun; nothing was created", remedy.EscapedValue(host))
	}
	return fmt.Sprintf("Set \"dolt_mode\": \"server\", \"dolt_server_host\": %s and \"dolt_server_port\": %d in its .beads/metadata.json, then rerun (a dolt.port in this workspace's own configuration still outranks that port)%s; nothing was created", remedy.EscapedValue(host), port, inside)
}

// workspaceDoltPort is the dolt.port a workspace, beadsDir, names in its
// own config.yaml or config.local.yaml (which outranks it), which an open
// from another workspace does not read; 0 when it names none.
func workspaceDoltPort(beadsDir string) int {
	value, _, err := config.WorkspaceYamlSettingWithLocal(beadsDir, "dolt.port")
	p, convErr := strconv.Atoi(value)
	if err != nil || convErr != nil || p <= 0 {
		return 0
	}
	return p
}

// configDoltPort is the dolt.port the loaded configuration names
// (config.GetYamlConfig: BD_DOLT_PORT, then the workspace's own
// config.local.yaml and config.yaml, then the user-level config.yaml
// files), as doltserver's port chain reads it; 0 for none.
func configDoltPort() int {
	p, err := strconv.Atoi(config.GetYamlConfig("dolt.port"))
	if err != nil || p <= 0 {
		return 0
	}
	return p
}

// existingTargetLaunchPort is the dolt.port this process reads
// (configDoltPort), 0 for none, and where, the configuration a remedy
// names for it: the user-level config.yaml where that names the port
// (config.GetUserYamlConfig), which bd run in the target reads too where
// its own config names none, else this workspace's own configuration. An
// open of a target from here dials it over the target's metadata.json
// dolt_server_port, as doltserver's port chain ranks them, and never reads
// the dolt.port the target's own config names. A BD_DOLT_PORT, which
// outranks both files, bd run in the target reads too, so it never decides
// a refusal.
func existingTargetLaunchPort() (port int, where string) {
	port = configDoltPort()
	if port == 0 {
		return 0, ""
	}
	if user, err := strconv.Atoi(config.GetUserYamlConfig("dolt.port")); err == nil && user == port {
		return port, "the user-level config.yaml"
	}
	return port, "this workspace's own configuration"
}

// firstPort is the first of ports that names one, as a port chain picks
// it, or 0 when none does.
func firstPort(ports ...int) int {
	for _, p := range ports {
		if p > 0 {
			return p
		}
	}
	return 0
}

// existingTargetRefusal is refuseExistingCreateTarget's own refusal of a
// target that has a metadata.json. Its messages name no path: bd create
// renders msg as "target repo X: ..." (handleRepoTargetInitError) and bd
// migrate-personal planningMsg as "planning repo X: ...". They differ only
// in a remedy that makes the write inside the target, which bd
// migrate-personal cannot.
type existingTargetRefusal struct{ msg, planningMsg string }

func (r *existingTargetRefusal) Error() string { return r.msg }

// targetRefusal is refuseUninitializableTarget's own refusal of a target: a
// shape it must not initialize, in a message that starts with the target's
// path and names the remedy. bd migrate-personal raises it as a refusal of
// the planning repo (refuseUninitializablePlanningRepo).
type targetRefusal struct{ msg string }

func (r *targetRefusal) Error() string { return r.msg }

func refuseTarget(format string, args ...any) error {
	return &targetRefusal{msg: fmt.Sprintf(format, args...)}
}

// serverInitRemedy completes the bd init command, initCmd ("bd init" or
// "bd init --server"), that refuseUninitializableTarget tells the user to run
// in a target, beadsDir, whose own config selects Dolt server mode. host is
// the target's non-local dolt.host, or "" for none. bd init does not read
// dolt.host from config.yaml, so a remote host is passed with --server-host,
// quoted for the shell the command is pasted into on goos (remedy.ShellQuotedFor),
// since it is the target repository's content. A host no quoting there passes
// on unchanged is left as a <host> placeholder, and hostNote, which the
// refusal prints after the command, names it escaped (remedy.EscapedValue), so
// pasting that name in its place runs nothing either. Its port is passed
// with it (remoteServerPort): bd init takes a .beads/dolt-server.port over
// any configured port, and that file is a local server's bookkeeping, which
// bd itself discards for a remote host (doltserver.DefaultConfig), so the
// remote host would be dialed on a stale local port.
func serverInitRemedy(goos, initCmd, beadsDir, host string) (command, hostNote string) {
	if host != "" {
		quoted, ok := remedy.ShellQuotedFor(goos, host)
		if !ok {
			quoted, hostNote = "<host>", fmt.Sprintf(" with its dolt.host %s for <host>", remedy.EscapedValue(host))
		}
		initCmd += fmt.Sprintf(" --server-host %s --server-port %d", quoted, remoteServerPort(beadsDir))
	}
	return initCmd + " --database <name>", hostNote
}

// remoteServerPort is the port of the remote server a target, beadsDir, names
// by its dolt.host: its own dolt.port (config.local.yaml beside config.yaml
// outranks it), else configfile.DefaultDoltServerPort, the port bd dials for a
// remote host that has none configured.
func remoteServerPort(beadsDir string) int {
	value, _, err := config.WorkspaceYamlSettingWithLocal(beadsDir, "dolt.port")
	if port, convErr := strconv.Atoi(value); err == nil && convErr == nil && port > 0 {
		return port
	}
	return configfile.DefaultDoltServerPort
}

// adoptTargetDatabase gives a .beads with no metadata.json that holds one
// embedded database, database, the metadata.json that names it, instead of
// creating a database named after the source prefix beside it, which would
// hide whatever it holds. It is the database fork setup creates
// (autoConfigureForkContributor), and the one an auto-routed open uses until
// a metadata.json names another (autoRouteTargetDatabase). The database keeps
// its issue_prefix, or takes sourceStore's when it has none, and its project
// identity (adoptedDatabaseSettings). check, when set, inspects the opened
// database before anything is written and refuses the adoption by returning
// an error.
func adoptTargetDatabase(ctx context.Context, beadsDir, database string, sourceStore storage.DoltStorage, check func(storage.DoltStorage) error) error {
	target, err := newDoltStore(ctx, &dolt.Config{BeadsDir: beadsDir, Database: database})
	if err != nil {
		// The open error is bd's own and can span lines: the printer spells
		// what in it a terminal acts on, and keeps its lines.
		return fmt.Errorf("failed to open %s's database %s: %w", remedy.PrintedValue(filepath.Dir(beadsDir)), remedy.EscapedValue(database), remedy.PrintedTextError(err))
	}
	if check != nil {
		err = check(target)
	}
	var projectID string
	if err == nil {
		projectID, err = adoptedDatabaseSettings(ctx, target, sourceStore)
	}
	if closeErr := target.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return saveEmbeddedTargetMetadata(beadsDir, database, projectID)
}

// refuseUncommittedTargetChanges is the check adoptTargetDatabase runs for a
// routed bd create (ensureBeadsDirForPath): it refuses the adopted database,
// database, when its working set already holds changes the create's commit
// would publish under the create's message. With dolt.auto-commit on, the
// create commits there: its write's own commit stages every table the write
// touched (issueOpsContext), and from an embedded workspace the create then
// commits the whole working set (DOLT_COMMIT('-Am'), commitPendingIfEmbedded).
// bd migrate-personal refuses the same database for the same reason
// (refuseUncommittedPlanningChanges). It writes nothing.
func refuseUncommittedTargetChanges(ctx context.Context, target storage.DoltStorage, beadsDir, database string) error {
	if commits, err := writesCommitNow(); err != nil || !commits {
		return err
	}
	// The target, its database and its tables are named as
	// refuseUninitializableTarget names the target (remedy.PrintedValue).
	repo := remedy.PrintedValue(filepath.Dir(beadsDir))
	tables, err := uncommittedTables(ctx, target)
	if err != nil {
		return fmt.Errorf("failed to read the uncommitted changes of %s's database %s: %w", repo, remedy.EscapedValue(database), err)
	}
	if len(tables) == 0 {
		return nil
	}
	return fmt.Errorf("%s has no metadata.json and its database %s has uncommitted changes (%s), which bd create's commit would publish; commit them there with `bd dolt commit`, then rerun; nothing was created", repo, remedy.EscapedValue(database), remedy.PrintedList(tables, ", "))
}

// adoptedDatabaseSettings gives a database adoptTargetDatabase adopts
// sourceStore's issue_prefix when it has none and a source store is given, as
// ensureBeadsDirForPath gives the database it creates (a routed bd create into
// it needs one), and returns the project_id its metadata.json is to carry: the
// database's own _project_id when it has one, which bd's workspace identity
// check compares that field with (validateWorkspaceIdentity), else a new one.
func adoptedDatabaseSettings(ctx context.Context, target, sourceStore storage.DoltStorage) (string, error) {
	prefix, err := target.GetConfig(ctx, "issue_prefix")
	if err != nil {
		return "", fmt.Errorf("failed to read the target database's issue_prefix: %w", err)
	}
	if prefix == "" && sourceStore != nil {
		if sourcePrefix, err := sourceStore.GetConfig(ctx, "issue_prefix"); err == nil && sourcePrefix != "" {
			if err := target.SetConfig(ctx, "issue_prefix", sourcePrefix); err != nil {
				return "", fmt.Errorf("failed to set prefix in the target database: %w", err)
			}
		}
	}
	projectID, err := target.GetMetadata(ctx, "_project_id")
	if err != nil {
		return "", fmt.Errorf("failed to read the target database's project identity: %w", err)
	}
	if projectID == "" {
		projectID = configfile.GenerateProjectID()
	}
	return projectID, nil
}

// saveEmbeddedTargetMetadata writes the metadata.json ensureBeadsDirForPath
// gives a target: an embedded Dolt workspace using database under projectID.
func saveEmbeddedTargetMetadata(beadsDir, database, projectID string) error {
	cfg := configfile.DefaultConfig()
	cfg.Backend = configfile.BackendDolt
	cfg.DoltDatabase = database
	cfg.DoltMode = configfile.DoltModeEmbedded
	cfg.ProjectID = projectID
	if err := cfg.Save(beadsDir); err != nil {
		return fmt.Errorf("failed to write metadata.json: %w", err)
	}
	return nil
}
