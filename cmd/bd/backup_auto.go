package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage"
)

// isBackupAutoEnabled returns whether backup should run.
// If user explicitly configured backup.enabled, use that.
// Otherwise, auto-enable when a git remote exists.
func isBackupAutoEnabled() bool {
	if config.GetValueSource("backup.enabled") != config.SourceDefault {
		return config.GetBool("backup.enabled")
	}
	return primeHasGitRemote()
}

// isBackupGitPushEnabled returns whether git commit+push should run after backup.
// Defaults to OFF — requires explicit opt-in via backup.git-push: true in config.yaml
// or BD_BACKUP_GIT_PUSH=true environment variable.
//
// Git backup accumulates commits without bound and pushes to whatever default
// remote exists, which may not be the intended target. Users who want this
// behavior must explicitly enable it.
//
// Always disabled in stealth mode (no-git-ops) — stealth means no git operations.
func isBackupGitPushEnabled() bool {
	if config.GetBool("no-git-ops") {
		return false
	}
	if config.GetValueSource("backup.git-push") != config.SourceDefault {
		return config.GetBool("backup.git-push")
	}
	return false
}

// maybeAutoBackup runs a JSONL backup if enabled and the throttle interval has passed.
// Called from PersistentPostRun after auto-commit.
func maybeAutoBackup(ctx context.Context) {
	// Skip backup entirely when running as a git hook (post-checkout, post-merge, etc.).
	// Git hooks call 'bd hooks run' which goes through PersistentPostRun — without this
	// guard, every git checkout/merge/rebase triggers a backup commit on the current branch.
	if os.Getenv("BD_GIT_HOOK") == "1" {
		debug.Logf("backup: skipping — running as git hook\n")
		return
	}

	if !isBackupAutoEnabled() {
		return
	}
	if store == nil {
		return
	}
	if lm, ok := store.(storage.LifecycleManager); ok && lm.IsClosed() {
		return
	}

	dir, err := backupDir()
	if err != nil {
		debug.Logf("backup: failed to get backup dir: %v\n", err)
		return
	}

	state, err := loadBackupState(dir)
	if err != nil {
		debug.Logf("backup: failed to load state: %v\n", err)
		return
	}

	// Throttle: skip if we backed up recently
	interval := config.GetDuration("backup.interval")
	if interval == 0 {
		interval = 15 * time.Minute
	}
	if !state.Timestamp.IsZero() && time.Since(state.Timestamp) < interval {
		debug.Logf("backup: throttled (last backup %s ago, interval %s)\n",
			time.Since(state.Timestamp).Round(time.Second), interval)
		return
	}

	// Change detection: skip if nothing changed
	currentCommit, err := store.GetCurrentCommit(ctx)
	if err != nil {
		debug.Logf("backup: failed to get current commit: %v\n", err)
		return
	}
	if currentCommit == state.LastDoltCommit && state.LastDoltCommit != "" {
		debug.Logf("backup: no changes since last backup\n")
		return
	}

	// Run the export (force=true since we already checked change detection above)
	newState, err := runBackupExport(ctx, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: auto-backup failed: %v\n", err)
		return
	}

	debug.Logf("backup: exported %d issues, %d events, %d comments\n",
		newState.Counts.Issues, newState.Counts.Events, newState.Counts.Comments)

	// Optional git push — only on default branch to avoid polluting feature branches.
	if isBackupGitPushEnabled() {
		if branch, err := currentGitBranch(); err == nil && !isDefaultBranch(branch) {
			debug.Logf("backup: skipping git commit — on branch %q (not default)\n", branch)
		} else if err := gitBackup(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: backup git push failed: %v\n", err)
		}
	}
}

// currentGitBranch returns the current git branch name.
// Returns an error if not in a git repo or HEAD is detached.
func currentGitBranch() (string, error) {
	cmd := exec.Command("git", "symbolic-ref", "--short", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// isDefaultBranch returns true if the given branch name is a default/primary branch.
func isDefaultBranch(branch string) bool {
	return branch == "main" || branch == "master"
}
