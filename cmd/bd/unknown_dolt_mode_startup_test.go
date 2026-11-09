package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// unknownDoltModeWantSubstrings returns the guidance substrings every
// unknown-dolt_mode rejection must carry: the field, the offending value,
// the file it was read from, the supported vocabulary, the data-safety
// guarantee and the upgrade-or-correct remedy.
func unknownDoltModeWantSubstrings(mode string) []string {
	return []string{
		"dolt_mode",
		mode,
		"not recognized",
		"embedded",
		"server",
		"proxied-server",
		configfile.BackendNotOpenedGuarantee,
	}
}

// TestCommandStartupRejectsUnknownDoltModeBeforeLocalWrites is the
// deterministic regression for GH#5104's silent failure: a workspace whose
// metadata.json names a dolt_mode this build does not recognize was routed
// to the embedded store, read an out-of-tree database as "No issues found."
// and exited 0. Admission must refuse it with the mode's value, the
// metadata file, the supported modes and the no-storage-open guarantee,
// without writing storage or version tracking.
func TestCommandStartupRejectsUnknownDoltModeBeforeLocalWrites(t *testing.T) {
	bd := buildBDForInitTests(t)
	root := t.TempDir()
	beadsDir := filepath.Join(root, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatalf("create .beads: %v", err)
	}
	if err := (&configfile.Config{
		Backend:      configfile.BackendDolt,
		DoltMode:     "future-mode-1-4",
		DoltDatabase: "beads",
	}).Save(beadsDir); err != nil {
		t.Fatalf("save unknown-mode metadata: %v", err)
	}

	for _, args := range [][]string{{"list", "--json"}, {"stats"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cmd := exec.Command(bd, args...)
			cmd.Dir = root
			cmd.Env = append(removedBackendTestEnv(beadsDir), "BD_DISABLE_METRICS=1")
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("bd %s unexpectedly opened an unknown dolt_mode workspace:\n%s",
					strings.Join(args, " "), out)
			}
			message := string(out)
			if strings.Contains(message, "No issues found") {
				t.Fatalf("silent empty-tracker failure of GH#5104: bd %s reported an empty tracker for an unrecognized dolt_mode instead of refusing:\n%s",
					strings.Join(args, " "), out)
			}
			for _, want := range unknownDoltModeWantSubstrings("future-mode-1-4") {
				if !strings.Contains(message, want) {
					t.Errorf("unknown-mode error for bd %s missing %q:\n%s",
						strings.Join(args, " "), want, out)
				}
			}
			if !strings.Contains(message, "metadata.json") {
				t.Errorf("unknown-mode error for bd %s must name the metadata file it read:\n%s",
					strings.Join(args, " "), out)
			}

			for _, name := range []string{"embeddeddolt", "dolt", "beads.db", ".local_version", "proxieddb"} {
				if _, statErr := os.Stat(filepath.Join(beadsDir, name)); !os.IsNotExist(statErr) {
					t.Fatalf("unknown dolt_mode created %s (stat error: %v)", name, statErr)
				}
			}
		})
	}
}
