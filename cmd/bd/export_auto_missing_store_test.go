//go:build cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
)

// TestMissingJSONLIssueIDsInStore_IgnoresCompactedWisp pins the actual
// GH#4988 behaviour at the layer that wedged auto-export permanently:
// missingJSONLIssueIDsInStore itself. maphew's review noted that
// TestMissingJSONLIssueIDsInStore is named in the original guard commit's
// validation list but doesn't exist in the tree; this closes that gap.
//
// Seeds one persistent issue (present in the store) and a JSONL file that
// additionally lists an ephemeral wisp id that is NOT in the store — the
// TTL-compaction scenario, where a wisp was written to issues.jsonl and
// later deleted from Dolt by compaction. Asserts `missing` comes back
// empty: an out-of-scope (ephemeral) row's absence from the store must not
// be treated as a hard orphan, or auto-export wedges forever.
//
// Requires a live Dolt test server/container (cgo build); skips otherwise.
func TestMissingJSONLIssueIDsInStore_IgnoresCompactedWisp(t *testing.T) {
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available")
	}
	if testutil.DoltContainerCrashed() {
		t.Skipf("Dolt test server crashed: %v", testutil.DoltContainerCrashError())
	}

	ensureTestMode(t)
	saveAndRestoreGlobals(t)

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	dbName := uniqueTestDBName(t)
	testDBPath := filepath.Join(beadsDir, "dolt")
	writeTestMetadata(t, testDBPath, dbName)
	inner := newTestStore(t, testDBPath)
	store = inner
	storeMutex.Lock()
	storeActive = true
	storeMutex.Unlock()
	t.Cleanup(func() {
		store = nil
		storeMutex.Lock()
		storeActive = false
		storeMutex.Unlock()
	})

	ctx := context.Background()

	persistent := &types.Issue{
		ID:        "test-persist-1",
		Title:     "Persistent issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := inner.CreateIssue(ctx, persistent, "test-user"); err != nil {
		t.Fatalf("seed persistent issue: %v", err)
	}
	// The wisp is deliberately NOT created in the store: it simulates a
	// wisp that TTL compaction already deleted from Dolt after an earlier
	// export wrote it into issues.jsonl.

	jsonlPath := filepath.Join(tmpDir, "issues.jsonl")
	jsonl := strings.Join([]string{
		`{"_type":"issue","id":"test-persist-1","issue_type":"task"}`,
		`{"_type":"issue","id":"test-wisp-compacted","issue_type":"task","ephemeral":true}`,
		"",
	}, "\n")
	if err := os.WriteFile(jsonlPath, []byte(jsonl), 0o644); err != nil {
		t.Fatal(err)
	}

	missing, err := missingJSONLIssueIDsInStore(ctx, jsonlPath)
	if err != nil {
		t.Fatalf("missingJSONLIssueIDsInStore: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("missing = %v, want empty (compacted wisp is out-of-scope and must not be a hard orphan)", missing)
	}
}
