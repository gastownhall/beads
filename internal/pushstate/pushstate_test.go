package pushstate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPushStateLoad pins Load's contract for every file shape a workspace can
// hold (GH#6289): a missing file is the never-pushed state, a legacy file
// with only the success watermarks still parses, unknown fields are ignored
// (old files grow fields without breaking older readers), and a malformed
// file is an error so callers can warn instead of guessing.
func TestPushStateLoad(t *testing.T) {
	t.Run("missing file is never-pushed", func(t *testing.T) {
		st, err := Load(t.TempDir())
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if st != nil {
			t.Fatalf("Load = %+v, want nil for a missing file", st)
		}
	})

	t.Run("legacy file with only success watermarks", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, FileName),
			[]byte(`{"last_push":"2026-01-01T00:00:00Z","last_commit":"abc123"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		st, err := Load(dir)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if st == nil || st.LastPush != "2026-01-01T00:00:00Z" || st.LastCommit != "abc123" {
			t.Fatalf("Load = %+v, want the legacy success watermarks", st)
		}
		if st.LastAttempt != "" || st.LastError != "" {
			t.Errorf("legacy file grew attempt/error fields: %+v", st)
		}
	})

	t.Run("attempt and error fields round-trip the schema", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, FileName),
			[]byte(`{"last_push":"2026-01-01T00:00:00Z","last_commit":"abc123",`+
				`"last_attempt":"2026-01-02T00:00:00Z","last_error":"remote unreachable"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		st, err := Load(dir)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if st.LastAttempt != "2026-01-02T00:00:00Z" || st.LastError != "remote unreachable" {
			t.Fatalf("Load = %+v, want the recorded attempt and error", st)
		}
	})

	t.Run("unknown fields are ignored", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, FileName),
			[]byte(`{"last_push":"2026-01-01T00:00:00Z","future_field":"whatever"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		st, err := Load(dir)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if st == nil || st.LastPush != "2026-01-01T00:00:00Z" {
			t.Fatalf("Load = %+v, want the known fields parsed", st)
		}
	})

	t.Run("malformed file is an error", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, FileName), []byte(`not json`), 0o644); err != nil {
			t.Fatal(err)
		}
		st, err := Load(dir)
		if err == nil {
			t.Fatal("Load with malformed JSON: want error, got nil")
		}
		if st != nil {
			t.Errorf("Load with malformed JSON returned state %+v alongside the error", st)
		}
		if !strings.Contains(err.Error(), FileName) {
			t.Errorf("error %v does not name the file", err)
		}
	})
}
