package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
)

// pushState tracks auto-push state in a local file (.beads/push-state.json)
// instead of the Dolt metadata table, to avoid merge conflicts on multi-machine
// setups (GH#2466).
type pushState struct {
	LastPush   string `json:"last_push"`   // RFC3339 timestamp
	LastCommit string `json:"last_commit"` // Dolt commit hash
}

func pushStatePath() (string, error) {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return "", fmt.Errorf("not in a beads repository")
	}
	return filepath.Join(beadsDir, "push-state.json"), nil
}

func loadPushState() (*pushState, error) {
	path, err := pushStatePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path) //nolint:gosec // path is constructed internally
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ps pushState
	if err := json.Unmarshal(data, &ps); err != nil {
		return nil, err
	}
	return &ps, nil
}

func savePushState(ps *pushState) error {
	path, err := pushStatePath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(ps, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(path, data)
}

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

	// Load local push state (file-based, not in Dolt metadata table).
	// This avoids merge conflicts on multi-machine setups (GH#2466).
	ps, err := loadPushState()
	if err != nil {
		debug.Logf("dolt auto-push: failed to load push state: %v\n", err)
		return
	}

	// Debounce: skip if we pushed recently
	interval := config.GetDuration("dolt.auto-push-interval")
	if interval == 0 {
		interval = 5 * time.Minute
	}

	if ps != nil && ps.LastPush != "" {
		lastPush, err := time.Parse(time.RFC3339, ps.LastPush)
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
	if ps != nil && currentCommit == ps.LastCommit && ps.LastCommit != "" {
		debug.Logf("dolt auto-push: no changes since last push\n")
		return
	}

	// Push
	debug.Logf("dolt auto-push: pushing to origin...\n")
	if err := st.Push(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: dolt auto-push failed: %v\n", err)
		return
	}

	// Record last push time and commit to local file
	now := time.Now().UTC().Format(time.RFC3339)
	if err := savePushState(&pushState{LastPush: now, LastCommit: currentCommit}); err != nil {
		debug.Logf("dolt auto-push: failed to save push state: %v\n", err)
	}

	debug.Logf("dolt auto-push: pushed successfully\n")
}
