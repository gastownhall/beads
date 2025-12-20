package linear

import (
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/idgen"
	"github.com/steveyegge/beads/internal/types"
)

// IDGenerationOptions configures Linear hash ID generation.
type IDGenerationOptions struct {
	BaseLength int             // Starting hash length (3-8)
	MaxLength  int             // Maximum hash length (3-8)
	UsedIDs    map[string]bool // Pre-populated set to avoid collisions (e.g., DB IDs)
}

// BuildLinearDescription formats a Beads issue for Linear's description field.
// This mirrors the payload used during push to keep hash comparisons consistent.
func BuildLinearDescription(issue *types.Issue) string {
	description := issue.Description
	if issue.AcceptanceCriteria != "" {
		description += "\n\n## Acceptance Criteria\n" + issue.AcceptanceCriteria
	}
	if issue.Design != "" {
		description += "\n\n## Design\n" + issue.Design
	}
	if issue.Notes != "" {
		description += "\n\n## Notes\n" + issue.Notes
	}
	return description
}

// NormalizeIssueForLinearHash returns a copy of the issue using Linear's description
// formatting and clears fields not present in Linear's model to avoid false conflicts.
func NormalizeIssueForLinearHash(issue *types.Issue) *types.Issue {
	normalized := *issue
	normalized.Description = BuildLinearDescription(issue)
	normalized.AcceptanceCriteria = ""
	normalized.Design = ""
	normalized.Notes = ""
	if normalized.ExternalRef != nil && IsLinearExternalRef(*normalized.ExternalRef) {
		if canonical, ok := CanonicalizeLinearExternalRef(*normalized.ExternalRef); ok {
			normalized.ExternalRef = &canonical
		}
	}
	return &normalized
}

// GenerateIssueIDs generates unique hash-based IDs for issues that don't have one.
// Tracks used IDs to prevent collisions within the batch (and optionally against existing IDs).
// The creator parameter is used as part of the hash input (e.g., "linear-import").
func GenerateIssueIDs(issues []*types.Issue, prefix, creator string, opts IDGenerationOptions) error {
	usedIDs := opts.UsedIDs
	if usedIDs == nil {
		usedIDs = make(map[string]bool)
	}

	baseLength := opts.BaseLength
	if baseLength == 0 {
		baseLength = 6
	}
	maxLength := opts.MaxLength
	if maxLength == 0 {
		maxLength = 8
	}
	if baseLength < 3 {
		baseLength = 3
	}
	if maxLength > 8 {
		maxLength = 8
	}
	if baseLength > maxLength {
		baseLength = maxLength
	}

	// First pass: record existing IDs
	for _, issue := range issues {
		if issue.ID != "" {
			usedIDs[issue.ID] = true
		}
	}

	// Second pass: generate IDs for issues without one
	for _, issue := range issues {
		if issue.ID != "" {
			continue // Already has an ID
		}

		var generated bool
		for length := baseLength; length <= maxLength && !generated; length++ {
			for nonce := 0; nonce < 10; nonce++ {
				candidate := idgen.GenerateHashID(
					prefix,
					issue.Title,
					issue.Description,
					creator,
					issue.CreatedAt,
					length,
					nonce,
				)

				if !usedIDs[candidate] {
					issue.ID = candidate
					usedIDs[candidate] = true
					generated = true
					break
				}
			}
		}

		if !generated {
			return fmt.Errorf("failed to generate unique ID for issue '%s' after trying lengths %d-%d with 10 nonces each",
				issue.Title, baseLength, maxLength)
		}
	}

	return nil
}

// MappingConfig holds configurable mappings between Linear and Beads.
// All maps use lowercase keys for case-insensitive matching.
type MappingConfig struct {
	// PriorityMap maps Linear priority (0-4) to Beads priority (0-4).
	// Key is Linear priority as string, value is Beads priority.
	PriorityMap map[string]int

	// StateMap maps Linear state types/names to Beads statuses.
	// Key is lowercase state type or name, value is Beads status string.
	StateMap map[string]string

	// LabelTypeMap maps Linear label names to Beads issue types.
	// Key is lowercase label name, value is Beads issue type.
	LabelTypeMap map[string]string

	// RelationMap maps Linear relation types to Beads dependency types.
	// Key is Linear relation type, value is Beads dependency type.
	RelationMap map[string]string
}

// DefaultMappingConfig returns sensible default mappings.
func DefaultMappingConfig() *MappingConfig {
	return &MappingConfig{
		// Linear priority: 0=none, 1=urgent, 2=high, 3=medium, 4=low
		// Beads priority: 0=critical, 1=high, 2=medium, 3=low, 4=backlog
		PriorityMap: map[string]int{
			"0": 4, // No priority -> Backlog
			"1": 0, // Urgent -> Critical
			"2": 1, // High -> High
			"3": 2, // Medium -> Medium
			"4": 3, // Low -> Low
		},
		// Linear state types: backlog, unstarted, started, completed, canceled
		StateMap: map[string]string{
			"backlog":   "open",
			"unstarted": "open",
			"started":   "in_progress",
			"completed": "closed",
			"canceled":  "closed",
		},
		// Label patterns for issue type inference
		LabelTypeMap: map[string]string{
			"bug":         "bug",
			"defect":      "bug",
			"feature":     "feature",
			"enhancement": "feature",
			"epic":        "epic",
			"chore":       "chore",
			"maintenance": "chore",
			"task":        "task",
		},
		// Linear relation types to Beads dependency types
		RelationMap: map[string]string{
			"blocks":    "blocks",
			"blockedBy": "blocks", // Inverse: the related issue blocks this one
			"duplicate": "duplicates",
			"related":   "related",
		},
	}
}

// ConfigLoader is an interface for loading configuration values.
// This allows the mapping package to be decoupled from the storage layer.
type ConfigLoader interface {
	GetAllConfig() (map[string]string, error)
}

// LoadMappingConfig loads mapping configuration from a config loader.
// Config keys follow the pattern: linear.<category>_map.<key> = <value>
// Examples:
//
//	linear.priority_map.0 = 4       (Linear "no priority" -> Beads backlog)
//	linear.state_map.started = in_progress
//	linear.label_type_map.bug = bug
//	linear.relation_map.blocks = blocks
func LoadMappingConfig(loader ConfigLoader) *MappingConfig {
	config := DefaultMappingConfig()

	if loader == nil {
		return config
	}

	// Load all config keys and filter for linear mappings
	allConfig, err := loader.GetAllConfig()
	if err != nil {
		return config
	}

	for key, value := range allConfig {
		// Parse priority mappings: linear.priority_map.<linear_priority>
		if strings.HasPrefix(key, "linear.priority_map.") {
			linearPriority := strings.TrimPrefix(key, "linear.priority_map.")
			if beadsPriority, err := parseIntValue(value); err == nil {
				config.PriorityMap[linearPriority] = beadsPriority
			}
		}

		// Parse state mappings: linear.state_map.<state_type_or_name>
		if strings.HasPrefix(key, "linear.state_map.") {
			stateKey := strings.ToLower(strings.TrimPrefix(key, "linear.state_map."))
			config.StateMap[stateKey] = value
		}

		// Parse label-to-type mappings: linear.label_type_map.<label_name>
		if strings.HasPrefix(key, "linear.label_type_map.") {
			labelKey := strings.ToLower(strings.TrimPrefix(key, "linear.label_type_map."))
			config.LabelTypeMap[labelKey] = value
		}

		// Parse relation mappings: linear.relation_map.<relation_type>
		if strings.HasPrefix(key, "linear.relation_map.") {
			relationType := strings.TrimPrefix(key, "linear.relation_map.")
			config.RelationMap[relationType] = value
		}
	}

	return config
}

// parseIntValue safely parses an integer from a string config value.
func parseIntValue(s string) (int, error) {
	var v int
	_, err := fmt.Sscanf(s, "%d", &v)
	return v, err
}

// PriorityToBeads maps Linear priority (0-4) to Beads priority (0-4).
// Linear: 0=no priority, 1=urgent, 2=high, 3=medium, 4=low
// Beads:  0=critical, 1=high, 2=medium, 3=low, 4=backlog
// Uses configurable mapping from linear.priority_map.* config.
func PriorityToBeads(linearPriority int, config *MappingConfig) int {
	key := fmt.Sprintf("%d", linearPriority)
	if beadsPriority, ok := config.PriorityMap[key]; ok {
		return beadsPriority
	}
	// Fallback to default mapping if not configured
	return 2 // Default to Medium
}

// PriorityToLinear maps Beads priority (0-4) to Linear priority (0-4).
// Uses configurable mapping by inverting linear.priority_map.* config.
func PriorityToLinear(beadsPriority int, config *MappingConfig) int {
	// Build inverse map from config
	inverseMap := make(map[int]int)
	for linearKey, beadsVal := range config.PriorityMap {
		var linearVal int
		if _, err := fmt.Sscanf(linearKey, "%d", &linearVal); err == nil {
			inverseMap[beadsVal] = linearVal
		}
	}

	if linearPriority, ok := inverseMap[beadsPriority]; ok {
		return linearPriority
	}
	// Fallback to default mapping if not found
	return 3 // Default to Medium
}

// StateToBeadsStatus maps Linear state type to Beads status.
// Checks both state type (backlog, unstarted, etc.) and state name for custom workflows.
// Uses configurable mapping from linear.state_map.* config.
func StateToBeadsStatus(state *State, config *MappingConfig) types.Status {
	if state == nil {
		return types.StatusOpen
	}

	// First, try to match by state type (preferred)
	stateType := strings.ToLower(state.Type)
	if statusStr, ok := config.StateMap[stateType]; ok {
		return ParseBeadsStatus(statusStr)
	}

	// Then try to match by state name (for custom workflow states)
	stateName := strings.ToLower(state.Name)
	if statusStr, ok := config.StateMap[stateName]; ok {
		return ParseBeadsStatus(statusStr)
	}

	// Default fallback
	return types.StatusOpen
}

// ParseBeadsStatus converts a status string to types.Status.
func ParseBeadsStatus(s string) types.Status {
	switch strings.ToLower(s) {
	case "open":
		return types.StatusOpen
	case "in_progress", "in-progress", "inprogress":
		return types.StatusInProgress
	case "blocked":
		return types.StatusBlocked
	case "closed":
		return types.StatusClosed
	default:
		return types.StatusOpen
	}
}

// StatusToLinearStateType converts Beads status to Linear state type for filtering.
// This is used when pushing issues to Linear to find the appropriate state.
func StatusToLinearStateType(status types.Status) string {
	switch status {
	case types.StatusOpen:
		return "unstarted"
	case types.StatusInProgress:
		return "started"
	case types.StatusBlocked:
		return "started" // Linear doesn't have blocked state type
	case types.StatusClosed:
		return "completed"
	default:
		return "unstarted"
	}
}

// LabelToIssueType infers issue type from label names.
// Uses configurable mapping from linear.label_type_map.* config.
func LabelToIssueType(labels *Labels, config *MappingConfig) types.IssueType {
	if labels == nil {
		return types.TypeTask
	}

	for _, label := range labels.Nodes {
		labelName := strings.ToLower(label.Name)

		// Check exact match first
		if issueType, ok := config.LabelTypeMap[labelName]; ok {
			return ParseIssueType(issueType)
		}

		// Check if label contains any mapped keyword
		for keyword, issueType := range config.LabelTypeMap {
			if strings.Contains(labelName, keyword) {
				return ParseIssueType(issueType)
			}
		}
	}

	return types.TypeTask // Default
}

// ParseIssueType converts an issue type string to types.IssueType.
func ParseIssueType(s string) types.IssueType {
	switch strings.ToLower(s) {
	case "bug":
		return types.TypeBug
	case "feature":
		return types.TypeFeature
	case "task":
		return types.TypeTask
	case "epic":
		return types.TypeEpic
	case "chore":
		return types.TypeChore
	default:
		return types.TypeTask
	}
}

// RelationToBeadsDep converts a Linear relation to a Beads dependency type.
// Uses configurable mapping from linear.relation_map.* config.
func RelationToBeadsDep(relationType string, config *MappingConfig) string {
	if depType, ok := config.RelationMap[relationType]; ok {
		return depType
	}
	return "related" // Default fallback
}

// IssueToBeads converts a Linear issue to a Beads issue.
func IssueToBeads(li *Issue, config *MappingConfig) *IssueConversion {
	createdAt, err := time.Parse(time.RFC3339, li.CreatedAt)
	if err != nil {
		createdAt = time.Now()
	}

	updatedAt, err := time.Parse(time.RFC3339, li.UpdatedAt)
	if err != nil {
		updatedAt = time.Now()
	}

	issue := &types.Issue{
		Title:       li.Title,
		Description: li.Description,
		Priority:    PriorityToBeads(li.Priority, config),
		IssueType:   LabelToIssueType(li.Labels, config),
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
	}

	// Map state using configurable mapping
	issue.Status = StateToBeadsStatus(li.State, config)

	if li.CompletedAt != "" {
		completedAt, err := time.Parse(time.RFC3339, li.CompletedAt)
		if err == nil {
			issue.ClosedAt = &completedAt
		}
	}

	if li.Assignee != nil {
		if li.Assignee.Email != "" {
			issue.Assignee = li.Assignee.Email
		} else {
			issue.Assignee = li.Assignee.Name
		}
	}

	// Copy labels (bidirectional sync preserves all labels)
	if li.Labels != nil {
		for _, label := range li.Labels.Nodes {
			issue.Labels = append(issue.Labels, label.Name)
		}
	}

	externalRef := li.URL
	if canonical, ok := CanonicalizeLinearExternalRef(externalRef); ok {
		externalRef = canonical
	}
	issue.ExternalRef = &externalRef

	// Collect dependencies to be created after all issues are imported
	var deps []DependencyInfo

	// Map parent-child relationship
	if li.Parent != nil {
		deps = append(deps, DependencyInfo{
			FromLinearID: li.Identifier,
			ToLinearID:   li.Parent.Identifier,
			Type:         "parent-child",
		})
	}

	// Map relations to dependencies
	if li.Relations != nil {
		for _, rel := range li.Relations.Nodes {
			depType := RelationToBeadsDep(rel.Type, config)

			// For "blockedBy", we invert the direction since the related issue blocks this one
			if rel.Type == "blockedBy" {
				deps = append(deps, DependencyInfo{
					FromLinearID: li.Identifier,
					ToLinearID:   rel.RelatedIssue.Identifier,
					Type:         depType,
				})
				continue
			}

			// For "blocks", the related issue is blocked by this one.
			if rel.Type == "blocks" {
				deps = append(deps, DependencyInfo{
					FromLinearID: rel.RelatedIssue.Identifier,
					ToLinearID:   li.Identifier,
					Type:         depType,
				})
				continue
			}

			// For "duplicate" and "related", treat this issue as the source.
			deps = append(deps, DependencyInfo{
				FromLinearID: li.Identifier,
				ToLinearID:   rel.RelatedIssue.Identifier,
				Type:         depType,
			})
		}
	}

	return &IssueConversion{
		Issue:        issue,
		Dependencies: deps,
	}
}

// BuildLinearToLocalUpdates creates an updates map from a Linear issue
// to apply to a local Beads issue. This is used when Linear wins a conflict.
func BuildLinearToLocalUpdates(li *Issue, config *MappingConfig) map[string]interface{} {
	updates := make(map[string]interface{})

	// Update title
	updates["title"] = li.Title

	// Update description
	updates["description"] = li.Description

	// Update priority using configured mapping
	updates["priority"] = PriorityToBeads(li.Priority, config)

	// Update status using configured mapping
	updates["status"] = string(StateToBeadsStatus(li.State, config))

	// Update assignee if present
	if li.Assignee != nil {
		if li.Assignee.Email != "" {
			updates["assignee"] = li.Assignee.Email
		} else {
			updates["assignee"] = li.Assignee.Name
		}
	} else {
		updates["assignee"] = ""
	}

	// Update labels from Linear
	if li.Labels != nil {
		var labels []string
		for _, label := range li.Labels.Nodes {
			labels = append(labels, label.Name)
		}
		updates["labels"] = labels
	}

	// Update timestamps
	if li.UpdatedAt != "" {
		if updatedAt, err := time.Parse(time.RFC3339, li.UpdatedAt); err == nil {
			updates["updated_at"] = updatedAt
		}
	}

	// Handle closed state
	if li.CompletedAt != "" {
		if closedAt, err := time.Parse(time.RFC3339, li.CompletedAt); err == nil {
			updates["closed_at"] = closedAt
		}
	}

	return updates
}
