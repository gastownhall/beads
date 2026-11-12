package main

// classifiedReadOpen reports whether a store open is read-only purely by
// command classification (GH#804) and may therefore run the lazy defer-wake
// sweep (be-vbhpf). Strict --readonly, a preview, and an active migration
// freeze (dc-6jaq: a read runs during a freeze, but no write trails it) all
// disqualify it.
func classifiedReadOpen(policyReadOnly, readonlyMode, previewMode, frozen bool) bool {
	return policyReadOnly && !readonlyMode && !previewMode && !frozen
}
