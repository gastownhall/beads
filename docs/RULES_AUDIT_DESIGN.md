# `bd rules audit` / `bd rules compact` Design Doc

> **Status:** Ready | **Target:** Upstream PR to `steveyegge/beads`
> **Companion spec:** SESSION-OUTPUT-SPEC Items 2 & 6

---

## Problem Statement

Claude Code projects accumulate rules in `.claude/rules/*.md` over time. Each rule starts small and focused, but after a few months of active development, a real project can hit 40+ rule files with:

- **Contradictions** — Rule A says "always do X", Rule B says "never do X" (same scope, opposing directives)
- **Near-duplicates** — Three rules about agent efficiency that could be one file
- **Bloat** — 40 files, ~12K tokens of system prompt. Every Claude session loads all of them. Token cost scales linearly.
- **No auditing** — No tooling exists to detect any of the above. Discovery is manual.

Beads already tracks issues, specs, skills, and comments. Rules are the missing entity. `bd rules audit` fills this gap.

### Motivating Example

A real production project with 40+ rules had:
- 4 rules touching "agent discipline" (spawn, efficiency, token usage, verification) — all overlapping
- 2 rules with direct contradictions: one enforcing a strict blocking workflow, another allowing flexible parallel execution
- 8 rules that could merge into 3 composites, saving ~3K tokens per session
- Total rule token cost: ~12K tokens — reducible to ~7K with compaction

Nobody noticed until manual review. This should be automated.

---

## CLI Interface

### `bd rules audit`

Scan rules and report problems.

```
bd rules audit [--path .claude/rules/] [--json] [--threshold 0.6]

Flags:
  --path         Path to rules directory (default: .claude/rules/)
  --json         Structured JSON output for agent consumption
  --threshold    Jaccard similarity threshold for merge candidates (default: 0.6)
```

**Output (human-readable):**

```
Rules Audit — .claude/rules/
============================================================

Summary:
  Total rules:        42
  Token estimate:     ~11,800
  Contradictions:     2
  Merge candidates:   3 groups (12 rules → 4 files)

Contradictions:
┌─────────────────────────┬─────────────────────────┬───────────────────────────────┐
│ Rule A                  │ Rule B                  │ Tension                       │
├─────────────────────────┼─────────────────────────┼───────────────────────────────┤
│ blocker-first.md        │ parallel-workflow.md    │ "block until resolved" vs     │
│                         │                         │ "proceed in parallel"         │
├─────────────────────────┼─────────────────────────┼───────────────────────────────┤
│ verbose-logging.md      │ token-efficiency.md     │ "log all decisions" vs        │
│                         │                         │ "minimize output tokens"      │
└─────────────────────────┴─────────────────────────┴───────────────────────────────┘

Merge Candidates (similarity > 0.60):
  Group 1 — "agent discipline" (score: 0.78)
    → agent-spawn-discipline.md
    → agent-efficiency.md
    → agent-token-efficiency.md
    → agent-verification.md
    Suggested: merge into agent-discipline.md

  Group 2 — "context management" (score: 0.65)
    → context-first.md
    → context-loading.md
    Suggested: merge into context-management.md

Run `bd rules compact --auto` to apply suggested merges.
```

### `bd rules compact`

Merge related rules into composites.

```
bd rules compact --group <rule1> <rule2> ...   # Merge specific rules
bd rules compact --auto                         # Apply audit suggestions
bd rules compact --dry-run                      # Show diff without applying

Flags:
  --path         Path to rules directory (default: .claude/rules/)
  --group        List of rule filenames (without .md) to merge
  --auto         Use merge candidates from last audit
  --dry-run      Preview merged output without writing files
  --json         Structured JSON output
```

**Compact workflow:**

```
$ bd rules compact --group agent-spawn-discipline agent-efficiency agent-token-efficiency
Preview merge → agent-discipline.md:
────────────────────────────────────
# Agent Discipline
**Do:** Check for existing skill/tool before spawning. Reuse context. Minimize token output.
**Don't:** Spawn redundant agents. Repeat work across sessions. Log verbose reasoning.

Source rules: agent-spawn-discipline.md, agent-efficiency.md, agent-token-efficiency.md
────────────────────────────────────
Apply? [y/N]: y
✓ Created .claude/rules/agent-discipline.md
✓ Deleted 3 source files
```

---

## Algorithm

### Step 1: Parse Rules

Each `.md` file in the rules directory is parsed into a `RuleFile`:

1. Read file content
2. Extract the first `#` heading as the rule name (fallback: filename)
3. Extract **Do blocks**: lines matching `**Do:**` (with continuation lines)
4. Extract **Don't blocks**: lines matching `**Don't:**` (with continuation lines)
5. Extract all other content as `body` (for keyword extraction fallback)
6. Estimate token count: `len(content) / 4` (rough approximation)

### Step 2: Keyword Extraction

For each rule, extract a keyword set:

1. Tokenize Do/Don't lines into words (lowercase, strip punctuation)
2. Remove stop words (`the`, `a`, `is`, `to`, `for`, `and`, `or`, `in`, `of`, `it`, `that`, `this`, `with`, `be`, `not`, `do`, `don't`, `use`, `when`, `before`, `after`, `should`, `must`, `always`, `never`)
3. Keep remaining words as the keyword set
4. If Do/Don't blocks are empty, fall back to body keywords (same process)

No NLP libraries needed — simple tokenization is sufficient for `.claude/rules/` files which are short, imperative, and use consistent vocabulary.

### Step 3: Overlap Scoring (Merge Candidates)

For every pair of rules `(A, B)`:

```
jaccard(A, B) = |keywords(A) ∩ keywords(B)| / |keywords(A) ∪ keywords(B)|
```

If `jaccard(A, B) >= threshold` (default 0.6), they're a merge candidate.

**Grouping:** Build merge groups using single-linkage clustering:
- Start with all pairs above threshold
- If A overlaps B and B overlaps C, group {A, B, C}
- Label each group by the most frequent keyword

### Step 4: Contradiction Detection

Two rules contradict when they share scope but give opposing directives.

**Scope overlap:** `jaccard(A, B) >= 0.3` (lower than merge threshold — contradictions can exist between loosely related rules)

**Opposing directives check:**
1. Extract action verbs from Do blocks of rule A
2. Extract action verbs from Don't blocks of rule B
3. If the same verb appears in A's Do and B's Don't (or vice versa) on overlapping keyword scope → contradiction

**Antonym pairs** (hardcoded for MVP):
- `block` / `proceed`, `parallel`
- `verbose` / `minimize`, `concise`
- `always` / `never`
- `spawn` / `reuse`
- `wait` / `skip`
- `log` / `suppress`

If a Do directive from rule A uses a word, and a Do directive from rule B uses its antonym, with shared scope keywords → contradiction.

**Tension description:** Generated from the conflicting Do/Don't lines, truncated to ~60 chars.

### Step 5: Compaction (for `bd rules compact`)

Given a group of rules to merge:

1. Collect all Do blocks across source rules
2. Collect all Don't blocks across source rules
3. Deduplicate identical entries (exact string match after trimming)
4. Generate a merged heading from the group label
5. Output the composite rule in standard format:

```markdown
# {Group Label}
**Do:** {deduplicated do entries, newline-separated}
**Don't:** {deduplicated don't entries, newline-separated}
```

6. Show diff before applying
7. On confirmation: write merged file, delete source files

---

## Go Implementation Sketch

### File: `cmd/bd/rules.go`

```go
package main

// --- Types ---

// RuleFile represents a parsed .claude/rules/*.md file.
type RuleFile struct {
    Path     string   // absolute path
    Name     string   // filename without .md
    Title    string   // first # heading or filename
    DoLines  []string // extracted "Do:" directives
    DontLines []string // extracted "Don't:" directives
    Body     string   // full file content
    Keywords []string // extracted keywords (deduped, lowered)
    Tokens   int      // estimated token count
}

// ContradictionReport describes a tension between two rules.
type ContradictionReport struct {
    RuleA       string `json:"rule_a"`
    RuleB       string `json:"rule_b"`
    Tension     string `json:"tension"`
    DoLineA     string `json:"do_line_a"`
    DontLineB   string `json:"dont_line_b"`
    ScopeScore  float64 `json:"scope_score"`
}

// MergeCandidate represents a group of rules that could be combined.
type MergeCandidate struct {
    GroupLabel string   `json:"group_label"`
    Rules      []string `json:"rules"`
    Score      float64  `json:"score"` // average pairwise Jaccard
}

// AuditResult is the full output of `bd rules audit`.
type AuditResult struct {
    TotalRules      int                   `json:"total_rules"`
    TokenEstimate   int                   `json:"token_estimate"`
    Contradictions  []ContradictionReport `json:"contradictions"`
    MergeCandidates []MergeCandidate      `json:"merge_candidates"`
    Rules           []RuleFile            `json:"rules,omitempty"` // only in --json
}

// --- Key Functions ---

// ParseRuleFile reads a .md file and extracts structured rule data.
func ParseRuleFile(path string) (RuleFile, error)

// ExtractKeywords tokenizes Do/Don't lines, removes stop words.
func ExtractKeywords(lines []string) []string

// JaccardSimilarity computes keyword overlap between two rules.
func JaccardSimilarity(a, b []string) float64

// DetectContradictions finds opposing directives across rule pairs.
func DetectContradictions(rules []RuleFile, scopeThreshold float64) []ContradictionReport

// FindMergeCandidates groups rules by keyword overlap.
func FindMergeCandidates(rules []RuleFile, threshold float64) []MergeCandidate

// CompactRules merges a group of rules into a single composite file.
func CompactRules(rules []RuleFile, groupLabel string) (string, error)

// RunAudit is the top-level orchestrator for `bd rules audit`.
func RunAudit(rulesDir string, threshold float64) (*AuditResult, error)
```

### Cobra Commands

```go
var rulesCmd = &cobra.Command{
    Use:     "rules",
    Short:   "Audit and compact Claude rules",
    GroupID: GroupMaintenance,
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
    rulesAuditCmd.Flags().String("path", ".claude/rules/", "Path to rules directory")
    rulesAuditCmd.Flags().Bool("json", false, "JSON output")
    rulesAuditCmd.Flags().Float64("threshold", 0.6, "Jaccard similarity threshold")

    rulesCompactCmd.Flags().String("path", ".claude/rules/", "Path to rules directory")
    rulesCompactCmd.Flags().StringSlice("group", nil, "Rule names to merge")
    rulesCompactCmd.Flags().Bool("auto", false, "Apply audit suggestions")
    rulesCompactCmd.Flags().Bool("dry-run", false, "Preview without applying")
    rulesCompactCmd.Flags().Bool("json", false, "JSON output")

    rulesCmd.AddCommand(rulesAuditCmd)
    rulesCmd.AddCommand(rulesCompactCmd)
    rootCmd.AddCommand(rulesCmd)
}
```

### Patterns (matching existing codebase)

- **One file per command tree**: `rules.go` contains both `audit` and `compact` subcommands (like `spec.go` contains scan/cleanup/duplicates)
- **`--json` everywhere**: All output structs have JSON tags, `--json` flag switches rendering
- **GroupID**: Uses `GroupMaintenance` (same as `wobble`, `doctor`)
- **No daemon dependency**: Rules audit is purely local file analysis — no RPC, no SQLite
- **`tabwriter` for tables**: Human output uses `text/tabwriter` (same as `spec report`)

---

## Output Formats

### Human-Readable (default)

See the CLI Interface section above. Uses:
- `tabwriter` for the contradiction table
- Indented groups for merge candidates
- Color via `internal/ui` package (green for OK, yellow for warnings, red for contradictions)

### JSON (`--json`)

```json
{
  "total_rules": 42,
  "token_estimate": 11800,
  "contradictions": [
    {
      "rule_a": "blocker-first.md",
      "rule_b": "parallel-workflow.md",
      "tension": "\"block until resolved\" vs \"proceed in parallel\"",
      "do_line_a": "Block PR merges until all issues resolved",
      "dont_line_b": "Don't wait for blocking issues, proceed in parallel",
      "scope_score": 0.45
    }
  ],
  "merge_candidates": [
    {
      "group_label": "agent discipline",
      "rules": [
        "agent-spawn-discipline.md",
        "agent-efficiency.md",
        "agent-token-efficiency.md",
        "agent-verification.md"
      ],
      "score": 0.78
    }
  ]
}
```

---

## Test Plan

### Unit Tests (`cmd/bd/rules_test.go`)

| Test | What it verifies |
|------|-----------------|
| `TestParseRuleFile_Basic` | Extracts title, Do/Don't lines from well-formed rule |
| `TestParseRuleFile_NoDoBlocks` | Falls back to body keywords when no Do/Don't |
| `TestParseRuleFile_MultilineDo` | Handles continuation lines after `**Do:**` |
| `TestExtractKeywords` | Stop word removal, lowercasing, dedup |
| `TestJaccardSimilarity_Identical` | Returns 1.0 for same keyword sets |
| `TestJaccardSimilarity_Disjoint` | Returns 0.0 for no overlap |
| `TestJaccardSimilarity_Partial` | Returns correct score for partial overlap |
| `TestDetectContradictions_Direct` | Finds "Do X" vs "Don't X" across rules |
| `TestDetectContradictions_Antonym` | Finds "block" vs "proceed" antonym pair |
| `TestDetectContradictions_NoFalsePositive` | Unrelated rules don't trigger contradiction |
| `TestFindMergeCandidates_Grouping` | Clusters overlapping rules into groups |
| `TestFindMergeCandidates_Threshold` | Respects --threshold flag |
| `TestCompactRules_Dedup` | Removes identical Do lines from merged output |
| `TestCompactRules_PreservesOrder` | Merged output keeps stable ordering |
| `TestRunAudit_EmptyDir` | Handles directory with no .md files |
| `TestRunAudit_SingleRule` | No contradictions or merge candidates with 1 rule |
| `TestRunAudit_JSON` | JSON output matches expected schema |

### Edge Cases

- **Empty rules directory** — report 0 rules, no errors
- **Rule with only a title, no Do/Don't** — still appears in count, keywords from body
- **Binary/non-markdown files in rules dir** — skip gracefully
- **Symlinked rules** — follow symlinks (match Claude Code behavior)
- **Very long rules (>10K chars)** — keyword extraction still works, flag as bloated
- **UTF-8 with emoji** — handle gracefully in keyword extraction
- **Single rule** — no pairs to compare, clean report

### Integration Tests

- Create a temp directory with 5-6 synthetic rules (known contradictions and overlaps)
- Run full `RunAudit()` and verify the expected contradictions and merge groups
- Run `CompactRules()` and verify the merged file content
- Verify `--json` output parses as valid JSON

---

## Files to Create/Modify

| File | Action | Lines (est.) |
|------|--------|-------------|
| `cmd/bd/rules.go` | Create | ~450 |
| `cmd/bd/rules_test.go` | Create | ~350 |
| `docs/RULES_AUDIT.md` | Create (user docs) | ~80 |
| `CHANGELOG.md` | Update | +5 |

No new packages needed. No database changes. No daemon integration. Pure file analysis.

---

## Non-Goals (v1)

- **NLP/embeddings**: No semantic similarity — Jaccard on extracted keywords is sufficient for rule files
- **Auto-fix contradictions**: Only `compact` merges related rules. Resolving contradictions requires human judgment.
- **Cross-project rules**: Only scans one rules directory per invocation
- **Rule dependency tracking**: No DAG of rule relationships (future work)
- **MCP mode**: Standalone MCP wrapper is a separate project (SESSION-OUTPUT-SPEC Item 4)

---

## Open Questions

1. **Should `--fix` be a flag on `audit` or a separate `compact` command?** Current design: separate command. Reasoning: audit is read-only and safe to run anytime; compact modifies files and needs explicit intent.
2. **Threshold tuning**: 0.6 default is a guess. Needs calibration against real rule sets. Should we ship with 0.5 and let users tune up?
3. **Antonym list**: Hardcoded for MVP. Should this be configurable via a YAML file?
