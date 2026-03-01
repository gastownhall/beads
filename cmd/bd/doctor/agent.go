package doctor

import "fmt"

// AgentDiagnostic represents a single check result enriched for agent consumption.
// ZFC-compliant: Go observes and reports, the agent decides and acts.
type AgentDiagnostic struct {
	Name        string   `json:"name"`
	Status      string   `json:"status"`   // "error", "warning", "ok"
	Severity    string   `json:"severity"` // "blocking", "degraded", "advisory"
	Category    string   `json:"category"`
	Explanation string   `json:"explanation"`            // Full prose: what's wrong and why it matters
	Observed    string   `json:"observed"`               // What was actually found
	Expected    string   `json:"expected"`               // What should be the case
	Commands    []string `json:"commands,omitempty"`     // Exact remediation commands in order
	SourceFiles []string `json:"source_files,omitempty"` // Relevant source paths for investigation
}

// agentEnrichment holds the extra context fields an enricher adds.
type agentEnrichment struct {
	severity    string
	explanation string
	observed    string
	expected    string
	commands    []string
	sourceFiles []string
}

// enricher is a function that produces agent enrichment from a DoctorCheck.
type enricher func(dc DoctorCheck) agentEnrichment

// EnrichForAgent converts a DoctorCheck into an AgentDiagnostic with rich context.
func EnrichForAgent(dc DoctorCheck) AgentDiagnostic {
	ad := AgentDiagnostic{
		Name:     dc.Name,
		Status:   dc.Status,
		Category: dc.Category,
	}

	// Look up a specialized enricher for this check name
	if fn, ok := agentEnrichers[dc.Name]; ok {
		e := fn(dc)
		ad.Severity = e.severity
		ad.Explanation = e.explanation
		ad.Observed = e.observed
		ad.Expected = e.expected
		ad.Commands = e.commands
		ad.SourceFiles = e.sourceFiles
		return ad
	}

	// Generic fallback: synthesize from existing DoctorCheck fields
	ad.Severity = severityFromStatus(dc.Status)
	ad.Explanation = buildGenericExplanation(dc)
	ad.Observed = dc.Message
	if dc.Detail != "" {
		ad.Observed += "\n" + dc.Detail
	}
	ad.Expected = "Check passes with status OK"
	if dc.Fix != "" {
		ad.Commands = []string{dc.Fix}
	}
	return ad
}

func severityFromStatus(status string) string {
	switch status {
	case StatusError:
		return "blocking"
	case StatusWarning:
		return "degraded"
	default:
		return "advisory"
	}
}

func buildGenericExplanation(dc DoctorCheck) string {
	s := fmt.Sprintf("%s check reported %s: %s", dc.Name, dc.Status, dc.Message)
	if dc.Detail != "" {
		s += " " + dc.Detail
	}
	if dc.Fix != "" {
		s += " To fix: " + dc.Fix
	}
	return s
}

// agentEnrichers maps check names to specialized enrichment functions.
var agentEnrichers = map[string]enricher{
	"Installation":                 enrichInstallation,
	"Permissions":                  enrichPermissions,
	"Database":                     enrichDatabase,
	"Schema Compatibility":         enrichSchemaCompatibility,
	"Database Integrity":           enrichDatabaseIntegrity,
	"Large Database":               enrichLargeDatabase,
	"ID Format":                    enrichIDFormat,
	"CLI Version":                  enrichCLIVersion,
	"Git Hooks":                    enrichGitHooks,
	"Git Hooks Dolt Compatibility": enrichGitHooksDolt,
	"Gitignore":                    enrichGitignore,
	"Project Gitignore":            enrichProjectGitignore,
	"Git Working Tree":             enrichGitWorkingTree,
	"Git Upstream":                 enrichGitUpstream,
	"Fresh Clone":                  enrichFreshClone,
	"Database Config":              enrichDatabaseConfig,
	"Config Values":                enrichConfigValues,
	"Role Configuration":           enrichBeadsRole,
	"Lock Files":                   enrichStaleLockFiles,
	"Dolt Connection":              enrichDoltConnection,
	"Dolt Schema":                  enrichDoltSchema,
	"Dolt Issue Count":             enrichDoltIssueCount,
	"Dolt Status":                  enrichDoltStatus,
	"Dolt Locks":                   enrichDoltLocks,
	"Dolt Lock Health":             enrichLockHealth,
	"Dependency Cycles":            enrichDependencyCycles,
	"Duplicate Issues":             enrichDuplicateIssues,
	"Test Pollution":               enrichTestPollution,
	"Orphaned Dependencies":        enrichOrphanedDeps,
	"Child-Parent Dependencies":    enrichChildParentDeps,
	"Merge Artifacts":              enrichMergeArtifacts,
	"Classic Artifacts":            enrichClassicArtifacts,
	"Broken Migration State":       enrichBrokenMigration,
	"SQLite Residue":               enrichSQLiteResidue,
	"Embedded Mode Concurrency":    enrichEmbeddedConcurrency,
	"Pending Migrations":           enrichPendingMigrations,
	"KV Sync Status":               enrichKVSync,
	"Stale Closed Issues":          enrichStaleClosedIssues,
	"Stale Molecules":              enrichStaleMolecules,
	"Claude Integration":           enrichClaude,
	"Claude Settings Health":       enrichClaudeSettings,
	"Claude Hook Completeness":     enrichClaudeHooks,
	"Claude Plugin":                enrichClaudePlugin,
	"bd prime Output":              enrichBdPrimeOutput,
	"CLI Availability":             enrichBdInPath,
	"Repo Fingerprint":             enrichRepoFingerprint,
	"Version Tracking":             enrichMetadataVersion,
	"Orphaned Issues":              enrichOrphanedIssues,
	"Redirect Target Valid":        enrichRedirectTarget,
	"Redirect Tracking":            enrichRedirectTracking,
	"Redirect Target Sync":         enrichRedirectTargetSync,
	"Untracked Files":              enrichUntrackedFiles,
}

// --- Enrichment functions ---

func enrichInstallation(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "blocking",
		explanation: "The .beads/ directory does not exist in this project. Beads cannot function without it. This is the data directory that holds the issue database, configuration, and metadata.",
		observed:    dc.Message,
		expected:    ".beads/ directory exists at project root with config.yaml and dolt/ database inside",
		commands:    []string{"bd init"},
		sourceFiles: []string{"cmd/bd/doctor/installation.go:CheckInstallation"},
	}
}

func enrichPermissions(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "blocking",
		explanation: fmt.Sprintf("File permission issue in .beads/ directory: %s. The database or directory cannot be read/written, which prevents all beads operations.", dc.Message),
		observed:    dc.Message,
		expected:    ".beads/ directory and all contents are readable and writable by current user",
		commands:    []string{"bd doctor --fix", "chmod -R u+rw .beads/"},
		sourceFiles: []string{"cmd/bd/doctor/installation.go:CheckPermissions"},
	}
}

func enrichDatabase(dc DoctorCheck) agentEnrichment {
	e := agentEnrichment{
		observed:    dc.Message,
		sourceFiles: []string{"cmd/bd/doctor/database.go:CheckDatabaseVersion", "internal/storage/dolt/migrations.go"},
	}
	if dc.Status == StatusError {
		e.severity = "blocking"
		e.explanation = fmt.Sprintf("The Dolt database is broken or missing: %s. Without a working database, no beads commands will function.", dc.Message)
		e.expected = "Dolt database opens successfully and contains bd_version metadata matching CLI version"
		e.commands = []string{"bd doctor --fix", "rm -rf .beads/dolt && bd init"}
	} else {
		e.severity = "degraded"
		e.explanation = fmt.Sprintf("Database version mismatch: %s. The database was last written by a different bd version. Auto-migration should resolve this.", dc.Message)
		e.expected = "Database bd_version metadata matches CLI version"
		e.commands = []string{"bd doctor --fix"}
	}
	if dc.Detail != "" {
		e.observed += "\n" + dc.Detail
	}
	return e
}

func enrichSchemaCompatibility(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "blocking",
		explanation: fmt.Sprintf("The database schema is incompatible with the current CLI version: %s. Core tables (issues, dependencies, metadata, config, wisps) must be queryable. This usually means a migration hasn't been applied.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "All core tables (issues, dependencies, metadata, config, wisps) are queryable with the current schema",
		commands:    []string{"bd migrate", "rm -rf .beads/dolt && bd init"},
		sourceFiles: []string{"cmd/bd/doctor/database.go:CheckSchemaCompatibility", "internal/storage/dolt/migrations.go"},
	}
}

func enrichDatabaseIntegrity(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "blocking",
		explanation: fmt.Sprintf("Database integrity check failed: %s. Basic queries against metadata and statistics tables failed, suggesting the database may be corrupted.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "Metadata table readable, statistics query succeeds",
		commands:    []string{"bd doctor --fix", "rm -rf .beads/dolt && bd init"},
		sourceFiles: []string{"cmd/bd/doctor/database.go:CheckDatabaseIntegrity"},
	}
}

func enrichLargeDatabase(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "advisory",
		explanation: fmt.Sprintf("The database has accumulated many closed issues: %s. This may cause performance degradation in list/search operations. Pruning is optional and destructive.", dc.Message),
		observed:    dc.Message,
		expected:    "Closed issue count below configured threshold",
		commands:    []string{"bd cleanup --older-than 90"},
		sourceFiles: []string{"cmd/bd/doctor/database.go:CheckDatabaseSize"},
	}
}

func enrichIDFormat(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "degraded",
		explanation: fmt.Sprintf("ID format issue: %s. Beads uses hash-based IDs (e.g. bd-a1b2c). Sequential numeric IDs indicate a very old database that should be migrated.", dc.Message),
		observed:    dc.Message,
		expected:    "All issues use hash-based IDs with configured prefix",
		commands:    []string{"bd doctor --fix"},
		sourceFiles: []string{"cmd/bd/doctor/integrity.go:CheckIDFormat"},
	}
}

func enrichCLIVersion(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "advisory",
		explanation: fmt.Sprintf("CLI version check: %s. An outdated CLI may lack bug fixes or schema migrations needed by the current database.", dc.Message),
		observed:    dc.Message,
		expected:    "CLI version matches latest GitHub release",
		commands:    []string{"go install github.com/steveyegge/beads/cmd/bd@latest"},
		sourceFiles: []string{"cmd/bd/doctor/version.go:CheckCLIVersion"},
	}
}

func enrichGitHooks(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "degraded",
		explanation: fmt.Sprintf("Git hooks issue: %s. Beads git hooks auto-sync the database on commit/merge/push. Without them, changes won't propagate to other clones.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "Git hooks installed and version matches CLI version",
		commands:    []string{"bd hooks install"},
		sourceFiles: []string{"cmd/bd/doctor/git.go:CheckGitHooks", "cmd/bd/doctor/fix/hooks.go"},
	}
}

func enrichGitHooksDolt(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "blocking",
		explanation: fmt.Sprintf("Git hooks are incompatible with Dolt backend: %s. Hooks that predate the Dolt migration may run SQLite operations on a Dolt database, causing errors on every git operation.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "Git hooks contain Dolt-compatible sync commands",
		commands:    []string{"bd hooks install"},
		sourceFiles: []string{"cmd/bd/doctor/git.go:CheckGitHooksDoltCompatibility"},
	}
}

func enrichGitignore(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "degraded",
		explanation: fmt.Sprintf("Gitignore issue: %s. The .beads/.gitignore must exclude runtime files (dolt/, locks, temp files) while tracking config.yaml and metadata.json.", dc.Message),
		observed:    dc.Message,
		expected:    ".beads/.gitignore is up to date with current patterns",
		commands:    []string{"bd doctor --fix"},
		sourceFiles: []string{"cmd/bd/doctor/gitignore.go:CheckGitignore"},
	}
}

func enrichProjectGitignore(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "degraded",
		explanation: fmt.Sprintf("Project .gitignore issue: %s. The project-root .gitignore needs Dolt exclusion patterns to prevent committing large binary database files.", dc.Message),
		observed:    dc.Message,
		expected:    "Project .gitignore contains .beads/dolt/ exclusion pattern",
		commands:    []string{"bd doctor --fix"},
		sourceFiles: []string{"cmd/bd/doctor/gitignore.go:CheckProjectGitignore"},
	}
}

func enrichGitWorkingTree(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "advisory",
		explanation: fmt.Sprintf("Git working tree is dirty: %s. Uncommitted changes in .beads/ files may indicate interrupted operations or manual edits.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "Git working tree clean (no uncommitted .beads/ changes)",
		commands:    []string{"git status .beads/", "git add .beads/ && git commit -m 'sync beads state'"},
		sourceFiles: []string{"cmd/bd/doctor/git.go:CheckGitWorkingTree"},
	}
}

func enrichGitUpstream(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "advisory",
		explanation: fmt.Sprintf("Git upstream drift: %s. Local branch is out of sync with remote. This may mean beads changes from other clones haven't been pulled.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "Local branch is in sync with upstream tracking branch",
		commands:    []string{"git pull --rebase"},
		sourceFiles: []string{"cmd/bd/doctor/git.go:CheckGitUpstream"},
	}
}

func enrichFreshClone(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "blocking",
		explanation: fmt.Sprintf("Fresh clone detected: %s. The .beads/ directory exists (committed to git) but the local database hasn't been initialized. Run bd init to bootstrap from the tracked config.", dc.Message),
		observed:    dc.Message,
		expected:    "Local database initialized and ready for use",
		commands:    []string{"bd init"},
		sourceFiles: []string{"cmd/bd/doctor/legacy.go:CheckFreshClone"},
	}
}

func enrichDatabaseConfig(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "degraded",
		explanation: fmt.Sprintf("Database configuration mismatch: %s. The config.yaml backend setting doesn't match what's actually on disk. This can happen after a migration or manual file manipulation.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "config.yaml backend setting matches actual database on disk",
		commands:    []string{"bd doctor --fix"},
		sourceFiles: []string{"cmd/bd/doctor/legacy.go:CheckDatabaseConfig", "internal/configfile/config.go"},
	}
}

func enrichConfigValues(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "advisory",
		explanation: fmt.Sprintf("Configuration value issue: %s. One or more config values are invalid, unknown, or using deprecated keys.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "All config values are valid and recognized",
		commands:    []string{"bd config list", "bd doctor --fix"},
		sourceFiles: []string{"cmd/bd/doctor/config_values.go:CheckConfigValues"},
	}
}

func enrichBeadsRole(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "advisory",
		explanation: fmt.Sprintf("Beads role configuration: %s. The beads.role config determines how beads behaves (e.g. 'maintainer' vs 'contributor'). Missing role config falls back to URL heuristic detection.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "beads.role is configured in config.yaml",
		commands:    []string{"bd config set beads.role maintainer"},
		sourceFiles: []string{"cmd/bd/doctor/role.go:CheckBeadsRole"},
	}
}

func enrichStaleLockFiles(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "degraded",
		explanation: fmt.Sprintf("Stale lock files detected: %s. Lock files from crashed or killed bd processes prevent new operations. They are safe to remove if no bd process is currently running.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "No stale lock files in .beads/",
		commands:    []string{"bd doctor --fix"},
		sourceFiles: []string{"cmd/bd/doctor/locks.go:CheckStaleLockFiles"},
	}
}

func enrichDoltConnection(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "blocking",
		explanation: fmt.Sprintf("Cannot connect to Dolt database: %s. Either the embedded Dolt engine failed to start, or the Dolt server (if using server mode) is unreachable.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "Dolt database opens successfully (embedded) or server is reachable (server mode)",
		commands:    []string{"bd doctor --fix", "gt dolt status", "gt dolt start"},
		sourceFiles: []string{"cmd/bd/doctor/dolt.go:CheckDoltConnection"},
	}
}

func enrichDoltSchema(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "blocking",
		explanation: fmt.Sprintf("Dolt schema issue: %s. Required tables or columns are missing from the Dolt database. This usually means a migration is needed.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "All required tables and columns present in Dolt database",
		commands:    []string{"bd migrate", "bd doctor --fix"},
		sourceFiles: []string{"cmd/bd/doctor/dolt.go:CheckDoltSchema", "internal/storage/dolt/migrations.go"},
	}
}

func enrichDoltIssueCount(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "advisory",
		explanation: fmt.Sprintf("Dolt issue count: %s. Reports the number of issues in the Dolt database for basic sanity checking.", dc.Message),
		observed:    dc.Message,
		expected:    "Issue count is non-zero (unless this is a new/empty database)",
		sourceFiles: []string{"cmd/bd/doctor/dolt.go:CheckDoltIssueCount"},
	}
}

func enrichDoltStatus(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    severityFromStatus(dc.Status),
		explanation: fmt.Sprintf("Dolt database status: %s. Reports uncommitted changes, dirty working set, or other Dolt-specific state issues.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "Dolt working set is clean (no uncommitted changes)",
		commands:    []string{"bd sync"},
		sourceFiles: []string{"cmd/bd/doctor/dolt.go:CheckDoltStatus"},
	}
}

func enrichDoltLocks(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "degraded",
		explanation: fmt.Sprintf("Dolt lock issue: %s. Noms LOCK files prevent concurrent database access. If no bd process is running, these are stale and safe to remove.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "No stale noms LOCK files in .beads/dolt/",
		commands:    []string{"bd doctor --fix", "find .beads/dolt -name LOCK -delete"},
		sourceFiles: []string{"cmd/bd/doctor/migration_validation.go:CheckDoltLocks"},
	}
}

func enrichLockHealth(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "degraded",
		explanation: fmt.Sprintf("Lock health issue: %s. Database lock files may be held by another process or left from a crash. Check if another bd/dolt process is running before removing.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "No active or stale lock files",
		commands:    []string{"lsof +D .beads/dolt 2>/dev/null | grep LOCK", "bd doctor --fix"},
		sourceFiles: []string{"cmd/bd/doctor/dolt.go:CheckLockHealth"},
	}
}

func enrichDependencyCycles(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "degraded",
		explanation: fmt.Sprintf("Dependency cycle detected: %s. Circular dependencies prevent topological sorting and can confuse priority calculations. The cycle must be broken by removing one dependency edge.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "Dependency graph is a DAG (no cycles)",
		commands:    []string{"bd validate", "bd dep rm <issue-a> <issue-b>"},
		sourceFiles: []string{"cmd/bd/doctor/integrity.go:CheckDependencyCycles"},
	}
}

func enrichDuplicateIssues(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "advisory",
		explanation: fmt.Sprintf("Duplicate issues detected: %s. Multiple issues share the same title, which may indicate accidental creation. Review and close duplicates.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "No duplicate issue titles",
		commands:    []string{"bd validate", "bd close <duplicate-id>"},
		sourceFiles: []string{"cmd/bd/doctor/validation.go:CheckDuplicateIssues"},
	}
}

func enrichTestPollution(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "advisory",
		explanation: fmt.Sprintf("Test pollution detected: %s. Test-created issues are present in the production database. These are artifacts from test runs that didn't clean up properly.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "No test issues in production database",
		commands:    []string{"bd doctor --check=pollution", "bd doctor --check=pollution --clean"},
		sourceFiles: []string{"cmd/bd/doctor/validation.go:CheckTestPollution"},
	}
}

func enrichOrphanedDeps(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "advisory",
		explanation: fmt.Sprintf("Orphaned dependencies found: %s. Some dependency edges reference issues that no longer exist. These are harmless but noisy.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "All dependency edges reference existing issues",
		commands:    []string{"bd doctor --check=validate --fix"},
		sourceFiles: []string{"cmd/bd/doctor/validation.go:CheckOrphanedDependencies"},
	}
}

func enrichChildParentDeps(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "advisory",
		explanation: fmt.Sprintf("Child→parent dependencies found: %s. These are an anti-pattern where a child issue depends on its own parent. The parent-child relationship already implies ordering.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "No dependency edges between parent and child issues",
		commands:    []string{"bd doctor --fix --fix-child-parent"},
		sourceFiles: []string{"cmd/bd/doctor/validation.go:CheckChildParentDependencies"},
	}
}

func enrichMergeArtifacts(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "advisory",
		explanation: fmt.Sprintf("Merge artifacts found: %s. Git merge markers or backup files from conflict resolution are present in .beads/. These can be safely cleaned up.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "No merge artifacts in .beads/",
		commands:    []string{"bd doctor --fix"},
		sourceFiles: []string{"cmd/bd/doctor/validation.go:CheckMergeArtifacts"},
	}
}

func enrichClassicArtifacts(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "advisory",
		explanation: fmt.Sprintf("Classic (pre-Dolt) artifacts found: %s. These are leftover files from the SQLite/JSONL era that are no longer needed after Dolt migration.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "No classic artifacts present (JSONL files, SQLite database, cruft directories)",
		commands:    []string{"bd doctor --check=artifacts", "bd doctor --check=artifacts --clean"},
		sourceFiles: []string{"cmd/bd/doctor/artifacts.go:CheckClassicArtifacts"},
	}
}

func enrichBrokenMigration(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "blocking",
		explanation: fmt.Sprintf("Broken migration state: %s. The metadata says the backend is Dolt, but the Dolt database directory is missing or empty. This can happen if a migration was interrupted.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "If metadata says Dolt, then .beads/dolt/ exists and is a valid Dolt database",
		commands:    []string{"bd doctor --fix", "rm -rf .beads/dolt && bd init"},
		sourceFiles: []string{"cmd/bd/doctor/broken_migration.go:CheckBrokenMigrationState"},
	}
}

func enrichSQLiteResidue(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "advisory",
		explanation: fmt.Sprintf("SQLite residue after migration: %s. The old SQLite database file still exists after migrating to Dolt. It's safe to remove once you've verified the Dolt database works.", dc.Message),
		observed:    dc.Message,
		expected:    "No SQLite database files after Dolt migration",
		commands:    []string{"bd doctor --fix"},
		sourceFiles: []string{"cmd/bd/doctor/broken_migration.go:CheckSQLiteResidue"},
	}
}

func enrichEmbeddedConcurrency(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "advisory",
		explanation: fmt.Sprintf("Embedded Dolt concurrency concern: %s. Multiple bd processes accessing the same embedded Dolt database can cause lock contention. Consider switching to Dolt server mode for concurrent access.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "Either single-process access or Dolt server mode configured",
		commands:    []string{"gt dolt start", "bd config set dolt.server-mode true"},
		sourceFiles: []string{"cmd/bd/doctor/broken_migration.go:CheckEmbeddedModeConcurrency"},
	}
}

func enrichPendingMigrations(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    severityFromStatus(dc.Status),
		explanation: fmt.Sprintf("Pending migrations: %s. Database schema migrations are available but haven't been applied. Some may be required for correct operation.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "All available migrations have been applied",
		commands:    []string{"bd migrate"},
		sourceFiles: []string{"cmd/bd/doctor/migration.go:CheckPendingMigrations", "internal/storage/dolt/migrations.go"},
	}
}

func enrichKVSync(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "advisory",
		explanation: fmt.Sprintf("KV store sync status: %s. The key-value store may be out of sync with the main database.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "KV store is in sync with main database",
		commands:    []string{"bd sync"},
		sourceFiles: []string{"cmd/bd/doctor/kv.go:CheckKVSyncStatus"},
	}
}

func enrichStaleClosedIssues(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "advisory",
		explanation: fmt.Sprintf("Stale closed issues: %s. Old closed issues can be pruned to reduce database size and improve query performance.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "Closed issues are within acceptable age/count thresholds",
		commands:    []string{"bd cleanup --older-than 90"},
		sourceFiles: []string{"cmd/bd/doctor/maintenance.go:CheckStaleClosedIssues"},
	}
}

func enrichStaleMolecules(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "advisory",
		explanation: fmt.Sprintf("Stale molecules detected: %s. These are molecules (multi-step workflows) where all steps are complete but the molecule itself was never closed.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "Completed molecules are closed",
		commands:    []string{"bd mol list", "bd close <molecule-id>"},
		sourceFiles: []string{"cmd/bd/doctor/maintenance.go:CheckStaleMolecules"},
	}
}

func enrichClaude(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "advisory",
		explanation: fmt.Sprintf("Claude integration: %s. Beads integrates with Claude Code via hooks (SessionStart, PreCompact) defined in .claude/settings.json.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "Claude Code hooks configured for beads (bd prime on SessionStart)",
		commands:    []string{"bd hooks install"},
		sourceFiles: []string{"cmd/bd/doctor/claude.go:CheckClaude"},
	}
}

func enrichClaudeSettings(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "blocking",
		explanation: fmt.Sprintf("Claude settings file is malformed: %s. A corrupted .claude/settings.json prevents Claude Code from loading hooks, which breaks beads integration entirely.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    ".claude/settings.json is valid JSON",
		commands:    []string{"cat .claude/settings.json | python3 -m json.tool", "bd hooks install"},
		sourceFiles: []string{"cmd/bd/doctor/claude.go:CheckClaudeSettingsHealth"},
	}
}

func enrichClaudeHooks(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "advisory",
		explanation: fmt.Sprintf("Claude hook completeness: %s. Beads needs both SessionStart and PreCompact hooks to function properly in Claude Code sessions.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "Both SessionStart and PreCompact hooks configured for beads",
		commands:    []string{"bd hooks install"},
		sourceFiles: []string{"cmd/bd/doctor/claude.go:CheckClaudeHookCompleteness"},
	}
}

func enrichClaudePlugin(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "advisory",
		explanation: fmt.Sprintf("Claude plugin version: %s. An outdated Claude plugin may not support current beads features.", dc.Message),
		observed:    dc.Message,
		expected:    "Claude plugin version matches CLI version",
		commands:    []string{"bd hooks install"},
		sourceFiles: []string{"cmd/bd/doctor/claude.go:CheckClaudePlugin"},
	}
}

func enrichBdPrimeOutput(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "advisory",
		explanation: fmt.Sprintf("bd prime output issue: %s. The bd prime command's output may not match expected format, which can confuse agent context loading.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "bd prime produces valid context output",
		sourceFiles: []string{"cmd/bd/doctor/claude.go:VerifyPrimeOutput"},
	}
}

func enrichBdInPath(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "degraded",
		explanation: fmt.Sprintf("bd not in PATH: %s. Claude Code hooks invoke bd commands, but bd is not found in the system PATH. Hooks will fail silently.", dc.Message),
		observed:    dc.Message,
		expected:    "'bd' executable is in PATH and runnable",
		commands:    []string{"which bd", "go install github.com/steveyegge/beads/cmd/bd@latest"},
		sourceFiles: []string{"cmd/bd/doctor/claude.go:CheckBdInPath"},
	}
}

func enrichRepoFingerprint(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    severityFromStatus(dc.Status),
		explanation: fmt.Sprintf("Repo fingerprint mismatch: %s. The database may belong to a different repository (e.g. copied from another project or remote URL changed).", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "Database fingerprint matches current git remote URL",
		commands:    []string{"bd doctor --fix"},
		sourceFiles: []string{"cmd/bd/doctor/integrity.go:CheckRepoFingerprint"},
	}
}

func enrichMetadataVersion(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "advisory",
		explanation: fmt.Sprintf("Metadata version tracking: %s. The metadata.json LastBdVersion field tracks which bd version last wrote to this database.", dc.Message),
		observed:    dc.Message,
		expected:    "metadata.json LastBdVersion matches current CLI version",
		commands:    []string{"bd doctor --fix"},
		sourceFiles: []string{"cmd/bd/doctor/version.go:CheckMetadataVersionTracking"},
	}
}

func enrichOrphanedIssues(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "advisory",
		explanation: fmt.Sprintf("Orphaned issues: %s. These issues are referenced in git commits (e.g. 'fix(bd-xyz): ...') but are still open. They may have been accidentally left open after the fix was committed.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "Issues referenced in commits are closed",
		commands:    []string{"bd orphans", "bd close <issue-id>"},
		sourceFiles: []string{"cmd/bd/doctor/git.go:CheckOrphanedIssues"},
	}
}

func enrichRedirectTarget(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "degraded",
		explanation: fmt.Sprintf("Redirect target issue: %s. The .beads/redirect file points to an external directory that is missing or has an invalid database. This is used in worktree setups where multiple worktrees share one database.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "Redirect target exists and contains a valid beads database",
		commands:    []string{"cat .beads/redirect", "bd doctor --fix"},
		sourceFiles: []string{"cmd/bd/doctor/gitignore.go:CheckRedirectTargetValid"},
	}
}

func enrichRedirectTracking(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "advisory",
		explanation: fmt.Sprintf("Redirect file tracking: %s. The .beads/redirect file contains an absolute local path and should not be committed to git (it's machine-specific).", dc.Message),
		observed:    dc.Message,
		expected:    ".beads/redirect is in .gitignore and not tracked by git",
		commands:    []string{"echo '.beads/redirect' >> .beads/.gitignore", "git rm --cached .beads/redirect"},
		sourceFiles: []string{"cmd/bd/doctor/gitignore.go:CheckRedirectNotTracked"},
	}
}

func enrichRedirectTargetSync(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "advisory",
		explanation: fmt.Sprintf("Redirect target sync worktree: %s. The redirect target directory is missing a beads-sync worktree, which is needed for git-based synchronization of the shared database.", dc.Message),
		observed:    dc.Message + "\n" + dc.Detail,
		expected:    "Redirect target has a beads-sync worktree for database synchronization",
		commands:    []string{"bd init"},
		sourceFiles: []string{"cmd/bd/doctor/gitignore.go:CheckRedirectTargetSyncWorktree"},
	}
}

func enrichUntrackedFiles(dc DoctorCheck) agentEnrichment {
	return agentEnrichment{
		severity:    "advisory",
		explanation: fmt.Sprintf("Untracked beads files: %s. JSONL files in .beads/ are not tracked by git. In direct-commit mode, these should be committed to propagate changes.", dc.Message),
		observed:    dc.Message,
		expected:    "All .beads/*.jsonl files are tracked by git (or using sync-branch mode)",
		commands:    []string{"git add .beads/*.jsonl && git commit -m 'sync beads data'"},
		sourceFiles: []string{"cmd/bd/doctor/installation.go:CheckUntrackedBeadsFiles"},
	}
}
