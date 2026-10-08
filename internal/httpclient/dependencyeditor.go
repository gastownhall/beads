// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/dependencyeditor.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/encode"
	"github.com/steveyegge/beads/issueops"
)

// maxAddDependencyEdges mirrors the wire's own bound (internal/httpapi's
// dependency_edit.go). It is checked HERE as well as there so the refusal names
// the bound instead of arriving as a 400 the user has to decode — and, more
// importantly, so nothing is ever tempted to chunk a larger request: chunking is
// the one path that silently breaks the request's one-transaction,
// one-history-entry contract (L16).
const maxAddDependencyEdges = 100

// httpDependencyEditor serves issueops.DependencyEditor from the two dependency
// custom methods (design D8 row 18).
//
// The role's whole difficulty is its refusal vocabulary, and the wave that
// landed these operations published the typed discriminators this client asked
// for: dependency_cycle carries issue_id/blocker_id/blocker_is_ancestor, so
// *DependencyHierarchyConflictError reconstructs in BOTH polarities with no
// prose parsing, and dependency_exists carries existing_type/requested_type. The
// mapping lives in the wire's problem table; what lives here is the request
// half and the members the wire cannot carry.
type httpDependencyEditor struct {
	store *Store
	wire  WriteWire
}

var _ issueops.DependencyEditor = (*httpDependencyEditor)(nil)

// AddDependencies asserts a set of edges as one server-side transaction.
//
// Atomicity is the SERVER's and stays whole because the request does: the
// operation is all-or-nothing, so a client that split a graph across two
// requests would turn one atomic assertion into two and let a cycle gate that
// can only see one request at a time pass a graph that closes a loop across
// both. Hence the bound below refuses rather than chunks.
func (d *httpDependencyEditor) AddDependencies(ctx context.Context, req issueops.AddDependenciesRequest) (result issueops.AddDependenciesResult, err error) {
	// Write-side parity with reads: decorates a bare *encode.RefusedError into
	// *InexpressibleError so errors.As(err, &unsupported) reaches
	// *storage.ErrUnsupported, same as inexpressible does for a read role.
	defer func() { err = d.store.inexpressible("DependencyEditor.AddDependencies", err) }()
	if err := requireActor(req.Actor); err != nil {
		return issueops.AddDependenciesResult{}, err
	}
	if len(req.Edges) == 0 {
		return issueops.AddDependenciesResult{}, invalid("a dependency add names no edges")
	}
	if req.SkipPerEdgeCycleCheck {
		return issueops.AddDependenciesResult{}, refuse(encode.OpAddDependencies, "W-AddDependenciesRequest.SkipPerEdgeCycleCheck")
	}
	if len(req.Edges) > maxAddDependencyEdges {
		return issueops.AddDependenciesResult{}, refuse(encode.OpAddDependencies, "L16")
	}

	edges := make([]apigen.DependencyEdge, 0, len(req.Edges))
	for i, edge := range req.Edges {
		if err := checkEdge(i, edge); err != nil {
			return issueops.AddDependenciesResult{}, err
		}
		edges = append(edges, apigen.DependencyEdge{
			IssueId:     edge.IssueID,
			DependsOnId: edge.DependsOnID,
			Type:        string(edge.Type),
		})
	}

	res, err := d.wire.AddDependencies(ctx, apigen.AddDependenciesRequest{Actor: req.Actor, Edges: edges})
	if err != nil {
		return issueops.AddDependenciesResult{}, err
	}

	// The response echoes the request in request order, because all-or-nothing
	// means it is either every edge or the call failed. Reading it back rather
	// than re-echoing the request is what would catch a server that ever
	// answered something else.
	added := make([]issueops.DependencyEdge, 0, len(res.Added))
	for _, edge := range res.Added {
		added = append(added, issueops.DependencyEdge{
			IssueID:     edge.IssueId,
			DependsOnID: edge.DependsOnId,
			Type:        issueops.DependencyType(edge.Type),
		})
	}
	return issueops.AddDependenciesResult{Added: added}, nil
}

// RemoveDependency removes exactly the named edge. A missing edge is Removed
// false with a nil error — a success, not a refusal, and the wire says the same.
func (d *httpDependencyEditor) RemoveDependency(ctx context.Context, req issueops.RemoveDependencyRequest) (issueops.RemoveDependencyResult, error) {
	if err := requireActor(req.Actor); err != nil {
		return issueops.RemoveDependencyResult{}, err
	}
	if err := requireID("issue id", req.IssueID); err != nil {
		return issueops.RemoveDependencyResult{}, err
	}
	if err := requireID("depends-on id", req.DependsOnID); err != nil {
		return issueops.RemoveDependencyResult{}, err
	}

	res, err := d.wire.RemoveDependency(ctx, apigen.RemoveDependencyRequest{
		Actor:       req.Actor,
		IssueId:     req.IssueID,
		DependsOnId: req.DependsOnID,
	})
	if err != nil {
		return issueops.RemoveDependencyResult{}, err
	}
	return issueops.RemoveDependencyResult{Removed: res.Removed}, nil
}

// checkEdge applies the request-intrinsic refusals before the dial.
//
// The self-dependency check is the one worth naming: the server refuses it too,
// but as a 400 that reaches a client as ErrValidation, while the role's contract
// promises ErrSelfDependency — and that sentinel is one the caller can act on.
// It is decidable from the request alone, so deciding it here loses nothing and
// keeps the vocabulary the role documents.
func checkEdge(i int, edge issueops.DependencyEdge) error {
	switch {
	case edge.IssueID == "":
		return invalid("edges[%d].issue_id is required", i)
	case edge.DependsOnID == "":
		return invalid("edges[%d].depends_on_id is required", i)
	case edge.Type == "":
		return invalid("edges[%d].type is required", i)
	case edge.IssueID == edge.DependsOnID:
		return fmt.Errorf("%s: %w", edge.IssueID, issueops.ErrSelfDependency)
	}
	return nil
}
