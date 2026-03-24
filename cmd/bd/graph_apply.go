package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
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
	From              string            `json:"from,omitempty"`
	Labels            []string          `json:"labels,omitempty"`
	Metadata          map[string]string `json:"metadata,omitempty"`
	MetadataRefs      map[string]string `json:"metadata_refs,omitempty"`
	ParentKey         string            `json:"parent_key,omitempty"`
	ParentID          string            `json:"parent_id,omitempty"`
}

// GraphApplyEdge describes a dependency edge.
type GraphApplyEdge struct {
	FromKey  string `json:"from_key,omitempty"`
	FromID   string `json:"from_id,omitempty"`
	ToKey    string `json:"to_key,omitempty"`
	ToID     string `json:"to_id,omitempty"`
	Type     string `json:"type,omitempty"`
	Metadata string `json:"metadata,omitempty"`
}

// GraphApplyResult returns the concrete bead IDs assigned to each symbolic key.
type GraphApplyResult struct {
	IDs map[string]string `json:"ids"`
}

var graphApplyCmd = &cobra.Command{
	Use:   "graph-apply",
	Short: "Apply a symbolic issue graph atomically",
	Long: `Create a batch of issues with dependencies in a single transaction.

Reads a JSON plan file describing nodes (issues to create) and edges
(dependencies between them). Nodes reference each other by symbolic keys;
the command resolves them to real issue IDs after creation.

This is much faster than sequential bd create + bd dep add calls because
all issues are created in a single database transaction.`,
	Run: func(cmd *cobra.Command, args []string) {
		planFile, _ := cmd.Flags().GetString("plan-file")
		if planFile == "" {
			FatalError("--plan-file is required")
		}

		data, err := os.ReadFile(planFile) // #nosec G304 -- user-provided path is intentional
		if err != nil {
			FatalError("reading plan file: %v", err)
		}

		var plan GraphApplyPlan
		if err := json.Unmarshal(data, &plan); err != nil {
			FatalError("parsing plan file: %v", err)
		}

		if len(plan.Nodes) == 0 {
			FatalError("plan has no nodes")
		}

		if store == nil {
			FatalErrorWithHint("database not initialized",
				"run 'bd doctor' to diagnose, or 'bd init' to create a new database")
		}
		if actor == "" {
			actor = "bd"
		}

		result, err := executeGraphApply(rootCtx, &plan)
		if err != nil {
			FatalError("graph-apply: %v", err)
		}

		if jsonOutput {
			outputJSON(result)
		} else {
			fmt.Printf("Created %d issues\n", len(result.IDs))
			for key, id := range result.IDs {
				fmt.Printf("  %s -> %s\n", key, id)
			}
		}
	},
}

func executeGraphApply(ctx context.Context, plan *GraphApplyPlan) (*GraphApplyResult, error) {
	keyToID := make(map[string]string, len(plan.Nodes))

	// Build issues from nodes.
	issues := make([]*types.Issue, 0, len(plan.Nodes))
	pendingAssignees := make(map[int]string)

	for i, node := range plan.Nodes {
		if node.Key == "" {
			return nil, fmt.Errorf("node %d has empty key", i)
		}
		if node.Title == "" {
			return nil, fmt.Errorf("node %q has empty title", node.Key)
		}

		issueType := types.IssueType(node.Type)
		if issueType == "" {
			issueType = types.TypeTask
		}

		// Build metadata JSON from the string map.
		var metadataJSON json.RawMessage
		meta := make(map[string]string, len(node.Metadata))
		for k, v := range node.Metadata {
			meta[k] = v
		}
		// Resolve backward MetadataRefs (keys already created).
		for metaKey, refKey := range node.MetadataRefs {
			if resolvedID, ok := keyToID[refKey]; ok {
				meta[metaKey] = resolvedID
			}
		}
		if len(meta) > 0 {
			raw, err := json.Marshal(meta)
			if err != nil {
				return nil, fmt.Errorf("node %q: marshaling metadata: %w", node.Key, err)
			}
			metadataJSON = raw
		}

		issue := &types.Issue{
			Title:     node.Title,
			IssueType: issueType,
			Status:    types.StatusOpen,
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

	// Batch-create all issues in a single transaction.
	if err := store.CreateIssues(ctx, issues, actor); err != nil {
		return nil, fmt.Errorf("batch create: %w", err)
	}

	// Build key -> ID mapping from created issues.
	for i, node := range plan.Nodes {
		keyToID[node.Key] = issues[i].ID
	}

	// Second pass: resolve forward MetadataRefs (keys created after the referencing node).
	for i, node := range plan.Nodes {
		if len(node.MetadataRefs) == 0 {
			continue
		}
		needsUpdate := false
		mergedMeta := make(map[string]string)
		if issues[i].Metadata != nil {
			if err := json.Unmarshal(issues[i].Metadata, &mergedMeta); err != nil {
				return nil, fmt.Errorf("node %q: re-parsing metadata: %w", node.Key, err)
			}
		}
		for metaKey, refKey := range node.MetadataRefs {
			resolvedID, ok := keyToID[refKey]
			if !ok {
				return nil, fmt.Errorf("node %q: metadata ref %q references unknown key %q", node.Key, metaKey, refKey)
			}
			if mergedMeta[metaKey] != resolvedID {
				mergedMeta[metaKey] = resolvedID
				needsUpdate = true
			}
		}
		if needsUpdate {
			metaJSON, err := json.Marshal(mergedMeta)
			if err != nil {
				return nil, fmt.Errorf("node %q: marshaling updated metadata: %w", node.Key, err)
			}
			updates := map[string]interface{}{
				"metadata": json.RawMessage(metaJSON),
			}
			if err := store.UpdateIssue(ctx, issues[i].ID, updates, actor); err != nil {
				return nil, fmt.Errorf("node %q: updating metadata refs: %w", node.Key, err)
			}
		}
	}

	// Add dependencies from edges.
	for _, edge := range plan.Edges {
		fromID := resolveEdgeRef(edge.FromKey, edge.FromID, keyToID)
		toID := resolveEdgeRef(edge.ToKey, edge.ToID, keyToID)
		if fromID == "" || toID == "" {
			return nil, fmt.Errorf("edge has unresolved refs: from=%q/%q to=%q/%q",
				edge.FromKey, edge.FromID, edge.ToKey, edge.ToID)
		}
		depType := types.DependencyType(edge.Type)
		if depType == "" {
			depType = types.DepBlocks
		}
		dep := &types.Dependency{
			IssueID:     fromID,
			DependsOnID: toID,
			Type:        depType,
		}
		if err := store.AddDependency(ctx, dep, actor); err != nil {
			return nil, fmt.Errorf("adding edge %s->%s: %w", fromID, toID, err)
		}
	}

	// Add parent-child dependencies.
	for i, node := range plan.Nodes {
		parentID := node.ParentID
		if node.ParentKey != "" {
			resolved, ok := keyToID[node.ParentKey]
			if !ok {
				return nil, fmt.Errorf("node %q: parent key %q not found", node.Key, node.ParentKey)
			}
			parentID = resolved
		}
		if parentID != "" {
			dep := &types.Dependency{
				IssueID:     issues[i].ID,
				DependsOnID: parentID,
				Type:        types.DepParentChild,
			}
			if err := store.AddDependency(ctx, dep, actor); err != nil {
				return nil, fmt.Errorf("node %q: adding parent-child dep: %w", node.Key, err)
			}
		}
	}

	// Apply deferred assignees.
	for i, assignee := range pendingAssignees {
		updates := map[string]interface{}{
			"assignee": assignee,
		}
		if err := store.UpdateIssue(ctx, issues[i].ID, updates, actor); err != nil {
			return nil, fmt.Errorf("node %q: setting assignee: %w", plan.Nodes[i].Key, err)
		}
	}

	// Commit the full batch.
	commitMsg := plan.CommitMessage
	if commitMsg == "" {
		commitMsg = fmt.Sprintf("bd: graph-apply %d nodes", len(plan.Nodes))
	}
	if err := store.Commit(ctx, commitMsg); err != nil && !isDoltNothingToCommit(err) {
		return nil, fmt.Errorf("commit: %w", err)
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

func init() {
	graphApplyCmd.Flags().String("plan-file", "", "Path to graph apply JSON plan")
	rootCmd.AddCommand(graphApplyCmd)
}
