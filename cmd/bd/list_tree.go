package main

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// buildIssueTree builds parent-child tree structure from issues
// Uses actual parent-child dependencies from the database when store is provided
func buildIssueTree(issues []*types.Issue) (roots []*types.Issue, childrenMap map[string][]*types.Issue) {
	return buildIssueTreeWithDeps(issues, nil)
}

// buildIssueTreeWithDeps builds parent-child tree using dependency records
// If allDeps is nil, falls back to dotted ID hierarchy (e.g., "parent.1")
// Only parent-child dependency edges establish nesting; other edge types
// (blocks, waits-for, discovered-from, relates-to, ...) are workflow/graph
// links and are not rendered as hierarchy.
func buildIssueTreeWithDeps(issues []*types.Issue, allDeps map[string][]*types.Dependency) (roots []*types.Issue, childrenMap map[string][]*types.Issue) {
	issueMap := make(map[string]*types.Issue)
	childrenMap = make(map[string][]*types.Issue)
	isChild := make(map[string]bool)

	for _, issue := range issues {
		issueMap[issue.ID] = issue
	}

	// If we have dependency records, use them to find parent-child relationships.
	// Nesting is driven strictly by the parent-child edge type. Earlier versions
	// also nested any dependency whose target was an epic, but that conflated
	// workflow edges (a task that merely blocks an epic) with membership, so a
	// genuinely 2-layer parent tree could render as a 6+ level tangle and trigger
	// false "the hierarchy is broken" conclusions. This now matches the storage
	// layer, which scopes an epic's children to parent-child edges only
	// (see epic_closure.go); non-hierarchical edges stay off the tree.
	if allDeps != nil {
		addedChild := make(map[string]bool) // tracks "parentID:childID" to prevent duplicates
		for issueID, deps := range allDeps {
			for _, dep := range deps {
				if dep.Type != types.DepParentChild {
					continue
				}
				parentID := dep.DependsOnID
				// Only include if both parent and child are in the issue set
				child, childOk := issueMap[issueID]
				_, parentOk := issueMap[parentID]
				if !childOk || !parentOk {
					continue
				}

				key := parentID + ":" + issueID
				if !addedChild[key] {
					childrenMap[parentID] = append(childrenMap[parentID], child)
					addedChild[key] = true
				}
				isChild[issueID] = true
			}
		}
	}

	// Fallback: check for hierarchical subtask IDs (e.g., "parent.1")
	for _, issue := range issues {
		if isChild[issue.ID] {
			continue // Already a child via dependency
		}
		if strings.Contains(issue.ID, ".") {
			parts := strings.Split(issue.ID, ".")
			parentID := strings.Join(parts[:len(parts)-1], ".")
			if _, exists := issueMap[parentID]; exists {
				childrenMap[parentID] = append(childrenMap[parentID], issue)
				isChild[issue.ID] = true
				continue
			}
		}
	}

	// Roots are issues that aren't children of any other issue
	for _, issue := range issues {
		if !isChild[issue.ID] {
			roots = append(roots, issue)
		}
	}

	// Sort roots for stable tree ordering (fixes unstable --tree output)
	// Use same sorting logic as children for consistency
	slices.SortFunc(roots, compareIssuesByPriority)

	// Sort children within each parent for stable ordering in data structure
	for parentID := range childrenMap {
		slices.SortFunc(childrenMap[parentID], compareIssuesByPriority)
	}

	return roots, childrenMap
}

// compareIssuesByPriority provides stable sorting for tree display
// Primary sort: priority (P0 before P1 before P2...)
// Secondary sort: ID for deterministic ordering when priorities match
func compareIssuesByPriority(a, b *types.Issue) int {
	// Primary: priority (ascending: P0 before P1 before P2...)
	if result := cmp.Compare(a.Priority, b.Priority); result != 0 {
		return result
	}
	// Secondary: ID for deterministic order when priorities match
	return utils.NaturalCompareIDs(a.ID, b.ID)
}

// printPrettyTree recursively prints the issue tree
// Children are sorted by priority (P0 first) for intuitive reading
func printPrettyTree(childrenMap map[string][]*types.Issue, parentID string, prefix string) {
	children := childrenMap[parentID]

	// Sort children by priority using same comparison as roots for consistency
	slices.SortFunc(children, compareIssuesByPriority)

	for i, child := range children {
		isLast := i == len(children)-1
		connector := "├── "
		if isLast {
			connector = "└── "
		}
		fmt.Printf("%s%s%s\n", prefix, connector, formatPrettyIssue(child))

		extension := "│   "
		if isLast {
			extension = "    "
		}
		printPrettyTree(childrenMap, child.ID, prefix+extension)
	}
}

// displayPrettyList displays issues in pretty tree format (GH#654)
// Uses buildIssueTree which only supports dotted ID hierarchy
func displayPrettyList(issues []*types.Issue, showHeader bool) {
	displayPrettyListWithDeps(issues, showHeader, nil)
}

// displayPrettyListWithDeps displays issues in tree format using dependency data
func displayPrettyListWithDeps(issues []*types.Issue, showHeader bool, allDeps map[string][]*types.Dependency) {
	if showHeader {
		// Clear screen and show header
		fmt.Print("\033[2J\033[H")
		fmt.Println(strings.Repeat("=", 80))
		fmt.Printf("Beads - Open & In Progress (%s)\n", time.Now().Format("15:04:05"))
		fmt.Println(strings.Repeat("=", 80))
		fmt.Println()
	}

	if len(issues) == 0 {
		fmt.Println("No issues found.")
		return
	}

	roots, childrenMap := buildIssueTreeWithDeps(issues, allDeps)

	for _, issue := range roots {
		fmt.Println(formatPrettyIssue(issue))
		printPrettyTree(childrenMap, issue.ID, "")
	}

	// Summary
	fmt.Println()
	fmt.Println(strings.Repeat("-", 80))
	openCount := 0
	inProgressCount := 0
	for _, issue := range issues {
		switch issue.Status {
		case "open":
			openCount++
		case "in_progress":
			inProgressCount++
		}
	}
	fmt.Printf("Total: %d issues (%d open, %d in progress)\n", len(issues), openCount, inProgressCount)
	fmt.Println()
	fmt.Println("Status: ○ open  ◐ in_progress  ● blocked  ✓ closed  ❄ deferred")
}
