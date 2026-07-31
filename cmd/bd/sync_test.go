package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// syncOpsRecorder builds a syncOps whose steps are scripted per attempt and
// which records how many times each step ran. Scripts are indexed by call
// number; a call past the end of a script reuses the script's last entry, so a
// nil script means "always succeed".
type syncOpsRecorder struct {
	pullErrs      []error
	pullConflicts [][]string
	conflictsSeq  [][]string
	conflictsErrs []error
	recomputeVals []int
	recomputeErrs []error
	pushErrs      []error
	// fingerprints/fingerprintErrs script the dirty-graph evidence hook, one
	// entry per blocked attempt. Both empty leaves the hook NIL, which is the
	// production shape for a store that cannot answer the question at all.
	fingerprints    []string
	fingerprintErrs []error
	// mergeBlockers/mergeBlockersErrs script the positive constraint-violation
	// hook, one entry per blocked attempt. Both empty leaves the hook NIL,
	// mirroring fingerprints above.
	mergeBlockers     []storage.MergeBlockers
	mergeBlockersErrs []error

	pulls              int
	conflicts          int
	recomputes         int
	pushes             int
	fingerprintCalls   int
	mergeBlockersCalls int
}

func scriptedErr(script []error, call int) error {
	if len(script) == 0 {
		return nil
	}
	if call >= len(script) {
		return script[len(script)-1]
	}
	return script[call]
}

func (r *syncOpsRecorder) ops() syncOps {
	var fingerprint func(context.Context) (string, error)
	if len(r.fingerprints) > 0 || len(r.fingerprintErrs) > 0 {
		fingerprint = func(context.Context) (string, error) {
			call := r.fingerprintCalls
			r.fingerprintCalls++
			if err := scriptedErr(r.fingerprintErrs, call); err != nil {
				return "", err
			}
			if len(r.fingerprints) == 0 {
				return "", nil
			}
			if call >= len(r.fingerprints) {
				return r.fingerprints[len(r.fingerprints)-1], nil
			}
			return r.fingerprints[call], nil
		}
	}
	var blockers func(context.Context) (storage.MergeBlockers, error)
	if len(r.mergeBlockers) > 0 || len(r.mergeBlockersErrs) > 0 {
		blockers = func(context.Context) (storage.MergeBlockers, error) {
			call := r.mergeBlockersCalls
			r.mergeBlockersCalls++
			if err := scriptedErr(r.mergeBlockersErrs, call); err != nil {
				return storage.MergeBlockers{}, err
			}
			if len(r.mergeBlockers) == 0 {
				return storage.MergeBlockers{}, nil
			}
			if call >= len(r.mergeBlockers) {
				return r.mergeBlockers[len(r.mergeBlockers)-1], nil
			}
			return r.mergeBlockers[call], nil
		}
	}
	return syncOps{
		dirtyFingerprint: fingerprint,
		mergeBlockers:    blockers,
		pull: func(context.Context) ([]string, error) {
			call := r.pulls
			r.pulls++
			var conflicts []string
			if len(r.pullConflicts) > 0 {
				if call >= len(r.pullConflicts) {
					conflicts = r.pullConflicts[len(r.pullConflicts)-1]
				} else {
					conflicts = r.pullConflicts[call]
				}
			}
			return conflicts, scriptedErr(r.pullErrs, call)
		},
		conflicts: func(context.Context) ([]string, error) {
			call := r.conflicts
			r.conflicts++
			if err := scriptedErr(r.conflictsErrs, call); err != nil {
				return nil, err
			}
			if len(r.conflictsSeq) == 0 {
				return nil, nil
			}
			if call >= len(r.conflictsSeq) {
				return r.conflictsSeq[len(r.conflictsSeq)-1], nil
			}
			return r.conflictsSeq[call], nil
		},
		recompute: func(context.Context) (int, error) {
			call := r.recomputes
			r.recomputes++
			if err := scriptedErr(r.recomputeErrs, call); err != nil {
				return 0, err
			}
			if len(r.recomputeVals) == 0 {
				return 0, nil
			}
			if call >= len(r.recomputeVals) {
				return r.recomputeVals[len(r.recomputeVals)-1], nil
			}
			return r.recomputeVals[call], nil
		},
		push: func(context.Context) error {
			defer func() { r.pushes++ }()
			return scriptedErr(r.pushErrs, r.pushes)
		},
	}
}

func raceErr() error { return errors.New("push rejected: remote is ahead (non-fast-forward)") }

func TestRunSyncLoopHappyPath(t *testing.T) {
	r := &syncOpsRecorder{recomputeVals: []int{4}}
	out, err := runSyncLoop(context.Background(), r.ops(), defaultSyncAttempts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Status != syncStatusOK {
		t.Fatalf("status = %q, want %q", out.Status, syncStatusOK)
	}
	if !out.Pushed {
		t.Error("Pushed = false, want true")
	}
	if out.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", out.Attempts)
	}
	if out.RowsCorrected != 4 {
		t.Errorf("RowsCorrected = %d, want 4", out.RowsCorrected)
	}
	if r.pulls != 1 || r.recomputes != 1 || r.pushes != 1 {
		t.Errorf("pulls/recomputes/pushes = %d/%d/%d, want 1/1/1", r.pulls, r.recomputes, r.pushes)
	}
	// Pre-flight check plus the post-pull check.
	if r.conflicts != 2 {
		t.Errorf("conflict checks = %d, want 2 (pre-flight + post-pull)", r.conflicts)
	}
}

// A conflict left live by an earlier halted sync must be reported as a conflict
// before anything is attempted — Dolt refuses to merge over it, so without the
// pre-flight the operator sees an opaque transport failure instead.
func TestRunSyncLoopPreflightConflictHaltsBeforePull(t *testing.T) {
	r := &syncOpsRecorder{conflictsSeq: [][]string{{"issues"}}}
	out, err := runSyncLoop(context.Background(), r.ops(), defaultSyncAttempts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Status != syncStatusConflict {
		t.Fatalf("status = %q, want %q", out.Status, syncStatusConflict)
	}
	if r.pulls != 0 || r.pushes != 0 || r.recomputes != 0 {
		t.Errorf("pulls/recomputes/pushes = %d/%d/%d, want 0/0/0", r.pulls, r.recomputes, r.pushes)
	}
	if len(out.Conflicts) != 1 || out.Conflicts[0] != "issues" {
		t.Errorf("Conflicts = %v, want [issues]", out.Conflicts)
	}
}

// The load-bearing property: a pull that reports SUCCESS can still have left
// conflicts behind. The loop must detect them positively from the conflict
// tables and halt without pushing.
func TestRunSyncLoopConflictDespiteSuccessfulPull(t *testing.T) {
	r := &syncOpsRecorder{
		pullErrs:     nil, // pull succeeds
		conflictsSeq: [][]string{nil, {"issues", "dependencies"}},
	}
	out, err := runSyncLoop(context.Background(), r.ops(), defaultSyncAttempts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Status != syncStatusConflict {
		t.Fatalf("status = %q, want %q", out.Status, syncStatusConflict)
	}
	if r.pushes != 0 {
		t.Errorf("pushes = %d, want 0 (a conflicted merge must never be pushed)", r.pushes)
	}
	if r.recomputes != 0 {
		t.Errorf("recomputes = %d, want 0 (halt before touching the graph)", r.recomputes)
	}
	if out.Pushed {
		t.Error("Pushed = true, want false")
	}
	if len(out.Conflicts) != 2 {
		t.Errorf("Conflicts = %v, want two tables", out.Conflicts)
	}
}

// The conflict source that dolt_conflicts alone cannot see: the settle pass
// aborts the conflicted merge and restores the working set, so by the time the
// pull returns, dolt_conflicts is empty again and the conflicts exist only in
// the error the merge handed back.
func TestRunSyncLoopMergeCapturedConflicts(t *testing.T) {
	r := &syncOpsRecorder{pullConflicts: [][]string{{"issues"}}}
	out, err := runSyncLoop(context.Background(), r.ops(), defaultSyncAttempts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Status != syncStatusConflict {
		t.Fatalf("status = %q, want %q", out.Status, syncStatusConflict)
	}
	if len(out.Conflicts) != 1 || out.Conflicts[0] != "issues" {
		t.Errorf("Conflicts = %v, want [issues]", out.Conflicts)
	}
	if r.pushes != 0 || r.recomputes != 0 {
		t.Errorf("recomputes/pushes = %d/%d, want 0/0", r.recomputes, r.pushes)
	}
}

func TestRunSyncLoopUnionsCapturedAndLiveConflicts(t *testing.T) {
	r := &syncOpsRecorder{
		pullConflicts: [][]string{{"issues"}},
		conflictsSeq:  [][]string{nil, {"dependencies", "issues"}},
	}
	out, err := runSyncLoop(context.Background(), r.ops(), defaultSyncAttempts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Status != syncStatusConflict {
		t.Fatalf("status = %q, want %q", out.Status, syncStatusConflict)
	}
	want := []string{"dependencies", "issues"}
	if len(out.Conflicts) != len(want) {
		t.Fatalf("Conflicts = %v, want %v", out.Conflicts, want)
	}
	for i := range want {
		if out.Conflicts[i] != want[i] {
			t.Fatalf("Conflicts = %v, want %v", out.Conflicts, want)
		}
	}
}

// The mirror image: a pull that reports FAILURE where the conflict tables are
// populated is a conflict (exit 2), not a generic transport error (exit 1).
func TestRunSyncLoopConflictWinsOverPullError(t *testing.T) {
	r := &syncOpsRecorder{
		pullErrs:     []error{errors.New("merge aborted")},
		conflictsSeq: [][]string{nil, {"issues"}},
	}
	out, err := runSyncLoop(context.Background(), r.ops(), defaultSyncAttempts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Status != syncStatusConflict {
		t.Fatalf("status = %q, want %q", out.Status, syncStatusConflict)
	}
	if r.pushes != 0 {
		t.Errorf("pushes = %d, want 0", r.pushes)
	}
}

// wy-j6q2z finding 7: on a shared sql-server, a live conflict left by ANOTHER
// writer can coincide with THIS run's own pull failing for an unrelated
// reason (transport, auth). classifyPullError only extracts table names from
// a *versioncontrolops.MergeConflictsError, so an unrelated error leaves
// `merged` empty — the conflict branch still fires (from the live check), and
// the unrelated error must not vanish silently.
func TestRunSyncLoopDiscardedPullErrorOnLiveConflict(t *testing.T) {
	r := &syncOpsRecorder{
		pullErrs:     []error{errors.New("dial tcp: connection refused")},
		conflictsSeq: [][]string{nil, {"issues"}},
	}
	out, err := runSyncLoop(context.Background(), r.ops(), defaultSyncAttempts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Status != syncStatusConflict {
		t.Fatalf("status = %q, want %q", out.Status, syncStatusConflict)
	}
	if out.DiscardedPullError != "dial tcp: connection refused" {
		t.Errorf("DiscardedPullError = %q, want the unrelated pull error preserved", out.DiscardedPullError)
	}
}

// The companion case: when the pull error IS what the merge captured (merged
// non-empty), it is already fully described by out.Conflicts — recording it a
// second time as "discarded" would be misleading, since nothing was actually
// dropped.
func TestRunSyncLoopNoDiscardedPullErrorWhenPullCapturedTheConflict(t *testing.T) {
	r := &syncOpsRecorder{
		pullConflicts: [][]string{{"issues"}},
		pullErrs:      []error{errors.New("merge conflict in issues")},
	}
	out, err := runSyncLoop(context.Background(), r.ops(), defaultSyncAttempts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Status != syncStatusConflict {
		t.Fatalf("status = %q, want %q", out.Status, syncStatusConflict)
	}
	if out.DiscardedPullError != "" {
		t.Errorf("DiscardedPullError = %q, want empty when the pull's own error described the conflict", out.DiscardedPullError)
	}
}

// A pull failure with no conflicts is a plain error (exit 1) and must not be
// retried into the push-race budget.
func TestRunSyncLoopPullErrorWithoutConflicts(t *testing.T) {
	r := &syncOpsRecorder{pullErrs: []error{errors.New("dial tcp: connection refused")}}
	out, err := runSyncLoop(context.Background(), r.ops(), defaultSyncAttempts)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "pull:") {
		t.Errorf("error = %v, want it to name the pull step", err)
	}
	if out.Status == syncStatusConflict {
		t.Error("a transport failure must not be reported as a conflict")
	}
	if r.pulls != 1 {
		t.Errorf("pulls = %d, want 1 (no retry on a non-race failure)", r.pulls)
	}
}

// A conflict-check failure on a successful pull must surface as an error rather
// than being read as "no conflicts" and pushing anyway.
func TestRunSyncLoopConflictCheckErrorIsFatal(t *testing.T) {
	r := &syncOpsRecorder{conflictsErrs: []error{nil, errors.New("dolt_conflicts unavailable")}}
	_, err := runSyncLoop(context.Background(), r.ops(), defaultSyncAttempts)
	if err == nil {
		t.Fatal("expected an error")
	}
	if r.pushes != 0 {
		t.Errorf("pushes = %d, want 0 (never push on an unknown conflict state)", r.pushes)
	}
}

func TestRunSyncLoopPreflightConflictCheckErrorIsFatal(t *testing.T) {
	r := &syncOpsRecorder{conflictsErrs: []error{errors.New("dolt_conflicts unavailable")}}
	_, err := runSyncLoop(context.Background(), r.ops(), defaultSyncAttempts)
	if err == nil {
		t.Fatal("expected an error")
	}
	if r.pulls != 0 {
		t.Errorf("pulls = %d, want 0", r.pulls)
	}
}

func TestRunSyncLoopRecomputeErrorHaltsBeforePush(t *testing.T) {
	r := &syncOpsRecorder{recomputeErrs: []error{errors.New("recompute failed")}}
	_, err := runSyncLoop(context.Background(), r.ops(), defaultSyncAttempts)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "recompute-blocked") {
		t.Errorf("error = %v, want it to name the recompute step", err)
	}
	if r.pushes != 0 {
		t.Errorf("pushes = %d, want 0 (a stale is_blocked must not be published)", r.pushes)
	}
}

// A push race re-enters the loop at the pull so the retry merges the commits
// that beat us, and converges.
func TestRunSyncLoopPushRaceRetriesAndSucceeds(t *testing.T) {
	r := &syncOpsRecorder{
		pushErrs:      []error{raceErr(), raceErr(), nil},
		recomputeVals: []int{1, 2, 0},
	}
	out, err := runSyncLoop(context.Background(), r.ops(), defaultSyncAttempts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Status != syncStatusOK {
		t.Fatalf("status = %q, want %q", out.Status, syncStatusOK)
	}
	if out.Attempts != 3 {
		t.Errorf("Attempts = %d, want 3", out.Attempts)
	}
	if r.pulls != 3 {
		t.Errorf("pulls = %d, want 3 (each retry must re-pull)", r.pulls)
	}
	if r.recomputes != 3 {
		t.Errorf("recomputes = %d, want 3", r.recomputes)
	}
	if out.RowsCorrected != 3 {
		t.Errorf("RowsCorrected = %d, want 3 (accumulated across attempts)", out.RowsCorrected)
	}
	if out.LastPushError != "" {
		t.Errorf("LastPushError = %q, want empty on success", out.LastPushError)
	}
}

func TestRunSyncLoopRetriesExhausted(t *testing.T) {
	r := &syncOpsRecorder{pushErrs: []error{raceErr()}}
	out, err := runSyncLoop(context.Background(), r.ops(), 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Status != syncStatusRetriesExhausted {
		t.Fatalf("status = %q, want %q", out.Status, syncStatusRetriesExhausted)
	}
	if out.Attempts != 2 {
		t.Errorf("Attempts = %d, want 2", out.Attempts)
	}
	if r.pushes != 2 {
		t.Errorf("pushes = %d, want 2 (bounded by --attempts)", r.pushes)
	}
	if out.Pushed {
		t.Error("Pushed = true, want false")
	}
	if out.LastPushError == "" {
		t.Error("LastPushError is empty, want the last race error recorded")
	}
}

// A push failure that is not a race can never converge by retrying, so it must
// exit immediately rather than burning the attempt budget.
func TestRunSyncLoopNonRacePushErrorDoesNotRetry(t *testing.T) {
	r := &syncOpsRecorder{pushErrs: []error{errors.New("permission denied")}}
	_, err := runSyncLoop(context.Background(), r.ops(), defaultSyncAttempts)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "push:") {
		t.Errorf("error = %v, want it to name the push step", err)
	}
	if r.pushes != 1 {
		t.Errorf("pushes = %d, want 1", r.pushes)
	}
}

func TestRunSyncLoopMaxAttemptsFloor(t *testing.T) {
	r := &syncOpsRecorder{pushErrs: []error{raceErr()}}
	out, err := runSyncLoop(context.Background(), r.ops(), 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Attempts != 1 || r.pushes != 1 {
		t.Errorf("attempts/pushes = %d/%d, want 1/1 (a non-positive bound floors at one attempt)", out.Attempts, r.pushes)
	}
	if out.Status != syncStatusRetriesExhausted {
		t.Errorf("status = %q, want %q", out.Status, syncStatusRetriesExhausted)
	}
}

func TestIsPushRaceErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		// The two routes a real Dolt push race travels. The SQL procedure says
		// the branch is behind its remote counterpart; the CLI route folds
		// git's non-fast-forward rejection into the error text.
		{"behind", errors.New("local branch is behind remote"), true},
		{"behind its remote counterpart", errors.New("hint: Updates were rejected because the tip of your current branch is behind its remote counterpart"), true},
		{"hyphenated fast-forward", errors.New("push failed: non-fast-forward update"), true},
		{"spaced fast forward", errors.New("updates were not fast forward"), true},
		{"fastforward", errors.New("not a fastforward push"), true},
		{"uppercase", errors.New("PUSH FAILED: NON-FAST-FORWARD"), true},
		// The git-blobstore layer behind git+* remotes pushes with
		// --force-with-lease; a lost lease reads as none of the above.
		{"stale info", errors.New("! [rejected] main -> main (stale info)"), true},
		{"fetch first", errors.New("! [rejected] main -> main (fetch first)"), true},
		{"remote contains work", errors.New("the remote contains work that you do not have locally"), true},
		// A bare "rejected" is deliberately NOT a race. A protected branch or a
		// declining pre-receive hook rejects permanently: classifying that as
		// retryable makes a sync timer burn its whole attempt budget every tick
		// and report exit 3 ("transient, retry next tick") forever, so the
		// failure never surfaces as the error it is.
		{"bare rejection", errors.New("push to remote rejected"), false},
		{"protected branch", errors.New("remote rejected: refs/heads/main is a protected branch"), false},
		{"pre-receive hook", errors.New("push rejected by pre-receive hook"), false},
		{"unrelated failure", errors.New("dial tcp: connection refused"), false},
		{"auth failure", errors.New("permission denied"), false},
		// Hard divergence must never be treated as retryable: retrying can
		// never converge, and it would eat the whole attempt budget before
		// surfacing the guidance the operator actually needs.
		{"no common ancestor", errors.New("merge failed: no common ancestor"), false},
		{"cannot find common ancestor", errors.New("cannot find common ancestor for merge"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPushRaceErr(tt.err); got != tt.want {
				t.Errorf("isPushRaceErr(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// A wrapped divergence error must still be excluded: the loop wraps nothing
// itself, but the store layer does.
func TestIsPushRaceErrWrappedDivergence(t *testing.T) {
	err := fmt.Errorf("push to remote: %w", errors.New("no common ancestor"))
	if isPushRaceErr(err) {
		t.Error("wrapped divergence classified as a retryable race")
	}
}

// The no-remote hint is deliberately broader than isRemoteNotFoundErr — a
// default-remote fetch on a rig that never configured one fails with Dolt's
// bare "Error 1105: no remote", which the "remote ... not found" phrasing
// misses, and sync runs on a timer. What must NOT widen with it is anything
// describing a remote that exists and is broken.
func TestIsNoRemoteConfiguredErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"dolt bare no remote", errors.New("fetch from origin/main: Error 1105: no remote"), true},
		{"remote not found", errors.New(`remote "mini" not found`), true},
		{"not found, remote after", errors.New("not found: remote origin"), true},
		{"uppercase", errors.New("Error 1105: NO REMOTE"), true},
		// A configured-but-broken remote is a real failure and must keep its
		// non-zero exit — the structural confirmation is a second gate, but the
		// hint should not invite it here either.
		{"repo missing on the remote side", errors.New("remote repository does not exist"), false},
		{"auth", errors.New("permission denied"), false},
		{"transport", errors.New("dial tcp: connection refused"), false},
		{"branch missing", errors.New("branch main not found on remote-tracking ref"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNoRemoteConfiguredErr(tt.err); got != tt.want {
				t.Errorf("isNoRemoteConfiguredErr(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// Every error the narrower dolt-verb classifier accepts must still be accepted
// by sync's, or sync would exit non-zero where `bd dolt pull` exits 0.
// TestIsNoRemoteConfiguredErrSupersetOfRemoteNotFound checks the widening
// isNoRemoteConfiguredErr's doc comment promises: every "remote not found"
// wording it delegates to isRemoteNotFoundErr is also accepted directly, AND
// the bare "no remote" wording isRemoteNotFoundErr rejects is still accepted.
// The original version of this test asserted
// `isRemoteNotFoundErr(err) && !isNoRemoteConfiguredErr(err)`, which is
// unsatisfiable by construction — isNoRemoteConfiguredErr's implementation is
// literally `isRemoteNotFoundErr(err) || ...` — so it passed vacuously
// regardless of either function's behavior.
func TestIsNoRemoteConfiguredErrSupersetOfRemoteNotFound(t *testing.T) {
	for _, msg := range []string{
		`remote "origin" not found`,
		"REMOTE NOT FOUND",
		"not found: remote origin",
	} {
		err := errors.New(msg)
		if !isRemoteNotFoundErr(err) {
			t.Fatalf("test fixture %q is not accepted by isRemoteNotFoundErr; fix the fixture", msg)
		}
		if !isNoRemoteConfiguredErr(err) {
			t.Errorf("%q: accepted by isRemoteNotFoundErr but rejected by isNoRemoteConfiguredErr", msg)
		}
	}

	// The widening is not vacuous: Dolt's bare "no remote" phrasing is exactly
	// what isRemoteNotFoundErr misses (it requires both "remote" and "not
	// found"), and it is the whole reason isNoRemoteConfiguredErr exists.
	bareNoRemote := "Error 1105: no remote"
	err := errors.New(bareNoRemote)
	if isRemoteNotFoundErr(err) {
		t.Fatalf("test fixture %q is unexpectedly accepted by isRemoteNotFoundErr; fix the fixture", bareNoRemote)
	}
	if !isNoRemoteConfiguredErr(err) {
		t.Errorf("%q: bare no-remote wording should be accepted by isNoRemoteConfiguredErr even though isRemoteNotFoundErr rejects it", bareNoRemote)
	}
}

func TestClassifyPullError(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		tables, err := classifyPullError(nil)
		if err != nil || tables != nil {
			t.Errorf("classifyPullError(nil) = %v, %v; want nil, nil", tables, err)
		}
	})
	t.Run("unrelated error passes through", func(t *testing.T) {
		in := errors.New("dial tcp: connection refused")
		tables, err := classifyPullError(in)
		if tables != nil {
			t.Errorf("tables = %v, want nil", tables)
		}
		if !errors.Is(err, in) {
			t.Errorf("err = %v, want the original error", err)
		}
	})
	t.Run("merge conflicts are extracted, not surfaced as an error", func(t *testing.T) {
		mce := &versioncontrolops.MergeConflictsError{
			Conflicts: []storage.Conflict{{Field: "issues"}, {Field: "dependencies"}},
			MergeErr:  errors.New("merge failed"),
		}
		tables, err := classifyPullError(mce)
		if err != nil {
			t.Fatalf("err = %v, want nil (the conflict is an outcome, not a failure)", err)
		}
		want := []string{"dependencies", "issues"}
		if len(tables) != len(want) {
			t.Fatalf("tables = %v, want %v", tables, want)
		}
		for i := range want {
			if tables[i] != want[i] {
				t.Fatalf("tables = %v, want %v", tables, want)
			}
		}
	})
	t.Run("wrapped merge conflicts are still extracted", func(t *testing.T) {
		mce := &versioncontrolops.MergeConflictsError{Conflicts: []storage.Conflict{{Field: "issues"}}}
		tables, err := classifyPullError(fmt.Errorf("pull from origin: %w", mce))
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if len(tables) != 1 || tables[0] != "issues" {
			t.Errorf("tables = %v, want [issues]", tables)
		}
	})
}

func TestUnionTables(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want []string
	}{
		{"both empty", nil, nil, nil},
		{"only a", []string{"issues"}, nil, []string{"issues"}},
		{"only b", nil, []string{"issues"}, []string{"issues"}},
		{"deduped and sorted", []string{"issues"}, []string{"issues", "dependencies"}, []string{"dependencies", "issues"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := unionTables(tt.a, tt.b)
			if len(got) != len(tt.want) {
				t.Fatalf("unionTables(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("unionTables(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
				}
			}
		})
	}
}

func TestConflictTables(t *testing.T) {
	got := conflictTables([]storage.Conflict{
		{Field: "issues"},
		{Field: "dependencies"},
		{Field: "issues"},
		{Field: ""},
	})
	want := []string{"(unknown)", "dependencies", "issues"}
	if len(got) != len(want) {
		t.Fatalf("conflictTables = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("conflictTables = %v, want %v", got, want)
		}
	}
	if n := len(conflictTables(nil)); n != 0 {
		t.Errorf("conflictTables(nil) returned %d entries, want 0", n)
	}
}

// The is_blocked repair must run on EVERY attempt, including a quiet tick that
// merged nothing. RecomputeAllBlocked is specifically the repair that does not
// depend on a merge advancing HEAD (bd-6dnrw.37) — it is what recovers a column
// left stale by a conflicted pull an operator resolved by hand, which is a state
// sync manufactures itself by exiting 2. Gating it on "did anything merge" would
// mean that repair never runs again while every tick reports success.
func TestRunSyncLoopRecomputesOnEveryAttempt(t *testing.T) {
	r := &syncOpsRecorder{
		pushErrs:      []error{raceErr(), nil},
		recomputeVals: []int{0, 0},
	}
	out, err := runSyncLoop(context.Background(), r.ops(), defaultSyncAttempts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Status != syncStatusOK {
		t.Fatalf("status = %q, want %q", out.Status, syncStatusOK)
	}
	// Two attempts, two recomputes — and the zero rows corrected on a quiet
	// tick must not be mistaken for a reason to have skipped it.
	if r.recomputes != 2 {
		t.Errorf("recomputes = %d, want 2 (one per attempt, unconditionally)", r.recomputes)
	}
	if out.RowsCorrected != 0 {
		t.Errorf("RowsCorrected = %d, want 0", out.RowsCorrected)
	}
}

// A push race followed by a conflicted retry: the run has already pulled and
// repaired, so the halt report must not describe itself as having touched
// nothing.
func TestRunSyncLoopConflictAfterPushRaceRecordsPulled(t *testing.T) {
	r := &syncOpsRecorder{
		pushErrs:     []error{raceErr()},
		conflictsSeq: [][]string{nil, nil, {"issues"}},
	}
	out, err := runSyncLoop(context.Background(), r.ops(), defaultSyncAttempts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Status != syncStatusConflict {
		t.Fatalf("status = %q, want %q", out.Status, syncStatusConflict)
	}
	if !out.Pulled {
		t.Error("Pulled = false, want true (attempt 1 pulled and recomputed before the race)")
	}
	if out.ConflictsPreexisting {
		t.Error("ConflictsPreexisting = true, want false (this run's retry produced the conflict)")
	}
	if r.pushes != 1 {
		t.Errorf("pushes = %d, want 1 (the conflicted retry must not push)", r.pushes)
	}
}

// Whether the conflicted merge was aborted or left live in the working set is
// read from WHICH detection source fired, never assumed: the SQL pull route
// aborts and restores, while the CLI/git-protocol route deliberately leaves the
// conflict rows in place for the operator (finishCLIPull).
func TestRunSyncLoopConflictLivenessComesFromTheSource(t *testing.T) {
	t.Run("captured-only conflicts were aborted away", func(t *testing.T) {
		r := &syncOpsRecorder{pullConflicts: [][]string{{"issues"}}}
		out, err := runSyncLoop(context.Background(), r.ops(), defaultSyncAttempts)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out.ConflictsLive {
			t.Error("ConflictsLive = true, want false (the settle pass aborted the merge)")
		}
	})

	t.Run("live rows mean the database is conflicted now", func(t *testing.T) {
		r := &syncOpsRecorder{conflictsSeq: [][]string{nil, {"issues"}}}
		out, err := runSyncLoop(context.Background(), r.ops(), defaultSyncAttempts)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !out.ConflictsLive {
			t.Error("ConflictsLive = false, want true (the rows are live in the working set)")
		}
	})

	t.Run("a pre-existing conflict is live by definition", func(t *testing.T) {
		r := &syncOpsRecorder{conflictsSeq: [][]string{{"issues"}}}
		out, err := runSyncLoop(context.Background(), r.ops(), defaultSyncAttempts)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !out.ConflictsLive || !out.ConflictsPreexisting {
			t.Errorf("ConflictsLive/Preexisting = %v/%v, want true/true", out.ConflictsLive, out.ConflictsPreexisting)
		}
	})
}

// The halt report is the operator's only signal about what state the replica is
// in, and the three conflict cases need opposite instructions.
func TestSyncConflictMessage(t *testing.T) {
	joined := func(out *syncOutcome) string {
		return strings.Join(syncConflictMessage(out), "\n")
	}

	t.Run("names the conflicted tables", func(t *testing.T) {
		got := joined(&syncOutcome{Conflicts: []string{"dependencies", "issues"}})
		for _, table := range []string{"dependencies", "issues"} {
			if !strings.Contains(got, table) {
				t.Errorf("message does not name conflicted table %q:\n%s", table, got)
			}
		}
		if !strings.Contains(got, "nothing pushed") {
			t.Errorf("message does not say nothing was pushed:\n%s", got)
		}
	})

	t.Run("aborted conflict says the working set was restored", func(t *testing.T) {
		got := joined(&syncOutcome{Conflicts: []string{"issues"}})
		if !strings.Contains(got, "working set restored") {
			t.Errorf("message does not report the restore:\n%s", got)
		}
		if strings.Contains(got, "ALREADY in a conflicted state") {
			t.Errorf("fresh conflict described as pre-existing:\n%s", got)
		}
	})

	// The CLI/git-protocol route leaves the conflict rows live. Telling that
	// operator "the working set was restored" sends them away from a database
	// that is conflicted right now.
	t.Run("live conflict is never described as restored", func(t *testing.T) {
		got := joined(&syncOutcome{Conflicts: []string{"issues"}, ConflictsLive: true})
		if !strings.Contains(got, "LIVE in the working set") {
			t.Errorf("message does not report the live rows:\n%s", got)
		}
		if strings.Contains(got, "working set restored") {
			t.Errorf("a live conflict must not be described as restored:\n%s", got)
		}
	})

	t.Run("pre-existing conflict says the replica was already conflicted", func(t *testing.T) {
		got := joined(&syncOutcome{Conflicts: []string{"issues"}, ConflictsPreexisting: true, ConflictsLive: true})
		if !strings.Contains(got, "ALREADY in a conflicted state") {
			t.Errorf("message does not report the pre-existing conflict:\n%s", got)
		}
		if strings.Contains(got, "working set restored") {
			t.Errorf("a live conflict must not be described as restored:\n%s", got)
		}
	})

	t.Run("a run that pulled says so; one that did not stays quiet", func(t *testing.T) {
		got := joined(&syncOutcome{Conflicts: []string{"issues"}, Pulled: true})
		if !strings.Contains(got, "completed its pull and is_blocked repair") {
			t.Errorf("message does not report the earlier attempt:\n%s", got)
		}
		if quiet := joined(&syncOutcome{Conflicts: []string{"issues"}}); strings.Contains(quiet, "earlier attempt") {
			t.Errorf("single-attempt halt mentions an earlier attempt:\n%s", quiet)
		}
	})

	// Every halt describes an operator action, but only a live conflict has one
	// that runs right now — "resolve the divergence" with no command named
	// left the operator to go find bd conflicts on their own.
	t.Run("live and pre-existing conflicts name a concrete resolve command", func(t *testing.T) {
		for _, out := range []*syncOutcome{
			{Conflicts: []string{"issues"}, ConflictsLive: true},
			{Conflicts: []string{"issues"}, ConflictsPreexisting: true, ConflictsLive: true},
		} {
			got := joined(out)
			if !strings.Contains(got, "bd conflicts resolve") {
				t.Errorf("message does not name a resolve command:\n%s", got)
			}
		}
	})

	// The aborted-merge case has nothing live for `bd conflicts` to show, so it
	// must not point at that command as if the working set were conflicted —
	// but it still must not leave the operator with zero next step.
	t.Run("aborted conflict names a next step without claiming a live conflict", func(t *testing.T) {
		got := joined(&syncOutcome{Conflicts: []string{"issues"}})
		if !strings.Contains(got, "bd conflicts list") {
			t.Errorf("message does not mention checking for live conflicts:\n%s", got)
		}
		if strings.Contains(got, "bd conflicts resolve") {
			t.Errorf("aborted-merge halt must not send the operator to resolve a conflict that is not live:\n%s", got)
		}
	})
}

func TestSyncCommandRegistered(t *testing.T) {
	var found bool
	for _, cmd := range rootCmd.Commands() {
		if cmd.Use == "sync" {
			found = true
			if cmd.GroupID != "sync" {
				t.Errorf("GroupID = %q, want %q", cmd.GroupID, "sync")
			}
			if f := cmd.Flags().Lookup("remote"); f == nil {
				t.Error("missing --remote flag")
			}
			f := cmd.Flags().Lookup("attempts")
			if f == nil {
				t.Fatal("missing --attempts flag")
			}
			if f.DefValue != fmt.Sprint(defaultSyncAttempts) {
				t.Errorf("--attempts default = %q, want %d", f.DefValue, defaultSyncAttempts)
			}
			break
		}
	}
	if !found {
		t.Fatal("sync command not registered with rootCmd")
	}
}

// The exit codes are the command's machine contract: a sync timer branches on
// them. Pin them so a refactor cannot renumber them silently.
func TestSyncExitCodesArePinned(t *testing.T) {
	if ExitSyncConflict != 2 {
		t.Errorf("ExitSyncConflict = %d, want 2", ExitSyncConflict)
	}
	if ExitSyncRetriesExhausted != 3 {
		t.Errorf("ExitSyncRetriesExhausted = %d, want 3", ExitSyncRetriesExhausted)
	}
	if ExitSyncDirtyStuck != 4 {
		t.Errorf("ExitSyncDirtyStuck = %d, want 4", ExitSyncDirtyStuck)
	}
}

// dirtyGraphErr is the guard's real error shape: the sentinel wrapped with the
// offending table names (issueops.GuardBlockedRecomputeWorkingSet).
func dirtyGraphErr() error {
	return fmt.Errorf("%w: commit or discard pending changes to %s first",
		issueops.ErrBlockedRecomputeDirtyGraph, "issues")
}

func TestIsRecomputeDirtyGraphErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unrelated", errors.New("connection refused"), false},
		// The message alone must never be the signal — a *different* error
		// that happens to talk about clean working sets is not the guard.
		{"lookalike message", errors.New("is_blocked recompute needs a clean working set"), false},
		{"bare sentinel", issueops.ErrBlockedRecomputeDirtyGraph, true},
		{"guard's wrapped form", dirtyGraphErr(), true},
		{"wrapped again by the loop's step framing", fmt.Errorf("recompute-blocked: %w", dirtyGraphErr()), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRecomputeDirtyGraphErr(tt.err); got != tt.want {
				t.Errorf("isRecomputeDirtyGraphErr(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// A concurrent writer's uncommitted edit is foreign and self-healing, so the
// repair re-enters the attempt loop instead of failing the run and stranding
// local commits unpushed (wy-mlnz2).
func TestRunSyncLoopDirtyGraphRecomputeRetriesAndSucceeds(t *testing.T) {
	r := &syncOpsRecorder{
		recomputeErrs: []error{dirtyGraphErr(), dirtyGraphErr(), nil},
		recomputeVals: []int{0, 0, 4},
	}
	out, err := runSyncLoop(context.Background(), r.ops(), defaultSyncAttempts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Status != syncStatusOK {
		t.Fatalf("status = %q, want %q", out.Status, syncStatusOK)
	}
	if out.Attempts != 3 {
		t.Errorf("Attempts = %d, want 3", out.Attempts)
	}
	if r.pulls != 3 || r.recomputes != 3 {
		t.Errorf("pulls/recomputes = %d/%d, want 3/3 (each retry re-enters at the pull)", r.pulls, r.recomputes)
	}
	if r.pushes != 1 {
		t.Errorf("pushes = %d, want 1 (a blocked repair must not publish)", r.pushes)
	}
	if !out.Pushed || out.RowsCorrected != 4 {
		t.Errorf("Pushed/RowsCorrected = %v/%d, want true/4", out.Pushed, out.RowsCorrected)
	}
	if out.LastRecomputeError != "" {
		t.Errorf("LastRecomputeError = %q, want it cleared once the repair succeeded", out.LastRecomputeError)
	}
}

// Exhausting the budget is the transient exit (3), not a hard error (1): the
// next tick is expected to find a clean working set.
func TestRunSyncLoopDirtyGraphRetriesExhausted(t *testing.T) {
	r := &syncOpsRecorder{recomputeErrs: []error{dirtyGraphErr()}}
	out, err := runSyncLoop(context.Background(), r.ops(), 2)
	if err != nil {
		t.Fatalf("unexpected error: %v (a dirty working set is transient, not an exit-1 failure)", err)
	}
	if out.Status != syncStatusRetriesExhausted {
		t.Fatalf("status = %q, want %q", out.Status, syncStatusRetriesExhausted)
	}
	if out.Attempts != 2 || r.recomputes != 2 {
		t.Errorf("attempts/recomputes = %d/%d, want 2/2 (bounded by --attempts)", out.Attempts, r.recomputes)
	}
	if r.pushes != 0 {
		t.Errorf("pushes = %d, want 0 (a stale is_blocked must not be published)", r.pushes)
	}
	if out.Pushed || out.Pulled {
		t.Errorf("Pushed/Pulled = %v/%v, want false/false (no attempt completed its repair)", out.Pushed, out.Pulled)
	}
	if out.LastRecomputeError == "" {
		t.Error("LastRecomputeError is empty, want the guard error recorded")
	}
}

// The positive escalation this bead adds (wy-mhouc): a graph table dirty
// because of a constraint violation no writer will ever commit is knowable on
// the FIRST blocked attempt, from storage.MergeBlockerInspector — it must not
// wait out syncStuckTicks the way the tick-count inference does.
func TestRunSyncLoopConstraintViolationEscalatesOnAttemptOne(t *testing.T) {
	r := &syncOpsRecorder{
		recomputeErrs: []error{dirtyGraphErr()},
		mergeBlockers: []storage.MergeBlockers{{
			ConstraintViolations: []storage.ConstraintViolation{{Table: "issues", Count: 3}},
		}},
	}
	out, err := runSyncLoop(context.Background(), r.ops(), defaultSyncAttempts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Status != syncStatusDirtyStuck {
		t.Fatalf("status = %q, want %q", out.Status, syncStatusDirtyStuck)
	}
	if out.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1 (escalated on the first blocked attempt)", out.Attempts)
	}
	if r.recomputes != 1 {
		t.Errorf("recomputes = %d, want 1 (no retry budget spent once escalated)", r.recomputes)
	}
	if r.pushes != 0 {
		t.Errorf("pushes = %d, want 0", r.pushes)
	}
	if len(out.ConstraintViolations) != 1 || out.ConstraintViolations[0].Table != "issues" || out.ConstraintViolations[0].Count != 3 {
		t.Errorf("ConstraintViolations = %+v, want [{issues 3}]", out.ConstraintViolations)
	}
}

// No constraint violations on the graph tables means the existing tick-count
// inference is unchanged: a single blocked attempt still just reports
// retries-exhausted, not an immediate escalation.
func TestRunSyncLoopNoConstraintViolationsLeavesTickInferenceUnchanged(t *testing.T) {
	r := &syncOpsRecorder{
		recomputeErrs: []error{dirtyGraphErr()},
		mergeBlockers: []storage.MergeBlockers{{}},
	}
	out, err := runSyncLoop(context.Background(), r.ops(), 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Status != syncStatusRetriesExhausted {
		t.Fatalf("status = %q, want %q (no violations, so no positive escalation)", out.Status, syncStatusRetriesExhausted)
	}
	if len(out.ConstraintViolations) != 0 {
		t.Errorf("ConstraintViolations = %+v, want none", out.ConstraintViolations)
	}
}

// A constraint violation on some OTHER table (not one of the graph tables the
// guard found dirty) must not escalate — it is evidence about a table this
// sync's repair does not even read.
func TestRunSyncLoopConstraintViolationOnUnrelatedTableDoesNotEscalate(t *testing.T) {
	r := &syncOpsRecorder{
		recomputeErrs: []error{dirtyGraphErr()},
		mergeBlockers: []storage.MergeBlockers{{
			ConstraintViolations: []storage.ConstraintViolation{{Table: "wisps", Count: 9}},
		}},
	}
	out, err := runSyncLoop(context.Background(), r.ops(), 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Status != syncStatusRetriesExhausted {
		t.Fatalf("status = %q, want %q (violation is on a non-graph table)", out.Status, syncStatusRetriesExhausted)
	}
	if len(out.ConstraintViolations) != 0 {
		t.Errorf("ConstraintViolations = %+v, want none", out.ConstraintViolations)
	}
}

// A blockers-probe failure must never escalate: unavailable evidence is not
// evidence of being stuck, exactly like the dirty-fingerprint hook's contract.
func TestRunSyncLoopMergeBlockersProbeFailureDoesNotEscalate(t *testing.T) {
	r := &syncOpsRecorder{
		recomputeErrs:     []error{dirtyGraphErr()},
		mergeBlockersErrs: []error{errors.New("connection refused")},
	}
	out, err := runSyncLoop(context.Background(), r.ops(), 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Status != syncStatusRetriesExhausted {
		t.Fatalf("status = %q, want %q (a probe failure must fall back to the transient exit)", out.Status, syncStatusRetriesExhausted)
	}
	if len(out.ConstraintViolations) != 0 {
		t.Errorf("ConstraintViolations = %+v, want none", out.ConstraintViolations)
	}
}

// A nil mergeBlockers hook (production shape for a store without
// MergeBlockerInspector) must behave exactly like production did before this
// bead: no escalation, ever.
func TestRunSyncLoopNilMergeBlockersHookDoesNotEscalate(t *testing.T) {
	r := &syncOpsRecorder{recomputeErrs: []error{dirtyGraphErr()}}
	out, err := runSyncLoop(context.Background(), r.ops(), 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Status != syncStatusRetriesExhausted {
		t.Fatalf("status = %q, want %q", out.Status, syncStatusRetriesExhausted)
	}
}

// A recompute failure that is NOT the dirty-graph guard can never converge by
// retrying, so it must still halt the run immediately.
func TestRunSyncLoopNonDirtyRecomputeErrorDoesNotRetry(t *testing.T) {
	r := &syncOpsRecorder{recomputeErrs: []error{errors.New("connection refused")}}
	_, err := runSyncLoop(context.Background(), r.ops(), defaultSyncAttempts)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "recompute-blocked") {
		t.Errorf("error = %v, want it to name the recompute step", err)
	}
	if r.recomputes != 1 {
		t.Errorf("recomputes = %d, want 1 (a durable failure must not burn the budget)", r.recomputes)
	}
}

// LastPushError and LastRecomputeError must not both survive: whichever the
// FINAL attempt failed on is what the exit-3 report is built from, and the
// other one describes an attempt that has since been superseded.
func TestRunSyncLoopRetryClearsTheOtherLastError(t *testing.T) {
	t.Run("dirty recompute clears an earlier push race", func(t *testing.T) {
		r := &syncOpsRecorder{
			pushErrs:      []error{raceErr()},
			recomputeErrs: []error{nil, dirtyGraphErr()},
		}
		out, err := runSyncLoop(context.Background(), r.ops(), 2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out.Status != syncStatusRetriesExhausted {
			t.Fatalf("status = %q, want %q", out.Status, syncStatusRetriesExhausted)
		}
		if out.LastPushError != "" {
			t.Errorf("LastPushError = %q, want it cleared by the later recompute failure", out.LastPushError)
		}
		if out.LastRecomputeError == "" {
			t.Error("LastRecomputeError is empty, want the final attempt's failure recorded")
		}
		// The first attempt did complete its repair, so the outcome still says so.
		if !out.Pulled {
			t.Error("Pulled = false, want true (attempt 1 completed pull + repair)")
		}
	})

	t.Run("push race clears an earlier dirty recompute", func(t *testing.T) {
		r := &syncOpsRecorder{
			pushErrs:      []error{raceErr()},
			recomputeErrs: []error{dirtyGraphErr(), nil},
		}
		out, err := runSyncLoop(context.Background(), r.ops(), 2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out.Status != syncStatusRetriesExhausted {
			t.Fatalf("status = %q, want %q", out.Status, syncStatusRetriesExhausted)
		}
		if out.LastRecomputeError != "" {
			t.Errorf("LastRecomputeError = %q, want it cleared once the repair succeeded", out.LastRecomputeError)
		}
		if out.LastPushError == "" {
			t.Error("LastPushError is empty, want the final attempt's failure recorded")
		}
	})
}

// Both exit-3 causes are transient, but they are blocked on different machines:
// a race is another REPLICA, a dirty working set is another writer on THIS one.
func TestSyncRetriesExhaustedMessage(t *testing.T) {
	joined := func(out *syncOutcome) string {
		return strings.Join(syncRetriesExhaustedMessage(out), "\n")
	}

	t.Run("push race", func(t *testing.T) {
		got := joined(&syncOutcome{Attempts: 3, LastPushError: "non-fast-forward"})
		if !strings.Contains(got, "push-race retries exhausted after 3 attempt(s)") {
			t.Errorf("message does not report the exhausted race:\n%s", got)
		}
		if !strings.Contains(got, "non-fast-forward") {
			t.Errorf("message does not quote the last push error:\n%s", got)
		}
		if strings.Contains(got, "dirty working set") {
			t.Errorf("a push race must not be described as a dirty working set:\n%s", got)
		}
	})

	t.Run("dirty working set", func(t *testing.T) {
		got := joined(&syncOutcome{Attempts: 3, LastRecomputeError: dirtyGraphErr().Error()})
		if !strings.Contains(got, "dirty working set") {
			t.Errorf("message does not report the dirty working set:\n%s", got)
		}
		if !strings.Contains(got, "uncommitted changes to issues/dependencies") {
			t.Errorf("message does not say who is blocking the repair:\n%s", got)
		}
		if !strings.Contains(got, "transient") || !strings.Contains(got, "Nothing was pushed") {
			t.Errorf("message does not report transience and that nothing shipped:\n%s", got)
		}
		// Sending this operator to look at another replica is the wrong machine.
		if strings.Contains(got, "another replica kept winning the race") {
			t.Errorf("dirty working set described as a push race:\n%s", got)
		}
	})
}

// --attempts 1 means "no retry budget": the dirty guard is still classified as
// retryable, but there is nowhere to retry to, so it must reach the transient
// exit rather than falling through to a hard error or a push.
func TestRunSyncLoopDirtyGraphSingleAttempt(t *testing.T) {
	r := &syncOpsRecorder{recomputeErrs: []error{dirtyGraphErr()}}
	out, err := runSyncLoop(context.Background(), r.ops(), 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Status != syncStatusRetriesExhausted {
		t.Fatalf("status = %q, want %q", out.Status, syncStatusRetriesExhausted)
	}
	if r.recomputes != 1 || r.pushes != 0 {
		t.Errorf("recomputes/pushes = %d/%d, want 1/0", r.recomputes, r.pushes)
	}
}

// A cancelled context must win over the retry budget on the dirty path too,
// so a ^C or a timer deadline is not swallowed by retries.
func TestRunSyncLoopDirtyGraphHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &syncOpsRecorder{}
	ops := r.ops()
	inner := ops.recompute
	ops.recompute = func(c context.Context) (int, error) {
		cancel()
		_, _ = inner(c)
		return 0, dirtyGraphErr()
	}
	out, err := runSyncLoop(ctx, ops, defaultSyncAttempts)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if out.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1 (cancellation must beat the retry budget)", out.Attempts)
	}
}

// A run whose pull merged but whose repair was blocked has moved local history
// while leaving out.Pulled false. The halt report must not tell that operator
// the run touched nothing.
func TestSyncConflictMessageReportsABlockedRepair(t *testing.T) {
	got := strings.Join(syncConflictMessage(&syncOutcome{
		Conflicts:          []string{"issues"},
		LastRecomputeError: dirtyGraphErr().Error(),
	}), "\n")
	if !strings.Contains(got, "blocked by a dirty working set") {
		t.Errorf("message does not report the blocked repair:\n%s", got)
	}
	if !strings.Contains(got, "is NOT repaired") {
		t.Errorf("message does not warn that is_blocked was left unrepaired:\n%s", got)
	}
	quiet := strings.Join(syncConflictMessage(&syncOutcome{Conflicts: []string{"issues"}}), "\n")
	if strings.Contains(quiet, "blocked by a dirty working set") {
		t.Errorf("a clean single-attempt halt mentions a blocked repair:\n%s", quiet)
	}
}

// wy-j6q2z finding 7: a conflict halt that also masked an unrelated pull
// error must say so, naming the error text, so the operator does not assume
// resolving the conflict is the whole fix.
func TestSyncConflictMessageReportsADiscardedPullError(t *testing.T) {
	got := strings.Join(syncConflictMessage(&syncOutcome{
		Conflicts:          []string{"issues"},
		ConflictsLive:      true,
		DiscardedPullError: "dial tcp: connection refused",
	}), "\n")
	if !strings.Contains(got, "dial tcp: connection refused") {
		t.Errorf("message does not name the discarded pull error:\n%s", got)
	}
	quiet := strings.Join(syncConflictMessage(&syncOutcome{Conflicts: []string{"issues"}, ConflictsLive: true}), "\n")
	if strings.Contains(quiet, "pull error:") {
		t.Errorf("a halt with no discarded pull error must not mention one:\n%s", quiet)
	}
}

// fakeSyncStore is a minimal storage.DoltStorage for driving runSyncCommand
// end to end without a live Dolt server. Embedding storage.DoltStorage means
// every unoverridden method panics if called, so a test that reaches one is a
// test exercising a path it did not mean to.
type fakeSyncStore struct {
	storage.DoltStorage

	pullErr       error
	pullRemoteErr error
	pushErr       error
	pushRemoteErr error
	conflicts     []storage.Conflict
	conflictsErr  error
	recomputed    int
	recomputeErr  error
	remotes       []storage.RemoteInfo

	pullCalls       int
	pullRemoteCalls int
	pushCalls       int
	pushRemoteCalls int
	lastRemoteArg   string
}

func (f *fakeSyncStore) Pull(context.Context) error { f.pullCalls++; return f.pullErr }
func (f *fakeSyncStore) PullRemote(_ context.Context, remote string) error {
	f.pullRemoteCalls++
	f.lastRemoteArg = remote
	return f.pullRemoteErr
}
func (f *fakeSyncStore) Push(context.Context) error { f.pushCalls++; return f.pushErr }
func (f *fakeSyncStore) PushRemote(_ context.Context, remote string, _ bool) error {
	f.pushRemoteCalls++
	f.lastRemoteArg = remote
	return f.pushRemoteErr
}
func (f *fakeSyncStore) GetConflicts(context.Context) ([]storage.Conflict, error) {
	return f.conflicts, f.conflictsErr
}
func (f *fakeSyncStore) RecomputeAllBlocked(context.Context) (int, error) {
	return f.recomputed, f.recomputeErr
}
func (f *fakeSyncStore) ListRemotes(context.Context) ([]storage.RemoteInfo, error) {
	return f.remotes, nil
}

// setupSyncCommandTest wires the fake store, resets the sync command's flags
// and the globals runSyncCommand reads directly (rootCtx, jsonOutput), and
// restores everything on cleanup. Cannot be parallel: modifies process
// globals.
//
// It also stubs syncAdoptGitOrigin to the "nothing to adopt" answer. That is
// the default every pre-existing case here means (a solo rig with no git origin
// to adopt), and it is load-bearing safety: the real adoption resolves the
// active workspace, shells out to git, and can write and COMMIT
// .beads/config.yaml, so leaving it live would let a unit test mutate whatever
// repo the suite is run from. A case that is about adoption reassigns the seam
// after calling this.
func setupSyncCommandTest(t *testing.T, fake *fakeSyncStore) {
	t.Helper()
	saveAndRestoreGlobals(t)
	resetCommandContext()

	oldJSON := jsonOutput
	oldCtx := rootCtx
	oldQuiet := quietFlag
	oldAdopt := syncAdoptGitOrigin
	t.Cleanup(func() {
		jsonOutput = oldJSON
		rootCtx = oldCtx
		quietFlag = oldQuiet
		syncAdoptGitOrigin = oldAdopt
		_ = syncCmd.Flags().Set("attempts", fmt.Sprintf("%d", defaultSyncAttempts))
		_ = syncCmd.Flags().Set("remote", "")
	})
	syncAdoptGitOrigin = func(context.Context, storage.DoltStorage, adoptPolicy, adoptOptIn) (bool, error) { return false, nil }

	store = fake
	rootCtx = context.Background()
	jsonOutput = false
	quietFlag = false

	config.ResetForTesting()
	t.Cleanup(func() { config.ResetForTesting() })
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
}

// TestRunSyncCommandNoRemoteExitsZero covers the benign solo-rig path
// (wy-xtv17): a default-remote pull failing with Dolt's "no remote"
// wording, on a rig hasNoRemoteConfigured can positively confirm has none,
// must exit 0 rather than fail every tick of a timer.
func TestRunSyncCommandNoRemoteExitsZero(t *testing.T) {
	fake := &fakeSyncStore{
		pullErr: errors.New(`Error 1105: no remote`),
		remotes: nil,
	}
	setupSyncCommandTest(t, fake)

	err := runSyncCommand(syncCmd, nil)
	if err != nil {
		t.Fatalf("runSyncCommand() error = %v, want nil (confirmed no-remote must exit 0)", err)
	}
	if fake.pushCalls != 0 {
		t.Errorf("push must not be attempted when the pull never got past the no-remote failure, got %d calls", fake.pushCalls)
	}
}

// TestRunSyncCommandNoRemoteJSON covers the JSON envelope for the no-remote
// path specifically: status must be "no-remote", not the zero-value "ok".
func TestRunSyncCommandNoRemoteJSON(t *testing.T) {
	fake := &fakeSyncStore{pullErr: errors.New(`Error 1105: no remote`)}
	setupSyncCommandTest(t, fake)
	jsonOutput = true

	out := captureStdout(t, func() error { return runSyncCommand(syncCmd, nil) })
	var got syncOutcome
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("json.Unmarshal(%q): %v", out, err)
	}
	if got.Status != syncStatusNoRemote {
		t.Errorf("Status = %q, want %q", got.Status, syncStatusNoRemote)
	}
}

// TestRunSyncCommandNoPushSetsPushSkipped covers the noPush -> PushSkipped
// correction: a successful ok-status run with BD_NO_PUSH=true must not call
// Push, and the outcome the operator sees must say so (PushSkipped=true,
// Pushed=false) rather than silently reporting an ordinary successful push.
func TestRunSyncCommandNoPushSetsPushSkipped(t *testing.T) {
	fake := &fakeSyncStore{recomputed: 2}
	setupSyncCommandTest(t, fake)
	t.Setenv("BD_NO_PUSH", "true")
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	if !config.GetBool("no-push") {
		t.Fatal("test setup: BD_NO_PUSH=true must make no-push=true")
	}
	jsonOutput = true

	out := captureStdout(t, func() error { return runSyncCommand(syncCmd, nil) })
	if fake.pushCalls != 0 {
		t.Errorf("Push() must not be called under no-push: true; called %d time(s)", fake.pushCalls)
	}
	var got syncOutcome
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("json.Unmarshal(%q): %v", out, err)
	}
	if got.Status != syncStatusOK {
		t.Errorf("Status = %q, want %q", got.Status, syncStatusOK)
	}
	if got.Pushed {
		t.Error("Pushed = true under no-push: true, want false")
	}
	if !got.PushSkipped {
		t.Error("PushSkipped = false under no-push: true, want true")
	}
}

// TestRunSyncCommandAttemptsZeroRejected covers --attempts validation:
// runSyncCommand must refuse to run the loop at all on a non-positive
// budget, rather than silently flooring it (the loop itself floors <1 to 1,
// but the command's own validation is what actually surfaces the mistake to
// the operator instead of quietly running once).
func TestRunSyncCommandAttemptsZeroRejected(t *testing.T) {
	fake := &fakeSyncStore{}
	setupSyncCommandTest(t, fake)
	if err := syncCmd.Flags().Set("attempts", "0"); err != nil {
		t.Fatalf("Flags().Set(attempts, 0): %v", err)
	}

	err := runSyncCommand(syncCmd, nil)
	if err == nil {
		t.Fatal("runSyncCommand() error = nil, want a rejection of --attempts 0")
	}
	if code, ok := exitCodeFromError(err); !ok || code != 1 {
		t.Errorf("exitCodeFromError(err) = (%d, %v), want (1, true)", code, ok)
	}
	if fake.pullCalls != 0 {
		t.Errorf("pull must not run when --attempts is rejected, got %d call(s)", fake.pullCalls)
	}
}

// TestRunSyncCommandExitCodeMapping covers the exitError the command layer
// derives from each terminal syncOutcome.Status — the mapping a sync timer
// actually branches on (see the exit-code table in syncCmd's Long text).
func TestRunSyncCommandExitCodeMapping(t *testing.T) {
	tests := []struct {
		name     string
		fake     *fakeSyncStore
		wantCode int
		wantErr  bool
	}{
		{
			name:    "ok",
			fake:    &fakeSyncStore{},
			wantErr: false,
		},
		{
			name:     "conflict",
			fake:     &fakeSyncStore{conflicts: []storage.Conflict{{Field: "issues"}}},
			wantCode: ExitSyncConflict,
			wantErr:  true,
		},
		{
			name:     "retries-exhausted",
			fake:     &fakeSyncStore{pushErr: errors.New("local branch is behind remote")},
			wantCode: ExitSyncRetriesExhausted,
			wantErr:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupSyncCommandTest(t, tt.fake)
			jsonOutput = true // silence the operator-facing stderr report; the exit code is what's under test

			err := runSyncCommand(syncCmd, nil)
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("runSyncCommand() error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("runSyncCommand() error = nil, want non-nil")
			}
			code, ok := exitCodeFromError(err)
			if !ok {
				t.Fatalf("exitCodeFromError(%v) ok = false, want true", err)
			}
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
		})
	}
}

// TestRunSyncCommandJSONEnvelopePerStatus covers the --json outcome for each
// terminal status: the field values a sync timer parses (status, pushed,
// push_skipped) must match what actually happened, not just the exit code.
func TestRunSyncCommandJSONEnvelopePerStatus(t *testing.T) {
	tests := []struct {
		name       string
		fake       *fakeSyncStore
		wantStatus string
		wantPushed bool
	}{
		{
			name:       "ok",
			fake:       &fakeSyncStore{recomputed: 3},
			wantStatus: syncStatusOK,
			wantPushed: true,
		},
		{
			name:       "conflict",
			fake:       &fakeSyncStore{conflicts: []storage.Conflict{{Field: "issues"}}},
			wantStatus: syncStatusConflict,
			wantPushed: false,
		},
		{
			name:       "retries-exhausted",
			fake:       &fakeSyncStore{pushErr: errors.New("local branch is behind remote")},
			wantStatus: syncStatusRetriesExhausted,
			wantPushed: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupSyncCommandTest(t, tt.fake)
			jsonOutput = true

			out := captureStdout(t, func() error {
				_ = runSyncCommand(syncCmd, nil)
				return nil // the command's own error is asserted elsewhere; capture must not fail on the expected non-nil ones
			})
			var got syncOutcome
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatalf("json.Unmarshal(%q): %v", out, err)
			}
			if got.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, tt.wantStatus)
			}
			if got.Pushed != tt.wantPushed {
				t.Errorf("Pushed = %v, want %v", got.Pushed, tt.wantPushed)
			}
		})
	}
}

// TestRunSyncCommandQuietSilencesSuccessOutput covers the -q asymmetry: a
// successful sync must print nothing when quiet, matching the per-step
// progress lines which were already gated on isQuiet(). Before this fix,
// "Sync complete." printed unconditionally regardless of -q.
func TestRunSyncCommandQuietSilencesSuccessOutput(t *testing.T) {
	fake := &fakeSyncStore{recomputed: 2}
	setupSyncCommandTest(t, fake)
	quietFlag = true

	out := captureStdout(t, func() error { return runSyncCommand(syncCmd, nil) })
	if out != "" {
		t.Errorf("stdout = %q, want empty under -q", out)
	}
}

// TestRunSyncCommandQuietSilencesNoRemoteGuidance covers the same asymmetry
// for the benign no-remote path: printNoRemoteGuidance's ~15 lines of
// onboarding text is exactly the non-essential output -q exists to silence
// on a solo rig's unattended timer.
func TestRunSyncCommandQuietSilencesNoRemoteGuidance(t *testing.T) {
	fake := &fakeSyncStore{pullErr: errors.New("Error 1105: no remote")}
	setupSyncCommandTest(t, fake)
	quietFlag = true

	out := captureStdout(t, func() error { return runSyncCommand(syncCmd, nil) })
	if out != "" {
		t.Errorf("stdout = %q, want empty under -q", out)
	}
}

// TestRunSyncCommandQuietDoesNotSilenceConflict covers the other half of the
// asymmetry: -q means "errors only", and a merge conflict IS the error, so it
// must still be reported even under -q (unlike the success-path prints
// above).
func TestRunSyncCommandQuietDoesNotSilenceConflict(t *testing.T) {
	fake := &fakeSyncStore{conflicts: []storage.Conflict{{Field: "issues"}}}
	setupSyncCommandTest(t, fake)
	quietFlag = true

	// captureStdout and captureStderr share one non-reentrant mutex, so they
	// cannot nest; the conflict report goes to stderr only (see
	// printSyncOutcome), so that is the one stream under test here.
	stderr := captureStderr(t, func() { _ = runSyncCommand(syncCmd, nil) })
	if !strings.Contains(stderr, "merge conflict") {
		t.Errorf("stderr = %q, want a conflict report even under -q", stderr)
	}
}

// ---------------------------------------------------------------------------
// wy-gpzg7: sync's default-remote path adopts a git origin as the Dolt remote,
// the same way `bd dolt push` does.
// ---------------------------------------------------------------------------

// The bug: a first-time federation rig (git origin configured, no Dolt remote
// registered yet) that runs `bd sync` as its bring-up step used to get a silent
// no-op — pull fails with Dolt's bare no-remote wording, the confirmed-no-remote
// gate agrees because nothing ever adopted the origin, status=no-remote, exit 0
// — where `bd dolt push` on the very same rig would have adopted origin and
// pushed. Adoption must run BEFORE the loop, so the pull benefits from it too.
func TestRunSyncCommandAdoptsGitOriginOnDefaultRemote(t *testing.T) {
	fake := &fakeSyncStore{pullErr: errors.New("Error 1105: no remote"), recomputed: 2}
	setupSyncCommandTest(t, fake)
	adoptCalls := 0
	syncAdoptGitOrigin = func(context.Context, storage.DoltStorage, adoptPolicy, adoptOptIn) (bool, error) {
		adoptCalls++
		// What adoption does on a real first-time rig: the remote now exists,
		// so the pull that was failing with "no remote" succeeds and
		// dolt_remotes lists it.
		fake.pullErr = nil
		fake.remotes = []storage.RemoteInfo{{Name: "origin"}}
		return true, nil
	}

	jsonOutput = true
	out := captureStdout(t, func() error { return runSyncCommand(syncCmd, nil) })

	if adoptCalls != 1 {
		t.Fatalf("adoption ran %d time(s), want exactly 1 on the default-remote path", adoptCalls)
	}
	var got syncOutcome
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("json.Unmarshal(%q): %v", out, err)
	}
	if got.Status != syncStatusOK {
		t.Errorf("Status = %q, want %q — adoption before the loop is what makes the pull work", got.Status, syncStatusOK)
	}
	if fake.pushCalls != 1 {
		t.Errorf("Push() called %d time(s), want 1: an adopted rig must actually publish, not report no-remote", fake.pushCalls)
	}
}

// Adoption must precede the PULL, not merely the push: a rig that had to adopt
// its remote has nothing to pull from until it has one, so an adoption wired in
// front of the push only (mirroring `bd dolt push` too literally) would still
// leave sync failing on its first step.
func TestRunSyncCommandAdoptsBeforePull(t *testing.T) {
	fake := &fakeSyncStore{}
	setupSyncCommandTest(t, fake)
	pullsAtAdoption := -1
	syncAdoptGitOrigin = func(context.Context, storage.DoltStorage, adoptPolicy, adoptOptIn) (bool, error) {
		pullsAtAdoption = fake.pullCalls
		return true, nil
	}

	if err := runSyncCommand(syncCmd, nil); err != nil {
		t.Fatalf("runSyncCommand() error = %v, want nil", err)
	}
	if pullsAtAdoption != 0 {
		t.Errorf("Pull() had run %d time(s) when adoption was reached, want 0 (adoption must come first)", pullsAtAdoption)
	}
}

// A rig with no git origin to adopt is the ordinary solo rig, and it must keep
// the benign exit-0 no-remote report rather than acquiring a failure mode:
// adoption declining is not an error.
func TestRunSyncCommandNoGitOriginStillExitsZero(t *testing.T) {
	fake := &fakeSyncStore{pullErr: errors.New("Error 1105: no remote")}
	setupSyncCommandTest(t, fake)
	// setupSyncCommandTest already stubs "nothing to adopt"; assert the
	// no-remote contract survives it explicitly.
	jsonOutput = true

	out := captureStdout(t, func() error { return runSyncCommand(syncCmd, nil) })
	var got syncOutcome
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("json.Unmarshal(%q): %v", out, err)
	}
	if got.Status != syncStatusNoRemote {
		t.Errorf("Status = %q, want %q", got.Status, syncStatusNoRemote)
	}
}

// An explicitly named --remote must never adopt: the operator named a remote
// they expect to exist, and inventing a different one from git origin would
// sync somewhere they never asked for. Same reason the no-remote exit-0 gate is
// default-remote-only.
func TestRunSyncCommandNamedRemoteNeverAdopts(t *testing.T) {
	fake := &fakeSyncStore{}
	setupSyncCommandTest(t, fake)
	adoptCalls := 0
	syncAdoptGitOrigin = func(context.Context, storage.DoltStorage, adoptPolicy, adoptOptIn) (bool, error) {
		adoptCalls++
		return true, nil
	}
	if err := syncCmd.Flags().Set("remote", "mini"); err != nil {
		t.Fatalf("Flags().Set(remote, mini): %v", err)
	}

	if err := runSyncCommand(syncCmd, nil); err != nil {
		t.Fatalf("runSyncCommand() error = %v, want nil", err)
	}
	if adoptCalls != 0 {
		t.Errorf("adoption ran %d time(s) on --remote mini, want 0", adoptCalls)
	}
	if fake.pullRemoteCalls != 1 || fake.lastRemoteArg != "mini" {
		t.Errorf("named-remote pull = (%d calls, %q), want (1, %q)", fake.pullRemoteCalls, fake.lastRemoteArg, "mini")
	}
}

// A failed adoption is a real error, not a benign skip: it means the rig has no
// Dolt remote AND bd could not derive one (a broken dolt_remotes listing, an
// AddRemote refusal, an unwritable config.yaml). Reporting exit 0 there would
// hide the very misconfiguration that stops the rig from ever federating, and
// nothing may be pulled or pushed on top of it.
func TestRunSyncCommandAdoptionErrorFailsLoudly(t *testing.T) {
	fake := &fakeSyncStore{}
	setupSyncCommandTest(t, fake)
	adoptErr := errors.New("dolt_remotes unavailable")
	syncAdoptGitOrigin = func(context.Context, storage.DoltStorage, adoptPolicy, adoptOptIn) (bool, error) {
		return false, adoptErr
	}

	err := runSyncCommand(syncCmd, nil)
	if err == nil {
		t.Fatal("runSyncCommand() error = nil, want a failure when adoption errors")
	}
	if code, ok := exitCodeFromError(err); !ok || code != 1 {
		t.Errorf("exitCodeFromError(err) = (%d, %v), want (1, true)", code, ok)
	}
	if fake.pullCalls != 0 || fake.pushCalls != 0 {
		t.Errorf("pull/push ran (%d/%d) after a failed adoption, want (0/0)", fake.pullCalls, fake.pushCalls)
	}
}

// The seam only buys hermetic tests if it is still bolted to the real thing in
// production. Every case above replaces syncAdoptGitOrigin, so nothing else
// would notice it being left rewired — or never wired at all, which is the
// original bug restated.
func TestSyncAdoptGitOriginIsWiredToAdoption(t *testing.T) {
	got := reflect.ValueOf(syncAdoptGitOrigin).Pointer()
	want := reflect.ValueOf(adoptGitOriginRemoteForPush).Pointer()
	if got != want {
		t.Error("syncAdoptGitOrigin is not bound to adoptGitOriginRemoteForPush; sync would silently stop adopting a git origin")
	}
}

// ---------------------------------------------------------------------------
// wy-wub2s: telling a STUCK working set from a BUSY one, and reporting a run
// that fought more than one transient condition.
// ---------------------------------------------------------------------------

// Every blocked attempt seeing byte-identical pending edits is the evidence the
// cross-tick detector compares. The loop reports it; it does not itself decide
// anything is wrong.
func TestRunSyncLoopFoldsIdenticalDirtyEvidence(t *testing.T) {
	r := &syncOpsRecorder{
		recomputeErrs: []error{dirtyGraphErr()},
		fingerprints:  []string{"issues:aaa"},
	}
	out, err := runSyncLoop(context.Background(), r.ops(), 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Status != syncStatusRetriesExhausted {
		t.Fatalf("status = %q, want %q (the loop never escalates on its own)", out.Status, syncStatusRetriesExhausted)
	}
	if out.DirtyGraphFingerprint != "issues:aaa" {
		t.Errorf("DirtyGraphFingerprint = %q, want %q", out.DirtyGraphFingerprint, "issues:aaa")
	}
	if r.fingerprintCalls != 3 {
		t.Errorf("fingerprint samples = %d, want 3 (one per blocked attempt)", r.fingerprintCalls)
	}
}

// A working set that visibly moves between attempts is a BUSY fleet. Reporting
// a fingerprint for it would let the cross-tick detector escalate contention as
// a wedge, so the run must prove nothing.
func TestRunSyncLoopMovingDirtyEvidenceProvesNothing(t *testing.T) {
	cases := []struct {
		name            string
		fingerprints    []string
		fingerprintErrs []error
	}{
		{name: "dirty set changed between attempts", fingerprints: []string{"issues:aaa", "issues:bbb", "issues:aaa"}},
		{name: "evidence unavailable", fingerprintErrs: []error{errors.New("dolt_diff unsupported")}},
		{
			name:         "sampled clean: the other writer committed after the guard fired",
			fingerprints: []string{""},
		},
		{
			name:         "one unavailable sample disqualifies the run",
			fingerprints: []string{"issues:aaa", "issues:aaa", "issues:aaa"},
			// The middle sample fails, so the attempts either side cannot be
			// claimed to be identical to it.
			fingerprintErrs: []error{nil, errors.New("read timeout"), nil},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &syncOpsRecorder{
				recomputeErrs:   []error{dirtyGraphErr()},
				fingerprints:    tc.fingerprints,
				fingerprintErrs: tc.fingerprintErrs,
			}
			out, err := runSyncLoop(context.Background(), r.ops(), 3)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if out.DirtyGraphFingerprint != "" {
				t.Errorf("DirtyGraphFingerprint = %q, want empty", out.DirtyGraphFingerprint)
			}
		})
	}
}

// A store with no way to answer the question leaves the hook nil. That must be
// treated as unavailable evidence, not as "nothing changed".
func TestRunSyncLoopWithoutEvidenceHookNeverEscalates(t *testing.T) {
	r := &syncOpsRecorder{recomputeErrs: []error{dirtyGraphErr()}}
	out, err := runSyncLoop(context.Background(), r.ops(), 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.DirtyGraphFingerprint != "" {
		t.Errorf("DirtyGraphFingerprint = %q, want empty with no evidence hook", out.DirtyGraphFingerprint)
	}
	if _, stuck := classifyDirtyProgress(out, &syncState{StuckTicks: 99}, time.Now()); stuck {
		t.Error("escalated with no evidence at all")
	}
}

// A repair that succeeds is the working set demonstrably advancing, so earlier
// blocked attempts in the same run must not leave stuck-looking evidence behind
// for a later push-race exhaustion to inherit.
func TestRunSyncLoopSuccessfulRepairDropsDirtyEvidence(t *testing.T) {
	r := &syncOpsRecorder{
		recomputeErrs: []error{dirtyGraphErr(), nil},
		pushErrs:      []error{raceErr()},
		fingerprints:  []string{"issues:aaa"},
	}
	out, err := runSyncLoop(context.Background(), r.ops(), 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Status != syncStatusRetriesExhausted {
		t.Fatalf("status = %q, want %q", out.Status, syncStatusRetriesExhausted)
	}
	if out.DirtyGraphFingerprint != "" {
		t.Errorf("DirtyGraphFingerprint = %q, want empty (the repair ran, so the dirt cleared)", out.DirtyGraphFingerprint)
	}
}

// The mixed-history record (F7/F8): LastPushError/LastRecomputeError still name
// only the final attempt, and Transients is where "what did this run fight"
// lives.
func TestRunSyncLoopRecordsEveryTransient(t *testing.T) {
	r := &syncOpsRecorder{
		pushErrs:      []error{raceErr()},
		recomputeErrs: []error{nil, dirtyGraphErr()},
	}
	out, err := runSyncLoop(context.Background(), r.ops(), 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out.Transients) != 2 {
		t.Fatalf("Transients = %+v, want two entries", out.Transients)
	}
	if out.Transients[0].Kind != syncTransientPushRace || out.Transients[0].Attempt != 1 {
		t.Errorf("first transient = %+v, want a push race on attempt 1", out.Transients[0])
	}
	if out.Transients[1].Kind != syncTransientDirtyGraph || out.Transients[1].Attempt != 2 {
		t.Errorf("second transient = %+v, want a dirty graph on attempt 2", out.Transients[1])
	}
	if out.Transients[0].Error == "" || out.Transients[1].Error == "" {
		t.Error("transients must quote the error each attempt failed on")
	}
	// The pre-existing discriminator is unchanged: the final attempt only.
	if out.LastRecomputeError == "" || out.LastPushError != "" {
		t.Errorf("Last*Error = %q/%q, want only the final attempt's failure",
			out.LastPushError, out.LastRecomputeError)
	}
	if !out.sawTransient(syncTransientPushRace) || !out.sawTransient(syncTransientDirtyGraph) {
		t.Error("sawTransient does not report both conditions")
	}
}

func TestClassifyDirtyProgress(t *testing.T) {
	now := time.Unix(1700000000, 0)
	exhausted := func() *syncOutcome {
		return &syncOutcome{
			Status:                syncStatusRetriesExhausted,
			LastRecomputeError:    dirtyGraphErr().Error(),
			DirtyGraphFingerprint: "issues:aaa",
		}
	}

	t.Run("first sighting arms the marker without escalating", func(t *testing.T) {
		next, stuck := classifyDirtyProgress(exhausted(), &syncState{}, now)
		if stuck {
			t.Error("escalated on the first sighting — one run cannot tell stuck from busy")
		}
		if next.StuckTicks != 1 || next.DirtyGraphFingerprint != "issues:aaa" {
			t.Errorf("next = %+v, want the fingerprint at 1 tick", next)
		}
		if !next.FirstSeen.Equal(now) {
			t.Errorf("FirstSeen = %v, want %v", next.FirstSeen, now)
		}
	})

	t.Run("escalates when the same evidence survives the threshold", func(t *testing.T) {
		first := now.Add(-10 * time.Minute)
		prev := &syncState{DirtyGraphFingerprint: "issues:aaa", StuckTicks: syncStuckTicks - 1, FirstSeen: first}
		next, stuck := classifyDirtyProgress(exhausted(), prev, now)
		if !stuck {
			t.Fatalf("did not escalate at %d consecutive runs", next.StuckTicks)
		}
		if next.StuckTicks != syncStuckTicks {
			t.Errorf("StuckTicks = %d, want %d", next.StuckTicks, syncStuckTicks)
		}
		// The operator wants to know how long this has been wedged, so the
		// first sighting must survive the increments.
		if !next.FirstSeen.Equal(first) {
			t.Errorf("FirstSeen = %v, want the original sighting %v", next.FirstSeen, first)
		}
	})

	t.Run("different pending edits reset the count", func(t *testing.T) {
		prev := &syncState{DirtyGraphFingerprint: "issues:bbb", StuckTicks: syncStuckTicks + 5}
		next, stuck := classifyDirtyProgress(exhausted(), prev, now)
		if stuck {
			t.Error("escalated across a CHANGED dirty set — that is a busy fleet, not a wedge")
		}
		if next.StuckTicks != 1 {
			t.Errorf("StuckTicks = %d, want 1", next.StuckTicks)
		}
	})

	t.Run("any non-dirty outcome clears the marker", func(t *testing.T) {
		armed := &syncState{DirtyGraphFingerprint: "issues:aaa", StuckTicks: syncStuckTicks - 1}
		for _, out := range []*syncOutcome{
			{Status: syncStatusOK, Pushed: true},
			{Status: syncStatusConflict, Conflicts: []string{"issues"}},
			// Exhausted, but on a push race: this replica is not wedged on
			// pending graph edits.
			{Status: syncStatusRetriesExhausted, LastPushError: "non-fast-forward", DirtyGraphFingerprint: "issues:aaa"},
			// Exhausted on dirt, but with no comparable evidence.
			{Status: syncStatusRetriesExhausted, LastRecomputeError: dirtyGraphErr().Error()},
		} {
			next, stuck := classifyDirtyProgress(out, armed, now)
			if stuck {
				t.Errorf("status %q escalated", out.Status)
			}
			if next.DirtyGraphFingerprint != "" || next.StuckTicks != 0 {
				t.Errorf("status %q left marker %+v, want it cleared", out.Status, next)
			}
		}
	})
}

// The stuck report is the one that must NOT say "transient, retry on the next
// tick": that is the wording an operator has already been ignoring for however
// many ticks it took to get here.
func TestSyncStuckMessage(t *testing.T) {
	got := strings.Join(syncStuckMessage(&syncOutcome{
		Status:                syncStatusDirtyStuck,
		Attempts:              3,
		DirtyGraphStuckTicks:  4,
		LastRecomputeError:    dirtyGraphErr().Error(),
		DirtyGraphFingerprint: "issues:aaa",
	}), "\n")
	if !strings.Contains(got, "4 consecutive sync run(s)") {
		t.Errorf("message does not report how long this has been wedged:\n%s", got)
	}
	if !strings.Contains(got, "Nothing is advancing") {
		t.Errorf("message does not state the evidence:\n%s", got)
	}
	if !strings.Contains(got, "Resolve it by hand") {
		t.Errorf("message does not give the operator a next step:\n%s", got)
	}
	if strings.Contains(got, "retry on the next tick") {
		t.Errorf("stuck message still tells the operator to wait:\n%s", got)
	}
}

// The positive (constraint-violation) branch names the violating table(s)
// instead of a tick count, and must not claim a consecutive-run history it
// never measured.
func TestSyncStuckMessageConstraintViolations(t *testing.T) {
	got := strings.Join(syncStuckMessage(&syncOutcome{
		Status:               syncStatusDirtyStuck,
		Attempts:             1,
		LastRecomputeError:   dirtyGraphErr().Error(),
		ConstraintViolations: []storage.ConstraintViolation{{Table: "issues", Count: 3}},
	}), "\n")
	if !strings.Contains(got, "constraint violations") {
		t.Errorf("message does not name constraint violations as the cause:\n%s", got)
	}
	if !strings.Contains(got, "issues (3 row(s))") {
		t.Errorf("message does not name the violating table and count:\n%s", got)
	}
	if !strings.Contains(got, "Resolve it by hand") {
		t.Errorf("message does not give the operator a next step:\n%s", got)
	}
	if strings.Contains(got, "consecutive sync run(s)") {
		t.Errorf("positive-evidence message must not claim a tick-count history:\n%s", got)
	}
	if strings.Contains(got, "retry on the next tick") {
		t.Errorf("stuck message still tells the operator to wait:\n%s", got)
	}
}

func TestSyncMixedTransientNote(t *testing.T) {
	mixed := &syncOutcome{
		Attempts:           2,
		LastRecomputeError: dirtyGraphErr().Error(),
		Transients: []syncTransient{
			{Attempt: 1, Kind: syncTransientPushRace, Error: "non-fast-forward"},
			{Attempt: 2, Kind: syncTransientDirtyGraph, Error: dirtyGraphErr().Error()},
		},
	}
	got := strings.Join(syncRetriesExhaustedMessage(mixed), "\n")
	if !strings.Contains(got, "BOTH transient conditions") {
		t.Errorf("mixed run is not reported as mixed:\n%s", got)
	}
	// A single-condition run must stay short: the note is only worth its lines
	// when the headline is genuinely incomplete.
	single := &syncOutcome{
		Attempts:           2,
		LastRecomputeError: dirtyGraphErr().Error(),
		Transients: []syncTransient{
			{Attempt: 1, Kind: syncTransientDirtyGraph, Error: dirtyGraphErr().Error()},
		},
	}
	if quiet := strings.Join(syncRetriesExhaustedMessage(single), "\n"); strings.Contains(quiet, "BOTH") {
		t.Errorf("single-condition run claims a mixed history:\n%s", quiet)
	}
}

// The marker round-trips through the same .beads scratch file the next tick
// reads, and a cleared marker leaves no stale file behind.
func TestSyncStatePersistence(t *testing.T) {
	dir := t.TempDir()
	if got := loadSyncState(dir); got.DirtyGraphFingerprint != "" || got.StuckTicks != 0 {
		t.Fatalf("missing marker loaded as %+v, want zero", got)
	}
	saveSyncState(dir, &syncState{DirtyGraphFingerprint: "issues:aaa", StuckTicks: 2, FirstSeen: time.Unix(1700000000, 0)})
	got := loadSyncState(dir)
	if got.DirtyGraphFingerprint != "issues:aaa" || got.StuckTicks != 2 {
		t.Fatalf("round-tripped marker = %+v", got)
	}
	saveSyncState(dir, &syncState{})
	if _, err := os.Stat(filepath.Join(dir, syncStateFile)); !os.IsNotExist(err) {
		t.Errorf("cleared marker left a file behind (stat err = %v)", err)
	}
	// A corrupt marker must degrade to "no evidence", never fail the sync.
	if err := os.WriteFile(filepath.Join(dir, syncStateFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadSyncState(dir); got.DirtyGraphFingerprint != "" {
		t.Errorf("corrupt marker loaded as %+v, want zero", got)
	}
}
