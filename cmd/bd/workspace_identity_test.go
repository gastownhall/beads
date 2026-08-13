package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

func TestValidateWorkspaceIdentity_NilStore(t *testing.T) {
	origStore := store
	store = nil
	t.Cleanup(func() { store = origStore })

	if err := validateWorkspaceIdentity(context.Background(), filepath.Join(t.TempDir(), ".beads")); err != nil {
		t.Errorf("validateWorkspaceIdentity() with nil store = %v, want nil", err)
	}
}

type metadataRecordingStore struct {
	storage.DoltStorage
	metadataCalls int
}

func (s *metadataRecordingStore) GetMetadata(context.Context, string) (string, error) {
	s.metadataCalls++
	return "unexpected", nil
}

func TestValidateWorkspaceIdentity_NonexistentDir(t *testing.T) {
	origStore := store
	fakeStore := &metadataRecordingStore{}
	store = fakeStore
	t.Cleanup(func() { store = origStore })

	if err := validateWorkspaceIdentity(context.Background(), filepath.Join(t.TempDir(), "missing", ".beads")); err != nil {
		t.Errorf("validateWorkspaceIdentity() with no config = %v, want nil", err)
	}
	if fakeStore.metadataCalls != 0 {
		t.Errorf("validateWorkspaceIdentity() queried metadata %d times without config, want 0", fakeStore.metadataCalls)
	}
}
