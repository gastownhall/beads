//go:build !cgo

package doctor

// Non-CGO stubs for doctor checks that require Dolt database access.
// These checks are skipped in non-CGO builds.

func CheckOrphanedDependencies(_ string) DoctorCheck {
	return DoctorCheck{Name: "Orphaned Dependencies", Status: StatusWarning, Message: "Skipped: requires CGO"}
}

func CheckDuplicateIssues(_ string, _ bool, _ int) DoctorCheck {
	return DoctorCheck{Name: "Duplicate Issues", Status: StatusWarning, Message: "Skipped: requires CGO"}
}

func CheckTestPollution(_ string) DoctorCheck {
	return DoctorCheck{Name: "Test Pollution", Status: StatusWarning, Message: "Skipped: requires CGO"}
}

func CheckChildParentDependencies(_ string) DoctorCheck {
	return DoctorCheck{Name: "Child-Parent Dependencies", Status: StatusWarning, Message: "Skipped: requires CGO"}
}

func CheckGitConflicts(_ string) DoctorCheck {
	return DoctorCheck{Name: "Git Conflicts", Status: StatusWarning, Message: "Skipped: requires CGO"}
}

func CheckStaleClosedIssues(_ string) DoctorCheck {
	return DoctorCheck{Name: "Stale Closed Issues", Status: StatusWarning, Message: "Skipped: requires CGO"}
}

func CheckStaleMolecules(_ string) DoctorCheck {
	return DoctorCheck{Name: "Stale Molecules", Status: StatusWarning, Message: "Skipped: requires CGO"}
}

func CheckPersistentMolIssues(_ string) DoctorCheck {
	return DoctorCheck{Name: "Persistent Mol Issues", Status: StatusWarning, Message: "Skipped: requires CGO"}
}

func CheckStaleMQFiles(_ string) DoctorCheck {
	return DoctorCheck{Name: "Stale MQ Files", Status: StatusWarning, Message: "Skipped: requires CGO"}
}

func CheckPatrolPollution(_ string) DoctorCheck {
	return DoctorCheck{Name: "Patrol Pollution", Status: StatusWarning, Message: "Skipped: requires CGO"}
}

func FixStaleMQFiles(_ string) error {
	return nil
}

// WithStore variants (nocgo stubs).
// GH#2636: Prevents redundant server start/stop cycles during doctor checks.

func CheckOrphanedDependenciesWithStore(_ *SharedStore) DoctorCheck {
	return DoctorCheck{Name: "Orphaned Dependencies", Status: StatusWarning, Message: "Skipped: requires CGO"}
}

func CheckDuplicateIssuesWithStore(_ *SharedStore, _ bool, _ int) DoctorCheck {
	return DoctorCheck{Name: "Duplicate Issues", Status: StatusWarning, Message: "Skipped: requires CGO"}
}

func CheckTestPollutionWithStore(_ *SharedStore) DoctorCheck {
	return DoctorCheck{Name: "Test Pollution", Status: StatusWarning, Message: "Skipped: requires CGO"}
}

func CheckChildParentDependenciesWithStore(_ *SharedStore) DoctorCheck {
	return DoctorCheck{Name: "Child-Parent Dependencies", Status: StatusWarning, Message: "Skipped: requires CGO"}
}

func CheckStaleClosedIssuesWithStore(_ *SharedStore) DoctorCheck {
	return DoctorCheck{Name: "Stale Closed Issues", Status: StatusWarning, Message: "Skipped: requires CGO"}
}

func CheckStaleMoleculesWithStore(_ *SharedStore) DoctorCheck {
	return DoctorCheck{Name: "Stale Molecules", Status: StatusWarning, Message: "Skipped: requires CGO"}
}

func CheckPersistentMolIssuesWithStore(_ *SharedStore) DoctorCheck {
	return DoctorCheck{Name: "Persistent Mol Issues", Status: StatusWarning, Message: "Skipped: requires CGO"}
}
