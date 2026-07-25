package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

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

	pulls      int
	conflicts  int
	recomputes int
	pushes     int
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
	return syncOps{
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
func TestIsNoRemoteConfiguredErrSupersetOfRemoteNotFound(t *testing.T) {
	for _, msg := range []string{
		`remote "origin" not found`,
		"REMOTE NOT FOUND",
		"could not be found: unknown remote",
	} {
		err := errors.New(msg)
		if isRemoteNotFoundErr(err) && !isNoRemoteConfiguredErr(err) {
			t.Errorf("%q: accepted by isRemoteNotFoundErr but rejected by isNoRemoteConfiguredErr", msg)
		}
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
