package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/kvkeys"
)

// TestConfigShowExcludesTheKVPlane pins that `bd config show` does not print
// memories under source=database.
//
// It asserts the pure half of collectDatabaseEntries rather than shelling out,
// for the reason TestInfoConfigExcludesTheKVPlane does: the rest of that
// function is store acquisition, and what can regress is the filter.
//
// Why this route needed telling separately: it reads the store's GetAllConfig
// RAW rather than through issueops.WorkspaceConfig, so it inherited neither the
// enumeration exclusion nor the point-read refusal, and it prints values in
// FULL because it is the operator's provenance view. Every `bd remember` memory
// went into every terminal and pasted bug report that ran the command.
func TestConfigShowExcludesTheKVPlane(t *testing.T) {
	stored := map[string]string{
		"issue_prefix":    "bd",
		"custom.statuses": "awaiting_review",
		kvkeys.MemoryConfigKeyPrefix + "deploy-notes": "the staging deploy token is sk-live-000",
		kvkeys.Prefix + "release.channel":             "beta",
		// Near misses that must SURVIVE: the rule is an anchored prefix.
		"kvetch":                    "not under the kv prefix",
		"custom.mentions.kv.inside": "kept",
	}

	got := databaseConfigEntries(stored)

	seen := make(map[string]string, len(got))
	for _, entry := range got {
		if strings.HasPrefix(entry.Key, kvkeys.Prefix) {
			t.Errorf("bd config show would print %q = %q under source=database: the kv plane is user data "+
				"riding in the settings table, and this view prints values in full", entry.Key, entry.Value)
		}
		if entry.Source != "database" {
			t.Errorf("entry %q carries source %q, want %q", entry.Key, entry.Source, "database")
		}
		seen[entry.Key] = entry.Value
	}
	for key, want := range map[string]string{
		"issue_prefix":              "bd",
		"custom.statuses":           "awaiting_review",
		"kvetch":                    "not under the kv prefix",
		"custom.mentions.kv.inside": "kept",
	} {
		if got, ok := seen[key]; !ok || got != want {
			t.Errorf("config show dropped or altered %q: got %q (present=%v), want %q; only keys under %q may go",
				key, got, ok, want, kvkeys.Prefix)
		}
	}
}
