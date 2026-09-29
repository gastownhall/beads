package main

import (
	"testing"
)

func TestValidateWorkspaceIdentity_NilStore(t *testing.T) {
	// When the passed-in store is nil, validateWorkspaceIdentity should be a
	// no-op (no panic, no os.Exit).
	validateWorkspaceIdentity(nil, nil, "/nonexistent")
	// If we got here, no os.Exit was called — pass
}

func TestValidateWorkspaceIdentity_NonexistentDir(t *testing.T) {
	// When beadsDir doesn't exist, configfile.Load fails and we skip
	// validation. Store is nil here too (same as the pre-refactor version of
	// this test, which set the package-level store to nil): both cases
	// return before configfile.Load would even run, so this and
	// TestValidateWorkspaceIdentity_NilStore cover the same early return.
	validateWorkspaceIdentity(nil, nil, "/nonexistent/path/that/does/not/exist")
}
