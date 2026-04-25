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
