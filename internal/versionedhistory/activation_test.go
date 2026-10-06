package versionedhistory

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/externaldeps"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/telemetry"
	"github.com/steveyegge/beads/issueops"
)

// The resolver's tests. Most of them moved here from cmd/bd with the code they
// test, assertions unchanged: that is what shows the move preserved #6661's
// behavior. The rest are the new claims -- each store (and each provider) answers
// for itself, the environment is process-wide and can only turn recording on, and
// a read that errors is not the same as a row that is absent.

const envVar = "BD_VERSIONED_HISTORY_ENABLED"

// viperEnv sets the switch's environment variable and re-initialises viper, since
// config.GetBool answers false until viper is initialised and so would make every
// env-driven case below pass whatever the code did with the env.
func viperEnv(t *testing.T, value string) {
	t.Helper()
	t.Setenv(envVar, value)
	// Nothing from the machine's own config may decide these cases.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize(): %v", err)
	}
	t.Cleanup(config.ResetForTesting)
}

// captureStderr runs fn with os.Stderr redirected and returns what it wrote. With
// verbose set, debug.Logf writes too.
func captureStderr(t *testing.T, verbose bool, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	debug.SetVerbose(verbose)
	func() {
		defer func() {
			debug.SetVerbose(false)
			os.Stderr = orig
			_ = w.Close()
		}()
		fn()
	}()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// --- fakes -----------------------------------------------------------------

// settingsPlane is a workspace settings plane with one canned answer.
type settingsPlane struct {
	issueops.WorkspaceConfig
	value    string
	err      error
	panicMsg string
	reads    int
}

func (p *settingsPlane) GetSetting(_ context.Context, req issueops.GetSettingRequest) (issueops.SettingResult, error) {
	p.reads++
	if p.panicMsg != "" {
		panic(p.panicMsg)
	}
	return issueops.SettingResult{Key: req.Key, Value: p.value}, p.err
}

// configProbeStore is a raw store at the bottom of a decorator chain whose
// settings plane behaves however a case needs: panicking, erroring, absent, or
// answering. It counts the reads so a test can prove it was actually asked, rather
// than passing because the switch short-circuited before reaching it.
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
	sets     int
}

func (s *configProbeStore) WorkspaceConfig() (issueops.WorkspaceConfig, error) {
	s.calls++
	if s.panicMsg != "" {
		panic(s.panicMsg)
	}
	return s.cfg, s.err
}

// SetVersionedHistoryEnabled makes this store a storage.VersionedHistoryConfigurer,
// which is what earns it the store-plane read at activation time. Without it this
// store would be skipped before WorkspaceConfig was ever called, and every
// calls-count assertion below would pass for the wrong reason.
func (s *configProbeStore) SetVersionedHistoryEnabled(enabled bool) {
	s.enabled = enabled
	s.sets++
}

// nonConfigurerStore is the shape the capability guard exists for, and it is not a
// hypothetical: it is cmd/bd's own fakeProbingDoltStore, which embeds a nil
// storage.DoltStorage and documents that only its own methods may be called.
// SetVersionedHistoryEnabled is not part of DoltStorage, so nothing promotes it and
// this type does not satisfy storage.VersionedHistoryConfigurer -- while
// WorkspaceConfig IS part of DoltStorage, so it promotes, type-checks, and SIGSEGVs
// on call.
type nonConfigurerStore struct {
	storage.DoltStorage
	calls int
}

func (s *nonConfigurerStore) WorkspaceConfig() (issueops.WorkspaceConfig, error) {
	s.calls++
	panic("WorkspaceConfig on a store with no settings plane")
}

// planeOnlyStore can answer a settings query but cannot record versions, which is
// what `bd versions` may be pointed at: the read is the point there.
type planeOnlyStore struct {
	storage.DoltStorage
	cfg   issueops.WorkspaceConfig
	calls int
}

func (s *planeOnlyStore) WorkspaceConfig() (issueops.WorkspaceConfig, error) {
	s.calls++
	return s.cfg, nil
}

// nilDerefStore reproduces the exact shape that broke CI: the method body touches
// the receiver, so a TYPED NIL of this type is non-nil as an interface and panics
// when called.
type nilDerefStore struct {
	storage.DoltStorage
	cfg issueops.WorkspaceConfig
}

func (s *nilDerefStore) WorkspaceConfig() (issueops.WorkspaceConfig, error) {
	return s.cfg, nil
}

// configUseCaseProbe is a unit of work's config use case with one canned answer.
type configUseCaseProbe struct {
	domain.ConfigUseCase
	value string
	err   error
	reads int
	keys  []string
}

func (c *configUseCaseProbe) GetConfig(_ context.Context, key string) (string, error) {
	c.reads++
	c.keys = append(c.keys, key)
	return c.value, c.err
}

// unitProbe is a unit of work that records how it was finished: a settings read
// must be rolled back (Close), never committed.
type unitProbe struct {
	uow.UnitOfWork
	cfg       *configUseCaseProbe
	closed    int
	committed int
}

func (u *unitProbe) ConfigUseCase() domain.ConfigUseCase { return u.cfg }
func (u *unitProbe) Close(context.Context)               { u.closed++ }
func (u *unitProbe) Commit(context.Context, string) error {
	u.committed++
	return nil
}

// providerProbe is a unit-of-work provider that is a VersionedHistoryConfigurer.
type providerProbe struct {
	uow.UnitOfWorkProvider
	unit     *unitProbe
	newErr   error
	panicMsg string
	newCalls int
	enabled  bool
	sets     int
}

func (p *providerProbe) NewUOW(context.Context) (uow.UnitOfWork, error) {
	p.newCalls++
	if p.panicMsg != "" {
		panic(p.panicMsg)
	}
	if p.newErr != nil {
		return nil, p.newErr
	}
	return p.unit, nil
}

func (p *providerProbe) SetVersionedHistoryEnabled(enabled bool) {
	p.enabled = enabled
	p.sets++
}

func newProviderProbe(value string, err error) *providerProbe {
	return &providerProbe{unit: &unitProbe{cfg: &configUseCaseProbe{value: value, err: err}}}
}

// nonConfigurerProvider cannot record versions: it does not implement the capability.
type nonConfigurerProvider struct {
	uow.UnitOfWorkProvider
	newCalls int
}

func (p *nonConfigurerProvider) NewUOW(context.Context) (uow.UnitOfWork, error) {
	p.newCalls++
	panic("NewUOW on a provider that cannot record versions")
}

// --- the resolver, moved from cmd/bd with its assertions unchanged ----------

// TestStoreSettingAnswersFalseForAStoreThatCannotAnswer pins the contract the
// function's own doc states, across every way a store can fail to produce a true
// -- including the two that are panics rather than errors, which is the pair that
// took the process down.
func TestStoreSettingAnswersFalseForAStoreThatCannotAnswer(t *testing.T) {
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
			// No recover here on purpose: a panic escaping the call under test
			// must fail this test, not be absorbed by it.
			if got := StoreSetting(ctx, tt.store); got != tt.want {
				t.Errorf("StoreSetting = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestParseSettingBool pins how the stored string is read back. `bd config set`
// stores whatever the user typed, verbatim, so this is the whole translation
// between that and the boolean the factories activate on.
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

// TestEnabledForStoreSkipsTheStoreReadForANonConfigurer is the capability GUARD's
// test, and the regression test for the CI failure as it actually happened.
//
// The store read is only ever used to decide whether to call
// SetVersionedHistoryEnabled, so a store that cannot record versions has nothing to
// gain from being asked -- and, if its settings plane is absent or half-built,
// everything to lose. That is what took Test (macos-latest) and PR Core (wrapper
// timing) red at 32d4f966e, via cmd/bd's own fakeProbingDoltStore.
//
// The assertion that matters is calls == 0: not "it survived" (the recover would
// deliver that too, which is exactly how this regressed the first time) but "it was
// never asked".
func TestEnabledForStoreSkipsTheStoreReadForANonConfigurer(t *testing.T) {
	raw := &nonConfigurerStore{}
	if _, isConfigurer := storage.DoltStorage(raw).(storage.VersionedHistoryConfigurer); isConfigurer {
		t.Fatal("nonConfigurerStore satisfies VersionedHistoryConfigurer; this test cannot exercise the guard it claims to")
	}
	if EnabledForStore(context.Background(), raw) {
		t.Error("EnabledForStore = true for a store that cannot record versions")
	}
	if raw.calls != 0 {
		t.Fatalf("WorkspaceConfig called %d times on a store that cannot record versions, want 0", raw.calls)
	}
}

// TestEnabledForStoreReadsTheStoreWhenItCanRecord is the other half: the guard must
// not be so eager that it stops reading the plane that finding 1 existed to start
// reading. A store that CAN record still gets asked, and a stored true still turns
// recording on.
func TestEnabledForStoreReadsTheStoreWhenItCanRecord(t *testing.T) {
	raw := &configProbeStore{cfg: &settingsPlane{value: "true"}}
	if got := EnabledForStore(context.Background(), raw); !got {
		t.Fatal("EnabledForStore = false for a configurer whose store plane says true")
	}
	if raw.calls != 1 {
		t.Fatalf("store read %d times, want 1", raw.calls)
	}
}

// TestEnabledReadsTheRowWithoutTheCapabilityGate: `bd versions` is the one caller
// where the read is the point, so it must ask a store that cannot record too.
func TestEnabledReadsTheRowWithoutTheCapabilityGate(t *testing.T) {
	raw := &planeOnlyStore{cfg: &settingsPlane{value: "true"}}
	if _, isConfigurer := storage.DoltStorage(raw).(storage.VersionedHistoryConfigurer); isConfigurer {
		t.Fatal("planeOnlyStore satisfies VersionedHistoryConfigurer; this test cannot exercise what it claims to")
	}
	if !Enabled(context.Background(), raw) {
		t.Error("Enabled = false for a store whose row says true")
	}
	if raw.calls != 1 {
		t.Errorf("store read %d times, want 1", raw.calls)
	}
}

// --- each store answers for itself; the environment only turns recording on --

// TestActivateStoreReadsTheStoresOwnRow is D1 at the unit level: two stores opened
// by one process each read ITS OWN row. The one whose row says on records; the one
// with no row does not, and does not inherit the other's answer.
func TestActivateStoreReadsTheStoresOwnRow(t *testing.T) {
	ctx := context.Background()
	on := &configProbeStore{cfg: &settingsPlane{value: "true"}}
	absent := &configProbeStore{cfg: &settingsPlane{value: ""}}
	off := &configProbeStore{cfg: &settingsPlane{value: "false"}}

	for name, s := range map[string]*configProbeStore{"on": on, "absent": absent, "off": off} {
		got, err := ActivateStore(ctx, s, nil)
		if err != nil || got != storage.DoltStorage(s) {
			t.Fatalf("%s: ActivateStore = (%v, %v), want the same store and no error", name, got, err)
		}
	}
	if !on.enabled {
		t.Error("the store whose row says true was not activated")
	}
	if absent.enabled || off.enabled {
		t.Errorf("a store with no row (%v) or a false row (%v) was activated by another store's answer", absent.enabled, off.enabled)
	}
}

// TestActivateStoreReadsTheRowExactlyOncePerOpen: with the factories activating,
// wireStorageDecorators no longer does, so each store is activated and its row read
// once per open -- not twice.
func TestActivateStoreReadsTheRowExactlyOncePerOpen(t *testing.T) {
	plane := &settingsPlane{value: "true"}
	raw := &configProbeStore{cfg: plane}
	if _, err := ActivateStore(context.Background(), raw, nil); err != nil {
		t.Fatal(err)
	}
	if raw.calls != 1 || plane.reads != 1 {
		t.Errorf("settings plane fetched %d time(s) and row read %d time(s), want 1 and 1", raw.calls, plane.reads)
	}
	if raw.sets != 1 || !raw.enabled {
		t.Errorf("SetVersionedHistoryEnabled called %d time(s), enabled=%v; want exactly one call turning it on", raw.sets, raw.enabled)
	}
}

func TestActivateStorePassesAFailedOpenThrough(t *testing.T) {
	openErr := errors.New("open failed")

	got, err := ActivateStore(context.Background(), nil, openErr)
	if got != nil || !errors.Is(err, openErr) {
		t.Errorf("ActivateStore(nil, err) = (%v, %v), want (nil, the open error)", got, err)
	}

	// A store handed back alongside an error is not touched or read either.
	raw := &configProbeStore{cfg: &settingsPlane{value: "true"}}
	got, err = ActivateStore(context.Background(), raw, openErr)
	if got != storage.DoltStorage(raw) || !errors.Is(err, openErr) {
		t.Errorf("ActivateStore(store, err) = (%v, %v), want the store and the open error unchanged", got, err)
	}
	if raw.calls != 0 || raw.sets != 0 {
		t.Errorf("a failed open still touched the store: %d settings reads, %d activations", raw.calls, raw.sets)
	}
}

// TestActivateStoreSurvivesAStoreThatCannotAnswer is the regression test for the CI
// failure itself, at the layer that now owns it. This is the RECOVER's test: the
// store here IS a configurer, so the capability guard lets it through to the read,
// and only the recover keeps the panic from escaping.
func TestActivateStoreSurvivesAStoreThatCannotAnswer(t *testing.T) {
	raw := &configProbeStore{panicMsg: "WorkspaceConfig on a store with no settings plane"}
	got, err := ActivateStore(context.Background(), raw, nil)
	if err != nil || got != storage.DoltStorage(raw) {
		t.Fatalf("ActivateStore = (%v, %v), want the store back and no error", got, err)
	}
	if raw.calls != 1 {
		t.Fatalf("WorkspaceConfig called %d times, want 1; this test is not exercising the read it claims to", raw.calls)
	}
	if raw.enabled {
		t.Error("a store whose settings plane panicked was switched on")
	}
}

func TestActivateStoreLeavesAStoreThatCannotRecordUntouched(t *testing.T) {
	raw := &nonConfigurerStore{}
	got, err := ActivateStore(context.Background(), raw, nil)
	if err != nil || got != storage.DoltStorage(raw) {
		t.Fatalf("ActivateStore = (%v, %v), want the store back and no error", got, err)
	}
	if raw.calls != 0 {
		t.Errorf("a store that cannot record was asked for its settings plane %d time(s), want 0", raw.calls)
	}
}

// TestEnvironmentIsProcessWideAndShortCircuitsTheRead is D3 for stores: viper says
// on, so every store the process opens is activated -- including one whose own row
// says nothing -- and the row is not even read.
func TestEnvironmentIsProcessWideAndShortCircuitsTheRead(t *testing.T) {
	viperEnv(t, "1")

	absent := &configProbeStore{cfg: &settingsPlane{value: ""}}
	if _, err := ActivateStore(context.Background(), absent, nil); err != nil {
		t.Fatal(err)
	}
	if !absent.enabled {
		t.Error("the environment said on but a store with no row was not activated")
	}
	if absent.calls != 0 {
		t.Errorf("the settings plane was read %d time(s) although the environment had already answered, want 0", absent.calls)
	}
}

// TestEnvironmentZeroNeverSwitchesOffAStoreWhoseRowIsOn is D3's other half, and
// the rule that OR is never ranked: =0 is not "off". A store whose own row says on
// keeps recording, and a store with no row stays quiet. The only way off is the row.
func TestEnvironmentZeroNeverSwitchesOffAStoreWhoseRowIsOn(t *testing.T) {
	viperEnv(t, "0")
	ctx := context.Background()

	on := &configProbeStore{cfg: &settingsPlane{value: "true"}}
	absent := &configProbeStore{cfg: &settingsPlane{value: ""}}
	for _, s := range []*configProbeStore{on, absent} {
		if _, err := ActivateStore(ctx, s, nil); err != nil {
			t.Fatal(err)
		}
	}
	if !on.enabled {
		t.Error("BD_VERSIONED_HISTORY_ENABLED=0 switched off a store whose own row says true")
	}
	if absent.enabled {
		t.Error("BD_VERSIONED_HISTORY_ENABLED=0 switched ON a store with no row")
	}
}

// --- D7: an error is not an absent row --------------------------------------

func TestAnAbsentRowIsSilentlyOffButAReadErrorIsDiagnosed(t *testing.T) {
	ctx := context.Background()

	t.Run("absent row: off, and nothing to say", func(t *testing.T) {
		raw := &configProbeStore{cfg: &settingsPlane{value: ""}}
		var got bool
		out := captureStderr(t, true, func() { got = StoreSetting(ctx, raw) })
		if got {
			t.Error("an absent row resolved to on")
		}
		if out != "" {
			t.Errorf("an absent row is the normal state of every workspace and must not be diagnosed, got: %q", out)
		}
	})

	t.Run("GetSetting error: off, and diagnosed at debug level", func(t *testing.T) {
		raw := &configProbeStore{cfg: &settingsPlane{err: errors.New("read failed: table config is locked")}}
		var got bool
		out := captureStderr(t, true, func() { got = StoreSetting(ctx, raw) })
		if got {
			t.Error("a failed row read resolved to on")
		}
		for _, want := range []string{ConfigKey, "table config is locked"} {
			if !strings.Contains(out, want) {
				t.Errorf("the diagnostic does not mention %q: %q", want, out)
			}
		}
	})

	t.Run("WorkspaceConfig error: off, and diagnosed at debug level", func(t *testing.T) {
		raw := &configProbeStore{err: errors.New("server unreachable")}
		var got bool
		out := captureStderr(t, true, func() { got = StoreSetting(ctx, raw) })
		if got {
			t.Error("an unreadable settings plane resolved to on")
		}
		if !strings.Contains(out, "server unreachable") {
			t.Errorf("the diagnostic does not carry the cause: %q", out)
		}
	})

	t.Run("the diagnostic is debug-level, never unconditional stderr", func(t *testing.T) {
		raw := &configProbeStore{cfg: &settingsPlane{err: errors.New("read failed")}}
		out := captureStderr(t, false, func() { _ = StoreSetting(ctx, raw) })
		if out != "" {
			t.Errorf("this runs on every invocation and must be silent unless debugging, got: %q", out)
		}
	})
}

// --- Apply: the raw-store ordering, and the set-but-unsupported warning -----

// TestApplyReachesOnlyTheRawStore pins the ORDERING that activation depends on.
//
// The capability is reached by type assertion, and none of the decorators
// (telemetry, externaldeps, hooks) forwards SetVersionedHistoryEnabled. So applying
// the config after any wrap would assert against a wrapper, fail silently, and
// leave versioned history OFF while the operator's config said it was on -- a wrong
// answer rather than an empty one. That is why the factories, which hand back the
// concrete store before anything wraps it, are where activation lives.
//
// The second half of this test is the part that actually guards the ordering: it
// proves the wrapped chain does NOT satisfy the capability, so if a decorator ever
// starts forwarding, the constraint this test exists for has changed.
func TestApplyReachesOnlyTheRawStore(t *testing.T) {
	raw := &configProbeStore{}
	Apply(raw, true)
	if !raw.enabled {
		t.Error("Apply(raw, true) did not enable versioned history on the raw store")
	}

	wrap := func(s storage.DoltStorage) storage.DoltStorage {
		return externaldeps.New(
			telemetry.WrapStorage(s),
			func(externaldeps.ProjectName) (string, bool) { return "", false },
			func(context.Context, string) (storage.DoltStorage, error) { return nil, errors.New("unused") },
		)
	}
	if _, ok := wrap(&configProbeStore{}).(storage.VersionedHistoryConfigurer); ok {
		// Fatal, not Skip. A skip here returns BEFORE the assertion below -- the
		// half that actually guards the ordering -- so the case would report
		// "not applicable" while checking nothing.
		t.Fatal("a decorator now forwards SetVersionedHistoryEnabled; the ordering constraint has changed and this test needs rewriting rather than silently passing")
	}

	behindWrap := &configProbeStore{}
	captureStderr(t, false, func() { Apply(wrap(behindWrap), true) })
	if behindWrap.enabled {
		t.Error("the capability was somehow reached through the decorators; this test's premise is stale")
	}
}

// TestApplyOffIsANoop pins that the disabled path touches nothing at all --
// flag-off must be byte-identical to a build without the feature, which is the
// contract #6135 ships under.
func TestApplyOffIsANoop(t *testing.T) {
	raw := &configProbeStore{}
	out := captureStderr(t, true, func() { Apply(raw, false) })
	if raw.sets != 0 || raw.enabled {
		t.Errorf("Apply(raw, false) called SetVersionedHistoryEnabled %d time(s), enabled=%v; flag-off must be a no-op", raw.sets, raw.enabled)
	}
	if out != "" {
		t.Errorf("Apply(raw, false) wrote %q", out)
	}
}

// TestApplyWarnsWhenTheBackendCannotRecord: asking for history and not getting it is
// exactly the case a user must be told about. The wording is #6661's, unchanged.
func TestApplyWarnsWhenTheBackendCannotRecord(t *testing.T) {
	out := captureStderr(t, false, func() { Apply(&nonConfigurerStore{}, true) })
	for _, want := range []string{
		"warning: versioned-history.enabled is set, but this storage backend (",
		"does not support version history; it stays off",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the warning is missing %q: %q", want, out)
		}
	}
}

// TestConfigKeyIsNotYamlOnly is D5's pin at the unit level. versioned-history.* must
// NOT become a yaml-only key: `bd config set versioned-history.enabled true` keeps
// writing the database row exactly as #6661 wired it, and a yaml-only key would move
// the switch off the store.
func TestConfigKeyIsNotYamlOnly(t *testing.T) {
	if ConfigKey != "versioned-history.enabled" {
		t.Errorf("ConfigKey = %q; the key is documented, and read back by `bd versions`, under this exact spelling", ConfigKey)
	}
	if config.IsYamlOnlyKey(ConfigKey) {
		t.Errorf("%s is a yaml-only key, so `bd config set` would write config.yaml and no store would ever read it", ConfigKey)
	}
}

// --- K1: the provider path ---------------------------------------------------

// TestActivateProviderReadsItsOwnRow is K1's central claim. A unit-of-work provider
// has no storage.DoltStorage to ask, so its flag comes from viper OR its own row --
// read through a unit of work's ConfigUseCase -- and NOT from viper alone.
func TestActivateProviderReadsItsOwnRow(t *testing.T) {
	p := newProviderProbe("true", nil)
	got, err := ActivateProvider(context.Background(), p, nil)
	if err != nil || got != uow.UnitOfWorkProvider(p) {
		t.Fatalf("ActivateProvider = (%v, %v), want the provider back and no error", got, err)
	}
	if !p.enabled {
		t.Fatal("a provider whose database row says true was not activated: the flag was resolved from viper alone")
	}
	if p.unit.cfg.reads != 1 || len(p.unit.cfg.keys) != 1 || p.unit.cfg.keys[0] != ConfigKey {
		t.Errorf("config rows read = %v (%d read(s)), want exactly one read of %q", p.unit.cfg.keys, p.unit.cfg.reads, ConfigKey)
	}
	// A settings read is a read: rolled back, never committed.
	if p.unit.closed != 1 || p.unit.committed != 0 {
		t.Errorf("unit of work closed %d time(s) and committed %d time(s), want closed once and never committed", p.unit.closed, p.unit.committed)
	}
}

func TestActivateProviderLeavesAnAbsentOrFalseRowOff(t *testing.T) {
	for _, value := range []string{"", "false", "0", "on", "nonsense"} {
		t.Run(fmt.Sprintf("%q", value), func(t *testing.T) {
			p := newProviderProbe(value, nil)
			captureStderr(t, true, func() { _, _ = ActivateProvider(context.Background(), p, nil) })
			if p.enabled {
				t.Errorf("a provider whose row is %q was activated", value)
			}
			if p.unit.closed != 1 {
				t.Errorf("unit of work closed %d time(s), want 1", p.unit.closed)
			}
		})
	}
	t.Run("an absent row is silent", func(t *testing.T) {
		p := newProviderProbe("", nil)
		if out := captureStderr(t, true, func() { _, _ = ActivateProvider(context.Background(), p, nil) }); out != "" {
			t.Errorf("an absent row must not be diagnosed, got %q", out)
		}
	})
}

// TestActivateProviderReadFailureIsOffAndDiagnosed is D7 for providers: an error is
// not an absent row. It resolves to off -- but not silently.
func TestActivateProviderReadFailureIsOffAndDiagnosed(t *testing.T) {
	t.Run("GetConfig errors", func(t *testing.T) {
		p := newProviderProbe("", errors.New("no such table: config"))
		out := captureStderr(t, true, func() { _, _ = ActivateProvider(context.Background(), p, nil) })
		if p.enabled {
			t.Error("a failed row read activated the provider")
		}
		for _, want := range []string{ConfigKey, "no such table: config"} {
			if !strings.Contains(out, want) {
				t.Errorf("the diagnostic does not mention %q: %q", want, out)
			}
		}
		if p.unit.closed != 1 {
			t.Errorf("unit of work closed %d time(s) after a failed read, want 1", p.unit.closed)
		}
	})
	t.Run("NewUOW errors", func(t *testing.T) {
		p := &providerProbe{newErr: errors.New("connection refused")}
		out := captureStderr(t, true, func() { _, _ = ActivateProvider(context.Background(), p, nil) })
		if p.enabled {
			t.Error("a provider that could not open a unit of work was activated")
		}
		if !strings.Contains(out, "connection refused") {
			t.Errorf("the diagnostic does not carry the cause: %q", out)
		}
	})
	t.Run("silent unless debugging", func(t *testing.T) {
		p := newProviderProbe("", errors.New("no such table: config"))
		if out := captureStderr(t, false, func() { _, _ = ActivateProvider(context.Background(), p, nil) }); out != "" {
			t.Errorf("this runs on every invocation and must be silent unless debugging, got: %q", out)
		}
	})
}

func TestActivateProviderSurvivesAProviderThatPanics(t *testing.T) {
	p := &providerProbe{panicMsg: "NewUOW on a half-built provider"}
	got, err := ActivateProvider(context.Background(), p, nil)
	if err != nil || got != uow.UnitOfWorkProvider(p) {
		t.Fatalf("ActivateProvider = (%v, %v), want the provider back and no error", got, err)
	}
	if p.newCalls != 1 {
		t.Fatalf("NewUOW called %d times, want 1; this test is not exercising the read it claims to", p.newCalls)
	}
	if p.enabled {
		t.Error("a provider whose read panicked was activated")
	}
}

func TestActivateProviderSkipsTheReadForANonConfigurer(t *testing.T) {
	p := &nonConfigurerProvider{}
	if _, ok := uow.UnitOfWorkProvider(p).(storage.VersionedHistoryConfigurer); ok {
		t.Fatal("nonConfigurerProvider satisfies VersionedHistoryConfigurer; this test cannot exercise the guard it claims to")
	}
	got, err := ActivateProvider(context.Background(), p, nil)
	if err != nil || got != uow.UnitOfWorkProvider(p) {
		t.Fatalf("ActivateProvider = (%v, %v), want the provider back and no error", got, err)
	}
	if p.newCalls != 0 {
		t.Errorf("a provider that cannot record was asked for a unit of work %d time(s), want 0", p.newCalls)
	}
}

func TestActivateProviderPassesAFailedOpenThrough(t *testing.T) {
	openErr := errors.New("open failed")
	got, err := ActivateProvider(context.Background(), nil, openErr)
	if got != nil || !errors.Is(err, openErr) {
		t.Errorf("ActivateProvider(nil, err) = (%v, %v), want (nil, the open error)", got, err)
	}
	p := newProviderProbe("true", nil)
	got, err = ActivateProvider(context.Background(), p, openErr)
	if got != uow.UnitOfWorkProvider(p) || !errors.Is(err, openErr) {
		t.Errorf("ActivateProvider(provider, err) = (%v, %v), want the provider and the open error unchanged", got, err)
	}
	if p.newCalls != 0 || p.sets != 0 {
		t.Errorf("a failed open still touched the provider: %d unit(s) of work opened, %d activation(s)", p.newCalls, p.sets)
	}
}

// TestProviderEnvironmentIsProcessWideAndOnlyTurnsRecordingOn is D3 for providers.
func TestProviderEnvironmentIsProcessWideAndOnlyTurnsRecordingOn(t *testing.T) {
	t.Run("=1 activates without reading the row", func(t *testing.T) {
		viperEnv(t, "1")
		p := newProviderProbe("", nil)
		if _, err := ActivateProvider(context.Background(), p, nil); err != nil {
			t.Fatal(err)
		}
		if !p.enabled {
			t.Error("the environment said on but the provider was not activated")
		}
		if p.newCalls != 0 {
			t.Errorf("a unit of work was opened %d time(s) although the environment had already answered, want 0", p.newCalls)
		}
	})
	t.Run("=0 never switches off a provider whose row is on", func(t *testing.T) {
		viperEnv(t, "0")
		p := newProviderProbe("true", nil)
		if _, err := ActivateProvider(context.Background(), p, nil); err != nil {
			t.Fatal(err)
		}
		if !p.enabled {
			t.Error("BD_VERSIONED_HISTORY_ENABLED=0 switched off a provider whose own row says true")
		}
	})
}
