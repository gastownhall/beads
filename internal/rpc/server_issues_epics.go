package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/util"
	"github.com/steveyegge/beads/internal/utils"
)

// containsLabel checks if a label exists in the list
func containsLabel(labels []string, label string) bool {
	for _, l := range labels {
		if l == label {
			return true
		}
	}
	return false
}

// parseTimeRPC parses time strings in multiple formats (RFC3339, YYYY-MM-DD, etc.)
// Matches the parseTimeFlag behavior in cmd/bd/list.go for CLI parity
func parseTimeRPC(s string) (time.Time, error) {
	// Try RFC3339 first (ISO 8601 with timezone)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	
	// Try YYYY-MM-DD format (common user input)
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	
	// Try YYYY-MM-DD HH:MM:SS format
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t, nil
	}
	
	return time.Time{}, fmt.Errorf("unsupported date format: %q (use YYYY-MM-DD or RFC3339)", s)
}

func strValue(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func updatesFromArgs(a UpdateArgs) (map[string]interface{}, error) {
	u := map[string]interface{}{}
	if a.Title != nil {
		u["title"] = *a.Title
	}
	if a.Description != nil {
		u["description"] = *a.Description
	}
	if a.Status != nil {
		u["status"] = *a.Status
	}
	if a.Priority != nil {
		u["priority"] = *a.Priority
	}
	if a.Design != nil {
		u["design"] = *a.Design
	}
	if a.AcceptanceCriteria != nil {
		u["acceptance_criteria"] = *a.AcceptanceCriteria
	}
	if a.Notes != nil {
		u["notes"] = *a.Notes
	}
	if a.Assignee != nil {
		u["assignee"] = *a.Assignee
	}
	if a.ExternalRef != nil {
		u["external_ref"] = *a.ExternalRef
	}
	if a.EstimatedMinutes != nil {
		u["estimated_minutes"] = *a.EstimatedMinutes
	}
	if a.IssueType != nil {
		u["issue_type"] = *a.IssueType
	}
	// Messaging fields
	if a.Sender != nil {
		u["sender"] = *a.Sender
	}
	if a.Ephemeral != nil {
		u["wisp"] = *a.Ephemeral // Storage API uses "wisp", maps to "ephemeral" column
	}
	if a.RepliesTo != nil {
		u["replies_to"] = *a.RepliesTo
	}
	// Graph link fields
	if a.RelatesTo != nil {
		u["relates_to"] = *a.RelatesTo
	}
	if a.DuplicateOf != nil {
		u["duplicate_of"] = *a.DuplicateOf
	}
	if a.SupersededBy != nil {
		u["superseded_by"] = *a.SupersededBy
	}
	// Pinned field
	if a.Pinned != nil {
		u["pinned"] = *a.Pinned
	}
	// Agent slot fields
	if a.HookBead != nil {
		u["hook_bead"] = *a.HookBead
	}
	if a.RoleBead != nil {
		u["role_bead"] = *a.RoleBead
	}
	// Agent state fields
	if a.AgentState != nil {
		u["agent_state"] = *a.AgentState
	}
	if a.LastActivity != nil && *a.LastActivity {
		u["last_activity"] = time.Now()
	}
	// Agent identity fields
	if a.RoleType != nil {
		u["role_type"] = *a.RoleType
	}
	if a.Rig != nil {
		u["rig"] = *a.Rig
	}
	// Event fields
	if a.EventCategory != nil {
		u["event_category"] = *a.EventCategory
	}
	if a.EventActor != nil {
		u["event_actor"] = *a.EventActor
	}
	if a.EventTarget != nil {
		u["event_target"] = *a.EventTarget
	}
	if a.EventPayload != nil {
		u["event_payload"] = *a.EventPayload
	}
	// Gate fields
	if a.AwaitID != nil {
		u["await_id"] = *a.AwaitID
	}
	if len(a.Waiters) > 0 {
		u["waiters"] = a.Waiters
	}
	// Slot fields
	if a.Holder != nil {
		u["holder"] = *a.Holder
	}
	// Metadata field (GH#1413)
	if a.Metadata != nil {
		// Validate that the metadata is well-formed JSON
		if !json.Valid([]byte(*a.Metadata)) {
			return nil, fmt.Errorf("metadata must be valid JSON")
		}
		u["metadata"] = json.RawMessage(*a.Metadata)
	}
	// Time-based scheduling fields (GH#820)
	if a.DueAt != nil {
		if *a.DueAt == "" {
			u["due_at"] = nil // Clear the field
		} else {
			// Try date-only format first (YYYY-MM-DD)
			if t, err := time.ParseInLocation("2006-01-02", *a.DueAt, time.Local); err == nil {
				u["due_at"] = t
			} else if t, err := time.Parse(time.RFC3339, *a.DueAt); err == nil {
				// Try RFC3339 format (2025-01-15T10:00:00Z)
				u["due_at"] = t
			} else {
				return nil, fmt.Errorf("invalid due_at format %q: use YYYY-MM-DD or RFC3339", *a.DueAt)
			}
		}
	}
	if a.DeferUntil != nil {
		if *a.DeferUntil == "" {
			u["defer_until"] = nil // Clear the field
		} else {
			// Try date-only format first (YYYY-MM-DD)
			if t, err := time.ParseInLocation("2006-01-02", *a.DeferUntil, time.Local); err == nil {
				u["defer_until"] = t
			} else if t, err := time.Parse(time.RFC3339, *a.DeferUntil); err == nil {
				// Try RFC3339 format (2025-01-15T10:00:00Z)
				u["defer_until"] = t
			} else {
				return nil, fmt.Errorf("invalid defer_until format %q: use YYYY-MM-DD or RFC3339", *a.DeferUntil)
			}
		}
	}
	return u, nil
}

func (s *Server) handleCreate(req *Request) Response {
	var createArgs CreateArgs
	if err := json.Unmarshal(req.Args, &createArgs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid create args: %v", err),
		}
	}

	// Check for conflicting flags
	if createArgs.ID != "" && createArgs.Parent != "" {
		return Response{
			Success: false,
			Error:   "cannot specify both ID and Parent",
		}
	}

	// Warn if creating an issue without a description (unless it's a test issue)
	if createArgs.Description == "" && !strings.Contains(strings.ToLower(createArgs.Title), "test") {
		// Log warning to daemon logs (stderr goes to daemon logs)
		fmt.Fprintf(os.Stderr, "[WARNING] Creating issue '%s' without description. Issues without descriptions lack context for future work.\n", createArgs.Title)
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available (global daemon deprecated - use local daemon instead with 'bd daemon' in your project)",
		}
	}
	ctx := s.reqCtx(req)

	// If parent is specified, generate child ID
	issueID := createArgs.ID
	if createArgs.Parent != "" {
		childID, err := store.GetNextChildID(ctx, createArgs.Parent)
		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("failed to generate child ID: %v", err),
			}
		}
		issueID = childID
	}

	var design, acceptance, notes, assignee, externalRef *string
	if createArgs.Design != "" {
		design = &createArgs.Design
	}
	if createArgs.AcceptanceCriteria != "" {
		acceptance = &createArgs.AcceptanceCriteria
	}
	if createArgs.Notes != "" {
		notes = &createArgs.Notes
	}
	if createArgs.Assignee != "" {
		assignee = &createArgs.Assignee
	}
	if createArgs.ExternalRef != "" {
		externalRef = &createArgs.ExternalRef
	}

	// Parse DueAt if provided (GH#820)
	var dueAt *time.Time
	if createArgs.DueAt != "" {
		// Try date-only format first (YYYY-MM-DD)
		if t, err := time.ParseInLocation("2006-01-02", createArgs.DueAt, time.Local); err == nil {
			dueAt = &t
		} else if t, err := time.Parse(time.RFC3339, createArgs.DueAt); err == nil {
			// Try RFC3339 format (2025-01-15T10:00:00Z)
			dueAt = &t
		} else {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("invalid due_at format %q. Examples: 2025-01-15, 2025-01-15T10:00:00Z", createArgs.DueAt),
			}
		}
	}

	// Parse DeferUntil if provided (GH#820, GH#950, GH#952)
	var deferUntil *time.Time
	if createArgs.DeferUntil != "" {
		// Try date-only format first (YYYY-MM-DD)
		if t, err := time.ParseInLocation("2006-01-02", createArgs.DeferUntil, time.Local); err == nil {
			deferUntil = &t
		} else if t, err := time.Parse(time.RFC3339, createArgs.DeferUntil); err == nil {
			// Try RFC3339 format (2025-01-15T10:00:00Z)
			deferUntil = &t
		} else {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("invalid defer_until format %q. Examples: 2025-01-15, 2025-01-15T10:00:00Z", createArgs.DeferUntil),
			}
		}
	}

	issue := &types.Issue{
		ID:                 issueID,
		Title:              createArgs.Title,
		Description:        createArgs.Description,
		IssueType:          types.IssueType(createArgs.IssueType),
		Priority:           createArgs.Priority,
		Design:             strValue(design),
		AcceptanceCriteria: strValue(acceptance),
		Notes:              strValue(notes),
		Assignee:           strValue(assignee),
		ExternalRef:        externalRef,
		EstimatedMinutes:   createArgs.EstimatedMinutes,
		Status:             types.StatusOpen,
		// Messaging fields
		Sender:    createArgs.Sender,
		Ephemeral: createArgs.Ephemeral,
		// NOTE: RepliesTo now handled via replies-to dependency (Decision 004)
		// ID generation
		IDPrefix:  createArgs.IDPrefix,
		CreatedBy: createArgs.CreatedBy,
		Owner:     createArgs.Owner,
		// Molecule type
		MolType: types.MolType(createArgs.MolType),
		// Agent identity fields
		RoleType: createArgs.RoleType,
		Rig:      createArgs.Rig,
		// Event fields (map protocol names to internal names)
		EventKind: createArgs.EventCategory,
		Actor:     createArgs.EventActor,
		Target:    createArgs.EventTarget,
		Payload:   createArgs.EventPayload,
		// Time-based scheduling (GH#820, GH#950, GH#952)
		DueAt:      dueAt,
		DeferUntil: deferUntil,
	}
	
	// Check if any dependencies are discovered-from type
	// If so, inherit source_repo from the parent issue
	var discoveredFromParentID string
	for _, depSpec := range createArgs.Dependencies {
		depSpec = strings.TrimSpace(depSpec)
		if depSpec == "" {
			continue
		}
		
		var depType types.DependencyType
		var dependsOnID string
		
		if strings.Contains(depSpec, ":") {
			parts := strings.SplitN(depSpec, ":", 2)
			if len(parts) == 2 {
				depType = types.DependencyType(strings.TrimSpace(parts[0]))
				dependsOnID = strings.TrimSpace(parts[1])
				
				if depType == types.DepDiscoveredFrom {
					discoveredFromParentID = dependsOnID
					break
				}
			}
		}
	}
	
	// If we found a discovered-from dependency, inherit source_repo from parent
	if discoveredFromParentID != "" {
		parentIssue, err := store.GetIssue(ctx, discoveredFromParentID)
		if err == nil && parentIssue.SourceRepo != "" {
			issue.SourceRepo = parentIssue.SourceRepo
		}
		// If error getting parent or parent has no source_repo, continue with default
	}
	
	if err := store.CreateIssue(ctx, issue, s.reqActor(req)); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to create issue: %v", err),
		}
	}

	// If parent was specified, add parent-child dependency
	if createArgs.Parent != "" {
		dep := &types.Dependency{
			IssueID:     issue.ID,
			DependsOnID: createArgs.Parent,
			Type:        types.DepParentChild,
		}
		if err := store.AddDependency(ctx, dep, s.reqActor(req)); err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("failed to add parent-child dependency %s -> %s: %v", issue.ID, createArgs.Parent, err),
			}
		}
	}

	// If RepliesTo was specified, add replies-to dependency (Decision 004)
	if createArgs.RepliesTo != "" {
		dep := &types.Dependency{
			IssueID:     issue.ID,
			DependsOnID: createArgs.RepliesTo,
			Type:        types.DepRepliesTo,
			ThreadID:    createArgs.RepliesTo, // Use parent ID as thread root
		}
		if err := store.AddDependency(ctx, dep, s.reqActor(req)); err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("failed to add replies-to dependency %s -> %s: %v", issue.ID, createArgs.RepliesTo, err),
			}
		}
	}

	// Add labels if specified
	for _, label := range createArgs.Labels {
		if err := store.AddLabel(ctx, issue.ID, label, s.reqActor(req)); err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("failed to add label %s: %v", label, err),
			}
		}
	}

	// Auto-add role_type/rig labels for agent beads (enables filtering queries)
	// Check for gt:agent label to identify agent beads (Gas Town separation)
	if containsLabel(createArgs.Labels, "gt:agent") {
		if issue.RoleType != "" {
			label := "role_type:" + issue.RoleType
			if err := store.AddLabel(ctx, issue.ID, label, s.reqActor(req)); err != nil {
				return Response{
					Success: false,
					Error:   fmt.Sprintf("failed to add role_type label: %v", err),
				}
			}
		}
		if issue.Rig != "" {
			label := "rig:" + issue.Rig
			if err := store.AddLabel(ctx, issue.ID, label, s.reqActor(req)); err != nil {
				return Response{
					Success: false,
					Error:   fmt.Sprintf("failed to add rig label: %v", err),
				}
			}
		}
	}

	// Add dependencies if specified
	for _, depSpec := range createArgs.Dependencies {
		depSpec = strings.TrimSpace(depSpec)
		if depSpec == "" {
			continue
		}

		var depType types.DependencyType
		var dependsOnID string

		if strings.Contains(depSpec, ":") {
			parts := strings.SplitN(depSpec, ":", 2)
			if len(parts) != 2 {
				return Response{
					Success: false,
					Error:   fmt.Sprintf("invalid dependency format '%s', expected 'type:id' or 'id'", depSpec),
				}
			}
			depType = types.DependencyType(strings.TrimSpace(parts[0]))
			dependsOnID = strings.TrimSpace(parts[1])
		} else {
			depType = types.DepBlocks
			dependsOnID = depSpec
		}

		if !depType.IsValid() {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("invalid dependency type '%s' (valid: blocks, related, parent-child, discovered-from)", depType),
			}
		}

		dep := &types.Dependency{
			IssueID:     issue.ID,
			DependsOnID: dependsOnID,
			Type:        depType,
		}
		if err := store.AddDependency(ctx, dep, s.reqActor(req)); err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("failed to add dependency %s -> %s: %v", issue.ID, dependsOnID, err),
			}
		}
	}

	// Add waits-for dependency if specified
	if createArgs.WaitsFor != "" {
		// Validate gate type
		gate := createArgs.WaitsForGate
		if gate == "" {
			gate = types.WaitsForAllChildren
		}
		if gate != types.WaitsForAllChildren && gate != types.WaitsForAnyChildren {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("invalid waits_for_gate value '%s' (valid: all-children, any-children)", gate),
			}
		}

		// Create metadata JSON
		meta := types.WaitsForMeta{
			Gate: gate,
		}
		metaJSON, err := json.Marshal(meta)
		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("failed to serialize waits-for metadata: %v", err),
			}
		}

		dep := &types.Dependency{
			IssueID:     issue.ID,
			DependsOnID: createArgs.WaitsFor,
			Type:        types.DepWaitsFor,
			Metadata:    string(metaJSON),
		}
		if err := store.AddDependency(ctx, dep, s.reqActor(req)); err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("failed to add waits-for dependency %s -> %s: %v", issue.ID, createArgs.WaitsFor, err),
			}
		}
	}

	// Emit mutation event for event-driven daemon
	s.emitMutation(MutationCreate, issue.ID, issue.Title, issue.Assignee)

	data, _ := json.Marshal(issue)
	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleUpdate(req *Request) Response {
	var updateArgs UpdateArgs
	if err := json.Unmarshal(req.Args, &updateArgs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid update args: %v", err),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available (global daemon deprecated - use local daemon instead with 'bd daemon' in your project)",
		}
	}

	ctx := s.reqCtx(req)

	// Check if issue is a template (beads-1ra): templates are read-only
	issue, err := store.GetIssue(ctx, updateArgs.ID)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get issue: %v", err),
		}
	}
	if issue == nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("issue %s not found", updateArgs.ID),
		}
	}
	if issue.IsTemplate {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("cannot update template %s: templates are read-only; use 'bd molecule instantiate' to create a work item", updateArgs.ID),
		}
	}

	actor := s.reqActor(req)

	// Handle claim operation atomically
	if updateArgs.Claim {
		// Check if already claimed (has non-empty assignee)
		if issue.Assignee != "" {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("already claimed by %s", issue.Assignee),
			}
		}
		// Atomically set assignee and status
		claimUpdates := map[string]interface{}{
			"assignee": actor,
			"status":   "in_progress",
		}
		if err := store.UpdateIssue(ctx, updateArgs.ID, claimUpdates, actor); err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("failed to claim issue: %v", err),
			}
		}
	}

	updates, err := updatesFromArgs(updateArgs)
	if err != nil {
		return Response{
			Success: false,
			Error:   err.Error(),
		}
	}

	// Apply regular field updates if any
	if len(updates) > 0 {
		if err := store.UpdateIssue(ctx, updateArgs.ID, updates, actor); err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("failed to update issue: %v", err),
			}
		}
	}

	// Handle label operations
	// Set labels (replaces all existing labels)
	if len(updateArgs.SetLabels) > 0 {
		// Get current labels
		currentLabels, err := store.GetLabels(ctx, updateArgs.ID)
		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("failed to get current labels: %v", err),
			}
		}
		// Remove all current labels
		for _, label := range currentLabels {
			if err := store.RemoveLabel(ctx, updateArgs.ID, label, actor); err != nil {
				return Response{
					Success: false,
					Error:   fmt.Sprintf("failed to remove label %s: %v", label, err),
				}
			}
		}
		// Add new labels
		for _, label := range updateArgs.SetLabels {
			if err := store.AddLabel(ctx, updateArgs.ID, label, actor); err != nil {
				return Response{
					Success: false,
					Error:   fmt.Sprintf("failed to set label %s: %v", label, err),
				}
			}
		}
	}

	// Add labels
	for _, label := range updateArgs.AddLabels {
		if err := store.AddLabel(ctx, updateArgs.ID, label, actor); err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("failed to add label %s: %v", label, err),
			}
		}
	}

	// Remove labels
	for _, label := range updateArgs.RemoveLabels {
		if err := store.RemoveLabel(ctx, updateArgs.ID, label, actor); err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("failed to remove label %s: %v", label, err),
			}
		}
	}

	// Auto-add role_type/rig labels for agent beads when these fields are set
	// This enables filtering queries like: bd list --label=gt:agent --label=role_type:witness
	// Note: We remove old role_type/rig labels first to prevent accumulation
	// Check for gt:agent label to identify agent beads (Gas Town separation)
	issueLabels, _ := store.GetLabels(ctx, updateArgs.ID)
	if containsLabel(issueLabels, "gt:agent") {
		if updateArgs.RoleType != nil && *updateArgs.RoleType != "" {
			// Remove any existing role_type:* labels first
			for _, l := range issueLabels {
				if strings.HasPrefix(l, "role_type:") {
					_ = store.RemoveLabel(ctx, updateArgs.ID, l, actor)
				}
			}
			// Add new label
			label := "role_type:" + *updateArgs.RoleType
			if err := store.AddLabel(ctx, updateArgs.ID, label, actor); err != nil {
				return Response{
					Success: false,
					Error:   fmt.Sprintf("failed to add role_type label: %v", err),
				}
			}
		}
		if updateArgs.Rig != nil && *updateArgs.Rig != "" {
			// Remove any existing rig:* labels first
			for _, l := range issueLabels {
				if strings.HasPrefix(l, "rig:") {
					_ = store.RemoveLabel(ctx, updateArgs.ID, l, actor)
				}
			}
			// Add new label
			label := "rig:" + *updateArgs.Rig
			if err := store.AddLabel(ctx, updateArgs.ID, label, actor); err != nil {
				return Response{
					Success: false,
					Error:   fmt.Sprintf("failed to add rig label: %v", err),
				}
			}
		}
	}

	// Handle reparenting
	if updateArgs.Parent != nil {
		newParentID := *updateArgs.Parent

		// Validate new parent exists (unless empty string to remove parent)
		if newParentID != "" {
			newParent, err := store.GetIssue(ctx, newParentID)
			if err != nil {
				return Response{
					Success: false,
					Error:   fmt.Sprintf("failed to get new parent: %v", err),
				}
			}
			if newParent == nil {
				return Response{
					Success: false,
					Error:   fmt.Sprintf("parent issue %s not found", newParentID),
				}
			}
		}

		// Find and remove existing parent-child dependency
		deps, err := store.GetDependencyRecords(ctx, updateArgs.ID)
		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("failed to get dependencies: %v", err),
			}
		}
		for _, dep := range deps {
			if dep.Type == types.DepParentChild {
				if err := store.RemoveDependency(ctx, updateArgs.ID, dep.DependsOnID, actor); err != nil {
					return Response{
						Success: false,
						Error:   fmt.Sprintf("failed to remove old parent dependency: %v", err),
					}
				}
				break // Only one parent-child dependency expected
			}
		}

		// Add new parent-child dependency (if not removing parent)
		if newParentID != "" {
			newDep := &types.Dependency{
				IssueID:     updateArgs.ID,
				DependsOnID: newParentID,
				Type:        types.DepParentChild,
			}
			if err := store.AddDependency(ctx, newDep, actor); err != nil {
				return Response{
					Success: false,
					Error:   fmt.Sprintf("failed to add parent dependency: %v", err),
				}
			}
		}
	}

	// Emit mutation event for event-driven daemon (only if any updates or label/parent operations were performed)
	if len(updates) > 0 || len(updateArgs.SetLabels) > 0 || len(updateArgs.AddLabels) > 0 || len(updateArgs.RemoveLabels) > 0 || updateArgs.Parent != nil {
		// Determine effective assignee: use new assignee from update if provided, otherwise use existing
		effectiveAssignee := issue.Assignee
		if updateArgs.Assignee != nil && *updateArgs.Assignee != "" {
			effectiveAssignee = *updateArgs.Assignee
		}

		// Check if this was a status change - emit rich MutationStatus event
		if updateArgs.Status != nil && *updateArgs.Status != string(issue.Status) {
			s.emitRichMutation(MutationEvent{
				Type:      MutationStatus,
				IssueID:   updateArgs.ID,
				Title:     issue.Title,
				Assignee:  effectiveAssignee,
				Actor:     actor,
				OldStatus: string(issue.Status),
				NewStatus: *updateArgs.Status,
			})
		} else {
			s.emitRichMutation(MutationEvent{
				Type:     MutationUpdate,
				IssueID:  updateArgs.ID,
				Title:    issue.Title,
				Assignee: effectiveAssignee,
				Actor:    actor,
			})
		}
	}

	updatedIssue, getErr := store.GetIssue(ctx, updateArgs.ID)
	if getErr != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get updated issue: %v", getErr),
		}
	}

	data, _ := json.Marshal(updatedIssue)
	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleClose(req *Request) Response {
	var closeArgs CloseArgs
	if err := json.Unmarshal(req.Args, &closeArgs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid close args: %v", err),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available (global daemon deprecated - use local daemon instead with 'bd daemon' in your project)",
		}
	}

	ctx := s.reqCtx(req)

	// Check if issue is a template (beads-1ra): templates are read-only
	issue, err := store.GetIssue(ctx, closeArgs.ID)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get issue: %v", err),
		}
	}
	if issue != nil && issue.IsTemplate {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("cannot close template %s: templates are read-only", closeArgs.ID),
		}
	}

	// Check if issue has open blockers (GH#962)
	if !closeArgs.Force {
		blocked, blockers, err := store.IsBlocked(ctx, closeArgs.ID)
		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("failed to check blockers: %v", err),
			}
		}
		if blocked && len(blockers) > 0 {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("cannot close %s: blocked by open issues %v (use --force to override)", closeArgs.ID, blockers),
			}
		}
	}

	// Capture old status for rich mutation event
	oldStatus := ""
	if issue != nil {
		oldStatus = string(issue.Status)
	}

	if err := store.CloseIssue(ctx, closeArgs.ID, closeArgs.Reason, s.reqActor(req), closeArgs.Session); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to close issue: %v", err),
		}
	}

	// Emit rich status change event for event-driven daemon
	s.emitRichMutation(MutationEvent{
		Type:      MutationStatus,
		IssueID:   closeArgs.ID,
		Title:     issue.Title,
		Assignee:  issue.Assignee,
		OldStatus: oldStatus,
		NewStatus: "closed",
	})

	closedIssue, _ := store.GetIssue(ctx, closeArgs.ID)

	// If SuggestNext is requested, find newly unblocked issues (GH#679)
	if closeArgs.SuggestNext {
		unblocked, err := store.GetNewlyUnblockedByClose(ctx, closeArgs.ID)
		if err != nil {
			// Non-fatal: still return the closed issue
			unblocked = nil
		}
		result := CloseResult{
			Closed:    closedIssue,
			Unblocked: unblocked,
		}
		data, _ := json.Marshal(result)
		return Response{
			Success: true,
			Data:    data,
		}
	}

	// Backward compatible: just return the closed issue
	data, _ := json.Marshal(closedIssue)
	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleDelete(req *Request) Response {
	var deleteArgs DeleteArgs
	if err := json.Unmarshal(req.Args, &deleteArgs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid delete args: %v", err),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available (global daemon deprecated - use local daemon instead with 'bd daemon' in your project)",
		}
	}

	// Validate that we have issue IDs to delete
	if len(deleteArgs.IDs) == 0 {
		return Response{
			Success: false,
			Error:   "no issue IDs provided for deletion",
		}
	}

	ctx := s.reqCtx(req)

	// Use batch delete for cascade/multi-issue operations if storage supports it
	// This handles cascade delete properly by expanding dependents recursively
	// For simple single-issue deletes, use the direct path to preserve custom reason
	if batchDeleter, ok := store.(storage.BatchDeleter); ok {
		// Use batch delete if: cascade enabled, force enabled, multiple IDs, or dry-run
		useBatchDelete := deleteArgs.Cascade || deleteArgs.Force || len(deleteArgs.IDs) > 1 || deleteArgs.DryRun
		if useBatchDelete {
			result, err := batchDeleter.DeleteIssues(ctx, deleteArgs.IDs, deleteArgs.Cascade, deleteArgs.Force, deleteArgs.DryRun)
			if err != nil {
				return Response{
					Success: false,
					Error:   fmt.Sprintf("delete failed: %v", err),
				}
			}

			// Emit mutation events for deleted issues
			if !deleteArgs.DryRun {
				for _, issueID := range deleteArgs.IDs {
					s.emitMutation(MutationDelete, issueID, "", "")
				}
			}

			// Build response
			responseData := map[string]interface{}{
				"deleted_count": result.DeletedCount,
				"total_count":   len(deleteArgs.IDs),
			}
			if deleteArgs.DryRun {
				responseData["dry_run"] = true
				responseData["issue_count"] = result.DeletedCount
			}
			if result.DependenciesCount > 0 {
				responseData["dependencies_removed"] = result.DependenciesCount
			}
			if result.LabelsCount > 0 {
				responseData["labels_removed"] = result.LabelsCount
			}
			if result.EventsCount > 0 {
				responseData["events_removed"] = result.EventsCount
			}
			if len(result.OrphanedIssues) > 0 {
				responseData["orphaned_issues"] = result.OrphanedIssues
			}

			data, _ := json.Marshal(responseData)
			return Response{
				Success: true,
				Data:    data,
			}
		}
	}

	// Simple single-issue delete path (preserves custom reason)
	// DryRun mode: just return what would be deleted
	if deleteArgs.DryRun {
		data, _ := json.Marshal(map[string]interface{}{
			"dry_run":     true,
			"issue_count": len(deleteArgs.IDs),
			"issues":      deleteArgs.IDs,
		})
		return Response{
			Success: true,
			Data:    data,
		}
	}

	deletedCount := 0
	errors := make([]string, 0)

	// Delete each issue
	for _, issueID := range deleteArgs.IDs {
		// Verify issue exists before deleting
		issue, err := store.GetIssue(ctx, issueID)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", issueID, err))
			continue
		}
		if issue == nil {
			errors = append(errors, fmt.Sprintf("%s: not found", issueID))
			continue
		}

		// Check if issue is a template (beads-1ra): templates are read-only
		if issue.IsTemplate {
			errors = append(errors, fmt.Sprintf("%s: cannot delete template (templates are read-only)", issueID))
			continue
		}

		// Create tombstone instead of hard delete
		// This preserves deletion history and prevents resurrection during sync
		type tombstoner interface {
			CreateTombstone(ctx context.Context, id string, actor string, reason string) error
		}
		if t, ok := store.(tombstoner); ok {
			reason := deleteArgs.Reason
			if reason == "" {
				reason = "deleted via daemon"
			}
			if err := t.CreateTombstone(ctx, issueID, "daemon", reason); err != nil {
				errors = append(errors, fmt.Sprintf("%s: %v", issueID, err))
				continue
			}
		} else {
			// Fallback to hard delete if CreateTombstone not available
			if err := store.DeleteIssue(ctx, issueID); err != nil {
				errors = append(errors, fmt.Sprintf("%s: %v", issueID, err))
				continue
			}
		}

		// Emit mutation event for event-driven daemon
		s.emitMutation(MutationDelete, issueID, issue.Title, issue.Assignee)
		deletedCount++
	}

	// Build response
	result := map[string]interface{}{
		"deleted_count": deletedCount,
		"total_count":   len(deleteArgs.IDs),
	}

	if len(errors) > 0 {
		result["errors"] = errors
		if deletedCount == 0 {
			// All deletes failed
			return Response{
				Success: false,
				Error:   fmt.Sprintf("failed to delete all issues: %v", errors),
			}
		}
		// Partial success
		result["partial_success"] = true
	}

	data, _ := json.Marshal(result)
	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleList(req *Request) Response {
	var listArgs ListArgs
	if err := json.Unmarshal(req.Args, &listArgs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid list args: %v", err),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available (global daemon deprecated - use local daemon instead with 'bd daemon' in your project)",
		}
	}

	filter := types.IssueFilter{
		Limit: listArgs.Limit,
	}
	
	// Normalize status: treat "" or "all" as unset (no filter)
	if listArgs.Status != "" && listArgs.Status != "all" {
		status := types.Status(listArgs.Status)
		filter.Status = &status
	}
	
	if listArgs.IssueType != "" {
		issueType := types.IssueType(listArgs.IssueType)
		filter.IssueType = &issueType
	}
	if listArgs.Assignee != "" {
		filter.Assignee = &listArgs.Assignee
	}
	if listArgs.Priority != nil {
		filter.Priority = listArgs.Priority
	}
	
	// Normalize and apply label filters
	labels := util.NormalizeLabels(listArgs.Labels)
	labelsAny := util.NormalizeLabels(listArgs.LabelsAny)
	// Support both old single Label and new Labels array (backward compat)
	if len(labels) > 0 {
		filter.Labels = labels
	} else if listArgs.Label != "" {
		filter.Labels = []string{strings.TrimSpace(listArgs.Label)}
	}
	if len(labelsAny) > 0 {
		filter.LabelsAny = labelsAny
	}
	if len(listArgs.IDs) > 0 {
		ids := util.NormalizeLabels(listArgs.IDs)
		if len(ids) > 0 {
			filter.IDs = ids
		}
	}
	
	// Pattern matching
	filter.TitleContains = listArgs.TitleContains
	filter.DescriptionContains = listArgs.DescriptionContains
	filter.NotesContains = listArgs.NotesContains
	
	// Date ranges - use parseTimeRPC helper for flexible formats
	if listArgs.CreatedAfter != "" {
		t, err := parseTimeRPC(listArgs.CreatedAfter)
		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("invalid --created-after date: %v", err),
			}
		}
		filter.CreatedAfter = &t
	}
	if listArgs.CreatedBefore != "" {
		t, err := parseTimeRPC(listArgs.CreatedBefore)
		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("invalid --created-before date: %v", err),
			}
		}
		filter.CreatedBefore = &t
	}
	if listArgs.UpdatedAfter != "" {
		t, err := parseTimeRPC(listArgs.UpdatedAfter)
		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("invalid --updated-after date: %v", err),
			}
		}
		filter.UpdatedAfter = &t
	}
	if listArgs.UpdatedBefore != "" {
		t, err := parseTimeRPC(listArgs.UpdatedBefore)
		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("invalid --updated-before date: %v", err),
			}
		}
		filter.UpdatedBefore = &t
	}
	if listArgs.ClosedAfter != "" {
		t, err := parseTimeRPC(listArgs.ClosedAfter)
		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("invalid --closed-after date: %v", err),
			}
		}
		filter.ClosedAfter = &t
	}
	if listArgs.ClosedBefore != "" {
		t, err := parseTimeRPC(listArgs.ClosedBefore)
		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("invalid --closed-before date: %v", err),
			}
		}
		filter.ClosedBefore = &t
	}
	
	// Empty/null checks
	filter.EmptyDescription = listArgs.EmptyDescription
	filter.NoAssignee = listArgs.NoAssignee
	filter.NoLabels = listArgs.NoLabels
	
	// Priority range
	filter.PriorityMin = listArgs.PriorityMin
	filter.PriorityMax = listArgs.PriorityMax

	// Pinned filtering
	filter.Pinned = listArgs.Pinned

	// Template filtering: exclude templates by default
	if !listArgs.IncludeTemplates {
		isTemplate := false
		filter.IsTemplate = &isTemplate
	}

	// Parent filtering
	if listArgs.ParentID != "" {
		filter.ParentID = &listArgs.ParentID
	}

	// Ephemeral filtering
	filter.Ephemeral = listArgs.Ephemeral

	// Molecule type filtering
	if listArgs.MolType != "" {
		molType := types.MolType(listArgs.MolType)
		filter.MolType = &molType
	}

	// Status exclusion (for default non-closed behavior, GH#788)
	if len(listArgs.ExcludeStatus) > 0 {
		for _, s := range listArgs.ExcludeStatus {
			filter.ExcludeStatus = append(filter.ExcludeStatus, types.Status(s))
		}
	}

	// Type exclusion (for hiding internal types like gates, bd-7zka.2)
	if len(listArgs.ExcludeTypes) > 0 {
		for _, t := range listArgs.ExcludeTypes {
			filter.ExcludeTypes = append(filter.ExcludeTypes, types.IssueType(t))
		}
	}

	// Time-based scheduling filters (GH#820)
	filter.Deferred = listArgs.Deferred
	if listArgs.DeferAfter != "" {
		t, err := parseTimeRPC(listArgs.DeferAfter)
		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("invalid --defer-after date: %v", err),
			}
		}
		filter.DeferAfter = &t
	}
	if listArgs.DeferBefore != "" {
		t, err := parseTimeRPC(listArgs.DeferBefore)
		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("invalid --defer-before date: %v", err),
			}
		}
		filter.DeferBefore = &t
	}
	if listArgs.DueAfter != "" {
		t, err := parseTimeRPC(listArgs.DueAfter)
		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("invalid --due-after date: %v", err),
			}
		}
		filter.DueAfter = &t
	}
	if listArgs.DueBefore != "" {
		t, err := parseTimeRPC(listArgs.DueBefore)
		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("invalid --due-before date: %v", err),
			}
		}
		filter.DueBefore = &t
	}
	filter.Overdue = listArgs.Overdue

	// Guard against excessive ID lists to avoid SQLite parameter limits
	const maxIDs = 1000
	if len(filter.IDs) > maxIDs {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("--id flag supports at most %d issue IDs, got %d", maxIDs, len(filter.IDs)),
		}
	}

	ctx := s.reqCtx(req)
	issues, err := store.SearchIssues(ctx, listArgs.Query, filter)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to list issues: %v", err),
		}
	}

	// Populate labels for each issue
	for _, issue := range issues {
		labels, _ := store.GetLabels(ctx, issue.ID)
		issue.Labels = labels
	}

	// Get dependency counts in bulk (single query instead of N queries)
	issueIDs := make([]string, len(issues))
	for i, issue := range issues {
		issueIDs[i] = issue.ID
	}
	depCounts, _ := store.GetDependencyCounts(ctx, issueIDs)
	commentCounts, _ := store.GetCommentCounts(ctx, issueIDs)

	// Populate dependencies for JSON output
	allDeps, _ := store.GetAllDependencyRecords(ctx)
	for _, issue := range issues {
		issue.Dependencies = allDeps[issue.ID]
	}

	// Build response with counts
	issuesWithCounts := make([]*types.IssueWithCounts, len(issues))
	for i, issue := range issues {
		counts := depCounts[issue.ID]
		if counts == nil {
			counts = &types.DependencyCounts{DependencyCount: 0, DependentCount: 0}
		}
		issuesWithCounts[i] = &types.IssueWithCounts{
			Issue:           issue,
			DependencyCount: counts.DependencyCount,
			DependentCount:  counts.DependentCount,
			CommentCount:    commentCounts[issue.ID],
		}
	}

	data, _ := json.Marshal(issuesWithCounts)
	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleCount(req *Request) Response {
	var countArgs CountArgs
	if err := json.Unmarshal(req.Args, &countArgs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid count args: %v", err),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available (global daemon deprecated - use local daemon instead with 'bd daemon' in your project)",
		}
	}

	filter := types.IssueFilter{}

	// Normalize status: treat "" or "all" as unset (no filter)
	if countArgs.Status != "" && countArgs.Status != "all" {
		status := types.Status(countArgs.Status)
		filter.Status = &status
	}

	if countArgs.IssueType != "" {
		issueType := types.IssueType(countArgs.IssueType)
		filter.IssueType = &issueType
	}
	if countArgs.Assignee != "" {
		filter.Assignee = &countArgs.Assignee
	}
	if countArgs.Priority != nil {
		filter.Priority = countArgs.Priority
	}

	// Normalize and apply label filters
	labels := util.NormalizeLabels(countArgs.Labels)
	labelsAny := util.NormalizeLabels(countArgs.LabelsAny)
	if len(labels) > 0 {
		filter.Labels = labels
	}
	if len(labelsAny) > 0 {
		filter.LabelsAny = labelsAny
	}
	if len(countArgs.IDs) > 0 {
		ids := util.NormalizeLabels(countArgs.IDs)
		if len(ids) > 0 {
			filter.IDs = ids
		}
	}

	// Pattern matching
	filter.TitleContains = countArgs.TitleContains
	filter.DescriptionContains = countArgs.DescriptionContains
	filter.NotesContains = countArgs.NotesContains

	// Date ranges - use parseTimeRPC helper for flexible formats
	if countArgs.CreatedAfter != "" {
		t, err := parseTimeRPC(countArgs.CreatedAfter)
		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("invalid --created-after date: %v", err),
			}
		}
		filter.CreatedAfter = &t
	}
	if countArgs.CreatedBefore != "" {
		t, err := parseTimeRPC(countArgs.CreatedBefore)
		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("invalid --created-before date: %v", err),
			}
		}
		filter.CreatedBefore = &t
	}
	if countArgs.UpdatedAfter != "" {
		t, err := parseTimeRPC(countArgs.UpdatedAfter)
		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("invalid --updated-after date: %v", err),
			}
		}
		filter.UpdatedAfter = &t
	}
	if countArgs.UpdatedBefore != "" {
		t, err := parseTimeRPC(countArgs.UpdatedBefore)
		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("invalid --updated-before date: %v", err),
			}
		}
		filter.UpdatedBefore = &t
	}
	if countArgs.ClosedAfter != "" {
		t, err := parseTimeRPC(countArgs.ClosedAfter)
		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("invalid --closed-after date: %v", err),
			}
		}
		filter.ClosedAfter = &t
	}
	if countArgs.ClosedBefore != "" {
		t, err := parseTimeRPC(countArgs.ClosedBefore)
		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("invalid --closed-before date: %v", err),
			}
		}
		filter.ClosedBefore = &t
	}

	// Empty/null checks
	filter.EmptyDescription = countArgs.EmptyDescription
	filter.NoAssignee = countArgs.NoAssignee
	filter.NoLabels = countArgs.NoLabels

	// Priority range
	filter.PriorityMin = countArgs.PriorityMin
	filter.PriorityMax = countArgs.PriorityMax

	ctx := s.reqCtx(req)
	issues, err := store.SearchIssues(ctx, countArgs.Query, filter)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to count issues: %v", err),
		}
	}

	// If no grouping, just return the count
	if countArgs.GroupBy == "" {
		type CountResult struct {
			Count int `json:"count"`
		}
		data, _ := json.Marshal(CountResult{Count: len(issues)})
		return Response{
			Success: true,
			Data:    data,
		}
	}

	// Group by the specified field
	type GroupCount struct {
		Group string `json:"group"`
		Count int    `json:"count"`
	}

	counts := make(map[string]int)

	// For label grouping, fetch all labels in one query to avoid N+1
	var labelsMap map[string][]string
	if countArgs.GroupBy == "label" {
		issueIDs := make([]string, len(issues))
		for i, issue := range issues {
			issueIDs[i] = issue.ID
		}
		var err error
		labelsMap, err = store.GetLabelsForIssues(ctx, issueIDs)
		if err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("failed to get labels: %v", err),
			}
		}
	}

	for _, issue := range issues {
		var groupKey string
		switch countArgs.GroupBy {
		case "status":
			groupKey = string(issue.Status)
		case "priority":
			groupKey = fmt.Sprintf("P%d", issue.Priority)
		case "type":
			groupKey = string(issue.IssueType)
		case "assignee":
			if issue.Assignee == "" {
				groupKey = "(unassigned)"
			} else {
				groupKey = issue.Assignee
			}
		case "label":
			// For labels, count each label separately
			labels := labelsMap[issue.ID]
			if len(labels) > 0 {
				for _, label := range labels {
					counts[label]++
				}
				continue
			} else {
				groupKey = "(no labels)"
			}
		default:
			return Response{
				Success: false,
				Error:   fmt.Sprintf("invalid group_by value: %s (must be one of: status, priority, type, assignee, label)", countArgs.GroupBy),
			}
		}
		counts[groupKey]++
	}

	// Convert map to sorted slice
	groups := make([]GroupCount, 0, len(counts))
	for group, count := range counts {
		groups = append(groups, GroupCount{Group: group, Count: count})
	}

	type GroupedCountResult struct {
		Total  int          `json:"total"`
		Groups []GroupCount `json:"groups"`
	}

	result := GroupedCountResult{
		Total:  len(issues),
		Groups: groups,
	}

	data, _ := json.Marshal(result)
	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleResolveID(req *Request) Response {
	var args ResolveIDArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid resolve_id args: %v", err),
		}
	}

	if s.storage == nil {
		return Response{
			Success: false,
			Error:   "storage not available (global daemon deprecated - use local daemon instead with 'bd daemon' in your project)",
		}
	}

	ctx := s.reqCtx(req)
	resolvedID, err := utils.ResolvePartialID(ctx, s.storage, args.ID)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to resolve ID: %v", err),
		}
	}

	data, _ := json.Marshal(resolvedID)
	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleShow(req *Request) Response {
	var showArgs ShowArgs
	if err := json.Unmarshal(req.Args, &showArgs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid show args: %v", err),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available (global daemon deprecated - use local daemon instead with 'bd daemon' in your project)",
		}
	}

	ctx := s.reqCtx(req)
	issue, err := store.GetIssue(ctx, showArgs.ID)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get issue: %v", err),
		}
	}
	if issue == nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("issue not found: %s", showArgs.ID),
		}
	}

	// Populate labels, dependencies (with metadata), and dependents (with metadata)
	labels, _ := store.GetLabels(ctx, issue.ID)

	// Get dependencies and dependents with metadata (including dependency type)
	// These methods are part of the Storage interface - no type assertion needed
	deps, _ := store.GetDependenciesWithMetadata(ctx, issue.ID)
	dependents, _ := store.GetDependentsWithMetadata(ctx, issue.ID)

	// Fetch comments
	comments, _ := store.GetIssueComments(ctx, issue.ID)

	// Create detailed response with related data
	details := &types.IssueDetails{
		Issue:        *issue,
		Labels:       labels,
		Dependencies: deps,
		Dependents:   dependents,
		Comments:     comments,
	}

	data, _ := json.Marshal(details)
	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleReady(req *Request) Response {
	var readyArgs ReadyArgs
	if err := json.Unmarshal(req.Args, &readyArgs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid ready args: %v", err),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available (global daemon deprecated - use local daemon instead with 'bd daemon' in your project)",
		}
	}

	wf := types.WorkFilter{
		// Leave Status empty to get both 'open' and 'in_progress' (GH#5aml)
		Type:            readyArgs.Type,
		Priority:        readyArgs.Priority,
		Unassigned:      readyArgs.Unassigned,
		Limit:           readyArgs.Limit,
		SortPolicy:      types.SortPolicy(readyArgs.SortPolicy),
		Labels:          util.NormalizeLabels(readyArgs.Labels),
		LabelsAny:       util.NormalizeLabels(readyArgs.LabelsAny),
		IncludeDeferred: readyArgs.IncludeDeferred, // GH#820
	}
	if readyArgs.Assignee != "" && !readyArgs.Unassigned {
		wf.Assignee = &readyArgs.Assignee
	}
	if readyArgs.ParentID != "" {
		wf.ParentID = &readyArgs.ParentID
	}
	if readyArgs.MolType != "" {
		molType := types.MolType(readyArgs.MolType)
		wf.MolType = &molType
	}

	ctx := s.reqCtx(req)
	issues, err := store.GetReadyWork(ctx, wf)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get ready work: %v", err),
		}
	}

	// Build response with comment counts
	issueIDs := make([]string, len(issues))
	for i, issue := range issues {
		issueIDs[i] = issue.ID
	}
	commentCounts, _ := store.GetCommentCounts(ctx, issueIDs)
	issuesWithCounts := make([]*types.IssueWithCounts, len(issues))
	for i, issue := range issues {
		issuesWithCounts[i] = &types.IssueWithCounts{
			Issue:        issue,
			CommentCount: commentCounts[issue.ID],
		}
	}

	data, _ := json.Marshal(issuesWithCounts)
	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleBlocked(req *Request) Response {
	var blockedArgs BlockedArgs
	if err := json.Unmarshal(req.Args, &blockedArgs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid blocked args: %v", err),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available (global daemon deprecated - use local daemon instead with 'bd daemon' in your project)",
		}
	}

	var wf types.WorkFilter
	if blockedArgs.ParentID != "" {
		wf.ParentID = &blockedArgs.ParentID
	}

	ctx := s.reqCtx(req)
	blocked, err := store.GetBlockedIssues(ctx, wf)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get blocked issues: %v", err),
		}
	}

	data, _ := json.Marshal(blocked)
	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleStale(req *Request) Response {
	var staleArgs StaleArgs
	if err := json.Unmarshal(req.Args, &staleArgs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid stale args: %v", err),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available (global daemon deprecated - use local daemon instead with 'bd daemon' in your project)",
		}
	}

	filter := types.StaleFilter{
		Days:   staleArgs.Days,
		Status: staleArgs.Status,
		Limit:  staleArgs.Limit,
	}

	ctx := s.reqCtx(req)
	issues, err := store.GetStaleIssues(ctx, filter)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get stale issues: %v", err),
		}
	}

	data, _ := json.Marshal(issues)
	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleStats(req *Request) Response {
	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available (global daemon deprecated - use local daemon instead with 'bd daemon' in your project)",
		}
	}

	ctx := s.reqCtx(req)
	stats, err := store.GetStatistics(ctx)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get statistics: %v", err),
		}
	}

	data, _ := json.Marshal(stats)
	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleEpicStatus(req *Request) Response {
	var epicArgs EpicStatusArgs
	if err := json.Unmarshal(req.Args, &epicArgs); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid epic status args: %v", err),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available (global daemon deprecated - use local daemon instead with 'bd daemon' in your project)",
		}
	}

	ctx := s.reqCtx(req)
	epics, err := store.GetEpicsEligibleForClosure(ctx)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get epic status: %v", err),
		}
	}

	if epicArgs.EligibleOnly {
		filtered := []*types.EpicStatus{}
		for _, epic := range epics {
			if epic.EligibleForClose {
				filtered = append(filtered, epic)
			}
		}
		epics = filtered
	}

	data, err := json.Marshal(epics)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to marshal epics: %v", err),
		}
	}

	return Response{
		Success: true,
		Data:    data,
	}
}

// handleGetConfig retrieves a config value from the database
func (s *Server) handleGetConfig(req *Request) Response {
	var args GetConfigArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid get_config args: %v", err),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available",
		}
	}

	ctx := s.reqCtx(req)

	// Get config value from database
	value, err := store.GetConfig(ctx, args.Key)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get config %q: %v", args.Key, err),
		}
	}

	result := GetConfigResponse{
		Key:   args.Key,
		Value: value,
	}

	data, _ := json.Marshal(result)
	return Response{
		Success: true,
		Data:    data,
	}
}

// handleMolStale finds stale molecules (complete-but-unclosed)
func (s *Server) handleMolStale(req *Request) Response {
	var args MolStaleArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid mol_stale args: %v", err),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available",
		}
	}

	ctx := s.reqCtx(req)

	// Get all epics eligible for closure (complete but unclosed)
	epicStatuses, err := store.GetEpicsEligibleForClosure(ctx)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to query epics: %v", err),
		}
	}

	// Get blocked issues to find what each stale molecule is blocking
	blockedIssues, err := store.GetBlockedIssues(ctx, types.WorkFilter{})
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to query blocked issues: %v", err),
		}
	}

	// Build map of issue ID -> what issues it's blocking
	blockingMap := make(map[string][]string)
	for _, blocked := range blockedIssues {
		for _, blockerID := range blocked.BlockedBy {
			blockingMap[blockerID] = append(blockingMap[blockerID], blocked.ID)
		}
	}

	var staleMolecules []*StaleMolecule
	blockingCount := 0

	for _, es := range epicStatuses {
		// Skip if not eligible for close (not all children closed)
		if !es.EligibleForClose {
			continue
		}

		// Skip if no children and not showing all
		if es.TotalChildren == 0 && !args.ShowAll {
			continue
		}

		// Filter by unassigned if requested
		if args.UnassignedOnly && es.Epic.Assignee != "" {
			continue
		}

		// Find what this molecule is blocking
		blocking := blockingMap[es.Epic.ID]
		blockingIssueCount := len(blocking)

		// Filter by blocking if requested
		if args.BlockingOnly && blockingIssueCount == 0 {
			continue
		}

		mol := &StaleMolecule{
			ID:             es.Epic.ID,
			Title:          es.Epic.Title,
			TotalChildren:  es.TotalChildren,
			ClosedChildren: es.ClosedChildren,
			Assignee:       es.Epic.Assignee,
			BlockingIssues: blocking,
			BlockingCount:  blockingIssueCount,
		}

		staleMolecules = append(staleMolecules, mol)

		if blockingIssueCount > 0 {
			blockingCount++
		}
	}

	result := &MolStaleResponse{
		StaleMolecules: staleMolecules,
		TotalCount:     len(staleMolecules),
		BlockingCount:  blockingCount,
	}

	data, _ := json.Marshal(result)
	return Response{
		Success: true,
		Data:    data,
	}
}

// Gate handlers

func (s *Server) handleGateCreate(req *Request) Response {
	var args GateCreateArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid gate create args: %v", err),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available",
		}
	}

	ctx := s.reqCtx(req)
	now := time.Now()

	// Create gate issue
	gate := &types.Issue{
		Title:     args.Title,
		IssueType: "gate",
		Status:    types.StatusOpen,
		Priority:  1, // Gates are typically high priority
		Assignee:  "deacon/",
		Ephemeral:      true, // Gates are wisps (ephemeral)
		AwaitType: args.AwaitType,
		AwaitID:   args.AwaitID,
		Timeout:   args.Timeout,
		Waiters:   args.Waiters,
		CreatedAt: now,
		UpdatedAt: now,
	}
	gate.ContentHash = gate.ComputeContentHash()

	if err := store.CreateIssue(ctx, gate, s.reqActor(req)); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to create gate: %v", err),
		}
	}

	// Emit mutation event
	s.emitMutation(MutationCreate, gate.ID, gate.Title, gate.Assignee)

	data, _ := json.Marshal(GateCreateResult{ID: gate.ID})
	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleGateList(req *Request) Response {
	var args GateListArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid gate list args: %v", err),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available",
		}
	}

	ctx := s.reqCtx(req)

	// Build filter for gates
	gateType := types.IssueType("gate")
	filter := types.IssueFilter{
		IssueType: &gateType,
	}
	// By default, exclude closed gates (consistent with CLI behavior)
	if !args.All {
		filter.ExcludeStatus = []types.Status{types.StatusClosed}
	}

	gates, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to list gates: %v", err),
		}
	}

	data, _ := json.Marshal(gates)
	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleGateShow(req *Request) Response {
	var args GateShowArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid gate show args: %v", err),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available",
		}
	}

	ctx := s.reqCtx(req)

	// Resolve partial ID
	gateID, err := utils.ResolvePartialID(ctx, store, args.ID)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to resolve gate ID: %v", err),
		}
	}

	gate, err := store.GetIssue(ctx, gateID)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get gate: %v", err),
		}
	}
	if gate == nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("gate %s not found", gateID),
		}
	}
	if gate.IssueType != "gate" {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("%s is not a gate (type: %s)", gateID, gate.IssueType),
		}
	}

	data, _ := json.Marshal(gate)
	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleGateClose(req *Request) Response {
	var args GateCloseArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid gate close args: %v", err),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available",
		}
	}

	ctx := s.reqCtx(req)

	// Resolve partial ID
	gateID, err := utils.ResolvePartialID(ctx, store, args.ID)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to resolve gate ID: %v", err),
		}
	}

	// Verify it's a gate
	gate, err := store.GetIssue(ctx, gateID)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get gate: %v", err),
		}
	}
	if gate == nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("gate %s not found", gateID),
		}
	}
	if gate.IssueType != "gate" {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("%s is not a gate (type: %s)", gateID, gate.IssueType),
		}
	}

	reason := args.Reason
	if reason == "" {
		reason = "Gate closed"
	}

	oldStatus := string(gate.Status)

	if err := store.CloseIssue(ctx, gateID, reason, s.reqActor(req), ""); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to close gate: %v", err),
		}
	}

	// Emit rich status change event
	s.emitRichMutation(MutationEvent{
		Type:      MutationStatus,
		IssueID:   gateID,
		OldStatus: oldStatus,
		NewStatus: "closed",
	})

	closedGate, _ := store.GetIssue(ctx, gateID)
	data, _ := json.Marshal(closedGate)
	return Response{
		Success: true,
		Data:    data,
	}
}

func (s *Server) handleGateWait(req *Request) Response {
	var args GateWaitArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("invalid gate wait args: %v", err),
		}
	}

	store := s.storage
	if store == nil {
		return Response{
			Success: false,
			Error:   "storage not available",
		}
	}

	ctx := s.reqCtx(req)

	// Resolve partial ID
	gateID, err := utils.ResolvePartialID(ctx, store, args.ID)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to resolve gate ID: %v", err),
		}
	}

	// Get existing gate
	gate, err := store.GetIssue(ctx, gateID)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get gate: %v", err),
		}
	}
	if gate == nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("gate %s not found", gateID),
		}
	}
	if gate.IssueType != "gate" {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("%s is not a gate (type: %s)", gateID, gate.IssueType),
		}
	}
	if gate.Status == types.StatusClosed {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("gate %s is already closed", gateID),
		}
	}

	// Add new waiters (avoiding duplicates)
	waiterSet := make(map[string]bool)
	for _, w := range gate.Waiters {
		waiterSet[w] = true
	}
	newWaiters := []string{}
	for _, addr := range args.Waiters {
		if !waiterSet[addr] {
			newWaiters = append(newWaiters, addr)
			waiterSet[addr] = true
		}
	}

	addedCount := len(newWaiters)

	if addedCount > 0 {
		// Update waiters using the Storage interface
		allWaiters := append(gate.Waiters, newWaiters...)

		updates := map[string]interface{}{
			"waiters": allWaiters,
		}
		if err := store.UpdateIssue(ctx, gateID, updates, s.reqActor(req)); err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("failed to add waiters: %v", err),
			}
		}

		// Emit mutation event
		s.emitMutation(MutationUpdate, gateID, gate.Title, gate.Assignee)
	}

	data, _ := json.Marshal(GateWaitResult{AddedCount: addedCount})
	return Response{
		Success: true,
		Data:    data,
	}
}
