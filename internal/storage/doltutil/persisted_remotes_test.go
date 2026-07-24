package doltutil

import (
	"os"
	"path/filepath"
	"testing"
)

// PersistedRemotes must distinguish three states (bd-6dnrw.33): definitely no
// remotes (not a dolt repo, or an empty remotes map), remotes present, and
// "could not tell" (unreadable/corrupt state file) — the last as an error,
// never silently as "none".
func TestPersistedRemotes(t *testing.T) {
	writeState := func(t *testing.T, body string) string {
		t.Helper()
		dir := t.TempDir()
		doltDir := filepath.Join(dir, ".dolt")
		if err := os.MkdirAll(doltDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(doltDir, "repo_state.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	t.Run("not a dolt repository is no remotes, no error", func(t *testing.T) {
		remotes, err := PersistedRemotes(t.TempDir())
		if err != nil || remotes != nil {
			t.Fatalf("PersistedRemotes = %v, %v; want nil, nil", remotes, err)
		}
	})

	t.Run("empty remotes map is no remotes", func(t *testing.T) {
		dir := writeState(t, `{"head":"refs/heads/main","remotes":{},"backups":{},"branches":{}}`)
		remotes, err := PersistedRemotes(dir)
		if err != nil || len(remotes) != 0 {
			t.Fatalf("PersistedRemotes = %v, %v; want empty, nil", remotes, err)
		}
	})

	t.Run("remotes are returned sorted by name", func(t *testing.T) {
		dir := writeState(t, `{"remotes":{
			"upstream":{"name":"upstream","url":"file:///tmp/u","fetch_specs":["refs/heads/*:refs/remotes/upstream/*"],"params":{}},
			"origin":{"name":"origin","url":"file:///tmp/o","fetch_specs":["refs/heads/*:refs/remotes/origin/*"],"params":{}}
		}}`)
		remotes, err := PersistedRemotes(dir)
		if err != nil {
			t.Fatalf("PersistedRemotes: %v", err)
		}
		if len(remotes) != 2 || remotes[0].Name != "origin" || remotes[0].URL != "file:///tmp/o" ||
			remotes[1].Name != "upstream" || remotes[1].URL != "file:///tmp/u" {
			t.Fatalf("PersistedRemotes = %+v, want origin then upstream", remotes)
		}
	})

	t.Run("corrupt state file is an error, not silently none", func(t *testing.T) {
		dir := writeState(t, `{"remotes": not-json`)
		if _, err := PersistedRemotes(dir); err == nil {
			t.Fatal("PersistedRemotes on corrupt repo_state.json = nil error, want error")
		}
	})
}
