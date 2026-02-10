package main

// import_engine.go contains the core issue import logic, formerly in internal/importer/.
// This handles importing issues from any source (JSONL, Jira, GitLab, Linear, etc.)
// into the storage backend.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/linear"
	"github.com/steveyegge/beads/internal/routing"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// importIssuesEngine handles the core import logic used by both manual and auto-import.
func importIssuesEngine(ctx context.Context, _ string, store storage.Storage, issues []*types.Issue, opts ImportOptions) (*ImportResult, error) {
	result := &ImportResult{
		IDMapping:        make(map[string]string),
		MismatchPrefixes: make(map[string]int),
	}

	if store == nil {
		return nil, fmt.Errorf("import requires an initialized storage backend")
	}

	// Normalize Linear external_refs to canonical form to avoid slug-based duplicates.
	for _, issue := range issues {
		if issue.ExternalRef == nil || *issue.ExternalRef == "" {
			continue
		}
		if linear.IsLinearExternalRef(*issue.ExternalRef) {
			if canonical, ok := linear.CanonicalizeLinearExternalRef(*issue.ExternalRef); ok {
				issue.ExternalRef = &canonical
			}
		}
	}

	// Compute content hashes for all incoming issues
	for _, issue := range issues {
		issue.ContentHash = issue.ComputeContentHash()
	}

	// Auto-detect wisps by ID pattern and set ephemeral flag
	for _, issue := range issues {
		if strings.Contains(issue.ID, "-wisp-") && !issue.Ephemeral {
			issue.Ephemeral = true
		}
	}

	// GH#686: In multi-repo mode, skip prefix validation for all issues.
	if config.GetMultiRepoConfig() != nil && !opts.SkipPrefixValidation {
		opts.SkipPrefixValidation = true
	}

	// Read orphan handling from config if not explicitly set
	orphanHandling := storage.OrphanHandling(opts.OrphanHandling)
	if orphanHandling == "" {
		value, err := store.GetConfig(ctx, "import.orphan_handling")
		if err == nil && value != "" {
			switch storage.OrphanHandling(value) {
			case storage.OrphanStrict, storage.OrphanResurrect, storage.OrphanSkip, storage.OrphanAllow:
				orphanHandling = storage.OrphanHandling(value)
			default:
				orphanHandling = storage.OrphanAllow
			}
		} else {
			orphanHandling = storage.OrphanAllow
		}
	}

	// Check and handle prefix mismatches
	var err error
	issues, err = handlePrefixMismatch(ctx, store, issues, opts, orphanHandling, result)
	if err != nil {
		return result, err
	}

	// Validate no duplicate external_ref values in batch
	if err := validateNoDuplicateExternalRefs(issues, opts.ClearDuplicateExternalRefs, result); err != nil {
		return result, err
	}

	// Process deletion markers before issue upserts
	if len(opts.DeletionIDs) > 0 {
		if opts.DryRun {
			for _, id := range opts.DeletionIDs {
				if _, err := store.GetIssue(ctx, id); err == nil {
					result.Deleted++
				}
			}
		} else {
			for _, id := range opts.DeletionIDs {
				if _, err := store.GetIssue(ctx, id); err != nil {
					continue
				}
				if err := store.DeleteIssue(ctx, id); err != nil {
					if opts.Strict {
						return result, fmt.Errorf("failed to delete issue %s: %w", id, err)
					}
					fmt.Fprintf(os.Stderr, "Warning: failed to delete issue %s: %v\n", id, err)
					continue
				}
				result.Deleted++
			}
		}
	}

	// Detect and resolve collisions
	issues, err = engineDetectUpdates(ctx, store, issues, opts, result)
	if err != nil {
		return result, err
	}
	if opts.DryRun {
		return result, nil
	}

	// Apply changes atomically when transactions are supported.
	if err := store.RunInTransaction(ctx, func(tx storage.Transaction) error {
		if err := engineUpsertIssuesTx(ctx, tx, store, issues, opts, orphanHandling, result); err != nil {
			return err
		}
		if err := engineImportDependenciesTx(ctx, tx, issues, opts, result); err != nil {
			return err
		}
		if err := engineImportLabelsTx(ctx, tx, issues, opts); err != nil {
			return err
		}
		if err := engineImportCommentsTx(ctx, tx, issues, opts); err != nil {
			return err
		}
		return nil
	}); err != nil {
		if strings.Contains(err.Error(), "not supported") {
			if err := engineUpsertIssues(ctx, store, issues, opts, orphanHandling, result); err != nil {
				return nil, err
			}
			if err := engineImportDependencies(ctx, store, issues, opts, result); err != nil {
				return nil, err
			}
			if err := engineImportLabels(ctx, store, issues, opts); err != nil {
				return nil, err
			}
			if err := engineImportComments(ctx, store, issues, opts); err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}

	return result, nil
}

// handlePrefixMismatch checks and handles prefix mismatches.
func handlePrefixMismatch(ctx context.Context, store storage.Storage, issues []*types.Issue, opts ImportOptions, _ storage.OrphanHandling, result *ImportResult) ([]*types.Issue, error) {
	configuredPrefix, err := store.GetConfig(ctx, "issue_prefix")
	if err != nil {
		return nil, fmt.Errorf("failed to get configured prefix: %w", err)
	}

	if strings.TrimSpace(configuredPrefix) == "" {
		if opts.RenameOnImport {
			return nil, fmt.Errorf("cannot rename: issue_prefix not configured in database")
		}
		return issues, nil
	}

	result.ExpectedPrefix = configuredPrefix

	allowedPrefixesConfig, _ := store.GetConfig(ctx, "allowed_prefixes")
	beadsDir := filepath.Dir(store.Path())
	allowedPrefixes := buildAllowedPrefixSet(configuredPrefix, allowedPrefixesConfig, beadsDir)
	if allowedPrefixes == nil {
		return issues, nil
	}

	tombstoneMismatchPrefixes := make(map[string]int)
	nonTombstoneMismatchCount := 0
	var filteredIssues []*types.Issue

	for _, issue := range issues {
		prefixMatches := false
		for prefix := range allowedPrefixes {
			if strings.HasPrefix(issue.ID, prefix+"-") {
				prefixMatches = true
				break
			}
		}
		if !prefixMatches {
			prefix := utils.ExtractIssuePrefix(issue.ID)
			if issue.IsTombstone() {
				tombstoneMismatchPrefixes[prefix]++
			} else {
				result.PrefixMismatch = true
				result.MismatchPrefixes[prefix]++
				nonTombstoneMismatchCount++
				filteredIssues = append(filteredIssues, issue)
			}
		} else {
			filteredIssues = append(filteredIssues, issue)
		}
	}

	if nonTombstoneMismatchCount == 0 && len(tombstoneMismatchPrefixes) > 0 {
		var tombstonePrefixList []string
		for prefix, count := range tombstoneMismatchPrefixes {
			tombstonePrefixList = append(tombstonePrefixList, fmt.Sprintf("%s- (%d tombstones)", prefix, count))
		}
		fmt.Fprintf(os.Stderr, "Ignoring prefix mismatches (all are tombstones): %v\n", tombstonePrefixList)
		result.PrefixMismatch = false
		result.MismatchPrefixes = make(map[string]int)
		return filteredIssues, nil
	}

	if result.PrefixMismatch {
		if !opts.RenameOnImport && !opts.DryRun && !opts.SkipPrefixValidation {
			return nil, fmt.Errorf("prefix mismatch detected: database uses '%s-' but found issues with prefixes: %v (use --rename-on-import to automatically fix)", configuredPrefix, getPrefixList(result.MismatchPrefixes))
		}
	}

	if result.PrefixMismatch && opts.RenameOnImport && !opts.DryRun {
		if err := renameImportedIssuePrefixes(issues, configuredPrefix); err != nil {
			return nil, fmt.Errorf("failed to rename prefixes: %w", err)
		}
		result.PrefixMismatch = false
		result.MismatchPrefixes = make(map[string]int)
		return issues, nil
	}

	return issues, nil
}

// engineDetectUpdates detects same-ID scenarios
func engineDetectUpdates(ctx context.Context, store storage.Storage, issues []*types.Issue, opts ImportOptions, result *ImportResult) ([]*types.Issue, error) {
	dbIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{IncludeTombstones: true})
	if err != nil {
		return nil, fmt.Errorf("collision detection failed: %w", err)
	}
	dbByID := engineBuildIDMap(dbIssues)

	newCount := 0
	exactCount := 0
	collisionCount := 0
	for _, incoming := range issues {
		existing, ok := dbByID[incoming.ID]
		if !ok || existing == nil {
			newCount++
			continue
		}
		if existing.ContentHash != "" && incoming.ContentHash != "" && existing.ContentHash == incoming.ContentHash {
			exactCount++
			continue
		}
		collisionCount++
		result.CollisionIDs = append(result.CollisionIDs, incoming.ID)
	}

	result.Collisions = collisionCount
	if opts.DryRun {
		result.Created = newCount
		result.Updated = collisionCount
		result.Unchanged = exactCount
	}
	return issues, nil
}

func engineBuildHashMap(issues []*types.Issue) map[string]*types.Issue {
	result := make(map[string]*types.Issue)
	for _, issue := range issues {
		if issue.ContentHash != "" {
			result[issue.ContentHash] = issue
		}
	}
	return result
}

func engineBuildIDMap(issues []*types.Issue) map[string]*types.Issue {
	result := make(map[string]*types.Issue)
	for _, issue := range issues {
		result[issue.ID] = issue
	}
	return result
}

func engineHandleRename(ctx context.Context, s storage.Storage, existing *types.Issue, incoming *types.Issue) (string, error) {
	targetIssue, err := s.GetIssue(ctx, incoming.ID)
	if err == nil && targetIssue != nil {
		if targetIssue.ComputeContentHash() == incoming.ComputeContentHash() {
			deletedID := ""
			existingCheck, checkErr := s.GetIssue(ctx, existing.ID)
			if checkErr == nil && existingCheck != nil {
				if err := s.DeleteIssue(ctx, existing.ID); err != nil {
					return "", fmt.Errorf("failed to delete old ID %s: %w", existing.ID, err)
				}
				deletedID = existing.ID
			}
			return deletedID, nil
		}
		if incoming.UpdatedAt.After(existing.UpdatedAt) {
			updates := map[string]interface{}{
				"title": incoming.Title, "description": incoming.Description,
				"design": incoming.Design, "acceptance_criteria": incoming.AcceptanceCriteria,
				"notes": incoming.Notes, "external_ref": incoming.ExternalRef,
				"status": incoming.Status, "priority": incoming.Priority,
				"issue_type": incoming.IssueType, "assignee": incoming.Assignee,
			}
			if err := s.UpdateIssue(ctx, existing.ID, updates, "importer"); err != nil {
				return "", fmt.Errorf("failed to update issue %s: %w", existing.ID, err)
			}
		}
		return "", nil
	}

	existingCheck, checkErr := s.GetIssue(ctx, existing.ID)
	if checkErr != nil || existingCheck == nil {
		targetCheck, targetErr := s.GetIssue(ctx, incoming.ID)
		if targetErr == nil && targetCheck != nil && targetCheck.ComputeContentHash() == incoming.ComputeContentHash() {
			return "", nil
		}
		return "", fmt.Errorf("old ID %s doesn't exist and target ID %s is not as expected", existing.ID, incoming.ID)
	}

	oldID := existing.ID
	if err := s.DeleteIssue(ctx, oldID); err != nil {
		return "", fmt.Errorf("failed to delete old ID %s: %w", oldID, err)
	}
	if err := s.CreateIssue(ctx, incoming, "import-rename"); err != nil {
		targetIssue, getErr := s.GetIssue(ctx, incoming.ID)
		if getErr == nil && targetIssue != nil && targetIssue.ComputeContentHash() == incoming.ComputeContentHash() {
			return oldID, nil
		}
		return "", fmt.Errorf("failed to create renamed issue %s: %w", incoming.ID, err)
	}
	return oldID, nil
}

func engineHandleRenameTx(ctx context.Context, tx storage.Transaction, existing *types.Issue, incoming *types.Issue) (string, error) {
	targetIssue, err := tx.GetIssue(ctx, incoming.ID)
	if err == nil && targetIssue != nil {
		if targetIssue.ComputeContentHash() == incoming.ComputeContentHash() {
			deletedID := ""
			existingCheck, checkErr := tx.GetIssue(ctx, existing.ID)
			if checkErr == nil && existingCheck != nil {
				if err := tx.DeleteIssue(ctx, existing.ID); err != nil {
					return "", fmt.Errorf("failed to delete old ID %s: %w", existing.ID, err)
				}
				deletedID = existing.ID
			}
			return deletedID, nil
		}
		if incoming.UpdatedAt.After(existing.UpdatedAt) {
			updates := map[string]interface{}{
				"title": incoming.Title, "description": incoming.Description,
				"design": incoming.Design, "acceptance_criteria": incoming.AcceptanceCriteria,
				"notes": incoming.Notes, "external_ref": incoming.ExternalRef,
				"status": incoming.Status, "priority": incoming.Priority,
				"issue_type": incoming.IssueType, "assignee": incoming.Assignee,
			}
			if err := tx.UpdateIssue(ctx, existing.ID, updates, "importer"); err != nil {
				return "", fmt.Errorf("failed to update issue %s: %w", existing.ID, err)
			}
		}
		return "", nil
	}

	existingCheck, checkErr := tx.GetIssue(ctx, existing.ID)
	if checkErr != nil || existingCheck == nil {
		targetCheck, targetErr := tx.GetIssue(ctx, incoming.ID)
		if targetErr == nil && targetCheck != nil && targetCheck.ComputeContentHash() == incoming.ComputeContentHash() {
			return "", nil
		}
		return "", fmt.Errorf("old ID %s doesn't exist and target ID %s is not as expected", existing.ID, incoming.ID)
	}

	oldID := existing.ID
	if err := tx.DeleteIssue(ctx, oldID); err != nil {
		return "", fmt.Errorf("failed to delete old ID %s: %w", oldID, err)
	}
	if err := tx.CreateIssue(ctx, incoming, "import-rename"); err != nil {
		targetIssue, getErr := tx.GetIssue(ctx, incoming.ID)
		if getErr == nil && targetIssue != nil && targetIssue.ComputeContentHash() == incoming.ComputeContentHash() {
			return oldID, nil
		}
		return "", fmt.Errorf("failed to create renamed issue %s: %w", incoming.ID, err)
	}
	return oldID, nil
}

// engineUpsertIssues creates new issues or updates existing ones
func engineUpsertIssues(ctx context.Context, store storage.Storage, issues []*types.Issue, opts ImportOptions, orphanHandling storage.OrphanHandling, result *ImportResult) error {
	dbIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{IncludeTombstones: true})
	if err != nil {
		return fmt.Errorf("failed to get DB issues: %w", err)
	}

	dbByHash := engineBuildHashMap(dbIssues)
	dbByID := engineBuildIDMap(dbIssues)

	dbByExternalRef := make(map[string]*types.Issue)
	for _, issue := range dbIssues {
		if issue.ExternalRef != nil && *issue.ExternalRef != "" {
			dbByExternalRef[*issue.ExternalRef] = issue
			if linear.IsLinearExternalRef(*issue.ExternalRef) {
				if canonical, ok := linear.CanonicalizeLinearExternalRef(*issue.ExternalRef); ok {
					dbByExternalRef[canonical] = issue
				}
			}
		}
	}

	var newIssues []*types.Issue
	seenHashes := make(map[string]bool)
	seenIDs := make(map[string]bool)

	for _, incoming := range issues {
		hash := incoming.ContentHash
		if hash == "" {
			hash = incoming.ComputeContentHash()
			incoming.ContentHash = hash
		}
		if seenHashes[hash] {
			result.Skipped++
			continue
		}
		seenHashes[hash] = true
		if seenIDs[incoming.ID] {
			result.Skipped++
			continue
		}
		seenIDs[incoming.ID] = true

		if existingByID, found := dbByID[incoming.ID]; found {
			if existingByID.Status == types.StatusTombstone {
				result.Skipped++
				continue
			}
		}

		if incoming.ExternalRef != nil && *incoming.ExternalRef != "" {
			if existing, found := dbByExternalRef[*incoming.ExternalRef]; found {
				if !opts.SkipUpdate {
					if engineShouldProtectFromUpdate(existing.ID, incoming.UpdatedAt, opts.ProtectLocalExportIDs) {
						result.Skipped++
						continue
					}
					if !incoming.UpdatedAt.After(existing.UpdatedAt) {
						result.Unchanged++
						continue
					}
					updates := engineBuildUpdates(incoming)
					if issueDataChanged(existing, updates) {
						if err := store.UpdateIssue(ctx, existing.ID, updates, "import"); err != nil {
							return fmt.Errorf("error updating issue %s (matched by external_ref): %w", existing.ID, err)
						}
						result.Updated++
					} else {
						result.Unchanged++
					}
				} else {
					result.Skipped++
				}
				continue
			}
		}

		if existing, found := dbByHash[hash]; found {
			if existing.ID == incoming.ID {
				result.Unchanged++
			} else {
				existingPrefix := utils.ExtractIssuePrefix(existing.ID)
				incomingPrefix := utils.ExtractIssuePrefix(incoming.ID)
				if existingPrefix != incomingPrefix {
					result.Skipped++
				} else if !opts.SkipUpdate {
					deletedID, err := engineHandleRename(ctx, store, existing, incoming)
					if err != nil {
						return fmt.Errorf("failed to handle rename %s -> %s: %w", existing.ID, incoming.ID, err)
					}
					if deletedID != "" {
						delete(dbByID, deletedID)
					}
					result.Updated++
				} else {
					result.Skipped++
				}
			}
			continue
		}

		if existingWithID, found := dbByID[incoming.ID]; found {
			if existingWithID.Status == types.StatusTombstone {
				result.Skipped++
				continue
			}
			if !opts.SkipUpdate {
				if engineShouldProtectFromUpdate(incoming.ID, incoming.UpdatedAt, opts.ProtectLocalExportIDs) {
					result.Skipped++
					continue
				}
				if !incoming.UpdatedAt.After(existingWithID.UpdatedAt) {
					result.Unchanged++
					continue
				}
				updates := engineBuildUpdates(incoming)
				if issueDataChanged(existingWithID, updates) {
					if err := store.UpdateIssue(ctx, incoming.ID, updates, "import"); err != nil {
						return fmt.Errorf("error updating issue %s: %w", incoming.ID, err)
					}
					result.Updated++
				} else {
					result.Unchanged++
				}
			} else {
				result.Skipped++
			}
		} else {
			newIssues = append(newIssues, incoming)
		}
	}

	newIssues = engineFilterOrphans(dbIssues, dbByID, newIssues, issues, store, orphanHandling, opts, result)

	if len(newIssues) > 0 {
		sort.Slice(newIssues, func(i, j int) bool {
			depthI := engineHierarchyDepth(newIssues[i].ID)
			depthJ := engineHierarchyDepth(newIssues[j].ID)
			if depthI != depthJ {
				return depthI < depthJ
			}
			return newIssues[i].ID < newIssues[j].ID
		})
		for depth := 0; depth <= 3; depth++ {
			var batchForDepth []*types.Issue
			for _, issue := range newIssues {
				if engineHierarchyDepth(issue.ID) == depth {
					batchForDepth = append(batchForDepth, issue)
				}
			}
			if len(batchForDepth) > 0 {
				batchOpts := storage.BatchCreateOptions{
					OrphanHandling:       orphanHandling,
					SkipPrefixValidation: opts.SkipPrefixValidation,
				}
				if err := store.CreateIssuesWithFullOptions(ctx, batchForDepth, "import", batchOpts); err != nil {
					return fmt.Errorf("error creating depth-%d issues: %w", depth, err)
				}
				result.Created += len(batchForDepth)
			}
		}
	}

	return nil
}

func engineBuildUpdates(incoming *types.Issue) map[string]interface{} {
	updates := make(map[string]interface{})
	updates["title"] = incoming.Title
	updates["description"] = incoming.Description
	updates["status"] = incoming.Status
	updates["priority"] = incoming.Priority
	updates["issue_type"] = incoming.IssueType
	updates["design"] = incoming.Design
	updates["acceptance_criteria"] = incoming.AcceptanceCriteria
	updates["notes"] = incoming.Notes
	updates["closed_at"] = incoming.ClosedAt
	if incoming.Pinned {
		updates["pinned"] = incoming.Pinned
	}
	if incoming.Assignee != "" {
		updates["assignee"] = incoming.Assignee
	} else {
		updates["assignee"] = nil
	}
	if incoming.ExternalRef != nil && *incoming.ExternalRef != "" {
		updates["external_ref"] = *incoming.ExternalRef
	} else {
		updates["external_ref"] = nil
	}
	return updates
}

func engineFilterOrphans(dbIssues []*types.Issue, dbByID map[string]*types.Issue, newIssues []*types.Issue, allIncoming []*types.Issue, store storage.Storage, orphanHandling storage.OrphanHandling, _ ImportOptions, result *ImportResult) []*types.Issue {
	if orphanHandling == storage.OrphanSkip {
		var filtered []*types.Issue
		for _, issue := range newIssues {
			if isHier, parentID := engineIsHierarchicalID(issue.ID); isHier {
				var parentExists bool
				for _, dbIssue := range dbIssues {
					if dbIssue.ID == parentID {
						parentExists = true
						break
					}
				}
				if !parentExists {
					for _, newIssue := range newIssues {
						if newIssue.ID == parentID {
							parentExists = true
							break
						}
					}
				}
				if !parentExists {
					result.Skipped++
					continue
				}
			}
			filtered = append(filtered, issue)
		}
		newIssues = filtered
	}

	if orphanHandling == storage.OrphanStrict {
		newIDSet := make(map[string]bool, len(newIssues))
		for _, issue := range newIssues {
			newIDSet[issue.ID] = true
		}
		for _, issue := range newIssues {
			if isHier, parentID := engineIsHierarchicalID(issue.ID); isHier {
				if dbByID[parentID] == nil && !newIDSet[parentID] {
					// In strict mode this would error - handled by caller
					continue
				}
			}
		}
	}

	if orphanHandling == storage.OrphanResurrect {
		_ = engineAddResurrectedParents(store, dbByID, allIncoming, &newIssues)
	}

	return newIssues
}

// engineUpsertIssuesTx is the transaction-based version
func engineUpsertIssuesTx(ctx context.Context, tx storage.Transaction, store storage.Storage, issues []*types.Issue, opts ImportOptions, orphanHandling storage.OrphanHandling, result *ImportResult) error {
	dbIssues, err := tx.SearchIssues(ctx, "", types.IssueFilter{IncludeTombstones: true})
	if err != nil {
		return fmt.Errorf("failed to get DB issues: %w", err)
	}

	dbByHash := engineBuildHashMap(dbIssues)
	dbByID := engineBuildIDMap(dbIssues)

	dbByExternalRef := make(map[string]*types.Issue)
	for _, issue := range dbIssues {
		if issue.ExternalRef != nil && *issue.ExternalRef != "" {
			dbByExternalRef[*issue.ExternalRef] = issue
			if linear.IsLinearExternalRef(*issue.ExternalRef) {
				if canonical, ok := linear.CanonicalizeLinearExternalRef(*issue.ExternalRef); ok {
					dbByExternalRef[canonical] = issue
				}
			}
		}
	}

	var newIssues []*types.Issue
	seenHashes := make(map[string]bool)
	seenIDs := make(map[string]bool)

	for _, incoming := range issues {
		hash := incoming.ContentHash
		if hash == "" {
			hash = incoming.ComputeContentHash()
			incoming.ContentHash = hash
		}
		if seenHashes[hash] {
			result.Skipped++
			continue
		}
		seenHashes[hash] = true
		if seenIDs[incoming.ID] {
			result.Skipped++
			continue
		}
		seenIDs[incoming.ID] = true

		if existingByID, found := dbByID[incoming.ID]; found && existingByID != nil && existingByID.Status == types.StatusTombstone {
			result.Skipped++
			continue
		}

		if incoming.ExternalRef != nil && *incoming.ExternalRef != "" {
			if existing, found := dbByExternalRef[*incoming.ExternalRef]; found && existing != nil {
				if !opts.SkipUpdate {
					if engineShouldProtectFromUpdate(existing.ID, incoming.UpdatedAt, opts.ProtectLocalExportIDs) {
						result.Skipped++
						continue
					}
					if !incoming.UpdatedAt.After(existing.UpdatedAt) {
						result.Unchanged++
						continue
					}
					updates := engineBuildUpdates(incoming)
					if issueDataChanged(existing, updates) {
						if err := tx.UpdateIssue(ctx, existing.ID, updates, "import"); err != nil {
							return fmt.Errorf("error updating issue %s (matched by external_ref): %w", existing.ID, err)
						}
						result.Updated++
					} else {
						result.Unchanged++
					}
				} else {
					result.Skipped++
				}
				continue
			}
		}

		if existing, found := dbByHash[hash]; found && existing != nil {
			if existing.ID == incoming.ID {
				result.Unchanged++
			} else {
				existingPrefix := utils.ExtractIssuePrefix(existing.ID)
				incomingPrefix := utils.ExtractIssuePrefix(incoming.ID)
				if existingPrefix != incomingPrefix {
					result.Skipped++
				} else if !opts.SkipUpdate {
					deletedID, err := engineHandleRenameTx(ctx, tx, existing, incoming)
					if err != nil {
						return fmt.Errorf("failed to handle rename %s -> %s: %w", existing.ID, incoming.ID, err)
					}
					if deletedID != "" {
						delete(dbByID, deletedID)
					}
					result.Updated++
				} else {
					result.Skipped++
				}
			}
			continue
		}

		if existingWithID, found := dbByID[incoming.ID]; found && existingWithID != nil {
			if existingWithID.Status == types.StatusTombstone {
				result.Skipped++
				continue
			}
			if !opts.SkipUpdate {
				if engineShouldProtectFromUpdate(incoming.ID, incoming.UpdatedAt, opts.ProtectLocalExportIDs) {
					result.Skipped++
					continue
				}
				if !incoming.UpdatedAt.After(existingWithID.UpdatedAt) {
					result.Unchanged++
					continue
				}
				updates := engineBuildUpdates(incoming)
				if issueDataChanged(existingWithID, updates) {
					if err := tx.UpdateIssue(ctx, incoming.ID, updates, "import"); err != nil {
						return fmt.Errorf("error updating issue %s: %w", incoming.ID, err)
					}
					result.Updated++
				} else {
					result.Unchanged++
				}
			} else {
				result.Skipped++
			}
		} else {
			newIssues = append(newIssues, incoming)
		}
	}

	// Orphan handling
	if orphanHandling == storage.OrphanSkip {
		var filtered []*types.Issue
		for _, issue := range newIssues {
			if isHier, parentID := engineIsHierarchicalID(issue.ID); isHier {
				if dbByID[parentID] == nil {
					found := false
					for _, ni := range newIssues {
						if ni.ID == parentID {
							found = true
							break
						}
					}
					if !found {
						result.Skipped++
						continue
					}
				}
			}
			filtered = append(filtered, issue)
		}
		newIssues = filtered
	}
	if orphanHandling == storage.OrphanStrict {
		newIDSet := make(map[string]bool, len(newIssues))
		for _, issue := range newIssues {
			newIDSet[issue.ID] = true
		}
		for _, issue := range newIssues {
			if isHier, parentID := engineIsHierarchicalID(issue.ID); isHier {
				if dbByID[parentID] == nil && !newIDSet[parentID] {
					return fmt.Errorf("parent issue %s does not exist (strict mode)", parentID)
				}
			}
		}
	}
	if orphanHandling == storage.OrphanResurrect {
		if err := engineAddResurrectedParents(store, dbByID, issues, &newIssues); err != nil {
			return err
		}
	}

	if len(newIssues) > 0 {
		sort.Slice(newIssues, func(i, j int) bool {
			di := engineHierarchyDepth(newIssues[i].ID)
			dj := engineHierarchyDepth(newIssues[j].ID)
			if di != dj {
				return di < dj
			}
			return newIssues[i].ID < newIssues[j].ID
		})
		type importCreator interface {
			CreateIssueImport(ctx context.Context, issue *types.Issue, actor string, skipPrefixValidation bool) error
		}
		for _, iss := range newIssues {
			if ic, ok := tx.(importCreator); ok {
				if err := ic.CreateIssueImport(ctx, iss, "import", opts.SkipPrefixValidation); err != nil {
					return err
				}
			} else {
				if err := tx.CreateIssue(ctx, iss, "import"); err != nil {
					return err
				}
			}
			result.Created++
		}
	}

	return nil
}

func engineImportDependenciesTx(ctx context.Context, tx storage.Transaction, issues []*types.Issue, opts ImportOptions, result *ImportResult) error {
	for _, issue := range issues {
		if len(issue.Dependencies) == 0 {
			continue
		}
		existingDeps, err := tx.GetDependencyRecords(ctx, issue.ID)
		if err != nil {
			return fmt.Errorf("error checking dependencies for %s: %w", issue.ID, err)
		}
		existingSet := make(map[string]bool)
		for _, existing := range existingDeps {
			existingSet[fmt.Sprintf("%s|%s", existing.DependsOnID, existing.Type)] = true
		}
		for _, dep := range issue.Dependencies {
			if existingSet[fmt.Sprintf("%s|%s", dep.DependsOnID, dep.Type)] {
				continue
			}
			if err := tx.AddDependency(ctx, dep, "import"); err != nil {
				if opts.Strict {
					return fmt.Errorf("error adding dependency %s → %s: %w", dep.IssueID, dep.DependsOnID, err)
				}
				depDesc := fmt.Sprintf("%s → %s (%s)", dep.IssueID, dep.DependsOnID, dep.Type)
				fmt.Fprintf(os.Stderr, "Warning: Skipping dependency due to error: %s (%v)\n", depDesc, err)
				if result != nil {
					result.SkippedDependencies = append(result.SkippedDependencies, depDesc)
				}
			}
		}
	}
	return nil
}

func engineImportLabelsTx(ctx context.Context, tx storage.Transaction, issues []*types.Issue, opts ImportOptions) error {
	for _, issue := range issues {
		if len(issue.Labels) == 0 {
			continue
		}
		currentLabels, err := tx.GetLabels(ctx, issue.ID)
		if err != nil {
			return fmt.Errorf("error getting labels for %s: %w", issue.ID, err)
		}
		currentSet := make(map[string]bool, len(currentLabels))
		for _, l := range currentLabels {
			currentSet[l] = true
		}
		importSet := make(map[string]bool, len(issue.Labels))
		for _, label := range issue.Labels {
			importSet[label] = true
			if !currentSet[label] {
				if err := tx.AddLabel(ctx, issue.ID, label, "import"); err != nil {
					if opts.Strict {
						return fmt.Errorf("error adding label %s to %s: %w", label, issue.ID, err)
					}
				}
			}
		}
		for _, label := range currentLabels {
			if !importSet[label] {
				if err := tx.RemoveLabel(ctx, issue.ID, label, "import"); err != nil {
					if opts.Strict {
						return fmt.Errorf("error removing label %s from %s: %w", label, issue.ID, err)
					}
				}
			}
		}
	}
	return nil
}

func engineImportCommentsTx(ctx context.Context, tx storage.Transaction, issues []*types.Issue, opts ImportOptions) error {
	for _, issue := range issues {
		if len(issue.Comments) == 0 {
			continue
		}
		currentComments, err := tx.GetIssueComments(ctx, issue.ID)
		if err != nil {
			return fmt.Errorf("error getting comments for %s: %w", issue.ID, err)
		}
		existing := make(map[string]bool)
		for _, c := range currentComments {
			existing[fmt.Sprintf("%s:%s", c.Author, strings.TrimSpace(c.Text))] = true
		}
		for _, comment := range issue.Comments {
			if existing[fmt.Sprintf("%s:%s", comment.Author, strings.TrimSpace(comment.Text))] {
				continue
			}
			if _, err := tx.ImportIssueComment(ctx, issue.ID, comment.Author, comment.Text, comment.CreatedAt); err != nil {
				if opts.Strict {
					return fmt.Errorf("error adding comment to %s: %w", issue.ID, err)
				}
			}
		}
	}
	return nil
}

func engineImportDependencies(ctx context.Context, store storage.Storage, issues []*types.Issue, opts ImportOptions, result *ImportResult) error {
	dbIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{IncludeTombstones: true})
	if err != nil {
		return fmt.Errorf("failed to load issues for dependency validation: %w", err)
	}
	exists := make(map[string]bool, len(dbIssues))
	for _, iss := range dbIssues {
		if iss != nil {
			exists[iss.ID] = true
		}
	}
	for _, issue := range issues {
		if len(issue.Dependencies) == 0 {
			continue
		}
		existingDeps, err := store.GetDependencyRecords(ctx, issue.ID)
		if err != nil {
			return fmt.Errorf("error checking dependencies for %s: %w", issue.ID, err)
		}
		existingSet := make(map[string]bool)
		for _, existing := range existingDeps {
			existingSet[fmt.Sprintf("%s|%s", existing.DependsOnID, existing.Type)] = true
		}
		for _, dep := range issue.Dependencies {
			if !exists[dep.IssueID] || !exists[dep.DependsOnID] {
				depDesc := fmt.Sprintf("%s → %s (%s)", dep.IssueID, dep.DependsOnID, dep.Type)
				if opts.Strict {
					return fmt.Errorf("missing reference for dependency: %s", depDesc)
				}
				fmt.Fprintf(os.Stderr, "Warning: Skipping dependency due to missing reference: %s\n", depDesc)
				if result != nil {
					result.SkippedDependencies = append(result.SkippedDependencies, depDesc)
				}
				continue
			}
			if existingSet[fmt.Sprintf("%s|%s", dep.DependsOnID, dep.Type)] {
				continue
			}
			if err := store.AddDependency(ctx, dep, "import"); err != nil {
				if opts.Strict {
					return fmt.Errorf("error adding dependency %s → %s: %w", dep.IssueID, dep.DependsOnID, err)
				}
				depDesc := fmt.Sprintf("%s → %s (%s)", dep.IssueID, dep.DependsOnID, dep.Type)
				fmt.Fprintf(os.Stderr, "Warning: Skipping dependency due to error: %s (%v)\n", depDesc, err)
				if result != nil {
					result.SkippedDependencies = append(result.SkippedDependencies, depDesc)
				}
			}
		}
	}
	return nil
}

func engineImportLabels(ctx context.Context, store storage.Storage, issues []*types.Issue, opts ImportOptions) error {
	for _, issue := range issues {
		if len(issue.Labels) == 0 {
			continue
		}
		currentLabels, err := store.GetLabels(ctx, issue.ID)
		if err != nil {
			return fmt.Errorf("error getting labels for %s: %w", issue.ID, err)
		}
		currentSet := make(map[string]bool)
		for _, label := range currentLabels {
			currentSet[label] = true
		}
		importSet := make(map[string]bool, len(issue.Labels))
		for _, label := range issue.Labels {
			importSet[label] = true
			if !currentSet[label] {
				if err := store.AddLabel(ctx, issue.ID, label, "import"); err != nil {
					if opts.Strict {
						return fmt.Errorf("error adding label %s to %s: %w", label, issue.ID, err)
					}
				}
			}
		}
		for _, label := range currentLabels {
			if !importSet[label] {
				if err := store.RemoveLabel(ctx, issue.ID, label, "import"); err != nil {
					if opts.Strict {
						return fmt.Errorf("error removing label %s from %s: %w", label, issue.ID, err)
					}
				}
			}
		}
	}
	return nil
}

func engineImportComments(ctx context.Context, store storage.Storage, issues []*types.Issue, opts ImportOptions) error {
	for _, issue := range issues {
		if len(issue.Comments) == 0 {
			continue
		}
		currentComments, err := store.GetIssueComments(ctx, issue.ID)
		if err != nil {
			return fmt.Errorf("error getting comments for %s: %w", issue.ID, err)
		}
		existing := make(map[string]bool)
		for _, c := range currentComments {
			existing[fmt.Sprintf("%s:%s", c.Author, strings.TrimSpace(c.Text))] = true
		}
		for _, comment := range issue.Comments {
			if existing[fmt.Sprintf("%s:%s", comment.Author, strings.TrimSpace(comment.Text))] {
				continue
			}
			if _, err := store.ImportIssueComment(ctx, issue.ID, comment.Author, comment.Text, comment.CreatedAt); err != nil {
				if opts.Strict {
					return fmt.Errorf("error adding comment to %s: %w", issue.ID, err)
				}
			}
		}
	}
	return nil
}

func engineShouldProtectFromUpdate(issueID string, incomingTime time.Time, protectMap map[string]time.Time) bool {
	if protectMap == nil {
		return false
	}
	localTime, exists := protectMap[issueID]
	if !exists {
		return false
	}
	return !incomingTime.After(localTime)
}

func engineIsHierarchicalID(id string) (bool, string) {
	lastDot := strings.LastIndex(id, ".")
	if lastDot <= 0 || lastDot == len(id)-1 {
		return false, ""
	}
	suffix := id[lastDot+1:]
	for i := 0; i < len(suffix); i++ {
		if suffix[i] < '0' || suffix[i] > '9' {
			return false, ""
		}
	}
	return true, id[:lastDot]
}

func engineHierarchyDepth(id string) int {
	depth := 0
	cur := id
	for {
		isHier, parent := engineIsHierarchicalID(cur)
		if !isHier {
			return depth
		}
		depth++
		cur = parent
	}
}

func engineAddResurrectedParents(store storage.Storage, dbByID map[string]*types.Issue, allIncoming []*types.Issue, newIssues *[]*types.Issue) error {
	willExist := make(map[string]bool, len(dbByID)+len(*newIssues))
	for id, iss := range dbByID {
		if iss != nil {
			willExist[id] = true
		}
	}
	for _, iss := range *newIssues {
		willExist[iss.ID] = true
	}

	var ensureParent func(parentID string) error
	ensureParent = func(parentID string) error {
		if willExist[parentID] {
			return nil
		}
		if isHier, grandParent := engineIsHierarchicalID(parentID); isHier {
			if err := ensureParent(grandParent); err != nil {
				return err
			}
		}
		for _, iss := range allIncoming {
			if iss.ID == parentID {
				willExist[parentID] = true
				return nil
			}
		}
		beadsDir := filepath.Dir(store.Path())
		found, err := engineFindIssueInLocalJSONL(filepath.Join(beadsDir, "issues.jsonl"), parentID)
		if err != nil {
			return fmt.Errorf("parent issue %s does not exist and could not be resurrected: %w", parentID, err)
		}
		if found == nil {
			return fmt.Errorf("parent issue %s does not exist and cannot be resurrected from local JSONL history", parentID)
		}
		now := time.Now().UTC()
		closedAt := now
		tombstone := &types.Issue{
			ID: found.ID, Title: found.Title, IssueType: found.IssueType,
			Status: types.StatusClosed, Priority: 4,
			CreatedAt: found.CreatedAt, UpdatedAt: now, ClosedAt: &closedAt,
			Description: "[RESURRECTED] Recreated as closed to preserve hierarchical structure.",
		}
		tombstone.ContentHash = tombstone.ComputeContentHash()
		*newIssues = append(*newIssues, tombstone)
		willExist[parentID] = true
		return nil
	}

	for _, iss := range *newIssues {
		if isHier, parentID := engineIsHierarchicalID(iss.ID); isHier {
			if err := ensureParent(parentID); err != nil {
				return err
			}
		}
	}
	return nil
}

func engineFindIssueInLocalJSONL(jsonlPath, issueID string) (*types.Issue, error) {
	if jsonlPath == "" {
		return nil, nil
	}
	if _, err := os.Stat(jsonlPath); err != nil {
		return nil, nil
	}
	f, err := os.Open(jsonlPath) // #nosec G304
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var last *types.Issue
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.Contains(line, `"`+issueID+`"`) {
			continue
		}
		var iss types.Issue
		if err := json.Unmarshal([]byte(line), &iss); err != nil {
			continue
		}
		if iss.ID == issueID {
			iss.SetDefaults()
			copy := iss
			last = &copy
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return last, nil
}

func validateNoDuplicateExternalRefs(issues []*types.Issue, clearDuplicates bool, result *ImportResult) error {
	seen := make(map[string][]string)
	for _, issue := range issues {
		if issue.Status == types.StatusTombstone {
			continue
		}
		if issue.ExternalRef != nil && *issue.ExternalRef != "" {
			seen[*issue.ExternalRef] = append(seen[*issue.ExternalRef], issue.ID)
		}
	}
	var duplicates []string
	duplicateIssueIDs := make(map[string]bool)
	for ref, issueIDs := range seen {
		if len(issueIDs) > 1 {
			duplicates = append(duplicates, fmt.Sprintf("external_ref '%s' appears in issues: %v", ref, issueIDs))
			for i := 1; i < len(issueIDs); i++ {
				duplicateIssueIDs[issueIDs[i]] = true
			}
		}
	}
	if len(duplicates) > 0 {
		if clearDuplicates {
			for _, issue := range issues {
				if duplicateIssueIDs[issue.ID] {
					issue.ExternalRef = nil
				}
			}
			if result != nil {
				result.Skipped += len(duplicateIssueIDs)
			}
			return nil
		}
		sort.Strings(duplicates)
		return fmt.Errorf("batch import contains duplicate external_ref values:\n%s\n\nUse --clear-duplicate-external-refs to automatically clear duplicates", strings.Join(duplicates, "\n"))
	}
	return nil
}

func buildAllowedPrefixSet(primaryPrefix string, allowedPrefixesConfig string, beadsDir string) map[string]bool {
	if config.GetMultiRepoConfig() != nil {
		return nil
	}
	allowed := map[string]bool{primaryPrefix: true}
	if allowedPrefixesConfig != "" {
		for _, prefix := range strings.Split(allowedPrefixesConfig, ",") {
			prefix = strings.TrimSpace(prefix)
			if prefix == "" {
				continue
			}
			prefix = strings.TrimSuffix(prefix, "-")
			allowed[prefix] = true
		}
	}
	if beadsDir != "" {
		routes, _ := routing.LoadTownRoutes(beadsDir)
		for _, route := range routes {
			prefix := strings.TrimSuffix(route.Prefix, "-")
			if prefix != "" {
				allowed[prefix] = true
			}
		}
	}
	return allowed
}

func getPrefixList(prefixes map[string]int) []string {
	var result []string
	keys := make([]string, 0, len(prefixes))
	for k := range prefixes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, prefix := range keys {
		result = append(result, fmt.Sprintf("%s- (%d issues)", prefix, prefixes[prefix]))
	}
	return result
}

func renameImportedIssuePrefixes(issues []*types.Issue, targetPrefix string) error {
	idMapping := make(map[string]string)
	for _, issue := range issues {
		oldPrefix := utils.ExtractIssuePrefix(issue.ID)
		if oldPrefix == "" {
			return fmt.Errorf("cannot rename issue %s: malformed ID (no hyphen found)", issue.ID)
		}
		if oldPrefix != targetPrefix {
			suffix := strings.TrimPrefix(issue.ID, oldPrefix+"-")
			if suffix == "" || !engineIsValidIDSuffix(suffix) {
				return fmt.Errorf("cannot rename issue %s: invalid suffix '%s'", issue.ID, suffix)
			}
			idMapping[issue.ID] = fmt.Sprintf("%s-%s", targetPrefix, suffix)
		}
	}
	for _, issue := range issues {
		if newID, ok := idMapping[issue.ID]; ok {
			issue.ID = newID
		}
		issue.Title = engineReplaceIDReferences(issue.Title, idMapping)
		issue.Description = engineReplaceIDReferences(issue.Description, idMapping)
		if issue.Design != "" {
			issue.Design = engineReplaceIDReferences(issue.Design, idMapping)
		}
		if issue.AcceptanceCriteria != "" {
			issue.AcceptanceCriteria = engineReplaceIDReferences(issue.AcceptanceCriteria, idMapping)
		}
		if issue.Notes != "" {
			issue.Notes = engineReplaceIDReferences(issue.Notes, idMapping)
		}
		for i := range issue.Dependencies {
			if newID, ok := idMapping[issue.Dependencies[i].IssueID]; ok {
				issue.Dependencies[i].IssueID = newID
			}
			if newID, ok := idMapping[issue.Dependencies[i].DependsOnID]; ok {
				issue.Dependencies[i].DependsOnID = newID
			}
		}
		for i := range issue.Comments {
			issue.Comments[i].Text = engineReplaceIDReferences(issue.Comments[i].Text, idMapping)
		}
	}
	return nil
}

func engineReplaceIDReferences(text string, idMapping map[string]string) string {
	if len(idMapping) == 0 {
		return text
	}
	oldIDs := make([]string, 0, len(idMapping))
	for oldID := range idMapping {
		oldIDs = append(oldIDs, oldID)
	}
	sort.Slice(oldIDs, func(i, j int) bool {
		return len(oldIDs[i]) > len(oldIDs[j])
	})
	result := text
	for _, oldID := range oldIDs {
		result = engineReplaceBoundaryAware(result, oldID, idMapping[oldID])
	}
	return result
}

func engineReplaceBoundaryAware(text, oldID, newID string) string {
	if !strings.Contains(text, oldID) {
		return text
	}
	var result strings.Builder
	i := 0
	for i < len(text) {
		idx := strings.Index(text[i:], oldID)
		if idx == -1 {
			result.WriteString(text[i:])
			break
		}
		actualIdx := i + idx
		beforeOK := actualIdx == 0 || engineIsBoundary(text[actualIdx-1])
		afterIdx := actualIdx + len(oldID)
		afterOK := afterIdx >= len(text) || engineIsBoundary(text[afterIdx])
		result.WriteString(text[i:actualIdx])
		if beforeOK && afterOK {
			result.WriteString(newID)
		} else {
			result.WriteString(oldID)
		}
		i = afterIdx
	}
	return result.String()
}

func engineIsBoundary(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ',' || c == '.' || c == '!' || c == '?' || c == ':' || c == ';' || c == '(' || c == ')' || c == '[' || c == ']' || c == '{' || c == '}'
}

func engineIsValidIDSuffix(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || c == '.') {
			return false
		}
	}
	return true
}
