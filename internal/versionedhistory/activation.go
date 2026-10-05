// Package versionedhistory decides whether dual-write issue-version history is on
// for one store or unit-of-work provider, and applies that decision where the
// thing is constructed.
//
// It is a package rather than helpers inside cmd/bd for the same reason as
// internal/eventsjournal: bd doctor's repair handlers live in their own package
// and cannot import package main, and a repair that mutates issues needs exactly
// the same rule as the command surface. There is ONE implementation of that rule,
// here.
//
// THE SWITCH IS A PROPERTY OF THE STORE. `bd config set versioned-history.enabled
// true` writes a row of the workspace's own config table, and a store answers for
// itself: a write routed into rig B reads rig B's row, never the launching
// workspace's. The environment (BD_VERSIONED_HISTORY_ENABLED) and a hand-edited
// config.yaml value are read through viper, are process-wide, and can only turn
// recording ON for every store the process opens; nothing but the row turns a
// store off. The two are OR'd rather than ranked, deliberately: the hazard is a
// write that fails to record, never one that records when it needn't, so either
// plane saying yes is enough and neither can silently switch the other off.
//
// Activation is applied by the FACTORIES that construct a store or provider
// (ActivateStore, ActivateProvider), never by the commands that use one. Applying
// it at the root pre-run reached only the store a command opens for its own
// workspace, and missed every store a routed update, a routed close, `bd create
// --repo` or the direct-mode open builds for another one -- which then recorded no
// history while the command reported success. See
// cmd/bd/versioned_history_construction_test.go for the structural guard.
package versionedhistory

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/issueops"
)

// ConfigKey is the one spelling of the switch, used for the store-config read
// below and for the `bd config set` line the help text and the off-refusal tell
// people to run. They must not drift.
const ConfigKey = "versioned-history.enabled"

// Enabled reports whether version recording is on for this store, reading BOTH
// planes bd stores configuration in.
//
// This exists because the two disagreed. `bd config set versioned-history.enabled
// true` -- the command the help text and the off-refusal both name -- writes the
// workspace DATABASE, because the key is not a yaml-only key (config.IsYamlOnlyKey,
// and "versioned-history." is not among the prefixes at yaml_config.go).
// config.GetBool reads viper, which is yaml and env only. So the advertised command
// wrote one plane and the reader read the other: the user ran exactly what they
// were told, was told it worked, and the feature stayed off. Configured on,
// actually off -- the GH#536 class, and the precise failure this command was added
// to end. (bee-ghosttrack, #6661 review, finding 1.)
//
// The store plane is authoritative in the sense that matters: it makes the switch
// a property of the STORE rather than of whoever is calling, so two clients of one
// dolt sql-server agree about whether a write records a version. That closes the
// silent-holes case in the same review's finding 2 -- a client with recording off
// advances nothing, and the next enabled write mints a version that absorbs the
// unrecorded change under its own actor, producing contiguous revisions and a
// gapless-LOOKING history that is not one.
//
// The two are OR'd rather than ranked, deliberately. The hazard is a write that
// fails to record, never one that records when it needn't, so either plane saying
// yes is enough and neither can silently switch the other off. That keeps
// BD_VERSIONED_HISTORY_ENABLED=1 working for a one-off run without letting an unset
// (or =0) env var countermand a store that is recording.
//
// A store that cannot answer is not an error: proxied and no-db workspaces have no
// settings plane to read. Those fall back to viper alone rather than failing the
// command, because a store-config read is not worth breaking every bd invocation
// over.
//
// Nothing in production calls Enabled today. `bd versions` reports StoreSetting
// alone, because it only reads: the process-wide planes cannot make a store
// record, so they cannot speak for it. Activation uses EnabledForStore. Enabled
// stays, with its tests, as the ungated form of the combined rule.
func Enabled(ctx context.Context, st storage.DoltStorage) bool {
	if config.GetBool(ConfigKey) {
		// Already on via env or yaml; skip the store read entirely. This is also
		// what keeps the common path cheap when someone is driving the feature from
		// the environment.
		return true
	}
	return StoreSetting(ctx, st)
}

// StoreSetting reads the switch from the store's own settings plane, answering
// false for every reason a read can fail to produce a true.
//
// "EVERY reason" INCLUDES A PANIC, and that is the whole point of the defer below
// rather than an oversight to tidy away. The factories call this on every store
// they hand back, before anything has established that the store at the bottom of
// the chain can answer a config question at all -- so a store that cannot is not a
// hypothetical, it is a store that takes the process down. The shape that found
// it: a test double embedding a nil storage.DoltStorage, whose promoted
// WorkspaceConfig SIGSEGVs on call. The blast radius of letting that through is not
// one command; it is every bd invocation against any store whose settings plane is
// absent or half-built, for a read whose answer only ever decides whether to record
// versions. (bee-ghosttrack, #6661 second review.)
//
// A CAPABILITY ASSERTION ON WorkspaceConfig CANNOT REPLACE THIS, because
// WorkspaceConfig is declared on storage.DoltStorage itself
// (internal/storage/storage.go): every store satisfies an
// interface{ WorkspaceConfig() ... } assertion, including the one that panics when
// the method is actually called. That assertion would pass and the panic would
// happen anyway.
//
// AN ASSERTION ON storage.VersionedHistoryConfigurer IS DIFFERENT, and it is the
// primary guard now -- see EnabledForStore below. SetVersionedHistoryEnabled is NOT
// part of DoltStorage, so a store that embeds a nil DoltStorage does not promote it
// and does not satisfy that interface. (bee-ghosttrack, #6661 third review.)
//
// The recover stays anyway, because the guard does not cover every caller: `bd
// versions` calls StoreSetting directly, where the read is the point and no
// capability gate precedes it, and because a store that DOES implement the
// capability can still have a half-built settings plane. Guard first, recover as
// the backstop.
//
// A typed nil needs no separate guard for the same reason it needs no reflection:
// if its WorkspaceConfig panics, the recover answers false; if it returns (nil, nil)
// instead, the cfg == nil check answers false. Both arms are already here.
//
// The answer when the store cannot be read is false -- recording off, which is what
// the feature ships as. That is a real tradeoff and not a free one: a store that IS
// recording, whose settings read panics, is read as not recording, and a write
// during that window advances nothing. It is still the right side to fail to,
// because the alternative is that bd does not run at all, and a store too broken to
// answer a settings query is not one that was successfully recording versions a
// moment ago.
//
// WHAT IS AND IS NOT DIAGNOSED. An absent row is "" with a nil error by contract
// (WorkspaceConfig.GetSetting) and is the normal state of every workspace that never
// ran `bd config set`: it stays silently off. A read that ERRORS is different -- the
// row may well be there -- and resolving it to off with nothing to say so is the
// "set but silently off" shape this feature exists to end. Those go to debug.Logf,
// not to stderr: this runs on every invocation, and a diagnostic on a path the user
// did not ask about would be noise on all of them.
func StoreSetting(ctx context.Context, st storage.DoltStorage) (enabled bool) {
	if st == nil {
		return false
	}
	defer func() {
		if r := recover(); r != nil {
			// Deliberately swallowed, not re-raised and not logged: this runs on
			// every invocation and its answer is a default, so the honest report is
			// the false below rather than noise on a path the user did not ask
			// about. The named return makes that explicit.
			//
			// This is the opposite choice from the repo's other non-test recover
			// (internal/storage/dolt/transaction.go), which rolls back and then
			// re-panics, and the difference is not stylistic: there a caller must
			// not proceed over a half-applied transaction, so the panic has to keep
			// traveling. Here the caller can proceed perfectly well, because what it
			// wanted was a boolean with a documented default.
			enabled = false
		}
	}()
	cfg, err := st.WorkspaceConfig()
	if err != nil {
		debug.Logf("versioned history: %s left off: no settings plane readable on %T: %v\n", ConfigKey, st, err)
		return false
	}
	if cfg == nil {
		return false
	}
	res, err := cfg.GetSetting(ctx, issueops.GetSettingRequest{Key: ConfigKey})
	if err != nil {
		debug.Logf("versioned history: %s left off: reading the setting from %T failed: %v\n", ConfigKey, st, err)
		return false
	}
	return parseSettingBool(res.Value)
}

// parseSettingBool reads the stored string the way `bd config set` writes it.
// Unset is "" and a nil error by contract (WorkspaceConfig.GetSetting), which lands
// here as false -- the correct default for a flag that ships off.
func parseSettingBool(v string) bool {
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	return err == nil && b
}

// ValueEnables reports whether a stored value of the switch turns recording on,
// reading it the way every reader of the row does. `bd config set` uses it to
// decide whether the value it was just given deserves the replication and
// single-writer caveat, so that answer cannot drift from what the stores do with
// the same string.
func ValueEnables(v string) bool { return parseSettingBool(v) }

// EnabledForStore answers the switch for a freshly constructed store, which is the
// caller that runs on EVERY store bd opens.
//
// It differs from Enabled in one deliberate way: it will not read the store's
// settings plane unless the store could actually act on the answer. Activation's
// whole use for the result is Apply, which needs a storage.VersionedHistoryConfigurer;
// a store that is not one cannot record versions however the question is answered,
// so the read is pure cost and, for a store whose settings plane is absent or
// half-built, pure risk. This is what took CI red at 32d4f966e: the read panicked at
// wiring time on a test double embedding a nil storage.DoltStorage, which is a panic
// in a path whose answer only ever decides whether to record versions.
//
// The ordering matters and is not interchangeable with Apply's own assertion. The
// viper plane is read FIRST and unconditionally, so a store that is
// set-but-unsupported still reaches Apply with enabled=true and still gets the
// warning it prints. Only the STORE-plane read is gated. The one case that loses its
// warning is a store whose own settings plane says true but which lacks the
// capability -- and `bd config set` could not have written that plane on such a
// store in the first place.
//
// (bee-ghosttrack, #6661 third review, blocking finding 1.)
func EnabledForStore(ctx context.Context, st storage.DoltStorage) bool {
	if config.GetBool(ConfigKey) {
		return true
	}
	if _, canRecord := st.(storage.VersionedHistoryConfigurer); !canRecord {
		return false
	}
	return StoreSetting(ctx, st)
}

// ProviderSetting reads the switch from a unit-of-work provider's own database,
// answering false for every reason a read can fail to produce a true. It is the
// provider-path twin of StoreSetting: a provider has no storage.DoltStorage to ask
// for a settings plane, so the row is read the way the rest of the unit-of-work
// layer reads one -- through a unit of work's ConfigUseCase, as
// internal/storage/uow/issue_operations.go does for claim.pools.
//
// The read opens one unit of work, asks for one config row, and rolls it back: a
// pooled connection, START TRANSACTION, a SELECT, a ROLLBACK. It starts no server
// and creates no schema (the provider constructors have already opened the pool and
// run initSchema by the time this is called) and commits nothing. That is the whole
// cost of "each provider answers for its own database", and it is why this is read
// at construction, once, rather than falling back to viper alone: a provider that
// silently ignored the row would record nothing for a workspace that asked, while
// every command reported success.
//
// The same failure policy as StoreSetting applies, including the recover.
func ProviderSetting(ctx context.Context, p uow.UnitOfWorkProvider) (enabled bool) {
	if p == nil {
		return false
	}
	defer func() {
		if r := recover(); r != nil {
			enabled = false
		}
	}()
	unit, err := p.NewUOW(ctx)
	if err != nil {
		debug.Logf("versioned history: %s left off: opening a unit of work on %T to read it failed: %v\n", ConfigKey, p, err)
		return false
	}
	// Rolled back, never committed: this is a read.
	defer unit.Close(ctx)
	raw, err := unit.ConfigUseCase().GetConfig(ctx, ConfigKey)
	if err != nil {
		debug.Logf("versioned history: %s left off: reading the setting through %T failed: %v\n", ConfigKey, p, err)
		return false
	}
	return parseSettingBool(raw)
}

// EnabledForProvider is EnabledForStore for a provider: viper first, then the
// capability check, then the provider's own row.
func EnabledForProvider(ctx context.Context, p uow.UnitOfWorkProvider) bool {
	if config.GetBool(ConfigKey) {
		return true
	}
	if _, canRecord := p.(storage.VersionedHistoryConfigurer); !canRecord {
		return false
	}
	return ProviderSetting(ctx, p)
}

// ActivateStore applies a freshly opened store's own configured switch to it. It is
// written to be used as a deferred rewrite of a factory's named results:
//
//	func newX(ctx context.Context, ...) (s storage.DoltStorage, err error) {
//	    defer func() { s, err = versionedhistory.ActivateStore(ctx, s, err) }()
//	    ... open and return ...
//	}
//
// The shape is deliberate, and is internal/eventsjournal's. It keeps the open and
// its activation in ONE function body, so "constructs a store" and "activates
// history on it" are the same syntactic unit -- which is what lets the construction
// guard check the property structurally instead of chasing a call graph.
//
// The store is activated RAW: every factory hands back the concrete store, before
// any decorator wraps it. That ordering is load-bearing, because activation is a
// type assertion and none of the decorators (telemetry, externaldeps, hooks)
// forwards SetVersionedHistoryEnabled -- asserting against a wrapper would fail
// silently and leave history off while the config said it was on. It is pinned by
// TestApplyReachesOnlyTheRawStore.
//
// A failed open passes through untouched, so the typed-nil store never reaches the
// activation. A store that cannot version at all is returned as-is without its
// settings plane being read: there is nothing to activate, and the factories run for
// every command that opens a store.
func ActivateStore(ctx context.Context, s storage.DoltStorage, err error) (storage.DoltStorage, error) {
	if err != nil || s == nil {
		return s, err
	}
	Apply(s, EnabledForStore(ctx, s))
	return s, nil
}

// ActivateProvider is ActivateStore for a unit-of-work provider -- bd's second write
// plumbing, with its own transactions and its own activation switch, which
// proxied-server mode and `bd serve` against a server-mode workspace write through
// instead of a DoltStorage.
func ActivateProvider(ctx context.Context, p uow.UnitOfWorkProvider, err error) (uow.UnitOfWorkProvider, error) {
	if err != nil || p == nil {
		return p, err
	}
	Apply(p, EnabledForProvider(ctx, p))
	return p, nil
}

// Apply binds a resolved answer to one plumbing -- a store or a unit-of-work
// provider. Activation is per instance rather than process-global: a process can
// hold several plumbings at once (a routed create holds two), and enabling history
// on one must not enable it on the rest.
//
// It only ever turns recording ON. A false answer touches nothing, so flag-off is
// byte-identical to a build without the feature, which is the contract #6135 ships
// under.
//
// A plumbing that does not implement the capability is not an error: proxied and
// no-db backends legitimately do not. But it is not silently ignored either --
// asking for history and not getting it is exactly the case a user must be told
// about, so it warns.
func Apply(target any, enabled bool) {
	if !enabled {
		return
	}
	configurer, ok := target.(storage.VersionedHistoryConfigurer)
	if !ok {
		fmt.Fprintf(os.Stderr,
			"warning: versioned-history.enabled is set, but this storage backend (%T) does not support version history; it stays off\n",
			target)
		return
	}
	configurer.SetVersionedHistoryEnabled(true)
}
