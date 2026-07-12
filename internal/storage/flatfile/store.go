// Package flatfile provides a flat-file storage backend for beads.
//
// Each issue is stored as a separate JSON file under .beads/issues/,
// with comments in .beads/comments/ and config in .beads/config_kv.json.
// No database server is required; git provides version control and sync.
package flatfile

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
)

// FlatFileStore implements the storage.Storage interface using one JSON file
// per issue. It also satisfies storage.StoreLocator and storage.LifecycleManager.
// The embedded unsupportedDoltStorage shell answers the few methods the backend
// deliberately omits (FederationStore) with typed *storage.ErrUnsupported;
// every method implemented on *FlatFileStore shadows it.
type FlatFileStore struct {
	unsupportedDoltStorage

	beadsDir      string // root .beads/ directory
	issuesDir     string // .beads/issues/
	commentsDir   string // .beads/comments/
	memoriesDir   string // .beads/memories/
	eventsDir     string // .beads/events/
	counterPath   string // .beads/counter.json
	configKVPath  string // .beads/config_kv.json
	localMetaPath string // .beads/local_metadata.json
	mergeslotPath string // .beads/merge_slot.json
	repoMtimePath string // .beads/repo_mtime.json

	// Normalized custom-status/type stores, the flat-file analog of the
	// custom_statuses/custom_types tables. Synced by SetConfig and — like
	// the SQL tables — deliberately left untouched by DeleteConfig.
	customStatusesPath string // .beads/custom_statuses.json
	customTypesPath    string // .beads/custom_types.json

	mu         sync.RWMutex // protects counter file
	kvMu       sync.Mutex   // protects config/metadata KV read-modify-write
	commentSeq atomic.Int64 // monotonic counter for comment IDs

	// gitIdentOnce/gitIdentEnv cache the git-identity probe: when the repo has
	// no usable committer identity (fresh machine, CI runner), every git
	// invocation gets a synthetic fallback identity injected via env so
	// auto-commits don't fail with "empty ident name". See gitIdentityEnv.
	gitIdentOnce sync.Once
	gitIdentEnv  []string

	// writeMu serializes every read-modify-write cycle on issue files and the
	// merge-slot file: the whole read → mutate → writeIssue span of a mutation
	// runs under it, so concurrent same-issue mutations in one process cannot
	// lose updates (the SQL backends get this from transactions). It is a
	// single store-wide mutex, not a per-issue striped lock, on purpose: bd is
	// a single-process CLI where fsync dominates write cost, and multi-issue
	// mutations (UpdateIssueID inbound-edge rewrite, DeleteIssues cascade,
	// batch create, ReclaimExpiredLeases) would need ordered multi-lock
	// acquisition under striping. Pure readers do not take it — writeIssue's
	// atomic rename already guarantees they see a consistent file. Holders
	// must not call another writeMu-taking method (the lock is not reentrant);
	// wrappers like ReopenIssue/UpdateIssueType/ClaimReadyIssue therefore rely
	// on their inner call for locking. Cross-process mutual exclusion is out
	// of scope: the flat-file store assumes one writing process per checkout.
	writeMu sync.Mutex

	// txMu isolates transactions from every other write. RunInTransaction
	// holds the write half for the whole callback, so two transactions cannot
	// interleave journaled writes AND no non-transactional mutation can land
	// while a journal is active (SQL backends never roll back another
	// session's committed write; without this exclusion a concurrent plain
	// write would be captured in the foreign journal and reverted). Non-tx
	// mutators take the read half via txShared/lockWrite — see those helpers
	// for the lock order (txMu → writeMu → mu/kvMu) and the goroutine-owner
	// reentrancy rule. txOwner is the goroutine ID of the active transaction
	// (0 when none). Nested RunInTransaction calls would self-deadlock and
	// are unsupported, as on the SQL backends. journal is the active
	// transaction's pre-image map (nil outside a transaction), guarded by
	// journalMu.
	txMu      sync.RWMutex
	txOwner   atomic.Int64
	journalMu sync.Mutex
	journal   map[string]journalEntry

	closed atomic.Bool
}

// NewFlatFileStore creates a FlatFileStore rooted at beadsDir (typically ".beads").
// It creates the required subdirectories if they do not exist.
func NewFlatFileStore(beadsDir string) (*FlatFileStore, error) {
	s, err := newFlatFileStoreNoCreate(beadsDir)
	if err != nil {
		return nil, err
	}

	dirs := []string{s.issuesDir, s.commentsDir, s.memoriesDir, s.eventsDir}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("flatfile: create directory %s: %w", d, err)
		}
	}

	return s, nil
}

// newFlatFileStoreNoCreate builds a FlatFileStore without touching the
// filesystem. Read paths tolerate the missing subdirectories (readDirSafe,
// loadAllIssues); read-only cross-repo opens use this so they never mutate a
// foreign checkout (GH#3231).
func newFlatFileStoreNoCreate(beadsDir string) (*FlatFileStore, error) {
	abs, err := filepath.Abs(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("flatfile: resolve beads dir: %w", err)
	}

	// .beads is git-synced: a hostile repo can commit the directory ITSELF as
	// a symlink that a plain checkout materializes, routing every store read
	// and write (issues/, comments/, events/, config files) to an
	// attacker-chosen target. safePath's per-write rejectSymlink calls Lstat
	// only the final path component — Lstat traverses ancestors — so they
	// never see this link; reject it once here, which both open paths share.
	// Ancestors of beadsDir are the user's own filesystem, not controlled by
	// a checkout, so symlinks there (e.g. macOS /var) stay allowed.
	if err := rejectSymlink(abs); err != nil {
		return nil, err
	}

	s := &FlatFileStore{
		beadsDir:      abs,
		issuesDir:     filepath.Join(abs, "issues"),
		commentsDir:   filepath.Join(abs, "comments"),
		memoriesDir:   filepath.Join(abs, "memories"),
		eventsDir:     filepath.Join(abs, "events"),
		counterPath:   filepath.Join(abs, "counter.json"),
		configKVPath:  filepath.Join(abs, "config_kv.json"),
		localMetaPath: filepath.Join(abs, "local_metadata.json"),
		mergeslotPath: filepath.Join(abs, "merge_slot.json"),
		repoMtimePath: filepath.Join(abs, "repo_mtime.json"),

		customStatusesPath: filepath.Join(abs, "custom_statuses.json"),
		customTypesPath:    filepath.Join(abs, "custom_types.json"),
	}

	return s, nil
}

// goroutineID returns the current goroutine's ID, parsed from the
// "goroutine <id> [<state>]:" header runtime.Stack has emitted since Go 1.0.
// Used only to let the transaction goroutine's own mutations skip txMu's
// read half (it already holds the write half); goroutine IDs are never
// stored beyond the transaction span or used for anything else.
func goroutineID() int64 {
	buf := make([]byte, 64)
	n := runtime.Stack(buf, false)
	header := buf[:n]
	header = bytes.TrimPrefix(header, []byte("goroutine "))
	if i := bytes.IndexByte(header, ' '); i >= 0 {
		header = header[:i]
	}
	id, _ := strconv.ParseInt(string(header), 10, 64)
	return id
}

// txShared acquires txMu's read half so the caller's mutation cannot overlap
// an active transaction (and thus can never be captured in a foreign journal
// and reverted by its rollback). It returns the matching release func. The
// goroutine running RunInTransaction's callback already holds the write half,
// so its (promoted) mutations skip the read half by owner match — which is
// why mutations inside a transaction callback MUST run on the goroutine that
// called RunInTransaction: a callback-spawned goroutine's mutation blocks
// until the transaction completes. Like writeMu, txShared is not reentrant:
// a holder must not call another txShared/lockWrite-taking method.
func (s *FlatFileStore) txShared() func() {
	if s.txOwner.Load() == goroutineID() {
		return func() {}
	}
	s.txMu.RLock()
	return s.txMu.RUnlock
}

// lockWrite acquires txShared then writeMu — the required order (txMu before
// writeMu, matching rollbackJournal, which takes writeMu while the write half
// of txMu is held) — and returns the combined release func. Every exported
// mutator that previously took writeMu directly goes through it.
func (s *FlatFileStore) lockWrite() func() {
	release := s.txShared()
	s.writeMu.Lock()
	return func() {
		s.writeMu.Unlock()
		release()
	}
}

// Close marks the store as closed. Subsequent operations should return an error.
func (s *FlatFileStore) Close() error {
	s.closed.Store(true)
	return nil
}

// IsClosed reports whether Close has been called.
func (s *FlatFileStore) IsClosed() bool {
	return s.closed.Load()
}

// Path returns the root .beads/ directory path.
func (s *FlatFileStore) Path() string {
	return s.beadsDir
}

// CLIDir returns the root .beads/ directory path (same as Path for flat-file).
func (s *FlatFileStore) CLIDir() string {
	return s.beadsDir
}

// errClosed is returned by operations called after Close.
var errClosed = fmt.Errorf("flatfile: store is closed")

// checkClosed returns errClosed if the store has been closed.
func (s *FlatFileStore) checkClosed() error {
	if s.closed.Load() {
		return errClosed
	}
	return nil
}
