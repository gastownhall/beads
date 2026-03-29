package tracker

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// syncTracer is the OTel tracer for tracker sync spans.
var syncTracer = otel.Tracer("github.com/steveyegge/beads/tracker")

// PullHooks contains optional callbacks that customize pull (import) behavior.
// Trackers opt into behaviors by setting the hooks they need.
type PullHooks struct {
	// GenerateID assigns an ID to a newly-pulled issue before import.
	// If nil, issues keep whatever ID the storage layer assigns.
	// The hook receives the issue (with converted fields) and should set issue.ID.
	// Callers typically pre-load used IDs into the closure for collision avoidance.
	GenerateID func(ctx context.Context, issue *types.Issue) error

	// TransformIssue is called after FieldMapper.IssueToBeads() and before storage.
	// Use for description formatting, field normalization, etc.
	TransformIssue func(issue *types.Issue)

	// ShouldImport filters issues during pull. Return false to skip.
	// Called on the raw TrackerIssue before conversion to beads format.
	// If nil, all issues are imported.
	ShouldImport func(issue *TrackerIssue) bool
}

// PushHooks contains optional callbacks that customize push (export) behavior.
// Trackers opt into behaviors by setting the hooks they need.
type PushHooks struct {
	// FormatDescription transforms the description before sending to tracker.
	// Linear uses this for BuildLinearDescription (merging structured fields).
	// If nil, issue.Description is used as-is.
	FormatDescription func(issue *types.Issue) string

	// ContentEqual compares local and remote issues to skip unnecessary API calls.
	// Returns true if content is identical (skip update). If nil, uses timestamp comparison.
	ContentEqual func(local *types.Issue, remote *TrackerIssue) bool

	// ShouldPush filters issues during push. Return false to skip.
	// Called in addition to type/state/ephemeral filters. Use for prefix filtering, etc.
	// If nil, all issues (matching other filters) are pushed.
	ShouldPush func(issue *types.Issue) bool

	// BuildStateCache is called once before the push loop to pre-cache workflow states.
	// Returns an opaque cache value passed to ResolveState on each issue.
	// If nil, no caching is done.
	BuildStateCache func(ctx context.Context) (interface{}, error)

	// ResolveState maps a beads status to a tracker state ID using the cached state.
	// Only called if BuildStateCache is set. Returns (stateID, ok).
	ResolveState func(cache interface{}, status types.Status) (string, bool)
}

// Engine orchestrates synchronization between beads and an external tracker.
// It implements the shared Pull→Detect→Resolve→Push pattern that all tracker
// integrations follow, eliminating duplication between Linear, GitLab, etc.
type Engine struct {
	Tracker   IssueTracker
	Store     storage.Storage
	Actor     string
	PullHooks *PullHooks
	PushHooks *PushHooks

	// Callbacks for UI feedback (optional).
	OnMessage func(msg string)
	OnWarning func(msg string)

	// stateCache holds the opaque value from PushHooks.BuildStateCache during a push.
	// Tracker adapters access it via ResolveState().
	stateCache interface{}
}

// NewEngine creates a new sync engine for the given tracker and storage.
func NewEngine(tracker IssueTracker, store storage.Storage, actor string) *Engine {
	return &Engine{
		Tracker: tracker,
		Store:   store,
		Actor:   actor,
	}
}

// Sync performs a complete synchronization operation based on the given options.
func (e *Engine) Sync(ctx context.Context, opts SyncOptions) (*SyncResult, error) {
	ctx, span := syncTracer.Start(ctx, "tracker.sync",
		trace.WithAttributes(
			attribute.String("sync.tracker", e.Tracker.DisplayName()),
			attribute.Bool("sync.pull", opts.Pull || (!opts.Pull && !opts.Push)),
			attribute.Bool("sync.push", opts.Push || (!opts.Pull && !opts.Push)),
			attribute.Bool("sync.dry_run", opts.DryRun),
		),
	)
	defer span.End()

	result := &SyncResult{Success: true}

	// Default to bidirectional if neither specified
	if !opts.Pull && !opts.Push {
		opts.Pull = true
		opts.Push = true
	}

	// Track IDs to skip/force during push based on conflict resolution
	skipPushIDs := make(map[string]bool)
	forcePushIDs := make(map[string]bool)

	allowPullOverwriteIDs := make(map[string]bool)

	// Phase 1: Detect conflicts (only for bidirectional sync, skip if CommentsOnly)
	if opts.Pull && opts.Push && !opts.CommentsOnly {
		conflicts, err := e.DetectConflicts(ctx)
		if err != nil {
			e.warn("Failed to detect conflicts: %v", err)
		} else if len(conflicts) > 0 {
			result.Stats.Conflicts = len(conflicts)
			e.resolveConflicts(opts, conflicts, skipPushIDs, forcePushIDs, allowPullOverwriteIDs)
		}
	}

	// Phase 2: Pull (skip if CommentsOnly)
	if opts.Pull && !opts.CommentsOnly {
		pullStats, err := e.doPull(ctx, opts, allowPullOverwriteIDs)
		if err != nil {
			result.Success = false
			result.Error = fmt.Sprintf("pull failed: %v", err)
			span.RecordError(err)
			span.SetStatus(codes.Error, result.Error)
			return result, err
		}
		result.PullStats = *pullStats
		result.Stats.Pulled = pullStats.Created + pullStats.Updated
		result.Stats.Created += pullStats.Created
		result.Stats.Updated += pullStats.Updated
		result.Stats.Skipped += pullStats.Skipped
	}

	// Phase 3: Push (skip if CommentsOnly)
	if opts.Push && !opts.CommentsOnly {
		pushStats, err := e.doPush(ctx, opts, skipPushIDs, forcePushIDs)
		if err != nil {
			result.Success = false
			result.Error = fmt.Sprintf("push failed: %v", err)
			span.RecordError(err)
			span.SetStatus(codes.Error, result.Error)
			return result, err
		}
		result.PushStats = *pushStats
		result.Stats.Pushed = pushStats.Created + pushStats.Updated
		result.Stats.Created += pushStats.Created
		result.Stats.Updated += pushStats.Updated
		result.Stats.Skipped += pushStats.Skipped
		result.Stats.Errors += pushStats.Errors
		result.Warnings = append(result.Warnings, pushStats.Warnings...)
	}

	// Phases 4 & 5 share a cached map of external issue lookups to avoid
	// redundant FetchIssue calls (N+1 problem).
	needComments := !opts.NoComments
	_, hasCommentSyncer := e.Tracker.(CommentSyncer)
	needAttachments := !opts.NoAttachments && !opts.CommentsOnly
	_, hasAttachmentFetcher := e.Tracker.(AttachmentFetcher)

	var extIssueCache map[string]*TrackerIssue
	if (needComments && hasCommentSyncer) || (needAttachments && hasAttachmentFetcher) {
		extIssueCache = e.buildExtIssueCache(ctx)
	}

	// Phase 4: Comment sync (if tracker supports it and not disabled)
	if needComments {
		if syncer, ok := e.Tracker.(CommentSyncer); ok {
			commentStats := e.doCommentSync(ctx, opts, syncer, extIssueCache)
			result.Stats.CommentsPulled += commentStats.Pulled
			result.Stats.CommentsPushed += commentStats.Pushed
		}
	}

	// Phase 5: Attachment pull (if tracker supports it and not disabled)
	if needAttachments {
		if fetcher, ok := e.Tracker.(AttachmentFetcher); ok {
			attachStats := e.doAttachmentPull(ctx, opts, fetcher, extIssueCache)
			result.Stats.AttachmentsPulled += attachStats.Pulled
		}
	}

	// Record final stats as span attributes.
	span.SetAttributes(
		attribute.Int("sync.pulled", result.Stats.Pulled),
		attribute.Int("sync.pushed", result.Stats.Pushed),
		attribute.Int("sync.conflicts", result.Stats.Conflicts),
		attribute.Int("sync.created", result.Stats.Created),
		attribute.Int("sync.updated", result.Stats.Updated),
		attribute.Int("sync.skipped", result.Stats.Skipped),
		attribute.Int("sync.errors", result.Stats.Errors),
		attribute.Int("sync.comments_pulled", result.Stats.CommentsPulled),
		attribute.Int("sync.comments_pushed", result.Stats.CommentsPushed),
		attribute.Int("sync.attachments_pulled", result.Stats.AttachmentsPulled),
	)

	// Update last_sync timestamp
	if !opts.DryRun {
		lastSync := time.Now().UTC().Format(time.RFC3339Nano)
		key := e.Tracker.ConfigPrefix() + ".last_sync"
		if err := e.Store.SetConfig(ctx, key, lastSync); err != nil {
			e.warn("Failed to update last_sync: %v", err)
		}
		result.LastSync = lastSync
	}

	return result, nil
}

// DetectConflicts identifies issues that were modified both locally and externally
// since the last sync.
func (e *Engine) DetectConflicts(ctx context.Context) ([]Conflict, error) {
	ctx, span := syncTracer.Start(ctx, "tracker.detect_conflicts",
		trace.WithAttributes(attribute.String("sync.tracker", e.Tracker.DisplayName())),
	)
	defer span.End()

	// Get last sync time
	key := e.Tracker.ConfigPrefix() + ".last_sync"
	lastSyncStr, err := e.Store.GetConfig(ctx, key)
	if err != nil || lastSyncStr == "" {
		return nil, nil // No previous sync, no conflicts possible
	}

	lastSync, err := parseSyncTime(lastSyncStr)
	if err != nil {
		return nil, fmt.Errorf("invalid last_sync timestamp %q: %w", lastSyncStr, err)
	}

	// Find local issues with external refs for this tracker
	filter := types.IssueFilter{}
	issues, err := e.Store.SearchIssues(ctx, "", filter)
	if err != nil {
		return nil, fmt.Errorf("searching issues: %w", err)
	}

	var conflicts []Conflict
	for _, issue := range issues {
		extRef := derefStr(issue.ExternalRef)
		if extRef == "" || !e.Tracker.IsExternalRef(extRef) {
			continue
		}

		// Check if locally modified since last sync
		if issue.UpdatedAt.Before(lastSync) || issue.UpdatedAt.Equal(lastSync) {
			continue
		}

		// Fetch external version to check if also modified
		extID := e.Tracker.ExtractIdentifier(extRef)
		if extID == "" {
			continue
		}

		extIssue, err := e.Tracker.FetchIssue(ctx, extID)
		if err != nil || extIssue == nil {
			continue
		}

		if extIssue.UpdatedAt.After(lastSync) {
			conflicts = append(conflicts, Conflict{
				IssueID:            issue.ID,
				LocalUpdated:       issue.UpdatedAt,
				ExternalUpdated:    extIssue.UpdatedAt,
				ExternalRef:        extRef,
				ExternalIdentifier: extIssue.Identifier,
				ExternalInternalID: extIssue.ID,
			})
		}
	}

	span.SetAttributes(attribute.Int("sync.conflicts", len(conflicts)))
	return conflicts, nil
}

// doPull imports issues from the external tracker into beads.
func (e *Engine) doPull(ctx context.Context, opts SyncOptions, allowOverwriteIDs map[string]bool) (*PullStats, error) {
	ctx, span := syncTracer.Start(ctx, "tracker.pull",
		trace.WithAttributes(
			attribute.String("sync.tracker", e.Tracker.DisplayName()),
			attribute.Bool("sync.dry_run", opts.DryRun),
		),
	)
	defer span.End()

	stats := &PullStats{}

	// Determine if incremental sync is possible
	fetchOpts := FetchOptions{State: opts.State}
	var lastSync *time.Time
	key := e.Tracker.ConfigPrefix() + ".last_sync"
	if lastSyncStr, err := e.Store.GetConfig(ctx, key); err == nil && lastSyncStr != "" {
		if t, err := parseSyncTime(lastSyncStr); err == nil {
			fetchOpts.Since = &t
			lastSync = &t
			stats.Incremental = true
			stats.SyncedSince = lastSyncStr
		}
	}

	localIssues, err := e.Store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return nil, fmt.Errorf("searching local issues: %w", err)
	}
	localByExternalIdentifier := make(map[string]*types.Issue, len(localIssues))
	localByID := make(map[string]*types.Issue, len(localIssues))
	for _, localIssue := range localIssues {
		if localIssue == nil {
			continue
		}
		if localID := strings.TrimSpace(localIssue.ID); localID != "" {
			localByID[localID] = localIssue
		}
		if localIssue == nil || localIssue.ExternalRef == nil {
			continue
		}
		localRef := strings.TrimSpace(*localIssue.ExternalRef)
		if localRef == "" || !e.Tracker.IsExternalRef(localRef) {
			continue
		}
		identifier := e.Tracker.ExtractIdentifier(localRef)
		if identifier == "" {
			continue
		}
		localByExternalIdentifier[identifier] = localIssue
	}

	// Fetch issues from external tracker
	extIssues, err := e.Tracker.FetchIssues(ctx, fetchOpts)
	if err != nil {
		return nil, fmt.Errorf("fetching issues: %w", err)
	}
	stats.Candidates = len(extIssues)
	if provider, ok := e.Tracker.(PullStatsProvider); ok {
		stats.Queried, stats.Candidates = provider.LastPullStats()
	}

	mapper := e.Tracker.FieldMapper()
	var pendingDeps []DependencyInfo

	for _, extIssue := range extIssues {
		// ShouldImport hook: filter before conversion
		if e.PullHooks != nil && e.PullHooks.ShouldImport != nil {
			if !e.PullHooks.ShouldImport(&extIssue) {
				stats.Skipped++
				continue
			}
		}

		// Check if we already have this issue before dry-run so preview stats
		// distinguish creates from updates.
		ref := e.Tracker.BuildExternalRef(&extIssue)
		existing, _ := e.Store.GetIssueByExternalRef(ctx, ref)
		if existing == nil && ref != "" {
			identifier := e.Tracker.ExtractIdentifier(ref)
			if identifier != "" {
				existing = localByExternalIdentifier[identifier]
			}
		}
		conv := mapper.IssueToBeads(&extIssue)
		if conv == nil || conv.Issue == nil {
			stats.Skipped++
			continue
		}
		if existing == nil {
			if localID := strings.TrimSpace(conv.Issue.ID); localID != "" {
				existing = localByID[localID]
			}
		}

		// TransformIssue hook: description formatting, field normalization
		if e.PullHooks != nil && e.PullHooks.TransformIssue != nil {
			e.PullHooks.TransformIssue(conv.Issue)
		}

		// GenerateID hook: hash-based ID generation
		if e.PullHooks != nil && e.PullHooks.GenerateID != nil {
			if err := e.PullHooks.GenerateID(ctx, conv.Issue); err != nil {
				e.warn("Failed to generate ID for %s: %v", extIssue.Identifier, err)
				stats.Skipped++
				continue
			}
		}

		if existing != nil && pullIssueEqual(existing, conv.Issue, ref) {
			stats.Skipped++
			continue
		}

		if opts.DryRun {
			if existing != nil {
				e.msg("[dry-run] Would update local issue: %s - %s", extIssue.Identifier, extIssue.Title)
				stats.Updated++
			} else {
				e.msg("[dry-run] Would import: %s - %s", extIssue.Identifier, extIssue.Title)
				stats.Created++
			}
			continue
		}

		if existing != nil {
			// Conflict-aware pull: skip updating issues that were locally
			// modified since last sync. Conflict detection (Phase 2) will
			// handle these per the configured resolution strategy.
			// Without this guard, pull silently overwrites local changes
			// before conflict detection can compare timestamps.
			if lastSync != nil && existing.UpdatedAt.After(*lastSync) && !allowOverwriteIDs[existing.ID] {
				stats.Skipped++
				continue
			}

			updates := buildPullIssueUpdates(existing, conv.Issue, ref)
			if raw, ok := marshalTrackerMetadata(extIssue.Metadata); ok {
				updates["metadata"] = raw
			}

			if err := e.Store.RunInTransaction(ctx, fmt.Sprintf("bd: pull update %s", existing.ID), func(tx storage.Transaction) error {
				if err := tx.UpdateIssue(ctx, existing.ID, updates, e.Actor); err != nil {
					return err
				}
				return syncIssueLabels(ctx, tx, existing.ID, conv.Issue.Labels, e.Actor)
			}); err != nil {
				e.warn("Failed to update %s: %v", existing.ID, err)
				continue
			}
			stats.Updated++
		} else {
			// Create new issue
			conv.Issue.ExternalRef = strPtr(ref)
			if raw, ok := marshalTrackerMetadata(extIssue.Metadata); ok {
				conv.Issue.Metadata = raw
			}
			if err := e.Store.CreateIssue(ctx, conv.Issue, e.Actor); err != nil {
				e.warn("Failed to create issue for %s: %v", extIssue.Identifier, err)
				continue
			}
			stats.Created++
		}

		pendingDeps = append(pendingDeps, conv.Dependencies...)
	}

	// Create dependencies after all issues are imported
	e.createDependencies(ctx, pendingDeps)

	span.SetAttributes(
		attribute.Int("sync.created", stats.Created),
		attribute.Int("sync.updated", stats.Updated),
		attribute.Int("sync.skipped", stats.Skipped),
	)
	return stats, nil
}

func pullIssueEqual(local *types.Issue, remote *types.Issue, ref string) bool {
	if local == nil || remote == nil {
		return false
	}
	if local.Title != remote.Title ||
		local.Description != remote.Description ||
		local.Priority != remote.Priority ||
		local.Status != remote.Status ||
		local.IssueType != remote.IssueType ||
		strings.TrimSpace(local.Assignee) != strings.TrimSpace(remote.Assignee) ||
		!equalNormalizedStrings(local.Labels, remote.Labels) {
		return false
	}
	localRef := ""
	if local.ExternalRef != nil {
		localRef = strings.TrimSpace(*local.ExternalRef)
	}
	return localRef == strings.TrimSpace(ref)
}

func buildPullIssueUpdates(existing *types.Issue, remote *types.Issue, ref string) map[string]interface{} {
	updates := map[string]interface{}{
		"title":       remote.Title,
		"description": remote.Description,
		"priority":    remote.Priority,
		"status":      string(remote.Status),
		"issue_type":  string(remote.IssueType),
		"assignee":    remote.Assignee,
	}
	trimmedRef := strings.TrimSpace(ref)
	if trimmedRef == "" {
		return updates
	}
	if existing.ExternalRef == nil || strings.TrimSpace(*existing.ExternalRef) != trimmedRef {
		updates["external_ref"] = trimmedRef
	}
	return updates
}

func marshalTrackerMetadata(metadata interface{}) (json.RawMessage, bool) {
	if metadata == nil {
		return nil, false
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return nil, false
	}
	return json.RawMessage(raw), true
}

func syncIssueLabels(ctx context.Context, tx storage.Transaction, issueID string, desired []string, actor string) error {
	current, err := tx.GetLabels(ctx, issueID)
	if err != nil {
		return err
	}
	currentSet := normalizedStringSet(current)
	desiredSet := normalizedStringSet(desired)
	for label := range currentSet {
		if _, ok := desiredSet[label]; ok {
			continue
		}
		if err := tx.RemoveLabel(ctx, issueID, label, actor); err != nil {
			return err
		}
	}
	for label := range desiredSet {
		if _, ok := currentSet[label]; ok {
			continue
		}
		if err := tx.AddLabel(ctx, issueID, label, actor); err != nil {
			return err
		}
	}
	return nil
}

func equalNormalizedStrings(a, b []string) bool {
	an := normalizedStringSlice(a)
	bn := normalizedStringSlice(b)
	if len(an) != len(bn) {
		return false
	}
	for i := range an {
		if an[i] != bn[i] {
			return false
		}
	}
	return true
}

func normalizedStringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		result[value] = struct{}{}
	}
	return result
}

func normalizedStringSlice(values []string) []string {
	set := normalizedStringSet(values)
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func parseSyncTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, fmt.Errorf("empty sync timestamp")
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed, nil
	}
	return time.Parse(time.RFC3339, value)
}

// doPush exports beads issues to the external tracker.
func (e *Engine) doPush(ctx context.Context, opts SyncOptions, skipIDs, forceIDs map[string]bool) (*PushStats, error) {
	ctx, span := syncTracer.Start(ctx, "tracker.push",
		trace.WithAttributes(
			attribute.String("sync.tracker", e.Tracker.DisplayName()),
			attribute.Bool("sync.dry_run", opts.DryRun),
		),
	)
	defer span.End()

	stats := &PushStats{}

	// BuildStateCache hook: pre-cache workflow states once before the loop.
	// Stored on Engine so tracker adapters can call ResolveState() during push.
	e.stateCache = nil
	if e.PushHooks != nil && e.PushHooks.BuildStateCache != nil {
		var err error
		e.stateCache, err = e.PushHooks.BuildStateCache(ctx)
		if err != nil {
			return nil, fmt.Errorf("building state cache: %w", err)
		}
	}

	// Fetch local issues
	filter := types.IssueFilter{}
	issues, err := e.Store.SearchIssues(ctx, "", filter)
	if err != nil {
		return nil, fmt.Errorf("searching local issues: %w", err)
	}

	// Build descendant set if --parent was specified.
	var descendantSet map[string]bool
	if opts.ParentID != "" {
		descendantSet, err = e.buildDescendantSet(ctx, opts.ParentID)
		if err != nil {
			return nil, fmt.Errorf("resolving parent %s: %w", opts.ParentID, err)
		}
	}

	if batchTracker, ok := e.Tracker.(BatchPushTracker); ok {
		pushIssues, skipped := e.collectBatchPushIssues(issues, opts, descendantSet, skipIDs, forceIDs)
		stats.Skipped += skipped
		if len(pushIssues) == 0 {
			return stats, nil
		}
		if opts.DryRun {
			if dryRunner, ok := e.Tracker.(BatchPushDryRunner); ok {
				batchResult, err := dryRunner.BatchPushDryRun(ctx, pushIssues, forceIDs)
				if err != nil {
					return nil, fmt.Errorf("previewing batch push: %w", err)
				}
				e.renderBatchDryRun(pushIssues, batchResult)
				stats.Created += len(batchResult.Created)
				stats.Updated += len(batchResult.Updated)
				stats.Skipped += len(batchResult.Skipped)
				stats.Errors += len(batchResult.Errors)
				stats.Warnings = append(stats.Warnings, batchResult.Warnings...)
				for _, item := range batchResult.Errors {
					if item.LocalID != "" {
						e.warn("Failed to preview push %s in %s: %s", item.LocalID, e.Tracker.DisplayName(), item.Message)
						continue
					}
					e.warn("Failed to preview pushes in %s: %s", e.Tracker.DisplayName(), item.Message)
				}
				return stats, nil
			}
		} else {
			batchResult, err := batchTracker.BatchPush(ctx, pushIssues, forceIDs)
			if err != nil {
				return nil, fmt.Errorf("batch pushing issues: %w", err)
			}
			e.applyBatchPushResult(ctx, batchResult)
			stats.Created += len(batchResult.Created)
			stats.Updated += len(batchResult.Updated)
			stats.Skipped += len(batchResult.Skipped)
			stats.Errors += len(batchResult.Errors)
			stats.Warnings = append(stats.Warnings, batchResult.Warnings...)
			for _, item := range batchResult.Errors {
				if item.LocalID != "" {
					e.warn("Failed to push %s in %s: %s", item.LocalID, e.Tracker.DisplayName(), item.Message)
					continue
				}
				e.warn("Failed to push issues in %s: %s", e.Tracker.DisplayName(), item.Message)
			}
			return stats, nil
		}
	}

	for _, issue := range issues {
		// Limit to parent and its descendants if requested.
		if descendantSet != nil && !descendantSet[issue.ID] {
			stats.Skipped++
			continue
		}
		// Skip filtered types/states/ephemeral
		if !e.shouldPushIssue(issue, opts) {
			stats.Skipped++
			continue
		}

		// Skip issues matching exclude ID patterns
		if isExcludedByIDPattern(issue.ID, opts.ExcludeIDPatterns) {
			stats.Skipped++
			continue
		}

		// ShouldPush hook: custom filtering (prefix filtering, etc.)
		if e.PushHooks != nil && e.PushHooks.ShouldPush != nil {
			if !e.PushHooks.ShouldPush(issue) {
				stats.Skipped++
				continue
			}
		}

		// Skip conflict-excluded issues
		if skipIDs[issue.ID] {
			stats.Skipped++
			continue
		}

		extRef := derefStr(issue.ExternalRef)
		willCreate := extRef == "" || !e.Tracker.IsExternalRef(extRef)

		if opts.DryRun {
			if willCreate {
				e.msg("[dry-run] Would create in %s: %s", e.Tracker.DisplayName(), issue.Title)
				stats.Created++
			} else {
				e.msg("[dry-run] Would update in %s: %s", e.Tracker.DisplayName(), issue.Title)
				stats.Updated++
			}
			continue
		}

		// FormatDescription hook: apply to a copy so we don't mutate local data.
		pushIssue := issue
		if e.PushHooks != nil && e.PushHooks.FormatDescription != nil {
			copy := *issue
			copy.Description = e.PushHooks.FormatDescription(issue)
			pushIssue = &copy
		}

		if willCreate {
			// Create in external tracker
			created, err := e.Tracker.CreateIssue(ctx, pushIssue)
			if err != nil {
				e.warn("Failed to create %s in %s: %v", issue.ID, e.Tracker.DisplayName(), err)
				stats.Errors++
				continue
			}

			// Update local issue with external ref
			ref := e.Tracker.BuildExternalRef(created)
			updates := map[string]interface{}{"external_ref": ref}
			if err := e.Store.UpdateIssue(ctx, issue.ID, updates, e.Actor); err != nil {
				e.warn("Failed to update external_ref for %s: %v", issue.ID, err)
			}
			stats.Created++
		} else if !opts.CreateOnly || forceIDs[issue.ID] {
			// Update existing external issue
			extID := e.Tracker.ExtractIdentifier(extRef)
			if extID == "" {
				stats.Skipped++
				continue
			}

			// Check if update is needed
			if !forceIDs[issue.ID] {
				extIssue, err := e.Tracker.FetchIssue(ctx, extID)
				if err == nil && extIssue != nil {
					// ContentEqual hook: content-hash dedup to skip unnecessary API calls
					if e.PushHooks != nil && e.PushHooks.ContentEqual != nil {
						if e.PushHooks.ContentEqual(issue, extIssue) {
							stats.Skipped++
							continue
						}
					} else if !extIssue.UpdatedAt.Before(issue.UpdatedAt) {
						stats.Skipped++ // Default: external is same or newer
						continue
					}
				}
			}

			if _, err := e.Tracker.UpdateIssue(ctx, extID, pushIssue); err != nil {
				e.warn("Failed to update %s in %s: %v", issue.ID, e.Tracker.DisplayName(), err)
				stats.Errors++
				continue
			}
			stats.Updated++
		} else {
			stats.Skipped++
		}
	}

	span.SetAttributes(
		attribute.Int("sync.created", stats.Created),
		attribute.Int("sync.updated", stats.Updated),
		attribute.Int("sync.skipped", stats.Skipped),
		attribute.Int("sync.errors", stats.Errors),
	)
	return stats, nil
}

func (e *Engine) collectBatchPushIssues(issues []*types.Issue, opts SyncOptions, descendantSet, skipIDs, forceIDs map[string]bool) ([]*types.Issue, int) {
	pushIssues := make([]*types.Issue, 0, len(issues))
	skipped := 0
	for _, issue := range issues {
		if descendantSet != nil && !descendantSet[issue.ID] {
			skipped++
			continue
		}
		if !e.shouldPushIssue(issue, opts) {
			skipped++
			continue
		}
		if e.PushHooks != nil && e.PushHooks.ShouldPush != nil && !e.PushHooks.ShouldPush(issue) {
			skipped++
			continue
		}
		if skipIDs[issue.ID] {
			skipped++
			continue
		}

		extRef := derefStr(issue.ExternalRef)
		willCreate := extRef == "" || !e.Tracker.IsExternalRef(extRef)
		if !willCreate && opts.CreateOnly && !forceIDs[issue.ID] {
			skipped++
			continue
		}
		pushIssues = append(pushIssues, e.formatPushIssue(issue))
	}
	return pushIssues, skipped
}

func (e *Engine) formatPushIssue(issue *types.Issue) *types.Issue {
	if e.PushHooks == nil || e.PushHooks.FormatDescription == nil {
		return issue
	}
	copy := *issue
	copy.Description = e.PushHooks.FormatDescription(issue)
	return &copy
}

func (e *Engine) applyBatchPushResult(ctx context.Context, result *BatchPushResult) {
	if result == nil {
		return
	}
	items := append(append([]BatchPushItem(nil), result.Created...), result.Updated...)
	for _, item := range items {
		if item.LocalID == "" || strings.TrimSpace(item.ExternalRef) == "" {
			continue
		}
		updates := map[string]interface{}{"external_ref": strings.TrimSpace(item.ExternalRef)}
		if err := e.Store.UpdateIssue(ctx, item.LocalID, updates, e.Actor); err != nil {
			e.warn("Failed to update external_ref for %s: %v", item.LocalID, err)
		}
	}
}

func (e *Engine) renderBatchDryRun(issues []*types.Issue, result *BatchPushResult) {
	if result == nil {
		return
	}
	titles := make(map[string]string, len(issues))
	for _, issue := range issues {
		if issue == nil || issue.ID == "" {
			continue
		}
		titles[issue.ID] = issue.Title
	}
	for _, item := range result.Created {
		e.msg("[dry-run] Would create in %s: %s", e.Tracker.DisplayName(), titles[item.LocalID])
	}
	for _, item := range result.Updated {
		e.msg("[dry-run] Would update in %s: %s", e.Tracker.DisplayName(), titles[item.LocalID])
	}
}

// resolveConflicts applies the configured conflict resolution strategy.
func (e *Engine) resolveConflicts(opts SyncOptions, conflicts []Conflict, skipIDs, forceIDs, allowPullOverwriteIDs map[string]bool) {
	for _, c := range conflicts {
		switch opts.ConflictResolution {
		case ConflictLocal:
			forceIDs[c.IssueID] = true
			e.msg("Conflict on %s: keeping local version", c.IssueID)

		case ConflictExternal:
			skipIDs[c.IssueID] = true
			allowPullOverwriteIDs[c.IssueID] = true
			e.msg("Conflict on %s: keeping external version", c.IssueID)

		default: // ConflictTimestamp or unset
			if c.LocalUpdated.After(c.ExternalUpdated) {
				forceIDs[c.IssueID] = true
				e.msg("Conflict on %s: local is newer, pushing", c.IssueID)
			} else {
				skipIDs[c.IssueID] = true
				allowPullOverwriteIDs[c.IssueID] = true
				e.msg("Conflict on %s: external is newer, importing", c.IssueID)
			}
		}
	}
}

// reimportIssue fetches the external version and updates the local issue.
func (e *Engine) reimportIssue(ctx context.Context, c Conflict) {
	extIssue, err := e.Tracker.FetchIssue(ctx, c.ExternalIdentifier)
	if err != nil || extIssue == nil {
		e.warn("Failed to re-import %s: %v", c.IssueID, err)
		return
	}

	conv := e.Tracker.FieldMapper().IssueToBeads(extIssue)
	if conv == nil || conv.Issue == nil {
		return
	}

	updates := map[string]interface{}{
		"title":       conv.Issue.Title,
		"description": conv.Issue.Description,
		"priority":    conv.Issue.Priority,
		"status":      string(conv.Issue.Status),
	}
	if extIssue.Metadata != nil {
		if raw, err := json.Marshal(extIssue.Metadata); err == nil {
			updates["metadata"] = json.RawMessage(raw)
		}
	}

	if err := e.Store.UpdateIssue(ctx, c.IssueID, updates, e.Actor); err != nil {
		e.warn("Failed to update %s during reimport: %v", c.IssueID, err)
	}
}

// createDependencies creates dependencies from the pending list, matching
// external IDs to local issue IDs.
func (e *Engine) createDependencies(ctx context.Context, deps []DependencyInfo) {
	if len(deps) == 0 {
		return
	}

	for _, dep := range deps {
		fromIssue, _ := e.Store.GetIssueByExternalRef(ctx, dep.FromExternalID)
		toIssue, _ := e.Store.GetIssueByExternalRef(ctx, dep.ToExternalID)

		if fromIssue == nil || toIssue == nil {
			continue
		}

		d := &types.Dependency{
			IssueID:     fromIssue.ID,
			DependsOnID: toIssue.ID,
			Type:        types.DependencyType(dep.Type),
		}
		if err := e.Store.AddDependency(ctx, d, e.Actor); err != nil {
			e.warn("Failed to create dependency %s -> %s: %v", fromIssue.ID, toIssue.ID, err)
		}
	}
}

// buildDescendantSet returns the set of issue IDs consisting of the given parent
// and all its transitive descendants via parent-child dependencies.
func (e *Engine) buildDescendantSet(ctx context.Context, parentID string) (map[string]bool, error) {
	result := map[string]bool{parentID: true}
	queue := []string{parentID}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		dependents, err := e.Store.GetDependentsWithMetadata(ctx, current)
		if err != nil {
			return nil, fmt.Errorf("getting dependents of %s: %w", current, err)
		}
		for _, dep := range dependents {
			if dep.DependencyType == types.DepParentChild && !result[dep.Issue.ID] {
				result[dep.Issue.ID] = true
				queue = append(queue, dep.Issue.ID)
			}
		}
	}
	return result, nil
}

// shouldPushIssue checks if an issue should be included in push based on filters.
func (e *Engine) shouldPushIssue(issue *types.Issue, opts SyncOptions) bool {
	// Skip ephemeral issues (wisps, etc.) if requested
	if opts.ExcludeEphemeral && issue.Ephemeral {
		return false
	}

	if len(opts.TypeFilter) > 0 {
		found := false
		for _, t := range opts.TypeFilter {
			if issue.IssueType == t {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	for _, t := range opts.ExcludeTypes {
		if issue.IssueType == t {
			return false
		}
	}

	if opts.State == "open" && issue.Status == types.StatusClosed {
		return false
	}

	return true
}

// isExcludedByIDPattern returns true if the issue ID matches any of the
// configured exclude patterns (substring match).
func isExcludedByIDPattern(issueID string, patterns []string) bool {
	for _, pattern := range patterns {
		if pattern != "" && strings.Contains(issueID, pattern) {
			return true
		}
	}
	return false
}

// ResolveState maps a beads status to a tracker state ID using the push state cache.
// Returns (stateID, ok). Only usable during a push operation after BuildStateCache has run.
func (e *Engine) ResolveState(status types.Status) (string, bool) {
	if e.PushHooks == nil || e.PushHooks.ResolveState == nil || e.stateCache == nil {
		return "", false
	}
	return e.PushHooks.ResolveState(e.stateCache, status)
}

// commentSyncStats tracks comment sync results.
type commentSyncStats struct {
	Pulled     int
	Pushed     int
	PullErrors int
}

// attachmentPullStats tracks attachment pull results.
type attachmentPullStats struct {
	Pulled     int
	PullErrors int
}

// buildExtIssueCache builds a map of beads issueID -> TrackerIssue for all
// local issues that have an external_ref belonging to this tracker. This is
// called once and shared by comment sync and attachment pull to eliminate
// redundant FetchIssue API calls.
func (e *Engine) buildExtIssueCache(ctx context.Context) map[string]*TrackerIssue {
	cache := make(map[string]*TrackerIssue)

	filter := types.IssueFilter{}
	issues, err := e.Store.SearchIssues(ctx, "", filter)
	if err != nil {
		e.warn("buildExtIssueCache: failed to search issues: %v", err)
		return cache
	}

	for _, issue := range issues {
		extRef := derefStr(issue.ExternalRef)
		if extRef == "" || !e.Tracker.IsExternalRef(extRef) {
			continue
		}
		extID := e.Tracker.ExtractIdentifier(extRef)
		if extID == "" {
			continue
		}
		extIssue, fetchErr := e.Tracker.FetchIssue(ctx, extID)
		if fetchErr != nil || extIssue == nil {
			continue
		}
		cache[issue.ID] = extIssue
	}
	return cache
}

// doCommentSync synchronizes comments between beads and the external tracker.
// Pull: For issues with external_ref, fetch remote comments and create locally if not found.
// Push: For local comments without external_ref, create in the tracker and store the returned ID.
func (e *Engine) doCommentSync(ctx context.Context, opts SyncOptions, syncer CommentSyncer, extIssueCache map[string]*TrackerIssue) commentSyncStats {
	ctx, span := syncTracer.Start(ctx, "tracker.comment_sync",
		trace.WithAttributes(attribute.String("sync.tracker", e.Tracker.DisplayName())),
	)
	defer span.End()

	stats := commentSyncStats{}

	// Get last comment sync time
	var since time.Time
	key := e.Tracker.ConfigPrefix() + ".last_comment_sync"
	if lastSyncStr, err := e.Store.GetConfig(ctx, key); err == nil && lastSyncStr != "" {
		if t, err := time.Parse(time.RFC3339, lastSyncStr); err == nil {
			since = t
		}
	}

	// Find issues with external refs for this tracker
	filter := types.IssueFilter{}
	issues, err := e.Store.SearchIssues(ctx, "", filter)
	if err != nil {
		e.warn("Comment sync: failed to search issues: %v", err)
		return stats
	}

	for _, issue := range issues {
		extRef := derefStr(issue.ExternalRef)
		if extRef == "" || !e.Tracker.IsExternalRef(extRef) {
			continue
		}

		// Skip issues matching exclude ID patterns
		if isExcludedByIDPattern(issue.ID, opts.ExcludeIDPatterns) {
			continue
		}

		// Use the pre-built cache to avoid redundant FetchIssue calls.
		extIssue := extIssueCache[issue.ID]
		if extIssue == nil {
			continue
		}

		// Fetch remote comments once per issue for both pull and push phases.
		// The push phase uses these for text-based dedup as a safety net.
		// Determine effective cutoff for this issue. If the issue has never
		// had comments synced (no local comments with an external_ref), use
		// zero time to fetch all comments. This prevents suppressing
		// preexisting comments on issues newly entering sync.
		issueSince := since
		if !since.IsZero() {
			hasExtRefComment := false
			existingComments, cerr := e.Store.GetIssueComments(ctx, issue.ID)
			if cerr == nil {
				for _, ec := range existingComments {
					if ec.ExternalRef != "" {
						hasExtRefComment = true
						break
					}
				}
			}
			if !hasExtRefComment {
				issueSince = time.Time{} // zero — fetch all
			}
		}

		var remoteComments []TrackerComment
		remoteComments, err = syncer.FetchComments(ctx, extIssue.ID, issueSince)
		if err != nil {
			e.warn("Comment sync: failed to fetch comments for %s: %v", issue.ID, err)
			continue
		}

		// Build a set of remote comment texts for text-based dedup during push.
		// This prevents re-pushing a comment that already exists in the tracker
		// (e.g., when a previous push succeeded but the local external_ref
		// update failed, leaving the comment without an external_ref).
		remoteTextSet := make(map[string]string) // normalized text → external comment ID
		for _, rc := range remoteComments {
			remoteTextSet[normalizeCommentText(rc.Body)] = rc.ID
		}

		// PULL: Import remote comments that are missing locally
		didPull := opts.Pull || (!opts.Pull && !opts.Push)
		if didPull {
			for _, rc := range remoteComments {
				if opts.DryRun {
					e.msg("[dry-run] Would import comment from %s on %s", rc.Author, issue.ID)
					stats.Pulled++
					continue
				}

				// Check if we already have this comment by external_ref
				commentRef := e.Tracker.ConfigPrefix() + ":" + rc.ID
				existing := e.getCommentByExternalRef(ctx, issue.ID, commentRef)
				if existing != nil {
					// Update local comment if remote was edited (different body)
					if rc.Body != existing.Text {
						if err := e.updateCommentText(ctx, issue.ID, existing.ID, rc.Body); err != nil {
							e.warn("Comment sync: failed to update edited comment on %s: %v", issue.ID, err)
						}
					}
					continue // Already imported
				}

				// Import the comment
				if err := e.importComment(ctx, issue.ID, rc.Author, rc.Body, commentRef, rc.CreatedAt); err != nil {
					e.warn("Comment sync: failed to import comment on %s: %v", issue.ID, err)
					stats.PullErrors++
					continue
				}
				stats.Pulled++
			}
		}

		// PUSH: Push local comments without external_ref
		if opts.Push || (!opts.Pull && !opts.Push) {
			localComments, err := e.Store.GetIssueComments(ctx, issue.ID)
			if err != nil {
				e.warn("Comment sync: failed to get local comments for %s: %v", issue.ID, err)
				continue
			}

			for _, lc := range localComments {
				if lc.ExternalRef != "" {
					continue // Already synced
				}

				if opts.DryRun {
					e.msg("[dry-run] Would push comment from %s on %s", lc.Author, issue.ID)
					stats.Pushed++
					continue
				}

				// Safety check: if a comment with the same text already exists
				// in the remote tracker, adopt its ID instead of creating a
				// duplicate. This catches cases where a previous push created
				// the comment in the tracker but the local external_ref update
				// failed (crash, DB error, etc.).
				normalizedLocal := normalizeCommentText(lc.Text)
				if existingExtID, found := remoteTextSet[normalizedLocal]; found {
					commentRef := e.Tracker.ConfigPrefix() + ":" + existingExtID
					if err := e.updateCommentExternalRef(ctx, issue.ID, lc.ID, commentRef); err != nil {
						e.warn("Comment sync: failed to adopt existing remote comment ref on %s: %v", issue.ID, err)
					} else {
						e.msg("Comment sync: adopted existing remote comment for %s (text-based dedup)", issue.ID)
					}
					// Remove from remote set so a second local comment with
					// identical text doesn't also match this same remote comment.
					delete(remoteTextSet, normalizedLocal)
					continue
				}

				// Create in external tracker
				extCommentID, err := syncer.CreateComment(ctx, extIssue.ID, lc.Text)
				if err != nil {
					e.warn("Comment sync: failed to push comment on %s: %v", issue.ID, err)
					continue
				}

				// Update local comment with external_ref. If this fails, the
				// comment already exists in the tracker — next sync will catch
				// it via text-based dedup above instead of creating a duplicate.
				commentRef := e.Tracker.ConfigPrefix() + ":" + extCommentID
				if err := e.updateCommentExternalRef(ctx, issue.ID, lc.ID, commentRef); err != nil {
					e.warn("Comment sync: CRITICAL — pushed comment to %s but failed to save external_ref (comment %s, ref %s): %v. "+
						"Text-based dedup will prevent duplication on next sync.", issue.ID, lc.ID, commentRef, err)
				}
				stats.Pushed++
			}
		}
	}

	// Only advance last_comment_sync when a pull actually happened and
	// there were no pull errors. Advancing on push-only or partial failures
	// would skip remote comments that were never imported.
	didPullPhase := opts.Pull || (!opts.Pull && !opts.Push)
	if !opts.DryRun && didPullPhase && stats.PullErrors == 0 {
		lastSync := time.Now().UTC().Format(time.RFC3339)
		if err := e.Store.SetConfig(ctx, key, lastSync); err != nil {
			e.warn("Failed to update last_comment_sync: %v", err)
		}
	}

	span.SetAttributes(
		attribute.Int("sync.comments_pulled", stats.Pulled),
		attribute.Int("sync.comments_pushed", stats.Pushed),
	)
	return stats
}

// doAttachmentPull fetches attachment metadata from the external tracker and stores locally.
func (e *Engine) doAttachmentPull(ctx context.Context, opts SyncOptions, fetcher AttachmentFetcher, extIssueCache map[string]*TrackerIssue) attachmentPullStats {
	ctx, span := syncTracer.Start(ctx, "tracker.attachment_pull",
		trace.WithAttributes(attribute.String("sync.tracker", e.Tracker.DisplayName())),
	)
	defer span.End()

	stats := attachmentPullStats{}

	// Find issues with external refs for this tracker
	filter := types.IssueFilter{}
	issues, err := e.Store.SearchIssues(ctx, "", filter)
	if err != nil {
		e.warn("Attachment pull: failed to search issues: %v", err)
		return stats
	}

	for _, issue := range issues {
		extRef := derefStr(issue.ExternalRef)
		if extRef == "" || !e.Tracker.IsExternalRef(extRef) {
			continue
		}

		// Skip issues matching exclude ID patterns
		if isExcludedByIDPattern(issue.ID, opts.ExcludeIDPatterns) {
			continue
		}

		// Use the pre-built cache to avoid redundant FetchIssue calls.
		extIssue := extIssueCache[issue.ID]
		if extIssue == nil {
			continue
		}

		remoteAttachments, err := fetcher.FetchAttachments(ctx, extIssue.ID)
		if err != nil {
			e.warn("Attachment pull: failed to fetch attachments for %s: %v", issue.ID, err)
			stats.PullErrors++
			continue
		}

		for _, ra := range remoteAttachments {
			if opts.DryRun {
				e.msg("[dry-run] Would import attachment %q on %s", ra.Filename, issue.ID)
				stats.Pulled++
				continue
			}

			// Check if we already have this attachment by external_ref
			attRef := e.Tracker.ConfigPrefix() + ":" + ra.ID
			existing := e.getAttachmentByExternalRef(ctx, issue.ID, attRef)
			if existing != nil {
				continue // Already imported
			}

			// Store the attachment metadata
			att := &types.Attachment{
				IssueID:     issue.ID,
				ExternalRef: attRef,
				Filename:    ra.Filename,
				URL:         ra.URL,
				MimeType:    ra.MimeType,
				SizeBytes:   ra.SizeBytes,
				Source:      e.Tracker.Name(),
				Creator:     ra.Creator,
				CreatedAt:   ra.CreatedAt,
			}
			if err := e.createAttachment(ctx, att); err != nil {
				e.warn("Attachment pull: failed to create attachment on %s: %v", issue.ID, err)
				stats.PullErrors++
				continue
			}
			stats.Pulled++
		}
	}

	// Only advance last_attachment_sync when there were no pull errors.
	// Advancing on partial failures would skip remote attachments that were
	// never imported.
	if !opts.DryRun && stats.PullErrors == 0 {
		key := e.Tracker.ConfigPrefix() + ".last_attachment_sync"
		lastSync := time.Now().UTC().Format(time.RFC3339)
		if err := e.Store.SetConfig(ctx, key, lastSync); err != nil {
			e.warn("Failed to update last_attachment_sync: %v", err)
		}
	}

	span.SetAttributes(attribute.Int("sync.attachments_pulled", stats.Pulled))
	return stats
}

// getCommentByExternalRef looks up a comment by external_ref.
// Uses CommentRefStore if available, otherwise falls back to iterating comments.
func (e *Engine) getCommentByExternalRef(ctx context.Context, issueID, externalRef string) *types.Comment {
	if crs, ok := e.Store.(storage.CommentRefStore); ok {
		c, _ := crs.GetCommentByExternalRef(ctx, issueID, externalRef)
		return c
	}
	// Fallback: iterate all comments and match by external_ref
	comments, err := e.Store.GetIssueComments(ctx, issueID)
	if err != nil {
		return nil
	}
	for _, c := range comments {
		if c.ExternalRef == externalRef {
			return c
		}
	}
	return nil
}

// importComment creates a comment with an external_ref.
// Uses CommentRefStore if available, otherwise falls back to basic import.
func (e *Engine) importComment(ctx context.Context, issueID, author, text, externalRef string, createdAt time.Time) error {
	if crs, ok := e.Store.(storage.CommentRefStore); ok {
		_, err := crs.ImportCommentWithRef(ctx, issueID, author, text, externalRef, createdAt)
		return err
	}
	// Fallback: import without external_ref (dedup will rely on text matching)
	return e.Store.RunInTransaction(ctx, "comment sync: import", func(tx storage.Transaction) error {
		_, err := tx.ImportIssueComment(ctx, issueID, author, text, createdAt)
		return err
	})
}

// updateCommentExternalRef updates the external_ref field on a local comment.
// Uses CommentRefStore if available, otherwise is a no-op.
func (e *Engine) updateCommentExternalRef(ctx context.Context, issueID, commentID, externalRef string) error {
	if crs, ok := e.Store.(storage.CommentRefStore); ok {
		return crs.UpdateCommentExternalRef(ctx, issueID, commentID, externalRef)
	}
	return nil
}

// updateCommentText updates the text of an existing comment (for edited remote comments).
// Uses CommentRefStore (which implies UpdateCommentText support) if available, otherwise is a no-op.
func (e *Engine) updateCommentText(ctx context.Context, issueID, commentID, newText string) error {
	type commentTextUpdater interface {
		UpdateCommentText(ctx context.Context, issueID, commentID, newText string) error
	}
	if ctu, ok := e.Store.(commentTextUpdater); ok {
		return ctu.UpdateCommentText(ctx, issueID, commentID, newText)
	}
	return nil
}

// getAttachmentByExternalRef looks up an attachment by external_ref.
// Uses AttachmentStore if available, otherwise returns nil.
func (e *Engine) getAttachmentByExternalRef(ctx context.Context, issueID, externalRef string) *types.Attachment {
	if as, ok := e.Store.(storage.AttachmentStore); ok {
		att, _ := as.GetAttachmentByExternalRef(ctx, issueID, externalRef)
		return att
	}
	return nil
}

// createAttachment stores attachment metadata in the database.
// Uses AttachmentStore if available, otherwise returns nil (skips).
func (e *Engine) createAttachment(ctx context.Context, att *types.Attachment) error {
	if as, ok := e.Store.(storage.AttachmentStore); ok {
		_, err := as.CreateAttachment(ctx, att)
		return err
	}
	return nil
}

// strPtr returns a pointer to the given string.
func strPtr(s string) *string { return &s }

// derefStr safely dereferences a *string, returning "" for nil.
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (e *Engine) msg(format string, args ...interface{}) {
	if e.OnMessage != nil {
		e.OnMessage(fmt.Sprintf(format, args...))
	}
}

func (e *Engine) warn(format string, args ...interface{}) {
	if e.OnWarning != nil {
		e.OnWarning(fmt.Sprintf(format, args...))
	}
}

// normalizeCommentText strips leading/trailing whitespace for text-based
// comment dedup. This handles minor formatting differences between the local
// copy and the version returned by the external tracker.
func normalizeCommentText(text string) string {
	return strings.TrimSpace(text)
}
