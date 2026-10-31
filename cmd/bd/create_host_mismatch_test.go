package main

import (
	"errors"
	"maps"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/testutil/legacyworkspace"
)

// TestExistingTargetRemedyNamesTheLauncherHost pins GH#5961's cross-workspace
// gate: an open from here pairs the target's metadata.json with the
// LAUNCHER's config.yaml, and since the yaml host outranks the metadata one,
// a launcher whose dolt.host names a different server than the target's own
// config means no rerun from here can reach the target's server. bd create's
// remedy offers the write inside the target and no rerun; bd migrate-personal's
// says it cannot aim the write at the target's server; both name the
// launcher's dolt.host and the configuration it comes from.
func TestExistingTargetRemedyNamesTheLauncherHost(t *testing.T) {
	// The launcher's own configuration names a local host.
	isolateForeignWorkspaceSignals(t, "dolt:\n  host: 127.0.0.1\n")
	t.Setenv("BD_DOLT_HOST", "")
	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}

	// The target's own config names a remote server.
	target := filepath.Join(t.TempDir(), ".beads")
	legacyworkspace.Layout{Files: map[string]string{
		"metadata.json": `{"backend":"dolt","dolt_database":"beads"}`,
		"config.yaml":   "dolt:\n  mode: server\n  host: db.example.com\n  port: 23481\n",
	}}.Write(t, target)
	before := legacyworkspace.TreeDigest(t, target)

	var refusal *existingTargetRefusal
	if err := refuseExistingCreateTarget(target); !errors.As(err, &refusal) {
		t.Fatalf("refuseExistingCreateTarget = %v, want the embedded-open refusal", err)
	}
	create, planning := refusal.Error(), refusal.planningMsg

	if !strings.Contains(create, "then make the write inside") {
		t.Errorf("bd create's remedy must offer the write inside the target, not a rerun:\n%s", create)
	}
	if strings.Contains(create, "then rerun") {
		t.Errorf("bd create's remedy offers a rerun, which dials the launcher's host and not the target's server:\n%s", create)
	}
	const launcherHost = "this workspace's own configuration names dolt.host \"127.0.0.1\""
	if !strings.Contains(create, launcherHost) {
		t.Errorf("bd create's remedy does not name %q:\n%s", launcherHost, create)
	}
	if !strings.Contains(strings.ToLower(planning), launcherHost) {
		t.Errorf("bd migrate-personal's remedy does not name %q:\n%s", launcherHost, planning)
	}
	// bd create's write inside dials the target's own host, so only
	// migrate-personal's remedy, which cannot make it, names that server.
	if strings.Contains(create, "dolt_server_host\": ") {
		t.Errorf("bd create's remedy has the target stamp a dolt_server_host the launcher's host outranks:\n%s", create)
	}
	if !strings.Contains(planning, "cannot aim this write at its server at \"db.example.com\"") {
		t.Errorf("bd migrate-personal's remedy must say the write cannot be aimed at the target's server:\n%s", planning)
	}
	if strings.Contains(planning, "inside that repository") {
		t.Errorf("bd migrate-personal's remedy offers a write inside its planning repo:\n%s", planning)
	}

	if after := legacyworkspace.TreeDigest(t, target); !maps.Equal(after, before) {
		t.Fatalf("refuseExistingCreateTarget changed the target:\n%s", after)
	}
}
