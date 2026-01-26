package doctor

// Status constants for doctor checks
const (
	StatusOK      = "ok"
	StatusWarning = "warning"
	StatusError   = "error"
)

// Category constants for grouping doctor checks
const (
	CategoryCore        = "Core System"
	CategoryGit         = "Git Integration"
	CategoryRuntime     = "Runtime"
	CategoryData        = "Data & Config"
	CategoryIntegration = "Integrations"
	CategoryMetadata    = "Metadata"
	CategoryMaintenance = "Maintenance"
	CategoryFederation  = "Federation"
)

// CategoryOrder defines the display order for categories
var CategoryOrder = []string{
	CategoryCore,
	CategoryData,
	CategoryGit,
	CategoryRuntime,
	CategoryIntegration,
	CategoryFederation,
	CategoryMetadata,
	CategoryMaintenance,
}

// MinSyncBranchHookVersion is the minimum hook version that supports sync-branch bypass (issue #532)
const MinSyncBranchHookVersion = "0.29.0"

// DoctorCheck represents a single diagnostic check result
type DoctorCheck struct {
	Name     string `json:"name"`
	Status   string `json:"status"` // StatusOK, StatusWarning, or StatusError
	Message  string `json:"message"`
	Detail   string `json:"detail,omitempty"`
	Fix      string `json:"fix,omitempty"`
	Category string `json:"category,omitempty"` // category for grouping in output
}

// OrphanIssue represents an issue referenced in commits but still open.
// This is shared between 'bd orphans' and 'bd doctor' commands.
type OrphanIssue struct {
	IssueID              string
	Title                string
	Status               string
	LatestCommit         string
	LatestCommitMessage  string
}
