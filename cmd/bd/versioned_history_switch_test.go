package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/issueops"
)

// These tests exist because the switch had none. `bd versions`' store-config
// read shipped with only an embedded-dolt round-trip behind it
// (versions_switch_embedded_test.go), which skips unless
// BEADS_TEST_EMBEDDED_DOLT=1 and therefore skips in the lane CI actually runs.
// So versionedHistoryStoreSetting reached main with no test that called it at
// all, and the first thing to call it with a store that could not answer was
// CI itself -- as a SIGSEGV, in a function whose doc already promised "a store
// that cannot answer is not an error". (bee-ghosttrack, #6661 second review.)

// configProbeStore is a raw store at the bottom of a decorator chain whose
// settings plane behaves however a case needs: panicking, erroring, absent, or
// answering. It counts the reads so a test can prove it was actually asked,
// rather than passing because the switch short-circuited before reaching it.
//
// Every other method promotes from the embedded nil interface and panics if
// called, which is deliberate: nothing here should be calling them.
type configProbeStore struct {
	storage.DoltStorage
	cfg      issueops.WorkspaceConfig
	err      error
	panicMsg string
	calls    int
	enabled  bool
}

func (s *configProbeStore) WorkspaceConfig() (issueops.WorkspaceConfig, error) {
	s.calls++
	if s.panicMsg != "" {
		panic(s.panicMsg)
	}
	return s.cfg, s.err
}

// SetVersionedHistoryEnabled makes this store a storage.VersionedHistoryConfigurer,
// which is now what earns it the store-plane read at wiring time
// (versionedHistoryEnabledForWiring). Without it this store would be skipped
// before WorkspaceConfig was ever called, and every calls-count assertion
// below would pass for the wrong reason.
func (s *configProbeStore) SetVersionedHistoryEnabled(enabled bool) { s.enabled = enabled }

// nonConfigurerStore is the shape the wiring guard exists for, and it is not a
// hypothetical: it is cmd/bd's own fakeProbingDoltStore (dolt_test.go), which
// embeds a nil storage.DoltStorage and documents that only its own methods may
// be called. SetVersionedHistoryEnabled is not part of DoltStorage, so nothing
// promotes it and this type does not satisfy storage.VersionedHistoryConfigurer
// -- while WorkspaceConfig IS part of DoltStorage, so it promotes, type-checks,
// and SIGSEGVs on call.
type nonConfigurerStore struct {
	storage.DoltStorage
	calls int
}

func (s *nonConfigurerStore) WorkspaceConfig() (issueops.WorkspaceConfig, error) {
	s.calls++
	panic("WorkspaceConfig on a store with no settings plane")
}

// nilDerefStore reproduces the exact shape that broke CI: the method body
// touches the receiver, so a TYPED NIL of this type is non-nil as an interface
// and panics when called.
type nilDerefStore struct {
	storage.DoltStorage
	cfg issueops.WorkspaceConfig
}

func (s *nilDerefStore) WorkspaceConfig() (issueops.WorkspaceConfig, error) {
	return s.cfg, nil
}

// settingsPlane is a workspace settings plane with one canned answer.
type settingsPlane struct {
	issueops.WorkspaceConfig
	value    string
	err      error
	panicMsg string
}

func (p *settingsPlane) GetSetting(context.Context, issueops.GetSettingRequest) (issueops.SettingResult, error) {
	if p.panicMsg != "" {
		panic(p.panicMsg)
	}
	return issueops.SettingResult{Key: versionedHistorySettingKey, Value: p.value}, p.err
}

// TestVersionedHistoryStoreSettingAnswersFalseForAStoreThatCannotAnswer pins
// the contract the function's own doc states, across every way a store can
// fail to produce a true -- including the two that are panics rather than
// errors, which is the pair that took the process down.
func TestVersionedHistoryStoreSettingAnswersFalseForAStoreThatCannotAnswer(t *testing.T) {
	ctx := context.Background()
	var typedNil *nilDerefStore

	for _, tt := range []struct {
		name  string
		store storage.DoltStorage
		want  bool
	}{
		{"nil store", nil, false},
		{"typed nil store", typedNil, false},
		{"WorkspaceConfig panics", &configProbeStore{panicMsg: "no settings plane on this store"}, false},
		{"WorkspaceConfig errors", &configProbeStore{err: fmt.Errorf("server unreachable")}, false},
		{"WorkspaceConfig returns no plane", &configProbeStore{}, false},
		{"GetSetting panics", &configProbeStore{cfg: &settingsPlane{panicMsg: "settings table missing"}}, false},
		{"GetSetting errors", &configProbeStore{cfg: &settingsPlane{err: fmt.Errorf("read failed")}}, false},
		{"unset is off", &configProbeStore{cfg: &settingsPlane{value: ""}}, false},
		{"stored false", &configProbeStore{cfg: &settingsPlane{value: "false"}}, false},
		{"unparseable is off", &configProbeStore{cfg: &settingsPlane{value: "yes please"}}, false},
		{"stored true", &configProbeStore{cfg: &settingsPlane{value: "true"}}, true},
		{"stored 1", &configProbeStore{cfg: &settingsPlane{value: "1"}}, true},
		{"stored true with whitespace", &configProbeStore{cfg: &settingsPlane{value: " true\n"}}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// No recover here on purpose: a panic escaping the call under
			// test must fail this test, not be absorbed by it.
			if got := versionedHistoryStoreSetting(ctx, tt.store); got != tt.want {
				t.Errorf("versionedHistoryStoreSetting = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestWireStorageDecoratorsSurvivesAStoreThatCannotAnswer is the regression
// test for the CI failure itself, at the layer that failed.
//
// wireStorageDecorators reads the switch off the store on EVERY bd
// invocation, so a store whose settings plane cannot be read takes down the
// process at wiring time -- before any command has run, and regardless of
// whether the command had anything to do with versioned history. As a panic
// rather than an error it also takes every other test in the binary with it,
// which is why one unguarded read showed up in CI as a whole red test step.
//
// This is the RECOVER's test: the store here IS a configurer, so the guard
// lets it through to the read, and only the recover keeps the panic from
// escaping. Remove the recover in versionedHistoryStoreSetting and this test
// panics.
func TestWireStorageDecoratorsSurvivesAStoreThatCannotAnswer(t *testing.T) {
	clearTelemetryEnv(t)
	// Env-plane on would short-circuit before the store read and make this
	// test vacuous; the call count below is the backstop that proves it did not.
	t.Setenv("BD_VERSIONED_HISTORY_ENABLED", "")

	raw := &configProbeStore{panicMsg: "WorkspaceConfig on a store with no settings plane"}
	chain := wireStorageDecorators(raw, hooks.NewRunner("/nonexistent"), false)

	if chain == nil {
		t.Fatal("wireStorageDecorators returned nil for a non-nil store")
	}
	if raw.calls != 1 {
		t.Fatalf("raw store's WorkspaceConfig called %d times, want 1; this test is not exercising the store read it claims to", raw.calls)
	}
}

// TestWireStorageDecoratorsSkipsTheStoreReadForANonConfigurer is the GUARD's
// test, and the regression test for the CI failure as it actually happened.
//
// The store read is only ever used to decide whether to call
// SetVersionedHistoryEnabled, so a store that cannot record versions has
// nothing to gain from being asked -- and, if its settings plane is absent or
// half-built, everything to lose: the read panics at wiring time, on every bd
// invocation, before any command has run. That is what took Test
// (macos-latest) and PR Core (wrapper timing) red at 32d4f966e, via
// cmd/bd's own fakeProbingDoltStore.
//
// The assertion that matters is calls == 0: not "it survived" (the recover
// would deliver that too, which is exactly how this regressed the first time)
// but "it was never asked". Restore the ungated read in
// versionedHistoryEnabledForWiring and this fails on the count while still
// surviving, which is the distinction the two tests exist to keep apart.
func TestWireStorageDecoratorsSkipsTheStoreReadForANonConfigurer(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("BD_VERSIONED_HISTORY_ENABLED", "")

	raw := &nonConfigurerStore{}
	if _, isConfigurer := storage.DoltStorage(raw).(storage.VersionedHistoryConfigurer); isConfigurer {
		t.Fatal("nonConfigurerStore satisfies VersionedHistoryConfigurer; this test cannot exercise the guard it claims to")
	}

	chain := wireStorageDecorators(raw, hooks.NewRunner("/nonexistent"), false)

	if chain == nil {
		t.Fatal("wireStorageDecorators returned nil for a non-nil store")
	}
	if raw.calls != 0 {
		t.Fatalf("WorkspaceConfig called %d times on a store that cannot record versions, want 0", raw.calls)
	}
}

// TestVersionedHistoryEnabledForWiringReadsTheStoreWhenItCanRecord is the other
// half: the guard must not be so eager that it stops reading the plane that
// finding 1 existed to start reading. A store that CAN record still gets asked,
// and a stored true still turns recording on.
func TestVersionedHistoryEnabledForWiringReadsTheStoreWhenItCanRecord(t *testing.T) {
	t.Setenv("BD_VERSIONED_HISTORY_ENABLED", "")

	raw := &configProbeStore{cfg: &settingsPlane{value: "true"}}
	if got := versionedHistoryEnabledForWiring(raw); !got {
		t.Fatal("versionedHistoryEnabledForWiring = false for a configurer whose store plane says true")
	}
	if raw.calls != 1 {
		t.Fatalf("store read %d times, want 1", raw.calls)
	}
}

// TestParseSettingBool pins how the stored string is read back. `bd config
// set` stores whatever the user typed, verbatim, so this is the whole
// translation between that and the boolean the chain wires on.
func TestParseSettingBool(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want bool
	}{
		{"", false},
		{"true", true},
		{"TRUE", true},
		{"True", true},
		{"1", true},
		{"t", true},
		{" true ", true},
		{"false", false},
		{"0", false},
		{"off", false},
		{"on", false},
		{"nonsense", false},
	} {
		t.Run(fmt.Sprintf("%q", tt.in), func(t *testing.T) {
			if got := parseSettingBool(tt.in); got != tt.want {
				t.Errorf("parseSettingBool(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
