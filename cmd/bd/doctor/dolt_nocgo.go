//go:build !cgo

package doctor

// RunDoltHealthChecks returns N/A when CGO is not available.
func RunDoltHealthChecks(path string) []DoctorCheck {
	return []DoctorCheck{
		{Name: "Dolt Connection", Status: StatusOK, Message: "N/A (requires CGO for Dolt)", Category: CategoryCore},
		{Name: "Dolt Schema", Status: StatusOK, Message: "N/A (requires CGO for Dolt)", Category: CategoryCore},
		{Name: "Dolt-JSONL Sync", Status: StatusOK, Message: "N/A (requires CGO for Dolt)", Category: CategoryData},
		{Name: "Dolt Status", Status: StatusOK, Message: "N/A (requires CGO for Dolt)", Category: CategoryData},
	}
}

// CheckDoltConnection returns N/A when CGO is not available.
func CheckDoltConnection(path string) DoctorCheck {
	return DoctorCheck{
		Name:     "Dolt Connection",
		Status:   StatusOK,
		Message:  "N/A (requires CGO for Dolt)",
		Category: CategoryCore,
	}
}

// CheckDoltSchema returns N/A when CGO is not available.
func CheckDoltSchema(path string) DoctorCheck {
	return DoctorCheck{
		Name:     "Dolt Schema",
		Status:   StatusOK,
		Message:  "N/A (requires CGO for Dolt)",
		Category: CategoryCore,
	}
}

// CheckDoltIssueCount returns N/A when CGO is not available.
func CheckDoltIssueCount(path string) DoctorCheck {
	return DoctorCheck{
		Name:     "Dolt-JSONL Sync",
		Status:   StatusOK,
		Message:  "N/A (requires CGO for Dolt)",
		Category: CategoryData,
	}
}

// CheckDoltStatus returns N/A when CGO is not available.
func CheckDoltStatus(path string) DoctorCheck {
	return DoctorCheck{
		Name:     "Dolt Status",
		Status:   StatusOK,
		Message:  "N/A (requires CGO for Dolt)",
		Category: CategoryData,
	}
}
