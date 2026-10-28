package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

// TestOutrankedServerHostNoticeClearsStderrOnce pins GH#5961's mismatch
// notice: a server-mode workspace whose .beads/metadata.json names 127.0.0.1
// and whose .beads/config.yaml names 192.0.2.50 gets exactly ONE notice per
// process, even though bd resolves its connection on every command — and the
// notice names both hosts, both files, and a remedy that converges
// (`bd dolt set host`, which writes .beads/config.yaml dolt.host too, AC-7).
func TestOutrankedServerHostNoticeClearsStderrOnce(t *testing.T) {
	for _, name := range []string{"BEADS_DOLT_SERVER_HOST", "BD_DOLT_HOST"} {
		t.Setenv(name, "")
	}
	// A fresh HOME keeps the user-level config.yaml out of the notice.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"),
		[]byte("dolt.host: 192.0.2.50\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := configfile.Config{
		Backend:        configfile.BackendDolt,
		DoltMode:        configfile.DoltModeServer,
		DoltServerHost: "127.0.0.1",
	}
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	t.Cleanup(config.ResetForTesting)

	// Resolving the connection twice in one process is what a bd command
	// sequence does; only the first resolution may print the notice.
	var buf strings.Builder
	writeOutrankedServerHostWarning(&buf, beadsDir, &cfg)
	writeOutrankedServerHostWarning(&buf, beadsDir, &cfg)
	notice := buf.String()

	if got := strings.Count(notice, "GH#5961"); got != 1 {
		t.Fatalf("notice printed %d times in one process, want exactly 1:\n%s", got, notice)
	}
	for _, want := range []string{
		"192.0.2.50", // the yaml host, the one bd dials
		"127.0.0.1",  // the outranked metadata host
		".beads/metadata.json",
		"config.yaml",
		"bd dolt set host 127.0.0.1", // the remedy that keeps the metadata host, converging
	} {
		if !strings.Contains(notice, want) {
			t.Errorf("notice does not name %q:\n%s", want, notice)
		}
	}
}
