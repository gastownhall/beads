package main

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/steveyegge/beads/internal/github"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// bd-uschhs (#5850) AC-6: the GitHub relationship sync pass writes remote
// links from what GetDependenciesWithMetadata answers, and that reader's
// issue-only contract (an edge whose target has no row in this database is
// dropped, never placeholdered — see hydrateAnchorEdgesInTx) is the seam
// that keeps the pass from writing a link to an issue that does not exist.
// Two pins here:
//
//  1. THE SEAM, from the consumer side: with the store answering the way the
//     real reader does — only real rows — the pass derives links for the
//     real far ends alone, and none for the unresolvable edge the anchor
//     carries.
//  2. DEFENCE IN DEPTH: even a store that answered with a row for a missing
//     target cannot get a link written, because the collector keeps only
//     far ends that are issues in scope. Both gates are asserted so the
//     test fails if either is ever traded away.
//
// The reader-level guarantee itself is pinned on real databases by the
// conformance portable case (backend/conformance), which runs against the
// dolt and embedded stores this fake cannot stand in for.
type linkSyncFakeStore struct {
	storage.Storage // promoted: any method this test does not stub fails loudly
	reader          linkSyncPageReader
	deps            map[string][]*types.IssueWithDependencyMetadata
}

func (s *linkSyncFakeStore) IssueReader() (issueops.Reader, error) {
	return s.reader, nil
}

func (s *linkSyncFakeStore) GetDependenciesWithMetadata(_ context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	return s.deps[issueID], nil
}

// linkSyncPageReader answers the workspace List the collector opens, and
// refuses Ready/Get: this test is about the relationship pass, and a silent
// empty answer from those would let a future refactor move the pass onto a
// path this test never covered.
type linkSyncPageReader struct {
	issues []*types.Issue
}

func (r linkSyncPageReader) List(_ context.Context, _ issueops.ListRequest) (issueops.IssuePage, error) {
	items := make([]*types.IssueWithCounts, 0, len(r.issues))
	for _, iss := range r.issues {
		items = append(items, &types.IssueWithCounts{Issue: iss})
	}
	return issueops.IssuePage{Items: items}, nil
}

func (linkSyncPageReader) Ready(context.Context, issueops.ReadyRequest) (issueops.IssuePage, error) {
	return issueops.IssuePage{}, errors.New("unreachable: the relationship pass does not read ready work")
}

func (linkSyncPageReader) Get(context.Context, issueops.GetRequest) (*issueops.IssueDetails, error) {
	return nil, errors.New("unreachable: the relationship pass does not read detail views")
}

func TestCollectGitHubLinkSyncDataNeverLinksAMissingTarget(t *testing.T) {
	issueWithRef := func(id string, number int) *types.Issue {
		ref := "https://github.com/o/r/issues/" + strconv.Itoa(number)
		return &types.Issue{ID: id, Title: id, ExternalRef: &ref}
	}
	parent := issueWithRef("bd-epic", 11)
	child := issueWithRef("bd-child", 22)
	blocker := issueWithRef("bd-blk", 33)
	page := linkSyncPageReader{issues: []*types.Issue{parent, child, blocker}}
	scope := github.NewRefScope("https://github.com", "o", "r")

	// The anchor carries one parent-child edge and one blocks edge to real,
	// linked issues — and one tracks edge to a target this database has no
	// row for. The real reader answers with the first two and drops the
	// third before the collector ever sees it.
	store := &linkSyncFakeStore{
		Storage: nil,
		reader:  page,
		deps: map[string][]*types.IssueWithDependencyMetadata{
			"bd-epic": {
				{Issue: *child, DependencyType: types.DepParentChild},
				{Issue: *blocker, DependencyType: types.DepBlocks},
			},
			"bd-child": {},
			"bd-blk":   {},
		},
	}
	data, warnings := collectGitHubLinkSyncData(context.Background(), store, scope, tracker.SyncOptions{})
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if len(data.DesiredLinks) != 2 {
		t.Fatalf("desired links = %+v, want exactly the sub-issue and blocked-by links for the real far ends", data.DesiredLinks)
	}
	for _, link := range data.DesiredLinks {
		if link.FromBeadsID == "external:liveop:ghost" || link.ToBeadsID == "external:liveop:ghost" {
			t.Errorf("a link was written for the unresolvable target: %+v", link)
		}
		if link.FromBeadsID == "" || link.ToBeadsID == "" {
			t.Errorf("a link lost its beads ids: %+v", link)
		}
	}

	// DEFENCE IN DEPTH: a store that answers with a row for a missing target
	// anyway cannot get a link written for it — the far end is not an issue
	// in scope, so the collector drops it before deriving any link.
	stray := &linkSyncFakeStore{
		Storage: nil,
		reader:  page,
		deps: map[string][]*types.IssueWithDependencyMetadata{
			"bd-epic": {
				{Issue: *child, DependencyType: types.DepParentChild},
				{Issue: types.Issue{ID: "external:liveop:ghost"}, DependencyType: types.DepParentChild},
				{Issue: types.Issue{ID: "liveop-x"}, DependencyType: types.DepBlocks},
			},
			"bd-child": {},
			"bd-blk":   {},
		},
	}
	data, warnings = collectGitHubLinkSyncData(context.Background(), stray, scope, tracker.SyncOptions{})
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if len(data.DesiredLinks) != 1 {
		t.Fatalf("desired links = %+v, want the one link for the real far end alone", data.DesiredLinks)
	}
	// The link's direction: FromBeadsID is the PARENT far end the edge
	// names, ToBeadsID the child issue the collector is iterating — so the
	// real parent-child edge (bd-epic's edge to bd-child) survives as
	// bd-child -> bd-epic, and nothing else does.
	if data.DesiredLinks[0].FromBeadsID != "bd-child" || data.DesiredLinks[0].ToBeadsID != "bd-epic" {
		t.Errorf("the surviving link = %+v, want the real parent-child link bd-child -> bd-epic", data.DesiredLinks[0])
	}
}
