package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/storage"
)

// defaultOffNoteStore is an open store for the export.auto=false path. That
// path must not touch storage, so every method but IsClosed panics through
// the nil embedded interface.
type defaultOffNoteStore struct {
	storage.DoltStorage
	closed bool
}

func (s *defaultOffNoteStore) IsClosed() bool { return s.closed }

type defaultOffNoteFixture struct {
	dir       string
	beadsDir  string
	statePath string
	jsonlPath string
}

// defaultOffNoteWorkspace makes a workspace that auto-exported under the
// pre-v1.0.5 default: export-state.json and issues.jsonl exist, no layer sets
// export.auto, and an open store is installed. Output-mode globals start in
// normal human mode and are restored afterwards.
func defaultOffNoteWorkspace(t *testing.T) defaultOffNoteFixture {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	for _, name := range []string{
		"BD_EXPORT_AUTO", "BEADS_EXPORT_AUTO",
		"BD_EXPORT_PATH", "BEADS_EXPORT_PATH",
		"BD_GIT_HOOK",
	} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}

	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeDefaultOffNoteFile(t, filepath.Join(beadsDir, "metadata.json"), `{"database":"beads","backend":"dolt"}`)
	writeDefaultOffNoteFile(t, filepath.Join(beadsDir, "config.yaml"), "issue-prefix: test\n")
	statePath := filepath.Join(beadsDir, exportAutoStateFile)
	writeDefaultOffNoteFile(t, statePath, `{"last_dolt_commit":"abc","timestamp":"2026-05-01T00:00:00Z","issues":1,"memories":0,"last_diff_anchor":"abc"}`)
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	writeDefaultOffNoteFile(t, jsonlPath, `{"id":"test-1","title":"one"}`+"\n")

	t.Chdir(dir)
	t.Setenv("BEADS_DIR", beadsDir)
	ensureCleanGlobalState(t)
	saveAndRestoreGlobals(t)
	oldQuiet, oldJSON, oldHookJSON, oldGlobal := quietFlag, jsonOutput, primeHookJSONMode, globalFlag
	t.Cleanup(func() {
		quietFlag, jsonOutput, primeHookJSONMode, globalFlag = oldQuiet, oldJSON, oldHookJSON, oldGlobal
	})
	quietFlag, jsonOutput, primeHookJSONMode, globalFlag = false, false, false, false
	store = &defaultOffNoteStore{}

	return defaultOffNoteFixture{dir: dir, beadsDir: beadsDir, statePath: statePath, jsonlPath: jsonlPath}
}

func writeDefaultOffNoteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// wantDefaultOffNote spells the notice out rather than reading it from the
// implementation, so a change to the printed text fails here.
func wantDefaultOffNote(fullPath string) string {
	return "Info: auto-export is off by default since bd v1.0.5; " + fullPath + " is no longer refreshed automatically.\n" +
		"      Re-enable: bd config set export.auto true\n" +
		"      Keep off:  bd config set export.auto false  (silences this note)\n"
}

// runDefaultOffNoteCheck runs the post-command export check and returns what
// it wrote to stdout and stderr, both captured under one stdioMutex hold.
func runDefaultOffNoteCheck(t *testing.T) (stdout, stderr string) {
	t.Helper()
	stdioMutex.Lock()
	defer stdioMutex.Unlock()

	read := func(r *os.File) <-chan string {
		done := make(chan string, 1)
		go func() {
			var buf bytes.Buffer
			_, _ = io.Copy(&buf, r)
			done <- buf.String()
		}()
		return done
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outDone, errDone := read(outR), read(errR)
	oldStdout, oldStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	runErr := maybeAutoExport(context.Background(), false)
	os.Stdout, os.Stderr = oldStdout, oldStderr
	_ = outW.Close()
	_ = errW.Close()
	stdout, stderr = <-outDone, <-errDone
	_ = outR.Close()
	_ = errR.Close()
	if runErr != nil {
		t.Fatalf("maybeAutoExport: %v", runErr)
	}
	return stdout, stderr
}

// GH#4887: a workspace that auto-exported under the pre-v1.0.5 default and
// never set export.auto stops refreshing its JSONL after upgrade with no
// message. The post-command check must say so, naming the file and both
// commands that record an explicit choice.
func TestMaybeAutoExportNotesDefaultOffForPriorExporter(t *testing.T) {
	cases := []struct {
		name       string
		exportPath string // export.path in config.yaml; "" leaves it at the default
		global     bool
		jsonlName  string
	}{
		{name: "default path", jsonlName: "issues.jsonl"},
		{name: "configured export.path", exportPath: "custom/out.jsonl", jsonlName: "custom/out.jsonl"},
		{name: "empty export.path in global mode", exportPath: `""`, global: true, jsonlName: "global-issues.jsonl"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := defaultOffNoteWorkspace(t)
			if tc.exportPath != "" {
				writeDefaultOffNoteFile(t, filepath.Join(fx.beadsDir, "config.yaml"), "issue-prefix: test\nexport:\n  path: "+tc.exportPath+"\n")
			}
			if tc.jsonlName != "issues.jsonl" {
				if err := os.Remove(fx.jsonlPath); err != nil {
					t.Fatal(err)
				}
				writeDefaultOffNoteFile(t, filepath.Join(fx.beadsDir, tc.jsonlName), `{"id":"test-1"}`+"\n")
			}
			globalFlag = tc.global
			initConfigForTest(t)

			stdout, stderr := runDefaultOffNoteCheck(t)
			want := wantDefaultOffNote(filepath.Join(beads.FindBeadsDir(), tc.jsonlName))
			if stderr != want {
				t.Fatalf("stderr: want the default-off note byte for byte\nwant %q\ngot  %q", want, stderr)
			}
			if stdout != "" {
				t.Fatalf("stdout must stay empty, got %q", stdout)
			}
		})
	}
}
