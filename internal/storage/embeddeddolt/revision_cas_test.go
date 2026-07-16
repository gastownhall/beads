//go:build cgo

package embeddeddolt_test

import (
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestEmbeddedUpdateIssueIfMatchConcurrent pins the embedded store's serialization
// claim (red-team B1.2 #4). The embedded leg has no commit-time (1213) retry — it
// relies on withConn's fresh per-operation connection to serialize writes. This
// races N whole-row CAS writers that guard the same revision but write different
// columns: exactly one must win, the rest must fail with a typed precondition
// error, and ZERO may fail with a raw error (a leaked 1213/1205 would mean the
// serialization assumption is false and the embedded CAS needs a retry/lock).
func TestEmbeddedUpdateIssueIfMatchConcurrent(t *testing.T) {
	te := newTestEnv(t, "ecas")
	ctx := t.Context()

	iss := &types.Issue{
		ID: "ecas-1", Title: "t", Status: types.StatusOpen,
		IssueType: types.TypeTask, Priority: 2,
	}
	if err := te.store.CreateIssue(ctx, iss, "tester"); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := te.store.GetIssue(ctx, "ecas-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	base := got.Revision
	if base == 0 {
		t.Fatal("created issue has revision 0; embedded insert did not stamp a nonce")
	}

	fields := []string{"title", "description", "design", "notes", "acceptance_criteria"}
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins, conflicts, others := 0, 0, 0

	for i := range fields {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			exp := base
			_, err := te.store.UpdateIssueIfMatch(ctx, "ecas-1",
				map[string]interface{}{fields[i]: "w" + strconv.Itoa(i)}, &exp, "tester")
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, storage.ErrPreconditionFailed):
				conflicts++
			default:
				others++
				t.Errorf("racer %d: unexpected non-CAS error (embedded serialization leak?): %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	if wins != 1 {
		t.Fatalf("wins = %d, want exactly 1 (split-brain / lost update if >1)", wins)
	}
	if conflicts != len(fields)-1 {
		t.Fatalf("conflicts = %d, want %d", conflicts, len(fields)-1)
	}
	if others != 0 {
		t.Fatalf("raw (non-CAS) errors = %d — embedded writes did not serialize cleanly", others)
	}
	after, err := te.store.GetIssue(ctx, "ecas-1")
	if err != nil {
		t.Fatalf("final get: %v", err)
	}
	if after.Revision == base {
		t.Fatal("revision unchanged after a winning CAS")
	}
}
