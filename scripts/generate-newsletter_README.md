# Newsletter Generator

This script generates a weekly Beads newsletter based on the changelog, git commits, and code changes.

## Setup

### Environment Variables

Set the appropriate API key for your chosen model:

```bash
# For Claude
export ANTHROPIC_API_KEY="your-api-key"

# For OpenAI
export OPENAI_API_KEY="your-api-key"

# Optionally set the model (defaults to claude-sonnet-4-20250514)
export AI_MODEL="claude-sonnet-4-20250514"
# or
export AI_MODEL="gpt-4o"

# Optional: Auto-commit and push
export AUTO_COMMIT="true"
```

## Usage

### Generate a newsletter (last week)

```bash
git checkout main
git pull
uv run scripts/generate-newsletter.py
```

### Generate for last N days

```bash
uv run scripts/generate-newsletter.py --days 30
```

### Generate since a specific date

```bash
# Since an absolute date
uv run scripts/generate-newsletter.py --since 2025-12-15

# Or use relative format (last 14 days)
uv run scripts/generate-newsletter.py --since 14d
```

### Generate for a specific release range

```bash
# From v0.39 to v0.48
uv run scripts/generate-newsletter.py --from-release v0.39.0 --to-release v0.48.0

# From v0.39 to present
uv run scripts/generate-newsletter.py --from-release v0.39.0

# Up to v0.48.0
uv run scripts/generate-newsletter.py --to-release v0.48.0
```

### With specific AI model

```bash
uv run scripts/generate-newsletter.py --model gpt-4o
```

### Dry run (print to stdout)

```bash
uv run scripts/generate-newsletter.py --dry-run
```

### Output to specific file

```bash
uv run scripts/generate-newsletter.py --output my-newsletter.md
```

### Help

```bash
uv run scripts/generate-newsletter.py --help
```

## Cron Job Setup

Add to your crontab for weekly generation:

```bash
# Run every Monday at 9 AM
0 9 * * 1 cd /path/to/beads && uv run scripts/generate-newsletter.py
```

## How It Works

The newsletter generator creates a deeper dive beyond the changelog with workflow-impacting content:

1. **Reads the most recent version** from `CHANGELOG.md`
2. **Determines time period** - uses "last week" or "since last release" (whichever is longer), or as specified
3. **Fetches git commits** for that period
4. **Extracts changelog section** for the current version
5. **Extracts new commands & options** - diffs the `cmd/` directory between versions, parses cobra command definitions to identify new CLI commands with descriptions
6. **Extracts breaking changes** - mines the changelog for explicit breaking change entries
7. **Finds documentation context** - searches `README.md` and `docs/` for relevant command documentation
8. **Sends structured data to AI** - includes commits, changelog, new commands, and breaking changes
9. **AI generates narrative newsletter** - creates prose sections (not bullet lists) explaining:
   - Why new commands matter and how to use them
   - What breaking changes require and migration paths
   - Which features users should prioritize exploring
10. **Writes to `NEWSLETTER.md`**

The AI prompt specifically requests narrative paragraphs to help users understand workflow impacts and new features worth exploring, rather than just listing changes.

## Supported Models

| Provider | Example Models |
|----------|---------------|
| Anthropic | `claude-sonnet-4-20250514`, `claude-opus-4-20250514` |
| OpenAI | `gpt-4o`, `gpt-4o-mini`, `o1-preview`, `o3-mini` |

The script auto-detects the provider from the model name.
