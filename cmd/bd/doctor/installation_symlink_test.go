package doctor

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// doctorTestWriteName is the fixed probe filename the permission checks
// historically wrote into .beads/ (bd-1tmym4 / #7888).
const doctorTestWriteName = ".doctor-test-write"

// runPermissionProbes runs both permission checks against repo (which must
// resolve to beadsDir) and returns their verdicts.
func runPermissionProbes(t *testing.T, repo, beadsDir string) (DoctorCheck, DoctorCheck) {
	t.Helper()
	standalone := CheckPermissions(repo)
	shared := CheckPermissionsWithStore(repo, &SharedStore{beadsDir: beadsDir})
	return standalone, shared
}

func assertProbeOK(t *testing.T, name string, check DoctorCheck) {
	t.Helper()
	if check.Status != StatusOK {
		t.Errorf("%s: expected StatusOK, got %s: %s", name, check.Status, check.Message)
	}
}

// probeLeftovers returns any probe-created entries the checks left behind in
// beadsDir. A preexisting entry the checks must not touch (the exact
// .doctor-test-write name) is not a leftover; probes are suffixed temp names.
func probeLeftovers(t *testing.T, beadsDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(beadsDir)
	if err != nil {
		t.Fatal(err)
	}
	var leftovers []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".doctor-test-write-") {
			leftovers = append(leftovers, e.Name())
		}
	}
	return leftovers
}

// TestPermissionProbeAvoidsSymlink: a preexisting .doctor-test-write entry —
// file or symlink — is left untouched, writability is still reported, and the
// check removes only its own temporary probe (bd-1tmym4 / #7888).
func TestPermissionProbeAvoidsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}

	newWritableBeadsDir := func(t *testing.T) (repo, beadsDir, victim string) {
		t.Helper()
		repo = t.TempDir()
		beadsDir = filepath.Join(repo, ".beads")
		if err := os.Mkdir(beadsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		victim = filepath.Join(repo, "victim.txt")
		if err := os.WriteFile(victim, []byte("original\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return repo, beadsDir, victim
	}
	assertVictim := func(t *testing.T, victim, lane string) {
		t.Helper()
		got, err := os.ReadFile(victim)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "original\n" {
			t.Errorf("%s: probe wrote through the symlink: victim now %q", lane, got)
		}
	}

	t.Run("preexisting regular file", func(t *testing.T) {
		repo, beadsDir, _ := newWritableBeadsDir(t)
		preexisting := filepath.Join(beadsDir, doctorTestWriteName)
		if err := os.WriteFile(preexisting, []byte("user data\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		standalone, shared := runPermissionProbes(t, repo, beadsDir)
		assertProbeOK(t, "standalone", standalone)
		assertProbeOK(t, "shared", shared)

		got, err := os.ReadFile(preexisting)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "user data\n" {
			t.Errorf("preexisting .doctor-test-write was modified: %q", got)
		}
		if leftovers := probeLeftovers(t, beadsDir); len(leftovers) != 0 {
			t.Errorf("probe leftovers: %v", leftovers)
		}
	})

	t.Run("preexisting symlink standalone", func(t *testing.T) {
		repo, beadsDir, victim := newWritableBeadsDir(t)
		if err := os.Symlink(victim, filepath.Join(beadsDir, doctorTestWriteName)); err != nil {
			t.Fatal(err)
		}

		standalone, _ := runPermissionProbes(t, repo, beadsDir)
		assertProbeOK(t, "standalone", standalone)
		assertVictim(t, victim, "standalone")
	})

	t.Run("preexisting symlink shared", func(t *testing.T) {
		repo, beadsDir, victim := newWritableBeadsDir(t)
		if err := os.Symlink(victim, filepath.Join(beadsDir, doctorTestWriteName)); err != nil {
			t.Fatal(err)
		}

		_, shared := runPermissionProbes(t, repo, beadsDir)
		assertProbeOK(t, "shared", shared)
		assertVictim(t, victim, "shared")
	})
}

// TestPermissionProbeCleansUp: both checks remove their own temporary probe
// and leave nothing behind in .beads/ (bd-1tmym4 / #7888).
func TestPermissionProbeCleansUp(t *testing.T) {
	repo := t.TempDir()
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.Mkdir(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	standalone, shared := runPermissionProbes(t, repo, beadsDir)
	assertProbeOK(t, "standalone", standalone)
	assertProbeOK(t, "shared", shared)

	if leftovers := probeLeftovers(t, beadsDir); len(leftovers) != 0 {
		t.Errorf("probe leftovers: %v", leftovers)
	}
}