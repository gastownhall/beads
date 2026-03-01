package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
)

// isDoltAutoPushEnabled returns whether auto-push to Dolt remote should run.
// If user explicitly configured dolt.auto-push, use that.
// Otherwise, auto-enable when a Dolt remote named "origin" exists.
func isDoltAutoPushEnabled(ctx context.Context) bool {
	if config.GetValueSource("dolt.auto-push") != config.SourceDefault {
		return config.GetBool("dolt.auto-push")
	}
	// Auto-enable when a Dolt remote exists
	st := getStore()
	if st == nil || st.IsClosed() {
		return false
	}
	has, err := st.HasRemote(ctx, "origin")
	if err != nil {
		debug.Logf("dolt auto-push: failed to check remote: %v\n", err)
		return false
	}
	return has
}

// maybeAutoPush pushes to the Dolt remote if enabled and the debounce interval has passed.
// Called from PersistentPostRun after auto-commit and auto-backup.
func maybeAutoPush(ctx context.Context) {
	if isSandboxMode() {
		debug.Logf("dolt auto-push: skipped (sandbox mode)\n")
		return
	}
	if !isDoltAutoPushEnabled(ctx) {
		return
	}

	st := getStore()
	if st == nil || st.IsClosed() {
		return
	}

	// Debounce: skip if we pushed recently
	interval := config.GetDuration("dolt.auto-push-interval")
	if interval == 0 {
		interval = 5 * time.Minute
	}

	lastPushStr, err := st.GetMetadata(ctx, "dolt_auto_push_last")
	if err != nil {
		debug.Logf("dolt auto-push: failed to get last push time: %v\n", err)
		return
	}
	if lastPushStr != "" {
		lastPush, err := time.Parse(time.RFC3339, lastPushStr)
		if err == nil && time.Since(lastPush) < interval {
			debug.Logf("dolt auto-push: throttled (last push %s ago, interval %s)\n",
				time.Since(lastPush).Round(time.Second), interval)
			return
		}
	}

	// Change detection: skip if nothing changed since last push
	currentCommit, err := st.GetCurrentCommit(ctx)
	if err != nil {
		debug.Logf("dolt auto-push: failed to get current commit: %v\n", err)
		return
	}
	lastPushedCommit, _ := st.GetMetadata(ctx, "dolt_auto_push_commit")
	if currentCommit == lastPushedCommit && lastPushedCommit != "" {
		debug.Logf("dolt auto-push: no changes since last push\n")
		return
	}

	// Push
	debug.Logf("dolt auto-push: pushing to origin...\n")
	if err := st.Push(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: dolt auto-push failed: %v\n", err)
		return
	}

	// Record last push time and commit
	now := time.Now().UTC().Format(time.RFC3339)
	if err := st.SetMetadata(ctx, "dolt_auto_push_last", now); err != nil {
		debug.Logf("dolt auto-push: failed to record push time: %v\n", err)
	}
	if err := st.SetMetadata(ctx, "dolt_auto_push_commit", currentCommit); err != nil {
		debug.Logf("dolt auto-push: failed to record push commit: %v\n", err)
	}

	debug.Logf("dolt auto-push: pushed successfully\n")
}
