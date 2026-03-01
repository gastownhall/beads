//go:build !cgo

package doctor

// Non-CGO stubs for doctor checks that require Dolt database access.
// These checks are skipped in non-CGO builds.

func CheckMergeArtifacts(_ string) DoctorCheck {
	return DoctorCheck{Name: "Merge Artifacts", Status: StatusOK, Message: "Requires CGO"}
}

func CheckOrphanedDependencies(_ string) DoctorCheck {
	return DoctorCheck{Name: "Orphaned Dependencies", Status: StatusOK, Message: "Requires CGO"}
}

func CheckDuplicateIssues(_ string, _ bool, _ int) DoctorCheck {
	return DoctorCheck{Name: "Duplicate Issues", Status: StatusOK, Message: "Requires CGO"}
}

func CheckTestPollution(_ string) DoctorCheck {
	return DoctorCheck{Name: "Test Pollution", Status: StatusOK, Message: "Requires CGO"}
}

func CheckChildParentDependencies(_ string) DoctorCheck {
	return DoctorCheck{Name: "Child-Parent Dependencies", Status: StatusOK, Message: "Requires CGO"}
}

func CheckGitConflicts(_ string) DoctorCheck {
	return DoctorCheck{Name: "Git Conflicts", Status: StatusOK, Message: "Requires CGO"}
}

func CheckStaleClosedIssues(_ string) DoctorCheck {
	return DoctorCheck{Name: "Stale Closed Issues", Status: StatusOK, Message: "Requires CGO"}
}

func CheckStaleMolecules(_ string) DoctorCheck {
	return DoctorCheck{Name: "Stale Molecules", Status: StatusOK, Message: "Requires CGO"}
}

func CheckPersistentMolIssues(_ string) DoctorCheck {
	return DoctorCheck{Name: "Persistent Mol Issues", Status: StatusOK, Message: "Requires CGO"}
}

func CheckStaleMQFiles(_ string) DoctorCheck {
	return DoctorCheck{Name: "Stale MQ Files", Status: StatusOK, Message: "Requires CGO"}
}

func CheckPatrolPollution(_ string) DoctorCheck {
	return DoctorCheck{Name: "Patrol Pollution", Status: StatusOK, Message: "Requires CGO"}
}

func FixStaleMQFiles(_ string) error {
	return nil
}
