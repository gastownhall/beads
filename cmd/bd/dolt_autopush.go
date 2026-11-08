package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/pushstate"
	"github.com/steveyegge/beads/internal/storage"
)

// pushState is the shared push-state schema (internal/pushstate). The
// file-based auto-push state lives in .beads/push-state.json rather than the
// Dolt metadata table to avoid merge conflicts on multi-machine setups
// (GH#2466). Since GH#6289 last_push/last_commit are success watermarks;
// failed attempts record last_attempt/last_error instead of advancing them.
type pushState = pushstate.State

type autoPushTarget interface {
	GetCurrentCommit(ctx context.Context) (string, error)
	Push(ctx context.Context) error
}

func pushStatePath() (string, error) {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return "", fmt.Errorf("%s", activeWorkspaceNotFoundError())
	}
	return filepath.Join(beadsDir, "push-state.json"), nil
}

func loadPushState() (*pushState, error) {
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return nil, fmt.Errorf("%s", activeWorkspaceNotFoundError())
	}
	return pushstate.Load(beadsDir)
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

// autoPushTimeout bounds the st.Push() call that shells out to git fetch,
// which blocks indefinitely when the remote is unreachable (GH#3370).
const autoPushTimeout = 30 * time.Second

// isDoltAutoPushEnabled returns whether auto-push to Dolt remote should run.
// Returns true only when explicitly opted in via dolt.auto-push=true in
// .beads/config.yaml (or BD_DOLT_AUTO_PUSH=true env var).
//
// Previously the default was to auto-enable when an "origin" remote exists.
// That is unsafe at any concurrency above one writer: git+ssh remotes have no
// chunk-level upload atomicity, so concurrent dolt pushes race on the remote
// manifest and can leave it referencing chunks that were never uploaded. Any
// subsequent fetch/clone/push propagates the dangling reference. Set
// dolt.auto-push=true to restore the old behavior on single-writer setups.
func isDoltAutoPushEnabled(_ context.Context) bool {
	return config.GetBool("dolt.auto-push")
}

// pushWithContext is a caller-side guard. Push implementations should honor
// ctx directly, but this keeps auto-push from blocking forever if one does not.
func pushWithContext(ctx context.Context, target autoPushTarget) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- target.Push(ctx)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// pushedCommitForState returns the commit to record as LastCommit after a
// successful auto-push. Push commits pending changes before pushing (GH#5433),
// so HEAD can move during the push call; reusing the pre-push value would leave
// LastCommit pointing at the parent of what was actually pushed, and the next
// tick would see a mismatch and push once redundantly before re-latching.
//
// A failed re-read is not a push failure -- the push already succeeded -- so
// fall back to the pre-push commit rather than leaving LastCommit unset and
// losing change detection entirely.
func pushedCommitForState(ctx context.Context, target autoPushTarget, prePush string) string {
	c, err := target.GetCurrentCommit(ctx)
	if err != nil {
		debug.Logf("dolt auto-push: failed to re-read commit after push: %v\n", err)
		return prePush
	}
	return c
}

// maybeAutoPush pushes to the Dolt remote if enabled and the debounce interval has passed.
// Called from PersistentPostRun after auto-commit and auto-backup.
// lastErrorLimit bounds the last_error line recorded in push-state.json: the
// file is meant to be read by humans and parsers, not to carry a push's whole
// stderr.
const lastErrorLimit = 512

// sanitizePushStateError renders a failed push's error as the last_error
// value push-state.json records (GH#6289): a timeout names the bound it hit,
// any other error keeps its message with embedded URL credentials scrubbed
// (the same scrubber bootstrap and sync errors go through), whitespace
// collapsed to one line, and the line truncated at lastErrorLimit bytes on a
// UTF-8 boundary so a split rune is never written.
func sanitizePushStateError(timedOut bool, pushTimeout time.Duration, err error) string {
	if timedOut {
		return fmt.Sprintf("timed out after %s", pushTimeout)
	}
	if err == nil {
		return ""
	}
	return truncateUTF8(strings.Join(strings.Fields(scrubURLCredentials(err.Error())), " "), lastErrorLimit)
}

// truncateUTF8 cuts s to at most limit bytes without splitting a UTF-8 rune.
func truncateUTF8(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	b := []byte(s[:limit])
	for len(b) > 0 {
		r, size := utf8.DecodeLastRune(b)
		if r == utf8.RuneError && size == 1 {
			b = b[:len(b)-1]
			continue
		}
		break
	}
	return string(b)
}

// maybeAutoPush pushes to the Dolt remote if enabled and the debounce interval has passed.
// Called from PersistentPostRun after auto-commit and auto-backup.
func maybeAutoPush(ctx context.Context) {
	if isSandboxMode() {
		debug.Logf("dolt auto-push: skipped (sandbox mode)\n")
		return
	}
	if isDoltLocalOnly() {
		debug.Logf("dolt auto-push: skipped (dolt.local-only=true)\n")
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
	if err := pushWithContext(pushCtx, st); err != nil {
		if !isQuiet() && !jsonOutput {
			if pushCtx.Err() == context.DeadlineExceeded {
				fmt.Fprintf(os.Stderr, "Warning: dolt auto-push timed out after %s (remote may be unreachable)\n", pushTimeout)
			} else {
				fmt.Fprintf(os.Stderr, "Warning: dolt auto-push failed: %v\n", err)
			}
			if isAncestorPKMismatchErr(err) {
				printAncestorPKMismatchGuidance(err)
			} else if isDivergedHistoryErr(err) {
				printDivergedHistoryGuidance("push")
			} else if isRemoteDanglingReferenceErr(err) {
				printRemoteDanglingReferenceGuidance(ctx, st, "")
			}
		}
		debug.Logf("dolt auto-push: push error: %v\n", err)
		// Throttle retries after failure so a hanging remote doesn't make every
		// subsequent bd command pay the push timeout. The attempt is recorded
		// as an attempt — last_attempt plus a scrubbed last_error — and the
		// success watermarks last_push/last_commit are left untouched, so the
		// file never claims a push happened that never reached the remote
		// (GH#6289) and, when the remote recovers, the change-detection check
		// (currentCommit != LastCommit) still triggers.
		if ps == nil {
			ps = &pushState{}
		}
		ps.LastAttempt = time.Now().UTC().Format(time.RFC3339)
		ps.LastError = sanitizePushStateError(pushCtx.Err() == context.DeadlineExceeded, pushTimeout, err)
		if saveErr := savePushState(ps); saveErr != nil {
			debug.Logf("dolt auto-push: failed to save push state after error: %v\n", saveErr)
		}
		return
	}

	// Record last push time and commit to local file.
	now := time.Now().UTC().Format(time.RFC3339)
	if err := savePushState(&pushState{LastPush: now, LastCommit: pushedCommitForState(ctx, st, currentCommit)}); err != nil {
		debug.Logf("dolt auto-push: failed to save push state: %v\n", err)
	}

	debug.Logf("dolt auto-push: pushed successfully\n")
}
