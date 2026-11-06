package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
