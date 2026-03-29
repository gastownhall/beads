package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// GraphApplyPlan describes a symbolic bead graph to create atomically.
type GraphApplyPlan struct {
	CommitMessage string           `json:"commit_message,omitempty"`
	Nodes         []GraphApplyNode `json:"nodes"`
	Edges         []GraphApplyEdge `json:"edges,omitempty"`
}

// GraphApplyNode describes a single bead to create.
type GraphApplyNode struct {
	Key               string            `json:"key"`
	Title             string            `json:"title"`
	Type              string            `json:"type,omitempty"`
	Description       string            `json:"description,omitempty"`
	Assignee          string            `json:"assignee,omitempty"`
	AssignAfterCreate bool              `json:"assign_after_create,omitempty"`
	Priority          *int              `json:"priority,omitempty"` // nil defaults to P2
	Labels            []string          `json:"labels,omitempty"`
	Metadata          map[string]string `json:"metadata,omitempty"`
	MetadataRefs      map[string]string `json:"metadata_refs,omitempty"`
	ParentKey         string            `json:"parent_key,omitempty"`
	ParentID          string            `json:"parent_id,omitempty"`
}

// GraphApplyEdge describes a dependency edge.
type GraphApplyEdge struct {
	FromKey string `json:"from_key,omitempty"`
	FromID  string `json:"from_id,omitempty"`
	ToKey   string `json:"to_key,omitempty"`
	ToID    string `json:"to_id,omitempty"`
	Type    string `json:"type,omitempty"`
}

// GraphApplyResult returns the concrete bead IDs assigned to each symbolic key.
type GraphApplyResult struct {
	IDs map[string]string `json:"ids"`
}

// createIssuesFromGraph handles `bd create --graph <plan-file>`.
func createIssuesFromGraph(planFile string) {
	data, err := os.ReadFile(planFile) // #nosec G304 -- user-provided path is intentional
	if err != nil {
		FatalError("reading graph plan: %v", err)
	}

	var plan GraphApplyPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		FatalError("parsing graph plan: %v", err)
	}

	if err := validateGraphApplyPlan(&plan); err != nil {
		FatalError("invalid graph plan: %v", err)
	}

	result, err := executeGraphApply(rootCtx, &plan)
	if err != nil {
		FatalError("graph create: %v", err)
	}

	if jsonOutput {
		outputJSON(result)
	} else {
		fmt.Printf("Created %d issues\n", len(result.IDs))
		keys := make([]string, 0, len(result.IDs))
		for key := range result.IDs {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Printf("  %s -> %s\n", key, result.IDs[key])
		}
	}
}

// validateGraphApplyPlan checks the plan for structural errors before any writes.
func validateGraphApplyPlan(plan *GraphApplyPlan) error {
	if len(plan.Nodes) == 0 {
		return fmt.Errorf("plan has no nodes")
	}

	// Load custom types so user-configured types (spec, session, etc.) are accepted.
	var customTypes []string
	if store != nil {
		ct, _ := store.GetCustomTypes(rootCtx)
		customTypes = ct
	}
	if len(customTypes) == 0 {
		customTypes = config.GetCustomTypesFromYAML()
	}

	seenKeys := make(map[string]bool, len(plan.Nodes))
	for i, node := range plan.Nodes {
		if node.Key == "" {
			return fmt.Errorf("node %d has empty key", i)
		}
		if seenKeys[node.Key] {
			return fmt.Errorf("duplicate node key %q", node.Key)
		}
		seenKeys[node.Key] = true
		if node.Title == "" {
			return fmt.Errorf("node %q has empty title", node.Key)
		}
		if node.Type != "" {
			it := types.IssueType(node.Type)
			if !it.IsValidWithCustom(customTypes) {
				return fmt.Errorf("node %q: invalid type %q", node.Key, node.Type)
			}
		}
		// Validate MetadataRefs point to known keys.
		for metaKey, refKey := range node.MetadataRefs {
			if !seenKeys[refKey] {
				found := false
				for _, other := range plan.Nodes {
					if other.Key == refKey {
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("node %q: metadata ref %q references unknown key %q", node.Key, metaKey, refKey)
				}
			}
		}
		if node.ParentKey != "" && !seenKeys[node.ParentKey] {
			found := false
			for _, other := range plan.Nodes {
				if other.Key == node.ParentKey {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("node %q: parent key %q not found in plan", node.Key, node.ParentKey)
			}
		}
	}

	for i, edge := range plan.Edges {
		if edge.FromKey != "" && !seenKeys[edge.FromKey] {
			return fmt.Errorf("edge %d: from key %q not found in plan", i, edge.FromKey)
		}
		if edge.ToKey != "" && !seenKeys[edge.ToKey] {
			return fmt.Errorf("edge %d: to key %q not found in plan", i, edge.ToKey)
		}
		if edge.FromKey == "" && edge.FromID == "" {
			return fmt.Errorf("edge %d: must specify from_key or from_id", i)
		}
		if edge.ToKey == "" && edge.ToID == "" {
			return fmt.Errorf("edge %d: must specify to_key or to_id", i)
		}
		if edge.Type != "" {
			dt := types.DependencyType(edge.Type)
			if !dt.IsValid() {
				return fmt.Errorf("edge %d: invalid dependency type %q", i, edge.Type)
			}
		}
	}

	return nil
}

func executeGraphApply(ctx context.Context, plan *GraphApplyPlan) (*GraphApplyResult, error) {
	keyToID := make(map[string]string, len(plan.Nodes))

	commitMsg := plan.CommitMessage
	if commitMsg == "" {
		commitMsg = fmt.Sprintf("bd: graph-apply %d nodes", len(plan.Nodes))
	}

	if err := store.RunInTransaction(ctx, commitMsg, func(tx storage.Transaction) error {
		issues := make([]*types.Issue, 0, len(plan.Nodes))
		pendingAssignees := make(map[int]string)

		for i, node := range plan.Nodes {
			issueType := types.IssueType(node.Type)
			if issueType == "" {
				issueType = types.TypeTask
			}

			var metadataJSON json.RawMessage
			if len(node.Metadata) > 0 {
				raw, err := json.Marshal(node.Metadata)
				if err != nil {
					return fmt.Errorf("node %q: marshaling metadata: %w", node.Key, err)
				}
				metadataJSON = raw
			}

			priority := 2 // Default P2
			if node.Priority != nil {
				priority = *node.Priority
			}

			issue := &types.Issue{
				Title:     node.Title,
				IssueType: issueType,
				Status:    types.StatusOpen,
				Priority:  priority,
				Labels:    node.Labels,
				Metadata:  metadataJSON,
			}
			if node.Description != "" {
				issue.Description = node.Description
			}
			if node.Assignee != "" {
				if node.AssignAfterCreate {
					pendingAssignees[i] = node.Assignee
				} else {
					issue.Assignee = node.Assignee
				}
			}

			issues = append(issues, issue)
		}

		if err := tx.CreateIssues(ctx, issues, actor); err != nil {
			return fmt.Errorf("batch create: %w", err)
		}

		for i, node := range plan.Nodes {
			keyToID[node.Key] = issues[i].ID
		}

		// Persist labels.
		for i, node := range plan.Nodes {
			for _, label := range node.Labels {
				if err := tx.AddLabel(ctx, issues[i].ID, label, actor); err != nil {
					return fmt.Errorf("node %q: adding label %q: %w", node.Key, label, err)
				}
			}
		}

		// Resolve MetadataRefs now that all IDs are known.
		for i, node := range plan.Nodes {
			if len(node.MetadataRefs) == 0 {
				continue
			}
			mergedMeta := make(map[string]string)
			if issues[i].Metadata != nil {
				if err := json.Unmarshal(issues[i].Metadata, &mergedMeta); err != nil {
					return fmt.Errorf("node %q: re-parsing metadata: %w", node.Key, err)
				}
			}
			for metaKey, refKey := range node.MetadataRefs {
				mergedMeta[metaKey] = keyToID[refKey]
			}
			metaJSON, err := json.Marshal(mergedMeta)
			if err != nil {
				return fmt.Errorf("node %q: marshaling updated metadata: %w", node.Key, err)
			}
			updates := map[string]interface{}{
				"metadata": json.RawMessage(metaJSON),
			}
			if err := tx.UpdateIssue(ctx, issues[i].ID, updates, actor); err != nil {
				return fmt.Errorf("node %q: updating metadata refs: %w", node.Key, err)
			}
		}

		// Add dependencies from edges.
		for _, edge := range plan.Edges {
			fromID := resolveEdgeRef(edge.FromKey, edge.FromID, keyToID)
			toID := resolveEdgeRef(edge.ToKey, edge.ToID, keyToID)
			depType := types.DependencyType(edge.Type)
			if depType == "" {
				depType = types.DepBlocks
			}
			dep := &types.Dependency{
				IssueID:     fromID,
				DependsOnID: toID,
				Type:        depType,
			}
			if err := tx.AddDependency(ctx, dep, actor); err != nil {
				return fmt.Errorf("adding edge %s->%s: %w", fromID, toID, err)
			}
		}

		// Add parent-child dependencies.
		for i, node := range plan.Nodes {
			parentID := node.ParentID
			if node.ParentKey != "" {
				parentID = keyToID[node.ParentKey]
			}
			if parentID != "" {
				dep := &types.Dependency{
					IssueID:     issues[i].ID,
					DependsOnID: parentID,
					Type:        types.DepParentChild,
				}
				if err := tx.AddDependency(ctx, dep, actor); err != nil {
					return fmt.Errorf("node %q: adding parent-child dep: %w", node.Key, err)
				}
			}
		}

		// Apply deferred assignees.
		for i, assignee := range pendingAssignees {
			updates := map[string]interface{}{
				"assignee": assignee,
			}
			if err := tx.UpdateIssue(ctx, issues[i].ID, updates, actor); err != nil {
				return fmt.Errorf("node %q: setting assignee: %w", plan.Nodes[i].Key, err)
			}
		}

		return nil
	}); err != nil {
		return nil, err
	}

	return &GraphApplyResult{IDs: keyToID}, nil
}

func resolveEdgeRef(key, id string, keyToID map[string]string) string {
	if id != "" {
		return id
	}
	if key != "" {
		return keyToID[key]
	}
	return ""
}
