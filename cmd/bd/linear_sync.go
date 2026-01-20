package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/linear"
	"github.com/steveyegge/beads/internal/types"
)

// doPullFromLinear imports issues from Linear using the GraphQL API.
// Supports incremental sync by checking linear.last_sync config and only fetching
// issues updated since that timestamp.
func doPullFromLinear(ctx context.Context, dryRun bool, state string, skipLinearIDs map[string]bool) (*linear.PullStats, error) {
	stats := &linear.PullStats{}

	client, err := getLinearClient(ctx)
	if err != nil {
		return stats, fmt.Errorf("failed to create Linear client: %w", err)
	}

	var linearIssues []linear.Issue
	lastSyncStr, _ := store.GetConfig(ctx, "linear.last_sync")

	if lastSyncStr != "" {
		lastSync, err := time.Parse(time.RFC3339, lastSyncStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: invalid linear.last_sync timestamp, doing full sync\n")
			linearIssues, err = client.FetchIssues(ctx, state)
			if err != nil {
				return stats, fmt.Errorf("failed to fetch issues from Linear: %w", err)
			}
		} else {
			stats.Incremental = true
			stats.SyncedSince = lastSyncStr
			linearIssues, err = client.FetchIssuesSince(ctx, state, lastSync)
			if err != nil {
				return stats, fmt.Errorf("failed to fetch issues from Linear (incremental): %w", err)
			}
			if !dryRun {
				fmt.Printf("  Incremental sync since %s\n", lastSync.Format("2006-01-02 15:04:05"))
			}
		}
	} else {
		linearIssues, err = client.FetchIssues(ctx, state)
		if err != nil {
			return stats, fmt.Errorf("failed to fetch issues from Linear: %w", err)
		}
		if !dryRun {
			fmt.Println("  Full sync (no previous sync timestamp)")
		}
	}

	mappingConfig := loadLinearMappingConfig(ctx)

	idMode := getLinearIDMode(ctx)
	hashLength := getLinearHashLength(ctx)

	var beadsIssues []*types.Issue
	var allDeps []linear.DependencyInfo
	linearIDToBeadsID := make(map[string]string)

	for i := range linearIssues {
		conversion := linear.IssueToBeads(&linearIssues[i], mappingConfig)
		beadsIssues = append(beadsIssues, conversion.Issue.(*types.Issue))
		allDeps = append(allDeps, conversion.Dependencies...)
	}

	if len(beadsIssues) == 0 {
		fmt.Println("  No issues to import")
		return stats, nil
	}

	if len(skipLinearIDs) > 0 {
		var filteredIssues []*types.Issue
		skipped := 0
		for _, issue := range beadsIssues {
			if issue.ExternalRef == nil {
				filteredIssues = append(filteredIssues, issue)
				continue
			}
			linearID := linear.ExtractLinearIdentifier(*issue.ExternalRef)
			if linearID != "" && skipLinearIDs[linearID] {
				skipped++
				continue
			}
			filteredIssues = append(filteredIssues, issue)
		}
		if skipped > 0 {
			stats.Skipped += skipped
		}
		beadsIssues = filteredIssues

		if len(allDeps) > 0 {
			var filteredDeps []linear.DependencyInfo
			for _, dep := range allDeps {
				if skipLinearIDs[dep.FromLinearID] || skipLinearIDs[dep.ToLinearID] {
					continue
				}
				filteredDeps = append(filteredDeps, dep)
			}
			allDeps = filteredDeps
		}
	}

	prefix, err := store.GetConfig(ctx, "issue_prefix")
	if err != nil || prefix == "" {
		prefix = "bd"
	}

	if idMode == "hash" {
		existingIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{IncludeTombstones: true})
		if err != nil {
			return stats, fmt.Errorf("failed to fetch existing issues for ID collision avoidance: %w", err)
		}
		usedIDs := make(map[string]bool, len(existingIssues))
		for _, issue := range existingIssues {
			if issue.ID != "" {
				usedIDs[issue.ID] = true
			}
		}

		idOpts := linear.IDGenerationOptions{
			BaseLength: hashLength,
			MaxLength:  8,
			UsedIDs:    usedIDs,
		}
		if err := linear.GenerateIssueIDs(beadsIssues, prefix, "linear-import", idOpts); err != nil {
			return stats, fmt.Errorf("failed to generate issue IDs: %w", err)
		}
	} else if idMode != "db" {
		return stats, fmt.Errorf("unsupported linear.id_mode %q (expected \"hash\" or \"db\")", idMode)
	}

	opts := ImportOptions{
		DryRun:     dryRun,
		SkipUpdate: false,
	}

	result, err := importIssuesCore(ctx, dbPath, store, beadsIssues, opts)
	if err != nil {
		return stats, fmt.Errorf("import failed: %w", err)
	}

	stats.Created = result.Created
	stats.Updated = result.Updated
	stats.Skipped = result.Skipped

	if dryRun {
		if stats.Incremental {
			fmt.Printf("  Would import %d issues from Linear (incremental since %s)\n",
				len(linearIssues), stats.SyncedSince)
		} else {
			fmt.Printf("  Would import %d issues from Linear (full sync)\n", len(linearIssues))
		}
		return stats, nil
	}

	allBeadsIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to fetch issues for dependency mapping: %v\n", err)
		return stats, nil
	}

	for _, issue := range allBeadsIssues {
		if issue.ExternalRef != nil && linear.IsLinearExternalRef(*issue.ExternalRef) {
			linearID := linear.ExtractLinearIdentifier(*issue.ExternalRef)
			if linearID != "" {
				linearIDToBeadsID[linearID] = issue.ID
			}
		}
	}

	depsCreated := 0
	for _, dep := range allDeps {
		fromID, fromOK := linearIDToBeadsID[dep.FromLinearID]
		toID, toOK := linearIDToBeadsID[dep.ToLinearID]

		if !fromOK || !toOK {
			continue
		}

		dependency := &types.Dependency{
			IssueID:     fromID,
			DependsOnID: toID,
			Type:        types.DependencyType(dep.Type),
			CreatedAt:   time.Now(),
		}
		err := store.AddDependency(ctx, dependency, actor)
		if err != nil {
			if !strings.Contains(err.Error(), "already exists") &&
				!strings.Contains(err.Error(), "duplicate") {
				fmt.Fprintf(os.Stderr, "Warning: failed to create dependency %s -> %s (%s): %v\n",
					fromID, toID, dep.Type, err)
			}
		} else {
			depsCreated++
		}
	}

	if depsCreated > 0 {
		fmt.Printf("  Created %d dependencies from Linear relations\n", depsCreated)
	}

	return stats, nil
}

// doPushToLinear exports issues to Linear using the GraphQL API.
// typeFilters includes only issues matching these types (empty means all).
// excludeTypes excludes issues matching these types.
func doPushToLinear(ctx context.Context, dryRun bool, createOnly bool, updateRefs bool, forceUpdateIDs map[string]bool, skipUpdateIDs map[string]bool, typeFilters []string, excludeTypes []string) (*linear.PushStats, error) {
	stats := &linear.PushStats{}

	client, err := getLinearClient(ctx)
	if err != nil {
		return stats, fmt.Errorf("failed to create Linear client: %w", err)
	}

	allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return stats, fmt.Errorf("failed to get local issues: %w", err)
	}

	// Apply type filters
	if len(typeFilters) > 0 || len(excludeTypes) > 0 {
		typeSet := make(map[string]bool, len(typeFilters))
		for _, t := range typeFilters {
			typeSet[strings.ToLower(t)] = true
		}
		excludeSet := make(map[string]bool, len(excludeTypes))
		for _, t := range excludeTypes {
			excludeSet[strings.ToLower(t)] = true
		}

		var filtered []*types.Issue
		for _, issue := range allIssues {
			issueType := strings.ToLower(string(issue.IssueType))

			// If type filters specified, issue must match one
			if len(typeFilters) > 0 && !typeSet[issueType] {
				continue
			}
			// If exclude types specified, issue must not match any
			if excludeSet[issueType] {
				continue
			}
			filtered = append(filtered, issue)
		}
		allIssues = filtered
	}

	var toCreate []*types.Issue
	var toUpdate []*types.Issue

	for _, issue := range allIssues {
		if issue.IsTombstone() {
			continue
		}

		if issue.ExternalRef != nil && linear.IsLinearExternalRef(*issue.ExternalRef) {
			if !createOnly {
				toUpdate = append(toUpdate, issue)
			}
		} else if issue.ExternalRef == nil {
			toCreate = append(toCreate, issue)
		}
	}

	var stateCache *linear.StateCache
	if !dryRun && (len(toCreate) > 0 || (!createOnly && len(toUpdate) > 0)) {
		stateCache, err = linear.BuildStateCache(ctx, client)
		if err != nil {
			return stats, fmt.Errorf("failed to fetch team states: %w", err)
		}
	}

	mappingConfig := loadLinearMappingConfig(ctx)

	for _, issue := range toCreate {
		if dryRun {
			stats.Created++
			continue
		}

		linearPriority := linear.PriorityToLinear(issue.Priority, mappingConfig)
		stateID := stateCache.FindStateForBeadsStatus(issue.Status)

		description := linear.BuildLinearDescription(issue)

		linearIssue, err := client.CreateIssue(ctx, issue.Title, description, linearPriority, stateID, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to create issue '%s' in Linear: %v\n", issue.Title, err)
			stats.Errors++
			continue
		}

		stats.Created++
		fmt.Printf("  Created: %s -> %s\n", issue.ID, linearIssue.Identifier)

		if updateRefs && linearIssue.URL != "" {
			externalRef := linearIssue.URL
			if canonical, ok := linear.CanonicalizeLinearExternalRef(externalRef); ok {
				externalRef = canonical
			}
			updates := map[string]interface{}{
				"external_ref": externalRef,
			}
			if err := store.UpdateIssue(ctx, issue.ID, updates, actor); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to update external_ref for %s: %v\n", issue.ID, err)
				stats.Errors++
			}
		}
	}

	if len(toUpdate) > 0 && !createOnly {
		for _, issue := range toUpdate {
			if skipUpdateIDs != nil && skipUpdateIDs[issue.ID] {
				stats.Skipped++
				continue
			}

			linearIdentifier := linear.ExtractLinearIdentifier(*issue.ExternalRef)
			if linearIdentifier == "" {
				fmt.Fprintf(os.Stderr, "Warning: could not extract Linear identifier from %s: %s\n",
					issue.ID, *issue.ExternalRef)
				stats.Errors++
				continue
			}

			linearIssue, err := client.FetchIssueByIdentifier(ctx, linearIdentifier)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to fetch Linear issue %s: %v\n",
					linearIdentifier, err)
				stats.Errors++
				continue
			}
			if linearIssue == nil {
				fmt.Fprintf(os.Stderr, "Warning: Linear issue %s not found (may have been deleted)\n",
					linearIdentifier)
				stats.Skipped++
				continue
			}

			linearUpdatedAt, err := time.Parse(time.RFC3339, linearIssue.UpdatedAt)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to parse Linear UpdatedAt for %s: %v\n",
					linearIdentifier, err)
				stats.Errors++
				continue
			}

			forcedUpdate := forceUpdateIDs != nil && forceUpdateIDs[issue.ID]
			if !forcedUpdate && !issue.UpdatedAt.After(linearUpdatedAt) {
				stats.Skipped++
				continue
			}

			if !forcedUpdate {
				localComparable := linear.NormalizeIssueForLinearHash(issue)
				linearComparable := linear.IssueToBeads(linearIssue, mappingConfig).Issue.(*types.Issue)
				if localComparable.ComputeContentHash() == linearComparable.ComputeContentHash() {
					stats.Skipped++
					continue
				}
			}

			if dryRun {
				stats.Updated++
				continue
			}

			description := linear.BuildLinearDescription(issue)

			updatePayload := map[string]interface{}{
				"title":       issue.Title,
				"description": description,
			}

			linearPriority := linear.PriorityToLinear(issue.Priority, mappingConfig)
			if linearPriority > 0 {
				updatePayload["priority"] = linearPriority
			}

			stateID := stateCache.FindStateForBeadsStatus(issue.Status)
			if stateID != "" {
				updatePayload["stateId"] = stateID
			}

			updatedLinearIssue, err := client.UpdateIssue(ctx, linearIssue.ID, updatePayload)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to update Linear issue %s: %v\n",
					linearIdentifier, err)
				stats.Errors++
				continue
			}

			stats.Updated++
			fmt.Printf("  Updated: %s -> %s\n", issue.ID, updatedLinearIssue.Identifier)
		}
	}

	if dryRun {
		fmt.Printf("  Would create %d issues in Linear\n", stats.Created)
		if !createOnly {
			fmt.Printf("  Would update %d issues in Linear\n", stats.Updated)
		}
	}

	return stats, nil
}
