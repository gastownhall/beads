package httpclient

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/httpclient/encode"
	"github.com/steveyegge/beads/issueops"
)

// TestDetectCyclesIncludeTracksRefusesWithoutDialing is "L-cycles-tracks"'s
// pin: listDependencyCycles carries no include_tracks parameter at all, so a
// request that asks for the widened (tracks-following) walk is refused with a
// typed, ledger-cited error BEFORE any request reaches the wire, rather than
// silently answering the narrower default walk the server would have returned
// for a bare request.
//
// This is a unit pin rather than something the served tier proves: the
// refusal is pre-dial by design (mirrors TestEdgeCountRefusesMoreAnchorsThanTheWireCarries
// in counts_test.go), so a served run would only show that a request this
// client never sends would also have done something server-side. What has to
// be true here is that the client never dials at all.
func TestDetectCyclesIncludeTracksRefusesWithoutDialing(t *testing.T) {
	w := newCountingWire(`{"items":[],"has_more":false}`)
	detector, err := New(testTarget(t), w, nil).CycleDetector()
	if err != nil {
		t.Fatalf("CycleDetector(): %v", err)
	}

	_, err = detector.DetectCycles(t.Context(), issueops.DetectCyclesRequest{IncludeTracks: true})
	if !errors.Is(err, encode.ErrRefused) {
		t.Fatalf("IncludeTracks request encoded without a refusal: %v", err)
	}
	var refusal *encode.RefusedError
	if !errors.As(err, &refusal) || refusal.Row.ID != "L-cycles-tracks" {
		t.Errorf("refusal cites %+v, want ledger row L-cycles-tracks", err)
	}
	if len(w.dialed) != 0 {
		t.Errorf("IncludeTracks refusal dialed %d requests, want 0", len(w.dialed))
	}
}

// TestDetectCyclesWithoutIncludeTracksStillDials is the companion case: a
// request that leaves IncludeTracks false is unaffected by the refusal above
// and still reaches listDependencyCycles exactly as before.
func TestDetectCyclesWithoutIncludeTracksStillDials(t *testing.T) {
	w := newCountingWire(`{"items":[],"has_more":false}`)
	detector, err := New(testTarget(t), w, nil).CycleDetector()
	if err != nil {
		t.Fatalf("CycleDetector(): %v", err)
	}

	if _, err := detector.DetectCycles(t.Context(), issueops.DetectCyclesRequest{}); err != nil {
		t.Fatalf("DetectCycles(IncludeTracks: false): %v", err)
	}
	if len(w.dialed) != 1 {
		t.Errorf("dialed %d times, want exactly 1", len(w.dialed))
	}
}
