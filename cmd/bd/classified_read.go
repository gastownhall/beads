package main

// classifiedReadOpen reports whether the root store open may carry
// ClassifiedRead, the permission that lets a command's read-only
// classification still run the lazy defer-wake sweep (GH#804, be-vbhpf).
// The sweep is an incidental write, so every input that forbids writes
// forbids it: strict --readonly and an explicit preview by policy, and an
// active migration freeze (dc-6jaq) because a read is deliberately allowed
// past the freeze gate while its side effects are not (bd-6zf0q5). frozen is
// the invocation's resolved migration.Result.Frozen(), never a bare
// marker-path check: an undeterminable gate is not an open gate.
func classifiedReadOpen(policyReadOnly, readonlyMode, previewMode, frozen bool) bool {
	return policyReadOnly && !readonlyMode && !previewMode && !frozen
}
