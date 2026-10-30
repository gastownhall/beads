package backend_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/backend"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/issueops"
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

// TestErrIdempotencyExpiredAliasesTheIssueopsSentinel pins that the retired-id
// refusal is ONE value across the three packages that name it, so a backend
// consumer's errors.Is matches what a store returned.
func TestErrIdempotencyExpiredAliasesTheIssueopsSentinel(t *testing.T) {
	if backend.ErrIdempotencyExpired != storage.ErrIdempotencyExpired || storage.ErrIdempotencyExpired != issueops.ErrIdempotencyExpired {
		t.Fatal("ErrIdempotencyExpired must be the same value in backend, internal/storage and issueops")
	}
	var err error = &issueops.IdempotencyExpiredError{ID: "bd-1"}
	if !errors.Is(fmt.Errorf("create: %w", err), backend.ErrIdempotencyExpired) {
		t.Fatalf("errors.Is(*IdempotencyExpiredError, backend.ErrIdempotencyExpired) = false")
	}
}
