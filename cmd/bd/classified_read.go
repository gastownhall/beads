package main

// classifiedReadOpen reports whether a command's store open is a
// classification-only read (GH#804: bd ready/bd list are read-only for the
// CURRENT project) that may still run the lazy defer-wake sweep (be-vbhpf).
// That sweep is a write, so strict --readonly, a preview, and a migration
// freeze (bd-6zf0q5) all withhold the permission; the read itself still runs.
// frozen must be migration.Result.Frozen(), which also counts a failed marker
// lookup as frozen.
func classifiedReadOpen(policyReadOnly, readonlyMode, previewMode, frozen bool) bool {
	return policyReadOnly && !readonlyMode && !previewMode && !frozen
}
