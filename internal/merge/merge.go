// Copyright (c) 2024 @neongreen (https://github.com/neongreen)
// Originally from: https://github.com/neongreen/mono/tree/main/beads-merge
//
// MIT License
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.
//
// ---
// Vendored into beads with permission from @neongreen.
// See: https://github.com/neongreen/mono/issues/240

package merge

import (
	"bufio"
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// Issue embeds types.Issue to prevent field drift (GH#1481).
// It only adds RawLine for conflict marker output.
// Dependencies are now inherited from types.Issue directly.
type Issue struct {
	types.Issue

	// RawLine stores the original JSON line for conflict marker output
	RawLine string `json:"-"`
}

// IssueKey uniquely identifies an issue for matching
type IssueKey struct {
	ID        string
	CreatedAt time.Time
	CreatedBy string
}

// Merge3Way performs a 3-way merge of JSONL issue files
func Merge3Way(outputPath, basePath, leftPath, rightPath string, debug bool) error {
	if debug {
		fmt.Fprintf(os.Stderr, "=== DEBUG MODE ===\n")
		fmt.Fprintf(os.Stderr, "Output path: %s\n", outputPath)
		fmt.Fprintf(os.Stderr, "Base path:   %s\n", basePath)
		fmt.Fprintf(os.Stderr, "Left path:   %s\n", leftPath)
		fmt.Fprintf(os.Stderr, "Right path:  %s\n", rightPath)
		fmt.Fprintf(os.Stderr, "\n")
	}

	// Read all three files
	baseIssues, err := readIssues(basePath)
	if err != nil {
		return fmt.Errorf("error reading base file: %w", err)
	}
	if debug {
		fmt.Fprintf(os.Stderr, "Base issues read: %d\n", len(baseIssues))
	}

	leftIssues, err := readIssues(leftPath)
	if err != nil {
		return fmt.Errorf("error reading left file: %w", err)
	}
	if debug {
		fmt.Fprintf(os.Stderr, "Left issues read: %d\n", len(leftIssues))
	}

	rightIssues, err := readIssues(rightPath)
	if err != nil {
		return fmt.Errorf("error reading right file: %w", err)
	}
	if debug {
		fmt.Fprintf(os.Stderr, "Right issues read: %d\n", len(rightIssues))
		fmt.Fprintf(os.Stderr, "\n")
	}

	// Perform 3-way merge
	result, conflicts := merge3Way(baseIssues, leftIssues, rightIssues, debug)

	if debug {
		fmt.Fprintf(os.Stderr, "Merge complete:\n")
		fmt.Fprintf(os.Stderr, "  Merged issues: %d\n", len(result))
		fmt.Fprintf(os.Stderr, "  Conflicts: %d\n", len(conflicts))
		fmt.Fprintf(os.Stderr, "\n")
	}

	// Open output file for writing
	outFile, err := os.Create(outputPath) // #nosec G304 -- outputPath provided by CLI flag but sanitized earlier
	if err != nil {
		return fmt.Errorf("error creating output file: %w", err)
	}
	defer outFile.Close()

	// Write merged result to output file
	for _, issue := range result {
		line, err := json.Marshal(issue)
		if err != nil {
			return fmt.Errorf("error marshaling issue %s: %w", issue.ID, err)
		}
		if _, err := fmt.Fprintln(outFile, string(line)); err != nil {
			return fmt.Errorf("error writing merged issue: %w", err)
		}
	}

	// Write conflicts to output file
	for _, conflict := range conflicts {
		if _, err := fmt.Fprintln(outFile, conflict); err != nil {
			return fmt.Errorf("error writing conflict: %w", err)
		}
	}

	if debug {
		fmt.Fprintf(os.Stderr, "Output written to: %s\n", outputPath)
		fmt.Fprintf(os.Stderr, "\n")

		// Show first few lines of output for debugging
		if err := outFile.Sync(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to sync output file: %v\n", err)
		}
		// #nosec G304 -- debug output reads file created earlier in same function
		if content, err := os.ReadFile(outputPath); err == nil {
			lines := 0
			fmt.Fprintf(os.Stderr, "Output file preview (first 10 lines):\n")
			for _, line := range splitLines(string(content)) {
				if lines >= 10 {
					fmt.Fprintf(os.Stderr, "... (%d more lines)\n", len(splitLines(string(content)))-10)
					break
				}
				fmt.Fprintf(os.Stderr, "  %s\n", line)
				lines++
			}
		}
		fmt.Fprintf(os.Stderr, "\n")
	}

	// Return error if there were conflicts (caller can check this)
	if len(conflicts) > 0 {
		if debug {
			fmt.Fprintf(os.Stderr, "Merge completed with %d conflicts\n", len(conflicts))
		}
		return fmt.Errorf("merge completed with %d conflicts", len(conflicts))
	}

	if debug {
		fmt.Fprintf(os.Stderr, "Merge completed successfully with no conflicts\n")
	}
	return nil
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func readIssues(path string) ([]Issue, error) {
	file, err := os.Open(path) // #nosec G304 -- path supplied by CLI flag and validated upstream
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	var issues []Issue
	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if line == "" {
			continue
		}

		var issue Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			return nil, fmt.Errorf("failed to parse line %d: %w", lineNum, err)
		}
		issue.RawLine = line
		issues = append(issues, issue)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading file: %w", err)
	}

	return issues, nil
}

func makeKey(issue Issue) IssueKey {
	return IssueKey{
		ID:        issue.ID,
		CreatedAt: issue.CreatedAt,
		CreatedBy: issue.CreatedBy,
	}
}

// Use constants from types package to avoid duplication
const (
	StatusTombstone = types.StatusTombstone
	StatusClosed    = types.StatusClosed
)

// Comment is an alias to types.Comment for backward compatibility
type Comment = types.Comment

// Alias TTL constants from types package for local use
var (
	DefaultTombstoneTTL = types.DefaultTombstoneTTL
	ClockSkewGrace      = types.ClockSkewGrace
)

// IsTombstone returns true if the issue has been soft-deleted
func IsTombstone(issue Issue) bool {
	return issue.Status == StatusTombstone
}

// IsExpiredTombstone returns true if the tombstone has exceeded its TTL.
// Non-tombstone issues always return false.
// ttl is the configured TTL duration; if zero, DefaultTombstoneTTL is used.
func IsExpiredTombstone(issue Issue, ttl time.Duration) bool {
	// Non-tombstones never expire
	if !IsTombstone(issue) {
		return false
	}

	// Tombstones without DeletedAt are not expired (safety: shouldn't happen in valid data)
	if issue.DeletedAt == nil || issue.DeletedAt.IsZero() {
		return false
	}

	// Use default TTL if not specified
	if ttl == 0 {
		ttl = DefaultTombstoneTTL
	}

	// Add clock skew grace period to the TTL
	effectiveTTL := ttl + ClockSkewGrace

	// Check if the tombstone has exceeded its TTL
	expirationTime := issue.DeletedAt.Add(effectiveTTL)
	return time.Now().After(expirationTime)
}

func merge3Way(base, left, right []Issue, debug bool) ([]Issue, []string) {
	return Merge3WayWithTTL(base, left, right, DefaultTombstoneTTL, debug)
}

// Merge3WayWithTTL performs a 3-way merge with configurable tombstone TTL.
// This is the core merge function that handles tombstone semantics.
// Use this when you need to configure TTL for testing, debugging, or
// per-repository configuration. For default TTL behavior, use merge3Way.
// When debug is true, logs resurrection events to stderr.
func Merge3WayWithTTL(base, left, right []Issue, ttl time.Duration, debug bool) ([]Issue, []string) {
	// Build maps for quick lookup by IssueKey
	baseMap := make(map[IssueKey]Issue)
	for _, issue := range base {
		baseMap[makeKey(issue)] = issue
	}

	leftMap := make(map[IssueKey]Issue)
	for _, issue := range left {
		leftMap[makeKey(issue)] = issue
	}

	rightMap := make(map[IssueKey]Issue)
	for _, issue := range right {
		rightMap[makeKey(issue)] = issue
	}

	// Also build ID-based maps for fallback matching
	// This handles cases where the same issue has slightly different CreatedAt/CreatedBy
	// (e.g., due to timestamp precision differences between systems)
	leftByID := make(map[string]Issue)
	for _, issue := range left {
		leftByID[issue.ID] = issue
	}

	rightByID := make(map[string]Issue)
	for _, issue := range right {
		rightByID[issue.ID] = issue
	}

	// Track which issues we've processed (by both key and ID)
	processed := make(map[IssueKey]bool)
	processedIDs := make(map[string]bool) // track processed IDs to avoid duplicates
	var result []Issue
	var conflicts []string

	// Process all unique keys
	allKeys := make(map[IssueKey]bool)
	for k := range baseMap {
		allKeys[k] = true
	}
	for k := range leftMap {
		allKeys[k] = true
	}
	for k := range rightMap {
		allKeys[k] = true
	}

	for key := range allKeys {
		if processed[key] {
			continue
		}
		processed[key] = true

		baseIssue, inBase := baseMap[key]
		leftIssue, inLeft := leftMap[key]
		rightIssue, inRight := rightMap[key]

		// ID-based fallback matching for tombstone preservation
		// If key doesn't match but same ID exists in the other side, use that
		if !inLeft && inRight {
			if fallback, found := leftByID[rightIssue.ID]; found {
				leftIssue = fallback
				inLeft = true
				// Mark the fallback's key as processed to avoid duplicate
				processed[makeKey(fallback)] = true
			}
		}
		if !inRight && inLeft {
			if fallback, found := rightByID[leftIssue.ID]; found {
				rightIssue = fallback
				inRight = true
				// Mark the fallback's key as processed to avoid duplicate
				processed[makeKey(fallback)] = true
			}
		}

		// Check if we've already processed this ID (via a different key)
		currentID := key.ID
		if currentID == "" {
			if inLeft {
				currentID = leftIssue.ID
			} else if inRight {
				currentID = rightIssue.ID
			} else if inBase {
				currentID = baseIssue.ID
			}
		}
		if currentID != "" && processedIDs[currentID] {
			continue
		}
		if currentID != "" {
			processedIDs[currentID] = true
		}

		// Determine tombstone status
		leftTombstone := inLeft && IsTombstone(leftIssue)
		rightTombstone := inRight && IsTombstone(rightIssue)

		// Handle different scenarios
		if inBase && inLeft && inRight {
			// All three present - handle tombstone cases first

			// CASE: Both are tombstones - merge tombstones (later deleted_at wins)
			if leftTombstone && rightTombstone {
				merged := mergeTombstones(leftIssue, rightIssue)
				result = append(result, merged)
				continue
			}

			// CASE: Left is tombstone, right is live
			if leftTombstone && !rightTombstone {
				if IsExpiredTombstone(leftIssue, ttl) {
					// Tombstone expired - resurrection allowed, keep live issue
					if debug {
						fmt.Fprintf(os.Stderr, "Issue %s resurrected (tombstone expired)\n", rightIssue.ID)
					}
					result = append(result, rightIssue)
				} else {
					// Tombstone wins
					result = append(result, leftIssue)
				}
				continue
			}

			// CASE: Right is tombstone, left is live
			if rightTombstone && !leftTombstone {
				if IsExpiredTombstone(rightIssue, ttl) {
					// Tombstone expired - resurrection allowed, keep live issue
					if debug {
						fmt.Fprintf(os.Stderr, "Issue %s resurrected (tombstone expired)\n", leftIssue.ID)
					}
					result = append(result, leftIssue)
				} else {
					// Tombstone wins
					result = append(result, rightIssue)
				}
				continue
			}

			// CASE: Both are live issues - standard merge
			merged, conflict := mergeIssue(baseIssue, leftIssue, rightIssue)
			if conflict != "" {
				conflicts = append(conflicts, conflict)
			} else {
				result = append(result, merged)
			}
		} else if !inBase && inLeft && inRight {
			// Added in both - handle tombstone cases

			// CASE: Both are tombstones - merge tombstones
			if leftTombstone && rightTombstone {
				merged := mergeTombstones(leftIssue, rightIssue)
				result = append(result, merged)
				continue
			}

			// CASE: Left is tombstone, right is live
			if leftTombstone && !rightTombstone {
				if IsExpiredTombstone(leftIssue, ttl) {
					if debug {
						fmt.Fprintf(os.Stderr, "Issue %s resurrected (tombstone expired)\n", rightIssue.ID)
					}
					result = append(result, rightIssue)
				} else {
					result = append(result, leftIssue)
				}
				continue
			}

			// CASE: Right is tombstone, left is live
			if rightTombstone && !leftTombstone {
				if IsExpiredTombstone(rightIssue, ttl) {
					if debug {
						fmt.Fprintf(os.Stderr, "Issue %s resurrected (tombstone expired)\n", leftIssue.ID)
					}
					result = append(result, leftIssue)
				} else {
					result = append(result, rightIssue)
				}
				continue
			}

			// CASE: Both are live - merge using deterministic rules with empty base
			emptyBase := Issue{
				Issue: types.Issue{
					ID:        leftIssue.ID,
					CreatedAt: leftIssue.CreatedAt,
					CreatedBy: leftIssue.CreatedBy,
				},
			}
			merged, _ := mergeIssue(emptyBase, leftIssue, rightIssue)
			result = append(result, merged)
		} else if inBase && inLeft && !inRight {
			// Deleted in right (implicitly), maybe modified in left
			// Check if left is a tombstone - tombstones must be preserved
			if leftTombstone {
				result = append(result, leftIssue)
				continue
			}
			// RULE 2: deletion always wins over modification
			// This is because deletion is an explicit action that should be preserved
			continue
		} else if inBase && !inLeft && inRight {
			// Deleted in left (implicitly), maybe modified in right
			// Check if right is a tombstone - tombstones must be preserved
			if rightTombstone {
				result = append(result, rightIssue)
				continue
			}
			// RULE 2: deletion always wins over modification
			// This is because deletion is an explicit action that should be preserved
			continue
		} else if !inBase && inLeft && !inRight {
			// Added only in left (could be a tombstone)
			result = append(result, leftIssue)
		} else if !inBase && !inLeft && inRight {
			// Added only in right (could be a tombstone)
			result = append(result, rightIssue)
		}
	}

	// Sort by ID for deterministic output (matches bd export behavior)
	slices.SortFunc(result, func(a, b Issue) int {
		return cmp.Compare(a.ID, b.ID)
	})

	return result, conflicts
}

// mergeTombstones merges two tombstones for the same issue.
// The tombstone with the later deleted_at timestamp wins.
//
// Edge cases for nil/zero DeletedAt:
//   - If both nil/zero: left wins (arbitrary but deterministic)
//   - If left nil/zero, right not: right wins (has timestamp)
//   - If right nil/zero, left not: left wins (has timestamp)
//
// Nil/zero DeletedAt shouldn't happen in valid data (validation catches it),
// but we handle it defensively here.
func mergeTombstones(left, right Issue) Issue {
	leftHasDeletedAt := left.DeletedAt != nil && !left.DeletedAt.IsZero()
	rightHasDeletedAt := right.DeletedAt != nil && !right.DeletedAt.IsZero()

	// Handle nil/zero DeletedAt explicitly for clarity
	if !leftHasDeletedAt && !rightHasDeletedAt {
		// Both invalid - left wins as tie-breaker
		return left
	}
	if !leftHasDeletedAt {
		// Left invalid, right valid - right wins
		return right
	}
	if !rightHasDeletedAt {
		// Right invalid, left valid - left wins
		return left
	}
	// Both valid - use later deleted_at as the authoritative tombstone
	if isTimePtrAfter(left.DeletedAt, right.DeletedAt) {
		return left
	}
	return right
}

func mergeIssue(base, left, right Issue) (Issue, string) {
	result := Issue{
		Issue: types.Issue{
			ID:        base.ID,
			CreatedAt: base.CreatedAt,
			CreatedBy: base.CreatedBy,
		},
	}

	// Merge title - on conflict, side with latest updated_at wins
	result.Title = mergeFieldByUpdatedAt(base.Title, left.Title, right.Title, left.UpdatedAt, right.UpdatedAt)

	// Merge description - on conflict, side with latest updated_at wins
	result.Description = mergeFieldByUpdatedAt(base.Description, left.Description, right.Description, left.UpdatedAt, right.UpdatedAt)

	// Merge notes - on conflict, concatenate both sides
	result.Notes = mergeNotes(base.Notes, left.Notes, right.Notes)

	// Merge status - SPECIAL RULE: closed always wins over open
	result.Status = mergeStatus(base.Status, left.Status, right.Status)

	// Merge priority - on conflict, higher priority wins (lower number = more urgent)
	result.Priority = mergePriority(base.Priority, left.Priority, right.Priority)

	// Merge issue_type - on conflict, local (left) wins
	result.IssueType = mergeIssueType(base.IssueType, left.IssueType, right.IssueType)

	// Merge updated_at - take the max
	result.UpdatedAt = maxTime(left.UpdatedAt, right.UpdatedAt)

	// Merge closed_at - only if status is closed
	// This prevents invalid state (status=open with closed_at set)
	if result.Status == StatusClosed {
		result.ClosedAt = maxTimePtr(left.ClosedAt, right.ClosedAt)
		// Merge close_reason and closed_by_session - use value from side with later closed_at (GH#891)
		// This ensures we keep the most recent close action's metadata
		if isTimePtrAfter(left.ClosedAt, right.ClosedAt) {
			result.CloseReason = left.CloseReason
			result.ClosedBySession = left.ClosedBySession
		} else if right.ClosedAt != nil && !right.ClosedAt.IsZero() {
			result.CloseReason = right.CloseReason
			result.ClosedBySession = right.ClosedBySession
		} else {
			// Both nil/zero or only left has value - prefer left
			result.CloseReason = left.CloseReason
			result.ClosedBySession = left.ClosedBySession
		}
	} else {
		result.ClosedAt = nil
		result.CloseReason = ""
		result.ClosedBySession = ""
	}

	// Merge dependencies - proper 3-way merge where removals win
	result.Dependencies = mergeDependencies(base.Dependencies, left.Dependencies, right.Dependencies)

	// === Merge new fields added in GH#1480 ===
	// For most fields, use standard 3-way merge (left wins on conflict)

	// Content fields
	result.Design = mergeField(base.Design, left.Design, right.Design)
	result.AcceptanceCriteria = mergeField(base.AcceptanceCriteria, left.AcceptanceCriteria, right.AcceptanceCriteria)
	result.SpecID = mergeField(base.SpecID, left.SpecID, right.SpecID)

	// Assignment - use standard merge (left wins on conflict)
	result.Assignee = mergeField(base.Assignee, left.Assignee, right.Assignee)
	result.Owner = mergeField(base.Owner, left.Owner, right.Owner)

	// Scheduling - use maxTimePtr for pointer time fields (latest wins)
	result.DueAt = maxTimePtr(left.DueAt, right.DueAt)
	result.DeferUntil = maxTimePtr(left.DeferUntil, right.DeferUntil)

	// External references
	result.ExternalRef = mergeStringPtr(base.ExternalRef, left.ExternalRef, right.ExternalRef)
	result.SourceSystem = mergeField(base.SourceSystem, left.SourceSystem, right.SourceSystem)

	// Labels - prefer left's version on conflict (no sophisticated merge for now)
	result.Labels = mergeLabels(base.Labels, left.Labels, right.Labels)

	// Comments - prefer left's version on conflict
	result.Comments = mergeCommentPtrs(base.Comments, left.Comments, right.Comments)

	// Metadata - prefer the version from the side with latest updated_at
	result.Metadata = mergeMetadata(base.Metadata, left.Metadata, right.Metadata, left.UpdatedAt, right.UpdatedAt)

	// Gate fields
	result.AwaitType = mergeField(base.AwaitType, left.AwaitType, right.AwaitType)
	result.AwaitID = mergeField(base.AwaitID, left.AwaitID, right.AwaitID)
	result.Timeout = mergeDuration(base.Timeout, left.Timeout, right.Timeout)
	result.Waiters = mergeStringSlice(base.Waiters, left.Waiters, right.Waiters)

	// Flags - any true value wins (union semantics)
	result.Pinned = base.Pinned || left.Pinned || right.Pinned
	result.IsTemplate = base.IsTemplate || left.IsTemplate || right.IsTemplate
	result.Ephemeral = base.Ephemeral || left.Ephemeral || right.Ephemeral
	result.Crystallizes = base.Crystallizes || left.Crystallizes || right.Crystallizes

	// Wisp/messaging
	result.Sender = mergeField(base.Sender, left.Sender, right.Sender)
	result.WispType = mergeWispType(base.WispType, left.WispType, right.WispType)

	// Agent fields
	result.HookBead = mergeField(base.HookBead, left.HookBead, right.HookBead)
	result.RoleBead = mergeField(base.RoleBead, left.RoleBead, right.RoleBead)
	result.AgentState = mergeAgentState(base.AgentState, left.AgentState, right.AgentState)
	result.LastActivity = maxTimePtr(left.LastActivity, right.LastActivity)
	result.RoleType = mergeField(base.RoleType, left.RoleType, right.RoleType)
	result.Rig = mergeField(base.Rig, left.Rig, right.Rig)

	// Molecule type
	result.MolType = mergeMolType(base.MolType, left.MolType, right.MolType)

	// Compaction fields - prefer newer compaction
	if isTimePtrAfter(left.CompactedAt, right.CompactedAt) {
		result.CompactionLevel = left.CompactionLevel
		result.CompactedAt = left.CompactedAt
		result.CompactedAtCommit = left.CompactedAtCommit
		result.OriginalSize = left.OriginalSize
	} else if right.CompactedAt != nil && !right.CompactedAt.IsZero() {
		result.CompactionLevel = right.CompactionLevel
		result.CompactedAt = right.CompactedAt
		result.CompactedAtCommit = right.CompactedAtCommit
		result.OriginalSize = right.OriginalSize
	} else {
		result.CompactionLevel = left.CompactionLevel
		result.CompactedAt = left.CompactedAt
		result.CompactedAtCommit = left.CompactedAtCommit
		result.OriginalSize = left.OriginalSize
	}

	// Slot holder
	result.Holder = mergeField(base.Holder, left.Holder, right.Holder)

	// Formula tracking
	result.SourceFormula = mergeField(base.SourceFormula, left.SourceFormula, right.SourceFormula)
	result.SourceLocation = mergeField(base.SourceLocation, left.SourceLocation, right.SourceLocation)

	// Estimated minutes - prefer non-nil, then left
	result.EstimatedMinutes = mergeIntPtr(base.EstimatedMinutes, left.EstimatedMinutes, right.EstimatedMinutes)

	// Quality score - prefer non-nil, then left
	result.QualityScore = mergeFloatPtr(base.QualityScore, left.QualityScore, right.QualityScore)

	// If status became tombstone via mergeStatus safety fallback,
	// copy tombstone fields from whichever side has them
	if result.Status == StatusTombstone {
		// Prefer the side with more recent deleted_at, or left if tied
		if isTimePtrAfter(left.DeletedAt, right.DeletedAt) {
			result.DeletedAt = left.DeletedAt
			result.DeletedBy = left.DeletedBy
			result.DeleteReason = left.DeleteReason
			result.OriginalType = left.OriginalType
		} else if right.DeletedAt != nil && !right.DeletedAt.IsZero() {
			result.DeletedAt = right.DeletedAt
			result.DeletedBy = right.DeletedBy
			result.DeleteReason = right.DeleteReason
			result.OriginalType = right.OriginalType
		} else if left.DeletedAt != nil {
			result.DeletedAt = left.DeletedAt
			result.DeletedBy = left.DeletedBy
			result.DeleteReason = left.DeleteReason
			result.OriginalType = left.OriginalType
		}
		// Note: if neither has DeletedAt, tombstone fields remain empty
		// This represents invalid data that validation should catch
	}

	// All field conflicts are now auto-resolved deterministically
	return result, ""
}

func mergeStatus(base, left, right types.Status) types.Status {
	// RULE 0: tombstone is handled at the merge3Way level, not here.
	// If a tombstone status reaches here, it means both sides have the same
	// issue with possibly different statuses - tombstone should not be one of them
	// (that case is handled by the tombstone merge logic).
	// However, if somehow one side has tombstone status, preserve it as a safety measure.
	if left == StatusTombstone || right == StatusTombstone {
		// This shouldn't happen in normal flow - tombstones are handled earlier
		// But if it does, tombstone wins (deletion is explicit)
		return StatusTombstone
	}

	// RULE 1: closed always wins over open
	// This prevents the insane situation where issues never die
	if left == StatusClosed || right == StatusClosed {
		return StatusClosed
	}

	// Otherwise use standard 3-way merge
	if base == left && base != right {
		return right
	}
	if base == right && base != left {
		return left
	}
	// Both changed to same value or no change - left wins
	return left
}

func mergeField(base, left, right string) string {
	if base == left && base != right {
		return right
	}
	if base == right && base != left {
		return left
	}
	// Both changed to same value or no change - left wins
	return left
}

// mergeFieldByUpdatedAt resolves conflicts by picking the value from the side
// with the latest updated_at timestamp
func mergeFieldByUpdatedAt(base, left, right string, leftUpdatedAt, rightUpdatedAt time.Time) string {
	// Standard 3-way merge for non-conflict cases
	if base == left && base != right {
		return right
	}
	if base == right && base != left {
		return left
	}
	if left == right {
		return left
	}
	// True conflict: both sides changed to different values
	// Pick the value from the side with the latest updated_at
	if isTimeAfter(leftUpdatedAt, rightUpdatedAt) {
		return left
	}
	return right
}

// mergeIssueType performs a 3-way merge for IssueType
func mergeIssueType(base, left, right types.IssueType) types.IssueType {
	if base == left && base != right {
		return right
	}
	if base == right && base != left {
		return left
	}
	// Both changed to same value or no change - left wins
	return left
}

// mergeStringPtr performs a 3-way merge for *string fields
func mergeStringPtr(base, left, right *string) *string {
	baseVal := ""
	if base != nil {
		baseVal = *base
	}
	leftVal := ""
	if left != nil {
		leftVal = *left
	}
	rightVal := ""
	if right != nil {
		rightVal = *right
	}

	if baseVal == leftVal && baseVal != rightVal {
		return right
	}
	if baseVal == rightVal && baseVal != leftVal {
		return left
	}
	// Both changed to same value or no change - left wins
	return left
}

// mergeDuration performs a 3-way merge for time.Duration fields
func mergeDuration(base, left, right time.Duration) time.Duration {
	if base == left && base != right {
		return right
	}
	if base == right && base != left {
		return left
	}
	// Both changed to same value or no change - left wins
	return left
}

// mergeWispType performs a 3-way merge for WispType
func mergeWispType(base, left, right types.WispType) types.WispType {
	if base == left && base != right {
		return right
	}
	if base == right && base != left {
		return left
	}
	return left
}

// mergeAgentState performs a 3-way merge for AgentState
func mergeAgentState(base, left, right types.AgentState) types.AgentState {
	if base == left && base != right {
		return right
	}
	if base == right && base != left {
		return left
	}
	return left
}

// mergeMolType performs a 3-way merge for MolType
func mergeMolType(base, left, right types.MolType) types.MolType {
	if base == left && base != right {
		return right
	}
	if base == right && base != left {
		return left
	}
	return left
}

// mergeNotes handles notes merging - on conflict, concatenate both sides
func mergeNotes(base, left, right string) string {
	// Standard 3-way merge for non-conflict cases
	if base == left && base != right {
		return right
	}
	if base == right && base != left {
		return left
	}
	if left == right {
		return left
	}
	// True conflict: both sides changed to different values - concatenate
	if left == "" {
		return right
	}
	if right == "" {
		return left
	}
	return left + "\n\n---\n\n" + right
}

// mergePriority handles priority merging - on conflict, higher priority wins (lower number)
// Special case: 0 is treated as "unset/no priority" due to Go's zero value.
// Any explicitly set priority (!=0) wins over 0.
func mergePriority(base, left, right int) int {
	// Standard 3-way merge for non-conflict cases
	if base == left && base != right {
		return right
	}
	if base == right && base != left {
		return left
	}
	if left == right {
		return left
	}
	// True conflict: both sides changed to different values

	// Treat 0 as "unset" - explicitly set priority wins over unset
	// Use != 0 instead of > 0 to handle negative priorities
	if left == 0 && right != 0 {
		return right // right has explicit priority, left is unset
	}
	if right == 0 && left != 0 {
		return left // left has explicit priority, right is unset
	}

	// Both have explicit priorities (or both are 0) - higher priority wins (lower number = more urgent)
	if left < right {
		return left
	}
	return right
}

// isTimeAfter returns true if t1 is after t2 (or equal - left wins on tie).
// Zero times are treated as "unset" - a set time beats an unset time.
// On exact tie, returns true (left wins for consistency with original behavior).
func isTimeAfter(t1, t2 time.Time) bool {
	t1Zero := t1.IsZero()
	t2Zero := t2.IsZero()

	if t1Zero && t2Zero {
		return true // both unset, left wins
	}
	if t1Zero {
		return false // t1 unset, t2 set - t2 wins
	}
	if t2Zero {
		return true // t1 set, t2 unset - t1 wins
	}

	// Both set - left wins on tie (matching original: !time2.After(time1))
	return !t2.After(t1)
}

// isTimePtrAfter returns true if t1 is after t2 (or equal - left wins on tie) for pointer times.
// Nil pointers are treated as "unset" - a set time beats an unset time.
// On exact tie, returns true (left wins for consistency with original behavior).
func isTimePtrAfter(t1, t2 *time.Time) bool {
	t1Set := t1 != nil && !t1.IsZero()
	t2Set := t2 != nil && !t2.IsZero()

	if !t1Set && !t2Set {
		return true // both unset, left wins
	}
	if !t1Set {
		return false // t1 unset, t2 set - t2 wins
	}
	if !t2Set {
		return true // t1 set, t2 unset - t1 wins
	}

	// Both set - left wins on tie (matching original: !time2.After(time1))
	return !t2.After(*t1)
}

// maxTime returns the later of two times.
// Zero times are treated as "unset" - a set time beats an unset time.
func maxTime(t1, t2 time.Time) time.Time {
	t1Zero := t1.IsZero()
	t2Zero := t2.IsZero()

	if t1Zero && t2Zero {
		return time.Time{}
	}
	if t1Zero {
		return t2
	}
	if t2Zero {
		return t1
	}

	if t1.After(t2) {
		return t1
	}
	return t2
}

// maxTimePtr returns the later of two pointer times.
// Nil or zero times are treated as "unset" - a set time beats an unset time.
func maxTimePtr(t1, t2 *time.Time) *time.Time {
	t1Set := t1 != nil && !t1.IsZero()
	t2Set := t2 != nil && !t2.IsZero()

	if !t1Set && !t2Set {
		return nil
	}
	if !t1Set {
		return t2
	}
	if !t2Set {
		return t1
	}

	if t1.After(*t2) {
		return t1
	}
	return t2
}

// mergeDependencies performs a proper 3-way merge of dependencies
// Key principle: REMOVALS ARE AUTHORITATIVE
// - If dep was in base and removed by left OR right → exclude (removal wins)
// - If dep wasn't in base and added by left OR right → include
// - If dep was in base and both still have it → include
func mergeDependencies(base, left, right []*types.Dependency) []*types.Dependency {
	// Build sets for O(1) lookup
	depKey := func(dep *types.Dependency) string {
		if dep == nil {
			return ""
		}
		return fmt.Sprintf("%s:%s:%s", dep.IssueID, dep.DependsOnID, dep.Type)
	}

	baseSet := make(map[string]bool)
	for _, dep := range base {
		if dep != nil {
			baseSet[depKey(dep)] = true
		}
	}

	leftSet := make(map[string]bool)
	leftDeps := make(map[string]*types.Dependency)
	for _, dep := range left {
		if dep != nil {
			key := depKey(dep)
			leftSet[key] = true
			leftDeps[key] = dep
		}
	}

	rightSet := make(map[string]bool)
	rightDeps := make(map[string]*types.Dependency)
	for _, dep := range right {
		if dep != nil {
			key := depKey(dep)
			rightSet[key] = true
			rightDeps[key] = dep
		}
	}

	// Collect all unique keys
	allKeys := make(map[string]bool)
	for k := range baseSet {
		allKeys[k] = true
	}
	for k := range leftSet {
		allKeys[k] = true
	}
	for k := range rightSet {
		allKeys[k] = true
	}

	var result []*types.Dependency
	seen := make(map[string]bool)

	for key := range allKeys {
		inBase := baseSet[key]
		inLeft := leftSet[key]
		inRight := rightSet[key]

		// 3-way merge logic:
		if inBase {
			// Was in base - check if either side removed it
			if !inLeft {
				// Left removed it → don't include (left wins)
				continue
			}
			if !inRight {
				// Right removed it → don't include (right wins)
				continue
			}
			// Both still have it → include
		} else {
			// Wasn't in base - must have been added by left or right
			if !inLeft && !inRight {
				// Neither has it (shouldn't happen but handle gracefully)
				continue
			}
			// At least one side added it → include
		}

		if !seen[key] {
			seen[key] = true
			// Prefer left's version of the dep (for any metadata differences)
			if dep, ok := leftDeps[key]; ok {
				result = append(result, dep)
			} else if dep, ok := rightDeps[key]; ok {
				result = append(result, dep)
			}
		}
	}

	return result
}


// === Helper functions for new field merging (GH#1480) ===

// mergeLabels performs a 3-way merge of labels using standard 3-way merge semantics.
// On conflict, left wins (consistent with other field merge behavior).
func mergeLabels(base, left, right []string) []string {
	// Standard 3-way merge logic
	if slicesEqual(base, left) && !slicesEqual(base, right) {
		return right // Only right changed
	}
	if slicesEqual(base, right) && !slicesEqual(base, left) {
		return left // Only left changed
	}
	// Both changed or neither changed - left wins
	return left
}

// slicesEqual checks if two string slices are equal
func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// mergeCommentPtrs performs a 3-way merge of comment pointers using standard 3-way merge semantics.
// On conflict, left wins (consistent with other field merge behavior).
func mergeCommentPtrs(base, left, right []*types.Comment) []*types.Comment {
	// Standard 3-way merge logic
	if commentSlicesEqual(base, left) && !commentSlicesEqual(base, right) {
		return right // Only right changed
	}
	if commentSlicesEqual(base, right) && !commentSlicesEqual(base, left) {
		return left // Only left changed
	}
	// Both changed or neither changed - left wins
	return left
}

// commentSlicesEqual checks if two comment slices are equal by comparing IDs
func commentSlicesEqual(a, b []*types.Comment) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] == nil && b[i] == nil {
			continue
		}
		if a[i] == nil || b[i] == nil {
			return false
		}
		if a[i].ID != b[i].ID {
			return false
		}
	}
	return true
}

// mergeMetadata performs a 3-way merge of JSON metadata.
// Prefers the version from the side with the latest updated_at.
func mergeMetadata(base, left, right json.RawMessage, leftUpdatedAt, rightUpdatedAt time.Time) json.RawMessage {
	// If both sides have the same metadata as base, keep base
	if jsonEqual(base, left) && jsonEqual(base, right) {
		return base
	}

	// If only left changed
	if jsonEqual(base, right) && !jsonEqual(base, left) {
		return left
	}

	// If only right changed
	if jsonEqual(base, left) && !jsonEqual(base, right) {
		return right
	}

	// Both changed - prefer the one with latest updated_at
	if isTimeAfter(leftUpdatedAt, rightUpdatedAt) {
		return left
	}
	return right
}

// jsonEqual compares two JSON values for equality
func jsonEqual(a, b json.RawMessage) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	// Simple byte comparison (works for our use case)
	return string(a) == string(b)
}

// mergeInt64 performs a 3-way merge of int64 values.
// Uses standard merge semantics (left wins on conflict).
func mergeInt64(base, left, right int64) int64 {
	if base == left && base != right {
		return right
	}
	if base == right && base != left {
		return left
	}
	// Both changed to same value or no change - left wins
	return left
}

// mergeStringSlice performs a 3-way merge of string slices using standard 3-way merge semantics.
// On conflict, left wins (consistent with other field merge behavior).
func mergeStringSlice(base, left, right []string) []string {
	// Standard 3-way merge logic
	if slicesEqual(base, left) && !slicesEqual(base, right) {
		return right // Only right changed
	}
	if slicesEqual(base, right) && !slicesEqual(base, left) {
		return left // Only left changed
	}
	// Both changed or neither changed - left wins
	return left
}

// mergeIntPtr performs a 3-way merge of *int values.
// Prefers non-nil values, then left on conflict.
func mergeIntPtr(base, left, right *int) *int {
	// If both sides have the same value as base, keep base
	if intPtrEqual(base, left) && intPtrEqual(base, right) {
		return base
	}

	// If only left changed
	if intPtrEqual(base, right) && !intPtrEqual(base, left) {
		return left
	}

	// If only right changed
	if intPtrEqual(base, left) && !intPtrEqual(base, right) {
		return right
	}

	// Both changed - prefer non-nil, then left
	if left != nil {
		return left
	}
	return right
}

func intPtrEqual(a, b *int) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// mergeFloatPtr performs a 3-way merge of *float32 values.
// Prefers non-nil values, then left on conflict.
func mergeFloatPtr(base, left, right *float32) *float32 {
	// If both sides have the same value as base, keep base
	if floatPtrEqual(base, left) && floatPtrEqual(base, right) {
		return base
	}

	// If only left changed
	if floatPtrEqual(base, right) && !floatPtrEqual(base, left) {
		return left
	}

	// If only right changed
	if floatPtrEqual(base, left) && !floatPtrEqual(base, right) {
		return right
	}

	// Both changed - prefer non-nil, then left
	if left != nil {
		return left
	}
	return right
}

func floatPtrEqual(a, b *float32) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}
