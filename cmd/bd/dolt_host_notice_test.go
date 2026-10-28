package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

// outrankedHostWorkspace writes a .beads directory holding metadata and
// yaml as metadata.json and config.yaml, loads its configuration as bd run
// inside it does, and returns it with its metadata.json.
func outrankedHostWorkspace(t *testing.T, metadata, yaml string) (string, *configfile.Config) {
	t.Helper()
	isolateForeignWorkspaceSignals(t, "")
	t.Setenv("BD_DOLT_HOST", "")
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"metadata.json": metadata, "config.yaml": yaml} {
		if err := os.WriteFile(filepath.Join(beadsDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("BEADS_DIR", beadsDir)
	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		t.Fatalf("configfile.Load: %v, %v", cfg, err)
	}
	return beadsDir, cfg
}

// TestOutrankedServerHostNoticePrintsOnce pins the GH#5961 notice: a
// server-mode workspace whose config.yaml dolt.host now outranks a different
// dolt_server_host in its tracked metadata.json is told so once per process,
// however often bd resolves its connection, naming both hosts, both files,
// and remedies that converge.
func TestOutrankedServerHostNoticePrintsOnce(t *testing.T) {
	beadsDir, cfg := outrankedHostWorkspace(t,
		`{"backend":"dolt","dolt_mode":"server","dolt_server_host":"127.0.0.1"}`,
		"dolt:\n  host: 192.0.2.50\n")

	var stderr bytes.Buffer
	warnOutrankedServerHost(&stderr, beadsDir, cfg)
	warnOutrankedServerHost(&stderr, beadsDir, cfg)

	out := stderr.String()
	if n := strings.Count(out, "now outranks"); n != 1 {
		t.Fatalf("notice printed %d times, want 1:\n%s", n, out)
	}
	for _, want := range []string{
		`.beads/config.yaml dolt.host "192.0.2.50" now outranks dolt_server_host "127.0.0.1" in .beads/metadata.json (GH#5961); bd dials "192.0.2.50".`,
		`To keep "192.0.2.50": remove "dolt_server_host" from .beads/metadata.json and commit the removal`,
		`To keep "127.0.0.1": run bd dolt set host 127.0.0.1 (it writes .beads/config.yaml dolt.host too).`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("notice lacks %q:\n%s", want, out)
		}
	}
}

// TestOutrankedServerHostNoticeStaysSilent pins the cases the GH#5961 notice
// leaves alone: the two files naming the same server, an explicit embedded
// workspace, and a metadata-only host, which still decides.
func TestOutrankedServerHostNoticeStaysSilent(t *testing.T) {
	for _, tc := range []struct{ name, metadata, yaml string }{
		{"same server", `{"backend":"dolt","dolt_mode":"server","dolt_server_host":"DB.example.com"}`, "dolt:\n  host: db.example.com\n"},
		{"explicit embedded", `{"backend":"dolt","dolt_mode":"embedded","dolt_server_host":"127.0.0.1"}`, "dolt:\n  host: 192.0.2.50\n"},
		{"metadata host only", `{"backend":"dolt","dolt_mode":"server","dolt_server_host":"127.0.0.1"}`, "dolt:\n  port: 3307\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir, cfg := outrankedHostWorkspace(t, tc.metadata, tc.yaml)
			var stderr bytes.Buffer
			warnOutrankedServerHost(&stderr, beadsDir, cfg)
			if stderr.Len() != 0 {
				t.Errorf("notice printed, want none:\n%s", stderr.String())
			}
		})
	}
}
