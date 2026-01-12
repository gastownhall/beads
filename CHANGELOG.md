# Changelog

All notable changes to the beads project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.47.1] - 2026-01-12

### Added

- **`bd list --ready` flag** - Show only issues with no blockers (bd-ihu31)
  - Filters to issues that are immediately actionable
  - Equivalent to `bd ready` but integrated into list command

- **Markdown rendering in comments** - Comment text now renders Markdown (#1019)
  - Enhanced readability for formatted notes and descriptions

### Changed

- **Release formula improvements** - Updated beads-release formula with v0.47.0 learnings
  - Better gate handling and step organization

### Fixed

- **Nil pointer in wisp create** - Prevent panic when creating wisps (mol)
  - Fixed nil pointer dereference in molecule creation

- **Route prefix for rig issues** - Use correct prefix when creating issues in rigs (#1028)
  - Issues created in rigs now use the proper routing prefix

- **Duplicate merge target selection** - Prefer issues with children/deps (GH#1022)
  - Better heuristics for choosing merge target in duplicate detection

- **SQLite cache rebuild** - Rebuild blocked_issues_cache after rename-prefix (GH#1016)
  - Ensures cache consistency after prefix changes

- **Doctor JSONL check** - Exclude sync_base.jsonl from multiple files check (#1021)
  - Reduces false positives in doctor diagnostics

- **Merge struct completeness** - Add QualityScore field to merge Issue struct
  - Ensures all fields preserved during merge operations

- **MCP custom types** - Support custom issue types and statuses in MCP (#1023)
  - MCP server now handles non-built-in types correctly

- **Hyphenated prefix validation** - Support hyphens in ValidateIDFormat (#1013)
  - Prefixes like `my-project-` now validate correctly

- **Git worktree initialization** - Prevent bd init inside git worktrees (#1026)
  - Avoids configuration issues when initializing in worktree directories

### Documentation

- **bd reset clarification** - Document command behavior and workarounds (GH#922)
  - Clearer guidance on reset command usage

## [0.47.0] - 2026-01-11

### Added

- **Pull-first sync with 3-way merge** - Major sync improvement (#918)
  - Reconciles local changes with remote updates before pushing
  - Field-level conflict merging reduces manual intervention
  - Base state tracking for better change detection

- **`bd resolve-conflicts` command** - Resolve JSONL merge conflict markers (bd-7e7ddffa)
  - Mechanical mode uses updated_at timestamps for deterministic resolution
  - Closed status wins over open, higher priority wins
  - Notes concatenated, dependencies unioned
  - Dry-run mode and JSON output for agent integration

- **`bd create --dry-run`** - Preview issue creation without side effects (bd-0hi7)
  - Shows what would be created in human-readable or JSON format
  - Works with --rig/--prefix flags

- **Gate auto-discovery** - Auto-discover workflow run ID in `bd gate check` (bd-fbkd)
  - Queries GitHub directly when await_id is a workflow name hint
  - ZFC-compliant: takes most recent run deterministically

- **Linear project filter** - `linear.project_id` config for sync (#938)
  - Fetch only issues from a specific project instead of all team issues

- **`bd ready --gated`** - Gate-resume discovery for molecules (bd-lhalq)
  - Find molecules waiting on gates for automatic resumption

- **Multi-repo custom types** - Trust and discover types across repositories (bd-62g22, bd-9ji4z)
  - `bd doctor` discovers custom types from multiple repos
  - Non-built-in types trusted during hydration

- **Visual UX improvements** - Enhanced display for list tree, graph, and show commands
  - Better formatting and readability

- **Stale database handling** - AllowStale option in List API (bd-dpkdm)
  - Read-only commands auto-import on stale DB (#977, #982)
  - Cold-start bootstrap for read commands

- **Batch molecule operations** - `bd mol burn` supports multiple molecules (feat(mol))

- **Redirect health checks** - `bd doctor` validates redirect configurations

- **Schema extensions** - New fields for HOP integration
  - `crystallizes` column in sqlite storage
  - `attests` edge type for skill attestations
  - `owner` field for human attribution
  - `actor` fallback includes git user.name (#994)

### Fixed

- **Daemon mode completeness** - Several daemon mode gaps closed (GH#952)
  - `--due` and `--defer` flags now work in daemon mode (#953)
  - `bd dep add/remove --json` returns proper JSON output (#961)
  - `DeferUntil` field parsed correctly in daemon handleCreate (#950)
  - Silence deprecation warnings in `--json` mode (#1039a691)

- **Sync robustness**
  - Canonicalize dbPath to fix filepath.Rel errors (GH#959, #960)
  - Validate custom types in batch issue creation (#943)
  - Force-add .beads in worktree for contributor mode (#947)
  - Initialize store after daemon disconnect (GH#984)
  - `sync --import-only` works when daemon was connected

- **Windows fixes**
  - Infinite loop in findLocalBeadsDir/findOriginalBeadsDir (GH#996)
  - `bd init` no longer hangs when not in a git repo (#991)
  - Daemon stop/kill uses proper Windows API (GH#992)
  - SQLite uses DELETE mode on WSL2 Windows filesystem (GH#920)

- **Daemon socket handling** - Long workspace paths now work (GH#1001, #1008)
  - Socket path shortening for deep directory structures
  - Relocate daemon socket for deep paths

- **Prevent data corruption**
  - FK constraint failures on batch/concurrent issue creation (GH#956)
  - Prevent closing issues with open blockers (GH#962)
  - Nil pointer panic in dep --json mode (GH#998)

- **Doctor improvements**
  - Recognize bd shims when external manager config exists (GH#946)
  - Detect lefthook jobs syntax (GH#981)
  - Add .sync.lock and sync_base.jsonl to gitignore (#980)

- **Prime command** - Use flush-only workflow when no git remote configured (#940)

- **Install safety** - Stop existing daemons before binary replacement (#945)

- **Git hooks** - Add `--no-daemon` to sync commands to prevent inline import failures (#948)

- **Linear sync** - Use project_id when creating issues via `sync --push` (GH#973, #1012)

- **Team wizard** - Validate sync.branch in wizard and migrate commands (GH#923)

- **Worktree fixes**
  - Skip beads restore when directory is redirected (bd-lmqhe)
  - Migrate sync works in git worktree environments (#970)

- **Misc fixes**
  - `bd edit` parses EDITOR with args (GH#987)
  - Use SearchIssues for ID resolution (GH#942)
  - Respect hierarchy.max-depth config setting (GH#995, #997)
  - Add timeout to daemon request context to prevent hangs
  - Avoid null values in Claude settings hooks (GH#955)
  - Restore Gas Town types (agent, role, rig, convoy, slot) (GH#941)
  - Add TypeRig constant and IsBuiltIn method (GH#1002)

### Changed

- **Daemon CLI refactor** - Consolidated to subcommands with semantic styling (#1006)

- **Release formula refactor** - Bump script broken into individual version-update steps (bd-a854)
  - More durable: can resume from specific step if interrupted
  - Better visibility in activity feed

### Documentation

- Add lazybeads (Bubble Tea TUI by @codegangsta) to community tools (#951)
- Fix `bd quickstart` link to database extension documentation (#939)

## [0.46.0] - 2026-01-06

### Added

- **Custom type support** - Configure custom issue types in beads config.yaml (bd-649s)
  - Define project-specific types beyond the built-in set
  - Types persist across sync and export

- **Gas Town types extraction** - Core Gas Town types moved into beads package (bd-i54l)
  - Enables beads to understand rig identities and agent workflows
  - Foundation for deeper Gas Town integration

### Fixed

- **Gate workflow discovery** - Handle workflow name hints in `gh:run` gate discovery (bd-m8ew)
  - Better matching of GitHub Actions workflow runs
  - Consolidated numeric ID handling

## [0.45.0] - 2026-01-06

### Added

- **Dynamic shell completions** - Tab completion for issue IDs in bash/zsh/fish (#935)
  - Optimized prefix filtering for faster completion
  - Added completions to more commands

- **Android/Termux support** - Native ARM64 binaries for Android (#887)

- **Deep pre-commit framework integration** - `bd doctor` checks pre-commit hook configs (bd-28r5)

- **Rig identity bead type** - New `rig` bead type for Gas Town rig tracking (gt-zmznh)

- **`--filter-parent` alias** - Alternative to `--parent` in `bd list` (bd-3p4u)

- **Unified auto-sync config** - Simpler daemon configuration for agent workflows (#904)

- **BD_SOCKET env var** - Test isolation for daemon socket paths (#914)

### Fixed

- **External hook manager detection** - `bd doctor` now detects lefthook, husky, pre-commit, and other hook managers
  - Checks if external managers have `bd hooks run` integration configured
  - Reports which hooks have bd integration vs which are missing
  - `bd doctor --fix` uses `--chain` flag when external managers detected to preserve existing hooks
  - Supports YAML, TOML, and JSON config formats for lefthook
  - Detects active manager from git hooks when multiple managers present

- **Init branch persistence** - `--branch` flag now correctly persists to config.yaml (#934)

- **Worktree resolution** - Resolve worktrees by name from git registry (#921)

- **Sync with redirect** - Use inline import for `--import-only` with .beads/redirect (bd-ysal)
  - Handle .beads/redirect in git status checks (bd-arjb)
  - Persist sync branch to yaml and database (GH#909)
  - Atomic export and force-push detection (bd-3bhl, bd-4hh5)

- **Doctor improvements**
  - Recognize lowercase 's' skip-worktree flag (#931)
  - Improve messaging for detection-only hook managers (bd-par1)
  - Align duplicate detection with `bd duplicates` (bd-sali)
  - Query metadata table instead of config for last_import_time (#916)

- **Update prefix routing** - `bd update` now routes like `bd show` (bd-618f)

- **Formula phase** - beads-release formula marked as vapor phase (gt-mjsjv)

- **Lint fixes** - Address gosec, misspell, and unparam warnings

- **CI improvements** - Windows smoke tests, timeouts, golangci-lint exclusions

### Changed

- **Test refactoring** - Replace testify with stdlib in daemon tests (#936)

### Documentation

- Add bun installation method to INSTALLING.md (#930)
- Add Parade app to community tools list

## [0.44.0] - 2026-01-04

### Added

- **Recipe-based setup architecture** - Major refactor of `bd init` (bd-i3ed)
  - Modular, composable setup via recipes
  - Cleaner separation of init concerns

- **Gate evaluation system** - Phases 2-4
  - `bd gate check` for timer and GitHub gate evaluation (GH#884)
  - `bd gate discover` for auto-discovery of gh:run await_id (bd-z6kw)
  - `bd gate add-waiter` and `bd gate show` for phase handoff
  - Cross-rig bead gate support
  - Gate-aware beads-release.formula v2 (bd-r24e)
  - Merge-slot gate for serialized conflict resolution

- **Dependency command improvements**
  - `--blocks` shorthand flag for natural dependency syntax (GH#884)
  - `--blocked-by` and `--depends-on` flag aliases (bd-09kt)

- **Multi-prefix support** - `allowed_prefixes` config option (#881)
  - Configure multiple valid prefixes per rig
  - Enables flexible issue routing

- **Sync improvements**
  - Detect uncommitted JSONL changes before sync (GH#885)
  - `bd doctor` sync divergence check for JSONL/SQLite/git
  - `BD_DEBUG_SYNC` env for protection debugging

- **`PRIME.md` override** - Workflow customization (GH#876)
  - Custom prime output per project

- **Daemon config** - `daemon.auto_*` settings in config.yaml (GH#871)

- **Compound visualization** - `bd mol show` displays compound structure (bd-iw4z)

- **`/handoff` skill** - Session cycling slash command (bd-xwvo)

### Fixed

- **`bd ready` now shows in_progress issues** (#894)
  - Previously filtered out your active work

- **`bd show` displays external_ref field** in text output (#899)

- **macOS case-insensitive path handling** (GH#880)
  - Worktree validation, daemon paths, git operations
  - Canonicalize path case for consistency

- **Sync metadata timing** - Finalize after commit, not push (GH#885)
  - Defer SQLite metadata updates until after git commit
  - Prevents sync state corruption

- **Sparse checkout isolation** - Prevent config leaking to main repo (GH#886)
  - Disable sparse checkout on main repo after worktree creation

- **`close_reason` preserved** during merge/sync (GH#891)

- **Parent hub contamination** during `bd init` (GH#896)

- **NoDb mode** - Set cmdCtx.StoreActive correctly (GH#897)

- **Event storm prevention** when gitRefsPath is empty (#883)

- **Doctor improvements**
  - Detect status mismatches between DB and JSONL (GH#885)
  - Detect missing git repo, improve daemon startup message (#890)
  - Skip JSONL tracking checks in sync-branch mode (GH#858)

- **`bd rename-prefix`** - Sync JSONL before and after (#893)

- **`sync.remote` config** now respected in `bd sync` (GH#872)

- **Snapshot protection** made timestamp-aware (GH#865)

- **Hyphenated rig names** supported in agent IDs (GH#854, GH#868)

- **Cross-repo agent routing** (#864)

- **Config key normalization** - GetYamlConfig matches SetYamlConfig (#874)

- **Daemon startup failure** - Propagate reason to user (GH#863)

- **Molecule variable substitution** in root bead title/desc

- **Submodule detection** - Correct main repo root detection (#849)

- **`bd merge` output** sorted by issue id (#859)

- **Auto-create agent bead** when `bd agent state` called on non-existent agent

- **Windows zip extraction** - Add retry logic for npm install

- **Cycle detection** runs in daemon mode for --blocks flag

- **`bd slot set`** cross-beads prefix routing (bd-hmeb)

- **`git status` noise** - Hide issues.jsonl when sync.branch configured (GH#870)

### Changed

- **Skill names** updated from `/bd-*` to `/beads:*` (#862)

### Performance

- **Batch external dep checks** by project (bd-687v)

### Internal

- Extract warnIfCyclesExist helper
- Gate field parsing and creation tests
- Sync unit tests for gitHasUncommittedBeadsChanges
- SQLite cache rebuild benchmarks
- Repository guards in deployment workflows

## [0.43.0] - 2026-01-02

### Added

- **Step.Gate evaluation** - Phase 1: Human Gates
  - Gate steps that require human approval before proceeding
  - Foundation for workflow control points

- **`bd lint` command** - Template validation
  - Validate issue templates against schema
  - `--validate` flag also added to `bd create`

- **`bd ready --pretty`** - Formatted output
  - Human-friendly display of ready work

### Fixed

- **Cross-rig routing** - `bd close` and `bd update` now support cross-rig operations via prefix routing
- **Agent ID validation** - Now accepts any rig prefix (GH#827)
- **`bd sync` in bare repo worktrees** - Fixed exit 128 error (GH#827)
- **`bd --no-db dep tree`** - Now shows complete tree (GH#836)
- **`.beads/last-touched`** - Restored to gitignore template (GH#838)

## [0.42.0] - 2025-12-30

### Added

- **`llms.txt` standard support** - AI agent discoverability (#784)
  - Standard endpoint for AI agents to discover project info
  - Helps LLM-powered tools understand your project

- **`bd preflight` command** - PR readiness checks
  - Static checklist for pre-commit/pre-push verification
  - Phase 1 implementation with test, lint, and version checks planned

- **`--claim` flag for `bd update`** - Work queue semantics
  - Atomic claim-and-assign for multi-agent coordination
  - Prevents race conditions in work assignment

- **`bd state` and `bd set-state`** - Label-based state management
  - Helper commands for operational state patterns
  - Documents labels-as-state pattern

- **`bd activity --town`** - Cross-rig activity feed
  - Aggregated view across all rigs in town
  - Better visibility into multi-rig workflows

- **Convoy issue type** - Reactive completion tracking
  - Issues that complete when all tracked items complete
  - `tracks` relation type for convoy membership

- **Agent identity trailers** - prepare-commit-msg hook
  - Automatically adds agent identity to commits
  - Structured labels for agent beads

- **Daemon RPC endpoints** - Config and mol stale
  - Remote config queries via daemon
  - Stale molecule detection endpoint

- **Non-TTY auto-detection** - Cleaner output in pipes
  - Automatically adjusts output for non-interactive use

### Fixed

- **Git hook chaining** now works correctly (GH#816)
- **`.beads/redirect` not committed** - Prevents worktree conflicts (GH#814)
- **`bd sync` with sync-branch** - Fixes worktree copy direction (GH#810, #812)
- **`sync.branch` validation** - Rejects main/master as sync branch (GH#807)
- **Read operations read-only** - No more database writes on list/ready/show (GH#804)
- **`bd list` defaults** - Non-closed issues, 50 limit to protect context (GH#788)
- **External direct-commit bypass** - When sync.branch configured (bd-n663)
- **Migration 022 syntax** - SQL error on v0.30.3 upgrade path
- **MCP redirect support** - Plugin follows .beads/redirect files
- **Jira sync error** - Better message when Python script not found (GH#803)
- **Doctor false positives** - For molecule/wisp prefix variants
- **BD_ACTOR in direct mode** - Consistent actor handling
- **Label accumulation** - When updating agent role_type/rig

## [0.41.0] - 2025-12-29

### Added

- **`bd swarm` commands** - Multi-agent batch work coordination
  - `bd swarm create` - Create swarm from epic with children
  - `bd swarm status` - Show swarm progress and blocked work
  - `bd swarm validate` - Validate swarm structure and dependencies
  - N+1 query fix for blocked checks (performance optimization)

- **`bd repair` command** - Orphaned reference repair
  - Detect and repair orphaned foreign key references
  - Support for comments/events orphan detection
  - `--json` flag for machine-readable output
  - Transaction safety with backup and dirty_issues marking

- **`bd compact --purge-tombstones`** - Dependency-aware cleanup
  - Remove tombstones while respecting dependency order
  - Safe cleanup that won't break DAG structure

- **`bd init --from-jsonl`** - Manual cleanup preservation
  - Initialize database from curated JSONL file
  - Preserves manual edits made to JSONL

- **`bd human` command** - Focused help menu
  - Human-friendly command reference
  - Quick overview without CLI verbosity

- **`bd show --short`** - Compact output mode
  - Brief issue summary for scripting
  - Less verbose than default format

- **`bd delete --reason`** - Audit trail for deletions
  - Optional reason stored in activity log
  - Better traceability for issue cleanup

- **`hooked` status** - Hook-based work assignment
  - New status for issues assigned to agent hooks
  - Enables autonomous agent work pickup

- **`mol_type` schema field** - Molecule classification
  - Track molecule type (patrol, work, etc.)
  - New migration (adds schema field)

- **Agent ID canonical naming** - Validation update
  - Updated validation for orchestrator naming conventions
  - Supports rig/role/name format

### Fixed

- **`--var` flag allows commas in values** (GH#786)
  - Variables like `--var files=a.go,b.go` now work correctly
  - Parser respects quoted values

- **`bd sync` in bare repo worktrees** (GH#785)
  - Fixed sync failure when working in worktrees of bare repos
  - Correctly detects git configuration

- **`bd delete --cascade` recursive deletion** (GH#787)
  - Now properly deletes all transitive dependents
  - Previously only deleted direct children

- **`bd doctor` pre-push hook detection** (GH#799)
  - No longer falsely reports hook issues
  - Correctly identifies bd-managed hooks

- **Gitignore fork protection** (GH#796)
  - Removed negations that could override protection
  - Safer fork handling

- **Illumos/Solaris disk space check** (GH#798)
  - Added platform support for disk space detection
  - Expands OS compatibility

- **Pre-migration orphan cleanup** - Chicken-and-egg fix
  - Clean orphans before migration to avoid failures
  - Smoother upgrade path

- **`hq-` prefix routing** - Town root discovery
  - Correctly finds routes.jsonl from anywhere in town
  - Fixes cross-rig routing for HQ beads

- **Config.yaml database override warning**
  - Shows warning when config overrides db location
  - Helps debug unexpected behavior

- **`normalizeBeadsRelPath` edge case** - Similar prefix handling
  - Fixes path normalization for similar prefix names
  - e.g., `beads` vs `beads-sync`

- **`bd doctor --fix` redirect handling**
  - Properly follows .beads/redirect files
  - Limited verbose output for cleaner runs

### Changed

- **CLI command consolidation** - Reduced surface area
  - Grouped related commands under parent commands
  - Cleaner `bd --help` output

- **Code organization** - File size limits
  - Split large cmd/bd files to meet 800-line limit
  - init.go: 1928 → 705 lines
  - Improved maintainability

- **Documentation updates**
  - Replace Epic Planning with Ready Front model
  - Add components overview (CLI vs Plugin vs MCP)
  - Add installation method comparison table

### Internal

- Extract IssueDetails to shared type
- Export FollowRedirect and consolidate implementations
- Extract shared getEpicChildren helper for swarm commands
- Extract hashFieldWriter to reduce ComputeContentHash repetition
- Break up runCook (275 lines) into focused helpers
- Break up flushToJSONLWithState (280 lines) into focused helpers
- Extract shared importFromJSONLData function
- Consolidate duplicated step collection functions
- Add git helper and guard annotations for tests
- Fix golangci-lint errors (errcheck, gosec, unparam)

## [0.40.0] - 2025-12-28

### Added

- **`bd worktree` command** - Parallel development support
  - Manage git worktrees with beads integration
  - Enables working on multiple issues simultaneously

- **`bd slot` commands** - Agent bead slot management
  - Track agent assignments with dedicated slot operations
  - Supports multi-agent orchestration workflows

- **`bd agent state` command** - ZFC-compliant state reporting
  - Report agent state in standardized format
  - Enables machine-readable status checks

- **`bd doctor --deep`** - Full graph integrity validation
  - Deep validation of bead dependency graphs
  - Includes agent bead integrity checks

- **Agent bead support** - New bead types
  - `type=agent` and `type=role` for agent tracking
  - Agent-specific fields in schema (migration 030)
  - Agent ID pattern validation on create

- **Computed `.parent` field** - JSON output enhancement
  - Parent field included in JSON for convenience
  - Simplifies integration with external tools

- **Auto-bypass daemon for wisp operations** - Performance
  - Ephemeral wisp operations skip daemon overhead
  - Faster patrol cycle processing

- **Pour warning for vapor-phase formulas** - Safety
  - Warning when pouring formulas designed for wisps
  - Prevents accidental persistent molecule creation

### Fixed

- **O(2^n) → O(V+E) cycle detection** (GH#775) - Performance
  - Replaced exponential algorithm with linear DFS
  - Dramatic speedup for large dependency graphs

- **Import hash mismatch warnings** - Data integrity
  - Update jsonl_file_hash on import operations
  - Prevents spurious hash mismatch warnings

### Deprecated

The following commands are deprecated and will be removed in v1.0.0:

- **`bd relate`** → use `bd dep relate` instead
- **`bd unrelate`** → use `bd dep unrelate` instead
- **`bd daemons`** → use `bd daemon <subcommand>` instead
- **`bd cleanup`** → use `bd admin cleanup` instead
- **`bd compact`** → use `bd admin compact` instead
- **`bd reset`** → use `bd admin reset` instead
- **`bd comment`** → use `bd comments add` instead
- **`bd template`** → use `bd mol` instead
- **`bd templates`** → use `bd formula list` instead
- **`bd template show`** → use `bd mol show` instead
- **`bd template bond`** → use `bd mol bond` instead
- **`bd detect-pollution`** → use `bd doctor --check=pollution` instead
- **`bd migrate-hash-ids`** → use `bd migrate hash-ids` instead
- **`bd migrate-tombstones`** → use `bd migrate tombstones` instead
- **`bd migrate-sync`** → use `bd migrate sync` instead
- **`bd migrate-issues`** → use `bd migrate issues` instead

All deprecated commands continue to work but print a warning. Update your scripts
and muscle memory before v1.0.0 to avoid breakage.

### Changed

- **Community tools documentation** (GH#772, GH#776)
  - Consolidated into dedicated docs page
  - Added opencode-beads to community tools list

- **Activity feed improvements** - Better context
  - Shows title and assignee in activity entries
  - More informative `bd activity` output

## [0.39.1] - 2025-12-27

### Added

- **`bd where` command** - Show active beads location
  - Displays the resolved database path after following redirects
  - Helpful for debugging multi-rig setups

- **`--parent` flag for `bd update`** - Reparent issues
  - Move issues between epics with `bd update <id> --parent=<new-parent>`
  - Supports clearing parent with `--parent=none`

- **Redirect info in `bd prime`** - Multi-clone support
  - Shows when database is redirected to another location
  - Improves visibility into routing behavior

### Fixed

- **Doctor follows redirects** - Multi-clone compatibility
  - `bd doctor` now correctly follows database redirects
  - Prevents false negatives when running from rig roots

- **Remove 8-char prefix limit** (GH#770) - `bd rename-prefix`
  - Removed arbitrary length restriction on issue prefixes
  - Allows longer, more descriptive prefixes

### Changed

- **Git context consolidation** - Internal refactor
  - Unified git context into single cached struct
  - Reduces redundant git operations

### Documentation

- **Database Redirects section** - ADVANCED.md
  - Comprehensive documentation for redirect feature
  - Explains multi-clone integration patterns

- **Community Tools update** (GH#771) - README.md
  - Added opencode-beads to community tools list

## [0.39.0] - 2025-12-27

### Added

- **`bd orphans` command** (GH#767) - Detect orphaned issues
  - Finds issues mentioned in git commits that were never closed
  - DRY refactoring of orphan detection from `bd doctor`
  - Helps maintain issue hygiene in larger projects

- **`bd admin` parent command** - Consolidated admin tools
  - `bd admin cleanup` - Clean up unused data
  - `bd admin compact` - Compact the database
  - `bd admin reset` - Reset to initial state
  - Reduces top-level command clutter

- **`--prefix` flag for `bd create`** - Cross-rig issue creation
  - Create issues in other rigs from any directory
  - Accepts prefix (`bd-`), short form (`bd`), or rig name (`beads`)
  - Works with existing routes.jsonl routing

### Changed

- **`bd mol catalog` → `bd formula list`** - Command rename
  - Aligns with formula-based terminology
  - `bd formula list` shows available molecule templates

- **`bd info --thanks`** - Relocated thanks command
  - Contributors list moved under `bd info`
  - Reduces command namespace pollution

- **Removed unused commands**
  - `bd pin`, `bd unpin`, `bd hook` removed
  - Functionality covered by orchestrator molecule commands
  - Cleaner separation between beads (data) and orchestration

- **`bd doctor --check=pollution`** - Integrated test pollution check
  - Detects test artifacts left in production database
  - Previously standalone `bd detect-pollution` command

### Fixed

- **macOS codesigning** - Fixed `bump-version.sh --install`
  - Ad-hoc signing for local installations
  - Prevents macOS from quarantining the binary

- **Lint errors and Nix vendorHash** (GH#769)
  - Resolved golangci-lint issues
  - Updated Nix package hash for reproducible builds

### Documentation

- **Issue Statuses section** - CLI_REFERENCE.md
  - Comprehensive status lifecycle documentation
  - Clear explanations of open, pinned, in_progress, blocked, deferred, closed, tombstone

- **Consolidated UI_PHILOSOPHY files** (GH#745)
  - Merged duplicate philosophy docs
  - Single source of truth for UI design principles

- **README and PLUGIN.md fixes** (GH#763)
  - Corrected installation instructions
  - Updated plugin documentation

## [0.38.0] - 2025-12-27

### Added

- **Prefix-based routing** - Cross-rig command routing
  - `bd` commands auto-route to correct rig based on issue ID prefix
  - Routes stored in `~/gt/.beads/routes.jsonl`
  - Enables seamless multi-rig workflows from any directory

- **Cross-rig ID auto-resolve** - Smarter dependency handling
  - `bd dep add` auto-resolves issue IDs across different rigs
  - No need to specify full paths for cross-rig dependencies

- **`bd mol pour/wisp` subcommands** - Reorganized command hierarchy
  - `bd mol pour` for persistent molecules
  - `bd mol wisp` for ephemeral workflows
  - Cleaner organization under `bd mol` namespace

- **Comments display in `bd show`** (GH#177) - Enhanced issue details
  - Comments now visible in issue output
  - Shows full discussion thread for issues

- **`created_by` field on issues** (GH#748) - Creator tracking
  - Track who created each issue for audit trail
  - Useful for multi-agent workflows

- **Database corruption recovery** (GH#753) - Robust doctor repairs
  - `bd doctor --fix` can now auto-repair corrupted SQLite databases
  - JSONL integrity checks detect and fix malformed entries
  - Git hygiene checks for stale branches

- **Chaos testing for releases** - Thorough validation
  - `--run-chaos-tests` flag in release script
  - Exercises edge cases and failure modes

- **Pre-commit config** - Local lint enforcement
  - Consistent code quality before commits

### Changed

- **CLI consolidation** - Reduced top-level command clutter
  - `bd cleanup`, `bd compact`, `bd reset` → `bd admin cleanup|compact|reset`
  - `bd migrate-*` → `bd migrate hash-ids|issues|sync|tombstones`
  - `bd cook` → `bd formula cook`
  - `bd detect-pollution` → `bd doctor --check=pollution`
  - `bd quickstart` hidden, use docs/QUICKSTART.md instead
  - Hidden aliases maintain backwards compatibility with deprecation notices

- **Sync backoff and tips consolidation** (GH#753) - Smarter daemon
  - Daemon uses exponential backoff for sync retries
  - Tips consolidated from multiple sources

- **Wisp/Ephemeral naming finalized** - `wisp` is canonical
  - `bd mol wisp` is the correct command
  - Internal API uses "ephemeral" but CLI uses "wisp"

### Fixed

- **Comments display position** (GH#756) - Formatting fix
  - Comments now display outside dependents block
  - Proper visual hierarchy in `bd show` output

- **no-db mode storeActive** (GH#761) - JSONL-only fix
  - `storeActive` correctly set in no-database mode
  - Fixes issues with JSONL-only installations

- **`--resolution` alias** (GH#746) - Backwards compatibility
  - Restored `--resolution` as alias for `--reason` on `bd close`

- **`bd graph` with daemon** (GH#751) - Daemon compatibility
  - Graph generation works when daemon is running
  - No more conflicts between graph and daemon operations

- **`created_by` in RPC path** (GH#754) - Daemon propagation
  - Creator field correctly passed through daemon RPC

- **Migration 028 idempotency** (GH#757) - Safe re-runs
  - Migration handles partial or repeated runs gracefully
  - Checks for existing columns before adding

- **Routed ID daemon bypass** - Cross-rig show
  - `bd show` with routed IDs bypasses daemon correctly
  - Storage connections closed per iteration to prevent leaks

- **Modern git init** (GH#753) - Test compatibility
  - Tests use `--initial-branch=main` for modern git versions

- **golangci-lint clean** (GH#753) - All platforms
  - Resolved all lint errors across platforms

### Improved

- **Test coverage** - Comprehensive testing
  - Doctor, daemon, storage, and RPC client paths covered
  - Chaos testing integration for edge cases

## [0.37.0] - 2025-12-26

### Added

- **Gate commands** - Async coordination for agent workflows
  - `bd gate create` - Create gates with await conditions (gh:run, gh:pr, timer, human, mail)
  - `bd gate eval` - Evaluate timer and GitHub gates (checks run completion, PR merge)
  - `bd gate approve` - Approve human gates
  - `bd gate show/list/close/wait` - Full gate lifecycle management

- **`bd close --suggest-next`** (GH#679) - Smart workflow continuation
  - Shows newly unblocked issues after closing
  - Helps agents find next actionable work

- **`bd ready/blocked --parent`** (GH#743) - Epic-scoped filtering
  - Filter issues by parent bead or epic
  - Enables focused work within a subtree

- **TOML support for formulas** - Alternative format
  - `.formula.toml` files alongside JSON support
  - Human-friendly authoring option

- **Fork repo auto-detection** (GH#742) - Contributor workflow
  - Detects fork repositories during init
  - Offers to configure .git/info/exclude for stealth mode

- **Control flow operators** - Advanced formula composition
  - `loop` operator for iterating over collections
  - `gate` operator for conditional execution
  - Condition evaluator for gates and loops
  - Control flow integrates with aspect composition

- **Aspect composition** - Cross-cutting workflow concerns
  - `aspects` field in formulas for reusable patterns
  - Aspects match steps by type, title pattern, or custom criteria
  - Enables logging, retries, notifications as composable concerns

- **Runtime expansion types** - Dynamic step generation
  - `on_complete` - Generate steps when a step completes
  - `for-each` - Expand steps for each item in a collection
  - Enables dynamic workflows based on runtime state

- **`bd formula list/show`** - Formula discovery commands
  - `bd formula list` - List available formulas in search paths
  - `bd formula show <name>` - Display formula definition and metadata

- **`bd mol stale`** - Detect complete-but-unclosed molecules
  - Finds molecules where all children are closed but molecule is open
  - Helps identify forgotten cleanup tasks

- **Stale molecules check in doctor** - Proactive detection
  - `bd doctor` now warns about stale molecules
  - Integrated with `bd mol stale` detection

- **Distinct ID prefixes** - Clear entity type identification
  - Protos: `bd-proto-xxx` prefix
  - Molecules: `bd-mol-xxx` prefix
  - Wisps: `bd-wisp-xxx` prefix
  - Makes entity type immediately visible in IDs

- **`no-git-ops` config** (GH#593) - Manual git control
  - `bd config set no-git-ops true` to disable auto git operations
  - `bd prime` outputs stealth-mode protocol when enabled
  - Useful for custom git workflows or manual commit review

### Changed

- **Formula format: YAML → JSON** - Standardized format
  - Formulas now use `.formula.json` extension
  - JSON provides better tooling support and validation
  - Existing YAML formulas need migration

- **Removed `bd mol run`** - Orchestration delegated to orchestrator
  - Molecule execution now handled by orchestrator commands
  - `bd` focuses on issue tracking primitives
  - Use orchestrator's molecule runner for execution

- **Simplified wisp architecture** - Single database model
  - Wisps stored in main database with `Wisp=true` flag
  - Removed separate `.beads-wisp/` directory
  - Wisps filtered from JSONL export automatically

### Fixed

- **Gate await fields preserved during upsert** - Multirepo sync
  - Gate fields no longer cleared during multi-repo sync operations
  - Enables reliable gate coordination across clones

- **Tombstones retain closed_at timestamp** - Soft delete metadata
  - Tombstones preserve the original close time
  - Maintains accurate timeline for deleted issues

- **Git detection caching** - Performance improvement
  - Cache git worktree/repo detection results
  - Eliminates repeated slowness on worktree checks

- **Windows MCP graceful fallback** (GH#387) - Platform compatibility
  - Daemon mode falls back gracefully on Windows
  - Prevents crashes when daemon unavailable

- **Windows npm postinstall file locking** (GH#670) - Install reliability
  - Handles file lock errors during npm install
  - Improves installation success on Windows

- **installed_plugins.json v2 format** (GH#741) - Claude Code compatibility
  - `bd doctor` now handles both v1 and v2 plugin file formats
  - Fixes "cannot unmarshal array" error on newer Claude Code versions

- **git.IsWorktree() hang on Windows** (GH#727) - Init safety
  - Added isGitRepo() guard before calling git.IsWorktree()
  - Fixes `bd init` hanging outside git repositories on Windows

- **Skill files deleted by bd sync** (GH#738) - Sync safety
  - `bd sync` no longer deletes skill files in .claude/
  - Prevents accidental removal of Claude Code configuration

- **doctor check false positives** (GH#709) - Cleaner diagnostics
  - Skips interactions.jsonl and molecules.jsonl in sync checks
  - These files are runtime state, not sync targets

- **FatalErrorRespectJSON** - Consistent error output
  - All commands respect `--json` flag for error output
  - Errors return proper JSON structure when flag is set

- **bd sync commits non-.beads files** - Sync isolation
  - Sync operations now only commit .beads/ directory changes
  - Prevents unintended commits of working directory files

- **Content-level merge for divergence** - Better conflict resolution
  - Divergence recovery uses content-level merging
  - Reduces false conflicts during multi-clone sync

- **Child→parent dep fix opt-in** - Migration control
  - `--fix-child-parent` flag required for automatic fix
  - Prevents unexpected dependency modifications

- **Aspect self-matching recursion** - Formula safety
  - Aspects cannot match themselves during expansion
  - Prevents infinite recursion in aspect composition

- **Map expansion nested matching** - Correct child handling
  - Map expansion now correctly matches nested child steps
  - Fixes missing expansions in hierarchical formulas

- **Mol/wisp ID prefix combination** - Correct ID generation
  - Database prefix combined with type prefix correctly
  - IDs like `bd-mol-xxx` instead of `bdmol-xxx`

- **mol run hierarchical children** (bd-c8d5, bd-drcx) - Complete loading
  - `bd mol run` loads all hierarchical children
  - Supports title lookup for molecule identification

- **Wisps excluded from sync** (bd-687g, bd-9avq) - Export correctness
  - Wisps filtered from JSONL export and nodb operations
  - Ephemeral molecules don't pollute shared state

### Documentation

- **MOLECULES.md rewrite** - Workflow-first structure
  - Reorganized around practical workflows
  - Clearer distinction between protos, molecules, wisps

- **Molecular chemistry docs** - Conceptual foundation
  - Phase metaphor: solid (proto) → liquid (mol) → vapor (wisp)
  - Pour, bond, distill, squash operations explained

## [0.36.0] - 2025-12-24

### Added

- **Formula system** (bd-weu8, bd-wa2l) - Declarative workflow templates
  - `bd cook <formula>` - Execute a formula template with variable interpolation
  - Formula files (`.formula.json`) support inheritance via `extends`
  - `needs` and `waits_for` fields for dependency declarations
  - `--prefix` flag for custom issue prefix when cooking
  - Search paths: `.beads/formulas/`, `~/.beads/formulas/`, `~/gt/.beads/formulas/`

- **Gate issue type** - Async coordination primitives
  - `bd gate create <name>` - Create a gate for coordinating parallel work
  - `bd gate open <id>` - Open a gate (unblock waiters)
  - `bd gate close <id>` - Close a gate (block new work)
  - Gates integrate with `waits-for` dependencies for fanout patterns

- **`bd list` viewer enhancements** (#729) - Built-in terminal UI
  - `--pretty` - Colorized, formatted output for human viewing
  - `--watch` - Live-updating view (refreshes on changes)

- **`bd search` filters** - Enhanced search capabilities
  - `--after`, `--before` - Date range filtering
  - `--priority` - Exact priority match
  - `--content` - Full-text content search

- **`bd compact --prune`** - Standalone tombstone pruning
  - Prune old tombstones without full compaction
  - Configurable retention period

- **`bd export --priority`** - Exact priority filter for exports

- **`--resolution` alias** (GH#721) - Alternative to `--reason` on `bd close`
  - `bd close bd-42 --resolution "Fixed in commit abc123"`

- **Database size check in doctor** (#724) - Issue count threshold warnings
  - Warns when issue count exceeds configurable threshold
  - Helps identify when cleanup or compaction is needed

- **Config override notifications** (#731) - Transparency for config sources
  - Shows when environment variables override config file values
  - Helps debug unexpected behavior from env overrides

- **Windows code signing infrastructure** - Signed Windows binaries
  - Code signing certificate integration for Windows releases
  - Improved trust and installation experience on Windows

- **RPC endpoints for monitoring** (bd-0oqz, bd-l13p)
  - `GetMoleculeProgress` - Query molecule execution state
  - `GetWorkerStatus` - Query worker health and current task

- **Cross-database molecule spawning**
  - `bd mol run` can spawn in external beads databases
  - Enables cross-project workflow orchestration

- **Config-based close hooks** - Custom scripts on issue close
  - Configure hooks in `.beads/config.yaml`
  - Run validation or notification scripts when issues close

### Changed

- **Removed `bd mol spawn`** - Use pour/wisp only
  - `bd pour <proto>` for persistent molecules
  - `bd wisp create <proto>` for ephemeral molecules
  - Simplifies mental model: pour = liquid (persistent), wisp = vapor (ephemeral)

- **`bd ready` excludes workflow types** - Cleaner ready queue
  - Gates and other workflow coordination types excluded by default
  - Use `--include-workflow` to see all types

- **Natural language activation** (#718) - Improved Claude integration
  - Enhanced activation patterns for natural language commands
  - Anthropic 2025 API compliance updates

### Fixed

- **Dots in prefix handling** (GH#664) - SQLite extractParentChain fix
  - Prefixes containing dots (e.g., `my.project`) now work correctly
  - Parent chain extraction uses proper escaping

- **allowed_prefixes config respected** - Import validation
  - Import now respects `allowed_prefixes` configuration
  - Prevents importing issues with unauthorized prefixes

- **Child counter updates** (GH#728) - Explicit child ID creation
  - `child_counters` table updated when explicit child IDs created
  - Prevents ID collision when mixing auto and explicit child IDs

- **Comment timestamps preserved** (#735) - Import fidelity
  - `created_at` timestamps on comments preserved during import
  - Maintains audit trail for imported issues

- **sync.remote config respected** (#736) - Daemon sync operations
  - Daemon respects configured remote for sync operations
  - Fixes sync to wrong remote when multiple remotes configured

- **Export requires -o flag** (#733) - Explicit output control
  - `bd export` now requires `-o` flag to write to file
  - Prevents accidental stdout pollution in scripts

- **YAML config key normalization** (#732) - Consistent config parsing
  - Config keys normalized to canonical format (snake_case)
  - `sync.remote` and `sync_remote` both work

- **JSON output standardization** - Consistent API
  - Empty arrays return `[]` not `null`
  - Error responses have consistent structure
  - All commands with `--json` follow same patterns

- **MCP Claude Code compatibility** - Tool schema fix
  - Added `output_schema=None` to MCP tools
  - Fixes "Invalid schema" errors in Claude Code

- **Windows file locking** - Better Windows support
  - Proper file handle cleanup prevents locking issues
  - Fixes "file in use" errors on Windows

- **Template commands with daemon** - Daemon mode compatibility
  - `bd pour`, `bd wisp create` work correctly with daemon running

- **Startup config to config.yaml** (GH#536) - Config persistence
  - Startup wizard writes to config.yaml, not SQLite
  - Configuration survives database recreation

- **Multi-hyphen prefixes** (GH#422) - Prefix parsing
  - Prefixes like `my-project-name` parsed correctly
  - Fixes issue ID extraction with complex prefixes

- **`--pour` flag in bond operations** - Phase control
  - `bd mol bond --pour` correctly forces liquid phase

- **Stealth mode gitignore** (GH#704) - Local-only exclusion
  - Uses `.git/info/exclude` instead of global gitignore
  - Stealth mode truly local to the repository

- **Pinned field import** - Field preservation
  - `pinned` field preserved during JSONL import

- **External deps in orphan check** - Migration fix
  - External dependencies excluded from orphan validation
  - Fixes spurious migration warnings

- **Child→parent dependency detection** - Prevents LLM temporal reasoning trap
  - Epic children can no longer depend on their parent epic
  - Blocks a common AI mistake: LLMs use temporal reasoning for "phases" and invert dependencies
  - Example: "Phase 1 before Phase 2" triggers "Phase 1 blocks Phase 2" → WRONG
  - Correct model: "Phase 2 needs Phase 1" → `bd dep add phase2 phase1`
  - Clear error message explains the anti-pattern when detected

### Improved

- **Test coverage** - Significant improvements across codebase
  - daemon: 27% → 72%
  - compact: 17% → 82%
  - setup: 28% → 54%
  - storage: interface conformance tests
  - RPC: comprehensive delete handler tests

- **Structured logging** - Better observability
  - Daemon uses `slog` for structured logging
  - Consistent log format across components

- **Code organization** - Modular refactoring
  - Split `sync.go` into focused modules
  - Split `queries.go` into focused modules
  - Typed JSON response structs (no more `map[string]interface{}`)

- **JSONL size reduction** - Smaller exports
  - `omitempty` on all JSONL fields
  - Removes null/empty values from exports

## [0.35.0] - 2025-12-23

### Added

- **Dynamic molecule bonding** - Attach templates to running molecules at runtime
  - `bd mol bond <proto> <target> --ref <issue-id>` - Inject template steps dynamically
  - Enables responsive workflows: attach error recovery, expand scope mid-execution
  - Templates can reference existing issue data via `--ref` for variable interpolation

- **`bd activity` command** - Real-time state feed for workflow monitoring
  - Stream mutation events as they happen (JSON format)
  - Monitor molecule progress without polling
  - Useful for dashboards, debugging, and automation

- **`waits-for` dependency type** - Fanout coordination gates
  - `bd dep add <gate> <step> --type waits-for` - Gate waits for all steps
  - Unlike `blocks`, waits-for is used for parallel coordination (fan-in pattern)
  - Gate unblocks only when ALL waits-for dependencies complete

- **Parallel step detection** - Auto-identify parallelizable work
  - Molecules automatically detect steps that can run concurrently
  - `bd mol show` displays parallel-capable step groups
  - Helps agents optimize execution order

- **Molecule navigation commands** (bd-sal9, bd-ieyy) - Track progress through workflows
  - `bd mol current` - Show current step and molecule progress
  - `bd close --continue` - Auto-advance to next step in molecule
  - `bd close --no-auto` - Close without auto-claiming next step

- **`bd list --parent` flag** - Filter issues by parent
  - `bd list --parent=bd-xyz --status=open` shows open children of bd-xyz
  - Useful for epic progress tracking and molecule step listing

- **Conditional bond type** - Run steps only when predecessors fail
  - `bd mol bond --type conditional-blocks` for error-handling workflows
  - Step B runs only if step A fails (useful for fallback paths)

- **External dependency display** - Show cross-project deps in tree view
  - `bd dep tree` now displays external dependencies with repo prefix
  - Satisfied external deps filtered from blocked issues

- **Performance optimization indexes** (bd-bha9, bd-a9y3) - Faster queries for large databases
  - Added indexes for common query patterns
  - Improved GetReadyWork and SearchIssues performance

### Changed

- **Consolidated doctor --fix** (GH#715, bd-bqcc) - Single command for all fixes
  - `bd doctor --fix` now handles: hooks, permissions, sync branch, schema
  - Removed individual `bd migrate`, `bd hooks install` suggestions
  - Daemon health check integrated into doctor flow

- **Auto_pull config** (GH#707) - Periodic remote sync in event-driven mode
  - `bd config set daemon.auto_pull true` enables pull every 5 minutes
  - Prevents staleness when git changes happen outside daemon's watch

### Fixed

- **Rich mutation events for status changes** - Events now include full context
  - Status transitions emit complete before/after state
  - Enables better monitoring and auditing

- **External deps filtered from GetBlockedIssues** - Correct blocking calculation
  - External dependencies no longer incorrectly block local work
  - Only local blocking deps affect ready queue

- **Parallel execution migration race** (GH#720) - Multiple daemons no longer corrupt DB
  - Added file-based migration lock to prevent concurrent schema changes
  - Retry logic when migration lock is held by another process

- **bd create -f with daemon** (GH#719) - File flag now works in daemon mode
  - `-f/--file` flag properly forwarded through RPC

- **FindJSONLInDir interactions.jsonl** (GH#709) - No longer picks wrong file
  - Skip `interactions.jsonl` when discovering JSONL files

- **Daemon duplicate log messages** (PR#713) - Reduced log spam
- **Worktree health check redundancy** (PR#711) - Integrated into CreateBeadsWorktree
- **Diverged sync branch handling** (GH#697) - Auto-recovery on push failure
- **Tombstones in JSONL export** (GH#696) - Deletions now propagate correctly

### Refactored

- Remove legacy autoflush code paths
- Consolidate duplicate path-finding utilities
- Replace map[string]interface{} with typed JSON structs
- Migrate sort.Slice to slices.SortFunc
- Add testEnv helpers, reduce SQLite test file sizes

## [0.34.0] - 2025-12-22

### Added

- **Wisp commands** - Full ephemeral molecule management
  - `bd wisp create <proto>` - Instantiate proto as ephemeral wisp (solid→vapor)
  - `bd wisp list` - List all wisps with stale detection
  - `bd wisp gc` - Garbage collect orphaned wisps
  - Wisps have Wisp=true flag and are not exported to JSONL (never sync)

- **Chemistry UX commands** - Phase-aware molecule operations
  - `bd pour <proto>` - Instantiate proto as persistent mol (solid→liquid)
  - `bd mol bond --wisp` - Force spawn as vapor when attaching to mol
  - `bd mol bond --pour` - Force spawn as liquid when attaching to wisp
  - Squash clears Wisp flag, promoting to persistent (exported to JSONL)

- **Cross-project dependencies** (bd-66w1, bd-om4a) - Reference issues across repos
  - `external:<repo>:<id>` dependency syntax
  - `bd ship <id> --to <repo>` - Ship issues to other beads repos
  - `bd ready` filters by external dependency satisfaction
  - Configure additional repos in `.beads/config.yaml`

- **Orphan detection in bd doctor** - Find issues with missing parents
  - Detects parent-child relationships pointing to deleted issues
  - Suggests fix commands for orphaned issues

### Changed

- **Multi-repo config uses YAML** (GH#683) - `bd repo add/remove` now writes to `.beads/config.yaml`
  - Fixes disconnect where CLI wrote to DB but hydration read from YAML
  - `bd repo remove` now cleans up hydrated issues from removed repo
  - Breaking: `bd repo add` no longer accepts optional alias argument

### Fixed

- **Wisp flag handling** - Wisps now use Wisp=true flag in main database (not exported to JSONL)
- **Prefix validation in multi-repo mode** (GH#686) - Skip validation for external repos
- **Empty config values** (GH#680, GH#684) - Handle gracefully in getRepoConfig()
- **Doctor UX improvements** (GH#687) - Better diagnostics and daemon integration
- **Orphaned test file** - Removed repo_test.go with undefined functions

## [0.33.2] - 2025-12-21

## [0.33.1] - 2025-12-21

### Changed

- **Ephemeral → Wisp rename** - Aligns with Steam Engine metaphor (wisps = ephemeral vapor)
  - JSON field changed from `ephemeral` to `wisp` (breaking change for API consumers)
  - CLI flag changed from `--ephemeral` to `--wisp` on `bd cleanup`
  - SQLite column remains `ephemeral` (no migration needed)

### Fixed

- **Lint error in mail.go** - Added nosec directive for mail delegate exec.Command

## [0.33.0] - 2025-12-21

### Added

- **Wisp molecules** - Support for ephemeral wisps
  - `bd wisp create` creates wisp issues that live only in SQLite
  - Wisp issues never export to JSONL (prevents zombie resurrection)
  - Use `bd pour` for persistent mols, `bd wisp create` for ephemeral wisps
  - `bd mol squash` compresses wisp children into a digest issue
  - `--summary` flag allows agents to provide AI-generated summaries

### Fixed

- **Comments not deleted with issue** - `DeleteIssue` now cascades to comments table

## [0.32.1] - 2025-12-21

### Added

- **MCP output control parameters** (PR#667) - Reduce context window usage by up to 97%
  - `brief` - Return minimal responses: `BriefIssue` for reads, `OperationResult` for writes
  - `brief_deps` - Full issue with compact dependencies
  - `fields` - Custom field projection with validation
  - `max_description_length` - Truncate long descriptions
  - New models: `BriefIssue`, `BriefDep`, `OperationResult`
  - Default `brief=True` for writes (minimal confirmations)

- **MCP filtering parameters** - Align MCP tools with CLI capabilities
  - `labels` / `labels_any` - AND/OR label filtering
  - `query` - Title search (case-insensitive)
  - `unassigned` - Filter to unassigned issues
  - `sort_policy` - Sort ready work by hybrid/priority/oldest

### Fixed

- **Pin field not in allowed update fields**
  - `bd update --pinned` was failing with "invalid field" error
  - Added `pinned` to allowedUpdateFields and importer

## [0.32.0] - 2025-12-20

### Changed

- **Removed `bd mail` commands** - Mail is orchestration, not data plane
  - Removed: `bd mail send`, `bd mail inbox`, `bd mail read`, `bd mail ack`, `bd mail reply`
  - The underlying data model is unchanged: `type=message`, `Sender`, `Ephemeral`, `replies_to` fields remain
  - Orchestration tools should implement mail interfaces on top of the beads data model
  - This follows the principle that beads is a data store, not a workflow engine

### Fixed

- **Symlink preservation in atomicWriteFile** (PR#665) - Thanks @qmx
  - `bd setup claude` no longer clobbers nix/home-manager managed `~/.claude/settings.json`
  - New `ResolveForWrite()` helper writes to symlink target instead of replacing symlink

- **Broken link in examples** (GH#666) - Fixed LABELS.md link in multiple-personas example

## [0.31.0] - 2025-12-20

### Added

- **`bd defer` / `bd undefer` commands** - New "deferred" status for icebox issues
  - Issues that are deliberately postponed, not blocked by dependencies
  - Deferred issues excluded from `bd ready` and shown with ❄️ snowflake styling
  - Full support in MCP server, graph views, and statistics

- **Agent audit trail** (GH#649) - Append-only logging for AI agent interactions
  - New `.beads/interactions.jsonl` audit file
  - `bd audit record` - Log LLM calls, tool calls, or pipe JSON via stdin
  - `bd audit label <id>` - Append quality labels (good/bad) for dataset curation
  - `bd compact --audit` - Optionally log compaction prompts/responses
  - Audit entries are immutable; labels create new referencing entries

- **Directory-aware label scoping** (GH#541) - Auto-filter issues by directory in monorepos
  - Configure `directory.labels` to map paths to label filters
  - `bd ready` and `bd list` auto-apply when in matching directories
  - Example: `packages/maverick: maverick` shows only maverick-labeled issues

- **Molecules catalog** - Separate storage for template molecules
  - Templates now live in `molecules.jsonl`, distinct from work items
  - Hierarchical loading: built-in → town → user → project
  - Molecules use `mol-*` ID namespace with `is_template: true`

- **Windows winget manifest** (GH#524) - Prepare beads for Windows Package Manager
  - Added manifest files for winget submission
  - Once merged to microsoft/winget-pkgs: `winget install SteveYegge.beads`

- **Git commit configuration** (GH#600) - Control beads auto-commit behavior
  - `git.author` - Override commit author (useful for bots)
  - `git.no-gpg-sign` - Disable GPG signing (fixes Touch ID prompts)

- **Require description config** (GH#596) - Enforce descriptions on issue creation
  - Set `create.require-description: true` to error on missing descriptions
  - Also supports `BD_CREATE_REQUIRE_DESCRIPTION` env var

### Changed

- **`bd stats` merged into `bd status`** (GH#644) - Consolidated status commands
  - `stats` now an alias for `status`
  - Colorized output with emoji header
  - Shows all statistics (tombstones, pinned, epics, lead time)
  - Added `--no-activity` flag to skip git activity parsing

- **Thin hook shims** (GH#615) - Hooks now delegate to `bd hooks run`
  - Eliminates hook version drift after upgrades
  - No more manual `bd hooks install --force` needed
  - Shims use `# bd-shim v1` marker (format version, not bd version)

- **MCP context tool consolidation** - Merged 3 tools into 1
  - Combined `set_context`, `where_am_i`, `init` into single `context` tool
  - Actions: `set` (default with workspace_root), `show` (default), `init`

- **Doctor improvements** (GH#656) - Enhanced diagnostics and testing
  - Visual improvements with grouped output
  - Comprehensive test coverage added
  - Fixed gosec warnings
  - Contributed by @rsnodgrass

### Fixed

- **`relates-to` cycle detection** (GH#661) - Exclude relates-to from cycle detection
  - Relates-to links are bidirectional by design, not cycles

- **Doctor `.local_version` check** (GH#662) - Check correct version file
  - Now checks `.local_version` instead of deprecated `LastBdVersion`

- **Doctor Claude plugin link** (GH#623) - Updated broken documentation link

- **Read-only gitignore in stealth mode** (GH#663) - Print manual instructions instead of failing
  - When global gitignore is read-only (e.g., symlink to immutable location), shows what to add manually
  - Contributed by @qmx

## [0.30.7] - 2025-12-19

### Fixed

- **`bd graph` nil pointer crash** (#657) - Fixed crash when running `bd graph` on epics
  - `renderGraph()` was passed `nil` instead of the subgraph, causing panic in `computeDependencyCounts()`

- **Windows npm installer file lock** (#652) - Fixed installation failure on Windows
  - The download file handle wasn't fully closed before extraction attempted
  - Now properly waits for `file.close()` callback before proceeding

## [0.30.6] - 2025-12-18

### Added

- **`bd graph` dependency counts** - Graph command shows dependency counts using subgraph formatting
- **`types.StatusPinned`** - New status for persistent beads that survive cleanup

### Fixed

- **CRITICAL: Dependency resurrection bug** - Fixed 3-way merge to respect dependency removals
  - `mergeDependencies` was using union (additive-only) instead of proper 3-way merge
  - Now removals are authoritative: if either left or right removes a dep, it stays removed
  - This prevented orphaned parent-child relationships from being permanently removed

## [0.30.5] - 2025-12-18

### Removed

- **YAML simple template system** - Removed `--from-template` flag from `bd create`
  - Deleted embedded templates: `bug.yaml`, `epic.yaml`, `feature.yaml`
  - Templates are now purely Beads-based (epics with `template` label)
  - Use `bd template instantiate <id>` for template workflows

## [0.30.4] - 2025-12-18

### Added

- **`bd template instantiate`** - Create beads issues from YAML workflow templates
  - `bd template instantiate <file.yaml>` - Create issues from workflow definitions
  - `--assignee <identity>` flag for auto-assignment during instantiation
  - Supports multi-issue workflows with dependency chains
  - Templates define issue properties (title, type, priority, dependencies)

### Changed

- **`bd mail inbox --identity`** - Fixed to properly filter by identity parameter

### Fixed

- **Orphan detection warnings** - No longer warns about closed issues or tombstones
  - Previously `bd doctor` reported false positives for completed dependencies

### Removed

- **Legacy MCP Agent Mail integration** - Removed obsolete `mcp_agents` package
- **YAML workflow execution system** - Replaced by simpler template instantiation

### Notes

- **Experimental edges**: The new graph link fields (`relates_to`, `replies_to`, `duplicate_of`, `superseded_by`) and mail commands are **experimental and subject to change** in upcoming releases. Early adopters should expect breaking changes to these APIs.

## [0.30.3] - 2025-12-17

### Fixed

- **Data loss race condition** - Removed unsafe `ClearDirtyIssues()` method
  - Old method cleared ALL dirty issues, risking data loss if export failed partway
  - All code now uses `ClearDirtyIssuesByID()` which only clears exported issues
  - Affects: `internal/storage/sqlite/dirty.go`, `internal/storage/memory/memory.go`

### Closed (Already Implemented)

- **Stale database warning** - Commands now warn when database is out of sync with JSONL
- **Staleness check error handling** (bd-n4td, bd-o4qy) - Proper warnings and error returns

## [0.30.2] - 2025-12-16

### Added

- **`bd setup droid`** (GH#598) - Factory.ai (Droid) IDE support
  - Configure beads for use with Factory.ai's Droid
  - Contributed by @jordanhubbard

- **Messaging schema fields** - Foundation for inter-agent messaging
  - New `message` issue type for agent-to-agent communication
  - New fields: `sender`, `wisp`, `replies_to`, `relates_to`, `duplicate_of`, `superseded_by`
  - New dependency types: `replies-to`, `relates-to`, `duplicates`, `supersedes`
  - Schema migration 019 (automatic on first use)

- **`bd mail` commands** - Inter-agent messaging
  - `bd mail send <recipient> -s <subject> -m <body>` - Send messages
  - `bd mail inbox` - List open messages for your identity
  - `bd mail read <id>` - Display message content
  - `bd mail ack <id>` - Acknowledge (close) messages
  - `bd mail reply <id> -m <body>` - Reply to messages (creates threads)
  - Identity via `BEADS_IDENTITY` env var or `.beads/config.json`

- **Graph link commands** - Knowledge graph relationships
  - `bd relate <id1> <id2>` - Create bidirectional "see also" links
  - `bd unrelate <id1> <id2>` - Remove relates_to links
  - `bd duplicate <id> --of <canonical>` - Mark issue as duplicate (closes it)
  - `bd supersede <old> --with <new>` - Mark issue as superseded (closes it)
  - `bd show --thread` - View message threads via replies_to chain

- **Hooks system** - Extensible event notifications
  - `.beads/hooks/on_create` - Runs after issue creation
  - `.beads/hooks/on_update` - Runs after issue update
  - `.beads/hooks/on_close` - Runs after issue close
  - `.beads/hooks/on_message` - Runs after message send
  - Hooks receive issue ID, event type as args, full JSON on stdin

- **`bd cleanup --wisp` flag** - Clean up transient messages
  - Deletes only closed issues with `wisp=true`
  - Useful for cleaning up messages after swarms complete

### Fixed

- **Windows build errors** (GH#585) - Fixed gosec lint warnings
  - Contributed by @deblasis

- **Issue ID prefix extraction** - Word-like suffixes (e.g., `my-project-audit`) now parse correctly
  - Previously could incorrectly split on word boundaries

### Removed

- **Legacy deletions.jsonl code** - Fully migrated to inline tombstones
  - Removed `deletions.jsonl` from git tracking
  - All deletion tracking now via inline tombstones in `issues.jsonl`

### Documentation

- **Messaging documentation** - New docs for messaging system
  - `docs/messaging.md` - Full messaging reference with examples
  - `docs/graph-links.md` - Graph link types and use cases
  - Updated `AGENTS.md` with inter-agent messaging section

- Windows installation command in upgrade instructions (GH#589)
  - Contributed by @alexx-ftw

- Aligned `bd prime` guidance with skill's hybrid TodoWrite approach

## [0.30.1] - 2025-12-16

### Added

- **`bd reset` command** (GH#505) - Complete beads removal from a repository
  - Removes `.beads/` directory and all associated data
  - Cleans up git hooks installed by beads
  - Use `--force` to skip confirmation prompt

- **`bd cleanup --hard` flag** - Bypass tombstone TTL safety
  - Immediately removes tombstones regardless of age
  - Use when you need to force-clean deleted issues

- **`bd update --type` flag** (GH#522) - Change issue type after creation
  - Convert between task, bug, feature, epic types
  - Example: `bd update bd-xyz --type epic`

- **`bd q` silent quick-capture mode** (GH#540)
  - Capture issues silently without output
  - Ideal for scripting and automation

- **`bd sync --check` flag** - Pre-sync integrity checks
  - Validates database state before syncing
  - Catches potential issues early

- **`bd show` displays dependent issue status** (#583)
  - Shows status for all blocked-by and blocking issues
  - Better visibility into dependency chains

- **`bd daemon --status` shows config** (#569)
  - Displays daemon configuration in status output
  - Contributed by @crcatala

- **`bd daemon --stop-all`** - Kill all daemon processes
  - Useful for cleanup when multiple daemons are running

- **Auto-disable daemon in git worktrees** (#567)
  - Daemon automatically disabled in worktrees for safety
  - Prevents database conflicts between worktrees

- **`claude.local.md` support** - Local-only documentation
  - Add project notes that won't be committed
  - Gitignored by default

- **Auto-add "landing the plane" instructions to AGENTS.md**
  - New projects get session-close protocol guidance

- **Inline tombstones for soft-delete**
  - Deleted issues become tombstones with `status: "tombstone"` in `issues.jsonl`
  - Full audit trail: `deleted_at`, `deleted_by`, `delete_reason`, `original_type`
  - TTL-based expiration (default 30 days) with automatic pruning via `bd compact`
  - Proper 3-way merge support: fresh tombstones win, expired tombstones allow resurrection

- **`bd migrate-tombstones` command**
  - Converts legacy `deletions.jsonl` entries to inline tombstones
  - Archives old file as `deletions.jsonl.migrated`

- **Enhanced Git Worktree Support**
  - Shared `.beads` database across all worktrees
  - Worktree-aware database discovery
  - Git hooks automatically adapt to worktree context
  - Documentation in `docs/WORKTREES.md`

### Fixed

- **Multi-hyphen prefix parsing** (GH#405) - Prefixes like `my-cool-project` now work correctly
- **`bd sync` on sync branch** (GH#519) - Fixed sync when already on the sync branch
- **Pre-commit hook with removed `.beads`** (GH#483) - No longer blocks commits after beads removal
- **Priority format error messages** (GH#517) - Clearer guidance on P0-P4 format
- **Status naming consistency** (GH#444) - Uses `in_progress` everywhere
- **Orphan detection with dots in directory names** (GH#508) - No more false positives
- **Circular error in pre-push hook** (GH#532) - Fixed infinite loop scenario
- **Doctor --fix auto-migrates tombstones** - Automatic migration during repair
- **Daemon detects external DB replacement** (#564) - Contributed by @deblasis
- **Import handles hierarchical hash IDs** (#584) - Contributed by @rsnodgrass
- **Nested worktree detection** (GH#509) - Correctly finds `.beads/` in parent repo
- **Import skips cross-prefix content matches** - Prevents incorrect renames
- **3-way merge tolerates missing issues** - More robust conflict resolution
- **Pre-commit warns instead of failing on flush error** - Graceful degradation

### Security

- **Go toolchain updated to 1.24.11** - Addresses CVEs in standard library

### CI

- **PR check for `.beads/issues.jsonl` changes** - Rejects accidental database commits

### Dependencies

- Bump `github.com/ncruces/go-sqlite3` to 0.30.3
- Bump `github.com/spf13/cobra` to 1.10.2
- Bump `golang.org/x/mod` to 0.31.0
- Bump `golang.org/x/term` to 0.38.0
- Bump `pydantic` to 2.12.5 (beads-mcp)
- Bump `fastmcp` to 2.14.1 (beads-mcp)

## [0.29.0] - 2025-12-03

### Added

- **`--estimate` / `-e` flag for `bd create` and `bd update` (GH #443)**
  - Add time estimates to issues in minutes
  - Example: `bd create "My task" --estimate 120` (2 hours)
  - Example: `bd update bd-xyz --estimate 60` (1 hour)
  - Enables planning and prioritization features in vscode-beads

- **`bd doctor` improvements**
  - SQLite integrity check - Detects database corruption
  - Configuration value validation - Validates config settings
  - Stale sync branch detection - Warns about abandoned beads-sync branches
  - `--output` flag - Export diagnostics to file for sharing
  - `--dry-run` flag - Preview fixes without applying
  - Per-fix confirmation mode - Approve each fix individually

- **`--readonly` flag** - Read-only mode for worker sandboxes
  - Blocks all write operations
  - Useful for parallel worker processes that should only read

### Fixed

- **`bd sync` safety improvements**
  - Auto-push after merge with safety check
  - Handle diverged histories with content-based merge
  - Multiple safety check enhancements

- **Auto-resolve merge conflicts deterministically**
  - All field conflicts resolved without prompts
  - Uses consistent rules for field-level merging

- **3-char all-letter base36 hash support (GH #446)**
  - Fixes prefix extraction for edge case hashes like "bd-abc"

- **`bd ready` message fix**
  - Shows correct message when all issues are closed

- **Version notification spam fix**
  - Store version in gitignored .local_version file

- **Nix flake vendorHash update**
  - Fixed build after dependency bumps

### Documentation

- Added perles and vscode-beads to Third-Party Tools
- Encourage batch close and parallel creation in `bd prime` output

## [0.28.0] - 2025-12-01

### Added

- **`bd daemon --local` flag (#433)** - Run daemon without git operations
  - Ideal for multi-repo setups where git sync happens externally
  - Prevents daemon from triggering git commits/pushes
  - Use with worktrees or when beads sync is handled by another process

- **`bd daemon --foreground` flag** - Run daemon in foreground mode
  - For systemd/supervisord/launchd integration
  - Logs to stdout instead of background file
  - Process stays attached to terminal

- **`bd migrate-sync` command** - Migrate to sync.branch workflow
  - Moves beads data to a dedicated sync branch
  - Keeps main branch clean of .beads/ commits
  - Automated setup of sync.branch configuration

### Fixed

- **Database Migration: close_reason column**
  - Added missing database column for close_reason field
  - Fixes sync loops where close_reason was lost on import/export
  - Automatic migration on first run

- **Multi-repo Prefix Filtering (GH #437)**
  - Issues now filtered by prefix when flushing from non-primary repos
  - Prevents issues from other projects appearing in exports

- **Parent-Child Dependency UX (GH #440)**
  - Fixed backwards documentation in DEPENDENCIES.md
  - `bd show` now displays epic children under "Children" not "Blocks"
  - Clearer UI labels for dependency relationships

- **sync.branch Workflow Fixes**
  - Fixed .beads/ restoration from branch after sync
  - Prevents final flush after sync.branch restore
  - `bd doctor` now detects when on sync branch

- **Jira API Migration**
  - Updated from deprecated Jira API v2 to v3
  - Fixes authentication issues with newer Jira instances

- **Redundant Database Queries**
  - Removed extra GetCloseReason() calls after column migration
  - Improves query performance for issue retrieval

### Documentation

- Added go install fallback instructions for Claude Code web (GH #439)
- Added uninstall documentation (GH #445)

### Internal

- Refactored daemon sync functions to reduce ~200 lines of duplication
- Consolidated local-only sync functions into shared implementation

## [0.27.2] - 2025-11-30

### Fixed

- **CRITICAL: Prevent Mass Database Deletion on JSONL Reset**
  - git-history-backfill now includes safety guard to prevent purging entire database
  - If >50% of issues would be deleted via git history, operation is aborted with warning
  - Threshold prevents accidental deletion when JSONL is reset (git reset, branch switch, etc.)
  - If 10-50% would be deleted, operation proceeds but shows warning message
  - Fixes bug where `git reset --hard origin/main` would lose all issues

- **Fix Fresh Clone Initialization**
  - `bd init` now works on fresh clones that have JSONL but no database
  - Auto-detects issue prefix from existing JSONL (no `--prefix` flag needed)
  - Prevents "database not found" errors on first run in a cloned repository
  
- **Import Warning for Deleted Issues**
  - New warning message when issues are skipped due to deletions manifest
  - Helps users understand why expected issues aren't being imported
  - Warns user to check `bd deleted` for history of removed issues

- **Extract Issue Prefix for 3-char Hashes (#425)**
  - `ExtractIssuePrefix()` now handles base36 hashes as short as 3 characters
  - Previously required 4+ chars, breaking hyphenated prefixes (e.g., `document-intelligence-0sa`)
  - Updated hash validation to accept base36 (0-9, a-z) instead of just hex
  - Requires at least one digit to distinguish hashes from English words

## [0.27.0] - 2025-11-29

### Added

- **Custom Status States**: Define custom issue statuses via config
  - Configure project-specific statuses like `testing`, `blocked`, `review`
  - Status validation ensures only configured statuses are used
  - Backwards compatible: open/in_progress/closed always work

- **Contributor Fork Workflows**: `bd init --contributor` now auto-configures `sync.remote=upstream`
  - Syncs issues from upstream rather than origin
  - Ideal for contributors working on forks

- **Git Worktree Support**: Full support for git worktrees (#416)
  - `bd hooks install` now works correctly in worktrees
  - Helpful message when entering worktree repo without beads initialized
  - Hooks properly detect worktree vs main repository

- **Daemon Health Checks**: Health monitoring in daemon event loop
  - Periodic health checks detect stale database state
  - Auto-recovery from detected inconsistencies

- **Fresh Clone Detection**: `bd doctor` now detects fresh clones
  - Suggests `bd init` when JSONL exists but no database
  - Improved onboarding experience for new contributors

- **bd sync --squash**: Batch multiple sync commits into one
  - Reduces commit noise when syncing frequently
  - Optional flag for cleaner git history

- **Error Handling Helpers**: Extracted FatalError/WarnError utilities
  - Consistent error formatting across codebase
  - Better error messages for users

### Fixed

- **CRITICAL: Sync Corruption Prevention**: Multiple fixes prevent stale database from corrupting JSONL
  - **Hash-based staleness detection**: SHA256 hash comparison catches content mismatches
  - **Reverse ZFC check**: Detects when JSONL has more issues than DB
  - **Stale daemon connection** (eb4b81d): Prevents corruption from stale SQLite connections
  - Combined, these fixes eliminate the sync corruption bugs that affected v0.26.x

- **Multi-Hyphen Prefix Support** (#419): Hash IDs with multi-part prefixes now handled correctly
  - Example: `my-app-abc123` correctly parsed as prefix `my-app`

- **Out-of-Order Dependencies** (#414): JSONL import handles dependencies before their targets exist
  - Fixes import failures when issues reference not-yet-imported dependencies

- **--from-main Sync Mode** (#418): Now defaults to `noGitHistory=true`
  - Prevents spurious deletions when syncing from main branch

- **JSONL-Only Mode Detection**: Auto-detects when config has `no-db: true`
  - Properly handles repositories using JSONL without SQLite

- **Init Safety Guard**: Prevents overwriting existing JSONL data on init
  - Warns user when data already exists, requires confirmation

- **Snapshot Cleanup**: Properly cleans up snapshot files after sync
  - Removes `.beads/*.snapshot` files that could cause conflicts

- **Daemon Registry Locking**: Cross-process locking prevents registry corruption
  - Fixes race conditions when multiple processes access daemon registry

- **Doctor Merge Artifacts**: Excludes merge artifacts from "multiple JSONL" warning
  - Reduces false positives during merge resolution

### Changed

- **Documentation**: Fixed birthday paradox threshold explanation in README
- **Documentation**: Corrected `bd dep add` syntax and semantics

### Community

- PR #419: Multi-hyphen prefix support
- PR #418: --from-main noGitHistory default
- PR #416: Git worktree hooks support
- PR #415: CI fixes
- PR #414: Out-of-order dependency handling
- PR #404: Error on invalid JSON during init (@joelklabo)

## [0.26.2] - 2025-11-29

### Fixed

- **Hash-Based Staleness Detection**: Prevents stale DB from corrupting JSONL when counts match
  - Previous count-based check (0.26.1) missed cases where DB and JSONL had similar issue counts
  - New detection computes SHA256 hash of JSONL content and stores it after import
  - On export, compares current JSONL hash against stored hash to detect modifications
  - If JSONL was modified externally (e.g., by git pull), triggers re-import before export
  - Ensures database is always synchronized with JSONL before exporting changes

## [0.26.1] - 2025-11-29

### Fixed

- **CRITICAL: Reverse ZFC Check**: Prevents stale database from corrupting JSONL
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

- **bd sync Commit Scope**
  - `bd sync` now only commits `.beads/` files, not other staged files
  - Prevents accidental commits of unrelated staged changes

- **Auto-Import to Wrong File**
  - Fixed auto-import exporting to wrong JSONL file
  - FindJSONLPath now correctly skips deletions.jsonl

- **Deletions.jsonl Handling**
  - Git hooks now properly stage deletions.jsonl for cross-clone propagation
  - bd doctor no longer warns about deletions.jsonl
  - Prevent rebase failures from deletions.jsonl writes

- **Defense-in-Depth**
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

- **Deletion Propagation**: Deletions now sync across clones via deletions manifest
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

- **Ephemeral Branch Sync**: New `bd sync --from-main` flag
  - Syncs from main branch without pushing to remote
  - Ideal for feature branches that sync issues from main
  - Prevents pushing local branch changes to origin

## [0.24.4] - 2025-11-25

### Added

- **Transaction API**: Full transactional support for atomic multi-operation workflows
  - New `storage.Transaction` interface with CreateIssue, UpdateIssue, CloseIssue, DeleteIssue
  - Dependency operations: AddDependency, RemoveDependency within transactions
  - Label operations: AddLabel, RemoveLabel with transactional semantics
  - Config/Metadata operations for atomic config+issue workflows
  - Uses `BEGIN IMMEDIATE` mode to prevent deadlocks
  - Automatic rollback on error or panic, commit on success
  - 1,147 lines of new implementation with comprehensive tests

- **Tip System Infrastructure**: Smart contextual hints for users
  - Tips shown after successful commands (list, ready, create, show)
  - Condition-based filtering, priority ordering, probability rolls
  - Frequency limits prevent tip spam
  - Respects `--json` and `--quiet` flags
  - Deterministic testing via `BEADS_TIP_SEED` env var

- **Sorting for bd list and bd search**: New `--sort` and `--reverse` flags
  - Sort by: priority, created, updated, closed, status, id, title, type, assignee
  - Smart defaults: priority ascending (P0 first), dates descending (newest first)
  - Works with all existing filters

- **Claude Integration Verification**: New bd doctor checks
  - `CheckBdInPath`: verifies 'bd' is in PATH (needed for hooks)
  - `CheckDocumentationBdPrimeReference`: detects version mismatches in docs

- **ARM Linux Support**: GoReleaser now builds for linux/arm64 (PR #371 by @tjg184)
  - Enables bd on ARM-based Linux systems like Raspberry Pi, AWS Graviton

- **Orphan Detection Migration**: Identifies orphaned child issues
  - Detects issues with hierarchical IDs where parent no longer exists
  - Logs suggestions: delete, convert to standalone, or restore parent
  - Idempotent and safe to run multiple times

### Fixed

- **Transaction Cache Invalidation**: blocked_issues_cache now invalidates correctly
  - UpdateIssue status changes trigger cache invalidation
  - CloseIssue always invalidates (closed issues don't block)
  - AddDependency/RemoveDependency invalidate for blocking types

- **SQLITE_BUSY Retry Logic**: Exponential backoff for concurrent writes
  - `beginImmediateWithRetry()` with 5 retries (10ms, 20ms, 40ms, 80ms, 160ms)
  - Eliminates spurious failures under normal concurrent usage
  - Context cancellation respected between retry attempts

- **bd import Argument Validation**: Helpful error for common mistake
  - Running `bd import file.jsonl` (without `-i`) now shows clear error
  - Previously silently read from stdin, confusing users with "0 created"

- **ZFC Import Export Skip**: Preserve JSONL source of truth
  - After stale DB import from JSONL, skip export to avoid overwriting
  - Fixes scenario where DB with fewer issues would overwrite JSONL

- **Daemon Reopen with Reason**: `--reason` flag now works in daemon mode
  - Previously ignored in daemon RPC calls

- **Windows Test Failures**: Skip file permission tests on Windows
  - Windows doesn't support Unix-style permissions (0600, 0755)
  - Core functionality still tested, only permission checks skipped

### Changed

- **bd daemon UX**: New `--start` flag, help text when no args
  - `bd daemon` now shows help instead of immediately starting
  - Use `bd daemon --start` to explicitly start
  - Auto-start still works (uses `--start` internally)
  - More discoverable for new users

### Documentation

- **blocked_issues_cache Architecture**: Document cache behavior and invalidation
- **Antivirus False Positives**: Guide for handling security software alerts
- **import.orphan_handling**: Complete documentation
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

- **Shared Database Pattern**: Refactored multiple test files to use shared DB pattern
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

- **bd info --whats-new**: Agent version awareness for quick upgrade summaries
  - Shows last 3 versions with workflow-impacting changes
  - Supports `--json` flag for machine-readable output
  - Helps agents understand what changed without re-reading full docs

- **bd hooks install**: Embedded git hooks command
  - Replaces external install script with native command
  - Git hooks now embedded in bd binary
  - Works for all bd users, not just source repo users

- **bd cleanup**: Bulk deletion command for closed issues
  - Agent-driven compaction for large databases
  - Removes closed issues older than specified threshold

### Fixed

- **3-way JSONL Merge**: Auto-invoked on conflicts
  - Automatically triggers intelligent merge on JSONL conflicts
  - No manual intervention required
  - Warning message added to zombie issues.jsonl file

- **Auto-import on Missing Database** (ab4ec90): `bd import` now auto-initializes database when missing
- **Daemon Crash Recovery**: Panic handler with socket cleanup prevents orphaned processes
- **Stale Database Exports**: ID-based staleness detection prevents exporting stale data
- **Windows MCP Subprocess Timeout**: Fix for git detection on Windows
- **Daemon Orphaning** (a6c9579): Track parent PID and exit when parent dies
- **Test Pollution Prevention** (bd-z528, bd-2c5a): Safeguards to prevent test issues in production database
- **Client Self-Heal** (a236558): Auto-recovery for stale daemon.pid files
- **Post-Merge Hook Error Messages** (abb1d1c): Show actual error messages instead of silent failures
- **Auto-import During Delete**: Disable auto-import during delete operations to prevent conflicts
- **MCP Workspace Context**: Auto-detect workspace from CWD
- **Import Sync Warning**: Warn when import syncs with working tree but not git HEAD
- **GH#254**: `bd init` now detects and chains with existing git hooks
- **GH#249**: Add nil storage checks to prevent RPC daemon crashes
- **GH#252**: Fix SQLite driver name mismatch causing "unknown driver" errors
- **Nested .beads Directories**: Prevent creating nested .beads directories
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
  - Reduced RPC dial timeout from 2s to 200ms for fast-fail
  - Standardized daemon detection with tryDaemonLock probe
  - Improved internal/daemon test coverage to 60%

- **Code Organization**:
  - Refactored snapshot management into dedicated module
  - Documented external_ref in content hash behavior
  - Added MCP server functions for repair commands
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

- **Vendored beads-merge by @neongreen**: Native `bd merge` command for intelligent JSONL merging
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

- **Parent Resurrection**: Automatic resurrection of deleted parent issues from JSONL history
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

- **Memory Database Connection Pool**: Fixed `:memory:` database handling to use single shared connection
  - Prevents "no such table" errors when using in-memory databases
  - Ensures connection pool reuses the same in-memory instance
  - Critical fix for event-driven daemon mode tests

- **Test Suite Stability**: Fixed event-driven test flakiness
  - Added `waitFor` helper for event-driven testing
  - Improved timing-dependent test reliability

## [0.21.6] - 2025-11-04

### Added

- **npm Package**: Created `@beads/bd` npm package for Node.js/Claude Code for Web integration
  - Native binary downloads from GitHub releases
  - Integration tests and release documentation
  - Postinstall script for platform-specific binary installation

- **Template Support**: Issue creation from markdown templates
  - Create multiple issues from a single file
  - Structured format for bulk issue creation

- **`bd comment` Alias**: Convenient shorthand for `bd comments add`

### Changed

- **Base36 Issue IDs** (GH #213): Switched from hex to Base36 encoding for shorter, more readable IDs
  - Reduces ID length while maintaining uniqueness
  - More human-friendly format

### Fixed

- **SQLite URI Handling**: Fixed `file://` URI scheme to prevent query params in filename
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
  - `bd status` - Database overview command showing issue counts and stats
  - `bd comment` - Convenient alias for `bd comments add`
  - `bd daemons restart` - Restart specific daemon without manual kill/start
  - `--json` flag for `bd stale` command

- **Protected Branch Workflow**:
  - `BEADS_DIR` environment variable for custom database location
  - `sync.branch` configuration for protected branch workflows
  - Git worktree management with sparse checkout for sync branches
    - Only checks out `.beads/` in worktrees, minimal disk usage
    - Only used when `sync.branch` is configured, not for default users
  - Comprehensive protected branch documentation

- **Migration & Validation**:
  - Migration inspection tools for AI agents
  - Conflict marker detection in `bd import` and `bd validate`
  - Git hooks health check in `bd doctor`
  - External reference (`external_ref`) UNIQUE constraint and validation
  - `external_ref` now primary matching key for import updates

### Fixed

- **Critical Fixes**:
  - Daemon corruption from git conflicts
  - MCP `set_context` hangs with stdio transport (GH #153)
  - Double-release race condition in `importInProgress` flag
  - Critical daemon race condition causing stale exports

- **Configuration & Migration**:
  - `bd migrate` now detects and sets missing `issue_prefix` config
  - Config system refactored (renamed `config.json` → `metadata.json`)
  - Config version update in migrate command

- **Daemon & RPC**:
  - `bd doctor --json` flag not working
  - `bd import` now flushes JSONL immediately for daemon visibility
  - Panic recovery in RPC `handleConnection`
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
- **Substring ID Matching**: All bd commands now support partial ID matching
  - `bd show abc` matches any ID containing "abc" (e.g., `bd-abc123`)
  - Ambiguous matches show helpful error with all candidates
- **Daemon Registry**: Multi-daemon management for multiple workspaces
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
- **Configurable Sort Policy**: `bd ready --sort` flag for work queue ordering
  - `hybrid` (default): Priority-weighted by staleness
  - `priority`: Strict priority ordering for autonomous systems
  - `oldest`: Pure FIFO for long-tail work
- **Release Automation**: New scripts for streamlined releases
  - `scripts/release.sh`: Full automated release (version bump, tests, tag, Homebrew, install)
  - `scripts/update-homebrew.sh`: Automated Homebrew formula updates

### Fixed
- **Critical**: Database reinitialization test re-landed with CI fixes
  - Windows: Fixed git path handling (forward slash normalization)
  - Nix: Skip test when git unavailable
  - JSON: Increased scanner buffer to 64MB for large issues
- **Bug**: Stale daemon socket detection
  - MCP server now health-checks cached connections before use
  - Auto-reconnect with exponential backoff on stale sockets
  - Handles daemon restarts/upgrades gracefully
- **Linting**: Fixed all errcheck warnings in production code
  - Proper error handling for database resources and transactions
  - Graceful EOF handling in interactive input
- **Linting**: Fixed revive style issues
  - Removed unused parameters, renamed builtin shadowing
- **Linting**: Fixed goconst warnings

## [0.17.0] - 2025-10-24

### Added
- **Git Hooks**: Automatic installation prompt during `bd init`
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
- **Multi-Database Warning**: Detect and warn about nested beads databases
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
- **Bug**: Daemon auto-start reliability
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
- **Automated Releases**: GoReleaser workflow for cross-platform binaries
  - Automatic GitHub releases on version tags
  - Linux, macOS, Windows binaries for amd64 and arm64
  - Checksums and changelog generation included
- **PyPI Automation**: Automated MCP server publishing to PyPI
  - GitHub Actions workflow publishes beads-mcp on version tags
  - Eliminates manual PyPI upload step
- **Sandbox Mode**: `--sandbox` flag for Claude Code integration
  - Isolated environment for AI agent experimentation
  - Prevents production database modifications during testing

### Fixed
- **Critical**: Idempotent import timestamp churn
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
- **Lifecycle Safety Documentation**: Complete documentation for UnderlyingDB() usage
  - Added tracking guidelines for database lifecycle safety
  - Documented transaction management best practices
  - Prevents UAF (use-after-free) bugs in extensions

### Fixed
- **Critical**: Git worktree detection and warnings
  - Added automatic detection when running in git worktrees
  - Displays prominent warning if daemon mode is active in worktree
  - Prevents daemon from committing/pushing to wrong branch
  - Documents `--no-daemon` flag as solution for worktree users
- **Critical**: Multiple daemon race condition
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
- **Daemon RPC Improvements**: Enhanced sync operations
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
- **Critical**: Daemon cache staleness
  - Daemon now detects external database modifications via mtime check
  - Prevents serving stale data after external `bd import`, `rm bd.db`, etc.
  - Cache automatically invalidates when DB file changes
- **Critical**: Counter desync after deletions
  - Issue counters now sync correctly after bulk deletions
  - Prevents ID gaps and counter drift
- **Critical**: Labels and dependencies not persisted in daemon mode (#101)
  - Fixed label operations failing silently in daemon mode
  - Fixed dependency operations not saving in daemon mode
  - Both now correctly propagate through RPC layer
- **Daemon sync support**: `bd sync` command now works in daemon mode
  - Previously crashed with nil pointer when daemon running
  - Export operations now properly routed through RPC
- **Acceptance flag normalization**: Unified `--acceptance` flag behavior (bd-228, #102)
  - Added `--acceptance-criteria` as clearer alias
  - Both flags work identically for backward compatibility
- **Auto-import Git conflicts**: Better detection of merge conflicts
  - Auto-import detects and warns about unresolved Git merge conflicts
  - Prevents importing corrupted JSONL with conflict markers
  - Clear instructions for resolving conflicts

### Changed
- **BREAKING**: Removed global daemon socket fallback
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
- **Agent Onboarding**: New `bd onboard` command for agent-first documentation
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
- **Label Filtering**: Enhanced `bd list` command with label-based filtering
  - `--label` (or `-l`): Filter by multiple labels with AND semantics (must have ALL)
  - `--label-any`: Filter by multiple labels with OR semantics (must have AT LEAST ONE)
  - Examples:
    - `bd list --label backend,urgent`: Issues with both 'backend' AND 'urgent'
    - `bd list --label-any frontend,backend`: Issues with either 'frontend' OR 'backend'
  - Works in both daemon and direct modes
  - Includes comprehensive test coverage
- **Log Rotation**: Automatic daemon log rotation with configurable limits
  - Prevents unbounded log file growth for long-running daemons
  - Configurable via environment variables: `BEADS_DAEMON_LOG_MAX_SIZE`, `BEADS_DAEMON_LOG_MAX_BACKUPS`, `BEADS_DAEMON_LOG_MAX_AGE`, `BEADS_DAEMON_LOG_COMPRESS`
  - Optional compression of rotated logs
  - Defaults: 50MB max size, 7 backups, 30 day retention, compression enabled
- **Batch Deletion**: Enhanced `bd delete` command with batch operations
  - Delete multiple issues at once: `bd delete bd-1 bd-2 bd-3 --force`
  - Read from file: `bd delete --from-file deletions.txt --force`
  - Dry-run mode: `--dry-run` to preview deletions before execution
  - Cascade mode: `--cascade` to recursively delete all dependents
  - Force mode: `--force` to orphan dependents instead of failing
  - Atomic transactions: all deletions succeed or none do
  - Comprehensive statistics: tracks deleted issues, dependencies, labels, and events

### Fixed
- **Critical**: `bd list --status all` showing 0 issues
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
  - Tests for init command edge cases

### Fixed
- **Critical**: Metadata errors causing crashes on first import
  - Auto-import now treats missing metadata as first import instead of failing
  - Eliminates initialization errors in fresh repositories
- **Critical**: N+1 query pattern in auto-import
  - Replaced per-issue queries with batch fetching
  - Dramatically improves performance for large imports
- **Critical**: Duplicate issue imports
  - Added deduplication logic to prevent importing same issue multiple times
  - Maintains data integrity during repeated imports
- **Bug**: Auto-flush missing after renumber/rename-prefix
  - Commands now properly export to JSONL after completion
  - Ensures git sees latest changes immediately
- **Bug**: Renumber ID collision with UUID temp IDs
  - Uses proper UUID-based temporary IDs to prevent conflicts during renumbering
  - ID counter now correctly syncs after renumbering operations
- **Bug**: Collision resolution dependency handling
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
- Cycle detection performance benchmarks added

### Testing
- Integration tests for daemon RPC request/response flows
- End-to-end coverage for delete/restore lifecycles  
- Regression tests for metadata handling, auto-flush, ID counter sync
- Comprehensive tests for collision detection in auto-import

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
- **Cross-Type Cycle Prevention**: Dependency cycles detected across all types
  - Prevents A→B→A cycles even when mixing `blocks`, `related`, etc.
  - Semantic validation for parent-child direction
  - Diagnostic warnings when cycles detected

### Fixed
- **Critical**: Auto-import collision skipping bug (bd-393, bd-228)
  - Import would silently skip collisions instead of remapping
  - Could cause data loss when merging branches
  - Now correctly applies collision resolution with remapping
- **Critical**: Transaction state corruption
  - Nested transactions could corrupt database state
  - Fixed with proper transaction boundary handling
- **Critical**: Concurrent temp file collisions (bd-306, bd-373)
  - Multiple `bd` processes would collide on shared `.tmp` filename
  - Now uses PID suffix for temp files: `.beads/issues.jsonl.tmp.12345`
- **Critical**: Circular dependency detection gaps
  - Some cycle patterns were missed by detection algorithm
  - Enhanced with comprehensive cycle prevention
- **Bug**: False positive merge conflict detection (bd-313, bd-270)
  - Auto-import would detect conflicts when none existed
  - Fixed with improved Git conflict marker detection
- **Bug**: Import timeout with large issue sets
  - 200+ issue imports would timeout
  - Optimized import performance
- **Bug**: Collision resolver missing ID counter sync
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
- Cycle detection benchmarks added
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
- **Critical**: Hash-based auto-import replaces mtime comparison
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
