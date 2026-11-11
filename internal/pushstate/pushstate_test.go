package pushstate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPushStateLoad(t *testing.T) {
	write := func(t *testing.T, content string) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	t.Run("missing file is neutral", func(t *testing.T) {
		ps, err := Load(t.TempDir())
		if err != nil || ps != nil {
			t.Fatalf("Load(missing) = %+v, %v; want nil, nil", ps, err)
		}
	})

	t.Run("legacy file without attempt fields", func(t *testing.T) {
		ps, err := Load(write(t, `{"last_push":"2026-03-09T12:00:00Z","last_commit":"abc123"}`))
		if err != nil {
			t.Fatalf("Load(legacy): %v", err)
		}
		want := State{LastPush: "2026-03-09T12:00:00Z", LastCommit: "abc123"}
		if ps == nil || *ps != want {
			t.Fatalf("Load(legacy) = %+v, want %+v", ps, want)
		}
	})

	t.Run("attempt fields and unknown fields", func(t *testing.T) {
		ps, err := Load(write(t, `{"last_push":"a","last_commit":"c","last_attempt":"b","last_error":"boom","future":1}`))
		if err != nil {
			t.Fatalf("Load(new): %v", err)
		}
		want := State{LastPush: "a", LastCommit: "c", LastAttempt: "b", LastError: "boom"}
		if ps == nil || *ps != want {
			t.Fatalf("Load(new) = %+v, want %+v", ps, want)
		}
	})

	t.Run("malformed JSON is an error", func(t *testing.T) {
		if _, err := Load(write(t, `not json`)); err == nil {
			t.Fatal("Load(malformed) = nil error, want a parse error")
		}
	})

	t.Run("unreadable path is an error", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, FileName), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(dir); err == nil {
			t.Fatal("Load(directory at push-state path) = nil error, want a read error")
		}
	})
}
