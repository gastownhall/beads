package main

import (
	"testing"
)

func TestValidateWorkspaceIdentity_NilStore(t *testing.T) {
	// When store is nil, validateWorkspaceIdentity should be a no-op
	// (no panic, no os.Exit)
	origStore := store
	store = nil
	defer func() { store = nil; store = origStore }()

	validateWorkspaceIdentity(nil, "/nonexistent")
	// If we got here, no os.Exit was called — pass
}

func TestValidateWorkspaceIdentity_NonexistentDir(t *testing.T) {
	// When beadsDir doesn't exist, configfile.Load fails and we skip validation
	origStore := store
	store = nil
	defer func() { store = origStore }()

	validateWorkspaceIdentity(nil, "/nonexistent/path/that/does/not/exist")
}
