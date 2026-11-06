package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
)

// GH#4887: export.auto became opt-in in #4063 (v1.0.5). A workspace that
// auto-exported under the old default has export-state.json and issues.jsonl
// but no export.auto key, so maybeAutoExport returns at its export.auto check
// and the JSONL goes stale with no explanation. These tests pin the
// default-off notice: when the artifacts say this workspace used to
// auto-export, an eligible human-output command says so and names both
// commands that resolve the implicit choice.
//
// Untagged on purpose (cmd/bd/export_auto_test.go is cgo-tagged): everything
// here runs the disabled export path, which needs no database. Not parallel —
// the fixture swaps process globals (store, output flags) and os.Stderr.

// noteCheckStore is the minimal open store the disabled export path needs.
// Every DoltStorage method is the nil embedded interface, so any accidental
// database call on this path panics and fails the test. GetStateHash lets the
// explicitly-true row run the real enabled flow as far as its change-detection
// early return without any other store work.
type noteCheckStore struct {
	storage.DoltStorage
	stateHash string
}

func (noteCheckStore) IsClosed() bool { return false }

func (s noteCheckStore) GetStateHash(context.Context) (string, error) {
	return s.stateHash, nil
}

// defaultOffNoteFixture is the affected GH#4887 workspace: .beads/ holds both
// prior auto-export artifacts, export.auto is unset everywhere (viper at
// SourceDefault, no env var, nothing on disk), config is isolated, and an
// open stub store is installed.
type defaultOffNoteFixture struct {
	dir      string
	beadsDir string
}

// defaultOffNoteStateJSON seeds export-state.json. Its contents are
// irrelevant to the notice (metadata only) but shaped like a real state file.
const defaultOffNoteStateJSON = `{"last_dolt_commit":"note-check-hash","last_diff_anchor":"abc123","timestamp":"2026-01-01T00:00:00Z","issues":3,"memories":0}`

// defaultOffNoteJSONL seeds the export JSONL. One prior-exported issue; the
// notice never reads it, only stats it.
const defaultOffNoteJSONL = `{"_type":"issue","id":"bd-note-1","issue_type":"task","title":"prior export"}`

// newDefaultOffNoteFixture builds the affected workspace around configYAML
// (the .beads/config.yaml contents) with the export JSONL seeded at
// jsonlRelPath relative to .beads/.
func newDefaultOffNoteFixtureWithConfig(t *testing.T, configYAML, jsonlRelPath string) *defaultOffNoteFixture {
	t.Helper()

	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	beadsDir := filepath.Join(dir, ".beads")
	for _, p := range []string{home, beadsDir} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f := &defaultOffNoteFixture{dir: dir, beadsDir: beadsDir}

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	// Unset rather than empty: an exported-but-empty BD_EXPORT_AUTO still
	// counts as "set" for GetValueSource and would silence the notice for
	// the wrong reason. Inherited values are restored at cleanup.
	for _, name := range []string{"BD_EXPORT_AUTO", "BEADS_EXPORT_AUTO", "BD_GIT_HOOK"} {
		noteTestUnsetenv(t, name)
	}

	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, exportAutoStateFile), []byte(defaultOffNoteStateJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	jsonlPath := filepath.Join(beadsDir, jsonlRelPath)
	if err := os.MkdirAll(filepath.Dir(jsonlPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jsonlPath, []byte(defaultOffNoteJSONL), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Chdir(dir)
	t.Setenv("BEADS_DIR", beadsDir)
	initConfigForTest(t)

	// Human-output defaults; no command context so isQuiet() reads globals.
	resetCommandContext()
	origJSON, origQuiet, origHookJSON, origGlobal := jsonOutput, quietFlag, primeHookJSONMode, globalFlag
	jsonOutput, quietFlag, primeHookJSONMode, globalFlag = false, false, false, false
	t.Cleanup(func() {
		jsonOutput, quietFlag, primeHookJSONMode, globalFlag = origJSON, origQuiet, origHookJSON, origGlobal
	})

	saveAndRestoreGlobals(t)
	store = noteCheckStore{stateHash: "note-check-hash"}
	return f
}

func newDefaultOffNoteFixture(t *testing.T) *defaultOffNoteFixture {
	t.Helper()
	return newDefaultOffNoteFixtureWithConfig(t, "{}\n", "issues.jsonl")
}

// noteTestUnsetenv removes name from the environment for this test and
// restores any inherited value at cleanup. t.Setenv cannot unset.
func noteTestUnsetenv(t *testing.T, name string) {
	t.Helper()
	val, ok := os.LookupEnv(name)
	if !ok {
		return
	}
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv(name, val) })
}

// captureNoteOutput runs fn with os.Stdout and os.Stderr captured and returns
// both, apart. One redirection under stdioMutex — calling captureStdout inside
// captureStderr would deadlock on the same non-reentrant mutex.
func captureNoteOutput(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	stdioMutex.Lock()
	defer stdioMutex.Unlock()

	oldOut, oldErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}

	var outBuf, errBuf bytes.Buffer
	outDone, errDone := make(chan struct{}), make(chan struct{})
	go func() { _, _ = io.Copy(&outBuf, outR); close(outDone) }()
	go func() { _, _ = io.Copy(&errBuf, errR); close(errDone) }()

	os.Stdout, os.Stderr = outW, errW
	// restore drains both pipes before anyone reads the buffers — reading a
	// bytes.Buffer io.Copy is still appending to is a data race, and a
	// return statement reads its operands before defers run. Idempotent so
	// the deferred copy is safe for the t.Fatalf-inside-fn path.
	restored := false
	restore := func() {
		if restored {
			return
		}
		restored = true
		_ = outW.Close()
		_ = errW.Close()
		os.Stdout, os.Stderr = oldOut, oldErr
		<-outDone
		<-errDone
		_ = outR.Close()
		_ = errR.Close()
	}
	defer restore()

	fn()
	restore()
	return outBuf.String(), errBuf.String()
}

// noteAutoExportDefaultOffText is the exact notice line for fullPath.
func noteAutoExportDefaultOffText(fullPath string) string {
	return "Info: auto-export is off by default since bd v1.0.5; " + fullPath +
		" is no longer refreshed automatically. Re-enable with: bd config set export.auto true" +
		" (keep it off and silence this note: bd config set export.auto false)\n"
}

// TestMaybeAutoExportNotesDefaultOffForPriorExporter pins AC-1: an affected
// workspace (default-sourced export.auto, no explicit on-disk setting, both
// prior export artifacts, open store, human output) gets exactly one stderr
// notice naming the v1.0.5 default, the resolved JSONL path and both
// configuration commands — with stdout and the successful result unchanged.
func TestMaybeAutoExportNotesDefaultOffForPriorExporter(t *testing.T) {
	t.Run("default path", func(t *testing.T) {
		f := newDefaultOffNoteFixture(t)

		var runErr error
		stdout, stderr := captureNoteOutput(t, func() {
			runErr = maybeAutoExport(context.Background(), false)
		})

		if runErr != nil {
			t.Fatalf("maybeAutoExport error = %v, want nil", runErr)
		}
		want := noteAutoExportDefaultOffText(filepath.Join(f.beadsDir, "issues.jsonl"))
		if stderr != want {
			t.Fatalf("stderr = %q, want exactly the default-off notice %q", stderr, want)
		}
		if stdout != "" {
			t.Fatalf("stdout = %q, want empty (the notice must not touch stdout or the command result)", stdout)
		}
	})

	t.Run("custom export path", func(t *testing.T) {
		f := newDefaultOffNoteFixtureWithConfig(t, "export:\n  path: custom-export.jsonl\n", "custom-export.jsonl")

		var runErr error
		stdout, stderr := captureNoteOutput(t, func() {
			runErr = maybeAutoExport(context.Background(), false)
		})

		if runErr != nil {
			t.Fatalf("maybeAutoExport error = %v, want nil", runErr)
		}
		want := noteAutoExportDefaultOffText(filepath.Join(f.beadsDir, "custom-export.jsonl"))
		if stderr != want {
			t.Fatalf("stderr = %q, want the notice naming the configured path %q", stderr, want)
		}
		if stdout != "" {
			t.Fatalf("stdout = %q, want empty", stdout)
		}
	})

	t.Run("empty configured path falls back by global flag", func(t *testing.T) {
		f := newDefaultOffNoteFixtureWithConfig(t, "export:\n  path: \"\"\n", "global-issues.jsonl")
		globalFlag = true

		var runErr error
		stdout, stderr := captureNoteOutput(t, func() {
			runErr = maybeAutoExport(context.Background(), false)
		})

		if runErr != nil {
			t.Fatalf("maybeAutoExport error = %v, want nil", runErr)
		}
		want := noteAutoExportDefaultOffText(filepath.Join(f.beadsDir, "global-issues.jsonl"))
		if stderr != want {
			t.Fatalf("stderr = %q, want the notice naming the global fallback path %q", stderr, want)
		}
		if stdout != "" {
			t.Fatalf("stdout = %q, want empty", stdout)
		}
	})

	// The notice must name both commands verbatim so the line stays greppable
	// and copy-pasteable even if the surrounding wording is polished later.
	if !strings.Contains(noteAutoExportDefaultOffText("/x/issues.jsonl"), "bd config set export.auto true") ||
		!strings.Contains(noteAutoExportDefaultOffText("/x/issues.jsonl"), "bd config set export.auto false") {
		t.Fatal("notice wording must retain both complete configuration commands")
	}
}

// noteTestDirEntries lists the sorted entry names of dir.
func noteTestDirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// TestMaybeAutoExportDefaultOffNoteLeavesStateUntouched pins AC-2: repeated
// eligible invocations each print the notice while every byte on disk —
// export-state.json, the JSONL, the config, the .beads directory entries —
// stays identical, and export.auto keeps resolving false from SourceDefault.
// This is also the proof the notice carries no hidden one-shot or throttle
// state: a persisted marker would have to appear somewhere under .beads, and
// no entry may appear or change.
func TestMaybeAutoExportDefaultOffNoteLeavesStateUntouched(t *testing.T) {
	// The notice only stats the state file, so odd contents must be preserved
	// verbatim: a legacy pre-versioning state file, one carrying unknown
	// fields from a newer binary, and unparseable bytes.
	stateVariants := map[string]string{
		"legacy state":     `{"last_dolt_commit":"abc123","timestamp":"2025-06-01T00:00:00Z","issues":2}`,
		"unknown fields":   `{"v":99,"last_dolt_commit":"abc","future_field":{"a":[1,2]},"issues":2}`,
		"malformed state":  `{"last_dolt_commit":"abc123", not json`,
	}
	for name, stateJSON := range stateVariants {
		t.Run(name, func(t *testing.T) {
			f := newDefaultOffNoteFixture(t)
			statePath := filepath.Join(f.beadsDir, exportAutoStateFile)
			if err := os.WriteFile(statePath, []byte(stateJSON), 0o600); err != nil {
				t.Fatal(err)
			}
			jsonlPath := filepath.Join(f.beadsDir, "issues.jsonl")
			configPath := filepath.Join(f.beadsDir, "config.yaml")

			before := map[string]string{
				"state":  noteTestFileHash(t, statePath),
				"jsonl":  noteTestFileHash(t, jsonlPath),
				"config": noteTestFileHash(t, configPath),
			}
			beforeEntries := noteTestDirEntries(t, f.beadsDir)

			wantNote := noteAutoExportDefaultOffText(jsonlPath)
			for i := 1; i <= 2; i++ {
				var runErr error
				stdout, stderr := captureNoteOutput(t, func() {
					runErr = maybeAutoExport(context.Background(), false)
				})
				if runErr != nil {
					t.Fatalf("invocation %d: maybeAutoExport error = %v, want nil", i, runErr)
				}
				if stderr != wantNote {
					t.Fatalf("invocation %d: stderr = %q, want the notice again (repeats until an explicit choice is recorded): %q", i, stderr, wantNote)
				}
				if stdout != "" {
					t.Fatalf("invocation %d: stdout = %q, want empty", i, stdout)
				}
			}

			for path, want := range before {
				var got string
				switch path {
				case "state":
					got = noteTestFileHash(t, statePath)
				case "jsonl":
					got = noteTestFileHash(t, jsonlPath)
				case "config":
					got = noteTestFileHash(t, configPath)
				}
				if got != want {
					t.Fatalf("%s changed: sha256 before %s after %s — the notice must not write while export is off", path, want, got)
				}
			}
			if after := noteTestDirEntries(t, f.beadsDir); !slicesEqual(after, beforeEntries) {
				t.Fatalf(".beads entries changed: before %v after %v — no suppression marker may be persisted", beforeEntries, after)
			}
			if config.GetBool("export.auto") {
				t.Fatal("export.auto resolved true; want the default false")
			}
			if src := config.GetValueSource("export.auto"); src != config.SourceDefault {
				t.Fatalf("export.auto source = %v, want SourceDefault after the notices", src)
			}
		})
	}
}

// noteTestFileHash returns the sha256 of the file at path, hex-encoded.
func noteTestFileHash(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// noteTestRunQuiet runs maybeAutoExport in workspace f and asserts it stays
// quiet: nil error, empty stderr, empty stdout.
func noteTestRunQuiet(t *testing.T, f *defaultOffNoteFixture) {
	t.Helper()
	var runErr error
	stdout, stderr := captureNoteOutput(t, func() {
		runErr = maybeAutoExport(context.Background(), false)
	})
	if runErr != nil {
		t.Fatalf("maybeAutoExport error = %v, want nil", runErr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want no default-off notice", stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
}

// TestMaybeAutoExportDefaultOffNoteStaysQuiet pins AC-3 through AC-5: every
// workspace, configuration or output context that already answers the
// export.auto question — or is not the notice's audience — gets no notice.
// The control subtest first proves the shared fixture does produce one, so
// the quiet rows can never pass vacuously while the notice is broken.
func TestMaybeAutoExportDefaultOffNoteStaysQuiet(t *testing.T) {
	t.Run("control notice fires in the affected workspace", func(t *testing.T) {
		f := newDefaultOffNoteFixture(t)
		var runErr error
		stdout, stderr := captureNoteOutput(t, func() {
			runErr = maybeAutoExport(context.Background(), false)
		})
		if runErr != nil {
			t.Fatalf("maybeAutoExport error = %v, want nil", runErr)
		}
		if stderr != noteAutoExportDefaultOffText(filepath.Join(f.beadsDir, "issues.jsonl")) {
			t.Fatalf("control: stderr = %q, want the notice (the quiet rows below are only meaningful while it fires)", stderr)
		}
		if stdout != "" {
			t.Fatalf("control: stdout = %q, want empty", stdout)
		}
	})

	rows := []struct {
		name  string
		setup func(t *testing.T) *defaultOffNoteFixture
	}{
		{
			name: "explicit false in project config",
			setup: func(t *testing.T) *defaultOffNoteFixture {
				return newDefaultOffNoteFixtureWithConfig(t, "export:\n  auto: false\n", "issues.jsonl")
			},
		},
		{
			name: "explicit false in local config",
			setup: func(t *testing.T) *defaultOffNoteFixture {
				f := newDefaultOffNoteFixture(t)
				if err := os.WriteFile(filepath.Join(f.beadsDir, "config.local.yaml"), []byte("export:\n  auto: false\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				initConfigForTest(t) // re-merge the local layer into viper
				return f
			},
		},
		{
			name: "explicit false in user config",
			setup: func(t *testing.T) *defaultOffNoteFixture {
				f := newDefaultOffNoteFixture(t)
				userCfgDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "bd")
				if err := os.MkdirAll(userCfgDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(userCfgDir, "config.yaml"), []byte("export:\n  auto: false\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				initConfigForTest(t) // re-merge the user layer into viper
				return f
			},
		},
		{
			name: "explicit false via runtime Set",
			setup: func(t *testing.T) *defaultOffNoteFixture {
				f := newDefaultOffNoteFixture(t)
				config.Set("export.auto", false)
				return f
			},
		},
		{
			name: "explicit false via BD_EXPORT_AUTO",
			setup: func(t *testing.T) *defaultOffNoteFixture {
				f := newDefaultOffNoteFixture(t)
				t.Setenv("BD_EXPORT_AUTO", "false")
				return f
			},
		},
		{
			name: "explicit false via BEADS_EXPORT_AUTO",
			setup: func(t *testing.T) *defaultOffNoteFixture {
				f := newDefaultOffNoteFixture(t)
				t.Setenv("BEADS_EXPORT_AUTO", "false")
				return f
			},
		},
		{
			// LookupEnv semantics: an exported-but-empty variable still counts
			// as the user answering, so it must not fall through to the
			// default the notice describes.
			name: "set-but-empty BD_EXPORT_AUTO",
			setup: func(t *testing.T) *defaultOffNoteFixture {
				f := newDefaultOffNoteFixture(t)
				t.Setenv("BD_EXPORT_AUTO", "")
				return f
			},
		},
		{
			name: "set-but-empty BEADS_EXPORT_AUTO",
			setup: func(t *testing.T) *defaultOffNoteFixture {
				f := newDefaultOffNoteFixture(t)
				t.Setenv("BEADS_EXPORT_AUTO", "")
				return f
			},
		},
		{
			// Explicit true runs the real enabled flow (change detection says
			// "no changes since last export" against the stub state hash and
			// returns before any store work): no notice, no error — the
			// explicit-enabled path is unchanged by the notice change.
			name: "explicit true keeps the enabled export flow quiet",
			setup: func(t *testing.T) *defaultOffNoteFixture {
				f := newDefaultOffNoteFixture(t)
				config.Set("export.auto", true)
				return f
			},
		},
		{
			name: "no export-state.json",
			setup: func(t *testing.T) *defaultOffNoteFixture {
				f := newDefaultOffNoteFixture(t)
				noteTestRemove(t, filepath.Join(f.beadsDir, exportAutoStateFile))
				return f
			},
		},
		{
			name: "no export JSONL",
			setup: func(t *testing.T) *defaultOffNoteFixture {
				f := newDefaultOffNoteFixture(t)
				noteTestRemove(t, filepath.Join(f.beadsDir, "issues.jsonl"))
				return f
			},
		},
		{
			name: "export-state.json is a directory",
			setup: func(t *testing.T) *defaultOffNoteFixture {
				f := newDefaultOffNoteFixture(t)
				p := filepath.Join(f.beadsDir, exportAutoStateFile)
				noteTestRemove(t, p)
				noteTestMkdir(t, p)
				return f
			},
		},
		{
			name: "export JSONL is a directory",
			setup: func(t *testing.T) *defaultOffNoteFixture {
				f := newDefaultOffNoteFixture(t)
				p := filepath.Join(f.beadsDir, "issues.jsonl")
				noteTestRemove(t, p)
				noteTestMkdir(t, p)
				return f
			},
		},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			f := row.setup(t)
			noteTestRunQuiet(t, f)
		})
	}
}

func noteTestRemove(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func noteTestMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}
