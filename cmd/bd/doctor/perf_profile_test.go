package doctor

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
)

// plantProfileEntry puts a live symlink, a dangling symlink or a regular file
// at path and returns a check that it and any link target are unchanged.
func plantProfileEntry(t *testing.T, path, kind string) func() {
	t.Helper()
	sentinel := filepath.Join(t.TempDir(), "sentinel")
	switch kind {
	case "symlink", "dangling":
		if kind == "symlink" {
			if err := os.WriteFile(sentinel, []byte("sentinel"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(sentinel, path); err != nil {
			t.Fatal(err)
		}
	case "file":
		if err := os.WriteFile(path, []byte("user data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return func() {
		t.Helper()
		switch kind {
		case "symlink":
			if data, err := os.ReadFile(sentinel); err != nil || string(data) != "sentinel" {
				t.Errorf("sentinel changed: %q, %v", data, err)
			}
		case "dangling":
			if _, err := os.Lstat(sentinel); !os.IsNotExist(err) {
				t.Errorf("dangling target created: %v", err)
			}
		case "file":
			if data, err := os.ReadFile(path); err != nil || string(data) != "user data" {
				t.Errorf("planted file changed: %q, %v", data, err)
			}
		}
		if kind != "file" {
			if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Errorf("planted link changed: %v, %v", info, err)
			}
		}
	}
}

// assertFreshProfile checks that got is a new non-empty regular file beside
// requested, distinct from it, named after it.
func assertFreshProfile(t *testing.T, requested, got string) {
	t.Helper()
	if got == requested {
		t.Fatalf("profile reused the predictable path %s", requested)
	}
	if filepath.Dir(got) != filepath.Dir(requested) {
		t.Errorf("profile %s not beside %s", got, requested)
	}
	stem := strings.TrimSuffix(filepath.Base(requested), ".prof")
	if base := filepath.Base(got); !strings.HasPrefix(base, stem) || !strings.HasSuffix(base, ".prof") {
		t.Errorf("profile name %q does not keep %q", base, filepath.Base(requested))
	}
	info, err := os.Lstat(got)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		t.Errorf("profile not a written regular file: %v, %v", info, err)
	}
}

func TestCPUProfileAvoidsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs privileges on windows")
	}
	for _, kind := range []string{"symlink", "dangling", "file"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "beads-dolt-perf-test.prof")
			unchanged := plantProfileEntry(t, path, kind)
			f, err := startCPUProfile(path)
			if err != nil {
				t.Fatal(err)
			}
			stopCPUProfile(f)
			unchanged()
			assertFreshProfile(t, path, f.Name())
		})
	}
}

func TestCPUProfileCreateErrors(t *testing.T) {
	dir := t.TempDir()
	if f, err := startCPUProfile(filepath.Join(dir, "missing", "profile.prof")); err == nil {
		stopCPUProfile(f)
		t.Fatal("accepted missing parent")
	}
	f, err := startCPUProfile(filepath.Join(dir, "profile.prof"))
	if err != nil {
		t.Fatal(err)
	}
	defer stopCPUProfile(f)
	if other, err := startCPUProfile(filepath.Join(dir, "profile.prof")); err == nil {
		_ = other.Close()
		t.Error("started second CPU profile")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("failed profile left temporary files: %v", entries)
	}
}

// TestDoltPerfReportsActualProfilePath pins the --perf wiring: with an entry
// planted at the predictable name, the diagnostics profile into a distinct
// file and report that file, not the requested name.
func TestDoltPerfReportsActualProfilePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs privileges on windows")
	}
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.Local)
	orig := perfProfileNow
	perfProfileNow = func() time.Time { return fixed }
	t.Cleanup(func() { perfProfileNow = orig })

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A port nothing listens on: the run profiles, then stops at the server check.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	cfg := configfile.DefaultConfig()
	cfg.Backend = configfile.BackendDolt
	cfg.DoltMode = configfile.DoltModeServer
	cfg.DoltServerHost = "127.0.0.1"
	cfg.DoltServerPort = port
	cfg.DoltDatabase = "doctest_perf_profile"
	if err := cfg.Save(beadsDir); err != nil {
		t.Fatal(err)
	}

	t.Chdir(tmpDir)
	requested := filepath.Join(tmpDir, "beads-dolt-perf-2026-01-02-030405.prof")
	unchanged := plantProfileEntry(t, requested, "symlink")

	metrics, _ := RunDoltPerformanceDiagnostics(tmpDir, true)
	if metrics == nil || metrics.ProfilePath == "" {
		t.Fatalf("no profile reported: %+v", metrics)
	}
	unchanged()
	got := metrics.ProfilePath
	if !filepath.IsAbs(got) {
		got = filepath.Join(tmpDir, got)
	}
	assertFreshProfile(t, requested, got)
}
