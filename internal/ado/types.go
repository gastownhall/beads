// Package ado provides client and data types for the Azure DevOps REST API.
//
// This package handles all interactions with Azure DevOps work item tracking,
// including fetching, creating, and updating work items. It provides
// bidirectional mapping between ADO's data model and Beads' internal types.
package ado

import "time"

// API configuration constants.
const (
	// DefaultBaseURL is the Azure DevOps base URL.
	DefaultBaseURL = "https://dev.azure.com"

	// APIVersion is the ADO REST API version used by this package.
	APIVersion = "7.1"

	// MaxBatchSize is the maximum number of work item IDs per batch GET request.
	MaxBatchSize = 200

	// MaxResponseSize is the maximum response body size (50 MB).
	MaxResponseSize = 50 * 1024 * 1024

	// DefaultTimeout is the default HTTP request timeout.
	DefaultTimeout = 30 * time.Second

	// MaxRetries is the maximum number of retries for transient failures.
	MaxRetries = 3

	// RetryDelay is the base delay between retries.
	RetryDelay = time.Second
)

// Common ADO work item field path constants.
const (
	// FieldTitle is the work item title field.
	FieldTitle = "System.Title"

	// FieldDescription is the work item description field.
	FieldDescription = "System.Description"

	// FieldState is the work item state field.
	FieldState = "System.State"

	// FieldWorkItemType is the work item type field (e.g., Bug, Task, User Story).
	FieldWorkItemType = "System.WorkItemType"

	// FieldAreaPath is the work item area path field.
	FieldAreaPath = "System.AreaPath"

	// FieldIterationPath is the work item iteration path field.
	FieldIterationPath = "System.IterationPath"

	// FieldPriority is the work item priority field (1-4 in ADO).
	FieldPriority = "Microsoft.VSTS.Common.Priority"

	// FieldTags is the work item tags field (semicolon-separated).
	FieldTags = "System.Tags"

	// FieldChangedDate is the work item last-changed date field.
	FieldChangedDate = "System.ChangedDate"

	// FieldCreatedDate is the work item creation date field.
	FieldCreatedDate = "System.CreatedDate"

	// FieldAssignedTo is the work item assigned-to field.
	FieldAssignedTo = "System.AssignedTo"

	// FieldStoryPoints is the story points scheduling field.
	FieldStoryPoints = "Microsoft.VSTS.Scheduling.StoryPoints"

	// FieldSeverity is the severity field, required for Bug work items.
	// Values: "1 - Critical", "2 - High", "3 - Medium", "4 - Low".
	FieldSeverity = "Microsoft.VSTS.Common.Severity"

	// FieldRemainingWork is the remaining work scheduling field.
	FieldRemainingWork = "Microsoft.VSTS.Scheduling.RemainingWork"

	// FieldTeamProject is the team project field.
	FieldTeamProject = "System.TeamProject"
)

// ADO link relation type constants.
const (
	// RelParent is the parent link type (hierarchy reverse).
	RelParent = "System.LinkTypes.Hierarchy-Reverse"

	// RelChild is the child link type (hierarchy forward).
	RelChild = "System.LinkTypes.Hierarchy-Forward"

	// RelRelated is the related link type.
	RelRelated = "System.LinkTypes.Related"

	// RelDependencyOf is the dependency-of (successor) link type.
	RelDependencyOf = "System.LinkTypes.Dependency-Reverse"

	// RelDependsOn is the depends-on (predecessor) link type.
	RelDependsOn = "System.LinkTypes.Dependency-Forward"
)

// SecretString wraps a string value and prevents accidental exposure
// in logs, JSON output, and fmt formatting.
type SecretString struct {
	value string
}

// NewSecretString creates a SecretString from the given plaintext value.
func NewSecretString(s string) SecretString {
	return SecretString{value: s}
}

// String returns a redacted placeholder, implementing fmt.Stringer.
func (s SecretString) String() string {
	return "[REDACTED]"
}

// Expose returns the actual secret value. Use only where the raw credential
// is required, such as setting an HTTP Authorization header.
func (s SecretString) Expose() string {
	return s.value
}

// MarshalJSON returns the JSON encoding of a redacted placeholder,
// preventing the secret from leaking into serialized output.
func (s SecretString) MarshalJSON() ([]byte, error) {
	return []byte(`"[REDACTED]"`), nil
}

// IsEmpty reports whether the underlying secret value is empty.
func (s SecretString) IsEmpty() bool {
	return s.value == ""
}

// WorkItem represents an Azure DevOps work item from the REST API.
type WorkItem struct {
	ID        int                    `json:"id"`
	Rev       int                    `json:"rev"`
	Fields    map[string]interface{} `json:"fields"`
	URL       string                 `json:"url"`
	Relations []WorkItemRelation     `json:"relations,omitempty"`
}

// GetField returns the value of a work item field, or nil if not present.
func (w *WorkItem) GetField(name string) interface{} {
	if w.Fields == nil {
		return nil
	}
	return w.Fields[name]
}

// GetStringField returns a work item field as a string, or empty string
// if the field is missing or not a string.
func (w *WorkItem) GetStringField(name string) string {
	v := w.GetField(name)
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// GetIntField returns a work item field as an int, or 0 if the field
// is missing or not numeric. JSON numbers decoded as float64 are converted.
func (w *WorkItem) GetIntField(name string) int {
	v := w.GetField(name)
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	default:
		return 0
	}
}

// WorkItemRelation represents a link between work items.
type WorkItemRelation struct {
	Rel        string                 `json:"rel"`
	URL        string                 `json:"url"`
	Attributes map[string]interface{} `json:"attributes"`
}

// PatchOperation represents a single JSON Patch operation for
// creating or updating work items via the ADO REST API.
type PatchOperation struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
	From  string      `json:"from,omitempty"`
}

// WIQLRequest is the request body for a WIQL (Work Item Query Language) query.
type WIQLRequest struct {
	Query string `json:"query"`
}

// WIQLResult is the response from a WIQL query, containing work item references.
type WIQLResult struct {
	WorkItems []WIQLWorkItemRef `json:"workItems"`
}

// WIQLWorkItemRef is a lightweight reference to a work item returned by WIQL.
type WIQLWorkItemRef struct {
	ID  int    `json:"id"`
	URL string `json:"url"`
}

// Project represents an Azure DevOps team project.
type Project struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	URL         string `json:"url"`
	State       string `json:"state"`
}

// WorkItemType describes a work item type available in a project.
type WorkItemType struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// WorkItemState describes a state for a work item type.
type WorkItemState struct {
	Name     string `json:"name"`
	Color    string `json:"color"`
	Category string `json:"category"`
}
