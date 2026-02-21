package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
)

var migrateWispsCmd = &cobra.Command{
	Use:   "wisps",
	Short: "Migrate ephemeral SQLite data to Dolt wisps tables",
	Long: `Copies all issues and dependencies from the ephemeral SQLite store
(.beads/ephemeral.sqlite3) into the Dolt wisps and wisp_dependencies tables.

The wisps table is marked in dolt_ignore, so this data won't pollute Dolt
commit history. The SQLite files are kept as backup.

This is Phase 3 of the wisps migration: moving existing ephemeral data from
SQLite into Dolt's untracked wisps tables so that all ephemeral state lives
in a single database engine.`,
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("migrate wisps")

		dryRun, _ := cmd.Flags().GetBool("dry-run")

		if store == nil {
			fmt.Fprintln(os.Stderr, "Error: store not initialized")
			os.Exit(1)
		}

		es := store.EphemeralStore()
		if es == nil {
			fmt.Fprintln(os.Stderr, "Error: ephemeral store not available (no ephemeral.sqlite3)")
			os.Exit(1)
		}

		ctx := context.Background()
		doltDB := store.UnderlyingDB()

		// Ensure wisps table exists (should have been created by migration 004).
		if err := ensureWispsTable(ctx, doltDB); err != nil {
			fmt.Fprintf(os.Stderr, "Error ensuring wisps table: %v\n", err)
			os.Exit(1)
		}

		// Ensure wisp_dependencies table exists (matches wisp_% dolt_ignore pattern).
		if err := ensureWispDependenciesTable(ctx, doltDB); err != nil {
			fmt.Fprintf(os.Stderr, "Error ensuring wisp_dependencies table: %v\n", err)
			os.Exit(1)
		}

		sqliteDB := es.DB()

		// Count source rows
		var srcIssueCount, srcDepCount int
		if err := sqliteDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&srcIssueCount); err != nil {
			fmt.Fprintf(os.Stderr, "Error counting SQLite issues: %v\n", err)
			os.Exit(1)
		}
		if err := sqliteDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM dependencies").Scan(&srcDepCount); err != nil {
			fmt.Fprintf(os.Stderr, "Error counting SQLite dependencies: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Source: %s\n", es.Path())
		fmt.Printf("  Issues:       %d\n", srcIssueCount)
		fmt.Printf("  Dependencies: %d\n", srcDepCount)

		if srcIssueCount == 0 {
			fmt.Println("\nNo issues to migrate.")
			return
		}

		if dryRun {
			fmt.Println("\nDry run — no changes made.")
			return
		}

		start := time.Now()

		// Migrate issues
		migratedIssues, skippedIssues, err := migrateIssuesToWisps(ctx, sqliteDB, doltDB)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error migrating issues: %v\n", err)
			os.Exit(1)
		}

		// Migrate dependencies
		migratedDeps, skippedDeps, err := migrateDepsToWispDeps(ctx, sqliteDB, doltDB)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error migrating dependencies: %v\n", err)
			os.Exit(1)
		}

		elapsed := time.Since(start)

		// Verify destination counts
		var dstIssueCount, dstDepCount int
		_ = doltDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM wisps").Scan(&dstIssueCount)
		_ = doltDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM wisp_dependencies").Scan(&dstDepCount)

		fmt.Printf("\nMigration complete in %v:\n", elapsed.Round(time.Millisecond))
		fmt.Printf("  Issues migrated:  %d (skipped %d duplicates)\n", migratedIssues, skippedIssues)
		fmt.Printf("  Deps migrated:    %d (skipped %d duplicates)\n", migratedDeps, skippedDeps)
		fmt.Printf("\nVerification:\n")
		fmt.Printf("  Wisps table:             %d rows\n", dstIssueCount)
		fmt.Printf("  Wisp_dependencies table: %d rows\n", dstDepCount)

		if dstIssueCount >= srcIssueCount {
			fmt.Println("  ✓ Issue counts match or exceed source")
		} else {
			fmt.Printf("  ⚠ Wisps table has %d rows, source had %d\n", dstIssueCount, srcIssueCount)
		}
		if dstDepCount >= srcDepCount {
			fmt.Println("  ✓ Dependency counts match or exceed source")
		} else {
			fmt.Printf("  ⚠ Wisp_dependencies has %d rows, source had %d\n", dstDepCount, srcDepCount)
		}

		fmt.Println("\nSQLite file kept as backup. Remove manually when stable.")

		commandDidWrite.Store(true)
	},
}

// ensureWispsTable verifies the wisps table exists in Dolt.
func ensureWispsTable(ctx context.Context, db *sql.DB) error {
	var count int
	err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'wisps'").Scan(&count)
	if err != nil {
		return fmt.Errorf("check wisps table: %w", err)
	}
	if count == 0 {
		return fmt.Errorf("wisps table does not exist — run schema migrations first (bd-f6b62)")
	}
	return nil
}

// ensureWispDependenciesTable creates the wisp_dependencies table if it doesn't exist.
// The table name matches the wisp_% dolt_ignore pattern, so it won't be tracked.
func ensureWispDependenciesTable(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, wispDependenciesSchema)
	return err
}

const wispDependenciesSchema = `CREATE TABLE IF NOT EXISTS wisp_dependencies (
    issue_id VARCHAR(255) NOT NULL,
    depends_on_id VARCHAR(255) NOT NULL,
    type VARCHAR(32) NOT NULL DEFAULT 'blocks',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by VARCHAR(255) NOT NULL DEFAULT '',
    metadata JSON DEFAULT (JSON_OBJECT()),
    thread_id VARCHAR(255) DEFAULT '',
    PRIMARY KEY (issue_id, depends_on_id),
    INDEX idx_wisp_dep_issue (issue_id),
    INDEX idx_wisp_dep_depends (depends_on_id)
)`

// migrateIssuesToWisps reads all issues from SQLite and INSERTs into Dolt wisps table.
// Returns (migrated, skipped, error). Skipped means the row already existed.
func migrateIssuesToWisps(ctx context.Context, sqliteDB, doltDB *sql.DB) (int, int, error) {
	rows, err := sqliteDB.QueryContext(ctx, `SELECT
		id, content_hash, title, description, design, acceptance_criteria, notes,
		status, priority, issue_type, assignee, estimated_minutes,
		created_at, created_by, owner, updated_at, closed_at, closed_by_session, external_ref, spec_id,
		compaction_level, compacted_at, compacted_at_commit, original_size, source_repo, close_reason,
		sender, ephemeral, wisp_type, pinned, is_template, crystallizes,
		await_type, await_id, timeout_ns, waiters,
		hook_bead, role_bead, agent_state, last_activity, role_type, rig, mol_type,
		event_kind, actor, target, payload,
		due_at, defer_until,
		quality_score, work_type, source_system, metadata
	FROM issues`)
	if err != nil {
		return 0, 0, fmt.Errorf("query SQLite issues: %w", err)
	}
	defer rows.Close()

	migrated, skipped := 0, 0

	for rows.Next() {
		// Scan all columns as nullable strings/values to avoid type mismatch issues
		// between SQLite (TEXT) and Dolt (VARCHAR/DATETIME/etc.)
		var (
			id, contentHash, title, description, design, acceptanceCriteria, notes sql.NullString
			status, issueType, assignee                                           sql.NullString
			priority                                                              sql.NullInt64
			estimatedMinutes                                                      sql.NullInt64
			createdAt, createdBy, owner, updatedAt                                sql.NullString
			closedAt, closedBySession, externalRef, specID                        sql.NullString
			compactionLevel                                                       sql.NullInt64
			compactedAt, compactedAtCommit                                        sql.NullString
			originalSize                                                          sql.NullInt64
			sourceRepo, closeReason                                               sql.NullString
			sender, wispType                                                      sql.NullString
			ephemeral, pinned, isTemplate, crystallizes                           sql.NullInt64
			awaitType, awaitID                                                    sql.NullString
			timeoutNs                                                             sql.NullInt64
			waiters                                                               sql.NullString
			hookBead, roleBead, agentState, lastActivity, roleType, rig, molType  sql.NullString
			eventKind, actor, target, payload                                     sql.NullString
			dueAt, deferUntil                                                     sql.NullString
			qualityScore                                                          sql.NullFloat64
			workType, sourceSystem, metadata                                      sql.NullString
		)

		if err := rows.Scan(
			&id, &contentHash, &title, &description, &design, &acceptanceCriteria, &notes,
			&status, &priority, &issueType, &assignee, &estimatedMinutes,
			&createdAt, &createdBy, &owner, &updatedAt, &closedAt, &closedBySession, &externalRef, &specID,
			&compactionLevel, &compactedAt, &compactedAtCommit, &originalSize, &sourceRepo, &closeReason,
			&sender, &ephemeral, &wispType, &pinned, &isTemplate, &crystallizes,
			&awaitType, &awaitID, &timeoutNs, &waiters,
			&hookBead, &roleBead, &agentState, &lastActivity, &roleType, &rig, &molType,
			&eventKind, &actor, &target, &payload,
			&dueAt, &deferUntil,
			&qualityScore, &workType, &sourceSystem, &metadata,
		); err != nil {
			return migrated, skipped, fmt.Errorf("scan SQLite row: %w", err)
		}

		_, err := doltDB.ExecContext(ctx, `INSERT IGNORE INTO wisps (
			id, content_hash, title, description, design, acceptance_criteria, notes,
			status, priority, issue_type, assignee, estimated_minutes,
			created_at, created_by, owner, updated_at, closed_at, closed_by_session, external_ref, spec_id,
			compaction_level, compacted_at, compacted_at_commit, original_size, source_repo, close_reason,
			sender, ephemeral, wisp_type, pinned, is_template, crystallizes,
			await_type, await_id, timeout_ns, waiters,
			hook_bead, role_bead, agent_state, last_activity, role_type, rig, mol_type,
			event_kind, actor, target, payload,
			due_at, defer_until,
			quality_score, work_type, source_system, metadata
		) VALUES (
			?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?,
			?, ?, ?, ?
		)`,
			id, contentHash, title, description, design, acceptanceCriteria, notes,
			status, priority, issueType, assignee, estimatedMinutes,
			createdAt, createdBy, owner, updatedAt, closedAt, closedBySession, externalRef, specID,
			compactionLevel, compactedAt, compactedAtCommit, originalSize, sourceRepo, closeReason,
			sender, ephemeral, wispType, pinned, isTemplate, crystallizes,
			awaitType, awaitID, timeoutNs, waiters,
			hookBead, roleBead, agentState, lastActivity, roleType, rig, molType,
			eventKind, actor, target, payload,
			dueAt, deferUntil,
			qualityScore, workType, sourceSystem, metadata,
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: failed to insert %s: %v\n", id.String, err)
			skipped++
			continue
		}

		migrated++
	}

	return migrated, skipped, rows.Err()
}

// migrateDepsToWispDeps reads all dependencies from SQLite and INSERTs into Dolt wisp_dependencies.
func migrateDepsToWispDeps(ctx context.Context, sqliteDB, doltDB *sql.DB) (int, int, error) {
	rows, err := sqliteDB.QueryContext(ctx,
		`SELECT issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id
		 FROM dependencies`)
	if err != nil {
		return 0, 0, fmt.Errorf("query SQLite dependencies: %w", err)
	}
	defer rows.Close()

	migrated, skipped := 0, 0

	for rows.Next() {
		var issueID, dependsOnID, depType, createdAt, createdBy sql.NullString
		var metadata, threadID sql.NullString

		if err := rows.Scan(&issueID, &dependsOnID, &depType, &createdAt, &createdBy, &metadata, &threadID); err != nil {
			return migrated, skipped, fmt.Errorf("scan SQLite dependency: %w", err)
		}

		_, err := doltDB.ExecContext(ctx, `INSERT IGNORE INTO wisp_dependencies
			(issue_id, depends_on_id, type, created_at, created_by, metadata, thread_id)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			issueID, dependsOnID, depType, createdAt, createdBy, metadata, threadID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: failed to insert dep %s->%s: %v\n",
				issueID.String, dependsOnID.String, err)
			skipped++
			continue
		}

		migrated++
	}

	return migrated, skipped, rows.Err()
}

func init() {
	migrateWispsCmd.Flags().Bool("dry-run", false, "Show source counts without migrating")
	migrateCmd.AddCommand(migrateWispsCmd)
}
