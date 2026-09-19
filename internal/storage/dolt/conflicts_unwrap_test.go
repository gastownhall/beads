package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// The wy-jpd3.5 review BLOCKER, guarded WITHOUT dolt (wy-wrq9o F6). The CLI
// never holds the concrete store: it holds decorators (telemetry, and the
// hook-firing store whenever hooks are on). A decorator that embeds the
// DoltStorage interface promotes only that interface's methods, so a naked
// assertion to ConflictInspector on the wrapper is false and every conflict
// reads as "no conflicts" — which is exactly how `bd conflicts` came to print
// "No merge conflicts" over a wedged merge. storage.UnwrapStore is the fix.
//
// Every assertion here is a type assertion over a typed-nil store: no method
// is ever called, so it needs no dolt server, no Docker, and no container. It
// runs on every developer machine, which is the whole point — the dolt-backed
// half of this guard (conflicts_integration_test.go) skips without one.

// unwrapProbeStore is a *DoltStore that is never dialled. UnwrapStore and the
// interface assertions below are pure type-level operations, so a nil pointer
// carrying the real method set is exactly the right subject.
func unwrapProbeStore() storage.DoltStorage { return (*DoltStore)(nil) }

// nonForwardingDecorator is a decorator that provably does NOT forward the
// optional conflict interfaces: it embeds the DoltStorage interface, so its
// method set is DoltStorage's plus Unwrap, and nothing else.
//
// It stands in for the production decorators on purpose (wy-wrq9o F7). The
// old form of this guard asserted that HookFiringStore does not satisfy
// ConflictInspector — pinning a LIMITATION of production code, so teaching
// the decorator to forward the interface (a strict improvement) would turn
// the test red. Non-forwarding is a property of the trap being modelled, not
// of any particular decorator, so the test double owns it.
type nonForwardingDecorator struct {
	storage.DoltStorage
	inner storage.DoltStorage
}

func (d *nonForwardingDecorator) Unwrap() storage.DoltStorage { return d.inner }

// TestDoltStoreExposesTheConflictSurface pins the method set the CLI reaches
// for. If DoltStore ever loses one of these, `bd conflicts` degrades to
// "no conflicts" rather than failing to build.
func TestDoltStoreExposesTheConflictSurface(t *testing.T) {
	concrete := unwrapProbeStore()
	if _, ok := concrete.(storage.ConflictInspector); !ok {
		t.Error("*DoltStore no longer satisfies ConflictInspector: bd conflicts show/resolve would read as 'no conflicts'")
	}
	if _, ok := concrete.(storage.MergeBlockerInspector); !ok {
		t.Error("*DoltStore no longer satisfies MergeBlockerInspector: schema conflicts and constraint violations would be invisible")
	}
}

// TestUnwrapStoreReachesTheConflictSurfaceThroughDecorators is the regression
// guard proper: whatever the CLI is handed, UnwrapStore must land on the
// concrete store, and the optional interfaces must be reachable from there.
func TestUnwrapStoreReachesTheConflictSurfaceThroughDecorators(t *testing.T) {
	concrete := unwrapProbeStore()

	chains := map[string]storage.DoltStorage{
		"bare store": concrete,
		// The trap, modelled by a decorator that cannot forward.
		"non-forwarding decorator": &nonForwardingDecorator{DoltStorage: concrete, inner: concrete},
		// Two production decorators deep, exactly as cmd/bd's chain can be.
		"hook-firing store": storage.NewHookFiringStore(concrete, nil),
		"two hook-firing stores": storage.NewHookFiringStore(
			storage.NewHookFiringStore(concrete, nil), nil),
		// Mixed: a decorator that does not forward, wrapping one that might.
		"mixed chain": &nonForwardingDecorator{
			DoltStorage: storage.NewHookFiringStore(concrete, nil),
			inner:       storage.NewHookFiringStore(concrete, nil),
		},
	}
	for name, chain := range chains {
		t.Run(name, func(t *testing.T) {
			unwrapped := storage.UnwrapStore(chain)
			if unwrapped != concrete {
				t.Fatalf("UnwrapStore did not reach the concrete store: got %T, want %T", unwrapped, concrete)
			}
			if _, ok := unwrapped.(storage.ConflictInspector); !ok {
				t.Error("UnwrapStore(chain) does not satisfy ConflictInspector: bd conflicts would report 'No merge conflicts' over a wedged merge (the wy-jpd3.5 review blocker)")
			}
			if _, ok := unwrapped.(storage.MergeBlockerInspector); !ok {
				t.Error("UnwrapStore(chain) does not satisfy MergeBlockerInspector: schema conflicts and constraint violations would be invisible to bd conflicts")
			}
		})
	}
}

// TestNakedAssertionOnADecoratorIsNotEnough keeps the guard above from going
// vacuous. If a naked assertion already reached the conflict surface through
// every wrapper, UnwrapStore would be untested dead weight and nobody would
// notice its removal. The subject is the test double, whose non-forwarding is
// guaranteed by construction — asserting this about a PRODUCTION decorator
// would forbid ever improving it (wy-wrq9o F7).
func TestNakedAssertionOnADecoratorIsNotEnough(t *testing.T) {
	concrete := unwrapProbeStore()
	decorated := storage.DoltStorage(&nonForwardingDecorator{DoltStorage: concrete, inner: concrete})

	if _, ok := decorated.(storage.ConflictInspector); ok {
		t.Fatal("nonForwardingDecorator satisfies ConflictInspector — DoltStorage must have absorbed the conflict methods; update this test double so the UnwrapStore guard stays non-vacuous")
	}
	if _, ok := decorated.(storage.MergeBlockerInspector); ok {
		t.Fatal("nonForwardingDecorator satisfies MergeBlockerInspector — DoltStorage must have absorbed GetMergeBlockers; update this test double so the UnwrapStore guard stays non-vacuous")
	}
	if _, ok := storage.UnwrapStore(decorated).(storage.ConflictInspector); !ok {
		t.Error("UnwrapStore is the only route to the conflict surface and it does not reach it")
	}
}
