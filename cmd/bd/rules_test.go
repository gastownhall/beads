package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	if got := ruleFileNames(after); strings.Join(got, ",") != "alpha.md.backup,beta.md.backup,disk.md" {
		t.Fatalf("want the composite and two backups left, got %v", got)
	}
	if content := after["disk.md"]; !strings.Contains(content, "validate input before writing to disk") || !strings.Contains(content, "Source rules: alpha.md, beta.md") {
		t.Errorf("disk.md is not the expected composite: %q", content)
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

// A source that vanished before compact copied it fails the backup, so no
// source is deleted: the composite and the completed backups stay, and the
// result says the composite was written.
func TestWriteCompactedRule_ReportsFailedBackup(t *testing.T) {
	dir := tempRulesDir(t)
	present := writeRule(t, dir, "alpha", "# Alpha\n")
	missing := filepath.Join(dir, "gone.md")

	composite := filepath.Join(dir, "composite.md")
	done, err := writeCompactedRule(composite, "# Composite\n", []RuleFile{{Path: present}, {Path: missing}})
	if err == nil || !strings.Contains(err.Error(), "could not back up "+missing) || !strings.Contains(err.Error(), "no source file was deleted") {
		t.Fatalf("expected the failed backup to be reported, got: %v", err)
	}
	if !done.Written {
		t.Error("expected written=true: the composite was created before the backup failed")
	}
	if !done.BackedUp[present] || done.BackedUp[missing] || len(done.Deleted) != 0 {
		t.Errorf("backed up = %v, deleted = %v, want only %s backed up", done.BackedUp, done.Deleted, present)
	}
	if _, statErr := os.Stat(composite); statErr != nil {
		t.Errorf("expected the composite to survive a failed backup: %v", statErr)
	}
	if got, readErr := os.ReadFile(present + ".backup"); readErr != nil || string(got) != "# Alpha\n" {
		t.Errorf("expected the completed backup to survive, got %q, %v", got, readErr)
	}
	if _, statErr := os.Stat(present); statErr != nil {
		t.Errorf("expected %s to survive a failed backup: %v", present, statErr)
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
	if got := ruleFileNames(snapshotRulesDir(t, dir)); strings.Join(got, ",") != "alpha.md.backup,beta.md.backup,disk.md" {
		t.Errorf("want the composite and two backups left, got %v", got)
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
	if got := ruleFileNames(after); strings.Join(got, ",") != "alpha.md.backup,beta.md.backup,disk.md" {
		t.Errorf("want the composite and one backup per distinct source left, got %v", got)
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

// Previews never write, prompt or read stdin, in text or JSON, whether or
// not stdout is a terminal (the test's stdout is a pipe or file).
func TestRulesCompact_JSONPreviewsWithoutForce(t *testing.T) {
	for _, args := range [][]string{{"--auto"}, {"--auto", "--dry-run", "--force"}, {"--group", "alpha,beta"}, {"--group", "alpha,beta", "--dry-run", "--force"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			dir := tempRulesDir(t)
			writeRule(t, dir, "alpha", fmt.Sprintf(sharedDiskDirective, "Alpha", "alpha"))
			writeRule(t, dir, "beta", fmt.Sprintf(sharedDiskDirective, "Beta", "beta"))
			before := snapshotRulesDir(t, dir)

			out, err := runRulesCompactJSONRaw(t, append([]string{"--path", dir}, args...)...)
			if err != nil {
				t.Fatalf("preview failed: %v\n%s", err, out)
			}
			assertRulesDirUnchanged(t, dir, before)
			var results []map[string]interface{}
			if args[0] == "--auto" {
				if err := json.Unmarshal([]byte(out), &results); err != nil {
					t.Fatalf("auto JSON is not an array (%v): %s", err, out)
				}
			} else {
				var payload map[string]interface{}
				if err := json.Unmarshal([]byte(out), &payload); err != nil {
					t.Fatalf("group JSON is not an object (%v): %s", err, out)
				}
				if payload["dry_run"] != true {
					t.Errorf("expected dry_run=true, got: %v", payload)
				}
				results = append(results, payload)
			}
			if len(results) != 1 {
				t.Fatalf("want one group, got: %s", out)
			}
			if results[0]["applied"] != false {
				t.Errorf("expected applied=false, got: %v", results[0])
			}
		})
	}
}

// runRulesCompactJSONRaw runs compact with --json and returns raw stdout.
func runRulesCompactJSONRaw(t *testing.T, args ...string) (string, error) {
	t.Helper()
	prevJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = prevJSON })

	cmd := newRulesCompactTestCmd(t, args...)
	return captureStdoutWithError(t, func() error { return runRulesCompact(cmd, nil) })
}

// The GH#5719 fixture: each source carries prose the composite drops.
const (
	issue5719Alpha = "# Alpha\n\n**Do:** validate input before writing to disk\n\n## Rationale\nThis prevents corruption when the writer crashes mid-flush.\n\n## Exceptions\nNone.\n"
	issue5719Beta  = "# Beta\n\n**Do:** validate input before writing to disk\n\n## Rationale\nSecond file, distinct prose that must survive.\n"
)

// Forced compaction keeps every source's exact bytes in <source>.backup
// before deleting it, in all four apply modes.
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
			alpha := writeRule(t, dir, "alpha", issue5719Alpha)
			beta := writeRule(t, dir, "beta", issue5719Beta)
			args := append([]string{"--path", dir}, tc.args...)

			var sources []interface{}
			if tc.json {
				out, err := runRulesCompactJSONRaw(t, args...)
				if err != nil {
					t.Fatalf("compact failed: %v\n%s", err, out)
				}
				var result map[string]interface{}
				if tc.args[0] == "--auto" {
					var results []map[string]interface{}
					if err := json.Unmarshal([]byte(out), &results); err != nil || len(results) != 1 {
						t.Fatalf("want a one-group results array (%v): %s", err, out)
					}
					result = results[0]
				} else if err := json.Unmarshal([]byte(out), &result); err != nil {
					t.Fatalf("group JSON is not an object (%v): %s", err, out)
				}
				if result["applied"] != true {
					t.Errorf("expected applied=true, got: %v", result)
				}
				if _, hasErr := result["error"]; hasErr {
					t.Errorf("unexpected error in result: %v", result)
				}
				sources, _ = result["sources"].([]interface{})
				if len(sources) != 2 {
					t.Fatalf("want two sources reported, got: %v", result["sources"])
				}
			} else {
				cmd := newRulesCompactTestCmd(t, args...)
				out := captureStdout(t, func() error { return runRulesCompact(cmd, nil) })
				for _, want := range []string{"Merged → disk.md", "Backed up: alpha.md → alpha.md.backup, beta.md → beta.md.backup", "deleted 2 source files"} {
					if !strings.Contains(out, want) {
						t.Errorf("output missing %q:\n%s", want, out)
					}
				}
			}

			for i, src := range []struct{ path, content string }{{alpha, issue5719Alpha}, {beta, issue5719Beta}} {
				got, err := os.ReadFile(src.path + ".backup")
				if err != nil {
					t.Fatalf("backup of %s: %v", src.path, err)
				}
				if string(got) != src.content {
					t.Errorf("backup of %s = %q, want the original bytes %q", src.path, got, src.content)
				}
				if _, err := os.Lstat(src.path); !os.IsNotExist(err) {
					t.Errorf("expected %s to be deleted, got %v", src.path, err)
				}
				if sources != nil {
					s, _ := sources[i].(map[string]interface{})
					if s["file"] != src.path || s["backup"] != src.path+".backup" || s["backed_up"] != true {
						t.Errorf("source %d reported as %v", i, s)
					}
				}
			}
			composite, err := os.ReadFile(filepath.Join(dir, "disk.md"))
			if err != nil {
				t.Fatalf("composite: %v", err)
			}
			if !strings.Contains(string(composite), "**Do:** validate input before writing to disk") {
				t.Errorf("composite lost the directive: %q", composite)
			}
			if !strings.Contains(string(composite), "Source rules: alpha.md, beta.md") {
				t.Errorf("composite lost the source attribution: %q", composite)
			}
			audit, err := RunAudit(dir, 0.6)
			if err != nil {
				t.Fatalf("audit: %v", err)
			}
			if audit.TotalRules != 1 {
				t.Errorf("audit counts %d rules after compaction, want 1 (backups must not count)", audit.TotalRules)
			}
		})
	}
}

// runRulesCompactText runs compact in text mode and returns stdout, stderr
// and the error.
func runRulesCompactText(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := newRulesCompactTestCmd(t, args...)
	outFile, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	defer outFile.Close()
	errFile, err := os.CreateTemp(t.TempDir(), "stderr-*")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	defer errFile.Close()

	stdioMutex.Lock()
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outFile, errFile
	runErr := runRulesCompact(cmd, nil)
	os.Stdout, os.Stderr = oldOut, oldErr
	stdioMutex.Unlock()

	stdout, err := os.ReadFile(outFile.Name())
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	stderr, err := os.ReadFile(errFile.Name())
	if err != nil {
		t.Fatalf("read captured stderr: %v", err)
	}
	return string(stdout), string(stderr), runErr
}

// An existing output or backup path, dangling symlinks included, refuses its
// group in preview and apply alike: nothing of that group is written, backed
// up or deleted, and compact exits nonzero naming the path.
func TestRulesCompact_RefusesExistingDestinations(t *testing.T) {
	for _, dest := range []struct {
		name  string
		plant func(t *testing.T, dir string) string
	}{
		{"existing backup", func(t *testing.T, dir string) string {
			p := filepath.Join(dir, "beta.md.backup")
			if err := os.WriteFile(p, []byte("an older backup\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return p
		}},
		{"dangling backup symlink", func(t *testing.T, dir string) string {
			p := filepath.Join(dir, "alpha.md.backup")
			if err := os.Symlink(filepath.Join(dir, "nowhere"), p); err != nil {
				t.Fatal(err)
			}
			return p
		}},
		{"dangling output symlink", func(t *testing.T, dir string) string {
			p := filepath.Join(dir, "disk.md")
			if err := os.Symlink(filepath.Join(dir, "nowhere"), p); err != nil {
				t.Fatal(err)
			}
			return p
		}},
	} {
		for _, args := range [][]string{{"--auto"}, {"--auto", "--force"}, {"--group", "alpha,beta"}, {"--group", "alpha,beta", "--force"}} {
			t.Run(dest.name+" "+strings.Join(args, " "), func(t *testing.T) {
				dir := tempRulesDir(t)
				writeRule(t, dir, "alpha", issue5719Alpha)
				writeRule(t, dir, "beta", issue5719Beta)
				conflict := dest.plant(t, dir)
				before := snapshotDirLinks(t, dir)

				out, stderr, err := runRulesCompactText(t, append([]string{"--path", dir}, args...)...)
				if err == nil {
					t.Fatalf("expected compact to refuse the group:\n%s", out)
				}
				if !strings.Contains(stderr, conflict+" already exists") {
					t.Errorf("expected the refusal to name %s, got: %s", conflict, stderr)
				}
				if strings.Contains(out, "Preview merge") || strings.Contains(out, "Merged →") {
					t.Errorf("a refused group must not read as a preview or merge:\n%s", out)
				}
				if after := snapshotDirLinks(t, dir); fmt.Sprint(after) != fmt.Sprint(before) {
					t.Errorf("rules dir changed:\nbefore %v\nafter  %v", before, after)
				}
			})
		}
	}
}

// One refused auto group does not stop an independent one: the refused group
// stays as it was, the other previews or applies, and compact exits nonzero.
func TestRulesCompact_AutoRefusalSparesIndependentGroups(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		json bool
	}{
		{"preview", []string{"--auto"}, false},
		{"force", []string{"--auto", "--force"}, false},
		{"force json", []string{"--auto", "--force"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := tempRulesDir(t)
			writeRule(t, dir, "alpha", issue5719Alpha)
			writeRule(t, dir, "beta", issue5719Beta)
			writeRule(t, dir, "gamma", "# Gamma\n\n**Do:** rotate vault secrets weekly\n")
			writeRule(t, dir, "delta", "# Delta\n\n**Do:** rotate vault secrets weekly\n\nOn-call owns it.\n")
			if err := os.WriteFile(filepath.Join(dir, "alpha.md.backup"), []byte("older\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			before := snapshotRulesDir(t, dir)
			args := append([]string{"--path", dir}, tc.args...)

			var out string
			var err error
			if tc.json {
				out, err = runRulesCompactJSONRaw(t, args...)
				var results []map[string]interface{}
				if jerr := json.Unmarshal([]byte(out), &results); jerr != nil || len(results) != 2 {
					t.Fatalf("want two group results (%v): %s", jerr, out)
				}
				for _, r := range results {
					msg, _ := r["error"].(string)
					switch r["group"] {
					case "disk":
						if r["applied"] != false || !strings.Contains(msg, "alpha.md.backup already exists") {
							t.Errorf("disk group should be refused, got %v", r)
						}
					case "rotate":
						if r["applied"] != true || msg != "" {
							t.Errorf("rotate group should apply, got %v", r)
						}
					default:
						t.Errorf("unexpected group %v", r)
					}
				}
			} else {
				var stderr string
				out, stderr, err = runRulesCompactText(t, args...)
				if !strings.Contains(stderr, "alpha.md.backup already exists") {
					t.Errorf("expected the refusal to name alpha.md.backup, got: %s", stderr)
				}
				if !strings.Contains(out, "Refused merge → disk.md") {
					t.Errorf("expected the disk group labelled as refused:\n%s", out)
				}
			}
			if err == nil {
				t.Fatalf("expected a nonzero exit for the refused group:\n%s", out)
			}

			after := snapshotRulesDir(t, dir)
			for _, name := range []string{"alpha.md", "beta.md", "alpha.md.backup"} {
				if after[name] != before[name] {
					t.Errorf("refused group's %s changed: %q -> %q", name, before[name], after[name])
				}
			}
			if _, ok := after["disk.md"]; ok {
				t.Error("refused group wrote disk.md")
			}
			if _, ok := after["beta.md.backup"]; ok {
				t.Error("refused group backed up beta.md")
			}
			if len(tc.args) == 1 {
				if fmt.Sprint(ruleFileNames(after)) != fmt.Sprint(ruleFileNames(before)) {
					t.Errorf("preview wrote files: %v", ruleFileNames(after))
				}
				if !strings.Contains(out, "Preview merge → rotate.md") {
					t.Errorf("expected the rotate group previewed:\n%s", out)
				}
				return
			}
			if _, ok := after["rotate.md"]; !ok {
				t.Errorf("independent group was not applied: %v", ruleFileNames(after))
			}
			for _, name := range []string{"gamma", "delta"} {
				if after[name+".md.backup"] != before[name+".md"] {
					t.Errorf("%s.md.backup = %q, want %q", name, after[name+".md.backup"], before[name+".md"])
				}
				if _, ok := after[name+".md"]; ok {
					t.Errorf("%s.md should be deleted", name)
				}
			}
		})
	}
}

// Two auto groups that would write the same composite: the later one is
// refused in preview as well as apply, rather than overwriting or reading as
// a safe preview.
func TestRulesCompact_AutoRefusesRepeatedOutputName(t *testing.T) {
	for _, args := range [][]string{{"--auto"}, {"--auto", "--force"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			dir := tempRulesDir(t)
			writeRule(t, dir, "alpha", fmt.Sprintf(sharedDiskDirective, "Alpha", "alpha"))
			writeRule(t, dir, "beta", fmt.Sprintf(sharedDiskDirective, "Beta", "beta"))
			writeRule(t, dir, "gamma", "# Gamma\n\n**Do:** disk quota zone\n")
			writeRule(t, dir, "delta", "# Delta\n\n**Do:** disk quota zone\n")
			before := snapshotRulesDir(t, dir)

			out, stderr, err := runRulesCompactText(t, append([]string{"--path", dir}, args...)...)
			if err == nil {
				t.Fatalf("expected a nonzero exit for the repeated composite name:\n%s", out)
			}
			if strings.Count(out, "Refused merge → disk.md") != 1 {
				t.Errorf("expected exactly one group refused:\n%s", out)
			}
			if !strings.Contains(stderr, "disk.md is also the composite of another group") {
				t.Errorf("expected the refusal to name disk.md, got: %s", stderr)
			}
			after := snapshotRulesDir(t, dir)
			// Exactly one pair is untouched; with --force the other is merged.
			untouched := 0
			for _, pair := range [][2]string{{"alpha.md", "beta.md"}, {"gamma.md", "delta.md"}} {
				if after[pair[0]] == before[pair[0]] && after[pair[1]] == before[pair[1]] {
					untouched++
				}
			}
			want := 1
			if len(args) == 1 {
				want = 2
			}
			if untouched != want {
				t.Errorf("%d groups untouched, want %d: %v", untouched, want, ruleFileNames(after))
			}
		})
	}
}

// snapshotDirLinks returns name -> content, or the link target for a
// symlink, for every entry in dir.
func snapshotDirLinks(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	files := make(map[string]string, len(entries))
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if e.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(p)
			if err != nil {
				t.Fatalf("readlink %s: %v", p, err)
			}
			files[e.Name()] = "-> " + target
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		files[e.Name()] = string(data)
	}
	return files
}

// A backup path that appears after the destination check still fails the
// exclusive create: that file is left as it was, the backups already made
// stay, and no source is deleted.
func TestWriteCompactedRule_BackupAppearingAfterCheckIsKept(t *testing.T) {
	dir := tempRulesDir(t)
	alpha := writeRule(t, dir, "alpha", issue5719Alpha)
	beta := writeRule(t, dir, "beta", issue5719Beta)
	if err := os.WriteFile(beta+".backup", []byte("an older backup\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	composite := filepath.Join(dir, "disk.md")

	done, err := writeCompactedRule(composite, "# Disk\n", []RuleFile{{Path: alpha}, {Path: beta}})
	if err == nil || !strings.Contains(err.Error(), beta+".backup already exists") || !strings.Contains(err.Error(), "no source file was deleted") {
		t.Fatalf("expected the refused backup to be reported, got: %v", err)
	}
	if !done.Written || !done.BackedUp[alpha] || done.BackedUp[beta] || len(done.Deleted) != 0 {
		t.Errorf("reported %+v, want written, only alpha backed up, nothing deleted", done)
	}
	want := map[string]string{
		"alpha.md":        issue5719Alpha,
		"alpha.md.backup": issue5719Alpha,
		"beta.md":         issue5719Beta,
		"beta.md.backup":  "an older backup\n",
		"disk.md":         "# Disk\n",
	}
	if got := snapshotRulesDir(t, dir); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("rules dir = %v, want %v", got, want)
	}
}

// failRemovingRule makes removeRuleSource fail for paths named name.
func failRemovingRule(t *testing.T, name string) {
	t.Helper()
	prev := removeRuleSource
	removeRuleSource = func(path string) error {
		if filepath.Base(path) == name {
			return &os.PathError{Op: "remove", Path: path, Err: os.ErrPermission}
		}
		return prev(path)
	}
	t.Cleanup(func() { removeRuleSource = prev })
}

// A delete that fails after every backup completed is reported with its path
// and an accurate partial state: the composite and all backups stay, the
// other sources are deleted, and compact exits nonzero.
func TestRulesCompact_ReportsFailedDelete(t *testing.T) {
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
			alpha := writeRule(t, dir, "alpha", issue5719Alpha)
			beta := writeRule(t, dir, "beta", issue5719Beta)
			failRemovingRule(t, "beta.md")
			args := append([]string{"--path", dir}, tc.args...)

			if tc.json {
				out, err := runRulesCompactJSONRaw(t, args...)
				if err == nil {
					t.Fatalf("expected a nonzero exit for the failed delete:\n%s", out)
				}
				var result map[string]interface{}
				if tc.args[0] == "--auto" {
					var results []map[string]interface{}
					if jerr := json.Unmarshal([]byte(out), &results); jerr != nil || len(results) != 1 {
						t.Fatalf("want a one-group results array (%v): %s", jerr, out)
					}
					result = results[0]
				} else if jerr := json.Unmarshal([]byte(out), &result); jerr != nil {
					t.Fatalf("group JSON is not an object (%v): %s", jerr, out)
				}
				if msg, _ := result["error"].(string); result["applied"] != true || !strings.Contains(msg, "could not delete 1 source file(s): remove "+beta) {
					t.Errorf("want applied=true and an error naming %s, got %v", beta, result)
				}
				sources, _ := result["sources"].([]interface{})
				if len(sources) != 2 {
					t.Fatalf("want two sources, got %v", result["sources"])
				}
				for i, want := range []struct {
					file    string
					deleted bool
				}{{alpha, true}, {beta, false}} {
					s, _ := sources[i].(map[string]interface{})
					if s["file"] != want.file || s["backed_up"] != true || s["deleted"] != want.deleted {
						t.Errorf("source %d = %v, want %s backed up, deleted=%v", i, s, want.file, want.deleted)
					}
				}
			} else {
				out, stderr, err := runRulesCompactText(t, args...)
				if err == nil {
					t.Fatalf("expected a nonzero exit for the failed delete:\n%s", out)
				}
				if !strings.Contains(stderr, "remove "+beta) {
					t.Errorf("expected the error to name %s, got: %s", beta, stderr)
				}
				for _, want := range []string{
					"Backed up: alpha.md → alpha.md.backup, beta.md → beta.md.backup",
					"Created disk.md and deleted alpha.md, but could not delete beta.md (see the error); every backup is kept",
				} {
					if !strings.Contains(out, want) {
						t.Errorf("output missing %q:\n%s", want, out)
					}
				}
			}

			want := map[string]string{
				"alpha.md.backup": issue5719Alpha,
				"beta.md":         issue5719Beta,
				"beta.md.backup":  issue5719Beta,
			}
			got := snapshotRulesDir(t, dir)
			composite := got["disk.md"]
			delete(got, "disk.md")
			if !strings.Contains(composite, "validate input before writing to disk") {
				t.Errorf("composite missing or wrong: %q", composite)
			}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("rules dir = %v, want %v plus disk.md", got, want)
			}
		})
	}
}

// After a failed backup the text says no source was deleted, not that some
// deletes failed.
func TestPrintCompactPreview_FailedBackupDeletesNothing(t *testing.T) {
	out := captureStdout(t, func() error {
		printCompactPreview("disk.md", "# Disk\n", []compactSource{
			{File: "/r/alpha.md", Backup: "/r/alpha.md.backup", BackedUp: true},
			{File: "/r/beta.md", Backup: "/r/beta.md.backup"},
		}, true, false)
		return nil
	})
	for _, want := range []string{"Backed up: alpha.md → alpha.md.backup\n", "Created disk.md, but no source file was deleted (see the error)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// A rule exercising every way the directive walker treats a line:
// frontmatter, a title, bullets and plain continuations, a heading and a
// blank line ending a block, an empty directive marker, trailing prose.
const notCarriedGamma = "---\npaths: src/**\n---\n# Gamma\n\n**Do:** rotate vault secrets weekly\n- audit the rotation log\ncontinue plain text\n## Why\nLeaked keys stay valid otherwise.\n\n**Don't:**\n* share secrets in chat\n\nClosing note.\n"

// Preview names, per source, every nonblank line the composite drops, never
// a line it carries, along with the backup path.
func TestRulesCompact_PreviewListsNotCarried(t *testing.T) {
	wantGamma := []string{"---", "paths: src/**", "---", "# Gamma", "## Why", "Leaked keys stay valid otherwise.", "Closing note."}
	wantAlpha := []string{"# Alpha", "## Rationale", "This prevents corruption when the writer crashes mid-flush.", "## Exceptions", "None."}
	wantBeta := []string{"# Beta", "## Rationale", "Second file, distinct prose that must survive."}

	t.Run("group json", func(t *testing.T) {
		dir := tempRulesDir(t)
		gamma := writeRule(t, dir, "gamma", notCarriedGamma)
		delta := writeRule(t, dir, "delta", "# Delta\n\n**Do:** rotate vault secrets weekly\n")
		out, err := runRulesCompactJSONRaw(t, "--path", dir, "--group", "gamma,delta")
		if err != nil {
			t.Fatalf("preview failed: %v\n%s", err, out)
		}
		var payload struct {
			Sources []struct {
				File       string   `json:"file"`
				Backup     string   `json:"backup"`
				NotCarried []string `json:"not_carried"`
			} `json:"sources"`
		}
		if err := json.Unmarshal([]byte(out), &payload); err != nil {
			t.Fatalf("bad JSON (%v): %s", err, out)
		}
		want := []struct {
			file       string
			notCarried []string
		}{{gamma, wantGamma}, {delta, []string{"# Delta"}}}
		if len(payload.Sources) != len(want) {
			t.Fatalf("sources = %+v", payload.Sources)
		}
		for i, w := range want {
			s := payload.Sources[i]
			if s.File != w.file || s.Backup != w.file+".backup" || fmt.Sprintf("%q", s.NotCarried) != fmt.Sprintf("%q", w.notCarried) {
				t.Errorf("source %d = %+v, want %s with not_carried %q", i, s, w.file, w.notCarried)
			}
		}
	})

	t.Run("auto json", func(t *testing.T) {
		dir := tempRulesDir(t)
		writeRule(t, dir, "alpha", issue5719Alpha)
		writeRule(t, dir, "beta", issue5719Beta)
		out, err := runRulesCompactJSONRaw(t, "--path", dir, "--auto")
		if err != nil {
			t.Fatalf("preview failed: %v\n%s", err, out)
		}
		var results []struct {
			Sources []struct {
				NotCarried []string `json:"not_carried"`
			} `json:"sources"`
		}
		if err := json.Unmarshal([]byte(out), &results); err != nil || len(results) != 1 || len(results[0].Sources) != 2 {
			t.Fatalf("want one group of two sources (%v): %s", err, out)
		}
		for i, want := range [][]string{wantAlpha, wantBeta} {
			if got := results[0].Sources[i].NotCarried; fmt.Sprintf("%q", got) != fmt.Sprintf("%q", want) {
				t.Errorf("source %d not_carried = %q, want %q", i, got, want)
			}
		}
	})

	// Text, in preview and after applying.
	for _, args := range [][]string{{"--auto"}, {"--auto", "--force"}} {
		t.Run("text "+strings.Join(args, " "), func(t *testing.T) {
			dir := tempRulesDir(t)
			writeRule(t, dir, "alpha", issue5719Alpha)
			writeRule(t, dir, "beta", issue5719Beta)
			out, _, err := runRulesCompactText(t, append([]string{"--path", dir}, args...)...)
			if err != nil {
				t.Fatalf("compact failed: %v\n%s", err, out)
			}
			i := strings.Index(out, "Not carried into the composite")
			if i < 0 {
				t.Fatalf("output does not disclose the dropped lines:\n%s", out)
			}
			section := out[i:]
			if j := strings.Index(section, "Backed up:"); j >= 0 {
				section = section[:j]
			}
			if j := strings.Index(section, "Would back up:"); j >= 0 {
				section = section[:j]
			}
			if !strings.Contains(section, "keeps only") {
				t.Errorf("disclosure does not say a composite keeps only directives:\n%s", section)
			}
			want := "  alpha.md:\n    " + strings.Join(wantAlpha, "\n    ") + "\n  beta.md:\n    " + strings.Join(wantBeta, "\n    ") + "\n"
			if !strings.Contains(section, want) {
				t.Errorf("disclosure missing %q:\n%s", want, section)
			}
			if strings.Contains(section, "validate input") {
				t.Errorf("a carried directive is listed as dropped:\n%s", section)
			}
			if !strings.Contains(out, "alpha.md → alpha.md.backup, beta.md → beta.md.backup") {
				t.Errorf("output does not name the backups:\n%s", out)
			}
		})
	}
}

// Edge lines around directives: frontmatter, headings, a bold non-directive
// note and a plain continuation. The parser's directive lines stay as they
// were; the dropped lines reach the JSON preview untouched.
func TestRulesCompact_NotCarriedEdgeLines(t *testing.T) {
	dir := tempRulesDir(t)
	content := "---\ntitle: frontmatter\n---\n# Heading\n\nIntro prose.\n\n**Do:**\n- bullet one\n- bullet two\ncontinuation of the do block\n\n**Don't:** never do this\n\n## Rationale\nBecause.\n\n**Note:** not a directive.\nTrailing prose.\n"
	writeRule(t, dir, "edges", content)

	rf, err := ParseRuleFile(filepath.Join(dir, "edges.md"))
	if err != nil {
		t.Fatal(err)
	}
	wantNotCarried := []string{
		"---",
		"title: frontmatter",
		"---",
		"# Heading",
		"Intro prose.",
		"## Rationale",
		"Because.",
		"**Note:** not a directive.",
		"Trailing prose.",
	}
	if fmt.Sprint(rf.NotCarried) != fmt.Sprint(wantNotCarried) {
		t.Errorf("NotCarried = %v, want %v", rf.NotCarried, wantNotCarried)
	}
	wantDo := []string{"bullet one", "bullet two", "continuation of the do block"}
	if fmt.Sprint(rf.DoLines) != fmt.Sprint(wantDo) {
		t.Errorf("DoLines = %v, want %v", rf.DoLines, wantDo)
	}
	if fmt.Sprint(rf.DontLines) != fmt.Sprint([]string{"never do this"}) {
		t.Errorf("DontLines = %v, want [never do this]", rf.DontLines)
	}

	// The same list reaches the JSON preview untouched.
	writeRule(t, dir, "twin", content)
	out, err := runRulesCompactJSONRaw(t, "--path", dir, "--group", "edges,twin")
	if err != nil {
		t.Fatalf("preview: %v\n%s", err, out)
	}
	var payload struct {
		Sources []struct {
			File       string   `json:"file"`
			NotCarried []string `json:"not_carried"`
		} `json:"sources"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("bad JSON (%v): %s", err, out)
	}
	edges := filepath.Join(dir, "edges.md")
	found := false
	for _, s := range payload.Sources {
		if s.File != edges {
			continue
		}
		found = true
		if fmt.Sprint(s.NotCarried) != fmt.Sprint(wantNotCarried) {
			t.Errorf("JSON not_carried = %v, want %v", s.NotCarried, wantNotCarried)
		}
	}
	if !found {
		t.Errorf("JSON preview has no source %s: %s", edges, out)
	}

	// Audit JSON never carries the field: it is compact's disclosure, not
	// audit data.
	audit, err := RunAudit(dir, 0.6)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(audit)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "not_carried") || strings.Contains(string(encoded), "NotCarried") {
		t.Errorf("audit JSON must not carry the disclosure field: %s", encoded)
	}
}

// failParsingRule makes compact's re-parse of an --auto group's rule fail
// for paths named name, as when the file changes after the audit read it.
func failParsingRule(t *testing.T, name string) {
	t.Helper()
	prev := parseCompactSource
	parseCompactSource = func(path string) (RuleFile, error) {
		if filepath.Base(path) == name {
			return RuleFile{}, fmt.Errorf("read rule file %s: %w", path, os.ErrPermission)
		}
		return prev(path)
	}
	t.Cleanup(func() { parseCompactSource = prev })
}

// A prose-only rule grouped by its body keywords contributes nothing to a
// composite, so compact never merges, backs up or deletes it.
const proseOnlyGamma = "# Validate\n\nValidate input writing disk.\n"

// --auto merges only the rules that contribute directives and parsed: the
// others stay untouched with a named warning, and a group left with fewer
// than two contributors is skipped whole, even under --force.
func TestRulesCompact_AutoSkipsNoncontributingRules(t *testing.T) {
	for _, tc := range []struct {
		name         string
		rules        map[string]string
		parseFails   string
		wantWarnings []string
		wantMerged   []string // sources merged into disk.md; nil = group skipped
	}{
		{
			name:         "prose-only member excluded",
			rules:        map[string]string{"alpha": issue5719Alpha, "beta": issue5719Beta, "gamma": proseOnlyGamma},
			wantWarnings: []string{"skipping gamma.md: it has no **Do:** or **Don't:** lines"},
			wantMerged:   []string{"alpha", "beta"},
		},
		{
			name:         "prose-only exclusion leaves one",
			rules:        map[string]string{"alpha": issue5719Alpha, "gamma": proseOnlyGamma},
			wantWarnings: []string{"skipping gamma.md: it has no **Do:** or **Don't:** lines", `skipping group "disk": fewer than two of its rules can be merged (alpha.md)`},
		},
		{
			name:         "parse failure leaves one",
			rules:        map[string]string{"alpha": issue5719Alpha, "beta": issue5719Beta},
			parseFails:   "beta.md",
			wantWarnings: []string{"skipping beta.md: read rule file", `skipping group "disk": fewer than two of its rules can be merged (alpha.md)`},
		},
		{
			name:         "both exclusions leave one",
			rules:        map[string]string{"alpha": issue5719Alpha, "beta": issue5719Beta, "gamma": proseOnlyGamma},
			parseFails:   "beta.md",
			wantWarnings: []string{"skipping beta.md: read rule file", "skipping gamma.md: it has no **Do:** or **Don't:** lines", `skipping group "disk": fewer than two of its rules can be merged (alpha.md)`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := tempRulesDir(t)
			for name, content := range tc.rules {
				writeRule(t, dir, name, content)
			}
			if tc.parseFails != "" {
				failParsingRule(t, tc.parseFails)
			}
			before := snapshotRulesDir(t, dir)

			out, stderr, err := runRulesCompactText(t, "--path", dir, "--auto", "--force")
			if err != nil {
				t.Fatalf("compact failed: %v\n%s\n%s", err, out, stderr)
			}
			for _, w := range tc.wantWarnings {
				if !strings.Contains(stderr, "Warning: "+w) {
					t.Errorf("stderr missing warning %q:\n%s", w, stderr)
				}
			}

			after := snapshotRulesDir(t, dir)
			if tc.wantMerged == nil {
				assertRulesDirUnchanged(t, dir, before)
				if strings.Contains(out, "Merged →") {
					t.Errorf("skipped group reads as merged:\n%s", out)
				}
				return
			}
			merged := make(map[string]bool)
			var names []string
			for _, name := range tc.wantMerged {
				merged[name+".md"] = true
				names = append(names, name+".md")
				if after[name+".md.backup"] != before[name+".md"] {
					t.Errorf("%s.md.backup = %q, want the original", name, after[name+".md.backup"])
				}
			}
			for name, content := range before {
				if merged[name] {
					if _, ok := after[name]; ok {
						t.Errorf("merged source %s was not deleted", name)
					}
					continue
				}
				if after[name] != content {
					t.Errorf("excluded %s changed: %q", name, after[name])
				}
				if _, ok := after[name+".backup"]; ok {
					t.Errorf("excluded %s was backed up", name)
				}
			}
			if want := "Source rules: " + strings.Join(names, ", "); !strings.Contains(after["disk.md"], want) {
				t.Errorf("composite attribution: want %q in %q", want, after["disk.md"])
			}
		})
	}
}

// Naming a rule without directives in --group refuses the whole group
// unchanged, in preview and apply, text and JSON.
func TestRulesCompact_GroupRefusesRuleWithoutDirectives(t *testing.T) {
	for _, args := range [][]string{{"--group", "alpha,gamma,beta"}, {"--group", "alpha,gamma,beta", "--force"}} {
		for _, asJSON := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s json=%v", strings.Join(args, " "), asJSON), func(t *testing.T) {
				dir := tempRulesDir(t)
				writeRule(t, dir, "alpha", issue5719Alpha)
				writeRule(t, dir, "beta", issue5719Beta)
				writeRule(t, dir, "gamma", proseOnlyGamma)
				before := snapshotRulesDir(t, dir)
				args := append([]string{"--path", dir}, args...)

				var msg string
				var err error
				if asJSON {
					msg, err = runRulesCompactJSONRaw(t, args...)
				} else {
					_, msg, err = runRulesCompactText(t, args...)
				}
				if err == nil {
					t.Fatal("expected compact to refuse the group")
				}
				if !strings.Contains(msg, "gamma.md has no **Do:** or **Don't:** lines") {
					t.Errorf("expected the refusal to name gamma.md, got: %s", msg)
				}
				assertRulesDirUnchanged(t, dir, before)
			})
		}
	}
}

// A symlinked source is backed up as an independent regular file holding the
// target's bytes; deleting the source removes only the link.
func TestRulesCompact_SymlinkSourceBackupIsRegularFile(t *testing.T) {
	dir := tempRulesDir(t)
	elsewhere := t.TempDir()
	target := filepath.Join(elsewhere, "alpha-real.md")
	if err := os.WriteFile(target, []byte(issue5719Alpha), 0o600); err != nil {
		t.Fatal(err)
	}
	alpha := filepath.Join(dir, "alpha.md")
	if err := os.Symlink(target, alpha); err != nil {
		t.Fatal(err)
	}
	writeRule(t, dir, "beta", issue5719Beta)

	cmd := newRulesCompactTestCmd(t, "--path", dir, "--group", "alpha,beta", "--force")
	captureStdout(t, func() error { return runRulesCompact(cmd, nil) })

	info, err := os.Lstat(alpha + ".backup")
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("backup of the symlinked source is not a regular file: %v, %v", info, err)
	}
	if got, _ := os.ReadFile(alpha + ".backup"); string(got) != issue5719Alpha {
		t.Errorf("backup = %q, want the target's bytes", got)
	}
	if _, err := os.Lstat(alpha); !os.IsNotExist(err) {
		t.Errorf("expected the source link removed, got %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != issue5719Alpha {
		t.Errorf("link target changed: %q", got)
	}
}
