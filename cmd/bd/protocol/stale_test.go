package protocol

import "testing"

// TestProtocol_StaleRejectsNonPositiveDays asserts that bd stale rejects
// --days values less than 1. Zero and negative days are nonsensical for
// a staleness check and should fail with a non-zero exit code.
func TestProtocol_StaleRejectsNonPositiveDays(t *testing.T) {
	w := newWorkspace(t)

	t.Run("zero", func(t *testing.T) {
		_, err := w.tryRun("stale", "--days", "0")
		if err == nil {
			t.Error("bd stale --days 0 should fail but exited 0")
		}
	})

	t.Run("negative", func(t *testing.T) {
		_, err := w.tryRun("stale", "--days", "-1")
		if err == nil {
			t.Error("bd stale --days -1 should fail but exited 0")
		}
	})
}
