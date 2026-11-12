package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pullMergeMessageMarker is the stand-in for a token in the remote URL. A
// file:// URL drops userinfo, but Dolt copies whatever URL it stored into a
// DOLT_PULL merge message, so the marker sits in the path where it survives.
const pullMergeMessageMarker = "SECRETe2q8zs"

// divergedPullFixture is a server-mode store and a peer clone of the store's
// file remote, whose histories have diverged: each holds a commit the other
// lacks, so the store's next pull has to make a merge commit.
type divergedPullFixture struct {
	store     *DoltStore
	remoteURL string
}

// newDivergedPullFixture builds the fixture. remoteName is the remote both
// sides name; setUpstream pushes with --set-upstream so the store's branch
// tracks remoteName/main, which is what sends a bare CALL DOLT_PULL(<remote>)
// through Dolt's own merge rather than the URL-free GH#3144 fallback.
func newDivergedPullFixture(t *testing.T, ctx context.Context, remoteName string, setUpstream bool) divergedPullFixture {
	t.Helper()
	return newPullFixture(t, ctx, remoteName, setUpstream, true)
}

// newPullFixture is newDivergedPullFixture with the local commit optional:
// without it the store is only behind the remote, so its pull fast-forwards.
func newPullFixture(t *testing.T, ctx context.Context, remoteName string, setUpstream, localDiverges bool) divergedPullFixture {
	t.Helper()
	skipIfNoDolt(t)

	tmpDir := t.TempDir()
	dbName := uniqueTestDBName(t)
	peerDB := dbName + "_peer"

	store, err := New(ctx, &Config{
		Path:            tmpDir,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		Database:        dbName,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		_, _ = store.db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", peerDB))
		_, _ = store.db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
		store.Close()
	})

	mustExec := func(stage, query string, args ...any) {
		t.Helper()
		if _, err := store.db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
	}

	mustExec("create marker table", "CREATE TABLE pull_msg_marker (id INT PRIMARY KEY, v VARCHAR(64))")
	mustExec("stage", "CALL DOLT_ADD('-A')")
	mustExec("commit", "CALL DOLT_COMMIT('-m', 'test: pull message marker')")

	remoteURL := "file://" + filepath.Join(tmpDir, "x-access-token-"+pullMergeMessageMarker)
	mustExec("add remote", "CALL DOLT_REMOTE('add', ?, ?)", remoteName, remoteURL)
	if setUpstream {
		mustExec("push", "CALL DOLT_PUSH('--set-upstream', ?, 'main')", remoteName)
	} else {
		mustExec("push", "CALL DOLT_PUSH(?, 'main')", remoteName)
	}
	store.remote = remoteName
	store.branch = "main"

	if setUpstream {
		// Fatal precondition: upstream tracking is in effect. Base's peer SQL
		// route was a remote-only CALL DOLT_PULL(<remote>); with tracking that
		// call takes Dolt's own merge, the one that writes the remote URL into
		// the message. Without tracking it fails and base took the URL-free
		// GH#3144 fallback, so the fixture would not reproduce the leak. Run
		// that call here, with nothing to merge yet, and roll it back.
		probe, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin tracking probe: %v", err)
		}
		if _, err := probe.ExecContext(ctx, "CALL DOLT_PULL(?)", remoteName); err != nil {
			_ = probe.Rollback()
			t.Fatalf("fixture broken: remote-only DOLT_PULL(?) failed (%v); upstream tracking from --set-upstream is not in effect", err)
		}
		_ = probe.Rollback()
	}

	var storedURL string
	if err := store.db.QueryRowContext(ctx, "SELECT url FROM dolt_remotes WHERE name = ?", remoteName).Scan(&storedURL); err != nil {
		t.Fatalf("read stored remote url: %v", err)
	}
	if !strings.Contains(storedURL, pullMergeMessageMarker) {
		t.Fatalf("fixture broken: the stored remote URL %q lacks the marker", storedURL)
	}

	mustExec("clone peer", "CALL DOLT_CLONE(?, ?)", remoteURL, peerDB)
	peer := openPeerDB(t, store.connStr, peerDB)
	t.Cleanup(func() { _ = peer.Close() })

	// Local diverges first...
	if localDiverges {
		mustExec("local insert", "INSERT INTO pull_msg_marker VALUES (2, 'local')")
		mustExec("local stage", "CALL DOLT_ADD('-A')")
		mustExec("local commit", "CALL DOLT_COMMIT('-m', 'local: add row')")
	}

	// ...then another clone pushes a different commit.
	for _, step := range []struct{ stage, query string }{
		{"insert", "INSERT INTO pull_msg_marker VALUES (1, 'from peer')"},
		{"stage", "CALL DOLT_ADD('-A')"},
		{"commit", "CALL DOLT_COMMIT('-m', 'peer: add row')"},
		{"push", "CALL DOLT_PUSH('origin', 'main')"},
	} {
		if _, err := peer.ExecContext(ctx, step.query); err != nil {
			t.Fatalf("peer %s: %v", step.stage, err)
		}
	}

	return divergedPullFixture{store: store, remoteURL: remoteURL}
}

// assertMergeMessageOmitsRemoteURL checks what a merging pull left behind:
// the peer's row arrived (control), HEAD is a two-parent merge (precondition:
// only a merge commit can carry the message), no commit message in history
// holds the marker or a file:// URL, and HEAD's message names the tracking ref
// wantRef.
func assertMergeMessageOmitsRemoteURL(t *testing.T, ctx context.Context, db *sql.DB, wantRef string) {
	t.Helper()

	var got string
	if err := db.QueryRowContext(ctx, "SELECT v FROM pull_msg_marker WHERE id = 1").Scan(&got); err != nil {
		t.Fatalf("control broken: the pull did not deliver the peer's row: %v", err)
	}
	if got != "from peer" {
		t.Fatalf("control broken: peer row = %q, want %q", got, "from peer")
	}

	var parents int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_commit_ancestors WHERE commit_hash = HASHOF('HEAD')").Scan(&parents); err != nil {
		t.Fatalf("count HEAD parents: %v", err)
	}
	if parents != 2 {
		t.Fatalf("precondition broken: HEAD has %d parent(s), want a two-parent merge commit", parents)
	}

	var leaking int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_log WHERE message LIKE ? OR message LIKE '%file://%'",
		"%"+pullMergeMessageMarker+"%").Scan(&leaking); err != nil {
		t.Fatalf("scan dolt_log: %v", err)
	}
	var headMsg string
	if err := db.QueryRowContext(ctx, "SELECT message FROM dolt_log LIMIT 1").Scan(&headMsg); err != nil {
		t.Fatalf("read HEAD message: %v", err)
	}
	t.Logf("merge commit message: %q", headMsg)
	if leaking != 0 {
		t.Errorf("%d commit message(s) record the remote URL, which bd dolt push would publish; HEAD reads %q", leaking, headMsg)
	}
	if !strings.Contains(headMsg, wantRef) {
		t.Errorf("merge message %q does not name the tracking ref %q", headMsg, wantRef)
	}
}

// TestServerPullMergeMessageOmitsRemoteURL pins bd-e2q8zs on both SQL routes
// of the default-remote pull: a server-mode pull that has to merge must not
// copy the stored remote URL (credentials included) into the merge commit
// message. Dolt's own pull writes "Merge branch 'main' of <url> into main";
// bd now fetches and merges the tracking ref, whose message names only
// origin/main.
func TestServerPullMergeMessageOmitsRemoteURL(t *testing.T) {
	for _, tc := range []struct {
		name       string
		remoteUser string
	}{
		{name: "default"},
		// The Hosted Dolt route (remoteUser set, remote == s.remote). A file
		// remote ignores --user: it only sets the GRPC username parameter.
		{name: "remote_user", remoteUser: "e2q8zs-user"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()

			fx := newDivergedPullFixture(t, ctx, "origin", false)
			store := fx.store
			store.remoteUser = tc.remoteUser

			// Route precondition: credentials must not divert this pull to the
			// CLI credential route, or the subtest would pass on the wrong route.
			if useCLI, err := store.prepareCLIRouteForCredentials(ctx, "origin", store.credentialsForRemote("origin")); err != nil || useCLI {
				t.Fatalf("precondition broken: credential CLI route = (%v, %v), want the SQL route", useCLI, err)
			}

			report, err := store.pullTransportReporting(ctx, "origin")
			if err != nil {
				t.Fatalf("pull: %v", err)
			}
			if !report.Merged {
				t.Errorf("a merging pull reported Merged=false (%+v)", report)
			}
			assertMergeMessageOmitsRemoteURL(t, ctx, store.db, "origin/main")
		})
	}
}

// TestFederationPullFromMergeMessageOmitsPeerURL is the federation peer twin
// of TestServerPullMergeMessageOmitsRemoteURL (bd-e2q8zs). The store's branch
// tracks the peer (pushed with --set-upstream), which is what sent the peer
// route's bare CALL DOLT_PULL(<peer>) through Dolt's own merge, the one that
// writes the peer URL into the message. PullFrom now fetches the peer's
// branch and merges <peer>/<branch>.
func TestFederationPullFromMergeMessageOmitsPeerURL(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	fx := newDivergedPullFixture(t, ctx, "origin", true)
	conflicts, err := fx.store.PullFrom(ctx, "origin")
	if err != nil {
		t.Fatalf("PullFrom: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("PullFrom returned conflicts %+v for disjoint rows", conflicts)
	}
	assertMergeMessageOmitsRemoteURL(t, ctx, fx.store.db, "origin/main")
}

// TestServerPullFastForwardAndNoOpMakeNoMergeCommit pins that the fetch+merge
// SQL route (bd-e2q8zs) keeps the fast-forward and no-op behavior DOLT_PULL
// had: a store that is only behind fast-forwards without a merge commit and
// reports Merged, the repeated pull changes nothing and reports !Merged, and
// verifyPullLanded accepts both.
func TestServerPullFastForwardAndNoOpMakeNoMergeCommit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	fx := newPullFixture(t, ctx, "origin", false, false)
	store := fx.store

	headOf := func() (hash string, parents int) {
		t.Helper()
		var err error
		if hash, err = store.GetCurrentCommit(ctx); err != nil {
			t.Fatalf("read HEAD: %v", err)
		}
		if err := store.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM dolt_commit_ancestors WHERE commit_hash = ?", hash).Scan(&parents); err != nil {
			t.Fatalf("count HEAD parents: %v", err)
		}
		return hash, parents
	}

	preFF, _ := headOf()
	ff, err := store.pullTransportReporting(ctx, "origin")
	if err != nil {
		t.Fatalf("fast-forward pull: %v", err)
	}
	ffHead, ffParents := headOf()
	var got string
	if err := store.db.QueryRowContext(ctx, "SELECT v FROM pull_msg_marker WHERE id = 1").Scan(&got); err != nil {
		t.Fatalf("control broken: the fast-forward did not deliver the peer's row: %v", err)
	}
	if ffHead == preFF || ffParents != 1 {
		t.Errorf("fast-forward pull: HEAD %s -> %s with %d parent(s), want a moved single-parent HEAD (no merge commit)", preFF, ffHead, ffParents)
	}
	if !ff.Reported || !ff.Merged {
		t.Errorf("fast-forward pull report = %+v, want Merged", ff)
	}
	if err := store.verifyPullLanded(ctx, "origin", preFF); err != nil {
		t.Errorf("verifyPullLanded after the fast-forward: %v", err)
	}

	noop, err := store.pullTransportReporting(ctx, "origin")
	if err != nil {
		t.Fatalf("no-op pull: %v", err)
	}
	noopHead, _ := headOf()
	if noopHead != ffHead {
		t.Errorf("no-op pull moved HEAD %s -> %s", ffHead, noopHead)
	}
	if !noop.Reported || noop.Merged {
		t.Errorf("no-op pull report = %+v, want Reported and !Merged", noop)
	}
	if err := store.verifyPullLanded(ctx, "origin", ffHead); err != nil {
		t.Errorf("verifyPullLanded after the no-op: %v", err)
	}

	var merges int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_log WHERE message LIKE 'Merge%'").Scan(&merges); err != nil {
		t.Fatalf("scan dolt_log: %v", err)
	}
	if merges != 0 {
		t.Errorf("%d merge commit(s) in history after a fast-forward and a no-op pull", merges)
	}
}
