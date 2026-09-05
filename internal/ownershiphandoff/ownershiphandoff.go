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
	Request              Request   `json:"request"`
	Snapshot             Snapshot  `json:"snapshot,omitempty"`
	SnapshotCaptured     bool      `json:"snapshot_captured,omitempty"`
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
	Phase     Phase    `json:"phase"`
	Owner     Owner    `json:"owner"`
	Root      string   `json:"root"`
	Database  string   `json:"database"`
	Endpoint  Endpoint `json:"endpoint"`
	Mutates   bool     `json:"mutates"`
	ErrorCode string   `json:"error_code,omitempty"`
}

// Hooks are provider-owned operations. StopLegacy must refuse unless it can
// positively identify the process as the legacy owner; it must never kill an
// unknown process.
type Hooks struct {
	Snapshot   func(context.Context, Request) (Snapshot, error)
	Configure  func(context.Context, Request, Snapshot) error
	StopLegacy func(context.Context, Request, Snapshot) error
	Verify     func(context.Context, Request, Snapshot) error
	Commit     func(context.Context, Request, Snapshot) error
	// CommitReplay is an optional idempotent recovery operation for a commit
	// attempt whose outcome is unknown (for example, a process crash after the
	// hook ran but before its completion checkpoint was durable). Without this
	// explicit provider guarantee, Execute refuses to guess or invoke Commit a
	// second time.
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
	if r.Root == "" {
		return errors.New("root is required")
	}
	abs := r.Root
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("root must be an existing directory: %w", err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return fmt.Errorf("resolve root: %w", err)
	}
	if filepath.Clean(abs) != filepath.Clean(real) {
		return errors.New("root must be canonical and not symlinked")
	}
	e := r.Endpoint
	if e.Socket != "" && e.Port != 0 {
		return errors.New("endpoint may specify socket or port, not both")
	}
	if e.Socket != "" {
		if e.Host != "" {
			return errors.New("socket endpoint must not specify a host")
		}
		if !filepath.IsAbs(e.Socket) {
			return errors.New("socket must be absolute")
		}
		resolved, err := resolvePathForContainment(e.Socket)
		if err != nil {
			return fmt.Errorf("resolve socket: %w", err)
		}
		rel, err := filepath.Rel(real, resolved)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errors.New("external unix endpoint is not eligible for handoff")
		}
		return nil
	}
	if e.Port <= 0 || e.Port > 65535 {
		return errors.New("endpoint port is invalid")
	}
	host := strings.ToLower(e.Host)
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return errors.New("external endpoint is not eligible for handoff")
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
	if j.Owner != OwnerLegacyGC && j.Owner != OwnerBD {
		return Journal{}, errors.New("handoff journal has unknown owner")
	}
	switch j.Phase {
	case PhasePrepared, PhaseTargetConfigured, PhaseOldOwnerStopped, PhaseVerified, PhaseCommitted:
	default:
		return Journal{}, errors.New("handoff journal has unknown phase")
	}
	if j.Phase != PhaseCommitted && j.Owner != OwnerLegacyGC {
		return Journal{}, errors.New("uncommitted handoff journal must remain owned by legacy-gc")
	}
	if j.Phase == PhaseCommitted && j.Owner != OwnerBD {
		return Journal{}, errors.New("committed handoff journal must be owned by bd")
	}
	if (j.CommitHookInProgress || j.CommitHookRan) && j.Phase != PhaseVerified && j.Phase != PhaseCommitted {
		return Journal{}, errors.New("handoff journal has commit checkpoint in an invalid phase")
	}
	if j.CommitHookInProgress && j.CommitHookRan {
		return Journal{}, errors.New("handoff journal has conflicting commit checkpoints")
	}
	if j.Phase == PhaseCommitted && j.CommitHookInProgress {
		return Journal{}, errors.New("committed handoff journal has an in-progress commit")
	}
	return j, nil
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
	return nil
}

func result(j Journal, mutates bool) Result {
	return Result{Phase: j.Phase, Owner: j.Owner, Root: j.Request.Root, Database: j.Request.Database, Endpoint: j.Request.Endpoint, Mutates: mutates, ErrorCode: j.ErrorCode}
}

func mutationOccurred(j Journal) bool {
	return j.Phase == PhaseOldOwnerStopped || j.Phase == PhaseVerified ||
		j.Phase == PhaseCommitted || j.CommitHookRan || j.CommitHookInProgress
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

// Execute validates and performs a handoff. Dry-run validates identity only
// and never invokes a hook or opens a provider. A committed journal replays as
// a no-op. Failures are journaled and leave legacy-gc authoritative.
func Execute(ctx context.Context, r Request, journalPath string, h Hooks, dryRun bool) (Result, error) {
	if err := ValidateRequest(r); err != nil {
		return Result{Phase: PhasePrepared, Owner: OwnerLegacyGC, Root: r.Root, Database: r.Database, Endpoint: r.Endpoint, ErrorCode: "invalid_request"}, err
	}
	if j, err := Load(journalPath); err == nil {
		if j.Request != r {
			j.ErrorCode = "identity_conflict"
			return result(j, false), errors.New("handoff journal identity conflicts with request")
		}
		if j.Phase == PhaseCommitted {
			return result(j, false), nil
		}
		r = j.Request
	} else if !os.IsNotExist(err) {
		return Result{Phase: PhasePrepared, Owner: OwnerLegacyGC, Root: r.Root, Database: r.Database, Endpoint: r.Endpoint, ErrorCode: "journal_unreadable"}, err
	}
	if dryRun {
		return Result{Phase: PhasePrepared, Owner: OwnerLegacyGC, Root: r.Root, Database: r.Database, Endpoint: r.Endpoint, Mutates: false}, nil
	}
	lockPath := journalPath + ".lock"
	lock, err := acquireLock(lockPath)
	if err != nil {
		return Result{Phase: PhasePrepared, Owner: OwnerLegacyGC, Root: r.Root, Database: r.Database, Endpoint: r.Endpoint, ErrorCode: "concurrent_handoff"}, fmt.Errorf("acquire handoff lock: %w", err)
	}
	defer func() {
		_ = lockfile.FlockUnlock(lock)
		_ = lock.Close()
	}()
	j := Journal{Request: r, Owner: OwnerLegacyGC, Phase: PhasePrepared, UpdatedAt: time.Now().UTC()}
	if old, err := Load(journalPath); err == nil {
		if old.Request != r {
			old.ErrorCode = "identity_conflict"
			return result(old, mutationOccurred(old)), errors.New("handoff journal identity conflicts with request")
		}
		if old.Phase == PhaseCommitted {
			return result(old, false), nil
		}
		j = old
	} else if !os.IsNotExist(err) {
		return result(j, false), err
	}
	fail := func(code string, err error) (Result, error) {
		j.ErrorCode, j.Error, j.UpdatedAt = code, err.Error(), time.Now().UTC()
		if saveErr := save(journalPath, j); saveErr != nil {
			return journalSaveError(j, errors.Join(err, saveErr))
		}
		return result(j, mutationOccurred(j)), err
	}
	if j.Phase == PhasePrepared {
		if !snapshotCaptured(j) {
			if h.Snapshot == nil {
				return fail("snapshot_unavailable", errors.New("snapshot hook is required"))
			}
			s, err := h.Snapshot(ctx, r)
			if err != nil {
				return fail("snapshot_failed", err)
			}
			j.Snapshot = s
			j.SnapshotCaptured = true
			if err := save(journalPath, j); err != nil {
				return journalSaveError(j, err)
			}
		}
	}
	if j.Phase == PhasePrepared {
		if h.Configure == nil {
			return fail("configure_unavailable", errors.New("configure hook is required"))
		}
		if err := h.Configure(ctx, r, j.Snapshot); err != nil {
			return fail("target_configure_failed", err)
		}
		j.Phase = PhaseTargetConfigured
		j.UpdatedAt = time.Now().UTC()
		if err := save(journalPath, j); err != nil {
			return journalSaveError(j, err)
		}
	}
	if j.Phase == PhaseTargetConfigured {
		if h.StopLegacy == nil {
			return fail("owner_stop_unavailable", errors.New("legacy owner stop hook is required"))
		}
		if err := h.StopLegacy(ctx, r, j.Snapshot); err != nil {
			return fail("owner_stop_failed", err)
		}
		j.Phase = PhaseOldOwnerStopped
		j.UpdatedAt = time.Now().UTC()
		if err := save(journalPath, j); err != nil {
			return journalSaveError(j, err)
		}
	}
	if j.Phase == PhaseOldOwnerStopped {
		if h.Verify == nil {
			return fail("verify_unavailable", errors.New("verify hook is required"))
		}
		if err := h.Verify(ctx, r, j.Snapshot); err != nil {
			return fail("verification_failed", err)
		}
		j.Phase = PhaseVerified
		j.UpdatedAt = time.Now().UTC()
		if err := save(journalPath, j); err != nil {
			return journalSaveError(j, err)
		}
	}
	if j.Phase == PhaseVerified {
		if j.CommitHookInProgress && !j.CommitHookRan {
			if h.CommitReplay == nil {
				message := "commit hook outcome is unknown; an idempotent commit replay hook is required"
				if j.Error != "" {
					message = j.Error + "; " + message
				}
				return fail("commit_recovery_required", errors.New(message))
			}
			if err := h.CommitReplay(ctx, r, j.Snapshot); err != nil {
				return fail("commit_recovery_failed", err)
			}
			j.CommitHookRan = true
			j.CommitHookInProgress = false
			j.ErrorCode = ""
			j.Error = ""
			j.UpdatedAt = time.Now().UTC()
			if err := save(journalPath, j); err != nil {
				return journalSaveError(j, err)
			}
		}
		if !j.CommitHookRan {
			if h.Commit == nil {
				return fail("commit_unavailable", errors.New("commit hook is required"))
			}
			// Reserve the commit hook durably before invoking it. If the process
			// crashes after this checkpoint, a retry refuses to invoke Commit a
			// second time unless the provider supplies CommitReplay.
			j.CommitHookInProgress = true
			j.ErrorCode = ""
			j.Error = ""
			j.UpdatedAt = time.Now().UTC()
			if err := save(journalPath, j); err != nil {
				return journalSaveError(j, err)
			}
			if err := h.Commit(ctx, r, j.Snapshot); err != nil {
				j.ErrorCode = "commit_failed"
				j.Error = err.Error()
				j.UpdatedAt = time.Now().UTC()
				if saveErr := save(journalPath, j); saveErr != nil {
					return journalSaveError(j, errors.Join(err, saveErr))
				}
				return result(j, mutationOccurred(j)), err
			}
			j.CommitHookRan = true
			j.CommitHookInProgress = false
			j.ErrorCode = ""
			j.Error = ""
			j.UpdatedAt = time.Now().UTC()
			if err := save(journalPath, j); err != nil {
				return journalSaveError(j, err)
			}
		}
		j.Owner = OwnerBD
		j.Phase = PhaseCommitted
		j.ErrorCode = ""
		j.Error = ""
		j.UpdatedAt = time.Now().UTC()
		if err := save(journalPath, j); err != nil {
			return journalSaveError(j, err)
		}
	}
	return result(j, true), nil
}
