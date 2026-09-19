//go:build cgo

package embeddeddolt

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

func TestEmbeddedDoltStoreIssueLifecycleBuildsOperations(t *testing.T) {
	lifecycle, err := (&EmbeddedDoltStore{}).IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle() error = %v", err)
	}
	if lifecycle == nil {
		t.Fatal("IssueLifecycle() returned nil operations")
	}
}

func TestEmbeddedDoltStoreIssueLifecycleRejectsNilStore(t *testing.T) {
	lifecycle, err := (*EmbeddedDoltStore)(nil).IssueLifecycle()
	if lifecycle != nil {
		t.Fatalf("IssueLifecycle() lifecycle = %T, want nil", lifecycle)
	}
	var unsupported *storage.ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("IssueLifecycle() error = %v, want *storage.ErrUnsupported", err)
	}
}
