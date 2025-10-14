# Git Hooks for Beads

Optional git hooks for immediate export/import of beads issues.

**NOTE**: As of bd v0.9+, **auto-sync is enabled by default!** These hooks are optional and provide:
- **Immediate export** (no 5-second debounce wait)
- **Guaranteed import** after every git operation
- **Extra safety** for critical workflows

## What These Hooks Do

- **pre-commit**: Exports SQLite → JSONL before every commit (immediate, no debounce)
- **post-merge**: Imports JSONL → SQLite after git pull/merge (guaranteed)
- **post-checkout**: Imports JSONL → SQLite after branch switching (guaranteed)

This keeps your `.beads/issues.jsonl` (committed to git) in sync with your local SQLite database (gitignored).

## Do You Need These Hooks?

**Most users don't need hooks anymore!** bd automatically:
- Exports after CRUD operations (5-second debounce)
- Imports when JSONL is newer than DB

**Install hooks if you:**
- Want immediate export (no waiting 5 seconds)
- Want guaranteed import after every git operation
- Need extra certainty for team workflows
- Prefer explicit automation over automatic behavior

## Installation

### Quick Install

```bash
cd /path/to/your/project
./examples/git-hooks/install.sh
```

The installer will prompt before overwriting existing hooks.

### Manual Install

```bash
# Copy hooks to .git/hooks/
cp examples/git-hooks/pre-commit .git/hooks/
cp examples/git-hooks/post-merge .git/hooks/
cp examples/git-hooks/post-checkout .git/hooks/

# Make them executable
chmod +x .git/hooks/pre-commit
chmod +x .git/hooks/post-merge
chmod +x .git/hooks/post-checkout
```

## Usage

Once installed, the hooks run automatically:

```bash
# Creating/updating issues
bd create "New feature" -p 1
bd update bd-1 --status in_progress

# Committing changes - hook exports automatically
git add .
git commit -m "Update feature"
# 🔗 Exporting beads issues to JSONL...
# ✓ Beads issues exported and staged

# Pulling changes - hook imports automatically
git pull
# 🔗 Importing beads issues from JSONL...
# ✓ Beads issues imported successfully

# Switching branches - hook imports automatically
git checkout feature-branch
# 🔗 Importing beads issues from JSONL...
# ✓ Beads issues imported successfully
```

## How It Works

### The Workflow

1. You work with bd commands (`create`, `update`, `close`)
2. Changes are stored in SQLite (`.beads/*.db`) - fast local queries
3. Before commit, hook exports to JSONL (`.beads/issues.jsonl`) - git-friendly
4. JSONL is committed to git (source of truth)
5. After pull/merge/checkout, hook imports JSONL back to SQLite
6. Your local SQLite cache is now in sync with git

### Why This Design?

**SQLite for speed**:
- Fast queries (dependency trees, ready work)
- Rich SQL capabilities
- Sub-100ms response times

**JSONL for git**:
- Clean diffs (one issue per line)
- Mergeable (independent lines)
- Human-readable
- AI-resolvable conflicts

Best of both worlds!

## Troubleshooting

### Hook not running

```bash
# Check if hook is executable
ls -l .git/hooks/pre-commit
# Should show -rwxr-xr-x

# Make it executable if needed
chmod +x .git/hooks/pre-commit
```

### Export/import fails

```bash
# Check if bd is in PATH
which bd

# Check if you're in a beads-initialized directory
bd list
```

### Merge conflicts in issues.jsonl

If you get merge conflicts in `.beads/issues.jsonl`:

1. Most conflicts are safe to resolve by keeping both sides
2. Each line is an independent issue
3. Look for `<<<<<<< HEAD` markers
4. Keep all lines that don't conflict
5. For actual conflicts on the same issue, choose the newest

Example conflict:

```
<<<<<<< HEAD
{"id":"bd-3","title":"Updated title","status":"closed","updated_at":"2025-10-12T10:00:00Z"}
=======
{"id":"bd-3","title":"Updated title","status":"in_progress","updated_at":"2025-10-12T09:00:00Z"}
>>>>>>> feature-branch
```

Resolution: Keep the HEAD version (newer timestamp).

After resolving:
```bash
git add .beads/issues.jsonl
git commit
bd import -i .beads/issues.jsonl  # Sync to SQLite
```

## Uninstalling

```bash
rm .git/hooks/pre-commit
rm .git/hooks/post-merge
rm .git/hooks/post-checkout
```

## Customization

### Skip hook for one commit

```bash
git commit --no-verify -m "Skip hooks"
```

### Add to existing hooks

If you already have git hooks, you can append to them:

```bash
# Append to existing pre-commit
cat examples/git-hooks/pre-commit >> .git/hooks/pre-commit
```

### Filter exports

Export only specific issues:

```bash
# Edit pre-commit hook, change:
bd export --format=jsonl -o .beads/issues.jsonl

# To:
bd export --format=jsonl --status=open -o .beads/issues.jsonl
```

## See Also

- [Git hooks documentation](https://git-scm.com/book/en/v2/Customizing-Git-Git-Hooks)
- [../../TEXT_FORMATS.md](../../TEXT_FORMATS.md) - JSONL merge strategies
- [../../GIT_WORKFLOW.md](../../GIT_WORKFLOW.md) - Design rationale
