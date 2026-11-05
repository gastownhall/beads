package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
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

// scanStore is a raw store whose issue reader answers the pre-enable scan's
// listings with canned rows. It cannot say which rows participate in history, so it
// is the backend the scan has to over-approximate on. Every other method promotes
// from the embedded nil interface and panics if called.
//
// Its listing answers the way the real one does in the three respects the scan
// relies on: a Labels filter selects the rows that carry every label named, a row's
// labels are left off when the request skips them, and each row names its parent
// (parents) the way a page's Parent does. labelReads counts the listings filtered by
// a label, and labelErr fails them.
type scanStore struct {
	storage.DoltStorage
	rows       []*types.Issue
	parents    map[string]string
	labelReads int
	labelErr   error
}

func (s *scanStore) IssueReader() (issueops.Reader, error) { return scanReader{s: s}, nil }

type scanReader struct {
	issueops.Reader
	s *scanStore
}

func (r scanReader) List(_ context.Context, req issueops.ListRequest) (issueops.IssuePage, error) {
	if len(req.Labels) > 0 {
		r.s.labelReads++
		if r.s.labelErr != nil {
			return issueops.IssuePage{}, r.s.labelErr
		}
	}
	items := make([]*issueops.IssueWithCounts, 0, len(r.s.rows))
	for _, issue := range r.s.rows {
		if !hasEveryLabel(issue, req.Labels) {
			continue
		}
		row := issue
		if req.SkipLabels && len(issue.Labels) > 0 {
			bare := *issue
			bare.Labels = nil
			row = &bare
		}
		item := &issueops.IssueWithCounts{Issue: row}
		if parent, ok := r.s.parents[issue.ID]; ok {
			item.Parent = &parent
		}
		items = append(items, item)
	}
	return issueops.IssuePage{Items: items}, nil
}

func hasEveryLabel(issue *types.Issue, labels []string) bool {
	for _, want := range labels {
		found := false
		for _, label := range issue.Labels {
			if label == want {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// participationScanStore is a scanStore that also answers
// storage.HistoryParticipation, and records what it was asked.
type participationScanStore struct {
	scanStore
	participating map[string]bool
	err           error
	asked         [][]string
}

func (s *participationScanStore) ParticipatingIssueIDs(_ context.Context, ids []string) (map[string]struct{}, error) {
	s.asked = append(s.asked, append([]string(nil), ids...))
	if s.err != nil {
		return nil, s.err
	}
	out := map[string]struct{}{}
	for _, id := range ids {
		if s.participating[id] {
			out[id] = struct{}{}
		}
	}
	return out, nil
}

// TestVersionedHistorySwitchScanNamesOnlyRowsThatParticipate pins the precision of
// the pre-enable scan on the direct route, in process, against the decision it makes.
//
// Design §16.2b's write fence skips a legacy row (participation_generation NULL)
// before the mint looks at it, so a value a legacy row holds can never make a write
// to that row fail, and the scan must not refuse the switch over it unless it is a
// template (TestVersionedHistorySwitchScanChecksTheLegacyIssuesAPourCopies pins
// that half; no row here is one): on a store that has never recorded, every row is
// legacy. The scan reads rows, which do not carry participation, finds the ones a
// version would refuse, asks the store which of those participate
// (storage.HistoryParticipation, peeled out of the decorator chain), and keeps only
// them. It asks about the offenders alone, and a clean store is asked nothing. A
// store that cannot answer keeps every offender, which can only refuse more, and the
// refusal says it could not tell. A participation read that fails refuses, because a
// check that could not run has not passed.
func TestVersionedHistorySwitchScanNamesOnlyRowsThatParticipate(t *testing.T) {
	big := json.RawMessage(`{"ts":1727000000000000000}`)
	legacy := &types.Issue{ID: "vs-legacy", Metadata: big}
	participating := &types.Issue{ID: "vs-part", Metadata: big}
	gate := &types.Issue{ID: "vs-gate", IssueType: types.TypeGate, Timeout: 2600 * time.Hour}
	clean := &types.Issue{ID: "vs-clean", Metadata: json.RawMessage(`{"ok":1}`)}
	wisp := &types.Issue{ID: "vs-wisp", Ephemeral: true, Metadata: big}
	rows := []*types.Issue{legacy, participating, gate, clean, wisp}
	inStore := map[string]bool{participating.ID: true, gate.ID: true, clean.ID: true}

	withStore := func(t *testing.T, st storage.DoltStorage) {
		t.Helper()
		oldStore, oldProxied := store, proxiedServerMode
		store, proxiedServerMode = st, false
		t.Cleanup(func() { store, proxiedServerMode = oldStore, oldProxied })
	}
	enable := func() error {
		_, err := checkVersionedHistoryCanBeEnabled(context.Background(), versionedHistorySettingKey, "true")
		return err
	}

	t.Run("names the participating offenders and not the legacy one", func(t *testing.T) {
		st := &participationScanStore{scanStore: scanStore{rows: rows}, participating: inStore}
		withStore(t, st)
		err := enable()
		if err == nil {
			t.Fatal("the switch was allowed over a participating row a version could not record")
		}
		out := err.Error()
		for _, id := range []string{participating.ID, gate.ID} {
			if !strings.Contains(out, id) {
				t.Errorf("the refusal does not name %s, which participates and holds a value a version could not record:\n%s", id, out)
			}
		}
		for _, id := range []string{legacy.ID, clean.ID, wisp.ID} {
			if strings.Contains(out, id) {
				t.Errorf("the refusal names %s, whose writes the mint never refuses:\n%s", id, out)
			}
		}
		if !strings.Contains(out, "2 issues") || !strings.Contains(out, "participates in history") || strings.Contains(out, "cannot tell") {
			t.Errorf("the refusal does not count the participating offenders alone or say they participate:\n%s", out)
		}
		if want := [][]string{{legacy.ID, participating.ID, gate.ID}}; !reflect.DeepEqual(st.asked, want) {
			t.Errorf("participation was asked about %v, want the offenders alone, once: %v", st.asked, want)
		}
	})

	t.Run("allows the switch when every offender is legacy", func(t *testing.T) {
		withStore(t, &participationScanStore{scanStore: scanStore{rows: rows}, participating: map[string]bool{clean.ID: true}})
		if err := enable(); err != nil {
			t.Fatalf("the switch was refused over legacy rows, which the write fence never versions: %v", err)
		}
	})

	t.Run("asks nothing of a clean store", func(t *testing.T) {
		st := &participationScanStore{scanStore: scanStore{rows: []*types.Issue{clean, wisp}}, participating: inStore}
		withStore(t, st)
		if err := enable(); err != nil {
			t.Fatalf("a store with no offender was refused: %v", err)
		}
		if len(st.asked) != 0 {
			t.Errorf("participation was read for a store with no offender: %v", st.asked)
		}
	})

	t.Run("keeps every offender, and says so, on a store that cannot tell", func(t *testing.T) {
		withStore(t, &scanStore{rows: rows})
		err := enable()
		if err == nil {
			t.Fatal("the switch was allowed although the store could not say which offenders participate")
		}
		out := err.Error()
		for _, id := range []string{legacy.ID, participating.ID, gate.ID} {
			if !strings.Contains(out, id) {
				t.Errorf("the refusal does not name %s:\n%s", id, out)
			}
		}
		if !strings.Contains(out, "3 issues") || !strings.Contains(out, "cannot tell") {
			t.Errorf("the refusal does not say it could not tell which issues participate:\n%s", out)
		}
	})

	t.Run("refuses when the participation read fails", func(t *testing.T) {
		withStore(t, &participationScanStore{scanStore: scanStore{rows: rows}, err: errors.New("unknown column 'participation_generation'")})
		err := enable()
		if err == nil || !strings.Contains(err.Error(), "cannot check the store's issues before turning versioned history on") {
			t.Fatalf("a failed participation read answered %v, want the refusal that says the check could not run", err)
		}
	})
}

// participationScanProvider serves a participationScanStore to the proxied route:
// the scan's listings through the provider's issue reader, and the participation
// read through a unit of work's IssueUseCase, which is the only way that route
// reaches it. Every other method of the unit of work and the use case promotes from
// a nil interface and panics if called, so the check cannot write through either.
type participationScanProvider struct{ st *participationScanStore }

var (
	_ uow.UnitOfWorkProvider = participationScanProvider{}
	_ uow.IssueReaderSource  = participationScanProvider{}
)

func (p participationScanProvider) IssueReader() (issueops.Reader, error) {
	return p.st.IssueReader()
}

func (p participationScanProvider) NewUOW(context.Context) (uow.UnitOfWork, error) {
	return participationScanUOW{st: p.st}, nil
}

func (p participationScanProvider) Close(context.Context) error { return nil }

type participationScanUOW struct {
	uow.UnitOfWork
	st *participationScanStore
}

func (u participationScanUOW) Close(context.Context) {}

func (u participationScanUOW) IssueUseCase() domain.IssueUseCase {
	return participationScanIssues{st: u.st}
}

type participationScanIssues struct {
	domain.IssueUseCase
	st *participationScanStore
}

func (uc participationScanIssues) ParticipatingIssueIDs(ctx context.Context, ids []string) (map[string]struct{}, error) {
	return uc.st.ParticipatingIssueIDs(ctx, ids)
}

// scanRoute installs st as the store the scan reads on one route: the direct route
// reads it as the command's store, the proxied route only through
// participationScanProvider, with no store at all.
type scanRoute struct {
	name    string
	install func(t *testing.T, st *participationScanStore)
}

func scanRoutes() []scanRoute {
	install := func(t *testing.T, st storage.DoltStorage, provider uow.UnitOfWorkProvider, proxied bool) {
		t.Helper()
		oldStore, oldProvider, oldProxied := store, uowProvider, proxiedServerMode
		store, uowProvider, proxiedServerMode = st, provider, proxied
		t.Cleanup(func() { store, uowProvider, proxiedServerMode = oldStore, oldProvider, oldProxied })
	}
	return []scanRoute{
		{"direct", func(t *testing.T, st *participationScanStore) { install(t, st, nil, false) }},
		{"proxied", func(t *testing.T, st *participationScanStore) {
			install(t, nil, participationScanProvider{st: st}, true)
		}},
	}
}

// TestVersionedHistorySwitchProxiedRouteReadsParticipationThroughTheProvider is the
// proxied twin of the participation half of
// TestVersionedHistorySwitchScanNamesOnlyRowsThatParticipate. On that route the scan
// asks a unit of work's IssueUseCase which offenders participate, and the answer has
// the same two failure modes the direct route's has: a read that fails, and no
// provider to read through. Each must refuse with the refusal that says the check
// could not run. A read that came back as "cannot tell" instead would refuse with a
// different list, naming legacy rows the mint never refuses a write to, and a read
// that came back empty would clear the switch over every participating offender.
func TestVersionedHistorySwitchProxiedRouteReadsParticipationThroughTheProvider(t *testing.T) {
	big := json.RawMessage(`{"ts":1727000000000000000}`)
	legacy := &types.Issue{ID: "vp-legacy", Metadata: big}
	participating := &types.Issue{ID: "vp-part", Metadata: big}
	gate := &types.Issue{ID: "vp-gate", IssueType: types.TypeGate, Timeout: 2600 * time.Hour}
	rows := []*types.Issue{legacy, participating, gate}
	inStore := map[string]bool{participating.ID: true, gate.ID: true}
	proxied := scanRoutes()[1]
	enable := func() error {
		_, err := checkVersionedHistoryCanBeEnabled(context.Background(), versionedHistorySettingKey, "true")
		return err
	}

	t.Run("names the participating offenders and not the legacy one", func(t *testing.T) {
		st := &participationScanStore{scanStore: scanStore{rows: rows}, participating: inStore}
		proxied.install(t, st)
		err := enable()
		if err == nil {
			t.Fatal("the switch was allowed over a participating row a version could not record")
		}
		out := err.Error()
		if !strings.Contains(out, participating.ID) || !strings.Contains(out, gate.ID) || strings.Contains(out, legacy.ID) {
			t.Errorf("the refusal does not name exactly the participating offenders %s and %s:\n%s", participating.ID, gate.ID, out)
		}
		if !strings.Contains(out, "2 issues") || strings.Contains(out, "cannot tell") {
			t.Errorf("the refusal does not count the participating offenders alone:\n%s", out)
		}
		if want := [][]string{{legacy.ID, participating.ID, gate.ID}}; !reflect.DeepEqual(st.asked, want) {
			t.Errorf("the provider was asked about %v, want the offenders alone, once: %v", st.asked, want)
		}
	})

	t.Run("refuses when the participation read fails", func(t *testing.T) {
		st := &participationScanStore{scanStore: scanStore{rows: rows}, err: errors.New("unknown column 'participation_generation'")}
		proxied.install(t, st)
		err := enable()
		if err == nil || !strings.Contains(err.Error(), "cannot check the store's issues before turning versioned history on") {
			t.Fatalf("a failed participation read on the proxied route answered %v, want the refusal that says the check could not run", err)
		}
		if len(st.asked) != 1 {
			t.Errorf("the provider was asked %d time(s), want once: the refusal must come from the read that failed", len(st.asked))
		}
	})

	t.Run("refuses without a provider", func(t *testing.T) {
		proxied.install(t, &participationScanStore{scanStore: scanStore{rows: rows}, participating: inStore})
		uowProvider = nil
		got, known, err := participatingIssueIDs(context.Background(), []string{legacy.ID})
		if err == nil || known || got != nil {
			t.Fatalf("participatingIssueIDs with no provider = %v, %v, %v; want an error, so the switch refuses rather than passing or keeping every offender", got, known, err)
		}
	})
}

// TestVersionedHistorySwitchScanChecksTheLegacyIssuesAPourCopies pins the other reason
// a legacy offender matters. A write to a legacy row is never refused, but `bd mol
// pour` and `bd mol bond` copy a proto's metadata and gate timeouts into every issue
// they create, and a new issue is versioned, so a proto holding such a value makes
// every persistent pour or bond of it fail. The scan finds a proto's issues the way
// pour does, from each root down:
//   - a template (bd cook --persist writes every issue of a proto it cooks as one),
//     which REFUSES the switch, marked (template): bd update, the in-place fix, refuses
//     a template, so the switch names the fixes that work (bd cook --persist --force,
//     bd delete),
//   - a template, or an issue labeled template (the labels are read with a listing of
//     their own, since the scan's listing skips them), is a proto's root, and every
//     issue under one, through the parent a row names, a parent-child dependency, or a
//     dotted id, to any depth, is part of it. A legacy one that is not a template does
//     NOT refuse the switch: it can be fixed in place with history on, and the label
//     cannot tell a proto from a molecule poured from a stored proto, which carries
//     the proto's label. The scan warns instead, naming the outermost root. A template
//     without the label is a root too: pour refuses it, but bond treats any template
//     as a proto and, on the direct route, copies everything under it.
//
// A dotted id whose row has a parent-child dependency elsewhere is not under its id's
// parent, as in pour (GH#2476). A legacy offender that is part of no proto is passed
// over without a word, the read of the labels is made only when a legacy offender that
// is not a template is left, and a read of them that fails turns the warning into one
// that says the scan could not tell, refusing nothing. It runs on both routes: the
// walk is the same code, but each route lists the rows through its own reader.
func TestVersionedHistorySwitchScanChecksTheLegacyIssuesAPourCopies(t *testing.T) {
	big := json.RawMessage(`{"ts":1727000000000000000}`)
	participating := &types.Issue{ID: "vq-part", Metadata: big}
	legacy := &types.Issue{ID: "vq-legacy", Metadata: big}
	// Dotted under a row that is part of no proto.
	legacyUnder := &types.Issue{ID: "vq-legacy.1", Metadata: big}
	cooked := &types.Issue{ID: "vq-cooked", IssueType: types.TypeMolecule, IsTemplate: true, Labels: []string{MoleculeLabel}}
	cookedGate := &types.Issue{ID: "vq-cooked.gate-review", IssueType: types.TypeGate, IsTemplate: true, Timeout: 2600 * time.Hour}
	hand := &types.Issue{ID: "vq-hand", IssueType: types.TypeEpic, Labels: []string{MoleculeLabel}, Metadata: big}
	// Under hand through the parent its row names, and labeled itself, as a step
	// created with --parent is: its root is still hand, the outermost.
	byParent := &types.Issue{ID: "vq-step", Labels: []string{MoleculeLabel}, Metadata: big}
	// Under byParent through a parent-child dependency alone.
	byEdge := &types.Issue{ID: "vq-deep", Metadata: big,
		Dependencies: []*types.Dependency{{IssueID: "vq-deep", DependsOnID: byParent.ID, Type: types.DepParentChild}}}
	// Under hand through its dotted id alone.
	byID := &types.Issue{ID: "vq-hand.9", Metadata: big}
	// Dotted under hand, but reparented onto a row that is part of no proto.
	reparented := &types.Issue{ID: "vq-hand.7", Metadata: big,
		Dependencies: []*types.Dependency{{IssueID: "vq-hand.7", DependsOnID: legacy.ID, Type: types.DepParentChild}}}
	// A template without the label is still a proto's root: pour refuses it, but bond
	// treats any template as a proto (bondsAsProto) and, on the direct route, copies
	// the whole subgraph under it, vq-attached included.
	bare := &types.Issue{ID: "vq-bare", IssueType: types.TypeMolecule, IsTemplate: true}
	underBare := &types.Issue{ID: "vq-attached", Metadata: big}
	// A molecule poured from a stored proto before history was on: its root carries
	// the proto's label, and its gate the proto's timeout.
	poured := &types.Issue{ID: "vq-poured", IssueType: types.TypeMolecule, Labels: []string{MoleculeLabel}}
	pouredGate := &types.Issue{ID: "vq-pgate", IssueType: types.TypeGate, Timeout: 2600 * time.Hour}
	// A blocking edge onto a proto does not make an issue part of it.
	blockedBy := &types.Issue{ID: "vq-blocked", Metadata: big,
		Dependencies: []*types.Dependency{{IssueID: "vq-blocked", DependsOnID: hand.ID, Type: types.DepBlocks}}}
	rows := []*types.Issue{participating, legacy, legacyUnder, cooked, cookedGate, hand, byParent, byEdge, byID, reparented, bare, underBare, poured, pouredGate, blockedBy}
	parents := map[string]string{cookedGate.ID: cooked.ID, byParent.ID: hand.ID, underBare.ID: bare.ID, pouredGate.ID: poured.ID, reparented.ID: legacy.ID}
	enable := func() (string, error) {
		return checkVersionedHistoryCanBeEnabled(context.Background(), versionedHistorySettingKey, "true")
	}
	// line returns the line of out that lists id, or "".
	line := func(out, id string) string {
		for _, l := range strings.Split(out, "\n") {
			if strings.HasPrefix(l, "  "+id+"  ") {
				return l
			}
		}
		return ""
	}
	warned := map[string]string{
		hand.ID:       hand.ID,
		byParent.ID:   hand.ID,
		byEdge.ID:     hand.ID,
		byID.ID:       hand.ID,
		pouredGate.ID: poured.ID,
		underBare.ID:  bare.ID,
	}
	unnamed := []string{legacy.ID, legacyUnder.ID, reparented.ID, blockedBy.ID}

	for _, route := range scanRoutes() {
		t.Run(route.name, func(t *testing.T) {
			t.Run("refuses over templates and warns of the other legacy issues of a proto", func(t *testing.T) {
				st := &participationScanStore{scanStore: scanStore{rows: rows, parents: parents}, participating: map[string]bool{participating.ID: true}}
				route.install(t, st)
				warning, err := enable()
				if err == nil {
					t.Fatal("the switch was allowed over a template whose every persistent pour a version would refuse")
				}
				out := err.Error()
				if !strings.Contains(out, "2 issues") || line(out, participating.ID) == "" || !strings.Contains(line(out, cookedGate.ID), "(template) ") {
					t.Errorf("the refusal does not name exactly %s and the template %s, marked:\n%s", participating.ID, cookedGate.ID, out)
				}
				if l := line(out, participating.ID); strings.Contains(l, "(template)") || strings.Contains(l, "(proto") {
					t.Errorf("the refusal marks %s, which is listed because it participates:\n%s", participating.ID, out)
				}
				for _, want := range []string{"bd mol pour and bd mol bond copy its value", "bd cook <formula> --persist --force", "molecules.jsonl catalog", "named in the warning above"} {
					if !strings.Contains(out, want) {
						t.Errorf("the refusal does not contain %q:\n%s", want, out)
					}
				}
				if strings.Contains(out, "SET timeout_ns") || strings.Contains(out, "(proto") {
					t.Errorf("the refusal offers a gate remedy or names a proto issue although every gate it lists is a template:\n%s", out)
				}
				for id, root := range warned {
					if line(out, id) != "" {
						t.Errorf("the refusal names %s, a legacy issue that is not a template:\n%s", id, out)
					}
					if l := line(warning, id); !strings.HasSuffix(l, "(proto "+root+")") {
						t.Errorf("the warning lists %s as %q, want it under the proto rooted at %s:\n%s", id, l, root, warning)
					}
				}
				for _, id := range append(append([]string{}, unnamed...), participating.ID, cookedGate.ID) {
					if line(warning, id) != "" {
						t.Errorf("the warning names %s, which is part of no proto or is in the refusal:\n%s", id, warning)
					}
				}
				for _, want := range []string{fmt.Sprintf("%d legacy issues", len(warned)), "bd label remove <root> template", "bd update <id> --unset-metadata <key>", "bd delete <id> --force"} {
					if !strings.Contains(warning, want) {
						t.Errorf("the warning does not contain %q:\n%s", want, warning)
					}
				}
				if st.labelReads != 1 {
					t.Errorf("the labels were read %d time(s), want once", st.labelReads)
				}
			})

			t.Run("lets a legacy poured molecule through with a warning", func(t *testing.T) {
				st := &participationScanStore{scanStore: scanStore{rows: []*types.Issue{legacy, poured, pouredGate}, parents: parents}, participating: map[string]bool{}}
				route.install(t, st)
				warning, err := enable()
				if err != nil {
					t.Fatalf("the switch was refused over a legacy poured molecule, which bd update or bd label remove fixes with history on: %v", err)
				}
				if !strings.HasSuffix(line(warning, pouredGate.ID), "(proto "+poured.ID+")") || line(warning, legacy.ID) != "" {
					t.Errorf("the warning does not name exactly the poured gate %s under %s:\n%s", pouredGate.ID, poured.ID, warning)
				}
			})

			t.Run("reads no labels when every offender participates or is a template", func(t *testing.T) {
				st := &participationScanStore{scanStore: scanStore{rows: []*types.Issue{participating, cooked, cookedGate}, parents: parents}, participating: map[string]bool{participating.ID: true}}
				route.install(t, st)
				warning, err := enable()
				if err == nil || !strings.Contains(err.Error(), participating.ID) || !strings.Contains(err.Error(), cookedGate.ID) {
					t.Fatalf("the switch answered %v, want a refusal naming %s and %s", err, participating.ID, cookedGate.ID)
				}
				if warning != "" || st.labelReads != 0 {
					t.Errorf("with no legacy offender to place, the labels were read %d time(s) and the warning is %q; want neither", st.labelReads, warning)
				}
				if strings.Contains(err.Error(), "warning above") {
					t.Errorf("the refusal points to a warning that was not printed:\n%s", err)
				}
			})

			t.Run("says nothing when no legacy offender is part of a proto", func(t *testing.T) {
				cleanHand := &types.Issue{ID: hand.ID, IssueType: types.TypeEpic, Labels: []string{MoleculeLabel}}
				st := &participationScanStore{scanStore: scanStore{rows: []*types.Issue{legacy, legacyUnder, reparented, blockedBy, cleanHand}, parents: parents}, participating: map[string]bool{}}
				route.install(t, st)
				if warning, err := enable(); err != nil || warning != "" {
					t.Fatalf("the switch answered %q, %v over legacy rows that are part of no proto; want no warning and no refusal", warning, err)
				}
			})

			t.Run("warns that it could not tell when the label read fails", func(t *testing.T) {
				st := &participationScanStore{scanStore: scanStore{rows: []*types.Issue{legacy, poured, pouredGate}, parents: parents, labelErr: errors.New("connection reset by peer")}, participating: map[string]bool{}}
				route.install(t, st)
				warning, err := enable()
				if err != nil {
					t.Fatalf("a failed label read refused the switch (%v), although nothing it could find refuses", err)
				}
				if !strings.Contains(warning, "could not check whether a legacy issue") || !strings.Contains(warning, "connection reset by peer") {
					t.Errorf("a failed label read warned %q, want the warning that says the scan could not tell, and why", warning)
				}
			})
		})
	}
}
