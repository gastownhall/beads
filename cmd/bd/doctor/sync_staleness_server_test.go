//go:build cgo

package doctor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/pushstate"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/testutil"
)

// originSyncWorkspace is a server-mode workspace on the Dolt test server
// whose database has a file:// origin, plus a file:// peer1 when withPeer.
// It returns the workspace root, its .beads directory, and an open store on
// the database for moving refs; nothing is pushed yet.
func originSyncWorkspace(t *testing.T, port int, withPeer bool) (repo, beadsDir string, st *dolt.DoltStore) {
	t.Helper()
	repo, beadsDir = probeOnlyServerWorkspace(t, port)
	t.Setenv("BEADS_DOLT_SERVER_PORT", strconv.Itoa(port))
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, err = dolt.New(ctx, &dolt.Config{
		Path:         t.TempDir(),
		ServerHost:   "127.0.0.1",
		ServerPort:   port,
		Database:     cfg.DoltDatabase,
		MaxOpenConns: 1,
	})
	if err != nil {
		t.Fatalf("open %s: %v", cfg.DoltDatabase, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	remotes := []string{"origin"}
	if withPeer {
		remotes = append(remotes, "peer1")
	}
	for _, name := range remotes {
		if err := st.AddRemote(ctx, name, "file://"+t.TempDir()); err != nil {
			t.Fatalf("add remote %s: %v", name, err)
		}
	}
	return repo, beadsDir, st
}

// doltCall runs a Dolt stored procedure on st's database.
func doltCall(t *testing.T, st *dolt.DoltStore, query string) {
	t.Helper()
	if _, err := st.DB().ExecContext(context.Background(), query); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

// writeDoctorPushState writes beadsDir's push-state.json.
func writeDoctorPushState(t *testing.T, beadsDir string, ps pushstate.State) {
	t.Helper()
	data, err := json.Marshal(ps)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, pushstate.FileName), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestCheckFederationSyncStaleness_OriginTrackingRef_RealDolt drives Sync
// Staleness against a real Dolt server and file:// remotes (GH#6289). The
// origin leg reads only the cached origin ref and push-state.json, so every
// case runs without a network call.
func TestCheckFederationSyncStaleness_OriginTrackingRef_RealDolt(t *testing.T) {
	if testutil.DoltContainerCrashed() {
		t.Skipf("Dolt test server crashed: %v", testutil.DoltContainerCrashError())
	}
	port := doctorTestServerPort()
	if port == 0 {
		t.Skip("Dolt test server not available")
	}
	ctx := context.Background()

	t.Run("AC-6: failed attempt with the cached origin ref equal to HEAD warns", func(t *testing.T) {
		repo, beadsDir, st := originSyncWorkspace(t, port, false)
		if err := st.Push(ctx); err != nil {
			t.Fatalf("push origin: %v", err)
		}
		head, err := st.GetCurrentCommit(ctx)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC().Format(time.RFC3339)
		writeDoctorPushState(t, beadsDir, pushstate.State{
			LastPush: now, LastCommit: head, LastAttempt: now, LastError: "timed out after 30s",
		})

		check := CheckFederationSyncStaleness(repo)
		if check.Status != StatusWarning || check.Message != "Origin sync staleness detected" ||
			!strings.Contains(check.Detail, "at "+now+" failed: timed out after 30s") || !strings.Contains(check.Fix, "bd dolt push") {
			t.Fatalf("got %s %q (detail %q, fix %q), want a warning naming the failed push and how to retry",
				check.Status, check.Message, check.Detail, check.Fix)
		}
	})

	t.Run("AC-7: origin added but never fetched or pushed warns that the comparison is unavailable", func(t *testing.T) {
		repo, _, _ := originSyncWorkspace(t, port, false)
		check := CheckFederationSyncStaleness(repo)
		if check.Status != StatusWarning || check.Message != "Origin sync staleness detected" ||
			!strings.Contains(check.Detail, "comparison with cached remote refs unavailable") {
			t.Fatalf("got %s %q (detail %q), want a warning that no cached origin ref exists", check.Status, check.Message, check.Detail)
		}
	})

	t.Run("AC-7: refreshed cached origin ref equal to HEAD passes locally", func(t *testing.T) {
		repo, _, st := originSyncWorkspace(t, port, false)
		if err := st.Push(ctx); err != nil {
			t.Fatalf("push origin: %v", err)
		}
		check := CheckFederationSyncStaleness(repo)
		if check.Status != StatusOK || check.Message != "No local sync staleness detected" ||
			!strings.Contains(check.Detail, "do not verify the remote's current contents") {
			t.Fatalf("got %s %q (detail %q), want the explicitly local-only pass", check.Status, check.Message, check.Detail)
		}
	})

	t.Run("AC-7: a commit after the push warns that local history is ahead", func(t *testing.T) {
		repo, _, st := originSyncWorkspace(t, port, false)
		if err := st.Push(ctx); err != nil {
			t.Fatalf("push origin: %v", err)
		}
		doltCall(t, st, "CALL DOLT_COMMIT('--allow-empty', '-m', 'unpushed')")
		check := CheckFederationSyncStaleness(repo)
		if check.Status != StatusWarning || check.Message != "Origin sync staleness detected" ||
			!strings.Contains(check.Detail, "origin: local is 1 commit(s) ahead of the cached origin tracking ref") {
			t.Fatalf("got %s %q (detail %q), want a warning that local is 1 commit ahead of the cached origin ref", check.Status, check.Message, check.Detail)
		}
	})

	t.Run("AC-8: disabled origin is skipped and peer warnings are kept", func(t *testing.T) {
		repo, beadsDir, st := originSyncWorkspace(t, port, true)
		if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("dolt:\n  local-only: true\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		// One commit on peer1 that local history lacks; origin is never
		// pushed and its push history records a failure, either of which
		// would warn if the origin leg ran.
		doltCall(t, st, "CALL DOLT_COMMIT('--allow-empty', '-m', 'peer only')")
		if err := st.PushRemote(ctx, "peer1", false); err != nil {
			t.Fatalf("push peer1: %v", err)
		}
		doltCall(t, st, "CALL DOLT_RESET('--hard', 'HEAD~1')")
		writeDoctorPushState(t, beadsDir, pushstate.State{LastAttempt: time.Now().UTC().Format(time.RFC3339), LastError: "timed out after 30s"})

		check := CheckFederationSyncStaleness(repo)
		if check.Status != StatusWarning || check.Message != "1 total commits behind peers" ||
			!strings.Contains(check.Detail, "origin check skipped (dolt.local-only: true)") ||
			!strings.Contains(check.Detail, "peer1: 1 commits behind") ||
			strings.Contains(check.Detail, "failed") || strings.Contains(check.Detail, "cached origin") ||
			check.Fix != "Run 'bd federation sync' to synchronize with peers" {
			t.Fatalf("got %s %q (detail %q, fix %q), want the peer warning with the origin check skipped",
				check.Status, check.Message, check.Detail, check.Fix)
		}
	})
}
