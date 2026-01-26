package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

func TestDoltStoreImplementsCredentialMethods(t *testing.T) {
	// Compile-time check that DoltStore implements FederatedStorage with credential methods
	var _ storage.FederatedStorage = (*DoltStore)(nil)
	t.Log("DoltStore implements FederatedStorage interface with credential methods")
}
