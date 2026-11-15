package main

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/memoryops"
)

// memoriesOnlyStore is a remote backend reduced to the one read
// roleExportSource.ExportMemories makes: it embeds a nil DoltStorage and
// answers only the Memories role accessor.
type memoriesOnlyStore struct {
	storage.DoltStorage
	memories memoryops.Memories
	err      error
}

func (s *memoriesOnlyStore) Memories() (memoryops.Memories, error) { return s.memories, s.err }

// failingMemoryList is a memory plane whose listing (listMemories) fails.
type failingMemoryList struct {
	memoryops.Memories
	err error
}

func (l failingMemoryList) List(context.Context, memoryops.ListRequest) (memoryops.ListResult, error) {
	return memoryops.ListResult{}, l.err
}

// TestRoleExportSourceExportMemoriesFailsTheExport pins exportSource's "fail
// rather than return a partial set" on the remote route: a memory plane that
// cannot be reached or listed ends the export with that error, never with an
// empty map. Degrading to empty would bring back what reading memories from
// the settings listing did, a remote `bd export --all` that exits 0 with no
// memory in it.
func TestRoleExportSourceExportMemoriesFailsTheExport(t *testing.T) {
	oldStore := store
	t.Cleanup(func() { store = oldStore })

	unreachable := errors.New("memories: no transport")
	listFailed := errors.New("listMemories: 503 Service Unavailable")
	for _, tc := range []struct {
		name  string
		store *memoriesOnlyStore
		want  error
	}{
		{"the role is unavailable", &memoriesOnlyStore{err: unreachable}, unreachable},
		{"the listing fails", &memoriesOnlyStore{memories: failingMemoryList{err: listFailed}}, listFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store = tc.store
			got, err := (&roleExportSource{}).ExportMemories(context.Background())
			if !errors.Is(err, tc.want) {
				t.Fatalf("ExportMemories error = %v, want %v", err, tc.want)
			}
			if got != nil {
				t.Fatalf("ExportMemories returned %v with its error, want no map", got)
			}
		})
	}
}
