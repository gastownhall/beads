package main

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/issueops"
)

// The resolver's own tests -- the table of ways a store can fail to answer, how the
// stored string is read back, the capability guard, the recover, and the raw-store
// ordering -- moved to internal/versionedhistory with the resolver, assertions
// unchanged, which is what shows the move preserved #6661's behavior. What stays
// here is the one thing that is cmd/bd's: how the decorator chain is wired.

// configProbeStore is a raw store at the bottom of a decorator chain whose
// settings plane answers however a case needs. It counts the reads so a test can
// prove it was actually asked -- or, here, that it was NOT.
//
// Every other method promotes from the embedded nil interface and panics if
// called, which is deliberate: nothing here should be calling them.
type configProbeStore struct {
	storage.DoltStorage
	cfg     issueops.WorkspaceConfig
	calls   int
	enabled bool
}

func (s *configProbeStore) WorkspaceConfig() (issueops.WorkspaceConfig, error) {
	s.calls++
	return s.cfg, nil
}

// SetVersionedHistoryEnabled makes this store a storage.VersionedHistoryConfigurer,
// which is what would earn it a store-plane read if wiring still did one.
func (s *configProbeStore) SetVersionedHistoryEnabled(enabled bool) { s.enabled = enabled }

// settingsPlane is a workspace settings plane with one canned answer.
type settingsPlane struct {
	issueops.WorkspaceConfig
	value string
}

func (p *settingsPlane) GetSetting(context.Context, issueops.GetSettingRequest) (issueops.SettingResult, error) {
	return issueops.SettingResult{Key: versionedHistorySettingKey, Value: p.value}, nil
}

// TestWireStorageDecoratorsLeavesActivationToTheFactories pins the other half of
// moving activation to where a store is constructed.
//
// wireStorageDecorators used to be the one place the switch was read and applied,
// and it runs on the ONE store a command opens for its own workspace. Every store
// a routed update, a routed close or `bd create --repo` opens for ANOTHER
// workspace is built by newDoltStoreFromConfig and never passed through here, so
// it recorded nothing while the command reported success. Activation now happens
// in the factories, for every store they hand back; if wiring ALSO read the row it
// would read it twice per open, and would read as the place that activates -- the
// misreading that produced the defect.
//
// The count is the assertion that matters, not "it survived": the store here would
// answer true, and would be switched on, if wiring still consulted it.
func TestWireStorageDecoratorsLeavesActivationToTheFactories(t *testing.T) {
	for _, tt := range []struct {
		name string
		env  string
	}{
		{"the store's row says on", ""},
		{"the environment says on", "1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			clearTelemetryEnv(t)
			t.Setenv("BD_VERSIONED_HISTORY_ENABLED", tt.env)
			// config.GetBool answers false until viper is initialised, so without
			// this the env subtest would pass whatever wiring did with the env.
			if err := config.Initialize(); err != nil {
				t.Fatalf("config.Initialize(): %v", err)
			}
			t.Cleanup(func() { _ = config.Initialize() })

			raw := &configProbeStore{cfg: &settingsPlane{value: "true"}}
			chain := wireStorageDecorators(raw, hooks.NewRunner("/nonexistent"), false)

			if chain == nil {
				t.Fatal("wireStorageDecorators returned nil for a non-nil store")
			}
			if raw.calls != 0 {
				t.Errorf("wireStorageDecorators read the store's settings plane %d time(s), want 0: activation belongs to the factory that constructed the store", raw.calls)
			}
			if raw.enabled {
				t.Error("wireStorageDecorators switched versioned history on; only the factory that constructs a store may, at construction")
			}
		})
	}
}
