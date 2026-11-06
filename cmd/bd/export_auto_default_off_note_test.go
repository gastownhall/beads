package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
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
// it wrote to stdout and stderr.
func runDefaultOffNoteCheck(t *testing.T) (stdout, stderr string) {
	t.Helper()
	return captureDefaultOffNoteOutput(t, func() error {
		return maybeAutoExport(context.Background(), false)
	})
}

// captureDefaultOffNoteOutput runs fn with stdout and stderr both captured
// under one stdioMutex hold, and fails the test if fn returns an error.
func captureDefaultOffNoteOutput(t *testing.T, fn func() error) (stdout, stderr string) {
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
	runErr := fn()
	os.Stdout, os.Stderr = oldStdout, oldStderr
	_ = outW.Close()
	_ = errW.Close()
	stdout, stderr = <-outDone, <-errDone
	_ = outR.Close()
	_ = errR.Close()
	if runErr != nil {
		t.Fatalf("export check: %v", runErr)
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

// The note only reports and keeps no memory of having been shown. Each
// invocation prints it again, and none changes export-state.json (whatever it
// holds), the JSONL, config.yaml, the .beads/ entries, or export.auto.
func TestMaybeAutoExportDefaultOffNoteLeavesStateUntouched(t *testing.T) {
	states := []struct{ name, content string }{
		{name: "current state", content: `{"v":1,"last_dolt_commit":"abc","timestamp":"2026-05-01T00:00:00Z","issues":1,"memories":0,"last_diff_anchor":"abc"}`},
		{name: "legacy unversioned state", content: `{"last_dolt_commit":"abc","timestamp":"2026-01-01T00:00:00Z"}`},
		{name: "unknown fields", content: `{"last_dolt_commit":"abc","future_field":{"x":[1,2]},"note_shown_at":"2026-05-01T00:00:00Z"}`},
		{name: "malformed state", content: "{not json\x00"},
	}
	for _, st := range states {
		t.Run(st.name, func(t *testing.T) {
			fx := defaultOffNoteWorkspace(t)
			writeDefaultOffNoteFile(t, fx.statePath, st.content)
			initConfigForTest(t)
			want := wantDefaultOffNote(filepath.Join(beads.FindBeadsDir(), "issues.jsonl"))

			before := snapshotDefaultOffNoteTree(t, fx.dir)
			for i := 1; i <= 2; i++ {
				stdout, stderr := runDefaultOffNoteCheck(t)
				if stderr != want {
					t.Fatalf("invocation %d: want the note every time\nwant %q\ngot  %q", i, want, stderr)
				}
				if stdout != "" {
					t.Fatalf("invocation %d: stdout must stay empty, got %q", i, stdout)
				}
				if after := snapshotDefaultOffNoteTree(t, fx.dir); !reflect.DeepEqual(after, before) {
					t.Fatalf("invocation %d changed the workspace:\nbefore %v\nafter  %v", i, before, after)
				}
				if config.GetBool("export.auto") {
					t.Fatalf("invocation %d: export.auto reads true, want false", i)
				}
				if src := config.GetValueSource("export.auto"); src != config.SourceDefault {
					t.Fatalf("invocation %d: export.auto source = %q, want %q", i, src, config.SourceDefault)
				}
			}
		})
	}
}

// snapshotDefaultOffNoteTree maps every file and directory under root to the
// sha256 of its contents ("dir" for directories).
func snapshotDefaultOffNoteTree(t *testing.T, root string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			snap[rel] = "dir"
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		snap[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// An explicit export.auto in any layer is a choice already made, and a
// workspace without both export artifacts has no JSONL going stale: neither
// gets the note.
func TestMaybeAutoExportDefaultOffNoteStaysQuiet(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, fx defaultOffNoteFixture)
		// afterInit runs once config is loaded; it must not change the
		// on-disk config.
		afterInit func(t *testing.T)
	}{
		{name: "project config.yaml false", setup: func(t *testing.T, fx defaultOffNoteFixture) {
			writeDefaultOffNoteFile(t, filepath.Join(fx.beadsDir, "config.yaml"), "issue-prefix: test\nexport:\n  auto: false\n")
		}},
		{name: "config.local.yaml false", setup: func(t *testing.T, fx defaultOffNoteFixture) {
			writeDefaultOffNoteFile(t, filepath.Join(fx.beadsDir, "config.local.yaml"), "export:\n  auto: false\n")
		}},
		{name: "user config false", setup: func(t *testing.T, fx defaultOffNoteFixture) {
			writeDefaultOffNoteFile(t, filepath.Join(fx.dir, "home", ".config", "bd", "config.yaml"), "export:\n  auto: false\n")
		}},
		{name: "runtime Set false", afterInit: func(t *testing.T) {
			config.Set("export.auto", false)
		}},
		{name: "BD_EXPORT_AUTO=false", setup: func(t *testing.T, _ defaultOffNoteFixture) {
			t.Setenv("BD_EXPORT_AUTO", "false")
		}},
		{name: "BEADS_EXPORT_AUTO=false", setup: func(t *testing.T, _ defaultOffNoteFixture) {
			t.Setenv("BEADS_EXPORT_AUTO", "false")
		}},
		{name: "BD_EXPORT_AUTO set empty", setup: func(t *testing.T, _ defaultOffNoteFixture) {
			t.Setenv("BD_EXPORT_AUTO", "")
		}},
		{name: "BEADS_EXPORT_AUTO set empty", setup: func(t *testing.T, _ defaultOffNoteFixture) {
			t.Setenv("BEADS_EXPORT_AUTO", "")
		}},
		{name: "no export-state.json", setup: func(t *testing.T, fx defaultOffNoteFixture) {
			removeDefaultOffNotePath(t, fx.statePath)
		}},
		{name: "no JSONL", setup: func(t *testing.T, fx defaultOffNoteFixture) {
			removeDefaultOffNotePath(t, fx.jsonlPath)
		}},
		{name: "export-state.json is a directory", setup: func(t *testing.T, fx defaultOffNoteFixture) {
			removeDefaultOffNotePath(t, fx.statePath)
			if err := os.Mkdir(fx.statePath, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "JSONL is a directory", setup: func(t *testing.T, fx defaultOffNoteFixture) {
			removeDefaultOffNotePath(t, fx.jsonlPath)
			if err := os.Mkdir(fx.jsonlPath, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := defaultOffNoteWorkspace(t)
			if tc.setup != nil {
				tc.setup(t, fx)
			}
			initConfigForTest(t)
			if tc.afterInit != nil {
				tc.afterInit(t)
			}
			if config.GetBool("export.auto") {
				t.Fatal("precondition: export.auto must read false so the check takes the disabled path")
			}

			before := snapshotDefaultOffNoteTree(t, fx.dir)
			stdout, stderr := runDefaultOffNoteCheck(t)
			if stderr != "" || stdout != "" {
				t.Fatalf("want no output, got stdout %q stderr %q", stdout, stderr)
			}
			if after := snapshotDefaultOffNoteTree(t, fx.dir); !reflect.DeepEqual(after, before) {
				t.Fatalf("the check changed the workspace:\nbefore %v\nafter  %v", before, after)
			}
		})
	}

	// An explicit true never reaches the disabled branch in maybeAutoExport;
	// the note itself must still treat it as a recorded choice.
	t.Run("explicit export.auto=true", func(t *testing.T) {
		fx := defaultOffNoteWorkspace(t)
		writeDefaultOffNoteFile(t, filepath.Join(fx.beadsDir, "config.yaml"), "issue-prefix: test\nexport:\n  auto: true\n")
		initConfigForTest(t)
		if !config.GetBool("export.auto") {
			t.Fatal("precondition: export.auto must read true")
		}
		stdout, stderr := captureDefaultOffNoteOutput(t, func() error {
			noteAutoExportDefaultOff()
			return nil
		})
		if stderr != "" || stdout != "" {
			t.Fatalf("want no output, got stdout %q stderr %q", stdout, stderr)
		}
	})
}

func removeDefaultOffNotePath(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

// The note is human-mode advice from a command that opened a store: quiet,
// JSON and hook-JSON output, a git hook, and a missing or closed store all
// keep it silent in an otherwise affected workspace.
func TestMaybeAutoExportDefaultOffNoteStaysQuietOutsideHumanOutput(t *testing.T) {
	cases := []struct {
		name  string
		apply func(t *testing.T)
	}{
		{name: "quiet", apply: func(t *testing.T) { quietFlag = true }},
		{name: "json", apply: func(t *testing.T) { jsonOutput = true }},
		{name: "prime hook-json", apply: func(t *testing.T) { primeHookJSONMode = true }},
		{name: "git hook", apply: func(t *testing.T) { t.Setenv("BD_GIT_HOOK", "1") }},
		{name: "no store", apply: func(t *testing.T) { store = nil }},
		{name: "closed store", apply: func(t *testing.T) { store = &defaultOffNoteStore{closed: true} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := defaultOffNoteWorkspace(t)
			initConfigForTest(t)
			tc.apply(t)

			before := snapshotDefaultOffNoteTree(t, fx.dir)
			stdout, stderr := runDefaultOffNoteCheck(t)
			if stderr != "" || stdout != "" {
				t.Fatalf("want no output, got stdout %q stderr %q", stdout, stderr)
			}
			if after := snapshotDefaultOffNoteTree(t, fx.dir); !reflect.DeepEqual(after, before) {
				t.Fatalf("the check changed the workspace:\nbefore %v\nafter  %v", before, after)
			}
		})
	}
}

// `bd config set export.auto ...` writes YAML without refreshing the loaded
// config, so in that same invocation export.auto still reads as the default.
// The note re-reads the workspace YAML and must already treat the new value
// as the user's choice. A YAML null stays unset, as bd's config reader has it.
func TestMaybeAutoExportDefaultOffNoteHonorsSameInvocationConfigWrite(t *testing.T) {
	cases := []struct {
		name     string
		write    func(t *testing.T, fx defaultOffNoteFixture)
		wantNote bool
	}{
		{name: "config set export.auto true", write: func(t *testing.T, _ defaultOffNoteFixture) {
			if err := config.SetYamlConfig("export.auto", "true"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "config set export.auto false", write: func(t *testing.T, _ defaultOffNoteFixture) {
			if err := config.SetYamlConfig("export.auto", "false"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "config.local.yaml written false", write: func(t *testing.T, fx defaultOffNoteFixture) {
			writeDefaultOffNoteFile(t, filepath.Join(fx.beadsDir, "config.local.yaml"), "export:\n  auto: false\n")
		}},
		{name: "config.yaml now unparseable", write: func(t *testing.T, fx defaultOffNoteFixture) {
			writeDefaultOffNoteFile(t, filepath.Join(fx.beadsDir, "config.yaml"), "export: [unclosed\n")
		}},
		{name: "null export section stays unset", wantNote: true, write: func(t *testing.T, fx defaultOffNoteFixture) {
			writeDefaultOffNoteFile(t, filepath.Join(fx.beadsDir, "config.yaml"), "issue-prefix: test\nexport:\n  # auto: true\n")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := defaultOffNoteWorkspace(t)
			initConfigForTest(t)
			tc.write(t, fx)
			if config.GetBool("export.auto") {
				t.Fatal("precondition: export.auto must still read false in memory")
			}
			if src := config.GetValueSource("export.auto"); src != config.SourceDefault {
				t.Fatalf("precondition: export.auto source = %q, want %q", src, config.SourceDefault)
			}

			stdout, stderr := runDefaultOffNoteCheck(t)
			want := ""
			if tc.wantNote {
				want = wantDefaultOffNote(filepath.Join(beads.FindBeadsDir(), "issues.jsonl"))
			}
			if stderr != want {
				t.Fatalf("stderr:\nwant %q\ngot  %q", want, stderr)
			}
			if stdout != "" {
				t.Fatalf("stdout must stay empty, got %q", stdout)
			}
		})
	}
}
