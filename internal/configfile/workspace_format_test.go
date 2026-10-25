package configfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckWorkspaceFormatRefusesOtherModels(t *testing.T) {
	const beadsDir = "/work/repo/.beads"
	for _, mode := range []string{GraphModeLink, "future-format", "Dependency"} {
		t.Run(mode, func(t *testing.T) {
			err := CheckWorkspaceFormat(&Config{Backend: BackendDolt, GraphMode: mode}, beadsDir)
			var formatErr *UnsupportedWorkspaceFormatError
			if !errors.As(err, &formatErr) {
				t.Fatalf("CheckWorkspaceFormat() = %v, want *UnsupportedWorkspaceFormatError", err)
			}
			if formatErr.BeadsDir != beadsDir || formatErr.GraphMode != mode {
				t.Fatalf("refusal = %+v, want BeadsDir %q and GraphMode %q", formatErr, beadsDir, mode)
			}
			for _, want := range []string{beadsDir, `graph_mode "` + mode + `"`, "this bd build does not support", BackendNotOpenedGuarantee} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal %q does not mention %q", err, want)
				}
			}
			if hint := formatErr.Hint(); !strings.Contains(hint, `graph_mode "`+mode+`"`) || !strings.Contains(hint, "unchanged") {
				t.Errorf("hint %q does not name the mode or ask to keep the marker", hint)
			}
		})
	}
}

func TestCheckWorkspaceFormatAdmitsIssueWorkspaces(t *testing.T) {
	for name, cfg := range map[string]*Config{
		"no metadata":       nil,
		"absent marker":     {Backend: BackendDolt},
		"dependency marker": {Backend: BackendDolt, GraphMode: GraphModeDependency},
	} {
		t.Run(name, func(t *testing.T) {
			if err := CheckWorkspaceFormat(cfg, "/work/repo/.beads"); err != nil {
				t.Fatalf("CheckWorkspaceFormat() = %v, want admission", err)
			}
		})
	}
}

// TestCheckDiscoveredWorkspaceFormatLeavesWorkspaceUnchanged pins the
// pre-check every open runs before Load: it refuses a graph workspace marked
// in either metadata file without migrating a legacy config.json, and leaves
// a workspace with no metadata to the caller's own load.
func TestCheckDiscoveredWorkspaceFormatLeavesWorkspaceUnchanged(t *testing.T) {
	const metadata = `{"backend":"dolt","graph_mode":"link"}`
	for _, name := range []string{ConfigFileName, "config.json"} {
		t.Run(name, func(t *testing.T) {
			beadsDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(beadsDir, name), []byte(metadata), 0o600); err != nil {
				t.Fatal(err)
			}
			err := CheckDiscoveredWorkspaceFormat(beadsDir)
			var formatErr *UnsupportedWorkspaceFormatError
			if !errors.As(err, &formatErr) || formatErr.BeadsDir != beadsDir || formatErr.GraphMode != GraphModeLink {
				t.Fatalf("CheckDiscoveredWorkspaceFormat() = %v, want the link refusal for %s", err, beadsDir)
			}
			entries, err := os.ReadDir(beadsDir)
			if err != nil || len(entries) != 1 || entries[0].Name() != name {
				t.Fatalf("pre-check changed the workspace: %v, %v", entries, err)
			}
		})
	}
	if err := CheckDiscoveredWorkspaceFormat(t.TempDir()); err != nil {
		t.Fatalf("CheckDiscoveredWorkspaceFormat(no metadata) = %v, want nil", err)
	}
}

// TestCheckWorkspaceFormatRefusesFormatMarker pins the marker half of the
// check: a beadsDir holding GraphFormatMarkerFile is refused whatever its
// metadata says, with its own text and hint, while graph_mode still wins when
// both declare the format.
func TestCheckWorkspaceFormatRefusesFormatMarker(t *testing.T) {
	beadsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(beadsDir, GraphFormatMarkerFile), []byte("anything"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, cfg := range map[string]*Config{
		"no metadata":       nil,
		"stripped metadata": {Backend: BackendDolt},
		"dependency marker": {Backend: BackendDolt, GraphMode: GraphModeDependency},
	} {
		t.Run(name, func(t *testing.T) {
			err := CheckWorkspaceFormat(cfg, beadsDir)
			var formatErr *UnsupportedWorkspaceFormatError
			if !errors.As(err, &formatErr) || !formatErr.FormatMarker || formatErr.BeadsDir != beadsDir || formatErr.GraphMode != "" {
				t.Fatalf("CheckWorkspaceFormat() = %#v, want the format marker refusal", err)
			}
			wantErr := "workspace " + beadsDir + " has a graph-preview-format marker, which this bd build does not support; " + BackendNotOpenedGuarantee
			if err.Error() != wantErr {
				t.Errorf("refusal = %q, want %q", err, wantErr)
			}
			wantHint := "restore metadata.json (graph_mode and the graph_* fields) from version control or a backup, then use a bd build that supports graph workspaces; do not remove .beads/graph-preview-format"
			if formatErr.Hint() != wantHint {
				t.Errorf("hint = %q, want %q", formatErr.Hint(), wantHint)
			}
		})
	}
	err := CheckWorkspaceFormat(&Config{Backend: BackendDolt, GraphMode: GraphModeLink}, beadsDir)
	var formatErr *UnsupportedWorkspaceFormatError
	if !errors.As(err, &formatErr) || formatErr.FormatMarker || formatErr.GraphMode != GraphModeLink {
		t.Fatalf("CheckWorkspaceFormat(link beside marker) = %#v, want the graph_mode refusal", err)
	}
	if err := CheckWorkspaceFormat(nil, ""); err != nil {
		t.Fatalf("CheckWorkspaceFormat(nil, \"\") = %v, want admission without probing the working directory", err)
	}
}

// TestCheckDiscoveredWorkspaceFormatRefusesFormatMarker pins that the
// pre-check refuses a marked workspace with no, stripped or unparseable
// metadata, and still leaves unparseable metadata without the marker to the
// caller's own load.
func TestCheckDiscoveredWorkspaceFormatRefusesFormatMarker(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"marker only":                    {GraphFormatMarkerFile: ""},
		"stripped metadata.json":         {GraphFormatMarkerFile: "x", ConfigFileName: `{"backend":"dolt"}`},
		"stripped legacy config.json":    {GraphFormatMarkerFile: "x", "config.json": `{"backend":"dolt"}`},
		"unparseable metadata.json":      {GraphFormatMarkerFile: "x", ConfigFileName: `{"backend":`},
		"unparseable legacy config.json": {GraphFormatMarkerFile: "x", "config.json": `{"backend":`},
	} {
		t.Run(name, func(t *testing.T) {
			beadsDir := t.TempDir()
			for file, content := range files {
				if err := os.WriteFile(filepath.Join(beadsDir, file), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := CheckDiscoveredWorkspaceFormat(beadsDir)
			var formatErr *UnsupportedWorkspaceFormatError
			if !errors.As(err, &formatErr) || !formatErr.FormatMarker || formatErr.BeadsDir != beadsDir {
				t.Fatalf("CheckDiscoveredWorkspaceFormat() = %v, want the format marker refusal", err)
			}
			if _, err := LoadAndCheckWorkspaceFormat(beadsDir); !errors.As(err, &formatErr) || !formatErr.FormatMarker {
				t.Fatalf("LoadAndCheckWorkspaceFormat() = %v, want the format marker refusal", err)
			}
			entries, err := os.ReadDir(beadsDir)
			if err != nil || len(entries) != len(files) {
				t.Fatalf("pre-check changed the workspace: %v, %v", entries, err)
			}
		})
	}
	unparseable := t.TempDir()
	if err := os.WriteFile(filepath.Join(unparseable, ConfigFileName), []byte(`{"backend":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckDiscoveredWorkspaceFormat(unparseable); err != nil {
		t.Fatalf("CheckDiscoveredWorkspaceFormat(unparseable, no marker) = %v, want nil", err)
	}
	if _, err := LoadAndCheckWorkspaceFormat(unparseable); err == nil || !strings.Contains(err.Error(), "loading config") {
		t.Fatalf("LoadAndCheckWorkspaceFormat(unparseable, no marker) = %v, want the load failure", err)
	}
}
