package dolt

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

func TestDoltStoreIssueLifecycleBuildsOperations(t *testing.T) {
	lifecycle, err := (&DoltStore{}).IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle() error = %v", err)
	}
	if lifecycle == nil {
		t.Fatal("IssueLifecycle() returned nil operations")
	}
}

func TestDoltStoreIssueLifecycleRejectsNilStore(t *testing.T) {
	lifecycle, err := (*DoltStore)(nil).IssueLifecycle()
	if lifecycle != nil {
		t.Fatalf("IssueLifecycle() lifecycle = %T, want nil", lifecycle)
	}
	var unsupported *storage.ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("IssueLifecycle() error = %v, want *storage.ErrUnsupported", err)
	}
}
