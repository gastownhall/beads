package dolt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// getMetadataMap reads an issue's metadata JSON back as a raw-value map.
func getMetadataMap(t *testing.T, ctx context.Context, store *DoltStore, id string) map[string]json.RawMessage {
	t.Helper()
	issue, err := store.GetIssue(ctx, id)
	if err != nil {
		t.Fatalf("GetIssue(%s): %v", id, err)
	}
	m := make(map[string]json.RawMessage)
	if len(issue.Metadata) > 0 {
		if err := json.Unmarshal(issue.Metadata, &m); err != nil {
			t.Fatalf("unmarshal metadata for %s (%s): %v", id, issue.Metadata, err)
		}
	}
	return m
}

// updatedEventActors returns the actors of every EventUpdated recorded for id.
func updatedEventActors(t *testing.T, ctx context.Context, store *DoltStore, id string) []string {
	t.Helper()
	events, err := store.GetEvents(ctx, id, 0)
	if err != nil {
		t.Fatalf("GetEvents(%s): %v", id, err)
	}
	var actors []string
	for _, e := range events {
		if e.EventType == types.EventUpdated {
			actors = append(actors, e.Actor)
		}
	}
	return actors
}

// TestMergeMetadataConcurrentNoClobber is the money test for B1: many goroutines
// merge DISTINCT keys into the SAME issue at once. The old cross-transaction
// SlotSet (GetIssue in tx1, UpdateIssue in tx2) let each writer clobber the
// others because every writer wrote back a whole-metadata blob computed from a
// stale read. MergeMetadata does the read-modify-write inside one withRetryTx,
// so Dolt's optimistic-commit conflict on the shared metadata cell forces the
// loser to retry and re-read the now-committed metadata — every key must survive.
func TestMergeMetadataConcurrentNoClobber(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()

	ctx, cancel := concurrentTestContext(t)
	defer cancel()

	const id = "mm-concurrent"
	createPerm(t, ctx, store, id)

	// A pre-existing key must survive the concurrent merges untouched.
	if err := store.MergeMetadata(ctx, id, "seed", json.RawMessage(`"original"`), "tester"); err != nil {
		t.Fatalf("seed MergeMetadata: %v", err)
	}

	const numGoroutines = 8
	var wg sync.WaitGroup
	errs := make(chan error, numGoroutines)

	for n := range numGoroutines {
		wg.Go(func() {
			key := fmt.Sprintf("key-%d", n)
			val := json.RawMessage(fmt.Sprintf(`%d`, n))
			if err := store.MergeMetadata(ctx, id, key, val, fmt.Sprintf("worker-%d", n)); err != nil {
				errs <- fmt.Errorf("goroutine %d MergeMetadata(%q): %w", n, key, err)
			}
		})
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("merge error: %v", err)
	}
	if t.Failed() {
		t.Fatal("concurrent MergeMetadata failed — withRetryTx should have retried serialization conflicts")
	}

	m := getMetadataMap(t, ctx, store, id)
	if got := string(m["seed"]); got != `"original"` {
		t.Errorf("pre-existing key clobbered: metadata[seed] = %s, want \"original\"", got)
	}
	for n := range numGoroutines {
		key := fmt.Sprintf("key-%d", n)
		want := fmt.Sprintf("%d", n)
		if got, ok := m[key]; !ok {
			t.Errorf("key %q missing after concurrent merge — CLOBBERED (B1 regression)", key)
		} else if string(got) != want {
			t.Errorf("metadata[%q] = %s, want %s", key, got, want)
		}
	}
}

// TestMergeMetadataSequential covers the simple ordering: merge A, then B, and a
// pre-existing unrelated key is preserved across both.
func TestMergeMetadataSequential(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const id = "mm-seq"
	createPerm(t, ctx, store, id)

	if err := store.MergeMetadata(ctx, id, "pre", json.RawMessage(`"kept"`), "tester"); err != nil {
		t.Fatalf("merge pre: %v", err)
	}
	if err := store.MergeMetadata(ctx, id, "a", json.RawMessage(`"A"`), "tester"); err != nil {
		t.Fatalf("merge a: %v", err)
	}
	if err := store.MergeMetadata(ctx, id, "b", json.RawMessage(`"B"`), "tester"); err != nil {
		t.Fatalf("merge b: %v", err)
	}

	m := getMetadataMap(t, ctx, store, id)
	for key, want := range map[string]string{"pre": `"kept"`, "a": `"A"`, "b": `"B"`} {
		if got := string(m[key]); got != want {
			t.Errorf("metadata[%q] = %s, want %s", key, got, want)
		}
	}
}

// TestMergeMetadataNestedJSON proves a nested object value round-trips as a
// nested object, not a stringified blob — the reason MergeMetadata takes a
// json.RawMessage instead of a string.
func TestMergeMetadataNestedJSON(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const id = "mm-nested"
	createPerm(t, ctx, store, id)

	nested := json.RawMessage(`{"commit":"abc","pr":7}`)
	if err := store.MergeMetadata(ctx, id, "merge", nested, "tester"); err != nil {
		t.Fatalf("MergeMetadata nested: %v", err)
	}

	m := getMetadataMap(t, ctx, store, id)
	var got struct {
		Commit string `json:"commit"`
		PR     int    `json:"pr"`
	}
	if err := json.Unmarshal(m["merge"], &got); err != nil {
		t.Fatalf("nested value did not round-trip as an object: %s (%v)", m["merge"], err)
	}
	if got.Commit != "abc" || got.PR != 7 {
		t.Errorf("nested round-trip = %+v, want {commit:abc pr:7}", got)
	}
}

// TestMergeMetadataWisp merges a metadata key on a wisp (ephemeral) id, exercising
// the dolt_ignored wisp path (no DOLT_COMMIT).
func TestMergeMetadataWisp(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const id = "mm-wisp"
	createWisp(t, ctx, store, id)

	if err := store.MergeMetadata(ctx, id, "phase", json.RawMessage(`"done"`), "tester"); err != nil {
		t.Fatalf("MergeMetadata on wisp: %v", err)
	}
	m := getMetadataMap(t, ctx, store, id)
	if got := string(m["phase"]); got != `"done"` {
		t.Errorf("wisp metadata[phase] = %s, want \"done\"", got)
	}
}

// TestMergeMetadataNotFound returns storage.ErrNotFound for a missing id.
func TestMergeMetadataNotFound(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	err := store.MergeMetadata(ctx, "mm-missing", "k", json.RawMessage(`"v"`), "tester")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("err = %v, want errors.Is(_, storage.ErrNotFound)", err)
	}
}

// TestMergeMetadataInvalidJSON rejects a value that is not valid JSON up front.
func TestMergeMetadataInvalidJSON(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const id = "mm-badjson"
	createPerm(t, ctx, store, id)

	if err := store.MergeMetadata(ctx, id, "k", json.RawMessage(`{not json`), "tester"); err == nil {
		t.Fatal("MergeMetadata with invalid JSON value: want error, got nil")
	}
}

// TestSlotSetByteCompat locks in that the MergeMetadata-backed SlotSet stores the
// exact same metadata JSON as the historical whole-metadata rewrite: a string
// value is stored as a JSON string, so a single SlotSet yields {"key":"value"}.
func TestSlotSetByteCompat(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const id = "mm-slotcompat"
	createPerm(t, ctx, store, id)

	if err := store.SlotSet(ctx, id, "key", "value", "tester"); err != nil {
		t.Fatalf("SlotSet: %v", err)
	}

	issue, err := store.GetIssue(ctx, id)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got := string(issue.Metadata); got != `{"key":"value"}` {
		t.Errorf("SlotSet metadata = %s, want {\"key\":\"value\"} (byte-compat with old SlotSet)", got)
	}

	// SlotGet still reads the string back verbatim.
	if v, err := store.SlotGet(ctx, id, "key"); err != nil || v != "value" {
		t.Errorf("SlotGet after SlotSet = (%q, %v), want (value, nil)", v, err)
	}
}

// TestMergeMetadataRecordsEvent proves the audit trail is not dropped: routing
// through UpdateIssueInTx means MergeMetadata (and SlotSet built on it) each
// record an EventUpdated attributed to the caller's actor, exactly as the old
// SlotSet did via UpdateIssue.
func TestMergeMetadataRecordsEvent(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const id = "mm-event"
	createPerm(t, ctx, store, id)

	if err := store.MergeMetadata(ctx, id, "k", json.RawMessage(`"v"`), "merger"); err != nil {
		t.Fatalf("MergeMetadata: %v", err)
	}
	if err := store.SlotSet(ctx, id, "k2", "v2", "setter"); err != nil {
		t.Fatalf("SlotSet: %v", err)
	}

	actors := updatedEventActors(t, ctx, store, id)
	if !slices.Contains(actors, "merger") {
		t.Errorf("no EventUpdated attributed to \"merger\"; updated-event actors = %v", actors)
	}
	if !slices.Contains(actors, "setter") {
		t.Errorf("no EventUpdated attributed to \"setter\"; updated-event actors = %v", actors)
	}
}

// TestMergeMetadataOverwrite: merging the same key twice takes the last value and
// leaves other keys intact.
func TestMergeMetadataOverwrite(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const id = "mm-overwrite"
	createPerm(t, ctx, store, id)

	if err := store.MergeMetadata(ctx, id, "other", json.RawMessage(`"keep"`), "tester"); err != nil {
		t.Fatalf("merge other: %v", err)
	}
	if err := store.MergeMetadata(ctx, id, "a", json.RawMessage(`1`), "tester"); err != nil {
		t.Fatalf("merge a=1: %v", err)
	}
	if err := store.MergeMetadata(ctx, id, "a", json.RawMessage(`2`), "tester"); err != nil {
		t.Fatalf("merge a=2: %v", err)
	}

	m := getMetadataMap(t, ctx, store, id)
	if got := string(m["a"]); got != "2" {
		t.Errorf("metadata[a] = %s, want 2 (overwrite)", got)
	}
	if got := string(m["other"]); got != `"keep"` {
		t.Errorf("metadata[other] = %s, want \"keep\" (untouched)", got)
	}
}

// TestMergeMetadataSlotGetInterop: a value merged via MergeMetadata is readable
// through SlotGet.
func TestMergeMetadataSlotGetInterop(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const id = "mm-interop"
	createPerm(t, ctx, store, id)

	if err := store.MergeMetadata(ctx, id, "s", json.RawMessage(`"hello"`), "tester"); err != nil {
		t.Fatalf("MergeMetadata: %v", err)
	}
	if v, err := store.SlotGet(ctx, id, "s"); err != nil || v != "hello" {
		t.Errorf("SlotGet after MergeMetadata = (%q, %v), want (hello, nil)", v, err)
	}
}

// TestSlotClearRemovesKeyPreservesOthers: clearing one key leaves the rest, and
// clearing an absent key is a silent no-op (behavior parity with the old
// cross-tx SlotClear, now atomic).
func TestSlotClearRemovesKeyPreservesOthers(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const id = "sc-basic"
	createPerm(t, ctx, store, id)

	if err := store.SlotSet(ctx, id, "a", "1", "tester"); err != nil {
		t.Fatalf("SlotSet a: %v", err)
	}
	if err := store.SlotSet(ctx, id, "b", "2", "tester"); err != nil {
		t.Fatalf("SlotSet b: %v", err)
	}
	if err := store.SlotClear(ctx, id, "a", "tester"); err != nil {
		t.Fatalf("SlotClear a: %v", err)
	}

	m := getMetadataMap(t, ctx, store, id)
	if _, ok := m["a"]; ok {
		t.Errorf("key a still present after SlotClear")
	}
	if got := string(m["b"]); got != `"2"` {
		t.Errorf("metadata[b] = %s, want \"2\" (untouched by clearing a)", got)
	}
	if err := store.SlotClear(ctx, id, "nonexistent", "tester"); err != nil {
		t.Errorf("SlotClear absent key: want nil, got %v", err)
	}
}

// TestSlotClearConcurrentNoClobber is the money test for the clear path: pre-set
// keys are cleared concurrently while distinct new keys are set. The old cross-tx
// SlotClear read a stale whole-metadata blob and wrote it back, so a concurrent
// SlotSet's key vanished. With DeleteMetadataInTx sharing one transaction, every
// set key that was never cleared must survive, and every cleared key is gone.
func TestSlotClearConcurrentNoClobber(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := concurrentTestContext(t)
	defer cancel()

	const id = "sc-concurrent"
	createPerm(t, ctx, store, id)

	const n = 6
	for i := range n {
		if err := store.SlotSet(ctx, id, fmt.Sprintf("clear-%d", i), "x", "seed"); err != nil {
			t.Fatalf("seed clear-%d: %v", i, err)
		}
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2*n)
	for i := range n {
		wg.Go(func() {
			if err := store.SlotSet(ctx, id, fmt.Sprintf("set-%d", i), fmt.Sprintf("v%d", i), "setter"); err != nil {
				errs <- fmt.Errorf("SlotSet set-%d: %w", i, err)
			}
		})
		wg.Go(func() {
			if err := store.SlotClear(ctx, id, fmt.Sprintf("clear-%d", i), "clearer"); err != nil {
				errs <- fmt.Errorf("SlotClear clear-%d: %w", i, err)
			}
		})
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("%v", err)
	}
	if t.Failed() {
		t.Fatal("concurrent SlotSet/SlotClear errored — withRetryTx should have retried conflicts")
	}

	m := getMetadataMap(t, ctx, store, id)
	for i := range n {
		setKey := fmt.Sprintf("set-%d", i)
		want := fmt.Sprintf("%q", fmt.Sprintf("v%d", i))
		if got, ok := m[setKey]; !ok {
			t.Errorf("set key %q missing — a concurrent SlotClear CLOBBERED it (B1 regression)", setKey)
		} else if string(got) != want {
			t.Errorf("metadata[%q] = %s, want %s", setKey, got, want)
		}
		clearKey := fmt.Sprintf("clear-%d", i)
		if _, ok := m[clearKey]; ok {
			t.Errorf("clear key %q still present — concurrent SlotClear did not take effect", clearKey)
		}
	}
}
