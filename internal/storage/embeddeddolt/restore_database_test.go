//go:build cgo

package embeddeddolt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRestoreDatabaseRefusesNonBackupDirBeforeConnecting pins that
// EmbeddedDoltStore.RestoreDatabase refuses a source that holds no Dolt backup
// before it opens the engine (bd-jwaljq, GH#7098): a forced
// DOLT_BACKUP('restore') drops the live database before it syncs the source,
// so callers that bypass the CLI preflight, bootstrap among them, need the
// same refusal. The store is marked closed, so reaching the connection step
// shows up as errClosed and no engine is ever opened.
func TestRestoreDatabaseRefusesNonBackupDirBeforeConnecting(t *testing.T) {
	root := t.TempDir()
	mkdir := func(name string, files ...string) string {
		t.Helper()
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	file := filepath.Join(root, "a-file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, dir, want string
	}{
		{name: "missing", dir: filepath.Join(root, "absent"), want: "backup source does not exist"},
		{name: "regular_file", dir: file, want: "backup source is not a directory"},
		{name: "empty_dir", dir: mkdir("empty"), want: "holds no Dolt backup"},
		{name: "exports_only", dir: mkdir("exports", "issues.jsonl", "backup_state.json"), want: "holds no Dolt backup"},
		// A backup at a path Dolt reads as another directory: Dolt unescapes
		// the "%23", on every platform.
		{name: "backup_at_a_misread_path", dir: mkdir("proj%232", "manifest"), want: "cannot be restored where it is"},
	} {
		for _, force := range []bool{true, false} {
			s := &EmbeddedDoltStore{database: "beads"}
			s.closed.Store(true)
			err := s.RestoreDatabase(context.Background(), tc.dir, force)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s, force=%v: RestoreDatabase error = %v, want one containing %q", tc.name, force, err, tc.want)
			}
			if errors.Is(err, errClosed) {
				t.Errorf("%s, force=%v: RestoreDatabase reached the engine connection: %v", tc.name, force, err)
			}
		}
	}

	// Control: a source with a manifest passes the check and reaches the
	// connection step, so the refusals above are the check's own.
	s := &EmbeddedDoltStore{database: "beads"}
	s.closed.Store(true)
	if err := s.RestoreDatabase(context.Background(), mkdir("backup", "manifest"), true); !errors.Is(err, errClosed) {
		t.Errorf("RestoreDatabase from a backup = %v, want errClosed from the connection step", err)
	}
}
