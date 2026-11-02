package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
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

// --- rules compact apply-path tests ---

func newRulesCompactTestCmd(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "compact"}
	cmd.Flags().String("path", "", "")
	cmd.Flags().StringSlice("group", nil, "")
	cmd.Flags().Bool("auto", false, "")
	cmd.Flags().Bool("dry-run", false, "")
	cmd.Flags().Bool("force", false, "")
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	return cmd
}

// snapshotRulesDir returns name -> content for every file in dir.
func snapshotRulesDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	files := make(map[string]string, len(entries))
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		files[e.Name()] = string(data)
	}
	return files
}

func assertRulesDirUnchanged(t *testing.T, dir string, before map[string]string) {
	t.Helper()
	after := snapshotRulesDir(t, dir)
	if len(after) != len(before) {
		t.Fatalf("rules dir changed: before %v, after %v", ruleFileNames(before), ruleFileNames(after))
	}
	for name, content := range before {
		if after[name] != content {
			t.Errorf("%s changed:\nbefore: %q\nafter:  %q", name, content, after[name])
		}
	}
}

func ruleFileNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

const sharedDiskDirective = "# %s\n\n**Do:** validate input before writing to disk\n\n## Rationale\nProse for %s.\n"

func TestRulesCompact_PreviewsWithoutForce(t *testing.T) {
	for _, args := range [][]string{{"--auto"}, {"--group", "alpha,beta"}, {"--auto", "--dry-run", "--force"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			dir := tempRulesDir(t)
			writeRule(t, dir, "alpha", fmt.Sprintf(sharedDiskDirective, "Alpha", "alpha"))
			writeRule(t, dir, "beta", fmt.Sprintf(sharedDiskDirective, "Beta", "beta"))
			before := snapshotRulesDir(t, dir)

			cmd := newRulesCompactTestCmd(t, append([]string{"--path", dir}, args...)...)
			out := captureStdout(t, func() error { return runRulesCompact(cmd, nil) })

			assertRulesDirUnchanged(t, dir, before)
			for _, want := range []string{"Preview merge", "Would delete: alpha.md, beta.md", "--force"} {
				if !strings.Contains(out, want) {
					t.Errorf("preview output missing %q:\n%s", want, out)
				}
			}
		})
	}
}

func TestRulesCompact_ForceApplies(t *testing.T) {
	dir := tempRulesDir(t)
	writeRule(t, dir, "alpha", fmt.Sprintf(sharedDiskDirective, "Alpha", "alpha"))
	writeRule(t, dir, "beta", fmt.Sprintf(sharedDiskDirective, "Beta", "beta"))

	cmd := newRulesCompactTestCmd(t, "--path", dir, "--auto", "--force")
	captureStdout(t, func() error { return runRulesCompact(cmd, nil) })

	after := snapshotRulesDir(t, dir)
	if len(after) != 3 {
		t.Fatalf("want the composite and both backups left, got %v", ruleFileNames(after))
	}
	for name, content := range after {
		switch name {
		case "alpha.md.backup":
			if content != fmt.Sprintf(sharedDiskDirective, "Alpha", "alpha") {
				t.Errorf("%s must hold the exact original bytes: %q", name, content)
			}
		case "beta.md.backup":
			if content != fmt.Sprintf(sharedDiskDirective, "Beta", "beta") {
				t.Errorf("%s must hold the exact original bytes: %q", name, content)
			}
		default:
			if !strings.Contains(content, "validate input before writing to disk") || !strings.Contains(content, "Source rules: alpha.md, beta.md") {
				t.Errorf("%s is not the expected composite: %q", name, content)
			}
		}
	}
}

// A source rule named like the composite used to be overwritten by the
// composite and then deleted along with the other sources, leaving nothing.
func TestRulesCompact_RefusesSourceNamedLikeComposite(t *testing.T) {
	for _, args := range [][]string{{"--auto"}, {"--group", "disk,beta"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			dir := tempRulesDir(t)
			writeRule(t, dir, "disk", fmt.Sprintf(sharedDiskDirective, "Disk", "disk"))
			writeRule(t, dir, "beta", fmt.Sprintf(sharedDiskDirective, "Beta", "beta"))
			before := snapshotRulesDir(t, dir)

			cmd := newRulesCompactTestCmd(t, append([]string{"--path", dir, "--force"}, args...)...)
			var err error
			stderr := captureStderr(t, func() {
				captureStdoutIgnoringError(t, func() { err = runRulesCompact(cmd, nil) })
			})

			if err == nil {
				t.Fatal("expected compact to fail when the composite path is a source")
			}
			if !strings.Contains(stderr, "disk.md already exists") {
				t.Errorf("expected the refusal to name disk.md, got: %s", stderr)
			}
			assertRulesDirUnchanged(t, dir, before)
		})
	}
}

// An unrelated rule that happens to share the composite's name used to be
// overwritten silently.
func TestRulesCompact_RefusesUnrelatedExistingFile(t *testing.T) {
	for _, args := range [][]string{{"--auto"}, {"--group", "alpha,beta"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			dir := tempRulesDir(t)
			writeRule(t, dir, "alpha", fmt.Sprintf(sharedDiskDirective, "Alpha", "alpha"))
			writeRule(t, dir, "beta", fmt.Sprintf(sharedDiskDirective, "Beta", "beta"))
			writeRule(t, dir, "disk", "# Storage quotas\n\n**Do:** keep quota alarms at eighty percent\n\nUnrelated prose that must survive.\n")
			before := snapshotRulesDir(t, dir)

			cmd := newRulesCompactTestCmd(t, append([]string{"--path", dir, "--force"}, args...)...)
			var err error
			stderr := captureStderr(t, func() {
				captureStdoutIgnoringError(t, func() { err = runRulesCompact(cmd, nil) })
			})

			if err == nil {
				t.Fatal("expected compact to fail rather than overwrite disk.md")
			}
			if !strings.Contains(stderr, "disk.md already exists") {
				t.Errorf("expected the refusal to name disk.md, got: %s", stderr)
			}
			assertRulesDirUnchanged(t, dir, before)
		})
	}
}

// A source that cannot be read cannot be backed up, so it must stop the
// merge before any deletion: a missing file used to surface as a failed
// delete after the other sources were already gone.
func TestWriteCompactedRule_MissingSourceFailsAtBackup(t *testing.T) {
	dir := tempRulesDir(t)
	present := writeRule(t, dir, "alpha", "# Alpha\n")
	missing := filepath.Join(dir, "gone.md")
	sources := []RuleFile{{Path: present}, {Path: missing}}
	reports := newCompactSourceReports(sources)

	composite := filepath.Join(dir, "composite.md")
	written, err := writeCompactedRule(composite, "# Composite\n", sources, reports)
	if err == nil || !strings.Contains(err.Error(), "could not back up") || !strings.Contains(err.Error(), "gone.md") {
		t.Fatalf("expected the failed backup to name gone.md, got: %v", err)
	}
	if !written {
		t.Error("expected written=true: the composite was created before the backup failed")
	}
	if _, statErr := os.Stat(composite); statErr != nil {
		t.Errorf("expected the composite to survive a failed backup: %v", statErr)
	}
	if _, statErr := os.Stat(present); statErr != nil {
		t.Errorf("no source may be deleted before every backup succeeded; %s must survive: %v", present, statErr)
	}
	if _, statErr := os.Stat(present + ".backup"); statErr != nil {
		t.Errorf("completed backups must survive a later failure: %v", statErr)
	}
	if !reports[0].BackedUp || reports[1].BackedUp {
		t.Errorf("reports must reflect the partial state: %+v", reports)
	}
}

// captureStdoutIgnoringError swallows stdout for a call whose error the test
// inspects itself (captureStdout fails the test on any error).
func captureStdoutIgnoringError(t *testing.T, fn func()) {
	t.Helper()
	old := os.Stdout
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open devnull: %v", err)
	}
	defer devnull.Close()
	os.Stdout = devnull
	defer func() { os.Stdout = old }()
	fn()
}

// captureStdoutWithError captures stdout for a call that may fail, returning
// both (captureStdout fails the test on any error).
func captureStdoutWithError(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	defer f.Close()
	stdioMutex.Lock()
	old := os.Stdout
	os.Stdout = f
	runErr := fn()
	os.Stdout = old
	stdioMutex.Unlock()
	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	return string(data), runErr
}

func runRulesCompactJSON(t *testing.T, args ...string) (map[string]interface{}, error) {
	t.Helper()
	prevJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = prevJSON })

	cmd := newRulesCompactTestCmd(t, args...)
	out, err := captureStdoutWithError(t, func() error { return runRulesCompact(cmd, nil) })
	var payload map[string]interface{}
	if jsonErr := json.Unmarshal([]byte(out), &payload); jsonErr != nil {
		t.Fatalf("stdout was not a JSON object (%v): %s", jsonErr, out)
	}
	return payload, err
}

func TestRulesCompact_GroupJSONForceApplies(t *testing.T) {
	dir := tempRulesDir(t)
	writeRule(t, dir, "alpha", fmt.Sprintf(sharedDiskDirective, "Alpha", "alpha"))
	writeRule(t, dir, "beta", fmt.Sprintf(sharedDiskDirective, "Beta", "beta"))

	payload, err := runRulesCompactJSON(t, "--path", dir, "--group", "alpha,beta", "--force")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if payload["applied"] != true {
		t.Errorf("expected applied=true, got: %v", payload)
	}
	if after := snapshotRulesDir(t, dir); len(after) != 3 {
		t.Errorf("want the composite and both backups left, got %v", ruleFileNames(after))
	}
}

// When the composite is written but a source delete fails, the --group JSON
// result must still say the composite was applied, as the --auto results do.
// Naming alpha twice makes the second delete of alpha.md fail.
func TestRulesCompact_GroupJSONReportsPartialApply(t *testing.T) {
	dir := tempRulesDir(t)
	writeRule(t, dir, "alpha", fmt.Sprintf(sharedDiskDirective, "Alpha", "alpha"))
	writeRule(t, dir, "beta", fmt.Sprintf(sharedDiskDirective, "Beta", "beta"))

	payload, err := runRulesCompactJSON(t, "--path", dir, "--group", "alpha,beta,alpha", "--force")

	if err == nil {
		t.Fatal("expected an error for the failed delete")
	}
	if payload["applied"] != true {
		t.Errorf("expected applied=true after the composite was written, got: %v", payload)
	}
	if msg, _ := payload["error"].(string); !strings.Contains(msg, "could not delete 1 source file") {
		t.Errorf("expected the failed delete in the JSON error, got: %v", payload)
	}
	after := snapshotRulesDir(t, dir)
	if len(after) != 3 {
		t.Errorf("want the composite and both distinct-path backups left, got %v", ruleFileNames(after))
	}
}

// TestRulesCompactAuto_PreservesSourceProse reproduces GH#5719: `bd rules
// compact --auto` must not destroy rule prose. After the command, every
// source file's non-directive text must still exist somewhere under the
// rules directory (kept source, backup, or merged composite).
func TestRulesCompactAuto_PreservesSourceProse(t *testing.T) {
	dir := tempRulesDir(t)
	writeRule(t, dir, "alpha", "# Alpha\n\n**Do:** validate input before writing to disk\n\n## Rationale\nThis prevents corruption when the writer crashes mid-flush.\n\n## Exceptions\nNone.\n")
	writeRule(t, dir, "beta", "# Beta\n\n**Do:** validate input before writing to disk\n\n## Rationale\nSecond file, distinct prose that must survive.\n")

	if err := rulesCompactCmd.Flags().Set("path", dir); err != nil {
		t.Fatal(err)
	}
	if err := rulesCompactCmd.Flags().Set("auto", "true"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = rulesCompactCmd.Flags().Set("path", ".claude/rules/")
		_ = rulesCompactCmd.Flags().Set("auto", "false")
	})
	if err := runRulesCompact(rulesCompactCmd, nil); err != nil {
		t.Fatalf("compact --auto: %v", err)
	}

	var all strings.Builder
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			b, _ := os.ReadFile(p)
			all.Write(b)
		}
		return nil
	})
	for _, prose := range []string{
		"This prevents corruption when the writer crashes mid-flush.",
		"Second file, distinct prose that must survive.",
	} {
		if !strings.Contains(all.String(), prose) {
			t.Errorf("prose %q lost after compact --auto; rules dir now holds:\n%s", prose, all.String())
		}
	}
}

// runRulesCompactJSONValue runs compact with --json and decodes the payload,
// array or object, so tests can assert on either output shape.
func runRulesCompactJSONValue(t *testing.T, args ...string) (interface{}, error) {
	t.Helper()
	prevJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = prevJSON })

	cmd := newRulesCompactTestCmd(t, args...)
	out, err := captureStdoutWithError(t, func() error { return runRulesCompact(cmd, nil) })
	var payload interface{}
	if jsonErr := json.Unmarshal([]byte(out), &payload); jsonErr != nil {
		t.Fatalf("stdout was not valid JSON (%v): %s", jsonErr, out)
	}
	return payload, err
}

// Without --force (or with --force --dry-run) compact previews in every
// mode — text, JSON, piped stdout — and writes nothing. The tests capture
// stdout through a pipe (never a TTY), so a prompt would hang them, not
// bypass them: passing proves no prompt and no stdin read.
func TestRulesCompact_PreviewJSONAppliedFalse(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"auto json", []string{"--auto"}},
		{"group json", []string{"--group", "alpha,beta"}},
		{"auto dry-run force json", []string{"--auto", "--dry-run", "--force"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := tempRulesDir(t)
			writeRule(t, dir, "alpha", fmt.Sprintf(sharedDiskDirective, "Alpha", "alpha"))
			writeRule(t, dir, "beta", fmt.Sprintf(sharedDiskDirective, "Beta", "beta"))
			before := snapshotRulesDir(t, dir)

			payload, err := runRulesCompactJSONValue(t, append([]string{"--path", dir}, tc.args...)...)
			if err != nil {
				t.Fatalf("preview must not fail: %v", err)
			}

			var objects []map[string]interface{}
			switch v := payload.(type) {
			case map[string]interface{}:
				objects = []map[string]interface{}{v}
			case []interface{}:
				for _, item := range v {
					m, ok := item.(map[string]interface{})
					if !ok {
						t.Fatalf("results item is not an object: %v", item)
					}
					objects = append(objects, m)
				}
			default:
				t.Fatalf("unexpected JSON payload: %v", payload)
			}
			if len(objects) == 0 {
				t.Fatal("expected at least one result object")
			}
			for _, m := range objects {
				if m["applied"] != false {
					t.Errorf("preview reported applied=%v, want false: %v", m["applied"], m)
				}
			}

			assertRulesDirUnchanged(t, dir, before)
		})
	}
}

// The GH#5719 fixture: two rules sharing a directive, each with distinct
// prose that a composite does not carry.
const (
	issue5719Alpha = "# Alpha\n\n**Do:** validate input before writing to disk\n\n## Rationale\nThis prevents corruption when the writer crashes mid-flush.\n\n## Exceptions\nNone.\n"
	issue5719Beta  = "# Beta\n\n**Do:** validate input before writing to disk\n\n## Rationale\nSecond file, distinct prose that must survive.\n"
)

// TestRulesCompact_Issue5719KeepsBackups covers GH#5719: a forced merge
// keeps the directives in the composite, preserves an exact byte-for-byte
// copy of every source as <source>.backup, removes the sources, and leaves
// the audit counting one rule — in all four modes.
func TestRulesCompact_Issue5719KeepsBackups(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		json bool
	}{
		{"auto text", []string{"--auto", "--force"}, false},
		{"auto json", []string{"--auto", "--force"}, true},
		{"group text", []string{"--group", "alpha,beta", "--force"}, false},
		{"group json", []string{"--group", "alpha,beta", "--force"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := tempRulesDir(t)
			writeRule(t, dir, "alpha", issue5719Alpha)
			writeRule(t, dir, "beta", issue5719Beta)
			args := append([]string{"--path", dir}, tc.args...)

			if tc.json {
				payload, err := runRulesCompactJSONValue(t, args...)
				if err != nil {
					t.Fatalf("forced apply: %v", err)
				}
				var objects []map[string]interface{}
				switch v := payload.(type) {
				case map[string]interface{}:
					objects = []map[string]interface{}{v}
				case []interface{}:
					for _, item := range v {
						m, ok := item.(map[string]interface{})
						if !ok {
							t.Fatalf("results item is not an object: %v", item)
						}
						objects = append(objects, m)
					}
				default:
					t.Fatalf("unexpected JSON payload: %v", payload)
				}
				if len(objects) != 1 {
					t.Fatalf("expected exactly one group result, got %d: %v", len(objects), payload)
				}
				if objects[0]["applied"] != true {
					t.Errorf("expected applied=true, got: %v", objects[0])
				}
				srcs, ok := objects[0]["sources"].([]interface{})
				if !ok || len(srcs) != 2 {
					t.Fatalf("expected sources with 2 entries, got: %v", objects[0]["sources"])
				}
				for _, s := range srcs {
					m, ok := s.(map[string]interface{})
					if !ok {
						t.Fatalf("sources entry is not an object: %v", s)
					}
					if m["backed_up"] != true {
						t.Errorf("expected backed_up=true, got: %v", m)
					}
					backup, _ := m["backup"].(string)
					if backup == "" {
						t.Errorf("expected a named backup path, got: %v", m)
					}
					if _, ok := m["file"].(string); !ok {
						t.Errorf("expected a file name, got: %v", m)
					}
				}
			} else {
				cmd := newRulesCompactTestCmd(t, args...)
				out := captureStdout(t, func() error { return runRulesCompact(cmd, nil) })
				for _, want := range []string{"backups: alpha.md.backup, beta.md.backup", "validate input before writing to disk"} {
					if !strings.Contains(out, want) {
						t.Errorf("apply output missing %q:\n%s", want, out)
					}
				}
			}

			for name, want := range map[string]string{"alpha.md.backup": issue5719Alpha, "beta.md.backup": issue5719Beta} {
				got, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil {
					t.Fatalf("read %s: %v", name, err)
				}
				if string(got) != want {
					t.Errorf("%s = %q, want the exact original bytes %q", name, got, want)
				}
			}
			composite, err := os.ReadFile(filepath.Join(dir, "disk.md"))
			if err != nil {
				t.Fatalf("read composite: %v", err)
			}
			if !strings.Contains(string(composite), "**Do:** validate input before writing to disk") {
				t.Errorf("composite lost the directive:\n%s", composite)
			}
			if !strings.Contains(string(composite), "Source rules: alpha.md, beta.md") {
				t.Errorf("composite lost the source attribution:\n%s", composite)
			}
			for _, name := range []string{"alpha.md", "beta.md"} {
				if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
					t.Errorf("%s should be removed after the merge", name)
				}
			}
			audit, err := RunAudit(dir, 0.6)
			if err != nil {
				t.Fatalf("audit after merge: %v", err)
			}
			if audit.TotalRules != 1 {
				t.Errorf("audit counts %d rules after the merge, want 1 (the .backup copies must not count)", audit.TotalRules)
			}
		})
	}
}

// refusesCompactResult runs compact expecting a refusal, returning the
// captured stderr and the command error, so subtests can share the setup.
func refusesCompactResult(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newRulesCompactTestCmd(t, args...)
	var err error
	stderr := captureStderr(t, func() {
		captureStdoutIgnoringError(t, func() { err = runRulesCompact(cmd, nil) })
	})
	return stderr, err
}

// An existing .backup destination refuses the group, in preview and apply,
// because applying would have to overwrite a recovery copy that is not this
// run's to replace.
func TestRulesCompact_RefusesExistingBackupDestination(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"preview auto", []string{"--auto"}},
		{"apply auto", []string{"--auto", "--force"}},
		{"preview group", []string{"--group", "alpha,beta"}},
		{"apply group", []string{"--group", "alpha,beta", "--force"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := tempRulesDir(t)
			writeRule(t, dir, "alpha", fmt.Sprintf(sharedDiskDirective, "Alpha", "alpha"))
			writeRule(t, dir, "beta", fmt.Sprintf(sharedDiskDirective, "Beta", "beta"))
			const backupContent = "precious earlier recovery copy"
			if err := os.WriteFile(filepath.Join(dir, "alpha.md.backup"), []byte(backupContent), 0o600); err != nil {
				t.Fatal(err)
			}
			before := snapshotRulesDir(t, dir)

			stderr, err := refusesCompactResult(t, append([]string{"--path", dir}, tc.args...)...)

			if err == nil {
				t.Fatal("expected compact to fail rather than touch alpha.md.backup")
			}
			if !strings.Contains(stderr, "alpha.md.backup already exists") && !strings.Contains(stderr, filepath.Join(dir, "alpha.md.backup")+" already exists") {
				t.Errorf("expected the refusal to name alpha.md.backup, got: %s", stderr)
			}
			assertRulesDirUnchanged(t, dir, before)
		})
	}
}

// A JSON refusal must surface the conflict and exit nonzero without
// applying anything.
func TestRulesCompact_RefusesExistingBackupDestinationJSON(t *testing.T) {
	dir := tempRulesDir(t)
	writeRule(t, dir, "alpha", fmt.Sprintf(sharedDiskDirective, "Alpha", "alpha"))
	writeRule(t, dir, "beta", fmt.Sprintf(sharedDiskDirective, "Beta", "beta"))
	if err := os.WriteFile(filepath.Join(dir, "alpha.md.backup"), []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := snapshotRulesDir(t, dir)

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"auto", []string{"--auto", "--force"}},
		{"group", []string{"--group", "alpha,beta", "--force"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var payload interface{}
			var err error
			if tc.name == "auto" {
				payload, err = runRulesCompactJSONValue(t, append([]string{"--path", dir}, tc.args...)...)
			} else {
				payload, err = runRulesCompactJSONValue(t, append([]string{"--path", dir}, tc.args...)...)
			}
			if err == nil {
				t.Fatal("expected a nonzero exit for the refused group")
			}
			encoded, mErr := json.Marshal(payload)
			if mErr != nil {
				t.Fatal(mErr)
			}
			if !strings.Contains(string(encoded), "alpha.md.backup already exists") {
				t.Errorf("expected the JSON error to name alpha.md.backup, got: %s", encoded)
			}
		})
	}
	assertRulesDirUnchanged(t, dir, before)
}

// snapshotRulesDirNoFollow snapshots a directory that may hold symlinks,
// recording them by kind instead of following them (a dangling link cannot
// be read through).
func snapshotRulesDirNoFollow(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	files := make(map[string]string, len(entries))
	for _, e := range entries {
		if info, err := e.Info(); err == nil && info.Mode()&os.ModeSymlink != 0 {
			files[e.Name()] = "<symlink>"
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		files[e.Name()] = string(data)
	}
	return files
}

func assertRulesDirUnchangedNoFollow(t *testing.T, dir string, before map[string]string) {
	t.Helper()
	after := snapshotRulesDirNoFollow(t, dir)
	if len(after) != len(before) {
		t.Fatalf("rules dir changed: before %v, after %v", ruleFileNames(before), ruleFileNames(after))
	}
	for name, content := range before {
		if after[name] != content {
			t.Errorf("%s changed:\nbefore: %q\nafter:  %q", name, content, after[name])
		}
	}
}

// A destination that exists only as a dangling symlink still counts as
// taken: Lstat sees the link, and following it would replace the link.
func TestRulesCompact_RefusesDanglingSymlinkDestination(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks on windows needs developer mode")
	}
	for _, tc := range []struct {
		name   string
		link   string
		apply  bool
		labels [2]string
	}{
		{"output symlink preview", "disk.md", false, [2]string{"Alpha", "Beta"}},
		{"output symlink apply", "disk.md", true, [2]string{"Alpha", "Beta"}},
		{"backup symlink preview", "alpha.md.backup", false, [2]string{"Alpha", "Beta"}},
		{"backup symlink apply", "alpha.md.backup", true, [2]string{"Alpha", "Beta"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := tempRulesDir(t)
			writeRule(t, dir, "alpha", fmt.Sprintf(sharedDiskDirective, tc.labels[0], "alpha"))
			writeRule(t, dir, "beta", fmt.Sprintf(sharedDiskDirective, tc.labels[1], "beta"))
			if err := os.Symlink(filepath.Join(dir, "does-not-exist"), filepath.Join(dir, tc.link)); err != nil {
				t.Fatal(err)
			}
			before := snapshotRulesDirNoFollow(t, dir)

			args := []string{"--path", dir, "--group", "alpha,beta"}
			if tc.apply {
				args = append(args, "--force")
			}
			stderr, err := refusesCompactResult(t, args...)

			if err == nil {
				t.Fatal("expected compact to refuse the dangling symlink destination")
			}
			if !strings.Contains(stderr, tc.link+" already exists") {
				t.Errorf("expected the refusal to name %s, got: %s", tc.link, stderr)
			}
			assertRulesDirUnchangedNoFollow(t, dir, before)
		})
	}
}

// Two auto groups whose labels collapse onto the same output name: the
// first claims the name, the second is refused, and an independent group
// is still processed.
func TestRulesCompact_AutoRefusesDuplicateOutputLabels(t *testing.T) {
	dir := tempRulesDir(t)
	// Two 3-member chains whose dominant keyword is "disk" for both, so
	// both groups are labelled disk; the quota pair forms an independent
	// second group.
	writeRule(t, dir, "a1", "# A1\n\n**Do:** disk alpha\n")
	writeRule(t, dir, "a2", "# A2\n\n**Do:** disk alpha beta\n")
	writeRule(t, dir, "a3", "# A3\n\n**Do:** disk beta\n")
	writeRule(t, dir, "b1", "# B1\n\n**Do:** disk gamma\n")
	writeRule(t, dir, "b2", "# B2\n\n**Do:** disk gamma delta\n")
	writeRule(t, dir, "b3", "# B3\n\n**Do:** disk delta\n")
	writeRule(t, dir, "q1", "# Q1\n\n**Do:** quota\n")
	writeRule(t, dir, "q2", "# Q2\n\n**Do:** quota\n")

	// Preview: nothing is written, so only the in-run reservation can
	// catch the second disk group; the refusal exits nonzero.
	var previewErr error
	previewStderr := captureStderr(t, func() {
		captureStdoutIgnoringError(t, func() { previewErr = runRulesCompact(newRulesCompactTestCmd(t, "--path", dir, "--auto"), nil) })
	})
	if previewErr == nil {
		t.Fatal("expected the preview to exit nonzero for the refused duplicate group")
	}
	if !strings.Contains(previewStderr, "disk.md") {
		t.Errorf("expected the preview refusal to name disk.md, got: %s", previewStderr)
	}
	for _, name := range []string{"a1.md", "a2.md", "a3.md", "b1.md", "b2.md", "b3.md", "q1.md", "q2.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("preview must not write or delete anything; %s: %v", name, err)
		}
	}

	// Apply: the first disk group lands, the second is refused by the
	// composite now on disk, and the quota group still applies.
	var applyErr error
	stderr := captureStderr(t, func() {
		captureStdoutIgnoringError(t, func() { applyErr = runRulesCompact(newRulesCompactTestCmd(t, "--path", dir, "--auto", "--force"), nil) })
	})
	if applyErr == nil {
		t.Fatal("expected a nonzero exit for the refused duplicate group")
	}
	if !strings.Contains(stderr, "disk.md") {
		t.Errorf("expected the duplicate-label refusal to name disk.md, got: %s", stderr)
	}
	after := snapshotRulesDir(t, dir)
	if _, ok := after["disk.md"]; !ok {
		t.Errorf("expected the first disk group's composite, got %v", ruleFileNames(after))
	}
	if _, ok := after["quota.md"]; !ok {
		t.Errorf("expected the quota group to apply independently, got %v", ruleFileNames(after))
	}
	// Whichever disk group ran second keeps every source byte-for-byte.
	aGone, bGone := true, true
	for _, name := range []string{"a1.md", "a2.md", "a3.md"} {
		if _, ok := after[name]; ok {
			aGone = false
		}
	}
	for _, name := range []string{"b1.md", "b2.md", "b3.md"} {
		if _, ok := after[name]; ok {
			bGone = false
		}
	}
	if aGone == bGone {
		t.Errorf("exactly one disk group should have applied (a-gone=%v, b-gone=%v): %v", aGone, bGone, ruleFileNames(after))
	}
	survivors := map[string]string{"a1.md": "# A1\n\n**Do:** disk alpha\n", "b1.md": "# B1\n\n**Do:** disk gamma\n"}
	for name, want := range survivors {
		if got, ok := after[name]; ok && got != want {
			t.Errorf("%s changed: %q", name, got)
		}
	}
}

// A backup that appears between the preflight check and the apply must be
// refused by exclusive creation, without being altered: the composite and
// the completed backups stay, the sources survive, the existing file keeps
// its bytes.
func TestWriteCompactedRule_NeverAltersExistingBackup(t *testing.T) {
	dir := tempRulesDir(t)
	alpha := writeRule(t, dir, "alpha", "# Alpha\n")
	beta := writeRule(t, dir, "beta", "# Beta\n")
	const existing = "earlier recovery copy, not this run's"
	if err := os.WriteFile(filepath.Join(dir, "beta.md.backup"), []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	sources := []RuleFile{{Path: alpha}, {Path: beta}}
	reports := newCompactSourceReports(sources)

	composite := filepath.Join(dir, "composite.md")
	written, err := writeCompactedRule(composite, "# Composite\n", sources, reports)

	if err == nil || !strings.Contains(err.Error(), "beta.md.backup already exists") {
		t.Fatalf("expected the refusal to name beta.md.backup, got: %v", err)
	}
	if !written {
		t.Error("expected written=true: the composite is created before the backups")
	}
	if got, rErr := os.ReadFile(filepath.Join(dir, "beta.md.backup")); rErr != nil || string(got) != existing {
		t.Errorf("existing backup must keep its bytes, got %q (%v)", got, rErr)
	}
	for _, path := range []string{alpha, beta, alpha + ".backup", composite} {
		if _, statErr := os.Stat(path); statErr != nil {
			t.Errorf("%s must survive the refusal: %v", path, statErr)
		}
	}
	if !reports[0].BackedUp || reports[1].BackedUp {
		t.Errorf("reports must reflect the partial state: %+v", reports)
	}
}
