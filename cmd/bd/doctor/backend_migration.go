package doctor

// CheckBackendMigration checks the storage backend status.
// Dolt is the only supported backend.
func CheckBackendMigration(_ string) DoctorCheck {
	return DoctorCheck{
		Name:     "Backend Migration",
		Status:   StatusOK,
		Message:  "Backend: Dolt (current default)",
		Category: CategoryCore,
	}
}
