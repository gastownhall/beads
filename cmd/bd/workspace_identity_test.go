package main

import (
	"context"
	"path/filepath"
	"testing"
)

func TestValidateWorkspaceIdentity_NilStore(t *testing.T) {
	origStore := store
	store = nil
	t.Cleanup(func() { store = origStore })

	if err := validateWorkspaceIdentity(context.Background(), filepath.Join(t.TempDir(), ".beads")); err != nil {
		t.Errorf("validateWorkspaceIdentity() with nil store = %v, want nil", err)
	}
}

func TestValidateWorkspaceIdentity_NonexistentDir(t *testing.T) {
	origStore := store
	store = nil
	t.Cleanup(func() { store = origStore })

	if err := validateWorkspaceIdentity(context.Background(), filepath.Join(t.TempDir(), "missing", ".beads")); err != nil {
		t.Errorf("validateWorkspaceIdentity() with no config = %v, want nil", err)
	}
}
