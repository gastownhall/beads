//go:build cgo

package embeddeddolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// TestGetHeadHashesReadsHeadCommitAndRoot pins the HEAD read that the
// guidance for Dolt's "Found dangling references" push refusal prints in
// embedded mode, where bd sql does not run: the commit is HEAD's, the root is
// the root value of the commit the branch points to, and neither moves with
// the working set.
func TestGetHeadHashesReadsHeadCommitAndRoot(t *testing.T) {
	ctx := t.Context()
	store := newPeerAuthTestStore(t)
	if err := store.SetConfig(ctx, "issue_prefix", "fedauth"); err != nil {
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}
	if err := store.Commit(ctx, "bd init"); err != nil {
		t.Fatalf("Commit(bd init): %v", err)
	}

	// rootOf reads DOLT_HASHOF_DB for ref: "" is the working set, which also
	// holds the tables bd keeps out of commits, and "main" is the root value
	// of the commit the store's branch points to.
	rootOf := func(ref string) string {
		t.Helper()
		query, args := "SELECT DOLT_HASHOF_DB()", []any{}
		if ref != "" {
			query, args = "SELECT DOLT_HASHOF_DB(?)", []any{ref}
		}
		var hash string
		if err := store.withDBConn(ctx, func(db versioncontrolops.DBConn) error {
			return db.QueryRowContext(ctx, query, args...).Scan(&hash)
		}); err != nil {
			t.Fatalf("read DOLT_HASHOF_DB(%q): %v", ref, err)
		}
		return hash
	}
	headHashes := func() (string, string) {
		t.Helper()
		commit, root, err := store.GetHeadHashes(ctx)
		if err != nil {
			t.Fatalf("GetHeadHashes: %v", err)
		}
		head, err := store.GetCurrentCommit(ctx)
		if err != nil {
			t.Fatalf("GetCurrentCommit: %v", err)
		}
		if commit != head {
			t.Fatalf("GetHeadHashes commit = %q, want HEAD %q", commit, head)
		}
		if branchRoot := rootOf("main"); root != branchRoot || root == commit {
			t.Fatalf("GetHeadHashes root = %q, want the root of main's commit %q (and not the commit %q)", root, branchRoot, commit)
		}
		return commit, root
	}

	commit, root := headHashes()
	working := rootOf("")
	if err := store.SetConfig(ctx, "issue_id_mode", "counter"); err != nil {
		t.Fatalf("SetConfig(issue_id_mode): %v", err)
	}
	if rootOf("") == working {
		t.Fatal("the uncommitted write did not move the working root")
	}
	if gotCommit, gotRoot := headHashes(); gotCommit != commit || gotRoot != root {
		t.Fatalf("after an uncommitted write, GetHeadHashes = (%q, %q), want HEAD's (%q, %q)", gotCommit, gotRoot, commit, root)
	}

	if err := store.Commit(ctx, "set issue_id_mode"); err != nil {
		t.Fatalf("Commit(set issue_id_mode): %v", err)
	}
	if gotCommit, gotRoot := headHashes(); gotCommit == commit || gotRoot == root {
		t.Fatalf("after a commit, GetHeadHashes = (%q, %q), want both to move from (%q, %q)", gotCommit, gotRoot, commit, root)
	}
}
