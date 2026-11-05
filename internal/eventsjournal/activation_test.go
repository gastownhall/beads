package eventsjournal

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

func writeWorkspace(t *testing.T, yaml string) string {
	t.Helper()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if yaml != "" {
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(yaml), 0o644); err != nil {
			t.Fatalf("write config.yaml: %v", err)
		}
	}
	return beadsDir
}

// TestEnabledForPrecedence pins the ladder an operator relies on.
//
// The environment override has to WIN. It is the documented way to turn the
// journal on for a process without editing a workspace — and a process that
// writes into several workspaces (a routed create, a hydration pass) is exactly
// where an operator reaches for it. A yaml file that could silently veto it
// would make `BD_EVENTS_JOURNAL=1` a suggestion, and the operator would find
// out by discovering an empty journal later.
//
// The workspace file has to win over the default, and it has to be the TARGET
// workspace's file: the journal records that workspace's mutations, so that
// workspace decides.
func TestEnabledForPrecedence(t *testing.T) {
	cases := []struct {
		name string
		env  string // "" = unset
		yaml string // "" = no config.yaml
		want bool
	}{
		{name: "nothing set anywhere is off", want: false},
		{name: "workspace yaml enables", yaml: "events-journal: true\n", want: true},
		{name: "workspace yaml disables", yaml: "events-journal: false\n", want: false},
		{name: "env enables over a silent workspace", env: "1", want: true},
		{
			// The case the ordering exists for.
			name: "env enables over a workspace that says false",
			env:  "1", yaml: "events-journal: false\n", want: true,
		},
		{
			name: "env disables over a workspace that says true",
			env:  "0", yaml: "events-journal: true\n", want: false,
		},
		{
			// An unparseable env value must not silently mean "on"; the file
			// below it answers instead.
			name: "unparseable env falls through to the workspace",
			env:  "yes-please", yaml: "events-journal: true\n", want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env == "" {
				t.Setenv(EnvVar, "")
				if err := os.Unsetenv(EnvVar); err != nil {
					t.Fatalf("unset %s: %v", EnvVar, err)
				}
			} else {
				t.Setenv(EnvVar, tc.env)
			}
			if got := EnabledFor(writeWorkspace(t, tc.yaml)); got != tc.want {
				t.Errorf("EnabledFor = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestEnabledForIgnoresAnotherWorkspacesConfig is the cross-workspace half: a
// routed write consults the TARGET, never the directory bd was launched from.
func TestEnabledForIgnoresAnotherWorkspacesConfig(t *testing.T) {
	if err := os.Unsetenv(EnvVar); err != nil {
		t.Fatalf("unset %s: %v", EnvVar, err)
	}
	launcher := writeWorkspace(t, "events-journal: true\n")
	target := writeWorkspace(t, "")

	if !EnabledFor(launcher) {
		t.Error("the launching workspace's own setting must apply to itself")
	}
	if EnabledFor(target) {
		t.Error("a target workspace that never enabled the journal must not inherit the launcher's setting")
	}
}

// unjournalableStore is a store that cannot journal: it does not implement
// storage.EventsJournalConfigurer.
type unjournalableStore struct{ storage.DoltStorage }

func (s *unjournalableStore) Close() error { return nil }

// journalableStore records what activation it was given.
type journalableStore struct {
	unjournalableStore
	enabled bool
	set     bool
}

func (s *journalableStore) SetEventsJournalEnabled(enabled bool) {
	s.enabled = enabled
	s.set = true
}

// TestApplyFailsClosedOnAPlumbingThatCannotJournal is the fail-closed branch,
// which nothing else exercises directly.
//
// A workspace that asked for a journal and got a backend that cannot write one
// must FAIL the open. The alternative — carry on with the setting silently
// dropped — produces a workspace whose every command succeeds and whose journal
// stays empty, which is the one outcome that breaks a consumer's trust without
// leaving a trace. A workspace that did NOT ask keeps working on any backend,
// because nothing is being promised.
func TestApplyFailsClosedOnAPlumbingThatCannotJournal(t *testing.T) {
	err := Apply(nil, true, true)
	if err == nil {
		t.Fatal("Apply(nil, true, true) returned nil: an enabled workspace on a plumbing that cannot journal must fail the open")
	}
	if !strings.Contains(err.Error(), "does not support the events journal") {
		t.Errorf("error does not name the cause: %v", err)
	}

	if err := Apply(nil, false, true); err != nil {
		t.Errorf("Apply(nil, false, true) = %v; a disabled workspace must accept any plumbing", err)
	}

	recorder := &journalableStore{}
	if err := Apply(recorder, true, true); err != nil {
		t.Fatalf("Apply(configurer, true, true) = %v", err)
	}
	if !recorder.set || !recorder.enabled {
		t.Errorf("activation was not applied: set=%v enabled=%v", recorder.set, recorder.enabled)
	}
}

// TestActivateStoreClosesAStoreItCannotActivate pins the other half of failing
// closed: the caller must not be handed a usable store that would mutate
// unrecorded, so the store is closed on the way out.
func TestActivateStoreClosesAStoreItCannotActivate(t *testing.T) {
	t.Setenv(EnvVar, "1")

	closed := &closeRecordingStore{}
	got, err := ActivateStore(writeWorkspace(t, ""), true, closed, nil)
	if err == nil {
		t.Fatal("ActivateStore succeeded on a store that cannot journal")
	}
	if got != nil {
		t.Error("a store that could not be activated must not be returned")
	}
	if !closed.closed {
		t.Error("a store that could not be activated must be closed, not leaked")
	}

	// A failed open passes straight through, untouched — the activation must
	// not mask the real error or try to close a store that was never opened.
	sentinel := errOpenFailed
	if _, err := ActivateStore("", true, nil, sentinel); err != sentinel {
		t.Errorf("ActivateStore rewrote a failed open: %v", err)
	}
}

// shapeCheckingStore is a journalable store whose activation probe reported
// activationErr (storage.EventsJournalShapeChecker).
type shapeCheckingStore struct {
	journalableStore
	activationErr error
	closed        bool
}

func (s *shapeCheckingStore) EventsJournalActivationError() error { return s.activationErr }

func (s *shapeCheckingStore) Close() error {
	s.closed = true
	return nil
}

// TestActivateStoreRefusesAnUnsupportedShapeOnlyForBeadWritingOpens pins the
// posture split on a table the journal cannot run against.
//
// An open that is going to write beads must refuse, and close the store:
// otherwise the open succeeds and every write fails one at a time afterward,
// the gci failure mode. An open that will not write beads must NOT refuse —
// bd list, a preview, bd dolt commit and bd dolt pull all worked against such
// a table before adaptive I/O — and it must leave the journal ENABLED, so a
// journaled write it does make (the defer-wake sweep) fails loudly on its
// INSERT instead of committing with no journal row.
func TestActivateStoreRefusesAnUnsupportedShapeOnlyForBeadWritingOpens(t *testing.T) {
	t.Setenv(EnvVar, "1")
	beadsDir := writeWorkspace(t, "")
	shapeErr := &issueops.JournalShapeError{Table: "bd_events_journal", Missing: []string{"op"}}

	writer := &shapeCheckingStore{activationErr: shapeErr}
	got, err := ActivateStore(beadsDir, true, writer, nil)
	if !errors.Is(err, issueops.ErrJournalShapeUnsupported) {
		t.Fatalf("bead-writing open: ActivateStore error = %v; want the shape refusal", err)
	}
	if got != nil || !writer.closed {
		t.Errorf("bead-writing open: a refused store must be closed, not returned (returned=%v closed=%v)", got != nil, writer.closed)
	}

	reader := &shapeCheckingStore{activationErr: shapeErr}
	got, err = ActivateStore(beadsDir, false, reader, nil)
	if err != nil {
		t.Fatalf("non-writing open: ActivateStore error = %v; the shape must not refuse an open that writes no beads", err)
	}
	if got != reader || reader.closed {
		t.Errorf("non-writing open: the store must be returned open (returned=%v closed=%v)", got == reader, reader.closed)
	}
	if !reader.set || !reader.enabled {
		t.Errorf("non-writing open: journal left disabled (set=%v enabled=%v); a write it makes would commit unrecorded", reader.set, reader.enabled)
	}
}

type closeRecordingStore struct {
	unjournalableStore
	closed bool
}

func (s *closeRecordingStore) Close() error {
	s.closed = true
	return nil
}

var errOpenFailed = &openError{}

type openError struct{}

func (*openError) Error() string { return "open failed" }

// compile-time proof the doubles above stand in for a real store.
var (
	_ storage.DoltStorage               = (*unjournalableStore)(nil)
	_ storage.EventsJournalConfigurer   = (*journalableStore)(nil)
	_ storage.EventsJournalShapeChecker = (*shapeCheckingStore)(nil)
)
