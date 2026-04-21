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
	"github.com/steveyegge/beads/internal/storage"
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
		return "", fmt.Errorf("%s", activeWorkspaceNotFoundError())
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

// Default timeouts for auto-push operations. The push timeout bounds
// the st.Push() call that shells out to git fetch, which blocks
// indefinitely when the remote is unreachable (GH#3370).
const (
	autoPushTimeout       = 30 * time.Second
	autoPushRemoteTimeout = 5 * time.Second
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
	if st == nil {
		return false
	}
	if lm, ok := storage.UnwrapStore(st).(storage.LifecycleManager); ok && lm.IsClosed() {
		return false
	}
	// Bound the remote check so a hung network doesn't block the CLI (GH#3370).
	remoteCtx, cancel := context.WithTimeout(ctx, autoPushRemoteTimeout)
	defer cancel()
	has, err := st.HasRemote(remoteCtx, "origin")
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
	if st == nil {
		return
	}
	if lm, ok := storage.UnwrapStore(st).(storage.LifecycleManager); ok && lm.IsClosed() {
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

	// Push with a bounded timeout so an unreachable remote doesn't block
	// the CLI indefinitely (GH#3370). The timeout is configurable via
	// dolt.auto-push-timeout (default 30s).
	pushTimeout := config.GetDuration("dolt.auto-push-timeout")
	if pushTimeout == 0 {
		pushTimeout = autoPushTimeout
	}
	pushCtx, pushCancel := context.WithTimeout(ctx, pushTimeout)
	defer pushCancel()

	debug.Logf("dolt auto-push: pushing to origin (timeout %s)...\n", pushTimeout)
	if err := st.Push(pushCtx); err != nil {
		if !isQuiet() && !jsonOutput {
			if pushCtx.Err() == context.DeadlineExceeded {
				fmt.Fprintf(os.Stderr, "Warning: dolt auto-push timed out after %s (remote may be unreachable)\n", pushTimeout)
			} else {
				fmt.Fprintf(os.Stderr, "Warning: dolt auto-push failed: %v\n", err)
			}
			if isDivergedHistoryErr(err) {
				printDivergedHistoryGuidance("push")
			}
		}
		debug.Logf("dolt auto-push: push error: %v\n", err)
		// Throttle retries after failure so a hanging remote doesn't make every
		// subsequent bd command pay the push timeout. We record the attempt
		// timestamp but NOT a new LastCommit, so when the remote recovers the
		// change-detection check (currentCommit != LastCommit) still triggers.
		if ps == nil {
			ps = &pushState{}
		}
		ps.LastPush = time.Now().UTC().Format(time.RFC3339)
		if saveErr := savePushState(ps); saveErr != nil {
			debug.Logf("dolt auto-push: failed to save push state after error: %v\n", saveErr)
		}
		return
	}

	// Record last push time and commit to local file
	now := time.Now().UTC().Format(time.RFC3339)
	if err := savePushState(&pushState{LastPush: now, LastCommit: currentCommit}); err != nil {
		debug.Logf("dolt auto-push: failed to save push state: %v\n", err)
	}

	debug.Logf("dolt auto-push: pushed successfully\n")
}
