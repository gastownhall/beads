package proxy

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/lockfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/identity"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
)

// ForceStopOptions configures ForceStopUnverified.
type ForceStopOptions struct {
	Timeout time.Duration
}

// ForceStopReport truthfully describes which irreversible force-stop actions
// completed, including partial progress returned alongside an error. The
// top-level fields describe the proxy record; Backend, when non-nil, carries
// the same fields for the backend (proxy-child) record.
type ForceStopReport struct {
	RecordPath      string
	PID             int
	Executable      string
	RecordFound     bool
	LockWasHeld     bool
	ProcessWasGone  bool
	SignalSent      bool
	QuarantinedPath string
	Backend         *ForceStopReport
}

// ForceStopUnverified is the narrow recovery primitive used by
// `bd dolt stop --force` after an ordinary verified Shutdown refuses an
// unverifiable proxy or backend record. It applies the same procedure to the
// proxy record (proxy.pid) and the backend record (proxy-child.pid): a
// pre-v2 managed-local deployment leaves BOTH as legacy records, so covering
// only the proxy record would lock the advertised recovery out of the real
// upgrade topology.
//
// For each record: when its lock is free, the function holds it while
// inspecting, signaling, and quarantining the unchanged record. When the lock
// is held (the usual pre-upgrade-proxy case), it first inspects and signals
// the live PID, waits for the lock to become free, then quarantines only if
// the record is unchanged. Both flows accept an already-gone recorded
// process. An unverified live PID is never signaled unless its executable
// basename is exactly bd or dolt (with an optional .exe suffix) AND its
// command line scopes it to this workspace; where the platform cannot
// establish that scope, force-stop refuses rather than guessing.
func ForceStopUnverified(rootDir string, opts ...ForceStopOptions) (ForceStopReport, error) {
	report := ForceStopReport{RecordPath: pidfile.Path(rootDir, PIDFileName)}
	if len(opts) > 1 {
		return report, errors.New("proxy.ForceStopUnverified: at most one options value is allowed")
	}
	timeout := shutdownConfirmDeadline
	if len(opts) == 1 && opts[0].Timeout != 0 {
		timeout = opts[0].Timeout
	}
	if timeout <= 0 {
		return report, fmt.Errorf("proxy.ForceStopUnverified: timeout must be positive, got %s", timeout)
	}
	if err := advanceStopEpoch(rootDir); err != nil {
		return report, fmt.Errorf("proxy.ForceStopUnverified: publish stop epoch: %w", err)
	}

	proxyErr := forceStopRecord(rootDir, LockFileName, PIDFileName, pidfile.KindProxy, timeout, &report)

	backendReport := ForceStopReport{RecordPath: pidfile.Path(rootDir, server.PIDFileName)}
	backendErr := forceStopRecord(
		rootDir,
		server.LockFileName,
		server.PIDFileName,
		pidfile.KindDoltBackend,
		timeout,
		&backendReport,
	)
	if backendReport.RecordFound || backendErr != nil {
		report.Backend = &backendReport
	}
	return report, errors.Join(proxyErr, backendErr)
}

// forceStopRecord runs the inspect-signal-quarantine procedure for one
// process record, writing partial progress into report as it goes.
func forceStopRecord(
	rootDir string,
	lockName string,
	pidName string,
	wantKind string,
	timeout time.Duration,
	report *ForceStopReport,
) error {
	deadline := time.Now().Add(timeout)
	lockPath := filepath.Join(rootDir, lockName)
	lock, err := util.TryLock(lockPath)
	if err == nil {
		defer lock.Unlock()
		return forceStopRecordLocked(rootDir, pidName, wantKind, deadline, report)
	}
	if !lockfile.IsLocked(err) {
		return fmt.Errorf("proxy.ForceStopUnverified: probe %s: %w", lockPath, err)
	}
	report.LockWasHeld = true

	record, err := readForceStopRecord(rootDir, pidName, report)
	if err != nil || record == nil {
		return err
	}
	if err := requireUnverifiableRecord(rootDir, record, wantKind); err != nil {
		return err
	}
	if err := inspectAndStopUnverifiedPID(rootDir, record.Pid, deadline, report); err != nil {
		return err
	}

	lock, err = acquireForceStopLock(lockPath, deadline)
	if err != nil {
		return err
	}
	defer lock.Unlock()
	return quarantineForceStopRecord(rootDir, pidName, record, report)
}

func forceStopRecordLocked(
	rootDir string,
	pidName string,
	wantKind string,
	deadline time.Time,
	report *ForceStopReport,
) error {
	record, err := readForceStopRecord(rootDir, pidName, report)
	if err != nil || record == nil {
		return err
	}
	if err := requireUnverifiableRecord(rootDir, record, wantKind); err != nil {
		return err
	}
	if err := inspectAndStopUnverifiedPID(rootDir, record.Pid, deadline, report); err != nil {
		return err
	}
	return quarantineForceStopRecord(rootDir, pidName, record, report)
}

func readForceStopRecord(rootDir, pidName string, report *ForceStopReport) (*pidfile.PidFile, error) {
	record, err := pidfile.Read(rootDir, pidName)
	if err != nil {
		if isMalformedPIDFileError(err) {
			return nil, unverifiableProcessError(
				"force-stop",
				report.RecordPath,
				0,
				err,
				unverifiableProcessChecks{},
			)
		}
		return nil, fmt.Errorf("proxy.ForceStopUnverified: read %s: %w", report.RecordPath, err)
	}
	if record == nil {
		return nil, nil
	}
	report.RecordFound = true
	report.PID = record.Pid
	return record, nil
}

func requireUnverifiableRecord(rootDir string, record *pidfile.PidFile, wantKind string) error {
	if err := record.ValidateV2(wantKind); err != nil {
		return nil
	}
	rootID, err := identity.RootID(rootDir)
	if err != nil {
		// Failing open here would route a possibly-verifiable record into the
		// destructive force path; surface the identity failure instead.
		return fmt.Errorf("proxy.ForceStopUnverified: resolve workspace identity: %w", err)
	}
	if record.RootID == rootID {
		return errors.New(
			"proxy.ForceStopUnverified: record has a verifiable v2 workspace identity; use proxy.Shutdown",
		)
	}
	return nil
}

func inspectAndStopUnverifiedPID(rootDir string, pid int, deadline time.Time, report *ForceStopReport) error {
	if pid <= 0 {
		return fmt.Errorf("proxy.ForceStopUnverified: record %s has invalid pid %d", report.RecordPath, pid)
	}
	// One stable handle covers inspection and signaling, so the PID cannot be
	// recycled between the executable check and the kill on platforms with a
	// pinning primitive (Linux pidfd, Windows process handle).
	proc, gone, err := openUnverifiedProcess(pid)
	if err != nil {
		return fmt.Errorf("proxy.ForceStopUnverified: open pid %d: %w", pid, err)
	}
	if gone {
		report.ProcessWasGone = true
		return nil
	}
	defer proc.close()

	executable, gone, err := proc.executableBasename()
	if err != nil {
		return fmt.Errorf("proxy.ForceStopUnverified: inspect executable for pid %d: %w", pid, err)
	}
	if gone {
		report.ProcessWasGone = true
		return nil
	}
	executable = normalizeForceStopExecutable(executable)
	report.Executable = executable
	if executable != "bd" && executable != "dolt" {
		return fmt.Errorf(
			"proxy.ForceStopUnverified: refusing to signal pid %d from %s: executable basename is %q, want bd or dolt",
			pid,
			report.RecordPath,
			executable,
		)
	}

	// Basename alone would let a recycled PID now running an unrelated bd or
	// dolt be killed; require the command line to tie the process to THIS
	// workspace, and refuse when that scope cannot be established.
	scoped, gone, err := proc.commandLineContains(rootDir)
	if err != nil {
		return fmt.Errorf(
			"proxy.ForceStopUnverified: refusing to signal pid %d from %s: workspace scope could not be established (%v); stop the process manually, then quarantine the record by renaming %s to %s.stale-<unix-timestamp> before retrying",
			pid,
			report.RecordPath,
			err,
			report.RecordPath,
			report.RecordPath,
		)
	}
	if gone {
		report.ProcessWasGone = true
		return nil
	}
	if !scoped {
		return fmt.Errorf(
			"proxy.ForceStopUnverified: refusing to signal pid %d from %s: its command line does not reference workspace %s, so it may be an unrelated %s process; stop it manually if it is yours, then quarantine the record by renaming %s to %s.stale-<unix-timestamp> before retrying",
			pid,
			report.RecordPath,
			rootDir,
			executable,
			report.RecordPath,
			report.RecordPath,
		)
	}

	gone, err = proc.kill()
	if err != nil {
		return fmt.Errorf("proxy.ForceStopUnverified: signal pid %d: %w", pid, err)
	}
	if gone {
		report.ProcessWasGone = true
		return nil
	}
	report.SignalSent = true

	for {
		exited, err := proc.exited()
		if err != nil {
			return fmt.Errorf("proxy.ForceStopUnverified: confirm pid %d exit: %w", pid, err)
		}
		if exited {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("proxy.ForceStopUnverified: timeout waiting for pid %d to exit", pid)
		}
		time.Sleep(shutdownConfirmPoll)
	}
}

func normalizeForceStopExecutable(name string) string {
	name = strings.TrimSpace(filepath.Base(name))
	name = strings.TrimSuffix(name, " (deleted)")
	name = strings.TrimSuffix(strings.ToLower(name), ".exe")
	return name
}

func acquireForceStopLock(lockPath string, deadline time.Time) (*util.Lock, error) {
	for {
		lock, err := util.TryLock(lockPath)
		if err == nil {
			return lock, nil
		}
		if !lockfile.IsLocked(err) {
			return nil, fmt.Errorf("proxy.ForceStopUnverified: acquire %s: %w", lockPath, err)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("proxy.ForceStopUnverified: timeout acquiring %s after signaling", lockPath)
		}
		time.Sleep(shutdownConfirmPoll)
	}
}

func quarantineForceStopRecord(
	rootDir string,
	pidName string,
	record *pidfile.PidFile,
	report *ForceStopReport,
) error {
	current, err := pidfile.Read(rootDir, pidName)
	if err != nil {
		return fmt.Errorf("proxy.ForceStopUnverified: re-read %s: %w", report.RecordPath, err)
	}
	if current == nil {
		return nil
	}
	if *current != *record {
		return fmt.Errorf(
			"proxy.ForceStopUnverified: record %s changed after pid %d was stopped; refusing to quarantine the replacement",
			report.RecordPath,
			record.Pid,
		)
	}
	target, err := quarantineRecord(rootDir, pidName, time.Now())
	if err != nil {
		return fmt.Errorf("proxy.ForceStopUnverified: quarantine %s: %w", report.RecordPath, err)
	}
	report.QuarantinedPath = target
	return nil
}
