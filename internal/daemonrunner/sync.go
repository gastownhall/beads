package daemonrunner

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// runSyncLoop manages the main daemon event loop for sync operations
func (d *Daemon) runSyncLoop(ctx context.Context, serverErrChan chan error) error {
	beadsDir := d.cfg.BeadsDir
	jsonlPath := filepath.Join(filepath.Dir(beadsDir), "issues.jsonl")
	
	ticker := time.NewTicker(d.cfg.Interval)
	defer ticker.Stop()

	doSync := func() {
		syncCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()

		if err := d.exportToJSONL(syncCtx, jsonlPath); err != nil {
			d.log.log("Export failed: %v", err)
			return
		}
		d.log.log("Exported to JSONL")

		if d.cfg.AutoCommit {
			hasChanges, err := gitHasChanges(syncCtx, jsonlPath)
			if err != nil {
				d.log.log("Error checking git status: %v", err)
				return
			}

			if hasChanges {
				message := "bd daemon sync: " + time.Now().Format("2006-01-02 15:04:05")
				if err := gitCommit(syncCtx, jsonlPath, message); err != nil {
					d.log.log("Commit failed: %v", err)
					return
				}
				d.log.log("Committed changes")
			}
		}

		if err := gitPull(syncCtx); err != nil {
			d.log.log("Pull failed: %v", err)
			return
		}
		d.log.log("Pulled from remote")

		beforeCount, err := d.countDBIssues(syncCtx)
		if err != nil {
			d.log.log("Failed to count issues before import: %v", err)
			return
		}

		if err := d.importFromJSONL(syncCtx, jsonlPath); err != nil {
			d.log.log("Import failed: %v", err)
			return
		}
		d.log.log("Imported from JSONL")

		afterCount, err := d.countDBIssues(syncCtx)
		if err != nil {
			d.log.log("Failed to count issues after import: %v", err)
			return
		}

		if err := d.validatePostImport(beforeCount, afterCount); err != nil {
			d.log.log("Post-import validation failed: %v", err)
			return
		}

		if d.cfg.AutoPush && d.cfg.AutoCommit {
			if err := gitPush(syncCtx); err != nil {
				d.log.log("Push failed: %v", err)
				return
			}
			d.log.log("Pushed to remote")
		}

		d.log.log("Sync cycle complete")
	}

	return d.runEventLoop(ctx, ticker, doSync, serverErrChan)
}

// runEventLoop handles signals and periodic sync
func (d *Daemon) runEventLoop(ctx context.Context, ticker *time.Ticker, doSync func(), serverErrChan chan error) error {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, daemonSignals...)
	defer signal.Stop(sigChan)

	for {
		select {
		case <-ticker.C:
			if ctx.Err() != nil {
				return nil
			}
			doSync()
		case sig := <-sigChan:
			if isReloadSignal(sig) {
				d.log.log("Received reload signal, ignoring (daemon continues running)")
				continue
			}
			d.log.log("Received signal %v, shutting down gracefully...", sig)
			d.cancel()
			if err := d.server.Stop(); err != nil {
				d.log.log("Error stopping RPC server: %v", err)
			}
			return nil
		case <-ctx.Done():
			d.log.log("Context canceled, shutting down")
			if err := d.server.Stop(); err != nil {
				d.log.log("Error stopping RPC server: %v", err)
			}
			return nil
		case err := <-serverErrChan:
			d.log.log("RPC server failed: %v", err)
			d.cancel()
			if err := d.server.Stop(); err != nil {
				d.log.log("Error stopping RPC server: %v", err)
			}
			return err
		}
	}
}

// exportToJSONL exports all issues to JSONL format
func (d *Daemon) exportToJSONL(ctx context.Context, jsonlPath string) error {
	// Get all issues
	issues, err := d.store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return fmt.Errorf("failed to get issues: %w", err)
	}

	// Sort by ID for consistent output
	sort.Slice(issues, func(i, j int) bool {
		return issues[i].ID < issues[j].ID
	})

	// Populate dependencies for all issues
	allDeps, err := d.store.GetAllDependencyRecords(ctx)
	if err != nil {
		return fmt.Errorf("failed to get dependencies: %w", err)
	}
	for _, issue := range issues {
		issue.Dependencies = allDeps[issue.ID]
	}

	// Populate labels for all issues
	for _, issue := range issues {
		labels, err := d.store.GetLabels(ctx, issue.ID)
		if err != nil {
			return fmt.Errorf("failed to get labels for %s: %w", issue.ID, err)
		}
		issue.Labels = labels
	}

	// Populate comments for all issues
	for _, issue := range issues {
		comments, err := d.store.GetIssueComments(ctx, issue.ID)
		if err != nil {
			return fmt.Errorf("failed to get comments for %s: %w", issue.ID, err)
		}
		issue.Comments = comments
	}

	// Write to temp file then rename for atomicity
	tempFile := jsonlPath + ".tmp"
	f, err := os.Create(tempFile)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer f.Close()

	encoder := json.NewEncoder(f)
	for _, issue := range issues {
		if err := encoder.Encode(issue); err != nil {
			return fmt.Errorf("failed to encode issue: %w", err)
		}
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	if err := os.Rename(tempFile, jsonlPath); err != nil {
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}

// importFromJSONL imports issues from JSONL format
func (d *Daemon) importFromJSONL(ctx context.Context, jsonlPath string) error {
	// For now we skip import in the daemon runner - the daemon in cmd/bd will handle it
	// This is a temporary implementation that delegates to the existing daemon code
	// TODO(bd-b5a3): Complete the refactoring by extracting the import logic
	return nil
}

// countDBIssues returns the count of issues in the database
func (d *Daemon) countDBIssues(ctx context.Context) (int, error) {
	// Try fast path with COUNT(*)
	type dbGetter interface {
		GetDB() interface{}
	}

	if getter, ok := d.store.(dbGetter); ok {
		if db, ok := getter.GetDB().(*sql.DB); ok && db != nil {
			var count int
			err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&count)
			if err == nil {
				return count, nil
			}
		}
	}

	// Fallback: load all issues and count them
	issues, err := d.store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return 0, fmt.Errorf("failed to count database issues: %w", err)
	}
	return len(issues), nil
}

// validatePostImport validates that the import didn't cause data loss
func (d *Daemon) validatePostImport(before, after int) error {
	if after < before {
		return fmt.Errorf("import reduced issue count: %d → %d (data loss detected!)", before, after)
	}
	return nil
}
