package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/debug"
)

// defaultOffNoteWorkspace makes a .beads/ dir, optionally with the
// export-state.json an earlier auto-export leaves behind, and points bd at it.
func defaultOffNoteWorkspace(t *testing.T, withExportState bool) {
	t.Helper()
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"database":"beads","backend":"dolt"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if withExportState {
		if err := os.WriteFile(filepath.Join(beadsDir, exportAutoStateFile), []byte(`{"last_dolt_commit":"abc","timestamp":"2026-05-01T00:00:00Z"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BD_EXPORT_AUTO", "")
	os.Unsetenv("BD_EXPORT_AUTO")
	t.Setenv("BEADS_EXPORT_AUTO", "")
	os.Unsetenv("BEADS_EXPORT_AUTO")
}

// GH#4887: a workspace that auto-exported under the pre-v1.0.5 default
// (export.auto=true) and never set the key stops exporting after upgrade
// with no message. maybeAutoExport must say so while the key is unset.
func TestMaybeAutoExportNotesDefaultOffForPriorExporter(t *testing.T) {
	defaultOffNoteWorkspace(t, true)
	initConfigForTest(t)
	debug.SetQuiet(false)

	out := captureStderr(t, func() {
		if err := maybeAutoExport(context.Background(), false); err != nil {
			t.Fatalf("maybeAutoExport: %v", err)
		}
	})
	// Spelled out rather than read from autoExportDefaultOffNote, so a change
	// to the printed text fails here.
	want := "Note: auto-export is off: export.auto defaults to false since v1.0.5, " +
		"but this workspace auto-exported before (.beads/export-state.json), so its JSONL is no longer updated.\n" +
		"  Resume:  bd config set export.auto true\n" +
		"  Silence: bd config set export.auto false\n"
	if out != want {
		t.Fatalf("want the default-off note on stderr, byte for byte:\nwant %q\ngot  %q", want, out)
	}
}

// The note only reports. Printing it must leave .beads/ exactly as it was:
// export-state.json byte-identical (same sha256), no JSONL written, no file
// added or removed, and export.auto still unset.
func TestMaybeAutoExportDefaultOffNoteLeavesStateUntouched(t *testing.T) {
	defaultOffNoteWorkspace(t, true)
	initConfigForTest(t)
	debug.SetQuiet(false)

	beadsDir := os.Getenv("BEADS_DIR")
	statePath := filepath.Join(beadsDir, exportAutoStateFile)
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	beforeSum := sha256.Sum256(before)
	beforeNames := dirNames(t, beadsDir)

	out := captureStderr(t, func() {
		if err := maybeAutoExport(context.Background(), false); err != nil {
			t.Fatalf("maybeAutoExport: %v", err)
		}
	})
	if out == "" {
		t.Fatal("precondition: the note should have printed")
	}

	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if afterSum := sha256.Sum256(after); afterSum != beforeSum || !bytes.Equal(after, before) {
		t.Fatalf("export-state.json changed: sha256 %x -> %x", beforeSum, afterSum)
	}
	if got := dirNames(t, beadsDir); !sameDirNames(got, beforeNames) {
		t.Fatalf(".beads/ entries changed: %v -> %v", beforeNames, got)
	}
	if _, err := os.Stat(filepath.Join(beadsDir, "issues.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("issues.jsonl must not be written while export.auto is off (stat err %v)", err)
	}
	if src := config.GetValueSource("export.auto"); src != config.SourceDefault {
		t.Fatalf("export.auto source = %q after the note, want %q", src, config.SourceDefault)
	}
}

func dirNames(t *testing.T, dir string) []string {
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

func sameDirNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestMaybeAutoExportDefaultOffNoteStaysQuiet(t *testing.T) {
	cases := []struct {
		name            string
		withExportState bool
		setExplicit     bool
		envExplicit     bool
		quiet           bool
	}{
		{name: "never auto-exported", withExportState: false},
		{name: "explicit export.auto=false", withExportState: true, setExplicit: true},
		{name: "explicit BD_EXPORT_AUTO=false", withExportState: true, envExplicit: true},
		{name: "quiet flag", withExportState: true, quiet: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defaultOffNoteWorkspace(t, tc.withExportState)
			if tc.envExplicit {
				t.Setenv("BD_EXPORT_AUTO", "false")
			}
			initConfigForTest(t)
			if tc.setExplicit {
				config.Set("export.auto", false)
			}
			debug.SetQuiet(tc.quiet)
			t.Cleanup(func() { debug.SetQuiet(false) })

			out := captureStderr(t, func() {
				if err := maybeAutoExport(context.Background(), false); err != nil {
					t.Fatalf("maybeAutoExport: %v", err)
				}
			})
			// Byte-identical to unpatched main: nothing at all on stderr.
			if out != "" {
				t.Fatalf("stderr must stay empty here, got %q", out)
			}
		})
	}
}
