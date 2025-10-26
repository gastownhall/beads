# Changelog

All notable changes to the beads project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.17.2] - 2025-10-25

### Added
- **Configurable Sort Policy**: `bd ready --sort` flag for work queue ordering (bd-147)
  - `hybrid` (default): Priority-weighted by staleness
  - `priority`: Strict priority ordering for autonomous systems
  - `oldest`: Pure FIFO for long-tail work
- **Release Automation**: New scripts for streamlined releases
  - `scripts/release.sh`: Full automated release (version bump, tests, tag, Homebrew, install)
  - `scripts/update-homebrew.sh`: Automated Homebrew formula updates

### Fixed
- **Critical**: Database reinitialization test re-landed with CI fixes (bd-130)
  - Windows: Fixed git path handling (forward slash normalization)
  - Nix: Skip test when git unavailable
  - JSON: Increased scanner buffer to 64MB for large issues
- **Bug**: Stale daemon socket detection (bd-137)
  - MCP server now health-checks cached connections before use
  - Auto-reconnect with exponential backoff on stale sockets
  - Handles daemon restarts/upgrades gracefully
- **Linting**: Fixed all errcheck warnings in production code (bd-58)
  - Proper error handling for database resources and transactions
  - Graceful EOF handling in interactive input
- **Linting**: Fixed revive style issues (bd-56)
  - Removed unused parameters, renamed builtin shadowing
- **Linting**: Fixed goconst warnings (bd-116)

## [0.17.0] - 2025-10-24

### Added
- **Git Hooks**: Automatic installation prompt during `bd init` (bd-51)
  - Eliminates race condition between auto-flush and git commits
  - Pre-commit hook: Flushes pending changes immediately before commit
  - Post-merge hook: Imports updated JSONL after pull/merge
  - Optional installation with Y/n prompt (defaults to yes)
  - See [examples/git-hooks/README.md](examples/git-hooks/README.md) for details
- **Duplicate Detection**: New `bd duplicates` command for finding and merging duplicate issues (bd-119, bd-203)
  - Automated duplicate detection with content-based matching
  - `--auto-merge` flag for batch merging duplicates
  - `--dry-run` mode to preview merges before execution
  - Helps maintain database cleanliness after imports
- **External Reference Import**: Smart import matching using `external_ref` field (bd-66-74, GH #142)
  - Issues with `external_ref` match by reference first, not content
  - Enables hybrid workflows with Jira, GitHub, Linear
  - Updates existing issues instead of creating duplicates
  - Database index on `external_ref` for fast lookups
- **Multi-Database Warning**: Detect and warn about nested beads databases (bd-75)
  - Prevents accidental creation of multiple databases in hierarchy
  - Helps users avoid confusion about which database is active

### Fixed
- **Critical**: Database reinitialization data loss bug (bd-130, DATABASE_REINIT_BUG.md)
  - Fixed bug where removing `.beads/` and running `bd init` would lose git-tracked issues
  - Now correctly imports from JSONL during initialization
  - Added comprehensive tests (later reverted due to CI issues on Windows/Nix)
- **Critical**: Foreign key constraint regression (bd-62, GH #144)
  - Pinned modernc.org/sqlite to v1.38.2 to avoid FK violations
  - Prevents database corruption from upstream regression
- **Critical**: Install script safety (GH #143 by @marcodelpin)
  - Prevents shell corruption from directory deletion during install
  - Restored proper error codes for safer installation
- **Bug**: Daemon auto-start reliability (bd-137)
  - Daemon now responsive immediately, runs initial sync in background
  - Fixes timeout issues when git pull is slow
  - Skip daemon-running check for forked child process
- **Bug**: Dependency timestamp churn during auto-import (bd-45, bd-137)
  - Auto-import no longer updates timestamps on unchanged dependencies
  - Eliminates perpetually dirty JSONL from metadata changes
- **Bug**: Import reporting accuracy (bd-49, bd-88)
  - `bd import` now correctly reports "X updated, Y unchanged" instead of "0 updated"
  - Better visibility into import operation results
- **Bug**: Memory database handling
  - Fixed :memory: database connection with shared cache mode
  - Proper URL construction for in-memory testing

### Changed
- **Removed**: Deprecated `bd repos` command
  - Global daemon architecture removed in favor of per-project daemons
  - Eliminated cross-project database confusion
- **Documentation**: Major reorganization and improvements
  - Condensed README, created specialized docs (QUICKSTART.md, ADVANCED.md, etc.)
  - Enhanced "Why not GitHub Issues?" FAQ section
  - Added Beadster to Community & Ecosystem section

### Performance
- Test coverage improvements: 46.0% → 57.7% (+11.7%)
  - Added tests for RPC, storage, cmd/bd helpers
  - New test files: coverage_test.go, helpers_test.go, epics_test.go

### Community
- Community contribution by @marcodelpin (install script safety fixes)
- Dependabot integration for automated dependency updates

## [0.16.0] - 2025-10-23

### Added
- **Automated Releases**: GoReleaser workflow for cross-platform binaries (bd-46)
  - Automatic GitHub releases on version tags
  - Linux, macOS, Windows binaries for amd64 and arm64
  - Checksums and changelog generation included
- **PyPI Automation**: Automated MCP server publishing to PyPI
  - GitHub Actions workflow publishes beads-mcp on version tags
  - Eliminates manual PyPI upload step
- **Sandbox Mode**: `--sandbox` flag for Claude Code integration (bd-35)
  - Isolated environment for AI agent experimentation
  - Prevents production database modifications during testing

### Fixed
- **Critical**: Idempotent import timestamp churn (bd-84)
  - Prevents timestamp updates when issue content unchanged
  - Reduces JSONL churn and git noise from repeated imports
- **Bug**: Windows CI test failures (bd-60, bd-99)
  - Fixed path separator issues and file handling on Windows
  - Skipped flaky tests to stabilize CI

### Changed
- **Configuration Migration**: Unified config management with Viper (bd-40-44, bd-78)
  - Migrated from manual env var handling to Viper
  - Bound all global flags to Viper for consistency
  - Kept `bd config` independent from Viper for modularity
  - Added comprehensive configuration tests
- **Documentation Refactor**: Improved documentation structure
  - Condensed main README
  - Created specialized guides (QUICKSTART.md, CONFIG.md, etc.)
  - Enhanced FAQ and community sections

### Testing
- Hardened `issueDataChanged` with type-safe comparisons
- Improved test isolation and reliability

## [0.15.0] - 2025-10-23

### Added
- **Configuration System**: New `bd config` command for managing configuration (GH #115)
  - Environment variable definitions with validation
  - Configuration file support (TOML/YAML/JSON)
  - Get/set/list/unset commands for user-friendly management
  - Validation and type checking for config values
  - Documentation in CONFIG.md

### Fixed
- **MCP Server**: Smart routing for lifecycle status changes in `update` tool (GH #123)
  - `update(status="closed")` now routes to `close()` tool to respect approval workflows
  - `update(status="open")` now routes to `reopen()` tool to respect approval workflows
  - Prevents bypass of Claude Code approval settings for lifecycle events
  - bd CLI remains unopinionated; routing happens only in MCP layer
  - Users can now safely auto-approve benign updates (priority, notes) without exposing closure bypass

## [0.14.0] - 2025-10-22

### Added
- **Lifecycle Safety Documentation**: Complete documentation for UnderlyingDB() usage (bd-64)
  - Added tracking guidelines for database lifecycle safety
  - Documented transaction management best practices
  - Prevents UAF (use-after-free) bugs in extensions

### Fixed
- **Critical**: Git worktree detection and warnings (bd-73)
  - Added automatic detection when running in git worktrees
  - Displays prominent warning if daemon mode is active in worktree
  - Prevents daemon from committing/pushing to wrong branch
  - Documents `--no-daemon` flag as solution for worktree users
- **Critical**: Multiple daemon race condition (bd-54)
  - Implemented file locking (`daemon.lock`) to prevent multiple daemons per repository
  - Uses `flock` on Unix, `LockFileEx` on Windows for process-level exclusivity
  - Lock held for daemon lifetime, automatically released on exit
  - Eliminates race conditions in concurrent daemon start attempts
  - Backward compatible: Falls back to PID check for pre-lock daemons during upgrades
- **Bug**: daemon.lock tracked in git
  - Removed daemon.lock from git tracking
  - Added to .gitignore to prevent future commits
- **Bug**: Regression in Nix Flake (#110)
  - Fixed flake build issues
  - Restored working Nix development environment

### Changed
- UnderlyingDB() deprecated for most use cases
  - New UnderlyingConn(ctx) provides safer scoped access
  - Reduced risk of UAF bugs in database extensions
  - Updated EXTENDING.md with migration guide

### Documentation
- Complete release process documentation in RELEASING.md
- Enhanced EXTENDING.md with lifecycle safety patterns
- Added UnderlyingDB() tracking guidelines

## [0.11.0] - 2025-10-22

### Added
- **Issue Merging**: New `bd merge` command for consolidating duplicate issues (bd-7, bd-11-17)
  - Merge multiple source issues into a single target issue
  - Automatically migrates all dependencies and dependents to target
  - Updates text references (bd-X mentions) across all issue fields
  - Closes source issues with "Merged into bd-Y" reason
  - Supports `--dry-run` for validation without changes
  - Example: `bd merge bd-42 bd-43 --into bd-41`
- **Multi-ID Operations**: Batch operations for increased efficiency (bd-195, #101)
  - `bd update`: Update multiple issues at once
  - `bd show`: View multiple issues in single call
  - `bd label add/remove`: Apply labels to multiple issues
  - `bd close`: Close multiple issues with one command
  - `bd reopen`: Reopen multiple issues together
  - Example: `bd close bd-1 bd-2 bd-3 --reason "Done"`
- **Daemon RPC Improvements**: Enhanced sync operations (bd-2)
  - `bd sync` now works correctly in daemon mode
  - Export operations properly supported via RPC
  - Prevents database access conflicts during sync
- **Acceptance Criteria Alias**: Added `--acceptance-criteria` flag (bd-228, #102)
  - Backward-compatible alias for `--acceptance` in `bd update`
  - Improves clarity and matches field name

### Fixed
- **Critical**: Test isolation and database pollution (bd-1, bd-15, bd-19, bd-52)
  - Comprehensive test isolation ensuring tests never pollute production database
  - Fixed stress test issues writing 1000+ test issues to production
  - Quarantined RPC benchmarks to prevent pollution
  - Added database isolation canary tests
- **Critical**: Daemon cache staleness (bd-49)
  - Daemon now detects external database modifications via mtime check
  - Prevents serving stale data after external `bd import`, `rm bd.db`, etc.
  - Cache automatically invalidates when DB file changes
- **Critical**: Counter desync after deletions (bd-49)
  - Issue counters now sync correctly after bulk deletions
  - Prevents ID gaps and counter drift
- **Critical**: Labels and dependencies not persisted in daemon mode (#101)
  - Fixed label operations failing silently in daemon mode
  - Fixed dependency operations not saving in daemon mode
  - Both now correctly propagate through RPC layer
- **Daemon sync support**: `bd sync` command now works in daemon mode (bd-2)
  - Previously crashed with nil pointer when daemon running
  - Export operations now properly routed through RPC
- **Acceptance flag normalization**: Unified `--acceptance` flag behavior (bd-228, #102)
  - Added `--acceptance-criteria` as clearer alias
  - Both flags work identically for backward compatibility
- **Auto-import Git conflicts**: Better detection of merge conflicts (bd-270)
  - Auto-import detects and warns about unresolved Git merge conflicts
  - Prevents importing corrupted JSONL with conflict markers
  - Clear instructions for resolving conflicts

### Changed
- **BREAKING**: Removed global daemon socket fallback (bd-231)
  - Each project now must use its own local daemon (.beads/bd.sock)
  - Prevents cross-project daemon connections and database pollution
  - Migration: Stop any global daemon and restart with `bd daemon` in each project
  - Warning displayed if old global socket (~/.beads/bd.sock) is found
- **Database cleanup**: Project database cleaned from 1000+ to 55 issues
  - Removed accumulated test pollution from stress testing
  - Renumbered issues for clean ID space (bd-1 through bd-55)
  - Better test isolation prevents future pollution

### Deprecated
- Global daemon socket support (see BREAKING change above)

## [0.10.0] - 2025-10-20

### Added
- **Agent Onboarding**: New `bd onboard` command for agent-first documentation (bd-173)
  - Outputs structured instructions for agents to integrate bd into documentation
  - Bootstrap workflow: Add 'BEFORE ANYTHING ELSE: run bd onboard' to AGENTS.md
  - Agent adapts instructions to existing project structure
  - More agentic approach vs. direct string replacement
  - Updates README with new bootstrap workflow

## [0.9.11] - 2025-10-20

### Added
- **Labels Documentation**: Comprehensive LABELS.md guide (bd-159, bd-163)
  - Complete label system documentation with workflows and best practices
  - Common label patterns (components, domains, size, quality gates, releases)
  - Advanced filtering techniques and integration examples
  - Added Labels section to README with quick reference

### Fixed
- **Critical**: MCP server crashes on None/null responses (bd-172, fixes #79)
  - Added null safety checks in `list_issues()`, `ready()`, and `stats()` methods
  - Returns empty arrays/dicts instead of crashing on None responses
  - Prevents TypeError when daemon returns empty results

## [0.9.10] - 2025-10-18

### Added
- **Label Filtering**: Enhanced `bd list` command with label-based filtering (bd-161)
  - `--label` (or `-l`): Filter by multiple labels with AND semantics (must have ALL)
  - `--label-any`: Filter by multiple labels with OR semantics (must have AT LEAST ONE)
  - Examples:
    - `bd list --label backend,urgent`: Issues with both 'backend' AND 'urgent'
    - `bd list --label-any frontend,backend`: Issues with either 'frontend' OR 'backend'
  - Works in both daemon and direct modes
  - Includes comprehensive test coverage
- **Log Rotation**: Automatic daemon log rotation with configurable limits (bd-154)
  - Prevents unbounded log file growth for long-running daemons
  - Configurable via environment variables: `BEADS_DAEMON_LOG_MAX_SIZE`, `BEADS_DAEMON_LOG_MAX_BACKUPS`, `BEADS_DAEMON_LOG_MAX_AGE`
  - Optional compression of rotated logs
  - Defaults: 10MB max size, 3 backups, 7 day retention, compression enabled
- **Batch Deletion**: Enhanced `bd delete` command with batch operations (bd-127)
  - Delete multiple issues at once: `bd delete bd-1 bd-2 bd-3 --force`
  - Read from file: `bd delete --from-file deletions.txt --force`
  - Dry-run mode: `--dry-run` to preview deletions before execution
  - Cascade mode: `--cascade` to recursively delete all dependents
  - Force mode: `--force` to orphan dependents instead of failing
  - Atomic transactions: all deletions succeed or none do
  - Comprehensive statistics: tracks deleted issues, dependencies, labels, and events

### Fixed
- **Critical**: `bd list --status all` showing 0 issues (bd-148)
  - Status filter now treats "all" as special value meaning "show all statuses"
  - Previously treated "all" as literal status value, matching no issues

## [0.9.9] - 2025-10-17

### Added
- **Daemon RPC Architecture**: Production-ready RPC protocol for client-daemon communication (bd-110, bd-111, bd-112, bd-114, bd-117)
  - Unix socket-based RPC enables faster command execution via long-lived daemon process
  - Automatic client detection with graceful fallback to direct mode
  - Serializes SQLite writes and batches git operations to prevent concurrent access issues
  - Resolves database corruption, git lock contention, and ID counter conflicts with multiple agents
  - Comprehensive integration tests and stress testing with 4+ concurrent agents
- **Issue Deletion**: `bd delete` command for removing issues with comprehensive cleanup
  - Safely removes issues from database and JSONL export
  - Cleans up dependencies and references to deleted issues
  - Works correctly with git-based workflows
- **Issue Restoration**: `bd restore` command for recovering compacted/deleted issues
  - Restores issues from git history when needed
  - Preserves references and dependency relationships
- **Prefix Renaming**: `bd rename-prefix` command for batch ID prefix changes
  - Updates all issue IDs and text references throughout the database
  - Useful for project rebranding or namespace changes
- **Comprehensive Testing**: Added scripttest-based integration tests (#59)
  - End-to-end coverage for CLI workflows
  - Tests for init command edge cases (bd-70)

### Fixed
- **Critical**: Metadata errors causing crashes on first import (bd-663)
  - Auto-import now treats missing metadata as first import instead of failing
  - Eliminates initialization errors in fresh repositories
- **Critical**: N+1 query pattern in auto-import (bd-666)
  - Replaced per-issue queries with batch fetching
  - Dramatically improves performance for large imports
- **Critical**: Duplicate issue imports (bd-421)
  - Added deduplication logic to prevent importing same issue multiple times
  - Maintains data integrity during repeated imports
- **Bug**: Auto-flush missing after renumber/rename-prefix (bd-346)
  - Commands now properly export to JSONL after completion
  - Ensures git sees latest changes immediately
- **Bug**: Renumber ID collision with UUID temp IDs (bd-345)
  - Uses proper UUID-based temporary IDs to prevent conflicts during renumbering
  - ID counter now correctly syncs after renumbering operations
- **Bug**: Collision resolution dependency handling (bd-437)
  - Uses unchecked dependency addition during collision remapping
  - Prevents spurious cycle detection errors
- **Bug**: macOS crashes documented (closes #3, bd-87)
  - Added CGO_ENABLED=1 workaround documentation for macOS builds

### Changed
- CLI commands now prefer RPC when daemon is running
  - Improved error reporting and diagnostics for RPC failures
  - More consistent exit codes and status messages
- Internal command architecture refactored for RPC client/server sharing
  - Reduced code duplication between direct and daemon modes
  - Improved reliability of background operations
- Ready work sort order flipped to show oldest issues first
  - Helps prioritize long-standing work items

### Performance
- Faster command execution through RPC-backed daemon (up to 10x improvement)
- N+1 query elimination in list/show operations
- Reduced write amplification from improved auto-flush behavior
- Cycle detection performance benchmarks added (bd-311)

### Testing
- Integration tests for daemon RPC request/response flows
- End-to-end coverage for delete/restore lifecycles  
- Regression tests for metadata handling, auto-flush, ID counter sync
- Comprehensive tests for collision detection in auto-import (bd-401)

### Documentation
- Release process documentation added (RELEASING.md)
- Multiple workstreams warning banner for development coordination

## [0.9.8] - 2025-10-16

### Added
- **Background Daemon Mode**: `bd daemon` command for continuous auto-sync (#bd-386)
  - Watches for changes and automatically exports to JSONL
  - Monitors git repository for incoming changes and auto-imports
  - Production-ready with graceful shutdown, PID file management, and signal handling
  - Eliminates manual export/import in active development workflows
- **Git Synchronization**: `bd sync` command for automated git workflows (#bd-378)
  - One-command sync: stage, commit, pull, push JSONL changes
  - Automatic merge conflict resolution with collision remapping
  - Status reporting shows sync progress and any issues
  - Ideal for distributed teams and CI/CD integration
- **Issue Compaction**: `bd compact` command to summarize old closed issues (bd-254-264)
  - AI-powered summarization using Claude Haiku
  - Reduces database size while preserving essential information
  - Configurable thresholds for age, dependencies, and references
  - Compaction status visible in `bd show` output
- **Label and Title Filtering**: Enhanced `bd list` command (#45, bd-269)
  - Filter by labels: `bd list --label bug,critical`
  - Filter by title: `bd list --title "auth"`
  - Combine with status/priority filters
- **List Output Formats**: `bd list --format` flag for custom output (PR #46)
  - Format options: `default`, `compact`, `detailed`, `json`
  - Better integration with scripts and automation tools
- **MCP Reopen Support**: Reopen closed issues via MCP server
  - Claude Desktop plugin can now reopen issues
  - Useful for revisiting completed work
- **Cross-Type Cycle Prevention**: Dependency cycles detected across all types (bd-312)
  - Prevents A→B→A cycles even when mixing `blocks`, `related`, etc.
  - Semantic validation for parent-child direction
  - Diagnostic warnings when cycles detected

### Fixed
- **Critical**: Auto-import collision skipping bug (bd-393, bd-228)
  - Import would silently skip collisions instead of remapping
  - Could cause data loss when merging branches
  - Now correctly applies collision resolution with remapping
- **Critical**: Transaction state corruption (bd-221)
  - Nested transactions could corrupt database state
  - Fixed with proper transaction boundary handling
- **Critical**: Concurrent temp file collisions (bd-306, bd-373)
  - Multiple `bd` processes would collide on shared `.tmp` filename
  - Now uses PID suffix for temp files: `.beads/issues.jsonl.tmp.12345`
- **Critical**: Circular dependency detection gaps (bd-307)
  - Some cycle patterns were missed by detection algorithm
  - Enhanced with comprehensive cycle prevention
- **Bug**: False positive merge conflict detection (bd-313, bd-270)
  - Auto-import would detect conflicts when none existed
  - Fixed with improved Git conflict marker detection
- **Bug**: Import timeout with large issue sets (bd-199)
  - 200+ issue imports would timeout
  - Optimized import performance
- **Bug**: Collision resolver missing ID counter sync (bd-331)
  - After remapping, ID counters weren't updated
  - Could cause duplicate IDs in subsequent creates
- **Bug**: NULL handling in statistics for empty databases (PR #37)
  - `bd stats` would crash on newly initialized databases
  - Fixed NULL value handling in GetStatistics

### Changed
- Compaction removes snapshot/restore (simplified to permanent decay)
- Export file writing refactored to avoid Windows Defender false positives (PR #31)
- Error handling improved in auto-import and fallback paths (PR #47)
- Reduced cyclomatic complexity in main.go (PR #48)
- MCP integration tests fixed and linting cleaned up (PR #40)

### Performance
- Cycle detection benchmarks added (bd-311)
- Import optimization for large issue sets
- Export uses PID-based temp files to avoid lock contention

### Community
- Merged PR #31: Windows Defender mitigation for export
- Merged PR #37: Fix NULL handling in statistics
- Merged PR #38: Nix flake for declarative builds
- Merged PR #40: MCP integration test fixes
- Merged PR #45: Label and title filtering for bd list
- Merged PR #46: Add --format flag to bd list
- Merged PR #47: Error handling consistency
- Merged PR #48: Cyclomatic complexity reduction

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

- **0.9.8** (2025-10-16): Daemon mode, git sync, compaction, critical bug fixes
- **0.9.2** (2025-10-14): Community PRs, critical bug fixes, and --deps flag
- **0.9.1** (2025-10-14): Performance optimization and critical bug fixes
- **0.9.0** (2025-10-12): Pre-release polish and collision resolution
- **0.1.0**: Initial development version

## Upgrade Guide

### Upgrading to 0.9.8

No breaking changes. All changes are backward compatible:
- **bd daemon**: New optional background service for auto-sync workflows
- **bd sync**: New optional git integration command
- **bd compact**: New optional command for issue summarization (requires Anthropic API key)
- **--format flag**: Optional new feature for `bd list`
- **Label/title filters**: Optional new filters for `bd list`
- **Bug fixes**: All critical fixes are transparent to users

Simply pull the latest version and rebuild:
```bash
go install github.com/steveyegge/beads/cmd/bd@latest
# or
git pull && go build -o bd ./cmd/bd
```

**Note**: The `bd compact` command requires an Anthropic API key in `$ANTHROPIC_API_KEY` environment variable. All other features work without any additional setup.

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
