package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/workspacegate"
)

// Workspace operation gate wiring (see internal/workspacegate).
//
// Every store-opening command holds the workspace gate (and the physical
// database-root gate(s)) SHARED for its store/provider lifetime, so
// maintenance operations — mode migration, backup restore, bd init — can
// hold them EXCLUSIVELY and refuse to run over live bd activity instead of
// running blind. Acquisition happens once at the PersistentPreRunE
// chokepoint with the final mode preselected (there is deliberately no
// SH→EX upgrade in workspacegate), and release happens in
// PersistentPostRunE after the store closes.
//
// Posture (deliberate, from the adversarial design review):
//
//   - SHARED failures that are NOT contention (resolver error, gate file
//     unbuildable, unsupported filesystem) warn once to stderr and continue
//     UNGATED. The gate is cooperative; a normal `bd list` must never brick
//     an existing deployment because its network mount cannot flock. Note
//     the honest reading of fail-open: it means "not DETECTABLY contended",
//     not "not contended" — e.g. EACCES on another OS user's 0600 gate file
//     beside a cross-user shared root lands here and proceeds ungated even
//     though that user may be mid-maintenance (workspacegate documents
//     cross-user shared roots as unsupported).
//   - ErrBusy on SHARED means an exclusive maintenance operation is live on
//     this workspace: abort with an actionable error naming the holder
//     (the gate's busy detail carries pid/reason/host from the advisory
//     sidecar). Proceeding would race a migration/restore mid-replace.
//   - EXCLUSIVE failures of any kind are hard errors: maintenance refuses
//     rather than pretends.
//
// Known residual (PR-B2 scope, documented honestly): the pre-chokepoint
// DISCOVERY code paths (configfile.Load at main.go's early config probe and
// internal/beads.findDatabaseInBeadsDir) can perform the legacy
// config.json→metadata.json migration write BEFORE this gate is acquired —
// the chokepoint necessarily runs after workspace selection. Deferring that
// legacy write behind the gate is out of scope here.

// workspaceGateHandle is the gate set held for the current command, stored
// beside `store` because their lifetimes are paired: acquired just before
// the store-opening phase of PersistentPreRunE, released after store close
// in PersistentPostRunE. nil when the command runs ungated (skipsStoreInit
// path, fail-open posture, or no workspace on disk).
var workspaceGateHandle *workspacegate.MultiHandle

// exclusiveGateWait is how long EXCLUSIVE acquisitions poll before giving
// up: long enough to ride out a short-lived `bd list` finishing, short
// enough that a genuinely busy workspace fails with a clear message rather
// than hanging. SHARED acquisitions stay non-blocking (a live exclusive
// holder is a migration/restore — waiting seconds will not outlast it, and
// normal commands should fail fast with the holder's name). A var, not a
// const, so tests can shorten it.
var exclusiveGateWait = 5 * time.Second

// exclusiveGateOnWait reports the first contended exclusive-gate attempt.
// Kept as a variable so ordering tests can observe contention without sleeps.
var exclusiveGateOnWait = func(holder string) {
	if !quietFlag {
		// %q: the holder string comes from another process's gate sidecar;
		// quoting neutralizes terminal escape sequences a hostile or corrupt
		// sidecar could smuggle into stderr.
		fmt.Fprintf(os.Stderr, "waiting for other bd commands to finish (%q)...\n", holder) //nolint:gosec // G705: stderr, not a browser context; %q additionally neutralizes terminal escapes
	}
}

// exclusiveGateOptions builds the acquisition options for an EXCLUSIVE
// hold: bounded wait, holder-info reason, and a single stderr note when the
// first attempt comes back busy so the wait does not look like a hang.
func exclusiveGateOptions(reason string) workspacegate.Options {
	return workspacegate.Options{
		Wait:   exclusiveGateWait,
		Reason: reason,
		OnWait: exclusiveGateOnWait,
	}
}

// initGateTimeoutEnv overrides initGateWaitDefault, following the
// BEADS_*_TIMEOUT knob convention (BEADS_PRIME_TIMEOUT, BEADS_FSCK_TIMEOUT):
// a Go duration ("90s", "2m") or bare whole seconds ("90").
const initGateTimeoutEnv = "BEADS_INIT_GATE_TIMEOUT"

// initGateWaitDefault is how long bd init waits for its EXCLUSIVE gate set.
// It is deliberately longer than exclusiveGateWait and scoped to init only:
// in shared-server mode every project's physical root is the one shared
// dolt data dir, so init in project A contends with init (or any gated
// command) in project B. A single init holds the gate for ~8s, so the
// generic 5s budget made two concurrent `bd init --shared-server` runs in
// different projects refuse each other. 30s rides out a few back-to-back
// inits while still failing with a clear error on a genuinely stuck holder.
// The other exclusive callers (backup restore, bootstrap, migrate) keep
// exclusiveGateWait: they are rare, deliberate maintenance operations rather
// than routine setup that tooling fans out across many projects at once.
const initGateWaitDefault = 30 * time.Second

// initGateNoticeDelay is how long init waits silently before telling the
// user it is blocked: a sub-2s wait is not worth a line of output. A var so
// tests can observe contention without sleeping.
var initGateNoticeDelay = 2 * time.Second

// initGateOnWait prints init's single "still waiting" notice. A var so
// tests can observe it.
var initGateOnWait = func(holder string) {
	if quietFlag {
		return
	}
	where := "this workspace"
	if doltserver.IsSharedServerMode() {
		where = "the shared server"
	}
	// %q: holder text comes from another process's gate sidecar (see
	// exclusiveGateOnWait).
	fmt.Fprintf(os.Stderr, "waiting for another bd process on %s (%q)...\n", where, holder) //nolint:gosec // G705: stderr, not a browser context; %q additionally neutralizes terminal escapes
}

// initGateWait resolves init's gate budget from initGateTimeoutEnv, falling
// back to initGateWaitDefault (with a warning) on unset, malformed, or
// non-positive values.
func initGateWait() time.Duration {
	raw := strings.TrimSpace(os.Getenv(initGateTimeoutEnv))
	if raw == "" {
		return initGateWaitDefault
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		d, err = time.ParseDuration(raw + "s")
	}
	if err != nil || d <= 0 {
		if !quietFlag {
			fmt.Fprintf(os.Stderr, "warning: %s=%q is not a positive duration; using default %s\n", initGateTimeoutEnv, raw, initGateWaitDefault)
		}
		return initGateWaitDefault
	}
	return d
}

// closeStoreBeforeGateRelease enforces "gates outlive the store" on the
// error exits: PersistentPostRunE's early returns (auto-commit/auto-export
// failures) and PersistentPreRunE failures after the store opened would
// otherwise release the gates while the store is still open, letting an
// exclusive maintenance operation start against storage that has not
// quiesced. Close whatever is still open, then release. The success paths
// nil out store/uowProvider after their own close, so this is a no-op
// there.
func closeStoreBeforeGateRelease() {
	ctx := rootCtx
	if ctx == nil {
		ctx = context.Background()
	}
	if uowProvider != nil {
		_ = uowProvider.Close(ctx) // Best effort: we are on an error exit already
		uowProvider = nil
	}
	if store != nil {
		storeMutex.Lock()
		storeActive = false
		storeMutex.Unlock()
		_ = store.Close() // Best effort: we are on an error exit already
		store = nil
	}
}

// releaseWorkspaceGates drops the command's gate set. Idempotent and
// nil-safe (MultiHandle.Release is once-guarded per handle), so it is safe
// to call from every exit path — PersistentPostRunE's deferred cleanup and
// PersistentPreRunE's error paths both call it, and double release is a
// no-op.
func releaseWorkspaceGates() {
	if workspaceGateHandle != nil {
		_ = workspaceGateHandle.Release() // Best effort: the flock dies with the process anyway
		workspaceGateHandle = nil
	}
}

// commandNeedsExclusiveGate classifies the store-opening commands that
// REPLACE storage rather than use it. Currently only `bd backup restore`
// flows through the chokepoint and needs exclusivity; `bd init` and the
// `bd migrate from-*-to-*` family are in the skip-store lists and acquire
// their exclusive gates inside their own Run functions instead.
func commandNeedsExclusiveGate(cmd *cobra.Command) bool {
	return cmd.Name() == "restore" && cmd.Parent() != nil && cmd.Parent().Name() == "backup"
}

// buildWorkspaceGateSet resolves the workspace gate plus the physical-root
// gates for whatever the open path will actually open, appending any
// extraRoots (used by migrate to cover the DESTINATION mode's root as well
// as the source's). Roots whose parent directory does not exist are skipped:
// workspacegate needs the gate file's parent, and a root whose parent is
// absent cannot be holding data anyone could clobber.
//
// The resolver's PhysicalRoots provenance is deliberately NOT returned: both
// callers discard it today, and the code comments at each call site already
// explain that provenance is intentionally not surfaced in user-facing
// errors (it names which directory got gated, not who holds it — the gate's
// own busy detail already names the holder).
func buildWorkspaceGateSet(beadsDir string, extraRoots ...string) ([]workspacegate.Gate, error) {
	pr, err := doltserver.ResolvePhysicalRoots(beadsDir)
	if err != nil {
		return nil, err
	}
	wsGate, err := workspacegate.ForWorkspace(pr.BeadsDir)
	if err != nil {
		return nil, err
	}
	gates := []workspacegate.Gate{wsGate}
	roots := append(append([]string{}, pr.Roots...), extraRoots...)
	for _, root := range roots {
		if _, serr := os.Stat(filepath.Dir(root)); serr != nil {
			continue
		}
		g, gerr := workspacegate.ForPhysicalRoot(root)
		if gerr != nil {
			return nil, gerr
		}
		gates = append(gates, g)
	}
	return gates, nil
}

// acquireCommandWorkspaceGates is the chokepoint acquisition for
// store-opening commands, called from PersistentPreRunE right after the
// workspace is selected. It stores the handle in workspaceGateHandle on
// success; on the fail-open paths it returns nil with no handle.
func acquireCommandWorkspaceGates(ctx context.Context, cmd *cobra.Command, beadsDir string) error {
	exclusive := commandNeedsExclusiveGate(cmd)

	// No workspace on disk: nothing to guard, and the store-open path will
	// produce its own (better) "no database found" error. Gating here would
	// scatter .gate.lock files into arbitrary directories users run bd in.
	if _, err := os.Stat(beadsDir); err != nil {
		return nil
	}

	gates, err := buildWorkspaceGateSet(beadsDir)
	if err != nil {
		if exclusive {
			return HandleErrorRespectJSON("workspace gate: %v", err)
		}
		if !quietFlag {
			fmt.Fprintf(os.Stderr, "warning: workspace gate unavailable, continuing ungated: %v\n", err)
		}
		return nil
	}

	mode := workspacegate.Shared
	opts := workspacegate.Options{}
	if exclusive {
		mode = workspacegate.Exclusive
		opts = exclusiveGateOptions("bd backup restore")
	}
	h, err := workspacegate.AcquireAll(ctx, mode, opts, gates...)
	if err != nil {
		if errors.Is(err, workspacegate.ErrBusy) {
			// Contention is never fail-open, in either mode — but the two
			// modes are blocked by OPPOSITE kinds of holders, so the
			// message must not claim "a maintenance operation" for both:
			// a SHARED attempt is blocked only by an exclusive maintenance
			// holder, while an EXCLUSIVE attempt (backup restore) is
			// usually blocked by ordinary shared commands.
			if exclusive {
				return HandleErrorRespectJSON("other bd commands are using this workspace; wait for them to finish and retry: %v", err)
			}
			return HandleErrorRespectJSON("a maintenance operation is running on this workspace; retry when it completes: %v", err)
		}
		if exclusive {
			return HandleErrorRespectJSON("workspace gate: %v", err)
		}
		if !quietFlag {
			fmt.Fprintf(os.Stderr, "warning: workspace gate acquisition failed, continuing ungated: %v\n", err)
		}
		return nil
	}
	workspaceGateHandle = h
	return nil
}

// acquireExclusiveWorkspaceGates is the maintenance-side acquisition used by
// bd init and bd migrate, which live on the skip-store path and therefore
// never reach the chokepoint. It takes the workspace gate plus the resolved
// physical roots (when .beads exists — bd init on a fresh directory has
// nothing to resolve yet) plus any extraRoots, all EXCLUSIVE in ONE
// AcquireAll (never nested — that is a workspacegate invariant). Failures
// are returned, not softened: maintenance refuses rather than pretends.
//
// Lock ordering (normative for the callers): workspace gate(s) →
// physical-root gate(s) → migrate.lock → embedded .lock → proxy locks →
// dolt-server.lock.
//
// Re-entrancy hazard for future callers: an EXCLUSIVE holder that shells
// out to git can re-enter bd through git hooks — the bd hook wrappers spawn
// `bd export`/similar, which flows through the chokepoint, attempts a
// SHARED acquisition against our own exclusive hold, and dies with ErrBusy.
// bd init is safe today because its git plumbing runs with
// `-c core.hooksPath=` / --no-verify; any future maintenance command that
// acquires these gates and then runs git must do the same, or the hook's
// child bd will fail (fail-closed, but confusing).
func acquireExclusiveWorkspaceGates(ctx context.Context, beadsDir, reason string, extraRoots ...string) (*workspacegate.MultiHandle, error) {
	return acquireExclusiveWorkspaceGatesWithOptions(ctx, beadsDir, exclusiveGateOptions(reason), extraRoots...)
}

// acquireExclusiveWorkspaceGatesWithOptions is acquireExclusiveWorkspaceGates
// with caller-supplied acquisition options (bd init uses a longer wait).
func acquireExclusiveWorkspaceGatesWithOptions(ctx context.Context, beadsDir string, opts workspacegate.Options, extraRoots ...string) (*workspacegate.MultiHandle, error) {
	// Defense against callers that computed no workspace (bootstrap plans
	// are the untrusted case): gating "" would resolve against the CWD and
	// fence an arbitrary directory.
	if strings.TrimSpace(beadsDir) == "" {
		return nil, errors.New("workspace gate: empty beads directory")
	}
	// Normalize a nil context (tests and helpers call maintenance paths
	// directly, before the process-level signal context exists); the
	// workspacegate package also defends, but do not rely on callees.
	if ctx == nil {
		ctx = context.Background()
	}
	var gates []workspacegate.Gate
	if _, err := os.Stat(beadsDir); err == nil {
		var gerr error
		gates, gerr = buildWorkspaceGateSet(beadsDir, extraRoots...)
		if gerr != nil {
			return nil, gerr
		}
	} else {
		// .beads does not exist yet (bd init creating it): the workspace
		// gate still works because its file lives BESIDE .beads in the
		// project directory, which does exist. Physical-root gates for
		// in-.beads roots are impossible (no parent) and unnecessary —
		// exclusivity on the workspace gate already excludes every gated
		// opener. Out-of-.beads extraRoots are still gated when their
		// parent exists.
		wsGate, werr := workspacegate.ForWorkspace(beadsDir)
		if werr != nil {
			return nil, werr
		}
		gates = []workspacegate.Gate{wsGate}
		for _, root := range extraRoots {
			if _, serr := os.Stat(filepath.Dir(root)); serr != nil {
				continue
			}
			g, gerr := workspacegate.ForPhysicalRoot(root)
			if gerr != nil {
				return nil, gerr
			}
			gates = append(gates, g)
		}
	}
	return workspacegate.AcquireAll(ctx, workspacegate.Exclusive, opts, gates...)
}

// acquireInitMutationGate holds init's complete exclusive gate set while its
// destructive preflight runs. A refusal or preflight error releases the gates;
// a successful caller owns the returned handle through replacement.
//
// Contention waits up to initGateWait() (not the generic exclusiveGateWait),
// honoring ctx cancellation, and prints one notice if the wait outlasts
// initGateNoticeDelay. Exclusivity is unchanged; past the bound init fails
// with the same refusal, naming the budget and the knob that raises it.
func acquireInitMutationGate(ctx context.Context, beadsDir, physicalRoot string, preflight func() error) (*workspacegate.MultiHandle, error) {
	wait := initGateWait()
	var notice *time.Timer
	opts := workspacegate.Options{
		Wait:   wait,
		Reason: "bd init",
		// AcquireAll calls OnWait at most once, synchronously, before it
		// returns, so notice is set (if at all) by the time Stop runs.
		OnWait: func(holder string) {
			notice = time.AfterFunc(initGateNoticeDelay, func() { initGateOnWait(holder) })
		},
	}
	h, err := acquireExclusiveWorkspaceGatesWithOptions(ctx, beadsDir, opts, physicalRoot)
	if notice != nil {
		notice.Stop()
	}
	if err != nil {
		if errors.Is(err, workspacegate.ErrBusy) {
			return nil, fmt.Errorf("bd init refuses to run over live bd activity on this workspace (waited %s; set %s to wait longer): %w", wait, initGateTimeoutEnv, err)
		}
		return nil, fmt.Errorf("bd init refuses to run over live bd activity on this workspace: %w", err)
	}
	if preflight != nil {
		if err := preflight(); err != nil {
			return nil, errors.Join(err, h.Release())
		}
	}
	return h, nil
}
