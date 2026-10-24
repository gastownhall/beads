package backend_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/backend"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
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

func TestErrRenameLabelSameNameAliasesIssueopsSentinel(t *testing.T) {
	if backend.ErrRenameLabelSameName != issueops.ErrRenameLabelSameName {
		t.Fatal("backend ErrRenameLabelSameName must preserve the issueops sentinel identity")
	}

	err := fmt.Errorf("rename: %w", backend.ErrRenameLabelSameName)
	if !errors.Is(err, issueops.ErrRenameLabelSameName) {
		t.Fatalf("errors.Is(err, issueops.ErrRenameLabelSameName) = false; err = %v", err)
	}
}
