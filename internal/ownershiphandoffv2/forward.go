package ownershiphandoffv2

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/procid"
)

// prepare captures everything bd will need and proves the scope is the one the
// caller named. It is the only verb that mutates nothing outside the journal,
// which is what lets a caller run it to find out whether a transfer is possible
// at all.
func (x *run) prepare() error {
	if x.j.Phase != "" {
		// Already prepared or beyond. Re-running a completed phase reports the
		// journal rather than re-observing: the snapshot is the record of what
		// the world looked like before anything moved, and re-taking it after a
		// later phase mutated something would quietly destroy the restore.
		return nil
	}
	e := x.evidence()
	beadsDir := x.beadsDir()

	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		e.record("workspace_metadata", GateSkipped)
		return x.fail(codedf(CodeDataDirInvalid, "read %s: %v", metadataPath(beadsDir), err), &e)
	}
	if backend := strings.ToLower(cfg.GetBackend()); backend != configfile.BackendDolt {
		e.record("workspace_metadata", GateSkipped)
		return x.fail(codedf(CodeUnsupportedScope, "workspace backend is %q, not %q", backend, configfile.BackendDolt), &e)
	}
	if mode := strings.ToLower(cfg.DoltMode); mode != "" && mode != configfile.DoltModeServer {
		e.record("workspace_metadata", GateSkipped)
		return x.fail(codedf(CodeUnsupportedScope,
			"workspace dolt_mode is %q; only an unset or server-mode workspace can be taken over", mode), &e)
	}
	if cfg.ProjectID != x.req.Workspace {
		e.record("workspace_metadata", GateSkipped)
		return x.fail(codedf(CodeIdentityConflict,
			"workspace metadata names project %q, not %q", cfg.ProjectID, x.req.Workspace), &e)
	}
	e.record("workspace_metadata", GatePassed)

	dataDir := x.dataDir()
	// A data dir outside the workspace is a shared or relocated server. Taking
	// it over would transfer a scope other workspaces are also using, which is
	// not what this verb means.
	if !pathUnder(dataDir, x.req.Root) || samePath(dataDir, x.req.Root) {
		e.record("data_dir_present", GateSkipped)
		return x.fail(codedf(CodeUnsupportedScope,
			"workspace data dir %s is outside %s; only a workspace-local server can be taken over",
			dataDir, x.req.Root), &e)
	}
	if _, statErr := os.Stat(dataDir); statErr != nil {
		e.record("data_dir_present", GateSkipped)
		return x.fail(codedf(CodeDataDirInvalid, "workspace data dir %s: %v", dataDir, statErr), &e)
	}
	e.record("data_dir_present", GatePassed)

	// bd must not mistake its own server for the caller's, and must not take
	// over a scope it already owns.
	if present, detail := bdServerPresent(beadsDir, x.req.Endpoint.Port); present {
		e.record("no_bd_server", GateSkipped)
		e.note("bd_server", detail)
		return x.fail(codedf(CodeBDServerPresent, "%s", detail), &e)
	}
	e.record("no_bd_server", GatePassed)

	if !handshakes(x.req.Endpoint.Host, x.req.Endpoint.Port) {
		e.record("legacy_handshake", GateSkipped)
		return x.fail(codedf(CodeLegacyUnreachable, "no MySQL handshake at %s", x.req.Endpoint), &e)
	}
	e.record("legacy_handshake", GatePassed)

	db, err := openScope(x.req.Endpoint.Host, x.req.Endpoint.Port, x.req.Database)
	if err != nil {
		e.record("legacy_serves_database", GateSkipped)
		return x.fail(codedf(CodeLegacyUnreachable, "connect to %s database %q: %v",
			x.req.Endpoint, x.req.Database, err), &e)
	}
	defer db.Close() //nolint:errcheck // read-only probe connection
	sentinels, err := readSentinels(x.ctx, db, x.req.Database)
	if err != nil {
		e.record("legacy_serves_database", GateSkipped)
		return x.fail(codedf(CodeLegacyUnreachable, "read scope sentinels through %s: %v", x.req.Endpoint, err), &e)
	}
	e.record("legacy_serves_database", GatePassed)
	e.record("sentinels_captured", GatePassed)

	// §3: bd resolves the caller's process itself. An unresolved instance is a
	// legitimate outcome — it downgrades the later disproof gate rather than
	// failing here, because a transfer whose stop bd cannot watch is still a
	// transfer whose other three gates can be evaluated.
	instance := captureInstance(x.req.Endpoint, dataDir)
	if instance.Resolved {
		e.record("legacy_instance", GatePassed)
		e.note("legacy_instance_bound_by", instance.BoundBy)
	} else {
		e.record("legacy_instance", GateUnavailable)
		e.note("legacy_instance_reason", instance.Reason)
	}
	e.PortHolder = doltserver.PortHolderSource()
	if x.opts.Hints.LegacyPID > 0 && instance.Resolved && x.opts.Hints.LegacyPID != instance.PID {
		// Informational only: bd believes what it observed. A caller whose
		// belief differs is worth recording, not worth refusing over.
		e.note("hint_pid_disagrees", fmt.Sprintf("caller said %d, bd resolved %d",
			x.opts.Hints.LegacyPID, instance.PID))
	}

	snap, err := captureSnapshot(x.req.Root, dataDir)
	if err != nil {
		e.record("snapshot", GateSkipped)
		return x.fail(coded(CodeDataDirInvalid, err), &e)
	}
	snap.Sentinels = sentinels
	e.record("snapshot", GatePassed)
	e.note("metadata_dolt_server_port", fmt.Sprintf("%d", snap.MetadataDoltServerPort))

	x.j.Owner = OwnerLegacy
	x.j.Snapshot = snap
	x.j.LegacyInstance = instance
	return x.advance(PhasePrepared, e)
}

// legacyGone proves the caller's server is not running. It mutates nothing: a
// refusal here leaves the scope exactly as the caller left it, so the caller
// can stop its server properly and run the verb again.
func (x *run) legacyGone() error {
	if x.j.Phase != PhasePrepared {
		return x.phaseGate(PhasePrepared, PhaseOldOwnerStopped)
	}
	e := x.evidence()
	e.PortHolder = doltserver.PortHolderSource()

	// (a) the endpoint is quiet, sampled rather than glanced at.
	quiet, detail := endpointQuiet(x.req.Endpoint)
	e.note("endpoint_samples", detail)
	if !quiet {
		e.record("endpoint_quiet", GateSkipped)
		return x.fail(codedf(CodeLegacyAlive, "legacy endpoint %s is still answering: %s", x.req.Endpoint, detail), &e)
	}
	e.record("endpoint_quiet", GatePassed)

	// (b) positive disproof of the instance captured at prepare.
	gone, ok, why := instanceGone(x.j.LegacyInstance)
	e.note("legacy_instance_disproof", why)
	switch {
	case !ok:
		e.record("legacy_instance_gone", GateUnavailable)
	case !gone:
		e.record("legacy_instance_gone", GateSkipped)
		return x.fail(codedf(CodeLegacyAlive, "%s", why), &e)
	default:
		e.record("legacy_instance_gone", GatePassed)
	}

	// (c) Dolt's own storage lock is released.
	locked, ok, why := dataDirLocked(x.dataDir())
	e.note("data_dir_lock", why)
	switch {
	case !ok:
		e.record("data_dir_unlocked", GateUnavailable)
	case locked:
		e.record("data_dir_unlocked", GateSkipped)
		return x.fail(codedf(CodeDataDirLocked, "%s", why), &e)
	default:
		e.record("data_dir_unlocked", GatePassed)
	}

	// (d) nothing holds the port — including a foreign listener that never
	// spoke MySQL and so passed gate (a) by saying nothing at all. The lookup
	// is three-valued: only a lookup that ran and found nothing passes this.
	pid, _, outcome := resolvePortHolder(x.req.Endpoint.Port, "")
	switch outcome {
	case doltserver.PortHolderHeld:
		e.record("port_released", GateSkipped)
		return x.fail(codedf(CodeLegacyAlive,
			"pid %d still holds port %d; the endpoint is silent but not free",
			pid, x.req.Endpoint.Port), &e)
	case doltserver.PortHolderNoHolder:
		e.record("port_released", GatePassed)
	default:
		e.record("port_released", GateUnavailable)
		e.note("port_release_reason", reasonPortHolderUndetermined)
	}

	return x.advance(PhaseOldOwnerStopped, e)
}

// configure launches bd's replacement server. This is the first verb that
// mutates anything outside the journal, so both mutations it can make are
// reserved first.
func (x *run) configure() error {
	if x.j.Phase != PhaseOldOwnerStopped {
		return x.phaseGate(PhaseOldOwnerStopped, PhaseTargetConfigured)
	}
	e := x.evidence()
	dataDir := x.dataDir()

	// Before anything is reserved or written: can this platform launch a server
	// whose identity bd will be able to prove and retire later? Only Linux can
	// (pidfd); darwin and Windows cannot bind a crash-window listener to a
	// durable launch intent at all. Discovering that after reserving the launch
	// leaves a reservation behind for a spawn that was never possible, so it is
	// checked here, where refusing costs nothing.
	//
	// This refuses rather than degrading. A replacement bd cannot prove it
	// started is one rollback cannot safely stop, and an ownership transfer
	// whose undo does not work is worse than one that never began.
	if !doltserver.SupportsStrictLaunchRecovery() {
		e.record("strict_launch_supported", GateUnavailable)
		e.note("strict_launch", "this platform cannot bind a launched server to a durable launch intent")
		return x.fail(codedf(CodeTargetLaunchFailed,
			"ownership transfer needs a launch identity this platform cannot provide (%s); "+
				"the replacement could be started but not later proven or retired", runtime.GOOS), &e)
	}
	e.record("strict_launch_supported", GatePassed)

	// A crash between reserving the launch and recording its identity leaves an
	// orphan holding the data dir. Retire it by its durable nonce before trying
	// again, or a second server would be launched over a running one.
	if x.j.Reservations.TargetLaunch == ReservationInProgress {
		if err := x.retireOrphanTarget(&e); err != nil {
			return x.fail(err, &e)
		}
	}

	if !isDoltRoot(dataDir) {
		if err := x.reserve("data_dir_init", ReservationInProgress); err != nil {
			return x.fail(err, &e)
		}
		if err := doltserver.EnsureDoltInit(dataDir); err != nil {
			return x.fail(codedf(CodeDataDirInvalid, "initialize %s: %v", dataDir, err), &e)
		}
		if err := x.reserve("data_dir_init", ReservationDone); err != nil {
			return x.fail(err, &e)
		}
		e.record("data_dir_initialized", GatePassed)
		e.note("data_dir_init", "bd created the .dolt subtree; rollback removes it")
	} else {
		e.record("data_dir_initialized", GateSkipped)
		e.note("data_dir_init", "already a dolt root; bd created nothing")
	}

	// P7 covers executables too. The strict launch re-resolves the binary with
	// EvalSymlinks and compares it to what was journaled, so journaling an
	// unresolved path makes every launch refuse "resolved executable changed"
	// wherever dolt is reached through a symlink — which is every package
	// manager that installs into a versioned directory and links it onto PATH.
	executable, err := exec.LookPath("dolt")
	if err != nil {
		return x.fail(codedf(CodeTargetLaunchFailed, "locate dolt: %v", err), &e)
	}
	if executable, err = filepath.Abs(executable); err != nil {
		return x.fail(codedf(CodeTargetLaunchFailed, "resolve dolt path: %v", err), &e)
	}
	executable = canonicalPath(executable)

	launchID, err := newLaunchID()
	if err != nil {
		return x.fail(codedf(CodeTargetLaunchFailed, "generate launch id: %v", err), &e)
	}
	host := x.req.Endpoint.Host
	port, err := freeLoopbackPort(host)
	if err != nil {
		return x.fail(codedf(CodeTargetLaunchFailed, "allocate a loopback port: %v", err), &e)
	}
	physicalBeadsDir, err := filepath.EvalSymlinks(x.beadsDir())
	if err != nil {
		return x.fail(codedf(CodeTargetLaunchFailed, "resolve %s: %v", x.beadsDir(), err), &e)
	}
	configPath := filepath.Join(physicalBeadsDir, "dolt-handoff-"+launchID+".yaml")

	// Reserve before the spawn, with everything needed to find the child again
	// if this process dies between exec and the identity being recorded.
	x.j.Target = Target{
		DataDir:      dataDir,
		LaunchID:     launchID,
		LaunchConfig: configPath,
		Executable:   executable,
		Host:         host,
		Port:         port,
	}
	if err := x.reserve("target_launch", ReservationInProgress); err != nil {
		return x.fail(err, &e)
	}

	state, err := doltserver.StartWithOptions(x.beadsDir(), doltserver.StartOptions{
		RequireFresh: true,
		LaunchID:     launchID,
		ConfigPath:   configPath,
		Executable:   executable,
		ExpectedHost: host,
		ExpectedPort: port,
		AfterSpawn:   x.opts.AfterSpawn,
	})
	if err != nil {
		return x.fail(codedf(CodeTargetLaunchFailed, "start replacement server: %v", err), &e)
	}
	// The identity comes from the launch itself, taken while Start held its
	// lifecycle lock — before anything probes the port, so no observation can
	// be of a different process than the one that was authorized.
	if state.Birth == "" || state.PID <= 0 {
		return x.fail(codedf(CodeTargetLaunchFailed,
			"replacement server started without a proven identity (pid %d)", state.PID), &e)
	}
	x.j.Target.PID = state.PID
	x.j.Target.Birth = state.Birth
	if state.Port > 0 {
		x.j.Target.Port = state.Port
	}
	e.record("target_launched", GatePassed)
	e.note("target", fmt.Sprintf("pid %d on %s:%d", state.PID, host, x.j.Target.Port))

	if err := x.reserve("target_launch", ReservationDone); err != nil {
		return x.fail(err, &e)
	}
	return x.advance(PhaseTargetConfigured, e)
}

// verify proves the replacement serves the same scope the caller's server did.
func (x *run) verify() error {
	if x.j.Phase != PhaseTargetConfigured {
		return x.phaseGate(PhaseTargetConfigured, PhaseVerified)
	}
	e := x.evidence()
	if err := x.checkTargetServesScope(&e); err != nil {
		return x.fail(err, &e)
	}
	return x.advance(PhaseVerified, e)
}

// checkTargetServesScope is verify's gate, factored out because commit re-runs
// it: between verify and commit the caller may have done anything, and commit
// is the irreversible half.
func (x *run) checkTargetServesScope(e *Evidence) error {
	if x.j.Target.PID <= 0 || x.j.Target.Birth == "" {
		return codedf(CodeTargetIdentityChanged, "journal has no target identity to verify")
	}
	match, err := verifyBirth(x.j.Target.PID, procid.Token(x.j.Target.Birth))
	if err != nil {
		if procid.IsProcessGone(err) {
			e.record("target_alive", GateSkipped)
			return codedf(CodeTargetIdentityChanged, "target pid %d is gone", x.j.Target.PID)
		}
		e.record("target_alive", GateUnavailable)
		e.note("target_alive_reason", err.Error())
	} else if !match {
		e.record("target_alive", GateSkipped)
		return codedf(CodeTargetIdentityChanged,
			"target pid %d no longer matches the identity captured at launch", x.j.Target.PID)
	} else {
		e.record("target_alive", GatePassed)
	}

	db, err := openScope(x.j.Target.Host, x.j.Target.Port, x.req.Database)
	if err != nil {
		e.record("target_serves_database", GateSkipped)
		return codedf(CodeTargetLaunchFailed, "connect to the replacement at %s:%d: %v",
			x.j.Target.Host, x.j.Target.Port, err)
	}
	defer db.Close() //nolint:errcheck // read-only probe connection
	got, err := readSentinels(x.ctx, db, x.req.Database)
	if err != nil {
		e.record("target_serves_database", GateSkipped)
		return codedf(CodeSentinelMismatch, "read sentinels through the replacement: %v", err)
	}
	e.record("target_serves_database", GatePassed)
	if diff, equal := sentinelsEqual(x.j.Snapshot.Sentinels, got); !equal {
		e.record("sentinels_match", GateSkipped)
		e.note("sentinel_diff", diff)
		return codedf(CodeSentinelMismatch, "the replacement serves different data: %s", diff)
	}
	e.record("sentinels_match", GatePassed)
	return nil
}

// commit transfers authority over the workspace's own files. It is the only
// irreversible verb, and the only one whose success is asserted with bd's own
// resolvers rather than with the writes it just made.
func (x *run) commit() error {
	if x.j.Phase == PhaseCommitted {
		return codedf(CodeAlreadyCommitted, "ownership of %s already transferred to bd", x.req.Root)
	}
	if x.j.Phase != PhaseVerified {
		return x.phaseGate(PhaseVerified, PhaseCommitted)
	}
	e := x.evidence()
	if err := x.checkTargetServesScope(&e); err != nil {
		return x.fail(err, &e)
	}

	if err := x.reserve("commit_write_set", ReservationInProgress); err != nil {
		return x.fail(err, &e)
	}
	if err := applyCommitWriteSet(x.req.Root, x.j.Target); err != nil {
		// The write set is half-applied at worst. Put it back before reporting,
		// so the caller's scope is left exactly as prepare found it.
		restoreErr := restoreCommitWriteSet(x.req.Root, x.j.Snapshot)
		x.j.Reservations.CommitWriteSet = ""
		e.record("write_set_applied", GateSkipped)
		if restoreErr != nil {
			e.note("restore_failed", restoreErr.Error())
		}
		return x.fail(codedf(CodeCommitRefused, "apply the commit write set: %v", err), &e)
	}
	e.record("write_set_applied", GatePassed)

	// The post-write assertion. bd asks its own resolvers whether the workspace
	// now reads as bd-owned — the same functions ordinary commands use, so a
	// write set that satisfies this cannot leave bd disagreeing with itself at
	// the next command.
	if reason := x.assertOwnedAfterWrite(); reason != "" {
		if restoreErr := restoreCommitWriteSet(x.req.Root, x.j.Snapshot); restoreErr != nil {
			e.note("restore_failed", restoreErr.Error())
		}
		x.j.Reservations.CommitWriteSet = ""
		e.record("resolvers_agree", GateSkipped)
		e.note("commit_refused_reason", reason)
		return x.fail(codedf(CodeCommitRefused, "%s", reason), &e)
	}
	e.record("resolvers_agree", GatePassed)

	if err := x.reserve("commit_write_set", ReservationDone); err != nil {
		return x.fail(err, &e)
	}
	x.j.Owner = OwnerBD
	return x.advance(PhaseCommitted, e)
}

// assertOwnedAfterWrite returns "" when every one of bd's own resolvers agrees
// the scope is now bd's, or the first disagreement otherwise.
func (x *run) assertOwnedAfterWrite() string {
	beadsDir := x.beadsDir()
	if mode := doltserver.ResolveServerMode(beadsDir); mode != doltserver.ServerModeOwned {
		return fmt.Sprintf("ResolveServerMode says %s, not owned", mode)
	}
	if !doltserver.ManagesLiveServerOnPort(beadsDir, x.j.Target.Port) {
		return fmt.Sprintf("bd does not resolve as managing a live server on port %d", x.j.Target.Port)
	}
	if !doltserver.ResolveAutoStartForDir(beadsDir) {
		return "auto-start does not resolve true for this workspace"
	}
	// The fence is asked about the journal commit is about to write, not the
	// one on disk: on disk it still says verified, which the fence correctly
	// refuses. "Would CheckNormalOpen admit?" is a question about the outcome.
	prospective := x.j
	prospective.Phase = PhaseCommitted
	prospective.Owner = OwnerBD
	if err := admits(x.req.Root, prospective); err != nil {
		return fmt.Sprintf("the handoff fence would not admit an ordinary open: %v", err)
	}
	return ""
}

// retireOrphanTarget finds and stops a replacement server this process started
// and then lost track of. It is identity-bound: only the child matching the
// durable nonce is a candidate, and only its captured birth authorizes a
// signal.
func (x *run) retireOrphanTarget(e *Evidence) error {
	if !doltserver.SupportsStrictLaunchRecovery() {
		e.record("orphan_retired", GateUnavailable)
		e.note("orphan_recovery", "this platform cannot bind a crash-window listener to its launch intent")
		return codedf(CodeTargetLaunchFailed,
			"a previous launch was interrupted and this platform cannot prove which process it started; roll back instead")
	}
	prior := x.j.Target
	if prior.LaunchID == "" || prior.LaunchConfig == "" || prior.Executable == "" {
		e.record("orphan_retired", GateSkipped)
		e.note("orphan_recovery", "no launch identity was reserved; nothing to retire")
		x.j.Reservations.TargetLaunch = ""
		return x.save()
	}
	state, err := doltserver.StartWithOptions(x.beadsDir(), doltserver.StartOptions{
		RequireFresh: true,
		RecoverOnly:  true,
		LaunchID:     prior.LaunchID,
		ConfigPath:   prior.LaunchConfig,
		Executable:   prior.Executable,
		ExpectedHost: prior.Host,
		ExpectedPort: prior.Port,
	})
	if errors.Is(err, doltserver.ErrStrictLaunchNotFound) {
		e.record("orphan_retired", GatePassed)
		e.note("orphan_recovery", "the interrupted launch left no child")
		x.j.Reservations.TargetLaunch = ""
		x.j.Target = Target{}
		_ = os.Remove(prior.LaunchConfig)
		return x.save()
	}
	if err != nil {
		e.record("orphan_retired", GateSkipped)
		return codedf(CodeTargetLaunchFailed, "look for the interrupted launch: %v", err)
	}
	binding, err := stopByIdentity(state.PID, state.Birth)
	e.note("target_stop_binding", binding)
	if err != nil {
		e.record("orphan_retired", GateSkipped)
		return err
	}
	e.record("orphan_retired", GatePassed)
	e.note("orphan_recovery", fmt.Sprintf("stopped orphan pid %d from launch %s", state.PID, prior.LaunchID))
	x.j.Reservations.TargetLaunch = ""
	x.j.Target = Target{}
	_ = os.Remove(prior.LaunchConfig)
	return x.save()
}

// phaseGate turns a verb asked for out of order into the right answer: a no-op
// when the journal is already past it, a typed refusal when it is not yet
// there.
func (x *run) phaseGate(need, produces Phase) error {
	if IsRollbackPhase(x.j.Phase) {
		return codedf(CodePhaseOrder,
			"this handoff is rolling back (%s); it cannot go forward to %s", x.j.Phase, produces)
	}
	if forwardRank[x.j.Phase] >= forwardRank[produces] {
		return nil // already done; Result reports the journal
	}
	return codedf(CodePhaseOrder, "handoff is %s; %s requires %s", x.j.Phase, produces, need)
}

// admits is CheckNormalOpen's decision, taken on a journal value rather than on
// the file. CheckNormalOpen loads and calls this; commit calls it with the
// journal it is about to write.
func admits(root string, j Journal) error {
	switch j.Phase {
	case PhaseCommitted:
		return nil
	case PhaseRolledBack:
		return assertRestored(root, j.Snapshot)
	default:
		return codedf(CodePhaseOrder, "ownership handoff is %s", j.Phase)
	}
}

func newLaunchID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

// stopByIdentity terminates a process only when it can be shown to still be the
// one that was captured. Nothing here signals a bare pid: that is the pid-reuse
// race this whole mechanism exists to avoid.
//
// The binding is not equally strong everywhere, and the caller journals which
// one was used. Where the kernel offers a handle bound to the process itself
// (pidfd on Linux), OpenStrict holds it across the signal and reuse is
// impossible. Elsewhere — darwin, Windows — procid.Open verifies the birth
// token and then signals, which leaves a narrow window in which the process
// could exit and its pid be reused. That is strictly stronger than not checking
// at all, and it is the most those platforms offer; refusing outright would
// leave a rollback with no way to stop the server it just started.
func stopByIdentity(pid int, birth string) (binding string, err error) {
	if pid <= 0 || birth == "" {
		return "", codedf(CodeTargetIdentityChanged, "no captured identity for pid %d", pid)
	}
	open, binding := procid.OpenStrict, "kernel-bound"
	if !procid.SupportsKernelBoundHandle() {
		open, binding = procid.Open, "verify-then-signal"
	}
	handle, err := open(pid, procid.Token(birth))
	if err != nil {
		if procid.IsProcessGone(err) {
			return binding, nil // already gone; stopping it is what was wanted
		}
		return binding, codedf(CodeTargetIdentityChanged,
			"open pid %d by its captured identity: %v", pid, err)
	}
	defer handle.Close() //nolint:errcheck // best effort on a handle being discarded
	if err := handle.Kill(); err != nil {
		return binding, codedf(CodeTargetIdentityChanged, "stop pid %d: %v", pid, err)
	}
	// Wait for it to actually go. A signal delivered is not a process exited,
	// and the caller restarts its own server into this data dir the moment
	// rollback returns — into Dolt's storage lock, if this returns early.
	if !waitForExit(pid, procid.Token(birth), targetExitTimeout) {
		return binding, codedf(CodeTargetIdentityChanged,
			"pid %d did not exit within %s of being stopped", pid, targetExitTimeout)
	}
	return binding, nil
}

// targetExitTimeout bounds the wait for a stopped replacement to leave. A Dolt
// server flushes on shutdown, so this is generous.
const targetExitTimeout = 30 * time.Second

// waitForExit polls until the captured identity is no longer running.
func waitForExit(pid int, birth procid.Token, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		match, err := verifyBirth(pid, birth)
		if err != nil || !match {
			// Gone, recycled into something else, or unobservable. None of
			// those is the process this handoff launched still serving.
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// configuredEndpoint reads the workspace config.yaml's resolved dolt endpoint.
// Used by rollback-finish to check that config.yaml points at whatever answered
// — not that it is byte-identical, because the caller's restart is licensed to
// canonicalise it.
func configuredEndpoint(beadsDir string) (string, string) {
	return config.GetStringFromDir(beadsDir, "dolt.host"),
		config.GetStringFromDir(beadsDir, "dolt.port")
}
