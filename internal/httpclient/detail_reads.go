// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/detail_reads.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The four off-role detail reads that ride getIssue, each beside the surface it
// belongs to now that its operation is on the synced wire (design D8, the flip
// mechanics offrole.go's header states). They moved OUT of offrole.go the way
// resolve.go and bridge.go's reads did: a served method sits with its surface,
// and what stays in offrole.go is the tombstone.
//
// All four are the `bd show` text path's off-role reads. getIssue carries the
// labels and the default dependencies without a parameter; the dependents and
// comments arms travel through include_dependents and include_comments (D4; L4
// retired). Each answers the same value the local method does, so a caller
// reading `issue.Labels`, the dependency list, the dependents or the comment
// thread cannot tell the two apart.
//
// A MISS IS (nil, nil), NOT AN ERROR, for every one of them, and it is the raw
// method's own contract: getIssue answers a nonexistent id with a 404 that the
// wire maps to issueops.ErrNotFound, and the local reads answer a nonexistent id
// with an empty result and a nil error (dolt/labels.go, dolt/dependencies.go,
// dolt/events.go). Reporting the miss as an error would print the transport's
// vocabulary where the local answer is silence.

// getIssueDetails is the shared dial behind the three include-bearing reads: GET
// the issue-detail path with the caller's query and decode types.IssueDetails,
// exactly as bridge.go's GetIssue does. A 404 is (nil, nil) rather than an
// error, so each read maps its own empty answer onto it.
func (s *Store) getIssueDetails(ctx context.Context, id string, query url.Values) (*types.IssueDetails, error) {
	if id == "" {
		return nil, nil
	}
	path, err := wire.IssuePath(id)
	if err != nil {
		return nil, err
	}
	var details types.IssueDetails
	err = s.dispatch(ctx, wire.Request{
		Op:      wire.OpGetIssue,
		Method:  http.MethodGet,
		Path:    path,
		Query:   query,
		IssueID: id,
	}, &details)
	if errors.Is(err, issueops.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &details, nil
}

// GetLabels serves getIssue's label list; `bd show`'s text path discards its
// error today (D4).
//
// It delegates to the raw GetIssue probe rather than re-dialing, because that
// probe already performs the lift the detail view needs — the labels arrive in a
// field of the envelope's OWN rather than on the embedded issue, and GetIssue
// hoists them onto issue.Labels so the two backends answer the same value. A
// second dial here would be a second copy of that correction.
func (s *Store) GetLabels(ctx context.Context, id string) ([]string, error) {
	issue, err := s.GetIssue(ctx, id)
	if err != nil || issue == nil {
		return nil, err
	}
	return issue.Labels, nil
}

// GetDependenciesWithMetadata answers getIssue's default-carried dependency
// list. No parameter asks for it — dependencies ride every detail read, and
// brief_deps defaults false so the rows arrive whole.
func (s *Store) GetDependenciesWithMetadata(ctx context.Context, id string) ([]*types.IssueWithDependencyMetadata, error) {
	details, err := s.getIssueDetails(ctx, id, url.Values{})
	if err != nil || details == nil {
		return nil, err
	}
	return details.Dependencies, nil
}

// GetDependentsWithMetadata answers getIssue's dependents arm, which is carried
// only when asked: include_dependents is the parameter D4 landed for it.
//
// The rows come back in the collectDependents SHALLOW projection (be-4d36f2:
// id, status, type, priority, title and the edge type only), because a hub bead
// with thousands of dependents would otherwise marshal gigabytes. That is the
// field set `bd show`'s dependents section and its reader-accessor JSON consume,
// so those paths are full-parity — but a caller reading any field OUTSIDE it
// over http reads a zero. `bd show --thread/--refs/--children` are exactly such
// callers (the message thread reads a reply's sender/recipient/body/timestamp,
// --refs/--children --json marshal the row whole), so they refuse against an
// http workspace rather than render a silently gutted answer (the show entry in
// enterprise_http_refusal.go carries the flag refusals and the citation). A
// full-dependent wire shape would retire those refusals and is a server-side ask.
func (s *Store) GetDependentsWithMetadata(ctx context.Context, id string) ([]*types.IssueWithDependencyMetadata, error) {
	q := url.Values{}
	q.Set("include_dependents", "true")
	details, err := s.getIssueDetails(ctx, id, q)
	if err != nil || details == nil {
		return nil, err
	}
	return details.Dependents, nil
}

// GetIssueComments answers getIssue's comment thread, carried only when
// include_comments asks for it.
//
// comments_omitted MUST be absent or false on the answer, and this checks it:
// the server sets that marker only in count-only mode, where a positive
// CommentCount sits beside a nil Comments (ga-clgh). A caller that read those nil
// Comments as an empty thread would mistake a withheld read for "no comments" —
// the exact confusion the marker exists to prevent — so a truthy marker beside a
// request that explicitly asked for the bodies is a server answering a different
// question, and it fails loudly rather than returning a silent empty.
func (s *Store) GetIssueComments(ctx context.Context, id string) ([]*types.Comment, error) {
	q := url.Values{}
	q.Set("include_comments", "true")
	details, err := s.getIssueDetails(ctx, id, q)
	if err != nil || details == nil {
		return nil, err
	}
	if details.CommentsOmitted != nil && *details.CommentsOmitted {
		return nil, fmt.Errorf("bd serve withheld the comment thread for %q despite include_comments: "+
			"comments_omitted is set, which the operation reserves for count-only mode", id)
	}
	return details.Comments, nil
}
