package main

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// routingSettingsFake answers the two settings reads routing makes and counts
// them. The embedded DoltStorage is nil: any other call panics, so the test
// says so if routing starts reading something else.
type routingSettingsFake struct {
	storage.DoltStorage
	remote bool
	reads  int
}

func (f *routingSettingsFake) IsRemoteBackendStore() bool { return f.remote }

func (f *routingSettingsFake) GetConfig(_ context.Context, key string) (string, error) {
	f.reads++
	if key == "routing.mode" {
		return "auto", nil
	}
	return "", nil
}

func (f *routingSettingsFake) GetAllConfig(context.Context) (map[string]string, error) {
	f.reads++
	return map[string]string{"routing.mode": "auto"}, nil
}

// TestRoutingSettingsAreLocalOnlyOnARemoteBackend pins one rule for both
// routing reads: `bd create`'s per-key getRoutingConfigValue and the lookup
// fallback's determineAutoRoutedRepoPath. On a remote backend neither reads
// the server's shared settings document (routing targets are local paths), so
// create and lookup cannot route differently for the same workspace. A local
// store is still consulted.
func TestRoutingSettingsAreLocalOnlyOnARemoteBackend(t *testing.T) {
	initConfigForTest(t)
	t.Setenv("BEADS_ROUTING_MODE", "")
	t.Setenv("BD_ROUTING_MODE", "")
	ctx := context.Background()

	remote := &routingSettingsFake{remote: true}
	if got := getRoutingConfigValue(ctx, remote, "routing.mode"); got != "" {
		t.Errorf("getRoutingConfigValue(remote, routing.mode) = %q, want \"\" (local config only)", got)
	}
	_, _ = determineAutoRoutedRepoPath(ctx, remote)
	if remote.reads != 0 {
		t.Errorf("a remote backend's settings were read %d times for routing, want 0", remote.reads)
	}

	local := &routingSettingsFake{}
	if got := getRoutingConfigValue(ctx, local, "routing.mode"); got != "auto" {
		t.Errorf("getRoutingConfigValue(local, routing.mode) = %q, want the store's \"auto\"", got)
	}
	if local.reads != 1 {
		t.Errorf("a local store's settings were read %d times, want 1", local.reads)
	}
}
