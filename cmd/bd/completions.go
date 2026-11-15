package main

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/types"
)

const issueIDCompletionDirective = cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveKeepOrder

// issueIDCompletion provides shell completion for issue IDs by querying the storage
// and returning a list of IDs with their titles as descriptions
func issueIDCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	// Initialize storage if not already initialized
	ctx := context.Background()
	if rootCtx != nil {
		ctx = rootCtx
	}

	// Get database path - use same logic as in PersistentPreRun
	currentDBPath := dbPath
	if currentDBPath == "" {
		currentDBPath = beads.FindDatabasePath()
		if currentDBPath == "" {
			return nil, issueIDCompletionDirective
		}
	}

	// Open database if store is not initialized
	currentStore := store
	if currentStore == nil {
		var err error
		currentStore, err = openReadOnlyStoreForDBPath(ctx, currentDBPath)
		if err != nil {
			// If we can't open database, return empty completion
			return nil, issueIDCompletionDirective
		}
		defer func() { _ = currentStore.Close() }()
	}

	// Use SearchIssues with IDPrefix filter to efficiently query matching issues
	filter := types.IssueFilter{
		IDPrefix: toComplete, // Filter at database level for better performance
	}
	issues, err := currentStore.SearchIssues(ctx, "", filter)
	if err != nil {
		// If we can't list issues, return empty completion
		return nil, issueIDCompletionDirective
	}

	slices.SortFunc(issues, func(a, b *types.Issue) int { return compareIssueIDs(a.ID, b.ID) })

	// Build completion list
	completions := make([]string, 0, len(issues))
	for _, issue := range issues {
		// Format: ID\tTitle (shown during completion)
		completions = append(completions, fmt.Sprintf("%s\t%s", issue.ID, issue.Title))
	}

	return completions, issueIDCompletionDirective
}

func compareIssueIDs(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		if c := compareIDSegments(as[i], bs[i]); c != 0 {
			return c
		}
	}
	return len(as) - len(bs)
}

func compareIDSegments(a, b string) int {
	an, aErr := strconv.Atoi(a)
	bn, bErr := strconv.Atoi(b)
	switch {
	case aErr == nil && bErr == nil:
		return an - bn
	case aErr == nil:
		return -1
	case bErr == nil:
		return 1
	}
	return strings.Compare(a, b)
}
