package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestAppend_CreatesFileAndWritesJSONL(t *testing.T) {
	tmp := t.TempDir()
	beadsDir := filepath.Join(tmp, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// beads.FindBeadsDir() validates that the directory contains project files.
	// Create metadata.json so BEADS_DIR is accepted by hasBeadsProjectFiles.
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	if err := os.WriteFile(metadataPath, []byte(`{"backend":"dolt"}`), 0644); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BD_AUDIT_ENABLED", "1")

	id1, err := Append(&Entry{Kind: "llm_call", Model: "test-model", Prompt: "p", Response: "r"})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if id1 == "" {
		t.Fatalf("expected id")
	}
	_, err = Append(&Entry{Kind: "label", ParentID: id1, Label: "good", Reason: "ok"})
	if err != nil {
		t.Fatalf("append label: %v", err)
	}

	p := filepath.Join(beadsDir, FileName)
	f, err := os.Open(p)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	lines := 0
	for sc.Scan() {
		lines++
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if lines != 2 {
		t.Fatalf("expected 2 lines, got %d", lines)
	}
}

func TestAppendIfEnabled_DisabledDoesNotCreateFile(t *testing.T) {
	tmp := t.TempDir()
	beadsDir := filepath.Join(tmp, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	if err := os.WriteFile(metadataPath, []byte(`{"backend":"dolt"}`), 0644); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BD_AUDIT_ENABLED", "")

	if _, err := AppendIfEnabled(&Entry{Kind: "llm_call", Model: "test-model"}); err == nil {
		t.Fatalf("expected disabled append to fail")
	}

	if _, err := os.Stat(filepath.Join(beadsDir, FileName)); !os.IsNotExist(err) {
		t.Fatalf("disabled append created %s: %v", FileName, err)
	}
}

func TestEnsureFile_DoesNotTruncateWhenConcurrentCreatorWins(t *testing.T) {
	tmp := t.TempDir()
	beadsDir := filepath.Join(tmp, ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	if err := os.WriteFile(metadataPath, []byte(`{"backend":"dolt"}`), 0644); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}
	t.Setenv("BEADS_DIR", beadsDir)

	wantContents := []byte("{\"id\":\"seed\"}\n")

	oldHook := ensureFileBeforeCreateHook
	defer func() { ensureFileBeforeCreateHook = oldHook }()

	var once sync.Once
	ensureFileBeforeCreateHook = func(path string) {
		once.Do(func() {
			if err := os.WriteFile(path, wantContents, 0644); err != nil {
				t.Fatalf("seed interactions log: %v", err)
			}
		})
	}

	gotPath, err := EnsureFile()
	if err != nil {
		t.Fatalf("EnsureFile: %v", err)
	}

	gotContents, err := os.ReadFile(gotPath)
	if err != nil {
		t.Fatalf("read interactions log: %v", err)
	}
	if !bytes.Equal(gotContents, wantContents) {
		t.Fatalf("EnsureFile truncated seeded content: got %q, want %q", gotContents, wantContents)
	}
}

func TestLogClaimed(t *testing.T) {
	setup := func(t *testing.T, enabled string) string {
		t.Helper()
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(beadsDir, 0750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt"}`), 0644); err != nil {
			t.Fatalf("write metadata.json: %v", err)
		}
		t.Setenv("BEADS_DIR", beadsDir)
		t.Setenv("BD_AUDIT_ENABLED", enabled)
		return beadsDir
	}

	t.Run("enabled writes one post-state-only claimed entry", func(t *testing.T) {
		beadsDir := setup(t, "1")
		LogClaimed("bd-1", "alice", "in_progress", "alice")

		data, err := os.ReadFile(filepath.Join(beadsDir, FileName))
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
		if len(lines) != 1 {
			t.Fatalf("expected 1 line, got %d: %s", len(lines), data)
		}
		var got map[string]any
		if err := json.Unmarshal(lines[0], &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got["kind"] != "claimed" || got["issue_id"] != "bd-1" || got["actor"] != "alice" {
			t.Fatalf("entry = %v, want kind claimed for bd-1 by alice", got)
		}
		if _, ok := got["reason"]; ok {
			t.Fatalf("entry = %v, want no reason", got)
		}
		want := map[string]any{"status": "in_progress", "assignee": "alice"}
		if !reflect.DeepEqual(got["extra"], want) {
			t.Fatalf("extra = %v, want exactly %v (no old/new values)", got["extra"], want)
		}
	})

	for _, enabled := range []string{"", "0"} {
		t.Run("disabled ("+enabled+") creates nothing", func(t *testing.T) {
			beadsDir := setup(t, enabled)
			LogClaimed("bd-1", "alice", "in_progress", "alice")
			if _, err := os.Stat(filepath.Join(beadsDir, FileName)); !os.IsNotExist(err) {
				t.Fatalf("disabled LogClaimed created %s: %v", FileName, err)
			}
		})
	}

	t.Run("unwritable sidecar stays best-effort", func(t *testing.T) {
		beadsDir := setup(t, "1")
		// A directory where the log should be makes every append fail.
		if err := os.Mkdir(filepath.Join(beadsDir, FileName), 0750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		LogClaimed("bd-1", "alice", "in_progress", "alice")
	})
}
