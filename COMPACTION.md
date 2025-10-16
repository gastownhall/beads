# Database Compaction Guide

## Overview

Beads compaction is **agentic memory decay** - your database naturally forgets fine-grained details of old work while preserving the essential context agents need. This keeps your database lightweight and fast, even after thousands of issues.

### Key Concepts

- **Semantic compression**: Claude Haiku summarizes issues intelligently, preserving decisions and outcomes
- **Two-tier system**: Gradual decay from full detail → summary → ultra-brief
- **Permanent decay**: Original content is discarded to save space (not reversible)
- **Safe by design**: Dry-run preview, eligibility checks, git history preserves old versions

## How It Works

### Tier 1: Semantic Compression (30+ days)

**Target**: Closed issues 30+ days old with no open dependents

**Process**:
1. Check eligibility (closed, 30+ days, no blockers)
2. Send to Claude Haiku for summarization
3. Replace verbose fields with concise summary
4. Store original size for statistics

**Result**: 70-80% space reduction

**Example**:

*Before (856 bytes):*
```
Title: Fix authentication race condition in login flow
Description: Users report intermittent 401 errors during concurrent
login attempts. The issue occurs when multiple requests hit the auth
middleware simultaneously...

Design: [15 lines of implementation details]
Acceptance Criteria: [8 test scenarios]
Notes: [debugging session notes]
```

*After (171 bytes):*
```
Title: Fix authentication race condition in login flow
Description: Fixed race condition in auth middleware causing 401s
during concurrent logins. Added mutex locks and updated tests.
Resolution: Deployed in v1.2.3.
```

### Tier 2: Ultra Compression (90+ days)

**Target**: Tier 1 issues 90+ days old, rarely referenced

**Process**:
1. Verify existing Tier 1 compaction
2. Check reference frequency (git commits, other issues)
3. Ultra-compress to single paragraph
4. Optionally prune events (keep created/closed only)

**Result**: 90-95% space reduction

**Example**:

*After Tier 2 (43 bytes):*
```
Description: Auth race condition fixed, deployed v1.2.3.
```

## CLI Reference

### Preview Candidates

```bash
# See what would be compacted
bd compact --dry-run --all

# Check Tier 2 candidates
bd compact --dry-run --all --tier 2

# Preview specific issue
bd compact --dry-run --id bd-42
```

### Compact Issues

```bash
# Compact all eligible issues (Tier 1)
bd compact --all

# Compact specific issue
bd compact --id bd-42

# Force compact (bypass checks - use with caution)
bd compact --id bd-42 --force

# Tier 2 ultra-compression
bd compact --all --tier 2

# Control parallelism
bd compact --all --workers 10 --batch-size 20
```

### Statistics & Monitoring

```bash
# Show compaction stats
bd compact --stats

# Output:
# Total issues: 2,438
# Compacted: 847 (34.7%)
#   Tier 1: 812 issues
#   Tier 2: 35 issues
# Space saved: 1.2 MB (68% reduction)
# Estimated cost: $0.85
```

## Eligibility Rules

### Tier 1 Eligibility

- ✅ Status: `closed`
- ✅ Age: 30+ days since `closed_at`
- ✅ Dependents: No open issues depending on this one
- ✅ Not already compacted

### Tier 2 Eligibility

- ✅ Already Tier 1 compacted
- ✅ Age: 90+ days since `closed_at`
- ✅ Low reference frequency:
  - Mentioned in <5 git commits in last 90 days, OR
  - Referenced by <3 issues created in last 90 days

## Configuration

### API Key Setup

**Option 1: Environment variable (recommended)**

```bash
export ANTHROPIC_API_KEY="sk-ant-..."
```

Add to your shell profile (`~/.zshrc`, `~/.bashrc`, etc.) for persistence.

**Option 2: CI/CD environments**

```yaml
# GitHub Actions
env:
  ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}

# GitLab CI
variables:
  ANTHROPIC_API_KEY: $ANTHROPIC_API_KEY
```

### Parallel Processing

Control performance vs. API rate limits:

```bash
# Default: 5 workers, 10 issues per batch
bd compact --all

# High throughput (watch rate limits!)
bd compact --all --workers 20 --batch-size 50

# Conservative (avoid rate limits)
bd compact --all --workers 2 --batch-size 5
```

## Cost Analysis

### Pricing Basics

Compaction uses Claude Haiku (~$1 per 1M input tokens, ~$5 per 1M output tokens).

Typical issue:
- Input: ~500 tokens (issue content)
- Output: ~100 tokens (summary)
- Cost per issue: ~$0.001 (0.1¢)

### Cost Examples

| Issues | Est. Cost | Time (5 workers) |
|--------|-----------|------------------|
| 100    | $0.10     | ~2 minutes       |
| 1,000  | $1.00     | ~20 minutes      |
| 10,000 | $10.00    | ~3 hours         |

### Monthly Cost Estimate

If you close 50 issues/month and compact monthly:
- **Monthly cost**: $0.05
- **Annual cost**: $0.60

Even large teams (500 issues/month) pay ~$6/year.

### Space Savings

| Database Size | Issues | After Tier 1 | After Tier 2 |
|---------------|--------|--------------|--------------|
| 10 MB         | 2,000  | 3 MB (-70%)  | 1 MB (-90%)  |
| 100 MB        | 20,000 | 30 MB (-70%) | 10 MB (-90%) |
| 1 GB          | 200,000| 300 MB (-70%)| 100 MB (-90%)|

## Automation

### Monthly Cron Job

```bash
#!/bin/bash
# /etc/cron.monthly/bd-compact.sh

export ANTHROPIC_API_KEY="sk-ant-..."
cd /path/to/your/repo

# Compact Tier 1
bd compact --all 2>&1 | tee -a ~/.bd-compact.log

# Commit results
git add .beads/issues.jsonl issues.db
git commit -m "Monthly compaction: $(date +%Y-%m)"
git push
```

Make executable:
```bash
chmod +x /etc/cron.monthly/bd-compact.sh
```

### Automated Workflow Script

```bash
#!/bin/bash
# examples/compaction/workflow.sh

# Exit on error
set -e

echo "=== BD Compaction Workflow ==="
echo "Date: $(date)"
echo

# Check API key
if [ -z "$ANTHROPIC_API_KEY" ]; then
  echo "Error: ANTHROPIC_API_KEY not set"
  exit 1
fi

# Preview candidates
echo "--- Preview Tier 1 Candidates ---"
bd compact --dry-run --all

read -p "Proceed with Tier 1 compaction? (y/N) " -n 1 -r
echo
if [[ $REPLY =~ ^[Yy]$ ]]; then
  echo "--- Running Tier 1 Compaction ---"
  bd compact --all
fi

# Preview Tier 2
echo
echo "--- Preview Tier 2 Candidates ---"
bd compact --dry-run --all --tier 2

read -p "Proceed with Tier 2 compaction? (y/N) " -n 1 -r
echo
if [[ $REPLY =~ ^[Yy]$ ]]; then
  echo "--- Running Tier 2 Compaction ---"
  bd compact --all --tier 2
fi

# Show stats
echo
echo "--- Final Statistics ---"
bd compact --stats

echo
echo "=== Compaction Complete ==="
```

### Pre-commit Hook (Automatic)

```bash
#!/bin/bash
# .git/hooks/pre-commit

# Auto-compact before each commit (optional, experimental)
if command -v bd &> /dev/null && [ -n "$ANTHROPIC_API_KEY" ]; then
  bd compact --all --dry-run > /dev/null 2>&1
  # Only compact if >10 eligible issues
  ELIGIBLE=$(bd compact --dry-run --all --json 2>/dev/null | jq '. | length')
  if [ "$ELIGIBLE" -gt 10 ]; then
    echo "Auto-compacting $ELIGIBLE eligible issues..."
    bd compact --all
  fi
fi
```

## Safety & Recovery

### Git History

Compaction is permanent - the original content is discarded to save space. However, you can recover old versions from git history:

```bash
# View issue before compaction
git log -p -- .beads/issues.jsonl | grep -A 50 "bd-42"

# Checkout old version
git checkout <commit-hash> -- .beads/issues.jsonl

# Or use git show
git show <commit-hash>:.beads/issues.jsonl | grep -A 50 "bd-42"
```

### Verification

After compaction, verify with:

```bash
# Check compaction stats
bd compact --stats

# Spot-check compacted issues
bd show bd-42
```

## Troubleshooting

### "ANTHROPIC_API_KEY not set"

```bash
export ANTHROPIC_API_KEY="sk-ant-..."
# Add to ~/.zshrc or ~/.bashrc for persistence
```

### Rate Limit Errors

Reduce parallelism:
```bash
bd compact --all --workers 2 --batch-size 5
```

Or add delays between batches (future enhancement).

### Issue Not Eligible

Check eligibility:
```bash
bd compact --dry-run --id bd-42
```

Force compact (if you know what you're doing):
```bash
bd compact --id bd-42 --force
```

## FAQ

### When should I compact?

- **Small projects (<500 issues)**: Rarely needed, maybe annually
- **Medium projects (500-5000 issues)**: Every 3-6 months
- **Large projects (5000+ issues)**: Monthly or quarterly
- **High-velocity teams**: Set up automated monthly compaction

### Can I recover compacted issues?

Compaction is permanent, but you can recover from git history:
```bash
git log -p -- .beads/issues.jsonl | grep -A 50 "bd-42"
```

### What happens to dependencies?

Dependencies are preserved. Compaction only affects the issue's text fields (description, design, notes, acceptance criteria).

### Does compaction affect git history?

No. Old versions of issues remain in git history. Compaction only affects the current state in `.beads/issues.jsonl` and `issues.db`.

### Should I commit compacted issues?

**Yes.** Compaction modifies both the database and JSONL. Commit and push:

```bash
git add .beads/issues.jsonl issues.db
git commit -m "Compact old closed issues"
git push
```

### What if my team disagrees on compaction frequency?

Use `bd compact --dry-run` to preview. Discuss the candidates before running. Since compaction is permanent, get team consensus first.

### Can I compact open issues?

No. Compaction only works on closed issues to ensure active work retains full detail.

### How does Tier 2 decide "rarely referenced"?

It checks:
1. Git commits mentioning the issue ID in last 90 days
2. Other issues referencing it in descriptions/notes

If references are low (< 5 commits or < 3 issues), it's eligible for Tier 2.

### Does compaction slow down queries?

No. Compaction reduces database size, making queries faster. Agents benefit from smaller context when reading issues.

### Can I customize the summarization prompt?

Not yet, but it's planned (bd-264). The current prompt is optimized for preserving key decisions and outcomes.

## Best Practices

1. **Start with dry-run**: Always preview before compacting
2. **Compact regularly**: Monthly or quarterly depending on project size
3. **Monitor costs**: Use `bd compact --stats` to track savings
4. **Automate it**: Set up cron jobs for hands-off maintenance
5. **Commit results**: Always commit and push after compaction
6. **Team communication**: Let team know before large compaction runs (it's permanent!)

## Examples

See [examples/compaction/](examples/compaction/) for:
- `workflow.sh` - Interactive compaction workflow
- `cron-compact.sh` - Automated monthly compaction
- `auto-compact.sh` - Smart auto-compaction with thresholds

## Related Documentation

- [README.md](README.md) - Quick start and overview
- [EXTENDING.md](EXTENDING.md) - Database schema and extensions
- [GIT_WORKFLOW.md](GIT_WORKFLOW.md) - Multi-machine collaboration

## Contributing

Found a bug or have ideas for improving compaction? Open an issue or PR!

Common enhancement requests:
- Custom summarization prompts (bd-264)
- Alternative LLM backends (local models)
- Configurable eligibility rules
- Compaction analytics dashboard
- Optional snapshot retention for restore (if requested)
