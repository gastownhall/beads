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
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// Issue represents a beads issue with all possible fields
type Issue struct {
	ID           string       `json:"id"`
	Title        string       `json:"title,omitempty"`
	Description  string       `json:"description,omitempty"`
	Notes        string       `json:"notes,omitempty"`
	Status       string       `json:"status,omitempty"`
	Priority     int          `json:"priority"` // No omitempty: 0 is valid (P0/critical)
	IssueType    string       `json:"issue_type,omitempty"`
	CreatedAt    string       `json:"created_at,omitempty"`
	UpdatedAt    string       `json:"updated_at,omitempty"`
	ClosedAt     string       `json:"closed_at,omitempty"`
	CreatedBy    string       `json:"created_by,omitempty"`
	Dependencies []Dependency `json:"dependencies,omitempty"`
	RawLine      string       `json:"-"` // Store original line for conflict output
	// Tombstone fields: inline soft-delete support for merge
	DeletedAt    string `json:"deleted_at,omitempty"`    // When the issue was deleted
	DeletedBy    string `json:"deleted_by,omitempty"`    // Who deleted the issue
	DeleteReason string `json:"delete_reason,omitempty"` // Why the issue was deleted
	OriginalType string `json:"original_type,omitempty"` // Issue type before deletion
}

// Dependency represents an issue dependency
type Dependency struct {
	IssueID     string `json:"issue_id"`
	DependsOnID string `json:"depends_on_id"`
	Type        string `json:"type"`
	CreatedAt   string `json:"created_at"`
	CreatedBy   string `json:"created_by"`
}

// IssueKey uniquely identifies an issue for matching
type IssueKey struct {
	ID        string
	CreatedAt string
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
	StatusTombstone = string(types.StatusTombstone)
	StatusClosed    = string(types.StatusClosed)
)

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
	if issue.DeletedAt == "" {
		return false
	}

	// Use default TTL if not specified
	if ttl == 0 {
		ttl = DefaultTombstoneTTL
	}

	// Parse the deleted_at timestamp
	deletedAt, err := time.Parse(time.RFC3339Nano, issue.DeletedAt)
	if err != nil {
		deletedAt, err = time.Parse(time.RFC3339, issue.DeletedAt)
		if err != nil {
			// Invalid timestamp means not expired (safety)
			return false
		}
	}

	// Add clock skew grace period to the TTL
	effectiveTTL := ttl + ClockSkewGrace

	// Check if the tombstone has exceeded its TTL
	expirationTime := deletedAt.Add(effectiveTTL)
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
				ID:        leftIssue.ID,
				CreatedAt: leftIssue.CreatedAt,
				CreatedBy: leftIssue.CreatedBy,
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

	return result, conflicts
}

// mergeTombstones merges two tombstones for the same issue.
// The tombstone with the later deleted_at timestamp wins.
//
// Edge cases for empty DeletedAt:
//   - If both empty: left wins (arbitrary but deterministic)
//   - If left empty, right not: right wins (has timestamp)
//   - If right empty, left not: left wins (has timestamp)
//
// Empty DeletedAt shouldn't happen in valid data (validation catches it),
// but we handle it defensively here.
func mergeTombstones(left, right Issue) Issue {
	// Handle empty DeletedAt explicitly for clarity
	if left.DeletedAt == "" && right.DeletedAt == "" {
		// Both invalid - left wins as tie-breaker
		return left
	}
	if left.DeletedAt == "" {
		// Left invalid, right valid - right wins
		return right
	}
	if right.DeletedAt == "" {
		// Right invalid, left valid - left wins
		return left
	}
	// Both valid - use later deleted_at as the authoritative tombstone
	if isTimeAfter(left.DeletedAt, right.DeletedAt) {
		return left
	}
	return right
}

func mergeIssue(base, left, right Issue) (Issue, string) {
	result := Issue{
		ID:        base.ID,
		CreatedAt: base.CreatedAt,
		CreatedBy: base.CreatedBy,
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
	result.IssueType = mergeField(base.IssueType, left.IssueType, right.IssueType)

	// Merge updated_at - take the max
	result.UpdatedAt = maxTime(left.UpdatedAt, right.UpdatedAt)

	// Merge closed_at - only if status is closed
	// This prevents invalid state (status=open with closed_at set)
	if result.Status == StatusClosed {
		result.ClosedAt = maxTime(left.ClosedAt, right.ClosedAt)
	} else {
		result.ClosedAt = ""
	}

	// Merge dependencies - proper 3-way merge where removals win
	result.Dependencies = mergeDependencies(base.Dependencies, left.Dependencies, right.Dependencies)

	// If status became tombstone via mergeStatus safety fallback,
	// copy tombstone fields from whichever side has them
	if result.Status == StatusTombstone {
		// Prefer the side with more recent deleted_at, or left if tied
		if isTimeAfter(left.DeletedAt, right.DeletedAt) {
			result.DeletedAt = left.DeletedAt
			result.DeletedBy = left.DeletedBy
			result.DeleteReason = left.DeleteReason
			result.OriginalType = left.OriginalType
		} else if right.DeletedAt != "" {
			result.DeletedAt = right.DeletedAt
			result.DeletedBy = right.DeletedBy
			result.DeleteReason = right.DeleteReason
			result.OriginalType = right.OriginalType
		} else if left.DeletedAt != "" {
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

func mergeStatus(base, left, right string) string {
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
	return mergeField(base, left, right)
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
func mergeFieldByUpdatedAt(base, left, right, leftUpdatedAt, rightUpdatedAt string) string {
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

// isTimeAfter returns true if t1 is after t2
func isTimeAfter(t1, t2 string) bool {
	if t1 == "" {
		return false
	}
	if t2 == "" {
		return true
	}

	time1, err1 := time.Parse(time.RFC3339Nano, t1)
	if err1 != nil {
		time1, err1 = time.Parse(time.RFC3339, t1)
	}

	time2, err2 := time.Parse(time.RFC3339Nano, t2)
	if err2 != nil {
		time2, err2 = time.Parse(time.RFC3339, t2)
	}

	// Handle parse errors consistently with maxTime:
	// - Valid timestamp beats invalid
	// - If both invalid, prefer left (t1) for consistency
	if err1 != nil && err2 != nil {
		return true // both invalid, prefer left
	}
	if err1 != nil {
		return false // t1 invalid, t2 valid - t2 wins
	}
	if err2 != nil {
		return true // t1 valid, t2 invalid - t1 wins
	}

	// Both valid - compare. On exact tie, left wins for consistency with IssueType rule
	// Using !time2.After(time1) returns true when t1 > t2 OR t1 == t2
	return !time2.After(time1)
}

func maxTime(t1, t2 string) string {
	if t1 == "" && t2 == "" {
		return ""
	}
	if t1 == "" {
		return t2
	}
	if t2 == "" {
		return t1
	}

	// Try RFC3339Nano first (supports fractional seconds), fall back to RFC3339
	time1, err1 := time.Parse(time.RFC3339Nano, t1)
	if err1 != nil {
		time1, err1 = time.Parse(time.RFC3339, t1)
	}

	time2, err2 := time.Parse(time.RFC3339Nano, t2)
	if err2 != nil {
		time2, err2 = time.Parse(time.RFC3339, t2)
	}

	// If both fail to parse, return t2 as fallback
	if err1 != nil && err2 != nil {
		return t2
	}
	// If only t1 failed to parse, return t2
	if err1 != nil {
		return t2
	}
	// If only t2 failed to parse, return t1
	if err2 != nil {
		return t1
	}

	if time1.After(time2) {
		return t1
	}
	return t2
}

// mergeDependencies performs a proper 3-way merge of dependencies
// Key principle: REMOVALS ARE AUTHORITATIVE
// - If dep was in base and removed by left OR right → exclude (removal wins)
// - If dep wasn't in base and added by left OR right → include
// - If dep was in base and both still have it → include
func mergeDependencies(base, left, right []Dependency) []Dependency {
	// Build sets for O(1) lookup
	depKey := func(dep Dependency) string {
		return fmt.Sprintf("%s:%s:%s", dep.IssueID, dep.DependsOnID, dep.Type)
	}

	baseSet := make(map[string]bool)
	for _, dep := range base {
		baseSet[depKey(dep)] = true
	}

	leftSet := make(map[string]bool)
	leftDeps := make(map[string]Dependency)
	for _, dep := range left {
		key := depKey(dep)
		leftSet[key] = true
		leftDeps[key] = dep
	}

	rightSet := make(map[string]bool)
	rightDeps := make(map[string]Dependency)
	for _, dep := range right {
		key := depKey(dep)
		rightSet[key] = true
		rightDeps[key] = dep
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

	var result []Dependency
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

