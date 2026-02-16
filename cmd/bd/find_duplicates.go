package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"unicode"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var findDuplicatesCmd = &cobra.Command{
	Use:     "find-duplicates",
	Aliases: []string{"find-dups"},
	GroupID: "views",
	Short:   "Find semantically similar issues using text analysis or AI",
	Long: `Find issues that are semantically similar but not exact duplicates.

Unlike 'bd duplicates' which finds exact content matches, find-duplicates
uses text similarity or AI to find issues that discuss the same topic
with different wording.

Approaches:
  mechanical  Token-based text similarity (default, no API key needed)
  ai          LLM-based semantic comparison (requires ANTHROPIC_API_KEY)

The mechanical approach tokenizes titles and descriptions, then computes
Jaccard similarity between all issue pairs. It's fast and free but may
miss semantically similar issues with very different wording.

The AI approach sends candidate pairs to Claude for semantic comparison.
It first uses mechanical pre-filtering to reduce the number of API calls,
then asks the LLM to judge whether the remaining pairs are true duplicates.

Examples:
  bd find-duplicates                       # Mechanical similarity (default)
  bd find-duplicates --threshold 0.4       # Lower threshold = more results
  bd find-duplicates --method ai           # Use AI for semantic comparison
  bd find-duplicates --status open         # Only check open issues
  bd find-duplicates --limit 20            # Show top 20 pairs
  bd find-duplicates --json                # JSON output`,
	Run: runFindDuplicates,
}

func init() {
	findDuplicatesCmd.Flags().String("method", "mechanical", "Detection method: mechanical, ai")
	findDuplicatesCmd.Flags().Float64("threshold", 0.5, "Similarity threshold (0.0-1.0, lower = more results)")
	findDuplicatesCmd.Flags().StringP("status", "s", "", "Filter by status (default: non-closed)")
	findDuplicatesCmd.Flags().IntP("limit", "n", 50, "Maximum number of pairs to show")
	findDuplicatesCmd.Flags().String("model", "", "AI model to use (only with --method ai; default from config ai.model)")
	rootCmd.AddCommand(findDuplicatesCmd)
}

// duplicatePair represents a pair of potentially duplicate issues.
type duplicatePair struct {
	IssueA     *types.Issue `json:"issue_a"`
	IssueB     *types.Issue `json:"issue_b"`
	Similarity float64      `json:"similarity"`
	Method     string       `json:"method"`
	Reason     string       `json:"reason,omitempty"`
}

func runFindDuplicates(cmd *cobra.Command, _ []string) {
	method, _ := cmd.Flags().GetString("method")
	threshold, _ := cmd.Flags().GetFloat64("threshold")
	status, _ := cmd.Flags().GetString("status")
	limit, _ := cmd.Flags().GetInt("limit")
	model, _ := cmd.Flags().GetString("model")
	if model == "" {
		model = config.DefaultAIModel()
	}

	ctx := rootCtx

	// Validate method
	if method != "mechanical" && method != "ai" {
		fmt.Fprintf(os.Stderr, "Error: invalid method %q (use: mechanical, ai)\n", method)
		os.Exit(1)
	}

	// AI method requires API key
	if method == "ai" {
		if os.Getenv("ANTHROPIC_API_KEY") == "" {
			fmt.Fprintf(os.Stderr, "Error: --method ai requires ANTHROPIC_API_KEY environment variable\n")
			os.Exit(1)
		}
	}


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
		fmt.Fprintf(os.Stderr, "Error fetching issues: %v\n", err)
		os.Exit(1)
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
	var pairs []duplicatePair
	switch method {
	case "mechanical":
		pairs = findMechanicalDuplicates(issues, threshold)
	case "ai":
		pairs = findAIDuplicates(ctx, issues, threshold, model)
	}

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
			Reason      string  `json:"reason,omitempty"`
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
				Reason:      p.Reason,
			}
		}
		outputJSON(map[string]interface{}{
			"pairs":     jsonPairs,
			"count":     len(jsonPairs),
			"method":    method,
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
		if p.Reason != "" {
			fmt.Printf("  %s %s\n", ui.RenderAccent("Reason:"), p.Reason)
		}
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

// findAIDuplicates uses LLM-based semantic comparison to find duplicates.
// It first pre-filters with mechanical similarity to reduce API calls.
func findAIDuplicates(ctx context.Context, issues []*types.Issue, threshold float64, model string) []duplicatePair {
	// Pre-filter: use mechanical similarity with a lower threshold to find candidates
	preFilterThreshold := threshold * 0.5 // Cast a wider net for pre-filtering
	if preFilterThreshold < 0.15 {
		preFilterThreshold = 0.15
	}
	candidates := findMechanicalDuplicates(issues, preFilterThreshold)

	if len(candidates) == 0 {
		return nil
	}

	// Limit candidates to avoid excessive API calls
	maxCandidates := 100
	if len(candidates) > maxCandidates {
		// Sort by mechanical similarity and take the top candidates
		sort.Slice(candidates, func(i, j int) bool {
			return candidates[i].Similarity > candidates[j].Similarity
		})
		candidates = candidates[:maxCandidates]
	}

	fmt.Fprintf(os.Stderr, "Analyzing %d candidate pairs with AI...\n", len(candidates))

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	client := anthropic.NewClient(option.WithAPIKey(apiKey))

	var pairs []duplicatePair

	// Batch candidates into groups for efficient API usage
	batchSize := 10
	for i := 0; i < len(candidates); i += batchSize {
		end := i + batchSize
		if end > len(candidates) {
			end = len(candidates)
		}
		batch := candidates[i:end]

		results := analyzeWithAI(ctx, client, anthropic.Model(model), batch)
		for _, r := range results {
			if r.Similarity >= threshold {
				pairs = append(pairs, r)
			}
		}
	}

	return pairs
}

// analyzeWithAI sends a batch of candidate pairs to the LLM for semantic comparison.
func analyzeWithAI(ctx context.Context, client anthropic.Client, model anthropic.Model, candidates []duplicatePair) []duplicatePair {
	if len(candidates) == 0 {
		return nil
	}

	// Build the prompt
	var sb strings.Builder
	sb.WriteString("You are analyzing issue pairs to determine if they are semantic duplicates.\n")
	sb.WriteString("For each pair, determine if they describe the same problem/task/feature.\n")
	sb.WriteString("Respond with a JSON array of objects, one per pair, with fields:\n")
	sb.WriteString("  - pair_index (int): 0-based index of the pair\n")
	sb.WriteString("  - is_duplicate (bool): true if semantically the same issue\n")
	sb.WriteString("  - confidence (float): 0.0-1.0 how confident you are\n")
	sb.WriteString("  - reason (string): brief explanation\n\n")
	sb.WriteString("Respond ONLY with the JSON array, no other text.\n\n")

	for i, c := range candidates {
		fmt.Fprintf(&sb, "--- Pair %d ---\n", i)
		fmt.Fprintf(&sb, "Issue A [%s]: %s\n", c.IssueA.ID, c.IssueA.Title)
		if c.IssueA.Description != "" {
			descA := c.IssueA.Description
			if len(descA) > 500 {
				descA = descA[:500] + "..."
			}
			fmt.Fprintf(&sb, "  Description: %s\n", descA)
		}
		fmt.Fprintf(&sb, "Issue B [%s]: %s\n", c.IssueB.ID, c.IssueB.Title)
		if c.IssueB.Description != "" {
			descB := c.IssueB.Description
			if len(descB) > 500 {
				descB = descB[:500] + "..."
			}
			fmt.Fprintf(&sb, "  Description: %s\n", descB)
		}
		sb.WriteString("\n")
	}

	message, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     model,
		MaxTokens: 2048,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(sb.String())),
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: AI analysis failed: %v\n", err)
		// Fall back to mechanical scores
		return candidates
	}

	if len(message.Content) == 0 || message.Content[0].Type != "text" {
		fmt.Fprintf(os.Stderr, "Warning: unexpected AI response format\n")
		return candidates
	}

	// Parse the JSON response
	responseText := message.Content[0].Text

	// Try to extract JSON from the response (handle markdown code blocks)
	jsonText := responseText
	if idx := strings.Index(jsonText, "["); idx >= 0 {
		jsonText = jsonText[idx:]
	}
	if idx := strings.LastIndex(jsonText, "]"); idx >= 0 {
		jsonText = jsonText[:idx+1]
	}

	var results []struct {
		PairIndex   int     `json:"pair_index"`
		IsDuplicate bool    `json:"is_duplicate"`
		Confidence  float64 `json:"confidence"`
		Reason      string  `json:"reason"`
	}

	if err := json.Unmarshal([]byte(jsonText), &results); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to parse AI response: %v\n", err)
		return candidates
	}

	// Map results back to candidate pairs
	var pairs []duplicatePair
	for _, r := range results {
		if r.PairIndex < 0 || r.PairIndex >= len(candidates) {
			continue
		}
		if r.IsDuplicate {
			c := candidates[r.PairIndex]
			pairs = append(pairs, duplicatePair{
				IssueA:     c.IssueA,
				IssueB:     c.IssueB,
				Similarity: r.Confidence,
				Method:     "ai",
				Reason:     r.Reason,
			})
		}
	}

	return pairs
}
