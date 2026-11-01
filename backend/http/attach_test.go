package bdhttp_test

// Written fresh for OSS beads S6: pins the bee-ghosttrack CHANGES_REQUESTED
// finding on #7288, should-fix 1 — `bd connect --clear` restoring a
// workspace's previous backend selection needs Attach to have recorded what
// that previous backend WAS, onto the sidecar, at connect time.

import (
	"context"
	"errors"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	bdhttp "github.com/steveyegge/beads/backend/http"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/httpclient"
	"github.com/steveyegge/beads/internal/testutil/legacyworkspace"
)

func mustAttachTestURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", raw, err)
	}
	return u
}

// TestAttachRecordsThePriorBackendAsPreviousBackend is the first-connect
// case: a workspace that selected "dolt" before ever running `bd connect`
// must have Attach record "dolt" as PreviousBackend, so --clear can restore
// it later.
func TestAttachRecordsThePriorBackendAsPreviousBackend(t *testing.T) {
	beadsDir := t.TempDir()
	cfg := &configfile.Config{Backend: "dolt", Database: "beads.db"}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("seed metadata.json: %v", err)
	}

	target := bdhttp.Target{BaseURL: mustAttachTestURL(t, "http://127.0.0.1:8080")}
	if err := bdhttp.Attach(beadsDir, target); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	got, err := bdhttp.LoadTarget(beadsDir)
	if err != nil {
		t.Fatalf("LoadTarget: %v", err)
	}
	if got.PreviousBackend != "dolt" {
		t.Errorf("PreviousBackend = %q, want %q", got.PreviousBackend, "dolt")
	}

	after, err := configfile.Load(beadsDir)
	if err != nil {
		t.Fatalf("reload metadata.json: %v", err)
	}
	// Asserts on the raw stored field, not GetBackend(): GetBackend() falls
	// back to BackendDolt for any name backendnames.Has doesn't recognize
	// (TestGetBackendAllowlist in internal/configfile), and "http" is only
	// ever added there by bdhttp.Register/httpclient.Register — a
	// once-per-process, panics-on-duplicate production wiring call this test
	// package must not make. The raw field is exactly what Attach controls.
	if after.Backend != httpclient.Backend {
		t.Errorf("metadata.json backend = %q after Attach, want %q", after.Backend, httpclient.Backend)
	}
}

// TestAttachOnAFreshWorkspaceRecordsTheImplicitDoltDefault covers a
// workspace with no metadata.json at all: GetBackend()'s own default
// ("dolt") is what a future --clear restores to, matching what every other
// backend-selection read in this codebase already treats as this
// workspace's backend.
func TestAttachOnAFreshWorkspaceRecordsTheImplicitDoltDefault(t *testing.T) {
	beadsDir := t.TempDir()
	target := bdhttp.Target{BaseURL: mustAttachTestURL(t, "http://127.0.0.1:8080")}
	if err := bdhttp.Attach(beadsDir, target); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	got, err := bdhttp.LoadTarget(beadsDir)
	if err != nil {
		t.Fatalf("LoadTarget: %v", err)
	}
	if got.PreviousBackend != "dolt" {
		t.Errorf("PreviousBackend = %q, want %q (configfile.BackendDolt)", got.PreviousBackend, "dolt")
	}
}

// TestAttachOnAReconnectKeepsTheEarlierPreviousBackend covers a workspace
// ALREADY on http (a bare re-connect, or --force re-pinning the same
// backend): Attach must not overwrite an already-recorded PreviousBackend
// with "http" itself, or a chain of reconnects would forget what --clear is
// supposed to restore.
func TestAttachOnAReconnectKeepsTheEarlierPreviousBackend(t *testing.T) {
	beadsDir := t.TempDir()
	// Not dolt: an unregistered "http" reads back through GetBackend() as the
	// dolt default, so a dolt prior could not tell carrying it forward from
	// recording that default.
	cfg := &configfile.Config{Backend: "postgres", Database: "beads.db"}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("seed metadata.json: %v", err)
	}

	first := bdhttp.Target{BaseURL: mustAttachTestURL(t, "http://127.0.0.1:8080")}
	if err := bdhttp.Attach(beadsDir, first); err != nil {
		t.Fatalf("first Attach: %v", err)
	}

	// A second connect to a different url, same workspace, same backend
	// ("http", already selected) — the shape `bd connect --force` to a new
	// server produces for an already-http workspace.
	second := bdhttp.Target{BaseURL: mustAttachTestURL(t, "http://127.0.0.1:9090")}
	if err := bdhttp.Attach(beadsDir, second); err != nil {
		t.Fatalf("second Attach: %v", err)
	}

	got, err := bdhttp.LoadTarget(beadsDir)
	if err != nil {
		t.Fatalf("LoadTarget: %v", err)
	}
	if got.PreviousBackend != "postgres" {
		t.Errorf("PreviousBackend after a reconnect = %q, want the original %q carried forward, not the just-left \"http\"", got.PreviousBackend, "postgres")
	}
	if got.BaseURL.String() != "http://127.0.0.1:9090" {
		t.Errorf("BaseURL = %q, want the second connect's url", got.BaseURL.String())
	}
}

// TestAttachHonorsACallerSuppliedPreviousBackend covers Connect's doc
// contract: a caller that already set target.PreviousBackend itself (none
// of this package's own callers do today) must have that value win over
// Attach's own inference.
func TestAttachHonorsACallerSuppliedPreviousBackend(t *testing.T) {
	beadsDir := t.TempDir()
	cfg := &configfile.Config{Backend: "dolt", Database: "beads.db"}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatalf("seed metadata.json: %v", err)
	}

	target := bdhttp.Target{BaseURL: mustAttachTestURL(t, "http://127.0.0.1:8080"), PreviousBackend: "mysql"}
	if err := bdhttp.Attach(beadsDir, target); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	got, err := bdhttp.LoadTarget(beadsDir)
	if err != nil {
		t.Fatalf("LoadTarget: %v", err)
	}
	if got.PreviousBackend != "mysql" {
		t.Errorf("PreviousBackend = %q, want the caller-supplied %q to win over the inferred %q", got.PreviousBackend, "mysql", "dolt")
	}
}

// TestAttachGitignoresBothPerUserFiles pins Attach's .gitignore guarantee for
// both files this backend keeps per user in beadsDir: the sidecar Attach
// writes, and the local metadata file the store writes later, on first use.
// `bd connect` also covers the second through doctor's required patterns,
// but an embedder activating through Attach alone has only this. A reconnect
// must not duplicate either line.
func TestAttachGitignoresBothPerUserFiles(t *testing.T) {
	beadsDir := t.TempDir()
	for _, raw := range []string{"http://127.0.0.1:8080", "http://127.0.0.1:9090"} {
		if err := bdhttp.Attach(beadsDir, bdhttp.Target{BaseURL: mustAttachTestURL(t, raw)}); err != nil {
			t.Fatalf("Attach(%s): %v", raw, err)
		}
	}
	content, err := os.ReadFile(filepath.Join(beadsDir, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore after Attach: %v", err)
	}
	for _, name := range []string{httpclient.TargetFileName, httpclient.LocalMetadataFileName} {
		count := 0
		for _, line := range strings.Split(string(content), "\n") {
			if strings.TrimSpace(line) == name {
				count++
			}
		}
		if count != 1 {
			t.Errorf(".gitignore names %s %d time(s) after two Attaches, want exactly 1:\n%s", name, count, content)
		}
	}
}

// workspaceTreeDigest maps every path under root to "dir" or its file's
// content, so a refusal can be checked to have left the workspace, and
// anything beside it, byte-identical.
func workspaceTreeDigest(t *testing.T, root string) map[string]string {
	t.Helper()
	digest := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			digest[rel] = "dir"
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest[rel] = "file:" + string(content)
		return nil
	})
	if err != nil {
		t.Fatalf("digest %s: %v", root, err)
	}
	return digest
}

// activationDoors are the two public calls that activate a workspace for this
// backend. Connect handshakes with server; Attach never dials.
var activationDoors = map[string]func(t *testing.T, beadsDir string, server *recordingServer) error{
	"Attach": func(t *testing.T, beadsDir string, server *recordingServer) error {
		return bdhttp.Attach(beadsDir, server.target(t))
	},
	"Connect": func(t *testing.T, beadsDir string, server *recordingServer) error {
		_, err := bdhttp.Connect(context.Background(), beadsDir, server.target(t), bdhttp.Options{HTTPClient: server.Client()})
		return err
	},
}

// workspaceWriters are the public calls that write this backend's files into
// a workspace: the activationDoors, and SaveTarget, which writes the sidecar
// alone and so cannot select the backend, but still writes the workspace.
func workspaceWriters() map[string]func(t *testing.T, beadsDir string, server *recordingServer) error {
	writers := maps.Clone(activationDoors)
	writers["SaveTarget"] = func(t *testing.T, beadsDir string, server *recordingServer) error {
		return bdhttp.SaveTarget(beadsDir, server.target(t))
	}
	return writers
}

// TestAttachConnectAndSaveTargetRefuseAGraphWorkspace is this backend's half
// of the graph workspace fence. Activation writes the sidecar, .gitignore and
// metadata.json's backend selection, and its metadata read migrates a legacy
// config.json, so a workspace whose metadata or graph-preview-format marker
// declares a format this build cannot open is refused before any of that, as
// every open and `bd connect` refuse it; SaveTarget, which an embedder can
// call alone, refuses it before writing the sidecar. Connect refuses before
// it dials: a handshake for a workspace it will not attach would only hand
// the server a credential.
func TestAttachConnectAndSaveTargetRefuseAGraphWorkspace(t *testing.T) {
	hermeticEnv(t)
	const marker = "link-preview-v9\n"
	layouts := []struct {
		name  string
		files map[string]string
		// mode and marker are the refusal's GraphMode and FormatMarker.
		mode   string
		marker bool
	}{
		{name: "graph_mode link", mode: "link", files: map[string]string{
			configfile.ConfigFileName: `{"backend":"dolt","dolt_mode":"server","graph_mode":"link","graph_workspace":"w1","graph_schema_version":3,"graph_ready":true}`,
		}},
		{name: "unknown graph_mode", mode: "future-format", files: map[string]string{
			configfile.ConfigFileName: `{"backend":"dolt","graph_mode":"future-format"}`,
		}},
		{name: "graph_mode in legacy config.json", mode: "link", files: map[string]string{
			"config.json": `{"backend":"dolt","dolt_mode":"server","graph_mode":"link"}`,
		}},
		{name: "marker only", marker: true, files: map[string]string{
			configfile.GraphFormatMarkerFile: marker,
		}},
		{name: "marker beside stripped metadata", marker: true, files: map[string]string{
			configfile.ConfigFileName:        `{"backend":"dolt","dolt_mode":"server"}`,
			configfile.GraphFormatMarkerFile: marker,
		}},
		{name: "marker beside dependency metadata", marker: true, files: map[string]string{
			configfile.ConfigFileName:        `{"backend":"dolt","graph_mode":"dependency"}`,
			configfile.GraphFormatMarkerFile: marker,
		}},
		{name: "marker beside unparseable metadata", marker: true, files: map[string]string{
			configfile.ConfigFileName:        `{"backend":`,
			configfile.GraphFormatMarkerFile: marker,
		}},
	}
	for _, layout := range layouts {
		for name, activate := range workspaceWriters() {
			t.Run(layout.name+"/"+name, func(t *testing.T) {
				root := t.TempDir()
				beadsDir := filepath.Join(root, ".beads")
				if err := os.MkdirAll(beadsDir, 0o700); err != nil {
					t.Fatal(err)
				}
				for file, content := range layout.files {
					if err := os.WriteFile(filepath.Join(beadsDir, file), []byte(content), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				before := workspaceTreeDigest(t, root)
				server := newRecordingServer(t)

				err := activate(t, beadsDir, server)
				var formatErr *bdhttp.UnsupportedWorkspaceFormatError
				if !errors.As(err, &formatErr) {
					t.Fatalf("%s = %v, want *bdhttp.UnsupportedWorkspaceFormatError", name, err)
				}
				if formatErr.BeadsDir != beadsDir || formatErr.GraphMode != layout.mode || formatErr.FormatMarker != layout.marker {
					t.Fatalf("refusal = %+v, want BeadsDir %q, GraphMode %q and FormatMarker %v", formatErr, beadsDir, layout.mode, layout.marker)
				}
				if !strings.Contains(err.Error(), configfile.BackendNotOpenedGuarantee) {
					t.Errorf("refusal lacks the data-safety guarantee: %v", err)
				}
				if after := workspaceTreeDigest(t, root); !maps.Equal(after, before) {
					t.Fatalf("%s changed the workspace: before %v, after %v", name, slices.Sorted(maps.Keys(before)), slices.Sorted(maps.Keys(after)))
				}
				server.wantNoRequests(t)
			})
		}
	}
}

// TestAttachConnectAndSaveTargetRefuseAHistoricalSQLiteWorkspace pins that
// this backend's activation refuses a historical SQLite workspace with the
// refusal bd's command admission prints there, before it writes anything:
// without it Attach migrated a pre-Dolt config.json, saved metadata.json
// selecting backend http over the SQLite database, and wrote the sidecar and
// .gitignore, after which bd itself no longer recognized the workspace as one
// to migrate. Connect refuses before it dials.
func TestAttachConnectAndSaveTargetRefuseAHistoricalSQLiteWorkspace(t *testing.T) {
	hermeticEnv(t)
	for _, layout := range legacyworkspace.HistoricalSQLiteLayouts() {
		for name, activate := range workspaceWriters() {
			t.Run(layout.Name+"/"+name, func(t *testing.T) {
				root := t.TempDir()
				beadsDir := filepath.Join(root, ".beads")
				layout.Write(t, beadsDir)
				before := workspaceTreeDigest(t, root)
				server := newRecordingServer(t)

				err := activate(t, beadsDir, server)
				if after := workspaceTreeDigest(t, root); !maps.Equal(after, before) {
					t.Fatalf("%s changed the workspace: before %v, after %v", name, slices.Sorted(maps.Keys(before)), slices.Sorted(maps.Keys(after)))
				}
				var legacyErr *bdhttp.LegacyWorkspaceError
				if !errors.As(err, &legacyErr) || err.Error() != legacyworkspace.HistoricalSQLiteRefusal {
					t.Fatalf("%s = %v, want *bdhttp.LegacyWorkspaceError with admission's refusal %q", name, err, legacyworkspace.HistoricalSQLiteRefusal)
				}
				server.wantNoRequests(t)
			})
		}
	}
}

// TestAttachAndConnectAdmitADependencyWorkspace is the other side of the
// fence: an explicit "dependency" graph_mode is the ordinary Issue workspace,
// so both doors activate it and keep the marker.
func TestAttachAndConnectAdmitADependencyWorkspace(t *testing.T) {
	hermeticEnv(t)
	for name, activate := range activationDoors {
		t.Run(name, func(t *testing.T) {
			beadsDir := t.TempDir()
			cfg := &configfile.Config{Backend: "dolt", Database: "beads.db", GraphMode: configfile.GraphModeDependency}
			if err := cfg.Save(beadsDir); err != nil {
				t.Fatalf("seed metadata.json: %v", err)
			}
			server := newRecordingServer(t)

			if err := activate(t, beadsDir, server); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			after, err := configfile.Load(beadsDir)
			if err != nil {
				t.Fatalf("reload metadata.json: %v", err)
			}
			if after.Backend != httpclient.Backend || after.GraphMode != configfile.GraphModeDependency {
				t.Errorf("metadata.json after %s = backend %q, graph_mode %q; want %q, %q", name, after.Backend, after.GraphMode, httpclient.Backend, configfile.GraphModeDependency)
			}
			if _, err := bdhttp.LoadTarget(beadsDir); err != nil {
				t.Errorf("LoadTarget after %s: %v", name, err)
			}
		})
	}
}
