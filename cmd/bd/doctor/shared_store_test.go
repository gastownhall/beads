package doctor

import (
	"testing"
)

func TestSharedStore_NilSafety(t *testing.T) {
	// All methods on nil SharedStore should be safe (no panic)
	var ss *SharedStore

	if ss.Store() != nil {
		t.Error("expected nil Store from nil SharedStore")
	}
	if ss.DB() != nil {
		t.Error("expected nil DB from nil SharedStore")
	}
	if ss.DoltStorage() != nil {
		t.Error("expected nil DoltStorage from nil SharedStore")
	}

	// Close on nil should not panic
	ss.Close()
}

func TestSharedStore_DoubleClose(t *testing.T) {
	// Double close should not panic even with a non-nil but closed store
	ss := &SharedStore{}
	ss.Close()
	ss.Close() // second close should be safe
}

func TestSharedStore_ClosedReturnsNil(t *testing.T) {
	ss := &SharedStore{}
	ss.Close()

	if ss.Store() != nil {
		t.Error("expected nil Store after Close")
	}
	if ss.DB() != nil {
		t.Error("expected nil DB after Close")
	}
	if ss.DoltStorage() != nil {
		t.Error("expected nil DoltStorage after Close")
	}
}

func TestOpenSharedStore_NonExistentPath(t *testing.T) {
	// Non-existent beads dir should return nil, nil (not an error)
	ss, err := OpenSharedStore("/nonexistent/path/.beads")
	if err != nil {
		t.Errorf("expected nil error for non-existent path, got: %v", err)
	}
	if ss != nil {
		t.Error("expected nil SharedStore for non-existent path")
	}
}
