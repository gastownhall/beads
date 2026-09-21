package main

import (
	"context"
	"fmt"
	"os"
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
// Extracted from main.go's PersistentPreRunE so the chain composition is
// unit-testable — the bug this PR fixes was a missing WrapStorage call,
// and the regression class deserves test coverage.
func wireStorageDecorators(store storage.DoltStorage, hookRunner *hooks.Runner, hooksDisabled bool) storage.DoltStorage {
	if store == nil {
		return nil
	}
	applyVersionedHistoryConfig(store, versionedHistoryEnabledForWiring(store))
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

// applyVersionedHistoryConfig turns dual-write issue-version history on for
// this store instance when versioned-history.enabled is set (env:
// BD_VERSIONED_HISTORY_ENABLED).
//
// It MUST run on the raw store, before wireStorageDecorators wraps anything.
// The capability is a type assertion, and none of the decorators above
// (telemetry.WrapStorage, externaldeps, HookFiringStore) forwards
// SetVersionedHistoryEnabled -- so calling this after a wrap would assert
// against the wrapper, fail silently, and leave history off while the config
// said it was on. That is the "wrong answer, not an empty one" failure this
// feature exists to avoid, so the ordering is pinned by
// TestVersionedHistoryConfigAppliesToTheRawStore.
//
// A store that does not implement the capability is not an error: proxied and
// no-db backends legitimately do not. But it is not silently ignored either --
// asking for history and not getting it is exactly the case a user must be
// told about, so it warns.
func applyVersionedHistoryConfig(store storage.DoltStorage, enabled bool) {
	if !enabled {
		return
	}
	configurer, ok := store.(storage.VersionedHistoryConfigurer)
	if !ok {
		fmt.Fprintf(os.Stderr,
			"warning: versioned-history.enabled is set, but this storage backend (%T) does not support version history; it stays off\n",
			store)
		return
	}
	configurer.SetVersionedHistoryEnabled(true)
}
