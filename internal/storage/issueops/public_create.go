package issueops

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// PublicCreateContext holds the configuration required to prepare a public
// create request for the domain create use case.
type PublicCreateContext struct {
	IssuePrefix     string
	AllowedPrefixes string
	CustomStatuses  []string
	CustomTypes     []string
}

// ValidatePublicCreateRequest checks public-create invariants independent of
// database configuration.
func ValidatePublicCreateRequest(request publicops.CreateRequest) error {
	if request.Actor == "" || request.Issue == nil {
		return publicCreateValidationError(fmt.Errorf("create: actor and issue are required"))
	}
	if err := types.CheckFieldLen("actor", request.Actor); err != nil {
		return publicCreateValidationError(fmt.Errorf("create: %w", err))
	}
	if len(request.Issue.Comments) > 0 || len(request.Issue.Dependencies) > 0 {
		return publicCreateValidationError(fmt.Errorf("create: issue comments and dependencies must be supplied through request fields"))
	}
	for _, field := range []struct{ name, value string }{{"assignee", request.Issue.Assignee}, {"owner", request.Issue.Owner}} {
		if err := types.CheckFieldLen(field.name, field.value); err != nil {
			return publicCreateValidationError(err)
		}
	}
	for _, label := range request.Issue.Labels {
		if err := types.CheckFieldLen("label", label); err != nil {
			return publicCreateValidationError(err)
		}
	}
	if err := types.CheckFieldLen("parent ID", request.ParentID); err != nil {
		return publicCreateValidationError(fmt.Errorf("create: %w", err))
	}
	return validatePublicCreateDependencies(request)
}

// PreparePublicCreateRequest snapshots, normalizes, and validates a public
// create request using the supplied configuration.
func PreparePublicCreateRequest(request publicops.CreateRequest, context PublicCreateContext) (publicops.CreateRequest, error) {
	request = CloneCreateRequest(request)
	if err := ValidatePublicCreateRequest(request); err != nil {
		return publicops.CreateRequest{}, err
	}
	issue := publicCreateIssue(request.Issue)
	if issue.Status == "" {
		issue.Status = types.StatusOpen
	}
	if issue.ID != "" && !request.ForceIDPrefix {
		// The caller's prefix wins when it supplied one; see
		// CreateRequest.IDPrefix for why a front door may know better than the
		// substrate does.
		prefix := context.IssuePrefix
		if request.IDPrefix != "" {
			prefix = request.IDPrefix
		}
		if err := ValidateIssueIDPrefix(issue.ID, strings.TrimSuffix(prefix, "-"), context.AllowedPrefixes); err != nil {
			return publicops.CreateRequest{}, publicCreateValidationError(err)
		}
	}
	if err := PrepareIssueForInsert(issue, context.CustomStatuses, context.CustomTypes); err != nil {
		return publicops.CreateRequest{}, publicCreateValidationError(err)
	}
	prepared := request
	prepared.Issue = issue
	if prepared.WaitsFor != nil && prepared.WaitsFor.Gate == "" {
		prepared.WaitsFor.Gate = string(types.WaitsForAllChildren)
	}
	if err := ValidatePublicCreateRequest(prepared); err != nil {
		return publicops.CreateRequest{}, err
	}
	return prepared, nil
}

// ClassifyPublicCreateError adds ErrValidation only to known deterministic
// public-create failures and leaves infrastructure and commit errors intact.
func ClassifyPublicCreateError(err error) error {
	if err == nil || errors.Is(err, storage.ErrValidation) || errors.Is(err, storage.ErrAlreadyExists) {
		return err
	}
	var conflict *domain.DependencyTypeConflictError
	var hierarchyConflict *domain.DependencyHierarchyConflictError
	var stateErr interface{ SQLState() string }
	if errors.As(err, &stateErr) && stateErr.SQLState() == "23505" {
		return fmt.Errorf("%w: %w", storage.ErrAlreadyExists, err)
	}
	if errors.Is(err, storage.ErrPrefixMismatch) || errors.Is(err, domain.ErrSelfDependency) || errors.Is(err, types.ErrFieldTooLong) || errors.Is(err, domain.ErrDependencyCycle) || errors.As(err, &conflict) || errors.As(err, &hierarchyConflict) {
		return publicCreateValidationError(err)
	}
	// A create whose requested relationship names a row that does not exist is
	// refused by the dependency write: as the typed endpoint refusal where the
	// write could name the absent endpoint, and as the target foreign key where
	// it could not. The caller asked for an edge to something absent, so this
	// is a deterministic refusal rather than an infrastructure error: classify
	// it the same way ExecuteCreate refuses a skipped dependency, so every
	// backend reports a missing dependency, parent, or waits-for target as
	// ErrValidation wrapping ErrNotFound.
	var missingEndpoint *domain.DependencyEndpointNotFoundError
	if errors.As(err, &missingEndpoint) || dberrors.IsMissingForeignKeyTarget(err) {
		return publicCreateValidationError(fmt.Errorf("create: dependency target does not exist: %w: %w", err, storage.ErrNotFound))
	}
	return err
}

func publicCreateValidationError(err error) error {
	return fmt.Errorf("%w: %w", storage.ErrValidation, err)
}

func publicCreateIssue(source *types.Issue) *types.Issue {
	return &types.Issue{
		ID: source.ID, Title: source.Title, Description: source.Description, Design: source.Design,
		AcceptanceCriteria: source.AcceptanceCriteria, Notes: source.Notes, SpecID: source.SpecID,
		Status: source.Status, Priority: source.Priority, IssueType: source.IssueType,
		Assignee: source.Assignee, Owner: source.Owner, EstimatedMinutes: cloneInt(source.EstimatedMinutes),
		CreatedAt: source.CreatedAt, CreatedBy: source.CreatedBy, UpdatedAt: source.UpdatedAt,
		StartedAt: cloneTime(source.StartedAt), ClosedAt: cloneTime(source.ClosedAt), CloseReason: source.CloseReason, ClosedBySession: source.ClosedBySession,
		DueAt: cloneTime(source.DueAt), DeferUntil: cloneTime(source.DeferUntil), ExternalRef: cloneString(source.ExternalRef), SourceSystem: source.SourceSystem, SourceRepo: source.SourceRepo,
		Metadata: cloneRawMessage(source.Metadata), Labels: append([]string(nil), source.Labels...), Sender: source.Sender,
		Ephemeral: source.Ephemeral, NoHistory: source.NoHistory, WispType: source.WispType, StorageClass: source.StorageClass,
		Pinned: source.Pinned, IsTemplate: source.IsTemplate, BondedFrom: append([]types.BondRef(nil), source.BondedFrom...),
		AwaitType: source.AwaitType, AwaitID: source.AwaitID, Timeout: source.Timeout, Waiters: append([]string(nil), source.Waiters...),
		SourceFormula: source.SourceFormula, SourceLocation: source.SourceLocation, MolType: source.MolType, WorkType: source.WorkType,
		EventKind: source.EventKind, Actor: source.Actor, Target: source.Target, Payload: source.Payload,
	}
}

func validatePublicCreateDependencies(request publicops.CreateRequest) error {
	type endpoint struct {
		newIssue bool
		id       string
	}
	type edge struct {
		source endpoint
		target endpoint
		typ    types.DependencyType
	}
	newIssue := endpoint{newIssue: true}
	endpointFor := func(id string) endpoint {
		if request.Issue.ID != "" && id == request.Issue.ID {
			return newIssue
		}
		return endpoint{id: id}
	}
	edges := make([]edge, 0, 1+len(request.Dependencies)+1)
	if request.ParentID != "" {
		edges = append(edges, edge{newIssue, endpointFor(request.ParentID), types.DepParentChild})
	}
	for index, dependency := range request.Dependencies {
		if err := types.CheckFieldLen("dependency target ID", dependency.TargetID); err != nil {
			return publicCreateValidationError(fmt.Errorf("create: dependency %d: %w", index, err))
		}
		if dependency.TargetID == "" || !dependency.Type.IsValid() {
			return publicCreateValidationError(fmt.Errorf("create: dependency target and type are required"))
		}
		if dependency.Metadata != "" && !json.Valid([]byte(dependency.Metadata)) {
			return publicCreateValidationError(fmt.Errorf("create: dependency %d metadata must be valid JSON", index))
		}
		if err := types.CheckFieldLen("dependency thread_id", dependency.ThreadID); err != nil {
			return publicCreateValidationError(fmt.Errorf("create: dependency %d: %w", index, err))
		}
		from, to := newIssue, endpointFor(dependency.TargetID)
		if dependency.Reverse {
			from, to = to, from
		}
		edges = append(edges, edge{from, to, dependency.Type})
	}
	if request.WaitsFor != nil {
		if err := types.CheckFieldLen("waits-for spawner ID", request.WaitsFor.SpawnerID); err != nil {
			return publicCreateValidationError(fmt.Errorf("create: %w", err))
		}
		if request.WaitsFor.SpawnerID == "" || (request.WaitsFor.Gate != "" && request.WaitsFor.Gate != string(types.WaitsForAllChildren) && request.WaitsFor.Gate != string(types.WaitsForAnyChildren)) {
			return publicCreateValidationError(fmt.Errorf("create: waits-for spawner and gate are invalid"))
		}
		edges = append(edges, edge{newIssue, endpointFor(request.WaitsFor.SpawnerID), types.DepWaitsFor})
	}
	type edgeKey struct {
		source endpoint
		target endpoint
	}
	seen := map[edgeKey]edge{}
	for _, edge := range edges {
		if edge.source == edge.target {
			return publicCreateValidationError(fmt.Errorf("%w: %s", domain.ErrSelfDependency, edge.source.id))
		}
		key := edgeKey{source: edge.source, target: edge.target}
		if previous, ok := seen[key]; ok {
			if previous.typ == edge.typ {
				return publicCreateValidationError(fmt.Errorf("create: duplicate dependency %s -> %s", edge.source.id, edge.target.id))
			}
			return publicCreateValidationError(&domain.DependencyTypeConflictError{IssueID: edge.source.id, DependsOnID: edge.target.id, ExistingType: string(previous.typ), RequestedType: string(edge.typ)})
		}
		seen[key] = edge
	}
	return nil
}
