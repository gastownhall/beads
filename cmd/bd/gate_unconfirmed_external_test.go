package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/types"
)

// unconfirmedBeadGateGetter answers every lookup with a pre-built error, the
// way routedBeadGateGetter hands prefixRoutedBeadGateTarget's error through,
// so the gate outcome of a real unconfirmed error can be asserted without a
// store.
type unconfirmedBeadGateGetter struct{ err error }

func (g unconfirmedBeadGateGetter) GetIssue(context.Context, string) (*types.Issue, error) {
	return nil, g.err
}

// TestBeadGateUnconfirmableExternalTarget pins AC-5 of #5861 item 2: a routed
// external target that cannot be confirmed — an unreachable endpoint, an
// unreadable sidecar, a managed-local sidecar, no sidecar, or a target naming
// no database — leaves the bead gate pending as errBeadGateTargetUnconfirmed:
// never a bare error the gate would report instead of pending, never a
// nil-issue miss this rig's store would read as the bead being gone, and
// never a fallback that answers from anywhere but the target itself.
//
// Before the fix every external target was unconfirmable this way (AC-2's
// red); this test keeps the failure modes of the now-attempted read honest.
func TestBeadGateUnconfirmableExternalTarget(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	townBeadsDir := filepath.Join(tmpDir, ".beads")
	rigBeadsDir := filepath.Join(tmpDir, "rig", ".beads")
	for _, dir := range []string{townBeadsDir, rigBeadsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(townBeadsDir, "routes.jsonl"), []byte(`{"prefix":"gt-","path":"rig"}`), 0o644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}
	oldDBPath := dbPath
	dbPath = filepath.Join(townBeadsDir, "dolt")
	t.Cleanup(func() { dbPath = oldDBPath })

	writeRig := func(t *testing.T, database string, sidecar *configfile.ProxiedServerClientInfo) {
		t.Helper()
		cfg := configfile.DefaultConfig()
		cfg.DoltMode = configfile.DoltModeProxiedServer
		cfg.DoltDatabase = database
		if err := cfg.Save(rigBeadsDir); err != nil {
			t.Fatalf("save rig metadata.json: %v", err)
		}
		// Start every case from no sidecar, then write the requested one, so
		// cases do not inherit the previous case's file.
		if err := os.Remove(configfile.ProxiedServerClientInfoPath(rigBeadsDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("remove rig sidecar: %v", err)
		}
		if sidecar != nil {
			if err := configfile.SaveProxiedServerClientInfo(rigBeadsDir, sidecar); err != nil {
				t.Fatalf("save rig sidecar: %v", err)
			}
		}
	}

	unreachable := &configfile.ProxiedServerClientInfo{
		External: &configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: 1},
	}
	cases := []struct {
		name     string
		database string
		sidecar  *configfile.ProxiedServerClientInfo
		rawJSON  bool // write a malformed sidecar instead
		wantText string
	}{
		{"unreachable endpoint", "gt", unreachable, false, "unreachable"},
		{"malformed sidecar", "gt", nil, true, configfile.ProxiedServerClientInfoPath(rigBeadsDir)},
		{"managed-local sidecar", "gt", &configfile.ProxiedServerClientInfo{}, false, "proxied-server route"},
		{"absent sidecar", "gt", nil, false, "proxied-server route"},
		{"target naming no database", "", unreachable, false, "dolt_database"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeRig(t, tc.database, tc.sidecar)
			if tc.rawJSON {
				if err := os.WriteFile(configfile.ProxiedServerClientInfoPath(rigBeadsDir), []byte("{not json"), 0o644); err != nil {
					t.Fatalf("write malformed sidecar: %v", err)
				}
			}
			issue, err := prefixRoutedBeadGateTarget(ctx, "gt-target1")
			if issue != nil {
				t.Fatalf("an unconfirmable target must not return an issue, got %+v", issue)
			}
			if err == nil {
				t.Fatal("an unconfirmable target must not read as a confirmed miss (nil issue, nil error)")
			}
			if !errors.Is(err, errBeadGateTargetUnconfirmed) {
				t.Fatalf("must wrap errBeadGateTargetUnconfirmed so the gate stays pending, got: %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantText) {
				t.Fatalf("error should name the cause (%q), got: %v", tc.wantText, err)
			}
			// And the gate outcome: pending with the unconfirmed reason, not
			// resolved (gone) and not a hard error.
			resolved, reason, gateErr := checkBeadGate(ctx, unconfirmedBeadGateGetter{err: err}, "gt-target1")
			if gateErr != nil {
				t.Fatalf("an unconfirmed target is a pending gate, not an error: %v", gateErr)
			}
			if resolved {
				t.Fatalf("an unconfirmed target must not resolve the gate: %s", reason)
			}
			if !strings.Contains(reason, "cannot confirm") {
				t.Fatalf("pending reason should say the absence is unconfirmed, got: %q", reason)
			}
		})
	}
}
