package issueops

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// ChangedTables records durable tables changed by one guarded operation.
type ChangedTables map[string]bool

// Add records table names, ignoring empty names and wisp-only tables.
func (tables ChangedTables) Add(names ...string) {
	for _, name := range names {
		if name != "" && name != "wisps" && !strings.HasPrefix(name, "wisp_") {
			tables[name] = true
		}
	}
}

// Merge records every table in other.
func (tables ChangedTables) Merge(other map[string]bool) {
	for table := range other {
		tables.Add(table)
	}
}

// UpdateFields converts the public scalar patch fields to storage updates.
func UpdateFields(patch publicops.IssuePatch) map[string]interface{} {
	updates := map[string]interface{}{}
	for _, field := range []struct {
		set bool
		key string
		val interface{}
	}{
		{patch.Title.Set, "title", patch.Title.Value},
		{patch.Description.Set, "description", patch.Description.Value},
		{patch.Design.Set, "design", patch.Design.Value},
		{patch.AcceptanceCriteria.Set, "acceptance_criteria", patch.AcceptanceCriteria.Value},
		{patch.Notes.Set, "notes", patch.Notes.Value},
		{patch.AppendNotes.Set, "append_notes", patch.AppendNotes.Value},
		{patch.SpecID.Set, "spec_id", patch.SpecID.Value},
		{patch.AwaitID.Set, "await_id", patch.AwaitID.Value},
		{patch.Status.Set, "status", patch.Status.Value},
		{patch.Priority.Set, "priority", patch.Priority.Value},
		{patch.IssueType.Set, "issue_type", patch.IssueType.Value},
		{patch.Assignee.Set, "assignee", patch.Assignee.Value},
		{patch.Owner.Set, "owner", patch.Owner.Value},
		{patch.ClosedBySession.Set, "closed_by_session", patch.ClosedBySession.Value},
		{patch.EstimatedMinutes.Set, "estimated_minutes", patch.EstimatedMinutes.Value},
		{patch.ExternalRef.Set, "external_ref", patch.ExternalRef.Value},
		{patch.DueAt.Set, "due_at", patch.DueAt.Value},
		{patch.DeferUntil.Set, "defer_until", patch.DeferUntil.Value},
	} {
		if field.set {
			updates[field.key] = field.val
		}
	}
	return updates
}

// ValidateUpdateRequest checks mutually exclusive guarded-update options and
// the canonical field values every backend must reject identically. Backends
// call it before touching the row so an invalid patch cannot half-apply.
func ValidateUpdateRequest(request publicops.UpdateRequest) error {
	if request.Claim && (request.ExpectedAssignee != nil || request.ExpectedStatus != nil) {
		return fmt.Errorf("%w: claim cannot use expected assignee or status", storage.ErrValidation)
	}
	if request.ForceAssigneeTransfer && (request.Claim || !request.Patch.Assignee.Set || request.ExpectedAssignee != nil) {
		return fmt.Errorf("%w: invalid forced assignee transfer", storage.ErrValidation)
	}
	patch := request.Patch
	if patch.Title.Set {
		if err := types.ValidateIssueTitle(patch.Title.Value); err != nil {
			return fmt.Errorf("%w: update title: %w", storage.ErrValidation, err)
		}
	}
	if patch.Priority.Set {
		if err := types.ValidateIssuePriority(patch.Priority.Value); err != nil {
			return fmt.Errorf("%w: update priority: %w", storage.ErrValidation, err)
		}
	}
	if patch.EstimatedMinutes.Set {
		if err := types.ValidateIssueEstimatedMinutes(patch.EstimatedMinutes.Value); err != nil {
			return fmt.Errorf("%w: update estimated_minutes: %w", storage.ErrValidation, err)
		}
	}
	if patch.Persistence.Set && !patch.Persistence.Value.IsValid() {
		return fmt.Errorf("%w: invalid persistence mode %q", storage.ErrValidation, patch.Persistence.Value)
	}
	return nil
}

// ValidateMetadataPatch checks mutually exclusive metadata edits.
func ValidateMetadataPatch(patch publicops.MetadataPatch) error {
	if patch.Replace.Set && (patch.Merge.Set || len(patch.Set) > 0 || len(patch.Unset) > 0) {
		return fmt.Errorf("%w: cannot combine metadata replacement with incremental metadata edits", storage.ErrValidation)
	}
	return nil
}

// ValidateScalarUpdates checks typed scalar values before they reach SQL.
func ValidateScalarUpdates(ctx context.Context, tx DBTX, updates map[string]interface{}) error {
	if rawType, ok := updates["issue_type"]; ok {
		var issueType types.IssueType
		switch value := rawType.(type) {
		case types.IssueType:
			issueType = value
		case string:
			issueType = types.IssueType(value)
		default:
			return fmt.Errorf("%w: invalid issue type %v", storage.ErrValidation, rawType)
		}
		customTypes, err := ResolveCustomTypesInTx(ctx, tx)
		if err != nil {
			return fmt.Errorf("resolve custom issue types: %w", err)
		}
		if !issueType.IsValidWithCustom(customTypes) {
			return fmt.Errorf("%w: invalid issue type %s", storage.ErrValidation, issueType)
		}
	}
	for _, field := range []string{"assignee", "owner"} {
		if raw, ok := updates[field]; ok {
			if value, ok := raw.(string); ok {
				if err := types.CheckFieldLen(field, value); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// AuthorizeAssigneeTransferWithPools is the assignee-transfer fence itself,
// with the pool aliases supplied rather than read. It is the single source of
// the predicate: the DBTX path below and the unit-of-work backend, which
// reaches config through a use case rather than a transaction, both evaluate
// it, so the two cannot drift into disagreeing about which transfers are
// fenced.
//
// The no-op and self-transfer exits (request.Patch.Assignee.Value against
// before.Assignee, and before.Assignee against request.Actor) are judged
// under actorMatches, not verbatim equality (ga-5ksp5): the same Gas Town
// identity arrives here spelled differently depending on which layer
// produced the string (a dotted alias vs its session-name form — see
// canonicalActor), and a byte-for-byte comparison would wrongly fence a
// holder editing its own claim, or reject an idempotent re-assert, just
// because the caller named the holder under a different layer's spelling.
//
// Passing nil pools answers every question except pool membership, so a caller
// that wants the config read only when it matters calls with nil first and
// re-evaluates with the loaded aliases on refusal.
func AuthorizeAssigneeTransferWithPools(before *types.Issue, request publicops.UpdateRequest, pools []string) error {
	if !request.Patch.Assignee.Set || actorMatches(request.Patch.Assignee.Value, before.Assignee) || request.ExpectedAssignee != nil || request.ForceAssigneeTransfer || before.Status != types.StatusInProgress || before.Assignee == "" || actorMatches(before.Assignee, request.Actor) {
		return nil
	}
	// Exact-string membership, deliberately not actorMatches (ga-v2k49, same
	// reason as claim.go's identical pool checks): a pool alias is a literal
	// claim.pools config value, not a Gas Town identity that gets respelled
	// per layer, so there is no cross-spelling variant to reconcile.
	for _, pool := range pools {
		if pool == before.Assignee {
			return nil
		}
	}
	return fmt.Errorf("%w: issue %s is assigned to %q", storage.ErrAlreadyClaimed, before.ID, before.Assignee)
}

// AuthorizeAssigneeTransfer protects an active assignment from unguarded transfer.
func AuthorizeAssigneeTransfer(ctx context.Context, tx DBTX, before *types.Issue, request publicops.UpdateRequest) error {
	if err := AuthorizeAssigneeTransferWithPools(before, request, nil); err == nil {
		return nil
	}
	pools, err := ClaimPoolAliasesInTx(ctx, tx)
	if err != nil {
		return err
	}
	return AuthorizeAssigneeTransferWithPools(before, request, pools)
}

// ApplyMetadataPatch returns the canonical metadata value and whether it changes.
func ApplyMetadataPatch(current json.RawMessage, patch publicops.MetadataPatch) (json.RawMessage, bool, error) {
	if !patch.Replace.Set && !patch.Merge.Set && len(patch.Set) == 0 && len(patch.Unset) == 0 {
		return current, false, nil
	}
	setKeys := make([]string, 0, len(patch.Set))
	for key := range patch.Set {
		setKeys = append(setKeys, key)
	}
	sort.Strings(setKeys)
	for _, key := range setKeys {
		if err := storage.ValidateMetadataKey(key); err != nil {
			return nil, false, fmt.Errorf("%w: %w", storage.ErrValidation, err)
		}
	}
	for _, key := range patch.Unset {
		if err := storage.ValidateMetadataKey(key); err != nil {
			return nil, false, fmt.Errorf("%w: %w", storage.ErrValidation, err)
		}
	}
	var next json.RawMessage
	if patch.Replace.Set {
		next = append(json.RawMessage(nil), patch.Replace.Value...)
		if len(next) == 0 {
			next = json.RawMessage(`{}`)
		}
		if !json.Valid(next) {
			return nil, false, fmt.Errorf("%w: metadata replacement is not valid JSON", storage.ErrValidation)
		}
	} else {
		next = append(json.RawMessage(nil), current...)
		if patch.Merge.Set {
			// A JSON null unmarshals into a nil overlay map, so the merge
			// below would silently accept it as "change nothing".
			if strings.TrimSpace(string(patch.Merge.Value)) == "null" {
				return nil, false, fmt.Errorf("%w: metadata merge must be a JSON object", storage.ErrValidation)
			}
			merged, err := storage.MergeMetadataJSON(next, patch.Merge.Value)
			if err != nil {
				return nil, false, fmt.Errorf("%w: metadata merge: %v", storage.ErrValidation, err)
			}
			next = merged
		}
		if len(patch.Set) > 0 || len(patch.Unset) > 0 {
			values := make(map[string]json.RawMessage)
			if len(next) > 0 && string(next) != "null" {
				if err := json.Unmarshal(next, &values); err != nil {
					return nil, false, fmt.Errorf("%w: metadata edits require an object: %v", storage.ErrValidation, err)
				}
			}
			for _, key := range setKeys {
				value := patch.Set[key]
				if !json.Valid(value) {
					return nil, false, fmt.Errorf("%w: metadata value for key %q is not valid JSON", storage.ErrValidation, key)
				}
				values[key] = append(json.RawMessage(nil), value...)
			}
			for _, key := range patch.Unset {
				delete(values, key)
			}
			encoded, err := json.Marshal(values)
			if err != nil {
				return nil, false, fmt.Errorf("%w: encode metadata edits: %v", storage.ErrValidation, err)
			}
			next = encoded
		}
	}
	if err := ValidateMetadataIfConfigured(next); err != nil {
		return nil, false, err
	}
	changed, err := metadataChanged(current, next)
	if err != nil {
		return nil, false, fmt.Errorf("%w: compare metadata: %v", storage.ErrValidation, err)
	}
	return next, changed, nil
}

func metadataChanged(current, next json.RawMessage) (bool, error) {
	canonical := func(raw json.RawMessage) (string, error) {
		if len(raw) == 0 || string(raw) == "null" {
			return "{}", nil
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", err
		}
		encoded, err := json.Marshal(value)
		return string(encoded), err
	}
	left, err := canonical(current)
	if err != nil {
		return false, err
	}
	right, err := canonical(next)
	if err != nil {
		return false, err
	}
	return left != right, nil
}

// ApplyLabelPatch applies ordered label edits and reports whether rows changed.
func ApplyLabelPatch(ctx context.Context, tx DBTX, current *types.Issue, patch publicops.LabelPatch, actor string) (bool, error) {
	if !patch.Replace.Set && len(patch.Add) == 0 && len(patch.Remove) == 0 {
		return false, nil
	}
	existing := stringSet(current.Labels)
	target := make(map[string]struct{}, len(existing)+len(patch.Add))
	if !patch.Replace.Set {
		for label := range existing {
			target[label] = struct{}{}
		}
	}
	// An empty-string entry is DROPPED rather than written or refused
	// (bd-yby99.29). A label row carrying '' is junk that renders as nothing
	// and matches nothing, so the two useful answers were dropping it and
	// refusing it; dropping keeps a stray empty entry from failing an
	// otherwise-good multi-label edit.
	//
	// Skipping here rather than at the insert is what makes it a no-op instead
	// of a silent partial write: an Add of only '' leaves target equal to
	// existing, so the sameStringSet check below reports Changed false and
	// writes nothing at all.
	//
	// Entries already in existing are deliberately untouched. A legacy '' row
	// survives an Add/Remove patch and is cleared by a Replace that omits it,
	// which is the ordinary set semantics rather than a migration.
	for _, label := range patch.Replace.Value {
		if label == "" {
			continue
		}
		target[label] = struct{}{}
	}
	for _, label := range patch.Add {
		if label == "" {
			continue
		}
		target[label] = struct{}{}
	}
	for _, label := range patch.Remove {
		delete(target, label)
	}
	if sameStringSet(existing, target) {
		return false, nil
	}
	sqlTx, ok := tx.(*sql.Tx)
	if !ok {
		return false, fmt.Errorf("apply labels: transaction must be *sql.Tx")
	}
	for _, label := range stringSetDifference(existing, target) {
		if err := RemoveLabelInTx(ctx, sqlTx, "", "", current.ID, label, actor); err != nil {
			return false, err
		}
	}
	for _, label := range stringSetDifference(target, existing) {
		if err := AddLabelInTx(ctx, sqlTx, "", "", current.ID, label, actor); err != nil {
			return false, err
		}
	}
	return true, nil
}

// ParentPatchResult reports the concrete changes made by ApplyParentPatch.
type ParentPatchResult struct {
	Changed          bool
	IssueRowsChanged bool
	WispRowsChanged  bool
}

// ApplyParentPatch replaces the parent-child targets and reports concrete changes.
func ApplyParentPatch(ctx context.Context, tx DBTX, current *types.Issue, parent publicops.Field[string], actor string) (ParentPatchResult, error) {
	if !parent.Set {
		return ParentPatchResult{}, nil
	}
	dependencies, err := GetDependencyRecordsForIssuesInTx(ctx, tx, []string{current.ID})
	if err != nil {
		return ParentPatchResult{}, err
	}
	existing := map[string]struct{}{}
	for _, dependency := range dependencies[current.ID] {
		if dependency.Type == types.DepParentChild {
			existing[dependency.DependsOnID] = struct{}{}
		}
	}
	target := map[string]struct{}{}
	if parent.Value != "" {
		target[parent.Value] = struct{}{}
	}
	if sameStringSet(existing, target) {
		return ParentPatchResult{}, nil
	}
	sqlTx, ok := tx.(*sql.Tx)
	if !ok {
		return ParentPatchResult{}, fmt.Errorf("apply parent: transaction must be *sql.Tx")
	}
	var recomputed RecomputeIsBlockedResult
	for _, parentID := range stringSetDifference(existing, target) {
		if _, err := removeDependencyInTx(ctx, sqlTx, current.ID, parentID, actor, false, &recomputed); err != nil {
			return ParentPatchResult{}, err
		}
	}
	for _, parentID := range stringSetDifference(target, existing) {
		if _, err := addDependencyInTx(ctx, sqlTx, &types.Dependency{IssueID: current.ID, DependsOnID: parentID, Type: types.DepParentChild}, actor, AddDependencyOpts{}, &recomputed); err != nil {
			return ParentPatchResult{}, err
		}
	}
	return ParentPatchResult{Changed: true, IssueRowsChanged: recomputed.IssueRowsChanged, WispRowsChanged: recomputed.WispRowsChanged}, nil
}

func stringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func sameStringSet(left, right map[string]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for value := range left {
		if _, ok := right[value]; !ok {
			return false
		}
	}
	return true
}

func stringSetDifference(left, right map[string]struct{}) []string {
	values := make([]string, 0)
	for value := range left {
		if _, ok := right[value]; !ok {
			values = append(values, value)
		}
	}
	sort.Strings(values)
	return values
}
