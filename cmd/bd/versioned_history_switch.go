package main

import (
	"context"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/issueops"
)

// versionedHistorySettingKey is the one spelling of the switch, used for both
// the store-config read below and the `bd config set` line the help text and
// the off-refusal tell people to run. They must not drift.
const versionedHistorySettingKey = "versioned-history.enabled"

// versionedHistoryEnabled reports whether version recording is on for this
// store, reading BOTH planes bd stores configuration in.
//
// This exists because the two disagreed. `bd config set
// versioned-history.enabled true` -- the command the help text and the
// off-refusal both name -- writes the workspace DATABASE, because the key is
// not a yaml-only key (config.IsYamlOnlyKey, and "versioned-history." is not
// among the prefixes at yaml_config.go). config.GetBool reads viper, which is
// yaml and env only. So the advertised command wrote one plane and the reader
// read the other: the user ran exactly what they were told, was told it
// worked, and the feature stayed off. Configured on, actually off -- the
// GH#536 class, and the precise failure this command was added to end.
// (bee-ghosttrack, #6661 review, finding 1.)
//
// The store plane is authoritative in the sense that matters: it makes the
// switch a property of the STORE rather than of whoever is calling, so two
// clients of one dolt sql-server agree about whether a write records a
// version. That closes the silent-holes case in the same review's finding 2 --
// a client with recording off advances nothing, and the next enabled write
// mints a version that absorbs the unrecorded change under its own actor,
// producing contiguous revisions and a gapless-LOOKING history that is not
// one.
//
// The two are OR'd rather than ranked, deliberately. The hazard is a write
// that fails to record, never one that records when it needn't, so either
// plane saying yes is enough and neither can silently switch the other off.
// That keeps BD_VERSIONED_HISTORY_ENABLED=1 working for a one-off run without
// letting an unset env var countermand a store that is recording.
//
// A store that cannot answer is not an error: proxied and no-db workspaces
// have no settings plane to read. Those fall back to viper alone rather than
// failing the command, because a store-config read is not worth breaking
// every bd invocation over.
func versionedHistoryEnabled(ctx context.Context, st storage.DoltStorage) bool {
	if config.GetBool(versionedHistorySettingKey) {
		// Already on via env or yaml; skip the store read entirely. This is
		// also what keeps the common path cheap when someone is driving the
		// feature from the environment.
		return true
	}
	return versionedHistoryStoreSetting(ctx, st)
}

// versionedHistoryStoreSetting reads the switch from the workspace settings
// plane, answering false for every reason a read can fail to produce a true.
//
// "EVERY reason" INCLUDES A PANIC, and that is the whole point of the defer
// below rather than an oversight to tidy away. wireStorageDecorators calls
// this on every bd invocation, before anything has established that the store
// at the bottom of the chain can answer a config question at all -- so a
// store that cannot is not a hypothetical, it is a store that takes the
// process down. The shape that found it: a test double embedding a nil
// storage.DoltStorage, whose promoted WorkspaceConfig SIGSEGVs on call. The
// blast radius of letting that through is not one command; it is every bd
// invocation against any store whose settings plane is absent or half-built,
// for a read whose answer only ever decides whether to record versions.
// (bee-ghosttrack, #6661 second review.)
//
// A CAPABILITY ASSERTION ON WorkspaceConfig CANNOT REPLACE THIS, because
// WorkspaceConfig is declared on storage.DoltStorage itself
// (internal/storage/storage.go): every store satisfies an
// interface{ WorkspaceConfig() ... } assertion, including the one that panics
// when the method is actually called. That assertion would pass and the panic
// would happen anyway.
//
// AN ASSERTION ON storage.VersionedHistoryConfigurer IS DIFFERENT, and it is
// the primary guard now -- see versionedHistoryEnabledForWiring below.
// SetVersionedHistoryEnabled is NOT part of DoltStorage, so a store that
// embeds a nil DoltStorage does not promote it and does not satisfy that
// interface. Verified, not assumed: the untouched fakeProbingDoltStore fails
// that assertion, and the guard alone keeps it out of the store read with no
// recover in play (TestWireStorageDecoratorsSkipsTheStoreReadForANonConfigurer).
// An earlier revision of this comment claimed no capability assertion could
// help; that was true only of the WorkspaceConfig shape above, and it is why
// the recover was reached for first. (bee-ghosttrack, #6661 third review.)
//
// The recover stays anyway, because the guard does not cover every caller:
// versionedHistoryEnabled is also called by `bd versions` (versions.go), where
// the read is the point and no capability gate precedes it, and because a
// store that DOES implement the capability can still have a half-built
// settings plane. Guard first, recover as the backstop.
//
// A typed nil needs no separate guard for the same reason it needs no
// reflection: if its WorkspaceConfig panics, the recover answers false; if it
// returns (nil, nil) instead, the cfg == nil check answers false. Both arms
// are already here.
//
// The answer when the store cannot be read is false -- recording off, which
// is what the feature ships as. That is a real tradeoff and not a free one: a
// store that IS recording, whose settings read panics, is read as not
// recording, and a write during that window advances nothing. It is still the
// right side to fail to, because the alternative is that bd does not run at
// all, and a store too broken to answer a settings query is not one that was
// successfully recording versions a moment ago.
func versionedHistoryStoreSetting(ctx context.Context, st storage.DoltStorage) (enabled bool) {
	if st == nil {
		return false
	}
	defer func() {
		if r := recover(); r != nil {
			// Deliberately swallowed, not re-raised and not logged: this
			// runs on every invocation and its answer is a default, so the
			// honest report is the false below rather than noise on a path
			// the user did not ask about. The named return makes that
			// explicit.
			//
			// This is the opposite choice from the repo's other non-test
			// recover (internal/storage/dolt/transaction.go), which rolls
			// back and then re-panics, and the difference is not stylistic:
			// there a caller must not proceed over a half-applied
			// transaction, so the panic has to keep traveling. Here the
			// caller can proceed perfectly well, because what it wanted was
			// a boolean with a documented default.
			enabled = false
		}
	}()
	cfg, err := st.WorkspaceConfig()
	if err != nil || cfg == nil {
		return false
	}
	res, err := cfg.GetSetting(ctx, issueops.GetSettingRequest{Key: versionedHistorySettingKey})
	if err != nil {
		return false
	}
	return parseSettingBool(res.Value)
}

// parseSettingBool reads the stored string the way `bd config set` writes it.
// Unset is "" and a nil error by contract (WorkspaceConfig.GetSetting), which
// lands here as false -- the correct default for a flag that ships off.
func parseSettingBool(v string) bool {
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	return err == nil && b
}

// versionedHistoryEnabledForWiring answers the switch for wireStorageDecorators,
// which is the one caller that runs on EVERY bd invocation.
//
// It differs from versionedHistoryEnabled in one deliberate way: it will not
// read the store's settings plane unless the store could actually act on the
// answer. wireStorageDecorators' whole use for the result is
// applyVersionedHistoryConfig, which needs a storage.VersionedHistoryConfigurer;
// a store that is not one cannot record versions however the question is
// answered, so the read is pure cost and, for a store whose settings plane is
// absent or half-built, pure risk. This is what took CI red at 32d4f966e: the
// read panicked at wiring time on a test double embedding a nil
// storage.DoltStorage, which is a panic in a path whose answer only ever
// decides whether to record versions.
//
// The ordering matters and is not interchangeable with applyVersionedHistoryConfig's
// own assertion. The viper plane is read FIRST and unconditionally, so a store
// that is set-but-unsupported still reaches applyVersionedHistoryConfig with
// enabled=true and still gets the warning it prints. Only the STORE-plane read
// is gated. The one case that loses its warning is a store whose own settings
// plane says true but which lacks the capability -- and `bd config set` could
// not have written that plane on such a store in the first place.
//
// (bee-ghosttrack, #6661 third review, blocking finding 1.)
func versionedHistoryEnabledForWiring(st storage.DoltStorage) bool {
	if config.GetBool(versionedHistorySettingKey) {
		return true
	}
	if _, canRecord := st.(storage.VersionedHistoryConfigurer); !canRecord {
		return false
	}
	return versionedHistoryStoreSetting(context.Background(), st)
}
