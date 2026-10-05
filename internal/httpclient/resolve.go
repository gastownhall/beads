// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/resolve.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/httpclient/encode"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The id-resolution surface (design D11). ResolvePartialID is the front door
// every id-taking command goes through, and it makes exactly three store
// probes: an exact-ids SearchIssues, two GetConfig reads for the prefix
// vocabulary, and — only if those fail — a substring SearchIssueIDs. The first
// two are served here; the third has no wire operation at all and is on the
// unsupported allowlist, where the refusal below gives it its vocabulary.

// ErrPartialIDSearch reports that a substring or prefix id lookup was asked of
// a server that publishes no operation for one.
var ErrPartialIDSearch = errors.New("partial-id search is not available over HTTP")

// PartialIDSearchError is D11's dedicated refusal, and it is dedicated because
// the raw failure it replaces answers the wrong question.
//
// The input may be a partial id OR a full id that simply does not exist, and
// the client cannot tell which: it asked for an exact row and got nothing back.
// A "failed to search issues" fallthrough would report the second case as a
// backend fault and the first as nothing at all, so the text names both
// outcomes and the one recovery that covers them.
type PartialIDSearchError struct {
	// Input is what the user typed, echoed so the message is about their id.
	Input string
	// ServerURL is the server that was asked.
	ServerURL string
	// Unsup is the portable sentinel, so errors.As classification holds through
	// this type exactly as it does through every other refusal here.
	Unsup *ErrHTTPUnsupported
}

func (e *PartialIDSearchError) Error() string {
	return fmt.Sprintf("no exact issue %q on bd serve at %s; partial-id search is not available over HTTP — "+
		"check the id or use a local workspace", e.Input, e.ServerURL)
}

func (e *PartialIDSearchError) Unwrap() []error { return []error{e.Unsup, ErrPartialIDSearch} }

func (s *Store) partialIDSearch(op, input string) error {
	e := &PartialIDSearchError{Input: input, ServerURL: s.target.String()}
	if unsup, ok := s.unsupported(op).(*ErrHTTPUnsupported); ok {
		e.Unsup = unsup
	}
	return e
}

// SearchIssues serves the two shapes the v0 wire can express and refuses every
// other one.
//
// The classification is the encoder's, not this method's: PlanSearch decides
// SHAPE FIRST and then sweeps the fields that shape cannot read, so a filter
// naming both an id set and a parent is refused before either arm is chosen.
// What is decided here is only what to do with each shape once it is named.
//
// The `query` argument is the substring search, and it has no wire operation:
// it refuses with D11's partial-id text rather than with a bare sentinel,
// because a caller that reaches this with a non-empty query is resolving an id
// and the useful thing to say is that the id was not found exactly.
func (s *Store) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	if strings.TrimSpace(query) != "" {
		return nil, s.partialIDSearch("SearchIssues", query)
	}
	plan, err := encode.PlanSearch(filter, func() encode.ListConfig { return s.listConfig(ctx) })
	if err != nil {
		return nil, s.inexpressible("SearchIssues", err)
	}
	switch plan.Shape {
	case encode.SearchExactIDs:
		return s.getIssuesByExactID(ctx, plan.IDs)
	case encode.SearchParentWalk:
		return s.walkDescendants(ctx, plan)
	}
	return nil, s.unsupported("SearchIssues")
}

// getIssuesByExactID is D11's fast path: one getIssue per named id, in the
// caller's order, bounded by the encoder at MaxExactIDs.
//
// A 404 becomes an ABSENT ROW, not an error. That is the whole reason this is a
// special case rather than a filter: ResolvePartialID's first probe reads
// `err == nil && len(issues) > 0`, so a miss reported as an error would abandon
// resolution at the exact point the resolver is supposed to fall through to its
// prefix pass. Every other failure — a dead connection, a busy server, a
// refused credential — passes through, because none of them is evidence about
// whether the row exists.
func (s *Store) getIssuesByExactID(ctx context.Context, ids []string) ([]*types.Issue, error) {
	found := make([]*types.Issue, 0, len(ids))
	for _, id := range ids {
		path, err := wire.IssuePath(id)
		if err != nil {
			return nil, err
		}
		var details types.IssueDetails
		err = s.dispatch(ctx, wire.Request{
			Op:      wire.OpGetIssue,
			Method:  http.MethodGet,
			Path:    path,
			IssueID: id,
		}, &details)
		if errors.Is(err, issueops.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		// The detail view carries labels in a field of its OWN rather than on
		// the embedded issue, and the local SearchIssues hydrates them onto the
		// issue. Lifting them across is what makes the two answers the same
		// value; a caller reading `issue.Labels` would otherwise see an
		// unlabeled row over http and a labeled one locally.
		issue := details.Issue
		if len(issue.Labels) == 0 && len(details.Labels) > 0 {
			issue.Labels = append([]string(nil), details.Labels...)
		}
		found = append(found, &issue)
	}
	return found, nil
}

// walkDescendants is D4's hierarchical shape: paged listIssues?parent=<id>.
//
// The CLI asks each level for every descendant (Limit=0 per level), and
// unlimited maps to PAGING TO EXHAUSTION rather than to the wire's `limit=0`,
// which a non-loopback server refuses outright. The recursion itself belongs to
// the caller — findAllDescendants re-parents the filter at every level — so
// this answers one level.
func (s *Store) walkDescendants(ctx context.Context, plan encode.SearchPlan) ([]*types.Issue, error) {
	// Not projected: a descendant walk hands its rows back as whole issues, and
	// a caller of the hierarchy has no way to learn a body was withheld.
	rows, err := s.fetchIssuePages(ctx, plan.Params, false, 0, 0, "", nil)
	if err != nil {
		return nil, err
	}
	out := make([]*types.Issue, 0, len(rows))
	for _, row := range rows {
		if row != nil && row.Issue != nil {
			out = append(out, row.Issue)
		}
	}
	return out, nil
}

// ErrSettingRedacted reports a setting whose value the server withheld because
// its KEY marks it credential-bearing.
var ErrSettingRedacted = errors.New("bd serve withholds this setting's value")

// RedactedSettingError is ledger row L9 made answerable: the value is absent
// with a reason, rather than absent as an empty string.
//
// Returning "" would be a dropped read dressed as an unset key, and the two are
// not the same answer — one says the workspace stores nothing, the other says
// the workspace stores something this client may not see.
type RedactedSettingError struct {
	Key       string
	ServerURL string
}

func (e *RedactedSettingError) Error() string {
	return fmt.Sprintf("bd serve at %s withholds %q: the key marks it credential-bearing, "+
		"so its value is not served to a client", e.ServerURL, e.Key)
}

func (e *RedactedSettingError) Unwrap() error { return ErrSettingRedacted }

// GetConfig serves getSetting. D11's prefix probes — `issue_prefix` and
// `allowed_prefixes` — ride it, and they are flat legacy keys present in the
// settings enumeration, so nothing about them is special-cased here.
//
// A key nothing stored and a key stored empty are ONE answer on this operation,
// exactly as they are on the local store: the wire omits the value in both
// cases and this returns ("", nil), which is what every caller of GetConfig
// already treats as "unset". The one answer that is NOT "" is a redacted key,
// where a value may well exist and is withheld (L9).
func (s *Store) GetConfig(ctx context.Context, key string) (string, error) {
	path, err := wire.SettingPath(key)
	if err != nil {
		return "", err
	}
	var setting apigen.Setting
	if err := s.dispatch(ctx, wire.Request{
		Op:     wire.OpGetSetting,
		Method: http.MethodGet,
		Path:   path,
	}, &setting); err != nil {
		return "", err
	}
	if setting.Redacted {
		return "", &RedactedSettingError{Key: key, ServerURL: s.target.String()}
	}
	if setting.Value == nil {
		return "", nil
	}
	return *setting.Value, nil
}
