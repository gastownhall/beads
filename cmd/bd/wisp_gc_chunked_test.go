//go:build cgo

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// wispGCRecordingStore is a real store whose deletion role records every
// request it is handed and, while failAt is set, refuses the failAt-th one
// (1-based) before it reaches the store, so the refused request changes
// nothing.
type wispGCRecordingStore struct {
	*dolt.DoltStore
	requests []issueops.DeleteRequest
	failAt   int
}

// errWispGCInjected is the stable, nonretryable refusal the recording store
// injects.
var errWispGCInjected = errors.New("injected deletion refusal")

func (s *wispGCRecordingStore) Deleter() (issueops.Deleter, error) {
	inner, err := s.DoltStore.Deleter()
	if err != nil {
		return nil, err
	}
	return wispGCRecordingDeleter{s: s, inner: inner}, nil
}

type wispGCRecordingDeleter struct {
	s     *wispGCRecordingStore
	inner issueops.Deleter
}

func (d wispGCRecordingDeleter) Delete(ctx context.Context, req issueops.DeleteRequest) (issueops.DeleteResult, error) {
	req.IDs = slices.Clone(req.IDs)
	d.s.requests = append(d.s.requests, req)
	if d.s.failAt > 0 && len(d.s.requests) == d.s.failAt {
		return issueops.DeleteResult{}, errWispGCInjected
	}
	return d.inner.Delete(ctx, req)
}

func (s *wispGCRecordingStore) requestSizes() []int {
	sizes := make([]int, 0, len(s.requests))
	for _, req := range s.requests {
		sizes = append(sizes, len(req.IDs))
	}
	return sizes
}

// useWispGCRealStore opens a fresh Dolt-backed store, installs it (behind the
// recording deletion role) as the store the direct gc route reads, and
// restores every global the command touches.
func useWispGCRealStore(t *testing.T) (*dolt.DoltStore, *wispGCRecordingStore) {
	t.Helper()
	s := newTestStore(t, filepath.Join(t.TempDir(), ".beads", "beads.db"))
	rec := &wispGCRecordingStore{DoltStore: s}

	ensureCleanGlobalState(t)
	saveAndRestoreGlobals(t)
	t.Setenv("BEADS_DIR", t.TempDir())
	oldCtx, oldJSON, oldQuiet, oldProxied, oldActor := rootCtx, jsonOutput, quietFlag, proxiedServerMode, actor
	oldDidWrite := commandDidWrite.Load()
	t.Cleanup(func() {
		rootCtx, jsonOutput, quietFlag, proxiedServerMode, actor = oldCtx, oldJSON, oldQuiet, oldProxied, oldActor
		commandDidWrite.Store(oldDidWrite)
		commandDeletedIssueIDs.reset()
	})
	commandDidWrite.Store(false)
	commandDeletedIssueIDs.reset()
	store, rootCtx, jsonOutput, quietFlag, proxiedServerMode, actor = rec, context.Background(), false, false, false, "test"
	return s, rec
}

// seedWisps creates n ephemeral tasks with the given status.
func seedWisps(t *testing.T, s *dolt.DoltStore, n int, status types.Status) []*types.Issue {
	t.Helper()
	now := time.Now()
	wisps := make([]*types.Issue, n)
	for i := range wisps {
		wisps[i] = &types.Issue{
			Title:     fmt.Sprintf("patrol step %d", i),
			Status:    status,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
		}
		if status == types.StatusClosed {
			wisps[i].ClosedAt = &now
		}
	}
	if err := s.CreateIssues(context.Background(), wisps, "test"); err != nil {
		t.Fatalf("creating %d %s wisps: %v", n, status, err)
	}
	return wisps
}

// wispCount is how many wisps are left in s.
func wispCount(t *testing.T, s *dolt.DoltStore) int {
	t.Helper()
	ephemeral := true
	found, err := s.SearchIssues(context.Background(), "", types.IssueFilter{Ephemeral: &ephemeral, Limit: 10000})
	if err != nil {
		t.Fatalf("listing wisps: %v", err)
	}
	return len(found)
}

// wispGCClosedFlags is `bd mol wisp gc --closed --force`; wispGCAgeFlags is
// `bd mol wisp gc --age 0s`, which selects every unprotected open wisp.
var (
	wispGCClosedFlags = map[string]string{"closed": "true", "force": "true"}
	wispGCAgeFlags    = map[string]string{"age": "0s"}
)

// runWispGCCommand runs `bd mol wisp gc` with flags through the command's own
// RunE, capturing stdout and stderr, and puts every flag back afterwards.
func runWispGCCommand(t *testing.T, flags map[string]string) (stdout, stderr string, err error) {
	t.Helper()
	t.Cleanup(func() {
		wispGCCmd.Flags().VisitAll(func(f *pflag.Flag) {
			if f.Changed {
				if sv, ok := f.Value.(pflag.SliceValue); ok {
					_ = sv.Replace(nil)
				} else {
					_ = f.Value.Set(f.DefValue)
				}
				f.Changed = false
			}
		})
	})
	for name, value := range flags {
		if err := wispGCCmd.Flags().Set(name, value); err != nil {
			t.Fatalf("setting --%s=%s: %v", name, value, err)
		}
	}
	return captureWispGC(t, func() error { return runWispGC(wispGCCmd, nil) })
}

// TestWispGCDeletesInBoundedTransactions is the regression test for GH#6246
// on both direct gc modes: 450 eligible wisps go to the deletion role in
// three uncascaded, forced requests of 200, 200 and 50 (one transaction
// each), every one is deleted, and the command prints one aggregate result.
// The age mode handed its whole selection to one request, so one transaction
// carried the blocked-state recompute of every wisp it deleted.
func TestWispGCDeletesInBoundedTransactions(t *testing.T) {
	modes := []struct {
		name   string
		status types.Status
		flags  map[string]string
	}{
		{"closed", types.StatusClosed, wispGCClosedFlags},
		{"age", types.StatusOpen, wispGCAgeFlags},
	}
	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			s, rec := useWispGCRealStore(t)
			seedWisps(t, s, 450, mode.status)
			jsonOutput = true

			stdout, stderr, err := runWispGCCommand(t, mode.flags)
			if err != nil {
				t.Fatalf("gc: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
			}
			if got, want := rec.requestSizes(), []int{200, 200, 50}; !slices.Equal(got, want) {
				t.Errorf("deletion request sizes = %v, want %v", got, want)
			}
			seen := map[string]bool{}
			for i, req := range rec.requests {
				if !req.Force || req.Cascade || req.DryRun {
					t.Errorf("request %d: force=%v cascade=%v dry_run=%v, want a forced, uncascaded, real delete", i+1, req.Force, req.Cascade, req.DryRun)
				}
				for _, id := range req.IDs {
					if seen[id] {
						t.Errorf("request %d repeats %s", i+1, id)
					}
					seen[id] = true
				}
			}
			if left := wispCount(t, s); left != 0 {
				t.Errorf("%d wisps remain, want 0", left)
			}
			obj := decodeSingleJSONObject(t, stdout)
			if got := obj["deleted_count"]; got != float64(450) {
				t.Errorf("deleted_count = %v, want 450", got)
			}
			if got, _ := obj["deleted_ids"].([]interface{}); len(got) != 450 {
				t.Errorf("deleted_ids names %d ids, want 450", len(got))
			}
			if got, _ := obj["deleted"].([]interface{}); len(got) != 450 {
				t.Errorf("deleted names %d ids, want 450", len(got))
			}
		})
	}
}

// TestWispGCKeepsCommittedChunksWhenALaterChunkFails pins that an age gc
// whose second deletion request is refused before it changes anything keeps
// the first chunk deleted, sends no third request, exits nonzero naming the
// 200 confirmed deletions and the 250 selected wisps it could not confirm,
// with the age command's own wording, and that rerunning without the fault
// deletes the rest. With the whole selection in one request, a failure took
// every deletion of the run with it.
func TestWispGCKeepsCommittedChunksWhenALaterChunkFails(t *testing.T) {
	s, rec := useWispGCRealStore(t)
	wisps := seedWisps(t, s, 450, types.StatusOpen)
	rec.failAt = 2

	stdout, stderr, err := runWispGCCommand(t, wispGCAgeFlags)
	if err == nil {
		t.Fatalf("gc succeeded, want the injected refusal\nstdout:\n%s", stdout)
	}
	if code, ok := exitCodeFromError(err); !ok || code != 1 {
		t.Errorf("error = %v, want an already-reported exit 1", err)
	}
	if got, want := rec.requestSizes(), []int{200, 200}; !slices.Equal(got, want) {
		t.Errorf("deletion request sizes = %v, want %v (no request after the refused one)", got, want)
	}
	if left := wispCount(t, s); left != 250 {
		t.Errorf("%d wisps remain, want the 250 the first chunk did not carry", left)
	}
	for _, id := range rec.requests[0].IDs {
		if _, err := s.GetIssue(context.Background(), id); err == nil {
			t.Errorf("%s of the committed first chunk is still present", id)
			break
		}
	}
	if !bytes.Contains([]byte(stdout), []byte("Deleted 200 issue(s)")) {
		t.Errorf("stdout lacks the 200 confirmed deletions:\n%s", stdout)
	}
	for _, want := range []string{
		"wisp gc stopped after deleting 200 of 450 selected wisp(s); 250 not confirmed deleted",
		errWispGCInjected.Error(),
		"Rerun the same command to reselect current data and delete the remaining abandoned wisps.",
	} {
		if !bytes.Contains([]byte(stderr), []byte(want)) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if bytes.Contains([]byte(stderr), []byte("--closed")) {
		t.Errorf("an age gc failure names the closed purge:\n%s", stderr)
	}
	assertExportBookkeeping(t, rec.requests[0].IDs, rec.requests[1].IDs)

	rec.failAt, rec.requests = 0, nil
	stdout, stderr, err = runWispGCCommand(t, wispGCAgeFlags)
	if err != nil {
		t.Fatalf("rerun: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if got, want := rec.requestSizes(), []int{200, 50}; !slices.Equal(got, want) {
		t.Errorf("rerun request sizes = %v, want %v", got, want)
	}
	if left := wispCount(t, s); left != 0 {
		t.Errorf("%d of %d wisps remain after the rerun, want 0", left, len(wisps))
	}
}
