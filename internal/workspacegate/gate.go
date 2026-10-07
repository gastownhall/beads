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
	"strconv"
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
	// Fairness: a waiting Shared acquisition (Options.Wait > 0) defers to a
	// queued Exclusive acquirer — see "Writer fairness" on Acquire — so a
	// rolling sequence of short shared holders cannot starve maintenance.
	// A fail-fast Shared attempt (Wait <= 0) does not consult the queue.
	Shared Mode = iota
	// Exclusive is held by maintenance operations. It conflicts with
	// every other holder, shared or exclusive. A waiting Exclusive
	// acquisition publishes its intent so new waiting Shared acquisitions
	// queue behind it (see Acquire).
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
	// attempt. Zero or negative means exactly one non-blocking try (which
	// also opts out of the writer-fairness queue in both directions: a
	// fail-fast Exclusive publishes no intent, and a fail-fast Shared
	// ignores published intent). The underlying blocking lock primitives
	// have no deadline support, so waiting is implemented as timed polling
	// of the non-blocking primitive.
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
	// IgnoreQueuedExclusive makes a waiting Shared acquisition skip the
	// writer-fairness check. Set it only when an ANCESTOR process already
	// holds this gate shared (see InheritedSharedHold): the queued
	// exclusive acquirer cannot proceed until that ancestor releases, and
	// the ancestor may be waiting on this process, so queueing would turn
	// a nested bd invocation into a deadlock that only the wait bound
	// breaks. Ignored for Exclusive.
	IgnoreQueuedExclusive bool
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

// intentPath is the writer-fairness lock: a waiting Exclusive acquirer
// holds it exclusively while it polls the gate, and waiting Shared
// acquirers defer while it is held. intentInfoPath is its advisory
// holder-info sidecar (a separate file because Windows LockFileEx locks
// are mandatory byte-range locks: other processes cannot read a locked
// file's contents). Both match the "*.gate.lock*" gitignore pattern.
func (g Gate) intentPath() string     { return g.path + ".intent" }
func (g Gate) intentInfoPath() string { return g.path + ".intent.info" }

func (g Gate) readInfo() *Info { return readInfoAt(g.infoPath()) }

func readInfoAt(path string) *Info {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path derives from the gate location this package computed
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
	desc := info.describe()
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

// describe renders a holder record as "pid N on host (reason) since T".
func (info *Info) describe() string {
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
	return desc
}

// queuedDetail describes the queued exclusive acquirer a waiting Shared
// attempt is deferring to. The intent lock is held whenever this is
// called, so the sidecar (written right after the lock is taken) normally
// names a live process; it is only absent in the brief window before the
// write or when the write failed.
func (g Gate) queuedDetail() string {
	info := readInfoAt(g.intentInfoPath())
	if info != nil {
		// A sidecar whose removal failed (Windows refuses to delete a file
		// another process has open) can outlive its writer; never present
		// a dead process as the queued operation.
		if host, _ := os.Hostname(); host == info.Hostname && !pidAlive(info.PID) {
			info = nil
		}
	}
	if info == nil {
		return "a bd maintenance operation queued for exclusive access"
	}
	return "a bd maintenance operation queued for exclusive access: " + info.describe()
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
//
// Writer fairness. flock and LockFileEx grant no queueing guarantees, and
// acquisition is non-blocking polling anyway, so without help a steady
// stream of short shared holders — each overlapping the next — would keep
// an Exclusive waiter out until its budget ran dry. A second lock file,
// the intent lock (<gate>.intent), fixes that:
//
//   - a waiting Exclusive acquisition (Wait > 0) whose first attempt is
//     busy takes the intent lock EXCLUSIVELY (non-blocking, retried each
//     poll), holds it while it polls the gate, and drops it as soon as it
//     owns the gate, gives up, or has held it for maxIntentHold without
//     getting in (after which it keeps polling unqueued);
//   - a waiting Shared acquisition (Wait > 0) checks the intent lock before
//     every attempt but the last (a momentary non-blocking SHARED probe)
//     and, while it is held, does not touch the gate — it waits, within its
//     own budget, exactly as if the gate were held exclusively. Its final
//     attempt tries the gate regardless.
//
// The two bounds keep the queue from ever costing availability. A queue can
// DELAY a shared acquirer but never FAIL it: the final attempt ignores
// intent, so a shared acquisition fails only when the gate itself is held
// exclusively. And a doomed exclusive waiter — one blocked behind a shared
// holder that will not leave soon (a --watch, a tail --follow, an open
// editor, a long-lived embedder) — stops queueing everyone else after
// maxIntentHold instead of for its whole budget. In-flight ordinary commands
// drain far faster than that, so the fairness win is kept.
//
// Queued exclusive operations still run one after another: while they take
// turns on the gate, a waiting shared acquirer waits for all of them (each
// new exclusive waiter re-publishes intent), bounded by its own budget.
//
// Shared holders that are already in keep running; the exclusive waiter
// waits only for them to drain, so its wait is bounded by the longest
// in-flight command rather than by the arrival rate. A shared acquirer that
// probed just before the intent was published can still slip in once, which
// costs at most one more command's duration.
//
// Stale intent cannot outlive its owner: it is an OS advisory lock, released
// by the kernel when the process exits or crashes (on every platform), never
// a marker file whose existence means anything. The intent file and its
// holder-info sidecar may linger; both are ignored whenever the lock itself
// is free. Every intent-lock failure other than "busy" (unsupported
// filesystem, permissions) degrades to the pre-fairness behavior instead of
// failing the acquisition: fairness is advisory, the gate is the authority.
//
// Lock order is unchanged in substance: the intent lock of a gate is taken
// only while acquiring that gate, so it slots directly before the gate in
// AcquireAll's sorted total order and cannot form a cycle.
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
	waiting := opts.Wait > 0
	defersToIntent := waiting && mode == Shared && !opts.IgnoreQueuedExclusive
	publishesIntent := waiting && mode == Exclusive
	var intentSince time.Time

	// intent is the writer-fairness lock while this Exclusive acquisition
	// holds it. Released on every return path — after writeInfo on success,
	// so a shared waiter never sees the gate unqueued-but-unclaimed.
	var intent *os.File
	defer func() { g.releaseIntent(intent) }()

	notified := false
	// finalAttempt is set once the budget ran out while this acquisition was
	// deferring to queued intent: it then gets exactly one more try at the
	// gate, ignoring the queue, before it may fail. Deciding finality from
	// the clock at the top of an iteration is not enough — the intent probe
	// itself takes time (a loaded machine can stall it for milliseconds), so
	// "budget not yet spent" before the probe can be "spent" after it, and
	// failing there would let a mere queue fail a shared acquirer.
	finalAttempt := false
	for {
		var detail string
		deferred := defersToIntent && !finalAttempt && time.Until(deadline) > 0 && g.ExclusiveQueued()
		if deferred {
			detail = g.queuedDetail()
			if testHookAfterIntentDefer != nil {
				testHookAfterIntentDefer()
			}
		} else {
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
			detail = g.busyDetail(mode)
		}
		if !notified {
			notified = true
			if opts.OnWait != nil {
				opts.OnWait(detail)
			}
		}
		remaining := time.Until(deadline)
		if deferred && remaining <= 0 {
			// Queued maintenance may delay a shared acquirer, never fail
			// it: the budget is spent, so try the gate itself once more.
			finalAttempt = true
			continue
		}
		if !waiting || remaining <= 0 {
			_ = f.Close()
			return nil, fmt.Errorf("workspacegate: %s (%s mode) held by %s: %w",
				g.path, mode, detail, ErrBusy)
		}
		if intent != nil && time.Since(intentSince) >= maxIntentHold {
			// Doomed or very slow: stop holding everyone else back.
			g.releaseIntent(intent)
			intent = nil
			publishesIntent = false
		}
		if publishesIntent && intent == nil {
			if intent = g.tryTakeIntent(opts.Reason); intent != nil {
				intentSince = time.Now()
			}
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

// testHookAfterIntentDefer, when set (tests only), runs right after a
// waiting shared acquisition decides to defer to queued intent, so tests can
// stall it past its deadline at exactly the point a loaded machine might.
var testHookAfterIntentDefer func()

// maxIntentHold caps how long one Exclusive acquisition keeps the intent
// lock without getting the gate. It must stay well below the shared wait
// budget callers use (bd: BEADS_GATE_WAIT_TIMEOUT, default 30s) and well
// above how long ordinary in-flight commands take to drain. A var so tests
// can shorten it.
var maxIntentHold = 10 * time.Second

// ExclusiveQueued reports whether a waiting Exclusive acquirer currently
// holds this gate's intent lock. The probe opens read-only and treats a
// missing file as "no intent", so ordinary shared commands never create
// intent files; any error other than "busy" reads as "not queued" (see the
// degradation rule on Acquire).
func (g Gate) ExclusiveQueued() bool {
	f, err := os.OpenFile(g.intentPath(), os.O_RDONLY, 0o600)
	if err != nil {
		return false
	}
	defer f.Close()
	lerr := lockfile.FlockSharedNonBlock(f)
	if lerr == nil {
		_ = lockfile.FlockUnlock(f)
		return false
	}
	return errors.Is(lerr, lockfile.ErrLockBusy) || lockfile.IsLocked(lerr)
}

// tryTakeIntent makes one non-blocking attempt at the intent lock and, on
// success, records the advisory intent sidecar. It returns nil when the
// lock is busy (another exclusive waiter is queued first; this acquirer
// keeps polling the gate and retries the intent next round) or unusable.
func (g Gate) tryTakeIntent(reason string) *os.File {
	f, err := os.OpenFile(g.intentPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil
	}
	if err := lockfile.FlockExclusiveNonBlock(f); err != nil {
		_ = f.Close()
		return nil
	}
	writeInfoAt(g.intentInfoPath(), reason)
	return f
}

// releaseIntent drops a held intent lock (nil-safe). As with the gate's own
// sidecar, the intent sidecar is removed BEFORE unlocking so a late removal
// can never delete the next waiter's record.
func (g Gate) releaseIntent(f *os.File) {
	if f == nil {
		return
	}
	_ = os.Remove(g.intentInfoPath())
	_ = lockfile.FlockUnlock(f)
	_ = f.Close()
}

// writeInfo records the advisory exclusive-holder sidecar.
func (g Gate) writeInfo(reason string) { writeInfoAt(g.infoPath(), reason) }

// writeInfoAt writes an advisory holder-info sidecar. Failures are
// deliberately swallowed: diagnostics must never block the operation that
// already holds the authoritative lock. The write goes to an O_EXCL temp
// file renamed into place, which (a) never follows a pre-planted symlink
// at either path — plain WriteFile would truncate the symlink's target —
// and (b) is atomic, so concurrent readers cannot see torn JSON.
func writeInfoAt(path, reason string) {
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
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
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
	if err := os.Rename(tmp, path); err != nil {
		// Windows can refuse to replace an existing file; retry once
		// after removing the destination. Still best effort.
		_ = os.Remove(path)
		if err := os.Rename(tmp, path); err != nil {
			_ = os.Remove(tmp)
		}
	}
}

// InheritedHoldEnv names the environment variable a bd process sets (to its
// PID) while it holds its command's gates SHARED, so that bd processes it
// spawns — `bd orphans` running `bd close`, git hooks under a bd-driven
// commit — can tell that an ancestor already holds the gate. See
// Options.IgnoreQueuedExclusive and InheritedSharedHold.
const InheritedHoldEnv = "BEADS_GATE_SHARED_HOLDER_PID"

// InheritedSharedHold reports whether InheritedHoldEnv names a live process
// other than this one, i.e. whether this process was (probably) spawned by a
// bd command that still holds its gates shared. A stale or recycled PID only
// costs fairness, never safety: the flag merely skips the writer queue, and
// the gate itself still excludes every exclusive holder.
func InheritedSharedHold() bool {
	raw := strings.TrimSpace(os.Getenv(InheritedHoldEnv))
	if raw == "" {
		return false
	}
	pid, err := strconv.Atoi(raw)
	if err != nil || pid == os.Getpid() {
		return false
	}
	return pidAlive(pid)
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
			// An exhausted budget leaves later gates one non-blocking
			// attempt (Wait 0), which ignores the writer-fairness queue —
			// the same as the final attempt of a waiting acquisition.
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
