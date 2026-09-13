package ownershiphandoffv2

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/steveyegge/beads/internal/doltserver"
)

// rollback (R1) undoes everything bd did and leaves the workspace byte-exact as
// prepare found it. It is allowed from any uncommitted phase, including from
// itself: a rollback interrupted halfway is resumed, not restarted.
//
// Restoring the artifacts is verified HERE, once, as R1's post-condition. After
// this the caller restarts its own server, which is licensed to canonicalise
// config.yaml — so this is the last moment at which byte-exactness is a fact
// anyone can check.
func (x *run) rollback() error {
	if x.j.Phase == PhaseCommitted {
		return codedf(CodeAlreadyCommitted,
			"ownership of %s was already transferred to bd; there is nothing to roll back", x.req.Root)
	}
	if x.j.Phase == PhaseRolledBack || x.j.Phase == PhaseLegacyConfigRestored {
		return nil // already there; Result reports the journal
	}
	e := x.evidence()
	if x.j.Phase != PhaseRollbackStarted {
		x.j.Phase = PhaseRollbackStarted
		if err := x.save(); err != nil {
			return x.fail(err, &e)
		}
	}

	// Stop the replacement, by its captured identity and nothing else.
	if x.j.Reservations.TargetLaunch != "" {
		if err := x.stopTarget(&e); err != nil {
			return x.fail(err, &e)
		}
	} else {
		e.record("target_stopped", GateSkipped)
		e.note("target_stop", "no launch was ever reserved")
	}

	// Undo the dolt init, if bd is the one who made it. The snapshot's record
	// of what the data dir looked like before is the authority: a reservation
	// says bd intended to initialize, the snapshot says whether there was
	// anything there already.
	if err := x.undoDataDirInit(&e); err != nil {
		return x.fail(err, &e)
	}

	if err := restoreCommitWriteSet(x.req.Root, x.j.Snapshot); err != nil {
		e.record("artifacts_restored", GateSkipped)
		return x.fail(codedf(CodeCommitRefused, "restore workspace artifacts: %v", err), &e)
	}
	if err := assertRestored(x.req.Root, x.j.Snapshot); err != nil {
		e.record("artifacts_restored", GateSkipped)
		return x.fail(codedf(CodeCommitRefused, "workspace artifacts are not byte-exact after restore: %v", err), &e)
	}
	e.record("artifacts_restored", GatePassed)

	// The nonce-bound launch config is bd's own litter, not the caller's.
	if x.j.Target.LaunchConfig != "" {
		if err := os.Remove(x.j.Target.LaunchConfig); err != nil && !os.IsNotExist(err) {
			e.note("launch_config_remove_failed", err.Error())
		}
	}
	return x.advance(PhaseLegacyConfigRestored, e)
}

// stopTarget terminates the replacement server. An identity that no longer
// matches is a refusal, not a license to kill whatever holds the pid now: the
// whole point of capturing a birth at launch is that the number alone means
// nothing later.
func (x *run) stopTarget(e *Evidence) error {
	target := x.j.Target
	if target.PID <= 0 || target.Birth == "" {
		// The launch was reserved but never confirmed. Find the child by its
		// durable nonce instead — the same recovery configure uses.
		if err := x.retireOrphanTarget(e); err != nil {
			return err
		}
		e.record("target_stopped", GatePassed)
		e.note("target_stop", "an unconfirmed launch was retired by its nonce")
		return nil
	}
	binding, err := stopByIdentity(target.PID, target.Birth)
	e.note("target_stop_binding", binding)
	if err != nil {
		e.record("target_stopped", GateSkipped)
		return err
	}
	e.record("target_stopped", GatePassed)
	e.note("target_stop", fmt.Sprintf("stopped pid %d by its captured identity (%s)", target.PID, binding))
	x.j.Reservations.TargetLaunch = ""
	return x.save()
}

// undoDataDirInit removes exactly what `dolt init` created, and only when bd
// created it. The reservation says bd intended to; the snapshot says the
// directory was not a Dolt root beforehand. Both must agree, because a
// reservation that survived from a run against an already-initialized directory
// must never delete the caller's database.
func (x *run) undoDataDirInit(e *Evidence) error {
	if x.j.Reservations.DataDirInit == "" {
		e.record("data_dir_init_undone", GateSkipped)
		e.note("data_dir_init_undo", "bd never reserved an init")
		return nil
	}
	if x.j.Snapshot.DataDirIsDolt {
		e.record("data_dir_init_undone", GateSkipped)
		e.note("data_dir_init_undo", "the data dir was already a dolt root at prepare; nothing bd made to remove")
		x.j.Reservations.DataDirInit = ""
		return x.save()
	}
	dotDolt := filepath.Join(x.dataDir(), ".dolt")
	if err := os.RemoveAll(dotDolt); err != nil {
		e.record("data_dir_init_undone", GateSkipped)
		return codedf(CodeDataDirInvalid, "remove %s: %v", dotDolt, err)
	}
	// ensureDoltInit also drops a compatibility marker beside .dolt. It is part
	// of what the init created, so it goes with it.
	if err := os.Remove(filepath.Join(x.dataDir(), ".bd-dolt-ok")); err != nil && !os.IsNotExist(err) {
		e.note("marker_remove_failed", err.Error())
	}
	e.record("data_dir_init_undone", GatePassed)
	e.note("data_dir_init_undo", "removed the .dolt subtree bd created")
	x.j.Reservations.DataDirInit = ""
	return x.save()
}

// rollbackFinish (R2) admits the caller's server back and retires the journal.
// It verifies only what bd still owns, and it is re-runnable while it refuses —
// a caller whose server came back late clears the fence by running it again.
func (x *run) rollbackFinish() error {
	if x.j.Phase == PhaseRolledBack {
		return nil
	}
	if x.j.Phase != PhaseLegacyConfigRestored {
		return codedf(CodePhaseOrder,
			"handoff is %s; rollback-finish requires %s", x.j.Phase, PhaseLegacyConfigRestored)
	}
	e := x.evidence()
	e.PortHolder = doltserver.PortHolderSource()
	beadsDir := x.beadsDir()

	// (i) something is answering at the caller's endpoint and it serves the
	// scope prepare saw.
	db, err := openScope(x.req.Endpoint.Host, x.req.Endpoint.Port, x.req.Database)
	if err != nil {
		e.record("legacy_answers", GateSkipped)
		return x.fail(codedf(CodeLegacyNotBack, "connect to %s: %v", x.req.Endpoint, err), &e)
	}
	got, sentinelErr := readSentinels(x.ctx, db, x.req.Database)
	_ = db.Close()
	if sentinelErr != nil {
		e.record("legacy_answers", GateSkipped)
		return x.fail(codedf(CodeLegacyNotBack, "read sentinels through %s: %v", x.req.Endpoint, sentinelErr), &e)
	}
	if diff, equal := sentinelsEqual(x.j.Snapshot.Sentinels, got); !equal {
		e.record("legacy_answers", GateSkipped)
		e.note("sentinel_diff", diff)
		return x.fail(codedf(CodeLegacyNotBack, "the server at %s serves different data: %s", x.req.Endpoint, diff), &e)
	}
	e.record("legacy_answers", GatePassed)

	// (ii) the artifacts bd restored are still the ones bd restored.
	for _, item := range []struct {
		name string
		path string
		art  Artifact
	}{
		{"metadata.json", metadataPath(beadsDir), x.j.Snapshot.Metadata},
		{doltserver.PortFileName, portFilePath(beadsDir), x.j.Snapshot.PortFile},
	} {
		if err := artifactMatches(item.path, item.art); err != nil {
			e.record("artifacts_still_restored", GateSkipped)
			return x.fail(codedf(CodeLegacyNotBack, "%s drifted after rollback: %v", item.name, err), &e)
		}
	}
	e.record("artifacts_still_restored", GatePassed)

	// (iii) config.yaml still POINTS at the endpoint that answered. Not
	// byte-exactness: the caller's restart canonicalises config.yaml, which is
	// its ordinary behavior and explicitly licensed after R1.
	host, port := configuredEndpoint(beadsDir)
	if host != x.req.Endpoint.Host || port != strconv.Itoa(x.req.Endpoint.Port) {
		e.record("config_points_at_legacy", GateSkipped)
		return x.fail(codedf(CodeLegacyNotBack,
			"config.yaml resolves to %s:%s, not the endpoint that answered (%s)",
			host, port, x.req.Endpoint), &e)
	}
	e.record("config_points_at_legacy", GatePassed)

	// (iv) identity, not liveness. Something answering is not the same as a
	// stable process bound to this workspace that is neither bd's replacement
	// nor bd's own server.
	if err := x.proveLegacyIdentity(&e); err != nil {
		return x.fail(err, &e)
	}

	if err := x.advance(PhaseRolledBack, e); err != nil {
		return err
	}
	// Archiving is the last act. A journal that stays behind fences ordinary
	// opens, so this is what gives the fence its exit.
	if err := archiveRolledBackJournal(x.path, x.j); err != nil {
		return coded(CodeJournalUnreadable, err)
	}
	return nil
}

// proveLegacyIdentity is R2 (iv). It resolves the port holder itself, captures
// its birth twice at least a second apart, and requires the two to agree — a
// process that restarted between the samples is not a server that came back, it
// is a server that is still coming back.
func (x *run) proveLegacyIdentity(e *Evidence) error {
	dataDir := x.dataDir()
	if doltserver.PortHolderSource() == "" {
		e.record("legacy_identity", GateUnavailable)
		e.note("legacy_identity_reason", reasonPortHolderUndetermined)
		return nil
	}
	pid, boundBy, outcome := resolvePortHolder(x.req.Endpoint.Port, dataDir)
	switch outcome {
	case doltserver.PortHolderUndetermined:
		e.record("legacy_identity", GateUnavailable)
		e.note("legacy_identity_reason", reasonPortHolderUndetermined)
		return nil
	case doltserver.PortHolderNoHolder:
		e.record("legacy_identity", GateSkipped)
		return codedf(CodeLegacyIdentityUnproven,
			"something answered at %s but no process holding that port is bound to %s",
			x.req.Endpoint, dataDir)
	}

	first, err := captureBirth(pid)
	if err != nil {
		e.record("legacy_identity", GateUnavailable)
		e.note("legacy_identity_reason", fmt.Sprintf("capture pid %d: %v", pid, err))
		return nil
	}
	time.Sleep(identitySettleInterval)
	second, err := captureBirth(pid)
	if err != nil {
		e.record("legacy_identity", GateSkipped)
		return codedf(CodeLegacyIdentityUnproven,
			"pid %d stopped being observable between samples: %v", pid, err)
	}
	if first != second {
		e.record("legacy_identity", GateSkipped)
		return codedf(CodeLegacyIdentityUnproven,
			"pid %d changed identity between samples; the endpoint is not settled", pid)
	}

	// It must not be bd's replacement wearing the caller's port.
	if x.j.Target.PID == pid && x.j.Target.Birth == string(first) {
		e.record("legacy_identity", GateSkipped)
		return codedf(CodeLegacyIdentityUnproven,
			"the process answering at %s is bd's own replacement (pid %d); it was not stopped",
			x.req.Endpoint, pid)
	}
	// Nor bd's own managed server for this root.
	if present, detail := bdServerPresent(x.beadsDir(), x.req.Endpoint.Port); present {
		e.record("legacy_identity", GateSkipped)
		e.note("bd_server", detail)
		return codedf(CodeBDServerPresent, "%s", detail)
	}

	e.record("legacy_identity", GatePassed)
	e.note("legacy_identity_bound_by", boundBy)
	e.note("legacy_identity_pid", strconv.Itoa(pid))
	return nil
}

// identitySettleInterval is the contract's "twice, at least a second apart".
// Two captures of the same birth token that far apart rule out a process that
// is restarting under the port rather than serving on it.
const identitySettleInterval = 1100 * time.Millisecond
