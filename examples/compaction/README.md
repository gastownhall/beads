# Compaction Examples

This directory contains example scripts for automating database compaction.

## Scripts

### workflow.sh

Interactive compaction workflow with prompts. Perfect for manual compaction runs.

```bash
chmod +x workflow.sh
export ANTHROPIC_API_KEY="sk-ant-..."
./workflow.sh
```

**Features:**
- Previews candidates before compaction
- Prompts for confirmation at each tier
- Shows final statistics
- Provides next-step guidance

**When to use:** Manual monthly/quarterly compaction

### cron-compact.sh

Fully automated compaction for cron jobs. No interaction required.

```bash
# Configure
export BD_REPO_PATH="/path/to/your/repo"
export BD_LOG_FILE="$HOME/.bd-compact.log"
export ANTHROPIC_API_KEY="sk-ant-..."

# Test manually
./cron-compact.sh

# Install to cron (monthly)
cp cron-compact.sh /etc/cron.monthly/bd-compact
chmod +x /etc/cron.monthly/bd-compact

# Or add to crontab
crontab -e
# Add: 0 2 1 * * /path/to/cron-compact.sh
```

**Features:**
- Pulls latest changes before compacting
- Logs all output
- Auto-commits and pushes results
- Reports counts of compacted issues

**When to use:** Automated monthly compaction for active projects

### auto-compact.sh

Smart auto-compaction with thresholds. Only runs if enough eligible issues exist.

```bash
chmod +x auto-compact.sh

# Compact if 10+ eligible issues
./auto-compact.sh

# Custom threshold
./auto-compact.sh --threshold 50

# Preview without compacting
./auto-compact.sh --dry-run
```

(Tier 2 ultra-compression is planned but not yet implemented; only `--tier 1`
works today.)

**Features:**
- Configurable eligibility threshold
- Skips compaction if below threshold
- Tier 1 semantic compression (Tier 2 planned)
- Dry-run mode for testing

**When to use:** 
- Pre-commit hooks (if ANTHROPIC_API_KEY set)
- CI/CD pipelines
- Conditional automation

## Configuration

All scripts require:

```bash
export ANTHROPIC_API_KEY="sk-ant-..."
```

Additional environment variables:

- `BD_REPO_PATH`: Repository path (cron-compact.sh)
- `BD_LOG_FILE`: Log file location (cron-compact.sh)

## Recommendations

### Small Projects (<500 issues)
Use `workflow.sh` manually, once or twice per year.

### Medium Projects (500-5000 issues)
Use `cron-compact.sh` quarterly or `auto-compact.sh` in CI.

### Large Projects (5000+ issues)
Use `cron-compact.sh` monthly with both tiers:
```bash
# Modify cron-compact.sh to run both tiers
```

### High-Velocity Teams
Combine approaches:
- `auto-compact.sh --threshold 50` in CI (Tier 1 only)
- `cron-compact.sh` monthly for Tier 2

## Testing

Before deploying to cron, test scripts manually:

```bash
# Test workflow
export ANTHROPIC_API_KEY="sk-ant-..."
./workflow.sh

# Test cron script
export BD_REPO_PATH="$(pwd)"
./cron-compact.sh

# Test auto-compact (dry run)
./auto-compact.sh --dry-run --threshold 1
```

## Troubleshooting

### Script says "bd command not found"

Ensure bd is in PATH:
```bash
which bd
export PATH="$PATH:/usr/local/bin"
```

### "ANTHROPIC_API_KEY not set"

```bash
export ANTHROPIC_API_KEY="sk-ant-..."
# Add to ~/.zshrc or ~/.bashrc for persistence
```

### Cron job not running

Check cron logs:
```bash
# Linux
grep CRON /var/log/syslog

# macOS
log show --predicate 'process == "cron"' --last 1h
```

Verify script is executable:
```bash
chmod +x /etc/cron.monthly/bd-compact
```

## Cost Monitoring

Track compaction costs:

```bash
# Show stats after compaction
bd admin compact --stats

# Estimate monthly cost
# (issues_compacted / 1000) * $1.00
```

Set up alerts if costs exceed budget (future feature: bd-cost-alert).

## See Also

- [COMPACTION.md](../../COMPACTION.md) - Comprehensive compaction guide
- [README.md](../../README.md) - Main documentation
- [GIT_WORKFLOW.md](../../GIT_WORKFLOW.md) - Multi-machine collaboration
