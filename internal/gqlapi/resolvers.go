package gqlapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	graphql "github.com/graph-gophers/graphql-go"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// Per-request budgets. MaxParallelism(1) makes graphql-go call one resolver at
// a time, so plain counters are safe.
const (
	// maxReadCalls bounds role calls: uncached Reader.Get, Querier.Query and
	// Reader.Ready.
	maxReadCalls = 200
	// maxResponseObjects bounds the Issue, Relation and Comment objects in a
	// response, counting every occurrence, including repeats and cache hits.
	maxResponseObjects = 10000
	// maxLongTextBytes bounds the long-text bytes in a response, counting
	// every selection, including aliases of the same field.
	maxLongTextBytes = 64 << 20
)

type getKey struct {
	id                   string
	dependents, comments bool
}
type queryResolver struct {
	roles     Roles
	cache     map[getKey]*issueops.IssueDetails
	readCalls int
	objects   int
	textBytes int
}

func (q *queryResolver) read() error {
	if q.readCalls >= maxReadCalls {
		return fmt.Errorf("read budget exceeded: maximum %d role calls", maxReadCalls)
	}
	q.readCalls++
	return nil
}
func (q *queryResolver) reserve(n int) error {
	if n > maxResponseObjects-q.objects {
		return fmt.Errorf("response object budget exceeded: maximum %d objects", maxResponseObjects)
	}
	q.objects += n
	return nil
}
func (q *queryResolver) chargeText(n int) error {
	if n > maxLongTextBytes-q.textBytes {
		return fmt.Errorf("long-text budget exceeded: maximum %d bytes", maxLongTextBytes)
	}
	q.textBytes += n
	return nil
}
func (q *queryResolver) longText(s string) (string, error) {
	if err := q.chargeText(len(s)); err != nil {
		return "", err
	}
	return s, nil
}

func options(ctx context.Context) (bool, bool) {
	dependents := graphql.HasSelectedField(ctx, "dependents")
	for _, field := range []string{"epic_total_children", "epic_closed_children", "epic_closeable", "unresolvable_dependents"} {
		dependents = dependents || graphql.HasSelectedField(ctx, field)
	}
	return dependents, graphql.HasSelectedField(ctx, "comments")
}
func (q *queryResolver) get(ctx context.Context, key getKey) (*issueops.IssueDetails, error) {
	if details, ok := q.cache[key]; ok {
		return details, nil
	}
	if err := q.read(); err != nil {
		return nil, err
	}
	details, err := q.roles.Reader.Get(ctx, issueops.GetRequest{
		ID: key.id, BriefDeps: true, IncludeDependents: key.dependents, IncludeComments: key.comments,
	})
	if errors.Is(err, issueops.ErrNotFound) {
		q.cache[key] = nil
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	q.cache[key] = details
	return details, nil
}
func (q *queryResolver) Issue(ctx context.Context, args struct{ ID graphql.ID }) (*issueResolver, error) {
	d, c := options(ctx)
	details, err := q.get(ctx, getKey{string(args.ID), d, c})
	if err != nil || details == nil {
		return nil, err
	}
	if err := q.reserve(1); err != nil {
		return nil, err
	}
	return &issueResolver{issue: &details.Issue, details: details, q: q}, nil
}
func (q *queryResolver) IssuesById(ctx context.Context, args struct{ IDs []graphql.ID }) ([]*issueResolver, error) {
	if len(args.IDs) > maxReadCalls {
		return nil, fmt.Errorf("issuesById limit exceeded: maximum %d IDs", maxReadCalls)
	}
	d, c := options(ctx)
	result := make([]*issueResolver, len(args.IDs))
	for i, id := range args.IDs {
		details, err := q.get(ctx, getKey{string(id), d, c})
		if err != nil {
			return nil, err
		}
		if details != nil {
			if err := q.reserve(1); err != nil {
				return nil, err
			}
			result[i] = &issueResolver{issue: &details.Issue, details: details, q: q}
		}
	}
	return result, nil
}

// orDefault returns def when an argument is null. graphql-go fills an omitted
// argument from its SDL default, but passes an explicit null, or an optional
// variable left out of --vars, as a null value; bd treats both as the default.
// def must match the SDL default.
func orDefault[T any](v *T, def T) T {
	if v == nil {
		return def
	}
	return *v
}
func listLimit(limit int32) (int, error) {
	if limit < 1 || limit > 200 {
		return 0, fmt.Errorf("limit must be between 1 and 200")
	}
	return int(limit), nil
}
func (q *queryResolver) Issues(ctx context.Context, args struct {
	Query   *string
	All     graphql.NullBool
	Sort    *string
	Reverse graphql.NullBool
	Limit   graphql.NullInt
	Offset  graphql.NullInt
}) (*pageResolver, error) {
	limit, err := listLimit(orDefault(args.Limit.Value, 50))
	if err != nil {
		return nil, err
	}
	expression := `id="*"`
	if args.Query != nil {
		expression = *args.Query
	}
	sort := ""
	if args.Sort != nil {
		sort = *args.Sort
	}
	if err := q.read(); err != nil {
		return nil, err
	}
	page, err := q.roles.Querier.Query(ctx, issueops.QueryRequest{
		Expression: expression, IncludeClosed: orDefault(args.All.Value, false), SortBy: sort, Reverse: orDefault(args.Reverse.Value, false), Limit: &limit, Offset: int(orDefault(args.Offset.Value, 0)),
	})
	if err != nil {
		return nil, err
	}
	return &pageResolver{page: page, q: q}, nil
}
func (q *queryResolver) Ready(ctx context.Context, args struct {
	Limit    graphql.NullInt
	Offset   graphql.NullInt
	Sort     graphql.NullString
	Assignee *string
	Type     *string
	Labels   *[]string
	Parent   *string
}) (*pageResolver, error) {
	limit, err := listLimit(orDefault(args.Limit.Value, 100))
	if err != nil {
		return nil, err
	}
	offset := orDefault(args.Offset.Value, 0)
	if offset < 0 {
		return nil, fmt.Errorf("ready offset must be non-negative")
	}
	req := issueops.ReadyRequest{Limit: &limit, Offset: int(offset), Sort: orDefault(args.Sort.Value, "priority")}
	if args.Assignee != nil {
		req.Assignee = *args.Assignee
	}
	if args.Type != nil {
		req.IssueType = *args.Type
	}
	if args.Labels != nil {
		req.Labels = *args.Labels
	}
	if args.Parent != nil {
		req.ParentID = *args.Parent
	}
	if err := q.read(); err != nil {
		return nil, err
	}
	page, err := q.roles.Reader.Ready(ctx, req)
	if err != nil {
		return nil, err
	}
	return &pageResolver{page: page, q: q}, nil
}

type pageResolver struct {
	page issueops.IssuePage
	q    *queryResolver
}

func (p *pageResolver) HasMore() bool { return p.page.HasMore }
func (p *pageResolver) Items(ctx context.Context) ([]*issueResolver, error) {
	if err := p.q.reserve(len(p.page.Items)); err != nil {
		return nil, err
	}
	d, c := options(ctx)
	items := make([]*issueResolver, 0, len(p.page.Items))
	for _, row := range p.page.Items {
		items = append(items, &issueResolver{issue: row.Issue, row: row, q: p.q, key: getKey{row.ID, d, c}})
	}
	return items, nil
}

type issueResolver struct {
	issue   *types.Issue
	row     *issueops.IssueWithCounts
	details *issueops.IssueDetails
	q       *queryResolver
	key     getKey
}

func (r *issueResolver) detail(ctx context.Context) (*issueops.IssueDetails, error) {
	if r.details != nil {
		return r.details, nil
	}
	// A list row comes from one read and its detail from another, so the
	// issue can be deleted in between.
	details, err := r.q.get(ctx, r.key)
	if err == nil && details == nil {
		return nil, fmt.Errorf("issue %s not found", r.issue.ID)
	}
	return details, err
}
func (r *issueResolver) ID() graphql.ID               { return graphql.ID(r.issue.ID) }
func (r *issueResolver) Title() string                { return r.issue.Title }
func (r *issueResolver) Description() (string, error) { return r.q.longText(r.issue.Description) }
func (r *issueResolver) Design() (string, error)      { return r.q.longText(r.issue.Design) }
func (r *issueResolver) AcceptanceCriteria() (string, error) {
	return r.q.longText(r.issue.AcceptanceCriteria)
}
func (r *issueResolver) Notes() (string, error)   { return r.q.longText(r.issue.Notes) }
func (r *issueResolver) SpecID() string           { return r.issue.SpecID }
func (r *issueResolver) Assignee() string         { return r.issue.Assignee }
func (r *issueResolver) Owner() string            { return r.issue.Owner }
func (r *issueResolver) CreatedBy() string        { return r.issue.CreatedBy }
func (r *issueResolver) CloseReason() string      { return r.issue.CloseReason }
func (r *issueResolver) ClosedBySession() string  { return r.issue.ClosedBySession }
func (r *issueResolver) LeaseGrantedNode() string { return r.issue.LeaseGrantedNode }
func (r *issueResolver) SourceSystem() string     { return r.issue.SourceSystem }
func (r *issueResolver) Sender() string           { return r.issue.Sender }
func (r *issueResolver) AwaitType() string        { return r.issue.AwaitType }
func (r *issueResolver) AwaitID() string          { return r.issue.AwaitID }
func (r *issueResolver) SourceFormula() string    { return r.issue.SourceFormula }
func (r *issueResolver) SourceLocation() string   { return r.issue.SourceLocation }
func (r *issueResolver) EventKind() string        { return r.issue.EventKind }
func (r *issueResolver) Actor() string            { return r.issue.Actor }
func (r *issueResolver) Target() string           { return r.issue.Target }
func (r *issueResolver) Payload() (string, error) { return r.q.longText(r.issue.Payload) }
func (r *issueResolver) Status() string           { return string(r.issue.Status) }
func (r *issueResolver) IssueType() string        { return string(r.issue.IssueType) }
func (r *issueResolver) WispType() string         { return string(r.issue.WispType) }
func (r *issueResolver) StorageClass() string     { return string(r.issue.StorageClass) }
func (r *issueResolver) MolType() string          { return string(r.issue.MolType) }
func (r *issueResolver) WorkType() string         { return string(r.issue.WorkType) }
func (r *issueResolver) Priority() (int32, error) { return toInt32("priority", r.issue.Priority) }
func (r *issueResolver) CompactionLevel() (int32, error) {
	return toInt32("compaction_level", r.issue.CompactionLevel)
}
func (r *issueResolver) OriginalSize() (int32, error) {
	return toInt32("original_size", r.issue.OriginalSize)
}
func (r *issueResolver) IsBlocked() bool               { return r.issue.IsBlocked }
func (r *issueResolver) Ephemeral() bool               { return r.issue.Ephemeral }
func (r *issueResolver) NoHistory() bool               { return r.issue.NoHistory }
func (r *issueResolver) Pinned() bool                  { return r.issue.Pinned }
func (r *issueResolver) IsTemplate() bool              { return r.issue.IsTemplate }
func (r *issueResolver) CreatedAt() graphql.Time       { return graphql.Time{Time: r.issue.CreatedAt} }
func (r *issueResolver) UpdatedAt() graphql.Time       { return graphql.Time{Time: r.issue.UpdatedAt} }
func (r *issueResolver) StartedAt() *graphql.Time      { return optionalTime(r.issue.StartedAt) }
func (r *issueResolver) ClosedAt() *graphql.Time       { return optionalTime(r.issue.ClosedAt) }
func (r *issueResolver) LeaseExpiresAt() *graphql.Time { return optionalTime(r.issue.LeaseExpiresAt) }
func (r *issueResolver) HeartbeatAt() *graphql.Time    { return optionalTime(r.issue.HeartbeatAt) }
func (r *issueResolver) DueAt() *graphql.Time          { return optionalTime(r.issue.DueAt) }
func (r *issueResolver) DeferUntil() *graphql.Time     { return optionalTime(r.issue.DeferUntil) }
func (r *issueResolver) CompactedAt() *graphql.Time    { return optionalTime(r.issue.CompactedAt) }
func (r *issueResolver) ExternalRef() *string          { return r.issue.ExternalRef }
func (r *issueResolver) CompactedAtCommit() *string    { return r.issue.CompactedAtCommit }
func optionalTime(t *time.Time) *graphql.Time {
	if t == nil {
		return nil
	}
	return &graphql.Time{Time: *t}
}
func (r *issueResolver) EstimatedMinutes() (*int32, error) {
	if r.issue.EstimatedMinutes == nil {
		return nil, nil
	}
	n, e := toInt32("estimated_minutes", *r.issue.EstimatedMinutes)
	return &n, e
}
func (r *issueResolver) Metadata() (*JSON, error) {
	if err := r.q.chargeText(len(r.issue.Metadata)); err != nil {
		return nil, err
	}
	return rawJSON(r.issue.Metadata), nil
}
func (r *issueResolver) BondedFrom() (*JSON, error) { return marshalJSON(r.issue.BondedFrom) }
func (r *issueResolver) Timeout() (*JSON, error)    { return marshalJSON(r.issue.Timeout) }
func (r *issueResolver) Waiters() []string          { return nonNil(r.issue.Waiters) }
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
func (r *issueResolver) Labels(ctx context.Context) ([]string, error) {
	if r.row != nil {
		return nonNil(r.issue.Labels), nil
	}
	d, e := r.detail(ctx)
	if e != nil {
		return nil, e
	}
	return nonNil(d.Labels), nil
}
func (r *issueResolver) Parent(ctx context.Context) (*string, error) {
	if r.row != nil {
		return r.row.Parent, nil
	}
	d, e := r.detail(ctx)
	if e != nil {
		return nil, e
	}
	return d.Parent, nil
}
func checkedCount(field string, n int64) (int32, error) {
	if n < math.MinInt32 || n > math.MaxInt32 {
		return 0, fmt.Errorf("%s value %d does not fit in a GraphQL Int", field, n)
	}
	return int32(n), nil
}
func (r *issueResolver) count(ctx context.Context, field string) (int32, error) {
	if r.row != nil {
		switch field {
		case "dependency_count":
			return toInt32(field, r.row.DependencyCount)
		case "dependent_count":
			return toInt32(field, r.row.DependentCount)
		default:
			return toInt32(field, r.row.CommentCount)
		}
	}
	d, e := r.detail(ctx)
	if e != nil {
		return 0, e
	}
	var n *int64
	switch field {
	case "dependency_count":
		n = d.DependencyCount
	case "dependent_count":
		n = d.DependentCount
	default:
		n = d.CommentCount
	}
	if n == nil {
		return 0, nil
	}
	return checkedCount(field, *n)
}
func (r *issueResolver) DependencyCount(ctx context.Context) (int32, error) {
	return r.count(ctx, "dependency_count")
}
func (r *issueResolver) DependentCount(ctx context.Context) (int32, error) {
	return r.count(ctx, "dependent_count")
}
func (r *issueResolver) CommentCount(ctx context.Context) (int32, error) {
	return r.count(ctx, "comment_count")
}
func (r *issueResolver) Dependencies(ctx context.Context) ([]*relationResolver, error) {
	d, e := r.detail(ctx)
	if e != nil {
		return nil, e
	}
	return r.relations(d.Dependencies)
}
func (r *issueResolver) Dependents(ctx context.Context) ([]*relationResolver, error) {
	d, e := r.detail(ctx)
	if e != nil {
		return nil, e
	}
	return r.relations(d.Dependents)
}
func (r *issueResolver) relations(rows []*types.IssueWithDependencyMetadata) ([]*relationResolver, error) {
	if err := r.q.reserve(len(rows)); err != nil {
		return nil, err
	}
	out := make([]*relationResolver, 0, len(rows))
	for _, row := range rows {
		out = append(out, &relationResolver{row: row, q: r.q})
	}
	return out, nil
}
func (r *issueResolver) Comments(ctx context.Context) ([]*commentResolver, error) {
	d, e := r.detail(ctx)
	if e != nil {
		return nil, e
	}
	if err := r.q.reserve(len(d.Comments)); err != nil {
		return nil, err
	}
	out := make([]*commentResolver, 0, len(d.Comments))
	for _, row := range d.Comments {
		out = append(out, &commentResolver{row: row, q: r.q})
	}
	return out, nil
}
func (r *issueResolver) Revision(ctx context.Context) (string, error) {
	d, e := r.detail(ctx)
	if e != nil {
		return "", e
	}
	return d.Revision, nil
}
func (r *issueResolver) EpicTotalChildren(ctx context.Context) (*int32, error) {
	d, e := r.detail(ctx)
	if e != nil {
		return nil, e
	}
	if d.EpicTotalChildren == nil {
		return nil, nil
	}
	n, e := toInt32("epic_total_children", *d.EpicTotalChildren)
	return &n, e
}
func (r *issueResolver) EpicClosedChildren(ctx context.Context) (*int32, error) {
	d, e := r.detail(ctx)
	if e != nil {
		return nil, e
	}
	if d.EpicClosedChildren == nil {
		return nil, nil
	}
	n, e := toInt32("epic_closed_children", *d.EpicClosedChildren)
	return &n, e
}
func (r *issueResolver) EpicCloseable(ctx context.Context) (*bool, error) {
	d, e := r.detail(ctx)
	if e != nil {
		return nil, e
	}
	return d.EpicCloseable, nil
}
func (r *issueResolver) UnresolvableDependencies(ctx context.Context) (*int32, error) {
	d, e := r.detail(ctx)
	if e != nil {
		return nil, e
	}
	if d.UnresolvableDependencies == nil {
		return nil, nil
	}
	n, e := toInt32("unresolvable_dependencies", *d.UnresolvableDependencies)
	return &n, e
}
func (r *issueResolver) UnresolvableDependents(ctx context.Context) (*int32, error) {
	d, e := r.detail(ctx)
	if e != nil {
		return nil, e
	}
	if d.UnresolvableDependents == nil {
		return nil, nil
	}
	n, e := toInt32("unresolvable_dependents", *d.UnresolvableDependents)
	return &n, e
}

type relationResolver struct {
	row *types.IssueWithDependencyMetadata
	q   *queryResolver
}

func (r *relationResolver) ID() graphql.ID         { return graphql.ID(r.row.ID) }
func (r *relationResolver) DependencyType() string { return string(r.row.DependencyType) }
func (r *relationResolver) Issue(ctx context.Context) (*issueResolver, error) {
	shallow := map[string]bool{"id": true, "title": true, "status": true, "issue_type": true, "priority": true}
	onlyShallow := true
	for _, name := range graphql.SelectedFieldNames(ctx) {
		if !shallow[name] {
			onlyShallow = false
			break
		}
	}
	if onlyShallow {
		if err := r.q.reserve(1); err != nil {
			return nil, err
		}
		return &issueResolver{issue: &r.row.Issue, q: r.q}, nil
	}
	d, c := options(ctx)
	details, e := r.q.get(ctx, getKey{r.row.ID, d, c})
	if e != nil {
		return nil, e
	}
	if details == nil {
		return nil, fmt.Errorf("relation issue %s not found", r.row.ID)
	}
	if err := r.q.reserve(1); err != nil {
		return nil, err
	}
	return &issueResolver{issue: &details.Issue, details: details, q: r.q}, nil
}

type commentResolver struct {
	row *types.Comment
	q   *queryResolver
}

func (r *commentResolver) ID() graphql.ID          { return graphql.ID(r.row.ID) }
func (r *commentResolver) IssueID() string         { return r.row.IssueID }
func (r *commentResolver) Author() string          { return r.row.Author }
func (r *commentResolver) Text() (string, error)   { return r.q.longText(r.row.Text) }
func (r *commentResolver) CreatedAt() graphql.Time { return graphql.Time{Time: r.row.CreatedAt} }

// toInt32 converts a count or level to GraphQL's 32-bit Int. A value that
// does not fit is an error, never a truncated number.
func toInt32[T int | int64](field string, v T) (int32, error) {
	if v < math.MinInt32 || v > math.MaxInt32 {
		return 0, fmt.Errorf("%s value %d does not fit in a GraphQL Int", field, v)
	}
	return int32(v), nil
}

// JSON is an output-only scalar holding a value exactly as encoding/json
// encodes the Go field bd prints with --json.
type JSON struct {
	raw json.RawMessage
}

// ImplementsGraphQLType binds JSON to the schema's JSON scalar.
func (JSON) ImplementsGraphQLType(name string) bool { return name == "JSON" }

// UnmarshalGraphQL refuses input: no argument in the schema takes JSON.
func (*JSON) UnmarshalGraphQL(any) error { return errors.New("JSON is an output-only scalar") }

// MarshalJSON emits the held value unchanged.
func (j JSON) MarshalJSON() ([]byte, error) { return j.raw, nil }

// rawJSON wraps an already-encoded value; an empty value is null.
func rawJSON(raw json.RawMessage) *JSON {
	if len(raw) == 0 {
		return nil
	}
	return &JSON{raw: raw}
}

func marshalJSON(v any) (*JSON, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return &JSON{raw: raw}, nil
}
