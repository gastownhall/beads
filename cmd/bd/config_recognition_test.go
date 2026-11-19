package main

import (
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// TestAuditConfigRecognition pins GH#5787: the config-set recognizer accepts
// every key the yaml-only writer accepts — the exact YamlOnlyKeys entries
// and the yaml-only namespaces (audit.*, list.*, mail.delegate) — because
// `bd config set` routes those keys to config.yaml and several of them
// (audit.enabled above all) are documented as config-set invocations.
// Unknown keys stay unrecognized so the typo warning keeps doing its job;
// protected keys (no-db, issue-prefix) keep their own dedicated refusal,
// which runs before the recognizer in runConfigSet.
func TestAuditConfigRecognition(t *testing.T) {
	for key := range config.YamlOnlyKeys {
		if !isRecognizedConfigKey(key) {
			t.Errorf("yaml-only key %q must be a recognized config key: bd config set routes it to config.yaml", key)
		}
	}

	for _, key := range []string{
		"audit.enabled", "audit.retention-days",
		"list.limit", "mail.delegate",
	} {
		if !isRecognizedConfigKey(key) {
			t.Errorf("representative yaml-only key %q must be recognized", key)
		}
	}

	for _, key := range []string{
		"auditt.enabled", "lists.limit", "node-iid", "totally.unknown", "custom-namespace",
	} {
		if isRecognizedConfigKey(key) {
			t.Errorf("unknown key %q must stay unrecognized so the typo warning keeps firing", key)
		}
	}
}
