package httpclient

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
)

// TestSharedConnectionsDialOncePerWorkspaceTarget pins WithSharedConnections'
// key (bts-xpw.3.14.6): within one scope, the registry's two opens of the same
// workspace share one transport; a rewritten sidecar (another server, or another
// identity pin) dials afresh; and outside any scope every open dials its own.
func TestSharedConnectionsDialOncePerWorkspaceTarget(t *testing.T) {
	dir := t.TempDir()
	target := pinnedProjectTarget(t)
	if err := SaveTarget(dir, target); err != nil {
		t.Fatalf("SaveTarget: %v", err)
	}
	dials := 0
	swapDialer(t, func(context.Context, Target) (WireClient, error) {
		dials++
		return &fakeWire{res: &apigen.ContextResponse{ProjectId: "proj-1"}}, nil
	})

	ctx := WithSharedConnections(context.Background())
	if WithSharedConnections(ctx) != ctx {
		t.Error("re-scoping an already scoped context started a second memo")
	}
	a, err := NewReadOnlyFromConfig(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewFromConfig(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if dials != 1 || a.(*Store).wire != b.(*Store).wire {
		t.Fatalf("two scoped opens of one workspace dialed %d times; want one shared transport", dials)
	}

	target.ExpectProjectID = "proj-2"
	if err := SaveTarget(dir, target); err != nil {
		t.Fatalf("SaveTarget: %v", err)
	}
	if _, err := NewFromConfig(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if dials != 2 {
		t.Fatalf("a re-pinned sidecar reused the old transport (%d dials)", dials)
	}

	for range 2 {
		if _, err := NewFromConfig(context.Background(), dir); err != nil {
			t.Fatal(err)
		}
	}
	if dials != 4 {
		t.Fatalf("unscoped opens shared a transport (%d dials, want 4)", dials)
	}
}

// memoWire is a fakeWire that records whether an open turned its read memo on.
type memoWire struct {
	fakeWire
	memoized bool
}

func (w *memoWire) EnableReadMemo() { w.memoized = true }

// TestOnlyAReadMemoScopeMemoizesSharedTransports pins the memo as its own
// opt-in (bts-xpw.3.14.6): cmd/bd shares the gate's transport with every
// command, bd serve and --watch polls included, but only a one-shot command
// asks for the memo, so a shared transport memoizes nothing unless WithReadMemo
// says so — and WithReadMemo outside a sharing scope memoizes nothing either,
// since an unshared open's writes would not clear its sibling open's memo.
func TestOnlyAReadMemoScopeMemoizesSharedTransports(t *testing.T) {
	dir := t.TempDir()
	if err := SaveTarget(dir, pinnedProjectTarget(t)); err != nil {
		t.Fatalf("SaveTarget: %v", err)
	}
	var last *memoWire
	swapDialer(t, func(context.Context, Target) (WireClient, error) {
		last = &memoWire{fakeWire: fakeWire{res: &apigen.ContextResponse{ProjectId: "proj-1"}}}
		return last, nil
	})

	for _, tc := range []struct {
		name string
		ctx  context.Context
		want bool
	}{
		{"shared only", WithSharedConnections(context.Background()), false},
		{"shared with a read memo", WithReadMemo(WithSharedConnections(context.Background())), true},
		{"read memo wrapped first", WithSharedConnections(WithReadMemo(context.Background())), true},
		{"read memo without a sharing scope", WithReadMemo(context.Background()), false},
		{"neither", context.Background(), false},
	} {
		last = nil
		if _, err := NewFromConfig(tc.ctx, dir); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if last == nil || last.memoized != tc.want {
			t.Errorf("%s: the open's transport memoized reads = %v, want %v", tc.name, last != nil && last.memoized, tc.want)
		}
	}
}
