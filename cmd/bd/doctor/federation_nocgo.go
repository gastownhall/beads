//go:build !cgo || windows
package doctor

// CheckFederationRemotesAPI returns N/A when CGO is not available.
func CheckFederationRemotesAPI(path string) DoctorCheck {
	return DoctorCheck{
		Name:     "Federation remotesapi",
		Status:   StatusOK,
		Message:  "N/A (requires CGO)",
		Category: CategoryFederation,
	}
}

// CheckFederationPeerConnectivity returns N/A when CGO is not available.
func CheckFederationPeerConnectivity(path string) DoctorCheck {
	return DoctorCheck{
		Name:     "Peer Connectivity",
		Status:   StatusOK,
		Message:  "N/A (requires CGO)",
		Category: CategoryFederation,
	}
}

// CheckFederationSyncStaleness returns N/A when CGO is not available.
func CheckFederationSyncStaleness(path string) DoctorCheck {
	return DoctorCheck{
		Name:     "Sync Staleness",
		Status:   StatusOK,
		Message:  "N/A (requires CGO)",
		Category: CategoryFederation,
	}
}

// CheckFederationConflicts returns N/A when CGO is not available.
func CheckFederationConflicts(path string) DoctorCheck {
	return DoctorCheck{
		Name:     "Federation Conflicts",
		Status:   StatusOK,
		Message:  "N/A (requires CGO)",
		Category: CategoryFederation,
	}
}

// CheckDoltServerModeMismatch returns N/A when CGO is not available.
func CheckDoltServerModeMismatch(path string) DoctorCheck {
	return DoctorCheck{
		Name:     "Dolt Mode",
		Status:   StatusOK,
		Message:  "N/A (requires CGO)",
		Category: CategoryFederation,
	}
}

