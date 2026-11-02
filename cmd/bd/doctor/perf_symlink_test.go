package doctor

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestCPUProfileAvoidsSymlink: profiling creates its profile file
// exclusively under a unique name, leaving any entry at the predictable
// basename — including a symlink and its target — untouched, and reports the
// file that was actually created (bd-1tmym4 / #7888).
func TestCPUProfileAvoidsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim.prof")
	if err := os.WriteFile(victim, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The name the pre-fix code created with os.Create, predictable to the
	// second. A committed symlink there redirected the gzip profile stream
	// onto its target.
	base := "beads-dolt-perf-2026-01-02-030405"
	predicted := filepath.Join(dir, base+".prof")
	if err := os.Symlink(victim, predicted); err != nil {
		t.Fatal(err)
	}

	f, err := startCPUProfile(dir, base)
	if err != nil {
		t.Fatal(err)
	}
	created := f.Name()
	stopCPUProfile(f)

	if created == predicted {
		t.Errorf("profile was created at the predictable name %s", created)
	}
	if !strings.HasPrefix(filepath.Base(created), base+"-") || !strings.HasSuffix(created, ".prof") {
		t.Errorf("profile name %q does not extend the requested basename", created)
	}
	fi, err := os.Lstat(created)
	if err != nil {
		t.Fatalf("reported path %s was not created: %v", created, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		t.Errorf("profile at %s is not a regular file: %v", created, fi.Mode())
	}
	// The predicted-name symlink and its target must be exactly as before.
	if got, err := os.ReadFile(victim); err != nil || string(got) != "original\n" {
		t.Errorf("symlink target was written through: %q (%v)", got, err)
	}
	if fi, err := os.Lstat(predicted); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("predicted-name symlink was replaced: %v (%v)", fi, err)
	}
}

// TestCPUProfileCreateErrors: a directory that cannot host the temp file
// fails the start, and a profile-start failure cleans up its own temp file.
// Profiles are started and stopped sequentially: pprof allows one active
// CPU profile per process (bd-1tmym4 / #7888).
func TestCPUProfileCreateErrors(t *testing.T) {
	t.Run("unusable directory", func(t *testing.T) {
		dir := t.TempDir()
		missing := filepath.Join(dir, "no-such-dir")
		if _, err := startCPUProfile(missing, "beads-dolt-perf"); err == nil {
			t.Error("expected an error for a nonexistent profile directory")
		}
	})

	t.Run("already profiling cleans up temp file", func(t *testing.T) {
		dir := t.TempDir()
		first, err := startCPUProfile(dir, "beads-dolt-perf")
		if err != nil {
			t.Fatal(err)
		}
		defer stopCPUProfile(first)

		second, err := startCPUProfile(dir, "beads-dolt-perf")
		if err == nil {
			stopCPUProfile(second)
			t.Fatal("expected an error while another CPU profile is active")
		}
		if second != nil {
			t.Errorf("expected nil file on failure, got %v", second.Name())
		}

		// The failed start must not leave its temp file behind.
		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if len(entries) != 1 {
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Errorf("failed start left %d files behind: %v", len(entries), names)
		}
	})
}