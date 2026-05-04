package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

var proveCmd = &cobra.Command{
	Use:     "prove <id>",
	GroupID: "issues",
	Short:   "Prove an issue against tracker and repo evidence",
	Long: `Prove treats an issue as a claim about reality and gathers deterministic
evidence before making a recommendation.

Current evidence sources:
  - issue text and comments
  - title similarity against nearby issues
  - repo-local path extraction and existence checks

It does not auto-close or auto-merge anything. It emits a proof packet and a
recommendation such as keep_open, review_for_close, merge_or_close_duplicate,
keep_as_parent, or keep_closed.`,
	Args: cobra.ExactArgs(1),
	Run:  runProve,
}

func init() {
	proveCmd.Flags().Float64("duplicate-threshold", 0.92, "Similarity threshold for duplicate candidate detection")
	proveCmd.Flags().StringArray("path-hint", nil, "Optional repo path hints used to trim extracted file references")
	proveCmd.Flags().String("title-prefix-regex", "", "Optional regex removed from titles before duplicate comparison")
	rootCmd.AddCommand(proveCmd)
}

type provePathEvidence struct {
	PathText     string `json:"path_text"`
	Exists       bool   `json:"exists"`
	ResolvedPath string `json:"resolved_path"`
}

type proveRecommendation struct {
	Action     string   `json:"action"`
	Confidence float64  `json:"confidence"`
	Why        []string `json:"why"`
	WhyNot     []string `json:"why_not"`
}

type provePacket struct {
	IssueID                string              `json:"issue_id"`
	Title                  string              `json:"title"`
	Status                 string              `json:"status"`
	Classification         string              `json:"classification"`
	Confidence             float64             `json:"confidence"`
	Rationale              []string            `json:"rationale"`
	CompletionSignalCounts map[string]int      `json:"completion_signal_counts"`
	PathEvidence           []provePathEvidence `json:"path_evidence"`
	DuplicateCandidates    []map[string]any    `json:"duplicate_candidates"`
	Recommendation         proveRecommendation `json:"recommendation"`
}

var (
	provePathPattern    = regexp.MustCompile(`(?i)(?:[A-Za-z]:[\\/])?[\w .\-\\/]+?\.(?:py|ps1|cs|md|jsonl|json|txt|bat|sh|yml|yaml|csproj|sln)`)
	proveStrongPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bclosing as completed\b`),
		regexp.MustCompile(`(?i)\bclosed as completed\b`),
		regexp.MustCompile(`(?i)\bis closed\b`),
		regexp.MustCompile(`(?i)\balready matches the (?:bead|issue|ticket)\b`),
		regexp.MustCompile(`(?i)\bcompletion evidence\b`),
		regexp.MustCompile(`(?i)\bacceptance bar is met\b`),
		regexp.MustCompile(`(?i)\bimplemented\b`),
		regexp.MustCompile(`(?i)\blanded\b`),
		regexp.MustCompile(`(?i)\bpasses\b`),
	}
	proveWeakPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bsmellcheck\b`),
		regexp.MustCompile(`(?i)\btruth pass\b`),
		regexp.MustCompile(`(?i)\bupdated\b`),
		regexp.MustCompile(`(?i)\badded test\b`),
		regexp.MustCompile(`(?i)\btest coverage\b`),
		regexp.MustCompile(`(?i)\bbuild passes\b`),
		regexp.MustCompile(`(?i)\b0 warnings\b`),
		regexp.MustCompile(`(?i)\b0 errors\b`),
	}
	proveTestPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\btest(s)?\b`),
		regexp.MustCompile(`(?i)\bpytest\b`),
		regexp.MustCompile(`(?i)\bdotnet test\b`),
		regexp.MustCompile(`(?i)\bbuild passes\b`),
		regexp.MustCompile(`(?i)\b0 warnings\b`),
		regexp.MustCompile(`(?i)\b0 errors\b`),
	}
	proveDefaultStopwords = map[string]struct{}{
		"a": {}, "an": {}, "and": {}, "architecture": {}, "bead": {}, "build": {}, "check": {},
		"closeout": {}, "code": {}, "doc": {}, "docs": {}, "fix": {}, "for": {}, "in": {},
		"is": {}, "issue": {}, "make": {}, "of": {}, "on": {}, "open": {}, "pass": {},
		"runtime": {}, "task": {}, "the": {}, "ticket": {}, "to": {}, "truth": {}, "update": {},
		"validation": {},
	}
)

func runProve(cmd *cobra.Command, args []string) {
	issueID := args[0]
	duplicateThreshold, _ := cmd.Flags().GetFloat64("duplicate-threshold")
	pathHints, _ := cmd.Flags().GetStringArray("path-hint")
	titlePrefixRegex, _ := cmd.Flags().GetString("title-prefix-regex")
	var titlePrefixRE *regexp.Regexp
	if titlePrefixRegex != "" {
		re, err := regexp.Compile(titlePrefixRegex)
		if err != nil {
			FatalError("invalid --title-prefix-regex: %v", err)
		}
		titlePrefixRE = re
	}
	ctx := rootCtx

	result, err := resolveAndGetIssueWithRouting(ctx, store, issueID)
	if err != nil {
		FatalErrorRespectJSON("fetching issue %s: %v", issueID, err)
	}
	if result == nil || result.Issue == nil {
		if result != nil {
			result.Close()
		}
		FatalErrorRespectJSON("issue %s not found", issueID)
	}
	defer result.Close()

	details := buildIssueDetailsForProve(ctx, result.Store, result.Issue)
	allIssues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		FatalErrorRespectJSON("fetching issues: %v", err)
	}
	repoRoot := git.GetRepoRoot()
	if repoRoot == "" {
		repoRoot, err = os.Getwd()
		if err != nil {
			FatalError("resolving workspace root: %v", err)
		}
	}
	packet := buildProofPacket(details, allIssues, duplicateThreshold, pathHints, titlePrefixRE, repoRoot)

	if jsonOutput {
		outputJSON(packet)
		return
	}
	printProvePacket(packet)
}

func buildIssueDetailsForProve(ctx context.Context, issueStore storage.Storage, issue *types.Issue) *types.IssueDetails {
	details := &types.IssueDetails{Issue: *issue}
	details.Labels, _ = issueStore.GetLabels(ctx, issue.ID)
	details.Dependencies, _ = issueStore.GetDependenciesWithMetadata(ctx, issue.ID)
	details.Dependents, _ = issueStore.GetDependentsWithMetadata(ctx, issue.ID)
	details.Comments, _ = issueStore.GetIssueComments(ctx, issue.ID)
	for _, dep := range details.Dependencies {
		if dep.DependencyType == types.DepParentChild {
			details.Parent = &dep.ID
			break
		}
	}
	return details
}

func buildProofPacket(details *types.IssueDetails, allIssues []*types.Issue, duplicateThreshold float64, pathHints []string, titlePrefixRE *regexp.Regexp, repoRoot string) provePacket {
	texts := []string{
		details.Description,
		details.AcceptanceCriteria,
		details.Notes,
	}
	for _, comment := range details.Comments {
		texts = append(texts, comment.Text)
	}
	strong, weak, testLike := scoreTextSignals(texts)
	pathEvidence := buildPathEvidence(texts, pathHints, repoRoot, 12)
	existingPaths := 0
	for _, item := range pathEvidence {
		if item.Exists {
			existingPaths++
		}
	}
	duplicates := findDuplicateCandidates(details.Issue, allIssues, duplicateThreshold, titlePrefixRE)

	classification := "active_candidate"
	confidence := 0.45
	rationale := []string{}

	if details.IssueType == types.TypeEpic || len(details.Dependents) > 0 {
		classification = "meta_or_parent"
		confidence = 0.78
		rationale = append(rationale, "Issue is an epic and/or has dependent children, so it likely tracks a parent lane rather than an immediately closable task.")
	}
	if len(duplicates) > 0 {
		top := duplicates[0]
		rationale = append(rationale, fmt.Sprintf("Closest duplicate candidate is %s with score %.2f.", top["id"], top["score"]))
		if score, ok := top["score"].(float64); ok && score >= 0.98 && classification != "likely_completed" {
			classification = "duplicate_candidate"
			if confidence < 0.94 {
				confidence = 0.94
			}
		}
	}
	if details.Status == types.StatusOpen && strong >= 1 {
		classification = "likely_completed"
		confidence = minFloat(0.98, 0.68+float64(strong)*0.12+float64(minInt(existingPaths, 3))*0.04+float64(minInt(testLike, 2))*0.05)
		rationale = append(rationale, "Open issue has strong completion language in notes/comments.")
	} else if details.Status == types.StatusOpen && strong == 0 && weak >= 3 && testLike >= 1 && existingPaths >= 1 {
		classification = "possibly_completed"
		confidence = 0.71
		rationale = append(rationale, "Open issue has multiple weak completion signals plus test/build evidence and referenced files that exist.")
	}
	if details.Status == types.StatusClosed {
		classification = "closed"
		confidence = 0.99
		rationale = append(rationale, "Issue is already closed in the tracker.")
	}
	if len(rationale) == 0 {
		rationale = append(rationale, "No decisive stale-open or duplicate evidence was found from deterministic signals.")
	}

	return provePacket{
		IssueID:        details.ID,
		Title:          details.Title,
		Status:         string(details.Status),
		Classification: classification,
		Confidence:     round2(confidence),
		Rationale:      rationale,
		CompletionSignalCounts: map[string]int{
			"strong":    strong,
			"weak":      weak,
			"test_like": testLike,
		},
		PathEvidence:        pathEvidence,
		DuplicateCandidates: duplicates,
		Recommendation:      buildRecommendation(classification, string(details.Status), confidence, duplicates, rationale),
	}
}

func buildRecommendation(classification, status string, confidence float64, duplicates []map[string]any, rationale []string) proveRecommendation {
	rec := proveRecommendation{
		Action:     "keep_open",
		Confidence: round2(confidence),
		Why:        rationale,
		WhyNot:     []string{},
	}
	switch {
	case status == "closed":
		rec.Action = "keep_closed"
	case classification == "duplicate_candidate":
		rec.Action = "merge_or_close_duplicate"
		if len(duplicates) > 0 {
			rec.WhyNot = append(rec.WhyNot, fmt.Sprintf("Closest duplicate candidate: %v.", duplicates[0]["id"]))
		}
	case classification == "likely_completed":
		rec.Action = "review_for_close"
	case classification == "possibly_completed":
		rec.Action = "review_for_close"
		rec.WhyNot = append(rec.WhyNot, "Evidence is suggestive, not decisive; verify acceptance criteria before closing.")
	case classification == "meta_or_parent":
		rec.Action = "keep_as_parent"
		rec.WhyNot = append(rec.WhyNot, "Parent/epic issues often stay open even when children close.")
	default:
		rec.Action = "keep_open"
		rec.WhyNot = append(rec.WhyNot, "No strong completion or duplicate signal was found.")
	}
	return rec
}

func scoreTextSignals(texts []string) (int, int, int) {
	strong, weak, testLike := 0, 0, 0
	for _, text := range texts {
		for _, pattern := range proveStrongPatterns {
			if pattern.MatchString(text) {
				strong++
			}
		}
		for _, pattern := range proveWeakPatterns {
			if pattern.MatchString(text) {
				weak++
			}
		}
		for _, pattern := range proveTestPatterns {
			if pattern.MatchString(text) {
				testLike++
			}
		}
	}
	return strong, weak, testLike
}

func buildPathEvidence(texts []string, pathHints []string, repoRoot string, limit int) []provePathEvidence {
	seen := map[string]struct{}{}
	evidence := []provePathEvidence{}
	absRepoRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return evidence
	}
	for _, text := range texts {
		matches := provePathPattern.FindAllString(text, -1)
		for _, raw := range matches {
			if len(evidence) >= limit {
				return evidence
			}
			pathText := strings.Trim(raw, ".,;:()[]{}<>\"'")
			if pathText == "" {
				continue
			}
			if !strings.Contains(pathText, "/") && !strings.Contains(pathText, "\\") && strings.Contains(pathText, " ") {
				continue
			}
			lowered := strings.ToLower(pathText)
			for _, hint := range pathHints {
				if idx := strings.Index(lowered, strings.ToLower(hint)); idx > 0 {
					pathText = pathText[idx:]
					lowered = strings.ToLower(pathText)
					break
				}
			}
			key := strings.ReplaceAll(strings.ToLower(pathText), "\\", "/")
			if _, ok := seen[key]; ok {
				continue
			}
			if filepath.IsAbs(pathText) {
				continue
			}
			resolved := filepath.Join(absRepoRoot, pathText)
			resolved = filepath.Clean(resolved)
			if !isWithinRoot(absRepoRoot, resolved) {
				continue
			}
			seen[key] = struct{}{}
			_, err := os.Stat(resolved)
			evidence = append(evidence, provePathEvidence{
				PathText:     pathText,
				Exists:       err == nil,
				ResolvedPath: resolved,
			})
		}
	}
	return evidence
}

func findDuplicateCandidates(issue types.Issue, allIssues []*types.Issue, threshold float64, titlePrefixRE *regexp.Regexp) []map[string]any {
	candidates := []map[string]any{}
	for _, other := range allIssues {
		if other.ID == issue.ID {
			continue
		}
		score := duplicateScore(issue.Title, other.Title, titlePrefixRE)
		if score >= threshold {
			candidates = append(candidates, map[string]any{
				"id":     other.ID,
				"title":  other.Title,
				"score":  score,
				"status": string(other.Status),
			})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		left := candidates[i]["score"].(float64)
		right := candidates[j]["score"].(float64)
		if left == right {
			return fmt.Sprintf("%v", candidates[i]["id"]) < fmt.Sprintf("%v", candidates[j]["id"])
		}
		return left > right
	})
	if len(candidates) > 5 {
		return candidates[:5]
	}
	return candidates
}

func duplicateScore(left, right string, titlePrefixRE *regexp.Regexp) float64 {
	leftNorm := normalizeProveTitle(left, titlePrefixRE)
	rightNorm := normalizeProveTitle(right, titlePrefixRE)
	if leftNorm == "" || rightNorm == "" {
		return 0
	}
	if leftNorm == rightNorm {
		return 1
	}
	leftTokens := tokenizeProve(leftNorm)
	rightTokens := tokenizeProve(rightNorm)
	intersection := 0
	unionKeys := map[string]struct{}{}
	for tok := range leftTokens {
		unionKeys[tok] = struct{}{}
		if _, ok := rightTokens[tok]; ok {
			intersection++
		}
	}
	for tok := range rightTokens {
		unionKeys[tok] = struct{}{}
	}
	jaccard := 0.0
	if len(unionKeys) > 0 {
		jaccard = float64(intersection) / float64(len(unionKeys))
	}
	seq := sequenceSimilarity(leftNorm, rightNorm)
	if seq > jaccard {
		return seq
	}
	return jaccard
}

func normalizeProveTitle(title string, prefixRE *regexp.Regexp) string {
	out := strings.ToLower(strings.TrimSpace(title))
	if prefixRE != nil {
		out = prefixRE.ReplaceAllString(out, "")
	}
	out = regexp.MustCompile(`\bp[0-4]\b`).ReplaceAllString(out, " ")
	out = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(out, " ")
	return strings.TrimSpace(regexp.MustCompile(`\s+`).ReplaceAllString(out, " "))
}

func tokenizeProve(text string) map[string]struct{} {
	tokens := map[string]struct{}{}
	for _, tok := range regexp.MustCompile(`[a-z0-9]+`).FindAllString(strings.ToLower(text), -1) {
		if len(tok) < 3 {
			continue
		}
		if _, stop := proveDefaultStopwords[tok]; stop {
			continue
		}
		tokens[tok] = struct{}{}
	}
	return tokens
}

func sequenceSimilarity(a, b string) float64 {
	if a == b {
		return 1
	}
	if a == "" || b == "" {
		return 0
	}
	maxLen := maxInt(len(a), len(b))
	dist := levenshteinDistance(a, b)
	return 1 - (float64(dist) / float64(maxLen))
}

func levenshteinDistance(a, b string) int {
	ar := []rune(a)
	br := []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}
	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := 0; j <= len(br); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 0
			if ar[i-1] != br[j-1] {
				cost = 1
			}
			curr[j] = minInt(
				minInt(curr[j-1]+1, prev[j]+1),
				prev[j-1]+cost,
			)
		}
		copy(prev, curr)
	}
	return prev[len(br)]
}

func printProvePacket(packet provePacket) {
	fmt.Printf("%s  %s  confidence=%.2f\n", packet.IssueID, packet.Classification, packet.Confidence)
	fmt.Printf("title: %s\n", packet.Title)
	fmt.Printf("status: %s\n", packet.Status)
	fmt.Printf("recommendation: %s\n", packet.Recommendation.Action)
	fmt.Printf("signals: strong=%d weak=%d test_like=%d\n",
		packet.CompletionSignalCounts["strong"],
		packet.CompletionSignalCounts["weak"],
		packet.CompletionSignalCounts["test_like"],
	)
	for _, line := range packet.Rationale {
		fmt.Printf("- %s\n", line)
	}
	if len(packet.PathEvidence) > 0 {
		fmt.Println("paths:")
		for _, item := range packet.PathEvidence {
			state := "missing"
			if item.Exists {
				state = "exists"
			}
			fmt.Printf("  - [%s] %s\n", state, item.PathText)
		}
	}
	if len(packet.DuplicateCandidates) > 0 {
		fmt.Println("duplicates:")
		for _, item := range packet.DuplicateCandidates {
			fmt.Printf("  - %v score=%.3f status=%v :: %v\n", item["id"], item["score"], item["status"], item["title"])
		}
	}
}

func round2(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}

func round3(v float64) float64 {
	return float64(int(v*1000+0.5)) / 1000
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func isWithinRoot(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}
