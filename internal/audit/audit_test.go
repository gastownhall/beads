package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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

// auditTestWorkspace isolates the sidecar in a fresh BEADS_DIR, the way the
// package's other tests do: metadata.json satisfies FindBeadsDir's
// project-files check.
func auditTestWorkspace(t *testing.T) string {
	t.Helper()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	if err := os.WriteFile(metadataPath, []byte(`{"backend":"dolt"}`), 0644); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	return beadsDir
}

func TestLogClaimed(t *testing.T) {
	t.Run("enabled_appends_post_state_only", func(t *testing.T) {
		beadsDir := auditTestWorkspace(t)
		t.Setenv("BD_AUDIT_ENABLED", "1")

		LogClaimed("bd-x1", "alice", "in_progress", "alice")

		data, err := os.ReadFile(filepath.Join(beadsDir, FileName))
		if err != nil {
			t.Fatalf("read sidecar: %v", err)
		}
		var e Entry
		if err := json.Unmarshal(data, &e); err != nil {
			t.Fatalf("bad sidecar line %q: %v", data, err)
		}
		if e.Kind != "claimed" || e.IssueID != "bd-x1" || e.Actor != "alice" {
			t.Errorf("entry = kind %q issue %q actor %q, want claimed/bd-x1/alice", e.Kind, e.IssueID, e.Actor)
		}
		want := map[string]any{"status": "in_progress", "assignee": "alice"}
		if len(e.Extra) != len(want) || e.Extra["status"] != "in_progress" || e.Extra["assignee"] != "alice" {
			t.Errorf("extra = %v, want exactly %v (post-state only, no invented prior values)", e.Extra, want)
		}
	})

	t.Run("disabled_appends_nothing", func(t *testing.T) {
		beadsDir := auditTestWorkspace(t)
		t.Setenv("BD_AUDIT_ENABLED", "")

		LogClaimed("bd-x2", "alice", "in_progress", "alice")

		if _, err := os.Stat(filepath.Join(beadsDir, FileName)); !os.IsNotExist(err) {
			t.Fatalf("disabled LogClaimed created %s: %v", FileName, err)
		}
	})

	t.Run("empty_issue_id_appends_nothing", func(t *testing.T) {
		beadsDir := auditTestWorkspace(t)
		t.Setenv("BD_AUDIT_ENABLED", "1")

		LogClaimed("", "alice", "in_progress", "alice")

		if _, err := os.Stat(filepath.Join(beadsDir, FileName)); !os.IsNotExist(err) {
			t.Fatalf("LogClaimed with an empty issue id created %s: %v", FileName, err)
		}
	})

	t.Run("unwritable_sidecar_stays_best_effort", func(t *testing.T) {
		beadsDir := auditTestWorkspace(t)
		t.Setenv("BD_AUDIT_ENABLED", "1")
		// A directory where the sidecar file belongs makes every open fail
		// without touching permissions tricks.
		if err := os.Mkdir(filepath.Join(beadsDir, FileName), 0750); err != nil {
			t.Fatalf("mkdir blocking sidecar: %v", err)
		}

		// Must not panic or report: sidecar logging never blocks operations.
		LogClaimed("bd-x3", "alice", "in_progress", "alice")
	})
}
