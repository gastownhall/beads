// Package federation coordinates multi-remote synchronization operations
// for beads' Dolt-powered storage layer. It provides the SyncOrchestrator
// which pushes to multiple remotes with failure handling and retry logic.
package federation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/lockfile"
	"github.com/steveyegge/beads/internal/storage"
)

// RemotePusher is the minimal interface needed for push/pull operations.
// DoltStore satisfies this via its PushTo and PullFrom methods.
type RemotePusher interface {
	PushTo(ctx context.Context, peer string) error
	PullFrom(ctx context.Context, peer string) ([]storage.Conflict, error)
}

// PushStatus represents the outcome of a push to a single remote.
type PushStatus string

const (
	PushStatusSuccess PushStatus = "success"
	PushStatusFailed  PushStatus = "failed"
	PushStatusSkipped PushStatus = "skipped" // not attempted (e.g., primary failed)
)

// PushResult is the result of pushing to a single remote.
type PushResult struct {
	Remote   config.RemoteConfig
	Status   PushStatus
	Error    error
	Retries  int
	Duration time.Duration
}

// PushAllResult aggregates results from pushing to all configured remotes.
type PushAllResult struct {
	Results  []PushResult
	Duration time.Duration
}

// PrimaryOK returns true if the primary remote push succeeded.
func (r *PushAllResult) PrimaryOK() bool {
	for _, res := range r.Results {
		if res.Remote.Role == config.RemoteRolePrimary {
			return res.Status == PushStatusSuccess
		}
	}
	return false
}

// AllOK returns true if all attempted remotes succeeded.
func (r *PushAllResult) AllOK() bool {
	for _, res := range r.Results {
		if res.Status == PushStatusFailed {
			return false
		}
	}
	return true
}

// FailedRemotes returns the names of remotes that failed.
func (r *PushAllResult) FailedRemotes() []string {
	var names []string
	for _, res := range r.Results {
		if res.Status == PushStatusFailed {
			names = append(names, res.Remote.Name)
		}
	}
	return names
}

// ErrDegradedSync indicates primary succeeded but one or more backups failed.
// Callers should treat this as a warning, not a full failure.
var ErrDegradedSync = errors.New("degraded sync: primary succeeded but one or more backups failed")

// ErrNoPrimary indicates no primary remote is configured.
var ErrNoPrimary = errors.New("no primary remote configured in federation.remotes")

// ErrLockHeld indicates another sync operation is in progress.
var ErrLockHeld = errors.New("federation sync lock held by another process")

// Option configures a SyncOrchestrator.
type Option func(*SyncOrchestrator)

// WithMaxRetries sets the maximum number of retries for transient errors.
func WithMaxRetries(n int) Option {
	return func(o *SyncOrchestrator) { o.maxRetries = n }
}

// WithRetryInterval sets the initial retry backoff interval.
func WithRetryInterval(d time.Duration) Option {
	return func(o *SyncOrchestrator) { o.retryInterval = d }
}

// WithIsRetryable injects a function to classify errors as transient.
func WithIsRetryable(fn func(error) bool) Option {
	return func(o *SyncOrchestrator) { o.isRetryable = fn }
}

// SyncOrchestrator coordinates multi-remote push operations with
// sequential execution, failure handling, and advisory locking.
type SyncOrchestrator struct {
	store         RemotePusher
	remotes       []config.RemoteConfig
	beadsDir      string
	maxRetries    int
	retryInterval time.Duration
	isRetryable   func(error) bool
}

// New creates a SyncOrchestrator for the given remotes.
func New(store RemotePusher, remotes []config.RemoteConfig, beadsDir string, opts ...Option) *SyncOrchestrator {
	o := &SyncOrchestrator{
		store:         store,
		remotes:       remotes,
		beadsDir:      beadsDir,
		maxRetries:    3,
		retryInterval: 500 * time.Millisecond,
		isRetryable:   defaultIsRetryable,
	}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// PushAll pushes to all configured remotes sequentially.
// Primary is always pushed first. If primary fails, backups are skipped.
// Returns ErrDegradedSync (wrapped) if primary succeeds but backups fail.
func (o *SyncOrchestrator) PushAll(ctx context.Context) (*PushAllResult, error) {
	primary, backups := o.sortedRemotes()
	if primary == nil {
		return nil, ErrNoPrimary
	}

	// Acquire advisory lock
	lockFile, err := o.acquireLock()
	if err != nil {
		return nil, err
	}
	defer o.releaseLock(lockFile)

	start := time.Now()
	results := make([]PushResult, 0, len(o.remotes))

	// Push to primary first
	primaryResult := o.pushWithRetry(ctx, *primary)
	results = append(results, primaryResult)

	if primaryResult.Status != PushStatusSuccess {
		// Primary failed — skip all backups
		for _, backup := range backups {
			results = append(results, PushResult{
				Remote: backup,
				Status: PushStatusSkipped,
			})
		}
		return &PushAllResult{Results: results, Duration: time.Since(start)},
			fmt.Errorf("primary remote %q push failed: %w", primary.Name, primaryResult.Error)
	}

	// Push to backups (failures don't stop other backups)
	var backupFailed bool
	for _, backup := range backups {
		if ctx.Err() != nil {
			// Context canceled — skip remaining
			results = append(results, PushResult{
				Remote: backup,
				Status: PushStatusSkipped,
			})
			continue
		}
		result := o.pushWithRetry(ctx, backup)
		results = append(results, result)
		if result.Status == PushStatusFailed {
			backupFailed = true
		}
	}

	allResult := &PushAllResult{Results: results, Duration: time.Since(start)}

	if backupFailed {
		return allResult, fmt.Errorf("%w: %v", ErrDegradedSync, allResult.FailedRemotes())
	}
	return allResult, nil
}

// Pull pulls from the primary remote only.
// Backups are push-only mirrors and are never pulled from.
func (o *SyncOrchestrator) Pull(ctx context.Context) ([]storage.Conflict, error) {
	primary, _ := o.sortedRemotes()
	if primary == nil {
		return nil, ErrNoPrimary
	}
	return o.store.PullFrom(ctx, primary.Name)
}

// pushWithRetry pushes to a single remote with retry logic for transient errors.
func (o *SyncOrchestrator) pushWithRetry(ctx context.Context, remote config.RemoteConfig) PushResult {
	start := time.Now()
	retries := 0

	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = o.retryInterval
	bo.MaxElapsedTime = 0 // controlled by maxRetries instead

	var lastErr error
	for attempt := 0; attempt <= o.maxRetries; attempt++ {
		if attempt > 0 {
			retries++
			wait := bo.NextBackOff()
			if wait == backoff.Stop {
				break
			}
			select {
			case <-ctx.Done():
				return PushResult{
					Remote:   remote,
					Status:   PushStatusFailed,
					Error:    ctx.Err(),
					Retries:  retries - 1,
					Duration: time.Since(start),
				}
			case <-time.After(wait):
			}
		}

		err := o.store.PushTo(ctx, remote.Name)
		if err == nil {
			return PushResult{
				Remote:   remote,
				Status:   PushStatusSuccess,
				Retries:  retries,
				Duration: time.Since(start),
			}
		}

		lastErr = err
		if !o.isRetryable(err) {
			break
		}
	}

	return PushResult{
		Remote:   remote,
		Status:   PushStatusFailed,
		Error:    lastErr,
		Retries:  retries,
		Duration: time.Since(start),
	}
}

// sortedRemotes returns primary first, then backups sorted by name.
func (o *SyncOrchestrator) sortedRemotes() (*config.RemoteConfig, []config.RemoteConfig) {
	var primary *config.RemoteConfig
	var backups []config.RemoteConfig

	for i := range o.remotes {
		if o.remotes[i].Role == config.RemoteRolePrimary {
			primary = &o.remotes[i]
		} else {
			backups = append(backups, o.remotes[i])
		}
	}

	sort.Slice(backups, func(i, j int) bool {
		return backups[i].Name < backups[j].Name
	})

	return primary, backups
}

// acquireLock acquires a non-blocking advisory lock for the push sequence.
func (o *SyncOrchestrator) acquireLock() (*os.File, error) {
	lockPath := filepath.Join(o.beadsDir, "federation.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600) //nolint:gosec // lockPath is constructed from trusted beadsDir
	if err != nil {
		return nil, fmt.Errorf("cannot open federation lock file: %w", err)
	}

	if err := lockfile.FlockExclusiveNonBlocking(f); err != nil {
		_ = f.Close()
		if lockfile.IsLocked(err) {
			return nil, ErrLockHeld
		}
		return nil, fmt.Errorf("cannot acquire federation lock: %w", err)
	}

	return f, nil
}

// releaseLock releases the advisory lock and closes the file.
func (o *SyncOrchestrator) releaseLock(f *os.File) {
	if f == nil {
		return
	}
	_ = lockfile.FlockUnlock(f)
	_ = f.Close()
}

// defaultIsRetryable classifies errors as transient (retryable) or permanent.
// Callers can override this via WithIsRetryable for tighter integration
// with DoltStore's isRetryableError().
func defaultIsRetryable(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	transientPatterns := []string{
		"timeout",
		"connection reset",
		"connection refused",
		"broken pipe",
		"i/o timeout",
		"bad connection",
		"invalid connection",
		"lost connection",
		"server has gone away",
		"temporary failure",
		"try again",
	}
	for _, pat := range transientPatterns {
		if containsFold(msg, pat) {
			return true
		}
	}
	return false
}

// containsFold reports whether s contains substr (case-insensitive).
func containsFold(s, substr string) bool {
	sLen := len(s)
	subLen := len(substr)
	if subLen > sLen {
		return false
	}
	for i := 0; i <= sLen-subLen; i++ {
		match := true
		for j := 0; j < subLen; j++ {
			sc := s[i+j]
			tc := substr[j]
			if sc >= 'A' && sc <= 'Z' {
				sc += 'a' - 'A'
			}
			if tc >= 'A' && tc <= 'Z' {
				tc += 'a' - 'A'
			}
			if sc != tc {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
