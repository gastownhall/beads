package main

import (
	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/telemetry"
)

// wireStorageDecorators composes the storage chain in the order the rest of
// bd expects:
//
//	caller → HookFiringStore (outer) → InstrumentedStorage → raw DoltStorage
//
// telemetry.WrapStorage is a no-op when telemetry is disabled, so the
// instrumentation layer is only present when BD_OTEL_ENABLED=true (or a
// legacy BD_OTEL_* selector is set). The hook layer sits outermost so
// storage spans measure pure DB time without hook-firing overhead.
//
// Extracted from main.go's PersistentPreRunE so the chain composition is
// unit-testable — the bug this PR fixes was a missing WrapStorage call,
// and the regression class deserves test coverage.
func wireStorageDecorators(store storage.DoltStorage, hookRunner *hooks.Runner, hooksDisabled bool) storage.DoltStorage {
	if store == nil {
		return nil
	}
	store = telemetry.WrapStorage(store)
	if hookRunner != nil && !hooksDisabled {
		store = storage.NewHookFiringStore(store, hookRunner)
	}
	return store
}

// waitForCommandHooks lets the hooks this command fired finish before the
// process exits, bounded by the runner's own per-hook budget.
//
// It covers BOTH plumbings, because both fire through the one runner this
// process built: the DoltStorage chain wires it into HookFiringStore above, and
// proxied mode wires the same value into the notifying provider's sinks. A
// command that fired nothing waits on an empty group and returns at once.
//
// CALLED FROM TWO PLACES, and idempotent so that costs nothing: PersistentPostRunE
// for the clean path, where it must run after the store is closed, and main()
// after ExecuteC for every path cobra skips PostRunE on — a RunE that returned
// an error, which is how a partial batch exits with one issue committed.
func waitForCommandHooks() {
	if hookRunner == nil {
		return
	}
	hookRunner.Wait(hookRunner.Timeout())
}
