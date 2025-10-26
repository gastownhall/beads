package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/storage/sqlite"
)

// StaleIssueInfo contains information about an orphaned issue claim
type StaleIssueInfo struct {
	IssueID            string    `json:"issue_id"`
	IssueTitle         string    `json:"issue_title"`
	IssuePriority      int       `json:"issue_priority"`
	ExecutorInstanceID string    `json:"executor_instance_id"`
	ExecutorStatus     string    `json:"executor_status"`
	ExecutorHostname   string    `json:"executor_hostname"`
	ExecutorPID        int       `json:"executor_pid"`
	LastHeartbeat      time.Time `json:"last_heartbeat"`
	ClaimedAt          time.Time `json:"claimed_at"`
	ClaimedDuration    string    `json:"claimed_duration"` // Human-readable duration
}

var staleCmd = &cobra.Command{
	Use:   "stale",
	Short: "Show orphaned claims and dead executors",
	Long: `Show issues stuck in_progress with execution_state where the executor is dead or stopped.
This helps identify orphaned work that needs manual recovery.

An issue is considered stale if:
  - It has an execution_state (claimed by an executor)
  - AND the executor status is 'stopped'
  - OR the executor's last_heartbeat is older than the threshold

Default threshold: 300 seconds (5 minutes)`,
	Run: func(cmd *cobra.Command, args []string) {
		threshold, _ := cmd.Flags().GetInt("threshold")
		release, _ := cmd.Flags().GetBool("release")

		// Get stale issues
		staleIssues, err := getStaleIssues(threshold)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		// Handle JSON output
		if jsonOutput {
			if staleIssues == nil {
				staleIssues = []*StaleIssueInfo{}
			}
			outputJSON(staleIssues)
			return
		}

		// Handle empty result
		if len(staleIssues) == 0 {
			green := color.New(color.FgGreen).SprintFunc()
			fmt.Printf("\n%s No stale issues found (all executors healthy)\n\n", green("✨"))
			return
		}

		// Display stale issues
		red := color.New(color.FgRed).SprintFunc()
		yellow := color.New(color.FgYellow).SprintFunc()
		fmt.Printf("\n%s Found %d stale issue(s) with orphaned claims:\n\n", yellow("⚠️"), len(staleIssues))

		for i, si := range staleIssues {
			fmt.Printf("%d. [P%d] %s: %s\n", i+1, si.IssuePriority, si.IssueID, si.IssueTitle)
			fmt.Printf("   Executor: %s (%s)\n", si.ExecutorInstanceID, si.ExecutorStatus)
			fmt.Printf("   Host: %s (PID: %d)\n", si.ExecutorHostname, si.ExecutorPID)
			fmt.Printf("   Last heartbeat: %s (%.0f seconds ago)\n",
				si.LastHeartbeat.Format("2006-01-02 15:04:05"),
				time.Since(si.LastHeartbeat).Seconds())
			fmt.Printf("   Claimed for: %s\n", si.ClaimedDuration)
			fmt.Println()
		}

		// Handle release flag
		if release {
			fmt.Printf("%s Releasing %d stale issue(s)...\n\n", yellow("🔧"), len(staleIssues))

			releaseCount, err := releaseStaleIssues(staleIssues)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s Failed to release issues: %v\n", red("✗"), err)
				os.Exit(1)
			}

			green := color.New(color.FgGreen).SprintFunc()
			fmt.Printf("%s Successfully released %d issue(s) and marked executors as stopped\n\n", green("✓"), releaseCount)

			// Schedule auto-flush if any issues were released
			if releaseCount > 0 {
				markDirtyAndScheduleFlush()
			}
		} else {
			cyan := color.New(color.FgCyan).SprintFunc()
			fmt.Printf("%s Use --release flag to automatically release these issues\n\n", cyan("💡"))
		}
	},
}

// getStaleIssues queries for issues with execution_state where executor is dead/stopped
func getStaleIssues(thresholdSeconds int) ([]*StaleIssueInfo, error) {
	// Ensure we have a direct store when daemon lacks stale support
	if daemonClient != nil {
		if err := ensureDirectMode("daemon does not support stale command"); err != nil {
			return nil, fmt.Errorf("failed to open database: %w", err)
		}
	} else if store == nil {
		if err := ensureStoreActive(); err != nil {
			return nil, fmt.Errorf("failed to open database: %w", err)
		}
	}

	ctx := context.Background()
	cutoffTime := time.Now().Add(-time.Duration(thresholdSeconds) * time.Second)

	// Query for stale issues
	// Use LEFT JOIN to catch orphaned execution states where executor instance is missing
	query := `
		SELECT
			i.id,
			i.title,
			i.priority,
			ies.executor_instance_id,
			COALESCE(ei.status, 'missing'),
			COALESCE(ei.hostname, 'unknown'),
			COALESCE(ei.pid, 0),
			ei.last_heartbeat,
			ies.started_at
		FROM issues i
		JOIN issue_execution_state ies ON i.id = ies.issue_id
		LEFT JOIN executor_instances ei ON ies.executor_instance_id = ei.instance_id
		WHERE ei.instance_id IS NULL
		   OR ei.status = 'stopped'
		   OR ei.last_heartbeat < ?
		ORDER BY ei.last_heartbeat ASC, i.priority ASC
	`

	// Access the underlying SQLite connection
	sqliteStore, ok := store.(*sqlite.SQLiteStorage)
	if !ok {
		return nil, fmt.Errorf("stale command requires SQLite backend")
	}

	rows, err := sqliteStore.QueryContext(ctx, query, cutoffTime)
	if err != nil {
		return nil, fmt.Errorf("failed to query stale issues: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var staleIssues []*StaleIssueInfo
	for rows.Next() {
		var si StaleIssueInfo
		var lastHeartbeat sql.NullTime
		err := rows.Scan(
			&si.IssueID,
			&si.IssueTitle,
			&si.IssuePriority,
			&si.ExecutorInstanceID,
			&si.ExecutorStatus,
			&si.ExecutorHostname,
			&si.ExecutorPID,
			&lastHeartbeat,
			&si.ClaimedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan stale issue: %w", err)
		}

		// Handle nullable last_heartbeat
		if lastHeartbeat.Valid {
			si.LastHeartbeat = lastHeartbeat.Time
		} else {
			// Use Unix epoch for missing executors
			si.LastHeartbeat = time.Unix(0, 0)
		}

		// Calculate claimed duration
		si.ClaimedDuration = formatDuration(time.Since(si.ClaimedAt))

		staleIssues = append(staleIssues, &si)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating stale issues: %w", err)
	}

	return staleIssues, nil
}

// releaseStaleIssues releases all stale issues by deleting execution state and resetting status
func releaseStaleIssues(staleIssues []*StaleIssueInfo) (int, error) {
	// Ensure we have a direct store when daemon lacks stale support
	if daemonClient != nil {
		if err := ensureDirectMode("daemon does not support stale command"); err != nil {
			return 0, fmt.Errorf("failed to open database: %w", err)
		}
	} else if store == nil {
		if err := ensureStoreActive(); err != nil {
			return 0, fmt.Errorf("failed to open database: %w", err)
		}
	}

	ctx := context.Background()

	// Access the underlying SQLite connection for transaction
	sqliteStore, ok := store.(*sqlite.SQLiteStorage)
	if !ok {
		return 0, fmt.Errorf("stale command requires SQLite backend")
	}

	// Start transaction for atomic cleanup
	tx, err := sqliteStore.BeginTx(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	releaseCount := 0
	now := time.Now()

	for _, si := range staleIssues {
		// Delete execution state
		_, err = tx.ExecContext(ctx, `
			DELETE FROM issue_execution_state
			WHERE issue_id = ?
		`, si.IssueID)
		if err != nil {
			return 0, fmt.Errorf("failed to delete execution state for issue %s: %w", si.IssueID, err)
		}

		// Reset issue status to 'open'
		_, err = tx.ExecContext(ctx, `
			UPDATE issues
			SET status = 'open', updated_at = ?
			WHERE id = ?
		`, now, si.IssueID)
		if err != nil {
			return 0, fmt.Errorf("failed to reset issue status for %s: %w", si.IssueID, err)
		}

		// Add comment explaining the release
		comment := fmt.Sprintf("Issue automatically released - executor instance %s became stale (last heartbeat: %s)",
			si.ExecutorInstanceID, si.LastHeartbeat.Format("2006-01-02 15:04:05"))
		_, err = tx.ExecContext(ctx, `
			INSERT INTO events (issue_id, event_type, actor, comment, created_at)
			VALUES (?, 'status_changed', 'system', ?, ?)
		`, si.IssueID, comment, now)
		if err != nil {
			return 0, fmt.Errorf("failed to add release comment for issue %s: %w", si.IssueID, err)
		}

		// Mark executor instance as 'stopped' if not already
		_, err = tx.ExecContext(ctx, `
			UPDATE executor_instances
			SET status = 'stopped'
			WHERE instance_id = ? AND status != 'stopped'
		`, si.ExecutorInstanceID)
		if err != nil {
			return 0, fmt.Errorf("failed to mark executor as stopped: %w", err)
		}

		releaseCount++
	}

	// Commit the transaction
	if err = tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return releaseCount, nil
}

// formatDuration formats a duration in a human-readable way
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.0f seconds", d.Seconds())
	}
	if d < time.Hour {
		return fmt.Sprintf("%.0f minutes", d.Minutes())
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%.1f hours", d.Hours())
	}
	return fmt.Sprintf("%.1f days", d.Hours()/24)
}

func init() {
	staleCmd.Flags().IntP("threshold", "t", 300, "Heartbeat threshold in seconds (default: 300 = 5 minutes)")
	staleCmd.Flags().BoolP("release", "r", false, "Automatically release all stale issues")

	rootCmd.AddCommand(staleCmd)
}
