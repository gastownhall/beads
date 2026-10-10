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

	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
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
	searchFilter types.IssueFilter
	closeCalls   []gateCloseCall
}

func (f *fakeGateCheckStore) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) ([]*types.Issue, error) {
	f.searchFilter = filter
	return f.issues, nil
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

			updateCalls := 0
			discoverRunIDByWorkflowNameFunc = func(workflowHint string) (string, error) {
				if workflowHint != "release.yml" {
					t.Fatalf("unexpected workflow hint %q", workflowHint)
				}
				return "12345", nil
			}
			updateGateAwaitIDFunc = func(_ interface{}, gateID, runID string) error {
				updateCalls++
				if gateID != "bd-gate" {
					t.Fatalf("expected gate ID bd-gate, got %q", gateID)
				}
				if runID != "12345" {
					t.Fatalf("expected discovered run ID 12345, got %q", runID)
				}
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

			if updateCalls != tt.wantUpdateCalls {
				t.Fatalf("updateGateAwaitIDFunc call count = %d, want %d", updateCalls, tt.wantUpdateCalls)
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
			if fakeStore.searchFilter.IssueType == nil || *fakeStore.searchFilter.IssueType != "gate" {
				t.Fatalf("expected gate filter, got %+v", fakeStore.searchFilter)
			}
			if len(fakeStore.searchFilter.ExcludeStatus) != 1 || fakeStore.searchFilter.ExcludeStatus[0] != types.StatusClosed {
				t.Fatalf("expected closed-status exclusion, got %+v", fakeStore.searchFilter.ExcludeStatus)
			}
			if fakeStore.searchFilter.Limit != 100 {
				t.Fatalf("expected limit 100, got %d", fakeStore.searchFilter.Limit)
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
		// Sorted by ID first ("g-closed" < "g-open" < "g-open2"), then capped.
		got := filterIssueGates(deps, true, 1)
		if len(got) != 1 || got[0].ID != "g-closed" {
			t.Fatalf("expected limit=1 -> [g-closed], got %v", gateIDs(got))
		}
	})

	t.Run("sorted_by_id_regardless_of_input_order", func(t *testing.T) {
		// Same dependency set, presented in a different (map-iteration-like)
		// order, must still come back sorted by ID.
		shuffled := []*types.Issue{deps[4], deps[1], deps[0]} // g-open2, g-closed, g-open
		got := filterIssueGates(shuffled, true, 0)
		want := []string{"g-closed", "g-open", "g-open2"}
		if ids := gateIDs(got); !reflect.DeepEqual(ids, want) {
			t.Fatalf("expected deterministic order %v, got %v", want, ids)
		}
	})

	t.Run("empty_deps", func(t *testing.T) {
		got := filterIssueGates(nil, true, 0)
		if len(got) != 0 {
			t.Fatalf("expected no gates, got %v", gateIDs(got))
		}
		if got == nil {
			t.Fatalf("expected non-nil empty slice, got nil")
		}
	})

	t.Run("empty_result_marshals_to_json_empty_array_not_null", func(t *testing.T) {
		got := filterIssueGates(nil, true, 0)
		b, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if string(b) != "[]" {
			t.Fatalf("expected JSON [], got %s", b)
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
			if !strings.Contains(err.Error(), "--repo applies only to gh:run and gh:pr gates") {
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
