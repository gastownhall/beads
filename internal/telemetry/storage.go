package telemetry

import (
	"context"
	"encoding/json"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

const storageScopeName = "github.com/steveyegge/beads/storage"

// InstrumentedStorage wraps storage.DoltStorage with OTel tracing and metrics.
// Methods on the core Storage interface are overridden to emit bd.storage.*
// counters, duration histograms, and per-operation spans. Methods on the
// DoltStorage capability sub-interfaces (VersionControl, HistoryViewer,
// SyncStore, etc.) pass through to the embedded inner store unchanged —
// those operations already have their own dolt.* spans inside the dolt
// implementation, so wrapping them here would double-count.
//
// Use WrapStorage to construct one; it returns the original store unchanged
// when telemetry is disabled.
type InstrumentedStorage struct {
	storage.DoltStorage // passthrough for capability methods we don't instrument
	inner               storage.DoltStorage
	tracer              trace.Tracer
	ops                 metric.Int64Counter
	dur                 metric.Float64Histogram
	errs                metric.Int64Counter
	issueGauge          metric.Int64Gauge
}

// WrapStorage returns s decorated with OTel instrumentation.
// When telemetry is disabled, s is returned as-is with zero overhead.
func WrapStorage(s storage.DoltStorage) storage.DoltStorage {
	if !Enabled() {
		return s
	}
	m := Meter(storageScopeName)
	ops, _ := m.Int64Counter("bd.storage.operations",
		metric.WithDescription("Total storage operations executed"),
	)
	dur, _ := m.Float64Histogram("bd.storage.operation.duration",
		metric.WithDescription("Storage operation duration in milliseconds"),
		metric.WithUnit("ms"),
	)
	errs, _ := m.Int64Counter("bd.storage.errors",
		metric.WithDescription("Total storage operation errors"),
	)
	issueGauge, _ := m.Int64Gauge("bd.issue.count",
		metric.WithDescription("Current number of issues by status (snapshot from GetStatistics)"),
	)
	return &InstrumentedStorage{
		DoltStorage: s,
		inner:       s,
		tracer:      Tracer(storageScopeName),
		ops:         ops,
		dur:         dur,
		errs:        errs,
		issueGauge:  issueGauge,
	}
}

// Unwrap satisfies storage.Unwrapper so storage.UnwrapStore can peel the
// instrumentation layer for optional-interface type assertions.
func (s *InstrumentedStorage) Unwrap() storage.DoltStorage { return s.inner }

// op starts a span and records a metric for the named storage operation.
func (s *InstrumentedStorage) op(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span, time.Time) {
	all := append([]attribute.KeyValue{attribute.String("db.operation", name)}, attrs...)
	ctx, span := s.tracer.Start(ctx, "storage."+name,
		trace.WithAttributes(all...),
		trace.WithSpanKind(trace.SpanKindClient),
	)
	s.ops.Add(ctx, 1, metric.WithAttributes(all...))
	return ctx, span, time.Now()
}

// done ends the span, records duration and optional error.
func (s *InstrumentedStorage) done(ctx context.Context, span trace.Span, start time.Time, err error, attrs ...attribute.KeyValue) {
	ms := float64(time.Since(start).Milliseconds())
	s.dur.Record(ctx, ms, metric.WithAttributes(attrs...))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		s.errs.Add(ctx, 1, metric.WithAttributes(attrs...))
	}
	span.End()
}

// ── Issue CRUD ──────────────────────────────────────────────────────────────

func (s *InstrumentedStorage) CreateIssue(ctx context.Context, issue *types.Issue, actor string) error {
	attrs := []attribute.KeyValue{
		attribute.String("bd.actor", actor),
		attribute.String("bd.issue.type", string(issue.IssueType)),
	}
	ctx, span, t := s.op(ctx, "CreateIssue", attrs...)
	err := s.inner.CreateIssue(ctx, issue, actor)
	s.done(ctx, span, t, err, attrs...)
	return err
}

func (s *InstrumentedStorage) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error {
	attrs := []attribute.KeyValue{
		attribute.String("bd.actor", actor),
		attribute.Int("bd.issue.count", len(issues)),
	}
	ctx, span, t := s.op(ctx, "CreateIssues", attrs...)
	err := s.inner.CreateIssues(ctx, issues, actor)
	s.done(ctx, span, t, err, attrs...)
	return err
}

func (s *InstrumentedStorage) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	attrs := []attribute.KeyValue{attribute.String("bd.issue.id", id)}
	ctx, span, t := s.op(ctx, "GetIssue", attrs...)
	v, err := s.inner.GetIssue(ctx, id)
	s.done(ctx, span, t, err, attrs...)
	return v, err
}

func (s *InstrumentedStorage) GetIssueByExternalRef(ctx context.Context, externalRef string) (*types.Issue, error) {
	ctx, span, t := s.op(ctx, "GetIssueByExternalRef")
	v, err := s.inner.GetIssueByExternalRef(ctx, externalRef)
	s.done(ctx, span, t, err)
	return v, err
}

func (s *InstrumentedStorage) GetIssuesByIDs(ctx context.Context, ids []string) ([]*types.Issue, error) {
	attrs := []attribute.KeyValue{attribute.Int("bd.issue.count", len(ids))}
	ctx, span, t := s.op(ctx, "GetIssuesByIDs", attrs...)
	v, err := s.inner.GetIssuesByIDs(ctx, ids)
	s.done(ctx, span, t, err, attrs...)
	return v, err
}

func (s *InstrumentedStorage) UpdateIssue(ctx context.Context, id string, updates map[string]interface{}, actor string) error {
	attrs := []attribute.KeyValue{
		attribute.String("bd.issue.id", id),
		attribute.String("bd.actor", actor),
		attribute.Int("bd.update.count", len(updates)),
	}
	ctx, span, t := s.op(ctx, "UpdateIssue", attrs...)
	err := s.inner.UpdateIssue(ctx, id, updates, actor)
	s.done(ctx, span, t, err, attrs...)
	return err
}

func (s *InstrumentedStorage) UpdateIssueChecked(ctx context.Context, id string, updates map[string]interface{}, actor string, opts storage.UpdateIssueOptions) error {
	attrs := []attribute.KeyValue{
		attribute.String("bd.issue.id", id),
		attribute.String("bd.actor", actor),
		attribute.Int("bd.update.count", len(updates)),
	}
	ctx, span, t := s.op(ctx, "UpdateIssueChecked", attrs...)
	err := s.inner.UpdateIssueChecked(ctx, id, updates, actor, opts)
	s.done(ctx, span, t, err, attrs...)
	return err
}

func (s *InstrumentedStorage) ReopenIssue(ctx context.Context, id string, reason string, actor string) error {
	attrs := []attribute.KeyValue{
		attribute.String("bd.issue.id", id),
		attribute.String("bd.actor", actor),
	}
	ctx, span, t := s.op(ctx, "ReopenIssue", attrs...)
	err := s.inner.ReopenIssue(ctx, id, reason, actor)
	s.done(ctx, span, t, err, attrs...)
	return err
}

func (s *InstrumentedStorage) UnclaimIssue(ctx context.Context, id string, actor string, force bool) error {
	attrs := []attribute.KeyValue{
		attribute.String("bd.issue.id", id),
		attribute.String("bd.actor", actor),
		attribute.Bool("bd.force", force),
	}
	ctx, span, t := s.op(ctx, "UnclaimIssue", attrs...)
	err := s.inner.UnclaimIssue(ctx, id, actor, force)
	s.done(ctx, span, t, err, attrs...)
	return err
}

func (s *InstrumentedStorage) UnclaimIssueIfAssignee(ctx context.Context, id string, actor string, expectedAssignee string) error {
	attrs := []attribute.KeyValue{
		attribute.String("bd.issue.id", id),
		attribute.String("bd.actor", actor),
		attribute.String("bd.issue.expected_assignee", expectedAssignee),
	}
	ctx, span, t := s.op(ctx, "UnclaimIssueIfAssignee", attrs...)
	err := s.inner.UnclaimIssueIfAssignee(ctx, id, actor, expectedAssignee)
	s.done(ctx, span, t, err, attrs...)
	return err
}

func (s *InstrumentedStorage) UpdateIssueType(ctx context.Context, id string, issueType string, actor string) error {
	attrs := []attribute.KeyValue{
		attribute.String("bd.issue.id", id),
		attribute.String("bd.issue.type", issueType),
		attribute.String("bd.actor", actor),
	}
	ctx, span, t := s.op(ctx, "UpdateIssueType", attrs...)
	err := s.inner.UpdateIssueType(ctx, id, issueType, actor)
	s.done(ctx, span, t, err, attrs...)
	return err
}

func (s *InstrumentedStorage) CloseIssue(ctx context.Context, id string, reason string, actor string, session string) error {
	attrs := []attribute.KeyValue{
		attribute.String("bd.issue.id", id),
		attribute.String("bd.actor", actor),
	}
	ctx, span, t := s.op(ctx, "CloseIssue", attrs...)
	err := s.inner.CloseIssue(ctx, id, reason, actor, session)
	s.done(ctx, span, t, err, attrs...)
	return err
}

func (s *InstrumentedStorage) CloseIssueChecked(ctx context.Context, id string, actor string, opts storage.CloseIssueOptions) (storage.CloseIssueResult, error) {
	attrs := []attribute.KeyValue{
		attribute.String("bd.issue.id", id),
		attribute.String("bd.actor", actor),
	}
	ctx, span, t := s.op(ctx, "CloseIssueChecked", attrs...)
	res, err := s.inner.CloseIssueChecked(ctx, id, actor, opts)
	s.done(ctx, span, t, err, attrs...)
	return res, err
}

func (s *InstrumentedStorage) DeleteIssue(ctx context.Context, id string) error {
	attrs := []attribute.KeyValue{attribute.String("bd.issue.id", id)}
	ctx, span, t := s.op(ctx, "DeleteIssue", attrs...)
	err := s.inner.DeleteIssue(ctx, id)
	s.done(ctx, span, t, err, attrs...)
	return err
}

func (s *InstrumentedStorage) SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error) {
	attrs := []attribute.KeyValue{attribute.String("bd.query", query)}
	ctx, span, t := s.op(ctx, "SearchIssues", attrs...)
	issues, err := s.inner.SearchIssues(ctx, query, filter)
	if err == nil {
		span.SetAttributes(attribute.Int("bd.result.count", len(issues)))
	}
	s.done(ctx, span, t, err, attrs...)
	return issues, err
}

func (s *InstrumentedStorage) SearchIssuesWithCounts(ctx context.Context, query string, filter types.IssueFilter) ([]*types.IssueWithCounts, error) {
	attrs := []attribute.KeyValue{attribute.String("bd.query", query)}
	ctx, span, t := s.op(ctx, "SearchIssuesWithCounts", attrs...)
	v, err := s.inner.SearchIssuesWithCounts(ctx, query, filter)
	if err == nil {
		span.SetAttributes(attribute.Int("bd.result.count", len(v)))
	}
	s.done(ctx, span, t, err, attrs...)
	return v, err
}

func (s *InstrumentedStorage) SearchIssueIDs(ctx context.Context, query string, filter types.IssueFilter) ([]string, error) {
	attrs := []attribute.KeyValue{attribute.String("bd.query", query)}
	ctx, span, t := s.op(ctx, "SearchIssueIDs", attrs...)
	ids, err := s.inner.SearchIssueIDs(ctx, query, filter)
	if err == nil {
		span.SetAttributes(attribute.Int("bd.result.count", len(ids)))
	}
	s.done(ctx, span, t, err, attrs...)
	return ids, err
}

// ── Dependencies ────────────────────────────────────────────────────────────

func (s *InstrumentedStorage) AddDependency(ctx context.Context, dep *types.Dependency, actor string) error {
	attrs := []attribute.KeyValue{
		attribute.String("bd.dep.from", dep.IssueID),
		attribute.String("bd.dep.to", dep.DependsOnID),
		attribute.String("bd.dep.type", string(dep.Type)),
	}
	ctx, span, t := s.op(ctx, "AddDependency", attrs...)
	err := s.inner.AddDependency(ctx, dep, actor)
	s.done(ctx, span, t, err, attrs...)
	return err
}

func (s *InstrumentedStorage) AddDependencyWithOptions(ctx context.Context, dep *types.Dependency, actor string, opts storage.DependencyAddOptions) error {
	attrs := []attribute.KeyValue{
		attribute.String("bd.dep.from", dep.IssueID),
		attribute.String("bd.dep.to", dep.DependsOnID),
		attribute.String("bd.dep.type", string(dep.Type)),
	}
	ctx, span, t := s.op(ctx, "AddDependency", attrs...)
	err := s.inner.AddDependencyWithOptions(ctx, dep, actor, opts)
	s.done(ctx, span, t, err, attrs...)
	return err
}

func (s *InstrumentedStorage) RemoveDependency(ctx context.Context, issueID, dependsOnID string, actor string) error {
	attrs := []attribute.KeyValue{
		attribute.String("bd.dep.from", issueID),
		attribute.String("bd.dep.to", dependsOnID),
	}
	ctx, span, t := s.op(ctx, "RemoveDependency", attrs...)
	err := s.inner.RemoveDependency(ctx, issueID, dependsOnID, actor)
	s.done(ctx, span, t, err, attrs...)
	return err
}

func (s *InstrumentedStorage) RemoveDependencyWithOptions(ctx context.Context, issueID, dependsOnID string, actor string, opts storage.DependencyRemoveOptions) error {
	attrs := []attribute.KeyValue{
		attribute.String("bd.dep.from", issueID),
		attribute.String("bd.dep.to", dependsOnID),
	}
	ctx, span, t := s.op(ctx, "RemoveDependency", attrs...)
	err := s.inner.RemoveDependencyWithOptions(ctx, issueID, dependsOnID, actor, opts)
	s.done(ctx, span, t, err, attrs...)
	return err
}

func (s *InstrumentedStorage) GetDependencies(ctx context.Context, issueID string) ([]*types.Issue, error) {
	attrs := []attribute.KeyValue{attribute.String("bd.issue.id", issueID)}
	ctx, span, t := s.op(ctx, "GetDependencies", attrs...)
	v, err := s.inner.GetDependencies(ctx, issueID)
	s.done(ctx, span, t, err, attrs...)
	return v, err
}

func (s *InstrumentedStorage) GetDependents(ctx context.Context, issueID string) ([]*types.Issue, error) {
	attrs := []attribute.KeyValue{attribute.String("bd.issue.id", issueID)}
	ctx, span, t := s.op(ctx, "GetDependents", attrs...)
	v, err := s.inner.GetDependents(ctx, issueID)
	s.done(ctx, span, t, err, attrs...)
	return v, err
}

func (s *InstrumentedStorage) GetDependenciesWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	attrs := []attribute.KeyValue{attribute.String("bd.issue.id", issueID)}
	ctx, span, t := s.op(ctx, "GetDependenciesWithMetadata", attrs...)
	v, err := s.inner.GetDependenciesWithMetadata(ctx, issueID)
	s.done(ctx, span, t, err, attrs...)
	return v, err
}

func (s *InstrumentedStorage) GetDependentsWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	attrs := []attribute.KeyValue{attribute.String("bd.issue.id", issueID)}
	ctx, span, t := s.op(ctx, "GetDependentsWithMetadata", attrs...)
	v, err := s.inner.GetDependentsWithMetadata(ctx, issueID)
	s.done(ctx, span, t, err, attrs...)
	return v, err
}

func (s *InstrumentedStorage) GetDependencyTree(ctx context.Context, issueID string, maxDepth int, showAllPaths bool, reverse bool) ([]*types.TreeNode, error) {
	attrs := []attribute.KeyValue{
		attribute.String("bd.issue.id", issueID),
		attribute.Int("bd.max_depth", maxDepth),
	}
	ctx, span, t := s.op(ctx, "GetDependencyTree", attrs...)
	v, err := s.inner.GetDependencyTree(ctx, issueID, maxDepth, showAllPaths, reverse)
	s.done(ctx, span, t, err, attrs...)
	return v, err
}

// ── Labels ──────────────────────────────────────────────────────────────────

func (s *InstrumentedStorage) AddLabel(ctx context.Context, issueID, label, actor string) error {
	attrs := []attribute.KeyValue{
		attribute.String("bd.issue.id", issueID),
		attribute.String("bd.label", label),
	}
	ctx, span, t := s.op(ctx, "AddLabel", attrs...)
	err := s.inner.AddLabel(ctx, issueID, label, actor)
	s.done(ctx, span, t, err, attrs...)
	return err
}

func (s *InstrumentedStorage) RemoveLabel(ctx context.Context, issueID, label, actor string) error {
	attrs := []attribute.KeyValue{
		attribute.String("bd.issue.id", issueID),
		attribute.String("bd.label", label),
	}
	ctx, span, t := s.op(ctx, "RemoveLabel", attrs...)
	err := s.inner.RemoveLabel(ctx, issueID, label, actor)
	s.done(ctx, span, t, err, attrs...)
	return err
}

func (s *InstrumentedStorage) GetLabels(ctx context.Context, issueID string) ([]string, error) {
	attrs := []attribute.KeyValue{attribute.String("bd.issue.id", issueID)}
	ctx, span, t := s.op(ctx, "GetLabels", attrs...)
	v, err := s.inner.GetLabels(ctx, issueID)
	s.done(ctx, span, t, err, attrs...)
	return v, err
}

func (s *InstrumentedStorage) GetIssuesByLabel(ctx context.Context, label string) ([]*types.Issue, error) {
	attrs := []attribute.KeyValue{attribute.String("bd.label", label)}
	ctx, span, t := s.op(ctx, "GetIssuesByLabel", attrs...)
	v, err := s.inner.GetIssuesByLabel(ctx, label)
	s.done(ctx, span, t, err, attrs...)
	return v, err
}

// ── Work queries ─────────────────────────────────────────────────────────────

func (s *InstrumentedStorage) GetReadyWork(ctx context.Context, filter types.WorkFilter) ([]*types.Issue, error) {
	ctx, span, t := s.op(ctx, "GetReadyWork")
	v, err := s.inner.GetReadyWork(ctx, filter)
	if err == nil {
		span.SetAttributes(attribute.Int("bd.result.count", len(v)))
	}
	s.done(ctx, span, t, err)
	return v, err
}

func (s *InstrumentedStorage) GetReadyWorkWithCounts(ctx context.Context, filter types.WorkFilter) ([]*types.IssueWithCounts, error) {
	ctx, span, t := s.op(ctx, "GetReadyWorkWithCounts")
	v, err := s.inner.GetReadyWorkWithCounts(ctx, filter)
	if err == nil {
		span.SetAttributes(attribute.Int("bd.result.count", len(v)))
	}
	s.done(ctx, span, t, err)
	return v, err
}

func (s *InstrumentedStorage) GetBlockedIssues(ctx context.Context, filter types.WorkFilter) ([]*types.BlockedIssue, error) {
	ctx, span, t := s.op(ctx, "GetBlockedIssues")
	v, err := s.inner.GetBlockedIssues(ctx, filter)
	if err == nil {
		span.SetAttributes(attribute.Int("bd.result.count", len(v)))
	}
	s.done(ctx, span, t, err)
	return v, err
}

func (s *InstrumentedStorage) GetEpicsEligibleForClosure(ctx context.Context) ([]*types.EpicStatus, error) {
	ctx, span, t := s.op(ctx, "GetEpicsEligibleForClosure")
	v, err := s.inner.GetEpicsEligibleForClosure(ctx)
	s.done(ctx, span, t, err)
	return v, err
}

// ── Comments & events ────────────────────────────────────────────────────────

func (s *InstrumentedStorage) AddIssueComment(ctx context.Context, issueID, author, text string) (*types.Comment, error) {
	attrs := []attribute.KeyValue{
		attribute.String("bd.issue.id", issueID),
		attribute.String("bd.actor", author),
	}
	ctx, span, t := s.op(ctx, "AddIssueComment", attrs...)
	v, err := s.inner.AddIssueComment(ctx, issueID, author, text)
	s.done(ctx, span, t, err, attrs...)
	return v, err
}

func (s *InstrumentedStorage) GetIssueComments(ctx context.Context, issueID string) ([]*types.Comment, error) {
	attrs := []attribute.KeyValue{attribute.String("bd.issue.id", issueID)}
	ctx, span, t := s.op(ctx, "GetIssueComments", attrs...)
	v, err := s.inner.GetIssueComments(ctx, issueID)
	s.done(ctx, span, t, err, attrs...)
	return v, err
}

func (s *InstrumentedStorage) GetIssueCommentsPage(ctx context.Context, issueID string, after storage.CommentPageCursor, limit int) ([]*types.Comment, error) {
	attrs := []attribute.KeyValue{attribute.String("bd.issue.id", issueID)}
	ctx, span, t := s.op(ctx, "GetIssueCommentsPage", attrs...)
	v, err := s.inner.GetIssueCommentsPage(ctx, issueID, after, limit)
	s.done(ctx, span, t, err, attrs...)
	return v, err
}

func (s *InstrumentedStorage) GetEvents(ctx context.Context, issueID string, limit int) ([]*types.Event, error) {
	attrs := []attribute.KeyValue{attribute.String("bd.issue.id", issueID)}
	ctx, span, t := s.op(ctx, "GetEvents", attrs...)
	v, err := s.inner.GetEvents(ctx, issueID, limit)
	s.done(ctx, span, t, err, attrs...)
	return v, err
}

func (s *InstrumentedStorage) GetAllEventsSince(ctx context.Context, since time.Time) ([]*types.Event, error) {
	attrs := []attribute.KeyValue{attribute.String("bd.since", since.Format(time.RFC3339))}
	ctx, span, t := s.op(ctx, "GetAllEventsSince", attrs...)
	v, err := s.inner.GetAllEventsSince(ctx, since)
	s.done(ctx, span, t, err, attrs...)
	return v, err
}

// ── Statistics ───────────────────────────────────────────────────────────────

func (s *InstrumentedStorage) GetStatistics(ctx context.Context) (*types.Statistics, error) {
	ctx, span, t := s.op(ctx, "GetStatistics")
	v, err := s.inner.GetStatistics(ctx)
	s.done(ctx, span, t, err)
	if err == nil && v != nil {
		// Record current issue counts as gauge snapshots, broken down by status.
		statusAttr := func(status string) metric.MeasurementOption {
			return metric.WithAttributes(attribute.String("status", status))
		}
		s.issueGauge.Record(ctx, int64(v.OpenIssues), statusAttr("open"))
		s.issueGauge.Record(ctx, int64(v.InProgressIssues), statusAttr("in_progress"))
		s.issueGauge.Record(ctx, int64(v.ClosedIssues), statusAttr("closed"))
		s.issueGauge.Record(ctx, int64(v.DeferredIssues), statusAttr("deferred"))
	}
	return v, err
}

// ── Configuration ────────────────────────────────────────────────────────────

func (s *InstrumentedStorage) SetConfig(ctx context.Context, key, value string) error {
	attrs := []attribute.KeyValue{attribute.String("bd.config.key", key)}
	ctx, span, t := s.op(ctx, "SetConfig", attrs...)
	err := s.inner.SetConfig(ctx, key, value)
	s.done(ctx, span, t, err, attrs...)
	return err
}

func (s *InstrumentedStorage) GetConfig(ctx context.Context, key string) (string, error) {
	attrs := []attribute.KeyValue{attribute.String("bd.config.key", key)}
	ctx, span, t := s.op(ctx, "GetConfig", attrs...)
	v, err := s.inner.GetConfig(ctx, key)
	s.done(ctx, span, t, err, attrs...)
	return v, err
}

func (s *InstrumentedStorage) GetAllConfig(ctx context.Context) (map[string]string, error) {
	ctx, span, t := s.op(ctx, "GetAllConfig")
	v, err := s.inner.GetAllConfig(ctx)
	s.done(ctx, span, t, err)
	return v, err
}

func (s *InstrumentedStorage) SetLocalMetadata(ctx context.Context, key, value string) error {
	attrs := []attribute.KeyValue{attribute.String("bd.local_metadata.key", key)}
	ctx, span, t := s.op(ctx, "SetLocalMetadata", attrs...)
	err := s.inner.SetLocalMetadata(ctx, key, value)
	s.done(ctx, span, t, err, attrs...)
	return err
}

func (s *InstrumentedStorage) GetLocalMetadata(ctx context.Context, key string) (string, error) {
	attrs := []attribute.KeyValue{attribute.String("bd.local_metadata.key", key)}
	ctx, span, t := s.op(ctx, "GetLocalMetadata", attrs...)
	v, err := s.inner.GetLocalMetadata(ctx, key)
	s.done(ctx, span, t, err, attrs...)
	return v, err
}

// ── Transactions ─────────────────────────────────────────────────────────────

func (s *InstrumentedStorage) RunInTransaction(ctx context.Context, commitMsg string, fn func(tx storage.Transaction) error) error {
	ctx, span, t := s.op(ctx, "RunInTransaction", attribute.String("db.commit_msg", commitMsg))
	err := s.inner.RunInTransaction(ctx, commitMsg, fn)
	s.done(ctx, span, t, err)
	return err
}

// ── Wisp queries ─────────────────────────────────────────────────────────────

func (s *InstrumentedStorage) ListWisps(ctx context.Context, filter types.WispFilter) ([]*types.Issue, error) {
	ctx, span, t := s.op(ctx, "ListWisps")
	v, err := s.inner.ListWisps(ctx, filter)
	s.done(ctx, span, t, err)
	return v, err
}

// ── Streaming iterators ─────────────────────────────────────────────────────
//
// Iter* methods record a single span covering iterator CONSTRUCTION (the
// SQL query setup). Per-row work is NOT traced — the returned iterator is
// the inner store's iterator, unwrapped. Adding per-row tracing would
// require a wrapper type that ends a long-lived span on Close; that
// optimization is intentionally deferred until callers need it.

func (s *InstrumentedStorage) IterIssues(ctx context.Context, query string, filter types.IssueFilter) (storage.Iter[types.Issue], error) {
	ctx, span, t := s.op(ctx, "IterIssues")
	it, err := s.inner.IterIssues(ctx, query, filter)
	s.done(ctx, span, t, err)
	return it, err
}

func (s *InstrumentedStorage) IterDependentsWithMetadata(ctx context.Context, issueID string) (storage.Iter[types.IssueWithDependencyMetadata], error) {
	attrs := []attribute.KeyValue{attribute.String("bd.issue.id", issueID)}
	ctx, span, t := s.op(ctx, "IterDependentsWithMetadata", attrs...)
	it, err := s.inner.IterDependentsWithMetadata(ctx, issueID)
	s.done(ctx, span, t, err, attrs...)
	return it, err
}

func (s *InstrumentedStorage) IterDependenciesWithMetadata(ctx context.Context, issueID string) (storage.Iter[types.IssueWithDependencyMetadata], error) {
	attrs := []attribute.KeyValue{attribute.String("bd.issue.id", issueID)}
	ctx, span, t := s.op(ctx, "IterDependenciesWithMetadata", attrs...)
	it, err := s.inner.IterDependenciesWithMetadata(ctx, issueID)
	s.done(ctx, span, t, err, attrs...)
	return it, err
}

func (s *InstrumentedStorage) IterIssueComments(ctx context.Context, issueID string) (storage.Iter[types.Comment], error) {
	attrs := []attribute.KeyValue{attribute.String("bd.issue.id", issueID)}
	ctx, span, t := s.op(ctx, "IterIssueComments", attrs...)
	it, err := s.inner.IterIssueComments(ctx, issueID)
	s.done(ctx, span, t, err, attrs...)
	return it, err
}

func (s *InstrumentedStorage) IterEvents(ctx context.Context, issueID string, limit int) (storage.Iter[types.Event], error) {
	attrs := []attribute.KeyValue{attribute.String("bd.issue.id", issueID)}
	ctx, span, t := s.op(ctx, "IterEvents", attrs...)
	it, err := s.inner.IterEvents(ctx, issueID, limit)
	s.done(ctx, span, t, err, attrs...)
	return it, err
}

func (s *InstrumentedStorage) IterAllEventsSince(ctx context.Context, since time.Time) (storage.Iter[types.Event], error) {
	attrs := []attribute.KeyValue{attribute.String("bd.since", since.Format(time.RFC3339))}
	ctx, span, t := s.op(ctx, "IterAllEventsSince", attrs...)
	it, err := s.inner.IterAllEventsSince(ctx, since)
	s.done(ctx, span, t, err, attrs...)
	return it, err
}

func (s *InstrumentedStorage) IterReadyWork(ctx context.Context, filter types.WorkFilter) (storage.Iter[types.Issue], error) {
	ctx, span, t := s.op(ctx, "IterReadyWork")
	it, err := s.inner.IterReadyWork(ctx, filter)
	s.done(ctx, span, t, err)
	return it, err
}

func (s *InstrumentedStorage) IterBlockedIssues(ctx context.Context, filter types.WorkFilter) (storage.Iter[types.BlockedIssue], error) {
	ctx, span, t := s.op(ctx, "IterBlockedIssues")
	it, err := s.inner.IterBlockedIssues(ctx, filter)
	s.done(ctx, span, t, err)
	return it, err
}

func (s *InstrumentedStorage) IterWisps(ctx context.Context, filter types.WispFilter) (storage.Iter[types.Issue], error) {
	ctx, span, t := s.op(ctx, "IterWisps")
	it, err := s.inner.IterWisps(ctx, filter)
	s.done(ctx, span, t, err)
	return it, err
}

// ── Count* aggregates ─────────────────────────────────────────────────────────

func (s *InstrumentedStorage) CountIssues(ctx context.Context, query string, filter types.IssueFilter) (int64, error) {
	ctx, span, t := s.op(ctx, "CountIssues")
	v, err := s.inner.CountIssues(ctx, query, filter)
	s.done(ctx, span, t, err)
	return v, err
}

func (s *InstrumentedStorage) CountIssuesByGroup(ctx context.Context, filter types.IssueFilter, groupBy string) (map[string]int, error) {
	ctx, span, t := s.op(ctx, "CountIssuesByGroup")
	v, err := s.inner.CountIssuesByGroup(ctx, filter, groupBy)
	s.done(ctx, span, t, err)
	return v, err
}

func (s *InstrumentedStorage) CountDependents(ctx context.Context, issueID string) (int64, error) {
	ctx, span, t := s.op(ctx, "CountDependents", attribute.String("issue.id", issueID))
	v, err := s.inner.CountDependents(ctx, issueID)
	s.done(ctx, span, t, err)
	return v, err
}

func (s *InstrumentedStorage) CountDependencies(ctx context.Context, issueID string) (int64, error) {
	ctx, span, t := s.op(ctx, "CountDependencies", attribute.String("issue.id", issueID))
	v, err := s.inner.CountDependencies(ctx, issueID)
	s.done(ctx, span, t, err)
	return v, err
}

func (s *InstrumentedStorage) CountIssueComments(ctx context.Context, issueID string) (int64, error) {
	ctx, span, t := s.op(ctx, "CountIssueComments", attribute.String("issue.id", issueID))
	v, err := s.inner.CountIssueComments(ctx, issueID)
	s.done(ctx, span, t, err)
	return v, err
}

func (s *InstrumentedStorage) CountEvents(ctx context.Context, issueID string, limit int) (int64, error) {
	ctx, span, t := s.op(ctx, "CountEvents", attribute.String("issue.id", issueID))
	v, err := s.inner.CountEvents(ctx, issueID, limit)
	s.done(ctx, span, t, err)
	return v, err
}

// ── MergeSlot ────────────────────────────────────────────────────────────────

func (s *InstrumentedStorage) MergeSlotCreate(ctx context.Context, actor string) (*types.Issue, error) {
	ctx, span, t := s.op(ctx, "MergeSlotCreate")
	v, err := s.inner.MergeSlotCreate(ctx, actor)
	s.done(ctx, span, t, err)
	return v, err
}

func (s *InstrumentedStorage) MergeSlotCheck(ctx context.Context) (*storage.MergeSlotStatus, error) {
	ctx, span, t := s.op(ctx, "MergeSlotCheck")
	v, err := s.inner.MergeSlotCheck(ctx)
	s.done(ctx, span, t, err)
	return v, err
}

func (s *InstrumentedStorage) MergeSlotAcquire(ctx context.Context, holder, actor string, wait bool) (*storage.MergeSlotResult, error) {
	ctx, span, t := s.op(ctx, "MergeSlotAcquire", attribute.String("slot.holder", holder))
	v, err := s.inner.MergeSlotAcquire(ctx, holder, actor, wait)
	s.done(ctx, span, t, err)
	return v, err
}

func (s *InstrumentedStorage) MergeSlotRelease(ctx context.Context, holder, actor string) error {
	ctx, span, t := s.op(ctx, "MergeSlotRelease", attribute.String("slot.holder", holder))
	err := s.inner.MergeSlotRelease(ctx, holder, actor)
	s.done(ctx, span, t, err)
	return err
}

// ── Metadata slots ─────────────────────────────────────────────────────────

func (s *InstrumentedStorage) SlotSet(ctx context.Context, issueID, key, value, actor string) error {
	ctx, span, t := s.op(ctx, "SlotSet", attribute.String("slot.key", key))
	err := s.inner.SlotSet(ctx, issueID, key, value, actor)
	s.done(ctx, span, t, err)
	return err
}

func (s *InstrumentedStorage) SlotGet(ctx context.Context, issueID, key string) (string, error) {
	ctx, span, t := s.op(ctx, "SlotGet", attribute.String("slot.key", key))
	v, err := s.inner.SlotGet(ctx, issueID, key)
	s.done(ctx, span, t, err)
	return v, err
}

func (s *InstrumentedStorage) SlotClear(ctx context.Context, issueID, key, actor string) error {
	ctx, span, t := s.op(ctx, "SlotClear", attribute.String("slot.key", key))
	err := s.inner.SlotClear(ctx, issueID, key, actor)
	s.done(ctx, span, t, err)
	return err
}

func (s *InstrumentedStorage) MergeMetadata(ctx context.Context, issueID, key string, value json.RawMessage, actor string) error {
	ctx, span, t := s.op(ctx, "MergeMetadata", attribute.String("slot.key", key))
	err := s.inner.MergeMetadata(ctx, issueID, key, value, actor)
	s.done(ctx, span, t, err)
	return err
}

// ── Lifecycle ────────────────────────────────────────────────────────────────

func (s *InstrumentedStorage) Close() error {
	return s.inner.Close()
}

// Compile-time interface satisfaction.
var (
	_ storage.DoltStorage = (*InstrumentedStorage)(nil)
	_ storage.Unwrapper   = (*InstrumentedStorage)(nil)
)
