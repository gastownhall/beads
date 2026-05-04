package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/linear"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// linearCmd is the root command for Linear integration.
var linearCmd = &cobra.Command{
	Use:     "linear",
	GroupID: "advanced",
	Short:   "Linear integration commands",
	Long: `Synchronize issues between beads and Linear.

Configuration:
  bd config set linear.api_key "YOUR_API_KEY"
  bd config set linear.team_id "TEAM_ID"
  bd config set linear.team_ids "TEAM_ID1,TEAM_ID2"  # Multiple teams (comma-separated)
  bd config set linear.project_id "PROJECT_ID"  # Optional: sync only this project

Environment variables (alternative to config):
  LINEAR_API_KEY  - Linear API key (for individual developers)
  LINEAR_TEAM_ID  - Linear team ID (UUID, singular)
  LINEAR_TEAM_IDS - Linear team IDs (comma-separated UUIDs)

OAuth (for CI workers / automated sync):
  LINEAR_OAUTH_CLIENT_ID     - OAuth app client ID
  LINEAR_OAUTH_CLIENT_SECRET - OAuth app client secret

  When both OAuth env vars are set, OAuth client_credentials flow is used
  instead of the API key. This allows CI workers to authenticate as an
  application (actor=application) rather than impersonating a user.
  Precedence: OAuth > LINEAR_API_KEY > config file.

Data Mapping (optional, sensible defaults provided):
  Priority mapping (Linear 0-4 to Beads 0-4):
    bd config set linear.priority_map.0 4    # No priority -> Backlog
    bd config set linear.priority_map.1 0    # Urgent -> Critical
    bd config set linear.priority_map.2 1    # High -> High
    bd config set linear.priority_map.3 2    # Medium -> Medium
    bd config set linear.priority_map.4 3    # Low -> Low

  State mapping (Linear state type to Beads status):
    bd config set linear.state_map.backlog open
    bd config set linear.state_map.unstarted open
    bd config set linear.state_map.started in_progress
    bd config set linear.state_map.completed closed
    bd config set linear.state_map.canceled closed
    bd config set linear.state_map.my_custom_state in_progress  # Custom state names

  Label to issue type mapping:
    bd config set linear.label_type_map.bug bug
    bd config set linear.label_type_map.feature feature
    bd config set linear.label_type_map.epic epic

  Relation type mapping (Linear relations to Beads dependencies):
    bd config set linear.relation_map.blocks blocks
    bd config set linear.relation_map.blockedBy blocks
    bd config set linear.relation_map.duplicate duplicates
    bd config set linear.relation_map.related related

  ID generation (optional, hash IDs to match bd/Jira hash mode):
    bd config set linear.id_mode "hash"      # hash (default)
    bd config set linear.hash_length "6"     # hash length 3-8 (default: 6)

Examples:
  bd linear sync --pull         # Import issues from Linear
  bd linear sync --push         # Export issues to Linear
  bd linear sync                # Bidirectional sync (pull then push)
  bd linear sync --dry-run      # Preview sync without changes
  bd create "Fix login" --external-ref https://linear.app/team/issue/TEAM-123
                              # Link a local issue to an existing Linear issue
  bd linear status              # Show sync status`,
}

// linearSyncCmd handles synchronization with Linear.
var linearSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Synchronize issues with Linear",
	Long: `Synchronize issues between beads and Linear.

Modes:
  --pull              Import issues from Linear into beads
  --push              Export issues from beads to Linear
  --pull-if-stale     Pull only if data is stale (skip if fresh)
  (no flags)          Bidirectional sync: pull then push, with conflict resolution

Staleness (--pull-if-stale):
  --threshold 20m     How old data must be before pulling (default 20m)
  A 5-minute debounce prevents agent loops: if a pull completed within 5 minutes,
  data is always treated as fresh regardless of the threshold.

Team Selection:
  --team ID1,ID2  Override configured team IDs for this sync
  Multiple teams can be configured via linear.team_ids (comma-separated).
  Falls back to linear.team_id for backward compatibility.
  Push requires explicit --team when multiple teams are configured.

Pull Options:
  --milestones       Reconstruct Linear project milestones as local epic parents

Type Filtering (--push only):
  --type task,feature       Only sync issues of these types
  --exclude-type wisp       Exclude issues of these types
  --include-ephemeral       Include ephemeral issues (wisps, etc.); default is to exclude
  --parent TICKET           Only push this ticket and its descendants
  --relations               Import Linear relations as bd dependencies on pull

Conflict Resolution:
  By default, newer timestamp wins. Override with:
  --prefer-local    Always prefer local beads version
  --prefer-linear   Always prefer Linear version

Examples:
  bd linear sync --pull                         # Import from Linear
  bd linear sync --pull-if-stale                # Pull only if data is stale
  bd linear sync --pull-if-stale --threshold 5m # Pull if older than 5 minutes
  bd linear sync --pull --relations             # Import Linear blocking relations as bd deps
  bd linear sync --push --create-only           # Push new issues only
  bd linear sync --push --type=task,feature     # Push only tasks and features
  bd linear sync --push --exclude-type=wisp     # Push all except wisps
  bd linear sync --push --parent=bd-abc123      # Push one ticket tree
  bd linear sync --dry-run                      # Preview without changes
  bd linear sync --prefer-local                 # Bidirectional, local wins`,
	Run: runLinearSync,
}

// linearStatusCmd shows the current sync status.
var linearStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show Linear sync status",
	Long: `Show the current Linear sync status, including:
  - Last sync timestamp
  - Configuration status
  - Number of issues with Linear links
  - Issues pending push (no external_ref)`,
	Run: runLinearStatus,
}

// linearTeamsCmd lists available teams.
var linearTeamsCmd = &cobra.Command{
	Use:   "teams",
	Short: "List available Linear teams",
	Long: `List all teams accessible with your Linear API key.

Use this to find the team ID (UUID) needed for configuration.

Example:
  bd linear teams
  bd config set linear.team_id "12345678-1234-1234-1234-123456789abc"`,
	Run: runLinearTeams,
}

func init() {
	linearSyncCmd.Flags().Bool("pull", false, "Pull issues from Linear")
	linearSyncCmd.Flags().Bool("push", false, "Push issues to Linear")
	linearSyncCmd.Flags().Bool("dry-run", false, "Preview sync without making changes")
	linearSyncCmd.Flags().Bool("prefer-local", false, "Prefer local version on conflicts")
	linearSyncCmd.Flags().Bool("prefer-linear", false, "Prefer Linear version on conflicts")
	linearSyncCmd.Flags().Bool("create-only", false, "Only create new issues, don't update existing")
	linearSyncCmd.Flags().Bool("update-refs", true, "Update external_ref after creating Linear issues")
	linearSyncCmd.Flags().Bool("milestones", false, "Reconstruct Linear project milestones as local epic parents when pulling")
	linearSyncCmd.Flags().String("state", "all", "Issue state to sync: open, closed, all")
	linearSyncCmd.Flags().StringSlice("type", nil, "Only sync issues of these types (can be repeated)")
	linearSyncCmd.Flags().StringSlice("exclude-type", nil, "Exclude issues of these types (can be repeated)")
	linearSyncCmd.Flags().Bool("include-ephemeral", false, "Include ephemeral issues (wisps, etc.) when pushing to Linear")
	linearSyncCmd.Flags().String("parent", "", "Limit push to this beads ticket and its descendants")
	linearSyncCmd.Flags().StringSlice("team", nil, "Team ID(s) to sync (overrides configured team_id/team_ids)")
	linearSyncCmd.Flags().Bool("relations", false, "Import Linear relations as bd dependencies when pulling")
	linearSyncCmd.Flags().Bool("pull-if-stale", false, "Pull only if Linear data is stale (skip if fresh)")
	linearSyncCmd.Flags().Duration("threshold", linear.DefaultStaleThreshold, "Staleness threshold for --pull-if-stale (default 20m)")
	linearSyncCmd.Flags().Bool("no-wait", false, "Fail immediately if another sync is running instead of waiting")
	registerSelectiveSyncFlags(linearSyncCmd)

	linearCmd.AddCommand(linearSyncCmd)
	linearCmd.AddCommand(linearStatusCmd)
	linearCmd.AddCommand(linearTeamsCmd)
	rootCmd.AddCommand(linearCmd)
}

func runLinearSync(cmd *cobra.Command, args []string) {
	pull, _ := cmd.Flags().GetBool("pull")
	push, _ := cmd.Flags().GetBool("push")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	preferLocal, _ := cmd.Flags().GetBool("prefer-local")
	preferLinear, _ := cmd.Flags().GetBool("prefer-linear")
	createOnly, _ := cmd.Flags().GetBool("create-only")
	milestones, _ := cmd.Flags().GetBool("milestones")
	state, _ := cmd.Flags().GetString("state")
	typeFilters, _ := cmd.Flags().GetStringSlice("type")
	excludeTypes, _ := cmd.Flags().GetStringSlice("exclude-type")
	includeEphemeral, _ := cmd.Flags().GetBool("include-ephemeral")
	cliTeams, _ := cmd.Flags().GetStringSlice("team")
	relations, _ := cmd.Flags().GetBool("relations")
	pullIfStale, _ := cmd.Flags().GetBool("pull-if-stale")
	threshold, _ := cmd.Flags().GetDuration("threshold")
	noWait, _ := cmd.Flags().GetBool("no-wait")

	// Handle --pull-if-stale: skip pull if data is fresh
	if pullIfStale {
		beadsDir := resolveBeadsDirForStaleness()
		if beadsDir != "" {
			// Debounce: if a pull completed within 5 minutes, always fresh
			if linear.IsWithinDebounce(beadsDir) {
				info := linear.GetStalenessInfo(beadsDir, threshold)
				if jsonOutput {
					outputJSON(map[string]interface{}{
						"is_fresh":  true,
						"last_pull": info.LastPull.Format(time.RFC3339),
						"age":       linear.FormatAge(info.Age),
						"skipped":   true,
					})
				} else {
					fmt.Printf("Linear data is fresh (last pull %s ago, within debounce)\n", linear.FormatAge(info.Age))
				}
				return
			}

			if !linear.IsPullStale(beadsDir, threshold) {
				info := linear.GetStalenessInfo(beadsDir, threshold)
				if jsonOutput {
					outputJSON(map[string]interface{}{
						"is_fresh":  true,
						"last_pull": info.LastPull.Format(time.RFC3339),
						"age":       linear.FormatAge(info.Age),
						"skipped":   true,
					})
				} else {
					fmt.Printf("Linear data is fresh (last pull %s ago)\n", linear.FormatAge(info.Age))
				}
				return
			}
		}
		// Data is stale — proceed with pull
		pull = true
	}

	// Acquire per-workspace concurrency lock to serialize sync invocations.
	if lockDir := beads.FindBeadsDir(); lockDir != "" {
		wait := !noWait
		if !wait {
			fmt.Fprintln(os.Stderr, "Acquiring sync lock (non-blocking)...")
		} else {
			fmt.Fprintln(os.Stderr, "Acquiring sync lock...")
		}
		syncLock, err := linear.AcquireSyncLock(lockDir, wait)
		if err != nil {
			if held, ok := err.(*linear.SyncLockHeldError); ok {
				if held.Info != nil {
					FatalError("another bd linear sync is already running (PID %d, started %s)",
						held.Info.PID, held.Info.Started.Format("15:04:05"))
				}
				FatalError("another bd linear sync is already running")
			}
			FatalError("acquiring sync lock: %v", err)
		}
		defer func() {
			if err := syncLock.Release(); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to release sync lock: %v\n", err)
			}
		}()
	}

	if !dryRun {
		CheckReadonly("linear sync")
	}

	if preferLocal && preferLinear {
		FatalError("cannot use both --prefer-local and --prefer-linear")
	}
	if milestones && push && !pull {
		FatalError("--milestones only applies when pulling from Linear")
	}

	if err := ensureStoreActive(); err != nil {
		FatalError("database not available: %v", err)
	}

	if err := validateLinearConfig(cliTeams); err != nil {
		FatalError("%v", err)
	}

	ctx := rootCtx
	teamIDs := getLinearTeamIDs(ctx, cliTeams)
	willPush := push || !pull

	// Require explicit --team for push when multiple teams are configured.
	if willPush && len(teamIDs) > 1 && len(cliTeams) == 0 {
		FatalError("push requires explicit --team flag when multiple teams are configured\n" +
			"Use: bd linear sync --push --team <TEAM_ID>")
	}

	// Create and initialize the Linear tracker
	lt := &linear.Tracker{}
	lt.SetTeamIDs(teamIDs)
	if err := lt.Init(ctx, store); err != nil {
		FatalError("initializing Linear tracker: %v", err)
	}
	if willPush {
		if err := lt.ValidatePushStateMappings(ctx); err != nil {
			FatalError("%v", err)
		}
	}

	// Create the sync engine
	engine := tracker.NewEngine(lt, store, actor)
	engine.OnMessage = func(msg string) { fmt.Println("  " + msg) }
	engine.OnWarning = func(msg string) { fmt.Fprintf(os.Stderr, "Warning: %s\n", msg) }

	// Set up Linear-specific pull hooks
	engine.PullHooks = buildLinearPullHooks(ctx, linearPullHookOptions{
		Milestones: milestones,
		DryRun:     dryRun,
		Actor:      actor,
	})

	// Build sync options from CLI flags
	opts := tracker.SyncOptions{
		Pull:       pull,
		Push:       push,
		DryRun:     dryRun,
		CreateOnly: createOnly,
		State:      state,
	}
	opts.DependencySources = linearPullDependencySources(relations)

	// Convert type filters
	for _, t := range typeFilters {
		opts.TypeFilter = append(opts.TypeFilter, types.IssueType(strings.ToLower(t)))
	}
	for _, t := range excludeTypes {
		opts.ExcludeTypes = append(opts.ExcludeTypes, types.IssueType(strings.ToLower(t)))
	}
	if !includeEphemeral {
		opts.ExcludeEphemeral = true
	}

	if err := applySelectiveSyncFlags(cmd, &opts, push); err != nil {
		FatalError("%v", err)
	}
	allowProjectCreates := opts.ParentID != "" || len(opts.IssueIDs) > 0

	// Set up Linear-specific push hooks
	engine.PushHooks = buildLinearPushHooks(ctx, lt, allowProjectCreates)

	// Map conflict resolution
	if preferLocal {
		opts.ConflictResolution = tracker.ConflictLocal
	} else if preferLinear {
		opts.ConflictResolution = tracker.ConflictExternal
	} else {
		opts.ConflictResolution = tracker.ConflictTimestamp
	}

	// Run sync
	result, err := engine.Sync(ctx, opts)
	if err != nil {
		if jsonOutput {
			outputJSON(result)
		} else {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		os.Exit(1)
	}

	// Record successful pull timestamp
	if (pull || !push) && !dryRun {
		if beadsDir := resolveBeadsDirForStaleness(); beadsDir != "" {
			_ = linear.WriteLastPullTimestamp(beadsDir)
		}
	}

	// Output results
	if jsonOutput {
		if pullIfStale {
			// Augment JSON output with staleness info
			resultMap := map[string]interface{}{
				"stats":    result.Stats,
				"warnings": result.Warnings,
				"is_fresh": true,
				"skipped":  false,
			}
			outputJSON(resultMap)
		} else {
			outputJSON(result)
		}
	} else if dryRun {
		fmt.Println("\n✓ Dry run complete (no changes made)")
	} else {
		if result.Stats.Pulled > 0 {
			fmt.Printf("✓ Pulled %d issues (%d created, %d updated)\n",
				result.Stats.Pulled, result.Stats.Created, result.Stats.Updated)
		}
		if result.Stats.Pushed > 0 {
			fmt.Printf("✓ Pushed %d issues\n", result.Stats.Pushed)
		}
		if result.Stats.Conflicts > 0 {
			fmt.Printf("→ Resolved %d conflicts\n", result.Stats.Conflicts)
		}
		fmt.Println("\n✓ Linear sync complete")
		if len(result.Warnings) > 0 {
			fmt.Println("\nWarnings:")
			for _, w := range result.Warnings {
				fmt.Printf("  - %s\n", w)
			}
		}
	}
}

func linearPullDependencySources(includeRelations bool) []tracker.DependencySource {
	if includeRelations {
		return nil
	}
	return []tracker.DependencySource{tracker.DependencySourceParent}
}

type linearPullHookOptions struct {
	Milestones bool
	DryRun     bool
	Actor      string
}

// buildLinearPullHooks creates PullHooks for Linear-specific pull behavior.
func buildLinearPullHooks(ctx context.Context, opts linearPullHookOptions) *tracker.PullHooks {
	return buildLinearPullHooksForStore(ctx, store, opts)
}

func buildLinearPullHooksForStore(ctx context.Context, st storage.Storage, opts linearPullHookOptions) *tracker.PullHooks {
	idMode := getLinearIDMode(ctx)
	hashLength := getLinearHashLength(ctx)

	hooks := &tracker.PullHooks{}
	hookActor := opts.Actor
	if hookActor == "" {
		hookActor = actor
	}

	var generateID func(context.Context, *types.Issue) error
	if idMode == "hash" && st != nil {
		// Pre-load existing IDs for collision avoidance
		existingIssues, err := st.SearchIssues(ctx, "", types.IssueFilter{})
		usedIDs := make(map[string]bool)
		if err == nil {
			for _, issue := range existingIssues {
				if issue.ID != "" {
					usedIDs[issue.ID] = true
				}
			}
		}

		// YAML config takes precedence — in shared-server mode the DB
		// may belong to a different project (GH#2469).
		prefix := config.GetString("issue-prefix")
		if prefix == "" {
			var err error
			prefix, err = st.GetConfig(ctx, "issue_prefix")
			if err != nil || prefix == "" {
				prefix = "bd"
			}
		}

		generateID = func(_ context.Context, issue *types.Issue) error {
			ids := []*types.Issue{issue}
			idOpts := linear.IDGenerationOptions{
				BaseLength: hashLength,
				MaxLength:  8,
				UsedIDs:    usedIDs,
			}
			if err := linear.GenerateIssueIDs(ids, prefix, "linear-import", idOpts); err != nil {
				return err
			}
			// Track the newly generated ID for future collision avoidance
			usedIDs[issue.ID] = true
			return nil
		}
		hooks.GenerateID = generateID
	}

	if opts.Milestones && st != nil {
		hooks.AfterConvert = func(ctx context.Context, extIssue *tracker.TrackerIssue, conv *tracker.IssueConversion, ref string, _ *types.Issue, syncOpts tracker.SyncOptions) error {
			li, ok := extIssue.Raw.(*linear.Issue)
			if !ok || li == nil || li.ProjectMilestone == nil {
				return nil
			}
			if syncOpts.DryRun || opts.DryRun {
				return nil
			}
			milestoneRef, err := ensureLinearMilestoneEpic(ctx, st, li.ProjectMilestone, hookActor, generateID)
			if err != nil {
				return err
			}
			if strings.TrimSpace(ref) == "" {
				return fmt.Errorf("missing external ref for Linear issue %s", extIssue.Identifier)
			}
			conv.Dependencies = append(conv.Dependencies, tracker.DependencyInfo{
				FromExternalID: ref,
				ToExternalID:   milestoneRef,
				Type:           string(types.DepParentChild),
				Source:         tracker.DependencySourceParent,
			})
			return nil
		}
	}

	return hooks
}

const linearMilestoneExternalRefPrefix = "linear:project-milestone:"

func linearMilestoneExternalRef(id string) string {
	return linearMilestoneExternalRefPrefix + strings.TrimSpace(id)
}

func isLinearMilestoneExternalRef(ref string) bool {
	return strings.HasPrefix(strings.TrimSpace(ref), linearMilestoneExternalRefPrefix)
}

func ensureLinearMilestoneEpic(ctx context.Context, st storage.Storage, ms *linear.ProjectMilestone, actor string, generateID func(context.Context, *types.Issue) error) (string, error) {
	milestoneID := strings.TrimSpace(ms.ID)
	if milestoneID == "" {
		return "", fmt.Errorf("Linear project milestone is missing id")
	}
	title := strings.TrimSpace(ms.Name)
	if title == "" {
		title = milestoneID
	}
	description := ms.Description
	ref := linearMilestoneExternalRef(milestoneID)

	metadata, err := mergedLinearMilestoneMetadata(nil, ms)
	if err != nil {
		return "", err
	}

	existing, err := findLinearMilestoneEpic(ctx, st, ref, milestoneID, title)
	if err != nil {
		return "", err
	}
	if existing != nil {
		updates := map[string]interface{}{}
		if existing.Title != title {
			updates["title"] = title
		}
		if existing.Description != description {
			updates["description"] = description
		}
		if existing.IssueType != types.TypeEpic {
			updates["issue_type"] = string(types.TypeEpic)
		}
		if existing.ExternalRef == nil || strings.TrimSpace(*existing.ExternalRef) != ref {
			updates["external_ref"] = ref
		}
		mergedMetadata, err := mergedLinearMilestoneMetadata(existing.Metadata, ms)
		if err != nil {
			return "", err
		}
		if string(existing.Metadata) != string(mergedMetadata) {
			updates["metadata"] = mergedMetadata
		}
		if len(updates) > 0 {
			if err := st.UpdateIssue(ctx, existing.ID, updates, actor); err != nil {
				return "", fmt.Errorf("updating Linear milestone epic %s: %w", existing.ID, err)
			}
		}
		return ref, nil
	}

	externalRef := ref
	epic := &types.Issue{
		Title:       title,
		Description: description,
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeEpic,
		ExternalRef: &externalRef,
		Metadata:    metadata,
	}
	if generateID != nil {
		if err := generateID(ctx, epic); err != nil {
			return "", fmt.Errorf("generating Linear milestone epic ID: %w", err)
		}
	}
	if err := st.CreateIssue(ctx, epic, actor); err != nil {
		return "", fmt.Errorf("creating Linear milestone epic %q: %w", title, err)
	}
	return ref, nil
}

func findLinearMilestoneEpic(ctx context.Context, st storage.Storage, ref, milestoneID, title string) (*types.Issue, error) {
	if existing, err := st.GetIssueByExternalRef(ctx, ref); err == nil {
		return existing, nil
	} else if !errors.Is(err, storage.ErrNotFound) {
		return nil, err
	}

	issues, err := st.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return nil, fmt.Errorf("searching local issues for Linear milestone %s: %w", milestoneID, err)
	}
	for _, issue := range issues {
		if issueHasLinearMilestoneID(issue, milestoneID) {
			return issue, nil
		}
	}

	for _, issue := range issues {
		if issue.IssueType != types.TypeEpic || !strings.EqualFold(strings.TrimSpace(issue.Title), title) {
			continue
		}
		ref := ""
		if issue.ExternalRef != nil {
			ref = strings.TrimSpace(*issue.ExternalRef)
		}
		if ref == "" {
			return issue, nil
		}
	}
	return nil, nil
}

func mergedLinearMilestoneMetadata(existing json.RawMessage, ms *linear.ProjectMilestone) (json.RawMessage, error) {
	data := make(map[string]interface{})
	if len(existing) > 0 {
		trimmed := strings.TrimSpace(string(existing))
		if trimmed != "" && trimmed != "null" {
			if err := json.Unmarshal(existing, &data); err != nil {
				return nil, fmt.Errorf("existing milestone metadata is not a JSON object: %w", err)
			}
		}
	}

	linearMeta, _ := data["linear"].(map[string]interface{})
	if linearMeta == nil {
		linearMeta = make(map[string]interface{})
	}
	linearMeta["kind"] = "project_milestone"
	linearMeta["project_milestone"] = map[string]interface{}{
		"id":          strings.TrimSpace(ms.ID),
		"name":        ms.Name,
		"description": ms.Description,
		"progress":    ms.Progress,
		"targetDate":  ms.TargetDate,
	}
	data["linear"] = linearMeta

	raw, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshaling Linear milestone metadata: %w", err)
	}
	return json.RawMessage(raw), nil
}

func issueHasLinearMilestoneID(issue *types.Issue, milestoneID string) bool {
	if issue == nil || len(issue.Metadata) == 0 {
		return false
	}
	var data struct {
		Linear struct {
			Kind             string `json:"kind"`
			ProjectMilestone struct {
				ID string `json:"id"`
			} `json:"project_milestone"`
		} `json:"linear"`
	}
	if err := json.Unmarshal(issue.Metadata, &data); err != nil {
		return false
	}
	return data.Linear.Kind == "project_milestone" &&
		strings.TrimSpace(data.Linear.ProjectMilestone.ID) == strings.TrimSpace(milestoneID)
}

func isLinearMilestoneIssue(issue *types.Issue) bool {
	if issue == nil {
		return false
	}
	if issue.ExternalRef != nil && isLinearMilestoneExternalRef(*issue.ExternalRef) {
		return true
	}
	var data struct {
		Linear struct {
			Kind string `json:"kind"`
		} `json:"linear"`
	}
	if len(issue.Metadata) == 0 || json.Unmarshal(issue.Metadata, &data) != nil {
		return false
	}
	return data.Linear.Kind == "project_milestone"
}

// buildLinearPushHooks creates PushHooks for Linear-specific push behavior.
func buildLinearPushHooks(ctx context.Context, lt *linear.Tracker, allowProjectCreates bool) *tracker.PushHooks {
	config := lt.MappingConfig()
	return &tracker.PushHooks{
		FormatDescription: func(issue *types.Issue) string {
			return linear.BuildLinearDescription(issue)
		},
		ContentEqual: func(local *types.Issue, remote *tracker.TrackerIssue) bool {
			remoteIssue, ok := remote.Raw.(*linear.Issue)
			if ok && remoteIssue != nil {
				return linear.PushFieldsEqual(local, remoteIssue, config)
			}
			remoteConv := lt.FieldMapper().IssueToBeads(remote)
			if remoteConv == nil || remoteConv.Issue == nil {
				return false
			}
			return linear.PushFieldsEqualToBeads(local, remoteConv.Issue)
		},
		BuildStateCache: func(ctx context.Context) (interface{}, error) {
			return linear.BuildStateCacheFromTracker(ctx, lt)
		},
		ResolveState: func(cache interface{}, status types.Status) (string, bool) {
			sc, ok := cache.(*linear.StateCache)
			if !ok || sc == nil {
				return "", false
			}
			id := sc.FindStateForBeadsStatus(status)
			return id, id != ""
		},
		ShouldPush: func(issue *types.Issue) bool {
			if isLinearMilestoneIssue(issue) {
				return false
			}
			if projectID, _ := store.GetConfig(ctx, "linear.project_id"); projectID != "" {
				if issue.ExternalRef == nil || strings.TrimSpace(*issue.ExternalRef) == "" {
					if !allowProjectCreates {
						return false
					}
				}
			}

			// Apply push prefix filtering if configured
			pushPrefix, _ := store.GetConfig(ctx, "linear.push_prefix")
			if pushPrefix == "" {
				return true
			}
			for _, prefix := range strings.Split(pushPrefix, ",") {
				prefix = strings.TrimSpace(prefix)
				prefix = strings.TrimSuffix(prefix, "-")
				if prefix != "" && strings.HasPrefix(issue.ID, prefix+"-") {
					return true
				}
			}
			return false
		},
	}
}

func runLinearStatus(cmd *cobra.Command, args []string) {
	ctx := rootCtx

	if err := ensureStoreActive(); err != nil {
		FatalError("%v", err)
	}

	apiKey, _ := getLinearConfig(ctx, "linear.api_key")
	oauthClientID, _ := getLinearConfig(ctx, "linear.oauth_client_id")
	oauthClientSecret, _ := getLinearConfig(ctx, "linear.oauth_client_secret")
	teamIDs := getLinearTeamIDs(ctx, nil)
	lastSync, _ := store.GetConfig(ctx, "linear.last_sync")

	hasOAuth := oauthClientID != "" && oauthClientSecret != ""
	configured := (apiKey != "" || hasOAuth) && len(teamIDs) > 0

	allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		FatalError("%v", err)
	}

	withLinearRef := 0
	pendingPush := 0
	for _, issue := range allIssues {
		if issue.ExternalRef != nil && linear.IsLinearExternalRef(*issue.ExternalRef) {
			withLinearRef++
		} else if issue.ExternalRef == nil {
			pendingPush++
		}
	}

	if jsonOutput {
		hasAPIKey := apiKey != ""
		// Backward compat: include team_id as first team, plus full list.
		teamID := ""
		if len(teamIDs) > 0 {
			teamID = teamIDs[0]
		}
		authMode := "none"
		if hasOAuth {
			authMode = "oauth"
		} else if hasAPIKey {
			authMode = "api_key"
		}
		outputJSON(map[string]interface{}{
			"configured":      configured,
			"has_api_key":     hasAPIKey,
			"has_oauth":       hasOAuth,
			"auth_mode":       authMode,
			"team_id":         teamID,
			"team_ids":        teamIDs,
			"last_sync":       lastSync,
			"total_issues":    len(allIssues),
			"with_linear_ref": withLinearRef,
			"pending_push":    pendingPush,
		})
		return
	}

	fmt.Println("Linear Sync Status")
	fmt.Println("==================")
	fmt.Println()

	if !configured {
		fmt.Println("Status: Not configured")
		fmt.Println()
		fmt.Println("To configure Linear integration:")
		fmt.Println("  bd config set linear.api_key \"YOUR_API_KEY\"")
		fmt.Println("  bd config set linear.team_id \"TEAM_ID\"")
		fmt.Println("  bd config set linear.team_ids \"TEAM_ID1,TEAM_ID2\"  # multiple teams")
		fmt.Println()
		fmt.Println("Or use environment variables:")
		fmt.Println("  export LINEAR_API_KEY=\"YOUR_API_KEY\"")
		fmt.Println("  export LINEAR_TEAM_ID=\"TEAM_ID\"")
		fmt.Println()
		fmt.Println("For CI/OAuth authentication:")
		fmt.Println("  export LINEAR_OAUTH_CLIENT_ID=\"...\"")
		fmt.Println("  export LINEAR_OAUTH_CLIENT_SECRET=\"...\"")
		return
	}

	if len(teamIDs) == 1 {
		fmt.Printf("Team ID:      %s\n", teamIDs[0])
	} else {
		fmt.Printf("Team IDs:     %s (%d teams)\n", strings.Join(teamIDs, ", "), len(teamIDs))
	}
	if hasOAuth {
		fmt.Printf("Auth:         OAuth (client_credentials)\n")
	} else {
		fmt.Printf("API Key:      %s\n", maskAPIKey(apiKey))
	}
	if lastSync != "" {
		fmt.Printf("Last Sync:    %s\n", lastSync)
	} else {
		fmt.Println("Last Sync:    Never")
	}
	fmt.Println()
	fmt.Printf("Total Issues: %d\n", len(allIssues))
	fmt.Printf("With Linear:  %d\n", withLinearRef)
	fmt.Printf("Local Only:   %d\n", pendingPush)

	if pendingPush > 0 {
		fmt.Println()
		fmt.Printf("Run 'bd linear sync --push' to push %d local issue(s) to Linear\n", pendingPush)
	}
}

func runLinearTeams(cmd *cobra.Command, args []string) {
	ctx := rootCtx

	client, err := buildLinearClient(ctx, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	teams, err := client.FetchTeams(ctx)
	if err != nil {
		FatalError("fetching teams: %v", err)
	}

	if len(teams) == 0 {
		fmt.Println("No teams found (check your API key permissions)")
		return
	}

	if jsonOutput {
		outputJSON(teams)
		return
	}

	fmt.Println("Available Linear Teams")
	fmt.Println("======================")
	fmt.Println()
	fmt.Printf("%-40s  %-6s  %s\n", "ID (use this for linear.team_id)", "Key", "Name")
	fmt.Printf("%-40s  %-6s  %s\n", "----------------------------------------", "------", "----")
	for _, team := range teams {
		fmt.Printf("%-40s  %-6s  %s\n", team.ID, team.Key, team.Name)
	}
	fmt.Println()
	fmt.Println("To configure:")
	fmt.Println("  bd config set linear.team_id \"<ID>\"")
	fmt.Println("  bd config set linear.team_ids \"<ID1>,<ID2>\"  # multiple teams")
}

// resolveBeadsDirForStaleness returns the active beads directory for
// staleness tracking. Falls back to BEADS_DIR env, then dbPath resolution.
func resolveBeadsDirForStaleness() string {
	if dir := os.Getenv("BEADS_DIR"); dir != "" {
		return dir
	}
	if dbPath != "" {
		return resolveCommandBeadsDir(dbPath)
	}
	return ""
}

// uuidRegex matches valid UUID format (with or without hyphens).
var uuidRegex = regexp.MustCompile(`^[0-9a-fA-F]{8}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{12}$`)

func isValidUUID(s string) bool {
	return uuidRegex.MatchString(s)
}

// validateLinearConfig checks that required Linear configuration is present.
// cliTeams is the list of team IDs from the --team flag (may be nil).
func validateLinearConfig(cliTeams []string) error {
	if err := ensureStoreActive(); err != nil {
		return fmt.Errorf("database not available: %w", err)
	}

	ctx := rootCtx

	// Accept either OAuth credentials or API key.
	oauthClientID, _ := getLinearConfig(ctx, "linear.oauth_client_id")
	oauthClientSecret, _ := getLinearConfig(ctx, "linear.oauth_client_secret")
	hasOAuth := oauthClientID != "" && oauthClientSecret != ""

	if !hasOAuth {
		apiKey, _ := getLinearConfig(ctx, "linear.api_key")
		if apiKey == "" {
			return fmt.Errorf("Linear authentication not configured\n" +
				"Options:\n" +
				"  OAuth (for CI):  export LINEAR_OAUTH_CLIENT_ID=... LINEAR_OAUTH_CLIENT_SECRET=...\n" +
				"  API key (devs):  export LINEAR_API_KEY=... or bd config set linear.api_key \"YOUR_API_KEY\"")
		}
	}

	teamIDs := getLinearTeamIDs(ctx, cliTeams)
	if len(teamIDs) == 0 {
		return fmt.Errorf("no Linear team ID configured\nRun: bd config set linear.team_id \"TEAM_ID\"\nOr:  bd config set linear.team_ids \"TEAM_ID1,TEAM_ID2\"\nOr: export LINEAR_TEAM_ID=TEAM_ID")
	}

	for _, id := range teamIDs {
		if !isValidUUID(id) {
			return fmt.Errorf("invalid Linear team ID (expected UUID format like '12345678-1234-1234-1234-123456789abc')\nInvalid value: %s", id)
		}
	}

	return nil
}

// maskAPIKey returns a masked version of an API key for display.
// Shows first 4 and last 4 characters, with dots in between.
func maskAPIKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

// getLinearConfig reads a Linear configuration value. Returns the value and its source.
// Priority: environment variable > project config.
// Env vars take precedence so CI workers can override config without modifying config.yaml.
func getLinearConfig(ctx context.Context, key string) (value string, source string) {
	// Secret keys (e.g. linear.api_key) are stored in config.yaml, not the
	// Dolt database, to avoid leaking secrets when pushing to remotes.
	// Env vars are checked first so that LINEAR_OAUTH_CLIENT_ID/SECRET etc.
	// override whatever is in config.yaml.
	if config.IsYamlOnlyKey(key) {
		envKey := linearConfigToEnvVar(key)
		if envKey != "" {
			if value := os.Getenv(envKey); value != "" {
				return value, fmt.Sprintf("environment variable (%s)", envKey)
			}
		}
		if value := config.GetString(key); value != "" {
			return value, "project config (config.yaml)"
		}
		return "", ""
	}

	// Try to read from store (works in direct mode)
	if store != nil {
		value, _ = store.GetConfig(ctx, key) // Best effort: empty value is valid fallback
		if value != "" {
			return value, "project config (bd config)"
		}
	} else if dbPath != "" {
		tempStore, err := openReadOnlyStoreForDBPath(ctx, dbPath)
		if err == nil {
			defer func() { _ = tempStore.Close() }()
			value, _ = tempStore.GetConfig(ctx, key) // Best effort: empty value is valid fallback
			if value != "" {
				return value, "project config (bd config)"
			}
		}
	}

	// Fall back to environment variable
	envKey := linearConfigToEnvVar(key)
	if envKey != "" {
		value = os.Getenv(envKey)
		if value != "" {
			return value, fmt.Sprintf("environment variable (%s)", envKey)
		}
	}

	return "", ""
}

// linearConfigToEnvVar maps Linear config keys to their environment variable names.
func linearConfigToEnvVar(key string) string {
	switch key {
	case "linear.api_key":
		return "LINEAR_API_KEY"
	case "linear.team_id":
		return "LINEAR_TEAM_ID"
	case "linear.team_ids":
		return "LINEAR_TEAM_IDS"
	case "linear.oauth_client_id":
		return "LINEAR_OAUTH_CLIENT_ID"
	case "linear.oauth_client_secret":
		return "LINEAR_OAUTH_CLIENT_SECRET"
	default:
		return ""
	}
}

// getLinearTeamIDs resolves the effective team IDs from all config sources.
// Precedence: cliTeams (--team flag) > linear.team_ids > LINEAR_TEAM_IDS > linear.team_id > LINEAR_TEAM_ID
func getLinearTeamIDs(ctx context.Context, cliTeams []string) []string {
	pluralVal, _ := getLinearConfig(ctx, "linear.team_ids")
	singularVal, _ := getLinearConfig(ctx, "linear.team_id")
	return tracker.ResolveProjectIDs(cliTeams, pluralVal, singularVal)
}

// getLinearClient creates a configured Linear client from beads config.
// Uses the first configured team ID for operations that require a single team.
//
// Auth precedence:
//  1. OAuth env vars (LINEAR_OAUTH_CLIENT_ID + LINEAR_OAUTH_CLIENT_SECRET)
//  2. LINEAR_API_KEY env var
//  3. linear.oauth_client_id + linear.oauth_client_secret in config
//  4. linear.api_key in config
func getLinearClient(ctx context.Context) (*linear.Client, error) {
	teamIDs := getLinearTeamIDs(ctx, nil)
	if len(teamIDs) == 0 {
		return nil, fmt.Errorf("Linear team ID not configured")
	}

	client, err := buildLinearClient(ctx, teamIDs[0])
	if err != nil {
		return nil, err
	}

	if store != nil {
		if endpoint, _ := store.GetConfig(ctx, "linear.api_endpoint"); endpoint != "" {
			client = client.WithEndpoint(endpoint)
		}
		if projectID, _ := store.GetConfig(ctx, "linear.project_id"); projectID != "" {
			client = client.WithProjectID(projectID)
		}
		// Apply optional rate-limit circuit-breaker floor.
		// Readable/settable via `bd config get/set linear.rate_limit_floor`.
		// Also honored via the LINEAR_RATE_LIMIT_FLOOR environment variable.
		floorStr, _ := getLinearConfig(ctx, "linear.rate_limit_floor")
		if floorStr == "" {
			floorStr = os.Getenv("LINEAR_RATE_LIMIT_FLOOR")
		}
		if floorStr != "" {
			if v, err := strconv.Atoi(strings.TrimSpace(floorStr)); err == nil && v >= 0 {
				client = client.WithRateLimitFloor(v)
			}
		}
	}

	return client, nil
}

// buildLinearClient resolves auth credentials and returns an appropriately
// configured Linear client. OAuth takes precedence over API key.
func buildLinearClient(ctx context.Context, teamID string) (*linear.Client, error) {
	oauthClientID, _ := getLinearConfig(ctx, "linear.oauth_client_id")
	oauthClientSecret, _ := getLinearConfig(ctx, "linear.oauth_client_secret")

	if oauthClientID != "" && oauthClientSecret != "" {
		debug.Logf("Linear: using OAuth client-credentials authentication")
		oauthCfg := linear.OAuthConfig{
			ClientID:     oauthClientID,
			ClientSecret: oauthClientSecret,
		}
		return linear.NewOAuthClient(oauthCfg, teamID), nil
	}

	apiKey, _ := getLinearConfig(ctx, "linear.api_key")
	if apiKey == "" {
		return nil, fmt.Errorf("Linear authentication not configured\n" +
			"Options:\n" +
			"  OAuth (for CI):  export LINEAR_OAUTH_CLIENT_ID=... LINEAR_OAUTH_CLIENT_SECRET=...\n" +
			"  API key (devs):  export LINEAR_API_KEY=... or bd config set linear.api_key \"...\"")
	}

	return linear.NewClient(apiKey, teamID), nil
}

// storeConfigLoader adapts the store to the linear.ConfigLoader interface.
type storeConfigLoader struct {
	ctx context.Context
}

func (l *storeConfigLoader) GetAllConfig() (map[string]string, error) {
	return store.GetAllConfig(l.ctx)
}

// loadLinearMappingConfig loads mapping configuration from beads config.
func loadLinearMappingConfig(ctx context.Context) *linear.MappingConfig {
	if store == nil {
		return linear.DefaultMappingConfig()
	}
	return linear.LoadMappingConfig(&storeConfigLoader{ctx: ctx})
}

// getLinearIDMode returns the configured ID mode for Linear imports.
// Supported values: "hash" (default) or "db".
func getLinearIDMode(ctx context.Context) string {
	mode, _ := getLinearConfig(ctx, "linear.id_mode")
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return "hash"
	}
	return mode
}

// getLinearHashLength returns the configured hash length for Linear imports.
// Values are clamped to the supported range 3-8.
func getLinearHashLength(ctx context.Context) int {
	raw, _ := getLinearConfig(ctx, "linear.hash_length")
	if raw == "" {
		return 6
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 6
	}
	if value < 3 {
		return 3
	}
	if value > 8 {
		return 8
	}
	return value
}
