package metrics

import (
	"os"
	"testing"
)

// TestInTestModeDetectsEnv exercises the extracted boolean decision directly:
// inTestMode must match on BEADS_TEST_MODE=1 exactly, the same idiom the
// storage layer uses, and treat anything else (unset, empty, "0", "true") as
// not-test-mode.
func TestInTestModeDetectsEnv(t *testing.T) {
	cases := []struct {
		name string
		val  string
		set  bool
		want bool
	}{
		{name: "unset", set: false, want: false},
		{name: "empty", val: "", set: true, want: false},
		{name: "one", val: "1", set: true, want: true},
		{name: "true-word", val: "true", set: true, want: false},
		{name: "zero", val: "0", set: true, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(EnvTestMode, tc.val)
			} else {
				// Hermetic unset: the repo's own runner exports
				// BEADS_TEST_MODE=1 (scripts/test.sh), so relying on the
				// ambient environment would fail under a supported test
				// mode. t.Setenv registers the restore; Unsetenv then
				// clears it for this subtest.
				t.Setenv(EnvTestMode, "")
				os.Unsetenv(EnvTestMode)
			}
			if got := inTestMode(); got != tc.want {
				t.Errorf("inTestMode() = %v, want %v (val=%q set=%v)", got, tc.want, tc.val, tc.set)
			}
		})
	}
}

// TestMaybeSpawnFlusherNoOpInTestMode guards the source-level test-mode guard:
// with BEADS_TEST_MODE=1 set, MaybeSpawnFlusher must skip spawning the
// detached send-metrics child even though metrics are otherwise enabled and
// BD_DISABLE_EVENT_FLUSH is unset. If the guard regresses this would fork a
// real child process during `go test`.
func TestMaybeSpawnFlusherNoOpInTestMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(EnvTestMode, "1")
	// CI exports BD_DISABLE_EVENT_FLUSH=1 workflow-wide (main.yml/pr.yml env
	// blocks), which would trip the flushDisabledByEnv precondition below
	// before the test-mode gate is ever exercised. Clear it hermetically.
	t.Setenv(EnvDisableEventFlush, "")
	os.Unsetenv(EnvDisableEventFlush)

	if _, err := Init("0.0.0-test", true, ""); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !Enabled() {
		t.Fatalf("Enabled() = false, want true")
	}
	if flushDisabledByEnv() {
		t.Fatalf("flushDisabledByEnv() = true, want false (BD_DISABLE_EVENT_FLUSH unset)")
	}

	// The only thing preventing a spawn here is the BEADS_TEST_MODE guard —
	// assert the extracted decision directly, so a regression fails the test
	// instead of silently forking a real child.
	if shouldSpawnFlusher() {
		t.Fatalf("shouldSpawnFlusher() = true under BEADS_TEST_MODE=1, want false")
	}

	// Positive control: with test mode hermetically cleared (and every other
	// gate still open), the spawn decision must flip to true — proving the
	// assertion above is gated on BEADS_TEST_MODE and not on some other
	// condition that happens to hold in this environment.
	t.Setenv(EnvTestMode, "")
	os.Unsetenv(EnvTestMode)
	t.Setenv(EnvDisableEventFlush, "")
	os.Unsetenv(EnvDisableEventFlush)
	if !shouldSpawnFlusher() {
		t.Fatalf("shouldSpawnFlusher() = false with BEADS_TEST_MODE cleared, want true")
	}

	// And the exported entry point still returns without spawning under the
	// guard (smoke path).
	t.Setenv(EnvTestMode, "1")
	MaybeSpawnFlusher()
}
