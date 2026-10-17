package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/httpclient"
	"github.com/steveyegge/beads/internal/httpclient/encode"
	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

var gateTestStdoutMu sync.Mutex

type gateCloseCall struct {
	id      string
	reason  string
	actor   string
	session string
}

type fakeGateCheckStore struct {
	storage.DoltStorage
	issues       []*types.Issue
	queries      []issueops.QueryRequest
	searchCalled bool
	closeCalls   []gateCloseCall
	updates      []issueops.UpdateRequest
	updateErr    error
}

// SearchIssues records that the raw filter was used: gate check lists through
// the Querier role (ListGates), which a remote backend serves, and must not.
func (f *fakeGateCheckStore) SearchIssues(_ context.Context, _ string, _ types.IssueFilter) ([]*types.Issue, error) {
	f.searchCalled = true
	return f.issues, nil
}

func (f *fakeGateCheckStore) Querier() (issueops.Querier, error) { return f, nil }

func (f *fakeGateCheckStore) Query(_ context.Context, req issueops.QueryRequest) (issueops.IssuePage, error) {
	f.queries = append(f.queries, req)
	items := make([]*types.IssueWithCounts, 0, len(f.issues))
	for _, issue := range f.issues {
		items = append(items, &types.IssueWithCounts{Issue: issue})
	}
	return issueops.IssuePage{Items: items}, nil
}

// IssueLifecycle hands back a Lifecycle that records Update: gate check's
// sighting and await_id writes go through the role (issues.update over HTTP),
// never through the legacy map-based UpdateIssue the HTTP store does not serve.
func (f *fakeGateCheckStore) IssueLifecycle() (issueops.Lifecycle, error) {
	return &fakeGateLifecycle{store: f}, nil
}

type fakeGateLifecycle struct {
	issueops.Lifecycle
	store *fakeGateCheckStore
}

func (l *fakeGateLifecycle) Update(_ context.Context, req issueops.UpdateRequest) (issueops.UpdateResult, error) {
	l.store.updates = append(l.store.updates, req)
	if l.store.updateErr != nil {
		return issueops.UpdateResult{}, l.store.updateErr
	}
	return issueops.UpdateResult{}, nil
}

func (f *fakeGateCheckStore) CloseIssue(_ context.Context, id, reason, actor, session string) error {
	f.closeCalls = append(f.closeCalls, gateCloseCall{
		id:      id,
		reason:  reason,
		actor:   actor,
		session: session,
	})
	return nil
}

func captureGateStdout(t *testing.T, fn func()) string {
	t.Helper()

	gateTestStdoutMu.Lock()
	defer gateTestStdoutMu.Unlock()

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	fn()

	_ = w.Close()
	os.Stdout = old
	<-done
	_ = r.Close()

	return buf.String()
}

func resetGateCheckFlags(t *testing.T) {
	t.Helper()

	if err := gateCheckCmd.Flags().Set("type", ""); err != nil {
		t.Fatalf("reset type flag: %v", err)
	}
	if err := gateCheckCmd.Flags().Set("dry-run", "false"); err != nil {
		t.Fatalf("reset dry-run flag: %v", err)
	}
	if err := gateCheckCmd.Flags().Set("escalate", "false"); err != nil {
		t.Fatalf("reset escalate flag: %v", err)
	}
	if err := gateCheckCmd.Flags().Set("limit", "100"); err != nil {
		t.Fatalf("reset limit flag: %v", err)
	}

	gateCheckCmd.Flags().Lookup("type").Changed = false
	gateCheckCmd.Flags().Lookup("dry-run").Changed = false
	gateCheckCmd.Flags().Lookup("escalate").Changed = false
	gateCheckCmd.Flags().Lookup("limit").Changed = false
}

func TestShouldCheckGate(t *testing.T) {
	tests := []struct {
		name       string
		awaitType  string
		typeFilter string
		want       bool
	}{
		// Empty filter matches all
		{"empty filter matches gh:run", "gh:run", "", true},
		{"empty filter matches gh:pr", "gh:pr", "", true},
		{"empty filter matches timer", "timer", "", true},
		{"empty filter matches human", "human", "", true},
		{"empty filter matches bead", "bead", "", true},

		// "all" filter matches all
		{"all filter matches gh:run", "gh:run", "all", true},
		{"all filter matches gh:pr", "gh:pr", "all", true},
		{"all filter matches timer", "timer", "all", true},
		{"all filter matches bead", "bead", "all", true},

		// "gh" filter matches all GitHub types
		{"gh filter matches gh:run", "gh:run", "gh", true},
		{"gh filter matches gh:pr", "gh:pr", "gh", true},
		{"gh filter does not match timer", "timer", "gh", false},
		{"gh filter does not match human", "human", "gh", false},
		{"gh filter does not match bead", "bead", "gh", false},

		// Exact type filters
		{"gh:run filter matches gh:run", "gh:run", "gh:run", true},
		{"gh:run filter does not match gh:pr", "gh:pr", "gh:run", false},
		{"gh:pr filter matches gh:pr", "gh:pr", "gh:pr", true},
		{"gh:pr filter does not match gh:run", "gh:run", "gh:pr", false},
		{"timer filter matches timer", "timer", "timer", true},
		{"timer filter does not match gh:run", "gh:run", "timer", false},
		{"bead filter matches bead", "bead", "bead", true},
		{"bead filter does not match timer", "timer", "bead", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate := &types.Issue{
				AwaitType: tt.awaitType,
			}
			got := shouldCheckGate(gate, tt.typeFilter)
			if got != tt.want {
				t.Errorf("shouldCheckGate(%q, %q) = %v, want %v",
					tt.awaitType, tt.typeFilter, got, tt.want)
			}
		})
	}
}

// TestFilterCheckableGatesSkipsTemplates: no type filter selects a persisted
// proto's gates, even from a listing that returned them beside the open ones
// (gate check's own query leaves them out). A template gate is the blueprint
// every pour copies, so a sweep neither evaluates nor writes it.
func TestFilterCheckableGatesSkipsTemplates(t *testing.T) {
	var gates []*types.Issue
	for _, awaitType := range []string{"gh:run", "gh:pr", "timer", "human", "bead"} {
		gates = append(gates,
			&types.Issue{ID: "live-" + awaitType, IssueType: "gate", AwaitType: awaitType},
			&types.Issue{ID: "tpl-" + awaitType, IssueType: "gate", AwaitType: awaitType, IsTemplate: true},
		)
	}
	every := []string{"live-gh:run", "live-gh:pr", "live-timer", "live-human", "live-bead"}
	for _, tt := range []struct {
		filter string
		want   []string
	}{
		{"", every},
		{"all", every},
		{"gh", []string{"live-gh:run", "live-gh:pr"}},
		{"gh:run", []string{"live-gh:run"}},
		{"gh:pr", []string{"live-gh:pr"}},
		{"timer", []string{"live-timer"}},
		{"bead", []string{"live-bead"}},
	} {
		var got []string
		for _, gate := range filterCheckableGates(gates, tt.filter) {
			got = append(got, gate.ID)
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("filterCheckableGates(%q) = %v, want %v", tt.filter, got, tt.want)
		}
	}
}

// fakeBeadGateGetter fakes the one lookup checkBeadGate performs.
type fakeBeadGateGetter struct {
	issues map[string]*types.Issue
	err    error
	gotID  string
}

func (f *fakeBeadGateGetter) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	f.gotID = id
	if f.err != nil {
		return nil, f.err
	}
	return f.issues[id], nil
}

// checkBeadGate runs only the target lookup of a bead gate check. It skips
// the sighting rule evaluateBeadGate adds, so a gone bead reads as resolved.
func checkBeadGate(ctx context.Context, st issueGetter, awaitID string) (bool, string, error) {
	c, err := inspectBeadGate(ctx, st, awaitID)
	return c.resolved, c.reason, err
}

func TestCheckBeadGate_CrossRigUsesBeadIDForRoutedLookup(t *testing.T) {
	ctx := context.Background()
	st := &fakeBeadGateGetter{issues: map[string]*types.Issue{
		"gt-abc": {ID: "gt-abc", Status: types.StatusClosed},
	}}

	satisfied, reason, _ := checkBeadGate(ctx, st, "gastown:gt-abc")
	if !satisfied {
		t.Fatalf("expected closed routed bead to satisfy gate, got reason %q", reason)
	}
	if st.gotID != "gt-abc" {
		t.Fatalf("routed lookup ID = %q, want %q", st.gotID, "gt-abc")
	}
}

func TestProxiedFreshReadGetterPreservesLocalNotFoundWhenRoutingUnavailable(t *testing.T) {
	withStubbedProxiedLookup(t, nil)

	oldDBPath := dbPath
	dbPath = filepath.Join(t.TempDir(), ".beads", "dolt")
	t.Cleanup(func() { dbPath = oldDBPath })

	_, err := (proxiedFreshReadGetter{}).GetIssue(context.Background(), stubMissingID)
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("GetIssue error = %v, want original local not-found", err)
	}
	if errors.Is(err, errBeadGateTargetUnconfirmed) {
		t.Fatalf("GetIssue error = %v: with no route the local miss is authoritative, not unconfirmed", err)
	}
	issue, local, _ := (proxiedFreshReadGetter{}).getBeadGateTarget(context.Background(), stubMissingID)
	if issue != nil || !local {
		t.Fatalf("getBeadGateTarget = (%v, local %v), want (nil, local true)", issue, local)
	}
}

func TestCheckBeadGate_InvalidCrossRigFormat(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		awaitID string
	}{
		{name: "missing rig", awaitID: ":gt-abc"},
		{name: "missing bead", awaitID: "my-project:"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			satisfied, reason, _ := checkBeadGate(ctx, nil, tt.awaitID)
			if satisfied {
				t.Errorf("expected not satisfied for %q", tt.awaitID)
			}
			if !gateTestContainsIgnoreCase(reason, "expected <rig>:<bead-id>") {
				t.Errorf("reason %q does not describe the expected format", reason)
			}
		})
	}
}

func TestCheckBeadGate_EmptyAwaitID(t *testing.T) {
	satisfied, reason, _ := checkBeadGate(context.Background(), nil, "")
	if satisfied {
		t.Error("expected not satisfied for empty await_id")
	}
	if reason == "" {
		t.Error("expected reason to be set")
	}
}

func TestCheckBeadGate_LocalBead(t *testing.T) {
	// A plain (no-colon) await_id is a bead in this rig's own database
	// (wy-hgms2): closed resolves the gate, anything else stays pending with
	// a status-bearing reason.
	ctx := context.Background()
	st := &fakeBeadGateGetter{
		issues: map[string]*types.Issue{
			"bd-closed": {ID: "bd-closed", Status: types.StatusClosed},
			"bd-open":   {ID: "bd-open", Status: types.StatusOpen},
		},
	}

	satisfied, reason, _ := checkBeadGate(ctx, st, "bd-closed")
	if !satisfied {
		t.Errorf("expected satisfied for closed local bead, got reason %q", reason)
	}
	if !gateTestContainsIgnoreCase(reason, "closed") {
		t.Errorf("reason %q does not mention closed", reason)
	}

	satisfied, reason, _ = checkBeadGate(ctx, st, "bd-open")
	if satisfied {
		t.Error("expected not satisfied for open local bead")
	}
	if !gateTestContainsIgnoreCase(reason, "open") {
		t.Errorf("reason %q does not mention the bead status", reason)
	}
}

// A gate awaiting a bead that no longer exists can never resolve on its own:
// the bead will never close. Treat the absence as resolution rather than
// leaving the gate pending forever.
func TestCheckBeadGate_LocalBeadNotFound(t *testing.T) {
	st := &fakeBeadGateGetter{issues: map[string]*types.Issue{}}
	satisfied, reason, err := checkBeadGate(context.Background(), st, "bd-missing")
	if err != nil {
		t.Fatalf("a missing bead is not an error, got %v", err)
	}
	if !satisfied {
		t.Errorf("expected satisfied for missing local bead, got reason %q", reason)
	}
	if !gateTestContainsIgnoreCase(reason, "no longer exists") {
		t.Errorf("reason %q does not mention that the bead no longer exists", reason)
	}
}

// Negative control: a genuine backend failure is not a missing bead, so it
// is an error, neither pending nor resolved.
func TestCheckBeadGate_LocalBeadLookupError(t *testing.T) {
	// A store that cannot be read is an error, not a pending gate: the
	// caller must be able to tell "dolt is down" from "still waiting".
	boom := errors.New("dolt exploded")
	st := &fakeBeadGateGetter{err: boom}
	satisfied, reason, err := checkBeadGate(context.Background(), st, "bd-abc")
	if satisfied {
		t.Error("expected not satisfied on lookup error")
	}
	if err == nil {
		t.Fatal("expected a lookup error to be returned as an error")
	}
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want it to wrap the lookup error", err)
	}
	if !gateTestContainsIgnoreCase(err.Error(), "dolt exploded") {
		t.Errorf("err %q does not carry the lookup error", err)
	}
	if reason != "" {
		t.Errorf("reason = %q, want empty on an error", reason)
	}
}

func TestCheckBeadGate_LocalBeadNotFoundErrorResolves(t *testing.T) {
	// A getter that reports absence through an error (storage.ErrNotFound
	// from a store, sql.ErrNoRows from the proxied domain seam, or the
	// partial-ID resolver's text) is a missing bead, not a broken store: the
	// gate resolves rather than pending forever on a bead that can never close.
	for _, tt := range []struct {
		name string
		err  error
	}{
		{name: "storage sentinel", err: storage.ErrNotFound},
		{name: "wrapped storage sentinel", err: fmt.Errorf("lookup bd-missing: %w", storage.ErrNotFound)},
		{name: "sql no rows", err: sql.ErrNoRows},
		{name: "wrapped sql no rows", err: fmt.Errorf("query bead: %w", sql.ErrNoRows)},
		{name: "partial-id resolver text", err: errors.New("no issue found matching bd-missing")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st := &fakeBeadGateGetter{err: tt.err}
			satisfied, reason, err := checkBeadGate(context.Background(), st, "bd-missing")
			if err != nil {
				t.Fatalf("not-found is a missing bead, not an error, got %v", err)
			}
			if !satisfied {
				t.Errorf("expected satisfied for a missing bead, got reason %q", reason)
			}
			if !gateTestContainsIgnoreCase(reason, "no longer exists") {
				t.Errorf("reason %q does not mention that the bead no longer exists", reason)
			}
		})
	}
}

func TestEvaluateGates_BeadStoreErrorCountsAsError(t *testing.T) {
	// The bead arm used to drop the lookup error on the floor, so a dead
	// store reported every bead gate as pending and the check exited 0.
	gate := &types.Issue{ID: "bd-gate", IssueType: "gate", AwaitType: "bead", AwaitID: "bd-abc"}
	st := &fakeBeadGateGetter{err: errors.New("dolt exploded")}

	results := evaluateGates(context.Background(), []*types.Issue{gate}, time.Now(), st, nil, nil)
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if results[0].err == nil {
		t.Fatal("expected the store error on the result")
	}
	if results[0].resolved {
		t.Error("a store error must not resolve the gate")
	}

	closeCalls := 0
	var resolved, escalated, errCount int
	_ = captureGateStdout(t, func() {
		resolved, escalated, errCount = applyGateCheckResults(results, false, false, func(*types.Issue, string) error {
			closeCalls++
			return nil
		})
	})
	if errCount != 1 || resolved != 0 || escalated != 0 {
		t.Errorf("counts = (resolved %d, escalated %d, errors %d), want (0, 0, 1)", resolved, escalated, errCount)
	}
	if closeCalls != 0 {
		t.Errorf("closeResolved called %d times on an errored gate", closeCalls)
	}
}

func TestCheckBeadGate_UnconfirmedMissStaysPending(t *testing.T) {
	// A miss outside this rig's own store (a route whose rig cannot be read,
	// or a routed store that did not return the bead) does not prove the bead
	// is gone. The gate stays pending with the cause, even when the wrapped
	// error is itself a not-found, and it is not an error row either.
	for _, tt := range []struct {
		name string
		err  error
	}{
		{name: "routed store not-found", err: fmt.Errorf("%w: bead gt-open routes to gastown: %w", errBeadGateTargetUnconfirmed, fmt.Errorf("get gt-open: %w", storage.ErrNotFound))},
		{name: "routed partial-id miss", err: fmt.Errorf("%w: bead gt-open routes to gastown: %w", errBeadGateTargetUnconfirmed, errors.New("no issue found matching gt-open"))},
		{name: "routed store cannot be opened", err: fmt.Errorf("%w: bead gt-open routes to gastown: %w", errBeadGateTargetUnconfirmed, errors.New("target rig has no dolt_database configured"))},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st := &fakeBeadGateGetter{err: tt.err}
			satisfied, reason, err := checkBeadGate(context.Background(), st, "gastown:gt-open")
			if err != nil {
				t.Fatalf("an unconfirmed miss is a pending gate, not an error, got %v", err)
			}
			if satisfied {
				t.Fatalf("an unconfirmed miss resolved the gate: %s", reason)
			}
			if !strings.Contains(reason, "cannot confirm") {
				t.Errorf("reason %q does not say the absence is unconfirmed", reason)
			}
		})
	}
}

// fakeRoutedBeadGateGetter answers like a getter that reached the bead
// through a route: the answer did not come from this rig's own store.
type fakeRoutedBeadGateGetter struct{ fakeBeadGateGetter }

func (f *fakeRoutedBeadGateGetter) getBeadGateTarget(ctx context.Context, id string) (*types.Issue, bool, error) {
	issue, err := f.GetIssue(ctx, id)
	return issue, false, err
}

func TestEvaluateGates_BeadGateSightingRule(t *testing.T) {
	// bd gate check resolves a gate whose awaited bead is gone only when an
	// earlier check saw that bead in this rig, so an await_id that never
	// named a real bead stays pending instead of unblocking its step.
	const seen = `{"await_seen":"bd-target"}`
	open := map[string]*types.Issue{"bd-target": {ID: "bd-target", Status: types.StatusOpen}}
	closed := map[string]*types.Issue{"bd-target": {ID: "bd-target", Status: types.StatusClosed}}
	// The getter is the gate's own store, which a check reads the gate back
	// from once the bead is gone: there the gate records its sighting.
	missing := map[string]*types.Issue{"bd-gate": {ID: "bd-gate", IssueType: "gate", AwaitType: "bead", AwaitID: "bd-target", Metadata: json.RawMessage(seen)}}
	unconfirmed := fmt.Errorf("%w: bead bd-target routes to rig: %w", errBeadGateTargetUnconfirmed, storage.ErrNotFound)

	for _, tt := range []struct {
		name         string
		getter       issueGetter
		metadata     string
		wantResolved bool
		wantReason   string
		wantRecords  int
	}{
		{name: "missing and never seen", getter: &fakeBeadGateGetter{issues: missing}, wantReason: "no earlier gate check saw it"},
		{name: "missing after a sighting", getter: &fakeBeadGateGetter{issues: missing}, metadata: seen, wantResolved: true, wantReason: "no longer exists"},
		{name: "missing, sighting of another await_id", getter: &fakeBeadGateGetter{issues: missing}, metadata: `{"await_seen":"bd-other"}`, wantReason: "no earlier gate check saw it"},
		{name: "open, first sighting", getter: &fakeBeadGateGetter{issues: open}, wantReason: "is open", wantRecords: 1},
		{name: "open, already seen", getter: &fakeBeadGateGetter{issues: open}, metadata: seen, wantReason: "is open"},
		{name: "open in another rig", getter: &fakeRoutedBeadGateGetter{fakeBeadGateGetter{issues: open}}, wantReason: "is open"},
		{name: "closed", getter: &fakeBeadGateGetter{issues: closed}, wantResolved: true, wantReason: "closed"},
		{name: "unconfirmed miss after a sighting", getter: &fakeBeadGateGetter{err: unconfirmed}, metadata: seen, wantReason: "cannot confirm"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gate := &types.Issue{ID: "bd-gate", IssueType: "gate", AwaitType: "bead", AwaitID: "bd-target"}
			if tt.metadata != "" {
				gate.Metadata = json.RawMessage(tt.metadata)
			}
			var recorded []*types.Issue
			recordSeen := func(g *types.Issue) error {
				recorded = append(recorded, g)
				return nil
			}

			results := evaluateGates(context.Background(), []*types.Issue{gate}, time.Now(), tt.getter, nil, recordSeen)
			if len(results) != 1 {
				t.Fatalf("results = %d, want 1", len(results))
			}
			r := results[0]
			if r.err != nil {
				t.Fatalf("err = %v, want a pending or resolved gate", r.err)
			}
			if r.resolved != tt.wantResolved {
				t.Errorf("resolved = %v, want %v (reason %q)", r.resolved, tt.wantResolved, r.reason)
			}
			if !strings.Contains(r.reason, tt.wantReason) {
				t.Errorf("reason %q does not contain %q", r.reason, tt.wantReason)
			}
			if len(recorded) != tt.wantRecords {
				t.Fatalf("recordSeen called %d times, want %d", len(recorded), tt.wantRecords)
			}
			if tt.wantRecords == 1 && recorded[0] != gate {
				t.Errorf("recordSeen got %v, want the checked gate", recorded[0])
			}
		})
	}
}

// gateReadBackGetter is a gate's own store in which the awaited bead is gone.
// Reading the gate back returns gate, as found through a route when routed,
// or fails with err.
type gateReadBackGetter struct {
	gate   *types.Issue
	routed bool
	err    error
}

func (g gateReadBackGetter) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	issue, _, err := g.getBeadGateTarget(ctx, id)
	return issue, err
}

func (g gateReadBackGetter) getBeadGateTarget(_ context.Context, id string) (*types.Issue, bool, error) {
	switch {
	case id != "bd-gate":
		return nil, true, fmt.Errorf("get %s: %w", id, storage.ErrNotFound)
	case g.err != nil:
		return nil, false, g.err
	case g.gate == nil:
		return nil, true, fmt.Errorf("get %s: %w", id, storage.ErrNotFound)
	}
	return g.gate, !g.routed, nil
}

func TestEvaluateBeadGate_GoneBeadReadsTheGateBack(t *testing.T) {
	// The gate a check holds can be older than a rename of its bead, which
	// moves the stored gate to the new ID before the old one goes. Once the
	// bead is gone, the gate resolves only if the stored gate still waits on
	// it with its sighting.
	gateWith := func(awaitID, metadata string) *types.Issue {
		g := &types.Issue{ID: "bd-gate", IssueType: "gate", AwaitType: "bead", AwaitID: awaitID}
		if metadata != "" {
			g.Metadata = json.RawMessage(metadata)
		}
		return g
	}
	listed := gateWith("bd-target", `{"await_seen":"bd-target"}`)
	boom := errors.New("dolt exploded")

	for _, tt := range []struct {
		name         string
		getter       gateReadBackGetter
		wantResolved bool
		wantReason   string
		wantErr      error
	}{
		{name: "stored as listed", getter: gateReadBackGetter{gate: gateWith("bd-target", `{"await_seen":"bd-target"}`)}, wantResolved: true, wantReason: "no longer exists"},
		{name: "moved by a rename", getter: gateReadBackGetter{gate: gateWith("bd-renamed", `{"await_seen":"bd-renamed"}`)}, wantReason: "the gate changed while it was being checked"},
		{name: "moved back without its sighting", getter: gateReadBackGetter{gate: gateWith("bd-target", "")}, wantReason: "the gate changed while it was being checked"},
		{name: "gate gone", getter: gateReadBackGetter{}, wantReason: "the gate could not be read back"},
		{name: "gate found only through a route", getter: gateReadBackGetter{gate: listed, routed: true}, wantReason: "the gate could not be read back"},
		{name: "gate behind an unconfirmed route", getter: gateReadBackGetter{err: fmt.Errorf("%w: bead bd-gate routes to rig: %w", errBeadGateTargetUnconfirmed, boom)}, wantReason: "the gate could not be read back"},
		{name: "gate cannot be read", getter: gateReadBackGetter{err: boom}, wantErr: boom},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gate := *listed
			resolved, reason, err := evaluateBeadGate(context.Background(), &gate, tt.getter, nil)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) || resolved || reason != "" {
					t.Fatalf("evaluateBeadGate = (%v, %q, %v), want only an error wrapping %v", resolved, reason, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want a pending or resolved gate", err)
			}
			if resolved != tt.wantResolved {
				t.Errorf("resolved = %v, want %v (reason %q)", resolved, tt.wantResolved, reason)
			}
			if !strings.Contains(reason, tt.wantReason) {
				t.Errorf("reason %q does not contain %q", reason, tt.wantReason)
			}
		})
	}
}

func TestEvaluateBeadGate_WithoutRecorder(t *testing.T) {
	// --dry-run passes no recorder: the open bead is reported and nothing is
	// written.
	gate := &types.Issue{ID: "bd-gate", AwaitType: "bead", AwaitID: "bd-target"}
	st := &fakeBeadGateGetter{issues: map[string]*types.Issue{"bd-target": {ID: "bd-target", Status: types.StatusOpen}}}
	resolved, reason, err := evaluateBeadGate(context.Background(), gate, st, nil)
	if err != nil || resolved || !strings.Contains(reason, "is open") {
		t.Fatalf("evaluateBeadGate = (%v, %q, %v), want a pending gate", resolved, reason, err)
	}
}

func TestEvaluateBeadGate_RecordFailureIsAnError(t *testing.T) {
	// A sighting that cannot be recorded would let the gate pend forever once
	// the bead is deleted, so the failure is reported rather than dropped.
	gate := &types.Issue{ID: "bd-gate", AwaitType: "bead", AwaitID: "bd-target"}
	st := &fakeBeadGateGetter{issues: map[string]*types.Issue{"bd-target": {ID: "bd-target", Status: types.StatusOpen}}}
	boom := errors.New("write refused")
	resolved, reason, err := evaluateBeadGate(context.Background(), gate, st, func(*types.Issue) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap the write error", err)
	}
	if !strings.Contains(err.Error(), "recording that bead bd-target exists") {
		t.Errorf("err %q does not say what failed", err)
	}
	if resolved || reason != "" {
		t.Errorf("evaluateBeadGate = (%v, %q), want neither resolved nor pending on an error", resolved, reason)
	}
}

func TestBeadGateSeenUpdate_KeepsOtherMetadata(t *testing.T) {
	old := &types.Issue{Metadata: json.RawMessage(`{"repo":"o/r"}`)}
	resolved, err := storageissueops.ResolveMergeOps(old, beadGateSeenUpdate("rig:bd-x"))
	if err != nil {
		t.Fatalf("ResolveMergeOps: %v", err)
	}
	meta, ok := resolved["metadata"].(json.RawMessage)
	if !ok {
		t.Fatalf("resolved metadata is %T, want json.RawMessage", resolved["metadata"])
	}
	var got map[string]string
	if err := json.Unmarshal(meta, &got); err != nil {
		t.Fatalf("resolved metadata %s: %v", meta, err)
	}
	if got["repo"] != "o/r" || got[beadGateSeenKey] != "rig:bd-x" {
		t.Errorf("metadata = %s, want repo kept and %s set to the await_id", meta, beadGateSeenKey)
	}

	gate := &types.Issue{AwaitID: "rig:bd-x", Metadata: meta}
	if !beadGateTargetSeen(gate) {
		t.Error("the recorded sighting is not read back")
	}
	gate.AwaitID = "rig:bd-y"
	if beadGateTargetSeen(gate) {
		t.Error("a sighting of the old await_id counts after the gate was retargeted")
	}
}

// TestBeadGateSeenPatchMatchesTheMapUpdate ties the sighting's two encodings
// together: the direct route records it as beadGateSeenPatch through the
// Lifecycle role, while the proxied route and rename still write
// beadGateSeenUpdate's map. A gate must read back the same sighting whichever
// route recorded it.
func TestBeadGateSeenPatchMatchesTheMapUpdate(t *testing.T) {
	for _, awaitID := range []string{"bd-x", "rig:bd-x", `bd-"quoted"`} {
		update := beadGateSeenUpdate(awaitID)
		set, ok := update[storageissueops.OpSetMetadata].(map[string]json.RawMessage)
		if len(update) != 1 || !ok {
			t.Fatalf("beadGateSeenUpdate(%q) = %v, want a single metadata set", awaitID, update)
		}
		want := issueops.IssuePatch{Metadata: issueops.MetadataPatch{Set: set}}
		if got := beadGateSeenPatch(awaitID); !reflect.DeepEqual(got, want) {
			t.Errorf("beadGateSeenPatch(%q) = %+v, want the map update's set %+v", awaitID, got, want)
		}
	}
}

func TestBeadGateTargetSeen(t *testing.T) {
	if beadGateTargetSeen(nil) {
		t.Error("a nil gate has no sighting")
	}
	for _, tt := range []struct {
		name     string
		awaitID  string
		metadata string
		want     bool
	}{
		{name: "no metadata", awaitID: "bd-x"},
		{name: "null metadata", awaitID: "bd-x", metadata: "null"},
		{name: "metadata not an object", awaitID: "bd-x", metadata: `["bd-x"]`},
		{name: "no sighting", awaitID: "bd-x", metadata: `{"repo":"o/r"}`},
		{name: "sighting not a string", awaitID: "bd-x", metadata: `{"await_seen":true}`},
		{name: "null sighting", awaitID: "bd-x", metadata: `{"await_seen":null}`},
		{name: "no await_id", metadata: `{"await_seen":""}`},
		{name: "sighting of this await_id", awaitID: "bd-x", metadata: `{"await_seen":"bd-x"}`, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gate := &types.Issue{AwaitID: tt.awaitID}
			if tt.metadata != "" {
				gate.Metadata = json.RawMessage(tt.metadata)
			}
			if got := beadGateTargetSeen(gate); got != tt.want {
				t.Errorf("beadGateTargetSeen = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBeadGateRetargetUpdate(t *testing.T) {
	// The update points a gate at a new await_id and drops its sighting only
	// when asked; the rest of the metadata is kept either way.
	gate := &types.Issue{AwaitID: "bd-old", Metadata: json.RawMessage(`{"repo":"o/r","await_seen":"bd-old"}`)}
	for _, tt := range []struct {
		name     string
		dropSeen bool
		wantMeta string // "" means the metadata is not written
	}{
		{name: "sighting kept"},
		{name: "sighting dropped", dropSeen: true, wantMeta: `{"repo":"o/r"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resolved, err := storageissueops.ResolveMergeOps(gate, beadGateRetargetUpdate("rig:bd-new", tt.dropSeen))
			if err != nil {
				t.Fatalf("ResolveMergeOps: %v", err)
			}
			if resolved["await_id"] != "rig:bd-new" {
				t.Errorf("await_id = %v, want %q", resolved["await_id"], "rig:bd-new")
			}
			meta, written := resolved["metadata"]
			switch {
			case tt.wantMeta == "" && written:
				t.Errorf("metadata = %s, want it left alone", meta)
			case tt.wantMeta != "" && !written:
				t.Errorf("metadata not written, want %s", tt.wantMeta)
			case tt.wantMeta != "":
				if got, ok := meta.(json.RawMessage); !ok || string(got) != tt.wantMeta {
					t.Errorf("metadata = %s, want %s", meta, tt.wantMeta)
				}
			}
		})
	}
}

func TestPlanBeadGateMoves(t *testing.T) {
	// A rename moves the bead gates that wait on the renamed bead, and only
	// those. A <rig>: prefix on await_id is kept.
	gate := func(id, awaitType, awaitID, metadata string) *types.Issue {
		g := &types.Issue{ID: id, IssueType: types.TypeGate, AwaitType: awaitType, AwaitID: awaitID}
		if metadata != "" {
			g.Metadata = json.RawMessage(metadata)
		}
		return g
	}
	issues := []*types.Issue{
		gate("bd-g1", "bead", "bd-old", ""),
		gate("bd-g2", "bead", "rig:bd-old", `{"await_seen":"rig:bd-old"}`),
		gate("bd-g3", "bead", "bd-old", `{"await_seen":"bd-new"}`),
		gate("bd-g4", "bead", "bd-old.1", `{"await_seen":"bd-old.1"}`),
		gate("bd-g5", "gh:run", "bd-old", ""),
		gate("bd-g6", "bead", ":bd-old", ""),
		{ID: "bd-task", IssueType: types.TypeTask, AwaitType: "bead", AwaitID: "bd-old"},
	}
	got := planBeadGateMoves(beadGatesByTarget(issues)["bd-old"], "bd-old", "bd-new")
	want := []beadGateMove{
		{gateID: "bd-g1", fromID: "bd-old", toID: "bd-new"},
		{gateID: "bd-g2", fromID: "rig:bd-old", toID: "rig:bd-new", seen: true},
		{gateID: "bd-g3", fromID: "bd-old", toID: "bd-new", staleSeen: true},
	}
	if !slices.Equal(got, want) {
		t.Errorf("moves = %+v\nwant    %+v", got, want)
	}
}

// renameGateStore is an in-memory store for renameIssueKeepingBeadGates whose
// writes can fail. Writes are numbered from 1: failAt fails one write, or with
// failRest every write from it on, and with landed a failing write is applied
// before it returns its error, like a commit whose response was lost. After
// each applied write it checks that bd gate check would resolve no gate:
// every issue it holds is open, so none may resolve, whether the check reads
// the gates now or listed them before the rename began.
type renameGateStore struct {
	t        *testing.T
	issues   map[string]*types.Issue
	listed   []*types.Issue // a concurrent check's copies, listed before the rename
	writes   int
	failAt   int
	failRest bool
	landed   bool
}

func newRenameGateStore(t *testing.T, issues []*types.Issue) *renameGateStore {
	s := &renameGateStore{t: t, issues: make(map[string]*types.Issue, len(issues))}
	for _, issue := range issues {
		c := *issue
		s.issues[issue.ID] = &c
	}
	return s
}

func (s *renameGateStore) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	issue, ok := s.issues[id]
	if !ok {
		return nil, fmt.Errorf("issue %s: %w", id, storage.ErrNotFound)
	}
	c := *issue
	return &c, nil
}

func (s *renameGateStore) UpdateIssue(_ context.Context, id string, updates map[string]interface{}, _ string) error {
	return s.write(func() error {
		issue, ok := s.issues[id]
		if !ok {
			return fmt.Errorf("issue %s: %w", id, storage.ErrNotFound)
		}
		resolved, err := storageissueops.ResolveMergeOps(issue, updates)
		if err != nil {
			return err
		}
		updated := *issue
		for field, value := range resolved {
			switch field {
			case "await_id":
				updated.AwaitID, ok = value.(string)
			case "metadata":
				updated.Metadata, ok = value.(json.RawMessage)
			default:
				ok = false
			}
			if !ok {
				return fmt.Errorf("unexpected update %s=%v", field, value)
			}
		}
		*issue = updated
		return nil
	})
}

func (s *renameGateStore) UpdateIssueID(_ context.Context, oldID, newID string, issue *types.Issue, _ string) error {
	return s.write(func() error {
		stored, ok := s.issues[oldID]
		if !ok {
			return fmt.Errorf("issue %s: %w", oldID, storage.ErrNotFound)
		}
		// Like UpdateIssueIDInTx, write the ID and the text fields: await_id
		// and metadata keep their stored values.
		delete(s.issues, oldID)
		stored.ID, stored.Title = newID, issue.Title
		s.issues[newID] = stored
		return nil
	})
}

func (s *renameGateStore) write(apply func() error) error {
	s.writes++
	failing := s.failAt > 0 && (s.writes == s.failAt || (s.failRest && s.writes > s.failAt))
	if failing && !s.landed {
		return errors.New("injected write failure")
	}
	if err := apply(); err != nil {
		return err
	}
	s.requireNoGateResolves()
	if failing {
		return errors.New("injected lost commit response")
	}
	return nil
}

func (s *renameGateStore) requireNoGateResolves() {
	s.t.Helper()
	for _, issue := range s.issues {
		s.requireGateDoesNotResolve(issue, "")
	}
	for _, issue := range s.listed {
		s.requireGateDoesNotResolve(issue, " listed before the rename")
	}
}

func (s *renameGateStore) requireGateDoesNotResolve(issue *types.Issue, note string) {
	s.t.Helper()
	if issue.IssueType != types.TypeGate {
		return
	}
	gate := *issue
	resolved, reason, err := evaluateBeadGate(context.Background(), &gate, s, nil)
	if resolved || err != nil {
		s.t.Errorf("after write %d, gate %s%s (await_id %q, metadata %s) resolves: %q, err %v", s.writes, gate.ID, note, gate.AwaitID, gate.Metadata, reason, err)
	}
}

// rename renames bd-old to bd-new the way bd rename does, with the gates
// listed before the first write. A concurrent bd gate check that listed them
// at the same moment holds its own copies, kept in s.listed.
func (s *renameGateStore) rename() error {
	listing := make([]*types.Issue, 0, len(s.issues))
	s.listed = make([]*types.Issue, 0, len(s.issues))
	for _, issue := range s.issues {
		renames, checks := *issue, *issue
		listing = append(listing, &renames)
		s.listed = append(s.listed, &checks)
	}
	slices.SortFunc(listing, func(a, b *types.Issue) int { return strings.Compare(a.ID, b.ID) })
	issue, err := s.GetIssue(context.Background(), "bd-old")
	if err != nil {
		return err
	}
	return renameIssueKeepingBeadGates(context.Background(), s, issue, "bd-new", beadGatesByTarget(listing)["bd-old"], "test")
}

// renameGateFixtures each hold bd-old, the issue renamed to bd-new, with
// the bead gates on it, and the gates expected after the rename by ID.
var renameGateFixtures = []struct {
	name   string
	issues []*types.Issue
	want   map[string]types.Issue // only AwaitID and Metadata are compared
}{
	{
		name: "gates on a bead",
		issues: []*types.Issue{
			{ID: "bd-old", Status: types.StatusOpen, IssueType: types.TypeTask},
			{ID: "bd-g1", Status: types.StatusOpen, IssueType: types.TypeGate, AwaitType: "bead", AwaitID: "bd-old",
				Metadata: json.RawMessage(`{"repo":"o/r","await_seen":"bd-old"}`)},
			{ID: "bd-g2", Status: types.StatusOpen, IssueType: types.TypeGate, AwaitType: "bead", AwaitID: "rig:bd-old"},
			{ID: "bd-g3", Status: types.StatusOpen, IssueType: types.TypeGate, AwaitType: "bead", AwaitID: "bd-old",
				Metadata: json.RawMessage(`{"await_seen":"bd-new"}`)},
		},
		want: map[string]types.Issue{
			"bd-g1": {AwaitID: "bd-new", Metadata: json.RawMessage(`{"await_seen":"bd-new","repo":"o/r"}`)},
			"bd-g2": {AwaitID: "rig:bd-new"},
			"bd-g3": {AwaitID: "bd-new", Metadata: json.RawMessage(`{}`)},
		},
	},
	{
		name: "a gate on itself",
		issues: []*types.Issue{
			{ID: "bd-old", Status: types.StatusOpen, IssueType: types.TypeGate, AwaitType: "bead", AwaitID: "bd-old",
				Metadata: json.RawMessage(`{"await_seen":"bd-old"}`)},
		},
		want: map[string]types.Issue{
			"bd-new": {AwaitID: "bd-new", Metadata: json.RawMessage(`{"await_seen":"bd-new"}`)},
		},
	},
}

func TestRenameIssueKeepingBeadGates(t *testing.T) {
	// A rename points each bead gate on the bead at the new ID. A gate that
	// had seen the bead keeps its sighting under the new ID; one that had not,
	// or whose sighting named the new ID before the bead had it, is unseen.
	for _, fx := range renameGateFixtures {
		t.Run(fx.name, func(t *testing.T) {
			st := newRenameGateStore(t, fx.issues)
			if err := st.rename(); err != nil {
				t.Fatalf("rename: %v", err)
			}
			for id, want := range fx.want {
				got, ok := st.issues[id]
				if !ok {
					t.Errorf("gate %s is missing after the rename", id)
					continue
				}
				if got.AwaitID != want.AwaitID || string(got.Metadata) != string(want.Metadata) {
					t.Errorf("gate %s: await_id=%q metadata=%s, want %q and %s", id, got.AwaitID, got.Metadata, want.AwaitID, want.Metadata)
				}
			}
		})
	}
}

func TestRenameIssueKeepingBeadGates_FailedWritesResolveNoGate(t *testing.T) {
	// UpdateIssueID commits on its own, so a rename takes several writes, and
	// any of them can fail, or land and still return an error. No state they
	// leave may let bd gate check resolve a gate whose bead exists; the store
	// checks after every write. A rename that fails before anything lands
	// leaves each gate on bd-old without a sighting, for a check to record
	// again.
	for _, fx := range renameGateFixtures {
		probe := newRenameGateStore(t, fx.issues)
		if err := probe.rename(); err != nil {
			t.Fatalf("%s: rename without failures: %v", fx.name, err)
		}
		for n := 1; n <= probe.writes; n++ {
			for _, mode := range []struct {
				name             string
				failRest, landed bool
			}{
				{name: "fails"},
				{name: "lands and fails", landed: true},
				{name: "fails with every later write", failRest: true},
				{name: "lands and fails with every later write", failRest: true, landed: true},
			} {
				t.Run(fmt.Sprintf("%s/write %d of %d %s", fx.name, n, probe.writes, mode.name), func(t *testing.T) {
					st := newRenameGateStore(t, fx.issues)
					st.failAt, st.failRest, st.landed = n, mode.failRest, mode.landed
					err := st.rename()
					st.requireNoGateResolves()
					if err == nil {
						// Only a sighting that could not be moved is not an error.
						if _, ok := st.issues["bd-new"]; !ok {
							t.Fatal("rename returned no error, but bd-old was not renamed")
						}
						return
					}
					if mode.failRest || mode.landed {
						return
					}
					for _, issue := range fx.issues {
						got := st.issues[issue.ID]
						if issue.IssueType == types.TypeGate && (got.AwaitID != issue.AwaitID || beadGateTargetSeen(got)) {
							t.Errorf("gate %s after a failed rename: await_id=%q metadata=%s, want %q without a sighting", issue.ID, got.AwaitID, got.Metadata, issue.AwaitID)
						}
					}
				})
			}
		}
	}
}

// withBeadGateTown points dbPath at a fresh town and returns its .beads
// directory, where a test writes the routes.jsonl a missed lookup follows.
func withBeadGateTown(t *testing.T) string {
	t.Helper()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create town beads dir: %v", err)
	}
	oldDBPath := dbPath
	dbPath = filepath.Join(beadsDir, "dolt")
	t.Cleanup(func() { dbPath = oldDBPath })
	return beadsDir
}

func TestProxiedFreshReadGetter_LocalMissFollowsRoutes(t *testing.T) {
	// The proxied getter must not report a route it could not follow as the
	// local not-found: an OPEN bead in a rig that cannot be read would
	// resolve its gate.
	for _, tt := range []struct {
		name         string
		routes       string // routes.jsonl content; empty makes it a directory, which cannot be read
		awaitID      string
		wantResolved bool
		wantReason   []string
	}{
		{
			name:       "route to a rig that cannot be read",
			routes:     `{"prefix":"gt-","path":"gastown"}`,
			awaitID:    "gastown:gt-open",
			wantReason: []string{"cannot confirm", "routes to gastown", "no dolt_database"},
		},
		{
			name:       "routes.jsonl cannot be read",
			awaitID:    stubMissingID,
			wantReason: []string{"cannot confirm", "reading routes.jsonl"},
		},
		{
			name:         "no route for the prefix",
			routes:       `{"prefix":"gt-","path":"gastown"}`,
			awaitID:      stubMissingID,
			wantResolved: true,
			wantReason:   []string{"no longer exists"},
		},
		{
			name:         "route back to this rig",
			routes:       `{"prefix":"bd-","path":"."}`,
			awaitID:      stubMissingID,
			wantResolved: true,
			wantReason:   []string{"no longer exists"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			withStubbedProxiedLookup(t, nil)
			routesPath := filepath.Join(withBeadGateTown(t), "routes.jsonl")
			if tt.routes == "" {
				if err := os.Mkdir(routesPath, 0o755); err != nil {
					t.Fatalf("create unreadable routes.jsonl: %v", err)
				}
			} else if err := os.WriteFile(routesPath, []byte(tt.routes), 0o644); err != nil {
				t.Fatalf("write routes.jsonl: %v", err)
			}

			resolved, reason, err := checkBeadGate(context.Background(), proxiedFreshReadGetter{}, tt.awaitID)
			if err != nil {
				t.Fatalf("checkBeadGate error = %v, want a pending or resolved gate", err)
			}
			if resolved != tt.wantResolved {
				t.Errorf("resolved = %v, want %v (reason %q)", resolved, tt.wantResolved, reason)
			}
			for _, want := range tt.wantReason {
				if !strings.Contains(reason, want) {
					t.Errorf("reason %q does not contain %q", reason, want)
				}
			}
		})
	}
}

// beadGateLocalStore stands in for this rig's own store: GetIssue answers
// from issues (or fails with err), and no contributor routing is configured.
// Anything else is a nil call.
type beadGateLocalStore struct {
	storage.DoltStorage
	issues map[string]*types.Issue
	err    error
}

func (s *beadGateLocalStore) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	if s.err != nil {
		return nil, s.err
	}
	if issue, ok := s.issues[id]; ok {
		return issue, nil
	}
	return nil, fmt.Errorf("get %s: %w", id, storage.ErrNotFound)
}

func (s *beadGateLocalStore) GetAllConfig(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}

func TestRoutedBeadGateGetter_ReportsWhereTheAnswerCameFrom(t *testing.T) {
	ctx := context.Background()
	beadsDir := withBeadGateTown(t)
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(`{"prefix":"gt-","path":"gastown"}`), 0o644); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}
	getter := routedBeadGateGetter{localStore: &beadGateLocalStore{issues: map[string]*types.Issue{
		"bd-open": {ID: "bd-open", Status: types.StatusOpen},
	}}}

	issue, local, err := getter.getBeadGateTarget(ctx, "bd-open")
	if err != nil || issue == nil || !local {
		t.Errorf("local bead: getBeadGateTarget = (%v, local %v, %v), want the bead from this rig", issue, local, err)
	}

	issue, local, err = getter.getBeadGateTarget(ctx, "bd-missing")
	if !errors.Is(err, storage.ErrNotFound) || errors.Is(err, errBeadGateTargetUnconfirmed) || issue != nil || !local {
		t.Errorf("unrouted miss: getBeadGateTarget = (%v, local %v, %v), want this rig's not-found", issue, local, err)
	}

	issue, local, err = getter.getBeadGateTarget(ctx, "gt-open")
	if !errors.Is(err, errBeadGateTargetUnconfirmed) || issue != nil || local {
		t.Errorf("unreadable route: getBeadGateTarget = (%v, local %v, %v), want an unconfirmed miss", issue, local, err)
	}

	boom := errors.New("dolt exploded")
	_, _, err = routedBeadGateGetter{localStore: &beadGateLocalStore{err: boom}}.getBeadGateTarget(ctx, "gt-open")
	if !errors.Is(err, boom) || errors.Is(err, errBeadGateTargetUnconfirmed) {
		t.Errorf("local read failure: err = %v, want the read error, not a routed miss", err)
	}
}

func TestPrintGateCheckSummary_ErrorsFailTheCommand(t *testing.T) {
	origJSON := jsonOutput
	t.Cleanup(func() { jsonOutput = origJSON })

	for _, tt := range []struct {
		name    string
		json    bool
		errors  int
		wantErr bool
	}{
		{name: "clean sweep", errors: 0, wantErr: false},
		{name: "one unreadable gate", errors: 1, wantErr: true},
		{name: "json still fails", json: true, errors: 2, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			jsonOutput = tt.json
			var err error
			out := captureGateStdout(t, func() {
				err = printGateCheckSummary(3, 1, 0, tt.errors, false)
			})
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v (output %q)", err, tt.wantErr, out)
			}
			if !strings.Contains(out, "Checked 3 gates") {
				t.Errorf("summary line missing from output %q", out)
			}
			if tt.wantErr && !strings.Contains(err.Error(), "could not be checked or closed") {
				t.Errorf("err %q does not say the gates could not be checked or closed", err)
			}
		})
	}
}

func TestCheckBeadGate_NilStoreStaysPending(t *testing.T) {
	satisfied, reason, _ := checkBeadGate(context.Background(), nil, "bd-abc")
	if satisfied {
		t.Error("expected not satisfied with no store")
	}
	if reason == "" {
		t.Error("expected reason to be set")
	}
}

func TestCheckGHPRUsesStateWithoutMergedField(t *testing.T) {
	resolved, escalated, reason, err := checkGHPRWithRunner(&types.Issue{
		IssueType: "gate",
		AwaitType: "gh:pr",
		AwaitID:   "3488",
	}, fakeGHRunner(t,
		`{"state":"MERGED","title":"Fix gate"}`,
		"pr", "view", "3488", "--json", "state,title",
	))
	if err != nil {
		t.Fatalf("checkGHPR returned error: %v", err)
	}
	if !resolved {
		t.Fatal("expected merged PR to resolve")
	}
	if escalated {
		t.Fatal("did not expect merged PR to escalate")
	}
	if !gateTestContains(reason, "was merged") {
		t.Fatalf("reason = %q, want merged message", reason)
	}
}

func TestCheckGHPRUsesRepositoryFromMetadata(t *testing.T) {
	resolved, escalated, reason, err := checkGHPRWithRunner(&types.Issue{
		IssueType: "gate",
		AwaitType: "gh:pr",
		AwaitID:   "608",
		Metadata:  json.RawMessage(`{"repo":"srobroek/agentic-packages"}`),
	}, fakeGHRunner(t,
		`{"state":"MERGED","title":"Cross-repo gate"}`,
		"pr", "view", "608", "--json", "state,title", "--repo", "srobroek/agentic-packages",
	))
	if err != nil {
		t.Fatalf("checkGHPR returned error: %v", err)
	}
	if !resolved || escalated {
		t.Fatalf("resolved, escalated = %v, %v; want true, false (%s)", resolved, escalated, reason)
	}
}

func TestCheckGHRunUsesRepositoryFromMetadata(t *testing.T) {
	resolved, escalated, reason, err := checkGHRunWithRunner(&types.Issue{
		IssueType: "gate",
		AwaitType: "gh:run",
		AwaitID:   "12345",
		Metadata:  json.RawMessage(`{"repo":"srobroek/agentic-packages"}`),
	}, nil,
		fakeGHRunner(t,
			`{"status":"completed","conclusion":"success","name":"CI"}`,
			"run", "view", "12345", "--json", "status,conclusion,name", "--repo", "srobroek/agentic-packages",
		),
	)
	if err != nil {
		t.Fatalf("checkGHRun returned error: %v", err)
	}
	if !resolved || escalated {
		t.Fatalf("resolved, escalated = %v, %v; want true, false (%s)", resolved, escalated, reason)
	}
}

// TestCheckGHRun_CrossRepoDiscoveryUsesInjectedRunner covers the standards
// note on the SF1 review: discoverRunIDByWorkflowNameInRepo was hard-wired to
// runGHCommand, so the cross-repo discovery path (a workflow-name hint plus
// metadata.repo) could not be exercised through the injected ghCommandRunner
// seam at all. Both the discovery "run list" call and the follow-up "run
// view" call must go through the same fake runner - if either one reached
// the real runGHCommand this test would fail (or hang) instead of using the
// canned response below.
func TestCheckGHRun_CrossRepoDiscoveryUsesInjectedRunner(t *testing.T) {
	var calls [][]string
	fakeRunner := func(args ...string) (stdout, stderr []byte, err error) {
		calls = append(calls, append([]string(nil), args...))
		switch args[0] {
		case "run":
			if len(args) > 1 && args[1] == "list" {
				return []byte(`[{"databaseId":999,"name":"release","status":"completed","conclusion":"success","workflowName":"release.yml"}]`), nil, nil
			}
			if len(args) > 1 && args[1] == "view" {
				return []byte(`{"status":"completed","conclusion":"success","name":"CI"}`), nil, nil
			}
		}
		t.Fatalf("unexpected gh invocation: %v", args)
		return nil, nil, nil
	}

	resolved, escalated, reason, err := checkGHRunWithRunner(&types.Issue{
		IssueType: "gate",
		AwaitType: "gh:run",
		AwaitID:   "release.yml",
		Metadata:  json.RawMessage(`{"repo":"srobroek/agentic-packages"}`),
	}, nil, fakeRunner)
	if err != nil {
		t.Fatalf("checkGHRun returned error: %v", err)
	}
	if !resolved || escalated {
		t.Fatalf("resolved, escalated = %v, %v; want true, false (%s)", resolved, escalated, reason)
	}

	wantCalls := [][]string{
		{"run", "list", "--workflow", "release.yml", "--json", "databaseId,name,status,conclusion,createdAt,workflowName", "--limit", "5", "--repo", "srobroek/agentic-packages"},
		{"run", "view", "999", "--json", "status,conclusion,name", "--repo", "srobroek/agentic-packages"},
	}
	if len(calls) != len(wantCalls) {
		t.Fatalf("gh invocations = %v, want %v", calls, wantCalls)
	}
	for i, want := range wantCalls {
		if !slices.Equal(calls[i], want) {
			t.Errorf("gh invocation %d = %v, want %v", i, calls[i], want)
		}
	}
}

func TestQueryGitHubRunsForWorkflowUsesRepository(t *testing.T) {
	runs, err := queryGitHubRunsForWorkflowInRepoWithRunner(
		"release.yml",
		5,
		"srobroek/agentic-packages",
		fakeGHRunner(t,
			`[{"databaseId":12345,"name":"release","status":"completed","conclusion":"success","workflowName":"release.yml"}]`,
			"run", "list", "--workflow", "release.yml", "--json", "databaseId,name,status,conclusion,createdAt,workflowName", "--limit", "5", "--repo", "srobroek/agentic-packages",
		),
	)
	if err != nil {
		t.Fatalf("queryGitHubRunsForWorkflowInRepo returned error: %v", err)
	}
	if len(runs) != 1 || runs[0].DatabaseID != 12345 {
		t.Fatalf("runs = %#v, want one run with database ID 12345", runs)
	}
}

func TestGitHubRepoFromIssueRejectsInvalidMetadata(t *testing.T) {
	tests := []struct {
		name     string
		metadata json.RawMessage
	}{
		{"missing_owner", json.RawMessage(`{"repo":"missing-owner"}`)},
		{"shell_metacharacter", json.RawMessage(`{"repo":"owner/repo;echo"}`)},
		{"metadata_not_an_object", json.RawMessage(`"not-an-object"`)},
		// SF3: an explicit JSON null must be rejected rather than silently
		// falling back to the current repository - the dangerous direction,
		// since it could point a cross-repo check at the wrong repo.
		{"repo_null", json.RawMessage(`{"repo":null}`)},
		{"repo_number", json.RawMessage(`{"repo":42}`)},
		{"repo_bool", json.RawMessage(`{"repo":true}`)},
		{"repo_object", json.RawMessage(`{"repo":{"owner":"a","name":"b"}}`)},
		{"repo_array", json.RawMessage(`{"repo":["a","b"]}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if repo, err := githubRepoFromIssue(&types.Issue{Metadata: tt.metadata}); err == nil {
				t.Fatalf("githubRepoFromIssue(%s) = %q, nil; want validation error", tt.metadata, repo)
			}
		})
	}
}

// TestGitHubRepoFromIssueAllowsMissingRepoKey verifies metadata without a
// "repo" key at all (as opposed to an explicit null) still falls back to the
// current repository without error - only an explicit malformed value is
// rejected (SF3).
func TestGitHubRepoFromIssueAllowsMissingRepoKey(t *testing.T) {
	tests := []struct {
		name     string
		metadata json.RawMessage
	}{
		{"nil_metadata", nil},
		{"null_metadata", json.RawMessage(`null`)},
		{"empty_object", json.RawMessage(`{}`)},
		{"unrelated_key", json.RawMessage(`{"priority":"high"}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, err := githubRepoFromIssue(&types.Issue{Metadata: tt.metadata})
			if err != nil {
				t.Fatalf("githubRepoFromIssue(%s) returned error: %v", tt.metadata, err)
			}
			if repo != "" {
				t.Fatalf("githubRepoFromIssue(%s) = %q, want empty", tt.metadata, repo)
			}
		})
	}
}

// TestRepoMetadataForGateRestrictsToGitHubTypes covers SF4: repo metadata
// inheritance/validation must only run for gh:* gate types. A human or timer
// gate blocking an issue with non-GitHub-shaped "repo" metadata (legal per
// the metadata contract - "any valid JSON") must not fail gate creation.
func TestRepoMetadataForGateRestrictsToGitHubTypes(t *testing.T) {
	badRepoMetadata := json.RawMessage(`{"repo":"not-owner-slash-repo"}`)

	nonGitHubTypes := []string{"human", "timer", "bead"}
	for _, gateType := range nonGitHubTypes {
		t.Run("ignores_bad_repo_metadata_for_"+gateType, func(t *testing.T) {
			metadata, err := repoMetadataForGate(gateType, &types.Issue{Metadata: badRepoMetadata})
			if err != nil {
				t.Fatalf("repoMetadataForGate(%q) returned error: %v; non-GitHub gates must tolerate arbitrary repo metadata", gateType, err)
			}
			if metadata != nil {
				t.Fatalf("repoMetadataForGate(%q) = %s, want nil metadata", gateType, metadata)
			}
		})
	}

	githubTypes := []string{"gh:run", "gh:pr"}
	for _, gateType := range githubTypes {
		t.Run("rejects_bad_repo_metadata_for_"+gateType, func(t *testing.T) {
			if _, err := repoMetadataForGate(gateType, &types.Issue{Metadata: badRepoMetadata}); err == nil {
				t.Fatalf("repoMetadataForGate(%q) = nil error, want validation error", gateType)
			}
		})

		t.Run("inherits_valid_repo_for_"+gateType, func(t *testing.T) {
			metadata, err := repoMetadataForGate(gateType, &types.Issue{
				Metadata: json.RawMessage(`{"repo":"srobroek/agentic-packages"}`),
			})
			if err != nil {
				t.Fatalf("repoMetadataForGate(%q) returned error: %v", gateType, err)
			}
			var decoded struct {
				Repo string `json:"repo"`
			}
			if unmarshalErr := json.Unmarshal(metadata, &decoded); unmarshalErr != nil {
				t.Fatalf("repoMetadataForGate(%q) = %s, not valid JSON: %v", gateType, metadata, unmarshalErr)
			}
			if decoded.Repo != "srobroek/agentic-packages" {
				t.Fatalf("repoMetadataForGate(%q) repo = %q, want srobroek/agentic-packages", gateType, decoded.Repo)
			}
		})
	}

	t.Run("no_metadata_no_repo", func(t *testing.T) {
		metadata, err := repoMetadataForGate("gh:run", &types.Issue{})
		if err != nil {
			t.Fatalf("repoMetadataForGate(gh:run) returned error: %v", err)
		}
		if metadata != nil {
			t.Fatalf("repoMetadataForGate(gh:run) = %s, want nil", metadata)
		}
	})
}

func TestIsNumericID(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		// Numeric IDs
		{"12345", true},
		{"12345678901234567890", true},
		{"0", true},
		{"1", true},

		// Non-numeric (workflow names, etc.)
		{"", false},
		{"release.yml", false},
		{"CI", false},
		{"release", false},
		{"123abc", false},
		{"abc123", false},
		{"12.34", false},
		{"-123", false},
		{"123-456", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := isNumericID(tt.input)
			if got != tt.want {
				t.Errorf("isNumericID(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestNeedsDiscovery(t *testing.T) {
	tests := []struct {
		name      string
		awaitType string
		awaitID   string
		want      bool
	}{
		// gh:run gates
		{"gh:run empty await_id", "gh:run", "", true},
		{"gh:run workflow name hint", "gh:run", "release.yml", true},
		{"gh:run workflow name without ext", "gh:run", "CI", true},
		{"gh:run numeric run ID", "gh:run", "12345", false},
		{"gh:run large numeric ID", "gh:run", "12345678901234567890", false},

		// Other gate types should not need discovery
		{"gh:pr gate", "gh:pr", "", false},
		{"timer gate", "timer", "", false},
		{"human gate", "human", "", false},
		{"bead gate", "bead", "rig:id", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate := &types.Issue{
				AwaitType: tt.awaitType,
				AwaitID:   tt.awaitID,
			}
			got := needsDiscovery(gate)
			if got != tt.want {
				t.Errorf("needsDiscovery(%q, %q) = %v, want %v",
					tt.awaitType, tt.awaitID, got, tt.want)
			}
		})
	}
}

func TestGetWorkflowNameHint(t *testing.T) {
	tests := []struct {
		name    string
		awaitID string
		want    string
	}{
		{"empty", "", ""},
		{"numeric ID", "12345", ""},
		{"workflow name", "release.yml", "release.yml"},
		{"workflow name yaml", "ci.yaml", "ci.yaml"},
		{"workflow name no ext", "CI", "CI"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate := &types.Issue{AwaitID: tt.awaitID}
			got := getWorkflowNameHint(gate)
			if got != tt.want {
				t.Errorf("getWorkflowNameHint(%q) = %q, want %q", tt.awaitID, got, tt.want)
			}
		})
	}
}

func TestCheckGHRun_DryRunDoesNotPersistDiscoveredRunID(t *testing.T) {
	origDiscover := discoverRunIDByWorkflowNameFunc
	origUpdate := updateGateAwaitIDFunc
	origStatus := checkGHRunStatusFunc
	t.Cleanup(func() {
		discoverRunIDByWorkflowNameFunc = origDiscover
		updateGateAwaitIDFunc = origUpdate
		checkGHRunStatusFunc = origStatus
	})

	updateCalls := 0
	discoverRunIDByWorkflowNameFunc = func(workflowHint string) (string, error) {
		if workflowHint != "release.yml" {
			t.Fatalf("unexpected workflow hint %q", workflowHint)
		}
		return "12345", nil
	}
	updateGateAwaitIDFunc = func(_ interface{}, gateID, runID string) error {
		updateCalls++
		t.Fatalf("unexpected await_id persistence for %s -> %s", gateID, runID)
		return nil
	}
	checkGHRunStatusFunc = func(runID string) (bool, bool, string, error) {
		if runID != "12345" {
			t.Fatalf("expected discovered run ID 12345, got %q", runID)
		}
		return true, false, "workflow 'release' succeeded", nil
	}

	resolved, escalated, reason, err := checkGHRun(&types.Issue{
		ID:      "bd-gate",
		AwaitID: "release.yml",
	}, nil)
	if err != nil {
		t.Fatalf("checkGHRun returned error: %v", err)
	}
	if !resolved {
		t.Fatal("expected dry-run check to resolve using discovered run status")
	}
	if escalated {
		t.Fatal("did not expect escalation for successful workflow")
	}
	if reason == "" {
		t.Fatal("expected resolution reason")
	}
	if updateCalls != 0 {
		t.Fatalf("expected no await_id updates during dry-run, got %d", updateCalls)
	}
}

func TestCheckGHRun_PersistsDiscoveredRunIDOutsideDryRun(t *testing.T) {
	origDiscover := discoverRunIDByWorkflowNameFunc
	origUpdate := updateGateAwaitIDFunc
	origStatus := checkGHRunStatusFunc
	t.Cleanup(func() {
		discoverRunIDByWorkflowNameFunc = origDiscover
		updateGateAwaitIDFunc = origUpdate
		checkGHRunStatusFunc = origStatus
	})

	updateCalls := 0
	discoverRunIDByWorkflowNameFunc = func(workflowHint string) (string, error) {
		if workflowHint != "release.yml" {
			t.Fatalf("unexpected workflow hint %q", workflowHint)
		}
		return "67890", nil
	}
	updateGateAwaitIDFunc = func(_ interface{}, gateID, runID string) error {
		updateCalls++
		if gateID != "bd-gate" {
			t.Fatalf("expected gate ID bd-gate, got %q", gateID)
		}
		if runID != "67890" {
			t.Fatalf("expected discovered run ID 67890, got %q", runID)
		}
		return nil
	}
	checkGHRunStatusFunc = func(runID string) (bool, bool, string, error) {
		if runID != "67890" {
			t.Fatalf("expected discovered run ID 67890, got %q", runID)
		}
		return false, false, "workflow 'release' is queued", nil
	}

	resolved, escalated, reason, err := checkGHRun(&types.Issue{
		ID:      "bd-gate",
		AwaitID: "release.yml",
	}, func(gateID, runID string) error { return updateGateAwaitIDFunc(nil, gateID, runID) })
	if err != nil {
		t.Fatalf("checkGHRun returned error: %v", err)
	}
	if resolved {
		t.Fatal("did not expect queued workflow to resolve")
	}
	if escalated {
		t.Fatal("did not expect queued workflow to escalate")
	}
	if reason == "" {
		t.Fatal("expected pending reason")
	}
	if updateCalls != 1 {
		t.Fatalf("expected one await_id update outside dry-run, got %d", updateCalls)
	}
}

func TestCheckGHRun_ReturnsErrorWhenPersistingDiscoveredRunIDFails(t *testing.T) {
	origDiscover := discoverRunIDByWorkflowNameFunc
	origUpdate := updateGateAwaitIDFunc
	origStatus := checkGHRunStatusFunc
	t.Cleanup(func() {
		discoverRunIDByWorkflowNameFunc = origDiscover
		updateGateAwaitIDFunc = origUpdate
		checkGHRunStatusFunc = origStatus
	})

	discoverRunIDByWorkflowNameFunc = func(workflowHint string) (string, error) {
		if workflowHint != "release.yml" {
			t.Fatalf("unexpected workflow hint %q", workflowHint)
		}
		return "12345", nil
	}
	updateGateAwaitIDFunc = func(_ interface{}, gateID, runID string) error {
		if gateID != "bd-gate" {
			t.Fatalf("expected gate ID bd-gate, got %q", gateID)
		}
		if runID != "12345" {
			t.Fatalf("expected discovered run ID 12345, got %q", runID)
		}
		return errors.New("write failed")
	}
	checkGHRunStatusFunc = func(runID string) (bool, bool, string, error) {
		t.Fatalf("did not expect status check after await_id persistence failure, got %q", runID)
		return false, false, "", nil
	}

	resolved, escalated, reason, err := checkGHRun(&types.Issue{
		ID:      "bd-gate",
		AwaitID: "release.yml",
	}, func(gateID, runID string) error { return updateGateAwaitIDFunc(nil, gateID, runID) })
	if err == nil {
		t.Fatal("expected checkGHRun to return an error when await_id persistence fails")
	}
	if resolved {
		t.Fatal("did not expect resolution when await_id persistence fails")
	}
	if escalated {
		t.Fatal("did not expect escalation when await_id persistence fails")
	}
	if reason != "" {
		t.Fatalf("expected empty reason on persistence failure, got %q", reason)
	}
	if !gateTestContains(err.Error(), "failed to update gate with discovered run ID") {
		t.Fatalf("expected wrapped persistence error, got %v", err)
	}
}

func TestCheckGHRunStatus_Success(t *testing.T) {
	resolved, escalated, reason, err := checkGHRunStatusInRepoWithRunner(
		"12345",
		"",
		fakeGHRunner(t,
			`{"status":"completed","conclusion":"success","name":"release"}`,
			"run", "view", "12345", "--json", "status,conclusion,name",
		),
	)
	if err != nil {
		t.Fatalf("checkGHRunStatus returned error: %v", err)
	}
	if !resolved {
		t.Fatal("expected successful workflow run to resolve the gate")
	}
	if escalated {
		t.Fatal("did not expect successful workflow run to escalate the gate")
	}
	if reason != "workflow 'release' succeeded" {
		t.Fatalf("checkGHRunStatus reason = %q, want %q", reason, "workflow 'release' succeeded")
	}
}

func TestGateCheck_GHRunWorkflowDiscoveryPersistence(t *testing.T) {
	tests := []struct {
		name            string
		dryRun          bool
		wantUpdateCalls int
		wantCloseCalls  int
		wantOutput      string
	}{
		{
			name:            "dry run keeps discovered run ID in memory only",
			dryRun:          true,
			wantUpdateCalls: 0,
			wantCloseCalls:  0,
			wantOutput:      "would resolve - workflow 'release' succeeded",
		},
		{
			name:            "live run persists discovered run ID before closing",
			dryRun:          false,
			wantUpdateCalls: 1,
			wantCloseCalls:  1,
			wantOutput:      "resolved - workflow 'release' succeeded",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origStore := store
			origRootCtx := rootCtx
			origJSONOutput := jsonOutput
			origReadonlyMode := readonlyMode
			origActor := actor
			origDiscover := discoverRunIDByWorkflowNameFunc
			origUpdate := updateGateAwaitIDFunc
			origStatus := checkGHRunStatusFunc
			t.Cleanup(func() {
				store = origStore
				rootCtx = origRootCtx
				jsonOutput = origJSONOutput
				readonlyMode = origReadonlyMode
				actor = origActor
				discoverRunIDByWorkflowNameFunc = origDiscover
				updateGateAwaitIDFunc = origUpdate
				checkGHRunStatusFunc = origStatus
				resetGateCheckFlags(t)
			})

			resetGateCheckFlags(t)

			fakeStore := &fakeGateCheckStore{
				issues: []*types.Issue{
					{
						ID:        "bd-gate",
						IssueType: "gate",
						AwaitType: "gh:run",
						AwaitID:   "release.yml",
					},
				},
			}

			store = fakeStore
			rootCtx = context.Background()
			jsonOutput = false
			readonlyMode = false
			actor = "test-actor"

			if err := gateCheckCmd.Flags().Set("dry-run", map[bool]string{true: "true", false: "false"}[tt.dryRun]); err != nil {
				t.Fatalf("set dry-run flag: %v", err)
			}
			if err := gateCheckCmd.Flags().Set("type", "gh:run"); err != nil {
				t.Fatalf("set type flag: %v", err)
			}
			if err := gateCheckCmd.Flags().Set("escalate", "false"); err != nil {
				t.Fatalf("set escalate flag: %v", err)
			}
			if err := gateCheckCmd.Flags().Set("limit", "100"); err != nil {
				t.Fatalf("set limit flag: %v", err)
			}

			discoverRunIDByWorkflowNameFunc = func(workflowHint string) (string, error) {
				if workflowHint != "release.yml" {
					t.Fatalf("unexpected workflow hint %q", workflowHint)
				}
				return "12345", nil
			}
			updateGateAwaitIDFunc = func(_ interface{}, _, _ string) error {
				t.Fatal("gate check persisted await_id through the legacy UpdateIssue path; want the Lifecycle role")
				return nil
			}
			checkGHRunStatusFunc = func(runID string) (bool, bool, string, error) {
				if runID != "12345" {
					t.Fatalf("expected discovered run ID 12345, got %q", runID)
				}
				return true, false, "workflow 'release' succeeded", nil
			}

			output := captureGateStdout(t, func() {
				if err := gateCheckCmd.RunE(gateCheckCmd, nil); err != nil {
					t.Fatalf("gateCheckCmd.RunE: %v", err)
				}
			})

			if len(fakeStore.updates) != tt.wantUpdateCalls {
				t.Fatalf("Lifecycle.Update call count = %d, want %d", len(fakeStore.updates), tt.wantUpdateCalls)
			}
			if tt.wantUpdateCalls == 1 {
				req := fakeStore.updates[0]
				if req.IssueID != "bd-gate" || req.Actor != "test-actor" {
					t.Fatalf("Lifecycle.Update = %+v, want bd-gate by test-actor", req)
				}
				want := issueops.IssuePatch{AwaitID: issueops.Field[string]{Set: true, Value: "12345"}}
				if !reflect.DeepEqual(req.Patch, want) {
					t.Fatalf("Lifecycle.Update patch = %+v, want only AwaitID=12345", req.Patch)
				}
			}
			if len(fakeStore.closeCalls) != tt.wantCloseCalls {
				t.Fatalf("CloseIssue call count = %d, want %d", len(fakeStore.closeCalls), tt.wantCloseCalls)
			}
			if !gateTestContains(output, tt.wantOutput) {
				t.Fatalf("output %q does not contain %q", output, tt.wantOutput)
			}
			if !gateTestContains(output, "Checked 1 gates: 1 resolved, 0 escalated, 0 errors") {
				t.Fatalf("summary output missing expected counts: %q", output)
			}
			if fakeStore.searchCalled {
				t.Fatal("gate check listed through the raw SearchIssues filter; want issueops.ListGates")
			}
			if len(fakeStore.queries) != 1 {
				t.Fatalf("Querier.Query calls = %d, want 1", len(fakeStore.queries))
			}
			q := fakeStore.queries[0]
			if q.Expression != "type=gate AND template=false" || q.IncludeClosed || q.Limit == nil || *q.Limit != 100 {
				t.Fatalf("gate check query = %+v, want type=gate AND template=false, closed excluded, limit 100", q)
			}
			if tt.wantCloseCalls == 1 {
				call := fakeStore.closeCalls[0]
				if call.id != "bd-gate" {
					t.Fatalf("expected CloseIssue for bd-gate, got %q", call.id)
				}
				if call.reason != "workflow 'release' succeeded" {
					t.Fatalf("expected CloseIssue reason to match status, got %q", call.reason)
				}
				if call.actor != "test-actor" {
					t.Fatalf("expected CloseIssue actor test-actor, got %q", call.actor)
				}
			}
		})
	}
}

// templateGateCheckStore answers a bead gate's target lookup, so a sweep that
// evaluates a bead gate shows up in gets.
type templateGateCheckStore struct {
	*fakeGateCheckStore
	gets []string
}

func (s *templateGateCheckStore) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	s.gets = append(s.gets, id)
	return &types.Issue{ID: id, Status: types.StatusOpen}, nil
}

// TestGateCheckSkipsTemplateGates sweeps a persisted proto's gates beside the
// open ones. The listing's query leaves templates out, but this fake answers
// every expression alike, so the rows reach filterCheckableGates and the test
// pins that backstop. The Lifecycle role refuses a write to a template, so
// evaluating one failed every sweep, after a gh:run gate had already asked
// GitHub for its run. A template gate is neither evaluated nor written, and an
// ordinary gate beside it still is.
func TestGateCheckSkipsTemplateGates(t *testing.T) {
	expired := time.Now().Add(-2 * time.Hour)
	templates := []*types.Issue{
		{ID: "bd-proto.gate-await-ci", IssueType: "gate", AwaitType: "gh:run", AwaitID: "release.yml", IsTemplate: true},
		{ID: "bd-proto.gate-await-bead", IssueType: "gate", AwaitType: "bead", AwaitID: "bd-target", IsTemplate: true},
		{ID: "bd-proto.gate-await-timer", IssueType: "gate", AwaitType: "timer", CreatedAt: expired, Timeout: time.Hour, IsTemplate: true},
	}
	live := &types.Issue{ID: "bd-timer", IssueType: "gate", AwaitType: "timer", CreatedAt: expired, Timeout: time.Hour}

	for _, tt := range []struct {
		name       string
		gates      []*types.Issue
		wantClosed []string
		wantOutput string
	}{
		{"only templates", templates, nil, "No open gates found."},
		{"templates beside a live gate", append(slices.Clone(templates), live), []string{"bd-timer"}, "Checked 1 gates: 1 resolved, 0 escalated, 0 errors"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			origStore := store
			origRootCtx := rootCtx
			origJSONOutput := jsonOutput
			origReadonlyMode := readonlyMode
			origActor := actor
			origDiscover := discoverRunIDByWorkflowNameFunc
			origUpdate := updateGateAwaitIDFunc
			origStatus := checkGHRunStatusFunc
			t.Cleanup(func() {
				store = origStore
				rootCtx = origRootCtx
				jsonOutput = origJSONOutput
				readonlyMode = origReadonlyMode
				actor = origActor
				discoverRunIDByWorkflowNameFunc = origDiscover
				updateGateAwaitIDFunc = origUpdate
				checkGHRunStatusFunc = origStatus
				resetGateCheckFlags(t)
			})
			resetGateCheckFlags(t)

			fake := &templateGateCheckStore{fakeGateCheckStore: &fakeGateCheckStore{issues: tt.gates}}
			store = fake
			rootCtx = context.Background()
			jsonOutput = false
			readonlyMode = false
			actor = "test-actor"

			var ghCalls []string
			discoverRunIDByWorkflowNameFunc = func(workflowHint string) (string, error) {
				ghCalls = append(ghCalls, "discover "+workflowHint)
				return "12345", nil
			}
			checkGHRunStatusFunc = func(runID string) (bool, bool, string, error) {
				ghCalls = append(ghCalls, "status "+runID)
				return false, false, "in progress", nil
			}
			updateGateAwaitIDFunc = func(_ interface{}, gateID, _ string) error {
				ghCalls = append(ghCalls, "legacy await_id write "+gateID)
				return nil
			}

			var runErr error
			output := captureGateStdout(t, func() { runErr = gateCheckCmd.RunE(gateCheckCmd, nil) })
			if runErr != nil {
				t.Fatalf("gateCheckCmd.RunE: %v\noutput:\n%s", runErr, output)
			}
			if !gateTestContains(output, tt.wantOutput) {
				t.Errorf("output %q does not contain %q", output, tt.wantOutput)
			}
			if len(ghCalls) != 0 {
				t.Errorf("the template gh:run gate was evaluated: %v", ghCalls)
			}
			if len(fake.gets) != 0 {
				t.Errorf("the template bead gate's target was looked up: %v", fake.gets)
			}
			var written []string
			for _, req := range fake.updates {
				written = append(written, req.IssueID)
			}
			if len(written) != 0 {
				t.Errorf("Lifecycle.Update wrote %v, want no write to a template gate", written)
			}
			var closed []string
			for _, call := range fake.closeCalls {
				closed = append(closed, call.id)
			}
			if !slices.Equal(closed, tt.wantClosed) {
				t.Errorf("closed gates = %v, want %v", closed, tt.wantClosed)
			}
		})
	}
}

func TestWorkflowNameMatches(t *testing.T) {
	tests := []struct {
		name         string
		hint         string
		workflowName string
		runName      string
		want         bool
	}{
		// Exact matches
		{"exact workflow name", "Release", "Release", "release.yml", true},
		{"exact run name", "release.yml", "Release", "release.yml", true},
		{"case insensitive workflow", "release", "Release", "release.yml", true},
		{"case insensitive run", "RELEASE.YML", "Release", "release.yml", true},

		// Hint with suffix, match display name without
		{"hint yml vs display name", "release.yml", "release", "ci.yml", true},
		{"hint yaml vs display name", "release.yaml", "release", "ci.yaml", true},

		// Hint without suffix, match filename with suffix
		{"hint base vs filename yml", "release", "CI", "release.yml", true},
		{"hint base vs filename yaml", "release", "CI", "release.yaml", true},

		// No match
		{"no match different name", "release", "CI", "ci.yml", false},
		{"no match partial", "rel", "Release", "release.yml", false},
		{"empty hint", "", "Release", "release.yml", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := workflowNameMatches(tt.hint, tt.workflowName, tt.runName)
			if got != tt.want {
				t.Errorf("workflowNameMatches(%q, %q, %q) = %v, want %v",
					tt.hint, tt.workflowName, tt.runName, got, tt.want)
			}
		})
	}
}

func TestCheckGHPR_StateHandling(t *testing.T) {
	tests := []struct {
		name           string
		ghJSON         string
		wantResolved   bool
		wantEscalated  bool
		reasonContains string
	}{
		{
			name:           "MERGED resolves gate",
			ghJSON:         `{"state":"MERGED","title":"Add feature X"}`,
			wantResolved:   true,
			wantEscalated:  false,
			reasonContains: "was merged",
		},
		{
			name:           "CLOSED escalates without merge",
			ghJSON:         `{"state":"CLOSED","title":"Stale PR"}`,
			wantResolved:   false,
			wantEscalated:  true,
			reasonContains: "closed without merging",
		},
		{
			name:           "OPEN leaves gate pending",
			ghJSON:         `{"state":"OPEN","title":"WIP"}`,
			wantResolved:   false,
			wantEscalated:  false,
			reasonContains: "still open",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate := &types.Issue{AwaitID: "https://github.com/org/repo/pull/1"}
			resolved, escalated, reason, err := checkGHPRWithRunner(gate, fakeGHRunner(t,
				tt.ghJSON,
				"pr", "view", gate.AwaitID, "--json", "state,title",
			))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resolved != tt.wantResolved {
				t.Errorf("resolved = %v, want %v", resolved, tt.wantResolved)
			}
			if escalated != tt.wantEscalated {
				t.Errorf("escalated = %v, want %v", escalated, tt.wantEscalated)
			}
			if !gateTestContainsIgnoreCase(reason, tt.reasonContains) {
				t.Errorf("reason %q does not contain %q", reason, tt.reasonContains)
			}
		})
	}
}

func TestCheckGHPR_NoMergedFieldRequested(t *testing.T) {
	gate := &types.Issue{AwaitID: "https://github.com/org/repo/pull/99"}
	resolved, _, reason, err := checkGHPRWithRunner(gate, fakeGHRunner(t,
		`{"state":"MERGED","title":"Test PR"}`,
		"pr", "view", gate.AwaitID, "--json", "state,title",
	))
	if err != nil {
		t.Fatalf("checkGHPR failed (likely requested 'merged' field): %v", err)
	}
	if !resolved {
		t.Errorf("expected resolved=true for MERGED state")
	}
	if !gateTestContainsIgnoreCase(reason, "was merged") {
		t.Errorf("reason %q should contain 'was merged'", reason)
	}
}

func fakeGHRunner(t *testing.T, stdout string, wantArgs ...string) ghCommandRunner {
	t.Helper()
	return func(args ...string) ([]byte, []byte, error) {
		t.Helper()
		if !slices.Equal(args, wantArgs) {
			t.Fatalf("gh arguments = %q, want %q", args, wantArgs)
		}
		return []byte(stdout), nil, nil
	}
}

// gateTestContainsIgnoreCase checks if haystack contains needle (case-insensitive)
func gateTestContainsIgnoreCase(haystack, needle string) bool {
	return gateTestContains(gateTestLowerCase(haystack), gateTestLowerCase(needle))
}

func gateTestContains(s, substr string) bool {
	return len(s) >= len(substr) && gateTestFindSubstring(s, substr) >= 0
}

func gateTestLowerCase(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 32
		}
	}
	return string(b)
}

func gateTestFindSubstring(s, substr string) int {
	if len(substr) == 0 {
		return 0
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// TestFilterIssueGates covers the bead-scoping helper behind `bd gate list <issue-id>`:
// only gate-type dependencies are returned, --all controls closed visibility, and the
// limit is honored. Regression guard for the bug where `bd gate list <bead>` silently
// ignored the argument and returned the DB-wide gate list.
func TestFilterIssueGates(t *testing.T) {
	gate := types.IssueType("gate")
	task := types.IssueType("task")
	deps := []*types.Issue{
		{ID: "g-open", IssueType: gate, Status: types.StatusOpen},
		{ID: "g-closed", IssueType: gate, Status: types.StatusClosed},
		{ID: "t-blocker", IssueType: task, Status: types.StatusOpen}, // not a gate
		nil, // defensive: skipped
		{ID: "g-open2", IssueType: gate, Status: types.StatusOpen},
	}

	t.Run("open_only_excludes_closed_and_nongates", func(t *testing.T) {
		got := filterIssueGates(deps, false, 0)
		ids := gateIDs(got)
		if len(got) != 2 || ids[0] != "g-open" || ids[1] != "g-open2" {
			t.Fatalf("expected [g-open g-open2], got %v", ids)
		}
	})

	t.Run("all_includes_closed_gates_only", func(t *testing.T) {
		got := filterIssueGates(deps, true, 0)
		ids := gateIDs(got)
		if len(got) != 3 {
			t.Fatalf("expected 3 gates (incl. closed), got %v", ids)
		}
		for _, id := range ids {
			if id == "t-blocker" {
				t.Fatalf("non-gate dependency leaked into result: %v", ids)
			}
		}
	})

	t.Run("limit_caps_results", func(t *testing.T) {
		got := filterIssueGates(deps, true, 1)
		if len(got) != 1 || got[0].ID != "g-open" {
			t.Fatalf("expected limit=1 -> [g-open], got %v", gateIDs(got))
		}
	})

	t.Run("empty_deps", func(t *testing.T) {
		if got := filterIssueGates(nil, true, 0); len(got) != 0 {
			t.Fatalf("expected no gates, got %v", gateIDs(got))
		}
	})
}

func gateIDs(gs []*types.Issue) []string {
	ids := make([]string, 0, len(gs))
	for _, g := range gs {
		ids = append(ids, g.ID)
	}
	return ids
}

func TestGateMetadataForCreateExplicitRepo(t *testing.T) {
	inherited := &types.Issue{ID: "bd-target", Metadata: json.RawMessage(`{"repo":"acme/inherited"}`)}
	decodeRepo := func(t *testing.T, metadata json.RawMessage) string {
		t.Helper()
		var decoded struct {
			Repo string `json:"repo"`
		}
		if err := json.Unmarshal(metadata, &decoded); err != nil {
			t.Fatalf("metadata %s is not valid JSON: %v", metadata, err)
		}
		return decoded.Repo
	}

	t.Run("explicit_repo_wins_over_inherited", func(t *testing.T) {
		metadata, err := gateMetadataForCreate(gateCreateInput{gateType: "gh:pr", repo: "gastownhall/beads"}, inherited)
		if err != nil {
			t.Fatalf("gateMetadataForCreate returned error: %v", err)
		}
		if got := decodeRepo(t, metadata); got != "gastownhall/beads" {
			t.Fatalf("repo = %q, want gastownhall/beads (the flag, not the blocked issue's value)", got)
		}
	})

	t.Run("no_flag_inherits", func(t *testing.T) {
		metadata, err := gateMetadataForCreate(gateCreateInput{gateType: "gh:run"}, inherited)
		if err != nil {
			t.Fatalf("gateMetadataForCreate returned error: %v", err)
		}
		if got := decodeRepo(t, metadata); got != "acme/inherited" {
			t.Fatalf("repo = %q, want the inherited acme/inherited", got)
		}
	})

	t.Run("no_flag_no_metadata_is_nil", func(t *testing.T) {
		metadata, err := gateMetadataForCreate(gateCreateInput{gateType: "gh:pr"}, &types.Issue{ID: "bd-plain"})
		if err != nil {
			t.Fatalf("gateMetadataForCreate returned error: %v", err)
		}
		if metadata != nil {
			t.Fatalf("metadata = %s, want nil (current repository)", metadata)
		}
	})

	t.Run("inherited_error_names_the_blocked_issue", func(t *testing.T) {
		_, err := gateMetadataForCreate(gateCreateInput{gateType: "gh:pr"}, &types.Issue{ID: "bd-bad", Metadata: json.RawMessage(`{"repo":"owner/repo;echo"}`)})
		if err == nil {
			t.Fatal("malformed inherited metadata.repo must fail closed")
		}
		if !strings.Contains(err.Error(), "invalid GitHub repository metadata on bd-bad") {
			t.Fatalf("error %q does not name the blocked issue's metadata", err)
		}
	})

	t.Run("invalid_explicit_repo_is_rejected", func(t *testing.T) {
		for _, bad := range []string{"not-owner-slash-repo", "owner/repo;echo", "owner//repo", "https://github.com/owner/repo"} {
			_, err := gateMetadataForCreate(gateCreateInput{gateType: "gh:pr", repo: bad}, inherited)
			if err == nil {
				t.Errorf("--repo %q accepted, want validation error", bad)
				continue
			}
			if !strings.Contains(err.Error(), "--repo") {
				t.Errorf("error for --repo %q does not name the flag: %v", bad, err)
			}
		}
	})

	t.Run("host_form_is_accepted", func(t *testing.T) {
		metadata, err := gateMetadataForCreate(gateCreateInput{gateType: "gh:run", repo: "ghe.example.com/acme/widgets"}, nil)
		if err != nil {
			t.Fatalf("HOST/OWNER/REPO rejected: %v", err)
		}
		if got := decodeRepo(t, metadata); got != "ghe.example.com/acme/widgets" {
			t.Fatalf("repo = %q", got)
		}
	})

	t.Run("non_github_type_refuses_the_flag", func(t *testing.T) {
		for _, gateType := range []string{"human", "timer", "bead"} {
			_, err := gateMetadataForCreate(gateCreateInput{gateType: gateType, repo: "acme/widgets"}, inherited)
			if err == nil {
				t.Errorf("--repo on a %s gate accepted, want refusal (nothing would read it)", gateType)
				continue
			}
			if !strings.Contains(err.Error(), "--repo applies only to GitHub or GitLab gates") {
				t.Errorf("refusal for %s gate has the wrong text: %v", gateType, err)
			}
		}
	})
}

func TestCheckGHPRNotFoundNamesTheRepository(t *testing.T) {
	notFound := func(t *testing.T, wantArgs ...string) ghCommandRunner {
		t.Helper()
		return func(args ...string) ([]byte, []byte, error) {
			if strings.Join(args, " ") != strings.Join(wantArgs, " ") {
				t.Fatalf("gh args = %q, want %q", args, wantArgs)
			}
			return nil, []byte("GraphQL: Could not resolve to a PullRequest with the number of 7173. (repository.pullRequest)"), fmt.Errorf("exit status 1")
		}
	}

	t.Run("cross_repo", func(t *testing.T) {
		resolved, escalated, reason, err := checkGHPRWithRunner(&types.Issue{
			IssueType: "gate", AwaitType: "gh:pr", AwaitID: "7173",
			Metadata: json.RawMessage(`{"repo":"gastownhall/beads"}`),
		}, notFound(t, "pr", "view", "7173", "--json", "state,title", "--repo", "gastownhall/beads"))
		if err != nil {
			t.Fatalf("checkGHPR returned error: %v", err)
		}
		if resolved || !escalated {
			t.Fatalf("resolved, escalated = %v, %v; want false, true", resolved, escalated)
		}
		if reason != "pull request not found: #7173 in gastownhall/beads" {
			t.Fatalf("reason = %q; it must name the repository the number was resolved against", reason)
		}
	})

	t.Run("current_repo", func(t *testing.T) {
		_, escalated, reason, err := checkGHPRWithRunner(&types.Issue{
			IssueType: "gate", AwaitType: "gh:pr", AwaitID: "7173",
		}, notFound(t, "pr", "view", "7173", "--json", "state,title"))
		if err != nil || !escalated {
			t.Fatalf("escalated, err = %v, %v; want true, nil", escalated, err)
		}
		if reason != "pull request not found: #7173 in the current repository" {
			t.Fatalf("reason = %q", reason)
		}
	})
}

func TestCheckGHRunNotFoundNamesTheRepository(t *testing.T) {
	notFound := func(t *testing.T, wantArgs ...string) ghCommandRunner {
		t.Helper()
		return func(args ...string) ([]byte, []byte, error) {
			if !slices.Equal(args, wantArgs) {
				t.Fatalf("gh args = %q, want %q", args, wantArgs)
			}
			// Stub stderr that reaches the escalation arm; real gh 404
			// output does not (see real_gh_404_names_the_repository).
			return nil, []byte("run 12345 not found"), fmt.Errorf("exit status 1")
		}
	}

	t.Run("cross_repo", func(t *testing.T) {
		resolved, escalated, reason, err := checkGHRunWithRunner(&types.Issue{
			ID: "gt-run", IssueType: "gate", AwaitType: "gh:run", AwaitID: "12345",
			Metadata: json.RawMessage(`{"repo":"gastownhall/beads"}`),
		}, nil, notFound(t, "run", "view", "12345", "--json", "status,conclusion,name", "--repo", "gastownhall/beads"))
		if err != nil {
			t.Fatalf("checkGHRun returned error: %v", err)
		}
		if resolved || !escalated {
			t.Fatalf("resolved, escalated = %v, %v; want false, true", resolved, escalated)
		}
		if reason != "workflow run not found: 12345 in gastownhall/beads" {
			t.Fatalf("reason = %q; it must name the repository the run ID was looked up in", reason)
		}
	})

	t.Run("current_repo", func(t *testing.T) {
		_, escalated, reason, err := checkGHRunStatusInRepoWithRunner("12345", "",
			notFound(t, "run", "view", "12345", "--json", "status,conclusion,name"))
		if err != nil || !escalated {
			t.Fatalf("escalated, err = %v, %v; want true, nil", escalated, err)
		}
		if reason != "workflow run not found: 12345 in the current repository" {
			t.Fatalf("reason = %q", reason)
		}
	})

	t.Run("real_gh_404_names_the_repository", func(t *testing.T) {
		// gh run view's real stderr for a run the repository does not have.
		// A token without access to the repository gets the same 404, so it
		// stays an error rather than an escalation; its URL names the repo.
		stderr := "failed to get run: HTTP 404: Not Found (https://api.github.com/repos/gastownhall/beads/actions/runs/12345?exclude_pull_requests=true)\n"
		resolved, escalated, _, err := checkGHRunStatusInRepoWithRunner("12345", "gastownhall/beads",
			func(args ...string) ([]byte, []byte, error) {
				return nil, []byte(stderr), fmt.Errorf("exit status 1")
			})
		if err == nil || resolved || escalated {
			t.Fatalf("resolved, escalated, err = %v, %v, %v; want false, false, an error", resolved, escalated, err)
		}
		if !strings.Contains(err.Error(), "/repos/gastownhall/beads/") {
			t.Fatalf("err = %q; it must name the repository the run ID was looked up in", err)
		}
	})
}

// TestGateCheckWritesGoThroughTheLifecycleRole pins gate check's two non-close
// writes to the Lifecycle role: the bead-gate sighting is a single-key metadata
// set and the gh:run discovery is an await_id patch, which a connected HTTP
// workspace sends as issues.update (the sighting) or refuses before the dial
// (await_id). The legacy map-based UpdateIssue is not on the HTTP store, so a
// sighting written through it never reached the server.
func TestGateCheckWritesGoThroughTheLifecycleRole(t *testing.T) {
	origStore, origActor := store, actor
	t.Cleanup(func() { store, actor = origStore, origActor })
	fake := &fakeGateCheckStore{}
	store, actor = fake, "test-actor"

	if err := updateGateThroughLifecycle(context.Background(), "bd-gate", beadGateSeenPatch("rig:bd-x")); err != nil {
		t.Fatalf("sighting: %v", err)
	}
	if err := persistGateCheckAwaitID(context.Background(), "bd-run", "4242"); err != nil {
		t.Fatalf("await_id: %v", err)
	}
	if len(fake.updates) != 2 {
		t.Fatalf("Lifecycle.Update calls = %d, want 2", len(fake.updates))
	}
	seen := fake.updates[0]
	wantSeen := issueops.IssuePatch{Metadata: issueops.MetadataPatch{Set: map[string]json.RawMessage{beadGateSeenKey: json.RawMessage(`"rig:bd-x"`)}}}
	if seen.IssueID != "bd-gate" || seen.Actor != "test-actor" || !reflect.DeepEqual(seen.Patch, wantSeen) {
		t.Fatalf("sighting update = %+v, want a %s metadata set on bd-gate", seen, beadGateSeenKey)
	}
	run := fake.updates[1]
	wantRun := issueops.IssuePatch{AwaitID: issueops.Field[string]{Set: true, Value: "4242"}}
	if run.IssueID != "bd-run" || !reflect.DeepEqual(run.Patch, wantRun) {
		t.Fatalf("await_id update = %+v, want AwaitID=4242 on bd-run", run)
	}
}

// gateCheckWireRefusal is what a connected HTTP workspace's Lifecycle role
// returns for a patch member the v0 wire has no member for, before any
// request: the encoder's divergence-ledger row joined to the store's
// unsupported sentinel, as (*httpclient.Store).inexpressible builds it.
func gateCheckWireRefusal(row string) error {
	return &httpclient.InexpressibleError{
		Unsup: &httpclient.ErrHTTPUnsupported{
			Unsup:     &storage.ErrUnsupported{Op: "Lifecycle.Update", Backend: "http"},
			ServerURL: "http://127.0.0.1:9",
		},
		Refused: &encode.RefusedError{Op: encode.OpUpdateIssue, Row: encode.RowByID(row)},
	}
}

// TestGateCheckAwaitIDRefusalNamesTheRunAndTheServer: the HTTP wire refuses an
// await_id edit by its ledger row, and that refusal alone is reworded, so the
// gate's row names the run, await_id and the server instead of the role
// method and the wire operation. The typed chain survives the reword.
func TestGateCheckAwaitIDRefusalNamesTheRunAndTheServer(t *testing.T) {
	origStore, origActor := store, actor
	t.Cleanup(func() { store, actor = origStore, origActor })
	refusal := gateCheckWireRefusal(awaitIDPatchRefusal)
	store, actor = &fakeGateCheckStore{updateErr: refusal}, "test-actor"

	err := persistGateCheckAwaitID(context.Background(), "bd-run", "4242")
	want := "run 4242 cannot be recorded as await_id: bd serve at http://127.0.0.1:9 does not accept an await_id update; check the gate in a local workspace"
	if err == nil || err.Error() != want {
		t.Fatalf("await_id refusal = %v, want %q", err, want)
	}
	if !errors.Is(err, refusal) || !errors.Is(err, encode.ErrRefused) {
		t.Errorf("reworded refusal %v no longer wraps the role's refusal", err)
	}
	var unsupported *storage.ErrUnsupported
	if !errors.As(err, &unsupported) || unsupported.Op != "Lifecycle.Update" || unsupported.Backend != "http" {
		t.Errorf("reworded refusal %v does not reach *storage.ErrUnsupported", err)
	}
	var refused *encode.RefusedError
	if !errors.As(err, &refused) || refused.Row.ID != awaitIDPatchRefusal {
		t.Errorf("reworded refusal %v does not reach the %s ledger row", err, awaitIDPatchRefusal)
	}
}

// TestGateCheckAwaitIDPassesOtherRefusalsThrough: every other error from the
// await_id write is returned exactly as the role raised it. Another patch
// member's refusal, or a server that does not advertise the capability the
// member needs, says something the await_id sentence would hide.
func TestGateCheckAwaitIDPassesOtherRefusalsThrough(t *testing.T) {
	origStore, origActor := store, actor
	t.Cleanup(func() { store, actor = origStore, origActor })
	actor = "test-actor"

	for _, tt := range []struct {
		name string
		err  error
	}{
		{"another patch member's refusal", gateCheckWireRefusal("W-IssuePatch.SpecID")},
		{"a capability the server does not advertise", &httpclient.ErrHTTPUnsupported{
			Unsup:     &storage.ErrUnsupported{Op: "Lifecycle.Update", Backend: "http", Capability: "issues.update.gates"},
			ServerURL: "http://127.0.0.1:9",
		}},
		{"a local backend's refusal", &storage.ErrUnsupported{Op: "Lifecycle.Update", Backend: "dolt"}},
		{"a failed write", errors.New("connection reset")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store = &fakeGateCheckStore{updateErr: tt.err}
			if err := persistGateCheckAwaitID(context.Background(), "bd-run", "4242"); err != tt.err {
				t.Fatalf("persistGateCheckAwaitID = %v, want the role's own error %v", err, tt.err)
			}
		})
	}
}

// fakeGateRoles is the Reader and Lifecycle `bd gate create` and
// `bd gate add-waiter` run over. Its Update honors ExpectedVersion the way
// every backend does, and can simulate concurrent adders: each of the first
// `concurrent` updates finds that another writer appended its waiter (and
// reminted the row version) after the command's read.
type fakeGateRoles struct {
	issueops.Lifecycle
	issues     map[string]*types.Issue
	creates    []issueops.CreateRequest
	updates    []issueops.UpdateRequest
	createErr  error
	updateErr  error
	concurrent []string
}

func newFakeGateRoles(issues ...*types.Issue) *fakeGateRoles {
	f := &fakeGateRoles{issues: map[string]*types.Issue{}}
	for _, issue := range issues {
		f.issues[issue.ID] = issue
	}
	return f
}

func (f *fakeGateRoles) Ready(context.Context, issueops.ReadyRequest) (issueops.IssuePage, error) {
	panic("unused")
}

func (f *fakeGateRoles) List(context.Context, issueops.ListRequest) (issueops.IssuePage, error) {
	panic("unused")
}

func (f *fakeGateRoles) Get(_ context.Context, req issueops.GetRequest) (*issueops.IssueDetails, error) {
	issue, ok := f.issues[req.ID]
	if !ok {
		return nil, storage.ErrNotFound
	}
	copied := *issue
	copied.Waiters = slices.Clone(issue.Waiters)
	return &issueops.IssueDetails{Issue: copied}, nil
}

func (f *fakeGateRoles) Create(_ context.Context, req issueops.CreateRequest) (issueops.CreateResult, error) {
	f.creates = append(f.creates, req)
	if f.createErr != nil {
		return issueops.CreateResult{}, f.createErr
	}
	created := *req.Issue
	created.ID = "bd-gate1"
	created.Dependencies = []*types.Dependency{{IssueID: req.Dependencies[0].TargetID, DependsOnID: created.ID, Type: types.DepBlocks}}
	f.issues[created.ID] = &created
	return issueops.CreateResult{Issue: &created}, nil
}

func (f *fakeGateRoles) Update(_ context.Context, req issueops.UpdateRequest) (issueops.UpdateResult, error) {
	f.updates = append(f.updates, req)
	if f.updateErr != nil {
		return issueops.UpdateResult{}, f.updateErr
	}
	issue := f.issues[req.IssueID]
	if len(f.concurrent) > 0 {
		issue.Waiters = append(issue.Waiters, f.concurrent[0])
		issue.RowVersion++
		f.concurrent = f.concurrent[1:]
	}
	if req.ExpectedVersion != nil && *req.ExpectedVersion != issue.RowVersion {
		return issueops.UpdateResult{}, issueops.ErrVersionMismatch
	}
	if req.Patch.Waiters.Set {
		issue.Waiters = slices.Clone(req.Patch.Waiters.Value)
	}
	issue.RowVersion++
	return issueops.UpdateResult{Issue: issue}, nil
}

func setGateTestActor(t *testing.T) {
	t.Helper()
	origActor := actor
	t.Cleanup(func() { actor = origActor })
	actor = "test-actor"
}

// TestCreateGateIsOneCreateWithAReverseBlocksEdge: `bd gate create` is ONE
// Lifecycle.Create carrying the gate members and the blocks edge from the
// blocked issue to the new gate, so the gate and its edge land atomically on
// every route (a connected HTTP workspace sends one createIssue).
func TestCreateGateIsOneCreateWithAReverseBlocksEdge(t *testing.T) {
	setGateTestActor(t)
	roles := newFakeGateRoles(&types.Issue{ID: "bd-target", Title: "blocked", IssueType: types.TypeTask})

	in := gateCreateInput{blocksID: "bd-target", gateType: "timer", timeout: 90 * time.Minute, reason: "cool down"}
	gate, target, err := createGateThroughRoles(context.Background(), roles, roles, in)
	if err != nil {
		t.Fatalf("createGateThroughRoles: %v", err)
	}
	if len(roles.creates) != 1 || len(roles.updates) != 0 {
		t.Fatalf("createGateThroughRoles made %d creates and %d updates, want exactly one create", len(roles.creates), len(roles.updates))
	}
	req := roles.creates[0]
	wantDeps := []issueops.CreateDependency{{TargetID: "bd-target", Type: types.DepBlocks, Reverse: true}}
	if !reflect.DeepEqual(req.Dependencies, wantDeps) {
		t.Fatalf("create dependencies = %+v, want %+v", req.Dependencies, wantDeps)
	}
	if req.Actor != "test-actor" {
		t.Errorf("create actor = %q, want test-actor", req.Actor)
	}
	if req.Issue.IssueType != "gate" || req.Issue.AwaitType != "timer" || req.Issue.Timeout != 90*time.Minute {
		t.Errorf("created gate = type %q await_type %q timeout %s, want a 90m timer gate", req.Issue.IssueType, req.Issue.AwaitType, req.Issue.Timeout)
	}
	if !strings.Contains(req.Issue.Description, "cool down") {
		t.Errorf("gate description %q does not carry the reason", req.Issue.Description)
	}
	if gate.ID != "bd-gate1" || target.ID != "bd-target" {
		t.Errorf("createGateThroughRoles returned gate %q target %q", gate.ID, target.ID)
	}
	if gate.Dependencies != nil {
		t.Errorf("createGateThroughRoles returned the gate with dependency records %v; bd gate create never printed them", gate.Dependencies)
	}
}

// TestCreateGateRefusalWritesNothing: a refused create (a server that does not
// advertise issues.create.gates) surfaces the refusal naming the token, and a
// missing blocked issue never reaches the create.
func TestCreateGateRefusalWritesNothing(t *testing.T) {
	setGateTestActor(t)

	t.Run("token refused", func(t *testing.T) {
		roles := newFakeGateRoles(&types.Issue{ID: "bd-target", IssueType: types.TypeTask})
		roles.createErr = &storage.ErrUnsupported{Op: "createIssue", Backend: "http", Capability: "issues.create.gates"}
		_, _, err := createGateThroughRoles(context.Background(), roles, roles, gateCreateInput{blocksID: "bd-target", gateType: "timer"})
		var unsupported *storage.ErrUnsupported
		if !errors.As(err, &unsupported) || !strings.Contains(err.Error(), "issues.create.gates") {
			t.Fatalf("createGateThroughRoles error = %v, want the refusal naming issues.create.gates", err)
		}
		if _, ok := roles.issues["bd-gate1"]; ok || len(roles.updates) != 0 {
			t.Fatalf("a refused create wrote something")
		}
	})

	t.Run("missing target", func(t *testing.T) {
		roles := newFakeGateRoles()
		_, _, err := createGateThroughRoles(context.Background(), roles, roles, gateCreateInput{blocksID: "bd-absent", gateType: "human"})
		if err == nil || err.Error() != "issue not found: bd-absent" {
			t.Fatalf("createGateThroughRoles error = %v, want issue not found: bd-absent", err)
		}
		if len(roles.creates) != 0 {
			t.Fatalf("createGateThroughRoles created a gate for a missing target")
		}
	})
}

// TestAddGateWaiterIsAGuardedWholeListUpdate: the waiter list is written whole,
// guarded by the version the read returned. A concurrent adder makes the write
// miss; the retry re-reads (keeping the other waiter) and re-deduplicates.
func TestAddGateWaiterIsAGuardedWholeListUpdate(t *testing.T) {
	setGateTestActor(t)
	origSleep := gateAddWaiterSleep
	t.Cleanup(func() { gateAddWaiterSleep = origSleep })
	var sleeps []time.Duration
	gateAddWaiterSleep = func(_ context.Context, d time.Duration) error {
		sleeps = append(sleeps, d)
		return nil
	}
	gate := func() *types.Issue {
		return &types.Issue{ID: "bd-g", IssueType: "gate", Waiters: []string{"a"}, RowVersion: 7}
	}

	t.Run("first write", func(t *testing.T) {
		roles := newFakeGateRoles(gate())
		added, err := addGateWaiter(context.Background(), roles, roles, "bd-g", "b")
		if err != nil || !added {
			t.Fatalf("addGateWaiter = %v, %v; want added", added, err)
		}
		if len(roles.updates) != 1 {
			t.Fatalf("updates = %d, want 1", len(roles.updates))
		}
		req := roles.updates[0]
		if req.ExpectedVersion == nil || *req.ExpectedVersion != 7 {
			t.Errorf("update not guarded by the read's version 7: %v", req.ExpectedVersion)
		}
		want := issueops.IssuePatch{Waiters: issueops.Field[[]string]{Set: true, Value: []string{"a", "b"}}}
		if !reflect.DeepEqual(req.Patch, want) {
			t.Errorf("patch = %+v, want only the whole waiter list [a b]", req.Patch)
		}
	})

	t.Run("concurrent adder retries", func(t *testing.T) {
		roles := newFakeGateRoles(gate())
		roles.concurrent = []string{"x"}
		added, err := addGateWaiter(context.Background(), roles, roles, "bd-g", "b")
		if err != nil || !added {
			t.Fatalf("addGateWaiter = %v, %v; want added after one retry", added, err)
		}
		if len(roles.updates) != 2 {
			t.Fatalf("updates = %d, want the missed write and its retry", len(roles.updates))
		}
		if got := roles.issues["bd-g"].Waiters; !reflect.DeepEqual(got, []string{"a", "x", "b"}) {
			t.Errorf("waiters = %v, want [a x b]: the concurrent waiter must survive", got)
		}
	})

	t.Run("concurrent adder of the same waiter", func(t *testing.T) {
		roles := newFakeGateRoles(gate())
		roles.concurrent = []string{"b"}
		added, err := addGateWaiter(context.Background(), roles, roles, "bd-g", "b")
		if err != nil || added {
			t.Fatalf("addGateWaiter = %v, %v; want already registered after the re-read", added, err)
		}
		if got := roles.issues["bd-g"].Waiters; !reflect.DeepEqual(got, []string{"a", "b"}) {
			t.Errorf("waiters = %v, want [a b]", got)
		}
	})

	t.Run("already registered writes nothing", func(t *testing.T) {
		roles := newFakeGateRoles(gate())
		added, err := addGateWaiter(context.Background(), roles, roles, "bd-g", "a")
		if err != nil || added || len(roles.updates) != 0 {
			t.Fatalf("addGateWaiter = %v, %v with %d updates; want a no-op", added, err, len(roles.updates))
		}
	})

	t.Run("backs off between attempts", func(t *testing.T) {
		sleeps = nil
		roles := newFakeGateRoles(gate())
		roles.concurrent = []string{"x1", "x2", "x3", "x4", "x5", "x6", "x7", "x8", "x9"}
		added, err := addGateWaiter(context.Background(), roles, roles, "bd-g", "b")
		if err != nil || !added {
			t.Fatalf("addGateWaiter = %v, %v; want added once the nine concurrent writers are done", added, err)
		}
		if len(roles.updates) != 10 || len(sleeps) != 9 {
			t.Fatalf("updates = %d, sleeps = %d; want 10 writes with a backoff before each retry", len(roles.updates), len(sleeps))
		}
		want := gateAddWaiterBackoffBase
		for i, d := range sleeps {
			if d < want/2 || d >= want {
				t.Errorf("backoff %d = %s, want in [%s, %s)", i+1, d, want/2, want)
			}
			if want*2 <= gateAddWaiterBackoffCap {
				want *= 2
			} else {
				want = gateAddWaiterBackoffCap
			}
		}
	})

	t.Run("gives up after the attempt bound", func(t *testing.T) {
		sleeps = nil
		roles := newFakeGateRoles(gate())
		for i := 0; i <= gateAddWaiterMaxAttempts; i++ {
			roles.concurrent = append(roles.concurrent, fmt.Sprintf("x%d", i))
		}
		_, err := addGateWaiter(context.Background(), roles, roles, "bd-g", "b")
		if !errors.Is(err, issueops.ErrVersionMismatch) {
			t.Fatalf("addGateWaiter error = %v, want the version mismatch", err)
		}
		if len(roles.updates) != gateAddWaiterMaxAttempts {
			t.Fatalf("updates = %d, want %d attempts", len(roles.updates), gateAddWaiterMaxAttempts)
		}
	})

	t.Run("a cancelled wait stops the loop", func(t *testing.T) {
		roles := newFakeGateRoles(gate())
		roles.concurrent = []string{"x1", "x2"}
		gateAddWaiterSleep = func(context.Context, time.Duration) error { return context.Canceled }
		t.Cleanup(func() {
			gateAddWaiterSleep = func(_ context.Context, d time.Duration) error {
				sleeps = append(sleeps, d)
				return nil
			}
		})
		_, err := addGateWaiter(context.Background(), roles, roles, "bd-g", "b")
		if !errors.Is(err, context.Canceled) || len(roles.updates) != 1 {
			t.Fatalf("addGateWaiter = %v after %d updates, want the cancellation after one write", err, len(roles.updates))
		}
	})

	t.Run("refusal is not retried", func(t *testing.T) {
		roles := newFakeGateRoles(gate())
		roles.updateErr = &storage.ErrUnsupported{Op: "updateIssue", Backend: "http", Capability: "issues.update.gates"}
		_, err := addGateWaiter(context.Background(), roles, roles, "bd-g", "b")
		if err == nil || !strings.Contains(err.Error(), "issues.update.gates") || len(roles.updates) != 1 {
			t.Fatalf("addGateWaiter error = %v after %d updates, want one refused update naming issues.update.gates", err, len(roles.updates))
		}
	})

	t.Run("not a gate", func(t *testing.T) {
		roles := newFakeGateRoles(&types.Issue{ID: "bd-t", IssueType: types.TypeTask})
		_, err := addGateWaiter(context.Background(), roles, roles, "bd-t", "b")
		if err == nil || !strings.Contains(err.Error(), "is not a gate issue") {
			t.Fatalf("addGateWaiter error = %v, want the not-a-gate refusal", err)
		}
	})

	t.Run("missing gate", func(t *testing.T) {
		roles := newFakeGateRoles()
		_, err := addGateWaiter(context.Background(), roles, roles, "bd-none", "b")
		if err == nil || err.Error() != "gate not found: bd-none" {
			t.Fatalf("addGateWaiter error = %v, want gate not found: bd-none", err)
		}
	})
}

// TestFindPendingGatesListsThroughListGates: gate discover lists the open gates
// through issueops.ListGates (queryIssues over HTTP), never the raw storage
// filter, and skips templates and gates that already hold a run ID.
func TestFindPendingGatesListsThroughListGates(t *testing.T) {
	fake := &fakeGateCheckStore{issues: []*types.Issue{
		{ID: "bd-hint", IssueType: "gate", AwaitType: "gh:run", AwaitID: "release.yml"},
		{ID: "bd-empty", IssueType: "gate", AwaitType: "gh:run"},
		{ID: "bd-numeric", IssueType: "gate", AwaitType: "gh:run", AwaitID: "12345"},
		{ID: "bd-template", IssueType: "gate", AwaitType: "gh:run", IsTemplate: true},
		{ID: "bd-timer", IssueType: "gate", AwaitType: "timer"},
	}}
	gates, err := findPendingGatesWith(context.Background(), fake)
	if err != nil {
		t.Fatalf("findPendingGatesWith: %v", err)
	}
	var ids []string
	for _, g := range gates {
		ids = append(ids, g.ID)
	}
	if !reflect.DeepEqual(ids, []string{"bd-hint", "bd-empty"}) {
		t.Fatalf("pending gates = %v, want [bd-hint bd-empty]", ids)
	}
	if fake.searchCalled {
		t.Fatal("gate discover used the raw SearchIssues filter")
	}
	if len(fake.queries) != 1 || fake.queries[0].Expression != issueops.GateListExpression {
		t.Fatalf("queries = %+v, want one ListGates query", fake.queries)
	}
	if fake.queries[0].IncludeClosed {
		t.Fatal("gate discover listed closed gates; only open gates await a run")
	}
}

// TestUpdateGateAwaitIDGoesThroughTheLifecycleRole: gate discover's and
// bd close's await_id persist is the Lifecycle patch gate check makes, never
// the legacy map-based UpdateIssue the HTTP store does not serve.
func TestUpdateGateAwaitIDGoesThroughTheLifecycleRole(t *testing.T) {
	origStore := store
	t.Cleanup(func() { store = origStore })
	setGateTestActor(t)
	fake := &fakeGateCheckStore{}
	store = fake

	if err := updateGateAwaitID(nil, "bd-run", "4242"); err != nil {
		t.Fatalf("updateGateAwaitID: %v", err)
	}
	want := issueops.IssuePatch{AwaitID: issueops.Field[string]{Set: true, Value: "4242"}}
	if len(fake.updates) != 1 || fake.updates[0].IssueID != "bd-run" || !reflect.DeepEqual(fake.updates[0].Patch, want) {
		t.Fatalf("updates = %+v, want one AwaitID=4242 patch on bd-run", fake.updates)
	}
}
