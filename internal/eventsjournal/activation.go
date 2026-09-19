// Package eventsjournal resolves and applies durable events-journal activation
// to one storage instance.
//
// It is a package rather than a helper inside cmd/bd because two binaries'
// worth of code opens stores that mutate beads: the command surface itself, and
// bd doctor's repair handlers, which live in their own package and cannot
// import package main. Journal coverage has to hold for both — a repair that
// deletes stale issues or imports a JSONL backlog is an ordinary bead mutation,
// and a consumer whose mirror silently diverges because `bd doctor --fix` ran
// is the failure this feature exists to prevent.
//
// Activation is applied by the FACTORIES that construct a store or provider,
// never by the commands that use one. See
// cmd/bd/events_journal_construction_test.go for the structural guard that
// keeps it that way, and the note at the top of cmd/bd/events_journal.go for
// the three ways it was got wrong before.
package eventsjournal

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
)

// ConfigKey is the workspace setting that turns the journal on.
const ConfigKey = "events-journal"

// EnvVar is the environment override for ConfigKey, matching viper's BD_ prefix
// and hyphen-to-underscore mapping.
const EnvVar = "BD_EVENTS_JOURNAL"

// EnabledFor reports activation for the workspace rooted at beadsDir.
//
// Precedence, highest first:
//
//  1. An explicitly set BD_EVENTS_JOURNAL. Environment beats file, as it does
//     everywhere else in bd's configuration, and it is the documented way to
//     turn the journal on for a process without editing a workspace — including
//     a process that writes into several workspaces.
//  2. The TARGET workspace's own config.yaml. The journal records THAT
//     workspace's mutations, so that workspace decides whether they are
//     recorded: a routed `bd create --repo ../other` must neither journal into
//     a project that never asked for it nor skip journaling one that did. The
//     launching workspace's file is deliberately never consulted for a target.
//  3. The default, off.
//
// beadsDir is empty only where no workspace has been resolved yet; there is no
// target to consult, so the process-merged value is the only answer available.
func EnabledFor(beadsDir string) bool {
	return resolveEnabledFor(EnvVar, ConfigKey, beadsDir, false)
}

// resolveEnabledFor is the precedence above, for one env/config key pair and
// its default. AutoPruneEnabledFor resolves the same way against a different
// default, and the two must not drift: they answer for the same workspace in
// the same command, and an operator who sets one in a target's config.yaml
// expects the other to be read from the same place.
func resolveEnabledFor(envVar, configKey, beadsDir string, byDefault bool) bool {
	if enabled, ok := parseBool(os.LookupEnv(envVar)); ok {
		return enabled
	}
	if beadsDir == "" {
		// The process-merged value, resolved the same way as the workspace file
		// below rather than through GetBool. GetBool casts an unparseable value
		// to false, which for a key that DEFAULTS TO TRUE turns a typo into a
		// silent opt-out on one arm and a silent opt-in on the other — the same
		// setting answering differently depending only on whether a workspace
		// had been resolved yet.
		if enabled, ok := parseBool(config.GetString(configKey), true); ok {
			return enabled
		}
		return byDefault
	}
	if enabled, ok := parseBool(config.WorkspaceYamlValue(beadsDir, configKey)); ok {
		return enabled
	}
	return byDefault
}

// parseBool accepts what strconv.ParseBool accepts plus the YAML 1.1 boolean
// spellings — yes/no/on/off, any case.
//
// The extras are not cosmetic. These settings are read out of a hand-edited
// config.yaml, `no` is how a large share of people write false in YAML, and a
// value this function rejects falls through to the key's DEFAULT. For
// events-journal-auto-prune that default is true, so `events-journal-auto-prune:
// no` would have read as "keep pruning" — an unrecognized opt-out that goes on
// deleting the records the operator was trying to preserve. A parser used to
// resolve deletion policy has to understand the way the file is actually
// written.
func parseBool(raw string, present bool) (bool, bool) {
	if !present {
		return false, false
	}
	value := strings.TrimSpace(raw)
	if parsed, err := strconv.ParseBool(value); err == nil {
		return parsed, true
	}
	switch strings.ToLower(value) {
	case "yes", "on":
		return true, true
	case "no", "off":
		return false, true
	}
	return false, false
}

// ActivateStore applies beadsDir's configured activation to a freshly opened
// store. It is written to be used as a deferred rewrite of a factory's named
// results:
//
//	func newX(...) (s storage.DoltStorage, err error) {
//	    defer func() { s, err = eventsjournal.ActivateStore(beadsDir, s, err) }()
//	    ... open and return ...
//	}
//
// The shape is deliberate. It keeps the open and its activation in ONE function
// body, so "constructs a store" and "activates the journal on it" are the same
// syntactic unit — which is what lets the construction guard check the property
// structurally instead of chasing a call graph. A failed open passes through
// untouched; a failed activation closes the store rather than returning one
// that would mutate unrecorded.
func ActivateStore(beadsDir string, s storage.DoltStorage, err error) (storage.DoltStorage, error) {
	if err != nil || s == nil {
		return s, err
	}
	configurer, _ := storage.UnwrapStore(s).(storage.EventsJournalConfigurer)
	if cfgErr := Apply(configurer, EnabledFor(beadsDir)); cfgErr != nil {
		_ = s.Close()
		return nil, cfgErr
	}
	return s, nil
}

// Apply binds a resolved activation to one storage instance. Activation is per
// instance rather than process-global: a process can hold several plumbings at
// once (a routed create holds two stores), and enabling one must not enable the
// rest.
//
// A nil configurer means the plumbing cannot journal. An enabled workspace
// FAILS there rather than running with a silently empty journal: the point of
// the journal is that a consumer can trust its cursor, and a plumbing that
// records nothing while reporting success is the one outcome that breaks that
// trust invisibly. A disabled workspace accepts any plumbing.
func Apply(configurer storage.EventsJournalConfigurer, enabled bool) error {
	if configurer == nil {
		if enabled {
			return fmt.Errorf("storage backend does not support the events journal")
		}
		return nil
	}
	configurer.SetEventsJournalEnabled(enabled)
	return nil
}
