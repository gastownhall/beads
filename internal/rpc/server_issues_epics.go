package rpc

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/util"
	"github.com/steveyegge/beads/internal/utils"
)

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

func updatesFromArgs(a UpdateArgs) map[string]interface{} {
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
	return u
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

	var design, acceptance, assignee, externalRef *string
	if createArgs.Design != "" {
		design = &createArgs.Design
	}
	if createArgs.AcceptanceCriteria != "" {
		acceptance = &createArgs.AcceptanceCriteria
	}
	if createArgs.Assignee != "" {
		assignee = &createArgs.Assignee
	}
	if createArgs.ExternalRef != "" {
		externalRef = &createArgs.ExternalRef
	}

	issue := &types.Issue{
		ID:                 issueID,
		Title:              createArgs.Title,
		Description:        createArgs.Description,
		IssueType:          types.IssueType(createArgs.IssueType),
		Priority:           createArgs.Priority,
		Design:             strValue(design),
		AcceptanceCriteria: strValue(acceptance),
		Assignee:           strValue(assignee),
		ExternalRef:        externalRef,
		EstimatedMinutes:   createArgs.EstimatedMinutes,
		Status:             types.StatusOpen,
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

	// Add labels if specified
	for _, label := range createArgs.Labels {
		if err := store.AddLabel(ctx, issue.ID, label, s.reqActor(req)); err != nil {
			return Response{
				Success: false,
				Error:   fmt.Sprintf("failed to add label %s: %v", label, err),
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

	// Emit mutation event for event-driven daemon
	s.emitMutation(MutationCreate, issue.ID)

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
	updates := updatesFromArgs(updateArgs)
	actor := s.reqActor(req)

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

	// Emit mutation event for event-driven daemon (only if any updates or label operations were performed)
	if len(updates) > 0 || len(updateArgs.SetLabels) > 0 || len(updateArgs.AddLabels) > 0 || len(updateArgs.RemoveLabels) > 0 {
		s.emitMutation(MutationUpdate, updateArgs.ID)
	}

	issue, err := store.GetIssue(ctx, updateArgs.ID)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get updated issue: %v", err),
		}
	}

	data, _ := json.Marshal(issue)
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
	if err := store.CloseIssue(ctx, closeArgs.ID, closeArgs.Reason, s.reqActor(req)); err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to close issue: %v", err),
		}
	}

	// Emit mutation event for event-driven daemon
	s.emitMutation(MutationUpdate, closeArgs.ID)

	issue, _ := store.GetIssue(ctx, closeArgs.ID)
	data, _ := json.Marshal(issue)
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
	var deps []*types.IssueWithDependencyMetadata
	var dependents []*types.IssueWithDependencyMetadata
	if sqliteStore, ok := store.(*sqlite.SQLiteStorage); ok {
		deps, _ = sqliteStore.GetDependenciesWithMetadata(ctx, issue.ID)
		dependents, _ = sqliteStore.GetDependentsWithMetadata(ctx, issue.ID)
	} else {
		// Fallback for non-SQLite storage (won't have dependency type metadata)
		regularDeps, _ := store.GetDependencies(ctx, issue.ID)
		for _, d := range regularDeps {
			deps = append(deps, &types.IssueWithDependencyMetadata{
				Issue:          *d,
				DependencyType: types.DepBlocks, // default
			})
		}
		regularDependents, _ := store.GetDependents(ctx, issue.ID)
		for _, d := range regularDependents {
			dependents = append(dependents, &types.IssueWithDependencyMetadata{
				Issue:          *d,
				DependencyType: types.DepBlocks, // default
			})
		}
	}

	// Create detailed response with related data
	type IssueDetails struct {
		*types.Issue
		Labels       []string                              `json:"labels,omitempty"`
		Dependencies []*types.IssueWithDependencyMetadata `json:"dependencies,omitempty"`
		Dependents   []*types.IssueWithDependencyMetadata `json:"dependents,omitempty"`
	}

	details := &IssueDetails{
		Issue:        issue,
		Labels:       labels,
		Dependencies: deps,
		Dependents:   dependents,
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
		Status:     types.StatusOpen,
		Priority:   readyArgs.Priority,
		Unassigned: readyArgs.Unassigned,
		Limit:      readyArgs.Limit,
		SortPolicy: types.SortPolicy(readyArgs.SortPolicy),
		Labels:     util.NormalizeLabels(readyArgs.Labels),
		LabelsAny:  util.NormalizeLabels(readyArgs.LabelsAny),
	}
	if readyArgs.Assignee != "" && !readyArgs.Unassigned {
		wf.Assignee = &readyArgs.Assignee
	}

	ctx := s.reqCtx(req)
	issues, err := store.GetReadyWork(ctx, wf)
	if err != nil {
		return Response{
			Success: false,
			Error:   fmt.Sprintf("failed to get ready work: %v", err),
		}
	}

	data, _ := json.Marshal(issues)
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
