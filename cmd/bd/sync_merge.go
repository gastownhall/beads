package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
)

// MergeResult contains the outcome of a 3-way merge
type MergeResult struct {
	Merged          []*beads.Issue    // Final merged state
	Conflicts       int               // Number of true conflicts resolved
	Strategy        map[string]string // Per-issue: "local", "remote", "merged", "same"
	ManualConflicts []ManualConflict  // Fields requiring manual resolution
}

// ManualConflict represents a field conflict that requires manual user resolution.
// When a field's strategy is "manual", the conflict is flagged here instead of
// being auto-resolved.
type ManualConflict struct {
	IssueID     string      // Issue ID with conflict
	Field       string      // Field name (e.g., "estimated_minutes")
	LocalValue  interface{} // Local (ours) value
	RemoteValue interface{} // Remote (theirs) value
	LocalTime   time.Time   // When local value was set
	RemoteTime  time.Time   // When remote value was set
}

// MergeStrategy constants for describing how each issue was merged
const (
	StrategyLocal  = "local"  // Only local changed
	StrategyRemote = "remote" // Only remote changed
	StrategyMerged = "merged" // True conflict, LWW applied
	StrategySame   = "same"   // Both made identical change (or no change)
)

// mergeFieldLevel performs field-by-field merge for true conflicts.
// Returns a new issue with:
// - Scalar fields: from the newer issue (LWW by updated_at, remote wins on tie)
// - Labels: union of both
// - Dependencies: union of both (by DependsOnID+Type)
// - Comments: append from both (deduplicated by ID or content)
// - compaction_level: max strategy (highest value wins)
// - estimated_minutes: manual strategy if configured (flags for user resolution)
//
// Also returns any manual conflicts that require user resolution.
func mergeFieldLevel(_base, local, remote *beads.Issue) (*beads.Issue, []ManualConflict) {
	var manualConflicts []ManualConflict

	// Determine which is newer for LWW scalars
	localNewer := local.UpdatedAt.After(remote.UpdatedAt)

	// Clock skew detection: warn if timestamps differ by more than 24 hours
	timeDiff := local.UpdatedAt.Sub(remote.UpdatedAt)
	if timeDiff < 0 {
		timeDiff = -timeDiff
	}
	if timeDiff > 24*time.Hour {
		fmt.Fprintf(os.Stderr, "Warning: Issue %s has %v timestamp difference (possible clock skew)\n",
			local.ID, timeDiff.Round(time.Hour))
	}

	// Start with a copy of the newer issue for scalar fields
	var merged beads.Issue
	if localNewer {
		merged = *local
	} else {
		merged = *remote
	}

	// Get per-field strategies from config
	fieldStrategies := config.GetFieldStrategies()

	// Handle compaction_level with configurable strategy (default: max)
	compactionStrategy := fieldStrategies["compaction_level"]
	if compactionStrategy == "" {
		compactionStrategy = config.FieldStrategyMax // Default for compaction_level
	}
	switch compactionStrategy {
	case config.FieldStrategyMax:
		merged.CompactionLevel = maxInt(local.CompactionLevel, remote.CompactionLevel)
	case config.FieldStrategyNewest:
		// Already handled by default LWW merge above
	case config.FieldStrategyManual:
		if local.CompactionLevel != remote.CompactionLevel {
			manualConflicts = append(manualConflicts, ManualConflict{
				IssueID:     local.ID,
				Field:       "compaction_level",
				LocalValue:  local.CompactionLevel,
				RemoteValue: remote.CompactionLevel,
				LocalTime:   local.UpdatedAt,
				RemoteTime:  remote.UpdatedAt,
			})
			// Keep local value as tentative (can be overridden by resolve command)
			merged.CompactionLevel = local.CompactionLevel
		}
	}

	// Handle estimated_minutes with configurable strategy (default: manual per spec)
	estimatedStrategy := fieldStrategies["estimated_minutes"]
	if estimatedStrategy == "" {
		estimatedStrategy = config.FieldStrategyManual // Default for estimated_minutes (human judgment needed)
	}
	switch estimatedStrategy {
	case config.FieldStrategyMax:
		merged.EstimatedMinutes = maxIntPtr(local.EstimatedMinutes, remote.EstimatedMinutes)
	case config.FieldStrategyNewest:
		// Already handled by default LWW merge above
	case config.FieldStrategyManual:
		if !intPtrEqual(local.EstimatedMinutes, remote.EstimatedMinutes) {
			manualConflicts = append(manualConflicts, ManualConflict{
				IssueID:     local.ID,
				Field:       "estimated_minutes",
				LocalValue:  derefIntPtr(local.EstimatedMinutes),
				RemoteValue: derefIntPtr(remote.EstimatedMinutes),
				LocalTime:   local.UpdatedAt,
				RemoteTime:  remote.UpdatedAt,
			})
			// Keep local value as tentative
			merged.EstimatedMinutes = local.EstimatedMinutes
		}
	}

	// Union merge: Labels (always union unless configured otherwise)
	labelsStrategy := fieldStrategies["labels"]
	if labelsStrategy == "" || labelsStrategy == config.FieldStrategyUnion {
		merged.Labels = mergeLabels(local.Labels, remote.Labels)
	} else if labelsStrategy == config.FieldStrategyNewest {
		// Use LWW for labels (already set from initial merge)
	}

	// Union merge: Dependencies (by DependsOnID+Type key)
	merged.Dependencies = mergeDependencies(local.Dependencies, remote.Dependencies)

	// Append merge: Comments (deduplicated)
	merged.Comments = mergeComments(local.Comments, remote.Comments)

	return &merged, manualConflicts
}

// maxInt returns the larger of two integers
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// maxIntPtr returns a pointer to the larger of two *int values
// Treats nil as 0 for comparison purposes
func maxIntPtr(a, b *int) *int {
	aVal := 0
	bVal := 0
	if a != nil {
		aVal = *a
	}
	if b != nil {
		bVal = *b
	}
	if aVal >= bVal {
		return a
	}
	return b
}

// derefIntPtr safely dereferences an int pointer, returning nil representation for display
func derefIntPtr(p *int) interface{} {
	if p == nil {
		return nil
	}
	return *p
}

// displayManualConflicts prints manual conflicts that need user resolution.
// These are fields where the configured strategy is "manual" and values differ.
func displayManualConflicts(conflicts []ManualConflict) {
	fmt.Fprintf(os.Stderr, "\n⚠ %d field conflict(s) require manual resolution:\n", len(conflicts))

	for _, c := range conflicts {
		fmt.Fprintf(os.Stderr, "\n  %s.%s:\n", c.IssueID, c.Field)
		fmt.Fprintf(os.Stderr, "    Local:  %v (set %s)\n", formatConflictValue(c.LocalValue), c.LocalTime.Format("2006-01-02"))
		fmt.Fprintf(os.Stderr, "    Remote: %v (set %s)\n", formatConflictValue(c.RemoteValue), c.RemoteTime.Format("2006-01-02"))
	}

	fmt.Fprintf(os.Stderr, "\n  To resolve, use one of:\n")
	fmt.Fprintf(os.Stderr, "    bd sync --ours      # Keep all local values\n")
	fmt.Fprintf(os.Stderr, "    bd sync --theirs    # Keep all remote values\n")
	fmt.Fprintf(os.Stderr, "    bd resolve <issue-id> <field> <value>  # Set specific value\n\n")
}

// formatConflictValue formats a conflict value for display
func formatConflictValue(v interface{}) string {
	if v == nil {
		return "(not set)"
	}
	return fmt.Sprintf("%v", v)
}

// mergeLabels performs set union on labels
func mergeLabels(local, remote []string) []string {
	seen := make(map[string]bool)
	var result []string

	// Add all local labels
	for _, label := range local {
		if !seen[label] {
			seen[label] = true
			result = append(result, label)
		}
	}

	// Add remote labels not in local
	for _, label := range remote {
		if !seen[label] {
			seen[label] = true
			result = append(result, label)
		}
	}

	// Sort for deterministic output
	sort.Strings(result)
	return result
}

// dependencyKey creates a unique key for deduplication
// Uses DependsOnID + Type as the identity (same target+type = same dependency)
func dependencyKey(d *beads.Dependency) string {
	if d == nil {
		return ""
	}
	return d.DependsOnID + ":" + string(d.Type)
}

// mergeDependencies performs set union on dependencies
func mergeDependencies(local, remote []*beads.Dependency) []*beads.Dependency {
	seen := make(map[string]*beads.Dependency)

	// Add all local dependencies
	for _, dep := range local {
		if dep == nil {
			continue
		}
		key := dependencyKey(dep)
		seen[key] = dep
	}

	// Add remote dependencies not in local (or with newer timestamp)
	for _, dep := range remote {
		if dep == nil {
			continue
		}
		key := dependencyKey(dep)
		if existing, ok := seen[key]; ok {
			// Keep the one with newer CreatedAt
			if dep.CreatedAt.After(existing.CreatedAt) {
				seen[key] = dep
			}
		} else {
			seen[key] = dep
		}
	}

	// Collect and sort by key for deterministic output
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	result := make([]*beads.Dependency, 0, len(keys))
	for _, k := range keys {
		result = append(result, seen[k])
	}

	return result
}

// commentKey creates a unique key for deduplication
// Uses ID if present, otherwise content hash
func commentKey(c *beads.Comment) string {
	if c == nil {
		return ""
	}
	if c.ID != 0 {
		return fmt.Sprintf("id:%d", c.ID)
	}
	// Fallback to content-based key for comments without ID
	return fmt.Sprintf("content:%s:%s", c.Author, c.Text)
}

// mergeComments performs append-merge on comments with deduplication
func mergeComments(local, remote []*beads.Comment) []*beads.Comment {
	seen := make(map[string]*beads.Comment)

	// Add all local comments
	for _, c := range local {
		if c == nil {
			continue
		}
		key := commentKey(c)
		seen[key] = c
	}

	// Add remote comments not in local
	for _, c := range remote {
		if c == nil {
			continue
		}
		key := commentKey(c)
		if _, ok := seen[key]; !ok {
			seen[key] = c
		}
	}

	// Collect all comments
	result := make([]*beads.Comment, 0, len(seen))
	for _, c := range seen {
		result = append(result, c)
	}

	// Sort by CreatedAt for chronological order
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})

	return result
}

// MergeIssues performs 3-way merge: base x local x remote -> merged
//
// Algorithm:
// 1. Build lookup maps for base, local, and remote by issue ID
// 2. Collect all unique issue IDs across all three sets
// 3. For each ID, apply MergeIssue to determine final state
// 4. Return merged result with per-issue strategy annotations
func MergeIssues(base, local, remote []*beads.Issue) *MergeResult {
	// Build lookup maps by issue ID
	baseMap := buildIssueMap(base)
	localMap := buildIssueMap(local)
	remoteMap := buildIssueMap(remote)

	// Collect all unique issue IDs
	allIDs := collectUniqueIDs(baseMap, localMap, remoteMap)

	result := &MergeResult{
		Merged:          make([]*beads.Issue, 0, len(allIDs)),
		Strategy:        make(map[string]string),
		ManualConflicts: make([]ManualConflict, 0),
	}

	for _, id := range allIDs {
		baseIssue := baseMap[id]
		localIssue := localMap[id]
		remoteIssue := remoteMap[id]

		merged, strategy, manualConflicts := MergeIssue(baseIssue, localIssue, remoteIssue)

		// Always record strategy (even for deletions, for logging/debugging)
		result.Strategy[id] = strategy

		// Collect manual conflicts for fields that need user resolution
		if len(manualConflicts) > 0 {
			result.ManualConflicts = append(result.ManualConflicts, manualConflicts...)
		}

		if merged != nil {
			result.Merged = append(result.Merged, merged)
			if strategy == StrategyMerged {
				result.Conflicts++
			}
		}
		// If merged is nil, the issue was deleted (present in base but not in local/remote)
	}

	return result
}

// MergeIssue merges a single issue using 3-way algorithm
//
// Cases:
// - base=nil: First sync (no common ancestor)
//   - local=nil, remote=nil: impossible (would not be in allIDs)
//   - local=nil: return remote (new from remote)
//   - remote=nil: return local (new from local)
//   - both exist: LWW by updated_at (both added independently)
//
// - base!=nil: Standard 3-way merge
//   - base=local=remote: no changes (same)
//   - base=local, remote differs: only remote changed (remote)
//   - base=remote, local differs: only local changed (local)
//   - local=remote (but differs from base): both made identical change (same)
//   - all three differ: true conflict, LWW by updated_at (merged)
//
// - Deletion handling:
//   - local=nil (deleted locally): if remote unchanged from base, delete; else keep remote
//   - remote=nil (deleted remotely): if local unchanged from base, delete; else keep local
func MergeIssue(base, local, remote *beads.Issue) (*beads.Issue, string, []ManualConflict) {
	// Case: no base state (first sync)
	if base == nil {
		if local == nil && remote == nil {
			// Should not happen (would not be in allIDs)
			return nil, StrategySame, nil
		}
		if local == nil {
			return remote, StrategyRemote, nil
		}
		if remote == nil {
			return local, StrategyLocal, nil
		}
		// Both exist with no base: treat as conflict, use field-level merge
		// This allows labels/comments to be union-merged even in first sync
		merged, manualConflicts := mergeFieldLevel(nil, local, remote)
		return merged, StrategyMerged, manualConflicts
	}

	// Case: local deleted
	if local == nil {
		// If remote unchanged from base, honor the local deletion
		if issueEqual(base, remote) {
			return nil, StrategyLocal, nil
		}
		// Remote changed after local deleted: keep remote (remote wins conflict)
		return remote, StrategyMerged, nil
	}

	// Case: remote deleted
	if remote == nil {
		// If local unchanged from base, honor the remote deletion
		if issueEqual(base, local) {
			return nil, StrategyRemote, nil
		}
		// Local changed after remote deleted: keep local (local wins conflict)
		return local, StrategyMerged, nil
	}

	// Standard 3-way cases (all three exist)
	if issueEqual(base, local) && issueEqual(base, remote) {
		// No changes anywhere
		return local, StrategySame, nil
	}

	if issueEqual(base, local) {
		// Only remote changed
		return remote, StrategyRemote, nil
	}

	if issueEqual(base, remote) {
		// Only local changed
		return local, StrategyLocal, nil
	}

	if issueEqual(local, remote) {
		// Both made identical change
		return local, StrategySame, nil
	}

	// True conflict: use field-level merge
	// - Scalar fields use LWW (remote wins on tie)
	// - Labels use union (no data loss)
	// - Dependencies use union (no data loss)
	// - Comments use append (deduplicated)
	// - compaction_level uses max (or configured strategy)
	// - estimated_minutes uses configured strategy (may flag for manual resolution)
	merged, manualConflicts := mergeFieldLevel(base, local, remote)
	return merged, StrategyMerged, manualConflicts
}

// issueEqual compares two issues for equality (content-level, not pointer)
// Compares all merge-relevant fields: content, status, workflow, assignment
func issueEqual(a, b *beads.Issue) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}

	// Core identification
	if a.ID != b.ID {
		return false
	}

	// Issue content
	if a.Title != b.Title ||
		a.Description != b.Description ||
		a.Design != b.Design ||
		a.AcceptanceCriteria != b.AcceptanceCriteria ||
		a.Notes != b.Notes {
		return false
	}

	// Status & workflow
	if a.Status != b.Status ||
		a.Priority != b.Priority ||
		a.IssueType != b.IssueType {
		return false
	}

	// Assignment
	if a.Assignee != b.Assignee {
		return false
	}
	if !intPtrEqual(a.EstimatedMinutes, b.EstimatedMinutes) {
		return false
	}

	// Timestamps (updated_at is crucial for LWW)
	if !a.UpdatedAt.Equal(b.UpdatedAt) {
		return false
	}

	// Closed state
	if !timePtrEqual(a.ClosedAt, b.ClosedAt) ||
		a.CloseReason != b.CloseReason {
		return false
	}

	// Time-based scheduling
	if !timePtrEqual(a.DueAt, b.DueAt) ||
		!timePtrEqual(a.DeferUntil, b.DeferUntil) {
		return false
	}

	// External reference
	if !stringPtrEqual(a.ExternalRef, b.ExternalRef) {
		return false
	}

	// Labels (order-independent comparison)
	if !stringSliceEqual(a.Labels, b.Labels) {
		return false
	}

	// Dependencies (order-independent comparison)
	if !dependenciesEqual(a.Dependencies, b.Dependencies) {
		return false
	}

	// Comments (order-independent comparison)
	if !commentsEqual(a.Comments, b.Comments) {
		return false
	}

	return true
}

// buildIssueMap creates a lookup map from issue ID to issue pointer
func buildIssueMap(issues []*beads.Issue) map[string]*beads.Issue {
	m := make(map[string]*beads.Issue, len(issues))
	for _, issue := range issues {
		if issue != nil {
			m[issue.ID] = issue
		}
	}
	return m
}

// collectUniqueIDs gathers all unique issue IDs from the three maps
// Returns sorted for deterministic output
func collectUniqueIDs(base, local, remote map[string]*beads.Issue) []string {
	seen := make(map[string]bool)
	for id := range base {
		seen[id] = true
	}
	for id := range local {
		seen[id] = true
	}
	for id := range remote {
		seen[id] = true
	}

	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Helper functions for pointer comparison

func intPtrEqual(a, b *int) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func stringPtrEqual(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func timePtrEqual(a, b *time.Time) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Equal(*b)
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	// Sort copies for order-independent comparison
	aCopy := make([]string, len(a))
	bCopy := make([]string, len(b))
	copy(aCopy, a)
	copy(bCopy, b)
	sort.Strings(aCopy)
	sort.Strings(bCopy)
	for i := range aCopy {
		if aCopy[i] != bCopy[i] {
			return false
		}
	}
	return true
}

// dependenciesEqual compares two dependency slices for equality (order-independent).
// Compares by (depends_on_id, type) pairs which form the logical identity of a dependency.
func dependenciesEqual(a, b []*beads.Dependency) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	// Build key sets for order-independent comparison
	aKeys := make([]string, len(a))
	for i, d := range a {
		aKeys[i] = fmt.Sprintf("%s:%s", d.DependsOnID, d.Type)
	}
	bKeys := make([]string, len(b))
	for i, d := range b {
		bKeys[i] = fmt.Sprintf("%s:%s", d.DependsOnID, d.Type)
	}
	sort.Strings(aKeys)
	sort.Strings(bKeys)
	for i := range aKeys {
		if aKeys[i] != bKeys[i] {
			return false
		}
	}
	return true
}

// commentsEqual compares two comment slices for equality (order-independent).
// Compares by (author, text, created_at) which identifies a comment's content.
func commentsEqual(a, b []*beads.Comment) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	// Build key sets for order-independent comparison
	aKeys := make([]string, len(a))
	for i, c := range a {
		aKeys[i] = fmt.Sprintf("%s:%s:%d", c.Author, c.Text, c.CreatedAt.UnixNano())
	}
	bKeys := make([]string, len(b))
	for i, c := range b {
		bKeys[i] = fmt.Sprintf("%s:%s:%d", c.Author, c.Text, c.CreatedAt.UnixNano())
	}
	sort.Strings(aKeys)
	sort.Strings(bKeys)
	for i := range aKeys {
		if aKeys[i] != bKeys[i] {
			return false
		}
	}
	return true
}

// Base state storage functions for sync_base.jsonl

const syncBaseFileName = "sync_base.jsonl"

// loadBaseState loads the last-synced state from .beads/sync_base.jsonl
// Returns empty slice if file doesn't exist (first sync scenario)
func loadBaseState(beadsDir string) ([]*beads.Issue, error) {
	baseStatePath := filepath.Join(beadsDir, syncBaseFileName)

	// Check if file exists
	if _, err := os.Stat(baseStatePath); os.IsNotExist(err) {
		// First sync: no base state
		return nil, nil
	}

	// Read and parse JSONL file
	file, err := os.Open(baseStatePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var issues []*beads.Issue
	scanner := bufio.NewScanner(file)
	// Increase buffer for large issues
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if line == "" {
			continue
		}

		var issue beads.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Skipping malformed line %d in sync_base.jsonl: %v\n", lineNum, err)
			continue
		}
		issues = append(issues, &issue)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return issues, nil
}

// saveBaseState writes the merged state to .beads/sync_base.jsonl
// This becomes the base for the next 3-way merge
func saveBaseState(beadsDir string, issues []*beads.Issue) error {
	baseStatePath := filepath.Join(beadsDir, syncBaseFileName)

	// Write to temp file first for atomicity
	tempPath := baseStatePath + ".tmp"
	file, err := os.Create(tempPath)
	if err != nil {
		return err
	}

	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)

	for _, issue := range issues {
		if err := encoder.Encode(issue); err != nil {
			_ = file.Close() // Best-effort cleanup
			_ = os.Remove(tempPath)
			return err
		}
	}

	if err := file.Close(); err != nil {
		_ = os.Remove(tempPath) // Best-effort cleanup
		return err
	}

	// Atomic rename
	return os.Rename(tempPath, baseStatePath)
}
