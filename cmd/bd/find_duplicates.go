package main

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/steveyegge/beads/internal/cobra"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var findDuplicatesCmd = &cobra.Command{
	Use:     "find-duplicates",
	Aliases: []string{"find-dups"},
	GroupID: "views",
	Short:   "Find semantically similar issues using text analysis",
	Long: `Find issues that are semantically similar but not exact duplicates.

Unlike 'bd duplicates' which finds exact content matches, find-duplicates
uses token-based text similarity to find issues that discuss the same topic
with different wording.

The detector tokenizes titles and descriptions, then computes the average
of Jaccard and cosine similarity between all issue pairs. It's fast and
free, but may miss semantically similar issues with very different wording.

Examples:
  bd find-duplicates                       # Token similarity (default)
  bd find-duplicates --threshold 0.4       # Lower threshold = more results
  bd find-duplicates --status open         # Only check open issues
  bd find-duplicates --limit 20            # Show top 20 pairs
  bd find-duplicates --json                # JSON output`,
	Run: runFindDuplicates,
}

func init() {
	findDuplicatesCmd.Flags().Float64("threshold", 0.5, "Similarity threshold (0.0-1.0, lower = more results)")
	findDuplicatesCmd.Flags().StringP("status", "s", "", "Filter by status (default: non-closed)")
	findDuplicatesCmd.Flags().IntP("limit", "n", 50, "Maximum number of pairs to show")
	rootCmd.AddCommand(findDuplicatesCmd)
}

// duplicatePair represents a pair of potentially duplicate issues.
type duplicatePair struct {
	IssueA     *types.Issue `json:"issue_a"`
	IssueB     *types.Issue `json:"issue_b"`
	Similarity float64      `json:"similarity"`
	Method     string       `json:"method"`
}

func runFindDuplicates(cmd *cobra.Command, _ []string) {
	threshold, _ := cmd.Flags().GetFloat64("threshold")
	status, _ := cmd.Flags().GetString("status")
	limit, _ := cmd.Flags().GetInt("limit")

	ctx := rootCtx

	// Fetch issues
	filter := types.IssueFilter{}
	if status != "" && status != "all" {
		s := types.Status(status)
		filter.Status = &s
	}

	var issues []*types.Issue
	var err error

	issues, err = store.SearchIssues(ctx, "", filter)
	if err != nil {
		FatalError("fetching issues: %v", err)
	}

	// Default: filter out closed issues unless status flag is set
	if status == "" {
		var filtered []*types.Issue
		for _, issue := range issues {
			if issue.Status != types.StatusClosed {
				filtered = append(filtered, issue)
			}
		}
		issues = filtered
	}

	if len(issues) < 2 {
		if jsonOutput {
			outputJSON(map[string]interface{}{
				"pairs": []interface{}{},
				"count": 0,
			})
		} else {
			fmt.Println("Not enough issues to compare (need at least 2)")
		}
		return
	}

	// Find duplicate pairs
	pairs := findMechanicalDuplicates(issues, threshold)

	// Sort by similarity (highest first)
	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].Similarity > pairs[j].Similarity
	})

	// Apply limit
	if limit > 0 && len(pairs) > limit {
		pairs = pairs[:limit]
	}

	// Output
	if jsonOutput {
		type pairJSON struct {
			IssueAID    string  `json:"issue_a_id"`
			IssueBID    string  `json:"issue_b_id"`
			IssueATitle string  `json:"issue_a_title"`
			IssueBTitle string  `json:"issue_b_title"`
			Similarity  float64 `json:"similarity"`
			Method      string  `json:"method"`
		}
		jsonPairs := make([]pairJSON, len(pairs))
		for i, p := range pairs {
			jsonPairs[i] = pairJSON{
				IssueAID:    p.IssueA.ID,
				IssueBID:    p.IssueB.ID,
				IssueATitle: p.IssueA.Title,
				IssueBTitle: p.IssueB.Title,
				Similarity:  p.Similarity,
				Method:      p.Method,
			}
		}
		outputJSON(map[string]interface{}{
			"pairs":     jsonPairs,
			"count":     len(jsonPairs),
			"method":    "mechanical",
			"threshold": threshold,
		})
		return
	}

	if len(pairs) == 0 {
		fmt.Printf("No similar issues found (threshold: %.0f%%)\n", threshold*100)
		return
	}

	fmt.Printf("%s Found %d potential duplicate pair(s) (threshold: %.0f%%):\n\n",
		ui.RenderWarn("🔍"), len(pairs), threshold*100)

	for i, p := range pairs {
		pct := p.Similarity * 100
		fmt.Printf("%s Pair %d (%.0f%% similar):\n", ui.RenderAccent("━━"), i+1, pct)
		fmt.Printf("  %s %s\n", ui.RenderPass(p.IssueA.ID), p.IssueA.Title)
		fmt.Printf("  %s %s\n", ui.RenderPass(p.IssueB.ID), p.IssueB.Title)
		fmt.Printf("  %s bd show %s %s\n\n", ui.RenderAccent("Compare:"), p.IssueA.ID, p.IssueB.ID)
	}
}

// tokenize splits text into lowercase word tokens, removing punctuation.
func tokenize(text string) map[string]int {
	tokens := make(map[string]int)
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-'
	})
	for _, w := range words {
		if len(w) > 1 { // Skip single chars
			tokens[w]++
		}
	}
	return tokens
}

// issueText returns the combined text content of an issue for comparison.
func issueText(issue *types.Issue) string {
	parts := []string{issue.Title}
	if issue.Description != "" {
		parts = append(parts, issue.Description)
	}
	return strings.Join(parts, " ")
}

// jaccardSimilarity computes the Jaccard similarity between two token sets.
func jaccardSimilarity(a, b map[string]int) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}

	intersection := 0
	union := 0

	// Count union from a
	for token, countA := range a {
		if countB, ok := b[token]; ok {
			if countA < countB {
				intersection += countA
			} else {
				intersection += countB
			}
			if countA > countB {
				union += countA
			} else {
				union += countB
			}
		} else {
			union += countA
		}
	}
	// Count tokens only in b
	for token, countB := range b {
		if _, ok := a[token]; !ok {
			union += countB
		}
	}

	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// cosineSimilarity computes the cosine similarity between two token vectors.
func cosineSimilarity(a, b map[string]int) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}

	dotProduct := 0.0
	magA := 0.0
	magB := 0.0

	for token, countA := range a {
		fa := float64(countA)
		magA += fa * fa
		if countB, ok := b[token]; ok {
			dotProduct += fa * float64(countB)
		}
	}
	for _, countB := range b {
		fb := float64(countB)
		magB += fb * fb
	}

	if magA == 0 || magB == 0 {
		return 0
	}
	return dotProduct / (math.Sqrt(magA) * math.Sqrt(magB))
}

// findMechanicalDuplicates finds similar issues using token-based text similarity.
func findMechanicalDuplicates(issues []*types.Issue, threshold float64) []duplicatePair {
	// Pre-tokenize all issues
	type tokenized struct {
		issue  *types.Issue
		tokens map[string]int
	}
	items := make([]tokenized, len(issues))
	for i, issue := range issues {
		items[i] = tokenized{
			issue:  issue,
			tokens: tokenize(issueText(issue)),
		}
	}

	var pairs []duplicatePair

	// Compare all pairs
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			// Use average of Jaccard and cosine for better accuracy
			jaccard := jaccardSimilarity(items[i].tokens, items[j].tokens)
			cosine := cosineSimilarity(items[i].tokens, items[j].tokens)
			similarity := (jaccard + cosine) / 2

			if similarity >= threshold {
				pairs = append(pairs, duplicatePair{
					IssueA:     items[i].issue,
					IssueB:     items[j].issue,
					Similarity: similarity,
					Method:     "mechanical",
				})
			}
		}
	}

	return pairs
}

