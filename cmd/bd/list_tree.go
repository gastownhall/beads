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

// printPrettyTree recursively prints the issue tree.
// Children are ordered by dependency then priority when dr != nil (--deps), else
// by priority (P0 first) for intuitive reading. When dr is set, each node's
// dependency edges are annotated just beneath it.
func printPrettyTree(childrenMap map[string][]*types.Issue, parentID string, prefix string, dr *depRender) {
	children := childrenMap[parentID]

	if dr != nil {
		children = orderSiblingsByDeps(children, dr.allDeps)
	} else {
		// Sort children by priority using same comparison as roots for consistency
		slices.SortFunc(children, compareIssuesByPriority)
	}

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
		dr.annotationsFor(child.ID, prefix+extension)
		printPrettyTree(childrenMap, child.ID, prefix+extension, dr)
	}
}

// displayPrettyList displays issues in pretty tree format (GH#654)
// Uses buildIssueTree which only supports dotted ID hierarchy
// There is no --ready arm behind this one: it is the plain tree, so the
// summary keeps its status breakdown.
func displayPrettyList(issues []*types.Issue, showHeader bool) {
	displayPrettyListWithDeps(issues, showHeader, nil, false, false, "")
}

// displayPrettyListWithDeps displays issues in tree format using dependency data.
// readyFiltered and statusSelector must be threaded from the caller's --ready
// / --status state rather than defaulted here: the watch paths reach the
// summary through this wrapper, and a hardcoded false silently restores the
// vacuous "(N open, 0 in progress)" that listFooterLine exists to suppress.
func displayPrettyListWithDeps(issues []*types.Issue, showHeader bool, allDeps map[string][]*types.Dependency, truncated, readyFiltered bool, statusSelector string) {
	displayPrettyListWithDepsMode(issues, showHeader, allDeps, "", truncated, readyFiltered, statusSelector)
}

// listFooterLine renders the one-line summary under a text listing.
//
// The status breakdown is only meaningful when the query could have returned
// more than one status. Under --ready the query is status-pinned: the default
// (no --status, or --status all) is still open, so "(N open, 0 in progress)"
// is a tautology for ANY database, including one with a thousand in-progress
// issues matching the same label. An explicit --status is the intersection
// (GH#5832), and the same tautology applies to whatever selector was asked
// for — the footer must name that selector rather than reuse the default-open
// sentence.
//
// Printed next to a real count that number reads as a finding rather than an
// artifact of the flag: "0 in progress" answers the question "is anything in
// progress here?" with a confident no, while the rows that would have said
// otherwise were removed before counting. So when a status filter is in force by
// construction, say what was excluded instead of asserting a count for it. This
// is the same principle as the truncation arm below, which refuses to label a
// cut-off page "Total" (GH#5362): a count is only honest alongside its scope.
func listFooterLine(total, open, inProgress int, truncated, readyFiltered bool, statusSelector string) string {
	if readyFiltered {
		// No status breakdown: --ready makes it vacuous. Name the scope instead.
		scope := readyFooterScope(statusSelector)
		if truncated {
			return fmt.Sprintf("Showing %d ready issues (%s); more match (truncated by --limit). Use --limit 0 for all.", total, scope)
		}
		return fmt.Sprintf("Ready: %d issues with no active blockers (%s)", total, scope)
	}
	if truncated {
		return fmt.Sprintf("Showing %d issues (%d open, %d in progress); more match (truncated by --limit). Use --limit 0 for all.",
			total, open, inProgress)
	}
	return fmt.Sprintf("Total: %d issues (%d open, %d in progress)", total, open, inProgress)
}

// readyFooterScope names the status pin a --ready listing actually used.
// Empty / "all" still take the open default; an explicit selector is the
// intersection and must not reuse "excludes in_progress" (GH#5832).
func readyFooterScope(statusSelector string) string {
	var parts []string
	for _, part := range strings.Split(statusSelector, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 || (len(parts) == 1 && parts[0] == "all") {
		return "open only — --ready excludes in_progress"
	}
	return strings.Join(parts, ",") + " only"
}

// displayPrettyListWithDepsMode displays issues in tree format. When depsMode is
// "scheduling" or "all", the tree also annotates each node's dependency edges and
// orders siblings by their scheduling dependencies (see orderSiblingsByDeps). An
// empty depsMode is the plain parent-child tree. truncated means the page was cut
// by --limit; the summary then says "Showing N" instead of "Total: N" (GH#5362).
// readyFiltered means --ready was in force; statusSelector is the --status value
// so the summary names the pin that actually applied — see listFooterLine.
func displayPrettyListWithDepsMode(issues []*types.Issue, showHeader bool, allDeps map[string][]*types.Dependency, depsMode string, truncated, readyFiltered bool, statusSelector string) {
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

	var dr *depRender
	if depsMode != "" {
		inView := make(map[string]*types.Issue, len(issues))
		for _, issue := range issues {
			inView[issue.ID] = issue
		}
		dr = &depRender{mode: depsMode, allDeps: allDeps, inView: inView}
		roots = orderSiblingsByDeps(roots, allDeps)
	}

	for _, issue := range roots {
		fmt.Println(formatPrettyIssue(issue))
		dr.annotationsFor(issue.ID, "")
		printPrettyTree(childrenMap, issue.ID, "", dr)
	}

	// Summary — counts describe the shown page; never label a truncated page "Total".
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
	fmt.Println(listFooterLine(len(issues), openCount, inProgressCount, truncated, readyFiltered, statusSelector))
	fmt.Println()
	fmt.Println("Status: ○ open  ◐ in_progress  ● blocked  ✓ closed  ❄ deferred")
	fmt.Println("Priority: P0–P4 (label only; not a status icon)")
	if dr != nil {
		fmt.Printf("Deps:   %s = depends-on / relationship (points to target); siblings ordered so dependencies come first; ↗ = target outside current view\n", depGlyph)
	}
}
