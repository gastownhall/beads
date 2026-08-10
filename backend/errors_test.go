package backend_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/backend"
	"github.com/steveyegge/beads/internal/storage"
)

func TestErrCommitIndeterminateAliasesStorageSentinel(t *testing.T) {
	if backend.ErrCommitIndeterminate != storage.ErrCommitIndeterminate {
		t.Fatal("backend ErrCommitIndeterminate must preserve storage sentinel identity")
	}

	err := fmt.Errorf("commit: %w", backend.ErrCommitIndeterminate)
	if !errors.Is(err, storage.ErrCommitIndeterminate) {
		t.Fatalf("errors.Is(err, storage.ErrCommitIndeterminate) = false; err = %v", err)
	}
}
