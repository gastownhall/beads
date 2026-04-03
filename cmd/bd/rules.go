package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"
	"unicode"

	"github.com/spf13/cobra"
)

// --- Types ---

// RuleFile represents a parsed .claude/rules/*.md file.
type RuleFile struct {
	Path      string   `json:"path"`
	Name      string   `json:"name"`
	Title     string   `json:"title"`
	DoLines   []string `json:"do_lines"`
	DontLines []string `json:"dont_lines"`
	Body      string   `json:"body,omitempty"`
	Keywords  []string `json:"keywords"`
	Tokens    int      `json:"tokens"`
}

// ContradictionReport describes a tension between two rules.
type ContradictionReport struct {
	RuleA      string  `json:"rule_a"`
	RuleB      string  `json:"rule_b"`
	Tension    string  `json:"tension"`
	DoLineA    string  `json:"do_line_a"`
	DontLineB  string  `json:"dont_line_b"`
	ScopeScore float64 `json:"scope_score"`
}

// MergeCandidate represents a group of rules that could be combined.
type MergeCandidate struct {
	GroupLabel string   `json:"group_label"`
	Rules      []string `json:"rules"`
	Score      float64  `json:"score"`
}

// AuditResult is the full output of `bd rules audit`.
type AuditResult struct {
	TotalRules      int                   `json:"total_rules"`
	TokenEstimate   int                   `json:"token_estimate"`
	Contradictions  []ContradictionReport `json:"contradictions"`
	MergeCandidates []MergeCandidate      `json:"merge_candidates"`
	Rules           []RuleFile            `json:"rules,omitempty"`
}

// --- Stop Words ---

var stopWords = map[string]bool{
	"the": true, "a": true, "is": true, "to": true, "for": true,
	"and": true, "or": true, "in": true, "of": true, "it": true,
	"that": true, "this": true, "with": true, "be": true, "not": true,
	"do": true, "don't": true, "use": true, "when": true, "before": true,
	"after": true, "should": true, "must": true, "always": true, "never": true,
	"an": true, "are": true, "as": true, "at": true, "by": true,
	"from": true, "has": true, "have": true, "if": true, "on": true,
	"was": true, "were": true, "will": true, "you": true, "your": true,
}

// --- Antonym Pairs ---

// antonymPairs maps words to their antonyms for contradiction detection.
var antonymPairs = map[string][]string{
	"block":    {"proceed", "parallel"},
	"proceed":  {"block"},
	"parallel": {"block"},
	"verbose":  {"minimize", "concise"},
	"minimize": {"verbose"},
	"concise":  {"verbose"},
	"spawn":    {"reuse"},
	"reuse":    {"spawn"},
	"wait":     {"skip"},
	"skip":     {"wait"},
	"log":      {"suppress"},
	"suppress": {"log"},
}

// --- Regex Patterns ---

var (
	headingRe = regexp.MustCompile(`(?m)^#\s+(.+)`)
	doRe      = regexp.MustCompile(`(?i)^\*\*Do:?\*\*:?\s*(.*)`)
	dontRe    = regexp.MustCompile(`(?i)^\*\*Don'?t:?\*\*:?\s*(.*)`)
)

// --- Core Functions ---

// ParseRuleFile reads a .md file and extracts structured rule data.
func ParseRuleFile(path string) (RuleFile, error) {
	// #nosec G304 -- path comes from controlled filepath.Join of user-specified rules directory
	data, err := os.ReadFile(path)
	if err != nil {
		return RuleFile{}, fmt.Errorf("read rule file %s: %w", path, err)
	}

	content := string(data)
	name := strings.TrimSuffix(filepath.Base(path), ".md")

	rf := RuleFile{
		Path: path,
		Name: name,
		Body: content,
		// Rough token estimate: 1 token ~ 4 chars
		Tokens: len(content) / 4,
	}

	// Extract title from first heading
	if m := headingRe.FindStringSubmatch(content); len(m) > 1 {
		rf.Title = strings.TrimSpace(m[1])
	} else {
		rf.Title = name
	}

	// Extract Do and Don't blocks
	rf.DoLines, rf.DontLines = extractAllDirectives(content)

	// Extract keywords from Do/Don't lines first, fallback to body
	allDirectives := append(rf.DoLines, rf.DontLines...)
	if len(allDirectives) > 0 {
		rf.Keywords = ExtractKeywords(allDirectives)
	} else {
		rf.Keywords = ExtractKeywords([]string{content})
	}

	return rf, nil
}

// extractAllDirectives parses Do and Don't blocks from rule content.
// Don't patterns are checked first to avoid false matches (since "Don't" contains "Do").
func extractAllDirectives(content string) (doLines, dontLines []string) {
	lines := strings.Split(content, "\n")

	// blockType: 0=none, 1=do, 2=dont
	blockType := 0

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Check Don't first (it contains "Do", so must be checked before Do)
		if m := dontRe.FindStringSubmatch(line); len(m) > 1 {
			blockType = 2
			text := strings.TrimSpace(m[1])
			if text != "" {
				dontLines = append(dontLines, text)
			}
			continue
		}

		if m := doRe.FindStringSubmatch(line); len(m) > 1 {
			blockType = 1
			text := strings.TrimSpace(m[1])
			if text != "" {
				doLines = append(doLines, text)
			}
			continue
		}

		if blockType != 0 {
			// Continuation: line starts with - or is non-empty non-heading text
			if strings.HasPrefix(trimmed, "-") || (strings.HasPrefix(trimmed, "*") && !strings.HasPrefix(trimmed, "**")) {
				// Strip leading bullet
				text := strings.TrimLeft(trimmed, "-* ")
				if text != "" {
					if blockType == 1 {
						doLines = append(doLines, text)
					} else {
						dontLines = append(dontLines, text)
					}
				}
			} else if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "**") {
				// End of block
				blockType = 0
			} else {
				// Plain continuation text
				if blockType == 1 {
					doLines = append(doLines, trimmed)
				} else {
					dontLines = append(dontLines, trimmed)
				}
			}
		}
	}
	return doLines, dontLines
}

// ExtractKeywords tokenizes lines, removes stop words, lowercases, and deduplicates.
func ExtractKeywords(lines []string) []string {
	seen := make(map[string]bool)
	var keywords []string

	for _, line := range lines {
		words := tokenizeWords(line)
		for _, w := range words {
			w = strings.ToLower(w)
			if len(w) < 2 {
				continue
			}
			if stopWords[w] {
				continue
			}
			if !seen[w] {
				seen[w] = true
				keywords = append(keywords, w)
			}
		}
	}

	sort.Strings(keywords)
	return keywords
}

// tokenize splits text into words, stripping punctuation.
func tokenizeWords(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\''
	})
}

// JaccardSimilarity computes keyword overlap between two keyword sets.
func JaccardSimilarity(a, b []string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0.0
	}

	setA := make(map[string]bool, len(a))
	for _, w := range a {
		setA[w] = true
	}
	setB := make(map[string]bool, len(b))
	for _, w := range b {
		setB[w] = true
	}

	intersection := 0
	for w := range setA {
		if setB[w] {
			intersection++
		}
	}

	union := len(setA)
	for w := range setB {
		if !setA[w] {
			union++
		}
	}

	if union == 0 {
		return 0.0
	}
	return float64(intersection) / float64(union)
}

// DetectContradictions finds opposing directives across rule pairs.
// Two rules contradict when they share scope (jaccard >= scopeThreshold)
// and have opposing directives (same verb in Do vs Don't, or antonym pairs).
func DetectContradictions(rules []RuleFile, scopeThreshold float64) []ContradictionReport {
	var reports []ContradictionReport

	for i := 0; i < len(rules); i++ {
		for j := i + 1; j < len(rules); j++ {
			a, b := rules[i], rules[j]
			score := JaccardSimilarity(a.Keywords, b.Keywords)
			if score < scopeThreshold {
				continue
			}

			// Check for direct contradictions: verb in A's Do appears in B's Don't
			if c := findDirectContradiction(a, b, score); c != nil {
				reports = append(reports, *c)
				continue
			}
			// Check reverse direction
			if c := findDirectContradiction(b, a, score); c != nil {
				// Swap labels since we checked b->a
				c.RuleA, c.RuleB = a.Name+".md", b.Name+".md"
				reports = append(reports, *c)
				continue
			}

			// Check for antonym pair contradictions in Do vs Do
			if c := findAntonymContradiction(a, b, score); c != nil {
				reports = append(reports, *c)
			}
		}
	}

	return reports
}

// findDirectContradiction checks if a verb from A's Do appears in B's Don't.
func findDirectContradiction(a, b RuleFile, scopeScore float64) *ContradictionReport {
	aDoWords := extractActionWords(a.DoLines)
	bDontWords := extractActionWords(b.DontLines)

	for word, doLine := range aDoWords {
		if dontLine, ok := bDontWords[word]; ok {
			tension := truncateTension(
				fmt.Sprintf("%q vs %q", summarizeLine(doLine), summarizeLine(dontLine)),
			)
			return &ContradictionReport{
				RuleA:      a.Name + ".md",
				RuleB:      b.Name + ".md",
				Tension:    tension,
				DoLineA:    doLine,
				DontLineB:  dontLine,
				ScopeScore: scopeScore,
			}
		}
	}
	return nil
}

// findAntonymContradiction checks if A's Do words have antonyms in B's Do words.
func findAntonymContradiction(a, b RuleFile, scopeScore float64) *ContradictionReport {
	aDoWords := extractActionWords(a.DoLines)
	bDoWords := extractActionWords(b.DoLines)

	for wordA, lineA := range aDoWords {
		antonyms, ok := antonymPairs[wordA]
		if !ok {
			continue
		}
		for _, ant := range antonyms {
			if lineB, ok := bDoWords[ant]; ok {
				tension := truncateTension(
					fmt.Sprintf("%q vs %q", summarizeLine(lineA), summarizeLine(lineB)),
				)
				return &ContradictionReport{
					RuleA:      a.Name + ".md",
					RuleB:      b.Name + ".md",
					Tension:    tension,
					DoLineA:    lineA,
					DontLineB:  lineB,
					ScopeScore: scopeScore,
				}
			}
		}
	}
	return nil
}

// extractActionWords returns a map of lowercase action words to their source line.
func extractActionWords(lines []string) map[string]string {
	result := make(map[string]string)
	for _, line := range lines {
		words := tokenizeWords(line)
		for _, w := range words {
			w = strings.ToLower(w)
			if len(w) >= 2 && !stopWords[w] {
				if _, exists := result[w]; !exists {
					result[w] = line
				}
			}
		}
	}
	return result
}

// summarizeLine truncates a line for display in tension descriptions.
func summarizeLine(line string) string {
	if len(line) > 40 {
		return line[:37] + "..."
	}
	return line
}

// truncateTension ensures tension string fits in a table cell.
func truncateTension(s string) string {
	if len(s) > 60 {
		return s[:57] + "..."
	}
	return s
}

// FindMergeCandidates groups rules by keyword overlap using single-linkage clustering.
func FindMergeCandidates(rules []RuleFile, threshold float64) []MergeCandidate {
	n := len(rules)
	if n < 2 {
		return nil
	}

	// Build adjacency: which pairs exceed the threshold
	type pair struct {
		i, j  int
		score float64
	}
	var pairs []pair
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			score := JaccardSimilarity(rules[i].Keywords, rules[j].Keywords)
			if score >= threshold {
				pairs = append(pairs, pair{i, j, score})
			}
		}
	}

	if len(pairs) == 0 {
		return nil
	}

	// Union-Find for single-linkage clustering
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(x, y int) {
		px, py := find(x), find(y)
		if px != py {
			parent[px] = py
		}
	}

	for _, p := range pairs {
		union(p.i, p.j)
	}

	// Collect groups
	groups := make(map[int][]int)
	for i := 0; i < n; i++ {
		root := find(i)
		groups[root] = append(groups[root], i)
	}

	// Build merge candidates (only groups with 2+ members)
	var candidates []MergeCandidate
	for _, members := range groups {
		if len(members) < 2 {
			continue
		}

		// Compute average pairwise score
		var totalScore float64
		var pairCount int
		for mi := 0; mi < len(members); mi++ {
			for mj := mi + 1; mj < len(members); mj++ {
				totalScore += JaccardSimilarity(rules[members[mi]].Keywords, rules[members[mj]].Keywords)
				pairCount++
			}
		}
		avgScore := 0.0
		if pairCount > 0 {
			avgScore = totalScore / float64(pairCount)
		}

		// Collect rule names
		var ruleNames []string
		for _, idx := range members {
			ruleNames = append(ruleNames, rules[idx].Name+".md")
		}
		sort.Strings(ruleNames)

		// Find most frequent keyword for group label
		label := findGroupLabel(rules, members)

		candidates = append(candidates, MergeCandidate{
			GroupLabel: label,
			Rules:      ruleNames,
			Score:      roundTo2(avgScore),
		})
	}

	// Sort by score descending
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})

	return candidates
}

// findGroupLabel finds the most common keyword across a group of rules.
func findGroupLabel(rules []RuleFile, indices []int) string {
	freq := make(map[string]int)
	for _, idx := range indices {
		for _, kw := range rules[idx].Keywords {
			freq[kw]++
		}
	}

	bestWord := "rules"
	bestCount := 0
	for w, c := range freq {
		if c > bestCount || (c == bestCount && w < bestWord) {
			bestWord = w
			bestCount = c
		}
	}
	return bestWord
}

// roundTo2 rounds a float to 2 decimal places.
func roundTo2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}

// CompactRules merges a group of rules into a single composite markdown file.
func CompactRules(rules []RuleFile, groupLabel string) (string, error) {
	if len(rules) == 0 {
		return "", fmt.Errorf("no rules to compact")
	}

	// Collect and deduplicate Do/Don't lines
	seenDo := make(map[string]bool)
	seenDont := make(map[string]bool)
	var doLines, dontLines []string

	for _, r := range rules {
		for _, line := range r.DoLines {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" && !seenDo[trimmed] {
				seenDo[trimmed] = true
				doLines = append(doLines, trimmed)
			}
		}
		for _, line := range r.DontLines {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" && !seenDont[trimmed] {
				seenDont[trimmed] = true
				dontLines = append(dontLines, trimmed)
			}
		}
	}

	// Build composite
	var sb strings.Builder
	sb.WriteString("# ")
	sb.WriteString(titleCase(groupLabel))
	sb.WriteString("\n")

	if len(doLines) > 0 {
		sb.WriteString("**Do:** ")
		sb.WriteString(strings.Join(doLines, ". "))
		sb.WriteString("\n")
	}
	if len(dontLines) > 0 {
		sb.WriteString("**Don't:** ")
		sb.WriteString(strings.Join(dontLines, ". "))
		sb.WriteString("\n")
	}

	// Source attribution
	var sourceNames []string
	for _, r := range rules {
		sourceNames = append(sourceNames, r.Name+".md")
	}
	sb.WriteString("\nSource rules: ")
	sb.WriteString(strings.Join(sourceNames, ", "))
	sb.WriteString("\n")

	return sb.String(), nil
}

// RunAudit is the top-level orchestrator for `bd rules audit`.
func RunAudit(rulesDir string, threshold float64) (*AuditResult, error) {
	entries, err := os.ReadDir(rulesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return &AuditResult{}, nil
		}
		return nil, fmt.Errorf("read rules directory: %w", err)
	}

	var rules []RuleFile
	totalTokens := 0

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		path := filepath.Join(rulesDir, entry.Name())
		rf, err := ParseRuleFile(path)
		if err != nil {
			// Skip files that can't be parsed
			fmt.Fprintf(os.Stderr, "Warning: skipping %s: %v\n", entry.Name(), err)
			continue
		}
		rules = append(rules, rf)
		totalTokens += rf.Tokens
	}

	result := &AuditResult{
		TotalRules:    len(rules),
		TokenEstimate: totalTokens,
	}

	if len(rules) < 2 {
		result.Contradictions = []ContradictionReport{}
		result.MergeCandidates = []MergeCandidate{}
		result.Rules = rules
		return result, nil
	}

	result.Contradictions = DetectContradictions(rules, 0.3)
	result.MergeCandidates = FindMergeCandidates(rules, threshold)
	result.Rules = rules

	if result.Contradictions == nil {
		result.Contradictions = []ContradictionReport{}
	}
	if result.MergeCandidates == nil {
		result.MergeCandidates = []MergeCandidate{}
	}

	return result, nil
}

// --- Cobra Commands ---

var rulesCmd = &cobra.Command{
	Use:     "rules",
	Short:   "Audit and compact Claude rules",
	GroupID: "maint",
}

var rulesAuditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Scan rules for contradictions and merge opportunities",
	Run:   runRulesAudit,
}

var rulesCompactCmd = &cobra.Command{
	Use:   "compact",
	Short: "Merge related rules into composites",
	Run:   runRulesCompact,
}

func init() {
	// Audit command flags
	rulesAuditCmd.Flags().String("path", ".claude/rules/", "Path to rules directory")
	rulesAuditCmd.Flags().Float64("threshold", 0.6, "Jaccard similarity threshold")

	// Compact command flags
	rulesCompactCmd.Flags().String("path", ".claude/rules/", "Path to rules directory")
	rulesCompactCmd.Flags().StringSlice("group", nil, "Rule names to merge")
	rulesCompactCmd.Flags().Bool("auto", false, "Apply audit suggestions")
	rulesCompactCmd.Flags().Bool("dry-run", false, "Preview without applying")

	// Register subcommands
	rulesCmd.AddCommand(rulesAuditCmd)
	rulesCmd.AddCommand(rulesCompactCmd)
	rootCmd.AddCommand(rulesCmd)
}

func runRulesAudit(cmd *cobra.Command, args []string) {
	rulesPath, _ := cmd.Flags().GetString("path")
	threshold, _ := cmd.Flags().GetFloat64("threshold")

	result, err := RunAudit(rulesPath, threshold)
	if err != nil {
		FatalErrorRespectJSON("rules audit failed: %v", err)
	}

	if jsonOutput {
		outputJSON(result)
		return
	}

	// Human-readable output
	fmt.Printf("Rules Audit — %s\n", rulesPath)
	fmt.Println(strings.Repeat("=", 60))
	fmt.Println()

	fmt.Println("Summary:")
	fmt.Printf("  Total rules:        %d\n", result.TotalRules)
	fmt.Printf("  Token estimate:     ~%d\n", result.TokenEstimate)
	fmt.Printf("  Contradictions:     %d\n", len(result.Contradictions))

	mergeRuleCount := 0
	for _, mc := range result.MergeCandidates {
		mergeRuleCount += len(mc.Rules)
	}
	if len(result.MergeCandidates) > 0 {
		fmt.Printf("  Merge candidates:   %d groups (%d rules)\n",
			len(result.MergeCandidates), mergeRuleCount)
	} else {
		fmt.Println("  Merge candidates:   0")
	}
	fmt.Println()

	// Contradictions table
	if len(result.Contradictions) > 0 {
		fmt.Println("Contradictions:")
		tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
		fmt.Fprintf(tw, "  Rule A\tRule B\tTension\n")
		fmt.Fprintf(tw, "  ------\t------\t-------\n")
		for _, c := range result.Contradictions {
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", c.RuleA, c.RuleB, c.Tension)
		}
		_ = tw.Flush()
		fmt.Println()
	}

	// Merge candidates
	if len(result.MergeCandidates) > 0 {
		fmt.Printf("Merge Candidates (similarity > %.2f):\n", threshold)
		for i, mc := range result.MergeCandidates {
			fmt.Printf("  Group %d — %q (score: %.2f)\n", i+1, mc.GroupLabel, mc.Score)
			for _, r := range mc.Rules {
				fmt.Printf("    → %s\n", r)
			}
			suggested := strings.ReplaceAll(mc.GroupLabel, " ", "-") + ".md"
			fmt.Printf("    Suggested: merge into %s\n\n", suggested)
		}
		fmt.Printf("Run `bd rules compact --auto` to apply suggested merges.\n")
	}
}

func runRulesCompact(cmd *cobra.Command, args []string) {
	CheckReadonly("rules compact")

	rulesPath, _ := cmd.Flags().GetString("path")
	groupNames, _ := cmd.Flags().GetStringSlice("group")
	autoMode, _ := cmd.Flags().GetBool("auto")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	if !autoMode && len(groupNames) == 0 {
		FatalErrorRespectJSON("specify --group <rule1,rule2,...> or --auto")
	}

	if autoMode {
		// Run audit to get merge candidates
		result, err := RunAudit(rulesPath, 0.6)
		if err != nil {
			FatalErrorRespectJSON("audit for auto-compact failed: %v", err)
		}

		if len(result.MergeCandidates) == 0 {
			if jsonOutput {
				outputJSON(map[string]string{"status": "no merge candidates found"})
			} else {
				fmt.Println("No merge candidates found.")
			}
			return
		}

		type compactResult struct {
			Group   string `json:"group"`
			Output  string `json:"output"`
			Rules   int    `json:"rules_merged"`
			Applied bool   `json:"applied"`
		}
		var results []compactResult

		for _, mc := range result.MergeCandidates {
			// Parse the actual rule files for this group
			var groupRules []RuleFile
			for _, ruleName := range mc.Rules {
				path := filepath.Join(rulesPath, ruleName)
				rf, err := ParseRuleFile(path)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Warning: skipping %s: %v\n", ruleName, err)
					continue
				}
				groupRules = append(groupRules, rf)
			}

			merged, err := CompactRules(groupRules, mc.GroupLabel)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: compact group %q failed: %v\n", mc.GroupLabel, err)
				continue
			}

			applied := false
			if !dryRun {
				outName := strings.ReplaceAll(mc.GroupLabel, " ", "-") + ".md"
				outPath := filepath.Join(rulesPath, outName)
				if err := os.WriteFile(outPath, []byte(merged), 0o600); err != nil {
					fmt.Fprintf(os.Stderr, "Error writing %s: %v\n", outPath, err)
					continue
				}
				// Delete source files
				for _, ruleName := range mc.Rules {
					srcPath := filepath.Join(rulesPath, ruleName)
					_ = os.Remove(srcPath)
				}
				applied = true
			}

			results = append(results, compactResult{
				Group:   mc.GroupLabel,
				Output:  merged,
				Rules:   len(groupRules),
				Applied: applied,
			})

			if !jsonOutput {
				if dryRun {
					fmt.Printf("Preview merge → %s.md:\n", mc.GroupLabel)
				} else {
					fmt.Printf("Merged → %s.md:\n", mc.GroupLabel)
				}
				fmt.Println(strings.Repeat("─", 40))
				fmt.Print(merged)
				fmt.Println(strings.Repeat("─", 40))
				fmt.Println()
			}
		}

		if jsonOutput {
			outputJSON(results)
		}
		return
	}

	// Manual --group mode
	var groupRules []RuleFile
	for _, name := range groupNames {
		if !strings.HasSuffix(name, ".md") {
			name = name + ".md"
		}
		path := filepath.Join(rulesPath, name)
		rf, err := ParseRuleFile(path)
		if err != nil {
			FatalErrorRespectJSON("cannot read rule %s: %v", name, err)
		}
		groupRules = append(groupRules, rf)
	}

	if len(groupRules) < 2 {
		FatalErrorRespectJSON("need at least 2 rules to merge")
	}

	label := findGroupLabel(groupRules, makeRange(len(groupRules)))
	merged, err := CompactRules(groupRules, label)
	if err != nil {
		FatalErrorRespectJSON("compact failed: %v", err)
	}

	if jsonOutput {
		outputJSON(map[string]interface{}{
			"group":   label,
			"output":  merged,
			"rules":   len(groupRules),
			"dry_run": dryRun,
		})
		return
	}

	outName := strings.ReplaceAll(label, " ", "-") + ".md"
	if dryRun {
		fmt.Printf("Preview merge → %s:\n", outName)
	} else {
		fmt.Printf("Merged → %s:\n", outName)
	}
	fmt.Println(strings.Repeat("─", 40))
	fmt.Print(merged)
	fmt.Println(strings.Repeat("─", 40))

	if !dryRun {
		outPath := filepath.Join(rulesPath, outName)
		if err := os.WriteFile(outPath, []byte(merged), 0o600); err != nil {
			FatalErrorRespectJSON("write merged file: %v", err)
		}
		for _, rf := range groupRules {
			_ = os.Remove(rf.Path)
		}
		fmt.Printf("\nCreated %s, deleted %d source files.\n", outName, len(groupRules))
	}
}

// titleCase capitalizes the first letter of each word.
func titleCase(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

// makeRange returns a slice [0, 1, ..., n-1].
func makeRange(n int) []int {
	r := make([]int, n)
	for i := range r {
		r[i] = i
	}
	return r
}
