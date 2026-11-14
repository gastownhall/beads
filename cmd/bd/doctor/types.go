package doctor

import "github.com/steveyegge/beads/internal/remedy"

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
	CategoryPerformance = "Performance"
	CategoryFederation  = "Federation"
	CategoryDolt        = "Dolt Storage"
)

// CategoryOrder defines the display order for categories
var CategoryOrder = []string{
	CategoryCore,
	CategoryData,
	CategoryGit,
	CategoryDolt,
	CategoryRuntime,
	CategoryPerformance,
	CategoryIntegration,
	CategoryFederation,
	CategoryMetadata,
	CategoryMaintenance,
}

// DoctorCheck represents a single diagnostic check result
type DoctorCheck struct {
	Name     string `json:"name"`
	Status   string `json:"status"` // StatusOK, StatusWarning, or StatusError
	Message  string `json:"message"`
	Detail   string `json:"detail,omitempty"`
	Fix      string `json:"fix,omitempty"`
	Category string `json:"category,omitempty"` // category for grouping in output
}

// Printed returns c as a text printer of checks shows it: each text field
// spelled by remedy.PrintedText. A check's message, detail or fix can repeat
// a value a cloned repository sets (a dial error naming metadata.json's
// dolt_server_host, a routed path), and a carriage return or escape in it
// would redraw the fix printed under it. JSON output is left as it is:
// encoding/json escapes those characters itself.
func (c DoctorCheck) Printed() DoctorCheck {
	c.Name = remedy.PrintedText(c.Name)
	c.Message = remedy.PrintedText(c.Message)
	c.Detail = remedy.PrintedText(c.Detail)
	c.Fix = remedy.PrintedText(c.Fix)
	return c
}

// Printed returns d as the agent-facing text printer shows it, each text
// field spelled by remedy.PrintedText, as DoctorCheck.Printed spells a
// check's.
func (d AgentDiagnostic) Printed() AgentDiagnostic {
	d.Name = remedy.PrintedText(d.Name)
	d.Explanation = remedy.PrintedText(d.Explanation)
	d.Observed = remedy.PrintedText(d.Observed)
	d.Expected = remedy.PrintedText(d.Expected)
	d.Commands = append([]string(nil), d.Commands...)
	for i, cmd := range d.Commands {
		d.Commands[i] = remedy.PrintedText(cmd)
	}
	d.SourceFiles = append([]string(nil), d.SourceFiles...)
	for i, file := range d.SourceFiles {
		d.SourceFiles[i] = remedy.PrintedText(file)
	}
	return d
}

// OrphanIssue represents an issue referenced in commits but still open.
// This is shared between 'bd orphans' and 'bd doctor' commands.
type OrphanIssue struct {
	IssueID             string
	Title               string
	Status              string
	LatestCommit        string
	LatestCommitMessage string
}
