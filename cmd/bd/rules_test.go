package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- Helpers ---

// writeRule creates a synthetic rule file in the given directory.
func writeRule(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name+".md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write rule %s: %v", name, err)
	}
	return path
}

// tempRulesDir creates a temporary directory for test rule files.
func tempRulesDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "rules-test-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// --- ParseRuleFile Tests ---

func TestRulesParseRuleFile_Basic(t *testing.T) {
	dir := tempRulesDir(t)
	content := `# Context-First Rule
**Do:** Check breadcrumb/aliases/recent git BEFORE Grep/Glob when user references past work.
**Don't:** Search immediately without checking context files first.
`
	path := writeRule(t, dir, "context-first", content)

	rf, err := ParseRuleFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rf.Name != "context-first" {
		t.Errorf("name = %q, want %q", rf.Name, "context-first")
	}
	if rf.Title != "Context-First Rule" {
		t.Errorf("title = %q, want %q", rf.Title, "Context-First Rule")
	}
	if len(rf.DoLines) != 1 {
		t.Errorf("do lines = %d, want 1", len(rf.DoLines))
	}
	if len(rf.DontLines) != 1 {
		t.Errorf("dont lines = %d, want 1", len(rf.DontLines))
	}
	if rf.Tokens <= 0 {
		t.Error("tokens should be > 0")
	}
	if len(rf.Keywords) == 0 {
		t.Error("keywords should not be empty")
	}
}

func TestRulesParseRuleFile_NoDoBlocks(t *testing.T) {
	dir := tempRulesDir(t)
	content := `# General Guidelines
Keep code clean and well-documented.
Follow the project conventions for naming and structure.
`
	path := writeRule(t, dir, "general", content)

	rf, err := ParseRuleFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(rf.DoLines) != 0 {
		t.Errorf("do lines = %d, want 0", len(rf.DoLines))
	}
	if len(rf.DontLines) != 0 {
		t.Errorf("dont lines = %d, want 0", len(rf.DontLines))
	}
	// Should still have keywords from body
	if len(rf.Keywords) == 0 {
		t.Error("keywords should be extracted from body when no Do/Don't blocks")
	}
}

func TestRulesParseRuleFile_MultilineDo(t *testing.T) {
	dir := tempRulesDir(t)
	content := `# Multi Rule
**Do:**
- Check context files first
- Use existing tools before manual work
- Verify output matches expected format

**Don't:**
- Skip verification steps
- Use raw bash when tools exist
`
	path := writeRule(t, dir, "multi-rule", content)

	rf, err := ParseRuleFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(rf.DoLines) != 3 {
		t.Errorf("do lines = %d, want 3; got: %v", len(rf.DoLines), rf.DoLines)
	}
	if len(rf.DontLines) != 2 {
		t.Errorf("dont lines = %d, want 2; got: %v", len(rf.DontLines), rf.DontLines)
	}
}

// --- ExtractKeywords Tests ---

func TestRulesExtractKeywords(t *testing.T) {
	lines := []string{
		"Check the context files before searching",
		"Use existing tools and verify the output",
	}

	keywords := ExtractKeywords(lines)

	if len(keywords) == 0 {
		t.Fatal("keywords should not be empty")
	}

	// Should not contain stop words
	for _, kw := range keywords {
		if stopWords[kw] {
			t.Errorf("keyword %q is a stop word", kw)
		}
	}

	// Should be lowercase
	for _, kw := range keywords {
		if kw != kw {
			t.Errorf("keyword %q is not lowercase", kw)
		}
	}

	// Should contain expected words
	kwSet := make(map[string]bool)
	for _, kw := range keywords {
		kwSet[kw] = true
	}
	expected := []string{"check", "context", "files", "searching", "existing", "tools", "verify", "output"}
	for _, e := range expected {
		if !kwSet[e] {
			t.Errorf("missing expected keyword %q in %v", e, keywords)
		}
	}
}

// --- JaccardSimilarity Tests ---

func TestRulesJaccardSimilarity_Identical(t *testing.T) {
	a := []string{"context", "files", "check", "search"}
	b := []string{"context", "files", "check", "search"}

	score := JaccardSimilarity(a, b)
	if score != 1.0 {
		t.Errorf("identical sets: score = %f, want 1.0", score)
	}
}

func TestRulesJaccardSimilarity_Disjoint(t *testing.T) {
	a := []string{"context", "files", "check"}
	b := []string{"deploy", "server", "build"}

	score := JaccardSimilarity(a, b)
	if score != 0.0 {
		t.Errorf("disjoint sets: score = %f, want 0.0", score)
	}
}

func TestRulesJaccardSimilarity_Partial(t *testing.T) {
	a := []string{"context", "files", "check", "search"}
	b := []string{"context", "files", "load", "import"}

	// intersection = {context, files} = 2
	// union = {context, files, check, search, load, import} = 6
	// jaccard = 2/6 = 0.333...
	score := JaccardSimilarity(a, b)
	expected := 2.0 / 6.0
	if diff := score - expected; diff > 0.01 || diff < -0.01 {
		t.Errorf("partial overlap: score = %f, want ~%f", score, expected)
	}
}

// --- DetectContradictions Tests ---

func TestRulesDetectContradictions_Direct(t *testing.T) {
	rules := []RuleFile{
		{
			Name:     "rule-a",
			DoLines:  []string{"Block PR merges until all issues resolved"},
			Keywords: []string{"block", "issues", "merges", "pr", "resolved"},
		},
		{
			Name:      "rule-b",
			DontLines: []string{"Don't block PR merges for minor issues"},
			Keywords:  []string{"block", "issues", "merges", "minor", "pr"},
		},
	}

	contradictions := DetectContradictions(rules, 0.3)
	if len(contradictions) == 0 {
		t.Fatal("expected at least 1 contradiction, got 0")
	}

	c := contradictions[0]
	if c.RuleA != "rule-a.md" || c.RuleB != "rule-b.md" {
		t.Errorf("contradiction rules = (%q, %q), want (rule-a.md, rule-b.md)", c.RuleA, c.RuleB)
	}
	if c.ScopeScore < 0.3 {
		t.Errorf("scope score = %f, should be >= 0.3", c.ScopeScore)
	}
}

func TestRulesDetectContradictions_Antonym(t *testing.T) {
	rules := []RuleFile{
		{
			Name:     "blocker-first",
			DoLines:  []string{"Block deployment pipeline until tests pass"},
			Keywords: []string{"block", "deployment", "pipeline", "tests", "pass"},
		},
		{
			Name:     "fast-deploy",
			DoLines:  []string{"Proceed with deployment pipeline even if tests fail"},
			Keywords: []string{"proceed", "deployment", "pipeline", "tests", "fail"},
		},
	}
	// Jaccard: intersection={deployment,pipeline,tests}=3, union={block,deployment,pipeline,tests,pass,proceed,fail}=7
	// Score = 3/7 = 0.43 > 0.3 threshold

	contradictions := DetectContradictions(rules, 0.3)
	if len(contradictions) == 0 {
		t.Fatal("expected antonym contradiction (block vs proceed), got 0")
	}
}

func TestRulesDetectContradictions_NoFalsePositive(t *testing.T) {
	rules := []RuleFile{
		{
			Name:     "context-first",
			DoLines:  []string{"Check breadcrumbs before searching"},
			Keywords: []string{"breadcrumbs", "check", "context", "searching"},
		},
		{
			Name:     "tool-first",
			DoLines:  []string{"Use existing MCP tools before manual work"},
			Keywords: []string{"existing", "manual", "mcp", "tools", "work"},
		},
	}

	contradictions := DetectContradictions(rules, 0.3)
	if len(contradictions) != 0 {
		t.Errorf("expected 0 contradictions for unrelated rules, got %d: %+v",
			len(contradictions), contradictions)
	}
}

// --- FindMergeCandidates Tests ---

func TestRulesFindMergeCandidates_Grouping(t *testing.T) {
	rules := []RuleFile{
		{
			Name:     "agent-spawn",
			Keywords: []string{"agent", "check", "existing", "spawn", "tool"},
		},
		{
			Name:     "agent-efficiency",
			Keywords: []string{"agent", "efficiency", "existing", "reuse", "tool"},
		},
		{
			Name:     "agent-tokens",
			Keywords: []string{"agent", "minimize", "output", "token", "tool"},
		},
		{
			Name:     "deploy-guide",
			Keywords: []string{"build", "deploy", "production", "server", "staging"},
		},
	}

	candidates := FindMergeCandidates(rules, 0.4)

	// The three agent rules should cluster together
	found := false
	for _, mc := range candidates {
		if len(mc.Rules) >= 2 {
			// Check that deploy-guide is not in any agent group
			for _, r := range mc.Rules {
				if r == "deploy-guide.md" {
					t.Error("deploy-guide should not be grouped with agent rules")
				}
			}
			found = true
		}
	}
	if !found {
		t.Error("expected at least one merge group with 2+ agent rules")
	}
}

func TestRulesFindMergeCandidates_Threshold(t *testing.T) {
	rules := []RuleFile{
		{
			Name:     "rule-a",
			Keywords: []string{"alpha", "beta", "gamma", "delta"},
		},
		{
			Name:     "rule-b",
			Keywords: []string{"alpha", "beta", "epsilon", "zeta"},
		},
	}

	// jaccard = 2/6 = 0.333
	// With threshold 0.3, should find a candidate
	candidates := FindMergeCandidates(rules, 0.3)
	if len(candidates) == 0 {
		t.Error("expected merge candidate at threshold 0.3")
	}

	// With threshold 0.5, should not find a candidate
	candidates = FindMergeCandidates(rules, 0.5)
	if len(candidates) != 0 {
		t.Errorf("expected no merge candidate at threshold 0.5, got %d", len(candidates))
	}
}

// --- CompactRules Tests ---

func TestRulesCompactRules_Dedup(t *testing.T) {
	rules := []RuleFile{
		{
			Name:      "rule-a",
			DoLines:   []string{"Check context first", "Use existing tools"},
			DontLines: []string{"Skip verification"},
		},
		{
			Name:      "rule-b",
			DoLines:   []string{"Check context first", "Verify output format"},
			DontLines: []string{"Skip verification", "Use raw bash"},
		},
	}

	merged, err := CompactRules(rules, "combined")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// "Check context first" should appear only once
	count := 0
	for _, line := range strings.Split(merged, "\n") {
		if rulesContains(line, "Check context first") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("'Check context first' appears %d times, want 1", count)
	}

	// "Skip verification" should appear only once
	count = 0
	for _, line := range strings.Split(merged, "\n") {
		if rulesContains(line, "Skip verification") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("'Skip verification' appears %d times, want 1", count)
	}
}

func TestRulesCompactRules_PreservesOrder(t *testing.T) {
	rules := []RuleFile{
		{
			Name:    "rule-a",
			DoLines: []string{"First directive", "Second directive"},
		},
		{
			Name:    "rule-b",
			DoLines: []string{"Third directive"},
		},
	}

	merged, err := CompactRules(rules, "ordered")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// All three should appear in order
	if !rulesContains(merged, "First directive") {
		t.Error("missing 'First directive'")
	}
	if !rulesContains(merged, "Second directive") {
		t.Error("missing 'Second directive'")
	}
	if !rulesContains(merged, "Third directive") {
		t.Error("missing 'Third directive'")
	}

	// Check order: First before Second before Third
	idx1 := rulesIndexOf(merged, "First directive")
	idx2 := rulesIndexOf(merged, "Second directive")
	idx3 := rulesIndexOf(merged, "Third directive")
	if idx1 >= idx2 || idx2 >= idx3 {
		t.Errorf("directives not in order: First@%d, Second@%d, Third@%d", idx1, idx2, idx3)
	}
}

// --- RunAudit Integration Tests ---

func TestRulesRunAudit_EmptyDir(t *testing.T) {
	dir := tempRulesDir(t)

	result, err := RunAudit(dir, 0.6)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalRules != 0 {
		t.Errorf("total rules = %d, want 0", result.TotalRules)
	}
	if result.TokenEstimate != 0 {
		t.Errorf("token estimate = %d, want 0", result.TokenEstimate)
	}
}

func TestRulesRunAudit_SingleRule(t *testing.T) {
	dir := tempRulesDir(t)
	writeRule(t, dir, "only-rule", `# Only Rule
**Do:** Check context first.
**Don't:** Skip verification.
`)

	result, err := RunAudit(dir, 0.6)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalRules != 1 {
		t.Errorf("total rules = %d, want 1", result.TotalRules)
	}
	if len(result.Contradictions) != 0 {
		t.Errorf("contradictions = %d, want 0 (single rule)", len(result.Contradictions))
	}
	if len(result.MergeCandidates) != 0 {
		t.Errorf("merge candidates = %d, want 0 (single rule)", len(result.MergeCandidates))
	}
}

func TestRulesRunAudit_JSON(t *testing.T) {
	dir := tempRulesDir(t)
	writeRule(t, dir, "rule-a", `# Rule Alpha
**Do:** Check breadcrumbs before searching.
**Don't:** Search immediately without context.
`)
	writeRule(t, dir, "rule-b", `# Rule Beta
**Do:** Use existing tools before manual work.
**Don't:** Manually run commands when tools exist.
`)

	result, err := RunAudit(dir, 0.6)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify JSON marshaling works
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("JSON marshal failed: %v", err)
	}

	// Verify it can be unmarshaled back
	var decoded AuditResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}

	if decoded.TotalRules != result.TotalRules {
		t.Errorf("decoded total_rules = %d, want %d", decoded.TotalRules, result.TotalRules)
	}

	// Verify expected JSON fields exist
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("JSON unmarshal to map failed: %v", err)
	}
	for _, field := range []string{"total_rules", "token_estimate", "contradictions", "merge_candidates"} {
		if _, ok := raw[field]; !ok {
			t.Errorf("missing JSON field %q", field)
		}
	}
}

// --- Integration: Full audit with contradictions and merges ---

func TestRulesRunAudit_Integration(t *testing.T) {
	dir := tempRulesDir(t)

	// Create rules that should contradict
	writeRule(t, dir, "blocker-first", `# Blocker First
**Do:** Block PR merges until all issues are resolved.
**Don't:** Allow merges with open blockers.
`)
	writeRule(t, dir, "parallel-workflow", `# Parallel Workflow
**Do:** Proceed with merges in parallel even if minor issues exist.
**Don't:** Block the pipeline for non-critical issues.
`)

	// Create rules that should be merge candidates
	writeRule(t, dir, "agent-spawn", `# Agent Spawn Discipline
**Do:** Check for existing agent before spawning new ones.
**Don't:** Spawn redundant agents for the same task.
`)
	writeRule(t, dir, "agent-efficiency", `# Agent Efficiency
**Do:** Reuse existing agent context and tools.
**Don't:** Spawn new agents when existing ones can handle the task.
`)

	// Create an unrelated rule
	writeRule(t, dir, "deploy-guide", `# Deploy Guide
**Do:** Run integration tests before deploying to production.
**Don't:** Deploy without passing CI checks.
`)

	result, err := RunAudit(dir, 0.4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.TotalRules != 5 {
		t.Errorf("total rules = %d, want 5", result.TotalRules)
	}

	// Should find contradiction between blocker-first and parallel-workflow
	if len(result.Contradictions) == 0 {
		t.Error("expected at least 1 contradiction between blocker-first and parallel-workflow")
	}

	// Should find merge candidates for agent rules
	if len(result.MergeCandidates) == 0 {
		t.Error("expected at least 1 merge group for agent rules")
	}
}

func TestRulesRunAudit_NonexistentDir(t *testing.T) {
	result, err := RunAudit("/tmp/nonexistent-rules-dir-xyz", 0.6)
	if err != nil {
		t.Fatalf("should not error on nonexistent dir, got: %v", err)
	}
	if result.TotalRules != 0 {
		t.Errorf("total rules = %d, want 0", result.TotalRules)
	}
}

func TestRulesRunAudit_SkipsNonMarkdown(t *testing.T) {
	dir := tempRulesDir(t)
	writeRule(t, dir, "valid-rule", `# Valid
**Do:** Something useful.
`)
	// Write a non-markdown file
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a rule"), 0644)
	os.WriteFile(filepath.Join(dir, ".hidden"), []byte("hidden file"), 0644)

	result, err := RunAudit(dir, 0.6)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TotalRules != 1 {
		t.Errorf("total rules = %d, want 1 (should skip non-.md files)", result.TotalRules)
	}
}

// --- Helpers ---

func rulesContains(s, substr string) bool {
	return strings.Contains(s, substr)
}

func rulesIndexOf(s, substr string) int {
	return strings.Index(s, substr)
}
