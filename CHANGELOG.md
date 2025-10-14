# Changelog

All notable changes to the beads project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.9.2] - 2025-10-14

### Added
- **One-Command Dependency Creation**: `--deps` flag for `bd create` (#18)
  - Create issues with dependencies in a single command
  - Format: `--deps type:id` or just `--deps id` (defaults to blocks)
  - Multiple dependencies: `--deps discovered-from:bd-20,blocks:bd-15`
  - Whitespace-tolerant parsing
  - Particularly useful for AI agents creating discovered-from issues
- **External Reference Tracking**: `external_ref` field for linking to external trackers
  - Link bd issues to GitHub, Jira, Linear, etc.
  - Example: `bd create "Issue" --external-ref gh-42`
  - `bd update` supports updating external references
  - Tracked in JSONL for git portability
- **Metadata Storage**: Internal metadata table for system state
  - Stores import hash for idempotent auto-import
  - Enables future extensibility for system preferences
  - Auto-migrates existing databases
- **Windows Support**: Complete Windows 11 build instructions (#10)
  - Tested with mingw-w64
  - Full CGo support documented
  - PATH setup instructions
- **Go Extension Example**: Complete working example of database extensions (#15)
  - Demonstrates custom table creation
  - Shows cross-layer queries joining with issues
  - Includes test suite and documentation
- **Issue Type Display**: `bd list` now shows issue type in output (#17)
  - Better visibility: `bd-1 [P1] [bug] open`
  - Helps distinguish bugs from features at a glance

### Fixed
- **Critical**: Dependency tree deduplication for diamond dependencies (bd-85, #1)
  - Fixed infinite recursion in complex dependency graphs
  - Prevents duplicate nodes at same level
  - Handles multiple blockers correctly
- **Critical**: Hash-based auto-import replaces mtime comparison (bd-84)
  - Git pull updates mtime but may not change content
  - Now uses SHA256 hash to detect actual changes
  - Prevents unnecessary imports after git operations
- **Critical**: Parallel issue creation race condition (PR #8, bd-66)
  - Multiple processes could generate same ID
  - Replaced in-memory counter with atomic database counter
  - Syncs counters after import to prevent collisions
  - Comprehensive test coverage

### Changed
- Auto-import now uses content hash instead of modification time
- Dependency tree visualization improved for complex graphs
- Better error messages for dependency operations

### Community
- Merged PR #8: Parallel issue creation fix
- Merged PR #10: Windows build instructions
- Merged PR #12: Fix quickstart EXTENDING.md link
- Merged PR #14: Better enable Go extensions
- Merged PR #15: Complete Go extension example
- Merged PR #17: Show issue type in list output

## [0.9.1] - 2025-10-14

### Added
- **Incremental JSONL Export**: Major performance optimization
  - Dirty issue tracking system to only export changed issues
  - Auto-flush with 5-second debounce after CRUD operations
  - Automatic import when JSONL is newer than database
  - `--no-auto-flush` and `--no-auto-import` flags for manual control
  - Comprehensive test coverage for auto-flush/import
- **ID Space Partitioning**: Explicit ID assignment for parallel workers
  - `bd create --id worker1-100` for controlling ID allocation
  - Enables multiple agents to work without conflicts
  - Documented in CLAUDE.md for agent workflows
- **Auto-Migration System**: Seamless database schema upgrades
  - Automatically adds dirty_issues table to existing databases
  - Silent migration on first access after upgrade
  - No manual intervention required

### Fixed
- **Critical**: Race condition in dirty tracking (TOCTOU bug)
  - Could cause data loss during concurrent operations
  - Fixed by tracking specific exported IDs instead of clearing all
- **Critical**: Export with filters cleared all dirty issues
  - Status/priority filters would incorrectly mark non-matching issues as clean
  - Now only clears issues that were actually exported
- **Bug**: Malformed ID detection never worked
  - SQLite CAST returns 0 for invalid strings, not NULL
  - Now correctly detects non-numeric ID suffixes like "bd-abc"
  - No false positives on legitimate zero-prefixed IDs
- **Bug**: Inconsistent dependency dirty marking
  - Duplicated 20+ lines of code in AddDependency/RemoveDependency
  - Refactored to use shared markIssuesDirtyTx() helper
- Fixed unchecked error in import.go when unmarshaling JSON
- Fixed unchecked error returns in test cleanup code
- Removed duplicate test code in dependencies_test.go
- Fixed Go version in go.mod (was incorrectly set to 1.25.2)

### Changed
- Export now tracks which specific issues were exported
- ClearDirtyIssuesByID() added (ClearDirtyIssues() deprecated with race warning)
- Dependency operations use shared dirty-marking helper (DRY)

### Performance
- Incremental export: Only writes changed issues (vs full export)
- Regex caching in ID replacement: 1.9x performance improvement
- Automatic debounced flush prevents excessive I/O

## [0.9.0] - 2025-10-12

### Added
- **Collision Resolution System**: Automatic ID remapping for import collisions
  - Reference scoring algorithm to minimize updates during remapping
  - Word-boundary regex matching to prevent false replacements
  - Automatic updating of text references and dependencies
  - `--resolve-collisions` flag for safe branch merging
  - `--dry-run` flag to preview collision detection
- **Export/Import with JSONL**: Git-friendly text format
  - Dependencies embedded in JSONL for complete portability
  - Idempotent import (exact matches detected)
  - Collision detection (same ID, different content)
- **Ready Work Algorithm**: Find issues with no open blockers
  - `bd ready` command shows unblocked work
  - `bd blocked` command shows what's waiting
- **Dependency Management**: Four dependency types
  - `blocks`: Hard blocker (affects ready work)
  - `related`: Soft relationship
  - `parent-child`: Epic/subtask hierarchy
  - `discovered-from`: Track issues discovered during work
- **Database Discovery**: Auto-find database in project hierarchy
  - Walks up directory tree like git
  - Supports `$BEADS_DB` environment variable
  - Falls back to `~/.beads/default.db`
- **Comprehensive Documentation**:
  - README.md with 900+ lines of examples and FAQs
  - CLAUDE.md for AI agent integration patterns
  - SECURITY.md with security policy and best practices
  - TEXT_FORMATS.md analyzing JSONL approach
  - EXTENDING.md for database extension patterns
  - GIT_WORKFLOW.md for git integration
- **Examples**: Real-world integration patterns
  - Python agent implementation
  - Bash agent script
  - Git hooks for automatic export/import
  - Branch merge workflow with collision resolution
  - Claude Desktop MCP integration (coming soon)

### Changed
- Switched to JSONL as source of truth (from binary SQLite)
- SQLite database now acts as ephemeral cache
- Issue IDs generated with numerical max (not alphabetical)
- Export sorts issues by ID for consistent git diffs

### Security
- SQL injection protection via allowlisted field names
- Input validation for all issue fields
- File path validation for database operations
- Warnings about not storing secrets in issues

## [0.1.0] - Initial Development

### Added
- Core issue tracking (create, update, list, show, close)
- SQLite storage backend
- Dependency tracking with cycle detection
- Label support
- Event audit trail
- Full-text search
- Statistics and reporting
- `bd init` for project initialization
- `bd quickstart` interactive tutorial

---

## Version History

- **0.9.2** (2025-10-14): Community PRs, critical bug fixes, and --deps flag
- **0.9.1** (2025-10-14): Performance optimization and critical bug fixes
- **0.9.0** (2025-10-12): Pre-release polish and collision resolution
- **0.1.0**: Initial development version

## Upgrade Guide

### Upgrading to 0.9.2

No breaking changes. All changes are backward compatible:
- **--deps flag**: Optional new feature for `bd create`
- **external_ref**: Optional field, existing issues unaffected
- **Metadata table**: Auto-migrates on first use
- **Bug fixes**: All critical fixes are transparent to users

Simply pull the latest version and rebuild:
```bash
go install github.com/steveyegge/beads/cmd/bd@latest
# or
git pull && go build -o bd ./cmd/bd
```

### Upgrading to 0.9.1

No breaking changes. All changes are backward compatible:
- **Auto-migration**: The dirty_issues table is automatically added to existing databases
- **Auto-flush/import**: Enabled by default, improves workflow (can disable with flags if needed)
- **ID partitioning**: Optional feature, use `--id` flag only if needed for parallel workers

If you're upgrading from 0.9.0, simply pull the latest version. Your existing database will be automatically migrated on first use.

### Upgrading to 0.9.0

No breaking changes. The JSONL export format is backward compatible.

If you have issues in your database:
1. Run `bd export -o .beads/issues.jsonl` to create the text file
2. Commit `.beads/issues.jsonl` to git
3. Add `.beads/*.db` to `.gitignore`

New collaborators can clone the repo and run:
```bash
bd import -i .beads/issues.jsonl
```

The SQLite database will be automatically populated from the JSONL file.

## Future Releases

See open issues tagged with milestone markers for planned features in upcoming releases.

For version 1.0, see: `bd dep tree bd-8` (the 1.0 milestone epic)
