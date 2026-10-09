package main

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// fakeMolBackend is a storage.DoltStorage whose only live members are the ones
// these tests call; anything else panics on the nil embedded interface.
type fakeMolBackend struct {
	storage.DoltStorage
	getter    issueops.BatchGetter
	getterErr error
	issues    map[string]*types.Issue
	reader    issueops.Reader
	edges     issueops.EdgeReader
}

func (f fakeMolBackend) BatchGetter() (issueops.BatchGetter, error) { return f.getter, f.getterErr }
func (f fakeMolBackend) IssueReader() (issueops.Reader, error)      { return f.reader, nil }
func (f fakeMolBackend) EdgeReader() (issueops.EdgeReader, error)   { return f.edges, nil }
func (f fakeMolBackend) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	return f.issues[id], nil
}

type refusingGetter struct{ err error }

func (g refusingGetter) GetMany(context.Context, issueops.GetManyRequest) (issueops.GetManyResult, error) {
	return issueops.GetManyResult{}, g.err
}

type refusingReader struct {
	issueops.Reader
	err error
}

func (r refusingReader) List(context.Context, issueops.ListRequest) (issueops.IssuePage, error) {
	return issueops.IssuePage{}, r.err
}

type refusingEdges struct{ err error }

func (e refusingEdges) ReadEdges(context.Context, issueops.EdgeReadRequest) (issueops.EdgeReadResult, error) {
	return issueops.EdgeReadResult{}, e.err
}

// A backend that does not serve the batch read (an http server without
// issues.batchGet, as the MC server is) still answers the roots, one Get per id.
func TestRoleMolStoreGetIssuesByIDsFallsBackOnlyOnUnsupported(t *testing.T) {
	issues := map[string]*types.Issue{"bd-1": {ID: "bd-1"}, "bd-2": {ID: "bd-2"}}
	unsupported := &storage.ErrUnsupported{Op: "GetMany", Backend: "http"}

	s := roleMolStore{DoltStorage: fakeMolBackend{getter: refusingGetter{err: unsupported}, issues: issues}}
	got, err := s.GetIssuesByIDs(context.Background(), []string{"bd-1", "bd-missing", "bd-2"})
	if err != nil {
		t.Fatalf("GetIssuesByIDs with an unsupported batch: %v", err)
	}
	if len(got) != 2 || got[0].ID != "bd-1" || got[1].ID != "bd-2" {
		t.Fatalf("GetIssuesByIDs = %v, want bd-1 and bd-2 by per-id Get", got)
	}

	boom := errors.New("connection reset")
	s = roleMolStore{DoltStorage: fakeMolBackend{getter: refusingGetter{err: boom}, issues: issues}}
	if _, err := s.GetIssuesByIDs(context.Background(), []string{"bd-1"}); !errors.Is(err, boom) {
		t.Fatalf("GetIssuesByIDs with a failing batch = %v, want the failure, not a fallback", err)
	}
}

// The listing and the parent walk RETURN a refusal; they used to answer "no
// molecules", which is how `bd mol current` went silently empty over http.
func TestMoleculeFindersSurfaceRefusals(t *testing.T) {
	refusal := &storage.ErrUnsupported{Op: "SearchIssuesWithCounts", Backend: "http"}
	s := newRoleMolStore(fakeMolBackend{reader: refusingReader{err: refusal}, edges: refusingEdges{err: refusal}})

	var unsupported *storage.ErrUnsupported
	if _, err := findInProgressMolecules(context.Background(), s, "alice"); !errors.As(err, &unsupported) {
		t.Errorf("findInProgressMolecules error = %v, want the refusal", err)
	}
	if _, err := findHookedMolecules(context.Background(), s, "alice"); !errors.As(err, &unsupported) {
		t.Errorf("findHookedMolecules error = %v, want the refusal", err)
	}
	if _, err := findInProgressMoleculeIDs(context.Background(), s, "alice"); !errors.As(err, &unsupported) {
		t.Errorf("findInProgressMoleculeIDs error = %v, want the refusal", err)
	}
	if _, err := findParentMolecules(context.Background(), s, []string{"bd-1"}); !errors.As(err, &unsupported) {
		t.Errorf("findParentMolecules error = %v, want the refusal", err)
	}
}

func TestIsLostStepClaim(t *testing.T) {
	for _, err := range []error{issueops.ErrStatusMismatch, issueops.ErrNotClaimable, issueops.ErrAlreadyClaimed} {
		if !isLostStepClaim(errors.Join(errors.New("claim"), err)) {
			t.Errorf("isLostStepClaim(%v) = false, want true", err)
		}
	}
	if isLostStepClaim(&storage.ErrUnsupported{Op: "RunInTransaction"}) {
		t.Error("isLostStepClaim(unsupported) = true; a refusal must surface, not try the next step")
	}
}
