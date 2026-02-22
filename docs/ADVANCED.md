# Advanced bd Features

This guide covers advanced features for power users and specific use cases.

## Table of Contents

- [Renaming Prefix](#renaming-prefix)
- [Merging Duplicate Issues](#merging-duplicate-issues)
- [Git Worktrees](#git-worktrees)
- [Database Redirects](#database-redirects)
- [Handling Import Collisions](#handling-import-collisions)
- [Custom Git Hooks](#custom-git-hooks)
- [Extensible Database](#extensible-database)
- [Architecture: Storage, RPC, and MCP](#architecture-storage-rpc-and-mcp)

## Renaming Prefix

Change the issue prefix for all issues in your database. This is useful if your prefix is too long or you want to standardize naming.

```bash
# Preview changes without applying
bd rename-prefix kw- --dry-run

# Rename from current prefix to new prefix
bd rename-prefix kw-

# JSON output
bd rename-prefix kw- --json
```

The rename operation:
- Updates all issue IDs (e.g., `knowledge-work-1` → `kw-1`)
- Updates all text references in titles, descriptions, design notes, etc.
- Updates dependencies and labels
- Updates the counter table and config

**Prefix validation rules:**
- Max length: 8 characters
- Allowed characters: lowercase letters, numbers, hyphens
- Must start with a letter
- Must end with a hyphen (or will be trimmed to add one)
- Cannot be empty or just a hyphen

Example workflow:
```bash
# You have issues like knowledge-work-1, knowledge-work-2, etc.
bd list  # Shows knowledge-work-* issues

# Preview the rename
bd rename-prefix kw- --dry-run

# Apply the rename
bd rename-prefix kw-

# Now you have kw-1, kw-2, etc.
bd list  # Shows kw-* issues
```

## Duplicate Detection

Find issues with identical content using automated duplicate detection:

```bash
# Find all content duplicates in the database
bd duplicates

# Show duplicates in JSON format
bd duplicates --json

# Automatically merge all duplicates
bd duplicates --auto-merge

# Preview what would be merged
bd duplicates --dry-run

# Detect duplicates during import
bd import -i issues.jsonl --dedupe-after
```

**How it works:**
- Groups issues by content hash (title, description, design, acceptance criteria)
- Only groups issues with matching status (open with open, closed with closed)
- Chooses merge target by reference count (most referenced) or smallest ID
- Reports duplicate groups with suggested merge commands

**Example output:**

```
🔍 Found 3 duplicate group(s):

━━ Group 1: Fix authentication bug
→ bd-10 (open, P1, 5 references)
  bd-42 (open, P1, 0 references)
  Suggested: bd merge bd-42 --into bd-10

💡 Run with --auto-merge to execute all suggested merges
```

**AI Agent Workflow:**

1. **Periodic scans**: Run `bd duplicates` to check for duplicates
2. **During import**: Use `--dedupe-after` to detect duplicates after collision resolution
3. **Auto-merge**: Use `--auto-merge` to automatically consolidate duplicates
4. **Manual review**: Use `--dry-run` to preview merges before executing

## Merging Duplicate Issues

Consolidate duplicate issues into a single issue while preserving dependencies and references:

```bash
# Merge bd-42 and bd-43 into bd-41
bd merge bd-42 bd-43 --into bd-41

# Merge multiple duplicates at once
bd merge bd-10 bd-11 bd-12 --into bd-10

# Preview merge without making changes
bd merge bd-42 bd-43 --into bd-41 --dry-run

# JSON output
bd merge bd-42 bd-43 --into bd-41 --json
```

**What the merge command does:**
1. **Validates** all issues exist and prevents self-merge
2. **Closes** source issues with reason `Merged into bd-X`
3. **Migrates** all dependencies from source issues to target
4. **Updates** text references across all issue descriptions, notes, design, and acceptance criteria

**Example workflow:**

```bash
# You discover bd-42 and bd-43 are duplicates of bd-41
bd show bd-41 bd-42 bd-43

# Preview the merge
bd merge bd-42 bd-43 --into bd-41 --dry-run

# Execute the merge
bd merge bd-42 bd-43 --into bd-41
# ✓ Merged 2 issue(s) into bd-41

# Verify the result
bd show bd-41  # Now has dependencies from bd-42 and bd-43
bd dep tree bd-41  # Shows unified dependency tree
```

**Important notes:**
- Source issues are permanently closed (status: `closed`)
- All dependencies pointing to source issues are redirected to target
- Text references like "see bd-42" are automatically rewritten to "see bd-41"
- Operation cannot be undone (but git history preserves the original state)
**AI Agent Workflow:**

When agents discover duplicate issues, they should:
1. Search for similar issues: `bd list --json | grep "similar text"`
2. Compare issue details: `bd show bd-41 bd-42 --json`
3. Merge duplicates: `bd merge bd-42 --into bd-41`
4. File a discovered-from issue if needed: `bd create "Found duplicates during bd-X" --deps discovered-from:bd-X`

## Git Worktrees

Git worktrees work with bd. Each worktree can have its own `.beads` directory, or worktrees can share a database via redirects (see [Database Redirects](#database-redirects)).

**With Dolt backend:** Each worktree operates directly on the database — no daemon coordination needed. Use `bd sync` to synchronize JSONL with git when ready.

**With Dolt server mode:** Multiple worktrees can connect to the same Dolt server for concurrent access without conflicts.

## Database Redirects

Multiple git clones can share a single beads database using redirect files. This is useful for:
- Multi-agent setups where several clones work on the same issues
- Development environments with multiple checkout directories
- Avoiding duplicate databases across clones

### Setting Up a Redirect

Create a `.beads/redirect` file pointing to the shared database location:

```bash
# In your secondary clone
mkdir -p .beads
echo "../main-clone/.beads" > .beads/redirect

# Or use an absolute path
echo "/path/to/shared/.beads" > .beads/redirect
```

The redirect file should contain a single path (relative or absolute) to the target `.beads` directory.

**Example setup:**
```
repo/
├── main-clone/
│   └── .beads/
│       └── beads.db      ← Actual database
├── agent-1/
│   └── .beads/
│       └── redirect      ← Points to ../main-clone/.beads
└── agent-2/
    └── .beads/
        └── redirect      ← Points to ../main-clone/.beads
```

### Checking Active Location

Use `bd where` to see which database is actually being used:

```bash
bd where
# /path/to/main-clone/.beads
#   (via redirect from /path/to/agent-1/.beads)
#   prefix: bd
#   database: /path/to/main-clone/.beads/beads.db

bd where --json
# {"path": "...", "redirected_from": "...", "prefix": "bd", "database_path": "..."}
```

### Limitations

- **Single-level redirects only**: Redirect chains are not followed (A → B → C won't work)
- **Target must exist**: The redirect target directory must exist and contain a valid database

### When to Use Redirects

**Good use cases:**
- Multiple AI agents working on the same project
- Parallel development clones (feature work, bug fixes)
- Testing clones that should see production issues

**Not recommended for:**
- Separate projects (use separate databases)
- Long-lived forks (they should have their own issues)
- Git worktrees (each should have its own `.beads` directory)

## Handling Git Merge Conflicts

**With hash-based IDs (v0.20.1+), ID collisions are eliminated.** Different issues get different hash IDs, so concurrent creation doesn't cause conflicts.

### Understanding Same-ID Scenarios

When you encounter the same ID during import, it's an **update operation**, not a collision:

- Hash IDs are content-based and remain stable across updates
- Same ID + different fields = normal update to existing issue
- bd automatically applies updates when importing

**Preview changes before importing:**
```bash
# After git merge or pull
bd import -i .beads/issues.jsonl --dry-run

# Output shows:
# Exact matches (idempotent): 15
# New issues: 5
# Updates: 3
#
# Issues to be updated:
#   bd-a3f2: Fix authentication (changed: priority, status)
#   bd-b8e1: Add feature (changed: description)
```

### Git Merge Conflicts

The conflicts you'll encounter are **git merge conflicts** in the JSONL file when the same issue was modified on both branches (different timestamps/fields). This is not an ID collision.

**Resolution:**
```bash
# After git merge creates conflict
git checkout --theirs .beads/issues.jsonl  # Accept remote version
# OR
git checkout --ours .beads/issues.jsonl    # Keep local version
# OR manually resolve in editor (keep line with newer updated_at)

# Import the resolved JSONL
bd import -i .beads/issues.jsonl

# Commit the merge
git add .beads/issues.jsonl
git commit
```

### Advanced: Intelligent Merge Tools

For Git merge conflicts in `.beads/issues.jsonl`, consider using **[beads-merge](https://github.com/neongreen/mono/tree/main/beads-merge)** - a specialized merge tool by @neongreen that:

- Matches issues across conflicted JSONL files
- Merges fields intelligently (e.g., combines labels, picks newer timestamps)
- Resolves conflicts automatically where possible
- Leaves remaining conflicts for manual resolution
- Works as a Git/jujutsu merge driver

After using beads-merge to resolve the git conflict, just run `bd import` to update your database.

## Custom Git Hooks

Git hooks keep the JSONL file in sync with the Dolt database for git portability:

### Using the Installer (Recommended)

```bash
bd hooks install
```

This installs:
- **pre-commit** — Exports Dolt changes to JSONL and stages it
- **post-merge** — Imports pulled JSONL changes into Dolt (using branch-then-merge for cell-level conflict resolution)
- **post-checkout** — Imports JSONL after branch checkout

### Manual Setup

Create `.git/hooks/pre-commit`:
```bash
#!/bin/bash
bd export -o .beads/issues.jsonl
git add .beads/issues.jsonl
```

Create `.git/hooks/post-merge`:
```bash
#!/bin/bash
bd import -i .beads/issues.jsonl
```

Make hooks executable:
```bash
chmod +x .git/hooks/pre-commit .git/hooks/post-merge
```

See [DOLT.md](DOLT.md) for details on how hooks work with the Dolt backend.

## Extensible Database

> **Note:** Custom table extensions via `UnderlyingDB()` are a **SQLite-only** pattern.
> With the Dolt backend, build standalone integration tools using bd's CLI with `--json`
> flags, or use the JSONL files directly. See [EXTENDING.md](EXTENDING.md) for details.

For SQLite-backend users, you can extend bd with your own tables and queries:

- Add custom metadata to issues
- Build integrations with other tools
- Implement custom workflows
- Create reports and analytics

**See [EXTENDING.md](EXTENDING.md) for complete documentation.**

## Architecture: Storage, RPC, and MCP

Understanding the role of each component:

### Beads (Core)
- **Dolt database** (primary) — Version-controlled SQL, the source of truth for all issues, dependencies, labels
- **SQLite database** (legacy) — Still supported for simple single-user setups
- **Storage layer** — Interface-based CRUD operations, dependency resolution, collision detection
- **Business logic** — Ready work calculation, merge operations, import/export
- **CLI commands** — Direct database access via `bd` command

### RPC Layer (Dolt Server Mode)
- **Multi-writer access** — Connects to a running `dolt sql-server` for concurrent clients
- **Used in multi-agent setups** — Gas Town and similar environments where multiple agents write simultaneously
- **Not needed for single-user** — a single Dolt server handles all local operations

### MCP Server (Optional)
- **Protocol adapter** — Translates MCP calls to direct CLI invocations
- **Workspace routing** — Finds correct `.beads` directory based on working directory
- **Stateless** — Doesn't cache or store any issue data itself
- **Editor integration** — Makes bd available to Claude, Cursor, and other MCP clients

**Key principle**: All heavy lifting (dependency graphs, collision resolution, merge logic) happens in the core bd storage layer. The RPC and MCP layers are thin adapters.

## Next Steps

- **[README.md](../README.md)** - Core features and quick start
- **[TROUBLESHOOTING.md](TROUBLESHOOTING.md)** - Common issues and solutions
- **[FAQ.md](FAQ.md)** - Frequently asked questions
- **[CONFIG.md](CONFIG.md)** - Configuration system guide
- **[EXTENDING.md](EXTENDING.md)** - Database extension patterns
