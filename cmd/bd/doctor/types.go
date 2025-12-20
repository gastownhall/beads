package doctor

// Status constants for doctor checks
const (
	StatusOK      = "ok"
	StatusWarning = "warning"
	StatusError   = "error"
)

// MinSyncBranchHookVersion is the minimum hook version that supports sync-branch bypass (issue #532)
const MinSyncBranchHookVersion = "0.29.0"

// DoctorCheck represents a single diagnostic check result
type DoctorCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // StatusOK, StatusWarning, or StatusError
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
	Fix     string `json:"fix,omitempty"`
}
