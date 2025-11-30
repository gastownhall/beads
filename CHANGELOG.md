# Changelog

All notable changes to the beads project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.26.2] - 2025-11-29

### Fixed

- **Hash-Based Staleness Detection (bd-f2f)**: Prevents stale DB from corrupting JSONL when counts match
  - Previous count-based check (0.26.1) missed cases where DB and JSONL had similar issue counts
  - New detection computes SHA256 hash of JSONL content and stores it after import
  - On export, compares current JSONL hash against stored hash to detect modifications
  - If JSONL was modified externally (e.g., by git pull), triggers re-import before export
  - Ensures database is always synchronized with JSONL before exporting changes

## [0.26.1] - 2025-11-29

### Fixed

- **CRITICAL: Reverse ZFC Check (bd-53c)**: Prevents stale database from corrupting JSONL
  - Root cause: `bd sync` exports DB to JSONL before pulling from remote
  - If local DB is stale (fewer issues than JSONL), stale data would corrupt the JSONL
  - Added reverse ZFC check: detects when JSONL has >20% more issues than DB
  - When detected, imports JSONL first to sync database before any export
  - Prevents fresh/stale clones from exporting incomplete database state

## [0.26.0] - 2025-11-27

### Added

- **bd doctor --check-health**: Lightweight health checks for startup hooks (3fe94f2)
  - Quick, silent health checks (exit 0 on success, non-zero on issues)
  - Checks: version mismatch, sync.branch config, outdated hooks
  - New `hints.doctor` config option to suppress doctor hints globally
  - Git hooks now call `bd doctor --check-health` in post-merge/post-checkout

- **--no-git-history Flag**: Prevent spurious deletions during import/sync (5506486)
  - Use when git history is unreliable (shallow clones, squash merges)
  - Prevents deletion manifest from removing issues based on stale history

- **gh2jsonl Hash ID Mode**: Content-based ID generation for GitHub imports (#383 by @deangiberson)
  - `--id-mode {sequential|hash}` flag (default: sequential for backward compatibility)
  - `--hash-length {3,4,5,6,7,8}` for configurable hash length (default: 6)
  - Hash IDs are deterministic using title, description, creator, timestamp

- **CI Provenance Attestation**: npm publish now includes provenance attestation (03d62d0)

- **bdui**: Added to Third-Party Tools ecosystem (#384)

### Fixed

- **Critical: MCP Protocol Stdin Fix** (PR #400 by @cleak)
  - Subprocess stdin inheritance was breaking MCP JSON-RPC protocol
  - All subprocess calls now use `stdin=subprocess.DEVNULL`
  - Fixes hanging/blocking issues in Claude Desktop MCP integration

- **Git Worktree Staleness** (#399)
  - Staleness check was failing after writes in git worktrees
  - Now uses RFC3339Nano precision for `last_import_time` metadata

- **Multi-Part Prefix Support** (#398)
  - Issue ID extraction now correctly handles multi-part prefixes
  - Example: `my-app-123` correctly extracts prefix `my-app`

- **bd sync Commit Scope** (bd-red)
  - `bd sync` now only commits `.beads/` files, not other staged files
  - Prevents accidental commits of unrelated staged changes

- **Auto-Import to Wrong File** (bd-tqo)
  - Fixed auto-import exporting to wrong JSONL file
  - FindJSONLPath now correctly skips deletions.jsonl

- **Deletions.jsonl Handling**
  - Git hooks now properly stage deletions.jsonl for cross-clone propagation
  - bd doctor no longer warns about deletions.jsonl
  - Prevent rebase failures from deletions.jsonl writes

- **Defense-in-Depth** (bd-4t7)
  - Added additional check for --no-auto-import flag

- **Tilde Expansion**: Global gitignore path now expands `~` correctly

- **beads-mcp Type Checking**: Resolved all mypy type checking errors

- **CI Test Stability**: Fixed Windows test failures

### Changed

- **Stealth Mode**: Removed global gitattributes setup from `bd init --stealth` (#391)
  - Stealth mode now purely local with no global git configuration

- **Pre-Push Hook Error Message**: Improved clarity when sync fails (#390)

### Refactoring

- Extract path canonicalization and database search helpers (7d765c2)
- Consolidate check-health DB access and expand hook checks (3458956)

### Documentation

- Sync skill CLI reference with current docs (62d1dc9)

### Community

- PR #400: MCP stdin fix (@cleak)
- PR #398: Multi-part prefix support
- PR #391: Stealth mode gitattributes removal
- PR #390: Pre-push hook error message (@jonathanpberger)
- PR #384: bdui ecosystem addition
- PR #383: gh2jsonl hash ID support (@deangiberson)

## [0.25.1] - 2025-11-25

### Added

- **Zombie Resurrection Prevention**: Stale clones can no longer resurrect deleted issues
  - New JSONL sanitization step (3.6) after git pull removes deleted issues before import
  - Prevents git's 3-way merge from re-adding issues that were deleted elsewhere
  - New `bd doctor` check 18: "Deletions Manifest" detects missing/empty manifest
  - New `bd doctor --fix` hydrates deletions.jsonl from git history for pre-v0.25.0 deletions
  - ID validation prevents false positives from non-issue JSON fields

### Fixed

- **bd sync commit scope**: Now commits entire `.beads/` directory before pull
  - Previously only committed beads.jsonl, leaving metadata.json unstaged
  - Fixes "You have unstaged changes" error during `git pull --rebase`

## [0.25.0] - 2025-11-25

### Added

- **Deletion Propagation**: Deletions now sync across clones via deletions manifest (bd-imj)
  - New `.beads/deletions.jsonl` tracks deleted issues with timestamp, actor, reason
  - Import automatically purges issues that were deleted in other clones
  - Git history fallback for pruned deletion records (self-healing)
  - New `bd deleted` command to view deletion audit trail
  - Auto-compact during sync (opt-in via `deletions.auto_compact: true`)
  - Configurable retention period (`deletions.retention_days`, default 7)
  - Local unpushed work protected from accidental deletion
  - Full documentation in docs/DELETIONS.md

- **Stealth Mode**: New `bd init --stealth` flag for invisible beads usage (#381)
  - Adds `.beads/` to project's `.gitignore` automatically
  - Useful for personal issue tracking in shared repos without affecting collaborators
  - All bd functionality works normally, just not committed to git

- **Ephemeral Branch Sync**: New `bd sync --from-main` flag (gt-ick9)
  - Syncs from main branch without pushing to remote
  - Ideal for feature branches that sync issues from main
  - Prevents pushing local branch changes to origin

## [0.24.4] - 2025-11-25

### Added

- **Transaction API**: Full transactional support for atomic multi-operation workflows (bd-8bq)
  - New `storage.Transaction` interface with CreateIssue, UpdateIssue, CloseIssue, DeleteIssue
  - Dependency operations: AddDependency, RemoveDependency within transactions
  - Label operations: AddLabel, RemoveLabel with transactional semantics
  - Config/Metadata operations for atomic config+issue workflows
  - Uses `BEGIN IMMEDIATE` mode to prevent deadlocks
  - Automatic rollback on error or panic, commit on success
  - 1,147 lines of new implementation with comprehensive tests

- **Tip System Infrastructure**: Smart contextual hints for users (bd-d4i)
  - Tips shown after successful commands (list, ready, create, show)
  - Condition-based filtering, priority ordering, probability rolls
  - Frequency limits prevent tip spam
  - Respects `--json` and `--quiet` flags
  - Deterministic testing via `BEADS_TIP_SEED` env var

- **Sorting for bd list and bd search**: New `--sort` and `--reverse` flags (bd-22g)
  - Sort by: priority, created, updated, closed, status, id, title, type, assignee
  - Smart defaults: priority ascending (P0 first), dates descending (newest first)
  - Works with all existing filters

- **Claude Integration Verification**: New bd doctor checks (bd-o78)
  - `CheckBdInPath`: verifies 'bd' is in PATH (needed for hooks)
  - `CheckDocumentationBdPrimeReference`: detects version mismatches in docs

- **ARM Linux Support**: GoReleaser now builds for linux/arm64 (PR #371 by @tjg184)
  - Enables bd on ARM-based Linux systems like Raspberry Pi, AWS Graviton

- **Orphan Detection Migration**: Identifies orphaned child issues (bd-3852)
  - Detects issues with hierarchical IDs where parent no longer exists
  - Logs suggestions: delete, convert to standalone, or restore parent
  - Idempotent and safe to run multiple times

### Fixed

- **Transaction Cache Invalidation**: blocked_issues_cache now invalidates correctly (bd-1c4h)
  - UpdateIssue status changes trigger cache invalidation
  - CloseIssue always invalidates (closed issues don't block)
  - AddDependency/RemoveDependency invalidate for blocking types

- **SQLITE_BUSY Retry Logic**: Exponential backoff for concurrent writes (bd-ola6)
  - `beginImmediateWithRetry()` with 5 retries (10ms, 20ms, 40ms, 80ms, 160ms)
  - Eliminates spurious failures under normal concurrent usage
  - Context cancellation respected between retry attempts

- **bd import Argument Validation**: Helpful error for common mistake (bd-77gm)
  - Running `bd import file.jsonl` (without `-i`) now shows clear error
  - Previously silently read from stdin, confusing users with "0 created"

- **ZFC Import Export Skip**: Preserve JSONL source of truth (bd-l0r)
  - After stale DB import from JSONL, skip export to avoid overwriting
  - Fixes scenario where DB with fewer issues would overwrite JSONL

- **Daemon Reopen with Reason**: `--reason` flag now works in daemon mode (bd-r46)
  - Previously ignored in daemon RPC calls

- **Windows Test Failures**: Skip file permission tests on Windows
  - Windows doesn't support Unix-style permissions (0600, 0755)
  - Core functionality still tested, only permission checks skipped

### Changed

- **bd daemon UX**: New `--start` flag, help text when no args (bd-gfu)
  - `bd daemon` now shows help instead of immediately starting
  - Use `bd daemon --start` to explicitly start
  - Auto-start still works (uses `--start` internally)
  - More discoverable for new users

### Documentation

- **blocked_issues_cache Architecture**: Document cache behavior and invalidation
- **Antivirus False Positives**: Guide for handling security software alerts (bd-t4u1)
- **import.orphan_handling**: Complete documentation (bd-9cdc)
- **Error Handling Patterns**: Comprehensive audit and guidelines

### Dependencies

- Bump github.com/tetratelabs/wazero from 1.10.0 to 1.10.1 (#374)
- Bump github.com/anthropics/anthropic-sdk-go from 1.18.0 to 1.18.1 (#373)
- Bump actions/checkout from 4 to 6 (#372)

### Community

- PR #371: ARM Linux support (@tjg184)

## [0.24.3] - 2025-11-24

### Added

- **BD_GUIDE.md Generation**: Version-stamped documentation for AI agents (bd-woro, 9e16469)
  - New `--output` flag for `bd onboard` command generates BD_GUIDE.md
  - Separates bd-specific instructions from project-specific instructions
  - Auto-generated header with version stamp warns against manual editing
  - Detects outdated BD_GUIDE.md and suggests regeneration after upgrades
  - Git-trackable diffs show exactly what changed between versions

- **Configurable Export Error Policies**: Flexible error handling for export operations (bd-exug, e3e0a04)
  - Four policies: strict (fail-fast), best-effort (skip with warnings), partial (retry then skip), required-core (fail on core data only)
  - Per-project configuration via `bd config set export.error_policy`
  - Separate policy for auto-exports via `auto_export.error_policy`
  - Retry with exponential backoff for transient failures
  - Optional export manifests documenting completeness

- **Command Set Standardization**: Complete flag and feature consistency overhaul (bd-au0, 273a4d1)
  - Global verbosity flags: `--verbose/-v` and `--quiet/-q` across all commands
  - Standardized `--dry-run` flag behavior across all commands
  - Label operations added to `bd update`: `--add-labels` and `--remove-labels`
  - Enhanced `bd export` with comprehensive filters (assignee, type, labels, priority, dates)
  - Enhanced `bd search` with date and priority filters
  - Improved documentation for `bd clean` vs `bd cleanup` disambiguation

- **Auto-Migration on Version Bump**: Automatic database schema updates (bd-jgxi, 7796f5c)
  - Database version automatically synced when bd CLI is upgraded
  - Eliminates recurring "version mismatch" warnings in bd doctor
  - Best-effort and silent to avoid disrupting commands

- **Version Tracking Validation**: Comprehensive bd doctor checks (bd-u4sb, d8f3eb0)
  - Validates metadata.json exists with valid LastBdVersion field
  - Warns if LastBdVersion is very old (>10 minor versions behind)
  - Ensures version tracking system is working correctly

- **Git Hooks Executable Validation**: Hook reliability improvements (bd-fwul, 1c715bc)
  - bd doctor now validates git hooks have executable bit set
  - Prevents silent hook failures from incorrect permissions

- **Socket Cleanup Race Condition Fix**: Daemon reliability improvement (bd-4owj, 18f8105)
  - Re-checks socket existence after lock check to avoid stale state
  - Handles daemon startup race conditions gracefully

- **Monitor Web UI Enhancements**: UX/UI improvements (aa2df73)
  - Interactive stats cards as filters
  - Multi-select priority filtering with P0 support
  - Find-as-you-type search functionality
  - Modern card-based UI design with better mobile responsiveness
  - Dev mode flag for easier development

- **bump-version.sh Automation**: PyPI integration (0547004)
  - New `--upgrade-mcp` flag for automatic beads-mcp package upgrades
  - Tries pip first, falls back to uv tool
  - Warns about version mismatch before PyPI publish

### Fixed

- **JSONL Import Foreign Key Violations**: Graceful handling of deletions during merge (bd-koab, d45cff5)
  - Import now continues when dependencies reference deleted issues
  - Reports skipped dependencies with clear warnings
  - Prevents complete import failure from FK constraint violations
  - Common scenario: merges that delete issues referenced by other issues

- **Metadata JSONL Path Auto-Detection**: Fix configuration mismatches (bd-afd, d1641c7)
  - Auto-detects actual JSONL file when metadata.json is recreated
  - bd doctor --fix now includes DatabaseConfig() auto-repair
  - Prefers beads.jsonl over issues.jsonl (canonical name)
  - Prevents mismatches after git clean, merge conflicts, or rebases

- **JSONL Resurrection Bug**: Critical fix for deleted issue resurrection (bd-v0y, c9a2e7a)
  - Removed mtime fast-path in hasJSONLChanged() causing false negatives
  - Git doesn't preserve mtime on checkout, causing incorrect change detection
  - Now always uses content hash for reliable comparison
  - Prevents bd sync from overwriting pulled JSONL and resurrecting deleted issues

- **ZFC (JSONL First Consistency)**: Fix stale DB overwriting JSONL on sync (bd-l0r, 1ba068f, 2e4171a, 949ab42)
  - bd sync now detects stale DB (>50% divergence from JSONL) and imports first
  - After ZFC import, skips export to prevent overwriting JSONL source of truth
  - Fixes bug where DB with 688 issues would overwrite JSONL with 62 issues after pull
  - JSONL is source of truth after git pull - DB syncs to match, not vice versa
  - Preserves local uncommitted changes while catching stale DB scenarios

- **Merge Conflict Semantics**: Improved resolution policies (bd-pq5k, d4f9a05)
  - Merge logic now enforces: closed ALWAYS wins over open
  - Deletion ALWAYS wins over modification
  - Fixed closed_at handling: only set when status='closed'
  - Prevents issues from getting stuck in invalid states
  - Eliminates "zombie issues" that never die

- **JSONL Merge Conflict Auto-Resolution**: Streamlined rebase workflow (bd-cwmt, 3cf5e26)
  - bd sync now auto-resolves JSONL conflicts during rebase
  - Detects rebase state and JSONL-only conflicts
  - Auto-exports from DB and continues rebase automatically
  - Eliminates manual conflict resolution in common scenarios

- **Staleness Check Error Handling**: Better metadata validation (bd-2q6d, bd-o4qy, bd-n4td, b75914b)
  - CheckStaleness now returns errors for corrupted last_import_time metadata
  - Enhanced warning messages when staleness check fails
  - Handles empty string metadata (memory store behavior)
  - Prevents silent failures with corrupted metadata

- **N+1 Query Pattern in Export**: Dramatic performance improvement (bd-rcmg, 9c6b375)
  - Added batch methods: GetCommentsForIssues() and GetLabels() in bulk
  - Reduced query count from ~201 to ~3-5 for 100 issues
  - Eliminated per-issue loops in handleExport() and triggerExport()

- **Sync Branch Auto-Configuration**: Prevent bd sync failures after init (bd-flil, bd-rsua, a4c38d5, 83609d5)
  - bd init now auto-sets sync.branch to current git branch
  - bd doctor detects missing sync.branch config and provides --fix
  - All branch detection uses 'git symbolic-ref' to work in fresh repos
  - Fixes 'bd sync --status' error after fresh bd init

- **Merge Driver Auto-Repair**: Fix stale git configurations (bd-3sz0, bd-tbz3, 1c8dd49)
  - Detects old bd versions (<0.24.0) with invalid %L/%R placeholders
  - Auto-repairs during bd init and bd doctor --fix
  - Git only supports %O (base), %A (current), %B (other) placeholders
  - Supports both canonical (issues.jsonl) and legacy (beads.jsonl) filenames

- **Unvalidated Dependency Parsing**: Prevent empty ID lookups (bd-ia8r, e8a752e)
  - Validates dependsOnID is non-empty before setting discoveredFromParentID
  - Ensures parent issue exists before generating child IDs in direct mode

- **Windows CI Test Failures**: Cross-platform reliability (153f724)
  - Fixed file locking issue in TestNewSQLiteStorage (added defer store.Close())
  - Fixed nil slice return in TestFindAllDatabases (explicit empty slice initialization)
  - Both issues related to stricter Windows file locking behavior

- **Invalid closed_at States**: Data integrity fix (bd-1rh, b428254)
  - Removed invalid closed_at timestamps for open issues
  - Prevents inconsistent state from merge conflicts

- **Security**: File permission hardening (b6870de, ae5a4ac)
  - Changed file permissions from 0644 to 0600 for security (gosec G302)
  - Config files now owner read/write only (not world readable)
  - Added comprehensive security tests for WriteFile permissions
  - Handles read-only files by fixing permissions before writing

### Changed

- **bd init Defaults**: Better out-of-box experience (bd-bxha, ec4117d)
  - Git hooks and merge driver now installed by default
  - Removed interactive prompts (simpler workflow)
  - New opt-out flags: `--skip-hooks` and `--skip-merge-driver`
  - Shows warning messages on failure with suggestion to run bd doctor --fix

- **bd init Validation**: Automatic setup verification (bd-zwtq, 3a36d0b)
  - Runs bd doctor diagnostics at end of bd init
  - Immediately identifies configuration problems before user encounters them
  - Catches: missing hooks, unconfigured merge driver, missing docs, metadata issues

- **Internal Code Organization**: Improved maintainability (bd-0a43, 58f37d0)
  - Split monolithic sqlite.go (1050 lines) into focused files
  - store.go: Database initialization and utilities
  - queries.go: Issue CRUD operations
  - config.go: Configuration and metadata
  - comments.go: Comment operations
  - Zero functional changes, all tests pass

- **Documentation**: Organization improvements (a930fa3, 62b5f53)
  - Moved event-driven daemon details to docs/DAEMON.md
  - Added comprehensive error handling guidelines
  - Reduced duplication in AGENTS.md

### Testing

- **Blocked Issues Cache Validation**: Comprehensive cache tests (bd-13gm, 0e6ed91)
  - 8 tests verify cache invalidation behavior
  - Tests for dependency add/remove, status changes, transitive blocking
  - Direct cache table queries validate implementation correctness

- **Sync Test Optimization**: Reduced boilerplate (bd-ktng, dfcbb7d)
  - Added shared git repo setup helpers
  - Refactored 19 test functions
  - Reduced duplicate code by ~300 lines

### Performance

- **Daemon Log Rotation**: Production-ready configuration (bd-t7ds, f454b3d)
  - Max size increased: 10MB → 50MB per file
  - Max backups increased: 3 → 7 files
  - Max age increased: 7 → 30 days
  - Better handles long-running daemons with high log output

## [0.24.2] - 2025-11-22

### Fixed

- **Test Stability**: Complete rootCtx initialization fix for all hanging tests (issue #355, b8db5ab)
  - Fixed TestGetAssignedStatus missing rootCtx initialization (a517ec9)
  - Prevents test hangs from uninitialized context
  - Improved test reliability and isolation

- **JSONL Configuration**: Improved bd doctor JSONL checks to focus on real problems (87ee3a6)
  - Reduces false positives in JSONL validation
  - Better detection of actual configuration issues

### Changed

- **JSONL Filename Default**: Changed default JSONL filename from `beads.jsonl` to `issues.jsonl` (c4c5c80)
  - Updated TestFindJSONLPathDefault to match new default (5eefec7)
  - Removed stale `issues.jsonl` in favor of configured `beads.jsonl` (d918e47)
  - More intuitive default filename for new users

## [0.24.1] - 2025-11-22

### Added

- **bd search**: Date and priority filters (787fb4e)
  - `--created-after`, `--created-before` for date filtering
  - `--priority-min`, `--priority-max` for priority range filtering
  - Enables more precise search queries

- **bd count**: New command for counting and grouping issues (d7f4189)
  - Count issues by status, priority, type, or labels
  - Helpful for generating statistics and reports

- **Test Infrastructure**: Automatic skip list for tests (0040e80)
  - Improves test reliability and maintenance
  - Automatically manages flaky or environment-specific tests

### Fixed

- **Test Stability**: Fixed hanging tests by initializing rootCtx (822baa0, bd-n25)
  - Prevents test hangs from context cancellation issues
  - Better test isolation and cleanup

- **Git Merge Driver**: Corrected placeholders from %L/%R to %A/%B (ddd209e)
  - Fixes merge driver configuration for proper conflict resolution
  - Uses correct git merge driver variable names

- **Database Paths**: Deduplicate database paths when symlinks present (#354, f724b61)
  - Prevents duplicate database detection when symlinks are involved
  - Improves reliability in complex filesystem setups

### Changed

- **bd list**: Accept both integer and P-format for priority flags (2e2b8d7)
  - `--priority 1` and `--priority P1` now both work
  - More flexible CLI input for priority filtering

- **bd update**: Added `--body` flag as alias for `--description` (bb5a480)
  - More intuitive flag name for updating issue descriptions
  - Both flags work identically for backward compatibility

- **bd update**: Added label operations (3065db2)
  - `--add-labels` and `--remove-labels` flags
  - Simplifies label management in update operations

- **GitHub Copilot Support**: Added `.github/copilot-instructions.md` (605fff1)
  - Provides project-specific guidance for GitHub Copilot
  - Improves AI-assisted development experience

- **Documentation**: Moved design/audit docs from cmd/bd to docs/ (ce433bb)
  - Better organization of project documentation
  - Clearer separation of code and documentation

### Performance

- **Test Suite**: Deleted 7 redundant tests from main_test.go (fa727c7)
  - 3x speedup in test execution
  - Improved CI/CD performance

- **Test Coverage**: Tagged 16 slow integration tests with build tags (8290243)
  - Faster local test runs with `-short` flag
  - CI can still run full test suite

### Testing

- **Security Tests**: Added security and error handling tests for lint warnings (74f3844)
  - Improved code quality and safety
  - Better coverage of edge cases

- **Shared Database Pattern**: Refactored multiple test files to use shared DB pattern (bd-1rh)
  - compact_test.go, integrity_test.go, validate_test.go, epic_test.go, duplicates_test.go
  - Improved test consistency and maintainability
  - Faster test execution through better resource sharing

## [0.24.0] - 2025-11-20

### Added

- **bd doctor --fix**: Automatic repair functionality (bd-ykd9, 7806937)
  - Automatically fixes issues detected by `bd doctor`
  - Repairs common database inconsistencies without manual intervention

- **bd clean**: Remove temporary merge artifacts (e8355c2)
  - Cleans up `.base`, `.ours`, `.theirs` snapshot files
  - Helps maintain clean `.beads/` directory after merges

- **bd cleanup**: Enhanced bulk deletion command
  - Delete multiple closed issues efficiently
  - Improved from previous versions with better performance

- **.beads/README.md Generation**: Auto-generated during `bd init` (bd-m7ge, e1c8853)
  - Provides project-specific beads documentation
  - Helps new contributors understand the setup

- **blocked_issues_cache Table**: Performance optimization for GetReadyWork (62c1f42, ed23f8f)
  - Caches blocked issue relationships
  - Dramatically improves `bd ready` performance on large databases

- **Commit Hash in Version Output**: Enhanced version reporting (bd-hpt5, 7c96142)
  - `bd version` now shows git commit hash
  - Helps identify exact build for debugging

- **Auto-detection of Issue Prefix**: Scans git history to detect prefix (#277, 8f37904)
  - Automatically discovers project's issue prefix
  - Reduces manual configuration needed

- **external_ref Support in Daemon RPC**: Full daemon mode support (#304, 57b6ea6)
  - MCP server can now set external references in daemon mode
  - Parity with CLI functionality

- **Context Optimization Features**: AI agent improvements (#297, f7e80dd)
  - Context propagation with graceful cancellation (bd-rtp, bd-yb8, bd-2o2, 57253f9)
  - Better memory management for long-running agent sessions

### Fixed

- **Critical: Auto-import Resurrection Bug** (bd-khnb, 0020eb4, e28e3ea, 7b6370f)
  - Fixed critical bug where deleted issues were resurrected during auto-import
  - Cleaned up 497+ resurrected issues from production database
  - Prevents data corruption from improper JSONL replay

- **Critical: bd sync Auto-resolves Conflicts** (bd-ca0b, a1e5075)
  - `bd sync` now automatically resolves conflicts instead of failing
  - Dramatically improves multi-agent workflow reliability
  - Eliminates manual conflict resolution in most cases

- **Critical: Content-based Timestamp Skew Prevention** (bd-lm2q, d0e7047)
  - Fixed false-positive "JSONL is newer than database" warnings
  - Uses content-based comparison instead of timestamp-only
  - Prevents unnecessary imports that would corrupt state

- **Critical: bd sync DB Changes After Import** (81c741b)
  - Ensures database changes are properly applied after import
  - Fixes desync issues between JSONL and database

- **Critical: Context Propagation Lifecycle Bugs** (bd-rtp, bd-yb8, bd-2o2, 57253f9, a17e4af)
  - Fixed multiple context propagation issues causing crashes
  - Graceful cancellation support for long-running operations
  - Improved stability for AI agent workflows

- **Critical: Race Condition in Auto-flush** (bd-52, a9b2f9f)
  - Fixed race condition in auto-flush mechanism
  - Prevents data loss during concurrent operations

- **Critical: Resource Leaks and Error Handling** (#327, fb65163)
  - Fixed critical resource leaks in daemon mode
  - Improved error handling throughout codebase

- **Critical: In-memory Database Deadlock** (bd-yvlc, 944ed10)
  - Fixed deadlock in migrations when using in-memory database
  - Improves test reliability

- **MCP Schema Generation Recursion Bug** (GH#346, f3a678f)
  - Fixed infinite recursion in MCP schema generation
  - Prevents stack overflow crashes

- **FK Constraint Failures** (bd-5arw, 345766b)
  - Fixed foreign key constraint failures in AddComment and ApplyCompaction
  - Improved data integrity

- **--parent Flag Behavior** (b9919fe)
  - Now correctly creates parent-child dependency relationships
  - Previously was creating wrong dependency type

- **Exact ID Matching Priority** (gh-316, 934ae04)
  - Prefers exact ID matches over prefix matches
  - Prevents ambiguous ID resolution

- **Daemon Lifetime on macOS** (GH#278, 68f9bef)
  - Fixed daemon exiting after 5s on macOS due to PID 1 parent monitoring
  - Daemon now runs reliably on macOS

- **Daemon Export/JSONL Sync** (GH#301, #321, 04a1996)
  - Fixed daemon export leaving JSONL newer than database
  - Ensures proper sync between export and database state

- **bd doctor Hash ID Detection** (GH#322, 8c1f865)
  - Fixed doctor incorrectly diagnosing hash IDs as sequential
  - Improved detection logic for ID format validation

- **ResolvePartialID Handling** (GH#336, 4432af0)
  - Improved ResolvePartialID / ResolveID handling for `bd show`
  - Better partial ID matching and error messages

- **bd sync Windows Upstream Detection** (#281, 1deaad1)
  - Fixed upstream branch detection on Windows
  - Improved cross-platform compatibility

- **Compact Command Daemon Mode** (#294, d9904a8)
  - Fixed compact command failing with 'SQLite DB needed' error when daemon running
  - Removed premature store check, uses ensureDirectMode

- **DB mtime Update After Import** (#296, 9dff345)
  - Fixed DB mtime not being updated after import with 0 changes
  - Prevents false staleness warnings

- **FOREIGN KEY Constraint on Non-existent Issues** (09666b4)
  - Fixed constraint failures when operating on non-existent issues
  - Better error handling and validation

- **Monitor WebUI Daemon Detection** (e36baee)
  - Fixed monitor-webui failure to detect running daemon
  - Improved daemon health checking

- **Onboard Test Deadlock on Windows** (4e22214)
  - Fixed deadlock in onboard tests on Windows
  - Improved test stability

- **Windows Concurrent Issue Creation** (4cd26c8)
  - Fixed concurrent issue creation failures on Windows
  - Better file locking on Windows

- **Missing Git Hook Message** (#306, 92f3af5)
  - Improved messaging when git hooks are missing
  - Clearer instructions for users

- **Prefix Detection for Hyphenated Apps** (83472ac, bd-fasa)
  - Fixed prefix detection to only use first hyphen
  - Handles hyphenated application names correctly

- **External Ref Migration Failures** (8be792a)
  - Fixed external_ref migration failure on old databases
  - Backward compatibility improvements

- **Duplicate Function Declaration** (#328, 167ab67)
  - Fixed compilation failure from duplicate computeJSONLHash declaration
  - Removed old version, kept simpler implementation
  - Updated test to match new API

### Changed

- **Performance Improvements** (#319, 690c73f):
  - Optimized GetReadyWork to use blocked_issues_cache (ed23f8f)
  - Replaced N+1 label queries with bulk fetch in `bd list` (968d9e2)
  - Cache invalidation for blocked_issues_cache (614ba8a)
  - Significant speedup for large databases

- **FlushManager Improvements** (445857f)
  - Added constants for magic numbers
  - Enhanced error logging
  - Comprehensive functional tests

- **Auto-upgrade .beads/.gitignore** (#300, f4a2f87)
  - Automatically upgrades .gitignore on bd operations
  - Ensures latest patterns are always applied

- **Code Refactoring**:
  - Extract duplicated validation logic to internal/validation (d5239ee)
  - Centralize error handling patterns in storage layer (bd-bwk2, 3b2cac4)
  - Extract duplicated validation and flag logic (bd-g5p7, bbfedb0)
  - Improved code organization and maintainability

- **Documentation Improvements**:
  - Document files created by bd init and clarify .gitattributes (721274b, e7fd1dd)
  - How to resolve merge conflicts in .beads/beads.jsonl (4985a68)
  - Document MCP tools loading issue in Claude Code (GH#346, 79b8dbe)
  - Add uv prerequisite to Claude Code plugin docs (#293, a020c6c)
  - Don't auto-install Go in Windows installer (#302, 0cba73b)

- **Improved Error Messages** (#349, 27c0c33)
  - Compact error messages
  - Remove bogus merge suggestion
  - Add daemon/maintenance docs

- **AGENTS.md Refactoring** (21a0656)
  - Extracted detailed instructions to prevent context pollution
  - Better organization for AI agent consumption

- **Type Safety Improvements** (9e57cb6)
  - Improved type safety in beads-mcp
  - Fixed minor type issues

- **Test Improvements**:
  - Fix CI regressions and stabilize tests (7b63b5a)
  - Fix parallel test deadlock (1fc9bf6)
  - Annotate gosec-safe file accesses (bf9b2c8)

- **Local-only Git Repo Support** (bd-biwp, 4de9f01)
  - Support repositories without remote origin
  - Better handling of local development workflows

- **Version Marker in Post-checkout Hook** (ad2154b)
  - Add version marker to post-checkout hook
  - Include in CheckGitHooks for better version tracking

### Performance

- **GetReadyWork Optimization** (bd-5qim, 690c73f, 62c1f42, ed23f8f)
  - Introduced blocked_issues_cache table
  - Eliminated expensive recursive queries
  - Dramatically faster for large dependency graphs

- **bd list N+1 Query Elimination** (968d9e2)
  - Replaced per-issue label queries with bulk fetch
  - Significant speedup when listing many labeled issues

### Community

- **Pull Requests**:
  - #338: Prevent daemon from exiting when launcher process exits (@cpdata)
  - #337: Improve ResolvePartialID handling (@cpdata)
  - #333: Fix doctor incorrectly diagnosing hash IDs (@cpdata)
  - #327: Address critical resource leaks and error handling
  - #306: Improve missing git hook message
  - #304: Add external_ref support to daemon mode RPC
  - #302: Windows installer improvements
  - #300: Automatic .beads/.gitignore upgrade
  - #297: Context optimization features for AI agents
  - #296: Fix DB mtime update after import
  - #294: Fix compact command in daemon mode
  - #293: Add uv prerequisite documentation
  - #281: Fix bd sync Windows upstream detection
  - #277: Auto-detection of issue prefix from git history

- **Dependency Updates**:
  - Bump github.com/anthropics/anthropic-sdk-go from 1.17.0 to 1.18.0 (#330)
  - Bump golang.org/x/mod from 0.29.0 to 0.30.0 (#331)
  - Bump fastmcp from 2.13.0.2 to 2.13.1 (#332)
  - Bump pydantic from 2.12.0 to 2.12.4 (#285)
  - Bump pydantic-settings from 2.11.0 to 2.12.0 (#286)
  - Bump golangci/golangci-lint-action from 8 to 9 (#287)
  - Bump golang.org/x/sys from 0.36.0 to 0.38.0 (#288)
  - Bump github.com/ncruces/go-sqlite3 from 0.29.1 to 0.30.1 (#290)
  - Bump github.com/google/go-cmp from 0.6.0 to 0.7.0 (#291)

### Notes

This release represents a major stability and performance improvement with **179 commits** since 0.23.1. Key themes:
- **Reliability**: Fixed critical auto-import resurrection bug and multiple daemon issues
- **Performance**: Significant optimizations for `bd ready` and `bd list`
- **AI Agent Support**: Improved context propagation and error handling
- **Cross-platform**: Better Windows and macOS support
- **Developer Experience**: Auto-detection, better error messages, improved docs

## [0.23.1] - 2025-11-08

### Fixed

- **#263: Database mtime not updated after import causing false `bd doctor` warnings**
  - When `bd sync --import-only` completed, SQLite WAL mode wouldn't update the main database file's mtime
  - This caused `bd doctor` to incorrectly warn "JSONL is newer than database" even when perfectly synced
  - Now updates database mtime after imports to prevent false warnings

- **#261: SQLite URI missing 'file:' prefix causing version detection failures**
  - Without 'file:' scheme, SQLite treated `?mode=ro` as part of filename instead of connection option
  - Created bogus files like `beads.db?mode=ro`
  - Caused `bd doctor` to incorrectly report "version pre-0.17.5 (very old)" on modern databases

- **bd-17d5: Conflict marker false positives on JSON-encoded content**
  - Issues containing JSON strings with `<<<<<<<` would trigger false conflict marker detection
  - Now checks raw bytes before JSON decoding to avoid false positives

- **bd-ckvw: Schema compatibility probe prevents silent migration failures**
  - Migrations could fail silently, causing cryptic "no such column" and UNIQUE constraint errors later
  - Now probes schema after migrations, retries once if incomplete, and fails fast with clear error
  - Daemon refuses RPC if client has newer minor version to prevent schema mismatches

- **#264/#262: Remove stale `--resolve-collisions` references**
  - Docs/error messages still referenced `--resolve-collisions` flag (removed in v0.20)
  - Fixed post-merge hook error messages and git-hooks README

### Changed

- **bd-auf1: Auto-cleanup snapshot files after successful merge**
  - `.beads/` no longer accumulates orphaned `.base`, `.ours`, `.theirs` snapshot files after merges

- **bd-ky74: Optimize CLI tests with in-process testing**
  - Converted exec.Command() tests to in-process rootCmd.Execute() calls
  - **Dramatically faster: 10+ minutes → just a few seconds**
  - Improved test coverage from 20.2% to 23.3%

- **bd-6uix: Message system improvements**
  - 30s HTTP timeout prevents hangs, full message reading, --importance validation, server-side filtering

- **Remove noisy version field from metadata.json**
  - Eliminated redundant version mismatch warnings on every bd upgrade
  - Daemon version checking via RPC is sufficient

### Added

- Go agent example with Agent Mail support
- Agent Mail multi-workspace deployment guide and scripts

## [0.23.0] - 2025-11-08

### Added

- **Agent Mail Integration**: Complete Python adapter library with comprehensive documentation and multi-agent coordination tests
  - Python adapter library in `integrations/agent-mail-python/`
  - Agent Mail quickstart guide and comprehensive integration docs
  - Multi-agent race condition tests and failure scenario tests
  - Automated git traffic benchmark showing **98.5% reduction in git traffic** compared to git-only sync
  - Bash-agent integration example

- **bd info --whats-new** (bd-eiz9): Agent version awareness for quick upgrade summaries
  - Shows last 3 versions with workflow-impacting changes
  - Supports `--json` flag for machine-readable output
  - Helps agents understand what changed without re-reading full docs

- **bd hooks install** (bd-908z): Embedded git hooks command
  - Replaces external install script with native command
  - Git hooks now embedded in bd binary
  - Works for all bd users, not just source repo users

- **bd cleanup**: Bulk deletion command for closed issues (bd-buol)
  - Agent-driven compaction for large databases
  - Removes closed issues older than specified threshold

### Fixed

- **3-way JSONL Merge** (bd-jjua): Auto-invoked on conflicts
  - Automatically triggers intelligent merge on JSONL conflicts
  - No manual intervention required
  - Warning message added to zombie issues.jsonl file

- **Auto-import on Missing Database** (ab4ec90): `bd import` now auto-initializes database when missing
- **Daemon Crash Recovery** (bd-vcg5): Panic handler with socket cleanup prevents orphaned processes
- **Stale Database Exports** (bd-srwk): ID-based staleness detection prevents exporting stale data
- **Windows MCP Subprocess Timeout** (bd-r79z): Fix for git detection on Windows
- **Daemon Orphaning** (a6c9579): Track parent PID and exit when parent dies
- **Test Pollution Prevention** (bd-z528, bd-2c5a): Safeguards to prevent test issues in production database
- **Client Self-Heal** (a236558): Auto-recovery for stale daemon.pid files
- **Post-Merge Hook Error Messages** (abb1d1c): Show actual error messages instead of silent failures
- **Auto-import During Delete** (bd-8kde): Disable auto-import during delete operations to prevent conflicts
- **MCP Workspace Context** (bd-8zf2): Auto-detect workspace from CWD
- **Import Sync Warning** (bd-u4f5): Warn when import syncs with working tree but not git HEAD
- **GH#254** (bd-tuqd): `bd init` now detects and chains with existing git hooks
- **GH#249**: Add nil storage checks to prevent RPC daemon crashes
- **GH#252**: Fix SQLite driver name mismatch causing "unknown driver" errors
- **Nested .beads Directories** (bd-eqjc): Prevent creating nested .beads directories
- **Windows SQLite Support**: Fix SQLite in releases for Windows

### Changed

- **Agent Affordances** (observations from agents using beads):
  - **bd new**: Added as alias for `bd create` command (agents often tried this)
  - **bd list**: Changed default to one-line-per-issue format to prevent agent miscounting; added `--long` flag for previous detailed format

- **Developer Experience**:
  - Extracted supplemental docs from AGENTS.md for better organization
  - Added warning for working tree vs git HEAD sync mismatches
  - Completion commands now work without database
  - Config included in `bd info` JSON output
  - Python cache files added to .gitignore
  - RPC diagnostics available via `BD_RPC_DEBUG` env var
  - Reduced RPC dial timeout from 2s to 200ms for fast-fail (bd-expt)
  - Standardized daemon detection with tryDaemonLock probe (bd-wgu4)
  - Improved internal/daemon test coverage to 60%

- **Code Organization**:
  - Refactored snapshot management into dedicated module (bd-urob)
  - Documented external_ref in content hash behavior (bd-9f4a)
  - Added MCP server functions for repair commands (bd-7bbc4e6a)
  - Added version number to beads-mcp startup log
  - Added system requirements section for glibc compatibility in docs

- **Release Automation**:
  - Automatic Homebrew formula update in release workflow
  - Gitignore Formula/bd.rb (auto-generated, real source is homebrew-beads tap)

- **Other**:
  - Added `docs/` directory to links (#242)
  - RPC monitoring solution with web UI as implementation example (#244)
  - Remove old install.sh script, replaced by `bd hooks install`
  - Remove vc.db exclusion from FindDatabasePath filter

## [0.22.1] - 2025-11-06

### Added

- **Vendored beads-merge by @neongreen** (bd-bzfy): Native `bd merge` command for intelligent JSONL merging
  - Vendored beads-merge algorithm into `internal/merge/` with full attribution and MIT license
  - New `bd merge` command as native wrapper (no external binary needed)
  - Same field-level 3-way merge algorithm, now built into bd
  - Auto-configured during `bd init` (both interactive and `--quiet` modes)
  - Thanks to @neongreen for permission to vendor: https://github.com/neongreen/mono/issues/240
  - Original tool: https://github.com/neongreen/mono/tree/main/beads-merge

- **Git Hook Version Detection** (bd-iou5, 991c624): `bd info` now detects outdated git hooks
  - Adds version markers to all git hook templates (pre-commit, post-merge, pre-push)
  - Warns when installed hooks are outdated or missing
  - Suggests running `examples/git-hooks/install.sh` to update
  - Prevents issues like the `--resolve-collisions` flag error after updates

- **Public API for External Extensions** (8f676a4): Extensibility improvements for third-party tools
- **Multi-Repo Patterns Documentation** (e73f89e): Comprehensive guide for AI agents working across multiple repositories
- **Snapshot Versioning** (a891ebe): Add versioning and timestamp validation for snapshots
- `--clear-duplicate-external-refs` flag for `bd import` command (9de98cf)

### Fixed

- **Multi-Workspace Deletion Tracking** (708a81c, e5a6c05, 4718583): Proper deletion tracking across multiple workspaces
  - Fixes issue where deletions in one workspace weren't propagated to others
  - Added `DeleteIssue` to Storage interface for backend extensibility (e291ee0)
- **Import/Export Deadlock** (a0d24f3): Prevent import/export from hanging when daemon is running
- **Pre-Push Hook** (3ba245e): Fix pre-push hook blocking instead of exporting
- **Hash ID Recognition** (c924731, 055f1d9): Fix `isHashID` to recognize Base36 hash IDs and IDs without a-f letters
- **Git Merge Artifacts** (41b1a21): Ignore merge artifacts in `.beads/.gitignore`
- **bd status Command** (1edf3c6): Now uses git history for recent activity detection
- **Performance**: Add raw string equality short-circuit before jsonEquals (5c1f441)

### Changed

- **Code Organization**:
  - Extract SQLite migrations into separate files (b655b29)
  - Centralize BD_DEBUG logging into `internal/debug` package (95cbcf4)
  - Extract `normalizeLabels` to `internal/util/strings.go` (9520e7a)
  - Reorganize project structure: move Go files to `internal/beads`, docs to `docs/` (584c266)
  - Remove unused `internal/daemonrunner/` package (~1,500 LOC) (a7ec8a2)

- **Testing**:
  - Optimize test suite with `testing.Short()` guards for faster local testing (11fa142, 0f4b03e)
  - Add comprehensive tests for merge driver auto-config (6424ebd)
  - Add comprehensive tests for 3-way merge functionality (14b2d34)
  - Add edge case tests for `getMultiRepoJSONLPaths()` (78c9d74)

- **CI/CD**:
  - Separate Homebrew update workflow with PAT support (739786e)
  - Add manual trigger to Homebrew workflow for testing (563c12b)
  - Fix Linux checksums extraction in Homebrew workflow (c47f40b)
  - Add script to automate Nix vendorHash updates (#235)

### Performance

- Cache `getMultiRepoJSONLPaths()` to avoid redundant calls (7afb143)

## [0.22.0] - 2025-11-05

### Added

- **Intelligent Merge Driver** (bd-omx1, 52c5059): Auto-configured git merge driver for JSONL conflict resolution
  - Vendors beads-merge algorithm for field-level 3-way merging
  - Automatically configured during `bd init` (both interactive and `--quiet` modes)
  - Matches issues by identity (id + created_at + created_by)
  - Smart field merging: timestamps→max, dependencies→union, status/priority→3-way
  - Eliminates most git merge conflicts in `.beads/beads.jsonl`

- **Onboarding Wizards** (b230a22): New `bd init` workflows for different collaboration models
  - `bd init --contributor`: OSS contributor wizard (separate planning repo)
  - `bd init --team`: Team collaboration wizard (branch-based workflow)
  - Interactive setup with fork detection and remote configuration
  - Auto-configures sync settings for each workflow

- **Migration Tools** (349817a): New `bd migrate-issues` command for cross-repo issue migration
  - Migrate issues between repositories while preserving dependencies
  - Source filtering (by label, priority, status, type)
  - Automatic remote repo detection and push
  - Complete multi-repo workflow documentation

- **Multi-Phase Development Guide** (3ecc16e): Comprehensive workflow examples
  - Multi-phase development (feature → integration → deployment)
  - Multiple personas (designer, frontend dev, backend dev)
  - Best practices for complex projects

- **Dependency Status** (3acaf1d): Show blocker status in `bd show` output
  - Displays "Blocked by N open issues" when dependencies exist
  - Shows "Ready to work (no blockers)" when unblocked

- **DevContainer Support** (247e659): Automatic bd setup in GitHub Codespaces
  - Pre-configured Go environment with bd pre-installed
  - Auto-detects existing `.beads/` and imports on startup

- **Landing the Plane Protocol** (095e40d): Session-ending checklist for AI agents
  - Quality gates, sync procedures, git cleanup
  - Ensures clean handoff between sessions

### Fixed

- **SearchIssues N+1 Query** (bd-5ots, e90e485): Eliminated N+1 query bug in label loading
  - Batch-loads labels for all issues in one query
  - Significant performance improvement for `bd list` with many labeled issues

- **Sync Validation** (bd-9bsx, 5438485): Prevent infinite dirty loop in auto-sync
  - Added export verification to detect write failures
  - Ensures JSONL line count matches database after export

- **bd edit Direct Mode** (GH #227, d4c73c3): Force `bd edit` to always use direct mode
  - Prevents daemon interference with interactive editor sessions
  - Resolves hang issues when editing in terminals

- **SQLite Driver on arm64 macOS** (f9771cd): Fixed missing SQLite driver in arm64 builds
  - Explicitly imports CGO-enabled sqlite driver
  - Resolves "database driver not found" errors on Apple Silicon

- **external_ref Type Handling** (e1e58ef): Handle both string and *string in UpdateIssue RPC
  - Fixes type mismatch errors in MCP server
  - Ensures consistent API behavior

- **Windows Test Stability** (2ac28b0, 8c5e51e): Skip flaky concurrent tests on Windows
  - Prevents false failures in CI/CD
  - Improves overall test suite reliability

### Changed

- **Test Suite Performance** (0fc4da7): Optimized test suite for 15-18x speedup
  - Reduced redundant database operations
  - Parallelized independent test cases
  - Faster CI/CD builds

- **Priority Format** (b8785d3): Added support for P-prefix priority format (P0-P4)
  - Accepts both `--priority 1` and `--priority P1`
  - More intuitive for GitHub/Jira users

- **--label Alias** (85ca8c3): Added `--label` as alias for `--labels` in `bd create`
  - Both singular and plural forms now work
  - Improved CLI ergonomics

- **--parent Flag in Daemon Mode** (fc89f15): Added `--parent` support in daemon RPC
  - MCP server can now set parent relationships
  - Parity with CLI functionality

### Documentation

- **Multi-Repo Migration Guide** (9e60ed1): Complete documentation for multi-repo workflows
  - OSS contributors, teams, multi-phase development
  - Addresses common questions about fork vs branch workflows

- **beads-merge Setup Instructions** (527e491): Enhanced merge driver documentation
  - Installation guide for standalone binary
  - Jujutsu configuration examples

## [0.21.9] - 2025-11-05

### Added

- **Epic/Child Filtering** (bd-zkl, fbe790a): New `bd list` filters for hierarchical issue queries
  - `--ancestor <id>`: Filter by ancestor issue (shows all descendants)
  - `--parent <id>`: Filter by direct parent issue
  - `--epic <id>`: Alias for `--ancestor` (more intuitive for epic-based workflows)
  - `ancestor_id` field added to issue type for efficient epic hierarchy queries

- **Advanced List Filters**: Pattern matching, date ranges, and empty checks
  - **Pattern matching**: `--title-contains`, `--desc-contains`, `--notes-contains` (case-insensitive substring)
  - **Date ranges**: `--created-after/before`, `--updated-after/before`, `--closed-after/before`
  - **Empty checks**: `--empty-description`, `--no-assignee`, `--no-labels`
  - **Priority ranges**: `--priority-min`, `--priority-max`

- **Database Migration** (bd-bb08, 3bde4b0): Added `ON DELETE CASCADE` to `child_counters` table
  - Prevents orphaned child counter records when issues are deleted
  - Comprehensive migration tests ensure data integrity

### Fixed

- **Import Timestamp Preservation** (8b9a486): Fixed critical bug where `closed_at` timestamps were lost during sync
  - Ensures closed issues retain their original completion timestamps
  - Prevents issue resurrection timestamps from overwriting real closure times

- **Import Config Respect** (7292c85): Import now respects `import.missing_parents` config setting
  - Previously ignored config for parent resurrection behavior
  - Now correctly honors user's preference for handling missing parents

- **GoReleaser Homebrew Tap** (37ed10c): Fixed homebrew tap to point to `steveyegge/homebrew-beads`
  - Automated homebrew formula updates now work correctly
  - Resolves brew installation issues

- **npm Package Versioning** (626d51d): Added npm-package to version bump script
  - Ensures `@beads/bd` npm package stays in sync with CLI releases
  - Prevents version mismatches across distribution channels

- **Linting** (52cf2af): Fixed golangci-lint errors
  - Added proper error handling
  - Added gosec suppressions for known-safe operations

### Changed

- **RPC Filter Parity** (510ca17): Comprehensive test coverage for CLI vs RPC filter behavior
  - Ensures MCP server and CLI have identical filtering semantics
  - Validates all new filters work correctly in both modes

## [0.21.8] - 2025-11-05

### Added

- **Parent Resurrection** (bd-58c0): Automatic resurrection of deleted parent issues from JSONL history
  - Prevents import failures when parent issues have been deleted
  - Creates tombstone placeholders for missing hierarchical parents
  - Best-effort dependency resurrection from JSONL

### Changed

- **Error Messages**: Improved error messages for missing parent issues
  - Old: `"parent issue X does not exist"`
  - New: `"parent issue X does not exist and could not be resurrected from JSONL history"`
  - **Breaking**: Scripts parsing exact error messages may need updates

### Fixed

- **JSONL Resurrection Logic**: Fixed to use LAST occurrence instead of FIRST (append-only semantics)
- **Version Bump Script**: Added `--tag` and `--push` flags to automate release tagging
  - Addresses confusion where version bump doesn't trigger GitHub release
  - New usage: `./scripts/bump-version.sh X.Y.Z --commit --tag --push`

## [0.21.7] - 2025-11-04

### Fixed

- **Memory Database Connection Pool** (bd-b121): Fixed `:memory:` database handling to use single shared connection
  - Prevents "no such table" errors when using in-memory databases
  - Ensures connection pool reuses the same in-memory instance
  - Critical fix for event-driven daemon mode tests

- **Test Suite Stability**: Fixed event-driven test flakiness
  - Added `waitFor` helper for event-driven testing
  - Improved timing-dependent test reliability

## [0.21.6] - 2025-11-04

### Added

- **npm Package** (bd-febc): Created `@beads/bd` npm package for Node.js/Claude Code for Web integration
  - Native binary downloads from GitHub releases
  - Integration tests and release documentation
  - Postinstall script for platform-specific binary installation

- **Template Support** (bd-164b): Issue creation from markdown templates
  - Create multiple issues from a single file
  - Structured format for bulk issue creation

- **`bd comment` Alias** (bd-d3f0): Convenient shorthand for `bd comments add`

### Changed

- **Base36 Issue IDs** (GH #213): Switched from hex to Base36 encoding for shorter, more readable IDs
  - Reduces ID length while maintaining uniqueness
  - More human-friendly format

### Fixed

- **SQLite URI Handling** (bd-c54b): Fixed `file://` URI scheme to prevent query params in filename
  - Prevents database corruption from malformed URIs
  - Fixed `:memory:` database connection strings

- **`bd init --no-db` Behavior** (GH #210): Now correctly creates `metadata.json` and `config.yaml`
  - Previously failed to set `no-db: true` flag
  - Improved metadata-only initialization workflow

- **Symlink Path Resolution**: Fixed `findDatabaseInTree` to properly resolve symlinks
- **Epic Hierarchy Display**: Fixed `bd show` command to correctly display epic child relationships
- **CI Stability**: Fixed performance thresholds, test eligibility, and lint errors

### Dependencies

- Bumped `github.com/anthropics/anthropic-sdk-go` from 1.14.0 to 1.16.0
- Bumped `fastmcp` from 2.13.0.1 to 2.13.0.2

## [0.21.5] - 2025-11-02

### Fixed

- **Critical Double JSON Encoding Bug** (bd-1048, bd-4ec8): Fixed widespread bug in daemon RPC calls where `ResolveID` responses were incorrectly converted using `string(resp.Data)` instead of `json.Unmarshal`. This caused IDs to become double-quoted (`"\"bd-1048\""`) and database lookups to fail. Affected commands:
  - `bd show` - nil pointer dereference and 3 instances of double encoding
  - `bd dep add/remove/tree` - 5 instances
  - `bd label add/remove/list` - 3 instances  
  - `bd reopen` - 1 instance
  
  All 12 instances fixed with proper JSON unmarshaling.

## [0.21.4] - 2025-11-02

### Added

- **New Commands**:
  - `bd status` - Database overview command showing issue counts and stats (bd-28db)
  - `bd comment` - Convenient alias for `bd comments add` (bd-d3f0)
  - `bd daemons restart` - Restart specific daemon without manual kill/start
  - `--json` flag for `bd stale` command

- **Protected Branch Workflow**:
  - `BEADS_DIR` environment variable for custom database location (bd-e16b)
  - `sync.branch` configuration for protected branch workflows (bd-b7d2)
  - Git worktree management with sparse checkout for sync branches (bd-a4b5)
    - Only checks out `.beads/` in worktrees, minimal disk usage
    - Only used when `sync.branch` is configured, not for default users
  - Comprehensive protected branch documentation

- **Migration & Validation**:
  - Migration inspection tools for AI agents (bd-627d)
  - Conflict marker detection in `bd import` and `bd validate`
  - Git hooks health check in `bd doctor`
  - External reference (`external_ref`) UNIQUE constraint and validation
  - `external_ref` now primary matching key for import updates (bd-1022)

### Fixed

- **Critical Fixes**:
  - Daemon corruption from git conflicts (bd-8931)
  - MCP `set_context` hangs with stdio transport (GH #153)
  - Double-release race condition in `importInProgress` flag
  - Critical daemon race condition causing stale exports

- **Configuration & Migration**:
  - `bd migrate` now detects and sets missing `issue_prefix` config
  - Config system refactored (renamed `config.json` → `metadata.json`)
  - Config version update in migrate command

- **Daemon & RPC**:
  - `bd doctor --json` flag not working (bd-6049)
  - `bd import` now flushes JSONL immediately for daemon visibility (bd-47f1)
  - Panic recovery in RPC `handleConnection` (bd-1048)
  - Daemon auto-upgrades database version instead of exiting

- **Windows Compatibility**:
  - Windows test failures (path handling, bd binary references)
  - Windows CI: forward slashes in git hook shell scripts
  - TestMetricsSnapshot/uptime flakiness on Windows

- **Code Quality**:
  - All golangci-lint errors fixed - linter now passes cleanly
  - All gosec, misspell, and unparam linter warnings resolved
  - Tightened file permissions and added security exclusions

### Changed

- Daemon automatically upgrades database schema version instead of exiting
- Git worktree management for sync branches uses sparse checkout (`.beads/` only)
- Improved test isolation and performance optimization

## [0.21.2] - 2025-11-01

### Changed
- Homebrew formula now auto-published in main repo via GoReleaser
- Deprecated separate homebrew-beads tap repository

## [0.21.1] - 2025-10-31

### Changed
- Version bump for consistency across CLI, MCP server, and plugin

## [0.20.1] - 2025-10-31

### Breaking Changes

- **Hash-Based IDs Now Default**: Sequential IDs (bd-1, bd-2) replaced with hash-based IDs (bd-a1b2, bd-f14c)
  - 4-character hashes for 0-500 issues
  - 5-character hashes for 500-1,500 issues  
  - 6-character hashes for 1,500-10,000 issues
  - Progressive length extension prevents collisions with birthday paradox math
  - **Migration required**: Run `bd migrate` to upgrade schema (removes `issue_counters` table)
  - Existing databases continue working - migration is opt-in
  - Dramatically reduces merge conflicts in multi-worker/multi-branch workflows
  - Eliminates ID collision issues when multiple agents create issues concurrently

### Removed

- **Sequential ID Generation**: Removed `SyncAllCounters()`, `AllocateNextID()`, and collision remapping logic (bd-c7af, bd-8e05, bd-4c74)
  - Hash IDs handle collisions by extending hash length, not remapping
  - `issue_counters` table removed from schema
  - `--resolve-collisions` flag removed from import (no longer needed)
  - 400+ lines of obsolete collision handling code removed

### Changed

- **Collision Handling**: Automatic hash extension on collision instead of ID remapping
  - Much simpler and more reliable than sequential remapping
  - No cross-branch coordination needed
  - Birthday paradox ensures extremely low collision rates

### Migration Notes

**For users upgrading from 0.20.0 or earlier:**

1. Run `bd migrate` to detect and upgrade old database schemas
2. Database continues to work without migration, but you'll see warnings
3. Hash IDs provide better multi-worker reliability at the cost of non-numeric IDs
4. Old sequential IDs like `bd-152` become hash IDs like `bd-f14c`

See README.md for hash ID format details and birthday paradox collision analysis.

## [0.20.0] - 2025-10-30

### Added
- **Hash-Based IDs**: New collision-resistant ID system (bd-168, bd-166, bd-167)
  - 6-character hash IDs with progressive 7/8-char fallback on collision
  - Opt-in via `.beads/config.toml` with `id_mode = "hash"`
  - Migration tool: `bd migrate --to-hash-ids` for existing databases
  - Prefix-optional ID parsing (e.g., `bd-abc123` or just `abc123`)
  - Hierarchical child ID generation for discovered-from relationships
- **Substring ID Matching**: All bd commands now support partial ID matching (bd-170)
  - `bd show abc` matches any ID containing "abc" (e.g., `bd-abc123`)
  - Ambiguous matches show helpful error with all candidates
- **Daemon Registry**: Multi-daemon management for multiple workspaces (bd-07b8c8)
  - `bd daemons list` shows all running daemons across workspaces
  - `bd daemons health` detects version mismatches and stale sockets
  - `bd daemons logs <workspace>` for per-daemon log viewing
  - `bd daemons killall` to restart all daemons after upgrades

### Fixed
- **Test Stability**: Deprecated sequence-ID collision tests
  - Kept `TestFiveCloneCollision` for hash-ID multi-clone testing
  - Fixed `TestTwoCloneCollision` to use merge instead of rebase
- **Linting**: golangci-lint v2.5.0 compatibility
  - Added `version: 2` field to `.golangci.yml`
  - Renamed `exclude` to `exclude-patterns` for v3 format

### Changed
- **Multiple bd Detection**: Warning when multiple bd binaries in PATH (PR #182)
  - Prevents confusion from version conflicts
  - Shows locations of all bd binaries found

## [0.17.7] - 2025-10-26

### Fixed
- **Test Isolation**: Export test failures due to hash caching between subtests
  - Added `ClearAllExportHashes()` method to SQLiteStorage for test isolation
  - Export tests now properly reset state between subtests
  - Fixes intermittent test failures when running full test suite

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
  - Configurable via environment variables: `BEADS_DAEMON_LOG_MAX_SIZE`, `BEADS_DAEMON_LOG_MAX_BACKUPS`, `BEADS_DAEMON_LOG_MAX_AGE`, `BEADS_DAEMON_LOG_COMPRESS`
  - Optional compression of rotated logs
  - Defaults: 50MB max size, 7 backups, 30 day retention, compression enabled
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
