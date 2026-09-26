package doltutil

import (
	"os"
	"path/filepath"
	"strings"
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

	t.Run("git_ref parameter is surfaced as Ref", func(t *testing.T) {
		dir := writeState(t, `{"remotes":{
			"branch":{"name":"branch","url":"git+https://example.com/repo.git","fetch_specs":["refs/heads/*:refs/remotes/branch/*"],"params":{"git_ref":"refs/heads/issue-data"}},
			"plain":{"name":"plain","url":"file:///tmp/p","fetch_specs":["refs/heads/*:refs/remotes/plain/*"],"params":{}},
			"unit":{"name":"unit","url":"git+file:///srv/ledgers","fetch_specs":["refs/heads/*:refs/remotes/unit/*"],"params":{"git_ref":"refs/dolt/units/team-12542"}}
		}}`)
		remotes, err := PersistedRemotes(dir)
		if err != nil {
			t.Fatalf("PersistedRemotes: %v", err)
		}
		want := map[string]string{"branch": "refs/heads/issue-data", "plain": "", "unit": "refs/dolt/units/team-12542"}
		if len(remotes) != len(want) {
			t.Fatalf("PersistedRemotes = %+v, want %d remotes", remotes, len(want))
		}
		for _, r := range remotes {
			if r.Ref != want[r.Name] {
				t.Errorf("remote %s: Ref = %q, want %q", r.Name, r.Ref, want[r.Name])
			}
		}
		if got, _ := FindCLIRemoteRef(dir, "unit"); got != "refs/dolt/units/team-12542" {
			t.Errorf("FindCLIRemoteRef(unit) = %q", got)
		}
		if got, _ := FindCLIRemoteRef(dir, "plain"); got != "" {
			t.Errorf("FindCLIRemoteRef(plain) = %q, want empty", got)
		}
		if got, _ := FindCLIRemoteRef(dir, "absent"); got != "" {
			t.Errorf("FindCLIRemoteRef(absent) = %q, want empty", got)
		}
	})

	t.Run("corrupt state file is an error, not silently none", func(t *testing.T) {
		dir := writeState(t, `{"remotes": not-json`)
		if _, err := PersistedRemotes(dir); err == nil {
			t.Fatal("PersistedRemotes on corrupt repo_state.json = nil error, want error")
		}
	})

	// A git_ref of the wrong JSON type is "could not tell" too, not "no ref":
	// silently reading it as the default is what would re-add a remote that is
	// pinned to a custom ref onto refs/dolt/data.
	t.Run("a git_ref that is not a string is an error", func(t *testing.T) {
		dir := writeState(t, `{"remotes":{
			"unit":{"name":"unit","url":"git+file:///srv/ledgers","fetch_specs":[],"params":{"git_ref":["refs/dolt/units/team-12542"]}}
		}}`)
		remotes, err := PersistedRemotes(dir)
		if err == nil {
			t.Fatalf("PersistedRemotes with a non-string git_ref = %+v, nil; want an error", remotes)
		}
		if !strings.Contains(err.Error(), "unit") {
			t.Errorf("error %q does not name the remote", err)
		}
		if _, err := FindCLIRemoteRef(dir, "unit"); err == nil {
			t.Error("FindCLIRemoteRef with a non-string git_ref = nil error, want the read error")
		}
	})
}
