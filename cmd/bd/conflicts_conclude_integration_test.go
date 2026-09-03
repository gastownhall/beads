//go:build cgo

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// concludeProbeStore keeps the real Dolt store underneath the command while
// making two otherwise hard-to-reproduce edges deterministic: a failed
// diagnostic read, and a commit implementation that silently leaves the merge
// open. It deliberately does not implement storage.Unwrapper, so the optional
// MergeBlockerInspector methods below remain visible to mergeBlockers.
type concludeProbeStore struct {
	storage.DoltStorage
	blockers   *storage.MergeBlockers
	blockerErr error
	noOpCommit bool
	commitErr  error
}

func (s *concludeProbeStore) GetMergeBlockers(ctx context.Context) (storage.MergeBlockers, error) {
	if s.blockers != nil || s.blockerErr != nil {
		if s.blockers == nil {
			return storage.MergeBlockers{}, s.blockerErr
		}
		return *s.blockers, s.blockerErr
	}
	return s.DoltStorage.(storage.MergeBlockerInspector).GetMergeBlockers(ctx)
}

func (s *concludeProbeStore) CommitMergeResolution(ctx context.Context, message string) error {
	if s.commitErr != nil {
		return s.commitErr
	}
	if s.noOpCommit {
		return nil
	}
	return s.DoltStorage.CommitMergeResolution(ctx, message)
}

func wedgeResolvedConcludeMerge(t *testing.T) storage.DoltStorage {
	t.Helper()
	ctx := context.Background()
	raw := newTestStore(t, filepath.Join(t.TempDir(), ".beads", "beads.db"))
	db := raw.DB()

	const issueID = "conclude-command-1"
	if err := raw.CreateIssue(ctx, &types.Issue{
		ID: issueID, Title: "base", Status: types.StatusOpen,
		Priority: 2, IssueType: types.TypeTask,
	}, "test"); err != nil {
		t.Fatalf("create seed issue: %v", err)
	}
	if err := raw.Commit(ctx, "seed conclude command test"); err != nil {
		t.Fatalf("commit seed issue: %v", err)
	}

	var currentBranch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
		t.Fatalf("read current branch: %v", err)
	}
	peerBranch := currentBranch + "_conclude_peer"
	if err := raw.Branch(ctx, peerBranch); err != nil {
		t.Fatalf("create peer branch: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = db.ExecContext(cleanupCtx, "CALL DOLT_MERGE('--abort')")
		_, _ = db.ExecContext(cleanupCtx, "CALL DOLT_CHECKOUT(?)", currentBranch)
		_, _ = db.ExecContext(cleanupCtx, "CALL DOLT_BRANCH('-Df', ?)", peerBranch)
	})

	if _, err := db.ExecContext(ctx, "UPDATE issues SET title = 'ours' WHERE id = ?", issueID); err != nil {
		t.Fatalf("update ours: %v", err)
	}
	if err := raw.Commit(ctx, "our conclude title"); err != nil {
		t.Fatalf("commit ours: %v", err)
	}
	if err := raw.Checkout(ctx, peerBranch); err != nil {
		t.Fatalf("checkout peer: %v", err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE issues SET title = 'theirs' WHERE id = ?", issueID); err != nil {
		t.Fatalf("update theirs: %v", err)
	}
	if err := raw.Commit(ctx, "their conclude title"); err != nil {
		t.Fatalf("commit theirs: %v", err)
	}
	if err := raw.Checkout(ctx, currentBranch); err != nil {
		t.Fatalf("checkout current: %v", err)
	}
	for _, stmt := range []string{
		"SET @@dolt_allow_commit_conflicts = 1",
		"SET @@dolt_force_transaction_commit = 1",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_MERGE(?)", peerBranch); err != nil {
		t.Logf("DOLT_MERGE returned: %v", err)
	}
	rows, err := raw.GetConflictRows(ctx, "issues")
	if err != nil || len(rows) != 1 {
		t.Fatalf("wedge live conflict: rows=%d err=%v", len(rows), err)
	}
	return raw
}

func runConcludeCommand(t *testing.T, commandStore storage.DoltStorage, asJSON bool) (string, string, error) {
	t.Helper()
	stdioMutex.Lock()
	defer stdioMutex.Unlock()

	oldStdout, oldStderr := os.Stdout, os.Stderr
	oldStore, oldRootCtx, oldJSON := store, rootCtx, jsonOutput
	oldConclude := conflictsConclude
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = wOut, wErr
	defer func() { os.Stdout, os.Stderr = oldStdout, oldStderr }()
	store, rootCtx, jsonOutput = commandStore, context.Background(), asJSON
	conflictsConclude = true

	commandErr := conflictsResolveCmd.RunE(conflictsResolveCmd, nil)

	_ = wOut.Close()
	_ = wErr.Close()
	store, rootCtx, jsonOutput = oldStore, oldRootCtx, oldJSON
	conflictsConclude = oldConclude

	var stdout, stderr bytes.Buffer
	_, _ = stdout.ReadFrom(rOut)
	_, _ = stderr.ReadFrom(rErr)
	_ = rOut.Close()
	_ = rErr.Close()
	return stdout.String(), stderr.String(), commandErr
}

func requireExitOne(t *testing.T, err error) {
	t.Helper()
	if code, ok := exitCodeFromError(err); !ok || code != 1 {
		t.Fatalf("command error = %v, want exit code 1", err)
	}
}

func decodeConcludePayload(t *testing.T, out string) map[string]interface{} {
	t.Helper()
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("decode conclude JSON %q: %v", out, err)
	}
	return payload
}

func requireConcludeKeys(t *testing.T, payload map[string]interface{}) {
	t.Helper()
	got := make([]string, 0, len(payload))
	for key := range payload {
		got = append(got, key)
	}
	sort.Strings(got)
	want := []string{"blockers", "committed", "merging", "remaining", "schema_version"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("conclude JSON keys = %v, want %v", got, want)
	}
}

// TestConcludeResolvedMergeCommand drives the actual resolve command handler
// over a real Dolt merge. It pins every branch that planConclude's unit tests
// cannot: JSON's non-zero error, warning emission, haveStatus dispatch, stable
// success/no-op payloads, the real merge commit, and the post-commit guard.
func TestConcludeResolvedMergeCommand(t *testing.T) {
	raw := wedgeResolvedConcludeMerge(t)
	ctx := context.Background()

	t.Run("live conflicts fail in JSON mode", func(t *testing.T) {
		out, _, err := runConcludeCommand(t, raw, true)
		requireExitOne(t, err)
		payload := decodeConcludePayload(t, out)
		if !strings.Contains(payload["error"].(string), "still live") {
			t.Fatalf("live-conflict JSON = %v", payload)
		}
	})

	inspector := raw.(storage.ConflictInspector)
	if n, err := inspector.ResolveConflictRows(ctx, "issues", []string{"conclude-command-1"}, "ours"); err != nil || n != 1 {
		t.Fatalf("resolve conflict without concluding: n=%d err=%v", n, err)
	}

	t.Run("blockers fail in JSON mode", func(t *testing.T) {
		blocked := storage.MergeBlockers{Merging: true, SchemaConflictTables: []string{"issues"}}
		out, _, err := runConcludeCommand(t, &concludeProbeStore{DoltStorage: raw, blockers: &blocked}, true)
		requireExitOne(t, err)
		payload := decodeConcludePayload(t, out)
		if !strings.Contains(payload["error"].(string), "merge not concluded") {
			t.Fatalf("blocked JSON = %v", payload)
		}
	})

	t.Run("blocker read warning is visible", func(t *testing.T) {
		probeErr := errors.New("probe blocker read failed")
		_, stderr, err := runConcludeCommand(t, &concludeProbeStore{
			DoltStorage: raw, blockerErr: probeErr, commitErr: errors.New("stop after warning"),
		}, false)
		requireExitOne(t, err)
		if !strings.Contains(stderr, "Warning: could not read") || !strings.Contains(stderr, probeErr.Error()) {
			t.Fatalf("stderr missing blocker warning: %q", stderr)
		}
	})

	t.Run("silent no-op commit is rejected", func(t *testing.T) {
		_, stderr, err := runConcludeCommand(t, &concludeProbeStore{DoltStorage: raw, noOpCommit: true}, false)
		requireExitOne(t, err)
		if !strings.Contains(stderr, "commit did not conclude the merge") {
			t.Fatalf("stderr missing post-commit guard: %q", stderr)
		}
	})

	t.Run("real commit has stable JSON payload", func(t *testing.T) {
		out, _, err := runConcludeCommand(t, raw, true)
		if err != nil {
			t.Fatalf("conclude real merge: %v; output=%s", err, out)
		}
		payload := decodeConcludePayload(t, out)
		requireConcludeKeys(t, payload)
		if payload["committed"] != true || payload["remaining"] != float64(0) || payload["merging"] != false {
			t.Fatalf("committed payload = %v", payload)
		}
	})

	t.Run("nothing to conclude uses the same JSON shape", func(t *testing.T) {
		out, _, err := runConcludeCommand(t, raw, true)
		if err != nil {
			t.Fatalf("conclude closed merge: %v; output=%s", err, out)
		}
		payload := decodeConcludePayload(t, out)
		requireConcludeKeys(t, payload)
		if payload["committed"] != false || payload["remaining"] != float64(0) || payload["merging"] != false {
			t.Fatalf("no-op payload = %v", payload)
		}
	})
}
