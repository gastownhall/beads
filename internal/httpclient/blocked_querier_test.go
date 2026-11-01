package httpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"

	beads "github.com/steveyegge/beads"
	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/issueops"
)

// TestIsBlockedBatchRidesTheBlockingAnnotatorBehindItsCapability pins the
// client half of the http IsBlockedBatch: beads.AsBlockedQuerier finds it, it
// asks listBlockingAnnotations with is_blocked=true, chunked at
// issueops.BlockedFlagsChunk ids, and answers id -> flag with an id the server
// gave no flag (no row) absent.
func TestIsBlockedBatchRidesTheBlockingAnnotatorBehindItsCapability(t *testing.T) {
	served := &apigen.ContextResponse{Capabilities: []string{"dependencies.blocking", wire.CapBlockingIsBlocked}}
	w := &countingWire{}
	w.preflight = func(context.Context, string) error { return nil }
	w.do = func(_ context.Context, req wire.Request, out any) error {
		w.dialed = append(w.dialed, req)
		items := make([]map[string]any, 0)
		for _, id := range req.Query["issue_id"] {
			item := map[string]any{"id": id, "blocked_by": []string{}, "blocks": []string{}}
			switch id {
			case "x-ghost":
			case "x-7", "x-150":
				item["is_blocked"] = true
			default:
				item["is_blocked"] = false
			}
			items = append(items, item)
		}
		raw, _ := json.Marshal(map[string]any{"items": items})
		return json.Unmarshal(raw, out)
	}
	var store beads.Storage = New(testTarget(t), w, served)
	querier, ok := beads.AsBlockedQuerier(store)
	if !ok {
		t.Fatal("AsBlockedQuerier(http store) = false")
	}
	ids := []string{"x-ghost"}
	for i := range 160 {
		ids = append(ids, fmt.Sprintf("x-%d", i))
	}
	ids = append(ids, "x-7") // a repeat is asked once
	flags, err := querier.IsBlockedBatch(t.Context(), ids)
	if err != nil {
		t.Fatalf("IsBlockedBatch: %v", err)
	}
	if len(w.dialed) != 2 {
		t.Fatalf("dialed %d requests, want 2 (161 distinct ids at %d per request)", len(w.dialed), issueops.BlockedFlagsChunk)
	}
	for _, req := range w.dialed {
		if req.Op != wire.OpListBlockingAnnotations || req.Path != wire.PathDependenciesBlocking {
			t.Errorf("dialed %s %s, want listBlockingAnnotations", req.Op, req.Path)
		}
		if got := req.Query["is_blocked"]; !slices.Equal(got, []string{"true"}) {
			t.Errorf("query[is_blocked] = %v, want [true]", got)
		}
		if n := len(req.Query["issue_id"]); n > issueops.BlockedFlagsChunk {
			t.Errorf("a request named %d ids, over the %d bound", n, issueops.BlockedFlagsChunk)
		}
	}
	if len(flags) != 160 {
		t.Errorf("len(flags) = %d, want 160 (the ghost absent)", len(flags))
	}
	if _, ok := flags["x-ghost"]; ok {
		t.Error("an id the server gave no flag is present; absent means no row")
	}
	if !flags["x-7"] || !flags["x-150"] || flags["x-8"] {
		t.Errorf("flags x-7=%v x-150=%v x-8=%v, want true true false", flags["x-7"], flags["x-150"], flags["x-8"])
	}
}

// TestIsBlockedBatchRefusesBeforeDialingWithoutTheCapability is the skew side:
// against a server that does not advertise dependencies.blocking.isBlocked (the
// deployed 1.3.0 serve, for one), the read refuses before the dial with the
// typed refusal gc's ready projection latches its degrade on, now naming the
// capability that would lift it. Sending it would cost a 400
// unknown_parameter; dropping is_blocked would pass "no flag" off as "free".
func TestIsBlockedBatchRefusesBeforeDialingWithoutTheCapability(t *testing.T) {
	masked := &apigen.ContextResponse{Capabilities: []string{"dependencies.blocking"}}
	w := newCountingWire(`{"items":[]}`)
	_, err := New(testTarget(t), w, masked).IsBlockedBatch(t.Context(), []string{"x-1"})
	var unsup *storage.ErrUnsupported
	if !errors.As(err, &unsup) {
		t.Fatalf("IsBlockedBatch error = %v, want *storage.ErrUnsupported", err)
	}
	if unsup.Capability != wire.CapBlockingIsBlocked {
		t.Errorf("refusal capability = %q, want %s", unsup.Capability, wire.CapBlockingIsBlocked)
	}
	if len(w.dialed) != 0 {
		t.Errorf("dialed %d times, want 0", len(w.dialed))
	}

	// The plain decoration is unaffected by the missing token.
	annotator, err := New(testTarget(t), w, masked).BlockingAnnotator()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := annotator.AnnotateBlocking(t.Context(), issueops.BlockingRequest{IDs: []string{"x-1"}}); err != nil {
		t.Fatalf("AnnotateBlocking without the flag: %v", err)
	}
	if w.query(t).Has("is_blocked") {
		t.Error("a request without IncludeIsBlocked sent is_blocked")
	}
}
