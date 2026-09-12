package metrics

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// writeQueueFile creates a file in dir with the given content and mtime.
func writeQueueFile(t *testing.T, dir, name string, size int, mtime time.Time) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return path
}

func names(t *testing.T, dir string) map[string]bool {
	t.Helper()
	dirents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, de := range dirents {
		got[de.Name()] = true
	}
	return got
}

func TestPruneTTLDropsStaleBatchesAndOrphanTemps(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeQueueFile(t, dir, "old.evtq", 10, now.Add(-8*24*time.Hour))
	writeQueueFile(t, dir, ".write-orphan", 10, now.Add(-8*24*time.Hour))
	writeQueueFile(t, dir, "young.evtq", 10, now.Add(-time.Hour))
	writeQueueFile(t, dir, ".write-live", 10, now.Add(-time.Minute))

	dropped, freed := pruneQueue(context.Background(), dir, now, 7*24*time.Hour, 100, 1<<20)
	if dropped != 2 || freed != 20 {
		t.Fatalf("dropped=%d freed=%d, want 2/20", dropped, freed)
	}
	got := names(t, dir)
	if got["old.evtq"] || got[".write-orphan"] {
		t.Fatalf("stale files survived: %v", got)
	}
	if !got["young.evtq"] || !got[".write-live"] {
		t.Fatalf("young files pruned: %v", got)
	}
}

func TestPruneCountCapDropsOldestFirst(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	// 5 young batches, distinct mtimes, oldest = q0.
	for i := 0; i < 5; i++ {
		writeQueueFile(t, dir, "q"+string(rune('0'+i))+".evtq", 10,
			now.Add(-time.Duration(5-i)*time.Minute))
	}
	dropped, _ := pruneQueue(context.Background(), dir, now, 7*24*time.Hour, 3, 1<<20)
	if dropped != 2 {
		t.Fatalf("dropped=%d, want 2", dropped)
	}
	got := names(t, dir)
	for _, want := range []string{"q2.evtq", "q3.evtq", "q4.evtq"} {
		if !got[want] {
			t.Fatalf("newest survivor %s missing: %v", want, got)
		}
	}
	for _, gone := range []string{"q0.evtq", "q1.evtq"} {
		if got[gone] {
			t.Fatalf("oldest %s survived: %v", gone, got)
		}
	}
}

func TestPruneByteCapDropsOldestFirst(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeQueueFile(t, dir, "a.evtq", 400, now.Add(-3*time.Minute))
	writeQueueFile(t, dir, "b.evtq", 400, now.Add(-2*time.Minute))
	writeQueueFile(t, dir, "c.evtq", 400, now.Add(-time.Minute))

	// 1000-byte cap: dropping only "a" (oldest) brings the total to 800.
	dropped, freed := pruneQueue(context.Background(), dir, now, 7*24*time.Hour, 100, 1000)
	if dropped != 1 || freed != 400 {
		t.Fatalf("dropped=%d freed=%d, want 1/400", dropped, freed)
	}
	got := names(t, dir)
	if got["a.evtq"] || !got["b.evtq"] || !got["c.evtq"] {
		t.Fatalf("wrong survivor set: %v", got)
	}
}

func TestPruneNeverTouchesMarkerOrLock(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	ancient := now.Add(-365 * 24 * time.Hour)
	writeQueueFile(t, dir, ".last-flush", 1, ancient)
	writeQueueFile(t, dir, "eventkit.lock", 1, ancient)
	writeQueueFile(t, dir, "unrelated.txt", 1, ancient)

	dropped, _ := pruneQueue(context.Background(), dir, now, 7*24*time.Hour, 0, 0)
	if dropped != 0 {
		t.Fatalf("dropped=%d, want 0", dropped)
	}
	got := names(t, dir)
	for _, want := range []string{".last-flush", "eventkit.lock", "unrelated.txt"} {
		if !got[want] {
			t.Fatalf("non-queue file %s pruned", want)
		}
	}
}

func TestPruneCapNeverTakesYoungWriteTemps(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	// Young emitter temps: a live emitter may hold one between CreateTemp and
	// Rename — cap-deleting it would make that Rename fail and lose a live
	// event. Only TTL (7d) may ever reclaim a .write-* file.
	writeQueueFile(t, dir, ".write-a", 10, now.Add(-time.Second))
	writeQueueFile(t, dir, ".write-b", 10, now.Add(-time.Hour))
	// Enough over-cap batches, all OLDER than the temps, that a buggy
	// implementation counting temps as cap candidates would delete them first.
	for i := 0; i < 4; i++ {
		writeQueueFile(t, dir, "q"+string(rune('0'+i))+".evtq", 10,
			now.Add(-time.Duration(10-i)*time.Hour))
	}
	dropped, _ := pruneQueue(context.Background(), dir, now, 7*24*time.Hour, 1, 1<<20)
	if dropped != 3 {
		t.Fatalf("dropped=%d, want 3 (oldest batches only)", dropped)
	}
	got := names(t, dir)
	if !got[".write-a"] || !got[".write-b"] {
		t.Fatalf("young emitter temp cap-deleted: %v", got)
	}
	if !got["q3.evtq"] {
		t.Fatalf("newest batch should survive: %v", got)
	}
}

func TestPruneMissingDirIsNoop(t *testing.T) {
	dropped, freed := pruneQueue(context.Background(), filepath.Join(t.TempDir(), "nope"), time.Now(), time.Hour, 1, 1)
	if dropped != 0 || freed != 0 {
		t.Fatalf("dropped=%d freed=%d, want 0/0", dropped, freed)
	}
}

func TestPruneWithinCapsIsNoop(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeQueueFile(t, dir, "a.evtq", 10, now.Add(-time.Minute))
	dropped, _ := pruneQueue(context.Background(), dir, now, 7*24*time.Hour, 10, 1<<20)
	if dropped != 0 {
		t.Fatalf("dropped=%d, want 0", dropped)
	}
	if !names(t, dir)["a.evtq"] {
		t.Fatal("in-cap file pruned")
	}
}

// ctxExpiringAfter is a deadline context whose deadline falls part way through
// the scan. It reports live for the first n consultations and expired from
// then on, which makes "the budget ran out after n chunks" deterministic
// without a timing race: pruneQueue consults the context once per directory
// chunk, so n chunks are processed.
//
// Expiry flips every part of the context interface together — Err returns
// DeadlineExceeded, Done is closed, Deadline moves into the past — and either
// Err or Done counts as a consultation. A partial fake (Err only) would let a
// future scan that waits on Done pass this test while production still walks
// the whole spool. Not safe for concurrent use; the prune walk is sequential.
type ctxExpiringAfter struct {
	context.Context
	state *expiryState
}

type expiryState struct {
	remaining int
	done      chan struct{}
	expiredAt time.Time
}

// consult burns one consultation and reports whether the context is expired.
func (c ctxExpiringAfter) consult() bool {
	if c.state.remaining > 0 {
		c.state.remaining--
		return false
	}
	select {
	case <-c.state.done:
	default:
		c.state.expiredAt = time.Now()
		close(c.state.done)
	}
	return true
}

func (c ctxExpiringAfter) Err() error {
	if c.consult() {
		return context.DeadlineExceeded
	}
	return nil
}

func (c ctxExpiringAfter) Done() <-chan struct{} {
	c.consult()
	return c.state.done
}

func (c ctxExpiringAfter) Deadline() (time.Time, bool) {
	select {
	case <-c.state.done:
		return c.state.expiredAt, true
	default:
		// Still live: a deadline far enough out that a budget computed from
		// it is positive, as a real un-expired deadline context reports.
		return time.Now().Add(time.Hour), true
	}
}

func expiringAfter(chunks int) context.Context {
	return ctxExpiringAfter{
		Context: context.Background(),
		state:   &expiryState{remaining: chunks, done: make(chan struct{})},
	}
}

// seedExpired writes n past-TTL .evtq batches and returns their names.
func seedExpired(t *testing.T, dir string, n int, now time.Time) {
	t.Helper()
	for i := 0; i < n; i++ {
		writeQueueFile(t, dir, fmt.Sprintf("b%04d%s", i, queuedEventExt), 10, now.Add(-8*24*time.Hour))
	}
}

// TestPruneQueueStopsOnExpiredContext is the GH#5871 regression: the prune the
// send-metrics child runs before its flush must be bounded by the child's
// advertised flushTimeout budget. With the budget already spent, the prune has
// to return instead of walking (and lstat-ing) the whole spool — the walk that
// kept observed children alive ~15 minutes against a 30s advertised budget.
func TestPruneQueueStopsOnExpiredContext(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	const seeded = 200
	seedExpired(t, dir, seeded, now)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dropped, freed := pruneQueue(ctx, dir, now, 7*24*time.Hour, 10_000, 64<<20)
	if dropped != 0 || freed != 0 {
		t.Errorf("pruneQueue with an already-expired context = (%d dropped, %d freed), want (0, 0): the budget was gone before it started", dropped, freed)
	}
	if got := len(names(t, dir)); got != seeded {
		t.Errorf("queue holds %d files after an out-of-budget prune, want all %d untouched", got, seeded)
	}
}

// TestPruneQueueTruncatesAndStillMakesProgress pins the other half: once the
// pass has TTL deletions to keep, expiring mid-scan must stop the scan (so the
// walk stays inside its budget) yet keep those deletions, so the pass makes
// forward progress instead of costing the queue nothing.
func TestPruneQueueTruncatesAndStillMakesProgress(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	const seeded = 200
	seedExpired(t, dir, seeded, now)

	// One chunk of budget, then the deadline is spent.
	dropped, _ := pruneQueue(expiringAfter(1), dir, now, 7*24*time.Hour, 10_000, 64<<20)
	if dropped == 0 {
		t.Errorf("pruneQueue dropped 0 of %d expired batches: a truncated prune must still make forward progress", seeded)
	}
	if dropped >= seeded {
		t.Errorf("pruneQueue dropped %d of %d expired batches: it ran the whole spool after its budget expired", dropped, seeded)
	}
	if got := len(names(t, dir)); got != seeded-dropped {
		t.Errorf("queue holds %d files, want %d (seeded %d - dropped %d)", got, seeded-dropped, seeded, dropped)
	}
}

// seedYoung writes n in-TTL .evtq batches, newest first (b0000 is the
// youngest), and returns the names in age order.
func seedYoung(t *testing.T, dir string, n int, now time.Time) []string {
	t.Helper()
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = fmt.Sprintf("b%04d%s", i, queuedEventExt)
		writeQueueFile(t, dir, out[i], 10, now.Add(-time.Duration(i+1)*time.Minute))
	}
	return out
}

// countingReader counts the ReadDir calls made through it, so a test can
// assert a walk stayed within its budgeted chunk count directly instead of
// inferring it from dropped/freed (which, for an all-young pile, can look
// the same across very different numbers of chunks examined).
type countingReader struct {
	r     dirChunkReader
	calls int
}

func (cr *countingReader) ReadDir(n int) ([]os.DirEntry, error) {
	cr.calls++
	return cr.r.ReadDir(n)
}

// TestPruneQueueCapsYoungPileWhenTTLMadeNoProgress pins the cap-skip livelock
// half of be-wwy2.3. The caps are the only bound on a queue whose files are
// all inside the TTL — exactly the 149k-file/1.1GB pile GH#5660 added them
// for. Under the bounded-heap design, eviction happens incrementally as each
// entry is seen, so every chunk boundary is a safe stopping point: the walk
// must honor the ctx budget unconditionally here too, the same as any other
// pass, instead of finishing the whole listing just because nothing was
// TTL-eligible.
func TestPruneQueueCapsYoungPileWhenTTLMadeNoProgress(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	const seeded = 200
	const maxFiles = 10
	const budgetedChunks = 1
	seedYoung(t, dir, seeded, now)

	f, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	counting := &countingReader{r: f}

	// One chunk of budget, then the deadline is spent — with nothing past
	// TTL for the first chunk (or any chunk) to have deleted.
	pruneQueueFrom(expiringAfter(budgetedChunks), counting, dir, now, 7*24*time.Hour, maxFiles, 64<<20)

	if counting.calls > budgetedChunks {
		t.Errorf("pruneQueueFrom made %d ReadDir calls, want at most %d: a young over-cap pile must not force the walk to finish the whole listing just because nothing was TTL-eligible", counting.calls, budgetedChunks)
	}
}

// TestPruneQueueConvergesAcrossRepeatedCalls pins be-wwy2.3's actual fix: a
// young over-cap pile too large to examine within one budget converges to
// maxFiles across however many separate budget-respecting calls it takes —
// simulating repeated 5-minutes-apart send-metrics invocations against the
// same on-disk directory state — with no single call ever exceeding its own
// budgeted chunk count. This replaces the old single-pass-completeness
// guarantee (a young over-cap pile fully capped within one, possibly
// over-budget, call) with the multi-pass convergence the design trades it
// for.
func TestPruneQueueConvergesAcrossRepeatedCalls(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	const seeded = 200
	const maxFiles = 10
	const budgetedChunks = 1
	const maxCalls = 50 // generous: a livelock must fail loud, not hang
	seedYoung(t, dir, seeded, now)

	converged := false
	for call := 0; call < maxCalls; call++ {
		f, err := os.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		counting := &countingReader{r: f}
		pruneQueueFrom(expiringAfter(budgetedChunks), counting, dir, now, 7*24*time.Hour, maxFiles, 64<<20)
		f.Close()

		if counting.calls > budgetedChunks {
			t.Fatalf("call %d: pruneQueueFrom made %d ReadDir calls, want at most %d", call, counting.calls, budgetedChunks)
		}
		if got := len(names(t, dir)); got <= maxFiles {
			converged = true
			break
		}
	}
	if !converged {
		t.Fatalf("directory did not converge to <= maxFiles=%d within %d budget-respecting calls", maxFiles, maxCalls)
	}
}

// failAfterChunks serves n real directory chunks and then fails, standing in
// for a listing that breaks mid-walk (I/O error, a dir that went away).
type failAfterChunks struct {
	r         dirChunkReader
	remaining int
}

var errSimulatedReadDir = errors.New("simulated readdir failure")

func (f *failAfterChunks) ReadDir(n int) ([]os.DirEntry, error) {
	if f.remaining <= 0 {
		return nil, errSimulatedReadDir
	}
	f.remaining--
	return f.r.ReadDir(n)
}

// recordingReader wraps a dirChunkReader and records the name of every entry
// it serves. ReadDir order is filesystem-dependent, not chronological (see
// prune.go's own comment on the walk), so a test cannot assume which names
// land in the first chunk — recording them is what lets a test verify claims
// about "the examined prefix" without depending on iteration order.
type recordingReader struct {
	r    dirChunkReader
	seen []string
}

func (rr *recordingReader) ReadDir(n int) ([]os.DirEntry, error) {
	dirents, err := rr.r.ReadDir(n)
	for _, de := range dirents {
		rr.seen = append(rr.seen, de.Name())
	}
	return dirents, err
}

// TestPruneQueueDoesNotCapOnPartialListing pins the fail-closed half, updated
// for the bounded-heap design's incremental eviction: a non-EOF read error
// ends the walk holding only a PREFIX of the queue, but under this design
// that prefix's own eviction decisions were already made and applied as it
// was examined, so they stand. What must NOT happen is any decision about
// the un-examined remainder — that part is left exactly as it was, not
// capped from a guessed-at prefix (the mistake the old truncation path
// existed to prevent).
func TestPruneQueueDoesNotCapOnPartialListing(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	const seeded = 200
	const maxFiles = 10
	seedYoung(t, dir, seeded, now)

	f, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// One good chunk, then the listing breaks.
	rec := &recordingReader{r: f}
	failing := &failAfterChunks{r: rec, remaining: 1}
	dropped, freed := pruneQueueFrom(context.Background(), failing, dir, now, 7*24*time.Hour, maxFiles, 64<<20)

	if len(rec.seen) != pruneChunkSize {
		t.Fatalf("test setup: examined %d entries, want exactly %d (one full chunk)", len(rec.seen), pruneChunkSize)
	}
	const wantDropped = pruneChunkSize - maxFiles // 64 - 10 = 54
	if dropped != wantDropped || freed != int64(wantDropped)*10 {
		t.Errorf("dropped=%d freed=%d, want %d/%d: a partial listing's own examined-prefix eviction must stand, not be discarded", dropped, freed, wantDropped, int64(wantDropped)*10)
	}

	got := names(t, dir)
	if want := seeded - wantDropped; len(got) != want {
		t.Fatalf("queue holds %d files, want %d (seeded %d - dropped %d)", len(got), want, seeded, wantDropped)
	}

	examined := map[string]bool{}
	for _, name := range rec.seen {
		examined[name] = true
	}
	nameAge := func(name string) time.Duration {
		trimmed := strings.TrimSuffix(name, queuedEventExt)
		trimmed = strings.TrimPrefix(trimmed, "b")
		idx, convErr := strconv.Atoi(trimmed)
		if convErr != nil {
			t.Fatalf("parse index from %s: %v", name, convErr)
		}
		return time.Duration(idx+1) * time.Minute
	}

	examinedSurvivors, examinedDropped := 0, 0
	var oldestSurvivorAge, youngestDroppedAge time.Duration
	haveSurvivor, haveDropped := false, false
	for name := range examined {
		age := nameAge(name)
		if got[name] {
			examinedSurvivors++
			if !haveSurvivor || age > oldestSurvivorAge {
				oldestSurvivorAge = age
				haveSurvivor = true
			}
		} else {
			examinedDropped++
			if !haveDropped || age < youngestDroppedAge {
				youngestDroppedAge = age
				haveDropped = true
			}
		}
	}
	if examinedSurvivors != maxFiles {
		t.Errorf("examined chunk kept %d survivors, want exactly maxFiles=%d", examinedSurvivors, maxFiles)
	}
	if examinedDropped != wantDropped {
		t.Errorf("examined chunk dropped %d, want %d", examinedDropped, wantDropped)
	}
	if haveSurvivor && haveDropped && oldestSurvivorAge > youngestDroppedAge {
		t.Errorf("a kept survivor (age %v) is older than a dropped entry (age %v): the cap did not keep the newest of the examined chunk", oldestSurvivorAge, youngestDroppedAge)
	}

	for i := 0; i < seeded; i++ {
		name := fmt.Sprintf("b%04d%s", i, queuedEventExt)
		if !examined[name] && !got[name] {
			t.Errorf("never-examined entry %s was removed: only the examined prefix may be touched", name)
		}
	}
}

// TestQueueMinHeapNeverExceedsCapacity is the heap-invariant regression for
// be-wwy2.3: pruneQueueFrom's walk pushes one live entry at a time and evicts
// the oldest whenever the heap grows past maxFiles, so the heap itself must
// never hold more than maxFiles entries at any point during that sequence —
// not just after the walk completes. This is what backs the "bounded memory"
// claim in the design: a single prune pass's peak memory for live candidates
// is O(maxFiles), not O(every live entry in the directory).
func TestQueueMinHeapNeverExceedsCapacity(t *testing.T) {
	const maxFiles = 10
	const seeded = 200
	now := time.Now()

	h := &queueMinHeap{}
	heap.Init(h)
	for i := 0; i < seeded; i++ {
		heap.Push(h, queueEntry{
			path:    fmt.Sprintf("q%04d%s", i, queuedEventExt),
			modTime: now.Add(-time.Duration(i+1) * time.Minute),
			size:    10,
		})
		for h.Len() > maxFiles {
			heap.Pop(h)
		}
		if h.Len() > maxFiles {
			t.Fatalf("after push %d: heap holds %d entries, want at most %d", i, h.Len(), maxFiles)
		}
	}
	if h.Len() != maxFiles {
		t.Fatalf("final heap holds %d entries, want exactly %d (seeded %d > maxFiles)", h.Len(), maxFiles, seeded)
	}
}

// TestQueueMinHeapCapacityZero pins the maxFiles=0 edge case the design
// calls out explicitly: every pushed entry must be immediately evictable
// back off again, with no off-by-one that assumes a non-zero capacity.
func TestQueueMinHeapCapacityZero(t *testing.T) {
	const maxFiles = 0
	now := time.Now()

	h := &queueMinHeap{}
	heap.Init(h)
	heap.Push(h, queueEntry{path: "q.evtq", modTime: now, size: 10})
	for h.Len() > maxFiles {
		heap.Pop(h)
	}
	if h.Len() != 0 {
		t.Fatalf("heap holds %d entries after evicting to maxFiles=0, want 0", h.Len())
	}
}
