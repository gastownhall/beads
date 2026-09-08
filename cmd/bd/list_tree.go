package main

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

// buildIssueTree builds parent-child tree structure from issues
// Uses actual parent-child dependencies from the database when store is provided
func buildIssueTree(issues []*types.Issue) (roots []*types.Issue, childrenMap map[string][]*types.Issue) {
	return buildIssueTreeWithDeps(issues, nil)
}

// buildIssueTreeWithDeps builds parent-child tree using dependency records
// If allDeps is nil, falls back to dotted ID hierarchy (e.g., "parent.1")
// Only explicit parent-child dependencies create hierarchy; other edge types
// (supersedes, tracks, relates-to, ...) express relationships that are not
// containment and must not nest one issue under another.
func buildIssueTreeWithDeps(issues []*types.Issue, allDeps map[string][]*types.Dependency) (roots []*types.Issue, childrenMap map[string][]*types.Issue) {
	issueMap := make(map[string]*types.Issue)
	childrenMap = make(map[string][]*types.Issue)
	isChild := make(map[string]bool)

	// Build issue map
	for _, issue := range issues {
		issueMap[issue.ID] = issue
	}

	// If we have dependency records, use them to find parent-child relationships
	if allDeps != nil {
		addedChild := make(map[string]bool) // tracks "parentID:childID" to prevent duplicates
		for issueID, deps := range allDeps {
			for _, dep := range deps {
				parentID := dep.DependsOnID
				// Only include if both parent and child are in the issue set
				child, childOk := issueMap[issueID]
				_, parentOk := issueMap[parentID]
				if !childOk || !parentOk {
					continue
				}

				// relates-to is a loose graph link, not a hierarchical edge:
				// treating it as parent-child causes incorrect nesting and, when
				// bidirectional, marks both endpoints as children of each other
				// — collapsing them out of the root set and silently dropping
				// whole subtrees from `bd list`. See gastownhall/beads#3936.
				if dep.Type == types.DepRelatesTo {
					continue
				}

				// Only an explicit parent-child edge builds hierarchy.
				//
				// Previously any dependency whose target was an epic was
				// promoted to parent-child. That swept in edges that express
				// no containment at all — most damagingly `supersedes`, a
				// version-chain link that is routinely mutual between two
				// epics (A supersedes B while B supersedes A). Promoting it
				// created a hierarchy cycle, and printPrettyTree walked it
				// until the disk filled: on a 1853-issue graph `bd list --all`
				// emitted 17.7 GB before the OOM killer stopped it.
				//
				// Note `bd dep cycles` stays silent on this shape because it
				// validates the blocking graph, not the rendered hierarchy.
				// The dotted-id fallback below still nests real subtasks.
				if dep.Type == types.DepParentChild {
					key := parentID + ":" + issueID
					if !addedChild[key] {
						childrenMap[parentID] = append(childrenMap[parentID], child)
						addedChild[key] = true
					}
					isChild[issueID] = true
				}
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

// maxTreeDepth caps how deep printPrettyTree will recurse. It mirrors the
// depth ceiling renderTree already enforces in dep.go, so a malformed or
// future edge type can never turn `bd list` into an unbounded writer again.
// Real hierarchies are orders of magnitude shallower than this.
const maxTreeDepth = 64

// printPrettyTree recursively prints the issue tree
// Children are sorted by priority (P0 first) for intuitive reading
func printPrettyTree(childrenMap map[string][]*types.Issue, parentID string, prefix string) {
	printPrettyTreeGuarded(childrenMap, parentID, prefix, map[string]bool{parentID: true}, 0)
}

// printPrettyTreeGuarded carries the two defenses renderTree (dep.go) already
// has and this renderer lacked: a visited set covering the current ancestor
// path, and a depth ceiling.
//
// visited is scoped to the path, not to the whole walk: a node legitimately
// reachable through two different parents must still render under each, while
// a node that reappears inside its own ancestry is a cycle and is cut.
func printPrettyTreeGuarded(
	childrenMap map[string][]*types.Issue,
	parentID string,
	prefix string,
	visited map[string]bool,
	depth int,
) {
	if depth >= maxTreeDepth {
		return
	}

	children := childrenMap[parentID]

	// Sort children by priority using same comparison as roots for consistency
	slices.SortFunc(children, compareIssuesByPriority)

	for i, child := range children {
		isLast := i == len(children)-1
		connector := "├── "
		if isLast {
			connector = "└── "
		}

		// A child already on this path closes a cycle: render it once, marked,
		// and stop. Without this the walk never terminates.
		if visited[child.ID] {
			fmt.Printf("%s%s%s %s\n", prefix, connector, formatPrettyIssue(child),
				ui.RenderMuted("(cycle)"))
			continue
		}

		fmt.Printf("%s%s%s\n", prefix, connector, formatPrettyIssue(child))

		extension := "│   "
		if isLast {
			extension = "    "
		}

		visited[child.ID] = true
		printPrettyTreeGuarded(childrenMap, child.ID, prefix+extension, visited, depth+1)
		delete(visited, child.ID)
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
