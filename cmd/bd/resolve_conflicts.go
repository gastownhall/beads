package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/merge"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var resolveConflictsCmd = &cobra.Command{
	Use:     "resolve-conflicts [file]",
	GroupID: GroupMaintenance,
	Short:   "Resolve git merge conflicts in JSONL files",
	Long: `Resolve git merge conflict markers in beads JSONL files.

When git merges fail to auto-resolve, JSONL files can end up with conflict
markers (<<<<<<, =======, >>>>>>). This command parses those markers and
resolves the conflicts using beads merge semantics.

Modes:
  mechanical (default)  Uses deterministic merge rules (updated_at wins, etc.)
  interactive           Prompts for each conflict (not yet implemented)

The file defaults to .beads/issues.jsonl if not specified.

Examples:
  bd resolve-conflicts                    # Resolve conflicts in .beads/issues.jsonl
  bd resolve-conflicts --dry-run          # Show what would be resolved
  bd resolve-conflicts custom.jsonl       # Resolve conflicts in custom file
  bd resolve-conflicts --json             # Output results as JSON`,
	Args: cobra.MaximumNArgs(1),
	// PreRun disables PersistentPreRun for this command (no database needed)
	PreRun: func(cmd *cobra.Command, args []string) {},
	Run:    runResolveConflicts,
}

var (
	resolveConflictsMode   string
	resolveConflictsDryRun bool
	resolveConflictsJSON   bool
	resolveConflictsPath   string
)

func init() {
	resolveConflictsCmd.Flags().StringVar(&resolveConflictsMode, "mode", "mechanical", "Resolution mode: mechanical, interactive")
	resolveConflictsCmd.Flags().BoolVar(&resolveConflictsDryRun, "dry-run", false, "Show what would be resolved without making changes")
	resolveConflictsCmd.Flags().BoolVar(&resolveConflictsJSON, "json", false, "Output results as JSON")
	resolveConflictsCmd.Flags().StringVar(&resolveConflictsPath, "path", ".", "Path to repository with .beads directory")
	rootCmd.AddCommand(resolveConflictsCmd)
}

// conflictRegion represents a single conflict in the file
type conflictRegion struct {
	StartLine  int      // Line number where <<<<<<< starts
	EndLine    int      // Line number where >>>>>>> ends
	LeftSide   []string // Lines between <<<<<<< and =======
	RightSide  []string // Lines between ======= and >>>>>>>
	LeftLabel  string   // Label after <<<<<<< (e.g., "HEAD")
	RightLabel string   // Label after >>>>>>> (e.g., "branch-name")
}

// resolveConflictsResult is the JSON output structure
type resolveConflictsResult struct {
	FilePath          string                   `json:"file_path"`
	DryRun            bool                     `json:"dry_run"`
	Mode              string                   `json:"mode"`
	ConflictsFound    int                      `json:"conflicts_found"`
	ConflictsResolved int                      `json:"conflicts_resolved"`
	Status            string                   `json:"status"` // "success", "no_conflicts", "dry_run", "error"
	BackupPath        string                   `json:"backup_path,omitempty"`
	Error             string                   `json:"error,omitempty"`
	Conflicts         []conflictResolutionInfo `json:"conflicts,omitempty"`
}

type conflictResolutionInfo struct {
	LineRange  string `json:"line_range"`
	LeftLabel  string `json:"left_label"`
	RightLabel string `json:"right_label"`
	Resolution string `json:"resolution"` // "merged", "left", "right", "both"
	IssueID    string `json:"issue_id,omitempty"`
}

func runResolveConflicts(cmd *cobra.Command, args []string) {
	// Determine file path
	var filePath string
	if len(args) > 0 {
		filePath = args[0]
	} else {
		filePath = defaultResolveConflictsFilePath(resolveConflictsPath)
	}

	// Validate mode
	if resolveConflictsMode != "mechanical" && resolveConflictsMode != "interactive" {
		outputResolveError(filePath, fmt.Sprintf("invalid mode: %s (use 'mechanical' or 'interactive')", resolveConflictsMode))
		os.Exit(1)
	}

	if resolveConflictsMode == "interactive" {
		outputResolveError(filePath, "interactive mode not yet implemented")
		os.Exit(1)
	}

	// Check file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		outputResolveError(filePath, fmt.Sprintf("file not found: %s", filePath))
		os.Exit(1)
	}

	// Read file content
	content, err := os.ReadFile(filePath) // #nosec G304 -- user-provided path for conflict resolution
	if err != nil {
		outputResolveError(filePath, fmt.Sprintf("reading file: %v", err))
		os.Exit(1)
	}

	// Parse conflicts
	conflicts, cleanLines, err := parseConflicts(string(content))
	if err != nil {
		outputResolveError(filePath, fmt.Sprintf("parsing conflicts: %v", err))
		os.Exit(1)
	}

	result := resolveConflictsResult{
		FilePath:       filePath,
		DryRun:         resolveConflictsDryRun,
		Mode:           resolveConflictsMode,
		ConflictsFound: len(conflicts),
	}

	// No conflicts case
	if len(conflicts) == 0 {
		result.Status = "no_conflicts"
		if resolveConflictsJSON {
			outputResolveJSON(result, 0)
		}
		fmt.Printf("%s No conflict markers found in %s\n", ui.RenderPass("✓"), filePath)
		return
	}

	if !resolveConflictsJSON {
		fmt.Printf("Found %d conflict region(s) in %s\n", len(conflicts), filePath)
		if resolveConflictsDryRun {
			fmt.Println("[DRY-RUN] No changes will be made")
		}
		fmt.Println()
	}

	// Resolve each conflict
	var resolvedLines []string
	resolvedLines = append(resolvedLines, cleanLines...)

	for i, conflict := range conflicts {
		resolution, info := resolveConflict(conflict, i+1)
		result.Conflicts = append(result.Conflicts, info)

		if !resolveConflictsJSON && !resolveConflictsDryRun {
			fmt.Printf("  Conflict %d (lines %d-%d): %s\n", i+1, conflict.StartLine, conflict.EndLine, info.Resolution)
		}

		resolvedLines = append(resolvedLines, resolution...)
		result.ConflictsResolved++
	}

	// Dry-run output
	if resolveConflictsDryRun {
		result.Status = "dry_run"
		if resolveConflictsJSON {
			outputResolveJSON(result, 0)
		} else {
			fmt.Printf("[DRY-RUN] Would resolve %d conflict(s)\n", len(conflicts))
			for i, info := range result.Conflicts {
				fmt.Printf("  %d. Lines %s: %s", i+1, info.LineRange, info.Resolution)
				if info.IssueID != "" {
					fmt.Printf(" (issue: %s)", info.IssueID)
				}
				fmt.Println()
			}
		}
		return
	}

	// Create backup
	backupPath := filePath + ".pre-resolve"
	if err := copyFile(filePath, backupPath); err != nil {
		outputResolveError(filePath, fmt.Sprintf("creating backup: %v", err))
		os.Exit(1)
	}
	result.BackupPath = backupPath

	if !resolveConflictsJSON {
		fmt.Printf("  Backup created: %s\n", filepath.Base(backupPath))
	}

	// Write resolved content
	output := strings.Join(resolvedLines, "\n")
	if len(resolvedLines) > 0 {
		output += "\n"
	}
	if err := os.WriteFile(filePath, []byte(output), 0644); err != nil { // #nosec G306 -- standard file permissions
		outputResolveError(filePath, fmt.Sprintf("writing file: %v", err))
		os.Exit(1)
	}

	result.Status = "success"
	if resolveConflictsJSON {
		outputResolveJSON(result, 0)
	} else {
		fmt.Println()
		fmt.Printf("%s Resolved %d conflict(s) in %s\n", ui.RenderPass("✓"), len(conflicts), filepath.Base(filePath))
		fmt.Printf("Backup preserved at: %s\n", filepath.Base(backupPath))
	}
}

func defaultResolveConflictsFilePath(rootPath string) string {
	return filepath.Join(rootPath, ".beads", "issues.jsonl")
}

// parseConflicts extracts conflict regions and non-conflicted lines from content
func parseConflicts(content string) ([]conflictRegion, []string, error) {
	var conflicts []conflictRegion
	var cleanLines []string

	scanner := bufio.NewScanner(strings.NewReader(content))
	lineNum := 0
	var current *conflictRegion
	inLeft := false
	inRight := false

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// Detect conflict start
		if strings.HasPrefix(line, "<<<<<<<") {
			if current != nil {
				return nil, nil, fmt.Errorf("nested conflict at line %d", lineNum)
			}
			current = &conflictRegion{
				StartLine: lineNum,
				LeftLabel: strings.TrimSpace(strings.TrimPrefix(line, "<<<<<<<")),
			}
			inLeft = true
			continue
		}

		// Detect conflict separator
		if strings.HasPrefix(line, "=======") && current != nil {
			inLeft = false
			inRight = true
			continue
		}

		// Detect conflict end
		if strings.HasPrefix(line, ">>>>>>>") && current != nil {
			current.EndLine = lineNum
			current.RightLabel = strings.TrimSpace(strings.TrimPrefix(line, ">>>>>>>"))
			conflicts = append(conflicts, *current)
			current = nil
			inLeft = false
			inRight = false
			continue
		}

		// Collect lines
		if current != nil {
			if inLeft {
				current.LeftSide = append(current.LeftSide, line)
			} else if inRight {
				current.RightSide = append(current.RightSide, line)
			}
		} else {
			cleanLines = append(cleanLines, line)
		}
	}

	if current != nil {
		return nil, nil, fmt.Errorf("unclosed conflict starting at line %d", current.StartLine)
	}

	return conflicts, cleanLines, scanner.Err()
}

// resolveConflict resolves a single conflict region using merge semantics
func resolveConflict(conflict conflictRegion, _ int) ([]string, conflictResolutionInfo) {
	info := conflictResolutionInfo{
		LineRange:  fmt.Sprintf("%d-%d", conflict.StartLine, conflict.EndLine),
		LeftLabel:  conflict.LeftLabel,
		RightLabel: conflict.RightLabel,
	}

	// Try to parse left and right as JSON issues
	var leftIssues, rightIssues []merge.Issue

	for _, line := range conflict.LeftSide {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var issue merge.Issue
		if err := json.Unmarshal([]byte(line), &issue); err == nil {
			issue.RawLine = line
			leftIssues = append(leftIssues, issue)
		}
	}

	for _, line := range conflict.RightSide {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var issue merge.Issue
		if err := json.Unmarshal([]byte(line), &issue); err == nil {
			issue.RawLine = line
			rightIssues = append(rightIssues, issue)
		}
	}

	// If we couldn't parse as JSON, keep both sides
	if len(leftIssues) == 0 && len(rightIssues) == 0 {
		info.Resolution = "kept_both_unparseable"
		var result []string
		result = append(result, conflict.LeftSide...)
		result = append(result, conflict.RightSide...)
		return result, info
	}

	// If only one side has valid JSON, use that
	if len(leftIssues) > 0 && len(rightIssues) == 0 {
		info.Resolution = "left_only_valid"
		if len(leftIssues) == 1 {
			info.IssueID = leftIssues[0].ID
		}
		return conflict.LeftSide, info
	}

	if len(rightIssues) > 0 && len(leftIssues) == 0 {
		info.Resolution = "right_only_valid"
		if len(rightIssues) == 1 {
			info.IssueID = rightIssues[0].ID
		}
		return conflict.RightSide, info
	}

	// Both sides have valid JSON - merge them
	// Use the 3-way merge logic with empty base (both sides added the same issue)
	var result []string
	mergedIDs := make(map[string]bool)

	for _, left := range leftIssues {
		// Find matching right issue by ID
		var matchingRight *merge.Issue
		for i := range rightIssues {
			if rightIssues[i].ID == left.ID {
				matchingRight = &rightIssues[i]
				break
			}
		}

		if matchingRight != nil {
			// Merge the two versions
			merged := mergeIssueConflict(left, *matchingRight)
			mergedJSON, err := json.Marshal(merged)
			if err != nil {
				// Fall back to left on marshal error
				result = append(result, left.RawLine)
			} else {
				result = append(result, string(mergedJSON))
			}
			mergedIDs[left.ID] = true
			info.IssueID = left.ID
			info.Resolution = "merged"
		} else {
			// No matching right issue - keep left
			result = append(result, left.RawLine)
		}
	}

	// Add any right issues that weren't merged
	for _, right := range rightIssues {
		if !mergedIDs[right.ID] {
			result = append(result, right.RawLine)
		}
	}

	if info.Resolution == "" {
		info.Resolution = "merged_multiple"
	}

	return result, info
}

// mergeIssueConflict merges two conflicting issue versions
// Uses similar logic to internal/merge but simplified for conflict resolution
func mergeIssueConflict(left, right merge.Issue) merge.Issue {
	result := merge.Issue{
		Issue: types.Issue{
			ID:        left.ID,
			CreatedAt: left.CreatedAt,
			CreatedBy: left.CreatedBy,
		},
	}

	// Title: prefer later updated_at
	result.Title = pickByUpdatedAt(left.Title, right.Title, left.UpdatedAt, right.UpdatedAt)

	// Description: prefer later updated_at
	result.Description = pickByUpdatedAt(left.Description, right.Description, left.UpdatedAt, right.UpdatedAt)

	// Notes: concatenate if different
	if left.Notes == right.Notes {
		result.Notes = left.Notes
	} else if left.Notes == "" {
		result.Notes = right.Notes
	} else if right.Notes == "" {
		result.Notes = left.Notes
	} else {
		result.Notes = left.Notes + "\n\n---\n\n" + right.Notes
	}

	// Status: tombstone wins (explicit deletion), then closed
	if left.Status == types.StatusTombstone || right.Status == types.StatusTombstone {
		result.Status = types.StatusTombstone
	} else if left.Status == types.StatusClosed || right.Status == types.StatusClosed {
		result.Status = types.StatusClosed
	} else {
		result.Status = pickStatusByUpdatedAt(left.Status, right.Status, left.UpdatedAt, right.UpdatedAt)
	}

	// Priority: lower number (higher priority) wins
	if left.Priority != 0 && right.Priority != 0 {
		if left.Priority < right.Priority {
			result.Priority = left.Priority
		} else {
			result.Priority = right.Priority
		}
	} else if left.Priority != 0 {
		result.Priority = left.Priority
	} else {
		result.Priority = right.Priority
	}

	// IssueType: prefer left
	if left.IssueType != "" {
		result.IssueType = left.IssueType
	} else {
		result.IssueType = right.IssueType
	}

	// UpdatedAt: max
	result.UpdatedAt = maxTimeVal(left.UpdatedAt, right.UpdatedAt)

	// ClosedAt: max (if status is closed)
	if result.Status == types.StatusClosed {
		result.ClosedAt = maxTimePtr(left.ClosedAt, right.ClosedAt)
		// CloseReason and ClosedBySession from whichever has later ClosedAt
		if isTimePtrAfter(left.ClosedAt, right.ClosedAt) {
			result.CloseReason = left.CloseReason
			result.ClosedBySession = left.ClosedBySession
		} else {
			result.CloseReason = right.CloseReason
			result.ClosedBySession = right.ClosedBySession
		}
	}

	// Dependencies: union (left wins on duplicates)
	depMap := make(map[string]*types.Dependency)
	for _, dep := range left.Dependencies {
		if dep == nil {
			continue
		}
		key := fmt.Sprintf("%s:%s:%s", dep.IssueID, dep.DependsOnID, dep.Type)
		depMap[key] = dep
	}
	for _, dep := range right.Dependencies {
		if dep == nil {
			continue
		}
		key := fmt.Sprintf("%s:%s:%s", dep.IssueID, dep.DependsOnID, dep.Type)
		if _, exists := depMap[key]; !exists {
			depMap[key] = dep
		}
	}
	for _, dep := range depMap {
		result.Dependencies = append(result.Dependencies, dep)
	}

	// Tombstone fields
	if result.Status == types.StatusTombstone {
		if isTimePtrAfter(left.DeletedAt, right.DeletedAt) {
			result.DeletedAt = left.DeletedAt
			result.DeletedBy = left.DeletedBy
			result.DeleteReason = left.DeleteReason
			result.OriginalType = left.OriginalType
		} else {
			result.DeletedAt = right.DeletedAt
			result.DeletedBy = right.DeletedBy
			result.DeleteReason = right.DeleteReason
			result.OriginalType = right.OriginalType
		}
	}

	return result
}

func pickByUpdatedAt(left, right string, leftTime, rightTime time.Time) string {
	if left == right {
		return left
	}
	if isTimeAfter(leftTime, rightTime) {
		return left
	}
	return right
}

func pickStatusByUpdatedAt(left, right types.Status, leftTime, rightTime time.Time) types.Status {
	if left == right {
		return left
	}
	if isTimeAfter(leftTime, rightTime) {
		return left
	}
	return right
}

func maxTimeVal(t1, t2 time.Time) time.Time {
	if t1.IsZero() {
		return t2
	}
	if t2.IsZero() {
		return t1
	}
	if t1.After(t2) {
		return t1
	}
	return t2
}

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

// isTimeAfter returns true if t1 is after t2 (or equal - left wins on tie).
// Zero times are treated as "unset" - a set time beats an unset time.
// Matches internal/merge.isTimeAfter semantics for consistency.
func isTimeAfter(t1, t2 time.Time) bool {
	if t1.IsZero() && t2.IsZero() {
		return true // both unset, left wins
	}
	if t1.IsZero() {
		return false // t1 unset, t2 set - t2 wins
	}
	if t2.IsZero() {
		return true // t1 set, t2 unset - t1 wins
	}
	// Both set - left wins on tie (matches internal/merge)
	return !t2.After(t1)
}

// isTimePtrAfter returns true if t1 is after t2 (or equal - left wins on tie) for pointer times.
// Nil pointers are treated as "unset" - a set time beats an unset time.
// Matches internal/merge.isTimePtrAfter semantics for consistency.
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
	// Both set - left wins on tie (matches internal/merge)
	return !t2.After(*t1)
}

func outputResolveError(filePath, errMsg string) {
	if resolveConflictsJSON {
		result := resolveConflictsResult{
			FilePath: filePath,
			Status:   "error",
			Error:    errMsg,
		}
		outputResolveJSON(result, 1)
	} else {
		fmt.Fprintf(os.Stderr, "Error: %s\n", errMsg)
	}
}

func outputResolveJSON(result resolveConflictsResult, exitCode int) {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, `{"error": "failed to marshal JSON: %v"}`, err)
		os.Exit(1)
	}
	fmt.Println(string(data))
	os.Exit(exitCode)
}
