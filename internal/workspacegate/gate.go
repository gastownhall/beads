// Package workspacegate provides a two-level cross-process fence for beads
// workspaces: normal commands hold a SHARED gate for the lifetime of their
// store/provider, and maintenance operations (mode migration, restore,
// destructive repair) hold it EXCLUSIVELY so they can detect cooperating
// bd activity on the workspace and on the physical database root, and
// refuse to run over it — the gate cannot ask a holder to leave, only
// make it visible and exclude new work.
//
// The OS advisory lock (flock / LockFileEx via internal/lockfile) is the
// only authority. Gate files are never deleted, and they deliberately live
// in a stable parent directory OUTSIDE every root a gated operation may
// replace: on Unix, flock follows the open inode, not the path, so a lock
// file inside a directory that gets renamed or recreated would let new
// processes lock a fresh inode while the old holder still holds the stale
// one. Consequently:
//
//   - the workspace gate for <dir>/.beads lives at <dir>/.beads.gate.lock
//     (never inside .beads), and
//   - the physical-root gate for a server root like .beads/dolt lives at
//     .beads/dolt.gate.lock (beside, never inside, the root).
//
// Invariants for callers:
//
//   - a gated operation must never replace or rename the gate file's
//     parent directory;
//   - every gate a call path needs is acquired in a single AcquireAll —
//     never nest a Gate.Acquire inside a held MultiHandle, or acquire
//     gates one by one in ad-hoc order; the sorted-path total order only
//     protects callers that go through one AcquireAll.
//
// The gate is cooperative. Processes that predate it, or library
// consumers using the ungated beads.Open, acquire nothing; operations
// that need a hard quiescence guarantee must refuse when they cannot
// assume sole cooperative ownership, rather than pretend this lock fences
// strangers. Filesystems without advisory-lock support (some network
// mounts) fail acquisition rather than degrade silently.
package workspacegate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/steveyegge/beads/internal/lockfile"
)

// Mode selects shared (normal command) or exclusive (maintenance
// operation) acquisition. There is no upgrade path: converting a held
// shared lock to exclusive is not atomic on any supported platform and a
// same-process upgrade attempt can deadlock against itself, so callers
// must classify the command and acquire its final mode once.
type Mode int

const (
	// Shared is held by normal commands for the lifetime of their open
	// store or UOW provider. Any number of shared holders coexist.
	//
	// Starvation note: a rolling sequence of shared holders (e.g. one bd
	// command after another against a busy workspace) can starve an
	// Exclusive acquirer indefinitely — acquisition is LOCK_NB polling
	// with no intent signal, so a new shared holder has no way to know an
	// exclusive attempt is queued and back off. A future extension could
	// have Exclusive publish an advisory "waiting" marker (parallel to
	// the holder-info sidecar) that new Shared acquisitions check and
	// voluntarily defer to, but that does not exist yet: Options.Wait on
	// an Exclusive acquisition is not a fairness guarantee.
	Shared Mode = iota
	// Exclusive is held by maintenance operations. It conflicts with
	// every other holder, shared or exclusive. See the starvation note
	// on Shared: nothing here prevents shared holders from starving an
	// Exclusive acquirer.
	Exclusive
)

func (m Mode) String() string {
	if m == Exclusive {
		return "exclusive"
	}
	return "shared"
}

// ErrBusy is returned when the gate cannot be acquired within the
// configured wait budget. Use errors.Is; the wrapped error carries holder
// diagnostics when available. The public alias for external consumers is
// beads.ErrGateBusy.
var ErrBusy = errors.New("workspace gate busy")

// Options tunes acquisition. The zero value means a single non-blocking
// attempt with no diagnostics callback.
type Options struct {
	// Wait bounds how long Acquire keeps retrying after the first busy
	// attempt. Zero or negative means exactly one non-blocking try.
	// The underlying blocking lock primitives have no deadline support,
	// so waiting is implemented as timed polling of the non-blocking
	// primitive. This is not a fairness guarantee: see the starvation
	// note on Exclusive — a Wait on an Exclusive acquisition can still
	// exhaust its budget against a rolling sequence of Shared holders
	// that each individually released before it, none of which had any
	// signal that an exclusive acquirer was waiting.
	Wait time.Duration
	// PollInterval is the retry cadence while waiting (default 100ms).
	PollInterval time.Duration
	// Reason is recorded in the advisory holder-info sidecar on
	// exclusive acquisition so blocked commands can say who is holding
	// the gate and why. Ignored for shared mode.
	Reason string
	// OnWait, when set, is called once when the first attempt comes back
	// busy, with a human-readable description of the holder (best
	// effort, from the advisory sidecar). AcquireAll fires it at most
	// once across the whole gate set.
	OnWait func(holder string)
}

// Gate identifies one gate file. Construct via ForWorkspace or
// ForPhysicalRoot so the location and canonicalization rules stay uniform.
type Gate struct {
	path string
}

// Path returns the gate file location (diagnostics only — never lock this
// file through other means, and never delete it).
func (g Gate) Path() string { return g.path }

// gateKey derives the comparison key AcquireAll uses to dedupe and sort
// gate paths. On case-insensitive-capable filesystems (Windows, macOS
// default HFS+/APFS) two differently-cased spellings of the same gate
// path are the same file, so comparing raw paths would treat one physical
// gate as two, defeating both dedupe and the deadlock-free total order.
// Lowercasing on those platforms only widens the set of paths treated as
// equal; it never splits an otherwise-equal pair. Gate.path (the open
// path) is left untouched — only this comparison key is lowered.
func gateKey(path string) string {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.ToLower(path)
	}
	return path
}

// gateFileName maps a guarded directory base name to its sibling gate
// file name: ".beads" -> ".beads.gate.lock", "dolt" -> "dolt.gate.lock".
// The base is kept verbatim (no dot-stripping) so distinct sibling names
// can never collide on one gate file.
func gateFileName(base string) string {
	return base + ".gate.lock"
}

// forDir builds the gate for a guarded directory: the gate file sits in
// the directory's parent. The parent must exist; the guarded directory
// itself may not exist yet (bd init gates the workspace it is creating).
// The parent is canonicalized (symlinks resolved) so that every process
// that reaches the same physical parent agrees on one gate file, no
// matter which path spelling it used.
func forDir(dir string) (Gate, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Gate{}, fmt.Errorf("workspacegate: resolving %s: %w", dir, err)
	}
	abs = filepath.Clean(abs)

	// Gate identity must be stable across the guarded directory being
	// absent, created, replaced, or recreated — that is the point of
	// placing the gate beside it. So identity derives from the
	// canonicalized PARENT plus the literal base name, never from
	// resolving the guarded path itself: full-path resolution would
	// silently select a different gate once the directory appears as a
	// symlink, letting two exclusive holders coexist. The flip side is
	// that a guarded directory that IS a symlink has no stable identity
	// under this scheme, so it is refused outright rather than gated
	// ambiguously.
	if fi, err := os.Lstat(abs); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return Gate{}, fmt.Errorf("workspacegate: %s is a symlink; gate the physical directory it points to", abs)
	}
	parent, base := filepath.Split(abs)
	switch base {
	case "", ".", "..":
		return Gate{}, fmt.Errorf("workspacegate: cannot gate %q: the guarded path must be a named directory", dir)
	}
	canonParent, err := filepath.EvalSymlinks(filepath.Clean(parent))
	if err != nil {
		return Gate{}, fmt.Errorf("workspacegate: gate parent %s must exist: %w", parent, err)
	}
	return Gate{path: filepath.Join(canonParent, gateFileName(base))}, nil
}

// ForWorkspace returns the gate guarding a workspace's .beads directory
// (pass the .beads directory itself). The gate file is a sibling of
// .beads; bd's project gitignore management covers "*.gate.lock*".
func ForWorkspace(beadsDir string) (Gate, error) { return forDir(beadsDir) }

// ForPhysicalRoot returns the gate guarding a physical database root (a
// dolt server root such as .beads/dolt or ~/.beads/shared-server/dolt).
// Distinct workspaces that point at the same physical root resolve to the
// same gate file, which is the point: a workspace-level gate alone cannot
// stop workspace B from restarting the server workspace A is draining.
//
// Cross-user shared roots are unsupported: the gate file is created 0o600
// (see Acquire), so a second OS user attempting to gate a shared root such
// as ~/.beads/shared-server/dolt hits EACCES on the sibling gate file, not
// a graceful degradation. Do not widen the mode to 0o666 to work around
// this without an explicit owner decision — that would let any local user
// release or corrupt another user's gate.
//
// Derived invariant for an in-.beads physical root (e.g.
// .beads/embeddeddolt): the gate file for that root lives beside it,
// INSIDE .beads (see forDir), so callers must not replace or recreate
// .beads itself while such a root is gated — doing so replaces the gate
// file's parent along with everything else in it, which is exactly the
// split-inode hazard the package comment warns against for the guarded
// root's own parent.
func ForPhysicalRoot(root string) (Gate, error) { return forDir(root) }

// Info is the advisory holder-info sidecar written next to the gate file
// on exclusive acquisition. It is diagnostics only: the flock is the
// authority, and a stale sidecar (crashed holder) is ignored whenever the
// lock itself is free.
type Info struct {
	PID       int       `json:"pid"`
	Hostname  string    `json:"hostname,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	StartedAt time.Time `json:"started_at"`
}

func (g Gate) infoPath() string { return g.path + ".info" }

func (g Gate) readInfo() *Info {
	data, err := os.ReadFile(g.infoPath())
	if err != nil {
		return nil
	}
	var info Info
	if json.Unmarshal(data, &info) != nil {
		return nil
	}
	return &info
}

// busyDetail renders best-effort holder diagnostics for a failed
// acquisition in the given mode. The sidecar is written only by exclusive
// holders, so its trustworthiness depends on what blocked us:
//
//   - a SHARED attempt is blocked only by a live exclusive holder, so a
//     readable sidecar almost certainly describes it;
//   - an EXCLUSIVE attempt is blocked by shared holders (which write no
//     sidecar) just as often as by an exclusive one, and a sidecar left
//     by a crashed migration would then name a dead PID as the culprit —
//     so it is qualified, and dropped entirely when its process is
//     provably gone.
func (g Gate) busyDetail(mode Mode) string {
	info := g.readInfo()
	if info == nil {
		if mode == Exclusive {
			return "other bd processes (shared holders record no info)"
		}
		return "another bd process (no holder info recorded)"
	}
	desc := fmt.Sprintf("pid %d", info.PID)
	if info.Hostname != "" {
		desc += " on " + info.Hostname
	}
	if info.Reason != "" {
		desc += " (" + info.Reason + ")"
	}
	if !info.StartedAt.IsZero() {
		desc += " since " + info.StartedAt.UTC().Format(time.RFC3339)
	}
	stale := func() bool {
		host, _ := os.Hostname()
		return host == info.Hostname && !pidAlive(info.PID)
	}
	if mode == Shared {
		// A live exclusive holder is the only thing that blocks a SHARED
		// attempt, so a readable sidecar should describe it — but the
		// sidecar write and the flock are not atomic with each other, so
		// a dead recorded PID must still be reported as stale rather than
		// presented as fact.
		if stale() {
			return fmt.Sprintf("other bd processes (a stale exclusive-holder record from dead pid %d was ignored)", info.PID)
		}
		return desc
	}
	if stale() {
		return fmt.Sprintf("other bd processes (a stale exclusive-holder record from dead pid %d was ignored)", info.PID)
	}
	return "possibly " + desc + ", or shared holders"
}

// pidAlive reports best-effort process liveness via the zero-signal
// probe. Where the probe is unsupported it errs toward alive, which only
// makes the diagnostic more cautious (the stale record is then presented
// as "possibly" rather than dropped).
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		// Windows: FindProcess fails when no such process exists.
		return false
	}
	defer func() { _ = p.Release() }() // Windows: FindProcess opens a handle; release it on every return path.
	err = p.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
		return false
	}
	// EPERM (alive, not ours) and unsupported-platform errors land here.
	return true
}

// Handle is a held gate. Release it exactly when the store/provider it
// protects has closed; Release is idempotent.
type Handle struct {
	gate Gate
	mode Mode
	f    *os.File

	once sync.Once
	err  error
}

// Mode reports how this handle was acquired.
func (h *Handle) Mode() Mode { return h.mode }

// Release drops the lock and closes the file handle. For exclusive
// handles it also best-effort removes the holder-info sidecar. The gate
// file itself is intentionally never removed: deleting a lock file that
// another process is about to open reintroduces the split-inode race the
// gate location rules exist to avoid.
func (h *Handle) Release() error {
	if h == nil {
		return nil
	}
	h.once.Do(func() {
		var errs []error
		if h.mode == Exclusive {
			// Remove the sidecar BEFORE unlocking: after the unlock a new
			// exclusive holder may already have written its own sidecar,
			// and a late removal here would delete that holder's info.
			// Best effort; a leftover sidecar is ignored once the flock
			// is free.
			_ = os.Remove(h.gate.infoPath())
		}
		if err := lockfile.FlockUnlock(h.f); err != nil {
			errs = append(errs, fmt.Errorf("workspacegate: unlock %s: %w", h.gate.path, err))
		}
		if err := h.f.Close(); err != nil {
			errs = append(errs, fmt.Errorf("workspacegate: close %s: %w", h.gate.path, err))
		}
		h.err = errors.Join(errs...)
	})
	return h.err
}

// Acquire takes the gate in the given mode, polling until Options.Wait is
// exhausted or ctx is done. The returned handle's file descriptor is not
// inherited by spawned children (Go opens files close-on-exec on Unix and
// non-inheritable on Windows), so a dolt child outliving its bd parent
// does not keep the gate held.
func (g Gate) Acquire(ctx context.Context, mode Mode, opts Options) (*Handle, error) {
	if g.path == "" {
		return nil, errors.New("workspacegate: zero Gate; use ForWorkspace/ForPhysicalRoot")
	}
	// Tolerate a nil context rather than panicking on ctx.Err below: gate
	// acquisition sits on CLI plumbing paths (cobra hooks, migrate helpers)
	// that tests and embedders invoke directly without the process-level
	// signal context, and a nil-deref here kills the whole test binary.
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("workspacegate: acquiring %s: %w", g.path, err)
	}
	poll := opts.PollInterval
	if poll <= 0 {
		poll = 100 * time.Millisecond
	}
	deadline := time.Now().Add(opts.Wait)

	f, err := os.OpenFile(g.path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("workspacegate: open gate %s: %w", g.path, err)
	}

	try := lockfile.FlockSharedNonBlock
	if mode == Exclusive {
		try = lockfile.FlockExclusiveNonBlock
	}

	notified := false
	for {
		err := try(f)
		if err == nil {
			h := &Handle{gate: g, mode: mode, f: f}
			if mode == Exclusive {
				g.writeInfo(opts.Reason)
			}
			return h, nil
		}
		if !errors.Is(err, lockfile.ErrLockBusy) && !lockfile.IsLocked(err) {
			_ = f.Close()
			return nil, fmt.Errorf("workspacegate: lock %s: %w", g.path, err)
		}
		if !notified {
			notified = true
			if opts.OnWait != nil {
				opts.OnWait(g.busyDetail(mode))
			}
		}
		remaining := time.Until(deadline)
		if opts.Wait <= 0 || remaining <= 0 {
			_ = f.Close()
			return nil, fmt.Errorf("workspacegate: %s (%s mode) held by %s: %w",
				g.path, mode, g.busyDetail(mode), ErrBusy)
		}
		// Never sleep past the wait budget: a Wait shorter than the poll
		// interval must still come back within (about) Wait, and the
		// deadline is re-checked above before any further attempt.
		sleep := poll
		if remaining < sleep {
			sleep = remaining
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, fmt.Errorf("workspacegate: waiting for %s: %w", g.path, ctx.Err())
		case <-time.After(sleep):
		}
	}
}

// writeInfo records the advisory exclusive-holder sidecar. Failures are
// deliberately swallowed: diagnostics must never block the operation that
// already holds the authoritative lock. The write goes to an O_EXCL temp
// file renamed into place, which (a) never follows a pre-planted symlink
// at either path — plain WriteFile would truncate the symlink's target —
// and (b) is atomic, so concurrent readers cannot see torn JSON.
func (g Gate) writeInfo(reason string) {
	host, _ := os.Hostname()
	data, err := json.Marshal(Info{
		PID:       os.Getpid(),
		Hostname:  host,
		Reason:    reason,
		StartedAt: time.Now().UTC(),
	})
	if err != nil {
		return
	}
	tmp := fmt.Sprintf("%s.%d.tmp", g.infoPath(), os.Getpid())
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // G304: path derives from the gate location this package computed, not request input; the preceding os.Remove clears a pre-planted file, and O_EXCL closes the remove-then-open window so a symlink replanted in between is refused rather than followed
	if err != nil {
		return
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp)
		return
	}
	// Rename replaces a symlink itself rather than following it.
	if err := os.Rename(tmp, g.infoPath()); err != nil {
		// Windows can refuse to replace an existing file; retry once
		// after removing the destination. Still best effort.
		_ = os.Remove(g.infoPath())
		if err := os.Rename(tmp, g.infoPath()); err != nil {
			_ = os.Remove(tmp)
		}
	}
}

// MultiHandle holds several gates acquired by AcquireAll and releases
// them in reverse acquisition order.
type MultiHandle struct {
	handles []*Handle
}

// Release drops every held gate in reverse order. Idempotent.
func (m *MultiHandle) Release() error {
	if m == nil {
		return nil
	}
	var errs []error
	for i := len(m.handles) - 1; i >= 0; i-- {
		if err := m.handles[i].Release(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// AcquireAll takes several gates in one mode, always in sorted
// canonical-path order so that every process acquiring overlapping gate
// sets uses the same total order and cannot deadlock against another
// AcquireAll caller. Duplicate gates (workspace and physical root
// resolving to the same file) collapse to one acquisition. On any
// failure, gates already acquired are released in reverse order.
//
// Options.Wait is a TOTAL budget across the whole set, not per gate, and
// Options.OnWait fires at most once. Gates rank before every other beads
// lock: callers must call AcquireAll before touching migrate.lock, the
// init lock, proxy locks, the dolt-server start lock, or schema GET_LOCK,
// and release it after those.
func AcquireAll(ctx context.Context, mode Mode, opts Options, gates ...Gate) (*MultiHandle, error) {
	// See Acquire: nil contexts are normalized, not dereferenced.
	if ctx == nil {
		ctx = context.Background()
	}
	uniq := make(map[string]Gate, len(gates))
	for _, g := range gates {
		if g.path == "" {
			return nil, errors.New("workspacegate: zero Gate in AcquireAll")
		}
		uniq[gateKey(g.path)] = g
	}
	ordered := make([]Gate, 0, len(uniq))
	for _, g := range uniq {
		ordered = append(ordered, g)
	}
	sort.Slice(ordered, func(i, j int) bool { return gateKey(ordered[i].path) < gateKey(ordered[j].path) })

	perGate := opts
	if opts.OnWait != nil {
		var once sync.Once
		cb := opts.OnWait
		perGate.OnWait = func(holder string) { once.Do(func() { cb(holder) }) }
	}
	deadline := time.Now().Add(opts.Wait)

	m := &MultiHandle{handles: make([]*Handle, 0, len(ordered))}
	for _, g := range ordered {
		if opts.Wait > 0 {
			perGate.Wait = time.Until(deadline)
			if perGate.Wait < 0 {
				perGate.Wait = 0
			}
		}
		h, err := g.Acquire(ctx, mode, perGate)
		if err != nil {
			rerr := m.Release()
			return nil, errors.Join(err, rerr)
		}
		m.handles = append(m.handles, h)
	}
	return m, nil
}

// ExclusiveHolder reports whether an exclusive holder currently holds the
// gate, with its advisory info when available. It is a diagnostic (for
// doctor and error paths): the check momentarily takes a SHARED lock, so
// it cannot disturb other shared holders, but a concurrent fail-fast
// EXCLUSIVE acquirer could transiently observe the gate as busy — never
// call this in a hot loop next to maintenance operations. It cannot
// distinguish "free" from "held shared", deliberately: that distinction
// would require a transient exclusive probe, which measurably injects
// spurious failures into concurrent fail-fast acquirers.
//
// The error return is non-nil when the state could not be determined
// (unreadable gate file, unsupported filesystem); callers must not treat
// that as "not held".
func (g Gate) ExclusiveHolder() (held bool, info *Info, err error) {
	if g.path == "" {
		return false, nil, errors.New("workspacegate: zero Gate")
	}
	// O_RDONLY: this is a read-only probe (flock does not require a
	// writable descriptor), and it widens reach — a gate file owned by
	// another user with no write permission for us is still probeable.
	f, err := os.OpenFile(g.path, os.O_RDONLY, 0o600)
	if err != nil {
		if os.IsNotExist(err) {
			// No gate file: nothing has ever gated here.
			return false, nil, nil
		}
		return false, nil, fmt.Errorf("workspacegate: probe %s: %w", g.path, err)
	}
	defer f.Close()

	if lerr := lockfile.FlockSharedNonBlock(f); lerr == nil {
		_ = lockfile.FlockUnlock(f)
		return false, nil, nil
	} else if !errors.Is(lerr, lockfile.ErrLockBusy) && !lockfile.IsLocked(lerr) {
		return false, nil, fmt.Errorf("workspacegate: probe %s: %w", g.path, lerr)
	}
	return true, g.readInfo(), nil
}
