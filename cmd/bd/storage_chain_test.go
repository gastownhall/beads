package main

import (
	"context"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/externaldeps"
	"github.com/steveyegge/beads/internal/telemetry"
)

// stubChainStore is a stand-in for a concrete DoltStorage. The embedded
// interface exists only for decorator identity tests; ActiveDatabaseSize is
// implemented explicitly so the sizing-capability test never reaches a nil
// promoted method.
type stubChainStore struct {
	storage.DoltStorage
	databaseSize int64
}

func (s *stubChainStore) ActiveDatabaseSize(context.Context) (int64, error) {
	return s.databaseSize, nil
}

// stubPrefixChainStore is a concrete store that DOES implement the optional
// prefix-read capability, standing in for DoltStore/EmbeddedDoltStore in the
// discovery test below. It records the prefixes it was asked for, and counts
// full reads, so the test can prove the call reached the raw store's fast path
// rather than a fallback. The full read returns the same rows the prefix read
// would, as the real stores do, so only those counters can tell the arms apart.
type stubPrefixChainStore struct {
	stubChainStore
	prefixReads []string
	fullReads   int
}

func (s *stubPrefixChainStore) GetConfigByPrefix(_ context.Context, prefix string) (map[string]string, error) {
	s.prefixReads = append(s.prefixReads, prefix)
	return map[string]string{prefix + "one": "1"}, nil
}

func (s *stubPrefixChainStore) GetAllConfig(context.Context) (map[string]string, error) {
	s.fullReads++
	return map[string]string{"kv.mail.one": "1"}, nil
}

// clearTelemetryEnv is defined once for the package, in
// command_telemetry_test.go; it unsets every BD_OTEL_* / OTEL_* variable
// telemetry.Enabled or the SDK looks at, so each test starts from a known
// baseline.

func TestWireStorageDecorators_NilStorePassesThrough(t *testing.T) {
	if got := wireStorageDecorators(nil, hooks.NewRunner("/nonexistent"), false); got != nil {
		t.Errorf("wireStorageDecorators(nil, ...) = %v; want nil", got)
	}
}

func TestWireStorageDecorators_TelemetryOff_HookOn(t *testing.T) {
	clearTelemetryEnv(t)
	raw := &stubChainStore{}
	got := wireStorageDecorators(raw, hooks.NewRunner("/nonexistent"), false)

	hf, ok := got.(*storage.HookFiringStore)
	if !ok {
		t.Fatalf("outer decorator: got %T; want *storage.HookFiringStore", got)
	}
	ext, ok := hf.Unwrap().(*externaldeps.Store)
	if !ok {
		t.Fatalf("second decorator: got %T; want *externaldeps.Store", hf.Unwrap())
	}
	if inner := ext.Unwrap(); inner.(*stubChainStore) != raw {
		t.Errorf("external dependency policy should wrap raw store directly when telemetry off; got %T", inner)
	}
}

// Asserts the full HookFiringStore → externaldeps.Store → InstrumentedStorage
// → raw chain that the rest of bd depends on for storage spans + bd.storage.* / bd.issue.count
// metrics. This is the regression test for the original PR-3475 bug, where
// WrapStorage was implemented but never called.
func TestWireStorageDecorators_TelemetryOn_HookOn(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("BD_OTEL_STDOUT", "true")
	raw := &stubChainStore{}
	got := wireStorageDecorators(raw, hooks.NewRunner("/nonexistent"), false)

	hf, ok := got.(*storage.HookFiringStore)
	if !ok {
		t.Fatalf("outer decorator: got %T; want *storage.HookFiringStore", got)
	}
	ext, ok := hf.Unwrap().(*externaldeps.Store)
	if !ok {
		t.Fatalf("second decorator: got %T; want *externaldeps.Store", hf.Unwrap())
	}
	inst, ok := ext.Unwrap().(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatalf("middle decorator: got %T; want *telemetry.InstrumentedStorage", ext.Unwrap())
	}
	if inner := inst.Unwrap(); inner.(*stubChainStore) != raw {
		t.Errorf("InstrumentedStorage.Unwrap() should return raw store; got %T", inner)
	}

	if peeled := storage.UnwrapStore(got); peeled.(*stubChainStore) != raw {
		t.Errorf("storage.UnwrapStore should peel both decorator layers; got %T", peeled)
	}
}

func TestDoltBackupSizeUnwrapsStorageDecorators(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("BD_OTEL_STDOUT", "true")
	raw := &stubChainStore{databaseSize: 99}
	wrapped := wireStorageDecorators(raw, hooks.NewRunner("/nonexistent"), false)

	size, available, err := doltBackupSizeForStore(t.Context(), wrapped)
	if err != nil {
		t.Fatalf("doltBackupSizeForStore: %v", err)
	}
	if !available || size != 99 {
		t.Fatalf("doltBackupSizeForStore = (%d, %v), want (99, true)", size, available)
	}
}

func TestGCStoreSizeUnwrapsStorageDecorators(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("BD_OTEL_STDOUT", "true")
	raw := &stubChainStore{databaseSize: 99}
	wrapped := wireStorageDecorators(raw, hooks.NewRunner("/nonexistent"), false)

	if got := storeSizeBytesForStore(t.Context(), wrapped); got != 99 {
		t.Fatalf("storeSizeBytesForStore = %d, want 99", got)
	}
}

// TestKVPrefixReaderUnwrapsStorageDecorators pins the read behind
// `bd kv list --prefix`. That flag reads only the rows under a prefix when the
// store offers the optional GetConfigByPrefix capability, and falls back to a
// full GetAllConfig scan when it does not — a fallback that returns identical
// rows, so a discovery miss produces no error, no log, and no failing test
// while costing exactly the full-table serialization the flag exists to avoid.
// Every decorator wireStorageDecorators installs embeds the
// storage.DoltStorage interface, which does not declare GetConfigByPrefix, so
// discovery only works if it peels. Covers every chain configuration, and
// drives readKVListConfig, the read the command performs, so a bare assertion
// on that path fails here rather than only one inside the discovery helper.
func TestKVPrefixReaderUnwrapsStorageDecorators(t *testing.T) {
	for _, tc := range []struct {
		name          string
		telemetryOn   bool
		hooksDisabled bool
	}{
		{name: "TelemetryOff_HookOn"},
		{name: "TelemetryOn_HookOn", telemetryOn: true},
		{name: "TelemetryOff_HookDisabled", hooksDisabled: true},
		{name: "TelemetryOn_HookDisabled", telemetryOn: true, hooksDisabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearTelemetryEnv(t)
			if tc.telemetryOn {
				t.Setenv("BD_OTEL_STDOUT", "true")
			}
			raw := &stubPrefixChainStore{}
			wrapped := wireStorageDecorators(raw, hooks.NewRunner("/nonexistent"), tc.hooksDisabled)

			got, err := readKVListConfig(t.Context(), wrapped, "mail.")
			if err != nil {
				t.Fatalf("readKVListConfig: %v", err)
			}
			if raw.fullReads != 0 {
				t.Fatalf("prefix read fell back to %d full GetAllConfig scan(s) through the wired chain (%T); kv list --prefix would silently full-scan", raw.fullReads, wrapped)
			}
			if want := []string{"kv.mail."}; !reflect.DeepEqual(raw.prefixReads, want) {
				t.Errorf("raw store prefix reads = %v; want %v — the read never reached the concrete store", raw.prefixReads, want)
			}
			if want := (map[string]string{"kv.mail.one": "1"}); !reflect.DeepEqual(got, want) {
				t.Errorf("readKVListConfig = %v; want %v", got, want)
			}
		})
	}
}

// TestKVPrefixReaderAbsentCapabilityFallsBack keeps the test above honest:
// discovery must report a store that lacks the capability as incapable, so the
// caller takes the GetAllConfig fallback. The telemetry-on case also pins the
// deliberate choice of peel-to-raw over peel-until-implements —
// InstrumentedStorage does implement GetConfigByPrefix, and we peel past it,
// trading the storage span on prefix reads for the repo's standard UnwrapStore
// idiom. Flipping that trade should have to flip this assertion.
func TestKVPrefixReaderAbsentCapabilityFallsBack(t *testing.T) {
	for _, telemetryOn := range []bool{false, true} {
		name := "TelemetryOff"
		if telemetryOn {
			name = "TelemetryOn"
		}
		t.Run(name, func(t *testing.T) {
			clearTelemetryEnv(t)
			if telemetryOn {
				t.Setenv("BD_OTEL_STDOUT", "true")
			}
			raw := &stubChainStore{}
			wrapped := wireStorageDecorators(raw, hooks.NewRunner("/nonexistent"), false)

			if _, ok := configPrefixReaderFor(wrapped); ok {
				t.Errorf("store without GetConfigByPrefix reported as prefix-capable through %T", wrapped)
			}
		})
	}
}

func TestWireStorageDecorators_TelemetryOn_HookDisabled(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("BD_OTEL_STDOUT", "true")
	raw := &stubChainStore{}
	got := wireStorageDecorators(raw, hooks.NewRunner("/nonexistent"), true)

	ext, ok := got.(*externaldeps.Store)
	if !ok {
		t.Fatalf("outer decorator: got %T; want *externaldeps.Store", got)
	}
	inst, ok := ext.Unwrap().(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatalf("expected *telemetry.InstrumentedStorage when hooks disabled; got %T", ext.Unwrap())
	}
	if inner := inst.Unwrap(); inner.(*stubChainStore) != raw {
		t.Errorf("InstrumentedStorage.Unwrap() should return raw store; got %T", inner)
	}
}

func TestWireStorageDecorators_TelemetryOff_HookDisabled(t *testing.T) {
	clearTelemetryEnv(t)
	raw := &stubChainStore{}
	got := wireStorageDecorators(raw, hooks.NewRunner("/nonexistent"), true)
	ext, ok := got.(*externaldeps.Store)
	if !ok {
		t.Fatalf("outer decorator: got %T; want *externaldeps.Store", got)
	}
	if ext.Unwrap().(*stubChainStore) != raw {
		t.Errorf("with telemetry off and hooks disabled, expected external decorator around raw store; got %T", ext.Unwrap())
	}
}

func TestWireStorageDecorators_NilHookRunner(t *testing.T) {
	clearTelemetryEnv(t)
	raw := &stubChainStore{}
	got := wireStorageDecorators(raw, nil, false)
	ext, ok := got.(*externaldeps.Store)
	if !ok {
		t.Fatalf("outer decorator: got %T; want *externaldeps.Store", got)
	}
	if ext.Unwrap().(*stubChainStore) != raw {
		t.Errorf("with telemetry off and nil hookRunner, expected external decorator around raw store; got %T", ext.Unwrap())
	}
}
