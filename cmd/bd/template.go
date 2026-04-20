package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/formula"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// BeadsTemplateLabel is the label used to identify Beads-based templates
const BeadsTemplateLabel = "template"

// variablePattern matches {{variable}} placeholders
var variablePattern = regexp.MustCompile(`\{\{([a-zA-Z_][a-zA-Z0-9_]*)\}\}`)

// TemplateSubgraph holds a template epic and all its descendants
type TemplateSubgraph struct {
	Root         *types.Issue              // The template epic
	Issues       []*types.Issue            // All issues in the subgraph (including root)
	Dependencies []*types.Dependency       // All dependencies within the subgraph
	IssueMap     map[string]*types.Issue   // ID -> Issue for quick lookup
	VarDefs      map[string]formula.VarDef // Variable definitions from formula (for defaults)
	Phase        string                    // Recommended phase: "liquid" (pour) or "vapor" (wisp)
	Pour         bool                      // If true, steps should be materialized as sub-issues (from formula pour=true)
}

// InstantiateResult holds the result of template instantiation
type InstantiateResult struct {
	NewEpicID string            `json:"new_epic_id"`
	IDMapping map[string]string `json:"id_mapping"` // old ID -> new ID
	Created   int               `json:"created"`    // number of issues created
}

// CloneOptions controls how the subgraph is cloned during spawn/bond
type CloneOptions struct {
	Vars      map[string]string // Variable substitutions for {{key}} placeholders
	Assignee  string            // Assign the root epic to this agent/user
	Actor     string            // Actor performing the operation
	Ephemeral bool              // If true, spawned issues are marked for bulk deletion
	Prefix    string            // Override prefix for ID generation (bd-hobo: distinct prefixes)

	// Dynamic bonding fields (for Christmas Ornament pattern)
	ParentID string // Parent molecule ID to bond under (e.g., "patrol-x7k")
	ChildRef string // Child reference with variables (e.g., "arm-{{polecat_name}}")

	// Atomic attachment: if set, adds a dependency from the spawned root to
	// AttachToID within the same transaction as the clone, preventing orphans.
	AttachToID    string               // Molecule ID to attach spawned root to
	AttachDepType types.DependencyType // Dependency type for the attachment

	// RootOnly: if true, only create the root issue (no child step issues).
	// Used by patrol wisps where steps are inlined at prime time, not tracked as beads.
	RootOnly bool
}

// bondedIDPattern validates bonded IDs (alphanumeric, dash, underscore, dot)
var bondedIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

// =============================================================================
// Beads Template Functions
// =============================================================================

// loadTemplateSubgraph loads a template epic and all its descendants
func loadTemplateSubgraph(ctx context.Context, s storage.DoltStorage, templateID string) (*TemplateSubgraph, error) {
	if s == nil {
		return nil, fmt.Errorf("no database connection")
	}

	// Get the root issue
	root, err := s.GetIssue(ctx, templateID)
	if err != nil {
		return nil, fmt.Errorf("failed to get template: %w", err)
	}
	if root == nil {
		return nil, fmt.Errorf("template %s not found", templateID)
	}

	subgraph := &TemplateSubgraph{
		Root:     root,
		Issues:   []*types.Issue{root},
		IssueMap: map[string]*types.Issue{root.ID: root},
	}

	// Recursively load all children (with cycle detection, GH#2719)
	visited := map[string]bool{root.ID: true}
	if err := loadDescendants(ctx, s, subgraph, root.ID, visited); err != nil {
		return nil, err
	}

	// Load all dependencies within the subgraph
	for _, issue := range subgraph.Issues {
		deps, err := s.GetDependencyRecords(ctx, issue.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to get dependencies for %s: %w", issue.ID, err)
		}
		for _, dep := range deps {
			// Only include dependencies where both ends are in the subgraph
			if _, ok := subgraph.IssueMap[dep.DependsOnID]; ok {
				subgraph.Dependencies = append(subgraph.Dependencies, dep)
			}
		}
	}

	return subgraph, nil
}

// loadDescendants recursively loads all child issues.
// It uses two strategies to find children:
// 1. Check dependency records for parent-child relationships
// 2. Check for hierarchical IDs (parent.N) to catch children with missing/wrong deps
//
// The visited set tracks IDs already expanded to detect cycles (GH#2719).
// Without this, cyclic parent-child dependencies cause unbounded recursion leading to OOM.
func loadDescendants(ctx context.Context, s storage.DoltStorage, subgraph *TemplateSubgraph, parentID string, visited map[string]bool) error {
	// Track children we've already added to avoid duplicates
	addedChildren := make(map[string]bool)

	// Strategy 1: Get direct parent-child dependents with relationship metadata.
	dependents, err := s.GetDependentsWithMetadata(ctx, parentID)
	if err != nil {
		return fmt.Errorf("failed to get dependents of %s: %w", parentID, err)
	}

	// Only keep explicit parent-child relationships.
	for _, dependent := range dependents {
		if dependent.DependencyType != types.DepParentChild {
			continue
		}

		if _, exists := subgraph.IssueMap[dependent.ID]; exists {
			continue // Already in subgraph
		}

		// Cycle detection (GH#2719)
		if visited[dependent.ID] {
			continue
		}

		child := dependent.Issue

		// Add to subgraph
		subgraph.Issues = append(subgraph.Issues, &child)
		subgraph.IssueMap[child.ID] = &child
		addedChildren[child.ID] = true

		// Mark visited before recursing
		visited[child.ID] = true
		if err := loadDescendants(ctx, s, subgraph, child.ID, visited); err != nil {
			return err
		}
	}

	// Strategy 2: Find hierarchical children by ID pattern
	// This catches children that have missing or incorrect dependency types.
	// Hierarchical IDs follow the pattern: parentID.N (e.g., "gt-abc.1", "gt-abc.2")
	hierarchicalChildren, err := findHierarchicalChildren(ctx, s, parentID)
	if err != nil {
		// Non-fatal: continue with what we have
		return nil
	}

	for _, child := range hierarchicalChildren {
		if addedChildren[child.ID] {
			continue // Already added via dependency
		}
		if _, exists := subgraph.IssueMap[child.ID]; exists {
			continue // Already in subgraph
		}

		// Cycle detection (GH#2719)
		if visited[child.ID] {
			continue
		}

		// Check if this hierarchical child has been reparented to a different parent (GH#2476).
		// If it has an explicit parent-child dependency pointing elsewhere, skip it —
		// the ID pattern match is stale and the child belongs to another molecule.
		depRecs, err := s.GetDependencyRecords(ctx, child.ID)
		if err == nil {
			reparented := false
			for _, dep := range depRecs {
				if dep.Type == types.DepParentChild && dep.DependsOnID != parentID {
					reparented = true
					break
				}
			}
			if reparented {
				continue
			}
		}

		// Add to subgraph
		subgraph.Issues = append(subgraph.Issues, child)
		subgraph.IssueMap[child.ID] = child
		addedChildren[child.ID] = true

		// Mark visited before recursing
		visited[child.ID] = true
		if err := loadDescendants(ctx, s, subgraph, child.ID, visited); err != nil {
			return err
		}
	}

	return nil
}

// findHierarchicalChildren finds issues with IDs that match the pattern parentID.N
// This catches hierarchical children that may be missing parent-child dependencies.
func findHierarchicalChildren(ctx context.Context, s storage.DoltStorage, parentID string) ([]*types.Issue, error) {
	pattern := parentID + "."
	candidates, err := s.SearchIssues(ctx, "", types.IssueFilter{IDPrefix: pattern})
	if err != nil {
		return nil, err
	}

	var children []*types.Issue
	for _, issue := range candidates {
		_, directParentID, depth := types.ParseHierarchicalID(issue.ID)
		if depth > 0 && directParentID == parentID {
			children = append(children, issue)
		}
	}

	return children, nil
}

// =============================================================================
// Proto Lookup Functions
// =============================================================================

// resolveProtoIDOrTitle resolves a proto by ID or title.
// It first tries to resolve as an ID (via ResolvePartialID).
// If that fails, it searches for protos with matching titles.
// Returns the proto ID if found, or an error if not found or ambiguous.
func resolveProtoIDOrTitle(ctx context.Context, s storage.DoltStorage, input string) (string, error) {
	// Strategy 1: Try to resolve as an ID
	protoID, err := utils.ResolvePartialID(ctx, s, input)
	if err == nil {
		// Verify it's a proto (has template label)
		issue, getErr := s.GetIssue(ctx, protoID)
		if getErr == nil && issue != nil {
			labels, _ := s.GetLabels(ctx, protoID)
			for _, label := range labels {
				if label == BeadsTemplateLabel {
					return protoID, nil // Found a valid proto by ID
				}
			}
		}
		// ID resolved but not a proto - continue to title search
	}

	// Strategy 2: Search for protos by title
	protos, err := s.GetIssuesByLabel(ctx, BeadsTemplateLabel)
	if err != nil {
		return "", fmt.Errorf("failed to search protos: %w", err)
	}

	var matches []*types.Issue
	var exactMatch *types.Issue

	for _, proto := range protos {
		// Check for exact title match (case-insensitive)
		if strings.EqualFold(proto.Title, input) {
			exactMatch = proto
			break
		}
		// Check for partial title match (case-insensitive)
		if strings.Contains(strings.ToLower(proto.Title), strings.ToLower(input)) {
			matches = append(matches, proto)
		}
	}

	if exactMatch != nil {
		return exactMatch.ID, nil
	}

	if len(matches) == 0 {
		return "", fmt.Errorf("no proto found matching %q (by ID or title)", input)
	}

	if len(matches) == 1 {
		return matches[0].ID, nil
	}

	// Multiple matches - show them all for disambiguation
	var matchNames []string
	for _, m := range matches {
		matchNames = append(matchNames, fmt.Sprintf("%s: %s", m.ID, m.Title))
	}
	return "", fmt.Errorf("ambiguous: %q matches %d protos:\n  %s\nUse the ID or a more specific title", input, len(matches), strings.Join(matchNames, "\n  "))
}

// extractVariables finds all {{variable}} patterns in text.
// Handlebars control keywords like "else", "this" are excluded.
func extractVariables(text string) []string {
	matches := variablePattern.FindAllStringSubmatch(text, -1)
	seen := make(map[string]bool)
	var vars []string
	for _, match := range matches {
		if len(match) >= 2 && !seen[match[1]] {
			name := match[1]
			// Skip Handlebars control keywords
			if isHandlebarsKeyword(name) {
				continue
			}
			vars = append(vars, name)
			seen[name] = true
		}
	}
	return vars
}

// isHandlebarsKeyword returns true for Handlebars control keywords
// that look like variables but aren't (e.g., "else", "this").
func isHandlebarsKeyword(name string) bool {
	switch name {
	case "else", "this", "root", "index", "key", "first", "last":
		return true
	default:
		return false
	}
}

// extractAllVariables finds all variables across the entire subgraph
func extractAllVariables(subgraph *TemplateSubgraph) []string {
	allText := ""
	for _, issue := range subgraph.Issues {
		allText += issue.Title + " " + issue.Description + " "
		allText += issue.Design + " " + issue.AcceptanceCriteria + " " + issue.Notes + " "
	}
	return extractVariables(allText)
}

// extractRequiredVariables returns only variables that don't have defaults.
// If VarDefs is available (from a cooked formula), uses it to filter out defaulted vars.
// Otherwise, falls back to returning all variables.
func extractRequiredVariables(subgraph *TemplateSubgraph) []string {
	allVars := extractAllVariables(subgraph)

	// If no VarDefs, assume all variables are required (legacy template behavior)
	if subgraph.VarDefs == nil {
		return allVars
	}

	// VarDefs exists (from a cooked formula) - only declared variables matter.
	// Variables in text but NOT in VarDefs are ignored - they're documentation
	// handlebars meant for LLM agents, not formula input variables (gt-ky9loa).
	var required []string
	for _, v := range allVars {
		def, exists := subgraph.VarDefs[v]
		if !exists {
			// Not a declared formula variable - skip (documentation handlebars)
			continue
		}
		// A declared variable is required if it has no default.
		// nil Default = no default specified (must provide).
		// Non-nil Default (including &"") = has explicit default (optional).
		if def.Default == nil {
			required = append(required, v)
		}
	}
	return required
}

// applyVariableDefaults merges formula default values with provided variables.
// Returns a new map with defaults applied for any missing variables.
func applyVariableDefaults(vars map[string]string, subgraph *TemplateSubgraph) map[string]string {
	if subgraph.VarDefs == nil {
		return vars
	}

	result := make(map[string]string)
	for k, v := range vars {
		result[k] = v
	}

	// Apply defaults for missing variables (including empty-string defaults)
	for name, def := range subgraph.VarDefs {
		if _, exists := result[name]; !exists && def.Default != nil {
			result[name] = *def.Default
		}
	}

	return result
}

// substituteVariables replaces {{variable}} with values
func substituteVariables(text string, vars map[string]string) string {
	return variablePattern.ReplaceAllStringFunc(text, func(match string) string {
		// Extract variable name from {{name}}
		name := match[2 : len(match)-2]
		if val, ok := vars[name]; ok {
			return val
		}
		return match // Leave unchanged if not found
	})
}

// generateBondedID creates a custom ID for dynamically bonded molecules.
// When bonding a proto to a parent molecule, this generates IDs like:
//   - Root: parent.childref (e.g., "patrol-x7k.arm-ace")
//   - Children: parent.childref.step (e.g., "patrol-x7k.arm-ace.capture")
//
// The childRef is variable-substituted before use.
// Returns empty string if not a bonded operation (opts.ParentID empty).
func generateBondedID(oldID string, rootID string, opts CloneOptions) (string, error) {
	if opts.ParentID == "" {
		return "", nil // Not a bonded operation
	}

	// Substitute variables in childRef
	childRef := substituteVariables(opts.ChildRef, opts.Vars)

	// Validate childRef after substitution
	if childRef == "" {
		return "", fmt.Errorf("childRef is empty after variable substitution")
	}
	if !bondedIDPattern.MatchString(childRef) {
		return "", fmt.Errorf("invalid childRef '%s': must be alphanumeric, dash, underscore, or dot only", childRef)
	}

	if oldID == rootID {
		// Root issue: parent.childref
		newID := fmt.Sprintf("%s.%s", opts.ParentID, childRef)
		return newID, nil
	}

	// Child issue: parent.childref.relative
	// Extract the relative portion of the old ID (part after root)
	relativeID := getRelativeID(oldID, rootID)
	if relativeID == "" {
		// No hierarchical relationship - use a suffix from the old ID to ensure uniqueness.
		// Extract the last part of the old ID (after any prefix or dash)
		suffix := extractIDSuffix(oldID)
		newID := fmt.Sprintf("%s.%s.%s", opts.ParentID, childRef, suffix)
		return newID, nil
	}

	newID := fmt.Sprintf("%s.%s.%s", opts.ParentID, childRef, relativeID)
	return newID, nil
}

// extractIDSuffix extracts a suffix from an ID for use when IDs aren't hierarchical.
// For "patrol-abc123", returns "abc123".
// For "bd-xyz.1", returns "1".
// This ensures child IDs remain unique when bonding.
func extractIDSuffix(id string) string {
	// First try to get the part after the last dot (for hierarchical IDs)
	if lastDot := strings.LastIndex(id, "."); lastDot >= 0 {
		return id[lastDot+1:]
	}
	// Otherwise, get the part after the last dash (for prefix-hash IDs)
	if lastDash := strings.LastIndex(id, "-"); lastDash >= 0 {
		return id[lastDash+1:]
	}
	// Fallback: use the whole ID
	return id
}

// getRelativeID extracts the relative portion of a child ID from its parent.
// For example: getRelativeID("bd-abc.step1.sub", "bd-abc") returns "step1.sub"
// Returns empty string if oldID equals rootID or doesn't start with rootID.
func getRelativeID(oldID, rootID string) string {
	if oldID == rootID {
		return ""
	}
	// Check if oldID starts with rootID followed by a dot
	prefix := rootID + "."
	if strings.HasPrefix(oldID, prefix) {
		return oldID[len(prefix):]
	}
	return ""
}

// cloneSubgraph creates new issues from the template with variable substitution.
// Uses CloneOptions to control all spawn/bond behavior including dynamic bonding.
func cloneSubgraph(ctx context.Context, s storage.DoltStorage, subgraph *TemplateSubgraph, opts CloneOptions) (*InstantiateResult, error) {
	if s == nil {
		return nil, fmt.Errorf("no database connection")
	}

	// Generate new IDs and create mapping
	idMapping := make(map[string]string)

	// Use transaction for atomicity
	err := transact(ctx, s, "bd: clone template subgraph", func(tx storage.Transaction) error {
		// First pass: create all issues with new IDs
		for _, oldIssue := range subgraph.Issues {
			// RootOnly: skip child issues, only create the root
			if opts.RootOnly && oldIssue.ID != subgraph.Root.ID {
				continue
			}
			// Determine assignee: use override for root epic, otherwise keep template's
			issueAssignee := oldIssue.Assignee
			if oldIssue.ID == subgraph.Root.ID && opts.Assignee != "" {
				issueAssignee = opts.Assignee
			}

			newIssue := &types.Issue{
				// ID will be set below based on bonding options
				Title:              substituteVariables(oldIssue.Title, opts.Vars),
				Description:        substituteVariables(oldIssue.Description, opts.Vars),
				Design:             substituteVariables(oldIssue.Design, opts.Vars),
				AcceptanceCriteria: substituteVariables(oldIssue.AcceptanceCriteria, opts.Vars),
				Notes:              substituteVariables(oldIssue.Notes, opts.Vars),
				Status:             types.StatusOpen, // Always start fresh
				Priority:           oldIssue.Priority,
				IssueType:          oldIssue.IssueType,
				Assignee:           issueAssignee,
				EstimatedMinutes:   oldIssue.EstimatedMinutes,
				Ephemeral:          opts.Ephemeral, // mark for cleanup when closed
				IDPrefix:           opts.Prefix,    // distinct prefixes for mols/wisps
				// Gate fields (for async coordination)
				AwaitType: oldIssue.AwaitType,
				AwaitID:   substituteVariables(oldIssue.AwaitID, opts.Vars),
				Timeout:   oldIssue.Timeout,
				Metadata:  oldIssue.Metadata, // carry formula step metadata (GH#3341)
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}

			// Generate custom ID for dynamic bonding if ParentID is set
			if opts.ParentID != "" {
				bondedID, err := generateBondedID(oldIssue.ID, subgraph.Root.ID, opts)
				if err != nil {
					return fmt.Errorf("failed to generate bonded ID for %s: %w", oldIssue.ID, err)
				}
				newIssue.ID = bondedID
			}

			if err := tx.CreateIssue(ctx, newIssue, opts.Actor); err != nil {
				return fmt.Errorf("failed to create issue from %s: %w", oldIssue.ID, err)
			}

			idMapping[oldIssue.ID] = newIssue.ID
		}

		// Second pass: recreate dependencies with new IDs
		for _, dep := range subgraph.Dependencies {
			newFromID, ok1 := idMapping[dep.IssueID]
			newToID, ok2 := idMapping[dep.DependsOnID]
			if !ok1 || !ok2 {
				continue // Skip if either end is outside the subgraph
			}

			newDep := &types.Dependency{
				IssueID:     newFromID,
				DependsOnID: newToID,
				Type:        dep.Type,
			}
			if err := tx.AddDependency(ctx, newDep, opts.Actor); err != nil {
				return fmt.Errorf("failed to create dependency: %w", err)
			}
		}

		// Atomic attachment: link spawned root to target molecule within
		// the same transaction (bd-wvplu: prevents orphaned spawns)
		if opts.AttachToID != "" {
			attachDep := &types.Dependency{
				IssueID:     idMapping[subgraph.Root.ID],
				DependsOnID: opts.AttachToID,
				Type:        opts.AttachDepType,
			}
			if err := tx.AddDependency(ctx, attachDep, opts.Actor); err != nil {
				return fmt.Errorf("attaching to molecule: %w", err)
			}
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return &InstantiateResult{
		NewEpicID: idMapping[subgraph.Root.ID],
		IDMapping: idMapping,
		Created:   len(idMapping),
	}, nil
}

// printTemplateTree prints the template structure as a tree.
// Uses a visited set to detect cycles (GH#2719) and avoid infinite recursion.
func printTemplateTree(subgraph *TemplateSubgraph, parentID string, depth int, isRoot bool) {
	visited := make(map[string]bool)
	printTemplateTreeVisited(subgraph, parentID, depth, isRoot, visited)
}

// printTemplateTreeVisited is the internal recursive implementation with cycle tracking.
func printTemplateTreeVisited(subgraph *TemplateSubgraph, parentID string, depth int, isRoot bool, visited map[string]bool) {
	indent := strings.Repeat("  ", depth)

	// Print root
	if isRoot {
		fmt.Printf("%s   %s (root)\n", indent, subgraph.Root.Title)
		visited[parentID] = true
	}

	// Find children of this parent
	var children []*types.Issue
	for _, dep := range subgraph.Dependencies {
		if dep.DependsOnID == parentID && dep.Type == types.DepParentChild {
			if child, ok := subgraph.IssueMap[dep.IssueID]; ok {
				children = append(children, child)
			}
		}
	}

	// Print children
	for i, child := range children {
		connector := "├──"
		if i == len(children)-1 {
			connector = "└──"
		}
		vars := extractVariables(child.Title)
		varStr := ""
		if len(vars) > 0 {
			varStr = fmt.Sprintf(" [%s]", strings.Join(vars, ", "))
		}

		// Cycle detection (GH#2719)
		if visited[child.ID] {
			fmt.Printf("%s   %s %s%s (cycle detected, skipping)\n", indent, connector, child.Title, varStr)
			continue
		}
		fmt.Printf("%s   %s %s%s\n", indent, connector, child.Title, varStr)
		visited[child.ID] = true
		printTemplateTreeVisited(subgraph, child.ID, depth+1, false, visited)
	}
}
