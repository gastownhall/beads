package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/ownershiphandoff"
	"github.com/steveyegge/beads/internal/procid"
	storedolt "github.com/steveyegge/beads/internal/storage/dolt"
)

// directHandoffProvider composes bd's direct-local lifecycle with the
// positively identifying GC protocol. GC remains the only component that can
// stop the legacy process; bd starts and proves its replacement only after GC
// has released the endpoint.
type directHandoffProvider struct{ legacy ownershiphandoff.Provider }

// Narrow test seams for the exact target-identity boundary. They remain the
// production functions outside package tests; keeping the decision here makes
// it possible to prove that a listener is refused before EnsureRunningDetailed
// gets its normal adoption opportunity.
var (
	handoffDoltIsRunning   = doltserver.IsRunning
	handoffStrictStart     = doltserver.StartWithOptions
	handoffProcessVerify   = procid.Verify
	handoffSupportsStrict  = doltserver.SupportsStrictLaunchRecovery
	verifyRestoredSentinel = verifyHandoffSentinel
)

func (p directHandoffProvider) OwnershipHandoffHooks(ctx context.Context, request ownershiphandoff.Request) (ownershiphandoff.Hooks, error) {
	// Run skips provider resolution for an already committed journal. Every
	// resumable phase reaches this boundary, so refuse unsupported strict
	// recovery platforms before Configure or any legacy lifecycle action.
	if !handoffSupportsStrict() {
		return ownershiphandoff.Hooks{}, ownershiphandoff.CodedError{Code: "unsupported_platform", Err: errors.New("strict Dolt ownership handoff requires Linux with working pidfd signaling")}
	}
	legacy, err := p.legacy.OwnershipHandoffHooks(ctx, request)
	if err != nil {
		return ownershiphandoff.Hooks{}, err
	}
	if request.Root != request.CityRoot || request.Endpoint.Socket != "" {
		return ownershiphandoff.Hooks{}, ownershiphandoff.CodedError{Code: "unsupported_scope", Err: errors.New("ownership handoff must be run at the city root")}
	}
	if legacy.Snapshot == nil || legacy.Configure == nil || legacy.StopLegacy == nil || legacy.Verify == nil {
		return ownershiphandoff.Hooks{}, ownershiphandoff.CodedError{Code: "provider_unavailable", Err: errors.New("GC handoff provider is incomplete")}
	}
	return ownershiphandoff.Hooks{
		ConfigureMutates: true,
		Snapshot: func(snapshotCtx context.Context, r ownershiphandoff.Request) (ownershiphandoff.Snapshot, error) {
			s, err := legacy.Snapshot(snapshotCtx, r)
			if err != nil {
				return ownershiphandoff.Snapshot{}, err
			}
			beadsDir := filepath.Join(r.Root, ".beads")
			physicalBeadsDir, resolveErr := filepath.EvalSymlinks(beadsDir)
			if resolveErr != nil {
				return ownershiphandoff.Snapshot{}, ownershiphandoff.CodedError{Code: "unsupported_scope", Err: fmt.Errorf("resolve physical handoff workspace: %w", resolveErr)}
			}
			identity, identityErr := handoffGCIdentity(s.Metadata)
			if identityErr != nil {
				return ownershiphandoff.Snapshot{}, identityErr
			}
			wantDataDir := filepath.Join(physicalBeadsDir, "dolt")
			if identity.DataDir != wantDataDir || filepath.Clean(doltserver.ResolveDoltDir(beadsDir)) != wantDataDir {
				return ownershiphandoff.Snapshot{}, ownershiphandoff.CodedError{Code: "unsupported_scope", Err: errors.New("ownership handoff requires the physical .beads/dolt data directory")}
			}
			if s.WorkspaceMetadata, s.WorkspaceMetadataPresent, s.WorkspaceMetadataMode, err = readHandoffArtifact(filepath.Join(beadsDir, "metadata.json")); err != nil {
				return ownershiphandoff.Snapshot{}, err
			}
			if s.WorkspaceConfig, s.WorkspaceConfigPresent, s.WorkspaceConfigMode, err = readHandoffArtifact(filepath.Join(beadsDir, "config.yaml")); err != nil {
				return ownershiphandoff.Snapshot{}, err
			}
			if s.WorkspacePort, s.WorkspacePortPresent, s.WorkspacePortMode, err = readHandoffArtifact(filepath.Join(beadsDir, doltserver.PortFileName)); err != nil {
				return ownershiphandoff.Snapshot{}, err
			}
			if len(s.Metadata) != 0 {
				if err := captureHandoffSentinel(snapshotCtx, beadsDir, &s, r); err != nil {
					return ownershiphandoff.Snapshot{}, err
				}
			}
			return s, nil
		},
		Configure: func(configureCtx context.Context, r ownershiphandoff.Request, s ownershiphandoff.Snapshot) error {
			if err := legacy.Configure(configureCtx, r, s); err != nil {
				return err
			}
			beadsDir := filepath.Join(r.Root, ".beads")
			if err := requireConfigureHandoffArtifacts(beadsDir, s); err != nil {
				return err
			}
			// Do not alter GC's endpoint-origin controls before StopLegacy: GC
			// re-inspects those markers under its own lifecycle lock.
			return patchHandoffYAML(filepath.Join(beadsDir, "config.yaml"), map[string]any{"dolt.auto-start": false})
		},
		StopLegacy: func(stopCtx context.Context, r ownershiphandoff.Request, s ownershiphandoff.Snapshot) error {
			err := legacy.StopLegacy(stopCtx, r, s)
			if err == nil || !isHandoffProcessMissing(err) {
				return err
			}
			// A timed-out handoff-stop can have delivered its signal after the
			// caller's deadline but before GC could write the response. A retry
			// then sees process_missing. Re-prove that exact legacy absence using
			// the GC hook before advancing; this never adopts a listener and lets
			// the normal direct-target verification own the next transition.
			_, verifyErr := legacy.Verify(stopCtx, r, s, nil)
			if verifyErr != nil {
				return fmt.Errorf("verify legacy absence after process_missing stop: %w", verifyErr)
			}
			return nil
		},
		Verify: func(verifyCtx context.Context, r ownershiphandoff.Request, s ownershiphandoff.Snapshot, checkpoint func(ownershiphandoff.Snapshot) error) (ownershiphandoff.Snapshot, error) {
			identity, err := handoffGCIdentity(s.Metadata)
			if err != nil {
				return s, err
			}
			beadsDir := filepath.Join(r.Root, ".beads")
			if filepath.Clean(doltserver.ResolveDoltDir(beadsDir)) != identity.DataDir {
				return s, ownershiphandoff.CodedError{Code: "identity_changed", Err: errors.New("bd data directory differs from GC snapshot")}
			}
			var intentErr error
			s, intentErr = prepareStrictHandoffLaunch(beadsDir, s, checkpoint)
			if intentErr != nil {
				return s, intentErr
			}
			if currentHandoffOrigin(filepath.Join(beadsDir, "config.yaml")) == "city_canonical" {
				if err := requireHandoffConfig(beadsDir, s, handoffPublishedConfig(r)); err != nil {
					return s, err
				}
			} else {
				if err := requireHandoffConfig(beadsDir, s, map[string]any{"dolt.auto-start": false}); err != nil {
					return s, err
				}
				if _, err := legacy.Verify(verifyCtx, r, s, checkpoint); err != nil {
					return s, err
				}
				if err := patchHandoffYAML(filepath.Join(beadsDir, "config.yaml"), handoffPublishedConfig(r)); err != nil {
					return s, err
				}
				// EnsureRunningDetailed reads the process-wide configuration singleton.
				// Reinitialize it after the durable publication so it observes the
				// just-authorized auto-start rather than the staged false value.
				if err := config.Initialize(); err != nil {
					return s, fmt.Errorf("reload published direct handoff config: %w", err)
				}
			}
			if currentHandoffOrigin(filepath.Join(beadsDir, "config.yaml")) != "city_canonical" {
				return s, ownershiphandoff.CodedError{Code: "identity_changed", Err: errors.New("published direct handoff config is missing its canonical origin")}
			}
			// Prove the artifact is either the captured file or the exact staged
			// target port before EnsurePortFile is allowed to repair it. In
			// particular, do not replace a symlink or mode drift and thereby hide
			// the evidence that must make this transfer stop.
			if err := requireHandoffPort(beadsDir, s, r.Endpoint.Port); err != nil {
				return s, err
			}
			// Do not repair the port file before strict launch has inspected the
			// endpoint under Start's lock. In particular, a crash-window retry on
			// a platform without positive process proof must leave every artifact
			// untouched rather than converting an unproven listener into a target.
			if err := handoffTargetMayStart(r, s); err != nil {
				return s, err
			}
			state, err := doltserver.StartWithOptions(beadsDir, doltserver.StartOptions{RequireFresh: true,
				LaunchID: s.TargetLaunchID, ConfigPath: s.TargetLaunchConfig, Executable: s.TargetLaunchExecutable,
				ExpectedHost: r.Endpoint.Host, ExpectedPort: r.Endpoint.Port})
			if err != nil {
				if errors.Is(err, doltserver.ErrFreshStartRequired) {
					return s, ownershiphandoff.CodedError{Code: "identity_changed", Err: err}
				}
				return s, fmt.Errorf("start direct Dolt server: %w", err)
			}
			if state == nil || !state.Running || state.PID <= 0 {
				return s, ownershiphandoff.CodedError{Code: "verification_failed", Err: errors.New("strict direct Dolt start returned no running target")}
			}
			// A successful start is the last point at which this target can be
			// uniquely identified before another fallible operation. Checkpoint it
			// now, before configuration, port, or store probes can fail, so rollback
			// can retire exactly this process.
			var captureErr error
			s, captureErr = checkpointDirectHandoffTarget(r, identity.DataDir, state, s, checkpoint)
			if captureErr != nil {
				return s, captureErr
			}
			if err := requireHandoffConfig(beadsDir, s, handoffPublishedConfig(r)); err != nil {
				return s, err
			}
			if err := requireHandoffPort(beadsDir, s, r.Endpoint.Port); err != nil {
				return s, err
			}
			verifiedSnapshot, err := verifyDirectHandoffTarget(verifyCtx, beadsDir, r, identity.DataDir, state, s, checkpoint)
			return verifiedSnapshot, err
		},
		Commit: func(commitCtx context.Context, r ownershiphandoff.Request, s ownershiphandoff.Snapshot) error {
			identity, err := handoffGCIdentity(s.Metadata)
			if err != nil {
				return err
			}
			beadsDir := filepath.Join(r.Root, ".beads")
			if err := requireHandoffConfig(beadsDir, s, handoffPublishedConfig(r)); err != nil {
				return err
			}
			if err := requireHandoffPort(beadsDir, s, r.Endpoint.Port); err != nil {
				return err
			}
			state, stateErr := recoverStrictDirectTarget(beadsDir, r, s)
			if stateErr != nil {
				return stateErr
			}
			_, err = verifyDirectHandoffTarget(commitCtx, beadsDir, r, identity.DataDir, state, s, nil)
			return err
		},
		CommitReplay: func(commitCtx context.Context, r ownershiphandoff.Request, s ownershiphandoff.Snapshot) error {
			identity, err := handoffGCIdentity(s.Metadata)
			if err != nil {
				return err
			}
			beadsDir := filepath.Join(r.Root, ".beads")
			if err := requireHandoffConfig(beadsDir, s, handoffPublishedConfig(r)); err != nil {
				return err
			}
			if err := requireHandoffPort(beadsDir, s, r.Endpoint.Port); err != nil {
				return err
			}
			state, stateErr := recoverStrictDirectTarget(beadsDir, r, s)
			if stateErr != nil {
				return stateErr
			}
			_, err = verifyDirectHandoffTarget(commitCtx, beadsDir, r, identity.DataDir, state, s, nil)
			return err
		},
		Rollback: func(rollbackCtx context.Context, r ownershiphandoff.Request, s ownershiphandoff.Snapshot, phase ownershiphandoff.Phase, checkpoint func(ownershiphandoff.Phase, ownershiphandoff.Snapshot) error) (ownershiphandoff.Snapshot, error) {
			beadsDir := filepath.Join(r.Root, ".beads")
			// A crash after the durable restore checkpoint may have allowed GC to
			// start, but before the rolled_back journal save. Do not treat that GC
			// process as a replacement bd target or start another one: a fresh
			// inspect is the positive adoption proof for this exact phase.
			if phase == ownershiphandoff.PhaseLegacyConfigRestored {
				return recoverRestoredLegacyOwner(rollbackCtx, beadsDir, r, s, legacy)
			}
			// A target must never be silently adopted or killed during compensation.
			// The direct target has to be either absent or exactly the birth identity
			// checkpointed before its store probe.
			if err := requireRollbackHandoffArtifacts(beadsDir, r, s); err != nil {
				return s, err
			}
			if err := retireVerifiedDirectTarget(beadsDir, r, s); err != nil {
				return s, err
			}
			if err := restoreHandoffArtifacts(beadsDir, s); err != nil {
				return s, err
			}
			if err := checkpoint(ownershiphandoff.PhaseLegacyConfigRestored, s); err != nil {
				return s, err
			}
			if legacy.RestartLegacy == nil {
				return s, ownershiphandoff.CodedError{Code: "rollback_unavailable", Err: errors.New("GC handoff provider cannot restart the restored managed owner")}
			}
			if err := legacy.RestartLegacy(rollbackCtx, r, s); err != nil {
				return s, err
			}
			return verifyRestoredLegacyOwner(rollbackCtx, beadsDir, r, s, legacy)
		},
	}, nil
}

func isHandoffProcessMissing(err error) bool {
	var coded interface{ HandoffErrorCode() string }
	return errors.As(err, &coded) && coded.HandoffErrorCode() == "process_missing"
}

func verifyRestoredLegacyOwner(ctx context.Context, beadsDir string, request ownershiphandoff.Request, snapshot ownershiphandoff.Snapshot, legacy ownershiphandoff.Hooks) (ownershiphandoff.Snapshot, error) {
	if legacy.Snapshot == nil {
		return snapshot, ownershiphandoff.CodedError{Code: "rollback_unavailable", Err: errors.New("GC handoff provider cannot inspect restored owner")}
	}
	restarted, err := legacy.Snapshot(ctx, request)
	if err != nil {
		return snapshot, err
	}
	if err := requireHandoffConfig(beadsDir, snapshot, nil); err != nil {
		return snapshot, err
	}
	if err := verifyRestoredSentinel(ctx, beadsDir, snapshot, request); err != nil {
		return snapshot, err
	}
	// A restarted GC owner has a new process-birth identity. Keep the durable
	// legacy proof internally consistent so rolled-back ordinary admission
	// authenticates the fresh inspected owner rather than the stopped one.
	snapshot.Metadata, snapshot.Sentinel = restarted.Metadata, restarted.Sentinel
	return snapshot, nil
}

// recoverRestoredLegacyOwner completes the only rollback checkpoint at which
// GC may be restarted. The checkpoint is deliberately written before the
// restart call, so a crash there is recoverable: a proven running GC owner is
// adopted, a proven missing owner is restarted once, and every other identity
// failure remains a refusal.
func recoverRestoredLegacyOwner(ctx context.Context, beadsDir string, request ownershiphandoff.Request, snapshot ownershiphandoff.Snapshot, legacy ownershiphandoff.Hooks) (ownershiphandoff.Snapshot, error) {
	restored, err := verifyRestoredLegacyOwner(ctx, beadsDir, request, snapshot, legacy)
	if err == nil {
		return restored, nil
	}
	if !isHandoffProcessMissing(err) {
		return snapshot, err
	}
	if legacy.RestartLegacy == nil {
		return snapshot, ownershiphandoff.CodedError{Code: "rollback_unavailable", Err: errors.New("GC handoff provider cannot restart the restored managed owner")}
	}
	if err := legacy.RestartLegacy(ctx, request, snapshot); err != nil {
		return snapshot, err
	}
	return verifyRestoredLegacyOwner(ctx, beadsDir, request, snapshot, legacy)
}

// handoffTargetMayStart permits an uncheckpointed target only for the strict
// launcher, which holds the start lock and proves its durable launch identity.
// A checkpointed target may restart only after its prior birth identity is
// proven dead.
func handoffTargetMayStart(r ownershiphandoff.Request, snapshot ownershiphandoff.Snapshot) error {
	if snapshot.TargetPID == 0 {
		// StartWithOptions holds the start lock and can prove a crash-window
		// child from the durable nonce, config path, executable, and data root.
		// It refuses every other listener. Keeping this branch side-effect free
		// lets unsupported platforms fail closed before repair or launch.
		return nil
	}
	match, err := handoffProcessVerify(snapshot.TargetPID, procid.Token(snapshot.TargetBirth))
	if err != nil {
		return ownershiphandoff.CodedError{Code: "identity_changed", Err: fmt.Errorf("verify prior target identity: %w", err)}
	}
	if match {
		// StartWithOptions re-proves the durable nonce, exact endpoint, and
		// birth token under its lock before it may return this target.
		return nil
	}
	if err := requireHandoffEndpointReleased(r.Endpoint.Host, r.Endpoint.Port); err != nil {
		return ownershiphandoff.CodedError{Code: "identity_changed", Err: errors.New("a different direct Dolt process appeared after the checkpointed target died")}
	}
	return nil
}

type gcSnapshotIdentity struct {
	DataDir string `json:"data_dir"`
}

func handoffPublishedConfig(r ownershiphandoff.Request) map[string]any {
	return map[string]any{
		"dolt.auto-start": true, "gc.endpoint_origin": "city_canonical", "gc.endpoint_status": "verified",
		"dolt.host": r.Endpoint.Host, "dolt.port": r.Endpoint.Port,
	}
}

func handoffGCIdentity(raw []byte) (gcSnapshotIdentity, error) {
	var response struct {
		Identity gcSnapshotIdentity `json:"identity"`
	}
	if err := json.Unmarshal(raw, &response); err != nil || response.Identity.DataDir == "" || !filepath.IsAbs(response.Identity.DataDir) {
		return gcSnapshotIdentity{}, ownershiphandoff.CodedError{Code: "protocol_version", Err: errors.New("GC snapshot has no canonical data directory")}
	}
	response.Identity.DataDir = filepath.Clean(response.Identity.DataDir)
	return response.Identity, nil
}

// prepareStrictHandoffLaunch persists the unique child identity before any
// target process can be spawned. A retry can then distinguish the child that
// survived a crash before PID checkpoint from every other listener.
func prepareStrictHandoffLaunch(beadsDir string, snapshot ownershiphandoff.Snapshot, checkpoint func(ownershiphandoff.Snapshot) error) (ownershiphandoff.Snapshot, error) {
	complete := snapshot.TargetLaunchID != "" && snapshot.TargetLaunchConfig != "" && snapshot.TargetLaunchExecutable != ""
	partial := snapshot.TargetLaunchID != "" || snapshot.TargetLaunchConfig != "" || snapshot.TargetLaunchExecutable != ""
	if partial && !complete {
		return snapshot, ownershiphandoff.CodedError{Code: "identity_changed", Err: errors.New("handoff target launch intent is incomplete")}
	}
	if !complete {
		physicalBeadsDir, err := filepath.EvalSymlinks(beadsDir)
		if err != nil {
			return snapshot, ownershiphandoff.CodedError{Code: "verification_failed", Err: fmt.Errorf("resolve handoff workspace: %w", err)}
		}
		binary, err := exec.LookPath("dolt")
		if err != nil {
			return snapshot, ownershiphandoff.CodedError{Code: "endpoint_unreachable", Err: fmt.Errorf("resolve direct Dolt executable: %w", err)}
		}
		binary, err = filepath.EvalSymlinks(binary)
		if err != nil {
			return snapshot, ownershiphandoff.CodedError{Code: "verification_failed", Err: fmt.Errorf("canonicalize direct Dolt executable: %w", err)}
		}
		nonce := make([]byte, 16)
		if _, err := rand.Read(nonce); err != nil {
			return snapshot, ownershiphandoff.CodedError{Code: "verification_failed", Err: fmt.Errorf("generate handoff launch identity: %w", err)}
		}
		snapshot.TargetLaunchID = hex.EncodeToString(nonce)
		snapshot.TargetLaunchConfig = filepath.Join(physicalBeadsDir, "dolt-handoff-"+snapshot.TargetLaunchID+".yaml")
		snapshot.TargetLaunchExecutable = binary
		if checkpoint == nil {
			return snapshot, ownershiphandoff.CodedError{Code: "identity_changed", Err: errors.New("handoff launch intent has no durable checkpoint")}
		}
		if err := checkpoint(snapshot); err != nil {
			return snapshot, err
		}
	}
	return snapshot, nil
}

func verifyDirectHandoffTarget(ctx context.Context, beadsDir string, r ownershiphandoff.Request, dataDir string, state *doltserver.State, snapshot ownershiphandoff.Snapshot, checkpoint func(ownershiphandoff.Snapshot) error) (ownershiphandoff.Snapshot, error) {
	snapshot, err := checkpointDirectHandoffTarget(r, dataDir, state, snapshot, checkpoint)
	if err != nil {
		return snapshot, err
	}
	store, err := storedolt.NewFromConfigWithOptions(ctx, beadsDir, &storedolt.Config{DisableAutoStart: true, ReadOnly: true, OwnershipHandoffProbe: true})
	if err != nil {
		return snapshot, ownershiphandoff.CodedError{Code: "endpoint_unreachable", Err: fmt.Errorf("open direct Dolt store: %w", err)}
	}
	if err := store.Close(); err != nil {
		return snapshot, fmt.Errorf("close direct Dolt store: %w", err)
	}
	if err := verifyDirectHandoffSentinel(ctx, beadsDir, snapshot, r); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

// checkpointDirectHandoffTarget captures and durably checkpoints the exact
// replacement process before any database probe. A caller that cannot
// checkpoint must fail with the identity still available for rollback.
func checkpointDirectHandoffTarget(r ownershiphandoff.Request, dataDir string, strictState *doltserver.State, snapshot ownershiphandoff.Snapshot, checkpoint func(ownershiphandoff.Snapshot) error) (ownershiphandoff.Snapshot, error) {
	if snapshot.TargetPID == 0 && checkpoint == nil {
		return snapshot, ownershiphandoff.CodedError{Code: "identity_changed", Err: errors.New("direct Dolt target is missing its verified checkpoint")}
	}
	if strictState == nil || !strictState.Running || strictState.PID <= 0 || strictState.Birth == "" || strictState.Port != r.Endpoint.Port || filepath.Clean(strictState.DataDir) != dataDir {
		return snapshot, ownershiphandoff.CodedError{Code: "verification_failed", Err: errors.New("strict direct Dolt target does not match handoff endpoint")}
	}
	state := strictState
	if snapshot.TargetPID == 0 {
		match, verifyErr := handoffProcessVerify(state.PID, procid.Token(strictState.Birth))
		if verifyErr != nil || !match {
			return snapshot, ownershiphandoff.CodedError{Code: "identity_changed", Err: errors.New("strict direct Dolt process identity changed before checkpoint")}
		}
		snapshot.TargetPID, snapshot.TargetBirth, snapshot.TargetDataDir = state.PID, strictState.Birth, dataDir
		if checkpoint != nil {
			if err := checkpoint(snapshot); err != nil {
				return snapshot, err
			}
		}
	} else if snapshot.TargetPID != state.PID || snapshot.TargetDataDir != dataDir {
		if checkpoint == nil {
			return snapshot, ownershiphandoff.CodedError{Code: "identity_changed", Err: errors.New("direct Dolt process identity changed outside target-start verification")}
		}
		// A dead checkpointed target may be replaced only through the preceding
		// handoffTargetMayStart/EnsureRunningDetailed path, which refused every
		// pre-existing listener. Persist this new identity before opening the
		// store so a crash can never cause arbitrary adoption on retry.
		match, verifyErr := handoffProcessVerify(snapshot.TargetPID, procid.Token(snapshot.TargetBirth))
		if verifyErr != nil || match {
			return snapshot, ownershiphandoff.CodedError{Code: "identity_changed", Err: errors.New("direct Dolt process identity changed")}
		}
		match, verifyErr = handoffProcessVerify(state.PID, procid.Token(strictState.Birth))
		if verifyErr != nil || !match {
			return snapshot, ownershiphandoff.CodedError{Code: "identity_changed", Err: errors.New("strict replacement direct Dolt process identity changed before checkpoint")}
		}
		snapshot.TargetPID, snapshot.TargetBirth, snapshot.TargetDataDir = state.PID, strictState.Birth, dataDir
		if checkpoint != nil {
			if err := checkpoint(snapshot); err != nil {
				return snapshot, err
			}
		}
	} else if match, err := handoffProcessVerify(state.PID, procid.Token(snapshot.TargetBirth)); err != nil {
		return snapshot, ownershiphandoff.CodedError{Code: "identity_changed", Err: fmt.Errorf("verify direct Dolt process identity: %w", err)}
	} else if !match {
		return snapshot, ownershiphandoff.CodedError{Code: "identity_changed", Err: errors.New("direct Dolt process identity changed")}
	}
	return snapshot, nil
}

func captureHandoffSentinel(ctx context.Context, beadsDir string, snapshot *ownershiphandoff.Snapshot, request ownershiphandoff.Request) error {
	store, err := storedolt.NewFromConfigWithOptions(ctx, beadsDir, &storedolt.Config{DisableAutoStart: true, ReadOnly: true, OwnershipHandoffProbe: true,
		Database: request.Database, ServerHost: request.Endpoint.Host, ServerPort: request.Endpoint.Port, ServerUser: "root"})
	if err != nil {
		return fmt.Errorf("open legacy direct Dolt store: %w", err)
	}
	defer store.Close() //nolint:errcheck
	rows, err := store.QueryContext(ctx, "SELECT id FROM issues ORDER BY id LIMIT 1")
	if err != nil {
		return fmt.Errorf("read legacy issue sentinel: %w", err)
	}
	if rows.Next() {
		if err := rows.Scan(&snapshot.SentinelIssue); err != nil {
			_ = rows.Close()
			return err
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = store.QueryContext(ctx, "SELECT issue_id, COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) FROM dependencies ORDER BY issue_id LIMIT 1")
	if err != nil {
		return fmt.Errorf("read legacy dependency sentinel: %w", err)
	}
	if rows.Next() {
		if err := rows.Scan(&snapshot.SentinelEdgeFrom, &snapshot.SentinelEdgeTo); err != nil {
			_ = rows.Close()
			return err
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	return rows.Close()
}

func currentHandoffOrigin(path string) string {
	data, present, _, err := readHandoffArtifact(path)
	if err != nil || !present {
		return ""
	}
	var values map[string]any
	if yaml.Unmarshal(data, &values) != nil {
		return ""
	}
	value, _ := values["gc.endpoint_origin"].(string)
	return value
}

func readHandoffArtifact(path string) ([]byte, bool, uint32, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, 0, nil
	}
	if err != nil {
		return nil, false, 0, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, 0, fmt.Errorf("handoff artifact %s must be a regular file", path)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- Lstat above requires a regular handoff artifact
	if err != nil {
		return nil, false, 0, err
	}
	return data, true, uint32(info.Mode().Perm()), nil
}

func requireCapturedHandoffArtifacts(beadsDir string, snapshot ownershiphandoff.Snapshot) error {
	for _, artifact := range []struct {
		name    string
		data    []byte
		present bool
		mode    uint32
	}{
		{name: "metadata.json", data: snapshot.WorkspaceMetadata, present: snapshot.WorkspaceMetadataPresent, mode: snapshot.WorkspaceMetadataMode},
		{name: "config.yaml", data: snapshot.WorkspaceConfig, present: snapshot.WorkspaceConfigPresent, mode: snapshot.WorkspaceConfigMode},
		{name: doltserver.PortFileName, data: snapshot.WorkspacePort, present: snapshot.WorkspacePortPresent, mode: snapshot.WorkspacePortMode},
	} {
		if err := requireHandoffArtifact(filepath.Join(beadsDir, artifact.name), artifact.data, artifact.present, artifact.mode); err != nil {
			return err
		}
	}
	return nil
}

// requireConfigureHandoffArtifacts accepts a retry only when Configure has
// either not yet staged its one lifecycle key or has already written exactly
// that staged state before the phase checkpoint. Metadata and port artifacts
// remain byte- and mode-exact in both cases.
func requireConfigureHandoffArtifacts(beadsDir string, snapshot ownershiphandoff.Snapshot) error {
	for _, artifact := range []struct {
		name    string
		data    []byte
		present bool
		mode    uint32
	}{
		{name: "metadata.json", data: snapshot.WorkspaceMetadata, present: snapshot.WorkspaceMetadataPresent, mode: snapshot.WorkspaceMetadataMode},
		{name: doltserver.PortFileName, data: snapshot.WorkspacePort, present: snapshot.WorkspacePortPresent, mode: snapshot.WorkspacePortMode},
	} {
		if err := requireHandoffArtifact(filepath.Join(beadsDir, artifact.name), artifact.data, artifact.present, artifact.mode); err != nil {
			return err
		}
	}
	if err := requireHandoffArtifact(filepath.Join(beadsDir, "config.yaml"), snapshot.WorkspaceConfig, snapshot.WorkspaceConfigPresent, snapshot.WorkspaceConfigMode); err == nil {
		return nil
	}
	return requireHandoffConfig(beadsDir, snapshot, map[string]any{"dolt.auto-start": false})
}

// requireHandoffConfig proves that metadata and every unrelated config key
// still match Snapshot. The handoff lifecycle keys are the only permitted
// changes between the captured raw YAML and the current configuration.
func requireHandoffConfig(beadsDir string, snapshot ownershiphandoff.Snapshot, changes map[string]any) error {
	if err := requireHandoffArtifact(filepath.Join(beadsDir, "metadata.json"), snapshot.WorkspaceMetadata, snapshot.WorkspaceMetadataPresent, snapshot.WorkspaceMetadataMode); err != nil {
		return err
	}
	currentConfig, present, mode, err := readHandoffArtifact(filepath.Join(beadsDir, "config.yaml"))
	if err != nil {
		return ownershiphandoff.CodedError{Code: "identity_changed", Err: err}
	}
	if changes == nil {
		if present != snapshot.WorkspaceConfigPresent || (present && (mode != snapshot.WorkspaceConfigMode || !bytes.Equal(currentConfig, snapshot.WorkspaceConfig))) {
			return ownershiphandoff.CodedError{Code: "identity_changed", Err: errors.New("workspace config changed after handoff snapshot")}
		}
		return nil
	}
	want := map[string]any{}
	if len(snapshot.WorkspaceConfig) != 0 {
		if err := yaml.Unmarshal(snapshot.WorkspaceConfig, &want); err != nil {
			return ownershiphandoff.CodedError{Code: "identity_changed", Err: fmt.Errorf("parse captured config.yaml: %w", err)}
		}
	}
	for key, value := range changes {
		want[key] = value
	}
	got := map[string]any{}
	if !present || mode != 0o600 {
		return ownershiphandoff.CodedError{Code: "identity_changed", Err: errors.New("workspace config is not the staged regular file")}
	}
	if err := yaml.Unmarshal(currentConfig, &got); err != nil {
		return ownershiphandoff.CodedError{Code: "identity_changed", Err: fmt.Errorf("parse current config.yaml: %w", err)}
	}
	if !reflect.DeepEqual(got, want) {
		return ownershiphandoff.CodedError{Code: "identity_changed", Err: errors.New("workspace config drifted during ownership handoff")}
	}
	return nil
}

func requireHandoffArtifact(path string, want []byte, wantPresent bool, wantMode uint32) error {
	got, present, mode, err := readHandoffArtifact(path)
	if err != nil {
		return ownershiphandoff.CodedError{Code: "identity_changed", Err: err}
	}
	if present != wantPresent || (present && (mode != wantMode || !bytes.Equal(got, want))) {
		return ownershiphandoff.CodedError{Code: "identity_changed", Err: fmt.Errorf("handoff artifact %s changed after snapshot", filepath.Base(path))}
	}
	return nil
}

// requireHandoffPort permits only the exact captured port artifact or the
// regular 0600 port file that EnsurePortFile writes for this direct target.
// It rejects an absent-for-empty substitution, a mode drift, and symlinks
// before any start or rollback mutation can touch the workspace.
func requireHandoffPort(beadsDir string, snapshot ownershiphandoff.Snapshot, port int) error {
	path := filepath.Join(beadsDir, doltserver.PortFileName)
	if err := requireHandoffArtifact(path, snapshot.WorkspacePort, snapshot.WorkspacePortPresent, snapshot.WorkspacePortMode); err == nil {
		return nil
	}
	data, present, mode, err := readHandoffArtifact(path)
	if err != nil {
		return ownershiphandoff.CodedError{Code: "identity_changed", Err: err}
	}
	if !present || mode != 0o600 || string(data) != strconv.Itoa(port) {
		return ownershiphandoff.CodedError{Code: "identity_changed", Err: errors.New("workspace port artifact drifted during ownership handoff")}
	}
	return nil
}

func retireVerifiedDirectTarget(beadsDir string, request ownershiphandoff.Request, snapshot ownershiphandoff.Snapshot) error {
	if snapshot.TargetPID == 0 {
		return retireUncheckpointedStrictTarget(beadsDir, request, snapshot)
	}
	match, err := handoffProcessVerify(snapshot.TargetPID, procid.Token(snapshot.TargetBirth))
	if err != nil {
		return ownershiphandoff.CodedError{Code: "identity_changed", Err: fmt.Errorf("verify rollback target identity: %w", err)}
	}
	if !match {
		if err := requireHandoffEndpointReleased(request.Endpoint.Host, request.Endpoint.Port); err != nil {
			return ownershiphandoff.CodedError{Code: "identity_changed", Err: fmt.Errorf("cannot retire a replacement direct Dolt target: %w", err)}
		}
		return nil
	}
	// Do not call IsRunning here: its lifecycle cleanup may act on mutable PID
	// state. The process-birth token is the sole authority to signal this child.
	handle, err := procid.OpenStrict(snapshot.TargetPID, procid.Token(snapshot.TargetBirth))
	if err != nil {
		return ownershiphandoff.CodedError{Code: "identity_changed", Err: fmt.Errorf("open verified direct target for retirement: %w", err)}
	}
	defer handle.Close() //nolint:errcheck // the process handle is only a retirement guard
	if err := handle.Kill(); err != nil {
		return ownershiphandoff.CodedError{Code: "rollback_failed", Err: fmt.Errorf("retire verified direct target: %w", err)}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		match, verifyErr := handoffProcessVerify(snapshot.TargetPID, procid.Token(snapshot.TargetBirth))
		if verifyErr != nil {
			return ownershiphandoff.CodedError{Code: "rollback_failed", Err: fmt.Errorf("verify retired direct target exit: %w", verifyErr)}
		}
		if !match {
			break
		}
		if time.Now().After(deadline) {
			return ownershiphandoff.CodedError{Code: "rollback_failed", Err: errors.New("verified direct target did not exit after retirement signal")}
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err := requireHandoffEndpointReleased(request.Endpoint.Host, request.Endpoint.Port); err != nil {
		return ownershiphandoff.CodedError{Code: "rollback_failed", Err: err}
	}
	for _, path := range []string{filepath.Join(beadsDir, doltserver.PIDFileName), filepath.Join(beadsDir, doltserver.PortFileName)} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return ownershiphandoff.CodedError{Code: "rollback_failed", Err: fmt.Errorf("clear retired direct target state %s: %w", filepath.Base(path), err)}
		}
	}
	return nil
}

// retireUncheckpointedStrictTarget handles the compensating path after a
// crash-window child was launched but Verify failed before its PID checkpoint.
// It uses RecoverOnly, so it can only discover the journal's exact nonce-bound
// process and can never create a new child during rollback.
func retireUncheckpointedStrictTarget(beadsDir string, request ownershiphandoff.Request, snapshot ownershiphandoff.Snapshot) error {
	state, err := handoffStrictStart(beadsDir, doltserver.StartOptions{
		RequireFresh: true, RecoverOnly: true,
		LaunchID: snapshot.TargetLaunchID, ConfigPath: snapshot.TargetLaunchConfig, Executable: snapshot.TargetLaunchExecutable,
		ExpectedHost: request.Endpoint.Host, ExpectedPort: request.Endpoint.Port,
	})
	if err != nil {
		if errors.Is(err, doltserver.ErrStrictLaunchNotFound) {
			if releaseErr := requireHandoffEndpointReleased(request.Endpoint.Host, request.Endpoint.Port); releaseErr == nil {
				return nil
			}
		}
		return ownershiphandoff.CodedError{Code: "identity_changed", Err: fmt.Errorf("reconcile uncheckpointed strict target: %w", err)}
	}
	if state == nil || !state.Running || state.PID <= 0 || state.Port != request.Endpoint.Port || filepath.Clean(state.DataDir) != filepath.Clean(doltserver.ResolveDoltDir(beadsDir)) {
		return ownershiphandoff.CodedError{Code: "identity_changed", Err: errors.New("recovered strict target does not match rollback endpoint")}
	}
	if state.Birth == "" {
		return ownershiphandoff.CodedError{Code: "identity_changed", Err: errors.New("strict recovery returned no process-birth identity")}
	}
	snapshot.TargetPID, snapshot.TargetBirth, snapshot.TargetDataDir = state.PID, state.Birth, state.DataDir
	return retireVerifiedDirectTarget(beadsDir, request, snapshot)
}

// recoverStrictDirectTarget obtains a fresh nonce-and-birth proof for commit
// replay without opening mutable lifecycle state or launching a replacement.
func recoverStrictDirectTarget(beadsDir string, request ownershiphandoff.Request, snapshot ownershiphandoff.Snapshot) (*doltserver.State, error) {
	state, err := handoffStrictStart(beadsDir, doltserver.StartOptions{
		RequireFresh: true, RecoverOnly: true, RequireReady: true,
		LaunchID: snapshot.TargetLaunchID, ConfigPath: snapshot.TargetLaunchConfig, Executable: snapshot.TargetLaunchExecutable,
		ExpectedHost: request.Endpoint.Host, ExpectedPort: request.Endpoint.Port,
	})
	if err != nil {
		return nil, ownershiphandoff.CodedError{Code: "identity_changed", Err: fmt.Errorf("recover strict direct target: %w", err)}
	}
	if state == nil || !state.Running || state.PID != snapshot.TargetPID || state.Birth == "" || state.Port != request.Endpoint.Port || filepath.Clean(state.DataDir) != filepath.Clean(snapshot.TargetDataDir) {
		return nil, ownershiphandoff.CodedError{Code: "identity_changed", Err: errors.New("recovered strict direct target changed identity")}
	}
	return state, nil
}

// requireHandoffEndpointReleased proves no process holds the requested
// listener before rollback replaces its endpoint artifacts and restarts GC.
// A stale PID file is not evidence of endpoint release.
func requireHandoffEndpointReleased(host string, port int) error {
	if port < 1 || port > 65535 {
		return errors.New("handoff endpoint has invalid port")
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("direct target did not release requested endpoint: %w", err)
	}
	if err := listener.Close(); err != nil {
		return fmt.Errorf("close endpoint release probe: %w", err)
	}
	return nil
}

func restoreHandoffArtifacts(beadsDir string, snapshot ownershiphandoff.Snapshot) error {
	for _, artifact := range []struct {
		name    string
		data    []byte
		present bool
		mode    uint32
	}{
		{name: "metadata.json", data: snapshot.WorkspaceMetadata, present: snapshot.WorkspaceMetadataPresent, mode: snapshot.WorkspaceMetadataMode},
		{name: "config.yaml", data: snapshot.WorkspaceConfig, present: snapshot.WorkspaceConfigPresent, mode: snapshot.WorkspaceConfigMode},
		{name: doltserver.PortFileName, data: snapshot.WorkspacePort, present: snapshot.WorkspacePortPresent, mode: snapshot.WorkspacePortMode},
	} {
		path := filepath.Join(beadsDir, artifact.name)
		if !artifact.present {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return ownershiphandoff.CodedError{Code: "rollback_failed", Err: fmt.Errorf("remove restored %s: %w", artifact.name, err)}
			}
			if err := syncHandoffDirectory(beadsDir); err != nil {
				return ownershiphandoff.CodedError{Code: "rollback_failed", Err: fmt.Errorf("sync removal of restored %s: %w", artifact.name, err)}
			}
			continue
		}
		mode := os.FileMode(artifact.mode)
		if err := writeHandoffFileAtomic(path, artifact.data, mode); err != nil {
			return ownershiphandoff.CodedError{Code: "rollback_failed", Err: fmt.Errorf("restore %s: %w", artifact.name, err)}
		}
	}
	return nil
}

func requireRollbackHandoffArtifacts(beadsDir string, r ownershiphandoff.Request, snapshot ownershiphandoff.Snapshot) error {
	if err := requireHandoffConfig(beadsDir, snapshot, handoffPublishedConfig(r)); err == nil {
		if err := requireRollbackHandoffPort(beadsDir, snapshot, r.Endpoint.Port); err == nil {
			return nil
		}
	}
	if err := requireHandoffConfig(beadsDir, snapshot, map[string]any{"dolt.auto-start": false}); err == nil {
		if err := requireRollbackHandoffPort(beadsDir, snapshot, r.Endpoint.Port); err == nil {
			return nil
		}
	}
	return ownershiphandoff.CodedError{Code: "identity_changed", Err: errors.New("workspace artifacts drifted before rollback")}
}

// A failed EnsureRunningDetailed can remove the port file it repaired before
// spawning. During rollback that missing file is an expected direct-lifecycle
// cleanup outcome; the captured bytes are restored immediately afterwards.
// Any present replacement still has to be either the exact captured regular
// file or the known direct-target port file.
func requireRollbackHandoffPort(beadsDir string, snapshot ownershiphandoff.Snapshot, port int) error {
	path := filepath.Join(beadsDir, doltserver.PortFileName)
	_, present, _, err := readHandoffArtifact(path)
	if err != nil {
		return ownershiphandoff.CodedError{Code: "identity_changed", Err: err}
	}
	if !present {
		return nil
	}
	return requireHandoffPort(beadsDir, snapshot, port)
}

func verifyHandoffSentinel(ctx context.Context, beadsDir string, snapshot ownershiphandoff.Snapshot, request ownershiphandoff.Request) error {
	verified := ownershiphandoff.Snapshot{}
	if err := captureHandoffSentinel(ctx, beadsDir, &verified, request); err != nil {
		return ownershiphandoff.CodedError{Code: "rollback_failed", Err: err}
	}
	if verified.SentinelIssue != snapshot.SentinelIssue || verified.SentinelEdgeFrom != snapshot.SentinelEdgeFrom || verified.SentinelEdgeTo != snapshot.SentinelEdgeTo {
		return ownershiphandoff.CodedError{Code: "rollback_failed", Err: errors.New("restored GC target sentinel does not match snapshot")}
	}
	return nil
}

func verifyDirectHandoffSentinel(ctx context.Context, beadsDir string, snapshot ownershiphandoff.Snapshot, request ownershiphandoff.Request) error {
	verified := ownershiphandoff.Snapshot{}
	if err := captureHandoffSentinel(ctx, beadsDir, &verified, request); err != nil {
		return ownershiphandoff.CodedError{Code: "verification_failed", Err: err}
	}
	if verified.SentinelIssue != snapshot.SentinelIssue || verified.SentinelEdgeFrom != snapshot.SentinelEdgeFrom || verified.SentinelEdgeTo != snapshot.SentinelEdgeTo {
		return ownershiphandoff.CodedError{Code: "verification_failed", Err: errors.New("bd target sentinel does not match the legacy snapshot")}
	}
	return nil
}

func patchHandoffYAML(path string, changes map[string]any) error {
	data, present, _, err := readHandoffArtifact(path)
	if err != nil {
		return err
	}
	if !present {
		data = nil
	}
	values := map[string]any{}
	if len(data) != 0 {
		if err := yaml.Unmarshal(data, &values); err != nil {
			return fmt.Errorf("parse config.yaml: %w", err)
		}
	}
	for key, value := range changes {
		values[key] = value
	}
	encoded, err := yaml.Marshal(values)
	if err != nil {
		return err
	}
	return writeHandoffFileAtomic(path, encoded, 0o600)
}

func writeHandoffFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ownership-handoff-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return syncHandoffDirectory(filepath.Dir(path))
}

func syncHandoffDirectory(dir string) error {
	f, err := os.Open(dir) // #nosec G304 -- dir is filepath.Dir of the handoff config path built beneath the admitted workspace
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck
	return f.Sync()
}
