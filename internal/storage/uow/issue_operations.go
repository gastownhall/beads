package uow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/domain"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// issueOperations runs public issue mutations through a unit of work.
type issueOperations struct {
	provider UnitOfWorkProvider
}

// IssueLifecycleSource is the write-side twin of IssueReaderSource: the
// accessor a unit-of-work provider offers so a consumer holding a provider by
// interface asks for the role instead of reaching for a constructor.
type IssueLifecycleSource interface {
	IssueLifecycle() (publicops.Lifecycle, error)
}

// IssueLifecycle returns the guarded issue-lifecycle surface for this
// provider. A unit of work is not a special case: callers reach the verbs
// through the same accessor they use on a store.
func (p *doltSQLProvider) IssueLifecycle() (publicops.Lifecycle, error) {
	return NewIssueOperations(p)
}

// NewIssueOperations constructs public issue operations backed by provider.
func NewIssueOperations(provider UnitOfWorkProvider) (publicops.Lifecycle, error) {
	if isNilUnitOfWorkProvider(provider) {
		return nil, fmt.Errorf("new issue operations: unit-of-work provider must not be nil")
	}
	return &issueOperations{provider: provider}, nil
}

func isNilUnitOfWorkProvider(provider UnitOfWorkProvider) bool {
	if provider == nil {
		return true
	}
	value := reflect.ValueOf(provider)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

var _ publicops.Lifecycle = (*issueOperations)(nil)

// Create creates one issue in a retried unit-of-work transaction.
func (o *issueOperations) Create(ctx context.Context, request publicops.CreateRequest) (publicops.CreateResult, error) {
	snapshot := storageissueops.CloneCreateRequest(request)
	if err := storageissueops.ValidatePublicCreateRequest(snapshot); err != nil {
		return publicops.CreateResult{}, err
	}
	return RunTxResult(ctx, o.provider, func(ctx context.Context, uw UnitOfWork) (publicops.CreateResult, string, error) {
		attempt := storageissueops.CloneCreateRequest(snapshot)
		createContext, err := uw.ConfigUseCase().LoadCreateContext(ctx)
		if err != nil {
			return publicops.CreateResult{}, "", err
		}
		attempt, err = storageissueops.PreparePublicCreateRequest(attempt, storageissueops.PublicCreateContext{
			IssuePrefix:     createContext.IssuePrefix,
			AllowedPrefixes: createContext.AllowedPrefixes,
			CustomStatuses:  types.CustomStatusNames(createContext.CustomStatuses),
			CustomTypes:     createContext.CustomTypes,
		})
		if err != nil {
			return publicops.CreateResult{}, "", err
		}
		// Configured infra types live in the wisp tables, the same routing the
		// stores' own CreateIssue applies (internal/storage/dolt/issues.go) and
		// the public facade applies in issueops.ExecuteCreate. Mark the issue
		// before createParams reads it so ID minting and table routing agree on
		// the destination. A no-history create keeps its own retention mode and
		// already routes to wisps.
		if attempt.Issue != nil && !attempt.Issue.Ephemeral && !attempt.Issue.NoHistory && createContext.InfraTypes[string(attempt.Issue.IssueType)] {
			attempt.Issue.Ephemeral = true
		}
		params, useWisp, err := createParams(attempt)
		if err != nil {
			return publicops.CreateResult{}, "", validationError(err)
		}
		var created domain.CreateIssueResult
		if useWisp {
			created, err = uw.IssueUseCase().CreateWisp(ctx, params, attempt.Actor)
		} else {
			created, err = uw.IssueUseCase().CreateIssue(ctx, params, attempt.Actor)
		}
		if err != nil {
			return publicops.CreateResult{}, "", storageissueops.ClassifyPublicCreateError(err)
		}
		issue, err := hydrateIssueOperation(ctx, uw, created.Issue, true, false)
		if err != nil {
			return publicops.CreateResult{}, "", err
		}
		return publicops.CreateResult{Issue: issue}, "create issue", nil
	})
}

func createParams(request publicops.CreateRequest) (domain.CreateIssueParams, bool, error) {
	if request.Issue == nil {
		return domain.CreateIssueParams{}, false, fmt.Errorf("create: issue must not be nil")
	}
	issue := storageissueops.CloneCreateRequest(publicops.CreateRequest{Issue: request.Issue}).Issue
	params := domain.CreateIssueParams{
		Issue:                   issue,
		ExplicitID:              issue.ID,
		ParentID:                request.ParentID,
		Labels:                  append([]string(nil), issue.Labels...),
		InheritLabelsFromParent: request.InheritLabelsFromParent,
		ForcePrefix:             request.ForceIDPrefix,
		CreateOnly:              true,
	}
	for _, dependency := range request.Dependencies {
		params.Dependencies = append(params.Dependencies, domain.DependencySpec{
			Type:          dependency.Type,
			TargetID:      dependency.TargetID,
			SwapDirection: dependency.Reverse,
			Metadata:      dependency.Metadata,
			ThreadID:      dependency.ThreadID,
		})
	}
	if request.WaitsFor != nil {
		params.WaitsFor = &domain.WaitsForSpec{SpawnerID: request.WaitsFor.SpawnerID, Gate: request.WaitsFor.Gate}
	}
	return params, issue.Ephemeral || issue.NoHistory, nil
}

func hydrateIssueOperation(ctx context.Context, uw UnitOfWork, issue *types.Issue, includeComments, issuePlaneOnly bool) (*types.Issue, error) {
	if issue == nil {
		return nil, fmt.Errorf("hydrate issue operation: created issue is nil")
	}
	stored, useWisp, err := operationIssue(ctx, uw, issue.ID, issuePlaneOnly)
	if err != nil {
		return nil, fmt.Errorf("hydrate issue operation: %w", err)
	}
	clone := storageissueops.CloneCreateRequest(publicops.CreateRequest{Issue: stored}).Issue
	if !includeComments {
		clone.Comments = nil
	}
	labels := uw.LabelUseCase()
	if labels == nil {
		return nil, fmt.Errorf("hydrate issue labels: label use case is unavailable")
	}
	if useWisp {
		clone.Labels, err = labels.GetWispLabels(ctx, clone.ID)
	} else {
		clone.Labels, err = labels.GetLabels(ctx, clone.ID)
	}
	if err != nil {
		return nil, fmt.Errorf("hydrate issue labels: %w", err)
	}

	dependencies := uw.DependencyUseCase()
	if dependencies == nil {
		return nil, fmt.Errorf("hydrate issue dependencies: dependency use case is unavailable")
	}
	var records map[string][]*types.Dependency
	if useWisp {
		records, err = dependencies.GetWispDependencyRecords(ctx, []string{clone.ID})
	} else {
		records, err = dependencies.GetIssueDependencyRecords(ctx, []string{clone.ID})
	}
	if err != nil {
		return nil, fmt.Errorf("hydrate issue dependencies: %w", err)
	}
	clone.Dependencies = records[clone.ID]

	if includeComments {
		comments := uw.CommentUseCase()
		if comments == nil {
			return nil, fmt.Errorf("hydrate issue comments: comment use case is unavailable")
		}
		if useWisp {
			clone.Comments, err = comments.GetCommentsForWisp(ctx, clone.ID)
		} else {
			clone.Comments, err = comments.GetCommentsForIssue(ctx, clone.ID)
		}
		if err != nil {
			return nil, fmt.Errorf("hydrate issue comments: %w", err)
		}
	}
	return storageissueops.CloneCreateRequest(publicops.CreateRequest{Issue: clone}).Issue, nil
}

// Update updates one issue in a retried unit-of-work transaction.
func (o *issueOperations) Update(ctx context.Context, request publicops.UpdateRequest) (publicops.UpdateResult, error) {
	snapshot := storageissueops.CloneUpdateRequest(request)
	if err := validateUpdateRequest(snapshot); err != nil {
		return publicops.UpdateResult{}, err
	}
	return RunTxResult(ctx, o.provider, func(ctx context.Context, uw UnitOfWork) (publicops.UpdateResult, string, error) {
		attempt := storageissueops.CloneUpdateRequest(snapshot)
		spec, err := updateSpec(attempt)
		if err != nil {
			return publicops.UpdateResult{}, "", validationError(err)
		}
		before, _, err := operationIssue(ctx, uw, attempt.IssueID, attempt.IssuePlaneOnly)
		if err != nil {
			return publicops.UpdateResult{}, "", err
		}
		before, err = hydrateIssueOperation(ctx, uw, before, false, attempt.IssuePlaneOnly)
		if err != nil {
			return publicops.UpdateResult{}, "", err
		}
		// The assignee fence belongs here, over the same-transaction row the
		// update is about to mutate, and not in ApplyUpdate: callers build their
		// own UpdateSpec, which carries no override, so a fence down there
		// silently ignores a caller's --force. A stale compare-and-set
		// precondition outranks the fence on the other backends, which check
		// version and status before authorizing, so when one is stale the fence
		// stands down and ApplyUpdate reports the mismatch instead.
		if updatePreconditionsHold(attempt, before) {
			if err := authorizeAssigneeTransfer(ctx, uw, before, attempt); err != nil {
				return publicops.UpdateResult{}, "", err
			}
		}
		// ActorMatches (not a verbatim compare, ga-v2k49): a holder re-claiming
		// under a respelled identity is a real CAS win one layer down (domain/db's
		// Claim already canonicalizes), so this bookkeeping must recognize it as
		// the same no-op rather than staging a phantom history entry.
		claimChanged := attempt.Claim && (before.Status != types.StatusInProgress || !storageissueops.ActorMatches(before.Assignee, attempt.Actor))
		updated, err := uw.IssueUseCase().ApplyUpdate(ctx, attempt.IssueID, spec, attempt.Actor)
		if err != nil {
			return publicops.UpdateResult{}, "", err
		}
		issue, err := hydrateIssueOperation(ctx, uw, updated, false, attempt.IssuePlaneOnly)
		if err != nil {
			return publicops.UpdateResult{}, "", err
		}
		changed := claimChanged || !semanticIssueEqual(before, issue)
		return publicops.UpdateResult{Issue: issue, Changed: changed}, updateHistoryEntry(attempt, changed), nil
	})
}

// updatePreconditionsHold reports whether request's compare-and-set
// preconditions match before — that is, whether ApplyUpdate would get past its
// own guards, leaving the assignee fence as the operative refusal.
// ExpectedAssignee needs no entry here: the fence already stands down whenever
// a caller supplies one.
func updatePreconditionsHold(request publicops.UpdateRequest, before *types.Issue) bool {
	if request.ExpectedVersion != nil && *request.ExpectedVersion != before.RowVersion {
		return false
	}
	if request.ExpectedStatus != nil && *request.ExpectedStatus != before.Status {
		return false
	}
	return true
}

// authorizeAssigneeTransfer applies the shared assignee-transfer fence to an
// update running in this unit of work. It is the sibling of the
// issueops.AuthorizeAssigneeTransfer call in ExecuteUpdate — the same predicate
// and the same refusal — reached through the config use case because a unit of
// work has no transaction handle to read claim.pools from. The read only
// happens once the transfer is otherwise fenced, and its failure propagates
// rather than being read as "no pools configured": treating an unreadable
// config as an empty alias set would refuse pool work the fence was never meant
// to touch.
func authorizeAssigneeTransfer(ctx context.Context, uw UnitOfWork, before *types.Issue, request publicops.UpdateRequest) error {
	if err := storageissueops.AuthorizeAssigneeTransferWithPools(before, request, nil); err == nil {
		return nil
	}
	raw, err := uw.ConfigUseCase().GetConfig(ctx, "claim.pools")
	if err != nil {
		return fmt.Errorf("authorize assignee transfer %s: read claim pools: %w", request.IssueID, err)
	}
	return storageissueops.AuthorizeAssigneeTransferWithPools(before, request, storageissueops.ParseClaimPools(raw))
}

func updateSpec(request publicops.UpdateRequest) (domain.UpdateSpec, error) {
	fields := make(map[string]any)
	patch := request.Patch
	if err := validateMetadataPatch(patch.Metadata); err != nil {
		return domain.UpdateSpec{}, validationError(err)
	}
	setField(fields, "title", patch.Title)
	setField(fields, "description", patch.Description)
	setField(fields, "design", patch.Design)
	setField(fields, "acceptance_criteria", patch.AcceptanceCriteria)
	setField(fields, "spec_id", patch.SpecID)
	setField(fields, "await_id", patch.AwaitID)
	setField(fields, "status", patch.Status)
	setField(fields, "priority", patch.Priority)
	if patch.IssueType.Set {
		fields["issue_type"] = string(patch.IssueType.Value)
	}
	setField(fields, "assignee", patch.Assignee)
	setField(fields, "owner", patch.Owner)
	setField(fields, "closed_by_session", patch.ClosedBySession)
	setField(fields, "estimated_minutes", patch.EstimatedMinutes)
	setField(fields, "external_ref", patch.ExternalRef)
	setField(fields, "due_at", patch.DueAt)
	setField(fields, "defer_until", patch.DeferUntil)
	if patch.Notes.Set && patch.AppendNotes.Set {
		return domain.UpdateSpec{}, validationError(fmt.Errorf("update: notes and append notes cannot both be set"))
	}
	setField(fields, "notes", patch.Notes)
	if patch.AppendNotes.Set {
		fields[storageissueops.OpAppendNotes] = patch.AppendNotes.Value
	}
	if patch.Metadata.Replace.Set {
		replacement := json.RawMessage("{}")
		if len(patch.Metadata.Replace.Value) > 0 {
			replacement = patch.Metadata.Replace.Value
		}
		if err := storageissueops.ValidateMetadataIfConfigured(replacement); err != nil {
			return domain.UpdateSpec{}, validationError(err)
		}
		fields["metadata"] = replacement
	} else {
		if patch.Metadata.Merge.Set {
			fields[storageissueops.OpMergeMetadata] = patch.Metadata.Merge.Value
		}
		if len(patch.Metadata.Set) > 0 {
			fields[storageissueops.OpSetMetadata] = patch.Metadata.Set
		}
		if len(patch.Metadata.Unset) > 0 {
			fields[storageissueops.OpUnsetMetadata] = patch.Metadata.Unset
		}
	}
	// Spelled only alongside a status change, matching the embedded backend:
	// without one the funnel has no crossing to judge.
	if request.ForceClosePolicy && patch.Status.Set {
		fields[storageissueops.OpForceClosePolicy] = true
	}
	var persistence *types.PersistenceMode
	if patch.Persistence.Set {
		value := patch.Persistence.Value
		persistence = &value
	}
	var setLabels *([]string)
	if patch.Labels.Replace.Set {
		labels := append([]string(nil), patch.Labels.Replace.Value...)
		setLabels = &labels
	}
	return domain.UpdateSpec{
		Fields:           fields,
		Claim:            request.Claim,
		ExpectedVersion:  request.ExpectedVersion,
		ExpectedAssignee: request.ExpectedAssignee,
		ExpectedStatus:   statusPointer(request.ExpectedStatus),
		Persistence:      persistence,
		AddLabels:        append([]string(nil), patch.Labels.Add...),
		RemoveLabels:     append([]string(nil), patch.Labels.Remove...),
		SetLabels:        setLabels,
		Reparent:         fieldStringPointer(patch.ParentID),
	}, nil
}

func setField[T any](fields map[string]any, name string, field publicops.Field[T]) {
	if field.Set {
		fields[name] = field.Value
	}
}

func fieldStringPointer(field publicops.Field[string]) *string {
	if !field.Set {
		return nil
	}
	value := field.Value
	return &value
}

func statusPointer(status *publicops.Status) *string {
	if status == nil {
		return nil
	}
	value := string(*status)
	return &value
}

// Close closes one issue in a retried unit-of-work transaction.
func (o *issueOperations) Close(ctx context.Context, request publicops.CloseRequest) (publicops.CloseResult, error) {
	snapshot := storageissueops.CloneCloseRequest(request)
	if err := validateCloseRequest(snapshot); err != nil {
		return publicops.CloseResult{}, err
	}
	return RunTxResult(ctx, o.provider, func(ctx context.Context, uw UnitOfWork) (publicops.CloseResult, string, error) {
		attempt := storageissueops.CloneCloseRequest(snapshot)
		issue, useWisp, err := operationIssue(ctx, uw, attempt.IssueID, false)
		if err != nil {
			return publicops.CloseResult{}, "", err
		}
		if attempt.ExpectedVersion != nil {
			if _, err := uw.IssueUseCase().ApplyUpdate(ctx, attempt.IssueID, domain.UpdateSpec{ExpectedVersion: attempt.ExpectedVersion}, attempt.Actor); err != nil {
				return publicops.CloseResult{}, "", err
			}
		}
		before, err := hydrateIssueOperation(ctx, uw, issue, false, false)
		if err != nil {
			return publicops.CloseResult{}, "", err
		}
		var closed domain.CloseIssueResult
		if useWisp {
			closed, err = uw.IssueUseCase().CloseWispChecked(ctx, attempt.IssueID, domain.CloseIssueParams{Reason: attempt.Reason, Session: attempt.Session}, attempt.Actor, attempt.Force)
		} else {
			closed, err = uw.IssueUseCase().CloseIssueChecked(ctx, attempt.IssueID, domain.CloseIssueParams{Reason: attempt.Reason, Session: attempt.Session}, attempt.Actor, attempt.Force)
		}
		if err != nil {
			return publicops.CloseResult{}, "", err
		}
		if closed.Issue != nil {
			issue = closed.Issue
		}
		hydrated, err := hydrateIssueOperation(ctx, uw, issue, false, false)
		if err != nil {
			return publicops.CloseResult{}, "", err
		}
		return publicops.CloseResult{Issue: hydrated, Changed: !semanticIssueEqual(before, hydrated), OpenChildren: closed.OpenChildren},
			"close issue", nil
	})
}

// Reopen reopens one issue in a retried unit-of-work transaction.
func (o *issueOperations) Reopen(ctx context.Context, request publicops.ReopenRequest) (publicops.ReopenResult, error) {
	snapshot := storageissueops.CloneReopenRequest(request)
	if err := validateReopenRequest(snapshot); err != nil {
		return publicops.ReopenResult{}, err
	}
	return RunTxResult(ctx, o.provider, func(ctx context.Context, uw UnitOfWork) (publicops.ReopenResult, string, error) {
		attempt := storageissueops.CloneReopenRequest(snapshot)
		issue, useWisp, err := operationIssue(ctx, uw, attempt.IssueID, false)
		if err != nil {
			return publicops.ReopenResult{}, "", err
		}
		if attempt.ExpectedVersion != nil {
			if _, err := uw.IssueUseCase().ApplyUpdate(ctx, attempt.IssueID, domain.UpdateSpec{ExpectedVersion: attempt.ExpectedVersion}, attempt.Actor); err != nil {
				return publicops.ReopenResult{}, "", err
			}
		}
		before, err := hydrateIssueOperation(ctx, uw, issue, false, false)
		if err != nil {
			return publicops.ReopenResult{}, "", err
		}
		var reopened domain.ReopenIssueResult
		if useWisp {
			reopened, err = uw.IssueUseCase().ReopenWisp(ctx, attempt.IssueID, domain.ReopenIssueParams{Reason: attempt.Reason}, attempt.Actor)
		} else {
			reopened, err = uw.IssueUseCase().ReopenIssue(ctx, attempt.IssueID, domain.ReopenIssueParams{Reason: attempt.Reason}, attempt.Actor)
		}
		if err != nil {
			return publicops.ReopenResult{}, "", err
		}
		if reopened.Issue != nil {
			issue = reopened.Issue
		}
		hydrated, err := hydrateIssueOperation(ctx, uw, issue, false, false)
		if err != nil {
			return publicops.ReopenResult{}, "", err
		}
		return publicops.ReopenResult{Issue: hydrated, Changed: !semanticIssueEqual(before, hydrated)},
			storageissueops.HistoryEntry(attempt.Provenance, "reopen issue"), nil
	})
}

// updateHistoryEntry names the history entry an update records, or "" for the
// one update that records none.
//
// A request that asks for a CLAIM AND NOTHING ELSE and changed nothing wrote
// nothing: the compare-and-set matched no row, and the idempotent branch it
// took grants no lease and records no event. Naming an entry for it would let a
// polling caller mint one per call. Every other update names one even when its
// post-state compares equal, because a same-value write still touches the row.
func updateHistoryEntry(request publicops.UpdateRequest, changed bool) string {
	if !changed && request.Claim && reflect.DeepEqual(request.Patch, publicops.IssuePatch{}) {
		return ""
	}
	return storageissueops.HistoryEntry(request.Provenance, "update issue")
}

// operationIssue resolves id to the row an operation is about. Both planes are
// searched unless issuePlaneOnly restricts it, in which case a wisp id is a
// miss rather than an ephemeral row to operate on. Every call runs inside the
// operation's own transaction.
func operationIssue(ctx context.Context, uw UnitOfWork, id string, issuePlaneOnly bool) (*types.Issue, bool, error) {
	if !issuePlaneOnly {
		issue, err := uw.IssueUseCase().GetWisp(ctx, id)
		if err == nil && issue != nil {
			return issue, true, nil
		}
		if err != nil && !errors.Is(err, publicops.ErrNotFound) && !dberrors.IsNoRows(err) {
			return nil, false, fmt.Errorf("read wisp %s: %w", id, err)
		}
	}
	issue, err := uw.IssueUseCase().GetIssue(ctx, id)
	if err != nil {
		if errors.Is(err, publicops.ErrNotFound) || dberrors.IsNoRows(err) {
			return nil, false, fmt.Errorf("%w: issue %s", publicops.ErrNotFound, id)
		}
		return nil, false, err
	}
	if issue == nil {
		return nil, false, fmt.Errorf("%w: issue %s", publicops.ErrNotFound, id)
	}
	return issue, false, nil
}

func validationError(err error) error {
	if errors.Is(err, publicops.ErrValidation) {
		return err
	}
	return fmt.Errorf("%w: %w", publicops.ErrValidation, err)
}

func validateUpdateRequest(request publicops.UpdateRequest) error {
	if request.Actor == "" || request.IssueID == "" {
		return validationError(fmt.Errorf("update: actor and issue ID must not be empty"))
	}
	if err := storageissueops.ValidateUpdateRequest(request); err != nil {
		return validationError(err)
	}
	if err := validateMetadataPatch(request.Patch.Metadata); err != nil {
		return validationError(err)
	}
	return nil
}

func validateCloseRequest(request publicops.CloseRequest) error {
	if request.Actor == "" || request.IssueID == "" {
		return validationError(fmt.Errorf("close: actor and issue ID must not be empty"))
	}
	return nil
}

func validateMetadataPatch(metadata publicops.MetadataPatch) error {
	if metadata.Replace.Set && (metadata.Merge.Set || len(metadata.Set) > 0 || len(metadata.Unset) > 0) {
		return fmt.Errorf("metadata replacement cannot combine with incremental edits")
	}
	if metadata.Replace.Set && len(metadata.Replace.Value) > 0 && !json.Valid(metadata.Replace.Value) {
		return fmt.Errorf("metadata replacement is not valid JSON")
	}
	if metadata.Merge.Set {
		var object map[string]json.RawMessage
		if len(metadata.Merge.Value) == 0 || json.Unmarshal(metadata.Merge.Value, &object) != nil || object == nil {
			return fmt.Errorf("metadata merge must be a JSON object")
		}
	}
	keys := make([]string, 0, len(metadata.Set))
	for key := range metadata.Set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := storage.ValidateMetadataKey(key); err != nil {
			return err
		}
		if !json.Valid(metadata.Set[key]) {
			return fmt.Errorf("metadata value for key %q is not valid JSON", key)
		}
	}
	for _, key := range metadata.Unset {
		if err := storage.ValidateMetadataKey(key); err != nil {
			return err
		}
	}
	return nil
}

func validateReopenRequest(request publicops.ReopenRequest) error {
	if request.Actor == "" || request.IssueID == "" {
		return validationError(fmt.Errorf("reopen: actor and issue ID must not be empty"))
	}
	return nil
}

func semanticIssueEqual(left, right *types.Issue) bool {
	if left == nil || right == nil {
		return left == right
	}
	leftCopy := *left
	rightCopy := *right
	leftCopy.UpdatedAt, rightCopy.UpdatedAt = time.Time{}, time.Time{}
	leftCopy.RowVersion, rightCopy.RowVersion = 0, 0
	return reflect.DeepEqual(leftCopy, rightCopy)
}
