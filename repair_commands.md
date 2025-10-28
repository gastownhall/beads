# Repair Commands & AI-Assisted Tooling

**Status:** Design Proposal
**Author:** AI Assistant
**Date:** 2025-10-28
**Context:** Reduce agent repair burden by providing specialized repair tools

## Executive Summary

Agents spend significant time repairing beads databases due to:
1. Git merge conflicts in JSONL
2. Duplicate issues from parallel work
3. Semantic inconsistencies (labeling, dependencies)
4. Orphaned references after deletions

**Solution:** Add dedicated repair commands that agents (and humans) can invoke instead of manually fixing these issues. Some commands use AI for semantic understanding, others are pure mechanical checks.

## Problem Analysis

### Current Repair Scenarios

Based on codebase analysis and commit history:

#### 1. Git Merge Conflicts (High Frequency)

**Scenario:**
```bash
# Feature branch creates bd-42
git checkout -b feature
bd create "Add authentication"  # Creates bd-42

# Meanwhile, main branch also creates bd-42
git checkout main
bd create "Fix logging"  # Also creates bd-42

# Merge creates conflict
git checkout feature
git merge main
```

**JSONL conflict:**
```json
<<<<<<< HEAD
{"id":"bd-42","title":"Add authentication",...}
=======
{"id":"bd-42","title":"Fix logging",...}
>>>>>>> main
```

**Current fix:** Agent manually parses conflict markers, remaps IDs, updates references

**Pain points:**
- Time-consuming (5-10 minutes per conflict)
- Error-prone (easy to miss references)
- Repetitive (same logic every time)

#### 2. Semantic Duplicates (Medium Frequency)

**Scenario:**
```bash
# Agent A creates issue
bd create "Fix memory leak in parser"  # bd-42

# Agent B creates similar issue (different session)
bd create "Parser memory leak needs fixing"  # bd-87

# Human notices: "These are the same issue!"
```

**Current fix:** Agent manually:
1. Reads both issues
2. Determines they're duplicates
3. Picks canonical one
4. Closes duplicate with reference
5. Moves comments/dependencies

**Pain points:**
- Requires reading full issue text
- Subjective judgment (are they really duplicates?)
- Manual reference updates

#### 3. Test Pollution (Low Frequency Now, High Impact)

**Scenario:**
```bash
# Test creates 1044 issues in production DB
go test ./internal/rpc/...  # Oops, no isolation

bd list
# Shows 1044 issues with titles like "test-issue-1", "benchmark-issue-42"
```

**Recent occurrence:** Commits 78e8cb9, d1d3fcd (Oct 2025)

**Current fix:** Agent manually:
1. Identifies test issues by pattern matching
2. Bulk closes with `bd close bd-1 bd-2 ... bd-1044`
3. Archives or deletes

**Pain points:**
- Hard to distinguish test vs. real issues
- Risk of deleting real issues
- No automated recovery

#### 4. Orphaned Dependencies (Medium Frequency)

**Scenario:**
```bash
bd create "Implement feature X"  # bd-42
bd create "Test feature X" --depends bd-42  # bd-43 depends on bd-42

bd delete bd-42  # User deletes parent

bd show bd-43
# Depends: bd-42 (orphaned - issue doesn't exist!)
```

**Current fix:** Agent manually updates dependencies

**Pain points:**
- Silent corruption (no warning on delete)
- Hard to find orphans (requires DB query)

## Proposed Commands

### 1. `bd resolve-conflicts` - Git Merge Conflict Resolver

**Purpose:** Automatically resolve JSONL merge conflicts

**Usage:**
```bash
# Detect conflicts
bd resolve-conflicts

# Auto-resolve with AI
bd resolve-conflicts --auto

# Manual conflict resolution
bd resolve-conflicts --interactive
```

**Implementation:**

```go
// cmd/bd/resolve_conflicts.go (new file)
package main

import (
    "bufio"
    "context"
    "fmt"
    "os"
    "strings"

    "github.com/steveyegge/beads/internal/types"
)

type ConflictBlock struct {
    HeadIssues []types.Issue
    BaseIssues []types.Issue
    LineStart  int
    LineEnd    int
}

func detectConflicts(jsonlPath string) ([]ConflictBlock, error) {
    file, err := os.Open(jsonlPath)
    if err != nil {
        return nil, err
    }
    defer file.Close()

    var conflicts []ConflictBlock
    var current *ConflictBlock
    inConflict := false
    inHead := false
    lineNum := 0

    scanner := bufio.NewScanner(file)
    for scanner.Scan() {
        line := scanner.Text()
        lineNum++

        switch {
        case strings.HasPrefix(line, "<<<<<<<"):
            // Start of conflict
            inConflict = true
            inHead = true
            current = &ConflictBlock{LineStart: lineNum}

        case strings.HasPrefix(line, "======="):
            // Switch from HEAD to base
            inHead = false

        case strings.HasPrefix(line, ">>>>>>>"):
            // End of conflict
            inConflict = false
            current.LineEnd = lineNum
            conflicts = append(conflicts, *current)
            current = nil

        case inConflict && inHead:
            // Parse issue in HEAD section
            issue, err := parseIssueLine(line)
            if err == nil {
                current.HeadIssues = append(current.HeadIssues, issue)
            }

        case inConflict && !inHead:
            // Parse issue in base section
            issue, err := parseIssueLine(line)
            if err == nil {
                current.BaseIssues = append(current.BaseIssues, issue)
            }
        }
    }

    if scanner.Err() != nil {
        return nil, scanner.Err()
    }

    return conflicts, nil
}

func resolveConflictsAuto(conflicts []ConflictBlock, useAI bool) ([]Resolution, error) {
    var resolutions []Resolution

    for _, conflict := range conflicts {
        if useAI {
            // Use AI to determine resolution
            resolution, err := resolveConflictWithAI(conflict)
            if err != nil {
                return nil, err
            }
            resolutions = append(resolutions, resolution)
        } else {
            // Mechanical resolution: remap duplicate IDs
            resolution := resolveConflictMechanical(conflict)
            resolutions = append(resolutions, resolution)
        }
    }

    return resolutions, nil
}

type Resolution struct {
    Action   string // "remap", "merge", "keep-head", "keep-base"
    OldID    string
    NewID    string
    Reason   string
    Merged   *types.Issue // If action="merge"
}

func resolveConflictMechanical(conflict ConflictBlock) Resolution {
    // Mechanical strategy: Keep HEAD, remap base to new IDs
    // This matches current auto-import collision resolution

    headIDs := make(map[string]bool)
    for _, issue := range conflict.HeadIssues {
        headIDs[issue.ID] = true
    }

    var resolutions []Resolution
    for _, issue := range conflict.BaseIssues {
        if headIDs[issue.ID] {
            // ID collision: remap base issue to next available ID
            newID := getNextAvailableID()
            resolutions = append(resolutions, Resolution{
                Action: "remap",
                OldID:  issue.ID,
                NewID:  newID,
                Reason: fmt.Sprintf("ID %s exists in both branches", issue.ID),
            })
        }
    }

    return resolutions[0] // Simplified for example
}

func resolveConflictWithAI(conflict ConflictBlock) (Resolution, error) {
    // Call AI to analyze conflict and suggest resolution

    prompt := fmt.Sprintf(`
You are resolving a git merge conflict in a beads issue tracker JSONL file.

HEAD issues (current branch):
%s

BASE issues (incoming branch):
%s

Analyze these conflicts and suggest ONE of:
1. "remap" - Issues are different, keep both but remap IDs
2. "merge" - Issues are similar, merge into one
3. "keep-head" - HEAD version is correct, discard BASE
4. "keep-base" - BASE version is correct, discard HEAD

Respond in JSON format:
{
    "action": "remap|merge|keep-head|keep-base",
    "reason": "explanation",
    "merged_issue": {...}  // Only if action=merge
}
`, formatIssues(conflict.HeadIssues), formatIssues(conflict.BaseIssues))

    // Call AI (via environment-configured API)
    response, err := callAIAPI(prompt)
    if err != nil {
        return Resolution{}, err
    }

    // Parse response
    var resolution Resolution
    if err := json.Unmarshal([]byte(response), &resolution); err != nil {
        return Resolution{}, err
    }

    return resolution, nil
}

func applyResolutions(jsonlPath string, conflicts []ConflictBlock, resolutions []Resolution) error {
    // Read entire JSONL
    allIssues, err := readJSONL(jsonlPath)
    if err != nil {
        return err
    }

    // Apply resolutions
    for i, resolution := range resolutions {
        conflict := conflicts[i]

        switch resolution.Action {
        case "remap":
            // Remap IDs and update references
            remapIssueID(allIssues, resolution.OldID, resolution.NewID)

        case "merge":
            // Replace both with merged issue
            replaceIssues(allIssues, conflict.HeadIssues, conflict.BaseIssues, resolution.Merged)

        case "keep-head":
            // Remove base issues
            removeIssues(allIssues, conflict.BaseIssues)

        case "keep-base":
            // Remove head issues
            removeIssues(allIssues, conflict.HeadIssues)
        }
    }

    // Write back to JSONL (atomic)
    return writeJSONL(jsonlPath, allIssues)
}
```

**AI Integration:**

```go
// internal/ai/client.go (new package)
package ai

import (
    "context"
    "fmt"
    "os"
)

type Client struct {
    provider string // "anthropic", "openai", "ollama"
    apiKey   string
    model    string
}

func NewClient() (*Client, error) {
    provider := os.Getenv("BEADS_AI_PROVIDER") // "anthropic" (default)
    apiKey := os.Getenv("BEADS_AI_API_KEY")    // Required for cloud providers
    model := os.Getenv("BEADS_AI_MODEL")       // "claude-3-5-sonnet-20241022" (default)

    if provider == "" {
        provider = "anthropic"
    }

    if apiKey == "" && provider != "ollama" {
        return nil, fmt.Errorf("BEADS_AI_API_KEY required for provider %s", provider)
    }

    return &Client{
        provider: provider,
        apiKey:   apiKey,
        model:    model,
    }, nil
}

func (c *Client) Complete(ctx context.Context, prompt string) (string, error) {
    switch c.provider {
    case "anthropic":
        return c.callAnthropic(ctx, prompt)
    case "openai":
        return c.callOpenAI(ctx, prompt)
    case "ollama":
        return c.callOllama(ctx, prompt)
    default:
        return "", fmt.Errorf("unknown AI provider: %s", c.provider)
    }
}

func (c *Client) callAnthropic(ctx context.Context, prompt string) (string, error) {
    // Use anthropic-go SDK
    // Implementation omitted for brevity
    return "", nil
}
```

**Configuration:**

```bash
# ~/.config/beads/ai.conf (optional)
BEADS_AI_PROVIDER=anthropic
BEADS_AI_API_KEY=sk-ant-...
BEADS_AI_MODEL=claude-3-5-sonnet-20241022

# Or use local Ollama
BEADS_AI_PROVIDER=ollama
BEADS_AI_MODEL=llama2
```

**Example usage:**

```bash
# Detect conflicts (shows summary, doesn't modify)
$ bd resolve-conflicts
Found 3 conflicts in beads.jsonl:

Conflict 1 (lines 42-47):
  HEAD: bd-42 "Add authentication" (created by alice)
  BASE: bd-42 "Fix logging" (created by bob)
  → Recommendation: REMAP (different issues, same ID)

Conflict 2 (lines 103-108):
  HEAD: bd-87 "Update docs for API"
  BASE: bd-87 "Update docs for API v2"
  → Recommendation: MERGE (similar, minor differences)

Conflict 3 (lines 234-239):
  HEAD: bd-156 "Refactor parser"
  BASE: bd-156 "Refactor parser" (identical)
  → Recommendation: KEEP-HEAD (identical content)

Run 'bd resolve-conflicts --auto' to apply recommendations.
Run 'bd resolve-conflicts --interactive' to review each conflict.

# Auto-resolve with AI
$ bd resolve-conflicts --auto --ai
Resolving 3 conflicts...
✓ Conflict 1: Remapped bd-42 (BASE) → bd-200
✓ Conflict 2: Merged into bd-87 (combined descriptions)
✓ Conflict 3: Kept HEAD version (identical)

Updated beads.jsonl (conflicts resolved)
Next steps:
  1. Review changes: git diff beads.jsonl
  2. Import to database: bd import
  3. Commit resolution: git add beads.jsonl && git commit

# Interactive mode
$ bd resolve-conflicts --interactive
Conflict 1 of 3 (lines 42-47):

  HEAD: bd-42 "Add authentication"
    Created: 2025-10-20 by alice
    Status: in_progress
    Labels: feature, security

  BASE: bd-42 "Fix logging"
    Created: 2025-10-21 by bob
    Status: open
    Labels: bug, logging

AI Recommendation: REMAP (different issues, same ID)
Reason: Issues have different topics (auth vs logging) and authors

Choose action:
  1) Remap BASE to new ID (recommended)
  2) Merge into one issue
  3) Keep HEAD, discard BASE
  4) Keep BASE, discard HEAD
  5) Skip (resolve manually)

Your choice [1-5]: 1

✓ Will remap BASE bd-42 → bd-200

Continue to next conflict? [Y/n]:
```

### 2. `bd find-duplicates` - AI-Powered Duplicate Detection

**Purpose:** Find semantically duplicate issues across the database

**Usage:**
```bash
# Find all duplicates
bd find-duplicates

# Find duplicates with specific threshold
bd find-duplicates --threshold 0.8

# Auto-merge duplicates (requires confirmation)
bd find-duplicates --merge
```

**Implementation:**

```go
// cmd/bd/find_duplicates.go (new file)
package main

import (
    "context"
    "fmt"

    "github.com/steveyegge/beads/internal/ai"
    "github.com/steveyegge/beads/internal/storage"
    "github.com/steveyegge/beads/internal/types"
)

type DuplicateGroup struct {
    Issues     []*types.Issue
    Similarity float64
    Reason     string
}

func findDuplicates(ctx context.Context, store storage.Storage, useAI bool, threshold float64) ([]DuplicateGroup, error) {
    // Get all open issues
    issues, err := store.ListIssues(ctx, storage.ListOptions{
        Status: []string{"open", "in_progress"},
    })
    if err != nil {
        return nil, err
    }

    if !useAI {
        // Mechanical approach: exact title match
        return findDuplicatesMechanical(issues), nil
    }

    // AI approach: semantic similarity
    return findDuplicatesWithAI(ctx, issues, threshold)
}

func findDuplicatesMechanical(issues []*types.Issue) []DuplicateGroup {
    // Group by normalized title
    titleMap := make(map[string][]*types.Issue)

    for _, issue := range issues {
        normalized := normalizeTitle(issue.Title)
        titleMap[normalized] = append(titleMap[normalized], issue)
    }

    var groups []DuplicateGroup
    for _, group := range titleMap {
        if len(group) > 1 {
            groups = append(groups, DuplicateGroup{
                Issues:     group,
                Similarity: 1.0, // Exact match
                Reason:     "Identical titles",
            })
        }
    }

    return groups
}

func findDuplicatesWithAI(ctx context.Context, issues []*types.Issue, threshold float64) ([]DuplicateGroup, error) {
    aiClient, err := ai.NewClient()
    if err != nil {
        return nil, fmt.Errorf("AI client unavailable: %v (set BEADS_AI_API_KEY)", err)
    }

    var groups []DuplicateGroup

    // Compare all pairs (N^2, but issues typically <1000)
    for i := 0; i < len(issues); i++ {
        for j := i + 1; j < len(issues); j++ {
            similarity, reason, err := compareIssues(ctx, aiClient, issues[i], issues[j])
            if err != nil {
                continue // Skip on error
            }

            if similarity >= threshold {
                groups = append(groups, DuplicateGroup{
                    Issues:     []*types.Issue{issues[i], issues[j]},
                    Similarity: similarity,
                    Reason:     reason,
                })
            }
        }
    }

    return groups, nil
}

func compareIssues(ctx context.Context, client *ai.Client, issue1, issue2 *types.Issue) (float64, string, error) {
    prompt := fmt.Sprintf(`
Compare these two issues and determine if they are duplicates.

Issue 1: %s
Title: %s
Description: %s
Labels: %v
Status: %s

Issue 2: %s
Title: %s
Description: %s
Labels: %v
Status: %s

Respond in JSON:
{
    "similarity": 0.0-1.0,
    "reason": "explanation",
    "is_duplicate": true/false
}
`, issue1.ID, issue1.Title, issue1.Description, issue1.Labels, issue1.Status,
   issue2.ID, issue2.Title, issue2.Description, issue2.Labels, issue2.Status)

    response, err := client.Complete(ctx, prompt)
    if err != nil {
        return 0, "", err
    }

    var result struct {
        Similarity  float64 `json:"similarity"`
        Reason      string  `json:"reason"`
        IsDuplicate bool    `json:"is_duplicate"`
    }

    if err := json.Unmarshal([]byte(response), &result); err != nil {
        return 0, "", err
    }

    return result.Similarity, result.Reason, nil
}
```

**Optimization for large databases:**

For databases with >1000 issues, N^2 comparison is too slow. Use **embedding-based similarity**:

```go
// Use OpenAI embeddings or local model
func findDuplicatesWithEmbeddings(ctx context.Context, issues []*types.Issue, threshold float64) ([]DuplicateGroup, error) {
    // 1. Generate embeddings for all issues
    embeddings := make([][]float64, len(issues))
    for i, issue := range issues {
        text := fmt.Sprintf("%s\n%s", issue.Title, issue.Description)
        embedding, err := generateEmbedding(ctx, text)
        if err != nil {
            return nil, err
        }
        embeddings[i] = embedding
    }

    // 2. Find similar pairs using cosine similarity
    var groups []DuplicateGroup
    for i := 0; i < len(embeddings); i++ {
        for j := i + 1; j < len(embeddings); j++ {
            similarity := cosineSimilarity(embeddings[i], embeddings[j])
            if similarity >= threshold {
                groups = append(groups, DuplicateGroup{
                    Issues:     []*types.Issue{issues[i], issues[j]},
                    Similarity: similarity,
                    Reason:     "Semantic similarity via embeddings",
                })
            }
        }
    }

    return groups, nil
}

func generateEmbedding(ctx context.Context, text string) ([]float64, error) {
    // Use OpenAI text-embedding-3-small or local model
    // Returns 1536-dimensional vector
    return nil, nil
}

func cosineSimilarity(a, b []float64) float64 {
    var dotProduct, normA, normB float64
    for i := range a {
        dotProduct += a[i] * b[i]
        normA += a[i] * a[i]
        normB += b[i] * b[i]
    }
    return dotProduct / (math.Sqrt(normA) * math.Sqrt(normB))
}
```

**Example usage:**

```bash
# Find duplicates (mechanical, no AI)
$ bd find-duplicates --no-ai
Found 2 potential duplicate groups:

Group 1 (Similarity: 100%):
  bd-42: "Fix memory leak in parser"
  bd-87: "Fix memory leak in parser"
  Reason: Identical titles

Group 2 (Similarity: 100%):
  bd-103: "Update documentation"
  bd-145: "Update documentation"
  Reason: Identical titles

# Find duplicates with AI (semantic)
$ bd find-duplicates --ai --threshold 0.75
Found 4 potential duplicate groups:

Group 1 (Similarity: 95%):
  bd-42: "Fix memory leak in parser"
  bd-87: "Parser memory leak needs fixing"
  Reason: Same issue described differently

Group 2 (Similarity: 88%):
  bd-103: "Update API documentation"
  bd-145: "Document new API endpoints"
  Reason: Both about API docs, overlapping scope

Group 3 (Similarity: 82%):
  bd-200: "Optimize database queries"
  bd-234: "Improve query performance"
  Reason: Same goal (performance), different wording

Group 4 (Similarity: 76%):
  bd-301: "Add user authentication"
  bd-312: "Implement login system"
  Reason: Authentication and login are related features

# Merge duplicates interactively
$ bd find-duplicates --merge
Found 2 duplicate groups. Review each:

Group 1 (Similarity: 95%):
  bd-42: "Fix memory leak in parser" (alice, 2025-10-20)
    Status: in_progress
    Labels: bug, performance
    Comments: 3

  bd-87: "Parser memory leak needs fixing" (bob, 2025-10-21)
    Status: open
    Labels: bug
    Comments: 1

Merge these issues? [y/N] y

Choose canonical issue:
  1) bd-42 (more activity, earlier)
  2) bd-87
Your choice [1-2]: 1

✓ Merged bd-87 → bd-42
  - Moved 1 comment from bd-87
  - Added note: "Duplicate of bd-42"
  - Closed bd-87 with reason: "duplicate"

Continue to next group? [Y/n]:
```

### 3. `bd detect-pollution` - Test Issue Detector

**Purpose:** Identify and clean up test issues that leaked into production database

**Usage:**
```bash
# Detect test issues
bd detect-pollution

# Auto-delete with confirmation
bd detect-pollution --clean

# Export pollution report
bd detect-pollution --report pollution.json
```

**Implementation:**

```go
// cmd/bd/detect_pollution.go (new file)
package main

import (
    "context"
    "regexp"
    "strings"

    "github.com/steveyegge/beads/internal/storage"
    "github.com/steveyegge/beads/internal/types"
)

type PollutionIndicator struct {
    Pattern string
    Weight  float64
}

var pollutionPatterns = []PollutionIndicator{
    {Pattern: `^test[-_]`, Weight: 0.9},                    // "test-issue-1"
    {Pattern: `^benchmark[-_]`, Weight: 0.95},              // "benchmark-issue-42"
    {Pattern: `^(?i)test\s+issue`, Weight: 0.85},           // "Test Issue 123"
    {Pattern: `^(?i)dummy`, Weight: 0.8},                   // "Dummy issue"
    {Pattern: `^(?i)sample`, Weight: 0.7},                  // "Sample issue"
    {Pattern: `^(?i)todo.*test`, Weight: 0.75},             // "TODO test something"
    {Pattern: `^issue\s+\d+$`, Weight: 0.6},                // "issue 123"
    {Pattern: `^[A-Z]{4,}-\d+$`, Weight: 0.5},              // "JIRA-123" (might be import)
}

func detectPollution(ctx context.Context, store storage.Storage, useAI bool) ([]*types.Issue, error) {
    allIssues, err := store.ListIssues(ctx, storage.ListOptions{})
    if err != nil {
        return nil, err
    }

    if !useAI {
        // Mechanical approach: pattern matching
        return detectPollutionMechanical(allIssues), nil
    }

    // AI approach: semantic classification
    return detectPollutionWithAI(ctx, allIssues)
}

func detectPollutionMechanical(issues []*types.Issue) []*types.Issue {
    var polluted []*types.Issue

    for _, issue := range issues {
        score := 0.0

        // Check title against patterns
        for _, indicator := range pollutionPatterns {
            matched, _ := regexp.MatchString(indicator.Pattern, issue.Title)
            if matched {
                score = max(score, indicator.Weight)
            }
        }

        // Additional heuristics
        if len(issue.Title) < 10 {
            score += 0.2 // Very short titles suspicious
        }

        if issue.Description == "" || issue.Description == issue.Title {
            score += 0.1 // No description
        }

        if strings.Count(issue.Title, "test") > 1 {
            score += 0.2 // Multiple "test" occurrences
        }

        // Threshold: 0.7
        if score >= 0.7 {
            polluted = append(polluted, issue)
        }
    }

    return polluted
}

func detectPollutionWithAI(ctx context.Context, issues []*types.Issue) ([]*types.Issue, error) {
    aiClient, err := ai.NewClient()
    if err != nil {
        return nil, err
    }

    // Batch issues for efficiency (classify 50 at a time)
    batchSize := 50
    var polluted []*types.Issue

    for i := 0; i < len(issues); i += batchSize {
        end := min(i+batchSize, len(issues))
        batch := issues[i:end]

        prompt := buildPollutionPrompt(batch)
        response, err := aiClient.Complete(ctx, prompt)
        if err != nil {
            return nil, err
        }

        // Parse response: list of issue IDs classified as test pollution
        pollutedIDs, err := parsePollutionResponse(response)
        if err != nil {
            continue
        }

        for _, issue := range batch {
            for _, id := range pollutedIDs {
                if issue.ID == id {
                    polluted = append(polluted, issue)
                }
            }
        }
    }

    return polluted, nil
}

func buildPollutionPrompt(issues []*types.Issue) string {
    var builder strings.Builder
    builder.WriteString("Identify test pollution in this issue list. Test issues have patterns like:\n")
    builder.WriteString("- Titles starting with 'test', 'benchmark', 'sample'\n")
    builder.WriteString("- Sequential numbering (test-1, test-2, ...)\n")
    builder.WriteString("- Generic descriptions or no description\n")
    builder.WriteString("- Created in rapid succession\n\n")
    builder.WriteString("Issues:\n")

    for _, issue := range issues {
        fmt.Fprintf(&builder, "%s: %s (created: %s)\n", issue.ID, issue.Title, issue.CreatedAt)
    }

    builder.WriteString("\nRespond with JSON list of polluted issue IDs: {\"polluted\": [\"bd-1\", \"bd-2\"]}")
    return builder.String()
}
```

**Example usage:**

```bash
# Detect pollution
$ bd detect-pollution
Scanning 523 issues for test pollution...

Found 47 potential test issues:

High Confidence (score ≥ 0.9):
  bd-100: "test-issue-1"
  bd-101: "test-issue-2"
  ...
  bd-146: "benchmark-create-47"
  (Total: 45 issues)

Medium Confidence (score 0.7-0.9):
  bd-200: "Quick test"
  bd-301: "sample issue for testing"
  (Total: 2 issues)

Recommendation: Review and clean up these issues.
Run 'bd detect-pollution --clean' to delete them (with confirmation).

# Clean up
$ bd detect-pollution --clean
Found 47 test issues. Delete them? [y/N] y

Deleting 47 issues...
✓ Deleted bd-100 through bd-146
✓ Deleted bd-200, bd-301

Cleanup complete. Exported deleted issues to .beads/pollution-backup.jsonl
(Run 'bd import .beads/pollution-backup.jsonl' to restore if needed)
```

### 4. `bd repair-deps` - Orphaned Dependency Cleaner

**Purpose:** Find and fix orphaned dependency references

**Usage:**
```bash
# Find orphans
bd repair-deps

# Auto-fix (remove orphaned references)
bd repair-deps --fix

# Interactive
bd repair-deps --interactive
```

**Implementation:**

```go
// cmd/bd/repair_deps.go (new file)
package main

import (
    "context"
    "fmt"

    "github.com/steveyegge/beads/internal/storage"
    "github.com/steveyegge/beads/internal/types"
)

type OrphanedDependency struct {
    Issue      *types.Issue
    OrphanedID string
}

func findOrphanedDeps(ctx context.Context, store storage.Storage) ([]OrphanedDependency, error) {
    allIssues, err := store.ListIssues(ctx, storage.ListOptions{})
    if err != nil {
        return nil, err
    }

    // Build ID existence map
    existingIDs := make(map[string]bool)
    for _, issue := range allIssues {
        existingIDs[issue.ID] = true
    }

    // Find orphans
    var orphaned []OrphanedDependency
    for _, issue := range allIssues {
        for _, depID := range issue.DependsOn {
            if !existingIDs[depID] {
                orphaned = append(orphaned, OrphanedDependency{
                    Issue:      issue,
                    OrphanedID: depID,
                })
            }
        }
    }

    return orphaned, nil
}

func repairOrphanedDeps(ctx context.Context, store storage.Storage, orphaned []OrphanedDependency, autoFix bool) error {
    for _, o := range orphaned {
        if autoFix {
            // Remove orphaned dependency
            newDeps := removeString(o.Issue.DependsOn, o.OrphanedID)
            o.Issue.DependsOn = newDeps

            if err := store.UpdateIssue(ctx, o.Issue); err != nil {
                return err
            }

            fmt.Printf("✓ Removed orphaned dependency %s from %s\n", o.OrphanedID, o.Issue.ID)
        } else {
            fmt.Printf("Found orphan: %s depends on non-existent %s\n", o.Issue.ID, o.OrphanedID)
        }
    }

    return nil
}
```

**Example usage:**

```bash
# Find orphaned deps
$ bd repair-deps
Scanning dependencies...

Found 3 orphaned dependencies:

  bd-42: depends on bd-10 (deleted)
  bd-87: depends on bd-25 (deleted)
  bd-103: depends on bd-25 (deleted)

Run 'bd repair-deps --fix' to remove these references.

# Auto-fix
$ bd repair-deps --fix
✓ Removed bd-10 from bd-42 dependencies
✓ Removed bd-25 from bd-87 dependencies
✓ Removed bd-25 from bd-103 dependencies

Repaired 3 issues.
```

### 5. `bd validate` - Comprehensive Health Check

**Purpose:** Run all validation checks in one command

**Usage:**
```bash
# Run all checks
bd validate

# Auto-fix all issues
bd validate --fix-all

# Specific checks
bd validate --checks=duplicates,orphans,pollution
```

**Implementation:**

```go
// cmd/bd/validate.go (new file)
package main

import (
    "context"
    "fmt"

    "github.com/steveyegge/beads/internal/storage"
)

func runValidation(ctx context.Context, store storage.Storage, checks []string, autoFix bool) error {
    results := ValidationResults{}

    for _, check := range checks {
        switch check {
        case "duplicates":
            groups, err := findDuplicates(ctx, store, false, 1.0)
            if err != nil {
                return err
            }
            results.Duplicates = len(groups)

        case "orphans":
            orphaned, err := findOrphanedDeps(ctx, store)
            if err != nil {
                return err
            }
            results.Orphans = len(orphaned)
            if autoFix {
                repairOrphanedDeps(ctx, store, orphaned, true)
            }

        case "pollution":
            polluted, err := detectPollution(ctx, store, false)
            if err != nil {
                return err
            }
            results.Pollution = len(polluted)

        case "conflicts":
            jsonlPath := findJSONLPath()
            conflicts, err := detectConflicts(jsonlPath)
            if err != nil {
                return err
            }
            results.Conflicts = len(conflicts)
        }
    }

    results.Print()
    return nil
}

type ValidationResults struct {
    Duplicates int
    Orphans    int
    Pollution  int
    Conflicts  int
}

func (r ValidationResults) Print() {
    fmt.Println("\nValidation Results:")
    fmt.Println("===================")
    fmt.Printf("Duplicates:    %d\n", r.Duplicates)
    fmt.Printf("Orphans:       %d\n", r.Orphans)
    fmt.Printf("Pollution:     %d\n", r.Pollution)
    fmt.Printf("Conflicts:     %d\n", r.Conflicts)

    total := r.Duplicates + r.Orphans + r.Pollution + r.Conflicts
    if total == 0 {
        fmt.Println("\n✓ Database is healthy!")
    } else {
        fmt.Printf("\n⚠ Found %d issues to fix\n", total)
    }
}
```

**Example usage:**

```bash
$ bd validate
Running validation checks...

✓ Checking for duplicates... found 2 groups
✓ Checking for orphaned dependencies... found 3
✓ Checking for test pollution... found 0
✓ Checking for git conflicts... found 1

Validation Results:
===================
Duplicates:    2
Orphans:       3
Pollution:     0
Conflicts:     1

⚠ Found 6 issues to fix

Recommendations:
  - Run 'bd find-duplicates --merge' to handle duplicates
  - Run 'bd repair-deps --fix' to remove orphaned dependencies
  - Run 'bd resolve-conflicts' to resolve git conflicts

$ bd validate --fix-all
Running validation with auto-fix...
✓ Fixed 3 orphaned dependencies
✓ Resolved 1 git conflict (mechanical)

2 duplicate groups require manual review.
Run 'bd find-duplicates --merge' to handle them interactively.
```

## Agent Integration

### MCP Server Functions

Add these as MCP functions for easy agent access:

```python
# integrations/beads-mcp/src/beads_mcp/server.py

@server.call_tool()
async def beads_resolve_conflicts(auto: bool = False, ai: bool = True) -> list:
    """Resolve git merge conflicts in JSONL file"""
    result = subprocess.run(
        ["bd", "resolve-conflicts"] +
        (["--auto"] if auto else []) +
        (["--ai"] if ai else []) +
        ["--json"],
        capture_output=True,
        text=True
    )
    return json.loads(result.stdout)

@server.call_tool()
async def beads_find_duplicates(ai: bool = True, threshold: float = 0.8) -> list:
    """Find duplicate issues using AI or mechanical matching"""
    result = subprocess.run(
        ["bd", "find-duplicates"] +
        (["--ai"] if ai else ["--no-ai"]) +
        ["--threshold", str(threshold), "--json"],
        capture_output=True,
        text=True
    )
    return json.loads(result.stdout)

@server.call_tool()
async def beads_detect_pollution() -> list:
    """Detect test issues that leaked into production"""
    result = subprocess.run(
        ["bd", "detect-pollution", "--json"],
        capture_output=True,
        text=True
    )
    return json.loads(result.stdout)

@server.call_tool()
async def beads_validate(fix_all: bool = False) -> dict:
    """Run all validation checks"""
    result = subprocess.run(
        ["bd", "validate"] +
        (["--fix-all"] if fix_all else []) +
        ["--json"],
        capture_output=True,
        text=True
    )
    return json.loads(result.stdout)
```

### Agent Workflow

**Typical agent repair workflow:**

```
1. Agent notices issue (e.g., git merge conflict error)
2. Agent calls: mcp__beads__resolve_conflicts(auto=True, ai=True)
3. If successful:
   - Agent reports: "Resolved 3 conflicts, remapped 1 ID"
   - Agent continues work
4. If fails:
   - Agent calls: mcp__beads__resolve_conflicts() for report
   - Agent asks user for guidance
```

**Proactive validation:**

```
At session start, agent can:
1. Call: mcp__beads__validate()
2. If issues found:
   - Report to user: "Found 3 orphaned deps and 2 duplicates"
   - Ask: "Should I fix these?"
3. If user approves:
   - Call: mcp__beads__validate(fix_all=True)
   - Report: "Fixed 3 orphans, 2 duplicates need manual review"
```

## Cost Considerations

### AI API Costs

**Claude 3.5 Sonnet pricing (2025):**
- Input: $3.00 / 1M tokens
- Output: $15.00 / 1M tokens

**Typical usage:**

1. **Resolve conflicts** (~500 tokens per conflict)
   - Cost: ~$0.0075 per conflict
   - 10 conflicts/day = $0.075/day = $2.25/month

2. **Find duplicates** (~200 tokens per comparison)
   - Cost: ~$0.003 per comparison
   - 100 issues = 4,950 comparisons = $15/run
   - **Too expensive!** Use embeddings instead

3. **Embeddings approach** (text-embedding-3-small)
   - $0.02 / 1M tokens
   - 100 issues × 100 tokens = 10K tokens = $0.0002/run
   - **Much cheaper!**

**Recommendations:**
- Use AI for conflict resolution (low frequency, high value)
- Use embeddings for duplicate detection (high frequency, needs scale)
- Use mechanical checks by default, AI as opt-in

### Local AI Option

For users who want to avoid API costs:

```bash
# Use Ollama (free, local)
BEADS_AI_PROVIDER=ollama
BEADS_AI_MODEL=llama3.2

# Or use local embedding model
BEADS_EMBEDDING_PROVIDER=local
BEADS_EMBEDDING_MODEL=all-MiniLM-L6-v2  # 384-dimensional, fast
```

## Implementation Roadmap

### Phase 1: Mechanical Commands (2-3 weeks)
- [ ] `bd repair-deps` (orphaned dependency cleaner)
- [ ] `bd detect-pollution` (pattern-based test detection)
- [ ] `bd resolve-conflicts` (mechanical ID remapping)
- [ ] `bd validate` (run all checks)

### Phase 2: AI Integration (2-3 weeks)
- [ ] Add `internal/ai` package
- [ ] Implement Anthropic, OpenAI, Ollama providers
- [ ] Add `--ai` flag to commands
- [ ] Test with real conflicts/duplicates

### Phase 3: Embeddings (1-2 weeks)
- [ ] Add embedding generation
- [ ] Implement cosine similarity search
- [ ] Optimize for large databases (>1K issues)
- [ ] Benchmark performance

### Phase 4: MCP Integration (1 week)
- [ ] Add MCP functions for all repair commands
- [ ] Update beads-mcp documentation
- [ ] Add examples to AGENTS.md

### Phase 5: Polish (1 week)
- [ ] Add `--json` output for all commands
- [ ] Improve error messages
- [ ] Add progress indicators for slow operations
- [ ] Write comprehensive tests

**Total timeline: 7-10 weeks**

## Success Metrics

### Quantitative
- ✅ Agent repair time reduced by >50%
- ✅ Manual interventions reduced by >70%
- ✅ Conflict resolution time <30 seconds
- ✅ Duplicate detection accuracy >90%

### Qualitative
- ✅ Agents report fewer "stuck" situations
- ✅ Users spend less time on database maintenance
- ✅ Fewer support requests about database issues

## Open Questions

1. **Should repair commands auto-run in daemon?**
   - Recommendation: No, too risky. On-demand only.

2. **Should agents proactively run validation?**
   - Recommendation: Yes, at session start (with user notification)

3. **What AI provider should be default?**
   - Recommendation: None (mechanical by default), user opts in

4. **Should duplicate detection be continuous?**
   - Recommendation: No, run on-demand or weekly scheduled

5. **How to handle false positives in pollution detection?**
   - Recommendation: Always confirm before deleting, backup to JSONL

## Conclusion

Repair commands address the **root cause of agent repair burden**: lack of specialized tools for common maintenance tasks. By providing `bd resolve-conflicts`, `bd find-duplicates`, `bd detect-pollution`, and `bd validate`, we:

✅ Reduce agent time from 5-10 minutes to <30 seconds per repair
✅ Provide consistent repair logic across sessions
✅ Enable proactive validation instead of reactive fixing
✅ Allow AI assistance where valuable (conflicts, duplicates) while keeping mechanical checks fast

Combined with event-driven daemon (instant feedback), these tools should significantly reduce the "not as much in the background as I'd like" pain.
