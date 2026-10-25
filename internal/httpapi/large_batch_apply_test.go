package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/issueops"
)

// The pins for the raised POST issues:batchApply envelope: the 16 MiB body
// cap, the one-wide large-write semaphore that serializes oversized
// transactions without touching an ordinary request, and the EXTENDED (never
// narrowed) run budget a request over largeApplyItemThreshold items gets once
// it is admitted. The item-count cap itself (issueops.MaxApplyBatchItems,
// 100->1000) is pinned at the role and storage layers (issueops,
// internal/storage/uow, internal/storage/embeddeddolt, internal/storage/dolt);
// backend/conformance's RunBatchApplyBoundsTheItemCount already scales to it
// automatically, which is why it is not repinned here.

// TestApplyBatchBodyCapIsEnforcedWhileReading mirrors
// TestClaimBodyCapIsEnforcedWhileReading (claim_test.go): the raised 16 MiB
// cap is refused mid-read, at the decoder, before any member exists to name.
func TestApplyBatchBodyCapIsEnforcedWhileReading(t *testing.T) {
	oversized := `{"actor":"` + strings.Repeat("x", maxApplyBatchBodyBytes) + `"}`
	r := httptest.NewRequest(http.MethodPost, batchApplyPath, strings.NewReader(oversized))

	members, res := decodeJSONObject(httptest.NewRecorder(), r, maxApplyBatchBodyBytes)
	if res == nil {
		t.Fatalf("a %d-byte body was accepted (%d members)", len(oversized), len(members))
	}
	if res.Problem.Status != http.StatusBadRequest || res.Problem.Code != string(CodeInvalidArgument) {
		t.Errorf("problem = %d/%s, want 400/%s", res.Problem.Status, res.Problem.Code, CodeInvalidArgument)
	}
	if res.Problem.Detail == nil || !strings.Contains(*res.Problem.Detail, "larger than") {
		t.Errorf("detail = %v, want it to say the body was too large", res.Problem.Detail)
	}
}

// TestApplyBatchAcceptsABodyThePreB1CapWouldHaveRefused proves the raise is
// real end-to-end over the wire, not just in the constant: a body north of the
// OLD 4 MiB cap (but comfortably under the new 16 MiB one) must now succeed,
// built from ordinary create items rather than one oversized member.
func TestApplyBatchAcceptsABodyThePreB1CapWouldHaveRefused(t *testing.T) {
	const oldCap = 4 << 20
	applier := &roleBatchApplier{}
	ts := newApplyBatchServer(t, applier)

	// 60 items with a ~100 KiB description each is ~6 MiB of body — over the
	// old 4 MiB cap, under the new 16 MiB one.
	desc := strings.Repeat("d", 100<<10)
	items := make([]string, 60)
	for i := range items {
		items[i] = fmt.Sprintf(`{"kind":"create","create":{"title":"t-%d","description":%q}}`, i, desc)
	}
	body := `{"actor":"alice","items":[` + strings.Join(items, ",") + `]}`
	if len(body) <= oldCap {
		t.Fatalf("test body is %d bytes, want more than the old %d-byte cap to actually exercise the raise", len(body), oldCap)
	}
	if int64(len(body)) >= maxApplyBatchBodyBytes {
		t.Fatalf("test body is %d bytes, want less than the new %d-byte cap", len(body), maxApplyBatchBodyBytes)
	}

	resp := ts.claim(t, batchApplyPath, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a %d-byte body under the raised cap: %s", resp.StatusCode, len(body), readAll(t, resp))
	}
}

// TestCapBatchApplyLargeTiesAllThreeLimits pins the three numbers
// CapBatchApplyLarge (issues.batchApplyLarge) promises together: an older
// client that checks this token before dialing an over-100-item plan relies
// on ALL THREE — the item cap, the body cap and the run-budget ceiling — so a
// change that lowers one of them without also removing the token would
// advertise an envelope this server no longer actually honors.
func TestCapBatchApplyLargeTiesAllThreeLimits(t *testing.T) {
	const (
		wantMaxItems       = 1000
		wantMaxBodyBytes   = 16 << 20
		wantThreshold      = 100
		wantDefaultCeiling = 5 * time.Minute
	)
	if issueops.MaxApplyBatchItems != wantMaxItems {
		t.Errorf("issueops.MaxApplyBatchItems = %d, want %d", issueops.MaxApplyBatchItems, wantMaxItems)
	}
	if maxApplyBatchBodyBytes != wantMaxBodyBytes {
		t.Errorf("maxApplyBatchBodyBytes = %d, want %d", maxApplyBatchBodyBytes, wantMaxBodyBytes)
	}
	if largeApplyItemThreshold != wantThreshold {
		t.Errorf("largeApplyItemThreshold = %d, want %d", largeApplyItemThreshold, wantThreshold)
	}
	if DefaultLargeApplyCeiling != wantDefaultCeiling {
		t.Errorf("DefaultLargeApplyCeiling = %s, want %s", DefaultLargeApplyCeiling, wantDefaultCeiling)
	}
	if !slices.Contains(behaviorCapabilities, CapBatchApplyLarge) {
		t.Fatal("CapBatchApplyLarge is not advertised in behaviorCapabilities; a client cannot discover this envelope")
	}
}

// TestAcquireLargeApplySerializesOneAtATime drives acquireLargeApply
// directly: a second caller blocks until the first releases.
func TestAcquireLargeApplySerializesOneAtATime(t *testing.T) {
	s := &Server{
		largeApplySem:     make(chan struct{}, 1),
		semTimeout:        time.Minute, // generous: this test is about serialization, not timeout
		largeApplyCeiling: time.Minute,
		closing:           make(chan struct{}),
	}

	runCtx1, release1, err := s.acquireLargeApply(context.Background())
	if err != nil {
		t.Fatalf("first acquireLargeApply: %v", err)
	}
	dl1, ok := runCtx1.Deadline()
	if !ok {
		t.Fatal("first run context carries no deadline")
	}
	if got := s.largeApplyDeadline.Load(); got == nil || !got.Equal(dl1) {
		t.Fatalf("largeApplyDeadline while held = %v, want %v", got, dl1)
	}

	// A second, live caller blocks until the first releases.
	second := make(chan struct{})
	go func() {
		_, release2, err := s.acquireLargeApply(context.Background())
		if err != nil {
			t.Errorf("second acquireLargeApply: %v", err)
			return
		}
		release2()
		close(second)
	}()

	select {
	case <-second:
		t.Fatal("the second acquire completed before the first released — the slot is not one-wide")
	case <-time.After(50 * time.Millisecond):
	}

	release1()
	if got := s.largeApplyDeadline.Load(); got != nil {
		t.Fatalf("largeApplyDeadline after release = %v, want nil", got)
	}

	select {
	case <-second:
	case <-time.After(2 * time.Second):
		t.Fatal("the second acquire never completed after the first released")
	}
}

// TestAcquireLargeApplyDeadlineStartsAtAcquisitionNotAtCall pins that the run
// budget is measured from the moment the slot is actually taken, not from
// when the caller first started waiting for it: a caller queued for a while
// must still get the FULL largeApplyCeiling once admitted, not whatever is
// left of a deadline that started ticking back when it first queued.
func TestAcquireLargeApplyDeadlineStartsAtAcquisitionNotAtCall(t *testing.T) {
	s := &Server{
		largeApplySem:     make(chan struct{}, 1),
		semTimeout:        time.Minute,
		largeApplyCeiling: time.Minute,
		closing:           make(chan struct{}),
	}
	_, release1, err := s.acquireLargeApply(context.Background())
	if err != nil {
		t.Fatalf("first acquireLargeApply: %v", err)
	}

	// The queue delay must be LARGE relative to the slack below, not the
	// other way around: a mutation that measures the run deadline from the
	// CALL (when acquireLargeApply was entered and started queueing) instead
	// of from ACQUISITION (when the slot was actually won) only differs from
	// the correct behavior by roughly the queue delay. A short queue delay
	// (e.g. 250ms) against generous slack (e.g. 2s) would hide that
	// difference entirely; a 2s queue delay against 250ms slack cannot.
	const queueDelay = 2 * time.Second
	const slack = 250 * time.Millisecond
	queueStart := time.Now()
	go func() {
		time.Sleep(queueDelay)
		release1()
	}()

	runCtx2, release2, err := s.acquireLargeApply(context.Background())
	if err != nil {
		t.Fatalf("second acquireLargeApply: %v", err)
	}
	defer release2()
	acquiredAt := time.Now()

	if waited := acquiredAt.Sub(queueStart); waited < queueDelay/2 {
		t.Fatalf("test setup: the second caller was not actually queued (waited only %s)", waited)
	}
	dl2, ok := runCtx2.Deadline()
	if !ok {
		t.Fatal("second run context carries no deadline")
	}
	budget := dl2.Sub(acquiredAt)
	if budget < s.largeApplyCeiling-slack || budget > s.largeApplyCeiling+slack {
		t.Errorf("run budget measured from ACQUISITION = %s, want ~%s (the deadline must start when the lock is acquired, not when the wait began)", budget, s.largeApplyCeiling)
	}
}

// TestAcquireLargeApplyRefusesAfterClosing pins the drain-safety half of
// acquireLargeApply: once s.closing fires, every NEW acquisition attempt is
// refused with ErrBusy rather than being admitted or left to hang — which is
// what lets Serve's graceful-drain budget be computed ONCE at shutdown start
// and still be correct (see Serve's doc comment).
func TestAcquireLargeApplyRefusesAfterClosing(t *testing.T) {
	s := &Server{
		largeApplySem:     make(chan struct{}, 1),
		semTimeout:        time.Minute,
		largeApplyCeiling: time.Minute,
		closing:           make(chan struct{}),
	}
	close(s.closing)

	if _, _, err := s.acquireLargeApply(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatalf("acquireLargeApply after closing = %v, want ErrBusy", err)
	}

	// The slot itself must stay free: a refusal here is not a leak.
	select {
	case s.largeApplySem <- struct{}{}:
	default:
		t.Fatal("largeApplySem appears held after a refusal that never admitted anything")
	}
}

// TestAdmitLargeApplyRefusesIfClosingRacesInAfterTheSlotIsAlreadyWon pins item
// 3's other half directly: a select with multiple ready cases (largeApplySem
// and s.closing both ready in acquireLargeApply) picks pseudo-randomly, so a
// goroutine can win the large-apply slot in the very instant a graceful
// shutdown begins — after Serve has already snapshotted the drain budget.
// admitLargeApply's post-store recheck of s.closing is what refuses that
// admission instead of letting it run under a stale budget. This test
// exercises admitLargeApply directly, simulating that exact ordering
// (slot already taken, THEN closing fires, THEN admitLargeApply runs)
// without depending on actually winning a timing race.
func TestAdmitLargeApplyRefusesIfClosingRacesInAfterTheSlotIsAlreadyWon(t *testing.T) {
	s := &Server{
		largeApplySem:     make(chan struct{}, 1),
		largeApplyCeiling: time.Minute,
		closing:           make(chan struct{}),
	}
	s.largeApplySem <- struct{}{} // simulate acquireLargeApply's select already having won the slot
	close(s.closing)              // shutdown raced in between winning the slot and admission

	runCtx, release, err := s.admitLargeApply(context.Background())
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("admitLargeApply after a same-instant close = %v, want ErrBusy", err)
	}
	if runCtx != nil || release != nil {
		t.Fatalf("admitLargeApply after a same-instant close returned runCtx=%v release non-nil=%t, want both nil", runCtx, release != nil)
	}
	if got := s.largeApplyDeadline.Load(); got != nil {
		t.Fatalf("largeApplyDeadline left stamped at %v after a refused admission, want nil", *got)
	}

	// The slot must be released back, not leaked, by the refusal.
	select {
	case s.largeApplySem <- struct{}{}:
	default:
		t.Fatal("largeApplySem appears held after admitLargeApply refused a same-instant-close admission")
	}
}

// TestLargeApplyRefusalIsBusyNotInternal is the HTTP-level twin of
// TestAcquireLargeApplyRefusesAfterClosing's sibling case: a request that
// gives up while queued for the large-apply slot — because the bounded wait
// (semTimeout) expired — must answer 503 busy with Retry-After, never the
// non-retryable 500 a bare context error would produce. Nothing was written,
// so nothing here is unsafe to retry.
func TestLargeApplyRefusalIsBusyNotInternal(t *testing.T) {
	applier := &controlledApplier{gate: make(chan struct{})}
	defer close(applier.gate)
	ts := newTestServer(t, rolesConfig(Config{BatchApplier: applier}), func(s *Server) {
		s.semTimeout = 100 * time.Millisecond
	})

	bigBody := createItemsBody("alice", largeApplyItemThreshold+1)
	holderDone := make(chan claimResult, 1)
	go func() { holderDone <- ts.claimAsync(bigBody) }()
	waitForCallCount(t, applier, 1)

	resp := ts.claim(t, batchApplyPath, bigBody)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 busy: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["code"] != string(CodeBusy) {
		t.Errorf("code = %v, want %s", body["code"], CodeBusy)
	}
	if ra := resp.Header.Get("Retry-After"); ra == "" {
		t.Error("missing Retry-After header on a busy refusal")
	}
}

// TestAcquireLargeApplyRefusesCleanlyWhenAQueuedCallerDisconnects is item
// 7's explicit disconnect sub-bullet: a request queued for the large-apply
// slot whose OWN context ends first — the client hung up, or its ordinary
// (unrelated) request deadline expired — must answer the same ErrBusy every
// other refusal above answers. Nothing was written on this path, so it is
// safe to retry; returning the raw ctx.Err() instead would let it fall
// through to a non-retryable 500, misrepresenting a refusal that cost
// nothing as a server fault.
func TestAcquireLargeApplyRefusesCleanlyWhenAQueuedCallerDisconnects(t *testing.T) {
	s := &Server{
		largeApplySem:     make(chan struct{}, 1),
		semTimeout:        time.Minute, // long enough that ctx, not the timer, ends the wait
		largeApplyCeiling: time.Minute,
		closing:           make(chan struct{}),
	}
	_, release1, err := s.acquireLargeApply(context.Background())
	if err != nil {
		t.Fatalf("first acquireLargeApply: %v", err)
	}
	defer release1()

	callerCtx, cancel := context.WithCancel(context.Background())
	queuedErr := make(chan error, 1)
	go func() {
		_, _, err := s.acquireLargeApply(callerCtx)
		queuedErr <- err
	}()

	// Give the queued caller time to actually reach the bounded wait before
	// disconnecting it, so this exercises the ctx.Done() branch, not a race
	// against acquireLargeApply's own setup.
	time.Sleep(50 * time.Millisecond)
	cancel() // the client hangs up (or its own unrelated deadline expires)

	select {
	case err := <-queuedErr:
		if !errors.Is(err, ErrBusy) {
			t.Errorf("queued caller's error after its own ctx ended = %v, want ErrBusy (never a raw ctx.Err() misclassified as a 500)", err)
		}
		if errors.Is(err, context.Canceled) {
			t.Errorf("queued caller's error = %v wraps context.Canceled directly; it must be exactly ErrBusy, not a ctx error a caller could mistake for its own cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("acquireLargeApply never returned after the queued caller's context was canceled")
	}
}

// TestLargeApplyQueueDoesNotStarveOrdinaryRequests reproduces the DoS this
// slice fixes: route() acquires the GENERAL sem slot before the handler ever
// runs and holds it for the handler's whole lifetime, so a goroutine that
// then blocks UNBOUNDED on the one-wide large-apply lock starves every other
// request behind a saturated sem. Two requests queued behind a held
// large-apply slot must each give up (and free their sem slot) within a
// bounded wait, so a later ordinary request is still served promptly rather
// than finding the general sem exhausted by requests parked forever.
func TestLargeApplyQueueDoesNotStarveOrdinaryRequests(t *testing.T) {
	applier := &controlledApplier{gate: make(chan struct{})}
	defer close(applier.gate)
	ts := newTestServer(t, rolesConfig(Config{BatchApplier: applier}), func(s *Server) {
		s.sem = make(chan struct{}, 2) // a small general pool, easy to saturate
		s.semTimeout = 200 * time.Millisecond
	})

	bigBody := createItemsBody("alice", largeApplyItemThreshold+1)
	holderDone := make(chan claimResult, 1)
	go func() { holderDone <- ts.claimAsync(bigBody) }()
	waitForCallCount(t, applier, 1)

	// Saturate the tuned general sem with requests that will each queue
	// behind the held large-apply slot.
	queuedDone := make(chan claimResult, 2)
	for i := 0; i < 2; i++ {
		go func() { queuedDone <- ts.claimAsync(bigBody) }()
	}

	// Each queued request must be SHED — 503 busy — well inside a few
	// multiples of the tuned semTimeout, not held open indefinitely. Before
	// the fix (an unbounded select on ctx.Done() alone) this never returns,
	// and the loop below times out instead — the "failing before, passing
	// after" reproduction the fix is checked against.
	for i := 0; i < 2; i++ {
		select {
		case res := <-queuedDone:
			if res.err != nil {
				t.Fatalf("queued large request %d transport error: %v", i, res.err)
			}
			if res.resp.StatusCode != http.StatusServiceUnavailable {
				t.Errorf("queued large request %d status = %d, want 503 busy: %s", i, res.resp.StatusCode, readAll(t, res.resp))
			} else if ra := res.resp.Header.Get("Retry-After"); ra == "" {
				t.Errorf("queued large request %d carries no Retry-After header", i)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("queued large request %d never gave up the general sem slot it held while parked on the large-apply lock — this is the starvation bug", i)
		}
	}

	// Both queued requests gave their sem slots back. A fresh, ordinary small
	// request must be served promptly — the general sem must not still be
	// exhausted by requests that gave up on the large-apply lock but never
	// released it.
	start := time.Now()
	smallResp := ts.claim(t, batchApplyPath, createItemsBody("carol", 1))
	if smallResp.StatusCode != http.StatusOK {
		t.Fatalf("small request after the queue cleared status = %d, want 200: %s", smallResp.StatusCode, readAll(t, smallResp))
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("small request after the queue cleared took %s, want well under a second", elapsed)
	}
}

// TestLargeApplyBurstNeverStarvesSmallRequestsForSemTimeout is item 1 of the
// coordinator's second review: a BOUNDED wait for largeApplySem is not
// enough on its own. Every large request that queues for the slot still
// holds its OWN general sem slot for the whole wait (route() acquires sem
// before the handler runs), so a burst of large requests could occupy a
// semTimeout's worth of general concurrency slots merely queueing — even
// though only one of them could ever win the large-apply slot — starving
// small requests behind a saturated sem for that entire window.
//
// The fix caps the number of WAITERS at one: everything beyond the request
// already holding largeApplySem and the one already queued behind it is
// refused immediately (ErrBusy, no wait, no sem slot held for it). This test
// pins the OBSERABLE consequence directly: with a small general sem pool and
// a semTimeout long enough that the old bounded-but-unbounded-waiter-count
// behavior would visibly stall small requests, 16 concurrent large requests
// racing 8 concurrent small ones must never make a small request wait on the
// large-apply contention at all — each small one completes in well under a
// second, not "eventually, once a wait times out" (semTimeout, 5s). The bound
// is a stall detector, not a latency benchmark: a small request can still
// queue briefly behind the refused large requests, each holding a general sem
// slot while its body decodes, and under -race on a loaded 4-vCPU CI runner
// that measured ~160ms.
func TestLargeApplyBurstNeverStarvesSmallRequestsForSemTimeout(t *testing.T) {
	applier := &controlledApplier{gate: make(chan struct{})}
	gateClosed := false
	defer func() {
		if !gateClosed {
			close(applier.gate)
		}
	}()
	ts := newTestServer(t, rolesConfig(Config{BatchApplier: applier}), func(s *Server) {
		s.sem = make(chan struct{}, 4) // small general pool: easy to saturate if waiters pile up
		s.semTimeout = 5 * time.Second // long enough that an old-style multi-waiter stall would be obvious
	})

	bigBody := createItemsBody("alice", largeApplyItemThreshold+1)

	const numBig = 16
	bigDone := make(chan claimResult, numBig)
	for i := 0; i < numBig; i++ {
		go func() { bigDone <- ts.claimAsync(bigBody) }()
	}
	// Let the first big request actually take the slot and start running
	// (blocked on the gate) before firing the small requests, so the small
	// requests race real large-apply contention, not just a cold start.
	waitForCallCount(t, applier, 1)

	const numSmall = 8
	type smallResult struct {
		claimResult
		elapsed time.Duration
	}
	smallDone := make(chan smallResult, numSmall)
	for i := 0; i < numSmall; i++ {
		go func(i int) {
			start := time.Now()
			res := ts.claimAsync(createItemsBody(fmt.Sprintf("small-%d", i), 1))
			smallDone <- smallResult{claimResult: res, elapsed: time.Since(start)}
		}(i)
	}

	for i := 0; i < numSmall; i++ {
		select {
		case res := <-smallDone:
			if res.err != nil {
				t.Fatalf("small request transport error: %v", res.err)
			}
			if res.resp.StatusCode != http.StatusOK {
				t.Errorf("small request status = %d, want 200: %s", res.resp.StatusCode, readAll(t, res.resp))
			}
			if res.elapsed > time.Second {
				t.Errorf("small request took %s, want well under a second: a waiter for the large-apply slot must never hold a general sem slot long enough to stall a small request", res.elapsed)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("small request %d never completed — this is the starvation bug item 1 fixes", i)
		}
	}

	// Drain every big request so the test can exit cleanly: exactly one runs
	// (blocked on the gate) at a time, at most one more ever queues and then
	// wins the slot once the first releases, and the rest are refused
	// immediately. Closing (rather than sending once) unblocks every request
	// that is, or will be, parked on the gate, however many turns it takes
	// the single-waiter slot to cycle through.
	close(applier.gate)
	gateClosed = true
	for i := 0; i < numBig; i++ {
		select {
		case <-bigDone:
		case <-time.After(3 * time.Second):
			t.Fatalf("big request %d never returned", i)
		}
	}
}

// TestDrainWaitsForInFlightLargeApplyButRefusesAQueuedOne is the graceful-
// shutdown story: a large apply already holding the slot when shutdown
// begins is waited out and its commit lands; a second one still queued for
// the slot when s.closing fires is refused (503 busy) rather than extending
// the drain further or being force-killed.
func TestDrainWaitsForInFlightLargeApplyButRefusesAQueuedOne(t *testing.T) {
	applier := &controlledApplier{gate: make(chan struct{})}
	stdout := &strings.Builder{}
	stderr := &lockedBuffer{}
	srv, err := Listen(rolesConfig(Config{
		BatchApplier:      applier,
		Addr:              "127.0.0.1:0",
		Stdout:            stdout,
		Stderr:            stderr,
		LargeApplyCeiling: time.Minute,
	}))
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	// Long enough that s.closing, not the timer, is what refuses the queued
	// request below.
	srv.semTimeout = 5 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.Serve(ctx) }()

	ts := &testServer{Server: srv, client: &http.Client{Timeout: 20 * time.Second}, base: "http://" + srv.Addr()}

	bigBody := createItemsBody("alice", largeApplyItemThreshold+1)
	holderDone := make(chan claimResult, 1)
	go func() { holderDone <- ts.claimAsync(bigBody) }()
	waitForCallCount(t, applier, 1)

	queuedDone := make(chan claimResult, 1)
	go func() { queuedDone <- ts.claimAsync(bigBody) }()
	// Give the queued request time to reach acquireLargeApply's bounded wait
	// before shutdown begins; decoding a small fixed body is fast enough that
	// a short fixed sleep is the simplest synchronization here, matching this
	// file's other fixed-delay waits.
	time.Sleep(100 * time.Millisecond)

	cancel() // begin graceful shutdown with the first in flight, the second queued

	select {
	case res := <-queuedDone:
		if res.err != nil {
			t.Fatalf("queued large request transport error: %v", res.err)
		}
		if res.resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("queued request during drain status = %d, want 503 busy: %s", res.resp.StatusCode, readAll(t, res.resp))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the queued large request was not refused once shutdown began")
	}

	// The in-flight one must be waited out, not cut off: let it finish now.
	close(applier.gate)

	select {
	case res := <-holderDone:
		if res.err != nil {
			t.Fatalf("in-flight large request transport error: %v", res.err)
		}
		if res.resp.StatusCode != http.StatusOK {
			t.Errorf("in-flight large request status during drain = %d, want 200: %s", res.resp.StatusCode, readAll(t, res.resp))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the in-flight large request never completed during drain")
	}

	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Serve did not return after shutdown")
	}

	log := stderr.String()
	if !strings.Contains(log, "event=shutdown_complete") {
		t.Errorf("shutdown did not complete cleanly:\n%s", log)
	}
	if strings.Contains(log, "event=shutdown_forced") {
		t.Errorf("shutdown was forced rather than completing the drain:\n%s", log)
	}
	if got := applier.callCount(); got != 1 {
		t.Errorf("ApplyBatch call count = %d, want 1 (the queued request must never have reached the role)", got)
	}
}

// TestServeExtendsDrainPastAFixedFloorForAnInFlightLargeApply is item 3 of
// the coordinator's second review: TestDrainWaitsForInFlightLargeApplyButRefusesAQueuedOne
// above exercises the real Serve() path, but the in-flight large apply there
// is released within a couple hundred milliseconds of shutdown starting — far
// under even the OLD fixed drainTimeout constant (20s) — so a mutation
// reverting Serve to always drain for a fixed floor, ignoring
// drainBudget's extension for an in-flight large apply's own remaining
// deadline, would still pass it undetected.
//
// This test shrinks the floor (Server.drainTimeout) to something a unit test
// can afford to actually exceed, holds a large apply past that shrunk floor
// but still well under its own extended deadline, and asserts Serve waits it
// out rather than force-killing the connection: proof the extension is wired
// into Serve's real drain, not just correct in the drainBudget helper.
func TestServeExtendsDrainPastAFixedFloorForAnInFlightLargeApply(t *testing.T) {
	const shrunkFloor = 150 * time.Millisecond
	const heldPastFloor = 600 * time.Millisecond // several floors, still << the ceiling below
	const ceiling = 5 * time.Second

	applier := &controlledApplier{gate: make(chan struct{})}
	stdout := &strings.Builder{}
	stderr := &lockedBuffer{}
	srv, err := Listen(rolesConfig(Config{
		BatchApplier:      applier,
		Addr:              "127.0.0.1:0",
		Stdout:            stdout,
		Stderr:            stderr,
		LargeApplyCeiling: ceiling,
	}))
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	srv.drainTimeout = shrunkFloor

	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.Serve(ctx) }()

	ts := &testServer{Server: srv, client: &http.Client{Timeout: 20 * time.Second}, base: "http://" + srv.Addr()}

	bigBody := createItemsBody("alice", largeApplyItemThreshold+1)
	holderDone := make(chan claimResult, 1)
	go func() { holderDone <- ts.claimAsync(bigBody) }()
	waitForCallCount(t, applier, 1)

	cancel() // begin graceful shutdown with the large apply in flight

	// Release the gate well past shrunkFloor. If Serve used the fixed floor
	// instead of drainBudget's extension, Shutdown's drainCtx would have
	// expired by now and forced the connection closed already.
	time.Sleep(heldPastFloor)
	close(applier.gate)

	select {
	case res := <-holderDone:
		if res.err != nil {
			t.Fatalf("in-flight large request transport error (a fixed-floor drain would force-kill this connection before releasing the gate above): %v", res.err)
		}
		if res.resp.StatusCode != http.StatusOK {
			t.Errorf("in-flight large request status = %d, want 200: %s", res.resp.StatusCode, readAll(t, res.resp))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the in-flight large request never completed during drain")
	}

	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after shutdown")
	}

	log := stderr.String()
	if !strings.Contains(log, "event=shutdown_complete") {
		t.Errorf("shutdown did not complete cleanly:\n%s", log)
	}
	if strings.Contains(log, "event=shutdown_forced") {
		t.Errorf("shutdown was forced rather than extending the drain past the shrunk floor for the in-flight large apply:\n%s", log)
	}
}

// TestDrainBudget pins drainBudget's extension behavior directly, without
// paying for a real multi-second wait: no large apply in flight drains in
// exactly drainTimeout; one in flight with a deadline further out than
// drainTimeout EXTENDS the budget to that deadline; one with a deadline
// closer than drainTimeout (e.g. it is nearly done) never SHRINKS the budget
// below drainTimeout.
func TestDrainBudget(t *testing.T) {
	farFuture := time.Now().Add(time.Hour)
	nearFuture := time.Now().Add(time.Second)

	cases := []struct {
		name string
		dl   *time.Time
		want time.Duration
	}{
		{"no large apply in flight", nil, drainTimeout},
		{"large apply deadline far past drainTimeout", &farFuture, time.Until(farFuture) + drainGrace},
		{"large apply deadline closer than drainTimeout", &nearFuture, drainTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := drainBudget(drainTimeout, tc.dl)
			const slack = 2 * time.Second
			if got < tc.want-slack || got > tc.want+slack {
				t.Errorf("drainBudget(%v) = %s, want ~%s", tc.dl, got, tc.want)
			}
		})
	}
}

// deadlineCapturingApplier records the ctx.Deadline() each call actually
// received, and whether that ctx carried issueops' extended-retry-budget
// marker, so an HTTP-level test can check what budget handleApplyBatch handed
// the role without a real backend or a sleep-based race.
type deadlineCapturingApplier struct {
	mu        sync.Mutex
	deadlines []time.Time
	marked    []bool
}

func (d *deadlineCapturingApplier) ApplyBatch(ctx context.Context, req issueops.ApplyBatchRequest) (issueops.ApplyBatchResult, error) {
	dl, _ := ctx.Deadline()
	d.mu.Lock()
	d.deadlines = append(d.deadlines, dl)
	d.marked = append(d.marked, issueops.HasExtendedRetryBudget(ctx))
	d.mu.Unlock()
	return issueops.ApplyBatchResult{Items: make([]issueops.ItemResult, len(req.Items))}, nil
}

func (d *deadlineCapturingApplier) lastMarked(t *testing.T) bool {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.marked) == 0 {
		t.Fatal("ApplyBatch was never called")
	}
	return d.marked[len(d.marked)-1]
}

func (d *deadlineCapturingApplier) last(t *testing.T) time.Time {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.deadlines) == 0 {
		t.Fatal("ApplyBatch was never called")
	}
	return d.deadlines[len(d.deadlines)-1]
}

// TestApplyBatchDeadlineAtItemCounts pins which budget governs the context
// the role actually receives at each side of largeApplyItemThreshold: EXACTLY
// requestDeadline (60s) at and under the threshold — route()'s own
// unconditional deadline, untouched by anything in this file — and EXACTLY
// the configured largeApplyCeiling above it, never a value scaled by item
// count. A distinctive, non-default ceiling is configured so a test passing
// by coincidence against a hardcoded production constant is not possible.
func TestApplyBatchDeadlineAtItemCounts(t *testing.T) {
	const testCeiling = 17 * time.Second
	applier := &deadlineCapturingApplier{}
	ts := newTestServer(t, rolesConfig(Config{BatchApplier: applier}), func(s *Server) {
		s.largeApplyCeiling = testCeiling
	})

	cases := []struct {
		name  string
		items int
		want  time.Duration
	}{
		{"at threshold (100)", largeApplyItemThreshold, requestDeadline},
		{"just over threshold (101)", largeApplyItemThreshold + 1, testCeiling},
		{"500", 500, testCeiling},
		{"at cap (1000)", issueops.MaxApplyBatchItems, testCeiling},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			resp := ts.claim(t, batchApplyPath, createItemsBody("alice", tc.items))
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
			}
			got := applier.last(t).Sub(start)
			const slack = 5 * time.Second
			if got < tc.want-slack || got > tc.want+slack {
				t.Errorf("%d items: applier's ctx budget = %s, want ~%s", tc.items, got, tc.want)
			}
		})
	}
}

// TestApplyBatchDeadlineExceedsTheOrdinaryRequestDeadlineWhenConfiguredTo is
// item 7's large-run-ctx sub-bullet: TestApplyBatchDeadlineAtItemCounts above
// configures testCeiling = 17s, which is BELOW requestDeadline (60s) — so a
// mutation that narrows the large-apply run context to
// min(largeApplyCeiling, requestDeadline), or otherwise lets route()'s
// ordinary 60s deadline leak into the detached run context, would produce the
// exact same 17s result and pass undetected. A ceiling ABOVE requestDeadline
// is the only configuration that can tell the two apart, so this test uses
// one (97s).
func TestApplyBatchDeadlineExceedsTheOrdinaryRequestDeadlineWhenConfiguredTo(t *testing.T) {
	const testCeiling = 97 * time.Second
	if testCeiling <= requestDeadline {
		t.Fatalf("test setup: testCeiling (%s) must exceed requestDeadline (%s) to distinguish an extended budget from a narrowed one", testCeiling, requestDeadline)
	}
	applier := &deadlineCapturingApplier{}
	ts := newTestServer(t, rolesConfig(Config{BatchApplier: applier}), func(s *Server) {
		s.largeApplyCeiling = testCeiling
	})

	start := time.Now()
	resp := ts.claim(t, batchApplyPath, createItemsBody("alice", largeApplyItemThreshold+1))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	got := applier.last(t).Sub(start)
	const slack = 5 * time.Second
	if got < testCeiling-slack {
		t.Errorf("applier's ctx budget = %s, want ~%s: the large apply's own ceiling must not be narrowed to the ordinary %s request deadline", got, testCeiling, requestDeadline)
	}
}

// TestApplyBatchMarksOnlyALargeApplyForTheExtendedRetryBudget pins the seam
// between the two halves of the large-apply retry budget.
// internal/storage/uow's retryTxBudget lets a commit-time conflict keep
// retrying past the ordinary 15s ceiling only when ctx carries
// issueops.WithExtendedRetryBudget's marker, and admitLargeApply is the one
// place that sets it. The uow tests mark their contexts by hand, and the
// deadline tests above never look at the marker, so without this pin the
// extension could be left unwired with every other test green. The case at
// the threshold matters as much as the ones over it: marking an ordinary
// request would raise its retry ceiling from 15s to nearly its whole 60s
// deadline, the regression the marker exists to prevent.
func TestApplyBatchMarksOnlyALargeApplyForTheExtendedRetryBudget(t *testing.T) {
	applier := &deadlineCapturingApplier{}
	ts := newTestServer(t, rolesConfig(Config{BatchApplier: applier}))

	cases := []struct {
		name  string
		items int
		want  bool
	}{
		{"at threshold (100)", largeApplyItemThreshold, false},
		{"just over threshold (101)", largeApplyItemThreshold + 1, true},
		{"at cap (1000)", issueops.MaxApplyBatchItems, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := ts.claim(t, batchApplyPath, createItemsBody("alice", tc.items))
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
			}
			if got := applier.lastMarked(t); got != tc.want {
				t.Errorf("%d items: issueops.HasExtendedRetryBudget(applier's ctx) = %t, want %t", tc.items, got, tc.want)
			}
		})
	}
}

// controlledApplier is a BatchApplier that blocks a call carrying more than
// largeApplyItemThreshold items on a caller-controlled gate, so an HTTP-level
// test can prove the semaphore serializes oversized requests without a real
// backend or a sleep-based race.
type controlledApplier struct {
	mu    sync.Mutex
	calls []int // len(req.Items), one entry per call, in arrival order

	gate chan struct{} // closed to release every big call waiting on it
}

func (c *controlledApplier) ApplyBatch(ctx context.Context, req issueops.ApplyBatchRequest) (issueops.ApplyBatchResult, error) {
	c.mu.Lock()
	c.calls = append(c.calls, len(req.Items))
	c.mu.Unlock()
	if len(req.Items) > largeApplyItemThreshold {
		select {
		case <-c.gate:
		case <-ctx.Done():
			return issueops.ApplyBatchResult{}, ctx.Err()
		}
	}
	return issueops.ApplyBatchResult{Items: make([]issueops.ItemResult, len(req.Items))}, nil
}

func (c *controlledApplier) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

// createItemsBody builds a minimal valid ApplyBatchRequest body with n create
// items, for a test that only cares about item COUNT, not content.
func createItemsBody(actor string, n int) string {
	items := make([]string, n)
	for i := range items {
		items[i] = fmt.Sprintf(`{"kind":"create","create":{"title":"t-%d"}}`, i)
	}
	return fmt.Sprintf(`{"actor":%q,"items":[%s]}`, actor, strings.Join(items, ","))
}

// TestLargeApplySemaphoreBoundaryIsInclusiveOfThreshold pins the exact
// largeApplyItemThreshold boundary: a request carrying EXACTLY the threshold
// must never touch largeApplySem at all, even while another request holds it
// — it must complete immediately rather than queuing. One item more must
// queue behind it. This is the `>` (not `>=`) comparison in handleApplyBatch,
// pinned directly: mutating it to `>=` makes the boundary case above block
// and fail this test.
func TestLargeApplySemaphoreBoundaryIsInclusiveOfThreshold(t *testing.T) {
	applier := &controlledApplier{gate: make(chan struct{})}
	ts := newTestServer(t, rolesConfig(Config{BatchApplier: applier}), func(s *Server) {
		s.semTimeout = 200 * time.Millisecond
	})

	bigBody := createItemsBody("alice", largeApplyItemThreshold+1)
	holderDone := make(chan claimResult, 1)
	go func() { holderDone <- ts.claimAsync(bigBody) }()
	waitForCallCount(t, applier, 1)

	exactBody := createItemsBody("bob", largeApplyItemThreshold)
	start := time.Now()
	resp := ts.claim(t, batchApplyPath, exactBody)
	elapsed := time.Since(start)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("a %d-item request took %s while the large-apply slot was held; it must not wait on it", largeApplyItemThreshold, elapsed)
	}

	close(applier.gate)
	res := <-holderDone
	if res.err != nil {
		t.Fatalf("large request transport error: %v", res.err)
	}
	if res.resp.StatusCode != http.StatusOK {
		t.Fatalf("large request status = %d, want 200: %s", res.resp.StatusCode, readAll(t, res.resp))
	}
}

// TestLargeApplyRequestsSerializeOverHTTP is the end-to-end version of
// TestAcquireLargeApplySerializesOneAtATime: two concurrent requests each
// carrying more than largeApplyItemThreshold items must not run inside
// ApplyBatch at the same time, while a concurrent SMALL (<=threshold) request
// is never made to wait behind either of them.
func TestLargeApplyRequestsSerializeOverHTTP(t *testing.T) {
	applier := &controlledApplier{gate: make(chan struct{})}
	ts := newTestServer(t, rolesConfig(Config{BatchApplier: applier}))

	bigBody := createItemsBody("alice", largeApplyItemThreshold+1)
	smallBody := createItemsBody("bob", 5)

	firstDone := make(chan claimResult, 1)
	go func() { firstDone <- ts.claimAsync(bigBody) }()

	// Wait for the first big request to actually enter ApplyBatch (holding
	// the semaphore) before starting the second.
	waitForCallCount(t, applier, 1)

	secondDone := make(chan claimResult, 1)
	go func() { secondDone <- ts.claimAsync(bigBody) }()

	// The second big request must NOT reach ApplyBatch while the first still
	// holds the slot: it blocks in acquireLargeApply instead.
	select {
	case res := <-secondDone:
		t.Fatalf("the second large request completed before the first (status=%v err=%v) — the large-write semaphore did not serialize them", statusOf(res), res.err)
	case <-time.After(50 * time.Millisecond):
	}
	if got := applier.callCount(); got != 1 {
		t.Fatalf("ApplyBatch call count while the first large request is held = %d, want 1: the second must not have reached the role yet", got)
	}

	// A concurrent SMALL request is unaffected by the in-flight large one: it
	// must complete promptly rather than queue behind the semaphore it never
	// touches.
	smallResp := ts.claim(t, batchApplyPath, smallBody)
	if smallResp.StatusCode != http.StatusOK {
		t.Fatalf("small concurrent request status = %d, want 200: %s", smallResp.StatusCode, readAll(t, smallResp))
	}

	// Release the gate: both big calls (the first already inside ApplyBatch,
	// the second still queued on the semaphore) can now finish in turn.
	close(applier.gate)

	for i, ch := range []chan claimResult{firstDone, secondDone} {
		select {
		case res := <-ch:
			if res.err != nil {
				t.Errorf("large request %d transport error: %v", i, res.err)
				continue
			}
			if res.resp.StatusCode != http.StatusOK {
				t.Errorf("large request %d status = %d, want 200: %s", i, res.resp.StatusCode, readAll(t, res.resp))
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("large request %d never completed after the gate opened", i)
		}
	}

	if got := applier.callCount(); got != 3 {
		t.Fatalf("total ApplyBatch calls = %d, want 3 (two large, one small)", got)
	}
}

// waitForCallCount polls until applier has recorded at least n calls or the
// test's patience runs out. Used only to synchronize a goroutine with the
// point where a handler has entered the role, not to assert timing.
func waitForCallCount(t *testing.T, applier *controlledApplier, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if applier.callCount() >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("ApplyBatch was not called %d time(s) within the wait window (got %d)", n, applier.callCount())
}

// claimResult carries an HTTP response or a transport error, so a goroutine
// can report either back to the test goroutine over a channel. Go testing
// disallows t.Fatal/t.Fatalf (and every other *testing.T failure method) from
// any goroutine but the one running the test function — no amount of internal
// serialization inside *testing.T changes that — so a helper meant to be
// called from a spawned goroutine must never call one, which is why this type
// exists and why claimAsync below reports through it instead.
type claimResult struct {
	resp *http.Response
	err  error
}

// statusOf reports a claimResult's status code, or -1 for a transport error,
// for a failure message that needs to read one without risking a nil
// dereference.
func statusOf(r claimResult) int {
	if r.resp == nil {
		return -1
	}
	return r.resp.StatusCode
}

// claimAsync is ts.claim's non-fatal twin: safe to call from any goroutine,
// because it never calls a *testing.T method. A caller reads the returned
// claimResult back on the TEST goroutine and fails there if it carries an
// error.
func (ts *testServer) claimAsync(body string) claimResult {
	req, err := http.NewRequest(http.MethodPost, ts.base+batchApplyPath, strings.NewReader(body))
	if err != nil {
		return claimResult{err: fmt.Errorf("new request: %w", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.client.Do(req)
	if err != nil {
		return claimResult{err: fmt.Errorf("POST %s: %w", batchApplyPath, err)}
	}
	return claimResult{resp: resp}
}
