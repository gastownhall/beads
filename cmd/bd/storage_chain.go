package main

import (
	"context"
	"path/filepath"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/externaldeps"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/telemetry"
)

// wireStorageDecorators composes the storage chain in the order the rest of
// bd expects:
//
//	caller → HookFiringStore → externaldeps.Store → InstrumentedStorage → raw DoltStorage
//
// telemetry.WrapStorage is a no-op when telemetry is disabled, so the
// instrumentation layer is only present when BD_OTEL_ENABLED=true (or a
// legacy BD_OTEL_* selector is set). The hook layer sits outside telemetry so
// storage spans measure pure DB time without hook-firing overhead. The policy
// sits directly beneath hooks so serve's one hook-layer peel retains it.
//
// Versioned-history activation is NOT applied here. It used to be, and this runs
// on the one store a command opens for its own workspace, so every store a
// routed write opens for another workspace missed it. It now happens in the
// factories that construct a store, on the raw store before anything can wrap it
// (see versioned_history.go) -- which is also why the decorators above, none of
// which forwards SetVersionedHistoryEnabled, cannot get in its way.
//
// Extracted from main.go's PersistentPreRunE so the chain composition is
// unit-testable — the bug this PR fixes was a missing WrapStorage call,
// and the regression class deserves test coverage.
func wireStorageDecorators(store storage.DoltStorage, hookRunner *hooks.Runner, hooksDisabled bool) storage.DoltStorage {
	if store == nil {
		return nil
	}
	store = telemetry.WrapStorage(store)
	store = wireExternalDependencyPolicy(store)
	if hookRunner != nil && !hooksDisabled {
		store = storage.NewHookFiringStore(store, hookRunner)
	}
	return store
}

// wireExternalDependencyPolicy applies only the read/guard policy. Routed
// stores must use this without inheriting the caller's hooks or telemetry.
func wireExternalDependencyPolicy(store storage.DoltStorage) storage.DoltStorage {
	if store == nil {
		return nil
	}
	return externaldeps.New(
		store,
		func(project externaldeps.ProjectName) (string, bool) {
			path := config.ResolveExternalProjectPath(string(project))
			return path, path != ""
		},
		func(ctx context.Context, projectRoot string) (storage.DoltStorage, error) {
			return newReadOnlyStoreFromConfig(ctx, filepath.Join(projectRoot, ".beads"))
		},
	)
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

func wireExternalDependencyUOWProvider(provider uow.UnitOfWorkProvider) uow.UnitOfWorkProvider {
	return externaldeps.WrapUOWProvider(
		provider,
		func(project externaldeps.ProjectName) (string, bool) {
			path := config.ResolveExternalProjectPath(string(project))
			return path, path != ""
		},
		func(ctx context.Context, projectRoot string) (storage.DoltStorage, error) {
			return newReadOnlyStoreFromConfig(ctx, filepath.Join(projectRoot, ".beads"))
		},
	)
}
