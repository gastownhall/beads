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
func versionedHistoryStoreSetting(ctx context.Context, st storage.DoltStorage) bool {
	if st == nil {
		return false
	}
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
