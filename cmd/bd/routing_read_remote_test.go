package main

import (
	"context"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/routing"
	"github.com/steveyegge/beads/internal/storage"
)

// routingSettingsFake answers the two settings reads routing makes and counts
// them. The embedded DoltStorage is nil: any other call panics, so the test
// says so if routing starts reading something else.
type routingSettingsFake struct {
	storage.DoltStorage
	remote   bool
	reads    int
	settings map[string]string
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
	if f.settings != nil {
		return f.settings, nil
	}
	return map[string]string{"routing.mode": "auto"}, nil
}

// routingKeysFake is a store with the keyed-read capability. Its GetAllConfig is
// routingSettingsFake's, so a read of the whole table shows up in reads.
type routingKeysFake struct {
	routingSettingsFake
	keyReads int
	asked    []string
}

func (f *routingKeysFake) GetConfigKeys(_ context.Context, keys ...string) (map[string]string, error) {
	f.keyReads++
	f.asked = keys
	out := make(map[string]string)
	for _, key := range keys {
		if v, ok := f.settings[key]; ok {
			out[key] = v
		}
	}
	return out, nil
}

// routingDecorator stands in for cmd/bd's storage decorators, which embed the
// DoltStorage interface and so hide optional capabilities from an assertion.
type routingDecorator struct{ storage.DoltStorage }

func (d routingDecorator) Unwrap() storage.DoltStorage { return d.DoltStorage }

// TestResolveRoutingConfigAsksOnlyForItsKeys: a store with the keyed read answers
// from it, through the decorator chain, and never reads the whole table, whose
// kv.* rows can be megabytes.
func TestResolveRoutingConfigAsksOnlyForItsKeys(t *testing.T) {
	initConfigForTest(t)
	store := &routingKeysFake{routingSettingsFake: routingSettingsFake{settings: map[string]string{
		"routing.mode":       "explicit",
		"kv.memory.huge.row": "not a routing key",
	}}}

	got := resolveRoutingConfig(context.Background(), routingDecorator{store})
	if got.Mode != "explicit" {
		t.Errorf("Mode = %q, want the store's explicit", got.Mode)
	}
	if store.keyReads != 1 || store.reads != 0 {
		t.Errorf("keyed reads = %d, whole-table reads = %d; want 1 and 0", store.keyReads, store.reads)
	}
	if !slices.Equal(store.asked, routingConfigKeys) {
		t.Errorf("asked for %v, want routingConfigKeys %v", store.asked, routingConfigKeys)
	}
}

// TestResolveRoutingConfigReadsTheSettingsOnce pins what `bd create` and the
// lookup fallback both build their routing from: one read of the store, with the
// legacy contributor.* keys filling only what the routing.* keys leave empty.
func TestResolveRoutingConfigReadsTheSettingsOnce(t *testing.T) {
	initConfigForTest(t)
	for _, tc := range []struct {
		name     string
		settings map[string]string
		want     routing.RoutingConfig
	}{
		{
			name:     "routing keys",
			settings: map[string]string{"routing.mode": "explicit", "routing.contributor": " ~/plan ", "routing.default": "~/default", "routing.maintainer": "~/main"},
			want:     routing.RoutingConfig{Mode: "explicit", ContributorRepo: "~/plan", DefaultRepo: "~/default", MaintainerRepo: "~/main"},
		},
		{
			name:     "legacy keys fill empty routing keys",
			settings: map[string]string{"contributor.auto_route": "true", "contributor.planning_repo": "~/legacy"},
			want:     routing.RoutingConfig{Mode: "auto", ContributorRepo: "~/legacy"},
		},
		{
			name:     "routing keys win over legacy keys",
			settings: map[string]string{"routing.mode": "explicit", "routing.contributor": "~/plan", "contributor.auto_route": "true", "contributor.planning_repo": "~/legacy"},
			want:     routing.RoutingConfig{Mode: "explicit", ContributorRepo: "~/plan"},
		},
		{
			name:     "auto_route other than true leaves the mode empty",
			settings: map[string]string{"contributor.auto_route": "false"},
			want:     routing.RoutingConfig{},
		},
		{
			name:     "no settings",
			settings: map[string]string{"unrelated": "x"},
			want:     routing.RoutingConfig{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &routingSettingsFake{settings: tc.settings}
			got := resolveRoutingConfig(context.Background(), store)
			if *got != tc.want {
				t.Errorf("resolveRoutingConfig = %+v, want %+v", *got, tc.want)
			}
			if store.reads != 1 {
				t.Errorf("settings were read %d times, want 1", store.reads)
			}
		})
	}
}

// TestRoutingSettingsAreLocalOnlyOnARemoteBackend pins one rule for every routing
// read: getRoutingConfigValue, and resolveRoutingConfig under `bd create` and the
// lookup fallback's determineAutoRoutedRepoPath. On a remote backend none reads
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
	_ = resolveRoutingConfig(ctx, remote)
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
