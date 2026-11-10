package versioncontrolops

import (
	"context"
	"errors"
	"regexp"
	"testing"
)

// TestAbortMergeHasNoResetFallback pins the exported AbortMerge the CLI pull
// route uses to abort a merge it declined (GH#7839): the CALL is drained, its
// error reaches the caller, and unlike the SQL route's best-effort abortMerge
// it never falls back to DOLT_RESET('--hard') — the caller only claims the
// working set was restored after re-reading the merge state.
func TestAbortMergeHasNoResetFallback(t *testing.T) {
	abortFailed := errors.New("fatal: There is no merge to abort")
	for _, tc := range []struct {
		name     string
		abortErr error
	}{
		{name: "abort succeeds"},
		{name: "abort fails", abortErr: abortFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, mock := newDrainMock(t)
			if tc.abortErr != nil {
				mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_MERGE('--abort')")).WillReturnError(tc.abortErr)
			} else {
				expectDrainedCall(mock, "CALL DOLT_MERGE('--abort')")
			}

			err := AbortMerge(context.Background(), rec)
			if !errors.Is(err, tc.abortErr) || (tc.abortErr == nil && err != nil) {
				t.Fatalf("AbortMerge = %v, want %v", err, tc.abortErr)
			}
			rec.assertDrained(t, mock)
			if rec.sentStatement("DOLT_RESET") {
				t.Errorf("AbortMerge issued DOLT_RESET; want none (statements: execs %q, queries %q)", rec.execs, rec.queries)
			}
		})
	}
}
