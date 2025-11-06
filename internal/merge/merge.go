// Package merge implements 3-way merge for beads JSONL files.
//
// This code is vendored from https://github.com/neongreen/mono/tree/main/beads-merge
// Original author: Emily (@neongreen, https://github.com/neongreen)
//
// MIT License
// Copyright (c) 2025 Emily
// See ATTRIBUTION.md for full license text
//
// The merge algorithm provides field-level intelligent merging for beads issues:
// - Matches issues by identity (id + created_at + created_by)
// - Smart field merging with 3-way comparison
// - Dependency union with deduplication
// - Timestamp handling (max wins)
// - Deletion detection
package merge

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/steveyegge/beads/internal/types"
)

// IssueKey uniquely identifies an issue for matching across merge branches
type IssueKey struct {
	ID        string
	CreatedAt string
	CreatedBy string
}

// issueWithRaw wraps an issue with its original JSONL line for conflict output
type issueWithRaw struct {
	Issue   *types.Issue
	RawLine string
}

// ReadIssues reads issues from a JSONL file
func ReadIssues(path string) ([]*types.Issue, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	var issues []*types.Issue
	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if line == "" {
			continue
		}

		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			return nil, fmt.Errorf("failed to parse line %d: %w", lineNum, err)
		}
		issues = append(issues, &issue)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading file: %w", err)
	}

	return issues, nil
}

// makeKey creates an IssueKey from an issue for identity matching
func makeKey(issue *types.Issue) IssueKey {
	// Use created_at for key (created_by not tracked in types.Issue currently)
	return IssueKey{
		ID:        issue.ID,
		CreatedAt: issue.CreatedAt.Format(time.RFC3339Nano),
		CreatedBy: "", // Not currently tracked, rely on ID + timestamp
	}
}

// Merge3Way performs a 3-way merge of issue lists
// Returns merged issues and conflict markers (if any)
func Merge3Way(base, left, right []*types.Issue) ([]*types.Issue, []string) {
	// Convert to maps with raw lines preserved
	baseMap := make(map[IssueKey]issueWithRaw)
	for _, issue := range base {
		raw, _ := json.Marshal(issue)
		baseMap[makeKey(issue)] = issueWithRaw{issue, string(raw)}
	}

	leftMap := make(map[IssueKey]issueWithRaw)
	for _, issue := range left {
		raw, _ := json.Marshal(issue)
		leftMap[makeKey(issue)] = issueWithRaw{issue, string(raw)}
	}

	rightMap := make(map[IssueKey]issueWithRaw)
	for _, issue := range right {
		raw, _ := json.Marshal(issue)
		rightMap[makeKey(issue)] = issueWithRaw{issue, string(raw)}
	}

	// Track which issues we've processed
	processed := make(map[IssueKey]bool)
	var result []*types.Issue
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

		// Handle different scenarios
		if inBase && inLeft && inRight {
			// All three present - merge
			merged, conflict := mergeIssue(baseIssue, leftIssue, rightIssue)
			if conflict != "" {
				conflicts = append(conflicts, conflict)
			} else {
				result = append(result, merged)
			}
		} else if !inBase && inLeft && inRight {
			// Added in both - check if identical
			if issuesEqual(leftIssue.Issue, rightIssue.Issue) {
				result = append(result, leftIssue.Issue)
			} else {
				conflicts = append(conflicts, makeConflict(leftIssue.RawLine, rightIssue.RawLine))
			}
		} else if inBase && inLeft && !inRight {
			// Deleted in right, maybe modified in left
			if issuesEqual(baseIssue.Issue, leftIssue.Issue) {
				// Deleted in right, unchanged in left - accept deletion
				continue
			} else {
				// Modified in left, deleted in right - conflict
				conflicts = append(conflicts, makeConflictWithBase(baseIssue.RawLine, leftIssue.RawLine, ""))
			}
		} else if inBase && !inLeft && inRight {
			// Deleted in left, maybe modified in right
			if issuesEqual(baseIssue.Issue, rightIssue.Issue) {
				// Deleted in left, unchanged in right - accept deletion
				continue
			} else {
				// Modified in right, deleted in left - conflict
				conflicts = append(conflicts, makeConflictWithBase(baseIssue.RawLine, "", rightIssue.RawLine))
			}
		} else if !inBase && inLeft && !inRight {
			// Added only in left
			result = append(result, leftIssue.Issue)
		} else if !inBase && !inLeft && inRight {
			// Added only in right
			result = append(result, rightIssue.Issue)
		}
	}

	return result, conflicts
}

func mergeIssue(base, left, right issueWithRaw) (*types.Issue, string) {
	result := &types.Issue{
		ID:        base.Issue.ID,
		CreatedAt: base.Issue.CreatedAt,
	}

	// Merge title
	result.Title = mergeField(base.Issue.Title, left.Issue.Title, right.Issue.Title)

	// Merge description
	result.Description = mergeField(base.Issue.Description, left.Issue.Description, right.Issue.Description)

	// Merge notes
	result.Notes = mergeField(base.Issue.Notes, left.Issue.Notes, right.Issue.Notes)

	// Merge design
	result.Design = mergeField(base.Issue.Design, left.Issue.Design, right.Issue.Design)

	// Merge acceptance criteria
	result.AcceptanceCriteria = mergeField(base.Issue.AcceptanceCriteria, left.Issue.AcceptanceCriteria, right.Issue.AcceptanceCriteria)

	// Merge status
	result.Status = types.Status(mergeField(string(base.Issue.Status), string(left.Issue.Status), string(right.Issue.Status)))

	// Merge priority
	if base.Issue.Priority == left.Issue.Priority && base.Issue.Priority != right.Issue.Priority {
		result.Priority = right.Issue.Priority
	} else if base.Issue.Priority == right.Issue.Priority && base.Issue.Priority != left.Issue.Priority {
		result.Priority = left.Issue.Priority
	} else {
		result.Priority = left.Issue.Priority
	}

	// Merge issue_type
	result.IssueType = types.IssueType(mergeField(string(base.Issue.IssueType), string(left.Issue.IssueType), string(right.Issue.IssueType)))

	// Merge updated_at - take the max
	result.UpdatedAt = maxTime(left.Issue.UpdatedAt, right.Issue.UpdatedAt)

	// Merge closed_at - take the max
	if left.Issue.ClosedAt != nil && right.Issue.ClosedAt != nil {
		max := maxTime(*left.Issue.ClosedAt, *right.Issue.ClosedAt)
		result.ClosedAt = &max
	} else if left.Issue.ClosedAt != nil {
		result.ClosedAt = left.Issue.ClosedAt
	} else if right.Issue.ClosedAt != nil {
		result.ClosedAt = right.Issue.ClosedAt
	}

	// Merge dependencies - combine and deduplicate
	result.Dependencies = mergeDependencies(left.Issue.Dependencies, right.Issue.Dependencies)

	// Merge labels - combine and deduplicate
	result.Labels = mergeLabels(left.Issue.Labels, right.Issue.Labels)

	// Copy other fields from left (assignee, external_ref, source_repo)
	result.Assignee = left.Issue.Assignee
	result.ExternalRef = left.Issue.ExternalRef
	result.SourceRepo = left.Issue.SourceRepo

	// Check if we have a real conflict
	if hasConflict(base.Issue, left.Issue, right.Issue) {
		return result, makeConflictWithBase(base.RawLine, left.RawLine, right.RawLine)
	}

	return result, ""
}

func mergeField(base, left, right string) string {
	if base == left && base != right {
		return right
	}
	if base == right && base != left {
		return left
	}
	// Both changed to same value or no change
	return left
}

func maxTime(t1, t2 time.Time) time.Time {
	if t1.After(t2) {
		return t1
	}
	return t2
}

func mergeDependencies(left, right []*types.Dependency) []*types.Dependency {
	seen := make(map[string]bool)
	var result []*types.Dependency

	for _, dep := range left {
		key := fmt.Sprintf("%s:%s:%s", dep.IssueID, dep.DependsOnID, dep.Type)
		if !seen[key] {
			seen[key] = true
			result = append(result, dep)
		}
	}

	for _, dep := range right {
		key := fmt.Sprintf("%s:%s:%s", dep.IssueID, dep.DependsOnID, dep.Type)
		if !seen[key] {
			seen[key] = true
			result = append(result, dep)
		}
	}

	return result
}

func mergeLabels(left, right []string) []string {
	seen := make(map[string]bool)
	var result []string

	for _, label := range left {
		if !seen[label] {
			seen[label] = true
			result = append(result, label)
		}
	}

	for _, label := range right {
		if !seen[label] {
			seen[label] = true
			result = append(result, label)
		}
	}

	return result
}

func hasConflict(base, left, right *types.Issue) bool {
	// Check if any field has conflicting changes (all three different)
	if base.Title != left.Title && base.Title != right.Title && left.Title != right.Title {
		return true
	}
	if base.Description != left.Description && base.Description != right.Description && left.Description != right.Description {
		return true
	}
	if base.Notes != left.Notes && base.Notes != right.Notes && left.Notes != right.Notes {
		return true
	}
	if base.Status != left.Status && base.Status != right.Status && left.Status != right.Status {
		return true
	}
	if base.Priority != left.Priority && base.Priority != right.Priority && left.Priority != right.Priority {
		return true
	}
	if base.IssueType != left.IssueType && base.IssueType != right.IssueType && left.IssueType != right.IssueType {
		return true
	}
	return false
}

func issuesEqual(a, b *types.Issue) bool {
	// Use go-cmp for deep equality comparison
	return cmp.Equal(a, b)
}

func makeConflict(left, right string) string {
	conflict := "<<<<<<< left\n"
	if left != "" {
		conflict += left + "\n"
	}
	conflict += "=======\n"
	if right != "" {
		conflict += right + "\n"
	}
	conflict += ">>>>>>> right\n"
	return conflict
}

func makeConflictWithBase(base, left, right string) string {
	conflict := "<<<<<<< left\n"
	if left != "" {
		conflict += left + "\n"
	}
	conflict += "||||||| base\n"
	if base != "" {
		conflict += base + "\n"
	}
	conflict += "=======\n"
	if right != "" {
		conflict += right + "\n"
	}
	conflict += ">>>>>>> right\n"
	return conflict
}

// MergeFiles performs 3-way merge on JSONL files and writes result to output
// Returns true if conflicts were found, false if merge was clean
func MergeFiles(outputPath, basePath, leftPath, rightPath string, debug bool) (bool, error) {
	// Read all input files
	baseIssues, err := ReadIssues(basePath)
	if err != nil {
		return false, fmt.Errorf("failed to read base file: %w", err)
	}
	
	leftIssues, err := ReadIssues(leftPath)
	if err != nil {
		return false, fmt.Errorf("failed to read left file: %w", err)
	}
	
	rightIssues, err := ReadIssues(rightPath)
	if err != nil {
		return false, fmt.Errorf("failed to read right file: %w", err)
	}
	
	if debug {
		fmt.Fprintf(os.Stderr, "Base issues: %d\n", len(baseIssues))
		fmt.Fprintf(os.Stderr, "Left issues: %d\n", len(leftIssues))
		fmt.Fprintf(os.Stderr, "Right issues: %d\n", len(rightIssues))
	}
	
	// Perform 3-way merge
	merged, conflicts := Merge3Way(baseIssues, leftIssues, rightIssues)
	
	if debug {
		fmt.Fprintf(os.Stderr, "Merged issues: %d\n", len(merged))
		fmt.Fprintf(os.Stderr, "Conflicts: %d\n", len(conflicts))
	}
	
	// Write output file
	outFile, err := os.Create(outputPath)
	if err != nil {
		return false, fmt.Errorf("failed to create output file: %w", err)
	}
	defer outFile.Close()
	
	// Write merged issues
	for _, issue := range merged {
		data, err := json.Marshal(issue)
		if err != nil {
			return false, fmt.Errorf("failed to marshal issue: %w", err)
		}
		if _, err := outFile.Write(data); err != nil {
			return false, fmt.Errorf("failed to write issue: %w", err)
		}
		if _, err := outFile.WriteString("\n"); err != nil {
			return false, fmt.Errorf("failed to write newline: %w", err)
		}
	}
	
	// Write conflict markers if any
	for _, conflict := range conflicts {
		if _, err := outFile.WriteString(conflict); err != nil {
			return false, fmt.Errorf("failed to write conflict: %w", err)
		}
	}
	
	hasConflicts := len(conflicts) > 0
	return hasConflicts, nil
}
