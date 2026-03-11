# Protected Branch Workflow Example

This example demonstrates how to use beads with protected branches on platforms like GitHub, GitLab, and Bitbucket.

## Scenario

You have a repository with:
- Protected `main` branch (requires pull requests)
- Multiple developers/AI agents working on issues
- Desire to track issues in git without bypassing branch protection

## Solution

Use beads' separate sync branch feature to commit issue metadata to a dedicated branch (e.g., `beads-metadata`), then periodically merge via pull request.

## Quick Demo

### 1. Setup (One Time)

```bash
# Clone this repo or create a new one
git init my-project
cd my-project

# Initialize beads with separate sync branch
bd init --branch beads-metadata --quiet

# Verify configuration
bd config get sync.branch
# Output: beads-metadata
```

### 2. Create Issues (Agent Workflow)

```bash
# AI agent creates issues normally
bd create "Implement user authentication" -t feature -p 1
bd create "Add login page" -t task -p 1
bd create "Write auth tests" -t task -p 2

# Link tasks to parent feature
bd link bd-XXXXX --blocks bd-YYYYY  # auth blocks login
bd link bd-XXXXX --blocks bd-ZZZZZ  # auth blocks tests

# Start work
bd update bd-XXXXX --claim
```

**Note:** Replace `bd-XXXXX` etc. with actual issue IDs created above.

### 3. Auto-Sync (Server Mode)

```bash
# Start Dolt server with auto-commit
bd config set dolt.auto-commit on
bd dolt start

# All issue changes are now automatically committed to beads-metadata branch
```

Check what's been committed:

```bash
# View commits on sync branch
git log beads-metadata --oneline | head -5

# View diff between main and sync branch
git log main..beads-metadata --oneline
```

### 4. Manual Sync (Without Server)

If you're not using the Dolt server:

```bash
# Create or update issues
bd create "Fix bug in login" -t bug -p 0
bd update bd-XXXXX --status closed

# Manually push to remote
bd dolt push

# Verify commit
git log beads-metadata -1
```

### 5. Merge to Main (Human Review)

Option 1: Via pull request (recommended):

```bash
# Push sync branch
git push origin beads-metadata

# Create PR on GitHub
gh pr create --base main --head beads-metadata \
  --title "Update issue metadata" \
  --body "Automated issue tracker updates from beads"

# After PR is approved and merged:
git checkout main
git pull
bd dolt pull  # Pull merged changes into local database
```

Option 2: Direct merge (if you have push access):

```bash
# Preview merge
git log main..beads-metadata --oneline

# Perform merge
git checkout main
git merge beads-metadata --no-ff
git push
bd dolt pull  # Pull merged changes into local database
```

### 6. Multi-Clone Sync

If you have multiple clones or agents:

```bash
# Clone 1: Create issue
bd create "New feature" -t feature -p 1
bd dolt push  # Push to remote
git push origin beads-metadata

# Clone 2: Pull changes
git fetch origin beads-metadata
bd dolt pull  # Pull from remote into local database
bd list  # See the new feature issue
```

## Workflow Summary

```
┌─────────────────┐
│  Agent creates  │
│  or updates     │
│  issues         │
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│  Dolt server    │
│  (or manual     │
│  sync) commits  │
│  to beads-      │
│  metadata       │
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│  Periodically   │
│  merge to main  │
│  via PR         │
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│  All clones     │
│  pull and       │
│  import         │
└─────────────────┘
```

## Directory Structure

When using separate sync branch, your repo will have:

```
my-project/
├── .git/
│   ├── beads-worktrees/       # Hidden worktree directory
│   │   └── beads-metadata/    # Lightweight checkout of sync branch
│   │       └── .beads/
│   │           └── dolt/
│   └── ...
├── .beads/                    # Main beads directory (in your workspace)
│   ├── dolt/                  # Dolt database (source of truth)
│   └── config.yaml            # Beads configuration
├── src/                       # Your application code
│   └── ...
└── README.md
```

**Key points:**
- `.git/beads-worktrees/` is hidden from your main workspace
- Only `.beads/` is checked out in the worktree (sparse checkout)
- Your `src/` code is never affected by beads commits
- Minimal disk overhead (~few MB for worktree)

## Tips

### For Humans

- **Review before merging:** Use `git log main..beads-metadata --oneline` to see what changed
- **Batch merges:** Don't need to merge after every issue - merge when convenient
- **PR descriptions:** Link to specific issues in PR body for context

### For AI Agents

- **No workflow changes:** Agents use `bd create`, `bd update`, etc. as normal
- **Let the Dolt server handle it:** With auto-commit enabled, agents don't think about sync
- **Session end:** Run `bd dolt push` at end of session to ensure everything is pushed

### Troubleshooting

**"Merge conflicts during sync"**

Dolt handles merges natively using three-way merge. If conflicts occur:
1. Run `bd sql "SELECT * FROM dolt_conflicts"` to view them
2. Resolve with `bd sql "CALL dolt_conflicts_resolve('--ours')"` or `'--theirs'`
3. Complete with `bd dolt push`

**"Worktree doesn't exist"**

The Dolt server creates it automatically on first commit. To create manually:
```bash
bd config get sync.branch  # Verify it's set
bd dolt stop && bd dolt start              # Server will create worktree
```

**"Changes not syncing"**

Make sure:
- `bd config get sync.branch` returns the same value on all clones
- Dolt server is running: `bd doctor`
- Both clones have fetched: `git fetch origin beads-metadata`

## Advanced: GitHub Actions Integration

Automate the merge process with GitHub Actions:

```yaml
name: Auto-Merge Beads Metadata
on:
  schedule:
    - cron: '0 0 * * *'  # Daily at midnight
  workflow_dispatch:

jobs:
  merge-beads:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
        with:
          fetch-depth: 0

      - name: Install bd
        run: curl -fsSL https://raw.githubusercontent.com/steveyegge/beads/main/scripts/install.sh | bash

      - name: Check for changes
        id: check
        run: |
          git fetch origin beads-metadata
          if git diff --quiet main origin/beads-metadata -- .beads/; then
            echo "has_changes=false" >> $GITHUB_OUTPUT
          else
            echo "has_changes=true" >> $GITHUB_OUTPUT
          fi

      - name: Create PR
        if: steps.check.outputs.has_changes == 'true'
        run: |
          gh pr create --base main --head beads-metadata \
            --title "Update issue metadata" \
            --body "Automated issue tracker updates from beads" \
            || echo "PR already exists"
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

## See Also

- [docs/PROTECTED_BRANCHES.md](../../docs/PROTECTED_BRANCHES.md) - Complete guide
- [AGENTS.md](../../AGENTS.md) - Agent integration instructions
- [docs/QUICKSTART.md](../../docs/QUICKSTART.md) - `bd dolt push` / `bd dolt pull` usage
