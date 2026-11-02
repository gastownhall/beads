//go:build cgo

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/configfile"
)

// readInitStoreSwitchFixture returns the bytes of the workspace files a
// refused store switch must leave untouched.
func readInitStoreSwitchFixture(t *testing.T, beadsDir string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	for _, name := range []string{"metadata.json", "config.yaml"} {
		data, err := os.ReadFile(filepath.Join(beadsDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		files[name] = data
	}
	return files
}

// TestEmbeddedInitReinitLocalRefusesDatabaseRepoint is the residual #3495
// defect: --reinit-local with an explicit --database other than the recorded
// one moved the workspace onto a new, empty database without confirmation
// whenever the old one held zero issues, abandoning its database-stored
// configuration (types.custom). The switch must be refused without the
// destroy token, before either database is opened.
func TestEmbeddedInitReinitLocalRefusesDatabaseRepoint(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt init tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "fi", "--skip-hooks", "--skip-agents")
	bdRunOK(t, bd, dir, "config", "set", "types.custom", "molecule,convoy")
	before := readInitStoreSwitchFixture(t, beadsDir)

	cmd := exec.Command(bd, "init", "--quiet", "--reinit-local", "--prefix", "fi", "--database", "other", "--non-interactive", "--skip-hooks", "--skip-agents")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != ExitDestroyTokenMissing {
		t.Fatalf("reinit onto another database without a destroy token: want exit %d, got %v\n%s", ExitDestroyTokenMissing, err, out)
	}

	after := readInitStoreSwitchFixture(t, beadsDir)
	for name, data := range before {
		if string(after[name]) != string(data) {
			t.Errorf("%s changed by a refused store switch:\nbefore:\n%s\nafter:\n%s", name, data, after[name])
		}
	}
	requireNoFile(t, filepath.Join(beadsDir, "embeddeddolt", "other"))
	if got := readBack(t, beadsDir, "fi", "types.custom", false); got != "molecule,convoy" {
		t.Errorf("types.custom in the recorded database after a refused switch: got %q, want %q", got, "molecule,convoy")
	}
}

// TestEmbeddedInitReinitLocalDestroyTokenAuthorizesRepoint: the matching
// destroy token authorizes the switch once. The workspace then records the
// new database, and the old one stays in place with its configuration, which
// is not copied into the new store.
func TestEmbeddedInitReinitLocalDestroyTokenAuthorizesRepoint(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt init tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "fi", "--skip-hooks", "--skip-agents")
	bdRunOK(t, bd, dir, "config", "set", "types.custom", "molecule,convoy")

	cmd := exec.Command(bd, "init", "--quiet", "--reinit-local", "--prefix", "fi", "--database", "other",
		"--destroy-token", FormatDestroyToken("fi"), "--non-interactive", "--skip-hooks", "--skip-agents")
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("reinit onto another database with the destroy token: %v\n%s", err, out)
	}

	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		t.Fatalf("load metadata.json after the switch: cfg=%v err=%v", cfg, err)
	}
	if cfg.DoltDatabase != "other" {
		t.Errorf("recorded database after an authorized switch: got %q, want %q", cfg.DoltDatabase, "other")
	}
	requireFile(t, filepath.Join(beadsDir, "embeddeddolt", "fi"))
	requireFile(t, filepath.Join(beadsDir, "embeddeddolt", "other"))
	if got := readBack(t, beadsDir, "fi", "types.custom", false); got != "molecule,convoy" {
		t.Errorf("types.custom in the database left behind: got %q, want %q", got, "molecule,convoy")
	}
}
