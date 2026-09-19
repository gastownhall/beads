package main

import (
	"context"
	"sync"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/eventsjournal"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/uow"
)

// Events-journal activation, applied in ONE place: the factories that construct
// a store or a unit-of-work provider (store_factory.go, store_factory_nocgo.go,
// uow_factory.go, and bd doctor's repair handlers through the same package).
// Every plumbing bd opens goes through one of those, so a new command cannot
// acquire a store that silently records nothing.
//
// It did not start there, and the three ways it was wrong are the argument for
// the guard that now holds it. First a process-global switch. Then two root
// pre-run call sites, which covered the CLI's own store and its proxied
// provider and missed `bd serve` (which builds its own provider for a
// server-mode workspace), routed creates and remote-cache hydration (which open
// a SECOND store for another workspace), the pluggable backend registry arm,
// and the personal-migration planning store. Then a blanket exemption for bd
// doctor, whose stated reason — "workspace repairs, not bead mutations" — was
// false for three of its repair handlers. Each miss ran with the journal off
// while every command reported success, because an empty journal is
// indistinguishable from a quiet one.
// TestEveryStoreConstructionActivatesTheEventsJournal keeps it centralized.
//
// The policy itself lives in internal/eventsjournal so bd doctor's fix package,
// which cannot import package main, applies the identical rule.

// eventsJournalEnabled reports the activation the PROCESS resolved, for the
// paths that have no particular workspace in hand.
func eventsJournalEnabled() bool {
	return config.GetBool(eventsjournal.ConfigKey)
}

// activateEventsJournalStore is eventsjournal.ActivateStore under the name the
// construction guard matches. Kept as a wrapper rather than called directly so
// cmd/bd has one spelling of the idiom and the guard has one name to look for.
func activateEventsJournalStore(beadsDir string, s storage.DoltStorage, err error) (storage.DoltStorage, error) {
	return eventsjournal.ActivateStore(beadsDir, s, err)
}

// eventsJournalMaintenanceRunner returns the plumbing auto-prune should run
// against, or nil when this process has none.
//
// The provider is checked FIRST because a proxied-server command has both: a
// unit-of-work provider it actually writes through, and (in some topologies) a
// store opened alongside it. Maintenance belongs on the plumbing that wrote the
// records — same connection settings, same database, same transaction
// discipline.
//
// A backend that cannot journal cannot need pruning, so a nil result is a
// silent no-op rather than a diagnostic.
func eventsJournalMaintenanceRunner() issueops.EventsMaintenanceRunner {
	if uowProvider != nil {
		if runner := eventsJournalMaintenanceRunnerFor(uowProvider); runner != nil {
			return runner
		}
	}
	return eventsJournalMaintenanceRunnerFor(store)
}

// eventsJournalMaintenanceRunnerFor resolves ONE plumbing value — a store or a
// unit-of-work provider — to its maintenance capability.
//
// BOTH decorator chains are peeled, and the provider half is not symmetry for
// its own sake. bd wraps the unit-of-work provider in a hook-firing decorator
// whenever a hook runner exists, which is by default; a bare type assertion
// against that wrapper matches nothing, and auto-prune becomes a silent no-op
// in exactly the topology — proxied server — that journals fastest. Nothing
// reports it: retention that never runs looks identical to retention with
// nothing to do.
//
// The wrapper also forwards the capability itself (notifyingProvider.
// RunEventsMaintenanceTx), so this is the second of two defenses rather than
// the only one. That is deliberate: the forwarder keeps the wrapper's SURFACE
// honest, which the parity guards enforce, and this peel keeps RESOLUTION
// honest against a future decorator that forgets one. A capability that is
// invisible when it goes missing gets both.
func eventsJournalMaintenanceRunnerFor(plumbing any) issueops.EventsMaintenanceRunner {
	if plumbing == nil {
		return nil
	}
	switch p := plumbing.(type) {
	case storage.DoltStorage:
		if p == nil {
			return nil
		}
		plumbing = storage.UnwrapStore(p)
	case uow.UnitOfWorkProvider:
		if p == nil {
			return nil
		}
		plumbing = uow.UnwrapProvider(p)
	}
	runner, _ := plumbing.(issueops.EventsMaintenanceRunner)
	return runner
}

// shouldAutoPruneEventsJournal reports whether this command pays for retention
// maintenance. The first two gates are the SAME pair auto-export and auto-push
// use one block above, deliberately rather than a new classification:
//
//   - runsPostCommandMaintenance excludes strict --readonly (a store opened to
//     refuse writes) and `bd serve`, which drives the same pass from its own
//     ticker rather than from a command boundary it does not have.
//   - isReadOnlyCommand excludes the classified reads, and it is load-bearing
//     rather than belt-and-braces: a read-only classification opens the store
//     with OpenForReadOnlyCommand, which is "otherwise a normal writable
//     store", so nothing underneath refuses a maintenance delete. Drop this
//     check and `bd list` prunes the journal — which
//     TestAutoPruneDoesNotRunForReadOnlyCommands catches.
//
// Two more gates are specific to this trigger:
//
//   - A PREVIEW command (--dry-run, --inspect) promised not to mutate, and the
//     root pre-run holds it to that with a write-refusing open. The trigger has
//     to make the same judgement the store-open mode did, or maintenance
//     becomes the one write a preview performs — logged as a failure at best,
//     and a real delete on any plumbing that does not refuse it.
//   - The `bd events` family is excluded whole. Reading a feed must never be
//     what prunes it: `bd events tail --follow` is a consumer, and a consumer
//     that trims its own backlog on the way out is a surprise nobody asked
//     for. `bd events prune` is excluded for the opposite reason — it just
//     resolved the retention bound by hand, so an automatic pass immediately
//     behind it is work with nothing to do.
//
// commandDidWrite — the flag Dolt auto-commit gates on — is deliberately NOT
// consulted. It marks "this write still needs a Dolt commit", which the
// embedded backend never sets because it commits inside the store; gating on it
// would silently exclude the entire embedded topology from retention. The
// classification is coarser (an unclassified command that wrote nothing can
// reach the throttle read) and that is the right way round: the cost of a
// false positive is one indexed query, and the cost of a false negative is a
// journal nothing ever bounds.
func shouldAutoPruneEventsJournal(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	if !runsPostCommandMaintenance(cmd.Name(), readonlyMode) || isReadOnlyCommand(cmd.Name()) {
		return false
	}
	if isPreviewCommand(cmd) || isEventsJournalCommand(cmd) {
		return false
	}
	return true
}

// isEventsJournalCommand reports whether cmd is `bd events` or one of its
// subcommands. It walks the ancestor chain rather than matching leaf names:
// `tail`, `export` and `prune` are all names the top level uses for something
// else, and a gate that matched on the leaf would be exempting the wrong
// commands the day one of them moves.
func isEventsJournalCommand(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if c == eventsCmd {
			return true
		}
	}
	return false
}

// autoPruneOnce holds the once-per-process guarantee the trigger promises.
// PersistentPostRunE runs once per bd invocation anyway; the guard is what
// keeps that true for the in-process callers that run Execute() repeatedly (the
// cmd/bd test binary, library embedders), where an unguarded trigger would turn
// a maintenance pass into a per-command tax.
var autoPruneOnce sync.Once

// maybeAutoPruneEventsJournal is the CLI's writer-pays retention trigger: after
// a mutating command has committed, bound the journal to its configured floors.
//
// WHERE IT IS CALLED FROM is the whole design. It runs in the root
// PersistentPostRunE, in the post-command maintenance region that already
// carries Dolt auto-commit, auto-backup, auto-export and auto-push — after the
// user's data is durably committed and pushed, before the store is closed. That
// is the narrowest existing seam with all four properties this needs: the
// command SUCCEEDED (an error return never reaches it), the mutation
// transaction is closed (so maintenance cannot extend or roll back a user's
// write), a store or provider is still open, and read-only commands are already
// excluded by the surrounding classification. `bd serve` is excluded there by
// name and drives the same pass from its own ticker instead.
//
// The alternatives were all worse in the same way — they fire somewhere that
// does not know a command finished. A post-write hook in the store decorator
// chain fires per write op, which on a batch command means dozens of passes,
// each one racing the mutation transaction that has not been committed yet. The
// issueops emit leaf is inside that transaction. Close() is reached from tests,
// doctor handlers and half-failed opens, where a delete is a surprise. And a
// PostRunE on each mutating command is the hand-maintained list that this
// feature's own review found broken twice.
//
// gating, in order of cost: config (free), plumbing (free), then the throttle
// (one indexed query). A workspace without the journal pays nothing at all.
func maybeAutoPruneEventsJournal(ctx context.Context, beadsDir string) {
	autoPruneOnce.Do(func() {
		if !eventsjournal.EnabledFor(beadsDir) || !eventsjournal.AutoPruneEnabledFor(beadsDir) {
			return
		}
		runner := eventsJournalMaintenanceRunner()
		if runner == nil {
			return
		}
		n, err := eventsjournal.AutoPrune(ctx, runner, eventsJournalAutoPruneOptions())
		reportEventsJournalAutoPrune(n, err)
	})
}

// eventsJournalAutoPruneOptions reads the retention floors the pass applies.
// Same keys and same accessor as `bd events prune`, so the automatic bound and
// the manual one cannot be computed from different numbers.
func eventsJournalAutoPruneOptions() eventsjournal.AutoPruneOptions {
	return eventsjournal.AutoPruneOptions{
		RetainDays: config.GetInt("events-journal-retain-days"),
		RetainRows: config.GetInt("events-journal-retain-rows"),
	}
}

// reportEventsJournalAutoPrune logs a pass. Debug-level and never returned:
// retention maintenance must not change what a user's command printed or what
// it exited with. A workspace whose journal cannot be pruned is a workspace
// with a large journal, not a broken CLI.
func reportEventsJournalAutoPrune(n int64, err error) {
	if err != nil {
		debug.Logf("events journal: auto-prune failed after %d row(s): %v\n", n, err)
		return
	}
	if n > 0 {
		debug.Logf("events journal: auto-prune deleted %d row(s)\n", n)
	}
}

// activateEventsJournalProvider is the same for a unit-of-work provider — bd's
// second write plumbing, with its own transactions and its own activation.
func activateEventsJournalProvider(ctx context.Context, beadsDir string, p uow.UnitOfWorkProvider, err error) (uow.UnitOfWorkProvider, error) {
	if err != nil || p == nil {
		return p, err
	}
	configurer, _ := p.(storage.EventsJournalConfigurer)
	if cfgErr := eventsjournal.Apply(configurer, eventsjournal.EnabledFor(beadsDir)); cfgErr != nil {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), providerCloseTimeout)
		defer cancel()
		_ = p.Close(closeCtx)
		return nil, cfgErr
	}
	return p, nil
}
