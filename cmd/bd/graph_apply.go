package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
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

		if store == nil {
			FatalErrorWithHint("database not initialized",
				"run 'bd doctor' to diagnose, or 'bd init' to create a new database")
		}
		if actor == "" {
			actor = "bd"
		}

		if err := validateGraphApplyPlan(&plan); err != nil {
			FatalError("invalid plan: %v", err)
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

// validateGraphApplyPlan checks the plan for structural errors before any writes.
func validateGraphApplyPlan(plan *GraphApplyPlan) error {
	if len(plan.Nodes) == 0 {
		return fmt.Errorf("plan has no nodes")
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
		// Validate MetadataRefs point to known keys.
		for metaKey, refKey := range node.MetadataRefs {
			if !seenKeys[refKey] {
				// Check if it's a forward ref (key defined later in the plan).
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
		// Validate ParentKey points to a known key.
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

	// Validate edge refs.
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
		// Build issues from nodes.
		issues := make([]*types.Issue, 0, len(plan.Nodes))
		pendingAssignees := make(map[int]string)

		for i, node := range plan.Nodes {
			issueType := types.IssueType(node.Type)
			if issueType == "" {
				issueType = types.TypeTask
			}

			// Build metadata JSON. MetadataRefs are resolved in a second pass
			// after IDs are assigned by CreateIssues.
			var metadataJSON json.RawMessage
			if len(node.Metadata) > 0 {
				raw, err := json.Marshal(node.Metadata)
				if err != nil {
					return fmt.Errorf("node %q: marshaling metadata: %w", node.Key, err)
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
		if err := tx.CreateIssues(ctx, issues, actor); err != nil {
			return fmt.Errorf("batch create: %w", err)
		}

		// Build key -> ID mapping from created issues.
		for i, node := range plan.Nodes {
			keyToID[node.Key] = issues[i].ID
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

		return nil // Triggers commit with commitMsg
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

func init() {
	graphApplyCmd.Flags().String("plan-file", "", "Path to graph apply JSON plan")
	rootCmd.AddCommand(graphApplyCmd)
}
