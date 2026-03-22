package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
)

var (
	flattenDryRun bool
	flattenForce  bool
)

var flattenCmd = &cobra.Command{
	Use:     "flatten",
	GroupID: "maint",
	Short:   "Squash all Dolt history into a single commit",
	Long: `Nuclear option: squash ALL Dolt commit history into a single commit.

This uses the Tim Sehn recipe:
  1. Create a new branch from the current state
  2. Soft-reset to the initial commit (preserving all data)
  3. Commit everything as a single snapshot
  4. Swap main branch to the new flattened branch
  5. Run Dolt GC to reclaim space from old history

This is irreversible — all commit history is lost. The resulting database
has exactly one commit containing all current data.

Use this when:
  - Your .beads/dolt directory has grown very large
  - You don't need commit-level history (time travel)
  - You want to start fresh with minimal storage

Examples:
  bd flatten --dry-run               # Preview: show commit count and disk usage
  bd flatten --force                 # Actually squash all history
  bd flatten --force --json          # JSON output`,
	Run: func(_ *cobra.Command, _ []string) {
		if !flattenDryRun {
			CheckReadonly("flatten")
		}
		ctx := rootCtx
		start := time.Now()

		beadsDir := beads.FindBeadsDir()
		if beadsDir == "" {
			FatalError("could not find .beads directory")
		}

		// Detect server mode from config
		cfg, _ := configfile.Load(beadsDir)
		serverMode := cfg != nil && cfg.IsDoltServerMode()

		doltPath := filepath.Join(beadsDir, "dolt")

		// In embedded mode, validate local dolt directory and CLI
		if !serverMode {
			if _, err := os.Stat(doltPath); os.IsNotExist(err) {
				FatalError("Dolt directory not found at %s", doltPath)
			}
			if _, err := exec.LookPath("dolt"); err != nil {
				FatalErrorWithHint("dolt command not found in PATH",
					"Install Dolt from https://github.com/dolthub/dolt")
			}
		}

		// Count commits
		accessor, ok := store.(storage.RawDBAccessor)
		if !ok {
			FatalError("storage backend does not support raw DB access")
		}
		db := accessor.DB()
		var commitCount int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log").Scan(&commitCount); err != nil {
			FatalError("failed to count commits: %v", err)
		}

		// Get initial commit hash (oldest ancestor)
		var initialHash string
		if err := db.QueryRowContext(ctx,
			"SELECT commit_hash FROM dolt_log ORDER BY date ASC LIMIT 1",
		).Scan(&initialHash); err != nil {
			FatalError("failed to find initial commit: %v", err)
		}

		// Size reporting: only available in embedded mode (local dolt dir)
		var sizeBefore int64
		if !serverMode {
			sizeBefore, _ = getDirSize(doltPath)
		}

		if flattenDryRun {
			if jsonOutput {
				result := map[string]interface{}{
					"dry_run":       true,
					"commit_count":  commitCount,
					"initial_hash":  initialHash,
					"server_mode":   serverMode,
					"would_flatten": commitCount > 1,
				}
				if !serverMode {
					result["dolt_path"] = doltPath
					result["size_before"] = sizeBefore
					result["size_display"] = formatBytes(sizeBefore)
				}
				outputJSON(result)
				return
			}
			fmt.Printf("DRY RUN — Flatten preview\n\n")
			if serverMode {
				fmt.Printf("  Mode:           server\n")
			} else {
				fmt.Printf("  Dolt directory: %s\n", doltPath)
				fmt.Printf("  Current size:   %s\n", formatBytes(sizeBefore))
			}
			fmt.Printf("  Commits:        %d\n", commitCount)
			fmt.Printf("  Initial commit: %s\n", initialHash)
			if commitCount <= 1 {
				fmt.Printf("\n  Already flat (1 commit). Nothing to do.\n")
			} else {
				fmt.Printf("\n  Would squash %d commits into 1.\n", commitCount)
				fmt.Printf("  Run with --force to proceed.\n")
			}
			return
		}

		if commitCount <= 1 {
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"success":      true,
					"message":      "already flat",
					"commit_count": commitCount,
				})
				return
			}
			fmt.Println("Already flat (1 commit). Nothing to do.")
			return
		}

		if !flattenForce {
			FatalErrorWithHint(
				fmt.Sprintf("would squash %d commits into 1 (irreversible)", commitCount),
				"Use --force to confirm or --dry-run to preview.")
		}

		if !jsonOutput {
			fmt.Printf("Flattening %d commits...\n", commitCount)
		}

		if serverMode {
			flattenViaSQL(ctx, db, initialHash)
		} else {
			flattenViaCLI(doltPath, initialHash)
		}

		elapsed := time.Since(start)

		if serverMode {
			// No local size info in server mode
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"success":        true,
					"commits_before": commitCount,
					"commits_after":  1,
					"server_mode":    true,
					"elapsed_ms":     elapsed.Milliseconds(),
				})
				return
			}
			fmt.Printf("✓ Flattened %d commits → 1\n", commitCount)
			fmt.Printf("  Time: %v\n", elapsed.Round(time.Millisecond))
		} else {
			sizeAfter, _ := getDirSize(doltPath)
			freed := sizeBefore - sizeAfter
			if freed < 0 {
				freed = 0
			}
			if jsonOutput {
				outputJSON(map[string]interface{}{
					"success":        true,
					"commits_before": commitCount,
					"commits_after":  1,
					"size_before":    sizeBefore,
					"size_after":     sizeAfter,
					"freed_bytes":    freed,
					"freed_display":  formatBytes(freed),
					"elapsed_ms":     elapsed.Milliseconds(),
				})
				return
			}
			fmt.Printf("✓ Flattened %d commits → 1\n", commitCount)
			fmt.Printf("  %s → %s (freed %s)\n", formatBytes(sizeBefore), formatBytes(sizeAfter), formatBytes(freed))
			fmt.Printf("  Time: %v\n", elapsed.Round(time.Millisecond))
		}
	},
}

// flattenViaSQL performs the Tim Sehn flatten recipe using SQL stored procedures.
// Used in server mode where the dolt CLI can't reach the server's data.
func flattenViaSQL(ctx context.Context, db *sql.DB, initialHash string) {
	execSQL := func(name, query string, args ...interface{}) {
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			FatalError("flatten step '%s' failed: %v", name, err)
		}
	}

	// Tim Sehn recipe via SQL stored procedures:
	// 1. Create temp branch
	// 2. Checkout temp branch
	// 3. Soft-reset to initial commit (collapses all history into working set)
	// 4. Stage all + commit as single snapshot
	// 5. Checkout main
	// 6. Hard-reset main to the flattened branch
	// 7. Delete temp branch
	// 8. GC
	execSQL("create temp branch", "CALL DOLT_BRANCH('flatten-tmp')")
	execSQL("checkout temp branch", "CALL DOLT_CHECKOUT('flatten-tmp')")
	execSQL("soft reset to initial", "CALL DOLT_RESET('--soft', ?)", initialHash)
	execSQL("commit flattened snapshot", "CALL DOLT_COMMIT('-Am', 'flatten: squash all history into single commit')")
	execSQL("checkout main", "CALL DOLT_CHECKOUT('main')")
	execSQL("reset main to flattened", "CALL DOLT_RESET('--hard', 'flatten-tmp')")
	execSQL("delete temp branch", "CALL DOLT_BRANCH('-D', 'flatten-tmp')")
	execSQL("garbage collect", "CALL DOLT_GC()")
}

// flattenViaCLI performs the Tim Sehn flatten recipe using the dolt CLI.
// Used in embedded mode where the local .beads/dolt/ directory is the data source.
func flattenViaCLI(doltPath, initialHash string) {
	// Close the store connection before running CLI operations
	// that manipulate branches, to avoid locked database issues.
	if store != nil {
		_ = store.Close()
	}

	steps := []struct {
		name string
		args []string
	}{
		{"create temp branch", []string{"branch", "flatten-tmp"}},
		{"checkout temp branch", []string{"checkout", "flatten-tmp"}},
		{"soft reset to initial", []string{"reset", "--soft", initialHash}},
		{"stage all changes", []string{"add", "."}},
		{"commit flattened snapshot", []string{"commit", "-Am", "flatten: squash all history into single commit"}},
		{"checkout main", []string{"checkout", "main"}},
		{"reset main to flattened", []string{"reset", "--hard", "flatten-tmp"}},
		{"delete temp branch", []string{"branch", "-D", "flatten-tmp"}},
		{"garbage collect", []string{"gc"}},
	}

	for _, step := range steps {
		cmd := exec.Command("dolt", step.args...) // #nosec G204 -- fixed commands
		cmd.Dir = doltPath
		output, err := cmd.CombinedOutput()
		if err != nil {
			FatalError("flatten step '%s' failed: %v\nOutput: %s", step.name, err, string(output))
		}
	}
}

func init() {
	flattenCmd.Flags().BoolVar(&flattenDryRun, "dry-run", false, "Preview without making changes")
	flattenCmd.Flags().BoolVarP(&flattenForce, "force", "f", false, "Confirm irreversible history squash")

	rootCmd.AddCommand(flattenCmd)
}
