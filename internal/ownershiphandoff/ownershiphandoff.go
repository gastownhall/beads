// Package ownershiphandoff implements the explicit, journaled handoff from a
// legacy GC-owned direct-local Dolt server to bd. It deliberately has no
// process-management policy: callers must provide a positively identifying
// legacy-owner stop hook.
package ownershiphandoff

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/lockfile"
)

// Owner identifies the lifecycle authority for a Dolt scope.
type Owner string

const (
	OwnerLegacyGC Owner = "legacy-gc"
	OwnerBD       Owner = "bd"
)

// Phase is a durable checkpoint in the handoff journal.
type Phase string

const (
	PhasePrepared         Phase = "prepared"
	PhaseTargetConfigured Phase = "target_configured"
	PhaseOldOwnerStopped  Phase = "old_owner_stopped"
	PhaseVerified         Phase = "verified"
	PhaseCommitted        Phase = "committed"
)

// Endpoint identifies the local server endpoint. Exactly one of Port or
// Socket may be set. Host must be loopback; ownership handoff never adopts a
// remotely managed server.
type Endpoint struct {
	Host   string `json:"host,omitempty"`
	Port   int    `json:"port,omitempty"`
	Socket string `json:"socket,omitempty"`
}

// String returns a stable display form for the endpoint.
func (e Endpoint) String() string {
	if e.Socket != "" {
		return "unix://" + e.Socket
	}
	return fmt.Sprintf("%s:%d", e.Host, e.Port)
}

// Request identifies the exact scope being handed off.
type Request struct {
	Root      string   `json:"root"`
	Database  string   `json:"database"`
	Workspace string   `json:"workspace"`
	Endpoint  Endpoint `json:"endpoint"`
	Owner     Owner    `json:"owner"`
}

// Snapshot contains provider metadata captured before mutation.
type Snapshot struct {
	Metadata []byte `json:"metadata,omitempty"`
	Config   []byte `json:"config,omitempty"`
	Sentinel string `json:"sentinel,omitempty"`
}

// Journal is the atomically persisted handoff state.
type Journal struct {
	Request          Request  `json:"request"`
	Snapshot         Snapshot `json:"snapshot,omitempty"`
	SnapshotCaptured bool     `json:"snapshot_captured,omitempty"`
	// LegacyStopInProgress records that StopLegacy was invoked but has not yet
	// reached its own checkpoint. It makes a partial or interrupted stop a
	// durable mutation instead of leaving the journal claiming the legacy owner
	// was never touched.
	LegacyStopInProgress bool      `json:"legacy_stop_in_progress,omitempty"`
	CommitHookInProgress bool      `json:"commit_hook_in_progress,omitempty"`
	CommitHookRan        bool      `json:"commit_hook_ran,omitempty"`
	Phase                Phase     `json:"phase"`
	Owner                Owner     `json:"owner"`
	ErrorCode            string    `json:"error_code,omitempty"`
	Error                string    `json:"error,omitempty"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// Result is the typed front-door outcome shared by text and JSON callers.
type Result struct {
	Phase    Phase    `json:"phase"`
	Owner    Owner    `json:"owner"`
	Root     string   `json:"root"`
	Database string   `json:"database"`
	Endpoint Endpoint `json:"endpoint"`
	// Mutates reports whether handoff execution has touched provider-visible
	// state, cumulatively across resumed attempts: a refusal or failure inherits
	// the mutation an earlier attempt made durable. No-op outcomes report false
	// even against a journal whose phase proves prior mutation — a dry run and a
	// committed replay both change nothing themselves — so read Phase, not
	// Mutates, for how far the handoff has progressed.
	Mutates   bool   `json:"mutates"`
	ErrorCode string `json:"error_code,omitempty"`
}

// Hooks are provider-owned operations.
//
// Retry contract: Execute resumes from the last durable journal phase, so a
// crash between a hook returning and its checkpoint reaching disk replays that
// hook. Snapshot, Configure, StopLegacy, and Verify must therefore be
// idempotent under re-invocation, and an effect that is already applied is
// success rather than a conflict. Commit is the sole exception: it is bracketed
// by durable checkpoints so it is invoked at most once, and an ambiguous
// outcome is recovered only through CommitReplay.
type Hooks struct {
	// Snapshot captures provider metadata before any mutation. It runs at most
	// once per journal because the captured snapshot is made durable before
	// Configure.
	Snapshot func(context.Context, Request) (Snapshot, error)
	// Configure prepares the bd-owned target. It must not mutate the
	// legacy-owned scope: until Commit, legacy-gc stays authoritative and every
	// failure path must be able to leave the scope untouched. Re-running it
	// against an already-configured target must succeed.
	Configure func(context.Context, Request, Snapshot) error
	// StopLegacy stops the legacy owner. It must refuse unless it can
	// positively identify the process as the legacy owner; it must never kill an
	// unknown process. An already-absent legacy owner is success: the refusal
	// duty is about never killing an unidentified *live* process, so a retry
	// after a stop that was durable but uncheckpointed must not wedge the
	// handoff.
	StopLegacy func(context.Context, Request, Snapshot) error
	// Verify confirms the bd-owned target serves the scope. It is read-only and
	// re-runnable.
	Verify func(context.Context, Request, Snapshot) error
	// Commit retires the legacy owner's artifacts and transfers authority.
	Commit func(context.Context, Request, Snapshot) error
	// CommitReplay is an optional idempotent recovery operation for a commit
	// attempt whose outcome is unknown: a process crash after the hook ran but
	// before its completion checkpoint was durable, or any error returned by
	// Commit, which may have applied part of its effect before failing. Without
	// this explicit provider guarantee, Execute refuses to guess or invoke
	// Commit a second time.
	CommitReplay func(context.Context, Request, Snapshot) error
}

// ValidateRequest rejects incomplete, non-canonical, or remotely managed identities.
func ValidateRequest(r Request) error {
	if !filepath.IsAbs(r.Root) || filepath.Clean(r.Root) != r.Root {
		return errors.New("root must be an absolute canonical path")
	}
	if r.Owner != OwnerLegacyGC {
		return errors.New("owner must be legacy-gc")
	}
	if r.Database == "" || r.Workspace == "" {
		return errors.New("database and workspace are required")
	}
	root, err := resolveRoot(r.Root)
	if err != nil {
		return err
	}
	return validateEndpoint(root, r.Endpoint)
}

// resolveRoot requires root to exist as a directory and to already be its own
// canonical path, and returns the resolved form used for socket containment.
func resolveRoot(root string) (string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("root must be an existing directory: %w", err)
	}
	// A root that exists but is not a directory is a precondition failure with no
	// underlying error, so it must not be wrapped: %w over a nil error renders a
	// format artifact where the operator expects the reason.
	if !info.IsDir() {
		return "", errors.New("root must be an existing directory")
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}
	if filepath.Clean(root) != filepath.Clean(real) {
		return "", errors.New("root must be canonical and not symlinked")
	}
	return real, nil
}

// validateEndpoint accepts only a socket contained by root or a loopback TCP
// port. root must already be resolved by resolveRoot.
func validateEndpoint(root string, e Endpoint) error {
	if e.Socket != "" && e.Port != 0 {
		return errors.New("endpoint may specify socket or port, not both")
	}
	if e.Socket != "" {
		return validateSocketEndpoint(root, e)
	}
	if e.Port <= 0 || e.Port > 65535 {
		return errors.New("endpoint port is invalid")
	}
	if e.Host == "" {
		return errors.New("endpoint host is required")
	}
	host := strings.ToLower(e.Host)
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return errors.New("external endpoint is not eligible for handoff")
	}
	return nil
}

// validateSocketEndpoint requires an absolute canonical socket path that stays
// inside root once symlinked parent directories are resolved. The canonical-form
// requirement is load-bearing, not cosmetic: containment resolution has to clean
// the path before it can walk it, and cleaning collapses a "link/.." pair
// lexically, whereas the kernel walks the symlink first and lands somewhere
// else. Rejecting the non-canonical spelling outright keeps the two readings
// from ever disagreeing — the same reason Root must be canonical.
func validateSocketEndpoint(root string, e Endpoint) error {
	if e.Host != "" {
		return errors.New("socket endpoint must not specify a host")
	}
	if !filepath.IsAbs(e.Socket) {
		return errors.New("socket must be absolute")
	}
	if filepath.Clean(e.Socket) != e.Socket {
		return errors.New("socket must be a canonical path")
	}
	resolved, err := resolvePathForContainment(e.Socket)
	if err != nil {
		return fmt.Errorf("resolve socket: %w", err)
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("external unix endpoint is not eligible for handoff")
	}
	return nil
}

// resolvePathForContainment resolves every existing component of path. The
// final socket is commonly created by the provider after validation, so a
// missing final component is retained while symlinked parent directories are
// still resolved. This prevents a socket beneath root from escaping through a
// symlink introduced after the lexical Rel check.
func resolvePathForContainment(path string) (string, error) {
	path = filepath.Clean(path)
	var suffix []string
	for {
		if _, err := os.Lstat(path); err == nil {
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return "", err
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved), nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", os.ErrNotExist
		}
		suffix = append(suffix, filepath.Base(path))
		path = parent
	}
}

// Load reads and validates a handoff journal from path.
func Load(path string) (Journal, error) {
	b, err := os.ReadFile(path) //nolint:gosec // path is the operator-selected journal path
	if err != nil {
		return Journal{}, err
	}
	var j Journal
	if err := json.Unmarshal(b, &j); err != nil {
		return Journal{}, fmt.Errorf("decode handoff journal: %w", err)
	}
	if err := validateJournal(j); err != nil {
		return Journal{}, err
	}
	return j, nil
}

// validateJournal rejects any journal state the phase machine cannot have
// written. Each checkpoint is only meaningful in the phase that reserves it, so
// an impossible combination means corruption or an out-of-band edit rather than
// a resumable handoff.
func validateJournal(j Journal) error {
	if j.Owner != OwnerLegacyGC && j.Owner != OwnerBD {
		return errors.New("handoff journal has unknown owner")
	}
	switch j.Phase {
	case PhasePrepared, PhaseTargetConfigured, PhaseOldOwnerStopped, PhaseVerified, PhaseCommitted:
	default:
		return errors.New("handoff journal has unknown phase")
	}
	if j.Phase != PhaseCommitted && j.Owner != OwnerLegacyGC {
		return errors.New("uncommitted handoff journal must remain owned by legacy-gc")
	}
	if j.Phase == PhaseCommitted && j.Owner != OwnerBD {
		return errors.New("committed handoff journal must be owned by bd")
	}
	if (j.CommitHookInProgress || j.CommitHookRan) && j.Phase != PhaseVerified && j.Phase != PhaseCommitted {
		return errors.New("handoff journal has commit checkpoint in an invalid phase")
	}
	if j.CommitHookInProgress && j.CommitHookRan {
		return errors.New("handoff journal has conflicting commit checkpoints")
	}
	if j.Phase == PhaseCommitted && j.CommitHookInProgress {
		return errors.New("committed handoff journal has an in-progress commit")
	}
	if j.LegacyStopInProgress && j.Phase != PhaseTargetConfigured {
		return errors.New("handoff journal has a stop checkpoint in an invalid phase")
	}
	return nil
}

func save(path string, j Journal) error {
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".handoff-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(append(b, '\n'))
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	// The rename itself must be durable, not just the temp file's contents. A
	// checkpoint that a hook's external effects outlive is worse than no
	// checkpoint: on power loss the pre-reservation journal would be resurrected
	// and a retry would re-invoke a hook this journal already promised was
	// reserved.
	return syncDir(filepath.Dir(path))
}

// syncDir flushes a directory entry so a completed rename survives power loss.
func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // dir is the operator-selected journal's directory
	if err != nil {
		return fmt.Errorf("sync handoff journal directory: %w", err)
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return fmt.Errorf("sync handoff journal directory: %w", err)
	}
	if err := d.Close(); err != nil {
		return fmt.Errorf("close handoff journal directory: %w", err)
	}
	return nil
}

func result(j Journal, mutates bool) Result {
	return Result{Phase: j.Phase, Owner: j.Owner, Root: j.Request.Root, Database: j.Request.Database, Endpoint: j.Request.Endpoint, Mutates: mutates, ErrorCode: j.ErrorCode}
}

// requestResult reports an outcome refused before any journal was adopted.
func requestResult(r Request, code string) Result {
	return Result{Phase: PhasePrepared, Owner: OwnerLegacyGC, Root: r.Root, Database: r.Database, Endpoint: r.Endpoint, ErrorCode: code}
}

func mutationOccurred(j Journal) bool {
	return j.Phase == PhaseOldOwnerStopped || j.Phase == PhaseVerified ||
		j.Phase == PhaseCommitted || j.CommitHookRan || j.CommitHookInProgress ||
		j.LegacyStopInProgress
}

func snapshotCaptured(j Journal) bool {
	return j.SnapshotCaptured || len(j.Snapshot.Metadata) != 0 ||
		len(j.Snapshot.Config) != 0 || j.Snapshot.Sentinel != ""
}

func journalSaveError(j Journal, saveErr error) (Result, error) {
	j.ErrorCode = "journal_save_failed"
	j.Error = saveErr.Error()
	return result(j, mutationOccurred(j)), fmt.Errorf("save handoff journal: %w", saveErr)
}

// acquireLock opens a persistent advisory lock beside the journal. The lock
// inode is never removed: the kernel releases the lock when the process exits,
// so a crash cannot wedge retries or let stale reclaimers unlink a live lock.
func acquireLock(path string) (*os.File, error) {
	lock, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0600) //nolint:gosec // path is derived from the operator-selected journal
	if err != nil {
		return nil, err
	}
	if err := lockfile.FlockExclusiveNonBlocking(lock); err != nil {
		_ = lock.Close()
		return nil, err
	}
	return lock, nil
}

// lockErrorCode separates a genuinely concurrent handoff from a lock file that
// could not be opened at all. Reporting ENOENT or EACCES as concurrent_handoff
// sends an operator hunting a process that does not exist.
func lockErrorCode(err error) string {
	if lockfile.IsLocked(err) {
		return "concurrent_handoff"
	}
	return "lock_unavailable"
}

// Execute validates and performs a handoff. Dry-run validates identity only
// and never invokes a hook or opens a provider. A committed journal replays as
// a no-op. Failures are journaled and leave legacy-gc authoritative.
func Execute(ctx context.Context, r Request, journalPath string, h Hooks, dryRun bool) (Result, error) {
	if err := ValidateRequest(r); err != nil {
		return requestResult(r, "invalid_request"), err
	}
	if out := preflight(r, journalPath, dryRun); out != nil {
		return out.res, out.err
	}
	lock, err := acquireLock(journalPath + ".lock")
	if err != nil {
		return requestResult(r, lockErrorCode(err)), fmt.Errorf("acquire handoff lock: %w", err)
	}
	defer func() {
		_ = lockfile.FlockUnlock(lock)
		_ = lock.Close()
	}()
	return newRun(r, journalPath, h).run(ctx)
}

// stepOutcome is a terminal answer from a handoff step. A nil *stepOutcome
// means the step is satisfied and the run continues to the next one.
type stepOutcome struct {
	res Result
	err error
}

func done(res Result, err error) *stepOutcome { return &stepOutcome{res: res, err: err} }

// preflight answers from an unlocked journal read. Identity conflicts,
// committed replays, unreadable journals, and dry runs never need the lock.
func preflight(r Request, journalPath string, dryRun bool) *stepOutcome {
	j, err := Load(journalPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return done(requestResult(r, "journal_unreadable"), err)
		}
		if dryRun {
			return done(requestResult(r, ""), nil)
		}
		return nil
	}
	if out := refuseOrReplay(j, r); out != nil {
		return out
	}
	if dryRun {
		// Report the adopted journal's real phase: an operator probing a
		// mid-flight handoff must not be told the scope is still prepared.
		return done(result(j, false), nil)
	}
	return nil
}

// refuseOrReplay settles the two questions a durable journal answers on its
// own: whether it belongs to a different scope, and whether the handoff already
// committed. A nil outcome means the journal matches and is still in flight.
func refuseOrReplay(j Journal, r Request) *stepOutcome {
	if j.Request != r {
		j.ErrorCode = "identity_conflict"
		return done(result(j, mutationOccurred(j)), errors.New("handoff journal identity conflicts with request"))
	}
	if j.Phase == PhaseCommitted {
		return done(result(j, false), nil)
	}
	return nil
}

// handoffRun holds the journal state for one lock-held handoff attempt.
type handoffRun struct {
	journalPath string
	hooks       Hooks
	j           Journal
}

func newRun(r Request, journalPath string, h Hooks) *handoffRun {
	return &handoffRun{
		journalPath: journalPath,
		hooks:       h,
		j:           Journal{Request: r, Owner: OwnerLegacyGC, Phase: PhasePrepared, UpdatedAt: time.Now().UTC()},
	}
}

// run adopts any durable journal and drives the phase machine from wherever
// that journal left off.
func (x *handoffRun) run(ctx context.Context) (Result, error) {
	steps := []func(context.Context) *stepOutcome{
		x.adopt, x.stepSnapshot, x.stepConfigure, x.stepStopLegacy, x.stepVerify, x.stepCommit,
	}
	for _, step := range steps {
		if out := step(ctx); out != nil {
			return out.res, out.err
		}
	}
	return result(x.j, true), nil
}

// adopt re-reads the journal under the lock. The preflight read is only an
// optimization and may have raced with another handoff.
func (x *handoffRun) adopt(context.Context) *stepOutcome {
	old, err := Load(x.journalPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		x.j.ErrorCode = "journal_unreadable"
		return done(result(x.j, false), err)
	}
	if out := refuseOrReplay(old, x.j.Request); out != nil {
		return out
	}
	x.j = old
	return nil
}

func (x *handoffRun) save() error { return save(x.journalPath, x.j) }

// advance records the next durable phase.
func (x *handoffRun) advance(p Phase) *stepOutcome {
	x.j.Phase = p
	x.j.UpdatedAt = time.Now().UTC()
	if err := x.save(); err != nil {
		return done(journalSaveError(x.j, err))
	}
	return nil
}

// fail journals a typed refusal and leaves legacy-gc authoritative.
func (x *handoffRun) fail(code string, err error) *stepOutcome {
	x.j.ErrorCode, x.j.Error, x.j.UpdatedAt = code, err.Error(), time.Now().UTC()
	if saveErr := x.save(); saveErr != nil {
		return done(journalSaveError(x.j, errors.Join(err, saveErr)))
	}
	return done(result(x.j, mutationOccurred(x.j)), err)
}

func (x *handoffRun) clearError() {
	x.j.ErrorCode = ""
	x.j.Error = ""
}

func (x *handoffRun) stepSnapshot(ctx context.Context) *stepOutcome {
	if x.j.Phase != PhasePrepared || snapshotCaptured(x.j) {
		return nil
	}
	if x.hooks.Snapshot == nil {
		return x.fail("snapshot_unavailable", errors.New("snapshot hook is required"))
	}
	s, err := x.hooks.Snapshot(ctx, x.j.Request)
	if err != nil {
		return x.fail("snapshot_failed", err)
	}
	x.j.Snapshot = s
	x.j.SnapshotCaptured = true
	if err := x.save(); err != nil {
		return done(journalSaveError(x.j, err))
	}
	return nil
}

func (x *handoffRun) stepConfigure(ctx context.Context) *stepOutcome {
	if x.j.Phase != PhasePrepared {
		return nil
	}
	if x.hooks.Configure == nil {
		return x.fail("configure_unavailable", errors.New("configure hook is required"))
	}
	if err := x.hooks.Configure(ctx, x.j.Request, x.j.Snapshot); err != nil {
		return x.fail("target_configure_failed", err)
	}
	return x.advance(PhaseTargetConfigured)
}

func (x *handoffRun) stepStopLegacy(ctx context.Context) *stepOutcome {
	if x.j.Phase != PhaseTargetConfigured {
		return nil
	}
	if x.hooks.StopLegacy == nil {
		return x.fail("owner_stop_unavailable", errors.New("legacy owner stop hook is required"))
	}
	// Reserve the stop durably before invoking it. Once the hook has been
	// entered its effect cannot be assumed absent, so a failed or interrupted
	// stop must report a mutation rather than an untouched legacy owner.
	if !x.j.LegacyStopInProgress {
		x.j.LegacyStopInProgress = true
		x.j.UpdatedAt = time.Now().UTC()
		if err := x.save(); err != nil {
			return done(journalSaveError(x.j, err))
		}
	}
	if err := x.hooks.StopLegacy(ctx, x.j.Request, x.j.Snapshot); err != nil {
		return x.fail("owner_stop_failed", err)
	}
	x.j.LegacyStopInProgress = false
	return x.advance(PhaseOldOwnerStopped)
}

func (x *handoffRun) stepVerify(ctx context.Context) *stepOutcome {
	if x.j.Phase != PhaseOldOwnerStopped {
		return nil
	}
	if x.hooks.Verify == nil {
		return x.fail("verify_unavailable", errors.New("verify hook is required"))
	}
	if err := x.hooks.Verify(ctx, x.j.Request, x.j.Snapshot); err != nil {
		return x.fail("verification_failed", err)
	}
	return x.advance(PhaseVerified)
}

func (x *handoffRun) stepCommit(ctx context.Context) *stepOutcome {
	if x.j.Phase != PhaseVerified {
		return nil
	}
	if out := x.replayAmbiguousCommit(ctx); out != nil {
		return out
	}
	if out := x.invokeCommit(ctx); out != nil {
		return out
	}
	x.j.Owner = OwnerBD
	x.clearError()
	return x.advance(PhaseCommitted)
}

// replayAmbiguousCommit resolves a commit that was reserved but never reached
// its completion checkpoint. Only an explicitly idempotent provider hook may
// advance it.
func (x *handoffRun) replayAmbiguousCommit(ctx context.Context) *stepOutcome {
	if !x.j.CommitHookInProgress || x.j.CommitHookRan {
		return nil
	}
	if x.hooks.CommitReplay == nil {
		return x.fail("commit_recovery_required", errors.New(commitRecoveryMessage(x.j)))
	}
	if err := x.hooks.CommitReplay(ctx, x.j.Request, x.j.Snapshot); err != nil {
		return x.fail("commit_recovery_failed", err)
	}
	return x.recordCommitRan()
}

// commitRecoveryMessage explains an ambiguous commit, keeping the original
// commit failure as context. It builds the message once from the journal's own
// commit_failed error and is then a fixed point: a journal that already carries
// the explanation is returned unchanged, so repeated retries neither grow the
// message without bound nor discard the cause that started the recovery. An
// operator reading a wedged journal needs that cause most on the retry where a
// regenerated message would have lost it.
func commitRecoveryMessage(j Journal) string {
	const message = "commit hook outcome is unknown; an idempotent commit replay hook is required"
	if j.ErrorCode == "commit_failed" && j.Error != "" {
		return j.Error + "; " + message
	}
	if strings.HasSuffix(j.Error, message) {
		return j.Error
	}
	return message
}

func (x *handoffRun) invokeCommit(ctx context.Context) *stepOutcome {
	if x.j.CommitHookRan {
		return nil
	}
	if x.hooks.Commit == nil {
		return x.fail("commit_unavailable", errors.New("commit hook is required"))
	}
	// Reserve the commit hook durably before invoking it. If the process
	// crashes after this checkpoint, a retry refuses to invoke Commit a
	// second time unless the provider supplies CommitReplay.
	x.j.CommitHookInProgress = true
	x.clearError()
	x.j.UpdatedAt = time.Now().UTC()
	if err := x.save(); err != nil {
		return done(journalSaveError(x.j, err))
	}
	if err := x.hooks.Commit(ctx, x.j.Request, x.j.Snapshot); err != nil {
		return x.fail("commit_failed", err)
	}
	return x.recordCommitRan()
}

// recordCommitRan durably retires the in-progress reservation once the commit
// effect is known to have been applied exactly once.
func (x *handoffRun) recordCommitRan() *stepOutcome {
	x.j.CommitHookRan = true
	x.j.CommitHookInProgress = false
	x.clearError()
	x.j.UpdatedAt = time.Now().UTC()
	if err := x.save(); err != nil {
		return done(journalSaveError(x.j, err))
	}
	return nil
}
